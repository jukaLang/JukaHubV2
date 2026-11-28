package main

// racontrols_map.go — a game's own per-button map: each RetroPad button can
// be pointed at any RetroPad button (or None) for that game, beyond the
// Buttons presets and the controls layout.
//
// The map lives in the game's game-controls.json record, beside its Buttons
// and Turbo choices, and is laid over them when the one-launch remap file is
// written (prepareGameControls): the preset and the layout decide the buttons
// the map does not name, the map decides the rest. Values are RetroArch's
// RETRO_DEVICE_ID_JOYPAD numbers, -1 for "None".
//
// The game options sheet shows one row per button (game_options_ui.go), each
// cycling through the RetroPad's buttons; the launch path and the cleanup are
// the same remap file the Buttons choice uses, so a remap the player saved in
// RetroArch is still never shadowed, and standalone emulators (which read no
// remap file) are left alone.

import "strings"

// gameButtonsMapTargets are the values a row cycles through: every RetroPad
// button, then None.
func gameButtonsMapTargets() []int {
	targets := make([]int, 0, len(raRemapButtonNames)+1)
	for i := range raRemapButtonNames {
		targets = append(targets, i)
	}
	return append(targets, -1)
}

// gameButtonsMapStep moves along the targets. Pure.
func gameButtonsMapStep(cur, delta int, wrap bool) int {
	targets := gameButtonsMapTargets()
	idx := 0
	for i, to := range targets {
		if to == cur {
			idx = i
		}
	}
	idx += delta
	switch {
	case wrap && idx >= len(targets):
		idx = 0
	case wrap && idx < 0:
		idx = len(targets) - 1
	case idx < 0:
		idx = 0
	case idx >= len(targets):
		idx = len(targets) - 1
	}
	return targets[idx]
}

// gameButtonsMapLabel names a target for the sheet. Pure.
func gameButtonsMapLabel(to int) string {
	if to < 0 || to >= len(raRemapButtonNames) {
		return "None"
	}
	return strings.ToUpper(raRemapButtonNames[to])
}

// normaliseGameButtonsMap keeps the button names RetroArch knows and the
// values a remap file can carry; a map left with nothing is nil, so a record
// with only a map is still dropped when it becomes empty. Pure.
func normaliseGameButtonsMap(m map[string]int) map[string]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int, len(m))
	for name, to := range m {
		name = strings.ToLower(strings.TrimSpace(name))
		if raRemapButtonID(name) < 0 || to < -1 || to >= len(raRemapButtonNames) {
			continue
		}
		out[name] = to
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// gameButtonsMapMerge lays a game's own mappings over the preset's and the
// layout's. Pure; base is never modified.
func gameButtonsMapMerge(base map[string]int, own map[string]int) map[string]int {
	own = normaliseGameButtonsMap(own)
	if len(own) == 0 {
		return base
	}
	out := make(map[string]int, len(base)+len(own))
	for name, to := range base {
		out[name] = to
	}
	for name, to := range own {
		out[name] = to
	}
	return out
}

// gameButtonsMapFor is the game's own map, copied for the caller.
func gameButtonsMapFor(romPath string) map[string]int {
	return normaliseGameButtonsMap(gameControlsFor(romPath).Map)
}

// gameButtonsMapSet points one of the game's buttons at another (or None).
// Main thread (the sheet); the file is small and written whole.
func gameButtonsMapSet(romPath, button string, to int) {
	button = strings.ToLower(strings.TrimSpace(button))
	if raRemapButtonID(button) < 0 || to < -1 || to >= len(raRemapButtonNames) {
		return
	}
	own := gameControlsFor(romPath)
	if own.Map == nil {
		own.Map = map[string]int{}
	}
	own.Map[button] = to
	gameControlsSet(romPath, own)
}

// gameButtonsMapStepFor applies Left/Right (or A, which wraps) on a sheet
// row. It reports whether anything changed.
func gameButtonsMapStepFor(game libraryGame, button string, delta int, wrap bool) bool {
	own := gameButtonsMapFor(game.Path)
	cur := raRemapButtonID(button)
	if to, ok := own[button]; ok {
		cur = to
	}
	next := gameButtonsMapStep(cur, delta, wrap)
	if next == cur {
		return false
	}
	gameButtonsMapSet(game.Path, button, next)
	return true
}

// gameButtonsMapSheetValue is a map row's right-hand value: the game's own
// target, or Automatic (the Buttons choice and layout decide it).
func gameButtonsMapSheetValue(game libraryGame, button string) string {
	if to, ok := gameButtonsMapFor(game.Path)[button]; ok {
		return gameButtonsMapLabel(to) + " · this game"
	}
	return "Automatic"
}

// gameButtonsMapSheetOwn reports a map row holding the game's own target.
func gameButtonsMapSheetOwn(game libraryGame, button string) bool {
	_, ok := gameButtonsMapFor(game.Path)[button]
	return ok
}
