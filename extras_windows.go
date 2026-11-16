//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	pOpenSCManager    = advapi32.NewProc("OpenSCManagerW")
	pOpenService      = advapi32.NewProc("OpenServiceW")
	pEnumServicesEx   = advapi32.NewProc("EnumServicesStatusExW")
	pControlService   = advapi32.NewProc("ControlService")
	pStartService     = advapi32.NewProc("StartServiceW")
	pCloseServiceHdl  = advapi32.NewProc("CloseServiceHandle")
	pRegisterHotKey   = user32.NewProc("RegisterHotKey")
	pUnregisterHotKey = user32.NewProc("UnregisterHotKey")
)

const wmHotkey = 0x0312

// ---------------------------------------------------------------- services

type svcInfo struct {
	Name    string
	Display string
}

type enumSvc struct {
	Name, Display *uint16
	Status        [9]uint32
}

func ptrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 && n < 1024 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

// runningServices lists the Win32 services that are running now.
func runningServices() ([]svcInfo, error) {
	scm, _, err := pOpenSCManager.Call(0, 0, 0x0001|0x0004)
	if scm == 0 {
		return nil, err
	}
	defer pCloseServiceHdl.Call(scm)
	var out []svcInfo
	buf := make([]byte, 256*1024)
	var resume uint32
	for i := 0; i < 16; i++ {
		var needed, count uint32
		r, _, e := pEnumServicesEx.Call(scm, 0, 0x30, 1, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
			uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&resume)), 0)
		more := r == 0 && e == syscall.Errno(234)
		if r == 0 && !more {
			return out, e
		}
		for k := uint32(0); k < count; k++ {
			s := (*enumSvc)(unsafe.Add(unsafe.Pointer(&buf[0]), uintptr(k)*unsafe.Sizeof(enumSvc{})))
			out = append(out, svcInfo{Name: ptrToString(s.Name), Display: ptrToString(s.Display)})
		}
		if !more {
			break
		}
	}
	return out, nil
}

func openService(name string, access uintptr) (uintptr, uintptr, error) {
	scm, _, err := pOpenSCManager.Call(0, 0, 0x0001)
	if scm == 0 {
		return 0, 0, err
	}
	h, _, err := pOpenService.Call(scm, uintptr(unsafe.Pointer(u16(name))), access)
	if h == 0 {
		pCloseServiceHdl.Call(scm)
		return 0, 0, err
	}
	return scm, h, nil
}

func stopService(name string) error {
	scm, h, err := openService(name, 0x0020|0x0004)
	if err != nil {
		return err
	}
	defer pCloseServiceHdl.Call(scm)
	defer pCloseServiceHdl.Call(h)
	var st [7]uint32
	if r, _, e := pControlService.Call(h, 1, uintptr(unsafe.Pointer(&st[0]))); r == 0 {
		return e
	}
	return nil
}

func startService(name string) error {
	scm, h, err := openService(name, 0x0010)
	if err != nil {
		return err
	}
	defer pCloseServiceHdl.Call(scm)
	defer pCloseServiceHdl.Call(h)
	if r, _, e := pStartService.Call(h, 0, 0); r == 0 && e != syscall.Errno(1056) { // already running
		return e
	}
	return nil
}

func (a *app) servicePatterns() []string {
	var pats []string
	if a.cfg.PauseServices {
		pats = append(pats, a.cfg.Services...)
	}
	if a.cfg.PauseVPN {
		pats = append(pats, a.cfg.VPN...)
	}
	return pats
}

// pauseServices stops the listed background services for the time of the
// blackout mode and remembers which of them were running.
func (a *app) pauseServices() {
	pats := a.servicePatterns()
	if !a.elevated || len(pats) == 0 || a.svcBusy {
		return
	}
	a.svcBusy = true
	a.work(func() {
		var stopped, notes []string
		list, err := runningServices()
		if err != nil {
			notes = append(notes, "list: "+err.Error())
		}
		for _, s := range list {
			if !MatchService(s.Name, s.Display, pats) {
				continue
			}
			if err := stopService(s.Name); err != nil {
				notes = append(notes, fmt.Sprintf("%s: %v", s.Name, err))
				continue
			}
			stopped = append(stopped, s.Name)
		}
		a.post(func() {
			a.svcBusy = false
			a.log("services stopped: %q, problems: %q", stopped, notes)
			for _, n := range stopped {
				if !inList(a.st.Services, n) {
					a.st.Services = append(a.st.Services, n)
				}
			}
			a.saveState()
			if a.st.Mode != modeBlackout {
				a.resumeServices()
			} else if len(stopped) > 0 {
				a.balloon("Фоновые службы остановлены",
					fmt.Sprintf("Остановлено служб: %d. Они запустятся снова, когда режим выключится.", len(stopped)), false)
			}
		})
	})
}

func (a *app) resumeServices() {
	if len(a.st.Services) == 0 || a.svcBusy {
		return
	}
	names := a.st.Services
	a.st.Services = nil
	a.saveState()
	a.work(func() {
		var notes []string
		for _, n := range names {
			if err := startService(n); err != nil {
				notes = append(notes, fmt.Sprintf("%s: %v", n, err))
			}
		}
		a.post(func() { a.log("services started again: %q, problems: %q", names, notes) })
	})
}

// ---------------------------------------------------------------- radios

const radioPre = "Add-Type -AssemblyName System.Runtime.WindowsRuntime; " +
	"$m = ([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object { $_.Name -eq 'AsTask' -and " +
	"$_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation`1' })[0]; " +
	"function Await($t, $rt) { $n = $m.MakeGenericMethod($rt).Invoke($null, @($t)); $n.Wait(-1) | Out-Null; $n.Result }; " +
	"[Windows.Devices.Radios.Radio,Windows.System.Devices,ContentType=WindowsRuntime] | Out-Null; " +
	"[Windows.Devices.Radios.RadioAccessStatus,Windows.System.Devices,ContentType=WindowsRuntime] | Out-Null; " +
	"Await ([Windows.Devices.Radios.Radio]::RequestAccessAsync()) ([Windows.Devices.Radios.RadioAccessStatus]) | Out-Null; " +
	"$rs = Await ([Windows.Devices.Radios.Radio]::GetRadiosAsync()) ([System.Collections.Generic.IReadOnlyList[Windows.Devices.Radios.Radio]]); " +
	"foreach ($r in $rs) { [string]$r.Kind + '=' + [string]$r.State }; "

func parseKVs(out string) map[string]string {
	m := map[string]string{}
	for _, f := range strings.Fields(out) {
		if i := strings.Index(f, "="); i > 0 {
			m[f[:i]] = f[i+1:]
		}
	}
	return m
}

// radioSet switches a radio ("Bluetooth" or "WiFi") and returns its state
// before the change and the answer of Windows ("Allowed" on success).
func radioSet(kind string, on bool) (prev, status, raw string) {
	want := "Off"
	if on {
		want = "On"
	}
	out, _ := runPS(radioPre + "foreach ($r in $rs) { if ([string]$r.Kind -eq '" + kind + "' -and [string]$r.State -ne '" + want + "') { " +
		"'set=' + [string](Await ($r.SetStateAsync('" + want + "')) ([Windows.Devices.Radios.RadioAccessStatus])) } }")
	m := parseKVs(out)
	return m[kind], m["set"], strings.Join(strings.Fields(out), " ")
}

type radioCtl struct {
	busy bool
	skip bool // nothing to switch off this time
}

// syncRadios brings Bluetooth and Wi-Fi in line with the current mode:
// Bluetooth is off in blackout mode, Wi-Fi is off while a film plays in it.
func (a *app) syncRadios() {
	blackout := a.st.Mode == modeBlackout
	film := a.track != nil && a.track.Kind == modeFilm
	a.syncRadio("Bluetooth", blackout && a.cfg.BTOff, &a.st.BTOff, &a.bt)
	a.syncRadio("WiFi", blackout && film && a.cfg.FilmWifiOff, &a.st.WifiOff, &a.wifi)
}

func (a *app) syncRadio(kind string, wantOff bool, offByUs *bool, c *radioCtl) {
	if !wantOff {
		c.skip = false
	}
	if c.busy || wantOff == *offByUs || (wantOff && c.skip) {
		return
	}
	c.busy = true
	a.work(func() {
		prev, status, raw := radioSet(kind, !wantOff)
		a.post(func() {
			c.busy = false
			a.log("radio %s: off=%v, answer: %s", kind, wantOff, raw)
			if !wantOff {
				*offByUs = false
			} else if prev == "On" && status == "Allowed" {
				*offByUs = true
				if kind == "WiFi" {
					a.balloon("Wi-Fi выключен на время кино", "Он включится сам, когда вы закроете плеер.", false)
				}
			} else {
				c.skip = true
			}
			a.saveState()
			a.syncRadios()
		})
	})
}

// ---------------------------------------------------------------- video card

const gpuStateScript = "$d = Get-PnpDevice -Class Display -PresentOnly -ErrorAction SilentlyContinue | " +
	"Where-Object { $_.FriendlyName -match 'NVIDIA' } | Select-Object -First 1; if (-not $d) { 'gpu=none'; exit }; " +
	"$p = (Get-PnpDeviceProperty -InstanceId $d.InstanceId -KeyName DEVPKEY_Device_PowerData -ErrorAction SilentlyContinue).Data; " +
	"if ($p -is [byte[]]) { 'bytes=' + ($p -join ',') } else { 'data=' + (($p | Out-String) -replace '\\s+', '_') }"

func smiExe() string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "nvidia-smi.exe")
}

// gpuHolders asks the NVIDIA driver which programs keep the video card busy.
// The question itself wakes the card up, so it is asked rarely.
func gpuHolders() ([]smiProc, string) {
	if _, err := os.Stat(smiExe()); err != nil {
		return nil, "nvidia-smi not found"
	}
	out, err := runHidden(smiExe())
	if err != nil {
		return nil, "nvidia-smi: " + err.Error()
	}
	note := ""
	if o, err := runHidden(smiExe(), "--query-gpu=power.draw,utilization.gpu,memory.used", "--format=csv,noheader"); err == nil {
		note = o
	}
	return ParseSmi(out), note
}

func (a *app) gpuLine() string {
	switch a.gpu {
	case gpuAsleep:
		return "Видеокарта NVIDIA спит"
	case gpuAbsent:
		return "Видеокарта NVIDIA отключена"
	case gpuAwake:
		if len(a.gpuUsers) > 0 {
			return "Видеокарта NVIDIA работает: " + strings.Join(a.gpuUserNames(), ", ")
		}
		return "Видеокарта NVIDIA работает"
	}
	return ""
}

func (a *app) gpuUserNames() []string {
	var names []string
	for _, p := range a.gpuUsers {
		if !inList(names, p.Name) {
			names = append(names, p.Name)
		}
	}
	return names
}

// watchGPU runs in blackout mode: it checks whether the NVIDIA card sleeps
// and, when it stays awake, finds out once which programs hold it.
func (a *app) watchGPU() {
	if !a.cfg.GPUWatch || a.st.Mode != modeBlackout || a.gpuBusy {
		return
	}
	a.gpuTicks++
	if !GPUCheckDue(a.gpuTicks, a.gpuCalm) {
		return
	}
	a.gpuBusy = true
	a.work(func() {
		out, _ := runPS(gpuStateScript)
		state := ParseGPUState(out)
		a.post(func() {
			a.gpuBusy = false
			if state != a.gpu || !a.gpuLogged {
				a.log("video card: state=%q, answer: %s", state, strings.Join(strings.Fields(out), " "))
				a.gpuLogged = true
			}
			a.gpu = state
			if state == gpuAsleep || state == gpuAbsent {
				a.gpuAwake, a.gpuUsers = 0, nil
				a.gpuCalm++
				return
			}
			a.gpuCalm = 0
			a.gpuAwake++
			if a.gpuAsked || a.st.Mode != modeBlackout || (state == gpuAwake && a.gpuAwake < 2) {
				return
			}
			a.gpuAsked, a.gpuBusy = true, true
			a.work(func() {
				users, note := gpuHolders()
				a.post(func() {
					a.gpuBusy = false
					a.gpuTicks = 7 // the question woke the card up, give it time to fall asleep
					var keep []smiProc
					for _, u := range users {
						if !protectedProcs[u.Name] {
							keep = append(keep, u)
						}
					}
					a.gpuUsers = keep
					a.log("video card users: %v, all: %v, card: %s", a.gpuUserNames(), users, note)
					if len(keep) > 0 {
						if state == gpuUnknown {
							a.gpu = gpuAwake
						}
						a.balloon("Видеокарта NVIDIA не спит",
							"Её держат: "+strings.Join(a.gpuUserNames(), ", ")+". Это лишние 5-15 Вт. В окне программы, «Обзор», им можно назначить встроенную графику.", true)
					}
				})
			})
		})
	})
}

const gpuPrefKey = `Software\Microsoft\DirectX\UserGpuPreferences`

const gpuPrefIntegrated = "GpuPreference=1;"

func openGPUPrefs() (uintptr, bool) {
	var h uintptr
	r, _, _ := pRegCreateKeyEx.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(u16(gpuPrefKey))), 0, 0, 0,
		0x0003, 0, uintptr(unsafe.Pointer(&h)), 0)
	return h, r == 0
}

// readGPUPref returns the graphics choice Windows keeps for a program.
func readGPUPref(exePath string) (string, bool) {
	h, ok := openGPUPrefs()
	if !ok {
		return "", false
	}
	defer pRegCloseKey.Call(h)
	buf := make([]uint16, 256)
	var typ uint32
	size := uint32(len(buf) * 2)
	r, _, _ := pRegQueryValueEx.Call(h, uintptr(unsafe.Pointer(u16(exePath))), 0,
		uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 || typ != 1 {
		return "", false
	}
	return syscall.UTF16ToString(buf[:size/2]), true
}

func writeGPUPref(exePath, val string) bool {
	h, ok := openGPUPrefs()
	if !ok {
		return false
	}
	defer pRegCloseKey.Call(h)
	u := syscall.StringToUTF16(val)
	r, _, _ := pRegSetValueEx.Call(h, uintptr(unsafe.Pointer(u16(exePath))), 0, 1,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)*2))
	return r == 0
}

func deleteGPUPref(exePath string) bool {
	h, ok := openGPUPrefs()
	if !ok {
		return false
	}
	defer pRegCloseKey.Call(h)
	r, _, _ := pRegDeleteValue.Call(h, uintptr(unsafe.Pointer(u16(exePath))))
	return r == 0
}

// preferIntegrated tells Windows to run a program on the built-in graphics.
func preferIntegrated(exePath string) bool {
	return writeGPUPref(exePath, gpuPrefIntegrated)
}

// liteGPUApply sends the known games to the built-in graphics for the time
// of the outage and remembers what was chosen before. A game the user has
// already put on the built-in graphics is left alone.
func (a *app) liteGPUApply() {
	for _, p := range a.st.Games {
		if _, done := a.st.GPUPrefs[p]; done {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		prev, _ := readGPUPref(p)
		if strings.Contains(prev, "GpuPreference=1") {
			continue
		}
		if !writeGPUPref(p, gpuPrefIntegrated) {
			a.log("built-in graphics for %s: not set", p)
			continue
		}
		if a.st.GPUPrefs == nil {
			a.st.GPUPrefs = map[string]string{}
		}
		a.st.GPUPrefs[p] = prev
		a.log("built-in graphics for the outage: %s (was %q)", p, prev)
	}
}

// liteGPURestore puts the graphics choice of the games back.
func (a *app) liteGPURestore() {
	for p, prev := range a.st.GPUPrefs {
		cur, _ := readGPUPref(p)
		ok := true
		switch {
		case cur != gpuPrefIntegrated: // changed by the user in the meantime
		case prev == "":
			ok = deleteGPUPref(p)
		default:
			ok = writeGPUPref(p, prev)
		}
		a.log("graphics choice restored: %s -> %q ok=%v", p, prev, ok)
	}
	a.st.GPUPrefs = nil
}

// ---------------------------------------------------------------- Lenovo charge limit

const ioctlChargeMode = 0x831020F8

// energyCall talks to the Lenovo energy driver. Code 0xFF reads the state,
// 3 switches the charge limit (conservation mode) on, 5 switches it off.
func energyCall(code uint32) (uint32, error) {
	h, err := syscall.CreateFile(syscall.StringToUTF16Ptr(`\\.\EnergyDrv`), syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(h)
	var out, n uint32
	err = syscall.DeviceIoControl(h, ioctlChargeMode, (*byte)(unsafe.Pointer(&code)), 4, (*byte)(unsafe.Pointer(&out)), 4, &n, nil)
	return out, err
}

// chargeLimit reads whether the Lenovo charge limit is on.
func chargeLimit() (on bool, raw uint32, err error) {
	raw, err = energyCall(0xFF)
	return raw&0x20 != 0, raw, err
}

// setChargeLimit switches the limit and reports the state read back.
func setChargeLimit(on bool) (now bool, note string) {
	code := uint32(5)
	if on {
		code = 3
	}
	_, err := energyCall(code)
	time.Sleep(700 * time.Millisecond)
	now, raw, err2 := chargeLimit()
	return now, fmt.Sprintf("set=%v err=%v, read=0x%08x err=%v", on, err, raw, err2)
}

func (a *app) probeChargeLimit() {
	a.work(func() {
		on, raw, err := chargeLimit()
		a.post(func() {
			a.limitKnown, a.limitOn = err == nil, on
			a.log("lenovo charge limit: on=%v raw=0x%08x err=%v", on, raw, err)
		})
	})
}

// toggleChargeLimit lifts the 80% limit so that the battery is full before
// an outage, or puts it back.
func (a *app) toggleChargeLimit() {
	if !a.limitKnown {
		return
	}
	want := !a.limitOn
	a.work(func() {
		now, note := setChargeLimit(want)
		a.post(func() {
			a.log("lenovo charge limit: %s", note)
			a.limitOn = now
			if now != want {
				a.st.LimitLifted = false
				a.saveState()
				a.balloon("Лимит зарядки не переключился", "Переключите режим сбережения батареи в Lenovo Vantage. Подробности в журнале.", true)
				return
			}
			a.st.LimitLifted = !now
			a.saveState()
			if now {
				a.balloon("Лимит зарядки возвращён", "Батарея снова заряжается до 75-80%.", false)
			} else {
				a.balloon("Лимит зарядки снят", "Батарея зарядится до 100%. Лимит вернётся сам, когда вы выключите режим отключения.", false)
			}
		})
	})
}

// restoreChargeLimit puts the limit back after an outage.
func (a *app) restoreChargeLimit() {
	if !a.st.LimitLifted {
		return
	}
	a.st.LimitLifted = false
	a.work(func() {
		now, note := setChargeLimit(true)
		a.post(func() {
			a.limitOn = now
			a.log("lenovo charge limit restored: %s", note)
		})
	})
}

// ---------------------------------------------------------------- small things

func (a *app) setHotkey() {
	pUnregisterHotKey.Call(a.hwnd, 1)
	if !a.cfg.Hotkey {
		return
	}
	r, _, err := pRegisterHotKey.Call(a.hwnd, 1, 0x0001|0x0002|0x4000, 'B') // Ctrl+Alt+B
	if r == 0 {
		a.log("hotkey Ctrl+Alt+B is taken by another program: %v", err)
	}
}

// checkVLC warns when VLC decodes video on the processor.
func (a *app) checkVLC() {
	data, err := os.ReadFile(filepath.Join(os.Getenv("APPDATA"), "vlc", "vlcrc"))
	if err != nil {
		return
	}
	off := VLCHwOff(string(data))
	a.log("vlc hardware decoding switched off: %v", off)
	if off {
		a.balloon("VLC декодирует видео процессором",
			"Включите: Инструменты, Настройки, Ввод / кодеки, «Аппаратное ускорение декодирования»: Автоматически. Расход упадёт в разы.", true)
	}
}

// lowBatteryWarn reminds about the charge at 30% and at 10%.
func (a *app) lowBatteryWarn() {
	if !a.cfg.LowWarn || !a.batOK {
		return
	}
	pct := a.percent()
	if a.onAC() || pct > 32 {
		a.warned = 0
		return
	}
	left := ""
	if avg, ok := a.avgDraw(); ok {
		left = " Этого хватит примерно на " + FmtDur(float64(a.bat.RemainingCapacity)/1000/avg) + "."
	}
	switch {
	case pct <= 10 && a.warned < 2:
		a.warned = 2
		a.balloon(fmt.Sprintf("В батарее осталось %d%%", pct), "Сохраните работу."+left, true)
	case pct <= 30 && a.warned < 1:
		a.warned = 1
		a.balloon(fmt.Sprintf("В батарее осталось %d%%", pct), "Самое время подключить павербанк."+left, false)
	}
}

// journal appends one finished outage to outages.csv.
func (a *app) journal() { a.journalAt(time.Now()) }

// journalAt closes the outage record at the given moment. Records of the
// mode switched on for a moment, or left on while the laptop sat on the
// grid, are not kept.
func (a *app) journalAt(end time.Time) {
	start, err := time.Parse(time.RFC3339, a.st.OutageStart)
	if err != nil {
		return
	}
	a.st.OutageStart = ""
	mins := int(end.Sub(start).Minutes() + 0.5)
	if !OutageWorthLogging(mins, a.st.OutagePct, a.percent(), a.st.BankUsedWh) {
		a.log("blackout mode was on for %d min without using the battery or the power bank, no outage record", mins)
		return
	}
	path := filepath.Join(a.dir, "outages.csv")
	head := ""
	if _, err := os.Stat(path); err != nil {
		head = "\xef\xbb\xbfначало;конец;минут;заряд в начале, %;заряд в конце, %;расход от батареи, Вт;взято у павербанка, Вт*ч\r\n"
	}
	line := fmt.Sprintf("%s;%s;%d;%d;%d;%.1f;%.0f\r\n", start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04"),
		mins, a.st.OutagePct, a.percent(), a.st.LastDrawW, a.st.BankUsedWh)
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		f.WriteString(head + line)
		f.Close()
	}
	a.log("outage finished: %d min, battery %d%% -> %d%%, power bank gave about %.0f Wh", mins, a.st.OutagePct, a.percent(), a.st.BankUsedWh)
}
