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
	clearHomeTextCache()
	clearAppIconCache()
	// Avatar textures are cached by URL across frames; freeing them here keeps
	// a later test from drawing one that this renderer already destroyed.
	clearChatAvatarCache()
	r.Destroy()
}
