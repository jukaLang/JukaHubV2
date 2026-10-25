package main

import (
	"math"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Dynamic Background — animated, weather-reactive, time-aware wallpapers
// Replaces the static ambient glow with a living, breathing backdrop.
// ──────────────────────────────────────────────────────────────────────

// bgMode controls how the background is rendered.
type bgMode int

const (
	bgModeAmbient  bgMode = iota // Default: animated blobs + particles
	bgModeImage                  // Custom wallpaper image
	bgModeGradient               // Time-of-day gradient
)

// Dynamic background state — singleton.
var dynbg = struct {
	mode        bgMode
	wallpaper   *sdl.Texture // user wallpaper
	wallpaperW  int32
	wallpaperH  int32
	particles   []bgParticle
	particleTex *sdl.Texture // shared dot texture
	once        sync.Once
	timePhase   float64 // 0..1 day cycle
	weatherHue  float64 // 0..1 shifted by weather
}{
	mode: bgModeAmbient,
}

// bgParticle is a single floating particle in the background.
type bgParticle struct {
	x, y       float64 // screen position (0..1 fractions)
	vx, vy     float64 // velocity
	size       float64 // base size
	alpha      uint8
	colorShift float64 // hue shift
	twinkle    float64 // twinkle speed
	phase      float64 // twinkle phase offset
}

// ──────────────────────────────────────────────────────────────────────
// Time-of-day palette
// ──────────────────────────────────────────────────────────────────────

// timeOfDayPalette returns colors based on current hour.
// Returns (top, bottom, accent, particle) colors.
type bgPalette struct {
	top, bottom, accent, particle sdl.Color
	blobAlpha                     uint8
}

func getTimePalette() bgPalette {
	hour := time.Now().Hour()
	min := time.Now().Minute()
	frac := float64(hour) + float64(min)/60.0

	switch {
	case frac < 5: // Night (0-5)
		return bgPalette{
			top:       sdl.Color{R: 6, G: 8, B: 20, A: 255},
			bottom:    sdl.Color{R: 10, G: 12, B: 30, A: 255},
			accent:    sdl.Color{R: 80, G: 100, B: 180, A: 255},
			particle:  sdl.Color{R: 120, G: 140, B: 200, A: 255},
			blobAlpha: 30,
		}
	case frac < 7: // Dawn (5-7)
		t := (frac - 5) / 2.0
		return bgPalette{
			top:       lerpColorF64(sdl.Color{R: 6, G: 8, B: 20}, sdl.Color{R: 40, G: 20, B: 60}, t),
			bottom:    lerpColorF64(sdl.Color{R: 10, G: 12, B: 30}, sdl.Color{R: 120, G: 60, B: 80}, t),
			accent:    sdl.Color{R: 255, G: 150, B: 100, A: 255},
			particle:  sdl.Color{R: 255, G: 180, B: 130, A: 255},
			blobAlpha: 40,
		}
	case frac < 10: // Morning (7-10)
		t := (frac - 7) / 3.0
		return bgPalette{
			top:       lerpColorF64(sdl.Color{R: 40, G: 20, B: 60}, sdl.Color{R: 15, G: 30, B: 70}, t),
			bottom:    lerpColorF64(sdl.Color{R: 120, G: 60, B: 80}, sdl.Color{R: 20, G: 45, B: 90}, t),
			accent:    lerpColorF64(sdl.Color{R: 255, G: 150, B: 100}, sdl.Color{R: 80, G: 200, B: 255}, t),
			particle:  sdl.Color{R: 200, G: 230, B: 255, A: 255},
			blobAlpha: 45,
		}
	case frac < 17: // Day (10-17)
		return bgPalette{
			top:       sdl.Color{R: 15, G: 30, B: 70, A: 255},
			bottom:    sdl.Color{R: 20, G: 45, B: 90, A: 255},
			accent:    sdl.Color{R: 80, G: 200, B: 255, A: 255},
			particle:  sdl.Color{R: 200, G: 230, B: 255, A: 255},
			blobAlpha: 50,
		}
	case frac < 19: // Sunset (17-19)
		t := (frac - 17) / 2.0
		return bgPalette{
			top:       lerpColorF64(sdl.Color{R: 15, G: 30, B: 70}, sdl.Color{R: 30, G: 15, B: 50}, t),
			bottom:    lerpColorF64(sdl.Color{R: 20, G: 45, B: 90}, sdl.Color{R: 100, G: 40, B: 60}, t),
			accent:    lerpColorF64(sdl.Color{R: 80, G: 200, B: 255}, sdl.Color{R: 255, G: 120, B: 80}, t),
			particle:  sdl.Color{R: 255, G: 160, B: 100, A: 255},
			blobAlpha: 45,
		}
	case frac < 21: // Evening (19-21)
		t := (frac - 19) / 2.0
		return bgPalette{
			top:       lerpColorF64(sdl.Color{R: 30, G: 15, B: 50}, sdl.Color{R: 10, G: 10, B: 25}, t),
			bottom:    lerpColorF64(sdl.Color{R: 100, G: 40, B: 60}, sdl.Color{R: 15, G: 15, B: 35}, t),
			accent:    lerpColorF64(sdl.Color{R: 255, G: 120, B: 80}, sdl.Color{R: 100, G: 80, B: 160}, t),
			particle:  sdl.Color{R: 180, G: 160, B: 220, A: 255},
			blobAlpha: 35,
		}
	default: // Night (21-24)
		t := (frac - 21) / 3.0
		return bgPalette{
			top:       lerpColorF64(sdl.Color{R: 10, G: 10, B: 25}, sdl.Color{R: 6, G: 8, B: 20}, t),
			bottom:    lerpColorF64(sdl.Color{R: 15, G: 15, B: 35}, sdl.Color{R: 10, G: 12, B: 30}, t),
			accent:    lerpColorF64(sdl.Color{R: 100, G: 80, B: 160}, sdl.Color{R: 80, G: 100, B: 180}, t),
			particle:  sdl.Color{R: 140, G: 150, B: 200, A: 255},
			blobAlpha: 30,
		}
	}
}

func lerpColorF64(a, b sdl.Color, t float64) sdl.Color {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return sdl.Color{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		A: 255,
	}
}

// ──────────────────────────────────────────────────────────────────────
// Weather-reactive hue shift
// ──────────────────────────────────────────────────────────────────────

// updateWeatherHue adjusts the background hue based on current weather.
func updateWeatherHue(code int) {
	// WMO weather codes: clear=0, partly cloudy=1-3, fog=45-48,
	// drizzle=51-55, rain=61-65, snow=71-77, shower=80-82, thunderstorm=95-99
	switch {
	case code == 0 || code == 1:
		// Clear: warm shift
		dynbg.weatherHue = 0.0
	case code <= 3:
		// Partly cloudy: slight cool
		dynbg.weatherHue = 0.1
	case code >= 45 && code <= 48:
		// Fog: muted
		dynbg.weatherHue = 0.3
	case code >= 51 && code <= 55:
		// Drizzle: blue
		dynbg.weatherHue = 0.2
	case code >= 61 && code <= 65:
		// Rain: deep blue
		dynbg.weatherHue = 0.25
	case code >= 71 && code <= 77:
		// Snow: white/cool
		dynbg.weatherHue = 0.4
	case code >= 80 && code <= 82:
		// Shower: blue
		dynbg.weatherHue = 0.2
	case code >= 95:
		// Thunderstorm: purple/dark
		dynbg.weatherHue = 0.5
	default:
		dynbg.weatherHue = 0.0
	}
}

// ──────────────────────────────────────────────────────────────────────
// Particle system
// ──────────────────────────────────────────────────────────────────────

const (
	maxBgParticles = 80
	bgParticleSize = 4
)

func initBgParticles() {
	dynbg.particles = make([]bgParticle, maxBgParticles)
	for i := range dynbg.particles {
		dynbg.particles[i] = bgParticle{
			x:          rand.Float64(),
			y:          rand.Float64(),
			vx:         (rand.Float64() - 0.5) * 0.0002,
			vy:         (rand.Float64() - 0.5) * 0.00015,
			size:       2 + rand.Float64()*4,
			alpha:      uint8(20 + rand.Float64()*60),
			colorShift: rand.Float64(),
			twinkle:    0.5 + rand.Float64()*2.0,
			phase:      rand.Float64() * math.Pi * 2,
		}
	}
}

func updateBgParticles(dt float64) {
	for i := range dynbg.particles {
		p := &dynbg.particles[i]
		p.x += p.vx * dt * 60
		p.y += p.vy * dt * 60
		// Wrap around
		if p.x < -0.05 {
			p.x = 1.05
		}
		if p.x > 1.05 {
			p.x = -0.05
		}
		if p.y < -0.05 {
			p.y = 1.05
		}
		if p.y > 1.05 {
			p.y = -0.05
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Particle dot texture (created once)
// ──────────────────────────────────────────────────────────────────────

func ensureBgParticleTex(renderer *sdl.Renderer) {
	if dynbg.particleTex != nil {
		return
	}
	sz := int32(bgParticleSize * 2)
	surface, err := sdl.CreateRGBSurfaceWithFormat(0, sz, sz, 32, sdl.PIXELFORMAT_ABGR8888)
	if err != nil {
		return
	}
	defer surface.Free()
	if err := surface.Lock(); err != nil {
		return
	}
	pitch := int(surface.Pitch)
	pixels := surface.Pixels()
	center := float64(bgParticleSize)
	radius := center
	for y := int32(0); y < sz; y++ {
		for x := int32(0); x < sz; x++ {
			dx := float64(x) - center
			dy := float64(y) - center
			d := math.Sqrt(dx*dx + dy*dy)
			t := 1.0 - d/radius
			if t < 0 {
				t = 0
			}
			a := uint8(math.Round(255 * t * t))
			off := int(y)*pitch + int(x)*4
			pixels[off+0] = 255
			pixels[off+1] = 255
			pixels[off+2] = 255
			pixels[off+3] = a
		}
	}
	surface.Unlock()
	tex, err := renderer.CreateTextureFromSurface(surface)
	if err != nil {
		return
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	dynbg.particleTex = tex
}

// ──────────────────────────────────────────────────────────────────────
// Wallpaper loading
// ──────────────────────────────────────────────────────────────────────

func loadWallpaper(renderer *sdl.Renderer, path string) bool {
	if path == "" {
		return false
	}
	// Try SDL_image first
	imgSurface, err := sdl.LoadBMP(path)
	if err != nil {
		// Try loading as image (jpg/png via SDL_image if available)
		f, ferr := os.Open(path)
		if ferr != nil {
			return false
		}
		f.Close()
		return false // Can't load without SDL_image
	}
	defer imgSurface.Free()

	// Scale to screen size keeping aspect ratio
	scale := math.Min(
		float64(screenWidth)/float64(imgSurface.W),
		float64(screenHeight)/float64(imgSurface.H),
	)
	newW := int32(float64(imgSurface.W) * scale)
	newH := int32(float64(imgSurface.H) * scale)

	// Use the surface directly for texture creation
	scaled := imgSurface

	tex, err := renderer.CreateTextureFromSurface(scaled)
	if err != nil {
		return false
	}
	dynbg.wallpaper = tex
	dynbg.wallpaperW = newW
	dynbg.wallpaperH = newH
	dynbg.mode = bgModeImage
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Main render function — called every frame after clearing the screen
// ──────────────────────────────────────────────────────────────────────

// dynamicBgLowPower reports whether the backdrop should draw its reduced
// variant. Low Power is on by default on the handhelds, and every blob and
// particle is a blended texture copy, so this is the biggest per-frame cost a
// user can switch on by hand.
func dynamicBgLowPower() bool {
	return appConfig != nil && effectiveLowPower(appConfig)
}

var lastBgTime float64

func renderDynamicBackground(renderer *sdl.Renderer) {
	if renderer == nil {
		return
	}
	dynbg.once.Do(func() {
		initBgParticles()
	})

	now := float64(sdl.GetTicks64()) / 1000.0
	dt := now - lastBgTime
	if lastBgTime == 0 {
		dt = 1.0 / 60.0
	}
	lastBgTime = now

	switch dynbg.mode {
	case bgModeImage:
		renderWallpaperBg(renderer, now)
	case bgModeGradient:
		renderGradientBg(renderer, now)
	default:
		renderAmbientDynamicBg(renderer, now)
	}

	// Update particles in all modes
	updateBgParticles(dt)
}

func renderWallpaperBg(renderer *sdl.Renderer, t float64) {
	if dynbg.wallpaper == nil {
		// Fallback to ambient
		renderAmbientDynamicBg(renderer, t)
		return
	}
	// Subtle Ken Burns effect — slow zoom + pan
	zoom := 1.0 + 0.03*math.Sin(t*0.02)
	panX := int32(10 * math.Sin(t*0.015))
	panY := int32(6 * math.Cos(t*0.012))
	w := int32(float64(dynbg.wallpaperW) * zoom)
	h := int32(float64(dynbg.wallpaperH) * zoom)
	x := (screenWidth-w)/2 + panX
	y := (screenHeight-h)/2 + panY

	// Dark overlay for readability
	_ = dynbg.wallpaper.SetAlphaMod(180)
	renderer.Copy(dynbg.wallpaper, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})

	// Subtle gradient overlay at bottom for text readability. This used to
	// be 80 one-pixel band fills per frame (10 on Low Power); the ramp is
	// static so it is built once into a texture and re-issued as one Copy
	// per frame (bgGrads below).
	scrim := bgGrads.scrim
	if !bgGrads.scrimFailed && (bgGrads.renderer != renderer || scrim == nil) {
		scrim = buildWallpaperScrim(renderer)
		if scrim == nil {
			bgGrads.scrimFailed = true
		}
	}
	if scrim != nil {
		_ = renderer.Copy(scrim, nil, &sdl.Rect{X: 0, Y: screenHeight - 80, W: screenWidth, H: 80})
	} else {
		for i := int32(0); i < 80; i++ {
			a := uint8(200 * (1.0 - float64(i)/80.0))
			renderer.SetDrawColor(0, 0, 0, a)
			renderer.FillRect(&sdl.Rect{X: 0, Y: screenHeight - 80 + i, W: screenWidth, H: 1})
		}
	}
}

func renderGradientBg(renderer *sdl.Renderer, t float64) {
	pal := getTimePalette()
	// Animated gradient: vertical gradient with slowly shifting colors
	hshift := dynbg.weatherHue * 30 // Subtle hue shift from weather
	top := sdl.Color{
		R: clampByte(int(pal.top.R) + int(hshift)),
		G: clampByte(int(pal.top.G) - int(hshift/2)),
		B: clampByte(int(pal.top.B) + int(hshift/3)),
		A: 255,
	}

	// Draw horizontal bands for gradient — now baked into a texture so the
	// 24 band fills become one Copy per frame.
	dayTex := bgGrads.dayTex
	if !bgGrads.dayFailed && (bgGrads.renderer != renderer || dayTex == nil ||
		bgGrads.dayW != screenWidth || bgGrads.dayH != screenHeight ||
		bgGrads.dayTop != top || bgGrads.dayBtm != pal.bottom) {
		dayTex = buildDayGradientTexture(renderer, top, pal.bottom, screenWidth, screenHeight)
		if dayTex != nil {
			bgGrads.dayTex = dayTex
			bgGrads.renderer = renderer
			bgGrads.dayW, bgGrads.dayH = screenWidth, screenHeight
			bgGrads.dayTop, bgGrads.dayBtm = top, pal.bottom
		} else {
			bgGrads.dayFailed = true
		}
	}
	if dayTex != nil {
		_ = renderer.Copy(dayTex, nil, &sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
		// Render particles on top
		renderBgParticles(renderer, t, pal)
		return
	}

	// Fallback when targets are unavailable: draw the 24 bands directly.
	bands := int32(24)
	bandH := screenHeight / bands
	for i := int32(0); i < bands; i++ {
		frac := float64(i) / float64(bands)
		r := uint8(float64(top.R) + (float64(pal.bottom.R)-float64(top.R))*frac)
		g := uint8(float64(top.G) + (float64(pal.bottom.G)-float64(top.G))*frac)
		b := uint8(float64(top.B) + (float64(pal.bottom.B)-float64(top.B))*frac)
		renderer.SetDrawColor(r, g, b, 255)
		renderer.FillRect(&sdl.Rect{X: 0, Y: i * bandH, W: screenWidth, H: bandH + 1})
	}

	// Render particles on top
	renderBgParticles(renderer, t, pal)
}

// ──────────────────────────────────────────────────────────────────────
// Gradient texture cache
//
// The wallpaper scrim and day-cycle backdrop were drawn as 80 resp. 24
// one-pixel band fills every frame. On the GLES2 renderer each of those
// bands is its own draw call and on the software renderer it is a full CPU
// raster pass, so both are now built once into a render-target texture and
// re-issued as a single blended Copy per frame — the same idea as the text
// and shape texture caches. The day texture is keyed by its colours, so a
// palette change (time of day, weather) rebuilds it once; a renderer swap
// invalidates everything without Destroy since textures belong to the old
// renderer.
//
// All fields are touched only from the render thread; no locking.
// ──────────────────────────────────────────────────────────────────────

var bgGrads struct {
	renderer       *sdl.Renderer
	scrim          *sdl.Texture // 80-pixel readability ramp under the wallpaper
	dayTex         *sdl.Texture // full-screen day-cycle gradient
	dayW, dayH     int32
	dayTop, dayBtm sdl.Color
	scrimFailed    bool // render targets unavailable; use the band fallback
	dayFailed      bool
}

// buildBgGradientTexture renders paint into a fresh render-target texture of
// w×h and returns it, or nil when targets are unavailable. Renderer state is
// restored either way so later draws keep their colour and clip.
func buildBgGradientTexture(renderer *sdl.Renderer, w, h int32, paint func(*sdl.Renderer, int32, int32)) *sdl.Texture {
	tex, err := renderer.CreateTexture(uint32(sdl.PIXELFORMAT_RGBA8888), sdl.TEXTUREACCESS_TARGET, w, h)
	if err != nil {
		return nil
	}
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)

	oldTarget := renderer.GetRenderTarget()
	oldClip := renderer.GetClipRect()
	_ = renderer.SetRenderTarget(tex)
	_ = renderer.SetClipRect(nil)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetDrawColor(0, 0, 0, 0)
	_ = renderer.Clear()
	paint(renderer, w, h)
	// On the default backbuffer GetRenderTarget() returns nil; a nil restore
	// still returns off the target, so always restore.
	_ = renderer.SetRenderTarget(oldTarget)
	if oldClip.W > 0 && oldClip.H > 0 {
		_ = renderer.SetClipRect(&oldClip)
	} else {
		_ = renderer.SetClipRect(nil)
	}
	return tex
}

// buildWallpaperScrim bakes the static alpha ramp into a texture and
// registers it in bgGrads. On render targets being unavailable it returns nil
// for good, and the caller keeps the one-pixel-band fallback.
func buildWallpaperScrim(renderer *sdl.Renderer) *sdl.Texture {
	bgGrads.renderer = renderer
	tex := buildBgGradientTexture(renderer, screenWidth, 80, func(rr *sdl.Renderer, tw, th int32) {
		// Same ramp the per-pixel fallback draws: 200-alpha at the top row
		// of the strip fading to clear at the bottom.
		for i := int32(0); i < th; i++ {
			a := uint8(200 * (1.0 - float64(i)/float64(th)))
			_ = rr.SetDrawColor(0, 0, 0, a)
			_ = rr.FillRect(&sdl.Rect{X: 0, Y: i, W: tw, H: 1})
		}
	})
	if tex != nil {
		bgGrads.scrim = tex
	}
	return tex
}

// buildDayGradientTexture bakes the day-cycle palette into a full-screen
// texture; the per-frame cost collapses from 24 band fills to one Copy.
func buildDayGradientTexture(renderer *sdl.Renderer, top, bottom sdl.Color, w, h int32) *sdl.Texture {
	tex := buildBgGradientTexture(renderer, w, h, func(rr *sdl.Renderer, tw, th int32) {
		const bands = 24
		bandH := th / bands
		for i := int32(0); i < bands; i++ {
			frac := float64(i) / float64(bands)
			r := uint8(float64(top.R) + (float64(bottom.R)-float64(top.R))*frac)
			g := uint8(float64(top.G) + (float64(bottom.G)-float64(top.G))*frac)
			b := uint8(float64(top.B) + (float64(bottom.B)-float64(top.B))*frac)
			_ = rr.SetDrawColor(r, g, b, 255)
			_ = rr.FillRect(&sdl.Rect{X: 0, Y: i * bandH, W: tw, H: bandH + 1})
		}
	})
	if tex == nil {
		bgGrads.dayFailed = true
		return nil
	}
	bgGrads.renderer = renderer
	return tex
}

// ClearBgGradientCache frees the cached gradient textures (renderer shutdown
// or wallpaper/theme change where colours were invalidated elsewhere).
func ClearBgGradientCache() {
	if bgGrads.renderer == nil {
		return
	}
	if bgGrads.scrim != nil {
		bgGrads.scrim.Destroy()
	}
	if bgGrads.dayTex != nil {
		bgGrads.dayTex.Destroy()
	}
	bgGrads = struct {
		renderer       *sdl.Renderer
		scrim          *sdl.Texture
		dayTex         *sdl.Texture
		dayW, dayH     int32
		dayTop, dayBtm sdl.Color
		scrimFailed    bool
		dayFailed      bool
	}{}
}

func renderAmbientDynamicBg(renderer *sdl.Renderer, t float64) {
	pal := getTimePalette()

	// Base fill
	renderer.SetDrawColor(pal.top.R, pal.top.G, pal.top.B, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	// Enhanced ambient glow — more colors and bigger blobs than original
	ensureAmbientGlow(renderer)
	if ambientGlow == nil {
		return
	}

	scale := float64(min32(screenWidth, screenHeight)) / 720.0

	type blob struct {
		ox, oy float64
		rx, ry float64
		speed  float64
		phase  float64
		size   float64
		color  sdl.Color
		alpha  uint8
	}

	// 6 blobs instead of 4 for richer visuals
	blobs := []blob{
		{0.3, 0.25, 0.14, 0.11, 0.08, 0.0, 640, pal.accent, pal.blobAlpha + 8},
		{0.7, 0.55, 0.16, 0.13, -0.06, 2.1, 740, sdl.Color{R: 139, G: 124, B: 255}, pal.blobAlpha},
		{0.55, 0.15, 0.12, 0.09, 0.12, 4.2, 500, sdl.Color{R: 59, G: 130, B: 246}, pal.blobAlpha - 4},
		{0.2, 0.65, 0.11, 0.12, -0.09, 1.2, 560, pal.particle, pal.blobAlpha - 8},
		{0.8, 0.3, 0.10, 0.08, 0.07, 3.5, 480, sdl.Color{R: 236, G: 72, B: 153}, pal.blobAlpha - 10},
		{0.4, 0.8, 0.09, 0.10, -0.05, 5.0, 420, sdl.Color{R: 34, G: 197, B: 94}, pal.blobAlpha - 12},
	}

	// Low Power keeps three blobs: the drift still reads, at half the copies.
	visibleBlobs := blobs
	if dynamicBgLowPower() {
		visibleBlobs = blobs[:3]
	}
	for i, b := range visibleBlobs {
		cx := float64(screenWidth)*b.ox + math.Cos(t*b.speed+b.phase)*float64(screenWidth)*b.rx
		cy := float64(screenHeight)*b.oy + math.Sin(t*b.speed*0.8+b.phase)*float64(screenHeight)*b.ry
		sz := b.size * scale * (1 + 0.12*math.Sin(t*0.18+float64(i)*1.7))
		d := int32(sz)
		if d < 32 {
			d = 32
		}
		cx = clampF64(cx, 0, float64(screenWidth))
		cy = clampF64(cy, 0, float64(screenHeight))
		_ = ambientGlow.SetColorMod(b.color.R, b.color.G, b.color.B)
		_ = ambientGlow.SetAlphaMod(b.alpha)
		_ = renderer.Copy(ambientGlow, nil, &sdl.Rect{
			X: int32(cx) - d/2, Y: int32(cy) - d/2, W: d, H: d,
		})
	}

	// Restore texture state
	_ = ambientGlow.SetColorMod(255, 255, 255)
	_ = ambientGlow.SetAlphaMod(255)

	// Render floating particles
	renderBgParticles(renderer, t, pal)
}

func renderBgParticles(renderer *sdl.Renderer, t float64, pal bgPalette) {
	ensureBgParticleTex(renderer)
	if dynbg.particleTex == nil {
		return
	}
	// Low Power draws a quarter of the particles; they are decorative and
	// the motion is still there.
	particles := dynbg.particles
	if dynamicBgLowPower() && len(particles) > 20 {
		particles = particles[:20]
	}
	for _, p := range particles {
		// Twinkle
		twinkle := 0.5 + 0.5*math.Sin(t*p.twinkle+p.phase)
		alpha := uint8(float64(p.alpha) * twinkle)
		if alpha < 5 {
			continue
		}
		// Color: base particle color shifted by hue
		pc := pal.particle
		shift := p.colorShift * 30
		pc.R = clampByte(int(pc.R) + int(shift))
		pc.G = clampByte(int(pc.G) - int(shift/2))
		pc.B = clampByte(int(pc.B) + int(shift/3))

		sz := int32(p.size * float64(min32(screenWidth, screenHeight)) / 720.0)
		if sz < 2 {
			sz = 2
		}
		x := int32(p.x * float64(screenWidth))
		y := int32(p.y * float64(screenHeight))
		_ = dynbg.particleTex.SetColorMod(pc.R, pc.G, pc.B)
		_ = dynbg.particleTex.SetAlphaMod(alpha)
		_ = renderer.Copy(dynbg.particleTex, nil, &sdl.Rect{X: x - sz/2, Y: y - sz/2, W: sz, H: sz})
	}
	// Restore
	_ = dynbg.particleTex.SetColorMod(255, 255, 255)
	_ = dynbg.particleTex.SetAlphaMod(255)
}

// ──────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────

func clampF64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// SetBgMode sets the background rendering mode.
func SetBgMode(mode bgMode) {
	dynbg.mode = mode
}

// SetWallpaper sets a user wallpaper image path.
func SetWallpaper(path string) {
	if path != "" {
		dynbg.mode = bgModeImage
	}
	// Wallpaper texture will be loaded on next render
}

// DynBGUpdateWeather should be called when weather data changes.
func DynBGUpdateWeather(code int) {
	updateWeatherHue(code)
}
