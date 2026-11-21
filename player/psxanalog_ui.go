package main

// psxanalog_ui.go — where the PlayStation pad (psxanalog.go) is chosen:
// Settings → Controls, with "Game buttons for" on PlayStation, has the
// "Analog controller" row (Auto / Always / Never); the game options sheet has
// "Analog controller" for one PlayStation game (Auto / DualShock / Standard
// pad), stepping like its Buttons row (racontrols_ui.go).

func psxAnalogLabel(mode string) string {
	switch mode {
	case psxAnalogAlways:
		return "Always"
	case psxAnalogNever:
		return "Never"
	}
	return "Auto"
}

func psxAnalogOptions(*Config) []setOption {
	return []setOption{
		{Value: psxAnalogAuto, Label: "Auto", Note: "A DualShock for the games known to read the sticks; the rest keep the standard pad"},
		{Value: psxAnalogAlways, Label: "Always", Note: "A DualShock for every game; a few digital-only games may not react to the D-pad"},
		{Value: psxAnalogNever, Label: "Never", Note: "The standard pad for every game, as RetroArch has it"},
	}
}

// psxAnalogSettingsRow is the Controls page's row, shown while "Game buttons
// for" is on PlayStation.
func psxAnalogSettingsRow() setRow {
	return setRow{
		ID: "psxAnalog", Label: "Analog controller", Kind: setKindChoice,
		Visible: func(*Config) bool { return gameControlsScope == psxAnalogConsole },
		Detail: func(*Config) string {
			if !stickHardware() {
				return "PlayStation games: this handheld has no sticks, so Auto keeps the standard pad · Always is for a Bluetooth pad"
			}
			return "PlayStation games: a DualShock with working sticks for the games that support them · a game can choose its own"
		},
		Options: psxAnalogOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(psxAnalogOptions(c), psxAnalogSetting(c)) },
		Choose: func(c *Config, value string) {
			value = normalisePSXAnalog(value)
			if value == "" {
				value = psxAnalogAuto
			}
			setStoreString(c, psxAnalogKey(), value)
			setToast("PlayStation analog controller: " + psxAnalogLabel(value))
		},
	}
}

// ── Game options sheet ───────────────────────────────────────────────────

// psxAnalogGameHasRow reports the sheet's Analog controller row: PlayStation
// games only.
func psxAnalogGameHasRow(game libraryGame) bool { return psxAnalogIsGame(game) }

// psxAnalogSheetValue is the row's right-hand value: the game's own pad, or
// "Auto · " and what Auto gives this game.
func psxAnalogSheetValue(game libraryGame) string {
	switch emuExtrasFor(game.Path).Analog {
	case psxAnalogGameDualShock:
		return "DualShock"
	case psxAnalogGameStandard:
		return "Standard pad"
	}
	d := psxAnalogConsoleDecide(appConfig, game, stickHardware())
	if d.Pad == psxAnalogPadDualShock {
		return "Auto · DualShock"
	}
	return "Auto · Standard pad"
}

// psxAnalogSheetHint is the row's second line.
func psxAnalogSheetHint(game libraryGame) string {
	if _, known := psxAnalogKnownTitle(game); known {
		return "This game reads the analog sticks · DualShock lets it, Standard pad is D-pad only"
	}
	return "DualShock for a game that reads the sticks · Standard pad for digital-only games"
}
