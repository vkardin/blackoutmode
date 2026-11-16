package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConfigRoundTrip(t *testing.T) {
	c := DefaultConfig()
	c.Level = "soft"
	c.Kill = []string{"adb", "musehub"}
	c.Brightness = 20
	c.AutoEnable = false
	got := ParseConfig("\xef\xbb\xbf" + c.Serialize())
	if got.Level != "soft" || got.Brightness != 20 || got.AutoEnable || len(got.Kill) != 2 || got.Kill[1] != "musehub" || got.PowerbankWh != 0 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	d := ParseConfig("kill = \nbrightness = 400\nlevel = weird\nfoo\n")
	if len(d.Kill) != 0 || d.Brightness != 100 || d.Level != "max" {
		t.Fatalf("bad parse: %+v", d)
	}
	if NormProc(" Adb.EXE ") != "adb" {
		t.Fatal("NormProc")
	}
	games := map[string]bool{
		`C:\Program Files (x86)\Steam\steamapps\common\Deadlock\game\bin\win64\deadlock.exe`: true,
		`C:\GOG Games\Cyberpunk 2077\bin\x64\Cyberpunk2077.exe`:                              true,
		`C:\XboxGames\Forza Horizon 5\Content\ForzaHorizon5.exe`:                             true,
		`D:\Games\Some Game\game.exe`:                                                        true,
		`C:\Program Files (x86)\Steam\steam.exe`:                                             false,
		`C:\Program Files\Epic Games\Launcher\Portal\Binaries\Win64\EpicGamesLauncher.exe`:   false,
		`C:\Program Files (x86)\Steam\steamapps\common\wallpaper_engine\wallpaper64.exe`:     false,
		`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`:                 false,
		`C:\Users\K\Documents\My Games\tool.exe`:                                             false,
		`C:\Riot Games\Riot Client\RiotClientServices.exe`:                                   false,
		`C:\Riot Games\VALORANT\live\ShooterGame\Binaries\Win64\VALORANT-Win64-Shipping.exe`: true,
	}
	for p, want := range games {
		if IsGamePath(p, c) != want {
			t.Errorf("IsGamePath(%s) = %v, want %v", p, !want, want)
		}
	}
	c.Games = []string{"javaw"}
	if !IsGamePath(`C:\Users\K\AppData\Roaming\.minecraft\runtime\bin\javaw.exe`, c) {
		t.Error("games list is ignored")
	}
	if !IsPlayer("vlc", c) || IsPlayer("brave", c) {
		t.Error("IsPlayer")
	}
	g2 := ParseConfig(c.Serialize())
	if len(g2.Games) != 1 || !g2.GameMode || len(g2.Throttle) != 6 || len(g2.NotGames) != 2 || !g2.FilmMode || len(g2.Players) != 6 {
		t.Errorf("game config round trip: %+v", g2)
	}
	if FmtDur(5.5) != "5 ч 30 мин" || FmtDur(0.4) != "24 мин" {
		t.Fatal("FmtDur: " + FmtDur(5.5) + " / " + FmtDur(0.4))
	}
}

func toPNG(size int, kind int) []byte {
	px := RenderIcon(size, kind)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i := 0; i < size*size; i++ {
		a := px[i*4+3]
		un := func(v byte) byte {
			if a == 0 {
				return 0
			}
			return byte(int(v) * 255 / int(a))
		}
		img.SetNRGBA(i%size, i/size, color.NRGBA{un(px[i*4+2]), un(px[i*4+1]), un(px[i*4]), a})
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// TestGenAssets writes previews of the tray badges when GEN_ASSETS is set.
// The program icon itself, res/app.ico, is drawn from res/logo.svg.
func TestGenAssets(t *testing.T) {
	dir := os.Getenv("GEN_ASSETS")
	if dir == "" {
		t.Skip()
	}
	for kind, name := range []string{"idle", "blackout", "game", "film"} {
		for _, size := range []int{16, 20, 24, 32, 256} {
			os.WriteFile(fmt.Sprintf("%s/tray-%s-%d.png", dir, name, size), toPNG(size, kind), 0o644)
		}
	}
}

func TestNewSettings(t *testing.T) {
	c := DefaultConfig()
	c.Widget, c.WidgetCorner, c.WidgetOpacity = "on", "bl", 50
	c.BTOff, c.ECores, c.FilmWifiOff, c.PauseVPN = false, false, false, false
	c.Services = []string{"wsearch", "sur qc"}
	g := ParseConfig(c.Serialize())
	if g.Widget != "on" || g.WidgetCorner != "bl" || g.WidgetOpacity != 50 || g.BTOff || g.ECores ||
		g.FilmWifiOff || g.PauseVPN || !g.PauseServices || len(g.Services) != 2 || g.Services[1] != "sur qc" || len(g.VPN) != 3 {
		t.Fatalf("round trip: %+v", g)
	}
	// a config written by 1.2.0 gets the defaults for everything new
	old := ParseConfig("level = soft\nwidget = sideways\nwidget_opacity = 5\n")
	if old.Level != "soft" || old.Widget != "auto" || old.WidgetOpacity != 30 || !old.BTOff || len(old.Services) == 0 {
		t.Fatalf("old config: %+v", old)
	}
	up := ParseConfig("services = " + services150 + "\n")
	up.Upgrade()
	if len(up.Services) != len(DefaultConfig().Services) || inList150(up.Services, "diagtrack") {
		t.Fatalf("upgrade: %+v", up.Services)
	}
	mine := ParseConfig("services = wsearch\n")
	mine.Upgrade()
	if len(mine.Services) != 1 {
		t.Fatalf("upgrade touched a user list: %+v", mine.Services)
	}
	d := DefaultConfig()
	pats := append(append([]string{}, d.Services...), d.VPN...)
	for name, want := range map[string]bool{
		"DSAService": true, "SystemUsageReportSvc_QUEENCREEK": true, "IntelTelemetryAgent": true, "IntelCollectorService": true,
		"PresentMonSharedService": true, "DiagTrack": false, "WinDefend": false, "IntelAudioService": false, "IntelGraphicsSoftwareService": false, "ESRV_SVC_QUEENCREEK": true,
		"WSearch": true, "Tailscale": true, "CloudflareWARP": true, "Surfshark Service": true,
		"Dhcp": false, "WlanSvc": false, "Audiosrv": false, "LenovoVantageService": false, "NVDisplay.ContainerLocalSystem": false,
	} {
		if MatchService(name, "", pats) != want {
			t.Errorf("MatchService(%s) != %v", name, want)
		}
	}
	if !MatchService("x", "Windows Search (WSearch)", pats) || MatchService("x", "y", nil) {
		t.Error("MatchService by display name")
	}
}

func TestParsers(t *testing.T) {
	if !VLCHwOff("# hw\r\navcodec-hw=none\r\n") || VLCHwOff("#avcodec-hw=any\n") || VLCHwOff("avcodec-hw=d3d11va\n") {
		t.Error("VLCHwOff")
	}
	for in, want := range map[string]string{
		"gpu=none":                       gpuAbsent,
		"bytes=56,0,0,0,1,0,0,0,0,0,0,0": gpuAwake,
		"bytes=56,0,0,0,4,0,0,0,0,0,0,0": gpuAsleep,
		"data=_something_":               gpuUnknown,
		"":                               gpuUnknown,
		"bytes=1,2":                      gpuUnknown,
	} {
		if got := ParseGPUState(in); got != want {
			t.Errorf("ParseGPUState(%q) = %q, want %q", in, got, want)
		}
	}
	smi := `Tue Nov 10 19:00:00 2026
+-----------------------------------------------------------------------------------------+
| NVIDIA-SMI 566.03                 Driver Version: 566.03         CUDA Version: 12.7     |
|=========================================+========================+======================|
|   0  NVIDIA GeForce RTX 3050 ...  WDDM  |   00000000:01:00.0 Off |                  N/A |
| N/A   45C    P8              3W /   60W |       0MiB /   4096MiB |      0%      Default |
+-----------------------------------------------------------------------------------------+
| Processes:                                                                              |
|  GPU   GI   CI        PID   Type   Process name                              GPU Memory |
|        ID   ID                                                               Usage      |
|=========================================================================================|
|    0   N/A  N/A      4321    C+G   ...Brave-Browser\Application\brave.exe      N/A      |
|    0   N/A  N/A      5555    C+G   C:\Program Files\Some App\Some App.exe      N/A      |
|    0   N/A  N/A      4321    C+G   ...Brave-Browser\Application\brave.exe      N/A      |
+-----------------------------------------------------------------------------------------+`
	got := ParseSmi(smi)
	if len(got) != 2 || got[0].PID != 4321 || got[0].Name != "brave" || got[1].Name != "some app" {
		t.Errorf("ParseSmi: %+v", got)
	}
	if len(ParseSmi("|  No running processes found  |")) != 0 {
		t.Error("ParseSmi empty")
	}
}

func TestWidgetLines(t *testing.T) {
	cases := []struct {
		in     widgetInput
		l1, l2 string
	}{
		{widgetInput{}, "Батарея не найдена", ""},
		{widgetInput{BatOK: true, Percent: 78, AvgW: 10, RemainWh: 50}, "78% · 5 ч 00 мин", "10.0 Вт · от батареи"},
		{widgetInput{BatOK: true, Percent: 78, AvgW: 25, EtaW: 10, RemainWh: 50}, "78% · 5 ч 00 мин", "25.0 Вт · от батареи"},
		{widgetInput{BatOK: true, Percent: 78, LastW: 20, RemainWh: 50}, "78% · ≈2 ч 30 мин", "от батареи · замеряю расход"},
		{widgetInput{BatOK: true, Percent: 78}, "78%", "от батареи · замеряю расход"},
		{widgetInput{BatOK: true, Percent: 60, OnAC: true, Charging: true, RateW: 45}, "60% · зарядка", "+45 Вт в батарею"},
		{widgetInput{BatOK: true, Percent: 80, OnAC: true}, "80%", "внешнее питание"},
		{widgetInput{BatOK: true, Percent: 80, OnAC: true, Blackout: true, LastW: 10, RemainWh: 52, BankWh: 100},
			"80% · ≈5 ч 12 мин", "павербанк ≈10 ч 00 мин"},
	}
	for _, c := range cases {
		if l1, l2 := WidgetLines(c.in); l1 != c.l1 || l2 != c.l2 {
			t.Errorf("WidgetLines(%+v) = %q / %q", c.in, l1, l2)
		}
	}
}

func inList150(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

func TestSchedule(t *testing.T) {
	data, err := os.ReadFile("testdata/region.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseRegion(data)
	if err != nil || r.ID != "dnipropetrovsk" {
		t.Fatalf("ParseRegion: %v %+v", err, r.ID)
	}
	at := func(hm string) time.Time {
		ts, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-10 "+hm, kyiv())
		return ts
	}
	// group 2.1 on 2026-11-10: 04:30-12:00 and 15:00-19:00 without power
	st := ScheduleStatus(r, "2.1", at("13:00"))
	if !st.Known || st.State != powerOn || st.ChangeAt.Format("15:04") != "15:00" || st.NextOff.Format("15:04") != "15:00" {
		t.Fatalf("13:00: %+v", st)
	}
	if !st.Usable || ScheduleLine(st) != "отключение ≈15:00" {
		t.Fatalf("13:00 line %q usable=%v", ScheduleLine(st), st.Usable)
	}
	st = ScheduleStatus(r, "2.1", at("16:00"))
	if st.State != powerOff || st.ChangeAt.Format("15:04") != "19:00" || ScheduleLine(st) != "по графику света нет до ≈19:00" {
		t.Fatalf("16:00: %+v %q", st, ScheduleLine(st))
	}
	st = ScheduleStatus(r, "2.1", at("21:00"))
	if st.State != powerOn || !st.ChangeAt.IsZero() || ScheduleLine(st) != "отключений в графике нет" {
		t.Fatalf("21:00: %+v %q", st, ScheduleLine(st))
	}
	if st := ScheduleStatus(r, "9.9", at("13:00")); st.Known || ScheduleLine(st) != "" {
		t.Fatalf("unknown group: %+v", st)
	}
	late, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-11 13:00", kyiv())
	if st := ScheduleStatus(r, "2.1", late); st.Known {
		t.Fatalf("a day without data must be unknown: %+v", st)
	}
	old := ScheduleStatus(r, "2.1", at("23:30"))
	if old.Usable || !strings.Contains(ScheduleLine(old), "устарел") {
		t.Fatalf("data fetched at 20:11 must be stale at 23:30: %+v", old)
	}
	for hm, want := range map[string]bool{"13:00": true, "14:45": true, "16:30": false, "18:40": true, "19:20": true, "15:20": true, "15:40": false} {
		if MainsExpected(r, "2.1", at(hm)) != want {
			t.Errorf("MainsExpected(%s) != %v", hm, want)
		}
	}
	for hm, want := range map[string]bool{"13:00": true, "14:45": true, "15:20": false, "16:30": false, "18:40": true, "19:20": true} {
		if MainsLikely(r, "2.1", at(hm)) != want {
			t.Errorf("MainsLikely(%s) != %v", hm, want)
		}
	}
	reg, grp := kolyPlace([]byte(`{"places":[{"id":"a","regionId":"kyiv-city","group":"3.1"},{"id":"b","regionId":"dnipropetrovsk","group":"2.1"}],"mainPlaceId":"b"}`))
	if reg != "dnipropetrovsk" || grp != "2.1" {
		t.Fatalf("kolyPlace: %s %s", reg, grp)
	}
	if reg, _ := kolyPlace([]byte(`{"places":[]}`)); reg != "" {
		t.Fatal("kolyPlace empty")
	}
}

func TestLiteGame(t *testing.T) {
	c := DefaultConfig()
	if !c.LiteGame || !c.LiteGPU || !c.LiteTurbo {
		t.Fatal("light game defaults")
	}
	c.LiteGPU, c.LiteTurbo = false, false
	g := ParseConfig(c.Serialize())
	if !g.LiteGame || g.LiteGPU || g.LiteTurbo {
		t.Errorf("light game round trip: %+v", g)
	}
	l := RememberGame(nil, `C:\g\a.exe`, 3)
	l = RememberGame(l, `C:\g\b.exe`, 3)
	l = RememberGame(l, `c:\G\A.exe`, 3)
	if len(l) != 2 || l[0] != `c:\G\A.exe` || l[1] != `C:\g\b.exe` {
		t.Errorf("RememberGame: %v", l)
	}
	for _, p := range []string{"c", "d", "e"} {
		l = RememberGame(l, p, 3)
	}
	if len(l) != 3 || l[0] != "e" {
		t.Errorf("RememberGame cap: %v", l)
	}
}

type stubBackend struct{}

// stubLang lets the preview be opened in another language: BM_LANG=uk.
func stubLang() string {
	if l := os.Getenv("BM_LANG"); l != "" {
		return l
	}
	return "ru"
}

func (stubBackend) Section(name string) (any, bool) {
	c := DefaultConfig()
	c.Lang = stubLang()
	c.PowerbankWh = 150
	switch name {
	case "state":
		return map[string]any{"version": "2.0.0", "elevated": true, "mode": "blackout", "kind": "", "exe": "", "manual": false, "onAC": false,
			"battery": true, "pct": 78, "watts": 11.2, "measured": true, "hours": 15.1, "bankWh": 120, "bankCap": 150, "bankKnown": true, "batWh": 51.2, "rateW": 0, "bankFactor": 1.18, "measure": map[string]any{"running": os.Getenv("BM_MEAS") == "run", "left": 290, "phase": 2, "baseW": 31.4, "saveW": 14.2, "at": "2026-11-16T09:10:00+02:00"},
			"updateFile": true, "updating": false, "updateErr": "",
			"gpu": "asleep", "gpuUsers": []string{}, "since": "16:52",
			"sched":  map[string]any{"known": true, "usable": true, "state": "off", "changeAt": "19:00", "nextOff": "", "region": "dnipropetrovsk", "group": "2.1"},
			"caps":   map[string]any{"ready": true, "vendor": "LENOVO", "model": "82SA", "fan": true, "limit": true, "nvidia": true, "battery": true, "hz": true},
			"update": os.Getenv("BM_UPD"), "lang": stubLang(), "setupDone": os.Getenv("BM_SETUP") == "", "autostart": true, "limitOn": true}, true
	case "config":
		return ConfigMap(c), true
	case "schedule":
		data, _ := os.ReadFile("testdata/region.json")
		r, _ := ParseRegion(data)
		days := ScheduleDays(r, "2.1")
		if len(days) > 0 {
			days[0].Date = time.Now().Format("2006-01-02")
		}
		return map[string]any{"days": days, "fetchedAt": r.FetchedAt, "status": r.Status, "emergency": false, "groups": r.GroupNames()}, true
	case "stats":
		p, _ := os.ReadFile(os.Getenv("BM_DATA") + "/power.csv")
		o, _ := os.ReadFile(os.Getenv("BM_DATA") + "/outages.csv")
		return map[string]any{"stats": BuildStats(string(p), string(o), time.Now()), "hogs": []map[string]any{{"Name": "brave", "Exe": "brave.exe", "CPU": 3.4}}}, true
	case "cleanup":
		return map[string]any{"busy": false, "items": []cleanItem{{ID: "temp", Bytes: 900 << 20}, {ID: "browsers", Bytes: 1 << 30, Note: "running"},
			{ID: "wu", Bytes: 300 << 20}, {ID: "bin", Bytes: 2 << 30}}, "hiber": int64(127) << 27, "downloads": int64(142) << 30, "last": "", "freed": 0, "admin": true, "binDays": 14}, true
	case "health":
		return map[string]any{"design": 60000, "full": 52000, "cycles": 210, "tempC": 47.0, "disks": []diskStub{{"SSD", "Healthy"}},
			"drives": []map[string]any{{"root": `C:\`, "free": 48, "total": 100}}, "upHours": 132.0, "autoruns": []string{"a", "b", "c"}, "limit": true}, true
	}
	return nil, false
}

type diskStub struct {
	Name   string `json:"name"`
	Health string `json:"health"`
}

func (stubBackend) Action(name string, arg map[string]string) (any, error) { return nil, nil }

func TestAPI(t *testing.T) {
	s, err := startAPI(stubBackend{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", s.Port)
	code := func(path, token string) int {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Header.Set("X-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code("/api/get/state", "") != 403 || code("/api/get/state", "wrong") != 403 || code("/api/get/state", s.Token) != 200 ||
		code("/api/get/nope", s.Token) != 404 || code("/", "") != 200 || code("/f/onest-latin-400-normal.woff2", "") != 200 {
		t.Error("api access rules")
	}
	c := DefaultConfig()
	c2, ok := SetConfigKey(c, "lang", "uk")
	if !ok || c2.Lang != "uk" {
		t.Error("SetConfigKey lang")
	}
	if _, ok := SetConfigKey(c, "nope", "1"); ok {
		t.Error("SetConfigKey accepts unknown keys")
	}
	if _, ok := SetConfigKey(c, "kill", "a\nlevel = soft"); ok {
		t.Error("SetConfigKey accepts line breaks")
	}
	for k := range ConfigMap(c) {
		if _, ok := SetConfigKey(c, k, ConfigMap(c)[k]); !ok {
			t.Errorf("key %s does not round trip", k)
		}
	}
	if !NewerVersion("v2.1.0", "2.0.0") || NewerVersion("2.0.0", "2.0.0") || NewerVersion("v1.9.9", "2.0.0") || !NewerVersion("2.0.10", "2.0.9") {
		t.Error("NewerVersion")
	}
	if port := os.Getenv("BM_MOCK"); port != "" {
		t.Logf("mock at %s", s.URL(""))
		os.WriteFile(os.Getenv("BM_MOCK"), []byte(s.URL("")), 0o644)
		time.Sleep(10 * time.Minute)
	}
}

func TestCleanAndStats(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "deep", "old.tmp"), make([]byte, 1000), 0o644)
	os.Chtimes(filepath.Join(dir, "sub", "deep", "old.tmp"), old, old)
	os.WriteFile(filepath.Join(dir, "sub", "new.tmp"), make([]byte, 500), 0o644)
	if b, n := dirSize(dir, 24*time.Hour, time.Now()); b != 1000 || n != 1 {
		t.Errorf("dirSize: %d %d", b, n)
	}
	freed, n := cleanDir(dir, 24*time.Hour, time.Now())
	if freed != 1000 || n != 1 {
		t.Errorf("cleanDir: %d %d", freed, n)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "new.tmp")); err != nil {
		t.Error("a fresh file was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "deep")); err == nil {
		t.Error("an empty folder was left")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("the root was removed")
	}

	bin := filepath.Join(dir, "bin", "S-1-5-21")
	os.MkdirAll(bin, 0o755)
	info := make([]byte, 28)
	binary.LittleEndian.PutUint64(info[0:], 2)
	binary.LittleEndian.PutUint64(info[8:], 4096)
	ft := (time.Now().Add(-30*24*time.Hour).Unix() + 11644473600) * 10000000
	binary.LittleEndian.PutUint64(info[16:], uint64(ft))
	os.WriteFile(filepath.Join(bin, "$IAAAAAA.txt"), info, 0o644)
	os.WriteFile(filepath.Join(bin, "$RAAAAAA.txt"), make([]byte, 4096), 0o644)
	binary.LittleEndian.PutUint64(info[16:], uint64((time.Now().Unix()+11644473600)*10000000))
	os.WriteFile(filepath.Join(bin, "$IBBBBBB.txt"), info, 0o644)
	os.WriteFile(filepath.Join(bin, "$RBBBBBB.txt"), make([]byte, 4096), 0o644)
	es := binEntries([]string{filepath.Join(dir, "bin")}, 14*24*time.Hour, time.Now())
	if len(es) != 1 || es[0].Size != 4096 {
		t.Fatalf("binEntries: %+v", es)
	}
	if f, n := cleanBin(es); f != 4096 || n != 1 {
		t.Errorf("cleanBin: %d %d", f, n)
	}
	if _, err := os.Stat(filepath.Join(bin, "$RBBBBBB.txt")); err != nil {
		t.Error("a fresh recycle bin item was removed")
	}

	power := "\xef\xbb\xbfвремя;режим;расход, Вт;заряд, %;видеокарта\r\n"
	for i := 0; i < 5; i++ {
		power += "2026-11-11 12:48;blackout;10.0;100;asleep\r\n2026-11-11 12:49;blackout;30.0;99;awake\r\n"
	}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local)
	out := "\xef\xbb\xbfh\r\n2026-11-10 19:53;2026-11-10 19:53;0;100;100;26.8;0\r\n2026-11-13 16:52;2026-11-13 20:24;212;100;22;10.3;2\r\n2026-11-14 16:23;2026-11-14 17:38;75;100;78;9.5;0\r\n"
	st := BuildStats(power, out, now)
	if len(st.Draws) != 2 || st.Draws[0].Watts != 10 || st.Draws[1].GPU != "awake" || st.AvgW != 20 ||
		len(st.Outages) != 2 || st.Outages[0].Mins != 75 || st.WeekCount != 2 || st.WeekMins != 287 || st.Longest != 212 {
		t.Errorf("BuildStats: %+v", st)
	}
	data, _ := os.ReadFile("testdata/region.json")
	if r, err := ParseRegion(data); err == nil {
		for g := range r.Days[0].Groups {
			for _, d := range ScheduleDays(r, g) {
				sum := 0.0
				for _, s := range d.Segs {
					f, _ := strconv.ParseFloat(s[0], 64)
					sum += f
				}
				if sum < 99.5 || sum > 100.5 {
					t.Errorf("ScheduleDays %s %s: %.2f%%", g, d.Date, sum)
				}
			}
			break
		}
	}
}

func TestOldVersionFiles(t *testing.T) {
	names := []string{"BlackoutMode-1.1.2.exe", "BlackoutMode-1.8.0-src.zip", "BlackoutMode-2.0.0.exe", "BlackoutMode-2.0.1.exe",
		"BlackoutMode-2.1.0-src.zip", "BlackoutMode-old.exe", "BlackoutMode-1.2.exe", "config.ini", "log.txt", "BlackoutMode-1.5.0.exe.bak", "Other-1.0.0.exe"}
	got := strings.Join(OldVersionFiles(names, "2.0.1"), ",")
	if got != "BlackoutMode-1.1.2.exe,BlackoutMode-1.8.0-src.zip,BlackoutMode-2.0.0.exe" {
		t.Errorf("OldVersionFiles: %s", got)
	}
}

func TestBankFix(t *testing.T) {
	c := bankCalib{CapWh: 150, MarkPct: -1}
	left, c := BankFix(c, 90)
	if left != 135 || c.MarkPct != 90 || c.Factor != 0 {
		t.Fatalf("first: %v %+v", left, c)
	}
	// counted 20 W*h, but the bank lost 20% = 30 W*h: factor 1.5
	c.ModelWh = 20
	left, c = BankFix(c, 70)
	if left != 105 || c.MarkPct != 70 || c.ModelWh != 0 || math.Abs(c.Factor-1.5) > 1e-9 {
		t.Fatalf("learn: %v %+v", left, c)
	}
	// a 2% drop only moves the estimate
	c.ModelWh = 3
	_, c = BankFix(c, 68)
	if c.MarkPct != 70 || c.ModelWh != 3 || c.Factor != 1.5 {
		t.Fatalf("small: %+v", c)
	}
	// next learn is averaged with the old factor: 10% = 15 W*h over 15 W*h -> 1.0, avg 1.25
	c.ModelWh = 15
	_, c = BankFix(c, 60)
	if math.Abs(c.Factor-1.25) > 1e-9 {
		t.Fatalf("avg: %+v", c)
	}
	// charged in between: no learning, new mark
	c.ModelWh = 40
	_, c = BankFix(c, 95)
	if c.MarkPct != 95 || c.ModelWh != 0 || c.Factor != 1.25 {
		t.Fatalf("charged: %+v", c)
	}
	if BankFactor(0) != 1 || BankFactor(1.3) != 1.3 {
		t.Fatal("BankFactor")
	}
}

func TestDrawsAndJournal(t *testing.T) {
	if DrawKey("blackout", "awake") != "blackout|awake" || DrawKey("blackout+film", "absent") != "blackout+film" {
		t.Error("DrawKey")
	}
	if LearnDraw(0, 14) != 14 || LearnDraw(20, 0.2) != 20 || math.Abs(LearnDraw(20, 10)-19) > 1e-9 {
		t.Error("LearnDraw")
	}
	for _, c := range []struct {
		mins, a, b int
		bank       float64
		want       bool
	}{{0, 100, 100, 0, false}, {1, 100, 99, 0, false}, {530, 100, 100, 0, false}, {75, 100, 78, 0, true}, {77, 100, 100, 27, true}} {
		if OutageWorthLogging(c.mins, c.a, c.b, c.bank) != c.want {
			t.Errorf("OutageWorthLogging %+v", c)
		}
	}
	// the card is asked at 30 s, then every 2 min, and less often while it stays calm
	due := func(calm int) (n int) {
		for tick := 1; tick <= 720; tick++ { // one hour
			if GPUCheckDue(tick, calm) {
				n++
			}
		}
		return
	}
	if due(0) != 31 || due(1) != 16 || due(2) != 8 || due(3) != 7 || due(9) != 7 {
		t.Errorf("GPUCheckDue: %d %d %d %d %d", due(0), due(1), due(2), due(3), due(9))
	}
	log := []byte("one\ntwo\nthree\nfour\n")
	if string(TrimLog(log, 100, 10)) != string(log) || string(TrimLog(log, 10, 12)) != "three\nfour\n" {
		t.Errorf("TrimLog: %q", TrimLog(log, 10, 12))
	}

	csv := "head\n"
	at := time.Date(2026, 11, 16, 8, 0, 0, 0, time.Local)
	for i := 0; i < 20; i++ { // an older run, then a gap, then the last one
		csv += fmt.Sprintf("%s;blackout;30.0;%d;awake\n", at.Add(time.Duration(i)*time.Minute).Format("2006-01-02 15:04"), 100-i)
	}
	at = at.Add(3 * time.Hour)
	for i := 0; i < 30; i++ {
		csv += fmt.Sprintf("%s;blackout;14.0;%d;absent\n", at.Add(time.Duration(i)*time.Minute).Format("2006-01-02 15:04"), 90-i/2)
	}
	pts, day := LastRun(csv, time.Local)
	if len(pts) != 30 || day != "2026-11-16" || pts[0].T != "11:00" || pts[29].Pct != 76 || pts[5].Watts != 14 {
		t.Errorf("LastRun: %d %s %+v", len(pts), day, pts)
	}
	seed := SeedDraws(csv)
	if seed["blackout"] != 14 || seed["blackout|awake"] != 30 || len(seed) != 2 {
		t.Errorf("SeedDraws: %v", seed)
	}
}

func TestComposeWidget(t *testing.T) {
	w, h := 180, 70
	px := make([]byte, w*h*4)
	if raw, err := os.ReadFile(os.Getenv("BM_WIDGET_IN")); err == nil && len(raw) == w*h { // text coverage for a preview
		for i, v := range raw {
			px[i*4], px[i*4+1], px[i*4+2] = v, v, v
		}
	}
	bars := widgetBars{List: []widgetBar{{0.84, BatteryColor(84)}, {0.5, colorBank}}, X: 13, Y: 52, W: w - 26, H: 3, Gap: 4}
	ComposeWidget(px, w, h, 10, bars)
	at := func(x, y int) [4]byte { o := (y*w + x) * 4; return [4]byte{px[o+2], px[o+1], px[o], px[o+3]} } // R, G, B, A
	if at(0, 0)[3] != 0 || at(w/2, 20)[3] != 204 {
		t.Errorf("corner %v, middle %v", at(0, 0), at(w/2, 20))
	}
	if c := at(30, 53); c != [4]byte{0x63, 0xd3, 0x91, 255} {
		t.Errorf("battery bar: %v", c)
	}
	if c := at(30, 60); c != [4]byte{0xf2, 0xb8, 0x4b, 255} {
		t.Errorf("bank bar: %v", c)
	}
	if c := at(150, 60); c[3] <= 204 || c[0] == 0xf2 { // the empty part of the bank bar: only the track
		t.Errorf("bank track: %v", c)
	}
	if BatteryColor(100) != colorOK || BatteryColor(30) != colorBank || BatteryColor(10) != colorLow {
		t.Error("BatteryColor")
	}
	if out := os.Getenv("BM_WIDGET_OUT"); out != "" {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := at(x, y)
				a := float64(c[3])
				if a > 0 { // back from premultiplied
					img.SetNRGBA(x, y, color.NRGBA{byte(float64(c[0]) * 255 / a), byte(float64(c[1]) * 255 / a), byte(float64(c[2]) * 255 / a), c[3]})
				}
			}
		}
		f, _ := os.Create(out)
		png.Encode(f, img)
		f.Close()
	}
}

func TestReleaseAndMeasure(t *testing.T) {
	body := []byte(`{"tag_name":"v2.4.0","assets":[
		{"name":"notes.txt","browser_download_url":"https://github.com/vkardin/blackoutmode/releases/download/v2.4.0/notes.txt"},
		{"name":"BlackoutMode-2.4.0.exe","browser_download_url":"https://github.com/vkardin/blackoutmode/releases/download/v2.4.0/BlackoutMode-2.4.0.exe","digest":"sha256:` + strings.Repeat("ab", 32) + `"}]}`)
	r := ParseRelease(body, "vkardin/blackoutmode")
	if r.Tag != "v2.4.0" || !strings.HasSuffix(r.URL, "/BlackoutMode-2.4.0.exe") || r.SHA256 != strings.Repeat("ab", 32) {
		t.Errorf("ParseRelease: %+v", r)
	}
	if other := ParseRelease(body, "someone/else"); other.Tag != "v2.4.0" || other.URL != "" {
		t.Errorf("a file from another repository must not be taken: %+v", other)
	}
	if ParseRelease([]byte("oops"), "a/b").Tag != "" {
		t.Error("ParseRelease on junk")
	}
	prog := make([]byte, 2<<20)
	prog[0], prog[1] = 'M', 'Z'
	sum := sha256.Sum256(prog)
	if CheckDownload(prog, "") != nil || CheckDownload(prog, hex.EncodeToString(sum[:])) != nil {
		t.Error("CheckDownload refuses a good file")
	}
	if CheckDownload(prog, strings.Repeat("0", 64)) == nil || CheckDownload(prog[:100], "") == nil || CheckDownload(make([]byte, 2<<20), "") == nil {
		t.Error("CheckDownload accepts a bad file")
	}

	m := &measureRun{Phase: 1}
	if m.SecondsLeft() != 8*60 {
		t.Errorf("SecondsLeft at the start: %d", m.SecondsLeft())
	}
	on := false
	for i := 0; i < 2*(measureSettle+measureTake) && !m.Finished; i++ {
		w := 30.0
		if on {
			w = 14
		}
		if i%7 == 3 {
			w = 0 // a tick without a reading
		}
		on = m.Step(w)
	}
	if !m.Finished || m.BaseW != 30 || m.SaveW != 14 || m.SecondsLeft() != 0 {
		t.Errorf("measureRun: %+v", m)
	}
	reg := schedRegion{Days: []schedDay{{Groups: map[string][]schedInterval{"2.2": nil, "1.1": nil}}, {Groups: map[string][]schedInterval{"1.1": nil, "3.1": nil}}}}
	if g := strings.Join(reg.GroupNames(), ","); g != "1.1,2.2,3.1" {
		t.Errorf("GroupNames: %s", g)
	}
}

// The version is written in two places; the release is built from both.
func TestVersionInResources(t *testing.T) {
	src, _ := os.ReadFile("main_windows.go")
	rc, _ := os.ReadFile("res/app.rc")
	m := regexp.MustCompile(`(?m)^\s*version\s*=\s*"(\d+)\.(\d+)\.(\d+)"$`).FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("no version in main_windows.go")
	}
	v := m[1] + "." + m[2] + "." + m[3]
	for _, want := range []string{"FILEVERSION " + m[1] + "," + m[2] + "," + m[3] + ",0", `"FileVersion", "` + v + `"`, `"ProductVersion", "` + v + `"`, "BlackoutMode-" + v + ".exe"} {
		if !strings.Contains(string(rc), want) {
			t.Errorf("res/app.rc does not have %s", want)
		}
	}
	log, _ := os.ReadFile("CHANGELOG.md")
	if !strings.Contains(string(log), "\n## "+v+"\n") {
		t.Errorf("CHANGELOG.md has no section for %s", v)
	}
}
