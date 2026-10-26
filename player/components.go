package main

// ──────────────────────────────────────────────────────────────────────
// Shared UI components
//
// Scenes own their layout and state; these helpers own the visual recipe so
// the same card, row, badge or progress bar looks identical everywhere. They
// are stateless draw helpers and depend only on the design tokens.
// ──────────────────────────────────────────────────────────────────────

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// UICardStyle is the surface recipe for a panel or card.
type UICardStyle struct {
	Fill        sdl.Color
	Border      sdl.Color
	Radius      int32
	BorderWidth int32
	Shadow      uint8 // 0 disables the soft drop shadow
}

// DefaultUICardStyle returns the standard raised surface.
func DefaultUICardStyle() UICardStyle {
	return UICardStyle{
		Fill:        ColorSurfaceAlt,
		Border:      ColorBorder,
		Radius:      RadiusMD,
		BorderWidth: 1,
	}
}

// UICard draws a card surface: optional soft shadow, rounded fill, hairline.
func UICard(renderer *sdl.Renderer, rect sdl.Rect, style UICardStyle) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if style.Radius <= 0 {
		style.Radius = RadiusMD
	}
	if style.BorderWidth <= 0 {
		style.BorderWidth = 1
	}
	if style.Shadow > 0 {
		fillRoundedRect(renderer, rect.X+2, rect.Y+3, rect.W, rect.H, style.Radius, ShadowFill(style.Shadow))
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, style.Radius, style.Fill)
	if style.Border.A > 0 {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, style.Radius, style.BorderWidth, style.Border)
	}
}

// UIButtonVariant selects one of the app's semantic button roles. A custom
// variant uses the fill and label colours supplied in the style instead.
type UIButtonVariant int

const (
	UIButtonSecondary UIButtonVariant = iota
	UIButtonPrimary
	UIButtonGhost
	UIButtonDanger
	UIButtonCustom
)

// UIButtonStyle is the visual recipe for a button. Zero values fall back to the
// variant defaults, so callers only set the parts they need to override.
type UIButtonStyle struct {
	Variant       UIButtonVariant
	Fill          sdl.Color
	HoverFill     sdl.Color
	FocusFill     sdl.Color
	Border        sdl.Color
	LabelColor    sdl.Color
	Font          *ttf.Font
	Radius        int32
	Pill          bool
	NoShadow      bool
	HideFocusRing bool
	Progress      float64 // 0..1 hold-to-confirm fill over the surface
	ProgressFill  sdl.Color
}

// UIButtonState carries the interaction flags for one button draw. Layout and
// hit-testing stay with the caller; this only controls the pixels.
type UIButtonState struct {
	Focused  bool
	Hovered  bool
	Pressed  bool
	Disabled bool
}

func uiButtonFill(variant UIButtonVariant) sdl.Color {
	switch variant {
	case UIButtonPrimary:
		return accentColor
	case UIButtonDanger:
		return ColorDanger
	case UIButtonGhost:
		return WithAlpha(ColorSurfaceAlt, 120)
	case UIButtonCustom:
		return sdl.Color{}
	default:
		return ColorSurfaceAlt
	}
}

func uiButtonHoverFill(variant UIButtonVariant, fill sdl.Color) sdl.Color {
	switch variant {
	case UIButtonPrimary, UIButtonDanger, UIButtonCustom:
		return lighten(fill, 6)
	case UIButtonGhost:
		return WithAlpha(ColorSurfaceRaised, 170)
	default:
		return ColorCardHover
	}
}

func uiButtonFocusFill(variant UIButtonVariant, fill sdl.Color) sdl.Color {
	switch variant {
	case UIButtonPrimary, UIButtonDanger, UIButtonCustom:
		return lighten(fill, 10)
	case UIButtonGhost:
		return WithAlpha(ColorSurfaceRaised, 200)
	default:
		return ColorCardFocus
	}
}

// UIButton draws the app-wide button recipe: one rounded surface, a top sheen,
// a hairline border, a focus ring and a centred label. It returns rect so scene
// code can record the hit target in the same expression.
func UIButton(renderer *sdl.Renderer, config *Config, rect sdl.Rect, label string, style UIButtonStyle, state UIButtonState) sdl.Rect {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return rect
	}

	radius := style.Radius
	if radius <= 0 {
		radius = RadiusMD
	}
	if style.Pill {
		radius = rect.H / 2
	}

	fill := style.Fill
	if fill.A == 0 {
		fill = uiButtonFill(style.Variant)
	}
	hoverFill := style.HoverFill
	if hoverFill.A == 0 {
		hoverFill = uiButtonHoverFill(style.Variant, fill)
	}
	focusFill := style.FocusFill
	if focusFill.A == 0 {
		focusFill = uiButtonFocusFill(style.Variant, fill)
	}
	border := style.Border
	if border.A == 0 {
		border = ColorBorderDefault
	}
	labelColor := style.LabelColor
	if labelColor.A == 0 {
		labelColor = contrastText(fill)
	}

	if state.Disabled {
		fill = WithAlpha(fill, 90)
		border = WithAlpha(border, 90)
		labelColor = WithAlpha(ColorTextTertiary(), 160)
	} else if state.Pressed {
		fill = darken(fill, 16)
	} else if state.Focused {
		fill = focusFill
	} else if state.Hovered {
		fill = hoverFill
	}

	if state.Focused && !state.Disabled && !style.HideFocusRing {
		border = focusColorForAccent(accentColor)
	} else if state.Hovered && !state.Disabled {
		border = ColorBorderHover
	}

	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if !style.NoShadow {
		fillRoundedRect(renderer, rect.X+2, rect.Y+4, rect.W, rect.H, radius, ShadowFill(22))
	}
	if state.Focused && !state.Disabled && !style.HideFocusRing {
		fillRoundedRect(renderer, rect.X-FocusGlow, rect.Y-FocusGlow, rect.W+2*FocusGlow, rect.H+2*FocusGlow, radius+FocusGlow, WithAlpha(accentColor, 24))
	}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, fill)
	if style.Progress > 0 {
		progressFill := style.ProgressFill
		if progressFill.A == 0 {
			progressFill = lighten(fill, 24)
		}
		UIProgressBar(renderer, rect, style.Progress, sdl.Color{}, progressFill)
	}
	// A full-width highlight line only fits a rounded rectangle; on a pill or
	// circle it would stick out past the curved top edge.
	if !style.Pill && radius < rect.H/2 {
		renderer.SetDrawColor(255, 255, 255, 14)
		renderer.FillRect(&sdl.Rect{X: rect.X + 2, Y: rect.Y + 1, W: rect.W - 4, H: 1})
	}
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, border)
	if state.Focused && !state.Disabled && !style.HideFocusRing {
		strokeRoundedRect(renderer, rect.X-FocusRing, rect.Y-FocusRing, rect.W+2*FocusRing, rect.H+2*FocusRing, radius+FocusRing, FocusRing, focusColorForAccent(accentColor))
		fillRoundedRect(renderer, rect.X+1, rect.Y+1, rect.W-2, rect.H/2, radius-1, SurfaceHighlight(8))
	}

	// Substitute before measuring: a label like "Screensaver · $timeout" is only
	// as wide as its value once the variable is in it. Ellipsizing the template
	// and then drawing the substituted value either ran the text past the button
	// or, when the template was the longer string, cut the "$name" in half so the
	// raw placeholder was what reached the screen.
	if config != nil {
		label = substituteVars(label, config.Variables.Custom)
	}
	if style.Font != nil && label != "" {
		text := label
		// Compact pills and icon buttons are sized from their label; only
		// trim labels on full-size buttons where a caller may pass a long
		// string.
		if maxW := rect.W - 2*SpaceXS; maxW > 0 && rect.W >= 60 {
			text = ellipsize(style.Font, text, maxW)
		}
		if tw, th, err := style.Font.SizeUTF8(text); err == nil {
			renderText(renderer, config, style.Font, text, labelColor,
				rect.X+(rect.W-int32(tw))/2, rect.Y+(rect.H-int32(th))/2)
		}
	}
	return rect
}

// UIBadgeSize measures a count badge before drawing it, so callers can align
// it to an edge.
func UIBadgeSize(config *Config, text string) (int32, int32) {
	font := FontForRole(config, TypeCaption)
	if font == nil || text == "" {
		return 0, 0
	}
	tw, th, _ := font.SizeUTF8(text)
	pad := SpaceSM
	return int32(tw) + pad*2, int32(th) + 8
}

// UIBadge draws a small count pill and returns its rect.
func UIBadge(renderer *sdl.Renderer, config *Config, x, y int32, text string, bg, fg sdl.Color) sdl.Rect {
	w, h := UIBadgeSize(config, text)
	if renderer == nil || w <= 0 || h <= 0 {
		return sdl.Rect{}
	}
	font := FontForRole(config, TypeCaption)
	rect := sdl.Rect{X: x, Y: y, W: w, H: h}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, h/2, bg)
	_, th, _ := font.SizeUTF8(text)
	renderText(renderer, config, font, text, fg, rect.X+SpaceSM, rect.Y+(h-int32(th))/2)
	return rect
}

// UIProgressBar draws a rounded progress bar. A transparent track skips the
// background, which is what a progress-in-place button wants.
func UIProgressBar(renderer *sdl.Renderer, rect sdl.Rect, fraction float64, track, fill sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if fraction < 0 {
		fraction = 0
	} else if fraction > 1 {
		fraction = 1
	}
	radius := rect.H / 2
	if track.A > 0 {
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, track)
	}
	w := int32(math.Round(float64(rect.W) * fraction))
	if w > 0 {
		fillRoundedRect(renderer, rect.X, rect.Y, w, rect.H, radius, fill)
	}
}

// UIListRow draws one list row: shadow, surface, and the selected/hovered
// treatment (accent tint and left bar when selected).
func UIListRow(renderer *sdl.Renderer, rect sdl.Rect, radius int32, selected, hovered bool) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if radius <= 0 {
		radius = RadiusMD
	}
	x, y, w, h := rect.X, rect.Y, rect.W, rect.H
	fillRoundedRect(renderer, x+1, y+2, w, h, radius, ShadowFill(24))
	fillRoundedRect(renderer, x, y, w, h, radius, ColorSurfaceRow)
	if selected {
		// The tint alone is too quiet on a dim handheld panel: on a theme whose
		// accent is close to the row surface the wash reads as "slightly lighter
		// card" and the cursor is lost. The selected row therefore carries the
		// same focus ring the overlay panels draw (installHighlight falls back to
		// the text colour when the accent itself has no contrast), plus the solid
		// spine so the cursor is still obvious where the ring is clipped by a
		// scroll viewport.
		fillRoundedRect(renderer, x, y, w, h, radius, WithAlpha(accentColor, 50))
		ring := installHighlight()
		strokeRoundedRect(renderer, x, y, w, h, radius, 2, ring)
		renderer.SetDrawColor(ring.R, ring.G, ring.B, 255)
		renderer.FillRect(&sdl.Rect{X: x + 1, Y: y + 2, W: 3, H: h - 4})
		renderer.SetDrawColor(255, 255, 255, 20)
		renderer.FillRect(&sdl.Rect{X: x + 4, Y: y + 1, W: w - 8, H: 1})
		renderer.SetDrawColor(ring.R, ring.G, ring.B, 15)
		renderer.FillRect(&sdl.Rect{X: x + 1, Y: y + h - 2, W: w - 2, H: 1})
	} else if hovered {
		fillRoundedRect(renderer, x, y, w, h, radius, WithAlpha(ColorCardHover, 120))
		renderer.SetDrawColor(255, 255, 255, 12)
		renderer.FillRect(&sdl.Rect{X: x + 2, Y: y + 1, W: w - 4, H: 1})
	}
}

// UIEmptyState draws the standard "nothing here yet" block: an optional muted
// glyph, a title and one supporting line, centred inside rect.
func UIEmptyState(renderer *sdl.Renderer, config *Config, rect sdl.Rect, glyph, title, body string) {
	uiStateBlock(renderer, config, rect, glyph, title, body, ColorTextTertiary())
}

// UIErrorState is the same block in the danger colour, for failures.
func UIErrorState(renderer *sdl.Renderer, config *Config, rect sdl.Rect, glyph, title, body string) {
	uiStateBlock(renderer, config, rect, glyph, title, body, ColorDanger)
}

// uiStateBlockMetrics measures the vertical pieces of an empty/error state.
// Layout code uses the same metrics (via uiStateBlockHeight) to size the
// surface, so a centred block can never be taller than its background.
func uiStateBlockMetrics(config *Config, glyph, title, body string) (glyphH, titleH, bodyH, gap int32) {
	titleFont := FontForRole(config, TypeHeading)
	if titleFont == nil {
		return 0, 0, 0, 0
	}
	bodyFont := FontForRole(config, TypeCaption)
	glyphFont := FontForRole(config, TypeTitle)
	titleH = int32(titleFont.Height())
	if bodyFont != nil && body != "" {
		bodyH = int32(bodyFont.Height())
	}
	if glyph == "@bell" {
		// Drawn icons need a little more room than a text glyph; the bell reads
		// as an empty-state illustration instead of an error marker.
		glyphH = 36 + SpaceMD
	} else if glyphFont != nil && glyph != "" {
		glyphH = int32(glyphFont.Height()) + SpaceMD
	}
	if bodyH > 0 {
		gap = SpaceXS
	}
	return glyphH, titleH, bodyH, gap
}

// uiStateBlockHeight returns the exact height the glyph/title/body stack
// needs. Callers that draw the surface themselves (the notification dropdown)
// reserve this instead of guessing a row height.
func uiStateBlockHeight(config *Config, glyph, title, body string) int32 {
	glyphH, titleH, bodyH, gap := uiStateBlockMetrics(config, glyph, title, body)
	return glyphH + titleH + gap + bodyH
}

func uiStateBlock(renderer *sdl.Renderer, config *Config, rect sdl.Rect, glyph, title, body string, tone sdl.Color) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	titleFont := FontForRole(config, TypeHeading)
	bodyFont := FontForRole(config, TypeCaption)
	glyphFont := FontForRole(config, TypeTitle)
	if titleFont == nil {
		return
	}

	glyphH, titleH, bodyH, gap := uiStateBlockMetrics(config, glyph, title, body)
	bellGlyph := glyph == "@bell"
	stackH := glyphH + titleH + gap + bodyH
	y := rect.Y + (rect.H-stackH)/2
	if y < rect.Y {
		y = rect.Y
	}
	cx := rect.X + rect.W/2

	if glyphH > 0 {
		if bellGlyph {
			drawBellIcon(renderer, cx, y+glyphH/2, 36, tone)
		} else {
			gw, _, _ := glyphFont.SizeUTF8(glyph)
			renderText(renderer, config, glyphFont, glyph, tone, cx-int32(gw)/2, y)
		}
		y += glyphH
	}
	tw, _, _ := titleFont.SizeUTF8(title)
	renderText(renderer, config, titleFont, title, ColorTextPrimary(), cx-int32(tw)/2, y)
	y += titleH + gap
	if bodyH > 0 {
		bw, _, _ := bodyFont.SizeUTF8(body)
		renderText(renderer, config, bodyFont, body, ColorTextSecondary(), cx-int32(bw)/2, y)
	}
}

// UIGridColumns returns how many cells of at least minCellW fit across areaW
// with the given gap. It always returns at least 1 so layout maths never
// divides by zero, even in a zero-width element during startup.
func UIGridColumns(areaW, minCellW, gap int32) int {
	if areaW <= 0 || minCellW <= 0 {
		return 1
	}
	if gap < 0 {
		gap = 0
	}
	cols := int((areaW + gap) / (minCellW + gap))
	if cols < 1 {
		cols = 1
	}
	return cols
}

// UIGridCellWidth distributes areaW across cols cells and gaps. The result is
// never negative; a too-narrow area yields zero-width cells rather than a
// rectangle that wraps around.
func UIGridCellWidth(areaW int32, cols int, gap int32) int32 {
	if cols < 1 {
		cols = 1
	}
	if gap < 0 {
		gap = 0
	}
	w := (areaW - gap*int32(cols-1)) / int32(cols)
	if w < 0 {
		return 0
	}
	return w
}

// UIGridVisibleRows returns how many rows of at least rowH fit in areaH. It
// always returns at least 1, matching UIGridColumns.
func UIGridVisibleRows(areaH, rowH, gap int32) int {
	if areaH <= 0 || rowH <= 0 {
		return 1
	}
	if gap < 0 {
		gap = 0
	}
	rows := int((areaH + gap) / (rowH + gap))
	if rows < 1 {
		rows = 1
	}
	return rows
}

// UIGridCellRect returns the rect of cell index in a grid anchored at
// (originX, originY). Index is zero-based, left to right, top to bottom.
func UIGridCellRect(index, cols int, originX, originY, cellW, cellH, gap int32) sdl.Rect {
	if cols < 1 {
		cols = 1
	}
	if index < 0 {
		index = 0
	}
	row := index / cols
	col := index % cols
	return sdl.Rect{
		X: originX + int32(col)*(cellW+gap),
		Y: originY + int32(row)*(cellH+gap),
		W: cellW,
		H: cellH,
	}
}

// ── Page header ───────────────────────────────────────────────────────────

// UIPageHeader is the bar a page or a full-screen panel opens with: an optional
// glyph well, the title, an optional subtitle under it and an optional status
// read-out at the right edge.
//
// The Tools page, the Tools overlay and the About screen each used to lay this
// bar out by hand, with their own offsets and their own idea of where the
// subtitle goes; they all build one of these now, and UIPageHeaderDraw is the
// only code that draws it.
type UIPageHeader struct {
	X, Y, W  int32
	Title    string
	Subtitle string
	// Status is the right-aligned read-out ("WiFi · 10.0.0.5", "Offline"). In a
	// centred header it becomes a third stacked line.
	Status      string
	StatusColor sdl.Color
	// TitleRole picks the title's font role ("big", "medium" or "small"; the
	// zero value means medium); TitleSize overrides its pixel size.
	TitleRole    string
	TitleSize    int
	SubtitleRole string // default "small"
	StatusRole   string // default "small"
	// Glyph draws the icon inside the tinted well. Nil means no well, and the
	// title starts at X.
	Glyph func(renderer *sdl.Renderer, box sdl.Rect, tint sdl.Color)
	// Centered stacks the three lines in the middle of the band: a brand block
	// (About) rather than a page bar.
	Centered bool
}

// UIPageHeaderMetrics is the band's measured geometry. A layout that reserves
// room for the header and the draw that fills it both call
// uiPageHeaderMetricsFor, so the two cannot disagree about how tall it is.
type UIPageHeaderMetrics struct {
	TitleH    int32
	LineH     int32
	WellBox   int32
	TitleY    int32
	SubtitleY int32
	StatusY   int32
	Height    int32
}

func uiHeaderRole(role, fallback string) string {
	if strings.TrimSpace(role) == "" {
		return fallback
	}
	return role
}

// uiPageHeaderFont resolves one line's font, falling back through the theme's
// own roles so a config that overrides only "small" still gets text.
func uiPageHeaderFont(config *Config, role string, size int) *ttf.Font {
	role = uiHeaderRole(role, "medium")
	if size > 0 {
		if font := getFontSized(config, role, size); font != nil {
			return font
		}
	}
	if font, _ := getCachedFont(config, role); font != nil {
		return font
	}
	if font, _ := getCachedFont(config, "medium"); font != nil {
		return font
	}
	font, _ := getCachedFont(config, "small")
	return font
}

func uiPageHeaderMetricsFor(config *Config, h UIPageHeader) UIPageHeaderMetrics {
	title := uiPageHeaderFont(config, h.TitleRole, h.TitleSize)
	sub := uiPageHeaderFont(config, h.SubtitleRole, 0)
	status := uiPageHeaderFont(config, h.StatusRole, 0)
	if sub == nil {
		sub = status
	}
	if status == nil {
		status = sub
	}
	m := UIPageHeaderMetrics{TitleH: 28, LineH: 20}
	if title != nil {
		m.TitleH = int32(title.Height())
	}
	if sub != nil {
		m.LineH = int32(sub.Height())
	}
	if h.Glyph != nil {
		m.WellBox = m.TitleH + 10
	}

	if h.Centered {
		m.TitleY = 0
		m.SubtitleY = m.TitleY + m.TitleH + 6
		m.StatusY = m.SubtitleY + m.LineH + 4
		m.Height = m.TitleY + m.TitleH
		if strings.TrimSpace(h.Subtitle) != "" {
			m.Height = m.SubtitleY + m.LineH
		}
		if strings.TrimSpace(h.Status) != "" {
			m.Height = m.StatusY + m.LineH
		}
		return m
	}

	// Left-aligned: the title sits in the well's first line, the subtitle goes
	// directly under the well, and the status is centred against the well.
	m.TitleY = 5
	m.SubtitleY = m.WellBox + 2
	if m.WellBox == 0 {
		m.SubtitleY = m.TitleY + m.TitleH + 4
	}
	m.StatusY = m.TitleY
	if m.WellBox > 0 {
		m.StatusY = (m.WellBox - m.LineH) / 2
	}
	m.Height = m.TitleY + m.TitleH
	if m.WellBox > 0 {
		m.Height = m.WellBox
	}
	if strings.TrimSpace(h.Subtitle) != "" {
		m.Height = m.SubtitleY + m.LineH
	}
	return m
}

// UIPageHeaderDraw paints the band and returns the y where the page's content
// starts, so a caller never has to re-derive the header's height.
func UIPageHeaderDraw(renderer *sdl.Renderer, config *Config, h UIPageHeader) int32 {
	if renderer == nil {
		return h.Y
	}
	m := uiPageHeaderMetricsFor(config, h)
	title := uiPageHeaderFont(config, h.TitleRole, h.TitleSize)
	sub := uiPageHeaderFont(config, h.SubtitleRole, 0)
	status := uiPageHeaderFont(config, h.StatusRole, 0)

	textX := h.X
	if h.Glyph != nil && m.WellBox > 0 {
		tint := appGridTileTint(h.Title)
		fillRoundedRect(renderer, h.X, h.Y, m.WellBox, m.WellBox, 12, WithAlpha(tint, 44))
		strokeRoundedRect(renderer, h.X, h.Y, m.WellBox, m.WellBox, 12, 1, WithAlpha(tint, 150))
		h.Glyph(renderer, insetRect(sdl.Rect{X: h.X, Y: h.Y, W: m.WellBox, H: m.WellBox}, 5), tint)
		textX = h.X + m.WellBox + 14
	}

	if h.Centered {
		if title != nil {
			tw, _, _ := title.SizeUTF8(h.Title)
			renderText(renderer, config, title, h.Title, ColorTextPrimary(), h.X+(h.W-int32(tw))/2, h.Y+m.TitleY)
		}
		if sub != nil && strings.TrimSpace(h.Subtitle) != "" {
			sw, _, _ := sub.SizeUTF8(h.Subtitle)
			renderText(renderer, config, sub, h.Subtitle, ColorTextSecondary(), h.X+(h.W-int32(sw))/2, h.Y+m.SubtitleY)
		}
		if status != nil && strings.TrimSpace(h.Status) != "" {
			color := h.StatusColor
			if color.A == 0 {
				color = ColorTextTertiary()
			}
			sw, _, _ := status.SizeUTF8(h.Status)
			renderText(renderer, config, status, h.Status, color, h.X+(h.W-int32(sw))/2, h.Y+m.StatusY)
		}
		return h.Y + m.Height
	}

	if title != nil {
		avail := h.X + h.W - textX
		renderText(renderer, config, title, ellipsize(title, h.Title, avail), ColorTextPrimary(), textX, h.Y+m.TitleY)
	}
	if sub != nil && strings.TrimSpace(h.Subtitle) != "" {
		renderText(renderer, config, sub, ellipsize(sub, h.Subtitle, h.W), ColorTextTertiary(), h.X, h.Y+m.SubtitleY)
	}
	if status != nil && strings.TrimSpace(h.Status) != "" {
		color := h.StatusColor
		if color.A == 0 {
			color = ColorTextTertiary()
		}
		if sw, _, err := status.SizeUTF8(h.Status); err == nil {
			renderText(renderer, config, status, h.Status, color, h.X+h.W-int32(sw), h.Y+m.StatusY)
		}
	}
	return h.Y + m.Height
}

// ── Category tile ────────────────────────────────────────────────────────

// UICategoryTile is the launcher's tile recipe: a card, a tinted icon well, a
// two-line name and the shared focus treatment. The Tools page and the Tools
// overlay draw through it (and the Apps grid uses the same colours), so a tile
// looks the same wherever it appears.
type UICategoryTile struct {
	Rect  sdl.Rect
	Label string
	// Surface picks the card recipe: a tile on the page's own backdrop, or a
	// tile standing on a raised panel (the Tools overlay), which needs the
	// panel's surface instead of the page's card colour.
	Surface UITileSurface
	// Glyph draws the icon inside the well; it receives the inset box and the
	// tile's tint (accent while focused).
	Glyph    func(renderer *sdl.Renderer, box sdl.Rect, tint sdl.Color)
	Focused  bool
	Hovered  bool
	Pressed  bool
	Disabled bool
	// HoverProgress is 0..1, so a hover ring can fade in.
	HoverProgress float64
	// Status draws the corner dot (running / needs attention). Zero alpha draws
	// nothing.
	Status sdl.Color
	// LabelSize caps the two-line name; 0 uses the tile default.
	LabelSize int
	Radius    int32
}

// UITileSurface is which surface a category tile is painted on.
type UITileSurface int

const (
	// UITileCard is the launcher's resting card: the theme's card colour, for a
	// tile on the page background.
	UITileCard UITileSurface = iota
	// UITileRaised is for a tile inside a raised panel, where the card colour is
	// the panel colour and the tile would otherwise disappear into it.
	UITileRaised
)

// uiTileLabelFont picks the largest label whose two lines fit the space under
// the icon. A theme whose "small" font runs tall (30px glyphs at a 20px
// request) would otherwise wrap to one clipped line while its neighbours
// wrapped to two.
func uiTileLabelFont(config *Config, avail int32, cap int) (*ttf.Font, int32) {
	if cap <= 0 {
		cap = 22
	}
	for size := cap; size >= 14; size -= 2 {
		font := getFontSized(config, "small", size)
		if font == nil {
			continue
		}
		lineH := int32(font.Height() + 1)
		if avail <= 0 || lineH*2 <= avail || size <= 14 {
			return font, lineH
		}
	}
	font := appTileLabelFont(config)
	if font == nil {
		return nil, 0
	}
	return font, int32(font.Height() + 1)
}

// UICategoryTileDraw paints one tile. Hit-testing and layout stay with the
// caller; this only owns the pixels.
func UICategoryTileDraw(renderer *sdl.Renderer, config *Config, tile UICategoryTile) {
	w, h := tile.Rect.W, tile.Rect.H
	if renderer == nil || w <= 0 || h <= 0 {
		return
	}
	radius := tile.Radius
	if radius <= 0 {
		radius = TileRadius
	}
	focusCol := focusColorForAccent(accentColor)

	ox, oy := tile.Rect.X, tile.Rect.Y
	if tile.Pressed {
		age := float64(sdl.GetTicks64() - pressStartTime)
		off := int32(math.Round(float64(PressOffset) * uiPressProgress(config, age, float64(pressDuration))))
		ox += off
		oy += off
	}

	if tile.Focused {
		fillRoundedRect(renderer, ox-FocusGlow, oy-FocusGlow, w+2*FocusGlow, h+2*FocusGlow,
			radius+FocusGlow, WithAlpha(focusCol, 24))
		strokeRoundedRect(renderer, ox-FocusRing, oy-FocusRing, w+2*FocusRing, h+2*FocusRing,
			radius+FocusRing, FocusRing, focusCol)
	} else if tile.HoverProgress > 0.01 {
		a := uint8(80 * uiClamp01(tile.HoverProgress))
		strokeRoundedRect(renderer, ox-HoverRing, oy-HoverRing, w+2*HoverRing, h+2*HoverRing,
			radius+HoverRing, HoverRing, WithAlpha(focusCol, a))
	}

	base, border := HomeCardColor(), HomeBorderColor()
	if tile.Surface == UITileRaised {
		base = ColorSurfaceRaised
	}
	if tile.Focused {
		base, border = HomeCardFocusColor(), WithAlpha(focusCol, 200)
		if tile.Surface == UITileRaised {
			// The raised surface is the panel's own colour: tint it so a focused
			// tile still reads as raised, not as a hole in the panel.
			base = ColorSurfaceRaised
		}
	} else if tile.Hovered {
		border = WithAlpha(focusCol, 120)
	}
	fillRoundedRect(renderer, ox, oy, w, h, radius, base)
	if tile.Focused && tile.Surface == UITileRaised {
		fillRoundedRect(renderer, ox, oy, w, h, radius, WithAlpha(accentColor, 70))
		strokeRoundedRect(renderer, ox, oy, w, h, radius, 2, WithAlpha(accentColor, 235))
	} else {
		strokeRoundedRect(renderer, ox, oy, w, h, radius, 1, border)
	}

	if tile.Status.A > 0 {
		fillCircle(renderer, ox+w-13, oy+13, 4, tile.Status)
	}

	tint := appGridTileTint(tile.Label)
	iconCol := tint
	if tile.Focused {
		iconCol = focusCol
	}
	// The well is a share of the tile's own height, so a short tile and a tall
	// one keep the same proportions instead of one of them losing its label.
	box := int32(float64(h) * 0.32)
	if box > 52 {
		box = 52
	}
	if box < 32 {
		box = 32
	}
	wellX := ox + (w-box)/2
	wellY := oy + 8
	wellAlpha := uint8(40)
	if tile.Focused {
		wellAlpha = 62
	}
	fillRoundedRect(renderer, wellX, wellY, box, box, 14, WithAlpha(tint, wellAlpha))
	strokeRoundedRect(renderer, wellX, wellY, box, box, 14, 1, WithAlpha(tint, 150))
	if tile.Glyph != nil {
		tile.Glyph(renderer, insetRect(sdl.Rect{X: wellX, Y: wellY, W: box, H: box}, 8), iconCol)
	}

	if strings.TrimSpace(tile.Label) == "" {
		return
	}
	top := wellY + box + 5
	avail := oy + h - 8 - top
	font, lineH := uiTileLabelFont(config, avail, tile.LabelSize)
	if font == nil || lineH <= 0 {
		return
	}
	maxLines := int(avail / lineH)
	if maxLines < 1 {
		maxLines = 1
	}
	if maxLines > 2 {
		maxLines = 2
	}
	color := ColorTextPrimary()
	if tile.Disabled {
		color = ColorTextTertiary()
	}
	for li, line := range appTileLabelLines(font, tile.Label, w-16, maxLines) {
		lw, _, _ := font.SizeUTF8(line)
		renderText(renderer, config, font, line, color, ox+(w-int32(lw))/2, top+int32(li)*lineH)
	}
}

// ── Section label ────────────────────────────────────────────────────────

// UISectionLabel draws a group heading: a tinted glyph chip, the uppercase
// title, and a hairline running to the panel edge. The Tools page and the Tools
// overlay both group their tiles this way.
func UISectionLabel(renderer *sdl.Renderer, config *Config, title string, x, y, w int32, glyph func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color)) {
	font := getFontSized(config, "small", 15)
	if renderer == nil || font == nil {
		return
	}
	label := strings.ToUpper(strings.TrimSpace(title))
	_, th, _ := font.SizeUTF8(label)
	textY := y + 6
	textX := x
	if th > 0 && glyph != nil {
		chip := int32(th) + 12
		chipY := y + (int32(th)+14-chip)/2
		tint := appGridTileTint(label)
		fillRoundedRect(renderer, x, chipY, chip, chip, chip/4, WithAlpha(tint, 38))
		strokeRoundedRect(renderer, x, chipY, chip, chip, chip/4, 1, WithAlpha(tint, 130))
		glyph(renderer, insetRect(sdl.Rect{X: x, Y: chipY, W: chip, H: chip}, 4), tint)
		textX += chip + 10
	}
	tw, _, _ := font.SizeUTF8(label)
	renderText(renderer, config, font, label, ColorTextTertiary(), textX, textY)
	if lx := textX + int32(tw) + 12; lx < x+w {
		renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 80)
		renderer.FillRect(&sdl.Rect{X: lx, Y: textY + int32(th)/2, W: x + w - lx, H: 1})
	}
}

// UILoadingSkeleton draws a shimmering placeholder block. phase is a free
// running value in seconds; it only needs to change over time.
func UILoadingSkeleton(renderer *sdl.Renderer, rect sdl.Rect, phase float64) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	pulse := 0.5 + 0.5*math.Sin(phase*2*math.Pi)
	alpha := uint8(16 + 22*pulse)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, RadiusSM, WithAlpha(ColorTextTertiary(), alpha))
}
