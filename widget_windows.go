//go:build windows

package main

import (
	"fmt"
	"math"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	pUpdateLayered   = user32.NewProc("UpdateLayeredWindow")
	pShowWindow      = user32.NewProc("ShowWindow")
	pSetWindowPos    = user32.NewProc("SetWindowPos")
	pSysParamsInfo   = user32.NewProc("SystemParametersInfoW")
	pGetDpiForSystem = user32.NewProc("GetDpiForSystem")
	pCreateCompatDC  = gdi32.NewProc("CreateCompatibleDC")
	pDeleteDC        = gdi32.NewProc("DeleteDC")
	pSelectObject    = gdi32.NewProc("SelectObject")
	pCreateFont      = gdi32.NewProc("CreateFontW")
	pSetBkMode       = gdi32.NewProc("SetBkMode")
	pSetTextColor    = gdi32.NewProc("SetTextColor")
	pTextOut         = gdi32.NewProc("TextOutW")
	pTextExtent      = gdi32.NewProc("GetTextExtentPoint32W")
	pGdiFlush        = gdi32.NewProc("GdiFlush")
)

const widgetClass = appName + "Widget"

type rect struct{ Left, Top, Right, Bottom int32 }

func widgetProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	r, _, _ := pDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
}

// createWidget makes a small always-on-top window that lets clicks through.
func (a *app) createWidget(inst uintptr) {
	cls := u16(widgetClass)
	wc := wndClassEx{WndProc: syscall.NewCallback(widgetProc), Instance: inst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		a.log("widget: RegisterClassEx failed: %v", err)
		return
	}
	// layered, click-through, topmost, no taskbar button, never takes focus
	const ex = 0x00080000 | 0x00000020 | 0x00000008 | 0x00000080 | 0x08000000
	h, _, err := pCreateWindowEx.Call(ex, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16(appTitle))),
		0x80000000, 0, 0, 0, 0, 0, 0, inst, 0)
	if h == 0 {
		a.log("widget: CreateWindowEx failed: %v", err)
	}
	a.widget = h
}

func (a *app) widgetWanted() bool {
	switch a.cfg.Widget {
	case "on":
		return true
	case "auto":
		return a.st.Mode == modeBlackout
	}
	return false
}

func (a *app) widgetInput() widgetInput {
	in := widgetInput{BatOK: a.batOK, Percent: a.percent(), OnAC: a.onAC(), Blackout: a.st.Mode == modeBlackout,
		LastW: a.lastDraw(), BankWh: a.bankLeft() / BankFactor(a.st.BankFactor)}
	if a.batOK {
		in.Charging = a.bat.Charging != 0
		in.RateW = float64(a.bat.Rate) / 1000
		in.RemainWh = float64(a.bat.RemainingCapacity) / 1000
	}
	if avg, ok := a.avgDraw(); ok {
		in.AvgW, in.EtaW = avg, a.etaDraw()
	}
	return in
}

// widgetBars are the thin bars under the text: the battery, and the power
// bank while it matters.
func (a *app) widgetBars() []widgetBar {
	if !a.batOK {
		return nil
	}
	pct := a.percent()
	bars := []widgetBar{{Frac: float64(pct) / 100, Color: BatteryColor(pct)}}
	if full := float64(a.cfg.PowerbankWh); full > 0 && (a.st.Mode == modeBlackout || a.st.BankKnown) {
		bars = append(bars, widgetBar{Frac: math.Min(1, a.bankLeft()/full), Color: colorBank})
	}
	return bars
}

// updateWidget shows, hides or redraws the widget as needed.
func (a *app) updateWidget() {
	if a.widget == 0 {
		return
	}
	if a.closing || !a.widgetWanted() {
		if a.widgetKey != "" {
			pShowWindow.Call(a.widget, 0)
			a.widgetKey = ""
		}
		return
	}
	l1, l2 := WidgetLines(a.widgetInput())
	if sl := ScheduleLine(a.schedStatus()); sl != "" {
		l2 += "\n" + sl
	}
	bars := a.widgetBars()
	key := l1 + "\n" + l2 + "\n" + a.cfg.WidgetCorner + string(rune('0'+a.cfg.WidgetOpacity/10))
	for _, b := range bars {
		key += fmt.Sprintf("|%.2f%v", b.Frac, b.Color)
	}
	if key != a.widgetKey {
		if a.drawWidget(l1, l2, bars) {
			if a.widgetKey == "" {
				pShowWindow.Call(a.widget, 4) // show without taking focus
			}
			a.widgetKey = key
		}
	}
	// stay above full-screen players
	pSetWindowPos.Call(a.widget, ^uintptr(0), 0, 0, 0, 0, 0x0001|0x0002|0x0010)
}

func makeFont(px int, weight int) uintptr {
	h, _, _ := pCreateFont.Call(uintptr(uint32(int32(-px))), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 4, 0,
		uintptr(unsafe.Pointer(u16("Segoe UI"))))
	return h
}

func textSize(dc uintptr, s string) (int, int) {
	u := utf16.Encode([]rune(s))
	if len(u) == 0 {
		return 0, 0
	}
	var sz point
	pTextExtent.Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&sz)))
	return int(sz.X), int(sz.Y)
}

func textOut(dc uintptr, x, y int, s string) {
	u := utf16.Encode([]rune(s))
	if len(u) == 0 {
		return
	}
	pTextOut.Call(dc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)))
}

func (a *app) drawWidget(l1, l2 string, bars []widgetBar) bool {
	dpi := uintptr(96)
	if pGetDpiForSystem.Find() == nil {
		if d, _, _ := pGetDpiForSystem.Call(); d >= 96 && d <= 480 {
			dpi = d
		}
	}
	sc := func(v float64) int { return int(v*float64(dpi)/96 + 0.5) }

	dc, _, _ := pCreateCompatDC.Call(0)
	if dc == 0 {
		return false
	}
	defer pDeleteDC.Call(dc)
	f1, f2 := makeFont(sc(19), 600), makeFont(sc(12.5), 400)
	defer pDeleteObject.Call(f1)
	defer pDeleteObject.Call(f2)

	old, _, _ := pSelectObject.Call(dc, f1)
	w1, h1 := textSize(dc, l1)
	pSelectObject.Call(dc, f2)
	small := strings.Split(l2, "\n")
	padX, padY := sc(13), sc(8)
	w, h, h2 := w1, h1, 0
	for _, line := range small {
		lw, lh := textSize(dc, line)
		if lw > w {
			w = lw
		}
		if lh > h2 {
			h2 = lh
		}
	}
	if l2 != "" {
		h += h2 * len(small)
	}
	if w < sc(150) && len(bars) > 0 {
		w = sc(150) // room for the bars to say something
	}
	barH, barGap, barTop := sc(3), sc(4), sc(8)
	textH := h
	w += 2 * padX
	h += 2 * padY
	if len(bars) > 0 {
		h += barTop + len(bars)*barH + (len(bars)-1)*barGap
	}
	if w <= 0 || h <= 0 || w > 4000 || h > 1000 {
		pSelectObject.Call(dc, old)
		return false
	}

	bi := bitmapInfoHeader{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	dib, _, _ := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if dib == 0 || bits == nil {
		pSelectObject.Call(dc, old)
		return false
	}
	defer pDeleteObject.Call(dib)
	oldBmp, _, _ := pSelectObject.Call(dc, dib)

	pSetBkMode.Call(dc, 1)
	pSetTextColor.Call(dc, 0x00FFFFFF)
	pSelectObject.Call(dc, f1)
	textOut(dc, padX, padY, l1)
	pSetTextColor.Call(dc, 0x00B4B4B4)
	pSelectObject.Call(dc, f2)
	for i, line := range small {
		textOut(dc, padX, padY+h1+i*h2, line)
	}
	pGdiFlush.Call()
	ComposeWidget(unsafe.Slice((*byte)(bits), w*h*4), w, h, float64(sc(10)),
		widgetBars{List: bars, X: padX, Y: padY + textH + barTop, W: w - 2*padX, H: barH, Gap: barGap})

	var wa rect
	pSysParamsInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&wa)), 0)
	if wa.Right <= wa.Left || wa.Bottom <= wa.Top {
		cx, _, _ := pGetSystemMetrics.Call(0)
		cy, _, _ := pGetSystemMetrics.Call(1)
		wa = rect{0, 0, int32(cx), int32(cy)}
	}
	mg := int32(sc(16))
	pos := point{wa.Left + mg, wa.Top + mg}
	if a.cfg.WidgetCorner == "tr" || a.cfg.WidgetCorner == "br" {
		pos.X = wa.Right - mg - int32(w)
	}
	if a.cfg.WidgetCorner == "bl" || a.cfg.WidgetCorner == "br" {
		pos.Y = wa.Bottom - mg - int32(h)
	}
	size := point{int32(w), int32(h)}
	var src point
	blend := [4]byte{0, 0, byte(clamp(a.cfg.WidgetOpacity, 30, 100) * 255 / 100), 1}
	r, _, err := pUpdateLayered.Call(a.widget, 0, uintptr(unsafe.Pointer(&pos)), uintptr(unsafe.Pointer(&size)),
		dc, uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), 2)
	pSelectObject.Call(dc, oldBmp)
	pSelectObject.Call(dc, old)
	if r == 0 && !a.widgetErr {
		a.widgetErr = true
		a.log("widget: UpdateLayeredWindow failed: %v", err)
	}
	return r != 0
}
