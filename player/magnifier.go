package main

// magnifier.go — A11Y-7: a zoom lens that follows the focus.
//
// The lens is a bounded copy of the finished frame. Once every refresh it reads
// the pixels of a region around the focused control, uploads them into ONE
// streaming texture (the only allocation, and never more than the lens itself)
// and draws that texture magnified in a card beside the control. Nothing is
// re-rendered: drawing every screen a second time into a render target would
// double the frame cost on the PowerVR handheld.
//
// The lens sits beside the focus, never over it. A lens centred on the thing it
// magnifies would read its own previous frame back and feed the picture into
// itself; below/above placement also keeps the real control visible while its
// larger copy is read.

import (
	"strconv"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	magnifierGap       = int32(12) // between the focus and the lens
	magnifierMargin    = int32(8)  // from the screen edge
	magnifierMinLensW  = int32(260)
	magnifierMaxLensW  = int32(560)
	magnifierMinLensH  = int32(64)   // below this the lens is not worth drawing
	magnifierRefreshMS = uint64(100) // a stale-by-a-tenth lens is still legible
	magnifierMinSrcW   = int32(48)
)

type magnifierState struct {
	tex *sdl.Texture
	buf []byte
	// srcW/srcH are the texture's pixels, which are the source rect scaled by
	// the renderer's output scale on a high-DPI window.
	srcW, srcH int32
	outW, outH int32
	// captured is the logical source rect held by tex, which is how a re-read
	// is decided when the focus moves or the zoom changes.
	captured sdl.Rect
	valid    bool
	lastMS   uint64
	// failed records a backend that cannot read pixels back: the lens then
	// stays off for the session instead of retrying every frame.
	failed bool
}

var magnifier magnifierState

// a11yClampMagnifierZoom normalises the stored factor: only 2× and 3× exist.
func a11yClampMagnifierZoom(zoom int) int {
	if zoom == 3 {
		return 3
	}
	return 2
}

// magnifierLensSize is the lens box for a focus. It is the screen's share wide
// and 16:9 tall, shortened to the room on the roomier side of the focus; a
// focus with no room either side returns 0 and no lens is drawn.
func magnifierLensSize(focus sdl.Rect) (int32, int32) {
	w := screenWidth * 2 / 5
	if w > magnifierMaxLensW {
		w = magnifierMaxLensW
	}
	if w < magnifierMinLensW {
		w = magnifierMinLensW
	}
	if max := screenWidth - 2*magnifierMargin; w > max {
		w = max
	}
	h := w * 9 / 16
	if max := screenHeight / 2; h > max {
		h = max
	}
	above := focus.Y - magnifierMargin - magnifierGap
	below := screenHeight - magnifierMargin - magnifierGap - (focus.Y + focus.H)
	room := above
	if below > room {
		room = below
	}
	if room < h {
		h = room
	}
	if w <= 0 || h < magnifierMinLensH {
		return 0, 0
	}
	return w, h
}

// magnifierSourceRect is the part of the frame the lens shows: a lens-sized
// region divided by the zoom, centred on the focus and kept on screen.
func magnifierSourceRect(focus sdl.Rect) sdl.Rect {
	lensW, lensH := magnifierLensSize(focus)
	if lensW <= 0 || lensH <= 0 {
		return sdl.Rect{}
	}
	zoom := int32(a11yClampMagnifierZoom(a11y.magnifierZoom))
	srcW, srcH := lensW/zoom, lensH/zoom
	if srcW < magnifierMinSrcW {
		srcW = magnifierMinSrcW
	}
	if srcH < magnifierMinSrcW {
		srcH = magnifierMinSrcW
	}
	if srcW > screenWidth {
		srcW = screenWidth
	}
	if srcH > screenHeight {
		srcH = screenHeight
	}
	r := sdl.Rect{
		X: focus.X + focus.W/2 - srcW/2,
		Y: focus.Y + focus.H/2 - srcH/2,
		W: srcW,
		H: srcH,
	}
	if r.X < 0 {
		r.X = 0
	}
	if r.Y < 0 {
		r.Y = 0
	}
	if r.X+r.W > screenWidth {
		r.X = screenWidth - r.W
	}
	if r.Y+r.H > screenHeight {
		r.Y = screenHeight - r.H
	}
	return r
}

// magnifierLensRect places the lens below the focus when there is room, else
// above it. It reports false when neither side can hold a lens clear of the
// source, which is also what stops the lens from magnifying itself.
func magnifierLensRect(focus, src sdl.Rect) (sdl.Rect, bool) {
	lensW, lensH := magnifierLensSize(focus)
	if lensW <= 0 || lensH <= 0 {
		return sdl.Rect{}, false
	}
	x := src.X + src.W/2 - lensW/2
	if x < magnifierMargin {
		x = magnifierMargin
	}
	if max := screenWidth - lensW - magnifierMargin; x > max {
		x = max
	}
	// The gap is measured from the source, not the focus: a source clamped to
	// a screen edge reaches further than the control does, and a lens placed
	// against the focus alone could sit on the pixels it is magnifying.
	candidates := [2]sdl.Rect{
		{X: x, Y: src.Y + src.H + magnifierGap, W: lensW, H: lensH},
		{X: x, Y: src.Y - magnifierGap - lensH, W: lensW, H: lensH},
	}
	for _, r := range candidates {
		if r.Y < magnifierMargin || r.Y+r.H > screenHeight-magnifierMargin {
			continue
		}
		if magnifierOverlap(r, src) {
			continue
		}
		return r, true
	}
	return sdl.Rect{}, false
}

// magnifierOverlap reports whether two rects share any pixels.
func magnifierOverlap(a, b sdl.Rect) bool {
	return a.W > 0 && a.H > 0 && b.W > 0 && b.H > 0 &&
		a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// magnifierRead copies src into the lens texture. The buffer and the texture
// are reused, so a refresh allocates nothing after the first frame.
func magnifierRead(renderer *sdl.Renderer, src sdl.Rect, ow, oh int32) bool {
	if renderer == nil || src.W <= 0 || src.H <= 0 || ow <= 0 || oh <= 0 {
		return false
	}
	sx := float64(ow) / float64(screenWidth)
	sy := float64(oh) / float64(screenHeight)
	read := sdl.Rect{
		X: int32(float64(src.X) * sx),
		Y: int32(float64(src.Y) * sy),
		W: int32(float64(src.W)*sx + 0.999),
		H: int32(float64(src.H)*sy + 0.999),
	}
	if read.X < 0 {
		read.X = 0
	}
	if read.Y < 0 {
		read.Y = 0
	}
	if read.X+read.W > ow {
		read.W = ow - read.X
	}
	if read.Y+read.H > oh {
		read.H = oh - read.Y
	}
	if read.W <= 0 || read.H <= 0 {
		return false
	}
	pitch := int(read.W) * 4
	need := pitch * int(read.H)
	if cap(magnifier.buf) < need {
		magnifier.buf = make([]byte, need)
	}
	magnifier.buf = magnifier.buf[:need]
	if err := renderer.ReadPixels(&read, sdl.PIXELFORMAT_ABGR8888,
		unsafe.Pointer(&magnifier.buf[0]), pitch); err != nil {
		magnifier.failed = true
		return false
	}
	if magnifier.tex == nil || magnifier.srcW != read.W || magnifier.srcH != read.H {
		if magnifier.tex != nil {
			magnifier.tex.Destroy()
			magnifier.tex = nil
		}
		tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888,
			sdl.TEXTUREACCESS_STREAMING, read.W, read.H)
		if err != nil {
			magnifier.failed = true
			return false
		}
		magnifier.tex = tex
		magnifier.srcW, magnifier.srcH = read.W, read.H
	}
	if err := magnifier.tex.Update(nil, unsafe.Pointer(&magnifier.buf[0]), pitch); err != nil {
		magnifier.failed = true
		return false
	}
	magnifier.captured, magnifier.outW, magnifier.outH = src, ow, oh
	magnifier.valid = true
	magnifier.lastMS = sdl.GetTicks64()
	return true
}

// magnifierRender draws the lens for the focused control. It runs at the end of
// a frame, before Present, from the main loop.
func magnifierRender(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !a11y.magnifier || magnifier.failed {
		return
	}
	node, ok := focusEngine.Current()
	if !ok || node.Rect.W <= 0 || node.Rect.H <= 0 {
		return
	}
	src := magnifierSourceRect(node.Rect)
	if src.W <= 0 || src.H <= 0 {
		return
	}
	dst, ok := magnifierLensRect(node.Rect, src)
	if !ok {
		return
	}
	ow, oh, err := renderer.GetOutputSize()
	if err != nil || ow <= 0 || oh <= 0 {
		return
	}
	now := sdl.GetTicks64()
	// The focus moved, the zoom changed, the window was resized, or the lens
	// has been showing the same pixels for a tenth of a second: re-read. The
	// lens is never over its own source, so the previous one cannot be in the
	// copy.
	stale := !magnifier.valid || magnifier.captured != src || magnifier.outW != ow || magnifier.outH != oh
	if stale || now-magnifier.lastMS >= magnifierRefreshMS {
		magnifierRead(renderer, src, ow, oh)
	}
	if !magnifier.valid || magnifier.tex == nil {
		return
	}
	magnifierDraw(renderer, config, dst)
}

// magnifierDraw paints the lens card, the enlarged copy and the zoom badge.
func magnifierDraw(renderer *sdl.Renderer, config *Config, dst sdl.Rect) {
	const radius = int32(16)
	fillRoundedRect(renderer, dst.X, dst.Y, dst.W, dst.H, radius, glassFillFocus())
	inner := sdl.Rect{X: dst.X + 4, Y: dst.Y + 4, W: dst.W - 8, H: dst.H - 8}
	_ = renderer.Copy(magnifier.tex, nil, &inner)
	strokeRoundedRect(renderer, dst.X, dst.Y, dst.W, dst.H, radius, 2, glassAccent())

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	label := strconv.Itoa(a11yClampMagnifierZoom(a11y.magnifierZoom)) + "×"
	w, h, err := font.SizeUTF8(label)
	if err != nil || w <= 0 {
		return
	}
	chip := sdl.Rect{
		X: dst.X + 10,
		Y: dst.Y + 10,
		W: int32(w) + 16,
		H: int32(h) + 8,
	}
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, sdl.Color{R: 12, G: 16, B: 26, A: 210})
	renderText(renderer, config, font, label, sdl.Color{R: 255, G: 255, B: 255, A: 235}, chip.X+8, chip.Y+4)
}

// magnifierReset drops the cached copy, for the switch turning off and for the
// tests. The lens then re-reads on its next frame.
func magnifierReset() {
	if magnifier.tex != nil {
		magnifier.tex.Destroy()
	}
	magnifier = magnifierState{}
}
