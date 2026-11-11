package main

// emuautoupdate_desktop.go — Emulator updates on a PC.
//
// On Windows the Emulators page installs a portable RetroArch and the
// standalone emulators from their official releases into managed folders
// (emuwininstall.go). Each folder carries a manifest, and an update lays the
// new release over the folder and keeps every file it replaces for Roll back.
// They used to update only on a press; this gives a PC the handheld's daily
// pass, under the same Emulator updates setting:
//
//   - every emulator JukaHub installed and manages — whatever emuWinSpecs can
//     install and has a manifest on disk, so one added to that list (the Xbox
//     360 emulator) is covered with no change here — is an updater of the pass
//     (emuWinRuntimeUpdaters). It is skipped while it runs, whether JukaHub
//     started it or anything else did (emuProcessUnder lists the processes);
//   - the managed RetroArch's cores come from the buildbot's Windows tree: the
//     core pass's work, restricted to that RetroArch's own cores folder, and
//     not while that RetroArch runs. A RetroArch someone else installed
//     (Steam's included) keeps its own cores and its own updater;
//   - RetroArch itself only moves from stable release to stable release: when
//     the buildbot's stable folder cannot be read the installer falls back to
//     today's nightly, a quarter of a gigabyte that would otherwise be fetched
//     again every day. A press on its tile still installs that nightly;
//   - the moment: never while a game or emulator JukaHub started is on screen,
//     another install runs, a video plays or another program fills the screen
//     (a game, a film, a presentation: Windows' notification state), and not
//     while a laptop runs on its battery; otherwise once the PC has been left
//     alone for a few minutes — no key or mouse in any window (Windows' last
//     input), no controller in JukaHub. A pass three days overdue stops
//     waiting for the charger, never for the idle minutes. The handheld's
//     first-pass rule (overdue once it has waited a poll) is not used here: a
//     PC keeps its time and is normally on mains, so only a real three-day gap
//     — or three days of waiting — counts.
//
// Runtimes registered by other files run here too wherever they say they
// apply, which is all a Linux desktop gets.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// emuDesktopIdle is how long JukaHub must have been left alone before a pass
// starts on a PC.
const emuDesktopIdle = 3 * time.Minute

// emuDesktopNotice is the one toast that tells a PC its emulators now update
// by themselves. It is shown once, at the first look of the scheduler that is
// not over a game and finds something JukaHub installed.
const emuDesktopNotice = "JukaHub now keeps the emulators it installed up to date by itself, keeping the old version for a roll back · " +
	"change it in Settings → Games & emulators → Emulator updates"

// emuDesktopStarted keeps the scheduler to one goroutine.
var emuDesktopStarted atomic.Bool

// startDesktopEmuAutoUpdate starts the PC's scheduler; announce asks for the
// one-time notice (the mode is the unchosen default). It runs on the main
// thread from startCoreAutoUpdate.
func startDesktopEmuAutoUpdate(announce bool) {
	if coreAutoUpdateDisabled() || !emuDesktopStarted.CompareAndSwap(false, true) {
		return
	}
	if announce && !emuDesktopNoticeShown() {
		coreUpdateMigrationPending.Store(true)
	}
	go func() {
		time.Sleep(coreAutoUpdateStartupDelay)
		for {
			_, waiting := emuDesktopTick(time.Now())
			if waiting {
				time.Sleep(coreAutoUpdateRetry)
			} else {
				time.Sleep(coreAutoUpdatePoll)
			}
		}
	}()
}

// emuDesktopNoticeShown reports that the one-time notice was shown before.
func emuDesktopNoticeShown() bool {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	emuRuntimeLoadLocked()
	return emuRuntimeState.file.DesktopNotice
}

// emuDesktopNoticeDone records that the notice was shown.
func emuDesktopNoticeDone() {
	emuRuntimeState.mu.Lock()
	defer emuRuntimeState.mu.Unlock()
	emuRuntimeLoadLocked()
	emuRuntimeState.file.DesktopNotice = true
	emuRuntimeSaveLocked()
}

// emuDesktopTick is coreAutoUpdateTick for a PC: the pass once, if it is due
// and the moment is right. ran and waiting mean what they mean there.
func emuDesktopTick(now time.Time) (ran, waiting bool) {
	if coreUpdateMigrationPending.Load() && !gameScreenSuspended() && emuDesktopHasManaged() &&
		coreUpdateMigrationPending.Swap(false) {
		ShowToast(emuDesktopNotice, ToastKindInfo)
		emuDesktopNoticeDone()
	}
	mode := emuCoreUpdateModeNow()
	if mode == coreUpdateOff {
		coreAutoUpdateStopWaiting()
		return false, false
	}
	if coreAutoUpdateDisabled() {
		return false, false
	}
	if gameScreenSuspended() {
		// Nothing runs over a game or an emulator JukaHub started, and
		// nothing is looked at during one (a look lists the processes): the
		// first look after it decides.
		return false, true
	}
	coresDir := emuDesktopCoresDir()
	last := coreAutoUpdateLastCheck()
	coresDue := coresDir != "" && coreAutoUpdateDue(last, now) && !coreAutoUpdateRanRecently()
	if coresDue && emuProcessUnder(filepath.Dir(coresDir)) {
		// RetroArch is running: a core it has loaded is not replaced under it.
		coresDue = false
	}
	extras := emuRuntimesForPass(now, coresDue)
	if !coresDue && len(extras) == 0 {
		coreAutoUpdateStopWaiting()
		return false, false
	}
	waited := coreAutoUpdateWaited()
	if !IsOnline() {
		return false, true
	}
	busy := installBarActive() || videoHoldsScreenAwake() || emuDesktopFullScreenApp()
	overdue := coresDue && emuDesktopOverdueFor(last, now, waited)
	for _, u := range extras {
		overdue = overdue || emuDesktopOverdueFor(emuRuntimeRecordFor(u.Key).CheckedAt, now, waited)
	}
	if !emuDesktopMoment(gameScreenSuspended(), busy, emuDesktopOnBattery(), emuDesktopIdleFor(now), overdue) {
		return false, true
	}
	coreAutoUpdateStopWaiting()
	var managed map[string]coreBuild
	if coresDue {
		managed = emuDesktopManagedCores(coresDir)
		if len(managed) == 0 {
			// The managed RetroArch has no cores yet: looked at tomorrow.
			coreAutoUpdateStamp(now)
			coresDue = false
		}
	}
	if mode == coreUpdateNotify {
		var active func() map[string]coreBuild
		if coresDue {
			active = func() map[string]coreBuild { return managed }
		}
		return emuAutoNotifyPass(now, active, extras)
	}
	var cores emuAutoCoreRunner
	if coresDue {
		cores = emuDesktopCoreRunner(coresDir)
	}
	return emuAutoInstallPass(now, cores, extras)
}

// emuDesktopMoment is a PC's "is now a good time": never over a game or an
// emulator JukaHub started, another install, a playing video or another
// program's full screen (busy); only once the PC has been left alone for
// emuDesktopIdle; and not on a laptop's battery unless long overdue.
func emuDesktopMoment(inGame, busy, onBattery bool, idle time.Duration, overdue bool) bool {
	if inGame || busy {
		return false
	}
	return idle >= emuDesktopIdle && (!onBattery || overdue)
}

// emuDesktopOverdueFor is coreAutoUpdateOverdueFor for a PC: a real stamp three
// days old, or — no stamp yet (the first pass on this PC, an updater never
// checked) or one the clock cannot place — three days of waiting this session.
// The handheld's "a first pass that waited a poll is overdue" is for a device
// that is always on battery; a PC's first pass waits for mains like any other.
func emuDesktopOverdueFor(last string, now time.Time, waited time.Duration) bool {
	t, ok := parseReleaseTimestamp(last)
	if !ok || now.Before(t) || t.Before(minSaneClock) || now.Before(minSaneClock) {
		return waited >= coreAutoUpdateOverdue
	}
	return now.Sub(t) >= coreAutoUpdateOverdue
}

// emuDesktopIdleFor is how long the PC has been left alone: the shorter of
// JukaHub's own input age, which hears its controllers, and Windows' last
// keyboard or mouse input in any window (emuDesktopSystemIdle; unknown off
// Windows), so a user busy in another program is not "idle".
func emuDesktopIdleFor(now time.Time) time.Duration {
	idle := userInputAge(now)
	if sys := emuDesktopSystemIdle(); sys >= 0 && sys < idle {
		idle = sys
	}
	return idle
}

// emuDesktopHasManaged reports that JukaHub installed something on this PC
// that the pass looks after.
func emuDesktopHasManaged() bool {
	return emuDesktopCoresDir() != "" || len(emuWinManagedSpecs()) > 0
}

// emuDesktopCoresCache keeps emuDesktopCoresDir off the render path: the Core
// builds page asks on every frame it draws, and the answer is a manifest read
// and a stat.
var emuDesktopCoresCache struct {
	mu  sync.Mutex
	at  time.Time
	dir string
}

// emuDesktopCoresDirCached is emuDesktopCoresDir, at most ten seconds old.
func emuDesktopCoresDirCached() string {
	emuDesktopCoresCache.mu.Lock()
	defer emuDesktopCoresCache.mu.Unlock()
	if emuDesktopCoresCache.at.IsZero() || time.Since(emuDesktopCoresCache.at) > 10*time.Second {
		emuDesktopCoresCache.dir = emuDesktopCoresDir()
		emuDesktopCoresCache.at = time.Now()
	}
	return emuDesktopCoresCache.dir
}

// emuDesktopCoresDir is the cores folder of the RetroArch JukaHub installed on
// this PC — beside its retroarch.exe, where a portable RetroArch loads cores
// from — or "" when there is none.
func emuDesktopCoresDir() string {
	if !IsWindows() {
		return ""
	}
	dir, m, ok := emuWinManaged("RetroArch")
	exe := filepath.FromSlash(strings.TrimSpace(m.Exe))
	if !ok || exe == "" || !filepath.IsLocal(exe) {
		return ""
	}
	cores := filepath.Join(dir, filepath.Dir(exe), "cores")
	if info, err := os.Stat(cores); err != nil || !info.IsDir() {
		return ""
	}
	return cores
}

// emuDesktopManagedCores is the inventory's active cores that live in
// coresDir: the managed RetroArch's, and no other RetroArch's.
func emuDesktopManagedCores(coresDir string) map[string]coreBuild {
	out := map[string]coreBuild{}
	for core, build := range scanCoreInventory().Active {
		if emuPathWithin(build.Path, coresDir) {
			out[core] = build
		}
	}
	return out
}

// emuDesktopCoreRunner brings the managed RetroArch's cores forward:
// runInstalledCoreUpdates' steps over that RetroArch's cores alone, with the
// same rules — a build refused before is not fetched again, the pass stops
// while the disk still has room, a stop ends it between cores — and the same
// report. (runInstalledCoreUpdates updates every RetroArch the inventory finds,
// which a press may do and an unattended pass must not.)
func emuDesktopCoreRunner(coresDir string) emuAutoCoreRunner {
	return func(ctx context.Context, client *http.Client, progress func(phase, detail string)) emuInstallReport {
		var report emuInstallReport
		notify := func(phase, detail string) {
			if progress != nil {
				progress(phase, detail)
			}
		}
		notify(installPhaseResolving, "")
		index, err := fetchCoreIndex(ctx, client)
		if err != nil {
			report.Errors = append(report.Errors, "core index: "+err.Error())
			return report
		}
		report.Checked = true
		active := emuDesktopManagedCores(coresDir)
		planned := planCoreUpdates(index, active)
		report.Current = len(active) - len(planned)
		if report.Current < 0 {
			report.Current = 0
		}
		updates := make([]coreUpdate, 0, len(planned))
		for _, upd := range planned {
			if reason, refused := coreBuildRefused(upd.Core, upd.CRC32); refused {
				logDebug("emulators", "core %s %s not fetched: refused before (%s)", upd.Core, upd.Date, reason)
				report.KnownRefused++
				continue
			}
			updates = append(updates, upd)
		}
		pool := compatCorePoolDir()
		for i, upd := range updates {
			if ctx.Err() != nil {
				report.Interrupted = true
				break
			}
			if free, err := freeBytes(filepath.Dir(upd.Active)); err == nil && free < coreUpdateFreeFloor(upd.Active) {
				report.LowSpace = bootFormatBytes(free)
				logWarn("emulators", "core updates stopped before %s: only %s free", upd.Core, report.LowSpace)
				break
			}
			notify(installPhaseDownload, fmt.Sprintf("%s (%d/%d)", coreConfigDisplayName(upd.Core), i+1, len(updates)))
			build, err := fetchCoreUpdate(ctx, client, upd, pool)
			if err == nil {
				var target string
				target, _, err = activateCoreBuild(build)
				emuCRCForget(target)
			}
			if err != nil {
				var refused *coreRefusedError
				switch {
				case ctx.Err() != nil:
					report.Interrupted = true
				case errors.As(err, &refused):
					report.Refused = append(report.Refused, coreConfigDisplayName(upd.Core))
					if report.RefusedNote == "" {
						report.RefusedNote = err.Error()
					}
				default:
					report.Errors = append(report.Errors, coreConfigDisplayName(upd.Core)+": "+err.Error())
				}
				if report.Interrupted {
					break
				}
				continue
			}
			report.Updated = append(report.Updated, coreConfigDisplayName(upd.Core))
		}
		return report
	}
}

// ── The Windows emulators as updaters ───────────────────────────────────────

// emuWinRuntimeUpdaters is one updater per Windows emulator JukaHub can
// install (emuWinSpecs), keyed "win:<Name>", built fresh for each pass. They
// apply on Windows only, and Installed means a manifest in a managed folder:
// an emulator installed by hand is launched, never replaced.
func emuWinRuntimeUpdaters() []emuRuntimeUpdater {
	if !IsWindows() {
		return nil
	}
	var out []emuRuntimeUpdater
	for _, spec := range emuWinSpecs {
		if !spec.installable() {
			continue
		}
		out = append(out, emuRuntimeUpdater{
			Key:       "win:" + spec.Name,
			Title:     spec.Name,
			Supported: IsWindows,
			Installed: func() bool {
				_, _, ok := emuWinManaged(spec.Name)
				return ok
			},
			InUse: func() bool { return emuWinRunning(spec.Name) },
			Check: func(ctx context.Context, client *http.Client) (bool, string, error) {
				return emuWinAutoCheck(ctx, client, spec)
			},
			Install: func(ctx context.Context, client *http.Client, progress func(string)) error {
				return emuWinAutoInstall(ctx, client, spec, progress)
			},
		})
	}
	return out
}

// emuWinRunning reports that a managed emulator's files are in use: JukaHub
// started it (or a game in it), or any process runs a program from its
// folder.
func emuWinRunning(name string) bool {
	dir, _, ok := emuWinManaged(name)
	if !ok {
		return false
	}
	return emuScreenChildUnder(dir) || emuProcessUnder(dir)
}

// emuWinAutoCheck asks whether a managed emulator has a newer release. A fresh
// "no" from the Emulators page's check is taken as it is; anything else is
// asked of its project, so the label names the release the install will fetch
// (the pass's client answers the install's look from this one). The answer
// also badges the tile.
func emuWinAutoCheck(ctx context.Context, client *http.Client, spec emuWinSpec) (bool, string, error) {
	_, manifest, ok := emuWinManaged(spec.Name)
	if !ok {
		return false, "", nil
	}
	if _, newer, fresh := emuWinCachedAnswer(spec.Name); fresh && !newer {
		return false, "", nil
	}
	rel, err := emuWinResolve(ctx, client, spec)
	if err != nil {
		return false, "", err
	}
	if !emuWinUpdateAvailable(manifest, rel) {
		return false, "", nil
	}
	if spec.Buildbot && emuRetroArchNightly(rel.Tag) {
		logPatch("emulator updates: no stable RetroArch for Windows could be read; today's nightly is left for a press on its tile")
		return false, "", nil
	}
	emuNoteWinUpdate(spec.Name, rel)
	return true, emuReleaseLabel(manifest.Tag, rel), nil
}

// emuRetroArchNightly reports the tag emuResolveRetroArch gives a nightly.
func emuRetroArchNightly(tag string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), "nightly-")
}

// emuWinAutoInstall is the pass's install of one Windows emulator: the
// Emulators page's own (emuWinInstall), which lays the release over the folder
// and keeps every file it replaces for Roll back.
func emuWinAutoInstall(ctx context.Context, client *http.Client, spec emuWinSpec, progress func(string)) error {
	emuSetInstalling(spec.Name)
	defer emuSetInstalling("")
	result, err := emuWinInstall(ctx, client, spec, emuAutoWinProgress(progress))
	if err != nil {
		return err
	}
	invalidateEmulatorCache()
	emuForgetUpdate(spec.Name)
	if result.Exe != "" {
		requestAppIconExtraction([]string{result.Exe})
	}
	if result.Current {
		return errEmuUpdaterCurrent
	}
	if !result.Kept {
		logWarn("emulators", "%s %s: the previous version could not be kept for a roll back", spec.Name, result.Tag)
	}
	return nil
}
