package main

// emusystems.go — what each emulator, core and Roms/ folder is for.
//
// Three small tables the Emulators page and the Core builds page share:
//
//   - emuCoreSystems maps a libretro core to the system it runs and the family
//     the Core builds page filters by ("mupen64plus_next" is Nintendo 64, a
//     Nintendo core), so a list of file names reads as a list of consoles.
//   - emuDesktopSystems are the consoles only a desktop-class machine runs:
//     the Windows emulator that plays each one, and the honest reason the
//     handheld cannot, so a Roms/PS2 folder on the Smart Pro says "Not
//     supported" instead of offering an install that could never work.
//   - emuGlyphFor turns a catalog entry into one of the shared emu-* icons by
//     asking ICONS' iconEmuGlyphFor, with the core's system as a second guess.

import "strings"

// emuCoreSystem is one row of the core table.
type emuCoreSystem struct {
	prefix string
	system string
	family string
}

// The families the Core builds page can filter by, in chip order.
const (
	emuFamilyNintendo    = "Nintendo"
	emuFamilyPlayStation = "PlayStation"
	emuFamilySega        = "Sega"
	emuFamilyArcade      = "Arcade"
	emuFamilyOther       = "Other"
)

// emuFamilies is the chip order of the families.
var emuFamilies = []string{emuFamilyNintendo, emuFamilyPlayStation, emuFamilySega, emuFamilyArcade, emuFamilyOther}

// emuCoreSystems maps core name prefixes to systems. Longer prefixes that share
// a start with a shorter one come first ("mesen-s" before "mesen").
var emuCoreSystems = []emuCoreSystem{
	{"pcsx_rearmed", "PlayStation", emuFamilyPlayStation},
	{"swanstation", "PlayStation", emuFamilyPlayStation},
	{"duckstation", "PlayStation", emuFamilyPlayStation},
	{"beetle_psx", "PlayStation", emuFamilyPlayStation},
	{"mednafen_psx", "PlayStation", emuFamilyPlayStation},
	{"ppsspp", "PlayStation Portable", emuFamilyPlayStation},
	{"pcsx2", "PlayStation 2", emuFamilyPlayStation},
	{"lrps2", "PlayStation 2", emuFamilyPlayStation},
	{"play", "PlayStation 2", emuFamilyPlayStation},
	{"mupen64plus", "Nintendo 64", emuFamilyNintendo},
	{"parallel_n64", "Nintendo 64", emuFamilyNintendo},
	{"mesen-s", "SNES", emuFamilyNintendo},
	{"snes9x", "SNES", emuFamilyNintendo},
	{"bsnes", "SNES", emuFamilyNintendo},
	{"beetle_supafaust", "SNES", emuFamilyNintendo},
	{"fceumm", "NES", emuFamilyNintendo},
	{"nestopia", "NES", emuFamilyNintendo},
	{"quicknes", "NES", emuFamilyNintendo},
	{"mesen", "NES", emuFamilyNintendo},
	{"gambatte", "Game Boy", emuFamilyNintendo},
	{"sameboy", "Game Boy", emuFamilyNintendo},
	{"gearboy", "Game Boy", emuFamilyNintendo},
	{"tgbdual", "Game Boy", emuFamilyNintendo},
	{"mgba", "Game Boy Advance", emuFamilyNintendo},
	{"gpsp", "Game Boy Advance", emuFamilyNintendo},
	{"vba_next", "Game Boy Advance", emuFamilyNintendo},
	{"vbam", "Game Boy Advance", emuFamilyNintendo},
	{"meteor", "Game Boy Advance", emuFamilyNintendo},
	{"melonds", "Nintendo DS", emuFamilyNintendo},
	{"desmume", "Nintendo DS", emuFamilyNintendo},
	{"citra", "Nintendo 3DS", emuFamilyNintendo},
	{"dolphin", "GameCube", emuFamilyNintendo},
	{"pokemini", "Pokémon Mini", emuFamilyNintendo},
	{"beetle_vb", "Virtual Boy", emuFamilyNintendo},
	{"mednafen_vb", "Virtual Boy", emuFamilyNintendo},
	{"genesis_plus_gx", "Genesis", emuFamilySega},
	{"picodrive", "Genesis", emuFamilySega},
	{"blastem", "Genesis", emuFamilySega},
	{"smsplus", "Master System", emuFamilySega},
	{"gearsystem", "Master System", emuFamilySega},
	{"flycast", "Dreamcast", emuFamilySega},
	{"reicast", "Dreamcast", emuFamilySega},
	{"beetle_saturn", "Saturn", emuFamilySega},
	{"mednafen_saturn", "Saturn", emuFamilySega},
	{"yabasanshiro", "Saturn", emuFamilySega},
	{"yabause", "Saturn", emuFamilySega},
	{"kronos", "Saturn", emuFamilySega},
	{"mame", "Arcade", emuFamilyArcade},
	{"fbneo", "Arcade", emuFamilyArcade},
	{"fbalpha", "Arcade", emuFamilyArcade},
	{"fb_alpha", "Arcade", emuFamilyArcade},
	{"neocd", "Neo Geo CD", emuFamilyArcade},
	{"beetle_pce", "PC Engine", emuFamilyOther},
	{"mednafen_pce", "PC Engine", emuFamilyOther},
	{"beetle_ngp", "Neo Geo Pocket", emuFamilyOther},
	{"mednafen_ngp", "Neo Geo Pocket", emuFamilyOther},
	{"race", "Neo Geo Pocket", emuFamilyOther},
	{"beetle_wswan", "WonderSwan", emuFamilyOther},
	{"mednafen_wswan", "WonderSwan", emuFamilyOther},
	{"stella", "Atari 2600", emuFamilyOther},
	{"a5200", "Atari 5200", emuFamilyOther},
	{"atari800", "Atari 8-bit", emuFamilyOther},
	{"prosystem", "Atari 7800", emuFamilyOther},
	{"handy", "Atari Lynx", emuFamilyOther},
	{"beetle_lynx", "Atari Lynx", emuFamilyOther},
	{"virtualjaguar", "Atari Jaguar", emuFamilyOther},
	{"dosbox", "DOS", emuFamilyOther},
	{"scummvm", "ScummVM", emuFamilyOther},
	{"vecx", "Vectrex", emuFamilyOther},
	{"fuse", "ZX Spectrum", emuFamilyOther},
	{"vice", "Commodore 64", emuFamilyOther},
	{"puae", "Amiga", emuFamilyOther},
	{"bluemsx", "MSX", emuFamilyOther},
	{"fmsx", "MSX", emuFamilyOther},
}

// emuCoreInfo returns the system and family a core runs, or "" for a core the
// table does not know.
func emuCoreInfo(core string) (system, family string) {
	core = normalizeCoreName(core)
	if core == "" {
		return "", ""
	}
	for _, row := range emuCoreSystems {
		if strings.HasPrefix(core, row.prefix) {
			return row.system, row.family
		}
	}
	return "", ""
}

// emuCoreFamily is the filter family of a core, Other when unknown.
func emuCoreFamily(core string) string {
	if _, family := emuCoreInfo(core); family != "" {
		return family
	}
	return emuFamilyOther
}

// emuDesktopSystem is a console only a desktop-class machine runs.
type emuDesktopSystem struct {
	System string
	// Folders are the Roms/ folder names it is found under, lower-case.
	Folders []string
	// Emulator is the winEmuCatalog name that plays it on Windows ("" when the
	// catalog has none).
	Emulator string
	// Reason is why the handheld cannot run it, said plainly.
	Reason string
}

// emuDesktopSystems are the consoles the Smart Pro's A133P (four Cortex-A53
// cores and a PowerVR GE8300) cannot emulate at any speed worth calling
// playable. Each reason names what it does need instead of pretending.
var emuDesktopSystems = []emuDesktopSystem{
	{
		System:   "Xbox",
		Folders:  []string{"xbox", "xbox1", "ogxbox", "microsoft xbox", "microsoft - xbox"},
		Emulator: "xemu",
		Reason:   "xemu needs a desktop-class CPU and GPU — the Smart Pro's A133P can't run it. Games like Halo 2 run on PCs and high-end Android phones.",
	},
	{
		System:   "PlayStation 2",
		Folders:  []string{"ps2", "playstation 2", "playstation2", "sony - playstation 2"},
		Emulator: "PCSX2",
		Reason:   "PlayStation 2 emulation needs a far faster CPU and GPU than the A133P — a PC, or a recent Snapdragon-class phone.",
	},
	{
		System:   "PlayStation 3",
		Folders:  []string{"ps3", "playstation 3", "playstation3", "sony - playstation 3"},
		Emulator: "RPCS3",
		Reason:   "RPCS3 needs a desktop-class x86-64 CPU and GPU; nothing in a handheld of this class can run it.",
	},
	{
		System:   "Wii U",
		Folders:  []string{"wiiu", "wii u", "nintendo - wii u"},
		Emulator: "Cemu",
		Reason:   "Cemu needs a desktop-class CPU and GPU; the A133P is far too slow for Wii U games.",
	},
	{
		System:   "PS Vita",
		Folders:  []string{"psvita", "vita", "psv", "playstation vita", "sony - playstation vita"},
		Emulator: "Vita3K",
		Reason:   "Vita3K needs a desktop-class CPU and a Vulkan GPU; the Smart Pro's GPU can't run it.",
	},
	{
		System:   "GameCube",
		Folders:  []string{"gc", "gamecube", "ngc", "nintendo - gamecube"},
		Emulator: "Dolphin",
		Reason:   "Dolphin needs a much faster CPU and GPU than the A133P; GameCube games would not reach playable speed.",
	},
	{
		System:   "Wii",
		Folders:  []string{"wii", "nintendo - wii"},
		Emulator: "Dolphin",
		Reason:   "Dolphin needs a much faster CPU and GPU than the A133P; Wii games would not reach playable speed.",
	},
	{
		System:  "Switch",
		Folders: []string{"switch", "nsw", "nintendo switch", "nintendo - switch"},
		Reason:  "Switch emulation needs a desktop-class CPU and GPU, far beyond the A133P.",
	},
	{
		System:  "Xbox 360",
		Folders: []string{"xbox360", "x360", "xbox 360", "microsoft - xbox 360"},
		Reason:  "Xbox 360 emulation needs a desktop-class x86-64 CPU and GPU.",
	},
}

// emuDesktopSystemFor finds the desktop-only console a Roms/ folder or system
// name belongs to.
func emuDesktopSystemFor(name string) (emuDesktopSystem, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return emuDesktopSystem{}, false
	}
	for _, sys := range emuDesktopSystems {
		if strings.EqualFold(sys.System, key) {
			return sys, true
		}
		for _, folder := range sys.Folders {
			if folder == key {
				return sys, true
			}
		}
	}
	return emuDesktopSystem{}, false
}

// emuXboxEntryName is the Xbox tile the handheld always lists.
const emuXboxEntryName = "Xbox"

// emuGlyphFor picks the shared vector icon for a catalog entry: ICONS'
// iconEmuGlyphFor on the entry's name, then on the system its core runs, and
// the generic emulator icon when neither is known.
func emuGlyphFor(name, core string) string {
	known := func(glyph string) bool {
		glyph = strings.TrimSpace(glyph)
		return glyph != "" && glyph != "emu-generic"
	}
	if glyph := iconEmuGlyphFor(name); known(glyph) {
		return glyph
	}
	if core = normalizeCoreName(core); core != "" {
		if system, _ := emuCoreInfo(core); system != "" {
			if glyph := iconEmuGlyphFor(system); known(glyph) {
				return glyph
			}
		}
		if glyph := iconEmuGlyphFor(core); known(glyph) {
			return glyph
		}
	}
	if sys, ok := emuDesktopSystemFor(name); ok {
		if glyph := iconEmuGlyphFor(sys.System); known(glyph) {
			return glyph
		}
	}
	return "emu-generic"
}
