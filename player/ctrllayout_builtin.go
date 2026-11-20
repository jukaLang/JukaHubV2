package main

// ctrllayout_builtin.go — the control layouts JukaHub ships: two arcade
// layouts and a DS stylus.
//
// A layout remaps what the pad sends to the core, so it has to start from how
// the core already hands the arcade buttons to the RetroPad. Those defaults
// differ by core, which is why the arcade layouts carry a variant per core:
//
//	FinalBurn Neo, "Classic" device (the default; retro_input.cpp):
//	  CPS 6-button   Y light punch, X medium, L heavy; B light kick, A medium, R heavy
//	                 (L2 all three punches, R2 all three kicks)
//	  Neo Geo        A on B, B on A, C on Y, D on X ("BAYX")
//	MAME 2003-Plus, "Classic Gamepad" device (the default; mame2003.c):
//	                 Button 1..6 on B, A, Y, X, L, R
//
// The 6-button layout is the SNES Street Fighter II one — punches on Y, X and
// L1, kicks on B, A and R1 — which FinalBurn Neo already uses, so for it the
// layout only pins those six buttons against a Swap choice; under MAME
// 2003-Plus it moves them there. The Neo Geo layout keeps the same idea with
// four buttons: A (light punch) on Y, B (light kick) on B, C (heavy punch) on
// X, D (heavy kick) on A, so a player moving between Street Fighter and King
// of Fighters keeps punches on the left and top and kicks on the bottom and
// right. FinalBurn Neo's Classic Neo Geo order and MAME 2003-Plus's buttons 1
// to 4 land on the same RetroPad buttons, so one remap serves both. A core
// named nowhere here (MAME 2010, FB Alpha 2012) gets FinalBurn Neo's. A player
// who switched the core's own device type to "Modern" or "Fightstick" in
// RetroArch has moved the buttons already; these charts then read wrong, and
// "None" is the right choice for that game.
//
// The DS stylus needs the core's touch option as well as buttons. melonDS
// reads the stylus from the right stick only in its "Joystick" touch mode
// (melonds_touch_mode; R3 touches, R2 swaps the screens); melonDS DS moves its
// cursor with the right stick and touches with R3 out of the box (R2 steps to
// the next screen layout); DeSmuME moves a pointer with the right stick when
// desmume_pointer_device_r is "emulated" and taps with R2. The layout puts the
// tap on R2 for all three (pressing the stick you aim with is awkward) and the
// screen swap on L2 where the core has one. On a Brick, with no sticks, the
// D-pad pushes the right stick instead: the stylus moves with the D-pad, and
// the DS's own D-pad is off while the layout is in use — fine for the
// stylus-driven games this is for, and "None" puts it back.
//
// None of the built-in layouts is a console's default: a remap nobody asked
// for is the surprise this feature exists to avoid. X in the picker makes one
// the default for every game of a console.

import "strings"

func init() {
	for _, l := range ctrlLayoutBuiltins() {
		l.Source = "builtin"
		ctrlLayoutRegister(l)
	}
}

// ctrlLayoutLines builds a chart from "Button|Action" pairs.
func ctrlLayoutLines(pairs ...string) []ctrlLayoutLine {
	out := make([]ctrlLayoutLine, 0, len(pairs))
	for _, p := range pairs {
		button, action, _ := strings.Cut(p, "|")
		out = append(out, ctrlLayoutLine{Button: button, Action: action})
	}
	return out
}

// The cores the variants name (gameCoreFromLauncher's names).
var (
	ctrlLayoutMAMECores     = []string{"mame2003_plus"}
	ctrlLayoutDeSmuMECores  = []string{"desmume", "desmume2015"}
	ctrlLayoutMelonDSCores  = []string{"melonds"}
	ctrlLayoutMelonDSDSCore = []string{"melondsds"}
)

func ctrlLayoutBuiltins() []ctrlLayout {
	sixChart := ctrlLayoutLines(
		"Y|Light punch", "X|Medium punch", "L1|Heavy punch",
		"B|Light kick", "A|Medium kick", "R1|Heavy kick")
	return []ctrlLayout{
		{
			ID:          "arcade-6",
			Name:        "Arcade 6-button",
			Description: "Capcom fighters: punches on Y, X and L1, kicks on B, A and R1",
			Systems:     []string{"Arcade"},
			// FinalBurn Neo already sends these; set so a Swap choice leaves them.
			Buttons: map[string]string{"y": "y", "x": "x", "l": "l", "b": "b", "a": "a", "r": "r"},
			Chart: append(append([]ctrlLayoutLine(nil), sixChart...),
				ctrlLayoutLines("L2|All three punches", "R2|All three kicks")...),
			Variants: []ctrlLayoutVariant{
				{
					// Buttons 1..6 (light, medium, heavy punch; light, medium,
					// heavy kick) sit on B, A, Y, X, L, R.
					Cores:   ctrlLayoutMAMECores,
					Buttons: map[string]string{"y": "b", "x": "a", "l": "y", "b": "x", "a": "l", "r": "r"},
					Chart:   sixChart,
				},
			},
		},
		{
			ID:          "arcade-4-neogeo",
			Name:        "Arcade 4-button (Neo Geo)",
			Description: "A, B, C, D on Y, B, X, A: punches left and top, kicks bottom and right",
			Systems:     []string{"Neo Geo", "Neo Geo CD", "Arcade"},
			Buttons:     map[string]string{"y": "b", "b": "a", "x": "y", "a": "x"},
			Chart: ctrlLayoutLines(
				"Y|A · light punch", "B|B · light kick",
				"X|C · heavy punch", "A|D · heavy kick"),
		},
		{
			ID:          "ds-stylus",
			Name:        "DS stylus",
			Description: "Aim the stylus with the right stick (the D-pad on a Brick), tap with R2",
			Systems:     []string{"Nintendo DS"},
			// melonDS and melonDS DS: R3 touches, R2 swaps (next layout).
			Buttons: map[string]string{"r2": "r3", "l2": "r2"},
			Chart: ctrlLayoutLines(
				"Right stick|Moves the stylus", "R2 or R3|Taps the screen", "L2|Swaps the screens", "L3|Closes the lid"),
			Variants: []ctrlLayoutVariant{
				{
					Cores:       ctrlLayoutMelonDSCores,
					CoreOptions: map[string]string{"melonds_touch_mode": "Joystick"},
				},
				{
					Cores: ctrlLayoutMelonDSDSCore,
					Chart: ctrlLayoutLines(
						"Right stick|Moves the stylus", "R2 or R3|Taps the screen", "L2|Next screen layout", "L3|Closes the lid"),
				},
				{
					// DeSmuME taps with R2 itself; L2 stays its lid.
					Cores:       ctrlLayoutDeSmuMECores,
					Buttons:     map[string]string{"r2": "r2", "l2": "l2"},
					CoreOptions: map[string]string{"desmume_pointer_device_r": "emulated"},
					Chart:       ctrlLayoutLines("Right stick|Moves the stylus", "R2|Taps the screen", "L2|Closes the lid"),
				},
				{
					NoSticks: true,
					Buttons: map[string]string{
						"up": "rstick-up", "down": "rstick-down", "left": "rstick-left", "right": "rstick-right",
					},
					Chart: ctrlLayoutLines(
						"D-pad|Moves the stylus (not the DS's D-pad)", "R2|Taps the screen", "L2|Swaps the screens"),
				},
				{
					Cores:    ctrlLayoutMelonDSDSCore,
					NoSticks: true,
					Chart: ctrlLayoutLines(
						"D-pad|Moves the stylus (not the DS's D-pad)", "R2|Taps the screen", "L2|Next screen layout"),
				},
				{
					Cores:    ctrlLayoutDeSmuMECores,
					NoSticks: true,
					Chart: ctrlLayoutLines(
						"D-pad|Moves the stylus (not the DS's D-pad)", "R2|Taps the screen", "L2|Closes the lid"),
				},
			},
		},
	}
}
