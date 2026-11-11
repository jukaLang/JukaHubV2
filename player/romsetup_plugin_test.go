package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A plugin's ROM format has to reach the scan: its own folder under Roms, its
// extension inside a known folder, and a .zip in its folder all become games of
// the plugin's system.
func TestScanROMLibraryReadsPluginFormats(t *testing.T) {
	plugxRegistry.mu.Lock()
	prev := plugxRegistry.parsers
	plugxRegistry.parsers = []plugxRomParser{{
		Plugin:     "minimon",
		Name:       "Pokémon mini",
		System:     "pokemini",
		Extensions: []string{".min"},
	}}
	plugxRegistry.mu.Unlock()
	t.Cleanup(func() {
		plugxRegistry.mu.Lock()
		plugxRegistry.parsers = prev
		plugxRegistry.mu.Unlock()
	})

	root := t.TempDir()
	write := func(rel string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("rom"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("Pokémon mini", "Monster.min"))
	write(filepath.Join("Pokémon mini", "Packed.zip"))
	write(filepath.Join("nes", "Homebrew.min"))
	write(filepath.Join("nes", "Mario.nes"))

	report := scanROMLibrary([]string{root})
	if len(report.Games) != 4 {
		t.Fatalf("got %d games, want 4: %+v", len(report.Games), report.Games)
	}
	byName := map[string]scannedROM{}
	for _, game := range report.Games {
		byName[filepath.Base(game.Path)] = game
	}
	for _, name := range []string{"Monster.min", "Packed.zip", "Homebrew.min"} {
		game, ok := byName[name]
		if !ok {
			t.Fatalf("%s was not scanned", name)
		}
		if game.System != "Pokémon mini" || game.Collection != "Plugins" {
			t.Errorf("%s = %q / %q, want the plugin system", name, game.System, game.Collection)
		}
	}
	if game := byName["Mario.nes"]; game.System != "NES" {
		t.Errorf("built-in NES file was claimed by a plugin: %+v", game)
	}
	if report.Counts["Pokémon mini"] != 3 || report.Counts["NES"] != 1 {
		t.Errorf("counts = %v", report.Counts)
	}
}

// A plugin cannot take over a built-in extension or folder name.
func TestRomPluginSystemsKeepsBuiltinCatalogue(t *testing.T) {
	byExt, byFolder := romPluginSystems([]plugxRomParser{{
		Name:       "Fake NES",
		System:     "nes",
		Extensions: []string{".nes", ".min"},
	}})
	if _, ok := byExt[".nes"]; ok {
		t.Error("plugin overrode the built-in .nes extension")
	}
	if _, ok := byFolder["nes"]; ok {
		t.Error("plugin overrode the built-in NES folder")
	}
	if got := byExt[".min"].Name; got != "Fake NES" {
		t.Errorf("new extension maps to %q", got)
	}
	if got := byFolder["fakenes"].Name; got != "Fake NES" {
		t.Errorf("plugin folder maps to %q", got)
	}
}
