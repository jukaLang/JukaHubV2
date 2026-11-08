package main

// emutiles.go — the Emulators page: one tile per emulator, with its picture.
//
// emulators.go owns the catalog (what exists and whether it can launch). This
// file owns the page: a full-screen grid of glass tiles, each showing the
// emulator's own icon when the install ships one — the icon inside a TrimUI
// Emus/ folder, or the shell icon of a Windows executable — and otherwise the
// shared vector icon of what it emulates (emu-psp, emu-xbox, …).
//
// Status is spelled out, not implied: every usable-or-not tile carries a pill
// at its bottom edge reading "Available", "Unavailable", "Not installed" or
// "Not supported" — or "Update" when a newer build is known, "Installing…"
// with a progress track while one is being installed — and the picture of an
// unusable emulator is dimmed. A 4px corner dot was there before and it was the
// one thing people could not read from a metre away, which is exactly the
// distance a handheld is held at. The footer spells out the highlighted entry
// (version, kept build, path) and its actions as Home's glass hint badges, and
// every badge is also a click/tap target, so the mouse and a touchscreen reach
// Install, Update, Roll back and the rest the same way the pad does.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	emuTileMinW = int32(186)
	emuTileH    = int32(150)
	emuTileGap  = int32(14)
	emuPagePad  = OverlayPadX
	emuIconBox  = int32(64)
	// emuPageW is the width of the shared full-page card: the same 1040 the
	// Compatibility page uses, so the Emulators page lands on the same left and
	// right edges as its neighbours instead of running out to the screen edge.
	emuPageW = int32(1040)
	// emuPageMargin is the screen inset above and below that card.
	emuPageMargin = int32(20)
	// emuHeaderGap is the space between the shared header band and the first
	// row of tiles, matching the gap Downloads and Compatibility leave.
	emuHeaderGap = int32(10)
	// emuHeaderFallback is the band's height when the page is measured before a
	// config (and therefore the fonts) is available. It is the height the
	// shipped 720p config produces, so navigation and geometry tests that never
	// open a font still measure the page the way the device draws it.
	emuHeaderFallback = int32(86)
	emuPageFooterH    = int32(82)
	// emuStatusChipSize is the pill's own type size. It is deliberately smaller
	// than the tile name (22): the name identifies, the pill classifies.
	emuStatusChipSize = 15
	emuStatusChipPad  = int32(16)
	// emuTileMinIconBox is as small as the icon well may shrink before the name
	// and pill give way instead.
	emuTileMinIconBox = int32(36)
)

// emuGridLayout is the measured page: the outer page, the tile area inside it,
// and how many tiles fit at the current screen size.
type emuGridLayout struct {
	page  sdl.Rect
	body  sdl.Rect
	tileW int32
	tileH int32
	gap   int32
	cols  int
	rows  int
}

// emuPageRect is the page card: centred on the canvas, at the shared page's
// width, with the screen inset every full page keeps above and below it.
func emuPageRect() sdl.Rect {
	w := emuPageW
	if w > screenWidth-48 {
		w = screenWidth - 48
	}
	if w < 0 {
		w = screenWidth
	}
	h := screenHeight - 2*emuPageMargin
	if h < 0 {
		h = screenHeight
	}
	return sdl.Rect{
		X: (screenWidth - w) / 2,
		Y: (screenHeight - h) / 2,
		W: w,
		H: h,
	}
}

// emuPageHeader is the header the page opens with: the shared page bar, with
// the catalog's own summary under the title and its count opposite — or
// "Offline" in amber, because every install and update needs the connection.
// Every caller builds it here, so the band the grid reserves and the bar that
// is drawn are one measurement.
func emuPageHeader(entries []emuEntry, count string) UIPageHeader {
	h := UIPanelHeader("Emulators", emuSummary(entries), "emulator")
	h.Status = count
	h.StatusColor = ColorTextSecondary()
	return h
}

// emuPageHeaderBand is the strip the header owns above the grid. The height
// comes from the fonts, so it is measured from the page's own header; before a
// config exists (tests that only measure the page) it is the design height.
func emuPageHeaderBand(config *Config) int32 {
	if config == nil {
		return emuHeaderFallback
	}
	// The band depends on which header parts are present, not on their text,
	// so the catalog is not needed to measure it.
	return UIPanelHeaderBand(config, emuPageHeader(nil, "0"), emuHeaderGap)
}

// emuGridColumns is how many tiles fit across the page. Navigation needs only
// this number, and it follows the canvas rather than the type, so it can be
// answered without a config.
func emuGridColumns() int {
	bodyW := emuPageRect().W - 2*emuPagePad
	cols := int((bodyW + emuTileGap) / (emuTileMinW + emuTileGap))
	if cols < 1 {
		return 1
	}
	return cols
}

// emuGridView measures the page: the card, the area under the shared header,
// and how many tiles fit at the current screen size.
func emuGridView(config *Config) emuGridLayout {
	page := emuPageRect()
	band := emuPageHeaderBand(config)
	layout := emuGridLayout{
		page:  page,
		tileW: emuTileMinW,
		tileH: emuTileH,
		gap:   emuTileGap,
		cols:  1,
		rows:  1,
	}
	layout.body = sdl.Rect{
		X: page.X + emuPagePad,
		Y: page.Y + band,
		W: page.W - 2*emuPagePad,
		H: page.H - band - emuPageFooterH,
	}
	if layout.body.W <= 0 || layout.body.H <= 0 {
		return layout
	}
	cols := int((layout.body.W + emuTileGap) / (emuTileMinW + emuTileGap))
	if cols < 1 {
		cols = 1
	}
	layout.cols = cols
	layout.tileW = (layout.body.W - int32(cols-1)*emuTileGap) / int32(cols)
	rows := int((layout.body.H + emuTileGap) / (emuTileH + emuTileGap))
	if rows < 1 {
		rows = 1
	}
	layout.rows = rows
	return layout
}

// emuTileCell is the tile rect for an index inside the scrolled window.
func emuTileCell(layout emuGridLayout, index, firstRow int) sdl.Rect {
	col := int32(index % layout.cols)
	row := int32(index/layout.cols) - int32(firstRow)
	return sdl.Rect{
		X: layout.body.X + col*(layout.tileW+layout.gap),
		Y: layout.body.Y + row*(layout.tileH+layout.gap),
		W: layout.tileW,
		H: layout.tileH,
	}
}

// emuGridTarget moves one step on the grid. Left/Right clamp inside the row and
// Up/Down move a whole row; a partial last row lands on its last tile instead
// of a hole, so focus can never sit on nothing.
func emuGridTarget(cursor, total, cols, dx, dy int) int {
	if total <= 0 {
		return 0
	}
	if cols < 1 {
		cols = 1
	}
	if cursor < 0 || cursor >= total {
		cursor = 0
	}
	row, col := cursor/cols, cursor%cols
	row += dy
	col += dx
	if col < 0 {
		col = 0
	}
	if col > cols-1 {
		col = cols - 1
	}
	next := row*cols + col
	if next < 0 {
		next = 0
	}
	if next >= total {
		next = total - 1
	}
	return next
}

// emuTileIndexAt returns the tile under the pointer for click/hover handling,
// or -1 when the pointer is between tiles.
func emuTileIndexAt(layout emuGridLayout, total, firstRow int) int {
	for i := 0; i < total; i++ {
		cell := emuTileCell(layout, i, firstRow)
		if pointInRect(mouseX, mouseY, cell) {
			return i
		}
	}
	return -1
}

// emuFolderIcon finds the picture inside an emulator folder. TrimUI folders
// declare it in config.json (often just "icon.png"), and hand-made folders use
// the conventional names apps.go already knows.
func emuFolderIcon(folder string) string {
	if strings.TrimSpace(folder) == "" {
		return ""
	}
	if raw, err := os.ReadFile(filepath.Join(folder, "config.json")); err == nil {
		var cfg struct {
			Icon string `json:"icon"`
		}
		if json.Unmarshal(raw, &cfg) == nil {
			if declared := strings.TrimSpace(cfg.Icon); declared != "" {
				if resolved, err := resolveWithinRoot(folder, declared); err == nil {
					if icon := regularIconFile(resolved); icon != "" {
						return icon
					}
				}
			}
		}
	}
	return iconInFolder(folder)
}

// emuEntryIconPath is the image to draw for a tile, or "" when the tile should
// fall back to its glyph. The lookup stays cheap: the scan already found folder
// icons, and only Windows needs the per-frame cache probe for extracted icons.
// A real picture wins over the vector glyph: it is the emulator's own logo.
func emuEntryIconPath(entry emuEntry) string {
	if entry.IconPath != "" {
		return entry.IconPath
	}
	if entry.Launch == "" {
		return ""
	}
	if icon := sidecarIcon(entry.Launch); icon != "" {
		return icon
	}
	return extractedAppIcon(entry.Launch)
}

// emuProgramPaths lists the launchers that can carry an extracted icon, so the
// Windows helper can run once when the page opens.
func emuProgramPaths(entries []emuEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Launch) != "" {
			paths = append(paths, entry.Launch)
		}
	}
	return paths
}

// emuTileDisplayName makes a core id readable ("pcsx_rearmed" → "pcsx
// rearmed") without changing the catalog or the footer's exact name.
func emuTileDisplayName(name string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(name, "_", " ")), " ")
}

// emuStatusChipFont is the type used inside the status pill. It goes through
// getFontSized so accessibility text scaling still applies to it.
func emuStatusChipFont(config *Config) *ttf.Font {
	if font := getFontSized(config, "small", emuStatusChipSize); font != nil {
		return font
	}
	font, _ := getCachedFont(config, "small")
	return font
}

// emuStatusChipHeight follows the font, so a scaled-up pill stays readable
// instead of clipping its own label.
func emuStatusChipHeight(font *ttf.Font) int32 {
	if font != nil {
		if h := int32(font.Height()) + 6; h > 18 {
			return h
		}
	}
	return 18
}

// emuStatusChipRect centres the pill on the tile's bottom edge, wide enough for
// its label and never wider than the tile.
func emuStatusChipRect(cell sdl.Rect, font *ttf.Font, label string) sdl.Rect {
	w := int32(0)
	if font != nil {
		if tw, _, err := font.SizeUTF8(label); err == nil {
			w = int32(tw)
		}
	}
	w += emuStatusChipPad
	if w < 56 {
		w = 56
	}
	if max := cell.W - 16; w > max {
		w = max
	}
	h := emuStatusChipHeight(font)
	return sdl.Rect{
		X: cell.X + (cell.W-w)/2,
		Y: cell.Y + cell.H - 8 - h,
		W: w,
		H: h,
	}
}

// emuStatusChipColors is the pill body and its label: a tint of the status
// colour so the states read at a glance, not just by their wording. The tint is
// strong enough to stand off a focused tile's brighter glass.
func emuStatusChipColors(status emuStatus) (fill, text sdl.Color) {
	text = ColorTextSecondary()
	switch status {
	case emuAvailable:
		text = ToastSuccessInk()
		return WithAlpha(text, 46), text
	case emuBroken:
		text = ToastErrorInk()
		return WithAlpha(text, 48), text
	case emuUnsupported:
		text = ToastWarnInk()
		return WithAlpha(text, 42), text
	}
	return WithAlpha(text, 34), text
}

// emuStatusVeilAlpha dims an unusable tile's picture. A missing emulator is
// veiled harder than one that is installed but has no launcher: the first is
// absent, the second is close to working.
func emuStatusVeilAlpha(status emuStatus) uint8 {
	switch status {
	case emuBroken:
		return 70
	case emuMissing, emuUnsupported:
		return 135
	}
	return 0
}

// emuTileChip is the pill a tile shows: its status, or the state that matters
// more right now — an install running for it, or a newer build waiting.
func emuTileChip(entry emuEntry) (label string, fill, text sdl.Color) {
	if name := emuInstallingName(); name != "" && strings.EqualFold(name, entry.Name) {
		ink := glassAccent()
		return "Installing…", WithAlpha(ink, 50), ink
	}
	if emuTileUpdateKnown(entry) {
		ink := glassAccent()
		return "Update", WithAlpha(ink, 50), ink
	}
	label, _ = emuStatusLabel(entry.Status)
	fill, text = emuStatusChipColors(entry.Status)
	return label, fill, text
}

// drawEmuTileStatus draws the pill at the bottom of a tile. Rows that are
// actions rather than emulators (the pinned entries) and are usable stay bare:
// a column of "Available" pills on doors you can always open is noise.
//
// nameTop and lineH describe the space the tile name needs; when the pill would
// leave less than one line for it (the largest accessibility text on a 150px
// tile), the name wins and the pill is skipped. The footer always names the
// status, so nothing is lost, and text never draws through the pill.
func drawEmuTileStatus(r *sdl.Renderer, config *Config, cell sdl.Rect, entry emuEntry, nameTop, lineH int32) sdl.Rect {
	if entry.Pinned && entry.Status == emuAvailable {
		return sdl.Rect{}
	}
	font := emuStatusChipFont(config)
	if font == nil {
		return sdl.Rect{}
	}
	label, fill, text := emuTileChip(entry)
	chip := emuStatusChipRect(cell, font, label)
	if lineH > 0 && chip.Y-4-nameTop < lineH {
		return sdl.Rect{}
	}
	if r == nil {
		return chip
	}
	drawChip(r, config, chip, label, fill, text, font)
	// A hairline in the status ink gives the pill an edge of its own, so it
	// still reads on a focused tile, whose glass is brighter.
	strokeRoundedRect(r, chip.X, chip.Y, chip.W, chip.H, chip.H/2, 1, WithAlpha(text, 110))
	if label == "Installing…" {
		emuDrawInstallTrack(r, cell, chip)
	}
	return chip
}

// emuDrawInstallTrack is the thin progress line under an installing tile's
// pill, fed by the install bar's own byte counts.
func emuDrawInstallTrack(r *sdl.Renderer, cell, chip sdl.Rect) {
	visible, _, _, _, done, total, _, _, ending := installBarSnapshot()
	if !visible || ending {
		return
	}
	track := sdl.Rect{X: cell.X + 14, Y: chip.Y + chip.H + 3, W: cell.W - 28, H: 3}
	if track.Y+track.H > cell.Y+cell.H-1 {
		track.Y = cell.Y + cell.H - 1 - track.H
	}
	fillRoundedRect(r, track.X, track.Y, track.W, track.H, 1, glassA(30))
	if total > 0 && done > 0 {
		w := int32(int64(track.W) * done / total)
		if w > track.W {
			w = track.W
		}
		if w > 2 {
			fillRoundedRect(r, track.X, track.Y, w, track.H, 1, glassAccent())
		}
	}
}

// emuTileIconSize is the height of the icon well. It is emuIconBox normally and
// shrinks when the accessibility text scale leaves no room for a name line, a
// picture and the status pill together — the icon is the part that can give way
// without losing information. It never drops below emuTileMinIconBox, and an
// entry with no chip keeps the full box.
func emuTileIconSize(cell sdl.Rect, labelFont *ttf.Font, chipH int32) int32 {
	if labelFont == nil {
		return emuIconBox
	}
	lineH := int32(labelFont.Height() + 1)
	const topPad, nameGap, chipGap, bottomPad = int32(12), int32(8), int32(4), int32(8)
	need := topPad + emuIconBox + nameGap + lineH + chipGap + chipH + bottomPad
	if need <= cell.H {
		return emuIconBox
	}
	box := cell.H - (topPad + nameGap + lineH + chipGap + chipH + bottomPad)
	if box < emuTileMinIconBox {
		return emuTileMinIconBox
	}
	return box
}

// drawEmuTileIcon draws the emulator's picture fitted inside the square well,
// or the entry's vector glyph on a tinted well when there is no picture.
func drawEmuTileIcon(r *sdl.Renderer, config *Config, cell sdl.Rect, entry emuEntry, focused bool, box int32) {
	x := cell.X + (cell.W-box)/2
	y := cell.Y + 12

	if tex := appIconTexture(r, emuEntryIconPath(entry)); tex != nil {
		if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
			dw, dh := box, box
			if tw >= th {
				dh = max32(1, box*th/tw)
			} else {
				dw = max32(1, box*tw/th)
			}
			dst := sdl.Rect{X: x + (box-dw)/2, Y: y + (box-dh)/2, W: dw, H: dh}
			_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
			_ = r.Copy(tex, nil, &dst)
			return
		}
	}

	tint := appGridTileTint(entry.Name)
	wellAlpha := uint8(42)
	if focused {
		wellAlpha = 62
	}
	fillRoundedRect(r, x, y, box, box, 16, WithAlpha(tint, wellAlpha))
	strokeRoundedRect(r, x, y, box, box, 16, 1, WithAlpha(tint, 150))
	glyph := strings.TrimSpace(entry.Glyph)
	if glyph == "" {
		glyph = "emu-generic"
	}
	inner := max32(20, box*56/100)
	DrawHomeIcon(r, glyph, sdl.Rect{
		X: x + (box-inner)/2,
		Y: y + (box-inner)/2,
		W: inner,
		H: inner,
	}, false)
}

// drawEmuTile renders one tile: card, picture or glyph, wrapped name, and the
// status pill. The footer carries the long detail, so the tile stays legible.
func drawEmuTile(r *sdl.Renderer, config *Config, cell sdl.Rect, entry emuEntry, focused, hovered bool, labelFont *ttf.Font) {
	// Home's card: the glass surface, and Home's bloom, lift and ring when
	// focused.
	if focused {
		drawGlassFocusGlow(r, cell)
	}
	drawGlassSurface(r, cell, 16, glassState{Focused: focused, Hovered: hovered && !focused})

	chipFont := emuStatusChipFont(config)
	chipH := int32(0)
	if chipFont != nil && !(entry.Pinned && entry.Status == emuAvailable) {
		chipH = emuStatusChipHeight(chipFont)
	}
	box := emuTileIconSize(cell, labelFont, chipH)

	drawEmuTileIcon(r, config, cell, entry, focused, box)
	if veil := emuStatusVeilAlpha(entry.Status); veil > 0 {
		boxX := cell.X + (cell.W-box)/2
		fillRoundedRect(r, boxX, cell.Y+12, box, box, 16, WithAlpha(ColorShadow, veil))
	}

	if labelFont == nil {
		return
	}
	name := emuTileDisplayName(entry.Name)
	if name == "" {
		name = "Emulator"
	}
	lineH := int32(labelFont.Height() + 1)
	textTop := cell.Y + 12 + box + 8
	chip := drawEmuTileStatus(r, config, cell, entry, textTop, lineH)

	// The pill owns the bottom strip when it is there, so the name gets the
	// space above it and never draws through it.
	nameBottom := cell.Y + cell.H - 8
	if chip.W > 0 {
		nameBottom = chip.Y - 4
	}
	maxLines := int((nameBottom - textTop) / lineH)
	if maxLines < 1 {
		maxLines = 1
	}
	if maxLines > 2 {
		maxLines = 2
	}
	nameColor := ColorTextPrimary()
	if entry.Status == emuMissing || entry.Status == emuUnsupported {
		nameColor = ColorTextSecondary()
	}
	lines := wrapTextLines(labelFont, name, cell.W-18, maxLines)
	for i, line := range lines {
		lw, _ := measureText(labelFont, line)
		renderText(r, config, labelFont, line, nameColor,
			cell.X+(cell.W-lw)/2, textTop+int32(i)*lineH)
	}
}

// renderEmuPanel draws the Emulators page: header, tile grid, footer detail
// and the action badges. The name is kept from the old list panel so every
// entry point and test that already opened the Emulators screen keeps working.
func renderEmuPanel(renderer *sdl.Renderer, config *Config) {
	if !emuPanel.open || renderer == nil {
		return
	}
	entries := emulatorEntries()
	if len(entries) == 0 {
		entries = []emuEntry{{
			Name:   "No emulators found",
			Detail: "Nothing was found on this device",
			Status: emuMissing,
			Glyph:  "emu-generic",
		}}
	}
	if emuPanel.cursor >= len(entries) {
		emuPanel.cursor = len(entries) - 1
	}
	if emuPanel.cursor < 0 {
		emuPanel.cursor = 0
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	labelFont := appTileLabelFont(config)
	if labelFont == nil {
		labelFont = font
	}
	smallH := int32(font.Height())

	layout := emuGridView(config)
	emuPanelRect = layout.page
	firstRow := appGridFirstRow(emuPanel.cursor, layout.rows, len(entries), layout.cols)
	emuPanel.scroll = firstRow * layout.cols

	drawOverlayScrim(renderer)

	// The card is the shared full-page card: same width, radius, border and
	// accent hairline as Compatibility and the other page overlays.
	drawOverlayCard(renderer, layout.page)
	drawOverlayCardAccent(renderer, layout.page.X, layout.page.Y, layout.page.W)

	// The page opens with the shared header every other page uses — a glass
	// glyph badge, the title, a one-line summary under it and the count (or
	// "Offline") opposite — placed by the same inset and reserved with the same
	// measured band, so the grid starts exactly where the header ends.
	count := itoa(len(entries)) + " emulators"
	if len(entries) == 1 {
		count = "1 emulator"
	}
	totalRows := (len(entries) + layout.cols - 1) / layout.cols
	if totalRows > layout.rows {
		count = itoa(emuPanel.cursor+1) + " / " + itoa(len(entries))
	}
	header := emuPageHeader(entries, count)
	if !IsOnline() {
		header.Status = "Offline · installs need Wi-Fi"
		header.StatusColor = ToastWarnInk()
	}
	header = overlayHeaderOnCard(header, layout.page)
	UIPageHeaderDraw(renderer, config, header)

	// ── Tiles ───────────────────────────────────────────────────────────
	hovered := -1
	if pointInRect(mouseX, mouseY, layout.body) && !emuXboxSheetOpen() {
		hovered = emuTileIndexAt(layout, len(entries), firstRow)
	}
	emuRowHits = emuRowHits[:0]
	renderWithClip(renderer, layout.body.X, layout.body.Y, layout.body.W, layout.body.H, func(r *sdl.Renderer) {
		for i := range entries {
			row := i/layout.cols - firstRow
			if row < 0 || row >= layout.rows {
				continue
			}
			cell := emuTileCell(layout, i, firstRow)
			emuRowHits = append(emuRowHits, emuRowHit{index: i, rect: cell})
			drawEmuTile(r, config, cell, entries[i], i == emuPanel.cursor, i == hovered, labelFont)
		}
	})

	if totalRows > layout.rows {
		thumbFrac := float64(layout.rows) / float64(totalRows)
		scrollFrac := 0.0
		if totalRows-layout.rows > 0 {
			scrollFrac = float64(firstRow) / float64(totalRows-layout.rows)
		}
		drawScrollbar(renderer, layout.page.X+layout.page.W-8, layout.body.Y, 3,
			layout.body.H-emuTileGap, thumbFrac, scrollFrac)
	}

	// ── Footer: the highlighted entry, then its actions ─────────────────
	footerY := layout.page.Y + layout.page.H - emuPageFooterH
	setDrawColor(renderer, glassHairline())
	renderer.DrawLine(layout.body.X, footerY, layout.body.X+layout.body.W, footerY)

	selected := entries[emuPanel.cursor]
	emuDrawFooterDetail(renderer, config, font, selected, layout.body, footerY+12)

	hints := emuFooterHints(selected, retroAchievementsSupported())
	emuDrawFooterHints(renderer, config, hints, layout.body.X, footerY+12+smallH+8, layout.body.W)

	// The Xbox setup sheet sits over the page while it is open.
	renderEmuXboxSheet(renderer, config, layout.page)
}

// emuFooterSegment is one coloured part of the footer's detail line.
type emuFooterSegment struct {
	text  string
	color sdl.Color
}

// emuDrawFooterDetail is the footer's first line: the highlighted entry's
// name, its status, the installed version and the kept build, then the detail
// ellipsized into what is left of the width.
func emuDrawFooterDetail(renderer *sdl.Renderer, config *Config, font *ttf.Font, selected emuEntry, body sdl.Rect, y int32) {
	statusLabel, statusColor := emuStatusLabel(selected.Status)
	statusColor = StatusInk(statusColor)
	if selected.Status == emuMissing {
		statusColor = ColorTextTertiary()
	}
	x := body.X
	nameLine := ellipsize(font, emuTileDisplayName(selected.Name), body.W/2)
	renderText(renderer, config, font, nameLine, ColorTextPrimary(), x, y)
	nameW, _ := measureText(font, nameLine)
	x += nameW
	segments := []emuFooterSegment{{" · " + statusLabel, statusColor}}
	if emuTileUpdateKnown(selected) {
		segments = append(segments, emuFooterSegment{" · update available", glassAccent()})
	}
	if selected.Version != "" {
		segments = append(segments, emuFooterSegment{" · " + selected.Version, ColorTextSecondary()})
	}
	// A kept build is worth naming: it is the reference point R1 restores, and
	// a date here says which build that is without opening anything.
	if selected.KeptVersion != "" {
		segments = append(segments, emuFooterSegment{" · kept " + selected.KeptVersion, ColorTextTertiary()})
	} else if backup, ok := emuEntryBackup(selected); ok {
		segments = append(segments, emuFooterSegment{" · backup " + backup.Stamp.Format("2006-01-02"), ColorTextTertiary()})
	}
	for _, seg := range segments {
		w, _ := measureText(font, seg.text)
		if x+w > body.X+body.W {
			break
		}
		renderText(renderer, config, font, seg.text, seg.color, x, y)
		x += w
	}
	detail := selected.Detail
	if selected.Status == emuUnsupported && selected.Note != "" {
		detail = selected.Note
	}
	if detail != "" {
		detailX := x + 12
		if rest := body.W - (detailX - body.X); rest > 40 {
			renderText(renderer, config, font, ellipsize(font, detail, rest), ColorTextTertiary(), detailX, y)
		}
	}
}

// emuHintHit maps a drawn hint badge back to the button it names, so a click
// or tap on it runs the same action as the press (emuHintPress).
type emuHintHit struct {
	button string
	rect   sdl.Rect
}

// emuHintHits are the badges the last frame drew. Only touched on the
// render/event thread.
var emuHintHits []emuHintHit

// emuFittedHints trims a hint set to the width the page has, the way
// emuFooterLine trims the text form: the D-pad hint goes first, then the
// optional page keys from the right, and Back is always the last one standing.
func emuFittedHints(config *Config, hints []FooterHint, width int32) footerPlan {
	plan := planFooterHints(config, hints, width)
	if len(plan.hints) == len(hints) || len(hints) <= 2 {
		return plan
	}
	trimmed := hints
	if strings.EqualFold(strings.TrimSpace(trimmed[0].Button), "D-Pad") {
		trimmed = trimmed[1:]
		if plan = planFooterHints(config, trimmed, width); len(plan.hints) == len(trimmed) {
			return plan
		}
	}
	for len(trimmed) > 2 {
		trimmed = append(append([]FooterHint(nil), trimmed[:len(trimmed)-2]...), trimmed[len(trimmed)-1])
		if plan = planFooterHints(config, trimmed, width); len(plan.hints) == len(trimmed) {
			return plan
		}
	}
	return plan
}

// emuDrawFooterHints draws the controls as Home's glass hint badges, left to
// right from x, and records each badge as a click target.
func emuDrawFooterHints(renderer *sdl.Renderer, config *Config, hints []FooterHint, x, y, width int32) {
	emuHintHits = emuDrawHintRow(renderer, config, hints, x, y, width, emuHintHits[:0])
}

// emuDrawHintRow draws a fitted row of glass hint badges and appends a click
// target for every badge that names a button to hits. The Core builds page
// draws its footer with it too.
func emuDrawHintRow(renderer *sdl.Renderer, config *Config, hints []FooterHint, x, y, width int32, hits []emuHintHit) []emuHintHit {
	plan := emuFittedHints(config, hints, width)
	if plan.font == nil {
		return hits
	}
	textY := y + (plan.badgeH-plan.fontHeight)/2
	for i, hint := range plan.hints {
		labelX := x
		start := x
		if plan.badgeW[i] > 0 {
			badge := sdl.Rect{X: x, Y: y, W: plan.badgeW[i], H: plan.badgeH}
			labelX = badge.X + badge.W + plan.badgeGap
			drawGlassHintBadge(renderer, config, plan.font, badge, hint.Button, hint.Label, labelX, textY)
		} else {
			drawGlassText(renderer, config, plan.font, hint.Label, ColorTextMuted(), labelX, textY)
		}
		end := labelX + plan.labelW[i]
		if hint.Button != "" && hint.Button != "D-Pad" {
			hits = append(hits, emuHintHit{
				button: hint.Button,
				rect:   sdl.Rect{X: start - 4, Y: y - 4, W: end - start + 8, H: plan.badgeH + 8},
			})
		}
		x = end + plan.groupGap
	}
	return hits
}

// emuFooterLine fits the control hints into the width the page has. The widest
// set — a tile with a core to update, a build to roll back to, and the L1/R2/Y
// page keys all live — is wider than one page card, and a line that has to be
// ellipsized gets its tail cut, which is where B Back lives. Dropping whole
// hints is the readable trade: the D-pad line goes first (this page is a grid
// and the arrows are the one control nobody needs telling), then the optional
// page keys from the right, and Back is always the last hint standing.
func emuFooterLine(font *ttf.Font, hints []FooterHint, width int32) string {
	fits := func(line string) bool {
		if font == nil || width <= 0 {
			return true
		}
		tw, _, err := font.SizeUTF8(line)
		return err != nil || int32(tw) <= width
	}
	line := hintLine(hints...)
	if fits(line) {
		return line
	}
	trimmed := hints
	if len(trimmed) > 2 && strings.EqualFold(strings.TrimSpace(trimmed[0].Button), "D-Pad") {
		trimmed = trimmed[1:]
		if line = hintLine(trimmed...); fits(line) {
			return line
		}
	}
	for len(trimmed) > 2 {
		// Drop the hint just before Back: the optional page key is the one the
		// line can do without.
		trimmed = append(append([]FooterHint(nil), trimmed[:len(trimmed)-2]...), trimmed[len(trimmed)-1])
		if line = hintLine(trimmed...); fits(line) {
			return line
		}
	}
	return line
}

// emuEntryBackup is the newest kept build for a tile's core, if any. It goes
// through the same per-frame cache the hints use, so drawing never stats a
// folder.
func emuEntryBackup(e emuEntry) (coreBackup, bool) {
	core := emuCoreForEntry(e)
	if core == "" {
		return coreBackup{}, false
	}
	return emuNewestCoreBackup(core)
}

// emuAnyManaged reports whether the catalog holds a Windows emulator JukaHub
// installed (and Update all can therefore update).
func emuAnyManaged() bool {
	for _, e := range emulatorEntries() {
		if e.Managed {
			return true
		}
	}
	return false
}

// emuFooterHints is the control line the page draws. The A label follows what a
// press really does on the highlighted tile — the page installs an emulator
// where it used to print "no emulator found" — and X only appears when there is
// something on the card to update, so no hint promises a file that is not
// there. R1 appears only when there is a kept build to restore; Y is the Xbox
// setup on xemu's tile.
func emuFooterHints(e emuEntry, achievements bool) []FooterHint {
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	if label := emuInstallHintLabel(e); label != "" {
		hints = append(hints, FooterHint{Button: "A", Label: label})
	}
	if label := emuUpdateHintLabel(e); label != "" {
		hints = append(hints, FooterHint{Button: "X", Label: label})
	}
	if label := emuRollbackHintLabel(e); label != "" {
		hints = append(hints, FooterHint{Button: "R1", Label: label})
	}
	if len(emuInstalledCoreSet()) > 0 || (IsWindows() && emuAnyManaged()) {
		hints = append(hints, FooterHint{Button: "L1", Label: "Update all"})
	}
	// R2 opens the Core builds page. It only appears when there is something to
	// review, so no hint promises an empty page.
	if label := emuKeptBuildsHint(); label != "" {
		hints = append(hints, FooterHint{Button: "R2", Label: label})
	}
	if emuXboxSetupAvailable(e) {
		hints = append(hints, FooterHint{Button: "Y", Label: "Setup"})
	} else if achievements {
		hints = append(hints, FooterHint{Button: "Y", Label: "Achievements"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}
