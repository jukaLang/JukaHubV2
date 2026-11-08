package main

// iconlang.go — the icon language behind icondefs.go.
//
// An icon is a list of paint operations on a 24×24 grid. Each operation is a
// head token ending in ':' followed by path commands. The head's first letter
// is the kind:
//
//	s  stroke the path in the base stroke width (round caps and joins)
//	f  fill the path in full ink
//	d  fill the path as a duotone body (iconDuoOpacity)
//	D  duotone fill, then stroke the same path: the usual "object" look
//	F  solid fill, then stroke the same path: a filled shape with the
//	   stroke's rounded corners
//	k  knock the filled path out of everything painted so far
//	K  knock a stroke out: the gap that separates overlapping shapes
//
// Flags may follow the kind: w<n> stroke width as a multiple of the base
// width, o<n> opacity 0..1, b butt caps, q square caps, e even-odd fill rule,
// n no pixel snapping, a snap to the base stroke's pixel grid (so a dot or a
// wider stroke stays centred on the base-width lines it sits on), m<n> draw
// only at sizes >= n px, x<n> only below n px.
//
// Path commands take absolute grid coordinates, separated by spaces or
// commas. A command's argument list may repeat; M continues as L.
//
//	M x y               move               L x y        line
//	H x                 horizontal line    V y          vertical line
//	Q cx cy x y         quadratic Bézier
//	C x1 y1 x2 y2 x y   cubic Bézier
//	A cx cy r a0 a1     arc around (cx, cy) from a0 to a1 degrees, clockwise
//	                    from +x; it joins an open subpath with a line
//	U cx cy r a0 a1     the same arc, always as a subpath of its own
//	Z                   close the subpath
//	O cx cy r           circle             E cx cy rx ry   ellipse
//	R x y w h r         rounded rectangle
//	G cx cy r n rot     regular n-gon      T cx cy ro ri n rot   n-point star
//
// Rendering scales the grid to the requested pixel size and snaps it to the
// pixel grid (unless an op says n): line ends, rectangle edges and circle
// centres move by at most half a pixel so stroke and fill edges land on whole
// pixels. That is what keeps a 2px outline a solid 2px line instead of a
// blurred 3px one at 32px, where one grid unit is 1.33px. Curve control points
// follow their end points, and circle and arc radii are rounded so both edges
// of a stroked ring stay crisp.
//
// outputs/icons_preview.py is a line-by-line port of this file and of
// vecraster.go; a change to the language must be made in both.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// iconDuoOpacity is the ink strength of a duotone body (DUO_OPACITY in the
// preview script).
const iconDuoOpacity = 0.30

// iconFallbackName is the glyph an unknown name draws, so a typo in a config
// still paints a recognisable "app" mark instead of an empty well.
const iconFallbackName = "placeholder"

// iconCmd is one path command with its arguments in grid units.
type iconCmd struct {
	op   byte
	args [6]float64
}

// iconOp is one paint operation of an icon.
type iconOp struct {
	kind    byte // s f d D F k K
	width   float64
	opacity float64 // < 0: the kind's default
	cap     vecCap
	evenOdd bool
	noSnap  bool
	align   bool
	minSize float64
	maxSize float64
	cmds    []iconCmd
}

// iconCmdArity is how many numbers command c takes, or -1 when c is not a
// command letter.
func iconCmdArity(c byte) int {
	switch c {
	case 'Z':
		return 0
	case 'H', 'V':
		return 1
	case 'M', 'L':
		return 2
	case 'O':
		return 3
	case 'Q', 'E':
		return 4
	case 'A', 'U', 'R', 'G':
		return 5
	case 'C', 'T':
		return 6
	}
	return -1
}

// iconTok is one token of an icon source.
type iconTok struct {
	kind byte // 'h' op head, 'c' command, 'n' number
	text string
	cmd  byte
	num  float64
}

func iconIsDigit(c byte) bool { return c >= '0' && c <= '9' }

// iconTokenize splits an icon source into op heads, command letters and
// numbers.
func iconTokenize(src string) ([]iconTok, error) {
	fields := strings.FieldsFunc(src, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ',', ';':
			return true
		}
		return false
	})
	toks := make([]iconTok, 0, len(fields)*2)
	for _, raw := range fields {
		if strings.HasSuffix(raw, ":") {
			toks = append(toks, iconTok{kind: 'h', text: raw[:len(raw)-1]})
			continue
		}
		for i := 0; i < len(raw); {
			ch := raw[i]
			if iconCmdArity(ch) >= 0 {
				toks = append(toks, iconTok{kind: 'c', cmd: ch})
				i++
				continue
			}
			if iconIsDigit(ch) || ch == '-' || ch == '.' {
				j := i + 1
				for j < len(raw) && (iconIsDigit(raw[j]) || raw[j] == '.') {
					j++
				}
				v, err := strconv.ParseFloat(raw[i:j], 64)
				if err != nil {
					return nil, fmt.Errorf("bad number %q", raw[i:j])
				}
				toks = append(toks, iconTok{kind: 'n', num: v})
				i = j
				continue
			}
			return nil, fmt.Errorf("unexpected %q in %q", ch, raw)
		}
	}
	return toks, nil
}

// iconParseHead parses an op head such as "s", "Dw1.5" or "fo.4e".
func iconParseHead(h string) (iconOp, error) {
	op := iconOp{width: 1, opacity: -1}
	if h == "" || !strings.ContainsRune("sfdDFkK", rune(h[0])) {
		return op, fmt.Errorf("bad op head %q", h)
	}
	op.kind = h[0]
	for i := 1; i < len(h); {
		f := h[i]
		i++
		switch f {
		case 'w', 'o', 'm', 'x':
			j := i
			for j < len(h) && (iconIsDigit(h[j]) || h[j] == '.') {
				j++
			}
			if j == i {
				return op, fmt.Errorf("flag %q needs a number in %q", f, h)
			}
			v, err := strconv.ParseFloat(h[i:j], 64)
			if err != nil {
				return op, fmt.Errorf("bad number in op head %q", h)
			}
			i = j
			switch f {
			case 'w':
				op.width = v
			case 'o':
				op.opacity = v
			case 'm':
				op.minSize = v
			default:
				op.maxSize = v
			}
		case 'b':
			op.cap = vecCapButt
		case 'q':
			op.cap = vecCapSquare
		case 'e':
			op.evenOdd = true
		case 'n':
			op.noSnap = true
		case 'a':
			op.align = true
		default:
			return op, fmt.Errorf("unknown flag %q in op head %q", f, h)
		}
	}
	return op, nil
}

// iconNumbersAt reports whether toks[i:i+n] are all numbers.
func iconNumbersAt(toks []iconTok, i, n int) bool {
	if i+n > len(toks) {
		return false
	}
	for _, t := range toks[i : i+n] {
		if t.kind != 'n' {
			return false
		}
	}
	return true
}

// iconParse parses one icon source into its paint operations.
func iconParse(src string) ([]iconOp, error) {
	toks, err := iconTokenize(src)
	if err != nil {
		return nil, err
	}
	var ops []iconOp
	for i := 0; i < len(toks); {
		t := toks[i]
		if t.kind == 'h' {
			if len(ops) > 0 && len(ops[len(ops)-1].cmds) == 0 {
				return nil, fmt.Errorf("op before %q has no commands", t.text)
			}
			head, herr := iconParseHead(t.text)
			if herr != nil {
				return nil, herr
			}
			ops = append(ops, head)
			i++
			continue
		}
		if len(ops) == 0 {
			return nil, fmt.Errorf("commands before the first op head")
		}
		if t.kind != 'c' {
			return nil, fmt.Errorf("number %g without a command", t.num)
		}
		op := &ops[len(ops)-1]
		n := iconCmdArity(t.cmd)
		i++
		first := true
		for {
			if n == 0 {
				op.cmds = append(op.cmds, iconCmd{op: t.cmd})
				break
			}
			if !iconNumbersAt(toks, i, n) {
				if first {
					return nil, fmt.Errorf("%c needs %d numbers", t.cmd, n)
				}
				break
			}
			c := iconCmd{op: t.cmd}
			if t.cmd == 'M' && !first {
				c.op = 'L'
			}
			for k := 0; k < n; k++ {
				c.args[k] = toks[i+k].num
			}
			i += n
			op.cmds = append(op.cmds, c)
			first = false
			if i >= len(toks) || toks[i].kind != 'n' {
				break
			}
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("empty icon")
	}
	if len(ops[len(ops)-1].cmds) == 0 {
		return nil, fmt.Errorf("the last op has no commands")
	}
	return ops, nil
}

// iconStrokePx is the base stroke width in pixels at a glyph size. Below 22px
// it keeps a constant two-unit weight, fractional, so small glyphs do not turn
// hairline-thin; from 22px it is a whole number of pixels, so every
// axis-aligned stroke lands crisp on the pixel grid.
func iconStrokePx(size int) float64 {
	if size < 22 {
		return math.Max(1, float64(size)/12)
	}
	return math.Max(1, math.Floor(float64(size)/16+0.25+0.5))
}

// iconStrokeParity is the half-pixel offset of stroke centrelines: a stroke
// whose whole-pixel core is odd is centred on a pixel and an even one on a
// pixel edge, so the core is always made of fully covered pixels.
func iconStrokeParity(w float64) float64 {
	if int(math.Floor(w+0.01))%2 == 1 {
		return 0.5
	}
	return 0
}

// iconSnap moves v to the nearest k+q.
func iconSnap(v, q float64) float64 { return math.Floor(v-q+0.5) + q }

// iconPathBuilder turns one op's commands into a pixel-space path at scale s,
// snapping anchors to the offset p (0 for pixel edges, 0.5 for pixel centres).
// align marks a fill that follows the base stroke's grid instead of pixel
// edges (the a flag).
type iconPathBuilder struct {
	s      float64
	p      float64
	snap   bool
	stroke bool
	align  bool
}

func (b iconPathBuilder) sn(v float64) float64 {
	if !b.snap {
		return v
	}
	return iconSnap(v, b.p)
}

// axis snaps a circle's centre and radius along one axis. A stroked ring keeps
// a whole-pixel radius around a stroke-aligned centre, so both of its edges
// are crisp; a filled disc keeps whole-pixel edges, and an aligned disc keeps
// its centre on the stroke grid with a diameter of matching parity.
func (b iconPathBuilder) axis(c, r float64) (float64, float64) {
	if !b.snap {
		return c, r
	}
	if b.stroke {
		return b.sn(c), math.Max(0.5, math.Floor(r+0.5))
	}
	if b.align {
		var d float64
		if b.p == 0.5 {
			d = 2*math.Floor(r) + 1
		} else {
			d = math.Max(2, 2*math.Floor(r+0.5))
		}
		return iconSnap(c, b.p), d / 2
	}
	d := math.Max(1, math.Floor(2*r+0.5))
	q := 0.0
	if int(d)%2 == 1 {
		q = 0.5
	}
	return iconSnap(c, q), d / 2
}

func (b iconPathBuilder) circle(cx, cy, r float64) (float64, float64, float64) {
	x, rr := b.axis(cx, r)
	y, _ := b.axis(cy, r)
	return x, y, rr
}

// build replaces path with the op's commands in pixel space.
func (b iconPathBuilder) build(path *vecPath, cmds []iconCmd) {
	path.Reset()
	s := b.s
	var curRaw, cur vecPt
	for _, c := range cmds {
		a := c.args
		switch c.op {
		case 'M', 'L':
			rx, ry := a[0]*s, a[1]*s
			x, y := b.sn(rx), b.sn(ry)
			if c.op == 'M' {
				path.MoveTo(x, y)
			} else {
				path.LineTo(x, y)
			}
			curRaw, cur = vecPt{X: rx, Y: ry}, vecPt{X: x, Y: y}
		case 'H':
			rx := a[0] * s
			x := b.sn(rx)
			path.LineTo(x, cur.Y)
			curRaw, cur = vecPt{X: rx, Y: curRaw.Y}, vecPt{X: x, Y: cur.Y}
		case 'V':
			ry := a[0] * s
			y := b.sn(ry)
			path.LineTo(cur.X, y)
			curRaw, cur = vecPt{X: curRaw.X, Y: ry}, vecPt{X: cur.X, Y: y}
		case 'Q':
			d0x, d0y := cur.X-curRaw.X, cur.Y-curRaw.Y
			ex, ey := a[2]*s, a[3]*s
			x, y := b.sn(ex), b.sn(ey)
			d1x, d1y := x-ex, y-ey
			path.QuadTo(a[0]*s+(d0x+d1x)/2, a[1]*s+(d0y+d1y)/2, x, y)
			curRaw, cur = vecPt{X: ex, Y: ey}, vecPt{X: x, Y: y}
		case 'C':
			d0x, d0y := cur.X-curRaw.X, cur.Y-curRaw.Y
			ex, ey := a[4]*s, a[5]*s
			x, y := b.sn(ex), b.sn(ey)
			path.CubicTo(a[0]*s+d0x, a[1]*s+d0y, a[2]*s+(x-ex), a[3]*s+(y-ey), x, y)
			curRaw, cur = vecPt{X: ex, Y: ey}, vecPt{X: x, Y: y}
		case 'A', 'U':
			cx, cy, r := b.circle(a[0]*s, a[1]*s, a[2]*s)
			if c.op == 'U' {
				path.End()
			}
			path.Arc(cx, cy, r, a[3], a[4])
			curRaw, cur = path.cur, path.cur
		case 'Z':
			path.Close()
			curRaw, cur = path.cur, path.cur
		case 'O':
			cx, cy, r := b.circle(a[0]*s, a[1]*s, a[2]*s)
			path.Circle(cx, cy, r)
			curRaw, cur = path.cur, path.cur
		case 'E':
			cx, rx := b.axis(a[0]*s, a[2]*s)
			cy, ry := b.axis(a[1]*s, a[3]*s)
			path.Ellipse(cx, cy, rx, ry)
			curRaw, cur = path.cur, path.cur
		case 'R':
			x0, y0 := b.sn(a[0]*s), b.sn(a[1]*s)
			x1, y1 := b.sn((a[0]+a[2])*s), b.sn((a[1]+a[3])*s)
			path.RoundRect(x0, y0, x1-x0, y1-y0, a[4]*s)
			curRaw, cur = path.cur, path.cur
		case 'G':
			path.Polygon(b.sn(a[0]*s), b.sn(a[1]*s), a[2]*s, int(a[3]), a[4])
			curRaw, cur = path.cur, path.cur
		case 'T':
			path.Star(b.sn(a[0]*s), b.sn(a[1]*s), a[2]*s, a[3]*s, int(a[4]), a[5])
			curRaw, cur = path.cur, path.cur
		}
	}
}

// iconRaster renders ops at size×size pixels into cv, which it resets; path is
// scratch.
func iconRaster(ops []iconOp, size int, cv *vecCanvas, path *vecPath) {
	cv.Reset(size, size)
	if size <= 0 {
		return
	}
	s := float64(size) / 24
	base := iconStrokePx(size)
	fsize := float64(size)
	for i := range ops {
		op := &ops[i]
		if op.minSize > 0 && fsize < op.minSize {
			continue
		}
		if op.maxSize > 0 && fsize >= op.maxSize {
			continue
		}
		w := base * op.width
		ps := iconStrokeParity(w)
		pf := 0.0
		if op.align {
			ps = iconStrokeParity(base)
			pf = ps
		}
		snap := !op.noSnap
		if op.kind == 'D' || op.kind == 'F' {
			fillOpacity := 1.0
			if op.kind == 'D' {
				fillOpacity = iconDuoOpacity
			}
			if op.opacity >= 0 {
				fillOpacity = op.opacity
			}
			iconPathBuilder{s: s, p: pf, snap: snap, align: op.align}.build(path, op.cmds)
			cv.Paint(path, vecPaint{EvenOdd: op.evenOdd, Opacity: fillOpacity})
			strokeOpacity := 1.0
			if op.kind == 'F' && op.opacity >= 0 {
				strokeOpacity = op.opacity
			}
			iconPathBuilder{s: s, p: ps, snap: snap, stroke: true}.build(path, op.cmds)
			cv.Paint(path, vecPaint{Stroke: true, Width: w, Cap: op.cap, Opacity: strokeOpacity})
			continue
		}
		stroke := op.kind == 's' || op.kind == 'K'
		b := iconPathBuilder{s: s, p: pf, snap: snap, stroke: stroke, align: op.align}
		if stroke {
			b.p = ps
		}
		b.build(path, op.cmds)
		opacity := 1.0
		if op.kind == 'd' {
			opacity = iconDuoOpacity
		}
		if op.opacity >= 0 {
			opacity = op.opacity
		}
		cv.Paint(path, vecPaint{
			Stroke:  stroke,
			Width:   w,
			Cap:     op.cap,
			EvenOdd: op.evenOdd,
			Opacity: opacity,
			Erase:   op.kind == 'k' || op.kind == 'K',
		})
	}
}

// iconLibrary is the parsed icon set.
type iconLibrary struct {
	glyphs  map[string][]iconOp // by canonical name
	aliases map[string]string
	order   []string // canonical names in table order
	errs    []error  // sources that failed to parse; empty in a healthy build
}

var (
	iconLibOnce sync.Once
	iconLib     *iconLibrary
)

// iconLibGet parses icondefs.go's table on first use (about a millisecond)
// and returns the library.
func iconLibGet() *iconLibrary {
	iconLibOnce.Do(func() {
		lib := &iconLibrary{
			glyphs:  make(map[string][]iconOp, len(iconDefs)),
			aliases: make(map[string]string, len(iconAliasPairs)),
		}
		for _, d := range iconDefs {
			ops, err := iconParse(d.src)
			if err != nil {
				lib.errs = append(lib.errs, fmt.Errorf("icon %q: %w", d.name, err))
				continue
			}
			if _, dup := lib.glyphs[d.name]; dup {
				lib.errs = append(lib.errs, fmt.Errorf("icon %q is defined twice", d.name))
				continue
			}
			lib.glyphs[d.name] = ops
			lib.order = append(lib.order, d.name)
		}
		for _, p := range iconAliasPairs {
			lib.aliases[p[0]] = p[1]
		}
		iconLib = lib
	})
	return iconLib
}

// iconResolve maps an icon name — canonical or alias, in any case, with any
// padding — to the canonical glyph that draws it. Unknown names resolve to
// iconFallbackName.
func iconResolve(name string) string {
	lib := iconLibGet()
	key := strings.ToLower(strings.TrimSpace(name))
	if _, ok := lib.glyphs[key]; ok {
		return key
	}
	if target, ok := lib.aliases[key]; ok {
		if _, ok := lib.glyphs[target]; ok {
			return target
		}
	}
	return iconFallbackName
}

// iconKnown reports whether name draws its own glyph rather than the fallback.
func iconKnown(name string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	return key == iconFallbackName || iconResolve(key) != iconFallbackName
}

// iconRasterize renders the named icon (resolved like iconResolve) at
// size×size into dst as 8-bit alpha; cv and path are reusable scratch.
func iconRasterize(name string, size int, dst []uint8, cv *vecCanvas, path *vecPath) {
	iconRaster(iconLibGet().glyphs[iconResolve(name)], size, cv, path)
	cv.AlphaBytes(dst)
}
