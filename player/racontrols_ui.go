package main

// racontrols_ui.go — where the Buttons and Turbo choices (racontrols.go) are
// made.
//
// Settings → Controls has four rows, built like Settings → Emulation's
// (ratuning_settings.go): "Game buttons for" picks what the next two change —
// All consoles, or one console that then gets its own Buttons and Turbo,
// starting at "Default · …" (the All consoles choice, named). The picked
// console is a view for this run of the app, not a setting. Turbo speed is
// one choice for every console. Every row stores its value in the custom map
// through setStoreString.
//
// The game options sheet (game_options_ui.go) has a Buttons and a Turbo row
// for one game: Left/Right step through "Default · <the console's choice>"
// and the choices, A steps and wraps. When the last launch of the game could
// not apply them (the user's own remap file was there, or an older RetroArch
// whose pad buttons JukaHub cannot read), the row's line says so.

import "strings"

// gameControlsScope is the console the scoped rows edit; "" is all consoles.
// Main thread.
var gameControlsScope string

func gameControlsScopeName() string {
	if gameControlsScope == "" {
		return "All consoles"
	}
	return gameControlsScope
}

// gameControlsScopedKey is the key a scoped row writes.
func gameControlsScopedKey(globalKey, what string) string {
	if gameControlsScope == "" {
		return globalKey
	}
	return gameControlsConsoleKey(gameControlsScope, what)
}

func gameControlsToast(what, label string) {
	if gameControlsScope != "" {
		what = gameControlsScope + " " + strings.ToLower(what)
	}
	setToast(what + ": " + label)
}

// ── Settings → Controls ──────────────────────────────────────────────────

func gameControlsConsoleOptions(c *Config) []setOption {
	out := make([]setOption, 0, len(raTuneSystems)+1)
	out = append(out, setOption{Value: "", Label: "All consoles", Note: "The buttons every console starts with"})
	for _, s := range raTuneSystems {
		note := ""
		if gameButtonsConsoleOwn(c, s.Name) != "" || gameTurboConsoleOwn(c, s.Name) != "" ||
			gameDpadConsoleOwn(c, s.Name) != "" || raSticksLeftConsoleOwn(c, s.Name) != "" {
			note = "Has its own buttons"
		}
		out = append(out, setOption{Value: s.Name, Label: s.Name, Note: note})
	}
	return out
}

func gameButtonsOptions(c *Config) []setOption {
	out := make([]setOption, 0, len(gameButtonsModes)+1)
	if gameControlsScope != "" {
		out = append(out, setOption{Value: "", Label: "Default · " + gameButtonsLabel(gameButtonsAll(c)), Note: raTuneDefaultNote})
	}
	for _, m := range gameButtonsModes {
		out = append(out, setOption{Value: m, Label: gameButtonsLabel(m), Note: gameButtonsNote(m)})
	}
	return out
}

func gameButtonsStored(c *Config) string {
	if gameControlsScope == "" {
		return gameButtonsAll(c)
	}
	return gameButtonsConsoleOwn(c, gameControlsScope)
}

func gameTurboOptions(c *Config) []setOption {
	out := make([]setOption, 0, len(gameTurboModes)+1)
	if gameControlsScope != "" {
		out = append(out, setOption{Value: "", Label: "Default · " + gameTurboLabel(gameTurboAll(c)), Note: raTuneDefaultNote})
	}
	for _, m := range gameTurboModes {
		out = append(out, setOption{Value: m, Label: gameTurboLabel(m), Note: gameTurboNote(m)})
	}
	return out
}

func gameTurboStored(c *Config) string {
	if gameControlsScope == "" {
		return gameTurboAll(c)
	}
	return gameTurboConsoleOwn(c, gameControlsScope)
}

func gameTurboSpeedOptions(*Config) []setOption {
	return []setOption{
		{Value: gameTurboFast, Label: "Fast", Note: "About 15 presses a second"},
		{Value: gameTurboNormal, Label: "Normal", Note: "About 10 presses a second"},
		{Value: gameTurboSlow, Label: "Slow", Note: "About 6 presses a second"},
	}
}

// gameControlsSettingsRows are the Controls page's rows for RetroArch games.
func gameControlsSettingsRows() []setRow {
	return []setRow{
		{
			ID: "gameControlsConsole", Label: "Game buttons for", Kind: setKindChoice,
			Detail: func(*Config) string {
				if gameControlsScope == "" {
					return "RetroArch games · pick a console to give it its own buttons and turbo"
				}
				if stickHardware() { // + Left stick as D-pad (rasticks.go)
					return "The next four rows change " + gameControlsScope + " games only"
				}
				return "The rows below change " + gameControlsScope + " games only"
			},
			Options:    gameControlsConsoleOptions,
			Sheet:      gameControlsConsoleOptions,
			SheetValue: func(*Config) string { return gameControlsScope },
			SheetTitle: "Game buttons for",
			Current:    func(c *Config) int { return raTuneOptionIndex(gameControlsConsoleOptions(c), gameControlsScope) },
			Value:      func(*Config) string { return gameControlsScopeName() },
			Choose: func(_ *Config, value string) {
				gameControlsScope = ""
				for _, s := range raTuneSystems {
					if s.Name == value {
						gameControlsScope = s.Name
					}
				}
			},
		},
		{
			ID: "gameControlsButtons", Label: "Buttons", Kind: setKindChoice,
			Detail: func(*Config) string {
				if gameControlsScope == "" {
					return "What A, B, X and Y do in RetroArch games · a game can choose its own"
				}
				return gameControlsScope + " games only"
			},
			Options: gameButtonsOptions,
			Current: func(c *Config) int { return raTuneOptionIndex(gameButtonsOptions(c), gameButtonsStored(c)) },
			Choose: func(c *Config, value string) {
				value = normaliseGameButtons(value)
				if gameControlsScope == "" && value == "" {
					value = gameButtonsDefault
				}
				setStoreString(c, gameControlsScopedKey(gameControlsButtonsKey, "buttons"), value)
				label := gameButtonsLabel(value)
				if value == "" {
					label = "Default"
				}
				gameControlsToast("Buttons", label)
			},
		},
		{
			ID: "gameControlsTurbo", Label: "Turbo", Kind: setKindChoice,
			Detail: func(*Config) string {
				if gameControlsScope == "" {
					return "A button that fires by itself while you hold it, in RetroArch games"
				}
				return gameControlsScope + " games only"
			},
			Options: gameTurboOptions,
			Current: func(c *Config) int { return raTuneOptionIndex(gameTurboOptions(c), gameTurboStored(c)) },
			Choose: func(c *Config, value string) {
				value = normaliseGameTurbo(value)
				if gameControlsScope == "" && value == "" {
					value = gameTurboOff
				}
				setStoreString(c, gameControlsScopedKey(gameControlsTurboKey, "turbo"), value)
				label := gameTurboLabel(value)
				if value == "" {
					label = "Default"
				}
				gameControlsToast("Turbo", label)
			},
		},
		// The D-pad as the left stick, per console (racontrols_dpad.go).
		gameDpadStickSettingsRow(),
		// The left stick as the D-pad, per console; stick handhelds (rasticks.go).
		raSticksLeftRow(),
		{
			ID: "gameControlsTurboSpeed", Label: "Turbo speed", Kind: setKindChoice, Clamp: true,
			Detail:  setStatic("How fast a turbo button fires, in every console"),
			Options: gameTurboSpeedOptions,
			Current: func(c *Config) int {
				return raTuneOptionIndex(gameTurboSpeedOptions(c), gameTurboSpeedSetting(c))
			},
			Choose: func(c *Config, value string) {
				setStoreString(c, gameControlsTurboSpeedKey, value)
				setToast("Turbo speed: " + gameTurboSpeedLabel(value))
			},
		},
		// PlayStation only: DualShock for analog games (psxanalog_ui.go).
		psxAnalogSettingsRow(),
	}
}

// ── Game options sheet ───────────────────────────────────────────────────

// gameControlsStep moves along a per-game ladder ("" first, then choices).
// Left/Right stop at the ends; A (wrap) goes round.
func gameControlsStep(ladder []string, cur string, delta int, wrap bool) string {
	idx := 0
	for i, m := range ladder {
		if m == cur {
			idx = i
		}
	}
	idx += delta
	switch {
	case wrap && idx >= len(ladder):
		idx = 0
	case wrap && idx < 0:
		idx = len(ladder) - 1
	case idx < 0:
		idx = 0
	case idx >= len(ladder):
		idx = len(ladder) - 1
	}
	return ladder[idx]
}

// gameControlsSheetStep changes the sheet's game's Buttons (turbo false) or
// Turbo; it reports whether anything changed.
func gameControlsSheetStep(game libraryGame, turbo bool, delta int, wrap bool) bool {
	own := gameControlsFor(game.Path)
	if turbo {
		next := gameControlsStep(append([]string{""}, gameTurboModes...), own.Turbo, delta, wrap)
		if next == own.Turbo {
			return false
		}
		own.Turbo = next
	} else {
		next := gameControlsStep(append([]string{""}, gameButtonsModes...), own.Buttons, delta, wrap)
		if next == own.Buttons {
			return false
		}
		own.Buttons = next
	}
	gameControlsSet(game.Path, own)
	return true
}

// gameButtonsSheetValue and gameTurboSheetValue are the rows' right-hand
// values.
func gameButtonsSheetValue(game libraryGame) string {
	if own := gameControlsFor(game.Path).Buttons; own != "" {
		return gameButtonsLabel(own)
	}
	return "Default · " + gameButtonsLabel(gameButtonsForConsole(appConfig, game.System))
}

func gameTurboSheetValue(game libraryGame) string {
	if own := gameControlsFor(game.Path).Turbo; own != "" {
		return gameTurboLabel(own)
	}
	return "Default · " + gameTurboLabel(gameTurboForConsole(appConfig, game.System))
}

// gameControlsSheetOwn reports a row holding the game's own choice (the
// sheet draws it in the focus colour).
func gameControlsSheetOwn(game libraryGame, turbo bool) bool {
	own := gameControlsFor(game.Path)
	if turbo {
		return own.Turbo != ""
	}
	return own.Buttons != ""
}

// gameControlsSheetHint is a row's second line: what it does, or what the
// last launch could not apply.
func gameControlsSheetHint(game libraryGame, turbo bool) string {
	if note := gameControlsNote(game.Path); note != "" {
		if turbo == strings.HasPrefix(note, "Turbo") || strings.Contains(note, " · ") || strings.HasPrefix(note, "Not applied") {
			return note
		}
	}
	if turbo {
		return "A button that fires by itself while held · RetroArch games"
	}
	return "Swap A and B or X and Y · RetroArch games, this game only"
}
