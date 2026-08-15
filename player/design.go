package main

import "github.com/veandco/go-sdl2/sdl"

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
	colorBackground = sdl.Color{R: 8, G: 10, B: 16, A: 255}
	colorSurface    = sdl.Color{R: 14, G: 18, B: 26, A: 255}
	colorSurfaceAlt = sdl.Color{R: 20, G: 24, B: 34, A: 255}
	colorSurfaceRaised = sdl.Color{R: 26, G: 32, B: 44, A: 255}
	colorSurfaceOverlay = sdl.Color{R: 6, G: 8, B: 14, A: 230}

	// Borders - subtle and refined
	colorBorderSubtle = sdl.Color{R: 255, G: 255, B: 255, A: 8}
	colorBorderDefault = sdl.Color{R: 255, G: 255, B: 255, A: 16}
	colorBorderFocus = sdl.Color{R: 255, G: 255, B: 255, A: 50}

	// Text - improved hierarchy
	colorTextPrimary   = sdl.Color{R: 250, G: 252, B: 255, A: 255}
	colorTextSecondary = sdl.Color{R: 180, G: 192, B: 210, A: 255}
	colorTextTertiary  = sdl.Color{R: 120, G: 134, B: 158, A: 255}
	colorTextInverse   = sdl.Color{R: 14, G: 18, B: 26, A: 255}
	colorTextAccent    = sdl.Color{R: 140, G: 200, B: 255, A: 255}

	// Semantic
	colorSuccess = sdl.Color{R: 52, G: 211, B: 153, A: 255}
	colorWarning = sdl.Color{R: 251, G: 191, B: 36, A: 255}
	colorDanger  = sdl.Color{R: 248, G: 113, B: 113, A: 255}
	colorInfo    = sdl.Color{R: 96, G: 165, B: 250, A: 255}

	// Shadows
	shadowColor = sdl.Color{R: 0, G: 0, B: 0, A: 0}

	// Accent (injected from config at runtime)
	accentColor sdl.Color
)

// SetAccentColor updates the runtime accent color from config.
func SetAccentColor(c sdl.Color) {
	accentColor = c
}
