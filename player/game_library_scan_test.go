package main

import "testing"

// A scan that was started before a close or a newer rescan must not replace
// the panel's fresher data.
func TestApplyGameLibraryScanRejectsStaleGeneration(t *testing.T) {
	panel := gameLibraryPanel{scanGen: 4, scanning: true}
	if applyGameLibraryScan(&panel, 3, gameLibraryReport{Total: 99}, nil) {
		t.Fatal("a stale scan was applied")
	}
	if !panel.scanning || panel.lib.Total != 0 {
		t.Fatalf("stale scan changed the panel: %+v", panel)
	}
	if applyGameLibraryScan(nil, 4, gameLibraryReport{}, nil) {
		t.Fatal("a nil panel accepted a scan")
	}

	recents := []recentGame{{Path: "/roms/a.gba", System: "GBA", Title: "A"}}
	if !applyGameLibraryScan(&panel, 4, gameLibraryReport{Total: 2}, recents) {
		t.Fatal("the current scan was rejected")
	}
	if panel.scanning || panel.lib.Total != 2 || len(panel.recents) != 1 {
		t.Fatalf("current scan was not installed: %+v", panel)
	}
}

// An empty card must build an empty library, not a console row with no games.
func TestBuildGameLibraryWithEmptyScan(t *testing.T) {
	lib := buildGameLibraryWith(romScanReport{}, "", nil)
	if lib.Total != 0 || len(lib.Systems) != 0 || lib.WithArt != 0 {
		t.Fatalf("empty scan built %+v", lib)
	}
}
