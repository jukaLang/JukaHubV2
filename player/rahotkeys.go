package main

// rahotkeys.go — custom RetroArch hotkeys for every console, one console or
// one game, written into retroarch.cfg for one library launch.
//
// A RetroArch hotkey is the hotkey button held with another button: Menu +
// X saves a state on one firmware, Select + R1 on another. Each firmware
// ships its own set and RetroArch hides them four menus deep, so a player who
// wants Select + Start to quit (or Fast forward on R2) has to dig and then
// lives with it in every game. Here the shortcuts are rows: Settings →
// Controls (rahotkeys_ui.go) for every console or one console, and the game
// options sheet for one game. A game's choice wins over its console's, which
// wins over the all-consoles one; "RetroArch's own" (the default) writes
// nothing, so the firmware's hotkey stays. The choices are stored in
// data/game-hotkeys.json (rahotkeys_store.go).
//
// The shortcuts and the keys they write (input_<key>_btn, or _axis when the
// button is an axis):
//
//	Hotkey button        enable_hotkey        the button held with the others
//	RetroArch menu       menu_toggle
//	Quit game            exit_emulator
//	Save state           save_state
//	Load state           load_state
//	Previous slot        state_slot_decrease
//	Next slot            state_slot_increase
//	Fast forward         toggle_fast_forward
//	Fast forward (hold)  hold_fast_forward
//	Rewind               rewind               (works where rewind is on)
//	Screenshot           screenshot
//	Pause                pause_toggle
//	Restart game         reset
//	Translate screen     ai_service           (RetroArch's AI service; the
//	                                           service itself is set up by
//	                                           aitranslate*.go)
//
// Button codes are never guessed: rahotkeys_binds.go copies them from the
// config's own player-1 bindings and the pad's autoconfig profile. A button
// whose code cannot be found is not written, the log says why and the sheet
// shows it after the launch.
//
// JukaHub's own in-game chords come first. With Select as the hotkey button,
// Select + L2 (in-game menu), Select + R2 (game assistant), Select + R1
// (screenshot) and Select + L1 (the games mouse, when on) belong to JukaHub
// (gameassist_linux.go), so a shortcut there is refused on the rows and
// skipped at a launch; Select + Start is JukaHub's force quit when held, and
// only Quit game may sit there (both end the game). A shortcut on the hotkey
// button itself, or two shortcuts on one button, are refused too; across
// levels (a console's Save state on X, a game's Load state on X) the more
// specific one is written and the other skipped. When a written shortcut
// takes a button one of the firmware's own hotkeys used in retroarch.cfg,
// that hotkey is unbound for the launch, so one press never does two things
// (so is one on a button JukaHub makes the hotkey button); when JukaHub moves
// the hotkey button to Select, firmware hotkeys on JukaHub's chords are
// unbound the same way. A RetroArch with no hotkey button at all would fire a
// shortcut on its button alone, so then none is written.
//
// Everything goes through raScopeApply (racfgscope.go): put back when
// RetroArch exits, and a change made in RetroArch's menu during the game wins.
// Standalone emulators and NextUI paks (minarch) read none of it and are left
// alone.

import (
	"os"
	"sort"
	"strings"
)

const (
	raHotkeyRecordName = "game-hotkeys.json"
	// raHotkeyOwn is an explicit "RetroArch's own" (a console or game that
	// keeps the firmware's hotkey although the level above chose one).
	raHotkeyOwn = "own"
	// raHotkeyModifier is the hotkey button's field.
	raHotkeyModifier = "hotkey"
)

// raHotkeyAction is one shortcut: its stored field, RetroArch's bind key
// (input_<Key>_btn / _axis) and its words.
type raHotkeyAction struct {
	ID    string
	Key   string
	Label string
	Note  string
}

// raHotkeyActions are the shortcuts in menu order; the hotkey button first.
var raHotkeyActions = []raHotkeyAction{
	{raHotkeyModifier, "enable_hotkey", "Hotkey button", "Held with every shortcut below, the firmware's own too"},
	{"menu", "menu_toggle", "RetroArch menu", "Opens RetroArch's own menu"},
	{"exit", "exit_emulator", "Quit game", "Ends the game and comes back to JukaHub"},
	{"save", "save_state", "Save state", "Saves a state in the current slot"},
	{"load", "load_state", "Load state", "Loads the state in the current slot"},
	{"slotDown", "state_slot_decrease", "Previous slot", "The state slot before the current one"},
	{"slotUp", "state_slot_increase", "Next slot", "The state slot after the current one"},
	{"ffToggle", "toggle_fast_forward", "Fast forward", "Press to speed up, press again to stop"},
	{"ffHold", "hold_fast_forward", "Fast forward (hold)", "Fast while the buttons are held"},
	{"rewind", "rewind", "Rewind", "Plays backwards while held, where RetroArch's rewind is on"},
	{"shot", "screenshot", "Screenshot", "RetroArch's own screenshot"},
	{"pause", "pause_toggle", "Pause", "Pauses and resumes the game"},
	{"reset", "reset", "Restart game", "Starts the game over, like the console's reset button"},
	{"ai", "ai_service", "Translate screen", "RetroArch's AI service: reads or translates the game's text"},
}

// raHotkeyModifierButtons are the buttons offered as the hotkey button.
var raHotkeyModifierButtons = []string{"select", "menu", "start", "l3", "r3", "l1", "r1", "l2", "r2"}

func raHotkeyActionByID(id string) (raHotkeyAction, bool) {
	for _, a := range raHotkeyActions {
		if a.ID == id {
			return a, true
		}
	}
	return raHotkeyAction{}, false
}

// normaliseRAHotkey keeps a stored value this build knows: "own" or a button
// (the hotkey button only from its own list). "" drops it.
func normaliseRAHotkey(field, value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := raHotkeyActionByID(field); !ok {
		return ""
	}
	if value == raHotkeyOwn {
		return value
	}
	if field == raHotkeyModifier {
		for _, b := range raHotkeyModifierButtons {
			if b == value {
				return value
			}
		}
		return ""
	}
	if raPadButtonKnown(value) {
		return value
	}
	return ""
}

// raHotkeyStore is data/game-hotkeys.json.
var raHotkeyStore = &raLevelStore{name: raHotkeyRecordName, tag: "hotkeys", norm: normaliseRAHotkey}

// raHotkeyReservedSelect are the buttons JukaHub uses with Select during a
// game (gameassist_linux.go, menushot.go, vcursor): button → what for. The
// settings that turn a chord off free its button. c may be nil (all on).
func raHotkeyReservedSelect(c *Config) map[string]string {
	out := map[string]string{"start": "JukaHub's force quit (held)"}
	if c == nil || gameMenuEnabled(c) {
		out["l2"] = "JukaHub's in-game menu"
	}
	if c == nil || gameAssistEnabled(c) {
		out["r2"] = "the game assistant"
	}
	if c == nil || gameShotChordEnabled(c) {
		out["r1"] = "JukaHub's screenshot"
	}
	if c != nil && vcursorGamesEnabled(c) {
		out["l1"] = "the games mouse"
	}
	return out
}

// raHotkeyChordTaken is why modifier + button cannot be a shortcut for
// action, "" when it can. reserved is raHotkeyReservedSelect's map. Pure.
func raHotkeyChordTaken(action, modifier, button string, reserved map[string]string) string {
	if button == "" || button == raHotkeyOwn {
		return ""
	}
	if button == modifier {
		return raPadButtonLabel(button) + " is the hotkey button itself"
	}
	if modifier == "select" {
		if what := reserved[button]; what != "" && !(action == "exit" && button == "start") {
			return "Select + " + raPadButtonLabel(button) + " is " + what
		}
	}
	return ""
}

// raHotkeyPick is one shortcut's resolved button and the level it came from
// (0 every console, 1 the console, 2 the game).
type raHotkeyPick struct {
	Button string
	Level  int
}

// raHotkeyPicksFor is what a launch of game writes: every shortcut with a
// button at some level; "RetroArch's own" ones are left out.
func raHotkeyPicksFor(game libraryGame) map[string]raHotkeyPick {
	console := gameControlsConsole(game.System)
	out := map[string]raHotkeyPick{}
	for _, a := range raHotkeyActions {
		v, level := raHotkeyStore.resolve(console, game.Path, a.ID)
		if v == "" || v == raHotkeyOwn {
			continue
		}
		out[a.ID] = raHotkeyPick{Button: v, Level: level}
	}
	return out
}

// raHotkeyKeyName splits a hotkey bind key ("input_save_state_btn") into
// its shortcut key ("save_state") and kind ("btn" / "axis"); ok is false
// for player binds and anything else. Pure.
func raHotkeyKeyName(key string) (string, string, bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	if !strings.HasPrefix(k, "input_") || strings.HasPrefix(k, "input_player") || strings.HasPrefix(k, "input_libretro") {
		return "", "", false
	}
	switch {
	case strings.HasSuffix(k, "_btn"):
		return strings.TrimSuffix(strings.TrimPrefix(k, "input_"), "_btn"), "btn", true
	case strings.HasSuffix(k, "_axis"):
		return strings.TrimSuffix(strings.TrimPrefix(k, "input_"), "_axis"), "axis", true
	}
	return "", "", false
}

// raHotkeyProfileOn names a firmware hotkey the pad's autoconfig profile puts
// on bind and retroarch.cfg cannot take away, "" when none: RetroArch uses
// the profile's code wherever the config's is unbound, so a config bind that
// is "nul" (or on bind too, which the launch would unbind) leaves the
// profile's. RetroPad and stick binds are not hotkeys, nor except. Pure.
func raHotkeyProfileOn(cfg, profile string, bind raPadBind, except string) string {
	if profile == "" || !bind.ok() {
		return ""
	}
	for _, line := range strings.Split(profile, "\n") {
		k, v, _, ok := raConfigLine(line)
		if !ok || !lightgunBindUsable(v) {
			continue
		}
		name, kind, ok := raHotkeyKeyName(k)
		if !ok || name == "enable_hotkey" || name == except || raPadButtonKnownBind(name) || strings.HasPrefix(name, "l_x_") ||
			strings.HasPrefix(name, "l_y_") || strings.HasPrefix(name, "r_x_") || strings.HasPrefix(name, "r_y_") {
			continue
		}
		code := func(v string) raPadBind {
			if kind == "axis" {
				return raPadBind{Axis: strings.TrimSpace(v)}
			}
			return raPadBind{Btn: strings.TrimSpace(v)}
		}
		if !code(v).same(bind) {
			continue
		}
		if own, has := raConfigGet(cfg, k); has && lightgunBindUsable(own) && !code(own).same(bind) {
			continue // the config's own bind wins
		}
		return name
	}
	return ""
}

// raPadButtonKnownBind reports a RetroPad bind name ("a", "l", "l2", …).
func raPadButtonKnownBind(name string) bool {
	for _, b := range raPadButtons {
		if b.Bind != "" && b.Bind == name {
			return true
		}
	}
	return false
}

// raHotkeyBindValues are the two keys one shortcut writes for bind: the
// button or the axis, and "nul" in the other so an old bind there does not
// fire too.
func raHotkeyBindValues(key string, bind raPadBind) []raScopeValue {
	btn, axis := "nul", "nul"
	if bind.Btn != "" {
		btn = bind.Btn
	} else {
		axis = bind.Axis
	}
	return []raScopeValue{
		{Key: "input_" + key + "_btn", Value: btn},
		{Key: "input_" + key + "_axis", Value: axis},
	}
}

// raHotkeyPlan is what one config gets for a launch.
type raHotkeyPlan struct {
	Values []raScopeValue
	// Notes say what was not applied, for the log and the sheet.
	Notes []string
	// What lists what was applied, for the log.
	What []string
}

// raHotkeyPlanFor builds one config's values from its text, the pad's
// autoconfig profile text ("" when none), the launch's picks and JukaHub's
// Select chords. Pure.
func raHotkeyPlanFor(cfg, profile string, picks map[string]raHotkeyPick, reserved map[string]string) raHotkeyPlan {
	var p raHotkeyPlan
	pad := raPadRead(cfg, profile)

	// The hotkey button: the chosen one when its code is known, else the
	// firmware's (from the config, else the profile), named when it is one
	// of the pad's buttons.
	firmware := raPadBindIn(cfg, "input_", "enable_hotkey")
	if !firmware.ok() && profile != "" {
		firmware = raPadBindIn(profile, "input_", "enable_hotkey")
	}
	modifier, modWritten, modBind := "", false, firmware
	if pick, ok := picks[raHotkeyModifier]; ok {
		bind, ok := pad[pick.Button]
		// A firmware hotkey the profile keeps on the new hotkey button would
		// fire at every press of it (the profile's Quit on Start, and Start
		// made the hotkey button, quits each game at its first Start). Menu
		// alone opening RetroArch's menu is the TrimUI way, and no game uses
		// Menu.
		clash := ""
		if ok && !bind.same(firmware) {
			except := ""
			if pick.Button == "menu" {
				except = "menu_toggle"
			}
			clash = raHotkeyProfileOn(cfg, profile, bind, except)
		}
		switch {
		case clash != "":
			p.Notes = append(p.Notes, "Hotkey button "+raPadButtonLabel(pick.Button)+" not set: the pad's profile keeps RetroArch's "+clash+" on it, which would fire at every press")
		case ok:
			p.Values = append(p.Values, raHotkeyBindValues("enable_hotkey", bind)...)
			modifier, modWritten, modBind = pick.Button, true, bind
			p.What = append(p.What, "hotkey button "+raPadButtonLabel(pick.Button))
		default:
			p.Notes = append(p.Notes, "Hotkey button "+raPadButtonLabel(pick.Button)+" not set: JukaHub cannot read its code in RetroArch's controls")
		}
	}
	if !modWritten {
		modifier = raPadButtonFor(pad, firmware)
	}
	// With no hotkey button at all RetroArch fires every hotkey on its button
	// alone, so a shortcut on X would save a state at every press of X: none
	// is written then. A keyboard-only hotkey button counts (it blocks them).
	keyboard, _ := raConfigGet(cfg, "input_enable_hotkey")
	hasHotkey := modBind.ok() || lightgunBindUsable(keyboard)

	// The shortcuts, the most specific level first: a button two levels
	// chose goes to the closer one.
	type entry struct {
		action raHotkeyAction
		pick   raHotkeyPick
		order  int
	}
	var list []entry
	for i, a := range raHotkeyActions {
		if pick, ok := picks[a.ID]; ok && a.ID != raHotkeyModifier {
			list = append(list, entry{a, pick, i})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].pick.Level != list[j].pick.Level {
			return list[i].pick.Level > list[j].pick.Level
		}
		return list[i].order < list[j].order
	})
	if !hasHotkey && len(list) > 0 {
		p.Notes = append(p.Notes, "Shortcuts not set: this RetroArch has no hotkey button, so each would fire on its own button alone; choose a Hotkey button")
		list = nil
	}
	written := map[string]bool{"enable_hotkey": true}
	var codes []raPadBind
	// A hotkey button JukaHub moved: the firmware's hotkeys on its new button
	// would fire at every press of it, so they are unbound like the others
	// (but for the menu on Menu, the TrimUI way).
	modMoved := modWritten && !modBind.same(firmware)
	byButton := map[string]string{}
	for _, e := range list {
		label := e.action.Label
		button := e.pick.Button
		if other, ok := byButton[button]; ok {
			p.Notes = append(p.Notes, label+" not set: "+raPadButtonLabel(button)+" is "+other+" here")
			continue
		}
		if why := raHotkeyChordTaken(e.action.ID, modifier, button, reserved); why != "" {
			p.Notes = append(p.Notes, label+" not set: "+why)
			continue
		}
		bind, ok := pad[button]
		if !ok {
			p.Notes = append(p.Notes, label+" not set: JukaHub cannot read "+raPadButtonLabel(button)+"'s code in RetroArch's controls")
			continue
		}
		if bind.same(modBind) {
			// The hotkey button by its code (a firmware hotkey button JukaHub
			// could not name).
			p.Notes = append(p.Notes, label+" not set: "+raPadButtonLabel(button)+" is the hotkey button itself")
			continue
		}
		byButton[button] = label
		written[e.action.Key] = true
		codes = append(codes, bind)
		p.Values = append(p.Values, raHotkeyBindValues(e.action.Key, bind)...)
		p.What = append(p.What, label+" "+raPadButtonLabel(button))
	}

	// The firmware's own hotkeys on a button a shortcut now uses, and — when
	// JukaHub made Select the hotkey button — on JukaHub's Select chords, are
	// unbound for the launch.
	var chordCodes []raPadBind
	if modWritten && modifier == "select" {
		for button := range reserved {
			if bind, ok := pad[button]; ok {
				chordCodes = append(chordCodes, bind)
			}
		}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(cfg, "\n") {
		k, v, _, ok := raConfigLine(line)
		if !ok {
			continue
		}
		lk := strings.ToLower(k)
		if seen[lk] {
			continue // RetroArch reads the first line of a key
		}
		seen[lk] = true
		name, kind, ok := raHotkeyKeyName(k)
		if !ok || written[name] || !lightgunBindUsable(v) {
			continue
		}
		bind := raPadBind{Btn: strings.TrimSpace(v)}
		if kind == "axis" {
			bind = raPadBind{Axis: strings.TrimSpace(v)}
		}
		clash := modMoved && modBind.same(bind) && !(modifier == "menu" && name == "menu_toggle")
		for _, c := range codes {
			clash = clash || c.same(bind)
		}
		start, hasStart := pad["start"]
		for _, c := range chordCodes {
			if name == "exit_emulator" && hasStart && c.same(start) {
				continue // Quit may stay on Select + Start
			}
			clash = clash || c.same(bind)
		}
		if clash {
			p.Values = append(p.Values, raScopeValue{Key: "input_" + name + "_" + kind, Value: "nul"})
			p.What = append(p.What, "freed "+name)
		}
	}
	return p
}

// raHotkeyCandidates is a variable so tests could point at a temporary
// config.
var raHotkeyCandidates = retroArchConfigCandidates

// raHotkeysPrepare is the launch hook: the game's shortcuts into every config
// of the RetroArch it starts, for this launch only. It never blocks or fails
// a launch. Main thread.
func raHotkeysPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("hotkeys", "preparing the game's hotkeys failed: %v", r)
		}
	}()
	if !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	picks := raHotkeyPicksFor(game)
	if len(picks) == 0 {
		raHotkeyStore.setNote(game.Path, "")
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raHotkeyCandidates()))
	if len(targets) == 0 {
		logInfo("hotkeys", "no retroarch.cfg found for %s; launching with RetroArch's own hotkeys", launcher)
		raHotkeyStore.setNote(game.Path, "Not applied last time: JukaHub found no RetroArch settings file")
		return
	}
	reserved := raHotkeyReservedSelect(appConfig)
	var notes []string
	for _, cfgPath := range targets {
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		cfg := string(raw)
		profilePath, profile := raPadProfile(cfg, cfgPath)
		plan := raHotkeyPlanFor(cfg, profile, picks, reserved)
		for _, n := range plan.Notes {
			logInfo("hotkeys", "%s: %s (pad profile %q)", cfgPath, n, profilePath)
			dup := false
			for _, s := range notes {
				dup = dup || s == n
			}
			if !dup {
				notes = append(notes, n)
			}
		}
		if len(plan.Values) == 0 {
			continue
		}
		if _, err := raScopeApply(cfgPath, plan.Values); err != nil {
			logWarn("hotkeys", "cannot set the hotkeys in %s: %v", cfgPath, err)
			notes = append(notes, "The hotkeys could not be written")
			continue
		}
		Log().Info("hotkeys applied for this launch", "config", cfgPath, "system", game.System, "hotkeys", strings.Join(plan.What, ", "))
	}
	raHotkeyStore.setNote(game.Path, strings.Join(notes, " · "))
}

func init() {
	gameLaunchRegisterPrepare(raHotkeysPrepare)
}
