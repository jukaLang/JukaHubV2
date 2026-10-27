package main

// The home screen's cards are the largest textures in the shell, and the focus
// lift grows them by up to three pixels. Keyed by the grown size, every step of
// that ease minted a new card surface and focus ring — half a megabyte each at
// 720p — inside a shape cache the handheld budgets at 12 MB, so one D-pad move
// pushed the rest of the screen's chrome out and the next frames re-rasterised
// it. These pin the fix: one texture per card, blitted into the grown rect.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// shapeEntryCount is how many primitives the shape cache holds.
func shapeEntryCount() int {
	shapeTextures.mu.Lock()
	defer shapeTextures.mu.Unlock()
	return len(shapeTextures.entries)
}

// TestRoundedFillLiftReusesTheSettledShape draws one fill at its settled size
// and then at every step of the three-pixel lift the home cards animate.
func TestRoundedFillLiftReusesTheSettledShape(t *testing.T) {
	renderer := probeRenderer(t)
	base := sdl.Rect{X: 200, Y: 120, W: 480, H: 258}
	radius := int32(HomeCardRadius)

	fillRoundedRectAt(renderer, base, base, radius, ColorCard)
	warm := shapeEntryCount()

	for step := 1; step <= 3; step++ {
		grown := sdl.Rect{X: base.X - int32(step), Y: base.Y - int32(step), W: base.W + int32(step)*2, H: base.H + int32(step)*2}
		fillRoundedRectAt(renderer, base, grown, radius, ColorCard)
	}

	if got := shapeEntryCount(); got != warm {
		t.Fatalf("a %dpx lift over one card created %d shape textures, want %d: the whole animation has to share the settled texture, or every focus move fills the cache with one texture per step", 3, got-warm, 0)
	}
	if _, fallback, _ := ShapeCacheStats(); fallback != 0 {
		t.Fatalf("the lift painted %d shapes directly instead of blitting the cached texture", fallback)
	}
}

// TestHomeActionCardLiftSharesOneSurface walks the card's real drawing path,
// not just the shape helper: it is the call site that decides whether the
// settled rect or the lifting one becomes the cache key.
func TestHomeActionCardLiftSharesOneSurface(t *testing.T) {
	renderer := probeRenderer(t)
	newProbeFrame(t)
	layout := NewHomeLayout(probeW, probeH)
	fonts := loadHomeFonts()

	var item HomeItem
	found := false
	for _, id := range layout.Order {
		if candidate := layout.Items[id]; candidate.Rect.W > 0 && candidate.Rect.H > 0 {
			item, found = candidate, true
			break
		}
	}
	if !found {
		t.Fatal("the home layout has no card to draw")
	}

	layout.drawActionCard(renderer, fonts, item, true, false, 0, 0) // settled
	_, _, before := ShapeCacheStats()
	for _, lift := range []float64{0.2, 0.4, 0.6, 0.8, 1} {
		layout.drawActionCard(renderer, fonts, item, true, false, 0, lift)
	}
	_, _, after := ShapeCacheStats()

	// The card surface alone is rect.W x rect.H x 4 bytes — around half a
	// megabyte at 720p — so the old per-step keying added megabytes here. What
	// the ramp may still mint is the icon tile's own small textures, which
	// change size with the card.
	if grew := after - before; grew > 64<<10 {
		t.Fatalf("a full lift of a %dx%d card added %d KB of shape textures; the whole animation has to share the settled surface", item.Rect.W, item.Rect.H, grew>>10)
	}
}

// TestHomeCardLiftBlitCoversTheGrownRect proves the cached texture is scaled
// into the grown rect rather than drawn at its settled size: a card that only
// blitted at the base size would leave the lift's new ring of pixels empty.
func TestHomeCardLiftBlitCoversTheGrownRect(t *testing.T) {
	renderer := probeRenderer(t)

	base := sdl.Rect{X: 220, Y: 160, W: 480, H: 258}
	grown := scaleHomeCard(base, 1) // the settled focus lift: +3px on every side
	if grown.W != base.W+6 || grown.H != base.H+6 {
		t.Fatalf("scaleHomeCard(full lift) = %+v, want the card 6px wider and taller than %+v", grown, base)
	}

	f := newProbeFrame(t)
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	DrawHomeCardSurfaceAt(renderer, base, grown, true, false)

	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	// Three pixels inside the grown edge: only the lift reaches here, so a
	// surface blitted at the base size would leave it as the background.
	if f.isBG(grown.X+3, grown.Y+grown.H/2) {
		t.Fatalf("pixel %d,%d inside the lifted card is still the background: the cached surface was blitted at its settled size instead of into the grown rect", grown.X+3, grown.Y+grown.H/2)
	}
	// And the lift stops at the grown rect: one pixel outside stays background.
	if !f.isBG(grown.X-3, grown.Y+grown.H/2) {
		t.Fatalf("pixel %d,%d just outside the lifted card is %v, want the background: the scaled blit painted past its rect", grown.X-3, grown.Y+grown.H/2, f.at(grown.X-3, grown.Y+grown.H/2))
	}
}
