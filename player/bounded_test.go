package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pngHeaderClaiming returns just a PNG signature and an IHDR chunk declaring
// w×h RGBA pixels: what a decompression bomb looks like before its data.
func pngHeaderClaiming(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(ihdr)))
	buf.WriteString("IHDR")
	buf.Write(ihdr)
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	return buf.Bytes()
}

// An image whose header claims more pixels than the decode limit is refused
// before anything is allocated. A tiny PNG declaring 30000×30000 used to be
// expanded (twice) and get the handheld killed for running out of memory.
func TestDecodeImageRGBARefusesOversizedImages(t *testing.T) {
	_, err := decodeImageRGBA(context.Background(), pngHeaderClaiming(30000, 30000))
	if err == nil {
		t.Fatal("a 30000x30000 image was decoded")
	}
	// The header-only fixture would fail a full decode anyway (it has no
	// image data); the refusal must come from the size check, up front.
	if !strings.Contains(err.Error(), "decode limit") {
		t.Fatalf("error = %v, want the pixel-limit refusal", err)
	}
}

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
