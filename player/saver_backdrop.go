package main

// saver_backdrop.go — the idle screens' full-screen pictures, decoded and
// scaled off the render thread.
//
// The lock-screen picture and the dock's photo slideshow used to be decoded at
// full resolution on the render thread. A 12-megapixel camera JPEG stalled the
// frame for over a second, became a 48 MB texture, and a photo larger than the
// biggest texture the Smart Pro's GPU accepts failed to upload at all — which
// left the default idle screen showing as if no picture had been chosen.
//
// Pictures now take one path. A worker decodes the file (the Go decoders for
// JPEG, PNG and GIF; SDL_image for the formats Go lacks), applies the camera's
// EXIF orientation, crops it to the screen's aspect ratio (cover, never
// letterbox) and averages it down to exactly the screen size. Transparency is
// composited over the idle screen's dark base, and the legibility scrim the
// clock, chips and hints need is baked into the same pixels, so a frame costs
// one opaque copy and nothing else. The render thread only ever uploads a
// finished rendition, once.

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

const (
	// saverPicSampleCap bounds how many source pixels per axis one screen
	// pixel averages. A 4000-pixel photo shrunk to 1280 covers about three
	// source pixels per axis, so four keeps the average exact for ordinary
	// photos and merely sampled for 50-megapixel ones.
	saverPicSampleCap = 4

	// saverPicCacheMax is how many finished renditions are kept: the lock
	// picture plus the slideshow's current and next photo. Each is one
	// screen of RGBA, 3.7 MB at 720p.
	saverPicCacheMax = 3

	// saverPicMaxWorkers bounds the decodes running at once. A slideshow that
	// is skipped quickly must not stack up full-size decodes in RAM.
	saverPicMaxWorkers = 2

	// saverExifScanBytes is how much of a JPEG is read for its EXIF block. The
	// APP1 segment sits at the very start of the file and cannot exceed 64 KB.
	saverExifScanBytes = 128 << 10
)

// saverBackdropBase is the colour transparency is composited over: the idle
// screen's own dark base, so a PNG with an alpha channel looks the way it does
// over the saver instead of over black or white.
var saverBackdropBase = color.RGBA{R: 6, G: 8, B: 14, A: 255}

// saverScrimInk is the tint of the legibility scrim baked into every picture.
var saverScrimInk = color.RGBA{R: 4, G: 6, B: 12, A: 255}

// saverScrimAlpha is the scrim's strength (0..1) at a fraction t (0 at the top,
// 1 at the bottom) of the screen: a light veil over the whole picture, deeper
// behind the clock and status chips at the top and behind the controls and
// hints at the bottom, so a bright photo never swallows the text drawn there.
func saverScrimAlpha(t float64) float64 {
	a := 0.30
	if t < 0.36 {
		k := 1 - t/0.36
		a += 0.36 * k * k
	}
	if t > 0.58 {
		k := (t - 0.58) / 0.42
		a += 0.42 * k * k
	}
	if a > 0.82 {
		a = 0.82
	}
	return a
}

// ── Renditions ────────────────────────────────────────────────────────────

// saverPicKey names one rendition: one version of a file, scaled for one
// canvas. The stamp (modification time and size) makes a replaced file a new
// key, so an edited picture is never served from a stale rendition.
type saverPicKey struct {
	path  string
	stamp string
	w, h  int
}

type saverPicEntry struct {
	key    saverPicKey
	pixels *image.RGBA
	err    error
	done   bool
	used   uint64
}

// saverPics is the rendition cache shared by the lock picture and the dock's
// slideshow. Workers write their entry under the lock; the render thread only
// reads finished entries.
var saverPics struct {
	mu      sync.Mutex
	entries []*saverPicEntry
	clock   uint64
}

// saverPicRenderFn produces one rendition. It is a variable so a test can
// count or delay decodes without touching the cache logic.
var saverPicRenderFn = saverPicRender

// saverPicRequest returns the rendition for key. done is false while a worker
// is still producing it — the first request starts that worker — and a
// finished request carries either the pixels or the reason there are none.
func saverPicRequest(key saverPicKey) (*image.RGBA, bool, error) {
	if key.path == "" || key.w <= 0 || key.h <= 0 {
		return nil, true, errors.New("no picture to draw")
	}
	saverPics.mu.Lock()
	saverPics.clock++
	for _, e := range saverPics.entries {
		if e.key == key {
			e.used = saverPics.clock
			pix, done, err := e.pixels, e.done, e.err
			saverPics.mu.Unlock()
			return pix, done, err
		}
	}
	running := 0
	for _, e := range saverPics.entries {
		if !e.done {
			running++
		}
	}
	if running >= saverPicMaxWorkers {
		// Ask again on a later frame, when a worker has finished.
		saverPics.mu.Unlock()
		return nil, false, nil
	}
	entry := &saverPicEntry{key: key, used: saverPics.clock}
	saverPics.entries = append(saverPics.entries, entry)
	saverPicTrimLocked()
	saverPics.mu.Unlock()

	go func() {
		pix, err := saverPicRenderSafe(key)
		saverPics.mu.Lock()
		entry.pixels, entry.err, entry.done = pix, err, true
		saverPics.mu.Unlock()
	}()
	return nil, false, nil
}

// saverPicRenderSafe runs the decode with a recover: a malformed file must
// cost its own picture, not the process.
func saverPicRenderSafe(key saverPicKey) (pix *image.RGBA, err error) {
	defer func() {
		if r := recover(); r != nil {
			pix, err = nil, fmt.Errorf("decoding %s failed: %v", key.path, r)
		}
	}()
	return saverPicRenderFn(key.path, key.w, key.h)
}

// saverPicTrimLocked drops the least recently used finished renditions beyond
// the cap. Entries still being produced are never dropped: their worker holds
// a pointer to them.
func saverPicTrimLocked() {
	for len(saverPics.entries) > saverPicCacheMax {
		oldest := -1
		for i, e := range saverPics.entries {
			if !e.done {
				continue
			}
			if oldest < 0 || e.used < saverPics.entries[oldest].used {
				oldest = i
			}
		}
		if oldest < 0 {
			return
		}
		saverPics.entries = append(saverPics.entries[:oldest], saverPics.entries[oldest+1:]...)
	}
}

// saverPicForget drops every finished rendition except those of keep (the
// lock picture, which the next idle screen wants back at once). The dock calls
// it on the way out, so slideshow photos do not sit in memory behind the UI.
func saverPicForget(keep string) {
	saverPics.mu.Lock()
	defer saverPics.mu.Unlock()
	kept := saverPics.entries[:0]
	for _, e := range saverPics.entries {
		if !e.done || (keep != "" && e.key.path == keep) {
			kept = append(kept, e)
		}
	}
	for i := len(kept); i < len(saverPics.entries); i++ {
		saverPics.entries[i] = nil
	}
	saverPics.entries = kept
}

// ── Decoding ─────────────────────────────────────────────────────────────

// saverPicRender decodes path and returns it as a w×h rendition: oriented,
// cover-cropped, averaged down, composited over the dark base and scrimmed.
func saverPicRender(path string, w, h int) (*image.RGBA, error) {
	if w <= 0 || h <= 0 {
		return nil, errors.New("the screen has no size yet")
	}
	src, orient, err := saverPicDecode(path)
	if err != nil {
		return nil, err
	}
	out := saverPicCover(src, orient, w, h, saverBackdropBase)
	saverBakeScrim(out)
	return out, nil
}

// saverPicMaxPixels is the largest picture the idle screen decodes. A JPEG
// decodes to planar YCbCr (1.5 to 3 bytes a pixel), so it may be larger than
// the 4-bytes-a-pixel formats the shared limit is sized for.
func saverPicMaxPixels(format string) int64 {
	handheld := IsTrimuiDevice() || P().IsHandheld()
	if format == "jpeg" {
		if handheld {
			return 40 << 20
		}
		return 160 << 20
	}
	return imageDecodeMaxPixels()
}

// saverPicProbe reads only the header of a picture, so Files can refuse one
// the idle screen could not show — a renamed text file, a truncated download,
// a huge panorama — with a reason, instead of the idle screen quietly staying
// on its default look. BMP and WebP have no Go decoder (SDL_image reads them
// when the picture is drawn), so their size comes from saverPicHeaderSize.
func saverPicProbe(path string) error {
	name := filepath.Base(path)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot open %s", name)
	}
	defer f.Close()
	br := bufio.NewReader(f)
	cfg, format, err := image.DecodeConfig(br)
	if err != nil {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".bmp", ".webp":
			if errors.Is(err, image.ErrFormat) {
				// SDL_image decodes these; their header still says how big
				// the picture is, so an oversized one is refused here too.
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					return fmt.Errorf("cannot read %s", name)
				}
				head := make([]byte, 64)
				n, _ := io.ReadFull(f, head)
				w, h, ok := saverPicHeaderSize(head[:n])
				if !ok {
					return fmt.Errorf("%s is not a readable picture", name)
				}
				if int64(w)*int64(h) > saverPicMaxPixels("") {
					return fmt.Errorf("%s is %d×%d, too large for the lock screen", name, w, h)
				}
				return nil
			}
		}
		return fmt.Errorf("%s is not a readable picture", name)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("%s has no pixels", name)
	}
	if int64(cfg.Width)*int64(cfg.Height) > saverPicMaxPixels(format) {
		return fmt.Errorf("%s is %d×%d, too large for the lock screen", name, cfg.Width, cfg.Height)
	}
	return nil
}

// saverPicDecode reads a picture and its EXIF orientation (1 when it has none).
func saverPicDecode(path string) (image.Image, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 1, err
	}
	defer f.Close()

	head := make([]byte, saverExifScanBytes)
	n, _ := io.ReadFull(f, head)
	orient := saverJPEGOrientation(head[:n])

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, 1, err
	}
	br := bufio.NewReaderSize(f, 64<<10)
	cfg, format, cfgErr := image.DecodeConfig(br)
	if cfgErr != nil {
		if errors.Is(cfgErr, image.ErrFormat) {
			// Not a format the Go decoders know (BMP, WebP): SDL_image reads
			// those, and EXIF orientation only exists for JPEG anyway. Their
			// header is checked first, so an oversized picture is refused
			// before SDL allocates it, and the decode takes the shared permit
			// like every other one.
			if w, h, ok := saverPicHeaderSize(head[:n]); ok && int64(w)*int64(h) > saverPicMaxPixels("") {
				return nil, 1, fmt.Errorf("the picture is %dx%d, too large for the idle screen", w, h)
			}
			if !imageDecodeSem.Acquire(context.Background(), 1) {
				return nil, 1, errors.New("decode cancelled")
			}
			defer imageDecodeSem.Release(1)
			pic, sdlErr := saverPicDecodeSDL(path)
			return pic, 1, sdlErr
		}
		return nil, 1, cfgErr
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, 1, fmt.Errorf("the picture reports no size")
	}
	if int64(cfg.Width)*int64(cfg.Height) > saverPicMaxPixels(format) {
		return nil, 1, fmt.Errorf("the picture is %dx%d, too large for the idle screen", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, 1, err
	}
	br.Reset(f)
	// The shared decode permit keeps this decode from running alongside a
	// burst of chat images or thumbnails, which is what bounds peak RAM.
	if !imageDecodeSem.Acquire(context.Background(), 1) {
		return nil, 1, errors.New("decode cancelled")
	}
	defer imageDecodeSem.Release(1)
	pic, _, err := image.Decode(br)
	if err != nil {
		return nil, 1, err
	}
	return pic, orient, nil
}

// saverPicDecodeSDL decodes a format the Go decoders lack through SDL_image
// and copies the pixels out, so the SDL surface is freed before scaling.
// Decoding into a surface touches no renderer, so it is safe off the render
// thread.
func saverPicDecodeSDL(path string) (image.Image, error) {
	surface, err := img.Load(path)
	if err != nil {
		return nil, err
	}
	defer surface.Free()
	if surface.W <= 0 || surface.H <= 0 {
		return nil, errors.New("the picture has no pixels")
	}
	if int64(surface.W)*int64(surface.H) > saverPicMaxPixels("") {
		return nil, fmt.Errorf("the picture is %dx%d, too large for the idle screen", surface.W, surface.H)
	}
	rgba, err := surface.ConvertFormat(uint32(sdl.PIXELFORMAT_RGBA32), 0)
	if err != nil {
		return nil, err
	}
	defer rgba.Free()
	w, h, pitch := int(rgba.W), int(rgba.H), int(rgba.Pitch)
	src := rgba.Pixels()
	if pitch < w*4 || len(src) < pitch*h {
		return nil, errors.New("the picture's pixel buffer is short")
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		copy(out.Pix[y*out.Stride:y*out.Stride+w*4], src[y*pitch:y*pitch+w*4])
	}
	return out, nil
}

// saverPicHeaderSize reads the pixel size from a BMP or WebP header — the two
// formats SDL_image decodes for the idle screen — so an oversized one can be
// refused before a pixel is allocated. ok is false for anything else.
func saverPicHeaderSize(head []byte) (int, int, bool) {
	switch {
	case len(head) >= 26 && head[0] == 'B' && head[1] == 'M':
		var w, h int
		if binary.LittleEndian.Uint32(head[14:18]) == 12 {
			// The old OS/2 core header stores 16-bit sizes.
			w = int(binary.LittleEndian.Uint16(head[18:20]))
			h = int(binary.LittleEndian.Uint16(head[20:22]))
		} else {
			w = int(int32(binary.LittleEndian.Uint32(head[18:22])))
			h = int(int32(binary.LittleEndian.Uint32(head[22:26])))
		}
		if h < 0 {
			h = -h // a top-down bitmap stores a negative height
		}
		return w, h, w > 0 && h > 0
	case len(head) >= 30 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		switch string(head[12:16]) {
		case "VP8 ":
			// Lossy: a 3-byte frame tag, the 9d 01 2a start code, then two
			// 14-bit sizes.
			if head[23] != 0x9d || head[24] != 0x01 || head[25] != 0x2a {
				return 0, 0, false
			}
			w := int(binary.LittleEndian.Uint16(head[26:28]) & 0x3fff)
			h := int(binary.LittleEndian.Uint16(head[28:30]) & 0x3fff)
			return w, h, w > 0 && h > 0
		case "VP8L":
			// Lossless: the 0x2f signature, then 14 bits each of width-1 and
			// height-1.
			if head[20] != 0x2f {
				return 0, 0, false
			}
			bits := binary.LittleEndian.Uint32(head[21:25])
			return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, true
		case "VP8X":
			// Extended: 24-bit canvas width-1 and height-1.
			w := int(uint32(head[24])|uint32(head[25])<<8|uint32(head[26])<<16) + 1
			h := int(uint32(head[27])|uint32(head[28])<<8|uint32(head[29])<<16) + 1
			return w, h, true
		}
	}
	return 0, 0, false
}

// ── EXIF orientation ─────────────────────────────────────────────────────

// saverJPEGOrientation finds the EXIF orientation (1..8) at the start of a
// JPEG, or 1 when the file is not a JPEG or carries none. Phones store a
// portrait photo as landscape pixels plus this tag, so without it every
// portrait lock screen came out lying on its side.
func saverJPEGOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		switch {
		case marker == 0xFF:
			i++ // fill byte before a marker
			continue
		case marker == 0xD9 || marker == 0xDA:
			return 1 // end of image or start of scan: no EXIF ahead
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7):
			i += 2 // markers without a length
			continue
		}
		size := int(data[i+2])<<8 | int(data[i+3])
		if size < 2 {
			return 1
		}
		start, end := i+4, i+2+size
		if end > len(data) {
			return 1
		}
		if marker == 0xE1 && end-start >= 6 && string(data[start:start+6]) == "Exif\x00\x00" {
			return saverExifOrientation(data[start+6 : end])
		}
		i = end
	}
	return 1
}

// saverExifOrientation reads tag 0x0112 from the first IFD of a TIFF block.
func saverExifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 1
	}
	ifd := int(order.Uint32(tiff[4:8]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return 1
	}
	count := int(order.Uint16(tiff[ifd : ifd+2]))
	for k := 0; k < count; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[e:e+2]) != 0x0112 {
			continue
		}
		v := 0
		switch order.Uint16(tiff[e+2 : e+4]) {
		case 3: // SHORT, left-justified in the value field
			v = int(order.Uint16(tiff[e+8 : e+10]))
		case 4: // LONG: not what the standard says, but seen in the wild
			v = int(order.Uint32(tiff[e+8 : e+12]))
		}
		if v >= 1 && v <= 8 {
			return v
		}
		return 1
	}
	return 1
}

// saverOrient maps a display pixel (u, v) of an oriented picture back to the
// stored pixel: x = ax*u + bx*v + cx, y = ay*u + by*v + cy.
type saverOrient struct {
	ax, bx, cx int
	ay, by, cy int
}

// saverOrientFor returns the mapping for an EXIF orientation of a picture
// stored at sw×sh, and the size it is displayed at.
func saverOrientFor(orient, sw, sh int) (saverOrient, int, int) {
	switch orient {
	case 2: // mirrored left-right
		return saverOrient{ax: -1, cx: sw - 1, by: 1}, sw, sh
	case 3: // upside down
		return saverOrient{ax: -1, cx: sw - 1, by: -1, cy: sh - 1}, sw, sh
	case 4: // mirrored top-bottom
		return saverOrient{ax: 1, by: -1, cy: sh - 1}, sw, sh
	case 5: // transposed
		return saverOrient{bx: 1, ay: 1}, sh, sw
	case 6: // needs a quarter turn clockwise (a phone held upright)
		return saverOrient{bx: 1, ay: -1, cy: sh - 1}, sh, sw
	case 7: // transverse
		return saverOrient{bx: -1, cx: sw - 1, ay: -1, cy: sh - 1}, sh, sw
	case 8: // needs a quarter turn anticlockwise
		return saverOrient{bx: -1, cx: sw - 1, ay: 1}, sh, sw
	}
	return saverOrient{ax: 1, by: 1}, sw, sh
}

// ── Scaling ──────────────────────────────────────────────────────────────

// saverCoverCrop is the region of a srcW×srcH picture that fills a dstW×dstH
// screen without distortion: the full height of a wide picture or the full
// width of a tall one, centred. Cover, never letterbox.
func saverCoverCrop(srcW, srcH, dstW, dstH int) (x0, y0, cw, ch float64) {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return 0, 0, float64(srcW), float64(srcH)
	}
	srcAspect := float64(srcW) / float64(srcH)
	dstAspect := float64(dstW) / float64(dstH)
	if srcAspect > dstAspect {
		ch = float64(srcH)
		cw = ch * dstAspect
		return (float64(srcW) - cw) / 2, 0, cw, ch
	}
	cw = float64(srcW)
	ch = cw / dstAspect
	return 0, (float64(srcH) - ch) / 2, cw, ch
}

// saverPixelReader returns a reader for src's pixels at absolute coordinates,
// as premultiplied 8-bit channels. The common decoder outputs get direct
// access; anything else goes through At.
func saverPixelReader(src image.Image) func(x, y int) (uint32, uint32, uint32, uint32) {
	switch p := src.(type) {
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			yi, ci := p.YOffset(x, y), p.COffset(x, y)
			r, g, b := color.YCbCrToRGB(p.Y[yi], p.Cb[ci], p.Cr[ci])
			return uint32(r), uint32(g), uint32(b), 255
		}
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			i := p.PixOffset(x, y)
			return uint32(p.Pix[i]), uint32(p.Pix[i+1]), uint32(p.Pix[i+2]), uint32(p.Pix[i+3])
		}
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			i := p.PixOffset(x, y)
			a := uint32(p.Pix[i+3])
			return uint32(p.Pix[i]) * a / 255, uint32(p.Pix[i+1]) * a / 255, uint32(p.Pix[i+2]) * a / 255, a
		}
	case *image.Gray:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			v := uint32(p.Pix[p.PixOffset(x, y)])
			return v, v, v, 255
		}
	}
	return func(x, y int) (uint32, uint32, uint32, uint32) {
		r, g, b, a := src.At(x, y).RGBA()
		return r >> 8, g >> 8, b >> 8, a >> 8
	}
}

// saverPicCover renders src (with its EXIF orientation) into a w×h picture
// that covers the screen: averaged down when the source is larger, bilinear
// when it is smaller, transparency composited over base.
func saverPicCover(src image.Image, orient, w, h int, base color.RGBA) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return out
	}
	if sw <= 0 || sh <= 0 {
		saverFillRGBA(out, base)
		return out
	}
	m, dw, dh := saverOrientFor(orient, sw, sh)
	cx, cy, cw, _ := saverCoverCrop(dw, dh, w, h)
	scale := cw / float64(w) // display pixels per screen pixel, both axes
	read := saverPixelReader(src)
	minX, minY := bounds.Min.X, bounds.Min.Y
	baseR, baseG, baseB := uint32(base.R), uint32(base.G), uint32(base.B)
	// fetch reads display pixel (u, v) composited over the base.
	fetch := func(u, v int) (uint32, uint32, uint32) {
		x := m.ax*u + m.bx*v + m.cx
		y := m.ay*u + m.by*v + m.cy
		r, g, b, a := read(minX+x, minY+y)
		inv := 255 - a
		return r + baseR*inv/255, g + baseG*inv/255, b + baseB*inv/255
	}
	clampIdx := func(v, n int) int {
		if v < 0 {
			return 0
		}
		if v >= n {
			return n - 1
		}
		return v
	}

	if scale <= 1 {
		// Upscaling: bilinear between the four nearest source pixels, with
		// 8-bit fixed-point weights.
		u0s, u1s, wus := make([]int, w), make([]int, w), make([]uint32, w)
		for ox := 0; ox < w; ox++ {
			fu := cx + (float64(ox)+0.5)*scale - 0.5
			fl := math.Floor(fu)
			u0s[ox] = clampIdx(int(fl), dw)
			u1s[ox] = clampIdx(int(fl)+1, dw)
			wus[ox] = uint32(math.Round((fu - fl) * 256))
		}
		for oy := 0; oy < h; oy++ {
			fv := cy + (float64(oy)+0.5)*scale - 0.5
			fl := math.Floor(fv)
			v0, v1 := clampIdx(int(fl), dh), clampIdx(int(fl)+1, dh)
			wv := uint32(math.Round((fv - fl) * 256))
			row := out.Pix[oy*out.Stride:]
			for ox := 0; ox < w; ox++ {
				wu := wus[ox]
				r00, g00, b00 := fetch(u0s[ox], v0)
				r10, g10, b10 := fetch(u1s[ox], v0)
				r01, g01, b01 := fetch(u0s[ox], v1)
				r11, g11, b11 := fetch(u1s[ox], v1)
				rt := (r00*(256-wu) + r10*wu) >> 8
				gt := (g00*(256-wu) + g10*wu) >> 8
				bt := (b00*(256-wu) + b10*wu) >> 8
				rb := (r01*(256-wu) + r11*wu) >> 8
				gb := (g01*(256-wu) + g11*wu) >> 8
				bb := (b01*(256-wu) + b11*wu) >> 8
				o := ox * 4
				row[o] = uint8((rt*(256-wv) + rb*wv) >> 8)
				row[o+1] = uint8((gt*(256-wv) + gb*wv) >> 8)
				row[o+2] = uint8((bt*(256-wv) + bb*wv) >> 8)
				row[o+3] = 255
			}
		}
		return out
	}

	// Downscaling: average an n×n grid spread across each screen pixel's
	// footprint in the source.
	n := int(math.Ceil(scale))
	if n > saverPicSampleCap {
		n = saverPicSampleCap
	}
	us := make([]int, w*n)
	for ox := 0; ox < w; ox++ {
		for i := 0; i < n; i++ {
			us[ox*n+i] = clampIdx(int(cx+(float64(ox)+(float64(i)+0.5)/float64(n))*scale), dw)
		}
	}
	vs := make([]int, n)
	count := uint32(n * n)
	for oy := 0; oy < h; oy++ {
		for j := 0; j < n; j++ {
			vs[j] = clampIdx(int(cy+(float64(oy)+(float64(j)+0.5)/float64(n))*scale), dh)
		}
		row := out.Pix[oy*out.Stride:]
		for ox := 0; ox < w; ox++ {
			var sr, sg, sb uint32
			for _, v := range vs {
				for i := 0; i < n; i++ {
					r, g, b := fetch(us[ox*n+i], v)
					sr += r
					sg += g
					sb += b
				}
			}
			o := ox * 4
			row[o] = uint8(sr / count)
			row[o+1] = uint8(sg / count)
			row[o+2] = uint8(sb / count)
			row[o+3] = 255
		}
	}
	return out
}

// saverFillRGBA paints every pixel of dst with c.
func saverFillRGBA(dst *image.RGBA, c color.RGBA) {
	for i := 0; i+3 < len(dst.Pix); i += 4 {
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = c.R, c.G, c.B, 255
	}
}

// saverBakeScrim darkens a rendition by the scrim profile, row by row.
func saverBakeScrim(dst *image.RGBA) {
	h := dst.Bounds().Dy()
	w := dst.Bounds().Dx()
	if h <= 0 || w <= 0 {
		return
	}
	for y := 0; y < h; y++ {
		a := uint32(math.Round(saverScrimAlpha((float64(y)+0.5)/float64(h)) * 256))
		if a > 256 {
			a = 256
		}
		keep := 256 - a
		tr, tg, tb := uint32(saverScrimInk.R)*a, uint32(saverScrimInk.G)*a, uint32(saverScrimInk.B)*a
		row := dst.Pix[y*dst.Stride : y*dst.Stride+w*4]
		for o := 0; o+3 < len(row); o += 4 {
			row[o] = uint8((uint32(row[o])*keep + tr) >> 8)
			row[o+1] = uint8((uint32(row[o+1])*keep + tg) >> 8)
			row[o+2] = uint8((uint32(row[o+2])*keep + tb) >> 8)
		}
	}
}

// ── Upload ───────────────────────────────────────────────────────────────

// saverPicTexture uploads a finished rendition as one static texture. It must
// run on the render thread; the texture belongs to renderer.
func saverPicTexture(renderer *sdl.Renderer, pix *image.RGBA) (*sdl.Texture, error) {
	if renderer == nil || pix == nil || len(pix.Pix) == 0 {
		return nil, errors.New("nothing to upload")
	}
	b := pix.Bounds()
	tex, err := renderer.CreateTexture(uint32(sdl.PIXELFORMAT_RGBA32), sdl.TEXTUREACCESS_STATIC, int32(b.Dx()), int32(b.Dy()))
	if err != nil {
		return nil, err
	}
	if err := tex.Update(nil, unsafe.Pointer(&pix.Pix[0]), pix.Stride); err != nil {
		tex.Destroy()
		return nil, err
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	return tex, nil
}

// saverFadeIn is the alpha of something that appeared at since, fading in
// over d. A zero since means it has always been there.
func saverFadeIn(since, now time.Time, d time.Duration) uint8 {
	if since.IsZero() || d <= 0 {
		return 255
	}
	elapsed := now.Sub(since)
	if elapsed <= 0 {
		return 0
	}
	if elapsed >= d {
		return 255
	}
	return uint8(255 * elapsed / d)
}
