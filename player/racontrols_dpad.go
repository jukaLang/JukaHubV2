package main

// racontrols_dpad.go — "D-pad as stick" for RetroArch games started from the
// library (Settings → Controls, beside Buttons and Turbo): the D-pad drives
// the left analog stick.
//
// A Brick has no sticks, and the N64, the PSP and the Dreamcast have games
// that only move with one. RetroArch's own "Analog to Digital Type"
// (input_playerN_analog_dpad_mode: 0 none, 1 left, 2 right, 3/4 forced) is
// the other direction, the stick driving the D-pad, so it cannot help a
// handheld without one. A remap can: each D-pad direction handed to the core
// as a left-stick direction. RetroArch numbers the stick's directions after
// the 16 RetroPad buttons (RARCH_FIRST_CUSTOM_BIND = 16 in input_defines.h):
// 16 left X+ (right), 17 left X- (left), 18 left Y+ (down), 19 left Y- (up),
// and a remap file's input_player1_btn_up = "19" makes Up push the stick
// fully up (input_remapping.c turns a button remapped to an axis into that
// axis at full tilt). The core no longer sees those D-pad presses, and the
// stick is either at rest or fully over: no slow walk.
//
// It goes into the same one-launch remap file as the Buttons choice
// (racontrols_remap.go), merged in prepareGameControls under the game's
// control layout, whose buttons win: written for that launch only, never
// over a remap the user saved in RetroArch, removed when RetroArch exits.
//
// All consoles is Auto (the default: on for the N64, the PSP and the
// Dreamcast on a handheld without sticks — the Brick and the Hammer — and off
// everywhere else) or Off. A console picked on "Game buttons for" gets
// Default (All consoles' answer for it), On or Off. Standalone emulators and
// NextUI paks are not RetroArch and keep their own controls (prepareGameControls
// stops before them).

import "strings"

const (
	// gameDpadStickKey is the all-consoles choice; a console's own is
	// gameControls.<console slug>.dpadstick (gameControlsConsoleKey).
	gameDpadStickKey  = "gameControls.dpadStick"
	gameDpadStickWhat = "dpadstick"

	gameDpadAuto = "auto"
	gameDpadOn   = "on"
	gameDpadOff  = "off"

	// RetroArch's left-stick directions as remap targets (input_defines.h).
	raRemapLeftXPlus  = 16
	raRemapLeftXMinus = 17
	raRemapLeftYPlus  = 18
	raRemapLeftYMinus = 19
)

// gameDpadAnalogConsoles are the consoles Auto turns the D-pad into a stick
// for, by their Settings names (raTuneSystems).
var gameDpadAnalogConsoles = map[string]bool{
	"Nintendo 64":          true,
	"PlayStation Portable": true,
	"Dreamcast":            true,
}

// normaliseGameDpad keeps a known choice, "" for anything else.
func normaliseGameDpad(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case gameDpadAuto, gameDpadOn, gameDpadOff:
		return v
	}
	return ""
}

// gameDpadAll is the all-consoles choice: Auto unless turned Off.
func gameDpadAll(c *Config) string {
	if normaliseGameDpad(gameControlsRaw(c, gameDpadStickKey)) == gameDpadOff {
		return gameDpadOff
	}
	return gameDpadAuto
}

// gameDpadConsoleOwn is a console's own choice, On or Off; "" follows All
// consoles.
func gameDpadConsoleOwn(c *Config, console string) string {
	switch v := normaliseGameDpad(gameControlsRaw(c, gameControlsConsoleKey(console, gameDpadStickWhat))); v {
	case gameDpadOn, gameDpadOff:
		return v
	}
	return ""
}

// gameDpadAutoOn is Auto's answer for a console on this device.
func gameDpadAutoOn(console string) bool {
	return gameDpadAnalogConsoles[console] && !stickHardware()
}

// gameDpadDefaultOn is what a console with no choice of its own gets.
func gameDpadDefaultOn(c *Config, console string) bool {
	return gameDpadAll(c) != gameDpadOff && gameDpadAutoOn(console)
}

// gameDpadStickFor reports whether a launch of a system's game turns the
// D-pad into the left stick.
func gameDpadStickFor(c *Config, system string) bool {
	console := gameControlsConsole(system)
	switch gameDpadConsoleOwn(c, console) {
	case gameDpadOn:
		return true
	case gameDpadOff:
		return false
	}
	return gameDpadDefaultOn(c, console)
}

// raDpadStickRemap is the D-pad handed to the core as the left stick.
func raDpadStickRemap() map[string]int {
	return map[string]int{
		"up":    raRemapLeftYMinus,
		"down":  raRemapLeftYPlus,
		"left":  raRemapLeftXMinus,
		"right": raRemapLeftXPlus,
	}
}

// raDpadStickMerge adds the D-pad-as-stick lines to a launch's remap (the
// Buttons preset, nil for Normal) when the game's console wants them; the
// control layout is laid over the result, so its buttons win. The map passed
// in is not changed.
func raDpadStickMerge(remap map[string]int, game libraryGame) map[string]int {
	if !gameDpadStickFor(appConfig, game.System) {
		return remap
	}
	out := make(map[string]int, len(remap)+4)
	for k, v := range remap {
		out[k] = v
	}
	for k, v := range raDpadStickRemap() {
		if _, set := out[k]; !set {
			out[k] = v
		}
	}
	return out
}

// raDpadStickLabel names the Buttons choice with the D-pad, for the log.
func raDpadStickLabel(mode string, game libraryGame) string {
	if !gameDpadStickFor(appConfig, game.System) {
		return mode
	}
	if mode == "" || mode == gameButtonsDefault {
		return "D-pad as stick"
	}
	return mode + " + D-pad as stick"
}

// ── Settings → Controls ──────────────────────────────────────────────────

func gameDpadOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// gameDpadStickOptions are the row's stops for the picked console (or All
// consoles: gameControlsScope).
func gameDpadStickOptions(c *Config) []setOption {
	if gameControlsScope == "" {
		return []setOption{
			{Value: gameDpadAuto, Label: "Auto", Note: "On for N64, PSP and Dreamcast on a handheld without sticks"},
			{Value: gameDpadOff, Label: "Off", Note: "The D-pad is the D-pad in every game"},
		}
	}
	return []setOption{
		{Value: "", Label: "Default · " + gameDpadOnOff(gameDpadDefaultOn(c, gameControlsScope)), Note: raTuneDefaultNote},
		{Value: gameDpadOn, Label: "On", Note: "The D-pad moves the left stick"},
		{Value: gameDpadOff, Label: "Off", Note: "The D-pad is the D-pad"},
	}
}

// gameDpadStickStored is the choice the row shows.
func gameDpadStickStored(c *Config) string {
	if gameControlsScope == "" {
		return gameDpadAll(c)
	}
	return gameDpadConsoleOwn(c, gameControlsScope)
}

// gameDpadStickSettingsRow is the row under Turbo (racontrols_ui.go).
func gameDpadStickSettingsRow() setRow {
	return setRow{
		ID: "gameControlsDpadStick", Label: "D-pad as stick", Kind: setKindChoice,
		Detail: func(c *Config) string {
			if gameControlsScope == "" {
				return "For games that only move with the analog stick, in RetroArch"
			}
			return gameControlsScope + " games only · the D-pad moves the left stick"
		},
		Options: gameDpadStickOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(gameDpadStickOptions(c), gameDpadStickStored(c)) },
		Choose: func(c *Config, value string) {
			value = normaliseGameDpad(value)
			if gameControlsScope == "" && value != gameDpadOff {
				value = gameDpadAuto
			}
			setStoreString(c, gameControlsScopedKey(gameDpadStickKey, gameDpadStickWhat), value)
			label := "Default"
			switch value {
			case gameDpadAuto:
				label = "Auto"
			case gameDpadOn:
				label = "On"
			case gameDpadOff:
				label = "Off"
			}
			gameControlsToast("D-pad as stick", label)
		},
	}
}
