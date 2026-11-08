package main

// weather_toggle_test.go — the Weather switch in Settings does what it says.
// It used to write a value nothing read: the poller kept fetching and the
// forecast stayed on every screen. Off now stops the work and takes the
// forecast off screen, on brings the saved forecast straight back and wakes the
// poller, and only the user's own choice counts as "off" (the shipped configs'
// old mirrors say false on the handheld and were never honoured).

import (
	"testing"
	"time"
)

// weatherTestSwitch saves the process-wide switch and the popover, and puts
// them back afterwards, draining any wake-up the test left for the poller.
func weatherTestSwitch(t *testing.T) {
	t.Helper()
	prev := weatherEnabledFlag.Load()
	prevPopover := weatherPopoverOpen
	t.Cleanup(func() {
		weatherEnabledFlag.Store(prev)
		weatherPopoverOpen = prevPopover
		select {
		case <-weatherKick:
		default:
		}
	})
}

func TestWeatherSwitchReadsOnlyTheUsersOwnKey(t *testing.T) {
	cases := []struct {
		name   string
		custom map[string]interface{}
		typed  bool
		want   bool
	}{
		{"nothing stored", nil, false, true},
		{"only the shipped mirror says off", map[string]interface{}{"WeatherEnabled": false}, false, true},
		{"switched off", map[string]interface{}{weatherEnabledKey: false}, true, false},
		{"switched off by a toggle element", map[string]interface{}{weatherEnabledKey: "false"}, true, false},
		{"off in a hand edit", map[string]interface{}{weatherEnabledKey: "off"}, true, false},
		{"a zero from JSON", map[string]interface{}{weatherEnabledKey: float64(0)}, true, false},
		{"switched back on", map[string]interface{}{weatherEnabledKey: "true"}, false, true},
	}
	for _, c := range cases {
		config := &Config{}
		config.Variables.Custom = c.custom
		config.Variables.WeatherEnabled = c.typed
		if got := weatherEnabledFor(config); got != c.want {
			t.Errorf("%s: weatherEnabledFor = %v, want %v", c.name, got, c.want)
		}
	}
	if !weatherEnabledFor(nil) {
		t.Error("with no config at all the forecast must stay on")
	}
}

// TestWeatherSwitchOffClearsAndOnRestores drives the live state: off drops the
// forecast and closes the card, and nothing brings it back while off; on
// restores the saved forecast at once and wakes the poller exactly once.
func TestWeatherSwitchOffClearsAndOnRestores(t *testing.T) {
	weatherTestSwitch(t)
	path := withWeatherCacheFile(t)
	now := time.Now()
	if err := saveWeatherCache(path, cacheSampleDays(now), now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("save the forecast: %v", err)
	}
	weatherEnabledFlag.Store(true)
	wxMutex.Lock()
	weatherDaily, weatherReady = cacheSampleDays(now), true
	wxMutex.Unlock()
	weatherPopoverOpen = true

	weatherApplyEnabled(false)
	if weatherIsEnabled() {
		t.Fatal("the switch is off but the poller still sees it on")
	}
	wxMutex.Lock()
	ready, days := weatherReady, len(weatherDaily)
	wxMutex.Unlock()
	if ready || days != 0 {
		t.Fatalf("off left the forecast on screen (ready %v, %d days)", ready, days)
	}
	if weatherPopoverOpen {
		t.Fatal("off left the forecast card open")
	}
	if restoreWeatherCache(now) {
		t.Fatal("the saved forecast came back while the switch is off")
	}

	weatherApplyEnabled(true)
	if !weatherIsEnabled() {
		t.Fatal("the switch is on but the poller still sees it off")
	}
	wxMutex.Lock()
	ready, days = weatherReady, len(weatherDaily)
	wxMutex.Unlock()
	if !ready || days == 0 {
		t.Fatal("on did not bring the saved forecast back")
	}
	select {
	case <-weatherKick:
	default:
		t.Fatal("on did not wake the poller for a fresh forecast")
	}
	// Already on: nothing to restore and no second wake-up.
	weatherApplyEnabled(true)
	select {
	case <-weatherKick:
		t.Fatal("a switch that was already on woke the poller again")
	default:
	}
}

// TestWeatherSetEnabledStoresTheExactKey is the Settings row's path: the choice
// lands under the key the switch reads, the typed field follows, and the live
// state flips with it.
func TestWeatherSetEnabledStoresTheExactKey(t *testing.T) {
	weatherTestSwitch(t)
	withWeatherCacheFile(t)
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"WeatherEnabled": true}

	weatherSetEnabled(config, false)
	if got := config.Variables.Custom[weatherEnabledKey]; got != "false" {
		t.Fatalf("stored %v under %q, want \"false\"", got, weatherEnabledKey)
	}
	if config.Variables.WeatherEnabled || weatherEnabledFor(config) || weatherIsEnabled() {
		t.Fatal("the switch was turned off but something still reads it as on")
	}

	weatherSetEnabled(config, true)
	if !config.Variables.WeatherEnabled || !weatherEnabledFor(config) || !weatherIsEnabled() {
		t.Fatal("the switch was turned back on but something still reads it as off")
	}
}

// TestWeatherSetUnitStoresWhatTheCarouselShows: each stop of the Settings
// carousel round-trips, and a stale mirror cannot override the choice.
func TestWeatherSetUnitStoresWhatTheCarouselShows(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"WeatherUnit": "F", "weatherunit": "F"}
	for _, unit := range []string{"C", "F", ""} {
		weatherSetUnit(config, unit)
		if config.Variables.WeatherUnit != unit {
			t.Fatalf("chose %q, stored %q", unit, config.Variables.WeatherUnit)
		}
		for key := range config.Variables.Custom {
			if key == "WeatherUnit" || key == "weatherunit" || key == "weatherUnit" {
				t.Fatalf("the mirror %q is still there to override %q", key, unit)
			}
		}
		want := 0
		switch unit {
		case "C":
			want = 1
		case "F":
			want = 2
		}
		if got := setWeatherUnitCurrent(config); got != want {
			t.Fatalf("after choosing %q the carousel shows stop %d, want %d", unit, got, want)
		}
	}
	weatherSetUnit(config, "kelvin")
	if config.Variables.WeatherUnit != "" {
		t.Fatalf("an unknown unit was stored as %q, want automatic", config.Variables.WeatherUnit)
	}
}
