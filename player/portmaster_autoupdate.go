package main

// portmaster_autoupdate.go — JukaHub puts PortMaster's files on the card and
// keeps them current by itself.
//
// Juka (2026-11-20): "can we pretty much do everything including installing
// PortMaster? and auto updating it?" Ports run on PortMaster's folder of shared
// files (control.txt, its key mapper, the device probe), which PortMaster's own
// menu updates when it is opened — a menu JukaHub no longer needs for anything
// (ports_native_install.go). So:
//
//   - PortMaster joins the daily Emulator updates pass as one more updater
//     (emuautoupdate_registry.go), under the same setting (Install
//     automatically, the default; Notify me; Off) and the same rule about the
//     moment: never over a game or port, on the charger or once the player
//     stepped away unless long overdue. Check compares the version file in
//     Apps/PortMaster/PortMaster with the tag of PortMaster-GUI's latest
//     release, through the pass's GitHub client (one look per pass, the
//     hour-long hold after a 403). Install downloads trimui.portmaster.zip —
//     the package Install and Repair use — checks it holds launch.sh and
//     PortMaster/control.txt, and writes its files over Apps/PortMaster, each
//     beside its target and renamed over it, PortMaster's version file last,
//     so an update cut short still reads as out of date. Ports, runtimes,
//     themes and settings are not in the package and stay. InUse holds it while
//     a port, PortMaster, a port install or a manual PortMaster install runs.
//     A package no newer than what is on the card changes nothing.
//   - A card with ports but no PortMaster (its folder deleted, a card from
//     another firmware) gets PortMaster from the same pass, on the charger or
//     once the player stepped away, as hakuX's first install does.
//   - Opening the Ports panel or the Ports Store online with no PortMaster on
//     the card installs it at once (pmAutoInstallOnOpen), once a session.
//
// After an install from the Ports screens only what ports need is prefetched
// (a full bash where the firmware lacks one, portmaster_runtime.go); after the
// pass, a port's own start fetches it. Python waits until PortMaster's own
// menu is opened. Only the TrimUI app in Apps/PortMaster is written: a
// PortMaster a custom firmware keeps elsewhere goes on updating itself.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// pmAutoKey is the updater's key in the pass and its record.
const pmAutoKey = "portmaster"

func init() {
	registerEmuRuntimeUpdater(emuRuntimeUpdater{
		Key:       pmAutoKey,
		Title:     portMasterAppName,
		Supported: pmAutoSupported,
		Installed: pmAutoInstalled,
		InUse:     pmAutoInUse,
		Check:     pmAutoCheck,
		Install:   pmAutoInstall,
	})
}

// pmAutoSupported: the TrimUI handhelds, where JukaHub installs PortMaster.
func pmAutoSupported() bool { return runtime.GOOS == "linux" && IsTrimuiDevice() }

// pmAutoTarget is the TrimUI app folder the updater writes.
func pmAutoTarget() string { return filepath.Join(portMasterSDRoot, "Apps", "PortMaster") }

// pmAutoManaged reports a PortMaster that is the TrimUI app: launch.sh in
// Apps/PortMaster and control.txt in its PortMaster folder.
func pmAutoManaged() bool {
	t := pmAutoTarget()
	return regularFile(filepath.Join(t, "launch.sh")) && regularFile(filepath.Join(t, "PortMaster", "control.txt"))
}

// pmAutoLocalVersion is the first line of the TrimUI app's version file, or "".
func pmAutoLocalVersion() string {
	return pmVersionIn(filepath.Join(pmAutoTarget(), "PortMaster"))
}

// pmVersionIn is the first line of dir/version, or "".
func pmVersionIn(dir string) string {
	raw, ok := portReadSmall(filepath.Join(dir, "version"))
	if !ok {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	return strings.TrimSpace(line)
}

// pmAutoWanted reports a card with installed ports and no PortMaster at all
// (nor a half one in Apps/PortMaster, which Repair mends on a press).
func pmAutoWanted() bool {
	if portMasterLauncher(portMasterSDRoot) != "" || portRealControlFolder() != "" {
		return false
	}
	if _, err := os.Lstat(pmAutoTarget()); err == nil {
		return false
	}
	dirs, _ := pnCountRoots(portRoots(portMasterSDRoot))
	for _, n := range dirs {
		if n > 0 {
			return true
		}
	}
	return false
}

// pmAutoInstalled is the updater's Installed: the TrimUI app to update, or a
// card that needs its first install — which joins a pass only on the charger
// or once the player stepped away, so a 24 MB download does not start in the
// first minutes after start-up while the handheld is in use.
func pmAutoInstalled() bool {
	if pmAutoManaged() {
		return true
	}
	return (getBatteryCharging() || userInputAge(time.Now()) >= coreAutoUpdateIdle) && pmAutoWanted()
}

// pmAutoInUse holds the update while PortMaster's files may be read: a game,
// port or PortMaster on screen, an install of a port, or PortMaster's own
// install or repair from a press.
func pmAutoInUse() bool {
	if gameScreenSuspended() || portMasterInstalling() {
		return true
	}
	pstoreQueue.mu.Lock()
	busy := pstoreQueue.running
	pstoreQueue.mu.Unlock()
	return busy
}

// pmAutoCheck is the pass's question: is PortMaster-GUI's latest release newer
// than the card's (or is PortMaster missing from a card with ports)? The
// answer also feeds the Ports panel's PortMaster row.
func pmAutoCheck(ctx context.Context, client *http.Client) (bool, string, error) {
	tag, err := portMasterLatestTagWith(ctx, client)
	if err != nil {
		return false, "", err
	}
	if tag == "" {
		return false, "", fmt.Errorf("PortMaster's latest release has no tag")
	}
	if !pmAutoManaged() {
		// A first install: only in Install automatically (Notify me would
		// announce a missing PortMaster as a new version). A build the pass
		// installed before the folder went is not a rollback to respect.
		if emuCoreUpdateModeNow() != coreUpdateInstall || !pmAutoWanted() {
			return false, "", nil
		}
		emuRuntimeUpdateRecord(pmAutoKey, func(r *emuRuntimeRecord) { r.Installed = "" })
		return true, tag, nil
	}
	local := pmAutoLocalVersion()
	portMasterVersion.mu.Lock()
	portMasterVersion.local, portMasterVersion.latest, portMasterVersion.at = local, tag, time.Now()
	portMasterVersion.mu.Unlock()
	if local == "" {
		// No version to compare: Update PortMaster stays a press.
		return false, "", nil
	}
	return portVersionNewer(tag, local), tag, nil
}

// pmAutoInstall is the pass's install: PortMaster's first install on a card
// that needs it, else the newest package written over Apps/PortMaster.
func pmAutoInstall(ctx context.Context, client *http.Client, progress func(string)) error {
	portMasterInstallState.mu.Lock()
	if portMasterInstallState.running {
		portMasterInstallState.mu.Unlock()
		return errRelPackBusy // an install or repair from a press is running
	}
	portMasterInstallState.running = true
	portMasterInstallState.mu.Unlock()
	defer setPortMasterInstalling(false)
	if gameScreenSuspended() {
		return errRelPackBusy // a game started since the check: next quiet moment
	}
	first := !pmAutoManaged()
	if first && !pmAutoWanted() {
		return errEmuUpdaterCurrent
	}
	card := emuRuntimeCardFrom(ctx)
	if progress != nil {
		progress("Downloading")
	}
	inner, stage, err := portMasterDownloadPackageCtx(ctx, portMasterSDRoot, portMasterInstallURL,
		func(done, total int64) { card.bytes(installPhaseDownload, done, total) },
		func(phase string) { card.restart(phase) })
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := ctx.Err(); err != nil {
		return err
	}
	if progress != nil {
		progress("Installing")
	}
	if first {
		if err := installPortMasterTree(inner, portMasterSDRoot); err != nil {
			return err
		}
		emuRuntimeNoteFirstInstall(pmAutoKey) // "installed", not "updated", in the pass's toast
		logInfo("portmaster", "installed into %s by the Emulator updates pass", pmAutoTarget())
	} else {
		local, fresh := pmAutoLocalVersion(), pmVersionIn(filepath.Join(inner, "PortMaster"))
		if local != "" && fresh != "" && !portVersionNewer(fresh, local) {
			// The release's tag reads newer than the version file it carries
			// (they are written differently): the tag is recorded as this
			// card's, so the pass does not fetch the same package every day.
			portMasterVersion.mu.Lock()
			tag := portMasterVersion.latest
			portMasterVersion.mu.Unlock()
			if tag != "" {
				emuRuntimeUpdateRecord(pmAutoKey, func(r *emuRuntimeRecord) { r.Installed = tag })
			}
			return errEmuUpdaterCurrent
		}
		if err := portMasterOverlay(inner, pmAutoTarget()); err != nil {
			return fmt.Errorf("write PortMaster's files: %w", err)
		}
		_ = os.Chmod(filepath.Join(pmAutoTarget(), "launch.sh"), 0o755)
		portMasterEnsureDataDirs(portMasterSDRoot)
		logInfo("portmaster", "updated %s from %s to %s", pmAutoTarget(), setOr(local, "an unknown version"), setOr(fresh, "the latest release"))
	}
	if err := syncDisks(); err != nil {
		logWarn("portmaster", "%v", err)
	}
	// No runtime prefetch here: it would wait for, then take, the pass's
	// install card. A port's own start fetches a missing bash.
	tbxPost(func() {
		invalidatePortsCache()
		invalidateEmulatorCache()
		portMasterVersionForget()
		pmRuntimeForget()
		if first {
			noteAwayToolInstalled("PortMaster")
			trophyEvent(tkPortMaster)
		}
	})
	return nil
}

// pmAutoInstallTried keeps pmAutoInstallOnOpen to one try a session. Main
// thread only.
var pmAutoInstallTried bool

// pmAutoInstallOnOpen installs PortMaster's files when the Ports panel or the
// Ports Store opens online on a card without PortMaster: ports need them, and
// nothing else on those screens works without them. Once a session; the
// Install row stays for a retry. Main thread.
func pmAutoInstallOnOpen() {
	if pmAutoInstallTried || runtime.GOOS != "linux" || !IsTrimuiDevice() || !IsOnline() {
		return
	}
	// A PortMaster a firmware keeps elsewhere counts too: a second copy in
	// Apps/PortMaster would take over from it and its runtimes.
	if portMasterLauncher(portMasterSDRoot) != "" || portRealControlFolder() != "" || portMasterInstalling() || installBarActive() {
		return
	}
	if _, err := os.Lstat(pmAutoTarget()); err == nil {
		return // a half PortMaster: Repair on its row mends it
	}
	pmAutoInstallTried = true
	if err := startPortMasterInstall(); err != nil {
		logInfo("portmaster", "automatic install: %v", err)
		return
	}
	ShowToast("Setting up PortMaster's files for ports (about 24 MB) — you will not need to open PortMaster", ToastKindInfo)
}
