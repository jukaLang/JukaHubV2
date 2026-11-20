package main

import (
	"encoding/binary"
	"testing"
)

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

// TestGodotPackHeader pins the reading of outside data for the Godot shelf
// (godot.go): the engine version in a .pck's header, the versions in engine
// file names, and Roms/GODOT listing the .pck and not its side files.
func TestGodotPackHeader(t *testing.T) {
	head := func(magic string, nums ...uint32) []byte {
		b := []byte(magic)
		for _, n := range nums {
			b = binary.LittleEndian.AppendUint32(b, n)
		}
		return b
	}
	if v, ok := godotParsePackHeader(head("GDPC", 1, 3, 5, 2)); !ok || v != (godotVersion{3, 5, 2}) {
		t.Fatalf("Godot 3 header = %v %v", v, ok)
	}
	if v, ok := godotParsePackHeader(head("GDPC", 2, 4, 3, 0, 0)); !ok || v.String() != "4.3.0" {
		t.Fatalf("Godot 4 header = %v %v", v, ok)
	}
	for _, bad := range [][]byte{head("PK\x03\x04", 1, 3, 5, 2), head("GDPC", 1, 0, 5, 2), head("GDPC", 1, 3, 5)} {
		if v, ok := godotParsePackHeader(bad); ok {
			t.Fatalf("%q read as Godot %v", bad, v)
		}
	}
	for name, want := range map[string]string{
		"frt_3.5.2": "3.5.2", "frt_3.2.3.squashfs": "3.2.3", "godot_4.2.2.squashfs": "4.2.2",
		"Godot_v3.5.3-stable_x11.64": "3.5.3", "frt_100_332_arm64.bin": "3.3.2",
	} {
		if v, ok := godotEngineVersion(name); !ok || v.String() != want {
			t.Fatalf("engine %q = %v %v, want %s", name, v, ok, want)
		}
	}
	if v, ok := godotEngineVersion("launch.sh"); ok {
		t.Fatalf("launch.sh read as engine %v", v)
	}
	if !godotEngineFits([]godotVersion{{3, 5, 2}}, godotVersion{3, 4, 0}) || godotEngineFits([]godotVersion{{3, 5, 2}}, godotVersion{4, 2, 0}) {
		t.Fatal("engine fit: a 3.5 engine plays 3.4 packs and no 4.x pack")
	}
	if alias, ok := romSystemAliases["godot"]; !ok || alias.Name != godotSystemName {
		t.Fatalf("godot alias = %+v, known %v", alias, ok)
	}
	if !romListsFile(godotSystemName, "/r", "/r/game.pck", ".pck") || romListsFile(godotSystemName, "/r", "/r/game.png", ".png") {
		t.Fatal("Roms/GODOT must list .pck games and not their pictures")
	}
}
