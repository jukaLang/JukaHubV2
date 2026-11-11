package main

// update_resume.go — finishing a self-update the swap helper stopped partway.
//
// The swap helper (writeSwapScriptFor) copies a staged release over the
// installation one file at a time: the binary among the first, the release
// stamp (release-info.json) last. When it stops after the binary — a folder it
// could not write into, a full card, the power key — the next start runs the
// NEW build, and the journal check used to roll the swap back from there. That
// cannot restore the old binary: it is the running process, which a rollback
// skips. So the new JukaHub ran on the old release's files, release-info.json
// still named the old release, and the same update was offered again on every
// start. That was the "keeps asking me to update" loop of the first release
// that shipped files in bin/ and lib/ (November 2026): the helper of every
// earlier build could not create those folders (ensureInstallDirs).
//
// When the running executable is the staged release's binary, the startup now
// finishes the update instead of undoing it: it copies the rest of the staged
// files the way the helper copies them (folders made first, each file written
// to a temporary copy renamed over the old one, the release stamp last),
// commits the journal the way the helper does, and confirmAppliedUpdate then
// records the release as installed. If anything fails on the way, the journal
// is left to the ordinary rollback, exactly as before.
//
// That is also what gets every installation out of the loop in one press: the
// fixed release is installed by the old build's helper, which still stops at
// the first new folder — but only after it has put the fixed binary in place,
// and the fixed binary finishes the rest when it starts.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// swapHelperLogName is the file the POSIX swap helper writes its errors to,
// beside the script.
const swapHelperLogName = "apply-update.log"

// resumeTempSuffix names the copy a resumed swap writes before renaming it
// over the file. It is not the helper's ".new": a helper still copying when
// JukaHub starts (launch.sh gives up waiting after 30 s) must not write into
// the same temporary file.
const resumeTempSuffix = ".resume-new"

// updateResumeReplacedConfig is set when a resumed swap replaced the design
// file after main had already loaded it. The config watcher reloads it as soon
// as it starts (StartConfigWatcher), so the session runs on the new release's
// design and a Save there cannot write the old one back over it.
var updateResumeReplacedConfig atomic.Bool

// swapParentDir is the folder of a path in the swap helper's own syntax: up to
// the last separator of either kind, so a helper written for the other OS gets
// the right folder too. "" when the path has no folder part.
func swapParentDir(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i <= 0 {
		return ""
	}
	return p[:i]
}

// resumeInterruptedUpdates finishes, at startup, every app swap whose helper
// stopped after the new binary was in place. It runs before the version
// history adopts committed swaps (uhistAdoptCommittedSwaps), which is what
// keeps a finished swap's backups as a saved version, and before the journal
// check (checkInterruptedJournal) rolls back whatever is left.
func resumeInterruptedUpdates() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	resumeInterruptedUpdatesFor(self)
}

// resumeInterruptedUpdatesFor is resumeInterruptedUpdates for a given running
// executable, so a test can stand in for it. It returns how many it finished.
func resumeInterruptedUpdatesFor(self string) int {
	resumed := 0
	for _, p := range pendingJournals() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var j journal
		if json.Unmarshal(data, &j) != nil || j.Kind != "juka-update" {
			continue
		}
		j.path = p
		j.skipPath = self
		if resumeInterruptedUpdate(&j, self) {
			resumed++
		}
	}
	return resumed
}

// resumeInterruptedUpdate finishes the update journal j was protecting when
// its helper stopped after replacing the running executable (self). It reports
// whether it did; false leaves j to be rolled back.
func resumeInterruptedUpdate(j *journal, self string) bool {
	if j == nil || j.Kind != "juka-update" || strings.TrimSpace(j.Target) == "" || self == "" {
		return false
	}
	exe, err := filepath.Abs(self)
	if err != nil {
		return false
	}
	// The journal must have been protecting this very executable. On Windows
	// the Patch folder is per user and shared by every copy of JukaHub, and a
	// second copy must not finish the first one's swap into its own folder.
	if !journalCovers(j, exe) {
		return false
	}
	staging, err := stagingDirFor(j.Target)
	if err != nil {
		return false
	}
	// Only a tree that was unpacked and stamped in full may finish an install
	// (finishStagedRelease writes the marker last).
	if info, err := os.Stat(stagedReadyMarker(staging)); err != nil || !info.Mode().IsRegular() {
		return false
	}
	newBin := findStagedBinary(staging)
	if newBin == "" {
		return false
	}
	if same, err := sameFileContents(exe, newBin); err != nil || !same {
		// The helper stopped before the binary: the build running now is the
		// one the journal protects, and the rollback restores it faithfully.
		return false
	}
	files, err := stagedInstallFiles(filepath.Dir(newBin), filepath.Dir(exe))
	if err != nil {
		logPatch("update: cannot finish installing %s: %v", j.Target, err)
		return false
	}
	if tail := swapHelperLogTail(staging); tail != "" {
		logPatch("update: the swap helper for %s stopped: %s", j.Target, tail)
	}
	if err := finishStagedCopy(files, exe); err != nil {
		logPatch("update: could not finish installing %s (%v); undoing it instead", j.Target, err)
		return false
	}
	// Committed the way the helper commits: the journal file goes and its
	// backups stay, for the version history to keep (uhistAdoptCommittedSwaps)
	// or the startup purge to remove. A journal that will not go is emptied, so
	// a later start has nothing of it to undo; one that can be neither is
	// rolled back now rather than undone later, after the staged tree it would
	// need to finish again has been deleted.
	if j.path != "" {
		if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			j.Entries = nil
			data, mErr := json.Marshal(j)
			if mErr != nil || os.WriteFile(j.path, data, 0o600) != nil {
				logPatch("update: finished installing %s but could not commit its journal (%v); undoing it instead", j.Target, err)
				return false
			}
		}
	}
	invalidateInstalledReleaseInfo()
	logPatch("update: finished installing %s, which the swap helper had left half done", j.Target)
	return true
}

// journalCovers reports whether j recorded path among the files it protects.
func journalCovers(j *journal, path string) bool {
	for _, e := range j.Entries {
		if sameFilePath(e.Path, path) {
			return true
		}
	}
	return false
}

// noteUndoneSwap is the other outcome: the startup is rolling back a swap that
// could not be finished. The helper's own words go to the log and the user is
// told, where the rollback used to pass in silence and the update looked as if
// it had never been pressed.
func noteUndoneSwap(j *journal) {
	// An emptied journal is a finished swap that could not delete it: there is
	// nothing to undo and nothing to report.
	if j == nil || j.Kind != "juka-update" || len(j.Entries) == 0 {
		return
	}
	if staging, err := stagingDirFor(j.Target); err == nil {
		if tail := swapHelperLogTail(staging); tail != "" {
			logPatch("update: the swap helper for %s stopped: %s", j.Target, tail)
		}
	}
	// "Install" covers both swaps that write these journals: an update, and a
	// rollback to a saved version (uhist.go).
	notifPush("The last JukaHub install could not finish and was undone. You can try it again.", notifWarning)
}

// finishStagedCopy copies a staged release over the installation the way the
// swap helper does, except the running executable (already the staged one) and
// the files that already match (the ones the helper got to).
func finishStagedCopy(files []stagedInstallFile, exe string) error {
	if err := ensureInstallDirs(files, filepath.Dir(exe)); err != nil {
		return err
	}
	for _, f := range files {
		if sameFilePath(f.Dest, exe) {
			continue
		}
		if same, err := sameFileContents(f.Src, f.Dest); err == nil && same {
			continue
		}
		if err := replaceFileFrom(f.Src, f.Dest); err != nil {
			return err
		}
		if name := strings.ToLower(f.Name); name == "jukaconfig.json" || name == "jukaconfig.xml" {
			updateResumeReplacedConfig.Store(true)
		}
	}
	return nil
}

// replaceFileFrom puts a copy of src at dest through a temporary file renamed
// over it, as the helper's cp + mv does: whoever has dest open (the launcher
// reading launch.sh) keeps the old file, and nobody sees a half-written one.
func replaceFileFrom(src, dest string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	tmp := dest + resumeTempSuffix
	if err := copyFile(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The staged mode: the helpers in bin/ must stay executable. A FAT card
	// may refuse, as it may refuse the helper's chmod; its mount mode applies.
	_ = os.Chmod(tmp, info.Mode().Perm())
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", dest, err)
	}
	return nil
}

// sameFileContents reports whether a and b hold the same bytes: the same size,
// then the same SHA-256. A missing b is simply not the same.
func sameFileContents(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !ia.Mode().IsRegular() || !ib.Mode().IsRegular() || ia.Size() != ib.Size() {
		return false, nil
	}
	ha, err := SHA256File(a)
	if err != nil {
		return false, err
	}
	hb, err := SHA256File(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

// swapHelperLogTail is the end of what a swap helper wrote to its log in dir,
// as one line for the patch log; "" when there is none.
func swapHelperLogTail(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, swapHelperLogName))
	if err != nil {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	out := strings.Join(lines, " | ")
	if len(out) > 600 {
		out = strings.ToValidUTF8(out[len(out)-600:], "")
	}
	return out
}

// pruneStaleStagingTrees removes the release trees (staging/app-*) and their
// ready markers that updates which never finished left behind: a failed swap
// keeps its tree for a retry, and a retry of a newer release stages another.
// It runs once a release is confirmed as installed, when no staged release is
// waiting to be applied.
func pruneStaleStagingTrees(base string) {
	dir := filepath.Join(base, "staging")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "app-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			logPatch("update: could not remove the stale staging %s: %v", e.Name(), err)
		}
	}
}
