package main

// companion_cast_test.go — the remote cast: page, MJPEG feed, PIN gate, viewer
// accounting and the JPEG encoder. No SDL is involved: frames are published
// through the same function the render thread uses.

import (
	"bytes"
	"context"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// castTestFrame builds a tiny ARGB frame and its JPEG, the way the render
// thread publishes them.
func castTestFrame(t *testing.T, w, h int32) ([]byte, []byte) {
	t.Helper()
	pitch := int(w) * 4
	pixels := make([]byte, pitch*int(h))
	for y := 0; y < int(h); y++ {
		for x := 0; x < int(w); x++ {
			// B,G,R,A in SDL's little-endian byte order.
			i := y*pitch + x*4
			pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = 30, 120, 220, 255
		}
	}
	frame, err := encodeCompanionJPEG(pixels, w, h, pitch)
	if err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return pixels, frame
}

func resetCastState(t *testing.T) {
	t.Helper()
	castSetEnabled(false)
	companionShot.mu.Lock()
	companionShot.jpeg = nil
	companionShot.jpegSeq = 0
	companionShot.jpegAt = time.Time{}
	companionShot.raw = nil
	companionShot.at = time.Time{}
	companionShot.png = nil
	companionShot.streamWaiters = nil
	companionShot.waiters = nil
	companionShot.mu.Unlock()
	t.Cleanup(func() {
		castSetEnabled(false)
	})
}

func TestCastWatchURL(t *testing.T) {
	got := castWatchURL("192.168.1.42", 8317, "123456")
	want := "http://192.168.1.42:8317/watch?p=123456"
	if got != want {
		t.Errorf("watch url = %q, want %q", got, want)
	}
	if castWatchURL("", 8317, "123456") != "" || castWatchURL("192.168.1.42", 0, "1") != "" {
		t.Error("an incomplete address should not produce a link")
	}
}

func TestCastWatchPageEmbedsTheStream(t *testing.T) {
	page := string(castWatchPage("123456"))
	// The page always points at the stream endpoint with the PIN attached.
	if !strings.Contains(page, "/stream?p=123456") {
		t.Errorf("the page does not embed the stream URL: %s", page)
	}
	// A PIN with URL syntax in it is escaped rather than written raw, so it
	// cannot break out of the src attribute.
	nasty := string(castWatchPage(`"><script>alert(1)</script>`))
	if strings.Contains(nasty, `src="/stream?p="`+"\n") || strings.Contains(nasty, `<script>alert(1)</script>`) {
		t.Error("a crafted PIN was written into the page unescaped")
	}
	if !strings.Contains(nasty, "%22%3E%3Cscript%3E") {
		t.Errorf("the PIN was not percent-encoded: %s", nasty)
	}
}

func TestCastJPEGFrames(t *testing.T) {
	pixels, frame := castTestFrame(t, 8, 4)
	if len(frame) < 100 {
		t.Fatalf("jpeg is only %d bytes", len(frame))
	}
	img, err := jpeg.Decode(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("decode jpeg: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 8 || b.Dy() != 4 {
		t.Errorf("decoded %dx%d, want 8x4", b.Dx(), b.Dy())
	}
	r, g, b, _ := img.At(2, 1).RGBA()
	if r>>8 < 180 || g>>8 < 90 || b>>8 > 60 {
		t.Errorf("pixel decoded as rgb(%d,%d,%d), want the orange test colour", r>>8, g>>8, b>>8)
	}

	// The shared conversion rejects malformed frames instead of panicking.
	if _, err := encodeCompanionJPEG(nil, 0, 4, 32); err == nil {
		t.Error("a zero-width frame was accepted")
	}
	if _, err := encodeCompanionJPEG(make([]byte, 16), 8, 4, 32); err == nil {
		t.Error("a truncated frame was accepted")
	}
	if _, err := companionFrameImage(pixels, 8, 4, 8); err == nil {
		t.Error("a pitch smaller than the width was accepted")
	}
}

func TestCastEndpointsRequireTheCastToBeOn(t *testing.T) {
	resetCastState(t)
	srv := newCompanionServer("123456", t.TempDir(), func(companionEvent) {})
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	for _, path := range []string{"/watch?p=123456", "/stream?p=123456"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s while off = %d, want 403", path, resp.StatusCode)
		}
	}

	castSetEnabled(true)
	for _, path := range []string{"/watch?p=000000", "/stream?p=000000"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with a wrong PIN = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestCastWatchPageServed(t *testing.T) {
	resetCastState(t)
	castSetEnabled(true)
	srv := newCompanionServer("123456", t.TempDir(), func(companionEvent) {})
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/watch?p=123456")
	if err != nil {
		t.Fatalf("GET /watch: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q", ct)
	}
	if !strings.Contains(string(body), "/stream?p=123456") {
		t.Error("the served page does not point at the stream")
	}
}

func TestCastStreamDeliversFrames(t *testing.T) {
	resetCastState(t)
	castSetEnabled(true)
	pixels, frame := castTestFrame(t, 54, 30)
	publishCompanionPixels(pixels, 54, 30, 54*4, nil, frame)

	srv := newCompanionServer("123456", t.TempDir(), func(companionEvent) {})
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/stream?p=123456", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "multipart/x-mixed-replace") {
		t.Fatalf("content type = %q", ct)
	}
	if got := castViewerCount(); got != 1 {
		t.Errorf("viewer count while streaming = %d, want 1", got)
	}

	// The first published frame arrives without waiting for another capture. An
	// MJPEG part ends where the next boundary starts, so the part is read by its
	// Content-Length instead of to EOF.
	reader := multipart.NewReader(resp.Body, castBoundary)
	part, err := reader.NextPart()
	if err != nil {
		t.Fatalf("next part: %v", err)
	}
	if got := part.Header.Get("Content-Length"); got != strconv.Itoa(len(frame)) {
		t.Errorf("part content-length = %q, want %d", got, len(frame))
	}
	payload := make([]byte, len(frame))
	if _, err := io.ReadFull(part, payload); err != nil {
		t.Fatalf("read part: %v", err)
	}
	if !bytes.HasPrefix(payload, []byte{0xFF, 0xD8}) {
		t.Errorf("part does not start with a JPEG SOI: % x", payload[:2])
	}
	img, err := jpeg.Decode(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("decode streamed jpeg: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 54 || b.Dy() != 30 {
		t.Errorf("streamed frame is %dx%d, want 54x30", b.Dx(), b.Dy())
	}

	// A second frame reaches the same viewer.
	_, next := castTestFrame(t, 54, 30)
	publishCompanionPixels(pixels, 54, 30, 54*4, nil, next)
	part2, err := reader.NextPart()
	if err != nil {
		t.Fatalf("second part: %v", err)
	}
	if _, err := io.CopyN(io.Discard, part2, int64(len(next))); err != nil {
		t.Fatalf("read second part: %v", err)
	}

	// Turning the cast off ends the stream instead of leaving the socket open.
	castSetEnabled(false)
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("the stream kept running after the cast was turned off")
		cancel()
	}
	if got := castViewerCount(); got != 0 {
		t.Errorf("viewer count after the stream ended = %d, want 0", got)
	}
}

func TestCastViewerLimit(t *testing.T) {
	resetCastState(t)
	if castAddViewer() {
		t.Fatal("a viewer was admitted while the cast was off")
	}
	castSetEnabled(true)
	for i := 0; i < castMaxViewers; i++ {
		if !castAddViewer() {
			t.Fatalf("viewer %d was refused", i+1)
		}
	}
	if castAddViewer() {
		t.Error("the stream admitted more viewers than the limit")
	}
	for i := 0; i < castMaxViewers; i++ {
		castRemoveViewer()
	}
	if castViewerCount() != 0 {
		t.Errorf("viewer count = %d, want 0", castViewerCount())
	}
	castRemoveViewer() // must not go negative
	if castViewerCount() != 0 {
		t.Errorf("viewer count went negative: %d", castViewerCount())
	}
}

func TestCastStatusText(t *testing.T) {
	resetCastState(t)
	if got := castStatusText(); got != "Off" {
		t.Errorf("off status = %q", got)
	}
	castSetEnabled(true)
	if got := castStatusText(); !strings.Contains(got, "waiting") {
		t.Errorf("idle status = %q", got)
	}
	if !castAddViewer() {
		t.Fatal("could not add a viewer")
	}
	defer castRemoveViewer()
	if got := castStatusText(); !strings.Contains(got, "1 viewer") {
		t.Errorf("one-viewer status = %q", got)
	}
}

// The stream frame request must hand out a fresh frame and report a timeout
// rather than blocking a handler forever.
func TestCastStreamFrameTimeout(t *testing.T) {
	resetCastState(t)
	if _, _, ok := requestCompanionStreamFrame(0, 20*time.Millisecond); ok {
		t.Error("a frame was returned with nothing published")
	}
	_, frame := castTestFrame(t, 4, 2)
	pixels, _ := castTestFrame(t, 4, 2)
	publishCompanionPixels(pixels, 4, 2, 16, nil, frame)
	got, seq, ok := requestCompanionStreamFrame(0, time.Second)
	if !ok || len(got) == 0 || seq == 0 {
		t.Fatalf("request after publish = (%d bytes, seq %d, %v)", len(got), seq, ok)
	}
	if _, _, ok := requestCompanionStreamFrame(seq, 20*time.Millisecond); ok {
		t.Error("the same frame was served twice for a newer-than request")
	}
}

// The seam that matters: the render thread's capture has to produce a real JPEG
// of the real framebuffer when a viewer is watching, without anyone asking for
// a screenshot.
func TestCastCaptureProducesStreamFrame(t *testing.T) {
	resetCastState(t)
	castSetEnabled(true)
	if !castAddViewer() {
		t.Fatal("could not attach a viewer")
	}
	defer castRemoveViewer()

	// newProbeFrame initialises SDL and the shared hidden window the renderer
	// probes use; without it there is no window to create a renderer on.
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	renderer.SetDrawColor(24, 96, 200, 255)
	renderer.Clear()
	renderer.Present()

	captureCompanionScreen(renderer)

	frame, seq, ok := requestCompanionStreamFrame(0, 200*time.Millisecond)
	if !ok || seq == 0 {
		t.Fatal("the capture did not publish a stream frame")
	}
	img, err := jpeg.Decode(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("decode captured jpeg: %v", err)
	}
	b := img.Bounds()
	if b.Dx() < 8 || b.Dy() < 8 {
		t.Errorf("captured frame is %dx%d", b.Dx(), b.Dy())
	}
	// The solid blue background must survive the ARGB→JPEG conversion.
	r, g, blue, _ := img.At(b.Dx()/2, b.Dy()/2).RGBA()
	if blue>>8 < 120 || r>>8 > 120 || g>>8 > 160 {
		t.Errorf("captured centre pixel is rgb(%d,%d,%d), want the blue background", r>>8, g>>8, blue>>8)
	}
}
