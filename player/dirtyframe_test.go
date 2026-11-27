package main

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The backlog's Verify for PERF-3: a test of the redraw decision — idle asks
// for at most a frame a second; input, a scene change, a toast, a progress
// tick or a clock minute ask for one at once.
//
// The loop's own cadences, from frameProfileFor: the handheld idles at 66 ms
// a frame, a desktop at 50 ms, and a busy desktop at 16 ms.
var dirtyFrameTestCadences = []struct {
	name string
	step time.Duration
}{
	{"handheld deep idle", 66 * time.Millisecond},
	{"desktop deep idle", 50 * time.Millisecond},
	{"desktop active", 16 * time.Millisecond},
}

// A screen nothing touches must ask for at most one frame a second, whatever
// cadence the loop polls at. Before PERF-3 every poll drew and presented.
func TestIdleAsksForAtMostOneFrameASecond(t *testing.T) {
	const window = 10 * time.Second
	for _, cadence := range dirtyFrameTestCadences {
		t.Run(cadence.name, func(t *testing.T) {
			var lastPresent time.Time
			lastMinute := 0
			start := time.Unix(1_700_000_000, 0)
			minute := start.Hour()*60 + start.Minute()
			var presented []time.Time
			for elapsed := time.Duration(0); elapsed < window; elapsed += cadence.step {
				now := start.Add(elapsed)
				if frameShouldPresent(lastPresent, now, false, false, minute, lastMinute) {
					presented = append(presented, now)
					lastPresent, lastMinute = now, minute
				}
			}
			polls := int(window / cadence.step)
			// One frame for the very first decision (nothing is on screen yet),
			// then no more than one a second. The gaps come out slightly over a
			// second because the decision is only asked on a poll, never under
			// one.
			if most := 1 + int(window/dirtyFrameSafetyRedraw); len(presented) > most {
				t.Fatalf("%d frames over %v at %v (drawn %d times), want at most %d: idle presented %v",
					len(presented), window, cadence.step, polls, most, presented)
			}
			if len(presented) < 2 {
				t.Fatalf("idle presented %d frames, want the first frame and a safety redraw", len(presented))
			}
			// No two frames closer together than the safety period: the "one a
			// second" is a floor on the gap, not just a total.
			for i := 1; i < len(presented); i++ {
				if gap := presented[i].Sub(presented[i-1]); gap < dirtyFrameSafetyRedraw {
					t.Errorf("frames %d and %d are %v apart, want at least %v",
						i-1, i, gap, dirtyFrameSafetyRedraw)
				}
			}
		})
	}
}

// Every change the backlog lists must ask for a frame on the spot, not on the
// safety tick: a press, a scene change, a toast, a progress tick and the clock
// reaching a new minute.
func TestChangesAskForAFrameAtOnce(t *testing.T) {
	presented := time.Unix(1_700_000_000, 0)
	minute := presented.Hour()*60 + presented.Minute()
	cases := []struct {
		name     string
		dirty    bool
		animated bool
		clockMin int
		want     bool
		because  string
	}{
		{"input or a scene change", true, false, minute, true, "dirty"},
		{"a toast, progress or animation", false, true, minute, true, "animated"},
		{"the clock's minute", false, false, minute + 1, true, "clock"},
		{"nothing at all, one tick later", false, false, minute, false, "idle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := frameShouldPresent(presented, presented.Add(16*time.Millisecond), tc.dirty, tc.animated, tc.clockMin, minute)
			if got != tc.want {
				t.Fatalf("%s: frameShouldPresent = %v, want %v", tc.because, got, tc.want)
			}
		})
	}
}

// A dirty flag has to survive until a frame consumes it, or a press that lands
// between two frames is lost and the screen keeps the old picture until the
// safety redraw.
func TestDirtyFlagIsConsumedByTheNextFrame(t *testing.T) {
	frameDirtyFlag.Store(false)
	frameMarkDirty()
	if !frameDirtyFlag.Swap(false) {
		t.Fatal("the dirty flag was not raised")
	}
	if frameDirtyFlag.Swap(false) {
		t.Fatal("the dirty flag was not consumed by the swap")
	}
	// Two changes between frames cost one redraw, not two.
	frameMarkDirty()
	frameMarkDirty()
	if !frameDirtyFlag.Swap(false) {
		t.Fatal("the second change cleared the flag")
	}
}

// The safety redraw is the promise that nothing looks stale: a screen left
// alone is never more than a second behind. A wall clock that steps backwards
// must not stretch that second into the length of the step.
func TestSafetyRedrawBoundsStaleness(t *testing.T) {
	last := time.Unix(1_700_000_000, 0)
	minute := last.Hour()*60 + last.Minute()
	if frameShouldPresent(last, last.Add(dirtyFrameSafetyRedraw-time.Millisecond), false, false, minute, minute) {
		t.Error("a frame was drawn before the safety period ended")
	}
	if !frameShouldPresent(last, last.Add(dirtyFrameSafetyRedraw), false, false, minute, minute) {
		t.Error("the safety redraw did not come due at one second")
	}
	// The clock went back an hour (NTP, the user setting the time).
	if !frameShouldPresent(last, last.Add(-time.Hour), false, false, minute, minute) {
		t.Error("a clock that stepped backwards left the old frame up")
	}
}

// The ledger the loop actually calls has to skip an unchanged iteration: this
// is the wiring, not just the formula (frameDirtyDue is what main.go asks).
func TestDirtyFrameDueSkipsAnIdleIteration(t *testing.T) {
	framesPresented = framePresented{}
	frameDirtyFlag.Store(false)
	now := time.Unix(1_700_000_000, 0)
	if !dirtyFrameDue(now, false, false) {
		t.Fatal("the first frame was skipped")
	}
	if dirtyFrameDue(now.Add(16*time.Millisecond), false, false) {
		t.Fatal("an unchanged iteration drew a frame")
	}
	// A press (frameMarkDirty, which NotifyInput calls) draws at once.
	frameMarkDirty()
	if !dirtyFrameDue(now.Add(32*time.Millisecond), false, false) {
		t.Fatal("a press did not draw the frame that shows it")
	}
	// A start's frames are never skipped.
	if !dirtyFrameDue(now.Add(48*time.Millisecond), false, true) {
		t.Fatal("a starting frame was skipped")
	}
	// The safety redraw catches everything that changed without saying so.
	if !dirtyFrameDue(now.Add(48*time.Millisecond+dirtyFrameSafetyRedraw), false, false) {
		t.Fatal("the safety redraw did not come due")
	}
}

// A toast that expires must be painted away at once: its disappearance is the
// change, even though nothing was pressed and no animation is running.
func TestExpiredToastIsPaintedAway(t *testing.T) {
	framesPresented = framePresented{}
	frameDirtyFlag.Store(false)
	currentToastMu.Lock()
	currentToast = ToastState{Message: "Download finished", Kind: ToastKindSuccess, StartedAt: sdl.GetTicks64(), Duration: 5000}
	currentToastMu.Unlock()
	t.Cleanup(func() {
		currentToastMu.Lock()
		currentToast = ToastState{}
		currentToastMu.Unlock()
	})
	now := time.Now()
	if !dirtyFrameDue(now, false, false) {
		t.Fatal("the first frame with a toast was skipped")
	}
	if !dirtyFrameDue(now.Add(16*time.Millisecond), false, false) {
		t.Fatal("a toast on screen was left without its fade")
	}
	// Past its duration: the frame that clears it must be drawn now, not on
	// the next safety tick.
	currentToastMu.Lock()
	currentToast.StartedAt = sdl.GetTicks64() - 6000
	currentToastMu.Unlock()
	if !dirtyFrameDue(now.Add(32*time.Millisecond), false, false) {
		t.Fatal("an expired toast was left on the panel until the safety redraw")
	}
}

// The first frame always draws: the window is empty before it.
func TestFirstFrameAlwaysDraws(t *testing.T) {
	if !frameShouldPresent(time.Time{}, time.Unix(1_700_000_000, 0), false, false, 0, 0) {
		t.Fatal("the first frame was skipped")
	}
}

// Before-vs-after, measured by the decision itself: the loop polls at the idle
// cadence and used to draw on every poll; now it draws on the safety tick.
// These are the numbers the changelog records.
func TestIdleDrawReduction(t *testing.T) {
	const window = 10 * time.Second
	for _, cadence := range dirtyFrameTestCadences {
		polls := int(window / cadence.step)
		start := time.Unix(1_700_000_000, 0)
		minute := start.Hour()*60 + start.Minute()
		last := start
		lastMinute := minute
		drawn := 1 // the first frame
		for elapsed := cadence.step; elapsed < window; elapsed += cadence.step {
			now := start.Add(elapsed)
			if frameShouldPresent(last, now, false, false, minute, lastMinute) {
				drawn++
				last, lastMinute = now, minute
			}
		}
		t.Logf("%s: %d polls, %d draws before, %d draws after (%.0f%% fewer)",
			cadence.name, polls, polls, drawn, 100*(1-float64(drawn)/float64(polls)))
		if drawn > 1+int(window/dirtyFrameSafetyRedraw) {
			t.Errorf("%s: %d draws in ten idle seconds, want at most one a second", cadence.name, drawn)
		}
	}
}

// The decision runs on every loop iteration, so it has to be free: no
// allocation, a few comparisons.
func BenchmarkFrameShouldPresentIdle(b *testing.B) {
	last := time.Unix(1_700_000_000, 0)
	now := last.Add(500 * time.Millisecond)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		frameShouldPresent(last, now, false, false, 0, 0)
	}
}
