package main

// framestats.go — the frame-time histogram.
//
// render_bench_test.go and the memory harness compare cost *relative to the
// desktop they run on*, which is enough to catch a regression in review and
// useless for the question a handheld owner actually asks: does this build hold
// its frame budget on a Smart Pro? That number can only be measured on the
// device, so the main loop keeps a histogram of how long each iteration takes —
// input, scene logic, drawing and Present — and logs it.
//
// The buckets follow the budgets that matter on the hardware rather than even
// steps: 8 ms is half a 60 Hz frame, 17 ms is a whole one, 33 ms is 30 fps, 50 ms
// is a hitch you feel, and 100 ms is a stall. A percentile is therefore reported
// as an upper bound ("p50 ≤8ms") — honest about being a bucket, and precise
// enough to see which side of a vsync a frame landed on.
//
// The line describes the window that just ended, not the session: a boot that
// started slowly would otherwise make every later report look bad. The lifetime
// histogram is kept for the diagnostics report, which is what a bug report
// carries. Logging runs once a minute — enough to watch a long session, small
// enough for the 2000-line ring buffer — and JUKAHUB_FRAMESTATS=1 switches to a
// 5-second window for a focused measurement.

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// frameBucketBoundsMS are the upper edges of the histogram buckets, in
// milliseconds; a frame in the last bucket is slower than every bound.
var frameBucketBoundsMS = []float64{8, 17, 33, 50, 100}

const (
	// frameBucketOverflow indexes the "slower than every bound" bucket.
	frameBucketOverflow = 5
	// frameStatsLogInterval is the normal reporting cadence.
	frameStatsLogInterval = time.Minute
	// frameStatsProbeInterval is the cadence when JUKAHUB_FRAMESTATS=1 asks for
	// a measurement session.
	frameStatsProbeInterval = 5 * time.Second
	// frameMissBudgetMS is the bound the "over" share is stated against: one
	// 60 Hz frame. Isolated misses are survivable; a steady percentage is not.
	frameMissBudgetMS = 17
)

// frameTimeSnapshot is one histogram reading. It is used for both the window
// (what the log line describes) and the lifetime figures (what the diagnostics
// report carries); only the duration fields differ in meaning.
type frameTimeSnapshot struct {
	Frames  uint64
	Buckets [frameBucketOverflow + 1]uint64
	AvgMS   float64
	MaxMS   float64
	// Elapsed is how long the sample covers: the window for a log line, the
	// session for the report. It is what turns frame counts into a rate.
	Elapsed time.Duration
}

// frameStatsState is the live histogram. One global instance is fed by the main
// loop; tests build their own, so a test run cannot disturb the report.
type frameStatsState struct {
	mu sync.Mutex

	// lifetime
	buckets [frameBucketOverflow + 1]uint64
	frames  uint64
	sumMS   float64
	maxMS   float64
	started time.Time

	// current window
	windowBuckets [frameBucketOverflow + 1]uint64
	windowFrames  uint64
	windowSumMS   float64
	windowMaxMS   float64
	windowStart   time.Time

	// logInterval is the reporting cadence.
	logInterval time.Duration
	// now is the clock, swappable so a test can age a window without sleeping.
	now func() time.Time
}

var frameStats = newFrameStats()

func newFrameStats() *frameStatsState {
	interval := frameStatsLogInterval
	if os.Getenv("JUKAHUB_FRAMESTATS") == "1" {
		interval = frameStatsProbeInterval
	}
	return &frameStatsState{logInterval: interval, now: time.Now}
}

// frameStatsRecord adds one frame's duration and logs a line when the window has
// run out. It is called from the main loop after Present, so what it counts is
// the work a frame costs — not the idle delay that follows it.
func frameStatsRecord(d time.Duration) {
	if line, ok := frameStats.add(d, frameStats.now()); ok {
		log.Print(line)
	}
}

// add records one frame and reports whether the window ended, with the line to
// log. The clock is a parameter so the gating is testable without waiting a
// minute of wall time.
func (s *frameStatsState) add(d time.Duration, now time.Time) (string, bool) {
	if d < 0 {
		d = 0
	}
	ms := float64(d) / float64(time.Millisecond)
	idx := bucketForMS(ms)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.buckets[idx]++
	s.frames++
	s.sumMS += ms
	if ms > s.maxMS {
		s.maxMS = ms
	}
	if s.started.IsZero() {
		s.started = now
	}

	s.windowBuckets[idx]++
	s.windowFrames++
	s.windowSumMS += ms
	if ms > s.windowMaxMS {
		s.windowMaxMS = ms
	}
	if s.windowStart.IsZero() {
		s.windowStart = now
	}
	if now.Sub(s.windowStart) < s.logInterval {
		return "", false
	}

	line := s.windowLocked(now).LogLine()
	s.windowBuckets = [frameBucketOverflow + 1]uint64{}
	s.windowFrames = 0
	s.windowSumMS = 0
	s.windowMaxMS = 0
	s.windowStart = now
	return line, true
}

// snapshot is the reading DiagnosticsText prints: lifetime figures.
func (s *frameStatsState) snapshot() frameTimeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := frameTimeSnapshot{
		Frames:  s.frames,
		Buckets: s.buckets,
		MaxMS:   s.maxMS,
	}
	if s.frames > 0 {
		snap.AvgMS = s.sumMS / float64(s.frames)
	}
	if !s.started.IsZero() {
		snap.Elapsed = s.now().Sub(s.started)
	}
	return snap
}

// windowLocked is the reading for the window that just ended. Caller holds mu.
func (s *frameStatsState) windowLocked(now time.Time) frameTimeSnapshot {
	snap := frameTimeSnapshot{
		Frames:  s.windowFrames,
		Buckets: s.windowBuckets,
		MaxMS:   s.windowMaxMS,
	}
	if s.windowFrames > 0 {
		snap.AvgMS = s.windowSumMS / float64(s.windowFrames)
	}
	if !s.windowStart.IsZero() {
		snap.Elapsed = now.Sub(s.windowStart)
	}
	return snap
}

// bucketForMS is the histogram slot a frame of this length lands in. The bounds
// are inclusive, so a frame that took exactly 17 ms is still inside the 60 Hz
// budget rather than counted as a miss.
func bucketForMS(ms float64) int {
	for i, bound := range frameBucketBoundsMS {
		if ms <= bound {
			return i
		}
	}
	return frameBucketOverflow
}

// percentMS is the upper bound of the bucket the p-th percentile falls in, or
// the observed maximum when that is the overflow bucket.
func (s frameTimeSnapshot) percentMS(p float64) float64 {
	if s.Frames == 0 || p <= 0 {
		return 0
	}
	target := p * float64(s.Frames)
	var seen uint64
	for i, n := range s.Buckets {
		seen += n
		if float64(seen) >= target {
			if i >= len(frameBucketBoundsMS) {
				return s.MaxMS
			}
			return frameBucketBoundsMS[i]
		}
	}
	return s.MaxMS
}

// cumulativeShare is the fraction of frames at or under a bound.
func (s frameTimeSnapshot) cumulativeShare(bound float64) float64 {
	if s.Frames == 0 {
		return 0
	}
	var under uint64
	for i, n := range s.Buckets {
		if i >= len(frameBucketBoundsMS) || frameBucketBoundsMS[i] > bound {
			break
		}
		under += n
	}
	return float64(under) / float64(s.Frames)
}

// fps is the average frame rate of the sample.
func (s frameTimeSnapshot) fps() float64 {
	if s.Elapsed <= 0 || s.Frames == 0 {
		return 0
	}
	return float64(s.Frames) / s.Elapsed.Seconds()
}

// LogLine describes one window: how many frames, at what rate, and where the
// slow ones went. One line, because a log that needs reading twice is a log
// nobody reads.
func (s frameTimeSnapshot) LogLine() string {
	if s.Frames == 0 {
		return fmt.Sprintf("[frame] last %s: no frames presented", shortDuration(s.Elapsed))
	}
	return fmt.Sprintf("[frame] last %s: %d frames (%.1f fps), avg %.1fms, p50 ≤%.0fms, p99 ≤%.0fms, max %.0fms, over %dms %.1f%%",
		shortDuration(s.Elapsed), s.Frames, s.fps(), s.AvgMS,
		s.percentMS(0.50), s.percentMS(0.99), s.MaxMS,
		frameMissBudgetMS, (1-s.cumulativeShare(frameMissBudgetMS))*100)
}

// diagnosticsLines are the two report lines: the session's histogram, then the
// share table a maintainer reads to see where the frames went.
func (s frameTimeSnapshot) diagnosticsLines() []string {
	if s.Frames == 0 {
		return []string{"Frame time   : no frames measured yet"}
	}
	rate := ""
	if fps := s.fps(); fps > 0 {
		rate = fmt.Sprintf(" in %s (%.1f fps)", shortDuration(s.Elapsed), fps)
	}
	return []string{
		fmt.Sprintf("Frame time   : %d frames%s, avg %.1fms, p50 ≤%.0fms, p99 ≤%.0fms, max %.0fms",
			s.Frames, rate, s.AvgMS, s.percentMS(0.50), s.percentMS(0.99), s.MaxMS),
		fmt.Sprintf("Frame share  : ≤8ms %.1f%%, ≤17ms %.1f%%, ≤33ms %.1f%%, >33ms %.1f%%",
			s.cumulativeShare(8)*100, s.cumulativeShare(17)*100, s.cumulativeShare(33)*100,
			(1-s.cumulativeShare(33))*100),
	}
}

// shortDuration renders a sample length compactly: "58s", "5m", "1m30s".
func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	mins := int(d / time.Minute)
	secs := int(d % time.Minute)
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm%ds", mins, secs)
}
