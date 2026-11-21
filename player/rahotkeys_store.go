package main

// rahotkeys_store.go — the record behind the custom RetroArch hotkeys
// (rahotkeys.go) and the aspect ratio (raratio.go): choices for every
// console, for one console or for one game, in one small JSON file per
// feature in JukaHub's data folder.
//
//	{"schemaVersion":1,
//	 "global":{"hotkey":"select","save":"x"},
//	 "consoles":{"snes":{"save":"y"}},
//	 "games":{"/mnt/SDCARD/Roms/SFC/Super Mario World (USA).sfc":{"exit":"start"}}}
//
// A game's field wins over its console's, which wins over the all-consoles
// one; a missing field follows the next level. A console is keyed by
// raTuneSlug of its Settings name ("Game Boy Advance" → "game-boy-advance"),
// a game by gameOptionsKey of its ROM path. Rename file and the Game Boy
// Color move carry a game's entry, Reset game and Delete game drop it, through
// the same calls that carry its Buttons and Turbo (gameControlsMove and
// gameControlsForget in racontrols.go).
//
// Each store also keeps, for the sheet, what the last launch of a game could
// not apply (not saved).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const raLevelsSchemaVersion = 1

// raLevels is one record as it is written.
type raLevels struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Global        map[string]string            `json:"global,omitempty"`
	Consoles      map[string]map[string]string `json:"consoles,omitempty"`
	Games         map[string]map[string]string `json:"games,omitempty"`
}

// raLevel names where a choice is stored: one game (Game, a ROM path), one
// console (Console, its Settings name) or, with both empty, every console.
type raLevel struct {
	Console string
	Game    string
}

// raLevelStore is one feature's record in memory.
type raLevelStore struct {
	name string // the file in the data folder
	tag  string // the log subsystem
	// norm keeps a stored value this build knows ("" drops it): a record an
	// older or newer build wrote never applies a value nobody can read.
	norm func(field, value string) string

	mu     sync.Mutex
	loaded bool
	data   raLevels
	notes  map[string]string
}

// raLevelDataDir is JukaHub's data folder; a variable for tests.
var raLevelDataDir = func() string {
	dir, err := P().DataDir()
	if err != nil {
		return ""
	}
	return dir
}

func (s *raLevelStore) path() string {
	dir := raLevelDataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, s.name)
}

// cleanFields is fields with only the values this build knows, nil when
// none is left. Pure but for norm.
func (s *raLevelStore) cleanFields(fields map[string]string) map[string]string {
	var out map[string]string
	for k, v := range fields {
		if s.norm != nil {
			v = s.norm(k, v)
		}
		if v == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

func (s *raLevelStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.data = raLevels{SchemaVersion: raLevelsSchemaVersion}
	path := s.path()
	if path == "" {
		return
	}
	var file raLevels
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn(s.tag, "ignoring %s: %v", path, err)
		}
		return
	}
	s.data.Global = s.cleanFields(file.Global)
	for name, fields := range file.Consoles {
		// A hand-edited "SNES" is the "snes" fieldsLocked looks up.
		if f, slug := s.cleanFields(fields), raTuneSlug(name); f != nil && slug != "" {
			if s.data.Consoles == nil {
				s.data.Consoles = map[string]map[string]string{}
			}
			s.data.Consoles[slug] = f
		}
	}
	for rom, fields := range file.Games {
		key := gameOptionsKey(rom)
		if f := s.cleanFields(fields); f != nil && key != "" {
			if s.data.Games == nil {
				s.data.Games = map[string]map[string]string{}
			}
			s.data.Games[key] = f
		}
	}
}

// fieldsLocked is the map a level reads from and writes to; create makes a
// missing one.
func (s *raLevelStore) fieldsLocked(l raLevel, create bool) map[string]string {
	switch {
	case l.Game != "":
		key := gameOptionsKey(l.Game)
		if s.data.Games == nil && create {
			s.data.Games = map[string]map[string]string{}
		}
		if s.data.Games[key] == nil && create {
			s.data.Games[key] = map[string]string{}
		}
		return s.data.Games[key]
	case l.Console != "":
		slug := raTuneSlug(l.Console)
		if s.data.Consoles == nil && create {
			s.data.Consoles = map[string]map[string]string{}
		}
		if s.data.Consoles[slug] == nil && create {
			s.data.Consoles[slug] = map[string]string{}
		}
		return s.data.Consoles[slug]
	}
	if s.data.Global == nil && create {
		s.data.Global = map[string]string{}
	}
	return s.data.Global
}

// dropEmptyLocked removes a level left with no fields.
func (s *raLevelStore) dropEmptyLocked(l raLevel) {
	switch {
	case l.Game != "":
		if len(s.data.Games[gameOptionsKey(l.Game)]) == 0 {
			delete(s.data.Games, gameOptionsKey(l.Game))
		}
	case l.Console != "":
		if len(s.data.Consoles[raTuneSlug(l.Console)]) == 0 {
			delete(s.data.Consoles, raTuneSlug(l.Console))
		}
	default:
		if len(s.data.Global) == 0 {
			s.data.Global = nil
		}
	}
}

// field is one level's own value, "" when it follows the next level.
func (s *raLevelStore) field(l raLevel, field string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.fieldsLocked(l, false)[field]
}

// fields is a copy of one level's own values.
func (s *raLevelStore) fields(l raLevel) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := map[string]string{}
	for k, v := range s.fieldsLocked(l, false) {
		out[k] = v
	}
	return out
}

// resolve is a game's value for field and the level it came from: 2 the
// game, 1 its console, 0 every console; "" and -1 when nothing is set.
func (s *raLevelStore) resolve(console, romPath, field string) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if romPath != "" {
		if v := s.fieldsLocked(raLevel{Game: romPath}, false)[field]; v != "" {
			return v, 2
		}
	}
	if console != "" {
		if v := s.fieldsLocked(raLevel{Console: console}, false)[field]; v != "" {
			return v, 1
		}
	}
	if v := s.data.Global[field]; v != "" {
		return v, 0
	}
	return "", -1
}

// set stores one value ("" clears it) and writes the record. Main thread
// (the Settings rows and the sheet); the file is small and written whole.
func (s *raLevelStore) set(l raLevel, field, value string) {
	if s.norm != nil && value != "" {
		value = s.norm(field, value)
	}
	if l.Game != "" && gameOptionsKey(l.Game) == "" {
		return
	}
	s.mu.Lock()
	s.loadLocked()
	if value == "" {
		delete(s.fieldsLocked(l, false), field)
	} else {
		s.fieldsLocked(l, true)[field] = value
	}
	s.dropEmptyLocked(l)
	snap := s.snapshotLocked()
	s.mu.Unlock()
	s.save(snap)
}

// clear drops every value of one level; it reports whether there were any.
func (s *raLevelStore) clear(l raLevel) bool {
	s.mu.Lock()
	s.loadLocked()
	if len(s.fieldsLocked(l, false)) == 0 {
		s.mu.Unlock()
		return false
	}
	switch {
	case l.Game != "":
		delete(s.data.Games, gameOptionsKey(l.Game))
	case l.Console != "":
		delete(s.data.Consoles, raTuneSlug(l.Console))
	default:
		s.data.Global = nil
	}
	snap := s.snapshotLocked()
	s.mu.Unlock()
	s.save(snap)
	return true
}

// forgetGame drops one ROM's own values and note (Reset game, Delete game).
func (s *raLevelStore) forgetGame(romPath string) {
	s.setNote(romPath, "")
	s.clear(raLevel{Game: romPath})
}

// moveGame carries one ROM's own values to its new file (Rename file, the
// Game Boy Color move), in one write.
func (s *raLevelStore) moveGame(oldPath, newPath string) {
	oldKey, newKey := gameOptionsKey(oldPath), gameOptionsKey(newPath)
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return
	}
	s.setNote(oldPath, "")
	s.mu.Lock()
	s.loadLocked()
	own, ok := s.data.Games[oldKey]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.data.Games, oldKey)
	s.data.Games[newKey] = own
	snap := s.snapshotLocked()
	s.mu.Unlock()
	s.save(snap)
}

// snapshotLocked is a deep copy of the record, to write outside the lock.
func (s *raLevelStore) snapshotLocked() raLevels {
	cp := func(m map[string]string) map[string]string {
		if len(m) == 0 {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	snap := raLevels{SchemaVersion: raLevelsSchemaVersion, Global: cp(s.data.Global)}
	if len(s.data.Consoles) > 0 {
		snap.Consoles = make(map[string]map[string]string, len(s.data.Consoles))
		for k, v := range s.data.Consoles {
			snap.Consoles[k] = cp(v)
		}
	}
	if len(s.data.Games) > 0 {
		snap.Games = make(map[string]map[string]string, len(s.data.Games))
		for k, v := range s.data.Games {
			snap.Games[k] = cp(v)
		}
	}
	return snap
}

func (s *raLevelStore) save(snap raLevels) {
	path := s.path()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err == nil {
		err = AtomicWrite(path, append(data, '\n'), 0o644)
	}
	if err != nil {
		logWarn(s.tag, "cannot save %s: %v", path, err)
		ShowToast("Cannot save the choice: "+firstLine(err.Error()), ToastKindWarning)
	}
}

// note is what the last launch of a ROM could not apply, "" when all went in.
func (s *raLevelStore) note(romPath string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notes[gameOptionsKey(romPath)]
}

func (s *raLevelStore) setNote(romPath, note string) {
	key := gameOptionsKey(romPath)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if note == "" {
		delete(s.notes, key)
		return
	}
	if s.notes == nil {
		s.notes = map[string]string{}
	}
	s.notes[key] = note
}

// raGameRecordsForget and raGameRecordsMove are the two stores' part of
// gameControlsForget / gameControlsMove (racontrols.go). Main thread.
func raGameRecordsForget(romPath string) {
	raHotkeyStore.forgetGame(romPath)
	raRatioStore.forgetGame(romPath)
}

func raGameRecordsMove(oldPath, newPath string) {
	raHotkeyStore.moveGame(oldPath, newPath)
	raRatioStore.moveGame(oldPath, newPath)
}
