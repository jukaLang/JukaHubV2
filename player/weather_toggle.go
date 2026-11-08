package main

// weather_toggle.go — the Weather switch in Settings, wired all the way through.
//
// The switch used to write its value and stop there: nothing read
// WeatherEnabled, so the poller kept fetching every ten minutes and the
// forecast stayed in the header, on Home, on the dock and in the popover
// whatever the switch said. The state now has one reader-friendly form (an
// atomic the background poller can read), and turning it off really stops the
// work and takes the forecast off every screen; turning it on brings the saved
// forecast back at once and asks the poller for a fresh one.
//
// Hiding is done at the source rather than in every view: with the switch off
// the in-memory forecast is cleared and no fetch stores a new one, and every
// weather surface (header chip, Home chip, ambient dock, popover, Weather
// screen) already treats "no forecast" as "draw nothing" or explains itself.

import (
	"strings"
	"sync/atomic"
	"time"
)

// weatherEnabledKey is where the switch keeps the user's choice. Only this exact
// key counts as a choice: the typed weatherEnabled field and the capitalised
// WeatherEnabled mirror that both shipped configs carry were written long
// before anything honoured them (the device config even says false), so
// reading them would silently switch the forecast off on every handheld.
const weatherEnabledKey = "weatherEnabled"

var (
	// weatherEnabledFlag is the effective state, readable from the poller's
	// goroutine. It starts on, which is how every build before the switch
	// worked behaved.
	weatherEnabledFlag atomic.Bool
	// weatherKick wakes the poller when the switch turns back on, so the fresh
	// forecast does not wait for the next ten-minute tick.
	weatherKick = make(chan struct{}, 1)
)

func init() { weatherEnabledFlag.Store(true) }

// weatherEnabledFor reports what a config's switch says: on unless the user
// turned it off.
func weatherEnabledFor(config *Config) bool {
	if config == nil || config.Variables.Custom == nil {
		return true
	}
	v, ok := config.Variables.Custom[weatherEnabledKey]
	if !ok {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return !weatherSwitchOff(t)
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return true
}

// weatherSwitchOff reads the spellings a hand-edited or builder-written config
// uses for "off".
func weatherSwitchOff(s string) bool {
	switch s {
	case "false", "False", "FALSE", "0", "off", "Off", "no", "No":
		return true
	}
	return false
}

// weatherIsEnabled is the effective state for code without a config in hand:
// the poller, the fetch, the popover.
func weatherIsEnabled() bool { return weatherEnabledFlag.Load() }

// weatherSyncEnabled adopts a config's switch at startup and after a reload,
// and keeps the typed field in step so a toggle element drawn from it agrees.
func weatherSyncEnabled(config *Config) {
	on := weatherEnabledFor(config)
	if config != nil {
		config.Variables.WeatherEnabled = on
	}
	weatherApplyEnabled(on)
}

// weatherSetEnabled is the switch: it stores the choice under the exact key the
// toggle element uses (so the old Settings row and this one agree), saves it
// through the shared debounced writer and applies it live.
func weatherSetEnabled(config *Config, on bool) {
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom[weatherEnabledKey] = boolString(on)
		config.Variables.WeatherEnabled = on
		syncVariableOverrides(config)
	}
	weatherApplyEnabled(on)
}

// weatherApplyEnabled makes the state live. Off drops the forecast from memory
// (the cache file stays, so turning it back on is instant) and closes the card;
// on restores the cached forecast and kicks the poller.
func weatherApplyEnabled(on bool) {
	was := weatherEnabledFlag.Swap(on)
	if !on {
		wxMutex.Lock()
		weatherReady = false
		weatherDaily = nil
		wxMutex.Unlock()
		dismissWeatherPopover()
		return
	}
	if was {
		return
	}
	restoreWeatherCache(time.Now())
	select {
	case weatherKick <- struct{}{}:
	default:
	}
}

// weatherOffMessage is what the Weather screen and the popover say while the
// switch is off, instead of claiming the device is offline.
const weatherOffMessage = "Weather is off. Turn it on in Settings › General."

// weatherSetUnit stores the unit the Settings carousel chose: "" (automatic,
// from the location), "C" or "F". It stores it exactly the way
// cycleWeatherUnit does — the typed field, the custom mirrors dropped so they
// cannot override it, the marker that tells a deliberate Celsius from the old
// default — so the two can never disagree about what was saved.
func weatherSetUnit(config *Config, unit string) {
	if config == nil {
		return
	}
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "C":
		unit = "C"
	case "F":
		unit = "F"
	default:
		unit = ""
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.WeatherUnit = unit
	for key := range config.Variables.Custom {
		if strings.EqualFold(key, "weatherUnit") {
			delete(config.Variables.Custom, key)
		}
	}
	config.Variables.Custom["weatherUnitAuto"] = true
	saveUserConfigDebounced(userConfigFromVariables(config))
}
