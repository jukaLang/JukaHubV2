package main

// bootstyles.go — the boot animation's styles (Settings → General → Boot
// animation, Custom "bootStyle").
//
// bootos.go decides WHEN the full boot plays (startupAnimation) and runs it;
// this file decides WHAT it looks like:
//
//   - cinematic (the default): the camera sits a little above a dark floor
//     with a faint horizon glow in the theme's accent. The glass logo drops in
//     from above the top edge, stretching with its speed, and crashes onto the
//     floor: the motor gives one hard buzz on the exact frame of the impact,
//     the picture shakes and flashes, dust and sparks burst sideways along the
//     floor and cracks race out from the point of impact, glowing in the
//     accent and cooling to dark. The logo squashes, bounces twice (each
//     landing a weaker buzz and a quieter knock in the chime) and settles.
//     Then the wordmark, the theme's colours and the progress line come up as
//     in the classic boot, and the first screen arrives.
//   - roll: the rounded tile tumbles in from the left, rolling on its rounded
//     corners (the corner's centre travels along the floor as the tile turns,
//     so the roll is smooth, never a jump), lands on each side with a small
//     squash and a light tick, slows step by step and stops upright in the
//     middle, where the firmware's boot logo sits.
//   - image: the logo still and upright where the firmware's boot logo is, so
//     a power-on shows no jump; the wordmark, the progress line, no motion.
//   - classic: the glass light running round the pane (bootos.go), unchanged.
//   - random: cinematic, roll or classic, a different one each boot.
//   - none: no boot animation and no quick hello at all (bootOSPlanStartup):
//     the queued startup work runs and the first screen appears as soon as it
//     can, with no arrival. The fastest start.
//
// Reduced motion and Battery Saver (bootOSMotion) turn cinematic and roll into
// the still image: no shake, no particles, no flash. The quick hello of an app
// launch plays a compressed version of the style: a short drop with one small
// bounce (no floor, cracks or dust: building them would cost the launch the
// time the hello is meant to hide), or the last quarter-turn of the roll.
//
// Everything is drawn from textures made once before the first frame: the
// floor's fog (a small texture scaled up), the soft dot bootos.go already has
// (shadow, sparks, dust, glow), and the cracks, which are grown into a render
// target segment by segment as their time comes, so a frame only draws the new
// segments and copies the layer twice (its cool lines, then its glow). Where
// the driver refuses the target the visible segments are drawn as lines each
// frame instead. A panic inside a style falls back to the classic boot for
// the rest of the boot (styleGuard): the start never fails over a picture.

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// bootStyleKey is the Custom setting behind Settings → General → Boot
// animation.
const bootStyleKey = "bootStyle"

// Its values.
const (
	bootStyleCinematic = "cinematic"
	bootStyleRoll      = "roll"
	bootStyleImage     = "image"
	bootStyleClassic   = "classic"
	bootStyleRandom    = "random"
	bootStyleNone      = "none"
)

const (
	// bootStyleTailMS is how long the classic beats that follow the logo's
	// entrance (the wordmark, the dawn, the blooms, the progress line) take
	// before the hold may end, counted from the style's lead.
	bootStyleTailMS = 2300.0
	// bootStyleImageMinMS is the still image's shortest power-on hold: its
	// wordmark and progress line have played by then.
	bootStyleImageMinMS = 2000.0
	// bootStylePreviewAllotMS bounds the preview's hold: longer than the
	// longest style (a roll on a wide screen) and its simulated queue.
	bootStylePreviewAllotMS = 5600
	// bootStyleChimeMaxDelay caps how far a style pushes the chime's score
	// back, in seconds (shutdownChimeWait's bound counts on it).
	bootStyleChimeMaxDelay = 2.5
	// bootDropLiftMS is how long the firmware's logo takes to leave through
	// the top edge before it drops, on a power-on that starts from it.
	bootDropLiftMS = 300.0
	// bootDropContactMS is how long a bounce's landing stays on the floor.
	bootDropContactMS = 45.0
	// bootCrackGrowMS is how long the cracks take to reach their full length.
	bootCrackGrowMS = 420.0
	// bootCrackMaxSegs bounds the crack network.
	bootCrackMaxSegs = 320
	// bootRollGhostMS fades the firmware's logo out as the roll comes in.
	bootRollGhostMS = 220.0
	// bootPulseLateMS drops a buzz whose frame came this late: after a stall
	// it would no longer match the picture.
	bootPulseLateMS = 120.0
)

// bootDropBeats times one drop: the fall from the top edge to the floor (ms),
// the speed-stretch it reaches there and the squash of the impact (fractions),
// how long the impact keeps the logo on the floor, each bounce's airtime (ms)
// and height (of the screen height), the shake (of the screen height), and
// whether the floor, cracks, dust and flash are drawn.
type bootDropBeats struct {
	fall    float64
	stretch float64
	squash  float64
	hold    float64
	air     [2]float64
	height  [2]float64
	shake   float64
	scene   bool
}

// bootDropPower is the cinematic power-on's drop.
var bootDropPower = bootDropBeats{
	fall:    560,
	stretch: 0.14,
	squash:  0.20,
	hold:    70,
	air:     [2]float64{230, 120},
	height:  [2]float64{0.05, 0.014},
	shake:   0.022,
	scene:   true,
}

// bootDropQuick is the quick hello's: a short drop and one small bounce.
var bootDropQuick = bootDropBeats{
	fall:    280,
	stretch: 0.08,
	squash:  0.12,
	hold:    50,
	air:     [2]float64{140, 0},
	height:  [2]float64{0.02, 0},
	shake:   0.006,
}

// The impact's buzz and its two aftershocks, and the quick hello's tick.
var (
	bootPulseImpact = hapticPulse{strength: 1.00, ms: 180}
	bootPulseAfter1 = hapticPulse{strength: 0.55, ms: 60}
	bootPulseAfter2 = hapticPulse{strength: 0.32, ms: 40}
	bootPulseTick   = hapticPulse{strength: 0.45, ms: 30}
	bootPulseRest   = hapticPulse{strength: 0.60, ms: 45}
)

// bootDropSeg is one part of the drop after the impact: on the floor (a
// contact, squashed at its start) or in the air (a hop of height pixels).
type bootDropSeg struct {
	from   float64
	to     float64
	squash float64
	height float64
}

// bootCrackSeg is one straight piece of a crack on the canvas, born birth ms
// after the impact; main pieces are drawn wider than the branches.
type bootCrackSeg struct {
	x0, y0, x1, y1 float32
	birth          float32
	main           bool
}

// bootSpark is one particle of the impact: a spark flying sideways and falling
// back to its ground line, or a puff of dust drifting out and up.
type bootSpark struct {
	x      float64
	vx     float64
	vy     float64
	ground float64
	life   float64
	size   float64
	tint   float64
	dust   bool
}

// bootStylePulse is a buzz due at ms of the boot.
type bootStylePulse struct {
	at    float64
	pulse hapticPulse
}

// bootStyleRun is the style one boot plays, resolved and laid out for its
// screen. lead is how long the classic beats wait for the logo's entrance and
// minMS the shortest hold (zero: the classic rule).
type bootStyleRun struct {
	kind  string
	quick bool
	lead  float64
	minMS float64

	// The screen, the logo's resting centre, its artwork square and the
	// visible tile inside it, the floor (the tile's resting bottom), the
	// horizon and the floor's focal length, in canvas pixels.
	w        float64
	h        float64
	cx       float64
	cy       float64
	side     float64
	tile     float64
	floorY   float64
	horizonY float64
	focal    float64

	// The drop: its beats, the firmware logo's lift, the impact, the parts
	// after it, when it has settled and how far above its rest it starts.
	drop   *bootDropBeats
	lift   float64
	impact float64
	segs   []bootDropSeg
	land   []float64
	settle float64
	up     float64

	// The roll: its steps' ends (ms), how far a step carries the tile, the
	// tile's centre before the first step, its corner radius, half its flat
	// side, and whether the firmware's logo fades out under it.
	stepAt  []float64
	stepD   float64
	rollX0  float64
	rollRad float64
	rollA   float64
	ghost   bool

	// Side effects: the buzzes and how many have fired, and the knocks added
	// to the chime.
	pulses []bootStylePulse
	fired  int
	thuds  []chimeNote

	// The cinematic floor: its fog, its grid, the cracks (grown into crackTex
	// from crackTop down, crackDone of them so far) and the particles.
	floor     *sdl.Texture
	grid      []float32
	cracks    []bootCrackSeg
	crackTex  *sdl.Texture
	crackTop  int32
	crackDone int
	sparks    []bootSpark
}

// animated reports whether the style draws its own logo layer.
func (s *bootStyleRun) animated() bool {
	return s.kind == bootStyleCinematic || s.kind == bootStyleRoll
}

// ── The setting ─────────────────────────────────────────────────────────

// bootStyleSetting is the stored boot animation, cinematic unless chosen.
func bootStyleSetting(c *Config) string {
	if c != nil {
		if v, ok := customString(c.Variables.Custom, bootStyleKey); ok {
			switch s := strings.ToLower(strings.TrimSpace(v)); s {
			case bootStyleCinematic, bootStyleRoll, bootStyleImage, bootStyleClassic, bootStyleRandom, bootStyleNone:
				return s
			}
		}
	}
	return bootStyleCinematic
}

// bootStyleOptions are the Settings row's stops.
func bootStyleOptions(*Config) []setOption {
	return []setOption{
		{Value: bootStyleCinematic, Label: "Cinematic", Note: "The logo crashes onto the floor, which cracks, and the device shakes"},
		{Value: bootStyleRoll, Label: "Roll", Note: "The logo rolls in on its rounded corners and stops in the middle"},
		{Value: bootStyleImage, Label: "Still logo", Note: "The logo upright and still, with the progress line"},
		{Value: bootStyleClassic, Label: "Classic", Note: "A light runs round the glass logo as the colours come up"},
		{Value: bootStyleRandom, Label: "Surprise me", Note: "Cinematic, Roll or Classic, a different one each time"},
		{Value: bootStyleNone, Label: "None", Note: "No animation: the fastest start, straight to Home"},
	}
}

// bootStyleCurrent is the stop the stored choice is.
func bootStyleCurrent(c *Config) int {
	cur := bootStyleSetting(c)
	for i, o := range bootStyleOptions(c) {
		if o.Value == cur {
			return i
		}
	}
	return 0
}

// bootStyleChoose stores a stop and says so.
func bootStyleChoose(c *Config, value string) {
	if c == nil {
		return
	}
	v := strings.ToLower(strings.TrimSpace(value))
	for _, o := range bootStyleOptions(c) {
		if o.Value == v {
			setStoreString(c, bootStyleKey, v)
			setToast("Boot animation: " + o.Label)
			return
		}
	}
}

// bootStyleDetail is the line under the row.
func bootStyleDetail(c *Config) string {
	cur := bootStyleSetting(c)
	for _, o := range bootStyleOptions(c) {
		if o.Value == cur {
			return o.Note
		}
	}
	return ""
}

// bootStyleOff reports the boot animation switched off, which leaves the
// Startup animation row nothing to choose.
func bootStyleOff(c *Config) bool { return bootStyleSetting(c) == bootStyleNone }

// bootStylePreviewDetail is the line under "Watch the boot animation".
func bootStylePreviewDetail(c *Config) string {
	if bootStyleSetting(c) == bootStyleNone {
		return "Boot animation is off"
	}
	return "The full boot JukaHub plays when the device starts"
}

// bootStyleResolve picks the style one boot plays: random draws one of the
// animated styles, reduced motion stills the moving ones, and none (which
// never reaches a boot: bootOSPlanStartup skips it) plays as the still image.
func bootStyleResolve(c *Config, motion, quick bool) bootStyleRun {
	kind := bootStyleSetting(c)
	switch kind {
	case bootStyleRandom:
		picks := [3]string{bootStyleCinematic, bootStyleRoll, bootStyleClassic}
		kind = picks[int(uint64(time.Now().UnixNano())/1000%3)]
	case bootStyleNone:
		kind = bootStyleImage
	}
	if !motion && (kind == bootStyleCinematic || kind == bootStyleRoll) {
		kind = bootStyleImage
	}
	return bootStyleRun{kind: kind, quick: quick}
}

// bootStyleHapticsReady reports whether the motor has been found and the
// Vibration preferences applied (hapticsBind). A power-on runs the boot queue,
// which binds it, before its first beat; a launch's hello may get there first,
// and detecting from here would find no pad on a desktop for the session.
func bootStyleHapticsReady() bool {
	hapticsMu.Lock()
	defer hapticsMu.Unlock()
	return hapticTried && hapticDev != nil
}

// bootStyleRand is a xorshift step mapped to [0, 1).
func bootStyleRand(s *uint32) float64 {
	*s ^= *s << 13
	*s ^= *s >> 17
	*s ^= *s << 5
	return float64(*s) / 4294967296.0
}

// ── Laying a style out ──────────────────────────────────────────────────

// stylePrepare lays the chosen style out for the boot's layout and builds
// its textures. It runs once, before the first frame.
func (r *bootOSRun) stylePrepare() {
	s := &r.style
	if !s.animated() {
		if s.kind == bootStyleImage && r.powerOn {
			s.minMS = bootStyleImageMinMS
		}
		return
	}
	sp := r.layout.Sprite
	if sp.W <= 0 || screenWidth <= 0 || screenHeight <= 0 {
		*s = bootStyleRun{kind: bootStyleClassic}
		return
	}
	s.w, s.h = float64(screenWidth), float64(screenHeight)
	s.side = float64(sp.W)
	s.tile = s.side * (1 - 2*bootLogoTileMargin)
	s.cx = float64(sp.X) + s.side/2
	s.cy = float64(sp.Y) + s.side/2
	s.floorY = s.cy + s.tile/2
	switch s.kind {
	case bootStyleCinematic:
		s.prepareDrop(r.continuity && !s.quick)
		if s.drop.scene {
			r.prepareScene(uint32(time.Now().UnixNano()) | 1)
		}
	case bootStyleRoll:
		s.prepareRoll(r.continuity && !s.quick)
	}
	sort.SliceStable(s.pulses, func(i, j int) bool { return s.pulses[i].at < s.pulses[j].at })
}

// prepareDrop times the drop: lift (from the firmware's logo), fall, impact,
// the contacts and hops after it, the buzzes and knocks on each landing.
func (s *bootStyleRun) prepareDrop(lift bool) {
	d := &bootDropPower
	if s.quick {
		d = &bootDropQuick
	}
	s.drop = d
	// The artwork's square starts wholly above the top edge.
	s.up = s.cy + s.side/2 + 4
	s.lift = 0
	if lift {
		s.lift = bootDropLiftMS
	}
	s.impact = s.lift + d.fall
	s.segs = s.segs[:0]
	s.land = s.land[:0]
	at, strength := s.impact, 1.0
	for i := 0; ; i++ {
		contact := d.hold
		if i > 0 {
			contact = bootDropContactMS
		}
		s.segs = append(s.segs, bootDropSeg{from: at, to: at + contact, squash: d.squash * strength})
		s.land = append(s.land, at)
		at += contact
		if i >= len(d.air) || d.air[i] <= 0 {
			break
		}
		s.segs = append(s.segs, bootDropSeg{from: at, to: at + d.air[i], height: d.height[i] * s.h})
		at += d.air[i]
		strength *= 0.4
	}
	s.settle = at
	if s.quick {
		s.pulses = append(s.pulses, bootStylePulse{at: s.impact, pulse: bootPulseTick})
		s.lead = math.Max(0, s.impact-60)
		s.minMS = s.settle
		return
	}
	pulses := [3]hapticPulse{bootPulseImpact, bootPulseAfter1, bootPulseAfter2}
	knocks := [3][3]float64{{190, 0.36, 0.7}, {180, 0.12, 0.4}, {175, 0.06, 0.3}}
	for i, when := range s.land {
		if i >= len(pulses) {
			break
		}
		s.pulses = append(s.pulses, bootStylePulse{at: when, pulse: pulses[i]})
		k := knocks[i]
		s.thuds = append(s.thuds, chimeThudNote(when/1000, k[0], k[1], k[2]))
	}
	s.lead = s.impact
	s.minMS = s.lead + bootStyleTailMS
}

// prepareRoll lays the roll out: enough quarter-turns to bring the tile in
// from beyond the left edge, each a little slower than the last.
func (s *bootStyleRun) prepareRoll(ghost bool) {
	s.ghost = ghost
	s.rollRad = s.tile * bootLogoTileCorner
	s.rollA = (s.tile - 2*s.rollRad) / 2
	// One quarter-turn rolls a flat side and a quarter of the corner's arc.
	s.stepD = 2*s.rollA + s.rollRad*math.Pi/2
	if s.stepD <= 0 {
		s.stepD = s.tile
	}
	steps, total := 1, 380.0
	if !s.quick {
		steps = int(math.Ceil((s.cx + 0.75*s.tile) / s.stepD))
		if steps < 2 {
			steps = 2
		}
		if steps > 9 {
			steps = 9
		}
		total = 1900
	}
	s.rollX0 = s.cx - float64(steps)*s.stepD
	const ratio = 1.13
	unit := total * (ratio - 1) / (math.Pow(ratio, float64(steps)) - 1)
	s.stepAt = make([]float64, steps)
	acc := 0.0
	for i := range s.stepAt {
		acc += unit * math.Pow(ratio, float64(i))
		s.stepAt[i] = acc
	}
	end := s.stepAt[steps-1]
	for i, at := range s.stepAt {
		// Only a landing on screen is felt and heard.
		if s.rollX0+float64(i+1)*s.stepD+s.tile/2 <= 0 {
			continue
		}
		last := i == steps-1
		p, level := bootPulseTick, 0.06
		if last {
			p, level = bootPulseRest, 0.09
		}
		s.pulses = append(s.pulses, bootStylePulse{at: at, pulse: p})
		if !s.quick {
			s.thuds = append(s.thuds, chimeThudNote(at/1000, 420, level, 0.3))
		}
	}
	if s.quick {
		s.lead = math.Max(0, end-150)
		s.minMS = end + 120
		return
	}
	s.lead = end - 100
	s.minMS = s.lead + bootStyleTailMS
}

// prepareScene builds the cinematic floor: its fog texture, its perspective
// grid, the crack network and its render target, and the particles.
func (r *bootOSRun) prepareScene(seed uint32) {
	s := &r.style
	sp := r.layout.Sprite
	s.horizonY = float64(sp.Y) + 0.40*s.side
	s.focal = 4 * (s.floorY - s.horizonY)
	s.crackTop = int32(s.horizonY)
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	s.floor = bootLiquidTexture(r.renderer, bootFloorPixels(128, 64), 128, 64)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	s.grid = s.gridLines()
	rng := seed
	s.growCracks(&rng)
	s.crackTex = r.crackTarget()
	s.sparks = s.makeSparks(&rng)
}

// crackTarget is the crack layer's render target, cleared to transparent, or
// nil when the driver refuses one (the cracks are then drawn as lines).
func (r *bootOSRun) crackTarget() *sdl.Texture {
	s := &r.style
	h := screenHeight - s.crackTop
	if h <= 0 || screenWidth <= 0 {
		return nil
	}
	tex, err := r.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, screenWidth, h)
	if err != nil || tex == nil {
		return nil
	}
	if err := r.renderer.SetRenderTarget(tex); err != nil {
		_ = tex.Destroy()
		return nil
	}
	_ = r.renderer.SetDrawColor(0, 0, 0, 0)
	_ = r.renderer.Clear()
	if err := r.renderer.SetRenderTarget(nil); err != nil {
		logWarn("startup", "boot cracks could not release their target: %v", err)
	}
	_ = r.renderer.SetClipRect(nil)
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	return tex
}

// project maps a point of the floor — x across and y towards the viewer, in
// pixels as they measure at the point of impact — onto the canvas.
func (s *bootStyleRun) project(x, y float64) (float32, float32) {
	k := 1 - y/s.focal
	if k < 0.1 {
		k = 0.1
	}
	return float32(s.cx + x/k), float32(s.horizonY + (s.floorY-s.horizonY)/k)
}

// gridLines is the floor's faint perspective grid as x0, y0, x1, y1 runs.
func (s *bootStyleRun) gridLines() []float32 {
	if s.focal <= 0 || s.h-s.horizonY <= 0 {
		return nil
	}
	g := s.tile * 0.75
	near := s.focal * (1 - (s.floorY-s.horizonY)/(s.h-s.horizonY))
	far := -5 * s.focal
	out := make([]float32, 0, 4*32)
	for j := -9; j <= 9; j++ {
		x0, y0 := s.project(float64(j)*g, far)
		x1, y1 := s.project(float64(j)*g, near)
		out = append(out, x0, y0, x1, y1)
	}
	for j := -4; j < 12 && float64(j)*g < near; j++ {
		_, y := s.project(0, float64(j)*g)
		out = append(out, 0, y, float32(s.w), y)
	}
	return out
}

// bootCrackBirth is when a crack reaches a fraction u of the longest crack's
// length: fast at first, slowing as it spreads.
func bootCrackBirth(u float64) float64 {
	return bootCrackGrowMS * (1 - math.Sqrt(1-bootClamp01(u)))
}

// growCracks grows the crack network from the point of impact: six to eight
// cracks wandering outwards, branching now and then, in canvas coordinates,
// sorted by when each piece appears.
func (s *bootStyleRun) growCracks(rng *uint32) {
	maxLen := 0.42 * s.w
	if maxLen <= 0 || s.focal <= 0 {
		return
	}
	step := maxLen * 0.055
	segs := make([]bootCrackSeg, 0, bootCrackMaxSegs)
	var walk func(x, y, a, dist, length float64, depth int)
	walk = func(x, y, a, dist, length float64, depth int) {
		for left := length; left > 0 && len(segs) < bootCrackMaxSegs; {
			l := math.Min(left, step*(0.7+0.6*bootStyleRand(rng)))
			a += (bootStyleRand(rng) - 0.5) * 0.65
			nx, ny := x+math.Cos(a)*l, y+math.Sin(a)*l
			if ny > 0.86*s.focal {
				return
			}
			x0, y0 := s.project(x, y)
			x1, y1 := s.project(nx, ny)
			dist += l
			segs = append(segs, bootCrackSeg{x0: x0, y0: y0, x1: x1, y1: y1, birth: float32(bootCrackBirth(dist / maxLen)), main: depth == 0})
			x, y, left = nx, ny, left-l
			if depth < 2 && left > step && bootStyleRand(rng) < 0.2 {
				turn := 0.45 + 0.45*bootStyleRand(rng)
				if bootStyleRand(rng) < 0.5 {
					turn = -turn
				}
				walk(x, y, a+turn, dist, left*(0.35+0.25*bootStyleRand(rng)), depth+1)
			}
		}
	}
	n := 6 + int(bootStyleRand(rng)*3)
	r0 := s.tile * 0.32
	for i := 0; i < n; i++ {
		a := (float64(i) + 0.6*bootStyleRand(rng)) * 2 * math.Pi / float64(n)
		walk(math.Cos(a)*r0, math.Sin(a)*r0*0.5, a, r0, maxLen*(0.55+0.45*bootStyleRand(rng)), 0)
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].birth < segs[j].birth })
	s.cracks = segs
}

// makeSparks scatters the impact's particles: sparks flying wide and falling
// back to the floor, and dust drifting out and up.
func (s *bootStyleRun) makeSparks(rng *uint32) []bootSpark {
	out := make([]bootSpark, 0, 40)
	for i := 0; i < 40; i++ {
		dir := 1.0
		if bootStyleRand(rng) < 0.5 {
			dir = -1
		}
		p := bootSpark{
			x:      s.cx + dir*s.tile*(0.15+0.33*bootStyleRand(rng)),
			ground: s.floorY + s.h*(-0.015+0.07*bootStyleRand(rng)),
			tint:   bootStyleRand(rng),
			dust:   i >= 24,
		}
		if p.dust {
			p.vx = dir * s.w * (0.10 + 0.28*bootStyleRand(rng))
			p.vy = s.h * (0.03 + 0.06*bootStyleRand(rng))
			p.life = 700 + 450*bootStyleRand(rng)
			p.size = s.h * (0.035 + 0.04*bootStyleRand(rng))
		} else {
			p.vx = dir * s.w * (0.35 + 0.85*bootStyleRand(rng))
			p.vy = -s.h * (0.15 + 0.65*bootStyleRand(rng))
			p.life = 330 + 330*bootStyleRand(rng)
			p.size = s.h * (0.010 + 0.014*bootStyleRand(rng))
		}
		out = append(out, p)
	}
	return out
}

// bootFloorPixels renders the floor's fog: white, brightest in a band just
// under the horizon and fading towards the viewer and the sides (the boot
// tints it).
func bootFloorPixels(w, h int) []byte {
	if w <= 0 || h <= 0 {
		return nil
	}
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		v := (float64(y) + 0.5) / float64(h)
		d := (v - 0.07) / 0.22
		band := bootSmoothstep(0, 0.07, v) * math.Exp(-d*d)
		body := 0.30 * math.Pow(1-v, 1.4) * bootSmoothstep(0, 0.05, v)
		for x := 0; x < w; x++ {
			u := 2*(float64(x)+0.5)/float64(w) - 1
			bootPutPixel(pix, (y*w+x)*4, 255, 255, 255, (0.75*band+body)*(1-0.55*u*u))
		}
	}
	return pix
}

// release destroys the style's textures. It runs on the render thread while
// their renderer is alive, with the screen as the target.
func (s *bootStyleRun) release() {
	if s.crackTex != nil {
		_ = s.crackTex.Destroy()
	}
	if s.floor != nil {
		_ = s.floor.Destroy()
	}
	s.crackTex, s.floor = nil, nil
}

// chimeScore is the power-on chime for this style: the start chime pushed
// back so its bells still land on the blooms after the style's lead, with the
// style's knocks on its landings.
func (s *bootStyleRun) chimeScore() ([]chimeNote, float64) {
	delay := math.Min(bootOSChimeDelay+s.lead/1000, bootStyleChimeMaxDelay)
	score := bootOSDelayedScore(chimeStartScore, delay)
	score = append(score, s.thuds...)
	return score, chimeStartSeconds + delay
}

// ── Playing a style ─────────────────────────────────────────────────────

// styleGuard runs one step of a style. A panic falls back to the classic boot
// for the rest of this one: the renderer is handed back to the screen, the
// style's textures go, and false says this frame should be drawn classic.
func (r *bootOSRun) styleGuard(what string, fn func()) (ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logWarn("startup", "boot animation %q failed while %s, playing the classic boot: %v", r.style.kind, what, rec)
			if r.renderer != nil {
				_ = r.renderer.SetRenderTarget(nil)
				_ = r.renderer.SetClipRect(nil)
				_ = r.renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
			}
			r.style.release()
			r.style = bootStyleRun{kind: bootStyleClassic}
			r.drift = r.motion
			ok = false
		}
	}()
	fn()
	return true
}

// frame is the boot's layer at ms. The animated styles run the classic beats
// (wordmark, dawn, blooms, progress) after their own entrance and draw the
// logo themselves; the still image keeps every beat but moves nothing. hold
// is set for the hold's frames, which fire the style's buzzes; every frame
// grows the cracks first, before anything is drawn to the screen.
func (r *bootOSRun) frame(ms float64, hold bool) bootOSFrame {
	r.nowMS = ms
	s := &r.style
	switch s.kind {
	case bootStyleCinematic, bootStyleRoll:
		var f bootOSFrame
		if r.styleGuard("timing", func() {
			if hold {
				r.styleFire(ms)
			}
			r.crackUpdate(ms)
			f = bootOSTimeline(ms-s.lead, r.powerOn, r.motion, false)
			f.logo, f.logoScale, f.comet = 1, 1, -1
			if r.continuity {
				// The firmware's wordmark stays where it is.
				f.word = 1
			}
		}) {
			return f
		}
	case bootStyleImage:
		return bootOSTimeline(ms, r.powerOn, false, r.continuity)
	}
	return bootOSTimeline(ms, r.powerOn, r.motion, r.continuity)
}

// styleMinMS is the shortest hold of this boot: the style's, or def.
func (r *bootOSRun) styleMinMS(def float64) float64 {
	if r.style.minMS > 0 {
		return r.style.minMS
	}
	return def
}

// styleFire fires the buzzes due by ms.
func (r *bootOSRun) styleFire(ms float64) {
	s := &r.style
	for s.fired < len(s.pulses) && s.pulses[s.fired].at <= ms {
		p := s.pulses[s.fired]
		s.fired++
		if ms-p.at > bootPulseLateMS || !bootStyleHapticsReady() {
			continue
		}
		playHaptic(p.pulse)
	}
}

// crackUpdate draws the crack pieces born by ms into the crack layer.
func (r *bootOSRun) crackUpdate(ms float64) {
	s := &r.style
	if s.crackTex == nil || s.crackDone >= len(s.cracks) {
		return
	}
	t := float32(ms - s.impact)
	if t < 0 {
		return
	}
	n := s.crackDone
	for n < len(s.cracks) && s.cracks[n].birth <= t {
		n++
	}
	if n == s.crackDone {
		return
	}
	if err := r.renderer.SetRenderTarget(s.crackTex); err != nil {
		// The cracks are drawn as lines from here on.
		_ = s.crackTex.Destroy()
		s.crackTex = nil
		return
	}
	_ = r.renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	bootCrackLines(r.renderer, s.cracks[s.crackDone:n], 0, -float32(s.crackTop), sdl.Color{R: 255, G: 255, B: 255, A: 255}, 255, 130)
	if err := r.renderer.SetRenderTarget(nil); err != nil {
		logWarn("startup", "boot cracks could not release their target: %v", err)
	}
	_ = r.renderer.SetClipRect(nil)
	s.crackDone = n
}

// bootCrackLines draws crack pieces offset by (dx, dy) in c: a faint halo at
// halo alpha either side, then the line itself at alpha, two pixels wide for
// a main crack.
func bootCrackLines(renderer *sdl.Renderer, segs []bootCrackSeg, dx, dy float32, c sdl.Color, alpha, halo uint8) {
	if halo > 0 {
		_ = renderer.SetDrawColor(c.R, c.G, c.B, halo)
		for i := range segs {
			g := &segs[i]
			_ = renderer.DrawLineF(g.x0+dx, g.y0+dy-1, g.x1+dx, g.y1+dy-1)
			below := float32(1)
			if g.main {
				below = 2
			}
			_ = renderer.DrawLineF(g.x0+dx, g.y0+dy+below, g.x1+dx, g.y1+dy+below)
		}
	}
	_ = renderer.SetDrawColor(c.R, c.G, c.B, alpha)
	for i := range segs {
		g := &segs[i]
		_ = renderer.DrawLineF(g.x0+dx, g.y0+dy, g.x1+dx, g.y1+dy)
		if g.main {
			_ = renderer.DrawLineF(g.x0+dx, g.y0+dy+1, g.x1+dx, g.y1+dy+1)
		}
	}
}

// dropPose is the falling logo at ms: how far above its rest it is and its
// stretch or squash across and up.
func (s *bootStyleRun) dropPose(ms float64) (dy, sx, sy float64) {
	d := s.drop
	switch {
	case d == nil:
		return 0, 1, 1
	case ms < s.lift:
		// The firmware's logo leaves through the top, gathering speed.
		p := ms / s.lift
		return -s.up * p * p, 1 - 0.04*p, 1 + 0.08*p
	case ms < s.impact:
		p := (ms - s.lift) / d.fall
		st := d.stretch * p * p
		return -s.up * (1 - p*p), 1 - 0.5*st, 1 + st
	}
	for i := range s.segs {
		g := &s.segs[i]
		if ms >= g.to {
			continue
		}
		q := bootClamp01((ms - g.from) / (g.to - g.from))
		if g.height > 0 {
			return -g.height * 4 * q * (1 - q), 1, 1
		}
		sq := g.squash * math.Cos(math.Pi/2*q)
		return 0, 1 + 0.6*sq, 1 - sq
	}
	return 0, 1, 1
}

// rollPose is the rolling tile at ms: its centre, its turn (degrees,
// clockwise; zero at rest), its squash across and up, and its presence. The
// tile turns about its bottom-leading corner's centre, which itself rolls
// along the floor by the arc it turns through.
func (s *bootStyleRun) rollPose(ms float64) (x, y, angle, sx, sy, alpha float64) {
	steps := len(s.stepAt)
	if steps == 0 {
		return s.cx, s.cy, 0, 1, 1, 1
	}
	i, start := 0, 0.0
	for i < steps && ms >= s.stepAt[i] {
		start = s.stepAt[i]
		i++
	}
	theta := 0.0
	if i < steps {
		p := bootClamp01((ms - start) / (s.stepAt[i] - start))
		// Slow as it tips over, quick as it lands.
		theta = math.Pi / 2 * (1 - math.Cos(math.Pi/2*p))
	}
	a := s.rollA
	pivotX := s.rollX0 + float64(i)*s.stepD + a + s.rollRad*theta
	pivotY := s.floorY - s.rollRad
	x = pivotX - a*math.Cos(theta) + a*math.Sin(theta)
	y = pivotY - a*math.Sin(theta) - a*math.Cos(theta)
	angle = float64(i-steps)*90 + theta*180/math.Pi
	sx, sy, alpha = 1, 1, 1
	if i > 0 {
		amp := 0.045
		if i == steps {
			amp = 0.07
		}
		if since := ms - start; since < 220 {
			q := amp * math.Exp(-since/55) * math.Cos(2*math.Pi*since/150)
			sx, sy = 1+0.5*q, 1-q
		}
	}
	if s.quick {
		alpha = bootSmoothstep(0, 150, ms)
	}
	return x, y, angle, sx, sy, alpha
}

// shake is the cinematic picture's offset at ms: a jolt at the impact that
// dies away in under half a second, and a tremor at the first bounce.
func (s *bootStyleRun) shake(ms float64) (float64, float64) {
	if s.kind != bootStyleCinematic || s.drop == nil {
		return 0, 0
	}
	t := ms - s.impact
	if t < 0 {
		return 0, 0
	}
	amp := 0.0
	if t < 430 {
		amp = s.drop.shake * s.h * math.Exp(-t/130) * (1 - bootSmoothstep(330, 430, t))
	}
	if len(s.land) > 1 {
		if u := ms - s.land[1]; u >= 0 && u < 160 {
			amp += 0.25 * s.drop.shake * s.h * math.Exp(-u/50)
		}
	}
	if amp <= 0 {
		return 0, 0
	}
	return amp * math.Sin(t*2*math.Pi/46+0.4), 0.8 * amp * math.Cos(t*2*math.Pi/38)
}

// drawStyleLayer is drawLayer for the animated styles: the floor, the logo's
// shadow, its halo, the logo itself as the style moves it, its rim once it is
// upright, and the impact's particles and flash. fade and zoom are the
// arrival's, as in drawLayer. It reports false when the style failed and the
// frame should be drawn classic.
func (r *bootOSRun) drawStyleLayer(f *bootOSFrame, fade, zoom float64) bool {
	return r.styleGuard("drawing", func() {
		fade = bootClamp01(fade)
		if fade <= 0 {
			return
		}
		s := &r.style
		ms := r.nowMS
		light := glassLightTheme()
		k := &bootOSKit
		shx, shy := s.shake(ms)
		var x, y, angle, sx, sy, alpha float64
		switch s.kind {
		case bootStyleCinematic:
			dy, dsx, dsy := s.dropPose(ms)
			x, y, angle, sx, sy, alpha = s.cx, s.cy+dy, 0, dsx, dsy, 1
			if s.drop != nil && s.drop.scene {
				r.drawCineGround(ms, fade, shx, shy, light)
			}
		default:
			x, y, angle, sx, sy, alpha = s.rollPose(ms)
			r.drawRollGround(ms, fade, light)
		}
		// Squash and stretch keep the tile's bottom on the floor.
		y += s.tile / 2 * (1 - sy)
		x, y = x+shx, y+shy
		if k.dot != nil {
			// The contact shadow darkens and tightens as the logo nears the floor.
			gap := math.Max(0, s.floorY+shy-(y+s.tile/2))
			near := 1 - bootClamp01(gap/(0.6*s.h))
			peak := 150.0
			if light {
				peak = 70
			}
			_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
			r.drawDotWH(x, s.floorY+shy, s.tile*(0.8+0.5*near)*zoom, s.tile*0.16*zoom, sdl.Color{A: 255}, peak*near*near*alpha*fade)
		}
		if halo := bootClamp01(f.halo) * fade; halo > 0 && k.dot != nil {
			r.drawHalo(x, y, s.side*zoom, halo)
		}
		if s.ghost {
			if g := (1 - bootSmoothstep(0, bootRollGhostMS, ms)) * fade; g > 0 {
				r.drawLogoPose(s.cx, s.cy, 1, 1, 0, g, zoom)
			}
		}
		r.drawLogoPose(x, y, sx, sy, angle, alpha*fade, zoom)
		if rim := bootClamp01(f.rim) * fade; rim > 0 && angle == 0 && k.ring != nil {
			r.drawRim(x, y, zoom, rim)
		}
		if s.kind == bootStyleCinematic && s.drop != nil && s.drop.scene {
			r.drawSparks(ms, fade, shx, shy, light)
			r.drawFlash(ms, fade, light)
		}
		if k.dot != nil {
			_ = k.dot.SetColorMod(255, 255, 255)
			_ = k.dot.SetAlphaMod(255)
			_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
		}
	})
}

// drawLogoPose draws the logo artwork centred on (x, y), scaled sx across and
// sy up the screen, turned angle degrees clockwise, at alpha, grown by zoom.
func (r *bootOSRun) drawLogoPose(x, y, sx, sy, angle, alpha, zoom float64) {
	alpha = bootClamp01(alpha)
	if alpha <= 0 {
		return
	}
	tex := brandLogoTexture(r.renderer, r.layout.Sprite.W)
	if tex == nil {
		return
	}
	side := r.style.side * zoom
	w, h := side*sx, side*sy
	// The rect is the artwork's own before it turns: a quarter-turn swaps
	// which of its sides runs across the screen.
	if int(math.Round(angle/90))%2 != 0 {
		w, h = h, w
	}
	bootOSF = sdl.FRect{X: float32(x - w/2), Y: float32(y - h/2), W: float32(w), H: float32(h)}
	_ = tex.SetAlphaMod(uint8(math.Round(255 * alpha)))
	if angle == 0 {
		_ = r.renderer.CopyF(tex, nil, &bootOSF)
	} else {
		_ = r.renderer.CopyExF(tex, nil, &bootOSF, angle, nil, sdl.FLIP_NONE)
	}
	_ = tex.SetAlphaMod(255)
}

// drawDotWH paints the soft round light stretched to w×h, centred on (x, y),
// tinted c at alpha (0..255), in whatever blend mode the dot is set to.
func (r *bootOSRun) drawDotWH(x, y, w, h float64, c sdl.Color, alpha float64) {
	k := &bootOSKit
	if k.dot == nil || alpha <= 0 || w <= 0 || h <= 0 {
		return
	}
	_ = k.dot.SetColorMod(c.R, c.G, c.B)
	_ = k.dot.SetAlphaMod(uint8(math.Min(255, math.Round(alpha))))
	bootOSF = sdl.FRect{X: float32(x - w/2), Y: float32(y - h/2), W: float32(w), H: float32(h)}
	_ = r.renderer.CopyF(k.dot, nil, &bootOSF)
}

// bootStyleLightMode is how the boot's lights are laid on: added to a dark
// screen, blended on a light theme's, where added light vanishes.
func bootStyleLightMode(light bool) sdl.BlendMode {
	if light {
		return sdl.BLENDMODE_BLEND
	}
	return sdl.BLENDMODE_ADD
}

// drawCineGround paints the cinematic floor: its fog, the horizon's glow, the
// grid and the cracks, shaken by (shx, shy).
func (r *bootOSRun) drawCineGround(ms, fade, shx, shy float64, light bool) {
	s := &r.style
	renderer := r.renderer
	k := &bootOSKit
	p := bootSmoothstep(0, 450, ms) * fade
	if p <= 0 {
		return
	}
	accent := glassAccent()
	if s.floor != nil {
		tint := glassMix(sdl.Color{R: 52, G: 58, B: 76, A: 255}, accent, 0.30)
		a := 235.0
		if light {
			tint, a = sdl.Color{R: 20, G: 24, B: 34, A: 255}, 70
		}
		pad := 0.04 * s.w
		_ = s.floor.SetColorMod(tint.R, tint.G, tint.B)
		_ = s.floor.SetAlphaMod(uint8(math.Round(a * p)))
		bootOSF = sdl.FRect{X: float32(-pad + shx), Y: float32(s.horizonY + shy), W: float32(s.w + 2*pad), H: float32(s.h - s.horizonY + pad)}
		_ = renderer.CopyF(s.floor, nil, &bootOSF)
		_ = s.floor.SetColorMod(255, 255, 255)
		_ = s.floor.SetAlphaMod(255)
	}
	if k.dot != nil {
		_ = k.dot.SetBlendMode(bootStyleLightMode(light))
		r.drawDotWH(s.cx+shx, s.horizonY+shy, s.w*1.2, s.h*0.09, accent, 110*p)
		_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
	}
	if len(s.grid) >= 4 {
		gc := glassMix(sdl.Color{R: 120, G: 130, B: 150, A: 255}, accent, 0.5)
		if light {
			gc = sdl.Color{R: 40, G: 46, B: 60, A: 255}
		}
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		_ = renderer.SetDrawColor(gc.R, gc.G, gc.B, uint8(math.Round(30*p)))
		fx, fy := float32(shx), float32(shy)
		for i := 0; i+3 < len(s.grid); i += 4 {
			_ = renderer.DrawLineF(s.grid[i]+fx, s.grid[i+1]+fy, s.grid[i+2]+fx, s.grid[i+3]+fy)
		}
	}
	r.drawCracks(ms, p, shx, shy, light)
}

// drawCracks paints the crack layer at presence p: its lines in a cool tone,
// then their glow in the accent, hot as they spread and cooling after.
func (r *bootOSRun) drawCracks(ms, p, shx, shy float64, light bool) {
	s := &r.style
	t := ms - s.impact
	if t < 0 || len(s.cracks) == 0 || p <= 0 {
		return
	}
	glow := 1.0
	if t > bootCrackGrowMS {
		glow = 0.16 + 0.84*math.Exp(-(t-bootCrackGrowMS)/520)
	}
	accent := glassAccent()
	cool, coolA := glassMix(sdl.Color{R: 96, G: 104, B: 124, A: 255}, accent, 0.2), 150.0
	if light {
		cool, coolA = sdl.Color{R: 30, G: 34, B: 44, A: 255}, 170
	}
	if tex := s.crackTex; tex != nil {
		bootOSF = sdl.FRect{X: float32(shx), Y: float32(float64(s.crackTop) + shy), W: float32(screenWidth), H: float32(screenHeight - s.crackTop)}
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
		_ = tex.SetColorMod(cool.R, cool.G, cool.B)
		_ = tex.SetAlphaMod(uint8(math.Round(coolA * p)))
		_ = r.renderer.CopyF(tex, nil, &bootOSF)
		glowA := 255.0
		if light {
			glowA = 200
		}
		_ = tex.SetBlendMode(bootStyleLightMode(light))
		_ = tex.SetColorMod(accent.R, accent.G, accent.B)
		_ = tex.SetAlphaMod(uint8(math.Round(glowA * glow * p)))
		_ = r.renderer.CopyF(tex, nil, &bootOSF)
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
		_ = tex.SetColorMod(255, 255, 255)
		_ = tex.SetAlphaMod(255)
		return
	}
	// No target: the pieces born so far, drawn as lines.
	n := 0
	for n < len(s.cracks) && float64(s.cracks[n].birth) <= t {
		n++
	}
	_ = r.renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	col := glassMix(cool, accent, glow)
	bootCrackLines(r.renderer, s.cracks[:n], float32(shx), float32(shy), col, uint8(math.Round(220*p)), uint8(math.Round(70*p)))
}

// drawRollGround paints the roll's floor: one faint line of light along it.
func (r *bootOSRun) drawRollGround(ms, fade float64, light bool) {
	s := &r.style
	k := &bootOSKit
	p := bootSmoothstep(0, 300, ms) * fade
	if p <= 0 || k.dot == nil {
		return
	}
	_ = k.dot.SetBlendMode(bootStyleLightMode(light))
	r.drawDotWH(s.cx, s.floorY, s.w*0.95, math.Max(4, s.h*0.014), glassAccent(), 60*p)
	_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
}

// drawSparks paints the impact's dust (behind) and sparks at ms.
func (r *bootOSRun) drawSparks(ms, fade, shx, shy float64, light bool) {
	s := &r.style
	k := &bootOSKit
	t := ms - s.impact
	if t < 0 || t > 1200 || k.dot == nil {
		return
	}
	sec := t / 1000
	accent := glassAccent()
	dustCol := glassMix(sdl.Color{R: 150, G: 152, B: 166, A: 255}, accent, 0.18)
	if light {
		dustCol = sdl.Color{R: 90, G: 92, B: 104, A: 255}
	}
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	for pass := 0; pass < 2; pass++ {
		dust := pass == 0
		if dust {
			_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
		} else {
			_ = k.dot.SetBlendMode(bootStyleLightMode(light))
		}
		for i := range s.sparks {
			p := &s.sparks[i]
			if p.dust != dust {
				continue
			}
			u := t / p.life
			if u >= 1 {
				continue
			}
			if dust {
				x := p.x + p.vx*0.35*(1-math.Exp(-sec/0.35))
				y := p.ground - p.vy*(1-math.Exp(-sec/0.4))
				r.drawDot(x+shx, y+shy, p.size*(1+1.6*u), dustCol, 80*(1-u)*(1-u)*fade)
				continue
			}
			x := p.x + p.vx*sec
			y := math.Min(s.floorY+p.vy*sec+1.2*s.h*sec*sec, p.ground)
			col := glassMix(white, accent, 0.35+0.5*p.tint)
			if light {
				col = glassMix(accent, sdl.Color{A: 255}, 0.2)
			}
			r.drawDot(x+shx, y+shy, p.size*(1-0.4*u), col, 255*math.Pow(1-u, 1.3)*fade)
		}
	}
	_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
}

// drawFlash paints the impact's light: a burst of the accent where the logo
// hit and, on a dark screen, a brief brightening of the whole picture.
func (r *bootOSRun) drawFlash(ms, fade float64, light bool) {
	s := &r.style
	k := &bootOSKit
	t := ms - s.impact
	if t < 0 || t > 260 {
		return
	}
	e := math.Exp(-t/60) * fade
	if k.dot != nil {
		_ = k.dot.SetBlendMode(bootStyleLightMode(light))
		r.drawDotWH(s.cx, s.floorY, s.tile*2.6, s.tile*0.7, glassAccent(), 230*e)
		_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
	}
	if light {
		return
	}
	_ = r.renderer.SetDrawBlendMode(sdl.BLENDMODE_ADD)
	_ = r.renderer.SetDrawColor(255, 255, 255, uint8(math.Round(70*e)))
	bootLiquidR = sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
	_ = r.renderer.FillRect(&bootLiquidR)
	_ = r.renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
}
