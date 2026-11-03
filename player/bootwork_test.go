package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// resetBootWork empties the queue between tests and leaves it empty afterwards,
// so a failing test cannot leak a step into the next one.
func resetBootWork(t *testing.T) {
	t.Helper()
	bootWork.mu.Lock()
	bootWork.pending = nil
	bootWork.mu.Unlock()
	t.Cleanup(func() {
		bootWork.mu.Lock()
		bootWork.pending = nil
		bootWork.mu.Unlock()
	})
}

func TestBootWorkRunsInOrderExactlyOnce(t *testing.T) {
	resetBootWork(t)

	var order []string
	bootEnqueue("first", func() { order = append(order, "first") })
	bootEnqueue("second", func() { order = append(order, "second") })
	bootEnqueue("third", func() { order = append(order, "third") })
	bootEnqueue("", nil) // ignored, must not leave a hole

	if !bootWorkPending() {
		t.Fatal("queue reported empty after enqueue")
	}
	bootWorkDrain()

	if got := len(order); got != 3 {
		t.Fatalf("ran %d steps, want 3 (%v)", got, order)
	}
	if order[0] != "first" || order[1] != "second" || order[2] != "third" {
		t.Fatalf("order = %v, want first,second,third", order)
	}
	if bootWorkPending() {
		t.Fatal("queue still has steps after a drain")
	}

	bootWorkDrain()
	if len(order) != 3 {
		t.Fatalf("second drain re-ran steps: %v", order)
	}
}

// A tick always makes progress: a step that costs more than the whole budget
// must not stall the queue, or the splash would keep redrawing while the app
// waits for the next frame's budget.
func TestBootWorkTickAlwaysRunsOneStep(t *testing.T) {
	resetBootWork(t)

	var ran int32
	bootEnqueue("slow", func() {
		atomic.AddInt32(&ran, 1)
		time.Sleep(5 * time.Millisecond)
	})
	bootEnqueue("next", func() { atomic.AddInt32(&ran, 1) })

	bootWorkTick(0)

	if got := atomic.LoadInt32(&ran); got != 1 {
		t.Fatalf("tick with a spent budget ran %d steps, want 1", got)
	}
	if !bootWorkPending() {
		t.Fatal("one tick drained the whole queue")
	}

	bootWorkDrain()
	if got := atomic.LoadInt32(&ran); got != 2 {
		t.Fatalf("drain left steps unrun: %d of 2", got)
	}
}

// The tick is what runs during the animation; with a real budget it should get
// through several cheap steps rather than one per frame.
func TestBootWorkTickSpendsItsBudgetOnSeveralSteps(t *testing.T) {
	resetBootWork(t)

	var ran int32
	for i := 0; i < 4; i++ {
		bootEnqueue("quick", func() { atomic.AddInt32(&ran, 1) })
	}

	bootWorkTick(8 * time.Millisecond)

	if got := atomic.LoadInt32(&ran); got < 2 {
		t.Fatalf("tick ran %d cheap steps, want at least 2", got)
	}
}

// A warm start draws no splash at all, so the drain has to do all of the work.
func TestBootWorkDrainCoversASkippedSplash(t *testing.T) {
	resetBootWork(t)

	var ran int32
	bootEnqueue("placeholder-texture", func() { atomic.AddInt32(&ran, 1) })
	bootEnqueue("focus-graph", func() { atomic.AddInt32(&ran, 1) })

	bootWorkDrain()

	if got := atomic.LoadInt32(&ran); got != 2 {
		t.Fatalf("drain after a skipped splash ran %d steps, want 2", got)
	}
}

// The cache warm is shared: it must finish, leave a usable handle behind, and
// be a no-op on a second call rather than opening the file again.
func TestBootWarmCacheOpensTheSharedHandle(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Cleanup(closeSharedCache)

	bootWarmCacheInBackground()

	done := make(chan struct{})
	go func() { bootWaitForWarmCache(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cache warm did not finish")
	}

	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		t.Fatalf("shared cache after warm: %v", err)
	}
	if db == nil {
		t.Fatal("warm left no shared handle behind")
	}

	// A second call must not start another open, and must not block.
	bootWarmCacheInBackground()
	bootWaitForWarmCache()
}
