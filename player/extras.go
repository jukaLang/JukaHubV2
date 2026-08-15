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
	barH := int32(28)
	// gradient status bar
	for i := int32(0); i < barH; i += 2 {
		t := float32(i) / float32(barH)
		r := uint8(float32(8) + t*float32(18))
		g := uint8(float32(10) + t*float32(20))
		b := uint8(float32(16) + t*float32(26))
		renderer.SetDrawColor(r, g, b, 235)
		renderer.FillRect(&sdl.Rect{X: 0, Y: i, W: screenWidth, H: 2})
	}
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 160)
	renderer.FillRect(&sdl.Rect{X: 0, Y: barH - 1, W: screenWidth, H: 1})

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	white := sdl.Color{R: 235, G: 238, B: 245, A: 255}

	// App title (top-left)
	titleFont, _ := getCachedFont(config, "medium")
	if titleFont == nil {
		titleFont = font
	}
	// accent dot (pulsing)
	pulse := uint8(180 + 75*float64(math.Sin(float64(sdl.GetTicks64())/500.0)))
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, pulse)
	renderer.FillRect(&sdl.Rect{X: 12, Y: 8, W: 12, H: 12})
	renderText(renderer, config, titleFont, "JukaHub", white, 32, 3)

	// Username (under/next to title)
	if name, ok := config.Variables.Custom["TSPUsername"].(string); ok && name != "" {
		renderText(renderer, config, font, "Hi "+name, white, 130, 5)
	}

	// Clock (top-right)
	now := time.Now()
	clk := now.Format("15:04")
	cw, _, _ := font.SizeUTF8(clk)
	clockX := screenWidth - int32(cw) - 12

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
	wxX := clockX - int32(ww) - 24
	renderer.SetDrawColor(255, 255, 255, 40)
	renderer.FillRect(&sdl.Rect{X: wxX - 8, Y: 4, W: int32(ww) + 16, H: 20})
	renderText(renderer, config, font, wxText, white, wxX, 5)

	renderText(renderer, config, font, clk, white, clockX, 5)
}

// --- Persist settings/theme to jukaconfig.json ---

func saveConfig(config *Config) {
	// Copy without volatile runtime variables so the saved file stays clean.
	saved := *config
	savedVars := config.Variables
	savedVars.Custom = make(map[string]interface{})
	for k, v := range config.Variables.Custom {
		switch k {
		case "search_results", "fe_entries", "fe_path", "stream_url", "search_query", "search_error":
			// skip volatile runtime state
		default:
			savedVars.Custom[k] = v
		}
	}
	saved.Variables = savedVars

	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		log.Printf("saveConfig: marshal error: %v", err)
		return
	}
	if err := os.WriteFile("jukaconfig.json", data, 0644); err != nil {
		log.Printf("saveConfig: write error: %v", err)
		return
	}
	log.Printf("[DEBUG] Config saved to jukaconfig.json")
}
