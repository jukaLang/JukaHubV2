package main

import (
	"math"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// Ambient animated background.
//
// The whole effect is one cached 256x256 white radial soft-glow texture,
// tinted per blob with SetColorMod and blitted at low alpha. Per frame the
// render cost is only a handful of texture copies, so the drift stays cheap
// on the Trimui Smart Pro. Blob orbits are minutes long and amplitudes are
// small, so the motion reads as a gentle ambient pulse rather than motion
// sickness-inducing animation.
var (
	ambientGlowOnce sync.Once
	ambientGlow     *sdl.Texture
)

// ensureAmbientGlow builds the soft radial glow texture once per process.
func ensureAmbientGlow(renderer *sdl.Renderer) {
	ambientGlowOnce.Do(func() {
		const size = 256
		surface, err := sdl.CreateRGBSurfaceWithFormat(0, size, size, 32, sdl.PIXELFORMAT_ABGR8888)
		if err != nil {
			return
		}
		defer surface.Free()
		if err := surface.Lock(); err != nil {
			return
		}
		pitch := int(surface.Pitch)
		pixels := surface.Pixels()
		center := float64(size) / 2
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				dx := float64(x) - center
				dy := float64(y) - center
				d := math.Sqrt(dx*dx + dy*dy)
				t := 1 - d/center
				if t < 0 {
					t = 0
				}
				a := uint8(math.Round(255 * t * t))
				off := y*pitch + x*4
				// ABGR8888 on little-endian stores bytes R,G,B,A in memory.
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
		ambientGlow = tex
	})
}

// renderAmbientBackground draws the slowly drifting color blobs. It is called
// after the base background and content panel, before scene elements, so the
// glow reads as ambient light behind the controls. All texture state is
// restored to identity afterward so no later draw path inherits a tint.
func renderAmbientBackground(renderer *sdl.Renderer) {
	if renderer == nil {
		return
	}
	ensureAmbientGlow(renderer)
	if ambientGlow == nil {
		return
	}
	t := float64(sdl.GetTicks64()) / 1000.0
	scale := float64(min32(screenWidth, screenHeight)) / 720.0

	accent := ColorInfo
	purple := sdl.Color{R: 0x9D, G: 0x6C, B: 0xFF}
	blue := sdl.Color{R: 0x3B, G: 0x82, B: 0xF6}
	soft := sdl.Color{R: 0x6E, G: 0xE7, B: 0xFF}

	type blob struct {
		ox, oy float64 // orbit center, fractions of screen
		rx, ry float64 // orbit radii, fractions of screen
		speed  float64 // radians per second (minutes-long orbits)
		phase  float64
		size   float64 // base diameter at 720p
		color  sdl.Color
		alpha  uint8
	}
	blobs := []blob{
		{0.32, 0.28, 0.14, 0.11, 0.10, 0.0, 640, accent, 46},
		{0.68, 0.56, 0.16, 0.13, -0.08, 2.1, 740, purple, 40},
		{0.58, 0.18, 0.12, 0.09, 0.14, 4.2, 500, blue, 36},
		{0.22, 0.64, 0.11, 0.12, -0.11, 1.2, 560, soft, 32},
	}

	for i, b := range blobs {
		cx := float64(screenWidth)*b.ox + math.Cos(t*b.speed+b.phase)*float64(screenWidth)*b.rx
		cy := float64(screenHeight)*b.oy + math.Sin(t*b.speed*0.8+b.phase)*float64(screenHeight)*b.ry
		size := b.size * scale * (1 + 0.12*math.Sin(t*0.18+float64(i)*1.7))
		d := int32(size)
		if d < 32 {
			d = 32
		}
		// Keep the blob's center inside the visible area so it never bleeds
		// off the screen entirely.
		if cx < 0 {
			cx = 0
		}
		if cx > float64(screenWidth) {
			cx = float64(screenWidth)
		}
		if cy < 0 {
			cy = 0
		}
		if cy > float64(screenHeight) {
			cy = float64(screenHeight)
		}
		_ = ambientGlow.SetColorMod(b.color.R, b.color.G, b.color.B)
		_ = ambientGlow.SetAlphaMod(b.alpha)
		_ = renderer.Copy(ambientGlow, nil, &sdl.Rect{
			X: int32(cx) - d/2,
			Y: int32(cy) - d/2,
			W: d,
			H: d,
		})
	}
	// Restore identity so later draws (cards, text, borders) are not tinted.
	_ = ambientGlow.SetColorMod(255, 255, 255)
	_ = ambientGlow.SetAlphaMod(255)
}
