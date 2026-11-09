package main

import (
	"math"
	"math/rand"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Easter egg: type J-U-K-A on the home screen for fireworks
// ──────────────────────────────────────────────────────────────────────

const (
	fireworkBurstCount  = 80   // particles per explosion
	fireworkTrailLen    = 6    // trailing ghost particles per burst
	fireworkLifetime    = 2500 // ms before all particles die
	fireworkLaunchCount = 3    // number of simultaneous bursts
	jukaSequence        = "juka"
)

type fwParticle struct {
	x, y    float64
	vx, vy  float64
	life    float64 // 1.0 → 0.0
	decay   float64
	size    float64
	r, g, b uint8
	gravity float64
}

type fireworkState struct {
	sequence string // accumulated key presses
	active   bool
	start    time.Time
	bursts   [][]fwParticle // one burst = one explosion
}

var fw fireworkState

// onHomeKeyPress is called from the home screen event handler for every
// key-down. It feeds the J-U-K-A sequence and triggers fireworks on match.
func onHomeKeyPress(key string) {
	if fw.active {
		return // already showing
	}
	fw.sequence += key
	if len(fw.sequence) > len(jukaSequence) {
		fw.sequence = fw.sequence[len(fw.sequence)-len(jukaSequence):]
	}
	if fw.sequence == jukaSequence {
		triggerFireworks()
		fw.sequence = ""
	}
}

// triggerFireworks spawns multiple firework bursts from random screen positions.
func triggerFireworks() {
	fw.active = true
	fw.start = time.Now()
	fw.bursts = fw.bursts[:0]

	for b := 0; b < fireworkLaunchCount; b++ {
		// Each burst launches from a slightly different position.
		lx := float64(screenWidth)*0.2 + rand.Float64()*float64(screenWidth)*0.6
		ly := float64(screenHeight)*0.2 + rand.Float64()*float64(screenHeight)*0.3

		// Random color palette for this burst.
		palette := [][]uint8{
			{255, 100, 100}, // red
			{100, 255, 100}, // green
			{100, 150, 255}, // blue
			{255, 200, 60},  // gold
			{200, 100, 255}, // purple
			{100, 255, 230}, // cyan
			{255, 160, 60},  // orange
		}
		baseCol := palette[rand.Intn(len(palette))]

		burst := make([]fwParticle, fireworkBurstCount)
		for i := range burst {
			// Random direction with spherical-ish distribution.
			angle := rand.Float64() * math.Pi * 2
			speed := 60 + rand.Float64()*180
			// Add some vertical bias (fireworks go up).
			vyBias := -40 - rand.Float64()*60

			p := &burst[i]
			p.x = lx
			p.y = ly
			p.vx = math.Cos(angle) * speed
			p.vy = math.Sin(angle)*speed + vyBias
			p.life = 0.8 + rand.Float64()*0.2
			p.decay = 0.3 + rand.Float64()*0.4
			p.size = 2 + rand.Float64()*3
			p.gravity = 80 + rand.Float64()*40

			// Tint base color with random variation.
			variation := 20
			p.r = clampByte(int(baseCol[0]) + rand.Intn(variation*2) - variation)
			p.g = clampByte(int(baseCol[1]) + rand.Intn(variation*2) - variation)
			p.b = clampByte(int(baseCol[2]) + rand.Intn(variation*2) - variation)
		}
		fw.bursts = append(fw.bursts, burst)
	}
}

// clampByte clamps an int to [0, 255].
func clampByte(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// renderFireworks draws and advances all firework particles. Returns true
// while any particle is still alive.
func renderFireworks(renderer *sdl.Renderer, config *Config, dt float64) bool {
	if !fw.active {
		return false
	}
	elapsed := time.Since(fw.start)
	if elapsed.Milliseconds() > int64(fireworkLifetime) {
		fw.active = false
		fw.bursts = fw.bursts[:0]
		return false
	}

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	anyAlive := false
	for bi := range fw.bursts {
		burst := fw.bursts[bi]
		alive := false
		for i := range burst {
			p := &burst[i]
			if p.life <= 0 {
				continue
			}
			alive = true

			// Physics.
			p.vy += p.gravity * dt
			p.x += p.vx * dt
			p.y += p.vy * dt
			p.life -= p.decay * dt

			if p.life <= 0 {
				continue
			}

			// Draw trail ghost (3 trailing positions).
			alpha := uint8(255 * p.life)
			for t := fireworkTrailLen; t >= 0; t-- {
				trailFrac := float64(t) * 0.015
				tx := p.x - p.vx*trailFrac
				ty := p.y - p.vy*trailFrac
				trailAlpha := alpha
				if t > 0 {
					trailAlpha = uint8(float64(alpha) * (1.0 - float64(t)/float64(fireworkTrailLen+1)))
				}
				if trailAlpha == 0 {
					continue
				}
				drawParticle(renderer, int32(tx), int32(ty),
					int32(p.size*float64(t+1)/float64(fireworkTrailLen+1)),
					p.r, p.g, p.b, trailAlpha)
			}

			// Bright core.
			coreAlpha := uint8(float64(alpha) * 0.8)
			if coreAlpha > 20 {
				drawParticle(renderer, int32(p.x), int32(p.y),
					int32(p.size*0.6),
					255, 255, 255, coreAlpha)
			}
		}
		fw.bursts[bi] = burst
		if alive {
			anyAlive = true
		}
	}

	if !anyAlive {
		fw.active = false
		fw.bursts = fw.bursts[:0]
	}
	return anyAlive
}

// ──────────────────────────────────────────────────────────────────────
// Trophy confetti
//
// The celebration's confetti (trophy_celebrate.go) is a fixed pool of paper
// pieces: a burst fills free slots, nothing is allocated while they fly, and a
// full pool simply spawns fewer. Each piece is a small rectangle that spins in
// the screen plane and tumbles about its long axis — the tumble squashes its
// apparent width and shows the darker back of the paper — under gravity, air
// drag that brings it down to a flutter, and a side-to-side sway.
//
// Drawing is batched: every piece is rasterised into one-pixel-tall rows that
// go into a bucket per (colour, face, alpha step), and each bucket is a single
// FillRects call. 150 pieces cost a few dozen draw calls, not 150 textured
// copies, which matters on the Smart Pro's GLES2 path.
// ──────────────────────────────────────────────────────────────────────

const (
	// trophyConfettiMax is the most pieces alive at once: the budget the Smart
	// Pro holds at its 30 fps cap with a scene underneath.
	trophyConfettiMax = 150
	// trophyConfettiColors is the palette size of one burst.
	trophyConfettiColors = 6
	// trophyConfettiShades is the front and the back of the paper.
	trophyConfettiShades = 2
	// trophyConfettiAlphas is how many fade steps a piece goes through as it
	// dies; few, because each one is a bucket.
	trophyConfettiAlphas = 3
	// trophyConfettiBuckets is one bucket per colour, face and fade step.
	trophyConfettiBuckets = trophyConfettiColors * trophyConfettiShades * trophyConfettiAlphas
	// trophyConfettiBucketCap is a bucket's room; a full bucket is drawn and
	// emptied mid-frame instead of growing.
	trophyConfettiBucketCap = 256
	// trophyConfettiFade is how long a piece takes to fade out at the end of
	// its life, in seconds.
	trophyConfettiFade = float32(0.6)
)

// trophyConfettiStepAlpha is the alpha of each fade step.
var trophyConfettiStepAlpha = [trophyConfettiAlphas]uint8{255, 170, 85}

// trophyConfettiPiece is one piece of paper. Positions are in screen pixels,
// velocities in pixels per second, angles in radians.
type trophyConfettiPiece struct {
	x, y, vx, vy float32
	angle, spin  float32 // rotation in the screen plane
	flip, flipV  float32 // tumble about the long axis
	sway, swayV  float32 // side-to-side drift
	w, h         float32
	age, ttl     float32
	color        uint8
	alive        bool
}

// trophyConfettiSystem is the pool and its draw buckets.
type trophyConfettiSystem struct {
	pieces  [trophyConfettiMax]trophyConfettiPiece
	live    int
	palette [trophyConfettiColors][trophyConfettiShades]sdl.Color
	seed    uint32
	buckets [trophyConfettiBuckets][]sdl.Rect
}

var trophyConfetti trophyConfettiSystem

// trophyConfettiRand is a xorshift step: deterministic for a seed (the tests
// replay a burst) and free of the global source's lock and allocations.
func (s *trophyConfettiSystem) rand() float32 {
	x := s.seed
	if x == 0 {
		x = 0x9E3779B9
	}
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	s.seed = x
	return float32(x>>8) / float32(1<<24)
}

// between is a uniform value in [lo, hi).
func (s *trophyConfettiSystem) between(lo, hi float32) float32 {
	return lo + (hi-lo)*s.rand()
}

// trophyConfettiPalette is a tier's paper: its own metal, the metal's
// highlight, two festive colours that suit it, white, and the theme accent.
func trophyConfettiPalette(t trophyTier) [trophyConfettiColors]sdl.Color {
	m := trophyMetalOf(t)
	accent := glassAccent()
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	switch t {
	case trophySilver:
		return [trophyConfettiColors]sdl.Color{m.base, m.light, {R: 140, G: 190, B: 255, A: 255}, white, accent, {R: 196, G: 160, B: 255, A: 255}}
	case trophyGold:
		return [trophyConfettiColors]sdl.Color{m.base, m.light, {R: 255, G: 166, B: 56, A: 255}, white, accent, {R: 255, G: 104, B: 140, A: 255}}
	case trophyPlatinum:
		return [trophyConfettiColors]sdl.Color{m.base, m.light, {R: 188, G: 160, B: 255, A: 255}, white, {R: 255, G: 214, B: 90, A: 255}, accent}
	}
	return [trophyConfettiColors]sdl.Color{m.base, m.light, {R: 255, G: 140, B: 96, A: 255}, white, accent, {R: 255, G: 212, B: 102, A: 255}}
}

// setPalette installs a tier's palette, with a darker back face for each
// colour. Pieces still in the air keep their colour index, so a second burst
// recolours the first one's leftovers — harmless, and it keeps one palette.
func (s *trophyConfettiSystem) setPalette(t trophyTier) {
	front := trophyConfettiPalette(t)
	black := sdl.Color{R: 0, G: 0, B: 0, A: 255}
	for i, c := range front {
		c.A = 255
		s.palette[i][0] = c
		s.palette[i][1] = trophyMix(c, black, 0.32)
	}
}

// ensureBuckets gives every bucket its fixed room once.
func (s *trophyConfettiSystem) ensureBuckets() {
	for i := range s.buckets {
		if cap(s.buckets[i]) == 0 {
			s.buckets[i] = make([]sdl.Rect, 0, trophyConfettiBucketCap)
		}
	}
}

// spawn fills one free slot. It reports false when the pool is full.
func (s *trophyConfettiSystem) spawn(x, y, vx, vy float32) bool {
	if s.live >= trophyConfettiMax {
		return false
	}
	for i := range s.pieces {
		p := &s.pieces[i]
		if p.alive {
			continue
		}
		scale := float32(screenWidth) / 1280
		if scale < 0.6 {
			scale = 0.6
		}
		*p = trophyConfettiPiece{
			x: x, y: y, vx: vx, vy: vy,
			angle: s.between(0, 2*math.Pi),
			spin:  s.between(-9, 9),
			flip:  s.between(0, 2*math.Pi),
			flipV: s.between(5, 14),
			sway:  s.between(0, 2*math.Pi),
			swayV: s.between(2.5, 5),
			w:     s.between(7, 12) * scale,
			h:     s.between(3.5, 6) * scale,
			ttl:   s.between(2.2, 3.4),
			color: uint8(s.rand() * trophyConfettiColors),
			alive: true,
		}
		if p.color >= trophyConfettiColors {
			p.color = trophyConfettiColors - 1
		}
		s.live++
		return true
	}
	return false
}

// trophyConfettiBurst throws n pieces out of (x, y) — the medallion of the
// trophy card in the top-right corner — in a fan towards the screen's middle:
// left, up-left and a little down, fast at first and then fluttering down.
func trophyConfettiBurst(x, y float32, n int, tier trophyTier) {
	s := &trophyConfetti
	s.ensureBuckets()
	s.setPalette(tier)
	scale := float32(screenWidth) / 1280
	if scale < 0.6 {
		scale = 0.6
	}
	for i := 0; i < n; i++ {
		// Screen angles, y down: 180° is left, 270° is up.
		angle := s.between(150, 262) * math.Pi / 180
		speed := s.between(260, 620) * scale
		vx := speed * float32(math.Cos(float64(angle)))
		vy := speed * float32(math.Sin(float64(angle)))
		if !s.spawn(x+s.between(-10, 10), y+s.between(-10, 10), vx, vy) {
			return
		}
	}
}

// trophyConfettiRain drops n pieces from above the top edge across the whole
// width: the platinum's second wave.
func trophyConfettiRain(n int, tier trophyTier) {
	s := &trophyConfetti
	s.ensureBuckets()
	s.setPalette(tier)
	w := float32(screenWidth)
	for i := 0; i < n; i++ {
		x := s.between(0, w)
		y := -s.between(8, 90)
		if !s.spawn(x, y, s.between(-70, 70), s.between(30, 150)) {
			return
		}
	}
}

// trophyConfettiAlive is how many pieces are still in the air.
func trophyConfettiAlive() int {
	return trophyConfetti.live
}

// trophyConfettiUpdate advances the pool by dt seconds. It allocates nothing.
func trophyConfettiUpdate(dt float32) {
	s := &trophyConfetti
	if s.live == 0 || dt <= 0 {
		return
	}
	if dt > 0.05 {
		// A stalled frame must not teleport the paper across the screen.
		dt = 0.05
	}
	const (
		gravity  = float32(980)
		terminal = float32(185) // the flutter: paper falls slowly once the burst is spent
		swayAmp  = float32(30)
	)
	drag := float32(math.Exp(float64(-2.3 * dt)))
	bottom := float32(screenHeight) + 24
	right := float32(screenWidth) + 40
	for i := range s.pieces {
		p := &s.pieces[i]
		if !p.alive {
			continue
		}
		p.age += dt
		if p.age >= p.ttl || p.y > bottom || p.x < -40 || p.x > right {
			p.alive = false
			s.live--
			continue
		}
		p.vx *= drag
		p.vy = p.vy*drag + gravity*dt
		if p.vy > terminal {
			p.vy = terminal
		}
		p.sway += p.swayV * dt
		p.x += (p.vx + swayAmp*float32(math.Sin(float64(p.sway)))) * dt
		p.y += p.vy * dt
		p.angle += p.spin * dt
		p.flip += p.flipV * dt
	}
	if s.live < 0 {
		s.live = 0
	}
}

// trophyConfettiDraw rasterises every live piece into its bucket and draws the
// buckets. It allocates nothing: the buckets have fixed room.
func trophyConfettiDraw(renderer *sdl.Renderer) {
	s := &trophyConfetti
	if renderer == nil || s.live == 0 {
		return
	}
	s.ensureBuckets()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for i := range s.pieces {
		p := &s.pieces[i]
		if !p.alive {
			continue
		}
		step := 0
		if left := p.ttl - p.age; left < trophyConfettiFade {
			switch {
			case left < trophyConfettiFade/3:
				step = 2
			case left < trophyConfettiFade*2/3:
				step = 1
			}
		}
		tumble := float32(math.Cos(float64(p.flip)))
		face := 0
		if tumble < 0 {
			face = 1
			tumble = -tumble
		}
		bucket := (int(p.color)*trophyConfettiShades+face)*trophyConfettiAlphas + step
		s.rasterPiece(renderer, p, tumble, bucket)
	}
	for b := range s.buckets {
		s.flush(renderer, b)
	}
}

// rasterPiece turns one rotated rectangle into rows in a bucket. The piece is
// convex, so each row is the span between its edges' crossings.
func (s *trophyConfettiSystem) rasterPiece(renderer *sdl.Renderer, p *trophyConfettiPiece, tumble float32, bucket int) {
	hw := p.w / 2
	hh := p.h / 2 * tumble
	if hh < 0.6 {
		hh = 0.6 // edge-on paper is still a sliver, not nothing
	}
	ca := float32(math.Cos(float64(p.angle)))
	sa := float32(math.Sin(float64(p.angle)))
	ux, uy := ca*hw, sa*hw
	vx, vy := -sa*hh, ca*hh
	xs := [4]float32{p.x - ux - vx, p.x + ux - vx, p.x + ux + vx, p.x - ux + vx}
	ys := [4]float32{p.y - uy - vy, p.y + uy - vy, p.y + uy + vy, p.y - uy + vy}
	minY, maxY := ys[0], ys[0]
	for k := 1; k < 4; k++ {
		if ys[k] < minY {
			minY = ys[k]
		}
		if ys[k] > maxY {
			maxY = ys[k]
		}
	}
	y0 := int32(math.Floor(float64(minY)))
	y1 := int32(math.Ceil(float64(maxY)))
	if y1 <= y0 {
		y1 = y0 + 1
	}
	for row := y0; row < y1; row++ {
		yc := float32(row) + 0.5
		left, right := float32(math.MaxFloat32), float32(-math.MaxFloat32)
		for k := 0; k < 4; k++ {
			ax, ay := xs[k], ys[k]
			bx, by := xs[(k+1)%4], ys[(k+1)%4]
			if (ay <= yc && by > yc) || (by <= yc && ay > yc) {
				x := ax + (yc-ay)*(bx-ax)/(by-ay)
				if x < left {
					left = x
				}
				if x > right {
					right = x
				}
			}
		}
		if right < left {
			// A row through a vertex only: one pixel at the piece's centre line.
			left, right = p.x, p.x
		}
		x0 := int32(math.Floor(float64(left)))
		w := int32(math.Ceil(float64(right))) - x0
		if w < 1 {
			w = 1
		}
		if len(s.buckets[bucket]) == cap(s.buckets[bucket]) {
			s.flush(renderer, bucket)
		}
		s.buckets[bucket] = append(s.buckets[bucket], sdl.Rect{X: x0, Y: row, W: w, H: 1})
	}
}

// flush draws and empties one bucket.
func (s *trophyConfettiSystem) flush(renderer *sdl.Renderer, bucket int) {
	rects := s.buckets[bucket]
	if len(rects) == 0 {
		return
	}
	perColor := trophyConfettiShades * trophyConfettiAlphas
	ci := bucket / perColor
	face := (bucket % perColor) / trophyConfettiAlphas
	step := bucket % trophyConfettiAlphas
	c := s.palette[ci][face]
	_ = renderer.SetDrawColor(c.R, c.G, c.B, trophyConfettiStepAlpha[step])
	_ = renderer.FillRects(rects)
	s.buckets[bucket] = rects[:0]
}
