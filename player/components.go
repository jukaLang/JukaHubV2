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

	"github.com/veandco/go-sdl2/sdl"
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
		fillRoundedRect(renderer, x, y, w, h, radius, WithAlpha(accentColor, 50))
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
		renderer.FillRect(&sdl.Rect{X: x + 1, Y: y + 2, W: 3, H: h - 4})
		renderer.SetDrawColor(255, 255, 255, 20)
		renderer.FillRect(&sdl.Rect{X: x + 4, Y: y + 1, W: w - 8, H: 1})
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 15)
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

	glyphH, titleH, bodyH := int32(0), int32(titleFont.Height()), int32(0)
	if bodyFont != nil && body != "" {
		bodyH = int32(bodyFont.Height())
	}
	if glyphFont != nil && glyph != "" {
		glyphH = int32(glyphFont.Height()) + SpaceMD
	}
	gap := int32(0)
	if bodyH > 0 {
		gap = SpaceXS
	}
	stackH := glyphH + titleH + gap + bodyH
	y := rect.Y + (rect.H-stackH)/2
	if y < rect.Y {
		y = rect.Y
	}
	cx := rect.X + rect.W/2

	if glyphH > 0 {
		gw, _, _ := glyphFont.SizeUTF8(glyph)
		renderText(renderer, config, glyphFont, glyph, tone, cx-int32(gw)/2, y)
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
