package main

// safeboot.go — the recovery chord and the boot guard.
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

// ---------------------------------------------------------------------------
// Boot guard — crash-loop protection for a misbehaving update
// ---------------------------------------------------------------------------
//
// The recovery chord needs a person who knows about it. When the newest release
// fails on the first frames, nobody is pressing L+R+Start — the handheld boots
// into the broken build, dies, restarts itself (or the user does), forever.
//
// The boot guard is the automatic half: a small marker file records each boot.
// If a previous boot ended without the marker being cleared (the cleanup path
// deletes it on every graceful exit), that boot failed. Three *fast* failed
// boots of the same build within a recent update window roll the previous
// binary back automatically, exactly like the chord, and this launch continues
// in safe mode until the restored version takes over on the next start.
//
// The fast-failure window (90 s) is deliberate: a slow boot followed by a
// power cut is a long, good session, and the handheld is power-cycled often
// enough that counting those would fabricate failures. Only early death counts.
// The recent-update window (72 h) keeps the guard out of everyday life: an
// install nobody has updated in the last three days is not what a crash loop
// should rewrite. JUKAHUB_NO_BOOTGUARD=1 disables it for debugging.

const (
	// bootGuardFile lives in the patch state directory.
	bootGuardFile = "bootguard.json"
	// bootGuardThreshold consecutive fast failed boots trigger the rollback.
	bootGuardThreshold = 3
	// bootGuardFastExit is the longest a boot may have run to count as a
	// failed boot. Longer than this means the build started fine and died
	// later (power cut, firmware kill), which is not the build's fault.
	bootGuardFastExit = 90 * time.Second
	// bootGuardUpdateWindow limits automatic rollbacks to installs whose last
	// update is recent. Older installs fall back to the manual recovery chord.
	bootGuardUpdateWindow = 72 * time.Hour
)

// bootGuardFileState is the durable crash-loop counter.
type bootGuardFileState struct {
	Version  string    `json:"version"`   // build/release stamp of the counted boot
	Unclean  int       `json:"unclean"`   // consecutive fast failed boots
	LastBoot time.Time `json:"last_boot"` // when that boot started (UTC)
}

// bootGuardRollback is the revert to use, a variable so tests can watch it.
var bootGuardRollback = rollbackLastUpdateTo

// bootGuardVersionStamp picks the stamp identifying the running build. The
// release tag when one exists (updates set it), otherwise the compile stamp.
func bootGuardVersionStamp() string {
	if info := readInstalledReleaseInfo(); info.Tag != "" {
		return info.Tag
	}
	if t, ok := parseReleaseTimestamp(buildTime); ok {
		return t.Format(time.RFC3339)
	}
	return "build:" + buildDate
}

// readBootGuardFile loads the counter, reporting whether one existed.
func readBootGuardFile(dir string) (bootGuardFileState, bool) {
	data, err := os.ReadFile(filepath.Join(dir, bootGuardFile))
	if err != nil {
		return bootGuardFileState{}, false
	}
	var s bootGuardFileState
	if json.Unmarshal(data, &s) != nil {
		// A garbled marker must never suppress the guard: treat as no marker.
		return bootGuardFileState{}, false
	}
	return s, true
}

// writeBootGuardFile durably records the counter for the next boot.
func writeBootGuardFile(dir string, s bootGuardFileState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = AtomicWrite(filepath.Join(dir, bootGuardFile), append(data, '\n'), 0o600)
}

// bootGuardUpdateIsRecent reports whether the last recorded update sits inside
// the rollback window, so a crash loop is likely to be that update's fault.
func bootGuardUpdateIsRecent(now time.Time, updatedAt string) bool {
	t, ok := parseReleaseTimestamp(updatedAt)
	if !ok {
		return false
	}
	d := now.Sub(t)
	return d >= 0 && d <= bootGuardUpdateWindow
}

// initBootGuard runs the crash-loop check at startup, after the recovery
// chord: an explicit safe-mode request or a held chord disables the guard.
func initBootGuard() {
	if safeMode.Active {
		return
	}
	if strings.TrimSpace(os.Getenv("JUKAHUB_NO_BOOTGUARD")) != "" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		Log().Warn("boot guard: cannot locate the running executable", "err", err)
		return
	}
	dir, err := PatchStateDir()
	if err != nil {
		Log().Warn("boot guard: no patch state directory", "err", err)
		return
	}
	patchStateMu.Lock()
	updatedAt := patchState.LastUpdateAt
	patchStateMu.Unlock()
	applyBootGuard(dir, self, bootGuardVersionStamp(), updatedAt, time.Now(), rollbackLastUpdateTo)
}

// applyBootGuard is the decision core, split out so every rule (fast-fail
// counting, threshold, recency gate, rollback) is testable with a temp dir and
// a controlled clock. rollback is a parameter for the same reason.
func applyBootGuard(dir, self, version, updatedAt string, now time.Time, rollback func(self string) (name, version string, err error)) {
	marker := bootGuardFileState{Version: version, LastBoot: now.UTC()}
	prev, markerExisted := readBootGuardFile(dir)
	unclean := 0
	if markerExisted && prev.Version == version {
		// The previous boot did not reach its cleanup, so the marker is still
		// there. Count it as failed when it ended fast; a long run means the
		// build worked and something else killed the process.
		run := now.Sub(prev.LastBoot)
		if run < 0 || run <= bootGuardFastExit {
			// run < 0: the handheld has no RTC and the clock reset over the
			// power cycle — uptime before NTP is short by definition.
			unclean = prev.Unclean + 1
		}
	}

	if unclean >= bootGuardThreshold {
		if bootGuardUpdateIsRecent(now, updatedAt) {
			name, v, err := rollback(self)
			safeboot.AutoRevert = true
			if err != nil {
				safeboot.Err = err
				Log().Warn("boot guard: rollback attempt failed", "err", err)
			} else {
				safeboot.RolledBack = name
				safeboot.Version = v
				safeMode = safeModeState{Active: true, Reason: "crash-loop auto-rollback"}
				Log().Warn("boot guard: repeated failed boots, previous JukaHub restored"+
					" (the restored build takes over on the next start)",
					"set", name, "version", v)
			}
		} else {
			Log().Warn("boot guard: repeated failed boots, but no recent update to roll back")
		}
		// Restart the counter either way: a wedge must not grow forever, and
		// the restored build gets a full set of chances.
		unclean = 0
	}
	marker.Unclean = unclean
	writeBootGuardFile(dir, marker)
}

// clearBootGuard deletes the counter. It runs in the exit cleanup, so a boot
// that reached it was healthy — the next boot starts counting from zero.
func clearBootGuard() {
	dir, err := PatchStateDir()
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(dir, bootGuardFile))
}
