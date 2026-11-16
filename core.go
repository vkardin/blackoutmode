package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MatchService reports whether a service is covered by one of the patterns.
// A pattern is a lower-case part of the service name or of its display name.
func MatchService(name, display string, pats []string) bool {
	n, d := strings.ToLower(name), strings.ToLower(display)
	for _, p := range pats {
		if p != "" && (strings.Contains(n, p) || strings.Contains(d, p)) {
			return true
		}
	}
	return false
}

// VLCHwOff reports whether hardware decoding is switched off in a vlcrc file.
func VLCHwOff(vlcrc string) bool {
	for _, line := range strings.Split(vlcrc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "avcodec-hw=") {
			return strings.TrimPrefix(line, "avcodec-hw=") == "none"
		}
	}
	return false
}

// GPU power states as shown to the user.
const (
	gpuUnknown = ""
	gpuAsleep  = "asleep"
	gpuAwake   = "awake"
	gpuAbsent  = "absent"
)

// ParseGPUState reads the output of the video card query: "gpu=none", or
// "bytes=56,0,0,0,4,..." where the fifth byte is the device power state
// (1 working, 4 switched off).
func ParseGPUState(out string) string {
	for _, f := range strings.Fields(out) {
		if f == "gpu=none" {
			return gpuAbsent
		}
		if strings.HasPrefix(f, "bytes=") {
			b := strings.Split(strings.TrimPrefix(f, "bytes="), ",")
			if len(b) < 8 {
				return gpuUnknown
			}
			switch b[4] {
			case "1":
				return gpuAwake
			case "2", "3", "4":
				return gpuAsleep
			}
		}
	}
	return gpuUnknown
}

type smiProc struct {
	PID  uint32
	Name string // normalized
}

// ParseSmi extracts the process table from plain nvidia-smi output.
func ParseSmi(out string) []smiProc {
	var list []smiProc
	seen := map[uint32]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(strings.Trim(strings.TrimSpace(line), "|"))
		// GPU GI CI PID Type Name... Memory
		if len(f) < 7 {
			continue
		}
		if _, err := strconv.Atoi(f[0]); err != nil {
			continue
		}
		pid, err := strconv.ParseUint(f[3], 10, 32)
		if err != nil || pid == 0 || seen[uint32(pid)] {
			continue
		}
		if t := f[4]; t != "C" && t != "G" && t != "C+G" {
			continue
		}
		name := strings.Join(f[5:len(f)-1], " ")
		if i := strings.LastIndexAny(name, `\/`); i >= 0 {
			name = name[i+1:]
		}
		seen[uint32(pid)] = true
		list = append(list, smiProc{PID: uint32(pid), Name: NormProc(name)})
	}
	return list
}

// widgetInput is everything the on-screen widget shows.
type widgetInput struct {
	BatOK    bool
	Percent  int
	OnAC     bool
	Charging bool
	RateW    float64 // battery charge rate, W
	AvgW     float64 // measured draw on battery, 0 while unknown
	EtaW     float64 // the same over five minutes, steadier: for the time left
	LastW    float64 // draw remembered from the last time on battery
	RemainWh float64
	Blackout bool
	BankWh   float64 // estimated energy left in the power bank, W*h
}

// WidgetLines returns the large and the small line of the widget.
func WidgetLines(in widgetInput) (string, string) {
	if !in.BatOK {
		return "Батарея не найдена", ""
	}
	pct := fmt.Sprintf("%d%%", in.Percent)
	if !in.OnAC {
		if in.AvgW > 0.5 {
			eta := in.AvgW
			if in.EtaW > 0.5 {
				eta = in.EtaW
			}
			return pct + " · " + FmtDur(in.RemainWh/eta), fmt.Sprintf("%.1f Вт · от батареи", in.AvgW)
		}
		if in.LastW > 1 {
			return pct + " · ≈" + FmtDur(in.RemainWh/in.LastW), "от батареи · замеряю расход"
		}
		return pct, "от батареи · замеряю расход"
	}
	if in.Charging && in.RateW > 0.5 {
		return pct + " · зарядка", fmt.Sprintf("+%.0f Вт в батарею", in.RateW)
	}
	if in.Blackout && in.LastW > 1 {
		return pct + " · ≈" + FmtDur(in.RemainWh/in.LastW), "павербанк ≈" + FmtDur(in.BankWh/in.LastW)
	}
	return pct, "внешнее питание"
}

// RememberGame puts a game path at the front of the list of known games,
// without duplicates, and keeps the list short.
func RememberGame(list []string, path string, max int) []string {
	out := []string{path}
	for _, p := range list {
		if !strings.EqualFold(p, path) && len(out) < max {
			out = append(out, p)
		}
	}
	return out
}

// NewerVersion reports whether tag ("v2.1.0" or "2.1.0") is above cur.
func NewerVersion(tag, cur string) bool {
	parse := func(s string) [3]int {
		var v [3]int
		for i, p := range strings.SplitN(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".", 3) {
			n := 0
			for _, c := range p {
				if c < '0' || c > '9' {
					break
				}
				n = n*10 + int(c-'0')
			}
			v[i] = n
		}
		return v
	}
	a, b := parse(tag), parse(cur)
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// OldVersionFiles picks, among the file names of the program's folder, the
// copies and source archives of versions older than cur.
func OldVersionFiles(names []string, cur string) []string {
	var out []string
	for _, n := range names {
		rest, ok := strings.CutPrefix(n, "BlackoutMode-")
		if !ok {
			continue
		}
		ver, ok := strings.CutSuffix(rest, ".exe")
		if !ok {
			ver, ok = strings.CutSuffix(rest, "-src.zip")
		}
		if !ok || strings.Count(ver, ".") != 2 || strings.Trim(ver, "0123456789.") != "" {
			continue
		}
		if NewerVersion(cur, ver) {
			out = append(out, n)
		}
	}
	return out
}

// bankCalib is what is known about the power bank between two corrections.
type bankCalib struct {
	CapWh   float64 // usable capacity from the settings
	MarkPct float64 // charge the user gave last time, -1 if never
	ModelWh float64 // energy counted since then without the correction factor
	Factor  float64 // real energy taken from the bank per counted W*h, 0 while unknown
}

// BankFix applies the charge the user read off the power bank. It returns the
// energy left in the bank and the updated calibration. The factor is learned
// only when the charge has fallen by at least 5% and at least 5 W*h were
// counted in between, so that a coarse display does not throw it off.
func BankFix(c bankCalib, pct float64) (float64, bankCalib) {
	pct = math.Max(0, math.Min(100, pct))
	left := c.CapWh * pct / 100
	switch {
	case c.MarkPct < 0 || pct > c.MarkPct:
		// first correction or the bank was charged in between: start over
		c.MarkPct, c.ModelWh = pct, 0
	case c.MarkPct-pct >= 5 && c.ModelWh >= 5:
		ratio := math.Max(0.6, math.Min(2.5, c.CapWh*(c.MarkPct-pct)/100/c.ModelWh))
		if c.Factor > 0 {
			ratio = (c.Factor + ratio) / 2
		}
		c.Factor, c.MarkPct, c.ModelWh = ratio, pct, 0
	}
	// a small drop keeps the old mark, so the next correction sees a longer span
	return left, c
}

// BankFactor is the correction factor to apply, 1 while it is not learned yet.
func BankFactor(f float64) float64 {
	if f < 0.6 || f > 2.5 {
		return 1
	}
	return f
}

// DrawKey names a kind of work the draw is remembered for: the mode as it
// goes to power.csv, and whether the separate video card is awake.
func DrawKey(mode, gpu string) string {
	if gpu == gpuAwake {
		return mode + "|awake"
	}
	return mode
}

// LearnDraw folds one more minute of measured draw into the remembered
// value. Recent minutes weigh more, a single spike changes little.
func LearnDraw(old, sample float64) float64 {
	if sample < 1 || sample > 300 {
		return old
	}
	if old < 1 {
		return sample
	}
	return old*0.9 + sample*0.1
}

// OutageWorthLogging tells a real outage from the mode being switched on
// for a moment or left on while the laptop sat on the grid.
func OutageWorthLogging(mins, pctA, pctB int, bankWh float64) bool {
	return mins >= 2 && (pctB < pctA || bankWh >= 1)
}

// GPUCheckDue spaces out the checks of the video card. Each check starts
// PowerShell, which costs energy by itself, so a card that keeps giving the
// same calm answer is asked less and less often: every 2, 4, 8 and then
// 10 minutes. ticks counts 5-second steps since blackout mode came on,
// calm is the number of calm answers in a row.
func GPUCheckDue(ticks, calm int) bool {
	if ticks == 6 {
		return true // the first look, half a minute in
	}
	every := 24
	for i := 0; i < calm && every < 120; i++ {
		every *= 2
	}
	if every > 120 {
		every = 120
	}
	return ticks > 6 && ticks%every == 0
}

// TrimLog keeps the newest part of a log that has grown past max bytes,
// cutting at a line break.
func TrimLog(data []byte, max, keep int) []byte {
	if len(data) <= max || keep >= len(data) {
		return data
	}
	tail := data[len(data)-keep:]
	for i, c := range tail {
		if c == '\n' {
			return tail[i+1:]
		}
	}
	return tail
}

// widgetBar is one thin bar of the on-screen widget: how full, and in what colour (R, G, B).
type widgetBar struct {
	Frac  float64
	Color [3]byte
}

// widgetBars places the bars inside the widget picture, in pixels.
type widgetBars struct {
	List       []widgetBar
	X, Y, W, H int
	Gap        int
}

var (
	colorOK   = [3]byte{0x63, 0xd3, 0x91}
	colorBank = [3]byte{0xf2, 0xb8, 0x4b}
	colorLow  = [3]byte{0xff, 0x8f, 0x7a}
)

// BatteryColor is green, amber from 30% down and red from 10% down.
func BatteryColor(pct int) [3]byte {
	switch {
	case pct <= 10:
		return colorLow
	case pct <= 30:
		return colorBank
	}
	return colorOK
}

// pill is how much of the pixel at (cx, cy) a rounded bar covers, 0 to 1.
func pill(cx, cy, x0, y0, x1, y1 float64) float64 {
	r := (y1 - y0) / 2
	if x1-x0 < 2*r {
		r = (x1 - x0) / 2
	}
	if r <= 0 {
		return 0
	}
	dx := math.Max(math.Max(x0+r-cx, cx-(x1-r)), 0)
	dy := math.Max(math.Max(y0+r-cy, cy-(y1-r)), 0)
	return math.Min(math.Max(r-math.Hypot(dx, dy)+0.5, 0), 1)
}

// ComposeWidget turns grey text coverage (BGRA, text drawn white on black)
// into a rounded dark panel with premultiplied alpha, in place, and draws
// the bars into it.
func ComposeWidget(px []byte, w, h int, radius float64, bars widgetBars) {
	const bgA, trackA = 0.80, 0.16
	bg := [3]float64{0x1c, 0x18, 0x15} // B, G, R
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := (y*w + x) * 4
			c := float64(px[o]) / 255
			cx, cy := float64(x)+0.5, float64(y)+0.5
			dx := math.Max(math.Max(radius-cx, cx-(float64(w)-radius)), 0)
			dy := math.Max(math.Max(radius-cy, cy-(float64(h)-radius)), 0)
			m := math.Min(math.Max(radius-math.Hypot(dx, dy)+0.5, 0), 1)
			col := [3]float64{bg[0]*bgA*(1-c) + 255*c, bg[1]*bgA*(1-c) + 255*c, bg[2]*bgA*(1-c) + 255*c}
			alpha := bgA + (1-bgA)*c
			for i, b := range bars.List {
				y0 := float64(bars.Y + i*(bars.H+bars.Gap))
				y1 := y0 + float64(bars.H)
				if cy < y0-1 || cy > y1+1 {
					continue
				}
				x0, x1 := float64(bars.X), float64(bars.X+bars.W)
				if t := pill(cx, cy, x0, y0, x1, y1) * trackA; t > 0 {
					for k := range col {
						col[k] = col[k]*(1-t) + 255*t
					}
					alpha += (1 - alpha) * t
				}
				frac := math.Max(0, math.Min(1, b.Frac))
				if f := pill(cx, cy, x0, y0, x0+(x1-x0)*frac, y1); f > 0 && frac > 0 {
					rgb := [3]float64{float64(b.Color[2]), float64(b.Color[1]), float64(b.Color[0])} // to B, G, R
					for k := range col {
						col[k] = col[k]*(1-f) + rgb[k]*f
					}
					alpha += (1 - alpha) * f
				}
			}
			for i := 0; i < 3; i++ {
				px[o+i] = byte(col[i]*m + 0.5)
			}
			px[o+3] = byte(alpha*m*255 + 0.5)
		}
	}
}

// defaultUpdateRepo is where new versions are looked for unless the
// settings name another repository.
const defaultUpdateRepo = "vkardin/blackoutmode"

// releaseInfo is what the update needs from a GitHub release.
type releaseInfo struct {
	Tag    string
	URL    string // the program file of the release
	SHA256 string // its checksum in hex, empty when GitHub did not give one
}

// ParseRelease reads the answer of GitHub about the latest release and
// picks the program file among its attachments. Only a file that lies in
// the releases of that very repository is accepted.
func ParseRelease(body []byte, repo string) releaseInfo {
	var r struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if json.Unmarshal(body, &r) != nil {
		return releaseInfo{}
	}
	info := releaseInfo{Tag: r.Tag}
	prefix := "https://github.com/" + repo + "/releases/download/"
	for _, a := range r.Assets {
		n := strings.ToLower(a.Name)
		if strings.HasPrefix(n, "blackoutmode") && strings.HasSuffix(n, ".exe") && strings.HasPrefix(a.URL, prefix) {
			info.URL = a.URL
			if d := strings.TrimPrefix(a.Digest, "sha256:"); len(d) == 64 && d != a.Digest {
				info.SHA256 = strings.ToLower(d)
			}
			break
		}
	}
	return info
}

// CheckDownload makes sure a downloaded update is a Windows program of a
// sane size with the expected checksum.
func CheckDownload(data []byte, sha string) error {
	if len(data) < 1<<20 || len(data) > 64<<20 || data[0] != 'M' || data[1] != 'Z' {
		return errors.New("not a program file")
	}
	if sha != "" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != sha {
			return errors.New("checksum does not match")
		}
	}
	return nil
}

// Measuring the saving: the same few minutes of work without blackout mode
// and with it, on battery.
const (
	measureSettle = 12 // ticks to let the draw settle after a switch: one minute
	measureTake   = 36 // ticks measured in each half: three minutes
)

type measureRun struct {
	Phase    int     // 1 settle without saving, 2 measure it, 3 settle with saving, 4 measure it
	Ticks    int     // ticks spent in this phase
	Sum      float64 // draw added up in this phase
	N        int
	BaseW    float64 // the result of phase 2
	WasOn    bool    // blackout mode was on before the measurement
	Finished bool
	SaveW    float64 // the result of phase 4
}

// Step feeds one tick into the measurement. w is the draw on battery, 0
// when this tick gave no reading. It reports whether blackout mode has to
// be on for the next tick.
func (m *measureRun) Step(w float64) (wantOn bool) {
	m.Ticks++
	measuring := m.Phase == 2 || m.Phase == 4
	if measuring && w > 0.5 {
		m.Sum += w
		m.N++
	}
	limit := measureSettle
	if measuring {
		limit = measureTake
	}
	if m.Ticks >= limit {
		avg := 0.0
		if m.N > 0 {
			avg = m.Sum / float64(m.N)
		}
		switch m.Phase {
		case 2:
			m.BaseW = avg
		case 4:
			m.SaveW, m.Finished = avg, true
		}
		m.Phase, m.Ticks, m.Sum, m.N = m.Phase+1, 0, 0, 0
	}
	return m.Phase >= 3
}

// SecondsLeft is how long the measurement still takes, with 5-second ticks.
func (m *measureRun) SecondsLeft() int {
	left := -m.Ticks
	for p := m.Phase; p <= 4; p++ {
		if p == 2 || p == 4 {
			left += measureTake
		} else {
			left += measureSettle
		}
	}
	if left < 0 {
		left = 0
	}
	return left * 5
}
