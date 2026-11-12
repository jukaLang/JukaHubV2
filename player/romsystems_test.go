package main

import "testing"

// TestRomSystemAliasCoverage pins LIB-2: the folder names the firmware,
// CrossMix and No-Intro sets use for the systems in emusystems.go's core
// inventory resolve to a library system, and every system that has a libretro
// core names it.
func TestRomSystemAliasCoverage(t *testing.T) {
	cases := []struct {
		folder string
		system string
		core   string
	}{
		{"arcade", "Arcade", "fbneo"},
		{"mame2003", "Arcade", "fbneo"},
		{"a2600", "Atari 2600", "stella"},
		{"atari - atari 7800", "Atari 7800", "prosystem"},
		{"lynx", "Atari Lynx", "handy"},
		{"wsc", "WonderSwan Color", "mednafen_wswan"},
		{"bandai - wonderswan", "WonderSwan", "mednafen_wswan"},
		{"ngpc", "Neo Geo Pocket Color", "mednafen_ngp"},
		{"snk - neo geo pocket", "Neo Geo Pocket", "mednafen_ngp"},
		{"neocd", "Neo Geo CD", "neocd"},
		{"sega - sega cd", "Sega CD", "genesis_plus_gx"},
		{"sega32x", "32X", "picodrive"},
		{"dosbox", "DOS", "dosbox"},
	}
	for _, tc := range cases {
		alias, ok := romSystemAliases[tc.folder]
		if !ok {
			t.Fatalf("%q is not a known ROM folder", tc.folder)
		}
		if alias.Name != tc.system {
			t.Fatalf("%q maps to %q, want %q", tc.folder, alias.Name, tc.system)
		}
		if alias.Collection == "" {
			t.Fatalf("%q has no collection group", tc.folder)
		}
		if got := defaultCoreForSystem(alias.Name); got != tc.core {
			t.Fatalf("default core for %q = %q, want %q", alias.Name, got, tc.core)
		}
	}
	// PICO-8 has its own runtime, not a libretro core: the folder must still be
	// known so its games are scanned, but no core may be invented for it.
	if alias, ok := romSystemAliases["pico8"]; !ok || alias.Name != "PICO-8" {
		t.Fatalf("pico8 alias = %+v, known %v", alias, ok)
	}
	if core := defaultCoreForSystem("PICO-8"); core != "" {
		t.Fatalf("PICO-8 default core = %q, want none (its own program)", core)
	}
}

// TestRomExtensionsCoverNewSystems keeps the scan from dropping the carts of
// the systems above: an alias with no extension would list an empty folder.
func TestRomExtensionsCoverNewSystems(t *testing.T) {
	for _, ext := range []string{".a26", ".a78", ".lnx", ".ws", ".wsc", ".ngp", ".32x", ".p8"} {
		if !romFileExtensions[ext] {
			t.Fatalf("%s is not a recognised ROM extension", ext)
		}
	}
}
