package main

// ──────────────────────────────────────────────────────────────────────
// Shape texture cache
//
// fillRoundedRect / strokeRoundedRect / fillCircle / strokeCircle draw every
// card, ring and dial with many small renderer calls. On the handheld's GLES2
// renderer each call is its own draw pass, so a busy panel costs hundreds of
// passes per frame; on the software renderer each pass is a CPU fill. This
// cache renders a shape once into a texture and re-issues one blended Copy per
// frame, the same idea as the text texture cache but for primitives. The
// stored shape is opaque in the cache colour; the per-draw alpha reaches the
// frame through SetAlphaMod, so blending over the background is unchanged.
//
// Caches are renderer-local: a mismatch on the current renderer drops the
// entries (tests create many short-lived renderers; dropping without Destroy
// avoids touching textures owned by an already destroyed renderer).
// ──────────────────────────────────────────────────────────────────────

import (
	"container/list"
	"log"
	"os"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// shapeKinds names which primitive produced the entry.
const (
	shapeKindFillRound = iota
	shapeKindStrokeRound
	shapeKindFillCircle
	shapeKindStrokeCircle
)

// shapeTextureMaxSize caps the geometry we cache; nothing in the shell should
// legitimately exceed this and a pathological size must never eat the budget.
const shapeTextureMaxSize int32 = 2048

// shapeCacheBudgetBytes limits one renderer's cached shape memory. The
// handheld shares its 1 GB with ffmpeg decoders and SDL's frame pools, so the
// shape cache stays smaller there.
const (
	shapeBudgetHandheldBytes = int64(12 << 20)
	shapeBudgetDesktopBytes  = int64(48 << 20)
)

// shapeCacheBudgetBytes is the budget in force for this process, or for the
// branch a test pinned. It is a function rather than a field captured at
// construction: the harness pins the handheld branch after the cache already
// exists, and a cache that kept the budget it was born with would go on
// measuring the host.
//
// The branch is the device's, not the platform's. It used to be "Windows or
// not", which gave a Linux desktop the handheld ceiling and, more to the point,
// gave CI a different number from the machine the work was done on.
func shapeCacheBudgetBytes() int64 {
	return sceneBudgetByDevice(shapeBudgetHandheldBytes, shapeBudgetDesktopBytes)
}

type shapeCacheKey struct {
	kind      int32
	renderer  *sdl.Renderer
	color     sdl.Color // RGB only; alpha is always opaque in the stored shape
	w, h, r   int32
	thickness int32
}

type shapeCacheEntry struct {
	texture *sdl.Texture
	w, h    int32
	bytes   int64
	element *list.Element // front = most recently used
}

type shapeCache struct {
	mu            sync.Mutex
	entries       map[shapeCacheKey]*shapeCacheEntry
	order         *list.List
	current       *sdl.Renderer // entries belong to this renderer
	maxEntries    int
	totalBytes    int64
	textureDraws  int64
	fallbackDraws int64
}

var shapeTextures = newShapeCache()

func newShapeCache() *shapeCache {
	return &shapeCache{
		entries:    make(map[shapeCacheKey]*shapeCacheEntry),
		order:      list.New(),
		maxEntries: 512,
	}
}

func (sc *shapeCache) budget() int64 {
	return shapeCacheBudgetBytes()
}

// dropForRenderer forgets every entry owned by r without Destroy: when the
// renderer is gone, its textures are gone with it. Used on renderer switches
// and from the test renderer teardown, which creates a fresh renderer per case.
func (sc *shapeCache) dropForRenderer(r *sdl.Renderer) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for key, entry := range sc.entries {
		if key.renderer == r {
			delete(sc.entries, key)
			sc.order.Remove(entry.element)
		}
	}
	if sc.current == r {
		sc.current = nil
	}
	sc.totalBytes = 0
	sc.textureDraws = 0
	sc.fallbackDraws = 0
}

// Get returns the cached texture for the key, moving it to the front and
// making the cache table renderer-local. nil marks a miss.
func (sc *shapeCache) Get(key shapeCacheKey) *sdl.Texture {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.current != key.renderer {
		// A new renderer invalidates the whole table: forget every entry.
		for storedKey, entry := range sc.entries {
			delete(sc.entries, storedKey)
			sc.order.Remove(entry.element)
		}
		sc.totalBytes = 0
		sc.current = key.renderer
	}
	entry, ok := sc.entries[key]
	if !ok {
		return nil
	}
	sc.order.MoveToFront(entry.element)
	sc.textureDraws++
	return entry.texture
}

// Put stores the texture, evicting LRU entries beyond the byte budget.
func (sc *shapeCache) Put(key shapeCacheKey, tex *sdl.Texture, w, h int32) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	entryBytes := int64(w) * int64(h) * 4

	if entry, ok := sc.entries[key]; ok {
		if entry.texture != nil {
			entry.texture.Destroy()
		}
		sc.totalBytes += entryBytes - entry.bytes
		entry.texture = tex
		entry.w, entry.h = w, h
		entry.bytes = entryBytes
		sc.order.MoveToFront(entry.element)
		return
	}

	for sc.totalBytes+entryBytes > sc.budget() || len(sc.entries) >= sc.maxEntries {
		oldest := sc.order.Back()
		if oldest == nil {
			break
		}
		evictKey := oldest.Value.(shapeCacheKey)
		sc.order.Remove(oldest)
		if entry, ok := sc.entries[evictKey]; ok {
			if entry.texture != nil {
				entry.texture.Destroy()
			}
			sc.totalBytes -= entry.bytes
			delete(sc.entries, evictKey)
		}
	}

	entry := &shapeCacheEntry{texture: tex, w: w, h: h, bytes: entryBytes}
	entry.element = sc.order.PushFront(key)
	sc.entries[key] = entry
	sc.totalBytes += entryBytes
}

// Clear destroys every cached texture (theme change, shutdown).
func (sc *shapeCache) Clear() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for key, entry := range sc.entries {
		if entry.texture != nil {
			entry.texture.Destroy()
		}
		delete(sc.entries, key)
	}
	sc.order.Init()
	sc.totalBytes = 0
}

// shapeDrawFn paints the shape inside a w×h box with its top-left at (ox, oy).
// The cache calls it with ox=oy=0 while filling the opaque cached texture, and
// direct paths call it with the real destination so fallback logic draws in
// place without a second code path.
type shapeDrawFn func(renderer *sdl.Renderer, ox, oy, w, h int32)

// rendererSupportsTargets keeps one disabled renderer from retrying a failing
// texture teardown or CreateTexture per frame.
var (
	shapeTargetUnsupported   = map[*sdl.Renderer]bool{}
	shapeTargetUnsupportedMu sync.Mutex
)

func shapeIsCacheable(w, h int32) bool {
	return w > 0 && h > 0 && w <= shapeTextureMaxSize && h <= shapeTextureMaxSize
}

// drawShapeCached either Copy-blits a cached texture for the primitive or
// falls back to painting it with paint. paint must draw the shape at (0, 0)
// inside a w×h surface and use the exact RGB in c with full alpha, so the
// stored texture can be alpha-modulated per draw.
func debugShapeErrors() bool { return os.Getenv("JUKAHUB_SHAPE_DEBUG") != "" }

func drawShapeCached(renderer *sdl.Renderer, kind int32, x, y, w, h, r, thickness int32, c sdl.Color, paint shapeDrawFn) {
	drawShapeCachedAt(renderer, kind, w, h, sdl.Rect{X: x, Y: y, W: w, H: h}, r, thickness, c, paint)
}

// drawShapeCachedAt caches the primitive at baseW×baseH and blits it into dst,
// which may be a few pixels larger or smaller than that base. It exists for
// shapes that animate: the home card's hover lift grows it by up to three
// pixels, and keyed by the grown size every step of that ease minted a fresh
// card surface and focus ring — half a megabyte each at 720p — which then sat in
// a 12 MB cache the handheld budgets for the whole screen, pushing out every
// other shape. One texture per card now serves the whole animation, scaled in
// the blit, so the motion stays smooth without new uploads.
func drawShapeCachedAt(renderer *sdl.Renderer, kind int32, baseW, baseH int32, dst sdl.Rect, r, thickness int32, c sdl.Color, paint shapeDrawFn) {
	x, y, w, h := dst.X, dst.Y, dst.W, dst.H
	if baseW <= 0 || baseH <= 0 {
		baseW, baseH = w, h
	}
	if renderer == nil || !shapeIsCacheable(baseW, baseH) {
		if renderer != nil {
			paint(renderer, x, y, w, h)
		}
		return
	}
	shapeTargetUnsupportedMu.Lock()
	disabled := shapeTargetUnsupported[renderer]
	shapeTargetUnsupportedMu.Unlock()
	if disabled {
		shapeTextures.fallbackDraws++
		paint(renderer, x, y, w, h)
		return
	}

	key := shapeCacheKey{
		kind:      kind,
		renderer:  renderer,
		color:     sdl.Color{R: c.R, G: c.G, B: c.B, A: 255},
		w:         baseW,
		h:         baseH,
		r:         r,
		thickness: thickness,
	}
	if tex := shapeTextures.Get(key); tex != nil {
		if err := tex.SetAlphaMod(c.A); err != nil && debugShapeErrors() {
			log.Printf("[shape] SetAlphaMod(%d) failed: %v", c.A, err)
		}
		dest := sdl.Rect{X: x, Y: y, W: w, H: h}
		if err := renderer.Copy(tex, nil, &dest); err == nil {
			return
		} else if debugShapeErrors() {
			log.Printf("[shape] copy kind=%d %dx%d failed: %v", kind, w, h, err)
		}
		// Copy failed; fall through and paint directly this frame.
	}

	// Render the shape once, opaque, into a target texture at (0, 0), at the
	// base size the key names; the blit below scales it onto dst.
	tex, err := renderer.CreateTexture(uint32(sdl.PIXELFORMAT_RGBA8888), sdl.TEXTUREACCESS_TARGET, baseW, baseH)
	if err == nil {
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)

		oldTarget := renderer.GetRenderTarget()
		oldClip := renderer.GetClipRect()
		or, og, ob, oa, _ := renderer.GetDrawColor()
		oldColor := sdl.Color{R: or, G: og, B: ob, A: oa}

		hadClip := oldClip.W > 0 && oldClip.H > 0

		_ = renderer.SetRenderTarget(tex)
		if err == nil {
			_ = renderer.SetClipRect(nil)
			_ = renderer.SetDrawColor(0, 0, 0, 0)
			_ = renderer.Clear()
			_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
			paint(renderer, 0, 0, baseW, baseH)
			// On the default backbuffer GetRenderTarget() returns nil; a nil
			// restore still returns off the target, so always restore.
			_ = renderer.SetRenderTarget(oldTarget)
			if hadClip {
				_ = renderer.SetClipRect(&oldClip)
			} else {
				_ = renderer.SetClipRect(nil)
			}
			_ = renderer.SetDrawColor(oldColor.R, oldColor.G, oldColor.B, oldColor.A)
		}
	}
	if err != nil {
		if tex != nil {
			tex.Destroy()
		}
		shapeTargetUnsupportedMu.Lock()
		shapeTargetUnsupported[renderer] = true
		shapeTargetUnsupportedMu.Unlock()
		shapeTextures.fallbackDraws++
		paint(renderer, x, y, w, h)
		return
	}

	shapeTextures.Put(key, tex, baseW, baseH)
	_ = tex.SetAlphaMod(c.A)
	dest := sdl.Rect{X: x, Y: y, W: w, H: h}
	if err := renderer.Copy(tex, nil, &dest); err != nil {
		// Rare (e.g. targets out of range during a resize); forget the entry.
		shapeTextures.Clear()
		paint(renderer, x, y, w, h)
	}
}

// ClearShapeCache destroys all cached shape textures. Call it alongside
// ClearTextCache on theme changes (theme colours are cache keys) and at
// shutdown.
func ClearShapeCache() {
	shapeTextures.Clear()
}

// DropShapeCacheForRenderer forgets textures owned by a renderer that is about
// to be destroyed, without destroying them itself.
func DropShapeCacheForRenderer(r *sdl.Renderer) {
	if r == nil {
		return
	}
	shapeTargetUnsupportedMu.Lock()
	delete(shapeTargetUnsupported, r)
	shapeTargetUnsupportedMu.Unlock()
	shapeTextures.dropForRenderer(r)
}

// ShapeCacheStats reports how the cache split draws between Copy-blits and
// direct painting, and the texture memory held. Diagnostics.
func ShapeCacheStats() (cached, fallback int64, bytes int64) {
	sc := shapeTextures
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.textureDraws, sc.fallbackDraws, sc.totalBytes
}
