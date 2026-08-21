package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// --- System status helpers ---

func getBatteryPercent() int {
	// Try common Linux sysfs paths for battery capacity
	base := "/sys/class/power_supply"
	entries, err := os.ReadDir(base)
	if err != nil {
		return -1
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.Contains(strings.ToLower(name), "bat") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, name, "capacity"))
		if err == nil {
			var pct int
			if _, err := fmt.Fscan(strings.NewReader(string(data)), &pct); err == nil {
				if pct > 100 {
					pct = 100
				}
				if pct < 0 {
					pct = 0
				}
				return pct
			}
		}
	}
	return -1
}

func getWifiStatus() string {
	// Quick heuristic: look for wireless interfaces under /sys/class/net
	base := "/sys/class/net"
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			wireless := filepath.Join(base, entry.Name(), "wireless")
			if _, err := os.Stat(wireless); err == nil {
				return "WiFi"
			}
		}
	}
	return ""
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
)

// saveUserConfigDebounced batches rapid successive config changes into a single
// disk write after a short quiet period. This avoids excessive I/O on handheld
// devices when, for example, a user is typing in an input field.
func saveUserConfigDebounced(user *UserConfig) {
	userConfigSaveMutex.Lock()
	pendingUserConfig = user
	if userConfigSaveTimer != nil {
		userConfigSaveTimer.Stop()
	}
	userConfigSaveTimer = time.AfterFunc(2*time.Second, func() {
		userConfigSaveMutex.Lock()
		defer userConfigSaveMutex.Unlock()
		if pendingUserConfig != nil {
			saveUserConfig(pendingUserConfig)
			pendingUserConfig = nil
		}
	})
	userConfigSaveMutex.Unlock()
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

	saveUserConfigDebounced(&UserConfig{Variables: UserVariables{
		ButtonColor:        config.Variables.ButtonColor,
		LabelColor:         config.Variables.LabelColor,
		InputColor:         config.Variables.InputColor,
		Fullscreen:         config.Variables.Fullscreen,
		FileExplorerRoot:   config.Variables.FileExplorerRoot,
		WeatherEnabled:     config.Variables.WeatherEnabled,
		WeatherUnit:        config.Variables.WeatherUnit,
		TSPUsername:        config.Variables.TSPUsername,
		PlaybackResolution: config.Variables.PlaybackResolution,
		AudioBackend:       config.Variables.AudioBackend,
		GridColumns:        config.Variables.GridColumns,
		SearchWidth:        config.Variables.SearchWidth,
		DynamicBg:          config.Variables.DynamicBg,
		ScreensaverTimeout: config.Variables.ScreensaverTimeout,
		SocialNotifs:       config.Variables.SocialNotifs,
		Custom:             config.Variables.Custom,
	}})
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
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		fetchWeatherOnce()
		for range ticker.C {
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

func fetchWeatherOnce() {
	client := &http.Client{Timeout: 6 * time.Second}
	lat, lon := 40.71, -74.0 // default fallback (New York) if geolocation fails
	if geo, err := client.Get("https://ipapi.co/json/"); err == nil {
		defer geo.Body.Close()
		var g struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		}
		if err := json.NewDecoder(geo.Body).Decode(&g); err == nil && (g.Latitude != 0 || g.Longitude != 0) {
			lat, lon = g.Latitude, g.Longitude
		} else {
			log.Printf("[WARN] Geolocation unavailable, using default weather location")
		}
	} else {
		log.Printf("[WARN] Geolocation request failed, using default weather location")
	}
	url := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&daily=temperature_2m_max,temperature_2m_min,weather_code,precipitation_probability_max&forecast_days=7&timezone=auto", lat, lon)
	resp, err := client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()
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

// headerTitle is the single header line: "JukaHub - v0.4.0" on Home and
// "JukaHub - v0.4.0 - <Scene>" on every other scene. The active scene appears
// exactly once per frame, here.
func headerTitle(config *Config, scene SceneConfig) string {
	base := "JukaHub - " + versionString(config)
	if scene.Layout == "home" {
		return base
	}
	return base + " - " + sceneDisplayName(scene.Name)
}

// --- Top status bar (brand + version + scene + clock/weather/battery) ---

func renderStatusBar(renderer *sdl.Renderer, config *Config) {
	sceneTitleBackRect = sdl.Rect{}
	barH := HomeTopBarH
	if screenHeight < 600 {
		barH = HomeTopBarHSmall
	}
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
	white := ColorTextPrimary()
	secondary := ColorTextSecondary()

	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = font
	}

	scene := SceneConfig{}
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		scene = config.Scenes[currentSceneIndex]
	}

	// Left cluster: brand (primary) + " - vX.Y.Z" and " - Scene" (muted),
	// all on one line inside the single 56px header.
	titleStr := "JukaHub"
	titleW, _, _ := titleFont.SizeUTF8(titleStr)
	titleX := StatusBarMargin
	titleY := barH/2 - TitleCenterOffset
	renderText(renderer, config, titleFont, titleStr, white, titleX, titleY)

	restStr := " - " + versionString(config)
	if scene.Layout != "home" && scene.Name != "" {
		restStr += " - " + sceneDisplayName(scene.Name)
	}
	restW, _, _ := font.SizeUTF8(restStr)
	restX := titleX + int32(titleW) + 8
	renderText(renderer, config, font, restStr, secondary, restX, titleY+3)

	headerTextW := int32(titleW) + 8 + int32(restW)

	// Compact back pill for mouse users, right after the header text on every
	// non-home scene. Controller/keyboard use B/Escape; the footer says so.
	if scene.Layout != "home" && scene.Name != "" && scene.Name != "JukaLand" {
		small, _ := getCachedFont(config, "small")
		if small != nil {
			label := "<"
			bw, _, _ := small.SizeUTF8(label)
			pad := int32(8)
			bh := int32(22)
			w := int32(bw) + pad*2
			x := restX + int32(restW) + SpaceMD
			y := (barH - bh) / 2
			sceneTitleBackRect = sdl.Rect{X: x, Y: y, W: w, H: bh}
			fillRoundedRect(renderer, x, y, w, bh, bh/2, ColorIconSurface)
			renderText(renderer, config, small, label, secondary, x+pad, y+(bh-int32(small.Height()))/2)
			headerTextW += SpaceMD + w
		}
	}

	// Right-side status cluster in the canonical shared order (network,
	// weather, time, battery) — identical to the home header so the right
	// side of the top bar stays put across screens.
	parts := headerStatusParts(config)
	statusFont := headerStatusFont()
	if statusFont == nil {
		statusFont = font
	}
	rightX := screenWidth - StatusBarMargin
	gap := StatusPartGap

	// Measure total width and drop least-important items if they don't fit
	// next to the brand/version/scene/back cluster.
	totalW := int32(0)
	for _, p := range parts {
		pw, _, _ := statusFont.SizeUTF8(p)
		totalW += int32(pw)
	}
	if len(parts) > 1 {
		totalW += gap * int32(len(parts)-1)
	}
	maxW := screenWidth - StatusBarMargin - titleX - headerTextW - Space2XL
	if maxW < 120 {
		maxW = 120
	}
	for totalW > maxW && len(parts) > 1 {
		pw, _, _ := statusFont.SizeUTF8(parts[len(parts)-1])
		totalW -= int32(pw)
		totalW -= gap
		parts = parts[:len(parts)-1]
	}

	// Vertically center the cluster in the bar (same 16px font and position
	// as the home header).
	statusY := (barH - int32(statusFont.Height())) / 2
	if statusY < 0 {
		statusY = 0
	}
	startX := rightX - totalW
	for _, p := range parts {
		pw, _, _ := statusFont.SizeUTF8(p)
		col := secondary
		if strings.HasPrefix(p, "! Offline") {
			col = ColorWarning
		}
		renderText(renderer, config, statusFont, p, col, startX, statusY)
		startX += int32(pw) + gap
	}

}

// headerStatusParts returns the right-side status cluster in one canonical
// order shared by the home header and every scene's status bar: network,
// weather (hi/lo), time (HH:MM), battery. Both headers draw this exact list,
// so the right side of the top bar never shifts or reorders between screens.
func headerStatusParts(config *Config) []string {
	parts := make([]string, 0, 5)
	// Offline indicator takes priority over the SSID so the user instantly
	// knows why network features are failing.
	if !IsOnline() {
		parts = append(parts, "! Offline")
	} else if wifi := strings.TrimSpace(getWifiStatus()); wifi != "" {
		parts = append(parts, safeStatusText(wifi))
	}
	wxMutex.Lock()
	ready := weatherReady
	var today WeatherDay
	if len(weatherDaily) > 0 {
		today = weatherDaily[0]
	}
	wxMutex.Unlock()
	if ready && len(weatherDaily) > 0 {
		unit := config.Variables.WeatherUnit
		hi, lo := today.TMax, today.TMin
		if unit == "F" {
			hi = hi*9/5 + 32
			lo = lo*9/5 + 32
		}
		parts = append(parts, safeStatusText(fmt.Sprintf("%dC / %dC", int(hi), int(lo))))
	}
	parts = append(parts, safeStatusText(time.Now().Format("15:04")))
	if bat := getBatteryPercent(); bat >= 0 {
		parts = append(parts, safeStatusText(fmt.Sprintf("%d%%", bat)))
	}
	return parts
}

// headerStatusFont returns the small 16px Inter font both headers use for the
// right-side status cluster, so it renders at the same size everywhere.
func headerStatusFont() *ttf.Font {
	return loadHomeFonts().Small
}

// --- Footer rendering (controller hints) --

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
	// When the OS taskbar renders instead, it replaces the footer entirely.
	if sceneUsesTaskbar(scene.Name) {
		return
	}

	barH := HomeFooterH
	if screenHeight < 600 {
		barH = 42
	}
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

	// Contextual hints: every scene gets navigate + open + back. The File
	// Explorer's B binding climbs the directory tree, so it is advertised as
	// "Parent" inside a folder and "Back" at the root. Tube adds the X search
	// shortcut that actually exists for it.
	backLabel := "Back"
	if scene.Name == "FileExplorer" && feCurrentPath(config) != feRoot(config) {
		backLabel = "Parent"
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Navigate"},
		{Button: "A", Label: "Open"},
		{Button: "B", Label: backLabel},
	}
	if scene.Name == "Chat" {
		// In Chat, A sends the typed message rather than opening anything.
		hints[1] = FooterHint{Button: "A", Label: "Send"}
	}
	if scene.Name == "Tube" {
		hints = append(hints, FooterHint{Button: "X", Label: "Search"})
	}
	if scene.Name == "Chat" {
		hints = append(hints, FooterHint{Button: "Y", Label: "Ask AI"})
	}
	// Patch shows its own contextual hints (modal-aware) instead of the
	// generic defaults.
	if scene.Name == "Patch" {
		if patchHints, ok := patchFooterHints(); ok {
			hints = patchHints
		}
	}

	// Distinct hint groups: a small button badge pill plus a separate label,
	// 32px group gaps, centered as one row inside the footer.
	groupGap := int32(32)
	badgeH := int32(20)
	badgePad := int32(12)
	badgeGap := int32(6)
	fh := int32(font.Height())

	type group struct {
		badgeW int32
		w      int32
	}
	groups := make([]group, 0, len(hints))
	total := int32(0)
	for _, g := range hints {
		bw, _, _ := font.SizeUTF8(g.Button)
		lw, _, _ := font.SizeUTF8(g.Label)
		badgeW := int32(bw) + badgePad*2
		w := badgeW + badgeGap + int32(lw)
		groups = append(groups, group{badgeW: badgeW, w: w})
		total += w
	}
	if len(groups) > 1 {
		total += groupGap * int32(len(groups)-1)
	}

	x := (screenWidth - total) / 2
	textY := y + (barH-fh)/2
	badgeY := y + (barH-badgeH)/2
	for i, g := range hints {
		badge := sdl.Rect{X: x, Y: badgeY, W: groups[i].badgeW, H: badgeH}
		fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, ColorIconSurface)
		renderText(renderer, config, font, g.Button, ColorTextPrimary(), badge.X+badgePad, textY)
		labelX := badge.X + groups[i].badgeW + badgeGap
		renderText(renderer, config, font, g.Label, ColorTextSecondary(), labelX, textY)
		x += groups[i].w + groupGap
	}
}

// volatileCustomVars lists runtime-only keys that should not be persisted to
// disk. They are reconstructed each session rather than saved.
var volatileCustomVars = map[string]bool{
	"search_results": true,
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
	AudioBackend       string `json:"audioBackend"`
	ReducedMotion      bool   `json:"reducedMotion"`
	LowPower           bool   `json:"lowPower"`
	GridColumns        int    `json:"gridColumns"`
	SearchWidth        int    `json:"searchWidth"`
	DynamicBg          bool   `json:"dynamicBg"`
	ScreensaverTimeout int    `json:"screensaverTimeout"`
	SocialNotifs       bool   `json:"socialNotifs"`
	Custom             map[string]interface{}
}

func loadUserConfig() *UserConfig {
	data, err := os.ReadFile("jukauser.json")
	if err != nil {
		userConfigCache = &UserConfig{
			Variables: UserVariables{
				Custom: make(map[string]interface{}),
			},
		}
	} else {
		var uc UserConfig
		if err := json.Unmarshal(data, &uc); err != nil {
			log.Printf("loadUserConfig: parse error: %v", err)
			uc = UserConfig{}
		}
		if uc.Variables.Custom == nil {
			uc.Variables.Custom = make(map[string]interface{})
		}
		userConfigCache = &uc
	}
	// One-time migration: fold the legacy per-feature JSON files into their
	// jukauser.json sections, then delete them so only the two canonical JSON
	// files remain on disk.
	migrateLegacyUserFiles()
	return userConfigCache
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

func saveUserConfig(user *UserConfig) {
	if user == nil {
		return
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
	if err := AtomicWrite("jukauser.json", data, 0644); err != nil {
		log.Printf("saveUserConfig: write error: %v", err)
		return
	}
	LogScene("config").Info("user config saved")
}

func mergeUserConfig(config *Config, user *UserConfig) {
	if user == nil {
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
	config.Variables.AudioBackend = user.Variables.AudioBackend
	config.Variables.ReducedMotion = user.Variables.ReducedMotion
	config.Variables.LowPower = user.Variables.LowPower
	for k, v := range user.Variables.Custom {
		if !volatileCustomVars[k] {
			config.Variables.Custom[k] = v
		}
	}
}

// --- Persist design-only config to jukaconfig.json ---

func saveConfig(config *Config) {
	saved := *config
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
	saved.Variables.AudioBackend = ""

	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		log.Printf("saveConfig: marshal error: %v", err)
		return
	}
	if err := AtomicWrite("jukaconfig.json", data, 0644); err != nil {
		log.Printf("saveConfig: write error: %v", err)
		return
	}
	LogScene("config").Info("design config saved")
}
