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
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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

// imageDecodeMaxPixels is the largest image decodeImageRGBA expands: 16 MP on
// the handheld (64 MB as RGBA), more on a desktop.
func imageDecodeMaxPixels() int64 {
	if IsTrimuiDevice() || P().IsHandheld() {
		return 16 << 20
	}
	return 64 << 20
}

// imageDecodeSemReset rebuilds the decode semaphore at the current device
// profile. Tests use it to install a known capacity.
func imageDecodeSemReset(n int) { imageDecodeSem = newSemaphore(n) }

// decodeImageRGBA decodes image bytes into an *image.RGBA under the shared
// decode semaphore, so concurrent decodes are capped. The returned image owns
// its pixels; the caller frees it by dropping the reference (GC handles it).
//
// The conversion from the decoded interface to a dense RGBA happens here rather
// than at each call site, because it is the expensive part (one full-size copy)
// and it must happen while the permit is held — that is the whole point of the
// cap.
func decodeImageRGBA(ctx context.Context, data []byte) (*image.RGBA, error) {
	if !imageDecodeSem.Acquire(ctx, 1) {
		return nil, ctx.Err()
	}
	defer imageDecodeSem.Release(1)

	// The header is read first and an oversized image refused before a pixel
	// is allocated: a 1–2 MB PNG can declare 20000×20000, which decoded (and
	// converted, below) asked for over 3 GB — any image posted in a chat the
	// handheld showed could get it killed for running out of memory.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		if webpBytes(data) {
			if img, werr := webpDecodeBytes(data, 0, imageDecodeMaxPixels()); werr == nil {
				if rgba, ok := img.(*image.RGBA); ok {
					return rgba, nil
				}
				bounds := img.Bounds()
				rgba := image.NewRGBA(bounds)
				draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
				return rgba, nil
			}
		}
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > imageDecodeMaxPixels() {
		return nil, fmt.Errorf("image is %dx%d, over the %d-pixel decode limit", cfg.Width, cfg.Height, imageDecodeMaxPixels())
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba, nil
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	// draw.Src copies exactly what a Set of each At gives, through the
	// standard library's fast paths for a JPEG's YCbCr and a PNG's NRGBA: the
	// per-pixel interface calls took seconds for a phone photo on the A133P (a
	// tester's log, 2026-11-22: Chat's 4 MB picture held the screen for 5-9 s).
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
	return rgba, nil
}
