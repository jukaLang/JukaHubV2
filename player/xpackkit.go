package main

// xpackkit.go — the small UI kit the platform screens share: Experience packs,
// Config (the config inspector) and Backup & restore.
//
// It is a scrolling list of glass rows with section headings, switches, values
// and badges; a detail card's text and buttons; a modal picker sheet; and the
// page frame every screen opens with. Everything draws with Home's glass
// (ui_glass.go, components.go) and the theme inks, every text is measured and
// ellipsized before it is drawn, and a frame allocates nothing: wrapped text is
// cached, and the rects a click is tested against reuse their slices.

import (
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// xpackKitFonts are the type sizes the platform screens draw with.
type xpackKitFonts struct {
	big, title, body, small *ttf.Font
}

// xpackKitLoadFonts resolves the screens' fonts; false when they are not ready.
func xpackKitLoadFonts(c *Config) (xpackKitFonts, bool) {
	f := xpackKitFonts{
		big:   FontForRole(c, TypeHeading),
		title: FontForRole(c, TypeBody),
		body:  FontForRole(c, TypeLabel),
		small: FontForRole(c, TypeLabel),
	}
	return f, f.big != nil && f.title != nil && f.body != nil && f.small != nil
}

// xpackKitRowKind is what a list row is.
type xpackKitRowKind int

const (
	// xpackKitHeading is a section label; it never takes the focus.
	xpackKitHeading xpackKitRowKind = iota
	// xpackKitItem is a row that can be focused and pressed.
	xpackKitItem
)

// The tones a row's value and badge can take.
const (
	xpackKitToneNeutral = iota
	xpackKitToneGood
	xpackKitToneWarn
	xpackKitToneBad
	xpackKitToneAccent
)

// xpackKitRow is one list row. Only Label is required.
type xpackKitRow struct {
	Kind   xpackKitRowKind
	ID     string
	Label  string
	Detail string // the second line, in the secondary ink
	Value  string // right-aligned read-out (not drawn when Switch is set)
	// Badge is a small pill before the value ("Added", "Changed"), in Tone.
	Badge string
	Tone  int // one of the xpackKitTone values
	// Switch draws an on/off switch on the right, showing On.
	Switch   bool
	On       bool
	Disabled bool
}

// xpackKitToneInk is a tone as readable text on the current theme.
func xpackKitToneInk(tone int) sdl.Color {
	switch tone {
	case xpackKitToneGood:
		return SuccessInk()
	case xpackKitToneWarn:
		return WarningInk()
	case xpackKitToneBad:
		return DangerInk()
	case xpackKitToneAccent:
		return ColorTextAccent()
	}
	return ColorTextSecondary()
}

// xpackKitList is a scrolling list's focus and last frame's geometry.
type xpackKitList struct {
	cursor   int
	top      int32
	area     sdl.Rect
	rects    []sdl.Rect
	switches []sdl.Rect
}

// xpackKitFocusable reports whether row i can hold the focus.
func xpackKitFocusable(rows []xpackKitRow, i int) bool {
	return i >= 0 && i < len(rows) && rows[i].Kind == xpackKitItem
}

// clamp keeps the cursor on a focusable row.
func (l *xpackKitList) clamp(rows []xpackKitRow) {
	if len(rows) == 0 {
		l.cursor = 0
		return
	}
	if l.cursor >= len(rows) {
		l.cursor = len(rows) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
	if xpackKitFocusable(rows, l.cursor) {
		return
	}
	for i := l.cursor; i < len(rows); i++ {
		if xpackKitFocusable(rows, i) {
			l.cursor = i
			return
		}
	}
	for i := l.cursor; i >= 0; i-- {
		if xpackKitFocusable(rows, i) {
			l.cursor = i
			return
		}
	}
}

// move steps the focus |delta| focusable rows in delta's direction and reports
// whether it moved at all.
func (l *xpackKitList) move(rows []xpackKitRow, delta int) bool {
	if delta == 0 || len(rows) == 0 {
		return false
	}
	step, n := 1, delta
	if delta < 0 {
		step, n = -1, -delta
	}
	cur, moved := l.cursor, false
	for ; n > 0; n-- {
		next := cur + step
		for next >= 0 && next < len(rows) && !xpackKitFocusable(rows, next) {
			next += step
		}
		if next < 0 || next >= len(rows) {
			break
		}
		cur, moved = next, true
	}
	l.cursor = cur
	return moved
}

// atFirst reports whether no focusable row sits above the cursor, which is
// where Up hands the focus to the top bar, as on every screen.
func (l *xpackKitList) atFirst(rows []xpackKitRow) bool {
	for i := l.cursor - 1; i >= 0; i-- {
		if xpackKitFocusable(rows, i) {
			return false
		}
	}
	return true
}

// focused returns the focused row.
func (l *xpackKitList) focused(rows []xpackKitRow) (xpackKitRow, bool) {
	if xpackKitFocusable(rows, l.cursor) {
		return rows[l.cursor], true
	}
	return xpackKitRow{}, false
}

// hit finds the row under a click, and whether the click landed on its switch.
func (l *xpackKitList) hit(mx, my int32) (int, bool) {
	if !pointInRect(mx, my, l.area) {
		return -1, false
	}
	for i, r := range l.switches {
		if r.W > 0 && hitTouchTarget(r, mx, my) {
			return i, true
		}
	}
	for i, r := range l.rects {
		if r.W > 0 && pointInRect(mx, my, r) {
			return i, false
		}
	}
	return -1, false
}

// wheel moves the focus three rows per notch.
func (l *xpackKitList) wheel(rows []xpackKitRow, dy int32) bool {
	switch {
	case dy > 0:
		return l.move(rows, -3)
	case dy < 0:
		return l.move(rows, 3)
	}
	return false
}

// xpackKitRowHeight is the height of row r in fonts f.
func xpackKitRowHeight(f xpackKitFonts, r xpackKitRow) int32 {
	if r.Kind == xpackKitHeading {
		return int32(f.small.Height()) + 22
	}
	th := int32(f.title.Height())
	if r.Detail == "" {
		return max(th+24, 48)
	}
	return max(th+int32(f.small.Height())+24, 60)
}

// xpackKitSwitchRect is where a row's switch sits.
func xpackKitSwitchRect(r sdl.Rect) sdl.Rect {
	sw, sh := setSwitchSize(r.H)
	return sdl.Rect{X: r.X + r.W - 16 - sw, Y: r.Y + (r.H-sh)/2, W: sw, H: sh}
}

// xpackKitDrawList draws rows into area, scrolled so the focused row is in
// view, and records the rects a click is tested against. ring says whether the
// list holds the focus (the top bar or a sheet may hold it instead).
func xpackKitDrawList(renderer *sdl.Renderer, c *Config, f xpackKitFonts, l *xpackKitList, rows []xpackKitRow, area sdl.Rect, ring bool) {
	l.area = area
	n := len(rows)
	if cap(l.rects) < n {
		l.rects = make([]sdl.Rect, n)
		l.switches = make([]sdl.Rect, n)
	}
	l.rects = l.rects[:n]
	l.switches = l.switches[:n]
	const gap = int32(8)
	y := int32(0)
	for i := 0; i < n; i++ {
		h := xpackKitRowHeight(f, rows[i])
		if i == l.cursor {
			if y < l.top {
				l.top = y
			}
			if y+h > l.top+area.H {
				l.top = y + h - area.H
			}
		}
		y += h + gap
	}
	if total := y - gap; l.top > total-area.H {
		l.top = total - area.H
	}
	if l.top < 0 {
		l.top = 0
	}
	clip := area
	_ = renderer.SetClipRect(&clip)
	y = area.Y - l.top
	for i, row := range rows {
		h := xpackKitRowHeight(f, row)
		r := sdl.Rect{X: area.X, Y: y, W: area.W, H: h}
		y += h + gap
		l.rects[i], l.switches[i] = sdl.Rect{}, sdl.Rect{}
		if r.Y+r.H < area.Y || r.Y > area.Y+area.H {
			continue
		}
		if row.Kind == xpackKitHeading {
			UISectionLabel(renderer, c, row.Label, r.X+4, r.Y+6, r.W-8, nil)
			continue
		}
		l.rects[i] = r
		xpackKitDrawRow(renderer, f, row, r, ring && i == l.cursor, pointInRect(mouseX, mouseY, r))
		if row.Switch {
			l.switches[i] = xpackKitSwitchRect(r)
		}
	}
	_ = renderer.SetClipRect(nil)
}

// xpackKitDrawRow paints one row: the glass surface (focused: Home's accent
// ring), the label and detail on the left, and a switch or the value with its
// badge on the right. The right side is measured first and the label gets what
// is left, so nothing overlaps or runs out of the row.
func xpackKitDrawRow(renderer *sdl.Renderer, f xpackKitFonts, row xpackKitRow, r sdl.Rect, focused, hovered bool) {
	if focused {
		UIListRow(renderer, r, glassRowRadius, true, false)
	} else {
		drawGlassSurface(renderer, r, glassRowRadius, glassState{Hovered: hovered, NoLift: true, Disabled: row.Disabled})
	}
	const pad = int32(16)
	right := r.X + r.W - pad
	if row.Switch {
		sr := xpackKitSwitchRect(r)
		setSwitchDraw(renderer, sr, row.On, row.Disabled)
		right = sr.X - 14
	} else {
		if row.Value != "" {
			text := ellipsize(f.small, row.Value, r.W*2/5)
			tw, th := measureText(f.small, text)
			drawText(renderer, f.small, text, right-tw, r.Y+(r.H-th)/2, xpackKitToneInk(row.Tone), textAlignLeft)
			right -= tw + 12
		}
		if row.Badge != "" {
			bw, bh := setPillSize(f.small, row.Badge)
			if bw > 0 && right-bw > r.X+r.W/3 {
				ink := xpackKitToneInk(row.Tone)
				setPillDraw(renderer, f.small, row.Badge, right-bw, r.Y+(r.H-bh)/2, WithAlpha(ink, 44), ink)
				right -= bw + 10
			}
		}
	}
	avail := max(right-(r.X+pad), 40)
	ink := ColorTextPrimary()
	if row.Disabled {
		ink = ColorTextTertiary()
	}
	th := int32(f.title.Height())
	if row.Detail == "" {
		drawText(renderer, f.title, ellipsize(f.title, row.Label, avail), r.X+pad, r.Y+(r.H-th)/2, ink, textAlignLeft)
		return
	}
	sh := int32(f.small.Height())
	ty := r.Y + (r.H-th-sh-2)/2
	drawText(renderer, f.title, ellipsize(f.title, row.Label, avail), r.X+pad, ty, ink, textAlignLeft)
	drawText(renderer, f.small, ellipsize(f.small, row.Detail, avail), r.X+pad, ty+th+2, ColorTextSecondary(), textAlignLeft)
}

// xpackKitWrap caches one wrapped paragraph, so a card's text is measured when
// it changes rather than on every frame.
type xpackKitWrap struct {
	font  *ttf.Font
	text  string
	width int32
	limit int
	lines []string
	ready bool
}

// get returns text wrapped to width in at most limit lines.
func (w *xpackKitWrap) get(font *ttf.Font, text string, width int32, limit int) []string {
	if !w.ready || w.font != font || w.text != text || w.width != width || w.limit != limit {
		w.font, w.text, w.width, w.limit, w.ready = font, text, width, limit, true
		w.lines = wrapTextLines(font, text, width, limit)
	}
	return w.lines
}

// xpackKitText draws one line ellipsized to w and returns the y below it.
func xpackKitText(renderer *sdl.Renderer, font *ttf.Font, text string, x, y, w int32, ink sdl.Color) int32 {
	if text != "" {
		drawText(renderer, font, ellipsize(font, text, w), x, y, ink, textAlignLeft)
	}
	return y + int32(font.Height())
}

// xpackKitButton is one button a card offers.
type xpackKitButton struct {
	ID       string
	Label    string
	Primary  bool
	Disabled bool
}

// xpackKitDrawButtons lays buttons out two to a row from (x, y) across w,
// stopping above maxY, and records their rects (a zero rect for a button that
// did not fit). focus is the focused button's index, -1 for none. It returns
// the y below the last row.
func xpackKitDrawButtons(renderer *sdl.Renderer, c *Config, f xpackKitFonts, buttons []xpackKitButton, rects *[]sdl.Rect, x, y, w, maxY int32, focus int) int32 {
	*rects = (*rects)[:0]
	if len(buttons) == 0 {
		return y
	}
	const gap = int32(10)
	bw := (w - gap) / 2
	bh := max(int32(f.body.Height())+20, 44)
	for i, b := range buttons {
		col, row := int32(i%2), int32(i/2)
		r := sdl.Rect{X: x + col*(bw+gap), Y: y + row*(bh+gap), W: bw, H: bh}
		if r.Y+r.H > maxY {
			*rects = append(*rects, sdl.Rect{})
			continue
		}
		style := UIButtonStyle{Font: f.body}
		if b.Primary {
			style.Variant = UIButtonPrimary
		}
		UIButton(renderer, c, r, b.Label, style, UIButtonState{
			Focused:  i == focus,
			Hovered:  pointInRect(mouseX, mouseY, r),
			Disabled: b.Disabled,
		})
		*rects = append(*rects, r)
	}
	rows := int32((len(buttons) + 1) / 2)
	return y + rows*(bh+gap)
}

// xpackKitButtonHit finds the button under a click.
func xpackKitButtonHit(rects []sdl.Rect, mx, my int32) int {
	for i, r := range rects {
		if r.W > 0 && pointInRect(mx, my, r) {
			return i
		}
	}
	return -1
}

// xpackKitMoveButton steps the focus in a two-column button grid: dx moves
// along a row, dy between rows. It returns the new index and whether it moved.
func xpackKitMoveButton(cur, count, dx, dy int) (int, bool) {
	next := cur + dx + 2*dy
	if next < 0 || next >= count || (dx != 0 && next/2 != cur/2) {
		return cur, false
	}
	return next, true
}

// xpackKitFrame paints the backdrop and the page header and returns where the
// body starts, where it must end, and the side margin.
func xpackKitFrame(renderer *sdl.Renderer, c *Config, sceneName string, header UIPageHeader) (int32, int32, int32) {
	drawAppBackdrop(renderer, c, screenWidth, screenHeight)
	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + scaleY(8, screenHeight)
	bottom := screenHeight - bottomBarHeight(sceneName) - 16
	header.X, header.Y, header.W = margin, top, screenWidth-2*margin
	contentY := UIPageHeaderDraw(renderer, c, header) + 14
	return contentY, bottom, margin
}

// xpackKitFinish draws the shared status bar and footer over the page.
func xpackKitFinish(renderer *sdl.Renderer, c *Config) {
	_ = renderer.SetClipRect(nil)
	renderStatusBar(renderer, c)
	renderFooter(renderer, c)
}

// xpackKitChrome reports whether the top bar holds the focus, in which case a
// screen draws no focus ring of its own and lets the bar take the keys.
func xpackKitChrome() bool {
	return chromeTier(focusEngine.ElementIndex()) != 0
}

// xpackKitSheet is a modal list over a screen: a file picker, a choice.
type xpackKitSheet struct {
	open     bool
	title    string
	subtitle string
	rows     []xpackKitRow
	list     xpackKitList
	rect     sdl.Rect
}

// draw paints the sheet over the dimmed screen.
func (s *xpackKitSheet) draw(renderer *sdl.Renderer, c *Config, f xpackKitFonts) {
	if !s.open {
		return
	}
	drawOverlayScrim(renderer)
	w := min(screenWidth-2*scaleX(homeSideMargin, screenWidth), 780)
	h := min(screenHeight-headerBarHeight()-40, 540)
	s.rect = sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight-h)/2 + 10, W: w, H: h}
	drawOverlayCard(renderer, s.rect)
	header := overlayPageHeader(s.rect, s.title, s.subtitle, "")
	y := UIPageHeaderDraw(renderer, c, header) + 12
	area := sdl.Rect{X: s.rect.X + OverlayPadX, Y: y, W: s.rect.W - 2*OverlayPadX, H: s.rect.Y + s.rect.H - 18 - y}
	if area.H > 40 {
		s.list.clamp(s.rows)
		xpackKitDrawList(renderer, c, f, &s.list, s.rows, area, true)
	}
}

// xpackKitCount is "1 thing" or "n things".
func xpackKitCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}
