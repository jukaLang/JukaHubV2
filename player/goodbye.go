package main

import (
	"math"
	"math/rand"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Goodbye sequence: a warm "power down" that mirrors the boot splash
// ──────────────────────────────────────────────────────────────────────
//
// The startup splash (splash.go) warps a starfield OUT of the vanishing
// point. The exit sequence is its cooler mirror image: the field IMPLODES,
// every star rushing inward and dying at the centre, while a warm "GOODBYE"
// wordmark rises letter by letter above a contracting orbital ring. Sparks
// drift upward the whole time, and the sequence ends with a single bright
// bloom where the sky collapsed.
//
// The timeline (ms):
//
//	 250 – 1350   the orbital ring unrolls, then contracts toward centre
//	 350 – 1000   "POWERING DOWN" kicker fades up
//	 550 – 1650   "GOODBYE" arrives letter by letter in warm gold
//	1250 – 1800   an ember underline sweeps open beneath the word
//	1650 – 2150   "SEE YOU NEXT TIME" fades in
//	2150 – 2700   a metallic shine sweeps across the letters
//	2550 – 3400   the collapse accelerates into a point of light
//	2950 – 3250   final warm bloom flash
//	3060 – 3400   fade to black
//
// Like the splash, everything is drawn with SDL primitives and preallocated
// arrays so a frame never allocates: the exit also runs on the 1 GB TrimUI
// Smart Pro. Alphas are quantised via splashQuantAlpha to keep the text
// texture cache small.
//
// The goodbye plays regardless of the reduced-motion setting: it is a
// deliberate, one-time farewell rather than an idle flourish, and it is the
// single most characterful moment of the shutdown.

const (
	goodbyeDuration = 3400 // total goodbye duration in ms
	goodbyeStars    = 300  // imploding starfield size
	goodbyeSparks   = 46   // drifting ember sparks
	goodbyeFadeMS   = 340  // soft fade to black at the end
)

// goodbye timeline keyframes in milliseconds.
const (
	goodbyeRingIn      = 250
	goodbyeRingInEnd   = 1350
	goodbyeKickerIn    = 350
	goodbyeKickerEnd   = 1000
	goodbyeTitleStart  = 550
	goodbyeTitleEnd    = 1650
	goodbyeUnderlineIn = 1250
	goodbyeUnderlineTo = 1800
	goodbyeSubIn       = 1650
	goodbyeSubEnd      = 2150
	goodbyeShineStart  = 2150
	goodbyeShineEnd    = 2700
	goodbyeCollapse    = 2550
	goodbyeFlashStart  = 2950
	goodbyeFlashEnd    = 3250
)

// goodbyeStar is one star in the imploding field. x,y are the unit direction
// from the centre; z is the radial distance, 1 = far edge, 0 = at the centre.
type goodbyeStar struct {
	x, y    float64
	z       float64
	kind    uint8 // 0 = warm white, 1 = amber, 2 = rose
	respeed float64
}

// goodbyeSpark is a slowly rising ember that keeps the sky alive while the
// wordmark holds.
type goodbyeSpark struct {
	x, y  float64 // normalized 0..1 screen position
	vy    float64
	phase float64
	size  float64
}

type goodbyeState struct {
	stars     []goodbyeStar
	sparks    []goodbyeSpark
	ring      []splashRingPoint
	startTick uint64
	lastEl    float64
	dt        float64
	inited    bool
}

var goodbye goodbyeState

// initGoodbye builds the star field, sparks and ring once per sequence.
func initGoodbye() {
	goodbye.stars = make([]goodbyeStar, goodbyeStars)
	for i := range goodbye.stars {
		s := &goodbye.stars[i]
		a := rand.Float64() * 2 * math.Pi
		r := 0.25 + rand.Float64()*0.95
		s.x = math.Cos(a) * r
		s.y = math.Sin(a) * r
		s.z = 0.15 + rand.Float64()*0.85
		s.respeed = 0.7 + rand.Float64()*0.9
		switch {
		case rand.Float64() < 0.5:
			s.kind = 0
		case rand.Float64() < 0.8:
			s.kind = 1
		default:
			s.kind = 2
		}
	}

	goodbye.sparks = make([]goodbyeSpark, goodbyeSparks)
	for i := range goodbye.sparks {
		p := &goodbye.sparks[i]
		p.x = rand.Float64()
		p.y = rand.Float64()
		p.vy = 0.004 + rand.Float64()*0.012
		p.phase = rand.Float64() * 2 * math.Pi
		p.size = 1.2 + rand.Float64()*2.2
	}

	goodbye.ring = make([]splashRingPoint, splashRingN)
	for i := range goodbye.ring {
		p := &goodbye.ring[i]
		p.angle = float64(i) * 2 * math.Pi / float64(splashRingN)
		p.size = 1.4 + rand.Float64()*1.8
	}

	goodbye.startTick = sdl.GetTicks64()
	goodbye.lastEl = 0
	goodbye.dt = 16
	goodbye.inited = true
}

// goodbyeStarColor tints a star warm as it falls toward the centre.
func goodbyeStarColor(kind uint8, bright float64) (uint8, uint8, uint8) {
	switch kind {
	case 1: // amber
		return uint8(255 * bright), uint8(190 * bright), uint8(120 * bright)
	case 2: // rose
		return uint8(255 * bright), uint8(150 * bright), uint8(170 * bright)
	default: // warm white
		return uint8(255 * bright), uint8(232 * bright), uint8(190 * bright)
	}
}

// goodbyeDrawBackdrop paints the warm gradient, core glow and vignette.
func goodbyeDrawBackdrop(renderer *sdl.Renderer, elapsed, collapse float64) {
	// Deep warm vertical gradient.
	const bands = 26
	top := [3]float64{30, 18, 22}
	bottom := [3]float64{8, 5, 10}
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

	// Slow diagonal light beams, warm this time.
	drift := math.Sin(elapsed*0.00045) * 90
	for b := 0; b < 2; b++ {
		baseX := float64(screenWidth)*0.28 + drift + float64(b)*float64(screenWidth)*0.42
		for i := 0; i < 3; i++ {
			alpha := uint8(9 - i*3)
			renderer.SetDrawColor(220, 140, 90, alpha)
			renderer.DrawLine(int32(baseX)+int32(i*3), 0,
				int32(baseX)+int32(i*3)-int32(screenHeight)/3, screenHeight)
		}
	}

	// Core light: grows as the collapse intensifies.
	cy := screenHeight / 2
	pulse := 1 + 0.05*math.Sin(elapsed*0.0028)
	splashGlow(renderer, screenWidth/2, cy, int32(330*pulse), 255, 175, 115, uint8(26+72*collapse))
	splashGlow(renderer, screenWidth/2, cy, int32(140*pulse), 255, 215, 155, uint8(22+64*collapse))

	// Vignette so the centre glows.
	const steps = 13
	for i := 1; i <= steps; i++ {
		a := uint8(float64(78) * float64(steps-i) / float64(steps))
		h := int32(float64(screenHeight) * 0.32 * float64(i) / float64(steps))
		renderer.SetDrawColor(0, 0, 0, a)
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: h})
		renderer.FillRect(&sdl.Rect{X: 0, Y: screenHeight - h, W: screenWidth, H: h})
	}
}

// goodbyeDrawStars renders the imploding field: stars rush inward and die at
// the centre. As the collapse grows the whole sky accelerates and eventually
// stops respawning so the field empties into the point of light.
func goodbyeDrawStars(renderer *sdl.Renderer, elapsed, collapse float64) {
	cx := float64(screenWidth) / 2
	cy := float64(screenHeight) / 2
	spreadX := float64(screenWidth) * 0.62
	spreadY := float64(screenHeight) * 0.62
	speed := 0.00042 * (1 + 3.2*collapse)

	for i := range goodbye.stars {
		s := &goodbye.stars[i]
		s.z -= speed * s.respeed * goodbye.dt
		if s.z <= 0.02 {
			// Recycle near the edge while the sky is alive; once the final
			// collapse is under way, park the star so everything drains away.
			if collapse < 0.85 {
				a := rand.Float64() * 2 * math.Pi
				r := 0.5 + rand.Float64()*0.9
				s.x = math.Cos(a) * r
				s.y = math.Sin(a) * r
				s.z = 1
			} else {
				s.z = 1.05
				continue
			}
		}

		px := cx + s.x*s.z*spreadX
		py := cy + s.y*s.z*spreadY
		margin := 60.0
		if px < -margin || px > float64(screenWidth)+margin || py < -margin || py > float64(screenHeight)+margin {
			continue
		}

		bright := splashClamp01((1.0 - s.z) * 1.25)
		bright *= 0.85 + 0.15*math.Sin(elapsed*0.004+float64(i))
		if bright < 0.04 {
			continue
		}
		r, g, b := goodbyeStarColor(s.kind, bright)

		// Trail behind the star, pointing back toward where it came from.
		zTail := s.z + 0.05*(1+collapse*2)
		tx := cx + s.x*zTail*spreadX
		ty := cy + s.y*zTail*spreadY
		renderer.SetDrawColor(r, g, b, uint8(45+205*bright))
		renderer.DrawLine(int32(tx), int32(ty), int32(px), int32(py))
		renderer.SetDrawColor(255, 240, 220, uint8(30+150*bright))
		renderer.DrawPoint(int32(px), int32(py))
	}
}

// goodbyeDrawSparks renders the drifting embers that keep the frame alive.
func goodbyeDrawSparks(renderer *sdl.Renderer, elapsed float64) {
	for i := range goodbye.sparks {
		p := &goodbye.sparks[i]
		p.y -= p.vy * goodbye.dt
		if p.y < -0.05 {
			p.y = 1.05
			p.x = rand.Float64()
		}
		x := int32(p.x * float64(screenWidth))
		y := int32(p.y * float64(screenHeight))
		tw := 0.55 + 0.45*math.Sin(elapsed*0.004+p.phase)
		drawParticle(renderer, x, y, int32(p.size), 255, 190, 120, uint8(220*tw))
	}
}

// goodbyeDrawRing renders the orbital ring: it unrolls, then contracts toward
// the centre as the collapse grows, spinning the opposite way to the boot.
func goodbyeDrawRing(renderer *sdl.Renderer, elapsed, enter, collapse float64) {
	if enter <= 0 {
		return
	}
	cx := float64(screenWidth) / 2
	cy := float64(screenHeight) / 2
	radius := (300 - 180*collapse) * (1 + 0.02*math.Sin(elapsed*0.003)) * enter
	spin := -elapsed * 0.0009
	tilt := 0.55

	renderer.SetDrawColor(255, 180, 130, uint8(40*enter))
	steps := 96
	for i := 0; i < steps; i++ {
		a0 := float64(i) * 2 * math.Pi / float64(steps)
		a1 := float64(i+1) * 2 * math.Pi / float64(steps)
		renderer.DrawLine(
			int32(cx+math.Cos(a0)*radius), int32(cy+math.Sin(a0)*radius*tilt),
			int32(cx+math.Cos(a1)*radius), int32(cy+math.Sin(a1)*radius*tilt),
		)
	}

	for i := range goodbye.ring {
		p := &goodbye.ring[i]
		a := p.angle + spin
		depth := (math.Sin(a) + 1) / 2
		x := cx + math.Cos(a)*radius
		y := cy + math.Sin(a)*radius*tilt
		alpha := uint8((50 + 175*depth) * enter)
		size := int32(p.size * (0.7 + 0.9*depth))
		if size < 1 {
			size = 1
		}
		ta := a - 0.06
		renderer.SetDrawColor(255, 190, 140, alpha/2)
		renderer.DrawLine(
			int32(cx+math.Cos(ta)*radius), int32(cy+math.Sin(ta)*radius*tilt),
			int32(x), int32(y),
		)
		fillCircle(renderer, int32(x), int32(y), size, sdl.Color{R: 255, G: 220, B: 180, A: alpha})
	}
}

// goodbyeDrawWordmark renders the kicker, the per-letter "GOODBYE", the ember
// underline, the metallic shine sweep and the "SEE YOU NEXT TIME" subtitle.
func goodbyeDrawWordmark(renderer *sdl.Renderer, config *Config, elapsed float64) {
	font := splashGetWordmarkFont(config)
	if font == nil {
		return
	}
	small, _ := getCachedFont(config, "small")

	const word = "GOODBYE"
	runes := []rune(word)

	totalW, lh, _ := font.SizeUTF8(word)
	baseX := (screenWidth - int32(totalW)) / 2
	baseY := screenHeight*43/100 - int32(lh)/2

	// Kicker above the word.
	if small != nil {
		k := splashEase(splashClamp01((elapsed - goodbyeKickerIn) / (goodbyeKickerEnd - goodbyeKickerIn)))
		if k > 0 {
			kicker := "P O W E R I N G   D O W N"
			kw, _, _ := small.SizeUTF8(kicker)
			renderText(renderer, config, small, kicker,
				sdl.Color{R: 255, G: 190, B: 130, A: splashQuantAlpha(180 * k)},
				(screenWidth-int32(kw))/2, baseY-42)
		}
	}

	// Ember underline first, behind the letters.
	under := splashEase(splashClamp01((elapsed - goodbyeUnderlineIn) / (goodbyeUnderlineTo - goodbyeUnderlineIn)))
	if under > 0 {
		w := int32(float64(totalW+64) * under)
		x0 := screenWidth/2 - w/2
		y := baseY + int32(lh) + 26
		renderer.SetDrawColor(255, 150, 90, uint8(26*under))
		renderer.FillRect(&sdl.Rect{X: x0, Y: y - 3, W: w, H: 9})
		renderer.SetDrawColor(255, 190, 130, uint8(150*under))
		renderer.FillRect(&sdl.Rect{X: x0, Y: y, W: w, H: 2})
		renderer.SetDrawColor(255, 230, 200, uint8(210*under))
		renderer.FillRect(&sdl.Rect{X: screenWidth/2 - w/4, Y: y, W: w / 2, H: 2})
	}

	// Letters rise into place one by one.
	const stagger = 55.0
	x := baseX
	for i, r := range runes {
		start := float64(goodbyeTitleStart) + float64(i)*stagger
		t := splashEase(splashClamp01((elapsed - start) / (goodbyeTitleEnd - start)))
		rw, _, _ := font.SizeUTF8(string(r))
		if t > 0 {
			rise := int32((1 - t) * 30)
			alpha := splashQuantAlpha(255 * t)
			// Warm depth pass keeps the letters readable over the stars.
			renderText(renderer, config, font, string(r),
				sdl.Color{R: 40, G: 16, B: 22, A: splashQuantAlpha(float64(alpha) * 0.7)},
				x+2, baseY+rise+3)
			renderText(renderer, config, font, string(r),
				sdl.Color{R: 255, G: 216, B: 160, A: alpha}, x, baseY+rise)
		}
		x += int32(rw)
	}

	// Metallic shine sweep through a moving clip band.
	shine := splashClamp01((elapsed - goodbyeShineStart) / (goodbyeShineEnd - goodbyeShineStart))
	if shine > 0 && shine < 1 && elapsed > goodbyeTitleEnd-260 {
		bandW := int32(130)
		travel := float64(totalW) + float64(bandW) + 140
		bx := baseX - bandW - 70 + int32(travel*shine)
		clip := sdl.Rect{X: bx, Y: 0, W: bandW, H: screenHeight}
		renderer.SetClipRect(&clip)
		renderText(renderer, config, font, word, sdl.Color{R: 255, G: 255, B: 255, A: 245}, baseX, baseY)
		renderer.SetClipRect(nil)
	}

	// Subtitle below.
	if small != nil {
		tag := splashEase(splashClamp01((elapsed - goodbyeSubIn) / (goodbyeSubEnd - goodbyeSubIn)))
		if tag > 0 {
			sub := "SEE YOU NEXT TIME"
			tw, _, _ := small.SizeUTF8(sub)
			renderText(renderer, config, small, sub,
				sdl.Color{R: 255, G: 205, B: 165, A: splashQuantAlpha(225 * tag)},
				(screenWidth-int32(tw))/2, baseY+int32(lh)+66)
		}
	}
}

// renderGoodbye draws one frame of the goodbye sequence. Returns true while it
// is still active; false when it has finished and the caller may shut down.
func renderGoodbye(renderer *sdl.Renderer, config *Config, tick uint64) bool {
	if !goodbye.inited {
		initGoodbye()
	}

	elapsed := tick - goodbye.startTick
	if elapsed >= uint64(goodbyeDuration) {
		goodbye.inited = false
		return false
	}
	e := float64(elapsed)

	delta := e - goodbye.lastEl
	if delta < 1 || delta > 40 {
		delta = 16
	}
	goodbye.dt = delta
	goodbye.lastEl = e

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetClipRect(nil)

	collapse := splashEaseIn(splashClamp01(e / float64(goodbyeDuration-200)))
	enter := splashEase(splashClamp01((e - goodbyeRingIn) / (goodbyeRingInEnd - goodbyeRingIn)))

	goodbyeDrawBackdrop(renderer, e, collapse)
	goodbyeDrawRing(renderer, e, enter, collapse)
	goodbyeDrawStars(renderer, e, collapse)
	goodbyeDrawSparks(renderer, e)
	goodbyeDrawWordmark(renderer, config, e)

	// The point of light the sky collapses into, brightening toward the end.
	if e > float64(goodbyeCollapse) {
		t := splashClamp01((e - float64(goodbyeCollapse)) / float64(goodbyeDuration-goodbyeCollapse))
		splashGlow(renderer, screenWidth/2, screenHeight/2, int32(30+70*t), 255, 235, 205, uint8(120+120*t))
	}

	// Final warm bloom flash from the centre.
	if e > float64(goodbyeFlashStart) && e < float64(goodbyeFlashEnd) {
		t := (e - float64(goodbyeFlashStart)) / float64(goodbyeFlashEnd-goodbyeFlashStart)
		a := splashClamp01(math.Sin(math.Pi * t))
		splashGlow(renderer, screenWidth/2, screenHeight/2, int32(120+520*t), 255, 210, 160, uint8(160*a))
		splashGlow(renderer, screenWidth/2, screenHeight/2, int32(40+220*t), 255, 240, 220, uint8(200*a))
	}

	// Soft fade to black so the shutdown never pops.
	if e > float64(goodbyeDuration-goodbyeFadeMS) {
		t := (e - float64(goodbyeDuration-goodbyeFadeMS)) / float64(goodbyeFadeMS)
		renderer.SetDrawColor(2, 3, 6, uint8(245*splashClamp01(t)))
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	}

	return true
}

// ShowGoodbye blocks the caller for goodbyeDuration ms while the farewell
// plays, then returns so the caller can shut down gracefully. A window-close
// (QuitEvent) during the sequence is pushed back so it still exits the app.
func ShowGoodbye(renderer *sdl.Renderer, config *Config) {
	initGoodbye()
	start := time.Now()
	for {
		elapsed := time.Since(start).Milliseconds()
		if elapsed >= int64(goodbyeDuration) {
			break
		}

		for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
			switch ev.(type) {
			case *sdl.QuitEvent:
				sdl.PushEvent(ev)
				goodbye.inited = false
				return
			}
		}

		tick := uint64(elapsed) + goodbye.startTick
		renderGoodbye(renderer, config, tick)
		renderer.Present()
		sdl.Delay(16) // ~60fps
	}
	goodbye.inited = false
}

// ──────────────────────────────────────────────────────────────────────
// Exit request plumbing
// ──────────────────────────────────────────────────────────────────────
//
// The home Exit button and the config-driven "exit" trigger both funnel into
// requestAppExit(). The main loop notices the flag, plays ShowGoodbye, then
// shuts down through its normal deferred cleanup (persist favorites, free
// textures/fonts, sdl.Quit) rather than a hard os.Exit.

var (
	exitRequested bool
	goodbyePlayed bool
)

// requestAppExit asks the main loop to play the goodbye sequence and quit.
func requestAppExit() {
	exitRequested = true
}
