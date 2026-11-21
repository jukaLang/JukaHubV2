package main

// widescreen_store.go — the per-game record of two launch choices: the
// PlayStation pad (psxanalog.go) and Widescreen 16:9 (widescreen.go).
//
// The game options sheet sets them for one game; the launch reads them. Both
// live in one small file in JukaHub's data folder:
//
//	data/game-emu-extras.json
//	{"schemaVersion":1,"games":{
//	   "/mnt/SDCARD/Roms/PS/Ape Escape (USA).chd":{"analog":"dualshock"},
//	   "/mnt/SDCARD/Roms/N64/Mario Kart 64 (USA).z64":{"widescreen":"off"}}}
//
// An absent field follows the console's setting. A value the build does not
// know (a hand-edited typo) reads as absent, so it means "follow the
// console", never a surprise; a game left with no choice of its own is
// dropped from the file. The record follows the ROM: Rename file moves it and
// Reset game / Delete game drop it (racontrols.go gameControlsMove/Forget).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const (
	emuExtrasSchemaVersion = 1
	emuExtrasRecordName    = "game-emu-extras.json"
)

// emuExtrasGame is one ROM's own choices; "" follows its console.
type emuExtrasGame struct {
	Analog     string `json:"analog,omitempty"`     // psxAnalogGame*: "dualshock" | "standard"
	Widescreen string `json:"widescreen,omitempty"` // widescreenGame*: "on" | "off"
}

func (g emuExtrasGame) empty() bool { return g.Analog == "" && g.Widescreen == "" }

// emuExtrasNormalise drops the values this build does not know.
func emuExtrasNormalise(g emuExtrasGame) emuExtrasGame {
	g.Analog = normalisePSXAnalogGame(g.Analog)
	g.Widescreen = normaliseWidescreenGame(g.Widescreen)
	return g
}

type emuExtrasFile struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Games         map[string]emuExtrasGame `json:"games"`
}

// emuExtrasStore is the record in memory.
var emuExtrasStore struct {
	mu     sync.Mutex
	loaded bool
	games  map[string]emuExtrasGame
}

// emuExtrasPath is the record in JukaHub's data folder; a variable for tests.
var emuExtrasPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, emuExtrasRecordName)
}

func emuExtrasLoadLocked() {
	if emuExtrasStore.loaded {
		return
	}
	emuExtrasStore.loaded = true
	emuExtrasStore.games = map[string]emuExtrasGame{}
	path := emuExtrasPath()
	if path == "" {
		return
	}
	var file emuExtrasFile
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn("emulation", "ignoring %s: %v", path, err)
		}
		return
	}
	for rom, g := range file.Games {
		key := gameOptionsKey(rom)
		g = emuExtrasNormalise(g)
		if key == "" || g.empty() {
			continue
		}
		emuExtrasStore.games[key] = g
	}
}

// emuExtrasFor is one ROM's own choices.
func emuExtrasFor(romPath string) emuExtrasGame {
	key := gameOptionsKey(romPath)
	if key == "" {
		return emuExtrasGame{}
	}
	emuExtrasStore.mu.Lock()
	defer emuExtrasStore.mu.Unlock()
	emuExtrasLoadLocked()
	return emuExtrasStore.games[key]
}

// emuExtrasSet stores one ROM's choices and writes the record. Main thread
// (the sheet); the file is small and written whole.
func emuExtrasSet(romPath string, g emuExtrasGame) {
	key := gameOptionsKey(romPath)
	if key == "" {
		return
	}
	g = emuExtrasNormalise(g)
	emuExtrasStore.mu.Lock()
	emuExtrasLoadLocked()
	if g.empty() {
		delete(emuExtrasStore.games, key)
	} else {
		emuExtrasStore.games[key] = g
	}
	file := emuExtrasSnapshotLocked()
	emuExtrasStore.mu.Unlock()
	emuExtrasSave(file)
}

// emuExtrasForget drops one ROM's choices (Reset game, Delete game); the file
// is written only when it had some.
func emuExtrasForget(romPath string) {
	if !emuExtrasFor(romPath).empty() {
		emuExtrasSet(romPath, emuExtrasGame{})
	}
}

// emuExtrasMove carries one ROM's choices to its new file (Rename file), in
// one write.
func emuExtrasMove(oldPath, newPath string) {
	oldKey, newKey := gameOptionsKey(oldPath), gameOptionsKey(newPath)
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return
	}
	emuExtrasStore.mu.Lock()
	emuExtrasLoadLocked()
	own, ok := emuExtrasStore.games[oldKey]
	if !ok {
		emuExtrasStore.mu.Unlock()
		return
	}
	delete(emuExtrasStore.games, oldKey)
	emuExtrasStore.games[newKey] = own
	file := emuExtrasSnapshotLocked()
	emuExtrasStore.mu.Unlock()
	emuExtrasSave(file)
}

func emuExtrasSnapshotLocked() emuExtrasFile {
	file := emuExtrasFile{SchemaVersion: emuExtrasSchemaVersion, Games: make(map[string]emuExtrasGame, len(emuExtrasStore.games))}
	for k, v := range emuExtrasStore.games {
		file.Games[k] = v
	}
	return file
}

// emuExtrasSave writes the record whole, through a temporary file.
func emuExtrasSave(file emuExtrasFile) {
	path := emuExtrasPath()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = AtomicWrite(path, append(data, '\n'), 0o644)
	}
	if err != nil {
		logWarn("emulation", "cannot save the game's emulation choices: %v", err)
		ShowToast("Cannot save the choice: "+firstLine(err.Error()), ToastKindWarning)
	}
}
