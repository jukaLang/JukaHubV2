package main

// weather_popover.go — the "Local weather" panel behind the top-bar weather
// chip. The status cluster already showed today's high/low, but the numbers
// were plain text with no way to see the rest of the forecast. The chip now
// opens this card from both the home header and every scene status bar.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

var (
	weatherPopoverOpen bool
	weatherStatusRect  sdl.Rect

	// weatherPopoverFetching is true while the card's own refresh is running,
	// so the empty state can say "fetching" instead of "unavailable" for the
	// few seconds the request takes.
	weatherPopoverFetchingMu sync.Mutex
	weatherPopoverFetching   bool
)

func setWeatherPopoverFetching(v bool) {
	weatherPopoverFetchingMu.Lock()
	weatherPopoverFetching = v
	weatherPopoverFetchingMu.Unlock()
}

func weatherPopoverIsFetching() bool {
	weatherPopoverFetchingMu.Lock()
	defer weatherPopoverFetchingMu.Unlock()
	return weatherPopoverFetching
}

const (
	weatherPanelW = int32(500)
	weatherRowGap = int32(6)
)

// isWeatherStatusPart recognises the hi/lo chip produced by
// headerStatusParts. Network SSIDs and the clock cannot match the "12C / 8C"
// shape, so the chip remains unambiguous without changing its visible text.
func isWeatherStatusPart(s string) bool {
	parts := strings.Split(strings.TrimSpace(s), " / ")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimSuffix(p, "C")
		p = strings.TrimSuffix(p, "F")
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "-")
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// weatherPopoverRect anchors the card below the weather chip. The card is
// clamped to the viewport so a narrow screen or a long SSID cannot push it off
// the edge.
func weatherPopoverRect(config *Config) sdl.Rect {
	w := weatherPanelW
	if w > screenWidth-32 {
		w = screenWidth - 32
	}
	wxMutex.Lock()
	days := len(weatherDaily)
	ready := weatherReady
	wxMutex.Unlock()

	font, _ := getCachedFont(config, "small")
	lineH := int32(22)
	if font != nil {
		lineH = int32(font.Height()) + weatherRowGap
	}
	h := int32(190)
	if ready && days > 0 {
		h = 52 + int32(days)*lineH + 10
	}

	x := screenWidth - 32 - w
	if weatherStatusRect.W > 0 {
		x = weatherStatusRect.X + weatherStatusRect.W/2 - w/2
	}
	if x < 16 {
		x = 16
	}
	if x+w > screenWidth-16 {
		x = screenWidth - 16 - w
	}
	y := HomeTopBarH + 8
	if y+h > screenHeight-16 {
		h = screenHeight - 16 - y
	}
	return sdl.Rect{X: x, Y: y, W: w, H: h}
}

// toggleWeatherPopover opens or closes the forecast. Like the calendar and the
// notification center it is exclusive with the other top-bar overlays.
func toggleWeatherPopover() {
	weatherPopoverOpen = !weatherPopoverOpen
	if weatherPopoverOpen {
		homeCalendarOpen = false
		tb.startOpen = false
		nc.open = false
		// Opening the card is the moment the user is asking for the
		// forecast, so refresh a missing or stale one instead of showing the
		// "unavailable" state until the next 10-minute tick.
		wxMutex.Lock()
		stale := !weatherReady || len(weatherDaily) == 0
		wxMutex.Unlock()
		if stale {
			setWeatherPopoverFetching(true)
			go func() {
				defer setWeatherPopoverFetching(false)
				fetchWeatherOnce()
			}()
		}
	}
}

func dismissWeatherPopover() {
	weatherPopoverOpen = false
}

// ctrlWeatherPopover keeps the floating card modal for controller users: no
// D-pad event may reach the scene underneath, and A/B/Start close it.
func ctrlWeatherPopover(e *sdl.ControllerButtonEvent) bool {
	if e != nil {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B,
			sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
			dismissWeatherPopover()
			PlayBackSound()
		}
	}
	return true
}

// weatherPopoverClick consumes clicks while the forecast is open: clicks on the
// card stay inside it, any other click dismisses it without activating the
// scene underneath.
func weatherPopoverClick(mx, my int32, config *Config) bool {
	if !weatherPopoverOpen {
		return false
	}
	if pointInRect(mx, my, weatherPopoverRect(config)) {
		return true
	}
	dismissWeatherPopover()
	return true
}

// renderWeatherPopover draws the local forecast card. It is called by the
// shared render loop on every screen, after the scene chrome so it floats
// above the content.
func renderWeatherPopover(renderer *sdl.Renderer, config *Config) {
	if !weatherPopoverOpen || renderer == nil {
		return
	}
	r := weatherPopoverRect(config)
	if r.W <= 0 || r.H <= 0 {
		return
	}

	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}

	// A light scrim makes the card read as a foreground panel without hiding
	// the scene completely.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 70)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	UICard(renderer, r, UICardStyle{
		Fill:   sdl.Color{R: 16, G: 20, B: 30, A: 246},
		Border: ColorBorder,
		Radius: 16,
		Shadow: 90,
	})

	unit := weatherUnit(config)
	renderText(renderer, config, medium, "Local weather · °"+unit, ColorTextPrimary(), r.X+18, r.Y+14)

	wxMutex.Lock()
	ready := weatherReady
	days := append([]WeatherDay(nil), weatherDaily...)
	wxMutex.Unlock()

	if !ready || len(days) == 0 {
		title, body := "Weather unavailable", "Connect to the internet, then try again."
		if weatherPopoverIsFetching() {
			title, body = "Fetching local weather", "This needs an internet connection."
		}
		UIEmptyState(renderer, config,
			sdl.Rect{X: r.X, Y: r.Y + 52, W: r.W, H: r.H - 52},
			"!", title, body)
		return
	}

	convert := func(c float64) float64 { return weatherTemp(c, unit) }
	lineH := int32(font.Height()) + weatherRowGap
	rowY := r.Y + 52
	for i, d := range days {
		if rowY+lineH > r.Y+r.H-8 {
			break
		}
		label := d.Date
		if t, err := time.Parse("2006-01-02", d.Date); err == nil {
			if i == 0 {
				label = "Today"
			} else {
				label = t.Format("Mon")
			}
		}
		desc := weatherCodeDesc(d.Code)
		line := fmt.Sprintf("%s   %.0f° / %.0f°   %s", label, convert(d.TMax), convert(d.TMin), desc)
		if d.Pop > 0 {
			line += fmt.Sprintf("   %d%% rain", d.Pop)
		}
		col := ColorTextSecondary()
		if i == 0 {
			col = ColorTextPrimary()
		}
		renderText(renderer, config, font, ellipsize(font, line, r.W-36), col, r.X+18, rowY)
		rowY += lineH
	}
}
