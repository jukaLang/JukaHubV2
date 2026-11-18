package main

// cheats.go — cheats for games started in RetroArch from the library.
//
// NextUI ships "cheats support"; JukaHub had only PPSSPP's own cheat switch
// (ppsspp_managed.go, untouched here). RetroArch already knows how to run
// cheats — what a handheld lacks is a way to pick them without digging
// through Quick Menu → Cheats → Load Cheat File in every game. So the game
// options sheet has a Cheats row (cheats_ui.go): it finds the game's file in
// libretro's cheat database (cheats_db.go), lists every cheat with an on/off
// switch, and remembers the switched-on ones per ROM in
// data/game-cheats.json:
//
//	{"schemaVersion":1,"games":{
//	   "/mnt/SDCARD/Roms/GBA/Metroid Fusion (USA).gba":{
//	      "source":"/mnt/SDCARD/Apps/JukaHub/data/cheats/cheats.zip",
//	      "entry":"Nintendo - Game Boy Advance/Metroid Fusion (USA) (Code Breaker).cht",
//	      "on":["Infinite Health\n8200xxxx 0063"]}}}
//
// A cheat is remembered by its description and code, not by its number, so an
// updated database that reorders a file keeps the user's choice.
//
// At launch (prepareCheats, called from launchLibraryGame) a RetroArch game
// with at least one cheat switched on gets, for that launch only:
//
//   - its game-specific cheat file, the one RetroArch loads by itself when a
//     game starts: <cheat_database_path>/<core's library_name>/<ROM name>.cht
//     (cheat_manager_load_game_specific_cheats in RetroArch's cheat_manager.c;
//     the same file Quick Menu → Cheats saves). It holds every cheat of the
//     chosen database file, the switched-on ones marked enabled, so the
//     in-game Cheats menu shows the full list too;
//   - apply_cheats_after_load = "true" (Auto-Apply Cheats During Game Load) and
//     apply_cheats_after_toggle = "true" through raScopeApply (racfgscope.go),
//     undone when the game exits like every other launch setting; and
//     cheat_database_path itself when retroarch.cfg leaves it empty, so the
//     file is where this RetroArch looks.
//
// The file is removed once RetroArch has exited, the way videofilter.go
// removes its one-launch shader preset: written behind a record in the data
// folder (cheats-pending.json), watched until RetroArch is gone, repaired at
// the next start if JukaHub died mid-game. RetroArch saves the game-specific
// file again when a game closes, which drops the marker line JukaHub writes, so
// a file also counts as JukaHub's when its cheats (descriptions and codes, in
// order) are exactly the ones written. A file the user changed in RetroArch's
// menu (a cheat added) is theirs and stays; a game-specific file the user had
// before is never overwritten — that launch runs with the user's file, and the
// sheet says so.
//
// With no cheat switched on, nothing at all is written or changed. Standalone
// emulators are skipped; the PPSSPP core does not read .cht files.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cheatsSchemaVersion = 1
	cheatsRecordName    = "game-cheats.json"
	cheatsPendingName   = "cheats-pending.json"

	// cheatsFileMarker is the first line of every game-specific file JukaHub
	// writes. RetroArch's config reader skips "#" lines.
	cheatsFileMarker = "# JukaHub: written for one game started from JukaHub; removed when RetroArch exits"

	// chtMaxBytes caps one .cht file: the largest in libretro's database are
	// a few hundred KB (big PlayStation lists); anything past 1 MiB is not one.
	chtMaxBytes = 1 << 20
	// chtMaxCheats bounds the cheat numbers the parser accepts, so a stray
	// "cheat999999999_desc" cannot make it allocate.
	chtMaxCheats = 4096
	// chtMaxFields bounds a cheat's other keys (RetroArch writes about twenty),
	// so a junk file of unique cheat0_* keys cannot make the parse quadratic.
	chtMaxFields = 64

	cheatsStartWait = 30 * time.Second
	cheatsWatchPoll = 2 * time.Second
	cheatsWatchMax  = 8 * time.Hour
)

// ── The .cht format ──────────────────────────────────────────────────────

// chtField is one of a cheat's other keys (handler, address, value, …)
// without its "cheatN_" prefix, kept so a written file loses nothing.
type chtField struct {
	Key   string
	Value string
}

// chtCheat is one cheat of a .cht file.
type chtCheat struct {
	Desc   string
	Code   string
	Enable bool
	Extra  []chtField
}

// key is how the record remembers a cheat: description and code.
func (c chtCheat) key() string {
	return c.Desc + "\n" + c.Code
}

// label is the description, or a numbered stand-in for a nameless cheat.
func (c chtCheat) label(i int) string {
	if d := strings.TrimSpace(c.Desc); d != "" {
		return d
	}
	return "Cheat " + strconv.Itoa(i+1)
}

// parseCht reads a RetroArch cheat file: "cheats = N" and cheatK_desc,
// cheatK_code, cheatK_enable and further cheatK_* keys. It is tolerant: the
// declared count is not trusted (several database files declare the wrong
// one), unknown lines and comments are skipped, a cheat is kept when it has a
// code or an address (RetroArch's own handler stores the address instead), and
// cheats come back in number order with the gaps closed.
func parseCht(data []byte) []chtCheat {
	if len(data) > chtMaxBytes {
		data = data[:chtMaxBytes]
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	byIndex := map[int]*chtCheat{}
	maxIdx := -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		if !strings.HasPrefix(key, "cheat") {
			continue
		}
		rest := key[len("cheat"):]
		us := strings.IndexByte(rest, '_')
		if us <= 0 || us > 5 {
			continue // "cheats = N", or not a cheat key
		}
		n, err := strconv.Atoi(rest[:us])
		if err != nil || n < 0 || n >= chtMaxCheats {
			continue
		}
		field := rest[us+1:]
		if field == "" {
			continue
		}
		value := chtValue(line[eq+1:])
		c := byIndex[n]
		if c == nil {
			c = &chtCheat{}
			byIndex[n] = c
		}
		switch field {
		case "desc":
			c.Desc = value
		case "code":
			c.Code = value
		case "enable":
			c.Enable = chtBool(value)
		default:
			replaced := false
			for i := range c.Extra {
				if c.Extra[i].Key == field {
					c.Extra[i].Value = value // a repeated key: the last one counts, as in RetroArch
					replaced = true
				}
			}
			if !replaced && len(c.Extra) < chtMaxFields {
				c.Extra = append(c.Extra, chtField{Key: field, Value: value})
			}
		}
		if n > maxIdx {
			maxIdx = n
		}
	}
	out := make([]chtCheat, 0, len(byIndex))
	for i := 0; i <= maxIdx; i++ {
		c := byIndex[i]
		if c == nil {
			continue
		}
		if strings.TrimSpace(c.Code) == "" && !c.hasAddress() {
			continue
		}
		out = append(out, *c)
	}
	return out
}

func (c chtCheat) hasAddress() bool {
	for _, f := range c.Extra {
		if f.Key == "address" && strings.TrimSpace(f.Value) != "" {
			return true
		}
	}
	return false
}

// chtValue is a value as RetroArch's config reader sees it: inside the quotes
// when quoted (to the last quote, so a stray one inside a description does not
// cut it short), else the bare word.
func chtValue(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		v = v[1:]
		if end := strings.LastIndexByte(v, '"'); end >= 0 {
			v = v[:end]
		}
		return strings.TrimSpace(v)
	}
	if i := strings.IndexByte(v, '#'); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

func chtBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// chtQuote makes a value safe inside RetroArch's quotes: no quote character
// (its reader has no escapes) and no line break.
func chtQuote(v string) string {
	return strings.NewReplacer(`"`, "'", "\r", " ", "\n", " ").Replace(v)
}

// chtFormat writes cheats as a game-specific file, numbered from 0, with the
// marker line on top.
func chtFormat(cheats []chtCheat) string {
	var b strings.Builder
	b.WriteString(cheatsFileMarker + "\n")
	b.WriteString("cheats = \"" + strconv.Itoa(len(cheats)) + "\"\n\n")
	for i, c := range cheats {
		p := "cheat" + strconv.Itoa(i) + "_"
		b.WriteString(p + "desc = \"" + chtQuote(c.Desc) + "\"\n")
		b.WriteString(p + "code = \"" + chtQuote(c.Code) + "\"\n")
		b.WriteString(p + "enable = \"" + strconv.FormatBool(c.Enable) + "\"\n")
		for _, f := range c.Extra {
			b.WriteString(p + f.Key + " = \"" + chtQuote(f.Value) + "\"\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// cheatsFingerprint identifies a list by its descriptions and codes in order,
// as they read back after chtQuote — the part RetroArch keeps when it saves
// the file again on exit.
func cheatsFingerprint(cheats []chtCheat) string {
	h := sha256.New()
	for _, c := range cheats {
		h.Write([]byte(strings.TrimSpace(chtQuote(c.Desc))))
		h.Write([]byte{0})
		h.Write([]byte(strings.TrimSpace(chtQuote(c.Code))))
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// ── The per-game record ──────────────────────────────────────────────────

// cheatsGameRecord is one ROM's choice: the database file the list came from
// (Source is a folder or a zip, Entry the file's slash path inside it), whether
// the user picked that file by hand, and the switched-on cheats.
type cheatsGameRecord struct {
	Source string   `json:"source,omitempty"`
	Entry  string   `json:"entry,omitempty"`
	Picked bool     `json:"picked,omitempty"`
	On     []string `json:"on,omitempty"`
}

func (r cheatsGameRecord) empty() bool {
	return len(r.On) == 0 && !r.Picked
}

type cheatsRecordFile struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Games         map[string]cheatsGameRecord `json:"games"`
}

// cheatsStore is the record in memory. Toggles change it at once (a launch
// reads it from here) and the file is written when the sheet closes, not on
// every press: one small write per visit to the card.
var cheatsStore struct {
	mu     sync.Mutex
	loaded bool
	dirty  bool
	games  map[string]cheatsGameRecord
}

func cheatsRecordPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, cheatsRecordName)
}

func cheatsEnsureLoadedLocked() {
	if cheatsStore.loaded {
		return
	}
	cheatsStore.loaded = true
	cheatsStore.games = map[string]cheatsGameRecord{}
	path := cheatsRecordPath()
	if path == "" {
		return
	}
	var file cheatsRecordFile
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn("cheats", "ignoring %s: %v", path, err)
		}
		return
	}
	for rom, rec := range file.Games {
		key := gameOptionsKey(rom)
		if key == "" || rec.empty() {
			continue
		}
		cheatsStore.games[key] = rec
	}
}

// cheatsRecordFor is one ROM's record.
func cheatsRecordFor(romPath string) (cheatsGameRecord, bool) {
	key := gameOptionsKey(romPath)
	if key == "" {
		return cheatsGameRecord{}, false
	}
	cheatsStore.mu.Lock()
	defer cheatsStore.mu.Unlock()
	cheatsEnsureLoadedLocked()
	rec, ok := cheatsStore.games[key]
	return rec, ok
}

// cheatsSetRecord stores one ROM's record in memory; cheatsFlush writes it.
func cheatsSetRecord(romPath string, rec cheatsGameRecord) {
	key := gameOptionsKey(romPath)
	if key == "" {
		return
	}
	cheatsStore.mu.Lock()
	defer cheatsStore.mu.Unlock()
	cheatsEnsureLoadedLocked()
	if rec.empty() {
		if _, had := cheatsStore.games[key]; !had {
			return
		}
		delete(cheatsStore.games, key)
	} else {
		cheatsStore.games[key] = rec
	}
	cheatsStore.dirty = true
}

// cheatsFlush writes the record when it changed.
func cheatsFlush() {
	cheatsStore.mu.Lock()
	if !cheatsStore.dirty {
		cheatsStore.mu.Unlock()
		return
	}
	file := cheatsRecordFile{SchemaVersion: cheatsSchemaVersion, Games: make(map[string]cheatsGameRecord, len(cheatsStore.games))}
	for k, v := range cheatsStore.games {
		file.Games[k] = v
	}
	cheatsStore.dirty = false
	cheatsStore.mu.Unlock()

	path := cheatsRecordPath()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = AtomicWrite(path, append(data, '\n'), 0o644)
	}
	if err != nil {
		logWarn("cheats", "cannot save the cheat choices: %v", err)
		ShowToast("Cannot save the cheats: "+firstLine(err.Error()), ToastKindWarning)
		cheatsStore.mu.Lock()
		cheatsStore.dirty = true
		cheatsStore.mu.Unlock()
	}
}

// cheatsEnabledCount is how many cheats are on for a ROM (the options row).
func cheatsEnabledCount(romPath string) int {
	rec, ok := cheatsRecordFor(romPath)
	if !ok {
		return 0
	}
	return len(rec.On)
}

// cheatsApplyRecord marks the cheats the record switched on; it returns how
// many it found.
func cheatsApplyRecord(cheats []chtCheat, on []string) int {
	want := make(map[string]bool, len(on))
	for _, k := range on {
		want[k] = true
	}
	n := 0
	for i := range cheats {
		cheats[i].Enable = want[cheats[i].key()]
		if cheats[i].Enable {
			n++
		}
	}
	return n
}

// ── Launch ───────────────────────────────────────────────────────────────

// cheatsCoreFolder is the folder RetroArch names after the core a launcher
// starts (its library_name), "" when JukaHub cannot tell or the core does not
// read .cht files.
func cheatsCoreFolder(launcher, system string) string {
	core := gameCoreFromLauncher(launcher, system)
	if core == "" || core == "ppsspp" {
		return ""
	}
	folders := resetCoreFolders(core)
	if len(folders) == 0 {
		return ""
	}
	return folders[0]
}

// cheatsLaunchDir is the cheat folder a retroarch.cfg uses, and whether the
// launch has to set cheat_database_path because the file leaves it empty
// (RetroArch's built-in default differs between builds).
func cheatsLaunchDir(cfgText, cfgPath string) (string, bool) {
	if v, ok := retroArchConfigValue(cfgText, "cheat_database_path"); ok {
		if dir := resolveRetroArchSaveDir(v, cfgPath); dir != "" {
			return dir, false
		}
	}
	return filepath.Join(filepath.Dir(cfgPath), "cheats"), true
}

// prepareCheats is the launch-time entry point (game_library.go). It never
// blocks a launch: whatever fails is logged and the game starts without the
// cheats. With none switched on it does nothing at all.
func prepareCheats(launcher string, game libraryGame) {
	rec, ok := cheatsRecordFor(game.Path)
	if !ok || len(rec.On) == 0 {
		return
	}
	if !retroArchLauncher(launcher) {
		logDebug("cheats", "%s does not start RetroArch; its cheats are not applied", filepath.Base(launcher))
		return
	}
	stem := gameResetStem(game.Path)
	folder := cheatsCoreFolder(launcher, game.System)
	if stem == "" || folder == "" {
		logWarn("cheats", "cannot tell which core plays %s; launching without cheats", game.Path)
		return
	}
	data, err := cheatsReadRef(cheatRef{Source: rec.Source, Entry: rec.Entry})
	if err != nil {
		logWarn("cheats", "cannot read the cheat file %s: %v; launching without cheats", rec.Entry, err)
		return
	}
	cheats := parseCht(data)
	if cheatsApplyRecord(cheats, rec.On) == 0 {
		logWarn("cheats", "the cheats switched on for %s are no longer in %s; launching without cheats", game.Path, rec.Entry)
		return
	}
	text := chtFormat(cheats)
	fp := cheatsFingerprint(cheats)
	written := map[string]bool{}
	for _, cfg := range raScopeTargets(raLaunchCandidates(launcher, retroArchConfigCandidates())) {
		raw, err := os.ReadFile(cfg)
		if err != nil {
			continue
		}
		dir, setDir := cheatsLaunchDir(string(raw), cfg)
		path := filepath.Join(dir, folder, stem+".cht")
		key := strings.ToLower(filepath.Clean(path))
		if !written[key] {
			if !cheatsWriteGameFile(path, text, fp) {
				continue
			}
			written[key] = true
		}
		values := []raScopeValue{
			{Key: "apply_cheats_after_load", Value: "true"},
			{Key: "apply_cheats_after_toggle", Value: "true"},
		}
		if setDir {
			values = append(values, raScopeValue{Key: "cheat_database_path", Value: dir})
		}
		changed, err := raScopeApply(cfg, values)
		if err != nil {
			logWarn("cheats", "cannot switch on auto-applied cheats in %s: %v", cfg, err)
			continue
		}
		if changed {
			Log().Info("cheats applied for this launch", "file", path, "config", cfg)
		}
	}
}

// ── The one-launch game-specific file ────────────────────────────────────

// cheatsPending guards the record of written files and the watcher that
// removes them (videofilter.go's videoFilterPending, for cheat files).
var cheatsPending struct {
	mu      sync.Mutex
	gen     int
	running bool
}

type cheatsPendingEntry struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
}

type cheatsPendingFile struct {
	Files []cheatsPendingEntry `json:"files"`
}

func cheatsPendingPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, cheatsPendingName)
}

func cheatsPendingLoadLocked() []cheatsPendingEntry {
	path := cheatsPendingPath()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		return nil
	}
	var file cheatsPendingFile
	if json.Unmarshal(raw, &file) != nil {
		return nil
	}
	return file.Files
}

func cheatsPendingSaveLocked(files []cheatsPendingEntry) error {
	path := cheatsPendingPath()
	if path == "" {
		if len(files) == 0 {
			return nil
		}
		return os.ErrNotExist
	}
	if len(files) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(cheatsPendingFile{Files: files}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, append(data, '\n'), 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// cheatsOurFile reports whether a game-specific file is one JukaHub wrote:
// it still carries the marker, or RetroArch saved it again on exit with
// exactly the cheats written (fp).
func cheatsOurFile(raw []byte, fp string) bool {
	if strings.HasPrefix(strings.TrimPrefix(string(raw), "\ufeff"), cheatsFileMarker) {
		return true
	}
	return fp != "" && cheatsFingerprint(parseCht(raw)) == fp
}

// cheatsReadSmall reads a game-specific file, at most one byte past
// chtMaxBytes: a larger one is not JukaHub's (its fingerprint cannot match).
func cheatsReadSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, chtMaxBytes+1))
}

// cheatsUserFile reports whether path is a game-specific cheat file JukaHub
// did not write (the user saved cheats in RetroArch's menu): it is kept, and
// that launch runs with it.
func cheatsUserFile(path string) bool {
	raw, err := cheatsReadSmall(path)
	if err != nil {
		return false
	}
	cheatsPending.mu.Lock()
	defer cheatsPending.mu.Unlock()
	for _, e := range cheatsPendingLoadLocked() {
		if strings.EqualFold(filepath.Clean(e.Path), filepath.Clean(path)) && cheatsOurFile(raw, e.Fingerprint) {
			return false
		}
	}
	return !strings.HasPrefix(strings.TrimPrefix(string(raw), "\ufeff"), cheatsFileMarker)
}

// cheatsWriteGameFile writes the game-specific file for one launch. It is
// recorded before it is written, so a power cut in between leaves something
// to clean up rather than cheats on every firmware launch after.
func cheatsWriteGameFile(path, text, fp string) bool {
	if cheatsUserFile(path) {
		logInfo("cheats", "%s is the user's own RetroArch cheat file; it is kept and used for this launch", path)
		return false
	}
	cheatsPending.mu.Lock()
	defer cheatsPending.mu.Unlock()
	files := cheatsPendingLoadLocked()
	listed := false
	for i := range files {
		if strings.EqualFold(filepath.Clean(files[i].Path), filepath.Clean(path)) {
			files[i].Fingerprint = fp
			listed = true
		}
	}
	if !listed {
		files = append(files, cheatsPendingEntry{Path: path, Fingerprint: fp})
	}
	if err := cheatsPendingSaveLocked(files); err != nil {
		logWarn("cheats", "cannot record the one-launch cheat file (%v); launching without cheats", err)
		return false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logWarn("cheats", "cannot create %s: %v", filepath.Dir(path), err)
		return false
	}
	tmp := path + ".jukahub-tmp"
	if err := writeFileSynced(tmp, []byte(text), 0o644); err != nil {
		_ = os.Remove(tmp)
		logWarn("cheats", "cannot write %s: %v", path, err)
		return false
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		logWarn("cheats", "cannot write %s: %v", path, err)
		return false
	}
	cheatsPending.gen++
	if !cheatsPending.running {
		cheatsPending.running = true
		go cheatsWatchExit()
	}
	return true
}

// cheatsCleanupLocked removes every recorded file that is still JukaHub's. A
// file the user changed in RetroArch's cheat menu is theirs now and stays. The
// core folder goes too when the file was the last thing in it.
func cheatsCleanupLocked() {
	files := cheatsPendingLoadLocked()
	if len(files) == 0 {
		return
	}
	var keep []cheatsPendingEntry
	for _, e := range files {
		raw, err := cheatsReadSmall(e.Path)
		if err != nil {
			if !os.IsNotExist(err) {
				keep = append(keep, e) // a card not mounted yet: try again later
			}
			continue
		}
		if !cheatsOurFile(raw, e.Fingerprint) {
			logInfo("cheats", "%s was changed in RetroArch; it is the user's now and stays", e.Path)
			continue
		}
		if err := os.Remove(e.Path); err != nil && !os.IsNotExist(err) {
			logWarn("cheats", "cannot remove the one-launch cheat file %s: %v", e.Path, err)
			keep = append(keep, e)
			continue
		}
		_ = os.Remove(filepath.Dir(e.Path)) // only succeeds when empty
		logDebug("cheats", "removed the one-launch cheat file %s", e.Path)
	}
	if err := cheatsPendingSaveLocked(keep); err != nil {
		logWarn("cheats", "cannot update the cheat file record: %v", err)
	}
}

// cheatsCleanup is the startup repair: files a crash or power cut left behind
// go, so a game started from the firmware has no cheats it did not ask for.
func cheatsCleanup() {
	if retroArchRunning() {
		return // JukaHub restarted under a live game: the next launch's watcher cleans up
	}
	cheatsPending.mu.Lock()
	defer cheatsPending.mu.Unlock()
	if cheatsPending.running {
		return // a launch is in flight; its watcher cleans up
	}
	cheatsCleanupLocked()
}

// cheatsWatchExit waits for the game to take the screen and then for it, and
// RetroArch itself, to be gone — RetroArch saves the file again as it closes
// the game — then removes the file. A launch that never takes the screen is
// over after cheatsStartWait.
func cheatsWatchExit() {
	for {
		cheatsPending.mu.Lock()
		gen := cheatsPending.gen
		cheatsPending.mu.Unlock()

		start := time.Now()
		for !gameScreenSuspended() && !retroArchRunning() && time.Since(start) < cheatsStartWait {
			time.Sleep(cheatsWatchPoll / 4)
		}
		for (gameScreenSuspended() || retroArchRunning()) && time.Since(start) < cheatsWatchMax {
			time.Sleep(cheatsWatchPoll)
		}

		cheatsPending.mu.Lock()
		if gen != cheatsPending.gen {
			cheatsPending.mu.Unlock()
			continue // a newer launch wrote a file meanwhile: wait for that one
		}
		cheatsCleanupLocked()
		cheatsPending.running = false
		cheatsPending.mu.Unlock()
		return
	}
}

func init() {
	// After the first frame, beside the video filter's preset repair.
	deferStartupTask("cheats", func() { go cheatsCleanup() })
}
