package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestCastPresets(t *testing.T) {
	if castPresetIndex(" SMOOTH ") != 0 || castPresetIndex("sharp") != 2 || castPresetIndex("ultra") != -1 {
		t.Fatal("preset names do not resolve")
	}
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{castQualityKey: "sharp"}}}
	if p := castPresetFor(cfg); p.Name != "sharp" || p.Width != 0 {
		t.Fatalf("castQuality=sharp gave %+v", p)
	}
	cfg.Variables.Custom[castQualityKey] = "nonsense"
	if p := castPresetFor(cfg); castPresetIndex(p.Name) < 0 {
		t.Fatalf("an unknown quality fell back to %+v, not a preset", p)
	}
	if castDefaultQuality(true) != "smooth" || castDefaultQuality(false) != "balanced" {
		t.Fatal("the handheld must default to smooth and a desktop to balanced")
	}
	for _, p := range castPresets {
		if p.FPS <= 0 || p.Quality <= 0 || p.Quality > 100 || p.Label == "" {
			t.Fatalf("preset %+v is incomplete", p)
		}
	}
}

func TestCastParseRotate(t *testing.T) {
	for raw, want := range map[string]int{"0": 0, "90": 90, " 180 ": 180, "270": 270, "-90": 270, "": -1, "45": -1, "auto": -1} {
		if got := castParseRotate(raw); got != want {
			t.Errorf("castParseRotate(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestCastTargetSize(t *testing.T) {
	for _, c := range []struct{ w, h, pw, deg, tw, th int }{
		{1280, 720, 640, 0, 640, 360},
		{1280, 720, 960, 0, 960, 540},
		{1280, 720, 0, 0, 1280, 720},
		{960, 720, 640, 0, 640, 480},
		{1024, 768, 640, 0, 640, 480},
		{640, 360, 960, 0, 640, 360},    // never scaled up
		{720, 1280, 640, 270, 640, 360}, // a portrait framebuffer turned for a TV
		{720, 1280, 0, 90, 1280, 720},
	} {
		tw, th := castTargetSize(c.w, c.h, c.pw, c.deg)
		if tw != c.tw || th != c.th {
			t.Errorf("castTargetSize(%d, %d, %d, %d) = %dx%d, want %dx%d", c.w, c.h, c.pw, c.deg, tw, th, c.tw, c.th)
		}
	}
}

func TestCastBoxBoundsCoverEverySourcePixel(t *testing.T) {
	for _, c := range []struct{ from, to int }{{1280, 640}, {1280, 960}, {720, 540}, {7, 3}, {5, 5}} {
		b := castBoxBounds(nil, c.from, c.to)
		if len(b) != c.to+1 || b[0] != 0 || b[c.to] != c.from {
			t.Fatalf("%d→%d: bounds %v do not span the source", c.from, c.to, b)
		}
		for i := 0; i < c.to; i++ {
			if b[i+1] <= b[i] {
				t.Fatalf("%d→%d: cell %d is empty", c.from, c.to, i)
			}
		}
	}
}

func TestCastBoxScaleAverages(t *testing.T) {
	// 4x2, left half black, right half white, scaled to 2x1.
	src := make([]byte, 4*2*4)
	for y := 0; y < 2; y++ {
		for x := 2; x < 4; x++ {
			i := y*16 + x*4
			src[i], src[i+1], src[i+2] = 255, 255, 255
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, 2, 1))
	castBoxScale(dst, src, 16, 4, 2, castLayoutRGBA, castBoxBounds(nil, 4, 2), castBoxBounds(nil, 2, 1))
	if !bytes.Equal(dst.Pix, []byte{0, 0, 0, 255, 255, 255, 255, 255}) {
		t.Fatalf("box average = %v", dst.Pix)
	}

	// One 2x2 box of mixed greys averages to their mean.
	grey := []byte{0, 0, 0, 0, 100, 100, 100, 0, 200, 200, 200, 0, 100, 100, 100, 0}
	one := image.NewRGBA(image.Rect(0, 0, 1, 1))
	castBoxScale(one, grey, 8, 2, 2, castLayoutRGBA, castBoxBounds(nil, 2, 1), castBoxBounds(nil, 2, 1))
	if one.Pix[0] != 100 || one.Pix[3] != 255 {
		t.Fatalf("mixed box = %v, want 100 and opaque", one.Pix)
	}

	// A BGRA framebuffer pixel comes out as RGBA.
	bgra := []byte{10, 20, 30, 0}
	px := image.NewRGBA(image.Rect(0, 0, 1, 1))
	castBoxScale(px, bgra, 4, 1, 1, castLayoutBGRA, castBoxBounds(nil, 1, 1), castBoxBounds(nil, 1, 1))
	if !bytes.Equal(px.Pix, []byte{30, 20, 10, 255}) {
		t.Fatalf("BGRA pixel = %v, want 30 20 10 255", px.Pix)
	}
}

func TestCastRotateRGBA(t *testing.T) {
	red := []byte{255, 0, 0, 255}
	green := []byte{0, 255, 0, 255}
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	copy(src.Pix[0:4], red)
	copy(src.Pix[4:8], green)

	cw := image.NewRGBA(image.Rect(0, 0, 1, 2))
	castRotateRGBA(cw, src, 90)
	if !bytes.Equal(cw.Pix[0:4], red) || !bytes.Equal(cw.Pix[4:8], green) {
		t.Fatalf("a quarter turn clockwise of [red green] = %v, want red on top", cw.Pix)
	}
	ccw := image.NewRGBA(image.Rect(0, 0, 1, 2))
	castRotateRGBA(ccw, src, 270)
	if !bytes.Equal(ccw.Pix[0:4], green) || !bytes.Equal(ccw.Pix[4:8], red) {
		t.Fatalf("a quarter turn anticlockwise of [red green] = %v, want green on top", ccw.Pix)
	}
	half := image.NewRGBA(image.Rect(0, 0, 2, 1))
	castRotateRGBA(half, src, 180)
	if !bytes.Equal(half.Pix[0:4], green) || !bytes.Equal(half.Pix[4:8], red) {
		t.Fatalf("a half turn of [red green] = %v", half.Pix)
	}
}

func TestCastEncoderScalesAndTurns(t *testing.T) {
	const w, h = 64, 32
	src := make([]byte, w*h*4)
	for i := 0; i < len(src); i += 4 {
		src[i], src[i+1], src[i+2], src[i+3] = 200, 100, 50, 0
	}
	var enc castEncoder
	p := castPreset{Name: "test", Width: 32, FPS: 10, Quality: 85}
	frame, tw, th, err := enc.jpeg(src, w*4, w, h, castLayoutRGBA, p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tw != 32 || th != 16 {
		t.Fatalf("encoded %dx%d, want 32x16", tw, th)
	}
	img, err := jpeg.Decode(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("the frame is not a JPEG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 16 {
		t.Fatalf("decoded %v", b)
	}
	r, g, b, _ := img.At(16, 8).RGBA()
	if d := int(r>>8) - 200; d < -12 || d > 12 || int(g>>8) < 88 || int(g>>8) > 112 || int(b>>8) > 70 {
		t.Fatalf("colour drifted to %d %d %d", r>>8, g>>8, b>>8)
	}

	turned, tw, th, err := enc.jpeg(src, w*4, w, h, castLayoutRGBA, castPreset{Name: "t", FPS: 10, Quality: 80}, 90)
	if err != nil || tw != 32 || th != 64 {
		t.Fatalf("turned frame %dx%d, %v; want 32x64", tw, th, err)
	}
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(turned)); err != nil || cfg.Width != 32 || cfg.Height != 64 {
		t.Fatalf("turned JPEG is %+v, %v", cfg, err)
	}

	if _, _, _, err := enc.jpeg(src[:10], w*4, w, h, castLayoutRGBA, p, 0); err == nil {
		t.Fatal("a short buffer was encoded")
	}
}

func TestCastPNGIsOpaque(t *testing.T) {
	src := []byte{10, 20, 30, 0, 40, 50, 60, 0}
	data, err := castPNG(src, 8, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0xffff {
		t.Fatalf("screenshot alpha = %#x, want opaque", a)
	}
}

func TestCastJPEGSplitter(t *testing.T) {
	a := castTestJPEG(t, 16, 8, color.RGBA{R: 255, A: 255})
	b := castTestJPEG(t, 8, 8, color.RGBA{B: 255, A: 255})
	stream := append(append([]byte{0x00, 0x13, 0xFF}, a...), b...)

	var s castJPEGSplitter
	s.max = 1 << 20
	var got [][]byte
	for i := range stream {
		got = append(got, s.Feed(stream[i:i+1])...)
	}
	if len(got) != 2 || !bytes.Equal(got[0], a) || !bytes.Equal(got[1], b) {
		t.Fatalf("byte-by-byte split gave %d frames", len(got))
	}

	var chunked castJPEGSplitter
	chunked.max = 1 << 20
	got = nil
	for off := 0; off < len(stream); off += 37 {
		end := off + 37
		if end > len(stream) {
			end = len(stream)
		}
		got = append(got, chunked.Feed(stream[off:end])...)
	}
	if len(got) != 2 || !bytes.Equal(got[0], a) || !bytes.Equal(got[1], b) {
		t.Fatalf("chunked split gave %d frames", len(got))
	}

	var tail castJPEGSplitter
	tail.max = 1 << 20
	if frames := tail.Feed(a[:len(a)-1]); len(frames) != 0 {
		t.Fatal("a frame without its EOI was cut")
	}
	if frames := tail.Feed(a[len(a)-1:]); len(frames) != 1 || !bytes.Equal(frames[0], a) {
		t.Fatal("the frame did not complete with its last byte")
	}

	// A segment that claims more than the cap is dropped, not buffered for ever.
	runaway := castJPEGSplitter{max: 64}
	runaway.Feed(append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0xFF, 0xFF}, make([]byte, 100)...))
	if len(runaway.buf) != 0 {
		t.Fatalf("a runaway frame kept %d bytes", len(runaway.buf))
	}
}

func TestCastJPEGSize(t *testing.T) {
	frame := castTestJPEG(t, 24, 10, color.RGBA{G: 255, A: 255})
	if w, h, ok := castJPEGSize(frame); !ok || w != 24 || h != 10 {
		t.Fatalf("castJPEGSize = %d, %d, %v; want 24x10", w, h, ok)
	}
	if _, _, ok := castJPEGSize([]byte{1, 2, 3}); ok {
		t.Fatal("junk had a size")
	}
}

func TestCastFBGeomFromInfo(t *testing.T) {
	v := make([]byte, 160)
	put := func(b []byte, off, val int) { binary.LittleEndian.PutUint32(b[off:], uint32(val)) }
	put(v, 0, 1280)
	put(v, 4, 720)
	put(v, 8, 1280)
	put(v, 12, 1440)
	put(v, 20, 720)
	put(v, 24, 32)
	put(v, 32, 16) // red
	put(v, 44, 8)  // green
	put(v, 56, 0)  // blue
	fix := make([]byte, 80)
	put(fix, 48, 5120)
	g := castFBGeomFromInfo(v, fix, 8)
	if g.W != 1280 || g.H != 720 || g.VH != 1440 || g.YOff != 720 || g.BPP != 32 || g.Stride != 5120 || !g.valid() {
		t.Fatalf("decoded %+v", g)
	}
	if g.Layout != castLayoutBGRA {
		t.Fatalf("ARGB8888 decoded as %+v", g.Layout)
	}
	fix32 := make([]byte, 80)
	put(fix32, 44, 5120)
	if g32 := castFBGeomFromInfo(v, fix32, 4); g32.Stride != 5120 {
		t.Fatalf("32-bit line_length = %d", g32.Stride)
	}
	put(v, 32, 0)
	put(v, 56, 16)
	if rgba := castFBGeomFromInfo(v, fix, 8); rgba.Layout != castLayoutRGBA {
		t.Fatalf("ABGR8888 decoded as %+v", rgba.Layout)
	}
}

func TestCastSysfsParsing(t *testing.T) {
	if a, b, ok := castParsePair("1280,1440\n"); !ok || a != 1280 || b != 1440 {
		t.Fatal("virtual_size not parsed")
	}
	if a, b, ok := castParsePair("720x1280"); !ok || a != 720 || b != 1280 {
		t.Fatal("WxH not parsed")
	}
	if _, _, ok := castParsePair("junk"); ok {
		t.Fatal("junk parsed")
	}
	if n, ok := castParseInt("32\n"); !ok || n != 32 {
		t.Fatal("bits_per_pixel not parsed")
	}
	for _, c := range []struct{ vw, vh, cw, ch, w, h int }{
		{1280, 1440, 1280, 720, 1280, 720},
		{720, 2560, 1280, 720, 720, 1280},
		{1024, 768, 960, 720, 1024, 768},
		{1000, 999, 1280, 720, 1000, 999},
	} {
		if w, h := castGuessVisible(c.vw, c.vh, c.cw, c.ch); w != c.w || h != c.h {
			t.Errorf("castGuessVisible(%d, %d) = %dx%d, want %dx%d", c.vw, c.vh, w, h, c.w, c.h)
		}
	}

	dir := t.TempDir()
	for name, value := range map[string]string{
		"virtual_size": "1280,1440\n", "bits_per_pixel": "32\n", "stride": "5120\n",
		"pan": "0,720\n", "modes": "U:1280x720p-60\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g, err := castFBSysfsGeom(dir, 1280, 720)
	if err != nil || g.W != 1280 || g.H != 720 || g.VH != 1440 || g.Stride != 5120 || g.YOff != 720 || !g.valid() {
		t.Fatalf("sysfs geometry %+v, %v", g, err)
	}
}

func TestCastPixelHelpers(t *testing.T) {
	src := []byte{0x00, 0xF8, 0xE0, 0x07, 0x1F, 0x00} // red, green, blue
	dst := make([]byte, 12)
	castRGB565ToRGBA(dst, src, 3, 1, 6)
	if !bytes.Equal(dst, []byte{255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255}) {
		t.Fatalf("RGB565 expanded to %v", dst)
	}
	black := make([]byte, 16*16*4)
	if l := castMeanLuma(black, 64, 16, 16, castLayoutRGBA); l != 0 {
		t.Fatalf("black luma = %v", l)
	}
	white := bytes.Repeat([]byte{255}, 16*16*4)
	if l := castMeanLuma(white, 64, 16, 16, castLayoutRGBA); l < 254 {
		t.Fatalf("white luma = %v", l)
	}
	if castAutoRotate(720, 1280, 1280, 720) != 270 || castAutoRotate(1280, 720, 1280, 720) != 0 {
		t.Fatal("only a portrait framebuffer under a landscape canvas turns")
	}
}

func TestCastFFmpegArgs(t *testing.T) {
	smooth := strings.Join(castFFmpegArgs("/dev/fb0", castPresets[0], 0, 1280, 720), " ")
	for _, want := range []string{"-f fbdev", "-framerate 20", "-i /dev/fb0", "scale=640:-2:flags=area", "-q:v 7", "-f mjpeg pipe:1"} {
		if !strings.Contains(smooth, want) {
			t.Errorf("smooth grab %q lacks %q", smooth, want)
		}
	}
	if sharp := strings.Join(castFFmpegArgs("/dev/fb0", castPresets[2], 0, 1280, 720), " "); strings.Contains(sharp, "scale=") {
		t.Errorf("the sharp grab scales: %q", sharp)
	}
	if turned := strings.Join(castFFmpegArgs("/dev/fb0", castPresets[0], 270, 720, 1280), " "); !strings.Contains(turned, "transpose=2,scale=640:-2:flags=area") {
		t.Errorf("a turned grab %q does not turn before it scales", turned)
	}
	for q, want := range map[int]int{60: 7, 65: 6, 75: 4, 100: 2, 0: 17} {
		if got := castFFmpegQ(q); got != want {
			t.Errorf("castFFmpegQ(%d) = %d, want %d", q, got, want)
		}
	}
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

func TestCastFrameServesTheNewestJPEG(t *testing.T) {
	srv, _ := newTestCompanion(t)
	handler := srv.handler()
	token := castRotateToken()
	castTestSharing(t, true)
	frame := castTestJPEG(t, 16, 8, color.RGBA{R: 40, G: 80, B: 120, A: 255})
	publishCompanionJPEG(frame, 16, 8)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/frame.jpg?c="+token+"&s=0", nil))
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), frame) {
		t.Fatalf("frame.jpg = %d with %d bytes", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("Content-Type") != "image/jpeg" || rec.Header().Get("X-Frame-Seq") == "" {
		t.Fatalf("frame headers %v", rec.Header())
	}
	if castPollerCount() == 0 || castWatcherCount() == 0 {
		t.Fatal("a poll did not count as someone watching")
	}
}

func TestCastPollersCountAndCap(t *testing.T) {
	castPollers.mu.Lock()
	prev := castPollers.seen
	castPollers.seen = nil
	castPollers.mu.Unlock()
	t.Cleanup(func() {
		castPollers.mu.Lock()
		castPollers.seen = prev
		castPollers.mu.Unlock()
	})
	for i := 0; i < castMaxViewers-castViewerCount(); i++ {
		if !castNotePoll(fmt.Sprintf("10.0.0.%d:5000", i+1)) {
			t.Fatalf("browser %d was refused below the cap", i+1)
		}
	}
	if castNotePoll("10.0.0.99:5000") {
		t.Fatal("a browser polled past the viewer cap")
	}
	if !castNotePoll("10.0.0.1:6000") {
		t.Fatal("a browser already watching was refused")
	}
	if got := castPollerCount(); got != castMaxViewers-castViewerCount() {
		t.Fatalf("castPollerCount = %d", got)
	}
}

func TestCastStatusNotes(t *testing.T) {
	castTestSharing(t, false)
	if rep := castStatusNow(); rep.Sharing || rep.Source != "off" || rep.Note == "" || rep.Full {
		t.Fatalf("status while off = %+v", rep)
	}
	castEnabled.Store(true)
	prevSource, prevGames, prevFailed := castLive.source.Load(), castLive.games.Load(), castLive.fbFailed.Load()
	t.Cleanup(func() {
		castLive.source.Store(prevSource)
		castLive.games.Store(prevGames)
		castLive.fbFailed.Store(prevFailed)
	})
	castLive.source.Store(castSourcePaused)
	castLive.games.Store(false)
	castLive.fbFailed.Store(false)
	rep := castStatusNow()
	if rep.Source != "paused" {
		t.Fatalf("paused status = %+v", rep)
	}
	if !castGamesSupported() {
		// A desktop never reads a game's picture: the note says so instead of
		// blaming a switch that does not exist there.
		if !strings.Contains(rep.Note, "handheld only") {
			t.Fatalf("desktop game note = %q", rep.Note)
		}
		return
	}
	if !strings.Contains(rep.Note, "Sharing games is off") {
		t.Fatalf("games-off note = %q", rep.Note)
	}
	// Games on, nothing failed yet: the picture is on its way, not impossible.
	castLive.games.Store(true)
	if rep := castStatusNow(); strings.Contains(rep.Note, "cannot be shared") || !strings.Contains(rep.Note, "in a moment") {
		t.Fatalf("starting note = %q", rep.Note)
	}
	castLive.fbFailed.Store(true)
	if rep := castStatusNow(); !strings.Contains(rep.Note, "cannot be shared") {
		t.Fatalf("failed note = %q", rep.Note)
	}
}

// /status says when the share is full, so a page left out can say why instead
// of retrying in silence.
func TestCastStatusFull(t *testing.T) {
	castTestSharing(t, true)
	castTestPollers(t)
	if castStatusNow().Full && castViewerCount() < castMaxViewers {
		t.Fatal("an empty share reported full")
	}
	for i := 0; i < castMaxViewers-castViewerCount(); i++ {
		castNotePoll(fmt.Sprintf("10.1.0.%d:80", i+1))
	}
	if rep := castStatusNow(); !rep.Full {
		t.Fatalf("a share with %d watchers is not full: %+v", rep.Viewers, rep)
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

func TestCastNativeReaderCaps(t *testing.T) {
	for _, p := range castPresets {
		np := castNativePreset(p)
		if np.FPS > castNativeMaxFPS || np.Width <= 0 || np.Width > castNativeMaxWidth || np.Quality != p.Quality {
			t.Fatalf("native preset for %s = %+v", p.Name, np)
		}
	}
	if w := castNativeWait(125*time.Millisecond, 20*time.Millisecond); w != 105*time.Millisecond {
		t.Fatalf("wait after a quick frame = %v", w)
	}
	// A frame slower than the interval waits as long as it took: half a core.
	if w := castNativeWait(125*time.Millisecond, 200*time.Millisecond); w != 200*time.Millisecond {
		t.Fatalf("wait after a slow frame = %v", w)
	}
}

func TestCastUIGap(t *testing.T) {
	if g := castUIGap(20, false, 0); g != 50*time.Millisecond {
		t.Fatalf("desktop gap at 20 fps = %v", g)
	}
	if g := castUIGap(20, true, 0); g != castUIHandheldGap {
		t.Fatalf("handheld gap at 20 fps = %v, want the %v cap", g, castUIHandheldGap)
	}
	if g := castUIGap(20, true, 15*time.Millisecond); g != 150*time.Millisecond {
		t.Fatalf("gap with a 15 ms readback = %v, want ten times it", g)
	}
	if g := castUIGap(20, false, 500*time.Millisecond); g != castUIMaxGap {
		t.Fatalf("gap with a very slow readback = %v, want the %v bound", g, castUIMaxGap)
	}
}

// A still picture is not encoded again; after castKeepAlive the previous
// JPEG is handed back as the keep-alive.
func TestCastEncoderSkipsStillFrames(t *testing.T) {
	const w, h = 32, 16
	src := bytes.Repeat([]byte{90, 120, 150, 255}, w*h)
	enc := castEncoder{dedupe: true}
	p := castPreset{Name: "t", Width: 16, FPS: 10, Quality: 70}
	first, _, _, err := enc.jpeg(src, w*4, w, h, castLayoutRGBA, p, 0)
	if err != nil || len(first) == 0 {
		t.Fatalf("first frame: %d bytes, %v", len(first), err)
	}
	if again, _, _, err := enc.jpeg(src, w*4, w, h, castLayoutRGBA, p, 0); err != nil || len(again) != 0 {
		t.Fatalf("a still frame was encoded again (%d bytes, %v)", len(again), err)
	}
	enc.prevAt = time.Now().Add(-2 * castKeepAlive)
	if alive, _, _, _ := enc.jpeg(src, w*4, w, h, castLayoutRGBA, p, 0); !bytes.Equal(alive, first) {
		t.Fatal("the keep-alive is not the previous picture")
	}
	src[0] = 10
	if changed, _, _, _ := enc.jpeg(src, w*4, w, h, castLayoutRGBA, p, 0); len(changed) == 0 {
		t.Fatal("a changed frame was skipped")
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

func TestCastPanelLayoutFits(t *testing.T) {
	inside := func(r, card sdl.Rect) bool {
		return r.X >= card.X && r.Y >= card.Y && r.X+r.W <= card.X+card.W && r.Y+r.H <= card.Y+card.H
	}
	for _, c := range []struct{ w, h int32 }{{1280, 720}, {960, 720}, {800, 480}} {
		l := castPanelLayoutFor(c.w, c.h, true, 28, 17)
		if !inside(l.card, sdl.Rect{X: 0, Y: 0, W: c.w, H: c.h}) || !inside(l.qr, l.card) {
			t.Fatalf("%dx%d: card %+v or code %+v outside", c.w, c.h, l.card, l.qr)
		}
		for i, r := range l.rows {
			if !inside(r, l.card) || r.X < l.qr.X+l.qr.W {
				t.Fatalf("%dx%d: row %d %+v overlaps the code or leaves the card", c.w, c.h, i, r)
			}
		}
		if last := l.rows[2]; l.footerY <= last.Y+last.H || l.info.H <= 0 {
			t.Fatalf("%dx%d: footer at %d, info %+v under rows ending %d", c.w, c.h, l.footerY, l.info, last.Y+last.H)
		}
	}
}
