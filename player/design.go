package main

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// Design tokens for JukaHub.
// Modern sleek design system with glass morphism and refined surfaces.

var (
	// Spacing (4pt grid for tighter, more modern layout)
	SpaceXS = int32(4)
	SpaceSM = int32(8)
	SpaceMD = int32(12)
	SpaceLG = int32(16)
	SpaceXL = int32(24)
	Space2XL = int32(32)

	// Typography sizes (approximate px)
	FontSizeXS  = int32(10)
	FontSizeSM  = int32(12)
	FontSizeMD  = int32(14)
	FontSizeLG  = int32(18)
	FontSizeXL  = int32(24)
	FontSize2XL = int32(32)

	// Border radius
	RadiusSM = int32(6)
	RadiusMD = int32(10)
	RadiusLG = int32(14)
	RadiusXL = int32(20)

	// Surfaces - refined dark theme
	ColorBackground    = sdl.Color{R: 8, G: 10, B: 16, A: 255}
	ColorSurface       = sdl.Color{R: 14, G: 18, B: 26, A: 255}
	ColorSurfaceAlt    = sdl.Color{R: 20, G: 24, B: 34, A: 255}
	ColorSurfaceRaised = sdl.Color{R: 26, G: 32, B: 44, A: 255}

	// Borders - subtle and refined
	ColorBorderSubtle   = sdl.Color{R: 255, G: 255, B: 255, A: 8}
	ColorBorderDefault  = sdl.Color{R: 255, G: 255, B: 255, A: 16}
	ColorBorderFocus    = sdl.Color{R: 255, G: 255, B: 255, A: 50}

	// Semantic
	ColorSuccess = sdl.Color{R: 52, G: 211, B: 153, A: 255}
	ColorWarning = sdl.Color{R: 251, G: 191, B: 36, A: 255}
	ColorDanger  = sdl.Color{R: 248, G: 113, B: 113, A: 255}
	ColorInfo    = sdl.Color{R: 96, G: 165, B: 250, A: 255}

	// Shadow base (apply alpha at call site)
	ColorShadow = sdl.Color{R: 0, G: 0, B: 0, A: 255}

	// Overlay
	ColorOverlay = sdl.Color{R: 6, G: 8, B: 14, A: 200}

	// Extended surfaces (cards, rows, raised controls)
	ColorSurfaceCard  = sdl.Color{R: 18, G: 22, B: 32, A: 255}
	ColorSurfaceRow   = sdl.Color{R: 24, G: 28, B: 40, A: 255}
	ColorSurfacePanel = sdl.Color{R: 16, G: 19, B: 26, A: 255}
	ColorButtonRaised = sdl.Color{R: 44, G: 49, B: 62, A: 255}

	// Toast / notification semantic colors
	ColorToastError   = sdl.Color{R: 230, G: 80, B: 80, A: 255}
	ColorToastWarn    = sdl.Color{R: 230, G: 160, B: 80, A: 255}
	ColorToastInfo    = sdl.Color{R: 100, G: 200, B: 255, A: 255}
	ColorToastSuccess = sdl.Color{R: 52, G: 211, B: 153, A: 255}

	// Accent (injected from config at runtime)
	accentColor sdl.Color
)

// --- Text color accessors (called as functions throughout the UI) ---
//
// These resolve to mutable package-level vars so that ApplyThemeColors can
// retint the whole UI at runtime when the user picks a theme preset.

var (
	textPrimaryColor  = sdl.Color{R: 250, G: 252, B: 255, A: 255}
	textSecondaryColor = sdl.Color{R: 180, G: 192, B: 210, A: 255}
	textTertiaryColor = sdl.Color{R: 120, G: 134, B: 158, A: 255}
	textInverseColor  = sdl.Color{R: 14, G: 18, B: 26, A: 255}
	textAccentColor   = sdl.Color{R: 140, G: 200, B: 255, A: 255}
)

// ColorTextPrimary returns the primary text color.
func ColorTextPrimary() sdl.Color { return textPrimaryColor }

// ColorTextSecondary returns the secondary text color.
func ColorTextSecondary() sdl.Color { return textSecondaryColor }

// ColorTextTertiary returns the tertiary / placeholder text color.
func ColorTextTertiary() sdl.Color { return textTertiaryColor }

// ColorTextInverse returns the text color for use on accent fills.
func ColorTextInverse() sdl.Color { return textInverseColor }

// ColorTextAccent returns the accent-colored text.
func ColorTextAccent() sdl.Color { return textAccentColor }

// SetAccentColor updates the runtime accent color from config.
func SetAccentColor(c sdl.Color) {
	accentColor = c
}

// hexRGBA parses a #rrggbb or #rrggbbaa hex string into an sdl.Color.
// Missing alpha defaults to fully opaque.
func hexRGBA(hex string) sdl.Color {
	h := strings.TrimSpace(hex)
	if len(h) > 0 && h[0] == '#' {
		h = h[1:]
	}
	switch len(h) {
	case 8:
		r, _ := strconv.ParseUint(h[0:2], 16, 8)
		g, _ := strconv.ParseUint(h[2:4], 16, 8)
		b, _ := strconv.ParseUint(h[4:6], 16, 8)
		a, _ := strconv.ParseUint(h[6:8], 16, 8)
		return sdl.Color{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(a)}
	case 6:
		r, _ := strconv.ParseUint(h[0:2], 16, 8)
		g, _ := strconv.ParseUint(h[2:4], 16, 8)
		b, _ := strconv.ParseUint(h[4:6], 16, 8)
		return sdl.Color{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
	default:
		return sdl.Color{R: 255, G: 255, B: 255, A: 255}
	}
}

// ApplyThemeColors retints the core design tokens from a theme preset so the
// entire UI (backgrounds, surfaces, text, borders, accents) reflects the
// chosen theme.
func ApplyThemeColors(p ThemePreset) {
	ColorBackground = hexRGBA(p.Background)
	ColorSurface = hexRGBA(p.Surface)
	ColorSurfaceAlt = hexRGBA(p.SurfaceAlt)
	ColorSurfaceRaised = hexRGBA(p.SurfaceRaised)
	ColorSurfaceCard = hexRGBA(p.SurfaceRaised)
	ColorSurfaceRow = hexRGBA(p.SurfaceAlt)
	ColorSurfacePanel = hexRGBA(p.Surface)
	ColorButtonRaised = hexRGBA(p.SurfaceRaised)
	ColorBorderSubtle = hexRGBA(p.BorderSubtle)
	ColorBorderDefault = hexRGBA(p.BorderDefault)
	ColorBorderFocus = hexRGBA(p.BorderFocus)
	ColorSuccess = hexRGBA(p.Success)
	ColorWarning = hexRGBA(p.Warning)
	ColorDanger = hexRGBA(p.Danger)
	ColorInfo = hexRGBA(p.Info)
	ColorOverlay = hexRGBA(p.Overlay)
	overlayColor = ColorOverlay
	textPrimaryColor = hexRGBA(p.TextPrimary)
	textSecondaryColor = hexRGBA(p.TextSecondary)
	textTertiaryColor = hexRGBA(p.TextTertiary)
	textAccentColor = hexRGBA(p.Info)
	SetAccentColor(hexRGBA(p.Info))
	// Invalidate cached background/gradient textures so the new theme colors
	// are picked up on the very next frame.
	gradientTexture = nil
	gradientSceneKey = ""
}

// WithAlpha returns a copy of c with the alpha channel replaced.
func WithAlpha(c sdl.Color, a uint8) sdl.Color {
	c.A = a
	return c
}

// PanelFill returns the standard panel background color at the given alpha.
func PanelFill(alpha uint8) sdl.Color {
	return WithAlpha(ColorSurfaceAlt, alpha)
}

// CardFill returns the standard card/row background color.
func CardFill() sdl.Color {
	return ColorSurfaceRaised
}

// ShadowFill returns a shadow color at the given alpha.
func ShadowFill(alpha uint8) sdl.Color {
	return WithAlpha(ColorShadow, alpha)
}

// TextPrimary returns the primary text color.
func TextPrimary() sdl.Color {
	return ColorTextPrimary()
}

// TextSecondary returns the secondary text color.
func TextSecondary() sdl.Color {
	return ColorTextSecondary()
}

// TextTertiary returns the tertiary / placeholder text color.
func TextTertiary() sdl.Color {
	return ColorTextTertiary()
}

// ToastError returns the semantic error toast color.
func ToastError() sdl.Color { return ColorToastError }

// ToastWarn returns the semantic warning toast color.
func ToastWarn() sdl.Color { return ColorToastWarn }

// ToastInfo returns the semantic info toast color.
func ToastInfo() sdl.Color { return ColorToastInfo }

// ToastSuccess returns the semantic success toast color.
func ToastSuccess() sdl.Color { return ColorToastSuccess }

// GlossFill returns a white specular highlight at the given low alpha,
// used for subtle top-edge sheen on cards and panels.
func GlossFill(alpha uint8) sdl.Color {
	return sdl.Color{R: 255, G: 255, B: 255, A: alpha}
}
