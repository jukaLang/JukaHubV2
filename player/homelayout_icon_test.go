package main

// homelayout_icon_test.go — the home cards' glyphs.
//
// Two things went wrong here that no test noticed. The Emulator card asked for
// "emulator", a key the painter did not have (only "emulators"), so it drew the
// generic fallback ring while every card around it drew a real picture. And
// Files and Packages drew single-tone blobs: a folder whose tab and body were
// the same white, and a cube whose three faces were sixteen and thirty shades
// apart — on a white glyph that is white, so both read as blank rectangles at
// tile size. These checks pin the fix for both.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// homeCardIcons are the icon keys NewHomeLayout asks DrawHomeIcon for. Kept in
// step with rebuildItems; a card whose key is missing here is a card nobody has
// looked at.
func homeCardIcons() []string {
	return []string{
		"continue", "media", "files", "packages", "apps", "favorites",
		"browser", "chat", "tools", "emulator", "settings", "clock",
	}
}

// glyphPixels draws one glyph at its canonical 32px on a cleared frame and
// returns the 32x32 RGBA bytes.
func glyphPixels(t *testing.T, renderer *sdl.Renderer, name string, focused bool) []byte {
	t.Helper()
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	drawHomeIconShapes(renderer, name, 0, 0, 32, 32, focused)
	buf := make([]byte, 32*32*4)
	rect := sdl.Rect{X: 0, Y: 0, W: 32, H: 32}
	if err := renderer.ReadPixels(&rect, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), 32*4); err != nil {
		t.Fatalf("ReadPixels failed for %q: %v (the assertions that follow would otherwise never run)", name, err)
	}
	return buf
}

// Every home card must have its own glyph: none may fall through to the
// unknown-icon ring, which is what the Emulator card was drawing.
func TestHomeCardIconsAreNotTheFallbackGlyph(t *testing.T) {
	renderer := probeRenderer(t)
	fallback := glyphPixels(t, renderer, "not-an-icon-key", false)

	for _, name := range homeCardIcons() {
		got := glyphPixels(t, renderer, name, false)
		if string(got) == string(fallback) {
			t.Errorf("card icon %q draws the generic fallback ring: the painter has no case for it", name)
		}
	}
}

// glyphTones counts the distinct colours a glyph paints, and how much of its box
// is covered. A single-tone glyph is a silhouette; the icons that looked blank
// at tile size needed more than one plane to read.
func glyphTones(buf []byte) (tones int, coverage float64) {
	seen := map[[3]byte]bool{}
	lit := 0
	for i := 0; i+3 < len(buf); i += 4 {
		// ReadPixels returns ARGB in memory order B,G,R,A for ARGB8888.
		c := [3]byte{buf[i], buf[i+1], buf[i+2]}
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 0 {
			continue
		}
		seen[c] = true
		lit++
	}
	return len(seen), float64(lit) / float64(len(buf)/4)
}

// Files and Packages must read as more than one flat plane at tile size: at
// least three distinct tones (the body, a shaded panel or lid, and a seam or a
// ruled line), covering a substantial share of the box without becoming a
// solid block.
func TestFilesAndPackagesGlyphsHaveDepth(t *testing.T) {
	renderer := probeRenderer(t)
	for _, name := range []string{"files", "packages", "emulator"} {
		for _, focused := range []bool{false, true} {
			tones, coverage := glyphTones(glyphPixels(t, renderer, name, focused))
			minTones := 3
			if name == "emulator" {
				minTones = 2
			}
			if tones < minTones {
				t.Errorf("%q focused=%v paints %d tones; details must remain distinct", name, focused, tones)
			}
			if coverage < 0.2 || coverage > 0.9 {
				t.Errorf("%q focused=%v covers %.0f%% of its box", name, focused, coverage*100)
			}
		}
	}
}
