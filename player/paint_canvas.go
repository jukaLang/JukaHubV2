package main

// paint_canvas.go — Paint's picture in memory: the pixels, the drawing tools'
// pixel work, undo and redo, and the one texture the screen shows.
//
// The picture is an *image.NRGBA, the same straight-alpha layout SDL's
// ABGR8888 texture has on these little-endian machines, so a change is
// uploaded by pointing the texture at the changed rows and nothing is
// converted. Only the rectangle that changed since the last frame is
// uploaded: a pencil dot on a 1280×720 markup is a few hundred bytes, not
// 3.6 MB.
//
// Undo keeps patches, not whole pictures. A second copy of the picture
// ("shadow") holds it as it was when the last stroke ended; when a stroke ends,
// the rectangle it touched is copied out of the shadow (before) and out of the
// picture (after) into the history, and the shadow catches up. The same
// shadow lets a shape's preview be real pixels: each move puts back the
// shadow under the previous preview and draws the shape again, so what is
// shown while moving is exactly what A will keep. The history is capped by
// bytes (paintUndoBudget), oldest first, because a fill over a whole
// screenshot is 7 MB of before-and-after and the handheld has 1 GB.

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// paintUndoBudget is what the undo and redo history may hold, in bytes.
	paintUndoBudget = 24 << 20
	// paintUndoKeepOnLeave is the history kept while Paint is not on screen.
	paintUndoKeepOnLeave = 8 << 20
	// paintMaxEdge is the longest side a picture is opened at: a phone photo
	// is shrunk to it, a screenshot of any TrimUI screen is not.
	paintMaxEdge = 1920
	// paintFillTolerance is how far (per channel, summed) a pixel may differ
	// from the one clicked and still be filled on a screenshot, whose flat
	// areas are rarely one exact colour. A drawing fills exact colours only.
	paintFillTolerance = 48
)

// paintTool is one of the drawing tools.
type paintTool int

const (
	paintPencil paintTool = iota
	paintEraser
	paintLine
	paintRect
	paintEllipse
	paintArrow
	paintFill
	paintPicker
	paintToolCount
)

// paintToolNames are the tools' names, in the tool sheet's order.
var paintToolNames = [paintToolCount]string{
	"Pencil", "Eraser", "Line", "Rectangle", "Circle / ellipse", "Arrow", "Fill", "Eyedropper",
}

// paintToolShape reports whether a tool is placed with two presses (a corner,
// then the other) rather than drawn while held.
func paintToolShape(t paintTool) bool {
	return t == paintLine || t == paintRect || t == paintEllipse || t == paintArrow
}

// paintBrushSizes are the brush sizes, in canvas pixels.
var paintBrushSizes = [...]int{1, 2, 4, 8}

// paintPatch is one undo step: the rectangle a stroke touched, as it was and
// as the stroke left it.
type paintPatch struct {
	r             image.Rectangle
	before, after []byte
}

// paintPatchBytes is what a patch holds.
func paintPatchBytes(p paintPatch) int { return len(p.before) + len(p.after) }

// paintDoc is the picture being drawn.
type paintDoc struct {
	img *image.NRGBA
	// orig is the screenshot under a markup, or a sketch's white paper; the
	// eraser puts its pixels back. nil for pixel art, whose eraser leaves
	// transparency.
	orig *image.NRGBA
	// shadow is img as the last finished stroke left it.
	shadow *image.NRGBA
	w, h   int
	// source is the picture this was opened from ("" for a new drawing);
	// savedPath the PNG it was last saved as.
	source    string
	savedPath string
	// pixelArt is a small canvas: whole-number zoom, square brushes, a grid.
	pixelArt bool
	// changed is set by every finished stroke, cleared by a save; autoDirty
	// the same for the autosave.
	changed   bool
	autoDirty bool
	// rev counts the changes, so a save that finishes after another stroke
	// does not mark the drawing saved.
	rev int
	// origSaved is set once the screenshot under a markup is in the
	// autosave, which it only has to be once.
	origSaved bool

	undo, redo []paintPatch
	histBytes  int

	// stroke is the union of what the stroke in progress touched; preview
	// what the shape preview drew last.
	stroke  image.Rectangle
	preview image.Rectangle
	// upload is what changed since the texture was last updated.
	upload image.Rectangle
}

// newPaintDoc wraps a picture. orig may be nil.
func newPaintDoc(img, orig *image.NRGBA, source string, pixelArt bool) *paintDoc {
	b := img.Rect
	d := &paintDoc{img: img, orig: orig, w: b.Dx(), h: b.Dy(), source: source, pixelArt: pixelArt}
	d.shadow = paintCloneNRGBA(img)
	d.upload = b
	return d
}

// paintCloneNRGBA copies a picture.
func paintCloneNRGBA(src *image.NRGBA) *image.NRGBA {
	if src == nil {
		return nil
	}
	dst := image.NewNRGBA(src.Rect)
	copy(dst.Pix, src.Pix)
	return dst
}

// in reports whether a canvas point is on the picture.
func (d *paintDoc) in(x, y int) bool { return x >= 0 && y >= 0 && x < d.w && y < d.h }

// touch adds a rectangle to the stroke and to the next upload.
func (d *paintDoc) touch(r image.Rectangle) {
	r = r.Intersect(d.img.Rect)
	if r.Empty() {
		return
	}
	d.stroke = d.stroke.Union(r)
	d.upload = d.upload.Union(r)
}

// at is a pixel's colour.
func (d *paintDoc) at(x, y int) color.NRGBA {
	i := d.img.PixOffset(x, y)
	p := d.img.Pix[i : i+4 : i+4]
	return color.NRGBA{R: p[0], G: p[1], B: p[2], A: p[3]}
}

// put writes one pixel: erase puts the original back (or transparency).
func (d *paintDoc) put(x, y int, c color.NRGBA, erase bool) {
	i := d.img.PixOffset(x, y)
	p := d.img.Pix[i : i+4 : i+4]
	if erase {
		if d.orig != nil {
			copy(p, d.orig.Pix[i:i+4])
		} else {
			p[0], p[1], p[2], p[3] = 0, 0, 0, 0
		}
		return
	}
	if c.A == 255 || c.A == 0 {
		p[0], p[1], p[2], p[3] = c.R, c.G, c.B, c.A
		return
	}
	// A see-through colour lands over what is there (the eyedropper can pick
	// one up from a drawing).
	a := int(c.A)
	ba := int(p[3])
	outA := a + ba*(255-a)/255
	if outA == 0 {
		p[0], p[1], p[2], p[3] = 0, 0, 0, 0
		return
	}
	mix := func(src, dst uint8) uint8 {
		return uint8((int(src)*a + int(dst)*ba*(255-a)/255) / outA)
	}
	p[0], p[1], p[2], p[3] = mix(c.R, p[0]), mix(c.G, p[1]), mix(c.B, p[2]), uint8(outA)
}

// stamp draws the brush centred on a point: a square on pixel art, a disc
// from 3 pixels up elsewhere, so a markup stroke has round ends.
func (d *paintDoc) stamp(x, y, size int, c color.NRGBA, erase bool) {
	if size < 1 {
		size = 1
	}
	x0, y0 := x-(size-1)/2, y-(size-1)/2
	r := image.Rect(x0, y0, x0+size, y0+size).Intersect(d.img.Rect)
	if r.Empty() {
		return
	}
	round := !d.pixelArt && size >= 3
	cx, cy := float64(x0)+float64(size)/2, float64(y0)+float64(size)/2
	rr := float64(size) * float64(size) / 4
	for py := r.Min.Y; py < r.Max.Y; py++ {
		for px := r.Min.X; px < r.Max.X; px++ {
			if round {
				dx, dy := float64(px)+0.5-cx, float64(py)+0.5-cy
				if dx*dx+dy*dy > rr {
					continue
				}
			}
			d.put(px, py, c, erase)
		}
	}
	d.touch(r)
}

// line draws from one point to another with the brush (Bresenham), the first
// point included.
func (d *paintDoc) line(x0, y0, x1, y1, size int, c color.NRGBA, erase bool) {
	dx, dy := absInt(x1-x0), -absInt(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	e := dx + dy
	// A line from far outside the picture is bounded by its own length,
	// which the picture's size bounds (the cursor never leaves it).
	for n := 0; n < 1<<16; n++ {
		d.stamp(x0, y0, size, c, erase)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * e
		if e2 >= dy {
			e += dy
			x0 += sx
		}
		if e2 <= dx {
			e += dx
			y0 += sy
		}
	}
}

// rectOutline draws a rectangle with corners at the two points.
func (d *paintDoc) rectOutline(x0, y0, x1, y1, size int, c color.NRGBA) {
	d.line(x0, y0, x1, y0, size, c, false)
	d.line(x1, y0, x1, y1, size, c, false)
	d.line(x1, y1, x0, y1, size, c, false)
	d.line(x0, y1, x0, y0, size, c, false)
}

// ellipse draws the ellipse inside the box the two points span: the
// "circle something" tool. It walks the curve in short straight steps, so a
// large one has no gaps and a tiny one is not a blob.
func (d *paintDoc) ellipse(x0, y0, x1, y1, size int, c color.NRGBA) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	if x1-x0 < 2 || y1-y0 < 2 {
		d.rectOutline(x0, y0, x1, y1, size, c)
		return
	}
	cx, cy := float64(x0+x1)/2, float64(y0+y1)/2
	rx, ry := float64(x1-x0)/2, float64(y1-y0)/2
	steps := int(math.Ceil(2 * math.Pi * math.Max(rx, ry) / 3))
	if steps < 24 {
		steps = 24
	}
	if steps > 4096 {
		steps = 4096
	}
	px, py := int(math.Round(cx+rx)), int(math.Round(cy))
	for i := 1; i <= steps; i++ {
		t := 2 * math.Pi * float64(i) / float64(steps)
		nx, ny := int(math.Round(cx+rx*math.Cos(t))), int(math.Round(cy+ry*math.Sin(t)))
		d.line(px, py, nx, ny, size, c, false)
		px, py = nx, ny
	}
}

// arrow draws a line from the first point with a head at the second.
func (d *paintDoc) arrow(x0, y0, x1, y1, size int, c color.NRGBA) {
	d.line(x0, y0, x1, y1, size, c, false)
	length := math.Hypot(float64(x1-x0), float64(y1-y0))
	if length < 2 {
		return
	}
	head := math.Max(6, 4*float64(size))
	head = math.Min(head, length*0.6)
	ang := math.Atan2(float64(y1-y0), float64(x1-x0))
	for _, side := range [2]float64{-1, 1} {
		a := ang + math.Pi - side*0.5
		hx := x1 + int(math.Round(head*math.Cos(a)))
		hy := y1 + int(math.Round(head*math.Sin(a)))
		d.line(x1, y1, hx, hy, size, c, false)
	}
}

// shape draws a two-point tool's shape.
func (d *paintDoc) shape(t paintTool, x0, y0, x1, y1, size int, c color.NRGBA) {
	switch t {
	case paintLine:
		d.line(x0, y0, x1, y1, size, c, false)
	case paintRect:
		d.rectOutline(x0, y0, x1, y1, size, c)
	case paintEllipse:
		d.ellipse(x0, y0, x1, y1, size, c)
	case paintArrow:
		d.arrow(x0, y0, x1, y1, size, c)
	}
}

// restoreFromShadow puts the last finished state back over r (a preview
// being redrawn or cancelled).
func (d *paintDoc) restoreFromShadow(r image.Rectangle) {
	r = r.Intersect(d.img.Rect)
	if r.Empty() {
		return
	}
	paintCopyRect(d.img, d.shadow, r)
	d.upload = d.upload.Union(r)
}

// previewShape redraws the shape preview: the previous one goes, this one is
// drawn, both through the stroke rectangle so the end of the stroke keeps
// whichever was last.
func (d *paintDoc) previewShape(t paintTool, x0, y0, x1, y1, size int, c color.NRGBA) {
	d.restoreFromShadow(d.preview)
	before := d.stroke
	d.stroke = image.Rectangle{}
	d.shape(t, x0, y0, x1, y1, size, c)
	d.preview = d.stroke
	d.stroke = before.Union(d.stroke)
}

// cancelStroke drops the stroke in progress, preview included.
func (d *paintDoc) cancelStroke() {
	d.restoreFromShadow(d.stroke)
	d.restoreFromShadow(d.preview)
	d.stroke, d.preview = image.Rectangle{}, image.Rectangle{}
}

// commitStroke ends the stroke in progress: the touched rectangle becomes
// one undo step and the shadow catches up. It reports whether anything
// changed.
func (d *paintDoc) commitStroke() bool {
	r := d.stroke.Intersect(d.img.Rect)
	d.stroke, d.preview = image.Rectangle{}, image.Rectangle{}
	if r.Empty() {
		return false
	}
	// The stroke rectangle holds every place a shape's preview passed
	// through (put back since): only what really changed is kept, so an
	// ellipse dragged across a screenshot is not a whole-screen undo step.
	r = paintDiffBounds(d.shadow, d.img, r)
	if r.Empty() {
		return false
	}
	before := paintCutRect(d.shadow, r)
	after := paintCutRect(d.img, r)
	paintCopyRect(d.shadow, d.img, r)
	d.pushUndo(paintPatch{r: r, before: before, after: after})
	d.redo = d.dropAll(d.redo)
	d.changed, d.autoDirty = true, true
	d.rev++
	return true
}

// pushUndo adds a step and trims the oldest ones past the budget.
func (d *paintDoc) pushUndo(p paintPatch) {
	d.undo = append(d.undo, p)
	d.histBytes += paintPatchBytes(p)
	d.trimHistory(paintUndoBudget)
}

// dropAll forgets a list of steps and their bytes.
func (d *paintDoc) dropAll(list []paintPatch) []paintPatch {
	for _, p := range list {
		d.histBytes -= paintPatchBytes(p)
	}
	return nil
}

// trimHistory drops redo steps, then the oldest undo steps, until the
// history fits in budget bytes.
func (d *paintDoc) trimHistory(budget int) {
	if d.histBytes > budget && len(d.redo) > 0 {
		d.redo = d.dropAll(d.redo)
	}
	drop := 0
	for d.histBytes > budget && drop < len(d.undo) {
		d.histBytes -= paintPatchBytes(d.undo[drop])
		drop++
	}
	if drop > 0 {
		// A fresh slice, so the dropped patches' memory is really let go.
		d.undo = append([]paintPatch(nil), d.undo[drop:]...)
	}
}

// undoStep takes back the last step; redoStep does it again. They report
// whether there was one.
func (d *paintDoc) undoStep() bool {
	if len(d.undo) == 0 {
		return false
	}
	p := d.undo[len(d.undo)-1]
	d.undo = d.undo[:len(d.undo)-1]
	d.applyPatch(p.r, p.before)
	d.redo = append(d.redo, p)
	return true
}

func (d *paintDoc) redoStep() bool {
	if len(d.redo) == 0 {
		return false
	}
	p := d.redo[len(d.redo)-1]
	d.redo = d.redo[:len(d.redo)-1]
	d.applyPatch(p.r, p.after)
	d.undo = append(d.undo, p)
	return true
}

// applyPatch writes saved pixels into the picture and the shadow.
func (d *paintDoc) applyPatch(r image.Rectangle, pix []byte) {
	paintPasteRect(d.img, r, pix)
	paintPasteRect(d.shadow, r, pix)
	d.upload = d.upload.Union(r)
	d.changed, d.autoDirty = true, true
	d.rev++
}

// paintDiffBounds narrows r to the pixels that differ between two pictures of
// the same size; empty when none do.
func paintDiffBounds(a, b *image.NRGBA, r image.Rectangle) image.Rectangle {
	var out image.Rectangle
	n := r.Dx() * 4
	for y := r.Min.Y; y < r.Max.Y; y++ {
		i := a.PixOffset(r.Min.X, y)
		ra, rb := a.Pix[i:i+n], b.Pix[i:i+n]
		if bytes.Equal(ra, rb) {
			continue
		}
		x0 := 0
		for x0 < n && ra[x0] == rb[x0] {
			x0++
		}
		x1 := n
		for x1 > x0 && ra[x1-1] == rb[x1-1] {
			x1--
		}
		out = out.Union(image.Rect(r.Min.X+x0/4, y, r.Min.X+(x1+3)/4, y+1))
	}
	return out
}

// paintCutRect copies a rectangle's pixels out, row after row.
func paintCutRect(src *image.NRGBA, r image.Rectangle) []byte {
	rowBytes := r.Dx() * 4
	out := make([]byte, rowBytes*r.Dy())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		i := src.PixOffset(r.Min.X, y)
		copy(out[(y-r.Min.Y)*rowBytes:], src.Pix[i:i+rowBytes])
	}
	return out
}

// paintPasteRect writes pixels cut by paintCutRect back.
func paintPasteRect(dst *image.NRGBA, r image.Rectangle, pix []byte) {
	rowBytes := r.Dx() * 4
	for y := r.Min.Y; y < r.Max.Y; y++ {
		i := dst.PixOffset(r.Min.X, y)
		copy(dst.Pix[i:i+rowBytes], pix[(y-r.Min.Y)*rowBytes:])
	}
}

// paintCopyRect copies a rectangle between two pictures of the same size.
func paintCopyRect(dst, src *image.NRGBA, r image.Rectangle) {
	rowBytes := r.Dx() * 4
	for y := r.Min.Y; y < r.Max.Y; y++ {
		i := dst.PixOffset(r.Min.X, y)
		copy(dst.Pix[i:i+rowBytes], src.Pix[i:i+rowBytes])
	}
}

// fill floods the area of one colour around a point with c (the eraser's
// colour when erase): a scanline fill with its own stack, so a whole
// screenshot cannot overflow anything, and a visited map, so a tolerance
// cannot loop.
func (d *paintDoc) fill(x, y int, c color.NRGBA, erase bool) {
	if !d.in(x, y) {
		return
	}
	target := d.at(x, y)
	tol := 0
	if d.orig != nil {
		tol = paintFillTolerance
	}
	if !erase && tol == 0 && target == c {
		return
	}
	match := func(px, py int) bool {
		i := d.shadow.PixOffset(px, py)
		p := d.shadow.Pix[i : i+4 : i+4]
		diff := absInt(int(p[0])-int(target.R)) + absInt(int(p[1])-int(target.G)) +
			absInt(int(p[2])-int(target.B)) + absInt(int(p[3])-int(target.A))
		return diff <= tol
	}
	seen := make([]bool, d.w*d.h)
	stack := [][2]int{{x, y}}
	minX, minY, maxX, maxY := x, y, x, y
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		px, py := p[0], p[1]
		if seen[py*d.w+px] || !match(px, py) {
			continue
		}
		// Run left and right along the row.
		l := px
		for l > 0 && !seen[py*d.w+l-1] && match(l-1, py) {
			l--
		}
		r := px
		for r < d.w-1 && !seen[py*d.w+r+1] && match(r+1, py) {
			r++
		}
		for i := l; i <= r; i++ {
			seen[py*d.w+i] = true
			d.put(i, py, c, erase)
		}
		// One seed per run of fillable pixels above and below, not one per
		// pixel: a fill over a whole screenshot keeps a short stack instead
		// of tens of megabytes of it.
		for _, ny := range [2]int{py - 1, py + 1} {
			if ny < 0 || ny >= d.h {
				continue
			}
			inRun := false
			for i := l; i <= r; i++ {
				if seen[ny*d.w+i] || !match(i, ny) {
					inRun = false
					continue
				}
				if !inRun {
					stack = append(stack, [2]int{i, ny})
					inRun = true
				}
			}
		}
		minX, maxX = min(minX, l), max(maxX, r)
		minY, maxY = min(minY, py), max(maxY, py)
	}
	d.touch(image.Rect(minX, minY, maxX+1, maxY+1))
}

// ── The texture ───────────────────────────────────────────────────────────

// paintTex is the canvas's texture and how it samples.
var paintTex struct {
	tex    *sdl.Texture
	w, h   int
	linear bool
}

// paintReleaseTexture frees the canvas texture.
func paintReleaseTexture() {
	if paintTex.tex != nil {
		paintTex.tex.Destroy()
	}
	paintTex.tex = nil
	paintTex.w, paintTex.h = 0, 0
}

// paintTexture is the canvas's texture with every change uploaded. A canvas
// shown smaller than its pixels (a screenshot fitted to the screen) is
// sampled smoothly, one shown at whole pixels or larger sharply; crossing
// between them makes a new texture, because the filter is fixed when a
// texture is made.
func paintTexture(renderer *sdl.Renderer, d *paintDoc, linear bool) *sdl.Texture {
	if renderer == nil || d == nil || d.img == nil {
		return nil
	}
	if paintTex.tex != nil && (paintTex.w != d.w || paintTex.h != d.h || paintTex.linear != linear) {
		paintReleaseTexture()
	}
	if paintTex.tex == nil {
		prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
		quality := "0"
		if linear {
			quality = "1"
		}
		sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, quality)
		tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STREAMING, int32(d.w), int32(d.h))
		sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
		if err != nil || tex == nil {
			return nil
		}
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
		paintTex.tex, paintTex.w, paintTex.h, paintTex.linear = tex, d.w, d.h, linear
		d.upload = d.img.Rect
	}
	if r := d.upload.Intersect(d.img.Rect); !r.Empty() {
		rect := sdl.Rect{X: int32(r.Min.X), Y: int32(r.Min.Y), W: int32(r.Dx()), H: int32(r.Dy())}
		off := d.img.PixOffset(r.Min.X, r.Min.Y)
		_ = paintTex.tex.Update(&rect, unsafe.Pointer(&d.img.Pix[off]), d.img.Stride)
	}
	d.upload = image.Rectangle{}
	return paintTex.tex
}

// paintChecker is a small checkerboard tiled under transparent pixels.
var paintChecker *sdl.Texture

// paintCheckerSize is the checker texture's side; its squares are 8 pixels.
const paintCheckerSize = 128

// paintCheckerTexture makes the checkerboard once.
func paintCheckerTexture(renderer *sdl.Renderer) *sdl.Texture {
	if paintChecker != nil || renderer == nil {
		return paintChecker
	}
	pic := image.NewNRGBA(image.Rect(0, 0, paintCheckerSize, paintCheckerSize))
	for y := 0; y < paintCheckerSize; y++ {
		for x := 0; x < paintCheckerSize; x++ {
			v := uint8(205)
			if (x/8+y/8)%2 == 0 {
				v = 245
			}
			i := pic.PixOffset(x, y)
			pic.Pix[i], pic.Pix[i+1], pic.Pix[i+2], pic.Pix[i+3] = v, v, v, 255
		}
	}
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, paintCheckerSize, paintCheckerSize)
	if err != nil || tex == nil {
		return nil
	}
	if err := tex.Update(nil, unsafe.Pointer(&pic.Pix[0]), pic.Stride); err != nil {
		tex.Destroy()
		return nil
	}
	paintChecker = tex
	return tex
}

// paintReleaseChecker frees the checkerboard.
func paintReleaseChecker() {
	if paintChecker != nil {
		paintChecker.Destroy()
		paintChecker = nil
	}
}
