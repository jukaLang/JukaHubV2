package main

// webpage_gradient.go — gradients at their real angle, and radial ones.
//
// The reader drew a linear gradient on one axis only: `135deg` or `to bottom
// right` (the diagonal wash of most hero banners and buttons) ran straight
// down, and radial-gradient() — the soft glow behind a headline, a spotlight
// on a card — was not a ramp at all but its averaged colour, a flat tint.
//
// Both are now drawn as GPU geometry, exactly:
//
//   - A linear gradient at any angle. CSS draws it along a gradient line
//     through the box's centre, as long as the box's projection on it
//     (|w·sinθ| + |h·cosθ|); a corner keyword sets the angle so the line is
//     perpendicular to the diagonal between the two neighbouring corners,
//     which depends on the box's shape. The box's outline (rounded when it has
//     a radius — a rounded rectangle is convex) is cut into slabs between the
//     colour stops by lines across the gradient, each slab is a convex
//     polygon, and its vertices get the colour at their place on the line.
//     Within a slab the colour is linear in position, which is exactly what
//     the GPU interpolates, so the ramp is exact at any angle.
//   - A radial gradient: rings of vertices on ellipses at the stop positions
//     around its centre (`at 30% 20%`), sized by its ending shape (circle or
//     ellipse; closest-side, farthest-side, closest-corner, farthest-corner —
//     the default — or explicit radii), a flat band past the last stop out to
//     the box's far corner, every triangle clipped to the box's outline.
//
// Repeating gradients are drawn as one pass, as before; a renderer that cannot
// draw geometry falls back to the axis bands (linear) or the averaged colour
// (radial).

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The sizes a radial gradient's ending shape can take (webGradient.Size).
const (
	webRadialFarthestCorner uint8 = iota
	webRadialClosestSide
	webRadialClosestCorner
	webRadialFarthestSide
	webRadialExplicit
)

// webGPt is a geometry point with its colour as floats, so a clipped edge
// can interpolate both.
type webGPt struct {
	x, y       float64
	r, g, b, a float64
}

func webGPtColor(c sdl.Color, x, y float64) webGPt {
	return webGPt{x: x, y: y, r: float64(c.R), g: float64(c.G), b: float64(c.B), a: float64(c.A)}
}

func (p webGPt) color() sdl.Color {
	clamp := func(v float64) uint8 { return uint8(math.Max(0, math.Min(255, math.Round(v)))) }
	return sdl.Color{R: clamp(p.r), G: clamp(p.g), B: clamp(p.b), A: clamp(p.a)}
}

// webClipHalf keeps the part of the convex polygon in where a·x + b·y + c >= 0
// (one step of Sutherland–Hodgman), interpolating colours on cut edges. out
// is reused.
func webClipHalf(in, out []webGPt, a, b, c float64) []webGPt {
	out = out[:0]
	n := len(in)
	for i := 0; i < n; i++ {
		p, q := in[i], in[(i+1)%n]
		sp, sq := a*p.x+b*p.y+c, a*q.x+b*q.y+c
		if sp >= 0 {
			out = append(out, p)
		}
		if (sp >= 0) != (sq >= 0) {
			f := sp / (sp - sq)
			out = append(out, webGPt{
				x: p.x + (q.x-p.x)*f, y: p.y + (q.y-p.y)*f,
				r: p.r + (q.r-p.r)*f, g: p.g + (q.g-p.g)*f, b: p.b + (q.b-p.b)*f, a: p.a + (q.a-p.a)*f,
			})
		}
	}
	return out
}

// webGradOutline is the outline of a box with corner radius rad, as points in
// order: the rounded rectangle is convex, so slabs and triangles clipped to it
// stay convex.
func webGradOutline(x, y, w, h, rad float64) []webGPt {
	if rad <= 0.5 {
		return []webGPt{{x: x, y: y}, {x: x + w, y: y}, {x: x + w, y: y + h}, {x: x, y: y + h}}
	}
	pts := webRoundedOutline(nil, x, y, w, h, rad, webCornerSegments(rad))
	out := make([]webGPt, len(pts))
	for i, p := range pts {
		out[i] = webGPt{x: float64(p.X), y: float64(p.Y)}
	}
	return out
}

// webGradEdges turns a convex outline into the half-planes (a, b, c) whose
// intersection it is: a·x + b·y + c >= 0 inside.
func webGradEdges(outline []webGPt) [][3]float64 {
	area := 0.0
	n := len(outline)
	for i := 0; i < n; i++ {
		p, q := outline[i], outline[(i+1)%n]
		area += p.x*q.y - q.x*p.y
	}
	sign := 1.0
	if area < 0 {
		sign = -1
	}
	edges := make([][3]float64, 0, n)
	for i := 0; i < n; i++ {
		p, q := outline[i], outline[(i+1)%n]
		ex, ey := q.x-p.x, q.y-p.y
		if ex == 0 && ey == 0 {
			continue
		}
		edges = append(edges, [3]float64{-ey * sign, ex * sign, (ey*p.x - ex*p.y) * sign})
	}
	return edges
}

// webGradEmit appends a convex polygon as a triangle fan.
func webGradEmit(poly []webGPt) {
	if len(poly) < 3 {
		return
	}
	base := int32(len(webGeom.v))
	for _, p := range poly {
		webGeom.v = append(webGeom.v, sdl.Vertex{Position: sdl.FPoint{X: float32(p.x), Y: float32(p.y)}, Color: p.color()})
	}
	for i := int32(1); i+1 < int32(len(poly)); i++ {
		webGeom.idx = append(webGeom.idx, base, base+i, base+i+1)
	}
}

// webPaintGradientShape draws a gradient into rect, rounded by radius, as one
// geometry call: a linear one at any angle (and any axis one whose stops are
// placed in px or repeat), a radial or a conic one. false when it cannot: no
// geometry, an empty box or a degenerate shape. Render thread.
func webPaintGradientShape(r *sdl.Renderer, rect sdl.Rect, radius int32, g webGradient) bool {
	if webGeomFailed || r == nil || rect.W <= 0 || rect.H <= 0 || len(g.Stops) == 0 {
		return false
	}
	x, y, w, h := float64(rect.X), float64(rect.Y), float64(rect.W), float64(rect.H)
	rad := math.Max(0, math.Min(float64(radius), math.Min(w, h)/2))
	outline := webGradOutline(x, y, w, h, rad)
	webGeom.v, webGeom.idx = webGeom.v[:0], webGeom.idx[:0]
	var ok bool
	switch {
	case g.Conic:
		ok = webGradConic(outline, x, y, w, h, rad, g)
	case g.Radial:
		ok = webGradRadial(outline, x, y, w, h, rad, g)
	default:
		ok = webGradLinear(outline, x, y, w, h, g)
	}
	if !ok {
		return false
	}
	return webGeomDraw(r)
}

// webGradientResolve is g with its stops placed for a gradient line (or ray)
// pxLen CSS px long: stops written in px are resolved again for this box.
func webGradientResolve(g webGradient, pxLen float64) webGradient {
	if len(g.Raw) == 0 {
		return g
	}
	g.Stops = webGradientNormalize(g.Raw, pxLen, !g.Repeating)
	if g.Reversed {
		g.Stops = webGradientMirror(g.Stops)
	}
	return g
}

// webGradPeriod is a repeating gradient's period on the ramp, and its first
// stop; 0 when it does not repeat (or its stops do not span anything).
func webGradPeriod(g webGradient) (first, period float64) {
	if !g.Repeating || len(g.Stops) < 2 {
		return 0, 0
	}
	first = g.Stops[0].Pos
	period = g.Stops[len(g.Stops)-1].Pos - first
	if period < 1e-4 {
		return 0, 0
	}
	return first, period
}

// webGradColorIn is the ramp's colour at t inside its span [a, b]: a point
// on a hard edge (two stops at one place) takes the colour of the side the
// span is on, and a repeating ramp is read within its period.
func webGradColorIn(g webGradient, t, a, b float64) sdl.Color {
	const eps = 1e-7
	if !math.IsInf(a, -1) && t < a+eps {
		t = a + eps
	}
	if !math.IsInf(b, 1) && t > b-eps {
		t = b - eps
	}
	if first, period := webGradPeriod(g); period > 0 {
		t = first + math.Mod(math.Mod(t-first, period)+period, period)
	}
	return g.At(t)
}

// webGradCuts are the places on the ramp, strictly between lo and hi, where
// its colour stops lie — every period's, for a repeating ramp — in order;
// nil when a repeating ramp would need more than webGradMaxCuts.
func webGradCuts(g webGradient, lo, hi float64) []float64 {
	const webGradMaxCuts = 600
	cuts := make([]float64, 0, len(g.Stops)+2)
	add := func(c float64) {
		if c > lo && c < hi {
			cuts = append(cuts, c)
		}
	}
	first, period := webGradPeriod(g)
	if period == 0 {
		for _, s := range g.Stops {
			add(s.Pos)
		}
	} else {
		k0 := math.Floor((lo - first) / period)
		k1 := math.Ceil((hi - first) / period)
		if (k1-k0+1)*float64(len(g.Stops)) > webGradMaxCuts {
			return nil
		}
		for k := k0; k <= k1; k++ {
			for _, s := range g.Stops {
				add(s.Pos + k*period)
			}
		}
	}
	sort.Float64s(cuts)
	out := cuts[:0]
	for _, c := range cuts {
		if len(out) == 0 || c > out[len(out)-1]+1e-6 {
			out = append(out, c)
		}
	}
	return out
}

// webGradDirection is the unit vector a linear gradient runs along on screen
// (y down) for a box w×h.
func webGradDirection(g webGradient, w, h float64) (float64, float64) {
	if !g.Angled {
		// An axis ramp, its stops already mirrored for up or left.
		if g.Vertical {
			return 0, 1
		}
		return 1, 0
	}
	if g.CornerX != 0 && g.CornerY != 0 {
		// Perpendicular to the diagonal through the two neighbouring corners,
		// into the named corner's quadrant.
		dx, dy := float64(g.CornerX)*h, float64(g.CornerY)*w
		n := math.Hypot(dx, dy)
		if n == 0 {
			return 0, 1
		}
		return dx / n, dy / n
	}
	rad := g.Angle * math.Pi / 180
	return math.Sin(rad), -math.Cos(rad)
}

// webGradLinear cuts the outline into slabs between the stops along the
// gradient line and emits each with the colour of its vertices.
func webGradLinear(outline []webGPt, x, y, w, h float64, g webGradient) bool {
	ux, uy := webGradDirection(g, w, h)
	length := math.Abs(w*ux) + math.Abs(h*uy)
	if length <= 0 {
		return false
	}
	g = webGradientResolve(g, length/webViewportPixelScale())
	cx, cy := x+w/2, y+h/2
	at := func(p webGPt) float64 { return ((p.x-cx)*ux+(p.y-cy)*uy)/length + 0.5 }
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range outline {
		t := at(p)
		lo, hi = math.Min(lo, t), math.Max(hi, t)
	}
	inner := webGradCuts(g, lo, hi)
	if inner == nil {
		return false
	}
	cuts := make([]float64, 0, len(inner)+2)
	cuts = append(cuts, math.Inf(-1))
	cuts = append(cuts, inner...)
	cuts = append(cuts, math.Inf(1))
	var bufA, bufB []webGPt
	for i := 0; i+1 < len(cuts); i++ {
		poly := append(bufA[:0], outline...)
		if !math.IsInf(cuts[i], -1) {
			// t >= cuts[i]
			bufB = webClipHalf(poly, bufB, ux, uy, -(cx*ux+cy*uy)-(cuts[i]-0.5)*length)
			poly, bufB = bufB, poly
		}
		if !math.IsInf(cuts[i+1], 1) {
			// t <= cuts[i+1]
			bufB = webClipHalf(poly, bufB, -ux, -uy, (cx*ux+cy*uy)+(cuts[i+1]-0.5)*length)
			poly, bufB = bufB, poly
		}
		// The outer slabs end where the box does: a repeating ramp's corner
		// on a period's end takes the colour inside the box, not the next
		// period's first.
		a, b := cuts[i], cuts[i+1]
		if math.IsInf(a, -1) {
			a = lo
		}
		if math.IsInf(b, 1) {
			b = hi
		}
		for k := range poly {
			c := webGradColorIn(g, at(poly[k]), a, b)
			poly[k] = webGPtColor(c, poly[k].x, poly[k].y)
		}
		webGradEmit(poly)
		bufA = poly
	}
	return len(webGeom.idx) > 0
}

// webGradRadialSize is a radial gradient's ending shape for a box w×h with
// its centre at (cx, cy) inside it: the ellipse's radii. scale turns explicit
// CSS px radii into layout pixels.
func webGradRadialSize(g webGradient, w, h, cx, cy, scale float64) (float64, float64) {
	sideX0, sideX1 := math.Abs(cx), math.Abs(w-cx)
	sideY0, sideY1 := math.Abs(cy), math.Abs(h-cy)
	closeX, closeY := math.Min(sideX0, sideX1), math.Min(sideY0, sideY1)
	farX, farY := math.Max(sideX0, sideX1), math.Max(sideY0, sideY1)
	switch g.Size {
	case webRadialExplicit:
		rx, ry := g.RX*scale, g.RY*scale
		if g.RPct {
			rx, ry = g.RX*w, g.RY*h
		}
		if g.Circle {
			ry = rx
		}
		return rx, ry
	case webRadialClosestSide:
		if g.Circle {
			r := math.Min(closeX, closeY)
			return r, r
		}
		return closeX, closeY
	case webRadialFarthestSide:
		if g.Circle {
			r := math.Max(farX, farY)
			return r, r
		}
		return farX, farY
	case webRadialClosestCorner:
		if g.Circle {
			r := math.Hypot(closeX, closeY)
			return r, r
		}
		return closeX * math.Sqrt2, closeY * math.Sqrt2
	}
	if g.Circle {
		r := math.Hypot(farX, farY)
		return r, r
	}
	return farX * math.Sqrt2, farY * math.Sqrt2
}

// webGradCentre is a radial or conic gradient's centre on screen.
func webGradCentre(g webGradient, x, y, w, h, scale float64) (float64, float64) {
	cx, cy := g.CX*w, g.CY*h
	if g.CXPx {
		cx = g.CX * scale
	}
	if g.CYPx {
		cy = g.CY * scale
	}
	return x + cx, y + cy
}

// webGradClipper returns how a triangle of a radial or conic gradient is
// emitted: as it is when it lies inside the box's straight middle, dropped
// when it lies wholly outside the box, else cut by the outline's edges.
func webGradClipper(outline []webGPt, x, y, w, h, rad float64) func(a, b, c webGPt) {
	edges := webGradEdges(outline)
	inner := func(p webGPt) bool {
		inX := p.x >= x+rad && p.x <= x+w-rad && p.y >= y && p.y <= y+h
		inY := p.y >= y+rad && p.y <= y+h-rad && p.x >= x && p.x <= x+w
		return inX || inY
	}
	var tri, buf []webGPt
	return func(a, b, c webGPt) {
		if math.Max(a.x, math.Max(b.x, c.x)) < x || math.Min(a.x, math.Min(b.x, c.x)) > x+w ||
			math.Max(a.y, math.Max(b.y, c.y)) < y || math.Min(a.y, math.Min(b.y, c.y)) > y+h {
			return
		}
		tri = append(tri[:0], a, b, c)
		if !(inner(a) && inner(b) && inner(c)) {
			for _, e := range edges {
				buf = webClipHalf(tri, buf, e[0], e[1], e[2])
				tri, buf = buf, tri
				if len(tri) < 3 {
					return
				}
			}
		}
		webGradEmit(tri)
	}
}

// webGradRadial emits rings of the gradient around its centre, from the
// centre to past the farthest point of the box, every triangle clipped to the
// outline. Each band between two rings takes the colours of its own side of
// a hard edge.
func webGradRadial(outline []webGPt, x, y, w, h, rad float64, g webGradient) bool {
	// CSS px in the gradient (a centre or radii in px) are scaled like the
	// page's own lengths; the box is already in layout pixels.
	scale := webViewportPixelScale()
	cx, cy := webGradCentre(g, x, y, w, h, scale)
	rx, ry := webGradRadialSize(g, w, h, cx-x, cy-y, scale)
	if rx <= 0.5 || ry <= 0.5 {
		return false
	}
	g = webGradientResolve(g, rx/scale)
	dist := func(px, py float64) float64 { return math.Hypot((px-cx)/rx, (py-cy)/ry) }
	far := 0.0
	for _, p := range outline {
		far = math.Max(far, dist(p.x, p.y))
	}
	far = far*1.02 + 0.01
	inner := webGradCuts(g, 0, far)
	if inner == nil {
		return false
	}
	rings := make([]float64, 0, len(inner)+2)
	rings = append(rings, 0)
	rings = append(rings, inner...)
	rings = append(rings, far)
	// Enough segments that a ring's chords stay within a pixel of the curve.
	segs := int(math.Max(rx, ry)*far*2*math.Pi/14) + 16
	segs = max(24, min(segs, 96))
	cosT, sinT := make([]float64, segs), make([]float64, segs)
	for i := 0; i < segs; i++ {
		t := 2 * math.Pi * float64(i) / float64(segs)
		cosT[i], sinT[i] = math.Cos(t), math.Sin(t)
	}
	point := func(d float64, i int, c sdl.Color) webGPt {
		return webGPtColor(c, cx+rx*d*cosT[i%segs], cy+ry*d*sinT[i%segs])
	}
	emit := webGradClipper(outline, x, y, w, h, rad)
	for k := 1; k < len(rings); k++ {
		d0, d1 := rings[k-1], rings[k]
		cIn := webGradColorIn(g, d0, d0, d1)
		cOut := webGradColorIn(g, d1, d0, d1)
		for i := 0; i < segs; i++ {
			outer0, outer1 := point(d1, i, cOut), point(d1, i+1, cOut)
			if d0 == 0 {
				emit(webGPtColor(cIn, cx, cy), outer0, outer1)
				continue
			}
			inner0, inner1 := point(d0, i, cIn), point(d0, i+1, cIn)
			emit(inner0, outer0, outer1)
			emit(inner0, outer1, inner1)
		}
	}
	return len(webGeom.idx) > 0
}

// webGradConic emits the gradient as thin slices round its centre, out past
// the farthest point of the box, at most four degrees each and cut at every
// stop, every triangle clipped to the outline.
func webGradConic(outline []webGPt, x, y, w, h, rad float64, g webGradient) bool {
	scale := webViewportPixelScale()
	cx, cy := webGradCentre(g, x, y, w, h, scale)
	far := 0.0
	for _, p := range outline {
		far = math.Max(far, math.Hypot(p.x-cx, p.y-cy))
	}
	far = far*1.02 + 1
	inner := webGradCuts(webGradient{Stops: g.Stops}, 0, 1)
	cuts := make([]float64, 0, len(inner)+2)
	cuts = append(cuts, 0)
	cuts = append(cuts, inner...)
	cuts = append(cuts, 1)
	emit := webGradClipper(outline, x, y, w, h, rad)
	edge := func(s float64, c sdl.Color) webGPt {
		a := (g.From + s*360) * math.Pi / 180
		return webGPtColor(c, cx+far*math.Sin(a), cy-far*math.Cos(a))
	}
	for k := 1; k < len(cuts); k++ {
		a, b := cuts[k-1], cuts[k]
		n := max(1, int(math.Ceil((b-a)*90)))
		for j := 0; j < n; j++ {
			s0 := a + (b-a)*float64(j)/float64(n)
			s1 := a + (b-a)*float64(j+1)/float64(n)
			c0, c1 := webGradColorIn(g, s0, a, b), webGradColorIn(g, s1, a, b)
			p0, p1 := edge(s0, c0), edge(s1, c1)
			mid := webGPt{x: cx, y: cy, r: (p0.r + p1.r) / 2, g: (p0.g + p1.g) / 2, b: (p0.b + p1.b) / 2, a: (p0.a + p1.a) / 2}
			emit(mid, p0, p1)
		}
	}
	return len(webGeom.idx) > 0
}

// webGradientExact reads the exact direction of a linear gradient's first
// argument: an angle in CSS degrees, or a corner (cx, cy ±1) for `to bottom
// right`. axis reports a direction one of the four axes, which the axis strip
// already draws exactly.
func webGradientExact(arg string) (angle float64, cx, cy int8, axis bool) {
	d := strings.ToLower(strings.TrimSpace(arg))
	if a, ok := webGradientAngle(d); ok {
		rest := math.Mod(a, 90)
		return a, 0, 0, rest < 0.01 || rest > 89.99
	}
	switch {
	case strings.Contains(d, "left"):
		cx = -1
	case strings.Contains(d, "right"):
		cx = 1
	}
	switch {
	case strings.Contains(d, "top"):
		cy = -1
	case strings.Contains(d, "bottom"):
		cy = 1
	}
	return 0, cx, cy, cx == 0 || cy == 0
}

// webParseRadialGradient resolves radial-gradient() (repeating ones are drawn
// as one pass): an optional ending shape and size, an optional `at` position,
// then the colour stops at fractions of the gradient ray. It reports false for
// anything else.
func webParseRadialGradient(value string) (webGradient, bool) {
	lower := asciiLower(value)
	idx := strings.Index(lower, "radial-gradient(")
	if idx < 0 {
		return webGradient{}, false
	}
	open := idx + len("radial-gradient")
	end := webCSSParenEnd(value, open)
	if end <= open {
		return webGradient{}, false
	}
	parts := webSplitTopLevel(value[open+1:end], ',')
	if len(parts) < 2 {
		return webGradient{}, false
	}
	g := webGradient{Radial: true, Vertical: true, Size: webRadialFarthestCorner, CX: 0.5, CY: 0.5,
		Repeating: webGradientRepeating(lower, idx)}
	start := 0
	if _, isColor := webColorInValueAny(parts[0]); !isColor {
		if !webParseRadialShape(&g, parts[0]) {
			return webGradient{}, false
		}
		start = 1
	}
	if !webGradientParseStops(&g, parts[start:], false) {
		return webGradient{}, false
	}
	return g, true
}

// webParseConicGradient resolves conic-gradient(): an optional `from <angle>`
// and `at <position>`, then colour stops placed by angle or percentage of the
// turn (`red 0 25%` is a pie slice). A repeating one is drawn once round.
func webParseConicGradient(value string) (webGradient, bool) {
	lower := asciiLower(value)
	idx := strings.Index(lower, "conic-gradient(")
	if idx < 0 {
		return webGradient{}, false
	}
	open := idx + len("conic-gradient")
	end := webCSSParenEnd(value, open)
	if end <= open {
		return webGradient{}, false
	}
	parts := webSplitTopLevel(value[open+1:end], ',')
	if len(parts) < 2 {
		return webGradient{}, false
	}
	g := webGradient{Conic: true, Vertical: true, CX: 0.5, CY: 0.5}
	start := 0
	if _, isColor := webColorInValueAny(parts[0]); !isColor {
		fields := strings.Fields(strings.ToLower(strings.TrimSpace(parts[0])))
		for i := 0; i < len(fields); i++ {
			switch fields[i] {
			case "from":
				if i+1 < len(fields) {
					if deg, ok := webGradientAngleRaw(fields[i+1]); ok {
						g.From = deg
					}
					i++
				}
			case "at":
				webParseGradientCenter(&g, fields[i+1:])
				i = len(fields)
			}
		}
		start = 1
	}
	if !webGradientParseStops(&g, parts[start:], true) {
		return webGradient{}, false
	}
	return g, true
}

// webParseRadialShape reads `[circle|ellipse] [size] [at <position>]` into g.
func webParseRadialShape(g *webGradient, arg string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(arg)))
	at := len(fields)
	for i, f := range fields {
		if f == "at" {
			at = i
			break
		}
	}
	var lengths []string
	for _, f := range fields[:at] {
		switch f {
		case "circle":
			g.Circle = true
		case "ellipse":
		case "closest-side", "contain":
			g.Size = webRadialClosestSide
		case "closest-corner":
			g.Size = webRadialClosestCorner
		case "farthest-side":
			g.Size = webRadialFarthestSide
		case "farthest-corner", "cover":
			g.Size = webRadialFarthestCorner
		default:
			lengths = append(lengths, f)
		}
	}
	switch len(lengths) {
	case 0:
	case 1:
		// One length is a circle's radius; a percentage is not allowed there.
		px := webLength(lengths[0], 16)
		if px <= 0 {
			return false
		}
		g.Circle, g.Size, g.RX, g.RY = true, webRadialExplicit, px, px
	case 2:
		if pa, okA := webPercentValue(lengths[0]); okA {
			pb, okB := webPercentValue(lengths[1])
			if !okB || pa <= 0 || pb <= 0 {
				return false
			}
			g.Size, g.RX, g.RY, g.RPct = webRadialExplicit, pa/100, pb/100, true
			break
		}
		a, b := webLength(lengths[0], 16), webLength(lengths[1], 16)
		if a <= 0 || b <= 0 {
			return false
		}
		g.Size, g.RX, g.RY = webRadialExplicit, a, b
	default:
		return false
	}
	if at < len(fields) {
		webParseGradientCenter(g, fields[at+1:])
	}
	return true
}

// webParseGradientCenter reads a radial gradient's `at` position: keywords,
// percentages or px, one or two of them, x first unless a keyword says
// otherwise.
func webParseGradientCenter(g *webGradient, fields []string) {
	setX := func(v float64, px bool) { g.CX, g.CXPx = v, px }
	setY := func(v float64, px bool) { g.CY, g.CYPx = v, px }
	xDone, yDone := false, false
	for i, f := range fields {
		if i > 1 {
			break
		}
		switch f {
		case "left":
			setX(0, false)
			xDone = true
		case "right":
			setX(1, false)
			xDone = true
		case "top":
			setY(0, false)
			yDone = true
		case "bottom":
			setY(1, false)
			yDone = true
		case "center":
		default:
			var v float64
			px := false
			if pct, ok := webPercentValue(f); ok {
				v = pct / 100
			} else if n, err := strconv.ParseFloat(f, 64); err == nil && n == 0 {
				v = 0
			} else if l := webLength(f, 16); l != 0 {
				v, px = l, true
			} else {
				continue
			}
			if !xDone && (i == 0 || yDone) {
				setX(v, px)
				xDone = true
			} else if !yDone {
				setY(v, px)
				yDone = true
			}
		}
	}
}
