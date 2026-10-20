package main

// Tests for the companion screen view: the encoder (colour order, pitch, error
// cases), the request/timeout plumbing, and the HTTP handler's contract.

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// resetCompanionShot clears the shared capture state between tests.
func resetCompanionShot(t *testing.T) {
	t.Helper()
	companionShot.mu.Lock()
	prev := companionShot.png
	prevAt := companionShot.at
	companionShot.png, companionShot.at, companionShot.waiters = nil, time.Time{}, nil
	companionShot.mu.Unlock()
	t.Cleanup(func() {
		companionShot.mu.Lock()
		companionShot.png, companionShot.at, companionShot.waiters = prev, prevAt, nil
		companionShot.mu.Unlock()
	})
}

func TestEncodeScreenshotPNGSwapsChannels(t *testing.T) {
	const w, h = 2, 2
	// A pitch wider than the row proves the encoder honours stride padding
	// instead of reading the next row's pixels as this one's.
	const pitch = w*4 + 8
	pixels := make([]byte, pitch*h)
	colors := [][3]uint8{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {10, 20, 30}}
	for i, c := range colors {
		px := companionARGBPixel(c[0], c[1], c[2])
		row, col := i/w, i%w
		copy(pixels[row*pitch+col*4:], px[:])
	}

	encoded, err := encodeScreenshotPNG(pixels, w, h, pitch)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("decoded %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	for i, want := range colors {
		r, g, b, a := img.At(i%w, i/w).RGBA()
		got := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
		if got != want {
			t.Errorf("pixel %d = %v, want %v (argb byte order not swapped?)", i, got, want)
		}
		if a>>8 != 255 {
			t.Errorf("pixel %d alpha = %d, want opaque", i, a>>8)
		}
	}
}

func TestEncodeScreenshotPNGRejectsBadInput(t *testing.T) {
	if _, err := encodeScreenshotPNG(nil, 0, 10, 40); err == nil {
		t.Error("zero width must be rejected")
	}
	if _, err := encodeScreenshotPNG(make([]byte, 40), 10, 1, 20); err == nil {
		t.Error("a pitch smaller than the row must be rejected")
	}
	if _, err := encodeScreenshotPNG(make([]byte, 8), 10, 10, 40); err == nil {
		t.Error("a truncated buffer must be rejected")
	}
}

func TestRequestCompanionScreenFallsBackToLastFrame(t *testing.T) {
	resetCompanionShot(t)

	// Nothing captured yet: the request gives up rather than hanging.
	if _, err := requestCompanionScreen(20 * time.Millisecond); err == nil {
		t.Fatal("a request with no frame must time out")
	}

	// An old frame is still better than an error on a busy device.
	frame := []byte("png-bytes")
	companionShot.mu.Lock()
	companionShot.png = frame
	companionShot.at = time.Now().Add(-time.Minute)
	companionShot.mu.Unlock()
	got, err := requestCompanionScreen(20 * time.Millisecond)
	if err != nil || !bytes.Equal(got, frame) {
		t.Fatalf("stale fallback = %q, %v", got, err)
	}

	// A fresh frame is served without asking the render thread again.
	companionShot.mu.Lock()
	companionShot.png = []byte("fresh")
	companionShot.at = time.Now()
	companionShot.mu.Unlock()
	if got, err := requestCompanionScreen(20 * time.Millisecond); err != nil || string(got) != "fresh" {
		t.Fatalf("fresh frame = %q, %v", got, err)
	}
}

// The companion page's auto-refresh can overlap a manual refresh. Both requests
// must be woken by one capture instead of one of them waiting out its timeout.
func TestCompanionScreenWakesEveryWaiter(t *testing.T) {
	resetCompanionShot(t)

	results := make(chan []byte, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			frame, err := requestCompanionScreen(time.Second)
			if err != nil {
				errs <- err
				return
			}
			results <- frame
		}()
	}

	deadline := time.Now().Add(time.Second)
	for !companionScreenPending() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !companionScreenPending() {
		t.Fatal("no request registered a capture waiter")
	}

	publishCompanionFrame([]byte("shared-frame"))
	for i := 0; i < 2; i++ {
		select {
		case frame := <-results:
			if string(frame) != "shared-frame" {
				t.Fatalf("waiter got %q", frame)
			}
		case err := <-errs:
			t.Fatalf("waiter failed: %v", err)
		case <-time.After(time.Second):
			t.Fatal("a waiter was not woken by the capture")
		}
	}
}

func TestCompanionScreenHandler(t *testing.T) {
	resetCompanionShot(t)
	companionShot.mu.Lock()
	companionShot.png = []byte("frame")
	companionShot.at = time.Now()
	companionShot.mu.Unlock()

	server := &companionServer{}
	rec := httptest.NewRecorder()
	server.handleScreen(rec, httptest.NewRequest(http.MethodGet, "/api/screen", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/screen = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the screen must not be cached")
	}
	if rec.Body.String() != "frame" {
		t.Errorf("body = %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.handleScreen(rec, httptest.NewRequest(http.MethodPost, "/api/screen", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/screen = %d, want 405", rec.Code)
	}
}
