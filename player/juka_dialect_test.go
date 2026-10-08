package main

import (
	"image/color"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestParsePosIntBounds(t *testing.T) {
	tests := []struct {
		input string
		want  int32
		ok    bool
	}{
		{"2147483647", 2147483647, true},
		{"2147483648", 0, false},
		{"-1", 0, false},
		{"0", 0, true},
	}
	for _, tt := range tests {
		got, ok := parsePosInt(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parsePosInt(%q) = (%d, %t), want (%d, %t)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseInt32Bounds(t *testing.T) {
	tests := []struct {
		input string
		want  int32
		ok    bool
	}{
		{"2147483647", 2147483647, true},
		{"-2147483648", -2147483648, true},
		{"2147483648", 0, false},
		{"-2147483649", 0, false},
		{"not-an-int", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseInt32(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseInt32(%q) = (%d, %t), want (%d, %t)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestResolveElementDimensionsBounds(t *testing.T) {
	tests := []struct {
		name          string
		width         string
		height        string
		defaultWidth  int32
		defaultHeight int32
		wantWidth     int32
		wantHeight    int32
	}{
		{"input defaults for empty values", "", "", 200, 40, 200, 40},
		{"toggle defaults for empty values", "", "", 320, 48, 320, 48},
		{"accept int32 limits", "2147483647", "-2147483648", 200, 40, 2147483647, -2147483648},
		{"fallback on overflow independently", "2147483648", "64", 320, 48, 320, 64},
		{"fallback on underflow independently", "320", "-2147483649", 320, 48, 320, 48},
		{"fallback on malformed independently", "bad", "48px", 200, 40, 200, 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			element := Element{Width: StringOrInt(tt.width), Height: StringOrInt(tt.height)}
			width, height := resolveElementDimensions(element, tt.defaultWidth, tt.defaultHeight)
			if width != tt.wantWidth || height != tt.wantHeight {
				t.Errorf("resolveElementDimensions() = (%d, %d), want (%d, %d)", width, height, tt.wantWidth, tt.wantHeight)
			}
		})
	}
}

func TestResolveButtonDimensionsBounds(t *testing.T) {
	const defaultWidth, defaultHeight = int32(156), int32(56)
	tests := []struct {
		name       string
		width      string
		height     string
		wantWidth  int32
		wantHeight int32
	}{
		{"defaults for missing values", "", "", defaultWidth, defaultHeight},
		{"valid positive overrides", "320", "64", 320, 64},
		{"int32 maximum override", "2147483647", "2147483647", 2147483647, 2147483647},
		{"fallback for oversized width only", "2147483648", "64", defaultWidth, 64},
		{"fallback for underflowed height only", "320", "-2147483649", 320, defaultHeight},
		{"fallback for nonpositive values", "-1", "0", defaultWidth, defaultHeight},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			element := Element{Width: StringOrInt(tt.width), Height: StringOrInt(tt.height)}
			width, height := resolveButtonDimensions(element, 100, 20)
			if width != tt.wantWidth || height != tt.wantHeight {
				t.Errorf("resolveButtonDimensions() = (%d, %d), want (%d, %d)", width, height, tt.wantWidth, tt.wantHeight)
			}
		})
	}
}

func TestUnitConverterInputWidthBounds(t *testing.T) {
	tests := []struct {
		input int
		want  int32
	}{
		{-1, 0},
		{260, 0},
		{261, 1},
		{4259, 3999},
		{4260, 4000},
		{int(^uint(0) >> 1), 4000},
	}
	for _, tt := range tests {
		if got := unitConverterInputWidth(tt.input); got != tt.want {
			t.Errorf("unitConverterInputWidth(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

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
