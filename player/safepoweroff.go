package main

// safepoweroff.go — Safe power off: before the handheld restarts or powers
// off, programs that still have files open on the SD card are asked to close
// them, the card is synced and switched to read-only, so nothing is caught
// half-written.
//
// It runs at the very end of a Restart or Power off (devpower.go
// devPowerAfterShutdown), after JukaHub's own teardown has stopped the games,
// saved the settings, playtime and saves and synced the card, and after the
// Cleanup hooks; the order of those is unchanged. Then, at most
// safePowerOffBudget in all, on the handheld only (safepoweroff_linux.go):
//
//  1. Every process with an open file below a card mount (/proc/*/fd, at most
//     safePowerOffMaxPids processes and safePowerOffMaxFds links) gets
//     SIGTERM — never PID 1, a kernel thread, JukaHub itself or the scripts
//     that started it (its parent chain).
//  2. Up to safePowerOffWait for them to end.
//  3. sync.
//  4. Each card mount remounted read-only (deepest first), best effort: the
//     kernel refuses while a file is still open for writing, which is fine —
//     the sync already happened. JukaHub's own stdout and stderr (errors.txt
//     on the card) are pointed at /dev/null first so they are not that file.
//
// Any failure is logged and the power off goes on as before. If the restart
// or power off command itself then fails, the card is made writable again and
// stderr goes back to errors.txt (the undo devPowerAfterShutdown calls); the
// programs asked to close stay closed.
//
// Settings → Power & battery → Safe power off, on unless turned off; Linux
// TrimUI on its own firmware only (JukaHub OS's system has an init that
// unmounts the card). Nothing here was run on a device from this desk.

import (
	"sync/atomic"
	"time"
)

const (
	// safePowerOffKey is the Custom setting; unset is On.
	safePowerOffKey = "safePowerOff"
	// safePowerOffBudget bounds all of it.
	safePowerOffBudget = 3 * time.Second
	// safePowerOffWait bounds the wait for the programs asked to close.
	safePowerOffWait = 1500 * time.Millisecond
	// safePowerOffScanBudget bounds the /proc scan.
	safePowerOffScanBudget = 600 * time.Millisecond
	// safePowerOffMaxPids and safePowerOffMaxFds cap the scan's work.
	safePowerOffMaxPids = 2048
	safePowerOffMaxFds  = 30000
)

// safePowerOffOn mirrors the setting: the power off may run on the exit
// watchdog's goroutine, where the config is not read.
var safePowerOffOn atomic.Bool

func init() {
	safePowerOffOn.Store(true)
	deferStartupTask("safe-power-off", func() { safePowerOffOn.Store(safePowerOffEnabled(appConfig)) })
}

// safePowerOffEnabled reports the setting; unset is On.
func safePowerOffEnabled(c *Config) bool {
	if c != nil {
		if v, ok := customValue(c.Variables.Custom, safePowerOffKey); ok {
			return coerceBool(v)
		}
	}
	return true
}

// safePowerOffSettingsRow is Settings → Power & battery → Safe power off.
func safePowerOffSettingsRow() setRow {
	return setRow{
		ID: safePowerOffKey, Label: "Safe power off", Kind: setKindToggle,
		Detail: func(c *Config) string {
			if !safePowerOffEnabled(c) {
				return "Off: the card is synced, then the handheld powers off"
			}
			return "Closes programs using the card, syncs it and makes it read-only before powering off"
		},
		Visible: func(*Config) bool { return devPowerSupported() && !jukaosNativeActive() },
		Get:     safePowerOffEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, safePowerOffKey, on)
			safePowerOffOn.Store(on)
			setToast("Safe power off: " + setOnOff(on))
		},
	}
}

// safePowerOffPrepare runs the steps for verb ("poweroff" or "reboot") and
// returns what undoes the read-only card if verb then fails. Never blocks for
// longer than safePowerOffBudget; never panics out.
func safePowerOffPrepare(verb string) (undo func()) {
	undo = func() {}
	// JukaHub OS's own system shuts down through its init, which unmounts
	// the card itself (jukaos_native.go).
	if !devPowerSupported() || !safePowerOffOn.Load() || jukaosNativeActive() {
		return undo
	}
	defer func() {
		if r := recover(); r != nil {
			logWarn("power", "safe power off stopped: %v", r)
		}
	}()
	return safePowerOffRun(verb, time.Now().Add(safePowerOffBudget))
}
