package main

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// Design tokens for JukaHub.
// Modern sleek design system with glass morphism and refined surfaces.

// pt is a small 2D point used by the primitive drawing helpers (triangles,
// circles, icons) across launcher.go and homelayout.go.
type pt struct{ x, y int32 }

// Home screen layout constants (px at 720p; reduced on smaller viewports).
const (
	HomeMargin          int32 = 28
	HomeTopBarH         int32 = 56 // matches the home header height so the bar never shifts between screens
	HomeTopBarHSmall    int32 = 40
	HomeFooterH         int32 = 48
	HomeFooterHSmall    int32 = 42
	HomeSectionGap      int32 = 14
	HomeCardGap         int32 = 14
	HomeCols            int32 = 4
	HomeColsSmall       int32 = 3
	HomeColsTiny        int32 = 2
	HomeContinueH       int32 = 112
	HomeCardHMax        int32 = 158
	HomeCardHMin        int32 = 118
	HomeTileSlotMissing int   = -1
)

var (
	// Spacing (4pt grid for tighter, more modern layout)
	SpaceXS  = int32(4)
	SpaceSM  = int32(8)
	SpaceMD  = int32(12)
	SpaceLG  = int32(16)
	SpaceXL  = int32(24)
	Space2XL = int32(32)

	// Layout tokens (safe margins, offsets, and component sizing)
	StatusBarMargin   = int32(32) // matches the home header's 32px side margins
	TitleCenterOffset = int32(10)
	StatusPartGap     = int32(14)
	ScenePad          = int32(16)
	SceneTextGap      = int32(6)
	ArrowWidth        = int32(22)
	RecentPadding     = int32(18)
	RecentCardGap     = int32(14)
	CardAreaPad       = int32(18)
	CardAreaOffset    = int32(54)
	CardYOffset       = int32(42)
	LabelBottomPad    = int32(14)
	LabelPadding      = int32(40)
	EmptyIconSize     = int32(32)
	EmptyMsgOffset1   = int32(22)
	EmptyMsgOffset2   = int32(44)
	VideoButtonW      = int32(36)
	VideoButtonH      = int32(24)
	ExitButtonW       = int32(44)
	ExitButtonH       = int32(28)
	ChipHeight        = int32(22)
	ChipSize          = int32(26)
	MinCardWidth      = int32(120)
	MinCardHeight     = int32(40)
	PressOffset       = int32(2)
	FocusGlow         = int32(5)
	FocusRing         = int32(3)
	HoverRing         = int32(2)
	TileRadius        = int32(16)
	IconSizeMin       = int32(44)
	IconSizeMax       = int32(64)

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

	// Surfaces - refined dark "Midnight" theme
	// Background is nearly solid #090E19; ambient glow shapes are layered at
	// low alpha by the home renderer so cards stay clearly separated.
	ColorBackgroundTop    = sdl.Color{R: 9, G: 14, B: 25, A: 255}
	ColorBackgroundBottom = sdl.Color{R: 9, G: 14, B: 25, A: 255}
	ColorBackground       = ColorBackgroundBottom // default solid background
	ColorTopBar           = sdl.Color{R: 17, G: 23, B: 37, A: 255}
	ColorPanel            = sdl.Color{R: 18, G: 26, B: 42, A: 255} // #121A2A surface
	ColorPanelRaised      = sdl.Color{R: 27, G: 37, B: 54, A: 255}
	ColorCard             = sdl.Color{R: 23, G: 32, B: 51, A: 255} // #172033 resting card
	ColorCardFocus        = sdl.Color{R: 34, G: 49, B: 75, A: 255} // #22314B focused card
	ColorBorder           = sdl.Color{R: 42, G: 57, B: 83, A: 255} // #2A3953 resting border
	ColorFooter           = sdl.Color{R: 18, G: 26, B: 42, A: 255}

	// Borders - subtle and refined
	ColorBorderSubtle  = sdl.Color{R: 255, G: 255, B: 255, A: 8}
	ColorBorderDefault = sdl.Color{R: 255, G: 255, B: 255, A: 16}
	ColorBorderFocus   = sdl.Color{R: 110, G: 231, B: 255, A: 60}

	// Semantic
	ColorSuccess         = sdl.Color{R: 72, G: 213, B: 151, A: 255}
	ColorWarning         = sdl.Color{R: 255, G: 191, B: 105, A: 255}
	ColorDanger          = sdl.Color{R: 226, G: 104, B: 120, A: 255} // #E26878
	ColorAccent          = sdl.Color{R: 85, G: 216, B: 255, A: 255}  // #55D8FF
	ColorAccentSecondary = sdl.Color{R: 139, G: 124, B: 255, A: 255} // #8B7CFF
	ColorDangerSubtle    = sdl.Color{R: 255, G: 200, B: 200, A: 60}
	ColorInfo            = sdl.Color{R: 110, G: 231, B: 255, A: 255}

	// Shadow base (apply alpha at call site)
	ColorShadow = sdl.Color{R: 0, G: 0, B: 0, A: 255}

	// Overlay
	ColorOverlay = sdl.Color{R: 6, G: 8, B: 14, A: 180}

	// Surfaces (aliases for backward compat with existing scenes)
	ColorSurface       = ColorPanel
	ColorSurfaceAlt    = ColorCard
	ColorSurfaceRaised = ColorPanelRaised
	ColorSurfaceCard   = ColorCard
	ColorSurfaceRow    = ColorPanelRaised
	ColorSurfacePanel  = ColorPanel
	ColorButtonRaised  = ColorPanelRaised

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
	textPrimaryColor    = sdl.Color{R: 246, G: 248, B: 252, A: 255} // #F6F8FC
	textSecondaryColor  = sdl.Color{R: 156, G: 169, B: 189, A: 255} // #9CA9BD
	textTertiaryColor   = sdl.Color{R: 102, G: 117, B: 139, A: 255} // #66758B
	textInverseColor    = sdl.Color{R: 9, G: 11, B: 20, A: 255}
	textAccentColor     = sdl.Color{R: 110, G: 231, B: 255, A: 255}
	textAccentSecondary = sdl.Color{R: 139, G: 124, B: 255, A: 255} // #8B7CFF
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

// ColorTextMuted returns the muted/secondary text color.
func ColorTextMuted() sdl.Color { return textSecondaryColor }

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
	// Retint the home design tokens too, so a theme (e.g. Light) never leaves
	// the home screen with a light background but dark cards.
	ColorCard = hexRGBA(p.SurfaceAlt)
	ColorCardFocus = hexRGBA(p.SurfaceRaised)
	ColorCardFocused = ColorCardFocus
	ColorBorder = hexRGBA(p.BorderDefault)
	ColorAccent = hexRGBA(p.Info)
	ColorIconSurface = hexRGBA(p.InputColor)
	ColorIconDark = contrastText(ColorAccent)
	ColorFooter = hexRGBA(p.Surface)
	ColorBackgroundTop = hexRGBA(p.Background)
	ColorBackgroundBottom = ColorBackgroundTop
	ColorBackground = ColorBackgroundBottom
	// Invalidate cached background/gradient textures so the new theme colors
	// are picked up on the very next frame.
	gradientTexture = nil
	gradientSceneKey = ""
}

// contrastText returns a readable text color for the given background:
// dark text on bright surfaces, light text on dark surfaces.
func contrastText(bg sdl.Color) sdl.Color {
	lum := int(bg.R)*299 + int(bg.G)*587 + int(bg.B)*114
	if lum > 60000 {
		return sdl.Color{R: 18, G: 22, B: 30, A: 255}
	}
	return sdl.Color{R: 245, G: 248, B: 255, A: 255}
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

// --- Home screen color helpers ---

// HomeCardColor returns the opaque card background color for Home tiles.
func HomeCardColor() sdl.Color {
	return ColorCard
}

// HomeCardFocusColor returns the brighter card background color for focused tiles.
func HomeCardFocusColor() sdl.Color {
	return ColorCardFocus
}

// HomeBorderColor returns the border color for Home cards.
func HomeBorderColor() sdl.Color {
	return ColorBorder
}

// HomeFocusColor returns the cyan accent color for focus rings.
func HomeFocusColor() sdl.Color {
	return focusColorForAccent(accentColor)
}

// HomeFooterColor returns the footer background color.
func HomeFooterColor() sdl.Color {
	return ColorFooter
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

// Accent returns the primary accent color.
func Accent() sdl.Color { return ColorAccent }

// AccentSecondary returns the secondary accent color.
func AccentSecondary() sdl.Color { return ColorAccentSecondary }

// Danger returns the danger/error color.
func Danger() sdl.Color { return ColorDanger }

// GlossFill returns a white specular highlight at the given low alpha,
// used for subtle top-edge sheen on cards and panels.
func GlossFill(alpha uint8) sdl.Color {
	return sdl.Color{R: 255, G: 255, B: 255, A: alpha}
}

// focusColorForAccent returns a focus ring color from the accent hue.
func focusColorForAccent(accent sdl.Color) sdl.Color {
	if int(accent.R)+int(accent.G) > 420 && int(accent.R) > int(accent.B) {
		return sdl.Color{R: 139, G: 124, B: 255, A: 255}
	}
	return sdl.Color{R: 85, G: 216, B: 255, A: 255}
}

// fitTextWidth truncates `text` (single line) to fit within maxW using the
// given font, appending an ellipsis when truncated.
func fitTextWidth(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, maxW int32) string {
	if font == nil || maxW <= 0 {
		return text
	}
	tw, _, _ := font.SizeUTF8(text)
	if int32(tw) <= maxW {
		return text
	}
	runes := []rune(text)
	cur := ""
	for _, r := range runes {
		test := cur + string(r) + "…"
		tw, _, _ := font.SizeUTF8(test)
		if int32(tw) > maxW {
			break
		}
		cur += string(r)
	}
	return cur + "…"
}
