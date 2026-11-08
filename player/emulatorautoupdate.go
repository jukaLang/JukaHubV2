package main

// emulatorautoupdate.go — keeping the emulator cores current without a press.
//
// The Emulators page can bring every installed core forward in one action, but
// it needs the user to open that page and press it. On the handheld the card is
// the whole console: a core that is three nightlies behind is a game that runs
// worse for no reason the user can see, and the fix was a trip to a page they
// did not know about.
//
// So the update runs by itself, on by default (the "emuAutoUpdate" setting, on
// the Core builds page), at most once per coreAutoUpdateInterval. A scheduler
// wakes every coreAutoUpdatePoll after the interface is up and runs the pass
// when it is due and the moment is right:
//
//   - online, and no other patch operation or install owns the device;
//   - never while a game or emulator is on screen — and a game that starts
//     mid-pass stops it between cores;
//   - while charging, or after a couple of idle minutes, so a download never
//     competes with someone navigating; a pass more than three days overdue
//     runs at the next quiet moment regardless.
//
// It is the same work the page's action does — download, verify the CRC32 the
// index states, activate, keep the replaced build as a .bak up to
// maxCoreBackups — reported through the same install bar, and it ends with a
// toast ("Updated 3 emulator cores") that the notification centre keeps.
//
// What it will not do:
//   - run on a desktop, where the Emulators page updates on a press;
//   - stand down for a whole interval when the check failed. Only a pass that
//     actually read the index is stamped, so a device that boots before its
//     Wi-Fi associates tries again at the next poll.
//
// The index is an unsigned nightly listing. An update here is still checked
// against the CRC32 the index promises and the build it replaces is kept, so a
// nightly that turns out worse is one press away from being rolled back on the
// device (Emulators R1, or the Core builds page).

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// coreAutoUpdateInterval is how often the automatic pass may run. The
	// buildbot publishes nightly, so a daily cadence is as fresh as the channel
	// gets without re-downloading the same build on every launch.
	coreAutoUpdateInterval = 24 * time.Hour
	// coreAutoUpdateStartupDelay keeps the pass out of the boot path and behind
	// the release check's own window: both want the one patch slot, and a core
	// download can hold it for minutes.
	coreAutoUpdateStartupDelay = autoUpdateStartupDelay + 30*time.Second
	// coreAutoUpdatePoll is how often the scheduler looks for a good moment.
	coreAutoUpdatePoll = 20 * time.Minute
	// coreAutoUpdateIdle is how long the user must have been away before a
	// pass on battery may start.
	coreAutoUpdateIdle = 2 * time.Minute
	// coreAutoUpdateOverdue lets a pass run at the next quiet moment even on
	// battery and in use, so a device that is never idle still updates.
	coreAutoUpdateOverdue = 72 * time.Hour
	// emuAutoUpdateSetting is the custom variable the Core builds page toggles.
	emuAutoUpdateSetting = "emuAutoUpdate"
)

// coreAutoUpdateDue reports whether the background core pass may run, given the
// stamp of the last check that read the index. It follows autoUpdateCheckDue's
// rule, which matters on this hardware: the handheld has no RTC battery, so a
// missing, garbled, future or pre-2020 stamp must never suppress the update.
func coreAutoUpdateDue(last string, now time.Time) bool {
	t, ok := parseReleaseTimestamp(last)
	if !ok {
		return true
	}
	if now.Before(t) || t.Before(minSaneClock) || now.Before(minSaneClock) {
		return true
	}
	return now.Sub(t) >= coreAutoUpdateInterval
}

// coreAutoUpdateOverdueFor reports whether the last successful pass is so old
// that the pass should not wait for the device to be charging or idle.
func coreAutoUpdateOverdueFor(last string, now time.Time) bool {
	t, ok := parseReleaseTimestamp(last)
	if !ok || now.Before(t) || t.Before(minSaneClock) || now.Before(minSaneClock) {
		// No trustworthy stamp: the first pass on a fresh card still waits for
		// a quiet moment, but only one poll's worth.
		return false
	}
	return now.Sub(t) >= coreAutoUpdateOverdue
}

// coreAutoUpdateAllowed is the policy for this device, with the opt-outs kept
// separate so an operator (or a test) can stand the pass down without touching
// the device checks.
func coreAutoUpdateAllowed(disabled, supported, handheld, online bool) bool {
	if disabled || !supported || !handheld || !online {
		return false
	}
	return true
}

// coreAutoUpdateMoment is the "is now a good time" half of the decision: never
// over a game, never over another install, and on battery only once the user
// has stepped away — unless the pass is long overdue.
func coreAutoUpdateMoment(inGame, busy, charging bool, idle time.Duration, overdue bool) bool {
	if inGame || busy {
		return false
	}
	return charging || idle >= coreAutoUpdateIdle || overdue
}

// emuAutoUpdateOn mirrors the setting for the scheduler goroutine. The Custom
// map belongs to the main thread, so the goroutine reads this instead.
var emuAutoUpdateOn atomic.Bool

func init() { emuAutoUpdateOn.Store(true) }

// emuAutoUpdateEnabled reads the setting from the config (main thread only):
// on unless it was switched off.
func emuAutoUpdateEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	if v, ok := customValue(config.Variables.Custom, emuAutoUpdateSetting); ok {
		if s, isString := v.(string); isString {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "false", "0", "off", "no":
				return false
			}
			return true
		}
		return coerceBool(v)
	}
	return true
}

// emuSetAutoUpdate switches the automatic pass on or off and saves the choice
// (main thread only).
func emuSetAutoUpdate(config *Config, on bool) {
	emuAutoUpdateOn.Store(on)
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[emuAutoUpdateSetting] = on
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// coreAutoUpdateDisabled reports the environment opt-out. It mirrors
// JUKAHUB_NO_AUTOUPDATE, which stands the release check down.
func coreAutoUpdateDisabled() bool {
	return os.Getenv("JUKAHUB_NO_CORE_AUTOUPDATE") != ""
}

// coreAutoUpdateLastCheck is the stamp of the last pass that read the index.
func coreAutoUpdateLastCheck() string {
	patchStateMu.Lock()
	defer patchStateMu.Unlock()
	return patchState.CoreUpdateCheckedAt
}

// emuAutoUpdateSummary is the notification a pass that changed something
// leaves: how many cores, and which, kept short enough for a toast.
func emuAutoUpdateSummary(report emuInstallReport) string {
	n := len(report.Updated)
	if n == 0 {
		return report.summary()
	}
	noun := "emulator cores"
	if n == 1 {
		noun = "emulator core"
	}
	names := report.Updated
	more := ""
	if len(names) > 3 {
		more = fmt.Sprintf(" +%d more", len(names)-3)
		names = names[:3]
	}
	msg := fmt.Sprintf("Updated %d %s (%s%s)", n, noun, strings.Join(names, ", "), more)
	if len(report.Errors) > 0 {
		msg += fmt.Sprintf(" · %d failed", len(report.Errors))
	}
	return msg
}

// startCoreAutoUpdate starts the scheduler. It is best-effort: every reason it
// cannot run is a log line, never a startup error, and a device with nothing to
// update sees only the install bar settling. It runs from the deferred startup
// queue, on the main thread, which is where the setting may be read.
func startCoreAutoUpdate(config *Config) {
	emuAutoUpdateOn.Store(emuAutoUpdateEnabled(config))
	if !coreAutoUpdateAllowed(coreAutoUpdateDisabled(), emuCoreInstallSupported(), IsTrimuiDevice(), true) {
		return
	}
	go func() {
		time.Sleep(coreAutoUpdateStartupDelay)
		for {
			coreAutoUpdateTick(time.Now())
			time.Sleep(coreAutoUpdatePoll)
		}
	}()
}

// coreAutoUpdateTick runs the pass once if it is allowed, due and the moment is
// right. It reports whether a pass ran.
func coreAutoUpdateTick(now time.Time) bool {
	if !emuAutoUpdateOn.Load() {
		return false
	}
	if !coreAutoUpdateAllowed(coreAutoUpdateDisabled(), emuCoreInstallSupported(), IsTrimuiDevice(), IsOnline()) {
		return false
	}
	last := coreAutoUpdateLastCheck()
	if !coreAutoUpdateDue(last, now) {
		return false
	}
	if !coreAutoUpdateMoment(gameScreenSuspended(), installBarActive(), getBatteryCharging(),
		userInputAge(now), coreAutoUpdateOverdueFor(last, now)) {
		return false
	}
	if len(emuInstalledCoreSet()) == 0 {
		// Nothing to update; check again tomorrow rather than every poll.
		coreAutoUpdateStamp(now)
		return false
	}
	if !tryBeginPatchOp() {
		logPatch("core auto-update: skipped, another Patch operation is running")
		return false
	}
	defer endPatchOp()

	// A helper-tool download runs outside the Patch lock and may own the card
	// right now — on a first boot ffmpeg is often still arriving — and a begin
	// would retire its token and stop it. The cores wait for the next poll;
	// the check stamp is untouched, so they are still due then.
	if installBarActive() {
		logPatch("core auto-update: skipped, an install is running")
		return false
	}
	logPatch("core auto-update: checking installed cores")
	installBarBeginLabelled("Emulator cores", "Updating emulator cores")
	// The timeout derives from the card's run token (minted by the begin
	// above), so a cancel from the card or the strip stops the transfers.
	ctx, cancel := context.WithTimeout(installBarRunContext(), 20*time.Minute)
	defer cancel()
	// A game that starts while the pass runs gets the device back: the
	// watcher cancels the pass, which stops between cores.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if gameScreenSuspended() {
					logPatch("core auto-update: a game started, stopping")
					cancel()
					return
				}
			}
		}
	}()

	report := runInstalledCoreUpdates(ctx, newEmulatorUpdateClient(), installBarProgressHook())
	installBarPhase(installPhaseDone)
	installBarDone()

	if !report.Checked {
		// The index never arrived (offline mid-poll, a redirect refused).
		// Leaving the stamp alone means the next poll tries again.
		for _, e := range report.Errors {
			logPatch("core auto-update: %s", e)
		}
		return true
	}
	coreAutoUpdateStamp(now)

	if len(report.Updated) == 0 && len(report.Errors) == 0 {
		logPatch("core auto-update: %d core(s) checked, all current", report.Current)
		return true
	}
	if len(report.Updated) > 0 {
		// The tile list, the compatibility panel and the footer's rollback
		// cache all read the cores folder.
		invalidateEmulatorCache()
		invalidateCompatCache()
		emuForgetAllUpdates()
		coreBuildsRequestReload()
		PlaySuccessSound()
	}
	kind := ToastKindSuccess
	if len(report.Errors) > 0 {
		kind = ToastKindWarning
	}
	summary := emuAutoUpdateSummary(report)
	ShowToast(summary, kind)
	logPatch("core auto-update: %s", summary)
	return true
}

// coreAutoUpdateStamp records a pass that read the index (or found nothing to
// update), so the next one waits a day.
func coreAutoUpdateStamp(now time.Time) {
	patchStateMu.Lock()
	patchState.CoreUpdateCheckedAt = now.UTC().Format(time.RFC3339)
	patchStateMu.Unlock()
	_ = savePatchState()
}
