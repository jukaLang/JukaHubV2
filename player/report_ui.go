package main

// report_ui.go — one card layout for the community overlay's text pages:
// ROM Audit, Play Journal, Steam Achievements, the plugin and WINE catalogues,
// Offline reference. They used to each measure and draw the same thing with
// slightly different padding, so the same list looked different on each page.
// Keeping it here gives every page the same hairline, wrap, scroll and footer.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// drawTabUnderline is the same small active cue on every button-style tab
// strip. It fits inside the strip's own height, so no layout changes.
func drawTabUnderline(renderer *sdl.Renderer, rect sdl.Rect) {
	ink := installHighlight()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(ink.R, ink.G, ink.B, 210)
	renderer.FillRect(&sdl.Rect{X: rect.X + 6, Y: rect.Y + rect.H - 3, W: max(rect.W-12, 8), H: 3})
}

func renderReportCard(renderer *sdl.Renderer, config *Config, font *ttf.Font,
	panelX, panelY, panelW, panelH, padX, headerH int32, lines []string, hints []FooterHint) {
	contentX := panelX + padX
	contentW := panelW - 2*padX
	lineH := int32(font.Height()) + 7
	y := panelY + headerH + 16

	// A hairline under the title band separates the report from the header
	// without taking another font line away from the list.
	accent := installHighlight()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(accent.R, accent.G, accent.B, 60)
	renderer.FillRect(&sdl.Rect{X: contentX, Y: y - 8, W: contentW, H: 1})

	charW, _, _ := font.SizeUTF8("M")
	if charW < 1 {
		charW = 1
	}
	wrapped := biosDoctorWrapLines(lines, int(contentW/int32(charW)))
	available := int((panelY + panelH - 44 - y) / lineH)
	if available < 1 {
		available = 1
	}
	maxScroll := len(wrapped) - available
	if maxScroll < 0 {
		maxScroll = 0
	}
	if communityPanel.scroll > maxScroll {
		communityPanel.scroll = maxScroll
	}
	if communityPanel.scroll < 0 {
		communityPanel.scroll = 0
	}
	for i := 0; i < available; i++ {
		idx := communityPanel.scroll + i
		if idx >= len(wrapped) {
			break
		}
		line := wrapped[idx]
		if line == "" {
			y += lineH
			continue
		}
		ink := ColorTextSecondary()
		if strings.HasPrefix(line, "›") || (len(line) > 0 && line[0] != ' ' && !strings.HasPrefix(line, "Last lines")) {
			ink = ColorTextPrimary()
		}
		renderText(renderer, config, font, ellipsize(font, line, contentW), ink, contentX, y)
		y += lineH
	}
	footer := hintLine(hints...)
	fw, _, _ := font.SizeUTF8(footer)
	renderText(renderer, config, font, footer, ColorTextTertiary(),
		panelX+(panelW-int32(fw))/2, panelY+panelH-30)
}
