package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Config holds settings stored in config.ini. Everything except the
// process lists is edited from the tray menu.
type Config struct {
	Level         string // "max" or "soft"
	SetBrightness bool
	Brightness    int
	Kill          []string
	AutoEnable    bool
	PowerbankWh   int
	ChargeWarn    bool
	SetRefresh    bool
	RefreshHz     int
	QuietProfile  bool
	StrictCharger bool
	ECores        bool
	BTOff         bool
	PauseServices bool
	Services      []string
	PauseVPN      bool
	VPN           []string
	GPUWatch      bool
	LowWarn       bool
	Hotkey        bool

	Widget        string // "off", "auto" (shown in blackout mode) or "on"
	WidgetCorner  string // "tl", "tr", "bl", "br"
	WidgetOpacity int

	GameMode     bool
	GameTurbo    bool
	GameThermal  bool
	GamePriority bool
	GameThrottle bool
	Throttle     []string
	Games        []string
	NotGames     []string
	GameKill     []string

	BuildMode  bool // full power for builds and emulators on the charger
	BuildProcs []string
	CallMode   bool
	CallProcs  []string
	QuietAuto  bool // quiet profile at night
	QuietFrom  int
	QuietTo    int

	CleanAuto    bool
	CleanItems   []string
	CleanBinDays int

	AutorunWatch bool
	BankWarn     bool
	TgToken      string
	TgChat       string
	UpdateRepo   string
	UpdateCheck  bool
	Lang         string // ru, uk, en
	Click        string // "panel" or "toggle"
	CleanOld     bool   // move older copies of the program to the recycle bin

	LiteGame  bool // a game started while blackout mode is on
	LiteGPU   bool
	LiteTurbo bool

	FilmMode    bool
	Players     []string
	FilmWifiOff bool

	Schedule   bool
	Region     string
	Group      string
	WarnBefore bool
	WarnMin    int
	Precharge  bool
	AutoReturn bool
}

func DefaultConfig() Config {
	return Config{
		Level:         "max",
		SetBrightness: true,
		Brightness:    35,
		Kill:          nil,
		AutoEnable:    true,
		PowerbankWh:   0,
		ChargeWarn:    true,
		SetRefresh:    true,
		RefreshHz:     60,
		QuietProfile:  true,
		StrictCharger: false,
		ECores:        true,
		BTOff:         true,
		PauseServices: true,
		Services: []string{"dsaservice", "dsaupdateservice", "systemusagereportsvc", "esrv_svc",
			"inteltelemetryagent", "intelcollectorservice", "presentmonsharedservice", "wsearch", "sysmain"},
		PauseVPN: true,
		VPN:      []string{"surfshark", "cloudflarewarp", "tailscale"},
		GPUWatch: true,
		LowWarn:  true,
		Hotkey:   true,

		Widget:        "auto",
		WidgetCorner:  "tr",
		WidgetOpacity: 85,

		GameMode:     true,
		GameTurbo:    true,
		GameThermal:  true,
		GamePriority: true,
		GameThrottle: true,
		Throttle:     []string{"brave", "chrome", "msedge", "firefox", "telegram", "onedrive"},
		Games:        nil,
		NotGames:     []string{"wallpaper32", "wallpaper64"},
		GameKill:     nil,

		BuildMode:  true,
		BuildProcs: []string{"qemu-system-x86_64", "emulator", "studio64", "devenv", "msbuild", "cargo", "rustc"},
		CallMode:   false,
		CallProcs:  []string{"zoom", "ms-teams", "teams", "webex"},
		QuietAuto:  false,
		QuietFrom:  23,
		QuietTo:    7,

		CleanAuto:    true,
		CleanItems:   []string{"temp", "browsers", "wu"},
		CleanBinDays: 14,

		AutorunWatch: true,
		UpdateCheck:  true,
		BankWarn:     true,
		Lang:         "ru",
		Click:        "panel",
		CleanOld:     true,

		LiteGame:  true,
		LiteGPU:   true,
		LiteTurbo: true,

		FilmMode:    true,
		Players:     []string{"vlc", "mpc-hc64", "mpc-hc", "mpc-be64", "mpv", "potplayermini64"},
		FilmWifiOff: true,

		Schedule:   true,
		WarnBefore: true,
		WarnMin:    45,
		Precharge:  true,
		AutoReturn: true,
	}
}

func parseBool(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "yes", "true", "on", "да":
		return true
	case "0", "no", "false", "off", "нет":
		return false
	}
	return def
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parseList(val string) []string {
	var out []string
	for _, p := range strings.Split(val, ",") {
		if n := NormProc(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// ParseConfig reads "key = value" lines; unknown keys and bad values are ignored.
func ParseConfig(text string) Config {
	c := DefaultConfig()
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		i := strings.Index(line, "=")
		if i < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:i]))
		val := strings.TrimSpace(line[i+1:])
		switch key {
		case "level":
			if v := strings.ToLower(val); v == "max" || v == "soft" {
				c.Level = v
			}
		case "set_brightness":
			c.SetBrightness = parseBool(val, c.SetBrightness)
		case "brightness":
			if n, err := strconv.Atoi(val); err == nil {
				c.Brightness = clamp(n, 0, 100)
			}
		case "kill":
			c.Kill = parseList(val)
		case "auto_enable":
			c.AutoEnable = parseBool(val, c.AutoEnable)
		case "powerbank_wh":
			if n, err := strconv.Atoi(val); err == nil {
				c.PowerbankWh = clamp(n, 0, 5000)
			}
		case "charge_warn":
			c.ChargeWarn = parseBool(val, c.ChargeWarn)
		case "set_refresh":
			c.SetRefresh = parseBool(val, c.SetRefresh)
		case "refresh_hz":
			if n, err := strconv.Atoi(val); err == nil {
				c.RefreshHz = clamp(n, 30, 240)
			}
		case "quiet_profile":
			c.QuietProfile = parseBool(val, c.QuietProfile)
		case "strict_charger":
			c.StrictCharger = parseBool(val, c.StrictCharger)
		case "ecores":
			c.ECores = parseBool(val, c.ECores)
		case "bt_off":
			c.BTOff = parseBool(val, c.BTOff)
		case "pause_services":
			c.PauseServices = parseBool(val, c.PauseServices)
		case "services":
			c.Services = parseList(val)
		case "pause_vpn":
			c.PauseVPN = parseBool(val, c.PauseVPN)
		case "vpn":
			c.VPN = parseList(val)
		case "gpu_watch":
			c.GPUWatch = parseBool(val, c.GPUWatch)
		case "low_warn":
			c.LowWarn = parseBool(val, c.LowWarn)
		case "hotkey":
			c.Hotkey = parseBool(val, c.Hotkey)
		case "widget":
			if v := strings.ToLower(val); v == "off" || v == "auto" || v == "on" {
				c.Widget = v
			}
		case "widget_corner":
			if v := strings.ToLower(val); v == "tl" || v == "tr" || v == "bl" || v == "br" {
				c.WidgetCorner = v
			}
		case "widget_opacity":
			if n, err := strconv.Atoi(val); err == nil {
				c.WidgetOpacity = clamp(n, 30, 100)
			}
		case "schedule":
			c.Schedule = parseBool(val, c.Schedule)
		case "region":
			c.Region = strings.ToLower(val)
		case "group":
			c.Group = val
		case "warn_before":
			c.WarnBefore = parseBool(val, c.WarnBefore)
		case "warn_min":
			if n, err := strconv.Atoi(val); err == nil {
				c.WarnMin = clamp(n, 5, 180)
			}
		case "precharge":
			c.Precharge = parseBool(val, c.Precharge)
		case "auto_return":
			c.AutoReturn = parseBool(val, c.AutoReturn)
		case "film_wifi_off":
			c.FilmWifiOff = parseBool(val, c.FilmWifiOff)
		case "lite_game":
			c.LiteGame = parseBool(val, c.LiteGame)
		case "lite_gpu":
			c.LiteGPU = parseBool(val, c.LiteGPU)
		case "lite_turbo":
			c.LiteTurbo = parseBool(val, c.LiteTurbo)
		case "game_mode":
			c.GameMode = parseBool(val, c.GameMode)
		case "game_turbo":
			c.GameTurbo = parseBool(val, c.GameTurbo)
		case "game_thermal":
			c.GameThermal = parseBool(val, c.GameThermal)
		case "game_priority":
			c.GamePriority = parseBool(val, c.GamePriority)
		case "game_throttle":
			c.GameThrottle = parseBool(val, c.GameThrottle)
		case "build_mode":
			c.BuildMode = parseBool(val, c.BuildMode)
		case "build":
			c.BuildProcs = parseList(val)
		case "call_mode":
			c.CallMode = parseBool(val, c.CallMode)
		case "calls":
			c.CallProcs = parseList(val)
		case "quiet_auto":
			c.QuietAuto = parseBool(val, c.QuietAuto)
		case "quiet_from":
			if n, err := strconv.Atoi(val); err == nil {
				c.QuietFrom = clamp(n, 0, 23)
			}
		case "quiet_to":
			if n, err := strconv.Atoi(val); err == nil {
				c.QuietTo = clamp(n, 0, 23)
			}
		case "clean_auto":
			c.CleanAuto = parseBool(val, c.CleanAuto)
		case "clean_items":
			c.CleanItems = parseList(val)
		case "clean_bin_days":
			if n, err := strconv.Atoi(val); err == nil {
				c.CleanBinDays = clamp(n, 1, 365)
			}
		case "autorun_watch":
			c.AutorunWatch = parseBool(val, c.AutorunWatch)
		case "bank_warn":
			c.BankWarn = parseBool(val, c.BankWarn)
		case "tg_token":
			c.TgToken = val
		case "tg_chat":
			c.TgChat = val
		case "update_repo":
			c.UpdateRepo = val
		case "update_check":
			c.UpdateCheck = parseBool(val, c.UpdateCheck)
		case "lang":
			if val == "ru" || val == "uk" || val == "en" {
				c.Lang = val
			}
		case "clean_old":
			c.CleanOld = parseBool(val, c.CleanOld)
		case "click":
			if val == "panel" || val == "toggle" {
				c.Click = val
			}
		case "throttle":
			c.Throttle = parseList(val)
		case "games":
			c.Games = parseList(val)
		case "not_games":
			c.NotGames = parseList(val)
		case "game_kill":
			c.GameKill = parseList(val)
		case "film_mode":
			c.FilmMode = parseBool(val, c.FilmMode)
		case "players":
			c.Players = parseList(val)
		}
	}
	return c
}

// services150 is the default list of version 1.5.0; a config that still
// has it gets the current default instead.
const services150 = "dsaservice, dsaupdateservice, systemusagereportsvc, esrv_svc, sur qc, wsearch, sysmain, diagtrack, dosvc"

// Upgrade replaces defaults of older versions that were left untouched.
func (c *Config) Upgrade() {
	if strings.Join(c.Services, ", ") == services150 {
		c.Services = DefaultConfig().Services
	}
}

// NormProc turns "Adb.EXE " into "adb".
func NormProc(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, ".exe")
}

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// Serialize produces the full config.ini text with comments.
func (c Config) Serialize() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\r\n", a...) }
	j := func(l []string) string { return strings.Join(l, ", ") }
	w("# Режим отключения - настройки.")
	w("# Почти всё здесь меняется в окне программы. Руками имеет смысл")
	w("# править только списки программ. Изменения подхватываются сами.")
	w("")
	w("# ---------- Режим отключения ----------")
	w("# max - турбо-буст выключен, soft - турбо остаётся.")
	w("level = %s", c.Level)
	w("# Понижать яркость (1/0) и до скольки процентов.")
	w("set_brightness = %s", b2s(c.SetBrightness))
	w("brightness = %d", c.Brightness)
	w("# Программы, которые закрываются при включении режима. Через запятую, без .exe.")
	w("kill = %s", j(c.Kill))
	w("# Включать режим самому, когда пропала розетка.")
	w("auto_enable = %s", b2s(c.AutoEnable))
	w("# Полезная ёмкость павербанка в Вт*ч (48000 мА*ч - это примерно 150).")
	w("powerbank_wh = %d", c.PowerbankWh)
	w("# Предупреждать, если батарея ноутбука заряжается, пока режим включён.")
	w("charge_warn = %s", b2s(c.ChargeWarn))
	w("# Переключать экран на пониженную частоту (1/0) и на какую, Гц.")
	w("set_refresh = %s", b2s(c.SetRefresh))
	w("refresh_hz = %d", c.RefreshHz)
	w("# Ставить профиль Lenovo «Тихий». Работает только с правами администратора.")
	w("quiet_profile = %s", b2s(c.QuietProfile))
	w("# Отдавать работу энергоэффективным ядрам процессора.")
	w("ecores = %s", b2s(c.ECores))
	w("# Выключать Bluetooth (Wi-Fi остаётся включённым).")
	w("bt_off = %s", b2s(c.BTOff))
	w("# Останавливать фоновые службы на время режима. Нужны права администратора.")
	w("# В списках части имён служб, через запятую. После режима службы запускаются снова.")
	w("pause_services = %s", b2s(c.PauseServices))
	w("services = %s", j(c.Services))
	w("pause_vpn = %s", b2s(c.PauseVPN))
	w("vpn = %s", j(c.VPN))
	w("# Следить, спит ли видеокарта NVIDIA, и сообщать, кто её держит.")
	w("gpu_watch = %s", b2s(c.GPUWatch))
	w("# Предупреждать, когда в батарее осталось 30%% и 10%%.")
	w("low_warn = %s", b2s(c.LowWarn))
	w("# Включать и выключать режим клавишами Ctrl+Alt+B.")
	w("hotkey = %s", b2s(c.Hotkey))
	w("")
	w("# ---------- Виджет ----------")
	w("# off - не показывать, auto - показывать в режиме отключения, on - всегда.")
	w("widget = %s", c.Widget)
	w("# Угол экрана: tl, tr, bl, br (верх/низ, лево/право).")
	w("widget_corner = %s", c.WidgetCorner)
	w("# Непрозрачность в процентах, от 30 до 100.")
	w("widget_opacity = %d", c.WidgetOpacity)
	w("")
	w("# ---------- Игровой режим ----------")
	w("# Включаться самому, когда на экране игра и ноутбук на нормальной зарядке.")
	w("game_mode = %s", b2s(c.GameMode))
	w("# Включать турбо-буст на время игры.")
	w("game_turbo = %s", b2s(c.GameTurbo))
	w("# Переключать профиль Lenovo на «Производительность» на время игры.")
	w("game_thermal = %s", b2s(c.GameThermal))
	w("# Поднимать приоритет игры.")
	w("game_priority = %s", b2s(c.GamePriority))
	w("# Придерживать фоновые программы из списка throttle, пока идёт игра или фильм.")
	w("game_throttle = %s", b2s(c.GameThrottle))
	w("throttle = %s", j(c.Throttle))
	w("# Игры, которые не определились сами (например javaw для Minecraft).")
	w("games = %s", j(c.Games))
	w("# Программы из игровых папок, которые играми не считаются.")
	w("not_games = %s", j(c.NotGames))
	w("# Программы, которые закрываются при запуске игры.")
	w("game_kill = %s", j(c.GameKill))
	w("# Не включать игровой режим, если Lenovo сообщает о слабой зарядке.")
	w("# Нужны права администратора. Сначала проверьте по журналу, что значения верные.")
	w("strict_charger = %s", b2s(c.StrictCharger))
	w("")
	w("# ---------- Игра без света ----------")
	w("# Если игра запущена при включённом режиме отключения: экономия остаётся,")
	w("# игра получает приоритет, фоновые программы придерживаются.")
	w("lite_game = %s", b2s(c.LiteGame))
	w("# Без света запускать игры на встроенной графике, чтобы видеокарта NVIDIA спала.")
	w("# Действует при следующем запуске игры. Когда свет вернулся, выбор возвращается.")
	w("lite_gpu = %s", b2s(c.LiteGPU))
	w("# Оставлять игре немного турбо-буста, чтобы долгие расчёты не тянулись.")
	w("lite_turbo = %s", b2s(c.LiteTurbo))
	w("")
	w("# ---------- Режим кино ----------")
	w("# Включаться самому, когда открыт видеоплеер из списка players.")
	w("film_mode = %s", b2s(c.FilmMode))
	w("players = %s", j(c.Players))
	w("# Выключать Wi-Fi, пока открыт плеер и включён режим отключения.")
	w("film_wifi_off = %s", b2s(c.FilmWifiOff))
	w("")
	w("# ---------- График отключений ----------")
	w("# Брать график из открытых данных проекта «Коли Є».")
	w("schedule = %s", b2s(c.Schedule))
	w("# Область и очередь, например dnipropetrovsk и 2.1. Если пусто, берутся из виджета «Коли Є».")
	w("region = %s", c.Region)
	w("group = %s", c.Group)
	w("# Предупреждать перед отключением по графику и за сколько минут.")
	w("warn_before = %s", b2s(c.WarnBefore))
	w("warn_min = %d", c.WarnMin)
	w("# За два часа до отключения снимать лимит зарядки Lenovo, чтобы батарея была полной.")
	w("precharge = %s", b2s(c.Precharge))
	w("# Выключать режим самому, когда подключён родной блок питания и по графику свет уже есть.")
	w("auto_return = %s", b2s(c.AutoReturn))
	w("# Напоминать зарядить павербанк перед отключением, если в нём меньше половины.")
	w("bank_warn = %s", b2s(c.BankWarn))
	w("")
	w("# ---------- Профили ----------")
	w("# Сборка: полная мощность на зарядке, пока работает программа из списка build.")
	w("build_mode = %s", b2s(c.BuildMode))
	w("build = %s", j(c.BuildProcs))
	w("# Созвон: тихий вентилятор, пока на экране программа из списка calls.")
	w("call_mode = %s", b2s(c.CallMode))
	w("calls = %s", j(c.CallProcs))
	w("# Тишина: тихий профиль ночью, с какого часа и до какого.")
	w("quiet_auto = %s", b2s(c.QuietAuto))
	w("quiet_from = %d", c.QuietFrom)
	w("quiet_to = %d", c.QuietTo)
	w("")
	w("# ---------- Уборка ----------")
	w("# Раз в неделю чистить самому, на зарядке и когда ноутбук простаивает.")
	w("clean_auto = %s", b2s(c.CleanAuto))
	w("# Что чистить: temp, browsers, wu (кэш обновлений Windows), bin (корзина).")
	w("clean_items = %s", j(c.CleanItems))
	w("# Из корзины удаляются файлы старше стольких дней.")
	w("clean_bin_days = %d", c.CleanBinDays)
	w("")
	w("# ---------- Программа ----------")
	w("# Язык окна: ru, uk, en.")
	w("lang = %s", c.Lang)
	w("# Левый клик по значку: panel - мини-панель, toggle - включить или выключить режим.")
	w("click = %s", c.Click)
	w("# После обновления убирать старые версии программы из её папки в корзину.")
	w("clean_old = %s", b2s(c.CleanOld))
	w("# Предупреждать, когда новая программа прописалась в автозагрузку.")
	w("autorun_watch = %s", b2s(c.AutorunWatch))
	w("# Сообщения в Telegram: токен бота и номер чата. Пусто - не отправлять.")
	w("tg_token = %s", c.TgToken)
	w("tg_chat = %s", c.TgChat)
	w("# Раз в сутки смотреть, не вышла ли новая версия.")
	w("# Где искать: владелец/репозиторий на GitHub. Пусто - на странице программы.")
	w("update_check = %s", b2s(c.UpdateCheck))
	w("update_repo = %s", c.UpdateRepo)
	return b.String()
}

// FmtDur formats hours as "5 ч 30 мин".
func FmtDur(hours float64) string {
	if hours < 0 {
		hours = 0
	}
	m := int(hours*60 + 0.5)
	if m < 60 {
		return fmt.Sprintf("%d мин", m)
	}
	return fmt.Sprintf("%d ч %02d мин", m/60, m%60)
}

var gameDirs = []string{
	`\steamapps\common\`, `\steamlibrary\`, `\gog games\`, `\xboxgames\`, `\ea games\`,
	`\epic games\`, `\riot games\`, `\ubisoft\`, `\rockstar games\`, `\origin games\`, `\games\`,
}

var notGameMarks = []string{
	`\launcher\`, `riot client`, `social club`, `\steamworks shared\`,
	`crashhandler`, `crashreport`, `easyanticheat`, `battleye`, `installer`, `redist`,
	`unins`, `wallpaper`,
}

// Store clients live next to the games but are not games themselves.
var launcherNames = map[string]bool{
	"steam": true, "steamwebhelper": true, "steamservice": true, "epicgameslauncher": true,
	"upc": true, "ubisoftconnect": true, "uplaywebcore": true, "ubisoftgamelauncher": true,
	"ubisoftgamelauncher64": true, "riotclientservices": true, "riotclientux": true,
	"riotclientuxrender": true, "galaxyclient": true, "eadesktop": true, "ealauncher": true,
	"origin": true, "battle.net": true, "launcher": true, "gamingservices": true,
}

// IsGamePath decides whether an executable is a game: by the user's lists
// first, then by the folder it runs from.
func IsGamePath(path string, c Config) bool {
	p := strings.ToLower(path)
	name := p
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		name = p[i+1:]
	}
	name = NormProc(name)
	for _, n := range c.NotGames {
		if n == name {
			return false
		}
	}
	for _, n := range c.Games {
		if n == name {
			return true
		}
	}
	if launcherNames[name] {
		return false
	}
	for _, m := range notGameMarks {
		if strings.Contains(p, m) {
			return false
		}
	}
	for _, d := range gameDirs {
		if strings.Contains(p, d) {
			return true
		}
	}
	return false
}

// IsPlayer reports whether a normalized process name is a video player.
func IsPlayer(name string, c Config) bool {
	for _, n := range c.Players {
		if n == name {
			return true
		}
	}
	return false
}

// PowerbankOptions maps a label capacity in mA*h to usable W*h
// (3.7 V cells, about 85% conversion efficiency).
var PowerbankOptions = [][2]int{{10000, 31}, {20000, 63}, {30000, 94}, {48000, 150}, {60000, 189}}

var BrightnessOptions = []int{20, 35, 50, 70}
