package main

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// probeBootConfig is a minimal scene the generic renderer can draw: enough to
// rasterise a label, a panel and the background, which is all the warm-up pass
// has to move off the first frame.
func probeBootConfig() *Config {
	config := &Config{
		Scenes: []SceneConfig{{
			Name: "BootProbe",
			Elements: []Element{{
				Type:   "button",
				Text:   "warm-up probe label",
				X:      80,
				Y:      140,
				Width:  "320",
				Height: "56",
			}},
		}},
	}
	config.Variables.Custom = map[string]interface{}{}
	return config
}

// warmProbeRenderer brings SDL up and hands back a software renderer whose
// cleanup leaves the shared, renderer-owned globals empty. The warm-up builds
// the background and gradient textures on the renderer it is given; left
// behind, the next probe draws a dead handle and comes back black.
func warmProbeRenderer(t *testing.T) *sdl.Renderer {
	t.Helper()
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v", err)
	}
	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() {
		bgTexture, gradientTexture = nil, nil
		destroyTestRenderer(renderer)
		screenWidth, screenHeight = prevW, prevH
	})
	return renderer
}

// The warm-up pass draws a whole scene during the splash, so the one thing it
// must never do is paint the window: everything it draws belongs in the
// throwaway target. If the target were ignored, the user would see the home
// screen flash underneath the boot animation.
func TestWarmSceneCachesDrawsOnlyIntoItsTarget(t *testing.T) {
	f := newProbeFrame(t)
	renderer := warmProbeRenderer(t)

	previousIndex, previousHome := currentSceneIndex, homeLayoutActive
	t.Cleanup(func() { currentSceneIndex, homeLayoutActive = previousIndex, previousHome })
	// renderScene reads the current scene from the global index, exactly as the
	// boot path does once the focus graph has picked scene 0.
	currentSceneIndex = 0
	homeLayoutActive = false

	config := probeBootConfig()
	before := GetTextCache().Bytes()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()

	warmSceneCaches(renderer, config, config.Scenes[0])

	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v", err)
	}
	for y := int32(0); y < probeH; y += 8 {
		for x := int32(0); x < probeW; x += 8 {
			if !f.isBG(x, y) {
				t.Fatalf("warm-up leaked onto the window at (%d,%d): got %+v, want the background", x, y, f.at(x, y))
			}
		}
	}

	// A warm-up that draws nothing would keep the window clean and still be
	// useless, so check the labels really did land in the cache.
	if after := GetTextCache().Bytes(); after <= before {
		t.Fatalf("warm-up cached nothing: text cache stayed at %d bytes", before)
	}

	// The target is released afterwards, so the next frame still lands on the
	// window instead of disappearing into a destroyed texture.
	renderer.SetDrawColor(200, 30, 30, 255)
	renderer.Clear()
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed after the warm-up: %v", err)
	}
	if got := f.at(16, 16); got.R < 150 || got.G > 90 {
		t.Fatalf("frame after the warm-up did not reach the window: got %+v", got)
	}
}

// Boot must survive a renderer or config that cannot be warmed: a scene with no
// pages, an empty config, or no renderer at all. The app still opens, and the
// renderer is left usable instead of bound to a destroyed target.
func TestWarmSceneCachesToleratesMissingInputs(t *testing.T) {
	warmSceneCaches(nil, nil, SceneConfig{})
	warmSceneCaches(nil, &Config{}, SceneConfig{})

	renderer := warmProbeRenderer(t)
	warmSceneCaches(renderer, &Config{}, SceneConfig{})

	// The renderer must still be drawing to the window after a refused warm-up.
	renderer.SetDrawColor(10, 200, 60, 255)
	renderer.Clear()
	buf := make([]byte, probeW*probeH*4)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed after a refused warm-up: %v", err)
	}
	i := (int32(40)*probeW + 40) * 4
	// ARGB8888 lands in memory as B,G,R,A on little-endian hosts.
	if buf[i] > 100 || buf[i+1] < 150 {
		t.Fatalf("renderer stopped drawing to the window after a refused warm-up: B=%d G=%d", buf[i], buf[i+1])
	}
}
