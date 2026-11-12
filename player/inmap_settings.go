package main

// inmap_settings.go — the rows of Settings → Controls.
//
// The section gathers what the pad does: the control profile in use (a
// carousel; A lists them all), whether screens switch their own profile on,
// the hold Select + R1 hotkey, the Control profiles screen, the D-pad mode of
// each kind of content (navmode.go) and Local play. Every row calls the
// feature's own setter, which applies the change and saves it.

import (
	"strconv"
)

// inmapSettingsRows are the section's rows, in order.
func inmapSettingsRows() []setRow {
	rows := []setRow{
		{
			// What the hints call the controls: the pad's buttons or the
			// keyboard's keys (inputhints.go). Auto follows the device in use.
			ID: "buttonHints", Label: "Button hints", Kind: setKindChoice,
			Detail:  hintModeDetail,
			Options: hintModeOptions,
			Current: hintModeCurrent,
			Choose: func(c *Config, value string) {
				hintSetMode(c, value)
				setToast("Button hints: " + hintModeOptions(c)[hintModeCurrent(c)].Label)
			},
		},
		trimuiStickCalSettingsRow(),
		stickCalSettingsRow(),
		{
			ID: "inputProfile", Label: "Control profile", Kind: setKindChoice, Quiet: true,
			Detail:     inmapProfileDetail,
			Options:    inmapProfileOptions,
			Current:    inmapProfileCurrent,
			Sheet:      inmapProfileOptions,
			SheetValue: inmapChosenName,
			SheetTitle: "Control profile",
			Value:      inmapChosenName,
			Choose: func(c *Config, value string) {
				if p := inmapFindProfile(value); p != nil {
					inmapChoose(c, p, true)
				}
			},
		},
		{
			ID: "inputProfileAuto", Label: "Switch by screen", Kind: setKindToggle,
			Detail: setStatic("A profile that names a screen takes over on it"),
			Get:    func(*Config) bool { return inmapAutoEnabled() },
			Set: func(c *Config, on bool) {
				inmapSetAuto(c, on)
				setToast("Switch by screen: " + setOnOff(on))
			},
		},
		{
			ID: "inputProfileHotkey", Label: "Switch hotkey", Kind: setKindToggle,
			Detail: setStatic("Hold Select and press R1 to step through profiles"),
			Get:    func(*Config) bool { return inmapHotkeyEnabled() },
			Set: func(c *Config, on bool) {
				inmapSetHotkey(c, on)
				setToast("Profile switch hotkey: " + setOnOff(on))
			},
		},
		{
			ID: "inputProfiles", Label: "Edit profiles", Kind: setKindAction,
			Detail: setStatic("Every mapping and problem; import, copy and share"),
			Value:  inmapProfilesValue,
			Open:   inmapOpenProfiles,
		},
	}
	// The virtual cursor and each stick's dead zone
	// (stickcal.go).
	rows = append(rows, stickCalSettingsRows()...)
	for a := navArea(0); a < navAreaCount; a++ {
		rows = append(rows, navmodeSettingsRow(a))
	}
	return append(rows,
		setRow{
			ID: "navModeWrap", Label: "D-pad wraps around", Kind: setKindToggle,
			Detail: setStatic("Grid and List carry on from the far edge"),
			Get:    func(*Config) bool { return navmodeWrapEnabled() },
			Set: func(c *Config, on bool) {
				navmodeSetWrap(c, on)
				setToast("D-pad wraps around: " + setOnOff(on))
			},
		},
		setRow{
			ID: "localPlay", Label: "Local play", Kind: setKindAction,
			Detail: setStatic("Two or more controllers, or one passed around"),
			Value:  func(*Config) string { return hotseatCountText() },
			Open:   hotseatOpen,
		},
	)
}

// inmapChosenName is the chosen profile's name.
func inmapChosenName(*Config) string {
	if p := inmapChosen(); p != nil {
		return p.name
	}
	return inmapDefaultName
}

// inmapProfileOptions are the profiles as carousel stops.
func inmapProfileOptions(*Config) []setOption {
	list := inmapProfileList()
	out := make([]setOption, 0, len(list))
	for _, p := range list {
		out = append(out, setOption{Value: p.name, Label: p.name, Note: p.description})
	}
	return out
}

// inmapProfileCurrent is the chosen profile's stop.
func inmapProfileCurrent(c *Config) int {
	name := inmapChosenName(c)
	for i, p := range inmapProfileList() {
		if p.name == name {
			return i
		}
	}
	return -1
}

// inmapProfileDetail is the row's second line: what the chosen profile does,
// or which profile a screen switched on instead.
func inmapProfileDetail(*Config) string {
	chosen, effective := inmapChosen(), inmapEffective()
	if effective != nil && chosen != nil && effective != chosen {
		return "This screen uses " + effective.name
	}
	if chosen == nil || chosen.description == "" {
		return "Hold Select and press R1 to switch"
	}
	return chosen.description
}

// inmapProfilesValue is the Edit profiles row's read-out.
func inmapProfilesValue(*Config) string {
	list := inmapProfileList()
	broken := 0
	for _, p := range list {
		if p.errorCount() > 0 {
			broken++
		}
	}
	if broken > 0 {
		return inmapCount(broken, "needs a look", "need a look")
	}
	return strconv.Itoa(len(list)) + " profiles"
}
