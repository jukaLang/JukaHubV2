package main

import (
	"fmt"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// appgrid.go draws the Apps scene as a launcher grid of tiles instead of the
// old one-column list. The entries are still the "app_entries" list from
// apps.go; only the presentation and navigation change, so scan/launch code
// keeps working and user configs that still declare a dynamiclist on
// app_entries get the grid too.

const (
	appGridMinTileW = int32(150)
	appGridTileH    = int32(118)
	appGridGap      = int32(14)
	appGridPad      = int32(14)
	appGridHeadH    = int32(30)
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

// renderAppGrid draws the launcher: a header line with the app count, then
// rounded tiles with a letter badge and the app name.
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

	drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(220), accentColor)

	if len(entries) == 0 {
		title := "No launchable apps found"
		hint := "Drop programs in the apps folder next to JukaHub"
		tw, th, _ := font.SizeUTF8(title)
		renderText(renderer, config, font, title, ColorTextPrimary(),
			element.X+(elemW-int32(tw))/2, element.Y+elemH/2-int32(th))
		hw, _, _ := font.SizeUTF8(hint)
		renderText(renderer, config, font, hint, ColorTextTertiary(),
			element.X+(elemW-int32(hw))/2, element.Y+elemH/2+6)
		return
	}

	layout := newAppGridLayout(element, elemW)
	rows := appGridRows(element, elemH, layout)
	first := appGridFirstRow(focusedFileIndex, rows, len(entries), layout.cols)

	// A quiet count sits in the panel's bottom-right corner: the header belongs
	// to the scene label above, and the corner keeps it clear of the tiles.
	count := fmt.Sprintf("%d apps", len(entries))
	if len(entries) == 1 {
		count = "1 app"
	}
	cw, _, _ := font.SizeUTF8(count)
	renderText(renderer, config, font, count, ColorTextTertiary(),
		element.X+elemW-int32(cw)-appGridPad-2, element.Y+elemH-int32(font.Height())-8)

	hovered := -1
	if mouseX >= element.X && mouseX <= element.X+elemW && mouseY >= element.Y && mouseY <= element.Y+elemH {
		hovered = appGridIndexAt(element, entries, mouseX, mouseY)
	}

	renderWithClip(renderer, element.X+2, element.Y+appGridHeadH-4, elemW-4, elemH-appGridHeadH, func(r *sdl.Renderer) {
		for i, entry := range entries {
			cell := appGridCellRect(layout, i, first)
			if cell.Y+cell.H < element.Y+appGridHeadH || cell.Y > element.Y+elemH {
				continue
			}
			focused := i == focusedFileIndex

			drawCard(r, cell.X, cell.Y, cell.W, cell.H, 12)
			if focused {
				// Focus reads at a glance: a raised surface, an accent border and
				// an accent spine, not just a tint that darker themes hide.
				fillRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, ColorSurfaceRaised)
				fillRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, WithAlpha(accentColor, 70))
				strokeRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, 2, WithAlpha(accentColor, 235))
				fillRoundedRect(r, cell.X+2, cell.Y+12, 3, cell.H-24, 2, accentColor)
			} else if i == hovered {
				fillRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, WithAlpha(accentColor, 26))
				strokeRoundedRect(r, cell.X, cell.Y, cell.W, cell.H, 12, 1, WithAlpha(accentColor, 110))
			}

			name := appGridDisplayName(entry.Name)

			// Letter badge.
			badge := int32(38)
			bx := cell.X + 12
			by := cell.Y + 12
			badgeFill := WithAlpha(accentColor, 34)
			if focused {
				badgeFill = WithAlpha(accentColor, 90)
			}
			fillRoundedRect(r, bx, by, badge, badge, 10, badgeFill)
			glyph := appGridGlyph(name)
			gw, gh, _ := medium.SizeUTF8(glyph)
			renderText(r, config, medium, glyph, ColorTextPrimary(),
				bx+(badge-int32(gw))/2, by+(badge-int32(gh))/2)

			// A small accent dot marks JukaHub's own applets; external programs
			// get no marker.
			if appGridIsBuiltin(entry.Name) {
				fillCircle(r, cell.X+cell.W-14, cell.Y+14, 3, WithAlpha(accentColor, 200))
			}

			// Word wrap keeps names readable instead of cutting mid-word
			// ("Visual Studi / o Code").
			nameY := cell.Y + 56
			for li, line := range wrapTextLines(font, name, cell.W-22, 2) {
				renderText(r, config, font, line, ColorTextPrimary(), cell.X+12, nameY+int32(li)*int32(font.Height()+1))
			}
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
