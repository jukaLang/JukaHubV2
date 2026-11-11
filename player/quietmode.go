package main

// quietmode.go — Do Not Disturb for JukaHub's own feedback.
//
// Quiet Mode holds toasts, UI sounds and chimes until it is switched off. The
// alarm ringtone is not a UI sound and keeps ringing; its dismiss/snooze
// messages bypass the toast check so an alarm's outcome is still visible.
// The switch lives in Quick Settings and is stored in jukauser.json; like the
// other UI preferences it is read into a live value once a second.

import "sync/atomic"

const quietModeKey = "quietMode"

var quietModeActive atomic.Bool

// quietModeFromConfig reads the persisted switch. Missing or unreadable values
// mean quiet mode is off.
func quietModeFromConfig(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := customValue(config.Variables.Custom, quietModeKey); ok {
		return bootSfxParseBool(v, false)
	}
	return false
}

// quietModeBind refreshes the live value from config. It rides bootSfxBind,
// which alarmCheck calls once a second, so a config edit outside Quick Settings
// takes effect without a restart.
func quietModeBind(config *Config) {
	quietModeActive.Store(quietModeFromConfig(config))
}

// quietModeSet applies the switch and writes it into the live config. The
// caller saves the config through its usual path (Quick Settings does this in
// qsPersist).
func quietModeSet(config *Config, on bool) {
	quietModeActive.Store(on)
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[quietModeKey] = boolString(on)
}

// quietModeOn reports whether user feedback is currently held.
func quietModeOn() bool { return quietModeActive.Load() }
