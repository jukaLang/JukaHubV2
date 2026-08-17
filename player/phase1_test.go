package main

import (
	"testing"
)

// TestHomeLayoutFits1280x720 verifies that the home screen grid, status bar,
// Continue panel, and footer all fit within the 1280x720 logical canvas and
// do not overlap.
func TestHomeLayoutFits1280x720(t *testing.T) {
	// Build a small scene with the same 8 home tiles as jukaconfig.json.
	scene := SceneConfig{
		Name:   "Main",
		Layout: "home",
		Elements: []Element{
			{Type: "menu", X: 10, Y: 648, Trigger: "exit"},
			{Type: "recent", X: 30, Y: 44, Width: "1220", Height: "178", Variable: "recent_items"},
			{Type: "button", Text: "Media", X: 30, Y: 244, Width: "220", Height: "170", Style: "tile"},
			{Type: "button", Text: "Files", X: 280, Y: 244, Width: "220", Height: "170", Style: "tile"},
			{Type: "button", Text: "Packages", X: 530, Y: 244, Width: "220", Height: "170", Style: "tile"},
			{Type: "button", Text: "Chat", X: 780, Y: 244, Width: "220", Height: "170", Style: "tile"},
			{Type: "button", Text: "Favorites", X: 30, Y: 444, Width: "220", Height: "170", Style: "tile"},
			{Type: "button", Text: "Apps", X: 280, Y: 444, Width: "220", Height: "170", Style: "tile"},
			{Type: "button", Text: "Settings", X: 530, Y: 444, Width: "220", Height: "170", Style: "tile"},
			{Type: "button", Text: "Search / Tools", X: 780, Y: 444, Width: "220", Height: "170", Style: "tile"},
		},
	}

	hl := computeHomeLayout(1280, 720, 8)

	// Status bar and footer occupy known heights.
	barH := HomeTopBarH
	footerH := HomeFooterH
	if hl.HeadingRect.Y < barH {
		t.Errorf("heading starts above status bar: y=%d, barH=%d", hl.HeadingRect.Y, barH)
	}
	if hl.RecentRect.Y+hl.RecentRect.H > 720-footerH {
		t.Errorf("recent panel extends below footer: y+h=%d, screenH-footerH=%d", hl.RecentRect.Y+hl.RecentRect.H, 720-footerH)
	}
	if len(hl.TileRects) != 8 {
		t.Fatalf("expected 8 tile rects, got %d", len(hl.TileRects))
	}
	for i, r := range hl.TileRects {
		if r.X < 0 || r.Y < 0 || r.X+r.W > 1280 || r.Y+r.H > 720 {
			t.Errorf("tile %d out of bounds: %+v", i, r)
		}
		if r.Y+r.H > 720-footerH {
			t.Errorf("tile %d extends into footer: y+h=%d, footerTop=%d", i, r.Y+r.H, 720-footerH)
		}
	}

	// Focus graph should include all 8 tile buttons plus the recent panel.
	fe := NewFocusEngine()
	graph := fe.BuildGraph(scene)
	if len(graph.Nodes) != 9 {
		t.Errorf("expected 9 focusable nodes (8 tiles + recent), got %d", len(graph.Nodes))
	}
}

// TestWindowScaleConversion verifies that physical mouse coordinates are
// correctly mapped back to the 1280x720 logical canvas, including the
// letterbox offsets used when the window is resized.
func TestWindowScaleConversion(t *testing.T) {
	// Reset to defaults.
	screenWidth, screenHeight = 1280, 720
	updateWindowScale(1280, 720)
	if windowScaleFactor != 1.0 || windowOffsetX != 0 || windowOffsetY != 0 {
		t.Fatalf("unexpected scale for 1:1 window: factor=%v offset=(%d,%d)", windowScaleFactor, windowOffsetX, windowOffsetY)
	}
	x, y := physicalToLogical(640, 360)
	if x != 640 || y != 360 {
		t.Errorf("logical center mismatch: got (%d,%d), want (640,360)", x, y)
	}

	// Wider window: letterboxed horizontally, logical canvas centered. Scale
	// stays 1.0 because the height is unchanged.
	updateWindowScale(1920, 720)
	if windowScaleFactor != 1.0 {
		t.Fatalf("expected scale 1.0 for wider window, got %v", windowScaleFactor)
	}
	if windowOffsetX <= 0 {
		t.Fatalf("expected horizontal letterbox offset > 0, got %d", windowOffsetX)
	}
	x, y = physicalToLogical(windowOffsetX, windowOffsetY)
	if x != 0 || y != 0 {
		t.Errorf("top-left logical mismatch: got (%d,%d), want (0,0)", x, y)
	}

	// Larger window in both dimensions: scale > 1.
	updateWindowScale(1920, 1080)
	if windowScaleFactor <= 1.0 {
		t.Fatalf("expected scale > 1 for larger window, got %v", windowScaleFactor)
	}
	x, y = physicalToLogical(windowOffsetX, windowOffsetY)
	if x != 0 || y != 0 {
		t.Errorf("top-left logical mismatch after larger resize: got (%d,%d), want (0,0)", x, y)
	}
}

// TestSearchResultsSemanticInput verifies that search-results grid navigation
// through the semantic action helper updates the focused index consistently.
func TestSearchResultsSemanticInput(t *testing.T) {
	// Save and restore the globals this test touches.
	oldScene := currentSceneIndex
	oldFocus := focusedResultIndex
	defer func() { currentSceneIndex = oldScene; focusedResultIndex = oldFocus }()

	scene := SceneConfig{
		Name:   "Tube",
		Layout: "",
		Elements: []Element{
			{Type: "input", X: 60, Y: 85, Width: "760", Height: "42", Variable: "search_query"},
			{Type: "searchresults", X: 60, Y: 135, Width: "1160", Height: "412", Variable: "search_results", Columns: 2, Rows: 5},
		},
	}
	cfg := &Config{
		Scenes: []SceneConfig{scene},
		Variables: Variables{Custom: map[string]interface{}{
			"search_results": []VideoInfo{
				{ID: "1", Title: "One"},
				{ID: "2", Title: "Two"},
				{ID: "3", Title: "Three"},
				{ID: "4", Title: "Four"},
			},
		}},
	}
	currentSceneIndex = 0
	focusedResultIndex = 0

	// 2x5 grid, 4 items. Move right and left within the first row.
	handleSearchResultsAction(ActionNavigateRight, cfg)
	if focusedResultIndex != 1 {
		t.Errorf("expected focused index 1 after right, got %d", focusedResultIndex)
	}
	handleSearchResultsAction(ActionNavigateLeft, cfg)
	if focusedResultIndex != 0 {
		t.Errorf("expected focused index 0 after left, got %d", focusedResultIndex)
	}
	handleSearchResultsAction(ActionNavigateDown, cfg)
	if focusedResultIndex != 2 {
		t.Errorf("expected focused index 2 after down, got %d", focusedResultIndex)
	}
}

// TestReducedMotionAndLowPowerDefaults verifies that the Variables struct now
// carries the new settings and that merging from user config preserves them.
func TestReducedMotionAndLowPowerDefaults(t *testing.T) {
	cfg := &Config{
		Variables: Variables{
			Custom: make(map[string]interface{}),
		},
	}
	user := &UserConfig{
		Variables: UserVariables{
			ReducedMotion: true,
			LowPower:      true,
			Custom:        make(map[string]interface{}),
		},
	}
	mergeUserConfig(cfg, user)
	if !cfg.Variables.ReducedMotion {
		t.Error("ReducedMotion not merged from user config")
	}
	if !cfg.Variables.LowPower {
		t.Error("LowPower not merged from user config")
	}
}
