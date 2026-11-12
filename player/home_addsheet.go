package main

// home_addsheet.go — the "Add to Home" sheet edit mode opens from its "+"
// tile: everything that can go on Home and is not there yet, in three
// sections — the widgets, the built-in Home cards and the Start menu's
// screens, tools and actions (homeTileCatalog). A puts the highlighted entry
// on Home after the tile that had the focus; B closes the sheet.

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// The sheet in 1280x720 design units.
const (
	homeAddSheetW       int32 = 620
	homeAddSheetH       int32 = 560
	homeAddSheetHeadH   int32 = 86
	homeAddSheetFootH   int32 = 58
	homeAddSheetRowH    int32 = 54
	homeAddSheetHeaderH int32 = 30
	homeAddSheetEnterMS       = 160
)

// homeAddRow is one line of the list: a section heading (entry -1) or an
// entry.
type homeAddRow struct {
	header string
	entry  int
}

// homeAddHit is a drawn entry row, for the pointer.
type homeAddHit struct {
	entry int
	rect  sdl.Rect
}

// homeAddSheetState is the open sheet.
type homeAddSheetState struct {
	open    bool
	entries []homeCatalogEntry
	rows    []homeAddRow
	cursor  int // index into entries
	scroll  int // first row drawn
	after   HomeFocusID
	enter   uiEnter
	card    sdl.Rect
	list    sdl.Rect
	hits    []homeAddHit
}

// homeAddSheetOpen builds the catalogue and shows the sheet; a chosen entry
// goes after the tile after.
func homeAddSheetOpen(config *Config, after HomeFocusID) {
	if !homeEdit.open {
		return
	}
	if config == nil {
		config = appConfig
	}
	if len(homeEdit.work) >= homeTilesMax {
		// The stored list holds homeTilesMax tiles; one more would be dropped
		// on the next read.
		ShowToast("Home is full: remove a tile first", ToastKindInfo)
		return
	}
	s := &homeEdit.sheet
	*s = homeAddSheetState{open: true, after: after}
	s.enter.begin(uiClockMS(), homeAddSheetEnterMS)
	homeEdit.holding = false
	s.entries = homeTileCatalog(config, homeTilesPresent(homeEdit.work))
	section := ""
	for i, e := range s.entries {
		if e.Section != section {
			section = e.Section
			// Upper-cased here, once, not on every frame the heading is drawn.
			s.rows = append(s.rows, homeAddRow{header: strings.ToUpper(uiT(section)), entry: -1})
		}
		s.rows = append(s.rows, homeAddRow{entry: i})
	}
}

// homeAddSheetClose hides the sheet.
func homeAddSheetClose() {
	homeEdit.sheet.open = false
	homeEdit.sheet.enter.cancel()
	homeEdit.sheet.hits = homeEdit.sheet.hits[:0]
}

// homeAddSheetMove steps the highlight.
func homeAddSheetMove(d int) {
	s := &homeEdit.sheet
	n := len(s.entries)
	if n == 0 {
		return
	}
	next := s.cursor + d
	if next < 0 {
		next = 0
	}
	if next > n-1 {
		next = n - 1
	}
	if next != s.cursor {
		s.cursor = next
		PlayNavSound()
	}
	homeAddSheetKeepVisible()
}

// homeAddSheetJump moves the highlight to the first entry of the next (d=1)
// or previous (d=-1) section, or the start of this one.
func homeAddSheetJump(d int) {
	s := &homeEdit.sheet
	n := len(s.entries)
	if n == 0 {
		return
	}
	cur := s.entries[s.cursor].Section
	next := s.cursor
	if d > 0 {
		for next < n && s.entries[next].Section == cur {
			next++
		}
		if next >= n {
			return
		}
	} else {
		start := s.cursor
		for start > 0 && s.entries[start-1].Section == cur {
			start--
		}
		next = start
		if start == s.cursor && start > 0 {
			prev := s.entries[start-1].Section
			for next > 0 && s.entries[next-1].Section == prev {
				next--
			}
			if s.entries[next].Section != prev {
				next = start - 1
			}
		}
	}
	if next != s.cursor {
		s.cursor = next
		PlayNavSound()
	}
	homeAddSheetKeepVisible()
}

// homeAddSheetRowOf is the row an entry is drawn on.
func homeAddSheetRowOf(entry int) int {
	for i, r := range homeEdit.sheet.rows {
		if r.entry == entry {
			return i
		}
	}
	return 0
}

// homeAddSheetRowHeight is a row's height on the canvas.
func homeAddSheetRowHeight(r homeAddRow) int32 {
	if r.entry < 0 {
		return scaleY(homeAddSheetHeaderH, screenHeight)
	}
	return scaleY(homeAddSheetRowH, screenHeight)
}

// homeAddSheetListH is the list's height on the canvas.
func homeAddSheetListH() int32 {
	h := min32(scaleY(homeAddSheetH, screenHeight), screenHeight-2*scaleY(20, screenHeight))
	return h - scaleY(homeAddSheetHeadH, screenHeight) - scaleY(homeAddSheetFootH, screenHeight)
}

// homeAddSheetKeepVisible scrolls so the highlighted entry (and the heading
// right above it) is drawn.
func homeAddSheetKeepVisible() {
	s := &homeEdit.sheet
	row := homeAddSheetRowOf(s.cursor)
	top := row
	if top > 0 && s.rows[top-1].entry < 0 {
		top--
	}
	if top < s.scroll {
		s.scroll = top
	}
	listH := homeAddSheetListH()
	for s.scroll < row {
		used := int32(0)
		for i := s.scroll; i <= row && i < len(s.rows); i++ {
			used += homeAddSheetRowHeight(s.rows[i])
		}
		if used <= listH {
			break
		}
		s.scroll++
	}
}

// homeAddSheetChoose puts the highlighted entry on Home.
func homeAddSheetChoose(config *Config) {
	s := &homeEdit.sheet
	if s.cursor < 0 || s.cursor >= len(s.entries) {
		return
	}
	entry := s.entries[s.cursor]
	after := s.after
	homeAddSheetClose()
	PlayActivateSound()
	homeEditInsert(config, entry.Ref, after)
}

// homeAddSheetPad is the controller while the sheet is open.
func homeAddSheetPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		homeAddSheetMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		homeAddSheetMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		homeAddSheetJump(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		homeAddSheetJump(1)
	case sdl.CONTROLLER_BUTTON_A:
		homeAddSheetChoose(config)
	case sdl.CONTROLLER_BUTTON_B:
		PlayBackSound()
		homeAddSheetClose()
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y, sdl.CONTROLLER_BUTTON_BACK:
		// Select starts chords (home_edit.go); X and Y do nothing here.
	default:
		return false
	}
	return true
}

// homeAddSheetKey is the keyboard while the sheet is open.
func homeAddSheetKey(e *sdl.KeyboardEvent, config *Config) bool {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		homeAddSheetMove(-1)
	case sdl.K_DOWN:
		homeAddSheetMove(1)
	case sdl.K_PAGEUP, sdl.K_LEFT:
		homeAddSheetJump(-1)
	case sdl.K_PAGEDOWN, sdl.K_RIGHT:
		homeAddSheetJump(1)
	case sdl.K_HOME:
		homeAddSheetMove(-len(homeEdit.sheet.entries))
	case sdl.K_END:
		homeAddSheetMove(len(homeEdit.sheet.entries))
	case sdl.K_TAB:
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			homeAddSheetJump(-1)
		} else {
			homeAddSheetJump(1)
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if e.Repeat == 0 {
			homeAddSheetChoose(config)
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		if e.Repeat == 0 {
			PlayBackSound()
			homeAddSheetClose()
		}
	case sdl.K_q, sdl.K_e, sdl.K_x, sdl.K_y, sdl.K_r, sdl.K_DELETE:
	default:
		return false
	}
	return true
}

// homeAddSheetClick is a tap while the sheet is open: an entry is added, the
// card swallows the rest of its own area, a tap outside closes it.
func homeAddSheetClick(mx, my int32, config *Config) {
	s := &homeEdit.sheet
	for _, hit := range s.hits {
		if pointInRect(mx, my, hit.rect) {
			s.cursor = hit.entry
			homeAddSheetChoose(config)
			return
		}
	}
	if s.card.W > 0 && pointInRect(mx, my, s.card) {
		return
	}
	PlayBackSound()
	homeAddSheetClose()
}

// homeAddSheetHover highlights the entry under a moving pointer.
func homeAddSheetHover(mx, my int32) {
	s := &homeEdit.sheet
	for _, hit := range s.hits {
		if pointInRect(mx, my, hit.rect) {
			s.cursor = hit.entry
			return
		}
	}
}

// homeAddSheetWheel scrolls the list a row per notch.
func homeAddSheetWheel(dy int32) {
	s := &homeEdit.sheet
	if dy > 0 {
		if s.scroll > 0 {
			s.scroll--
		}
		return
	}
	if dy < 0 && s.scroll < len(s.rows)-1 {
		// Stop once the last row is drawn.
		used := int32(0)
		for i := s.scroll; i < len(s.rows); i++ {
			used += homeAddSheetRowHeight(s.rows[i])
		}
		if used > homeAddSheetListH() {
			s.scroll++
		}
	}
}

// renderHomeAddSheet draws the open sheet over Home.
func renderHomeAddSheet(renderer *sdl.Renderer, config *Config) {
	s := &homeEdit.sheet
	if !homeEdit.open || !s.open || renderer == nil {
		return
	}
	fonts := loadHomeFonts()
	w, h := screenWidth, screenHeight
	t := 1.0
	if uiMotionEnabled(config) {
		t = s.enter.progress(uiClockMS())
	} else {
		s.enter.cancel()
	}
	progress := uiEaseOutCubic(t)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	scrim := glassScrimColor(progress)
	scrim.A = uint8(float64(scrim.A) * 0.75)
	setDrawColor(renderer, scrim)
	_ = renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: w, H: h})

	cw := min32(scaleX(homeAddSheetW, w), w-2*scaleX(24, w))
	ch := min32(scaleY(homeAddSheetH, h), h-2*scaleY(20, h))
	rise := int32(math.Round(uiPanelRise.at(t)))
	card := sdl.Rect{X: (w - cw) / 2, Y: (h-ch)/2 + rise, W: cw, H: ch}
	s.card = card
	fillRoundedRect(renderer, card.X+3, card.Y+8, card.W, card.H, OverlayCardRadius, sdl.Color{R: 0, G: 0, B: 0, A: 90})
	drawGlassSheet(renderer, card, glassSheetFill())

	pad := scaleX(24, w)
	textW := card.W - 2*pad
	if f := fonts.Card; f != nil {
		drawText(renderer, f, "Add to Home", card.X+pad, card.Y+scaleY(20, h), ColorTextPrimary(), textAlignLeft)
		if note := fonts.Helper; note != nil {
			drawText(renderer, note, ellipsize(note, "A widget, a Home card or anything from the Start menu", textW),
				card.X+pad, card.Y+scaleY(20, h)+int32(f.Height())+scaleY(2, h), ColorTextMuted(), textAlignLeft)
		}
	}

	list := sdl.Rect{X: card.X + scaleX(12, w), Y: card.Y + scaleY(homeAddSheetHeadH, h), W: card.W - 2*scaleX(12, w)}
	list.H = card.Y + card.H - scaleY(homeAddSheetFootH, h) - list.Y
	s.list = list
	s.hits = s.hits[:0]
	if len(s.entries) == 0 {
		if f := fonts.Helper; f != nil {
			drawText(renderer, f, "Everything is already on Home", list.X+list.W/2, list.Y+list.H/2-int32(f.Height())/2,
				ColorTextMuted(), textAlignCenter)
		}
	} else {
		hadClip := renderer.IsClipEnabled()
		prevClip := renderer.GetClipRect()
		clip := list
		if hadClip {
			clip = glassIntersect(prevClip, list)
		}
		_ = renderer.SetClipRect(&clip)
		y := list.Y
		for i := s.scroll; i < len(s.rows) && y < list.Y+list.H; i++ {
			row := s.rows[i]
			rh := homeAddSheetRowHeight(row)
			if row.entry < 0 {
				if f := fonts.Meta; f != nil {
					drawText(renderer, f, row.header, list.X+scaleX(12, w), y+(rh-int32(f.Height()))/2,
						ColorAccent, textAlignLeft)
				}
				y += rh
				continue
			}
			r := sdl.Rect{X: list.X, Y: y + scaleY(2, h), W: list.W, H: rh - scaleY(4, h)}
			homeAddSheetDrawRow(renderer, fonts, s.entries[row.entry], r, row.entry == s.cursor)
			if r.Y+r.H <= list.Y+list.H {
				s.hits = append(s.hits, homeAddHit{entry: row.entry, rect: r})
			}
			y += rh
		}
		if hadClip {
			_ = renderer.SetClipRect(&prevClip)
		} else {
			_ = renderer.SetClipRect(nil)
		}
		// More below: a soft chevron under the list.
		chev := scaleY(16, h)
		if y >= list.Y+list.H && len(s.hits) > 0 && s.hits[len(s.hits)-1].entry < len(s.entries)-1 {
			drawHomeGlyphTinted(renderer, "chevron-down", sdl.Rect{X: list.X + list.W/2 - chev/2, Y: list.Y + list.H, W: chev, H: chev},
				WithAlpha(ColorTextMuted(), 160))
		}
	}
	homeAddSheetDrawHints(renderer, fonts, card)
}

// homeAddSheetDrawRow draws one entry: the glyph badge, the name and its line,
// and the size it arrives at.
func homeAddSheetDrawRow(renderer *sdl.Renderer, fonts HomeFonts, e homeCatalogEntry, r sdl.Rect, focused bool) {
	w, h := screenWidth, screenHeight
	radius := scaleY(14, h)
	if focused {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, homeGlass(24))
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, radius, 2, ColorAccent)
	}
	badgeD := min32(scaleY(38, h), r.H-scaleY(10, h))
	badge := sdl.Rect{X: r.X + scaleX(10, w), Y: r.Y + (r.H-badgeD)/2, W: badgeD, H: badgeD}
	glyph := drawIconBadge(renderer, badge, focused)
	if focused {
		drawHomeGlyphTinted(renderer, e.Glyph, glyph, ColorAccent)
	} else {
		DrawHomeIcon(renderer, e.Glyph, glyph, false)
	}
	right := r.X + r.W - scaleX(12, w)
	if f := fonts.Small; f != nil {
		label := homeTileSizeLabel(e.Size)
		lw, _ := measureText(f, label)
		chip := sdl.Rect{W: lw + scaleX(16, w), H: int32(f.Height()) + scaleY(6, h)}
		chip.X = right - chip.W
		chip.Y = r.Y + (r.H-chip.H)/2
		fillRoundedRect(renderer, chip.X, chip.Y, chip.W, chip.H, chip.H/2, glassChipFill())
		drawText(renderer, f, label, chip.X+chip.W/2, chip.Y+(chip.H-int32(f.Height()))/2, glassChipInk(), textAlignCenter)
		right = chip.X - scaleX(10, w)
	}
	textX := badge.X + badge.W + scaleX(12, w)
	textW := right - textX
	title, note := fonts.Tile, fonts.Meta
	if title == nil || textW <= 0 {
		return
	}
	titleH := int32(title.Height())
	noteH := int32(0)
	if note != nil && e.Note != "" {
		noteH = int32(note.Height())
	}
	ty := r.Y + (r.H-titleH-noteH)/2
	drawText(renderer, title, ellipsize(title, e.Title, textW), textX, ty, ColorTextPrimary(), textAlignLeft)
	if noteH > 0 {
		drawText(renderer, note, ellipsize(note, e.Note, textW), textX, ty+titleH, ColorTextMuted(), textAlignLeft)
	}
}

// homeAddSheetHints is the sheet's own hint row (the footer is under the
// scrim).
var homeAddSheetHints = [...]footerHint{
	{button: "A", label: "Add"},
	{button: "L1/R1", label: "Sections"},
	{button: "B", label: "Close"},
}

// homeAddSheetDrawHints draws the hint badges right-aligned along the card's
// bottom, in the footer's style.
func homeAddSheetDrawHints(renderer *sdl.Renderer, fonts HomeFonts, card sdl.Rect) {
	f := fonts.Small
	if f == nil {
		return
	}
	w, h := screenWidth, screenHeight
	badgeD := scaleY(22, h)
	gap := scaleX(7, w)
	itemGap := scaleX(18, w)
	fh := int32(f.Height())
	cy := card.Y + card.H - scaleY(homeAddSheetFootH, h)/2
	// With nothing left to add, A and the sections say nothing.
	hints := homeAddSheetHints[:]
	if len(homeEdit.sheet.entries) == 0 {
		hints = homeAddSheetHints[2:]
	}
	var badgeWs, groupWs [len(homeAddSheetHints)]int32
	total := int32(0)
	for i, g := range hints {
		bw, _ := measureText(f, homeHintKey(g.button, g.label))
		lw, _ := measureText(f, g.label)
		badgeWs[i] = max32(badgeD, bw+scaleX(12, w))
		groupWs[i] = badgeWs[i] + gap + lw
		total += groupWs[i]
	}
	total += itemGap * int32(len(hints)-1)
	x := card.X + card.W - scaleX(24, w) - total
	for i, g := range hints {
		badge := sdl.Rect{X: x, Y: cy - badgeD/2, W: badgeWs[i], H: badgeD}
		fillRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, homeGlass(16))
		strokeRoundedRect(renderer, badge.X, badge.Y, badge.W, badge.H, badge.H/2, 1, homeGlass(45))
		drawText(renderer, f, homeHintKey(g.button, g.label), badge.X+badge.W/2, badge.Y+(badge.H-fh)/2, homeGlass(210), textAlignCenter)
		drawText(renderer, f, g.label, badge.X+badge.W+gap, cy-fh/2, ColorTextMuted(), textAlignLeft)
		x += groupWs[i] + itemGap
	}
}
