package main

// framestats_test.go — the histogram that answers "what does a frame cost on
// this device". The figures are the reason it exists, so the buckets, the
// percentiles and the window reset are pinned here; a wrong bucket would put a
// 30 fps device in the green.

import (
	"strings"
	"testing"
	"time"
)

func TestFrameBucketBoundaries(t *testing.T) {
	cases := []struct {
		ms   float64
		want int
	}{
		{0, 0},
		{7.9, 0},
		{8, 0}, // exactly half a 60 Hz frame is still in budget
		{8.1, 1},
		{17, 1}, // exactly one frame is a hit, not a miss
		{17.1, 2},
		{33, 2},
		{33.1, 3},
		{50, 3},
		{50.1, 4},
		{100, 4},
		{100.1, frameBucketOverflow},
		{250, frameBucketOverflow},
	}
	for _, c := range cases {
		if got := bucketForMS(c.ms); got != c.want {
			t.Errorf("bucketForMS(%.1f) = %d, want %d", c.ms, got, c.want)
		}
	}
}

// newTestFrameStats is a histogram with a clock the test drives.
func newTestFrameStats() (*frameStatsState, *time.Time) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock := &now
	state := &frameStatsState{logInterval: frameStatsLogInterval, now: func() time.Time { return *clock }}
	return state, clock
}

// TestFrameStatsPercentilesAndShares: 6 of 10 frames at 5ms, 3 at 20ms and one
// at 40ms must read as a 60 fps device with a visible tail — p50 ≤8ms, p99
// ≤50ms, and 40% of frames over the 17ms budget.
func TestFrameStatsPercentilesAndShares(t *testing.T) {
	state, clock := newTestFrameStats()
	for i := 0; i < 6; i++ {
		state.add(5*time.Millisecond, *clock)
	}
	for i := 0; i < 3; i++ {
		state.add(20*time.Millisecond, *clock)
	}
	state.add(40*time.Millisecond, *clock)

	snap := state.snapshot()
	if snap.Frames != 10 {
		t.Fatalf("frames = %d, want 10", snap.Frames)
	}
	if got, want := snap.percentMS(0.50), 8.0; got != want {
		t.Errorf("p50 = %.0f, want %.0f", got, want)
	}
	if got, want := snap.percentMS(0.90), 33.0; got != want {
		t.Errorf("p90 = %.0f, want %.0f (the 20ms frames live in the ≤33 bucket)", got, want)
	}
	if got, want := snap.percentMS(0.99), 50.0; got != want {
		t.Errorf("p99 = %.0f, want %.0f", got, want)
	}
	if got, want := snap.cumulativeShare(17), 0.6; got != want {
		t.Errorf("share ≤17ms = %.2f, want %.2f", got, want)
	}
	if got, want := snap.MaxMS, 40.0; got != want {
		t.Errorf("max = %.1f, want %.1f", got, want)
	}
	if got, want := snap.AvgMS, 13.0; got != want {
		t.Errorf("avg = %.1f, want %.1f", got, want)
	}
}

// TestFrameStatsLogsPerWindow: the line describes the window that just ended and
// the next window starts empty, so a slow boot does not colour an hour of report.
func TestFrameStatsLogsPerWindow(t *testing.T) {
	state, clock := newTestFrameStats()
	state.logInterval = time.Second

	line, ok := state.add(9*time.Millisecond, *clock)
	if ok {
		t.Fatalf("the first frame ended a window: %q", line)
	}
	*clock = clock.Add(1500 * time.Millisecond)
	line, ok = state.add(40*time.Millisecond, *clock)
	if !ok {
		t.Fatal("a 1.5s window did not report")
	}
	for _, want := range []string{"[frame]", "2 frames", "avg 24.5ms", "over 17ms 50.0%"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q is missing %q", line, want)
		}
	}
	if strings.Contains(line, "fps") && !strings.Contains(line, "(1.3 fps)") {
		t.Errorf("log line %q has an unexpected rate", line)
	}

	// The next window holds only what happened after the log.
	*clock = clock.Add(time.Second)
	line, ok = state.add(5*time.Millisecond, *clock)
	if !ok {
		t.Fatal("the second window did not report")
	}
	if !strings.Contains(line, "1 frames") || !strings.Contains(line, "avg 5.0ms") {
		t.Errorf("second window line = %q, want only the new frame", line)
	}
	// The lifetime totals keep both windows for the report.
	if got := state.snapshot().Frames; got != 3 {
		t.Errorf("lifetime frames = %d, want 3", got)
	}
}

// TestFrameStatsProbeIntervalIsTheOnlyEnvSwitch keeps the default quiet: one
// line a minute, five seconds only when asked.
func TestFrameStatsProbeIntervalIsTheOnlyEnvSwitch(t *testing.T) {
	if got := newFrameStats().logInterval; got != frameStatsLogInterval {
		t.Fatalf("default interval = %s, want %s", got, frameStatsLogInterval)
	}
	t.Setenv("JUKAHUB_FRAMESTATS", "1")
	if got := newFrameStats().logInterval; got != frameStatsProbeInterval {
		t.Fatalf("probe interval = %s, want %s", got, frameStatsProbeInterval)
	}
}

// TestFrameStatsReportBlock: the diagnostics report carries the histogram, and
// says so plainly before any frame has been drawn.
func TestFrameStatsReportBlock(t *testing.T) {
	prev := frameStats
	t.Cleanup(func() { frameStats = prev })

	frameStats = newFrameStats()
	if lines := frameStats.snapshot().diagnosticsLines(); len(lines) != 1 ||
		!strings.Contains(lines[0], "no frames measured yet") {
		t.Fatalf("empty report = %v, want the explicit no-frame line", lines)
	}

	now := time.Now()
	for i := 0; i < 9; i++ {
		frameStats.add(7*time.Millisecond, now)
	}
	frameStats.add(60*time.Millisecond, now.Add(time.Second))
	lines := frameStats.snapshot().diagnosticsLines()
	if len(lines) != 2 {
		t.Fatalf("report block = %v, want two lines", lines)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Frame time   : 10 frames", "avg 12.3ms", "max 60ms", "Frame share  :"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report block %q is missing %q", joined, want)
		}
	}
	// And it reaches the report itself, which is what a bug report quotes.
	withTempPatchState(t)
	text := DiagnosticsText(nil)
	if !strings.Contains(text, "Frame time   : 10 frames") {
		t.Fatalf("DiagnosticsText has no frame-time line:\n%s", text)
	}
}
