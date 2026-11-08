package main

// vecraster.go — the anti-aliased vector rasterizer behind the icon set.
//
// The shell's glyphs used to be assembled from SDL primitives: aliased lines,
// a globe whose meridians were plotted point by point, a gear made of eight
// discs. Next to the anti-aliased Inter labels they looked jagged. This file is
// a small pure-Go rasterizer instead: paths are flattened into polylines and
// painted into a float coverage mask, which iconatlas.go converts to 8-bit
// alpha and uploads into a texture once per (glyph, size).
//
//   - Fills scan every pixel row at vecSubScan sub-scanlines and add the exact
//     horizontal span each sub-scanline covers, so a near-vertical edge gets
//     true area coverage and a near-horizontal one vecSubScan levels. Both the
//     non-zero and the even-odd rule are supported.
//   - Strokes are the union of one capsule per segment, evaluated as a signed
//     distance at each pixel centre. That union is exactly a round-join stroke,
//     with none of the winding artefacts an outline-offset stroke shows at tight
//     corners, and each open end can be round, butt or square.
//   - Every paint composites into the mask with "over" (at an opacity, which is
//     how the duotone fills are drawn) or "erase" (knock-outs).
//
// It imports no SDL, so the geometry is unit-tested without a renderer
// (vecraster_test.go). The canvas and path keep their buffers between uses:
// rasterizing allocates only when a buffer has to grow.

import "math"

// vecPt is a point in pixel space: x to the right, y down.
type vecPt struct{ X, Y float64 }

// vecCap is how a stroke ends at the open ends of a subpath. Interior vertices
// always join round.
type vecCap uint8

const (
	vecCapRound vecCap = iota
	vecCapButt
	vecCapSquare
)

const (
	// vecSubScan is how many sub-scanlines a fill samples per pixel row.
	vecSubScan = 16
	// vecFlatStep is the longest chord, in pixels, a curve or an arc is
	// flattened into. At icon radii a 0.75px chord deviates from the true
	// curve by well under a tenth of a pixel.
	vecFlatStep = 0.75
	// vecMaxCurveSegs bounds the flattening of a single curve.
	vecMaxCurveSegs = 512
)

// vecPath is a path flattened into polylines. Build it with MoveTo / LineTo /
// QuadTo / CubicTo / Arc / Close or the closed-shape helpers, paint it with a
// vecCanvas, then Reset it to reuse its buffers.
type vecPath struct {
	pts    []vecPt
	starts []int  // index in pts where each subpath begins
	closed []bool // per subpath
	cur    vecPt
	start  vecPt
	open   bool // a subpath is in progress
}

// Reset empties the path and keeps its buffers.
func (p *vecPath) Reset() {
	p.pts = p.pts[:0]
	p.starts = p.starts[:0]
	p.closed = p.closed[:0]
	p.cur = vecPt{}
	p.start = vecPt{}
	p.open = false
}

// MoveTo starts a new subpath at (x, y).
func (p *vecPath) MoveTo(x, y float64) {
	p.starts = append(p.starts, len(p.pts))
	p.closed = append(p.closed, false)
	p.pts = append(p.pts, vecPt{X: x, Y: y})
	p.cur = vecPt{X: x, Y: y}
	p.start = p.cur
	p.open = true
}

// LineTo adds a straight segment from the current point. After a Close (or
// on an empty path) it first starts a subpath at the current point, like SVG.
// A point equal to the current one is dropped: a zero-length segment would
// only add a redundant cap.
func (p *vecPath) LineTo(x, y float64) {
	if !p.open {
		p.MoveTo(p.cur.X, p.cur.Y)
	}
	if math.Abs(x-p.cur.X) < 1e-9 && math.Abs(y-p.cur.Y) < 1e-9 {
		return
	}
	p.pts = append(p.pts, vecPt{X: x, Y: y})
	p.cur = vecPt{X: x, Y: y}
}

// vecSegments is how many chords a curve of the given (control-polygon)
// length is flattened into.
func vecSegments(length float64) int {
	n := int(math.Ceil(length / vecFlatStep))
	if n < 1 {
		n = 1
	}
	if n > vecMaxCurveSegs {
		n = vecMaxCurveSegs
	}
	return n
}

// QuadTo adds a quadratic Bézier from the current point through control
// point (cx, cy) to (x, y).
func (p *vecPath) QuadTo(cx, cy, x, y float64) {
	if !p.open {
		p.MoveTo(p.cur.X, p.cur.Y)
	}
	x0, y0 := p.cur.X, p.cur.Y
	n := vecSegments(math.Hypot(cx-x0, cy-y0) + math.Hypot(x-cx, y-cy))
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		mt := 1 - t
		p.LineTo(mt*mt*x0+2*mt*t*cx+t*t*x, mt*mt*y0+2*mt*t*cy+t*t*y)
	}
}

// CubicTo adds a cubic Bézier from the current point with control points
// (c1x, c1y) and (c2x, c2y) to (x, y).
func (p *vecPath) CubicTo(c1x, c1y, c2x, c2y, x, y float64) {
	if !p.open {
		p.MoveTo(p.cur.X, p.cur.Y)
	}
	x0, y0 := p.cur.X, p.cur.Y
	n := vecSegments(math.Hypot(c1x-x0, c1y-y0) + math.Hypot(c2x-c1x, c2y-c1y) + math.Hypot(x-c2x, y-c2y))
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		mt := 1 - t
		a, b, c, d := mt*mt*mt, 3*mt*mt*t, 3*mt*t*t, t*t*t
		p.LineTo(a*x0+b*c1x+c*c2x+d*x, a*y0+b*c1y+c*c2y+d*y)
	}
}

// Arc adds a circular arc around (cx, cy) from angle a0 to a1 in degrees,
// measured clockwise from +x (y points down). An open subpath is joined to
// the arc's start with a line; otherwise the arc starts a new subpath.
func (p *vecPath) Arc(cx, cy, r, a0, a1 float64) {
	p.EllipseArc(cx, cy, r, r, a0, a1)
}

// EllipseArc is Arc with separate horizontal and vertical radii.
func (p *vecPath) EllipseArc(cx, cy, rx, ry, a0, a1 float64) {
	r0, r1 := a0*math.Pi/180, a1*math.Pi/180
	sx, sy := cx+rx*math.Cos(r0), cy+ry*math.Sin(r0)
	if p.open {
		p.LineTo(sx, sy)
	} else {
		p.MoveTo(sx, sy)
	}
	rmax := rx
	if ry > rmax {
		rmax = ry
	}
	n := vecSegments(math.Abs(r1-r0) * rmax)
	if n < 2 {
		n = 2
	}
	for i := 1; i <= n; i++ {
		a := r0 + (r1-r0)*float64(i)/float64(n)
		p.LineTo(cx+rx*math.Cos(a), cy+ry*math.Sin(a))
	}
}

// End finishes the current subpath without closing it, so the next segment
// command starts a new one instead of joining it.
func (p *vecPath) End() {
	p.open = false
}

// Close closes the current subpath; the current point returns to its start.
func (p *vecPath) Close() {
	if !p.open {
		return
	}
	p.closed[len(p.closed)-1] = true
	p.open = false
	p.cur = p.start
}

// Circle adds a closed circle as its own subpath.
func (p *vecPath) Circle(cx, cy, r float64) {
	p.Ellipse(cx, cy, r, r)
}

// Ellipse adds a closed axis-aligned ellipse as its own subpath.
func (p *vecPath) Ellipse(cx, cy, rx, ry float64) {
	if rx <= 0 || ry <= 0 {
		return
	}
	rmax := rx
	if ry > rmax {
		rmax = ry
	}
	n := vecSegments(2 * math.Pi * rmax)
	if n < 12 {
		n = 12
	}
	p.open = false
	p.MoveTo(cx+rx, cy)
	for i := 1; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		p.LineTo(cx+rx*math.Cos(a), cy+ry*math.Sin(a))
	}
	p.Close()
}

// RoundRect adds a closed rectangle with corner radius r (clamped to half the
// shorter side) as its own subpath, clockwise from the top edge.
func (p *vecPath) RoundRect(x, y, w, h, r float64) {
	if w <= 0 || h <= 0 {
		return
	}
	if r > w/2 {
		r = w / 2
	}
	if r > h/2 {
		r = h / 2
	}
	p.open = false
	if r <= 0 {
		p.MoveTo(x, y)
		p.LineTo(x+w, y)
		p.LineTo(x+w, y+h)
		p.LineTo(x, y+h)
		p.Close()
		return
	}
	p.MoveTo(x+r, y)
	p.Arc(x+w-r, y+r, r, -90, 0)
	p.Arc(x+w-r, y+h-r, r, 0, 90)
	p.Arc(x+r, y+h-r, r, 90, 180)
	p.Arc(x+r, y+r, r, 180, 270)
	p.Close()
}

// Polygon adds a closed regular n-gon of circumradius r, its first vertex
// straight up from the centre and then rotated by rot degrees.
func (p *vecPath) Polygon(cx, cy, r float64, n int, rot float64) {
	if n < 3 || r <= 0 {
		return
	}
	p.open = false
	for i := 0; i < n; i++ {
		a := (rot - 90 + 360*float64(i)/float64(n)) * math.Pi / 180
		x, y := cx+r*math.Cos(a), cy+r*math.Sin(a)
		if i == 0 {
			p.MoveTo(x, y)
		} else {
			p.LineTo(x, y)
		}
	}
	p.Close()
}

// Star adds a closed n-pointed star: points at radius ro, valleys at ri, the
// first point straight up and then rotated by rot degrees.
func (p *vecPath) Star(cx, cy, ro, ri float64, n int, rot float64) {
	if n < 2 || ro <= 0 || ri < 0 {
		return
	}
	p.open = false
	for i := 0; i < 2*n; i++ {
		r := ro
		if i%2 == 1 {
			r = ri
		}
		a := (rot - 90 + 180*float64(i)/float64(n)) * math.Pi / 180
		x, y := cx+r*math.Cos(a), cy+r*math.Sin(a)
		if i == 0 {
			p.MoveTo(x, y)
		} else {
			p.LineTo(x, y)
		}
	}
	p.Close()
}

// subpath returns the [start, end) range of subpath i in p.pts.
func (p *vecPath) subpath(i int) (int, int) {
	end := len(p.pts)
	if i+1 < len(p.starts) {
		end = p.starts[i+1]
	}
	return p.starts[i], end
}

// bounds is the box of every flattened point, or ok=false for an empty path.
func (p *vecPath) bounds() (minX, minY, maxX, maxY float64, ok bool) {
	if len(p.pts) == 0 {
		return 0, 0, 0, 0, false
	}
	minX, minY = p.pts[0].X, p.pts[0].Y
	maxX, maxY = minX, minY
	for _, q := range p.pts[1:] {
		if q.X < minX {
			minX = q.X
		}
		if q.X > maxX {
			maxX = q.X
		}
		if q.Y < minY {
			minY = q.Y
		}
		if q.Y > maxY {
			maxY = q.Y
		}
	}
	return minX, minY, maxX, maxY, true
}

// vecPaint describes one paint operation.
type vecPaint struct {
	Stroke  bool    // stroke the path; otherwise fill it
	Width   float64 // stroke width in pixels
	Cap     vecCap  // stroke ends at open subpath ends
	EvenOdd bool    // fill rule; the default is non-zero
	Opacity float64 // ink strength, 0..1
	Erase   bool    // remove coverage from the mask instead of adding ink
}

// vecBox is a half-open pixel box [x0, x1) × [y0, y1).
type vecBox struct{ x0, y0, x1, y1 int }

func (b vecBox) empty() bool { return b.x1 <= b.x0 || b.y1 <= b.y0 }

// vecEdge is a non-horizontal fill edge stored top-down.
type vecEdge struct {
	x0, y0 float64 // the upper end
	y1     float64 // the lower end's y
	dxdy   float64 // x change per unit of y
	dir    int32   // +1 when the path runs down this edge, -1 when up
}

// vecCross is one edge crossing on a sub-scanline.
type vecCross struct {
	x   float64
	dir int32
}

// vecCanvas is a w×h coverage mask plus the scratch buffers paints need.
type vecCanvas struct {
	w, h  int
	alpha []float32 // composited ink, row-major, 0..1
	cov   []float32 // one paint's coverage
	dist  []float32 // one stroke's signed distance field
	edges []vecEdge
	act   []int32
	cross []vecCross
}

func vecGrowF32(buf []float32, n int) []float32 {
	if cap(buf) < n {
		return make([]float32, n)
	}
	return buf[:n]
}

// Reset sizes the canvas to w×h and clears the mask.
func (c *vecCanvas) Reset(w, h int) {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	n := w * h
	c.w, c.h = w, h
	c.alpha = vecGrowF32(c.alpha, n)
	c.cov = vecGrowF32(c.cov, n)
	c.dist = vecGrowF32(c.dist, n)
	for i := range c.alpha {
		c.alpha[i] = 0
	}
}

// Coverage is the composited mask value at (x, y), 0 outside the canvas.
func (c *vecCanvas) Coverage(x, y int) float64 {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return 0
	}
	return float64(c.alpha[y*c.w+x])
}

// AlphaBytes writes the mask as 8-bit alpha, row-major, into dst, which must
// hold at least w*h bytes.
func (c *vecCanvas) AlphaBytes(dst []uint8) {
	n := c.w * c.h
	if len(dst) < n {
		return
	}
	for i, a := range c.alpha[:n] {
		switch {
		case a <= 0:
			dst[i] = 0
		case a >= 1:
			dst[i] = 255
		default:
			dst[i] = uint8(a*255 + 0.5)
		}
	}
}

// clip turns a float box (grown by pad) into the pixel box it touches,
// clipped to the canvas.
func (c *vecCanvas) clip(minX, minY, maxX, maxY, pad float64) vecBox {
	b := vecBox{
		x0: int(math.Floor(minX - pad)),
		y0: int(math.Floor(minY - pad)),
		x1: int(math.Ceil(maxX + pad)),
		y1: int(math.Ceil(maxY + pad)),
	}
	if b.x0 < 0 {
		b.x0 = 0
	}
	if b.y0 < 0 {
		b.y0 = 0
	}
	if b.x1 > c.w {
		b.x1 = c.w
	}
	if b.y1 > c.h {
		b.y1 = c.h
	}
	return b
}

// Paint rasterizes p with op and composites it into the mask.
func (c *vecCanvas) Paint(p *vecPath, op vecPaint) {
	if p == nil || c.w == 0 || c.h == 0 || op.Opacity <= 0 {
		return
	}
	var box vecBox
	var ok bool
	if op.Stroke {
		box, ok = c.strokeCoverage(p, op.Width, op.Cap)
	} else {
		box, ok = c.fillCoverage(p, op.EvenOdd)
	}
	if ok {
		c.composite(box, op.Opacity, op.Erase)
	}
}

func (c *vecCanvas) addEdge(a, b vecPt) {
	if a.Y == b.Y {
		return
	}
	dir := int32(1)
	if a.Y > b.Y {
		a, b = b, a
		dir = -1
	}
	c.edges = append(c.edges, vecEdge{x0: a.X, y0: a.Y, y1: b.Y, dxdy: (b.X - a.X) / (b.Y - a.Y), dir: dir})
}

func vecInside(wind int32, evenOdd bool) bool {
	if evenOdd {
		return wind&1 != 0
	}
	return wind != 0
}

// vecAddSpan adds weight w of horizontal coverage over [xa, xb) to one row,
// splitting the partial pixels at both ends exactly.
func vecAddSpan(row []float32, xa, xb, w float64) {
	if xa < 0 {
		xa = 0
	}
	if xb > float64(len(row)) {
		xb = float64(len(row))
	}
	if xb <= xa {
		return
	}
	ia, ib := int(xa), int(xb)
	if ia == ib {
		row[ia] += float32((xb - xa) * w)
		return
	}
	row[ia] += float32((float64(ia+1) - xa) * w)
	for i := ia + 1; i < ib; i++ {
		row[i] += float32(w)
	}
	if ib < len(row) {
		row[ib] += float32((xb - float64(ib)) * w)
	}
}

// fillCoverage rasterizes p's subpaths, each implicitly closed, into c.cov and
// returns the pixel box it wrote.
func (c *vecCanvas) fillCoverage(p *vecPath, evenOdd bool) (vecBox, bool) {
	c.edges = c.edges[:0]
	for i := range p.starts {
		s, e := p.subpath(i)
		if e-s < 2 {
			continue // a lone point encloses nothing
		}
		for j := s; j < e; j++ {
			b := p.pts[s]
			if j+1 < e {
				b = p.pts[j+1]
			}
			c.addEdge(p.pts[j], b)
		}
	}
	if len(c.edges) == 0 {
		return vecBox{}, false
	}
	minX, minY, maxX, maxY, _ := p.bounds()
	box := c.clip(minX, minY, maxX, maxY, 0)
	if box.empty() {
		return box, false
	}
	for y := box.y0; y < box.y1; y++ {
		row := c.cov[y*c.w+box.x0 : y*c.w+box.x1]
		for i := range row {
			row[i] = 0
		}
	}
	const inv = 1.0 / vecSubScan
	for py := box.y0; py < box.y1; py++ {
		top, bot := float64(py), float64(py+1)
		c.act = c.act[:0]
		for k := range c.edges {
			if c.edges[k].y0 < bot && c.edges[k].y1 > top {
				c.act = append(c.act, int32(k))
			}
		}
		if len(c.act) == 0 {
			continue
		}
		row := c.cov[py*c.w : (py+1)*c.w]
		for s := 0; s < vecSubScan; s++ {
			ys := top + (float64(s)+0.5)*inv
			c.cross = c.cross[:0]
			for _, k := range c.act {
				e := &c.edges[k]
				if ys >= e.y0 && ys < e.y1 {
					c.cross = append(c.cross, vecCross{x: e.x0 + (ys-e.y0)*e.dxdy, dir: e.dir})
				}
			}
			for i := 1; i < len(c.cross); i++ {
				v := c.cross[i]
				j := i - 1
				for j >= 0 && c.cross[j].x > v.x {
					c.cross[j+1] = c.cross[j]
					j--
				}
				c.cross[j+1] = v
			}
			wind := int32(0)
			spanX := 0.0
			for _, cr := range c.cross {
				was := vecInside(wind, evenOdd)
				wind += cr.dir
				now := vecInside(wind, evenOdd)
				if !was && now {
					spanX = cr.x
				} else if was && !now {
					vecAddSpan(row, spanX, cr.x, inv)
				}
			}
		}
	}
	return box, true
}

// strokeCoverage rasterizes a stroke of the given width along p into c.cov
// and returns the pixel box it wrote. Coverage is 0.5 minus the signed
// distance to the stroke's outline, clamped to 0..1: a one-pixel ramp that is
// exact box-filter coverage for axis-aligned edges.
func (c *vecCanvas) strokeCoverage(p *vecPath, width float64, cp vecCap) (vecBox, bool) {
	if width <= 0 || len(p.pts) == 0 {
		return vecBox{}, false
	}
	hw := width / 2
	pad := hw + 1
	if cp == vecCapSquare {
		pad = hw*math.Sqrt2 + 1
	}
	minX, minY, maxX, maxY, _ := p.bounds()
	box := c.clip(minX, minY, maxX, maxY, pad)
	if box.empty() {
		return box, false
	}
	for y := box.y0; y < box.y1; y++ {
		row := c.dist[y*c.w+box.x0 : y*c.w+box.x1]
		for i := range row {
			row[i] = 1e9
		}
	}
	for i := range p.starts {
		s, e := p.subpath(i)
		n := e - s
		if n == 0 {
			continue
		}
		if n == 1 {
			// A lone point is a dot: a disc, or a square for square caps.
			if cp != vecCapButt {
				c.segDist(box, p.pts[s], p.pts[s], hw, cp, cp)
			}
			continue
		}
		closed := p.closed[i]
		segs := n - 1
		if closed {
			segs = n
		}
		for j := 0; j < segs; j++ {
			a := p.pts[s+j]
			b := p.pts[s+(j+1)%n]
			ca, cb := vecCapRound, vecCapRound
			if !closed && j == 0 {
				ca = cp
			}
			if !closed && j == segs-1 {
				cb = cp
			}
			c.segDist(box, a, b, hw, ca, cb)
		}
	}
	for y := box.y0; y < box.y1; y++ {
		base := y * c.w
		for x := box.x0; x < box.x1; x++ {
			v := 0.5 - c.dist[base+x]
			if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			c.cov[base+x] = v
		}
	}
	return box, true
}

// segDist lowers c.dist inside box to the signed distance from each pixel
// centre to the stroke of segment a→b (half-width hw), whose ends are shaped
// by ca and cb. Round ends make the segment a capsule; butt and square ends
// make the end a rectangle side, flush or extended by hw.
func (c *vecCanvas) segDist(box vecBox, a, b vecPt, hw float64, ca, cb vecCap) {
	ext := hw + 1
	if ca == vecCapSquare || cb == vecCapSquare {
		ext = hw*math.Sqrt2 + 1
	}
	lo := vecPt{X: a.X, Y: a.Y}
	hi := vecPt{X: b.X, Y: b.Y}
	if lo.X > hi.X {
		lo.X, hi.X = hi.X, lo.X
	}
	if lo.Y > hi.Y {
		lo.Y, hi.Y = hi.Y, lo.Y
	}
	sb := c.clip(lo.X, lo.Y, hi.X, hi.Y, ext)
	if sb.x0 < box.x0 {
		sb.x0 = box.x0
	}
	if sb.y0 < box.y0 {
		sb.y0 = box.y0
	}
	if sb.x1 > box.x1 {
		sb.x1 = box.x1
	}
	if sb.y1 > box.y1 {
		sb.y1 = box.y1
	}
	dx, dy := b.X-a.X, b.Y-a.Y
	l := math.Sqrt(dx*dx + dy*dy)
	ux, uy := 1.0, 0.0
	if l > 1e-9 {
		ux, uy = dx/l, dy/l
	} else {
		l = 0
	}
	for py := sb.y0; py < sb.y1; py++ {
		qy := float64(py) + 0.5 - a.Y
		base := py * c.w
		for px := sb.x0; px < sb.x1; px++ {
			qx := float64(px) + 0.5 - a.X
			u := qx*ux + qy*uy
			v := qy*ux - qx*uy
			if v < 0 {
				v = -v
			}
			var d float64
			switch {
			case u < 0 && ca == vecCapRound:
				d = math.Sqrt(u*u+v*v) - hw
			case u > l && cb == vecCapRound:
				t := u - l
				d = math.Sqrt(t*t+v*v) - hw
			default:
				ex := -1e9
				if ca != vecCapRound {
					ex = -u
					if ca == vecCapSquare {
						ex -= hw
					}
				}
				if cb != vecCapRound {
					t := u - l
					if cb == vecCapSquare {
						t -= hw
					}
					if t > ex {
						ex = t
					}
				}
				ey := v - hw
				switch {
				case ex > 0 && ey > 0:
					d = math.Sqrt(ex*ex + ey*ey)
				case ex > ey:
					d = ex
				default:
					d = ey
				}
			}
			if f := float32(d); f < c.dist[base+px] {
				c.dist[base+px] = f
			}
		}
	}
}

// composite merges c.cov inside box into the mask: "over" adds ink at the
// given opacity, erase removes coverage.
func (c *vecCanvas) composite(box vecBox, opacity float64, erase bool) {
	op := float32(opacity)
	if op > 1 {
		op = 1
	}
	for y := box.y0; y < box.y1; y++ {
		base := y * c.w
		for x := box.x0; x < box.x1; x++ {
			v := c.cov[base+x]
			if v <= 0 {
				continue
			}
			if v > 1 {
				v = 1
			}
			v *= op
			a := c.alpha[base+x]
			if erase {
				c.alpha[base+x] = a * (1 - v)
			} else {
				c.alpha[base+x] = a + v*(1-a)
			}
		}
	}
}
