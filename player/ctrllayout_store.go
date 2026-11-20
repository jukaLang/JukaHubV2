package main

// ctrllayout_store.go — which control layout each game and each console uses,
// and the layouts the player writes themselves.
//
// The choices live in <data>/game-layouts.json, beside game-controls.json
// (the Buttons and Turbo choices, racontrols.go) and in the same shape:
//
//	{"schemaVersion":1,
//	 "games":{"/mnt/SDCARD/Roms/FBNEO/sfa3.zip":"arcade-6","/mnt/SDCARD/Roms/NDS/x.nds":"none"},
//	 "consoles":{"neo geo":"arcade-4-neogeo"}}
//
// A game's value is a layout id, or "none" for no layout even where its
// console has one; a game that is not listed follows its console. A console's
// value (keyed by the library system name in lower case) is a layout id or
// "none"; a console that is not listed uses the layout registered as its
// default (ctrlLayoutDefaultFor), if any. An id whose layout is gone — a
// deleted file of the player's — reads as not chosen. A missing or broken file
// is "nothing chosen"; a file from a newer JukaHub is read as nothing chosen
// and never written over.
//
// The player's own layouts are JSON files in <data>/control-layouts/, read on
// a goroutine after the first frame and again when the picker opens (so a
// file copied over USB shows up without a restart); a launch before that first
// read has finished reads them itself. The format is documented
// in docs/CONTROLS.md:
//
//	{"schemaVersion":1, "id":"kof-mine", "name":"KOF my way",
//	 "description":"…", "systems":["Neo Geo"],
//	 "buttons":{"y":"b","b":"a"}, "keys":{"start":"enter"},
//	 "coreOptions":{"melonds_touch_mode":"Joystick"},
//	 "variants":[{"cores":["mame2003_plus"],"noSticks":false,"buttons":{…},"coreOptions":{…},"chart":[…]}],
//	 "chart":[{"button":"Y","action":"Light punch"}]}
//
// Every value is checked: a file over 32 KB, one that does not parse, or one
// whose id is taken by a built-in layout is skipped; an unknown button or key
// name, or a core option that could break RetroArch's file (quotes, line
// breaks), is dropped and named in the log. The rest of the file still loads.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

const (
	ctrlLayoutSchemaVersion = 1
	ctrlLayoutRecordName    = "game-layouts.json"
	ctrlLayoutUserDirName   = "control-layouts"
	// ctrlLayoutNone is the stored choice for "no layout".
	ctrlLayoutNone = "none"
	// ctrlLayoutSourceUser marks a layout read from the player's files.
	ctrlLayoutSourceUser = "user"

	// Bounds on the player's files.
	ctrlLayoutUserFileMax   = 32 << 10
	ctrlLayoutUserFilesMax  = 64
	ctrlLayoutUserVariants  = 8
	ctrlLayoutUserChartMax  = 24
	ctrlLayoutUserOptionMax = 32
	ctrlLayoutUserProblems  = 12
)

// ── Choices (game-layouts.json) ──────────────────────────────────────────

type ctrlLayoutRecord struct {
	SchemaVersion int               `json:"schemaVersion"`
	Games         map[string]string `json:"games"`
	Consoles      map[string]string `json:"consoles"`
}

// ctrlLayoutStore is the record in memory, plus what the last launch of a
// game could not do (shown in the picker; not saved).
var ctrlLayoutStore struct {
	mu       sync.Mutex
	loaded   bool
	readOnly bool // the file is from a newer JukaHub
	games    map[string]string
	consoles map[string]string
	notes    map[string]string
}

// ctrlLayoutStorePath is the record in JukaHub's data folder; a variable for
// tests.
var ctrlLayoutStorePath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, ctrlLayoutRecordName)
}

// ctrlLayoutConsoleKey is the key a console's choice is stored under.
func ctrlLayoutConsoleKey(system string) string {
	return strings.ToLower(strings.TrimSpace(system))
}

// ctrlLayoutCleanChoice keeps a stored value that can be an id: lower case,
// trimmed, "" for anything else.
func ctrlLayoutCleanChoice(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if !ctrlLayoutValidID(v) {
		return ""
	}
	return v
}

func ctrlLayoutStoreLoadLocked() {
	if ctrlLayoutStore.loaded {
		return
	}
	ctrlLayoutStore.loaded = true
	ctrlLayoutStore.games = map[string]string{}
	ctrlLayoutStore.consoles = map[string]string{}
	path := ctrlLayoutStorePath()
	if path == "" {
		return
	}
	var file ctrlLayoutRecord
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn("controls", "ignoring %s: %v", path, err)
		}
		return
	}
	if file.SchemaVersion > ctrlLayoutSchemaVersion {
		logWarn("controls", "%s is from a newer JukaHub (schema %d); no layouts chosen, and the file is left as it is", path, file.SchemaVersion)
		ctrlLayoutStore.readOnly = true
		return
	}
	for rom, id := range file.Games {
		if key, id := gameOptionsKey(rom), ctrlLayoutCleanChoice(id); key != "" && id != "" {
			ctrlLayoutStore.games[key] = id
		}
	}
	for system, id := range file.Consoles {
		if key, id := ctrlLayoutConsoleKey(system), ctrlLayoutCleanChoice(id); key != "" && id != "" {
			ctrlLayoutStore.consoles[key] = id
		}
	}
}

// ctrlLayoutStoreLoad reads the record if it has not been read (any thread).
func ctrlLayoutStoreLoad() {
	ctrlLayoutStore.mu.Lock()
	ctrlLayoutStoreLoadLocked()
	ctrlLayoutStore.mu.Unlock()
}

// ctrlLayoutStoreLoaded reports whether the record is in memory: the game
// page's chip answers only then, never from the disk.
func ctrlLayoutStoreLoaded() bool {
	ctrlLayoutStore.mu.Lock()
	defer ctrlLayoutStore.mu.Unlock()
	return ctrlLayoutStore.loaded
}

// ctrlLayoutGameChoice is a game's own choice: "" (its console's), "none" or
// a layout id.
func ctrlLayoutGameChoice(romPath string) string {
	key := ctrlLayoutGameKey(romPath)
	if key == "" {
		return ""
	}
	ctrlLayoutStore.mu.Lock()
	defer ctrlLayoutStore.mu.Unlock()
	ctrlLayoutStoreLoadLocked()
	return ctrlLayoutStore.games[key]
}

// ctrlLayoutConsoleChoice is a console's choice: "" (its default), "none" or
// a layout id.
func ctrlLayoutConsoleChoice(system string) string {
	key := ctrlLayoutConsoleKey(system)
	if key == "" {
		return ""
	}
	ctrlLayoutStore.mu.Lock()
	defer ctrlLayoutStore.mu.Unlock()
	ctrlLayoutStoreLoadLocked()
	return ctrlLayoutStore.consoles[key]
}

// ctrlLayoutSnapshotLocked is the record as it is written.
func ctrlLayoutSnapshotLocked() ctrlLayoutRecord {
	file := ctrlLayoutRecord{
		SchemaVersion: ctrlLayoutSchemaVersion,
		Games:         make(map[string]string, len(ctrlLayoutStore.games)),
		Consoles:      make(map[string]string, len(ctrlLayoutStore.consoles)),
	}
	for k, v := range ctrlLayoutStore.games {
		file.Games[k] = v
	}
	for k, v := range ctrlLayoutStore.consoles {
		file.Consoles[k] = v
	}
	return file
}

// ctrlLayoutStoreChange edits the record under the lock and writes it whole
// (it is small). It reports whether the change was saved.
func ctrlLayoutStoreChange(change func() bool) bool {
	ctrlLayoutStore.mu.Lock()
	ctrlLayoutStoreLoadLocked()
	if ctrlLayoutStore.readOnly {
		ctrlLayoutStore.mu.Unlock()
		ShowToast("Control layouts were saved by a newer JukaHub · update to change them", ToastKindWarning)
		return false
	}
	if !change() {
		ctrlLayoutStore.mu.Unlock()
		return true
	}
	file := ctrlLayoutSnapshotLocked()
	ctrlLayoutStore.mu.Unlock()

	path := ctrlLayoutStorePath()
	if path == "" {
		return false
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = AtomicWrite(path, append(data, '\n'), 0o644)
	}
	if err != nil {
		logWarn("controls", "cannot save the control layouts: %v", err)
		ShowToast("Cannot save the layout: "+firstLine(err.Error()), ToastKindWarning)
		return false
	}
	return true
}

// ctrlLayoutGameKey keys a game's own choice: its file, or for a DOS or
// ScummVM folder game its folder, so a new start program keeps the choice
// (shelfGameKey).
func ctrlLayoutGameKey(romPath string) string {
	if strings.TrimSpace(romPath) == "" {
		return ""
	}
	return gameOptionsKey(shelfGameKey(romPath))
}

// ctrlLayoutSetGame stores a game's own choice ("" follows its console).
func ctrlLayoutSetGame(romPath, id string) bool {
	key := ctrlLayoutGameKey(romPath)
	if key == "" {
		return false
	}
	id = ctrlLayoutCleanChoice(id)
	return ctrlLayoutStoreChange(func() bool {
		if ctrlLayoutStore.games[key] == id {
			return false
		}
		if id == "" {
			delete(ctrlLayoutStore.games, key)
		} else {
			ctrlLayoutStore.games[key] = id
		}
		return true
	})
}

// ctrlLayoutSetConsole stores a console's choice ("" back to its default) and
// clears own choices listed in games, so the game the picker was opened for
// follows what was just picked.
func ctrlLayoutSetConsole(system, id string, games ...string) bool {
	key := ctrlLayoutConsoleKey(system)
	if key == "" {
		return false
	}
	id = ctrlLayoutCleanChoice(id)
	return ctrlLayoutStoreChange(func() bool {
		changed := ctrlLayoutStore.consoles[key] != id
		if id == "" {
			delete(ctrlLayoutStore.consoles, key)
		} else {
			ctrlLayoutStore.consoles[key] = id
		}
		for _, rom := range games {
			if k := ctrlLayoutGameKey(rom); k != "" {
				if _, own := ctrlLayoutStore.games[k]; own {
					delete(ctrlLayoutStore.games, k)
					changed = true
				}
			}
		}
		return changed
	})
}

// ctrlLayoutForgetGame drops a game's own choice (Reset game, Delete game;
// called from gameControlsForget). The file is written only when it had one.
func ctrlLayoutForgetGame(romPath string) {
	ctrlLayoutSetNote(romPath, "")
	if ctrlLayoutGameChoice(romPath) != "" {
		ctrlLayoutSetGame(romPath, "")
	}
}

// ctrlLayoutMoveGame carries a game's own choice to its new file (Rename
// file, the Game Boy Color move; called from gameControlsMove).
func ctrlLayoutMoveGame(oldPath, newPath string) {
	oldKey, newKey := ctrlLayoutGameKey(oldPath), ctrlLayoutGameKey(newPath)
	if oldKey == "" || newKey == "" || oldKey == newKey {
		return
	}
	ctrlLayoutSetNote(oldPath, "")
	if ctrlLayoutGameChoice(oldPath) == "" {
		return
	}
	ctrlLayoutStoreChange(func() bool {
		id, ok := ctrlLayoutStore.games[oldKey]
		if !ok {
			return false
		}
		delete(ctrlLayoutStore.games, oldKey)
		ctrlLayoutStore.games[newKey] = id
		return true
	})
}

// ctrlLayoutNote is what the last launch of a game could not apply, "" when
// it all went in.
func ctrlLayoutNote(romPath string) string {
	ctrlLayoutStore.mu.Lock()
	defer ctrlLayoutStore.mu.Unlock()
	return ctrlLayoutStore.notes[gameOptionsKey(romPath)]
}

func ctrlLayoutSetNote(romPath, note string) {
	key := gameOptionsKey(romPath)
	if key == "" {
		return
	}
	ctrlLayoutStore.mu.Lock()
	defer ctrlLayoutStore.mu.Unlock()
	if ctrlLayoutStore.notes == nil {
		ctrlLayoutStore.notes = map[string]string{}
	}
	if note == "" {
		delete(ctrlLayoutStore.notes, key)
		return
	}
	ctrlLayoutStore.notes[key] = note
}

// ── What a game uses ─────────────────────────────────────────────────────

// ctrlLayoutForGame is the layout game is played with: its own choice, else
// its console's choice, else the layout registered as its system's default.
// ok is false when none applies (or "None" was picked). The DOS and ScummVM
// shelves and the manual's Controls page call it too.
func ctrlLayoutForGame(game libraryGame) (ctrlLayout, bool) {
	switch id := ctrlLayoutGameChoice(game.Path); id {
	case ctrlLayoutNone:
		return ctrlLayout{}, false
	case "":
	default:
		if l, ok := ctrlLayoutByID(id); ok {
			return l, true
		}
	}
	return ctrlLayoutForConsole(game.System)
}

// ctrlLayoutForConsole is the layout a console's games use unless a game has
// its own choice.
func ctrlLayoutForConsole(system string) (ctrlLayout, bool) {
	switch id := ctrlLayoutConsoleChoice(system); id {
	case ctrlLayoutNone:
		return ctrlLayout{}, false
	case "":
	default:
		if l, ok := ctrlLayoutByID(id); ok {
			return l, true
		}
	}
	return ctrlLayoutDefaultFor(system)
}

// ── The player's layouts (<data>/control-layouts/*.json) ─────────────────

type ctrlLayoutUserLine struct {
	Button string `json:"button"`
	Action string `json:"action"`
}

type ctrlLayoutUserVariant struct {
	Cores       []string             `json:"cores,omitempty"`
	NoSticks    bool                 `json:"noSticks,omitempty"`
	Buttons     map[string]string    `json:"buttons,omitempty"`
	CoreOptions map[string]string    `json:"coreOptions,omitempty"`
	Chart       []ctrlLayoutUserLine `json:"chart,omitempty"`
}

type ctrlLayoutUserFile struct {
	SchemaVersion int                     `json:"schemaVersion"`
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	Description   string                  `json:"description,omitempty"`
	Systems       []string                `json:"systems,omitempty"`
	DefaultFor    []string                `json:"defaultFor,omitempty"`
	Buttons       map[string]string       `json:"buttons,omitempty"`
	Keys          map[string]string       `json:"keys,omitempty"`
	CoreOptions   map[string]string       `json:"coreOptions,omitempty"`
	Variants      []ctrlLayoutUserVariant `json:"variants,omitempty"`
	Chart         []ctrlLayoutUserLine    `json:"chart,omitempty"`
}

// ctrlLayoutValidID reports whether id can name a layout: 1–48 of a-z, 0-9,
// '-', '_' and '.', starting with a letter or digit.
func ctrlLayoutValidID(id string) bool {
	if id == "" || len(id) > 48 {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '-' || r == '_' || r == '.'):
		default:
			return false
		}
	}
	return true
}

// ctrlLayoutClip trims s and cuts it to at most n runes, on a rune boundary.
func ctrlLayoutClip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ctrlLayoutSafeOption reports whether a core option key and value can go
// into RetroArch's options file as `key = "value"` without breaking it.
func ctrlLayoutSafeOption(key, value string) bool {
	if key == "" || len(key) > 96 || len(value) > 128 {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return !strings.ContainsAny(value, "\"\r\n\x00")
}

// ctrlLayoutParseUser turns one of the player's files into a layout. stem is
// the file name without .json (the id when the file has none). problems are
// what was dropped, for the log. Pure.
func ctrlLayoutParseUser(data []byte, stem string) (l ctrlLayout, problems []string, err error) {
	if len(data) > ctrlLayoutUserFileMax {
		return l, nil, fmt.Errorf("larger than %d KB", ctrlLayoutUserFileMax>>10)
	}
	var f ctrlLayoutUserFile
	if err := json.Unmarshal(data, &f); err != nil {
		return l, nil, err
	}
	if f.SchemaVersion > ctrlLayoutSchemaVersion {
		return l, nil, fmt.Errorf("made for a newer JukaHub (schemaVersion %d)", f.SchemaVersion)
	}
	id := strings.ToLower(strings.TrimSpace(f.ID))
	if id == "" {
		id = raTuneSlug(stem)
	}
	if !ctrlLayoutValidID(id) || id == ctrlLayoutNone {
		return l, nil, fmt.Errorf("id %q: use up to 48 lower-case letters, digits and dashes", id)
	}
	l.ID = id
	l.Name = ctrlLayoutClip(f.Name, 48)
	if l.Name == "" {
		l.Name = id
	}
	l.Description = ctrlLayoutClip(f.Description, 120)
	clipList := func(list []string) []string {
		var out []string
		for _, s := range list {
			if s = ctrlLayoutClip(s, 40); s != "" && len(out) < 16 {
				out = append(out, s)
			}
		}
		return out
	}
	l.Systems = clipList(f.Systems)
	l.DefaultFor = clipList(f.DefaultFor)

	problem := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	buttons := func(in map[string]string, where string) map[string]string {
		if len(in) == 0 {
			return nil
		}
		out := map[string]string{}
		for from, to := range in {
			pad, sends := strings.ToLower(strings.TrimSpace(from)), strings.ToLower(strings.TrimSpace(to))
			if raRemapButtonID(pad) < 0 {
				problem("%s: unknown pad button %q", where, from)
				continue
			}
			if _, ok := ctrlLayoutTarget(sends); !ok {
				problem("%s: unknown button %q for %s", where, to, from)
				continue
			}
			out[pad] = sends
		}
		return out
	}
	options := func(in map[string]string, where string) map[string]string {
		if len(in) == 0 {
			return nil
		}
		out := map[string]string{}
		for k, v := range in {
			k = strings.TrimSpace(k)
			if len(out) >= ctrlLayoutUserOptionMax || !ctrlLayoutSafeOption(k, v) {
				problem("%s: core option %q skipped", where, k)
				continue
			}
			out[k] = v
		}
		return out
	}
	chart := func(in []ctrlLayoutUserLine) []ctrlLayoutLine {
		var out []ctrlLayoutLine
		for _, line := range in {
			b, a := ctrlLayoutClip(line.Button, 24), ctrlLayoutClip(line.Action, 60)
			if b == "" || a == "" || len(out) >= ctrlLayoutUserChartMax {
				continue
			}
			out = append(out, ctrlLayoutLine{Button: b, Action: a})
		}
		return out
	}
	l.Buttons = buttons(f.Buttons, "buttons")
	l.CoreOptions = options(f.CoreOptions, "coreOptions")
	l.Chart = chart(f.Chart)
	if len(f.Keys) > 0 {
		l.Keys = map[string]string{}
		for from, key := range f.Keys {
			fb, k := strings.ToLower(strings.TrimSpace(from)), strings.ToLower(strings.TrimSpace(key))
			if raRemapButtonID(fb) < 0 {
				problem("keys: unknown pad button %q", from)
				continue
			}
			if _, ok := ctrlLayoutKeyCode(k); !ok {
				problem("keys: unknown key %q for %s", key, from)
				continue
			}
			l.Keys[fb] = k
		}
	}
	for i, v := range f.Variants {
		if i >= ctrlLayoutUserVariants {
			problem("variants after the first %d skipped", ctrlLayoutUserVariants)
			break
		}
		where := fmt.Sprintf("variants[%d]", i)
		var cores []string
		for _, c := range v.Cores {
			c = normalizeCoreName(c)
			if c != "" && len(cores) < 16 {
				cores = append(cores, c)
			}
		}
		l.Variants = append(l.Variants, ctrlLayoutVariant{
			Cores:       cores,
			NoSticks:    v.NoSticks,
			Buttons:     buttons(v.Buttons, where+".buttons"),
			CoreOptions: options(v.CoreOptions, where+".coreOptions"),
			Chart:       chart(v.Chart),
		})
	}
	return l, problems, nil
}

// ctrlLayoutUserMu keeps one read of the folder at a time (a second caller
// waits for it); ctrlLayoutUserDone is set once a read has finished.
var (
	ctrlLayoutUserMu   sync.Mutex
	ctrlLayoutUserDone atomic.Bool
)

// ctrlLayoutUserEnsure reads the player's files now when no read has finished
// yet: a game resumed at power-on starts right after the startup queue, while
// the startup read may still be running, and must not lose its own layout.
func ctrlLayoutUserEnsure() {
	if !ctrlLayoutUserDone.Load() {
		ctrlLayoutLoadUser()
	}
}

// ctrlLayoutUserDir is the folder of the player's layouts; a variable for
// tests.
var ctrlLayoutUserDir = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, ctrlLayoutUserDirName)
}

// ctrlLayoutLoadUser reads the player's layout files and registers them. It
// reports how many layouts it registered. Goroutine: it reads the card (the
// launch path calls it once through ctrlLayoutUserEnsure).
func ctrlLayoutLoadUser() int {
	ctrlLayoutUserMu.Lock()
	defer ctrlLayoutUserMu.Unlock()
	defer ctrlLayoutUserDone.Store(true)
	defer func() {
		if r := recover(); r != nil {
			logWarn("controls", "reading the control layout files failed: %v", r)
		}
	}()
	dir := ctrlLayoutUserDir()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			logWarn("controls", "cannot read %s: %v", dir, err)
		}
		return 0
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(name), ".json") && !strings.HasPrefix(name, ".") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > ctrlLayoutUserFilesMax {
		logWarn("controls", "%s has %d layout files; reading the first %d", dir, len(names), ctrlLayoutUserFilesMax)
		names = names[:ctrlLayoutUserFilesMax]
	}
	count := 0
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := ctrlLayoutReadSmall(path)
		if err != nil {
			logWarn("controls", "skipping layout file %s: %v", name, err)
			continue
		}
		l, problems, err := ctrlLayoutParseUser(data, strings.TrimSuffix(name, filepath.Ext(name)))
		if err != nil {
			logWarn("controls", "skipping layout file %s: %v", name, err)
			continue
		}
		for i, p := range problems {
			if i == ctrlLayoutUserProblems {
				logWarn("controls", "layout file %s: %d more problems", name, len(problems)-i)
				break
			}
			logWarn("controls", "layout file %s: %s", name, p)
		}
		if have, ok := ctrlLayoutByID(l.ID); ok && have.Source != ctrlLayoutSourceUser {
			logWarn("controls", "skipping layout file %s: the id %q is a built-in layout's; give the file's layout its own id", name, l.ID)
			continue
		}
		l.Source = ctrlLayoutSourceUser
		ctrlLayoutRegister(l)
		count++
	}
	if count > 0 {
		logInfo("controls", "%d control layouts read from %s", count, dir)
	}
	return count
}

// ctrlLayoutReadSmall reads a layout file, refusing one over the size cap.
func ctrlLayoutReadSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, ctrlLayoutUserFileMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > ctrlLayoutUserFileMax {
		return nil, fmt.Errorf("larger than %d KB", ctrlLayoutUserFileMax>>10)
	}
	return data, nil
}

func init() {
	// After the first frame: the choices, then the player's layouts.
	deferStartupTask("control-layouts", func() {
		go func() {
			ctrlLayoutStoreLoad()
			ctrlLayoutLoadUser()
		}()
	})
}
