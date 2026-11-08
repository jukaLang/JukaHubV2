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

// TestRealToolsSceneEveryButtonReachable checks the Tools hub built from the
// live jukaconfig.json against the user-visible contract: every tool must be
// selectable with the D-pad. (The hub replaced the config's "Misc" page, whose
// buttons this test used to walk.) It pins the moves people make first — Down
// from Watch Party lands on Local play, the next tile of the Together section,
// Down again on the first toolkit, Right walks the toolkits and Up climbs back
// row by row — and then proves every tile is reachable through the four
// directions, dimmed tiles included.
func TestRealToolsSceneEveryButtonReachable(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	ensureToolsHubScene(config)
	if sceneIndexExact(config, "Misc") >= 0 {
		t.Fatal("the retired Misc page is still in the loaded config")
	}
	for _, net := range []toolsHubNet{{}, {ip: "192.168.1.20", online: true}} {
		tiles := toolsHubBuildTiles(config, net, toolsHubSlowValues{}, "")
		for _, cols := range []int{3, 4, 6} {
			g := toolsHubLayoutGrid(tiles, toolsHubGridSpec{width: 1216, cols: cols, tileH: 120, gap: 14, headingH: 38, sectionGap: 6})
			center := func(i int) int32 { return g.rects[i].X + g.rects[i].W/2 }

			// The page's own walk, from a fresh visit: focus starts on Watch Party.
			toolbox, audit := toolsHubIndexOf(tiles, "toolbox"), toolsHubIndexOf(tiles, "audit")
			localPlay := toolsHubIndexOf(tiles, "localplay")
			p := newToolsHubPage()
			p.grid = g
			if !p.move(tiles, 0, 1) || p.cursor != localPlay {
				t.Errorf("%d cols: Down from Watch Party selects %d, want Local play (%d)", cols, p.cursor, localPlay)
			}
			if !p.move(tiles, 0, 1) || p.cursor != toolbox {
				t.Errorf("%d cols: Down from Local play selects %d, want Toolbox (%d)", cols, p.cursor, toolbox)
			}
			if !p.move(tiles, 1, 0) || p.cursor != audit {
				t.Errorf("%d cols: Right from Toolbox selects %d, want Audit toolkit (%d)", cols, p.cursor, audit)
			}
			// Up climbs into the Together row, then onto Watch Party.
			p.move(tiles, 0, -1)
			if !p.move(tiles, 0, -1) || p.cursor != 0 {
				t.Errorf("%d cols: Up twice from the toolkits selects %d, want Watch Party (0)", cols, p.cursor)
			}

			reached := map[int]bool{0: true}
			queue := []int{0}
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				for _, d := range [][2]int{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
					if next, ok := toolsHubGridMove(g, cur, d[0], d[1], center(cur)); ok && !reached[next] {
						reached[next] = true
						queue = append(queue, next)
					}
				}
			}
			for i, tile := range tiles {
				if !reached[i] {
					t.Errorf("%d cols, net %+v: tile %q is not selectable with the D-pad", cols, net, tile.label)
				}
			}
		}
	}
}
