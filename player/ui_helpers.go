package main

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Rendering & UI Helpers — thin wrappers used by new feature files
// These bridge the gap between the newer overlay files and the
// existing rendering primitives in main.go and design.go.
// ──────────────────────────────────────────────────────────────────────

// drawRect fills a rectangle with a solid color (alpha-aware).
func drawRect(renderer *sdl.Renderer, rect *sdl.Rect, r, g, b, a uint8) {
	if renderer == nil || rect == nil {
		return
	}
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(r, g, b, a)
	renderer.FillRect(rect)
}

// drawRoundedRect fills a rounded rectangle. This is a simplified wrapper
// around fillRoundedRect that takes separate r,g,b,a components.
func drawRoundedRect(renderer *sdl.Renderer, rect *sdl.Rect, radius int32, r, g, b, a uint8) {
	if renderer == nil || rect == nil {
		return
	}
	c := sdl.Color{R: r, G: g, B: b, A: a}
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, c)
}

// colorNew creates an sdl.Color from RGBA components.
func colorNew(r, g, b, a uint8) sdl.Color {
	return sdl.Color{R: r, G: g, B: b, A: a}
}

// getAccentColor returns the current theme's accent color from config.
func getAccentColor(config *Config) sdl.Color {
	// Use the global accent color set by ApplyThemeColors
	if accentColor.A > 0 {
		return accentColor
	}
	// Fallback: check Custom map for info color
	if config != nil {
		if v, ok := config.Variables.Custom["info"].(string); ok && v != "" {
			return hexRGBA(v)
		}
	}
	return sdl.Color{R: 80, G: 130, B: 255, A: 255}
}

// getBodyFont returns a medium-sized body font.
func getBodyFont(config *Config, size int) *ttf.Font {
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		font, _ = getCachedFont(config, "default")
	}
	return font
}

// getDisplayFont returns a larger display/heading font.
func getDisplayFont(config *Config, size int) *ttf.Font {
	font, _ := getCachedFont(config, "large")
	if font == nil {
		font, _ = getCachedFont(config, "default")
	}
	return font
}

// getSmallFont returns a small font for secondary text.
func getSmallFont(config *Config) *ttf.Font {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		font, _ = getCachedFont(config, "default")
	}
	return font
}

// getFieldFont returns a font suitable for data labels.
func getFieldFont(config *Config) *ttf.Font {
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		font, _ = getCachedFont(config, "default")
	}
	return font
}

// renderTextAligned is a convenience wrapper around drawText for center-aligned text.
func renderTextAligned(renderer *sdl.Renderer, font *ttf.Font, text string, x, y int32, color sdl.Color) {
	if font == nil || renderer == nil || strings.TrimSpace(text) == "" {
		return
	}
	drawText(renderer, font, text, x, y, color, textAlignCenter)
}

// drawDivider renders a subtle horizontal divider line.
func drawDivider(renderer *sdl.Renderer, x, y, w int32, color sdl.Color) {
	if renderer == nil || w <= 0 {
		return
	}
	if color.A == 0 {
		color = ColorDivider
	}
	renderer.SetDrawColor(color.R, color.G, color.B, color.A)
	renderer.DrawLine(x, y, x+w, y)
}

// drawChip renders a compact pill-style tag with an icon or text inside.
func drawChip(renderer *sdl.Renderer, rect sdl.Rect, label string, color, textColor sdl.Color, font *ttf.Font) {
	if renderer == nil {
		return
	}
	r := int32(11)
	if rect.W < rect.H {
		r = rect.W / 2
	}
	// shadow
	fillRoundedRect(renderer, rect.X+1, rect.Y+2, rect.W, rect.H, r, ShadowFill(18))
	// body
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, r, color)
	// hairline
	renderer.SetDrawColor(255, 255, 255, 12)
	renderer.DrawRect(&sdl.Rect{X: rect.X + 1, Y: rect.Y + 1, W: rect.W - 2, H: 1})
	renderer.DrawRect(&sdl.Rect{X: rect.X + 1, Y: rect.Y + 1, W: 1, H: rect.H - 2})
	if font != nil && label != "" {
		lw, lh, _ := font.SizeUTF8(label)
		tx := rect.X + (rect.W-int32(lw))/2
		ty := rect.Y + (rect.H-int32(lh))/2
		renderText(renderer, nil, font, label, textColor, tx, ty)
	}
}

// drawSimpleProgress renders a compact progress bar with track + fill.
func drawSimpleProgress(renderer *sdl.Renderer, x, y, w, h int32, fraction float64) {
	if renderer == nil || w <= 0 || h <= 0 || fraction <= 0 {
		return
	}
	if fraction > 1 {
		fraction = 1
	}
	// track
	fillRoundedRect(renderer, x, y, w, h, h/2, ColorTrack)
	// fill
	fillW := int32(float64(w) * fraction)
	if fillW < 4 {
		fillW = 4
	}
	fillRoundedRect(renderer, x, y, fillW, h, h/2, ColorProgress)
	// leading highlight on the fill edge
	if fillW > 4 {
		renderer.SetDrawColor(255, 255, 255, 40)
		renderer.FillRect(&sdl.Rect{X: x, Y: y + 1, W: fillW - 2, H: 1})
	}
}
