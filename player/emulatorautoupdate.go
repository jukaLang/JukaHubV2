package main

// emulatorautoupdate.go — keeping the emulator cores current without a press.
//
// The Emulators page can bring every installed core forward in one action, but
// it needs the user to open that page and press it. On the handheld the card is
// the whole console: a core that is three nightlies behind is a game that runs
// worse for no reason the user can see, and the fix was a trip to a page they
// did not know about.
//
// So the update runs by itself, once per coreAutoUpdateInterval, after the
// interface is up. It is the same work the page's action does — download,
// verify the CRC32 the index states, activate, keep the replaced build as a
// .bak up to maxCoreBackups — reported through the same install bar, which is
// why the download bar appears over whatever screen is up while it runs.
//
// What it will not do:
//   - run on a desktop, where RetroArch manages its own cores and the buildbot
//     publishes Linux shared objects that nothing there loads;
//   - run offline, or while another patch operation owns the device;
//   - stand down for a whole interval when the check failed. Only a pass that
//     actually read the index is stamped, so a device that boots before its
//     Wi-Fi associates tries again on the next launch.
//
// The index is an unsigned nightly listing, which is why the Emulators page's
// activation used to be manual. An update here is still checked against the
// CRC32 the index promises and the build it replaces is kept, so a nightly that
// turns out worse is one press away from being rolled back on the device.

import (
	"context"
	"os"
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

// coreAutoUpdateAllowed is the policy for this device, with the environment
// switch kept separate so an operator (or a test) can stand the pass down
// without touching the device checks.
func coreAutoUpdateAllowed(disabled, supported, handheld, online bool) bool {
	if disabled || !supported || !handheld || !online {
		return false
	}
	return true
}

// coreAutoUpdateDisabled reports the environment opt-out. It mirrors
// JUKAHUB_NO_AUTOUPDATE, which stands the release check down.
func coreAutoUpdateDisabled() bool {
	return os.Getenv("JUKAHUB_NO_CORE_AUTOUPDATE") != ""
}

// startCoreAutoUpdate runs the automatic core update in the background. It is
// best-effort: every reason it cannot run is a log line, never a startup error,
// and a device with nothing to update sees only the install bar settling.
func startCoreAutoUpdate(config *Config) {
	if !coreAutoUpdateAllowed(coreAutoUpdateDisabled(), emuCoreInstallSupported(), IsTrimuiDevice(), true) {
		return
	}
	go func() {
		time.Sleep(coreAutoUpdateStartupDelay)
		if !coreAutoUpdateAllowed(coreAutoUpdateDisabled(), emuCoreInstallSupported(), IsTrimuiDevice(), IsOnline()) {
			logPatch("core auto-update: skipped (offline, or not the handheld)")
			return
		}
		patchStateMu.Lock()
		last := patchState.CoreUpdateCheckedAt
		patchStateMu.Unlock()
		if !coreAutoUpdateDue(last, time.Now()) {
			return
		}
		if !tryBeginPatchOp() {
			logPatch("core auto-update: skipped, another Patch operation is running")
			return
		}
		defer endPatchOp()

		logPatch("core auto-update: checking installed cores")
		installBarBeginLabelled("Emulator cores", "Updating emulator cores")
		// The timeout derives from the card's run token (minted by the begin
		// above), so a cancel from the card or the strip stops the transfers.
		ctx, cancel := context.WithTimeout(installBarRunContext(), 20*time.Minute)
		defer cancel()

		report := runInstalledCoreUpdates(ctx, newEmulatorUpdateClient(), installBarProgressHook())
		installBarPhase(installPhaseDone)
		installBarDone()

		if !report.Checked {
			// The index never arrived (offline mid-boot, a redirect refused).
			// Leaving the stamp alone means the next launch tries again.
			for _, e := range report.Errors {
				logPatch("core auto-update: %s", e)
			}
			return
		}
		patchStateMu.Lock()
		patchState.CoreUpdateCheckedAt = time.Now().UTC().Format(time.RFC3339)
		patchStateMu.Unlock()
		_ = savePatchState()

		if len(report.Updated) == 0 && len(report.Errors) == 0 {
			logPatch("core auto-update: %d core(s) checked, all current", report.Current)
			return
		}
		if len(report.Updated) > 0 {
			// The tile list, the compatibility panel and the footer's rollback
			// cache all read the cores folder.
			invalidateEmulatorCache()
			invalidateCompatCache()
			PlaySuccessSound()
		}
		kind := ToastKindSuccess
		if len(report.Errors) > 0 {
			kind = ToastKindWarning
		}
		ShowToast(report.summary(), kind)
		logPatch("core auto-update: %s", report.summary())
	}()
}
