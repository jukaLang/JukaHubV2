package main

// quickmenu_ui.go — drawing the Quick menu (quickmenu.go): the focused
// entry's picture or JukaHub's backdrop, the big tiles, the round buttons
// under them, the status bar and the footer. With "Backgrounds only" on and a
// picture for the focused entry, only the picture and a small label pill.

import (
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// quickMenuRowGap is the space between the tiles and the buttons.
	quickMenuRowGap = int32(28)
	// quickMenuTileGap is the space between two tiles.
	quickMenuTileGap = int32(16)
)

// quickMenuRender draws the screen. Render thread.
func quickMenuRender(renderer *sdl.Renderer, config *Config) {
	if quickMenu.visit.begin() || len(quickMenu.top)+len(quickMenu.bottom) == 0 {
		quickMenuEnter(config)
	}
	full := sdl.Rect{W: screenWidth, H: screenHeight}
	focused, _ := quickMenuFocused()
	bg := quickMenuBgTexture(renderer, focused.theme)
	if bg != nil {
		_ = renderer.SetDrawColor(0, 0, 0, 255)
		_ = renderer.FillRect(&full)
		drawCoverFill(renderer, bg, full)
	} else {
		drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	if bg != nil && quickMenuBgOnly(config) {
		quickMenuDrawLabelPill(renderer, config, fonts, focused)
		tbxRenderSheet(renderer, config)
		renderer.SetClipRect(nil)
		return
	}
	sheet := tbxSheetVisible(config)
	tilesRect, buttonsY, btn := quickMenuLayout(fonts)
	quickMenuDrawTiles(renderer, config, fonts, tilesRect, sheet)
	quickMenuDrawButtons(renderer, config, fonts, buttonsY, btn, sheet)
	peerEndFrame(renderer, config, quickMenuHints(focused))
}

// quickMenuLayout places the two rows: the tiles' band, the buttons' top and
// the buttons' size, the whole block centred between the bars.
func quickMenuLayout(fonts tbxFonts) (tiles sdl.Rect, buttonsY, btn int32) {
	top := headerBarHeight() + 16
	bottom := screenHeight - bottomBarHeight("") - 16
	margin := max32(16, scaleX(homeSideMargin, screenWidth))
	areaW := screenWidth - 2*margin
	btn = max32(44, min32(84, screenHeight/9))
	btnRowH := btn + 8 + fonts.smallH
	n := int32(max(1, len(quickMenu.top)))
	tileW := (areaW - quickMenuTileGap*(n-1)) / n
	avail := bottom - top
	tileH := avail - btnRowH - quickMenuRowGap
	if tileH > tileW*5/4 {
		tileH = tileW * 5 / 4
	}
	if tileH < 72 {
		tileH = 72
	}
	y := top + (avail-(tileH+quickMenuRowGap+btnRowH))/2
	if y < top {
		y = top
	}
	return sdl.Rect{X: margin, Y: y, W: areaW, H: tileH}, y + tileH + quickMenuRowGap, btn
}

// quickMenuDrawTiles draws the big tiles across band.
func quickMenuDrawTiles(renderer *sdl.Renderer, config *Config, fonts tbxFonts, band sdl.Rect, sheet bool) {
	items := quickMenu.top
	n := int32(len(items))
	if n == 0 {
		return
	}
	tileW := (band.W - quickMenuTileGap*(n-1)) / n
	for i, it := range items {
		rect := sdl.Rect{X: band.X + int32(i)*(tileW+quickMenuTileGap), Y: band.Y, W: tileW, H: band.H}
		focused := !sheet && quickMenu.row == 0 && quickMenu.col == i
		if focused {
			drawGlassFocusGlow(renderer, rect)
		}
		drawGlassSurface(renderer, rect, glassControlRadius, glassState{Focused: focused})
		g := min32(96, min32(rect.W, rect.H)*2/5)
		labelH := fonts.titleH
		glyphY := rect.Y + (rect.H-g-10-labelH)/2
		iconDraw(renderer, it.glyph, sdl.Rect{X: rect.X + (rect.W-g)/2, Y: glyphY, W: g, H: g}, ColorTextPrimary())
		label := ellipsize(fonts.title, it.label, rect.W-16)
		drawText(renderer, fonts.title, label, rect.X+rect.W/2, glyphY+g+10, ColorTextPrimary(), textAlignCenter)
	}
}

// quickMenuDrawButtons draws the round buttons in a centred row at y, each
// btn across with its label under it.
func quickMenuDrawButtons(renderer *sdl.Renderer, config *Config, fonts tbxFonts, y, btn int32, sheet bool) {
	items := quickMenu.bottom
	n := int32(len(items))
	if n == 0 {
		return
	}
	margin := max32(16, scaleX(homeSideMargin, screenWidth))
	slotW := min32((screenWidth-2*margin)/n, btn*2+24)
	x0 := (screenWidth - slotW*n) / 2
	for i, it := range items {
		cx := x0 + int32(i)*slotW + slotW/2
		rect := sdl.Rect{X: cx - btn/2, Y: y, W: btn, H: btn}
		focused := !sheet && quickMenu.row == 1 && quickMenu.col == i
		glyph, label, lit, busy := it.glyph, it.label, false, false
		if it.id == qmWifi {
			look := hqWifiLook(time.Now())
			glyph, lit, busy = look.glyph, look.on, look.busy
			if focused && look.sub != "" {
				label = "Wi-Fi · " + look.sub
			}
		}
		if focused {
			drawGlassFocusGlow(renderer, rect)
		}
		drawGlassSurface(renderer, rect, btn/2, glassState{Focused: focused})
		if lit {
			// A radio that is on: the button glows in the accent.
			fillRoundedRect(renderer, rect.X+4, rect.Y+4, rect.W-8, rect.H-8, (rect.W-8)/2, WithAlpha(glassAccent(), 90))
		}
		if busy {
			renderSpinner(renderer, cx, y+btn/2, btn/4, ColorTextPrimary())
		} else {
			g := btn / 2
			iconDraw(renderer, glyph, sdl.Rect{X: cx - g/2, Y: y + (btn-g)/2, W: g, H: g}, ColorTextPrimary())
		}
		ink := ColorTextSecondary()
		if focused {
			ink = ColorTextPrimary()
		}
		text := ellipsize(fonts.small, label, slotW-4)
		drawText(renderer, fonts.small, text, cx, y+btn+8, ink, textAlignCenter)
	}
}

// quickMenuDrawLabelPill is "Backgrounds only": the focused entry's name and
// the keys, in a small pill near the bottom edge.
func quickMenuDrawLabelPill(renderer *sdl.Renderer, config *Config, fonts tbxFonts, it quickMenuItem) {
	if it.label == "" {
		return
	}
	hint := "A " + quickMenuActionLabel(it) + " · B Close"
	nameW, _ := measureText(fonts.body, it.label)
	hintW, _ := measureText(fonts.small, hint)
	w := max32(nameW, hintW) + 40
	h := fonts.bodyH + fonts.smallH + 18
	x := (screenWidth - w) / 2
	y := screenHeight - h - 24
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, x, y, w, h, h/2, WithAlpha(ColorPanelRaised, 210))
	drawText(renderer, fonts.body, it.label, screenWidth/2, y+8, ColorTextPrimary(), textAlignCenter)
	drawText(renderer, fonts.small, hint, screenWidth/2, y+10+fonts.bodyH, ColorTextSecondary(), textAlignCenter)
}

// quickMenuActionLabel is what A does on an entry, for the footer.
func quickMenuActionLabel(it quickMenuItem) string {
	switch it.id {
	case qmWifi:
		if look := hqWifiLook(time.Now()); look.on {
			return "Turn off"
		}
		return "Turn on"
	case qmSleep:
		return "Sleep"
	case qmRestart:
		return "Restart"
	case qmPowerOff:
		return "Power off"
	case qmExit:
		return "Exit"
	}
	return "Open"
}

// quickMenuHints is the footer: what A does here, and B.
func quickMenuHints(it quickMenuItem) []FooterHint {
	return []FooterHint{
		{Button: "A", Label: quickMenuActionLabel(it)},
		{Button: "B", Label: "Close"},
	}
}
