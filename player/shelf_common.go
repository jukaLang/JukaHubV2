package main

// shelf_common.go — what the ScummVM adventure shelf (shelf_scummvm*.go) and
// the DOS PC shelf (shelf_dos*.go) share: which console folder a file is in,
// which folder is "the game", the title rule for both, and the small file that
// keeps each game's DOS settings and whether its controls card was seen.
//
// Both shelves list a game that is a folder by one file in it (romshelf.go),
// so a game must keep one identity when that file changes: a DOS game whose
// start program the player switches keeps its settings and its "controls
// seen" mark, because both are keyed by the game's folder, never by the listed
// file. A file straight in the console folder (a .zip, a CrossMix ScummVM
// shortcut) is its own game and its own key.
//
// The state is JukaHub's own JSON file (shelves.json in the data folder):
// schemaVersion and two maps. A missing or broken file reads as "nothing set"
// and the shelves use their defaults. It is written whole with AtomicWrite on
// each change, on the main thread, when the player changes a setting or
// dismisses a card — a few hundred bytes. The scan goroutine reads the start
// program overrides, so every access holds the mutex.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	shelfStateName    = "shelves.json"
	shelfStateVersion = 1
	// shelfStateMax bounds the read: a real file is a few kilobytes.
	shelfStateMax = 1 << 20
	// shelfConsoleDepth is how many folders above a file the console folder
	// may be: Roms/DOS/<game>/<sub>/<file> is three.
	shelfConsoleDepth = 5
)

// shelfDOSSettings is one DOS game's own settings. "" everywhere means the
// core's (or the player's RetroArch) own choice. Start is relative to the
// game's folder; the others are DOSBox Pure core option values. Replaced are
// the start programs the player switched away from (relative): a launch that
// still names one (the recents, a page opened before) starts the current
// start instead, while any other program of the folder opened from Files
// starts as asked.
type shelfDOSSettings struct {
	Start    string   `json:"start,omitempty"`
	Cycles   string   `json:"cycles,omitempty"`
	Machine  string   `json:"machine,omitempty"`
	Memory   string   `json:"memory,omitempty"`
	Mouse    string   `json:"mouse,omitempty"`
	Keyboard string   `json:"keyboard,omitempty"`
	Replaced []string `json:"replaced,omitempty"`
}

// empty reports settings that change nothing.
func (s shelfDOSSettings) empty() bool {
	return s.Start == "" && s.Cycles == "" && s.Machine == "" && s.Memory == "" &&
		s.Mouse == "" && s.Keyboard == "" && len(s.Replaced) == 0
}

// shelfStateFile is shelves.json.
type shelfStateFile struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Seen          map[string]bool             `json:"seen,omitempty"`
	DOS           map[string]shelfDOSSettings `json:"dos,omitempty"`
}

var shelfState struct {
	mu     sync.Mutex
	loaded bool
	file   shelfStateFile
}

func init() {
	romShelfRegisterStem(shelfStem)
}

func shelfStatePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, shelfStateName)
}

// shelfStateLocked is the state, read from disk the first time. The caller
// holds shelfState.mu.
func shelfStateLocked() *shelfStateFile {
	if shelfState.loaded {
		return &shelfState.file
	}
	shelfState.loaded = true
	shelfState.file = shelfStateFile{}
	path := shelfStatePath()
	if path == "" {
		return &shelfState.file
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > shelfStateMax {
		return &shelfState.file
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return &shelfState.file
	}
	var f shelfStateFile
	if err := json.Unmarshal(raw, &f); err != nil || f.SchemaVersion != shelfStateVersion {
		logWarn("library", "ignoring %s (broken or from another version)", path)
		return &shelfState.file
	}
	shelfState.file = f
	return &shelfState.file
}

// shelfStateSaveLocked writes the state. The caller holds shelfState.mu.
func shelfStateSaveLocked() {
	path := shelfStatePath()
	if path == "" {
		return
	}
	f := *shelfStateLocked()
	f.SchemaVersion = shelfStateVersion
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
		logWarn("library", "cannot save %s: %v", path, err)
	}
}

// shelfSeen reports whether the controls card of the game keyed key was
// dismissed with "Got it" (or its toast was shown).
func shelfSeen(key string) bool {
	shelfState.mu.Lock()
	defer shelfState.mu.Unlock()
	return shelfStateLocked().Seen[key]
}

func shelfMarkSeen(key string) {
	shelfState.mu.Lock()
	defer shelfState.mu.Unlock()
	st := shelfStateLocked()
	if st.Seen[key] {
		return
	}
	if st.Seen == nil {
		st.Seen = map[string]bool{}
	}
	st.Seen[key] = true
	shelfStateSaveLocked()
}

// shelfDOSGet is the DOS game keyed key's settings.
func shelfDOSGet(key string) shelfDOSSettings {
	shelfState.mu.Lock()
	defer shelfState.mu.Unlock()
	s := shelfStateLocked().DOS[key]
	// The caller may append to it: never to the stored slice.
	s.Replaced = append([]string(nil), s.Replaced...)
	return s
}

// shelfDOSSet stores them; all-default settings drop the game's entry.
func shelfDOSSet(key string, s shelfDOSSettings) {
	shelfState.mu.Lock()
	defer shelfState.mu.Unlock()
	st := shelfStateLocked()
	if s.empty() {
		if _, had := st.DOS[key]; !had {
			return
		}
		delete(st.DOS, key)
	} else {
		if st.DOS == nil {
			st.DOS = map[string]shelfDOSSettings{}
		}
		st.DOS[key] = s
	}
	shelfStateSaveLocked()
}

// ── Where a file is ─────────────────────────────────────────────────────────

// shelfConsoleOf finds the console folder above path when it is a DOS or
// ScummVM one. A console folder sits right in a Roms folder (every library
// root is one), and the first such folder up from the file decides: a game's
// own "DOS" subfolder is not the console, and a folder called "dos" above the
// whole Roms tree (a Windows user name) never turns every game into one DOS
// game. A game folder named like NextUI's "Name (TAG)" ("Doom (1993)", "Sky
// (CD)") is no console unless its tag names one. Without a Roms parent the
// nearest console-named folder answers. Path only, no disk access.
func shelfConsoleOf(path string) (consoleDir, system string, ok bool) {
	dir := filepath.Dir(filepath.Clean(path))
	nearDir, nearSystem := "", ""
	for i := 0; i < shelfConsoleDepth; i++ {
		name := filepath.Base(dir)
		if name == "" || name == "." || name == string(filepath.Separator) || strings.HasSuffix(name, ":") {
			break
		}
		parent := filepath.Dir(dir)
		if s, known := shelfConsoleName(name); known {
			if strings.EqualFold(filepath.Base(parent), "roms") {
				if s == "DOS" || s == "ScummVM" {
					return dir, s, true
				}
				return "", "", false
			}
			if nearDir == "" {
				nearDir, nearSystem = dir, s
			}
		}
		if parent == dir {
			break
		}
		dir = parent
	}
	if nearSystem == "DOS" || nearSystem == "ScummVM" {
		return nearDir, nearSystem, true
	}
	return "", "", false
}

// shelfConsoleName is the console a folder name names: an alias ("DOS",
// "SCUMMVM") or a NextUI "Name (TAG)" whose tag is a console's.
func shelfConsoleName(name string) (string, bool) {
	s, tagged, found := romFolderSystem(name, nil)
	if !found {
		return "", false
	}
	if tagged {
		_, tag, _ := nextuiFolderTag(name)
		if _, known := nextuiTagSystem(tag); !known {
			return "", false
		}
	}
	return s.Name, true
}

// shelfGameDir is the game folder path belongs to under consoleDir: the
// folder right under the console's, or for ScummVM (containers true) the one
// under a GAMES folder, CrossMix's place for the data its importer points at.
// top is true for a file straight in the console folder; gameDir is "" for a
// file in an artwork folder (Imgs, .media …) or a container's own files.
func shelfGameDir(consoleDir, path string, containers bool) (gameDir string, top bool) {
	rel, err := filepath.Rel(consoleDir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 1 {
		return "", true
	}
	for _, dir := range parts[:len(parts)-1] {
		if plugGameSkipDir[strings.ToLower(dir)] {
			return "", false
		}
	}
	if containers && strings.EqualFold(parts[0], "games") {
		if len(parts) < 3 {
			return "", false
		}
		return filepath.Join(consoleDir, parts[0], parts[1]), false
	}
	return filepath.Join(consoleDir, parts[0]), false
}

// shelfGameKey is the key a shelf game's settings and "seen" mark go by: its
// game folder, or the file itself when it is straight in the console folder.
func shelfGameKey(path string) string {
	consoleDir, system, ok := shelfConsoleOf(path)
	if !ok {
		return filepath.Clean(path)
	}
	if gameDir, _ := shelfGameDir(consoleDir, path, system == "ScummVM"); gameDir != "" {
		return gameDir
	}
	return filepath.Clean(path)
}

// shelfStem titles a file in a game folder after the folder; a file straight
// in the console folder keeps its own name (romGameStem's usual answer).
func shelfStem(path string) (string, bool) {
	consoleDir, system, ok := shelfConsoleOf(path)
	if !ok {
		return "", false
	}
	gameDir, top := shelfGameDir(consoleDir, path, system == "ScummVM")
	if top || gameDir == "" {
		return "", false
	}
	return filepath.Base(gameDir), true
}

// shelfSamePath compares two paths the way the card's file system does:
// Windows ignores case.
func shelfSamePath(a, b string) bool {
	if IsWindows() {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
