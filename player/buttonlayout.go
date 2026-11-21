package main

// buttonlayout.go — Settings → Controls → Button layout: Nintendo or Xbox,
// one choice for JukaHub's menus and RetroArch games.
//
// TrimUI handhelds print a Nintendo layout: A on the right, B at the bottom, X
// at the top, Y on the left, and the right one confirms. Players coming from
// an Xbox pad or a Steam Deck want the bottom one to. JukaHub had both halves
// in two places: Accessibility's button-layout preset, which swaps A/B and
// X/Y in every menu (a11y.go; the hints then name the button that now does the
// job, inputhints.go), and Controls → Game buttons' "Swap both" for RetroArch
// games (racontrols.go, a one-launch remap file). This row sets both:
//
//	Nintendo  the menus' swaps off; RetroArch games' All consoles Buttons back
//	          to what it was before Xbox was picked (Normal when that was
//	          Swap both, or nothing)
//	Xbox      the menus' swaps on; All consoles Buttons set to Swap both, its
//	          previous value remembered for Nintendo
//
// Either stop is a whole layout, as Accessibility's preset is: Accessibility's
// per-button remaps are cleared (the row says so first). A console's or a
// game's own Buttons still wins over All consoles (racontrols.go resolves the
// game, then the console, then All consoles), so a console set to Normal stays
// Nintendo-style under Xbox, and back.
//
// The row stores nothing of its own: it reads the menus' swaps — Xbox when both
// are on with no per-button remaps, Nintendo when neither is, Custom
// otherwise. So nothing changes on an upgrade: a player whose Accessibility
// preset already swapped both sees Xbox, and their RetroArch buttons stay as
// they were until they pick a stop; the row's line says when the games differ
// from the menus. The only key written besides the two features' own is
// buttonLayoutGamesBefore, the All consoles Buttons to put back (bookkeeping).
//
// The menus' half is a11ySetLayoutPreset's state change (both swaps on or off,
// per-button remaps cleared), made here without that function's toast: it
// names the swap "Nintendo order", which is what it is on a PC's Xbox pad but
// the Xbox order on a TrimUI. The hardware switches under Games & emulators
// (Swap A and B / Swap X and Y, core.go applyButtonSwap) are another layer —
// they make A mean the button printed A — and are not touched: the layout
// swaps on top of them, as Accessibility's preset always has. The row is shown
// on handhelds only; on a PC pad the letters already sit the Xbox way and
// Accessibility's preset is the switch.
//
// "Button hints show" (shown under Xbox): Printed letters, the default, names
// the button to press by the letter printed on it (the bottom button is "B"),
// which is what the hints already did; Roles names it by its job in the Xbox
// layout ("A" confirms). inputhints.go's hintRemapRefresh asks
// buttonLayoutHintRoles and drops the preset's relabelling while it is on.
//
// Not covered (yet): PPSSPP standalone (its controls.ini key map), PortMaster
// ports (gptokeyb's mapping per port) and NextUI paks (minarch has no remap
// file JukaHub writes) keep the printed layout.

import "time"

const (
	buttonLayoutNintendo = "nintendo"
	buttonLayoutXbox     = "xbox"
	buttonLayoutCustom   = "custom"

	// buttonLayoutGamesBeforeKey is RetroArch games' All consoles Buttons from
	// before Xbox was picked, put back by Nintendo; "" when nothing is
	// remembered. Bookkeeping (cfgdocRuntimeKeys).
	buttonLayoutGamesBeforeKey = "buttonLayoutGamesBefore"

	// buttonLayoutHintsKey is what the hints show: "letters" (unset) or
	// "roles".
	buttonLayoutHintsKey     = "buttonLayoutHints"
	buttonLayoutHintsLetters = "letters"
	buttonLayoutHintsRoles   = "roles"
)

// buttonLayoutMenus is the menus' layout, read from Accessibility's state.
// Main thread.
func buttonLayoutMenus() string {
	if len(a11y.keys) > 0 {
		return buttonLayoutCustom
	}
	switch {
	case a11y.swapAB && a11y.swapXY:
		return buttonLayoutXbox
	case !a11y.swapAB && !a11y.swapXY:
		return buttonLayoutNintendo
	}
	return buttonLayoutCustom
}

// buttonLayoutGamesXbox reports whether RetroArch games' All consoles Buttons
// is Swap both.
func buttonLayoutGamesXbox(c *Config) bool {
	return gameButtonsAll(c) == gameButtonsSwapBoth
}

// buttonLayoutRemember stores the All consoles Buttons to put back ("" for
// none) and saves the settings.
func buttonLayoutRemember(c *Config, v string) {
	if c == nil {
		return
	}
	if c.Variables.Custom == nil {
		c.Variables.Custom = map[string]interface{}{}
	}
	c.Variables.Custom[buttonLayoutGamesBeforeKey] = v
	saveUserConfigDebounced(userConfigFromVariables(c))
}

// buttonLayoutApply makes the whole layout Xbox or Nintendo: the menus' swaps
// and RetroArch games' All consoles Buttons. Main thread.
func buttonLayoutApply(c *Config, xbox bool) {
	if c == nil {
		return
	}
	// The menus: a11ySetLayoutPreset's change, without its toast (see the
	// file comment).
	a11y.swapAB, a11y.swapXY = xbox, xbox
	a11y.keys = map[uint8]uint8{}
	a11ySave(c)
	buttonLayoutRolesCache.at = time.Time{}

	// RetroArch games.
	current := normaliseGameButtons(gameControlsRaw(c, gameControlsButtonsKey))
	if xbox {
		if current == gameButtonsSwapBoth {
			return
		}
		if current == "" {
			current = gameButtonsDefault
		}
		buttonLayoutRemember(c, current)
		setStoreString(c, gameControlsButtonsKey, gameButtonsSwapBoth)
		return
	}
	if current == gameButtonsSwapBoth {
		back := normaliseGameButtons(gameControlsRaw(c, buttonLayoutGamesBeforeKey))
		if back == "" || back == gameButtonsSwapBoth {
			back = gameButtonsDefault
		}
		setStoreString(c, gameControlsButtonsKey, back)
	}
	if gameControlsRaw(c, buttonLayoutGamesBeforeKey) != "" {
		buttonLayoutRemember(c, "")
	}
}

// ── Hints ────────────────────────────────────────────────────────────────

// buttonLayoutRolesCache keeps the hints choice for a second: the hints ask
// every frame. Main thread.
var buttonLayoutRolesCache struct {
	at time.Time
	on bool
}

// buttonLayoutHintsSetting is the stored hints choice.
func buttonLayoutHintsSetting(c *Config) string {
	if raTuneRaw(c, buttonLayoutHintsKey) == buttonLayoutHintsRoles {
		return buttonLayoutHintsRoles
	}
	return buttonLayoutHintsLetters
}

// buttonLayoutHintRoles reports whether the hints name buttons by their role
// in the Xbox layout instead of the printed letter: Roles chosen, and the
// menus' layout is exactly the Xbox swap (per-button remaps always show the
// button to press). Main thread (inputhints.go's hintRemapRefresh).
func buttonLayoutHintRoles() bool {
	if len(a11y.keys) > 0 || !a11y.swapAB || !a11y.swapXY {
		return false
	}
	cache := &buttonLayoutRolesCache
	if now := time.Now(); cache.at.IsZero() || now.Sub(cache.at) >= time.Second {
		cache.at = now
		cache.on = appConfig != nil && buttonLayoutHintsSetting(appConfig) == buttonLayoutHintsRoles
	}
	return cache.on
}

// ── Settings → Controls ──────────────────────────────────────────────────

func buttonLayoutOptions(*Config) []setOption {
	return []setOption{
		{Value: buttonLayoutNintendo, Label: "Nintendo", Note: "The right button (A) confirms, as printed"},
		{Value: buttonLayoutXbox, Label: "Xbox", Note: "The bottom button confirms: A/B and X/Y swapped in menus and RetroArch games"},
	}
}

func buttonLayoutLabel(v string) string {
	switch v {
	case buttonLayoutXbox:
		return "Xbox"
	case buttonLayoutNintendo:
		return "Nintendo"
	}
	return "Custom"
}

// buttonLayoutDetail is the row's line: what it does, or where the games or
// Accessibility differ from the menus.
func buttonLayoutDetail(c *Config) string {
	menus := buttonLayoutMenus()
	games := buttonLayoutGamesXbox(c)
	switch {
	case !abButtonsSwapped(c) || !xyButtonsSwapped(c):
		// The hardware switches are on by default here; one turned off swaps
		// the menus once more, on top of this layout.
		return "Swap A and B or Swap X and Y (Games & emulators) is off and turns the menus round again"
	case menus == buttonLayoutCustom:
		return "Accessibility remaps some buttons; picking a layout clears those remaps"
	case menus == buttonLayoutXbox && !games:
		return "Menus: Xbox · RetroArch games: " + gameButtonsLabel(gameButtonsAll(c)) + " (Buttons, below)"
	case menus == buttonLayoutNintendo && games:
		return "Menus: Nintendo · RetroArch games: Swap both (Buttons, below)"
	}
	return "Which button confirms, in menus and RetroArch games · a console or game with its own Buttons keeps them"
}

func buttonLayoutHintsOptions(*Config) []setOption {
	return []setOption{
		{Value: buttonLayoutHintsLetters, Label: "Printed letters", Note: "The letter printed on the button to press"},
		{Value: buttonLayoutHintsRoles, Label: "Roles", Note: "The button's job in the Xbox layout: A confirms, B goes back"},
	}
}

// buttonLayoutSettingsRows are Settings → Controls' first rows
// (inmap_settings.go).
func buttonLayoutSettingsRows() []setRow {
	return []setRow{
		{
			ID: "buttonLayout", Label: "Button layout", Kind: setKindChoice,
			Detail:  buttonLayoutDetail,
			Visible: func(*Config) bool { return P().IsHandheld() },
			Options: buttonLayoutOptions,
			Current: func(c *Config) int { return raTuneOptionIndex(buttonLayoutOptions(c), buttonLayoutMenus()) },
			Value:   func(*Config) string { return buttonLayoutLabel(buttonLayoutMenus()) },
			Choose: func(c *Config, value string) {
				xbox := value == buttonLayoutXbox
				buttonLayoutApply(c, xbox)
				if xbox {
					setToast("Button layout: Xbox · the bottom button confirms")
				} else {
					setToast("Button layout: Nintendo · the right button confirms")
				}
			},
		},
		{
			ID: "buttonHintsShow", Label: "Button hints show", Kind: setKindChoice,
			Detail: setStatic("What the hints call a button while the Xbox layout is on"),
			Visible: func(*Config) bool {
				return P().IsHandheld() && buttonLayoutMenus() == buttonLayoutXbox
			},
			Options: buttonLayoutHintsOptions,
			Current: func(c *Config) int {
				return raTuneOptionIndex(buttonLayoutHintsOptions(c), buttonLayoutHintsSetting(c))
			},
			Choose: func(c *Config, value string) {
				if value != buttonLayoutHintsRoles {
					value = buttonLayoutHintsLetters
				}
				setStoreString(c, buttonLayoutHintsKey, value)
				buttonLayoutRolesCache.at = time.Time{}
				if value == buttonLayoutHintsRoles {
					setToast("Button hints show: Roles")
				} else {
					setToast("Button hints show: Printed letters")
				}
			},
		},
	}
}

func init() {
	cfgdocRuntimeKeys[buttonLayoutGamesBeforeKey] = true
}
