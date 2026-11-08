package main

// homesearch_render.go — drawing the Home search sheet.
//
// One glass sheet over the dimmed screen: the search field on top (a pill like
// the one on Home, with the glyph, the query or its placeholder, a caret and a
// clear button), the results under it in groups, and the footer band with the
// controller hints. While the on-screen keyboard is up the sheet shrinks to the
// space above it: the field and the best few matches as compact rows, so the
// results visibly follow every key.
//
// Nothing here allocates per frame: rows are drawn straight from gsResults,
// labels go through the shared text caches, and the click targets reuse one
// slice.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// Sheet metrics in 1280x720 canvas units.
const (
	homeSheetMaxW    int32 = 980
	homeSheetMargin  int32 = 32
	homeSheetPad     int32 = 20
	homeFieldH       int32 = 52
	homeFieldHKeys   int32 = 44 // the field while the keyboard is up
	homeRowH         int32 = 58
	homeRowHCompact  int32 = 44
	homeRowGap       int32 = 6
	homeGroupHeaderH int32 = 30
	homeSheetFooterH int32 = 48
)

// homeSearchFonts are the sheet's faces, all Inter through the shared cache.
type homeSearchFonts struct {
	field, title, meta, header *ttf.Font
}

func loadHomeSearchFonts(config *Config) (homeSearchFonts, bool) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		config = &Config{}
	}
	f := homeSearchFonts{
		field:  getFontSized(config, "small", 21),
		title:  getFontSized(config, "small", 18),
		meta:   getFontSized(config, "small", 14),
		header: getFontSized(config, "small", 13),
	}
	return f, f.field != nil && f.title != nil && f.meta != nil && f.header != nil
}

// homeSearchSheetRect is the sheet's frame on this canvas.
func homeSearchSheetRect() sdl.Rect {
	margin := scaleX(homeSheetMargin, screenWidth)
	w := screenWidth - 2*margin
	if maxW := scaleX(homeSheetMaxW, screenWidth); w > maxW {
		w = maxW
	}
	top := scaleY(24, screenHeight)
	return sdl.Rect{X: (screenWidth - w) / 2, Y: top, W: w, H: screenHeight - 2*top}
}

// renderGlobalSearch draws the sheet when it is open.
func renderGlobalSearch(renderer *sdl.Renderer, config *Config) {
	if !globalSearchOpen || renderer == nil {
		return
	}
	homeSearchRefreshIfIndexed()
	fonts, ok := loadHomeSearchFonts(config)
	homeSearch.hits = homeSearch.hits[:0]
	drawOverlayScrim(renderer)
	if !ok {
		return
	}

	keyboard := homeSearchKeyboardUp()
	sheet := homeSearchSheetRect()
	if keyboard && vkPanelRect.H > 0 {
		// The keyboard owns the bottom of the screen; the sheet keeps the part
		// above it, with a little air between the two.
		if bottom := vkPanelRect.Y - scaleY(10, screenHeight); bottom < sheet.Y+sheet.H {
			sheet.H = bottom - sheet.Y
		}
	}
	homeSearch.sheet = sheet
	minH := scaleY(homeFieldHKeys, screenHeight) + 2*scaleY(homeSheetPad, screenHeight)
	if sheet.H < minH {
		sheet.H = minH
	}
	drawGlassSheet(renderer, sheet, glassSheetFill())

	pad := scaleX(homeSheetPad, screenWidth)
	fieldH := scaleY(homeFieldH, screenHeight)
	if keyboard {
		fieldH = scaleY(homeFieldHKeys, screenHeight)
	}
	field := sdl.Rect{X: sheet.X + pad, Y: sheet.Y + scaleY(homeSheetPad, screenHeight), W: sheet.W - 2*pad, H: fieldH}
	homeSearchDrawField(renderer, fonts, field, keyboard || gsCursor < 0)

	footerH := int32(0)
	if !keyboard {
		footerH = scaleY(homeSheetFooterH, screenHeight)
		homeSearchDrawFooter(renderer, fonts, sdl.Rect{X: sheet.X, Y: sheet.Y + sheet.H - footerH, W: sheet.W, H: footerH})
	}
	list := sdl.Rect{
		X: sheet.X + pad,
		Y: field.Y + field.H + scaleY(12, screenHeight),
		W: sheet.W - 2*pad,
	}
	list.H = sheet.Y + sheet.H - footerH - scaleY(8, screenHeight) - list.Y
	if list.H <= 0 {
		return
	}
	if keyboard {
		homeSearchDrawCompact(renderer, fonts, list)
		return
	}
	homeSearchDrawList(renderer, fonts, list)
}

// homeSearchDrawField paints the search pill: glyph, query (or placeholder),
// caret and the clear button.
func homeSearchDrawField(renderer *sdl.Renderer, fonts homeSearchFonts, field sdl.Rect, focused bool) {
	homeSearch.field = field
	homeSearch.clear = sdl.Rect{}
	radius := field.H / 2
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, field.X, field.Y, field.W, field.H, radius, glassA(22))
	if focused {
		strokeRoundedRect(renderer, field.X, field.Y, field.W, field.H, radius, glassFocusRingWidth, glassAccent())
	} else {
		strokeRoundedRect(renderer, field.X, field.Y, field.W, field.H, radius, 1, glassEdgeHover())
	}
	glyph := field.H * 9 / 20
	icon := sdl.Rect{X: field.X + field.H/2 - glyph/4, Y: field.Y + (field.H-glyph)/2, W: glyph, H: glyph}
	drawHomeGlyphTinted(renderer, "search", icon, glassAccent())

	textX := icon.X + icon.W + scaleX(12, screenWidth)
	right := field.X + field.W - field.H/2
	if gsQuery != "" {
		// The clear button: a round glass chip with the close glyph.
		d := field.H - scaleY(16, screenHeight)
		clear := sdl.Rect{X: field.X + field.W - scaleX(10, screenWidth) - d, Y: field.Y + (field.H-d)/2, W: d, H: d}
		fillCircle(renderer, clear.X+d/2, clear.Y+d/2, d/2, glassA(26))
		g := d * 11 / 20
		DrawHomeIcon(renderer, "close", sdl.Rect{X: clear.X + (d-g)/2, Y: clear.Y + (d-g)/2, W: g, H: g}, false)
		homeSearch.clear = growTouchTarget(clear)
		right = clear.X - scaleX(8, screenWidth)
	}
	maxW := right - textX
	if maxW < 16 {
		return
	}
	textY := field.Y + (field.H-int32(fonts.field.Height()))/2
	if gsQuery == "" {
		hint := homeSearchHintFor(fonts.field, "", maxW)
		drawText(renderer, fonts.field, hint, textX, textY, ColorTextMuted(), textAlignLeft)
		if focused && homeSearchCaretOn() {
			setDrawColor(renderer, glassAccent())
			_ = renderer.FillRect(&sdl.Rect{X: textX - 2, Y: textY + 3, W: 2, H: int32(fonts.field.Height()) - 6})
		}
		return
	}
	// A long query shows its end, where the typing happens.
	shown, w := homeSearchFitTail(fonts.field, gsQuery, maxW)
	drawText(renderer, fonts.field, shown, textX, textY, ColorTextPrimary(), textAlignLeft)
	if focused && homeSearchCaretOn() {
		setDrawColor(renderer, glassAccent())
		_ = renderer.FillRect(&sdl.Rect{X: textX + w + 1, Y: textY + 3, W: 2, H: int32(fonts.field.Height()) - 6})
	}
}

// homeSearchCaretOn blinks the caret about twice a second.
func homeSearchCaretOn() bool {
	return (sdl.GetTicks64()/530)%2 == 0
}

// homeSearchFitTail returns the end of text that fits maxW, led by an
// ellipsis when the start had to go, and its width: the field shows where the
// typing happens, not the first words of a long query.
func homeSearchFitTail(font *ttf.Font, text string, maxW int32) (string, int32) {
	if font == nil || maxW <= 0 {
		return "", 0
	}
	if w, _ := measureText(font, text); w <= maxW {
		return text, w
	}
	runes := []rune(text)
	for i := 1; i < len(runes); i++ {
		candidate := "…" + string(runes[i:])
		if w, _ := measureText(font, candidate); w <= maxW {
			return candidate, w
		}
	}
	return "", 0
}

// homeSearchRowLayout walks the display rows (group headers and results) and
// calls visit with each row's rect in list space (before scrolling). It is the
// one source of row geometry for drawing, scrolling and clicks.
func homeSearchRowLayout(width int32, visit func(index int, header bool, rect sdl.Rect)) int32 {
	rowH := scaleY(homeRowH, screenHeight)
	gap := scaleY(homeRowGap, screenHeight)
	headerH := scaleY(homeGroupHeaderH, screenHeight)
	y := int32(0)
	cat := ""
	for i, r := range gsResults {
		if r.Category != cat {
			cat = r.Category
			if i > 0 {
				y += gap
			}
			visit(i, true, sdl.Rect{X: 0, Y: y, W: width, H: headerH})
			y += headerH
		}
		visit(i, false, sdl.Rect{X: 0, Y: y, W: width, H: rowH})
		y += rowH + gap
	}
	return y
}

// homeSearchDrawList draws the grouped results, scrolled so the highlighted
// row is always on screen.
func homeSearchDrawList(renderer *sdl.Renderer, fonts homeSearchFonts, list sdl.Rect) {
	// Scroll: keep the focused row (and its group header when it is the first
	// row of its group) inside the viewport.
	var focusTop, focusBottom int32 = -1, -1
	total := homeSearchRowLayout(list.W, func(index int, header bool, rect sdl.Rect) {
		if index != gsCursor {
			return
		}
		if header {
			focusTop = rect.Y
			return
		}
		if focusTop < 0 {
			focusTop = rect.Y
		}
		focusBottom = rect.Y + rect.H
	})
	scroll := int32(homeSearch.scroll)
	if gsCursor < 0 {
		scroll = 0
	} else if focusBottom >= 0 {
		if focusTop < scroll {
			scroll = focusTop
		}
		if focusBottom > scroll+list.H {
			scroll = focusBottom - list.H
		}
	}
	if maxScroll := total - list.H; scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	homeSearch.scroll = int(scroll)

	hovered := -1
	if pointInRect(mouseX, mouseY, list) {
		homeSearchRowLayout(list.W, func(index int, header bool, rect sdl.Rect) {
			if header {
				return
			}
			r := sdl.Rect{X: list.X, Y: list.Y + rect.Y - scroll, W: rect.W, H: rect.H}
			if pointInRect(mouseX, mouseY, r) {
				hovered = index
			}
		})
	}

	renderWithClip(renderer, list.X-4, list.Y, list.W+8, list.H, func(r *sdl.Renderer) {
		homeSearchRowLayout(list.W, func(index int, header bool, rect sdl.Rect) {
			y := list.Y + rect.Y - scroll
			if y+rect.H < list.Y || y > list.Y+list.H {
				return
			}
			row := sdl.Rect{X: list.X, Y: y, W: rect.W, H: rect.H}
			if header {
				homeSearchDrawHeader(r, fonts, row, gsResults[index].Category)
				return
			}
			homeSearchDrawRow(r, fonts, row, gsResults[index], index == gsCursor, index == hovered, false)
			// Only the visible part of a row is a click target.
			hit := glassIntersect(row, list)
			if hit.W > 0 && hit.H > 0 {
				homeSearch.hits = append(homeSearch.hits, homeSearchHit{index: index, rect: hit})
			}
		})
	})
	if total > list.H {
		thumb := float64(list.H) / float64(total)
		frac := 0.0
		if maxScroll := total - list.H; maxScroll > 0 {
			frac = float64(scroll) / float64(maxScroll)
		}
		drawScrollbar(renderer, list.X+list.W+scaleX(6, screenWidth), list.Y, 4, list.H, thumb, frac)
	}
}

// homeSearchDrawCompact lists the best matches as single-line rows while the
// keyboard is up; the group shows as a tag at the row's right end.
func homeSearchDrawCompact(renderer *sdl.Renderer, fonts homeSearchFonts, list sdl.Rect) {
	rowH := scaleY(homeRowHCompact, screenHeight)
	gap := scaleY(4, screenHeight)
	if len(gsResults) == 0 {
		return
	}
	y := list.Y
	for i := range gsResults {
		if y+rowH > list.Y+list.H {
			break
		}
		row := sdl.Rect{X: list.X, Y: y, W: list.W, H: rowH}
		homeSearchDrawRow(renderer, fonts, row, gsResults[i], i == gsCursor && !homeSearchKeyboardUp(), false, true)
		homeSearch.hits = append(homeSearch.hits, homeSearchHit{index: i, rect: row})
		y += rowH + gap
	}
}

// homeSearchDrawHeader paints a group title in the small caps style the
// Start menu uses, with a hairline running to the right edge.
func homeSearchDrawHeader(renderer *sdl.Renderer, fonts homeSearchFonts, rect sdl.Rect, title string) {
	label := strings.ToUpper(title)
	y := rect.Y + rect.H - int32(fonts.header.Height()) - scaleY(4, screenHeight)
	drawText(renderer, fonts.header, label, rect.X+scaleX(4, screenWidth), y, glassAccent(), textAlignLeft)
	lw, lh := measureText(fonts.header, label)
	lineX := rect.X + scaleX(4, screenWidth) + lw + scaleX(12, screenWidth)
	if lineX < rect.X+rect.W {
		setDrawColor(renderer, glassHairline())
		_ = renderer.DrawLine(lineX, y+lh/2, rect.X+rect.W, y+lh/2)
	}
}

// homeSearchDrawRow paints one result: Home's glass row (ring when focused),
// the glyph in its badge, title over subtitle, and the tag at the right end.
// compact rows are one line with the group as the tag.
func homeSearchDrawRow(renderer *sdl.Renderer, fonts homeSearchFonts, rect sdl.Rect, r gsResult, focused, hovered, compact bool) {
	if focused {
		drawGlassFocusGlow(renderer, rect)
	}
	drawGlassSurface(renderer, rect, glassRowRadius, glassState{Focused: focused, Hovered: hovered, NoLift: true})

	inset := scaleX(10, screenWidth)
	badgeD := rect.H - 2*scaleY(9, screenHeight)
	if compact {
		badgeD = rect.H - 2*scaleY(7, screenHeight)
	}
	badge := sdl.Rect{X: rect.X + inset, Y: rect.Y + (rect.H-badgeD)/2, W: badgeD, H: badgeD}
	glyph := drawGlassBadge(renderer, badge)
	ink := ColorTextPrimary()
	sub := ColorTextMuted()
	if r.Dimmed {
		ink = ColorTextTertiary()
		sub = WithAlpha(ColorTextTertiary(), 200)
	}
	if r.Icon == "juka" || r.Badge == "Juka" {
		drawHomeGlyphTinted(renderer, r.Icon, glyph, glassAccent())
	} else if r.Dimmed {
		drawHomeGlyphTinted(renderer, r.Icon, glyph, ColorTextTertiary())
	} else {
		DrawHomeIcon(renderer, r.Icon, glyph, false)
	}

	// The tag at the right end: the result's badge, or on a compact row the
	// group it belongs to.
	tag := r.Badge
	if compact && tag == "" {
		tag = r.Category
	}
	right := rect.X + rect.W - inset
	if tag != "" {
		tw, _ := measureText(fonts.meta, tag)
		chipH := int32(fonts.meta.Height()) + scaleY(6, screenHeight)
		chipW := tw + scaleX(16, screenWidth)
		chip := sdl.Rect{X: right - chipW, Y: rect.Y + (rect.H-chipH)/2, W: chipW, H: chipH}
		fill, edge, text := glassChipFill(), glassChipEdge(), ColorTextMuted()
		if tag == "Juka" {
			fill, edge, text = WithAlpha(glassAccent(), 46), WithAlpha(glassAccent(), 150), glassAccent()
		}
		fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, fill)
		strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, 1, edge)
		drawText(renderer, fonts.meta, tag, chip.X+chip.W/2, chip.Y+(chip.H-int32(fonts.meta.Height()))/2, text, textAlignCenter)
		right = chip.X - scaleX(10, screenWidth)
	}

	textX := badge.X + badge.W + scaleX(12, screenWidth)
	textW := right - textX
	if textW < 24 {
		return
	}
	title := ellipsize(fonts.title, r.Title, textW)
	if compact || strings.TrimSpace(r.Subtitle) == "" {
		drawText(renderer, fonts.title, title, textX, rect.Y+(rect.H-int32(fonts.title.Height()))/2, ink, textAlignLeft)
		return
	}
	titleH := int32(fonts.title.Height())
	metaH := int32(fonts.meta.Height())
	top := rect.Y + (rect.H-titleH-metaH)/2
	drawText(renderer, fonts.title, title, textX, top, ink, textAlignLeft)
	drawText(renderer, fonts.meta, ellipsize(fonts.meta, r.Subtitle, textW), textX, top+titleH, sub, textAlignLeft)
}

// homeSearchDrawFooter paints the hint band along the sheet's bottom edge.
func homeSearchDrawFooter(renderer *sdl.Renderer, fonts homeSearchFonts, band sdl.Rect) {
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, glassHairline())
	_ = renderer.DrawLine(band.X+OverlayCardRadius, band.Y, band.X+band.W-OverlayCardRadius, band.Y)
	font := fonts.meta
	badgeH := int32(font.Height()) + scaleY(6, screenHeight)
	gap := scaleX(16, screenWidth)
	textY := band.Y + (band.H-int32(font.Height()))/2
	badgeY := band.Y + (band.H-badgeH)/2
	hints := homeSearchHintsResults[:]
	if gsCursor < 0 {
		hints = homeSearchHintsField[:]
	}
	// Right-aligned like Home's footer: measure, then draw from the left.
	total := int32(0)
	for i, h := range hints {
		bw, _ := measureText(font, h.Button)
		lw, _ := measureText(font, h.Label)
		total += max32(badgeH, bw+scaleX(12, screenWidth)) + scaleX(6, screenWidth) + lw
		if i > 0 {
			total += gap
		}
	}
	x := band.X + band.W - scaleX(homeSheetPad, screenWidth) - total
	if minX := band.X + scaleX(homeSheetPad, screenWidth); x < minX {
		x = minX
	}
	for _, h := range hints {
		bw, _ := measureText(font, h.Button)
		badge := sdl.Rect{X: x, Y: badgeY, W: max32(badgeH, bw+scaleX(12, screenWidth)), H: badgeH}
		end := drawGlassHintBadge(renderer, nil, font, badge, h.Button, h.Label, badge.X+badge.W+scaleX(6, screenWidth), textY)
		x = end + gap
	}
}

// The sheet's footer hints: on a result, and on the field.
var (
	homeSearchHintsResults = [...]FooterHint{
		{Button: "A", Label: "Open"},
		{Button: "X", Label: "Edit search"},
		{Button: "L1/R1", Label: "Groups"},
		{Button: "B", Label: "Close"},
	}
	homeSearchHintsField = [...]FooterHint{
		{Button: "A", Label: "Type"},
		{Button: "Down", Label: "Results"},
		{Button: "B", Label: "Close"},
	}
)
