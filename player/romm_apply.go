package main

// romm_apply.go — carrying out a ROM manager plan, and putting it back.
//
// Moves never overwrite: a destination that exists gets a " (2)" name instead
// (romm_plan.go chose it, and it is checked again here in case the card changed
// in between). A move inside one card is a rename; across drives (a PC's
// Downloads to the library on another disk) the file is copied, the copy read
// back and compared, and only then is the original removed.
//
// Every run is written to a journal (data/rom-manager/journal.json) as it goes,
// a few moves at a time, so "Undo last organise" works even after a run that
// was interrupted. Undo moves every file back, again without overwriting, and
// removes the folders the run created if they are empty. Deleting is the one
// thing that cannot be undone, which is why the screen asks twice before it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// rommJournalMove is one file the run moved.
type rommJournalMove struct {
	From string `json:"from"`
	To   string `json:"to"`
	Size int64  `json:"size"`
}

// rommJournalRun is one organise.
type rommJournalRun struct {
	ID    string            `json:"id"`
	At    time.Time         `json:"at"`
	Moves []rommJournalMove `json:"moves"`
	// Dirs are the folders the run created, removed again by an undo when
	// they are empty.
	Dirs []string `json:"dirs,omitempty"`
	// Deleted lists what the run removed for good.
	Deleted []string `json:"deleted,omitempty"`
}

// rommJournal is the file: the last few runs, oldest first.
type rommJournal struct {
	Version int              `json:"version"`
	Runs    []rommJournalRun `json:"runs"`
}

const (
	rommJournalVersion = 1
	// rommJournalKeep is how many runs can be undone, newest first.
	rommJournalKeep = 5
	// rommJournalEvery is how many changes are made between journal saves.
	rommJournalEvery = 10
)

// rommJournalPath is where the journal lives. A variable so tests can point it
// at a temporary folder.
var rommJournalPath = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "rom-manager", "journal.json")
}

// rommLoadJournal reads the journal; a missing or broken one is empty.
func rommLoadJournal(path string) rommJournal {
	j := rommJournal{Version: rommJournalVersion}
	if strings.TrimSpace(path) == "" {
		return j
	}
	if _, err := readJSONWithBackup(path, &j); err != nil {
		if !os.IsNotExist(err) {
			logWarn("romm", "ignoring %s: %v", path, err)
		}
		return rommJournal{Version: rommJournalVersion}
	}
	return j
}

// rommSaveJournal writes the journal atomically.
func rommSaveJournal(path string, j rommJournal) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	j.Version = rommJournalVersion
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o644)
}

// rommSaveRun stores a run in the journal, replacing an earlier save of the same
// run, and keeps the newest rommJournalKeep runs.
func rommSaveRun(path string, run rommJournalRun) error {
	j := rommLoadJournal(path)
	replaced := false
	for i := range j.Runs {
		if j.Runs[i].ID == run.ID {
			j.Runs[i] = run
			replaced = true
		}
	}
	if !replaced {
		j.Runs = append(j.Runs, run)
	}
	if len(j.Runs) > rommJournalKeep {
		j.Runs = j.Runs[len(j.Runs)-rommJournalKeep:]
	}
	return rommSaveJournal(path, j)
}

// rommUndoable is how many runs the journal can put back.
func rommUndoable(path string) int {
	n := 0
	for _, run := range rommLoadJournal(path).Runs {
		if len(run.Moves) > 0 {
			n++
		}
	}
	return n
}

// rommLastRun is the run "Undo" would put back.
func rommLastRun(path string) (rommJournalRun, bool) {
	j := rommLoadJournal(path)
	for i := len(j.Runs) - 1; i >= 0; i-- {
		if len(j.Runs[i].Moves) > 0 {
			return j.Runs[i], true
		}
	}
	return rommJournalRun{}, false
}

// ── File operations ─────────────────────────────────────────────────────────

// rommCreateDirs makes dir and returns the folders that did not exist before,
// deepest first, so an undo can remove exactly those.
func rommCreateDirs(dir string) ([]string, error) {
	var missing []string
	for d := filepath.Clean(dir); ; {
		if _, err := os.Stat(d); err == nil {
			break
		}
		missing = append(missing, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return missing, nil
}

// rommCopyVerified copies src to dst through a temporary file beside dst, then
// reads the copy back and compares its size and CRC32 with what was read from
// src. dst must not exist; it is only created once the copy has proved good.
func rommCopyVerified(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".jukahub-part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	srcCRC := crc32.NewIEEE()
	n, copyErr := io.Copy(io.MultiWriter(out, srcCRC), in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		os.Remove(tmp)
		return err
	}
	check, err := os.Open(tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	dstCRC := crc32.NewIEEE()
	m, readErr := io.Copy(dstCRC, check)
	check.Close()
	if readErr != nil || m != n || dstCRC.Sum32() != srcCRC.Sum32() {
		os.Remove(tmp)
		if readErr != nil {
			return readErr
		}
		return fmt.Errorf("the copy of %s did not read back the same — is the card full?", filepath.Base(src))
	}
	if _, err := os.Lstat(dst); err == nil {
		os.Remove(tmp)
		return fmt.Errorf("%s appeared while it was being copied", filepath.Base(dst))
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// rommMoveFile moves one regular file to dst, or to the first free name beside
// it, and returns where it went. Nothing is ever overwritten.
func rommMoveFile(src, dst string) (string, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", filepath.Base(src))
	}
	final := rommFreeName(dst, nil)
	if err := os.Rename(src, final); err == nil {
		return final, nil
	}
	// Another drive, or a file system that cannot rename across folders: copy,
	// check the copy, then remove the original.
	if err := rommCopyVerified(src, final); err != nil {
		return "", err
	}
	if err := os.Remove(src); err != nil {
		// Two copies would be worse than none moved: take ours back.
		os.Remove(final)
		return "", fmt.Errorf("could not remove the original %s: %w", filepath.Base(src), err)
	}
	return final, nil
}

// rommDeleteFile removes one regular file; a folder or a link is refused.
func rommDeleteFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	return os.Remove(path)
}

// ── Apply ───────────────────────────────────────────────────────────────────

// rommApplyResult is what a run did.
type rommApplyResult struct {
	Moved   int
	Deleted int
	Failed  int
	Errors  []string
	Moves   []rommJournalMove
	Stopped bool
}

// rommApplyProgress receives done of total as the run goes.
type rommApplyProgress func(done, total int)

// rommApplyItems carries out the enabled items of a plan, journaling as it goes.
func rommApplyItems(ctx context.Context, items []rommItem, journalPath string, progress rommApplyProgress) rommApplyResult {
	if progress == nil {
		progress = func(int, int) {}
	}
	var res rommApplyResult
	now := time.Now()
	run := rommJournalRun{ID: now.Format("20060102-150405.000"), At: now}
	total := 0
	for _, it := range items {
		if it.On && it.Action != rommActNone {
			total++
		}
	}
	fail := func(path string, err error) {
		res.Failed++
		if len(res.Errors) < 8 {
			res.Errors = append(res.Errors, filepath.Base(path)+": "+firstLine(err.Error()))
		}
	}
	done, unsaved := 0, 0
	for _, it := range items {
		if !it.On || it.Action == rommActNone {
			continue
		}
		if ctx != nil && ctx.Err() != nil {
			res.Stopped = true
			break
		}
		switch it.Action {
		case rommActMove, rommActBIOS:
			moves, dirs, err := rommMoveItem(it)
			run.Moves = append(run.Moves, moves...)
			run.Dirs = append(run.Dirs, dirs...)
			res.Moves = append(res.Moves, moves...)
			if err != nil {
				fail(it.Path, err)
			} else {
				res.Moved++
			}
		case rommActDelete:
			if err := rommDeleteFile(it.Path); err != nil {
				fail(it.Path, err)
			} else {
				res.Deleted++
				run.Deleted = append(run.Deleted, it.Path)
			}
		}
		done++
		unsaved++
		progress(done, total)
		if unsaved >= rommJournalEvery {
			unsaved = 0
			if err := rommSaveRun(journalPath, run); err != nil {
				logWarn("romm", "cannot save the journal: %v", err)
			}
		}
	}
	if len(run.Moves) > 0 || len(run.Deleted) > 0 {
		if err := rommSaveRun(journalPath, run); err != nil {
			logWarn("romm", "cannot save the journal: %v", err)
		}
	}
	return res
}

// rommMoveItem moves an item's file (and a sheet's tracks with it). The tracks
// go first and every destination is checked before anything moves, so a disc
// game is never left half in each folder by a name clash.
func rommMoveItem(it rommItem) ([]rommJournalMove, []string, error) {
	if strings.TrimSpace(it.Dest) == "" {
		return nil, nil, fmt.Errorf("no destination")
	}
	destDir := filepath.Dir(it.Dest)
	if len(it.Parts) > 0 {
		for _, p := range append([]string{it.Dest}, it.Parts...) {
			dest := filepath.Join(destDir, filepath.Base(p))
			if _, err := os.Lstat(dest); err == nil {
				return nil, nil, fmt.Errorf("%s already has a %s", filepath.Base(destDir), filepath.Base(p))
			}
		}
	}
	dirs, err := rommCreateDirs(destDir)
	if err != nil {
		return nil, nil, err
	}
	var moves []rommJournalMove
	for _, p := range it.Parts {
		info, err := os.Stat(p)
		if err != nil {
			continue // a track that went missing since the scan
		}
		to, err := rommMoveFile(p, filepath.Join(destDir, filepath.Base(p)))
		if err != nil {
			return moves, dirs, err
		}
		moves = append(moves, rommJournalMove{From: p, To: to, Size: info.Size()})
	}
	info, err := os.Stat(it.Path)
	if err != nil {
		return moves, dirs, err
	}
	to, err := rommMoveFile(it.Path, it.Dest)
	if err != nil {
		return moves, dirs, err
	}
	moves = append(moves, rommJournalMove{From: it.Path, To: to, Size: info.Size()})
	return moves, dirs, nil
}

// ── Undo ────────────────────────────────────────────────────────────────────

// rommUndoResult is what an undo did. Moves maps the files' current places
// back to the ones they had before the run, for the same follow-ups an
// organise needs (per-game options, recents).
type rommUndoResult struct {
	Restored int
	Failed   int
	Lost     int
	Errors   []string
	Moves    []rommJournalMove
	At       time.Time
}

// rommErrNothingToUndo is returned when the journal has no run with moves.
var rommErrNothingToUndo = errors.New("there is nothing to undo")

// rommUndoLast puts the newest run back: every file to where it was (or beside
// it, when something took its place since), then the folders the run created
// are removed when empty, and the run leaves the journal.
func rommUndoLast(journalPath string) (rommUndoResult, error) {
	var res rommUndoResult
	j := rommLoadJournal(journalPath)
	idx := -1
	for i := len(j.Runs) - 1; i >= 0; i-- {
		if len(j.Runs[i].Moves) > 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return res, rommErrNothingToUndo
	}
	run := j.Runs[idx]
	res.At = run.At
	res.Lost = len(run.Deleted)
	for i := len(run.Moves) - 1; i >= 0; i-- {
		m := run.Moves[i]
		if _, err := os.Lstat(m.To); err != nil {
			res.Failed++
			if len(res.Errors) < 8 {
				res.Errors = append(res.Errors, filepath.Base(m.To)+" is no longer where it was moved to")
			}
			continue
		}
		if _, err := rommCreateDirs(filepath.Dir(m.From)); err != nil {
			res.Failed++
			if len(res.Errors) < 8 {
				res.Errors = append(res.Errors, filepath.Base(m.From)+": "+firstLine(err.Error()))
			}
			continue
		}
		back, err := rommMoveFile(m.To, m.From)
		if err != nil {
			res.Failed++
			if len(res.Errors) < 8 {
				res.Errors = append(res.Errors, filepath.Base(m.To)+": "+firstLine(err.Error()))
			}
			continue
		}
		res.Restored++
		res.Moves = append(res.Moves, rommJournalMove{From: m.To, To: back, Size: m.Size})
	}
	dirs := append([]string(nil), run.Dirs...)
	sort.Slice(dirs, func(a, b int) bool { return len(dirs[a]) > len(dirs[b]) })
	for _, d := range dirs {
		// Remove only succeeds on an empty folder: anything the user put there
		// since keeps it.
		_ = os.Remove(d)
	}
	j.Runs = append(j.Runs[:idx], j.Runs[idx+1:]...)
	if err := rommSaveJournal(journalPath, j); err != nil {
		return res, err
	}
	return res, nil
}

// ── Follow-ups ──────────────────────────────────────────────────────────────

// rommAfterMoves keeps what is keyed by a ROM's path following the file: the
// per-game options and the recents list. It also drops the caches that list
// the card (the emulator catalog, Home's search index). Safe off the render
// thread; the library itself is refreshed by the caller on the main loop.
func rommAfterMoves(moves []rommJournalMove) {
	if len(moves) == 0 {
		return
	}
	rommMigrateGameOptions(moves)
	rommMigrateRecents(moves)
	invalidateEmulatorCache()
	homeSearchIdx.mu.Lock()
	homeSearchIdx.builtAt = time.Time{}
	homeSearchIdx.mu.Unlock()
}

// rommMigrateGameOptions moves per-game options to the ROMs' new paths.
func rommMigrateGameOptions(moves []rommJournalMove) {
	gameOptionsMu.Lock()
	ensureGameOptionsLocked()
	changed := false
	for _, m := range moves {
		from, to := gameOptionsKey(m.From), gameOptionsKey(m.To)
		if from == "" || to == "" || from == to {
			continue
		}
		if opts, ok := gameOptionsStore[from]; ok {
			delete(gameOptionsStore, from)
			gameOptionsStore[to] = opts
			changed = true
		}
	}
	var snapshot map[string]gameOptions
	if changed {
		snapshot = make(map[string]gameOptions, len(gameOptionsStore))
		for k, v := range gameOptionsStore {
			snapshot[k] = v
		}
	}
	gameOptionsMu.Unlock()
	if changed {
		if err := saveGameOptionsFile(gameOptionsPath(), snapshot); err != nil {
			logWarn("romm", "cannot save the moved game options: %v", err)
		}
	}
}

// rommMigrateRecents points recently played games at their new paths, so
// Continue still finds them.
func rommMigrateRecents(moves []rommJournalMove) {
	byFrom := make(map[string]string, len(moves))
	for _, m := range moves {
		byFrom[strings.ToLower(filepath.Clean(m.From))] = m.To
	}
	gameRecentsMu.Lock()
	if !gameRecentsLoaded {
		gameRecents = loadGameRecentsFile(gameRecentsFile())
		gameRecentsLoaded = true
	}
	changed := false
	for i := range gameRecents {
		if to, ok := byFrom[strings.ToLower(filepath.Clean(gameRecents[i].Path))]; ok {
			gameRecents[i].Path = to
			changed = true
		}
	}
	var snapshot []recentGame
	if changed {
		snapshot = append([]recentGame(nil), gameRecents...)
	}
	gameRecentsMu.Unlock()
	if changed {
		if err := saveGameRecentsFile(gameRecentsFile(), snapshot); err != nil {
			logWarn("romm", "cannot save the recents: %v", err)
		}
	}
}
