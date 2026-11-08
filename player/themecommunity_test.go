package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func setThemeCatalogTestEntries(t *testing.T, entries []communityTheme) {
	t.Helper()
	themeCatalogState.mu.Lock()
	previous := themeCatalogState.entries
	themeCatalogState.entries = entries
	themeCatalogState.mu.Unlock()
	t.Cleanup(func() {
		themeCatalogState.mu.Lock()
		themeCatalogState.entries = previous
		themeCatalogState.mu.Unlock()
	})
}

func TestThemeCommunityPanelMovesAndWraps(t *testing.T) {
	previous := themeCommunityPanel
	t.Cleanup(func() { themeCommunityPanel = previous })
	setThemeCatalogTestEntries(t, []communityTheme{
		{ID: "one", Name: "One"},
		{ID: "two", Name: "Two"},
	})
	themeCommunityPanel = themeCommunityPanelState{open: true}

	themeCommunityMove(-1)
	if themeCommunityPanel.cursor != 1 {
		t.Fatalf("Up from the first row went to %d, want the last", themeCommunityPanel.cursor)
	}
	themeCommunityMove(1)
	if themeCommunityPanel.cursor != 0 {
		t.Fatalf("Down from the last row went to %d, want the first", themeCommunityPanel.cursor)
	}
	if rows := themeCommunityRows(); len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
}

func TestThemeCommunityPanelIgnoresKeysWhenClosed(t *testing.T) {
	previous := themeCommunityPanel
	t.Cleanup(func() { themeCommunityPanel = previous })
	themeCommunityPanel = themeCommunityPanelState{}
	e := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}
	if handleThemeCommunityKey(e, &Config{}) {
		t.Fatal("a closed panel consumed a key")
	}
}

func TestThemeCommunityRowHintMarksInstalled(t *testing.T) {
	// A shipped palette's id cannot be installed, so the row must not claim the
	// theme is installed and must not offer a key that always fails.
	if got := themeCommunityRowHint(communityTheme{ID: "dark"}); got != "Reserved id" {
		t.Fatalf("built-in row hint = %q", got)
	}
	if got := themeCommunityRowHint(communityTheme{ID: "not-installed-yet"}); got != "A install" {
		t.Fatalf("uninstalled row hint = %q", got)
	}

	id := "test-row-installed"
	themePresets[id] = ThemePreset{Name: "Installed elsewhere"}
	t.Cleanup(func() { forgetThemePreset(id) })
	if got := themeCommunityRowHint(communityTheme{ID: id}); got != "Installed" {
		t.Fatalf("installed row hint = %q", got)
	}
}

// The wheel has to move the selection: the renderer derives its scroll from the
// cursor, so a wheel handler that only bumped a scroll offset did nothing.
func TestThemeCommunityWheelMovesFocus(t *testing.T) {
	previous := themeCommunityPanel
	t.Cleanup(func() { themeCommunityPanel = previous })
	setThemeCatalogTestEntries(t, []communityTheme{
		{ID: "one"}, {ID: "two"}, {ID: "three"}, {ID: "four"},
	})
	themeCommunityPanel = themeCommunityPanelState{open: true}

	if !handleThemeCommunityWheel(1) {
		t.Fatal("the open panel ignored the wheel")
	}
	if themeCommunityPanel.cursor != 3 {
		t.Fatalf("one notch down moved the focus to %d, want 3", themeCommunityPanel.cursor)
	}
	handleThemeCommunityWheel(1)
	if themeCommunityPanel.cursor != 2 {
		t.Fatalf("a second notch wrapped to %d, want 2", themeCommunityPanel.cursor)
	}
	handleThemeCommunityWheel(-1)
	if themeCommunityPanel.cursor != 3 {
		t.Fatalf("a notch up moved the focus to %d, want 3", themeCommunityPanel.cursor)
	}
	if themeCommunityPanel.scroll != 0 {
		t.Fatalf("the wheel left a stale scroll offset of %d", themeCommunityPanel.scroll)
	}

	themeCommunityPanel = themeCommunityPanelState{}
	if handleThemeCommunityWheel(1) {
		t.Fatal("a closed panel consumed the wheel")
	}
}

// The browser has to draw on a real renderer without a nil font or a zero-sized
// card, and it must publish the row rects the clicks map through.
func TestThemeCommunityPanelRenders(t *testing.T) {
	previous := themeCommunityPanel
	previousRect := themeCommunityPanelRect
	previousRects := themeCommunityRowRects
	t.Cleanup(func() {
		themeCommunityPanel = previous
		themeCommunityPanelRect = previousRect
		themeCommunityRowRects = previousRects
	})
	setThemeCatalogTestEntries(t, []communityTheme{{
		ID:          "test-amber",
		Name:        "Terminal Amber",
		Author:      "@pixel_rune",
		Description: "Amber on near-black",
		Tags:        []string{"Dark"},
		Theme:       themeCatalogTestTheme(),
	}})
	themeCommunityPanel = themeCommunityPanelState{open: true}

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderThemeCommunityPanel(renderer, &Config{})
	if themeCommunityPanelRect.W <= 0 || themeCommunityPanelRect.H <= 0 {
		t.Fatalf("panel was not measured: %+v", themeCommunityPanelRect)
	}
	if len(themeCommunityRowRects) != 1 || themeCommunityRowRects[0].W <= 0 {
		t.Fatalf("rows were not published: %+v", themeCommunityRowRects)
	}
}
