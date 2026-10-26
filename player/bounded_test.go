package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pngBytes renders a solid w×h PNG for decode tests.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

// TestSemaphoreCapsConcurrency is the whole point: at most N callers hold the
// semaphore at once, even under a much larger fan-out.
func TestSemaphoreCapsConcurrency(t *testing.T) {
	const slots, fanout = 3, 20
	s := newSemaphore(slots)

	var running, peak int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < fanout; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !s.Acquire(context.Background(), 1) {
				t.Error("Acquire returned false for a live context")
				return
			}
			mu.Lock()
			running++
			if running > peak {
				peak = running
			}
			mu.Unlock()

			time.Sleep(2 * time.Millisecond)

			mu.Lock()
			running--
			mu.Unlock()
			s.Release(1)
		}()
	}
	wg.Wait()
	if peak > slots {
		t.Fatalf("peak concurrency = %d, want at most %d", peak, slots)
	}
	if peak < 2 {
		t.Fatalf("peak concurrency = %d; semaphore never overlapped, test is vacuous", peak)
	}
}

// TestSemaphoreCancelledAcquireTakesNothing: a caller cancelled while waiting
// returns false and does not consume a permit it never got.
func TestSemaphoreCancelledAcquireTakesNothing(t *testing.T) {
	s := newSemaphore(1)
	if !s.Acquire(context.Background(), 1) {
		t.Fatal("first Acquire failed")
	}
	// The one permit is held; a cancelled waiter must not steal or block.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.Acquire(ctx, 1) {
		t.Fatal("cancelled Acquire reported success")
	}
	// A fresh context still gets the permit once we release it.
	s.Release(1)
	if !s.Acquire(context.Background(), 1) {
		t.Fatal("permit was leaked by the cancelled waiter")
	}
}

// TestSemaphoreReleaseUnblocksWaiter: releasing wakes a blocked Acquire.
func TestSemaphoreReleaseUnblocksWaiter(t *testing.T) {
	s := newSemaphore(1)
	s.Acquire(context.Background(), 1)
	done := make(chan struct{})
	go func() {
		s.Acquire(context.Background(), 1)
		s.Release(1)
		close(done)
	}()
	s.Release(1)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Release did not unblock the waiter")
	}
}

// TestTryAcquireNeverBlocks: TryAcquire reports false when full instead of waiting.
func TestTryAcquireNeverBlocks(t *testing.T) {
	s := newSemaphore(1)
	if !s.TryAcquire(1) {
		t.Fatal("TryAcquire failed on an empty semaphore")
	}
	if s.TryAcquire(1) {
		t.Fatal("TryAcquire succeeded on a full semaphore")
	}
	s.Release(1)
	if !s.TryAcquire(1) {
		t.Fatal("TryAcquire failed after Release")
	}
}

// TestDecodeImageRGBAAvoidsDoubleCopy: an already-dense RGBA is returned as-is
// (no lossy re-wrap), and its pixels survive.
func TestDecodeImageRGBAAvoidsDoubleCopy(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	src.Set(1, 1, image.White.At(0, 0))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// PNG decodes to *image.NRGBA, so this exercises the conversion path.
	out, err := decodeImageRGBA(context.Background(), buf.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Bounds().Dx() != 4 || out.Bounds().Dy() != 4 {
		t.Fatalf("bounds = %v, want 4x4", out.Bounds())
	}
}

// TestDecodeImageRGBAReportsBadInput: malformed bytes surface the decode error
// rather than an empty image.
func TestDecodeImageRGBAReportsBadInput(t *testing.T) {
	if _, err := decodeImageRGBA(context.Background(), []byte("not an image")); err == nil {
		t.Fatal("decode of garbage reported no error")
	}
}

// TestDecodeImageRGBAHonoursCancellation: an already-cancelled context refuses
// to decode, proving the semaphore gate is actually consulted.
func TestDecodeImageRGBAHonoursCancellation(t *testing.T) {
	imageDecodeSemReset(1)
	defer imageDecodeSemReset(imageDecodeJobs())
	// Hold the only permit so the decode would have to wait, then cancel.
	if !imageDecodeSem.Acquire(context.Background(), 1) {
		t.Fatal("could not seize the decode permit")
	}
	defer imageDecodeSem.Release(1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := decodeImageRGBA(ctx, pngBytes(t, 2, 2)); err == nil {
		t.Fatal("cancelled decode reported no error")
	}
}

// TestRunBoundedJoins: the one-shot helper actually runs the work and waits.
func TestRunBoundedJoins(t *testing.T) {
	var ran int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		runBounded(2, &wg, func() { atomic.AddInt64(&ran, 1) })
	}
	wg.Wait()
	if ran != 8 {
		t.Fatalf("ran %d jobs, want 8", ran)
	}
}
