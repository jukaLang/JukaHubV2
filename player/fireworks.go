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
