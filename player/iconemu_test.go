package main

import "testing"

// Emulator, core, folder and system names each get the glyph of the hardware
// they run, and whatever iconEmuGlyphFor returns is a real glyph.
func TestIconEmuGlyphFor(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		// Windows executables and their catalog names.
		{"RetroArch", "emu-retroarch"},
		{"PPSSPP", "emu-psp"},
		{"DuckStation", "emu-ps1"},
		{"PCSX2", "emu-ps2"},
		{"pcsx2-qt", "emu-ps2"},
		{"AetherSX2", "emu-ps2"},
		{"NetherSX2", "emu-ps2"},
		{"Dolphin", "emu-gc"},
		{"Cemu", "emu-wiiu"},
		{"RPCS3", "emu-ps3"},
		{"Vita3K", "emu-vita"},
		{"xemu", "emu-xbox"},
		{"melonDS", "emu-nds"},
		{"DraStic", "emu-nds"},
		{"mGBA", "emu-gba"},
		{"FCEUX", "emu-nes"},
		{"Snes9x", "emu-snes"},
		{"bsnes", "emu-snes"},
		{"Mednafen", "emu-generic"},
		{"MAME", "emu-arcade"},
		{"Citra", "emu-3ds"},
		{"Lime3DS", "emu-3ds"},
		{"Azahar", "emu-3ds"},
		{"Yuzu", "emu-switch"},
		{"Ryujinx", "emu-switch"},
		{"Eden", "emu-switch"},
		{"Citron", "emu-switch"},
		{"DOSBox", "emu-dos"},
		{"ScummVM", "emu-scummvm"},
		{"Stella", "emu-atari"},
		{"Flycast", "emu-dc"},
		{"Redream", "emu-dc"},
		{"Yabause", "emu-saturn"},
		// libretro core names, with and without the suffix.
		{"mupen64plus_next", "emu-n64"},
		{"parallel_n64_libretro", "emu-n64"},
		{"pcsx_rearmed", "emu-ps1"},
		{"mednafen_psx_hw", "emu-ps1"},
		{"beetle_saturn", "emu-saturn"},
		{"beetle_pce_fast", "emu-pce"},
		{"mgba_libretro", "emu-gba"},
		{"gpsp", "emu-gba"},
		{"fceumm", "emu-nes"},
		{"nestopia", "emu-nes"},
		{"Mesen", "emu-nes"},
		{"Mesen-S", "emu-snes"},
		{"genesis_plus_gx", "emu-genesis"},
		{"picodrive", "emu-genesis"},
		{"mame2003_plus", "emu-arcade"},
		{"fbneo", "emu-arcade"},
		{"gambatte", "emu-gbc"},
		{"sameboy", "emu-gb"},
		{"dosbox_pure", "emu-dos"},
		{"hatari", "emu-atari"},
		// TrimUI Emus/ and Roms/ folder names.
		{"FC", "emu-nes"},
		{"SFC", "emu-snes"},
		{"MD", "emu-genesis"},
		{"PS", "emu-ps1"},
		{"GB", "emu-gb"},
		{"GBC", "emu-gbc"},
		{"GBA", "emu-gba"},
		{"N64", "emu-n64"},
		{"NDS", "emu-nds"},
		{"DC", "emu-dc"},
		{"PCE", "emu-pce"},
		{"GG", "emu-gg"},
		{"MS", "emu-sms"},
		{"NEOGEO", "emu-neogeo"},
		{"ARCADE", "emu-arcade"},
		{"SATURN", "emu-saturn"},
		// System names.
		{"Game Boy", "emu-gb"},
		{"Game Boy Color", "emu-gbc"},
		{"Game Boy Advance", "emu-gba"},
		{"Nintendo DS", "emu-nds"},
		{"Nintendo 3DS", "emu-3ds"},
		{"Nintendo 64", "emu-n64"},
		{"Super Nintendo", "emu-snes"},
		{"Wii", "emu-wii"},
		{"Wii U", "emu-wiiu"},
		{"PlayStation", "emu-ps1"},
		{"PlayStation 2", "emu-ps2"},
		{"PlayStation Portable", "emu-psp"},
		{"PS Vita", "emu-vita"},
		{"Mega Drive", "emu-genesis"},
		{"Master System", "emu-sms"},
		{"Game Gear", "emu-gg"},
		{"Sega Saturn", "emu-saturn"},
		{"Dreamcast", "emu-dc"},
		{"PC Engine", "emu-pce"},
		{"Neo Geo Pocket", "emu-neogeo"},
		{"Atari 2600", "emu-atari"},
		{"MS-DOS", "emu-dos"},
		// Nothing to go on.
		{"", "emu-generic"},
		{"BIOS Doctor", "emu-generic"},
		{"RetroAchievements", "emu-generic"},
	} {
		if got := iconEmuGlyphFor(tc.name); got != tc.want {
			t.Errorf("iconEmuGlyphFor(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, rule := range iconEmuRules {
		if iconResolve(rule.glyph) != rule.glyph {
			t.Errorf("rule glyph %q is not in the icon set", rule.glyph)
		}
	}
}
