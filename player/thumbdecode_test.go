package main

// thumbdecode_test.go — the render-path decode budget from the
// modernization checklist ("decode budget of 2/frame"). The gate is a
// counter over frame numbers, so every behaviour is a pure function of
// plain values and needs no renderer, no network and no SDL at all.

import (
	"sync"
	"testing"
)

func TestThumbnailDecodeBudgetForDevice(t *testing.T) {
	if got := thumbnailDecodeBudgetFor(true); got != 2 {
		t.Fatalf("handheld budget = %d, want 2 (the checklist number)", got)
	}
	if got := thumbnailDecodeBudgetFor(false); got <= thumbnailDecodeBudgetFor(true) {
		t.Fatalf("desktop budget = %d, want more than the handheld's %d", got, thumbnailDecodeBudgetFor(true))
	}
}

func TestAllowThumbnailDecodeResetsPerFrame(t *testing.T) {
	const budget = 2
	if !allowThumbnailDecode(1, budget) {
		t.Fatal("first load of frame 1 declined; a fresh frame opens a fresh budget")
	}
	if !allowThumbnailDecode(1, budget) {
		t.Fatal("second load of frame 1 declined while under budget")
	}
	if allowThumbnailDecode(1, budget) {
		t.Fatal("third load of frame 1 allowed; the frame must stop at the budget")
	}
	if allowThumbnailDecode(1, budget) {
		t.Fatal("fourth load of frame 1 allowed after the frame's budget was spent more than once in a row")
	}
	if !allowThumbnailDecode(2, budget) {
		t.Fatal("first load of frame 2 declined; a new frame number must reset the tally")
	}
}

func TestAllowThumbnailDecodeZeroBudgetDeclines(t *testing.T) {
	if allowThumbnailDecode(901, 0) {
		t.Fatal("a zero budget must decline everything, even on a fresh frame")
	}
}

func TestAllowThumbnailDecodeSurvivesConcurrentCallers(t *testing.T) {
	// Every render-row call lands here from the render thread; the mutex only
	// keeps the gate honest about being package state. Drive it hard from
	// several goroutines to confirm it stays consistent, not to model the
	// device loop.
	const budget = 4
	const frame = uint64(777)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if allowThumbnailDecode(frame, budget) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != budget {
		t.Fatalf("frame %d allowed %d decodes, want the %d budget", frame, allowed, budget)
	}
	// The next frame starts over, exactly once.
	if !allowThumbnailDecode(frame+1, budget) {
		t.Fatal("next frame declined after the previous frame spent its budget")
	}
}
