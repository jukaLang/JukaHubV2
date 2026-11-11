package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testBMPSpec describes a BMP the tests build by hand, independently of the
// parser under test.
type testBMPSpec struct {
	w, h        int
	bits        int
	compression uint32
	header      int // info header size: 40, 52, 56, 108 or 124
	topDown     bool
	masks       [4]uint32 // R, G, B, A for BI_BITFIELDS
	trailing    int       // bytes after the pixel rows
}

func (s testBMPSpec) stride() int { return (s.w*s.bits + 31) / 32 * 4 }

func (s testBMPSpec) offset() int {
	off := 14 + s.header
	if s.compression == bmpBIBitfields && s.header == 40 {
		off += 12
	}
	return off
}

// channelBytes is where R, G, B and the fourth byte sit in one pixel for this
// spec, worked out from the spec alone.
func (s testBMPSpec) channelBytes() (r, g, b, x int) {
	if s.bits == 24 {
		return 2, 1, 0, -1
	}
	if s.compression != bmpBIBitfields {
		return 2, 1, 0, 3
	}
	idx := func(m uint32) int {
		for i := 0; i < 4; i++ {
			if m == 0xFF<<(8*uint(i)) {
				return i
			}
		}
		return -1
	}
	r, g, b = idx(s.masks[0]), idx(s.masks[1]), idx(s.masks[2])
	return r, g, b, 6 - r - g - b
}

// testBMP builds the file: every pixel byte 0x11, trailing bytes 0xEE.
func testBMP(s testBMPSpec) []byte {
	off := s.offset()
	size := off + s.stride()*s.h + s.trailing
	b := make([]byte, size)
	le := binary.LittleEndian
	b[0], b[1] = 'B', 'M'
	le.PutUint32(b[2:], uint32(size))
	le.PutUint32(b[10:], uint32(off))
	le.PutUint32(b[14:], uint32(s.header))
	le.PutUint32(b[18:], uint32(s.w))
	h := int32(s.h)
	if s.topDown {
		h = -h
	}
	le.PutUint32(b[22:], uint32(h))
	le.PutUint16(b[26:], 1)
	le.PutUint16(b[28:], uint16(s.bits))
	le.PutUint32(b[30:], s.compression)
	le.PutUint32(b[34:], uint32(s.stride()*s.h))
	if s.compression == bmpBIBitfields {
		le.PutUint32(b[54:], s.masks[0])
		le.PutUint32(b[58:], s.masks[1])
		le.PutUint32(b[62:], s.masks[2])
		if s.header >= 56 {
			le.PutUint32(b[66:], s.masks[3])
		}
	}
	for i := off; i < off+s.stride()*s.h; i++ {
		b[i] = 0x11
	}
	for i := off + s.stride()*s.h; i < size; i++ {
		b[i] = 0xEE
	}
	return b
}

// testPixel reads the pixel at (x, y), y counted from the top, the way the spec
// says the file is laid out.
func testPixel(s testBMPSpec, data []byte, x, y int) (r, g, b, xb byte) {
	row := s.h - 1 - y
	if s.topDown {
		row = y
	}
	p := data[s.offset()+row*s.stride()+x*(s.bits/8):]
	ri, gi, bi, xi := s.channelBytes()
	r, g, b = p[ri], p[gi], p[bi]
	if xi >= 0 {
		xb = p[xi]
	}
	return r, g, b, xb
}

// testGradient is a w×h RGBA picture where every pixel is different.
func testGradient(w, h int) []byte {
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			out[i], out[i+1], out[i+2], out[i+3] = byte(x*7+y), byte(y*13+x), byte(x^y), 255
		}
	}
	return out
}

func TestBMPParsePatchRoundTrip(t *testing.T) {
	specs := map[string]testBMPSpec{
		"24-bit, padded rows, trailing bytes": {w: 5, h: 3, bits: 24, header: 40, trailing: 2},
		"24-bit top-down":                     {w: 6, h: 4, bits: 24, header: 40, topDown: true},
		"32-bit BI_RGB":                       {w: 5, h: 3, bits: 32, header: 40},
		"32-bit bitfields, 40-byte header":    {w: 4, h: 3, bits: 32, header: 40, compression: bmpBIBitfields, masks: [4]uint32{0x00FF0000, 0x0000FF00, 0x000000FF, 0}},
		"32-bit bitfields, V5 header, RGBA":   {w: 3, h: 5, bits: 32, header: 124, compression: bmpBIBitfields, masks: [4]uint32{0x000000FF, 0x0000FF00, 0x00FF0000, 0xFF000000}},
		"32-bit bitfields, V3 header, ABGR":   {w: 4, h: 2, bits: 32, header: 56, compression: bmpBIBitfields, masks: [4]uint32{0xFF000000, 0x00FF0000, 0x0000FF00, 0x000000FF}},
	}
	for name, s := range specs {
		t.Run(name, func(t *testing.T) {
			stock := testBMP(s)
			keep := append([]byte(nil), stock...)
			info, err := bmpParse(stock)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if info.Width != s.w || info.Height != s.h || info.TopDown != s.topDown || info.BitCount != s.bits {
				t.Fatalf("parsed %+v for %+v", info, s)
			}
			if info.DataOffset != s.offset() || info.RowStride != s.stride() {
				t.Fatalf("offset %d stride %d, want %d %d", info.DataOffset, info.RowStride, s.offset(), s.stride())
			}
			pic := testGradient(s.w, s.h)
			out, err := bmpPatch(stock, info, pic)
			if err != nil {
				t.Fatalf("patch: %v", err)
			}
			if !bytes.Equal(stock, keep) {
				t.Fatal("bmpPatch changed the stock bytes it was given")
			}
			if len(out) != len(stock) {
				t.Fatalf("patched length %d, stock %d", len(out), len(stock))
			}
			if !bytes.Equal(out[:info.DataOffset], stock[:info.DataOffset]) {
				t.Fatal("the header bytes changed")
			}
			if !bytes.Equal(out[info.pixelEnd():], stock[info.pixelEnd():]) {
				t.Fatal("the bytes after the pixel rows changed")
			}
			for y := 0; y < s.h; y++ {
				for x := 0; x < s.w; x++ {
					r, g, b, xb := testPixel(s, out, x, y)
					i := (y*s.w + x) * 4
					if r != pic[i] || g != pic[i+1] || b != pic[i+2] {
						t.Fatalf("pixel (%d,%d) = %d,%d,%d, want %d,%d,%d", x, y, r, g, b, pic[i], pic[i+1], pic[i+2])
					}
					if s.bits == 32 && xb != 0xFF {
						t.Fatalf("pixel (%d,%d) fourth byte %#x, want opaque", x, y, xb)
					}
				}
				// Row padding is zeroed.
				rowStart := s.offset() + y*s.stride()
				for i := rowStart + s.w*s.bits/8; i < rowStart+s.stride(); i++ {
					if out[i] != 0 {
						t.Fatalf("row %d padding byte %d = %#x", y, i-rowStart, out[i])
					}
				}
			}
		})
	}
}

func TestBMPParseRefusals(t *testing.T) {
	good := testBMPSpec{w: 8, h: 4, bits: 24, header: 40}
	cases := map[string]func() []byte{
		"too short": func() []byte { return testBMP(good)[:40] },
		"not a BMP": func() []byte { b := testBMP(good); b[0], b[1] = 0x89, 'P'; return b },
		"OS/2 header": func() []byte {
			b := testBMP(good)
			binary.LittleEndian.PutUint32(b[14:], 12)
			return b
		},
		"two planes": func() []byte {
			b := testBMP(good)
			binary.LittleEndian.PutUint16(b[26:], 2)
			return b
		},
		"8-bit palette": func() []byte { return testBMP(testBMPSpec{w: 8, h: 4, bits: 8, header: 40}) },
		"16-bit":        func() []byte { return testBMP(testBMPSpec{w: 8, h: 4, bits: 16, header: 40}) },
		"RLE8": func() []byte {
			return testBMP(testBMPSpec{w: 8, h: 4, bits: 8, header: 40, compression: 1})
		},
		"24-bit bitfields": func() []byte {
			b := testBMP(good)
			binary.LittleEndian.PutUint32(b[30:], bmpBIBitfields)
			return b
		},
		"masks that are not whole bytes": func() []byte {
			return testBMP(testBMPSpec{w: 4, h: 2, bits: 32, header: 40, compression: bmpBIBitfields, masks: [4]uint32{0x00FFF000, 0x0000FF00, 0x000000FF, 0}})
		},
		"two channels on one byte": func() []byte {
			return testBMP(testBMPSpec{w: 4, h: 2, bits: 32, header: 40, compression: bmpBIBitfields, masks: [4]uint32{0x0000FF00, 0x0000FF00, 0x000000FF, 0}})
		},
		"alpha over a colour byte": func() []byte {
			return testBMP(testBMPSpec{w: 4, h: 2, bits: 32, header: 56, compression: bmpBIBitfields, masks: [4]uint32{0x00FF0000, 0x0000FF00, 0x000000FF, 0x000000FF}})
		},
		"pixel rows cut short": func() []byte { b := testBMP(good); return b[:len(b)-5] },
		"pixel rows over the header": func() []byte {
			b := testBMP(good)
			binary.LittleEndian.PutUint32(b[10:], 20)
			return b
		},
		"zero width": func() []byte {
			b := testBMP(good)
			binary.LittleEndian.PutUint32(b[18:], 0)
			return b
		},
	}
	for name, build := range cases {
		if _, err := bmpParse(build()); err == nil {
			t.Errorf("%s: parsed without complaint", name)
		}
	}
}

func TestBootLogoAcceptable(t *testing.T) {
	ok := testBMP(testBMPSpec{w: 400, h: 300, bits: 24, header: 40})
	if _, err := bootLogoValidate(ok); err != nil {
		t.Fatalf("a 400x300 logo was refused: %v", err)
	}
	refuse := map[string][]byte{
		"portrait":  testBMP(testBMPSpec{w: 300, h: 400, bits: 24, header: 40}),
		"too small": testBMP(testBMPSpec{w: 100, h: 80, bits: 24, header: 40}),
	}
	lying := append([]byte(nil), ok...)
	binary.LittleEndian.PutUint32(lying[2:], 100)
	refuse["size field shorter than the pixels"] = lying
	for name, data := range refuse {
		_, err := bootLogoValidate(data)
		if !errors.Is(err, errBootLogoFormat) {
			t.Errorf("%s: err = %v, want a format refusal", name, err)
		}
	}
	info, err := bmpParse(ok)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootLogoAcceptable(info, bootLogoMaxBytes+1); !errors.Is(err, errBootLogoFormat) {
		t.Errorf("a logo over 8 MiB: err = %v", err)
	}
	// A size field of zero is legal for an uncompressed BMP.
	zero := append([]byte(nil), ok...)
	binary.LittleEndian.PutUint32(zero[2:], 0)
	if _, err := bootLogoValidate(zero); err != nil {
		t.Errorf("a zero size field was refused: %v", err)
	}
}

func TestBMPEncode24(t *testing.T) {
	pic := testGradient(7, 3)
	data := bmpEncode24(pic, 7, 3)
	info, err := bmpParse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.BitCount != 24 || info.TopDown || info.Width != 7 || info.Height != 3 || info.FileSize != len(data) {
		t.Fatalf("encoded %+v", info)
	}
	s := testBMPSpec{w: 7, h: 3, bits: 24, header: 40}
	for y := 0; y < 3; y++ {
		for x := 0; x < 7; x++ {
			r, g, b, _ := testPixel(s, data, x, y)
			i := (y*7 + x) * 4
			if r != pic[i] || g != pic[i+1] || b != pic[i+2] {
				t.Fatalf("pixel (%d,%d) = %d,%d,%d", x, y, r, g, b)
			}
		}
	}
	if bmpEncode24(pic, 0, 3) != nil || bmpEncode24(pic[:10], 7, 3) != nil {
		t.Fatal("an empty or short picture must encode to nil")
	}
}

func TestBootLogoComposePlacesLogoAndWordmark(t *testing.T) {
	const w, h, ww, wh = 1280, 720, 200, 52
	l := bootLogoPlace(w, h, wh)
	side := int(l.Sprite.W)
	logo := image.NewNRGBA(image.Rect(0, 0, side, side))
	for i := 0; i < len(logo.Pix); i += 4 {
		logo.Pix[i], logo.Pix[i+1], logo.Pix[i+2], logo.Pix[i+3] = 10, 200, 30, 255
	}
	word := bytes.Repeat([]byte{255}, ww*wh)
	pic := bootLogoCompose(w, h, logo, word, ww, wh)
	if len(pic) != w*h*4 {
		t.Fatalf("picture is %d bytes", len(pic))
	}
	at := func(x, y int) [4]byte {
		i := (y*w + x) * 4
		return [4]byte{pic[i], pic[i+1], pic[i+2], pic[i+3]}
	}
	sx, sy := int(l.Sprite.X), int(l.Sprite.Y)
	if got := at(sx+side/2, sy+side/2); got != [4]byte{10, 200, 30, 255} {
		t.Errorf("logo centre = %v", got)
	}
	if got := at(sx, sy); got != [4]byte{10, 200, 30, 255} {
		t.Errorf("logo corner = %v", got)
	}
	if got := at(sx-1, sy+side/2); got != [4]byte{0, 0, 0, 255} {
		t.Errorf("left of the logo = %v, want black", got)
	}
	if got := at(sx+side/2, sy+side); got != [4]byte{0, 0, 0, 255} {
		t.Errorf("under the logo = %v, want black", got)
	}
	ink := bootLogoInk
	wx, wy := (w-ww)/2, int(l.WordTop)
	if got := at(wx+ww/2, wy+wh/2); got != [4]byte{ink.R, ink.G, ink.B, 255} {
		t.Errorf("wordmark centre = %v, want the ink", got)
	}
	if got := at(wx-1, wy+wh/2); got != [4]byte{0, 0, 0, 255} {
		t.Errorf("left of the wordmark = %v, want black", got)
	}
	if got := at(0, 0); got != [4]byte{0, 0, 0, 255} {
		t.Errorf("corner = %v, want black", got)
	}
	// Half-covered logo pixels are blended over black.
	for i := 3; i < len(logo.Pix); i += 4 {
		logo.Pix[i] = 128
	}
	pic = bootLogoCompose(w, h, logo, nil, 0, 0)
	got := at(sx+side/2, sy+side/2)
	if got[1] < 98 || got[1] > 103 || got[3] != 255 {
		t.Errorf("half-covered logo pixel = %v, want green ≈ 100 and opaque", got)
	}
}

// ── State, backups and the operations, against a fake store ──────────────

// fakeBootLogo is a store held in memory.
type fakeBootLogo struct {
	data        []byte
	writes      int
	corruptNext bool // the next write lands with its last byte flipped
	panicRead   bool // a read panics, the way a driver bug would
	openErr     error
	onOpen      func(write bool) // runs on every open, before it returns
	opens       int
	closes      int
	writable    int // opens that asked for writing
	where       bootLogoWhere
}

func (f *fakeBootLogo) Where() bootLogoWhere { return f.where }
func (f *fakeBootLogo) Close() error         { f.closes++; return nil }

func (f *fakeBootLogo) Read() ([]byte, error) {
	if f.panicRead {
		panic("fake read")
	}
	return append([]byte(nil), f.data...), nil
}

func (f *fakeBootLogo) WriteInPlace(data []byte) error {
	if len(data) != len(f.data) {
		return errors.New("length changed")
	}
	f.writes++
	f.data = append([]byte(nil), data...)
	if f.corruptNext {
		f.corruptNext = false
		f.data[len(f.data)-1] ^= 0xFF
	}
	return nil
}

var bootLogoTestNow = time.Date(2026, 11, 10, 13, 30, 0, 0, time.UTC)

// testWordmark stands in for the SDL_ttf raster: a solid block.
func testWordmark(int) ([]uint8, int, int, bool) {
	return bytes.Repeat([]byte{255}, 60*22), 60, 22, true
}

// bootLogoResetUI forgets the row's state between tests.
func bootLogoResetUI() {
	u := &bootLogoUI
	u.mu.Lock()
	u.started, u.busy, u.have, u.probe, u.armOp, u.armAt = false, "", false, bootLogoProbe{}, "", time.Time{}
	u.mu.Unlock()
}

// withFakeBootLogo installs a fake store holding stock and points every hook
// at temporary folders; it returns the store, the test's root and the toasts.
func withFakeBootLogo(t *testing.T, stock []byte) (*fakeBootLogo, string, *[]string) {
	t.Helper()
	root := t.TempDir()
	fake := &fakeBootLogo{
		data:  append([]byte(nil), stock...),
		where: bootLogoWhere{Kind: bootLogoKindFile, Device: "/dev/fake0p1", Part: "boot-resource", File: "bootlogo.bmp"},
	}
	toasts := &[]string{}
	oldSupported, oldOpen, oldData, oldRoot := bootLogoSupportedFn, bootLogoOpenFn, bootLogoDataDirFn, bootLogoSDRootFn
	oldWord, oldGo, oldNow, oldToast := bootLogoWordmarkFn, bootLogoGo, bootLogoNow, bootLogoToast
	t.Cleanup(func() {
		bootLogoSupportedFn, bootLogoOpenFn, bootLogoDataDirFn, bootLogoSDRootFn = oldSupported, oldOpen, oldData, oldRoot
		bootLogoWordmarkFn, bootLogoGo, bootLogoNow, bootLogoToast = oldWord, oldGo, oldNow, oldToast
		bootLogoResetUI()
	})
	bootLogoSupportedFn = func() bool { return true }
	bootLogoOpenFn = func(write bool) (bootLogoTarget, error) {
		fake.opens++
		if fake.openErr != nil {
			return nil, fake.openErr
		}
		if write {
			fake.writable++
		}
		if fake.onOpen != nil {
			fake.onOpen(write)
		}
		return fake, nil
	}
	bootLogoDataDirFn = func() (string, error) { return filepath.Join(root, "data", "bootlogo"), nil }
	bootLogoSDRootFn = func() string { return filepath.Join(root, "sd") }
	bootLogoWordmarkFn = testWordmark
	bootLogoGo = func(fn func()) { fn() }
	bootLogoNow = func() time.Time { return bootLogoTestNow }
	bootLogoToast = func(msg string, _ ToastKind) { *toasts = append(*toasts, msg) }
	bootLogoResetUI()
	return fake, root, toasts
}

func testStockLogo() []byte {
	return testBMP(testBMPSpec{w: 400, h: 300, bits: 24, header: 40, trailing: 2})
}

func testArtwork() bootLogoArtwork {
	word, ww, wh, _ := testWordmark(300)
	return bootLogoArtwork{ScreenW: 400, ScreenH: 300, Word: word, WordW: ww, WordH: wh}
}

func TestBootLogoWriteBackupNeverOverwrites(t *testing.T) {
	old := bootLogoNow
	bootLogoNow = func() time.Time { return bootLogoTestNow }
	defer func() { bootLogoNow = old }()
	dir := t.TempDir()
	data := []byte("BM pretend logo")
	path, sum, err := bootLogoWriteBackup(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	if sum != bootLogoSHA(data) || filepath.Base(path) != "before-"+sum[:12]+".bmp" {
		t.Fatalf("backup %s sha %s", path, sum)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
		t.Fatal("backup content differs")
	}
	again, _, err := bootLogoWriteBackup(dir, data)
	if err != nil || again != path {
		t.Fatalf("identical copy not reused: %s, %v", again, err)
	}
	// A different file squatting on the name is left alone.
	if err := os.WriteFile(path, []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	other, _, err := bootLogoWriteBackup(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	if other == path {
		t.Fatal("the squatting file was overwritten")
	}
	if got, _ := os.ReadFile(path); string(got) != "someone else's" {
		t.Fatal("the squatting file changed")
	}
	if got, _ := os.ReadFile(other); !bytes.Equal(got, data) {
		t.Fatal("the new copy differs")
	}
}

func TestBootLogoInstallRestoreRoundTrip(t *testing.T) {
	stock := testStockLogo()
	fake, root, _ := withFakeBootLogo(t, stock)
	crossmix := filepath.Join(root, "sd", "Apps", "BootLogo", "Images")
	if err := os.MkdirAll(crossmix, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := bootLogoRunProbe().Status; got != bootLogoStatusOriginal {
		t.Fatalf("before install: %s", got)
	}
	msg, err := bootLogoInstall(testArtwork())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(msg, "installed") || fake.writes != 1 {
		t.Fatalf("install: %q after %d writes", msg, fake.writes)
	}
	// Every open is closed, and only the write itself had the store writable.
	if fake.opens != fake.closes || fake.writable != 1 {
		t.Fatalf("after install: %d opens, %d closes, %d writable", fake.opens, fake.closes, fake.writable)
	}
	if len(fake.data) != len(stock) || !bytes.Equal(fake.data[:54], stock[:54]) {
		t.Fatal("the install changed the file's length or header")
	}
	info, err := bmpParse(fake.data)
	if err != nil {
		t.Fatal(err)
	}
	// The logo landed inside the sprite's square: not every pixel is black.
	l := bootLogoPlace(400, 300, 22)
	spec := testBMPSpec{w: 400, h: 300, bits: 24, header: 40, trailing: 2}
	lit := 0
	for y := int(l.Sprite.Y); y < int(l.Sprite.Y+l.Sprite.H); y++ {
		for x := int(l.Sprite.X); x < int(l.Sprite.X+l.Sprite.W); x++ {
			if r, g, b, _ := testPixel(spec, fake.data, x, y); r|g|b != 0 {
				lit++
			}
		}
	}
	if lit < int(l.Sprite.W*l.Sprite.H)/4 {
		t.Fatalf("only %d lit pixels in the logo's square", lit)
	}
	if r, g, b, _ := testPixel(spec, fake.data, 2, 2); r|g|b != 0 {
		t.Fatalf("the corner is not black: %d,%d,%d", r, g, b)
	}
	if info.pixelEnd() != len(stock)-2 || fake.data[len(stock)-1] != 0xEE {
		t.Fatal("the trailing bytes were not kept")
	}

	st := bootLogoLoadState()
	if !st.Installed || st.Interrupted || st.OursSHA != bootLogoSHA(fake.data) || st.BackupSHA != bootLogoSHA(stock) {
		t.Fatalf("state after install: %+v", st)
	}
	if st.Original != st.BackupPath || st.Kind != bootLogoKindFile || st.Part != "boot-resource" {
		t.Fatalf("state after install: %+v", st)
	}
	if backup, _ := os.ReadFile(st.BackupPath); !bytes.Equal(backup, stock) {
		t.Fatal("the backup is not the stock logo")
	}
	if !bootLogoStateSaysOurs() {
		t.Fatal("the state does not say ours after an install")
	}
	copyData, err := os.ReadFile(filepath.Join(crossmix, bootLogoCrossMixName))
	if err != nil {
		t.Fatalf("no CrossMix copy: %v", err)
	}
	if ci, err := bmpParse(copyData); err != nil || ci.BitCount != 24 || ci.Width != 400 || ci.Height != 300 {
		t.Fatalf("CrossMix copy %+v, %v", ci, err)
	}
	if got := bootLogoRunProbe().Status; got != bootLogoStatusOurs {
		t.Fatalf("after install: %s", got)
	}

	msg, err = bootLogoInstall(testArtwork())
	if err != nil || !strings.Contains(msg, "already") || fake.writes != 1 || fake.writable != 1 {
		t.Fatalf("second install: %q, %v, %d writes, %d writable opens", msg, err, fake.writes, fake.writable)
	}

	msg, err = bootLogoRestore()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !bytes.Equal(fake.data, stock) || fake.writes != 2 {
		t.Fatalf("restore: %q, %d writes, stock back = %v", msg, fake.writes, bytes.Equal(fake.data, stock))
	}
	if fake.opens != fake.closes || fake.writable != 2 {
		t.Fatalf("after restore: %d opens, %d closes, %d writable", fake.opens, fake.closes, fake.writable)
	}
	if st := bootLogoLoadState(); st.Installed || st.Interrupted {
		t.Fatalf("state after restore: %+v", st)
	}
	if bootLogoStateSaysOurs() {
		t.Fatal("the state still says ours after a restore")
	}
	if got := bootLogoRunProbe().Status; got != bootLogoStatusOriginal {
		t.Fatalf("after restore: %s", got)
	}
	msg, err = bootLogoRestore()
	if err != nil || !strings.Contains(msg, "already") || fake.writes != 2 {
		t.Fatalf("second restore: %q, %v", msg, err)
	}
}

func TestBootLogoInstallRollsBackAWriteThatDoesNotReadBack(t *testing.T) {
	stock := testStockLogo()
	fake, _, _ := withFakeBootLogo(t, stock)
	fake.corruptNext = true
	_, err := bootLogoInstall(testArtwork())
	if err == nil || !strings.Contains(err.Error(), "put back") {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(fake.data, stock) || fake.writes != 2 {
		t.Fatalf("after the rollback: %d writes, stock back = %v", fake.writes, bytes.Equal(fake.data, stock))
	}
	if st := bootLogoLoadState(); st.Installed || st.Interrupted || st.BackupPath == "" {
		t.Fatalf("state after the rollback: %+v", st)
	}
}

func TestBootLogoInstallWritesNothingWithoutABackup(t *testing.T) {
	stock := testStockLogo()
	fake, root, _ := withFakeBootLogo(t, stock)
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("a file, not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	bootLogoDataDirFn = func() (string, error) { return filepath.Join(blocker, "bootlogo"), nil }
	_, err := bootLogoInstall(testArtwork())
	if err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("err = %v", err)
	}
	if fake.writes != 0 || !bytes.Equal(fake.data, stock) {
		t.Fatalf("%d writes without a backup", fake.writes)
	}
}

func TestBootLogoRestoreRefusesAnotherToolsLogo(t *testing.T) {
	stock := testStockLogo()
	fake, _, _ := withFakeBootLogo(t, stock)
	if _, err := bootLogoInstall(testArtwork()); err != nil {
		t.Fatal(err)
	}
	// Another tool swapped in a 32-bit logo: a different length.
	fake.data = testBMP(testBMPSpec{w: 400, h: 300, bits: 32, header: 40})
	writes := fake.writes
	_, err := bootLogoRestore()
	if err == nil || !strings.Contains(err.Error(), "another tool") {
		t.Fatalf("err = %v", err)
	}
	if fake.writes != writes {
		t.Fatal("restore wrote over another tool's logo")
	}
	if got := bootLogoRunProbe().Status; got != bootLogoStatusCustom {
		t.Fatalf("status = %s, want Custom", got)
	}
}

func TestBootLogoInterruptedWriteOffersRestoreOnly(t *testing.T) {
	stock := testStockLogo()
	fake, _, _ := withFakeBootLogo(t, stock)
	if _, err := bootLogoInstall(testArtwork()); err != nil {
		t.Fatal(err)
	}
	// A write cut short: same header and length, neither picture.
	half := append([]byte(nil), fake.data...)
	copy(half[len(half)/2:], stock[len(stock)/2:])
	fake.data = half
	st := bootLogoLoadState()
	st.Interrupted = true
	if err := bootLogoSaveState(st); err != nil {
		t.Fatal(err)
	}
	if got := bootLogoRunProbe().Status; got != bootLogoStatusDamaged {
		t.Fatalf("status = %s, want %s", got, bootLogoStatusDamaged)
	}
	if bootLogoStateSaysOurs() {
		t.Fatal("an interrupted write must not count as ours")
	}
	if _, err := bootLogoInstall(testArtwork()); err == nil || !strings.Contains(err.Error(), "cut short") {
		t.Fatalf("install over a cut-short write: %v", err)
	}
	if _, err := bootLogoRestore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !bytes.Equal(fake.data, stock) {
		t.Fatal("restore did not put the stock logo back")
	}
}

func TestBootLogoClassify(t *testing.T) {
	st := bootLogoState{Installed: true, OursSHA: "ours", BackupSHA: "backup"}
	cases := []struct {
		st   bootLogoState
		sum  string
		want string
	}{
		{bootLogoState{}, "anything", bootLogoStatusOriginal},
		{st, "ours", bootLogoStatusOurs},
		{st, "backup", bootLogoStatusOriginal},
		{st, "else", bootLogoStatusCustom},
		{bootLogoState{Interrupted: true, OursSHA: "ours", BackupSHA: "backup"}, "else", bootLogoStatusDamaged},
		{bootLogoState{Interrupted: true, OursSHA: "ours", BackupSHA: "backup"}, "backup", bootLogoStatusOriginal},
		{bootLogoState{Interrupted: true, OursSHA: "ours", BackupSHA: "backup"}, "ours", bootLogoStatusOurs},
	}
	for i, c := range cases {
		if got := bootLogoClassify(c.st, c.sum); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}

func TestBootLogoRowArmsThenRuns(t *testing.T) {
	stock := testStockLogo()
	fake, _, toasts := withFakeBootLogo(t, stock)
	now := bootLogoTestNow
	bootLogoNow = func() time.Time { return now }

	if !bootLogoRowVisible(nil) {
		t.Fatal("the row is hidden on a supported device")
	}
	if got := bootLogoRowValue(nil); got != bootLogoStatusChecking {
		t.Fatalf("first read-out %q, want %q", got, bootLogoStatusChecking)
	}
	if got := bootLogoRowValue(nil); got != bootLogoStatusOriginal {
		t.Fatalf("after the probe %q", got)
	}
	if d := bootLogoRowDetail(nil); !strings.Contains(d, "installs") {
		t.Fatalf("detail %q", d)
	}

	bootLogoRowOpen(nil)
	if fake.writes != 0 || len(*toasts) == 0 || !strings.HasPrefix((*toasts)[len(*toasts)-1], "Press A again") {
		t.Fatalf("first press: %d writes, toasts %q", fake.writes, *toasts)
	}
	now = now.Add(time.Second)
	bootLogoRowOpen(nil)
	if fake.writes != 1 {
		t.Fatalf("second press: %d writes, toasts %q", fake.writes, *toasts)
	}
	if got := bootLogoRowValue(nil); got != bootLogoStatusOurs {
		t.Fatalf("after install %q, toasts %q", got, *toasts)
	}

	// The arm expires: a press five seconds later arms again instead of
	// restoring.
	now = now.Add(5 * time.Second)
	bootLogoRowOpen(nil)
	if fake.writes != 1 || !strings.Contains((*toasts)[len(*toasts)-1], "put the previous boot logo back") {
		t.Fatalf("expired arm: %d writes, toasts %q", fake.writes, *toasts)
	}
	now = now.Add(5 * time.Second)
	bootLogoRowOpen(nil)
	if fake.writes != 1 {
		t.Fatal("a press after the arm window expired restored the logo")
	}
	now = now.Add(time.Second)
	bootLogoRowOpen(nil)
	if fake.writes != 2 || !bytes.Equal(fake.data, stock) {
		t.Fatalf("restore press: %d writes", fake.writes)
	}
	if got := bootLogoRowValue(nil); got != bootLogoStatusOriginal {
		t.Fatalf("after restore %q", got)
	}
}

func TestBootLogoRowUnavailable(t *testing.T) {
	fake, _, toasts := withFakeBootLogo(t, testStockLogo())
	fake.openErr = errors.Join(errBootLogoNoStore, errors.New("no partition named boot-resource"))
	bootLogoRowValue(nil)
	if got := bootLogoRowValue(nil); got != bootLogoStatusUnavailable {
		t.Fatalf("read-out %q", got)
	}
	want := bootLogoSentence(errBootLogoNoStore.Error())
	if d := bootLogoRowDetail(nil); d != want {
		t.Fatalf("detail %q, want %q", d, want)
	}
	bootLogoRowOpen(nil)
	if fake.writes != 0 || len(*toasts) == 0 || (*toasts)[len(*toasts)-1] != want {
		t.Fatalf("press on an unavailable row: toasts %q", *toasts)
	}

	bootLogoSupportedFn = func() bool { return false }
	if bootLogoRowVisible(nil) || bootLogoStateSaysOurs() {
		t.Fatal("an unsupported device shows the row or claims the logo")
	}
	if p := bootLogoRunProbe(); p.Status != bootLogoStatusUnavailable {
		t.Fatalf("probe on an unsupported device: %+v", p)
	}
}

// A record that exists but cannot be read stops every write: it is where the
// previous picture is kept and whether a write was cut short.
func TestBootLogoStrictStateStopsWrites(t *testing.T) {
	stock := testStockLogo()
	fake, root, _ := withFakeBootLogo(t, stock)
	dir := filepath.Join(root, "data", "bootlogo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	for name, body := range map[string]string{
		"damaged":            "{not json",
		"another version":    `{"version": 99, "installed": true, "oursSha256": "abc"}`,
		"damaged with a bak": "\x00\x01",
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(statePath, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(statePath + ".bak")
			if name == "damaged with a bak" {
				if err := os.WriteFile(statePath+".bak", []byte("also {broken"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := bootLogoInstall(testArtwork()); err == nil || !strings.Contains(err.Error(), "nothing was written") {
				t.Fatalf("install: %v", err)
			}
			if _, err := bootLogoRestore(); err == nil || !strings.Contains(err.Error(), "nothing was written") {
				t.Fatalf("restore: %v", err)
			}
			if fake.writes != 0 || fake.writable != 0 || !bytes.Equal(fake.data, stock) {
				t.Fatalf("%d writes, %d writable opens", fake.writes, fake.writable)
			}
			if bootLogoStateSaysOurs() {
				t.Fatal("an unreadable record claimed the logo")
			}
		})
	}
	// A record someone removed is a first install again.
	_ = os.Remove(statePath)
	if _, err := bootLogoInstall(testArtwork()); err != nil {
		t.Fatalf("install without a record: %v", err)
	}
}

// The logo is read on a read-only open and must still be the same when the
// store is opened for the write; otherwise nothing is written.
func TestBootLogoInstallRefusesALogoThatChangedBeforeTheWrite(t *testing.T) {
	stock := testStockLogo()
	fake, _, _ := withFakeBootLogo(t, stock)
	changed := append([]byte(nil), stock...)
	changed[len(changed)-10] ^= 0x55
	fake.onOpen = func(write bool) {
		if write {
			fake.data = append([]byte(nil), changed...)
		}
	}
	_, err := bootLogoInstall(testArtwork())
	if !errors.Is(err, errBootLogoChanged) {
		t.Fatalf("err = %v, want errBootLogoChanged", err)
	}
	if fake.writes != 0 || !bytes.Equal(fake.data, changed) {
		t.Fatalf("%d writes over a logo that changed", fake.writes)
	}
	if st := bootLogoLoadState(); st.Installed || st.Interrupted {
		t.Fatalf("state after the refusal: %+v", st)
	}
	if fake.opens != fake.closes {
		t.Fatalf("%d opens, %d closes", fake.opens, fake.closes)
	}
}

// Restore writes back only where the copy came from.
func TestBootLogoRestoreRefusesAnotherStore(t *testing.T) {
	stock := testStockLogo()
	fake, _, _ := withFakeBootLogo(t, stock)
	if _, err := bootLogoInstall(testArtwork()); err != nil {
		t.Fatal(err)
	}
	writes := fake.writes
	fake.where.Part = "bootloader"
	_, err := bootLogoRestore()
	if err == nil || !strings.Contains(err.Error(), "came from") {
		t.Fatalf("err = %v", err)
	}
	fake.where.Part = "boot-resource"
	fake.where.Kind = bootLogoKindRaw
	if _, err := bootLogoRestore(); err == nil || !strings.Contains(err.Error(), "came from") {
		t.Fatalf("another kind of store: %v", err)
	}
	if fake.writes != writes {
		t.Fatal("restore wrote to another store")
	}
	// Nor is a copy from another model of handheld written back.
	fake.where.Kind = bootLogoKindFile
	st := bootLogoLoadState()
	st.Model = "some-other-handheld"
	if err := bootLogoSaveState(st); err != nil {
		t.Fatal(err)
	}
	if _, err := bootLogoRestore(); err == nil || !strings.Contains(err.Error(), "some-other-handheld") {
		t.Fatalf("another model: %v", err)
	}
	if fake.writes != writes {
		t.Fatal("restore wrote another model's logo")
	}
}

// A probe that finds JukaHub's logo replaced stops the record saying ours.
func TestBootLogoProbeClearsAReplacedInstall(t *testing.T) {
	stock := testStockLogo()
	fake, _, _ := withFakeBootLogo(t, stock)
	if _, err := bootLogoInstall(testArtwork()); err != nil {
		t.Fatal(err)
	}
	if !bootLogoStateSaysOurs() {
		t.Fatal("not ours after the install")
	}
	other := append([]byte(nil), stock...)
	other[len(other)/2] ^= 0xFF
	fake.data = other
	if got := bootLogoRunProbe().Status; got != bootLogoStatusCustom {
		t.Fatalf("status %s, want Custom", got)
	}
	if st := bootLogoLoadState(); st.Installed {
		t.Fatalf("the record still says installed: %+v", st)
	}
	if bootLogoStateSaysOurs() {
		t.Fatal("still ours after another tool replaced the logo")
	}
}

// A panic in the worker becomes an error toast and frees the row.
func TestBootLogoWorkerPanicIsReported(t *testing.T) {
	stock := testStockLogo()
	fake, _, toasts := withFakeBootLogo(t, stock)
	now := bootLogoTestNow
	bootLogoNow = func() time.Time { return now }
	bootLogoRowValue(nil)
	if got := bootLogoRowValue(nil); got != bootLogoStatusOriginal {
		t.Fatalf("read-out %q", got)
	}
	bootLogoRowOpen(nil)
	fake.panicRead = true
	now = now.Add(time.Second)
	bootLogoRowOpen(nil)
	if fake.writes != 0 {
		t.Fatalf("%d writes", fake.writes)
	}
	last := (*toasts)[len(*toasts)-1]
	if !strings.Contains(last, "stopped unexpectedly") {
		t.Fatalf("last toast %q", last)
	}
	if _, busy, _, p := bootLogoSnapshot(); busy != "" || p.Status != bootLogoStatusUnavailable {
		t.Fatalf("after the panic: busy %q, status %q", busy, p.Status)
	}
	if fake.opens != fake.closes {
		t.Fatalf("a panic leaked an open store: %d opens, %d closes", fake.opens, fake.closes)
	}
}

func TestBootLogoWaitIdle(t *testing.T) {
	bootLogoResetUI()
	defer bootLogoResetUI()
	if !bootLogoWaitIdle(time.Millisecond) {
		t.Fatal("an idle row was not idle")
	}
	if !bootLogoBegin(bootLogoOpInstall) {
		t.Fatal("could not claim the row")
	}
	start := time.Now()
	if bootLogoWaitIdle(60 * time.Millisecond) {
		t.Fatal("a running install was reported finished")
	}
	if waited := time.Since(start); waited < 50*time.Millisecond {
		t.Fatalf("gave up after %v", waited)
	}
	bootLogoFinish(bootLogoProbe{})
	if !bootLogoWaitIdle(time.Millisecond) {
		t.Fatal("not idle after the install finished")
	}
}
