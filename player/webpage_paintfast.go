package main

// webpage_paintfast.go — fewer, cheaper draw calls per page frame.
//
// The layout keeps one run per word: links, find-in-page, the D-pad and every
// test read words. drawWebDocument used to draw them that way too, one text
// texture and one draw call per word: a full screen of text on the 1280×720
// handhelds is 250-300 copies per frame, and every new word scrolled in was a
// texture of its own in a 512-entry cache the rest of the UI shares. Each
// underlined link and each <code> chip also measured its text again with
// TTF_SizeUTF8 every frame.
//
// The painter now draws a row from paint runs (webPaintRow, built once per row
// the first time it is drawn): consecutive word runs in the same face, colour,
// decoration, link, background and offset that sit exactly one space apart
// are set as one string, measured once. A paragraph line is one or two draw
// calls, an underline runs under the spaces of a link as in a browser, and the
// widths come from the cache. Letter-spaced and justified lines, whose gaps
// are not a plain space, keep their words apart.
//
// Rounded page boxes, their borders and gradients used to go through the
// shape cache: one texture per box size, each baked through a render target
// the first frame it showed — a tile flush on the Mali and PowerVR GPUs, for
// every new card, button and <code> chip scrolled in. They are now one
// SDL_RenderGeometry fan each (webPaintFill, webPaintStroke), and a gradient
// is one strip with a vertex at every stop instead of 96 one-colour bands, so
// it is smooth as well as cheaper. Where SDL cannot draw geometry the old
// paths are used.

import (
	"math"
	"sort"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// webPaintRun is one stretch of a line as the painter draws it: one or more
// of the layout's word runs set as a single string. run is the first word run
// with Text holding the whole stretch; w and h are its measured size.
type webPaintRun struct {
	run webRunPos
	w   int32
	h   int32
}

// webPaintRow is one layout row's paint runs, built on its first draw.
type webPaintRow struct {
	built bool
	runs  []webPaintRun
}

// paintRuns returns row i's paint runs, building them the first time.
// Render thread.
func (l *webLayout) paintRuns(i int) []webPaintRun {
	if l == nil || i < 0 || i >= len(l.Rows) {
		return nil
	}
	if len(l.paint) != len(l.Rows) {
		l.paint = make([]webPaintRow, len(l.Rows))
	}
	p := &l.paint[i]
	if !p.built {
		p.runs = webBuildPaintRuns(l.Rows[i].Runs)
		p.built = true
	}
	return p.runs
}

// webBuildPaintRuns joins a row's word runs into paint runs. Invisible runs,
// inline images (painted from layout.Images) and lifted words (painted by
// their lifted layer, webLiftPositioned) are left out.
func webBuildPaintRuns(runs []webRunPos) []webPaintRun {
	if len(runs) == 0 {
		return nil
	}
	out := make([]webPaintRun, 0, len(runs))
	var spaceFont *ttf.Font
	spaceW := int32(-1)
	// Joining words with += copied the whole stretch again for every word (a
	// 30-word line built ~60 temporary strings). The builder grows in place;
	// the string is materialised once, when the stretch closes.
	var text strings.Builder
	closeRun := func() {
		if len(out) > 0 {
			out[len(out)-1].run.Text = text.String()
		}
	}
	for _, run := range runs {
		if run.Invisible || run.Image != nil || run.Lifted {
			continue
		}
		w, h := int32(0), int32(0)
		if run.Font != nil {
			if fw, fh, err := run.Font.SizeUTF8(run.Text); err == nil {
				w, h = int32(fw), int32(fh)
			}
		}
		if n := len(out); n > 0 {
			prev := &out[n-1]
			if run.Font != spaceFont {
				spaceFont, spaceW = run.Font, -1
				if run.Font != nil {
					if sw, _, err := run.Font.SizeUTF8(" "); err == nil {
						spaceW = int32(sw)
					}
				}
			}
			if gap, ok := webPaintJoinGap(prev, &run, spaceW); ok {
				if gap > 0 {
					text.WriteByte(' ')
				}
				text.WriteString(run.Text)
				prev.w = run.X + w - prev.run.X
				prev.h = max(prev.h, h)
				// The stretch now ends where the word did: at its inline box's
				// right edge, when the word closed one (webpage_inlinebox.go).
				prev.run.ChipTrail, prev.run.ChipRight = run.ChipTrail, run.ChipRight
				continue
			}
		}
		closeRun()
		text.Reset()
		text.WriteString(run.Text)
		out = append(out, webPaintRun{run: run, w: w, h: h})
	}
	closeRun()
	return out
}

// webPaintJoinGap reports whether run continues prev's stretch, and the gap
// between them: 0 when it starts where prev ends, spaceW when one automatic
// space separates them. Anything else (a stretched justified gap, letter
// spacing, word spacing, another cell) keeps them apart, and so does any
// difference in how the two are painted.
func webPaintJoinGap(prev *webPaintRun, run *webRunPos, spaceW int32) (int32, bool) {
	a := &prev.run
	if a.Font == nil || run.Font != a.Font || spaceW < 0 {
		return 0, false
	}
	if a.Control != nil || run.Control != nil || a.BoxW != 0 || a.BoxH != 0 || run.BoxW != 0 || run.BoxH != 0 {
		return 0, false
	}
	if a.Color != run.Color || a.Bold != run.Bold || a.Underline != run.Underline || a.Strike != run.Strike ||
		a.DecorationColor != run.DecorationColor || a.HasDecorationColor != run.HasDecorationColor ||
		a.DecorationThickness != run.DecorationThickness || a.DecorationStyle != run.DecorationStyle ||
		a.DecorationOffset != run.DecorationOffset {
		return 0, false
	}
	if a.HasShadow != run.HasShadow || a.ShadowX != run.ShadowX || a.ShadowY != run.ShadowY ||
		a.ShadowColor != run.ShadowColor || a.HasShadowColor != run.HasShadowColor {
		return 0, false
	}
	// Two links side by side stay apart: the space between them belongs to
	// neither and is not underlined.
	if a.Href != run.Href || a.HasBg != run.HasBg || a.Bg != run.Bg || a.VerticalOffset != run.VerticalOffset ||
		a.HasClip != run.HasClip || a.Clip != run.Clip {
		return 0, false
	}
	// A word stretch ends at an inline box's edge, and words in different
	// boxes stay apart (webpage_inlinebox.go).
	if a.ChipTrail != 0 || a.ChipRight || run.ChipLead != 0 || run.ChipLeft || (a.Chip == nil) != (run.Chip == nil) ||
		(a.Chip != nil && *a.Chip != *run.Chip) {
		return 0, false
	}
	gap := run.X - (a.X + prev.w)
	switch {
	case gap == 0 && run.SpaceBefore == 0:
		return 0, true
	case gap == spaceW && run.SpaceBefore == spaceW && spaceW > 0:
		return gap, true
	}
	return 0, false
}

// drawWebPaintRow draws one row's text: inline background chips first (one
// chip across neighbouring stretches with the same colour), then each stretch
// with its shadow, fake weight and decorations. x0/y are the page's left edge
// and the row's top on screen; rect and offset place clips. Render thread.
func drawWebPaintRow(r *sdl.Renderer, config *Config, runs []webPaintRun, x0, y int32, rect sdl.Rect, offset int32) {
	drawWebPaintRowAt(r, config, runs, x0, y, rect, rect.X, rect.Y-offset)
}

// drawWebPaintRowAt is drawWebPaintRow with the screen origin of the runs'
// clips given directly: a layer's runs are clipped in its own coordinates
// (webpage_layers.go). rect still bounds every clip.
func drawWebPaintRowAt(r *sdl.Renderer, config *Config, runs []webPaintRun, x0, y int32, rect sdl.Rect, clipX, clipY int32) {
	for i := 0; i < len(runs); i++ {
		pr := &runs[i]
		run := &pr.run
		if !(run.HasBg || run.Chip != nil) || run.Font == nil {
			continue
		}
		if run.BoxW > 0 && run.BoxH > 0 {
			// The field's frame goes where its text goes: VerticalOffset below
			// the row (a field in a centred flex item or a grid track).
			webPaintField(r, x0+run.X, y+run.VerticalOffset, run.BoxW, run.BoxH, run.Bg, run.Field)
			continue
		}
		if run.Chip != nil {
			// An inline box: its padding, border and radius
			// (webpage_inlinebox.go).
			i = webPaintChipSpan(r, runs, i, x0, y)
			continue
		}
		startX := x0 + run.X - 2
		endX := x0 + run.X + pr.w
		h := pr.h
		for j := i + 1; j < len(runs); j++ {
			next := &runs[j]
			// Two controls side by side (the region and time selects of a
			// search page) are two chips, not one bar.
			if !next.run.HasBg || next.run.Bg != run.Bg || next.run.Font == nil || next.run.BoxW > 0 || next.run.Chip != nil ||
				next.run.Control != run.Control {
				break
			}
			endX = x0 + next.run.X + next.w
			h = max(h, next.h)
			i = j
		}
		// The chip sits behind the glyphs, which the baseline and
		// vertical-align may have moved down the row. A select keeps its
		// page's border and corners (webPaintField).
		if run.Field != nil {
			webPaintField(r, startX, y+run.VerticalOffset, endX-startX+4, h, run.Bg, run.Field)
		} else {
			webPaintFill(r, startX, y+run.VerticalOffset, endX-startX+4, h, 3, run.Bg)
		}
	}
	for i := range runs {
		pr := &runs[i]
		run := &pr.run
		x, textY := x0+run.X, y+run.VerticalOffset
		restore := webClipPaint(r, rect, run.Clip, run.HasClip, clipX, clipY)
		if run.BoxW > 0 && run.BoxH > 0 && run.Font != nil {
			x += 6
			textY += (run.BoxH - int32(run.Font.Height())) / 2
		}
		if run.HasShadow {
			shadow := run.ShadowColor
			if !run.HasShadowColor {
				shadow = run.Color
			}
			drawTextRun(r, config, run.Font, run.Text, shadow, x+run.ShadowX, textY+run.ShadowY)
		}
		drawTextRun(r, config, run.Font, run.Text, run.Color, x, textY)
		if run.Bold && !webFontIsBold(run.Font) {
			// A bold run is laid out in a bold face (webpage_fonts.go).
			// Only when none would open is it the regular face, and a
			// second copy a pixel over is the weight it can have.
			drawTextRun(r, config, run.Font, run.Text, run.Color, x+1, textY)
		}
		if (run.Underline || run.Strike) && run.Font != nil && pr.w > 0 {
			decoration := run.Color
			if run.HasDecorationColor {
				decoration = run.DecorationColor
			}
			r.SetDrawColor(decoration.R, decoration.G, decoration.B, 220)
			thickness := run.DecorationThickness
			if thickness < 1 {
				thickness = 1
			}
			if run.Underline {
				for _, seg := range webDecorationPaintRects(x0+run.X, webUnderlinePaintY(run, textY), pr.w, thickness, run.DecorationStyle) {
					r.FillRect(&seg)
				}
			}
			if run.Strike {
				for _, seg := range webDecorationPaintRects(x0+run.X, textY+int32(run.Font.Height())/2, pr.w, thickness, run.DecorationStyle) {
					r.FillRect(&seg)
				}
			}
		}
		restore()
	}
}

// --- Geometry ----------------------------------------------------------------

// webGeomFailed is set the first time SDL_RenderGeometry refuses (an SDL older
// than 2.0.18, or a renderer without it): page boxes go back to the shape
// cache and gradients to bands from then on.
var webGeomFailed bool

// webGeom is the scratch the fans and strips are built in, reused from draw to
// draw. Render thread.
var webGeom struct {
	pts   []sdl.FPoint
	inner []sdl.FPoint
	v     []sdl.Vertex
	idx   []int32
	cuts  []float64
}

// webCornerSegments is how many chords one rounded corner is drawn with: a
// small radius needs few, and 10 keep a 48-px pill smooth.
func webCornerSegments(radius float64) int {
	switch {
	case radius <= 3:
		return 2
	case radius <= 8:
		return 4
	case radius <= 24:
		return 6
	}
	return 10
}

// webRoundedOutline appends the outline of the rounded rect (x, y, w, h) with
// corner radius rad to dst, clockwise on screen from the top-right corner,
// segs chords per corner. Every point lies on the outline, so the chords stay
// inside the curve and no pixel outside it is covered.
func webRoundedOutline(dst []sdl.FPoint, x, y, w, h, rad float64, segs int) []sdl.FPoint {
	centres := [4][3]float64{
		{x + w - rad, y + rad, -math.Pi / 2}, // top-right: from the top edge to the right edge
		{x + w - rad, y + h - rad, 0},        // bottom-right
		{x + rad, y + h - rad, math.Pi / 2},  // bottom-left
		{x + rad, y + rad, math.Pi},          // top-left
	}
	for _, c := range centres {
		for k := 0; k <= segs; k++ {
			a := c[2] + float64(k)/float64(segs)*math.Pi/2
			dst = append(dst, sdl.FPoint{X: float32(c[0] + rad*math.Cos(a)), Y: float32(c[1] + rad*math.Sin(a))})
		}
	}
	return dst
}

// webGeomDraw hands the scratch vertices and indices to SDL, untextured and
// blended. A refusal switches geometry off for the session.
func webGeomDraw(r *sdl.Renderer) bool {
	if len(webGeom.v) == 0 || len(webGeom.idx) == 0 {
		return true
	}
	_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if err := r.RenderGeometry(nil, webGeom.v, webGeom.idx); err != nil {
		webGeomFailed = true
		logWarn("browser", "RenderGeometry is unavailable for page boxes, using the shape cache: %v", err)
		return false
	}
	return true
}

// webPaintFill fills a page box. A square box is one FillRect; a rounded one is
// one triangle fan, or the shape cache where geometry is not available. The
// radius is clamped to half the shorter side. Render thread.
func webPaintFill(r *sdl.Renderer, x, y, w, h, radius int32, c sdl.Color) {
	if r == nil || w <= 0 || h <= 0 {
		return
	}
	if half := min(w, h) / 2; radius > half {
		radius = half
	}
	if radius <= 1 {
		// A one-pixel radius cannot be seen; the shape cache baked a texture
		// for it all the same.
		_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		_ = r.SetDrawColor(c.R, c.G, c.B, c.A)
		_ = r.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
		return
	}
	if !webGeomFailed {
		rad := float64(radius)
		pts := webRoundedOutline(webGeom.pts[:0], float64(x), float64(y), float64(w), float64(h), rad, webCornerSegments(rad))
		webGeom.pts = pts
		v := webGeom.v[:0]
		v = append(v, sdl.Vertex{Position: sdl.FPoint{X: float32(x) + float32(w)/2, Y: float32(y) + float32(h)/2}, Color: c})
		for _, p := range pts {
			v = append(v, sdl.Vertex{Position: p, Color: c})
		}
		idx := webGeom.idx[:0]
		n := int32(len(pts))
		for i := int32(0); i < n; i++ {
			next := i + 1
			if next == n {
				next = 0
			}
			idx = append(idx, 0, i+1, next+1)
		}
		webGeom.v, webGeom.idx = v, idx
		if webGeomDraw(r) {
			return
		}
	}
	fillRoundedRect(r, x, y, w, h, radius, c)
}

// webPaintField paints a text field's box: its fill, then the border its page
// gave it (frame, from webRunChip), with the page's corner radius — 3px when
// it set none, as fields always had. Render thread.
func webPaintField(r *sdl.Renderer, x, y, w, h int32, bg sdl.Color, frame *webInlineChip) {
	if r == nil || w <= 0 || h <= 0 {
		return
	}
	radius := int32(3)
	if frame != nil && frame.radius > 0 {
		radius = frame.radius
	}
	webPaintFill(r, x, y, w, h, radius, bg)
	if frame == nil || !frame.hasBorder() {
		return
	}
	b := frame.border
	if b[0] == b[1] && b[1] == b[2] && b[2] == b[3] {
		webPaintStroke(r, x, y, w, h, radius, b[0], frame.borderColor)
		return
	}
	// Uneven sides (an underlined field: border-bottom only) are square bars:
	// left, right, top, bottom, as webInlineChip.border orders them.
	edges := [4]sdl.Rect{
		{X: x, Y: y, W: b[0], H: h},
		{X: x + w - b[1], Y: y, W: b[1], H: h},
		{X: x, Y: y, W: w, H: b[2]},
		{X: x, Y: y + h - b[3], W: w, H: b[3]},
	}
	var sides [4]sdl.Rect
	n := 0
	for i := range edges {
		if b[i] > 0 {
			sides[n] = edges[i]
			n++
		}
	}
	c := frame.borderColor
	_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = r.SetDrawColor(c.R, c.G, c.B, c.A)
	_ = r.FillRects(sides[:n])
}

// webPaintStroke draws a page box's border ring of the given thickness. A
// square ring is four FillRects; a rounded one is one strip between the outer
// and inner outlines, or the shape cache without geometry. Render thread.
func webPaintStroke(r *sdl.Renderer, x, y, w, h, radius, thickness int32, c sdl.Color) {
	if r == nil || w <= 0 || h <= 0 || thickness < 1 {
		return
	}
	if half := min(w, h) / 2; radius > half {
		radius = half
	}
	if thickness*2 >= w || thickness*2 >= h {
		webPaintFill(r, x, y, w, h, radius, c)
		return
	}
	if radius <= 1 {
		_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		_ = r.SetDrawColor(c.R, c.G, c.B, c.A)
		sides := [4]sdl.Rect{
			{X: x, Y: y, W: w, H: thickness},
			{X: x, Y: y + h - thickness, W: w, H: thickness},
			{X: x, Y: y + thickness, W: thickness, H: h - 2*thickness},
			{X: x + w - thickness, Y: y + thickness, W: thickness, H: h - 2*thickness},
		}
		_ = r.FillRects(sides[:])
		return
	}
	if !webGeomFailed {
		rad := float64(radius)
		t := float64(thickness)
		segs := webCornerSegments(rad)
		outer := webRoundedOutline(webGeom.pts[:0], float64(x), float64(y), float64(w), float64(h), rad, segs)
		inner := webRoundedOutline(webGeom.inner[:0], float64(x)+t, float64(y)+t, float64(w)-2*t, float64(h)-2*t, math.Max(rad-t, 0), segs)
		webGeom.pts, webGeom.inner = outer, inner
		v := webGeom.v[:0]
		for i := range outer {
			v = append(v, sdl.Vertex{Position: outer[i], Color: c}, sdl.Vertex{Position: inner[i], Color: c})
		}
		idx := webGeom.idx[:0]
		n := int32(len(outer))
		for i := int32(0); i < n; i++ {
			next := i + 1
			if next == n {
				next = 0
			}
			o0, i0, o1, i1 := 2*i, 2*i+1, 2*next, 2*next+1
			idx = append(idx, o0, o1, i0, i0, o1, i1)
		}
		webGeom.v, webGeom.idx = v, idx
		if webGeomDraw(r) {
			return
		}
	}
	strokeRoundedRect(r, x, y, w, h, radius, thickness, c)
}

// webPaintGradient draws a two-axis-aligned CSS ramp as one strip: a pair of
// vertices at every colour stop (and along rounded corners), so the GPU
// interpolates exactly the piecewise-linear ramp the stops describe. false
// when geometry is not available. Render thread.
func webPaintGradient(r *sdl.Renderer, rect sdl.Rect, radius int32, g webGradient) bool {
	if webGeomFailed || r == nil || rect.W <= 0 || rect.H <= 0 || len(g.Stops) == 0 {
		return false
	}
	if g.Radial || g.Angled || g.Conic || g.Repeating || len(g.Raw) > 0 {
		// At its real angle, round, repeating, or with stops placed in px
		// (webpage_gradient.go).
		return webPaintGradientShape(r, rect, radius, g)
	}
	length, cross := float64(rect.W), float64(rect.H)
	if g.Vertical {
		length, cross = cross, length
	}
	rad := float64(radius)
	if half := math.Min(length, cross) / 2; rad > half {
		rad = half
	}
	if rad < 0 {
		rad = 0
	}
	cuts := append(webGeom.cuts[:0], 0, length)
	for _, stop := range g.Stops {
		cuts = append(cuts, math.Max(0, math.Min(1, stop.Pos))*length)
	}
	if rad > 0 {
		segs := webCornerSegments(rad)
		for k := 0; k < segs; k++ {
			d := rad * (1 - math.Sin(float64(k)/float64(segs)*math.Pi/2))
			cuts = append(cuts, d, length-d)
		}
		cuts = append(cuts, rad, length-rad)
	}
	sort.Float64s(cuts)
	uniq := cuts[:1]
	for _, c := range cuts[1:] {
		if c-uniq[len(uniq)-1] > 0.01 {
			uniq = append(uniq, c)
		}
	}
	webGeom.cuts = cuts
	inset := func(c float64) float64 {
		var dy float64
		switch {
		case rad <= 0:
			return 0
		case c < rad:
			dy = rad - c
		case c > length-rad:
			dy = c - (length - rad)
		default:
			return 0
		}
		return rad - math.Sqrt(math.Max(0, rad*rad-dy*dy))
	}
	x, y := float64(rect.X), float64(rect.Y)
	v := webGeom.v[:0]
	// A hard edge (two stops at one place: `red 50%, blue 50%`, a stripe) is
	// two vertex pairs at the cut, the colour before it and the colour after.
	const eps = 1e-7
	pairs := int32(0)
	for _, c := range uniq {
		left, right := g.At(c/length-eps), g.At(c/length+eps)
		in := inset(c)
		var a, b sdl.FPoint
		if g.Vertical {
			a = sdl.FPoint{X: float32(x + in), Y: float32(y + c)}
			b = sdl.FPoint{X: float32(x + cross - in), Y: float32(y + c)}
		} else {
			a = sdl.FPoint{X: float32(x + c), Y: float32(y + in)}
			b = sdl.FPoint{X: float32(x + c), Y: float32(y + cross - in)}
		}
		v = append(v, sdl.Vertex{Position: a, Color: left}, sdl.Vertex{Position: b, Color: left})
		pairs++
		if right != left {
			v = append(v, sdl.Vertex{Position: a, Color: right}, sdl.Vertex{Position: b, Color: right})
			pairs++
		}
	}
	idx := webGeom.idx[:0]
	for i := int32(0); i+1 < pairs; i++ {
		a0, b0, a1, b1 := 2*i, 2*i+1, 2*i+2, 2*i+3
		idx = append(idx, a0, b0, a1, b0, b1, a1)
	}
	webGeom.v, webGeom.idx = v, idx
	return webGeomDraw(r)
}
