//go:build !linux

package main

// powersleep_other.go — sleepNow off Linux. JukaHub has no safe way to sleep
// a PC (there is no existing helper for it, and a desktop app putting the
// whole machine to sleep is the OS's call, not a launcher's), so sleepNow
// only says so (powersleep.go checks powerSleepSupported first). The rest are
// the no-op halves the shared code calls.

import "time"

// powerSleepSupported is false: sleep is the handheld's.
func powerSleepSupported() bool { return false }

// powerSleepAsleepClock has no suspended-time clock here.
func powerSleepAsleepClock() (time.Duration, bool) { return 0, false }

// powerRTCWakeIn arms nothing.
func powerRTCWakeIn(time.Duration) func() { return func() {} }

// powerChildGroupStopped cannot tell; nothing is paused here anyway
// (alarmCanPauseChildren is false on Windows).
func powerChildGroupStopped(int) bool { return false }
