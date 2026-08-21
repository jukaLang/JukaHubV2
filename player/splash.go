package main

import (
	"math"
	"math/rand"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ──────────────────────────────────────────────────────────────────────
// Animated particle splash screen
// ──────────────────────────────────────────────────────────────────────

const (
	splashDuration = 2200 // total splash duration in ms
	splashPhase1   = 800  // particles converge
	splashPhase2   = 400  // glow pulse + logo fade in
	splashPhase3   = 1000 // hold + dissolve out
	splashParticle = 64   // number of particles
)

type splashParticleData struct {
	startX, startY     float64 // starting position (edge of screen)
	endX, endY         float64 // convergence target (center area)
	currentX, currentY float64
	phase              int // 0=converge, 1=pulse, 2=dissolve
	size               float64
	speed              float64
	orbitAngle         float64 // subtle orbit during phase 1
	orbitRadius        float64
	orbitSpeed         float64
	r, g, b            uint8
	alpha              uint8
	alive              bool
}

type splashState struct {
	particles []splashParticleData
	startTick uint64
	inited    bool
}

var splash splashState

// initSplash generates the particle set with random positions at screen edges.
func initSplash() {
	splash.particles = make([]splashParticleData, splashParticle)
	cx := float64(screenWidth) / 2
	cy := float64(screenHeight) / 2

	for i := range splash.particles {
		p := &splash.particles[i]
		// Start from random positions along screen edges.
		side := rand.Intn(4)
		switch side {
		case 0: // top
			p.startX = rand.Float64() * float64(screenWidth)
			p.startY = -20
		case 1: // right
			p.startX = float64(screenWidth) + 20
			p.startY = rand.Float64() * float64(screenHeight)
		case 2: // bottom
			p.startX = rand.Float64() * float64(screenWidth)
			p.startY = float64(screenHeight) + 20
		case 3: // left
			p.startX = -20
			p.startY = rand.Float64() * float64(screenHeight)
		}
		// Converge toward center with some randomness.
		p.endX = cx + (rand.Float64()-0.5)*120
		p.endY = cy + (rand.Float64()-0.5)*80
		p.currentX = p.startX
		p.currentY = p.startY
		p.size = 2 + rand.Float64()*4
		p.speed = 0.6 + rand.Float64()*0.4
		p.orbitAngle = rand.Float64() * math.Pi * 2
		p.orbitRadius = 8 + rand.Float64()*20
		p.orbitSpeed = 2 + rand.Float64()*3

		// Tint particles with the accent color range (blues and purples).
		p.r = uint8(100 + rand.Intn(80))
		p.g = uint8(120 + rand.Intn(60))
		p.b = uint8(200 + rand.Intn(56))
		p.alpha = 200 + uint8(rand.Intn(56))
		p.alive = true
	}
	splash.startTick = sdl.GetTicks64()
	splash.inited = true
}

// renderSplash draws one frame of the splash animation. Returns true while
// the splash is still active; false when it's done and the caller should
// proceed to the main UI.
func renderSplash(renderer *sdl.Renderer, config *Config, tick uint64) bool {
	if !splash.inited {
		initSplash()
	}

	elapsed := tick - splash.startTick
	if elapsed >= uint64(splashDuration) {
		splash.inited = false
		return false
	}

	frac := float64(elapsed) / float64(splashDuration)

	// Dark background.
	renderer.SetDrawColor(8, 10, 18, 255)
	renderer.Clear()

	// Phase calculations.
	phase1Frac := math.Min(float64(elapsed)/float64(splashPhase1), 1.0)
	phase2Start := float64(splashPhase1)
	phase3Start := float64(splashPhase1 + splashPhase2)

	// Glow intensity: peaks during phase 2, fades during phase 3.
	glowAlpha := uint8(0)
	if elapsed > uint64(phase2Start) {
		phase2Frac := math.Min((float64(elapsed)-phase2Start)/float64(splashPhase2), 1.0)
		glowAlpha = uint8(80 * phase2Frac)
	}
	if elapsed > uint64(phase3Start) {
		phase3Frac := (float64(elapsed) - phase3Start) / float64(splashPhase3)
		glowAlpha = uint8(float64(glowAlpha) * (1 - phase3Frac))
	}

	// Center glow: a soft circle that pulses during phase 2.
	if glowAlpha > 0 {
		cx := float64(screenWidth) / 2
		cy := float64(screenHeight) / 2
		glowR := float64(60 + int(40*math.Sin(float64(elapsed)*0.008)))
		for r := int(glowR); r > 0; r -= 4 {
			a := uint8(float64(glowAlpha) * float64(r) / glowR * 0.6)
			renderer.SetDrawColor(120, 160, 255, a)
			renderer.FillRect(&sdl.Rect{
				X: int32(cx) - int32(r), Y: int32(cy) - int32(r),
				W: int32(r * 2), H: int32(r * 2),
			})
		}
	}

	// Particles.
	for i := range splash.particles {
		p := &splash.particles[i]
		if !p.alive {
			continue
		}

		switch {
		case elapsed < uint64(splashPhase1):
			// Phase 1: converge from edge toward center.
			t := phase1Frac * p.speed
			if t > 1 {
				t = 1
			}
			// Smooth ease-out curve.
			t = 1 - (1-t)*(1-t)
			p.currentX = p.startX + (p.endX-p.startX)*t
			p.currentY = p.startY + (p.endY-p.startY)*t

			// Add a subtle orbit around the trajectory.
			p.orbitAngle += p.orbitSpeed * 0.016
			orbitOff := (1 - t) * p.orbitRadius
			p.currentX += math.Cos(p.orbitAngle) * orbitOff
			p.currentY += math.Sin(p.orbitAngle) * orbitOff

		case elapsed < uint64(splashPhase2):
			// Phase 2: hold at center, gently drift inward.
			drift := float64(elapsed-uint64(splashPhase1)) / float64(splashPhase2)
			orbitOff := (1 - drift) * p.orbitRadius * 0.3
			p.orbitAngle += p.orbitSpeed * 0.008
			p.currentX = p.endX + math.Cos(p.orbitAngle)*orbitOff
			p.currentY = p.endY + math.Sin(p.orbitAngle)*orbitOff

		default:
			// Phase 3: dissolve outward with expanding ring.
			dissolve := (float64(elapsed) - phase3Start) / float64(splashPhase3)
			// Fly outward and fade.
			dirX := p.endX - float64(screenWidth)/2
			dirY := p.endY - float64(screenHeight)/2
			dist := math.Sqrt(dirX*dirX + dirY*dirY)
			if dist < 1 {
				dirX, dirY = 1, 0
			} else {
				dirX /= dist
				dirY /= dist
			}
			// If particle was near center, pick a random direction.
			if dist < 30 {
				angle := p.orbitAngle
				dirX = math.Cos(angle)
				dirY = math.Sin(angle)
			}
			expand := dissolve * 600
			p.currentX = p.endX + dirX*expand
			p.currentY = p.endY + dirY*expand
			fade := 1.0 - dissolve
			if fade < 0 {
				fade = 0
			}
			p.alpha = uint8(float64(220) * fade)
		}

		// Draw the particle as a filled circle (concentric scanlines).
		drawParticle(renderer, int32(p.currentX), int32(p.currentY),
			int32(p.size), p.r, p.g, p.b, p.alpha)
	}

	// Logo text: fades in during phase 2, dissolves during phase 3.
	var logoAlpha uint8
	if elapsed >= uint64(splashPhase1) && elapsed < uint64(phase3Start) {
		phase2Frac := math.Min((float64(elapsed)-phase2Start)/float64(splashPhase2), 1.0)
		logoAlpha = uint8(255 * phase2Frac)
	} else if elapsed >= uint64(phase3Start) {
		dissolve := (float64(elapsed) - phase3Start) / float64(splashPhase3)
		logoAlpha = uint8(255 * (1 - dissolve))
	}

	if logoAlpha > 0 {
		font, _ := getCachedFont(config, "medium")
		if font != nil {
			logo := "JukaHub"
			lw, lh, _ := font.SizeUTF8(logo)
			lx := (screenWidth - int32(lw)) / 2
			ly := (screenHeight - int32(lh)) / 2

			// Glow behind text.
			if logoAlpha > 100 {
				glowA := uint8(float64(logoAlpha-100) * 0.4)
				renderer.SetDrawColor(120, 160, 255, glowA)
				renderer.FillRect(&sdl.Rect{
					X: lx - 20, Y: ly - 10,
					W: int32(lw) + 40, H: int32(lh) + 20,
				})
			}

			// Main logo text.
			col := sdl.Color{R: 240, G: 245, B: 255, A: logoAlpha}
			renderText(renderer, config, font, logo, col, lx, ly)

			// Subtitle below.
			sub, _ := getCachedFont(config, "small")
			if sub != nil {
				subAlpha := logoAlpha
				if subAlpha > 180 {
					subAlpha = 180
				}
				subtitle := "Your device. Your way."
				sw, _, _ := sub.SizeUTF8(subtitle)
				sx := (screenWidth - int32(sw)) / 2
				sy := ly + int32(lh) + 16
				subCol := sdl.Color{R: 150, G: 170, B: 200, A: subAlpha}
				renderText(renderer, config, sub, subtitle, subCol, sx, sy)
			}
		}
	}

	// Subtle progress bar at the bottom.
	if frac < 0.95 {
		barW := int32(float64(screenWidth) * frac)
		renderer.SetDrawColor(120, 160, 255, 120)
		renderer.FillRect(&sdl.Rect{X: 0, Y: screenHeight - 3, W: barW, H: 3})
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

// ShowSplash blocks the caller for splashDuration ms, animating particles
// that converge to reveal the JukaHub logo. Called once at startup after
// the renderer is ready.
func ShowSplash(renderer *sdl.Renderer, config *Config) {
	initSplash()
	start := time.Now()
	for time.Since(start).Milliseconds() < int64(splashDuration) {
		tick := uint64(time.Since(start).Milliseconds()) + splash.startTick
		renderSplash(renderer, config, tick)
		renderer.Present()
		sdl.Delay(16) // ~60fps
	}
	splash.inited = false
}
