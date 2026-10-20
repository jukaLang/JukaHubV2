package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func withWeatherData(t *testing.T, days []WeatherDay) {
	t.Helper()
	wxMutex.Lock()
	prevReady, prevDays := weatherReady, weatherDaily
	weatherReady, weatherDaily = len(days) > 0, days
	wxMutex.Unlock()
	t.Cleanup(func() {
		wxMutex.Lock()
		weatherReady, weatherDaily = prevReady, prevDays
		wxMutex.Unlock()
	})
}

func sampleWeatherDays(n int) []WeatherDay {
	days := make([]WeatherDay, n)
	for i := range days {
		days[i] = WeatherDay{Date: "2026-10-19", TMax: 21, TMin: 12, Code: 1, Pop: 20}
	}
	return days
}

// The hi/lo chip in the status cluster must act as the Local weather button on
// both the home header and scene headers.
func TestWeatherChipIsClickable(t *testing.T) {
	withWeatherData(t, sampleWeatherDays(7))
	prevOpen := weatherPopoverOpen
	t.Cleanup(func() { weatherPopoverOpen = prevOpen })
	weatherPopoverOpen = false

	weatherStatusRect = sdl.Rect{X: 900, Y: 12, W: 90, H: 30}
	t.Cleanup(func() { weatherStatusRect = sdl.Rect{} })
	if !handleChromeBarClick(weatherStatusRect.X+2, weatherStatusRect.Y+2, &Config{}) {
		t.Fatal("click on the weather chip was not consumed")
	}
	if !weatherPopoverOpen {
		t.Fatal("clicking the weather chip did not open Local weather")
	}
	if !handleChromeBarClick(weatherStatusRect.X+2, weatherStatusRect.Y+2, &Config{}) || weatherPopoverOpen {
		t.Fatal("second click on the weather chip did not close Local weather")
	}
}

func TestWeatherPopoverFitsAndDismisses(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	prevOpen := weatherPopoverOpen
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		weatherPopoverOpen = prevOpen
		weatherStatusRect = sdl.Rect{}
	})
	screenWidth, screenHeight = probeW, probeH
	weatherStatusRect = sdl.Rect{X: 900, Y: 12, W: 90, H: 30}
	withWeatherData(t, sampleWeatherDays(7))

	weatherPopoverOpen = true
	r := weatherPopoverRect(&Config{})
	if r.W <= 0 || r.H <= 0 {
		t.Fatalf("weather panel rect is empty: %+v", r)
	}
	if r.X < 0 || r.Y < 0 || r.X+r.W > screenWidth || r.Y+r.H > screenHeight {
		t.Fatalf("weather panel %+v escapes the %dx%d screen", r, screenWidth, screenHeight)
	}
	if !weatherPopoverClick(r.X+5, r.Y+5, &Config{}) {
		t.Fatal("click inside Local weather was not consumed")
	}
	if !weatherPopoverOpen {
		t.Fatal("clicking inside Local weather dismissed it")
	}
	if !weatherPopoverClick(10, screenHeight-10, &Config{}) {
		t.Fatal("outside click was not consumed while Local weather was open")
	}
	if weatherPopoverOpen {
		t.Fatal("outside click did not dismiss Local weather")
	}
}

// The home header must record the measured weather chip in the focus graph and
// open the card when it is clicked.
func TestHomeWeatherChipOpensForecast(t *testing.T) {
	snapshotHomeGlobals(t)
	prevOpen := weatherPopoverOpen
	t.Cleanup(func() { weatherPopoverOpen = prevOpen })
	weatherPopoverOpen = false
	withWeatherData(t, sampleWeatherDays(7))

	renderHomeFrame(t, false)
	r := homeLayout.Items[HomeFocusWeather].Rect
	if r.W <= 0 || r.H <= 0 {
		t.Fatalf("home weather chip was not measured: %+v", r)
	}
	if weatherStatusRect != r {
		t.Fatalf("weatherStatusRect %+v does not match the home chip %+v", weatherStatusRect, r)
	}
	if !handleHomeMouseClick(r.X+r.W/2, r.Y+r.H/2, &Config{}) {
		t.Fatal("click on the home weather chip was not handled")
	}
	if !weatherPopoverOpen {
		t.Fatal("clicking the home weather chip did not open Local weather")
	}

	// The card is exclusive with the calendar popover.
	homeCalendarOpen = false
	toggleCalendarPopover()
	if !homeCalendarOpen || weatherPopoverOpen {
		t.Fatalf("calendarOpen=%v weatherOpen=%v, want the calendar open and weather closed", homeCalendarOpen, weatherPopoverOpen)
	}
	homeCalendarOpen = false
}

// A controller user opens the card with A on the header chip; from then on the
// card owns the pad, and B closes it without leaving the scene.
func TestWeatherPopoverConsumesController(t *testing.T) {
	prevOpen := weatherPopoverOpen
	t.Cleanup(func() { weatherPopoverOpen = prevOpen })
	weatherPopoverOpen = true

	nav := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN}
	if !ctrlWeatherPopover(nav) {
		t.Fatal("controller input was not consumed while Local weather was open")
	}
	if !weatherPopoverOpen {
		t.Fatal("a D-pad press dismissed Local weather")
	}

	back := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_B}
	if !ctrlWeatherPopover(back) || weatherPopoverOpen {
		t.Fatal("B did not close Local weather")
	}
}

func TestWeatherStatusPartDetection(t *testing.T) {
	for _, s := range []string{"21C / 12C", " -3C / -8C ", "69F / 53F"} {
		if !isWeatherStatusPart(s) {
			t.Errorf("%q was not recognised as the weather chip", s)
		}
	}
	for _, s := range []string{"HomeWiFi", "15:04", "80%", "", "12C", "69F"} {
		if isWeatherStatusPart(s) {
			t.Errorf("%q was mistaken for the weather chip", s)
		}
	}
}
