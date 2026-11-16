//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

// The window and the small panel run as a second copy of the program
// (started with --ui=...), so nothing of them stays in memory once they
// are closed and a problem there never touches the tray application.

var (
	dwmapi             = syscall.NewLazyDLL("dwmapi.dll")
	puDwmSetAttr       = dwmapi.NewProc("DwmSetWindowAttribute")
	puReleaseCapture   = user32.NewProc("ReleaseCapture")
	puSendMessage      = user32.NewProc("SendMessageW")
	puDestroyWindow    = user32.NewProc("DestroyWindow")
	puGetAncestor      = user32.NewProc("GetAncestor")
	puLoadImage        = user32.NewProc("LoadImageW")
	puLoadCursor       = user32.NewProc("LoadCursorW")
	puGetWindowRect    = user32.NewProc("GetWindowRect")
	puUpdateWindow     = user32.NewProc("UpdateWindow")
	puCreateSolidBrush = syscall.NewLazyDLL("gdi32.dll").NewProc("CreateSolidBrush")
)

const (
	uiClass      = appName + "UI"
	uiTitleMain  = "BlackoutMode"
	uiTitlePanel = "BlackoutMode panel"
	uiExitFailed = 3
	uiMsgDrag    = 0x8001
)

type uiHost struct {
	hwnd    uintptr
	panel   bool
	web     *edge.Chromium
	shownAt time.Time
	scale   float64
	ready   int32 // the page has drawn itself
}

// uiLog keeps a short journal of the window process next to the main one.
func uiLog(format string, args ...any) {
	path := filepath.Join(pickDataDir(), "ui.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 100*1024 {
		os.Remove(path)
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, time.Now().Format("2006-01-02 15:04:05")+"  "+format+"\r\n", args...)
		f.Close()
	}
}

var theUI *uiHost

func uiWorkArea() (l, t, r, b int32) {
	var rc struct{ L, T, R, B int32 }
	pSystemParametersInf.Call(0x30, 0, uintptr(unsafe.Pointer(&rc)), 0)
	return rc.L, rc.T, rc.R, rc.B
}

func uiWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	u := theUI
	switch uint32(msg) {
	case 0x0083: // WM_NCCALCSIZE: the whole window is ours, no caption and no frame
		if wp != 0 {
			return 0
		}
	case 0x0005: // WM_SIZE
		if u != nil && u.web != nil {
			u.web.Resize()
		}
	case 0x0003, 0x0216: // WM_MOVE, WM_MOVING
		if u != nil && u.web != nil {
			u.web.NotifyParentWindowPositionChanged()
		}
	case 0x0006: // WM_ACTIVATE
		if u == nil || u.web == nil {
			break
		}
		if wp&0xffff != 0 {
			u.web.Focus()
			break
		}
		if u.panel && time.Since(u.shownAt) > 700*time.Millisecond {
			fg, _, _ := pGetForegroundWindow.Call()
			root, _, _ := puGetAncestor.Call(fg, 2)
			if fg != hwnd && root != hwnd {
				puDestroyWindow.Call(hwnd)
			}
		}
	case uiMsgDrag:
		puReleaseCapture.Call()
		puSendMessage.Call(hwnd, 0x00A1, 2, 0) // WM_NCLBUTTONDOWN, HTCAPTION
		return 0
	case wmClose:
		puDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, msg, wp, lp)
	return r
}

// uiMessage handles what the page asks the window to do.
func (u *uiHost) uiMessage(m string) {
	switch {
	case m == "ready":
		if atomic.CompareAndSwapInt32(&u.ready, 0, 1) {
			uiLog("the page is drawn")
		}
	case strings.HasPrefix(m, "err:"):
		uiLog("page: %s", m[4:])
	case m == "drag":
		pPostMessage.Call(u.hwnd, uiMsgDrag, 0, 0) // moved out of the browser's own callback
	case m == "min":
		pShowWindow.Call(u.hwnd, 6)
	case m == "close":
		pPostMessage.Call(u.hwnd, wmClose, 0, 0)
	case strings.HasPrefix(m, "h:") && u.panel:
		// the panel is as tall as its content and stays glued to its bottom edge
		n, err := strconv.Atoi(m[2:])
		if err != nil || n < 120 || n > 900 {
			return
		}
		var rc struct{ L, T, R, B int32 }
		puGetWindowRect.Call(u.hwnd, uintptr(unsafe.Pointer(&rc)))
		h := int32(float64(n)*u.scale + 0.5)
		pSetWindowPos.Call(u.hwnd, 0, uintptr(rc.L), uintptr(rc.B-h), uintptr(rc.R-rc.L), uintptr(h), 0x0004|0x0010)
	}
}

// runUI is the whole life of the window process. It returns uiExitFailed
// when the built-in browser component is missing, and the tray application
// then falls back to a browser window.
func runUI(view, link string) int {
	runtime.LockOSThread()
	u := &uiHost{panel: view == "panel", scale: 1}
	title := uiTitleMain
	if u.panel {
		title = uiTitlePanel
	}
	cls := u16(uiClass)
	if h, _, _ := pFindWindow.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16(title)))); h != 0 {
		if u.panel {
			pPostMessage.Call(h, wmClose, 0, 0)
		} else {
			pShowWindow.Call(h, 9)
			pSetForegroundWindow.Call(h)
		}
		return 0
	}
	if pSetDpiAwarenessCtx.Find() == nil {
		pSetDpiAwarenessCtx.Call(^uintptr(3))
	}
	if pGetDpiForSystem.Find() == nil {
		if d, _, _ := pGetDpiForSystem.Call(); d >= 96 {
			u.scale = float64(d) / 96
		}
	}
	inst, _, _ := pGetModuleHandle.Call(0)
	icon, _, _ := puLoadImage.Call(inst, 1, 1, 0, 0, 0x0040|0x8000)
	cursor, _, _ := puLoadCursor.Call(0, 32512)
	brush, _, _ := puCreateSolidBrush.Call(0x0013110f)
	wc := wndClassEx{WndProc: syscall.NewCallback(uiWndProc), Instance: inst, ClassName: cls, Icon: icon, IconSm: icon, Cursor: cursor, Background: brush}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, _ := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return uiExitFailed
	}

	l, t, r, b := uiWorkArea()
	px := func(v int) int32 { return int32(float64(v)*u.scale + 0.5) }
	var x, y, w, h int32
	style, ex := uintptr(0x00C00000|0x00040000|0x00020000|0x00080000|0x02000000), uintptr(0x00040000)
	if u.panel {
		w, h = px(340), px(430)
		x, y = r-w-px(12), b-h-px(12)
		style, ex = 0x80000000|0x00C00000|0x00040000|0x02000000, 0x00000080|0x00000008
	} else {
		w, h = px(1000), px(680)
		if w > r-l {
			w = r - l
		}
		if h > b-t {
			h = b - t
		}
		x, y = l+(r-l-w)/2, t+(b-t-h)/2
	}
	hwnd, _, _ := pCreateWindowEx.Call(ex, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16(title))), style,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, inst, 0)
	if hwnd == 0 {
		return uiExitFailed
	}
	u.hwnd = hwnd
	theUI = u
	if puDwmSetAttr.Find() == nil {
		dark, round, border := uint32(1), uint32(2), uint32(0x0036302b)
		puDwmSetAttr.Call(hwnd, 20, uintptr(unsafe.Pointer(&dark)), 4)
		puDwmSetAttr.Call(hwnd, 33, uintptr(unsafe.Pointer(&round)), 4)
		puDwmSetAttr.Call(hwnd, 34, uintptr(unsafe.Pointer(&border)), 4)
	}
	// make the frame change take effect
	pSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, 0x0001|0x0002|0x0004|0x0010|0x0020)

	uiLog("---- window %q, scale %.2f, %dx%d at %d,%d", view, u.scale, w, h, x, y)
	// The browser component is created in a window that is already on the
	// screen: created in a hidden one, it may stay blank.
	u.shownAt = time.Now()
	pShowWindow.Call(hwnd, 5)
	puUpdateWindow.Call(hwnd)
	pSetForegroundWindow.Call(hwnd)

	var embedded int32
	go func() {
		// no component, or a page that never drew itself: the tray
		// application opens a browser window instead
		time.Sleep(12 * time.Second)
		if atomic.LoadInt32(&embedded) == 0 || atomic.LoadInt32(&u.ready) == 0 {
			uiLog("giving up: embedded=%d ready=%d", atomic.LoadInt32(&embedded), atomic.LoadInt32(&u.ready))
			os.Exit(uiExitFailed)
		}
	}()
	web := edge.NewChromium()
	web.DataPath = filepath.Join(os.Getenv("LOCALAPPDATA"), appName, "webview")
	web.MessageCallback = u.uiMessage
	web.NavigationCompletedCallback = func(_ *edge.ICoreWebView2, _ *edge.ICoreWebView2NavigationCompletedEventArgs) {
		uiLog("navigation completed")
	}
	if !web.Embed(hwnd) {
		uiLog("the browser component could not be created")
		return uiExitFailed
	}
	atomic.StoreInt32(&embedded, 1)
	uiLog("the browser component is ready")
	u.web = web
	if s, err := web.GetSettings(); err == nil {
		s.PutAreDefaultContextMenusEnabled(false)
		s.PutAreDevToolsEnabled(false)
		s.PutIsStatusBarEnabled(false)
	}
	web.Show()
	web.Resize()
	web.Navigate(link + "&host=1")
	web.Focus()

	var m msgT
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	return 0
}

// closeUIWindows closes the window and the panel when the tray application exits.
func closeUIWindows() {
	cls := u16(uiClass)
	for _, t := range []string{uiTitleMain, uiTitlePanel} {
		if h, _, _ := pFindWindow.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16(t)))); h != 0 {
			pPostMessage.Call(h, wmClose, 0, 0)
		}
	}
}
