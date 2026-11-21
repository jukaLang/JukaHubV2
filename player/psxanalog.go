package main

// psxanalog.go — the PlayStation pad a game gets: a DualShock for the games
// that read the analog sticks, the standard pad for the rest.
//
// A PS1 game known to support the analog sticks gets a DualShock in port 1.
// Not every game: a DualShock for all of them breaks some digital-only games,
// which is why it works from a list. Nothing is written for good, and the
// choice is made at each library launch.
//
// The choice. Settings → Controls → Game buttons for → PlayStation has
// "Analog controller": Auto (the default: a DualShock for the games in the
// list below, on a handheld with sticks), Always, or Never. A game can choose
// its own in its options sheet: Auto, DualShock or Standard pad. On a model
// without sticks (the Brick and the Hammer, stickHardware) Auto is Never —
// there is nothing to read; Always and a game's own DualShock still apply,
// for a Bluetooth pad.
//
// The launch (psxAnalogPrepare, a launch hook from gamelaunch_hooks.go, run
// after the buttons and turbo of racontrols.go) sets, for that start only and
// through raScopeApply (racfgscope.go), RetroArch's device type for port 1 —
// input_libretro_device_p1, the key the light gun uses too (lightgun.go) — and
// input_player1_analog_dpad_mode 0, so a "left stick as D-pad" choice does not
// eat the stick the game now reads. Everything goes back when RetroArch exits.
// No second remap file is written: the one-launch remap of the Buttons choice
// (racontrols_remap.go) carries no device line, so the config's applies; a
// remap the user saved in RetroArch (CrossMix's detector writes one per game)
// names its own device and wins, which the log says.
//
// The device ids are RETRO_DEVICE_SUBCLASS(base, id) = ((id+1) << 8) | base,
// with ANALOG = 5 and JOYPAD = 1, and differ by core:
//
//	PCSX-ReARMed  DualShock 517  (RETRO_DEVICE_PSE_DUALSHOCK = SUBCLASS(ANALOG, 1))
//	Beetle PSX    DualShock 517  (RETRO_DEVICE_PS_DUALSHOCK = SUBCLASS(ANALOG, 1))
//	SwanStation   DualShock 261  (SUBCLASS(ANALOG, 0); its 517 is the flight-stick
//	              "Analog Joystick")
//	every core    standard pad 1 (RETRO_DEVICE_JOYPAD)
//
// PCSX-ReARMed starts its DualShock in digital mode and switches to analog by
// itself when the game asks; a game that supports the sticks but never asks
// (Crash Bandicoot 2) wants the core's analog combo, L1+R1+Select by default
// (pcsx_rearmed_analog_combo). SwanStation's DualShock starts in analog mode
// ("Force analog on reset", on by default). Another core, or a standalone
// emulator, is left alone.
//
// The list is titles, matched after normalizeGameTitle (compat.go: no
// extension, no region or revision tags, lower case), against the ROM's file
// name and the library's title. It is kept short on purpose: a game is in it
// only when its analog support is well known. A disc's serial would match
// regions exactly, but reading it means opening every CHD; titles are enough.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	// psxAnalogConsole is the console the feature is for (raTuneSystems name).
	psxAnalogConsole = "PlayStation"

	// The console setting (gameControls.playstation.analog).
	psxAnalogAuto   = "auto"
	psxAnalogAlways = "always"
	psxAnalogNever  = "never"

	// A game's own choice (data/game-emu-extras.json "analog").
	psxAnalogGameDualShock = "dualshock"
	psxAnalogGameStandard  = "standard"

	// What a launch plugs into port 1; "" leaves RetroArch's own device.
	psxAnalogPadDualShock = "dualshock"
	psxAnalogPadStandard  = "standard"
)

var (
	psxAnalogModes      = []string{psxAnalogAuto, psxAnalogAlways, psxAnalogNever}
	psxAnalogGameLadder = []string{"", psxAnalogGameDualShock, psxAnalogGameStandard}
)

// psxAnalogTitles are PlayStation games known to read the analog sticks
// (DualShock / Dual Analog), as titles; regional names are listed where they
// differ. Sources: the games' own "Analog Control Compatible" marks as
// collected in libretro-database issue #64 and NGEmu's DualShock list.
var psxAnalogTitles = []string{
	"Ace Combat 2",
	"Ace Combat 3 - Electrosphere",
	"Alien Resurrection",
	"Ape Escape",
	"Breath of Fire III",
	"Breath of Fire IV",
	"Chrono Cross",
	"Colin McRae Rally",
	"Colin McRae Rally 2.0",
	"Crash Bandicoot 2 - Cortex Strikes Back",
	"Crash Bandicoot - Warped",
	"Crash Bandicoot 3 - Warped",
	"Crash Team Racing",
	"CTR - Crash Team Racing",
	"Dino Crisis",
	"Dino Crisis 2",
	"Driver",
	"Driver - You Are the Wheelman",
	"Driver 2",
	"Driver 2 - Back on the Streets",
	"Final Fantasy VIII",
	"Final Fantasy IX",
	"Gran Turismo",
	"Gran Turismo 2",
	"Jet Moto 3",
	"Legacy of Kain - Soul Reaver",
	"Medal of Honor",
	"Medal of Honor - Underground",
	"MediEvil",
	"MediEvil II",
	"MediEvil 2",
	"Metal Gear Solid",
	"Metal Gear Solid - VR Missions",
	"Metal Gear Solid - Special Missions",
	"Need for Speed III - Hot Pursuit",
	"Need for Speed - High Stakes",
	"Need for Speed - Road Challenge",
	"Need for Speed - Porsche Unleashed",
	"Need for Speed - Porsche 2000",
	"Parasite Eve",
	"Parasite Eve II",
	"Populous - The Beginning",
	"Porsche Challenge",
	"Quake II",
	"R4 - Ridge Racer Type 4",
	"Ridge Racer Type 4",
	"Rayman 2 - The Great Escape",
	"Resident Evil - Director's Cut - Dual Shock Ver.",
	"Resident Evil 2 - Dual Shock Ver.",
	"Resident Evil 3 - Nemesis",
	"Silent Hill",
	"Spider-Man",
	"Spider-Man 2 - Enter Electro",
	"Spyro the Dragon",
	"Spyro 2 - Ripto's Rage!",
	"Spyro 2 - Gateway to Glimmer",
	"Spyro - Year of the Dragon",
	"Syphon Filter",
	"Syphon Filter 2",
	"Syphon Filter 3",
	"Tomb Raider III - Adventures of Lara Croft",
	"Tomb Raider - The Last Revelation",
	"Tomb Raider Chronicles",
	"Tony Hawk's Pro Skater",
	"Tony Hawk's Skateboarding",
	"Tony Hawk's Pro Skater 2",
	"Tony Hawk's Pro Skater 3",
	"Tony Hawk's Pro Skater 4",
	"Wipeout 3",
	"Wip3out",
}

// psxAnalogIndex maps a normalised title to the list's spelling; built once.
var psxAnalogIndex = sync.OnceValue(func() map[string]string {
	out := make(map[string]string, len(psxAnalogTitles))
	for _, t := range psxAnalogTitles {
		if key := psxAnalogNorm(t); key != "" {
			out[key] = t
		}
	}
	return out
})

// psxAnalogNorm normalises a list title the way normalizeGameTitle reads a
// file name. The list holds titles, not file names: the dummy extension keeps
// a title's own dot ("Colin McRae Rally 2.0") from being cut as one.
func psxAnalogNorm(title string) string {
	return normalizeGameTitle(title + ".cue")
}

// psxAnalogKnownTitle finds a game in the list by its file name, then by its
// library titles. ok is false for a game not in it.
func psxAnalogKnownTitle(game libraryGame) (string, bool) {
	index := psxAnalogIndex()
	if t, ok := index[normalizeGameTitle(filepath.Base(game.Path))]; ok {
		return t, true
	}
	for _, title := range []string{game.Title, game.DisplayTitle} {
		if strings.TrimSpace(title) == "" {
			continue
		}
		if t, ok := index[psxAnalogNorm(title)]; ok {
			return t, true
		}
	}
	return "", false
}

// psxAnalogIsGame reports a PlayStation game: the console the feature is for.
func psxAnalogIsGame(game libraryGame) bool {
	return canonicalCompatSystem(game.System) == psxAnalogConsole
}

// ── The choices ──────────────────────────────────────────────────────────

func normalisePSXAnalog(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, m := range psxAnalogModes {
		if v == m {
			return v
		}
	}
	return ""
}

func normalisePSXAnalogGame(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case psxAnalogGameDualShock, psxAnalogGameStandard:
		return v
	}
	return ""
}

// psxAnalogKey is the console setting's Custom key:
// gameControls.playstation.analog, beside the console's buttons and turbo.
func psxAnalogKey() string { return gameControlsConsoleKey(psxAnalogConsole, "analog") }

// psxAnalogSetting is the console setting; unset is Auto.
func psxAnalogSetting(c *Config) string {
	if v := normalisePSXAnalog(gameControlsRaw(c, psxAnalogKey())); v != "" {
		return v
	}
	return psxAnalogAuto
}

// psxAnalogDecision is what a launch plugs into port 1 and why.
type psxAnalogDecision struct {
	Pad string // psxAnalogPad*, or "" for RetroArch's own device
	Why string // for the log and the sheet
}

// psxAnalogConsoleDecide is the console setting's answer for a game, its own
// choice aside. sticks is whether the handheld has analog sticks.
func psxAnalogConsoleDecide(c *Config, game libraryGame, sticks bool) psxAnalogDecision {
	switch psxAnalogSetting(c) {
	case psxAnalogAlways:
		return psxAnalogDecision{Pad: psxAnalogPadDualShock, Why: "Always"}
	case psxAnalogNever:
		return psxAnalogDecision{Why: "Never"}
	}
	title, known := psxAnalogKnownTitle(game)
	switch {
	case !known:
		return psxAnalogDecision{Why: "not in the analog list"}
	case !sticks:
		return psxAnalogDecision{Why: title + " in list, but this handheld has no sticks"}
	}
	return psxAnalogDecision{Pad: psxAnalogPadDualShock, Why: title + " in list"}
}

// psxAnalogDecide is a launch's answer: the game's own choice, else its
// console's. Not a PlayStation game: nothing.
func psxAnalogDecide(c *Config, game libraryGame, sticks bool) psxAnalogDecision {
	if !psxAnalogIsGame(game) {
		return psxAnalogDecision{}
	}
	switch emuExtrasFor(game.Path).Analog {
	case psxAnalogGameDualShock:
		return psxAnalogDecision{Pad: psxAnalogPadDualShock, Why: "set for this game"}
	case psxAnalogGameStandard:
		return psxAnalogDecision{Pad: psxAnalogPadStandard, Why: "set for this game"}
	}
	return psxAnalogConsoleDecide(c, game, sticks)
}

// psxAnalogDevice is the device id a core takes for pad, ok false for a core
// whose ids JukaHub does not know.
func psxAnalogDevice(core, pad string) (int, bool) {
	core = normalizeCoreName(core)
	var dualshock int
	switch {
	case strings.HasPrefix(core, "pcsx_rearmed"),
		core == "mednafen_psx", core == "mednafen_psx_hw", core == "beetle_psx", core == "beetle_psx_hw":
		dualshock = 517
	case core == "swanstation":
		dualshock = 261
	default:
		return 0, false
	}
	if pad == psxAnalogPadStandard {
		return 1, true
	}
	return dualshock, true
}

// psxAnalogValues are the launch's retroarch.cfg values for pad. Pure.
func psxAnalogValues(device int, pad string) []raScopeValue {
	values := []raScopeValue{{Key: "input_libretro_device_p1", Value: strconv.Itoa(device)}}
	if pad == psxAnalogPadDualShock {
		// 0 = none: the stick reaches the game as a stick.
		values = append(values, raScopeValue{Key: "input_player1_analog_dpad_mode", Value: "0"})
	}
	return values
}

// psxAnalogPadLabel names a pad for the log and the sheet.
func psxAnalogPadLabel(pad string) string {
	switch pad {
	case psxAnalogPadDualShock:
		return "DualShock"
	case psxAnalogPadStandard:
		return "Standard pad"
	}
	return "RetroArch's own"
}

// ── Launch ───────────────────────────────────────────────────────────────

// psxAnalogCandidates is a variable so tests could point at a temporary
// config.
var psxAnalogCandidates = retroArchConfigCandidates

// psxAnalogPrepare is the launch hook. It never blocks or fails a launch: a
// core it does not know, a light gun on the port or a missing config is
// logged and the game starts with RetroArch's own pad. Main thread.
func psxAnalogPrepare(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("controls", "choosing the PlayStation pad failed: %v", r)
		}
	}()
	if appConfig == nil || !psxAnalogIsGame(game) || !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	d := psxAnalogDecide(appConfig, game, stickHardware())
	if d.Pad == "" {
		logInfo("controls", "psx analog: RetroArch's own pad (%s)", d.Why)
		return
	}
	// The light gun owns port 1 for its games (lightgun.go).
	if l, ok := ctrlLayoutForGame(game); ok && l.ID == lightgunLayoutID {
		logInfo("controls", "psx analog: skipped, the light gun has port 1 (%s)", filepath.Base(game.Path))
		return
	}
	core := gameCoreFromLauncher(launcher, game.System)
	device, ok := psxAnalogDevice(core, d.Pad)
	if !ok {
		logInfo("controls", "psx analog: %s not set, JukaHub does not know %s's device ids (%s)", psxAnalogPadLabel(d.Pad), core, d.Why)
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, psxAnalogCandidates()))
	if len(targets) == 0 {
		logInfo("controls", "psx analog: no retroarch.cfg found for %s; RetroArch's own pad", launcher)
		return
	}
	applied := false
	for _, cfgPath := range targets {
		if _, err := raScopeApply(cfgPath, psxAnalogValues(device, d.Pad)); err != nil {
			logWarn("controls", "psx analog: cannot set the pad in %s: %v", cfgPath, err)
			continue
		}
		applied = true
	}
	if !applied {
		return
	}
	logInfo("controls", "psx analog: %s (%s) · %s device %d", psxAnalogPadLabel(d.Pad), d.Why, core, device)
	if remap := psxAnalogUserRemap(core, game, targets); remap != "" {
		logInfo("controls", "psx analog: %s is the user's own remap and names its own device; RetroArch uses that one", remap)
	}
}

// psxAnalogUserRemap is a remap file the user (or CrossMix's detector) saved
// for the game, its folder or its core that names a port-1 device: RetroArch
// loads it after retroarch.cfg, so its device wins. "" when there is none.
func psxAnalogUserRemap(core string, game libraryGame, targets []string) string {
	folders := append(append([]string(nil), raTuneCoreFolders[core]...), resetCoreFolders(core)...)
	for _, cfgPath := range targets {
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		dir, _ := raRemapDir(string(raw), cfgPath)
		for _, folder := range folders {
			for _, p := range raRemapCandidates(dir, folder, game.Path) {
				if !raRemapUserFile(p) {
					continue
				}
				data, err := raRemapReadSmall(p)
				if err != nil {
					continue
				}
				if _, has := raConfigGet(string(data), "input_libretro_device_p1"); has {
					return p
				}
			}
		}
	}
	return ""
}

func init() {
	gameLaunchRegisterPrepare(psxAnalogPrepare)
}
