package main

// sessionsafe_recipes.go — what "Never lose a session" (sessionsafe.go) knows
// about each standalone emulator JukaHub starts: how to recognise it among the
// game's processes, whether it writes a save state as it quits and where that
// state lands, which signal asks it to quit, and whether it loads the state by
// itself at its next start. Everything here is data and text parsing; nothing
// in this file touches a process.
//
// None of the emulators offers a command interface like RetroArch's network
// commands, so the only save JukaHub can ask for is the one an emulator makes
// on its own way out: its "save state on exit" option, when the user (or the
// firmware's package) turned it on. JukaHub never turns those options on
// itself: an emulator's config belongs to the emulator, and a state written at
// every exit changes how the game behaves at every start.
//
// Every SDL program turns SIGINT and SIGTERM into SDL_QUIT unless it opted out
// (SDL_HINT_NO_SIGNAL_HANDLERS), so a polite signal is the same as closing the
// window: the emulator's own quit path runs and flushes the game's battery
// saves and memory cards. SIGKILL is never sent here; whatever is still alive
// after the bounded wait is the caller's (the suspend keeps it in RAM, the
// power off's teardown ends it).
//
// Researched 2026-11-19, none of it run on a handheld yet (backlog SAFE-HW-1):
//   - PPSSPP: upstream has no save-on-exit (hrydgard/ppsspp#8661); the
//     NextUI/MinUI builds of the Stapta-nl fork add SaveStateOnExit (ppsspp.ini
//     [General]) and run it on SIGTERM with a 5 s limit, beside
//     AutoLoadSaveState (0 off, 2 newest, 3+ a slot) for the resume. States are
//     PSP/PPSSPP_STATE/*.ppst. JukaHub's own build (2026-11-24,
//     player/native/build-ppsspp.sh) takes a save request instead (SIGUSR1,
//     sessionSafeRequestSave), known by the flag file beside its program
//     rather than by anything here.
//   - DraStic: enable_autosavestate = 1 in config/drastic.cfg writes a state
//     on a proper exit (trngaje/advanced_drastic#23); a hotkey quit or a power
//     cut does not (muOS forum, steward-fu/nds#63). Whether SIGTERM counts as a
//     proper exit is not documented, so the state file decides. States are
//     savestates/*.dss.
//   - Flycast: Dreamcast.AutoSaveState and Dreamcast.AutoLoadState in emu.cfg
//     (flycast core/cfg/option.cpp); states are <rom>.state / <rom>_N.state.
//   - Mednafen: "autosave 1" saves on close and loads on start (Mednafen docs);
//     SIGTERM and SIGINT are non-fatal signals that end it normally
//     (drivers/main.cpp). States are mcs/*.mca.
//   - DuckStation: [Main] SaveStateOnExit, on by default; states are
//     savestates/*.sav. Its resume needs -resume, which a firmware launch
//     script does not pass, so the state is loaded from its own menu.
//   - Mupen64Plus: --savestate loads a state at start, but there is no save on
//     exit and its F5 hotkey is a keyboard key; a graceful quit still writes
//     SRAM, EEPROM, flash and the Controller Pak as the core stops.
//   - The rest (Yaba Sanshiro, ScummVM, DOSBox, OpenBOR, PICO-8, fake-08,
//     Godot, LÖVE and GameMaker ports, MinArch, hakuX/xemu, Wine, Java,
//     ClassiCube, Luanti) have no save state JukaHub can trigger; each gets
//     the polite quit, which is where their own saves are flushed.
//   - Moonlight, Chiaki and the Xbox clients only stream: the game runs on the
//     other machine and nothing is lost on the handheld.
//
// A hotkey injected through a uinput keyboard (the way the games mouse makes
// its device, vcursor_bridge_linux.go) was considered and left out: SDL's
// evdev input only sees keyboards that existed when the emulator started (and
// none at all without udev), and the TrimUI emulators read the pad, not keys.

import (
	"strconv"
	"strings"
	"time"
)

// sessionSafeClass is what can be done for a running game.
type sessionSafeClass int

const (
	// sessionSafeNone: no game of JukaHub's is running.
	sessionSafeNone sessionSafeClass = iota
	// sessionSafeRetroArch: RetroArch, saved through its network commands
	// (lowbattery.go); this file leaves it to that path.
	sessionSafeRetroArch
	// sessionSafeOnQuit: writes a save state as it quits when its own
	// option is on.
	sessionSafeOnQuit
	// sessionSafeQuitOnly: no save state; the polite quit flushes the game's
	// own saves.
	sessionSafeQuitOnly
	// sessionSafeStream: a stream from another machine; nothing to save.
	sessionSafeStream
)

// sessionSafeSignal is the polite signal an emulator gets.
type sessionSafeSignal int

const (
	sessionSafeSigTerm sessionSafeSignal = iota
	sessionSafeSigInt
)

const (
	// sessionSafeDefaultWait is how long an emulator gets to save and quit.
	sessionSafeDefaultWait = 6 * time.Second
	// sessionSafeSaveWait is the same for one that writes a state on the way
	// out (the PPSSPP fork's own limit is 5 s).
	sessionSafeSaveWait = 9 * time.Second
	// sessionSafeConfigLimit bounds a config file read.
	sessionSafeConfigLimit = 512 << 10
	// sessionSafeScriptGrace is how long a launch script gets for its own
	// lines after the game (an unmount, a governor put back) once the
	// emulator has quit, before its group is asked to stop too.
	sessionSafeScriptGrace = 2 * time.Second
)

// sessionSafeShells are the interpreters a launch script runs in: never the
// emulator, even when the script is named after it (PPSSPP.sh run through its
// "#!" line has that name as its process name).
var sessionSafeShells = map[string]bool{"sh": true, "bash": true, "ash": true, "dash": true, "busybox": true}

// sessionSafeGameDirs are folders only games and emulators start from: a
// screen child whose launcher, arguments or working folder is in one was
// started as a game. Lower case, slash-separated.
var sessionSafeGameDirs = []string{"/roms/", "/emus/", "/ports/", "/portmaster/"}

// sessionSafeGamePath reports a path inside one of the game folders. Pure.
func sessionSafeGamePath(p string) bool {
	if p == "" {
		return false
	}
	lower := strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
	for _, dir := range sessionSafeGameDirs {
		if strings.Contains(lower, dir) {
			return true
		}
	}
	return false
}

// sessionSafeRecipe is one emulator.
type sessionSafeRecipe struct {
	ID    string
	Label string
	// Names match the process name, the program's file name or argv[0]
	// (closeAllRuleMatch: '=' whole, '^' prefix, '$' suffix), lower case.
	Names  []closeAllRule
	Class  sessionSafeClass
	Signal sessionSafeSignal
	Wait   time.Duration
	// Configs and StateDirs are relative to the emulator's HOME, its working
	// folder and its program's folder, tried in that order.
	Configs   []string
	StateDirs []string
	StateExt  []string
	// Flags reads the emulator's config text ("" when none was found): does
	// it save a state as it quits, and load it again at the next start.
	Flags func(cfg string) (saveOnQuit, autoLoad bool)
	// Option names the emulator's own switch for the user.
	Option string
	// WholeGroup sends the signal to the game's whole process group, the way
	// the exit path does, instead of to the emulator's own processes first
	// (Wine: the game is one of several wine processes).
	WholeGroup bool
}

// sessionSafeRecipes is the table, best match first within a class.
var sessionSafeRecipes = []sessionSafeRecipe{
	{
		ID: "ppsspp", Label: "PPSSPP", Class: sessionSafeOnQuit, Signal: sessionSafeSigTerm, Wait: sessionSafeSaveWait,
		Names: []closeAllRule{{"ppsspp", '^', ""}},
		Configs: []string{".config/ppsspp/PSP/SYSTEM/ppsspp.ini", "PSP/SYSTEM/ppsspp.ini",
			"memstick/PSP/SYSTEM/ppsspp.ini", ".ppsspp/PSP/SYSTEM/ppsspp.ini"},
		StateDirs: []string{".config/ppsspp/PSP/PPSSPP_STATE", "PSP/PPSSPP_STATE",
			"memstick/PSP/PPSSPP_STATE", ".ppsspp/PSP/PPSSPP_STATE"},
		StateExt: []string{".ppst"},
		Flags:    sessionSafePPSSPPFlags,
		Option:   "Save state on exit",
	},
	{
		ID: "drastic", Label: "DraStic", Class: sessionSafeOnQuit, Signal: sessionSafeSigTerm, Wait: sessionSafeSaveWait,
		Names:     []closeAllRule{{"drastic", '^', ""}},
		Configs:   []string{"config/drastic.cfg", "drastic.cfg"},
		StateDirs: []string{"savestates"},
		StateExt:  []string{".dss"},
		Flags:     sessionSafeDraSticFlags,
		Option:    "auto save state",
	},
	{
		ID: "flycast", Label: "Flycast", Class: sessionSafeOnQuit, Signal: sessionSafeSigTerm, Wait: sessionSafeSaveWait,
		Names: []closeAllRule{{"flycast", '^', ""}},
		Configs: []string{".config/flycast/emu.cfg", "emu.cfg", ".flycast/emu.cfg", "flycast/emu.cfg",
			".local/share/flycast/emu.cfg"},
		StateDirs: []string{".local/share/flycast", "data", ".", ".flycast", ".config/flycast"},
		StateExt:  []string{".state"},
		Flags:     sessionSafeFlycastFlags,
		Option:    "Auto save state",
	},
	{
		ID: "mednafen", Label: "Mednafen", Class: sessionSafeOnQuit, Signal: sessionSafeSigTerm, Wait: sessionSafeSaveWait,
		Names:     []closeAllRule{{"mednafen", '=', ""}},
		Configs:   []string{".mednafen/mednafen.cfg", "mednafen.cfg"},
		StateDirs: []string{".mednafen/mcs", "mcs"},
		StateExt:  []string{".mca"},
		Flags:     sessionSafeMednafenFlags,
		Option:    "autosave",
	},
	{
		ID: "duckstation", Label: "DuckStation", Class: sessionSafeOnQuit, Signal: sessionSafeSigTerm, Wait: sessionSafeSaveWait,
		Names: []closeAllRule{{"duckstation", '^', ""}},
		Configs: []string{".local/share/duckstation/settings.ini", "settings.ini",
			".config/duckstation/settings.ini"},
		StateDirs: []string{".local/share/duckstation/savestates", "savestates", ".config/duckstation/savestates"},
		StateExt:  []string{".sav"},
		Flags:     sessionSafeDuckStationFlags,
		Option:    "Save state on exit",
	},
	{ID: "mupen64plus", Label: "Mupen64Plus", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"mupen64plus", '^', ""}}},
	{ID: "yabasanshiro", Label: "Yaba Sanshiro", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"yabasanshiro", '^', ""}}},
	{ID: "scummvm", Label: "ScummVM", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"scummvm", '=', ""}}},
	{ID: "dosbox", Label: "DOSBox", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"dosbox", '^', ""}}},
	{ID: "openbor", Label: "OpenBOR", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"openbor", '^', ""}}},
	{ID: "pico8", Label: "PICO-8", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"pico8", '^', ""}, {"fake-08", '^', ""}}},
	{ID: "godot", Label: "Godot", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"frt", '^', ""}, {"godot", '^', ""}}},
	{ID: "port", Label: "This port", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"love", '=', ""}, {"gmloader", '^', ""}}},
	{ID: "minarch", Label: "MinArch", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"minarch", '^', ""}}},
	{ID: "xbox", Label: "hakuX", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"hakux", '^', ""}, {"xemu", '^', ""}}},
	{ID: "java", Label: "Java", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"java", '=', ""}}},
	{ID: "classicube", Label: "ClassiCube", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"classicube", '^', ""}}},
	{ID: "luanti", Label: "Luanti", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"luanti", '^', ""}, {"minetest", '^', ""}}},
	// Wine gets the interrupt the exit path sends (gamesuspend.go).
	{ID: "wine", Label: "Wine", Class: sessionSafeQuitOnly, Signal: sessionSafeSigInt, WholeGroup: true,
		Names: []closeAllRule{{"wine", '^', ""}, {"box64", '=', ""}, {"box86", '=', ""}, {".exe", '$', ""}}},
	{ID: "moonlight", Label: "Moonlight", Class: sessionSafeStream, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"moonlight", '^', ""}}},
	{ID: "remoteplay", Label: "Remote Play", Class: sessionSafeStream, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"chiaki", '^', ""}}},
	{ID: "xboxstream", Label: "Xbox Remote Play", Class: sessionSafeStream, Signal: sessionSafeSigTerm,
		Names: []closeAllRule{{"greenlight", '^', ""}, {"greenovercast", '^', ""}}},
}

// sessionSafeUnknown is a game no row names: the polite interrupt the exit
// path sends, no save.
var sessionSafeUnknown = sessionSafeRecipe{
	ID: "unknown", Label: "This game", Class: sessionSafeQuitOnly, Signal: sessionSafeSigInt,
}

// sessionSafeAndroid is an Android game in AndroidRT. Its runner is called
// "loader", too plain a name for the table, so it is known by its folder
// (sessionSafeProcRecipe). No save state: held at the battery, asked to quit
// at a power off (instantresume.go).
var sessionSafeAndroid = sessionSafeRecipe{
	ID: "android", Label: "This Android game", Class: sessionSafeQuitOnly, Signal: sessionSafeSigTerm,
}

// sessionSafeProcRecipe finds the row for one process: AndroidRT's runner by
// the folder its program runs from, anything else by its names
// (sessionSafeRecipeFor). Pure.
func sessionSafeProcRecipe(p sessionSafeProc) (*sessionSafeRecipe, bool) {
	argv0 := ""
	if len(p.argv) > 0 {
		argv0 = p.argv[0]
	}
	for _, s := range []string{p.exe, argv0} {
		if strings.Contains(strings.ToLower(strings.ReplaceAll(s, `\`, "/")), "/androidrt/") {
			return &sessionSafeAndroid, true
		}
	}
	return sessionSafeRecipeFor(p.comm, argv0, p.exe)
}

// sessionSafeRecipeFor finds the row for one process by its names (the
// kernel's process name, argv[0] and the program's path). Pure.
func sessionSafeRecipeFor(names ...string) (*sessionSafeRecipe, bool) {
	for i := range sessionSafeRecipes {
		r := &sessionSafeRecipes[i]
		for _, n := range names {
			lower := strings.ToLower(closeAllBase(n))
			if lower == "" {
				continue
			}
			for _, rule := range r.Names {
				if closeAllRuleMatch(rule, lower) {
					return r, true
				}
			}
		}
	}
	return nil, false
}

// sessionSafeClassRank orders classes when a game's group holds several
// known programs (a port's LÖVE beside its pad helper, Wine's server beside
// the game): the one that can save wins.
func sessionSafeClassRank(c sessionSafeClass) int {
	switch c {
	case sessionSafeRetroArch:
		return 4
	case sessionSafeOnQuit:
		return 3
	case sessionSafeQuitOnly:
		return 2
	case sessionSafeStream:
		return 1
	}
	return 0
}

// ── Config text (pure) ──────────────────────────────────────────────────

// sessionSafeConfigValue is the last value of key in an emulator config:
// "key = value" (ini, cfg) or "key value" (Mednafen), with quotes and
// trailing comments removed. Sections are ignored: none of the keys read here
// appears in two of them. Pure.
func sessionSafeConfigValue(cfg, key string) (string, bool) {
	value, found := "", false
	for _, line := range strings.Split(cfg, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || line[0] == '#' || line[0] == ';' || line[0] == '[' {
			continue
		}
		var k, v string
		if eq := strings.IndexByte(line, '='); eq >= 0 {
			k, v = line[:eq], line[eq+1:]
		} else if sp := strings.IndexAny(line, " \t"); sp >= 0 {
			k, v = line[:sp], line[sp+1:]
		} else {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(k), key) {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = v[:i]
		}
		value, found = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`)), true
	}
	return value, found
}

// sessionSafeTruthy reads the yes-words the emulators write.
func sessionSafeTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// sessionSafePPSSPPFlags: SaveStateOnExit (the fork's), AutoLoadSaveState not 0.
func sessionSafePPSSPPFlags(cfg string) (bool, bool) {
	v, _ := sessionSafeConfigValue(cfg, "SaveStateOnExit")
	auto, ok := sessionSafeConfigValue(cfg, "AutoLoadSaveState")
	n, err := strconv.Atoi(auto)
	return sessionSafeTruthy(v), ok && err == nil && n != 0
}

// sessionSafeDraSticFlags: enable_autosavestate. Whether DraStic loads that
// state by itself at the next start is not documented, so it is not claimed.
func sessionSafeDraSticFlags(cfg string) (bool, bool) {
	v, _ := sessionSafeConfigValue(cfg, "enable_autosavestate")
	return sessionSafeTruthy(v), false
}

// sessionSafeFlycastFlags: Dreamcast.AutoSaveState and Dreamcast.AutoLoadState.
func sessionSafeFlycastFlags(cfg string) (bool, bool) {
	save, _ := sessionSafeConfigValue(cfg, "Dreamcast.AutoSaveState")
	load, _ := sessionSafeConfigValue(cfg, "Dreamcast.AutoLoadState")
	return sessionSafeTruthy(save), sessionSafeTruthy(load)
}

// sessionSafeMednafenFlags: "autosave 1" saves on close and loads on start.
func sessionSafeMednafenFlags(cfg string) (bool, bool) {
	v, _ := sessionSafeConfigValue(cfg, "autosave")
	on := sessionSafeTruthy(v)
	return on, on
}

// sessionSafeDuckStationFlags: SaveStateOnExit, on unless the config says
// otherwise (DuckStation's own default). Loading it needs -resume.
func sessionSafeDuckStationFlags(cfg string) (bool, bool) {
	v, ok := sessionSafeConfigValue(cfg, "SaveStateOnExit")
	return !ok || sessionSafeTruthy(v), false
}
