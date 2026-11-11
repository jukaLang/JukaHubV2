package main

// a11y_settings.go — Settings → Accessibility.
//
// The Accessibility panel (a11y.go) used to be the only way in: a modal opened
// from Display & theme or the Start menu, so a player browsing Settings never
// found text size, mono audio or a remapped button. This page lists every one of
// those settings as an ordinary row in its own rail section, which also makes
// them searchable and deep-linkable like every other setting.
//
// Both pages call the setters in a11y.go, so a value is still clamped, applied
// to live state and announced in exactly one place; a row here only saves the
// config once its setter reports a change.

import "strconv"

// a11ySettingsSection is the rail section's id, used by deep links and search.
const a11ySettingsSection = "accessibility"

// a11yButtonNone is a button row's Disabled stop. It matches the keymap's saved
// spelling (a11yParseKeymap reads "none").
const a11yButtonNone = "none"

// a11yOpenSettings opens Settings on the Accessibility page. The Start menu's
// Accessibility entry lands here; the panel itself stays on Display & theme.
func a11yOpenSettings(config *Config) {
	if config == nil {
		return
	}
	section := setSectionIndex(a11ySettingsSection)
	if section < 0 {
		return
	}
	if setIsHubScene(setHubSceneName(config)) {
		// Already in Settings: switch sections in place.
		setHubSelectSection(config, section, false)
		setHubEnterPane(false)
		return
	}
	if sceneIndexExact(config, "Settings") < 0 {
		setToast("That screen is not part of this setup")
		return
	}
	setHub.lastSection = section
	setHub.deepLink = true
	setOpenScene("Settings")(config)
}

// a11ySettingsRows is the Accessibility page: the panel's catalogue as Settings
// rows, in the panel's order.
func a11ySettingsRows() []setRow {
	rows := []setRow{
		a11yScaleSettingRow(),
		{
			ID: "contrast", Label: "High contrast", Kind: setKindToggle,
			Detail: setStatic("Stronger borders and brighter text on every screen"),
			Get:    func(*Config) bool { return a11y.highContrast },
			Set:    setHighContrast,
		},
		{
			ID: "reduceTransparency", Label: "Reduce transparency", Kind: setKindToggle,
			Detail: setStatic("Makes every glass tile, row, sheet and chip opaque"),
			Get:    func(*Config) bool { return a11y.reduceTransparency },
			Set:    setReduceTransparency,
		},
		{
			ID: "increaseContrast", Label: "Increase contrast", Kind: setKindToggle,
			Detail: setStatic("Stronger edges, wider focus rings, 7:1 text"),
			Get:    func(*Config) bool { return a11y.increaseContrast },
			Set:    setIncreaseContrast,
		},
		{
			ID: "boldText", Label: "Bold text", Kind: setKindToggle,
			Detail: setStatic("Inter SemiBold in every menu, list and Home label"),
			Get:    func(*Config) bool { return a11y.boldText },
			Set:    setBoldText,
		},
		a11yToggleRow("legibleFont", "Legible reading font",
			"Atkinson Hyperlegible, Lexend, Verdana, Tahoma or Segoe UI from this device, whichever is installed; bundled Inter when none is",
			func() bool { return a11y.legibleFont }, a11ySetLegibleFont),
		a11yToggleRow("magnifier", "Magnifier",
			"A 2× or 3× lens beside the focused control, on every screen",
			func() bool { return a11y.magnifier }, a11ySetMagnifier),
		a11yMagnifierZoomSettingRow(),
		a11yColorblindSettingRow(),
		a11yToggleRow("monoAudio", "Mono audio",
			"Downmixes video and music to one channel for a single speaker or one-ear listening",
			func() bool { return a11y.monoAudio }, a11ySetMonoAudio),
		a11yNumberRow("audioBalance", "Audio balance",
			"Moves stereo video and music between the left and right channels; interface sounds stay centred",
			-100, 100, 10, "%", func() int { return a11y.audioBalance }, a11ySetAudioBalance),
		a11yToggleRow("captionsAlways", "Captions always on",
			"Each video starts with its subtitles enabled; CC still turns them off for the current video",
			func() bool { return a11y.captionsAlways }, a11ySetCaptionsAlways),
		a11yCaptionSizeSettingRow(),
		a11yToggleRow("captionBackground", "Caption background",
			"Draws a dark panel behind cues; off keeps only the text over the picture",
			func() bool { return a11y.captionBackground }, a11ySetCaptionBackground),
		a11yNumberRow("longPress", "Long press",
			"How long a letter or the globe must be held before its alternate action opens",
			250, 1000, 50, " ms", func() int { return a11y.longPressMs }, a11ySetLongPress),
		a11yNumberRow("repeatDelay", "Repeat delay",
			"How long a direction waits before it starts repeating",
			100, 800, 20, " ms", func() int { return a11y.repeatDelayMs }, a11ySetRepeatDelay),
		a11yNumberRow("repeatRate", "Repeat speed",
			"The gap between repeated direction steps; larger is slower",
			30, 200, 10, " ms", func() int { return a11y.repeatRateMs }, a11ySetRepeatRate),
		a11yNumberRow("toastDuration", "Toast duration",
			"How long a message stays on screen before it fades",
			50, 200, 25, "%", func() int { return a11y.toastPercent }, a11ySetToastPercent),
		a11yToggleRow("stickyChords", "Sticky chords",
			"Leaves the one-handed chord armed after a direction, so several face buttons can be pressed in sequence",
			func() bool { return a11y.stickyChords }, a11ySetStickyChords),
		a11yLayoutSettingRow(),
	}
	for _, button := range a11yPanelButtons {
		rows = append(rows, a11yButtonSettingRow(button))
	}
	return append(rows,
		a11ySelectSettingRow(),
		a11yToggleRow("oneHanded", "One-handed mode",
			"Tap the chord button, then a direction, to press A, B, X or Y with one thumb; tap again to cancel",
			func() bool { return a11y.oneHanded }, a11ySetOneHanded),
		a11yChordSettingRow(),
		a11ySlowRepeatSettingRow(),
		a11ySwitchSettingRow(),
		a11ySwitchIntervalSettingRow(),
		setPowerModeRow(),
		setVibrationLevelRow(),
		setRow{
			ID: "a11yReset", Label: "Reset to defaults", Kind: setKindAction,
			Detail: setStatic("Back to 100% text, default colours, centred audio and standard buttons"),
			Value:  setStatic("Reset"),
			Open: func(c *Config) {
				a11yResetDefaults(c)
				a11ySave(c)
			},
		},
	)
}

// a11yScaleSettingRow is the text-size carousel, the same steps the panel
// walks.
func a11yScaleSettingRow() setRow {
	return setRow{
		ID: "textSize", Label: "Text size", Kind: setKindChoice,
		Detail:  setStatic("Scales every menu, overlay and keyboard label"),
		Options: setTextSizeOptions,
		Current: func(*Config) int { return a11yScaleIndex() },
		Choose: func(c *Config, value string) {
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return
			}
			if a11ySetScale(f) {
				a11ySave(c)
			}
		},
	}
}

// a11yToggleRow adapts one boolean setter to a Settings switch.
func a11yToggleRow(id, label, detail string, get func() bool, set func(bool) bool) setRow {
	return setRow{
		ID: id, Label: label, Kind: setKindToggle,
		Detail: setStatic(detail),
		Get:    func(*Config) bool { return get() },
		Set: func(c *Config, on bool) {
			if set(on) {
				a11ySave(c)
			}
		},
	}
}

// a11yNumberOptions are the stops of a stepping row: lo, lo+step, … hi.
func a11yNumberOptions(lo, hi, step int, unit string) []setOption {
	if step <= 0 || hi < lo {
		return nil
	}
	out := make([]setOption, 0, (hi-lo)/step+1)
	for v := lo; v <= hi; v += step {
		out = append(out, setOption{Value: strconv.Itoa(v), Label: strconv.Itoa(v) + unit})
	}
	return out
}

// a11yNumberIndex is the stop closest to value, or -1 when there is none.
func a11yNumberIndex(lo, hi, step, value int) int {
	if step <= 0 || value < lo || value > hi {
		return -1
	}
	return (value - lo + step/2) / step
}

// a11yNumberRow is one stepping row (a delay, a level, a percentage).
func a11yNumberRow(id, label, detail string, lo, hi, step int, unit string,
	current func() int, apply func(int) bool) setRow {
	options := a11yNumberOptions(lo, hi, step, unit)
	return setRow{
		ID: id, Label: label, Kind: setKindChoice,
		Detail:  setStatic(detail),
		Options: func(*Config) []setOption { return options },
		Current: func(*Config) int { return a11yNumberIndex(lo, hi, step, current()) },
		Value:   func(*Config) string { return strconv.Itoa(current()) + unit },
		Choose: func(c *Config, value string) {
			n, err := strconv.Atoi(value)
			if err != nil {
				return
			}
			if apply(n) {
				a11ySave(c)
			}
		},
	}
}

func a11yCaptionSizeSettingRow() setRow {
	return setRow{
		ID: "captionSize", Label: "Caption size", Kind: setKindChoice,
		Detail: setStatic("Small, Medium or Large cue text above the bottom of the picture"),
		Options: func(*Config) []setOption {
			return []setOption{
				{Value: "0", Label: "Small"},
				{Value: "1", Label: "Medium"},
				{Value: "2", Label: "Large"},
			}
		},
		Current: func(*Config) int { return clampInt(a11y.captionSize, 0, 2) },
		Choose: func(c *Config, value string) {
			n, err := strconv.Atoi(value)
			if err != nil {
				return
			}
			if a11ySetCaptionSize(n) {
				a11ySave(c)
			}
		},
	}
}

func a11yMagnifierZoomSettingRow() setRow {
	return setRow{
		ID: "magnifierZoom", Label: "Magnifier zoom", Kind: setKindChoice,
		Detail: setStatic("How much the lens enlarges what it shows"),
		Options: func(*Config) []setOption {
			return []setOption{
				{Value: "2", Label: "2×"},
				{Value: "3", Label: "3×"},
			}
		},
		Current: func(*Config) int {
			if a11yClampMagnifierZoom(a11y.magnifierZoom) == 3 {
				return 1
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			zoom, err := strconv.Atoi(value)
			if err != nil {
				return
			}
			if a11ySetMagnifierZoom(zoom) {
				a11ySave(c)
			}
		},
	}
}

func a11yColorblindSettingRow() setRow {
	return setRow{
		ID: "colorblind", Label: "Colour-blind palette", Kind: setKindChoice,
		Detail: setStatic("Replaces the theme colours with a safer set"),
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(a11yColorblindModes))
			for _, mode := range a11yColorblindModes {
				out = append(out, setOption{Value: mode.key, Label: mode.label})
			}
			return out
		},
		Current: func(*Config) int { return a11yColorblindIndex(a11y.colorblind) },
		Choose: func(c *Config, value string) {
			if a11ySetColorblind(c, value) {
				a11ySave(c)
			}
		},
	}
}

func a11yLayoutSettingRow() setRow {
	return setRow{
		ID: "layoutPreset", Label: "Button layout", Kind: setKindChoice,
		Detail: setStatic("Nintendo order swaps A/B and X/Y together; applying a preset clears the per-button remaps below"),
		Options: func(*Config) []setOption {
			return []setOption{
				{Value: "standard", Label: "Standard"},
				{Value: "nintendo", Label: "Nintendo"},
			}
		},
		Current: func(*Config) int {
			switch a11yLayoutPresetLabel() {
			case "Nintendo":
				return 1
			case "Standard":
				return 0
			}
			return -1
		},
		Value: func(*Config) string { return a11yLayoutPresetLabel() },
		Choose: func(c *Config, value string) {
			if a11ySetLayoutPreset(value == "nintendo") {
				a11ySave(c)
			}
		},
	}
}

// a11yButtonTargetOptions are one button's stops: itself, every other button,
// then Disabled.
func a11yButtonTargetOptions(button uint8) []setOption {
	out := []setOption{{Value: "", Label: a11yButtonLabel(button) + " itself"}}
	for _, known := range a11yButtonNames {
		if known.button == button {
			continue
		}
		out = append(out, setOption{Value: known.key, Label: known.label})
	}
	return append(out, setOption{Value: a11yButtonNone, Label: "Disabled"})
}

// a11yButtonTargetIndex is a11yButtonTargetOptions' current stop.
func a11yButtonTargetIndex(button uint8) int {
	options := a11yButtonTargetOptions(button)
	logical, dropped := a11yResolveButton(button)
	if dropped {
		return len(options) - 1
	}
	if logical == button {
		return 0
	}
	for i, o := range options {
		if target, ok := a11yButtonCode(o.Value); ok && target == logical {
			return i
		}
	}
	return -1
}

// a11yButtonSettingRow remaps one physical button. It is a view of the same
// keymap layer the panel edits, so the two cannot disagree.
func a11yButtonSettingRow(button uint8) setRow {
	label := a11yButtonLabel(button)
	return setRow{
		ID: "button" + a11yButtonKey(button), Label: label, Kind: setKindChoice,
		Detail: func(*Config) string {
			return "Which action " + label + " sends; Disabled swallows the button"
		},
		Options: func(*Config) []setOption { return a11yButtonTargetOptions(button) },
		Current: func(*Config) int { return a11yButtonTargetIndex(button) },
		Choose: func(c *Config, value string) {
			// A blank value is "leave this button as it is", so the initial
			// value is the one that reaches the call on that path.
			target := button
			switch value {
			case "":
			case a11yButtonNone:
				target = a11yDisabledButton
			default:
				code, ok := a11yButtonCode(value)
				if !ok {
					return
				}
				target = code
			}
			if a11ySetButtonTarget(button, target) {
				a11ySave(c)
			}
		},
	}
}

func a11ySelectSettingRow() setRow {
	return setRow{
		ID: "selectKey", Label: "Select button", Kind: setKindChoice,
		Detail: setStatic("Moves the Select action to Start (both stay reachable), or disables Select entirely; Start keeps opening the menu"),
		Options: func(*Config) []setOption {
			return []setOption{
				{Value: a11ySelectDefault, Label: "Default"},
				{Value: a11ySelectSwapped, Label: "Swapped with Start"},
				{Value: a11ySelectDisabled, Label: "Disabled"},
			}
		},
		Current: func(*Config) int {
			switch a11ySelectKeyMode() {
			case a11ySelectSwapped:
				return 1
			case a11ySelectDisabled:
				return 2
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			if a11ySetSelectMode(value) {
				a11ySave(c)
			}
		},
	}
}

func a11yChordSettingRow() setRow {
	options := make([]setOption, 0, len(a11yChordButtons))
	for _, option := range a11yChordButtons {
		options = append(options, setOption{Value: option.key, Label: option.label})
	}
	return setRow{
		ID: "chordButton", Label: "Chord button", Kind: setKindChoice,
		Detail:  setStatic("The button that arms the one-handed chord; pick one your other hand's index finger can reach"),
		Options: func(*Config) []setOption { return options },
		Current: func(*Config) int {
			for i, option := range a11yChordButtons {
				if option.key == a11yChordKey(a11y.chordButton) {
					return i
				}
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			if a11ySetChordButton(value) {
				a11ySave(c)
			}
		},
	}
}

func a11ySwitchSettingRow() setRow {
	return setRow{
		ID: "switchAccess", Label: "Switch access", Kind: setKindChoice,
		Detail: setStatic("Walks the focus through every control on its own: one button presses the highlighted control, two buttons add B as next"),
		Options: func(*Config) []setOption {
			return []setOption{
				{Value: a11ySwitchOff, Label: "Off"},
				{Value: a11ySwitchOne, Label: "One button"},
				{Value: a11ySwitchTwo, Label: "Two buttons"},
			}
		},
		Current: func(*Config) int {
			switch a11ySwitchModeValue(a11y.switchMode) {
			case a11ySwitchOne:
				return 1
			case a11ySwitchTwo:
				return 2
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			if a11ySetSwitchMode(value) {
				a11ySave(c)
			}
		},
	}
}

func a11ySwitchIntervalSettingRow() setRow {
	options := make([]setOption, 0, len(a11ySwitchIntervalSteps))
	for _, ms := range a11ySwitchIntervalSteps {
		options = append(options, setOption{Value: strconv.Itoa(ms), Label: a11ySwitchIntervalLabel(ms)})
	}
	return setRow{
		ID: "switchInterval", Label: "Scan interval", Kind: setKindChoice,
		Detail: func(*Config) string {
			if !switchScanOn() {
				return "Turn Switch access on to use it"
			}
			return "How long a control stays highlighted before the scan moves on"
		},
		Disabled: func(*Config) bool { return !switchScanOn() },
		Options:  func(*Config) []setOption { return options },
		Current: func(*Config) int {
			want := clampInt(a11y.switchIntervalMs, 400, 5000)
			best, bestD := 0, 1<<30
			for i, ms := range a11ySwitchIntervalSteps {
				d := ms - want
				if d < 0 {
					d = -d
				}
				if d < bestD {
					best, bestD = i, d
				}
			}
			return best
		},
		Choose: func(c *Config, value string) {
			ms, err := strconv.Atoi(value)
			if err != nil {
				return
			}
			if a11ySetSwitchInterval(ms) {
				a11ySave(c)
			}
		},
	}
}

func a11ySlowRepeatSettingRow() setRow {
	return setRow{
		ID: "slowRepeat", Label: "Key repeat", Kind: setKindChoice,
		Detail: setStatic("How fast a held direction repeats on the on-screen keyboard; Slow is for hands that overshoot"),
		Options: func(*Config) []setOption {
			return []setOption{
				{Value: "normal", Label: "Normal"},
				{Value: "slow", Label: "Slow"},
			}
		},
		Current: func(*Config) int {
			if a11y.slowRepeat {
				return 1
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			if a11ySetSlowRepeat(value == "slow") {
				a11ySave(c)
			}
		},
	}
}
