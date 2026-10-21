package main

// patch_swap.go implements the JukaHub application self-update. The running
// process cannot replace its own files, so the swap is delegated to a tiny
// detached helper script (sh on the device, .bat on Windows) that:
//
//  1. waits for this process to exit,
//  2. copies the staged files over the installation (binary, SDL DLLs,
//     config, fonts, background) with retries while the old binary is locked,
//  3. deletes the journal, then relaunches JukaHub.
//
// The journal is written BEFORE the destructive step, so an interruption at
// any point is detected at next startup (checkInterruptedJournal) and rolled
// back automatically. A failing swap is never left half-applied.
//
// Release archives are the ones published by the release workflow:
// JukaHub-Windows.zip (Windows x64) and JukaHub-Trimui.zip (TrimUI arm64).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
)

const (
	patchMaxEntries     = 50000
	patchMaxExpandBytes = 2 << 30 // 2 GiB staged expansion cap
)

// Restart handshake between JukaHub and the device launcher.
//
// On the handheld the launcher script owns the screen: it pauses the firmware
// supervisors and stops MainUI before starting JukaHub, then restores them when
// JukaHub exits. An update exits the app on purpose, so the old launcher's
// restore step is exactly wrong: it brings MainUI back while the swap helper
// starts the new build, and two UIs then alternate frames on /dev/fb0 (the
// "blinking between MainUI and JukaHub" after an update).
//
// The launcher that knows this protocol runs JukaHub in a loop instead: when it
// exits with restartPendingName present it waits for the helper's verdict and
// starts the new build in the same process, so the supervisors stay paused and
// MainUI never comes back.
const (
	// launcherRestartProtocolToken is a comment line in launch.sh. The app reads
	// the launcher on disk before writing a marker, because during the update
	// that first ships this protocol the running launcher is still the old one
	// and would never read it.
	launcherRestartProtocolToken = "jukahub-restart-protocol: 1"
	restartPendingName           = ".restart-pending"
	restartSwappedName           = ".update-swapped"
	restartFailedName            = ".update-failed"
)

// deviceLauncherSupportsInPlaceRestart reports whether the launcher script on
// disk advertises the restart protocol.
func deviceLauncherSupportsInPlaceRestart(launcherPath string) bool {
	data, err := os.ReadFile(launcherPath)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), launcherRestartProtocolToken)
}

// launchedByDeviceLauncher reports whether this process was started by
// launch.sh. The environment variable is the direct signal; the parent process
// is the fallback, because a launcher installed before this protocol existed
// does not export it yet.
func launchedByDeviceLauncher() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("JUKAHUB_LAUNCHED_BY")), "launch.sh") {
		return true
	}
	if runtime.GOOS != "linux" {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", os.Getppid()))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ReplaceAll(string(raw), "\x00", " "), "launch.sh")
}

// clearStaleRestartMarkers removes handshake files a killed run left behind.
// Without this, a leftover .restart-pending would make the next update's helper
// skip its relaunch and wait for a launcher loop that is not running. The
// launcher clears the same files when it starts; this covers a build started
// straight from the stock menu, which never goes through that script.
func clearStaleRestartMarkers() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	clearStaleRestartMarkersIn(filepath.Dir(exe))
}

func clearStaleRestartMarkersIn(dir string) {
	for _, name := range []string{restartPendingName, restartSwappedName, restartFailedName} {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// prepareLauncherRestart writes the "my launcher will restart me" marker and
// returns its path, or "" when this install has no launcher that can honour it
// (a desktop build, or a launcher older than the protocol). The swap helper
// reads the same marker to decide between an in-place restart and a relaunch.
func prepareLauncherRestart(exeDir string) string {
	launcher := filepath.Join(exeDir, "launch.sh")
	if !deviceLauncherSupportsInPlaceRestart(launcher) || !launchedByDeviceLauncher() {
		return ""
	}
	marker := filepath.Join(exeDir, restartPendingName)
	if err := os.WriteFile(marker, []byte("JukaHub is restarting into an updated build.\n"), 0o644); err != nil {
		logPatch("update: could not write the restart marker (%v); the helper will relaunch the app", err)
		return ""
	}
	return marker
}

// appRestartRequested tells the render loop to exit after the swap helper was
// started, freeing the executable for replacement.
var appRestartRequested atomic.Bool

type jukaHubUpdateTarget struct {
	Device DeviceModel
	OS     string
	Arch   string
	Asset  string
}

// jukaHubUpdateTargetFor selects the release archive for a running platform.
//
// There are exactly two published builds: JukaHub-Windows.zip for Windows x64
// and JukaHub-Trimui.zip for arm64 Linux. The arm64 build does not depend on the
// detected model, so an unrecognised handheld (a new TrimUI revision, a custom
// firmware that hides the model file, or a Linux arm64 port of JukaHub) gets the
// same archive instead of a failed update. That was the bug behind "no JukaHub
// update target for device \"unknown\" on linux/arm64": detection is a
// convenience, not a prerequisite for updating.
func jukaHubUpdateTargetFor(device DeviceModel, goos, arch string) (jukaHubUpdateTarget, error) {
	if device == DeviceDevBuild && goos == "windows" && arch == "amd64" {
		// The Windows build is shared by every desktop install.
		return jukaHubUpdateTarget{Device: device, OS: goos, Arch: arch, Asset: "JukaHub-Windows.zip"}, nil
	}
	if goos == "linux" && arch == "arm64" {
		return jukaHubUpdateTarget{Device: device, OS: goos, Arch: arch, Asset: "JukaHub-Trimui.zip"}, nil
	}
	return jukaHubUpdateTarget{}, fmt.Errorf("no JukaHub update target for device %q on %s/%s", device, goos, arch)
}

// jukaHubAssetName returns the release asset filename for a supported device
// target, or an error when the device/platform has no published build.
func jukaHubAssetName(device DeviceModel, goos, arch string) (string, error) {
	target, err := jukaHubUpdateTargetFor(device, goos, arch)
	if err != nil {
		return "", err
	}
	return target.Asset, nil
}

// OpUpdateJukaHub checks the release feed and stages the newest archive for
// this device. The user confirms the swap through the modal; this operation
// stops at staging.
func OpUpdateJukaHub(ctx context.Context, config *Config) error {
	rel, err := FetchLatestJukaHubRelease(ctx)
	if err != nil {
		return err
	}
	if !releaseIsNewerThanInstalled(rel) {
		installed := installedBuildDate()
		if installed == "" {
			installed = versionString(config)
		}
		return fmt.Errorf("already up to date (%s)", installed)
	}
	if err := StageJukaHubUpdate(ctx, config, rel); err != nil {
		return err
	}
	patchEventSafe(PatchEvent{Kind: PatchEventModal, Modal: &PatchModalRequest{
		ID:       "confirm-swap",
		Title:    "Update JukaHub to " + rel.TagName + "?",
		Body:     "The new build is staged. Applying replaces the JukaHub files and restarts the app.\n\nBackup and rollback are automatic.",
		Confirm:  "Apply & Restart",
		HighRisk: true,
	}})
	return nil
}

// findStagedBinary locates the JukaHub executable inside a staged tree.
func findStagedBinary(staging string) string {
	var found string
	_ = filepath.WalkDir(staging, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if runtime.GOOS == "windows" {
			if name == "jukahub.exe" {
				found = p
			}
			return nil
		}
		if name == "jukahub" {
			found = p
		}
		return nil
	})
	return found
}

// swapScriptPath returns where the swap helper should be written.
func swapScriptPath(staging string) string {
	ext := ".sh"
	if runtime.GOOS == "windows" {
		ext = ".bat"
	}
	return filepath.Join(staging, "apply-update"+ext)
}

// OpApplyStagedSwap replaces the installation files with the staged release
// using the detached swap helper. The journal guarantees automatic rollback on
// interruption.
func OpApplyStagedSwap(ctx context.Context, config *Config) error {
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	patchStateMu.Lock()
	tag := patchState.LastUpdateVer
	patchStateMu.Unlock()
	if tag == "" {
		return errors.New("no staged update")
	}
	staging := filepath.Join(dir, "staging", "app-"+sanitizeFileName(tag))
	newBin := findStagedBinary(staging)
	if newBin == "" {
		return errors.New("staged files are missing — check for updates again")
	}
	installRoot := filepath.Dir(newBin)

	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return err
	}
	exeDir := filepath.Dir(exePath)

	files, err := stagedInstallFiles(installRoot, exeDir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("staged update contains no files")
	}

	patchRowStatus(ComponentApp, StatusBackingUp)
	journal := newJournal("juka-update", tag)
	for _, f := range files {
		if err := journal.recordReplace(f.Dest); err != nil {
			_ = journal.rollback()
			return err
		}
	}
	// Also back up the user config separately (never replaced by an update).
	if _, err := os.Stat(P().UserConfigPath()); err == nil {
		if _, _, err := CreateBackupSet("pre-juka-update", []string{P().UserConfigPath()}); err != nil {
			logPatch("backup warning: %v", err)
		}
	}

	patchRowStatus(ComponentApp, StatusApplying)
	script := swapScriptPath(staging)
	// Written before the helper starts, so it exists when the app exits and the
	// launcher looks at what to do next.
	restartMarker := prepareLauncherRestart(exeDir)
	if err := writeSwapScript(script, exePath, files, journal.path, os.Getppid()); err != nil {
		if restartMarker != "" {
			_ = os.Remove(restartMarker)
		}
		_ = journal.rollback()
		return err
	}

	// Run the helper detached: the running process exits as part of the swap.
	cmd := P().CommandContext(ctx, script)
	if err := cmd.Start(); err != nil {
		if restartMarker != "" {
			_ = os.Remove(restartMarker)
		}
		_ = journal.rollback()
		return fmt.Errorf("starting swap helper: %w", err)
	}
	patchRowStatus(ComponentApp, StatusRestartReq)
	// Let the render loop quit so the helper can replace the executable.
	appRestartRequested.Store(true)
	return nil
}

// OpCancelStagedSwap removes the staged update and its marker.
func OpCancelStagedSwap(ctx context.Context, config *Config) error {
	dir, err := PatchStateDir()
	if err != nil {
		return err
	}
	patchStateMu.Lock()
	tag := patchState.LastUpdateVer
	patchState.LastUpdateVer = ""
	patchStateMu.Unlock()
	_ = savePatchState()
	if tag != "" {
		_ = os.RemoveAll(filepath.Join(dir, "staging", "app-"+sanitizeFileName(tag)))
	}
	patchRowStatus(ComponentApp, StatusCurrent)
	return nil
}
