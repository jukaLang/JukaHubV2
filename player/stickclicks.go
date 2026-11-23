package main

// stickclicks.go — which handhelds can press their sticks in (L3 / R3).
//
// No JukaHub screen needs a stick click (owner decision, 2026-11-22). The
// original Smart Pro's sticks do not press in; the Brick and the Hammer have
// no sticks, and their front F1 / F2 keys, which the firmware reports as
// L3 / R3, do the same job on every screen (brickkeys.go); only the Smart
// Pro S and the Brick Pro click theirs (deviceHW.ClickSticks). So every
// action a screen has sits on a button every model has — the D-pad, A, B, X,
// Y, L1, R1, L2, R2, Select, Start and Menu — or in the screen's menu, and a
// hint never names a button the handheld in hand lacks.
//
// L3 / R3 stay what a game reads. The helpers that hand them to a game or
// let one be chosen (Game hotkeys, a control layout's chart, RetroArch's
// hotkey rows, the one-handed chord, the controller test) ask here: a button
// nothing on the handheld sends is not offered, and on a Brick the two are
// named after the keys that send them.

import "strings"

// padHasStickClicks reports whether the handheld's own sticks press in. A PC
// or an unidentified board is taken to hold a standard pad, which has them.
func padHasStickClicks() bool {
	d := brickKeysDevice()
	if !d.IsTrimui() {
		return true
	}
	return deviceHardware(d).ClickSticks
}

// padSendsStickClicks reports whether anything on the handheld sends L3 /
// R3: sticks that press in, or a Brick's front F1 / F2 keys.
func padSendsStickClicks() bool {
	return padHasStickClicks() || brickHasFunctionKeys()
}

// padStickClickName names L3 or R3 after what sends it here: F1 / F2 on a
// Brick or a Hammer, the stick clicks everywhere else. Any other name is
// returned as it is.
func padStickClickName(name string) string {
	if !brickHasFunctionKeys() {
		return name
	}
	switch name {
	case "L3":
		return "F1"
	case "R3":
		return "F2"
	}
	return name
}

// padChartLines is a control chart as this handheld can use it: a line for
// L3 or R3 (the DS stylus layout closes the lid with L3) is left out where
// nothing sends it, and named F1 / F2 on a Brick. The chart is returned as it
// is when it has no such line.
func padChartLines(lines []ctrlLayoutLine) []ctrlLayoutLine {
	found := false
	for _, l := range lines {
		if b := strings.TrimSpace(l.Button); b == "L3" || b == "R3" {
			found = true
			break
		}
	}
	if !found {
		return lines
	}
	sends := padSendsStickClicks()
	out := make([]ctrlLayoutLine, 0, len(lines))
	for _, l := range lines {
		if b := strings.TrimSpace(l.Button); b == "L3" || b == "R3" {
			if !sends {
				continue
			}
			l.Button = padStickClickName(b)
		}
		out = append(out, l)
	}
	return out
}
