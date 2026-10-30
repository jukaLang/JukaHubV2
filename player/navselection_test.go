package main

import (
	"fmt"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// useNavScene points the package navigation globals at a synthetic scene for
// the duration of a test and restores them afterwards, so tests that rely on
// untouched defaults (e.g. currentSceneIndex == -1) keep working.
func useNavScene(t *testing.T, scene SceneConfig) *Config {
	t.Helper()
	prevSceneIdx, prevSel := currentSceneIndex, selectedButtonIndex
	prevEngine := focusEngine
	prevBack, prevClock := sceneTitleBackRect, statusClockRect
	currentSceneIndex = 0
	selectedButtonIndex = 0
	focusEngine = NewFocusEngine()
	sceneTitleBackRect, statusClockRect = sdl.Rect{}, sdl.Rect{}
	t.Cleanup(func() {
		currentSceneIndex, selectedButtonIndex = prevSceneIdx, prevSel
		focusEngine = prevEngine
		sceneTitleBackRect, statusClockRect = prevBack, prevClock
	})
	return &Config{Scenes: []SceneConfig{scene}}
}

// makeNavGridScene builds a Tools-scene-style button grid: 3 columns × 4 rows,
// matching the Misc scene layout (x pitch 312 from 24, y pitch 65 from 72).
func makeNavGridScene(name string, rows, cols int) SceneConfig {
	var els []Element
	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			els = append(els, Element{
				Type:   "button",
				Text:   fmt.Sprintf("B%d%d", row, col),
				X:      int32(24 + col*312),
				Y:      int32(72 + row*65),
				Width:  "296",
				Height: "55",
			})
		}
	}
	return SceneConfig{Name: name, Elements: els}
}

func TestToggleAllowsNavigationAndActivation(t *testing.T) {
	scene := SceneConfig{Name: "ToggleNav", Elements: []Element{
		{Type: "toggle", Variable: "testToggle", X: 40, Y: 96, Width: "280", Height: "48"},
		{Type: "input", X: 40, Y: 200, Width: "280", Height: "48"},
	}}
	config := useNavScene(t, scene)
	config.Variables.Custom = map[string]interface{}{"testToggle": false}
	installGraph(t, config, 0)
	handleToggleAction(config, scene.Elements[0], ActionNavigateDown)
	if selectedButtonIndex != 1 || toggleValue(config, scene.Elements[0]) {
		t.Fatalf("Down from toggle: focus=%d, value=%v", selectedButtonIndex, toggleValue(config, scene.Elements[0]))
	}
	moveSelection(config, ActionNavigateUp)
	if selectedButtonIndex != 0 {
		t.Fatalf("Up from input: focus=%d, want toggle", selectedButtonIndex)
	}
	handleToggleAction(config, scene.Elements[0], ActionConfirm)
	if !toggleValue(config, scene.Elements[0]) {
		t.Fatal("Confirm did not activate toggle")
	}
}

func TestFirstSelectableIncludesAllFocusTypes(t *testing.T) {
	for _, typ := range []string{"toggle", "themegallery", "chat", "textbrowser", "packagelist"} {
		scene := SceneConfig{Elements: []Element{{Type: "label"}, {Type: typ}}}
		if got := findFirstSelectableElement(scene); got != 1 {
			t.Errorf("first selectable %s = %d, want 1", typ, got)
		}
	}
}

// TestMoveSelectionNotTrappedOnFirstNodes covers the keyboard/D-pad focus trap:
// moveSelection used to reinstall the focus graph on every press, which reset
// focus to the first node each time, so navigation could only ever reach the
// first two buttons (Terminal and Hardware Monitor in the Tools scene).
func TestMoveSelectionNotTrappedOnFirstNodes(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("GridNav", 4, 3))

	// Two consecutive Downs must advance two rows (0 -> 3 -> 6, row-major).
	moveSelection(config, ActionNavigateDown)
	if selectedButtonIndex != 3 {
		t.Fatalf("after first Down: selectedButtonIndex=%d, want 3 (row 1)", selectedButtonIndex)
	}
	moveSelection(config, ActionNavigateDown)
	if selectedButtonIndex != 6 {
		t.Fatalf("after second Down: selectedButtonIndex=%d, want 6 (focus reset between presses)", selectedButtonIndex)
	}
	moveSelection(config, ActionNavigateDown)
	if selectedButtonIndex != 9 {
		t.Fatalf("after third Down: selectedButtonIndex=%d, want 9 (row 3)", selectedButtonIndex)
	}
	moveSelection(config, ActionNavigateUp)
	if selectedButtonIndex != 6 {
		t.Fatalf("after Up: selectedButtonIndex=%d, want 6 (row 2)", selectedButtonIndex)
	}
}

// TestMoveSelectionHorizontalAxis asserts Left/Right move along the row rather
// than collapsing into Up/Down (grid columns must be reachable).
func TestMoveSelectionHorizontalAxis(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("GridNav3", 4, 3))

	moveSelection(config, ActionNavigateRight)
	if selectedButtonIndex != 1 {
		t.Fatalf("after Right: selectedButtonIndex=%d, want 1 (column 1)", selectedButtonIndex)
	}
	moveSelection(config, ActionNavigateRight)
	if selectedButtonIndex != 2 {
		t.Fatalf("after second Right: selectedButtonIndex=%d, want 2 (column 2)", selectedButtonIndex)
	}
	moveSelection(config, ActionNavigateLeft)
	if selectedButtonIndex != 1 {
		t.Fatalf("after Left: selectedButtonIndex=%d, want 1 (column 1)", selectedButtonIndex)
	}
}

// TestMoveSelectionEveryButtonReachable walks the whole grid and asserts that
// keyboard/D-pad navigation can reach every button.
func TestMoveSelectionEveryButtonReachable(t *testing.T) {
	config := useNavScene(t, makeNavGridScene("GridNav2", 4, 3))

	visited := map[int]bool{selectedButtonIndex: true}
	for row := 0; row < 4; row++ {
		if row > 0 {
			moveSelection(config, ActionNavigateDown)
			visited[selectedButtonIndex] = true
		}
		for i := 0; i < 2; i++ {
			moveSelection(config, ActionNavigateRight)
			visited[selectedButtonIndex] = true
		}
		for i := 0; i < 2; i++ {
			moveSelection(config, ActionNavigateLeft)
			visited[selectedButtonIndex] = true
		}
	}
	if len(visited) != 12 {
		t.Fatalf("reachable buttons: %d of 12, visited=%v", len(visited), visited)
	}
}

// TestRealToolsSceneEveryButtonReachable checks the actual Tools ("Misc")
// scene from the live jukaconfig.json against the user-visible contract: every
// button must be selectable with the keyboard. It pins the two reported moves
// (Down from Terminal must reach Hardware Monitor, Right must reach the button
// beside it) and then proves every button is reachable through key actions.
func TestRealToolsSceneEveryButtonReachable(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "Misc")
	if idx < 0 {
		t.Fatal("scene Misc (Tools) not found in jukaconfig.json")
	}
	scene := config.Scenes[idx]

	var buttons []int
	for i, el := range scene.Elements {
		if el.Type == "button" {
			buttons = append(buttons, i)
		}
	}
	if len(buttons) < 2 {
		t.Fatalf("Tools scene has %d buttons, want at least 2", len(buttons))
	}

	useNavScene(t, scene)
	build := func() { focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene)) }

	// Regression pair from the bug report: from the top-left button, Down must
	// reach the button directly below and Right the button directly beside it.
	build()
	start := focusEngine.ElementIndex()
	// Installing a graph preserves focus across reinstalls (by design), so
	// pin the walk's starting node explicitly.
	refocus := func() {
		build()
		if !focusEngine.SetByElementIndex(start) {
			t.Fatalf("cannot focus start button %d", start)
		}
	}
	startEl := scene.Elements[start]
	below, right := -1, -1
	for _, b := range buttons {
		el := scene.Elements[b]
		if el.X == startEl.X && el.Y > startEl.Y && (below < 0 || el.Y < scene.Elements[below].Y) {
			below = b
		}
		if el.Y == startEl.Y && el.X > startEl.X && (right < 0 || el.X < scene.Elements[right].X) {
			right = b
		}
	}
	if below < 0 || right < 0 {
		t.Fatalf("Tools scene is not a grid: below=%d right=%d", below, right)
	}
	refocus()
	if got := focusEngine.Navigate(ActionNavigateDown); got != below {
		t.Errorf("Down from %q selects %q, want %q", startEl.Text, scene.Elements[got].Text, scene.Elements[below].Text)
	}
	refocus()
	if got := focusEngine.Navigate(ActionNavigateRight); got != right {
		t.Errorf("Right from %q selects %q, want %q", startEl.Text, scene.Elements[got].Text, scene.Elements[right].Text)
	}

	// Every button must be reachable from the initial focus through key actions.
	refocus()
	reached := map[int]bool{focusEngine.ElementIndex(): true}
	queue := []int{focusEngine.ElementIndex()}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, act := range []Action{ActionNavigateUp, ActionNavigateDown, ActionNavigateLeft, ActionNavigateRight} {
			if !focusEngine.SetByElementIndex(cur) {
				continue
			}
			if next := focusEngine.Navigate(act); next >= 0 && !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}
	for _, b := range buttons {
		if !reached[b] {
			t.Errorf("button %q at (%d,%d) is not selectable with the keyboard",
				scene.Elements[b].Text, scene.Elements[b].X, scene.Elements[b].Y)
		}
	}
}
