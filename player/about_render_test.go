package main

// About used to be the one screen with no status bar and no footer badges, and
// its licence list ran over the bottom hint. These checks render the real scene
// and look at the pixels: chrome present, nothing in the gap above the footer.

import (
	"os"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// brightPixelsInRect counts near-white pixels in a region of a rendered frame.
func brightPixelsInRect(f *probeFrame, x0, y0, x1, y1 int32) int {
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			c := f.at(x, y)
			if int(c.R)+int(c.G)+int(c.B) > 420 {
				n++
			}
		}
	}
	return n
}

func TestAboutSceneDrawsTheSharedChrome(t *testing.T) {
	if _, err := os.Stat("jukaconfig.json"); err != nil {
		t.Fatalf("jukaconfig.json ships next to the tests: %v", err)
	}
	config, _, err := LoadXMLConfigWithFallback("jukaconfig.json")
	cancelPendingUserConfigSave(t)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	idx := sceneIndexExact(config, "About")
	if idx < 0 {
		t.Fatal("config has no About scene")
	}

	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	prevScene, prevConfig := currentSceneIndex, appConfig
	currentSceneIndex, appConfig = idx, config
	t.Cleanup(func() { currentSceneIndex, appConfig = prevScene, prevConfig })

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderScene(renderer, config, config.Scenes[idx])
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}

	// The status bar (brand, scene title, clock, bell) and the footer badges
	// are drawn from text, so both bands must contain bright pixels.
	if n := brightPixelsInRect(f, 0, 0, probeW, HomeTopBarH); n < 200 {
		t.Errorf("About drew %d bright pixels in the status bar: the header is missing", n)
	}
	if n := brightPixelsInRect(f, 0, probeH-HomeFooterH, probeW, probeH); n < 100 {
		t.Errorf("About drew %d bright pixels in the footer: the button hints are missing", n)
	}
	// The 16px gap between the content panel and the footer band is padding;
	// text there means a card overflowed again.
	if n := brightPixelsInRect(f, 0, probeH-HomeFooterH-16, probeW, probeH-HomeFooterH); n > 0 {
		t.Errorf("%d bright pixels in the gap above the footer: content overflows the panel", n)
	}

	// The guard above only counts near-white pixels, so it cannot see the
	// licence list: that text is tertiary (dim) colour. The Open Source card
	// used to stack eight licences in one column — 54px of title plus 8×20px of
	// lines is 214px inside a 196px card — and the last two bled over the card's
	// own bottom border into that gap, straight past this test. renderAbout now
	// measures what it painted, so the overflow is caught directly: no line may
	// reach past the card that contains it.
	if aboutCardBottom == 0 || aboutContentBottom == 0 {
		t.Fatalf("About reported no measurements (card=%d content=%d): the scene did not render",
			aboutCardBottom, aboutContentBottom)
	}
	if aboutContentBottom > aboutCardBottom {
		t.Errorf("About content reaches y=%d but its card ends at y=%d: %dpx of text overflows the card",
			aboutContentBottom, aboutCardBottom, aboutContentBottom-aboutCardBottom)
	}

	// And the licences themselves must be where the two-column layout puts them:
	// inside the card, never in the footer gap.
	if aboutContentBottom > probeH-HomeFooterH {
		t.Errorf("About content reaches y=%d, past the footer boundary at y=%d",
			aboutContentBottom, probeH-HomeFooterH)
	}
}
