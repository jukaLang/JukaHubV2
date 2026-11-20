package main

// sdmove_copy.go — the copy and the verification behind "Move to a new SD
// card".
//
// One run, on its own goroutine:
//
//  1. Copy: every file of the plan goes to a temporary name in its folder on
//     the new card, is fsynced, gets the original's modification time and is
//     renamed into place. The SHA-256 of the bytes read from the old card is
//     computed on the way. A file that cannot be read (a failing card) is
//     recorded and the move goes on; a new card that is full, read-only or
//     gone ends the run, which a later run continues.
//  2. Verify: the page cache is dropped and every copy is read back from the
//     new card and hashed. A mismatch is copied once more and read back
//     again; a second mismatch is a problem with the new card (a fake-capacity
//     card fails here).
//  3. Catch up: the old card is walked once more and whatever changed or
//     appeared since (a game played meanwhile, JukaHub's settings) is copied
//     and verified too.
//
// Progress goes into an append-only journal next to the manifest, synced
// every two seconds, and is folded into the manifest when the run ends —
// also when it is stopped — so a re-run picks up where this one stopped.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// sdMoveOptions are the user's choices on the plan.
type sdMoveOptions struct {
	LeftOut   map[string]bool
	AcceptBig bool
}

// sdMoveProgress is what the screen shows while a run works.
type sdMoveProgress struct {
	Phase      string // "prepare", "copy", "verify", "catchup", "finish"
	Done       int
	Total      int
	Bytes      int64
	TotalBytes int64
	Current    string
	Paused     string
	Problems   int
	Began      time.Time
}

// sdMoveResult is what a run did.
type sdMoveResult struct {
	Canceled   bool
	Err        string
	Copied     int
	Reused     int
	Verified   int
	Recopied   int
	Files      int
	Bytes      int64
	State      string
	Failed     []sdMoveProblem
	Unreadable []sdMoveProblem
	NotCopied  []sdMoveProblem
	LeftOut    []string
	DestPath   string
	DestDisk   string
	USB        bool
}

// sdMoveRunner is one run's state; only its goroutine touches it (gameSeen
// excepted: the watcher sets it).
type sdMoveRunner struct {
	ctx       context.Context
	scan      *sdMoveScan
	dest      string
	destFS    string
	opts      sdMoveOptions
	m         sdMoveManifest
	index     map[string]int
	lower     map[string]string
	exclude   map[string]bool
	journal   *os.File
	pending   []byte
	pendingN  int
	lastFlush time.Time
	dirs      map[string]bool
	prog      sdMoveProgress
	report    func(sdMoveProgress)
	lastRep   time.Time
	lastBatt  time.Time
	res       sdMoveResult
	buf       []byte
	// scanUnread and skipped are the latest walk's (the catch-up walk
	// replaces the first one's): what could not be listed or read, and
	// what was left alone. Both go into the record at the end.
	scanUnread []sdMoveProblem
	skipped    []sdMoveProblem
	// gameSeen is set by the watcher whenever a game owns the screen.
	gameSeen atomic.Bool
}

// sdMoveRun runs one move. report is called from this goroutine at most every
// 250 ms; it must not block.
func sdMoveRun(ctx context.Context, scan *sdMoveScan, d sdMoveDestInfo, opts sdMoveOptions, appVersion string, report func(sdMoveProgress)) sdMoveResult {
	r := &sdMoveRunner{
		ctx: ctx, scan: scan, dest: d.Root, destFS: d.FS, opts: opts, report: report,
		dirs: map[string]bool{}, exclude: map[string]bool{}, buf: make([]byte, 1<<20),
	}
	r.res.DestPath = d.Root
	r.scanUnread = append([]sdMoveProblem(nil), scan.Unreadable...)
	r.skipped = append([]sdMoveProblem(nil), scan.Skipped...)
	if opts.LeftOut != nil {
		for _, c := range sdMoveCatOrder {
			if opts.LeftOut[c] {
				r.res.LeftOut = append(r.res.LeftOut, c)
			}
		}
	}
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go r.watch(stopWatch)
	if err := r.prepare(d, appVersion); err != nil {
		r.res.Err = "The move could not start: " + firstLine(err.Error())
		return r.res
	}
	defer r.closeJournal()

	work := r.selectItems(scan.Items)
	r.copyAll(work, "copy")
	if !r.stopped() {
		r.verifyAll("verify", nil)
	}
	// Catch up until one pass ran with no game in between: a game played
	// during a pass writes saves its walk may already have passed, and the
	// old card would then be newer than the "ready" new one.
	for round := 0; !r.stopped(); round++ {
		r.guard() // waits while a game runs
		if r.stopped() {
			break
		}
		if round == 3 {
			r.res.Err = "Games were played while the move was finishing, so their newest saves may only be on the old card. Start the move again when no game is running: it copies only what changed."
			break
		}
		r.gameSeen.Store(false)
		r.catchUp()
		if !r.gameSeen.Load() {
			break
		}
	}
	r.finish()
	return r.res
}

// watch runs beside the run until stop closes. It keeps the handheld awake —
// a multi-gigabyte read-back, the catch-up walk or a slow card can go
// minutes without reaching guard, longer than Auto sleep's shortest wait —
// except while a game owns the screen (which keeps it awake itself) or once
// the battery is below the stop level without a charger, so a read stuck on
// a failing card cannot hold a flat handheld awake. It also notes every game
// that runs, for the catch-up rounds.
func (r *sdMoveRunner) watch(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var lastAwake, lastBatt time.Time
	battOK := true
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			if gameScreenSuspended() {
				r.gameSeen.Store(true)
				continue
			}
			if now.Sub(lastBatt) >= 30*time.Second {
				lastBatt = now
				pct := getBatteryPercent()
				battOK = pct < 0 || pct >= sdMoveLowBattery || getBatteryCharging()
			}
			if battOK && now.Sub(lastAwake) >= 20*time.Second {
				// Auto sleep, the idle screen and the idle-time updaters all
				// count from the last input; a move is use without input.
				noteUserInputTime(now)
				lastAwake = now
			}
		}
	}
}

// stopped reports a run that must end: stopped by the user, or by a problem
// that ends every file (the new card full or gone, the battery).
func (r *sdMoveRunner) stopped() bool {
	return r.ctx.Err() != nil || r.res.Err != ""
}

// prepare makes the move folder, folds in what an earlier run left (when it
// came from this card) and writes the manifest the journal builds on.
func (r *sdMoveRunner) prepare(d sdMoveDestInfo, appVersion string) error {
	r.setPhase("prepare", 0, 0)
	if r.dest == "" || d.SameCard {
		return errors.New("no new card")
	}
	dir := filepath.Join(r.dest, sdMoveDirName)
	if info, err := os.Lstat(dir); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New(sdMoveDirName + " on the new card is not a folder")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	m, ok, err := sdMoveReadState(r.dest)
	if err != nil || !ok || !sdMovePrevUsable(r.scan, &m) {
		// A damaged record, or one from another card: start a fresh one. The
		// files on the new card stay; matching names are copied again.
		m = sdMoveManifest{}
	}
	m.Format, m.Version, m.State = sdMoveFormat, sdMoveVersion, sdMoveStateCopying
	m.WrittenText = sdMoveClockText()
	m.AppVersion = appVersion
	dev := DetectDevice()
	m.Device, m.DeviceName = string(dev), deviceDisplayName(dev)
	m.SourceRoot, m.SourceFS, m.SourceCID = r.scan.Root, r.scan.FS, r.scan.CID
	m.DestFS = r.destFS
	m.AppRel, m.DataRel = r.scan.AppRel, r.scan.DataRel
	m.LeftOut = append([]string(nil), r.res.LeftOut...)
	m.Skipped = append([]sdMoveProblem(nil), r.scan.Skipped...)
	m.NotCopied, m.Unreadable, m.Failed = nil, nil, nil
	// The record only lists what is really on the new card: an earlier
	// copy that has gone or changed since is dropped (and copied again if
	// it is part of this run).
	kept := m.Files[:0]
	for _, f := range m.Files {
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		p, err := sdMoveJoin(r.dest, f.Path)
		if err != nil {
			continue
		}
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() && info.Size() == f.Size && sdMoveSameMtime(info.ModTime().UnixNano(), f.Mtime) {
			kept = append(kept, f)
		}
	}
	m.Files = kept
	r.m = m
	r.index = make(map[string]int, len(m.Files))
	for i, f := range m.Files {
		r.index[f.Path] = i
	}
	if !sdMoveCaseSensitive(r.destFS) {
		r.lower = make(map[string]string, len(m.Files))
		for _, f := range m.Files {
			r.lower[strings.ToLower(f.Path)] = f.Path
		}
	}
	if err := sdMoveWriteManifest(r.dest, &r.m); err != nil {
		return err
	}
	r.reindex()
	jpath := filepath.Join(dir, sdMoveJournalName)
	_ = os.Remove(jpath)
	j, err := os.OpenFile(jpath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	r.journal = j
	r.lastFlush = time.Now()
	return nil
}

// reindex rebuilds the path index after the files were re-sorted.
func (r *sdMoveRunner) reindex() {
	r.index = make(map[string]int, len(r.m.Files))
	for i, f := range r.m.Files {
		r.index[f.Path] = i
	}
}

// selectItems applies the plan's choices: left-out categories, files too big
// for FAT32 and names that clash on a card that ignores case are recorded in
// the manifest's not-copied list (left-out categories by name only).
func (r *sdMoveRunner) selectItems(items []sdMoveItem) []sdMoveItem {
	fat := sdMoveIsFAT32(r.destFS)
	clash := r.lower != nil && sdMoveCaseSensitive(r.scan.FS)
	var out []sdMoveItem
	for _, it := range items {
		if r.opts.LeftOut[it.Cat] || r.exclude[it.Rel] {
			continue
		}
		if fat && it.Size > sdMoveFAT32Max {
			r.exclude[it.Rel] = true
			r.res.NotCopied = append(r.res.NotCopied, sdMoveProblem{Path: it.Rel, Why: "4 GB or larger: FAT32 cannot hold it", Cat: it.Cat, Size: it.Size})
			continue
		}
		if clash {
			key := strings.ToLower(it.Rel)
			if other, ok := r.lower[key]; ok && other != it.Rel {
				r.exclude[it.Rel] = true
				r.res.NotCopied = append(r.res.NotCopied, sdMoveProblem{Path: it.Rel, Why: "same name as " + other + " on a card that ignores case", Cat: it.Cat, Size: it.Size})
				continue
			}
			r.lower[key] = it.Rel
		}
		out = append(out, it)
	}
	return out
}

// ── Progress, guards and the journal ─────────────────────────────────────

func (r *sdMoveRunner) setPhase(phase string, total int, totalBytes int64) {
	r.prog = sdMoveProgress{Phase: phase, Total: total, TotalBytes: totalBytes, Began: time.Now(), Problems: r.problemCount()}
	r.emit(true)
}

func (r *sdMoveRunner) problemCount() int {
	return len(r.res.Failed) + len(r.res.Unreadable)
}

func (r *sdMoveRunner) emit(force bool) {
	if r.report == nil {
		return
	}
	if !force && time.Since(r.lastRep) < 250*time.Millisecond {
		return
	}
	r.lastRep = time.Now()
	r.prog.Problems = r.problemCount()
	r.report(r.prog)
}

// guard runs between files and between chunks of a big one: it waits while a
// game runs and stops the run on a low battery without a charger (watch keeps
// the handheld awake).
func (r *sdMoveRunner) guard() {
	now := time.Now()
	for gameScreenSuspended() && r.ctx.Err() == nil {
		if r.prog.Paused == "" {
			r.prog.Paused = "Waiting for the game to close"
			r.emit(true)
		}
		r.flush(true)
		time.Sleep(time.Second)
	}
	if r.prog.Paused != "" {
		r.prog.Paused = ""
		r.emit(true)
	}
	if now.Sub(r.lastBatt) >= 30*time.Second {
		r.lastBatt = now
		if pct := getBatteryPercent(); pct >= 0 && pct < sdMoveLowBattery && !getBatteryCharging() {
			r.res.Err = fmt.Sprintf("The battery is down to %d%%. Plug in the charger and start the move again: it continues where it stopped.", pct)
		}
	}
}

// record notes a copied ("c") or verified ("v") file in the manifest and the
// journal.
func (r *sdMoveRunner) record(kind string, f sdMoveFile) {
	if kind == "c" {
		f.Verified = false
		if i, ok := r.index[f.Path]; ok {
			r.m.Files[i] = f
		} else {
			r.index[f.Path] = len(r.m.Files)
			r.m.Files = append(r.m.Files, f)
		}
		if r.lower != nil {
			r.lower[strings.ToLower(f.Path)] = f.Path
		}
	} else if i, ok := r.index[f.Path]; ok {
		r.m.Files[i].Verified = true
	}
	line, err := json.Marshal(sdMoveJournalLine{K: kind, F: f})
	if err == nil {
		r.pending = append(r.pending, line...)
		r.pending = append(r.pending, '\n')
		r.pendingN++
	}
	r.flush(false)
}

// flush writes the journal: first a sync of everything, so every rename it
// mentions is on the card, then the lines, then an fsync of the journal.
func (r *sdMoveRunner) flush(force bool) {
	if r.journal == nil || r.pendingN == 0 {
		return
	}
	if !force && r.pendingN < 256 && time.Since(r.lastFlush) < 2*time.Second {
		return
	}
	sdMoveSyncAll()
	if _, err := r.journal.Write(r.pending); err == nil {
		_ = r.journal.Sync()
	}
	r.pending = r.pending[:0]
	r.pendingN = 0
	r.lastFlush = time.Now()
}

func (r *sdMoveRunner) closeJournal() {
	if r.journal != nil {
		r.flush(true)
		_ = r.journal.Close()
		r.journal = nil
	}
}

// ── Copy ─────────────────────────────────────────────────────────────────

// copyAll copies items, skipping the ones an earlier run already copied.
func (r *sdMoveRunner) copyAll(items []sdMoveItem, phase string) {
	var total int64
	for _, it := range items {
		total += it.Size
	}
	r.setPhase(phase, len(items), total)
	for i, it := range items {
		r.guard()
		if r.stopped() {
			return
		}
		r.prog.Done, r.prog.Current = i, it.Rel
		r.emit(false)
		r.copyItem(it, false)
		if r.stopped() {
			return
		}
	}
	r.prog.Done = len(items)
	r.flush(true)
	r.emit(true)
}

// alreadyThere reports a file an earlier run copied whose original has not
// changed since and whose copy is still the one recorded.
func (r *sdMoveRunner) alreadyThere(it sdMoveItem) bool {
	i, ok := r.index[it.Rel]
	if !ok {
		return false
	}
	f := r.m.Files[i]
	if f.Size != it.Size || f.SrcMtime != it.Mtime || f.SrcMtime == 0 {
		return false
	}
	dst, err := sdMoveJoin(r.dest, it.Rel)
	if err != nil {
		return false
	}
	info, err := os.Lstat(dst)
	return err == nil && info.Mode().IsRegular() && info.Size() == f.Size && sdMoveSameMtime(info.ModTime().UnixNano(), f.Mtime)
}

// copyItem copies one file. force skips the "already there" test (a copy
// that failed verification).
func (r *sdMoveRunner) copyItem(it sdMoveItem, force bool) bool {
	if !force && r.alreadyThere(it) {
		r.res.Reused++
		r.prog.Bytes += it.Size
		return true
	}
	src, err := sdMoveJoin(r.scan.Root, it.Rel)
	if err != nil {
		r.res.Unreadable = append(r.res.Unreadable, sdMoveProblem{Path: it.Rel, Why: firstLine(err.Error()), Cat: it.Cat, Size: it.Size})
		return false
	}
	dst, err := sdMoveJoin(r.dest, it.Rel)
	if err != nil {
		r.writeFailed(it, err)
		return false
	}
	if err := r.ensureDir(path.Dir(it.Rel)); err != nil {
		r.writeFailed(it, err)
		return false
	}
	cur := it
	for attempt := 1; ; attempt++ {
		before := r.prog.Bytes
		f, readErr, err := r.copyOnce(cur, src, dst)
		if err != nil {
			r.prog.Bytes = before
			if r.stopped() {
				return false
			}
			if readErr {
				if !force && errors.Is(err, os.ErrNotExist) {
					// Gone from the old card since the walk (JukaHub tidied a
					// cache, a file was renamed): nothing left to keep. The
					// catch-up walk copies whatever took its place.
					return false
				}
				r.res.Unreadable = append(r.res.Unreadable, sdMoveProblem{Path: it.Rel, Why: "could not be read from the old card: " + firstLine(err.Error()), Cat: it.Cat, Size: it.Size})
				return false
			}
			r.writeFailed(it, err)
			return false
		}
		// A file that changed while it was read (JukaHub's own settings, a
		// save) is read again, up to three times; the last copy is kept but
		// marked, so the catch-up pass or a later run copies it once more.
		info, serr := os.Lstat(src)
		if serr == nil && info.Mode().IsRegular() && (info.Size() != f.Size || info.ModTime().UnixNano() != cur.Mtime) {
			if attempt < 3 {
				r.prog.Bytes = before
				cur.Size, cur.Mtime = info.Size(), info.ModTime().UnixNano()
				continue
			}
			f.SrcMtime = 0
		}
		r.record("c", f)
		r.res.Copied++
		return true
	}
}

// writeFailed records a file the new card would not take, or ends the run
// when the error means the card is full, read-only or gone.
func (r *sdMoveRunner) writeFailed(it sdMoveItem, err error) {
	if why, fatal := sdMoveFatalWriteErr(err); fatal {
		r.res.Err = why + " What was copied so far is kept; start the move again to continue."
		return
	}
	if _, serr := os.Stat(filepath.Join(r.dest, sdMoveDirName)); serr != nil {
		r.res.Err = "The new card went away — is the reader still plugged in? What was copied so far is kept; start the move again to continue."
		return
	}
	r.res.Failed = append(r.res.Failed, sdMoveProblem{Path: it.Rel, Why: "could not be written to the new card: " + firstLine(err.Error()), Cat: it.Cat, Size: it.Size})
}

// ensureDir makes rel (a folder) on the new card, one part at a time, and
// refuses a part that is a link or a file: the move never writes through a
// link that could lead off the card.
func (r *sdMoveRunner) ensureDir(rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(rel, "/") {
		if cur == "" {
			cur = part
		} else {
			cur += "/" + part
		}
		if r.dirs[cur] {
			continue
		}
		full, err := sdMoveJoin(r.dest, cur)
		if err != nil {
			return err
		}
		info, err := os.Lstat(full)
		if errors.Is(err, os.ErrNotExist) {
			if merr := os.Mkdir(full, 0o755); merr != nil && !errors.Is(merr, os.ErrExist) {
				return merr
			}
			info, err = os.Lstat(full)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s on the new card is a link or a file, not a folder", cur)
		}
		r.dirs[cur] = true
	}
	return nil
}

// copyOnce copies src to dst through a temporary name and returns the record
// of the copy. readErr says the error came from the old card.
func (r *sdMoveRunner) copyOnce(it sdMoveItem, src, dst string) (f sdMoveFile, readErr bool, err error) {
	in, err := os.Open(src)
	if err != nil {
		return f, true, err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), ".jhmove-*"+sdMoveTempSuffix)
	if err != nil {
		return f, false, err
	}
	tmp := out.Name()
	closed, done := false, false
	defer func() {
		if !closed {
			_ = out.Close()
		}
		if !done {
			// The temporary file is this copy's own; nothing else is removed.
			_ = os.Remove(tmp)
		}
	}()
	h := sha256.New()
	var n int64
	var sinceGuard int64
	for {
		if cerr := r.ctx.Err(); cerr != nil {
			return f, false, cerr
		}
		k, rerr := in.Read(r.buf)
		if k > 0 {
			h.Write(r.buf[:k])
			if _, werr := out.Write(r.buf[:k]); werr != nil {
				return f, false, werr
			}
			n += int64(k)
			r.prog.Bytes += int64(k)
			r.emit(false)
			if sinceGuard += int64(k); sinceGuard >= 64<<20 {
				sinceGuard = 0
				r.guard()
				if r.res.Err != "" {
					return f, false, errors.New("stopped")
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return f, true, rerr
		}
	}
	if err := out.Sync(); err != nil {
		return f, false, err
	}
	closed = true
	if err := out.Close(); err != nil {
		return f, false, err
	}
	mt := time.Unix(0, it.Mtime)
	_ = os.Chtimes(tmp, mt, mt)
	if err := os.Rename(tmp, dst); err != nil {
		return f, false, err
	}
	done = true
	info, err := os.Lstat(dst)
	if err != nil {
		return f, false, err
	}
	if info.Size() != n {
		return f, false, fmt.Errorf("the new card holds %d bytes of the %d written", info.Size(), n)
	}
	f = sdMoveFile{Path: it.Rel, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), Cat: it.Cat,
		Mtime: info.ModTime().UnixNano(), SrcMtime: it.Mtime, Live: it.Live}
	return f, false, nil
}

// ── Verify ───────────────────────────────────────────────────────────────

// hashDest reads a copy back from the new card itself (its cached pages are
// dropped first) and returns its SHA-256 and size.
func (r *sdMoveRunner) hashDest(rel string, count bool) (string, int64, error) {
	p, err := sdMoveJoin(r.dest, rel)
	if err != nil {
		return "", 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	sdMoveDropCache(f)
	h := sha256.New()
	var n int64
	for {
		if cerr := r.ctx.Err(); cerr != nil {
			return "", n, cerr
		}
		k, rerr := f.Read(r.buf)
		if k > 0 {
			h.Write(r.buf[:k])
			n += int64(k)
			if count {
				r.prog.Bytes += int64(k)
				r.emit(false)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", n, rerr
		}
	}
	sdMoveDropCache(f)
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// verifyAll reads back every copy not yet verified (or only those in only,
// when set) and fixes a mismatch by copying once more.
func (r *sdMoveRunner) verifyAll(phase string, only map[string]bool) {
	r.flush(true)
	sdMoveDropAllCaches()
	// By path, not position: a bad copy is taken out of the list as it goes.
	var todo []string
	var total int64
	for _, f := range r.m.Files {
		if f.Verified || (only != nil && !only[f.Path]) {
			continue
		}
		todo = append(todo, f.Path)
		total += f.Size
	}
	r.setPhase(phase, len(todo), total)
	for k, rel := range todo {
		r.guard()
		if r.stopped() {
			return
		}
		i, ok := r.index[rel]
		if !ok {
			continue
		}
		f := r.m.Files[i]
		r.prog.Done, r.prog.Current = k, f.Path
		r.emit(false)
		before := r.prog.Bytes
		sum, n, err := r.hashDest(f.Path, true)
		if r.ctx.Err() != nil {
			return
		}
		if err == nil && sum == f.SHA256 && n == f.Size {
			r.record("v", f)
			r.res.Verified++
			continue
		}
		r.prog.Bytes = before + f.Size
		if err != nil {
			if why, fatal := sdMoveFatalWriteErr(err); fatal && why != "" {
				r.res.Err = why + " What was copied so far is kept; start the move again to continue."
				return
			}
		}
		// Copy it once more from the old card and read it back again.
		it := sdMoveItem{Rel: f.Path, Size: f.Size, Mtime: f.SrcMtime, Cat: f.Cat, Live: f.Live}
		if info, serr := os.Lstat(filepath.Join(r.scan.Root, filepath.FromSlash(f.Path))); serr == nil && info.Mode().IsRegular() {
			it.Size, it.Mtime = info.Size(), info.ModTime().UnixNano()
		}
		failsBefore, unreadBefore := len(r.res.Failed), len(r.res.Unreadable)
		verifyBytes := r.prog.Bytes
		copied := r.copyItem(it, true)
		r.prog.Bytes = verifyBytes
		if !copied {
			if r.stopped() {
				return
			}
			if len(r.res.Failed) == failsBefore && len(r.res.Unreadable) == unreadBefore {
				r.res.Failed = append(r.res.Failed, sdMoveProblem{Path: f.Path, Why: "the copy did not match and could not be made again", Cat: f.Cat, Size: f.Size})
			}
			r.dropEntry(f.Path)
			continue
		}
		ni, ok := r.index[f.Path]
		if !ok {
			continue
		}
		nf := r.m.Files[ni]
		sum2, n2, err2 := r.hashDest(nf.Path, false)
		if r.ctx.Err() != nil {
			return
		}
		if err2 == nil && sum2 == nf.SHA256 && n2 == nf.Size {
			r.record("v", nf)
			r.res.Verified++
			r.res.Recopied++
			continue
		}
		why := "the new card returned different bytes twice — it may be faulty, or smaller than it claims"
		if err2 != nil {
			why = "could not be read back from the new card: " + firstLine(err2.Error())
		}
		r.res.Failed = append(r.res.Failed, sdMoveProblem{Path: f.Path, Why: why, Cat: f.Cat, Size: f.Size})
		r.dropEntry(f.Path)
	}
	r.prog.Done = len(todo)
	r.flush(true)
	r.emit(true)
}

// dropEntry takes a file whose copy is bad out of the manifest: the record
// must only list what is really on the new card.
func (r *sdMoveRunner) dropEntry(rel string) {
	i, ok := r.index[rel]
	if !ok {
		return
	}
	r.m.Files = append(r.m.Files[:i], r.m.Files[i+1:]...)
	r.reindex()
}

// ── Catch up ─────────────────────────────────────────────────────────────

// catchUp walks the old card again and copies what changed or appeared since
// the plan was made, then verifies those copies. A file that could not be
// read the first time gets this second try.
func (r *sdMoveRunner) catchUp() {
	r.setPhase("catchup", 0, 0)
	again, err := sdMoveScanSource(r.ctx, r.scan.Root, func(n int) {
		r.prog.Done = n
		r.emit(false)
	})
	if r.stopped() {
		return
	}
	if err != nil {
		// What changed during the move may be missing: the record must say
		// so, or the old card would be called a complete backup.
		r.scanUnread = append(r.scanUnread, sdMoveProblem{Why: "the old card could not be walked again to copy what changed during the move: " + firstLine(err.Error())})
		return
	}
	// This walk's view of what could not be read, or was left alone,
	// replaces the first one's.
	r.scanUnread = again.Unreadable
	r.skipped = again.Skipped
	failed := map[string]bool{}
	for _, p := range r.res.Failed {
		failed[p.Path] = true
	}
	var changed []sdMoveItem
	for _, it := range r.selectItems(again.Items) {
		if failed[it.Rel] {
			continue
		}
		if i, ok := r.index[it.Rel]; ok {
			f := r.m.Files[i]
			if f.Size == it.Size && f.SrcMtime == it.Mtime && f.SrcMtime != 0 {
				continue
			}
		}
		changed = append(changed, it)
	}
	if len(changed) == 0 {
		return
	}
	retried := map[string]bool{}
	for _, it := range changed {
		retried[it.Rel] = true
	}
	var still []sdMoveProblem
	for _, u := range r.res.Unreadable {
		if !retried[u.Path] {
			still = append(still, u)
		}
	}
	r.res.Unreadable = still
	r.copyAll(changed, "catchup")
	if r.stopped() {
		return
	}
	r.verifyAll("catchup", retried)
}

// ── Finish ───────────────────────────────────────────────────────────────

// finish writes the manifest with everything this run learned and removes
// the journal it replaces.
func (r *sdMoveRunner) finish() {
	r.setPhase("finish", 0, 0)
	r.flush(true)
	r.res.Canceled = r.ctx.Err() != nil
	complete := !r.stopped()
	allVerified := true
	for _, f := range r.m.Files {
		if !f.Verified {
			allVerified = false
			break
		}
	}
	switch {
	case complete && allVerified && len(r.res.Failed) == 0:
		r.m.State = sdMoveStateVerified
	case complete:
		r.m.State = sdMoveStateIncomplete
	default:
		r.m.State = sdMoveStateCopying
	}
	// Everything that is only on the old card goes into the record, so the
	// check after the swap never calls the old card a complete backup while
	// it still holds something: what the walks could not read, links and
	// names the new card cannot store, and copies of files that kept
	// changing while they were read (the old card may be newer; JukaHub's
	// own live files excepted).
	unread := make([]sdMoveProblem, 0, len(r.scanUnread)+len(r.res.Unreadable))
	for _, p := range r.scanUnread {
		unread = append(unread, sdMoveRecordable(p))
	}
	r.res.Unreadable = append(unread, r.res.Unreadable...)
	for _, p := range r.skipped {
		if sdMoveSkipLeavesData(p.Why) {
			r.res.NotCopied = append(r.res.NotCopied, sdMoveRecordable(p))
		}
	}
	for _, f := range r.m.Files {
		if f.SrcMtime == 0 && !f.Live {
			r.res.NotCopied = append(r.res.NotCopied, sdMoveProblem{Path: f.Path, Why: "kept changing while it was copied — the old card may hold a newer version", Cat: f.Cat, Size: f.Size})
		}
	}
	r.m.WrittenText = sdMoveClockText()
	r.m.NotCopied = r.res.NotCopied
	r.m.Unreadable = r.res.Unreadable
	r.m.Failed = r.res.Failed
	r.m.LeftOut = append([]string(nil), r.res.LeftOut...)
	r.m.Skipped = append([]sdMoveProblem(nil), r.skipped...)
	sort.Slice(r.m.Skipped, func(i, j int) bool { return r.m.Skipped[i].Path < r.m.Skipped[j].Path })
	if err := sdMoveWriteManifest(r.dest, &r.m); err != nil {
		// The journal stays: the next run folds it in.
		if r.res.Err == "" {
			r.res.Err = "The move's record could not be written to the new card: " + firstLine(err.Error())
		}
		r.m.State = sdMoveStateCopying
	} else {
		if r.journal != nil {
			_ = r.journal.Close()
			r.journal = nil
		}
		_ = os.Remove(filepath.Join(r.dest, sdMoveDirName, sdMoveJournalName))
		// A check result from an earlier move to this card no longer applies.
		_ = os.Remove(filepath.Join(r.dest, sdMoveDirName, sdMoveCheckName))
	}
	sdMoveSyncAll()
	r.res.State = r.m.State
	r.res.Files = len(r.m.Files)
	r.res.Bytes = r.m.TotalBytes
	r.res.Verified = 0
	for _, f := range r.m.Files {
		if f.Verified {
			r.res.Verified++
		}
	}
}
