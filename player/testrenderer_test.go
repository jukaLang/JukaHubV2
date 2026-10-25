package main

import "github.com/veandco/go-sdl2/sdl"

// destroyTestRenderer releases an offscreen test renderer. Text textures are
// cached package-wide but owned by their renderer: SDL frees them when the
// renderer is destroyed, so a later ClearTextCache or theme switch would try to
// destroy a dangling pointer. Dropping the caches first keeps the shared test
// process stable across hundreds of probes.
func destroyTestRenderer(r *sdl.Renderer) {
	if r == nil {
		return
	}
	ClearTextCache()
	// Shape textures follow the same rule as text textures: SDL frees them
	// with the renderer, so drop the cache first. The icon atlas' strip is
	// renderer-owned too.
	DropShapeCacheForRenderer(r)
	DropIconAtlasForRenderer(r)
	clearHomeTextCache()
	clearAppIconCache()
	// Avatar textures are cached by URL across frames; freeing them here keeps
	// a later test from drawing one that this renderer already destroyed.
	clearChatAvatarCache()
	// The static-layer caches (on-screen keyboard, chat, video overlay) are
	// renderer-owned textures too. They used to survive the renderer: a later
	// probe rebuilt them against a dead renderer or freed the dangling pointer,
	// which corrupted SDL's allocator and crashed an unrelated ReadPixels run
	// further down the suite. Drop them while their renderer is still alive.
	DestroyVKStaticCache()
	DestroyVKChatCache()
	DestroyVKVideoOverlayCache()
	r.Destroy()
}
