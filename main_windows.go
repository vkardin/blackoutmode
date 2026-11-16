//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

const (
	appName  = "BlackoutMode"
	appTitle = "Режим отключения"
	version  = "2.3.0"
	wndClass = appName + "Window"
)

// ---------------------------------------------------------------- Win32

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	powrprof = syscall.NewLazyDLL("powrprof.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	pRegisterClassEx      = user32.NewProc("RegisterClassExW")
	pCreateWindowEx       = user32.NewProc("CreateWindowExW")
	pDefWindowProc        = user32.NewProc("DefWindowProcW")
	pFindWindow           = user32.NewProc("FindWindowW")
	pGetMessage           = user32.NewProc("GetMessageW")
	pTranslateMessage     = user32.NewProc("TranslateMessage")
	pDispatchMessage      = user32.NewProc("DispatchMessageW")
	pPostMessage          = user32.NewProc("PostMessageW")
	pPostQuitMessage      = user32.NewProc("PostQuitMessage")
	pRegisterWindowMsg    = user32.NewProc("RegisterWindowMessageW")
	pSetTimer             = user32.NewProc("SetTimer")
	pCreatePopupMenu      = user32.NewProc("CreatePopupMenu")
	pAppendMenu           = user32.NewProc("AppendMenuW")
	pTrackPopupMenu       = user32.NewProc("TrackPopupMenu")
	pDestroyMenu          = user32.NewProc("DestroyMenu")
	pSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	pGetForegroundWindow  = user32.NewProc("GetForegroundWindow")
	pGetWindowThreadPID   = user32.NewProc("GetWindowThreadProcessId")
	pGetCursorPos         = user32.NewProc("GetCursorPos")
	pMessageBox           = user32.NewProc("MessageBoxW")
	pCreateIconIndirect   = user32.NewProc("CreateIconIndirect")
	pDestroyIcon          = user32.NewProc("DestroyIcon")
	pGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	pSetDpiAwarenessCtx   = user32.NewProc("SetProcessDpiAwarenessContext")
	pEnumDisplaySettings  = user32.NewProc("EnumDisplaySettingsW")
	pChangeDisplayEx      = user32.NewProc("ChangeDisplaySettingsExW")
	pChangeMsgFilterEx    = user32.NewProc("ChangeWindowMessageFilterEx")
	pIsUserAnAdmin        = shell32.NewProc("IsUserAnAdmin")
	pShellExecute         = shell32.NewProc("ShellExecuteW")
	pGetModuleHandle      = kernel32.NewProc("GetModuleHandleW")
	pMultiByteToWide      = kernel32.NewProc("MultiByteToWideChar")
	pCreateMutex          = kernel32.NewProc("CreateMutexW")
	pQueryImageName       = kernel32.NewProc("QueryFullProcessImageNameW")
	pGetPriorityClass     = kernel32.NewProc("GetPriorityClass")
	pSetPriorityClass     = kernel32.NewProc("SetPriorityClass")
	pSetProcessInfo       = kernel32.NewProc("SetProcessInformation")
	pShellNotifyIcon      = shell32.NewProc("Shell_NotifyIconW")
	pCreateDIBSection     = gdi32.NewProc("CreateDIBSection")
	pCreateBitmap         = gdi32.NewProc("CreateBitmap")
	pDeleteObject         = gdi32.NewProc("DeleteObject")
	pCallNtPowerInfo      = powrprof.NewProc("CallNtPowerInformation")
	pPowerGetActiveScheme = powrprof.NewProc("PowerGetActiveScheme")
	pPowerSetActiveScheme = powrprof.NewProc("PowerSetActiveScheme")
	pPowerReadAC          = powrprof.NewProc("PowerReadACValueIndex")
	pPowerReadDC          = powrprof.NewProc("PowerReadDCValueIndex")
	pPowerWriteAC         = powrprof.NewProc("PowerWriteACValueIndex")
	pPowerWriteDC         = powrprof.NewProc("PowerWriteDCValueIndex")
	pPowerGetOverlay      = powrprof.NewProc("PowerGetEffectiveOverlayScheme")
	pPowerSetOverlay      = powrprof.NewProc("PowerSetActiveOverlayScheme")
	pRegCreateKeyEx       = advapi32.NewProc("RegCreateKeyExW")
	pRegSetValueEx        = advapi32.NewProc("RegSetValueExW")
	pRegDeleteValue       = advapi32.NewProc("RegDeleteValueW")
	pRegQueryValueEx      = advapi32.NewProc("RegQueryValueExW")
	pRegCloseKey          = advapi32.NewProc("RegCloseKey")
)

const (
	wmDestroy        = 0x0002
	wmClose          = 0x0010
	wmEndSession     = 0x0016
	wmTimer          = 0x0113
	wmPowerBroadcast = 0x0218
	wmLButtonUp      = 0x0202
	wmRButtonUp      = 0x0205
	wmApp            = 0x8000
	wmTray           = wmApp + 1
	wmPost           = wmApp + 2

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10
	niifInfo   = 0x1
	niifWarn   = 0x2

	mfString    = 0x0
	mfGrayed    = 0x1
	mfChecked   = 0x8
	mfPopup     = 0x10
	mfSeparator = 0x800

	tpmRightButton = 0x0002
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	mbYesNo         = 0x4
	mbYesNoCancel   = 0x3
	mbIconInfo      = 0x40
	mbIconWarning   = 0x30
	mbIconQuestion  = 0x20
	mbSetForeground = 0x10000
	mbTopmost       = 0x40000
	idYes           = 6
	idNo            = 7

	processTerminate  = 0x0001
	processSetInfo    = 0x0200
	processQueryLtd   = 0x1000
	createNoWindow    = 0x08000000
	hkeyCurrentUser   = 0x80000001
	errorAlreadyExist = 183

	prioNormal      = 0x20
	prioHigh        = 0x80
	prioBelowNormal = 0x4000
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type point struct{ X, Y int32 }

type msgT struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type notifyIconData struct {
	Size            uint32
	Hwnd            uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GuidItem        syscall.GUID
	BalloonIcon     uintptr
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	Mask     uintptr
	Color    uintptr
}

type batteryState struct {
	AcOnLine          byte
	BatteryPresent    byte
	Charging          byte
	Discharging       byte
	Spare             [3]byte
	Tag               byte
	MaxCapacity       uint32
	RemainingCapacity uint32
	Rate              int32
	EstimatedTime     uint32
	DefaultAlert1     uint32
	DefaultAlert2     uint32
}

// devMode is DEVMODEW, display part.
type devMode struct {
	DeviceName                                                 [32]uint16
	SpecVersion, DriverVersion, Size, DriverExtra              uint16
	Fields                                                     uint32
	PosX, PosY                                                 int32
	Orientation, FixedOutput                                   uint32
	Color, Duplex, YResolution, TTOption, Collate              int16
	FormName                                                   [32]uint16
	LogPixels                                                  uint16
	BitsPerPel, PelsWidth, PelsHeight, DisplayFlags, Frequency uint32
	ICMMethod, ICMIntent, MediaType, DitherType                uint32
	Reserved1, Reserved2, PanningWidth, PanningHeight          uint32
}

func currentDisplay() (devMode, bool) {
	var dm devMode
	dm.Size = uint16(unsafe.Sizeof(dm))
	r, _, _ := pEnumDisplaySettings.Call(0, 0xFFFFFFFF, uintptr(unsafe.Pointer(&dm)))
	return dm, r != 0
}

// hasRefresh reports whether the main display offers this frequency at the current resolution.
func hasRefresh(cur devMode, hz uint32) bool {
	for i := uintptr(0); i < 2000; i++ {
		var dm devMode
		dm.Size = uint16(unsafe.Sizeof(dm))
		if r, _, _ := pEnumDisplaySettings.Call(0, i, uintptr(unsafe.Pointer(&dm))); r == 0 {
			break
		}
		if dm.PelsWidth == cur.PelsWidth && dm.PelsHeight == cur.PelsHeight &&
			dm.BitsPerPel == cur.BitsPerPel && dm.Frequency == hz {
			return true
		}
	}
	return false
}

func setRefresh(hz uint32) bool {
	dm, ok := currentDisplay()
	if !ok {
		return false
	}
	dm.Frequency = hz
	dm.Fields = 0x40000 | 0x80000 | 0x100000 | 0x400000 // bpp, width, height, frequency
	r, _, _ := pChangeDisplayEx.Call(0, uintptr(unsafe.Pointer(&dm)), 0, 1, 0)
	return int32(r) == 0
}

func isElevated() bool {
	r, _, _ := pIsUserAnAdmin.Call()
	return r != 0
}

// relaunchElevated starts this program again through the Windows elevation prompt.
func relaunchElevated() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	r, _, _ := pShellExecute.Call(0, uintptr(unsafe.Pointer(u16("runas"))), uintptr(unsafe.Pointer(u16(exe))), 0, 0, 1)
	return r > 32
}

type powerThrottling struct {
	Version     uint32
	ControlMask uint32
	StateMask   uint32
}

func u16(s string) *uint16 {
	u := utf16.Encode([]rune(strings.ReplaceAll(s, "\x00", "")))
	u = append(u, 0)
	return &u[0]
}

func setW(dst []uint16, s string) {
	u := utf16.Encode([]rune(s))
	if len(u) > len(dst)-1 {
		u = u[:len(dst)-1]
	}
	n := copy(dst, u)
	dst[n] = 0
}

func msgBox(text string, flags uintptr) int {
	r, _, _ := pMessageBox.Call(0, uintptr(unsafe.Pointer(u16(text))),
		uintptr(unsafe.Pointer(u16(appTitle))), flags|mbSetForeground|mbTopmost)
	return int(r)
}

// ---------------------------------------------------------------- GUID helpers

func mustGUID(s string) syscall.GUID {
	g, ok := parseGUID(s)
	if !ok {
		panic("bad guid " + s)
	}
	return g
}

func parseGUID(s string) (syscall.GUID, bool) {
	var g syscall.GUID
	s = strings.Trim(s, "{}")
	h := strings.ReplaceAll(s, "-", "")
	if len(s) != 36 || len(h) != 32 {
		return g, false
	}
	d1, e1 := strconv.ParseUint(h[0:8], 16, 32)
	d2, e2 := strconv.ParseUint(h[8:12], 16, 16)
	d3, e3 := strconv.ParseUint(h[12:16], 16, 16)
	if e1 != nil || e2 != nil || e3 != nil {
		return g, false
	}
	g.Data1, g.Data2, g.Data3 = uint32(d1), uint16(d2), uint16(d3)
	for i := 0; i < 8; i++ {
		b, err := strconv.ParseUint(h[16+i*2:18+i*2], 16, 8)
		if err != nil {
			return g, false
		}
		g.Data4[i] = byte(b)
	}
	return g, true
}

func guidStr(g syscall.GUID) string {
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		g.Data1, g.Data2, g.Data3, g.Data4[0], g.Data4[1],
		g.Data4[2], g.Data4[3], g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7])
}

// ---------------------------------------------------------------- power plan

const (
	subProcessor   = "54533251-82be-4824-96c1-47b60b740d00"
	setBoostMode   = "be337238-0d82-4146-a960-4f3749d470c7"
	setEPP         = "36687f9e-e3a5-4dbf-b1dc-15eb381c6863"
	setEPP1        = "36687f9e-e3a5-4dbf-b1dc-15eb381c6864"
	setMinCores    = "0cc5b647-c1df-4637-891a-dec35c318583"
	setMinCores1   = "0cc5b647-c1df-4637-891a-dec35c318584"
	setHeteroLong  = "93b8b6dc-0698-4d1c-9ee4-0644e900c85d"
	setHeteroShort = "bae08b81-2d5e-4688-ad6a-13243356654b"
	subPCIExpress  = "501a4d13-42af-4429-9fd1-a8218c268e20"
	setASPM        = "ee12f906-d277-404b-b6da-e5fa1a576df5"
	subWireless    = "19cbb8fa-5279-450e-9fac-8a3d5fedd0c1"
	setWirelessPS  = "12bbebe6-58d6-4636-95bb-3217ef867c1a"
	subUSB         = "2a737441-1930-4402-8d77-b2bebba308a3"
	setUSBSuspend  = "48e6b7a6-50f5-4782-a5d4-53bb8f07e226"
	subEnergySaver = "de830923-a562-41af-a086-e3a2c6bad2da"
	setESThreshold = "e69653ca-cf7f-4f05-aa73-cb833fa90ad4"
	setESBrightnes = "13d09884-f74e-474a-a852-b6bde8ad03a8"

	overlayEfficiency  = "961cc777-2547-4f9d-8174-7d86181b8a7a"
	overlayPerformance = "ded574b5-45a0-4f42-8737-46345c09c238"
)

const (
	modeNone     = ""
	modeBlackout = "blackout"
	modeGame     = "game"
	modeFilm     = "film"
	modeLite     = "lite" // a game on top of blackout mode, never stored as the main mode
	modeBuild    = "build"
	modeCall     = "call"
	modeQuiet    = "quiet"
	modeWork     = "work"

	overlayBalanced = "00000000-0000-0000-0000-000000000000"
)

// target describes one power plan setting. AC/DC of -1 leave that side alone.
// Dir +1 never lowers an existing value, -1 never raises it.
type target struct {
	Name     string
	Sub      string
	Set      string
	AC, DC   int
	Dir      int
	KeepZero bool
}

func (t target) want(cur uint32, v int) uint32 {
	if v < 0 {
		return cur
	}
	if t.KeepZero && cur == 0 {
		return 0
	}
	if t.Dir > 0 && cur > uint32(v) {
		return cur
	}
	if t.Dir < 0 && cur < uint32(v) {
		return cur
	}
	return uint32(v)
}

func savingTargets(c Config) []target {
	level := c.Level
	boost := target{Name: "turbo boost", Sub: subProcessor, Set: setBoostMode, AC: 0, DC: 0}
	epp := 100
	if level == "soft" {
		boost.AC, boost.DC, boost.KeepZero = 3, 3, true
		epp = 80
	}
	list := []target{
		boost,
		{Name: "energy preference", Sub: subProcessor, Set: setEPP, AC: epp, DC: epp, Dir: 1},
		{Name: "energy preference (P-cores)", Sub: subProcessor, Set: setEPP1, AC: epp, DC: epp, Dir: 1},
		{Name: "PCIe link power", Sub: subPCIExpress, Set: setASPM, AC: 2, DC: 2, Dir: 1},
		{Name: "Wi-Fi power saving", Sub: subWireless, Set: setWirelessPS, AC: 2, DC: 2, Dir: 1},
		{Name: "energy saver on battery", Sub: subEnergySaver, Set: setESThreshold, AC: -1, DC: 100},
		{Name: "energy saver dimming", Sub: subEnergySaver, Set: setESBrightnes, AC: 100, DC: 100},
	}
	if c.ECores {
		// 4 = prefer the efficient cores, the fast ones join in under load
		list = append(list,
			target{Name: "core choice", Sub: subProcessor, Set: setHeteroLong, AC: 4, DC: 4},
			target{Name: "core choice (short tasks)", Sub: subProcessor, Set: setHeteroShort, AC: 4, DC: 4})
	}
	return list
}

// liteTargets is blackout mode with room for a game: the same settings,
// but the processor may boost for a moment and reacts faster.
func liteTargets(c Config) []target {
	list := savingTargets(c)
	if !c.LiteTurbo {
		return list
	}
	for i := range list {
		switch list[i].Set {
		case setBoostMode:
			list[i].AC, list[i].DC, list[i].KeepZero = 3, 3, false
		case setEPP, setEPP1:
			list[i].AC, list[i].DC, list[i].Dir = 60, 60, 0
		}
	}
	return list
}

// callTargets keeps the processor responsive but calm during a video call.
func callTargets() []target {
	return []target{
		{Name: "energy preference", Sub: subProcessor, Set: setEPP, AC: 50, DC: 50},
		{Name: "energy preference (P-cores)", Sub: subProcessor, Set: setEPP1, AC: 50, DC: 50},
	}
}

// Game mode only ever runs on external power, so only AC values are touched.
func gameTargets(c Config) []target {
	list := []target{
		{Name: "energy preference", Sub: subProcessor, Set: setEPP, AC: 0, DC: -1},
		{Name: "energy preference (P-cores)", Sub: subProcessor, Set: setEPP1, AC: 0, DC: -1},
		{Name: "core parking", Sub: subProcessor, Set: setMinCores, AC: 100, DC: -1},
		{Name: "core parking (P-cores)", Sub: subProcessor, Set: setMinCores1, AC: 100, DC: -1},
		{Name: "PCIe link power", Sub: subPCIExpress, Set: setASPM, AC: 0, DC: -1},
		{Name: "Wi-Fi power saving", Sub: subWireless, Set: setWirelessPS, AC: 0, DC: -1},
		{Name: "USB suspend", Sub: subUSB, Set: setUSBSuspend, AC: 0, DC: -1},
	}
	if c.GameTurbo {
		list = append([]target{{Name: "turbo boost", Sub: subProcessor, Set: setBoostMode, AC: 2, DC: -1}}, list...)
	}
	return list
}

func activeScheme() (syscall.GUID, bool) {
	var p *syscall.GUID
	r, _, _ := pPowerGetActiveScheme.Call(0, uintptr(unsafe.Pointer(&p)))
	if r != 0 || p == nil {
		return syscall.GUID{}, false
	}
	g := *p
	syscall.LocalFree(syscall.Handle(unsafe.Pointer(p)))
	return g, true
}

func setActiveScheme(g syscall.GUID) bool {
	r, _, _ := pPowerSetActiveScheme.Call(0, uintptr(unsafe.Pointer(&g)))
	return r == 0
}

func readIdx(ac bool, scheme, sub, set syscall.GUID) (uint32, bool) {
	p := pPowerReadDC
	if ac {
		p = pPowerReadAC
	}
	var v uint32
	r, _, _ := p.Call(0, uintptr(unsafe.Pointer(&scheme)), uintptr(unsafe.Pointer(&sub)),
		uintptr(unsafe.Pointer(&set)), uintptr(unsafe.Pointer(&v)))
	return v, r == 0
}

func writeIdx(ac bool, scheme, sub, set syscall.GUID, v uint32) uintptr {
	p := pPowerWriteDC
	if ac {
		p = pPowerWriteAC
	}
	r, _, _ := p.Call(0, uintptr(unsafe.Pointer(&scheme)), uintptr(unsafe.Pointer(&sub)),
		uintptr(unsafe.Pointer(&set)), uintptr(v))
	return r
}

func getOverlay() (syscall.GUID, bool) {
	var g syscall.GUID
	if pPowerGetOverlay.Find() != nil {
		return g, false
	}
	r, _, _ := pPowerGetOverlay.Call(uintptr(unsafe.Pointer(&g)))
	return g, r == 0
}

func setOverlay(g syscall.GUID) bool {
	if pPowerSetOverlay.Find() != nil {
		return false
	}
	r, _, _ := pPowerSetOverlay.Call(uintptr(unsafe.Pointer(&g)))
	return r == 0
}

func readBattery() (batteryState, bool) {
	var b batteryState
	r, _, _ := pCallNtPowerInfo.Call(5, 0, 0, uintptr(unsafe.Pointer(&b)), unsafe.Sizeof(b))
	return b, r == 0
}

// ---------------------------------------------------------------- WMI through PowerShell

func runPS(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	return strings.TrimSpace(oemText(out)), err
}

// oemText turns the output of a console program into text. Windows tools
// answer in the console code page (866 on a Russian system), not in UTF-8.
func oemText(b []byte) string {
	if len(b) == 0 || utf8.Valid(b) {
		return string(b)
	}
	n, _, _ := pMultiByteToWide.Call(1, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0) // CP_OEMCP
	if n == 0 {
		return string(b)
	}
	w := make([]uint16, n)
	pMultiByteToWide.Call(1, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), uintptr(unsafe.Pointer(&w[0])), n)
	return syscall.UTF16ToString(w)
}

func readBrightness() (int, bool) {
	out, err := runPS("(Get-WmiObject -Namespace root/WMI -Class WmiMonitorBrightness | Select-Object -First 1).CurrentBrightness")
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(out)
	if err != nil || n < 0 || n > 100 {
		return 0, false
	}
	return n, true
}

func setBrightness(v int) bool {
	_, err := runPS(fmt.Sprintf("Get-WmiObject -Namespace root/WMI -Class WmiMonitorBrightnessMethods | ForEach-Object { $_.WmiSetBrightness(1, %d) | Out-Null }", clamp(v, 0, 100)))
	return err == nil
}

// Lenovo gaming laptops expose the thermal profile and the charger type here.
const lenovoObj = "$g = $null; try { $g = Get-WmiObject -Namespace root/WMI -Class LENOVO_GAMEZONE_DATA -ErrorAction Stop | Select-Object -First 1 } " +
	"catch { 'err=' + ($_.Exception.Message -replace '\\s+', '_') }; if (-not $g) { 'obj=none'; exit }; "

func lenovoCall(key, method string) string {
	return "try { '" + key + "=' + $g." + method + ".Data } catch { '" + key + "=' + ($_.Exception.Message -replace '\\s+', '_') }; "
}

func parseKV(out string) map[string]int {
	m := map[string]int{}
	for _, line := range strings.Fields(out) {
		if i := strings.Index(line, "="); i > 0 {
			if n, err := strconv.Atoi(line[i+1:]); err == nil {
				m[line[:i]] = n
			}
		}
	}
	return m
}

// lenovoQuery returns fan (1 quiet, 2 balanced, 3 performance), acfit and
// charge (1 means a full-power charger). Missing keys mean "not available".
func lenovoQuery() (map[string]int, string) {
	out, _ := runPS(lenovoObj + lenovoCall("fan", "GetSmartFanMode()") +
		lenovoCall("acfit", "IsACFitForOC()") + lenovoCall("charge", "GetPowerChargeMode()"))
	return parseKV(out), strings.Join(strings.Fields(out), " ")
}

func lenovoSetFan(v int) (int, bool) {
	out, _ := runPS(lenovoObj + lenovoCall("set", fmt.Sprintf("SetSmartFanMode(%d)", v)) +
		lenovoCall("fan", "GetSmartFanMode()"))
	got, ok := parseKV(out)["fan"]
	return got, ok
}

// ---------------------------------------------------------------- processes

var protectedProcs = map[string]bool{
	"explorer": true, "dwm": true, "svchost": true, "csrss": true, "winlogon": true,
	"lsass": true, "services": true, "smss": true, "wininit": true, "system": true,
	"registry": true, "fontdrvhost": true, "sihost": true, "ctfmon": true,
	"[system process]": true, "taskhostw": true, "runtimebroker": true,
	"searchhost": true, "startmenuexperiencehost": true, "shellexperiencehost": true,
}

type procInfo struct {
	PID  uint32
	Name string // normalized
	Exe  string // as reported
}

func listProcs() []procInfo {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer syscall.CloseHandle(snap)
	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	var out []procInfo
	for err = syscall.Process32First(snap, &pe); err == nil; err = syscall.Process32Next(snap, &pe) {
		exe := syscall.UTF16ToString(pe.ExeFile[:])
		out = append(out, procInfo{PID: pe.ProcessID, Name: NormProc(exe), Exe: exe})
	}
	return out
}

func procAlive(procs []procInfo, pid uint32, name string) bool {
	for _, p := range procs {
		if p.PID == pid {
			return p.Name == name
		}
	}
	return false
}

func procCPUTime(pid uint32) (int64, bool) {
	h, err := syscall.OpenProcess(processQueryLtd, false, pid)
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(h)
	var c, e, k, u syscall.Filetime
	if syscall.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0, false
	}
	ft := func(f syscall.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return ft(k) + ft(u), true
}

func procPath(pid uint32) string {
	h, err := syscall.OpenProcess(processQueryLtd, false, pid)
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	r, _, _ := pQueryImageName.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}

func foregroundPID() uint32 {
	hwnd, _, _ := pGetForegroundWindow.Call()
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	pGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// setPriority changes the priority class and returns the previous one.
func setPriority(pid uint32, class uint32) (uint32, bool) {
	h, err := syscall.OpenProcess(processSetInfo|processQueryLtd, false, pid)
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(h)
	old, _, _ := pGetPriorityClass.Call(uintptr(h))
	if old == 0 {
		old = prioNormal
	}
	r, _, _ := pSetPriorityClass.Call(uintptr(h), uintptr(class))
	return uint32(old), r != 0
}

// setEco switches Windows "efficiency mode" for a process; on=false hands
// the decision back to the system.
func setEco(pid uint32, on bool) {
	if pSetProcessInfo.Find() != nil {
		return
	}
	h, err := syscall.OpenProcess(processSetInfo, false, pid)
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	st := powerThrottling{Version: 1}
	if on {
		st.ControlMask, st.StateMask = 1, 1
	}
	pSetProcessInfo.Call(uintptr(h), 4, uintptr(unsafe.Pointer(&st)), unsafe.Sizeof(st))
}

// killByName terminates every process with the given normalized name.
func killByName(name string) (killed, failed int) {
	self := uint32(os.Getpid())
	for _, p := range listProcs() {
		if p.Name != name || p.PID == self || p.PID == 0 {
			continue
		}
		h, err := syscall.OpenProcess(processTerminate, false, p.PID)
		if err != nil {
			failed++
			continue
		}
		if syscall.TerminateProcess(h, 1) == nil {
			killed++
		} else {
			failed++
		}
		syscall.CloseHandle(h)
	}
	return
}

type hog struct {
	Name  string
	Exe   string
	CPU   float64 // percent of all cores
	Count int
}

// ---------------------------------------------------------------- persistent state

type savedSetting struct {
	Sub string `json:"sub"`
	Set string `json:"set"`
	AC  uint32 `json:"ac"`
	DC  uint32 `json:"dc"`
}

type state struct {
	Active          bool                      `json:"active"` // written by 1.0.0, kept in sync
	Mode            string                    `json:"mode"`
	Schemes         map[string][]savedSetting `json:"schemes"`
	OverlayAC       string                    `json:"overlay_ac"`
	OverlayDC       string                    `json:"overlay_dc"`
	Brightness      int                       `json:"brightness"`
	BrightnessSaved bool                      `json:"brightness_saved"`
	FanPrev         int                       `json:"fan_prev"`
	FanSaved        bool                      `json:"fan_saved"`
	RefreshPrev     uint32                    `json:"refresh_prev"`
	RefreshSaved    bool                      `json:"refresh_saved"`
	LastDrawW       float64                   `json:"last_draw_w"`
	Services        []string                  `json:"services,omitempty"` // stopped by blackout mode
	BTOff           bool                      `json:"bt_off"`
	WifiOff         bool                      `json:"wifi_off"`
	LimitLifted     bool                      `json:"limit_lifted"`
	OutageStart     string                    `json:"outage_start"`
	OutagePct       int                       `json:"outage_pct"`
	BankUsedWh      float64                   `json:"bank_used_wh"`
	FirstRunDone    bool                      `json:"first_run_done"`
	BankLeftWh      float64                   `json:"bank_left_wh"`
	BankKnown       bool                      `json:"bank_known"`
	BankMarkPct     *float64                  `json:"bank_mark_pct,omitempty"`  // charge the user gave last time
	BankModelWh     float64                   `json:"bank_model_wh"`            // counted since then, without the factor
	BankFactor      float64                   `json:"bank_factor,omitempty"`    // learned from the corrections
	Draws           map[string]float64        `json:"draws,omitempty"`          // remembered draw by kind of work, W
	LastSeen        string                    `json:"last_seen,omitempty"`      // the last time the program was surely running
	SeenNvidia      bool                      `json:"seen_nvidia,omitempty"`    // a separate NVIDIA card has been seen on this laptop
	MeasureBaseW    float64                   `json:"measure_base_w,omitempty"` // the last comparison: draw without saving
	MeasureSaveW    float64                   `json:"measure_save_w,omitempty"` // and with it
	MeasureAt       string                    `json:"measure_at,omitempty"`
	CleanLast       string                    `json:"clean_last,omitempty"`
	CleanFreed      int64                     `json:"clean_freed,omitempty"`
	Autoruns        []string                  `json:"autoruns,omitempty"`
	SetupDone       bool                      `json:"setup_done"`
	Games           []string                  `json:"games,omitempty"`     // full paths of the games seen so far
	GPUPrefs        map[string]string         `json:"gpu_prefs,omitempty"` // graphics choice to put back, by game path
}

// tracked is the game or the video player the current automatic mode follows.
type tracked struct {
	Kind     string
	PID      uint32
	Name     string
	Exe      string
	OldPrio  uint32
	PrioSet  bool
	Throttle map[uint32]thr
	Manual   bool // chosen by the user, stays until changed
	FanSet   bool
	FanPrev  int

	ThrottleLogged bool
}

type thr struct {
	Name string
	Prio uint32
}

// ---------------------------------------------------------------- app

type app struct {
	hwnd      uintptr
	dir       string
	cfg       Config
	cfgMod    time.Time
	st        state
	icons     [4]uintptr
	lastTip   string
	lastIcon  uintptr
	taskbarUp uint32
	closing   bool
	elevated  bool

	bat        batteryState
	batOK      bool
	prevAC     int // -1 unknown, 0 battery, 1 external
	acTicks    int
	chargeWarn bool
	draws      []float64 // the last minute of the draw on battery, W
	drawsLong  []float64 // the last five minutes: the time left should not jump with every spike
	bankLow    bool      // the user has been told the power bank is nearly empty
	ticks      int
	ovLogged   bool

	track      *tracked
	pendingPID uint32
	declined   map[uint32]bool

	prevCPU   map[uint32]int64
	prevCPUAt time.Time
	hogs      []hog

	lastToggle time.Time

	svcBusy    bool
	bt, wifi   radioCtl
	gpu        string
	gpuUsers   []smiProc
	gpuBusy    bool
	gpuLogged  bool
	gpuAsked   bool
	gpuTicks   int
	gpuAwake   int
	gpuCalm    int // calm answers of the video card in a row
	limitKnown bool
	limitOn    bool
	warned     int
	lastTick   time.Time
	widget     uintptr
	sched      schedCtl
	widgetKey  string
	widgetErr  bool

	caps        caps
	api         *apiServer
	clean       cleanView
	health      healthView
	prevMode    string
	tgWarned    int
	bankAsked   time.Time
	uiAsk       time.Time // the window was asked to show the power bank field
	updateRel   releaseInfo
	updating    bool
	updateErr   string
	meas        *measureRun // the comparison with and without saving, while it runs
	quiet       bool        // no notifications: the mode is switched by the program itself
	update      string
	updTried    time.Time
	autoSeen    bool
	autoOn      bool
	uiBroken    bool
	panelClosed time.Time

	mu   sync.Mutex
	q    []func()
	jobs chan func()
	logf *os.File
}

var theApp *app

func (a *app) log(format string, args ...any) {
	if a.logf == nil {
		return
	}
	fmt.Fprintf(a.logf, "%s  %s\r\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

func (a *app) post(f func()) {
	a.mu.Lock()
	a.q = append(a.q, f)
	a.mu.Unlock()
	pPostMessage.Call(a.hwnd, wmPost, 0, 0)
}

func (a *app) drain() {
	a.mu.Lock()
	q := a.q
	a.q = nil
	a.mu.Unlock()
	for _, f := range q {
		f()
	}
}

func (a *app) work(f func()) {
	select {
	case a.jobs <- f:
	default:
		a.log("job queue is full, job dropped")
	}
}

func pickDataDir() string {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		probe := filepath.Join(dir, ".write-test")
		if f, err := os.Create(probe); err == nil {
			f.Close()
			os.Remove(probe)
			return dir
		}
	}
	dir := filepath.Join(os.Getenv("APPDATA"), appName)
	os.MkdirAll(dir, 0o755)
	return dir
}

func (a *app) cfgPath() string   { return filepath.Join(a.dir, "config.ini") }
func (a *app) statePath() string { return filepath.Join(a.dir, "state.json") }

func (a *app) loadConfig() {
	data, err := os.ReadFile(a.cfgPath())
	if err != nil {
		a.cfg = DefaultConfig()
		a.saveConfig()
		return
	}
	a.cfg = ParseConfig(string(data))
	a.cfg.Upgrade()
	if fi, err := os.Stat(a.cfgPath()); err == nil {
		a.cfgMod = fi.ModTime()
	}
}

func (a *app) saveConfig() {
	if err := os.WriteFile(a.cfgPath(), []byte(a.cfg.Serialize()), 0o644); err != nil {
		a.log("config save failed: %v", err)
	}
	if fi, err := os.Stat(a.cfgPath()); err == nil {
		a.cfgMod = fi.ModTime()
	}
}

func (a *app) reloadConfigIfChanged() {
	fi, err := os.Stat(a.cfgPath())
	if err != nil || fi.ModTime().Equal(a.cfgMod) {
		return
	}
	a.loadConfig()
	a.log("config reloaded: %+v", a.cfg)
	a.setHotkey()
	a.reapply()
}

func (a *app) loadState() {
	a.st = state{}
	if data, err := os.ReadFile(a.statePath()); err == nil {
		json.Unmarshal(data, &a.st)
	}
	if a.st.Schemes == nil {
		a.st.Schemes = map[string][]savedSetting{}
	}
	if a.st.Active && a.st.Mode == modeNone {
		a.st.Mode = modeBlackout
	}
}

func (a *app) saveState() {
	a.st.Active = a.st.Mode == modeBlackout
	data, _ := json.MarshalIndent(a.st, "", "  ")
	tmp := a.statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		a.log("state save failed: %v", err)
		return
	}
	os.Remove(a.statePath())
	if err := os.Rename(tmp, a.statePath()); err != nil {
		a.log("state rename failed: %v", err)
	}
}

// ---------------------------------------------------------------- power: apply and restore

func (a *app) onAC() bool { return a.batOK && a.bat.AcOnLine != 0 }

func (a *app) desired() ([]target, string) {
	switch a.st.Mode {
	case modeBlackout:
		if a.track != nil && a.track.Kind == modeLite {
			return liteTargets(a.cfg), overlayEfficiency
		}
		return savingTargets(a.cfg), overlayEfficiency
	case modeFilm, modeWork, modeQuiet:
		return savingTargets(a.cfg), overlayEfficiency
	case modeGame, modeBuild:
		return gameTargets(a.cfg), overlayPerformance
	case modeCall:
		return callTargets(), overlayBalanced
	}
	return nil, ""
}

// applyPower brings the current power plan and power mode in line with the
// active mode. It is idempotent and saves original values before changing them.
func (a *app) applyPower() (problems int) {
	list, wantOverlay := a.desired()
	if list == nil {
		return 0
	}

	// Power mode is read first, before energy saver has a chance to change it.
	slot := &a.st.OverlayDC
	if a.onAC() {
		slot = &a.st.OverlayAC
	}
	if cur, ok := getOverlay(); ok && guidStr(cur) != wantOverlay {
		if setOverlay(mustGUID(wantOverlay)) {
			if *slot == "" {
				*slot = guidStr(cur)
			}
			if !a.ovLogged {
				a.log("power mode: %s -> %s", guidStr(cur), wantOverlay)
			}
		} else if !a.ovLogged {
			a.log("power mode: could not be changed")
		}
		a.ovLogged = true
	}

	scheme, ok := activeScheme()
	if !ok {
		a.log("cannot read the active power plan")
		return 1
	}
	key := guidStr(scheme)
	if _, done := a.st.Schemes[key]; done {
		return 0
	}
	saved := []savedSetting{}
	for _, t := range list {
		sub, set := mustGUID(t.Sub), mustGUID(t.Set)
		ac, ok1 := readIdx(true, scheme, sub, set)
		dc, ok2 := readIdx(false, scheme, sub, set)
		if !ok1 || !ok2 {
			a.log("skip %s: not available on this system", t.Name)
			continue
		}
		wantAC, wantDC := t.want(ac, t.AC), t.want(dc, t.DC)
		if wantAC == ac && wantDC == dc {
			continue
		}
		r1, r2 := uintptr(0), uintptr(0)
		if wantAC != ac {
			r1 = writeIdx(true, scheme, sub, set, wantAC)
		}
		if wantDC != dc {
			r2 = writeIdx(false, scheme, sub, set, wantDC)
		}
		if r1 != 0 || r2 != 0 {
			problems++
			a.log("FAILED %s: error ac=%d dc=%d", t.Name, r1, r2)
		} else {
			a.log("set %s: ac %d -> %d, dc %d -> %d", t.Name, ac, wantAC, dc, wantDC)
		}
		saved = append(saved, savedSetting{Sub: t.Sub, Set: t.Set, AC: ac, DC: dc})
	}
	a.st.Schemes[key] = saved
	if !setActiveScheme(scheme) {
		problems++
		a.log("FAILED to re-activate the power plan")
	}
	return problems
}

func (a *app) restorePower() {
	for key, saved := range a.st.Schemes {
		scheme, ok := parseGUID(key)
		if !ok {
			continue
		}
		for _, s := range saved {
			sub, ok1 := parseGUID(s.Sub)
			set, ok2 := parseGUID(s.Set)
			if !ok1 || !ok2 {
				continue
			}
			r1 := writeIdx(true, scheme, sub, set, s.AC)
			r2 := writeIdx(false, scheme, sub, set, s.DC)
			if r1 != 0 || r2 != 0 {
				a.log("FAILED restore %s: error ac=%d dc=%d", s.Set, r1, r2)
			}
		}
		a.log("restored power plan %s (%d settings)", key, len(saved))
	}
	a.st.Schemes = map[string][]savedSetting{}
	if cur, ok := activeScheme(); ok {
		setActiveScheme(cur)
	}
	a.restoreOverlayForCurrentSource()
	a.ovLogged = false
}

// Windows keeps the power mode per power source, so each source is restored
// when the laptop is actually on it.
func (a *app) restoreOverlayForCurrentSource() {
	slot := &a.st.OverlayDC
	if a.onAC() {
		slot = &a.st.OverlayAC
	}
	if *slot == "" {
		return
	}
	if g, ok := parseGUID(*slot); ok {
		setOverlay(g)
		a.log("power mode restored to %s", *slot)
	}
	*slot = ""
}

// reapply is used after a setting that affects the active mode has changed.
func (a *app) reapply() {
	if a.st.Mode == modeNone {
		return
	}
	a.restorePower()
	a.applyPower()
	a.saveState()
}

// ---------------------------------------------------------------- blackout mode

func (a *app) enable(reason string) {
	if a.st.Mode == modeBlackout {
		return
	}
	a.log("ENABLE blackout (%s), level=%s", reason, a.cfg.Level)
	if a.track != nil && a.track.Kind != modeFilm {
		a.leaveTrack("blackout mode")
	}
	if a.st.Mode != modeNone {
		a.restorePower()
	}
	a.st.Mode = modeBlackout
	problems := a.applyPower()
	if !a.quiet || a.st.OutageStart == "" { // a measurement in the middle of an outage does not split its record
		a.st.OutageStart, a.st.OutagePct, a.st.BankUsedWh = time.Now().Format(time.RFC3339), a.percent(), 0
	}
	a.saveState()
	a.gpu, a.gpuUsers, a.gpuTicks, a.gpuAwake, a.gpuCalm, a.gpuAsked, a.gpuLogged = gpuUnknown, nil, 0, 0, 0, false, false
	if a.cfg.LiteGame && a.cfg.LiteGPU {
		a.liteGPUApply()
		a.saveState()
	}
	a.pauseServices()
	a.syncRadios()

	killed := 0
	for _, name := range a.cfg.Kill {
		if protectedProcs[name] {
			continue
		}
		k, f := killByName(name)
		killed += k
		if k+f > 0 {
			a.log("kill %s: %d closed, %d failed", name, k, f)
		}
	}

	if a.cfg.SetRefresh && !a.st.RefreshSaved {
		want := uint32(a.cfg.RefreshHz)
		if dm, ok := currentDisplay(); ok && dm.Frequency > want && hasRefresh(dm, want) {
			if setRefresh(want) {
				a.st.RefreshPrev, a.st.RefreshSaved = dm.Frequency, true
				a.saveState()
			}
			a.log("refresh rate: %d -> %d Hz, changed=%v", dm.Frequency, want, a.st.RefreshSaved)
		}
	}
	profileNote := ""
	if a.cfg.QuietProfile {
		if !a.elevated {
			profileNote = " Профиль Lenovo не переключён: нужны права администратора (окно программы, «Обзор»)."
		} else {
			a.work(func() {
				info, raw := lenovoQuery()
				a.post(func() {
					cur, ok := info["fan"]
					a.log("thermal profile before blackout: %s", raw)
					if !ok || a.st.Mode != modeBlackout || a.st.FanSaved || (cur != 2 && cur != 3) {
						return
					}
					a.st.FanPrev, a.st.FanSaved = cur, true
					a.saveState()
					a.work(func() {
						got, ok := lenovoSetFan(1)
						a.post(func() {
							a.log("thermal profile: %d -> 1, now %d (readable=%v)", cur, got, ok)
							if !ok || got != 1 {
								a.st.FanSaved = false
								a.saveState()
							}
						})
					})
				})
			})
		}
	}

	watching := a.track != nil && a.track.Kind == modeFilm
	if a.cfg.SetBrightness && !watching {
		want := a.cfg.Brightness
		a.work(func() {
			cur, ok := readBrightness()
			changed := false
			if ok && cur > want {
				changed = setBrightness(want)
			}
			a.post(func() {
				a.log("brightness: read ok=%v current=%d target=%d changed=%v", ok, cur, want, changed)
				if !changed {
					return
				}
				if a.st.Mode != modeBlackout { // switched off in the meantime
					a.work(func() { setBrightness(cur) })
					return
				}
				if !a.st.BrightnessSaved {
					a.st.Brightness = cur
					a.st.BrightnessSaved = true
					a.saveState()
				}
			})
		})
	}

	a.refresh()
	text := "Экономия включена."
	if a.cfg.Level == "max" {
		text = "Экономия включена, турбо-буст выключен."
	}
	if killed > 0 {
		text += fmt.Sprintf(" Закрыто фоновых процессов: %d.", killed)
	}
	if problems > 0 {
		text += " Часть настроек не применилась, подробности в журнале."
	}
	text += profileNote
	if reason == "auto" {
		a.balloon("Розетка пропала, режим включён", text, problems > 0)
	} else {
		a.balloon("Режим отключения включён", text, problems > 0)
	}
}

func (a *app) disable(reason string) {
	if a.st.Mode != modeBlackout {
		return
	}
	a.log("DISABLE blackout (%s)", reason)
	if a.track != nil && a.track.Kind == modeLite {
		a.leaveTrack("blackout mode is off")
	}
	a.liteGPURestore()
	a.restorePower()
	a.st.Mode = modeNone
	if !a.quiet {
		a.journal()
	}
	a.resumeServices()
	a.restoreChargeLimit()
	a.gpu, a.gpuUsers = gpuUnknown, nil
	if a.st.BrightnessSaved {
		v := a.st.Brightness
		a.st.BrightnessSaved = false
		a.work(func() {
			ok := setBrightness(v)
			a.post(func() { a.log("brightness restored to %d ok=%v", v, ok) })
		})
	}
	if a.st.RefreshSaved {
		ok := setRefresh(a.st.RefreshPrev)
		a.log("refresh rate restored to %d Hz ok=%v", a.st.RefreshPrev, ok)
		a.st.RefreshSaved = false
	}
	if a.st.FanSaved {
		v := a.st.FanPrev
		a.st.FanSaved = false
		a.work(func() {
			got, ok := lenovoSetFan(v)
			a.post(func() { a.log("thermal profile restored to %d, now %d (readable=%v)", v, got, ok) })
		})
	}
	if a.track != nil && a.track.Kind == modeFilm {
		a.st.Mode = modeFilm
		a.applyPower()
	}
	a.saveState()
	a.syncRadios()
	a.refresh()
	a.balloon("Режим отключения выключен", "Обычные настройки питания возвращены.", false)
}

func (a *app) toggle() {
	if time.Since(a.lastToggle) < 700*time.Millisecond {
		return
	}
	if a.meas != nil { // the user takes the switch back
		a.meas = nil
		a.log("measurement ended: the mode was switched by hand")
	}
	a.lastToggle = time.Now()
	if a.st.Mode == modeBlackout {
		a.disable("click")
	} else {
		a.enable("click")
	}
}

// ---------------------------------------------------------------- game and film modes

func inList(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

// watchForeground decides, from the window the user is looking at, whether
// a game or a film has started.
func (a *app) watchForeground() {
	if a.track != nil && (a.track.Kind == modeGame || a.track.Kind == modeLite || a.track.Manual) {
		return
	}
	pid := foregroundPID()
	if pid == 0 || pid == uint32(os.Getpid()) || pid == a.pendingPID {
		return
	}
	if a.track != nil && a.track.PID == pid {
		return
	}
	path := procPath(pid)
	if path == "" {
		return
	}
	name := NormProc(filepath.Base(path))

	if a.cfg.GameMode && a.st.Mode != modeBlackout && a.onAC() && !a.declined[pid] && IsGamePath(path, a.cfg) {
		a.pendingPID = pid
		exe := filepath.Base(path)
		a.st.Games = RememberGame(a.st.Games, path, maxKnownGames)
		a.work(func() {
			info, raw := lenovoQuery()
			a.post(func() { a.enterGame(pid, name, exe, info, raw) })
		})
		return
	}
	if a.cfg.LiteGame && a.st.Mode == modeBlackout && IsGamePath(path, a.cfg) {
		a.enterLite(pid, name, filepath.Base(path), path)
		return
	}
	if a.cfg.FilmMode && a.track == nil && IsPlayer(name, a.cfg) {
		a.enterFilm(pid, name, filepath.Base(path))
		return
	}
	if a.cfg.CallMode && a.track == nil && a.st.Mode == modeNone && inList(a.cfg.CallProcs, name) {
		a.enterAuto(modeCall, pid, name, filepath.Base(path), false)
	}
}

const maxKnownGames = 40

// enterLite follows a game started while blackout mode is on. The saving
// stays, the game only gets priority and a little more processor.
func (a *app) enterLite(pid uint32, name, exe, path string) {
	if a.track != nil {
		a.leaveTrack("game started")
	}
	a.log("light game: %s (pid %d)", exe, pid)
	t := &tracked{Kind: modeLite, PID: pid, Name: name, Exe: exe, Throttle: map[uint32]thr{}}
	a.track = t
	a.restorePower()
	problems := a.applyPower()
	if a.cfg.GamePriority {
		t.OldPrio, t.PrioSet = setPriority(pid, prioHigh)
		a.log("game priority raised: %v", t.PrioSet)
	}
	a.throttleScan(listProcs())
	a.st.Games = RememberGame(a.st.Games, path, maxKnownGames)
	if a.cfg.LiteGPU {
		a.liteGPUApply()
	}
	a.saveState()
	a.refresh()
	gpuPref := a.cfg.LiteGPU
	a.work(func() {
		users, note := gpuHolders()
		a.post(func() {
			onCard := false
			for _, u := range users {
				if u.PID == pid {
					onCard = true
				}
			}
			a.log("light game on the NVIDIA card: %v (%s)", onCard, strings.Join(strings.Fields(note), " "))
			if a.track != t {
				return
			}
			switch {
			case onCard && gpuPref:
				a.balloon("Игра идёт на видеокарте NVIDIA",
					"Это около 20 Вт сверху. Сохранитесь и перезапустите игру: без света она запустится на встроенной графике.", true)
			case onCard:
				a.balloon("Игра идёт на видеокарте NVIDIA",
					"Это около 20 Вт сверху. В окне программы, «Профили» → «Игра без света», можно включить встроенную графику.", true)
			case problems > 0:
				a.balloon("Игра без света: "+exe, "Часть настроек не применилась, подробности в журнале.", true)
			default:
				a.balloon("Игра без света: "+exe, "Экономия остаётся, игре дан приоритет, фоновые программы придержаны.", false)
			}
		})
	})
}

func (a *app) enterGame(pid uint32, name, exe string, info map[string]int, raw string) {
	a.pendingPID = 0
	if a.st.Mode == modeBlackout || !a.onAC() || !a.cfg.GameMode || !procAlive(listProcs(), pid, name) {
		return
	}
	if a.track != nil {
		if a.track.Kind == modeGame {
			return
		}
		a.leaveTrack("game started")
	}
	a.log("game detected: %s (pid %d), charger info: %s", exe, pid, raw)
	acfit, ok1 := info["acfit"]
	charge, ok2 := info["charge"]
	if a.cfg.StrictCharger && ok1 && ok2 && acfit != 1 && charge != 1 {
		a.declined[pid] = true
		a.log("game mode skipped: low-power charger")
		a.balloon("Игровой режим не включён", "Подключена слабая зарядка. Для игр нужен родной блок питания.", true)
		return
	}

	t := &tracked{Kind: modeGame, PID: pid, Name: name, Exe: exe, Throttle: map[uint32]thr{}}
	a.track = t
	a.st.Mode = modeGame
	problems := a.applyPower()

	if a.cfg.GamePriority {
		t.OldPrio, t.PrioSet = setPriority(pid, prioHigh)
		a.log("game priority raised: %v", t.PrioSet)
	}
	for _, n := range a.cfg.GameKill {
		if protectedProcs[n] || n == name {
			continue
		}
		if k, f := killByName(n); k+f > 0 {
			a.log("kill %s: %d closed, %d failed", n, k, f)
		}
	}
	a.throttleScan(listProcs())

	if cur, ok := info["fan"]; a.cfg.GameThermal && ok && (cur == 1 || cur == 2) {
		a.st.FanPrev, a.st.FanSaved = cur, true
		a.work(func() {
			got, ok := lenovoSetFan(3)
			a.post(func() {
				a.log("thermal profile: %d -> 3, now %d (readable=%v)", cur, got, ok)
				if !ok || got != 3 {
					a.st.FanSaved = false
					a.saveState()
					a.balloon("Профиль охлаждения не переключился", "Нажмите Fn+Q и выберите режим производительности.", true)
				}
			})
		})
	}
	a.saveState()
	a.refresh()
	text := "Питание на максимум, фоновые программы придержаны."
	if _, ok := info["fan"]; a.cfg.GameThermal && !ok && a.caps.Lenovo() {
		text += " Профиль охлаждения переключите сами: Fn+Q."
	}
	if problems > 0 {
		text = "Часть настроек не применилась, подробности в журнале."
	}
	a.balloon("Игровой режим: "+exe, text, problems > 0)
}

func (a *app) enterFilm(pid uint32, name, exe string) {
	a.log("film mode: %s (pid %d)", exe, pid)
	a.track = &tracked{Kind: modeFilm, PID: pid, Name: name, Exe: exe, Throttle: map[uint32]thr{}}
	if a.st.Mode == modeNone {
		a.st.Mode = modeFilm
		a.applyPower()
	}
	a.throttleScan(listProcs())
	a.saveState()
	a.refresh()
	text := "Экономный план питания, фоновые программы придержаны."
	if a.st.Mode == modeBlackout {
		text = "Режим отключения остаётся включённым, фоновые программы придержаны."
	}
	a.balloon("Режим кино: "+exe, text, false)
	if name == "vlc" {
		a.checkVLC()
	}
	a.syncRadios()
}

// leaveTrack ends the automatic mode and puts everything back.
func (a *app) leaveTrack(reason string) {
	t := a.track
	if t == nil {
		return
	}
	a.track = nil
	a.log("%s mode off (%s): %s", t.Kind, reason, t.Exe)
	procs := listProcs()
	for pid, th := range t.Throttle {
		if procAlive(procs, pid, th.Name) {
			setPriority(pid, th.Prio)
			setEco(pid, false)
		}
	}
	if t.PrioSet && procAlive(procs, t.PID, t.Name) {
		setPriority(t.PID, t.OldPrio)
	}
	if a.st.Mode == t.Kind {
		a.restorePower()
		a.st.Mode = modeNone
	}
	if t.Kind == modeLite && a.st.Mode == modeBlackout {
		a.restorePower() // back to plain saving
		a.applyPower()
	}
	if t.FanSet {
		v := t.FanPrev
		a.work(func() {
			got, ok := lenovoSetFan(v)
			a.post(func() { a.log("thermal profile restored to %d, now %d (readable=%v)", v, got, ok) })
		})
	}
	if t.Kind == modeGame && a.st.FanSaved {
		v := a.st.FanPrev
		a.st.FanSaved = false
		a.work(func() {
			got, ok := lenovoSetFan(v)
			a.post(func() { a.log("thermal profile restored to %d, now %d (readable=%v)", v, got, ok) })
		})
	}
	a.saveState()
	a.syncRadios()
	a.refresh()
}

// throttleScan moves the listed background programs to efficiency mode.
// It is repeated so that newly started browser processes are covered too.
func (a *app) throttleScan(procs []procInfo) {
	t := a.track
	if t == nil || !a.cfg.GameThrottle || (t.Kind != modeGame && t.Kind != modeLite && t.Kind != modeFilm) {
		return
	}
	self := uint32(os.Getpid())
	n := 0
	for _, p := range procs {
		if p.PID == self || p.PID == t.PID || p.Name == t.Name || !inList(a.cfg.Throttle, p.Name) {
			continue
		}
		if _, done := t.Throttle[p.PID]; done {
			continue
		}
		old, ok := setPriority(p.PID, prioBelowNormal)
		if !ok {
			continue
		}
		setEco(p.PID, true)
		t.Throttle[p.PID] = thr{Name: p.Name, Prio: old}
		n++
	}
	if n > 0 && !t.ThrottleLogged {
		t.ThrottleLogged = true // browsers start new processes all the time, one line is enough
		a.log("background programs held back: %d processes, new ones will follow quietly", n)
	}
}

func (a *app) unthrottleAll() {
	t := a.track
	if t == nil {
		return
	}
	procs := listProcs()
	for pid, th := range t.Throttle {
		if procAlive(procs, pid, th.Name) {
			setPriority(pid, th.Prio)
			setEco(pid, false)
		}
	}
	t.Throttle = map[uint32]thr{}
}

// ---------------------------------------------------------------- monitoring

func (a *app) avgDraw() (float64, bool) {
	if len(a.draws) == 0 {
		return 0, false
	}
	s := 0.0
	for _, d := range a.draws {
		s += d
	}
	return s / float64(len(a.draws)), true
}

// etaDraw is the draw used for the time left: the average of the last five
// minutes once there is at least a minute of it.
func (a *app) etaDraw() float64 {
	list := a.drawsLong
	if len(list) < 12 {
		list = a.draws
	}
	if len(list) == 0 {
		return 0
	}
	s := 0.0
	for _, d := range list {
		s += d
	}
	return s / float64(len(list))
}

// bankLeft is the energy left in the power bank as far as it is known, W*h.
func (a *app) bankLeft() float64 {
	if a.st.BankKnown {
		return a.st.BankLeftWh
	}
	return math.Max(float64(a.cfg.PowerbankWh)-a.st.BankUsedWh, 0)
}

// setBankPct takes the charge shown on the power bank itself and corrects
// the estimate and the factor used to count what the bank gives away.
func (a *app) setBankPct(pct float64) error {
	if a.cfg.PowerbankWh <= 0 {
		return errors.New("nobank")
	}
	if pct < 0 || pct > 100 || math.IsNaN(pct) {
		return errors.New("pct")
	}
	c := bankCalib{CapWh: float64(a.cfg.PowerbankWh), MarkPct: -1, ModelWh: a.st.BankModelWh, Factor: a.st.BankFactor}
	if a.st.BankMarkPct != nil {
		c.MarkPct = *a.st.BankMarkPct
	}
	before := a.bankLeft()
	left, c := BankFix(c, pct)
	a.st.BankLeftWh, a.st.BankKnown, a.st.BankModelWh, a.st.BankFactor = left, true, c.ModelWh, c.Factor
	a.bankLow = left < c.CapWh/10
	mark := c.MarkPct
	a.st.BankMarkPct = &mark
	a.saveState()
	a.log("power bank charge set to %.0f%%: %.1f Wh left (counted %.1f), factor %.2f", pct, left, before, BankFactor(c.Factor))
	return nil
}

// modeKey is the mode as it goes to power.csv: blackout mode with what is
// going on inside it.
func (a *app) modeKey() string {
	mode := a.st.Mode
	if mode == modeNone {
		mode = "normal"
	}
	if a.track != nil && a.st.Mode == modeBlackout {
		mode += "+" + a.track.Kind
	}
	return mode
}

// lastDraw is the best guess of what the laptop draws right now while it
// cannot be measured (on external power): the draw remembered for this
// kind of work, or the last one measured on battery.
func (a *app) lastDraw() float64 {
	if w := a.st.Draws[DrawKey(a.modeKey(), a.gpu)]; w > 1 {
		return w
	}
	if w := a.st.Draws[a.modeKey()]; w > 1 {
		return w
	}
	return a.st.LastDrawW
}

func (a *app) percent() int {
	if !a.batOK || a.bat.MaxCapacity == 0 {
		return 0
	}
	return clamp(int(float64(a.bat.RemainingCapacity)*100/float64(a.bat.MaxCapacity)+0.5), 0, 100)
}

func (a *app) tick() {
	if a.closing {
		return
	}
	a.ticks++
	a.reloadConfigIfChanged()

	now := time.Now()
	dt := now.Sub(a.lastTick).Hours()
	a.lastTick = now
	b, ok := readBattery()
	a.bat, a.batOK = b, ok && b.BatteryPresent != 0
	if w := a.lastDraw(); a.batOK && b.AcOnLine != 0 && a.st.Mode == modeBlackout && w > 1 && dt > 0 && dt < 0.02 {
		// what the power bank has given: the work of the laptop plus battery charging
		if r := float64(b.Rate) / 1000; b.Charging != 0 && r > 0 && r < 300 {
			w += r
		}
		a.st.BankModelWh += w * dt
		w *= BankFactor(a.st.BankFactor)
		a.st.BankUsedWh += w * dt
		if a.st.BankKnown {
			a.st.BankLeftWh -= w * dt
			if a.st.BankLeftWh < 0 {
				a.st.BankLeftWh = 0
			}
			if full := float64(a.cfg.PowerbankWh); full > 0 && a.cfg.BankWarn && !a.bankLow && a.st.BankLeftWh < full/10 {
				a.bankLow = true
				a.balloon("Павербанк почти сел", fmt.Sprintf("По расчёту в нём около %.0f%%. Дальше ноутбук пойдёт от своей батареи: в ней %d%%.",
					a.st.BankLeftWh*100/full, a.percent()), true)
			}
		}
	}
	if a.batOK {
		ac := 0
		if b.AcOnLine != 0 {
			ac = 1
		}
		if a.prevAC != -1 && ac != a.prevAC {
			a.onSourceChange(ac == 1)
		}
		if a.prevAC != ac {
			a.acTicks = 0
			a.chargeWarn = false
		}
		a.prevAC = ac
		a.acTicks++

		rate := float64(b.Rate) / 1000 // W, negative while discharging
		a.measureTick(rate)
		if ac == 0 {
			if rate < -0.5 && rate > -300 {
				a.draws = append(a.draws, -rate)
				if len(a.draws) > 12 {
					a.draws = a.draws[1:]
				}
				a.drawsLong = append(a.drawsLong, -rate)
				if len(a.drawsLong) > 60 {
					a.drawsLong = a.drawsLong[1:]
				}
				if avg, ok := a.avgDraw(); ok && len(a.draws) >= 6 {
					a.st.LastDrawW = avg
				}
			}
		} else {
			a.draws, a.drawsLong = nil, nil
			if a.st.Mode == modeBlackout && a.cfg.ChargeWarn && !a.chargeWarn && a.acTicks >= 3 &&
				b.Charging != 0 && rate > 3 && rate < 300 {
				a.chargeWarn = true
				a.balloon("Батарея ноутбука заряжается",
					fmt.Sprintf("На зарядку уходит %.0f Вт. Если это павербанк, он тратится на батарею ноутбука, а не только на работу.", rate), true)
			}
		}
	}

	slow := a.ticks%3 == 1
	if a.track != nil {
		procs := listProcs()
		switch {
		case a.track.PID != 0 && !procAlive(procs, a.track.PID, a.track.Name):
			a.leaveTrack("closed")
		case (a.track.Kind == modeGame || a.track.Kind == modeBuild) && !a.onAC():
			a.leaveTrack("charger unplugged")
		case slow:
			a.throttleScan(procs)
		}
	}
	a.watchForeground()

	if slow {
		a.sampleProcs()
		if a.st.Mode != modeNone {
			before := len(a.st.Schemes)
			oAC, oDC := a.st.OverlayAC, a.st.OverlayDC
			a.applyPower() // covers a power plan switched by the user or by vendor software
			if len(a.st.Schemes) != before || oAC != a.st.OverlayAC || oDC != a.st.OverlayDC {
				a.saveState()
			}
		}
		if len(a.declined) > 64 {
			a.declined = map[uint32]bool{}
		}
	}
	if a.ticks%12 == 0 {
		if avg, ok := a.avgDraw(); ok && !a.onAC() && len(a.draws) >= 6 {
			if a.st.Draws == nil {
				a.st.Draws = map[string]float64{}
			}
			k := DrawKey(a.modeKey(), a.gpu)
			a.st.Draws[k] = LearnDraw(a.st.Draws[k], avg)
		}
	}
	if a.ticks%60 == 0 || (a.ticks%12 == 0 && a.st.Mode == modeBlackout) {
		a.st.LastSeen = now.Format(time.RFC3339)
		a.saveState()
	}
	a.lowBatteryWarn()
	a.v2Tick()
	a.scheduleTick()
	a.powerLog()
	a.watchGPU()
	a.syncRadios()
	if a.st.Mode != modeBlackout && len(a.st.Services) > 0 {
		a.resumeServices()
	}
	a.refresh()
}

func (a *app) onSourceChange(nowAC bool) {
	a.log("power source changed: external=%v", nowAC)
	if a.elevated && (a.caps.Lenovo() || !a.caps.Ready) {
		a.work(func() {
			time.Sleep(3 * time.Second)
			info, raw := lenovoQuery()
			a.post(func() {
				a.log("lenovo after the power source change: %q", raw)
				if nowAC {
					a.mainsBack(info)
				}
			})
		})
	} else if nowAC {
		a.work(func() {
			time.Sleep(3 * time.Second)
			a.post(func() { a.mainsBack(nil) })
		})
	}
	if !nowAC && a.track != nil && a.track.Kind == modeGame {
		a.leaveTrack("charger unplugged")
	}
	if a.st.Mode == modeBlackout {
		a.applyPower()
		a.saveState()
		if nowAC {
			a.balloon("Появилось внешнее питание",
				"Если дали свет, кликните по значку, чтобы вернуть обычные настройки. Если это павербанк, ничего делать не нужно.", false)
		}
		return
	}
	if a.st.Mode == modeNone {
		// Finish a power mode restore that had to wait for this source.
		if (nowAC && a.st.OverlayAC != "") || (!nowAC && a.st.OverlayDC != "") {
			a.restoreOverlayForCurrentSource()
			a.saveState()
		}
	} else {
		a.applyPower()
		a.saveState()
	}
	if !nowAC && a.cfg.AutoEnable {
		a.enable("auto")
	}
}

func (a *app) sampleProcs() {
	now := time.Now()
	cur := map[uint32]int64{}
	type agg struct {
		delta int64
		count int
		exe   string
	}
	by := map[string]*agg{}
	self := uint32(os.Getpid())
	for _, p := range listProcs() {
		if p.PID == 0 || p.PID == self {
			continue
		}
		t, ok := procCPUTime(p.PID)
		if !ok {
			continue
		}
		cur[p.PID] = t
		g := by[p.Name]
		if g == nil {
			g = &agg{exe: p.Exe}
			by[p.Name] = g
		}
		g.count++
		if prev, ok := a.prevCPU[p.PID]; ok && t >= prev {
			g.delta += t - prev
		}
	}
	elapsed := now.Sub(a.prevCPUAt)
	if a.prevCPU != nil && elapsed > time.Second {
		total := float64(elapsed.Nanoseconds()/100) * float64(runtime.NumCPU())
		hogs := []hog{}
		for name, g := range by {
			pct := float64(g.delta) * 100 / total
			if pct >= 0.3 && !protectedProcs[name] {
				hogs = append(hogs, hog{Name: name, Exe: g.exe, CPU: pct, Count: g.count})
			}
		}
		sort.Slice(hogs, func(i, j int) bool { return hogs[i].CPU > hogs[j].CPU })
		if len(hogs) > 8 {
			hogs = hogs[:8]
		}
		a.hogs = hogs
	}
	a.prevCPU, a.prevCPUAt = cur, now
}

func (a *app) modeLine() string {
	s := ""
	switch a.st.Mode {
	case modeBlackout:
		s = "Режим отключения: ВКЛ"
	case modeGame:
		s = "Игровой режим"
	case modeFilm:
		s = "Режим кино"
	case modeBuild:
		s = "Сборка"
	case modeCall:
		s = "Созвон"
	case modeQuiet:
		s = "Тишина"
	case modeWork:
		s = "Работа, экономно"
	default:
		return "Обычный режим"
	}
	if a.track != nil && a.track.Exe != "" {
		if a.st.Mode == modeBlackout {
			if a.track.Kind == modeLite {
				s += " + игра"
			} else {
				s += " + кино"
			}
		}
		s += ": " + a.track.Exe
	}
	return s
}

func (a *app) statusLines() (string, string) {
	if !a.batOK {
		return "Батарея не найдена", ""
	}
	pct := a.percent()
	var l1 string
	rate := float64(a.bat.Rate) / 1000
	if a.bat.AcOnLine == 0 {
		if avg, ok := a.avgDraw(); ok {
			left := float64(a.bat.RemainingCapacity) / 1000 / a.etaDraw()
			l1 = fmt.Sprintf("%.1f Вт · от батареи · %d%% · ≈%s", avg, pct, FmtDur(left))
		} else {
			l1 = fmt.Sprintf("От батареи · %d%% · замеряю расход…", pct)
		}
	} else if a.bat.Charging != 0 && rate > 0.5 {
		l1 = fmt.Sprintf("Внешнее питание · %d%% · зарядка батареи +%.0f Вт", pct, rate)
	} else {
		l1 = fmt.Sprintf("Внешнее питание · %d%% · батарея не заряжается", pct)
	}
	l2 := ""
	draw := a.lastDraw()
	if avg, ok := a.avgDraw(); ok {
		draw = avg
	}
	if draw > 1 && a.st.Mode != modeGame {
		left := a.bankLeft()
		l2 = fmt.Sprintf("Павербанка хватит на ≈%s при %.0f Вт", FmtDur(left/(draw*BankFactor(a.st.BankFactor))), draw)
	}
	return l1, l2
}

// ---------------------------------------------------------------- tray

func makeIcon(size int, kind int) uintptr {
	px := RenderIcon(size, kind)
	bi := bitmapInfoHeader{Width: int32(size), Height: -int32(size), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	color, _, _ := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 || bits == nil {
		return 0
	}
	copy(unsafe.Slice((*byte)(bits), len(px)), px)
	mask, _, _ := pCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, 0)
	ii := iconInfo{FIcon: 1, Mask: mask, Color: color}
	h, _, _ := pCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	pDeleteObject.Call(color)
	pDeleteObject.Call(mask)
	return h
}

func (a *app) nid() notifyIconData {
	var n notifyIconData
	n.Size = uint32(unsafe.Sizeof(n))
	n.Hwnd = a.hwnd
	n.ID = 1
	return n
}

func (a *app) curIcon() uintptr {
	if a.track != nil && a.track.Kind == modeFilm {
		return a.icons[3] // film stays visible on top of blackout mode
	}
	switch a.st.Mode {
	case modeBlackout:
		return a.icons[1]
	case modeGame:
		return a.icons[2]
	case modeFilm:
		return a.icons[3]
	}
	return a.icons[0]
}

func (a *app) tipText() string {
	l1, _ := a.statusLines()
	return a.modeLine() + "\n" + l1
}

func (a *app) addIcon() {
	n := a.nid()
	n.Flags = nifMessage | nifIcon | nifTip
	n.CallbackMessage = wmTray
	n.Icon = a.curIcon()
	a.lastTip, a.lastIcon = a.tipText(), n.Icon
	setW(n.Tip[:], a.lastTip)
	r, _, _ := pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		a.log("tray icon could not be added")
	}
}

func (a *app) refresh() {
	a.updateWidget()
	tip, icon := a.tipText(), a.curIcon()
	if tip == a.lastTip && icon == a.lastIcon {
		return
	}
	a.lastTip, a.lastIcon = tip, icon
	n := a.nid()
	n.Flags = nifIcon | nifTip
	n.Icon = icon
	setW(n.Tip[:], tip)
	pShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

func (a *app) balloon(title, text string, warn bool) {
	if a.quiet {
		return
	}
	n := a.nid()
	n.Flags = nifInfo
	setW(n.InfoTitle[:], title)
	setW(n.Info[:], text)
	n.InfoFlags = niifInfo
	if warn {
		n.InfoFlags = niifWarn
	}
	pShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

func (a *app) removeIcon() {
	n := a.nid()
	pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&n)))
}

// ---------------------------------------------------------------- autostart

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func openRunKey() (uintptr, bool) {
	var h uintptr
	r, _, _ := pRegCreateKeyEx.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(u16(runKey))), 0, 0, 0,
		0x0001|0x0002, 0, uintptr(unsafe.Pointer(&h)), 0)
	return h, r == 0
}

func autostartCmd() string {
	exe, _ := os.Executable()
	return `"` + exe + `"`
}

func autostartValue() (string, bool) {
	h, ok := openRunKey()
	if !ok {
		return "", false
	}
	defer pRegCloseKey.Call(h)
	var typ, size uint32
	buf := make([]uint16, 1024)
	size = uint32(len(buf) * 2)
	r, _, _ := pRegQueryValueEx.Call(h, uintptr(unsafe.Pointer(u16(appName))), 0,
		uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return "", false
	}
	return syscall.UTF16ToString(buf), true
}

// With administrator rights autostart goes through the Task Scheduler,
// because the Run key would start the program without them.
func schtasksExe() string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "schtasks.exe")
}

func runHidden(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(oemText(out)), err
}

func taskExists() bool {
	_, err := runHidden(schtasksExe(), "/Query", "/TN", appName)
	return err == nil
}

func createTask() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return err.Error(), false
	}
	u, err := user.Current()
	if err != nil {
		return err.Error(), false
	}
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	xml := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + esc.Replace(u.Username) + `</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + esc.Replace(u.Username) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>5</Priority>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + esc.Replace(exe) + `</Command></Exec></Actions>
</Task>`
	buf := []byte{0xFF, 0xFE}
	for _, c := range utf16.Encode([]rune(strings.ReplaceAll(xml, "\n", "\r\n"))) {
		buf = append(buf, byte(c), byte(c>>8))
	}
	tmp := filepath.Join(os.TempDir(), appName+"-task.xml")
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err.Error(), false
	}
	defer os.Remove(tmp)
	out, err := runHidden(schtasksExe(), "/Create", "/TN", appName, "/XML", tmp, "/F")
	return out, err == nil
}

func autostartOn() bool {
	if _, ok := autostartValue(); ok {
		return true
	}
	return taskExists()
}

func (a *app) toggleAutostart() {
	if autostartOn() {
		setAutostart(false)
		if taskExists() {
			if !a.elevated {
				msgBox("Автозапуск настроен с правами администратора. Чтобы его выключить, перезапустите приложение с правами администратора (кнопка в окне программы, «Обзор»).", mbIconInfo)
				return
			}
			out, err := runHidden(schtasksExe(), "/Delete", "/TN", appName, "/F")
			a.log("autostart task removed: %v %s", err == nil, out)
		}
		return
	}
	if a.elevated {
		out, ok := createTask()
		a.log("autostart task created: %v %s", ok, out)
		if !ok {
			msgBox("Не удалось создать задачу автозапуска. Подробности в журнале.", mbIconWarning)
		}
		return
	}
	if !setAutostart(true) {
		msgBox("Не удалось изменить автозапуск.", mbIconWarning)
	}
}

func setAutostart(on bool) bool {
	h, ok := openRunKey()
	if !ok {
		return false
	}
	defer pRegCloseKey.Call(h)
	if !on {
		pRegDeleteValue.Call(h, uintptr(unsafe.Pointer(u16(appName))))
		return true
	}
	u := utf16.Encode([]rune(autostartCmd()))
	u = append(u, 0)
	r, _, _ := pRegSetValueEx.Call(h, uintptr(unsafe.Pointer(u16(appName))), 0, 1,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)*2))
	return r == 0
}

// ---------------------------------------------------------------- menu

const (
	idStatus = 1 + iota
	idToggle
	idOpenUI
	idBankSet
	idLists
	idLog
	idExit
	idProfileBase = 20 // + index into trayProfiles
	idHogBase     = 1000
)

const tipsText = `Это приложение не может сделать за вас, а эффект заметный.

ИГРЫ
• Заряжайте родным блоком питания: от USB-C ноутбук режет мощность.
• Lenovo Vantage → Рабочий режим ГП → «dGPU» (нужна перезагрузка). Картинка идёт напрямую с дискретной видеокарты, это плюс к FPS и ровнее кадры.
• Панель NVIDIA → Управление параметрами 3D → Режим управления электропитанием → «Максимальная производительность».

КИНО
• VLC → Инструменты → Настройки → Ввод / кодеки → «Аппаратное ускорение декодирования»: Автоматически. Без него фильм декодирует процессор и расход в разы выше.

БЕЗ СВЕТА
• Подключайте павербанк, пока заряд выше 75%. Ниже ноутбук начинает заряжать свою батарею, и расход павербанка растёт на 20–35 Вт.
• Brave: Настройки → Система → «Энергосбережение» и «Экономия памяти».
• Telegram: Настройки → Продвинутые настройки → «Батарея и анимации».
• Vantage: Over Drive выключить. Подсветка клавиатуры: Fn + Пробел.`

// trayHogCPU is the share of the whole processor from which a program is
// worth a line in the tray menu. The window lists the lighter ones too.
const trayHogCPU = 4.0

// trayProfiles are the profiles offered in the tray menu, in the order of the window.
var trayProfiles = [][2]string{{"auto", "Авто"}, {"work", "Работа"}, {"game", "Игра"}, {"film", "Кино"}, {"build", "Сборка"}, {"quiet", "Тишина"}}

// showMenu is the short menu of the tray icon: what is going on, the few
// things worth doing without opening the window, and the way into it.
// Every setting lives in the window.
func (a *app) showMenu() {
	a.reloadConfigIfChanged()
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)
	add := func(m uintptr, id int, flags uintptr, text string) {
		pAppendMenu.Call(m, mfString|flags, uintptr(id), uintptr(unsafe.Pointer(u16(text))))
	}
	sep := func(m uintptr) { pAppendMenu.Call(m, mfSeparator, 0, 0) }
	sub := func(parent uintptr, text string) uintptr {
		m, _, _ := pCreatePopupMenu.Call()
		pAppendMenu.Call(parent, mfPopup, m, uintptr(unsafe.Pointer(u16(text))))
		return m
	}
	chk := func(b bool) uintptr {
		if b {
			return mfChecked
		}
		return 0
	}

	l1, _ := a.statusLines()
	add(menu, idStatus, 0, a.modeLine())
	add(menu, idStatus, 0, l1)
	if a.gpu == gpuAwake && a.st.Mode == modeBlackout {
		add(menu, idStatus, 0, a.gpuLine())
	}
	if sl := ScheduleLine(a.schedStatus()); sl != "" {
		add(menu, idStatus, 0, "График: "+sl)
	}
	sep(menu)
	if a.st.Mode == modeBlackout {
		add(menu, idToggle, 0, "Выключить режим отключения")
	} else {
		add(menu, idToggle, 0, "Включить режим отключения")
		pm := sub(menu, "Профиль")
		cur := "auto"
		if a.track != nil && a.track.Manual {
			for name, kind := range profileKinds {
				if kind == a.track.Kind {
					cur = name
				}
			}
		}
		for i, p := range trayProfiles {
			add(pm, idProfileBase+i, chk(cur == p[0]), p[1])
		}
	}
	if a.cfg.PowerbankWh > 0 && a.batOK {
		add(menu, idBankSet, 0, a.bankLine())
	}
	var hogs []hog
	for _, h := range a.hogs {
		if h.CPU >= trayHogCPU {
			hogs = append(hogs, h)
		}
	}
	if len(hogs) > 0 {
		hm := sub(menu, "Кто грузит процессор")
		for i, h := range hogs {
			add(hm, idHogBase+i, 0, fmt.Sprintf("%s: %.1f%% ЦП, процессов: %d. Завершить…", h.Exe, h.CPU, h.Count))
		}
	}
	sep(menu)
	if a.api != nil {
		add(menu, idOpenUI, 0, "Открыть BlackoutMode")
	} else {
		add(menu, idLists, 0, "Файл настроек…")
		add(menu, idLog, 0, "Журнал…")
	}
	add(menu, idExit, 0, "Выход")

	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(a.hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmRightButton|tpmNoNotify|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y), 0, a.hwnd, 0)
	pPostMessage.Call(a.hwnd, 0, 0, 0)

	id := int(cmd)
	switch {
	case id == idToggle:
		a.lastToggle = time.Time{}
		a.toggle()
	case id >= idProfileBase && id < idProfileBase+len(trayProfiles):
		if err := a.setProfile(trayProfiles[id-idProfileBase][0]); err != nil {
			switch err.Error() {
			case "battery":
				msgBox("Этот профиль работает только на зарядке.", mbIconInfo)
			case "blackout":
				msgBox("Без света профиль выбирается сам.", mbIconInfo)
			}
		}
	case id == idBankSet:
		a.uiAsk = time.Now()
		a.openUI("")
	case id == idOpenUI:
		a.openUI("")
	case id == idLists:
		exec.Command("notepad.exe", a.cfgPath()).Start()
	case id == idLog:
		exec.Command("notepad.exe", filepath.Join(a.dir, "log.txt")).Start()
	case id == idExit:
		a.exit()
	case id >= idHogBase && id < idHogBase+len(hogs):
		h := hogs[id-idHogBase]
		q := fmt.Sprintf("Завершить %s (процессов: %d)?\n\nНесохранённые данные в этой программе пропадут.", h.Exe, h.Count)
		if msgBox(q, mbYesNo|mbIconWarning) == idYes {
			k, f := killByName(h.Name)
			a.log("manual kill %s: %d closed, %d failed", h.Name, k, f)
			if f > 0 {
				msgBox(fmt.Sprintf("Закрыто: %d, не удалось закрыть: %d (нужны права администратора).", k, f), mbIconWarning)
			}
			a.hogs = nil
		}
	}
	a.refresh()
}

// bankLine is the tray menu item of the power bank: what is left and for how long.
func (a *app) bankLine() string {
	left := a.bankLeft()
	s := fmt.Sprintf("Павербанк ≈ %.0f%%", math.Min(100, left*100/float64(a.cfg.PowerbankWh)))
	draw := a.lastDraw()
	if avg, ok := a.avgDraw(); ok {
		draw = avg
	}
	if draw > 1 && a.st.Mode != modeGame {
		s += ", хватит на ≈" + FmtDur(left/(draw*BankFactor(a.st.BankFactor)))
	}
	return s + ". Уточнить заряд…"
}

// shutdown puts back everything the automatic modes changed. Blackout mode
// is left as it is: it has to survive a restart of the app.
func (a *app) shutdown() {
	if a.closing {
		return
	}
	a.closing = true
	closeUIWindows()
	a.leaveTrack("app is closing")
	a.saveState()
	a.removeIcon()
	a.log("exit")
}

func (a *app) exit() {
	if a.st.Mode == modeBlackout {
		switch msgBox("Режим отключения сейчас включён.\n\nВернуть обычные настройки питания перед выходом?", mbYesNoCancel|mbIconQuestion) {
		case idYes:
			a.disable("exit")
		case idNo:
		default:
			return
		}
	}
	a.shutdown()
	time.Sleep(4 * time.Second) // let the last restore jobs finish
	pPostQuitMessage.Call(0)
}

// ---------------------------------------------------------------- window

func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	a := theApp
	switch uint32(msg) {
	case wmTimer:
		if a != nil && a.hwnd != 0 {
			a.tick()
		}
		return 0
	case wmPowerBroadcast:
		if a != nil && a.hwnd != 0 && wparam == 0xA {
			a.tick()
		}
		return 1
	case wmTray:
		switch uint32(lparam) & 0xffff {
		case wmLButtonUp:
			if a.cfg.Click == "panel" && a.api != nil {
				a.openUI("panel")
			} else {
				a.toggle()
			}
		case wmRButtonUp:
			a.showMenu()
		}
		return 0
	case wmPost:
		a.drain()
		return 0
	case wmHotkey:
		if a != nil && a.hwnd != 0 {
			a.toggle()
		}
		return 0
	case wmClose:
		a.shutdown()
	case wmEndSession:
		if wparam != 0 {
			a.shutdown()
		}
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	if a != nil && a.taskbarUp != 0 && uint32(msg) == a.taskbarUp {
		a.lastTip, a.lastIcon = "", 0
		a.addIcon()
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
}

func (a *app) startupReport() {
	a.log("---- %s %s started, administrator=%v, data dir: %s", appName, version, a.elevated, a.dir)
	a.log("config: %+v", a.cfg)
	a.log("state: mode=%q saved plans=%d", a.st.Mode, len(a.st.Schemes))
	if scheme, ok := activeScheme(); ok {
		a.log("active power plan: %s", guidStr(scheme))
		seen := map[string]bool{}
		for _, t := range append(savingTargets(DefaultConfig()), gameTargets(a.cfg)...) {
			if seen[t.Set] {
				continue
			}
			seen[t.Set] = true
			ac, ok1 := readIdx(true, scheme, mustGUID(t.Sub), mustGUID(t.Set))
			dc, ok2 := readIdx(false, scheme, mustGUID(t.Sub), mustGUID(t.Set))
			a.log("  %-28s ac=%d dc=%d readable=%v", t.Name, ac, dc, ok1 && ok2)
		}
	} else {
		a.log("active power plan: cannot be read")
	}
	if g, ok := getOverlay(); ok {
		a.log("power mode: %s", guidStr(g))
	} else {
		a.log("power mode: not available")
	}
	if b, ok := readBattery(); ok {
		a.log("battery: present=%d external=%d charging=%d rate=%d mW remaining=%d of %d mWh",
			b.BatteryPresent, b.AcOnLine, b.Charging, b.Rate, b.RemainingCapacity, b.MaxCapacity)
	} else {
		a.log("battery: cannot be read")
	}
	a.work(func() {
		v, ok := readBrightness()
		_, raw := lenovoQuery()
		a.post(func() {
			a.log("brightness: readable=%v value=%d", ok, v)
			a.log("lenovo profile and charger: %q", raw)
		})
	})
}

// takeOver asks every running copy (for example an older version) to quit.
// It reports false when a copy stays: one started with administrator rights
// does not listen to an ordinary one.
func takeOver() bool {
	cls := u16(wndClass)
	for i := 0; i < 30; i++ {
		h, _, _ := pFindWindow.Call(uintptr(unsafe.Pointer(cls)), 0)
		if h == 0 {
			if i > 0 {
				time.Sleep(400 * time.Millisecond)
			}
			return true
		}
		if i%5 == 0 {
			pPostMessage.Call(h, wmClose, 0, 0)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func main() {
	if len(os.Args) == 3 && strings.HasPrefix(os.Args[1], "--ui=") {
		os.Exit(runUI(strings.TrimPrefix(os.Args[1], "--ui="), os.Args[2]))
	}
	runtime.LockOSThread()

	mname := u16("Local\\" + appName + "-single-instance")
	pCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(mname)))
	// The mutex of a copy with administrator rights may be out of reach,
	// so running copies are looked for by their window.
	if !takeOver() {
		if !isElevated() && relaunchElevated() {
			return
		}
		msgBox("Приложение уже запущено: значок с молнией в трее возле часов.", mbIconInfo)
		return
	}

	if pSetDpiAwarenessCtx.Find() == nil {
		pSetDpiAwarenessCtx.Call(^uintptr(3)) // per-monitor v2
	}

	a := &app{prevAC: -1, jobs: make(chan func(), 16), declined: map[uint32]bool{}, elevated: isElevated()}
	theApp = a
	a.dir = pickDataDir()
	logPath := filepath.Join(a.dir, "log.txt")
	if data, err := os.ReadFile(logPath); err == nil && len(data) > 400*1024 {
		os.WriteFile(logPath, TrimLog(data, 400*1024, 150*1024), 0o644)
	}
	a.logf, _ = os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	defer func() {
		if r := recover(); r != nil {
			a.log("PANIC: %v", r)
			msgBox(fmt.Sprintf("Внутренняя ошибка: %v\nПодробности в журнале.", r), mbIconWarning)
		}
	}()

	a.loadConfig()
	a.saveConfig() // adds settings introduced by a newer version
	a.loadState()
	go func() {
		for f := range a.jobs {
			f()
		}
	}()

	inst, _, _ := pGetModuleHandle.Call(0)
	cls := u16(wndClass)
	wc := wndClassEx{WndProc: syscall.NewCallback(wndProc), Instance: inst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		a.log("RegisterClassEx failed: %v", err)
		msgBox("Не удалось запустить приложение (RegisterClassEx).", mbIconWarning)
		return
	}
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16(appTitle))),
		0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		a.log("CreateWindowEx failed: %v", err)
		msgBox("Не удалось запустить приложение (CreateWindowEx).", mbIconWarning)
		return
	}
	a.hwnd = hwnd

	size, _, _ := pGetSystemMetrics.Call(49) // SM_CXSMICON
	if size < 16 || size > 64 {
		size = 16
	}
	for i := range a.icons {
		a.icons[i] = makeIcon(int(size), i)
		defer pDestroyIcon.Call(a.icons[i])
	}

	tb, _, _ := pRegisterWindowMsg.Call(uintptr(unsafe.Pointer(u16("TaskbarCreated"))))
	a.taskbarUp = uint32(tb)
	if a.elevated && pChangeMsgFilterEx.Find() == nil {
		pChangeMsgFilterEx.Call(hwnd, tb, 1, 0) // let Explorer tell us the taskbar was recreated
	}

	if b, ok := readBattery(); ok {
		a.bat, a.batOK = b, b.BatteryPresent != 0
	}

	if a.st.Draws == nil {
		if p, err := os.ReadFile(filepath.Join(a.dir, "power.csv")); err == nil {
			a.st.Draws = SeedDraws(string(p))
		}
	}
	// The laptop was off or the program was closed in the middle of an
	// outage: the outage is closed at the moment the program was last seen,
	// and a new one starts now if the mode is still on.
	if seen, err := time.Parse(time.RFC3339, a.st.LastSeen); err == nil && a.st.Mode == modeBlackout &&
		a.st.OutageStart != "" && time.Since(seen) > 20*time.Minute {
		a.log("the program was not running since %s, the outage record is split there", seen.Format("2006-01-02 15:04"))
		a.journalAt(seen)
		a.st.OutageStart, a.st.OutagePct, a.st.BankUsedWh = time.Now().Format(time.RFC3339), a.percent(), 0
		a.saveState()
	}
	// A game or film mode left over from a crash or a reboot is undone.
	if a.st.Mode != modeNone && a.st.Mode != modeBlackout {
		a.log("cleaning up after an interrupted %s mode", a.st.Mode)
		a.restorePower()
		a.st.Mode = modeNone
		if a.st.FanSaved {
			v := a.st.FanPrev
			a.st.FanSaved = false
			a.work(func() { lenovoSetFan(v) })
		}
		a.saveState()
	}
	if a.st.Mode != modeBlackout && len(a.st.GPUPrefs) > 0 {
		a.liteGPURestore()
		a.saveState()
	}
	// Autostart keeps pointing at this file after an update.
	if a.elevated {
		if _, run := autostartValue(); run || taskExists() {
			out, ok := createTask()
			a.log("autostart moved to an elevated task: %v %s", ok, out)
			if ok {
				setAutostart(false)
			}
		}
	} else if v, ok := autostartValue(); ok && !strings.EqualFold(v, autostartCmd()) {
		setAutostart(true)
	}

	a.addIcon()
	a.createWidget(inst)
	a.setHotkey()
	a.startupReport()
	a.probeChargeLimit()
	a.detectCaps()
	a.startUI()
	a.tick()
	pSetTimer.Call(hwnd, 1, 5000, 0)

	if !a.st.FirstRunDone {
		a.st.FirstRunDone = true
		a.saveState()
		a.balloon(appTitle, "Значок с молнией в трее. Левый клик открывает панель, правый — короткое меню. Все настройки в окне программы.", false)
	}

	var m msgT
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	if a.logf != nil {
		a.logf.Close()
	}
}
