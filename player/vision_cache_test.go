package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVisionCacheBoundsEncodedMedia(t *testing.T) {
	visionCache.mu.Lock()
	old := visionCache.entries
	visionCache.entries = newByteOrderedStore[string, visionCacheEntry](
		visionCacheMax, 4<<20, visionCacheEntryBytes, nil,
	)
	visionCache.mu.Unlock()
	t.Cleanup(func() {
		visionCache.mu.Lock()
		visionCache.entries = old
		visionCache.mu.Unlock()
	})

	for _, key := range []string{"first", "second"} {
		visionCachePut(visionCacheEntry{key: key, picture: jukaVisionImage{Data: make([]byte, 1<<20)}})
	}
	clip := visionCacheEntry{key: "clip", hasClip: true}
	for range 3 {
		clip.clip.frames = append(clip.clip.frames, jukaVisionImage{Data: make([]byte, 1<<20)})
	}
	visionCachePut(clip)
	if _, ok := visionCacheGet("first"); ok {
		t.Fatal("old picture survived the byte limit")
	}
	if got, ok := visionCacheGet("clip"); !ok || len(got.clip.frames) != 3 {
		t.Fatal("recent video frames were not cached")
	}
	visionCache.mu.Lock()
	_, bytes := visionCache.entries.stats()
	visionCache.mu.Unlock()
	if bytes > 4<<20 {
		t.Fatalf("vision cache holds %d bytes, over its 4 MB test limit", bytes)
	}

	visionCachePut(visionCacheEntry{key: "oversized", picture: jukaVisionImage{Data: make([]byte, 5<<20)}})
	if _, ok := visionCacheGet("oversized"); ok {
		t.Fatal("oversized item entered the cache")
	}
	visionCacheClear()
	visionCache.mu.Lock()
	entries, bytes := visionCache.entries.stats()
	visionCache.mu.Unlock()
	if entries != 0 || bytes != 0 {
		t.Fatalf("vision cache after clear: %d entries, %d bytes", entries, bytes)
	}
}

func TestVisionFrameReadHasSizeLimit(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.jpg")
	if err := os.WriteFile(small, []byte("frame"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := visionReadFrame(small); err != nil || string(data) != "frame" {
		t.Fatalf("small frame = %q, %v", data, err)
	}
	large := filepath.Join(dir, "large.jpg")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(visionFrameReadMaxBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := visionReadFrame(large); !errors.Is(err, errVisionTooLarge) {
		t.Fatalf("large frame error = %v, want size limit", err)
	}
}

func TestVisionCacheKeepsLatestAfterChat(t *testing.T) {
	visionCache.mu.Lock()
	old := visionCache.entries
	visionCache.entries = newByteOrderedStore[string, visionCacheEntry](
		visionCacheMax, 8<<20, visionCacheEntryBytes, nil,
	)
	visionCache.mu.Unlock()
	t.Cleanup(func() {
		visionCache.mu.Lock()
		visionCache.entries = old
		visionCache.mu.Unlock()
	})
	for _, key := range []string{"one", "two", "three"} {
		visionCachePut(visionCacheEntry{key: key, picture: jukaVisionImage{Data: make([]byte, 1<<20)}})
	}
	visionCacheKeepLatest()
	if _, ok := visionCacheGet("one"); ok {
		t.Fatal("old AI media survived Chat close")
	}
	if _, ok := visionCacheGet("two"); ok {
		t.Fatal("second AI media survived Chat close")
	}
	if _, ok := visionCacheGet("three"); !ok {
		t.Fatal("newest AI media was lost")
	}
}

// aiTrTestBMP builds a BMP the way RetroArch writes one: BITMAPINFOHEADER,
// blue-green-red pixels, rows padded to four bytes.
func aiTrTestBMP(w, h, bpp int, topDown bool, px func(x, y int) [3]byte) []byte {
	step := bpp / 8
	stride := (w*step + 3) &^ 3
	data := make([]byte, 54+stride*h)
	le := binary.LittleEndian
	copy(data, "BM")
	le.PutUint32(data[2:], uint32(len(data)))
	le.PutUint32(data[10:], 54)
	le.PutUint32(data[14:], 40)
	le.PutUint32(data[18:], uint32(int32(w)))
	height := int32(h)
	if topDown {
		height = -height
	}
	le.PutUint32(data[22:], uint32(height))
	le.PutUint16(data[26:], 1)
	le.PutUint16(data[28:], uint16(bpp))
	for y := 0; y < h; y++ {
		row := h - 1 - y
		if topDown {
			row = y
		}
		for x := 0; x < w; x++ {
			c := px(x, y)
			p := 54 + row*stride + x*step
			data[p], data[p+1], data[p+2] = c[2], c[1], c[0]
		}
	}
	return data
}

// The frame RetroArch's AI service posts to JukaHub's translation service
// (aitranslate_server.go) on its way to the vision model is outside data:
// BMP and PNG, padded rows, both row orders, and broken or hostile headers
// refused rather than read past.
func TestAITranslateReadsRetroArchFrames(t *testing.T) {
	colour := func(x, y int) [3]byte { return [3]byte{byte(10 + x), byte(100 + y), 200} }
	for _, tc := range []struct {
		name    string
		bpp     int
		topDown bool
	}{{"24-bit bottom-up", 24, false}, {"32-bit top-down", 32, true}} {
		img, err := aiTrDecodeBMP(aiTrTestBMP(3, 2, tc.bpp, tc.topDown, colour))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for y := 0; y < 2; y++ {
			for x := 0; x < 3; x++ {
				want := colour(x, y)
				got := img.RGBAAt(x, y)
				if got.R != want[0] || got.G != want[1] || got.B != want[2] || got.A != 255 {
					t.Fatalf("%s: pixel %d,%d = %v, want %v", tc.name, x, y, got, want)
				}
			}
		}
	}

	good := aiTrTestBMP(3, 2, 24, false, colour)
	broken := map[string][]byte{
		"cut short": good[:len(good)-6],
		"16-bit":    func() []byte { b := bytes.Clone(good); b[28] = 16; return b }(),
		"huge":      func() []byte { b := bytes.Clone(good); binary.LittleEndian.PutUint32(b[18:], 1<<30); return b }(),
		"offset":    func() []byte { b := bytes.Clone(good); binary.LittleEndian.PutUint32(b[10:], 1<<31); return b }(),
		"header":    good[:20],
	}
	for name, data := range broken {
		if _, err := aiTrDecodeBMP(data); err == nil {
			t.Errorf("%s: a broken BMP was accepted", name)
		}
	}

	// A PNG in the JSON body, its base64 broken across lines.
	src := image.NewRGBA(image.Rect(0, 0, 4, 3))
	src.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	body := `{"image": "` + b64[:10] + `\n` + b64[10:] + `", "format": "png", "label": "Nintendo - Game Boy__Game", "state": {"paused": 0}}`
	img, label, err := aiTrReadRequest(strings.NewReader(body))
	if err != nil || label != "Nintendo - Game Boy__Game" || img.Bounds().Dx() != 4 {
		t.Fatalf("PNG request: %v, %q", err, label)
	}
	if r, _, _, _ := img.At(1, 1).RGBA(); r>>8 != 255 {
		t.Fatalf("PNG request decoded the wrong picture")
	}
	if _, _, err := aiTrReadRequest(strings.NewReader(`{"image": ""}`)); err == nil {
		t.Fatal("a request without a picture was accepted")
	}
}

// The translation service answers only its own random path, POST only; and a
// custom address that would break the retroarch.cfg line it is written to is
// refused.
func TestAITranslateServiceGuards(t *testing.T) {
	s := &aiTrServer{token: "0123abcd"}
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/translate/wrong", http.StatusNotFound},
		{http.MethodPost, "/", http.StatusNotFound},
		{http.MethodGet, "/translate/0123abcd", http.StatusMethodNotAllowed},
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
		if rec.Code != tc.want {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}

	for _, bad := range []string{`http://host/a"b`, "http://host/#x", "http://host/a b", "ftp://host/", "http://"} {
		if _, problem := aiTrCheckURL(bad); problem == "" {
			t.Errorf("%q was accepted", bad)
		}
	}
	got, problem := aiTrCheckURL("192.168.1.20:4404")
	if problem != "" || got != "http://192.168.1.20:4404" {
		t.Fatalf("plain host: %q %q", got, problem)
	}
	de, _ := aiTrLangFor("de")
	if u := aiTrServiceURL("http://h/service?api_key=K", de); u != "http://h/service?api_key=K&target_lang=de" {
		t.Errorf("service address: %q", u)
	}
	if u := aiTrServiceURL("http://h/?target_lang=ja", de); u != "http://h/?target_lang=ja" {
		t.Errorf("a chosen target_lang was replaced: %q", u)
	}
}
