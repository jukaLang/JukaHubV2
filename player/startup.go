package main

// startup.go — how long boot takes, and how the splash fits inside it.
//
// The app used to play a fixed 3.6 s splash no matter how fast the rest of the
// boot was, so every launch cost at least that. The splash now gets whatever is
// left of a two-second budget: a warm device spends a few hundred milliseconds
// on it, a cold SD card skips it entirely because the budget is already gone.
//
// The timing marks are cheap (one slice append per phase) and always on. They
// answer "why is this slow?" from a single log line instead of guesswork, which
// matters on a handheld nobody can attach a profiler to.
//
// JUKAHUB_STARTUP_BENCH=1 makes the app exit after its first frame, so the boot
// path can be timed headless in CI or on a device over SSH.
// JUKAHUB_STARTUP_TARGET_MS overrides the budget (a slower card may prefer more
// splash, not less).

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// startupTargetDefaultMs is the budget from process start to usable UI.
	startupTargetDefaultMs = 2000
	// startupSplashMaxMs caps the splash even on a fast boot: the animation is
	// a greeting, not the point of the app. The whole 3.6 s arc is compressed
	// into this, so a short splash still shows the complete sequence.
	startupSplashMaxMs = 750
	// startupSplashMinMs is the shortest splash worth drawing. Below this the
	// flash of a warp animation reads as a glitch, so it is skipped instead.
	startupSplashMinMs = 250
	// startupFirstFrameReserveMs is held back for the first UI frame, which is
	// the most expensive one (every label is rasterised cold). Without it the
	// splash eats the budget and the app lands just over the target.
	startupFirstFrameReserveMs = 300
)

type startupMark struct {
	name string
	at   time.Duration
}

var startupClock = struct {
	mu    sync.Mutex
	began time.Time
	marks []startupMark
}{began: time.Now()}

// startupMarkNow records a phase boundary relative to process start.
func startupMarkNow(name string) {
	startupClock.mu.Lock()
	startupClock.marks = append(startupClock.marks, startupMark{name: name, at: time.Since(startupClock.began)})
	startupClock.mu.Unlock()
}

// startupElapsed is the time since the process started.
func startupElapsed() time.Duration { return time.Since(startupClock.began) }

// startupTarget is the boot budget, overridable for slow cards.
func startupTarget() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_STARTUP_TARGET_MS")); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms >= 500 && ms <= 30000 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return startupTargetDefaultMs * time.Millisecond
}

// splashAllotment returns how long the splash may run. elapsed is what boot has
// already cost before the splash, reserve is kept back for the first UI frame;
// the splash gets the rest of the budget, up to its own cap. It returns 0 when
// there is no meaningful time left, so a slow card opens the UI directly
// instead of adding an animation on top.
func splashAllotment(elapsed, target, reserve time.Duration) time.Duration {
	if target <= 0 {
		return 0
	}
	remaining := target - elapsed - reserve
	if remaining < startupSplashMinMs*time.Millisecond {
		return 0
	}
	if remaining > startupSplashMaxMs*time.Millisecond {
		return startupSplashMaxMs * time.Millisecond
	}
	return remaining
}

// splashReserve is the time held back for the first frame at this launch: the
// measured floor, or half of what boot has already cost when that is larger
// (a slow device has a slow first frame too).
func splashReserve(elapsed time.Duration) time.Duration {
	half := elapsed / 2
	if half > startupFirstFrameReserveMs*time.Millisecond {
		return half
	}
	return startupFirstFrameReserveMs * time.Millisecond
}

// startupBenchEnabled reports whether this run exits after its first frame.
func startupBenchEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_STARTUP_BENCH")))
	return v == "1" || v == "true" || v == "yes"
}

// startupReport is the one-line boot summary: the phases, then the total.
func startupReport() string {
	startupClock.mu.Lock()
	marks := append([]startupMark(nil), startupClock.marks...)
	startupClock.mu.Unlock()

	var b strings.Builder
	b.WriteString("startup:")
	for i, m := range marks {
		if i == 0 {
			fmt.Fprintf(&b, " %s=%dms", m.name, m.at.Milliseconds())
			continue
		}
		delta := m.at - marks[i-1].at
		fmt.Fprintf(&b, " %s=+%dms", m.name, delta.Milliseconds())
	}
	total := time.Since(startupClock.began)
	fmt.Fprintf(&b, " total=%dms target=%dms", total.Milliseconds(), startupTarget().Milliseconds())
	return b.String()
}

// startupLog writes the report once. It is called after the first frame, so the
// numbers are what the user actually waited for.
func startupLog() {
	log.Print(startupReport())
}

// startupBudgetNote explains a skipped splash in the log, which is the only
// evidence a user can send from the device.
func startupBudgetNote(elapsed time.Duration) string {
	target := startupTarget()
	if elapsed >= target {
		return "over budget before the splash; skipped"
	}
	if splashAllotment(elapsed, target, splashReserve(elapsed)) == 0 {
		return "not enough budget left for the splash; skipped"
	}
	return ""
}

// startupMarkBoot logs one line per launch at debug level for the log viewer,
// keeping Info for the summary.
func startupMarkBoot() {
	startupMarkNow("pre-sdl")
}
