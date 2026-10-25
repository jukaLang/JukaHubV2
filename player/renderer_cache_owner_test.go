package main

// renderer_cache_owner_test.go — the static-layer caches (keyboard, chat and
// video overlay) hold textures owned by the renderer that built them. SDL frees
// those textures with that renderer, so a cache reused after the renderer is
// gone hands a dangling pointer to Copy, and freeing it later corrupts the
// allocator — the intermittent SDL_RenderReadPixels crash in the suite.
//
// These tests pin the ownership rule without reproducing undefined behaviour:
// a second renderer must never receive the first renderer's texture, it must
// rebuild, and the caches must be droppable before their renderer dies.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func cacheProbeRenderer(t *testing.T, name string) *sdl.Renderer {
	t.Helper()
	if renderWin == nil {
		newProbeFrame(t) // initialises SDL video/ttf the way probes do
	}
	if renderErr != nil {
		t.Skipf("SDL video unavailable: %v", renderErr)
	}
	win, err := sdl.CreateWindow(name, 0, 0, probeW, probeH, sdl.WINDOW_HIDDEN)
	if err != nil {
		t.Skipf("window unavailable: %v", err)
	}
	renderer, err := sdl.CreateRenderer(win, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		win.Destroy()
		t.Skipf("software renderer unavailable: %v", err)
	}
	t.Cleanup(func() {
		destroyTestRenderer(renderer)
		win.Destroy()
	})
	return renderer
}

func TestVideoOverlayCacheIsNotSharedAcrossRenderers(t *testing.T) {
	a := cacheProbeRenderer(t, "cache-owner-a")
	b := cacheProbeRenderer(t, "cache-owner-b")

	texA := vkEnsureVideoOverlayCache(a, &Config{}, 320, 180)
	if texA == nil {
		t.Skip("this renderer has no render-target support")
	}
	buildsA := vkVideoOverlayBuilds

	texB := vkEnsureVideoOverlayCache(b, &Config{}, 320, 180)
	if texB == nil {
		t.Skip("second renderer has no render-target support")
	}
	if texB == texA {
		t.Fatal("second renderer reused a texture owned by the first")
	}
	if vkVideoOverlayBuilds == buildsA {
		t.Fatal("cache was not rebuilt for the new renderer")
	}
	if vkVideoOverlayOwner != b {
		t.Fatal("cache owner was not updated to the live renderer")
	}
}

func TestKeyboardAndChatCachesAreNotSharedAcrossRenderers(t *testing.T) {
	a := cacheProbeRenderer(t, "cache-owner-c")
	b := cacheProbeRenderer(t, "cache-owner-d")

	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = 640, 480
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	vkPanelRect = sdl.Rect{X: 20, Y: 20, W: 600, H: 440}
	font, _ := getCachedFont(&Config{}, "small")
	medium, _ := getCachedFont(&Config{}, "medium")
	if font == nil || medium == nil {
		t.Skip("no font available")
	}

	keyA := vkEnsureStaticLayer(a, &Config{}, medium, font)
	if keyA != nil {
		keyB := vkEnsureStaticLayer(b, &Config{}, medium, font)
		if keyB == keyA {
			t.Error("keyboard cache crossed renderers")
		}
		if vkStaticOwner != b {
			t.Error("keyboard cache owner was not updated")
		}
	}

	chatA := vkEnsureChatCache(a, &Config{}, 20, 20, 600, 400)
	if chatA != nil {
		chatB := vkEnsureChatCache(b, &Config{}, 20, 20, 600, 400)
		if chatB == chatA {
			t.Error("chat cache crossed renderers")
		}
		if vkChatStaticOwner != b {
			t.Error("chat cache owner was not updated")
		}
	}
}

// Dropping the caches before the renderer is destroyed must leave no dangling
// texture behind; that is what destroyTestRenderer now guarantees for every
// probe in the suite.
func TestDestroyCachesClearsOwnership(t *testing.T) {
	r := cacheProbeRenderer(t, "cache-owner-e")
	vkEnsureVideoOverlayCache(r, &Config{}, 320, 180)
	DestroyVKVideoOverlayCache()
	if vkVideoOverlayTex != nil || vkVideoOverlayOwner != nil || vkVideoOverlayValid {
		t.Fatal("video overlay cache was not fully released")
	}
	DestroyVKStaticCache()
	if vkStaticTex != nil || vkStaticOwner != nil || vkStaticValid {
		t.Fatal("keyboard cache was not fully released")
	}
	DestroyVKChatCache()
	if vkChatStaticTex != nil || vkChatStaticOwner != nil || vkChatStaticValid {
		t.Fatal("chat cache was not fully released")
	}
}
