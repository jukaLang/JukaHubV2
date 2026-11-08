package main

// emusystems_test.go — the tables that turn file names into consoles: a core
// knows its system and family, a Roms/ folder of a desktop-only console is
// recognised with the Windows emulator that plays it, and every tile gets a
// system icon rather than the old gamepad.

import "testing"

func TestEmuCoreInfoNamesTheSystem(t *testing.T) {
	cases := []struct {
		core, system, family string
	}{
		{"mupen64plus_next", "Nintendo 64", emuFamilyNintendo},
		{"pcsx_rearmed", "PlayStation", emuFamilyPlayStation},
		{"beetle_psx_hw", "PlayStation", emuFamilyPlayStation},
		{"mesen-s", "SNES", emuFamilyNintendo},
		{"mesen", "NES", emuFamilyNintendo},
		{"genesis_plus_gx", "Genesis", emuFamilySega},
		{"fbneo", "Arcade", emuFamilyArcade},
		{"mame2003_plus", "Arcade", emuFamilyArcade},
		{"dosbox_pure", "DOS", emuFamilyOther},
		{"mgba_libretro.so", "Game Boy Advance", emuFamilyNintendo},
		{"totally_unknown", "", ""},
	}
	for _, tc := range cases {
		system, family := emuCoreInfo(tc.core)
		if system != tc.system || family != tc.family {
			t.Errorf("emuCoreInfo(%q) = %q/%q, want %q/%q", tc.core, system, family, tc.system, tc.family)
		}
	}
	if got := emuCoreFamily("totally_unknown"); got != emuFamilyOther {
		t.Errorf("an unknown core files under %q, want Other", got)
	}
}

func TestEmuDesktopSystemFor(t *testing.T) {
	cases := []struct {
		folder, system, emulator string
		ok                       bool
	}{
		{"XBOX", "Xbox", "xemu", true},
		{"xbox1", "Xbox", "xemu", true},
		{"PS2", "PlayStation 2", "PCSX2", true},
		{"wiiu", "Wii U", "Cemu", true},
		{"GC", "GameCube", "Dolphin", true},
		{"Switch", "Switch", "", true},
		{"GBA", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		sys, ok := emuDesktopSystemFor(tc.folder)
		if ok != tc.ok || sys.System != tc.system || sys.Emulator != tc.emulator {
			t.Errorf("emuDesktopSystemFor(%q) = %+v (%v), want %q via %q (%v)", tc.folder, sys, ok, tc.system, tc.emulator, tc.ok)
		}
		if ok && sys.Reason == "" {
			t.Errorf("%s has no reason to show on the handheld", tc.folder)
		}
	}
}

// TestEmuGlyphForPicksASystemIcon pins the obvious mappings through ICONS'
// table, including a core whose own name says nothing (its system decides) and
// an unknown name falling back to the generic emulator icon.
func TestEmuGlyphForPicksASystemIcon(t *testing.T) {
	cases := []struct {
		name, core, want string
	}{
		{"xemu", "", "emu-xbox"},
		{"Xbox", "", "emu-xbox"},
		{"PPSSPP", "", "emu-psp"},
		{"RetroArch", "", "emu-retroarch"},
		{"mupen64plus_next", "mupen64plus_next", "emu-n64"},
		{"Banana", "", "emu-generic"},
	}
	for _, tc := range cases {
		if got := emuGlyphFor(tc.name, tc.core); got != tc.want {
			t.Errorf("emuGlyphFor(%q, %q) = %q, want %q", tc.name, tc.core, got, tc.want)
		}
	}
}
