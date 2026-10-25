package main

// ──────────────────────────────────────────────────────────────────────
// Icon atlas for the shared rendered glyph set
//
// The home cards, tools tiles, emulator tiles and launcher empty states all
// draw their icons through DrawHomeIcon, whose glyphs are built from filled
// primitives. On the GLES2 renderer every one of those primitives is its own
// cached-shape blit — a texture bind and a draw call — so a home screen drew
// the same few dozen small textures hundreds of times per frame just to paint
// its glyphs.
//
// This atlas bakes each glyph once, at its canonical 32px size (the size
// DrawIconTile centres in a card's icon tile), into one strip texture, and
// draws every later occurrence by Copy-ing the glyph's sub-rect: one texture
// bound for the whole frame's icons instead of one bind per primitive. Copy is
// the same blended blit the shape cache uses, so where a glyph's interior is
// opaque the frame pixels are what the direct renderer produced, and the
// anti-aliased edges carry the same baked-onto-transparent compositing as every
// other cached shape.
//
// The atlas is renderer-local (like shapeCache) because a texture belongs to
// the renderer that created it. Slots are filled in first-use order, and a full
// atlas or a theme change rebuilds from zero: the glyph set in use has ~15
// keys, so 64 slots are a comfortable ceiling for configs with unknown icon
// names. Unsupported renderers (no render targets) fall back to painting
// primitives directly, exactly as shapeCache does.
// ──────────────────────────────────────────────────────────────────────

import (
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// iconAtlasGlyph is the canonical glyph box: DrawIconTile centres a
	// 32px box inside the card's icon tile. Atlas blits are restricted to
	// boxes of exactly this size — that is every home card, icon tile and
	// tool/emulator tile — so rarer, differently sized callers (the 46px
	// launcher empty-state glyph, 36px bells, list-row glyphs) keep native
	// rendering at their box instead of drawing from a scaled blit.
	iconAtlasGlyph = int32(32)
	// iconAtlasGlyphMax caps the box we bake; nothing in the shell paints a
	// glyph past this and a pathological size must never eat a slot.
	iconAtlasGlyphMax = 256
	// iconAtlasCols/Rows: a 1024×64 strip holding 64 glyph slots.
	iconAtlasCols = int32(32)
	iconAtlasRows = int32(2)
)

func iconAtlasCapacity() int32 { return iconAtlasCols * iconAtlasRows }

// iconAtlasKey identifies a baked glyph. The icon name is normalized before
// hashing, so the multi-word aliases ("gamepad"/"emulators"/"controller")
// resolve to one slot each.
type iconAtlasKey struct {
	name    string
	focused bool
}

type iconAtlas struct {
	mu          sync.Mutex
	renderer    *sdl.Renderer // the one renderer the strip texture belongs to
	texture     *sdl.Texture
	slots       map[iconAtlasKey]int32
	next        int32
	unsupported map[*sdl.Renderer]bool
	copies      int64 // blits served from the atlas
	bakes       int64 // glyphs baked (one per unknown key)
	direct      int64 // fallback primitive paints on renderers without targets
}

var homeIconAtlas = &iconAtlas{
	slots:       map[iconAtlasKey]int32{},
	unsupported: map[*sdl.Renderer]bool{},
}

// directPaint records a glyph the atlas dropped and the caller is painting
// directly (unsupported renderer, non-canonical box, or a rare Copy failure).
func (a *iconAtlas) directPaint() {
	a.mu.Lock()
	a.direct++
	a.mu.Unlock()
}

// IconAtlasStats reports how the glyph set split between atlas blits, bakes
// and direct paints since the last renderer switch or clear. Diagnostics,
// following ShapeCacheStats.
func IconAtlasStats() (copies, bakes, direct int64) {
	homeIconAtlas.mu.Lock()
	defer homeIconAtlas.mu.Unlock()
	return homeIconAtlas.copies, homeIconAtlas.bakes, homeIconAtlas.direct
}

// dropForRenderer forgets a renderer's atlas without destroying the strip: when
// the renderer is gone, its textures are gone with it. Mirrors
// shapeCache.dropForRenderer so probe tests stay stable across many renderers.
func (a *iconAtlas) dropForRenderer(r *sdl.Renderer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.renderer == r {
		a.renderer = nil
		a.texture = nil
		a.slots = map[iconAtlasKey]int32{}
		a.next = 0
		a.copies, a.bakes, a.direct = 0, 0, 0
	}
	delete(a.unsupported, r)
}

// ClearIconAtlas destroys the baked strip and forgets the slots. Call it
// wherever ClearShapeCache runs: a theme change repaints the glyphs in the new
// palette, and shutdown frees the texture.
func ClearIconAtlas() {
	a := homeIconAtlas
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.texture != nil {
		a.texture.Destroy()
	}
	a.renderer = nil
	a.texture = nil
	a.slots = map[iconAtlasKey]int32{}
	a.next = 0
	a.copies, a.bakes, a.direct = 0, 0, 0
}

// DropIconAtlasForRenderer forgets an atlas owned by a renderer that is about
// to be destroyed, without destroying it itself.
func DropIconAtlasForRenderer(r *sdl.Renderer) {
	if r == nil {
		return
	}
	homeIconAtlas.dropForRenderer(r)
}

// atlasSlotRect is the strip sub-rect holding glyph slot.
func atlasSlotRect(slot int32) sdl.Rect {
	return sdl.Rect{
		X: slot % iconAtlasCols * iconAtlasGlyph,
		Y: slot / iconAtlasCols * iconAtlasGlyph,
		W: iconAtlasGlyph,
		H: iconAtlasGlyph,
	}
}

// blit draws the named glyph into dst from the atlas when possible, baking the
// glyph on first use. Only exact 32px boxes are atlas-served; anything else
// returns false so the caller paints primitives at its own size. It also
// returns false when dropped — no render targets, no renderer, a degenerate
// dst, or a Copy/texture-creation failure — with the same fallback.
func (a *iconAtlas) blit(renderer *sdl.Renderer, name string, focused bool, dst sdl.Rect) bool {
	if renderer == nil || dst.W != iconAtlasGlyph || dst.H != iconAtlasGlyph {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unsupported[renderer] {
		return false
	}
	if a.renderer != renderer || a.texture == nil {
		// A new renderer (or the first frame): a fresh atlas. Do not
		// Destroy a strip owned by the previous renderer — its textures
		// died with it.
		a.renderer = renderer
		a.texture = nil
		a.slots = map[iconAtlasKey]int32{}
		a.next = 0
		a.copies, a.bakes, a.direct = 0, 0, 0
	}
	key := iconAtlasKey{name: name, focused: focused}
	slot, baked := a.slots[key]
	if !baked {
		if a.next >= iconAtlasCapacity() {
			// Every slot is a distinct glyph name in use; rebuilding
			// costs one bake per glyph the frame actually draws, the
			// frame after a config with that many unknown icons.
			a.texture = nil
			a.slots = map[iconAtlasKey]int32{}
			a.next = 0
		}
		if err := a.bakeLocked(renderer, name, focused, a.next); err != nil {
			a.unsupported[renderer] = true
			return false
		}
		slot = a.next
		a.slots[key] = slot
		a.next = slot + 1
		a.bakes++
	} else {
		a.copies++
	}
	src := atlasSlotRect(slot)
	if err := renderer.Copy(a.texture, &src, &dst); err != nil {
		// Rare (e.g. targets out of range during a resize). Keep the
		// atlas for the next attempt, but paint this draw directly.
		return false
	}
	return true
}

// bakeLocked paints one glyph into the atlas's next slot. The painter runs at
// explicit slot coordinates, so the nested shape-cache target switches inside
// the glyph's primitives (each one renders a small texture and then restores
// our strip as the target) cannot clobber anything — no per-slot viewport is
// involved. The glyph is painted over the transparent strip the same way
// shapeCache bakes its shapes, so the per-draw Copy composites over the frame
// the way the direct renderer did.
func (a *iconAtlas) bakeLocked(renderer *sdl.Renderer, name string, focused bool, slot int32) error {
	if a.texture == nil {
		tex, err := renderer.CreateTexture(uint32(sdl.PIXELFORMAT_RGBA8888), sdl.TEXTUREACCESS_TARGET,
			iconAtlasCols*iconAtlasGlyph, iconAtlasRows*iconAtlasGlyph)
		if err != nil {
			return err
		}
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)

		oldTarget := renderer.GetRenderTarget()
		_ = renderer.SetRenderTarget(tex)
		_ = renderer.SetDrawColor(0, 0, 0, 0)
		_ = renderer.Clear()
		// On the default backbuffer GetRenderTarget() returns nil; a nil
		// restore still returns off the target, so always restore.
		_ = renderer.SetRenderTarget(oldTarget)
		a.texture = tex
	}
	slotRect := atlasSlotRect(slot)
	var oldBlend sdl.BlendMode
	_ = renderer.GetDrawBlendMode(&oldBlend)
	oldTarget := renderer.GetRenderTarget()

	if err := renderer.SetRenderTarget(a.texture); err != nil {
		return err
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	drawHomeIconShapes(renderer, name, slotRect.X, slotRect.Y, iconAtlasGlyph, iconAtlasGlyph, focused)
	_ = renderer.SetRenderTarget(oldTarget)
	_ = renderer.SetDrawBlendMode(oldBlend)
	return nil
}
