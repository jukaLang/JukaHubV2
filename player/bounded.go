package main

// Bounded concurrency.
//
// The thumbnail prefetcher has its own worker pool, but every other fan-out —
// chat avatars, companion frames, feed artwork — could start as many goroutines
// as there were items. On a 1 GB device that is a memory ceiling discovered the
// hard way: a busy chat with forty images opened forty decodes at once, each
// holding a full RGBA buffer, and the kernel OOM-killed the shell mid-scroll.
//
// A semaphore is the smallest thing that fixes it: a weighted channel that
// caps how many callers run a section at once. It is generic over weight so a
// caller that knows a job is expensive can take two permits, while the common
// case takes one.

import (
	"context"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"bytes"
	"sync"
)

// semaphore is a counting semaphore with a context-aware Acquire. The zero
// value is unusable; construct with newSemaphore. Acquire blocks until a
// permit is available or ctx is done.
type semaphore struct {
	slots chan struct{}
}

// newSemaphore returns a semaphore allowing n concurrent holders.
func newSemaphore(n int) *semaphore {
	if n < 1 {
		n = 1
	}
	return &semaphore{slots: make(chan struct{}, n)}
}

// Acquire takes weight permits, blocking until they are available or ctx ends.
// It reports false (and takes nothing) if ctx was already done.
func (s *semaphore) Acquire(ctx context.Context, weight int) bool {
	if s == nil {
		return true
	}
	if weight < 1 {
		weight = 1
	}
	if weight > cap(s.slots) {
		weight = cap(s.slots)
	}
	for i := 0; i < weight; i++ {
		select {
		case s.slots <- struct{}{}:
		case <-ctx.Done():
			// Release whatever this call already took so a cancelled caller
			// does not leak permits.
			for j := 0; j < i; j++ {
				<-s.slots
			}
			return false
		}
	}
	return true
}

// Release returns weight permits. Calling it without a matching successful
// Acquire is a programming error and will panic on the channel receive — that
// is deliberate, a silent permit leak is worse.
func (s *semaphore) Release(weight int) {
	if s == nil {
		return
	}
	if weight < 1 {
		weight = 1
	}
	if weight > cap(s.slots) {
		weight = cap(s.slots)
	}
	for i := 0; i < weight; i++ {
		<-s.slots
	}
}

// TryAcquire takes permits only if they are free right now, without blocking.
func (s *semaphore) TryAcquire(weight int) bool {
	if s == nil {
		return true
	}
	if weight < 1 {
		weight = 1
	}
	if weight > cap(s.slots) {
		weight = cap(s.slots)
	}
	for i := 0; i < weight; i++ {
		select {
		case s.slots <- struct{}{}:
		default:
			for j := 0; j < i; j++ {
				<-s.slots
			}
			return false
		}
	}
	return true
}

// imageDecodeJobs is how many images may be decoded at once. Two keeps a grid
// or a busy chat filling progressively without starving the render loop of
// memory; desktop decodes get more headroom.
func imageDecodeJobs() int {
	if IsTrimuiDevice() || P().IsHandheld() {
		return 2
	}
	return 4
}

var imageDecodeSem = newSemaphore(imageDecodeJobs())

// imageDecodeSemReset rebuilds the decode semaphore at the current device
// profile. Tests use it to install a known capacity.
func imageDecodeSemReset(n int) { imageDecodeSem = newSemaphore(n) }

// decodeImageRGBA decodes image bytes into an *image.RGBA under the shared
// decode semaphore, so concurrent decodes are capped. The returned image owns
// its pixels; the caller frees it by dropping the reference (GC handles it).
//
// The conversion from the decoded interface to a dense RGBA happens here rather
// than at each call site, because the per-pixel Set loop is the expensive part
// and it must happen while the permit is held — that is the whole point of the
// cap.
func decodeImageRGBA(ctx context.Context, data []byte) (*image.RGBA, error) {
	if !imageDecodeSem.Acquire(ctx, 1) {
		return nil, ctx.Err()
	}
	defer imageDecodeSem.Release(1)

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba, nil
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	return rgba, nil
}

// runBounded runs fn under a semaphore of n concurrent slots. It is the simple
// form for a one-shot fan-out that has no context to thread.
func runBounded(n int, wg *sync.WaitGroup, fn func()) {
	s := newSemaphore(n)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Acquire(context.Background(), 1)
		defer s.Release(1)
		fn()
	}()
}
