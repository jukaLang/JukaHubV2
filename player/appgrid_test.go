package main

import (
	"path/filepath"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// The launcher grid must fill the panel, keep every tile inside it, and line
// the click hitboxes up with what the renderer draws.
func TestAppGridLayoutFillsThePanelAndHitboxesMatch(t *testing.T) {
	element := Element{X: 24, Y: 100}
	elemW := int32(1232)
	layout := newAppGridLayout(element, elemW)

	if layout.cols < 4 {
		t.Fatalf("columns = %d, want a multi-column launcher on a 1232px panel", layout.cols)
	}
	if layout.tileW < appGridMinTileW {
		t.Fatalf("tile width = %d, want at least %d", layout.tileW, appGridMinTileW)
	}
	first := appGridCellRect(layout, 0, 0)
	lastInRow := appGridCellRect(layout, layout.cols-1, 0)
	if first.X < element.X || lastInRow.X+lastInRow.W > element.X+elemW {
		t.Fatalf("tiles exceed the panel: first=%+v last=%+v", first, lastInRow)
	}

	nextRow := appGridCellRect(layout, layout.cols, 0)
	if nextRow.X != first.X || nextRow.Y-first.Y != layout.tileH+layout.gap {
		t.Fatalf("row pitch = %+v, want same X and %d offset from %+v", nextRow, layout.tileH+layout.gap, first)
	}

	entries := make([]FileEntry, 0, layout.cols*2)
	for i := 0; i < layout.cols*2; i++ {
		entries = append(entries, FileEntry{Name: "App", Path: "app"})
	}
	focusedFileIndex = 0
	for i := range entries {
		cell := appGridCellRect(layout, i, 0)
		got := appGridIndexAt(element, entries, cell.X+cell.W/2, cell.Y+cell.H/2)
		if got != i {
			t.Fatalf("hit test at tile %d center returned %d", i, got)
		}
	}
}

func TestAppGridScrollingKeepsFocusVisible(t *testing.T) {
	cols, rows, total := 5, 3, 40
	focus := 30
	first := appGridFirstRow(focus, rows, total, cols)
	if first > focus/cols {
		t.Fatalf("first row %d is below the focused row %d", first, focus/cols)
	}
	if focus/cols >= first+rows {
		t.Fatalf("focused row %d is below the window (%d..%d)", focus/cols, first, first+rows-1)
	}
	if got := appGridFirstRow(0, rows, total, cols); got != 0 {
		t.Fatalf("first row at the top = %d, want 0", got)
	}
	// A grid shorter than the window never scrolls.
	if got := appGridFirstRow(2, rows, 6, cols); got != 0 {
		t.Fatalf("short list scrolled to row %d, want 0", got)
	}
}

func TestAppGridDisplayNameAndGlyph(t *testing.T) {
	if got := appGridDisplayName("Live TV (built-in)"); got != "Live TV" {
		t.Errorf("display name = %q, want %q", got, "Live TV")
	}
	if !appGridIsBuiltin("Live TV (built-in)") || appGridIsBuiltin("Notepad") {
		t.Error("built-in marker misdetected")
	}
	if got := appGridGlyph("  spotify"); got != "S" {
		t.Errorf("glyph = %q, want S", got)
	}
	if got := appGridGlyph(""); got != "?" {
		t.Errorf("empty glyph = %q, want ?", got)
	}
}

// The launcher moves in two dimensions; a press never leaves the list.
func TestAppGridMoveClampsInsideTheList(t *testing.T) {
	config := &Config{}
	element := Element{X: 24, Y: 100}
	scene := SceneConfig{Name: "Apps", Elements: []Element{{Type: "dynamiclist", Variable: appsEntriesVar, X: element.X, Y: element.Y}}}
	layout := newAppGridLayout(element, appGridElementWidth(element))
	cols := layout.cols
	entries := make([]FileEntry, 0, cols*2)
	for i := 0; i < cols*2; i++ {
		entries = append(entries, FileEntry{Name: "app", Path: "app"})
	}
	config.Variables.Custom = map[string]interface{}{appsEntriesVar: entries}

	focusedFileIndex = 0
	appGridMove(config, scene, 0, 1) // down one row
	if focusedFileIndex != cols {
		t.Fatalf("down from 0 = %d, want %d", focusedFileIndex, cols)
	}
	appGridMove(config, scene, 0, -1)
	if focusedFileIndex != 0 {
		t.Fatalf("up from row 1 = %d, want 0", focusedFileIndex)
	}
	appGridMove(config, scene, -1, 0)
	if focusedFileIndex != 0 {
		t.Fatalf("left at the first tile = %d, want 0", focusedFileIndex)
	}
	appGridMove(config, scene, 1, 0)
	if focusedFileIndex != 1 {
		t.Fatalf("right from 0 = %d, want 1", focusedFileIndex)
	}
	focusedFileIndex = 0
}

// A dead launcher row is removed instead of failing on every press.
func TestDropAppEntryRemovesTheFailedProgram(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{
		appsEntriesVar: []FileEntry{
			{Name: "Keep", Path: "/apps/keep.exe"},
			{Name: "Gone (built-in)", Path: "/apps/gone.exe"},
		},
	}
	focusedFileIndex = 1
	dropAppEntry(config, "/apps/gone.exe")

	entries, _ := config.Variables.Custom[appsEntriesVar].([]FileEntry)
	if len(entries) != 1 || entries[0].Name != "Keep" {
		t.Fatalf("entries after drop = %+v", entries)
	}
	if focusedFileIndex != 0 {
		t.Fatalf("focus = %d, want it clamped to the remaining row", focusedFileIndex)
	}
	// An unknown path changes nothing.
	dropAppEntry(config, "/apps/nope.exe")
	entries, _ = config.Variables.Custom[appsEntriesVar].([]FileEntry)
	if len(entries) != 1 {
		t.Fatalf("dropping an unknown path removed rows: %+v", entries)
	}
}

// Refresh must not resurrect a launcher row the user just saw fail.
func TestDroppedAppStaysHiddenAfterRefresh(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "Gone.exe")
	mustWriteAppFile(t, gone)
	mustWriteAppFile(t, filepath.Join(dir, "Keep.exe"))

	entries := collectAppEntries(nil, dir, nil)
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{appsEntriesVar: entries}
	dropAppEntry(config, gone)
	deadAppPaths[gone] = true
	t.Cleanup(func() { delete(deadAppPaths, gone) })

	for _, e := range collectAppEntries(nil, dir, nil) {
		if e.Path == gone {
			t.Fatal("a refresh listed the program that just failed to launch")
		}
	}
}

// The launcher only lists applets this build can really open.
func TestBuiltinAppletsAreLaunchable(t *testing.T) {
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	entries := builtinAppEntries(config)
	for _, e := range entries {
		if e.Name == "YouTube Shorts (built-in)" {
			t.Error("YouTube Shorts should live in Media, not in the launcher")
		}
		if e.Path == builtinActionPrefix+"chiaki" && !psRemoteSupported() {
			t.Error("PS Remote Play is listed although the remote-play helper cannot run here")
		}
	}
	if psRemoteSupported() {
		found := false
		for _, e := range entries {
			if e.Path == builtinActionPrefix+"chiaki" {
				found = true
			}
		}
		if !found {
			t.Error("PS Remote Play missing on a device that supports it")
		}
	}
}

// Layout helpers are shared by rendering and clicking; this pins the rect shape
// so a tile can never be zero-sized.
func TestAppGridCellRectIsPositive(t *testing.T) {
	layout := newAppGridLayout(Element{X: 0, Y: 0}, 800)
	cell := appGridCellRect(layout, 3, 0)
	if cell.W <= 0 || cell.H <= 0 {
		t.Fatalf("cell = %+v", cell)
	}
	var _ sdl.Rect = cell
}
