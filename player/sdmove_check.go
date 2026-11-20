package main

// sdmove_check.go — the check after the swap.
//
// When JukaHub starts from a card that holds a move record which has not been
// checked yet, it checks the card against it in the background: first that
// every file is there with its size (seconds), then that every file's bytes
// still hash to what the old card held (as long as reading the whole card
// takes; on a background thread at the lowest CPU and I/O priority, paused
// while a game runs or when the user pauses it). The answer is a toast and the
// card on the move screen; it is stored next to the record, with how far the
// hashing got, so a restart continues and a finished check never runs again
// unless asked.
//
// A file whose copy differs but whose modification time moved on was changed
// after the move (a game played on the new card): that is not damage. A
// difference with the time unchanged is. JukaHub's own folder is rewritten by
// JukaHub itself and never raises the alarm.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sdMoveCheckFormat = "jukahub-sd-move-check"
	sdMoveCheckList   = 200 // paths kept per list; the counts are complete
)

// The phases of a check.
const (
	sdMoveCheckSizes    = "sizes"
	sdMoveCheckContents = "contents"
	sdMoveCheckDone     = "done"
)

// sdMoveCheckState is .jukahub-move/check.json.
type sdMoveCheckState struct {
	Format    string   `json:"format"`
	Version   int      `json:"version"`
	Phase     string   `json:"phase"`
	Next      int      `json:"next"`
	Files     int      `json:"files"`
	Bytes     int64    `json:"bytes"`
	Missing   []string `json:"missing,omitempty"`
	MissingN  int      `json:"missingCount,omitempty"`
	Damaged   []string `json:"damaged,omitempty"`
	DamagedN  int      `json:"damagedCount,omitempty"`
	ChangedN  int      `json:"changedCount,omitempty"`
	OwnN      int      `json:"ownCount,omitempty"`
	MoveState string   `json:"moveState,omitempty"`
	LeftOut   []string `json:"leftOut,omitempty"`
	NotOnCard int      `json:"notOnCard,omitempty"`
	Err       string   `json:"error,omitempty"`
}

func (st *sdMoveCheckState) addMissing(rel string) {
	st.MissingN++
	if len(st.Missing) < sdMoveCheckList {
		st.Missing = append(st.Missing, rel)
	}
}

func (st *sdMoveCheckState) addDamaged(rel string) {
	st.DamagedN++
	if len(st.Damaged) < sdMoveCheckList {
		st.Damaged = append(st.Damaged, rel)
	}
}

// problems is what makes the old card worth keeping.
func (st *sdMoveCheckState) problems() int { return st.MissingN + st.DamagedN }

// clone copies st for the screen.
func (st *sdMoveCheckState) clone() *sdMoveCheckState {
	c := *st
	c.Missing = append([]string(nil), st.Missing...)
	c.Damaged = append([]string(nil), st.Damaged...)
	c.LeftOut = append([]string(nil), st.LeftOut...)
	return &c
}

// sdMoveParseCheck reads a stored check. Its lists are only shown, so an
// unsafe path in them is dropped; the counts are bounded.
func sdMoveParseCheck(data []byte) (sdMoveCheckState, bool) {
	var st sdMoveCheckState
	if len(data) > 4<<20 || json.Unmarshal(data, &st) != nil || st.Format != sdMoveCheckFormat || st.Version != sdMoveVersion {
		return sdMoveCheckState{}, false
	}
	switch st.Phase {
	case sdMoveCheckSizes, sdMoveCheckContents, sdMoveCheckDone:
	default:
		return sdMoveCheckState{}, false
	}
	clean := func(in []string) []string {
		var out []string
		for _, p := range in {
			if _, err := sdMoveCleanRel(p); err == nil && len(out) < sdMoveCheckList {
				out = append(out, p)
			}
		}
		return out
	}
	st.Missing, st.Damaged = clean(st.Missing), clean(st.Damaged)
	if st.Files < 0 || st.Next < 0 || st.Next > st.Files || st.MissingN < 0 || st.DamagedN < 0 {
		return sdMoveCheckState{}, false
	}
	var cats []string
	for _, c := range st.LeftOut {
		if sdMoveKnownCat(c) {
			cats = append(cats, c)
		}
	}
	st.LeftOut = cats
	return st, true
}

func sdMoveLoadCheck(dir string) (sdMoveCheckState, bool) {
	data, err := os.ReadFile(filepath.Join(dir, sdMoveCheckName))
	if err != nil {
		return sdMoveCheckState{}, false
	}
	return sdMoveParseCheck(data)
}

func sdMoveSaveCheck(dir string, st *sdMoveCheckState) {
	st.Format, st.Version = sdMoveCheckFormat, sdMoveVersion
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := sdMoveWriteAtomic(filepath.Join(dir, sdMoveCheckName), data); err != nil {
		logWarn("sdmove", "could not store the new card's check: %v", err)
	}
}

// ── Shared state ─────────────────────────────────────────────────────────

// sdMoveCheckShared is the check as the screen sees it.
var sdMoveCheckShared struct {
	mu     sync.Mutex
	st     *sdMoveCheckState
	done   int
	total  int
	paused bool
	gen    uint64
}

var (
	sdMoveCheckRunning atomic.Bool
	sdMoveCheckPaused  atomic.Bool
)

func sdMoveCheckPublish(st *sdMoveCheckState, done, total int, paused bool) {
	var c *sdMoveCheckState
	if st != nil {
		c = st.clone()
	}
	sdMoveCheckShared.mu.Lock()
	sdMoveCheckShared.st, sdMoveCheckShared.done, sdMoveCheckShared.total, sdMoveCheckShared.paused = c, done, total, paused
	sdMoveCheckShared.gen++
	sdMoveCheckShared.mu.Unlock()
}

// sdMoveCheckView is the last published check, its progress and whether one
// is running.
func sdMoveCheckView() (st *sdMoveCheckState, done, total int, running, paused bool) {
	sdMoveCheckShared.mu.Lock()
	defer sdMoveCheckShared.mu.Unlock()
	return sdMoveCheckShared.st, sdMoveCheckShared.done, sdMoveCheckShared.total, sdMoveCheckRunning.Load(), sdMoveCheckShared.paused
}

func init() {
	// After the first frame, where a toast can show (startup.go).
	deferStartupTask("sd-move-check", func() {
		if sdMoveSupported() {
			sdMoveCheckStart(false)
		}
	})
}

// sdMoveCheckStart runs the check on its own goroutine unless one runs.
// again starts over even when an earlier check finished.
func sdMoveCheckStart(again bool) {
	if !sdMoveCheckRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		// The whole check runs on one OS thread at the lowest priority; the
		// goroutine never unlocks it, so the thread ends with it.
		runtime.LockOSThread()
		sdMoveLowPriority()
		defer sdMoveCheckRunning.Store(false)
		sdMoveCheckRun(again)
	}()
}

// sdMoveCheckSummary is the result in one sentence, and how loud to say it.
func sdMoveCheckSummary(st *sdMoveCheckState) (string, ToastKind) {
	switch {
	case st == nil:
		return "", ToastKindInfo
	case st.Err != "":
		return "The move record on this card could not be read (" + st.Err + "). Keep your old card.", ToastKindWarning
	case st.MoveState != sdMoveStateVerified:
		return "This card holds a move that did not finish. Keep your old card: put it back in and run Move to a new SD card again.", ToastKindWarning
	}
	n := pluralizeCount(st.Files, "file", "files")
	if p := st.problems(); p > 0 {
		var bits []string
		if st.MissingN > 0 {
			bits = append(bits, pluralizeCount(st.MissingN, "file is", "files are")+" missing")
		}
		if st.DamagedN > 0 {
			bits = append(bits, pluralizeCount(st.DamagedN, "file differs", "files differ")+" from the old card")
		}
		return "New card check: " + strings.Join(bits, " and ") + ". Keep your old card and run the move again.", ToastKindWarning
	}
	if st.Phase != sdMoveCheckDone {
		return "New card: all " + n + " are here. Checking their contents in the background…", ToastKindInfo
	}
	if st.NotOnCard == 0 && len(st.LeftOut) == 0 {
		return "New card checked: all " + n + " are here. Your old card is a complete backup — you can reuse it.", ToastKindSuccess
	}
	return "New card checked: all " + n + " copied are here. Keep your old card: it still holds what was left out or could not be copied.", ToastKindInfo
}

// sdMoveCheckRun is one check of the card JukaHub runs from.
func sdMoveCheckRun(again bool) {
	card := sdMoveSourceRoot()
	if card == "" {
		return
	}
	dir := filepath.Join(card, sdMoveDirName)
	mpath := filepath.Join(dir, sdMoveManifestName)
	if info, err := os.Lstat(mpath); err != nil || !info.Mode().IsRegular() {
		sdMoveCheckPublish(nil, 0, 0, false)
		return
	}
	st, ok := sdMoveLoadCheck(dir)
	if ok && st.Phase == sdMoveCheckDone && !again {
		sdMoveCheckPublish(&st, st.Files, st.Files, false)
		return
	}
	data, err := os.ReadFile(mpath)
	var m sdMoveManifest
	if err == nil {
		m, err = sdMoveParseManifest(data)
	}
	if err != nil {
		st = sdMoveCheckState{Phase: sdMoveCheckDone, Err: firstLine(err.Error())}
		sdMoveSaveCheck(dir, &st)
		sdMoveCheckPublish(&st, 0, 0, false)
		msg, kind := sdMoveCheckSummary(&st)
		ShowToast(msg, kind)
		return
	}
	if !ok || again || st.Files != len(m.Files) || st.Phase == sdMoveCheckDone {
		st = sdMoveCheckState{Phase: sdMoveCheckSizes, Files: len(m.Files), Bytes: m.TotalBytes}
	}
	st.MoveState = m.State
	st.LeftOut = append([]string(nil), m.LeftOut...)
	st.NotOnCard = len(m.NotCopied) + len(m.Unreadable) + len(m.Failed)
	if m.State != sdMoveStateVerified {
		st.Phase = sdMoveCheckDone
		sdMoveSaveCheck(dir, &st)
		sdMoveCheckPublish(&st, 0, 0, false)
		msg, kind := sdMoveCheckSummary(&st)
		ShowToast(msg, kind)
		return
	}
	total := len(m.Files)
	if st.Phase == sdMoveCheckSizes {
		for i, f := range m.Files {
			if exitRequested.Load() {
				return
			}
			sdMoveCheckOneSize(card, f, &st)
			if i%500 == 0 {
				sdMoveCheckPublish(&st, i, total, false)
			}
		}
		st.Phase, st.Next = sdMoveCheckContents, 0
		sdMoveSaveCheck(dir, &st)
		sdMoveCheckPublish(&st, 0, total, false)
		msg, kind := sdMoveCheckSummary(&st)
		ShowToast(msg, kind)
	}
	buf := make([]byte, 1<<20)
	lastSave, lastPub := time.Now(), time.Time{}
	for i := st.Next; i < total; i++ {
		for (sdMoveCheckPaused.Load() || gameScreenSuspended()) && !exitRequested.Load() {
			if time.Since(lastPub) > time.Second {
				sdMoveCheckPublish(&st, i, total, true)
				lastPub = time.Now()
			}
			time.Sleep(2 * time.Second)
		}
		if exitRequested.Load() {
			st.Next = i
			sdMoveSaveCheck(dir, &st)
			return
		}
		sdMoveCheckOneHash(card, m.Files[i], &st, buf)
		if exitRequested.Load() {
			st.Next = i
			sdMoveSaveCheck(dir, &st)
			return
		}
		st.Next = i + 1
		if time.Since(lastSave) > 30*time.Second {
			sdMoveSaveCheck(dir, &st)
			lastSave = time.Now()
		}
		if time.Since(lastPub) > 500*time.Millisecond {
			sdMoveCheckPublish(&st, i+1, total, false)
			lastPub = time.Now()
		}
	}
	st.Phase, st.Next = sdMoveCheckDone, total
	sdMoveSaveCheck(dir, &st)
	sdMoveCheckPublish(&st, total, total, false)
	msg, kind := sdMoveCheckSummary(&st)
	ShowToast(msg, kind)
}

// sdMoveCheckOneSize is the fast pass for one file: there, a regular file,
// the recorded size.
func sdMoveCheckOneSize(card string, f sdMoveFile, st *sdMoveCheckState) {
	p, err := sdMoveJoin(card, f.Path)
	if err != nil {
		st.addMissing(f.Path)
		return
	}
	info, err := os.Lstat(p)
	switch {
	case err != nil || !info.Mode().IsRegular():
		if f.Live {
			st.OwnN++
		} else {
			st.addMissing(f.Path)
		}
	case info.Size() != f.Size:
		switch {
		case f.Live:
			st.OwnN++
		case sdMoveSameMtime(info.ModTime().UnixNano(), f.Mtime):
			st.addDamaged(f.Path)
		default:
			st.ChangedN++
		}
	}
}

// sdMoveCheckOneHash is the slow pass for one file. A file the fast pass
// already counted (missing, or another size) is skipped.
func sdMoveCheckOneHash(card string, f sdMoveFile, st *sdMoveCheckState, buf []byte) {
	p, err := sdMoveJoin(card, f.Path)
	if err != nil {
		return
	}
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.Size {
		return
	}
	sum, err := sdMoveHashFile(p, buf)
	if exitRequested.Load() {
		// Cut short by JukaHub closing: this file is checked again next time.
		return
	}
	switch {
	case err == nil && sum == f.SHA256:
		return
	case f.Live:
		st.OwnN++
	case err != nil || sdMoveSameMtime(info.ModTime().UnixNano(), f.Mtime):
		st.addDamaged(f.Path)
	default:
		st.ChangedN++
	}
}

// sdMoveHashFile is the SHA-256 of a file, read from the card (its pages are
// dropped afterwards, so a whole-card check does not push everything else out
// of memory).
func sdMoveHashFile(p string, buf []byte) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	for {
		if exitRequested.Load() {
			return "", errors.New("JukaHub is closing")
		}
		k, rerr := f.Read(buf)
		if k > 0 {
			h.Write(buf[:k])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	sdMoveDropCache(f)
	return hex.EncodeToString(h.Sum(nil)), nil
}
