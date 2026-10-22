package main

// startup_test.go — the boot budget: how much splash a launch earns, and what
// the timing report says.

import (
	"os"
	"path/filepath"
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

func TestWarmStartWithin(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	if warmStartWithin(time.Time{}, now) {
		t.Error("a missing marker counted as a warm start")
	}
	if !warmStartWithin(now.Add(-time.Minute), now) {
		t.Error("a launch one minute ago should be warm")
	}
	if warmStartWithin(now.Add(-startupWarmWindow-time.Second), now) {
		t.Error("a launch outside the window counted as warm")
	}
	if warmStartWithin(now.Add(time.Minute), now) {
		t.Error("a future marker counted as warm; a clock change would hide the splash forever")
	}
}

func TestStartupStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-state.json")
	want := startupState{
		Version:    startupStateVersion,
		LastLaunch: time.Now().UTC().Truncate(time.Second),
		TotalMS:    1120,
		TargetMS:   2000,
	}
	if err := saveStartupState(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok := loadStartupState(path)
	if !ok {
		t.Fatal("saved state did not load")
	}
	if !got.LastLaunch.Equal(want.LastLaunch) || got.TotalMS != want.TotalMS || got.Version != want.Version {
		t.Errorf("state = %+v, want %+v", got, want)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadStartupState(path); ok {
		t.Error("corrupt state loaded as a warm start")
	}
}

func TestStartupWarmStartUsesTheMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-state.json")
	t.Setenv(startupStateEnv, path)
	t.Setenv("JUKAHUB_STARTUP_BENCH", "")
	t.Setenv("JUKAHUB_BENCH_WARM", "")
	t.Cleanup(func() { startupStatePathOverride = "" })

	if startupWarmStart(time.Now()) {
		t.Error("warm start reported without a marker")
	}
	if err := saveStartupState(path, startupState{
		Version:    startupStateVersion,
		LastLaunch: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if !startupWarmStart(time.Now()) {
		t.Error("recent marker did not produce a warm start")
	}
	if err := saveStartupState(path, startupState{
		Version:    startupStateVersion,
		LastLaunch: time.Now().Add(-startupWarmWindow - time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if startupWarmStart(time.Now()) {
		t.Error("old marker still produced a warm start")
	}
}

func TestStartupRecordFirstFrameWritesTheMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-state.json")
	t.Setenv(startupStateEnv, path)
	t.Setenv("JUKAHUB_STARTUP_BENCH", "")
	startupRecordFirstFrame(1234 * time.Millisecond)
	state, ok := loadStartupState(path)
	if !ok {
		t.Fatal("first frame did not write a marker")
	}
	if state.TotalMS != 1234 || state.TargetMS != startupTarget().Milliseconds() {
		t.Errorf("marker = %+v, want total 1234 and the current target", state)
	}

	// The bench must not leave a marker behind: a CI run would then make the
	// next sample look warm.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUKAHUB_STARTUP_BENCH", "1")
	startupRecordFirstFrame(1234 * time.Millisecond)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("bench mode wrote a warm-start marker: %v", err)
	}
}

func TestStartupBenchForcesColdUnlessAsked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-state.json")
	t.Setenv(startupStateEnv, path)
	if err := saveStartupState(path, startupState{Version: startupStateVersion, LastLaunch: time.Now()}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUKAHUB_BENCH_WARM", "")
	t.Setenv("JUKAHUB_STARTUP_BENCH", "1")
	if startupWarmStart(time.Now()) {
		t.Error("the bench skipped the splash without being asked")
	}
	t.Setenv("JUKAHUB_BENCH_WARM", "1")
	if !startupWarmStart(time.Now()) {
		t.Error("JUKAHUB_BENCH_WARM did not exercise the warm path")
	}
}

func TestDeferredStartupRunsInOrderAndOnlyOnce(t *testing.T) {
	runDeferredStartup() // drop anything a previous test queued
	t.Cleanup(runDeferredStartup)

	var order []string
	deferStartupTask("first", func() { order = append(order, "first") })
	deferStartupTask("second", func() { order = append(order, "second") })
	deferStartupTask("", nil) // ignored
	runDeferredStartup()
	if strings.Join(order, ",") != "first,second" {
		t.Fatalf("task order = %v, want first,second", order)
	}
	runDeferredStartup()
	if len(order) != 2 {
		t.Errorf("deferred tasks ran twice: %v", order)
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
