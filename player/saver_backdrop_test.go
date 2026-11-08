package main

// The idle screens' picture pipeline: EXIF orientation, the cover crop, the
// averaged scaling, transparency over the dark base, the baked scrim, and the
// rendition cache that keeps decoding off the render thread. All of it is pure
// Go, so it runs without SDL.

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// saverTestPNGBytes encodes a 6x5 solid-colour PNG.
func saverTestPNGBytes(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 6, 5))
	for y := 0; y < 5; y++ {
		for x := 0; x < 6; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// saverExifAPP1 builds an APP1 segment carrying one orientation tag.
func saverExifAPP1(order binary.AppendByteOrder, orientation uint16) []byte {
	tiff := make([]byte, 0, 32)
	if order == binary.LittleEndian {
		tiff = append(tiff, 'I', 'I')
	} else {
		tiff = append(tiff, 'M', 'M')
	}
	tiff = order.AppendUint16(tiff, 42)
	tiff = order.AppendUint32(tiff, 8) // IFD0 right after the header
	tiff = order.AppendUint16(tiff, 1) // one entry
	tiff = order.AppendUint16(tiff, 0x0112)
	tiff = order.AppendUint16(tiff, 3) // SHORT
	tiff = order.AppendUint32(tiff, 1)
	tiff = order.AppendUint16(tiff, orientation)
	tiff = order.AppendUint16(tiff, 0)
	tiff = order.AppendUint32(tiff, 0) // no next IFD
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	return append(seg, payload...)
}

// saverJPEGWithOrientation encodes img as a JPEG and inserts the EXIF block
// right after the start-of-image marker, where cameras put it.
func saverJPEGWithOrientation(t *testing.T, img image.Image, order binary.AppendByteOrder, orientation uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	out := append([]byte{}, raw[:2]...)
	out = append(out, saverExifAPP1(order, orientation)...)
	return append(out, raw[2:]...)
}

func TestSaverJPEGOrientation(t *testing.T) {
	plain := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for _, order := range []binary.AppendByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, o := range []uint16{1, 3, 6, 8} {
			data := saverJPEGWithOrientation(t, plain, order, o)
			if got := saverJPEGOrientation(data); got != int(o) {
				t.Errorf("%v orientation %d read as %d", order, o, got)
			}
		}
	}
	var noExif bytes.Buffer
	if err := jpeg.Encode(&noExif, plain, nil); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"no EXIF":      noExif.Bytes(),
		"not a JPEG":   []byte("\x89PNG\r\n\x1a\n"),
		"empty":        nil,
		"truncated":    saverJPEGWithOrientation(t, plain, binary.LittleEndian, 6)[:20],
		"out of range": saverJPEGWithOrientation(t, plain, binary.BigEndian, 42),
	}
	for name, data := range cases {
		if got := saverJPEGOrientation(data); got != 1 {
			t.Errorf("%s: orientation %d, want 1", name, got)
		}
	}
}

// A phone photo stored landscape with "rotate 90°" comes out upright, filling
// the screen, through the real decode path.
func TestSaverPicRenderAppliesOrientation(t *testing.T) {
	// Stored 16x8: the left half red, the right half blue. Turned a quarter
	// clockwise it is 8 wide and 16 tall with red on top.
	stored := image.NewRGBA(image.Rect(0, 0, 16, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 16; x++ {
			c := color.RGBA{R: 230, G: 20, B: 20, A: 255}
			if x >= 8 {
				c = color.RGBA{R: 20, G: 20, B: 230, A: 255}
			}
			stored.SetRGBA(x, y, c)
		}
	}
	path := filepath.Join(t.TempDir(), "portrait.jpg")
	if err := os.WriteFile(path, saverJPEGWithOrientation(t, stored, binary.LittleEndian, 6), 0o644); err != nil {
		t.Fatal(err)
	}
	pic, orient, err := saverPicDecode(path)
	if err != nil || orient != 6 {
		t.Fatalf("decode: orientation %d, err %v", orient, err)
	}
	out := saverPicCover(pic, orient, 4, 8, saverBackdropBase)
	top, bottom := out.RGBAAt(2, 1), out.RGBAAt(2, 6)
	if top.R < top.B+80 || bottom.B < bottom.R+80 {
		t.Fatalf("top %v and bottom %v: the photo was not turned upright", top, bottom)
	}
}

func TestSaverOrientMapsEveryCorner(t *testing.T) {
	// For each orientation, where the stored top-left pixel lands on screen.
	const sw, sh = 3, 2
	want := map[int][2]int{
		1: {0, 0},
		2: {sw - 1, 0},
		3: {sw - 1, sh - 1},
		4: {0, sh - 1},
		5: {0, 0},
		6: {sh - 1, 0},
		7: {sh - 1, sw - 1},
		8: {0, sw - 1},
	}
	for o, at := range want {
		m, dw, dh := saverOrientFor(o, sw, sh)
		if o >= 5 && (dw != sh || dh != sw) {
			t.Errorf("orientation %d displays %dx%d, want %dx%d", o, dw, dh, sh, sw)
		}
		u, v := at[0], at[1]
		x := m.ax*u + m.bx*v + m.cx
		y := m.ay*u + m.by*v + m.cy
		if x != 0 || y != 0 {
			t.Errorf("orientation %d: display %v reads stored (%d,%d), want the top-left", o, at, x, y)
		}
		// Every display pixel must read a stored pixel inside the picture.
		for v := 0; v < dh; v++ {
			for u := 0; u < dw; u++ {
				x := m.ax*u + m.bx*v + m.cx
				y := m.ay*u + m.by*v + m.cy
				if x < 0 || x >= sw || y < 0 || y >= sh {
					t.Fatalf("orientation %d: display (%d,%d) reads outside the picture (%d,%d)", o, u, v, x, y)
				}
			}
		}
	}
}

func TestSaverCoverCropFillsWithoutDistortion(t *testing.T) {
	cases := []struct {
		name               string
		sw, sh, dw, dh     int
		wantX0, wantY0     float64
		wantCropW, wantCrH float64
	}{
		{"wide photo on 16:9", 4000, 1000, 1280, 720, 1111.111, 0, 1777.778, 1000},
		{"portrait photo on 16:9", 3000, 4000, 1280, 720, 0, 1156.25, 3000, 1687.5},
		{"same aspect", 1920, 1080, 1280, 720, 0, 0, 1920, 1080},
	}
	for _, c := range cases {
		x0, y0, cw, ch := saverCoverCrop(c.sw, c.sh, c.dw, c.dh)
		near := func(a, b float64) bool { return a-b < 0.01 && b-a < 0.01 }
		if !near(x0, c.wantX0) || !near(y0, c.wantY0) || !near(cw, c.wantCropW) || !near(ch, c.wantCrH) {
			t.Errorf("%s: crop (%.3f,%.3f %.3fx%.3f), want (%.3f,%.3f %.3fx%.3f)", c.name, x0, y0, cw, ch, c.wantX0, c.wantY0, c.wantCropW, c.wantCrH)
		}
		if ratio, want := cw/ch, float64(c.dw)/float64(c.dh); ratio-want > 0.001 || want-ratio > 0.001 {
			t.Errorf("%s: crop aspect %.4f, want the screen's %.4f", c.name, ratio, want)
		}
	}
}

// A wide picture covering a square screen keeps its middle: the red and blue
// halves of the centre survive and the cropped edges do not.
func TestSaverPicCoverCropsTheMiddle(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 400, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 400; x++ {
			var c color.RGBA
			switch {
			case x < 150 || x >= 250:
				c = color.RGBA{G: 220, A: 255} // the edges a cover crop removes
			case x < 200:
				c = color.RGBA{R: 220, A: 255}
			default:
				c = color.RGBA{B: 220, A: 255}
			}
			src.SetRGBA(x, y, c)
		}
	}
	out := saverPicCover(src, 1, 10, 10, saverBackdropBase)
	if b := out.Bounds(); b.Dx() != 10 || b.Dy() != 10 {
		t.Fatalf("output is %v, want the 10x10 screen", b)
	}
	for y := 0; y < 10; y++ {
		left, right := out.RGBAAt(1, y), out.RGBAAt(8, y)
		if left.R < 180 || left.G > 40 || right.B < 180 || right.G > 40 {
			t.Fatalf("row %d: left %v right %v; the crop is off-centre or kept the edges", y, left, right)
		}
		if out.RGBAAt(0, y).A != 255 {
			t.Fatal("the rendition is not opaque")
		}
	}
}

// Transparency is composited over the idle screen's dark base, and a small
// picture is scaled up smoothly to the full screen.
func TestSaverPicCoverCompositesAndUpscales(t *testing.T) {
	clear := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	out := saverPicCover(clear, 1, 8, 4, saverBackdropBase)
	if got := out.RGBAAt(3, 2); got.R != saverBackdropBase.R || got.G != saverBackdropBase.G || got.B != saverBackdropBase.B {
		t.Fatalf("a transparent picture became %v, want the dark base %v", got, saverBackdropBase)
	}

	half := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := 0; i < len(half.Pix); i += 4 {
		half.Pix[i], half.Pix[i+1], half.Pix[i+2], half.Pix[i+3] = 255, 255, 255, 128
	}
	out = saverPicCover(half, 1, 8, 4, saverBackdropBase)
	if got := out.RGBAAt(4, 2); got.R < 120 || got.R > 140 {
		t.Fatalf("a half-transparent white became %v, want about half way to the base", got)
	}

	// A paletted PNG with a transparent entry decodes through the real path.
	pal := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.NRGBA{}, color.NRGBA{R: 255, A: 255}})
	for i := range pal.Pix {
		if i%2 == 0 {
			pal.Pix[i] = 1
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, pal); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "palette.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	pix, err := saverPicRender(path, 16, 16)
	if err != nil {
		t.Fatalf("a paletted png with transparency failed: %v", err)
	}
	if pix.Bounds().Dx() != 16 || pix.Bounds().Dy() != 16 {
		t.Fatalf("rendition is %v, want 16x16", pix.Bounds())
	}
}

// The scrim is baked deepest behind the top and bottom bands and lightest in
// the middle, and never makes a picture disappear.
func TestSaverScrimProfile(t *testing.T) {
	top, mid, bottom := saverScrimAlpha(0), saverScrimAlpha(0.5), saverScrimAlpha(1)
	if !(top > mid && bottom > mid) {
		t.Fatalf("scrim top %.2f mid %.2f bottom %.2f: the bands are not deeper than the middle", top, mid, bottom)
	}
	for f := 0.0; f <= 1.0; f += 0.05 {
		if a := saverScrimAlpha(f); a < 0.2 || a > 0.82 {
			t.Fatalf("scrim at %.2f is %.2f, outside 0.2..0.82", f, a)
		}
	}
	white := image.NewRGBA(image.Rect(0, 0, 2, 10))
	for i := range white.Pix {
		white.Pix[i] = 255
	}
	saverBakeScrim(white)
	if m, e := white.RGBAAt(0, 5).R, white.RGBAAt(0, 0).R; m <= e || m < 150 {
		t.Fatalf("baked scrim: middle %d, top edge %d", m, e)
	}
}

func TestSaverPicProbe(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.png")
	if err := os.WriteFile(good, saverTestPNGBytes(t, color.RGBA{R: 1, A: 255}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saverPicProbe(good); err != nil {
		t.Errorf("a real png was refused: %v", err)
	}

	// A PNG whose header declares 20000x20000 is refused before a pixel is
	// allocated.
	var hdr bytes.Buffer
	hdr.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 20000)
	binary.BigEndian.PutUint32(ihdr[4:], 20000)
	ihdr[8], ihdr[9] = 8, 6
	chunk := append([]byte("IHDR"), ihdr...)
	_ = binary.Write(&hdr, binary.BigEndian, uint32(len(ihdr)))
	hdr.Write(chunk)
	_ = binary.Write(&hdr, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	huge := filepath.Join(dir, "huge.png")
	if err := os.WriteFile(huge, hdr.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saverPicProbe(huge); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("a 400-megapixel png was not refused as too large: %v", err)
	}

	broken := filepath.Join(dir, "broken.jpg")
	if err := os.WriteFile(broken, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saverPicProbe(broken); err == nil {
		t.Error("a fake jpg was accepted")
	}
	// BMP and WebP are decoded by SDL_image when drawn; the probe reads their
	// headers for the size.
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if err := saverPicProbe(write("legacy.bmp", saverTestBMPHeader(640, -480))); err != nil {
		t.Errorf("a valid .bmp was refused: %v", err)
	}
	if err := saverPicProbe(write("giant.bmp", saverTestBMPHeader(30000, 30000))); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("a 900-megapixel .bmp was not refused as too large: %v", err)
	}
	if err := saverPicProbe(write("fake.bmp", []byte("BM-not-really"))); err == nil {
		t.Error("a truncated .bmp was accepted")
	}
	if err := saverPicProbe(write("small.webp", saverTestWebPHeader("VP8L", 300, 200))); err != nil {
		t.Errorf("a valid .webp was refused: %v", err)
	}
	if err := saverPicProbe(write("giant.webp", saverTestWebPHeader("VP8X", 16000, 16000))); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("a 256-megapixel .webp was not refused as too large: %v", err)
	}
}

// saverTestBMPHeader is the 54-byte header of a 24-bit BMP of the given size
// (a negative height is a top-down bitmap).
func saverTestBMPHeader(w, h int32) []byte {
	b := make([]byte, 54)
	b[0], b[1] = 'B', 'M'
	binary.LittleEndian.PutUint32(b[2:], 54)
	binary.LittleEndian.PutUint32(b[10:], 54)
	binary.LittleEndian.PutUint32(b[14:], 40)
	binary.LittleEndian.PutUint32(b[18:], uint32(w))
	binary.LittleEndian.PutUint32(b[22:], uint32(h))
	binary.LittleEndian.PutUint16(b[26:], 1)
	binary.LittleEndian.PutUint16(b[28:], 24)
	return b
}

// saverTestWebPHeader is the start of a WebP file with one image chunk of the
// given kind ("VP8 ", "VP8L" or "VP8X") declaring w×h.
func saverTestWebPHeader(kind string, w, h int) []byte {
	b := make([]byte, 40)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 32)
	copy(b[8:], "WEBP")
	copy(b[12:], kind)
	binary.LittleEndian.PutUint32(b[16:], 20)
	switch kind {
	case "VP8 ":
		b[23], b[24], b[25] = 0x9d, 0x01, 0x2a
		binary.LittleEndian.PutUint16(b[26:], uint16(w))
		binary.LittleEndian.PutUint16(b[28:], uint16(h))
	case "VP8L":
		b[20] = 0x2f
		binary.LittleEndian.PutUint32(b[21:], uint32(w-1)|uint32(h-1)<<14)
	case "VP8X":
		b[24], b[25], b[26] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
		b[27], b[28], b[29] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)
	}
	return b
}

func TestSaverPicHeaderSize(t *testing.T) {
	os2 := saverTestBMPHeader(0, 0)
	binary.LittleEndian.PutUint32(os2[14:], 12)
	binary.LittleEndian.PutUint16(os2[18:], 320)
	binary.LittleEndian.PutUint16(os2[20:], 240)
	cases := []struct {
		name string
		head []byte
		w, h int
		ok   bool
	}{
		{"bmp", saverTestBMPHeader(800, 600), 800, 600, true},
		{"top-down bmp", saverTestBMPHeader(800, -600), 800, 600, true},
		{"os/2 bmp", os2, 320, 240, true},
		{"lossy webp", saverTestWebPHeader("VP8 ", 1024, 768), 1024, 768, true},
		{"lossless webp", saverTestWebPHeader("VP8L", 4000, 3000), 4000, 3000, true},
		{"extended webp", saverTestWebPHeader("VP8X", 12000, 9000), 12000, 9000, true},
		{"png", []byte("\x89PNG\r\n\x1a\n0000000000000000000000000000"), 0, 0, false},
		{"short", []byte("BM"), 0, 0, false},
	}
	for _, c := range cases {
		w, h, ok := saverPicHeaderSize(c.head)
		if ok != c.ok || (ok && (w != c.w || h != c.h)) {
			t.Errorf("%s: got %dx%d ok=%v, want %dx%d ok=%v", c.name, w, h, ok, c.w, c.h, c.ok)
		}
	}
}

// The rendition cache decodes once per version and size, off the caller's
// thread, and bounds how many decodes run at once.
func TestSaverPicRequestDecodesOnceOffThread(t *testing.T) {
	saverPicReset()
	t.Cleanup(saverPicReset)
	prev := saverPicRenderFn
	t.Cleanup(func() { saverPicRenderFn = prev })
	var renders atomic.Int32
	gate := make(chan struct{})
	var openGate sync.Once
	release := func() { openGate.Do(func() { close(gate) }) }
	// A failure before the gate opens must not leave the workers blocked.
	t.Cleanup(release)
	saverPicRenderFn = func(path string, w, h int) (*image.RGBA, error) {
		renders.Add(1)
		<-gate
		return image.NewRGBA(image.Rect(0, 0, w, h)), nil
	}
	// waitRenders polls for the workers instead of sleeping a fixed time, so a
	// slow runner does not read the count before the goroutines have run.
	waitRenders := func(want int32) int32 {
		deadline := time.Now().Add(5 * time.Second)
		for renders.Load() < want && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		return renders.Load()
	}
	key := saverPicKey{path: "/pics/a.png", stamp: "1:2", w: 8, h: 4}
	if pix, done, err := saverPicRequest(key); done || pix != nil || err != nil {
		t.Fatalf("the first request answered at once: %v %v %v", pix, done, err)
	}
	// Two workers at most: a third key is turned away until one is free (it
	// is not even queued, so the count can only ever reach two here).
	saverPicRequest(saverPicKey{path: "/pics/b.png", stamp: "1", w: 8, h: 4})
	if _, done, _ := saverPicRequest(saverPicKey{path: "/pics/c.png", stamp: "1", w: 8, h: 4}); done {
		t.Fatal("a third decode was answered while two were running")
	}
	if n := waitRenders(2); n != 2 {
		t.Fatalf("%d decodes started, want the cap of 2", n)
	}
	saverPics.mu.Lock()
	queued := len(saverPics.entries)
	saverPics.mu.Unlock()
	if queued != 2 {
		t.Fatalf("%d renditions are in the cache, want the two running ones", queued)
	}
	release()
	deadline := time.Now().Add(5 * time.Second)
	var pix *image.RGBA
	for time.Now().Before(deadline) {
		var done bool
		var err error
		if pix, done, err = saverPicRequest(key); done {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pix == nil || pix.Bounds().Dx() != 8 {
		t.Fatalf("the rendition never arrived: %v", pix)
	}
	before := renders.Load()
	if again, done, _ := saverPicRequest(key); !done || again != pix {
		t.Fatal("a finished rendition was decoded again")
	}
	if renders.Load() != before {
		t.Fatal("asking again started another decode")
	}
	// A new version of the file is a new key.
	saverPicRequest(saverPicKey{path: "/pics/a.png", stamp: "3:4", w: 8, h: 4})
	if waitRenders(before+1) == before {
		t.Fatal("a replaced file was served from the old rendition")
	}
	// Forgetting keeps the named picture and drops the rest.
	saverPicForget("/pics/a.png")
	saverPics.mu.Lock()
	for _, e := range saverPics.entries {
		if e.done && e.key.path != "/pics/a.png" {
			t.Errorf("forget kept %s", e.key.path)
		}
	}
	saverPics.mu.Unlock()
}

// A decoder that panics costs its own picture only.
func TestSaverPicRenderPanicIsContained(t *testing.T) {
	saverPicReset()
	t.Cleanup(saverPicReset)
	prev := saverPicRenderFn
	t.Cleanup(func() { saverPicRenderFn = prev })
	saverPicRenderFn = func(string, int, int) (*image.RGBA, error) { panic("bad file") }
	key := saverPicKey{path: "/pics/evil.png", stamp: "1", w: 4, h: 4}
	saverPicRequest(key)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, done, err := saverPicRequest(key); done {
			if err == nil {
				t.Fatal("a panicking decode reported success")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("a panicking decode never finished")
}

func TestSaverFadeIn(t *testing.T) {
	now := time.Now()
	if got := saverFadeIn(time.Time{}, now, time.Second); got != 255 {
		t.Errorf("no start = %d, want 255", got)
	}
	if got := saverFadeIn(now, now, time.Second); got != 0 {
		t.Errorf("at the start = %d, want 0", got)
	}
	if got := saverFadeIn(now.Add(-500*time.Millisecond), now, time.Second); got < 100 || got > 150 {
		t.Errorf("half way = %d", got)
	}
	if got := saverFadeIn(now.Add(-2*time.Second), now, time.Second); got != 255 {
		t.Errorf("after = %d, want 255", got)
	}
}
