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

	"github.com/veandco/go-sdl2/sdl"
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

// topBarError is a short message shown centered in the top bar.
var topBarError string

func setTopBarError(msg string) {
	topBarError = msg
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
}

// --- Top status bar (clock + weather + username) ---

func renderStatusBar(renderer *sdl.Renderer, config *Config) {
	barH := int32(28)
	// sleek frosted glass status bar (theme-aware)
	fillRoundedRect(renderer, 0, 0, screenWidth, barH, 0, WithAlpha(ColorSurfaceRaised, 230))
	// subtle top highlight
	renderer.SetDrawColor(255, 255, 255, 10)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: 1})

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	white := ColorTextPrimary()
	secondary := ColorTextTertiary()

	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = font
	}

	// Left: page title or profile (no version number on the home screen).
	titleStr := "JukaHub"
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		titleStr = config.Scenes[currentSceneIndex].Name
		if titleStr == "Main" {
			titleStr = "JukaHub"
		}
	}
	titleW, _, _ := titleFont.SizeUTF8(titleStr)
	renderText(renderer, config, titleFont, titleStr, white, 12, 6)

	// Username (placed after the title to avoid overlap)
	if name, ok := config.Variables.Custom["TSPUsername"].(string); ok && name != "" {
		userX := int32(12) + int32(titleW) + 16
		renderText(renderer, config, font, "Hi "+name, secondary, userX, 8)
	}

	// Right-side status: wifi + battery + weather + clock (icon-style, quiet).
	now := time.Now()
	clk := now.Format("15:04")
	wxMutex.Lock()
	ready := weatherReady
	var today WeatherDay
	if len(weatherDaily) > 0 {
		today = weatherDaily[0]
	}
	wxMutex.Unlock()
	wxText := "—"
	if ready && len(weatherDaily) > 0 {
		unit := config.Variables.WeatherUnit
		hi, lo := today.TMax, today.TMin
		if unit == "F" {
			hi = hi*9/5 + 32
			lo = lo*9/5 + 32
		}
		wxText = fmt.Sprintf("%d°/%d°", int(hi), int(lo))
	}

	var rightParts []string
	rightParts = append(rightParts, clk)
	if wxText != "—" {
		rightParts = append(rightParts, wxText)
	}
	bat := getBatteryPercent()
	if bat >= 0 {
		rightParts = append(rightParts, fmt.Sprintf("%d%%", bat))
	}
	wifi := getWifiStatus()
	if wifi != "" {
		rightParts = append(rightParts, wifi)
	}
	rightText := strings.Join(rightParts, "  ")
	rw, _, _ := font.SizeUTF8(rightText)
	rx := screenWidth - int32(rw) - 30
	// frosted pill behind the right-side status
	fillRoundedRect(renderer, rx-10, 5, int32(rw)+20, 18, 9, WithAlpha(ColorSurfaceAlt, 150))
	renderText(renderer, config, font, rightText, white, rx, 8)
}

func renderBottomErrorBar(renderer *sdl.Renderer, config *Config) {
	if topBarError == "" {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	barH := int32(22)
	y := screenHeight - barH
	fillRoundedRect(renderer, 0, y, screenWidth, barH, 0, WithAlpha(ColorSurfaceRaised, 240))
	renderer.SetDrawColor(255, 100, 100, 180)
	renderer.FillRect(&sdl.Rect{X: 0, Y: y, W: screenWidth, H: 1})
	ew, _, _ := font.SizeUTF8(topBarError)
	ex := (screenWidth - int32(ew)) / 2
	renderText(renderer, config, font, topBarError, sdl.Color{R: 255, G: 100, B: 100, A: 255}, ex, y+6)
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

type UserConfig struct {
	Variables UserVariables `json:"variables"`
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
	Custom             map[string]interface{}
}

func loadUserConfig() *UserConfig {
	data, err := os.ReadFile("jukauser.json")
	if err != nil {
		return &UserConfig{
			Variables: UserVariables{
				Custom: make(map[string]interface{}),
			},
		}
	}
	var uc UserConfig
	if err := json.Unmarshal(data, &uc); err != nil {
		log.Printf("loadUserConfig: parse error: %v", err)
		return &UserConfig{
			Variables: UserVariables{
				Custom: make(map[string]interface{}),
			},
		}
	}
	if uc.Variables.Custom == nil {
		uc.Variables.Custom = make(map[string]interface{})
	}
	return &uc
}

func saveUserConfig(user *UserConfig) {
	userCopy := *user
	userCopy.Variables.Custom = make(map[string]interface{})
	for k, v := range user.Variables.Custom {
		if volatileCustomVars[k] {
			continue
		}
		userCopy.Variables.Custom[k] = v
	}
	data, err := json.MarshalIndent(userCopy, "", "  ")
	if err != nil {
		log.Printf("saveUserConfig: marshal error: %v", err)
		return
	}
	if err := os.WriteFile("jukauser.json", data, 0644); err != nil {
		log.Printf("saveUserConfig: write error: %v", err)
		return
	}
	log.Printf("[DEBUG] User config saved to jukauser.json")
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
	if err := os.WriteFile("jukaconfig.json", data, 0644); err != nil {
		log.Printf("saveConfig: write error: %v", err)
		return
	}
	log.Printf("[DEBUG] Design config saved to jukaconfig.json")
}
