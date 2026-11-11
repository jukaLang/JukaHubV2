package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The handheld catalog lists RetroArch itself beside its cores: its binary,
// how many cores it holds and the config it reads.
func TestDeviceCatalogHasARetroArchTile(t *testing.T) {
	sd := t.TempDir()
	ra := filepath.Join(sd, "RetroArch")
	cores := filepath.Join(ra, ".retroarch", "cores")
	for _, p := range []string{
		filepath.Join(cores, "mgba_libretro.so"),
		filepath.Join(cores, "snes9x_libretro.so"),
		filepath.Join(ra, "ra64.trimui"),
		filepath.Join(ra, "retroarch.cfg"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entries := deviceEmulatorEntries(nil, "", cores)
	tile := crossmixEntry(t, entries, emuRetroArchTileName)
	if tile.Status != emuAvailable || tile.Launch != filepath.Join(ra, "ra64.trimui") {
		t.Fatalf("RetroArch tile: status %d, launch %q", tile.Status, tile.Launch)
	}
	for _, want := range []string{"2 cores", ra, "settings in retroarch.cfg"} {
		if !strings.Contains(tile.Detail, want) {
			t.Errorf("detail %q does not say %q", tile.Detail, want)
		}
	}
	if emuCoreForEntry(tile) != "" {
		t.Error("the RetroArch tile must not offer a core install or update")
	}
	if got := emuRetroArchMenuTile(tile); got == IsWindows() {
		t.Errorf("menu tile = %t on this OS", got)
	}
	// Core rows stay what they were: availability rows A turns into a search.
	if row := crossmixEntry(t, entries, "mgba"); row.Launch != "" || emuRetroArchMenuTile(row) {
		t.Errorf("core row changed: %+v", row)
	}

	// Without the binary the folder is still listed, as broken.
	if err := os.Remove(filepath.Join(ra, "ra64.trimui")); err != nil {
		t.Fatal(err)
	}
	tile = crossmixEntry(t, deviceEmulatorEntries(nil, "", cores), emuRetroArchTileName)
	if tile.Status != emuBroken || tile.Launch != "" || emuRetroArchMenuTile(tile) {
		t.Errorf("RetroArch without a binary: %+v", tile)
	}
	// No RetroArch folder, no tile.
	if _, ok := deviceRetroArchEntry(filepath.Join(sd, "nowhere", ".retroarch", "cores"), 0); ok {
		t.Error("a missing RetroArch folder produced a tile")
	}
}

// A core row searches the library by the library's own name for its system.
func TestEmuCoreSystemQuery(t *testing.T) {
	for _, tc := range []struct{ core, want string }{
		{"mgba", "Game Boy Advance"},
		{"genesis_plus_gx", "Mega Drive"},
		{"pcsx_rearmed", "PlayStation"},
		{"mystery_core", ""},
	} {
		if got := emuCoreSystemQuery(emuEntry{Name: tc.core, Core: tc.core}); got != tc.want {
			t.Errorf("%s: query %q, want %q", tc.core, got, tc.want)
		}
	}
}
