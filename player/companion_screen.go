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

	// Streaming (companion_cast.go). jpeg is the newest encoded frame for the
	// MJPEG feed; seq lets a viewer skip frames it already sent without
	// re-sending the same picture.
	jpeg    []byte
	jpegSeq uint64
	jpegAt  time.Time
	// raw is the newest captured framebuffer, kept so a viewer that connects
	// between captures gets a frame immediately instead of a timeout.
	raw      []byte
	rawW     int32
	rawH     int32
	rawPitch int
	rawAt    time.Time

	streamWaiters []chan struct{} // one per waiting viewer
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

// companionStreamDue reports whether the capture hook should run for the cast:
// there are viewers and the last encoded frame is older than the frame
// interval, so a 60 fps loop still encodes ten frames a second.
func companionStreamDue() bool {
	if castViewerCount() == 0 || !castIsEnabled() {
		return false
	}
	companionShot.mu.Lock()
	defer companionShot.mu.Unlock()
	return time.Since(companionShot.jpegAt) >= castFrameInterval
}

// publishCompanionPixels stores the captured framebuffer and, while viewers are
// watching, the JPEG the stream sends. waiters receive the PNG exactly as
// before, so /api/screen keeps its behaviour.
func publishCompanionPixels(pixels []byte, w, h int32, pitch int, png, jpeg []byte) {
	companionShot.mu.Lock()
	companionShot.raw = pixels
	companionShot.rawW, companionShot.rawH, companionShot.rawPitch = w, h, pitch
	companionShot.rawAt = time.Now()
	if len(png) > 0 {
		companionShot.png = png
		companionShot.at = companionShot.rawAt
	}
	if len(jpeg) > 0 {
		companionShot.jpeg = jpeg
		companionShot.jpegSeq++
		companionShot.jpegAt = companionShot.rawAt
	}
	// Only wake the consumers this capture actually served. A screenshot
	// request that arrived while a JPEG was being encoded keeps waiting for a
	// frame with its PNG instead of getting an empty answer.
	var waiters, streamWaiters []chan struct{}
	if len(png) > 0 {
		waiters = companionShot.waiters
		companionShot.waiters = nil
	}
	if len(jpeg) > 0 {
		streamWaiters = companionShot.streamWaiters
		companionShot.streamWaiters = nil
	}
	companionShot.mu.Unlock()
	for _, w := range waiters {
		close(w)
	}
	for _, w := range streamWaiters {
		close(w)
	}
}

// requestCompanionStreamFrame returns the newest JPEG newer than after. When
// nothing newer is ready it waits for the render loop, but never longer than
// timeout, so a viewer with a stalled device reconnects instead of hanging.
func requestCompanionStreamFrame(after uint64, timeout time.Duration) ([]byte, uint64, bool) {
	companionShot.mu.Lock()
	if len(companionShot.jpeg) > 0 && companionShot.jpegSeq > after &&
		time.Since(companionShot.jpegAt) < 2*time.Second {
		frame, seq := companionShot.jpeg, companionShot.jpegSeq
		companionShot.mu.Unlock()
		return frame, seq, true
	}
	wait := make(chan struct{})
	companionShot.streamWaiters = append(companionShot.streamWaiters, wait)
	companionShot.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-wait:
	case <-timer.C:
		removeCompanionStreamWaiter(wait)
	}

	companionShot.mu.Lock()
	defer companionShot.mu.Unlock()
	if len(companionShot.jpeg) == 0 || companionShot.jpegSeq <= after {
		return nil, after, false
	}
	return companionShot.jpeg, companionShot.jpegSeq, true
}

// removeCompanionStreamWaiter drops a timed-out viewer so a later capture does
// not close a channel nobody reads.
func removeCompanionStreamWaiter(wait chan struct{}) {
	companionShot.mu.Lock()
	defer companionShot.mu.Unlock()
	for i, w := range companionShot.streamWaiters {
		if w == wait {
			companionShot.streamWaiters = append(companionShot.streamWaiters[:i], companionShot.streamWaiters[i+1:]...)
			return
		}
	}
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
	if renderer == nil {
		return
	}
	// A screenshot request wants a PNG; a cast viewer wants a JPEG at the frame
	// interval. Either one is enough reason to read the framebuffer.
	wantPNG := companionScreenPending()
	wantJPEG := companionStreamDue()
	if !wantPNG && !wantJPEG {
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

	var pngFrame, jpegFrame []byte
	if wantPNG {
		if encoded, err := encodeScreenshotPNG(pixels, w, h, pitch); err == nil {
			pngFrame = encoded
		}
	}
	if wantJPEG {
		if encoded, err := encodeCompanionJPEG(pixels, w, h, pitch); err == nil {
			jpegFrame = encoded
		}
	}
	if len(pngFrame) == 0 && len(jpegFrame) == 0 {
		return
	}
	publishCompanionPixels(pixels, w, h, pitch, pngFrame, jpegFrame)
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
	img, err := companionFrameImage(pixels, w, h, pitch)
	if err != nil {
		return nil, err
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
