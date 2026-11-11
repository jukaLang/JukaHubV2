package main

import (
	"math"
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
	HomeTopBarH      int32 = 56 // matches the home header height so the bar never shifts between screens
	HomeTopBarHSmall int32 = 40
	HomeFooterH      int32 = 48
	HomeFooterHSmall int32 = 42
)

var (
	// Spacing scale (4pt base grid). Every explicit gap in the UI should
	// resolve to one of these values so the layout reads as a single system.
	SpaceXS  = int32(4)
	SpaceSM  = int32(8)
	SpaceMD  = int32(12)
	SpaceLG  = int32(16)
	SpaceXL  = int32(24)
	Space2XL = int32(32)
	Space3XL = int32(40)

	// Density aliases. "Airy" is the relaxed premium variant used on larger
	// screens; it is the same 4pt grid, just shifted up one step.
	SpaceAiryXS  = int32(6)
	SpaceAirySM  = int32(10)
	SpaceAiryMD  = int32(14)
	SpaceAiryLG  = int32(20)
	SpaceAiryXL  = int32(28)
	SpaceAiry2XL = int32(36)

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

	// Typography scale (approximate px). Every text size in the UI should
	// come from here so headings, body, and labels stay proportional.
	FontSizeXSS = int32(10)
	FontSizeSM  = int32(12)
	FontSizeMD  = int32(14)
	FontSizeLG  = int32(18)
	FontSizeXL  = int32(24)
	FontSize2XL = int32(32)
	FontSize3XL = int32(40)

	// Motion tokens (approximate durations in milliseconds).
	// Use these for any UI animations so motion feels consistent.
	AnimDurationFast     = int32(100)
	AnimDurationStandard = int32(200)
	AnimDurationSlow     = int32(300)
	AnimDurationSlower   = int32(400)
	AnimDurationReduced  = int32(0)

	// Border radius scale - softened for a modern glassy feel.
	// Match card/tile radii to the spacing system: small = sm, medium = md, large = lg.
	RadiusXS    = int32(6)
	RadiusSM    = int32(8)
	RadiusMD    = int32(12)
	RadiusLG    = int32(16)
	RadiusXL    = int32(24)
	RadiusXXL   = int32(32)
	RadiusRound = int32(9999) // fully round

	// Surfaces - the dark blue ramp.
	//
	// Every step is a real step: the panel sits 1.20:1 above the background,
	// rows 1.34:1 above the panel and a focused row 1.43:1 above a resting row,
	// which is what makes cards, rows and the focused row tell apart on a
	// handheld panel (the Brick's screen crushes neighbouring dark blues into
	// one flat field). The ramp used to move by ~1.1:1 per step, so every card
	// and every button read as the same dark blue as the page behind it.
	// Ambient glow shapes are still layered at low alpha so the steps hold.
	//
	// The hue is the Home screen's: white glass over the navy backdrop reads as
	// a neutral blue-grey, so the opaque surfaces sit in that family instead of
	// the saturated slate they used to be. Each step keeps its old luminance, so
	// the contrast ratios above (and every text ink's contrast on them) are
	// unchanged to the second decimal.
	ColorBackgroundTop    = sdl.Color{R: 7, G: 11, B: 18, A: 255} // #070B12
	ColorBackgroundBottom = sdl.Color{R: 7, G: 11, B: 18, A: 255}
	ColorBackground       = ColorBackgroundBottom                   // default solid background
	ColorTopBar           = sdl.Color{R: 27, G: 32, B: 41, A: 255}  // #1B2029 header band
	ColorPanel            = sdl.Color{R: 27, G: 32, B: 41, A: 255}  // #1B2029 surface
	ColorPanelRaised      = sdl.Color{R: 45, G: 54, B: 68, A: 255}  // #2D3644 raised row/card
	ColorCard             = sdl.Color{R: 36, G: 43, B: 55, A: 255}  // #242B37 resting card
	ColorCardFocus        = sdl.Color{R: 64, G: 77, B: 98, A: 255}  // #404D62 focused card
	ColorCardHover        = sdl.Color{R: 54, G: 64, B: 82, A: 255}  // #364052 hover state
	ColorBorder           = sdl.Color{R: 58, G: 75, B: 107, A: 255} // #3A4B6B resting border
	ColorFooter           = sdl.Color{R: 27, G: 32, B: 41, A: 255}

	// Borders - subtle and refined. The white alphas rose with the surfaces
	// above: at the old values a hairline over #16202F disappeared, so resting
	// buttons (bordered with ColorBorderDefault) had no edge at all.
	ColorBorderSubtle  = sdl.Color{R: 255, G: 255, B: 255, A: 22}
	ColorBorderDefault = sdl.Color{R: 255, G: 255, B: 255, A: 40}
	ColorBorderFocus   = sdl.Color{R: 110, G: 231, B: 255, A: 80}
	ColorBorderHover   = sdl.Color{R: 255, G: 255, B: 255, A: 55}

	// Semantic
	ColorSuccess         = sdl.Color{R: 72, G: 213, B: 151, A: 255}
	ColorWarning         = sdl.Color{R: 255, G: 191, B: 105, A: 255}
	ColorDanger          = sdl.Color{R: 240, G: 120, B: 138, A: 255} // #F0788A
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

	// Progress / track tokens
	ColorTrack           = sdl.Color{R: 255, G: 255, B: 255, A: 34}
	ColorProgress        = sdl.Color{R: 85, G: 216, B: 255, A: 255}
	ColorProgressIntense = sdl.Color{R: 60, G: 190, B: 255, A: 255}

	// Divider line
	ColorDivider = sdl.Color{R: 255, G: 255, B: 255, A: 22}

	// Icon tints for small glyph fills. ColorIconSurface and ColorIconDark are
	// declared once in homelayout.go (home design tokens) and get retinted at
	// runtime by ApplyDesignPalette.
	ColorIconTertiary = sdl.Color{R: 255, G: 255, B: 255, A: 55}

	// Chip / pill surface
	ColorChip      = sdl.Color{R: 53, G: 72, B: 106, A: 255} // #35486A
	ColorChipHover = sdl.Color{R: 63, G: 84, B: 120, A: 255} // #3F5478
	ColorChipFocus = sdl.Color{R: 69, G: 94, B: 134, A: 255} // #455E86

	// Accent (injected from config at runtime)
	accentColor sdl.Color
)

// --- Text color accessors (called as functions throughout the UI) ---
//
// These resolve to mutable package-level vars so that ApplyThemeColors can
// retint the whole UI at runtime when the user picks a theme preset.

var (
	textPrimaryColor    = sdl.Color{R: 246, G: 248, B: 252, A: 255} // #F6F8FC
	textSecondaryColor  = sdl.Color{R: 180, G: 193, B: 212, A: 255} // #B4C1D4
	textTertiaryColor   = sdl.Color{R: 143, G: 160, B: 186, A: 255} // #8FA0BA
	textInverseColor    = sdl.Color{R: 9, G: 11, B: 20, A: 255}
	textAccentColor     = sdl.Color{R: 110, G: 231, B: 255, A: 255}
	textAccentSecondary = sdl.Color{R: 139, G: 124, B: 255, A: 255} // #8B7CFF

	// Derived surface tints (set by ApplyThemeColors)
	surfaceHighlightColor = sdl.Color{R: 255, G: 255, B: 255, A: 14}
	topSheenColor         = sdl.Color{R: 255, G: 255, B: 255, A: 12}

	// Readable text variants of the semantic status colours. The fills stay as
	// authored — chips, bars and rings are graphics, not text — while labels use
	// these, which clear WCAG AA on the background and both surfaces.
	successInk = TextInk(ColorSuccess)
	warningInk = TextInk(ColorWarning)
	dangerInk  = TextInk(ColorDanger)
	infoInk    = TextInk(ColorInfo)
)

// ColorTextPrimary returns the primary text color.
func ColorTextPrimary() sdl.Color { return a11yReadableText(textPrimaryColor) }

// ColorTextSecondary returns the secondary text color.
func ColorTextSecondary() sdl.Color { return a11yReadableText(textSecondaryColor) }

// ColorTextTertiary returns the tertiary / placeholder text color.
func ColorTextTertiary() sdl.Color { return a11yReadableText(textTertiaryColor) }

// ColorTextInverse returns the text color for use on accent fills.
func ColorTextInverse() sdl.Color { return textInverseColor }

// ColorTextAccent returns the accent-colored text.
func ColorTextAccent() sdl.Color { return a11yReadableText(textAccentColor) }

// ColorTextMuted returns the muted/secondary text color.
func ColorTextMuted() sdl.Color { return a11yReadableText(textSecondaryColor) }

// a11yReadableText leaves a normal theme's text tokens untouched, but makes
// Increase Contrast apply to every semantic text accessor. TextInk blends a
// colour towards the readable end of the palette only when it misses the
// target, so colours that already pass come back unchanged.
func a11yReadableText(c sdl.Color) sdl.Color {
	if a11y.increaseContrast {
		return TextInk(c)
	}
	return c
}

// SuccessInk returns the success colour as readable text.
func SuccessInk() sdl.Color { return a11yReadableStatusInk(successInk, ColorSuccess) }

// WarningInk returns the warning colour as readable text.
func WarningInk() sdl.Color { return a11yReadableStatusInk(warningInk, ColorWarning) }

// DangerInk returns the danger colour as readable text.
func DangerInk() sdl.Color { return a11yReadableStatusInk(dangerInk, ColorDanger) }

// InfoInk returns the info colour as readable text.
func InfoInk() sdl.Color { return a11yReadableStatusInk(infoInk, ColorInfo) }

// a11yReadableStatusInk recomputes the cached status ink when Increase Contrast
// is on. The cached variants target 4.5:1, which is not enough for the 7:1
// mode, while leaving the normal path allocation-free.
func a11yReadableStatusInk(cached, source sdl.Color) sdl.Color {
	if a11y.increaseContrast {
		return TextInk(source)
	}
	return cached
}

// The toast palette is fixed brand colour: no preset retints it, which made it
// the worst case for text use — on the light theme the amber lands at 2.0:1 and
// the error red at 3.3:1 against the card. These derive their readable variants
// on each call rather than caching them, so they cannot go stale when the theme
// changes underneath (and cannot leak palette state between tests).
func ToastSuccessInk() sdl.Color { return TextInk(ColorToastSuccess) }

// ToastWarnInk returns the toast amber as readable text.
func ToastWarnInk() sdl.Color { return TextInk(ColorToastWarn) }

// ToastErrorInk returns the toast red as readable text.
func ToastErrorInk() sdl.Color { return TextInk(ColorToastError) }

// ToastInfoInk returns the toast blue as readable text.
func ToastInfoInk() sdl.Color { return TextInk(ColorToastInfo) }

// StatusInk maps a status or toast colour to the variant that is readable as
// text, and passes every other colour through untouched.
//
// It exists for the call sites that hold a colour whose provenance they do not
// control — the Wi-Fi status line, a panel subtitle, a diagnostics result — so
// the rule is applied once where the colour becomes a glyph, instead of at each
// producer, which also use the same values as fills. Only the exact tokens map:
// an alpha-shifted variant is graphics, not text.
func StatusInk(c sdl.Color) sdl.Color {
	switch c {
	case ColorSuccess:
		return successInk
	case ColorWarning:
		return warningInk
	case ColorDanger:
		return dangerInk
	case ColorInfo:
		return infoInk
	case ColorToastSuccess:
		return ToastSuccessInk()
	case ColorToastWarn:
		return ToastWarnInk()
	case ColorToastError:
		return ToastErrorInk()
	case ColorToastInfo:
		return ToastInfoInk()
	}
	return c
}

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
	// Accent text is ink, not fill: the raw Info colour also draws rings and
	// glows, where a 3:1 graphics ratio is enough.
	textAccentColor = TextInk(hexRGBA(p.Info))
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
	ColorTopBar = hexRGBA(p.Surface)
	ColorBackgroundTop = hexRGBA(p.Background)
	ColorBackgroundBottom = ColorBackgroundTop
	ColorBackground = ColorBackgroundBottom
	// Retint the new systemic tokens that the rest of the UI may reference.
	ColorProgress = hexRGBA(p.Info)
	ColorProgressIntense = hexRGBA(p.Info)
	ColorChip = hexRGBA(p.SurfaceAlt)
	ColorChipHover = hexRGBA(p.SurfaceRaised)
	ColorChipFocus = hexRGBA(p.SurfaceRaised)
	ColorDivider = hexRGBA(p.BorderSubtle)
	// Re-derive the inks against this preset: a status colour that reads well
	// as a fill may not read as a label on this background, and the gallery
	// ships 25 different ones.
	successInk = TextInk(ColorSuccess)
	warningInk = TextInk(ColorWarning)
	dangerInk = TextInk(ColorDanger)
	infoInk = TextInk(ColorInfo)
	// Derive soft sheen colors so cards and panels share one highlight look.
	surfaceHighlightColor = sdl.Color{R: 255, G: 255, B: 255, A: 14}
	topSheenColor = sdl.Color{R: 255, G: 255, B: 255, A: 12}
	// Invalidate cached background/gradient textures so the new theme colors
	// are picked up on the very next frame.
	gradientTexture = nil
	gradientSceneKey = ""
}

// wcagLuminance is the WCAG 2.1 relative luminance of an sRGB colour, the
// value contrast decisions have to be made on.
func wcagLuminance(c sdl.Color) float64 {
	channel := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.R) + 0.7152*channel(c.G) + 0.0722*channel(c.B)
}

// contrastText returns a readable text color for the given background: dark
// text on bright surfaces, light text on dark surfaces.
//
// The crossover is the WCAG one (L = 0.179), where white and this near-black
// label colour have the same contrast. A weighted 8-bit sum put that point at a
// 23.5% mean channel value instead, so every mid-dark fill — a lifted dark blue
// like #2E4061, for one — was given near-black text at ~1.7:1 while white on it
// reads at 12:1.
func contrastText(bg sdl.Color) sdl.Color {
	if wcagLuminance(bg) > 0.179 {
		return sdl.Color{R: 18, G: 22, B: 30, A: 255}
	}
	return sdl.Color{R: 245, G: 248, B: 255, A: 255}
}

// textInkMinContrast is WCAG AA for body text. Status colours double as chip,
// bar and ring fills, where 3:1 is enough, so only the ink variants carry it.
const textInkMinContrast = 4.5

// TextInk returns c adjusted just far enough to be readable as text on every
// surface the app draws text on.
//
// The status colours are authored as fills, and the app also draws labels in
// them ("Failed", "Offline", the Groq status). On a preset whose warning is a
// deep amber sitting at 2.6:1 against its own background, that label was barely
// there. Blending towards white, or towards dark ink on a light palette, keeps
// the hue (an amber stays amber) while reaching WCAG AA on all three surfaces.
// A colour that already passes comes back untouched, so a palette authored
// correctly cannot drift.
func TextInk(c sdl.Color) sdl.Color {
	minContrast := textInkMinContrast
	if a11y.increaseContrast {
		minContrast = 7.0
	}
	surfaces := [...]sdl.Color{ColorBackground, ColorSurface, ColorSurfaceAlt}
	readable := func(v sdl.Color) bool {
		for _, s := range surfaces {
			la, lb := wcagLuminance(v), wcagLuminance(s)
			if la < lb {
				la, lb = lb, la
			}
			if (la+0.05)/(lb+0.05) < minContrast {
				return false
			}
		}
		return true
	}
	if readable(c) {
		return c
	}
	// Towards white on dark surfaces, towards near-black on light ones: the
	// direction with contrast to give, not the one that looks better on the
	// palette currently loaded.
	goal := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	mean := 0.0
	for _, s := range surfaces {
		mean += wcagLuminance(s)
	}
	if mean/float64(len(surfaces)) > 0.179 {
		goal = sdl.Color{R: 18, G: 22, B: 30, A: 255}
	}
	mix := func(from, to uint8, t float64) uint8 {
		return uint8(math.Round(float64(from) + (float64(to)-float64(from))*t))
	}
	cand := c
	// 32 steps is finer than the 8-bit ramp being walked, so the result lands as
	// close to the authored colour as the contrast requirement allows.
	for step := 1; step <= 32; step++ {
		f := float64(step) / 32
		cand = sdl.Color{R: mix(c.R, goal.R, f), G: mix(c.G, goal.G, f), B: mix(c.B, goal.B, f), A: c.A}
		if readable(cand) {
			return cand
		}
	}
	return cand
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

// HomeBorderColor returns the border color for Home cards.
func HomeBorderColor() sdl.Color {
	return ColorBorder
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

// Danger returns the danger/error color.
func Danger() sdl.Color { return ColorDanger }

// GlossFill returns a white specular highlight at the given low alpha,
// used for subtle top-edge sheen on cards and panels.
func GlossFill(alpha uint8) sdl.Color {
	return sdl.Color{R: 255, G: 255, B: 255, A: alpha}
}

// --- Semantic typography -------------------------------------------------

// TypeRole names a text style in the design language. Screens pick a role
// instead of a raw font name, so the type scale can change in one place.
type TypeRole int

const (
	TypeTitle TypeRole = iota
	TypeHeading
	TypeBody
	TypeLabel
	TypeCaption
)

// FontName returns the logical font name configured for the role.
func (r TypeRole) FontName() string {
	switch r {
	case TypeTitle:
		return "large"
	case TypeHeading:
		return "medium"
	case TypeBody:
		return "medium"
	case TypeLabel:
		return "small"
	case TypeCaption:
		return "small"
	}
	return "medium"
}

// Size returns the role's design size in px. It mirrors the shipped
// jukaconfig FontSizes and is the fallback when a config omits the entry.
func (r TypeRole) Size() int32 {
	switch r {
	case TypeTitle:
		return FontSize2XL
	case TypeHeading:
		return FontSizeXL
	case TypeBody:
		return FontSizeLG
	case TypeLabel:
		return FontSizeLG
	case TypeCaption:
		return FontSizeMD
	}
	return FontSizeLG
}

// --- Legibility floor -----------------------------------------------------
//
// A handheld is read at arm's length. Valve's Steam Deck review allows no
// character below 9px at 1280x800 (about 1.06 mm on its 7" panel, read from
// 30 cm) and recommends 12px; on the Smart Pro's 4.96" 1280x720 panel those
// are about 12 and 16.5 canvas pixels. The design sheet sets every caption at
// 14px (mockups/home-16x9-and-4x3.html), but screens drew badges, overlines and
// metadata at 12-13px, and the Brick's 0.75 text scale took the 14px captions
// down to 11px and the 13px badges to 10px on a 3.2" panel.
const (
	// uiMinTextSize is the smallest size, in 1280-canvas design pixels, for
	// text a user has to read: captions, metadata, badges and hints.
	uiMinTextSize = 14
	// uiMinTextSizeCompact is the floor on the 960-wide compact canvas, after
	// its text scale: the most the Brick's fixed layouts take without
	// re-measuring them on the device (the sheet's own Brick frame asks 15).
	uiMinTextSizeCompact = 12
	// uiTextFloorFrom is the smallest authored size the floor lifts. Sizes
	// under it are glyph-like (a calendar's week digits, a counter drawn into
	// a fixed pill) and keep the size their artwork was drawn for.
	uiTextFloorFrom = 12
)

// uiLegibleFontSize maps an authored size to the size it is drawn at on this
// canvas: authored 12-13px captions are lifted to the 14px floor, the compact
// canvas's text scale is applied (uiScaleFontSize), and the result is kept at
// or above the compact floor. Sizes of 14 and up are unchanged on the wide
// canvas, so a layout that already met the floor draws exactly as before. The
// accessibility text size applies after this floor, so a user who picks the
// smaller step (85%) deliberately gets text below it.
func uiLegibleFontSize(size int) int {
	if size <= 0 {
		return size
	}
	authored := size
	if authored >= uiTextFloorFrom && authored < uiMinTextSize {
		authored = uiMinTextSize
	}
	scaled := uiScaleFontSize(authored)
	if size >= uiTextFloorFrom && uiTextScaleFactor() < 0.999 && scaled < uiMinTextSizeCompact {
		scaled = uiMinTextSizeCompact
	}
	return scaled
}

// FontForRole resolves the configured font for a semantic role.
func FontForRole(config *Config, role TypeRole) *ttf.Font {
	if config == nil {
		return nil
	}
	font, _ := getCachedFont(config, role.FontName())
	return font
}

// --- Density -------------------------------------------------------------

// Density selects the spacing rhythm of a layout.
type Density int

const (
	DensityCompact Density = iota
	DensityComfortable
)

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
	cur := ""
	for _, r := range text {
		test := cur + string(r) + "…"
		tw, _, _ := font.SizeUTF8(test)
		if int32(tw) > maxW {
			break
		}
		cur += string(r)
	}
	return cur + "…"
}
