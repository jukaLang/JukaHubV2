package main

// extra_systems.go — five consoles the Games library did not know: Sega
// NAOMI and Sammy Atomiswave, the Nintendo DSi, OpenBOR (beat-'em-up engine
// games) and the Tandy Color Computer ("CoCo").
//
// A card set up by spruceOS or Batocera (they use the same folder
// names) had Roms/NAOMI, ATOMISWAVE, NDSI, OPENBOR and COCO folders the scan
// skipped as unknown, so their games never showed. Each console is now
// built in, the way WASM-4 joined (wasm4.go), from init() and without
// touching the shared tables' text:
//
//   - Library: the folder names (romSystemAliases) with the libretro database
//     name covers are fetched under — "Sega - Naomi", "Atomiswave" and
//     "Nintendo - Nintendo DSi" are libretro-thumbnails folders. OpenBOR has
//     no database, and no "Tandy - Color Computer" folder could be confirmed,
//     so those two get no collection: their covers come from the game's own
//     screenshots instead of a thumbnail server's 404s. Which files count is a
//     shelf rule per console (romshelf.go): its formats only, never the BIOS
//     zip a NAOMI folder keeps beside the games (naomi.zip is listed as a game
//     elsewhere because it is a .zip), nor spruceOS's DSi boot menu.
//   - Emulators: NAOMI and Atomiswave are Flycast's other two arcade boards, so
//     an installed Flycast core (the Dreamcast's) plays them, started directly
//     (emuchoices.go: emuCoreAlsoPlays); the DSi plays in melonDS DS, the one
//     libretro core with a DSi mode. "Get more cores" fetches either for a
//     console with games (emuMoreCoreSuggestions), and the Emulators page's
//     "no emulator yet" tile knows them (defaultCoreForSystem, compat.go). An
//     Emus/NAOMI, NDSI, OPENBOR or COCO folder a firmware ships is matched by
//     name like any other console's (systemLauncherAliases).
//   - DSi mode: melonDS DS starts a cartridge as a DS unless its Console mode
//     option says DSi (only DSiWare is switched by the core itself), so a DSi
//     game's launch sets melonds_console_mode = "dsi" for that one launch
//     (raScopeApply: put back afterwards), in the options file RetroArch
//     reads for it (raTuneCoreOptionFile). The value "ds" was seen in a
//     melonDS DS log; "dsi" is its pair (DSI-HW-1 checks it).
//   - BIOS: NAOMI's naomi.zip and Atomiswave's awbios.zip were in the BIOS
//     table already (gamediag_bios.go); the DSi's four files join it here, so
//     Game Ready names what is missing before the launch and BIOS Doctor lists
//     them. They come from the player's own DSi and nothing here fetches them.
//   - OpenBOR and CoCo have no libretro core JukaHub can install (OpenBOR is
//     its own engine; XRoar, the CoCo emulator, has no libretro build that
//     could be confirmed, and MAME's CoCo driver is far too heavy for a ROM
//     list this small). Their games are listed, and a start with nothing to
//     play them says plainly what to add (extraSystemLauncherHint, through
//     emuLibraryLauncherHint) instead of "no launcher".

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	extraNaomiSystem      = "NAOMI"
	extraAtomiswaveSystem = "Atomiswave"
	extraDSiSystem        = "Nintendo DSi"
	extraOpenBORSystem    = "OpenBOR"
	extraCoCoSystem       = "Tandy CoCo"
)

const (
	// extraFlycastCore plays NAOMI and Atomiswave (and the Dreamcast).
	extraFlycastCore = "flycast"
	// extraDSiCore is melonDS DS's core name, and extraDSiCoreLabel its
	// library name: RetroArch's folder for its options, and the folder in the
	// BIOS folder it reads its files from first.
	extraDSiCore      = "melondsds"
	extraDSiCoreLabel = "melonDS DS"
	// extraDSiModeKey is melonDS DS's Console mode option.
	extraDSiModeKey   = "melonds_console_mode"
	extraDSiModeValue = "dsi"
)

// extraSystem is one of the five consoles.
type extraSystem struct {
	Name string
	// Collection is the libretro database name covers are filed under, ""
	// when there is none.
	Collection string
	// Folders are the Roms folder names it is found under, lower case.
	Folders []string
	// Launchers are the emulator folder and core names that play it.
	Launchers []string
	// Exts are its game files' extensions, lower case.
	Exts []string
	// Skip are file names in its folder that are not games, lower case.
	Skip []string
}

// extraSystems are the five consoles. The folder names are the ones spruceOS
// and Batocera cards use (NAOMI, ATOMISWAVE, NDSI, OPENBOR, COCO) and the
// spellings people give them.
var extraSystems = []extraSystem{
	{
		Name:       extraNaomiSystem,
		Collection: "Sega - Naomi",
		Folders:    []string{"naomi", "sega naomi", "sega - naomi"},
		Launchers:  []string{"naomi"},
		// ROM sets (.zip, .7z) and Flycast's loose-cartridge lists; a GD-ROM
		// game's .chd sits in a folder beside its zip and is not a game.
		Exts: []string{".zip", ".7z", ".lst", ".bin"},
		Skip: []string{"naomi.zip", "naomi2.zip", "awbios.zip"},
	},
	{
		Name:       extraAtomiswaveSystem,
		Collection: "Atomiswave",
		Folders:    []string{"atomiswave", "sammy atomiswave", "sammy - atomiswave"},
		Launchers:  []string{"atomiswave"},
		Exts:       []string{".zip", ".7z", ".lst", ".bin"},
		Skip:       []string{"awbios.zip", "naomi.zip"},
	},
	{
		Name:       extraDSiSystem,
		Collection: "Nintendo - Nintendo DSi",
		Folders:    []string{"ndsi", "dsi", "nintendo dsi", "nintendo - nintendo dsi"},
		Launchers:  []string{"ndsi", "dsi"},
		Exts:       []string{".nds", ".dsi", ".ids", ".zip", ".7z"},
		// spruceOS keeps a boot menu cart in its DS folders for its launcher.
		Skip: []string{"bootmenudsi.nds", "bootmenu.nds"},
	},
	{
		Name:      extraOpenBORSystem,
		Folders:   []string{"openbor"},
		Launchers: []string{"openbor"},
		Exts:      []string{".pak"},
	},
	{
		Name:      extraCoCoSystem,
		Folders:   []string{"coco", "coco3", "trs80coco", "trs-80 coco", "tandy coco", "color computer", "tandy color computer"},
		Launchers: []string{"coco", "xroar"},
		// Cartridges, cassettes and disk images as XRoar opens them.
		Exts: []string{".ccc", ".rom", ".cas", ".dsk", ".vdk", ".dmk", ".jvc", ".os9", ".bin"},
		// The machine's own ROMs, which people keep beside the games (the
		// launcher hint names the first three): not games.
		Skip: []string{"bas13.rom", "extbas11.rom", "disk11.rom", "bas12.rom", "extbas10.rom", "disk10.rom", "coco3.rom", "coco3p.rom"},
	},
}

// extraCorePlays is a core and the consoles it plays besides its own.
type extraCorePlays struct {
	core    string
	systems []string
}

// extraCoreAlsoPlays joins emuCoreAlsoPlays: Flycast's arcade boards, and
// melonDS DS's DSi mode. (The older melonDS core's DSi mode is not offered:
// melonDS DS replaced it.)
var extraCoreAlsoPlays = []extraCorePlays{
	{extraFlycastCore, []string{extraNaomiSystem, extraAtomiswaveSystem}},
	{extraDSiCore, []string{extraDSiSystem}},
}

// extraDSiBIOS is the DSi's row of the BIOS table (gamediag_bios.go). Only
// melonDS DS is told about it: an Emus/NDSI script's own emulator has its own
// files.
var extraDSiBIOS = gameDiagBIOS{
	System:   extraDSiSystem,
	Keys:     []string{"nintendo dsi", "ndsi", "dsi", "nintendo - nintendo dsi"},
	Required: []string{extraDSiCore},
	Groups:   [][]string{{"dsi_bios7.bin"}, {"dsi_bios9.bin"}, {"dsi_firmware.bin"}, {"dsi_nand.bin", "nand.bin"}},
	Note:     "Dumped from your own DSi; No-Intro calls the BIOS files bios7i.bin and bios9i.bin (rename them). melonDS DS also reads a \"melonDS DS\" folder inside this one, and a firmware or NAND under another name is picked in RetroArch → Core options → System.",
}

func init() {
	for _, s := range extraSystems {
		sys := romSystem{Name: s.Name, Collection: s.Collection}
		for _, folder := range s.Folders {
			// Another feature that named a folder first keeps it.
			if _, taken := romSystemAliases[folder]; !taken {
				romSystemAliases[folder] = sys
			}
			if _, known := compatSystemCanonicalNames[folder]; !known {
				compatSystemCanonicalNames[folder] = s.Name
			}
		}
		if _, taken := systemLauncherAliases[s.Name]; !taken {
			systemLauncherAliases[s.Name] = s.Launchers
		}
		romShelfRegister(s.Name, extraShelfRule(s))
	}
	for _, row := range extraCoreAlsoPlays {
		emuCoreAlsoPlays = append(emuCoreAlsoPlays, struct {
			prefix  string
			systems []string
		}{row.core, row.systems})
	}
	for system, cores := range map[string][]string{
		extraNaomiSystem:      {extraFlycastCore},
		extraAtomiswaveSystem: {extraFlycastCore},
		extraDSiSystem:        {extraDSiCore},
	} {
		if _, taken := emuMoreCoreSuggestions[system]; !taken {
			emuMoreCoreSuggestions[system] = cores
		}
	}
	// Flycast works the A133P as hard on these boards as on the Dreamcast.
	for _, system := range []string{extraNaomiSystem, extraAtomiswaveSystem} {
		if _, listed := raTuneSystemFor(system); !listed {
			raTuneSystems = append(raTuneSystems, raTuneSystem{Name: system, Tier: raTuneHeavy})
		}
	}
	gameDiagBIOSTable = append(gameDiagBIOSTable, extraDSiBIOS)
	gameLaunchRegisterPrepare(extraDSiPrepareLaunch)
}

// extraShelfRule lists a console's game files: its formats, less the files
// that are not games.
func extraShelfRule(s extraSystem) romShelfRule {
	exts := make(map[string]bool, len(s.Exts))
	for _, e := range s.Exts {
		exts[e] = true
	}
	skip := make(map[string]bool, len(s.Skip))
	for _, name := range s.Skip {
		skip[name] = true
	}
	return func(_, path, ext string) bool {
		return exts[ext] && !skip[strings.ToLower(filepath.Base(path))]
	}
}

// ── DSi mode ─────────────────────────────────────────────────────────────

// extraIsDSiGame reports a DSi game: by the library's console, or by the
// folder it sits in (a start from the recents of an older scan). No disk.
func extraIsDSiGame(game libraryGame) bool {
	return game.System == extraDSiSystem || lookSystemForROM(game.Path) == extraDSiSystem
}

// extraDSiPrepareLaunch switches melonDS DS to DSi mode for a DSi game's
// launch (a launch prepare hook, gamelaunch_hooks.go). Another core, or a
// standalone emulator from an Emus/NDSI folder, is left alone. It never
// blocks or fails a launch.
func extraDSiPrepareLaunch(launcher string, game libraryGame) {
	if !extraIsDSiGame(game) || !retroArchLauncher(launcher) {
		return
	}
	core := gameCoreFromLauncher(launcher, game.System)
	if !strings.HasPrefix(core, extraDSiCore) {
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, raTuneCandidates()))
	if len(targets) == 0 {
		logInfo("library", "DSi: no RetroArch config found; melonDS DS starts in its own console mode")
		return
	}
	opt := extraDSiOptionsFile(targets[0], core, game.Path)
	if opt == "" {
		logInfo("library", "DSi: no melonDS DS options file to set DSi mode in; DSiWare still starts in DSi mode by itself")
		return
	}
	if _, err := raScopeApply(opt, []raScopeValue{{Key: extraDSiModeKey, Value: extraDSiModeValue}}); err != nil {
		logWarn("library", "DSi: cannot set DSi mode in %s: %v", opt, err)
	}
}

// extraDSiOptionsFile is the options file RetroArch reads for melonDS DS on
// this game (raTuneCoreOptionFile). A core that never ran with per-core
// options has none yet: it gets an empty one, which RetroArch reads like its
// own (every option it does not name keeps the core's default, as on a first
// run), the way a first directly started core is seeded (emuchoices.go). With
// one shared options file for every core and none written yet, "" — nothing
// is created there.
func extraDSiOptionsFile(cfgPath, core, romPath string) string {
	if p := raTuneCoreOptionFile(cfgPath, core, romPath); p != "" {
		return p
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	cfg := string(raw)
	if global, has := raConfigGet(cfg, "global_core_options"); !has || raValueEqual(global, "true") {
		return ""
	}
	dir := filepath.Join(raTuneMenuConfigDir(cfg, cfgPath), extraDSiCoreLabel)
	path := filepath.Join(dir, extraDSiCoreLabel+".opt")
	if regularFile(path) {
		return path
	}
	if err := AtomicWrite(path, nil, 0o644); err != nil {
		logWarn("library", "DSi: cannot create %s: %v", path, err)
		return ""
	}
	return path
}

// ── What a console with no emulator needs ────────────────────────────────

// extraSystemLauncherHint is what a game of these consoles needs when
// nothing on the device plays it — the answer emuLibraryLauncherHint gives
// the launch, Game Ready's card and Home's console tile. "" for any other
// console. Cheap (a few stats): it runs on the main thread, on a press.
func extraSystemLauncherHint(system string) string {
	handheld := emuMoreCoresSupported()
	switch strings.TrimSpace(system) {
	case extraNaomiSystem, extraAtomiswaveSystem:
		bios := "naomi.zip"
		if strings.TrimSpace(system) == extraAtomiswaveSystem {
			bios = "awbios.zip"
		}
		if handheld {
			return system + " games play in RetroArch's Flycast core: Settings → Games & emulators → Get more cores fetches it. Put " + bios + " (as it is, not unzipped) in the BIOS folder's dc folder"
		}
		return system + " games play in RetroArch's Flycast core, with " + bios + " (not unzipped) in RetroArch's system/dc folder"
	case extraDSiSystem:
		files := "dsi_bios7.bin, dsi_bios9.bin, dsi_firmware.bin and dsi_nand.bin from your own DSi"
		if handheld {
			return "DSi games play in RetroArch's melonDS DS core in DSi mode: Settings → Games & emulators → Get more cores fetches it. It needs " + files + " in the BIOS folder"
		}
		return "DSi games play in RetroArch's melonDS DS core in DSi mode, with " + files + " in RetroArch's system folder"
	case extraOpenBORSystem:
		if IsWindows() {
			return "OpenBOR games need the OpenBOR engine (chronocrash.com): copy the .pak into its Paks folder and start OpenBOR"
		}
		if dir := extraOpenBORPortDir(); dir != "" {
			return "OpenBOR is installed as a port (" + dir + "): copy the .pak into its paks folder and start OpenBOR from Ports, or add an OpenBOR emulator folder (Emus/OPENBOR) to start games from here"
		}
		return "OpenBOR games need the OpenBOR engine, which has no RetroArch core: add an OpenBOR emulator folder (Emus/OPENBOR, a launch.sh that takes the .pak), or install OpenBOR from PortMaster and copy the .pak into its paks folder"
	case extraCoCoSystem:
		if IsWindows() {
			return "Tandy CoCo games play in XRoar (6809.org.uk/xroar), with the CoCo ROMs (bas13.rom, extbas11.rom, disk11.rom) beside it"
		}
		return "Tandy CoCo games play in XRoar, which has no RetroArch core: add an XRoar emulator folder (Emus/COCO, a launch.sh that takes the game) with the CoCo ROMs (bas13.rom, extbas11.rom, disk11.rom)"
	}
	return ""
}

// extraOpenBORPortDir is PortMaster's OpenBOR on the card: its folder in one
// of the Ports folders, "" when there is none. A FAT card ignores case.
func extraOpenBORPortDir() string {
	for _, root := range portRoots(portMasterSDRoot) {
		if dir := filepath.Join(root, "openbor"); dirExists(dir) {
			return dir
		}
	}
	return ""
}
