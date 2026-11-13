package main

// ui_glass.go — the Home screen's design language, for every other screen.
//
// Home (home_design.go, homelayout.go) is frosted glass over an ambient
// backdrop: a navy base with two soft colour blooms, translucent tiles with a
// hairline edge, an icon badge in a tile's top-left corner with the label
// anchored bottom-left, a cyan focus ring with a bloom behind it, and a footer
// band inset by the frame margin. Every other screen used an older recipe — an
// opaque slate header band, an opaque slate content panel, opaque slate rows
// and buttons, black dims behind overlays — so leaving Home looked like opening
// a different app.
//
// The helpers here are the one recipe both draw with now. The tokens are
// Home's own values (white at 5% for a tile, 8% for its edge, 20px corners,
// a 3px accent ring), made theme-aware: on a light palette the glass is mixed
// from a deep slate instead of white, so a light preset gets visible cards
// instead of white-on-white ones.
//
// Everything drawn here is a handful of alpha-modulated copies of cached
// shapes (shapecache.go) and one cached glow texture (backgroundfx.go), so the
// recipe costs the Smart Pro no more per frame than the slabs it replaces —
// less, because the scene backdrop no longer samples a 2560x1440 wallpaper.

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ── Ink ──────────────────────────────────────────────────────────────────

// glassThemeCache remembers whether the active background is light, so the
// per-frame glass calls do not recompute a WCAG luminance each time. It is
// keyed by the colour itself, so a theme change (ApplyThemeColors) or a test
// that swaps the palette is picked up on the next call.
var glassThemeCache struct {
	bg    sdl.Color
	light bool
	valid bool
}

// glassLightTheme reports whether the active palette is a light one.
func glassLightTheme() bool {
	bg := ColorBackground
	if glassThemeCache.valid && glassThemeCache.bg == bg {
		return glassThemeCache.light
	}
	glassThemeCache.bg = bg
	glassThemeCache.light = wcagLuminance(bg) > 0.4
	glassThemeCache.valid = true
	return glassThemeCache.light
}

// glassInk is the colour glass is mixed from: white over a dark palette (the
// Home look), a deep slate over a light one.
func glassInk() sdl.Color {
	if glassLightTheme() {
		return sdl.Color{R: 15, G: 23, B: 42, A: 255}
	}
	return sdl.Color{R: 255, G: 255, B: 255, A: 255}
}

// glassA is the glass ink at an alpha: the single call every glass surface,
// hairline, badge and chip goes through. A light palette needs a little more
// of the dark ink to read at the same strength as white does on navy.
func glassA(alpha uint8) sdl.Color {
	c := glassInk()
	if glassLightTheme() {
		boosted := int(alpha) * 3 / 2
		if boosted > 255 {
			boosted = 255
		}
		alpha = uint8(boosted)
	}
	if a11y.increaseContrast {
		boosted := int(alpha) + 24
		if boosted > 255 {
			boosted = 255
		}
		alpha = uint8(boosted)
	}
	if a11y.reduceTransparency {
		// Blend the ink into the backdrop at the token's own weight and return
		// an opaque colour. This keeps the token's hierarchy — a 13/255 tile is
		// still quieter than a 235/255 ring — without any surface underneath
		// showing through. It uses ColorBackground rather than the current
		// scene, which is the point: the token is stable and cacheable.
		t := 0.06 + float64(alpha)/255*0.82
		c = glassMix(ColorBackground, c, t)
		c.A = 255
		return c
	}
	c.A = alpha
	return c
}

// glassAccent is the focus and accent colour Home uses (the theme's Info
// colour, cyan by default). A preset can only lose it by choosing an accent
// as dark as its own background; the ring then falls back to the shared focus
// colour, so focus never disappears.
func glassAccent() sdl.Color {
	c := ColorAccent
	if c.A == 0 {
		return sdl.Color{R: 85, G: 216, B: 255, A: 255}
	}
	if math.Abs(colorLuma(c)-colorLuma(ColorBackground)) < 60 {
		return focusColorForAccent(c)
	}
	return c
}

// themeAccentColor is the value accentColor is set from: the active theme's
// accent (ColorAccent, which ApplyThemeColors sets to the preset's Info
// colour), so every screen's focus rings, pills and highlights are Home's.
func themeAccentColor() sdl.Color {
	return glassAccent()
}

// glassMix blends a toward b by t (0..1) per channel and returns it opaque.
func glassMix(a, b sdl.Color, t float64) sdl.Color {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	mix := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t))
	}
	return sdl.Color{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: 255}
}

// ── Tokens ───────────────────────────────────────────────────────────────

const (
	// glassControlRadius rounds buttons, inputs and toggles: the 12px icon
	// radius stepped up a notch, because a control is wider than a badge.
	glassControlRadius = int32(14)
	// glassRowRadius rounds list rows.
	glassRowRadius = int32(14)
	// glassFocusLift is how far a focused control grows on every side: Home's
	// 3px card lift, with the ring drawn on the lifted edge.
	glassFocusLift = int32(3)
)

// glassFocusRingWidth is Home's ring: 3px of accent on the control's edge.
// Increase Contrast widens it by 2px at runtime (a11yApplyVision), because it
// is read by every focusable's draw call rather than baked into one screen.
var glassFocusRingWidth int32 = 3

func glassFill() sdl.Color        { return glassA(13) }
func glassFillHover() sdl.Color   { return glassA(17) }
func glassFillFocus() sdl.Color   { return glassA(20) }
func glassFillPressed() sdl.Color { return glassA(26) }
func glassEdge() sdl.Color        { return glassA(20) }
func glassEdgeHover() sdl.Color   { return glassA(40) }

// glassChipFill / glassChipEdge / glassChipInk are Home's footer badge: the
// button name in a small glass pill.
func glassChipFill() sdl.Color { return glassA(16) }
func glassChipEdge() sdl.Color { return glassA(45) }
func glassChipInk() sdl.Color  { return glassA(210) }

// glassHairline is a divider or track line.
func glassHairline() sdl.Color { return glassA(16) }

// glassSheetFill is an overlay card's surface: the colour a Home tile has over
// the backdrop, made opaque. A translucent card over a dimmed screen let the
// screen's own text ghost through the card's; an opaque one in the same tone
// reads as the same glass without the ghosting.
func glassSheetFill() sdl.Color {
	// An opaque card is going down: realistic glass drawn after it this
	// frame must not frost the backdrop through it (glass_real.go).
	glassRealCover()
	return glassMix(glassMix(ColorBackground, glassInk(), 0.075), glassAccent(), 0.035)
}

// glassSheetDeepFill is the recessed variant, for the one page whose rows are
// the raised surface (Downloads): one step down from the sheet.
func glassSheetDeepFill() sdl.Color {
	return glassMix(glassMix(ColorBackground, glassInk(), 0.035), glassAccent(), 0.02)
}

// glassRaisedFill is a card standing on an overlay sheet, made opaque: the
// sheet with one more layer of glass on it. Panels that paint a summary card
// above their rows (the Downloads storage card) use it so the card reads as
// glass on glass rather than a slate slab.
func glassRaisedFill() sdl.Color {
	return glassMix(glassSheetFill(), glassInk(), 0.06)
}

// glassSheetEdge is the hairline around an overlay card.
func glassSheetEdge() sdl.Color { return glassA(26) }

// glassFooterFill is the scene footer band: Home's black wash over the
// backdrop, made opaque so a list scrolled under it cannot show through.
func glassFooterFill() sdl.Color {
	if glassLightTheme() {
		return glassMix(ColorBackground, glassInk(), 0.08)
	}
	return glassMix(ColorBackground, sdl.Color{R: 0, G: 0, B: 0, A: 255}, 0.3)
}

// glassScrimColor is the dim behind a modal overlay: the backdrop's own navy
// at the shared alpha, so a dialog sits in Home's ambience instead of in a
// black hole. progress scales it for a panel that fades its dim in.
func glassScrimColor(progress float64) sdl.Color {
	glassRealCover()
	c := ColorBackground
	c.A = uint8(math.Round(float64(OverlayScrimAlpha) * uiClamp01(progress)))
	return c
}

// ── Backdrop ─────────────────────────────────────────────────────────────

// appWallpaperIsShippedDefault reports whether the configured background image
// is the stock background.jpg. Home never drew it — its backdrop is procedural
// — so a scene drawing it was the most visible way the two looked like
// different apps, and keeping a 2560x1440 texture resident for it cost the
// Smart Pro about 15 MB. A wallpaper the user picked is still honoured, and
// now on every screen, Home included.
func appWallpaperIsShippedDefault(path string) bool {
	p := strings.TrimSpace(path)
	if p == "" {
		return true
	}
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return strings.EqualFold(p, "background.jpg")
}

// homeBackdropConfig is the config Home is being drawn for this frame. The
// home layout's Render has no config parameter, so renderHomeModern sets this
// before calling it and the layout's backdrop reads it (falling back to the
// global appConfig), which keeps the wallpaper refresh and the draw on one
// config instead of two.
var homeBackdropConfig *Config

// appWallpaperActive reports whether a user-chosen wallpaper is loaded and
// should be drawn under the backdrop's blooms. Never in screenshot mode: the
// picture is the user's own (screenshotmode.go).
func appWallpaperActive(config *Config) bool {
	if shotModeOn() {
		return false
	}
	return config != nil && (bgTexture != nil || gifWallpaperReady(config)) &&
		!appWallpaperIsShippedDefault(config.Variables.BackgroundImage)
}

// drawAppBackdrop paints Home's backdrop over a w×h screen: the navy base, the
// cyan bloom behind the top-left content, the violet one in the bottom-right
// corner, and the slow ambient layer. A wallpaper the user chose replaces the
// flat base, dimmed so glass and text stay legible on it. Home, every scene and
// every module screen draw this, so the background is one surface app-wide.
func drawAppBackdrop(renderer *sdl.Renderer, config *Config, w, h int32) {
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	// Realistic glass drawn next sits on this backdrop (glass_real.go).
	glassRealNoteBackdrop(renderer)
	// One config for every layer: the water (liquidbg.go) and the layers
	// below must agree on the wallpaper and the living background, or one
	// would paint an opaque base over what the other drew.
	if config == nil {
		config = appConfig
	}
	if config != nil {
		// A wallpaper picked in the picker survives a restart (gifwall.go).
		wallpaperRestoreSaved(config)
		// Cheap when nothing changed (a string compare); it is what lets a
		// module screen show a wallpaper picked while it was not on screen.
		ensureBackgroundTexture(renderer, config)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// With the liquid background on (liquidbg.go) the bottom layer — the
	// wallpaper, or the base and blooms baked into one picture — is drawn
	// through the water, and what it painted decides what is left to draw.
	water := liquidBackdropBase(renderer, config, w, h)
	if water != liquidDrewBackdrop {
		base := ColorBackground
		base.A = 255
		if appWallpaperActive(config) {
			if water != liquidDrewWallpaper {
				// An animated GIF wallpaper hands over its current frame
				// (gifwall.go).
				if tex, src := appWallpaperTexture(renderer, config); tex != nil {
					_ = renderer.Copy(tex, src, &sdl.Rect{X: 0, Y: 0, W: w, H: h})
				}
			}
			base.A = 150
		}
		setDrawColor(renderer, base)
		_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: w, H: h})
		blob := scaleX(900, w)
		// The bloom colours come from the theme (themegallery.go): the accent
		// and Home's violet unless a preset — a total conversion — names its
		// own pair.
		bloom, bloom2 := themeBackdropBlooms(config)
		homeGlowBlob(renderer, sdl.Rect{
			X: scaleX(60, w) - blob/2, Y: scaleY(300, h) - blob/2,
			W: blob, H: blob,
		}, bloom, 44)
		homeGlowBlob(renderer, sdl.Rect{
			X: w - blob/2 - scaleX(60, w), Y: h - blob/2,
			W: blob, H: blob,
		}, bloom2, 36)
	}
	switch {
	case config != nil && config.Variables.DynamicBg && water != liquidDrewNothing:
		// The living background's opaque base would cover the water: only its
		// drifting lights and particles go on top.
		renderDynamicBackgroundOver(renderer)
	case config != nil && config.Variables.DynamicBg:
		renderDynamicBackground(renderer)
	default:
		renderAmbientBackground(renderer)
	}
	// The light the ripples catch goes over everything the backdrop drew.
	liquidBackdropLight(renderer)
}

// drawAppBackdropBand repaints the backdrop inside one band of the screen. The
// scene header has no bar chrome — like Home's, it is the backdrop — but a body
// that scrolls under it must not show through the wordmark and the clock, so
// the band is repainted with the pixels the backdrop has there.
func drawAppBackdropBand(renderer *sdl.Renderer, config *Config, band sdl.Rect) {
	if renderer == nil || band.W <= 0 || band.H <= 0 {
		return
	}
	hadClip := renderer.IsClipEnabled()
	prev := renderer.GetClipRect()
	clip := band
	if hadClip {
		clip = glassIntersect(prev, band)
		if clip.W <= 0 || clip.H <= 0 {
			return
		}
	}
	_ = renderer.SetClipRect(&clip)
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	if hadClip {
		_ = renderer.SetClipRect(&prev)
	} else {
		_ = renderer.SetClipRect(nil)
	}
}

// glassIntersect returns the overlap of two rects (zero size when disjoint).
func glassIntersect(a, b sdl.Rect) sdl.Rect {
	x0, y0 := max32(a.X, b.X), max32(a.Y, b.Y)
	x1, y1 := min32(a.X+a.W, b.X+b.W), min32(a.Y+a.H, b.Y+b.H)
	if x1 <= x0 || y1 <= y0 {
		return sdl.Rect{}
	}
	return sdl.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// ── Controls ─────────────────────────────────────────────────────────────

// glassState is the interaction state of one glass control. NoLift keeps a
// focused control at its own size: list rows sit a few pixels apart inside
// clipped viewports, where a lifted ring would touch the next row.
type glassState struct {
	Focused  bool
	Hovered  bool
	Pressed  bool
	Disabled bool
	NoLift   bool
}

// glassLifted is rect grown by Home's focus lift.
func glassLifted(rect sdl.Rect) sdl.Rect {
	return sdl.Rect{
		X: rect.X - glassFocusLift, Y: rect.Y - glassFocusLift,
		W: rect.W + 2*glassFocusLift, H: rect.H + 2*glassFocusLift,
	}
}

// drawGlassFocusGlow paints Home's soft accent bloom behind a focused control.
// It is drawn before the control so the light reads as coming from behind it,
// and it is scaled to the control so a button's bloom does not wash over its
// neighbours the way a tile-sized one would.
func drawGlassFocusGlow(renderer *sdl.Renderer, rect sdl.Rect) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	pad := min32(rect.W, rect.H) / 4
	if pad < 8 {
		pad = 8
	}
	if pad > 26 {
		pad = 26
	}
	rect = homeFocusGlide.apply(rect)
	homeGlowBlob(renderer, sdl.Rect{
		X: rect.X - pad, Y: rect.Y - pad,
		W: rect.W + 2*pad, H: rect.H + 2*pad,
	}, glassAccent(), 70)
}

// drawGlassFocusRing is Home's ring: 3px of accent drawn on the control's own
// edge, so a focused control never grows past the slot it was laid out in.
func drawGlassFocusRing(renderer *sdl.Renderer, rect sdl.Rect, radius int32) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	// A focus that moved drops a ripple in the liquid background (liquidbg.go).
	liquidNoteFocus(rect)
	// On Home the ring glides between controls (homefocusglide.go); elsewhere
	// apply returns rect. The shape stays keyed to rect's size, so the cache
	// gains no entry per frame of the move.
	strokeRoundedRectAt(renderer, rect, homeFocusGlide.apply(rect), radius, glassFocusRingWidth, glassAccent())
}

// drawGlassSurface paints one glass control: the translucent fill, the hairline
// and, when focused, Home's treatment — the control lifts 3px on every side and
// the accent ring sits on the lifted edge. The bloom is left to the caller
// (drawGlassFocusGlow first), because a full-width list row has no room around
// it for one.
func drawGlassSurface(renderer *sdl.Renderer, rect sdl.Rect, radius int32, st glassState) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	// Settings → Display & theme → Glass: Realistic (glass_real.go).
	if glassRealSurface(renderer, rect, radius, st) {
		return
	}
	fill, edge := glassFill(), glassEdge()
	switch {
	case st.Disabled:
		fill, edge = glassA(8), glassA(14)
	case st.Pressed:
		fill = glassFillPressed()
	case st.Focused:
		fill = glassFillFocus()
	case st.Hovered:
		fill, edge = glassFillHover(), glassEdgeHover()
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if st.Focused && !st.Disabled {
		draw := rect
		if !st.NoLift {
			draw = glassLifted(rect)
			radius += glassFocusLift
		}
		fillRoundedRect(renderer, draw.X, draw.Y, draw.W, draw.H, radius, fill)
		drawGlassFocusRing(renderer, draw, radius)
		return
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, edge)
}

// glassButtonRadius is the corner a button of height h gets: a pill when it is
// header-sized, the control radius otherwise.
func glassButtonRadius(h int32) int32 {
	if h <= 40 {
		return h / 2
	}
	return glassControlRadius
}

// drawGlassText draws a label with whichever text path the caller can use:
// renderText needs a config (it substitutes $variables), drawText does not.
func drawGlassText(renderer *sdl.Renderer, config *Config, font *ttf.Font, text string, color sdl.Color, x, y int32) {
	if config != nil {
		renderText(renderer, config, font, text, color, x, y)
		return
	}
	drawText(renderer, font, text, x, y, color, textAlignLeft)
}

// drawGlassButton is a scene or panel button in Home's language: an optional
// low-alpha tint (an author's or the theme's button colour), the glass surface,
// Home's bloom and ring when focused, and a centred label.
func drawGlassButton(renderer *sdl.Renderer, config *Config, rect sdl.Rect, label string, font *ttf.Font, tint sdl.Color, st glassState) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	radius := glassButtonRadius(rect.H)
	if st.Focused && !st.Disabled {
		drawGlassFocusGlow(renderer, rect)
	}
	if tint.A > 0 && !st.Disabled {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, tint)
	}
	drawGlassSurface(renderer, rect, radius, st)
	if font == nil || strings.TrimSpace(label) == "" {
		return
	}
	if config != nil {
		label = substituteVars(label, config.Variables.Custom)
	}
	// The same inner width buttonLabelFont fits the label to, so a label it
	// fitted is never cut with an ellipsis here.
	text := label
	if maxW := rect.W - 2*SpaceXS; maxW > 0 && rect.W >= 60 {
		text = ellipsize(font, text, maxW)
	}
	tw, th := measureText(font, text)
	color := ColorTextPrimary()
	if st.Disabled {
		color = WithAlpha(ColorTextTertiary(), 160)
	}
	drawGlassText(renderer, config, font, text, color, rect.X+(rect.W-tw)/2, rect.Y+(rect.H-th)/2)
}

// glassButtonTint is the low-alpha wash an author's button colour leaves under
// the glass: enough to tell a red button from a blue one, not enough to turn
// the button back into an opaque slab.
func glassButtonTint(c sdl.Color) sdl.Color {
	if c.A == 0 {
		return c
	}
	c.A = 60
	return c
}

// drawGlassSheet paints an overlay card: the opaque glass surface, its hairline
// and the shared corner radius.
func drawGlassSheet(renderer *sdl.Renderer, rect sdl.Rect, fill sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if glassRealSheet(renderer, rect, fill) {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, OverlayCardRadius, fill)
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, OverlayCardRadius, 1, glassSheetEdge())
}

// drawGlassBadge paints Home's icon badge — the rounded glass square a tile's
// glyph sits in — and returns the box the glyph is drawn into. Unlike
// drawIconBadge it scales its corner and glyph with the badge, so the 32px
// badge of a compact tile keeps the 48px one's proportions.
func drawGlassBadge(renderer *sdl.Renderer, rect sdl.Rect) sdl.Rect {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return sdl.Rect{}
	}
	radius := rect.W / 4
	if radius > homeIconRadius {
		radius = homeIconRadius
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if !glassRealBadge(renderer, rect, radius) {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, glassA(20))
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, glassA(16))
	}
	glyph := rect.W / 2
	if glyph < 12 {
		glyph = rect.W - 4
	}
	return sdl.Rect{
		X: rect.X + (rect.W-glyph)/2,
		Y: rect.Y + (rect.H-glyph)/2,
		W: glyph, H: glyph,
	}
}

// glassTileLabel fits a tile's name into the width×avail block under its badge:
// the largest size, from maxSize down, at which the name fits on at most two
// lines without being cut — so a short name stays at Home's 20px title size on
// one line instead of shrinking to make room for a second line it does not
// need. When nothing fits whole, the smallest size's (ellipsized) lines are
// returned; no lines means not even one line fits, and the caller lays the
// tile out side by side instead.
func glassTileLabel(config *Config, label string, width, avail int32, maxSize int) (*ttf.Font, int32, []string) {
	if label == "" || width <= 0 || avail <= 0 {
		return nil, 0, nil
	}
	var font *ttf.Font
	var lineH int32
	var lines []string
	for size := maxSize; size >= 12; size -= 2 {
		f := getFontSized(config, "small", size)
		if f == nil {
			continue
		}
		lh := int32(f.Height()) + 1
		maxLines := int(avail / lh)
		if maxLines > 2 {
			maxLines = 2
		}
		if maxLines < 1 {
			continue
		}
		wrapped := appTileLabelLines(f, label, width, maxLines)
		font, lineH, lines = f, lh, wrapped
		if !glassLabelCut(wrapped, label) {
			break
		}
	}
	return font, lineH, lines
}

// glassLabelCut reports whether wrapping had to ellipsize the label.
func glassLabelCut(lines []string, label string) bool {
	if len(lines) == 0 || strings.Contains(label, "…") {
		return len(lines) == 0
	}
	for _, line := range lines {
		if strings.HasSuffix(line, "…") {
			return true
		}
	}
	return false
}

// ── Footer and hint badges ───────────────────────────────────────────────

// sceneFooterBand is the scene footer's band: Home's footer geometry — inset by
// the frame margin, rounded at the top, flush with the bottom edge, 56px tall.
// Scene layouts still reserve footerBarHeight() plus their 16px gap above it,
// so the band's extra height sits in that gap and never covers content.
func sceneFooterBand() sdl.Rect {
	h := footerBarHeight()
	if screenHeight >= 600 {
		h = scaleY(56, screenHeight)
	}
	margin := scaleX(homeSideMargin, screenWidth)
	return sdl.Rect{X: margin, Y: screenHeight - h, W: screenWidth - 2*margin, H: h}
}

// drawGlassFooterBand paints the band itself.
func drawGlassFooterBand(renderer *sdl.Renderer, band sdl.Rect) {
	if renderer == nil || band.W <= 0 || band.H <= 0 {
		return
	}
	fill := glassFooterFill()
	radius := int32(12)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, band.X, band.Y, band.W, band.H, radius, fill)
	// Square bottom edge: only the top corners are rounded.
	setDrawColor(renderer, fill)
	_ = renderer.FillRect(&sdl.Rect{X: band.X, Y: band.Y + band.H - radius, W: band.W, H: radius})
	setDrawColor(renderer, glassHairline())
	_ = renderer.DrawLine(band.X+radius, band.Y, band.X+band.W-radius, band.Y)
}

// drawGlassHintBadge paints one controller hint the way Home's footer does: the
// button name in a glass pill, then its action in the muted ink. It returns
// the x just past the label. The button is named in this frame's vocabulary
// (hintKeyFor: "A" is "Enter" while the hints name keyboard keys), so a caller
// that sizes the pill itself measures hintKeyFor(button, label) too.
func drawGlassHintBadge(renderer *sdl.Renderer, config *Config, font *ttf.Font, badge sdl.Rect, button, label string, labelX, textY int32) int32 {
	if renderer == nil || font == nil {
		return labelX
	}
	button = hintKeyFor(button, label)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, glassChipFill())
	strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, 1, glassChipEdge())
	bw, _ := measureText(font, button)
	drawGlassText(renderer, config, font, button, glassChipInk(), badge.X+(badge.W-bw)/2, textY)
	lw, _ := measureText(font, label)
	drawGlassText(renderer, config, font, label, ColorTextMuted(), labelX, textY)
	return labelX + lw
}
