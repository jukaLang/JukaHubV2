package main

// powermode.go — the named Power Mode selector (Battery Saver / Balanced /
// Performance). The modernization list asked for one setting that maps the
// three existing battery levers — LowPower, ReducedMotion and the adaptive
// frame cadence — onto a single value. The levers stay individually settable
// underneath: a named mode simply overrides what they would answer while it
// is selected, and Default hands control back to them.
//
// The name avoids "power profile" on purpose: that already names the CPU
// governor switch that surrounds game launches (powerprofile.go), and the two
// answer different questions — Power Mode is about the interface, the launch
// profile is about the emulated game.

import (
	"strings"
)

// Power Mode values. The empty string is Default: the LowPower and
// ReducedMotion toggles decide on their own, exactly as before this existed.
const (
	PowerModeDefault      = ""            // per-lever toggles decide
	PowerModeBatterySaver = "battery"     // motion off, slowest cadence
	PowerModeBalanced     = "balanced"    // smooth UI; idle cadence still slows the frame rate
	PowerModePerformance  = "performance" // full cadence even when idle, full motion
)

// powerModeOrder is also the order the quick settings row steps through from
// Default when pressed forward.
var powerModeOrder = []string{
	PowerModeDefault,
	PowerModeBatterySaver,
	PowerModeBalanced,
	PowerModePerformance,
}

// normalizePowerMode maps anything a config or the web builder could have
// written onto one of the four modes. Unknown values fall back to Default
// rather than threatening a mode nobody made.
func normalizePowerMode(s string) string {
	v := strings.TrimSpace(s)
	for _, mode := range powerModeOrder {
		if strings.EqualFold(v, mode) {
			return mode
		}
	}
	return PowerModeDefault
}

// powerModeLabel is the UI name of a mode, including the pill on the quick
// settings row, which only has room for one line.
func powerModeLabel(mode string) string {
	switch normalizePowerMode(mode) {
	case PowerModeBatterySaver:
		return "Battery Saver"
	case PowerModeBalanced:
		return "Balanced"
	case PowerModePerformance:
		return "Performance"
	}
	return "Default"
}

// powerModeIndex places a normalized mode back in the cycle order.
func powerModeIndex(mode string) int {
	mode = normalizePowerMode(mode)
	for i, m := range powerModeOrder {
		if m == mode {
			return i
		}
	}
	return 0
}

// currentPowerMode returns the mode a config is saved on, defaulting to
// Default for a nil config (tests, early startup).
func currentPowerMode(config *Config) string {
	if config == nil {
		return PowerModeDefault
	}
	return normalizePowerMode(config.Variables.PowerMode)
}

// effectiveLowPower is what every Low Power behaviour should read. A named
// mode answers for itself; otherwise the user's own toggle stands.
func effectiveLowPower(config *Config) bool {
	switch currentPowerMode(config) {
	case PowerModeBatterySaver:
		return true
	case PowerModeBalanced, PowerModePerformance:
		return false
	}
	if config == nil {
		return false
	}
	return config.Variables.LowPower
}

// effectiveReducedMotion is what every motion behaviour should read, with the
// same override rules as effectiveLowPower.
func effectiveReducedMotion(config *Config) bool {
	switch currentPowerMode(config) {
	case PowerModeBatterySaver:
		return true
	case PowerModeBalanced, PowerModePerformance:
		return false
	}
	if config == nil {
		return false
	}
	return config.Variables.ReducedMotion
}

// applyPowerMode feeds the frame-rate controller the current verdict. It is
// safe to call every frame (the main loop already does); the controller only
// latches a bool each time.
func applyPowerMode(config *Config) {
	if frameRateCtrl == nil {
		return
	}
	frameRateCtrl.SetLowPower(effectiveLowPower(config))
	frameRateCtrl.SetPerformance(currentPowerMode(config) == PowerModePerformance)
}

// cyclePowerModeTo saves mode, applies it live and reports the new name.
// The quick settings row calls this with the mode its Left/Right/press chose,
// so there is exactly one place where a mode becomes live state.
func cyclePowerModeTo(config *Config, mode string) string {
	mode = normalizePowerMode(mode)
	if config != nil {
		config.Variables.PowerMode = mode
		syncVariableOverrides(config)
	}
	applyPowerMode(config)
	return powerModeLabel(mode)
}

// nextPowerMode returns the mode dir step along the cycle from mode, wrapping
// in both directions.
func nextPowerMode(mode string, dir int) string {
	i := powerModeIndex(mode)
	n := len(powerModeOrder)
	i = ((i+dir)%n + n) % n
	return powerModeOrder[i]
}
