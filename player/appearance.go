package main

// Live preview for Settings → Appearance. The three colour fields are hex text
// boxes by nature; this panel shows what the saved palette actually looks like
// on real controls, so the screen reads as a design surface instead of a form.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// renderAppearancePreview draws a sample card using the theme colours currently
// stored in the config. It is a no-op on narrow screens, where the form already
// uses the full width.
func renderAppearancePreview(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || config == nil || screenWidth < 900 {
		return
	}

	labelCol := resolveColor(config, "$labelColor", sdl.Color{R: 240, G: 240, B: 240, A: 255})
	buttonCol := resolveColor(config, "$buttonColor", sdl.Color{R: 120, G: 130, B: 255, A: 255})
	inputCol := resolveColor(config, "$inputColor", sdl.Color{R: 20, G: 24, B: 34, A: 255})

	x := int32(620)
	y := int32(132)
	// Leave the same 24px inset the surrounding panel uses on its right edge.
	w := screenWidth - 48 - x
	h := int32(258)
	if w < 280 || y+h > screenHeight-90 {
		return
	}

	// Card shell.
	fillRoundedRect(renderer, x, y, w, h, RadiusLG, WithAlpha(ColorSurfacePanel, 210))
	strokeRoundedRect(renderer, x, y, w, h, RadiusLG, 1, WithAlpha(ColorBorder, 120))

	small, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if small == nil || medium == nil {
		return
	}

	caption := ColorTextTertiary()
	renderText(renderer, config, small, "PREVIEW", caption, x+24, y+18)

	// A sample primary button in the chosen button colour.
	btnW, btnH := int32(200), int32(54)
	btnY := y + 52
	fillRoundedRect(renderer, x+24, btnY, btnW, btnH, RadiusMD, buttonCol)
	strokeRoundedRect(renderer, x+24, btnY, btnW, btnH, RadiusMD, 1, sdl.Color{R: 255, G: 255, B: 255, A: 26})
	btnLabel := "Continue"
	if bw, bh, err := medium.SizeUTF8(btnLabel); err == nil {
		renderText(renderer, config, medium, btnLabel, labelCol,
			x+24+(btnW-int32(bw))/2, btnY+(btnH-int32(bh))/2)
	}

	// A sample label and input using the other two colours.
	renderText(renderer, config, small, "Label colour", labelCol, x+24, btnY+btnH+22)

	inputY := btnY + btnH + 52
	inputH := int32(48)
	fillRoundedRect(renderer, x+24, inputY, w-48, inputH, RadiusMD, inputCol)
	strokeRoundedRect(renderer, x+24, inputY, w-48, inputH, RadiusMD, 1, sdl.Color{R: 255, G: 255, B: 255, A: 22})
	renderText(renderer, config, small, "Input background", labelCol, x+38, inputY+12)
}
