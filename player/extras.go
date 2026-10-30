package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

	// userConfigSaveDelay is the debounce window; a variable so tests can
	// exercise the timer without sleeping two seconds.
	userConfigSaveDelay = 2 * time.Second
)

// saveUserConfigDebounced batches rapid successive config changes into a single
// disk write after a short quiet period. This avoids excessive I/O on handheld
// devices when, for example, a user is typing in an input field.
// userConfigWritesEnabled gates every jukauser.json write. The test binary
// turns it off: tests load the shipped config and exercise settings paths, and
// a run used to rewrite jukauser.json and its .bak in the checkout. The tests
// that assert a real save turn it back on for their own scope.
var userConfigWritesEnabled = true

func saveUserConfigDebounced(user *UserConfig) {
	if !userConfigWritesEnabled {
		return
	}
	userConfigSaveMutex.Lock()
	pendingUserConfig = user
	pendingUserConfigDir, _ = os.Getwd()
	if userConfigSaveTimer != nil {
		userConfigSaveTimer.Stop()
	}
	userConfigSaveTimer = time.AfterFunc(userConfigSaveDelay, func() {
		userConfigSaveMutex.Lock()
		defer userConfigSaveMutex.Unlock()
		if pendingUserConfig != nil {
			saveUserConfigToDir(pendingUserConfigDir, pendingUserConfig)
			pendingUserConfig = nil
			pendingUserConfigDir = ""
		}
	})
	userConfigSaveMutex.Unlock()
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
		GridColumns:        config.Variables.GridColumns,
		SearchWidth:        config.Variables.SearchWidth,
		DynamicBg:          config.Variables.DynamicBg,
		ScreensaverTimeout: config.Variables.ScreensaverTimeout,
		SocialNotifs:       config.Variables.SocialNotifs,
		ReducedMotion:      config.Variables.ReducedMotion,
		LowPower:           config.Variables.LowPower,
		LedSync:            config.Variables.LEDSync,
		Custom:             config.Variables.Custom,
	}}
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
	// Put the last saved forecast on screen before the first network attempt:
	// an offline boot then shows weather instead of "unavailable".
	restoreWeatherCache(time.Now())
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		wake := subscribeForegroundWake()
		fetchWeatherOnce()
		for {
			// Ten minutes is a long time to sit out while a game owns the screen; a
			// wake when JukaHub is back refreshes the header immediately instead
			// of leaving yesterday's forecast up. See backgroundwork.go.
			if !waitForPoll(ticker.C, wake) {
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
}

// lookupLocation resolves the device's approximate position and country from a
// keyless IP service. ipapi.co rate-limits anonymous callers aggressively, so
// ipwho.is goes first and ipinfo.io is the last fallback; a failure here only
// costs the forecast its exact location and unit preference.
func lookupLocation(client *http.Client) (geoLocation, bool) {
	return lookupLocationContext(context.Background(), client)
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
	}
	if !getGeoJSON(ctx, client, "https://ipwho.is/", &v) || !v.Success {
		return geoLocation{}, false
	}
	if v.Latitude == 0 && v.Longitude == 0 {
		return geoLocation{}, false
	}
	return geoLocation{Latitude: v.Latitude, Longitude: v.Longitude, CountryCode: v.CountryCode}, true
}

func lookupIPAPICo(ctx context.Context, client *http.Client) (geoLocation, bool) {
	var v struct {
		Error       bool    `json:"error"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
		CountryCode string  `json:"country_code"`
	}
	if !getGeoJSON(ctx, client, "https://ipapi.co/json/", &v) || v.Error {
		return geoLocation{}, false
	}
	if v.Latitude == 0 && v.Longitude == 0 {
		return geoLocation{}, false
	}
	return geoLocation{Latitude: v.Latitude, Longitude: v.Longitude, CountryCode: v.CountryCode}, true
}

func lookupIPInfo(ctx context.Context, client *http.Client) (geoLocation, bool) {
	var v struct {
		Loc     string `json:"loc"`
		Country string `json:"country"`
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
	return geoLocation{Latitude: lat, Longitude: lon, CountryCode: v.Country}, true
}

func fetchWeatherOnce() {
	fetchWeatherOnceContext(context.Background())
}

// fetchWeatherOnceContext refreshes the forecast under a caller's context, so
// the weather screen's fetch stops when the screen is left. The 20-second bound
// still applies to the background ticker, which has no scene to cancel it.
func fetchWeatherOnceContext(parent context.Context) {
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
		log.Printf("[WARN] Geolocation unavailable, using default weather location")
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
		Daily struct {
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
	wxMutex.Lock()
	weatherDaily = days
	weatherReady = len(days) > 0
	wxMutex.Unlock()
	// Persist the forecast so the next boot has weather before the network is
	// up; a failed write is logged, never fatal.
	if len(days) > 0 {
		noteWeatherFetched(days, time.Now())
	}
	// Update dynamic background with weather data.
	if len(days) > 0 {
		DynBGUpdateWeather(days[0].Code)
	}
}

// sceneDisplayName maps internal scene identifiers to human-readable labels
// shown in the header. The UI never prints a raw config/enum identifier.
func sceneDisplayName(name string) string {
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
		return "Network"
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
		return "Tasks"
	case "Misc":
		return "Tools"
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
		return "Playtime"
	case "PDFReader":
		return "PDF Reader"
	default:
		return name
	}
}

// versionString returns the canonical "vX.Y.Z" value. The repository's single
// version source is config.Version (jukaconfig.json "Version", with a "0.4.0"
// fallback in config_health); the prefix is normalized so the UI never shows
// "vv0.4.0".
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

// versionDisplayString is the UI form of the version: the release build date
// follows the version when it is known (from release-info.json, the release
// build stamp, or the last applied update).
//
//	"v0.4.0 · 2026-10-17"
//
// Without a known build date it is exactly versionString.
func versionDisplayString(config *Config) string {
	return formatBuildLabel(versionString(config), installedBuildDate())
}

// headerTitle is the single header line: "JukaHub - v0.4.0" on Home and
// "JukaHub - v0.4.0 - <Scene>" on every other scene. The active scene appears
// exactly once per frame, here.
func headerTitle(config *Config, scene SceneConfig) string {
	base := "JukaHub - " + versionDisplayString(config)
	if scene.Layout == "home" {
		return base
	}
	return base + " - " + sceneDisplayName(scene.Name)
}

// --- Top status bar (brand + version + scene + clock/weather/battery) ---

func renderStatusBar(renderer *sdl.Renderer, config *Config) {
	sceneTitleBackRect = sdl.Rect{}
	statusClockRect = sdl.Rect{}
	statusWiFiRect = sdl.Rect{}
	weatherStatusRect = sdl.Rect{}
	statusMediaRect = sdl.Rect{}
	barH := headerBarHeight()
	// Opaque top bar surface.
	fillRoundedRect(renderer, 0, 0, screenWidth, barH, 0, ColorTopBar)
	// Subtle bottom border.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 40)
	renderer.FillRect(&sdl.Rect{X: 0, Y: barH - 1, W: screenWidth, H: 1})

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	scene := SceneConfig{}
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		scene = config.Scenes[currentSceneIndex]
	}
	sceneName := ""
	if scene.Layout != "home" && scene.Name != "" {
		sceneName = sceneDisplayName(scene.Name)
	}

	// One brand block, one caption type and one button height on every screen:
	// the bar decides those in header.go, so Home and a scene cannot drift into
	// two different headers again.
	headerTextW := drawHeaderBrand(renderer, config, barH,
		headerCaptionText(versionDisplayString(config), sceneName))
	nextX := headerTextW + SpaceMD

	// The Back control is the bar's loudest button: larger than the rest, in
	// the accent colour, which it swaps for a solid fill (and dark type) while
	// the D-pad holds it. Home is the root and has nowhere to go back to, and
	// JukaLand's B is a game action, so neither draws one.
	if scene.Layout != "home" && scene.Name != "" && scene.Name != "JukaLand" {
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

	// Right-side status cluster in the canonical shared order (network,
	// weather, time, battery) — identical to the home header so the right
	// side of the top bar stays put across screens. The notification bell
	// sits to the right of the cluster as the top-right system corner.
	parts := headerStatusParts(config)
	statusFont := headerStatusFont()
	if statusFont == nil {
		statusFont = font
	}
	bellW, bellH := notifBellSize(config)
	rightX := screenWidth - StatusBarMargin
	if bellW > 0 {
		// The offer is placed against the bell the same way Home places it:
		// one corner gap, not the wider spacing the cluster parts use, so the
		// pill sits against the bell on both headers instead of 6px apart on
		// Home and 16px apart in a scene.
		rightX -= bellW + headerCornerGap
	}
	// The release offer (only while an update is advertised) is the same pill
	// in the same corner as on Home: immediately left of the bell, inside the
	// system corner rather than floating in the status cluster.
	if pill := drawHeaderUpdatePill(renderer, config, rightX, barH,
		focusEngine.ElementIndex() == ChromeUpdateIndex); pill.W > 0 {
		updatePrompt.badge = pill
		rightX = pill.X - headerCornerGap
	}
	gap := StatusPartGap

	// Measure total width and drop least-important items if they don't fit
	// next to the brand/version/scene/back cluster. Battery and clock are what
	// the user needs at a glance, so the network name goes first.
	maxW := screenWidth - StatusBarMargin - StatusBarMargin - headerTextW - Space2XL
	if maxW < 120 {
		maxW = 120
	}
	parts = fitStatusParts(parts, func(p string) int32 {
		return statusClusterPartWidth(statusFont, p)
	}, gap, maxW)

	// Vertically center the cluster in the bar (same 16px font and position
	// as the home header).
	statusY := (barH - int32(statusFont.Height())) / 2
	if statusY < 0 {
		statusY = 0
	}
	clusterW := int32(0)
	for _, p := range parts {
		clusterW += statusClusterPartWidth(statusFont, p)
	}
	if len(parts) > 1 {
		clusterW += gap * int32(len(parts)-1)
	}
	startX := rightX - clusterW
	// Two parts are drawn as gauges rather than text: the leading network slot
	// (signal bars, empty without a link) and the battery (a colour-coded cell).
	// drawStatusClusterPart returns the same width statusClusterPartWidth
	// measured, so the clock and the bell never move.
	drawStatusText := func(text string, color sdl.Color, x, y int32) {
		renderText(renderer, config, statusFont, text, color, x, y)
	}
	for _, p := range parts {
		pw := statusClusterPartWidth(statusFont, p)
		drawStatusClusterPart(renderer, statusFont, p, startX, statusY, drawStatusText)
		if isClockStatusPart(p) {
			// Measure the clock so it can hold focus like any other button.
			statusClockRect = sdl.Rect{
				X: startX - 6, Y: statusY - 6,
				W: pw + 12, H: int32(statusFont.Height()) + 12,
			}
		}
		if isWeatherStatusPart(p) {
			// The hi/lo chip is the Local weather button.
			weatherStatusRect = sdl.Rect{
				X: startX - 6, Y: statusY - 6,
				W: pw + 12, H: int32(statusFont.Height()) + 12,
			}
		}
		if isWiFiStatusPart(p) {
			// The meter is the Wi-Fi button on every screen.
			statusWiFiRect = sdl.Rect{
				X: startX - 6, Y: statusY - 6,
				W: pw + 12, H: int32(statusFont.Height()) + 12,
			}
		}
		if isChipStatusPart(p) {
			// The now-playing chip opens the player it is naming; the idle
			// output chip is measured the same way so it draws in the same
			// spot, but it does not join the focus graph.
			statusMediaRect = sdl.Rect{
				X: startX - 6, Y: statusY - 6,
				W: pw + 12, H: int32(statusFont.Height()) + 12,
			}
		}
		startX += pw + gap
	}
	// The meter is the Wi-Fi button, so its hit area is grown to a finger-sized
	// target once the cluster is placed and the neighbours' rects are known.
	if statusWiFiRect.W > 0 {
		statusWiFiRect = statusFingerRect(statusWiFiRect, barH, weatherStatusRect, statusClockRect)
	}
	if focusEngine.ElementIndex() == ChromeClockIndex && statusClockRect.W > 0 {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 230)
		renderer.DrawRect(&statusClockRect)
	}
	if focusEngine.ElementIndex() == ChromeWeatherIndex && weatherStatusRect.W > 0 {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 230)
		renderer.DrawRect(&weatherStatusRect)
	}
	if focusEngine.ElementIndex() == ChromeWiFiIndex && statusWiFiRect.W > 0 {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 230)
		renderer.DrawRect(&statusWiFiRect)
	}
	if focusEngine.ElementIndex() == ChromeMediaIndex && statusMediaRect.W > 0 {
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 230)
		renderer.DrawRect(&statusMediaRect)
	}

	// Notification bell: top-right corner, always reachable with the D-pad.
	if bellW > 0 {
		by := (barH - bellH) / 2
		renderNotifBell(renderer, config, screenWidth-StatusBarMargin, by)
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
// crowded: the network name first, then the forecast, then the offline badge.
// The clock and the battery level are what the user glanced at, so they leave
// last.
func statusDropIndex(parts []string) int {
	// The chip goes before any reading: it is the one part that can be
	// arbitrarily long — a media title or a headset's name — and the network,
	// forecast, clock and battery are what the corner exists for.
	if n := len(parts); n > 2 && isChipStatusPart(parts[n-1]) {
		return n - 1
	}
	if len(parts) > 2 && !strings.HasPrefix(parts[0], "! Offline") {
		return 0
	}
	for i, p := range parts {
		if isWeatherStatusPart(p) {
			return i
		}
	}
	if len(parts) > 2 && strings.HasPrefix(parts[0], "! Offline") {
		return 0
	}
	return len(parts) - 1
}

// headerStatusParts returns the right-side status cluster in one canonical
// order shared by the home header and every scene's status bar: network,
// weather (hi/lo), time (HH:MM), battery, and the chip — the now-playing title
// while something plays, the connected output's name while nothing does. Both
// headers draw this exact list, so the right side of the top bar never shifts or
// reorders between screens.
func headerStatusParts(config *Config) []string {
	parts := make([]string, 0, 5)
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
	var today WeatherDay
	if len(weatherDaily) > 0 {
		today = weatherDaily[0]
	}
	wxMutex.Unlock()
	if ready && len(weatherDaily) > 0 {
		unit := weatherUnit(config)
		hi, lo := weatherTemp(today.TMax, unit), weatherTemp(today.TMin, unit)
		parts = append(parts, safeStatusText(fmt.Sprintf("%d%s / %d%s", int(hi), unit, int(lo), unit)))
	}
	parts = append(parts, safeStatusText(time.Now().Format("15:04")))
	if bat := statusBatteryPercent(); bat >= 0 {
		parts = append(parts, safeStatusText(fmt.Sprintf("%d%%", bat)))
	}
	// The chip closes the cluster, so it sits in the far corner next to the
	// bell: the one place a "what am I listening to?" glance expects it.
	if chip, ok := statusMediaPart(); ok {
		parts = append(parts, chip)
	}
	return parts
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
	// The File Explorer's B binding climbs the directory tree, so it is
	// advertised as "Parent" inside a folder and "Back" at the root.
	backLabel := "Back"
	if scene.Name == "FileExplorer" && feCurrentPath(config) != feRoot(config) {
		backLabel = "Parent"
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Navigate"},
		{Button: "A", Label: "Open"},
		{Button: "B", Label: backLabel},
	}
	switch scene.Name {
	case "Browser":
		// The browser has its own D-pad model: links on the page, the address
		// bar on A, the search keyboard on Y and the full link list on X. The
		// triggers zoom the reader text — the same action the toolbar chip
		// steps — so both ways in are named here.
		hints[1] = FooterHint{Button: "A", Label: "Link / URL"}
		hints = append(hints,
			FooterHint{Button: "L1/R1", Label: "Scroll"},
			FooterHint{Button: "L2/R2", Label: "Zoom"},
			FooterHint{Button: "Y", Label: "Search"},
			FooterHint{Button: "X", Label: "Links"},
		)
	case "Chat":
		// A opens the on-screen keyboard (Return inside it sends); Y sends the
		// draft straight to the assistant and X renames the local user.
		hints[1] = FooterHint{Button: "A", Label: "Type"}
		hints = append(hints,
			FooterHint{Button: "Y", Label: "Ask AI"},
			FooterHint{Button: "X", Label: "Rename"},
		)
	case "Tube":
		// Shoulders page the results, triggers fetch the next page: the chip in
		// the corner advertises the same action.
		hints = append(hints,
			FooterHint{Button: "X", Label: "Search"},
			FooterHint{Button: "Y", Label: "Shorts"},
			FooterHint{Button: "L1/R1", Label: "Page"},
			FooterHint{Button: "R2", Label: "More"},
		)
	case "Apps":
		// The launcher is a tile grid: A starts the focused app.
		hints[1] = FooterHint{Button: "A", Label: "Launch"}
	case "Packages":
		// A row's action menu takes A (choose) and B (close) while it is
		// open; the footer follows it instead of advertising Open/Back.
		if packageActionsOpen {
			hints[1] = FooterHint{Button: "A", Label: "Select"}
			hints[2] = FooterHint{Button: "B", Label: "Close"}
		}
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
				FooterHint{Button: "L1/R1", Label: "Folder"},
			)
		} else if notes.tab == 3 {
			// Search is a draft, not an edit: B leaves the field.
			hints = []FooterHint{{Button: "B", Label: "Done"}}
		} else {
			hints = []FooterHint{{Button: "B", Label: "Save & Back"}}
		}
	case "Calculator":
		// The pad walks the key grid; the shoulders are Clear and Backspace.
		hints[1] = FooterHint{Button: "A", Label: "Select"}
		hints = append(hints,
			FooterHint{Button: "L1", Label: "Clear"},
			FooterHint{Button: "R1", Label: "Delete"},
		)
	case "PDFReader":
		// Page turns are the shoulders (or left/right); the pad scrolls the
		// page and nothing opens.
		hints = []FooterHint{
			{Button: "D-Pad", Label: "Scroll"},
			{Button: "L1/R1", Label: "Page"},
			{Button: "B", Label: "Back"},
		}
	case "Alarm":
		// The list and the editor take different keys, so the footer follows the
		// open tab instead of advertising "Open" while a time is being set.
		if alarmUI.tab == 0 {
			hints[1] = FooterHint{Button: "A", Label: "Edit"}
			hints = append(hints,
				FooterHint{Button: "X", Label: "Toggle"},
				FooterHint{Button: "Y", Label: "Add"},
				FooterHint{Button: "L1", Label: "Delete"},
			)
		} else {
			hints = []FooterHint{{"D-Pad", "Change"}, {"A", "Next"}, {"B", "Back"}}
		}
	case "Pomodoro":
		// A starts and pauses, X skips the phase, Y resets the cycle.
		hints[1] = FooterHint{Button: "A", Label: "Start / Pause"}
		hints = append(hints,
			FooterHint{Button: "X", Label: "Skip"},
			FooterHint{Button: "Y", Label: "Reset"},
		)
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
		button := strings.TrimSpace(hint.Button)
		label := strings.TrimSpace(hint.Label)
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

	barH := footerBarHeight()
	y := screenHeight - barH

	// Opaque footer surface on the shared dark surface, accent divider on top.
	fillRoundedRect(renderer, 0, y, screenWidth, barH, 0, HomeFooterColor())
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 40)
	renderer.FillRect(&sdl.Rect{X: 0, Y: y, W: screenWidth, H: 1})

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	// Hint groups: a small button badge pill plus a separate label, centered as
	// one row inside the footer. planFooterHints measures the row and re-lays it
	// out until it fits, because on the Brick's 960-wide canvas the full Tube
	// row used to run off both edges ("d Navigate" … "R2").
	plan := planFooterHints(config, footerHints(config, scene), screenWidth-24)
	font = plan.font
	hints := plan.hints
	total := plan.width

	x := (screenWidth - total) / 2
	textY := y + (barH-plan.fontHeight)/2
	badgeY := y + (barH-plan.badgeH)/2
	for i, g := range hints {
		badge := sdl.Rect{X: x, Y: badgeY, W: plan.badgeW[i], H: plan.badgeH}
		fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, ColorIconSurface)
		renderText(renderer, config, font, g.Button, ColorTextPrimary(), badge.X+plan.badgePad, textY)
		labelX := badge.X + plan.badgeW[i] + plan.badgeGap
		renderText(renderer, config, font, g.Label, ColorTextSecondary(), labelX, textY)
		x += plan.badgeW[i] + plan.badgeGap + plan.labelW[i] + plan.groupGap
	}
}

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

// planFooterHints picks the largest type and the tightest spacing that keep the
// hint row inside avail pixels, dropping trailing hints only if even the
// smallest type does not fit.
func planFooterHints(config *Config, hints []FooterHint, avail int32) footerPlan {
	font, size := getCachedFont(config, "small")
	if font == nil || size <= 0 {
		size = 16
	}
	spacings := []struct {
		size     int
		groupGap int32
		badgePad int32
		badgeGap int32
	}{
		{size, 32, 12, 6},
		{size - 1, 24, 10, 5},
		{size - 2, 18, 9, 5},
		{size - 3, 14, 8, 4},
		{size - 4, 10, 7, 4},
	}

	plan := footerPlan{hints: hints, groupGap: spacings[0].groupGap, badgePad: spacings[0].badgePad, badgeGap: spacings[0].badgeGap}
	for _, sp := range spacings {
		candidate := font
		if sp.size != size && sp.size > 0 {
			if smaller := getFontSized(config, "small", sp.size); smaller != nil {
				candidate = smaller
			}
		}
		plan.font = candidate
		plan.groupGap, plan.badgePad, plan.badgeGap = sp.groupGap, sp.badgePad, sp.badgeGap
		if footerHintsWidth(candidate, hints, sp.groupGap, sp.badgePad, sp.badgeGap) <= avail {
			break
		}
	}
	if plan.font == nil {
		plan.font = font
	}

	// Still too wide for even the smallest type: drop hints from the end.
	keep := len(plan.hints)
	for keep > 1 && footerHintsWidth(plan.font, plan.hints[:keep], plan.groupGap, plan.badgePad, plan.badgeGap) > avail {
		keep--
	}
	plan.hints = plan.hints[:keep]

	plan.fontHeight = int32(plan.font.Height())
	plan.badgeH = int32(20)
	if plan.fontHeight+6 > plan.badgeH {
		plan.badgeH = plan.fontHeight + 6
	}
	plan.badgeW = make([]int32, len(plan.hints))
	plan.labelW = make([]int32, len(plan.hints))
	for i, g := range plan.hints {
		bw, _ := measureText(plan.font, g.Button)
		lw, _ := measureText(plan.font, g.Label)
		plan.badgeW[i] = bw + plan.badgePad*2
		plan.labelW[i] = lw
	}
	plan.width = footerHintsWidth(plan.font, plan.hints, plan.groupGap, plan.badgePad, plan.badgeGap)
	return plan
}

// footerHintsWidth measures one hint row laid out with the given font and
// spacing, so the planner can compare it with the panel width before drawing.
func footerHintsWidth(font *ttf.Font, hints []FooterHint, groupGap, badgePad, badgeGap int32) int32 {
	if font == nil {
		return 0
	}
	total := int32(0)
	for _, g := range hints {
		// Cached widths: this runs several times per frame (once per type size
		// the planner tries), and the hints are the same strings every time.
		bw, _ := measureText(font, g.Button)
		lw, _ := measureText(font, g.Label)
		total += bw + badgePad*2 + badgeGap + lw
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
	ReducedMotion      bool   `json:"reducedMotion"`
	LowPower           bool   `json:"lowPower"`
	PowerMode          string `json:"powerMode"`
	LedSync            bool   `json:"ledSync"`
	GridColumns        int    `json:"gridColumns"`
	SearchWidth        int    `json:"searchWidth"`
	DynamicBg          bool   `json:"dynamicBg"`
	ScreensaverTimeout int    `json:"screensaverTimeout"`
	SocialNotifs       bool   `json:"socialNotifs"`
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
			log.Printf("[CONFIG] jukauser.json is unusable (%v); recovered settings from jukauser.json.bak", err)
			uc, err, recovered = backup, nil, true
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[CONFIG] jukauser.json not found; starting with defaults")
		} else {
			log.Printf("[CONFIG] cannot read jukauser.json: %v; starting with defaults", err)
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

func saveUserConfig(user *UserConfig) {
	saveUserConfigToDir("", user)
}

// saveUserConfigToDir persists jukauser.json into dir (empty means the current
// working directory). The debounced writer uses it so a pending save cannot
// land in whatever directory happens to be current two seconds later.
func saveUserConfigToDir(dir string, user *UserConfig) {
	if user == nil || !userConfigWritesEnabled {
		return
	}
	path := "jukauser.json"
	if dir != "" {
		path = filepath.Join(dir, "jukauser.json")
	}
	userCopy := *user
	userCopy.Variables.Custom = make(map[string]interface{})
	for k, v := range user.Variables.Custom {
		if volatileCustomVars[k] {
			continue
		}
		userCopy.Variables.Custom[k] = v
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
		log.Printf("saveUserConfig: marshal error: %v", err)
		return
	}
	// Keep a short rollback history next to the config. The helper skips an
	// identical newest snapshot, so debounced saves do not grow the folder.
	if powerConfigHistoryEnabled {
		if err := snapshotPowerConfig(path); err != nil {
			log.Printf("saveUserConfig: history snapshot: %v", err)
		}
	}
	if err := AtomicWrite(path, data, 0644); err != nil {
		log.Printf("saveUserConfig: write error: %v", err)
		return
	}
	LogScene("config").Info("user config saved", "path", path)
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
	config.Variables.ReducedMotion = user.Variables.ReducedMotion
	config.Variables.LowPower = user.Variables.LowPower
	config.Variables.LEDSync = user.Variables.LedSync
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
	for k, v := range user.Variables.Custom {
		if !volatileCustomVars[k] {
			config.Variables.Custom[k] = v
		}
	}
	ensureThemeColors(config)
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

	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		log.Printf("saveConfig: marshal error: %v", err)
		return
	}
	if err := AtomicWrite("jukaconfig.json", data, 0644); err != nil {
		log.Printf("saveConfig: write error: %v", err)
		configHealth.RecordSave("jukaconfig.json", fmt.Errorf("write error: %w", err))
		return
	}
	configHealth.RecordSave("jukaconfig.json", nil)
	configHealth.MarkSaveOK()
	LogScene("config").Info("design config saved")
}
