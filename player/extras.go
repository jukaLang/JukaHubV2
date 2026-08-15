package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// --- Recently Played (persisted to recently_played.json) ---

var (
	rpMutex        sync.Mutex
	recentlyPlayed = map[string]bool{}
)

func loadRecentlyPlayed() {
	data, err := os.ReadFile("recently_played.json")
	if err == nil {
		_ = json.Unmarshal(data, &recentlyPlayed)
	}
}

func isRecentlyPlayed(key string) bool {
	rpMutex.Lock()
	defer rpMutex.Unlock()
	return recentlyPlayed[key]
}

func recordPlayed(key string) {
	if key == "" {
		return
	}
	rpMutex.Lock()
	recentlyPlayed[key] = true
	data, _ := json.MarshalIndent(recentlyPlayed, "", "  ")
	rpMutex.Unlock()
	_ = os.WriteFile("recently_played.json", data, 0644)
}

// --- Weather (best-effort IP-geolocated current temperature; non-fatal) ---

var (
	wxMutex      sync.Mutex
	weatherTempC float64
	weatherReady bool
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
	url := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m", lat, lon)
	resp, err := client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var w struct {
		Current struct {
			Temperature2m float64 `json:"temperature_2m"`
		} `json:"current"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return
	}
	wxMutex.Lock()
	weatherTempC = w.Current.Temperature2m
	weatherReady = true
	wxMutex.Unlock()
}

// --- Top status bar (clock + weather + username) ---

func renderStatusBar(renderer *sdl.Renderer, config *Config) {
	barH := int32(36)
	// frosted glass status bar
	fillRoundedRect(renderer, 0, 0, screenWidth, barH, 0, sdl.Color{R: 18, G: 22, B: 32, A: 240})
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 140)
	renderer.FillRect(&sdl.Rect{X: 0, Y: barH - 1, W: screenWidth, H: 1})

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	white := sdl.Color{R: 235, G: 238, B: 245, A: 255}
	secondary := sdl.Color{R: 160, G: 170, B: 190, A: 255}

	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = font
	}

	// accent dot (pulsing)
	pulse := uint8(180 + 75*float64(math.Sin(float64(sdl.GetTicks64())/500.0)))
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, pulse)
	renderer.FillRect(&sdl.Rect{X: 16, Y: 10, W: 14, H: 14})
	renderText(renderer, config, titleFont, "JukaHub", white, 38, 6)

	// Username
	if name, ok := config.Variables.Custom["TSPUsername"].(string); ok && name != "" {
		renderText(renderer, config, font, "Hi "+name, secondary, 140, 10)
	}

	// Clock (top-right)
	now := time.Now()
	clk := now.Format("15:04")
	cw, _, _ := font.SizeUTF8(clk)
	clockX := screenWidth - int32(cw) - 16

	// Weather pill (immediately left of the clock)
	wxMutex.Lock()
	ready := weatherReady
	t := weatherTempC
	wxMutex.Unlock()
	wxText := ""
	if ready {
		unit := config.Variables.WeatherUnit
		disp := t
		if unit == "F" {
			disp = t*9/5 + 32
		}
		wxText = fmt.Sprintf("%.0f°%s", disp, unit)
	} else {
		wxText = "—"
	}
	ww, _, _ := font.SizeUTF8(wxText)
	wxX := clockX - int32(ww) - 20
	renderer.SetDrawColor(255, 255, 255, 30)
	renderer.FillRect(&sdl.Rect{X: wxX - 10, Y: 6, W: int32(ww) + 20, H: 22})
	renderText(renderer, config, font, wxText, white, wxX, 8)

	renderText(renderer, config, font, clk, white, clockX, 8)
}

// --- User config (mutable settings) persisted to jukauser.json ---

type UserConfig struct {
	Variables UserVariables `json:"variables"`
}

type UserVariables struct {
	ButtonColor        RGB                `json:"buttonColor"`
	LabelColor         RGB                `json:"labelColor"`
	InputColor         RGB                `json:"inputColor"`
	Fullscreen         bool               `json:"fullscreen"`
	FileExplorerRoot   string             `json:"fileExplorerRoot"`
	WeatherEnabled     bool               `json:"weatherEnabled"`
	WeatherUnit        string             `json:"weatherUnit"`
	TSPUsername        string             `json:"tspUsername"`
	PlaybackResolution string            `json:"playbackResolution"`
	AudioBackend       string             `json:"audioBackend"`
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
		switch k {
		case "search_results", "fe_entries", "fe_path", "stream_url", "search_query", "search_error":
			// skip volatile runtime state
		default:
			userCopy.Variables.Custom[k] = v
		}
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
		if _, volatile := map[string]bool{
			"search_results": true, "fe_entries": true, "fe_path": true,
			"stream_url": true, "search_query": true, "search_error": true,
		}[k]; !volatile {
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
		switch k {
		case "search_results", "fe_entries", "fe_path", "stream_url", "search_query", "search_error":
			// skip volatile runtime state
		default:
			saved.Variables.Custom[k] = v
		}
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

