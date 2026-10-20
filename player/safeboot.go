package main

// safeboot.go — the recovery chord.
//
// Safe mode already exists (--safe-mode, JUKAHUB_SAFE_MODE, safe-mode.flag) but
// all three need a working file manager, a terminal or a USB cable. When an
// update leaves a handheld that cannot start, none of those are at hand.
//
// So: hold L + R + Start while JukaHub boots. The chord must be held for the
// whole detection window, which makes it hard to trigger by accident, and it
// does three things:
//
//  1. turns on safe mode for this launch (plugins, updates and network work are
//     skipped), so a bad plugin or feed cannot stop the shell from starting;
//  2. restores the previous JukaHub binary from the newest backup set, so the
//     next launch runs the version that worked;
//  3. says so in the log and in Diagnostics.
//
// It deliberately does not restore the user's config: a backup set may be old,
// and silently reverting settings a user made after the update would be its own
// bug. The executable is the file an update replaces.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// safebootHoldWindow is how long the chord must stay down.
	safebootHoldWindow = 1200 * time.Millisecond
	// safebootPollInterval is the sampling cadence inside the window.
	safebootPollInterval = 60 * time.Millisecond
)

// safebootState records what the chord did, for Diagnostics and tests.
type safebootState struct {
	Triggered  bool
	RolledBack string // backup set name that was restored, empty if none
	Version    string // version recorded in that backup set
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

// rollbackLastUpdateTo restores the newest backed-up copy of self. It never
// removes the backup set, so a second attempt (or a manual restore) still works.
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
	if err := copyFile(src, self); err != nil {
		return "", "", err
	}
	if file.Mode != 0 {
		_ = os.Chmod(self, os.FileMode(file.Mode))
	}
	version = ""
	if m, err := loadBackupManifest(name); err == nil && m != nil {
		version = m.Version
	}
	return name, version, nil
}

// initSafeboot runs the chord check and the rollback. It is a no-op when safe
// mode was already requested another way, so an explicit flag is never
// overridden by a button that happens to be pressed.
func initSafeboot() {
	if safeMode.Active {
		return
	}
	if !detectSafebootHold(safebootHoldWindow) {
		return
	}
	safeMode = safeModeState{Active: true, Reason: "recovery chord (L+R+Start)"}
	safeboot.Triggered = true
	Log().Warn("safe mode enabled: recovery chord held at boot")

	self, err := os.Executable()
	if err != nil {
		safeboot.Err = err
		Log().Warn("safe boot: cannot locate the running executable", "err", err)
		return
	}
	name, version, err := rollbackLastUpdateTo(self)
	if err != nil {
		safeboot.Err = err
		Log().Warn("safe boot: no update to roll back", "err", err)
		return
	}
	safeboot.RolledBack = name
	safeboot.Version = version
	Log().Warn("safe boot: previous JukaHub restored from backup", "set", name, "version", version,
		"restartRequired", runtime.GOOS != "windows")
}

// safebootDiagnostic is the one-line Diagnostics fact, empty when the chord was
// not used.
func safebootDiagnostic() string {
	if !safeboot.Triggered {
		return ""
	}
	if safeboot.RolledBack == "" {
		if safeboot.Err != nil {
			return "Safe boot    : on (no rollback: " + safeboot.Err.Error() + ")"
		}
		return "Safe boot    : on"
	}
	version := safeboot.Version
	if version == "" {
		version = "previous build"
	}
	return fmt.Sprintf("Safe boot    : on (rolled back to %s)", version)
}
