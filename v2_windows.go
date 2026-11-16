//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	pGetLastInputInfo    = user32.NewProc("GetLastInputInfo")
	pSystemParametersInf = user32.NewProc("SystemParametersInfoW")
	pGetTickCount64      = kernel32.NewProc("GetTickCount64")
	pGetDiskFreeSpaceEx  = kernel32.NewProc("GetDiskFreeSpaceExW")
	pGetDriveType        = kernel32.NewProc("GetDriveTypeW")
	pRegOpenKeyEx        = advapi32.NewProc("RegOpenKeyExW")
	pRegEnumValue        = advapi32.NewProc("RegEnumValueW")
)

// ---------------------------------------------------------------- what this laptop can do

// caps is what was found on this laptop. Features that need something
// missing are skipped and hidden in the window.
type caps struct {
	Ready   bool   `json:"ready"`
	Vendor  string `json:"vendor"`
	Model   string `json:"model"`
	Fan     bool   `json:"fan"`     // cooling profile can be switched
	Limit   bool   `json:"limit"`   // battery charge limit can be switched
	Nvidia  bool   `json:"nvidia"`  // a separate NVIDIA card next to the built-in graphics
	Battery bool   `json:"battery"` // there is a battery at all
	Hz      bool   `json:"hz"`      // the screen offers the lower refresh rate
}

func (c caps) Lenovo() bool { return strings.Contains(strings.ToLower(c.Vendor), "lenovo") }

const capsScript = "$c = Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue; " +
	"'vendor=' + ($c.Manufacturer -replace '\\s+', '_'); 'model=' + ($c.Model -replace '\\s+', '_'); " +
	"$n = Get-PnpDevice -Class Display -ErrorAction SilentlyContinue | Where-Object { $_.FriendlyName -match 'NVIDIA' } | Select-Object -First 1; " +
	"'nvidia=' + [int][bool]$n"

func (a *app) detectCaps() {
	// The screen can be slowed down when it offers the lower rate and is, or
	// was before blackout mode, on a higher one.
	hz := false
	if cur, ok := currentDisplay(); ok {
		low := uint32(a.cfg.RefreshHz)
		hz = hasRefresh(cur, low) && (cur.Frequency > low || (a.st.RefreshSaved && a.st.RefreshPrev > low))
	}
	a.work(func() {
		out, _ := runPS(capsScript)
		kv := parseKVs(out)
		info, _ := lenovoQuery()
		_, fan := info["fan"]
		a.post(func() {
			// A card that is switched off for the outage is still a card of this laptop.
			if kv["nvidia"] == "1" && !a.st.SeenNvidia {
				a.st.SeenNvidia = true
				a.saveState()
			}
			a.caps = caps{Ready: true, Vendor: strings.ReplaceAll(kv["vendor"], "_", " "), Model: strings.ReplaceAll(kv["model"], "_", " "),
				Fan: fan, Limit: a.limitKnown, Nvidia: kv["nvidia"] == "1" || a.st.SeenNvidia, Battery: a.batOK, Hz: hz}
			a.log("this laptop: %+v", a.caps)
		})
	})
}

// ---------------------------------------------------------------- profiles

// enterAuto switches to one of the simple profiles: build, call, quiet,
// work, or a manually chosen game or film profile.
func (a *app) enterAuto(kind string, pid uint32, name, exe string, manual bool) {
	if a.st.Mode == modeBlackout {
		return
	}
	if a.track != nil {
		a.leaveTrack("profile " + kind)
	}
	if a.st.Mode != modeNone {
		a.restorePower()
		a.st.Mode = modeNone
	}
	a.log("%s profile on: %q manual=%v", kind, exe, manual)
	t := &tracked{Kind: kind, PID: pid, Name: name, Exe: exe, Manual: manual, Throttle: map[uint32]thr{}}
	a.track = t
	a.st.Mode = kind
	a.applyPower()
	a.throttleScan(listProcs())
	want := 0
	switch kind {
	case modeGame, modeBuild:
		if a.cfg.GameThermal {
			want = 3
		}
	case modeQuiet, modeCall:
		want = 1
	}
	if want != 0 && a.caps.Fan {
		a.work(func() {
			info, _ := lenovoQuery()
			cur, ok := info["fan"]
			if !ok || cur == want {
				return
			}
			got, ok := lenovoSetFan(want)
			a.post(func() {
				a.log("thermal profile: %d -> %d, now %d (readable=%v)", cur, want, got, ok)
				if !ok || got != want {
					return
				}
				if a.track == t {
					t.FanSet, t.FanPrev = true, cur
				} else {
					a.work(func() { lenovoSetFan(cur) })
				}
			})
		})
	}
	a.saveState()
	a.syncRadios()
	a.refresh()
}

var profileKinds = map[string]string{"work": modeWork, "game": modeGame, "film": modeFilm, "build": modeBuild, "quiet": modeQuiet, "call": modeCall}

// setProfile is the manual choice in the window: "auto" gives the choice
// back to the rules.
func (a *app) setProfile(name string) error {
	if name == "auto" {
		if a.track != nil && a.track.Manual {
			a.leaveTrack("back to auto")
		}
		return nil
	}
	kind, ok := profileKinds[name]
	if !ok {
		return errors.New("unknown")
	}
	if a.st.Mode == modeBlackout {
		return errors.New("blackout")
	}
	if (kind == modeGame || kind == modeBuild) && !a.onAC() {
		return errors.New("battery")
	}
	a.enterAuto(kind, 0, "", "", true)
	return nil
}

func inHours(h, from, to int) bool {
	if from == to {
		return false
	}
	if from < to {
		return h >= from && h < to
	}
	return h >= from || h < to
}

// v2Tick runs the slower chores: automatic profiles, reminders, the weekly
// clean-up, the watch over autostart.
func (a *app) v2Tick() {
	if t := a.track; t != nil && !t.Manual {
		switch {
		case t.Kind == modeBuild && !a.cfg.BuildMode, t.Kind == modeCall && !a.cfg.CallMode:
			a.leaveTrack("switched off")
		case t.Kind == modeQuiet && (!a.cfg.QuietAuto || !inHours(time.Now().Hour(), a.cfg.QuietFrom, a.cfg.QuietTo)):
			a.leaveTrack("quiet hours are over")
		}
	}
	if a.track == nil && a.st.Mode == modeNone && a.ticks%3 == 2 {
		switch {
		case a.cfg.BuildMode && a.onAC() && len(a.cfg.BuildProcs) > 0:
			for _, p := range listProcs() {
				if inList(a.cfg.BuildProcs, p.Name) {
					a.enterAuto(modeBuild, p.PID, p.Name, p.Exe, false)
					break
				}
			}
		}
		if a.track == nil && a.cfg.QuietAuto && inHours(time.Now().Hour(), a.cfg.QuietFrom, a.cfg.QuietTo) {
			a.enterAuto(modeQuiet, 0, "", "", false)
		}
	}

	// messages to the phone
	if a.prevMode != a.st.Mode {
		if a.st.Mode == modeBlackout {
			a.tg(fmt.Sprintf("Свет пропал. В батарее %d%%.", a.percent()))
		} else if a.prevMode == modeBlackout {
			a.tg(fmt.Sprintf("Свет вернулся. В батарее %d%%.", a.percent()))
		}
		a.prevMode = a.st.Mode
	}
	if a.warned > a.tgWarned {
		a.tg(fmt.Sprintf("В батарее ноутбука осталось %d%%.", a.percent()))
	}
	a.tgWarned = a.warned

	if a.ticks%12 != 5 {
		return
	}
	// once a minute from here on
	if st := a.schedStatus(); a.cfg.BankWarn && a.st.BankKnown && a.cfg.PowerbankWh > 0 && st.Known && st.Usable &&
		!st.NextOff.IsZero() && time.Until(st.NextOff) < 3*time.Hour && a.st.Mode != modeBlackout &&
		a.st.BankLeftWh < float64(a.cfg.PowerbankWh)/2 && !a.bankAsked.Equal(st.NextOff) {
		a.bankAsked = st.NextOff
		a.balloon("Зарядите павербанк", fmt.Sprintf("Отключение около %s, а в павербанке осталось меньше половины.", st.NextOff.Format("15:04")), false)
	}
	if a.cfg.CleanAuto && !a.clean.Busy && a.onAC() && a.st.Mode == modeNone && idleFor() > 10*time.Minute {
		last, _ := time.Parse(time.RFC3339, a.st.CleanLast)
		if time.Since(last) > 7*24*time.Hour {
			a.runClean(a.cfg.CleanItems, true)
		}
	}
	if a.cfg.AutorunWatch && (a.ticks%60 == 5 || !a.autoSeen) {
		a.autoSeen = true
		a.work(func() {
			now := autoruns()
			a.post(func() { a.autorunsSeen(now) })
		})
	}
	if a.cfg.UpdateCheck && time.Since(a.updTried) > 24*time.Hour && !a.st.WifiOff {
		a.updTried = time.Now()
		repo := a.updateRepo()
		go func() {
			rel := latestRelease(repo)
			a.post(func() {
				if rel.Tag != "" && NewerVersion(rel.Tag, version) && rel.Tag != a.update {
					a.update, a.updateRel = rel.Tag, rel
					a.log("update available: %s (file: %v)", rel.Tag, rel.URL != "")
					a.balloon("Вышла новая версия", "BlackoutMode "+strings.TrimPrefix(rel.Tag, "v")+". Обновиться можно в окне программы, на «Обзоре».", false)
				}
			})
		}()
	}
}

func idleFor() time.Duration {
	var li struct {
		Size uint32
		Time uint32
	}
	li.Size = 8
	if r, _, _ := pGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li))); r == 0 {
		return 0
	}
	now, _, _ := pGetTickCount64.Call()
	return time.Duration(uint32(now)-li.Time) * time.Millisecond
}

// toRecycleBin moves files to the recycle bin, where they can be restored.
func toRecycleBin(paths []string) bool {
	var from []uint16
	for _, p := range paths {
		from = append(from, syscall.StringToUTF16(p)...) // each name ends with a zero
	}
	from = append(from, 0)
	op := struct {
		Hwnd    uintptr
		Func    uint32
		From    *uint16
		To      *uint16
		Flags   uint16
		Aborted int32
		Maps    uintptr
		Title   *uint16
	}{Func: 3, From: &from[0], Flags: 0x0040 | 0x0010 | 0x0004 | 0x0400}
	r, _, _ := shell32.NewProc("SHFileOperationW").Call(uintptr(unsafe.Pointer(&op)))
	return r == 0
}

// ---------------------------------------------------------------- phone messages and updates

func (a *app) tg(text string) {
	token, chat := a.cfg.TgToken, a.cfg.TgChat
	if token == "" || chat == "" {
		return
	}
	host, _ := os.Hostname()
	go func() {
		c := http.Client{Timeout: 15 * time.Second}
		resp, err := c.PostForm("https://api.telegram.org/bot"+token+"/sendMessage", url.Values{"chat_id": {chat}, "text": {host + ": " + text}})
		if err == nil {
			resp.Body.Close()
		}
	}()
}

func (a *app) updateRepo() string {
	if a.cfg.UpdateRepo != "" {
		return a.cfg.UpdateRepo
	}
	return defaultUpdateRepo
}

func latestRelease(repo string) releaseInfo {
	c := http.Client{Timeout: 15 * time.Second}
	resp, err := c.Get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return releaseInfo{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return releaseInfo{}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return ParseRelease(body, repo)
}

// selfUpdate downloads the new version next to this one, checks it and
// starts it. The new copy asks this one to quit and takes over; the old
// file goes to the recycle bin on its first run.
func (a *app) selfUpdate() error {
	rel := a.updateRel
	if a.updating {
		return errors.New("busy")
	}
	if rel.URL == "" || !NewerVersion(rel.Tag, version) {
		return errors.New("noupdate")
	}
	a.updating, a.updateErr = true, ""
	dst := filepath.Join(a.dir, appName+"-"+strings.TrimPrefix(rel.Tag, "v")+".exe")
	a.log("update: downloading %s", rel.URL)
	go func() {
		err := func() error {
			c := http.Client{Timeout: 5 * time.Minute}
			resp, err := c.Get(rel.URL)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return errors.New(resp.Status)
			}
			data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20+1))
			if err != nil {
				return err
			}
			if err := CheckDownload(data, rel.SHA256); err != nil {
				return err
			}
			tmp := dst + ".part"
			if err := os.WriteFile(tmp, data, 0o755); err != nil {
				return err
			}
			os.Remove(dst)
			return os.Rename(tmp, dst)
		}()
		a.post(func() {
			a.updating = false
			if err != nil {
				a.updateErr = err.Error()
				a.log("update failed: %v", err)
				os.Remove(dst + ".part")
				return
			}
			a.log("update: starting %s (checksum checked: %v)", filepath.Base(dst), rel.SHA256 != "")
			if err := exec.Command(dst).Start(); err != nil {
				a.updateErr = err.Error()
				a.log("update: the new version did not start: %v", err)
			}
		})
	}()
	return nil
}

// ---------------------------------------------------------------- autostart watch

func regValueNames(root uintptr, key string) []string {
	var h uintptr
	if r, _, _ := pRegOpenKeyEx.Call(root, uintptr(unsafe.Pointer(u16(key))), 0, 0x20019, uintptr(unsafe.Pointer(&h))); r != 0 {
		return nil
	}
	defer pRegCloseKey.Call(h)
	var names []string
	for i := uintptr(0); i < 500; i++ {
		buf := make([]uint16, 512)
		n := uint32(len(buf))
		if r, _, _ := pRegEnumValue.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0, 0, 0); r != 0 {
			break
		}
		names = append(names, syscall.UTF16ToString(buf[:n]))
	}
	return names
}

// autoruns lists what starts together with Windows: both Run keys and the
// Startup folder.
func autoruns() []string {
	const run = `Software\Microsoft\Windows\CurrentVersion\Run`
	var out []string
	for _, n := range regValueNames(hkeyCurrentUser, run) {
		out = append(out, n)
	}
	for _, n := range regValueNames(0x80000002, run) {
		out = append(out, n)
	}
	files, _ := os.ReadDir(filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Startup`))
	for _, f := range files {
		if n := f.Name(); !strings.EqualFold(n, "desktop.ini") {
			out = append(out, strings.TrimSuffix(n, filepath.Ext(n)))
		}
	}
	sort.Strings(out)
	return out
}

func (a *app) autorunsSeen(now []string) {
	if a.st.Autoruns == nil {
		a.st.Autoruns = now
		a.saveState()
		return
	}
	var fresh []string
	for _, n := range now {
		if !inList(a.st.Autoruns, n) {
			fresh = append(fresh, n)
		}
	}
	if len(fresh) > 0 {
		a.log("new in autostart: %v", fresh)
		a.balloon("Новое в автозагрузке", strings.Join(fresh, ", ")+". Если не ставили сами, проверьте в диспетчере задач.", true)
	}
	if len(fresh) > 0 || len(now) != len(a.st.Autoruns) {
		a.st.Autoruns = now
		a.saveState()
	}
}

// ---------------------------------------------------------------- clean-up

type cleanView struct {
	Busy    bool        `json:"busy"`
	At      string      `json:"at"`
	Items   []cleanItem `json:"items"`
	Hiber   int64       `json:"hiber"`
	Down    int64       `json:"downloads"`
	Last    string      `json:"last"`
	Freed   int64       `json:"freed"`
	Admin   bool        `json:"admin"`
	BinDays int         `json:"binDays"`
}

func fixedDrives() []string {
	var out []string
	for c := 'C'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if t, _, _ := pGetDriveType.Call(uintptr(unsafe.Pointer(u16(root)))); t == 3 {
			out = append(out, root)
		}
	}
	return out
}

var browserDirs = []struct{ Proc, Dir string }{
	{"brave", `BraveSoftware\Brave-Browser\User Data`},
	{"chrome", `Google\Chrome\User Data`},
	{"msedge", `Microsoft\Edge\User Data`},
	{"vivaldi", `Vivaldi\User Data`},
}

// cleanPlan lists what can be cleaned and where it lies.
func cleanPlan(elevated bool, binDays int) []cleanItem {
	local, win := os.Getenv("LOCALAPPDATA"), os.Getenv("SystemRoot")
	running := map[string]bool{}
	for _, p := range listProcs() {
		running[p.Name] = true
	}
	temp := cleanItem{ID: "temp", Age: 24 * time.Hour, Dirs: []string{os.Getenv("TEMP")}}
	if elevated {
		temp.Dirs = append(temp.Dirs, filepath.Join(win, "Temp"))
	}
	br := cleanItem{ID: "browsers"}
	for _, b := range browserDirs {
		if running[b.Proc] {
			br.Note = "running"
			continue
		}
		profiles, _ := os.ReadDir(filepath.Join(local, b.Dir))
		for _, p := range profiles {
			if n := p.Name(); p.IsDir() && (n == "Default" || strings.HasPrefix(n, "Profile ")) {
				base := filepath.Join(local, b.Dir, n)
				br.Dirs = append(br.Dirs, filepath.Join(base, "Cache"), filepath.Join(base, "Code Cache"), filepath.Join(base, "GPUCache"))
			}
		}
	}
	if running["firefox"] {
		br.Note = "running"
	} else {
		profiles, _ := os.ReadDir(filepath.Join(local, `Mozilla\Firefox\Profiles`))
		for _, p := range profiles {
			br.Dirs = append(br.Dirs, filepath.Join(local, `Mozilla\Firefox\Profiles`, p.Name(), "cache2"))
		}
	}
	wu := cleanItem{ID: "wu", Age: 7 * 24 * time.Hour, Dirs: []string{filepath.Join(win, `SoftwareDistribution\Download`)}}
	if !elevated {
		wu.Note = "admin"
	}
	bin := cleanItem{ID: "bin", Age: time.Duration(binDays) * 24 * time.Hour}
	for _, d := range fixedDrives() {
		bin.Dirs = append(bin.Dirs, d+`$Recycle.Bin`)
	}
	plan := []cleanItem{temp, br, wu, bin}
	for i := range plan {
		var keep []string
		for _, d := range plan[i].Dirs {
			// only full paths below a drive root, never a relative or empty one
			if filepath.IsAbs(d) && len(filepath.Clean(d)) > 3 {
				keep = append(keep, d)
			}
		}
		plan[i].Dirs = keep
	}
	return plan
}

func (a *app) scanClean() {
	if a.clean.Busy {
		return
	}
	a.clean.Busy = true
	elevated, days := a.elevated, a.cfg.CleanBinDays
	go func() {
		now := time.Now()
		plan := cleanPlan(elevated, days)
		for i := range plan {
			it := &plan[i]
			if it.ID == "bin" {
				for _, e := range binEntries(it.Dirs, it.Age, now) {
					it.Bytes += e.Size
					it.Files++
				}
				continue
			}
			for _, d := range it.Dirs {
				b, n := dirSize(d, it.Age, now)
				it.Bytes += b
				it.Files += n
			}
		}
		var hiber, down int64
		if fi, err := os.Stat(filepath.Join(os.Getenv("SystemDrive")+`\`, "hiberfil.sys")); err == nil {
			hiber = fi.Size()
		}
		down, _ = dirSize(filepath.Join(os.Getenv("USERPROFILE"), "Downloads"), 0, now)
		a.post(func() {
			a.clean.Busy, a.clean.Items, a.clean.Hiber, a.clean.Down = false, plan, hiber, down
			a.clean.At = now.Format(time.RFC3339)
		})
	}()
}

func (a *app) runClean(ids []string, auto bool) {
	if a.clean.Busy {
		return
	}
	a.clean.Busy = true
	elevated, days := a.elevated, a.cfg.CleanBinDays
	a.log("clean-up started: %v auto=%v", ids, auto)
	go func() {
		now := time.Now()
		var freed int64
		files := 0
		for _, it := range cleanPlan(elevated, days) {
			if !inList(ids, it.ID) || it.Note == "admin" {
				continue
			}
			if it.ID == "bin" {
				b, n := cleanBin(binEntries(it.Dirs, it.Age, now))
				freed, files = freed+b, files+n
				continue
			}
			for _, d := range it.Dirs {
				b, n := cleanDir(d, it.Age, now)
				freed, files = freed+b, files+n
			}
		}
		a.post(func() {
			a.clean.Busy = false
			a.st.CleanLast, a.st.CleanFreed = now.Format(time.RFC3339), freed
			a.saveState()
			a.log("clean-up finished: %d files, %.0f MB", files, float64(freed)/1048576)
			if auto && freed > 50<<20 {
				a.balloon("Уборка сделана", fmt.Sprintf("Освобождено %.1f ГБ.", float64(freed)/(1<<30)), false)
			}
			a.scanClean()
		})
	}()
}

// ---------------------------------------------------------------- health

type diskView struct {
	Name   string `json:"name"`
	Health string `json:"health"`
}

type driveView struct {
	Root  string `json:"root"`
	Free  uint64 `json:"free"`
	Total uint64 `json:"total"`
}

type healthView struct {
	Busy    bool        `json:"busy"`
	At      time.Time   `json:"-"`
	Design  int         `json:"design"`
	Full    int         `json:"full"`
	Cycles  int         `json:"cycles"`
	TempC   float64     `json:"tempC"`
	Disks   []diskView  `json:"disks"`
	Drives  []driveView `json:"drives"`
	UpHours float64     `json:"upHours"`
	Autorun []string    `json:"autoruns"`
	Limit   *bool       `json:"limit"`
}

const healthScript = "$s = Get-CimInstance -Namespace root/wmi -ClassName BatteryStaticData -ErrorAction SilentlyContinue | Select-Object -First 1; " +
	"$f = Get-CimInstance -Namespace root/wmi -ClassName BatteryFullChargedCapacity -ErrorAction SilentlyContinue | Select-Object -First 1; " +
	"$c = Get-CimInstance -Namespace root/wmi -ClassName BatteryCycleCount -ErrorAction SilentlyContinue | Select-Object -First 1; " +
	"'design=' + [int]$s.DesignedCapacity; 'full=' + [int]$f.FullChargedCapacity; 'cycles=' + [int]$c.CycleCount; " +
	"$t = Get-CimInstance -Namespace root/wmi -ClassName MSAcpi_ThermalZoneTemperature -ErrorAction SilentlyContinue | Measure-Object -Property CurrentTemperature -Maximum; " +
	"'temp=' + [int]$t.Maximum; " +
	"Get-PhysicalDisk -ErrorAction SilentlyContinue | ForEach-Object { 'disk=' + ($_.FriendlyName -replace '[\\s=|]+', '_') + '|' + $_.HealthStatus }"

func (a *app) refreshHealth() {
	if a.health.Busy || time.Since(a.health.At) < 5*time.Minute {
		return
	}
	a.health.Busy = true
	go func() {
		out, _ := runPS(healthScript)
		h := healthView{At: time.Now()}
		for _, f := range strings.Fields(out) {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			n, _ := strconv.Atoi(v)
			switch k {
			case "design":
				h.Design = n
			case "full":
				h.Full = n
			case "cycles":
				h.Cycles = n
			case "temp":
				if n > 2000 {
					h.TempC = float64(n)/10 - 273.15
				}
			case "disk":
				name, state, _ := strings.Cut(v, "|")
				h.Disks = append(h.Disks, diskView{Name: strings.ReplaceAll(name, "_", " "), Health: state})
			}
		}
		for _, root := range fixedDrives() {
			var free, total, totalFree uint64
			if r, _, _ := pGetDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(u16(root))), uintptr(unsafe.Pointer(&free)),
				uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree))); r != 0 && total > 0 {
				h.Drives = append(h.Drives, driveView{Root: root, Free: totalFree, Total: total})
			}
		}
		up, _, _ := pGetTickCount64.Call()
		h.UpHours = float64(up) / 3600000
		h.Autorun = autoruns()
		a.post(func() { a.health = h })
	}()
}

// ---------------------------------------------------------------- the window

func browserExe() string {
	pf, pf86, local := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")
	for _, p := range []string{
		filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		filepath.Join(local, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (a *app) startUI() {
	s, err := startAPI(a, 0)
	if err != nil {
		a.log("window server failed: %v", err)
		return
	}
	a.api = s
	a.prevMode = a.st.Mode
	a.refreshAutostart()
	if a.st.CleanLast == "" {
		// the first automatic clean-up comes a week after the first start
		a.st.CleanLast = time.Now().Format(time.RFC3339)
		a.saveState()
	}
	a.log("window server on port %d", s.Port)
	if a.cfg.CleanOld {
		dir := a.dir
		go func() {
			time.Sleep(20 * time.Second) // the previous copy has closed by now
			exe, err := os.Executable()
			if err != nil || !strings.EqualFold(filepath.Dir(exe), dir) {
				return
			}
			entries, _ := os.ReadDir(dir)
			var names []string
			for _, e := range entries {
				if !e.IsDir() {
					names = append(names, e.Name())
				}
			}
			old := OldVersionFiles(names, version)
			if len(old) == 0 {
				return
			}
			var paths []string
			for _, n := range old {
				paths = append(paths, filepath.Join(dir, n))
			}
			ok := toRecycleBin(paths)
			a.post(func() { a.log("older versions moved to the recycle bin: %v ok=%v", old, ok) })
		}()
	}
	if !a.st.SetupDone {
		a.openUI("")
	}
}

// openUI shows the main window ("") or the small panel next to the tray
// ("panel"). Both live in a second copy of the program; when the built-in
// browser component is missing, a browser window is used.
func (a *app) openUI(view string) {
	if a.api == nil {
		msgBox("Окно недоступно: не удалось открыть локальный порт. Настройки можно поменять в файле config.ini, он открывается из меню значка.", mbIconWarning)
		return
	}
	if view == "panel" && time.Since(a.panelClosed) < 400*time.Millisecond {
		return // the click that closed the panel should not open it again
	}
	link := a.api.URL(view)
	exe, err := os.Executable()
	if err != nil || a.uiBroken {
		a.openUIBrowser(view, link)
		return
	}
	cmd := exec.Command(exe, "--ui="+view, link)
	if err := cmd.Start(); err != nil {
		a.log("window: %v", err)
		a.openUIBrowser(view, link)
		return
	}
	go func() {
		err := cmd.Wait()
		a.post(func() {
			if view == "panel" {
				a.panelClosed = time.Now()
			}
			if err != nil {
				a.log("window process: %v, a browser window is used from now on", err)
				a.uiBroken = true
				a.openUIBrowser(view, link)
			}
		})
	}()
}

func (a *app) openUIBrowser(view, link string) {
	exe := browserExe()
	if exe == "" {
		exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", link).Start()
		return
	}
	scale := 1.0
	if pGetDpiForSystem.Find() == nil {
		if d, _, _ := pGetDpiForSystem.Call(); d >= 96 {
			scale = float64(d) / 96
		}
	}
	args := []string{"--app=" + link, "--user-data-dir=" + filepath.Join(os.Getenv("LOCALAPPDATA"), appName, "window"),
		"--no-first-run", "--no-default-browser-check", "--disable-sync", "--disable-background-networking"}
	if view == "panel" {
		const w, h = 360, 480
		_, _, r, b := uiWorkArea()
		args = append(args, fmt.Sprintf("--window-size=%d,%d", w, h),
			fmt.Sprintf("--window-position=%d,%d", int(float64(r)/scale)-w-12, int(float64(b)/scale)-h-12))
	} else {
		args = append(args, "--window-size=1000,700")
	}
	if err := exec.Command(exe, args...).Start(); err != nil {
		a.log("window: %v", err)
		exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", link).Start()
	}
}

// refreshAutostart looks up whether the program starts with Windows. The
// lookup runs a system tool, so it is done aside and remembered.
func (a *app) refreshAutostart() {
	a.work(func() {
		on := autostartOn()
		a.post(func() { a.autoOn = on })
	})
}

// call runs f on the main thread and waits for it, so that the window's
// requests never touch the application state from another thread.
func (a *app) call(f func()) {
	done := make(chan struct{})
	a.post(func() {
		defer close(done)
		f()
	})
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
}

type stateView struct {
	Version    string   `json:"version"`
	Elevated   bool     `json:"elevated"`
	Mode       string   `json:"mode"`
	Kind       string   `json:"kind"`
	Exe        string   `json:"exe"`
	Manual     bool     `json:"manual"`
	OnAC       bool     `json:"onAC"`
	Battery    bool     `json:"battery"`
	Pct        int      `json:"pct"`
	Watts      float64  `json:"watts"`
	Measured   bool     `json:"measured"`
	Hours      float64  `json:"hours"`
	BankWh     float64  `json:"bankWh"`
	BankCap    int      `json:"bankCap"`
	BankKnow   bool     `json:"bankKnown"`
	BankFactor float64  `json:"bankFactor"`
	Ask        int64    `json:"ask,omitempty"` // when the tray menu asked for the power bank field, ms
	Charging   bool     `json:"charging"`
	BatWh      float64  `json:"batWh"` // energy left in the battery, W*h
	RateW      float64  `json:"rateW"` // battery charge rate while charging, W
	GPU        string   `json:"gpu"`
	GPUUsers   []string `json:"gpuUsers"`
	Since      string   `json:"since"`
	Sched      struct {
		Known    bool   `json:"known"`
		Usable   bool   `json:"usable"`
		State    string `json:"state"`
		ChangeAt string `json:"changeAt"`
		NextOff  string `json:"nextOff"`
		Region   string `json:"region"`
		Group    string `json:"group"`
	} `json:"sched"`
	Caps       caps        `json:"caps"`
	Update     string      `json:"update"`
	UpdateFile bool        `json:"updateFile"` // the release has a program file to install
	Updating   bool        `json:"updating"`
	UpdateErr  string      `json:"updateErr"`
	Measure    measureView `json:"measure"`
	Lang       string      `json:"lang"`
	SetupDone  bool        `json:"setupDone"`
	Autostart  bool        `json:"autostart"`
	LimitOn    bool        `json:"limitOn"`
}

func (a *app) stateNow() stateView {
	var v stateView
	v.Version, v.Elevated, v.Mode, v.OnAC, v.Battery, v.Pct = version, a.elevated, a.st.Mode, a.onAC(), a.batOK, a.percent()
	if a.track != nil {
		v.Kind, v.Exe, v.Manual = a.track.Kind, a.track.Exe, a.track.Manual
	}
	in := a.widgetInput()
	v.Charging = in.Charging
	v.BatWh = in.RemainWh
	if in.Charging && in.RateW > 0.5 {
		v.RateW = in.RateW
	}
	switch {
	case in.AvgW > 0.5:
		v.Watts, v.Measured = in.AvgW, true
	case in.LastW > 1:
		v.Watts = in.LastW
	}
	v.Hours = -1
	if v.Watts > 0 && a.batOK {
		wh := in.RemainWh
		if a.st.Mode == modeBlackout || a.st.BankKnown {
			wh += in.BankWh // already in terms of what reaches the laptop
		}
		eta := v.Watts
		if in.EtaW > 0.5 {
			eta = in.EtaW
		}
		v.Hours = wh / eta
	}
	v.BankWh, v.BankCap, v.BankKnow = a.bankLeft(), a.cfg.PowerbankWh, a.st.BankKnown
	v.BankFactor = BankFactor(a.st.BankFactor)
	if !a.uiAsk.IsZero() {
		v.Ask = a.uiAsk.UnixMilli()
	}
	v.GPU, v.GPUUsers = a.gpu, a.gpuUserNames()
	if t, err := time.Parse(time.RFC3339, a.st.OutageStart); err == nil && a.st.Mode == modeBlackout {
		v.Since = t.Format("15:04")
	}
	st := a.schedStatus()
	v.Sched.Known, v.Sched.Usable, v.Sched.State = st.Known, st.Usable, st.State
	if !st.ChangeAt.IsZero() {
		v.Sched.ChangeAt = st.ChangeAt.Format("15:04")
	}
	if !st.NextOff.IsZero() {
		v.Sched.NextOff = st.NextOff.Format("15:04")
	}
	v.Sched.Region, v.Sched.Group = a.schedPlace()
	v.Caps = a.caps
	v.Caps.Limit, v.Caps.Battery = a.limitKnown, a.batOK
	v.UpdateFile, v.Updating, v.UpdateErr = a.updateRel.URL != "", a.updating, a.updateErr
	v.Measure = a.measureNow()
	v.Update, v.Lang, v.SetupDone, v.Autostart, v.LimitOn = a.update, a.cfg.Lang, a.st.SetupDone, a.autoOn, a.limitOn
	return v
}

// Section answers the window's questions.
func (a *app) Section(name string) (v any, ok bool) {
	if name == "stats" {
		p, _ := os.ReadFile(filepath.Join(a.dir, "power.csv"))
		o, _ := os.ReadFile(filepath.Join(a.dir, "outages.csv"))
		st := BuildStats(string(p), string(o), time.Now())
		a.call(func() { v, ok = map[string]any{"stats": st, "hogs": a.hogs}, true })
		return
	}
	a.call(func() {
		switch name {
		case "state":
			v, ok = a.stateNow(), true
		case "config":
			v, ok = ConfigMap(a.cfg), true
		case "schedule":
			_, group := a.schedPlace()
			days := []dayLine{}
			if a.sched.loaded {
				days = ScheduleDays(a.sched.region, group)
			}
			v, ok = map[string]any{"days": days, "fetchedAt": a.sched.region.FetchedAt, "status": a.sched.region.Status,
				"emergency": a.sched.region.Emergency.Active, "groups": a.sched.region.GroupNames()}, true
		case "cleanup":
			if a.clean.Items == nil {
				a.scanClean()
			}
			c := a.clean
			c.Last, c.Freed, c.Admin, c.BinDays = a.st.CleanLast, a.st.CleanFreed, a.elevated, a.cfg.CleanBinDays
			v, ok = c, true
		case "health":
			a.refreshHealth()
			h := a.health
			if a.limitKnown {
				on := a.limitOn
				h.Limit = &on
			}
			v, ok = h, true
		}
	})
	return
}

// Action does what a button in the window asks for.
func (a *app) Action(name string, arg map[string]string) (v any, err error) {
	a.call(func() {
		switch name {
		case "toggle":
			a.lastToggle = time.Time{}
			a.toggle()
		case "profile":
			err = a.setProfile(arg["name"])
		case "set":
			c, ok := SetConfigKey(a.cfg, arg["key"], arg["value"])
			if !ok {
				err = errors.New("unknown")
				return
			}
			a.cfg = c
			a.saveConfig()
			a.log("setting changed in the window: %s = %s", arg["key"], arg["value"])
			switch arg["key"] {
			case "hotkey":
				a.setHotkey()
			case "level", "ecores", "game_turbo", "lite_turbo":
				a.reapply()
			case "lite_game", "lite_gpu":
				if a.cfg.LiteGame && a.cfg.LiteGPU && a.st.Mode == modeBlackout {
					a.liteGPUApply()
				} else {
					a.liteGPURestore()
				}
				a.saveState()
			case "region", "group", "schedule":
				a.fetchSchedule(true)
			case "clean_bin_days":
				a.clean.Items = nil
			}
			a.refresh()
		case "bankfull":
			err = a.setBankPct(100)
		case "bankpct":
			pct, e := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(arg["pct"]), "%"), 64)
			if e != nil {
				err = errors.New("pct")
				return
			}
			err = a.setBankPct(pct)
		case "gpufix":
			done := 0
			for _, u := range a.gpuUsers {
				if p := procPath(u.PID); p != "" && preferIntegrated(p) {
					done++
					a.log("built-in graphics assigned to %s", p)
				}
			}
			v = map[string]int{"done": done}
		case "scan":
			a.scanClean()
		case "clean":
			a.runClean(parseList(arg["ids"]), false)
		case "hiberoff":
			if !a.elevated {
				err = errors.New("admin")
				return
			}
			a.work(func() {
				out, e := runHidden("powercfg.exe", "/h", "off")
				a.post(func() {
					a.log("hibernation file removed: %v %s", e, out)
					a.scanClean()
				})
			})
		case "autostart":
			a.toggleAutostart()
			a.autoOn = !a.autoOn
			a.refreshAutostart()
		case "limit":
			a.toggleChargeLimit()
		case "elevate":
			if !relaunchElevated() {
				err = errors.New("cancelled")
			}
		case "update":
			err = a.selfUpdate()
		case "measure":
			err = a.measureStart()
		case "measurestop":
			a.measureStop("stopped in the window")
		case "setupdone":
			a.st.SetupDone = true
			a.saveState()
		case "schedrefresh":
			a.fetchSchedule(true)
		case "open":
			switch arg["what"] {
			case "log":
				exec.Command("notepad.exe", filepath.Join(a.dir, "log.txt")).Start()
			case "config":
				exec.Command("notepad.exe", a.cfgPath()).Start()
			case "tips":
				go msgBox(tipsText, mbIconInfo)
			case "folder":
				exec.Command("explorer.exe", a.dir).Start()
			case "downloads":
				exec.Command("explorer.exe", filepath.Join(os.Getenv("USERPROFILE"), "Downloads")).Start()
			case "startup":
				exec.Command("taskmgr.exe", "/0", "/startup").Start()
			case "main":
				a.openUI("")
			default:
				err = errors.New("unknown")
			}
		default:
			err = errors.New("unknown")
		}
	})
	return
}

// ---------------------------------------------------------------- measuring the saving

// measureView is what the window shows about the measurement.
type measureView struct {
	Running bool    `json:"running"`
	Left    int     `json:"left"`  // seconds
	Phase   int     `json:"phase"` // 1 and 2 without saving, 3 and 4 with it
	BaseW   float64 `json:"baseW"` // the last result: draw without saving
	SaveW   float64 `json:"saveW"` // and with it
	At      string  `json:"at"`    // when it was taken
}

func (a *app) measureNow() measureView {
	v := measureView{BaseW: a.st.MeasureBaseW, SaveW: a.st.MeasureSaveW, At: a.st.MeasureAt}
	if m := a.meas; m != nil {
		v.Running, v.Left, v.Phase = true, m.SecondsLeft(), m.Phase
	}
	return v
}

// measureStart begins the comparison: three minutes without blackout mode,
// three with it. It needs the laptop on battery and no game or film going.
func (a *app) measureStart() error {
	switch {
	case a.meas != nil:
		return errors.New("busy")
	case !a.batOK || a.onAC():
		return errors.New("needbattery")
	case a.track != nil:
		return errors.New("needidle")
	}
	a.meas = &measureRun{Phase: 1, WasOn: a.st.Mode == modeBlackout}
	a.log("measurement started, blackout mode was on: %v", a.meas.WasOn)
	a.quiet = true
	a.disable("measurement")
	a.quiet = false
	return nil
}

// measureStop ends the measurement early and puts the mode back.
func (a *app) measureStop(reason string) {
	m := a.meas
	if m == nil {
		return
	}
	a.meas = nil
	a.log("measurement ended: %s", reason)
	a.quiet = true
	if m.WasOn {
		a.enable("measurement")
	} else {
		a.disable("measurement")
	}
	a.quiet = false
}

// measureTick moves the measurement on; rate is the battery rate in W,
// negative while discharging.
func (a *app) measureTick(rate float64) {
	m := a.meas
	if m == nil {
		return
	}
	if a.onAC() || a.track != nil {
		a.measureStop("the charger was connected or a game or a film started")
		return
	}
	w := 0.0
	if rate < -0.5 && rate > -300 {
		w = -rate
	}
	wantOn := m.Step(w)
	if m.Finished {
		a.meas = nil
		if m.BaseW > 1 && m.SaveW > 1 {
			a.st.MeasureBaseW, a.st.MeasureSaveW, a.st.MeasureAt = m.BaseW, m.SaveW, time.Now().Format(time.RFC3339)
			a.log("measurement: %.1f W without saving, %.1f W with it", m.BaseW, m.SaveW)
			a.balloon("Замер готов", fmt.Sprintf("Без экономии %.1f Вт, с экономией %.1f Вт.", m.BaseW, m.SaveW), false)
		} else {
			a.log("measurement gave no readings")
		}
		a.quiet = true
		if m.WasOn {
			a.enable("measurement")
		} else {
			a.disable("measurement")
		}
		a.quiet = false
		a.saveState()
		return
	}
	a.quiet = true
	if wantOn {
		a.enable("measurement")
	} else {
		a.disable("measurement")
	}
	a.quiet = false
}
