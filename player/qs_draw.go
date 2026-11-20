package main

// qs_draw.go — drawing Quick Settings in its three layouts (qs_layout.go) and
// Edit's strip and badges (qs_edit.go).
//
// The card is the shared overlay sheet, risen and faded in as before. Under
// the title (or Edit's strip) sits, on Compact and while editing, a caption
// naming the focused button; then the buttons, clipped to the card and
// scrolled so the focused one is in view; then the battery and connection
// line and the button hints.
//
// A grid tile is Home's anatomy in small: a glass card, the icon badge in the
// top-left corner and the name at the bottom. A switch that is on washes its
// tile in the switch's own colour and fills its badge with it; a slider
// draws its level as a bar; a mode row shows its value, in the accent while
// a named mode is in force. A wide tile puts the badge on the left and two
// lines beside it. A Compact button is the badge alone, centred.

import (
	"math"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	qsPadX    = int32(16)
	qsHeaderH = int32(56)
	qsFooterH = int32(66)
	qsItemGap = int32(4)
	// qsPanelWide is the classic list's width; the controller legend's card
	// is sized from it too (controlslegend.go).
	qsPanelWide = int32(400)

	qsGridPadX     = int32(18)
	qsTileGap      = int32(10)
	qsCompactGap   = int32(8)
	qsTileCellW    = int32(132)
	qsTileCellH    = int32(84)
	qsCompactCellW = int32(76)
	qsCompactCellH = int32(64)
	qsCaptionH     = int32(26)
	qsBodyPad      = int32(8)
	// qsEditMinWide keeps the strip's five chips on one row.
	qsEditMinWide = int32(560)
)

// qsFonts are the faces the panel draws with.
type qsFonts struct {
	row    *ttf.Font // list rows, the info line and the hints
	label  *ttf.Font // a tile's name
	detail *ttf.Font // a tile's state, the caption, the chips
	title  *ttf.Font
}

// qsGeom is the panel's layout for one frame.
type qsGeom struct {
	panel sdl.Rect
	// caption is the height of the line under the title; 0 when there is
	// none.
	caption int32
	// view is where the buttons scroll (content coordinates start at its
	// top); clip is the view grown by the body's padding, so a focused
	// tile's glow is not cut flush.
	view, clip        sdl.Rect
	gridX             int32
	cellW, cellH, gap int32
	itemH             int32
	contentH          int32
}

// qsLook is what one button shows this frame.
type qsLook struct {
	label string
	state string
	// on: a switch that is on, or a mode row away from its default.
	on    bool
	tint  sdl.Color
	avail bool
	glyph string
	// frac is a slider's level, 0..1; -1 on other buttons.
	frac float64
}

// qsCycleValue is a mode row's value and whether it is away from its
// default (tinted like a switch that is on).
func qsCycleValue(config *Config, row int) (string, bool) {
	if row < 0 || row >= len(qs.cycles) {
		return "", false
	}
	c := &qs.cycles[row]
	switch row {
	case qsCycleRowLED:
		return ledPolicyLabel(ledPolicyOrder[c.Value].key), false
	case qsCycleRowXPack:
		return xpackQSLabel(), false
	case qsCycleRowPowerMode:
		return powerModeLabel(powerModeOrder[c.Value]), c.Value != powerModeIndex(PowerModeDefault)
	case qsCycleRowSpatialMode:
		return spatialModeValue(config), spatialSettingsFor(config).Mode != "off"
	case qsCycleRowAutoOff:
		d := sleepPowerOffDelay(config)
		return sleepPowerOffLabel(d), d > 0
	}
	return screenTimeoutLabel(c.Value), false
}

// qsLookOf reads a button's state.
func qsLookOf(config *Config, s qsSlot) qsLook {
	l := qsLook{glyph: s.Glyph, avail: true, frac: -1, tint: ColorAccent}
	switch s.Kind {
	case qsKindToggle:
		t := &qs.toggles[s.Idx]
		l.label, l.avail, l.tint = t.Label, t.Available, t.OnColor
		l.on = t.Value && t.Available
		switch {
		case !t.Available:
			l.state = "Unavailable"
		case t.Value:
			l.state = "On"
		default:
			l.state = "Off"
		}
		if !l.on {
			switch s.Glyph {
			case "wifi":
				l.glyph = "wifi-off"
			case "bluetooth":
				l.glyph = "bluetooth-off"
			}
		}
	case qsKindSlider:
		sl := &qs.sliders[s.Idx]
		l.label, l.avail = sl.Label, sl.Available
		frac := 0.0
		if sl.Max > sl.Min {
			frac = (sl.Value - sl.Min) / (sl.Max - sl.Min)
		}
		l.frac = math.Max(0, math.Min(1, frac))
		pct := int(math.Round(sl.Value * 100))
		l.state = strconv.Itoa(pct) + "%"
		if !sl.Available {
			l.state = "Unavailable"
		}
		switch {
		case sl.Action == "volume" && pct <= 0:
			l.glyph = "volume-mute"
		case sl.Action == "volume" && pct < 50:
			l.glyph = "volume-low"
		case sl.Action == "brightness" && pct < 50:
			l.glyph = "sun-low"
		}
	case qsKindLink:
		ln := &qs.links[s.Idx]
		l.label, l.avail = ln.Label, ln.Available
		if !ln.Available {
			l.state = ln.Detail
		} else if ln.Action == qsLinkStopwatch {
			// The clock's time, lit while it runs (stopwatch.go).
			l.state, l.on = stopwatchQSLine()
		}
	case qsKindCycle:
		l.label = qs.cycles[s.Idx].Label
		l.state, l.on = qsCycleValue(config, s.Idx)
	}
	if l.tint.A == 0 {
		l.tint = ColorAccent
	}
	return l
}

// ── The frame ─────────────────────────────────────────────────────────────

func renderQuickSettings(renderer *sdl.Renderer, config *Config) {
	if !qs.open {
		return
	}
	// Pick up a finished radio/level probe before drawing this frame, and the
	// brightness controller's level (its poll, the firmware's keys).
	qsDrainProbe()
	qsSyncBrightnessFromController()
	qsLayoutSync(config)

	row, _ := getCachedFont(config, "small")
	title, _ := getCachedFont(config, "medium")
	if row == nil {
		return
	}
	f := qsFonts{row: row, title: title, label: FontForRole(config, TypeBody), detail: FontForRole(config, TypeLabel)}
	if f.label == nil {
		f.label = row
	}
	if f.detail == nil {
		f.detail = f.label
	}

	// Entrance keyframes: the scrim fades and the panel rises into place as a
	// single layer, so the whole overlay settles instead of popping.
	progress := 1.0
	if uiMotionEnabled(config) {
		progress = qs.enter.progress(uiClockMS())
	} else {
		qs.enter.cancel()
	}
	fade := uiFadeIn.at(progress)
	slide := int32(math.Round(uiPanelRise.at(progress)))
	drawOverlayScrimFade(renderer, uiScrimProgress(progress, uiPanelEnterMS))

	g := qsMeasure(f)
	qsLay.panel, qsLay.list = g.panel, g.view
	if !qsEd.open || !qsEd.strip {
		qsScrollToFocus(g)
	}

	compositeUILayerIn(renderer, fade, slide, g.panel, func() {
		// The shared overlay sheet: Home's glass, made opaque.
		drawOverlayCard(renderer, g.panel)
		qsLay.editChip = sdl.Rect{}
		if qsEd.open {
			qsDrawStrip(renderer, f, g)
		} else {
			if f.title != nil {
				const head = "Quick Settings"
				tw, _ := measureText(f.title, head)
				renderText(renderer, config, f.title, head, ColorTextPrimary(), g.panel.X+(g.panel.W-tw)/2, g.panel.Y+16)
			}
			// A pencil in the corner: Edit for a mouse, which has no L2.
			d := qsHeaderH - 24
			qsLay.editChip = sdl.Rect{X: g.panel.X + g.panel.W - qsGridPadX - d, Y: g.panel.Y + 12, W: d, H: d}
			qsDrawChip(renderer, f.detail, qsLay.editChip, "", "edit", false, false)
		}
		if g.caption > 0 {
			qsDrawCaption(renderer, config, f, g)
		}
		qsDrawBody(renderer, config, f, g)
		qsDrawFooter(renderer, config, f, g)
	})
}

// qsMeasure lays the card out for this frame.
func qsMeasure(f qsFonts) qsGeom {
	var g qsGeom
	cols := int32(qsLay.cols)
	maxW, maxH := screenWidth-24, screenHeight-24
	switch qsLay.layout {
	case qsLayoutTiles:
		g.cellW, g.cellH, g.gap = qsTileCellW, qsTileCellH, qsTileGap
	case qsLayoutCompact:
		g.cellW, g.cellH, g.gap = qsCompactCellW, qsCompactCellH, qsCompactGap
	}
	if screenHeight < 600 && cols > 0 {
		g.cellH = g.cellH * 6 / 7
	}
	w := qsPanelWide + 20
	if cols > 0 {
		w = cols*g.cellW + (cols-1)*g.gap + 2*qsGridPadX
	}
	if qsEd.open {
		w = max32(w, max32(qsEditMinWide, qsStripWidth(f.detail)+2*qsGridPadX))
	}
	if w > maxW {
		w = maxW
	}
	gridW := int32(0)
	if cols > 0 {
		// A card narrower than the grid shrinks the cells; a wider one (the
		// strip's minimum) centres the grid.
		if room := w - 2*qsGridPadX - (cols-1)*g.gap; cols*g.cellW > room {
			g.cellW = max32(24, room/cols)
		}
		gridW = cols*g.cellW + (cols-1)*g.gap
	}
	if qsLay.layout == qsLayoutCompact || qsEd.open {
		g.caption = qsCaptionH
	}
	chrome := qsHeaderH + g.caption + 2*qsBodyPad + qsFooterH
	switch {
	case len(qsLay.slots) == 0:
		g.contentH = 3 * int32(f.label.Height())
	case cols > 0:
		rows := int32(max(qsLay.rows, 1))
		g.contentH = rows*g.cellH + (rows-1)*g.gap
	default:
		n := int32(len(qsLay.slots))
		// The rows shrink before the card would run off a 480-line screen,
		// then the list scrolls.
		g.itemH = 44
		for chrome+n*(g.itemH+qsItemGap)-qsItemGap > maxH && g.itemH > 30 {
			g.itemH--
		}
		g.contentH = n*(g.itemH+qsItemGap) - qsItemGap
	}
	h := chrome + g.contentH
	if h > maxH {
		h = maxH
	}
	g.panel = sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	viewH := max32(0, h-chrome)
	g.view = sdl.Rect{X: g.panel.X, Y: g.panel.Y + qsHeaderH + g.caption + qsBodyPad, W: w, H: viewH}
	g.clip = sdl.Rect{X: g.panel.X, Y: g.view.Y - qsBodyPad, W: w, H: viewH + 2*qsBodyPad}
	g.gridX = g.panel.X + (w-gridW)/2
	return g
}

// qsFocusSpan is the focused button's top and bottom in content coordinates.
func qsFocusSpan(g qsGeom) (int32, int32, bool) {
	i := qs.cursor
	if i < 0 || i >= len(qsLay.slots) {
		return 0, 0, false
	}
	if qsLay.cols > 0 {
		if i >= len(qsLay.cells) {
			return 0, 0, false
		}
		c := qsLay.cells[i]
		top := int32(c.Row) * (g.cellH + g.gap)
		return top, top + int32(c.H)*g.cellH + int32(c.H-1)*g.gap, true
	}
	top := int32(i) * (g.itemH + qsItemGap)
	return top, top + g.itemH, true
}

// qsScrollToFocus keeps the focused button in view: the focus is the only
// thing that moves the list.
func qsScrollToFocus(g qsGeom) {
	if top, bottom, ok := qsFocusSpan(g); ok {
		if top < qs.scroll {
			qs.scroll = top
		}
		if bottom > qs.scroll+g.view.H {
			qs.scroll = bottom - g.view.H
		}
	}
	if maxScroll := max32(0, g.contentH-g.view.H); qs.scroll > maxScroll {
		qs.scroll = maxScroll
	}
	if qs.scroll < 0 {
		qs.scroll = 0
	}
}

// qsSlotRect is where slot i is drawn this frame.
func qsSlotRect(g qsGeom, i int) sdl.Rect {
	if qsLay.cols > 0 && i < len(qsLay.cells) {
		c := qsLay.cells[i]
		return sdl.Rect{
			X: g.gridX + int32(c.Col)*(g.cellW+g.gap),
			Y: g.view.Y - qs.scroll + int32(c.Row)*(g.cellH+g.gap),
			W: int32(c.W)*g.cellW + int32(c.W-1)*g.gap,
			H: int32(c.H)*g.cellH + int32(c.H-1)*g.gap,
		}
	}
	return sdl.Rect{
		X: g.panel.X + qsPadX,
		Y: g.view.Y - qs.scroll + int32(i)*(g.itemH+qsItemGap),
		W: g.panel.W - 2*qsPadX,
		H: g.itemH,
	}
}

// qsDrawBody draws the buttons, clipped to the body, and records where each
// one is for the pointer.
func qsDrawBody(renderer *sdl.Renderer, config *Config, f qsFonts, g qsGeom) {
	n := len(qsLay.slots)
	qsLay.rects = qsLay.rects[:0]
	qsLay.bars = qsLay.bars[:0]
	qsEd.hideBadge, qsEd.sizeChip = sdl.Rect{}, sdl.Rect{}
	if n == 0 {
		qsDrawEmpty(renderer, config, f, g)
		return
	}
	prevClip := renderer.GetClipRect()
	if g.clip.H > 0 {
		_ = renderer.SetClipRect(&g.clip)
	}
	for i := 0; i < n; i++ {
		r := qsSlotRect(g, i)
		qsLay.rects = append(qsLay.rects, r)
		qsLay.bars = append(qsLay.bars, sdl.Rect{})
		if r.Y+r.H < g.clip.Y || r.Y > g.clip.Y+g.clip.H {
			continue
		}
		s := qsLay.slots[i]
		l := qsLookOf(config, s)
		focused := i == qs.cursor && !(qsEd.open && qsEd.strip)
		switch {
		case qsLay.cols == 0:
			qsDrawListRow(renderer, config, f, s, l, r, focused, i)
		case qsLay.layout == qsLayoutCompact:
			qsDrawCompactTile(renderer, config, f, s, l, r, g, focused, i)
		default:
			qsDrawTile(renderer, config, f, s, l, r, g, focused, i)
		}
		if qsEd.open {
			qsDrawEditMarks(renderer, f, s, r, g, focused)
		}
	}
	if prevClip.W > 0 && prevClip.H > 0 {
		_ = renderer.SetClipRect(&prevClip)
	} else {
		_ = renderer.SetClipRect(nil)
	}
}

// qsDrawEmpty is the body with every button hidden.
func qsDrawEmpty(renderer *sdl.Renderer, config *Config, f qsFonts, g qsGeom) {
	cx := g.panel.X + g.panel.W/2
	head := "Every button is hidden"
	line := hintLine(FooterHint{qsEditKeyName(), "Edit"}) + " " + uiT("shows them again")
	hw, _ := measureText(f.label, head)
	lw, _ := measureText(f.detail, line)
	y := g.view.Y + (g.view.H-int32(f.label.Height())-int32(f.detail.Height())-6)/2
	renderText(renderer, config, f.label, head, ColorTextPrimary(), cx-hw/2, y)
	renderText(renderer, config, f.detail, line, ColorTextMuted(), cx-lw/2, y+int32(f.label.Height())+6)
}

// ── Tiles ─────────────────────────────────────────────────────────────────

// qsTileSurface paints a tile's card: the focus bloom, the switch colour's
// wash while it is on, and the glass with Home's ring when focused.
func qsTileSurface(renderer *sdl.Renderer, l qsLook, r sdl.Rect, radius int32, focused bool) {
	if focused {
		drawGlassFocusGlow(renderer, r)
	}
	if l.on {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, WithAlpha(l.tint, 52))
	}
	// A focused button that does nothing here still wears the ring: the
	// cursor must never vanish.
	drawGlassSurface(renderer, r, radius, glassState{Focused: focused, NoLift: true, Disabled: !l.avail && !focused})
}

// qsDrawBadge paints the icon badge: filled in the switch's colour while it
// is on, Home's glass badge otherwise.
func qsDrawBadge(renderer *sdl.Renderer, b sdl.Rect, l qsLook) {
	if b.W <= 0 {
		return
	}
	if l.on {
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		fillRoundedRect(renderer, b.X, b.Y, b.W, b.H, min32(homeIconRadius, b.W/4), WithAlpha(l.tint, 235))
		gs := b.W / 2
		drawHomeGlyphTinted(renderer, l.glyph, sdl.Rect{X: b.X + (b.W-gs)/2, Y: b.Y + (b.H-gs)/2, W: gs, H: gs}, contrastText(l.tint))
		return
	}
	glyph := drawGlassBadge(renderer, b)
	ink := ColorTextPrimary()
	if !l.avail {
		ink = ColorTextTertiary()
	}
	drawHomeGlyphTinted(renderer, l.glyph, glyph, ink)
}

// qsInks are a tile's text colours: the name, and the state under it.
func qsInks(l qsLook) (sdl.Color, sdl.Color) {
	if !l.avail {
		return ColorTextTertiary(), WithAlpha(ColorTextTertiary(), 170)
	}
	return ColorTextPrimary(), WithAlpha(ColorTextSecondary(), 220)
}

// qsStateInk is the colour of a state line: the accent for a mode row away
// from its default, the switch's colour for one that is on.
func qsStateInk(l qsLook, sub sdl.Color) sdl.Color {
	if l.on && l.avail {
		return WithAlpha(l.tint, 245)
	}
	return sub
}

// qsDrawBar paints a slider's bar into track and records it for the pointer.
func qsDrawBar(renderer *sdl.Renderer, l qsLook, track sdl.Rect, slot int, adjusting bool) {
	if track.W <= 8 || l.frac < 0 {
		return
	}
	if slot >= 0 && slot < len(qsLay.bars) {
		// The pointer gets a taller target than the line it sees.
		qsLay.bars[slot] = sdl.Rect{X: track.X, Y: track.Y - 12, W: track.W, H: track.H + 24}
	}
	if !l.avail {
		fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, track.H/2, glassChipFill())
		return
	}
	fill := WithAlpha(ColorAccent, 210)
	if adjusting {
		fill = ColorAccent
	}
	UIProgressBar(renderer, track, l.frac, glassChipFill(), fill)
	kx := track.X + int32(math.Round(float64(track.W)*l.frac))
	kr := track.H/2 + 4
	if adjusting {
		kr += 2
		fillCircle(renderer, kx, track.Y+track.H/2, kr+2, WithAlpha(ColorAccent, 120))
	}
	fillCircle(renderer, kx, track.Y+track.H/2, kr, WithAlpha(ColorTextPrimary(), 245))
}

// qsDrawSwitch paints a wide tile's (and a list row's) switch at its right
// edge.
func qsDrawSwitch(renderer *sdl.Renderer, t *qsToggle, right, cy int32) {
	swW, swH := int32(44), int32(24)
	swX, swY := right-swW, cy-swH/2
	fill := t.OffColor
	if t.Value {
		fill = t.OnColor
	}
	fillRoundedRect(renderer, swX, swY, swW, swH, swH/2, fill)
	knobR := int32(8)
	knobX := swX + knobR + 4
	if t.Value {
		knobX = swX + swW - knobR - 4
	}
	fillCircle(renderer, knobX, cy, knobR, sdl.Color{R: 255, G: 255, B: 255, A: 240})
}

// qsDrawChevron paints the "opens a page" arrow with its right edge at right.
func qsDrawChevron(renderer *sdl.Renderer, right, cy, size int32, ink sdl.Color) {
	drawHomeGlyphTinted(renderer, "forward", sdl.Rect{X: right - size, Y: cy - size/2, W: size, H: size}, ink)
}

// qsDrawTile draws one button of the Tiles layout.
func qsDrawTile(renderer *sdl.Renderer, config *Config, f qsFonts, s qsSlot, l qsLook, r sdl.Rect, g qsGeom, focused bool, slot int) {
	radius := min32(18, g.cellH/4)
	qsTileSurface(renderer, l, r, radius, focused)
	ink, sub := qsInks(l)
	pad := max32(9, min32(14, g.cellH/7))
	b := max32(26, min32(44, g.cellH*42/100))
	lh, dh := int32(f.label.Height()), int32(f.detail.Height())
	adjusting := focused && qsLay.adjusting && s.Kind == qsKindSlider
	right := r.X + r.W - pad

	if s.Size.W >= 2 && s.Size.H == 1 {
		// Wide: the badge on the left, the name and the state beside it.
		badge := sdl.Rect{X: r.X + pad, Y: r.Y + (r.H-b)/2, W: b, H: b}
		qsDrawBadge(renderer, badge, l)
		tx := badge.X + b + pad
		cy := r.Y + r.H/2
		y0 := r.Y + (r.H-lh-dh-4)/2
		switch s.Kind {
		case qsKindSlider:
			pctW, _ := measureText(f.label, l.state)
			stateInk := sub
			if adjusting {
				stateInk = ColorAccent
			}
			renderText(renderer, config, f.label, ellipsize(f.label, l.label, right-tx-pctW-8), ink, tx, y0)
			renderText(renderer, config, f.label, l.state, stateInk, right-pctW, y0)
			qsDrawBar(renderer, l, sdl.Rect{X: tx + 2, Y: y0 + lh + 4 + (dh-6)/2, W: right - tx - 8, H: 6}, slot, adjusting)
		case qsKindToggle:
			room := right - tx
			if l.avail {
				qsDrawSwitch(renderer, &qs.toggles[s.Idx], right, cy)
				room -= 52
			}
			renderText(renderer, config, f.label, ellipsize(f.label, l.label, room), ink, tx, y0)
			renderText(renderer, config, f.detail, ellipsize(f.detail, l.state, room), qsStateInk(l, sub), tx, y0+lh+4)
		case qsKindLink:
			room := right - tx - 22
			qsDrawChevron(renderer, right, cy, 18, sub)
			if l.state == "" {
				renderText(renderer, config, f.label, ellipsize(f.label, l.label, room), ink, tx, cy-lh/2)
				break
			}
			renderText(renderer, config, f.label, ellipsize(f.label, l.label, room), ink, tx, y0)
			renderText(renderer, config, f.detail, ellipsize(f.detail, l.state, room), sub, tx, y0+lh+4)
		default:
			renderText(renderer, config, f.label, ellipsize(f.label, l.label, right-tx), ink, tx, y0)
			renderText(renderer, config, f.detail, ellipsize(f.detail, l.state, right-tx), qsStateInk(l, sub), tx, y0+lh+4)
		}
		return
	}

	// Stacked: the badge in the top-left corner, the name at the bottom —
	// on two lines, a size smaller if need be, rather than cut ("Close all
	// apps & games" on one cell). The badge is a little smaller here than on
	// a wide tile, to leave the name its two lines.
	b = max32(24, min32(40, g.cellH*36/100))
	badge := sdl.Rect{X: r.X + pad, Y: r.Y + pad, W: b, H: b}
	qsDrawBadge(renderer, badge, l)
	bottom := r.Y + r.H - pad
	tall := s.Size.H >= 2
	if tall && s.Kind == qsKindSlider {
		// A tall slider: the level big beside the badge, the bar at the
		// bottom under the name.
		barY := bottom - 6
		pw, _ := measureText(f.title, l.state)
		if f.title != nil && r.W-2*pad-b-8 >= pw {
			stateInk := sub
			if adjusting {
				stateInk = ColorAccent
			}
			renderText(renderer, config, f.title, l.state, stateInk, right-pw, badge.Y+(b-int32(f.title.Height()))/2)
		}
		renderText(renderer, config, f.label, ellipsize(f.label, l.label, r.W-2*pad), ink, r.X+pad, barY-8-lh)
		qsDrawBar(renderer, l, sdl.Rect{X: r.X + pad + 2, Y: barY, W: r.W - 2*pad - 4, H: 6}, slot, adjusting)
		return
	}
	switch {
	case tall && l.state != "":
		// Two rows: the state under the name.
		bottom -= dh + 2
		renderText(renderer, config, f.detail, ellipsize(f.detail, l.state, r.W-2*pad), qsStateInk(l, sub), r.X+pad, bottom+2)
	case s.Kind == qsKindLink && l.state == "":
		qsDrawChevron(renderer, right, badge.Y+b/2, 16, sub)
	default:
		// One cell: the state in the top-right corner, beside the badge.
		if room := r.W - 2*pad - b - 6; l.state != "" && room > 18 {
			st := ellipsize(f.detail, l.state, room)
			sw, _ := measureText(f.detail, st)
			renderText(renderer, config, f.detail, st, qsStateInk(l, sub), right-sw, badge.Y+(b-dh)/2)
		}
	}
	// Wrapped in the interface language, then drawn as already translated:
	// a line of a translated name is not a catalogue key.
	top := badge.Y + b + 4
	font, lineH, lines := glassTileLabel(config, uiT(l.label), r.W-2*pad, bottom-top, int(TypeBody.Size()))
	if font == nil || len(lines) == 0 {
		renderText(renderer, config, f.label, ellipsize(f.label, l.label, r.W-2*pad), ink, r.X+pad, bottom-lh)
		return
	}
	y := bottom - int32(len(lines))*lineH
	for i, line := range lines {
		renderText(renderer, config, font, uiMarkTranslated(line), ink, r.X+pad, y+int32(i)*lineH)
	}
}

// qsDrawCompactTile draws one button of the Compact layout: the badge
// alone, centred; a wide button adds its value (a slider its bar) beside it.
func qsDrawCompactTile(renderer *sdl.Renderer, config *Config, f qsFonts, s qsSlot, l qsLook, r sdl.Rect, g qsGeom, focused bool, slot int) {
	radius := min32(14, g.cellH/4)
	qsTileSurface(renderer, l, r, radius, focused)
	ink, sub := qsInks(l)
	adjusting := focused && qsLay.adjusting && s.Kind == qsKindSlider
	b := max32(22, min32(48, min32(g.cellW, g.cellH)*58/100))
	if s.Size.W >= 2 || s.Kind == qsKindSlider {
		pad := max32(8, (g.cellH-b)/2)
		badge := sdl.Rect{X: r.X + pad, Y: r.Y + (r.H-b)/2, W: b, H: b}
		qsDrawBadge(renderer, badge, l)
		tx := badge.X + b + pad
		right := r.X + r.W - pad
		cy := r.Y + r.H/2
		if s.Kind == qsKindSlider {
			qsDrawBar(renderer, l, sdl.Rect{X: tx + 2, Y: cy - 3, W: right - tx - 8, H: 6}, slot, adjusting)
			return
		}
		text, col := l.label, ink
		if s.Kind == qsKindCycle {
			text, col = l.state, qsStateInk(l, sub)
		}
		if room := right - tx; room > 16 {
			renderText(renderer, config, f.detail, ellipsize(f.detail, text, room), col, tx, cy-int32(f.detail.Height())/2)
		}
		return
	}
	badge := sdl.Rect{X: r.X + (r.W-b)/2, Y: r.Y + (g.cellH-b)/2, W: b, H: b}
	if qsEd.open && focused && s.Size.H < 2 {
		// Edit's size tab sits on the bottom edge: the badge moves up, a
		// little smaller, so the tab does not cover it.
		b = min32(b, r.H/2)
		badge = sdl.Rect{X: r.X + (r.W-b)/2, Y: r.Y + 4, W: b, H: b}
	}
	if s.Size.H >= 2 {
		// A tall button names itself under the badge.
		dh := int32(f.detail.Height())
		badge.Y = r.Y + (r.H-b-dh-6)/2
		name := ellipsize(f.detail, l.label, r.W-12)
		nw, _ := measureText(f.detail, name)
		renderText(renderer, config, f.detail, name, ink, r.X+(r.W-nw)/2, badge.Y+b+6)
	}
	qsDrawBadge(renderer, badge, l)
}

// ── The list ──────────────────────────────────────────────────────────────

// qsRowBackground is a list row's surface: only the focused row draws one, a
// selected list row in Home's glass (UIListRow), so the cursor reads the same
// here as in every list.
func qsRowBackground(renderer *sdl.Renderer, r sdl.Rect, focused bool) {
	if focused {
		UIListRow(renderer, r, 12, true, false)
	}
}

// qsLabelWidth is the room left for a row label after the icon column and the
// right-aligned detail (a missing backend, a level or "Open >"). Measuring the
// detail instead of reserving a fixed strip keeps "Airplane Mode" whole while
// still stopping "Bluetooth" from running into its detail.
func qsLabelWidth(font *ttf.Font, rowW int32, detail string) int32 {
	reserve := int32(0)
	if detail != "" {
		if dw, _ := measureText(font, detail); dw > 0 {
			reserve = dw + 24
		}
	}
	return max32(60, rowW-52-12-reserve)
}

// qsDrawListRow draws one row of the List layout: the glyph, the name, and on
// the right the switch, the level and its bar, the mode's value or "Open >".
// A row whose backend is missing is dimmed and says so instead.
func qsDrawListRow(renderer *sdl.Renderer, config *Config, f qsFonts, s qsSlot, l qsLook, r sdl.Rect, focused bool, slot int) {
	qsRowBackground(renderer, r, focused)
	font := f.row
	labelCol := WithAlpha(ColorTextSecondary(), 220)
	iconInk := WithAlpha(ColorTextPrimary(), 220)
	if !l.avail {
		labelCol = WithAlpha(ColorTextTertiary(), 180)
		iconInk = labelCol
	}
	if l.on {
		iconInk = l.tint
	}
	gs := min32(24, r.H-14)
	drawHomeGlyphTinted(renderer, l.glyph, sdl.Rect{X: r.X + 14, Y: r.Y + (r.H-gs)/2, W: gs, H: gs}, iconInk)
	fh := int32(font.Height())
	ty := r.Y + (r.H-fh)/2
	right := r.X + r.W - 12

	detail := ""
	switch {
	case !l.avail:
		detail = "Unavailable"
		if s.Kind == qsKindLink && l.state != "" {
			detail = l.state
		}
	case s.Kind == qsKindLink && l.state != "":
		// A link with a live value (the Stopwatch's clock) shows it instead.
		detail = l.state
	case s.Kind == qsKindLink:
		detail = "Open >"
	case s.Kind == qsKindCycle:
		detail = l.state
	}
	if s.Kind == qsKindSlider && l.avail {
		// The name and the level on the top line, the bar under them.
		ty = r.Y + 4
		renderText(renderer, config, font, ellipsize(font, l.label, qsLabelWidth(font, r.W, l.state)), labelCol, r.X+52, ty)
		vw, _ := measureText(font, l.state)
		renderText(renderer, config, font, l.state, WithAlpha(ColorTextSecondary(), 200), right-vw, ty)
		qsDrawBar(renderer, l, sdl.Rect{X: r.X + 100, Y: r.Y + r.H - 14, W: r.W - 130, H: 6}, slot, false)
		return
	}
	renderText(renderer, config, font, ellipsize(font, l.label, qsLabelWidth(font, r.W, detail)), labelCol, r.X+52, ty)
	if detail != "" {
		dw, _ := measureText(font, detail)
		col := WithAlpha(ColorTextTertiary(), 190)
		if s.Kind == qsKindCycle && l.avail {
			col = qsStateInk(l, WithAlpha(ColorTextSecondary(), 220))
		}
		renderText(renderer, config, font, detail, col, right-dw, ty)
		return
	}
	if s.Kind == qsKindToggle {
		qsDrawSwitch(renderer, &qs.toggles[s.Idx], r.X+r.W-8, r.Y+r.H/2)
	}
}

// ── Edit ──────────────────────────────────────────────────────────────────

// qsDrawEditMarks draws Edit's marks on one button: a hidden button is
// veiled and says so; the focused one is washed in the accent while it is
// carried, and wears the hide badge and, on a grid, the size chip.
func qsDrawEditMarks(renderer *sdl.Renderer, f qsFonts, s qsSlot, r sdl.Rect, g qsGeom, focused bool) {
	radius := int32(12)
	if qsLay.cols > 0 {
		radius = min32(18, g.cellH/4)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if s.Hidden {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, WithAlpha(ColorBackground, 165))
		const word = "Hidden"
		ww, _ := measureText(f.detail, word)
		x := r.X + (r.W-ww)/2
		if qsLay.cols == 0 {
			x = r.X + r.W - ww - 14
		}
		drawText(renderer, f.detail, word, x, r.Y+(r.H-int32(f.detail.Height()))/2, ColorTextMuted(), textAlignLeft)
	}
	if !focused {
		return
	}
	if qsEd.holding {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, WithAlpha(ColorAccent, 60))
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 2, ColorAccent)
	}
	// The pointer's targets only count while the whole button is in view.
	inView := r.Y >= g.clip.Y && r.Y+r.H <= g.clip.Y+g.clip.H

	// The hide badge: red minus to hide, accent plus to show again. On the
	// list it covers the glyph, the one place a row keeps free.
	d := max32(18, min32(26, r.H*30/100))
	badge := sdl.Rect{X: r.X + r.W - d - 6, Y: r.Y + 6, W: d, H: d}
	if qsLay.cols == 0 {
		d = min32(26, r.H-12)
		badge = sdl.Rect{X: r.X + 13, Y: r.Y + (r.H-d)/2, W: d, H: d}
	}
	fill, glyph := WithAlpha(ColorDanger, 235), "minus"
	if s.Hidden {
		fill, glyph = WithAlpha(ColorAccent, 235), "plus"
	}
	cx, cy, cr := badge.X+d/2, badge.Y+d/2, d/2
	fillCircle(renderer, cx, cy, cr+1, sdl.Color{R: 0, G: 0, B: 0, A: 70})
	fillCircle(renderer, cx, cy, cr, fill)
	gs := d * 11 / 20
	drawHomeGlyphTinted(renderer, glyph, sdl.Rect{X: cx - gs/2, Y: cy - gs/2, W: gs, H: gs}, contrastText(fill))
	if inView {
		qsEd.hideBadge = badge
	}

	if qsLay.cols == 0 {
		return
	}
	// The size chip, a tab on the bottom edge: accent while resizing.
	label := homeTileSizeLabel(s.Size)
	lw, _ := measureText(f.detail, label)
	ch := int32(f.detail.Height()) + 6
	chip := sdl.Rect{W: lw + 18, H: ch}
	chip.X = r.X + (r.W-chip.W)/2
	chip.Y = r.Y + r.H - ch - 5
	fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, ch/2, glassSheetFill())
	ink := glassChipInk()
	if qsEd.resizing {
		fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, ch/2, WithAlpha(ColorAccent, 235))
		ink = contrastText(ColorAccent)
	} else {
		fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, ch/2, glassChipFill())
		strokeRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, ch/2, 1, glassChipEdge())
	}
	drawText(renderer, f.detail, label, chip.X+chip.W/2, chip.Y+(ch-int32(f.detail.Height()))/2, ink, textAlignCenter)
	if inView {
		qsEd.sizeChip = chip
	}
}

// qsStripLabels are the strip's chip words, in chip order.
func qsStripLabel(i int) string {
	switch {
	case i >= 0 && i < len(qsLayoutChoices):
		return qsLayoutLabel(qsLayoutChoices[i])
	case i == qsEditChipReset:
		if qsEditResetArmed() {
			return "Press again"
		}
		return "Reset"
	}
	return "Done"
}

// qsStripChipW is one chip's width.
func qsStripChipW(font *ttf.Font, i int) int32 {
	lw, _ := measureText(font, qsStripLabel(i))
	w := lw + 28
	if i == qsEditChipDone {
		w += 22 // the check
	}
	return w
}

// qsStripWidth is the strip's width with its gaps, so the card can make room.
func qsStripWidth(font *ttf.Font) int32 {
	if font == nil {
		return 0
	}
	w := int32(0)
	for i := 0; i < qsEditChipCount; i++ {
		w += qsStripChipW(font, i) + 8
	}
	// "Press again" is the widest Reset gets; and a gap before Reset.
	if !qsEditResetArmed() {
		pw, _ := measureText(font, "Press again")
		rw, _ := measureText(font, "Reset")
		w += pw - rw
	}
	return w + 16
}

// qsDrawStrip draws Edit's strip where the title is: the layouts on the
// left, Reset and Done on the right.
func qsDrawStrip(renderer *sdl.Renderer, f qsFonts, g qsGeom) {
	qsEd.chips = [qsEditChipCount]sdl.Rect{}
	font := f.detail
	y, hh := g.panel.Y+12, qsHeaderH-20
	x := g.panel.X + qsGridPadX
	active := qsLayoutIndex(qsLay.layout)
	for i := range qsLayoutChoices {
		r := sdl.Rect{X: x, Y: y, W: qsStripChipW(font, i), H: hh}
		qsDrawChip(renderer, font, r, qsStripLabel(i), "", i == active, qsEd.strip && qsEd.stripIdx == i)
		qsEd.chips[i] = r
		x += r.W + 8
	}
	right := g.panel.X + g.panel.W - qsGridPadX
	done := sdl.Rect{W: qsStripChipW(font, qsEditChipDone), H: hh, Y: y}
	done.X = right - done.W
	reset := sdl.Rect{W: qsStripChipW(font, qsEditChipReset), H: hh, Y: y}
	reset.X = done.X - 8 - reset.W
	if reset.X >= x {
		qsDrawChip(renderer, font, reset, qsStripLabel(qsEditChipReset), "", false, qsEd.strip && qsEd.stripIdx == qsEditChipReset)
		qsEd.chips[qsEditChipReset] = reset
	}
	if done.X >= x {
		qsDrawChip(renderer, font, done, qsStripLabel(qsEditChipDone), "check", true, qsEd.strip && qsEd.stripIdx == qsEditChipDone)
		qsEd.chips[qsEditChipDone] = done
	}
}

// qsDrawChip is one of the strip's pills (Edit Home's drawEditChip): glass,
// accent-tinted for the layout in force and Done, ringed when focused.
func qsDrawChip(renderer *sdl.Renderer, font *ttf.Font, r sdl.Rect, label, icon string, active, focused bool) {
	if r.W <= 0 || r.H <= 0 || font == nil {
		return
	}
	radius := r.H / 2
	if focused {
		drawGlassFocusGlow(renderer, r)
	}
	fill, edge := homeGlass(16), homeGlass(36)
	if active {
		fill, edge = WithAlpha(ColorAccent, 56), WithAlpha(ColorAccent, 150)
	}
	if focused && !active {
		fill = homeGlass(28)
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, fill)
	if focused {
		drawGlassFocusRing(renderer, r, radius)
	} else {
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 1, edge)
	}
	lw, lh := measureText(font, label)
	glyph, gap := int32(0), int32(0)
	if icon != "" {
		glyph = min32(16, r.H-14)
		if label != "" {
			gap = 6
		}
	}
	x := r.X + (r.W-(glyph+gap+lw))/2
	if glyph > 0 {
		drawHomeGlyphTinted(renderer, icon, sdl.Rect{X: x, Y: r.Y + (r.H-glyph)/2, W: glyph, H: glyph}, ColorTextPrimary())
	}
	if label != "" {
		drawText(renderer, font, label, x+glyph+gap, r.Y+(r.H-lh)/2, ColorTextPrimary(), textAlignLeft)
	}
}

// qsStripNote is the caption for a focused strip chip.
func qsStripNote(i int) string {
	switch {
	case i >= 0 && i < len(qsLayoutChoices):
		switch qsLayoutChoices[i] {
		case qsLayoutCompact:
			return "Small buttons: everything on one screen"
		case qsLayoutList:
			return "One row per button"
		}
		return "A grid of big tiles you can resize"
	case i == qsEditChipReset:
		return "Put the default buttons back (press twice)"
	}
	return "Save and stop editing"
}

// qsDrawCaption draws the line under the title: the focused button and its
// state on Compact, and what the focus is on while editing.
func qsDrawCaption(renderer *sdl.Renderer, config *Config, f qsFonts, g qsGeom) {
	text := ""
	s, ok := qsFocused()
	switch {
	case qsEd.open && qsEd.strip:
		text = qsStripNote(qsEd.stripIdx)
	case !ok:
	case qsEd.open:
		l := qsLookOf(config, s)
		state := l.state
		switch {
		case s.Hidden:
			state = "Hidden"
		case qsLay.cols > 0:
			state = homeTileSizeLabel(s.Size)
		}
		text = l.label
		if qsEd.holding {
			text = uiT("Moving") + " " + l.label
		}
		if state != "" {
			text += " · " + state
		}
	default:
		l := qsLookOf(config, s)
		text = l.label
		if l.state != "" {
			text += " · " + l.state
		}
	}
	if text == "" {
		return
	}
	text = ellipsize(f.detail, text, g.panel.W-2*qsGridPadX)
	tw, _ := measureText(f.detail, text)
	y := g.panel.Y + qsHeaderH + (g.caption-int32(f.detail.Height()))/2 - 4
	renderText(renderer, config, f.detail, text, ColorTextMuted(), g.panel.X+(g.panel.W-tw)/2, y)
}

// ── The footer ────────────────────────────────────────────────────────────

// qsEditKeyName is L2's name in this frame's vocabulary: Q on a keyboard,
// as on Home (homeHintKey).
func qsEditKeyName() string {
	if hintsUseKeyboard() {
		return "Q"
	}
	return "L2"
}

// qsShoulderKeys is L1/R1's name in this frame's vocabulary. On a keyboard
// it is the keys that do the job here — Tab for the layout, PgUp/PgDn for a
// value — which the hint layer cannot guess from the label.
func qsShoulderKeys(layout bool) string {
	switch {
	case !hintsUseKeyboard():
		return "L1/R1"
	case layout:
		return "Tab"
	}
	return "PgUp/PgDn"
}

// qsFooterHints are the keys that do something right now, most important
// first and Close (or Done) last.
func qsFooterHints() []FooterHint {
	if qsEd.open {
		switch {
		case qsEd.resizing:
			return []FooterHint{{"D-Pad", "Resize"}, {"A", "Done"}}
		case qsEd.holding:
			return []FooterHint{{"D-Pad", "Move"}, {"A", "Drop"}}
		case qsEd.strip:
			return []FooterHint{{"A", "Choose"}, {qsShoulderKeys(true), "Layout"}, {"B", "Done"}}
		}
		hints := []FooterHint{{"A", "Move"}}
		if qsLay.cols > 0 {
			hints = append(hints, FooterHint{"X", "Resize"})
		}
		hide := "Hide"
		if s, ok := qsFocused(); ok && s.Hidden {
			hide = "Show"
		}
		return append(hints, FooterHint{"Y", hide}, FooterHint{qsShoulderKeys(true), "Layout"}, FooterHint{"B", "Done"})
	}
	if qsLay.adjusting {
		return []FooterHint{{"Left/Right", "Adjust"}, {"A", "Done"}}
	}
	act := "Change"
	var extra FooterHint
	if s, ok := qsFocused(); ok {
		switch s.Kind {
		case qsKindLink:
			act = "Open"
		case qsKindSlider:
			if qsLay.cols > 0 {
				act = "Adjust"
			} else {
				extra = FooterHint{"Left/Right", "Adjust"}
			}
		case qsKindCycle:
			if qsLay.cols > 0 {
				act, extra = "Next", FooterHint{qsShoulderKeys(false), "Change"}
			} else {
				extra = FooterHint{"Left/Right", "Change"}
			}
		}
	}
	hints := []FooterHint{{"A", act}, {qsEditKeyName(), "Edit"}}
	if extra.Button != "" {
		hints = append(hints, extra)
	}
	return append(hints, FooterHint{"B", "Close"})
}

// qsFooterLine is the hints that fit in maxW: the one before Close goes
// first until the line fits.
func qsFooterLine(font *ttf.Font, maxW int32) string {
	hints := qsFooterHints()
	line := hintLine(hints...)
	for len(hints) > 2 {
		if w, _ := measureText(font, line); w <= maxW {
			break
		}
		hints = append(hints[:len(hints)-2], hints[len(hints)-1])
		line = hintLine(hints...)
	}
	return line
}

// qsDrawFooter draws the battery and connection line and the hints. Both
// lines are placed from the font height, so a large UI scale cannot push
// them onto the card's edge.
func qsDrawFooter(renderer *sdl.Renderer, config *Config, f qsFonts, g qsGeom) {
	font := f.row
	smallH := int32(font.Height())
	footerY := g.panel.Y + g.panel.H - smallH - 8
	infoY := footerY - smallH - 8
	if bat := getBatteryPercent(); bat >= 0 {
		// The level, the cable and the time left (battery_eta.go), in the
		// longest form that leaves the connectivity label its room.
		netW, _ := measureText(font, "! Offline")
		room := g.panel.W - 2*qsPadX - netW - 16
		line := batteryETALevelLine(bat, getBatteryCharging(), "Charging", "On cable", batteryETA(), func(s string) bool {
			w, _, _ := font.SizeUTF8(s)
			return int32(w) <= room
		})
		renderText(renderer, config, font, line, WithAlpha(ColorTextSecondary(), 200), g.panel.X+qsPadX, infoY)
	}
	netStr := "Online"
	if !IsOnline() {
		netStr = "! Offline"
	}
	nw, _ := measureText(font, netStr)
	renderText(renderer, config, font, netStr, WithAlpha(ColorTextSecondary(), 200), g.panel.X+g.panel.W-nw-qsPadX, infoY)

	hint := qsFooterLine(font, g.panel.W-2*qsPadX)
	hw, _ := measureText(font, hint)
	renderText(renderer, config, font, hint, WithAlpha(ColorTextTertiary(), 190), g.panel.X+(g.panel.W-hw)/2, footerY)
}
