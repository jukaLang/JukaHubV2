package main

// files_dualpane_xfer.go — Copy to other pane and Move to other pane
// (files_dualpane.go): the marked rows, or the one under the cursor, go to the
// other pane's folder on a goroutine, one transfer at a time.
//
// Each item goes through the same helpers Paste uses (files_manager.go):
// feFreeTarget names it ("name (2)" when the name is taken) and feCopyTree
// copies it, a link as the file it points at. A move inside one card is a
// rename (instant, nothing copied); across cards or drives it is a copy and
// then the original removed, only after the copy succeeded — Paste's rule.
// The paths are resolved against the Files root on the render thread before
// the job starts, so the goroutine never reads the Files state. The other
// pane's bar shows "Copying 2 of 5 here…" with a progress line, the Y sheet's
// row shows the item being written (a second press there stops after it),
// and filesDualDrain (main loop, every frame) reports the end and reads both
// panes again.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// filesDualItem is one thing to send, resolved on the render thread.
type filesDualItem struct {
	name     string // the row's name, which the copy keeps
	lexical  string // the path as listed (a link itself)
	resolved string // what it is (a link's target)
	link     bool
}

// filesDualOutcome is a finished transfer.
type filesDualOutcome struct {
	move    bool
	total   int
	done    int
	failed  int
	stopped bool
	lastErr error
	dest    string
	first   string // the first new path, for the cursor
}

// filesDualXfer is the one transfer at a time.
var filesDualXfer struct {
	mu      sync.Mutex
	running bool
	move    bool
	dest    string
	done    int
	total   int
	current string
	stop    bool
	outcome *filesDualOutcome
}

// filesDualStatus is the transfer as a frame shows it.
type filesDualStatus struct {
	running bool
	move    bool
	dest    string
	done    int
	total   int
	current string
}

func filesDualSnapshot() filesDualStatus {
	x := &filesDualXfer
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.running {
		return filesDualStatus{}
	}
	return filesDualStatus{running: true, move: x.move, dest: x.dest, done: x.done, total: x.total, current: x.current}
}

// filesDualStop asks the running transfer to stop after the item it is on.
func filesDualStop() {
	filesDualXfer.mu.Lock()
	if filesDualXfer.running {
		filesDualXfer.stop = true
	}
	filesDualXfer.mu.Unlock()
}

// filesDualSystemConfirm is filesSystemConfirm for a transfer: it asks twice
// when need is set (a move out of a system folder, or anything into one).
func filesDualSystemConfirm(label string, need bool) bool {
	if !need {
		return true
	}
	key := "sys:" + label
	if feSheetArmed(key) {
		feSheet.armed = ""
		return true
	}
	feSheet.armed, feSheet.armedAt = key, time.Now()
	PlayNavSound()
	showToast(hintSentence("System folder — press %s again to "+strings.ToLower(label), "A", "Confirm"), ToastWarn())
	return false
}

// filesDualTransferStart checks and starts a transfer of the driven pane's
// targets into the other pane's folder.
func filesDualTransferStart(config *Config, move bool) {
	if config == nil || !filesDualOn(config) {
		return
	}
	label, verb := "Copy to other pane", "copy"
	if move {
		label, verb = "Move to other pane", "move"
	}
	if filesDualSnapshot().running {
		showToast("A transfer is still running — one at a time", ToastInfo())
		return
	}
	filesDualEnsureOther(config)
	o := filesDual.other
	if filesNav.atLocations {
		showToast("Open a folder and pick what to "+verb+" first", ToastInfo())
		return
	}
	if o.atLocations || o.listedPath == "" {
		showToast("Open a folder in the other pane first", ToastInfo())
		return
	}
	dest := o.listedPath
	if info, err := os.Stat(dest); err != nil || !info.IsDir() {
		showToast("The other pane's folder is not there any more", ToastError())
		return
	}
	// The checks below also compare the real folders, so a link to one of
	// them (or a folder reached through one) is the same folder.
	destReal, curReal := filesDualRealPath(dest), filesDualRealPath(filesNav.listedPath)
	if filesSamePath(dest, filesNav.listedPath) || filesSamePath(destReal, curReal) {
		showToast("Both panes show this folder — open another one in the other pane", ToastInfo())
		return
	}
	targets := feSelectionTargets(config)
	if len(targets) == 0 {
		showToast(hintSentence("Pick a file or folder, or mark several with %s", "X", "Mark"), ToastInfo())
		return
	}
	root := feRoot(config)
	cur := filesNav.listedPath
	items := make([]filesDualItem, 0, len(targets))
	for _, t := range targets {
		resolved, err := feResolveEntry(config, t.Path)
		if err != nil {
			showToast("Cannot "+verb+" "+t.Name+": "+filesFriendlyError(err), ToastError())
			return
		}
		resolvedReal := filesDualRealPath(resolved)
		if isPathWithin(dest, resolved) || isPathWithin(destReal, resolvedReal) {
			showToast("Cannot "+verb+" "+t.Name+" into itself", ToastError())
			return
		}
		lexical, err := resolveWithinRoot(root, t.Path)
		if err != nil {
			lexical = resolved
		}
		link := false
		if info, err := os.Lstat(lexical); err == nil && info.Mode()&os.ModeSymlink != 0 {
			link = true
		}
		// A move never takes the folder being browsed or one above it
		// (feDeleteEntry's rule), whatever route its path took here.
		if move && !link && cur != "" && (isPathWithin(cur, resolved) || isPathWithin(curReal, resolvedReal)) {
			showToast("Cannot move "+t.Name+": it holds the folder you are in", ToastError())
			return
		}
		items = append(items, filesDualItem{name: t.Name, lexical: lexical, resolved: resolved, link: link})
	}
	// System folders ask twice: the source of a move, or the destination.
	if !filesDualSystemConfirm(label, (move && filesNav.systemFolder) || o.systemFolder) {
		return
	}
	x := &filesDualXfer
	x.mu.Lock()
	x.running, x.move, x.dest, x.done, x.total, x.current, x.stop, x.outcome = true, move, dest, 0, len(items), "", false, nil
	x.mu.Unlock()
	go filesDualRun(items, dest, move)
	// The marks are spent: what they named is on its way.
	feSelectionClear()
	feSheetForget()
	PlayActivateSound()
	what := "Copying "
	if move {
		what = "Moving "
	}
	showToast(what+feItemCountLabel(len(items))+" to "+filesDualOtherName()+"…", ToastInfo())
}

// filesDualRealPath is path with its links resolved, or path itself when that
// fails (it is gone, or "").
func filesDualRealPath(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// filesDualRun is the transfer's goroutine.
func filesDualRun(items []filesDualItem, dest string, move bool) {
	out := &filesDualOutcome{move: move, total: len(items), dest: dest}
	x := &filesDualXfer
	defer func() {
		if r := recover(); r != nil {
			out.failed++
			out.lastErr = errors.New("the transfer stopped unexpectedly")
			logWarn("files", "two-pane transfer panicked: %v", r)
		}
		x.mu.Lock()
		x.running, x.stop, x.current = false, false, ""
		x.outcome = out
		x.mu.Unlock()
	}()
	for _, it := range items {
		x.mu.Lock()
		stop := x.stop
		x.current = it.name
		x.mu.Unlock()
		if stop {
			out.stopped = true
			break
		}
		target, err := filesDualOne(it, dest, move)
		if err != nil {
			out.failed++
			out.lastErr = err
			logWarn("files", "two-pane transfer of %s: %v", it.name, err)
		} else {
			out.done++
			if out.first == "" {
				out.first = target
			}
		}
		x.mu.Lock()
		x.done++
		x.mu.Unlock()
	}
}

// filesDualOne sends one item into dest and returns its new path.
func filesDualOne(it filesDualItem, dest string, move bool) (string, error) {
	target, err := feFreeTarget(dest, it.name)
	if err != nil {
		return "", err
	}
	if move && !it.link {
		// One card: a rename, nothing copied. Another card or drive refuses
		// it, and the copy below does the move.
		if err := os.Rename(it.resolved, target); err == nil {
			return target, nil
		}
	}
	if err := feCopyTree(it.resolved, target, map[string]bool{it.resolved: true}, 0); err != nil {
		return "", err
	}
	if !move {
		return target, nil
	}
	// The original goes only once its copy is complete. A link is removed as
	// a link, never the folder it points at.
	if it.link {
		err = os.Remove(it.lexical)
	} else {
		err = os.RemoveAll(it.resolved)
	}
	if err != nil {
		return "", fmt.Errorf("copied, but the original could not be removed: %w", err)
	}
	return target, nil
}

// filesDualDrain reports a finished transfer on the SDL thread (the main loop
// calls it every frame): both panes are read again, the cursor goes onto the
// first new item in the pane that received it, and the toast says how it went.
func filesDualDrain(config *Config) {
	x := &filesDualXfer
	x.mu.Lock()
	out := x.outcome
	x.outcome = nil
	x.mu.Unlock()
	if out == nil {
		return
	}
	if out.done > 0 {
		if out.move {
			trophyEventN(tkFileMove, int64(out.done))
		} else {
			trophyEventN(tkFileCopy, int64(out.done))
		}
	}
	if feSheet.armed == "dual-stop" {
		feSheet.armed = ""
	}
	if config != nil && feSceneActive(config) {
		if !filesNav.atLocations {
			focused, hasFocus := feFocusEntry(config)
			feListDirectory(config)
			switch {
			case out.first != "" && filesSamePath(filesNav.listedPath, out.dest) && !feSheet.open:
				feFocusPath(config, out.first)
			case hasFocus:
				feFocusPath(config, focused.Path)
			}
		}
		filesDualRefreshOther(config)
		if out.first != "" && filesSamePath(filesDual.other.listedPath, out.dest) {
			filesDualFocusOther(out.first)
		}
	} else {
		// Read again when Files is next drawn in two panes.
		filesDual.other.stale = true
	}
	showToast(filesDualOutcomeMessage(out))
}

// filesDualOutcomeMessage is a finished transfer's toast.
func filesDualOutcomeMessage(out *filesDualOutcome) (string, sdl.Color) {
	past, verb := "Copied", "copy"
	if out.move {
		past, verb = "Moved", "move"
	}
	where := filepath.Base(out.dest)
	switch {
	case out.stopped:
		return fmt.Sprintf("Stopped — %s %d of %d", strings.ToLower(past), out.done, out.total), ToastInfo()
	case out.done == 0 && out.lastErr != nil:
		return "Cannot " + verb + ": " + filesFriendlyError(out.lastErr), ToastError()
	case out.failed > 0:
		return fmt.Sprintf("%s %d, %d failed: %s", past, out.done, out.failed, filesFriendlyError(out.lastErr)), ToastWarn()
	}
	return past + " " + feItemCountLabel(out.done) + " to " + where, ToastSuccess()
}
