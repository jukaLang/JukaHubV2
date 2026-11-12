package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// --- System status helpers ---

// batteryPercentCache keeps the sysfs read off the render path: the header
// asks for the level every frame, and on the handheld each read is a syscall
// plus a directory walk.
var batteryPercentCache struct {
	mu  sync.Mutex
	at  time.Time
	pct int
}

// batteryPercentRefresh is how long a reading is reused. Long enough to skip
// per-frame reads, short enough that the number visibly tracks the charge.
const batteryPercentRefresh = time.Second

func getBatteryPercent() int {
	batteryPercentCache.mu.Lock()
	defer batteryPercentCache.mu.Unlock()
	if !batteryPercentCache.at.IsZero() && time.Since(batteryPercentCache.at) < batteryPercentRefresh {
		return batteryPercentCache.pct
	}
	// The same sysfs battery the charging check reads (see batterySysfsDir); -1
	// means "unknown", which the battery saver treats as "do nothing".
	pct := batteryPercentIn(batterySysfsDir())
	batteryPercentCache.at = time.Now()
	batteryPercentCache.pct = pct
	return pct
}

// safeStatusText strips characters that Inter font may not render and
// collapses whitespace so the status bar never shows missing-glyph boxes.
func safeStatusText(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r > 127 || (!unicode.IsPrint(r) && r != '\t') {
			continue
		}
		if unicode.IsSpace(r) || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

var (
	rpMutex        sync.Mutex
	recentlyPlayed = map[string]bool{}
)

var (
	userConfigSaveTimer *time.Timer
	userConfigSaveMutex sync.Mutex
	pendingUserConfig   *UserConfig
	// pendingUserConfigDir is the working directory captured when the save was
	// scheduled. A debounced write must land where the change was made even if
	// the process (or a test) has changed directory by the time the timer fires.
	pendingUserConfigDir string
	// pendingUserConfigSections marks the pending save as a section save: the
	// cached user config handed in for its favourites, chat or JukaLand
	// (saveUserConfigFile).
	pendingUserConfigSections bool

	// userConfigSaveDelay is the debounce window; a variable so tests can
	// exercise the timer without sleeping two seconds.
	userConfigSaveDelay = 2 * time.Second
)

// userConfigLatest holds the Variables of the newest jukauser.json written by
// a full save (every save but a section save), decoded from the bytes written
// so that nothing in it is shared with a live map. The cached user config's own
// Variables are the ones read at boot — no save updates them — and a section
// save hands that cache to the writer; writing its Variables back put the boot
// copy of every setting, recent list and trophy over everything saved since,
// at every exit too (the teardown saves the favourites after the playtime
// flush), so a start awarded the same trophies again each time. A section
// save writes these instead.
var userConfigLatest struct {
	mu   sync.Mutex
	vars *UserVariables
}

// userConfigLatestVars returns the newest full save's Variables, or nil before
// the first full save of the run. The value is never written once stored.
func userConfigLatestVars() *UserVariables {
	userConfigLatest.mu.Lock()
	defer userConfigLatest.mu.Unlock()
	return userConfigLatest.vars
}

// saveUserConfigDebounced batches rapid successive config changes into a single
// disk write after a short quiet period. This avoids excessive I/O on handheld
// devices when, for example, a user is typing in an input field.
// userConfigWritesEnabled gates every jukauser.json write. The test binary
// turns it off: tests load the shipped config and exercise settings paths, and
// a run used to rewrite jukauser.json and its .bak in the checkout. The tests
// that assert a real save turn it back on for their own scope.
var userConfigWritesEnabled = true

func saveUserConfigDebounced(user *UserConfig) {
	if user == nil || !userConfigWritesEnabled {
		return
	}
	// The write runs on a timer goroutine two seconds from now, and the Custom
	// map handed in is the live one the main thread keeps writing (playtime's
	// counters every second, background updates, every keystroke). Iterating
	// it there was a fatal "concurrent map iteration and map write". It is
	// copied here, on the caller's goroutine, so the timer owns everything it
	// touches. A section save (the cached user config) needs no copy once a
	// full save is on record: the writer takes its settings from that and its
	// sections from the cache as they are then (saveUserConfigFile).
	sections := user == userConfigCache
	if sections && userConfigLatestVars() != nil {
		user = &UserConfig{}
	} else {
		user = detachUserConfig(user)
	}
	userConfigSaveMutex.Lock()
	if sections && pendingUserConfig != nil && !pendingUserConfigSections {
		// A full save waiting in the slot carries this change already — it
		// fills its sections from the cache when it is written — with newer
		// settings. Its timer is left as it is, so a busy chat cannot keep
		// putting it off.
		userConfigSaveMutex.Unlock()
		return
	}
	pendingUserConfig = user
	pendingUserConfigSections = sections
	pendingUserConfigDir, _ = os.Getwd()
	if userConfigSaveTimer != nil {
		userConfigSaveTimer.Stop()
	}
	userConfigSaveTimer = time.AfterFunc(userConfigSaveDelay, func() {
		userConfigSaveMutex.Lock()
		defer userConfigSaveMutex.Unlock()
		if pendingUserConfig != nil {
			saveUserConfigFile(pendingUserConfigDir, pendingUserConfig, pendingUserConfigSections)
			pendingUserConfig = nil
			pendingUserConfigDir = ""
		}
	})
	userConfigSaveMutex.Unlock()
}

// flushPendingUserConfig writes a debounced jukauser.json save now, so the
// file holds the latest settings for whoever reads it next.
func flushPendingUserConfig() {
	userConfigSaveMutex.Lock()
	defer userConfigSaveMutex.Unlock()
	if userConfigSaveTimer != nil {
		userConfigSaveTimer.Stop()
	}
	if pendingUserConfig != nil {
		saveUserConfigFile(pendingUserConfigDir, pendingUserConfig, pendingUserConfigSections)
		pendingUserConfig = nil
		pendingUserConfigDir = ""
	}
}

// reloadUserConfigFromDisk refreshes the cached settings from jukauser.json
// and returns the cache. Its typed fields are filled once at boot and no save
// updates them, so a hot reload that merged the cache restored the boot value
// of every setting changed since. The favorites, chat and JukaLand sections
// stay as they are in memory: those are always the newest copy.
func reloadUserConfigFromDisk() *UserConfig {
	uc, err := readUserConfigFile("jukauser.json")
	if err != nil || uc == nil {
		return loadUserConfig()
	}
	if uc.Variables.Custom == nil {
		uc.Variables.Custom = make(map[string]interface{})
	}
	if userConfigCache == nil {
		userConfigCache = uc
		return uc
	}
	userConfigCache.Variables = uc.Variables
	return userConfigCache
}

// detachUserConfig returns a copy of user whose Custom map shares nothing with
// the live config. Each persisted value goes through JSON — exactly what the
// save does with it — so nested maps and slices (playtime's day and scene
// counters) come back as fresh values the caller can keep writing. Volatile
// keys are never saved, so they are left out rather than copied.
func detachUserConfig(user *UserConfig) *UserConfig {
	if user == nil {
		return nil
	}
	c := *user
	if user.Variables.Custom == nil {
		return &c
	}
	c.Variables.Custom = make(map[string]interface{}, len(user.Variables.Custom))
	for k, v := range user.Variables.Custom {
		if volatileCustomVars[k] {
			continue
		}
		data, err := json.Marshal(v)
		if err != nil {
			continue
		}
		var cp interface{}
		if json.Unmarshal(data, &cp) != nil {
			continue
		}
		c.Variables.Custom[k] = cp
	}
	return &c
}

// userConfigFromVariables snapshots the persisted half of a live config into
// the shape saveUserConfigDebounced expects. Every caller used to build this
// struct by hand; sharing it keeps a new setting from being remembered in one
// copy and forgotten in another.
func userConfigFromVariables(config *Config) *UserConfig {
	if config == nil {
		return nil
	}
	user := &UserConfig{Variables: UserVariables{
		ButtonColor:        config.Variables.ButtonColor,
		LabelColor:         config.Variables.LabelColor,
		InputColor:         config.Variables.InputColor,
		Fullscreen:         config.Variables.Fullscreen,
		FileExplorerRoot:   config.Variables.FileExplorerRoot,
		WeatherEnabled:     config.Variables.WeatherEnabled,
		WeatherUnit:        config.Variables.WeatherUnit,
		TSPUsername:        config.Variables.TSPUsername,
		PlaybackResolution: config.Variables.PlaybackResolution,
		SubtitleLanguage:   config.Variables.SubtitleLanguage,
		GridColumns:        config.Variables.GridColumns,
		SearchWidth:        config.Variables.SearchWidth,
		DynamicBg:          config.Variables.DynamicBg,
		ScreensaverTimeout: config.Variables.ScreensaverTimeout,
		Clock12:            config.Variables.Clock12,
		ReducedMotion:      config.Variables.ReducedMotion,
		LowPower:           config.Variables.LowPower,
		PowerMode:          config.Variables.PowerMode,
		LedSync:            config.Variables.LEDSync,
		Custom:             config.Variables.Custom,
	}}
	// Netplay's switch is a pointer in the file (a file without it keeps the
	// shipped default), and only Quick Settings used to fill it: a file last
	// written by any other save started the next run on the default.
	netplay := config.Variables.NetPlayEnabled
	user.Variables.NetPlayEnabled = &netplay
	// Levels are owned by Quick Settings and live only in jukauser.json. Carry
	// them through so a save triggered by any other screen does not reset them.
	if userConfigCache != nil {
		user.Variables.Volume = userConfigCache.Variables.Volume
		user.Variables.VolumeSet = userConfigCache.Variables.VolumeSet
		user.Variables.Brightness = userConfigCache.Variables.Brightness
		user.Variables.BrightnessSet = userConfigCache.Variables.BrightnessSet
	}
	return user
}

func loadRecentlyPlayed() {
	user := loadUserConfig()
	if user == nil || user.Variables.Custom == nil {
		return
	}
	val, ok := user.Variables.Custom["recently_played"]
	if !ok {
		return
	}
	list, ok := val.([]interface{})
	if !ok {
		return
	}
	for _, item := range list {
		if s, ok := item.(string); ok {
			recentlyPlayed[s] = true
		}
	}
}

func isRecentlyPlayed(key string) bool {
	rpMutex.Lock()
	defer rpMutex.Unlock()
	return recentlyPlayed[key]
}

func recordPlayed(config *Config, key string) {
	if key == "" {
		return
	}
	rpMutex.Lock()
	recentlyPlayed[key] = true
	var list []string
	for k := range recentlyPlayed {
		list = append(list, k)
	}
	rpMutex.Unlock()

	if config != nil {
		config.Variables.Custom["recently_played"] = list
	}

	saveUserConfigDebounced(userConfigFromVariables(config))
}

// --- Weather (best-effort IP-geolocated 7-day forecast; non-fatal) ---

// WeatherDay holds one day of the weekly forecast.
type WeatherDay struct {
	Date string  // ISO date "2006-01-02"
	TMax float64 // °C
	TMin float64 // °C
	Code int     // WMO weather code
	Pop  int     // precipitation probability %
}

var (
	wxMutex      sync.Mutex
	weatherReady bool
	weatherDaily []WeatherDay
)

func startWeather(config *Config) {
	// Adopt the Settings switch first (weather_toggle.go): with weather off
	// nothing is restored or fetched, and the forecast stays off every screen.
	weatherSyncEnabled(config)
	// Put the last saved forecast on screen before the first network attempt:
	// an offline boot then shows weather instead of "unavailable".
	if weatherIsEnabled() {
		restoreWeatherCache(time.Now())
	}
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		wake := subscribeForegroundWake()
		fetchWeatherOnce()
		for {
			// Ten minutes is a long time to sit out while a game owns the screen; a
			// wake when JukaHub is back refreshes the header immediately instead
			// of leaving yesterday's forecast up. See backgroundwork.go. The
			// Settings switch kicks the loop too when weather is turned back on.
			select {
			case <-ticker.C:
			case <-wake:
			case <-weatherKick:
			}
			if backgroundPaused() || !weatherIsEnabled() {
				continue
			}
			fetchWeatherOnce()
		}
	}()
}

// weatherCodeDesc maps a WMO weather code to a short, font-safe description.
// Emoji are intentionally avoided because the UI font may not contain them.
func weatherCodeDesc(code int) string {
	switch code {
	case 0:
		return "Clear"
	case 1:
		return "Mainly clear"
	case 2:
		return "Partly cloudy"
	case 3:
		return "Overcast"
	case 45, 48:
		return "Fog"
	case 51, 53, 55:
		return "Drizzle"
	case 56, 57:
		return "Freezing drizzle"
	case 61, 63, 65:
		return "Rain"
	case 66, 67:
		return "Freezing rain"
	case 71, 73, 75:
		return "Snow"
	case 77:
		return "Snow grains"
	case 80, 81, 82:
		return "Showers"
	case 85, 86:
		return "Snow showers"
	case 95:
		return "Thunderstorm"
	case 96, 99:
		return "Thunderstorm+hail"
	default:
		return "—"
	}
}

type geoLocation struct {
	Latitude    float64
	Longitude   float64
	CountryCode string
	// TimeZone is the IANA zone the provider places the address in, when it
	// says ("Europe/Berlin"); Settings → Date & time's "Set automatically".
	TimeZone string
}

// lookupLocationContext tries the providers in order, stopping when the
// caller's context ends so a cancelled scene does not wait out three timeouts.
func lookupLocationContext(ctx context.Context, client *http.Client) (geoLocation, bool) {
	for _, provider := range []func(context.Context, *http.Client) (geoLocation, bool){
		lookupIPWhoIs, lookupIPAPICo, lookupIPInfo,
	} {
		if ctx != nil && ctx.Err() != nil {
			return geoLocation{}, false
		}
		if g, ok := provider(ctx, client); ok {
			return g, true
		}
	}
	return geoLocation{}, false
}

func getGeoJSON(ctx context.Context, client *http.Client, url string, dst any) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dst) == nil
}

func lookupIPWhoIs(ctx context.Context, client *http.Client) (geoLocation, bool) {
	var v struct {
		Success     bool    `json:"success"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
		CountryCode string  `json:"country_code"`

		TimeZone struct {
			ID string `json:"id"`
		} `json:"timezone"`
	}
	if !getGeoJSON(ctx, client, "https://ipwho.is/", &v) || !v.Success {
		return geoLocation{}, false
	}
	if v.Latitude == 0 && v.Longitude == 0 {
		return geoLocation{}, false
	}
	return geoLocation{Latitude: v.Latitude, Longitude: v.Longitude, CountryCode: v.CountryCode, TimeZone: v.TimeZone.ID}, true
}

func lookupIPAPICo(ctx context.Context, client *http.Client) (geoLocation, bool) {
	var v struct {
		Error       bool    `json:"error"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
		CountryCode string  `json:"country_code"`
		TimeZone    string  `json:"timezone"`
	}
	if !getGeoJSON(ctx, client, "https://ipapi.co/json/", &v) || v.Error {
		return geoLocation{}, false
	}
	if v.Latitude == 0 && v.Longitude == 0 {
		return geoLocation{}, false
	}
	return geoLocation{Latitude: v.Latitude, Longitude: v.Longitude, CountryCode: v.CountryCode, TimeZone: v.TimeZone}, true
}

func lookupIPInfo(ctx context.Context, client *http.Client) (geoLocation, bool) {
	var v struct {
		Loc     string `json:"loc"`
		Country string `json:"country"`
		Zone    string `json:"timezone"`
	}
	if !getGeoJSON(ctx, client, "https://ipinfo.io/json", &v) {
		return geoLocation{}, false
	}
	parts := strings.Split(v.Loc, ",")
	if len(parts) != 2 {
		return geoLocation{}, false
	}
	lat, errLat := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	lon, errLon := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if errLat != nil || errLon != nil || (lat == 0 && lon == 0) {
		return geoLocation{}, false
	}
	return geoLocation{Latitude: lat, Longitude: lon, CountryCode: v.Country, TimeZone: v.Zone}, true
}

func fetchWeatherOnce() {
	fetchWeatherOnceContext(context.Background())
}

// fetchWeatherOnceContext refreshes the forecast under a caller's context, so
// the weather screen's fetch stops when the screen is left. The 20-second bound
// still applies to the background ticker, which has no scene to cancel it.
func fetchWeatherOnceContext(parent context.Context) {
	// Weather switched off in Settings: no geolocation, no forecast request.
	// Every caller (the poller, the popover, the Weather screen) comes here.
	if !weatherIsEnabled() {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 6 * time.Second}
	lat, lon := 40.71, -74.0 // default fallback (New York) if geolocation fails
	g, ok := lookupLocationContext(ctx, client)
	if ok {
		lat, lon = g.Latitude, g.Longitude
		setWeatherCountry(g.CountryCode)
	} else if ctx.Err() != nil {
		return
	} else {
		logWarn("extras", "Geolocation unavailable, using default weather location")
	}
	url := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&daily=temperature_2m_max,temperature_2m_min,weather_code,precipitation_probability_max&forecast_days=7&timezone=auto", lat, lon)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := doWithRetry(ctx, client, req, defaultRetryPolicy())
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var w struct {
		// The location's offset: the daily dates below are its calendar.
		UTCOffset *int `json:"utc_offset_seconds"`
		Daily     struct {
			Time []string  `json:"time"`
			TMax []float64 `json:"temperature_2m_max"`
			TMin []float64 `json:"temperature_2m_min"`
			Code []int     `json:"weather_code"`
			Pop  []int     `json:"precipitation_probability_max"`
		} `json:"daily"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if w.UTCOffset != nil {
		weatherSetLocationOffset(*w.UTCOffset, true)
	}
	n := len(w.Daily.Time)
	days := make([]WeatherDay, 0, n)
	for i := 0; i < n; i++ {
		d := WeatherDay{Date: w.Daily.Time[i]}
		if i < len(w.Daily.TMax) {
			d.TMax = w.Daily.TMax[i]
		}
		if i < len(w.Daily.TMin) {
			d.TMin = w.Daily.TMin[i]
		}
		if i < len(w.Daily.Code) {
			d.Code = w.Daily.Code[i]
		}
		if i < len(w.Daily.Pop) {
			d.Pop = w.Daily.Pop[i]
		}
		days = append(days, d)
	}
	// The switch may have been turned off while the request was in flight; a
	// late answer must not put the forecast back on screen.
	if !weatherIsEnabled() {
		return
	}
	wxMutex.Lock()
	weatherDaily = days
	weatherReady = len(days) > 0
	wxMutex.Unlock()
	// Persist the forecast so the next boot has weather before the network is
	// up; a failed write is logged, never fatal.
	if len(days) > 0 {
		noteWeatherFetched(days, time.Now())
	}
	// Update dynamic background with today's weather.
	if day, ok := weatherDayFor(days, time.Now()); ok {
		DynBGUpdateWeather(day.Code)
	}
}

// sceneDisplayName maps internal scene identifiers to human-readable labels
// shown in the header. The UI never prints a raw config/enum identifier.
func sceneDisplayName(name string) string {
	if label := tbxDisplayName(name); label != "" {
		return label
	}
	// Control profiles and Local play (inmap_ui.go).
	if label := inmapSceneDisplayName(name); label != "" {
		return label
	}
	// Experience packs, Config and Backup & restore (xpackroute.go).
	if label := xpackDisplayName(name); label != "" {
		return label
	}
	switch name {
	case "FileExplorer":
		return "Files"
	case "SettingsGeneral":
		return "General"
	case "SettingsAppearance":
		return "Appearance"
	case "SettingsSmart":
		return "Smart Widgets"
	case "ThemePresets":
		return "Themes"
	case "UnitConverter":
		return "Converter"
	case "CanvasSandbox":
		return "Canvas"
	case "LogExporter":
		return "Logs"
	case "NetSpeed":
		return "Speed test"
	case "DiskSpace":
		return "Storage"
	case "Hardware":
		return "Hardware"
	case "Browser":
		return "Browser"
	case "Tube":
		return "Media"
	case "Shorts":
		return "Shorts"
	case "TextBrowser":
		return "Text"
	case "IPStream":
		return "Streams"
	case "LiveTV":
		return "Live TV"
	case "Cron":
		return "Scheduled tasks"
	case netToolsSceneName:
		return "Network tools"
	case wifiScrSceneName:
		return "Wi-Fi"
	case "Apps":
		return "Apps"
	case "Patch":
		return "Patch"
	case "Packages":
		return "Package Manager"
	case "MusicPlayer":
		return "Music"
	case "Games":
		return "Games"
	case "Notes":
		return "Notes"
	case "Calculator":
		return "Calculator"
	case "Playtime":
		return "Trophies"
	case "PDFReader":
		return "Reader"
	case vkSettingsSceneName:
		return "Keyboard"
	case rommSceneName:
		return "ROM manager"
	case apkStoreSceneName:
		return apkStoreEntryName
	case voiceSceneName:
		return "Voice command"
	case vscSceneName:
		return "Voice shortcuts"
	default:
		return name
	}
}

// versionString returns the canonical "vX.Y.Z" value. The repository's single
// version source is config.Version (jukaconfig.json "Version", with a fallback
// in config_health); the prefix is normalized for update and report metadata.
func versionString(config *Config) string {
	v := ""
	if config != nil {
		v = strings.TrimSpace(config.Version)
	}
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		v = "0.4.0"
	}
	return "v" + v
}

// --- Top status bar (brand + scene + clock/weather/battery) ---

func renderStatusBar(renderer *sdl.Renderer, config *Config) {
	sceneTitleBackRect = sdl.Rect{}
	statusClockRect = sdl.Rect{}
	statusWiFiRect = sdl.Rect{}
	statusVolumeRect = sdl.Rect{}
	statusBluetoothRect = sdl.Rect{}
	statusBrightnessRect = sdl.Rect{}
	weatherStatusRect = sdl.Rect{}
	statusMediaRect = sdl.Rect{}
	headerTrophyClear()
	headerBugClear()
	// The release pill is measured again below whenever it is drawn; a rect
	// left from a frame that drew it (an offer since installed, screenshot
	// mode) would still take a click and be a focus stop where the cup is now.
	updatePrompt.badge = sdl.Rect{}
	barH := headerBarHeight()
	// Home's status row has no bar chrome: the wordmark and the system corner
	// sit straight on the backdrop. The band is repainted with the backdrop's
	// own pixels rather than left alone, so a body that scrolls up under the
	// header cannot show through the wordmark — and the header reads as part
	// of the page, the way it does on Home, instead of a slate strip on top.
	drawAppBackdropBand(renderer, config, sdl.Rect{X: 0, Y: 0, W: screenWidth, H: barH})

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	scene := SceneConfig{}
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		scene = config.Scenes[currentSceneIndex]
	}
	// One brand block, one caption type and one button height on every screen:
	// the bar decides those in header.go, so Home and a scene cannot drift into
	// two different headers again. UX-2: the screen's name is its page header's,
	// so the bar does not repeat it or Home's build date.
	headerTextW := drawHeaderBrand(renderer, config, barH,
		headerCaptionText("", statusBarCaption(scene)))
	nextX := headerTextW + SpaceMD

	// The Back control is the bar's loudest button: larger than the rest, in
	// the accent colour, which it swaps for a solid fill (and dark type) while
	// the D-pad holds it. Home is the root and has nowhere to go back to, so it
	// draws none. (JukaLand used to skip it too, when B broke blocks; B leaves
	// the game now like every other screen.)
	if scene.Layout != "home" && scene.Name != "" {
		sceneTitleBackRect = drawHeaderBack(renderer, config, nextX, barH,
			focusEngine.ElementIndex() == ChromeBackIndex)
		if sceneTitleBackRect.W > 0 {
			nextX = sceneTitleBackRect.X + sceneTitleBackRect.W + SpaceSM
		}
	}

	// App menu (the old taskbar Start button). Its rect is measured here so the
	// focus graph, keyboard activation and mouse hit-testing all agree on where
	// it is.
	{
		tb.startRect = drawHeaderMenu(renderer, config, nextX, barH, tb.startOpen,
			focusEngine.ElementIndex() == ChromeStartIndex)
		headerTextW = tb.startRect.X + tb.startRect.W - StatusBarMargin
	}

	// Watch Party chip: a live room is a session-level state, so it belongs in
	// the same place as the safe-mode chip. The count is the number of devices
	// currently present, which makes "did my friend join?" visible at a glance.
	if chipFont := headerCaptionFont(config); partyActive() && chipFont != nil {
		snap, _, _, _ := partyState()
		label := "PARTY"
		if n := len(snap.Members); n > 1 {
			label = fmt.Sprintf("PARTY %d", n)
		}
		pw, _ := measureText(chipFont, label)
		pad := int32(10)
		sh := headerPillHeight()
		w := pw + pad*2
		x := tb.startRect.X + tb.startRect.W + SpaceSM
		if safeModeActive() {
			// The safe chip is drawn next; keep this one to its left by
			// shifting right after it instead of overlapping.
			sw, _ := measureText(chipFont, "SAFE")
			x += sw + pad*2 + SpaceSM
		}
		y := (barH - sh) / 2
		fillRoundedRect(renderer, x, y, w, sh, sh/2, WithAlpha(accentColor, 45))
		strokeRoundedRect(renderer, x, y, w, sh, sh/2, 1, WithAlpha(accentColor, 210))
		renderText(renderer, config, chipFont, label, accentColor, x+pad, y+(sh-int32(chipFont.Height()))/2)
		headerTextW += SpaceSM + w
	}

	// Safe-mode chip: a persistent reminder that background network, plugin
	// and updater work is suppressed for this launch.
	if chipFont := headerCaptionFont(config); safeModeActive() && chipFont != nil {
		label := "SAFE"
		sw, _ := measureText(chipFont, label)
		pad := int32(10)
		sh := headerPillHeight()
		w := sw + pad*2
		x := tb.startRect.X + tb.startRect.W + SpaceSM
		y := (barH - sh) / 2
		warn := sdl.Color{R: 235, G: 170, B: 60, A: 255}
		fillRoundedRect(renderer, x, y, w, sh, sh/2, WithAlpha(warn, 40))
		strokeRoundedRect(renderer, x, y, w, sh, sh/2, 1, WithAlpha(warn, 210))
		renderText(renderer, config, chipFont, label, warn, x+pad, y+(sh-int32(chipFont.Height()))/2)
		headerTextW += SpaceSM + w
	}

	// Right side of the bar: the status cluster (brightness, volume,
	// Bluetooth, network, weather, time, battery, the chip) and the corner's
	// buttons (the release offer, the trophy room's cup, Report a bug, the
	// notification bell), identical to the home header so the right side of
	// the top bar stays put across screens. They stand in the user's order
	// (Edit Home → Top bar, header_arrange.go); the shipped order ends in
	// the corner, bell at the margin.
	headerSlotRectsClear()
	parts := headerStatusParts(config)
	statusFont := headerStatusFont()
	if statusFont == nil {
		statusFont = font
	}
	bellW, bellH := notifBellSize(config)
	right := screenWidth - StatusBarMargin
	// The corner's buttons are decided first, the way they always were: the
	// bell and Report a bug always, the cup only on a bar with room for the
	// clock and the battery beside it, the offer while a release is
	// advertised. Each sits one corner gap from its neighbour (the same
	// spacing Home uses), not at the wider spacing the cluster parts use.
	// usedW is the room they take, each with its gap.
	var corner [hsCount]int32
	usedW := int32(0)
	if bellW > 0 {
		corner[hsBell] = bellW
		usedW += bellW + headerCornerGap
	}
	if bugW, _ := headerBugSize(config); bugW > 0 && !headerSlotHidden(config, hsBug) {
		corner[hsBug] = bugW
		usedW += bugW + headerCornerGap
	}
	if trophyW, _ := headerTrophySize(config); trophyW > 0 && !headerSlotHidden(config, hsTrophy) &&
		right-usedW-trophyW-headerCornerGap-(StatusBarMargin+headerTextW+Space2XL) >= headerTrophyMinCluster {
		corner[hsTrophy] = trophyW
		usedW += trophyW + headerCornerGap
	}
	if updateAvailable() && !headerSlotHidden(config, hsUpdate) {
		if pillW, _ := updatePillSize(headerCaptionFont(config)); pillW > 0 {
			corner[hsUpdate] = pillW
			usedW += pillW + headerCornerGap
		}
	}
	gap := StatusPartGap

	// Measure total width and drop least-important items if they don't fit
	// next to the brand/version/scene/back cluster. Battery and clock are what
	// the user needs at a glance, so the network name goes first. The room is
	// what is left between that cluster and the corner (bell, cup, offer): the
	// screen's width alone let a crowded cluster run under the Menu pill.
	maxW := right - usedW - StatusBarMargin - headerTextW - Space2XL
	if maxW < 120 {
		maxW = 120
	}
	partW := func(p string) int32 {
		return statusClusterPartWidth(statusFont, p)
	}
	parts = fitStatusParts(parts, partW, gap, maxW)
	// A corner button the user moved in among the parts adds a gap or two:
	// the plan trims on until it fits the same room.
	_, plan := headerPlanFit(config, parts, partW, &corner, gap, headerCornerGap, right, right-usedW-maxW)

	// Vertically center the cluster in the bar (same 16px font and position
	// as the home header).
	statusY := (barH - int32(statusFont.Height())) / 2
	if statusY < 0 {
		statusY = 0
	}
	// Two parts are drawn as gauges rather than text: the network slot
	// (signal bars, empty without a link) and the battery (a colour-coded cell).
	// drawStatusClusterPart returns the same width statusClusterPartWidth
	// measured, so the clock and the bell never move.
	drawStatusText := func(text string, color sdl.Color, x, y int32) {
		renderText(renderer, config, statusFont, text, color, x, y)
	}
	focusIdx := focusEngine.ElementIndex()
	for _, it := range plan {
		switch it.slot {
		case hsBell:
			// Notification bell: always reachable with the D-pad.
			headerSlotRects[hsBell] = renderNotifBell(renderer, config, it.x+it.w, (barH-bellH)/2)
			continue
		case hsBug:
			headerSlotRects[hsBug] = drawHeaderBug(renderer, config, it.x+it.w, barH, focusIdx == ChromeBugIndex)
			continue
		case hsTrophy:
			headerSlotRects[hsTrophy] = drawHeaderTrophy(renderer, config, it.x+it.w, barH, focusIdx == ChromeTrophyIndex)
			continue
		case hsUpdate:
			// The release offer (only while an update is advertised) is the
			// same pill as on Home.
			pill := drawHeaderUpdatePill(renderer, config, it.x+it.w, barH, focusIdx == ChromeUpdateIndex)
			updatePrompt.badge = pill
			headerSlotRects[hsUpdate] = pill
			continue
		}
		p, startX, pw := it.part, it.x, it.w
		drawStatusClusterPart(renderer, statusFont, p, startX, statusY, drawStatusText)
		r := sdl.Rect{
			X: startX - 6, Y: statusY - 6,
			W: pw + 12, H: int32(statusFont.Height()) + 12,
		}
		headerSlotRects[it.slot] = r
		switch {
		case isClockStatusPart(p):
			// Measure the clock so it can hold focus like any other button.
			statusClockRect = r
		case isWeatherStatusPart(p):
			// The hi/lo chip is the Local weather button.
			weatherStatusRect = r
		case isWiFiStatusPart(p):
			// The meter is the Wi-Fi button on every screen.
			statusWiFiRect = r
		case isVolumeStatusPart(p):
			// The speaker is the volume button on every screen.
			statusVolumeRect = r
		case isBluetoothStatusPart(p):
			// The rune opens the Bluetooth page (statusbar_bluetooth.go).
			statusBluetoothRect = r
		case isBrightnessStatusPart(p):
			// The sun is the brightness button.
			statusBrightnessRect = r
		case isChipStatusPart(p):
			// The now-playing chip opens the player it is naming; the idle
			// output chip is measured the same way so it draws in the same
			// spot, but it does not join the focus graph.
			statusMediaRect = r
		}
	}
	// The meter, the Bluetooth rune, the speaker and the sun are buttons, so
	// their hit areas are grown to finger-sized targets once the bar is
	// placed and the neighbours' rects are known. Each gap between two of
	// them is split at its middle (statusGapNeighbours) instead of being
	// claimed by both, and none grows over another control.
	headerGrowStatusButtons(barH)
	// Focused header chips wear the ring Home's header draws: a rounded 3px
	// accent outline (glassFocusRingWidth), not a square hairline box.
	for _, chip := range []struct {
		index int
		rect  sdl.Rect
	}{
		{ChromeClockIndex, statusClockRect},
		{ChromeWeatherIndex, weatherStatusRect},
		{ChromeBrightnessIndex, statusBrightnessRect},
		{ChromeVolumeIndex, statusVolumeRect},
		{ChromeBluetoothIndex, statusBluetoothRect},
		{ChromeWiFiIndex, statusWiFiRect},
		{ChromeMediaIndex, statusMediaRect},
	} {
		if focusIdx == chip.index && chip.rect.W > 0 {
			r := chip.rect
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, glassFocusRingWidth, glassAccent())
		}
	}
}

// fitStatusParts trims the status cluster until it fits maxW. The network name
// is the only part that may go first: an offline warning explains failures and
// stays, and so do the weather, clock and battery readings.
func fitStatusParts(parts []string, width func(string) int32, gap, maxW int32) []string {
	total := int32(0)
	for _, p := range parts {
		total += width(p)
	}
	if len(parts) > 1 {
		total += gap * int32(len(parts)-1)
	}
	for total > maxW && len(parts) > 1 {
		drop := statusDropIndex(parts)
		total -= width(parts[drop]) + gap
		parts = append(parts[:drop], parts[drop+1:]...)
	}
	return parts
}

// statusDropIndex picks the least important part to remove when the bar is
// crowded: the network meter first, then the forecast, then the Bluetooth
// button, then the offline badge, then the brightness button, then the volume
// button. The clock and the battery level are what the user glanced at, so
// they leave last.
//
// The network slot used to be found as "index 0"; the volume button leads the
// cluster now, so every slot is found by what it is. Left as index 0, the
// speaker would have been the first thing a crowded bar gave up.
func statusDropIndex(parts []string) int {
	// The chip goes before any reading: it is the one part that can be
	// arbitrarily long — a media title or a headset's name — and the network,
	// forecast, clock and battery are what the corner exists for.
	if n := len(parts); n > 1 && isChipStatusPart(parts[n-1]) {
		if videoMiniVisible() && isMediaStatusPart(parts[n-1]) {
			if n == 2 {
				return 0
			}
		} else if n > 2 {
			return n - 1
		}
	}
	if len(parts) > 2 {
		for i, p := range parts {
			if p == statusWiFiMarker {
				return i
			}
		}
	}
	for i, p := range parts {
		if isWeatherStatusPart(p) {
			return i
		}
	}
	if len(parts) > 2 {
		// The offline badge explains why things fail, so the Bluetooth rune
		// gives way before it.
		for i, p := range parts {
			if isBluetoothStatusPart(p) {
				return i
			}
		}
		for i, p := range parts {
			if strings.HasPrefix(p, "! Offline") {
				return i
			}
		}
		for i, p := range parts {
			if isBrightnessStatusPart(p) {
				return i
			}
		}
		for i, p := range parts {
			if isVolumeStatusPart(p) {
				return i
			}
		}
	}
	if videoMiniVisible() && len(parts) > 1 && isMediaStatusPart(parts[len(parts)-1]) {
		return len(parts) - 2
	}
	return len(parts) - 1
}

// headerStatusParts returns the right-side status cluster in one canonical
// order shared by the home header and every scene's status bar (both lay it
// out in the user's own order with headerPlan, header_arrange.go): brightness,
// volume, Bluetooth, network, weather (hi/lo), time (HH:MM), battery, and the
// chip — the now-playing title while something plays, the connected output's
// name while nothing does. Both headers draw this exact list, so the right side
// of the top bar never shifts or reorders between screens.
func headerStatusParts(config *Config) []string {
	parts := make([]string, 0, 8)
	// The brightness button leads the cluster, immediately left of the volume
	// button, once the brightness controller knows what it drives
	// (brightnessctl.go) — always, after a moment: its last resort is a
	// software dimmer.
	if statusBrightnessAvailable() {
		parts = append(parts, statusBrightnessPart)
	}
	// The volume button follows, wherever there is a mixer for it to drive
	// (volumectl.go).
	if statusVolumeAvailable() {
		parts = append(parts, statusVolumePart)
	}
	// Then the Bluetooth button, immediately left of the Wi-Fi meter,
	// wherever the Bluetooth page can do something (statusbar_bluetooth.go).
	if statusBluetoothAvailable() {
		parts = append(parts, statusBluetoothPart)
	}
	// Offline indicator takes priority over the SSID so the user instantly
	// knows why network features are failing.
	// The slot always exists, even when the meter has no bars to fill: an empty
	// meter next to the battery is the honest "no link" state, and it is the
	// click target for the Wi-Fi picker.
	if !IsOnline() {
		parts = append(parts, "! Offline")
	} else {
		parts = append(parts, statusWiFiMarker)
	}
	wxMutex.Lock()
	ready := weatherReady
	today, haveToday := weatherDayFor(weatherDaily, time.Now())
	wxMutex.Unlock()
	if ready && haveToday {
		unit := weatherUnit(config)
		hi, lo := weatherTemp(today.TMax, unit), weatherTemp(today.TMin, unit)
		parts = append(parts, safeStatusText(fmt.Sprintf("%d%s / %d%s", int(hi), unit, int(lo), unit)))
	}
	parts = append(parts, safeStatusText(clockDigits(config, time.Now())))
	if bat := statusBatteryPercent(); bat >= 0 {
		parts = append(parts, safeStatusText(fmt.Sprintf("%d%%", bat)))
	}
	// The chip closes the cluster, so it sits in the far corner next to the
	// bell: the one place a "what am I listening to?" glance expects it.
	if chip, ok := statusMediaPart(); ok {
		parts = append(parts, chip)
	}
	// The controls the user hid in Edit Home → Top bar stay off every bar
	// (header_arrange.go). The order is applied where the bar is laid out
	// (headerPlan): trimming a crowded bar still reads this canonical order.
	return headerPartsVisible(config, parts)
}

// headerStatusFont returns the small 16px Inter font both headers use for the
// right-side status cluster, so it renders at the same size everywhere.
func headerStatusFont() *ttf.Font {
	return loadHomeFonts().Small
}

// --- Footer rendering (controller hints) --

// footerHints returns the controller hints for a scene. Every scene gets
// navigate + open + back; scenes with extra bindings add them. It is separate
// from the drawing code so the audit tests can check each label against what
// the input handlers really do.
func footerHints(config *Config, scene SceneConfig) []FooterHint {
	// The Terminal, Scheduled tasks and the system tool screens describe their
	// own state-dependent bindings (systool.go).
	if hints, ok := sysToolFooterHints(scene.Name); ok {
		return hints
	}
	// Watch Party, Toolbox, Audit toolkit, Security & trust and Report a bug
	// follow their focused control and open sheet (tbxui.go).
	if hints, ok := tbxFooterHints(config, scene.Name); ok {
		return hints
	}
	// Settings and Plugins name what the focused row or button does
	// (setui.go, plugui.go).
	if hints, ok := setFooterHints(scene); ok {
		return hints
	}
	// Control profiles and Local play follow their focus, their sheet and
	// their view (inmap_ui.go, hotseat_ui.go).
	if hints, ok := inmapScenesFooterHints(scene.Name); ok {
		return hints
	}
	// Experience packs, Config and Backup & restore follow their focus, their
	// tab and their open sheet (xpackroute.go).
	if hints, ok := xpackRouteFooterHints(scene); ok {
		return hints
	}
	// The File Explorer's B binding climbs the directory tree, so it is
	// advertised as "Up" inside a folder, "Locations" at a location's root and
	// "Back" on the Locations list (filesBackLabel reads the cached listing,
	// not the card).
	backLabel := "Back"
	if scene.Name == "FileExplorer" {
		backLabel = filesBackLabel(config)
		if chatPickActive() {
			// Picking for the chat, B calls the pick off (chatPickBack).
			backLabel = "Cancel"
		}
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Navigate"},
		{Button: "A", Label: "Open"},
		{Button: "B", Label: backLabel},
	}
	switch scene.Name {
	case "FileExplorer":
		// The screen is a file manager: A opens, Y is the action sheet, X marks
		// a row and B climbs a folder. While the sheet or the remote browser
		// owns the pad the footer follows it, so the labels never describe a
		// screen the user cannot see.
		if feSheet.open {
			hints = []FooterHint{
				{Button: "D-Pad", Label: "Choose"},
				{Button: "A", Label: "Run"},
				{Button: "B", Label: "Close"},
			}
			if feArchiveSheetBusy() {
				// B leaves the extraction running (files_extract_ui.go).
				hints[2].Label = "Hide"
			}
		} else if feRemotePanel.open {
			hints = []FooterHint{
				{Button: "A", Label: "Download"},
				{Button: "Y", Label: "Upload here"},
				{Button: "X", Label: "Address"},
				{Button: "B", Label: "Up"},
			}
		} else if filesNav.atLocations {
			hints = append(hints, FooterHint{Button: "Y", Label: "Options"})
		} else {
			// Picking for the chat, A on a file attaches it (chatfiles.go).
			if entry, ok := feFocusEntry(config); ok && chatPickActive() && !entry.IsDir {
				hints[1].Label = "Attach"
			} else if ok && feArchiveOpensSheet(entry) {
				// A on an archive opens its sheet (files_extract_ui.go).
				hints[1].Label = "Extract…"
			}
			hints = append(hints,
				FooterHint{Button: "Y", Label: "Actions"},
				FooterHint{Button: "X", Label: "Mark"},
				FooterHint{Button: "L1/R1", Label: "Page"},
			)
		}
	case "JukaLand":
		// The game names its own controls, for the pad or the keyboard,
		// whichever was used last (jland_input.go).
		hints = jlandSceneHints()
	case "Browser":
		// The offline card and JukaLand played on it name their own keys
		// while they own the page area (brw_offline.go).
		if own, ok := brwFooterHints(); ok {
			hints = own
			break
		}
		// The browser has its own D-pad model: links on the page, the address
		// bar on A, the search keyboard on Y and the full link list on X. The
		// triggers zoom the reader text — the same action the toolbar chip
		// steps — so both ways in are named here.
		hints[0] = FooterHint{Button: "Up/Down", Label: "Scroll"}
		hints[1] = FooterHint{Button: "A", Label: "Link / URL"}
		// The keyboard's halves (handleBrowserKey): - and + zoom, / searches.
		// Select leads the extras: the menu holds everything else, and the
		// full-screen page draws no footer, so this row is what the controls
		// card (F1) lists for it (brw_menu.go).
		hints = append(hints,
			FooterHint{Button: "Select / M", Label: "Menu"},
			FooterHint{Button: "Left/Right", Label: "Previous / next link"},
			FooterHint{Button: "L1/R1", Label: "Scroll"},
			FooterHint{Button: "L2/R2 / - / +", Label: "Zoom"},
			FooterHint{Button: "Y / /", Label: "Search"},
			FooterHint{Button: "X", Label: "Links"},
		)
	case "Chat":
		// The chat's row follows its state: browsing, a focused message, the
		// action bar or inline typing (chatinput.go).
		hints = chatFooterHints()
	case "Tube":
		// The Media screen's row follows what has the focus: the browser, the
		// Downloads list, the player, or its details sheet (media_hub.go).
		hints = mediaHubFooterHints(hints)
	case "Favorites":
		// X removes the focused row, Y takes the last removal back
		// (favorites.go).
		hints = favoritesFooterHints(hints)
	case "Apps":
		// The launcher is a tile grid: A starts the focused app.
		hints[1] = FooterHint{Button: "A", Label: "Launch"}
	case "Packages":
		// The store names the keys of whatever holds the pad: the details
		// sheet, the filter chips or the shelves (pkginput.go).
		hints = pkgFooterHints()
	case "Patch":
		// Patch is modal-aware and lists the keys the open dialog takes.
		if patchHints, ok := patchFooterHints(); ok {
			hints = patchHints
		}
	case "Notes":
		// The list takes the four action keys the handler really has; while a
		// note is open, typing owns the pad and only B does anything.
		if notes.tab == 0 {
			hints[1] = FooterHint{Button: "A", Label: "Edit"}
			hints = append(hints,
				FooterHint{Button: "Y", Label: "New"},
				FooterHint{Button: "X", Label: "Delete"},
				// "/ F": the keyboard cycles folders with F (inputhints.go
				// shows whichever half the user can press).
				FooterHint{Button: "L1/R1 / F", Label: "Folder"},
			)
		} else if notes.tab == 3 {
			// Search is a draft, not an edit: B leaves the field.
			hints = []FooterHint{{Button: "B", Label: "Done"}}
		} else {
			hints = []FooterHint{{Button: "B", Label: "Save & Back"}}
		}
	case "Calculator":
		// The pad walks the key grid; X inserts the graph variable and Y
		// toggles the graph view. The shoulders are Clear and Backspace.
		hints[1] = FooterHint{Button: "A", Label: "Select"}
		hints = append(hints,
			FooterHint{Button: "X", Label: "Insert x"},
			FooterHint{Button: "Y", Label: "Graph"},
			// The keyboard's twins: C clears, Backspace deletes (calculator.go).
			FooterHint{Button: "L1 / C", Label: "Clear"},
			FooterHint{Button: "R1 / Backspace", Label: "Delete"},
		)
	case "PDFReader":
		// The reader draws its own footer with these (pdfreader.go): pages
		// or chapters on the shoulders, zoom on the triggers, fit, text view.
		hints = pdfvFooterHints()
	case "Alarm":
		// The list and the editor take different keys, so the footer follows the
		// open tab instead of advertising "Open" while a time is being set.
		if alarmUI.pickOpen {
			// The music picker sheet owns the pad while it is open.
			hints = []FooterHint{{"D-Pad", "Move"}, {"L1/R1", "Page"}, {"A", "Choose"}, {"B", "Back"}}
		} else if alarmUI.tab == alarmTabList {
			hints[1] = FooterHint{Button: "A", Label: "Edit"}
			hints = append(hints,
				FooterHint{Button: "X", Label: "Toggle"},
				FooterHint{Button: "Y", Label: "Add"},
				// "/ Del": the keyboard deletes with Delete (handleAlarmInput).
				FooterHint{Button: "L1 / Del", Label: "Delete"},
			)
		} else {
			// The editor names the focused row's keys (alarmedit.go).
			hints = alarmEditFooterHints()
		}
	case synthSceneName:
		// The keys follow the Synth's page and its cursor (synth.go).
		hints = synthFooterHints()
	case "Pomodoro":
		// A starts and pauses, X skips the phase, Y resets the cycle.
		hints[1] = FooterHint{Button: "A", Label: "Start / Pause"}
		hints = append(hints,
			FooterHint{Button: "X", Label: "Skip"},
			FooterHint{Button: "Y", Label: "Reset"},
		)
	case toolsHubSceneName, netToolsSceneName, wifiScrSceneName:
		// The tools pages follow their focus and their open sheet, the way
		// Patch follows its dialog.
		hints = toolsScenesFooterHints(scene.Name, hints)
	case playtimeSceneName:
		// The trophy room's keys follow its tab, its focus and its sheet.
		hints = trophyRoomFooterHints()
	case rommSceneName:
		// The ROM manager names what A does on the focused control, and its
		// sheet's keys while one is up (romm_ui.go).
		hints = rommFooterHints(config)
	case apkStoreSceneName:
		// The APK Store follows its focus and its open sheet (apkstore.go).
		hints = apkStoreFooterHints(config)
	case voiceSceneName:
		// Voice command's keys follow what it is doing (voicecmd.go).
		hints = voiceFooterHints(config)
	case vscSceneName:
		// Voice shortcuts follows its step and its focus (voiceshortcuts_ui.go).
		hints = vscFooterHints(config)
	}
	return hints
}

// footerBarHeight is the shared footer's height. Every screen that draws its
// own body reserves this band at the bottom for the same reason it reserves
// headerBarHeight() at the top: the bar is chrome, not something a body draws
// over.
func footerBarHeight() int32 {
	if screenHeight > 0 && screenHeight < 600 {
		return 42
	}
	return HomeFooterH
}

// hintLine formats the controller hints drawn inside overlay panels. One style
// everywhere: a button, one space, a Title Case label, and three spaces between
// groups ("D-Pad Navigate   A Open   B Back"). Scenes use the badge footer
// instead; without this, the same button appeared as "A: open", "A open" and
// "A: Open" on different screens.
func hintLine(hints ...FooterHint) string {
	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		// The button in this frame's vocabulary: keyboard keys on a PC
		// without a pad or with a keyboard on the handheld (inputhints.go).
		button := strings.TrimSpace(hintKeyFor(hint.Button, hint.Label))
		// The label in the interface language (i18n.go); the joined line is
		// no longer a catalogue key, so it is translated here.
		label := strings.TrimSpace(uiT(hint.Label))
		switch {
		case button == "" && label == "":
			continue
		case button == "":
			parts = append(parts, label)
		case label == "":
			parts = append(parts, button)
		default:
			parts = append(parts, button+" "+label)
		}
	}
	return strings.Join(parts, "   ")
}

// renderFooter displays controller navigation hints pinned to the bottom of the viewport.
// It shows only the bindings actually supported by the current input code.
func renderFooter(renderer *sdl.Renderer, config *Config) {
	// The home scene owns its footer inside HomeLayout; this shared footer
	// serves every non-home scene as the single bottom hint bar.
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return
	}
	scene := config.Scenes[currentSceneIndex]
	if scene.Layout == "home" {
		return
	}

	// Home's footer: a band inset by the frame margin, rounded at the top and
	// flush with the bottom edge, with the controller hints right-aligned as
	// glass badges. It used to be a full-width opaque slate strip with the
	// hints centred, which is the first thing that told a user they had left
	// Home.
	band := sceneFooterBand()
	drawGlassFooterBand(renderer, band)

	// planFooterHints measures the row and re-lays it out until it fits,
	// because on the Brick's 960-wide canvas the full Tube row used to run off
	// both edges ("d Navigate" … "R2").
	pad := scaleX(16, screenWidth)
	hints := footerHints(config, scene)
	if scene.Name != voiceSceneName {
		if voice, ok := voiceOrbFooterHint(config); ok {
			hints = append([]FooterHint{voice}, hints...)
		}
	}
	plan := cachedFooterPlan(config, hints, band.W-2*pad)
	if plan.font == nil {
		return
	}
	x := band.X + band.W - pad - plan.width
	textY := band.Y + (band.H-plan.fontHeight)/2
	badgeY := band.Y + (band.H-plan.badgeH)/2
	for i, g := range plan.hints {
		labelX := x
		if plan.badgeW[i] > 0 {
			badge := sdl.Rect{X: x, Y: badgeY, W: plan.badgeW[i], H: plan.badgeH}
			labelX = badge.X + badge.W + plan.badgeGap
			drawGlassHintBadge(renderer, config, plan.font, badge, g.Button, g.Label, labelX, textY)
		} else {
			drawGlassText(renderer, config, plan.font, g.Label, ColorTextMuted(), labelX, textY)
		}
		x = labelX + plan.labelW[i] + plan.groupGap
	}
}

// footerHintSize is the hint row's type size in design pixels: Home's footer
// size. The row used to start at the theme's "small" role, which is 24px on a
// config that does not set it — half again the size of the same hints on Home.
const footerHintSize = 16

// footerPlan is one hint row laid out for a specific width: the font and
// spacing that fit, the hints that survived, and every measured badge so
// drawing never has to remeasure what planning already measured.
//
// The three navigation hints come first, so hints are dropped from the end: the
// player must always keep navigate/open/back.
type footerPlan struct {
	font       *ttf.Font
	hints      []FooterHint
	badgeW     []int32
	labelW     []int32
	width      int32
	fontHeight int32
	badgeH     int32
	groupGap   int32
	badgePad   int32
	badgeGap   int32
}

// The footer is drawn every frame, but its hints usually stay unchanged for
// many frames. Keep the measured layout until the available width, font or
// button vocabulary changes. This saves repeated font measurements across
// every non-Home scene while still following contextual hint changes.
var footerLayoutCache struct {
	valid    bool
	font     *ttf.Font
	avail    int32
	screenW  int32
	screenH  int32
	vocab    hintVocab
	remap    *hintPadRemap
	language string
	hints    []FooterHint
	plan     footerPlan
}

func sameFooterHints(a, b []FooterHint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cachedFooterPlan(config *Config, hints []FooterHint, avail int32) footerPlan {
	font := getFontSized(config, "small", footerHintSize)
	vocab, remap, language := hintVocabNow(), hintRemapNow.Load(), uiLanguageCode()
	c := &footerLayoutCache
	if font != nil && c.valid && c.font == font && c.avail == avail &&
		c.screenW == screenWidth && c.screenH == screenHeight &&
		c.vocab == vocab && c.remap == remap && c.language == language &&
		sameFooterHints(c.hints, hints) {
		return c.plan
	}
	plan := planFooterHints(config, hints, avail)
	if font == nil || plan.font == nil {
		c.valid = false
		return plan
	}
	// Both the key and the returned plan own their slices. Callers may reuse or
	// edit the hints they assembled for this frame.
	plan.hints = append([]FooterHint(nil), plan.hints...)
	c.hints = append(c.hints[:0], hints...)
	c.font, c.avail = font, avail
	c.screenW, c.screenH = screenWidth, screenHeight
	c.vocab, c.remap, c.language = vocab, remap, language
	c.plan, c.valid = plan, true
	return plan
}

// planFooterHints picks the largest type and the tightest spacing that keep the
// hint row inside avail pixels, dropping trailing hints only if even the
// smallest type does not fit.
func planFooterHints(config *Config, hints []FooterHint, avail int32) footerPlan {
	// Home's spacing first (18px between groups, a 6px pad either side of the
	// button name, 7px to the label), then a pixel smaller and tighter at each
	// step. Every candidate is opened at its design size through getFontSized,
	// which applies the canvas and text-size scaling exactly once; the old
	// planner scaled the role's already-scaled size a second time on the Brick.
	spacings := []struct {
		size     int
		groupGap int32
		badgePad int32
		badgeGap int32
	}{
		{footerHintSize, 18, 6, 7},
		{footerHintSize - 1, 16, 6, 6},
		{footerHintSize - 2, 14, 5, 5},
		{footerHintSize - 3, 12, 5, 4},
		{footerHintSize - 4, 10, 4, 4},
	}

	plan := footerPlan{hints: hints, groupGap: spacings[0].groupGap, badgePad: spacings[0].badgePad, badgeGap: spacings[0].badgeGap}
	for _, sp := range spacings {
		candidate := getFontSized(config, "small", sp.size)
		if candidate == nil {
			continue
		}
		plan.font = candidate
		plan.groupGap, plan.badgePad, plan.badgeGap = sp.groupGap, sp.badgePad, sp.badgeGap
		plan.badgeH = footerBadgeHeight(candidate)
		if footerHintsWidth(candidate, hints, sp.groupGap, sp.badgePad, sp.badgeGap, plan.badgeH) <= avail {
			break
		}
	}
	if plan.font == nil {
		plan.font, _ = getCachedFont(config, "small")
		if plan.font == nil {
			plan.hints = nil
			return plan
		}
		plan.badgeH = footerBadgeHeight(plan.font)
	}

	// Still too wide for even the smallest type: drop hints from the end.
	keep := len(plan.hints)
	for keep > 1 && footerHintsWidth(plan.font, plan.hints[:keep], plan.groupGap, plan.badgePad, plan.badgeGap, plan.badgeH) > avail {
		keep--
	}
	plan.hints = plan.hints[:keep]

	plan.fontHeight = int32(plan.font.Height())
	plan.badgeW = make([]int32, len(plan.hints))
	plan.labelW = make([]int32, len(plan.hints))
	for i, g := range plan.hints {
		// Measured as drawn: drawGlassHintBadge names the button in this
		// frame's vocabulary. plan.hints keeps the pad names, which the click
		// targets (emuHintPress) match on.
		plan.badgeW[i] = footerBadgeWidth(plan.font, hintKeyFor(g.Button, g.Label), plan.badgePad, plan.badgeH)
		plan.labelW[i], _ = measureText(plan.font, g.Label)
	}
	plan.width = footerHintsWidth(plan.font, plan.hints, plan.groupGap, plan.badgePad, plan.badgeGap, plan.badgeH)
	return plan
}

// footerBadgeHeight is a hint badge's height for a font: Home's 22px pill, or
// taller when the text size setting makes the type taller than that.
func footerBadgeHeight(font *ttf.Font) int32 {
	h := scaleY(22, screenHeight)
	if font != nil {
		if fh := int32(font.Height()) + 4; fh > h {
			h = fh
		}
	}
	return h
}

// footerBadgeWidth is the pill around one button name: the text plus its pad,
// never narrower than it is tall, so a single-letter button ("A", "B") is a
// round badge the way Home draws it. A hint with no button has no badge.
func footerBadgeWidth(font *ttf.Font, button string, badgePad, badgeH int32) int32 {
	if button == "" {
		return 0
	}
	bw, _ := measureText(font, button)
	w := bw + badgePad*2
	if w < badgeH {
		w = badgeH
	}
	return w
}

// footerHintsWidth measures one hint row laid out with the given font and
// spacing, so the planner can compare it with the panel width before drawing.
func footerHintsWidth(font *ttf.Font, hints []FooterHint, groupGap, badgePad, badgeGap, badgeH int32) int32 {
	if font == nil {
		return 0
	}
	total := int32(0)
	for _, g := range hints {
		// Cached widths: this runs several times per frame (once per type size
		// the planner tries), and the hints are the same strings every time.
		lw, _ := measureText(font, g.Label)
		total += lw
		if bw := footerBadgeWidth(font, hintKeyFor(g.Button, g.Label), badgePad, badgeH); bw > 0 {
			total += bw + badgeGap
		}
	}
	if len(hints) > 1 {
		total += groupGap * int32(len(hints)-1)
	}
	return total
}

// volatileCustomVars lists runtime-only keys that should not be persisted to
// disk. They are reconstructed each session rather than saved.
var volatileCustomVars = map[string]bool{
	"search_results": true,
	"app_entries":    true,
	"fe_entries":     true,
	"fe_path":        true,
	"stream_url":     true,
	"search_query":   true,
	"search_error":   true,
	"netspeed_text":  true,
	// What was typed into a web form (a password included) and the page text
	// itself: both went into jukauser.json on every save, and Settings → Save
	// put them in jukaconfig.json, which the live builder serves over Wi-Fi.
	"browser_form_value": true,
	"browser_content":    true,
}

// userConfigCache holds the loaded jukauser.json (user settings + favorites +
// chat history + jukaland save) so every save preserves all sections. The app
// persists exactly two JSON files: jukaconfig.json (layout/design, editable
// via the web editor) and jukauser.json (recently played, user settings,
// favorites, chat, etc.).
var userConfigCache *UserConfig

type UserConfig struct {
	Variables UserVariables   `json:"variables"`
	Favorites *FavoritesStore `json:"favorites,omitempty"`
	Chat      *ChatSection    `json:"chat,omitempty"`
	JukaLand  *JukaLandState  `json:"jukaland,omitempty"`
}

type UserVariables struct {
	ButtonColor        RGB    `json:"buttonColor"`
	LabelColor         RGB    `json:"labelColor"`
	InputColor         RGB    `json:"inputColor"`
	Fullscreen         bool   `json:"fullscreen"`
	FileExplorerRoot   string `json:"fileExplorerRoot"`
	WeatherEnabled     bool   `json:"weatherEnabled"`
	WeatherUnit        string `json:"weatherUnit"`
	TSPUsername        string `json:"tspUsername"`
	PlaybackResolution string `json:"playbackResolution"`
	SubtitleLanguage   string `json:"subtitleLanguage,omitempty"`
	ReducedMotion      bool   `json:"reducedMotion"`
	LowPower           bool   `json:"lowPower"`
	PowerMode          string `json:"powerMode"`
	LedSync            bool   `json:"ledSync"`
	GridColumns        int    `json:"gridColumns"`
	SearchWidth        int    `json:"searchWidth"`
	DynamicBg          bool   `json:"dynamicBg"`
	ScreensaverTimeout int    `json:"screensaverTimeout"`
	Clock12            bool   `json:"clock12"`
	// NetPlayEnabled is a pointer in the user file so an upgrade from a build
	// that predates the switch keeps the shipped default instead of reading a
	// missing key as "off".
	NetPlayEnabled *bool `json:"netPlayEnabled,omitempty"`

	// Quick Settings levels. The *Set flags separate "never touched" from a
	// real 0 (muted / dimmed), so a fresh install never overrides the device's
	// own mixer or backlight on first launch.
	Volume        int  `json:"volume"`
	VolumeSet     bool `json:"volumeSet"`
	Brightness    int  `json:"brightness"`
	BrightnessSet bool `json:"brightnessSet"`

	Custom map[string]interface{}
}

// userConfigRecoveryNotice is set when jukauser.json was unusable and the last
// backup had to be loaded. It is shown once the render loop is up (main.go)
// rather than during load, which runs before fonts and audio exist.
var userConfigRecoveryNotice string

// takeUserConfigRecoveryNotice returns the pending recovery notice once.
func takeUserConfigRecoveryNotice() string {
	notice := userConfigRecoveryNotice
	userConfigRecoveryNotice = ""
	return notice
}

// readUserConfigFile decodes one user-config file. A missing file reports the
// os error so the caller can distinguish a first run from a damaged file.
func readUserConfigFile(path string) (*UserConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var uc UserConfig
	if err := json.Unmarshal(data, &uc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &uc, nil
}

func loadUserConfig() *UserConfig {
	if userConfigCache != nil {
		return userConfigCache
	}
	uc, err := readUserConfigFile("jukauser.json")
	recovered := false
	if err != nil {
		// AtomicWrite keeps the previous file as .bak. Recover from it instead of
		// silently starting from defaults, which used to drop the theme, keymap
		// and every other preference over one bad write.
		if backup, backupErr := readUserConfigFile("jukauser.json.bak"); backupErr == nil {
			logWarn("config", "jukauser.json is unusable (%v); recovered settings from jukauser.json.bak", err)
			uc, err, recovered = backup, nil, true
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			logWarn("config", "jukauser.json not found; starting with defaults")
		} else {
			logWarn("config", "cannot read jukauser.json: %v; starting with defaults", err)
		}
	}
	if uc == nil {
		uc = &UserConfig{}
	}
	if uc.Variables.Custom == nil {
		uc.Variables.Custom = make(map[string]interface{})
	}
	userConfigCache = uc
	if recovered {
		// Put the recovered settings back at the primary path so the next launch
		// is clean, and tell the user once the UI can draw.
		userConfigRecoveryNotice = "Settings were damaged and have been restored from the last backup. Check anything you changed recently."
		saveUserConfig(userConfigCache)
	}
	// One-time migration: fold the legacy per-feature JSON files into their
	// jukauser.json sections, then delete them so only the two canonical JSON
	// files remain on disk.
	migrateLegacyUserFiles()
	// Older builds wrote their implicit Celsius default into jukauser.json.
	// Clear that legacy "C" once so the country-based default can take over;
	// the marker keeps a later explicit choice intact.
	if migrateWeatherUnitAuto(userConfigCache) {
		saveUserConfig(userConfigCache)
	}
	// The handheld face-button fix is persisted once so the setting the user
	// sees matches how the buttons actually behave.
	if migrateHandheldButtonMapping(userConfigCache, P().IsHandheld()) {
		saveUserConfig(userConfigCache)
	}
	return userConfigCache
}

// abSwapMigrationKey marks that the one-time handheld A/B default was applied.
const abSwapMigrationKey = "_abSwapLayoutApplied"

// migrateHandheldButtonMapping fixes the saved A/B setting on first run after
// the handheld mapping fix. Older builds shipped swapABButtons=false, but
// TrimUI firmware reports physical A as SDL B (and vice versa), so untouched
// installations had A going Back and B selecting. The marker keeps later user
// choices (including deliberately turning the swap off) intact.
func migrateHandheldButtonMapping(user *UserConfig, handheld bool) bool {
	if user == nil || !handheld {
		return false
	}
	if user.Variables.Custom == nil {
		user.Variables.Custom = make(map[string]interface{})
	}
	if _, done := user.Variables.Custom[abSwapMigrationKey]; done {
		return false
	}
	user.Variables.Custom["swapABButtons"] = true
	user.Variables.Custom[abSwapMigrationKey] = true
	return true
}

// migrateLegacyUserFiles folds any still-present legacy JSON files
// (favorites.json, chat.json, jukaland.json) into jukauser.json sections and
// removes them. Kept for users upgrading from older builds; new installs never
// create these files.
func migrateLegacyUserFiles() {
	if userConfigCache == nil {
		return
	}
	if userConfigCache.Favorites == nil {
		if data, err := os.ReadFile("favorites.json"); err == nil {
			var fs FavoritesStore
			if json.Unmarshal(data, &fs) == nil {
				userConfigCache.Favorites = &fs
				_ = os.Remove("favorites.json")
			}
		}
	}
	if userConfigCache.Chat == nil {
		if data, err := os.ReadFile("chat.json"); err == nil {
			var w struct {
				Messages []ChatMessage `json:"messages"`
			}
			if json.Unmarshal(data, &w) == nil {
				userConfigCache.Chat = &ChatSection{Messages: w.Messages}
				_ = os.Remove("chat.json")
			}
		}
	}
	if userConfigCache.JukaLand == nil {
		if data, err := os.ReadFile("jukaland.json"); err == nil {
			var js JukaLandState
			if json.Unmarshal(data, &js) == nil {
				userConfigCache.JukaLand = &js
				_ = os.Remove("jukaland.json")
			}
		}
	}
}

// migrateWeatherUnitAuto clears the old implicit Celsius default once so the
// country-based default can take over, and records that it ran so a later
// explicit choice is never rewritten. It reports whether anything changed.
func migrateWeatherUnitAuto(user *UserConfig) bool {
	if user == nil {
		return false
	}
	if user.Variables.Custom == nil {
		user.Variables.Custom = make(map[string]interface{})
	}
	if _, migrated := user.Variables.Custom["weatherUnitAuto"]; migrated {
		return false
	}
	user.Variables.Custom["weatherUnitAuto"] = true
	changed := false
	if strings.EqualFold(strings.TrimSpace(user.Variables.WeatherUnit), "C") {
		user.Variables.WeatherUnit = ""
		changed = true
	}
	for key, value := range user.Variables.Custom {
		if !strings.EqualFold(key, "weatherUnit") {
			continue
		}
		if s, ok := value.(string); ok && strings.EqualFold(strings.TrimSpace(s), "C") {
			delete(user.Variables.Custom, key)
			changed = true
		}
	}
	return changed
}

// saveUserConfig writes user now. It takes the debounced writer's lock, so the
// two cannot interleave, and drops a queued save it supersedes instead of
// letting that land after it with older data: a full save is built from the
// live config this instant and fills its sections from the cache, and a
// section save writes the cache's sections as they are now. A full save queued
// before a section save stays: its settings are newer than any on disk.
func saveUserConfig(user *UserConfig) {
	if user == nil || !userConfigWritesEnabled {
		return
	}
	sections := user == userConfigCache
	userConfigSaveMutex.Lock()
	defer userConfigSaveMutex.Unlock()
	if pendingUserConfig != nil && (!sections || pendingUserConfigSections) {
		pendingUserConfig = nil
		pendingUserConfigDir = ""
	}
	saveUserConfigFile("", user, sections)
}

// saveUserConfigToDir persists jukauser.json into dir (empty means the current
// working directory). The debounced writer uses it so a pending save cannot
// land in whatever directory happens to be current two seconds later.
func saveUserConfigToDir(dir string, user *UserConfig) {
	saveUserConfigFile(dir, user, false)
}

// saveUserConfigFile writes user into dir's jukauser.json. sections marks a
// section save: the cached user config — or the debounced writer's copy of it
// — handed in for its favourites, chat or JukaLand. The cache's Variables are
// the boot copy, so the newest full save's (userConfigLatest) are written in
// their place; before the run's first full save the cache's are the newest
// there are. A full save's Variables become the newest once they are on disk.
func saveUserConfigFile(dir string, user *UserConfig, sections bool) {
	if user == nil || !userConfigWritesEnabled {
		return
	}
	sections = sections || user == userConfigCache
	path := "jukauser.json"
	if dir != "" {
		path = filepath.Join(dir, "jukauser.json")
	}
	vars := user.Variables
	if sections {
		if latest := userConfigLatestVars(); latest != nil {
			vars = *latest
		}
	}
	userCopy := *user
	userCopy.Variables = vars
	userCopy.Variables.Custom = make(map[string]interface{}, len(vars.Custom))
	for k, v := range vars.Custom {
		if volatileCustomVars[k] {
			continue
		}
		userCopy.Variables.Custom[k] = v
	}
	// A section save writes the sections as they are now: a queued one holds
	// those it was queued with, and a newer one may have been written since.
	if sections && userConfigCache != nil {
		userCopy.Favorites, userCopy.Chat, userCopy.JukaLand = userConfigCache.Favorites, userConfigCache.Chat, userConfigCache.JukaLand
	}
	// Preserve sections (favorites / chat / jukaland) the caller didn't
	// supply, so a settings-only save never drops user data.
	if userConfigCache != nil {
		if userCopy.Favorites == nil {
			userCopy.Favorites = userConfigCache.Favorites
		}
		if userCopy.Chat == nil {
			userCopy.Chat = userConfigCache.Chat
		}
		if userCopy.JukaLand == nil {
			userCopy.JukaLand = userConfigCache.JukaLand
		}
	}
	// Keep the in-memory cache in sync with what we just persisted.
	if userConfigCache != nil {
		userConfigCache.Favorites = userCopy.Favorites
		userConfigCache.Chat = userCopy.Chat
		userConfigCache.JukaLand = userCopy.JukaLand
	}
	data, err := json.MarshalIndent(userCopy, "", "  ")
	if err != nil {
		logWarn("extras", "saveUserConfig: marshal error: %v", err)
		return
	}
	// Keep a short rollback history next to the config. The helper skips an
	// identical newest snapshot, so debounced saves do not grow the folder.
	if powerConfigHistoryEnabled {
		if err := snapshotPowerConfig(path); err != nil {
			logWarn("extras", "saveUserConfig: history snapshot: %v", err)
		}
	}
	if err := AtomicWrite(path, data, 0644); err != nil {
		logWarn("extras", "saveUserConfig: write error: %v", err)
		return
	}
	if !sections {
		userConfigNoteLatest(data)
	}
	LogScene("config").Info("user config saved", "path", path)
}

// userConfigNoteLatest keeps the Variables of a full save just written as the
// newest (userConfigLatest), decoded afresh from the bytes on disk so a section
// save on any goroutine can marshal them while the live maps change.
func userConfigNoteLatest(data []byte) {
	var written struct {
		Variables UserVariables `json:"variables"`
	}
	if err := json.Unmarshal(data, &written); err != nil {
		return
	}
	userConfigLatest.mu.Lock()
	userConfigLatest.vars = &written.Variables
	userConfigLatest.mu.Unlock()
}

func mergeUserConfig(config *Config, user *UserConfig) {
	if config == nil {
		return
	}
	if user == nil {
		// Even with no user file, empty colour slots must inherit the theme
		// instead of rendering as black.
		ensureThemeColors(config)
		return
	}
	config.Variables.ButtonColor = user.Variables.ButtonColor
	config.Variables.LabelColor = user.Variables.LabelColor
	config.Variables.InputColor = user.Variables.InputColor
	config.Variables.Fullscreen = user.Variables.Fullscreen
	config.Variables.FileExplorerRoot = user.Variables.FileExplorerRoot
	config.Variables.WeatherEnabled = user.Variables.WeatherEnabled
	config.Variables.WeatherUnit = user.Variables.WeatherUnit
	config.Variables.TSPUsername = user.Variables.TSPUsername
	config.Variables.PlaybackResolution = user.Variables.PlaybackResolution
	if user.Variables.SubtitleLanguage != "" {
		config.Variables.SubtitleLanguage = user.Variables.SubtitleLanguage
	}
	config.Variables.ReducedMotion = user.Variables.ReducedMotion
	config.Variables.LowPower = user.Variables.LowPower
	// Power Mode was saved by Quick Settings but never read back, so every
	// launch came up in Default (and the next save wrote that over the choice).
	config.Variables.PowerMode = normalizePowerMode(user.Variables.PowerMode)
	config.Variables.LEDSync = user.Variables.LedSync
	config.Variables.Clock12 = user.Variables.Clock12
	if user.Variables.NetPlayEnabled != nil {
		config.Variables.NetPlayEnabled = *user.Variables.NetPlayEnabled
	}
	// A zero timeout means "not saved" (old user files), not "never sleep".
	if user.Variables.ScreensaverTimeout > 0 {
		config.Variables.ScreensaverTimeout = user.Variables.ScreensaverTimeout
	}
	// 300 was the old shipped default, so an untouched user file still says
	// "5 minutes". Move it to the new 25 s default once and remember that the
	// value is the user's own choice from then on.
	if config.Variables.ScreensaverTimeout == 300 {
		if _, migrated := customValue(config.Variables.Custom, "screensaverTimeoutMigrated"); !migrated {
			config.Variables.ScreensaverTimeout = 25
			if config.Variables.Custom == nil {
				config.Variables.Custom = map[string]interface{}{}
			}
			config.Variables.Custom["screensaverTimeoutMigrated"] = true
		}
	}
	if config.Variables.Custom == nil && len(user.Variables.Custom) > 0 {
		config.Variables.Custom = make(map[string]interface{}, len(user.Variables.Custom))
	}
	for k, v := range user.Variables.Custom {
		if !volatileCustomVars[k] {
			config.Variables.Custom[k] = v
		}
	}
	ensureThemeColors(config)
	// The interface language the user chose (i18n.go).
	uiLanguageApply(config)
}

// --- Persist design-only config to jukaconfig.json ---

func saveConfig(config *Config) {
	// The Tools page lays its buttons out as tiles in memory; put the authored
	// geometry back before anything is marshalled and re-apply it afterwards, so
	// a save never writes the generated layout into jukaconfig.json.
	defer menuTilesDetachForSave(config)()
	saved := *config
	saved.SchemaVersion = ConfigSchemaVersion
	saved.Variables = config.Variables
	saved.Variables.Custom = make(map[string]interface{})
	for k, v := range config.Variables.Custom {
		if volatileCustomVars[k] {
			continue
		}
		saved.Variables.Custom[k] = v
	}
	saved.Variables.ButtonColor = RGB{}
	saved.Variables.LabelColor = RGB{}
	saved.Variables.InputColor = RGB{}
	saved.Variables.Fullscreen = false
	saved.Variables.FileExplorerRoot = ""
	saved.Variables.WeatherEnabled = false
	saved.Variables.WeatherUnit = ""
	saved.Variables.TSPUsername = ""
	saved.Variables.PlaybackResolution = ""
	saved.Variables.SubtitleLanguage = ""

	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		logWarn("extras", "saveConfig: marshal error: %v", err)
		return
	}
	if err := AtomicWrite("jukaconfig.json", data, 0644); err != nil {
		logWarn("extras", "saveConfig: write error: %v", err)
		configHealth.RecordSave("jukaconfig.json", fmt.Errorf("write error: %w", err))
		return
	}
	configHealth.RecordSave("jukaconfig.json", nil)
	configHealth.MarkSaveOK()
	// The watcher must not treat this write as an outside edit and reload it.
	if globalConfigWatcher != nil {
		globalConfigWatcher.IgnoreWrite("jukaconfig.json")
	}
	LogScene("config").Info("design config saved")
}
