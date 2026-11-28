package main

// racontrols_map_test.go — a game's own per-button map (GAME-4): it is laid
// over the Buttons preset and the control layout, written through the same
// one-launch remap file, and stored in the game's controls record.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGameButtonsMapMergeLaysOwnOverPreset(t *testing.T) {
	base := raRemapPreset(gameButtonsSwapAB)
	if len(base) != 2 {
		t.Fatalf("swap preset = %v, want two mappings", base)
	}
	got := gameButtonsMapMerge(base, map[string]int{
		"a":    raRemapButtonID("a"), // swap it back for this game
		"x":    -1,                   // and turn X off
		"nope": 3,                    // not a RetroPad button
		"y":    99,                   // not a target a remap can carry
	})
	if len(got) != 3 {
		t.Fatalf("merged = %v, want a, b and x only", got)
	}
	if got["a"] != raRemapButtonID("a") {
		t.Errorf("a = %d, want %d (the game's own target)", got["a"], raRemapButtonID("a"))
	}
	if got["b"] != raRemapButtonID("a") {
		t.Errorf("b = %d, want %d (the preset's)", got["b"], raRemapButtonID("a"))
	}
	if got["x"] != -1 {
		t.Errorf("x = %d, want -1 (None)", got["x"])
	}
	if base["a"] != raRemapButtonID("b") || base["b"] != raRemapButtonID("a") {
		t.Errorf("the preset was modified: %v", base)
	}
}

func TestGameButtonsMapChangesTheRemapFile(t *testing.T) {
	buttons := gameButtonsMapMerge(raRemapPreset(gameButtonsSwapAB), map[string]int{
		"a": raRemapButtonID("x"), // A sends X for this game
		"x": -1,                   // and X is off
	})
	text := raRemapText(buttons, nil)
	for _, want := range []string{
		`input_player1_btn_a = "9"`, // A -> X
		`input_player1_btn_b = "8"`, // the preset's B -> A still holds
		`input_player1_btn_x = "-1"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("remap file lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "input_player1_btn_y") {
		t.Errorf("an unset button was written:\n%s", text)
	}
}

func TestGameButtonsMapStepsEveryTarget(t *testing.T) {
	targets := gameButtonsMapTargets()
	if len(targets) != len(raRemapButtonNames)+1 {
		t.Fatalf("targets = %v, want every RetroPad button and None", targets)
	}
	seen := map[int]bool{}
	cur := 0
	for range targets {
		seen[cur] = true
		cur = gameButtonsMapStep(cur, 1, true)
	}
	if len(seen) != len(targets) || cur != 0 {
		t.Errorf("wrapping through the targets visited %d, ended at %d", len(seen), cur)
	}
	if got := gameButtonsMapStep(-1, 1, false); got != -1 {
		t.Errorf("right of None without wrap = %d, want -1", got)
	}
	if got := gameButtonsMapStep(0, -1, false); got != 0 {
		t.Errorf("left of B without wrap = %d, want 0", got)
	}
	if got := gameButtonsMapLabel(-1); got != "None" {
		t.Errorf("label of None = %q", got)
	}
	if got := gameButtonsMapLabel(raRemapButtonID("a")); got != "A" {
		t.Errorf("label of A = %q", got)
	}
}

func TestGameButtonsMapRowsNameEveryButton(t *testing.T) {
	for i, name := range raRemapButtonNames {
		row := gameOptionsRowMapB + i
		if got := gameOptionsMapButtonIndex(row); got != i {
			t.Errorf("row %d edits button %d, want %d", row, got, i)
		}
		if got, want := gameOptionsRowName(row), "Button "+strings.ToUpper(name); got != want {
			t.Errorf("row %d = %q, want %q", row, got, want)
		}
	}
	if got := gameOptionsMapButtonIndex(gameOptionsRowButtons); got != -1 {
		t.Errorf("the Buttons row edits button %d", got)
	}
	if got := gameOptionsMapButtonIndex(gameOptionsRowCtrlLayout); got != -1 {
		t.Errorf("the Controls layout row edits button %d", got)
	}
}

func TestGameButtonsMapSurvivesTheControlsRecord(t *testing.T) {
	// The record's path is a variable for tests (racontrols.go).
	prevPath := gameControlsPath
	recordPath := filepath.Join(t.TempDir(), "game-controls.json")
	gameControlsPath = func() string { return recordPath }
	t.Cleanup(func() { gameControlsPath = prevPath })
	gameControlsStore.mu.Lock()
	prevLoaded, prevGames := gameControlsStore.loaded, gameControlsStore.games
	gameControlsStore.loaded, gameControlsStore.games = false, nil
	gameControlsStore.mu.Unlock()
	t.Cleanup(func() {
		gameControlsStore.mu.Lock()
		gameControlsStore.loaded, gameControlsStore.games = prevLoaded, prevGames
		gameControlsStore.mu.Unlock()
	})

	rom := filepath.Join(t.TempDir(), "Game.sfc")
	gameButtonsMapSet(rom, "a", raRemapButtonID("b"))

	raw, err := os.ReadFile(gameControlsPath())
	if err != nil {
		t.Fatalf("the record was not written: %v", err)
	}
	var file gameControlsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	if got := file.Games[gameOptionsKey(rom)].Map["a"]; got != raRemapButtonID("b") {
		t.Fatalf("stored map = %v, want a -> B", file.Games[gameOptionsKey(rom)].Map)
	}

	// A map alone keeps the record: reloading it must not drop the game.
	gameControlsStore.mu.Lock()
	gameControlsStore.loaded, gameControlsStore.games = false, nil
	gameControlsStore.mu.Unlock()
	if got := gameButtonsMapFor(rom)["a"]; got != raRemapButtonID("b") {
		t.Errorf("reloaded map a = %d, want %d", got, raRemapButtonID("b"))
	}

	gameControlsSet(rom, gameControlsGame{})
	if own := gameControlsFor(rom); own.Buttons != "" || own.Turbo != "" || len(own.Map) != 0 {
		t.Errorf("clearing left %+v", own)
	}
}
