package main

// ctrllayout_apply.go — what a game's control layout does when the game
// starts.
//
// A layout touches two of RetroArch's files, both for one launch only:
//
//   - its Buttons go into the one-launch remap file the Buttons choice already
//     writes (racontrols_remap.go), so there is ONE writer, one marker line,
//     one cleanup when RetroArch exits, and the same rule that a remap the
//     player saved in RetroArch for the game, its folder or its core is never
//     shadowed. prepareGameControls lays the layout's buttons over the Buttons
//     choice's (ctrlLayoutMergeRemap): the layout wins for the buttons it
//     sets, the Buttons choice keeps the rest. A button sent to a stick
//     direction is RetroArch's digital-to-analog remap: the button's number in
//     the file is that stick direction's bind (16–23), and the core reads a
//     full push of the stick.
//   - its CoreOptions are set in the core's options file through raScopeApply
//     (ratuning.go's raTuneCoreOptionFile picks the file: the game's .opt, its
//     folder's, the core's or the global one), put back when RetroArch exits.
//     A core that has never run has no options file yet; that launch skips
//     them, says so in the log, and the picker shows it.
//
// Keys (keyboard keys for DOSBox Pure or ScummVM) are not written. RetroArch
// keeps a keyboard mapper in remap files (input_player1_key_<button> = "<key
// number>"), but whether it acts depends on the port's device type and the
// core, and that is not verified on the handhelds' RetroArch builds; a key
// map that silently does nothing is worse than a chart that says what the
// core's own mapping does. The keys are checked (ctrlLayoutKeyCode) and
// shown, the launch logs that they were not sent, and the picker says so.
//
// Standalone emulators and NextUI paks read neither file: a layout does not
// apply there, and the picker says the emulator keeps its own controls.

import (
	"sort"
	"strings"
)

// ctrlLayoutStickTargets are the stick directions a button can push, by the
// number RetroArch's remap files use for them (RARCH_ANALOG_LEFT_X_PLUS = 16 …
// RARCH_ANALOG_RIGHT_Y_MINUS = 23, input_defines.h; Y+ is down, as on
// libretro's analog axes).
var ctrlLayoutStickTargets = map[string]int{
	"lstick-right": 16,
	"lstick-left":  17,
	"lstick-down":  18,
	"lstick-up":    19,
	"rstick-right": 20,
	"rstick-left":  21,
	"rstick-down":  22,
	"rstick-up":    23,
}

// ctrlLayoutTarget is the remap number a layout's button value stands for:
// a RetroPad button, a stick direction, or -1 for "none".
func ctrlLayoutTarget(name string) (int, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "none" {
		return -1, true
	}
	if id := raRemapButtonID(name); id >= 0 {
		return id, true
	}
	n, ok := ctrlLayoutStickTargets[name]
	return n, ok
}

// ctrlLayoutKeyNames are the keyboard keys a layout's Keys may name, by
// RetroArch's own key names (input_config_key_label) plus a few plain
// aliases, with libretro's RETROK numbers (libretro.h).
var ctrlLayoutKeyNames = map[string]int{
	"backspace": 8, "tab": 9, "enter": 13, "return": 13, "pause": 19,
	"escape": 27, "esc": 27, "space": 32, "quote": 39, "comma": 44,
	"minus": 45, "period": 46, "slash": 47, "semicolon": 59, "equals": 61,
	"leftbracket": 91, "backslash": 92, "rightbracket": 93, "backquote": 96, "tilde": 96,
	"del": 127, "delete": 127, "kp_period": 266, "divide": 267, "multiply": 268,
	"subtract": 269, "kp_minus": 269, "add": 270, "kp_plus": 270, "kp_enter": 271,
	"up": 273, "down": 274, "right": 275, "left": 276, "insert": 277,
	"home": 278, "end": 279, "pageup": 280, "pagedown": 281,
	"rshift": 303, "shift": 304, "lshift": 304, "rctrl": 305, "ctrl": 306,
	"lctrl": 306, "ralt": 307, "alt": 308, "lalt": 308,
}

// ctrlLayoutKeyCode is a key name's RETROK number: the table above, a–z,
// 0–9 ("num0" too), keypad digits ("keypad0") and f1–f12.
func ctrlLayoutKeyCode(name string) (int, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if n, ok := ctrlLayoutKeyNames[name]; ok {
		return n, true
	}
	digit := func(s string) (int, bool) {
		if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
			return int(s[0] - '0'), true
		}
		return 0, false
	}
	switch {
	case len(name) == 1 && name[0] >= 'a' && name[0] <= 'z':
		return int(name[0]), true
	case len(name) == 1:
		if d, ok := digit(name); ok {
			return '0' + d, true
		}
	case strings.HasPrefix(name, "num"):
		if d, ok := digit(name[3:]); ok {
			return '0' + d, true
		}
	case strings.HasPrefix(name, "keypad"):
		if d, ok := digit(name[6:]); ok {
			return 256 + d, true
		}
	case strings.HasPrefix(name, "f"):
		n := 0
		for _, r := range name[1:] {
			if r < '0' || r > '9' {
				return 0, false
			}
			n = n*10 + int(r-'0')
		}
		if n >= 1 && n <= 12 {
			return 281 + n, true
		}
	}
	return 0, false
}

// ctrlLayoutResolved is a layout as one launch uses it: for one core, on this
// device.
type ctrlLayoutResolved struct {
	Layout ctrlLayout
	Core   string
	// Buttons maps a pad button to the remap number the core is handed.
	Buttons     map[string]int
	CoreOptions map[string]string
	Keys        map[string]string
	Chart       []ctrlLayoutLine
}

// ctrlLayoutVariantMatches reports whether v applies to core on a device with
// (sticks) or without analog sticks.
func ctrlLayoutVariantMatches(v ctrlLayoutVariant, core string, sticks bool) bool {
	if v.NoSticks && sticks {
		return false
	}
	if len(v.Cores) == 0 {
		return true
	}
	for _, c := range v.Cores {
		if strings.EqualFold(strings.TrimSpace(c), core) {
			return true
		}
	}
	return false
}

// ctrlLayoutResolve lays every matching variant over the layout, in order.
// Names it cannot read are left out (a registered layout from another
// feature is not validated the way the player's files are). Pure.
func ctrlLayoutResolve(l ctrlLayout, core string, sticks bool) ctrlLayoutResolved {
	core = normalizeCoreName(core)
	r := ctrlLayoutResolved{
		Layout:      l,
		Core:        core,
		Buttons:     map[string]int{},
		CoreOptions: map[string]string{},
		Keys:        map[string]string{},
		Chart:       l.Chart,
	}
	put := func(buttons map[string]string) {
		for from, to := range buttons {
			pad := strings.ToLower(strings.TrimSpace(from))
			n, ok := ctrlLayoutTarget(to)
			if raRemapButtonID(pad) < 0 || !ok {
				continue
			}
			r.Buttons[pad] = n
		}
	}
	options := func(opts map[string]string) {
		for k, v := range opts {
			if k = strings.TrimSpace(k); ctrlLayoutSafeOption(k, v) {
				r.CoreOptions[k] = v
			}
		}
	}
	put(l.Buttons)
	options(l.CoreOptions)
	for from, key := range l.Keys {
		pad := strings.ToLower(strings.TrimSpace(from))
		if _, ok := ctrlLayoutKeyCode(key); ok && raRemapButtonID(pad) >= 0 {
			r.Keys[pad] = strings.ToLower(strings.TrimSpace(key))
		}
	}
	for _, v := range l.Variants {
		if !ctrlLayoutVariantMatches(v, core, sticks) {
			continue
		}
		put(v.Buttons)
		options(v.CoreOptions)
		if len(v.Chart) > 0 {
			r.Chart = v.Chart
		}
	}
	return r
}

// ctrlLayoutMergeRemap lays a layout's buttons over the Buttons choice's
// (base, raRemapPreset): the layout wins for the buttons it sets. Buttons
// that end up sending themselves are dropped, so an empty result means no
// remap file is needed. Pure.
func ctrlLayoutMergeRemap(base map[string]int, r ctrlLayoutResolved) map[string]int {
	out := make(map[string]int, len(base)+len(r.Buttons))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range r.Buttons {
		out[k] = v
	}
	for k, v := range out {
		if v == raRemapButtonID(k) {
			delete(out, k)
		}
	}
	return out
}

// ctrlLayoutRemapLabel names what a remap file carries, for the log.
func ctrlLayoutRemapLabel(mode string, r ctrlLayoutResolved) string {
	if r.Layout.ID == "" || len(r.Buttons) == 0 {
		return mode
	}
	if mode == "" || mode == gameButtonsDefault {
		return "layout " + r.Layout.ID
	}
	return mode + " + layout " + r.Layout.ID
}

// ctrlLayoutPadOrder is the order the picker names a layout's buttons in.
var ctrlLayoutPadOrder = []string{
	"y", "x", "b", "a", "l", "r", "l2", "r2", "l3", "r3",
	"up", "down", "left", "right", "select", "start",
}

// ctrlLayoutPadLabel is a pad button's name on the handheld ("l" is L1).
func ctrlLayoutPadLabel(name string) string {
	switch name {
	case "l":
		return "L1"
	case "r":
		return "R1"
	case "select":
		return "Select"
	case "start":
		return "Start"
	case "up", "down", "left", "right":
		return "D-pad " + name
	}
	return strings.ToUpper(name)
}

// ctrlLayoutSetButtons lists, by their handheld names, the pad buttons a
// resolved layout decides.
func ctrlLayoutSetButtons(r ctrlLayoutResolved) []string {
	var out []string
	for _, name := range ctrlLayoutPadOrder {
		if _, ok := r.Buttons[name]; ok {
			out = append(out, ctrlLayoutPadLabel(name))
		}
	}
	return out
}

// ctrlLayoutLaunchFor is the game's layout resolved for the launcher, and
// whether one applies: a RetroArch launch (not a NextUI pak) of a game with
// a layout.
func ctrlLayoutLaunchFor(launcher string, game libraryGame) (ctrlLayoutResolved, bool) {
	if !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return ctrlLayoutResolved{}, false
	}
	ctrlLayoutUserEnsure() // a power-on resume can beat the startup read
	l, ok := ctrlLayoutForGame(game)
	if !ok {
		return ctrlLayoutResolved{}, false
	}
	return ctrlLayoutResolve(l, gameCoreFromLauncher(launcher, game.System), stickHardware()), true
}

// ctrlLayoutPrepare is the launch hook (gamelaunch_hooks.go), run right after
// prepareGameControls has written the remap: the layout's core options, and
// the note of what was not applied. It never blocks or fails a launch.
func ctrlLayoutPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("controls", "preparing the control layout failed: %v", r)
		}
	}()
	r, ok := ctrlLayoutLaunchFor(launcher, game)
	if !ok {
		ctrlLayoutSetNote(game.Path, "")
		return
	}
	var notes []string
	if len(r.CoreOptions) > 0 {
		if note := ctrlLayoutApplyOptions(launcher, game, r); note != "" {
			notes = append(notes, note)
		}
	}
	if len(r.Keys) > 0 {
		logInfo("controls", "layout %s: its keyboard keys are not sent (RetroArch's keyboard mapper is not verified on this build); the game's own key mapping applies", r.Layout.ID)
		notes = append(notes, "Its keyboard keys are not set by JukaHub yet")
	}
	ctrlLayoutSetNote(game.Path, strings.Join(notes, " · "))
	Log().Info("control layout for this launch", "layout", r.Layout.ID, "core", r.Core,
		"buttons", len(r.Buttons), "options", len(r.CoreOptions), "sticks", stickHardware())
}

// ctrlLayoutApplyOptions sets the layout's core options for the launch. It
// returns the picker's note when they could not be set, "" when they were.
func ctrlLayoutApplyOptions(launcher string, game libraryGame, r ctrlLayoutResolved) string {
	if len(raTuneCoreFolders[r.Core]) == 0 {
		logInfo("controls", "layout %s: JukaHub does not know where %s keeps its options; they are skipped", r.Layout.ID, r.Core)
		return "Its emulator options are not set for this core"
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		logInfo("controls", "no retroarch.cfg found for %s; layout %s's options skipped", launcher, r.Layout.ID)
		return "Its emulator options were not set: JukaHub found no RetroArch settings file"
	}
	opt := raTuneCoreOptionFile(targets[0], r.Core, game.Path)
	if opt == "" {
		logInfo("controls", "%s has no options file yet (start it once from RetroArch); layout %s's options skipped for this launch", r.Core, r.Layout.ID)
		return "Its emulator options were not set: start the game once from RetroArch, then again from here"
	}
	keys := make([]string, 0, len(r.CoreOptions))
	for k := range r.CoreOptions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]raScopeValue, 0, len(keys))
	for _, k := range keys {
		values = append(values, raScopeValue{Key: k, Value: r.CoreOptions[k]})
	}
	if _, err := raScopeApply(opt, values); err != nil {
		logWarn("controls", "cannot set layout %s's options in %s: %v", r.Layout.ID, opt, err)
		return "Its emulator options could not be written"
	}
	return ""
}

func init() {
	gameLaunchRegisterPrepare(ctrlLayoutPrepare)
}
