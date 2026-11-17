package main

// emubest.go — the best emulator for each system, on this hardware.
//
// CrossMix and the other TrimUI firmwares ship more than one emulator for the
// heavier systems — DraStic beside the melonDS and DeSmuME cores, a standalone
// PPSSPP beside RetroArch's, a standalone Mupen64Plus beside two N64 cores —
// and which one plays best is folklore. The table below writes that folklore
// down, ranked, per platform: the handheld (TrimUI Smart Pro / Brick, Allwinner
// A133P, PowerVR GE8300, 1 GB) and the Windows desktop. Where the ranking comes
// from (and which rows are judgement calls rather than sourced) is recorded in
// docs/GAME_LIBRARY.md, "Launching".
//
// The ranking is advice, never a decision taken from the user:
//
//   - The emulator picker (emupicker.go) lists the recommended installed
//     emulator first, with a "Recommended" badge; the highlight still starts
//     on the one picked last.
//   - With no pin for the game, no pin for the system and nothing picked last,
//     the game used to start in whichever emulator the catalog matched first.
//     That plain first match is now the recommended one (findGameLauncher,
//     resolveGameLauncherFor). Every pin, and the emulator picked last, still
//     win as before.
//   - On a PC the Emulators page marks the install tile of a better
//     emulator (DuckStation over a PlayStation core, melonDS over DeSmuME)
//     for a system that has games but only a weaker emulator installed. It
//     never installs anything by itself. The handheld gets no such tile:
//     its firmware's launch scripts choose the core (emuBestInstallTile).
//
// Recognising which emulator a catalog entry is costs at most one read of its
// launch script, kept per file (size and modification time), so the picker and
// the catalog build never read a script twice.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// emuBestKind says how a candidate runs: its own program, or a libretro core
// inside RetroArch.
type emuBestKind int

const (
	emuBestStandalone emuBestKind = iota
	emuBestCore
)

// emuBestCandidate is one ranked emulator for a system.
type emuBestCandidate struct {
	// Name is what the user reads ("DraStic", "mGBA (RetroArch)").
	Name string
	Kind emuBestKind
	// Core is the libretro core of a core candidate, in the buildbot's
	// spelling ("mupen64plus_next"). A core whose name starts with it counts
	// too (yabasanshiro_a133p is YabaSanshiro).
	Core string
	// Tokens recognise a standalone emulator: normalised words its launcher,
	// its folder or its launch script carries ("drastic", "ppsspp").
	Tokens []string
	// Win is the Windows emulator (emuWinSpecs / winEmuCatalog name) that is
	// this candidate on a PC, so the Emulators page can install it there.
	Win string
}

// emuBestSystem ranks the emulators of one library system (the names
// systemLauncherAliases uses), best first, for each platform.
type emuBestSystem struct {
	System   string
	Handheld []emuBestCandidate
	Desktop  []emuBestCandidate
}

// emuBestSA is a standalone candidate.
func emuBestSA(name, win string, tokens ...string) emuBestCandidate {
	return emuBestCandidate{Name: name, Kind: emuBestStandalone, Win: win, Tokens: tokens}
}

// emuBestRA is a RetroArch core candidate.
func emuBestRA(name, core string) emuBestCandidate {
	return emuBestCandidate{Name: name, Kind: emuBestCore, Core: core}
}

// emuBestGameBoy is the Game Boy and Game Boy Color ranking, the same on both
// platforms.
var emuBestGameBoy = []emuBestCandidate{
	emuBestRA("Gambatte (RetroArch)", "gambatte"),
	emuBestRA("SameBoy (RetroArch)", "sameboy"),
	emuBestRA("mGBA (RetroArch)", "mgba"),
	emuBestRA("Gearboy (RetroArch)", "gearboy"),
}

// emuBestTable is the ranking. Handheld: what CrossMix (cizia64/CrossMix-OS
// wiki, Emulators page; release notes 1.2 and 1.3), Knulli (Scarab release,
// standalone YabaSanshiro) and spruceOS (PRs 1873/1874, Saturn and N64 on
// the A133P) ship and benchmark on this chip. Desktop: the standalone
// emulators the Windows page installs, ahead of the cores they replace.
var emuBestTable = []emuBestSystem{
	{
		System: "Nintendo DS",
		Handheld: []emuBestCandidate{
			emuBestSA("DraStic", "", "drastic"),
			emuBestRA("melonDS DS (RetroArch)", "melondsds"),
			emuBestRA("melonDS (RetroArch)", "melonds"),
			emuBestRA("DeSmuME (RetroArch)", "desmume"),
		},
		Desktop: []emuBestCandidate{
			emuBestSA("melonDS", "melonDS", "melonds"),
			emuBestRA("melonDS DS (RetroArch)", "melondsds"),
			emuBestRA("DeSmuME (RetroArch)", "desmume"),
		},
	},
	{
		System: "PlayStation Portable",
		Handheld: []emuBestCandidate{
			emuBestSA("PPSSPP", "", "ppsspp"),
			emuBestRA("PPSSPP (RetroArch)", "ppsspp"),
		},
		Desktop: []emuBestCandidate{
			emuBestSA("PPSSPP", "PPSSPP", "ppsspp"),
			emuBestRA("PPSSPP (RetroArch)", "ppsspp"),
		},
	},
	{
		System: "Nintendo 64",
		Handheld: []emuBestCandidate{
			emuBestSA("Mupen64Plus", "", "mupen64plus", "m64p"),
			emuBestRA("Mupen64Plus-Next (RetroArch)", "mupen64plus_next"),
			emuBestRA("ParaLLEl N64 (RetroArch)", "parallel_n64"),
		},
		Desktop: []emuBestCandidate{
			emuBestRA("Mupen64Plus-Next (RetroArch)", "mupen64plus_next"),
			emuBestRA("ParaLLEl N64 (RetroArch)", "parallel_n64"),
		},
	},
	{
		System: "Dreamcast",
		Handheld: []emuBestCandidate{
			emuBestSA("Flycast", "", "flycast"),
			emuBestRA("Flycast (RetroArch)", "flycast"),
		},
		Desktop: []emuBestCandidate{
			emuBestRA("Flycast (RetroArch)", "flycast"),
		},
	},
	{
		System: "Saturn",
		Handheld: []emuBestCandidate{
			emuBestSA("YabaSanshiro", "", "yabasanshiro"),
			emuBestRA("YabaSanshiro (RetroArch)", "yabasanshiro"),
			emuBestRA("Yabause (RetroArch)", "yabause"),
		},
		Desktop: []emuBestCandidate{
			emuBestRA("Beetle Saturn (RetroArch)", "mednafen_saturn"),
			emuBestRA("Kronos (RetroArch)", "kronos"),
			emuBestRA("YabaSanshiro (RetroArch)", "yabasanshiro"),
		},
	},
	{
		System: "PlayStation",
		Handheld: []emuBestCandidate{
			emuBestRA("PCSX-ReARMed (RetroArch)", "pcsx_rearmed"),
			emuBestRA("SwanStation (RetroArch)", "swanstation"),
			emuBestRA("DuckStation (RetroArch)", "duckstation"),
			emuBestRA("Beetle PSX (RetroArch)", "mednafen_psx"),
		},
		Desktop: []emuBestCandidate{
			emuBestSA("DuckStation", "DuckStation", "duckstation"),
			emuBestRA("SwanStation (RetroArch)", "swanstation"),
			emuBestRA("Beetle PSX HW (RetroArch)", "mednafen_psx_hw"),
			emuBestRA("PCSX-ReARMed (RetroArch)", "pcsx_rearmed"),
		},
	},
	{
		System: "Game Boy Advance",
		Handheld: []emuBestCandidate{
			emuBestRA("mGBA (RetroArch)", "mgba"),
			emuBestSA("mGBA", "", "mgba"),
			emuBestRA("gpSP (RetroArch)", "gpsp"),
			emuBestRA("VBA Next (RetroArch)", "vba_next"),
		},
		Desktop: []emuBestCandidate{
			emuBestSA("mGBA", "mGBA", "mgba"),
			emuBestRA("mGBA (RetroArch)", "mgba"),
		},
	},
	{
		System: "SNES",
		Handheld: []emuBestCandidate{
			emuBestRA("Snes9x (RetroArch)", "snes9x"),
			emuBestRA("Snes9x 2005 (RetroArch)", "snes9x2005"),
			emuBestRA("bsnes (RetroArch)", "bsnes"),
		},
		Desktop: []emuBestCandidate{
			emuBestRA("Snes9x (RetroArch)", "snes9x"),
			emuBestSA("Snes9x", "Snes9x", "snes9x"),
			emuBestSA("bsnes", "bsnes", "bsnes"),
		},
	},
	{
		System: "NES",
		Handheld: []emuBestCandidate{
			emuBestRA("FCEUmm (RetroArch)", "fceumm"),
			emuBestRA("Nestopia (RetroArch)", "nestopia"),
			emuBestRA("QuickNES (RetroArch)", "quicknes"),
		},
		Desktop: []emuBestCandidate{
			emuBestRA("Mesen (RetroArch)", "mesen"),
			emuBestRA("FCEUmm (RetroArch)", "fceumm"),
			emuBestRA("Nestopia (RetroArch)", "nestopia"),
			emuBestSA("FCEUX", "FCEUX", "fceux"),
		},
	},
	{System: "Game Boy", Handheld: emuBestGameBoy, Desktop: emuBestGameBoy},
	{System: "Game Boy Color", Handheld: emuBestGameBoy, Desktop: emuBestGameBoy},
	{
		System: "Mega Drive",
		Handheld: []emuBestCandidate{
			emuBestRA("Genesis Plus GX (RetroArch)", "genesis_plus_gx"),
			emuBestRA("PicoDrive (RetroArch)", "picodrive"),
		},
		Desktop: []emuBestCandidate{
			emuBestRA("Genesis Plus GX (RetroArch)", "genesis_plus_gx"),
			emuBestRA("PicoDrive (RetroArch)", "picodrive"),
		},
	},
	{System: "GameCube", Desktop: []emuBestCandidate{emuBestSA("Dolphin", "Dolphin", "dolphin")}},
	{System: "Wii", Desktop: []emuBestCandidate{emuBestSA("Dolphin", "Dolphin", "dolphin")}},
	{System: "PlayStation 2", Desktop: []emuBestCandidate{emuBestSA("PCSX2", "PCSX2", "pcsx2")}},
}

// emuBestCandidatesFor is system's ranking on this platform, best first; nil
// for a system the table does not rank.
func emuBestCandidatesFor(system string) []emuBestCandidate {
	system = strings.TrimSpace(system)
	for _, s := range emuBestTable {
		if !strings.EqualFold(s.System, system) {
			continue
		}
		if IsWindows() {
			return s.Desktop
		}
		return s.Handheld
	}
	return nil
}

// ── Recognising an entry ─────────────────────────────────────────────────

// emuBestScript is what one launch script says about the emulator it starts.
type emuBestScript struct {
	size      int64
	mod       time.Time
	retroArch bool
	cores     []string
	// tokens are the standalone tokens of the whole table the script spells
	// out ("drastic" in "./drastic \"$1\"").
	tokens map[string]bool
}

// emuBestScripts caches emuBestScript per launcher path.
var emuBestScripts struct {
	mu sync.Mutex
	m  map[string]emuBestScript
}

// emuBestScriptScan bounds how much of a launch script is read.
const emuBestScriptScan = 64 << 10

// emuBestReadScript reads (once per file version) what launcher's script
// says. A file that cannot be read says nothing.
func emuBestReadScript(launcher string) emuBestScript {
	info, err := os.Stat(launcher)
	if err != nil || !info.Mode().IsRegular() {
		return emuBestScript{}
	}
	emuBestScripts.mu.Lock()
	if s, ok := emuBestScripts.m[launcher]; ok && s.size == info.Size() && s.mod.Equal(info.ModTime()) {
		emuBestScripts.mu.Unlock()
		return s
	}
	emuBestScripts.mu.Unlock()
	s := emuBestScript{size: info.Size(), mod: info.ModTime(), tokens: map[string]bool{}}
	ext := strings.ToLower(filepath.Ext(launcher))
	if ext != ".exe" && ext != ".so" && ext != ".dll" {
		if f, err := os.Open(launcher); err == nil {
			head, _ := io.ReadAll(io.LimitReader(f, emuBestScriptScan))
			f.Close()
			text := strings.ToLower(string(head))
			s.cores = coreNamesIn(text)
			s.retroArch = len(s.cores) > 0 || retroArchLauncher(launcher)
			squashed := normaliseLauncherName(text)
			for _, sys := range emuBestTable {
				for _, list := range [][]emuBestCandidate{sys.Handheld, sys.Desktop} {
					for _, c := range list {
						for _, tok := range c.Tokens {
							if strings.Contains(squashed, tok) {
								s.tokens[tok] = true
							}
						}
					}
				}
			}
		}
	}
	emuBestScripts.mu.Lock()
	if emuBestScripts.m == nil {
		emuBestScripts.m = map[string]emuBestScript{}
	}
	emuBestScripts.m[launcher] = s
	emuBestScripts.mu.Unlock()
	return s
}

// emuBestCoreRank is the rank of the core candidate core is: an exact name
// first, then a candidate the core's name starts with (yabasanshiro_a133p).
func emuBestCoreRank(core string, cands []emuBestCandidate) int {
	core = normalizeCoreName(core)
	if core == "" {
		return -1
	}
	for i, c := range cands {
		if c.Kind == emuBestCore && c.Core == core {
			return i
		}
	}
	for i, c := range cands {
		if c.Kind == emuBestCore && c.Core != "" && strings.HasPrefix(core, c.Core) {
			return i
		}
	}
	return -1
}

// emuBestTokenRank is the rank of the first standalone candidate whose token
// name contains.
func emuBestTokenRank(name string, cands []emuBestCandidate) int {
	if name == "" {
		return -1
	}
	for i, c := range cands {
		if c.Kind != emuBestStandalone {
			continue
		}
		for _, tok := range c.Tokens {
			if tok != "" && strings.Contains(name, tok) {
				return i
			}
		}
	}
	return -1
}

// emuBestStandaloneToken reports whether word is one of the standalone tokens
// in cands: a core named like its standalone (ppsspp, flycast, mgba) cannot be
// told apart by a file name alone.
func emuBestStandaloneToken(word string, cands []emuBestCandidate) bool {
	for _, c := range cands {
		if c.Kind != emuBestStandalone {
			continue
		}
		for _, tok := range c.Tokens {
			if tok == word {
				return true
			}
		}
	}
	return false
}

// emuBestRankOf is the rank of the emulator catalog entry e starts for a
// system ranked cands, or -1 when it is not recognised. The signals, strongest
// first: a core row or core file; the launcher's own name (drastic.sh,
// ppsspp_1.17.1_gl.sh, mupen64plus_next.sh, PPSSPPWindows64.exe); the cores a
// RetroArch launch script names; the entry's name (an Emus folder, a Windows
// emulator); the standalone emulators a script names.
func emuBestRankOf(e emuEntry, cands []emuBestCandidate) int {
	if len(cands) == 0 {
		return -1
	}
	launch := strings.TrimSpace(e.Launch)
	ext := strings.ToLower(filepath.Ext(launch))
	core := normalizeCoreName(e.Core)
	if core == "" && (ext == ".so" || ext == ".dll") {
		core = normalizeCoreName(filepath.Base(launch))
	}
	if core != "" {
		return emuBestCoreRank(core, cands)
	}
	if launch == "" {
		return -1
	}
	base := normaliseLauncherName(strings.TrimSuffix(filepath.Base(launch), filepath.Ext(launch)))
	// A launcher named after a core that is not also a standalone's name
	// ("mupen64plus_next.sh") is that core; "ppsspp_1.17.1_gl.sh" is not.
	for i, c := range cands {
		if c.Kind != emuBestCore {
			continue
		}
		n := normaliseLauncherName(c.Core)
		if n != "" && !emuBestStandaloneToken(n, cands) && strings.Contains(base, n) {
			return i
		}
	}
	if r := emuBestTokenRank(base, cands); r >= 0 {
		return r
	}
	script := emuBestReadScript(launch)
	if script.retroArch {
		if len(script.cores) == 1 {
			return emuBestCoreRank(script.cores[0], cands)
		}
		return -1
	}
	if r := emuBestTokenRank(normaliseLauncherName(e.Name), cands); r >= 0 {
		return r
	}
	for i, c := range cands {
		if c.Kind != emuBestStandalone {
			continue
		}
		for _, tok := range c.Tokens {
			if script.tokens[tok] {
				return i
			}
		}
	}
	return -1
}

// ── Using the ranking ────────────────────────────────────────────────────

// emuBestPick is the index in list (emulators that play system, in the
// catalog's order) of the recommended one, or -1 when there is none to
// recommend. It is careful: an entry it cannot recognise may be the better
// emulator, so a recognised one is only put ahead of an unrecognised entry
// when it is the system's very best.
func emuBestPick(system string, list []emuEntry) int {
	cands := emuBestCandidatesFor(system)
	if len(cands) == 0 || len(list) == 0 {
		return -1
	}
	best, bestRank := -1, len(cands)
	ranks := make([]int, len(list))
	for i, e := range list {
		ranks[i] = emuBestRankOf(e, cands)
		if ranks[i] >= 0 && ranks[i] < bestRank {
			best, bestRank = i, ranks[i]
		}
	}
	if best < 0 {
		return -1
	}
	if bestRank > 0 {
		for j := 0; j < best; j++ {
			if ranks[j] < 0 {
				return -1
			}
		}
	}
	return best
}

// emuBestOrder is list with the recommended emulator moved to the front (the
// rest keep their order), and whether there was one.
func emuBestOrder(system string, list []emuEntry) ([]emuEntry, bool) {
	i := emuBestPick(system, list)
	if i < 0 {
		return list, false
	}
	if i == 0 {
		return list, true
	}
	out := make([]emuEntry, 0, len(list))
	out = append(out, list[i])
	out = append(out, list[:i]...)
	out = append(out, list[i+1:]...)
	return out, true
}

// emuBestInstalledRank is the rank of the best emulator for system among
// entries that can start its games, or len(cands) when none is recognised.
func emuBestInstalledRank(system string, entries []emuEntry, cands []emuBestCandidate) int {
	best := len(cands)
	for _, e := range findGameLaunchers(system, entries) {
		if r := emuBestRankOf(e, cands); r >= 0 && r < best {
			best = r
		}
	}
	return best
}

// emuBestInstallTile is the index of the catalog tile that installs c and
// makes it play the system's games, or -1. On a PC that is a Windows emulator
// the page can download, or a missing core tile (a core file there is its own
// launcher). Not on the handheld: there the firmware's Emus launch scripts
// pick the core, so installing a core would not change what a game starts in,
// and the standalone emulators come with the firmware (PSP's PPSSPP has its
// own card, ppsspp_managed_ui.go) — the page claims no upgrade it cannot
// deliver.
func emuBestInstallTile(c emuBestCandidate, entries []emuEntry) int {
	if !IsWindows() {
		return -1
	}
	for i, e := range entries {
		if e.Status != emuMissing || e.Pinned {
			continue
		}
		switch c.Kind {
		case emuBestCore:
			if emuCoreInstallSupported() && c.Core != "" && normalizeCoreName(e.Core) == c.Core && strings.TrimSpace(e.Launch) == "" {
				return i
			}
		case emuBestStandalone:
			if c.Win == "" || !strings.EqualFold(e.Name, c.Win) {
				continue
			}
			if spec, ok := emuWinSpecFor(c.Win); ok && spec.installable() {
				return i
			}
		}
	}
	return -1
}

// emuBestUpgrade is the best emulator for system that ranks above every one
// installed and has an install tile in entries: the candidate, the tile, and
// the rank of the best installed one (len(cands) for none).
func emuBestUpgrade(system string, entries []emuEntry) (cand emuBestCandidate, tile, installed int, ok bool) {
	cands := emuBestCandidatesFor(system)
	if len(cands) == 0 {
		return emuBestCandidate{}, -1, 0, false
	}
	installed = emuBestInstalledRank(system, entries, cands)
	for r := 0; r < installed && r < len(cands); r++ {
		if t := emuBestInstallTile(cands[r], entries); t >= 0 {
			return cands[r], t, installed, true
		}
	}
	return emuBestCandidate{}, -1, installed, false
}

// emuBestInstalledFor names the best emulator installed for a library system
// ("DraStic"), or ok=false when none of the installed ones is recognised. It
// builds the emulator catalog when it is not built yet (a card scan): call it
// off the render thread, or after the Emulators page or the library built it.
func emuBestInstalledFor(system string) (name string, ok bool) {
	cands := emuBestCandidatesFor(system)
	if len(cands) == 0 {
		return "", false
	}
	if r := emuBestInstalledRank(system, emulatorEntries(), cands); r < len(cands) {
		return cands[r].Name, true
	}
	return "", false
}

// emuBestMissingFor names a better emulator for a library system that this
// device can install from the Emulators page ("melonDS"), or ok=false: one
// that ranks above every installed emulator (or any, when none is installed)
// and has an install tile there. Same cost note as emuBestInstalledFor.
func emuBestMissingFor(system string) (name string, ok bool) {
	cand, _, _, found := emuBestUpgrade(system, emulatorEntries())
	if !found {
		return "", false
	}
	return cand.Name, true
}

// emuBestSystemsWithGames lists the ranked systems that have a game on the
// card or the PC: a Roms folder of theirs with at least one file in it.
// CrossMix ships every Roms folder empty, so the folder alone says nothing.
func emuBestSystemsWithGames() map[string]bool {
	out := map[string]bool{}
	for _, root := range romLibraryRoots() {
		dirs, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			sys, ok := romSystemAliases[strings.ToLower(d.Name())]
			if !ok || out[sys.Name] || emuBestCandidatesFor(sys.Name) == nil {
				continue
			}
			if emuBestFolderHasFile(filepath.Join(root, d.Name())) {
				out[sys.Name] = true
			}
		}
	}
	return out
}

// emuBestFolderHasFile reports whether dir holds a visible file among its
// first entries.
func emuBestFolderHasFile(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	list, _ := f.ReadDir(64)
	for _, e := range list {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			return true
		}
	}
	return false
}

// emuBestDecorate marks, in a freshly built catalog, the install tile of a
// better emulator for every system that has games and a weaker emulator
// installed: the tile says so and sorts ahead of the other missing ones
// (sortEmulatorEntries). Nothing is installed; the tile's own Install does
// that when the user asks. A system with no emulator at all already has its
// "no emulator found" tile.
func emuBestDecorate(entries []emuEntry) []emuEntry {
	if !IsWindows() {
		// No install tile can deliver an upgrade there (emuBestInstallTile).
		return entries
	}
	withGames := emuBestSystemsWithGames()
	for _, sys := range emuBestTable {
		if !withGames[sys.System] {
			continue
		}
		cands := emuBestCandidatesFor(sys.System)
		cand, tile, installed, ok := emuBestUpgrade(sys.System, entries)
		if !ok || installed >= len(cands) || entries[tile].Better != "" {
			continue
		}
		entries[tile].Better = sys.System
		entries[tile].Detail = "Better emulator available: " + cand.Name + " plays " + sys.System +
			" better than the " + cands[installed].Name + " you use now · Install downloads the official build"
	}
	return entries
}
