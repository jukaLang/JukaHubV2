package main

// iconemu.go — which system glyph an emulator, core or console name draws.
//
// The Emulators page names its tiles after whatever the install calls itself:
// a Windows executable ("PPSSPP", "DuckStation"), a TrimUI Emus/ folder ("FC",
// "SFC", "PS"), a libretro core file ("mupen64plus_next", "genesis_plus_gx"),
// or a system ("Game Boy Advance"). iconEmuGlyphFor turns any of those into one
// of the emu-* glyphs in icondefs.go, so each tile shows the hardware it runs
// instead of a generic gamepad.

import (
	"strings"
	"sync"
)

// iconEmuRule maps name keys to a system glyph. A key matches when it equals a
// word of the name, equals the whole name with its separators removed ("Wii U"
// → "wiiu"), or — for keys of five letters or more — appears anywhere in it
// ("snes9x_next", "mupen64plus_next"). Short keys only match whole words, so
// "ps" never matches inside an unrelated word.
type iconEmuRule struct {
	glyph string
	keys  []string
}

// iconEmuRules is checked in order: more specific systems first, so PS2 wins
// over PS, Game Boy Color over Game Boy, 3DS over DS, Wii U over Wii, and the
// SNES "Mesen-S" over the NES "Mesen". RetroArch itself comes last, after
// every core name that merely carries a "_libretro" suffix.
var iconEmuRules = [...]iconEmuRule{
	{"emu-ps3", []string{"rpcs3", "ps3", "playstation3"}},
	{"emu-ps2", []string{"pcsx2", "aethersx2", "nethersx2", "lrps2", "ps2", "playstation2"}},
	{"emu-vita", []string{"vita3k", "psvita", "vita", "psv"}},
	{"emu-psp", []string{"ppsspp", "psp", "playstationportable"}},
	{"emu-ps1", []string{"duckstation", "swanstation", "pcsxrearmed", "pcsx", "rearmed", "epsxe", "psx", "ps1", "ps", "playstation"}},
	{"emu-dos", []string{"dosbox", "msdos", "dos"}},
	{"emu-scummvm", []string{"scummvm", "scumm"}},
	{"emu-xbox", []string{"xemu", "cxbx", "xenia", "xbox", "xbox360"}},
	{"emu-switch", []string{"yuzu", "ryujinx", "eden", "citron", "sudachi", "suyu", "torzu", "switch", "nintendoswitch"}},
	{"emu-3ds", []string{"citra", "lime3ds", "azahar", "panda3ds", "mandarine", "3ds", "nintendo3ds"}},
	{"emu-nds", []string{"melonds", "drastic", "desmume", "nds", "ds", "nintendods"}},
	{"emu-wiiu", []string{"cemu", "decaf", "wiiu"}},
	{"emu-wii", []string{"wii"}},
	{"emu-gc", []string{"dolphin", "gamecube", "gc", "ngc"}},
	{"emu-n64", []string{"mupen64plus", "mupen64", "mupen", "parallel", "project64", "simple64", "gopher64", "rmg", "n64", "nintendo64"}},
	{"emu-gba", []string{"mgba", "gpsp", "vbanext", "vbam", "vba", "visualboyadvance", "meteor", "gba", "gameboyadvance"}},
	{"emu-gbc", []string{"gambatte", "gbc", "gameboycolor", "gameboycolour"}},
	{"emu-gb", []string{"sameboy", "gearboy", "tgbdual", "gb", "sgb", "gameboy"}},
	{"emu-snes", []string{"mesens", "snes9x", "bsnes", "supafaust", "snes", "sfc", "supernintendo", "superfamicom"}},
	{"emu-nes", []string{"fceux", "fceumm", "nestopia", "quicknes", "mesen", "nes", "fc", "fds", "famicom", "nintendoentertainmentsystem"}},
	{"emu-genesis", []string{"genesisplusgx", "picodrive", "blastem", "kega", "fusion", "gpgx", "genesis", "megadrive", "md", "segacd", "megacd", "32x"}},
	{"emu-sms", []string{"smsplus", "gearsystem", "sms", "ms", "mastersystem", "segamastersystem"}},
	{"emu-gg", []string{"gamegear", "gg"}},
	{"emu-dc", []string{"flycast", "reicast", "redream", "nulldc", "demul", "dreamcast", "dc"}},
	{"emu-saturn", []string{"yabause", "yabasanshiro", "kronos", "saturn", "segasaturn", "ss"}},
	{"emu-pce", []string{"pcengine", "turbografx", "turbografx16", "tg16", "supergrafx", "sgx", "pcfx", "pce"}},
	{"emu-neogeo", []string{"neogeo", "neocd", "ngp", "ngpc", "race"}},
	{"emu-arcade", []string{"mame2000", "mame2003", "mame2010", "mame2015", "mame2016", "mame", "fbneo", "fbalpha", "finalburn", "fba", "arcade", "cps1", "cps2", "cps3", "naomi", "atomiswave"}},
	{"emu-atari", []string{"stella", "atari", "a2600", "a5200", "a7800", "prosystem", "handy", "lynx", "virtualjaguar", "jaguar"}},
	{"emu-retroarch", []string{"retroarch", "libretro"}},
}

// iconEmuCache memoizes iconEmuGlyphFor: a tile asks every frame, and the
// answer for a name never changes.
var iconEmuCache = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// iconEmuGlyphFor maps an emulator, libretro core, TrimUI folder or system name
// to its emu-* glyph, or "emu-generic" when nothing matches. Mednafen and other
// multi-system emulators get their system from the core or folder name
// ("mednafen_psx_hw" → PS1, "beetle_saturn" → Saturn) and the generic glyph on
// their own.
func iconEmuGlyphFor(name string) string {
	iconEmuCache.Lock()
	glyph, ok := iconEmuCache.m[name]
	iconEmuCache.Unlock()
	if ok {
		return glyph
	}
	glyph = iconEmuMatch(name)
	iconEmuCache.Lock()
	if len(iconEmuCache.m) >= 512 {
		iconEmuCache.m = map[string]string{}
	}
	iconEmuCache.m[name] = glyph
	iconEmuCache.Unlock()
	return glyph
}

// iconEmuMatch is iconEmuGlyphFor without the cache.
func iconEmuMatch(name string) string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	compact := strings.Join(words, "")
	if compact == "" {
		return "emu-generic"
	}
	for _, rule := range iconEmuRules {
		for _, key := range rule.keys {
			if key == compact || (len(key) >= 5 && strings.Contains(compact, key)) {
				return rule.glyph
			}
			for _, w := range words {
				if w == key {
					return rule.glyph
				}
			}
		}
	}
	return "emu-generic"
}
