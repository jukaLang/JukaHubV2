package main

import (
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Boot splash: a PS2-style starfield warp
// ──────────────────────────────────────────────────────────────────────
//
// The startup screen is the first thing anyone sees, so it is choreographed
// like a console boot sequence instead of a spinner:
//
//	 0.0s – 1.1s   stars rush out of the vanishing point (warp-in)
//	 0.4s – 1.4s   the orbital star ring unrolls around a rising glow
//	 1.0s – 1.9s   "SYSTEM SOFTWARE" and the wordmark arrive letter by letter
//	 2.0s – 2.6s   a metallic shine sweeps across the letters
//	 2.7s – 3.6s   hyperspace flash, the field streaks and hands over to the UI
//
// Everything is drawn with SDL primitives and preallocated arrays: the splash
// also runs on the 1 GB TrimUI Smart Pro, so a frame must not allocate or flood
// the renderer with tens of thousands of draw calls.

const (
	splashDuration = 3600 // total splash duration in ms
	splashStars    = 320  // warp starfield size
	splashRingN    = 84   // stars on the orbital ring
	splashFadeMS   = 260  // soft fade at the very end
)

// splash timeline keyframes in milliseconds.
const (
	splashWarpInEnd   = 1100
	splashRingIn      = 400
	splashRingInEnd   = 1400
	splashKickerIn    = 950
	splashKickerEnd   = 1280
	splashLogoStart   = 1000
	splashLogoEnd     = 1900
	splashUnderlineIn = 1400
	splashUnderlineTo = 1900
	splashTaglineIn   = 1800
	splashTaglineEnd  = 2200
	splashShineStart  = 2050
	splashShineEnd    = 2620
	splashWarpOut     = 2700
	splashFlashStart  = 2760
	splashFlashEnd    = 3060
)

type splashStar struct {
	x, y float64 // normalized -1..1 position in the field
	z    float64 // depth, 1 = far, 0 = at the camera
	kind uint8   // 0 = white, 1 = cyan, 2 = violet
}

type splashRingPoint struct {
	angle float64 // base angle on the ring
	size  float64
}

type splashState struct {
	stars       []splashStar
	ring        []splashRingPoint
	startTick   uint64
	lastElapsed float64
	dt          float64
	inited      bool
}

var splash splashState

// splashWordmarkFont is opened once at 92px: the wordmark is the hero of the
// boot screen and the generic "big" entry (52px) is too small to carry it.
var splashWordmarkFont *ttf.Font

func splashGetWordmarkFont(config *Config) *ttf.Font {
	if splashWordmarkFont != nil {
		return splashWordmarkFont
	}
	path := "Inter-Regular.ttf"
	if config != nil {
		for name, p := range config.Variables.Fonts {
			if strings.EqualFold(name, "big") && strings.TrimSpace(p) != "" {
				path = p
				break
			}
		}
	}
	f, err := ttf.OpenFont(resolvePath(path), 92)
	if err != nil {
		// Fall back to the configured font so a missing asset still boots.
		f, _ = getCachedFont(config, "big")
		return f
	}
	splashWordmarkFont = f
	return f
}

// splashEase is a cubic ease-out used for entrances.
func splashEase(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	u := 1 - t
	return 1 - u*u*u
}

func splashEaseIn(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t
}

// splashClamp01 keeps timeline math from overshooting.
func splashClamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// splashQuantAlpha snaps animated alphas to 16-value steps. renderText caches
// textures per colour, so a continuously fading logo would otherwise create a
// new texture for every frame and evict the whole cache.
func splashQuantAlpha(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(int(v/16) * 16)
}

// initSplash builds the star field and ring once per splash.
func initSplash() {
	splash.stars = make([]splashStar, splashStars)
	for i := range splash.stars {
		s := &splash.stars[i]
		s.x = rand.Float64()*2 - 1
		s.y = rand.Float64()*2 - 1
		// Spread the initial depths so the field is full on the first frame.
		s.z = 0.05 + rand.Float64()*0.95
		switch {
		case rand.Float64() < 0.62:
			s.kind = 0
		case rand.Float64() < 0.80:
			s.kind = 1
		default:
			s.kind = 2
		}
	}

	splash.ring = make([]splashRingPoint, splashRingN)
	for i := range splash.ring {
		p := &splash.ring[i]
		p.angle = float64(i) * 2 * math.Pi / float64(splashRingN)
		p.size = 1.2 + rand.Float64()*1.6
	}

	splash.startTick = sdl.GetTicks64()
	splash.lastElapsed = 0
	splash.dt = 16
	splash.inited = true
}

// splashWarpFactor is the star speed multiplier across the sequence: a quick
// jump to hyperspace, a cruise while the logo appears, then a final burst.
func splashWarpFactor(elapsed float64) float64 {
	switch {
	case elapsed < splashWarpInEnd:
		// Accelerate out of the "jump" (dramatic start).
		return 0.45 + 2.6*splashEaseIn(splashClamp01(elapsed/splashWarpInEnd))
	case elapsed < splashWarpOut:
		// Cruise, slowly settling so the logo reads clearly.
		t := splashClamp01((elapsed - splashWarpInEnd) / (splashWarpOut - splashWarpInEnd))
		return 3.05 - 1.85*t
	default:
		// Hyperspace jump into the app.
		t := splashClamp01((elapsed - splashWarpOut) / (splashDuration - splashWarpOut))
		return 1.2 + 4.4*t*t
	}
}

func splashStarColor(kind uint8, bright float64) (uint8, uint8, uint8) {
	switch kind {
	case 1: // cyan
		return uint8(140 * bright), uint8(215 * bright), uint8(255 * bright)
	case 2: // violet
		return uint8(190 * bright), uint8(160 * bright), uint8(255 * bright)
	default:
		return uint8(215 * bright), uint8(230 * bright), uint8(255 * bright)
	}
}

// splashGlow paints a soft radial haze as a stack of translucent discs. Each
// disc alone has a hard edge, but at these alphas the six steps are invisible
// and the result reads as a smooth core light — much cheaper than a per-pixel
// gradient on the device's software renderer.
func splashGlow(renderer *sdl.Renderer, cx, cy, rad int32, red, green, blue, peak uint8) {
	if rad <= 0 || peak == 0 {
		return
	}
	const layers = 6
	per := peak / layers
	if per == 0 {
		per = 1
	}
	for i := layers; i >= 1; i-- {
		rr := rad * int32(i) / layers
		// The outermost disc is the dimmest so the falloff is visible.
		a := uint8(int(per) * (layers - i + 2) / (layers + 1))
		if a == 0 {
			a = 1
		}
		fillCircle(renderer, cx, cy, rr, sdl.Color{R: red, G: green, B: blue, A: a})
	}
}

// splashDrawBackdrop paints the vertical gradient, glow, light beams and
// vignette.
func splashDrawBackdrop(renderer *sdl.Renderer, elapsed float64) {
	// Deep-space vertical gradient.
	const bands = 28
	top := [3]float64{11, 16, 32}
	bottom := [3]float64{3, 4, 9}
	for i := 0; i < bands; i++ {
		t := float64(i) / float64(bands)
		renderer.SetDrawColor(
			uint8(top[0]+(bottom[0]-top[0])*t),
			uint8(top[1]+(bottom[1]-top[1])*t),
			uint8(top[2]+(bottom[2]-top[2])*t),
			255,
		)
		h := screenHeight/int32(bands) + 1
		renderer.FillRect(&sdl.Rect{X: 0, Y: int32(i) * screenHeight / int32(bands), W: screenWidth, H: h})
	}

	// Slow diagonal light beams sweeping behind the starfield.
	drift := math.Sin(elapsed*0.00045) * 90
	for b := 0; b < 2; b++ {
		baseX := float64(screenWidth)*0.28 + drift + float64(b)*float64(screenWidth)*0.42
		for i := 0; i < 3; i++ {
			alpha := uint8(9 - i*3)
			renderer.SetDrawColor(90, 150, 230, alpha)
			renderer.DrawLine(int32(baseX)+int32(i*3), 0,
				int32(baseX)+int32(i*3)-int32(screenHeight)/3, screenHeight)
		}
	}

	// Core light: grows as the ring unrolls, peaks while the logo reads.
	core := splashEase(splashClamp01((elapsed - 300) / 1100))
	if core > 0 {
		pulse := 1 + 0.06*math.Sin(elapsed*0.0028)
		cy := screenHeight/2 - 30
		splashGlow(renderer, screenWidth/2, cy, int32(310*pulse), 80, 145, 235, uint8(30*core))
		splashGlow(renderer, screenWidth/2, cy, int32(130*pulse), 120, 185, 250, uint8(26*core))
	}

	// Vignette: darken the top and bottom edges so the centre glows.
	const steps = 14
	for i := 1; i <= steps; i++ {
		a := uint8(float64(70) * float64(steps-i) / float64(steps))
		h := int32(float64(screenHeight) * 0.30 * float64(i) / float64(steps))
		renderer.SetDrawColor(0, 0, 0, a)
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: h})
		renderer.FillRect(&sdl.Rect{X: 0, Y: screenHeight - h, W: screenWidth, H: h})
	}
}

// splashDrawGrid draws a receding perspective grid that drifts toward the
// viewer — the nod to the PS2 boot horizon.
func splashDrawGrid(renderer *sdl.Renderer, elapsed float64) {
	horizon := float64(screenHeight) * 0.64
	cx := float64(screenWidth) / 2

	// Converging verticals.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for i := -8; i <= 8; i++ {
		f := float64(i) / 8
		renderer.SetDrawColor(70, 130, 200, 22)
		renderer.DrawLine(int32(cx+f*30), int32(horizon),
			int32(cx+f*float64(screenWidth)*0.92), screenHeight)
	}

	// Rows sliding toward the camera with perspective compression.
	const rows = 11
	phase := math.Mod(elapsed*0.00022, 1.0)
	for i := 0; i < rows; i++ {
		t := math.Mod(phase+float64(i)/rows, 1.0)
		y := horizon + (float64(screenHeight)-horizon)*t*t*t
		a := uint8(6 + 20*(1-t))
		renderer.SetDrawColor(80, 150, 220, a)
		renderer.DrawLine(0, int32(y), screenWidth, int32(y))
	}

	// Warm horizon line where the grid meets the void.
	renderer.SetDrawColor(110, 180, 245, 38)
	renderer.FillRect(&sdl.Rect{X: 0, Y: int32(horizon) - 1, W: screenWidth, H: 1})
}

// splashDrawStars renders the warp field with motion streaks.
func splashDrawStars(renderer *sdl.Renderer, elapsed float64) {
	cx := float64(screenWidth) / 2
	cy := float64(screenHeight) / 2
	spreadX := float64(screenWidth) * 0.55
	spreadY := float64(screenHeight) * 0.55
	warp := splashWarpFactor(elapsed)

	// Dim the field slightly while the wordmark is centre stage.
	global := 1.0
	if elapsed > splashLogoStart && elapsed < splashLogoEnd {
		global = 1.0 - 0.22*splashClamp01((elapsed-splashLogoStart)/(splashLogoEnd-splashLogoStart))
	}

	for i := range splash.stars {
		s := &splash.stars[i]
		s.z -= 0.00034 * warp * splash.dt
		if s.z < 0.02 {
			s.x = rand.Float64()*2 - 1
			s.y = rand.Float64()*2 - 1
			s.z = 1
			continue
		}

		px := cx + s.x/s.z*spreadX
		py := cy + s.y/s.z*spreadY
		margin := 80.0
		if px < -margin || px > float64(screenWidth)+margin || py < -margin || py > float64(screenHeight)+margin {
			continue
		}

		bright := splashClamp01((1.0 - s.z) * 1.15)
		// Gentle twinkle keeps the field alive while the logo holds.
		bright *= 0.86 + 0.14*math.Sin(elapsed*0.004+float64(i))
		bright *= global
		if bright < 0.04 {
			continue
		}
		r, g, b := splashStarColor(s.kind, bright)

		// Trail: project the same star a little further away.
		zTail := s.z + 0.030*warp
		tx := cx + s.x/zTail*spreadX
		ty := cy + s.y/zTail*spreadY
		renderer.SetDrawColor(r, g, b, uint8(50+200*bright))
		renderer.DrawLine(int32(tx), int32(ty), int32(px), int32(py))

		// Every star keeps a point of light so the field reads as a sky, not
		// only as streaks.
		renderer.SetDrawColor(235, 245, 255, uint8(35+150*bright))
		renderer.DrawPoint(int32(px), int32(py))
		if s.z < 0.22 {
			// Bright cross for the nearest stars.
			renderer.SetDrawColor(245, 250, 255, uint8(70+120*bright))
			renderer.DrawPoint(int32(px)-1, int32(py))
			renderer.DrawPoint(int32(px)+1, int32(py))
			renderer.DrawPoint(int32(px), int32(py)-1)
			renderer.DrawPoint(int32(px), int32(py)+1)
		}
	}
}

// splashDrawRing renders the tilted orbital ring of stars around the logo.
func splashDrawRing(renderer *sdl.Renderer, elapsed float64) {
	enter := splashEase(splashClamp01((elapsed - splashRingIn) / (splashRingInEnd - splashRingIn)))
	if enter <= 0 {
		return
	}
	cx := float64(screenWidth) / 2
	cy := float64(screenHeight)/2 - 30
	radius := (220 + 120*enter) * (1 + 0.018*math.Sin(elapsed*0.003))
	spin := elapsed * 0.00075
	tilt := 0.165

	// A single faint ring outline anchors the orbit.
	renderer.SetDrawColor(90, 150, 230, uint8(40*enter))
	steps := 96
	for i := 0; i < steps; i++ {
		a0 := float64(i) * 2 * math.Pi / float64(steps)
		a1 := float64(i+1) * 2 * math.Pi / float64(steps)
		renderer.DrawLine(
			int32(cx+math.Cos(a0)*radius), int32(cy+math.Sin(a0)*radius*tilt),
			int32(cx+math.Cos(a1)*radius), int32(cy+math.Sin(a1)*radius*tilt),
		)
	}

	for i := range splash.ring {
		p := &splash.ring[i]
		a := p.angle + spin
		// Points nearest the viewer (sin > 0) are brighter and larger.
		depth := (math.Sin(a) + 1) / 2
		x := cx + math.Cos(a)*radius
		y := cy + math.Sin(a)*radius*tilt
		alpha := uint8((50 + 170*depth) * enter)
		size := int32(p.size * (0.7 + 0.9*depth))
		if size < 1 {
			size = 1
		}
		// Trail behind the point along the orbit.
		ta := a - 0.055
		renderer.SetDrawColor(120, 190, 255, alpha/2)
		renderer.DrawLine(
			int32(cx+math.Cos(ta)*radius), int32(cy+math.Sin(ta)*radius*tilt),
			int32(x), int32(y),
		)
		fillCircle(renderer, int32(x), int32(y), size, sdl.Color{R: 200, G: 225, B: 255, A: alpha})
	}
}

// splashDrawWordmark renders the kicker, per-letter logo entrance, the
// underline, the tagline and the metallic shine sweep.
func splashDrawWordmark(renderer *sdl.Renderer, config *Config, elapsed float64) {
	font := splashGetWordmarkFont(config)
	if font == nil {
		return
	}
	small, _ := getCachedFont(config, "small")

	const word = "JukaHub"
	runes := []rune(word)

	// Measure the whole word so the layout never jitters while letters arrive.
	totalW, lh, _ := font.SizeUTF8(word)
	baseX := (screenWidth - int32(totalW)) / 2
	baseY := screenHeight*47/100 - int32(lh)/2

	// Kicker: the "system software" line of a console boot.
	if small != nil {
		k := splashEase(splashClamp01((elapsed - splashKickerIn) / (splashKickerEnd - splashKickerIn)))
		if k > 0 {
			kicker := "S Y S T E M   S O F T W A R E"
			kw, _, _ := small.SizeUTF8(kicker)
			renderText(renderer, config, small, kicker,
				sdl.Color{R: 120, G: 165, B: 215, A: splashQuantAlpha(170 * k)},
				(screenWidth-int32(kw))/2, baseY-40)
		}
	}

	// Reveal window per letter with a short stagger.
	const stagger = 55.0
	letterReveal := func(i int) float64 {
		start := float64(splashLogoStart) + float64(i)*stagger
		return splashEase(splashClamp01((elapsed - start) / (splashLogoEnd - start)))
	}

	// Underline first (behind the letters) so it reads as a stage light.
	under := splashEase(splashClamp01((elapsed - splashUnderlineIn) / (splashUnderlineTo - splashUnderlineIn)))
	if under > 0 {
		w := int32(float64(totalW+64) * under)
		x0 := screenWidth/2 - w/2
		y := baseY + int32(lh) + 28
		// Wide halo, then the bright filament.
		renderer.SetDrawColor(90, 170, 240, uint8(26*under))
		renderer.FillRect(&sdl.Rect{X: x0, Y: y - 3, W: w, H: 9})
		renderer.SetDrawColor(130, 205, 255, uint8(150*under))
		renderer.FillRect(&sdl.Rect{X: x0, Y: y, W: w, H: 2})
		renderer.SetDrawColor(215, 240, 255, uint8(210*under))
		renderer.FillRect(&sdl.Rect{X: screenWidth/2 - w/4, Y: y, W: w / 2, H: 2})
	}

	// Letters rise into place one by one.
	x := baseX
	for i, r := range runes {
		t := letterReveal(i)
		rw, _, _ := font.SizeUTF8(string(r))
		if t > 0 {
			rise := int32((1 - t) * 28)
			alpha := splashQuantAlpha(255 * t)
			// Depth pass keeps the letters readable over the stars.
			renderText(renderer, config, font, string(r),
				sdl.Color{R: 12, G: 22, B: 40, A: splashQuantAlpha(float64(alpha) * 0.7)},
				x+2, baseY+rise+3)
			renderText(renderer, config, font, string(r),
				sdl.Color{R: 224, G: 238, B: 255, A: alpha}, x, baseY+rise)
		}
		x += int32(rw)
	}

	// Metallic shine: redraw the word in near-white through a moving clip band.
	shine := splashClamp01((elapsed - splashShineStart) / (splashShineEnd - splashShineStart))
	if shine > 0 && shine < 1 && elapsed > splashLogoEnd-260 {
		bandW := int32(120)
		travel := float64(totalW) + float64(bandW) + 140
		bx := baseX - bandW - 70 + int32(travel*shine)
		clip := sdl.Rect{X: bx, Y: 0, W: bandW, H: screenHeight}
		renderer.SetClipRect(&clip)
		renderText(renderer, config, font, word, sdl.Color{R: 255, G: 255, B: 255, A: 245}, baseX, baseY)
		renderer.SetClipRect(nil)
	}

	// Tagline sits clear of the ring's lower arc.
	if small != nil {
		tag := splashEase(splashClamp01((elapsed - splashTaglineIn) / (splashTaglineEnd - splashTaglineIn)))
		if tag > 0 {
			tagline := "YOUR DEVICE. YOUR WAY."
			tw, _, _ := small.SizeUTF8(tagline)
			tagCol := sdl.Color{R: 178, G: 205, B: 238, A: splashQuantAlpha(225 * tag)}
			renderText(renderer, config, small, tagline, tagCol,
				(screenWidth-int32(tw))/2, baseY+int32(lh)+72)
		}
	}
}

// splashDrawProgress draws the boot progress line, loading label and version.
func splashDrawProgress(renderer *sdl.Renderer, config *Config, elapsed float64) {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	progress := splashEase(splashClamp01(elapsed / (splashDuration - 200)))

	const margin = 140
	trackW := screenWidth - margin*2
	trackY := screenHeight - 64

	renderer.SetDrawColor(70, 105, 150, 80)
	renderer.FillRect(&sdl.Rect{X: margin, Y: trackY, W: trackW, H: 2})

	fillW := int32(float64(trackW) * progress)
	if fillW > 0 {
		renderer.SetDrawColor(120, 195, 255, 215)
		renderer.FillRect(&sdl.Rect{X: margin, Y: trackY - 1, W: fillW, H: 4})
		// Comet head with a soft halo.
		hx := margin + fillW
		fillCircle(renderer, hx, trackY+1, 7, sdl.Color{R: 120, G: 195, B: 255, A: 40})
		fillCircle(renderer, hx, trackY+1, 3, sdl.Color{R: 225, G: 242, B: 255, A: 235})
	}

	// "LOADING ···" on the left, product version on the right.
	dots := int(elapsed/280) % 4
	label := "LOADING" + strings.Repeat(".", dots)
	renderText(renderer, config, font, label, sdl.Color{R: 130, G: 160, B: 195, A: 200}, margin, trackY+12)

	ver := versionDisplayString(config)
	vw, _, _ := font.SizeUTF8(ver)
	renderText(renderer, config, font, ver, sdl.Color{R: 95, G: 115, B: 145, A: 180}, screenWidth-margin-int32(vw), trackY+12)

	// Small "press any key to skip" affordance keeps the boot from feeling long.
	hint := "PRESS ANY KEY TO SKIP"
	hw, _, _ := font.SizeUTF8(hint)
	hintAlpha := splashQuantAlpha(96 + 26*math.Sin(elapsed*0.004))
	renderText(renderer, config, font, hint, sdl.Color{R: 110, G: 140, B: 175, A: hintAlpha},
		(screenWidth-int32(hw))/2, trackY+34)
}

// renderSplash draws one frame of the splash animation. Returns true while the
// splash is still active; false when it's done and the caller should proceed to
// the main UI.
func renderSplash(renderer *sdl.Renderer, config *Config, tick uint64) bool {
	if !splash.inited {
		initSplash()
	}

	elapsed := tick - splash.startTick
	if elapsed >= uint64(splashDuration) {
		splash.inited = false
		return false
	}
	e := float64(elapsed)

	// Frame delta drives the star warp; clamp so a stalled frame cannot fling
	// the whole field past the camera at once.
	delta := e - splash.lastElapsed
	if delta < 1 || delta > 40 {
		delta = 16
	}
	splash.dt = delta
	splash.lastElapsed = e

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)

	splashDrawBackdrop(renderer, e)
	splashDrawGrid(renderer, e)
	splashDrawStars(renderer, e)
	splashDrawRing(renderer, e)
	splashDrawWordmark(renderer, config, e)
	splashDrawProgress(renderer, config, e)

	// Hyperspace flash as the field streaks past on the way out. Kept subtle:
	// a full-screen white wash hides the logo instead of selling the jump.
	if e > splashFlashStart && e < splashFlashEnd {
		t := (e - splashFlashStart) / (splashFlashEnd - splashFlashStart)
		a := uint8(42 * math.Sin(math.Pi*t))
		renderer.SetDrawColor(150, 195, 255, a)
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	}

	// Soft fade to black so the UI does not pop in mid-flash.
	if e > float64(splashDuration-splashFadeMS) {
		t := (e - float64(splashDuration-splashFadeMS)) / float64(splashFadeMS)
		renderer.SetDrawColor(4, 5, 10, uint8(235*splashClamp01(t)))
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	}

	return true
}

// drawParticle draws a small filled circle using horizontal lines.
func drawParticle(renderer *sdl.Renderer, cx, cy, r int32, red, green, blue, alpha uint8) {
	if r <= 0 || alpha == 0 {
		return
	}
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for dy := -r; dy <= r; dy++ {
		dx := int32(math.Sqrt(float64(r*r - dy*dy)))
		// Apply alpha falloff toward edges.
		edgeFade := 1.0 - float64(abs(int(dy)))/float64(r)
		a := uint8(float64(alpha) * edgeFade * edgeFade)
		renderer.SetDrawColor(red, green, blue, a)
		renderer.DrawLine(cx-dx, cy+dy, cx+dx, cy+dy)
	}
}

// ShowSplash plays the boot sequence for the time the boot budget left over.
// The animation's own timeline is compressed into that window, so a short run
// still shows the whole arc instead of a frozen frame, and a zero allotment
// opens the UI immediately. Any key press or click skips the rest of it.
func ShowSplash(renderer *sdl.Renderer, config *Config, allotted time.Duration) {
	if allotted <= 0 {
		return
	}
	initSplash()
	start := time.Now()
	for {
		elapsed := time.Since(start).Milliseconds()
		if elapsed >= allotted.Milliseconds() {
			break
		}

		// Skip on any input so a boot animation never delays a returning user.
		// Quit requests are pushed back so closing the window during the
		// splash still exits the app instead of being swallowed here.
		for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
			switch ev.(type) {
			case *sdl.QuitEvent:
				sdl.PushEvent(ev)
			case *sdl.KeyboardEvent, *sdl.MouseButtonEvent, *sdl.ControllerButtonEvent, *sdl.TouchFingerEvent:
				splash.inited = false
				return
			}
		}

		// Map the wall time onto the designed timeline so a 400 ms run is the
		// same animation played fast rather than a truncated one.
		scaled := elapsed * int64(splashDuration) / allotted.Milliseconds()
		tick := uint64(scaled) + splash.startTick
		renderSplash(renderer, config, tick)
		renderer.Present()
		sdl.Delay(16) // ~60fps
	}
	splash.inited = false
}
