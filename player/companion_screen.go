package main

// companion_screen.go — live screen view for the companion web UI.
//
// The companion server already types, presses keys, opens links and accepts
// uploads from a phone. Seeing the screen is the piece that makes it usable for
// support: "the settings menu is blank" becomes a screenshot instead of a
// description.
//
// SDL is not thread-safe, so the HTTP goroutine never touches the renderer. It
// registers a request and waits; the render loop captures the framebuffer on its
// own thread, encodes it once, and closes the wait channel. Frames are reused
// for a short window so refreshing the page cannot turn into a capture every
// frame.

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"net/http"
	"sync"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// companionShotReuse is how long a captured frame may be served again.
	companionShotReuse = 300 * time.Millisecond
	// companionShotWait bounds how long an HTTP request blocks for a frame.
	companionShotWait = 2 * time.Second
)

// handleScreen serves the current screen as PNG. GET only: a capture costs a
// framebuffer read on the device, so it must not be triggered by a form post.
func (s *companionServer) handleScreen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	frame, err := requestCompanionScreen(companionShotWait)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(frame)
}

type companionShotState struct {
	mu      sync.Mutex
	waiters []chan struct{} // one per waiting request, all closed by the capture
	png     []byte
	at      time.Time
}

var companionShot companionShotState

// requestCompanionScreen returns the newest frame as PNG, asking the render
// loop for a fresh one when the cached copy is old. It never blocks longer than
// companionShotWait and falls back to the last frame on a timeout, which keeps
// a paused or busy device from hanging the browser.
func requestCompanionScreen(timeout time.Duration) ([]byte, error) {
	companionShot.mu.Lock()
	if len(companionShot.png) > 0 && time.Since(companionShot.at) < companionShotReuse {
		frame := companionShot.png
		companionShot.mu.Unlock()
		return frame, nil
	}
	// Every request gets its own channel: the page's auto-refresh can overlap a
	// manual refresh, and one shared slot would leave the earlier request
	// waiting out its whole timeout.
	wait := make(chan struct{})
	companionShot.waiters = append(companionShot.waiters, wait)
	companionShot.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-wait:
	case <-timer.C:
		removeCompanionWaiter(wait)
		companionShot.mu.Lock()
		stale := companionShot.png
		companionShot.mu.Unlock()
		if len(stale) > 0 {
			return stale, nil
		}
		return nil, errors.New("the screen did not render in time")
	}

	companionShot.mu.Lock()
	defer companionShot.mu.Unlock()
	if len(companionShot.png) == 0 {
		return nil, errors.New("no screenshot captured")
	}
	return companionShot.png, nil
}

// companionScreenPending reports whether the render loop has a waiter, so the
// per-frame hook costs one mutex acquisition when nobody is looking.
func companionScreenPending() bool {
	companionShot.mu.Lock()
	defer companionShot.mu.Unlock()
	return len(companionShot.waiters) > 0
}

// removeCompanionWaiter drops a timed-out request so a later capture does not
// close a channel nobody is reading.
func removeCompanionWaiter(wait chan struct{}) {
	companionShot.mu.Lock()
	defer companionShot.mu.Unlock()
	for i, w := range companionShot.waiters {
		if w == wait {
			companionShot.waiters = append(companionShot.waiters[:i], companionShot.waiters[i+1:]...)
			return
		}
	}
}

// publishCompanionFrame stores a captured frame and wakes every waiter.
func publishCompanionFrame(frame []byte) {
	companionShot.mu.Lock()
	companionShot.png = frame
	companionShot.at = time.Now()
	waiters := companionShot.waiters
	companionShot.waiters = nil
	companionShot.mu.Unlock()
	for _, w := range waiters {
		close(w)
	}
}

// captureCompanionScreen grabs the framebuffer for a waiting request. It must
// be called from the render thread, after the frame is drawn.
func captureCompanionScreen(renderer *sdl.Renderer) {
	if renderer == nil || !companionScreenPending() {
		return
	}
	w, h, err := renderer.GetOutputSize()
	if err != nil || w <= 0 || h <= 0 {
		return
	}
	surface, err := sdl.CreateRGBSurfaceWithFormat(0, w, h, 32, sdl.PIXELFORMAT_ARGB8888)
	if err != nil {
		return
	}
	defer surface.Free()
	if err := surface.Lock(); err != nil {
		return
	}
	readErr := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888,
		unsafe.Pointer(&surface.Pixels()[0]), int(surface.Pitch))
	if readErr != nil {
		surface.Unlock()
		return
	}
	// Copy the rows out before unlocking: the surface is freed on return.
	pixels := make([]byte, int(surface.Pitch)*int(h))
	copy(pixels, surface.Pixels())
	pitch := int(surface.Pitch)
	surface.Unlock()

	frame, err := encodeScreenshotPNG(pixels, w, h, pitch)
	if err != nil {
		return
	}

	publishCompanionFrame(frame)
}

// encodeScreenshotPNG turns SDL's ARGB8888 rows into a PNG. The in-memory byte
// order is B,G,R,A (little-endian), so the channels are swapped here rather
// than handing the browser a colour-shifted image.
func encodeScreenshotPNG(pixels []byte, w, h int32, pitch int) ([]byte, error) {
	if w <= 0 || h <= 0 {
		return nil, errors.New("screenshot has no size")
	}
	if pitch < int(w)*4 {
		return nil, errors.New("screenshot pitch is smaller than its width")
	}
	if len(pixels) < pitch*int(h) {
		return nil, errors.New("screenshot buffer is truncated")
	}
	img := image.NewNRGBA(image.Rect(0, 0, int(w), int(h)))
	for y := 0; y < int(h); y++ {
		row := pixels[y*pitch : y*pitch+int(w)*4]
		dst := img.Pix[y*img.Stride : y*img.Stride+int(w)*4]
		for x := 0; x < int(w); x++ {
			b, g, r, a := row[x*4], row[x*4+1], row[x*4+2], row[x*4+3]
			if a == 0 {
				a = 255 // an opaque frame, even if the surface left alpha clear
			}
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = r, g, b, a
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// companionARGBPixel builds one ARGB8888 pixel in SDL's byte order (B,G,R,A),
// so the encoder test can state colours instead of channel offsets.
func companionARGBPixel(r, g, b uint8) [4]byte { return [4]byte{b, g, r, 0xff} }
