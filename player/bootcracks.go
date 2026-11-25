package main

// bootcracks.go — the cracks the cinematic boot's logo leaves in the ground
// (bootstyles.go).
//
// Juka, 2026-11-24: "we should make cracks on that one boot animation more
// realistic". The 11-23 cracks were lines a few pixels wide wandering out from
// the logo, glowing in the accent and cooling: they read as lightning, not as
// broken stone. These are cut into the stone, the way a slab breaks under a
// heavy blow, seen from above:
//
//   - the main cracks start under the tile — from its corners, where the blow
//     is hardest, then from its sides — and run out across the ground in
//     straight runs that waver and now and then kink, with a fine zigzag along
//     each run. They are widest at the tile and taper to nothing, their two
//     lips waver apart, near the impact a lip now and then chips out, and they
//     fork into thinner cracks that fork again;
//   - rings of cracks join them round the tile: a near ring that hugs the
//     tile's shape (the edge it was driven into) and a far one cut straight
//     across from crack to crack, which often stops partway. So the slab round
//     the tile is broken into pieces, and each piece that broke right off is
//     tipped a little and catches more or less of the light;
//   - short hair cracks all round the tile's edge, a crushed shadow hugging it,
//     and a little grit thrown out of the cracks, each grain with its shadow;
//   - every crack is a groove lit from the top-left, like the logo's shadow:
//     dark, darkest along its bottom, a soft shadow on its upper-left rim and a
//     thin bright lip along the far wall, which faces the light.
//
// The layer is drawn once, on the CPU and off the render thread (an estimated
// few tens of milliseconds on the handheld, while the logo falls), into one
// screen-sized texture uploaded a quarter at a time over four frames, so no
// frame of the fall pays for all of it. From the impact a disc round the tile
// reveals it, running out fast and slowing to a stop over bootCrackGrowMS, its
// edge soft, so the cracks run out from under the tile (a fan of triangles
// reading the layer: SDL_RenderGeometry; where the driver has none, a square
// window grows instead). A layer that is not ready by the impact joins as soon
// as it is.
//
// branding/mkbootcracks.py draws the same layer from the same seed, step for
// step (the same random draws in the same order): change the two together,
// and preview a change there first.

import (
	"math"
	"sort"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// bootCrackGrowMS is how long the cracks take to reach their full length.
	bootCrackGrowMS = 420.0
	// bootCrackMaxNodes bounds the crack network's points.
	bootCrackMaxNodes = 2400
	// bootCrackBands is how many frames the layer's upload is spread over.
	bootCrackBands = 4
	// bootCrackFanSides is how many sides the disc that reveals the layer has.
	bootCrackFanSides = 64
	// bootCrackWall is how dark (of 255) a pixel must be to part two pieces of
	// the slab.
	bootCrackWall = 89
)

// bootCrackNode is one point of a crack: where it is, and how far each lip is
// from its middle there — hp on the side its normal points to (left of the
// way it runs, on a screen whose y grows down), hm on the other.
type bootCrackNode struct {
	x, y   float64
	hp, hm float64
}

// bootCrackPlan is one boot's crack network, in canvas pixels: the screen, the
// tile's centre, side, half-side and corner, the cracks' longest reach, their
// step and starting half-width, how far out a lip may chip, the far ring's
// distance past the tile's edge, the cracks as polylines, the grit (x, y,
// radius), and the random state everything is drawn from.
type bootCrackPlan struct {
	w, h, cx, cy float64
	tile, a, rc  float64
	maxLen, step float64
	hw0, chipR   float64
	ringOff      float64
	lines        [][]bootCrackNode
	nodes        int
	pebbles      [][3]float64
	rng          uint32
}

// bootCrackImage is the drawn layer, handed from its worker to the render
// thread: w*h straight-alpha pixels (R, G, B, A), how far from the tile's
// centre the farthest crack ends, and where the cracks start (the tile's edge).
type bootCrackImage struct {
	pix   []byte
	w, h  int
	reach float64
	start float64
}

// bootCrackLayer is the layer's life on the render thread: the worker's
// answer until it is taken, the image, its texture and the rows uploaded so
// far, whether the driver refused geometry, and the reveal's vertices.
type bootCrackLayer struct {
	ready  <-chan *bootCrackImage
	img    *bootCrackImage
	tex    *sdl.Texture
	rows   int
	noGeom bool
	verts  []sdl.Vertex
}

// bootCrackFanIdx is the reveal's triangles: the disc's fan, then the band of
// its soft edge.
var bootCrackFanIdx = bootCrackFanIndices(bootCrackFanSides)

// bootCrackFanIndices lists the triangles of a disc of n sides: vertex 0 is
// the centre, 1..n the inner ring, n+1..2n the outer one.
func bootCrackFanIndices(n int) []int32 {
	idx := make([]int32, 0, 9*n)
	for i := 0; i < n; i++ {
		a, b := int32(1+i), int32(1+(i+1)%n)
		oa, ob := a+int32(n), b+int32(n)
		idx = append(idx, 0, a, b, a, oa, ob, a, ob, b)
	}
	return idx
}

// ── The tile's footprint ─────────────────────────────────────────────────

// bootFootSD is how far (x, y), from the tile's centre, lies outside the
// tile's rounded square of half-side a and corner radius rc (negative inside).
func bootFootSD(x, y, a, rc float64) float64 {
	qx, qy := math.Abs(x)-(a-rc), math.Abs(y)-(a-rc)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - rc
}

// bootFootR is how far from the tile's centre its edge is at angle theta.
func bootFootR(theta, a, rc float64) float64 {
	c, s := math.Cos(theta), math.Sin(theta)
	lo, hi := 0.0, 1.5*a
	for i := 0; i < 22; i++ {
		mid := (lo + hi) / 2
		if bootFootSD(c*mid, s*mid, a, rc) < 0 {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// bootAngleDiff is a-b wrapped to (-pi, pi].
func bootAngleDiff(a, b float64) float64 {
	d := math.Mod(a-b, 2*math.Pi)
	if d > math.Pi {
		d -= 2 * math.Pi
	} else if d <= -math.Pi {
		d += 2 * math.Pi
	}
	return d
}

// ── The network ──────────────────────────────────────────────────────────

// bootCrackGrow lays one boot's cracks out round a tile of side tile resting
// at (cx, cy) on a w*h screen, drawn from seed.
func bootCrackGrow(w, h, cx, cy, tile float64, seed uint32) *bootCrackPlan {
	maxLen := 0.46 * math.Max(w, h)
	p := &bootCrackPlan{
		w: w, h: h, cx: cx, cy: cy,
		tile: tile, a: tile / 2, rc: tile * bootLogoTileCorner,
		maxLen: maxLen, step: maxLen * 0.032,
		hw0: tile * 0.014, chipR: tile * 1.6,
		rng: seed,
	}
	if p.rng == 0 {
		p.rng = 1
	}
	rnd := p.rand
	// The main cracks: from the tile's corners, where the blow is hardest,
	// then into the widest gaps.
	type mainCrack struct {
		angle  float64
		corner bool
	}
	starts := make([]mainCrack, 0, 10)
	for k := 0; k < 4; k++ {
		jit := (rnd() - 0.5) * 0.30
		if rnd() < 0.85 {
			starts = append(starts, mainCrack{math.Pi/4 + float64(k)*math.Pi/2 + jit, true})
		}
	}
	n := 7 + int(rnd()*3)
	for try := 0; try < 40 && len(starts) < n; try++ {
		t := rnd() * 2 * math.Pi
		apart := true
		for _, m := range starts {
			if math.Abs(bootAngleDiff(t, m.angle)) <= 0.40 {
				apart = false
				break
			}
		}
		if apart {
			starts = append(starts, mainCrack{t, false})
		}
	}
	turnOf := func(a float64) float64 { return math.Mod(a+4*math.Pi, 2*math.Pi) }
	sort.Slice(starts, func(i, j int) bool { return turnOf(starts[i].angle) < turnOf(starts[j].angle) })
	mains := make([][]bootCrackNode, 0, len(starts))
	for _, m := range starts {
		r0 := bootFootR(m.angle, p.a, p.rc) * 0.94
		u := rnd()
		length := maxLen * (0.35 + 0.45*u)
		if m.corner {
			length = maxLen * (0.6 + 0.4*u)
		}
		heading := m.angle + (rnd()-0.5)*0.25
		hw := p.hw0 * (0.8 + 0.4*rnd())
		mains = append(mains, p.walk(p.cx+math.Cos(m.angle)*r0, p.cy+math.Sin(m.angle)*r0, heading, length, hw, 0, p.step))
	}
	// The rings: the slab round the tile broken into pieces between the main
	// cracks, a near ring that mostly closes and a far one that often does not.
	nearOff := tile * (0.16 + 0.08*rnd())
	farOff := tile * (0.45 + 0.15*rnd())
	rings := [2]struct {
		off, chance, hw float64
		chord           bool
	}{
		{nearOff, 0.8, p.hw0 * 0.45, false},
		{farOff, 0.55, p.hw0 * 0.35, true},
	}
	p.ringOff = farOff
	type crossing struct {
		x, y float64
		ok   bool
	}
	cross := make([]crossing, len(mains))
	for _, ring := range rings {
		for i, nodes := range mains {
			x, y, ok := p.ringCross(nodes, ring.off)
			cross[i] = crossing{x, y, ok}
		}
		for i := range mains {
			if rnd() >= ring.chance {
				continue
			}
			a, b := cross[i], cross[(i+1)%len(mains)]
			if !a.ok || !b.ok || len(mains) < 3 {
				continue
			}
			p.ringArc(a.x, a.y, b.x, b.y, ring.off, ring.hw, ring.chord)
		}
	}
	// The crushed edge: short hair cracks all round the tile.
	for i, count := 0, 16+int(rnd()*10); i < count; i++ {
		t := rnd() * 2 * math.Pi
		r0 := bootFootR(t, p.a, p.rc) * 0.97
		heading := t + (rnd()-0.5)*1.2
		length := tile * (0.04 + 0.10*rnd())
		hw := p.hw0 * (0.3 + 0.25*rnd())
		p.walk(p.cx+math.Cos(t)*r0, p.cy+math.Sin(t)*r0, heading, length, hw, 2, p.step*0.4)
	}
	// Grit thrown out of the cracks.
	for i := 0; i < 12; i++ {
		t := rnd() * 2 * math.Pi
		d := rnd()
		r := tile * (0.005 + 0.009*rnd())
		far := bootFootR(t, p.a, p.rc) + r + 2 + tile*1.3*d*d
		p.pebbles = append(p.pebbles, [3]float64{p.cx + math.Cos(t)*far, p.cy + math.Sin(t)*far, r})
	}
	return p
}

// rand is the plan's next random number in [0, 1).
func (p *bootCrackPlan) rand() float64 { return bootStyleRand(&p.rng) }

// walk adds one crack from (x, y), heading that way, length long, hw0 from its
// middle to each lip at its start; depth counts the forks it came from (2: it
// forks no more), step is the length of its runs. Each run wavers, now and
// then kinks and leans back away from the tile, and has a fine zigzag in its
// middle; the crack tapers to nothing at its end, its two lips waver apart,
// near the impact a main crack's lip now and then chips out, and it forks into
// thinner cracks. It returns the crack's points.
func (p *bootCrackPlan) walk(x, y, heading, length, hw0 float64, depth int, step float64) []bootCrackNode {
	rnd := p.rand
	nodes := []bootCrackNode{{x: x, y: y, hp: hw0, hm: hw0}}
	travelled, pp, pm := 0.0, hw0, hw0
	for travelled < length && p.nodes < bootCrackMaxNodes {
		l := step * (0.6 + 0.8*rnd())
		turn := (rnd() - 0.5) * 0.30
		if rnd() < 0.16 {
			k := 0.22 + 0.30*rnd()
			if rnd() < 0.5 {
				k = -k
			}
			turn += k
		}
		out := math.Atan2(y-p.cy, x-p.cx)
		heading += turn + bootAngleDiff(out, heading)*0.10
		c, s := math.Cos(heading), math.Sin(heading)
		nx, ny := x+c*l, y+s*l
		j := (rnd() - 0.5) * 0.28 * l
		mx, my := (x+nx)/2-s*j, (y+ny)/2+c*j
		travelled += l
		f := math.Min(1, travelled/length)
		hw := hw0 * math.Pow(1-f, 0.75)
		hp := hw * (0.7 + 0.6*rnd())
		hm := hw * (0.7 + 0.6*rnd())
		if depth == 0 && math.Hypot(nx-p.cx, ny-p.cy) < p.chipR && rnd() < 0.22 {
			if rnd() < 0.5 {
				hp *= 1.8 + 1.2*rnd()
			} else {
				hm *= 1.8 + 1.2*rnd()
			}
		}
		nodes = append(nodes, bootCrackNode{x: mx, y: my, hp: (pp + hp) / 2, hm: (pm + hm) / 2}, bootCrackNode{x: nx, y: ny, hp: hp, hm: hm})
		p.nodes += 2
		pp, pm = hp, hm
		x, y = nx, ny
		if x < -l || y < -l || x > p.w+l || y > p.h+l {
			break
		}
		chance := 0.07
		if depth == 0 {
			chance = 0.12
		}
		if depth < 2 && length-travelled > step && rnd() < chance {
			fork := 0.32 + 0.42*rnd()
			if rnd() < 0.5 {
				fork = -fork
			}
			left := (length - travelled) * (0.3 + 0.35*rnd())
			p.walk(x, y, heading+fork, left, math.Max(hw*0.75, 0.35), depth+1, step)
		}
	}
	p.lines = append(p.lines, nodes)
	return nodes
}

// ringCross is where a crack first gets off past the tile's edge, if it does.
func (p *bootCrackPlan) ringCross(nodes []bootCrackNode, off float64) (float64, float64, bool) {
	for i := 1; i < len(nodes); i++ {
		a, b := &nodes[i-1], &nodes[i]
		d1 := bootFootSD(b.x-p.cx, b.y-p.cy, p.a, p.rc)
		if d1 < off {
			continue
		}
		d0 := bootFootSD(a.x-p.cx, a.y-p.cy, p.a, p.rc)
		u := 1.0
		if d1 != d0 {
			u = bootClamp01((off - d0) / (d1 - d0))
		}
		return a.x + (b.x-a.x)*u, a.y + (b.y-a.y)*u, true
	}
	return 0, 0, false
}

// ringArc adds a crack round the impact from (x0, y0) to (x1, y1), both on
// main cracks off past the tile's edge: hugging the tile's shape (the crushed
// ring) or cut straight across (chord), bowing a little either way, hw from
// its middle to each lip; some stop partway, tapering out.
func (p *bootCrackPlan) ringArc(x0, y0, x1, y1, off, hw float64, chord bool) {
	rnd := p.rand
	t0 := math.Atan2(y0-p.cy, x0-p.cx)
	t1 := math.Atan2(y1-p.cy, x1-p.cx)
	span := math.Mod(t1-t0+4*math.Pi, 2*math.Pi)
	bulge := (rnd() - 0.5) * 0.35 * off
	stop := 1.0
	if rnd() < 0.3 {
		stop = 0.35 + 0.4*rnd()
	}
	if span <= 0.02 || span > math.Pi {
		return
	}
	r0 := math.Hypot(x0-p.cx, y0-p.cy)
	r1 := math.Hypot(x1-p.cx, y1-p.cy)
	e0 := r0 - (bootFootR(t0, p.a, p.rc) + off)
	e1 := r1 - (bootFootR(t1, p.a, p.rc) + off)
	mean := (r0 + r1) / 2
	m := max(2, int(math.Ceil(span*mean/(p.step*0.6))))
	nodes := make([]bootCrackNode, 0, m+1)
	for i := 0; i <= m; i++ {
		u := float64(i) / float64(m)
		if u > stop {
			break
		}
		var th, r float64
		if chord {
			x, y := x0+(x1-x0)*u, y0+(y1-y0)*u
			th = math.Atan2(y-p.cy, x-p.cx)
			r = math.Hypot(x-p.cx, y-p.cy) + bulge*math.Sin(math.Pi*u)
		} else {
			th = t0 + span*u
			r = bootFootR(th, p.a, p.rc) + off + e0*(1-u) + e1*u + bulge*math.Sin(math.Pi*u)
		}
		if i > 0 && i < m {
			r += (rnd() - 0.5) * 0.22 * p.step
		}
		k := hw * (0.7 + 0.6*rnd())
		if stop < 1 {
			k *= 1 - u/stop
		}
		nodes = append(nodes, bootCrackNode{x: p.cx + math.Cos(th)*r, y: p.cy + math.Sin(th)*r, hp: k, hm: k})
	}
	if len(nodes) >= 2 && p.nodes+len(nodes) <= bootCrackMaxNodes {
		p.nodes += len(nodes)
		p.lines = append(p.lines, nodes)
	}
}

// ── The raster ───────────────────────────────────────────────────────────

// bootCrackMax raises buf[i] to v (0..1), never lowers it: where two cracks
// meet, the darker wins instead of the two darkening each other.
func bootCrackMax(buf []uint8, i int, v float64) {
	if v <= 0 {
		return
	}
	if q := uint8(math.Round(255 * math.Min(1, v))); q > buf[i] {
		buf[i] = q
	}
}

// raster draws the layer on a w*h canvas: how dark each pixel is made (the
// cracks, the crushed edge, the pieces tipped away from the light, the grit's
// shadows) and how much light it catches (the lips, the pieces tipped towards
// it, the grit), laid over each other as one straight-alpha picture: black
// where the ground is broken, white where it catches the light.
func (p *bootCrackPlan) raster(w, h int) *bootCrackImage {
	if w <= 0 || h <= 0 {
		return nil
	}
	dark := make([]uint8, w*h)
	light := make([]uint8, w*h)
	p.crushed(dark, w, h)
	for _, line := range p.lines {
		for i := 1; i < len(line); i++ {
			bootCrackSegment(dark, light, w, h, &line[i-1], &line[i])
		}
	}
	p.pieces(dark, light, w, h)
	for _, g := range p.pebbles {
		bootCrackGrain(dark, light, w, h, g[0], g[1], g[2])
	}
	pix := make([]byte, w*h*4)
	for i := range dark {
		if dark[i] == 0 && light[i] == 0 {
			continue
		}
		dv, lv := float64(dark[i])/255, float64(light[i])/255
		a := 1 - (1-dv)*(1-lv)
		c := uint8(math.Round(255 * lv / a))
		pix[4*i], pix[4*i+1], pix[4*i+2] = c, c, c
		pix[4*i+3] = uint8(math.Round(255 * a))
	}
	reach := 0.0
	for _, line := range p.lines {
		for i := range line {
			nd := &line[i]
			reach = math.Max(reach, math.Hypot(nd.x-p.cx, nd.y-p.cy)+math.Max(nd.hp, nd.hm)+2)
		}
	}
	return &bootCrackImage{pix: pix, w: w, h: h, reach: reach, start: 0.9 * bootFootR(0, p.a, p.rc)}
}

// crushed darkens the ring of ground the tile was driven into, fading out a
// tenth of its side past its edge.
func (p *bootCrackPlan) crushed(dark []uint8, w, h int) {
	zone := p.tile * 0.10
	r := int(math.Ceil(p.a*1.5 + zone + 2))
	for y := max(0, int(p.cy)-r); y < min(h, int(p.cy)+r+1); y++ {
		for x := max(0, int(p.cx)-r); x < min(w, int(p.cx)+r+1); x++ {
			sd := bootFootSD(float64(x)+0.5-p.cx, float64(y)+0.5-p.cy, p.a, p.rc)
			if sd > -1 && sd < zone {
				bootCrackMax(dark, y*w+x, 0.34*(1-bootSmoothstep(0, zone, sd)))
			}
		}
	}
}

// bootCrackSegment draws one piece of a crack, from a to b, as a groove lit
// from the top-left: its two lips as far from its middle as the points say,
// tapering between them, round at its ends so the pieces join; dark inside,
// darkest along the bottom, a soft shadow past its upper-left rim and a bright
// lip just past the other, as strong as the crack runs across the light.
func bootCrackSegment(dark, light []uint8, w, h int, a, b *bootCrackNode) {
	dx, dy := b.x-a.x, b.y-a.y
	ln := math.Hypot(dx, dy)
	if ln < 1e-3 {
		return
	}
	ux, uy := dx/ln, dy/ln
	nx, ny := -uy, ux
	// The light falls from the top-left: the wall on the bottom-right of a
	// crack faces it.
	lit := (nx + ny) * math.Sqrt2 / 2
	litK := math.Abs(lit)
	pad := math.Max(math.Max(a.hp, a.hm), math.Max(b.hp, b.hm)) + 2.5
	x0 := max(0, int(math.Floor(math.Min(a.x, b.x)-pad)))
	y0 := max(0, int(math.Floor(math.Min(a.y, b.y)-pad)))
	x1 := min(w-1, int(math.Ceil(math.Max(a.x, b.x)+pad)))
	y1 := min(h-1, int(math.Ceil(math.Max(a.y, b.y)+pad)))
	for y := y0; y <= y1; y++ {
		py := float64(y) + 0.5
		for x := x0; x <= x1; x++ {
			px := float64(x) + 0.5
			t := bootClamp01(((px-a.x)*ux + (py-a.y)*uy) / ln)
			ex, ey := px-(a.x+dx*t), py-(a.y+dy*t)
			dist := math.Sqrt(ex*ex + ey*ey)
			s := ex*nx + ey*ny
			hw := a.hm + (b.hm-a.hm)*t
			if s >= 0 {
				hw = a.hp + (b.hp-a.hp)*t
			}
			if dist > hw+2.5 {
				continue
			}
			litSide := s*lit > 0
			cov := bootClamp01(hw+0.5-dist) * math.Min(1, 2*hw)
			// A V-shaped groove: darkest along its bottom.
			deep := 0.72 + 0.28*bootClamp01(1-dist/math.Max(hw, 0.5))
			d := 0.9 * cov * deep
			l := 0.0
			if litSide {
				d *= 0.82
				l = 0.30 * litK * bootClamp01(1-math.Abs(dist-(hw+0.35))/0.85) * math.Min(1, hw/0.8)
			} else if dist > hw {
				d = math.Max(d, 0.28*litK*bootClamp01(1-(dist-hw)/1.5)*math.Min(1, hw))
			}
			i := y*w + x
			bootCrackMax(dark, i, d)
			bootCrackMax(light, i, l)
		}
	}
}

// pieces tips the bits of slab the cracks cut right off round the tile: each
// patch of ground between the tile's edge and the far ring that the cracks
// close in on every side is shaded a little lighter or darker as one piece.
// A patch that reaches the open ground past the rings is still the slab and
// stays as it is.
func (p *bootCrackPlan) pieces(dark, light []uint8, w, h int) {
	reach := p.ringOff + p.tile*0.12
	r := int(math.Ceil(p.a*1.5 + reach + 2))
	x0, x1 := max(0, int(p.cx)-r), min(w-1, int(p.cx)+r)
	y0, y1 := max(0, int(p.cy)-r), min(h-1, int(p.cy)+r)
	bw, bh := x1-x0+1, y1-y0+1
	if bw <= 0 || bh <= 0 {
		return
	}
	// Each pixel of the box: -1 under the tile, 1 in the zone round it, 0
	// past it.
	zone := make([]int8, bw*bh)
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			sd := bootFootSD(float64(x0+x)+0.5-p.cx, float64(y0+y)+0.5-p.cy, p.a, p.rc)
			switch {
			case sd <= 0:
				zone[y*bw+x] = -1
			case sd < reach:
				zone[y*bw+x] = 1
			}
		}
	}
	crack := func(x, y int) bool { return dark[(y0+y)*w+x0+x] > bootCrackWall }
	label := make([]bool, bw*bh)
	var stack, patch []int32
	for sy := 0; sy < bh; sy++ {
		for sx := 0; sx < bw; sx++ {
			if zone[sy*bw+sx] != 1 || crack(sx, sy) || label[sy*bw+sx] {
				continue
			}
			label[sy*bw+sx] = true
			stack = append(stack[:0], int32(sy*bw+sx))
			patch = patch[:0]
			open := false
			for len(stack) > 0 {
				k := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				patch = append(patch, k)
				x, y := int(k)%bw, int(k)/bw
				for _, nb := range [4][2]int{{x - 1, y}, {x + 1, y}, {x, y - 1}, {x, y + 1}} {
					ax, ay := nb[0], nb[1]
					if ax < 0 || ay < 0 || ax >= bw || ay >= bh {
						open = true
						continue
					}
					j := ay*bw + ax
					if zone[j] != 1 {
						// Past the zone without crossing a crack: the
						// patch is still part of the slab.
						if zone[j] == 0 && !crack(ax, ay) {
							open = true
						}
						continue
					}
					if label[j] || crack(ax, ay) {
						continue
					}
					label[j] = true
					stack = append(stack, int32(j))
				}
			}
			shade := 0.05 + 0.08*p.rand()
			if p.rand() < 0.55 {
				shade = -shade
			}
			if open || len(patch) < 12 {
				continue
			}
			for _, k := range patch {
				i := (y0+int(k)/bw)*w + x0 + int(k)%bw
				if shade < 0 {
					bootCrackMax(dark, i, -shade)
				} else {
					bootCrackMax(light, i, shade)
				}
			}
		}
	}
}

// bootCrackGrain draws one grain of grit of radius r at (x, y): its shadow
// down and to the right, then the stone over it, catching a little light.
func bootCrackGrain(dark, light []uint8, w, h int, x, y, r float64) {
	for py := int(y - r - 3); py < int(y+r+4); py++ {
		for px := int(x - r - 3); px < int(x+r+4); px++ {
			if px < 0 || py < 0 || px >= w || py >= h {
				continue
			}
			ds := math.Hypot(float64(px)+0.5-(x+0.8*r+0.6), float64(py)+0.5-(y+0.8*r+0.6))
			sh := bootClamp01(r+0.5-ds) * 0.5
			cov := bootClamp01(r + 0.5 - math.Hypot(float64(px)+0.5-x, float64(py)+0.5-y))
			i := py*w + px
			dv := float64(dark[i]) / 255
			dark[i] = uint8(math.Round(255 * math.Max(dv*(1-cov), sh*(1-cov))))
			bootCrackMax(light, i, cov*0.2)
		}
	}
}

// ── On screen ────────────────────────────────────────────────────────────

// crackStart draws the crack layer off the render thread, from seed; the
// render thread takes it when it is done (crackUpdate). A panic in the worker
// costs the boot its cracks, nothing else.
func (s *bootStyleRun) crackStart(seed uint32) {
	w, h := int(s.w), int(s.h)
	if w <= 0 || h <= 0 || s.tile <= 0 {
		return
	}
	ready := make(chan *bootCrackImage, 1)
	s.crack = bootCrackLayer{ready: ready}
	cx, cy, tile := s.cx, s.cy, s.tile
	go func() {
		var img *bootCrackImage
		defer func() {
			if rec := recover(); rec != nil {
				logWarn("startup", "boot cracks could not be drawn: %v", rec)
				img = nil
			}
			ready <- img
		}()
		img = bootCrackGrow(float64(w), float64(h), cx, cy, tile, seed).raster(w, h)
	}()
}

// crackUpdate takes the crack layer from its worker once it is done and
// uploads it a band a frame (bootCrackBands), before anything is drawn.
func (r *bootOSRun) crackUpdate() {
	c := &r.style.crack
	if c.ready != nil {
		select {
		case img := <-c.ready:
			c.ready, c.img = nil, img
		default:
			return
		}
	}
	img := c.img
	if img == nil || c.rows >= img.h {
		return
	}
	if c.tex == nil {
		tex, err := r.renderer.CreateTexture(sdl.PIXELFORMAT_ABGR8888, sdl.TEXTUREACCESS_STATIC, int32(img.w), int32(img.h))
		if err != nil || tex == nil {
			logWarn("startup", "boot cracks have no texture: %v", err)
			c.img = nil
			return
		}
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
		c.tex = tex
	}
	band := (img.h + bootCrackBands - 1) / bootCrackBands
	end := min(img.h, c.rows+band)
	rect := sdl.Rect{X: 0, Y: int32(c.rows), W: int32(img.w), H: int32(end - c.rows)}
	if err := c.tex.Update(&rect, unsafe.Pointer(&img.pix[c.rows*img.w*4]), img.w*4); err != nil {
		logWarn("startup", "boot cracks could not be uploaded: %v", err)
		_ = c.tex.Destroy()
		c.tex, c.img = nil, nil
		return
	}
	c.rows = end
	if c.rows >= img.h {
		// The texture holds it now.
		img.pix = nil
	}
}

// release destroys the layer's texture; a worker still drawing hands its
// picture to no one.
func (c *bootCrackLayer) release() {
	if c.tex != nil {
		_ = c.tex.Destroy()
	}
	*c = bootCrackLayer{}
}

// bootCrackSoft is how wide the reveal's soft edge is.
func bootCrackSoft(reach float64) float64 { return math.Max(10, 0.05*reach) }

// bootCrackFront is how far from the tile's centre the cracks have run t ms
// after the impact: from start (the tile's edge) to past reach, fast at first
// and slowing to a stop at bootCrackGrowMS, the soft edge clear of the
// farthest crack by then.
func bootCrackFront(t, start, reach float64) float64 {
	u := 1.0
	if t < bootCrackGrowMS {
		q := 1 - math.Max(0, t)/bootCrackGrowMS
		u = 1 - q*q
	}
	return start + (reach+bootCrackSoft(reach)-start)*u
}

// drawCracks paints the crack layer at presence p, shaken by (shx, shy):
// revealed outwards from under the tile until bootCrackGrowMS after the
// impact, whole after. On a light theme it is a little fainter.
func (r *bootOSRun) drawCracks(ms, p, shx, shy float64, light bool) {
	s := &r.style
	c := &s.crack
	t := ms - s.impact
	img := c.img
	if t < 0 || p <= 0 || c.tex == nil || img == nil || c.rows < img.h {
		return
	}
	a := 255 * p
	if light {
		a *= 0.85
	}
	alpha := uint8(math.Round(math.Min(255, a)))
	bootOSF = sdl.FRect{X: float32(shx), Y: float32(shy), W: float32(img.w), H: float32(img.h)}
	if t >= bootCrackGrowMS {
		_ = c.tex.SetAlphaMod(alpha)
		_ = r.renderer.CopyF(c.tex, nil, &bootOSF)
		_ = c.tex.SetAlphaMod(255)
		return
	}
	front := bootCrackFront(t, img.start, img.reach)
	// Where the shaken layer lies on screen.
	screen := sdl.Rect{W: int32(img.w), H: int32(img.h)}
	shaken := sdl.Rect{X: int32(math.Round(shx)), Y: int32(math.Round(shy)), W: int32(img.w), H: int32(img.h)}
	layer, ok := shaken.Intersect(&screen)
	if !ok {
		return
	}
	if !c.noGeom {
		// The disc: full inside, fading out over its soft edge. Its corners
		// past the layer would smear the layer's edge texels: the clip keeps
		// it to the layer.
		v := c.fan(s.cx, s.cy, shx, shy, front, bootCrackSoft(img.reach), alpha, float64(img.w), float64(img.h))
		_ = r.renderer.SetClipRect(&layer)
		err := r.renderer.RenderGeometry(c.tex, v, bootCrackFanIdx)
		_ = r.renderer.SetClipRect(nil)
		if err == nil {
			return
		}
		c.noGeom = true
		logWarn("startup", "RenderGeometry is unavailable (%v): the boot's cracks grow in a square", err)
	}
	// No geometry: a square window round the tile grows with the front.
	f := int32(math.Round(front))
	win := sdl.Rect{X: int32(math.Round(s.cx+shx)) - f, Y: int32(math.Round(s.cy+shy)) - f, W: 2 * f, H: 2 * f}
	clip, ok := win.Intersect(&layer)
	if !ok {
		return
	}
	_ = r.renderer.SetClipRect(&clip)
	_ = c.tex.SetAlphaMod(alpha)
	_ = r.renderer.CopyF(c.tex, nil, &bootOSF)
	_ = c.tex.SetAlphaMod(255)
	_ = r.renderer.SetClipRect(nil)
}

// fan is the reveal's vertices for a front front from (cx, cy), shaken by
// (shx, shy): the centre and the inner ring at alpha, the outer ring at
// nothing, soft apart, reading the w*h layer where it lies unshaken.
func (c *bootCrackLayer) fan(cx, cy, shx, shy, front, soft float64, alpha uint8, w, h float64) []sdl.Vertex {
	n := bootCrackFanSides
	if cap(c.verts) < 2*n+1 {
		c.verts = make([]sdl.Vertex, 2*n+1)
	}
	v := c.verts[:2*n+1]
	full := sdl.Color{R: 255, G: 255, B: 255, A: alpha}
	none := sdl.Color{R: 255, G: 255, B: 255, A: 0}
	put := func(i int, x, y float64, col sdl.Color) {
		v[i] = sdl.Vertex{
			Position: sdl.FPoint{X: float32(x + shx), Y: float32(y + shy)},
			Color:    col,
			TexCoord: sdl.FPoint{X: float32(x / w), Y: float32(y / h)},
		}
	}
	inner := math.Max(0, front-soft)
	put(0, cx, cy, full)
	for i := 0; i < n; i++ {
		ang := 2 * math.Pi * float64(i) / float64(n)
		ca, sa := math.Cos(ang), math.Sin(ang)
		put(1+i, cx+ca*inner, cy+sa*inner, full)
		put(1+n+i, cx+ca*front, cy+sa*front, none)
	}
	return v
}
