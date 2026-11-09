package main

// safeboot.go — the recovery chord, and putting the previous JukaHub back.
//
// Safe mode already exists (--safe-mode, JUKAHUB_SAFE_MODE, safe-mode.flag) but
// all three need a working file manager, a terminal or a USB cable. When an
// update leaves a handheld that cannot start, none of those are at hand.
//
// So: hold L + R + Start (Ctrl + Alt + R on a keyboard) while JukaHub boots.
// The chord must be held for the whole detection window, which makes it hard to
// trigger by accident, and it:
//
//  1. turns on safe mode for this launch (plugins, updates and network work are
//     skipped), so a bad plugin or feed cannot stop the shell from starting;
//  2. opens the recovery card as soon as the screen is up (recoverycard.go):
//     the previous version, the last good settings, safe mode, or settings
//     reset to the shipped ones — games and saves are never touched, and the
//     destructive choices ask first and back the settings up before anything;
//  3. says so in the log and in Diagnostics.
//
// Nothing is reverted without a choice: a backup may be old, and silently
// putting back settings or a binary would be its own bug. The automatic half,
// for a build that cannot even reach the card, is the boot guard
// (bootguard.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// safebootHoldWindow is how long the chord must stay down.
	safebootHoldWindow = 1200 * time.Millisecond
	// safebootPollInterval is the sampling cadence inside the window.
	safebootPollInterval = 60 * time.Millisecond
)

// safebootState records what the chord (or the boot guard) did, for
// Diagnostics and tests.
type safebootState struct {
	Triggered  bool   // the recovery chord was held at boot
	AutoRevert bool   // the boot guard rolled back on its own
	RolledBack string // saved version or backup set that was restored, empty if none
	Version    string // the version it holds
	Restarts   bool   // JukaHub restarts into it now (Version history); else it takes over at the next start
	Err        error
}

var safeboot safebootState

// safebootHeldAll reports whether the chord was down in every sample. One
// missed sample means a tap, not a hold.
func safebootHeldAll(samples []bool) bool {
	if len(samples) == 0 {
		return false
	}
	for _, pressed := range samples {
		if !pressed {
			return false
		}
	}
	return true
}

// readSafebootChord samples the physical buttons. A controller is preferred
// because that is what the handheld exposes; the keyboard fallback (Ctrl+Alt+R)
// keeps the feature usable on a desktop build.
func readSafebootChord() bool {
	sdl.PumpEvents()
	if n := sdl.NumJoysticks(); n > 0 {
		pad := sdl.GameControllerOpen(0)
		if pad != nil {
			defer pad.Close()
			return pad.Button(sdl.CONTROLLER_BUTTON_LEFTSHOULDER) != 0 &&
				pad.Button(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER) != 0 &&
				pad.Button(sdl.CONTROLLER_BUTTON_START) != 0
		}
	}
	keys := sdl.GetKeyboardState()
	return keys[sdl.SCANCODE_LCTRL] != 0 && keys[sdl.SCANCODE_LALT] != 0 && keys[sdl.SCANCODE_R] != 0
}

// detectSafebootHold polls the chord for window. Any release aborts at once, so
// a stray button press during boot costs no delay beyond the first sample.
func detectSafebootHold(window time.Duration) bool {
	if window <= 0 {
		return false
	}
	var samples []bool
	deadline := time.Now().Add(window)
	for {
		pressed := readSafebootChord()
		samples = append(samples, pressed)
		if !pressed {
			return false
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(safebootPollInterval)
	}
	return safebootHeldAll(samples)
}

// loadBackupManifest reads one set's manifest from the patch state directory.
func loadBackupManifest(name string) (*BackupManifest, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "backups", filepath.Base(name), "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// latestExecutableBackup picks the newest set that contains self. The loader is
// a parameter so the selection logic is testable without a real state dir.
func latestExecutableBackup(sets []string, self string, load func(string) (*BackupManifest, error)) (string, BackupFile, bool) {
	for _, name := range sets { // ListBackupSets is newest-first
		m, err := load(name)
		if err != nil {
			continue
		}
		for _, f := range m.Files {
			if sameFilePath(f.Path, self) {
				return name, f, true
			}
		}
	}
	return "", BackupFile{}, false
}

// rollbackLastUpdateTo restores the newest backed-up copy of self from the Patch
// backup sets (installs from before Version history kept the replaced build
// there). It never removes the backup set, so a second attempt (or a manual
// restore) still works. The restored build takes over at the next start.
func rollbackLastUpdateTo(self string) (name, version string, err error) {
	if self == "" {
		return "", "", errors.New("cannot locate the running executable")
	}
	sets, err := ListBackupSets()
	if err != nil {
		return "", "", err
	}
	name, file, ok := latestExecutableBackup(sets, self, loadBackupManifest)
	if !ok {
		return "", "", errors.New("no backup set contains this executable")
	}
	dir, err := PatchStateDir()
	if err != nil {
		return "", "", err
	}
	src := filepath.Join(dir, "backups", filepath.Base(name), filepath.Base(file.Rel))
	if err := replaceRunningExecutable(src, self, os.FileMode(file.Mode)); err != nil {
		return "", "", err
	}
	version = ""
	if m, err := loadBackupManifest(name); err == nil && m != nil {
		version = m.Version
	}
	return name, version, nil
}

// replaceRunningExecutable puts src in place of self while self may be the
// process that is running. Writing into a running executable fails (ETXTBSY
// on Linux, a sharing violation on Windows), which is why the copy lands beside
// it first: Linux then renames over the old name (the running process keeps
// its open inode), and Windows, which refuses that rename, moves the running
// file aside to self.old first — a running .exe may be renamed.
func replaceRunningExecutable(src, self string, mode os.FileMode) error {
	tmp := self + ".restore"
	_ = os.Remove(tmp)
	if err := copyFile(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if mode == 0 {
		mode = 0o755
	}
	// copyFile creates the file 0600; an executable that lost its x bit does
	// not start. FAT cards ignore the mode, which is fine there.
	_ = os.Chmod(tmp, mode)
	if runtime.GOOS == "windows" {
		old := self + ".old"
		_ = os.Remove(old)
		if err := os.Rename(self, old); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, self); err != nil {
			_ = os.Rename(old, self)
			_ = os.Remove(tmp)
			return err
		}
		return nil
	}
	if err := os.Rename(tmp, self); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// rollbackToPreviousVersion puts the previous JukaHub back: the saved version
// in Version history released just before the running build
// (rollbackPreviousEntry), through the journaled swap helper an update uses
// (JukaHub restarts into it), or — on an install whose history is empty — the
// newest executable in the Patch backup sets, ready for the next start. It runs
// after InitPatchModule, which loads the history and the patch state.
func rollbackToPreviousVersion(config *Config) (name, version string, err error) {
	if entries, _ := uhistEntriesSnapshot(); len(entries) > 0 {
		e, ok := rollbackPreviousEntry(entries)
		if !ok {
			return "", "", errors.New("no version released before this one is saved in Version history")
		}
		if err := uhistRollbackOp(context.Background(), config, e.ID); err != nil {
			return "", "", err
		}
		return e.ID, uhistLabel(e.Tag, e.Date, e.Version), nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	return rollbackLastUpdateTo(self)
}

// rollbackPreviousEntry is rollbackPickVersion for the running build.
func rollbackPreviousEntry(entries []uhistEntry) (uhistEntry, bool) {
	running, known := installedReleaseTime()
	return rollbackPickVersion(entries, running, known, uhistSkippedTag(), readInstalledReleaseInfo().Tag)
}

// rollbackEntryReleaseTime is when a saved version was released: its stamped
// date, else the time in its tag.
func rollbackEntryReleaseTime(e uhistEntry) (time.Time, bool) {
	if t, ok := parseReleaseTimestamp(e.Date); ok {
		return t, true
	}
	return releaseTimeFromTag(e.Tag)
}

// rollbackPickVersion chooses the saved version "previous version" means: the
// one released last before the running build. The history's own order (when
// each was replaced, by the device's clock) is not used: without an RTC that
// clock can be years off, and after a rollback the newest entry is the build
// just left. The release this device was rolled back from (skipTag) and the
// running build's tag are never chosen, nor is a version released at or after
// the running one. When the running build's release time is unknown, or no
// dated version qualifies, an undated one is taken, newest saved first.
func rollbackPickVersion(entries []uhistEntry, running time.Time, haveRunning bool, skipTag, runningTag string) (uhistEntry, bool) {
	skipTag, runningTag = strings.TrimSpace(skipTag), strings.TrimSpace(runningTag)
	var best, undated uhistEntry
	var bestAt time.Time
	found, haveUndated := false, false
	for _, e := range entries { // newest saved first
		tag := strings.TrimSpace(e.Tag)
		if tag != "" && (strings.EqualFold(tag, skipTag) || strings.EqualFold(tag, runningTag)) {
			continue
		}
		at, dated := rollbackEntryReleaseTime(e)
		if !dated || !haveRunning {
			if !haveUndated {
				undated, haveUndated = e, true
			}
			continue
		}
		if !at.Before(running) {
			continue
		}
		if !found || at.After(bestAt) {
			best, bestAt, found = e, at, true
		}
	}
	if found {
		return best, true
	}
	return undated, haveUndated
}

// rollbackBackupSetCandidate finds what rollbackLastUpdateTo would restore on
// an install whose Version history is empty: the name and version of the
// newest Patch backup set that holds this executable. It reads the backup
// folder, so it runs off the render thread.
func rollbackBackupSetCandidate() (name, version string, ok bool) {
	self, err := os.Executable()
	if err != nil {
		return "", "", false
	}
	sets, err := ListBackupSets()
	if err != nil || len(sets) == 0 {
		return "", "", false
	}
	name, _, ok = latestExecutableBackup(sets, self, loadBackupManifest)
	if !ok {
		return "", "", false
	}
	if m, err := loadBackupManifest(name); err == nil && m != nil {
		version = m.Version
	}
	return name, version, true
}

// rollbackBackupSetNow puts that executable back for the recovery card, under
// the Patch operation lock. It copies tens of megabytes: off the render
// thread. The restored build takes over at the next start.
func rollbackBackupSetNow() error {
	if !tryBeginPatchOp() {
		return errors.New("another Patch operation is running; try again when it has finished")
	}
	defer endPatchOp()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	_, _, err = rollbackLastUpdateTo(self)
	return err
}

// safebootRemoveOldExecutable deletes the copy replaceRunningExecutable moved
// aside on Windows (a running .exe cannot be deleted, only renamed), once the
// build that was running from it has exited. Best-effort: when the old
// process is still closing, the next start tries again.
func safebootRemoveOldExecutable() {
	if runtime.GOOS != "windows" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	if err := os.Remove(self + ".old"); err == nil {
		logInfo("safeboot", "removed the build a rollback replaced (%s.old)", filepath.Base(self))
	}
}

// initSafeboot runs the chord check. It is a no-op when safe mode was already
// requested another way, so an explicit flag is never overridden by a button
// that happens to be pressed. What to restore is the user's choice on the
// recovery card (recoverycard.go), opened once the screen is up.
func initSafeboot() {
	safebootRemoveOldExecutable()
	if safeMode.Active {
		return
	}
	if !detectSafebootHold(safebootHoldWindow) {
		return
	}
	safeMode = safeModeState{Active: true, Reason: "recovery chord (L+R+Start)"}
	safeboot.Triggered = true
	Log().Warn("safe mode enabled: recovery chord held at boot; the recovery card opens with the first frame")
}

// safebootDiagnostic is the one-line Diagnostics fact, empty when neither the
// chord nor the boot guard did anything.
func safebootDiagnostic() string {
	if !safeboot.Triggered && !safeboot.AutoRevert {
		return ""
	}
	label := "Safe boot"
	cause := ""
	if safeboot.AutoRevert {
		label = "Boot guard"
		cause = " auto"
	}
	if safeboot.RolledBack == "" {
		if safeboot.Err != nil {
			return fmt.Sprintf("%-13s: on%s (no rollback: %s)", label, cause, safeboot.Err.Error())
		}
		return fmt.Sprintf("%-13s: on%s", label, cause)
	}
	version := safeboot.Version
	if version == "" {
		version = "previous build"
	}
	return fmt.Sprintf("%-13s: on%s (rolled back to %s)", label, cause, version)
}
