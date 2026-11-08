package main

// Offscreen render checks for the glass teardown. These reuse the shared
// dummy-driver probe from installbar_render_test.go: the card is SDL
// primitives and text, so real pixels can be captured without a window.

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// countNonBG counts pixels that differ from the flat background, so a render
// test proves the panel actually painted instead of leaving the frame blank.
func (f *probeFrame) countNonBG() int {
	n := 0
	for y := int32(0); y < probeH; y += 4 {
		for x := int32(0); x < probeW; x += 4 {
			if !f.isBG(x, y) {
				n++
			}
		}
	}
	return n
}

// The teardown runs its groups in order, and a step that fails is recorded on
// the card without stopping the ones after it.
func TestShutdownStepsRunInOrderAndRecordResults(t *testing.T) {
	var order []string
	steps := []shutdownStep{
		{label: "first", group: 0, fn: func() (string, error) {
			order = append(order, "first")
			return "ok", nil
		}},
		{label: "refused", group: 1, fn: func() (string, error) {
			order = append(order, "refused")
			return "", errors.New("read-only")
		}},
		{label: "last", group: 2, inline: true, fn: func() (string, error) {
			order = append(order, "last")
			return "", nil
		}},
	}
	track := newStageTrack("Powering down", []string{"first", "refused", "last"})
	runShutdownSteps(steps, track, nil)

	if got := strings.Join(order, ","); got != "first,refused,last" {
		t.Fatalf("step order = %q", got)
	}
	snap := track.snapshot()
	if snap.Done != 3 || snap.Failed != 1 {
		t.Fatalf("done %d failed %d, want 3 and 1", snap.Done, snap.Failed)
	}
	if snap.Items[0].State != stageDone || snap.Items[0].Detail != "ok" {
		t.Fatalf("first row = %+v", snap.Items[0])
	}
	if snap.Items[1].State != stageFailed {
		t.Fatalf("refused step = %v, want failed", snap.Items[1].State)
	}
	if snap.Items[2].State != stageDone {
		t.Fatalf("step after a failure = %v, want it still ran", snap.Items[2].State)
	}
}

// The real sequence has to cover the three things the teardown promises: the
// processes that own the screen and speaker, the files that hold state, and the
// CPU settings a launch may have raised.
func TestShutdownSequenceCoversProcessesFilesAndCPU(t *testing.T) {
	steps := shutdownSteps(&Config{})
	if len(steps) < 5 {
		t.Fatalf("teardown has %d steps, want the full sequence", len(steps))
	}
	var labels []string
	for _, step := range steps {
		if strings.TrimSpace(step.label) == "" {
			t.Fatal("a teardown step has no label")
		}
		if step.fn == nil {
			t.Fatalf("step %q has no work", step.label)
		}
		labels = append(labels, strings.ToLower(step.label))
	}
	joined := strings.Join(labels, " · ")
	for _, want := range []string{"games", "audio", "downloads", "services", "saving", "cpu", "caches"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("teardown %q does not cover %q", joined, want)
		}
	}
	// The card is synced last wherever there is a sync to run.
	if last := labels[len(labels)-1]; shutdownSyncs() != strings.Contains(last, "card") {
		t.Fatalf("last step %q, sync expected %v", last, shutdownSyncs())
	}
	// Every step that can block has a group after or beside the ones that stop
	// children: nothing is persisted before the games are stopped.
	if steps[0].group != 0 || !strings.Contains(labels[0], "games") {
		t.Fatalf("the teardown does not start by stopping games: %q", labels[0])
	}
}

// A step that never returns costs its row, not the teardown: it is recorded as
// timed out and the next group still runs, well inside the budget.
func TestShutdownStepTimeoutDoesNotHoldTheTeardown(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	ran := make(chan struct{}, 1)
	steps := []shutdownStep{
		{label: "wedged", group: 0, timeout: 60 * time.Millisecond, fn: func() (string, error) {
			<-block
			return "", nil
		}},
		{label: "after", group: 1, fn: func() (string, error) {
			ran <- struct{}{}
			return "done", nil
		}},
	}
	track := newStageTrack("Shutting down", []string{"wedged", "after"})
	started := time.Now()
	runShutdownStepsUntil(steps, track, nil, time.Now().Add(2*time.Second))
	if took := time.Since(started); took > time.Second {
		t.Fatalf("a wedged step held the teardown for %v", took)
	}
	select {
	case <-ran:
	default:
		t.Fatal("the group after a wedged step never ran")
	}
	snap := track.snapshot()
	if snap.Items[0].State != stageFailed || snap.Items[0].Detail != errShutdownTimeout.Error() {
		t.Fatalf("wedged row = %+v, want failed and timed out", snap.Items[0])
	}
	if snap.Items[1].State != stageDone {
		t.Fatalf("row after = %+v", snap.Items[1])
	}
}

// The overall deadline caps every wait, whatever a step's own timeout says.
func TestShutdownDeadlineCapsLongTimeouts(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	steps := []shutdownStep{{label: "slow", timeout: time.Minute, fn: func() (string, error) {
		<-block
		return "", nil
	}}}
	track := newStageTrack("Shutting down", []string{"slow"})
	started := time.Now()
	runShutdownStepsUntil(steps, track, nil, time.Now().Add(80*time.Millisecond))
	if took := time.Since(started); took > time.Second {
		t.Fatalf("the teardown ran %v past its deadline", took)
	}
	if snap := track.snapshot(); snap.Items[0].State != stageFailed {
		t.Fatalf("slow row = %+v, want it cut off", snap.Items[0])
	}
}

// Steps of one group run side by side: two that each wait for the other to
// have started can only both finish if they overlap.
func TestShutdownGroupRunsSideBySide(t *testing.T) {
	a, b := make(chan struct{}), make(chan struct{})
	steps := []shutdownStep{
		{label: "a", timeout: time.Second, fn: func() (string, error) {
			close(a)
			<-b
			return "a", nil
		}},
		{label: "b", timeout: time.Second, fn: func() (string, error) {
			close(b)
			<-a
			return "b", nil
		}},
	}
	track := newStageTrack("Shutting down", []string{"a", "b"})
	runShutdownStepsUntil(steps, track, nil, time.Now().Add(2*time.Second))
	snap := track.snapshot()
	if snap.Items[0].State != stageDone || snap.Items[1].State != stageDone {
		t.Fatalf("rows = %+v, want both done", snap.Items)
	}
}

// A panicking step is a failed row, not a crash on the way out.
func TestShutdownStepPanicIsAFailedRow(t *testing.T) {
	steps := []shutdownStep{{label: "boom", inline: true, fn: func() (string, error) {
		panic("broken")
	}}}
	track := newStageTrack("Shutting down", []string{"boom"})
	runShutdownStepsUntil(steps, track, nil, time.Now().Add(time.Second))
	if snap := track.snapshot(); snap.Items[0].State != stageFailed {
		t.Fatalf("panicking row = %+v", snap.Items[0])
	}
}

// A feature can register its own clean-up for the services row.
func TestBootOnShutdownHooksRunWithTheServices(t *testing.T) {
	// Leaving a (not running) Watch Party flushes its history file: keep it
	// out of the repository.
	t.Chdir(t.TempDir())
	bootShutdownHooks.mu.Lock()
	prev := bootShutdownHooks.fns
	bootShutdownHooks.fns = nil
	bootShutdownHooks.mu.Unlock()
	t.Cleanup(func() {
		bootShutdownHooks.mu.Lock()
		bootShutdownHooks.fns = prev
		bootShutdownHooks.mu.Unlock()
	})
	called := 0
	bootOnShutdown(func() { called++ })
	bootOnShutdown(func() { panic("a broken hook must not stop the rest") })
	bootOnShutdown(func() { called++ })
	bootOnShutdown(nil)
	if _, err := shutdownStopServices(); err != nil {
		t.Fatalf("services step: %v", err)
	}
	if called != 2 {
		t.Fatalf("%d hooks ran, want 2", called)
	}
}

// The panel must actually paint the card and its rows.
func TestShutdownPanelRendersTheTeardown(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	defer destroyTestRenderer(renderer)

	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()

	snap := stageSnapshot{
		Title: "Powering down", Total: 3, Done: 1, Active: 1,
		Current: "Saving your place",
		Items: []stageItem{
			{Label: "Stopping games and audio", State: stageDone, Detail: "nothing running"},
			{Label: "Saving your place", State: stageActive},
			{Label: "Restoring the CPU", State: stagePending},
		},
	}
	renderShutdownFrame(renderer, probeBootConfig(), snap, 300)

	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
	}
	if got := f.countNonBG(); got < 200 {
		t.Fatalf("teardown panel drew almost nothing (%d lit samples)", got)
	}
}

// requestAppExit must arm the exit flag the main loop watches.
func TestRequestAppExitSetsFlag(t *testing.T) {
	prev := exitRequested.Load()
	defer exitRequested.Store(prev)
	exitRequested.Store(false)
	requestAppExit()
	if !exitRequested.Load() {
		t.Error("requestAppExit did not set exitRequested")
	}
}
