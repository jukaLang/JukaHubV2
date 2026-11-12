//go:build linux

package main

// wake_resume_linux.go — the clocks behind the wake watcher (wake_resume.go)
// on Linux: CLOCK_BOOTTIME keeps counting while the device is suspended,
// CLOCK_MONOTONIC (Go's own monotonic clock here) does not, and neither is
// moved by settimeofday, network time or a time-zone change.

import (
	"time"

	"golang.org/x/sys/unix"
)

// wakeClockIsWall reports whether the sleep-counting clock is the wall clock,
// which JukaHub's own clock jumps move. BOOTTIME is not.
const wakeClockIsWall = false

// wakeCanTellFrozen reports whether a stopped process can be told from a
// sleeping system: on Linux the monotonic clock stops only for the latter.
const wakeCanTellFrozen = true

// wakeClocks reads BOOTTIME and MONOTONIC. It reports false when the kernel
// refuses either (a kernel older than 2.6.39 has no BOOTTIME).
func wakeClocks() (wakeSample, bool) {
	var boot, mono unix.Timespec
	if unix.ClockGettime(unix.CLOCK_BOOTTIME, &boot) != nil || unix.ClockGettime(unix.CLOCK_MONOTONIC, &mono) != nil {
		return wakeSample{}, false
	}
	return wakeSample{withSleep: time.Duration(boot.Nano()), mono: time.Duration(mono.Nano())}, true
}
