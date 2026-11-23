package main

// rahotkeys_ui.go — where the custom RetroArch hotkeys (rahotkeys.go) are
// chosen.
//
// Settings → Controls, under the Game buttons rows, built like them:
//
//	Hotkeys for      All consoles, or one console that gets its own
//	Hotkey button    the button held with every shortcut
//	Shortcut         which shortcut the next row changes (A lists them all,
//	                 each with its buttons)
//	Its buttons      that shortcut's button
//	Clear …          drops the scope's own choices (shown when it has some)
//
// A console's rows start at "Default · …", which follows All consoles and
// says what that is. The game options sheet has the same pair for one game:
// Hotkey (Left/Right picks the shortcut) and Hotkey buttons (Left/Right picks
// its button, Default following the console); after a launch that could not
// apply something, that row's line says what.
//
// A button that cannot be a shortcut here — one of JukaHub's Select chords,
// the hotkey button itself, or a button another shortcut of the scope uses —
// is skipped by Left/Right; the lists (A) still show it with the reason, and
// picking it there is refused with a toast. The picked console and shortcut
// are a view for this run of the app, not settings.

import "strings"

// raHotkeyScope is the console the scoped rows edit; "" is all consoles.
// raHotkeySel is the shortcut the Its buttons row edits on the Settings page,
// raHotkeySheetSel the one on the game options sheet. Main thread.
var (
	raHotkeyScope    string
	raHotkeySel      = "menu"
	raHotkeySheetSel = "menu"
)

// raHotkeyLevelNow is the Settings page's level.
func raHotkeyLevelNow() raLevel { return raLevel{Console: raHotkeyScope} }

// raHotkeyGameLevel is one game's level.
func raHotkeyGameLevel(game libraryGame) raLevel {
	return raLevel{Console: gameControlsConsole(game.System), Game: game.Path}
}

// raHotkeyEffective is a field's value at a level, the levels above
// included ("" when nothing is chosen anywhere).
func raHotkeyEffective(l raLevel, field string) string {
	v, _ := raHotkeyStore.resolve(l.Console, l.Game, field)
	return v
}

// raHotkeyInherited is what "Default" means at a level: the levels above's
// value.
func raHotkeyInherited(l raLevel, field string) string {
	switch {
	case l.Game != "":
		return raHotkeyEffective(raLevel{Console: l.Console}, field)
	case l.Console != "":
		return raHotkeyEffective(raLevel{}, field)
	}
	return ""
}

// raHotkeyModifierAt is the hotkey button at a level, "" for the firmware's
// own (which JukaHub only learns at a launch).
func raHotkeyModifierAt(l raLevel) string {
	if m := raHotkeyEffective(l, raHotkeyModifier); m != raHotkeyOwn {
		return m
	}
	return ""
}

// raHotkeyCombo is a shortcut's buttons in words.
func raHotkeyCombo(action, value, modifier string) string {
	if value == "" || value == raHotkeyOwn {
		return "RetroArch's own"
	}
	if action == raHotkeyModifier {
		return raPadButtonLabel(value)
	}
	mod := "Hotkey"
	if modifier != "" {
		mod = raPadButtonLabel(modifier)
	}
	return mod + " + " + raPadButtonLabel(value)
}

// raHotkeyValueText is a stored value at a level in words: "Default · …"
// for "" below All consoles.
func raHotkeyValueText(l raLevel, action, value string) string {
	if value == "" && (l.Console != "" || l.Game != "") {
		return "Default · " + raHotkeyCombo(action, raHotkeyInherited(l, action), raHotkeyModifierAt(l))
	}
	return raHotkeyCombo(action, value, raHotkeyModifierAt(l))
}

// raHotkeyWhyNot is why value cannot be action's at a level, "" when it can.
func raHotkeyWhyNot(c *Config, l raLevel, action, value string) string {
	if value == "" || value == raHotkeyOwn {
		return ""
	}
	reserved := raHotkeyReservedSelect(c)
	if action == raHotkeyModifier {
		for _, a := range raHotkeyActions {
			if a.ID == raHotkeyModifier {
				continue
			}
			if why := raHotkeyChordTaken(a.ID, value, raHotkeyEffective(l, a.ID), reserved); why != "" {
				return a.Label + ": " + why
			}
		}
		return ""
	}
	if why := raHotkeyChordTaken(action, raHotkeyModifierAt(l), value, reserved); why != "" {
		return why
	}
	for _, a := range raHotkeyActions {
		if a.ID == raHotkeyModifier || a.ID == action {
			continue
		}
		if raHotkeyEffective(l, a.ID) == value {
			return raPadButtonLabel(value) + " is " + a.Label + " here"
		}
	}
	return ""
}

// raHotkeyChoices is a shortcut's ladder at a level: Default (below All
// consoles), RetroArch's own, then its buttons.
func raHotkeyChoices(l raLevel, action string) []string {
	var out []string
	if l.Console != "" || l.Game != "" {
		out = append(out, "")
	}
	out = append(out, raHotkeyOwn)
	// L3 / R3 are offered only where something sends them: sticks that press
	// in, or a Brick's F1 / F2 (stickclicks.go). The original Smart Pro has
	// neither, and a hotkey on a button it lacks could never be pressed.
	sticks := padSendsStickClicks()
	if action == raHotkeyModifier {
		for _, b := range raHotkeyModifierButtons {
			if sticks || (b != "l3" && b != "r3") {
				out = append(out, b)
			}
		}
		return out
	}
	for _, b := range raPadButtons {
		if sticks || (b.ID != "l3" && b.ID != "r3") {
			out = append(out, b.ID)
		}
	}
	return out
}

// raHotkeyOptions are the rows' stops (valid only, for Left/Right) or the
// list's (all, the taken ones with the reason).
func raHotkeyOptions(c *Config, l raLevel, action string, all bool) []setOption {
	a, _ := raHotkeyActionByID(action)
	var out []setOption
	for _, v := range raHotkeyChoices(l, action) {
		why := raHotkeyWhyNot(c, l, action, v)
		if why != "" && !all {
			continue
		}
		note := ""
		switch {
		case why != "":
			note = "Taken: " + why
		case v == "":
			note = raTuneDefaultNote
		case v == raHotkeyOwn:
			note = "Leave the firmware's " + strings.ToLower(a.Label) + " as it is"
		}
		out = append(out, setOption{Value: v, Label: raHotkeyValueText(l, action, v), Note: note})
	}
	return out
}

// raHotkeyChoose stores one value at a level after the checks; it reports
// whether it did.
func raHotkeyChoose(c *Config, l raLevel, action, value string) bool {
	if l.Console == "" && l.Game == "" && value == "" {
		value = raHotkeyOwn
	}
	a, ok := raHotkeyActionByID(action)
	if !ok {
		return false
	}
	if why := raHotkeyWhyNot(c, l, action, value); why != "" {
		ShowToast(a.Label+" not changed: "+why, ToastKindWarning)
		return false
	}
	if l.Console == "" && l.Game == "" && value == raHotkeyOwn {
		value = "" // All consoles' own is simply nothing stored
	}
	raHotkeyStore.set(l, action, value)
	return true
}

// raHotkeyHasOwn reports whether a level has any choice of its own.
func raHotkeyHasOwn(l raLevel) bool { return len(raHotkeyStore.fields(l)) > 0 }

func raHotkeyScopeName() string {
	if raHotkeyScope == "" {
		return "All consoles"
	}
	return raHotkeyScope
}

// ── Settings → Controls ──────────────────────────────────────────────────

func raHotkeyConsoleOptions(*Config) []setOption {
	out := make([]setOption, 0, len(raTuneSystems)+1)
	out = append(out, setOption{Value: "", Label: "All consoles", Note: "The hotkeys every console starts with"})
	for _, s := range raTuneSystems {
		note := ""
		if raHotkeyHasOwn(raLevel{Console: s.Name}) {
			note = "Has its own hotkeys"
		}
		out = append(out, setOption{Value: s.Name, Label: s.Name, Note: note})
	}
	return out
}

func raHotkeyActionOptions(*Config) []setOption {
	l := raHotkeyLevelNow()
	out := make([]setOption, 0, len(raHotkeyActions))
	for _, a := range raHotkeyActions {
		if a.ID == raHotkeyModifier {
			continue
		}
		out = append(out, setOption{Value: a.ID, Label: a.Label,
			Note: raHotkeyValueText(l, a.ID, raHotkeyStore.field(l, a.ID))})
	}
	return out
}

// raHotkeyStoredAt is a level's own value as the rows show it: All
// consoles' nothing is RetroArch's own.
func raHotkeyStoredAt(l raLevel, action string) string {
	v := raHotkeyStore.field(l, action)
	if v == "" && l.Console == "" && l.Game == "" {
		return raHotkeyOwn
	}
	return v
}

// raHotkeySettingsRows are the Controls page's hotkey rows.
func raHotkeySettingsRows() []setRow {
	return []setRow{
		{
			ID: "raHotkeysScope", Label: "Hotkeys for", Kind: setKindChoice,
			Detail: func(*Config) string {
				if raHotkeyScope == "" {
					return "RetroArch's hotkeys · pick a console to give it its own"
				}
				return "The next rows change " + raHotkeyScope + " games only"
			},
			Options:    raHotkeyConsoleOptions,
			Sheet:      raHotkeyConsoleOptions,
			SheetValue: func(*Config) string { return raHotkeyScope },
			SheetTitle: "Hotkeys for",
			Current:    func(c *Config) int { return raTuneOptionIndex(raHotkeyConsoleOptions(c), raHotkeyScope) },
			Value:      func(*Config) string { return raHotkeyScopeName() },
			Choose: func(_ *Config, value string) {
				raHotkeyScope = ""
				for _, s := range raTuneSystems {
					if s.Name == value {
						raHotkeyScope = s.Name
					}
				}
			},
		},
		{
			ID: "raHotkeysModifier", Label: "Hotkey button", Kind: setKindChoice,
			Detail: func(*Config) string {
				if raHotkeyScope == "" {
					return "Held with every RetroArch shortcut · RetroArch's own keeps the firmware's"
				}
				return raHotkeyScope + " games only"
			},
			Options: func(c *Config) []setOption { return raHotkeyOptions(c, raHotkeyLevelNow(), raHotkeyModifier, false) },
			Sheet:   func(c *Config) []setOption { return raHotkeyOptions(c, raHotkeyLevelNow(), raHotkeyModifier, true) },
			SheetValue: func(*Config) string {
				return raHotkeyStoredAt(raHotkeyLevelNow(), raHotkeyModifier)
			},
			SheetTitle: "Hotkey button",
			Current: func(c *Config) int {
				return raTuneOptionIndex(raHotkeyOptions(c, raHotkeyLevelNow(), raHotkeyModifier, false), raHotkeyStoredAt(raHotkeyLevelNow(), raHotkeyModifier))
			},
			Value: func(*Config) string {
				l := raHotkeyLevelNow()
				return raHotkeyValueText(l, raHotkeyModifier, raHotkeyStore.field(l, raHotkeyModifier))
			},
			Choose: func(c *Config, value string) {
				l := raHotkeyLevelNow()
				if raHotkeyChoose(c, l, raHotkeyModifier, value) {
					raHotkeyToast("Hotkey button", raHotkeyValueText(l, raHotkeyModifier, raHotkeyStore.field(l, raHotkeyModifier)))
				}
			},
		},
		{
			ID: "raHotkeysAction", Label: "Shortcut", Kind: setKindChoice,
			Detail: func(*Config) string {
				if a, ok := raHotkeyActionByID(raHotkeySel); ok {
					return a.Note + " · A lists every shortcut"
				}
				return "A lists every shortcut"
			},
			Options:    raHotkeyActionOptions,
			Sheet:      raHotkeyActionOptions,
			SheetValue: func(*Config) string { return raHotkeySel },
			SheetTitle: "Shortcut",
			Current:    func(c *Config) int { return raTuneOptionIndex(raHotkeyActionOptions(c), raHotkeySel) },
			Choose: func(_ *Config, value string) {
				if _, ok := raHotkeyActionByID(value); ok && value != raHotkeyModifier {
					raHotkeySel = value
				}
			},
		},
		{
			ID: "raHotkeysKey", Label: "Its buttons", Kind: setKindChoice,
			Detail: func(*Config) string {
				what := "RetroArch games"
				if raHotkeyScope != "" {
					what = raHotkeyScope + " games only"
				}
				return what + " · a game can choose its own (Start in the library)"
			},
			Options: func(c *Config) []setOption { return raHotkeyOptions(c, raHotkeyLevelNow(), raHotkeySel, false) },
			Sheet:   func(c *Config) []setOption { return raHotkeyOptions(c, raHotkeyLevelNow(), raHotkeySel, true) },
			SheetValue: func(*Config) string {
				return raHotkeyStoredAt(raHotkeyLevelNow(), raHotkeySel)
			},
			SheetTitle: "Buttons",
			Current: func(c *Config) int {
				return raTuneOptionIndex(raHotkeyOptions(c, raHotkeyLevelNow(), raHotkeySel, false), raHotkeyStoredAt(raHotkeyLevelNow(), raHotkeySel))
			},
			Value: func(*Config) string {
				l := raHotkeyLevelNow()
				return raHotkeyValueText(l, raHotkeySel, raHotkeyStore.field(l, raHotkeySel))
			},
			Choose: func(c *Config, value string) {
				l := raHotkeyLevelNow()
				if raHotkeyChoose(c, l, raHotkeySel, value) {
					a, _ := raHotkeyActionByID(raHotkeySel)
					raHotkeyToast(a.Label, raHotkeyValueText(l, raHotkeySel, raHotkeyStore.field(l, raHotkeySel)))
				}
			},
		},
		{
			ID: "raHotkeysClear", Label: "Clear these hotkeys", Kind: setKindAction,
			Detail: func(*Config) string {
				if raHotkeyScope == "" {
					return "Every shortcut back to RetroArch's own"
				}
				return raHotkeyScope + " follows All consoles again"
			},
			Visible: func(*Config) bool { return raHotkeyHasOwn(raHotkeyLevelNow()) },
			Open: func(*Config) {
				if raHotkeyStore.clear(raHotkeyLevelNow()) {
					raHotkeyToast("Hotkeys", "cleared")
				}
			},
		},
	}
}

func raHotkeyToast(what, label string) {
	if raHotkeyScope != "" {
		what = raHotkeyScope + " " + strings.ToLower(what)
	}
	setToast(what + ": " + label)
}

// ── Game options sheet ───────────────────────────────────────────────────

// raHotkeySheetStepAction moves the Hotkey row's shortcut (wrap goes round).
func raHotkeySheetStepAction(delta int, wrap bool) bool {
	ids := make([]string, 0, len(raHotkeyActions))
	for _, a := range raHotkeyActions {
		ids = append(ids, a.ID)
	}
	next := gameControlsStep(ids, raHotkeySheetSel, delta, wrap)
	if next == raHotkeySheetSel {
		return false
	}
	raHotkeySheetSel = next
	return true
}

// raHotkeySheetStepButton moves the selected shortcut's button for the game
// through the choices it can take; it reports whether anything changed.
func raHotkeySheetStepButton(game libraryGame, delta int, wrap bool) bool {
	l := raHotkeyGameLevel(game)
	var ladder []string
	for _, v := range raHotkeyChoices(l, raHotkeySheetSel) {
		if raHotkeyWhyNot(appConfig, l, raHotkeySheetSel, v) == "" {
			ladder = append(ladder, v)
		}
	}
	cur := raHotkeyStore.field(l, raHotkeySheetSel)
	next := gameControlsStep(ladder, cur, delta, wrap)
	if next == cur {
		return false
	}
	return raHotkeyChoose(appConfig, l, raHotkeySheetSel, next)
}

// raHotkeySheetActionValue is the Hotkey row's value: the shortcut.
func raHotkeySheetActionValue() string {
	a, ok := raHotkeyActionByID(raHotkeySheetSel)
	if !ok {
		raHotkeySheetSel = "menu"
		a, _ = raHotkeyActionByID(raHotkeySheetSel)
	}
	return a.Label
}

// raSheetRow reports the sheet rows these files own: ladders, so the sheet
// draws their arrows.
func raSheetRow(row int) bool {
	return row == gameOptionsRowHotkey || row == gameOptionsRowHotkeyKey || row == gameOptionsRowRatio
}

// raSheetStep is Left/Right (wrap false) or A (wrap true) on the sheet's
// Hotkey, Hotkey buttons and Aspect ratio rows; it reports a change.
func raSheetStep(game libraryGame, row, delta int, wrap bool) bool {
	switch row {
	case gameOptionsRowHotkey:
		return raHotkeySheetStepAction(delta, wrap)
	case gameOptionsRowHotkeyKey:
		return raHotkeySheetStepButton(game, delta, wrap)
	case gameOptionsRowRatio:
		return raRatioSheetStep(game, delta, wrap)
	}
	return false
}

// raSheetOwn reports a row holding the game's own choice (the sheet draws it
// in the focus colour).
func raSheetOwn(game libraryGame, row int) bool {
	switch row {
	case gameOptionsRowHotkeyKey:
		return raHotkeySheetOwn(game)
	case gameOptionsRowRatio:
		return raRatioSheetOwn(game)
	}
	return false
}

// raHotkeySheetButtonValue is the Hotkey buttons row's value.
func raHotkeySheetButtonValue(game libraryGame) string {
	l := raHotkeyGameLevel(game)
	return raHotkeyValueText(l, raHotkeySheetSel, raHotkeyStore.field(l, raHotkeySheetSel))
}

// raHotkeySheetOwn reports the game's own choice on the buttons row.
func raHotkeySheetOwn(game libraryGame) bool {
	return raHotkeyStore.field(raHotkeyGameLevel(game), raHotkeySheetSel) != ""
}

// raHotkeySheetHint is the buttons row's second line: what the last launch
// could not apply, else what it does.
func raHotkeySheetHint(game libraryGame) string {
	if note := raHotkeyStore.note(game.Path); note != "" {
		return note
	}
	if a, ok := raHotkeyActionByID(raHotkeySheetSel); ok {
		return a.Note + " · RetroArch games, this game only"
	}
	return "RetroArch games, this game only"
}
