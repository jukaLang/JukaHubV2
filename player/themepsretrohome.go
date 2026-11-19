package main

// The three early PlayStation styles keep JukaHub's controls and tile actions,
// but give Home a menu composition of its own. The PS1 uses a paged, bevelled
// system menu; the PS2 a vertical browser beside a blue orbit; the PSP a
// horizontal icon crossbar over quiet waves. No console marks or artwork are
// used. These styles are deliberately confined to the Carousel arrangement:
// their matching Colour presets remain ordinary palettes.

import (
	"math"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	homeCarouselPS1 = "ps1"
	homeCarouselPS2 = "ps2"
	homeCarouselPSP = "psp"

	homePS1PageSize = 6
)

func homeRetroStyle(style string) bool {
	switch style {
	case homeCarouselPS1, homeCarouselPS2, homeCarouselPSP:
		return true
	}
	return false
}

func (h *HomeLayout) retroActive() bool {
	return h != nil && h.dynCarousel() && homeRetroStyle(homeConsoleLook.carousel)
}

// The hit rectangles also drive focus and clicks. Off-page PS1 tiles and
// off-screen PS2/PSP entries have no rectangle, so they cannot be tapped.
func (h *HomeLayout) retroTileRect(i int) (sdl.Rect, bool) {
	if h == nil || i < 0 || i >= len(h.dyn.tiles) {
		return sdl.Rect{}, false
	}
	switch homeConsoleLook.carousel {
	case homeCarouselPS1:
		if i/homePS1PageSize != h.dyn.carIdx/homePS1PageSize {
			return sdl.Rect{}, false
		}
		cell := i % homePS1PageSize
		return h.carDesignRect(112+float64(cell%3)*354, 220+float64(cell/3)*176, 328, 148), true
	case homeCarouselPS2:
		d := i - h.dyn.carIdx
		if d < -3 || d > 3 {
			return sdl.Rect{}, false
		}
		return h.carDesignRect(142, 211+float64(d+3)*55, 650, 48), true
	case homeCarouselPSP:
		d := i - h.dyn.carIdx
		if d < -4 || d > 4 {
			return sdl.Rect{}, false
		}
		side := 76.0
		if d == 0 {
			side = 104
		}
		// XMB icons stay square even on the compact 4:3 canvas.
		sx := float64(h.Width) / float64(HomeBaseWidth)
		sy := float64(h.Height) / float64(HomeBaseHeight)
		k := math.Min(sx, sy)
		pix := int32(math.Round(side * k))
		cx := int32(math.Round((640 + float64(d)*148) * sx))
		cy := int32(math.Round(285 * sy))
		r := sdl.Rect{X: cx - pix/2, Y: cy - pix/2, W: pix, H: pix}
		if r.X+r.W <= 0 || r.X >= h.Width {
			return sdl.Rect{}, false
		}
		return r, true
	}
	return sdl.Rect{}, false
}

// PS1 pages advance at their edge. The PS2 browser walks vertically; the PSP
// crossbar walks horizontally. Up and Down still reach the app's top bar and
// footer at the relevant edge.
func (h *HomeLayout) retroStepFrom(cur HomeFocusID, i int, direction FocusDirection) (HomeFocusID, bool) {
	n := len(h.dyn.tiles)
	if n == 0 {
		return cur, true
	}
	switch homeConsoleLook.carousel {
	case homeCarouselPS1:
		cell := i % homePS1PageSize
		switch direction {
		case FocusLeft:
			if cell%3 > 0 || (cell == 0 && i > 0) {
				return h.dyn.tiles[i-1].ID, true
			}
		case FocusRight:
			if i+1 < n && (cell%3 < 2 || cell == homePS1PageSize-1) {
				return h.dyn.tiles[i+1].ID, true
			}
		case FocusUp:
			if cell >= 3 {
				return h.dyn.tiles[i-3].ID, true
			}
			return "", false
		case FocusDown:
			if cell < 3 && i+3 < n && (i+3)/homePS1PageSize == i/homePS1PageSize {
				return h.dyn.tiles[i+3].ID, true
			}
			return "", false
		}
		return cur, true
	case homeCarouselPS2:
		switch direction {
		case FocusUp:
			if i > 0 {
				return h.dyn.tiles[i-1].ID, true
			}
			return "", false
		case FocusDown:
			if i+1 < n {
				return h.dyn.tiles[i+1].ID, true
			}
			return "", false
		}
		return cur, true
	case homeCarouselPSP:
		switch direction {
		case FocusLeft:
			if i > 0 {
				return h.dyn.tiles[i-1].ID, true
			}
		case FocusRight:
			if i+1 < n {
				return h.dyn.tiles[i+1].ID, true
			}
		case FocusUp, FocusDown:
			return "", false
		}
		return cur, true
	}
	return cur, true
}

func (h *HomeLayout) retroDrawHeading(renderer *sdl.Renderer, fonts HomeFonts) bool {
	if !h.retroActive() {
		return false
	}
	heading := ""
	switch homeConsoleLook.carousel {
	case homeCarouselPS1:
		heading = "System menu"
	case homeCarouselPS2:
		heading = "Browser"
	case homeCarouselPSP:
		heading = "Home"
	}
	drawText(renderer, fonts.Greeting, heading, scaleX(72, h.Width), scaleY(homeGreetingY, h.Height), ColorTextPrimary(), textAlignLeft)
	return true
}

func (h *HomeLayout) retroRender(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID) {
	if renderer == nil || !h.retroActive() {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	switch homeConsoleLook.carousel {
	case homeCarouselPS1:
		h.retroDrawPS1(renderer, fonts, data, focused)
	case homeCarouselPS2:
		h.retroDrawPS2(renderer, fonts, data, focused)
	case homeCarouselPSP:
		h.retroDrawPSP(renderer, fonts, data, focused)
	}
}

func (h *HomeLayout) retroDrawPS1(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID) {
	panel := ScaleHomeRect(sdl.Rect{X: 70, Y: 164, W: 1140, H: 411}, h.Width, h.Height)
	fillRoundedRect(renderer, panel.X, panel.Y, panel.W, panel.H, scaleX(8, h.Width), WithAlpha(ColorSurface, 224))
	strokeRoundedRect(renderer, panel.X, panel.Y, panel.W, panel.H, scaleX(8, h.Width), 1, ColorBorderDefault)
	inner := sdl.Rect{X: panel.X + scaleX(4, h.Width), Y: panel.Y + scaleY(4, h.Height), W: panel.W - scaleX(8, h.Width), H: panel.H - scaleY(8, h.Height)}
	strokeRoundedRect(renderer, inner.X, inner.Y, inner.W, inner.H, scaleX(5, h.Width), 1, WithAlpha(ColorSurfaceRaised, 240))
	if fonts.Small != nil {
		drawText(renderer, fonts.Small, "MAIN MENU", panel.X+scaleX(40, h.Width), panel.Y+scaleY(15, h.Height), ColorTextSecondary(), textAlignLeft)
		pages := max(1, (len(h.dyn.tiles)+homePS1PageSize-1)/homePS1PageSize)
		page := h.dyn.carIdx/homePS1PageSize + 1
		counter := strconv.Itoa(page) + " / " + strconv.Itoa(pages)
		drawText(renderer, fonts.Small, counter, panel.X+panel.W-scaleX(105, h.Width), panel.Y+scaleY(15, h.Height), ColorTextTertiary(), textAlignLeft)
	}
	// The four controller colours are a small system-detail accent, not logos.
	dots := [...]sdl.Color{{R: 46, G: 96, B: 196, A: 255}, {R: 197, G: 55, B: 83, A: 255}, {R: 36, G: 126, B: 82, A: 255}, {R: 174, G: 66, B: 139, A: 255}}
	for i, c := range dots {
		fillCircle(renderer, panel.X+panel.W-scaleX(204-int32(i)*16, h.Width), panel.Y+scaleY(28, h.Height), max32(2, scaleX(4, h.Width)), c)
	}
	for _, t := range h.dyn.tiles {
		item := h.Items[t.ID]
		if item.Rect.W <= 0 || item.Rect.H <= 0 {
			continue
		}
		r := item.Rect
		selected := t.ID == focused
		pressed := data.Pressed == t.ID
		if pressed {
			r.Y += homePressOffset(data.PressDepth, h.Height)
		}
		face := ColorSurfaceRaised
		if selected {
			face = ColorCard
		}
		fillRoundedRectAt(renderer, item.Rect, r, homeTileRadius, face)
		strokeRoundedRectAt(renderer, item.Rect, r, homeTileRadius, 1, ColorBorderDefault)
		bevel := sdl.Rect{X: r.X + scaleX(3, h.Width), Y: r.Y + scaleY(3, h.Height), W: r.W - scaleX(6, h.Width), H: r.H - scaleY(6, h.Height)}
		strokeRoundedRect(renderer, bevel.X, bevel.Y, bevel.W, bevel.H, max32(2, homeTileRadius-2), 1, WithAlpha(ColorSurface, 245))
		accent := homeFocusRingColor()
		if selected {
			bar := sdl.Rect{X: r.X + scaleX(17, h.Width), Y: r.Y + scaleY(14, h.Height), W: r.W - scaleX(34, h.Width), H: max32(2, scaleY(3, h.Height))}
			renderer.SetDrawColor(accent.R, accent.G, accent.B, 255)
			_ = renderer.FillRect(&bar)
			drawHomeFocusRing(renderer, item.Rect, r, homeTileRadius)
		}
		pad := scaleX(20, h.Width)
		iconSide := min32(scaleX(49, h.Width), scaleY(49, h.Height))
		iconRect := sdl.Rect{X: r.X + pad, Y: r.Y + (r.H-iconSide)/2, W: iconSide, H: iconSide}
		fillRoundedRect(renderer, iconRect.X-scaleX(6, h.Width), iconRect.Y-scaleY(6, h.Height), iconRect.W+scaleX(12, h.Width), iconRect.H+scaleY(12, h.Height), scaleX(8, h.Width), WithAlpha(ColorIconSurface, 225))
		icon := item.Icon
		if icon == "" {
			icon = "apps"
		}
		ink := ColorInfo
		if t.Dim {
			ink = ColorTextTertiary()
		}
		drawHomeGlyphTinted(renderer, icon, iconRect, ink)
		textX := iconRect.X + iconRect.W + pad
		textW := r.X + r.W - textX - pad
		if fonts.Tile != nil && textW > 0 {
			drawText(renderer, fonts.Tile, ellipsize(fonts.Tile, item.Title, textW), textX, r.Y+scaleY(43, h.Height), ColorTextPrimary(), textAlignLeft)
		}
		if fonts.Meta != nil && textW > 0 {
			drawText(renderer, fonts.Meta, ellipsize(fonts.Meta, item.Subtitle, textW), textX, r.Y+scaleY(75, h.Height), ColorTextSecondary(), textAlignLeft)
		}
		if homeEdit.open && t.Kind != homeTileAdd {
			h.drawEditBadges(renderer, fonts, t, r, false)
		}
	}
}

func (h *HomeLayout) retroDrawPS2(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID) {
	// The blue orbital light gives the browser a focal point while the actual
	// list remains plain text and quick to scan.
	orb := ScaleHomeRect(sdl.Rect{X: 838, Y: 219, W: 344, H: 344}, h.Width, h.Height)
	homeGlowBlob(renderer, orb, ColorAccent, 92)
	cx, cy := orb.X+orb.W/2, orb.Y+orb.H/2
	rad := min32(orb.W, orb.H) / 2
	fillCircle(renderer, cx, cy, rad*57/100, WithAlpha(ColorSurfaceAlt, 90))
	for k, alpha := range []uint8{96, 50, 28} {
		strokeCircle(renderer, cx, cy, rad*int32(58+k*17)/100, WithAlpha(ColorAccent, alpha))
	}
	if i := h.dyn.carIdx; i >= 0 && i < len(h.dyn.tiles) {
		item := h.Items[h.dyn.tiles[i].ID]
		icon := item.Icon
		if icon == "" {
			icon = "apps"
		}
		side := min32(scaleX(84, h.Width), scaleY(84, h.Height))
		drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: cx - side/2, Y: cy - side/2, W: side, H: side}, ColorTextPrimary())
	}
	if fonts.Small != nil {
		drawText(renderer, fonts.Small, "BROWSER", scaleX(156, h.Width), scaleY(174, h.Height), ColorTextTertiary(), textAlignLeft)
	}
	for _, t := range h.dyn.tiles {
		item := h.Items[t.ID]
		r := item.Rect
		if r.W <= 0 || r.H <= 0 {
			continue
		}
		selected := t.ID == focused
		if data.Pressed == t.ID {
			r.Y += homePressOffset(data.PressDepth, h.Height)
		}
		if selected {
			fillRoundedRectAt(renderer, item.Rect, r, homeTileRadius, WithAlpha(ColorSurfaceRaised, 185))
			strokeRoundedRectAt(renderer, item.Rect, r, homeTileRadius, 1, WithAlpha(ColorAccent, 125))
			drawHomeFocusRing(renderer, item.Rect, r, homeTileRadius)
		}
		ink := ColorTextSecondary()
		if selected {
			ink = ColorTextPrimary()
		}
		if t.Dim {
			ink = ColorTextTertiary()
		}
		dot := max32(3, scaleX(5, h.Width))
		fillCircle(renderer, r.X+scaleX(24, h.Width), r.Y+r.H/2, dot, WithAlpha(ColorAccent, 215))
		if fonts.Card != nil {
			drawText(renderer, fonts.Card, ellipsize(fonts.Card, item.Title, r.W-scaleX(80, h.Width)), r.X+scaleX(50, h.Width), r.Y+(r.H-int32(fonts.Card.Height()))/2, ink, textAlignLeft)
		}
		if homeEdit.open && t.Kind != homeTileAdd {
			h.drawEditBadges(renderer, fonts, t, r, false)
		}
	}
	if i := h.dyn.carIdx; i >= 0 && i < len(h.dyn.tiles) && fonts.Helper != nil {
		item := h.Items[h.dyn.tiles[i].ID]
		drawText(renderer, fonts.Helper, ellipsize(fonts.Helper, item.Subtitle, scaleX(650, h.Width)), scaleX(157, h.Width), scaleY(604, h.Height), ColorTextSecondary(), textAlignLeft)
	}
}

func (h *HomeLayout) retroDrawPSP(renderer *sdl.Renderer, fonts HomeFonts, data HomeViewData, focused HomeFocusID) {
	h.retroPSPWaves(renderer)
	lineY := scaleY(348, h.Height)
	lineL := scaleX(72, h.Width)
	lineR := h.Width - lineL
	renderer.SetDrawColor(255, 255, 255, 44)
	_ = renderer.DrawLine(lineL, lineY, lineR, lineY)
	for _, t := range h.dyn.tiles {
		item := h.Items[t.ID]
		r := item.Rect
		if r.W <= 0 || r.H <= 0 {
			continue
		}
		selected := t.ID == focused
		if data.Pressed == t.ID {
			r.Y += homePressOffset(data.PressDepth, h.Height)
		}
		if selected {
			drawHomeFocusGlow(renderer, r)
			fillRoundedRectAt(renderer, item.Rect, r, homeTileRadius, WithAlpha(ColorSurfaceRaised, 92))
			strokeRoundedRectAt(renderer, item.Rect, r, homeTileRadius, 1, WithAlpha(ColorTextPrimary(), 135))
			drawHomeFocusRing(renderer, item.Rect, r, homeTileRadius)
		}
		icon := item.Icon
		if icon == "" {
			icon = "apps"
		}
		g := r.W * 55 / 100
		ink := WithAlpha(ColorTextPrimary(), 178)
		if selected {
			ink = ColorTextPrimary()
		}
		if t.Dim {
			ink = ColorTextTertiary()
		}
		drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: r.X + (r.W-g)/2, Y: r.Y + (r.H-g)/2, W: g, H: g}, ink)
		f := fonts.Meta
		if selected {
			f = fonts.Card
		}
		if f != nil {
			maxW := scaleX(144, h.Width)
			label := ellipsize(f, item.Title, maxW)
			width, _ := measureText(f, label)
			margin := scaleX(8, h.Width)
			cx := max32(margin+width/2, min32(r.X+r.W/2, h.Width-margin-width/2))
			drawText(renderer, f, label, cx, scaleY(369, h.Height), ink, textAlignCenter)
		}
		if homeEdit.open && t.Kind != homeTileAdd {
			h.drawEditBadges(renderer, fonts, t, r, false)
		}
	}
	if i := h.dyn.carIdx; i >= 0 && i < len(h.dyn.tiles) && fonts.Helper != nil {
		item := h.Items[h.dyn.tiles[i].ID]
		drawText(renderer, fonts.Helper, ellipsize(fonts.Helper, item.Subtitle, scaleX(650, h.Width)), h.Width/2, scaleY(414, h.Height), ColorTextSecondary(), textAlignCenter)
	}
}

func (h *HomeLayout) retroPSPWaves(renderer *sdl.Renderer) {
	homeGlowBlob(renderer, ScaleHomeRect(sdl.Rect{X: 130, Y: 350, W: 1100, H: 265}, h.Width, h.Height), ColorInfo, 60)
	const steps = 48
	var points [steps + 1]sdl.Point
	for band := 0; band < 5; band++ {
		alpha := uint8(34 + band*12)
		renderer.SetDrawColor(230, 247, 255, alpha)
		for j := range points {
			x := float64(j) / steps
			y := 465.0 + float64(band)*22 + 23*math.Sin(x*2.4*math.Pi+float64(band)*0.32) - 32*x
			points[j] = sdl.Point{X: int32(math.Round(x * float64(h.Width))), Y: int32(math.Round(y * float64(h.Height) / float64(HomeBaseHeight)))}
		}
		_ = renderer.DrawLines(points[:])
		for i := range points {
			points[i].Y++
		}
		_ = renderer.DrawLines(points[:])
	}
}

// Theme cards preview the actual composition rather than a generic Carousel.
func drawRetroHomeSketch(renderer *sdl.Renderer, style string, x, y, w, h int32, ink, accent sdl.Color) bool {
	if !homeRetroStyle(style) || renderer == nil || w <= 8 || h <= 8 {
		return false
	}
	soft := WithAlpha(ink, 82)
	switch style {
	case homeCarouselPS1:
		for i := int32(0); i < 6; i++ {
			cw := w / 4
			ch := h / 3
			rx := x + w/11 + (i%3)*(cw+w/24)
			ry := y + h/8 + (i/3)*(ch+h/14)
			fillRoundedRect(renderer, rx, ry, cw, ch, 2, soft)
			strokeRoundedRect(renderer, rx, ry, cw, ch, 2, 1, ink)
			if i == 1 {
				strokeRoundedRect(renderer, rx-2, ry-2, cw+4, ch+4, 3, 2, accent)
			}
		}
	case homeCarouselPS2:
		for i := int32(0); i < 5; i++ {
			ry := y + h/7 + i*h/7
			bar := soft
			if i == 2 {
				bar = ink
			}
			fillRoundedRect(renderer, x+w/9, ry, w*45/100, max32(2, h/20), 2, bar)
		}
		strokeCircle(renderer, x+w*78/100, y+h/2, min32(w, h)/4, accent)
		strokeCircle(renderer, x+w*78/100, y+h/2, min32(w, h)/6, soft)
	case homeCarouselPSP:
		cy := y + h*42/100
		for i := int32(0); i < 5; i++ {
			cx := x + w/7 + i*w/6
			r := max32(3, h/11)
			if i == 2 {
				r *= 2
			}
			strokeCircle(renderer, cx, cy, r, ink)
			if i == 2 {
				strokeCircle(renderer, cx, cy, r+3, accent)
			}
		}
		for i := int32(0); i < 3; i++ {
			_ = renderer.SetDrawColor(accent.R, accent.G, accent.B, uint8(75-i*18))
			_ = renderer.DrawLine(x+w/10, y+h*68/100+i*h/11, x+w*9/10, y+h*75/100+i*h/13)
		}
	}
	return true
}
