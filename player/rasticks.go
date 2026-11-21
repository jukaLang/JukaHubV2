package main

// rasticks.go — what the left stick does in RetroArch games on a handheld
// with sticks (the Smart Pro, Smart Pro S and Brick Pro; stickHardware).
//
// Most consoles JukaHub plays had no stick: their games read a D-pad, and the
// stick on a Smart Pro sits right where a thumb rests. RetroArch can turn the
// left stick into the D-pad ("Analog to digital type",
// input_player1_analog_dpad_mode: 0 none, 1 left stick, 2 right stick, 3 and
// 4 the same "forced" even when the core asks for an analog pad), but that is
// one choice for every game, and the N64, PSP or Dreamcast game that reads
// the stick as a stick must not have it turned into four buttons.
//
// So "Left stick as D-pad" is a per-console choice, in Settings → Controls
// beside the Buttons and Turbo of racontrols_ui.go, shown only on a handheld
// with sticks:
//
//	All consoles  Auto (the default), On, Off, RetroArch's own
//	a console     Default · <what All consoles gives it>, On, Off, RetroArch's own
//
// Auto is On for every console whose pads had no stick, and Off for the
// Nintendo 64, PSP, Dreamcast, PlayStation 2, GameCube and Wii, whose games
// read it. The PlayStation follows the pad its game gets (psxanalog.go): a
// game played with the DualShock reads the sticks as sticks — psxanalog.go
// already writes mode 0 for it, and this file leaves that alone whatever the
// choice — and a game on the standard pad gets the stick as its D-pad. The
// Saturn, DS and the arcade systems are On: their pads (and the cores'
// default devices) are digital, so Off would only leave the stick dead.
//
// On writes 1, not the forced 3: an analog device the core asks for keeps its
// stick. Off writes 0. RetroArch's own writes nothing. Like every launch-time
// key, the value goes through raScopeApply (racfgscope.go): for that start
// only, put back when RetroArch exits. A remap file the user saved in
// RetroArch that sets the mode loads after retroarch.cfg and wins. A handheld
// without sticks (the Brick, the Hammer) gets nothing written; there the
// other direction, the D-pad driving the stick, is racontrols_dpad.go's
// "D-pad as stick".
//
// The PlayStation DualShock itself, with both sticks, is psxanalog.go's
// "Analog controller". Its L3 and R3 (the stick clicks) come from the
// handheld pad's RetroArch profile; a DualShock launch logs whether that
// profile binds them ("L3/R3: …" under controls), since JukaHub cannot know
// the pad's button numbers to write them itself.

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// raSticksLeftKey is the all-consoles Custom key; a console's own is
	// gameControls.<console slug>.leftStick (gameControlsConsoleKey).
	raSticksLeftKey  = "gameControls.leftStick"
	raSticksLeftWhat = "leftStick"

	raSticksAuto = "auto"
	raSticksOn   = "on"
	raSticksOff  = "off"
	// raTuneOwn ("own") is RetroArch's own: nothing written.

	// raSticksDpadKey is RetroArch's analog-to-digital key for player 1.
	raSticksDpadKey = "input_player1_analog_dpad_mode"
)

// raSticksAnalogConsoles are the consoles whose games read the stick: Auto
// leaves it a stick there. Names as raTuneSystemFor / canonicalCompatSystem
// give them.
var raSticksAnalogConsoles = map[string]bool{
	"Nintendo 64":          true,
	"PlayStation Portable": true,
	"Dreamcast":            true,
	"PlayStation 2":        true,
	"GameCube":             true,
	"Wii":                  true,
}

func normaliseRASticksLeft(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case raSticksAuto, raSticksOn, raSticksOff, raTuneOwn:
		return v
	}
	return ""
}

// raSticksLeftAll is the all-consoles choice; unset is Auto.
func raSticksLeftAll(c *Config) string {
	if v := normaliseRASticksLeft(gameControlsRaw(c, raSticksLeftKey)); v != "" {
		return v
	}
	return raSticksAuto
}

// raSticksLeftConsoleOwn is a console's own choice, "" when it follows All
// consoles. Auto is not a console's choice (it already is one per console).
func raSticksLeftConsoleOwn(c *Config, console string) string {
	if v := normaliseRASticksLeft(gameControlsRaw(c, gameControlsConsoleKey(console, raSticksLeftWhat))); v != raSticksAuto {
		return v
	}
	return ""
}

// raSticksLeftFor is a console's choice as a launch reads it: its own, else
// All consoles' (which may be Auto).
func raSticksLeftFor(c *Config, system string) string {
	if v := raSticksLeftConsoleOwn(c, gameControlsConsole(system)); v != "" {
		return v
	}
	return raSticksLeftAll(c)
}

// raSticksAutoOn is Auto's answer for a console: whether the stick is its
// D-pad (the PlayStation: for a standard-pad game).
func raSticksAutoOn(console string) bool {
	return !raSticksAnalogConsoles[canonicalCompatSystem(console)]
}

// raSticksDecision is what a launch does with the left stick.
type raSticksDecision struct {
	Mode      string // "1" (D-pad), "0" (a stick), or "" to write nothing
	Why       string // for the log
	DualShock bool   // a PlayStation game on the DualShock (psxanalog.go)
}

// raSticksLeftDecide is a launch's answer. sticks is whether the handheld has
// sticks. Pure but for the PlayStation pad's per-game record.
func raSticksLeftDecide(c *Config, game libraryGame, sticks bool) raSticksDecision {
	if !sticks {
		return raSticksDecision{Why: "no sticks on this handheld"}
	}
	if psxAnalogIsGame(game) {
		if d := psxAnalogDecide(c, game, sticks); d.Pad == psxAnalogPadDualShock {
			return raSticksDecision{Why: "DualShock, " + d.Why, DualShock: true}
		}
	}
	switch raSticksLeftFor(c, game.System) {
	case raSticksOn:
		return raSticksDecision{Mode: "1", Why: "On"}
	case raSticksOff:
		return raSticksDecision{Mode: "0", Why: "Off"}
	case raSticksAuto:
		if raSticksAutoOn(gameControlsConsole(game.System)) {
			return raSticksDecision{Mode: "1", Why: "Auto"}
		}
		return raSticksDecision{Mode: "0", Why: "Auto, its games read the stick"}
	}
	return raSticksDecision{Why: "RetroArch's own"}
}

// ── Launch ───────────────────────────────────────────────────────────────

// raSticksCandidates is a variable so tests could point at a temporary
// config.
var raSticksCandidates = retroArchConfigCandidates

// raSticksPrepare is the launch hook (gamelaunch_hooks.go). It never blocks or
// fails a launch. Main thread.
func raSticksPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("controls", "setting the left stick failed: %v", r)
		}
	}()
	if appConfig == nil || !stickHardware() || !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	d := raSticksLeftDecide(appConfig, game, true)
	targets := raScopeTargets(raLaunchCandidates(launcher, raSticksCandidates()))
	if len(targets) == 0 {
		return
	}
	if d.DualShock {
		raSticksLogL3R3(targets[0])
	}
	if d.Mode == "" {
		logInfo("controls", "left stick: left as it is (%s)", d.Why)
		return
	}
	for _, cfgPath := range targets {
		if _, err := raScopeApply(cfgPath, []raScopeValue{{Key: raSticksDpadKey, Value: d.Mode}}); err != nil {
			logWarn("controls", "left stick: cannot set it in %s: %v", cfgPath, err)
		}
	}
	what := "a stick"
	if d.Mode == "1" {
		what = "the D-pad"
	}
	logInfo("controls", "left stick: %s for %s (%s)", what, gameControlsConsole(game.System), d.Why)
}

// raSticksBound reports a bind that is a button, a hat or an axis, not
// "nul" or empty.
func raSticksBound(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && !strings.EqualFold(v, "nul")
}

// raSticksLogL3R3 logs whether RetroArch has L3 and R3 for player 1: a
// player-1 bind in retroarch.cfg, else the handheld pad's autoconfig profile
// (raPadProfile). Log only; card I/O on a DualShock launch.
func raSticksLogL3R3(cfgPath string) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	cfg := string(raw)
	missing := []string{}
	profile, text := raPadProfile(cfg, cfgPath)
	for _, b := range []string{"l3", "r3"} {
		if v, ok := raConfigGet(cfg, "input_player1_"+b+"_btn"); ok && raSticksBound(v) {
			continue
		}
		if profile != "" {
			if v, ok := raConfigGet(text, "input_"+b+"_btn"); ok && raSticksBound(v) {
				continue
			}
			if v, ok := raConfigGet(text, "input_"+b+"_axis"); ok && raSticksBound(v) {
				continue
			}
		}
		missing = append(missing, strings.ToUpper(b))
	}
	where := "no TrimUI pad profile found"
	if profile != "" {
		where = filepath.Base(profile)
	}
	if len(missing) == 0 {
		logInfo("controls", "L3/R3: bound for the DualShock (%s)", where)
		return
	}
	logInfo("controls", "L3/R3: %s not bound in retroarch.cfg or %s; the DualShock's stick clicks may do nothing", strings.Join(missing, " and "), where)
}

// ── Settings → Controls ──────────────────────────────────────────────────

func raSticksLabel(mode string) string {
	switch mode {
	case raSticksAuto:
		return "Auto"
	case raSticksOn:
		return "On"
	case raSticksOff:
		return "Off"
	}
	return "RetroArch's own"
}

// raSticksDefaultLabel is what a console's Default stop gives it: the All
// consoles choice, with Auto worked out for the console.
func raSticksDefaultLabel(c *Config, console string) string {
	all := raSticksLeftAll(c)
	if all != raSticksAuto {
		return raSticksLabel(all)
	}
	if canonicalCompatSystem(console) == psxAnalogConsole {
		return "On, a stick with the DualShock"
	}
	if raSticksAutoOn(console) {
		return "On"
	}
	return "Off"
}

func raSticksLeftOptions(c *Config) []setOption {
	out := make([]setOption, 0, 4)
	if gameControlsScope == "" {
		out = append(out, setOption{Value: raSticksAuto, Label: "Auto",
			Note: "The D-pad on consoles without a stick; a stick on N64, PSP and Dreamcast, and for PlayStation games on the DualShock"})
	} else {
		out = append(out, setOption{Value: "", Label: "Default · " + raSticksDefaultLabel(c, gameControlsScope), Note: raTuneDefaultNote})
	}
	return append(out,
		setOption{Value: raSticksOn, Label: "On", Note: "The left stick works the D-pad"},
		setOption{Value: raSticksOff, Label: "Off", Note: "The left stick stays a stick; a game without one ignores it"},
		setOption{Value: raTuneOwn, Label: "RetroArch's own", Note: "Leave RetroArch's analog-to-digital setting alone"})
}

func raSticksLeftStored(c *Config) string {
	if gameControlsScope == "" {
		return raSticksLeftAll(c)
	}
	return raSticksLeftConsoleOwn(c, gameControlsScope)
}

// raSticksLeftRow is the Controls page's "Left stick as D-pad" row, scoped
// by "Game buttons for" like Buttons and Turbo. Handhelds with sticks only.
func raSticksLeftRow() setRow {
	return setRow{
		ID: "raSticksLeft", Label: "Left stick as D-pad", Kind: setKindChoice,
		Visible: func(*Config) bool { return stickHardware() },
		Detail: func(*Config) string {
			switch gameControlsScope {
			case "":
				return "RetroArch games · Auto keeps it a stick where the games read one"
			case psxAnalogConsole:
				return "PlayStation games on the standard pad · a DualShock game keeps both sticks"
			}
			return gameControlsScope + " games only"
		},
		Options: raSticksLeftOptions,
		Current: func(c *Config) int { return raTuneOptionIndex(raSticksLeftOptions(c), raSticksLeftStored(c)) },
		Choose: func(c *Config, value string) {
			value = normaliseRASticksLeft(value)
			if gameControlsScope != "" && value == raSticksAuto {
				value = ""
			}
			if gameControlsScope == "" && value == "" {
				value = raSticksAuto
			}
			setStoreString(c, gameControlsScopedKey(raSticksLeftKey, raSticksLeftWhat), value)
			label := raSticksLabel(value)
			if value == "" {
				label = "Default"
			}
			what := "Left stick as D-pad"
			if gameControlsScope != "" {
				what += " · " + gameControlsScope
			}
			setToast(what + ": " + label)
		},
	}
}

func init() {
	gameLaunchRegisterPrepare(raSticksPrepare)
}
