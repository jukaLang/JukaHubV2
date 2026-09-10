package main

import (
	"image/color"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestSurfaceHighlight(t *testing.T) {
	c := SurfaceHighlight(42)
	if c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("SurfaceHighlight alpha should preserve white RGB, got %v", c)
	}
	if c.A != 42 {
		t.Errorf("SurfaceHighlight alpha mismatch: got %d, want 42", c.A)
	}
}

func TestTopSheen(t *testing.T) {
	c := TopSheen(18)
	if c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("TopSheen alpha should preserve white RGB, got %v", c)
	}
	if c.A != 18 {
		t.Errorf("TopSheen alpha mismatch: got %d, want 18", c.A)
	}
}

func TestDrawDividerNilRenderer(t *testing.T) {
	drawDivider(nil, 0, 0, 100, ColorDivider)
}

func TestDrawDividerZeroWidth(t *testing.T) {
	drawDivider(nil, 0, 0, 0, ColorDivider)
}

func TestDrawChipNilRenderer(t *testing.T) {
	drawChip(nil, sdl.Rect{}, "test", ColorChip, ColorTextPrimary(), nil)
}

func TestDrawChipEmptyLabel(t *testing.T) {
	drawChip(nil, sdl.Rect{}, "", ColorChip, ColorTextPrimary(), nil)
}

func TestDrawSimpleProgressNilRenderer(t *testing.T) {
	drawSimpleProgress(nil, 0, 0, 100, 8, 0.5)
}

func TestDrawSimpleProgressZeroWidth(t *testing.T) {
	drawSimpleProgress(nil, 0, 0, 0, 8, 0.5)
}

func TestDrawSimpleProgressZeroHeight(t *testing.T) {
	drawSimpleProgress(nil, 0, 0, 100, 0, 0.5)
}

func TestDrawSimpleProgressClampsFraction(t *testing.T) {
	// Verify fraction > 1 is clamped to 1.
	drawSimpleProgress(nil, 0, 0, 200, 10, 1.5)
	drawSimpleProgress(nil, 0, 0, 200, 10, -0.2)
}

func TestDesignTokenColorsAreValid(t *testing.T) {
	tokens := []struct {
		name string
		c    sdl.Color
	}{
		{"ColorTrack", ColorTrack},
		{"ColorProgress", ColorProgress},
		{"ColorDivider", ColorDivider},
		{"ColorChip", ColorChip},
		{"ColorChipHover", ColorChipHover},
		{"ColorChipFocus", ColorChipFocus},
		{"ColorIconSurface", ColorIconSurface},
		{"ColorIconDark", ColorIconDark},
		{"ColorIconTertiary", ColorIconTertiary},
	}

	for _, tk := range tokens {
		if tk.c.R > 255 || tk.c.G > 255 || tk.c.B > 255 || tk.c.A > 255 {
			t.Errorf("%s has out-of-range color component: %v", tk.name, tk.c)
		}
	}
}

// TestDesignTokenColorStringConversion verifies that design token colors
// can be converted to standard Go color.Color without panicking.
func TestDesignTokenColorConversion(t *testing.T) {
	tokens := []sdl.Color{
		ColorTrack,
		ColorProgress,
		ColorDivider,
		ColorChip,
		ColorIconSurface,
		ColorIconDark,
		ColorIconTertiary,
	}

	for i, c := range tokens {
		gc := color.Color(c)
		r, g, b, a := gc.RGBA()
		// RGBA returns values in [0, 0xFFFF], so check they're in range.
		if r > 0xFFFF || g > 0xFFFF || b > 0xFFFF || a > 0xFFFF {
			t.Errorf("token %d RGBA out of range: %v", i, gc)
		}
	}
}
