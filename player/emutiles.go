package main

// emutiles.go — the Emulators page: one tile per emulator, with its picture.
//
// emulators.go owns the catalog (what exists and whether it can launch). This
// file owns the page: a full-screen grid of tiles, each showing the emulator's
// own icon when the install ships one — the icon inside a TrimUI Emus/ folder,
// or the shell icon of a Windows executable — and a gamepad glyph otherwise.
//
// Status is spelled out, not implied: every usable-or-not tile carries a pill
// at its bottom edge reading "Available", "Unavailable" or "Not installed", and
// the picture of an unusable emulator is dimmed. A 4px corner dot was there
// before and it was the one thing people could not read from a metre away, which
// is exactly the distance a handheld is held at. The footer still spells out the
// highlighted entry and its path, so a long launcher path never has to fit
// inside a tile.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	emuTileMinW    = int32(186)
	emuTileH       = int32(150)
	emuTileGap     = int32(14)
	emuPagePad     = int32(24)
	emuIconBox     = int32(64)
	emuPageHeaderH = int32(96)
	emuPageFooterH = int32(82)
	emuPageMargin  = int32(20)
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

func emuGridView() emuGridLayout {
	page := sdl.Rect{
		X: emuPageMargin,
		Y: emuPageMargin,
		W: screenWidth - 2*emuPageMargin,
		H: screenHeight - 2*emuPageMargin,
	}
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
		Y: page.Y + emuPageHeaderH,
		W: page.W - 2*emuPagePad,
		H: page.H - emuPageHeaderH - emuPageFooterH,
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
// colour so the three states read at a glance, not just by their wording.
func emuStatusChipColors(status emuStatus) (fill, text sdl.Color) {
	text = ColorTextTertiary()
	switch status {
	case emuAvailable:
		text = ColorToastSuccess
		return WithAlpha(text, 38), text
	case emuBroken:
		text = ColorToastError
		return WithAlpha(text, 40), text
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
	case emuMissing:
		return 135
	}
	return 0
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
	label, _ := emuStatusLabel(entry.Status)
	chip := emuStatusChipRect(cell, font, label)
	if lineH > 0 && chip.Y-4-nameTop < lineH {
		return sdl.Rect{}
	}
	fill, text := emuStatusChipColors(entry.Status)
	drawChip(r, config, chip, label, fill, text, font)
	return chip
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
// or the entry's glyph on a tinted well when there is no picture.
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
		glyph = "gamepad"
	}
	inner := max32(20, box*48/100)
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
	drawCard(r, cell.X, cell.Y, cell.W, cell.H, 14)
	if focused {
		fillRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 14, ColorSurfaceRaised)
		fillRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 14, WithAlpha(accentColor, 70))
		strokeRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 14, 2, WithAlpha(accentColor, 235))
		fillRoundedRect(r, cell.X+2, cell.Y+14, 3, cell.H-28, 2, accentColor)
	} else if hovered {
		fillRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 14, WithAlpha(accentColor, 26))
		strokeRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 14, 1, WithAlpha(accentColor, 110))
	}

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
	if entry.Status == emuMissing {
		nameColor = ColorTextSecondary()
	}
	lines := wrapTextLines(labelFont, name, cell.W-18, maxLines)
	for i, line := range lines {
		lw, _, _ := labelFont.SizeUTF8(line)
		renderText(r, config, labelFont, line, nameColor,
			cell.X+(cell.W-int32(lw))/2, textTop+int32(i)*lineH)
	}
}

// renderEmuPanel draws the Emulators page: header, tile grid, footer detail.
// The name is kept from the old list panel so every entry point and test that
// already opened the Emulators screen keeps working.
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
			Glyph:  "gamepad",
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

	layout := emuGridView()
	emuPanelRect = layout.page
	firstRow := appGridFirstRow(emuPanel.cursor, layout.rows, len(entries), layout.cols)
	emuPanel.scroll = firstRow * layout.cols

	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 200)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

	fillRoundedRect(renderer, layout.page.X, layout.page.Y, layout.page.W, layout.page.H, 22, ColorPanelRaised)
	strokeRoundedRect(renderer, layout.page.X, layout.page.Y, layout.page.W, layout.page.H, 22, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: layout.page.X + 20, Y: layout.page.Y + 1, W: layout.page.W - 40, H: 2})

	// The page opens with the shared header every other page uses — a tinted
	// glyph well, the title, a one-line summary under it and the count
	// opposite — so the Emulators screen reads as the same kind of surface as
	// Tools and About instead of a bespoke panel.
	count := itoa(len(entries)) + " emulators"
	if len(entries) == 1 {
		count = "1 emulator"
	}
	totalRows := (len(entries) + layout.cols - 1) / layout.cols
	if totalRows > layout.rows {
		count = itoa(emuPanel.cursor+1) + " / " + itoa(len(entries))
	}
	UIPageHeaderDraw(renderer, config, UIPageHeader{
		X:           layout.page.X + emuPagePad,
		Y:           layout.page.Y + 16,
		W:           layout.page.W - 2*emuPagePad,
		Title:       "Emulators",
		Subtitle:    emuSummary(entries),
		Status:      count,
		StatusColor: ColorTextSecondary(),
		// The same title size the Tools page and the Settings tiles use: one
		// recipe, so every page header measures and draws the same.
		TitleSize: menuTitleSize,
		Glyph: func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			DrawHomeIcon(r, "gamepad", box, false)
		},
	})

	// ── Tiles ───────────────────────────────────────────────────────────
	hovered := -1
	if pointInRect(mouseX, mouseY, layout.body) {
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

	// ── Footer: the highlighted entry, then the controls ────────────────
	footerY := layout.page.Y + layout.page.H - emuPageFooterH
	renderer.SetDrawColor(ColorBorderDefault.R, ColorBorderDefault.G, ColorBorderDefault.B, 70)
	renderer.DrawLine(layout.body.X, footerY, layout.body.X+layout.body.W, footerY)

	selected := entries[emuPanel.cursor]
	statusLabel, statusColor := emuStatusLabel(selected.Status)
	nameLine := emuTileDisplayName(selected.Name)
	renderText(renderer, config, font, nameLine, ColorTextPrimary(),
		layout.body.X, footerY+12)
	nameW, _, _ := font.SizeUTF8(nameLine)
	statusEnd := layout.body.X + int32(nameW)
	renderText(renderer, config, font, " · "+statusLabel, statusColor,
		statusEnd, footerY+12)
	statusW, _, _ := font.SizeUTF8(" · " + statusLabel)
	statusEnd += int32(statusW)
	// A kept build is worth naming: it is the reference point R1 restores, and
	// a date here says which build that is without opening anything.
	if backup, ok := emuEntryBackup(selected); ok {
		backupLabel := " · backup " + backup.Stamp.Format("2006-01-02")
		renderText(renderer, config, font, backupLabel, ColorTextTertiary(),
			statusEnd, footerY+12)
		bw, _, _ := font.SizeUTF8(backupLabel)
		statusEnd += int32(bw)
	}
	detailX := statusEnd + 12
	if selected.Detail != "" {
		rest := layout.body.W - (detailX - layout.body.X)
		if rest > 40 {
			renderText(renderer, config, font, ellipsize(font, selected.Detail, rest),
				ColorTextTertiary(), detailX, footerY+12)
		}
	}

	hints := emuFooterHints(selected, retroAchievementsSupported())
	// The line is measured against the body instead of hoping it fits: the
	// Brick's canvas is narrower than the Smart Pro's, and a hint that runs
	// under the page border is unreadable.
	renderText(renderer, config, font, ellipsize(font, hintLine(hints...), layout.body.W),
		ColorTextSecondary(), layout.body.X, footerY+12+smallH+8)
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

// emuFooterHints is the control line the page draws. The A label follows what a
// press really does on the highlighted tile — the page installs an emulator
// where it used to print "no emulator found" — and X only appears when there is
// a core on the card to update, so no hint promises a file that is not there.
// R1 appears only when there is a kept build to restore.
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
	if len(emuInstalledCoreSet()) > 0 {
		hints = append(hints, FooterHint{Button: "L1", Label: "Update all"})
	}
	if achievements {
		hints = append(hints, FooterHint{Button: "Y", Label: "Achievements"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}
