package main

import (
	"math"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// testLuma is the relative luminance of a colour, for contrast assertions.
func testLuma(c sdl.Color) float64 {
	lin := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.04045 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// testBlendOver composites fg over bg in sRGB space, which is what the renderer
// does with BLENDMODE_BLEND.
func testBlendOver(fg, bg sdl.Color) sdl.Color {
	a := float64(fg.A) / 255
	mix := func(f, b uint8) uint8 {
		return uint8(math.Round(float64(f)*a + float64(b)*(1-a)))
	}
	return sdl.Color{R: mix(fg.R, bg.R), G: mix(fg.G, bg.G), B: mix(fg.B, bg.B), A: 255}
}

// TestThemeTrackReadsOnLightTheme is the regression test for the track token:
// it was a hardcoded white 34/255 for every theme, so on the shipped Light
// preset the progress tracks (download rows, the storage bar, installs, seeks,
// the activity rail) were white on a #f0f2f5 page and disappeared, leaving
// every bar as a fill floating in space. ApplyThemeColors now re-inks the
// track from the preset's text colour.
func TestThemeTrackReadsOnLightTheme(t *testing.T) {
	prev := capturePalette()
	defer restorePalette(prev)
	ApplyThemeColors(themePresets["light"])
	over := testBlendOver(ColorTrack, ColorBackground)
	delta := math.Abs(testLuma(over) - testLuma(ColorBackground))
	if delta < 0.05 {
		t.Fatalf("track over the Light background moves relative luminance by only %.3f (track %v over %v): the progress bar has no visible track",
			delta, ColorTrack, ColorBackground)
	}
	// Prove the assertion discriminates: the old hardcoded white 34/255 must
	// fail the same check, otherwise this test would pass on the bug too.
	old := testBlendOver(sdl.Color{R: 255, G: 255, B: 255, A: designTrackAlpha}, ColorBackground)
	oldDelta := math.Abs(testLuma(old) - testLuma(ColorBackground))
	if oldDelta >= 0.05 {
		t.Fatalf("the Light background does not expose the old white track (%.3f): pick a threshold the bug fails", oldDelta)
	}
}

// TestThemeTrackKeepsDarkLookAndAlpha makes sure the fix is a re-ink, not a
// redesign: the dark preset's track stays the near-white hairline it always
// was, at the authored alpha.
func TestThemeTrackKeepsDarkLookAndAlpha(t *testing.T) {
	prev := capturePalette()
	defer restorePalette(prev)
	ApplyThemeColors(themePresets["dark"])
	if ColorTrack.A != designTrackAlpha {
		t.Fatalf("track alpha = %d, want %d", ColorTrack.A, designTrackAlpha)
	}
	if ColorTrack.R < 235 || ColorTrack.G < 235 || ColorTrack.B < 235 {
		t.Fatalf("dark track = %v, want the near-white hairline", ColorTrack)
	}
}

// TestTrackSwatchIsOpaque pins the legend key: a swatch at the track's 34/255
// would be invisible in the legend row, so trackSwatch() is the same ink at
// full alpha, and it follows the theme.
func TestTrackSwatchIsOpaque(t *testing.T) {
	prev := capturePalette()
	defer restorePalette(prev)
	ApplyThemeColors(themePresets["light"])
	swatch := trackSwatch()
	if swatch.A != 255 {
		t.Fatalf("swatch alpha = %d, want 255", swatch.A)
	}
	if swatch.R != ColorTrack.R || swatch.G != ColorTrack.G || swatch.B != ColorTrack.B {
		t.Fatalf("swatch %v does not match the track ink %v", swatch, ColorTrack)
	}
	if swatch.R > 128 {
		t.Fatalf("Light swatch %v is still a light patch on a light legend", swatch)
	}
}
