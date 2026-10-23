package main

// palette_test.go — a snapshot of the global design tokens.
//
// ApplyThemeColors rewrites every colour token in the package, and it is the
// only way the app changes theme. A test that applies a preset therefore leaks
// that palette into every test that runs afterwards, which turns a fixed-order
// suite into an order-dependent one: a render test that expects the dark
// default sees "midnight" instead and reports "the focused row is not
// highlighted". This helper exists so a theme-switching test can hand the
// palette back.
//
// Keep the field list in sync with ApplyThemeColors in design.go. It is spelled
// out rather than derived because the app has no palette-to-preset conversion,
// and a round trip through a ThemePreset would silently drop derived tokens
// (ColorCardFocus is not the preset's SurfaceRaised).

import "github.com/veandco/go-sdl2/sdl"

type paletteSnapshot struct {
	background, surface, surfaceAlt, surfaceRaised sdl.Color
	surfaceCard, surfaceRow, surfacePanel          sdl.Color
	buttonRaised, borderSubtle, borderDefault      sdl.Color
	borderFocus, success, warning, danger          sdl.Color
	info, overlay, overlayAlias                    sdl.Color
	textPrimary, textSecondary, textTertiary       sdl.Color
	textAccent, accent                             sdl.Color
	card, cardFocus, cardFocused, border           sdl.Color
	iconSurface, iconDark, footer, topBar          sdl.Color
	backgroundTop, backgroundBottom                sdl.Color
	progress, progressIntense                      sdl.Color
	chip, chipHover, chipFocus, divider            sdl.Color
	surfaceHighlight, topSheen                     sdl.Color
}

// capturePalette records every token ApplyThemeColors writes.
func capturePalette() paletteSnapshot {
	return paletteSnapshot{
		background:       ColorBackground,
		surface:          ColorSurface,
		surfaceAlt:       ColorSurfaceAlt,
		surfaceRaised:    ColorSurfaceRaised,
		surfaceCard:      ColorSurfaceCard,
		surfaceRow:       ColorSurfaceRow,
		surfacePanel:     ColorSurfacePanel,
		buttonRaised:     ColorButtonRaised,
		borderSubtle:     ColorBorderSubtle,
		borderDefault:    ColorBorderDefault,
		borderFocus:      ColorBorderFocus,
		success:          ColorSuccess,
		warning:          ColorWarning,
		danger:           ColorDanger,
		info:             ColorInfo,
		overlay:          ColorOverlay,
		overlayAlias:     overlayColor,
		textPrimary:      textPrimaryColor,
		textSecondary:    textSecondaryColor,
		textTertiary:     textTertiaryColor,
		textAccent:       textAccentColor,
		accent:           accentColor,
		card:             ColorCard,
		cardFocus:        ColorCardFocus,
		cardFocused:      ColorCardFocused,
		border:           ColorBorder,
		iconSurface:      ColorIconSurface,
		iconDark:         ColorIconDark,
		footer:           ColorFooter,
		topBar:           ColorTopBar,
		backgroundTop:    ColorBackgroundTop,
		backgroundBottom: ColorBackgroundBottom,
		progress:         ColorProgress,
		progressIntense:  ColorProgressIntense,
		chip:             ColorChip,
		chipHover:        ColorChipHover,
		chipFocus:        ColorChipFocus,
		divider:          ColorDivider,
		surfaceHighlight: surfaceHighlightColor,
		topSheen:         topSheenColor,
	}
}

// restorePalette puts the captured tokens back.
func restorePalette(s paletteSnapshot) {
	ColorBackground = s.background
	ColorSurface = s.surface
	ColorSurfaceAlt = s.surfaceAlt
	ColorSurfaceRaised = s.surfaceRaised
	ColorSurfaceCard = s.surfaceCard
	ColorSurfaceRow = s.surfaceRow
	ColorSurfacePanel = s.surfacePanel
	ColorButtonRaised = s.buttonRaised
	ColorBorderSubtle = s.borderSubtle
	ColorBorderDefault = s.borderDefault
	ColorBorderFocus = s.borderFocus
	ColorSuccess = s.success
	ColorWarning = s.warning
	ColorDanger = s.danger
	ColorInfo = s.info
	ColorOverlay = s.overlay
	overlayColor = s.overlayAlias
	textPrimaryColor = s.textPrimary
	textSecondaryColor = s.textSecondary
	textTertiaryColor = s.textTertiary
	textAccentColor = s.textAccent
	accentColor = s.accent
	ColorCard = s.card
	ColorCardFocus = s.cardFocus
	ColorCardFocused = s.cardFocused
	ColorBorder = s.border
	ColorIconSurface = s.iconSurface
	ColorIconDark = s.iconDark
	ColorFooter = s.footer
	ColorTopBar = s.topBar
	ColorBackgroundTop = s.backgroundTop
	ColorBackgroundBottom = s.backgroundBottom
	ColorProgress = s.progress
	ColorProgressIntense = s.progressIntense
	ColorChip = s.chip
	ColorChipHover = s.chipHover
	ColorChipFocus = s.chipFocus
	ColorDivider = s.divider
	surfaceHighlightColor = s.surfaceHighlight
	topSheenColor = s.topSheen
	// A theme change drops the cached gradient, so a restore has to as well.
	gradientTexture = nil
	gradientSceneKey = ""
}
