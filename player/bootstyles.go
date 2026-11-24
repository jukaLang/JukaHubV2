package main

// bootstyles.go — the boot animation's styles (Settings → General → Boot
// animation, Custom "bootStyle").
//
// bootos.go decides WHEN the full boot plays (startupAnimation) and runs it;
// this file decides WHAT it looks like:
//
//   - cinematic: the camera looks straight down at the ground, a dark floor
//     lit in a pool round the logo's place, and the glass logo falls to it
//     from the screen (Juka, 2026-11-23: "the logo coming straight down from
//     the screen, like starting off big and getting smaller as it reaches the
//     ground"). It appears big, as if just under the glass, and shrinks to its
//     resting size as it falls, gathering speed, trailing a blur of where it
//     just was, its shadow sliding in under it and darkening, and crashes: the
//     motor gives one hard buzz on the exact frame of the impact, the picture
//     shakes and flashes, a wave of light runs out along the ground, dust and
//     sparks burst out all round and cracks race out from under the tile,
//     glowing in the accent and cooling to dark. The logo is driven into the
//     ground for a moment, bounces back up towards the screen twice (each
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
//   - none (the default since 2026-11-18): no boot animation and no quick
//     hello at all (bootOSPlanStartup): the queued startup work runs and the
//     first screen appears as soon as it can, with no arrival. The fastest
//     start.
//
// Reduced motion and Battery Saver (bootOSMotion) turn cinematic and roll into
// the still image: no shake, no particles, no flash. The quick hello of an app
// launch plays a compressed version of the style: a short fall from a little
// above with one small bounce (no ground, cracks or dust: building them would
// cost the launch the time the hello is meant to hide), or the last
// quarter-turn of the roll.
//
// Everything is drawn from textures made once before the first frame: the
// ground (a small texture scaled up), the tile's soft shadow, the artwork at
// its own 300 px for the frames where the falling logo is far bigger than at
// rest, the soft dot and the rim's glow bootos.go already has (sparks, dust,
// glow, the wave), and the cracks, which are grown into a render target
// segment by segment as their time comes, so a frame only draws the new
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
	// bootDropLiftMS is how long the firmware's logo takes to rise from the
	// ground to just under the screen before it falls, on a power-on that
	// starts from it.
	bootDropLiftMS = 340.0
	// bootDropContactMS is how long a bounce's landing stays on the ground.
	bootDropContactMS = 45.0
	// bootDropTrailMS spaces the falling logo's trail: where it was that long
	// ago, and twice that.
	bootDropTrailMS = 24.0
	// bootDropBigFrom is how many times its resting size the logo is drawn
	// before the artwork's own pixels take over from the resting size's.
	bootDropBigFrom = 1.25
	// bootDropShadowTex is the shadow texture's side, and bootDropShadowTile
	// the side of the tile's shape inside it.
	bootDropShadowTex  = 64
	bootDropShadowTile = 40
	// bootGroundPad is how far the ground reaches past each edge of the
	// screen (of its width), so the shake never shows its edge; bootGroundTexW
	// is its texture's width.
	bootGroundPad  = 0.04
	bootGroundTexW = 128
	// bootWaveMS is how long the impact's wave takes to run out and fade, and
	// bootWaveReach how far it gets (times the artwork's side).
	bootWaveMS    = 340.0
	bootWaveReach = 6.0
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

// bootDropBeats times one fall from the screen to the ground: how long it takes
// (ms), how big the logo starts (times its size at rest) and, when nothing was
// on screen before it, how long it takes to appear there (ms); how far the
// impact drives it into the ground (of its size) and how long that lasts (ms);
// each bounce's airtime (ms) and height (of the way back up to the screen);
// the shake (of the screen height); and whether the ground, cracks, wave,
// dust, sparks and flash are drawn.
type bootDropBeats struct {
	fall   float64
	start  float64
	appear float64
	dip    float64
	hold   float64
	air    [2]float64
	height [2]float64
	shake  float64
	scene  bool
}

// bootDropPower is the cinematic power-on's fall: from over four times the
// logo's size, so it starts out spilling past the screen's edges.
var bootDropPower = bootDropBeats{
	fall:   640,
	start:  4.4,
	appear: 160,
	dip:    0.08,
	hold:   70,
	air:    [2]float64{230, 120},
	height: [2]float64{0.06, 0.018},
	shake:  0.022,
	scene:  true,
}

// bootDropQuick is the quick hello's: a short fall from a little above and
// one small bounce.
var bootDropQuick = bootDropBeats{
	fall:   300,
	start:  1.7,
	appear: 110,
	dip:    0.05,
	hold:   50,
	air:    [2]float64{140, 0},
	height: [2]float64{0.025, 0},
	shake:  0.006,
}

// bootDropTrail is the presence of the falling logo's trail, nearest first.
var bootDropTrail = [2]float64{0.30, 0.15}

// The impact's buzz and its two aftershocks, and the quick hello's tick.
var (
	bootPulseImpact = hapticPulse{strength: 1.00, ms: 180}
	bootPulseAfter1 = hapticPulse{strength: 0.55, ms: 60}
	bootPulseAfter2 = hapticPulse{strength: 0.32, ms: 40}
	bootPulseTick   = hapticPulse{strength: 0.45, ms: 30}
	bootPulseRest   = hapticPulse{strength: 0.60, ms: 45}
)

// bootDropSeg is one part of the fall after the impact: on the ground (a
// contact, driven dip of its size into the ground at its start) or in the air
// (a hop height of the way back up to the screen).
type bootDropSeg struct {
	from   float64
	to     float64
	dip    float64
	height float64
}

// bootCrackSeg is one straight piece of a crack on the canvas, born birth ms
// after the impact, width pixels wide: the main cracks are widest near the
// impact, the branches thinnest.
type bootCrackSeg struct {
	x0, y0, x1, y1 float32
	birth          float32
	width          uint8
}

// bootSpark is one particle of the impact, thrown out from under the tile's
// edge at (x, y) with velocity (vx, vy), in pixels a second: a spark flying
// fast and dying quickly, or a puff of dust rolling out slower and spreading.
type bootSpark struct {
	x    float64
	y    float64
	vx   float64
	vy   float64
	life float64
	size float64
	tint float64
	dust bool
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
	// visible tile inside it, and the floor the roll runs along (the tile's
	// resting bottom), in canvas pixels.
	w      float64
	h      float64
	cx     float64
	cy     float64
	side   float64
	tile   float64
	floorY float64

	// The fall: its beats, the toss off the firmware's logo, the impact, the
	// parts after it, when it has settled and how high it starts (of the way
	// from the ground up to the screen).
	drop   *bootDropBeats
	lift   float64
	impact float64
	segs   []bootDropSeg
	land   []float64
	settle float64
	high   float64

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

	// The cinematic ground, the cracks (grown into crackTex, crackDone of them
	// so far) and the particles; the tile's shadow, and the artwork at its own
	// size for the frames where the falling logo is far bigger than at rest
	// (nil where that is no bigger than the resting size's texture).
	ground    *sdl.Texture
	cracks    []bootCrackSeg
	crackTex  *sdl.Texture
	crackDone int
	sparks    []bootSpark
	shadow    *sdl.Texture
	big       *sdl.Texture
}

// animated reports whether the style draws its own logo layer.
func (s *bootStyleRun) animated() bool {
	return s.kind == bootStyleCinematic || s.kind == bootStyleRoll
}

// ── The setting ─────────────────────────────────────────────────────────

// bootStyleSetting is the stored boot animation, bootStyleDefault unless chosen.
// Someone who never picked a style but asked for the full boot on every start
// (Startup animation → Full every time) asked for an animation, so they keep
// the one that was the default when they asked.
func bootStyleSetting(c *Config) string {
	if c != nil {
		if v, ok := customString(c.Variables.Custom, bootStyleKey); ok {
			switch s := strings.ToLower(strings.TrimSpace(v)); s {
			case bootStyleCinematic, bootStyleRoll, bootStyleImage, bootStyleClassic, bootStyleRandom, bootStyleNone:
				return s
			}
		}
		if bootOSAnimationSetting(c) == bootOSAnimationAlways {
			return bootStyleCinematic
		}
	}
	return bootStyleDefault()
}

// bootStyleDefault is the boot animation nobody chose: none, everywhere
// (2026-11-18, "as fast as possible"). The device has shown its own logo since
// power-on, JukaHub OS's launcher may already show the Home picture kept at
// the last power off (bootframe.go), and a two-second animation would be most
// of a boot that should take under two (startup.go): Home comes up as soon as
// it is ready, and the startup chime, if on, rises with it (bootChimeOverHome).
// A style picked in Settings still plays, on every platform.
func bootStyleDefault() string {
	return bootStyleNone
}

// bootStyleOptions are the Settings row's stops.
func bootStyleOptions(*Config) []setOption {
	return []setOption{
		{Value: bootStyleCinematic, Label: "Cinematic", Note: "The logo falls from the screen onto the ground, which cracks, and the device shakes"},
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
		r.prepareDropTextures()
		if s.drop.scene {
			r.prepareScene(uint32(time.Now().UnixNano()) | 1)
		}
	case bootStyleRoll:
		s.prepareRoll(r.continuity && !s.quick)
	}
	sort.SliceStable(s.pulses, func(i, j int) bool { return s.pulses[i].at < s.pulses[j].at })
}

// prepareDrop times the fall: the toss (from the firmware's logo), the fall,
// the impact, the contacts and hops after it, the buzzes and knocks on each
// landing.
func (s *bootStyleRun) prepareDrop(lift bool) {
	d := &bootDropPower
	if s.quick {
		d = &bootDropQuick
	}
	s.drop = d
	// The camera looks down from the screen, so a logo drawn d.start times
	// its resting size is this far up towards it (dropHeight).
	s.high = 1 - 1/math.Max(1.01, d.start)
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
		s.segs = append(s.segs, bootDropSeg{from: at, to: at + contact, dip: d.dip * strength})
		s.land = append(s.land, at)
		at += contact
		if i >= len(d.air) || d.air[i] <= 0 {
			break
		}
		s.segs = append(s.segs, bootDropSeg{from: at, to: at + d.air[i], height: d.height[i]})
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

// prepareDropTextures builds what every fall draws, the quick hello's too: the
// tile's soft shadow, and the artwork at its own size where that is well over
// the size the logo rests at, for the frames it falls through far bigger.
func (r *bootOSRun) prepareDropTextures() {
	s := &r.style
	const n, tile = bootDropShadowTex, bootDropShadowTile
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	s.shadow = bootLiquidTexture(r.renderer, bootLiquidShadowPixels(n, n, tile, tile, tile*bootLogoTileCorner), n, n)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	if src := brandLogoSource(); src != nil && float64(src.Rect.Dx()) > bootDropBigFrom*s.side {
		s.big = brandLogoUpload(r.renderer, src)
	}
}

// prepareScene builds the cinematic ground: its texture, the crack network
// and its render target, and the particles.
func (r *bootOSRun) prepareScene(seed uint32) {
	s := &r.style
	pad := bootGroundPad * s.w
	gw, gh := s.w+2*pad, s.h+2*pad
	th := int(math.Max(1, math.Round(bootGroundTexW*gh/gw)))
	prev := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	pix := bootGroundPixels(bootGroundTexW, th, (s.cx+pad)/gw, (s.cy+pad)/gh, gw/gh, seed^0x9e3779b9)
	s.ground = bootLiquidTexture(r.renderer, pix, bootGroundTexW, th)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prev)
	rng := seed
	s.growCracks(&rng)
	s.crackTex = r.crackTarget()
	s.sparks = s.makeSparks(&rng)
}

// crackTarget is the crack layer's render target, the whole screen cleared to
// transparent, or nil when the driver refuses one (the cracks are then drawn
// as lines).
func (r *bootOSRun) crackTarget() *sdl.Texture {
	if screenWidth <= 0 || screenHeight <= 0 {
		return nil
	}
	tex, err := r.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, screenWidth, screenHeight)
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

// bootCrackBirth is when a crack reaches a fraction u of the longest crack's
// length: fast at first, slowing as it spreads.
func bootCrackBirth(u float64) float64 {
	return bootCrackGrowMS * (1 - math.Sqrt(1-bootClamp01(u)))
}

// growCracks grows the crack network from under the logo: seven to nine
// cracks wandering out across the ground in every direction, branching now and
// then, in canvas coordinates, sorted by when each piece appears. A crack ends
// where it leaves the screen.
func (s *bootStyleRun) growCracks(rng *uint32) {
	maxLen := 0.46 * math.Max(s.w, s.h)
	if maxLen <= 0 {
		return
	}
	step := maxLen * 0.05
	segs := make([]bootCrackSeg, 0, bootCrackMaxSegs)
	var walk func(x, y, a, dist, length float64, depth int)
	walk = func(x, y, a, dist, length float64, depth int) {
		for left := length; left > 0 && len(segs) < bootCrackMaxSegs; {
			l := math.Min(left, step*(0.7+0.6*bootStyleRand(rng)))
			a += (bootStyleRand(rng) - 0.5) * 0.65
			nx, ny := x+math.Cos(a)*l, y+math.Sin(a)*l
			if nx < -l || ny < -l || nx > s.w+l || ny > s.h+l {
				return
			}
			dist += l
			width := uint8(1)
			if depth == 0 {
				width = 2
				if dist < 0.3*maxLen {
					width = 3
				}
			}
			segs = append(segs, bootCrackSeg{
				x0:    float32(x),
				y0:    float32(y),
				x1:    float32(nx),
				y1:    float32(ny),
				birth: float32(bootCrackBirth(dist / maxLen)),
				width: width,
			})
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
	n := 7 + int(bootStyleRand(rng)*3)
	// The cracks set out from just inside the tile's edge, so they come out
	// from under it.
	r0 := s.tile * 0.40
	for i := 0; i < n; i++ {
		a := (float64(i) + 0.6*bootStyleRand(rng)) * 2 * math.Pi / float64(n)
		walk(s.cx+math.Cos(a)*r0, s.cy+math.Sin(a)*r0, a, r0, maxLen*(0.5+0.5*bootStyleRand(rng)), 0)
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].birth < segs[j].birth })
	s.cracks = segs
}

// makeSparks scatters the impact's particles round the tile's edge: sparks
// flying out fast and dying quickly, and dust rolling out slower, spreading
// and lasting longer.
func (s *bootStyleRun) makeSparks(rng *uint32) []bootSpark {
	out := make([]bootSpark, 0, 40)
	reach := math.Max(s.w, s.h)
	for i := 0; i < 40; i++ {
		a := bootStyleRand(rng) * 2 * math.Pi
		r0 := s.tile * (0.36 + 0.14*bootStyleRand(rng))
		p := bootSpark{
			x:    s.cx + math.Cos(a)*r0,
			y:    s.cy + math.Sin(a)*r0,
			tint: bootStyleRand(rng),
			dust: i >= 24,
		}
		speed := reach * (0.45 + 0.75*bootStyleRand(rng))
		p.life = 330 + 330*bootStyleRand(rng)
		p.size = s.h * (0.010 + 0.014*bootStyleRand(rng))
		if p.dust {
			speed = reach * (0.10 + 0.22*bootStyleRand(rng))
			p.life = 700 + 450*bootStyleRand(rng)
			p.size = s.h * (0.035 + 0.04*bootStyleRand(rng))
		}
		p.vx, p.vy = math.Cos(a)*speed, math.Sin(a)*speed
		out = append(out, p)
	}
	return out
}

// bootNoise is a grid of random values read smoothly between its points
// (value noise): the ground's mottling.
type bootNoise struct {
	cols, rows int
	v          []float64
}

// bootNoiseGrid is noise cols cells across and as many down as keep its cells
// square on a rect aspect times wider than tall.
func bootNoiseGrid(cols int, aspect float64, rng *uint32) bootNoise {
	rows := int(math.Max(1, math.Round(float64(cols)/aspect)))
	g := bootNoise{cols: cols, rows: rows, v: make([]float64, (cols+1)*(rows+1))}
	for i := range g.v {
		g.v[i] = bootStyleRand(rng)
	}
	return g
}

// at is the noise at (u, v), in fractions of the rect.
func (g *bootNoise) at(u, v float64) float64 {
	gx, gy := bootClamp01(u)*float64(g.cols), bootClamp01(v)*float64(g.rows)
	x, y := min(int(gx), g.cols-1), min(int(gy), g.rows-1)
	tx, ty := bootSmoothstep(0, 1, gx-float64(x)), bootSmoothstep(0, 1, gy-float64(y))
	i := y*(g.cols+1) + x
	j := i + g.cols + 1
	top := g.v[i] + (g.v[i+1]-g.v[i])*tx
	bottom := g.v[j] + (g.v[j+1]-g.v[j])*tx
	return top + (bottom-top)*ty
}

// bootGroundPixels renders the ground seen from above, w×h: a pool of light
// round the point of impact at (fx, fy), in fractions of the texture, fading
// towards the edges, mottled like worn stone by two layers of noise drawn from
// seed, so no two boots have the same ground. aspect is the drawn rect's width
// over its height, which keeps the pool round on screen. White; the boot tints
// it.
func bootGroundPixels(w, h int, fx, fy, aspect float64, seed uint32) []byte {
	if w <= 0 || h <= 0 || aspect <= 0 {
		return nil
	}
	rng := seed | 1
	coarse := bootNoiseGrid(9, aspect, &rng)
	fine := bootNoiseGrid(23, aspect, &rng)
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		v := (float64(y) + 0.5) / float64(h)
		for x := 0; x < w; x++ {
			u := (float64(x) + 0.5) / float64(w)
			du, dv := (u-fx)*aspect/0.55, (v-fy)/0.55
			pool := 0.16 + 0.84*math.Exp(-(du*du+dv*dv))
			stone := 0.70 + 0.30*(0.65*coarse.at(u, v)+0.35*fine.at(u, v))
			bootPutPixel(pix, (y*w+x)*4, 255, 255, 255, pool*stone)
		}
	}
	return pix
}

// release destroys the style's textures. It runs on the render thread while
// their renderer is alive, with the screen as the target.
func (s *bootStyleRun) release() {
	for _, tex := range [...]*sdl.Texture{s.crackTex, s.ground, s.shadow, s.big} {
		if tex != nil {
			_ = tex.Destroy()
		}
	}
	s.crackTex, s.ground, s.shadow, s.big = nil, nil, nil, nil
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
	bootCrackLines(r.renderer, s.cracks[s.crackDone:n], 0, 0, sdl.Color{R: 255, G: 255, B: 255, A: 255}, 255, 130)
	if err := r.renderer.SetRenderTarget(nil); err != nil {
		logWarn("startup", "boot cracks could not release their target: %v", err)
	}
	_ = r.renderer.SetClipRect(nil)
	s.crackDone = n
}

// bootCrackLines draws crack pieces offset by (dx, dy) in c: a faint halo at
// halo alpha along both sides, then each piece at alpha, as many pixels wide
// as its width.
func bootCrackLines(renderer *sdl.Renderer, segs []bootCrackSeg, dx, dy float32, c sdl.Color, alpha, halo uint8) {
	line := func(g *bootCrackSeg, step int) {
		ax, ay := g.across()
		ox, oy := dx+ax*float32(step), dy+ay*float32(step)
		_ = renderer.DrawLineF(g.x0+ox, g.y0+oy, g.x1+ox, g.y1+oy)
	}
	if halo > 0 {
		_ = renderer.SetDrawColor(c.R, c.G, c.B, halo)
		for i := range segs {
			lo, hi := segs[i].span()
			line(&segs[i], lo-1)
			line(&segs[i], hi+1)
		}
	}
	_ = renderer.SetDrawColor(c.R, c.G, c.B, alpha)
	for i := range segs {
		lo, hi := segs[i].span()
		for step := lo; step <= hi; step++ {
			line(&segs[i], step)
		}
	}
}

// across is the one-pixel step across a crack piece that widens it: down for
// a piece running more across the screen than up it, right for one running
// more up, so a crack keeps its width whichever way it heads.
func (g *bootCrackSeg) across() (float32, float32) {
	if math.Abs(float64(g.x1-g.x0)) >= math.Abs(float64(g.y1-g.y0)) {
		return 0, 1
	}
	return 1, 0
}

// span is the steps across the piece that its pixels cover: one line, a
// second beside it, or one on either side.
func (g *bootCrackSeg) span() (lo, hi int) {
	switch {
	case g.width >= 3:
		return -1, 1
	case g.width == 2:
		return 0, 1
	}
	return 0, 0
}

// dropPose is the falling logo at ms: its size against its size at rest, how
// high it is (0 on the ground, 1 just under the screen, where it falls from)
// and how present it is.
func (s *bootStyleRun) dropPose(ms float64) (scale, high, alpha float64) {
	d := s.drop
	switch {
	case d == nil || s.high <= 0:
		return 1, 0, 1
	case ms < s.lift:
		// The firmware's logo is tossed up towards the screen, slowing as it
		// rises.
		p := ms / s.lift
		scale, high = s.dropHeight(s.high * (1 - (1-p)*(1-p)))
		return scale, high, 1
	case ms < s.impact:
		// It falls faster all the way down. Perspective would spread a plain
		// gravity fall into an even zoom; the steeper curve keeps the logo
		// looming under the screen, then brings it down fast.
		p := (ms - s.lift) / d.fall
		scale, high = s.dropHeight(s.high * (1 - p*p*p))
		alpha = 1
		if s.lift <= 0 {
			// Nothing was on screen: it appears where it starts.
			alpha = bootSmoothstep(0, d.appear, ms)
		}
		return scale, high, alpha
	}
	for i := range s.segs {
		g := &s.segs[i]
		if ms >= g.to {
			continue
		}
		q := bootClamp01((ms - g.from) / (g.to - g.from))
		if g.height > 0 {
			scale, high = s.dropHeight(g.height * 4 * q * (1 - q))
			return scale, high, 1
		}
		// Driven into the ground for a moment, then back to its place.
		return 1 - g.dip*math.Cos(math.Pi/2*q), 0, 1
	}
	return 1, 0, 1
}

// dropHeight is the logo's size against its size at rest, and its height
// against the height it falls from, when it is u of the way from the ground
// up to the screen: the camera looks down from the screen, so the logo looks
// 1/(1-u) times its resting size.
func (s *bootStyleRun) dropHeight(u float64) (scale, high float64) {
	u = math.Min(u, 0.95)
	return 1 / (1 - u), u / s.high
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

// drawStyleLayer is drawLayer for the animated styles. fade and zoom are the
// arrival's, as in drawLayer. It reports false when the style failed and the
// frame should be drawn classic.
func (r *bootOSRun) drawStyleLayer(f *bootOSFrame, fade, zoom float64) bool {
	return r.styleGuard("drawing", func() {
		fade = bootClamp01(fade)
		if fade <= 0 {
			return
		}
		light := glassLightTheme()
		if r.style.kind == bootStyleCinematic {
			r.drawDropLayer(f, fade, zoom, light)
		} else {
			r.drawRollLayer(f, fade, zoom, light)
		}
		if k := &bootOSKit; k.dot != nil {
			_ = k.dot.SetColorMod(255, 255, 255)
			_ = k.dot.SetAlphaMod(255)
			_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
		}
	})
}

// drawDropLayer paints the cinematic fall: the ground and its cracks, the
// impact's wave and dust, the logo's shadow, its halo, the trail of the
// falling logo and the logo itself, its rim once the classic beats light it,
// and the impact's sparks and flash. The whole picture shakes together.
func (r *bootOSRun) drawDropLayer(f *bootOSFrame, fade, zoom float64, light bool) {
	s := &r.style
	k := &bootOSKit
	ms := r.nowMS
	shx, shy := s.shake(ms)
	sc, high, alpha := s.dropPose(ms)
	x, y := s.cx+shx, s.cy+shy
	scene := s.drop != nil && s.drop.scene
	if scene {
		r.drawCineGround(ms, fade, shx, shy, light)
		r.drawWave(ms, fade, zoom, shx, shy)
		r.drawDust(ms, fade, shx, shy, light)
	}
	r.drawDropShadow(x, y, high, alpha*fade, zoom, light)
	if halo := bootClamp01(f.halo) * fade; halo > 0 && k.dot != nil {
		r.drawHalo(x, y, s.side*zoom*sc, halo)
	}
	r.drawDropTrail(ms, x, y, sc, fade, zoom)
	r.drawLogoPose(x, y, sc, sc, 0, alpha*fade, zoom)
	if rim := bootClamp01(f.rim) * fade; rim > 0 && k.ring != nil {
		r.drawRim(x, y, zoom*sc, rim)
	}
	if scene {
		r.drawSparks(ms, fade, shx, shy, light)
		r.drawFlash(ms, fade, shx, shy, light)
	}
}

// drawRollLayer paints the roll: the line of light along the floor, the
// contact shadow, the halo, the firmware's logo fading under the tile as it
// comes in, the tile as it rolls, and its rim once it is upright.
func (r *bootOSRun) drawRollLayer(f *bootOSFrame, fade, zoom float64, light bool) {
	s := &r.style
	k := &bootOSKit
	ms := r.nowMS
	x, y, angle, sx, sy, alpha := s.rollPose(ms)
	r.drawRollGround(ms, fade, light)
	// Squash and stretch keep the tile's bottom on the floor.
	y += s.tile / 2 * (1 - sy)
	if k.dot != nil {
		// The contact shadow darkens and tightens as the logo nears the floor.
		gap := math.Max(0, s.floorY-(y+s.tile/2))
		near := 1 - bootClamp01(gap/(0.6*s.h))
		peak := 150.0
		if light {
			peak = 70
		}
		_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
		r.drawDotWH(x, s.floorY, s.tile*(0.8+0.5*near)*zoom, s.tile*0.16*zoom, sdl.Color{A: 255}, peak*near*near*alpha*fade)
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
}

// drawDropShadow paints the falling logo's shadow on the ground under (x, y):
// the tile's soft shape, cast by a light above the top-left, so it lies down
// and to the right of the logo, larger and fainter, by more the higher the
// logo is (high, 0 on the ground to 1 where it falls from), and slides in
// under it, dark and tight, as it lands. alpha is the logo's presence.
func (r *bootOSRun) drawDropShadow(x, y, high, alpha, zoom float64, light bool) {
	s := &r.style
	if s.shadow == nil {
		return
	}
	up := bootClamp01(high)
	a := 255 * (1 - up) * (1 - up) * bootClamp01(alpha)
	if light {
		a *= 0.5
	}
	if a < 1 {
		return
	}
	off := (0.05 + 0.30*up) * s.tile * zoom
	d := s.tile * (1 + 0.55*up) * zoom * bootDropShadowTex / bootDropShadowTile
	_ = s.shadow.SetAlphaMod(uint8(math.Min(255, math.Round(a))))
	bootOSF = sdl.FRect{X: float32(x + off - d/2), Y: float32(y + off - d/2), W: float32(d), H: float32(d)}
	_ = r.renderer.CopyF(s.shadow, nil, &bootOSF)
	_ = s.shadow.SetAlphaMod(255)
}

// drawDropTrail paints where the falling logo was a moment ago, larger and
// fainter behind it, so its speed reads as a blur towards the screen. Only the
// fall leaves one: the toss slows to a stop and the bounces are small.
func (r *bootOSRun) drawDropTrail(ms, x, y, sc, fade, zoom float64) {
	s := &r.style
	if ms < s.lift || ms >= s.impact {
		return
	}
	for i := len(bootDropTrail); i >= 1; i-- {
		at := ms - float64(i)*bootDropTrailMS
		if at < s.lift {
			continue
		}
		was, _, alpha := s.dropPose(at)
		if was <= sc*1.01 {
			continue
		}
		speed := math.Min(1, (was/sc-1)/0.06)
		// A copy too faint to see would still cost its pixels.
		if a := alpha * fade * speed * bootDropTrail[i-1]; a >= 0.02 {
			r.drawLogoPose(x, y, was, was, 0, a, zoom)
		}
	}
}

// drawLogoPose draws the logo artwork centred on (x, y), scaled sx across and
// sy up the screen, turned angle degrees clockwise, at alpha, grown by zoom.
func (r *bootOSRun) drawLogoPose(x, y, sx, sy, angle, alpha, zoom float64) {
	alpha = bootClamp01(alpha)
	if alpha <= 0 {
		return
	}
	side := r.style.side * zoom
	w, h := side*sx, side*sy
	tex := brandLogoTexture(r.renderer, r.layout.Sprite.W)
	if big := r.style.big; big != nil && math.Max(w, h) > bootDropBigFrom*r.style.side {
		// Far bigger than at rest: the artwork's own pixels, not the resting
		// size's blown up.
		tex = big
	}
	if tex == nil {
		return
	}
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

// drawCineGround paints the ground and its cracks, shaken by (shx, shy).
func (r *bootOSRun) drawCineGround(ms, fade, shx, shy float64, light bool) {
	s := &r.style
	p := bootSmoothstep(0, 450, ms) * fade
	if p <= 0 {
		return
	}
	if s.ground != nil {
		tint := glassMix(sdl.Color{R: 52, G: 58, B: 76, A: 255}, glassAccent(), 0.30)
		a := 235.0
		if light {
			tint, a = sdl.Color{R: 20, G: 24, B: 34, A: 255}, 70
		}
		pad := bootGroundPad * s.w
		_ = s.ground.SetColorMod(tint.R, tint.G, tint.B)
		_ = s.ground.SetAlphaMod(uint8(math.Round(a * p)))
		bootOSF = sdl.FRect{X: float32(shx - pad), Y: float32(shy - pad), W: float32(s.w + 2*pad), H: float32(s.h + 2*pad)}
		_ = r.renderer.CopyF(s.ground, nil, &bootOSF)
		_ = s.ground.SetColorMod(255, 255, 255)
		_ = s.ground.SetAlphaMod(255)
	}
	r.drawCracks(ms, p, shx, shy, light)
}

// drawWave paints the impact's wave: the pane's rim of light thrown out along
// the ground from the tile's edge, widening and fading in a third of a second.
func (r *bootOSRun) drawWave(ms, fade, zoom, shx, shy float64) {
	s := &r.style
	t := ms - s.impact
	if t < 0 || t >= bootWaveMS || bootOSKit.ring == nil || s.side <= 0 {
		return
	}
	q := t / bootWaveMS
	// The rim's light runs round the pane; the wave sets off from the tile's
	// edge, further out.
	from := s.tile / (s.side * (1 - 2*bootLogoPaneInset))
	grow := from + (bootWaveReach-from)*(1-(1-q)*(1-q))
	r.drawRim(s.cx+shx, s.cy+shy, grow*zoom, (1-q)*(1-q)*fade)
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
		bootOSF = sdl.FRect{X: float32(shx), Y: float32(shy), W: float32(screenWidth), H: float32(screenHeight)}
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

// drawDust paints the impact's dust at ms, on the ground under the logo: puffs
// rolling out from under the tile's edge, slowing, spreading and thinning.
func (r *bootOSRun) drawDust(ms, fade, shx, shy float64, light bool) {
	s := &r.style
	k := &bootOSKit
	t := ms - s.impact
	if t < 0 || t > 1200 || k.dot == nil {
		return
	}
	col := glassMix(sdl.Color{R: 150, G: 152, B: 166, A: 255}, glassAccent(), 0.18)
	if light {
		col = sdl.Color{R: 90, G: 92, B: 104, A: 255}
	}
	_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
	// The fraction of a second's velocity covered so far, slowing to a stop.
	roll := 0.35 * (1 - math.Exp(-t/350))
	for i := range s.sparks {
		p := &s.sparks[i]
		u := t / p.life
		if !p.dust || u >= 1 {
			continue
		}
		r.drawDot(p.x+p.vx*roll+shx, p.y+p.vy*roll+shy, p.size*(1+1.6*u), col, 110*(1-u)*(1-u)*fade)
	}
}

// drawSparks paints the impact's sparks at ms, over the logo: thrown out from
// under the tile's edge and up towards the screen, so each grows as it rises
// and shrinks as it falls back, slowing and dying out.
func (r *bootOSRun) drawSparks(ms, fade, shx, shy float64, light bool) {
	s := &r.style
	k := &bootOSKit
	t := ms - s.impact
	if t < 0 || t > 1200 || k.dot == nil {
		return
	}
	accent := glassAccent()
	white := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	_ = k.dot.SetBlendMode(bootStyleLightMode(light))
	fly := 0.22 * (1 - math.Exp(-t/220))
	for i := range s.sparks {
		p := &s.sparks[i]
		u := t / p.life
		if p.dust || u >= 1 {
			continue
		}
		col := glassMix(white, accent, 0.35+0.5*p.tint)
		if light {
			col = glassMix(accent, sdl.Color{A: 255}, 0.2)
		}
		size := p.size * (1 + 5.2*u*(1-u)) * (1 - 0.4*u)
		r.drawDot(p.x+p.vx*fly+shx, p.y+p.vy*fly+shy, size, col, 255*math.Pow(1-u, 1.3)*fade)
	}
	_ = k.dot.SetBlendMode(sdl.BLENDMODE_BLEND)
}

// drawFlash paints the impact's light: a burst of the accent where the logo
// hit and, on a dark screen, a brief brightening of the whole picture.
func (r *bootOSRun) drawFlash(ms, fade, shx, shy float64, light bool) {
	s := &r.style
	k := &bootOSKit
	t := ms - s.impact
	if t < 0 || t > 260 {
		return
	}
	e := math.Exp(-t/60) * fade
	if k.dot != nil {
		_ = k.dot.SetBlendMode(bootStyleLightMode(light))
		d := s.tile * 3.2
		r.drawDotWH(s.cx+shx, s.cy+shy, d, d, glassAccent(), 230*e)
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
