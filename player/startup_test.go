package main

// startup_test.go — the boot budget: how much splash a launch earns, and what
// the timing report says.

import (
	"strings"
	"testing"
	"time"
)

func TestSplashAllotment(t *testing.T) {
	target := 2000 * time.Millisecond
	reserve := 300 * time.Millisecond
	cases := []struct {
		name    string
		elapsed time.Duration
		want    time.Duration
	}{
		{"instant boot is capped by the splash maximum", 0, 750 * time.Millisecond},
		{"fast boot keeps the cap", 600 * time.Millisecond, 750 * time.Millisecond},
		{"mid budget is the remainder", 1000 * time.Millisecond, 700 * time.Millisecond},
		{"slow boot gets the remainder", 1300 * time.Millisecond, 400 * time.Millisecond},
		{"too little left is skipped", 1500 * time.Millisecond, 0},
		{"over budget is skipped", 2500 * time.Millisecond, 0},
	}
	for _, c := range cases {
		got := splashAllotment(c.elapsed, target, reserve)
		if got != c.want {
			t.Errorf("%s: elapsed %v -> %v, want %v", c.name, c.elapsed, got, c.want)
		}
	}
	if got := splashAllotment(0, 0, reserve); got != 0 {
		t.Errorf("zero target -> %v, want no splash", got)
	}
	// The allotment plus the boot cost plus the first-frame reserve may never
	// push a launch past the budget.
	for _, elapsed := range []time.Duration{0, 250 * time.Millisecond, 700 * time.Millisecond, 1100 * time.Millisecond} {
		reserve := splashReserve(elapsed)
		if total := elapsed + splashAllotment(elapsed, target, reserve) + reserve; total > target {
			t.Errorf("elapsed %v: plan spends %v, over the %v budget", elapsed, total, target)
		}
	}
}

func TestSplashReserve(t *testing.T) {
	// The floor covers the first, fully cold, UI frame.
	if got := splashReserve(0); got != startupFirstFrameReserveMs*time.Millisecond {
		t.Errorf("reserve at start = %v", got)
	}
	if got := splashReserve(400 * time.Millisecond); got != startupFirstFrameReserveMs*time.Millisecond {
		t.Errorf("reserve for a fast boot = %v", got)
	}
	// A slow boot implies a slow first frame, so the reserve grows with it.
	if got := splashReserve(1200 * time.Millisecond); got != 600*time.Millisecond {
		t.Errorf("reserve for a slow boot = %v, want half the elapsed time", got)
	}
}

func TestStartupTargetFromEnv(t *testing.T) {
	t.Setenv("JUKAHUB_STARTUP_TARGET_MS", "")
	if got := startupTarget(); got != startupTargetDefaultMs*time.Millisecond {
		t.Errorf("default target = %v", got)
	}
	t.Setenv("JUKAHUB_STARTUP_TARGET_MS", "3500")
	if got := startupTarget(); got != 3500*time.Millisecond {
		t.Errorf("override target = %v", got)
	}
	// Nonsense is ignored rather than producing an absurd budget.
	t.Setenv("JUKAHUB_STARTUP_TARGET_MS", "soon")
	if got := startupTarget(); got != startupTargetDefaultMs*time.Millisecond {
		t.Errorf("bad override target = %v", got)
	}
	t.Setenv("JUKAHUB_STARTUP_TARGET_MS", "10")
	if got := startupTarget(); got != startupTargetDefaultMs*time.Millisecond {
		t.Errorf("out-of-range override target = %v", got)
	}
}

func TestStartupBenchFlag(t *testing.T) {
	for _, v := range []string{"1", "true", "YES"} {
		t.Setenv("JUKAHUB_STARTUP_BENCH", v)
		if !startupBenchEnabled() {
			t.Errorf("bench flag %q not recognised", v)
		}
	}
	for _, v := range []string{"", "0", "no"} {
		t.Setenv("JUKAHUB_STARTUP_BENCH", v)
		if startupBenchEnabled() {
			t.Errorf("bench flag %q should be off", v)
		}
	}
}

func TestStartupReportListsPhases(t *testing.T) {
	startupClock.mu.Lock()
	prevBegan, prevMarks := startupClock.began, startupClock.marks
	startupClock.began = time.Now()
	startupClock.marks = nil
	startupClock.mu.Unlock()
	t.Cleanup(func() {
		startupClock.mu.Lock()
		startupClock.began = prevBegan
		startupClock.marks = prevMarks
		startupClock.mu.Unlock()
	})

	startupMarkNow("pre-sdl")
	time.Sleep(2 * time.Millisecond)
	startupMarkNow("config")
	startupMarkNow("first-frame")

	report := startupReport()
	for _, want := range []string{"startup:", "pre-sdl=", "config=+", "first-frame=+", "total=", "target="} {
		if !strings.Contains(report, want) {
			t.Errorf("report %q is missing %q", report, want)
		}
	}
}

func TestStartupBudgetNote(t *testing.T) {
	if note := startupBudgetNote(0); note != "" {
		t.Errorf("in-budget note = %q, want none", note)
	}
	if note := startupBudgetNote(startupTarget() + time.Second); !strings.Contains(note, "over budget") {
		t.Errorf("over-budget note = %q", note)
	}
	// A launch that is inside the budget but has no time left for an
	// animation is a different case, and says so.
	tight := startupTarget() - 10*time.Millisecond
	if note := startupBudgetNote(tight); !strings.Contains(note, "not enough budget") {
		t.Errorf("tight-budget note = %q", note)
	}
	if note := startupBudgetNote(100 * time.Millisecond); note != "" {
		t.Errorf("early note = %q, want none", note)
	}
}
