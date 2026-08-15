package main

import "github.com/veandco/go-sdl2/sdl"

// Design tokens for JukaHub.
// Principles:
//  - 8pt spacing grid
//  - Clear hierarchy for small screens
//  - High contrast on dark surfaces
//  - Accent used sparingly for selection / primary actions

var (
	// Spacing (8pt grid)
	SpaceXS = int32(4)
	SpaceSM = int32(8)
	SpaceMD = int32(16)
	SpaceLG = int32(24)
	SpaceXL = int32(32)
	Space2XL = int32(48)

	// Typography sizes (approximate px)
	FontSizeXS  = int32(11)
	FontSizeSM  = int32(13)
	FontSizeMD  = int32(16)
	FontSizeLG  = int32(20)
	FontSizeXL  = int32(28)
	FontSize2XL = int32(36)

	// Border radius
	RadiusSM = int32(8)
	RadiusMD = int32(12)
	RadiusLG = int32(16)
	RadiusXL = int32(24)

	// Surfaces
	colorBackground = sdl.Color{R: 10, G: 12, B: 18, A: 255}
	colorSurface    = sdl.Color{R: 18, G: 22, B: 30, A: 255}
	colorSurfaceAlt = sdl.Color{R: 26, G: 30, B: 42, A: 255}
	colorSurfaceRaised = sdl.Color{R: 32, G: 38, B: 54, A: 255}
	colorSurfaceOverlay = sdl.Color{R: 8, G: 10, B: 16, A: 210}

	// Borders
	colorBorderSubtle = sdl.Color{R: 255, G: 255, B: 255, A: 18}
	colorBorderDefault = sdl.Color{R: 255, G: 255, B: 255, A: 35}
	colorBorderFocus = sdl.Color{R: 255, G: 255, B: 255, A: 120}

	// Text
	colorTextPrimary   = sdl.Color{R: 245, G: 248, B: 255, A: 255}
	colorTextSecondary = sdl.Color{R: 195, G: 208, B: 230, A: 255}
	colorTextTertiary  = sdl.Color{R: 145, G: 156, B: 180, A: 255}
	colorTextInverse   = sdl.Color{R: 18, G: 22, B: 30, A: 255}
	colorTextAccent    = sdl.Color{R: 160, G: 220, B: 255, A: 255}

	// Semantic
	colorSuccess = sdl.Color{R: 46, G: 204, B: 113, A: 255}
	colorWarning = sdl.Color{R: 241, G: 196, B: 15, A: 255}
	colorDanger  = sdl.Color{R: 231, G: 76, B: 60, A: 255}
	colorInfo    = sdl.Color{R: 52, G: 152, B: 219, A: 255}

	// Shadows
	shadowColor = sdl.Color{R: 0, G: 0, B: 0, A: 0}

	// Accent (injected from config at runtime)
	accentColor sdl.Color
)

// SetAccentColor updates the runtime accent color from config.
func SetAccentColor(c sdl.Color) {
	accentColor = c
}

// resolveColor falls back to accentColor when value is empty/default.
func resolveColor(config *Config, value string, fallback sdl.Color) sdl.Color {
	if value == "" || value == "$buttonColor" || value == "$labelColor" || value == "$inputColor" {
		return fallback
	}
	if strings.HasPrefix(value, "$") {
		switch strings.ToLower(value[1:]) {
		case "buttoncolor":
			return config.Variables.ButtonColor
		case "labelcolor":
			return config.Variables.LabelColor
		case "inputcolor":
			return config.Variables.InputColor
		case "accent":
			return accentColor
		}
	}
	if strings.HasPrefix(value, "#") {
		r, g, b := hexToRGB(value)
		return sdl.Color{R: r, G: g, B: b, A: 255}
	}
	return fallback
}
