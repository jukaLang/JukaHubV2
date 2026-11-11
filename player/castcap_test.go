package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// castTestJPEG encodes a small solid picture.
func castTestJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// castTestSharing switches the share flag for one test without starting the
// capture goroutines castSetEnabled would.
func castTestSharing(t *testing.T, on bool) {
	t.Helper()
	prev := castEnabled.Load()
	castEnabled.Store(on)
	t.Cleanup(func() { castEnabled.Store(prev) })
}

func TestCastWatchURLCarriesTheTokenNotThePIN(t *testing.T) {
	u := castWatchURL("192.168.1.42", 8317, "abcdefghijklmnop")
	if u != "http://192.168.1.42:8317/watch?c=abcdefghijklmnop" {
		t.Fatalf("watch URL = %q", u)
	}
	if castWatchURL("", 8317, "x") != "" || castWatchURL("10.0.0.2", 8317, "") != "" {
		t.Fatal("a link without an address or a token was built")
	}
	if tok := castNewToken(); len(tok) != 16 || strings.Trim(tok, "abcdefghijklmnopqrstuvwxyz234567") != "" {
		t.Fatalf("token %q is not 16 base32 characters", tok)
	}
}

func TestCastTokenGuard(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()
	token := castRotateToken()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status?c="+token, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status with the token = %d", rec.Code)
	}
	var rep castStatusReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil || rep.Quality == "" {
		t.Fatalf("status is not the report: %v %q", err, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status?c=aaaaaaaaaaaaaaaa", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong token got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, companionRequest(http.MethodGet, "/status", "123456", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the companion PIN got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/watch?c="+token, nil))
	page := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(page, "c="+token) || strings.Contains(page, "123456") {
		t.Fatalf("watch page %d carries the wrong credential", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("watch page CSP = %q", csp)
	}

	castRotateToken()
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status?c="+token, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a retired token still got %d", rec.Code)
	}
}

// A viewer holds a valid token. Requests with it must not clear the PIN's
// failure count, or the viewer could guess the PIN — which unlocks the
// keyboard — without ever reaching the lockout.
func TestCastTokenDoesNotResetThePINLockout(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()
	token := castRotateToken()
	for i := 0; i < companionFailLimit-1; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), companionRequest(http.MethodGet, "/?p=000000", "", nil))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status?c="+token, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the token was refused: %d", rec.Code)
	}
	handler.ServeHTTP(httptest.NewRecorder(), companionRequest(http.MethodGet, "/?p=000000", "", nil))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, companionRequest(http.MethodGet, "/?p=123456", "", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong PINs around a token request the right PIN got %d, want 429", companionFailLimit, rec.Code)
	}
}

func TestCastStreamAndFrameNeedSharingOn(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()
	token := castRotateToken()
	castTestSharing(t, false)
	for _, path := range []string{"/stream?c=", "/frame.jpg?c="} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+token, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s while sharing is off = %d, want 403", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/watch?c="+token, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the watch page must open while sharing is off, got %d", rec.Code)
	}
}

// castTestPollers empties the polling registry for one test and puts the old
// one back afterwards.
func castTestPollers(t *testing.T) {
	t.Helper()
	castPollers.mu.Lock()
	prev := castPollers.seen
	castPollers.seen = nil
	castPollers.mu.Unlock()
	t.Cleanup(func() {
		castPollers.mu.Lock()
		castPollers.seen = prev
		castPollers.mu.Unlock()
	})
}

// castTestTokenState keeps one test's token changes to itself.
func castTestTokenState(t *testing.T) {
	t.Helper()
	castToken.mu.Lock()
	prev := castToken.value
	castToken.mu.Unlock()
	prevLoaded := castTokenLoaded.Load()
	t.Cleanup(func() {
		castToken.mu.Lock()
		castToken.value = prev
		castToken.mu.Unlock()
		castTokenLoaded.Store(prevLoaded)
	})
}

// Streams and polling browsers share one cap: six pollers used to leave room
// for six streams more.
func TestCastViewerCapCountsPollers(t *testing.T) {
	castTestSharing(t, true)
	castTestPollers(t)
	base := castViewerCount()
	for i := 0; i < castMaxViewers-base-1; i++ {
		if !castNotePoll(fmt.Sprintf("10.2.0.%d:80", i+1)) {
			t.Fatalf("poller %d refused below the cap", i+1)
		}
	}
	if !castAddViewer("10.9.9.9:4000") {
		t.Fatal("the last free slot was refused to a stream")
	}
	defer castRemoveViewer()
	if castAddViewer("10.9.9.8:4000") {
		castRemoveViewer()
		t.Fatal("a stream joined a full share")
	}
	// A page that polled and now opens the stream is not refused by its own
	// polling footprint.
	if castMaxViewers-base-1 > 0 {
		if !castAddViewer("10.2.0.1:5555") {
			t.Fatal("a poller moving to the stream was refused by its own entry")
		}
		castRemoveViewer()
	}
}

// A single-frame request that waited through a new link (Y) must not be
// answered with the frame drawn after it: with the panel open, that picture
// shows the new link.
func TestCastFrameRechecksTheLinkAfterTheWait(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()
	castTestTokenState(t)
	castTestPollers(t)
	token := castRotateToken()
	castTestSharing(t, true)
	publishCompanionJPEG(castTestJPEG(t, 8, 8, color.RGBA{A: 255}), 8, 8)
	_, seq, _ := companionNewestJPEG()
	next := castTestJPEG(t, 8, 8, color.RGBA{R: 255, A: 255})
	go func() {
		time.Sleep(50 * time.Millisecond)
		castRotateToken()
		publishCompanionJPEG(next, 8, 8)
	}()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/frame.jpg?c=%s&s=%d", token, seq), nil))
	if rec.Code != http.StatusUnauthorized || bytes.Equal(rec.Body.Bytes(), next) {
		t.Fatalf("a frame for a retired link = %d with %d bytes", rec.Code, rec.Body.Len())
	}
}

// The token is kept in the user config so a TV's bookmark survives a restart;
// until it has been looked for, a link is neither accepted nor called dead.
func TestCastTokenIsSavedAndRestored(t *testing.T) {
	castTestTokenState(t)
	if !castTokenValid("abcdefghijklmn23") || castTokenValid("short") || castTokenValid("ABCDEFGHIJKLMN23") || castTokenValid("abcdefghijklmn01") {
		t.Fatal("castTokenValid accepts the wrong shapes")
	}
	castToken.mu.Lock()
	castToken.value = ""
	castToken.mu.Unlock()
	castTokenLoaded.Store(false)

	srv, _ := newTestCompanion(t)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status?c=abcdefghijklmn23", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a link before the token was restored got %d, want 503", rec.Code)
	}

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{castTokenKey: "abcdefghijklmn23"}}}
	castTokenLoad(cfg)
	if !castTokenMatches("abcdefghijklmn23") {
		t.Fatal("the saved token was not restored")
	}
	rec = httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status?c=abcdefghijklmn23", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the restored link got %d", rec.Code)
	}

	fresh := castRotateToken()
	if got := castEnsureToken(cfg); got != fresh || customStringValue(cfg.Variables.Custom, castTokenKey) != fresh {
		t.Fatalf("a new link was not saved: %q vs %v", got, cfg.Variables.Custom[castTokenKey])
	}
}

func TestCastMaskPINAndLiveLabel(t *testing.T) {
	castTestSharing(t, false)
	if castMaskPIN("123456") != "123456" {
		t.Fatal("the PIN was hidden while nothing is shared")
	}
	castEnabled.Store(true)
	if got := castMaskPIN("123456"); strings.Contains(got, "123456") {
		t.Fatalf("the PIN shows while sharing: %q", got)
	}
	if castLiveLabel(1) != "LIVE" || castLiveLabel(3) != "LIVE · 3" || castLiveLabel(4) != "LIVE · 4" {
		t.Fatal("LIVE pill labels are wrong")
	}
}

// The pairing code's link carries the PIN that types on the handheld, so the
// share paints over it: Home's header tile while Home is on screen, and the
// whole pairing card.
func TestCastRedactCoversThePairingCode(t *testing.T) {
	pix := bytes.Repeat([]byte{200}, 8*4*4)
	castRedact(pix, 32, 8, 4, []sdl.Rect{{X: 2, Y: 1, W: 3, H: 2}, {X: 7, Y: 3, W: 10, H: 10}})
	at := func(x, y int) []byte { return pix[y*32+x*4 : y*32+x*4+4] }
	navy := []byte{11, 15, 23, 255}
	if !bytes.Equal(at(3, 2), navy) || !bytes.Equal(at(7, 3), navy) {
		t.Fatalf("redacted pixels = %v, %v", at(3, 2), at(7, 3))
	}
	if !bytes.Equal(at(0, 0), []byte{200, 200, 200, 200}) || !bytes.Equal(at(5, 1), []byte{200, 200, 200, 200}) {
		t.Fatal("the redaction spilled outside its rects")
	}
	// An inverted or empty rect paints nothing and must not panic.
	castRedact(pix, 32, 8, 4, []sdl.Rect{{X: 6, Y: 0, W: -4, H: 2}, {X: 1, Y: 1, W: 0, H: 0}})
	if !bytes.Equal(at(3, 0), []byte{200, 200, 200, 200}) {
		t.Fatal("an inverted rect painted pixels")
	}

	prevHome, prevRect, prevOpen := homeLayoutActive, companionHeaderRect, companion.open
	prevPanel, prevQR, prevInfo := castPanel.open, castPanelRects[castFocusQR], castPanelInfoRect
	t.Cleanup(func() {
		homeLayoutActive, companionHeaderRect, companion.open = prevHome, prevRect, prevOpen
		castPanel.open, castPanelRects[castFocusQR], castPanelInfoRect = prevPanel, prevQR, prevInfo
	})
	castPanel.open = false
	homeLayoutActive = true
	companionHeaderRect = sdl.Rect{X: 10, Y: 20, W: 30, H: 30}
	companion.open = false
	if got := castShareRedactions(2, 2); len(got) != 1 || got[0] != (sdl.Rect{X: 20, Y: 40, W: 60, H: 60}) {
		t.Fatalf("Home's pairing tile redacted as %v", got)
	}
	homeLayoutActive = false
	if got := castShareRedactions(1, 1); len(got) != 0 {
		t.Fatalf("a tile left over from Home was redacted on another screen: %v", got)
	}
	companion.open = true
	if got := castShareRedactions(1, 1); len(got) != 1 || got[0].W <= 0 || got[0].H <= 0 {
		t.Fatalf("the pairing card redacted as %v", got)
	}
	// The Screen Share panel's own code and link are hidden from viewers.
	companion.open = false
	castPanel.open = true
	castPanelRects[castFocusQR] = sdl.Rect{X: 5, Y: 5, W: 100, H: 100}
	castPanelInfoRect = sdl.Rect{X: 120, Y: 5, W: 200, H: 80}
	if got := castShareRedactions(1, 1); len(got) != 2 || got[0] != castPanelRects[castFocusQR] || got[1] != castPanelInfoRect {
		t.Fatalf("the open panel redacted as %v", got)
	}
}
