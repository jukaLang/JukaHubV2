package main

// toolsgrid.go — what the Tools hub and the Network tools page share: the tile
// model, the sectioned grid (layout and D-pad walk), the page frame with its
// copy-IP chip, and the offline rule that dims a tile and says what it needs
// instead of letting a press fail.
//
// Both pages are registered scenes (toolshub.go, nettoolsscreen.go) drawn in
// Home's glass: the shared backdrop, the page header, glass tiles with the icon
// badge top-left and the name bottom-left, and the footer band. A page keeps
// its focus and scroll in a toolsHubPage, so coming back from a tool lands on
// the tile that opened it.
//
// Nothing here allocates per frame once a page is laid out: the tiles are
// rebuilt about once a second, the grid only when the tile set or the canvas
// changes, and every label goes through the shared text caches.

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// toolsHubNeed is what a tile needs before it can run.
type toolsHubNeed uint8

const (
	toolsHubNeedNothing toolsHubNeed = iota
	// toolsHubNeedLAN is an address on the local network. The servers, the
	// LAN scans and Watch Party work on a Wi-Fi network with no internet.
	toolsHubNeedLAN
	// toolsHubNeedInternet is a working route to the internet.
	toolsHubNeedInternet
)

// The tags a dimmed tile wears. The user's words: tools that cannot run are
// greyed out and say they need Wi-Fi, instead of failing on the press.
const (
	toolsHubTagWiFi     = "Needs Wi-Fi"
	toolsHubTagInternet = "Needs internet"
)

// toolsHubTone colours a tile's corner dot and its value line.
type toolsHubTone uint8

const (
	toolsHubToneNeutral toolsHubTone = iota
	toolsHubToneGood
	toolsHubToneWarn
	toolsHubToneBusy
)

// toolsHubTile is one tile of a tools page.
type toolsHubTile struct {
	// id is stable across rebuilds, so focus survives a refresh that adds or
	// drops a tile above the focused one.
	id string
	// section is set on the first tile of a group: the heading drawn above it,
	// with sectionIcon in its chip.
	section     string
	sectionIcon string
	label       string
	// value is the live one-liner under the name ("Running", an address).
	value string
	icon  string
	need  toolsHubNeed
	tone  toolsHubTone
	// wide tiles take a row of their own (the Watch Party hero).
	wide bool
	// blocked is the tag a tile wears while the network cannot serve it; it is
	// empty when the tile can run.
	blocked string
}

// toolsHubNet is the network as the tiles see it, read once per refresh.
type toolsHubNet struct {
	ip     string
	online bool
}

// toolsHubReadNet samples the LAN address and the internet probe. Both are
// cached by their owners, so this costs a mutex each.
func toolsHubReadNet() toolsHubNet {
	return toolsHubNet{ip: toolsIPAddress(), online: IsOnline()}
}

// toolsHubBlockedTag is the tag a tile with need wears on this network, or ""
// when it can run. No address at all is "Needs Wi-Fi" even for an internet
// tool: joining a network is the fix either way.
func toolsHubBlockedTag(need toolsHubNeed, net toolsHubNet) string {
	switch need {
	case toolsHubNeedLAN:
		if net.ip == "" {
			return toolsHubTagWiFi
		}
	case toolsHubNeedInternet:
		if net.ip == "" {
			return toolsHubTagWiFi
		}
		if !net.online {
			return toolsHubTagInternet
		}
	}
	return ""
}

// toolsHubBlockedMessage is what a press on a dimmed tile says.
func toolsHubBlockedMessage(label, tag string) string {
	if tag == toolsHubTagInternet {
		return label + " needs the internet — this Wi-Fi has no internet access right now"
	}
	return "Connect to Wi-Fi to use " + label
}

// toolsHubApplyNeeds fills every tile's tag for the network.
func toolsHubApplyNeeds(tiles []toolsHubTile, net toolsHubNet) {
	for i := range tiles {
		tiles[i].blocked = toolsHubBlockedTag(tiles[i].need, net)
	}
}

// toolsHubIndexOf finds a tile by id, or -1.
func toolsHubIndexOf(tiles []toolsHubTile, id string) int {
	for i := range tiles {
		if tiles[i].id == id {
			return i
		}
	}
	return -1
}

// ── Grid ──────────────────────────────────────────────────────────────────

// toolsHubGridRow is one visual row of tiles. headed marks a row with its
// section's heading directly above it, so scrolling to it shows the heading.
type toolsHubGridRow struct {
	y      int32
	tiles  []int
	headed bool
}

// toolsHubHeading is a section heading and the glyph drawn in its chip.
type toolsHubHeading struct {
	title string
	y     int32
	glyph func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color)
}

// toolsHubGrid is a laid-out page body. Coordinates start at the body's top
// edge; the page adds its own offset and the scroll when it draws.
type toolsHubGrid struct {
	rects    []sdl.Rect
	rows     []toolsHubGridRow
	headings []toolsHubHeading
	height   int32
	cols     int
}

// toolsHubGridSpec is the measured geometry a grid is laid out with.
type toolsHubGridSpec struct {
	x, width   int32
	cols       int
	tileH      int32
	gap        int32
	headingH   int32
	sectionGap int32
}

// toolsHubLayoutGrid lays tiles out in sections: a heading opens each group,
// the group's tiles wrap at cols, and a wide tile takes a row of its own. The
// tile order is the reading order, so the D-pad and the eye agree.
func toolsHubLayoutGrid(tiles []toolsHubTile, spec toolsHubGridSpec) toolsHubGrid {
	cols := spec.cols
	if cols < 1 {
		cols = 1
	}
	g := toolsHubGrid{rects: make([]sdl.Rect, len(tiles)), cols: cols}
	tileW := spec.width
	if cols > 1 {
		tileW = (spec.width - int32(cols-1)*spec.gap) / int32(cols)
	}
	var y int32
	col := 0
	open := false
	headed := false
	for i, t := range tiles {
		if t.section != "" {
			if open {
				y += spec.tileH + spec.gap
				open = false
			}
			if len(g.headings) > 0 {
				y += spec.sectionGap
			}
			g.headings = append(g.headings, toolsHubHeading{
				title: t.section,
				y:     y,
				glyph: toolsHubSectionGlyph(t.sectionIcon),
			})
			y += spec.headingH
			headed = true
		}
		if t.wide && open {
			y += spec.tileH + spec.gap
			open = false
		}
		if !open {
			g.rows = append(g.rows, toolsHubGridRow{y: y, headed: headed})
			headed = false
			open = true
			col = 0
		}
		rect := sdl.Rect{X: spec.x + int32(col)*(tileW+spec.gap), Y: y, W: tileW, H: spec.tileH}
		if t.wide {
			rect.X, rect.W = spec.x, spec.width
			col = cols
		} else {
			col++
		}
		g.rects[i] = rect
		last := len(g.rows) - 1
		g.rows[last].tiles = append(g.rows[last].tiles, i)
		if col >= cols {
			y += spec.tileH + spec.gap
			open = false
		}
	}
	if open {
		y += spec.tileH + spec.gap
	}
	if y > 0 {
		y -= spec.gap
	}
	g.height = y
	return g
}

// toolsHubSectionGlyph builds the heading chip's painter once per layout, so
// drawing a heading does not allocate a closure every frame.
func toolsHubSectionGlyph(icon string) func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
	if icon == "" {
		return nil
	}
	return func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
		DrawHomeIcon(r, icon, box, false)
	}
}

// toolsHubGridFind returns the row holding tile idx and its place in the row.
func toolsHubGridFind(g toolsHubGrid, idx int) (int, int) {
	for r, row := range g.rows {
		for k, t := range row.tiles {
			if t == idx {
				return r, k
			}
		}
	}
	return -1, -1
}

// toolsHubGridMove walks the grid from cur. Left and Right stay in the row and
// stop at its ends; Up and Down move to the neighbouring row and land on the
// tile nearest wantX, the column the walk started in, so a wide tile between
// two rows does not drag the cursor to the middle. ok is false when the move
// would leave the grid: the page decides what lies beyond the edge.
func toolsHubGridMove(g toolsHubGrid, cur, dx, dy int, wantX int32) (int, bool) {
	r, k := toolsHubGridFind(g, cur)
	if r < 0 {
		return cur, false
	}
	if dx != 0 {
		n := k + dx
		if n < 0 || n >= len(g.rows[r].tiles) {
			return cur, false
		}
		return g.rows[r].tiles[n], true
	}
	if dy == 0 {
		return cur, false
	}
	t := r + dy
	if t < 0 || t >= len(g.rows) {
		return cur, false
	}
	return toolsHubGridNearest(g, t, wantX), true
}

// toolsHubGridNearest is the tile of row r whose centre is nearest x.
func toolsHubGridNearest(g toolsHubGrid, r int, x int32) int {
	if r < 0 || r >= len(g.rows) {
		return -1
	}
	best, bestD := -1, int32(0)
	for _, idx := range g.rows[r].tiles {
		rc := g.rects[idx]
		d := rc.X + rc.W/2 - x
		if d < 0 {
			d = -d
		}
		if best < 0 || d < bestD {
			best, bestD = idx, d
		}
	}
	return best
}

// toolsHubSectionStep is the shoulder buttons' jump: the first tile of the next
// section, or — backwards — the start of the current section first and the
// previous one on the next press. Past the last section it lands on the last
// tile, so R1 always reaches the end of the page.
func toolsHubSectionStep(tiles []toolsHubTile, cur, dir int) int {
	n := len(tiles)
	if n == 0 {
		return cur
	}
	if cur < 0 {
		cur = 0
	}
	if cur >= n {
		cur = n - 1
	}
	if dir > 0 {
		for i := cur + 1; i < n; i++ {
			if tiles[i].section != "" {
				return i
			}
		}
		return n - 1
	}
	start := 0
	for i := cur; i >= 0; i-- {
		if tiles[i].section != "" {
			start = i
			break
		}
	}
	if start != cur {
		return start
	}
	for i := start - 1; i >= 0; i-- {
		if tiles[i].section != "" {
			return i
		}
	}
	return 0
}

// toolsHubScrollFor returns the offset that shows tile idx whole — with its
// heading when it opens a section, and the page top for the first row — and
// leaves the offset alone when the tile is already in view. pad keeps the
// focus ring and its bloom off the viewport's edge.
func toolsHubScrollFor(g toolsHubGrid, idx int, headingH, pad, viewH, scroll int32) int32 {
	r, _ := toolsHubGridFind(g, idx)
	if r < 0 || viewH <= 0 {
		return scroll
	}
	rc := g.rects[idx]
	top := rc.Y - pad
	if g.rows[r].headed {
		top = rc.Y - headingH
	}
	if r == 0 {
		top = 0
	}
	bottom := rc.Y + rc.H + pad
	if top < scroll {
		scroll = top
	}
	if bottom > scroll+viewH {
		scroll = bottom - viewH
	}
	return toolsHubClampScroll(scroll, g.height, viewH)
}

// toolsHubClampScroll keeps an offset inside the content.
func toolsHubClampScroll(scroll, contentH, viewH int32) int32 {
	maxScroll := contentH - viewH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}

// ── Page state ────────────────────────────────────────────────────────────

// toolsHubGridKey says when a cached layout is still valid: the canvas, the
// tile count and which tiles open sections or span the row.
type toolsHubGridKey struct {
	n      int
	x, w   int32
	cols   int
	tileH  int32
	shape  uint64
	header int32
}

// toolsHubPage is the focus and scroll of one tile page.
type toolsHubPage struct {
	cursor int
	// chip is true while the copy-IP chip holds the focus.
	chip bool
	// wantX is the column a vertical walk keeps; -1 when none is remembered.
	wantX int32
	// scroll is the drawn offset; target is where it eases to.
	scroll float64
	target int32
	lastAt uint64
	// hover is the tile under the pointer, -1 for none.
	hover     int
	chipHover bool
	// Last frame's geometry, in screen coordinates, for clicks and the wheel.
	tileRects []sdl.Rect
	chipRect  sdl.Rect
	view      sdl.Rect
	grid      toolsHubGrid
	key       toolsHubGridKey
	headingH  int32
	// follow asks the next frame to bring the cursor into view.
	follow bool
}

// newToolsHubPage is a page focused on its first tile.
func newToolsHubPage() toolsHubPage {
	return toolsHubPage{wantX: -1, hover: -1, follow: true}
}

// toolsHubShapeOf hashes the parts of a tile list the layout depends on.
func toolsHubShapeOf(tiles []toolsHubTile) uint64 {
	var h uint64 = 1469598103934665603
	for i := range tiles {
		bits := uint64(0)
		if tiles[i].section != "" {
			bits |= 1
		}
		if tiles[i].wide {
			bits |= 2
		}
		h = (h ^ bits) * 1099511628211
	}
	return h
}

// reset puts the focus on the first tile and the page at its top.
func (p *toolsHubPage) reset() {
	p.cursor = 0
	p.chip = false
	p.wantX = -1
	p.scroll = 0
	p.target = 0
	p.follow = true
}

// clampCursor keeps the cursor on a tile after the list changed.
func (p *toolsHubPage) clampCursor(n int) {
	if p.cursor >= n {
		p.cursor = n - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// move walks the page. Up from the first row reaches the chip, Down from the
// chip comes back to the row under it, and Up from the chip is not used so
// the top bar can take it. It reports whether the press was used.
func (p *toolsHubPage) move(tiles []toolsHubTile, dx, dy int) bool {
	if len(tiles) == 0 {
		if dy < 0 && !p.chip {
			p.chip = true
			PlayNavSound()
			return true
		}
		return false
	}
	p.clampCursor(len(tiles))
	if p.chip {
		if dy > 0 {
			p.chip = false
			// Grid rects are relative to the content's left edge.
			chipX := p.chipRect.X + p.chipRect.W/2 - p.view.X
			if next := toolsHubGridNearest(p.grid, 0, chipX); next >= 0 {
				p.cursor = next
			}
			p.wantX = -1
			p.follow = true
			PlayNavSound()
			return true
		}
		// Left/Right have nowhere to go from the chip; Up belongs to the bar.
		return dy == 0
	}
	if dx != 0 {
		p.wantX = -1
	}
	if dy != 0 && p.wantX < 0 && p.cursor < len(p.grid.rects) {
		rc := p.grid.rects[p.cursor]
		p.wantX = rc.X + rc.W/2
		if tiles[p.cursor].wide {
			// A row-wide tile has no column of its own: a walk that starts on
			// it enters the next row at its first tile, the way a launcher's
			// hero does, rather than in the middle of the row.
			p.wantX = rc.X
		}
	}
	next, ok := toolsHubGridMove(p.grid, p.cursor, dx, dy, p.wantX)
	if !ok {
		if dy < 0 && p.chipRect.W > 0 {
			p.chip = true
			PlayNavSound()
			return true
		}
		// The edge swallows the press so focus never jumps somewhere
		// unexpected; Up on a page without a chip goes to the bar.
		return dy >= 0
	}
	if next != p.cursor {
		p.cursor = next
		p.follow = true
		PlayNavSound()
	}
	return true
}

// jumpSection is L1/R1: one section back or forward.
func (p *toolsHubPage) jumpSection(tiles []toolsHubTile, dir int) {
	if len(tiles) == 0 {
		return
	}
	p.chip = false
	next := toolsHubSectionStep(tiles, p.cursor, dir)
	if next != p.cursor {
		p.cursor = next
		p.wantX = -1
		p.follow = true
		PlayNavSound()
	}
}

// wheel scrolls the body without moving the focus; the next D-pad press
// brings the focused tile back into view.
func (p *toolsHubPage) wheel(y int32) bool {
	if y == 0 || p.view.H <= 0 {
		return false
	}
	step := p.headingH * 2
	if step < 48 {
		step = 48
	}
	p.target = toolsHubClampScroll(p.target-y*step, p.grid.height, p.view.H)
	p.follow = false
	return true
}

// hitTile is the tile under a point, or -1.
func (p *toolsHubPage) hitTile(x, y int32) int {
	for i, r := range p.tileRects {
		if r.W > 0 && r.H > 0 && pointInRect(x, y, r) {
			return i
		}
	}
	return -1
}

// ── Fonts ─────────────────────────────────────────────────────────────────

// toolsHubFonts are the sizes a tile page draws with, resolved once a frame.
type toolsHubFonts struct {
	title *ttf.Font
	value *ttf.Font
	tag   *ttf.Font
	hero  *ttf.Font
	chip  *ttf.Font
	body  *ttf.Font
}

func toolsHubLoadFonts(config *Config) toolsHubFonts {
	small, _ := getCachedFont(config, "small")
	pick := func(f *ttf.Font) *ttf.Font {
		if f == nil {
			return small
		}
		return f
	}
	return toolsHubFonts{
		title: pick(getFontSized(config, "small", 20)),
		value: pick(getFontSized(config, "small", 15)),
		tag:   pick(getFontSized(config, "small", 13)),
		hero:  pick(getFontSized(config, "medium", 24)),
		chip:  pick(getFontSized(config, "small", 17)),
		body:  pick(getFontSized(config, "small", 17)),
	}
}

// toolsHubFontH is a font's line height, with a fallback for a missing font.
func toolsHubFontH(font *ttf.Font, fallback int32) int32 {
	if font == nil {
		return fallback
	}
	return int32(font.Height())
}

// ── Frame ─────────────────────────────────────────────────────────────────

// toolsHubFrame is a page's measured frame: the header band, the chip and the
// body viewport the grid scrolls in.
type toolsHubFrame struct {
	contentX, contentW int32
	headerY            int32
	bodyTop, bodyBot   int32
	chip               sdl.Rect
	spec               toolsHubGridSpec
}

// toolsHubMeasureFrame lays a page out on the current canvas. The content
// starts at Home's side margin, the header sits under the status bar, and the
// body ends above the footer band.
func toolsHubMeasureFrame(config *Config, header UIPageHeader, fonts toolsHubFonts, ip string) toolsHubFrame {
	var f toolsHubFrame
	margin := scaleX(homeSideMargin, screenWidth)
	if margin < 16 {
		margin = 16
	}
	f.contentX = margin
	f.contentW = screenWidth - 2*margin
	f.headerY = headerBarHeight() + 12
	m := uiPageHeaderMetricsFor(config, header)
	f.bodyTop = f.headerY + m.Height + 16
	f.bodyBot = screenHeight - bottomBarHeight("") - 16
	if f.bodyBot < f.bodyTop+60 {
		f.bodyBot = f.bodyTop + 60
	}
	chipW, chipH := toolsHubChipSize(fonts, ip)
	well := m.WellBox
	if well <= 0 {
		well = m.TitleH
	}
	f.chip = sdl.Rect{
		X: f.contentX + f.contentW - chipW,
		Y: f.headerY + (well-chipH)/2,
		W: chipW,
		H: chipH,
	}
	if f.chip.Y < headerBarHeight()+4 {
		f.chip.Y = headerBarHeight() + 4
	}

	gap := int32(14)
	minTile := int32(196)
	cols := int((f.contentW + gap) / (minTile + gap))
	if cols < 2 {
		cols = 2
	}
	if cols > 6 {
		cols = 6
	}
	tileH := screenHeight * 17 / 100
	if tileH < 104 {
		tileH = 104
	}
	if tileH > 132 {
		tileH = 132
	}
	headingH := toolsHubFontH(getFontSized(config, "small", 15), 18) + 20
	f.spec = toolsHubGridSpec{
		x:          0,
		width:      f.contentW,
		cols:       cols,
		tileH:      tileH,
		gap:        gap,
		headingH:   headingH,
		sectionGap: 6,
	}
	return f
}

// ── Chip ──────────────────────────────────────────────────────────────────

// toolsHubChipLabel is what the copy-IP chip says.
func toolsHubChipLabel(ip string) string {
	if ip == "" {
		return "No Wi-Fi"
	}
	return ip
}

// toolsHubChipSize measures the chip: a Wi-Fi glyph, the address and, when
// there is one, the copy glyph that says what pressing it does.
func toolsHubChipSize(fonts toolsHubFonts, ip string) (int32, int32) {
	tw, th := measureText(fonts.chip, toolsHubChipLabel(ip))
	h := th + 18
	if h < 40 {
		h = 40
	}
	icon := h - 20
	w := 14 + icon + 8 + tw + 14
	if ip != "" {
		w += 10 + icon
	}
	return w, h
}

// toolsHubDrawChip paints the copy-IP chip in the header's top-right corner.
// Without an address it is dimmed and reads "No Wi-Fi"; it still takes the
// focus, and pressing it says how to get an address.
func toolsHubDrawChip(renderer *sdl.Renderer, fonts toolsHubFonts, rect sdl.Rect, ip string, focused, hovered bool) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if focused {
		drawGlassFocusGlow(renderer, rect)
	}
	drawGlassSurface(renderer, rect, rect.H/2, glassState{
		Focused: focused,
		Hovered: hovered && !focused,
	})
	icon := rect.H - 20
	iy := rect.Y + (rect.H-icon)/2
	x := rect.X + 14
	glyph := "wifi"
	ink := ColorTextPrimary()
	if ip == "" {
		glyph = "wifi-off"
		ink = ColorTextTertiary()
	}
	toolsHubDrawGlyph(renderer, glyph, sdl.Rect{X: x, Y: iy, W: icon, H: icon}, ip == "")
	x += icon + 8
	label := toolsHubChipLabel(ip)
	tw, th := measureText(fonts.chip, label)
	drawText(renderer, fonts.chip, label, x, rect.Y+(rect.H-th)/2, ink, textAlignLeft)
	if ip != "" {
		DrawHomeIcon(renderer, "copy", sdl.Rect{X: x + tw + 10, Y: iy, W: icon, H: icon}, false)
	}
}

// toolsHubCopyIP is the chip's action.
func toolsHubCopyIP() {
	ip := toolsIPAddress()
	if ip == "" {
		ShowToast("No IP address yet — connect to Wi-Fi first", ToastKindWarning)
		return
	}
	if err := copyTextToClipboard(ip, "Tools"); err != nil {
		ShowToast("Could not copy the IP address", ToastKindError)
		return
	}
	ShowToast("IP copied", ToastKindSuccess)
}

// ── Tiles ─────────────────────────────────────────────────────────────────

// toolsHubToneColor is the dot (and value ink) for a tone.
func toolsHubToneColor(tone toolsHubTone) sdl.Color {
	switch tone {
	case toolsHubToneGood:
		return ColorToastSuccess
	case toolsHubToneWarn:
		return ColorToastWarn
	case toolsHubToneBusy:
		return glassAccent()
	}
	return sdl.Color{}
}

// toolsHubValueInk is the value line's colour: readable status inks for a
// running or warning tile, the muted ink otherwise.
func toolsHubValueInk(t toolsHubTile) sdl.Color {
	if t.blocked != "" {
		return ColorTextTertiary()
	}
	switch t.tone {
	case toolsHubToneGood:
		return ToastSuccessInk()
	case toolsHubToneWarn:
		return ToastWarnInk()
	}
	return ColorTextMuted()
}

// toolsHubDrawGlyph draws a tile's icon from the shared set: in the theme's
// ink, or in the tertiary ink on a tile the network cannot serve, so the icon
// greys out with its name.
func toolsHubDrawGlyph(renderer *sdl.Renderer, name string, box sdl.Rect, dim bool) {
	if dim {
		drawHomeGlyphTinted(renderer, name, box, ColorTextTertiary())
		return
	}
	DrawHomeIcon(renderer, name, box, false)
}

// toolsHubDrawTag paints a small pill right-aligned at (right, y) and returns
// its rect: the "Needs Wi-Fi" tag on a dimmed tile.
func toolsHubDrawTag(renderer *sdl.Renderer, font *ttf.Font, text string, right, y int32, fill, ink sdl.Color) sdl.Rect {
	if font == nil || text == "" {
		return sdl.Rect{}
	}
	tw, th := measureText(font, text)
	pill := sdl.Rect{X: right - tw - 16, Y: y, W: tw + 16, H: th + 6}
	fillRoundedRect(renderer, pill.X, pill.Y, pill.W, pill.H, pill.H/2, fill)
	drawText(renderer, font, text, pill.X+8, pill.Y+3, ink, textAlignLeft)
	return pill
}

// toolsHubDrawTile paints one tile in Home's card anatomy: a glass card with
// the icon badge top-left, the name bottom-left and the live value under it.
// A tile the network cannot serve is dimmed with a tag naming what it needs;
// it keeps its focus ring, because it stays focusable.
func toolsHubDrawTile(renderer *sdl.Renderer, fonts toolsHubFonts, rect sdl.Rect, t toolsHubTile, focused, hovered bool) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	blocked := t.blocked != ""
	radius := homeTileRadius
	if radius > rect.H/3 {
		radius = rect.H / 3
	}
	if focused {
		drawGlassFocusGlow(renderer, rect)
	}
	if t.wide && !blocked {
		// The hero carries the accent face Home's Media card has, as one flat
		// cached shape rather than a per-frame gradient.
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, WithAlpha(glassAccent(), 34))
	}
	drawGlassSurface(renderer, rect, radius, glassState{
		Focused:  focused,
		Hovered:  hovered && !focused,
		Disabled: blocked && !focused,
	})
	if t.wide && !blocked && !focused {
		strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, radius, 1, WithAlpha(glassAccent(), 110))
	}

	pad := rect.H / 8
	if pad < 12 {
		pad = 12
	}
	if pad > 18 {
		pad = 18
	}
	ink := ColorTextPrimary()
	if blocked {
		ink = ColorTextTertiary()
	}
	valueInk := toolsHubValueInk(t)

	if t.wide {
		toolsHubDrawHeroBody(renderer, fonts, rect, t, pad, ink, valueInk)
		return
	}

	badge := rect.H * 36 / 100
	if badge < 30 {
		badge = 30
	}
	if badge > homeIconSize {
		badge = homeIconSize
	}
	badgeRect := sdl.Rect{X: rect.X + pad, Y: rect.Y + pad, W: badge, H: badge}
	glyph := drawGlassBadge(renderer, badgeRect)
	toolsHubDrawGlyph(renderer, t.icon, glyph, blocked)
	if blocked {
		toolsHubDrawTag(renderer, fonts.tag, t.blocked, rect.X+rect.W-pad+4, rect.Y+pad-2,
			WithAlpha(ColorToastWarn, 38), ToastWarnInk())
	} else if c := toolsHubToneColor(t.tone); c.A > 0 {
		if t.tone == toolsHubToneBusy {
			phase := float64(sdl.GetTicks64()%1200) / 1200
			c.A = uint8(140 + 100*math.Sin(phase*2*math.Pi))
		}
		fillCircle(renderer, rect.X+rect.W-pad-2, rect.Y+pad+6, 5, c)
	}

	textW := rect.W - 2*pad
	valueH := toolsHubFontH(fonts.value, 18)
	titleH := toolsHubFontH(fonts.title, 24)
	bottom := rect.Y + rect.H - pad
	titleY := bottom - titleH
	if strings.TrimSpace(t.value) != "" {
		valueY := bottom - valueH
		titleY = valueY - titleH + 1
		drawText(renderer, fonts.value, ellipsize(fonts.value, t.value, textW), rect.X+pad, valueY, valueInk, textAlignLeft)
	}
	if titleY < badgeRect.Y+badge+2 {
		titleY = badgeRect.Y + badge + 2
	}
	drawText(renderer, fonts.title, ellipsize(fonts.title, t.label, textW), rect.X+pad, titleY, ink, textAlignLeft)
}

// toolsHubDrawHeroBody lays a wide tile out side by side: a large badge on the
// left, the name and the value beside it, and the state pill on the right.
func toolsHubDrawHeroBody(renderer *sdl.Renderer, fonts toolsHubFonts, rect sdl.Rect, t toolsHubTile, pad int32, ink, valueInk sdl.Color) {
	badge := rect.H * 52 / 100
	if badge < 40 {
		badge = 40
	}
	if badge > 64 {
		badge = 64
	}
	badgeRect := sdl.Rect{X: rect.X + pad + 6, Y: rect.Y + (rect.H-badge)/2, W: badge, H: badge}
	glyph := drawGlassBadge(renderer, badgeRect)
	toolsHubDrawGlyph(renderer, t.icon, glyph, t.blocked != "")

	right := rect.X + rect.W - pad - 4
	switch {
	case t.blocked != "":
		tag := toolsHubDrawTag(renderer, fonts.tag, t.blocked, right, rect.Y+(rect.H-toolsHubFontH(fonts.tag, 16)-6)/2,
			WithAlpha(ColorToastWarn, 38), ToastWarnInk())
		right = tag.X - 12
	case t.tone == toolsHubToneGood:
		tag := toolsHubDrawTag(renderer, fonts.tag, "LIVE", right, rect.Y+(rect.H-toolsHubFontH(fonts.tag, 16)-6)/2,
			WithAlpha(ColorToastSuccess, 46), ToastSuccessInk())
		right = tag.X - 12
	}

	textX := badgeRect.X + badge + 20
	textW := right - textX
	if textW < 40 {
		textW = 40
	}
	titleH := toolsHubFontH(fonts.hero, 30)
	valueH := toolsHubFontH(fonts.value, 18)
	blockH := titleH
	if t.value != "" {
		blockH += 2 + valueH
	}
	y := rect.Y + (rect.H-blockH)/2
	drawText(renderer, fonts.hero, ellipsize(fonts.hero, t.label, textW), textX, y, ink, textAlignLeft)
	if t.value != "" {
		drawText(renderer, fonts.value, ellipsize(fonts.value, t.value, textW), textX, y+titleH+2, valueInk, textAlignLeft)
	}
}

// ── Page ──────────────────────────────────────────────────────────────────

// toolsHubDrawPage paints a tile page under the shared bars: the header with
// the copy-IP chip, then the sectioned grid scrolled to keep the focus in
// view. ring is false while the top bar holds the focus, so the page never
// shows two focus rings.
func toolsHubDrawPage(renderer *sdl.Renderer, config *Config, page *toolsHubPage, header UIPageHeader, tiles []toolsHubTile, ip string, ring bool) {
	fonts := toolsHubLoadFonts(config)
	frame := toolsHubMeasureFrame(config, header, fonts, ip)

	header.X, header.Y = frame.contentX, frame.headerY
	header.W = frame.chip.X - 20 - frame.contentX
	UIPageHeaderDraw(renderer, config, header)

	page.chipRect = frame.chip
	page.chipHover = pointInRect(mouseX, mouseY, frame.chip)
	toolsHubDrawChip(renderer, fonts, frame.chip, ip, ring && page.chip, page.chipHover)

	key := toolsHubGridKey{
		n:      len(tiles),
		x:      frame.contentX,
		w:      frame.contentW,
		cols:   frame.spec.cols,
		tileH:  frame.spec.tileH,
		shape:  toolsHubShapeOf(tiles),
		header: frame.bodyTop,
	}
	if key != page.key || len(page.grid.rects) != len(tiles) {
		page.grid = toolsHubLayoutGrid(tiles, frame.spec)
		page.key = key
		page.follow = true
	}
	page.headingH = frame.spec.headingH
	page.clampCursor(len(tiles))

	viewH := frame.bodyBot - frame.bodyTop
	page.view = sdl.Rect{X: frame.contentX, Y: frame.bodyTop, W: frame.contentW, H: viewH}
	if page.follow && len(tiles) > 0 && !page.chip {
		page.target = toolsHubScrollFor(page.grid, page.cursor, frame.spec.headingH, 8, viewH, page.target)
		page.follow = false
	}
	if page.chip {
		page.target = 0
	}
	page.target = toolsHubClampScroll(page.target, page.grid.height, viewH)
	toolsHubEaseScroll(config, page)
	scroll := int32(math.Round(page.scroll))

	if cap(page.tileRects) < len(tiles) {
		page.tileRects = make([]sdl.Rect, len(tiles))
	}
	page.tileRects = page.tileRects[:len(tiles)]
	page.hover = -1

	// The clip reaches a little past the body so the focused tile's ring and
	// bloom are not cut flat at the viewport's edges.
	clipTop := frame.bodyTop - 10
	clipBot := frame.bodyBot + 6
	renderWithClip(renderer, 0, clipTop, screenWidth, clipBot-clipTop, func(r *sdl.Renderer) {
		for _, h := range page.grid.headings {
			y := frame.bodyTop + h.y - scroll
			if y+frame.spec.headingH < clipTop || y > clipBot {
				continue
			}
			UISectionLabel(r, config, h.title, frame.contentX, y, frame.contentW, h.glyph)
		}
		for i := range tiles {
			rc := page.grid.rects[i]
			rc.X += frame.contentX
			rc.Y += frame.bodyTop - scroll
			page.tileRects[i] = sdl.Rect{}
			if rc.Y+rc.H < clipTop || rc.Y > clipBot {
				continue
			}
			visible := glassIntersect(rc, sdl.Rect{X: 0, Y: frame.bodyTop, W: screenWidth, H: viewH})
			page.tileRects[i] = visible
			hovered := visible.W > 0 && pointInRect(mouseX, mouseY, visible)
			if hovered {
				page.hover = i
			}
			toolsHubDrawTile(r, fonts, rc, tiles[i], ring && !page.chip && i == page.cursor, hovered)
		}
	})

	if maxScroll := page.grid.height - viewH; maxScroll > 0 && viewH > 0 {
		trackX := frame.contentX + frame.contentW + (screenWidth-frame.contentX-frame.contentW)/2 - 2
		drawScrollbar(renderer, trackX, frame.bodyTop, 4, viewH,
			float64(viewH)/float64(page.grid.height), page.scroll/float64(maxScroll))
	}
}

// toolsHubEaseScroll moves the drawn offset toward the target: a short ease
// with motion on, a snap with reduced motion or the battery saver.
func toolsHubEaseScroll(config *Config, page *toolsHubPage) {
	now := sdl.GetTicks64()
	target := float64(page.target)
	if !uiMotionEnabled(config) || page.lastAt == 0 {
		page.scroll = target
		page.lastAt = now
		return
	}
	dt := float64(now - page.lastAt)
	page.lastAt = now
	if dt > 100 {
		dt = 100
	}
	k := 1 - math.Exp(-dt/70)
	page.scroll += (target - page.scroll) * k
	if math.Abs(target-page.scroll) < 0.5 {
		page.scroll = target
	}
}

// toolsHubRingVisible reports whether a page may draw its own focus ring: not
// while the top bar's chrome holds the focus.
func toolsHubRingVisible() bool {
	return chromeTier(focusEngine.ElementIndex()) == 0
}
