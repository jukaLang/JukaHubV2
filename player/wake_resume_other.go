//go:build !linux

package main

// wake_resume_other.go — the clocks behind the wake watcher (wake_resume.go)
// off Linux. Windows has no BOOTTIME pair Go exposes, so the sleep-counting
// clock is the wall clock and the other is Go's monotonic clock. On Windows
// that monotonic clock reads the interrupt time, which keeps counting through
// sleep and hibernation: the watcher's tick then simply arrives late, and the
// gap is the sleep. Where the monotonic clock stops in a sleep instead, the
// wall clock running ahead of it is the gap.

import "time"

// wakeClockIsWall reports whether the sleep-counting clock is the wall clock,
// so JukaHub's own clock jumps are taken off it.
const wakeClockIsWall = true

// wakeCanTellFrozen reports whether a stopped process can be told from a
// sleeping system: not here, so every gap counts as a sleep.
const wakeCanTellFrozen = false

// wakeEpoch anchors the monotonic reading.
var wakeEpoch = time.Now()

// wakeClocks reads the wall clock and the monotonic time since start.
func wakeClocks() (wakeSample, bool) {
	now := time.Now()
	return wakeSample{withSleep: time.Duration(now.UnixNano()), mono: now.Sub(wakeEpoch)}, true
}
