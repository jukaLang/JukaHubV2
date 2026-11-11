package main

// companion_screen.go — the handheld's picture, for the phone companion's
// screenshot and for Screen Share.
//
// The companion server already types, presses keys, opens links and accepts
// uploads from a phone. Seeing the screen is the piece that makes it usable for
// support: "the settings menu is blank" becomes a screenshot instead of a
// description.
//
// SDL is not thread-safe, so no HTTP goroutine touches the renderer. A request
// registers and waits; at the end of a frame the render loop copies the
// finished picture (captureCompanionScreen) and castcap.go's encoder goroutine
// turns it into the PNG a phone asked for, or the JPEG the share streams, then
// closes the waiters' channels. Frames are reused for a short window so a page
// refreshing in a loop cannot turn into a capture every frame.

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"net/http"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// companionShotReuse is how long a captured frame may be served again.
	companionShotReuse = 300 * time.Millisecond
	// companionShotWait bounds how long an HTTP request blocks for a frame.
	companionShotWait = 2 * time.Second
	// companionFrameFresh is how old the share's newest frame may be and still
	// go straight to a viewer that just connected.
	companionFrameFresh = 2 * time.Second
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

	// Screen Share (companion_cast.go). jpeg is the newest encoded frame;
	// jpegSeq lets a viewer skip frames it already sent.
	jpeg    []byte
	jpegSeq uint64
	jpegAt  time.Time

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

// companionScreenPending reports whether a screenshot request waits, so the
// per-frame hook costs one mutex acquisition when nobody is looking.
func companionScreenPending() bool {
	companionShot.mu.Lock()
	defer companionShot.mu.Unlock()
	return len(companionShot.waiters) > 0
}

// publishCompanionPNG hands a finished screenshot to the requests waiting for
// it.
func publishCompanionPNG(frame []byte) {
	if len(frame) == 0 {
		return
	}
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

// publishCompanionJPEG makes frame the share's newest picture and wakes the
// streams waiting for it. w and h are its size, for the panel and /status. The
// frame is shared by every stream at once, so its bytes are never changed.
func publishCompanionJPEG(frame []byte, w, h int) {
	if len(frame) == 0 {
		return
	}
	companionShot.mu.Lock()
	companionShot.jpeg = frame
	companionShot.jpegSeq++
	companionShot.jpegAt = time.Now()
	waiters := companionShot.streamWaiters
	companionShot.streamWaiters = nil
	companionShot.mu.Unlock()
	for _, wait := range waiters {
		close(wait)
	}
	castNoteFrame(len(frame), w, h)
}

// companionNewestJPEG is the share's newest frame, its sequence number and
// when it was made.
func companionNewestJPEG() ([]byte, uint64, time.Time) {
	companionShot.mu.Lock()
	defer companionShot.mu.Unlock()
	return companionShot.jpeg, companionShot.jpegSeq, companionShot.jpegAt
}

// requestCompanionStreamFrame returns the newest JPEG newer than after. When
// nothing newer is ready it waits for the next one, but never longer than
// timeout, so a viewer with a stalled device reconnects instead of hanging.
func requestCompanionStreamFrame(after uint64, timeout time.Duration) ([]byte, uint64, bool) {
	companionShot.mu.Lock()
	if len(companionShot.jpeg) > 0 && companionShot.jpegSeq > after &&
		time.Since(companionShot.jpegAt) < companionFrameFresh {
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

// captureCompanionScreen is the render loop's end-of-frame hook, called after
// every overlay is drawn and before the virtual cursor and Present. It copies
// the frame for a waiting screenshot or the share (castcap.go: no encoding
// here, the encoder goroutine does that), keeps the share's settings current,
// and then draws the LIVE pill — after the copy, so viewers never see it.
func captureCompanionScreen(renderer *sdl.Renderer) {
	if renderer == nil {
		return
	}
	castTick()
	castCaptureFrame(renderer, time.Now())
	castDrawLiveIndicator(renderer)
}

// encodeScreenshotPNG turns SDL's ARGB8888 rows (screenshot mode's capture)
// into a PNG. The in-memory byte order is B, G, R, A (little-endian), so the
// channels are swapped here rather than handing over a colour-shifted image,
// and every pixel is made opaque: a picture of the screen has no holes.
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
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for y := 0; y < int(h); y++ {
		row := pixels[y*pitch : y*pitch+int(w)*4]
		dst := img.Pix[y*img.Stride : y*img.Stride+int(w)*4]
		for x := 0; x < len(row); x += 4 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = row[x+2], row[x+1], row[x], 255
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
