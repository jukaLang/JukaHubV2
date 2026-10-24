package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// appgrid.go draws the Apps scene as a launcher grid of tiles instead of the
// old one-column list. The entries are still the "app_entries" list from
// apps.go; only the presentation and navigation change, so scan/launch code
// keeps working and user configs that still declare a dynamiclist on
// app_entries get the grid too.
//
// Each tile shows the app's own artwork when one can be found (see appicons.go)
// and a tinted initial when it cannot, so a launcher full of shortcuts without
// pictures still reads as a deliberate grid rather than a list of letters.

const (
	appGridMinTileW = int32(152)
	appGridTileH    = int32(132)
	appGridGap      = int32(12)
	appGridPad      = int32(12)
	appGridHeadH    = int32(44)
	// appIconBox is the square an app's artwork is fitted into at the top of a
	// tile; appIconTop is its distance from the tile's top edge.
	appIconBox = int32(52)
	appIconTop = int32(8)
	// appTileLabelSize is the pixel size of a tile's name. It is smaller than
	// the theme's list font because a tile is a square, not a row: the theme
	// size would fit one clipped word where two readable lines fit here.
	appTileLabelSize = 22
)

// appGridLayout is the measured grid: tile size, columns and the panel origin.
type appGridLayout struct {
	tileW   int32
	tileH   int32
	gap     int32
	cols    int
	originX int32
	originY int32
}

// appGridElementWidth uses the element's configured width when it has one and
// otherwise fills the content area, so the grid is not stuck in a narrow column
// on a 1280px panel.
func appGridElementWidth(element Element) int32 {
	return getElementWidth(element, screenWidth-element.X-24)
}

func newAppGridLayout(element Element, elemW int32) appGridLayout {
	layout := appGridLayout{tileW: appGridMinTileW, tileH: appGridTileH, gap: appGridGap, cols: 1}
	layout.originX = element.X + appGridPad
	layout.originY = element.Y + appGridHeadH
	usable := elemW - appGridPad*2
	if usable <= 0 {
		return layout
	}
	cols := int((usable + layout.gap) / (appGridMinTileW + layout.gap))
	if cols < 1 {
		cols = 1
	}
	layout.cols = cols
	layout.tileW = (usable - int32(cols-1)*layout.gap) / int32(cols)
	return layout
}

// appGridRows is how many tile rows fit in the element's height.
func appGridRows(element Element, elemH int32, layout appGridLayout) int {
	usable := elemH - appGridHeadH - appGridPad
	rows := int((usable + layout.gap) / (layout.tileH + layout.gap))
	if rows < 1 {
		rows = 1
	}
	return rows
}

// appGridFirstRow keeps the focused tile inside the visible window. Render and
// click hit-testing both use it, so a click can never land on the tile above
// the one the user aimed at.
func appGridFirstRow(focus, rows, total, cols int) int {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	if focus < 0 {
		focus = 0
	}
	totalRows := (total + cols - 1) / cols
	if totalRows <= rows {
		return 0
	}
	focusedRow := focus / cols
	first := 0
	if focusedRow >= rows {
		first = focusedRow - rows + 1
	}
	if maxFirst := totalRows - rows; first > maxFirst {
		first = maxFirst
	}
	return first
}

// appGridCellRect is the tile rect for index inside the scrolled window.
func appGridCellRect(layout appGridLayout, index, firstRow int) sdl.Rect {
	col := int32(index % layout.cols)
	row := int32(index/layout.cols) - int32(firstRow)
	return sdl.Rect{
		X: layout.originX + col*(layout.tileW+layout.gap),
		Y: layout.originY + row*(layout.tileH+layout.gap),
		W: layout.tileW,
		H: layout.tileH,
	}
}

// appGridEntries reads the published launcher rows for an element.
func appGridEntries(config *Config, element Element) []FileEntry {
	if config == nil {
		return nil
	}
	entries, _ := config.Variables.Custom[element.Variable].([]FileEntry)
	return entries
}

// appGridDisplayName strips the "(built-in)" suffix the list used to spell out
// in the row. Tiles carry the same information as a small dot instead.
func appGridDisplayName(name string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), "(built-in)"))
}

func appGridIsBuiltin(name string) bool {
	return strings.HasSuffix(strings.TrimSpace(name), "(built-in)")
}

// appGridGlyph is the letter drawn in a tile's badge: the first letter of the
// app's name, upper-cased so a lower-case shortcut still looks deliberate.
func appGridGlyph(name string) string {
	for _, r := range name {
		if r == ' ' || r == '\t' {
			continue
		}
		return strings.ToUpper(string(r))
	}
	return "?"
}

// appGridTileTint picks a stable colour for a tile that has no artwork. The
// same app keeps its colour across launches and neighbouring tiles rarely share
// one, so a launcher of initials still looks composed.
func appGridTileTint(name string) sdl.Color {
	palette := [...]sdl.Color{
		ColorAccent, ColorAccentSecondary, ColorSuccess, ColorInfo, ColorWarning, ColorDanger,
	}
	var h uint32 = 2166136261
	for _, r := range strings.ToLower(name) {
		h ^= uint32(r)
		h *= 16777619
	}
	return palette[h%uint32(len(palette))]
}

// appGridIndexAt returns the tile index under a click, or -1.
func appGridIndexAt(element Element, entries []FileEntry, mx, my int32) int {
	if len(entries) == 0 {
		return -1
	}
	elemW := appGridElementWidth(element)
	elemH := getElementHeight(element, 500)
	layout := newAppGridLayout(element, elemW)
	rows := appGridRows(element, elemH, layout)
	first := appGridFirstRow(focusedFileIndex, rows, len(entries), layout.cols)
	for i := range entries {
		cell := appGridCellRect(layout, i, first)
		if cell.Y+cell.H > element.Y+elemH || cell.Y < element.Y {
			continue
		}
		if mx >= cell.X && mx <= cell.X+cell.W && my >= cell.Y && my <= cell.Y+cell.H {
			return i
		}
	}
	return -1
}

// appGridMove navigates the grid. Left/Right walk the row, Up/Down jump a row;
// both clamp instead of wrapping so the focus cannot jump across the panel.
func appGridMove(config *Config, scene SceneConfig, dx, dy int) {
	element, ok := sceneDynamicListElement(scene)
	if !ok {
		return
	}
	entries := appGridEntries(config, element)
	n := len(entries)
	if n == 0 {
		return
	}
	if focusedFileIndex < 0 || focusedFileIndex >= n {
		focusedFileIndex = 0
		return
	}
	layout := newAppGridLayout(element, appGridElementWidth(element))
	cols := layout.cols
	if cols < 1 {
		cols = 1
	}
	row, col := focusedFileIndex/cols, focusedFileIndex%cols
	if dy != 0 {
		row += dy
		if row < 0 {
			row = 0
		}
	}
	if dx != 0 {
		col += dx
		if col < 0 {
			col = 0
		}
		if col > cols-1 {
			col = cols - 1
		}
	}
	idx := row*cols + col
	if idx >= n {
		idx = n - 1
	}
	if idx < 0 {
		idx = 0
	}
	if idx != focusedFileIndex {
		focusedFileIndex = idx
		PlayNavSound()
	}
}

// appGridKey handles a keyboard press while the launcher grid has focus. It
// reports whether the key was consumed.
func appGridKey(sym sdl.Keycode, config *Config) bool {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	scene := config.Scenes[currentSceneIndex]
	switch sym {
	case sdl.K_UP:
		appGridMove(config, scene, 0, -1)
	case sdl.K_DOWN:
		appGridMove(config, scene, 0, 1)
	case sdl.K_LEFT:
		appGridMove(config, scene, -1, 0)
	case sdl.K_RIGHT:
		appGridMove(config, scene, 1, 0)
	case sdl.K_RETURN, sdl.K_SPACE:
		appGridActivate(config)
	default:
		return false
	}
	return true
}

// appGridController is the controller twin of appGridKey.
func appGridController(button uint8, config *Config) bool {
	if currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	scene := config.Scenes[currentSceneIndex]
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		appGridMove(config, scene, 0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		appGridMove(config, scene, 0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		appGridMove(config, scene, -1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		appGridMove(config, scene, 1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		appGridActivate(config)
	default:
		return false
	}
	return true
}

// appGridActivate launches the focused tile.
func appGridActivate(config *Config) {
	scene := config.Scenes[currentSceneIndex]
	element, ok := sceneDynamicListElement(scene)
	if !ok {
		return
	}
	entries := appGridEntries(config, element)
	if focusedFileIndex < 0 || focusedFileIndex >= len(entries) {
		return
	}
	launchAppEntry(config, entries[focusedFileIndex])
}

// drawAppTileIcon draws the app's own artwork when it has some, and otherwise a
// tinted well holding the app's initial.
func drawAppTileIcon(r *sdl.Renderer, config *Config, cell sdl.Rect, entry FileEntry, name string, focused bool, medium *ttf.Font) {
	box := appIconBox
	x := cell.X + (cell.W-box)/2
	y := cell.Y + appIconTop

	if tex := appIconTexture(r, appEntryIconPath(entry)); tex != nil {
		if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
			// Fit inside the box without distorting the artwork; a wide or tall
			// icon stays centred in the same square as every other tile.
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

	// The initial keeps the tile's own colour while focused: the card border
	// and spine already say which tile is selected, and swapping the colour
	// would make every focused tile look the same.
	tint := appGridTileTint(name)
	wellAlpha := uint8(42)
	if focused {
		wellAlpha = 62
	}
	fillRoundedRect(r, x, y, box, box, 14, WithAlpha(tint, wellAlpha))
	strokeRoundedRect(r, x, y, box, box, 14, 1, WithAlpha(tint, 140))
	// The initial is sized to the well so it reads as artwork rather than a
	// small label left in the corner of a tile.
	glyphFont := medium
	if sized := getFontSized(config, "medium", int(box)*45/100); sized != nil {
		glyphFont = sized
	}
	glyph := appGridGlyph(name)
	gw, gh, _ := glyphFont.SizeUTF8(glyph)
	renderText(r, config, glyphFont, glyph, tint, x+(box-int32(gw))/2, y+(box-int32(gh))/2)
}

// appLabelKey identifies one wrapped tile name. The font pointer is part of the
// key because a different font means different metrics; ClearTextCache drops
// the cache whenever fonts or text sizes are reloaded.
type appLabelKey struct {
	name  string
	width int32
	lines int
	font  *ttf.Font
}

var (
	appLabelMu    sync.Mutex
	appLabelCache = map[appLabelKey][]string{}
)

const appLabelCacheMax = 256

// appTileLabelLines wraps a tile's name and remembers the result. A launcher
// frame used to measure every word of every visible tile, which cost more than
// drawing the tiles themselves; the same names wrap identically frame after
// frame, so one measurement per (name, width, lines, font) is enough.
func appTileLabelLines(font *ttf.Font, name string, width int32, maxLines int) []string {
	if font == nil || maxLines <= 0 {
		return nil
	}
	key := appLabelKey{name: name, width: width, lines: maxLines, font: font}
	appLabelMu.Lock()
	if lines, ok := appLabelCache[key]; ok {
		appLabelMu.Unlock()
		return lines
	}
	appLabelMu.Unlock()

	lines := wrapTextLines(font, name, width, maxLines)
	appLabelMu.Lock()
	if len(appLabelCache) >= appLabelCacheMax {
		appLabelCache = map[appLabelKey][]string{}
	}
	appLabelCache[key] = lines
	appLabelMu.Unlock()
	return lines
}

// clearAppLabelCache drops the wrapped names. Text metrics change with the
// theme's font or the accessibility text size, so this runs alongside the text
// texture cache.
func clearAppLabelCache() {
	appLabelMu.Lock()
	appLabelCache = map[appLabelKey][]string{}
	appLabelMu.Unlock()
}

// appTileLabelFont is the font a tile draws its name in. It scales with the
// accessibility text setting like every other font, so a large-text user gets
// fewer, bigger lines instead of text spilling out of the tile.
func appTileLabelFont(config *Config) *ttf.Font {
	if label := getFontSized(config, "small", appTileLabelSize); label != nil {
		return label
	}
	label, _ := getCachedFont(config, "small")
	return label
}

// drawAppTile renders one launcher tile: the icon (or initial), the app name
// underneath, and the focus treatment shared with the rest of the shell.
func drawAppTile(r *sdl.Renderer, config *Config, cell sdl.Rect, entry FileEntry, focused, hovered bool, labelFont, medium *ttf.Font) {
	drawCard(r, cell.X, cell.Y, cell.W, cell.H, 12)
	if focused {
		// Focus reads at a glance: a raised surface, an accent border and an
		// accent spine, not just a tint that darker themes hide.
		fillRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, ColorSurfaceRaised)
		fillRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, WithAlpha(accentColor, 70))
		strokeRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, 2, WithAlpha(accentColor, 235))
		fillRoundedRect(r, cell.X+2, cell.Y+12, 3, cell.H-24, 2, accentColor)
	} else if hovered {
		fillRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, WithAlpha(accentColor, 26))
		strokeRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, 1, WithAlpha(accentColor, 110))
	}

	name := appGridDisplayName(entry.Name)
	drawAppTileIcon(r, config, cell, entry, name, focused, medium)

	// A small accent dot marks JukaHub's own applets; external programs get no
	// marker, because their tile already carries the program's own icon.
	if appGridIsBuiltin(entry.Name) {
		fillCircle(r, cell.X+cell.W-13, cell.Y+13, 3, WithAlpha(accentColor, 200))
	}

	// Names wrap under the icon. They start at the same offset on every tile so
	// the grid keeps one baseline, and the number of lines follows the room the
	// tile really has (a large-text setting fits one taller line, not two that
	// spill out of the card).
	if labelFont == nil {
		return
	}
	lineH := int32(labelFont.Height() + 1)
	textTop := cell.Y + appIconTop + appIconBox + 8
	maxLines := int((cell.Y + cell.H - 6 - textTop) / lineH)
	if maxLines < 1 {
		maxLines = 1
	}
	if maxLines > 2 {
		maxLines = 2
	}
	lines := appTileLabelLines(labelFont, name, cell.W-16, maxLines)
	for li, line := range lines {
		lw, _, _ := labelFont.SizeUTF8(line)
		renderText(r, config, labelFont, line, ColorTextPrimary(),
			cell.X+(cell.W-int32(lw))/2, textTop+int32(li)*lineH)
	}
}

// renderAppGridEmpty fills a launcher that found nothing: a glyph, what
// happened, and where apps come from.
func renderAppGridEmpty(renderer *sdl.Renderer, config *Config, element Element, elemW, elemH int32, font *ttf.Font) {
	glyphBox := sdl.Rect{X: element.X + (elemW-46)/2, Y: element.Y + elemH/2 - 76, W: 46, H: 46}
	DrawHomeIcon(renderer, "apps", glyphBox, false)

	title := ellipsize(font, "No launchable apps yet", elemW-32)
	tw, th, _ := font.SizeUTF8(title)
	renderText(renderer, config, font, title, ColorTextPrimary(),
		element.X+(elemW-int32(tw))/2, element.Y+elemH/2-12)
	hint := ellipsize(font, "Drop an app in the apps folder next to JukaHub, then refresh", elemW-32)
	hw, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, ColorTextTertiary(),
		element.X+(elemW-int32(hw))/2, element.Y+elemH/2+int32(th))
}

// renderAppGrid draws the launcher: a header with the app count, then the grid
// of tiles.
func renderAppGrid(renderer *sdl.Renderer, config *Config, element Element) {
	elemW := appGridElementWidth(element)
	elemH := getElementHeight(element, 500)
	entries := appGridEntries(config, element)

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	medium, _ := getCachedFont(config, "medium")
	if medium == nil {
		medium = font
	}
	labelFont := appTileLabelFont(config)
	if labelFont == nil {
		labelFont = font
	}

	drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(220), accentColor)

	if len(entries) == 0 {
		renderAppGridEmpty(renderer, config, element, elemW, elemH, font)
		return
	}

	layout := newAppGridLayout(element, elemW)
	rows := appGridRows(element, elemH, layout)
	first := appGridFirstRow(focusedFileIndex, rows, len(entries), layout.cols)

	// A quiet count sits in the reserved header band: the grid opens on a wall
	// of tiles, so the panel says how much is in it without scrolling first.
	count := fmt.Sprintf("%d apps", len(entries))
	if len(entries) == 1 {
		count = "1 app"
	}
	renderText(renderer, config, labelFont, count, ColorTextTertiary(),
		element.X+appGridPad, element.Y+9)

	hovered := -1
	if mouseX >= element.X && mouseX <= element.X+elemW && mouseY >= element.Y && mouseY <= element.Y+elemH {
		hovered = appGridIndexAt(element, entries, mouseX, mouseY)
	}

	renderWithClip(renderer, element.X+2, element.Y+appGridHeadH-4, elemW-4, elemH-appGridHeadH, func(r *sdl.Renderer) {
		for i, entry := range entries {
			cell := appGridCellRect(layout, i, first)
			// Rows are drawn whole: a card sliced by the panel edge reads as a
			// rendering fault, and the scrollbar already says there is more.
			// The focused tile is the exception: in a panel too short for one row
			// it still has to be visible, so it is drawn and clipped.
			if cell.Y < element.Y+appGridHeadH || (cell.Y+cell.H > element.Y+elemH-4 && i != focusedFileIndex) {
				continue
			}
			drawAppTile(r, config, cell, entry, i == focusedFileIndex, i == hovered, labelFont, medium)
		}
	})

	totalRows := (len(entries) + layout.cols - 1) / layout.cols
	if totalRows > rows && rows > 0 {
		thumbFrac := float64(rows) / float64(totalRows)
		scrollFrac := 0.0
		if totalRows-rows > 0 {
			scrollFrac = float64(first) / float64(totalRows-rows)
		}
		drawScrollbar(renderer, element.X+elemW-6, element.Y+appGridHeadH, 3,
			elemH-appGridHeadH-appGridPad, thumbFrac, scrollFrac)
	}
}
