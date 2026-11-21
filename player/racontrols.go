package main

// racontrols.go — the buttons and turbo of RetroArch games started from the
// library: which face button does what, and a button that fires by itself
// while it is held.
//
// A handheld's A, B, X and Y sit where Nintendo put them, and many games
// (and players coming from another pad) want them the other way round: the
// SNES's B-to-jump under the thumb, a Sega game's main button on the right.
// Shooters and button-mashers want turbo. RetroArch can do both — remap files
// and Turbo Fire — but each is three menus deep and saved in a place the
// player has to remember. Here they are two choices:
//
//	Buttons  Normal, Swap A and B, Swap X and Y, Swap both (the Xbox layout)
//	Turbo    Off, A, B — holding that button fires it over and over
//
// plus one Turbo speed for every console (Fast, Normal, Slow), and D-pad as
// stick per console, which puts the D-pad lines in the same remap file
// (racontrols_dpad.go).
//
// Each console has its own Buttons and Turbo (Settings → Controls → Game
// buttons, racontrols_ui.go), with an "All consoles" choice the consoles
// start from, and each game can override its console in the game options
// sheet (game_options_ui.go). The console choices live in the custom map
// (gameControls.buttons, gameControls.<console>.turbo, …); the per-game ones
// in data/game-controls.json:
//
//	{"schemaVersion":1,"games":{
//	   "/mnt/SDCARD/Roms/SFC/Super Mario World (USA).sfc":{"buttons":"swap-ab","turbo":"b"}}}
//
// Nothing is written for good. At a library launch (prepareGameControls,
// called from launchLibraryGame beside prepareCheats):
//
//   - Buttons other than Default write a one-launch RetroArch remap file
//     (racontrols_remap.go) for the game, removed when RetroArch exits, the
//     way the video filter's shader preset and the cheats' file are. A remap
//     file the user saved in RetroArch for the game, its folder or its core is
//     never overwritten, deleted or shadowed: that launch keeps the user's own
//     remap, the log says so and the sheet's Buttons row shows it. The game's
//     control layout (ctrllayout_apply.go) puts its buttons in the same file,
//     winning over the Buttons choice for the buttons it sets.
//   - Turbo other than Off sets RetroArch's Turbo Fire keys in retroarch.cfg
//     through raScopeApply (racfgscope.go), put back when RetroArch exits
//     (racontrols_turbo.go explains the two generations of those keys).
//
// Standalone emulators and NextUI paks (minarch) are left alone: neither reads
// RetroArch's remap files or Turbo Fire.
//
// The in-game menu's Cheats list (gamemenu_cheats.go) is noted at the same
// launch, since it needs the cheat file prepareCheats has just written.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	gameControlsSchemaVersion = 1
	gameControlsRecordName    = "game-controls.json"

	// The custom-map keys: the all-consoles choices and the turbo speed;
	// a console's own choice is gameControls.<console slug>.buttons/.turbo.
	gameControlsPrefix        = "gameControls."
	gameControlsButtonsKey    = "gameControls.buttons"
	gameControlsTurboKey      = "gameControls.turbo"
	gameControlsTurboSpeedKey = "gameControls.turboSpeed"

	// Buttons.
	gameButtonsDefault  = "default"
	gameButtonsSwapAB   = "swap-ab"
	gameButtonsSwapXY   = "swap-xy"
	gameButtonsSwapBoth = "swap-both"

	// Turbo.
	gameTurboOff = "off"
	gameTurboA   = "a"
	gameTurboB   = "b"

	// Turbo speed.
	gameTurboFast   = "fast"
	gameTurboNormal = "normal"
	gameTurboSlow   = "slow"
)

// gameButtonsModes and gameTurboModes are the choices in menu order.
var (
	gameButtonsModes = []string{gameButtonsDefault, gameButtonsSwapAB, gameButtonsSwapXY, gameButtonsSwapBoth}
	gameTurboModes   = []string{gameTurboOff, gameTurboA, gameTurboB}
	gameTurboSpeeds  = []string{gameTurboFast, gameTurboNormal, gameTurboSlow}
)

func normaliseGameButtons(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, m := range gameButtonsModes {
		if v == m {
			return v
		}
	}
	return ""
}

func normaliseGameTurbo(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, m := range gameTurboModes {
		if v == m {
			return v
		}
	}
	return ""
}

func gameButtonsLabel(mode string) string {
	switch mode {
	case gameButtonsSwapAB:
		return "Swap A and B"
	case gameButtonsSwapXY:
		return "Swap X and Y"
	case gameButtonsSwapBoth:
		return "Swap both"
	}
	return "Normal"
}

func gameButtonsNote(mode string) string {
	switch mode {
	case gameButtonsSwapAB:
		return "A does what B does in the game, and B what A does"
	case gameButtonsSwapXY:
		return "X does what Y does in the game, and Y what X does"
	case gameButtonsSwapBoth:
		return "A and B, and X and Y, swapped: the Xbox layout"
	}
	return "Every button does what its letter says"
}

func gameTurboLabel(mode string) string {
	switch mode {
	case gameTurboA:
		return "A"
	case gameTurboB:
		return "B"
	}
	return "Off"
}

func gameTurboNote(mode string) string {
	switch mode {
	case gameTurboA:
		return "Holding A fires it over and over"
	case gameTurboB:
		return "Holding B fires it over and over"
	}
	return "Every button presses once"
}

func gameTurboSpeedLabel(speed string) string {
	switch speed {
	case gameTurboFast:
		return "Fast"
	case gameTurboSlow:
		return "Slow"
	}
	return "Normal"
}

// ── Console choices (custom map) ─────────────────────────────────────────

// gameControlsConsoleKey is a console's own key ("buttons" or "turbo").
func gameControlsConsoleKey(console, what string) string {
	return gameControlsPrefix + raTuneSlug(console) + "." + what
}

func gameControlsRaw(c *Config, key string) string {
	if c == nil {
		return ""
	}
	if v, ok := customValue(c.Variables.Custom, key); ok && v != nil {
		return coerceString(v)
	}
	return ""
}

// gameButtonsAll and gameTurboAll are the all-consoles choices.
func gameButtonsAll(c *Config) string {
	if v := normaliseGameButtons(gameControlsRaw(c, gameControlsButtonsKey)); v != "" {
		return v
	}
	return gameButtonsDefault
}

func gameTurboAll(c *Config) string {
	if v := normaliseGameTurbo(gameControlsRaw(c, gameControlsTurboKey)); v != "" {
		return v
	}
	return gameTurboOff
}

// gameButtonsConsoleOwn and gameTurboConsoleOwn are a console's own choice,
// "" when it follows All consoles.
func gameButtonsConsoleOwn(c *Config, console string) string {
	return normaliseGameButtons(gameControlsRaw(c, gameControlsConsoleKey(console, "buttons")))
}

func gameTurboConsoleOwn(c *Config, console string) string {
	return normaliseGameTurbo(gameControlsRaw(c, gameControlsConsoleKey(console, "turbo")))
}

// gameControlsConsole is the console name a library system goes by on the
// Settings page (raTuneSystemFor's names: "GBA" is "Game Boy Advance").
func gameControlsConsole(system string) string {
	sys, _ := raTuneSystemFor(system)
	if sys.Name != "" {
		return sys.Name
	}
	return strings.TrimSpace(system)
}

// gameButtonsForConsole and gameTurboForConsole resolve a console's choice.
func gameButtonsForConsole(c *Config, system string) string {
	if v := gameButtonsConsoleOwn(c, gameControlsConsole(system)); v != "" {
		return v
	}
	return gameButtonsAll(c)
}

func gameTurboForConsole(c *Config, system string) string {
	if v := gameTurboConsoleOwn(c, gameControlsConsole(system)); v != "" {
		return v
	}
	return gameTurboAll(c)
}

// gameTurboSpeedSetting is the one speed for every console; unset is Normal.
func gameTurboSpeedSetting(c *Config) string {
	v := strings.ToLower(strings.TrimSpace(gameControlsRaw(c, gameControlsTurboSpeedKey)))
	for _, s := range gameTurboSpeeds {
		if v == s {
			return v
		}
	}
	return gameTurboNormal
}

// ── Per-game choices (data/game-controls.json) ───────────────────────────

// gameControlsGame is one ROM's own choices; "" follows its console.
type gameControlsGame struct {
	Buttons string `json:"buttons,omitempty"`
	Turbo   string `json:"turbo,omitempty"`
}

type gameControlsFile struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Games         map[string]gameControlsGame `json:"games"`
}

// gameControlsStore is the record in memory, plus what the last launch of a
// game could not do (shown on the sheet's rows; not saved).
var gameControlsStore struct {
	mu     sync.Mutex
	loaded bool
	games  map[string]gameControlsGame
	notes  map[string]string
}

// gameControlsPath is the record in JukaHub's data folder; a variable for
// tests.
var gameControlsPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, gameControlsRecordName)
}

func gameControlsLoadLocked() {
	if gameControlsStore.loaded {
		return
	}
	gameControlsStore.loaded = true
	gameControlsStore.games = map[string]gameControlsGame{}
	path := gameControlsPath()
	if path == "" {
		return
	}
	var file gameControlsFile
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn("controls", "ignoring %s: %v", path, err)
		}
		return
	}
	for rom, g := range file.Games {
		key := gameOptionsKey(rom)
		g.Buttons, g.Turbo = normaliseGameButtons(g.Buttons), normaliseGameTurbo(g.Turbo)
		if key == "" || (g.Buttons == "" && g.Turbo == "") {
			continue
		}
		gameControlsStore.games[key] = g
	}
}

// gameControlsFor is one ROM's own choices.
func gameControlsFor(romPath string) gameControlsGame {
	key := gameOptionsKey(romPath)
	if key == "" {
		return gameControlsGame{}
	}
	gameControlsStore.mu.Lock()
	defer gameControlsStore.mu.Unlock()
	gameControlsLoadLocked()
	return gameControlsStore.games[key]
}

// gameControlsSet stores one ROM's choices and writes the record. Main
// thread (the sheet); the file is small and written whole.
func gameControlsSet(romPath string, g gameControlsGame) {
	key := gameOptionsKey(romPath)
	if key == "" {
		return
	}
	g.Buttons, g.Turbo = normaliseGameButtons(g.Buttons), normaliseGameTurbo(g.Turbo)
	gameControlsStore.mu.Lock()
	gameControlsLoadLocked()
	if g.Buttons == "" && g.Turbo == "" {
		delete(gameControlsStore.games, key)
	} else {
		gameControlsStore.games[key] = g
	}
	file := gameControlsSnapshotLocked()
	gameControlsStore.mu.Unlock()
	gameControlsSave(file)
}

// gameControlsForget drops one ROM's own choices (Reset game, Delete game);
// the file is written only when it had some. Main thread.
func gameControlsForget(romPath string) {
	gameControlsSetNote(romPath, "")
	ctrlLayoutForgetGame(romPath) // its control layout (ctrllayout_store.go)
	raGameRecordsForget(romPath)  // its hotkeys and aspect ratio (rahotkeys_store.go)
	emuExtrasForget(romPath)      // its PlayStation pad and widescreen (widescreen_store.go)
	if own := gameControlsFor(romPath); own.Buttons != "" || own.Turbo != "" {
		gameControlsSet(romPath, gameControlsGame{})
	}
}

// gameControlsMove carries one ROM's own choices to its new file (Rename
// file, the Game Boy Color move), in one write. Main thread.
func gameControlsMove(oldPath, newPath string) {
	oldKey, newKey := gameOptionsKey(oldPath), gameOptionsKey(newPath)
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return
	}
	gameControlsSetNote(oldPath, "")
	ctrlLayoutMoveGame(oldPath, newPath) // its control layout (ctrllayout_store.go)
	raGameRecordsMove(oldPath, newPath)  // its hotkeys and aspect ratio (rahotkeys_store.go)
	emuExtrasMove(oldPath, newPath)      // its PlayStation pad and widescreen (widescreen_store.go)
	gameControlsStore.mu.Lock()
	gameControlsLoadLocked()
	own, ok := gameControlsStore.games[oldKey]
	if !ok {
		gameControlsStore.mu.Unlock()
		return
	}
	delete(gameControlsStore.games, oldKey)
	gameControlsStore.games[newKey] = own
	file := gameControlsSnapshotLocked()
	gameControlsStore.mu.Unlock()
	gameControlsSave(file)
}

// gameControlsSnapshotLocked is the record as it is written.
func gameControlsSnapshotLocked() gameControlsFile {
	file := gameControlsFile{SchemaVersion: gameControlsSchemaVersion, Games: make(map[string]gameControlsGame, len(gameControlsStore.games))}
	for k, v := range gameControlsStore.games {
		file.Games[k] = v
	}
	return file
}

// gameControlsSave writes the record whole (it is small).
func gameControlsSave(file gameControlsFile) {
	path := gameControlsPath()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = AtomicWrite(path, append(data, '\n'), 0o644)
	}
	if err != nil {
		logWarn("controls", "cannot save the game's buttons: %v", err)
		ShowToast("Cannot save the buttons: "+firstLine(err.Error()), ToastKindWarning)
	}
}

// gameControlsNote is what the last launch of a ROM could not apply, "" when
// it all went in.
func gameControlsNote(romPath string) string {
	gameControlsStore.mu.Lock()
	defer gameControlsStore.mu.Unlock()
	return gameControlsStore.notes[gameOptionsKey(romPath)]
}

func gameControlsSetNote(romPath, note string) {
	key := gameOptionsKey(romPath)
	if key == "" {
		return
	}
	gameControlsStore.mu.Lock()
	defer gameControlsStore.mu.Unlock()
	if gameControlsStore.notes == nil {
		gameControlsStore.notes = map[string]string{}
	}
	if note == "" {
		delete(gameControlsStore.notes, key)
		return
	}
	gameControlsStore.notes[key] = note
}

// gameButtonsForGame and gameTurboForGame are what a launch of game uses: its
// own choice, else its console's.
func gameButtonsForGame(c *Config, game libraryGame) string {
	if own := gameControlsFor(game.Path).Buttons; own != "" {
		return own
	}
	return gameButtonsForConsole(c, game.System)
}

func gameTurboForGame(c *Config, game libraryGame) string {
	if own := gameControlsFor(game.Path).Turbo; own != "" {
		return own
	}
	return gameTurboForConsole(c, game.System)
}

// ── Launch ───────────────────────────────────────────────────────────────

// gameControlsCandidates is a variable so tests could point at a temporary
// config.
var gameControlsCandidates = retroArchConfigCandidates

// prepareGameControls is the launch-time entry point (game_library.go, after
// prepareCheats). It never blocks or fails a launch: whatever cannot be
// applied is logged, noted for the sheet, and the game starts with
// RetroArch's own buttons.
func prepareGameControls(launcher string, game libraryGame) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("controls", "preparing the game's buttons failed: %v", r)
		}
	}()
	// The in-game menu's cheat list reads the file prepareCheats just wrote.
	gmCheatsNoteLaunch(launcher, game)
	if !retroArchLauncher(launcher) || nextuiIsPakLauncher(launcher) {
		return
	}
	buttons := gameButtonsForGame(appConfig, game)
	turbo := gameTurboForGame(appConfig, game)
	// The game's control layout shares this one remap file: its buttons win
	// over the Buttons choice for the buttons it sets (ctrllayout_apply.go).
	layout, _ := ctrlLayoutLaunchFor(launcher, game)
	// D-pad as stick goes in under the layout too (racontrols_dpad.go).
	remap := ctrlLayoutMergeRemap(raDpadStickMerge(raRemapPreset(buttons), game), layout)
	if len(remap) == 0 && turbo == gameTurboOff {
		gameControlsSetNote(game.Path, "")
		return
	}
	targets := raScopeTargets(raLaunchCandidates(launcher, gameControlsCandidates()))
	if len(targets) == 0 {
		logInfo("controls", "no retroarch.cfg found for %s; launching with RetroArch's own buttons", launcher)
		gameControlsSetNote(game.Path, "Not applied last time: JukaHub found no RetroArch settings file")
		return
	}
	var notes []string
	turboRemap := map[string][]raScopeValue{}
	if turbo != gameTurboOff {
		speed := gameTurboSpeedSetting(appConfig)
		applied := false
		why := ""
		for _, cfgPath := range targets {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				continue
			}
			values, newKeys, reason := raTurboValues(string(raw), cfgPath, turbo, speed)
			if len(values) == 0 {
				why = reason
				logInfo("controls", "turbo %s not applied in %s: %s", turbo, cfgPath, reason)
				continue
			}
			if newKeys {
				// RetroArch 1.22.1 and newer read Turbo Fire from a remap file
				// too: one JukaHub writes carries the same values.
				turboRemap[cfgPath] = values
			}
			if _, err := raScopeApply(cfgPath, values); err != nil {
				logWarn("controls", "cannot set turbo in %s: %v", cfgPath, err)
				continue
			}
			applied = true
		}
		if applied {
			Log().Info("turbo applied for this launch", "button", turbo, "speed", speed, "system", game.System)
		} else if why != "" {
			notes = append(notes, "Turbo not applied last time: "+why)
		}
	}
	if len(remap) > 0 {
		if note := raRemapPrepare(launcher, game, targets, remap, ctrlLayoutRemapLabel(raDpadStickLabel(buttons, game), layout), turboRemap); note != "" {
			notes = append(notes, note)
		}
	}
	gameControlsSetNote(game.Path, strings.Join(notes, " · "))
}
