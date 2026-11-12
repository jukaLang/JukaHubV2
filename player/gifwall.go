package main

// gifwall.go — animated GIF wallpapers.
//
// A .gif picked as the wallpaper plays on every screen the backdrop is drawn
// on, Home included. The handheld has 1 GB of memory shared with its GPU and
// four A53 cores, so the player is built around three rules:
//
//   - Nothing heavy on the render thread. The file is walked, decoded frame by
//     frame, composited and scaled on one goroutine; the render thread only
//     uploads finished frames — a few megabytes a frame — and picks which
//     texture to draw.
//   - A fixed memory budget. image/gif's DecodeAll keeps every frame of a file
//     at its full size before anything can be scaled, and a few megabytes of
//     GIF can be hundreds of megabytes of frames. The file is walked block by
//     block instead (gifWallScan) and each frame is decoded on its own, so only
//     one frame's pixels, one composite canvas and the scaled frames that fit
//     the budget are ever held.
//   - Only what is shown is kept. Frames are cropped to the screen's aspect
//     ("cover", like a phone wallpaper), never kept larger than the screen, and
//     a frame's pixels are dropped the moment it is a texture.
//
// Pixel art (a GIF at most a third of the screen in both directions that a
// whole-number scale fills with little cropping) keeps its own resolution, is
// cropped so that scale fills the screen, and is drawn with nearest-neighbour
// filtering, so its pixels stay square and sharp instead of melting into a
// blur. A bigger GIF — a video clip — is smoothed like a photo.
//
// Frames play as they arrive: the first is on screen as soon as it is
// decoded, and the first loop runs at the speed the decoder hands the rest
// over, so a long GIF animates long before its last frame is in.
//
// The static loader (core.go ensureBackgroundTexture) leaves GIFs to this file:
// loading one there as well kept a second, stretched copy of its first frame,
// and where SDL_image cannot read GIFs it logged a failed load every 15 seconds.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gifWallMaxFileHandheld / gifWallMaxFileDesktop bound how much of a file
	// is read. A longer GIF plays the frames that fit; the rest is never read.
	gifWallMaxFileHandheld = 48 << 20
	gifWallMaxFileDesktop  = 160 << 20
	// gifWallBudgetHandheld / gifWallBudgetDesktop bound the scaled frames,
	// which is what the textures hold once they are uploaded. While a GIF is
	// still loading the decode needs more than that on top: the composite
	// canvas at the GIF's own resolution (up to gifWallMaxCanvas* pixels, four
	// bytes each), the same again for a GIF whose frames restore what was under
	// them, and image/gif's buffer for the frame being decoded (a byte a pixel,
	// new for every frame). On the handheld that peak is about 40 MB more,
	// for the seconds a decode takes.
	gifWallBudgetHandheld = 40 << 20
	gifWallBudgetDesktop  = 192 << 20
	// gifWallMaxFrames bounds one animation. A GIF longer than that is a film,
	// and its first thousand frames are what plays.
	gifWallMaxFrames = 1000
	// gifWallMaxCanvasHandheld / gifWallMaxCanvasDesktop bound, in pixels, the
	// composite canvas: the shown part of the GIF at its own resolution, the
	// one full-size buffer a decode keeps (twice for a GIF whose frames restore
	// what was under them).
	gifWallMaxCanvasHandheld = 4200000
	gifWallMaxCanvasDesktop  = 16800000
	// gifWallUploadHandheld / gifWallUploadDesktop are how much frame data one
	// UI frame may upload; at least one frame always goes up, so the first
	// frame is on screen at once whatever its size. The PowerVR driver lays
	// texture data out on the CPU, so the handheld takes smaller steps.
	gifWallUploadHandheld = 3 << 20
	gifWallUploadDesktop  = 6 << 20
	// gifWallGateMS is how long one choice of frame (and one upload pass)
	// stands within a single UI frame. The backdrop is drawn twice in a frame
	// that repaints its header band, and both must show the same picture; but
	// the loops that present without counting frames (the on-screen keyboard,
	// the shutdown card) must not freeze the animation either.
	gifWallGateMS = 50
	// gifWallMaxSide bounds the logical screen the one-frame decode is given.
	// Browsers clip a frame that pokes outside the screen and so does the
	// compositor, but a damaged file can name a frame tens of thousands of
	// pixels away; such a frame fails to decode instead.
	gifWallMaxSide = 16384
	// A frame asking for less than gifWallMinDelay ms plays for
	// gifWallDefaultDelay: every browser does this, and GIFs made for browsers
	// count on it.
	gifWallMinDelay     = 20
	gifWallDefaultDelay = 100
	// gifWallLowPowerStep is the animation clock's tick under Low Power, about
	// fifteen frames a second.
	gifWallLowPowerStep = 66
	// gifWallRetry is how long a GIF that could not be played waits before it
	// is looked at again — and then only if the file changed.
	gifWallRetry = 15 * time.Second
	// gifWallFrameBytesMax bounds one frame's compressed data.
	gifWallFrameBytesMax = 64 << 20

	// wallpaperSavedKey is the user-config key the picker's choice is kept
	// under, so it survives a restart; wallpaperNoneValue records "no
	// wallpaper" there.
	wallpaperSavedKey  = "wallpaper"
	wallpaperNoneValue = "none"
	// wallpaperAnimateKey is Settings → Display & theme → Animate wallpaper:
	// off holds a GIF wallpaper on its first frame.
	wallpaperAnimateKey = "wallpaperAnimate"
)

// gifWallHoldStill reports whether an animated wallpaper holds its first
// frame: under Battery Saver (every frame of a GIF is decode and upload work
// the battery pays for) or with Animate wallpaper switched off. Plain Reduce
// Motion does not stop it. The handheld's device config ships with Reduce
// Motion on, so following it left every GIF a user picked standing still — and
// picking an animated wallpaper is itself a choice of motion. Reduce Motion
// still calms the transitions and the liquid background; the switch is there
// for anyone who wants the picture without the movement.
func gifWallHoldStill(config *Config) bool {
	if currentPowerMode(config) == PowerModeBatterySaver {
		return true
	}
	return config != nil && !customBool(config.Variables.Custom, wallpaperAnimateKey, true)
}

// gifWallStillReason is the toast's half-sentence for a GIF that holds its
// first frame (gifWallHoldStill).
func gifWallStillReason(config *Config) string {
	if currentPowerMode(config) == PowerModeBatterySaver {
		return "Battery Saver holds it on its first frame"
	}
	return "Animate wallpaper is off, so it holds its first frame (Settings → Display & theme)"
}

// errGifWallCancelled ends a decode whose wallpaper is no longer wanted.
var errGifWallCancelled = errors.New("animated wallpaper decode cancelled")

// gifTrailer ends the one-frame GIF each frame is decoded from.
var gifTrailer = []byte{0x3B}

// gifWallpaperPath reports whether a wallpaper path is a GIF this file plays.
func gifWallpaperPath(path string) bool {
	return strings.EqualFold(filepath.Ext(strings.TrimSpace(path)), ".gif")
}

// ── Walking the file ──────────────────────────────────────────────────────

// gifScanFrame is one image of a GIF as the block walk found it: where it sits
// on the logical screen, what happens to it after it is shown, how long it is
// shown, and where its bytes are in the file.
type gifScanFrame struct {
	left, top, w, h int
	disposal        byte
	delayCS         int
	// gce is the frame's Graphic Control Extension, replayed in front of the
	// frame when it is decoded so image/gif sees its transparent colour.
	gce    [8]byte
	hasGCE bool
	// off and size locate the image descriptor through the end of the frame's
	// data in the file.
	off, size int64
}

// gifScan is the walk of one file.
type gifScan struct {
	width, height int    // the logical screen; 0 when the file says 0
	header        []byte // signature, screen descriptor and global colour table
	frames        []gifScanFrame
	capped        bool // more frames follow the frame cap
	cut           bool // the read limit ended the walk inside the file
	truncated     bool // capped or cut: the file holds more than was walked
}

// gifScanner reads a GIF front to back, keeping count of its position.
type gifScanner struct {
	r   *bufio.Reader
	pos int64
}

func (s *gifScanner) readByte() (byte, error) {
	b, err := s.r.ReadByte()
	if err == nil {
		s.pos++
	}
	return b, err
}

func (s *gifScanner) full(buf []byte) error {
	n, err := io.ReadFull(s.r, buf)
	s.pos += int64(n)
	return err
}

func (s *gifScanner) skip(n int) error {
	d, err := s.r.Discard(n)
	s.pos += int64(d)
	return err
}

// subBlocks skips a run of data sub-blocks through the empty one that ends it.
func (s *gifScanner) subBlocks() error {
	for {
		n, err := s.readByte()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := s.skip(int(n)); err != nil {
			return err
		}
	}
}

// gifColourTableBytes is the size of a colour table whose packed size field is
// bits.
func gifColourTableBytes(bits byte) int {
	return 3 << (uint(bits&7) + 1)
}

// gifWallScan walks a GIF's blocks without decoding a pixel: the frame list,
// each frame's geometry, timing and disposal, and where its data is. It reads
// at most limit bytes and keeps at most maxFrames frames; when the file goes
// on past either, capped or cut (and truncated) say so. A file cut short, or
// with junk after its last frame, keeps the frames before the damage, the way
// browsers play it, and is not truncated: there is nothing more to read.
func gifWallScan(r io.Reader, limit int64, maxFrames int) (*gifScan, error) {
	s := &gifScanner{r: bufio.NewReaderSize(io.LimitReader(r, limit), 64<<10)}
	head := make([]byte, 13)
	if err := s.full(head); err != nil || string(head[:3]) != "GIF" {
		return nil, errors.New("not a GIF file")
	}
	scan := &gifScan{
		width:  int(head[6]) | int(head[7])<<8,
		height: int(head[8]) | int(head[9])<<8,
	}
	if head[10]&0x80 != 0 {
		table := make([]byte, gifColourTableBytes(head[10]))
		if err := s.full(table); err != nil {
			return nil, errors.New("the GIF's colour table is cut short")
		}
		head = append(head, table...)
	}
	scan.header = head
	var gce [8]byte
	hasGCE := false
	// How the walk ended: every exit that is not a read failure says so
	// before it leaves.
	const (
		endRead = iota
		endTrailer
		endJunk
		endCap
	)
	end := endRead
walk:
	for {
		kind, err := s.readByte()
		if err != nil {
			break
		}
		switch kind {
		case 0x21: // extension
			label, err := s.readByte()
			if err != nil {
				break walk
			}
			if label == 0xF9 {
				size, err := s.readByte()
				if err != nil {
					break walk
				}
				if size >= 4 {
					var f [4]byte
					if err := s.full(f[:]); err != nil {
						break walk
					}
					gce = [8]byte{0x21, 0xF9, 0x04, f[0], f[1], f[2], f[3], 0x00}
					hasGCE = true
					if err := s.skip(int(size) - 4); err != nil {
						break walk
					}
				} else if err := s.skip(int(size)); err != nil {
					break walk
				}
			}
			if err := s.subBlocks(); err != nil {
				break walk
			}
		case 0x2C: // image descriptor
			off := s.pos - 1
			var d [9]byte
			if err := s.full(d[:]); err != nil {
				break walk
			}
			f := gifScanFrame{
				left: int(d[0]) | int(d[1])<<8, top: int(d[2]) | int(d[3])<<8,
				w: int(d[4]) | int(d[5])<<8, h: int(d[6]) | int(d[7])<<8,
				off: off, gce: gce, hasGCE: hasGCE,
			}
			if hasGCE {
				f.disposal = (gce[3] >> 2) & 7
				f.delayCS = int(gce[4]) | int(gce[5])<<8
			}
			if d[8]&0x80 != 0 {
				if err := s.skip(gifColourTableBytes(d[8])); err != nil {
					break walk
				}
			}
			if _, err := s.readByte(); err != nil { // LZW minimum code size
				break walk
			}
			if err := s.subBlocks(); err != nil {
				break walk
			}
			f.size = s.pos - off
			gce, hasGCE = [8]byte{}, false
			scan.frames = append(scan.frames, f)
			if len(scan.frames) >= maxFrames {
				end = endCap
				break walk
			}
		case 0x3B: // trailer
			end = endTrailer
			break walk
		case 0x00:
			// Padding some encoders leave between blocks.
		default:
			end = endJunk
			break walk
		}
	}
	switch end {
	case endCap:
		// Capped only when another block starts here: a file of exactly
		// maxFrames frames ends with its trailer.
		if next, err := s.r.Peek(1); err == nil {
			scan.capped = next[0] != 0x3B
		} else {
			scan.cut = s.pos >= limit
		}
	case endRead:
		// A read that failed at the limit stopped inside the file; one that
		// failed before it found the end of a damaged file.
		scan.cut = s.pos >= limit
	}
	scan.truncated = scan.capped || scan.cut
	if len(scan.frames) == 0 {
		return nil, errors.New("the GIF has no picture in it")
	}
	return scan, nil
}

// gifWallSourceSize is the logical screen, or — for a file that names a zero
// screen — the box that holds every frame.
func gifWallSourceSize(scan *gifScan) (int, int) {
	w, h := scan.width, scan.height
	if w > 0 && h > 0 {
		return w, h
	}
	w, h = 0, 0
	for _, f := range scan.frames {
		w = max(w, f.left+f.w)
		h = max(h, f.top+f.h)
	}
	return w, h
}

// gifWallFrameHeader is the file's header with the logical screen grown to
// hold every frame. image/gif refuses a frame that pokes outside the screen
// ("frame bounds larger than image bounds"); browsers clip such a frame
// instead, and so does the compositor here. The growth stops at
// gifWallMaxSide: a frame further out than that is damage, and failing to
// decode it is the right answer.
func gifWallFrameHeader(scan *gifScan) []byte {
	w, h := scan.width, scan.height
	for _, f := range scan.frames {
		w = max(w, min(f.left+f.w, gifWallMaxSide))
		h = max(h, min(f.top+f.h, gifWallMaxSide))
	}
	w, h = min(w, 0xFFFF), min(h, 0xFFFF)
	hdr := append([]byte(nil), scan.header...)
	copy(hdr, "GIF89a")
	hdr[6], hdr[7] = byte(w), byte(w>>8)
	hdr[8], hdr[9] = byte(h), byte(h>>8)
	return hdr
}

// gifWallDecodeFrame decodes one frame on its own: the header, the frame's
// control block and its bytes — read straight from the file — make a
// one-image GIF that image/gif can read. buf is reused for the frame's bytes
// and handed back.
//
// maxPixels bounds the frame before anything is read: image/gif allocates a
// byte per pixel of the frame's declared size up front, and once the header
// has grown to hold every frame its own bounds check no longer stands in the
// way — a damaged descriptor saying 65535×65535 would be a 4 GB allocation,
// which no recover() survives. Every caller passes a bound.
func gifWallDecodeFrame(file io.ReaderAt, header []byte, f gifScanFrame, buf []byte, maxPixels int) (*image.Paletted, []byte, error) {
	if f.size <= 0 || f.size > gifWallFrameBytesMax {
		return nil, buf, errors.New("a frame's data is out of range")
	}
	if maxPixels <= 0 || int64(f.w)*int64(f.h) > int64(maxPixels) {
		return nil, buf, fmt.Errorf("a frame is too large (%d×%d)", f.w, f.h)
	}
	if int64(cap(buf)) < f.size {
		buf = make([]byte, f.size)
	}
	body := buf[:f.size]
	if _, err := file.ReadAt(body, f.off); err != nil {
		return nil, buf, err
	}
	parts := []io.Reader{bytes.NewReader(header)}
	if f.hasGCE {
		parts = append(parts, bytes.NewReader(f.gce[:]))
	}
	parts = append(parts, bytes.NewReader(body), bytes.NewReader(gifTrailer))
	img, err := gif.Decode(io.MultiReader(parts...))
	if err != nil {
		return nil, buf, err
	}
	p, ok := img.(*image.Paletted)
	if !ok {
		return nil, buf, errors.New("a frame decoded to an unexpected image type")
	}
	return p, buf, nil
}

// ── Compositing ───────────────────────────────────────────────────────────

// gifWallPalette turns a frame's palette into RGBA bytes. A transparent entry
// keeps alpha 0, which tells the compositor to leave the pixel under it alone.
func gifWallPalette(p color.Palette, out *[256][4]byte) {
	*out = [256][4]byte{}
	for i, c := range p {
		if i >= len(out) {
			break
		}
		r, g, b, a := c.RGBA()
		if a == 0 {
			continue
		}
		out[i] = [4]byte{byte(r >> 8), byte(g >> 8), byte(b >> 8), 255}
	}
}

// gifWallCanvas is the composite of the frames so far over the part of the
// logical screen that is shown. It is always opaque: a transparent area shows
// the theme's base colour, the colour the backdrop would have there.
type gifWallCanvas struct {
	pix    []byte // RGBA, w*h*4
	w, h   int
	origin image.Point // where the canvas sits on the logical screen
	bg     [4]byte     // what disposing to the background clears to
	saved  []byte      // the area a "restore previous" frame covers, before it drew
	savedR image.Rectangle
}

func newGifWallCanvas(crop image.Rectangle, bg [4]byte) *gifWallCanvas {
	c := &gifWallCanvas{w: crop.Dx(), h: crop.Dy(), origin: crop.Min, bg: bg}
	c.pix = make([]byte, c.w*c.h*4)
	c.fill(image.Rect(0, 0, c.w, c.h))
	return c
}

// local maps a rect on the logical screen into the canvas, clipped to it.
func (c *gifWallCanvas) local(r image.Rectangle) image.Rectangle {
	return r.Sub(c.origin).Intersect(image.Rect(0, 0, c.w, c.h))
}

// fill clears r (canvas coordinates) to the background.
func (c *gifWallCanvas) fill(r image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := c.pix[(y*c.w+r.Min.X)*4 : (y*c.w+r.Max.X)*4]
		for i := 0; i < len(row); i += 4 {
			row[i], row[i+1], row[i+2], row[i+3] = c.bg[0], c.bg[1], c.bg[2], 255
		}
	}
}

// draw composites one decoded frame. A nil frame draws nothing: an empty frame
// that only holds time.
func (c *gifWallCanvas) draw(p *image.Paletted, pal *[256][4]byte) {
	if p == nil {
		return
	}
	r := c.local(p.Rect)
	if r.Empty() {
		return
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		src := p.Pix[(y+c.origin.Y-p.Rect.Min.Y)*p.Stride:]
		dst := c.pix[y*c.w*4:]
		dx := c.origin.X - p.Rect.Min.X
		for x := r.Min.X; x < r.Max.X; x++ {
			e := &pal[src[x+dx]]
			if e[3] == 0 {
				continue
			}
			o := x * 4
			dst[o], dst[o+1], dst[o+2], dst[o+3] = e[0], e[1], e[2], 255
		}
	}
}

// save remembers r (canvas coordinates) for restore.
func (c *gifWallCanvas) save(r image.Rectangle) {
	c.savedR = r
	n := r.Dx() * r.Dy() * 4
	if cap(c.saved) < n {
		c.saved = make([]byte, n)
	}
	c.saved = c.saved[:n]
	rowBytes := r.Dx() * 4
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(c.saved[(y-r.Min.Y)*rowBytes:], c.pix[(y*c.w+r.Min.X)*4:(y*c.w+r.Max.X)*4])
	}
}

// restore puts back what save remembered.
func (c *gifWallCanvas) restore() {
	r := c.savedR
	rowBytes := r.Dx() * 4
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(c.pix[(y*c.w+r.Min.X)*4:(y*c.w+r.Max.X)*4], c.saved[(y-r.Min.Y)*rowBytes:])
	}
}

// gifWallScale box-filters an RGBA image down to dw×dh, or copies it when the
// size already matches. Each output pixel averages the source pixels in its
// cell, so a frame shown at half its size keeps its detail instead of
// shimmering the way point sampling would.
func gifWallScale(src []byte, sw, sh int, dst []byte, dw, dh int) {
	if sw == dw && sh == dh {
		copy(dst, src[:sw*sh*4])
		return
	}
	x0 := make([]int, dw+1)
	for x := range x0 {
		x0[x] = x * sw / dw
	}
	for y := 0; y < dh; y++ {
		sy0, sy1 := y*sh/dh, (y+1)*sh/dh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < dw; x++ {
			sx0, sx1 := x0[x], x0[x+1]
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, b int
			for yy := sy0; yy < sy1; yy++ {
				row := src[(yy*sw+sx0)*4 : (yy*sw+sx1)*4]
				for i := 0; i < len(row); i += 4 {
					r += int(row[i])
					g += int(row[i+1])
					b += int(row[i+2])
				}
			}
			n := (sx1 - sx0) * (sy1 - sy0)
			o := (y*dw + x) * 4
			dst[o], dst[o+1], dst[o+2], dst[o+3] = byte(r/n), byte(g/n), byte(b/n), 255
		}
	}
}

// gifWallDelay is how long a frame shows, in milliseconds, by the browser rule.
func gifWallDelay(cs int) int {
	ms := cs * 10
	if ms < gifWallMinDelay {
		return gifWallDefaultDelay
	}
	return ms
}

// ── Planning ──────────────────────────────────────────────────────────────

// gifWallPlan is how one GIF is stored: the part of its logical screen that
// is shown, the size each kept frame is scaled to, the filter it is drawn
// with, and which frames are kept.
type gifWallPlan struct {
	crop    image.Rectangle
	w, h    int
	nearest bool
	keep    []bool
	kept    int
}

// gifWallCentre is a cw×ch rect centred in a sw×sh one.
func gifWallCentre(sw, sh, cw, ch int) image.Rectangle {
	x, y := (sw-cw)/2, (sh-ch)/2
	return image.Rect(x, y, x+cw, y+ch)
}

// gifWallPixelScale is the whole-number scale a srcW×srcH GIF is drawn at as
// pixel art on a canvasW×canvasH screen, or 0 when it is not pixel art. It has
// to be small next to the screen (a third of it or less each way: a video clip
// at half the screen is not pixel art) and the scale that covers the screen,
// rounded up to a whole number, may crop at most a tenth more than covering
// needs — rounding 2.7 up to 3 would cut away a tenth of a clip on each axis.
func gifWallPixelScale(srcW, srcH, canvasW, canvasH int) int {
	if srcW <= 0 || srcH <= 0 || srcW*3 > canvasW || srcH*3 > canvasH {
		return 0
	}
	cover := math.Max(float64(canvasW)/float64(srcW), float64(canvasH)/float64(srcH))
	k := int(math.Ceil(cover - 1e-9))
	if k < 1 || float64(k) > 1.1*cover {
		return 0
	}
	return k
}

// gifWallMakePlan fits a srcW×srcH GIF of frames frames to a canvasW×canvasH
// screen and a byte budget. The crop covers the screen's aspect. Pixel art
// (gifWallPixelScale) is kept at its own size and cropped to the screen
// divided by its scale, rounded down, so the scale never overshoots the
// screen: where it does not divide the screen exactly the GPU stretches the
// last pixel or two, never squeezes one. Anything else is never kept larger
// than the screen, and when the frames do not fit the budget they shrink
// first — to no less than a quarter of the screen — and then frames are
// dropped, evenly, with their time given to the frame before.
func gifWallMakePlan(srcW, srcH, canvasW, canvasH, frames int, budget int64) gifWallPlan {
	if canvasW <= 0 || canvasH <= 0 {
		canvasW, canvasH = 1280, 720
	}
	srcW, srcH = max(srcW, 1), max(srcH, 1)
	frames = max(frames, 1)
	var plan gifWallPlan
	if k := gifWallPixelScale(srcW, srcH, canvasW, canvasH); k > 0 {
		cw := min(srcW, max(1, canvasW/k))
		ch := min(srcH, max(1, canvasH/k))
		plan.crop = gifWallCentre(srcW, srcH, cw, ch)
		plan.w, plan.h = cw, ch
		plan.nearest = true
	} else {
		cw, ch := srcW, srcH
		if srcW*canvasH > srcH*canvasW {
			cw = (srcH*canvasW + canvasH/2) / canvasH // wider than the screen: crop the sides
		} else {
			ch = (srcW*canvasH + canvasW/2) / canvasW // taller: crop top and bottom
		}
		cw, ch = min(max(cw, 1), srcW), min(max(ch, 1), srcH)
		plan.crop = gifWallCentre(srcW, srcH, cw, ch)
		plan.w, plan.h = cw, ch
		if plan.w > canvasW {
			plan.w = canvasW
			plan.h = min(max((ch*canvasW+cw/2)/cw, 1), ch)
		}
		if need := int64(plan.w) * int64(plan.h) * 4 * int64(frames); need > budget && budget > 0 {
			f := math.Sqrt(float64(budget) / float64(need))
			nw := max(int(float64(plan.w)*f), min(plan.w, canvasW/2))
			if nw < plan.w {
				plan.h = min(max((plan.h*nw+plan.w/2)/plan.w, 1), plan.h)
				plan.w = nw
			}
		}
	}
	per := int64(plan.w) * int64(plan.h) * 4
	plan.kept = frames
	if budget > 0 && per*int64(frames) > budget {
		plan.kept = max(int(budget/per), 1)
	}
	plan.keep = make([]bool, frames)
	for j := 0; j < plan.kept; j++ {
		plan.keep[j*frames/plan.kept] = true
	}
	return plan
}

// ── Decoding ──────────────────────────────────────────────────────────────

// gifWallLimits are the bounds one decode works within.
type gifWallLimits struct {
	file   int64
	budget int64
	canvas int
	frames int
}

// gifWallLimitsNow picks the handheld's bounds or the desktop's.
func gifWallLimitsNow() gifWallLimits {
	if P().IsHandheld() {
		return gifWallLimits{file: gifWallMaxFileHandheld, budget: gifWallBudgetHandheld,
			canvas: gifWallMaxCanvasHandheld, frames: gifWallMaxFrames}
	}
	return gifWallLimits{file: gifWallMaxFileDesktop, budget: gifWallBudgetDesktop,
		canvas: gifWallMaxCanvasDesktop, frames: gifWallMaxFrames}
}

// gifWallOut is one finished frame waiting for the render thread.
type gifWallOut struct {
	pix   []byte // RGBA, w*h*4; nil once uploaded
	delay int    // ms
}

// gifWallJob is one decode in flight. The decoder appends finished frames; the
// render thread takes them, uploads them and drops their pixels.
type gifWallJob struct {
	gen  uint64
	path string
	// still is a first-frame-only decode (gifWallHoldStill, or memory running
	// short): the frame cap is the point, not news for the log.
	still bool

	mu      sync.Mutex
	frames  []gifWallOut
	w, h    int
	nearest bool
	done    bool
	err     error  // nothing could be played
	note    string // played, but not all of it (logged once)
	partial bool   // a frame after the first could not be decoded
}

// gifWallGen is the decode generation. A decoder whose job no longer carries
// it stops at its next frame.
var gifWallGen atomic.Uint64

// gifWallDecode runs one job to the end on its own goroutine. It touches
// nothing but the job and the generation counter.
func gifWallDecode(job *gifWallJob, canvasW, canvasH int, bg [4]byte, lim gifWallLimits) {
	err := gifWallDecodeFile(job, canvasW, canvasH, bg, lim)
	job.mu.Lock()
	job.done = true
	job.err = err
	job.mu.Unlock()
}

// gifWallDecodeFile opens, walks and renders the job's file.
func gifWallDecodeFile(job *gifWallJob, canvasW, canvasH int, bg [4]byte, lim gifWallLimits) error {
	file, err := os.Open(job.path)
	if err != nil {
		return err
	}
	defer file.Close()
	scan, err := gifWallScan(file, lim.file, lim.frames)
	if err != nil {
		return err
	}
	note := ""
	switch {
	case scan.capped && !job.still:
		note = fmt.Sprintf("plays its first %d frames, the most one animation keeps", len(scan.frames))
	case scan.cut:
		note = fmt.Sprintf("only the first %d frames fit the read limit", len(scan.frames))
	}
	if note != "" {
		job.mu.Lock()
		job.note = note
		job.mu.Unlock()
	}
	return gifWallRender(job, file, scan, canvasW, canvasH, bg, lim)
}

// gifWallRender decodes, composites, scales and hands over every frame of a
// walked file, in order. It is the decode minus the file system, so tests can
// drive it from memory.
func gifWallRender(job *gifWallJob, file io.ReaderAt, scan *gifScan, canvasW, canvasH int, bg [4]byte, lim gifWallLimits) error {
	srcW, srcH := gifWallSourceSize(scan)
	if srcW <= 0 || srcH <= 0 {
		return errors.New("the GIF has no picture size")
	}
	plan := gifWallMakePlan(srcW, srcH, canvasW, canvasH, len(scan.frames), lim.budget)
	if lim.canvas > 0 && plan.crop.Dx()*plan.crop.Dy() > lim.canvas {
		return fmt.Errorf("%d×%d is too large to animate here", srcW, srcH)
	}
	job.mu.Lock()
	job.w, job.h, job.nearest = plan.w, plan.h, plan.nearest
	job.mu.Unlock()

	header := gifWallFrameHeader(scan)
	canvas := newGifWallCanvas(plan.crop, bg)
	var pal [256][4]byte
	var buf []byte
	var err error
	last := -1 // the newest kept frame in job.frames
	for i, f := range scan.frames {
		if gifWallGen.Load() != job.gen {
			return errGifWallCancelled
		}
		// A game took the screen: the wallpaper can wait, the game cannot.
		for gameScreenSuspended() && gifWallGen.Load() == job.gen {
			time.Sleep(250 * time.Millisecond)
		}
		var p *image.Paletted
		if f.w > 0 && f.h > 0 {
			// A frame may be bigger than the shown crop (it is clipped while
			// compositing), but not without bound.
			p, buf, err = gifWallDecodeFrame(file, header, f, buf, 2*max(lim.canvas, plan.crop.Dx()*plan.crop.Dy()))
			if err != nil {
				if last < 0 {
					return err
				}
				job.mu.Lock()
				job.partial = true
				job.note = fmt.Sprintf("stopped at frame %d of %d: %v", i+1, len(scan.frames), err)
				job.mu.Unlock()
				return nil
			}
			gifWallPalette(p.Palette, &pal)
		}
		rect := canvas.local(image.Rect(f.left, f.top, f.left+f.w, f.top+f.h))
		if f.disposal == 3 {
			canvas.save(rect)
		}
		canvas.draw(p, &pal)
		delay := gifWallDelay(f.delayCS)
		switch {
		case plan.keep[i]:
			out := make([]byte, plan.w*plan.h*4)
			gifWallScale(canvas.pix, canvas.w, canvas.h, out, plan.w, plan.h)
			job.mu.Lock()
			job.frames = append(job.frames, gifWallOut{pix: out, delay: delay})
			last = len(job.frames) - 1
			job.mu.Unlock()
		case last >= 0:
			job.mu.Lock()
			job.frames[last].delay += delay
			job.mu.Unlock()
		}
		switch f.disposal {
		case 2:
			canvas.fill(rect)
		case 3:
			canvas.restore()
		}
	}
	if plan.kept < len(scan.frames) {
		job.mu.Lock()
		if job.note == "" {
			job.note = fmt.Sprintf("kept %d of %d frames at %d×%d to fit the memory budget",
				plan.kept, len(scan.frames), plan.w, plan.h)
		}
		job.mu.Unlock()
	}
	return nil
}

// ── Playing (render thread) ───────────────────────────────────────────────

// gifWallTex is one uploaded frame.
type gifWallTex struct {
	tex   *sdl.Texture
	delay int
}

// gifWall is the animation on screen. Render thread only; the decoder talks
// to it through the job.
var gifWall struct {
	path     string // the wallpaper path this state is for, as configured
	canvasW  int32
	canvasH  int32
	job      *gifWallJob
	renderer *sdl.Renderer // the renderer that owns the textures
	frames   []gifWallTex
	ends     []int64 // each frame's end, ms into the loop, once complete
	loop     int64
	complete bool
	start    uint64
	// still and bg are what the decode was made for: the first frame only
	// (gifWallHoldStill), and the theme's base colour baked into the GIF's
	// transparent areas. Either changing decodes it again.
	still bool
	bg    sdl.Color
	// final counts the uploaded frames whose time is settled: a kept frame
	// gains the time of the frames dropped after it until the next kept frame
	// is decoded. pos and posStart are the frame on screen while the rest are
	// still on their way, and when it went up (posSet: since the decode began).
	final    int
	pos      int
	posStart uint64
	posSet   bool
	// upStamp/upAt and pickStamp/pickAt hold one upload pass and one choice of
	// frame for the length of a UI frame (gifWallGateMS).
	upStamp   uint64
	upAt      uint64
	upValid   bool
	pickStamp uint64
	pickAt    uint64
	pickValid bool
	pickIdx   int
	// A path that could not be played, and when and in what state it was last
	// looked at.
	failedPath string
	failedAt   time.Time
	failedSig  string
	// frozenPath is a wallpaper that memory pressure left at its first frame
	// (gifWallShrinkForPressure); it stays still until it is picked again.
	frozenPath string
	// gamePaused means a game owns the screen. Only the first frame is retained
	// while away; the animation is decoded again after JukaHub returns.
	gamePaused bool
	// minDelay is the shortest settled frame time in ms, which the cadence a
	// playing wallpaper asks for is worked out from (gifWallFrameNeed); 0
	// until a frame's time is settled.
	minDelay int
}

// gifWallTicks is the animation clock; tests replace it.
var gifWallTicks = func() uint64 { return sdl.GetTicks64() }

// gifWallFileSig names a file's state (size and modification time) so a GIF
// that failed is only tried again once it changed.
func gifWallFileSig(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

// gifWallSync points the player at config's wallpaper and reports whether it
// is a GIF the player is showing (or about to). It is cheap when nothing
// changed: a handful of compares.
func gifWallSync(config *Config) bool {
	path := ""
	if config != nil {
		path = strings.TrimSpace(config.Variables.BackgroundImage)
	}
	if !gifWallpaperPath(path) || appWallpaperIsShippedDefault(path) {
		if gifWall.path != "" {
			gifWallRelease(true)
			gifWall.path = ""
		}
		return false
	}
	still := gifWallHoldStill(config)
	switch {
	case path != gifWall.path || gifWall.canvasW != screenWidth || gifWall.canvasH != screenHeight:
		gifWallRelease(true)
		gifWall.path = path
		gifWall.canvasW, gifWall.canvasH = screenWidth, screenHeight
		gifWall.still = still
		// A path that comes back is looked at afresh, like the static loader.
		gifWall.failedPath = ""
		gifWallStart()
	case gifWall.failedPath == path || gifWall.frozenPath == path:
		// A GIF that failed waits for its retry, and one memory pressure
		// stopped stays still: neither starts over for a switch or a theme.
	case still != gifWall.still || gifWall.bg != ColorBackground:
		// Holding still began (the first frame only: nothing more to hold in
		// memory) or ended (the whole animation), or the theme's base colour —
		// baked into the GIF's transparent areas — changed.
		gifWallRelease(true)
		gifWall.still = still
		gifWallStart()
	}
	if gifWall.job == nil && len(gifWall.frames) == 0 {
		gifWallMaybeRetry()
	}
	return true
}

// gifWallStart begins decoding the current path: every frame, or only the
// first for a still (gifWallHoldStill) or frozen (memory) wallpaper.
func gifWallStart() {
	gen := gifWallGen.Add(1)
	still := gifWall.still || (gifWall.frozenPath != "" && gifWall.frozenPath == gifWall.path)
	job := &gifWallJob{gen: gen, path: resolvePath(gifWall.path), still: still}
	gifWall.job = job
	gifWall.bg = ColorBackground
	gifWall.final, gifWall.pos, gifWall.posStart, gifWall.posSet = 0, 0, 0, false
	gifWall.minDelay = 0
	lim := gifWallLimitsNow()
	if still {
		lim.frames = 1
	}
	bg := [4]byte{gifWall.bg.R, gifWall.bg.G, gifWall.bg.B, 255}
	go gifWallDecode(job, int(gifWall.canvasW), int(gifWall.canvasH), bg, lim)
}

// gifWallMaybeRetry looks at a GIF that failed again once gifWallRetry has
// passed, and decodes it again only if the file changed in the meantime (a
// card mounted late, a file copied over).
func gifWallMaybeRetry() {
	if gifWall.failedPath != gifWall.path || time.Since(gifWall.failedAt) < gifWallRetry {
		return
	}
	gifWall.failedAt = time.Now()
	if gifWallFileSig(resolvePath(gifWall.path)) == gifWall.failedSig {
		return
	}
	gifWall.failedPath = ""
	gifWallStart()
}

// gifWallRelease forgets the animation: the decoder is told to stop, and the
// textures are destroyed — or only dropped when destroy is false, because the
// renderer that made them is gone and freed them itself.
func gifWallRelease(destroy bool) {
	gifWallGen.Add(1)
	if destroy {
		for _, f := range gifWall.frames {
			if f.tex != nil {
				_ = f.tex.Destroy()
			}
		}
	}
	gifWall.frames = nil
	gifWall.ends = nil
	gifWall.loop = 0
	gifWall.job = nil
	gifWall.complete = false
	gifWall.renderer = nil
	gifWall.final, gifWall.pos, gifWall.posStart, gifWall.posSet = 0, 0, 0, false
	gifWall.minDelay = 0
	gifWall.upValid = false
	gifWall.pickValid = false
	gifWall.gamePaused = false
}

// gifWallPauseForGame releases every animation texture after the first one
// while a game owns the framebuffer. The generation bump also lets the decoder
// drop its queued pixels instead of retaining them for the whole game session.
// Render thread only.
func gifWallPauseForGame() {
	if gifWall.path == "" || gifWall.gamePaused {
		return
	}
	gifWall.gamePaused = true
	gifWallGen.Add(1)
	gifWall.job = nil
	if len(gifWall.frames) > 1 {
		for _, f := range gifWall.frames[1:] {
			if f.tex != nil {
				_ = f.tex.Destroy()
			}
		}
		gifWall.frames = gifWall.frames[:1]
	}
	if len(gifWall.frames) == 1 {
		gifWall.frames[0].delay = max(gifWall.frames[0].delay, 1)
		gifWall.ends = []int64{int64(gifWall.frames[0].delay)}
		gifWall.loop = gifWall.ends[0]
		gifWall.complete = true
		gifWall.final = 1
		gifWall.pos, gifWall.posStart, gifWall.posSet = 0, 0, false
	} else {
		gifWall.ends = nil
		gifWall.loop = 0
		gifWall.complete = false
		gifWall.final = 0
		gifWall.pos, gifWall.posStart, gifWall.posSet = 0, 0, false
	}
	gifWall.pickValid = false
	gifWall.upValid = false
	logInfo("wallpaper", "paused animated wallpaper for game")
}

// gifWallResumeAfterGame drops the retained cover and lets the next backdrop
// sync decode the current configured wallpaper afresh. That sync also honours
// any settings or wallpaper change made while the game owned the screen.
// Render thread only.
func gifWallResumeAfterGame() {
	if !gifWall.gamePaused {
		return
	}
	path := gifWall.path
	gifWallRelease(true)
	if gifWall.failedPath == path {
		return
	}
	gifWall.path = ""
}

// gifWallEvict frees an animated wallpaper's textures and whatever its decoder
// holds, at once; the next backdrop draw decodes it again. Render thread only.
func gifWallEvict() {
	if gifWall.path == "" {
		return
	}
	gifWallRelease(true)
	gifWall.path = ""
}

// gifWallShrinkForPressure keeps only the first frame of an animated wallpaper
// when memory runs short (EvictCachesForPressure, on the render thread). Its
// frames are the largest set of textures the app holds — up to 40 MB on the
// handheld — and a still wallpaper is still a wallpaper. It stays still until
// it is picked again (gifWallRetryPick).
func gifWallShrinkForPressure() {
	if gifWall.path == "" || gifWall.frozenPath == gifWall.path {
		return
	}
	gifWall.frozenPath = gifWall.path
	if len(gifWall.frames) == 0 {
		// Nothing is on screen yet: start over, the first frame only.
		gifWallEvict()
		logInfo("wallpaper", "memory is short: the animated wallpaper will show its first frame only")
		return
	}
	gifWallGen.Add(1) // the decoder stops at its next frame
	gifWall.job = nil
	for _, f := range gifWall.frames[1:] {
		if f.tex != nil {
			_ = f.tex.Destroy()
		}
	}
	first := gifWall.frames[0]
	first.delay = max(first.delay, 1)
	gifWall.frames = []gifWallTex{first}
	gifWall.ends = []int64{int64(first.delay)}
	gifWall.loop = int64(first.delay)
	gifWall.complete = true
	gifWall.final, gifWall.pos = 1, 0
	gifWall.pickValid = false
	logInfo("wallpaper", "memory is short: the animated wallpaper %s holds its first frame", gifWall.path)
}

// gifWallRetryPick lets a GIF picked again in the picker start over at once:
// one that could not be played is tried again, and one memory pressure left at
// its first frame animates again. A GIF that is playing is left alone. Render
// thread (the picker's input).
func gifWallRetryPick(path string) {
	path = strings.TrimSpace(path)
	same := func(p string) bool { return p != "" && path != "" && filepath.Clean(p) == filepath.Clean(path) }
	if !same(gifWall.failedPath) && !same(gifWall.frozenPath) {
		return
	}
	if same(gifWall.frozenPath) {
		gifWall.frozenPath = ""
	}
	gifWall.failedPath = ""
	gifWallEvict()
}

// gifWallTooLarge says why the GIF at path cannot play as the wallpaper on a
// canvasW×canvasH screen, or "" when it can (or when only the decoder can
// tell). It reads the ten header bytes that name the GIF's size, so the picker
// can say so while it is still open instead of warning after it closed.
func gifWallTooLarge(path string, canvasW, canvasH int) string {
	p := resolvePath(path)
	if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	var head [10]byte
	if _, err := io.ReadFull(f, head[:]); err != nil || string(head[:3]) != "GIF" {
		return ""
	}
	w, h := int(head[6])|int(head[7])<<8, int(head[8])|int(head[9])<<8
	if w <= 0 || h <= 0 {
		return ""
	}
	lim := gifWallLimitsNow()
	plan := gifWallMakePlan(w, h, canvasW, canvasH, 1, lim.budget)
	if lim.canvas > 0 && plan.crop.Dx()*plan.crop.Dy() > lim.canvas {
		return fmt.Sprintf("That GIF is too large to animate here (%d × %d)", w, h)
	}
	return ""
}

// gifWallPoll notices a decode that ended with nothing to show. It needs no
// renderer, so it also runs while no frame has reached the screen yet — which
// is exactly when the backdrop does not ask for a texture.
func gifWallPoll() {
	job := gifWall.job
	if job == nil {
		return
	}
	job.mu.Lock()
	done, err, n := job.done, job.err, len(job.frames)
	job.mu.Unlock()
	if done && n == 0 && len(gifWall.frames) == 0 {
		gifWallFail(err)
	}
}

// gifWallFail records a GIF that could not be played and tells the user once.
func gifWallFail(err error) {
	gifWall.job = nil
	gifWall.failedPath = gifWall.path
	gifWall.failedAt = time.Now()
	gifWall.failedSig = gifWallFileSig(resolvePath(gifWall.path))
	if err == nil || errors.Is(err, errGifWallCancelled) {
		return
	}
	logWarn("wallpaper", "animated wallpaper %s could not be played: %v", gifWall.path, err)
	msg := "That GIF could not be played"
	if strings.Contains(err.Error(), "too large") {
		msg = "That GIF is too large to play as a wallpaper"
	}
	ShowToast(msg, ToastKindWarning)
}

// gifWallTexture uploads one frame as an opaque static texture. The filter is
// fixed when the texture is made, so the hint is set around the creation and
// put back for everyone else (go-sdl2 has no per-texture scale mode).
func gifWallTexture(renderer *sdl.Renderer, pix []byte, w, h int, nearest bool) *sdl.Texture {
	if renderer == nil || w <= 0 || h <= 0 || len(pix) < w*h*4 {
		return nil
	}
	quality := "linear"
	if nearest {
		quality = "nearest"
	}
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, quality)
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, int32(w), int32(h))
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if err != nil || tex == nil {
		return nil
	}
	// R,G,B,A bytes are ABGR8888 on the little-endian targets the app runs on.
	if err := tex.Update(nil, unsafe.Pointer(&pix[0]), w*4); err != nil {
		_ = tex.Destroy()
		return nil
	}
	// Opaque: the cheapest full-screen copy the GPU can do.
	_ = tex.SetBlendMode(sdl.BLENDMODE_NONE)
	return tex
}

// gifWallUploadBudget is how much frame data one UI frame may upload.
func gifWallUploadBudget() int {
	if P().IsHandheld() {
		return gifWallUploadHandheld
	}
	return gifWallUploadDesktop
}

// gifWallPump moves finished frames from the decoder to textures — at most
// gifWallUploadBudget a UI frame, but always at least one, so the first frame
// is on screen the moment it is decoded — settles the time of the frames whose
// time can no longer grow, and starts the loop once every frame is in.
func gifWallPump(renderer *sdl.Renderer) {
	job := gifWall.job
	if job == nil || renderer == nil {
		return
	}
	now := gifWallTicks()
	if gifWall.upValid && gifWall.upStamp == frameCount && now-gifWall.upAt < gifWallGateMS {
		return
	}
	gifWall.upStamp, gifWall.upAt, gifWall.upValid = frameCount, now, true
	job.mu.Lock()
	n, done := len(job.frames), job.done
	w, h, nearest := job.w, job.h, job.nearest
	job.mu.Unlock()
	budget := gifWallUploadBudget()
	sent := 0
	for len(gifWall.frames) < n {
		i := len(gifWall.frames)
		job.mu.Lock()
		pix := job.frames[i].pix
		job.mu.Unlock()
		if sent > 0 && sent+len(pix) > budget {
			break
		}
		tex := gifWallTexture(renderer, pix, w, h, nearest)
		if tex == nil {
			// Out of texture memory: stop the decoder and play the frames that
			// made it. Nothing made it means nothing can be shown.
			gifWallGen.Add(1)
			if i == 0 {
				gifWallFail(errors.New("its frames could not be uploaded"))
				return
			}
			logWarn("wallpaper", "animated wallpaper %s: playing the %d frames that fit in texture memory", gifWall.path, i)
			gifWallFinish(job, i)
			return
		}
		job.mu.Lock()
		job.frames[i].pix = nil
		job.mu.Unlock()
		gifWall.frames = append(gifWall.frames, gifWallTex{tex: tex})
		sent += len(pix)
	}
	gifWallSettle(job, n, done)
	if done && n > 0 && len(gifWall.frames) == n {
		gifWallFinish(job, n)
	}
}

// gifWallSettle copies the time of every uploaded frame whose time is settled:
// all of them once the decode is done, else all but the newest kept frame,
// which still gains the time of the frames dropped after it.
func gifWallSettle(job *gifWallJob, n int, done bool) {
	settled := n - 1
	if done {
		settled = n
	}
	settled = min(settled, len(gifWall.frames))
	if settled <= gifWall.final {
		return
	}
	job.mu.Lock()
	for i := gifWall.final; i < settled && i < len(job.frames); i++ {
		d := max(job.frames[i].delay, 1)
		gifWall.frames[i].delay = d
		if gifWall.minDelay == 0 || d < gifWall.minDelay {
			gifWall.minDelay = d
		}
	}
	job.mu.Unlock()
	gifWall.final = settled
}

// gifWallFinish starts the loop over the first n frames: their final delays,
// the loop's timeline, and the clock — set so the frame on screen carries on
// where it is instead of the loop jumping back to its first frame.
func gifWallFinish(job *gifWallJob, n int) {
	job.mu.Lock()
	n = min(n, len(job.frames), len(gifWall.frames))
	for i := 0; i < n; i++ {
		gifWall.frames[i].delay = max(job.frames[i].delay, 1)
	}
	note, partial := job.note, job.partial
	job.mu.Unlock()
	for _, f := range gifWall.frames[n:] {
		if f.tex != nil {
			_ = f.tex.Destroy()
		}
	}
	gifWall.frames = gifWall.frames[:n]
	gifWall.ends = make([]int64, n)
	gifWall.minDelay = 0
	var t int64
	for i, f := range gifWall.frames {
		t += int64(f.delay)
		gifWall.ends[i] = t
		if gifWall.minDelay == 0 || f.delay < gifWall.minDelay {
			gifWall.minDelay = f.delay
		}
	}
	gifWall.loop = t
	gifWall.complete = true
	gifWall.final = n
	now := gifWallTicks()
	gifWall.start = now
	if pos := gifWall.pos; gifWall.posSet && pos < n {
		d := int64(gifWall.frames[pos].delay)
		into := min(max(int64(now-gifWall.posStart), 0), d-1)
		// Unsigned arithmetic wraps both ways, so now-start comes back as
		// exactly this offset however early in the session the clock is.
		gifWall.start = now - uint64(gifWall.ends[pos]-d+into)
	}
	gifWall.pickValid = false
	gifWall.job = nil
	if note != "" {
		logInfo("wallpaper", "animated wallpaper %s: %s", gifWall.path, note)
	}
	if partial && n == 1 {
		ShowToast("That GIF could not be played — showing its first frame", ToastKindWarning)
	}
}

// gifWallFrameAt is the frame showing t ms into a loop whose frames end at
// ends (ascending).
func gifWallFrameAt(ends []int64, t int64) int {
	i := sort.Search(len(ends), func(i int) bool { return ends[i] > t })
	if i >= len(ends) {
		i = len(ends) - 1
	}
	return max(i, 0)
}

// gifWallAdvance plays the frames uploaded so far, in order, while the rest
// are still decoding. A frame gives way once its time is up, its time is
// settled and the next frame is up; when the next one is not there yet the
// newest stays on screen, and a frame that had to wait starts when it arrives.
// The clock never runs ahead of the decoder, so the first loop plays at the
// speed the frames come in and none is skipped for being late.
func gifWallAdvance(t uint64) int {
	if !gifWall.posSet {
		gifWall.pos, gifWall.posStart, gifWall.posSet = 0, t, true
	}
	for gifWall.pos < gifWall.final && gifWall.pos+1 < len(gifWall.frames) {
		end := gifWall.posStart + uint64(max(gifWall.frames[gifWall.pos].delay, 1))
		if t < end {
			break
		}
		gifWall.pos++
		if t-end > gifWallGateMS {
			// It waited on the decoder: its time starts now.
			gifWall.posStart = t
		} else {
			gifWall.posStart = end
		}
	}
	return gifWall.pos
}

// gifWallPick chooses the frame for this UI frame. A wallpaper holding still
// (gifWallHoldStill) shows the first; while frames are still arriving they play in order as they come
// (gifWallAdvance); once all are in, the loop runs by the clock. Low Power
// steps the clock at about fifteen frames a second.
func gifWallPick(config *Config) int {
	n := len(gifWall.frames)
	if n <= 1 || gifWallHoldStill(config) {
		return 0
	}
	now := gifWallTicks()
	if gifWall.pickValid && gifWall.pickStamp == frameCount && now-gifWall.pickAt < gifWallGateMS && gifWall.pickIdx < n {
		return gifWall.pickIdx
	}
	lowPower := effectiveLowPower(config)
	idx := 0
	if gifWall.complete && gifWall.loop > 0 {
		elapsed := int64(now-gifWall.start) % gifWall.loop
		if lowPower {
			elapsed -= elapsed % gifWallLowPowerStep
		}
		idx = gifWallFrameAt(gifWall.ends, elapsed)
	} else {
		t := now
		if lowPower {
			t -= t % gifWallLowPowerStep
		}
		idx = gifWallAdvance(t)
	}
	idx = min(max(idx, 0), n-1)
	gifWall.pickStamp, gifWall.pickAt, gifWall.pickIdx, gifWall.pickValid = frameCount, now, idx, true
	return idx
}

// appWallpaperTexture is the wallpaper picture to draw this frame and the part
// of it to show (nil shows all of it): the current frame of an animated GIF
// wallpaper while one plays, else the static wallpaper texture. GIF frames are
// already cropped to the screen's aspect, so the whole texture is the part to
// show.
func appWallpaperTexture(renderer *sdl.Renderer, config *Config) (*sdl.Texture, *sdl.Rect) {
	if renderer == nil {
		return bgTexture, nil
	}
	if gifWall.renderer != nil && gifWall.renderer != renderer {
		// A new renderer: the old one took its textures with it — forget them,
		// never destroy them — and their pixels were dropped on upload, so the
		// GIF is decoded again. This runs before gifWallSync, which would
		// otherwise destroy the dead renderer's textures on a wallpaper change
		// made in the same frame.
		gifWallRelease(false)
		if gifWall.path != "" {
			gifWallStart()
		}
	}
	if !gifWallSync(config) {
		return bgTexture, nil
	}
	gifWall.renderer = renderer
	gifWallPoll()
	gifWallPump(renderer)
	if len(gifWall.frames) == 0 {
		return bgTexture, nil
	}
	// The frame is going on screen: a playing GIF asks for the cadence its
	// frames need (adaptive.go), so a 25 fps clip is not shown at the 15 fps
	// an untouched screen idles at.
	if need := gifWallFrameNeed(config); need > 0 {
		requestFrameEvery(need)
	}
	return gifWall.frames[gifWallPick(config)].tex, nil
}

// gifWallFrameNeed is the frame interval a playing GIF wallpaper asks the
// frame-rate governor for: half its shortest frame, so every change of frame
// lands within half a frame of its time (the governor never goes faster than
// its active cadence, 30 fps on the handheld). It is 0 — nothing asked —
// when the picture does not move: one frame, holding still, Low Power (whose
// clock steps a GIF at fifteen frames a second, gifWallPick), or the living
// background, whose opaque base is painted over the wallpaper.
func gifWallFrameNeed(config *Config) uint32 {
	if len(gifWall.frames) <= 1 || gifWallHoldStill(config) || effectiveLowPower(config) ||
		(config != nil && config.Variables.DynamicBg) {
		return 0
	}
	d := gifWall.minDelay
	if d <= 0 {
		// No frame's time is settled yet: the browsers' default frame.
		d = gifWallDefaultDelay
	}
	return uint32(max(d/2, 1))
}

// wallSmooth is the static wallpaper appWallpaperTextureSmooth last looked
// at: the texture, the path it was loaded from and its renderer.
var wallSmooth struct {
	tex      *sdl.Texture
	path     string
	renderer *sdl.Renderer
}

// appWallpaperTextureSmooth is appWallpaperTexture for a picture that is drawn
// bent: the liquid background's refraction mesh (liquidbg.go), where a ripple
// acts as a lens and enlarges the picture to twice its size and more. The
// static wallpaper is loaded with SDL's default filter, nearest, which is
// exact for the plain 1:1 copy every other screen draws but turned those
// enlargements into blocks — the "pixelated" water a tester saw. go-sdl2 has
// no per-texture filter, so the picture is made once more through the same
// fit as at load (core.go fitBackgroundTexture: a 1:1 copy read back from a
// scratch target) with the linear hint up, and the new texture takes the old
// one's place in bgTexture: core.go still owns and frees it, and the 1:1
// copies look the same with either filter. It runs once per picture; a
// renderer without render targets keeps the original. A GIF frame already
// has the filter it should have (pixel art keeps nearest on purpose).
func appWallpaperTextureSmooth(renderer *sdl.Renderer, config *Config) (*sdl.Texture, *sdl.Rect) {
	tex, src := appWallpaperTexture(renderer, config)
	if tex == nil || renderer == nil || tex != bgTexture {
		return tex, src
	}
	if wallSmooth.tex == tex && wallSmooth.renderer == renderer && wallSmooth.path == bgImagePath {
		return tex, src
	}
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	smooth := fitBackgroundTexture(renderer, tex)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if smooth != nil && smooth != tex {
		// fitBackgroundTexture destroyed the original.
		bgTexture, tex = smooth, smooth
	}
	wallSmooth.tex, wallSmooth.path, wallSmooth.renderer = tex, bgImagePath, renderer
	return tex, src
}

// gifWallTextureBytes is what the wallpaper holds as textures — an animated
// one's frames, all the same size, and the static picture — for the memory
// line (memstat.go). Main thread.
func gifWallTextureBytes() int64 {
	var n int64
	if len(gifWall.frames) > 0 && gifWall.frames[0].tex != nil {
		if _, _, w, h, err := gifWall.frames[0].tex.Query(); err == nil {
			n += int64(w) * int64(h) * 4 * int64(len(gifWall.frames))
		}
	}
	if bgTexture != nil {
		if _, _, w, h, err := bgTexture.Query(); err == nil {
			n += int64(w) * int64(h) * 4
		}
	}
	return n
}

// gifWallpaperReady reports whether an animated GIF wallpaper has a frame to
// show for config's wallpaper — uploaded, or decoded and about to be: the
// backdrop only asks appWallpaperTexture (which uploads) once this is true.
func gifWallpaperReady(config *Config) bool {
	if !gifWallSync(config) {
		return false
	}
	gifWallPoll()
	if len(gifWall.frames) > 0 {
		return true
	}
	job := gifWall.job
	if job == nil {
		return false
	}
	job.mu.Lock()
	has := len(job.frames) > 0
	job.mu.Unlock()
	return has
}

// ── Remembering the picker's choice ───────────────────────────────────────

// wallpaperRestoreState is what the restore last looked at: the config, the
// wallpaper it named afterwards, the saved pick and whether an experience pack
// had the wallpaper. The per-frame call is a few compares while none of them
// changes. A reload in place (a backup restored, the config file edited) keeps
// the pointer but puts the stock picture back, and the restore runs again for
// it. missingAt is when the saved file was not there: it is looked for again
// after gifWallRetry, for a card that mounts late.
var wallpaperRestoreState struct {
	config    *Config
	current   string
	value     string
	pack      bool
	valid     bool
	missingAt time.Time
}

// wallpaperRestoreRemember records a config's state as looked at, so the next
// per-frame call leaves it alone (the picker calls it after a pick).
func wallpaperRestoreRemember(config *Config) {
	st := &wallpaperRestoreState
	st.config, st.valid, st.missingAt = config, config != nil, time.Time{}
	st.current, st.value, st.pack = "", "", false
	if config == nil {
		return
	}
	st.current = config.Variables.BackgroundImage
	st.value, _ = config.Variables.Custom[wallpaperSavedKey].(string)
	_, st.pack = config.Variables.Custom[xpackWallpaperKey]
}

// wallpaperRestorePath decides the wallpaper a config should show given the
// saved pick: the pick when the config names the stock background, no
// experience pack has the wallpaper, and the file is there; "" leaves the
// config alone. A wallpaper the config names itself wins, the rule the
// experience packs follow too.
func wallpaperRestorePath(current, saved string, packOwns bool, exists func(string) bool) string {
	saved = strings.TrimSpace(saved)
	if saved == "" || strings.EqualFold(saved, wallpaperNoneValue) || packOwns {
		return ""
	}
	if !appWallpaperIsShippedDefault(current) || exists == nil || !exists(saved) {
		return ""
	}
	return saved
}

// wallpaperFileExists reports whether a saved wallpaper is a readable file.
func wallpaperFileExists(path string) bool {
	info, err := os.Stat(resolvePath(path))
	return err == nil && info.Mode().IsRegular()
}

// wallpaperRestoreSaved puts the wallpaper the user picked back on a config
// that names the stock background. jukaconfig.json ships "background.jpg", so
// a pick that only lived in BackgroundImage was gone after a restart. It runs
// once per frame from the backdrop and only does work — one file check — when
// the config, the wallpaper it names, the saved pick or the pack's claim
// changed.
func wallpaperRestoreSaved(config *Config) {
	if config == nil {
		return
	}
	saved, _ := config.Variables.Custom[wallpaperSavedKey].(string)
	_, packOwns := config.Variables.Custom[xpackWallpaperKey]
	st := &wallpaperRestoreState
	if st.valid && config == st.config && saved == st.value && packOwns == st.pack &&
		config.Variables.BackgroundImage == st.current &&
		(st.missingAt.IsZero() || time.Since(st.missingAt) < gifWallRetry) {
		return
	}
	missing := false
	exists := func(p string) bool {
		ok := wallpaperFileExists(p)
		missing = !ok
		return ok
	}
	if path := wallpaperRestorePath(config.Variables.BackgroundImage, saved, packOwns, exists); path != "" {
		config.Variables.BackgroundImage = path
	}
	st.config, st.value, st.pack, st.valid = config, saved, packOwns, true
	st.current = config.Variables.BackgroundImage
	st.missingAt = time.Time{}
	if missing {
		st.missingAt = time.Now()
	}
}
