package main

// disctools_test.go — the cue/m3u helpers are pure file logic, so every rule
// that matters (never overwrite, never touch a cartridge folder, order the
// discs) is checked here without an SDL window.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDiscTestFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestCueSheetTextNamesTheBinaryAndMode(t *testing.T) {
	psx := cueSheetText("/roms/PSX/Final Fantasy VII.bin", "MODE2/2352")
	want := "FILE \"Final Fantasy VII.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"
	if psx != want {
		t.Fatalf("cue sheet = %q, want %q", psx, want)
	}
	sega := cueSheetText("Sonic CD.bin", "MODE1/2352")
	if !strings.Contains(sega, "TRACK 01 MODE1/2352") {
		t.Fatalf("sega cue sheet lost its mode: %q", sega)
	}
	if strings.Contains(sega, "roms") {
		t.Fatalf("cue sheet should name the file, not the path: %q", sega)
	}
}

func TestSplitDiscNumber(t *testing.T) {
	cases := []struct {
		name string
		base string
		disc int
		ok   bool
	}{
		{"Final Fantasy VII (Disc 1)", "Final Fantasy VII", 1, true},
		{"Metal Gear Solid [Disk 2]", "Metal Gear Solid", 2, true},
		{"Panzer Dragoon - Disc 3", "Panzer Dragoon", 3, true},
		{"Shenmue (CD 1 of 3)", "Shenmue", 1, true},
		{"Castlevania", "", 0, false},
		{"Odd (Disc 0)", "", 0, false},
		{"(Disc 1)", "", 0, false},
	}
	for _, tc := range cases {
		base, disc, ok := splitDiscNumber(tc.name)
		if ok != tc.ok || base != tc.base || disc != tc.disc {
			t.Errorf("splitDiscNumber(%q) = %q, %d, %v; want %q, %d, %v",
				tc.name, base, disc, ok, tc.base, tc.disc, tc.ok)
		}
	}
}

func TestWriteDiscHelpersCreatesOneCueForALoneBin(t *testing.T) {
	dir := t.TempDir()
	writeDiscTestFile(t, dir, "Crash Bandicoot.bin", "not really a disc")

	cues, playlists, errs := writeDiscHelpers(dir, "MODE2/2352")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if cues != 1 || playlists != 0 {
		t.Fatalf("cues = %d, playlists = %d; want 1, 0", cues, playlists)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Crash Bandicoot.cue"))
	if err != nil {
		t.Fatalf("cue was not written: %v", err)
	}
	if !strings.Contains(string(data), "FILE \"Crash Bandicoot.bin\" BINARY") {
		t.Fatalf("cue does not point at its bin: %q", data)
	}
}

func TestWriteDiscHelpersNeverOverwritesACue(t *testing.T) {
	dir := t.TempDir()
	writeDiscTestFile(t, dir, "Game.bin", "data")
	handEdited := "FILE \"Game.bin\" BINARY\n  TRACK 01 MODE2/2352\n  TRACK 02 AUDIO\n    INDEX 01 05:00:00\n"
	writeDiscTestFile(t, dir, "Game.cue", handEdited)

	cues, _, errs := writeDiscHelpers(dir, "MODE2/2352")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if cues != 0 {
		t.Fatalf("cues = %d, want 0: the existing sheet must be kept", cues)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Game.cue"))
	if err != nil {
		t.Fatalf("read cue: %v", err)
	}
	if string(data) != handEdited {
		t.Fatalf("existing cue was rewritten: %q", data)
	}
}

func TestWriteDiscHelpersSkipsPerTrackRips(t *testing.T) {
	dir := t.TempDir()
	writeDiscTestFile(t, dir, "Game (Track 1).bin", "data")
	writeDiscTestFile(t, dir, "Game (Track 2).bin", "data")

	cues, playlists, errs := writeDiscHelpers(dir, "MODE2/2352")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if cues != 0 || playlists != 0 {
		t.Fatalf("cues = %d, playlists = %d; track rips already have a sheet", cues, playlists)
	}
}

func TestWriteDiscHelpersWritesOrderedPlaylist(t *testing.T) {
	dir := t.TempDir()
	writeDiscTestFile(t, dir, "Final Fantasy VII (Disc 2).chd", "disc two")
	writeDiscTestFile(t, dir, "Final Fantasy VII (Disc 1).chd", "disc one")
	writeDiscTestFile(t, dir, "Final Fantasy VII (Disc 3).chd", "disc three")

	cues, playlists, errs := writeDiscHelpers(dir, "MODE2/2352")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if cues != 0 || playlists != 1 {
		t.Fatalf("cues = %d, playlists = %d; want 0, 1", cues, playlists)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Final Fantasy VII.m3u"))
	if err != nil {
		t.Fatalf("playlist was not written: %v", err)
	}
	want := "Final Fantasy VII (Disc 1).chd\nFinal Fantasy VII (Disc 2).chd\nFinal Fantasy VII (Disc 3).chd\n"
	if string(data) != want {
		t.Fatalf("playlist = %q, want %q", data, want)
	}
}

func TestWriteDiscHelpersPlaylistUsesTheGeneratedCues(t *testing.T) {
	dir := t.TempDir()
	writeDiscTestFile(t, dir, "Metal Gear Solid (Disc 2).bin", "disc two")
	writeDiscTestFile(t, dir, "Metal Gear Solid (Disc 1).bin", "disc one")

	cues, playlists, errs := writeDiscHelpers(dir, "MODE2/2352")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if cues != 2 || playlists != 1 {
		t.Fatalf("cues = %d, playlists = %d; want 2, 1", cues, playlists)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Metal Gear Solid.m3u"))
	if err != nil {
		t.Fatalf("playlist was not written: %v", err)
	}
	want := "Metal Gear Solid (Disc 1).cue\nMetal Gear Solid (Disc 2).cue\n"
	if string(data) != want {
		t.Fatalf("playlist = %q, want %q", data, want)
	}
}

func TestWriteDiscHelpersKeepsAnExistingPlaylist(t *testing.T) {
	dir := t.TempDir()
	writeDiscTestFile(t, dir, "Shenmue (Disc 1).chd", "one")
	writeDiscTestFile(t, dir, "Shenmue (Disc 2).chd", "two")
	writeDiscTestFile(t, dir, "Shenmue.m3u", "Disc 2 first on purpose\n")

	_, playlists, errs := writeDiscHelpers(dir, "MODE1/2352")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if playlists != 0 {
		t.Fatalf("playlists = %d, want 0: an existing playlist must be kept", playlists)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Shenmue.m3u"))
	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}
	if string(data) != "Disc 2 first on purpose\n" {
		t.Fatalf("existing playlist was rewritten: %q", data)
	}
}

func TestWriteDiscHelpersLeavesSingleDiscAlone(t *testing.T) {
	dir := t.TempDir()
	writeDiscTestFile(t, dir, "Ridge Racer (Disc 1).chd", "only disc")

	_, playlists, errs := writeDiscHelpers(dir, "MODE2/2352")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if playlists != 0 {
		t.Fatalf("playlists = %d, want 0: one disc needs no playlist", playlists)
	}
}

func TestGenerateDiscHelpersOnlyTouchesDiscSystems(t *testing.T) {
	root := t.TempDir()
	psx := filepath.Join(root, "PSX")
	gba := filepath.Join(root, "GBA")
	writeDiscTestFile(t, psx, "Wipeout.bin", "disc")
	writeDiscTestFile(t, gba, "Pokemon Emerald.bin", "not a disc at all")

	report := generateDiscHelpers([]string{root})
	if report.Systems != 1 {
		t.Fatalf("systems = %d, want 1", report.Systems)
	}
	if report.Cues != 1 || report.Created() != 1 {
		t.Fatalf("cues = %d, created = %d; want 1", report.Cues, report.Created())
	}
	if _, err := os.Stat(filepath.Join(psx, "Wipeout.cue")); err != nil {
		t.Fatalf("PSX cue missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gba, "Pokemon Emerald.cue")); !os.IsNotExist(err) {
		t.Fatalf("a cartridge folder was touched: %v", err)
	}
}

func TestGenerateDiscHelpersSkipsMissingRoots(t *testing.T) {
	report := generateDiscHelpers([]string{filepath.Join(t.TempDir(), "nope")})
	if report.Systems != 0 || report.Created() != 0 || len(report.Errors) != 0 {
		t.Fatalf("missing root produced %+v", report)
	}
	if got := report.Summary(); !strings.Contains(got, "No disc folders") {
		t.Fatalf("summary = %q", got)
	}
}

func TestDiscHelperSummaryCountsBothKinds(t *testing.T) {
	report := discHelperReport{Systems: 1, Cues: 2, Playlists: 1}
	if got := report.Summary(); !strings.Contains(got, "2 cue sheet(s) and 1 multi-disc playlist(s)") {
		t.Fatalf("summary = %q", got)
	}
	idle := discHelperReport{Systems: 1}
	if got := idle.Summary(); !strings.Contains(got, "already has its CUE and M3U") {
		t.Fatalf("summary = %q", got)
	}
}
