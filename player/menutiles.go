package main

// menutiles.go — the classic Tools menu drawn as a launcher page.
//
// The config's "Misc" scene (shown as "Tools") is a menu of shortcuts: every
// element is a button that opens another screen. Rendered as authored it was a
// wall of wide, ragged buttons with no icons and no title, which reads as a
// leftover from an older shell next to the Home and Apps grids.
//
// Instead of asking every config author to hand-place tiles, the scene is laid
// out here, once, when it is entered: grouped into sections, sized to the
// content panel, and given the same icon tiles the Apps launcher uses. The
// element order never changes, so focus indices and the persisted per-scene
// focus keep working; only X/Y/width/height/style/icon are rewritten.
//
// Two rules keep this honest:
//   - The layout is written into the in-memory config, so a save would leak it
//     into the user's file. saveConfig detaches first (menuTilesDetachForSave)
//     and reattaches after, so jukaconfig.json keeps the authored geometry.
//   - When the tiles cannot fit the panel (a small screen with a long menu),
//     the layout is refused and the scene renders exactly as authored. A page
//     that does not fit is worse than the buttons it replaced.

import (
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// menuTilesStyle marks the buttons this file re-laid out as tiles.
	menuTilesStyle = "menutile"

	// Section titles, in display order. A menu that contains only one kind of
	// tool shows only that section.
	menuTilesSystem      = "SYSTEM"
	menuTilesDiagnostics = "DIAGNOSTICS"
	menuTilesUpdates     = "UPDATES & TRANSFER"
	menuTilesMore        = "MORE"

	// Tile sizing. A tile narrower than menuTilesMinW cannot hold a two-line
	// label at the theme's tile font; below menuTilesMinH the icon well and the
	// label collide.
	menuTilesMinW = int32(188)
	menuTilesMinH = int32(88)
	menuTilesMaxH = int32(132)
	// menuTileLabelSize is the pixel size of a tile's name: smaller than the
	// theme's list font so two wrapped lines fit under the icon, the same trade
	// the Apps launcher makes.
	menuTileLabelSize = 20
	// menuTitleSize is the page title's pixel size: a page header should read
	// as a title, not as one more list row.
	menuTitleSize    = 28
	menuTilesMinCols = 2
	menuTilesMaxCols = 6
)

var menuTilesSectionOrder = []string{
	menuTilesSystem,
	menuTilesDiagnostics,
	menuTilesUpdates,
	menuTilesMore,
}

// menuTilesSection is one group of tiles and the heading drawn above it. y is
// absolute screen space, like the tile rects.
type menuTilesSection struct {
	title string
	y     int32
	elems []int
}

// menuTilesLayout is everything the decor needs to draw a page and the tiles
// need to be placed. rects is keyed by element index so an element that is not
// a button stays where the config put it.
type menuTilesLayout struct {
	panel    sdl.Rect
	titleY   int32
	subY     int32
	bodyTop  int32
	cols     int
	tileW    int32
	tileH    int32
	gap      int32
	sections []menuTilesSection
	rects    map[int]sdl.Rect
}

// menuTilesScene reports whether a scene is the classic Tools menu: a page of
// buttons that each open another screen. A scene that still uses the tile home
// layout, or that carries a list/text element, keeps the generic renderer.
func menuTilesScene(scene SceneConfig) bool {
	if scene.Layout != "" || len(scene.Elements) < 4 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(scene.Name)) {
	case "misc", "tools":
	default:
		return false
	}
	buttons := 0
	for _, elem := range scene.Elements {
		if elem.Type != "button" || strings.TrimSpace(elem.Trigger) == "" {
			return false
		}
		buttons++
	}
	return buttons >= 4
}

// menuTilesGroup sorts a tool into a section by what it does, not by where it
// happens to sit in the config. The keywords are matched against the label and
// the trigger so a custom menu ("Fan Control" → change_scene:Fan) still lands
// somewhere sensible instead of being dropped.
func menuTilesGroup(elem Element) string {
	key := strings.ToLower(elem.Text + " " + elem.Trigger + " " + elem.TriggerTarget)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(key, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("terminal", "cron", "task", "playtime", "achievement", "log", "cache", "clean", "setting"):
		return menuTilesSystem
	case has("hardware", "monitor", "chip", "cpu", "temperature", "disk", "storage", "space",
		"speed", "benchmark", "bench", "memory", "process", "battery"):
		return menuTilesDiagnostics
	case has("patch", "update", "ftp", " ip", "share", "transfer", "ssh", "companion",
		"package", "netplay", "download"):
		return menuTilesUpdates
	}
	return menuTilesMore
}

// menuTilesIcon names the glyph a tool draws in its tile. An empty result lets
// the launcher's own label/trigger mapping decide.
func menuTilesIcon(elem Element) string {
	key := strings.ToLower(elem.Text + " " + elem.Trigger + " " + elem.TriggerTarget)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(key, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("terminal"):
		return "terminal"
	case has("cron", "task", "schedul"):
		return "clock"
	case has("speed", "bandwidth", "latency"):
		return "speed"
	case has("benchmark", "bench", "score"):
		return "chart"
	case has("hardware", "monitor", "chip", "cpu", "temperature"):
		return "chip"
	case has("disk", "storage", "space"):
		return "disk"
	case has("log"):
		return "logs"
	case has("cache", "clean"):
		return "cache"
	case has("patch", "update", "upgrade"):
		return "update"
	case has("playtime", "achievement"):
		return "star"
	case has("ftp", " ip", "share", "transfer", "ssh", "network", "net"):
		return "network"
	case has("setting"):
		return "gear"
	case has("package", "store"):
		return "box"
	case has("file", "explorer"):
		return "files"
	case has("chat", "community", "support"):
		return "chat"
	case has("tv", "stream", "media", "video", "play"):
		return "tv"
	}
	return ""
}

// menuTilesLayoutFor measures the page. It is deterministic: the same scene and
// screen always produce the same rects, which is what lets the render path and
// the geometry written at scene entry agree without sharing state.
func menuTilesLayoutFor(config *Config, scene SceneConfig, elems []Element) (menuTilesLayout, bool) {
	if screenWidth <= 0 || screenHeight <= 0 {
		return menuTilesLayout{}, false
	}
	var l menuTilesLayout

	top := HomeTopBarH + 8
	bottom := screenHeight - bottomBarHeight(scene.Name) - 16
	l.panel = sdl.Rect{X: 24, Y: top, W: screenWidth - 48, H: bottom - top}
	if l.panel.W < 260 || l.panel.H < 220 {
		return menuTilesLayout{}, false
	}

	small, _ := getCachedFont(config, "small")
	title := getFontSized(config, "medium", menuTitleSize)
	if title == nil {
		title, _ = getCachedFont(config, "medium")
	}
	if title == nil {
		title = small
	}
	smallH, titleH := int32(28), int32(28)
	if small != nil {
		smallH = int32(small.Height())
	}
	if title != nil {
		titleH = int32(title.Height())
	}

	padX := SpaceXL
	l.titleY = l.panel.Y + SpaceLG + 2
	// Title band: the glyph well is one title line tall, the subtitle sits
	// directly beneath it.
	l.subY = l.titleY + titleH + 12
	l.bodyTop = l.subY + smallH + 18

	sectionH := int32(28)
	if f := getFontSized(config, "small", 15); f != nil {
		sectionH = int32(f.Height()) + 12
		if sectionH < 26 {
			sectionH = 26
		}
	}

	// Group the buttons, preserving the config's order inside each group.
	grouped := make(map[string][]int, len(menuTilesSectionOrder))
	for i, elem := range elems {
		if elem.Type != "button" {
			continue
		}
		group := menuTilesGroup(elem)
		grouped[group] = append(grouped[group], i)
	}
	sections := make([]menuTilesSection, 0, len(menuTilesSectionOrder))
	for _, title := range menuTilesSectionOrder {
		if len(grouped[title]) > 0 {
			sections = append(sections, menuTilesSection{title: title, elems: grouped[title]})
		}
	}
	if len(sections) == 0 {
		return menuTilesLayout{}, false
	}

	bodyW := l.panel.W - 2*padX
	l.gap = SpaceMD + 2
	availH := l.panel.Y + l.panel.H - 16 - l.bodyTop
	if bodyW <= 0 || availH <= 0 {
		return menuTilesLayout{}, false
	}

	rowsFor := func(cols int) int {
		rows := 0
		for _, s := range sections {
			rows += (len(s.elems) + cols - 1) / cols
		}
		return rows
	}

	// Columns come from the width first (a tile narrower than a readable label
	// is not worth showing), then widen only if the rows would not fit the
	// panel height.
	cols := int((bodyW + l.gap) / (menuTilesMinW + l.gap))
	if cols < menuTilesMinCols {
		cols = menuTilesMinCols
	}
	tileH := int32(0)
	chosen := 0
	for c := cols; c <= menuTilesMaxCols; c++ {
		rows := rowsFor(c)
		if rows <= 0 {
			continue
		}
		room := availH - int32(len(sections))*sectionH - int32(rows-1)*l.gap
		if room <= 0 {
			continue
		}
		h := room / int32(rows)
		if h > menuTilesMaxH {
			h = menuTilesMaxH
		}
		tileH, chosen = h, c
		if h >= menuTilesMinH {
			break
		}
	}
	if chosen == 0 || tileH < menuTilesMinH {
		// Even the widest grid cannot hold this menu: leave the authored
		// buttons alone rather than draw tiles over the footer.
		return menuTilesLayout{}, false
	}

	l.cols = chosen
	l.tileH = tileH
	l.tileW = (bodyW - int32(l.cols-1)*l.gap) / int32(l.cols)
	l.rects = make(map[int]sdl.Rect, len(elems))

	y := l.bodyTop
	for si := range sections {
		sec := &sections[si]
		sec.y = y
		y += sectionH
		for k, idx := range sec.elems {
			col := int32(k % l.cols)
			row := int32(k / l.cols)
			l.rects[idx] = sdl.Rect{
				X: l.panel.X + padX + col*(l.tileW+l.gap),
				Y: y + row*(l.tileH+l.gap),
				W: l.tileW,
				H: l.tileH,
			}
		}
		rows := int32((len(sec.elems) + l.cols - 1) / l.cols)
		y += rows*l.tileH + (rows-1)*l.gap + l.gap
	}
	l.sections = sections
	return l, true
}

// menuTilesSnapshot keeps the authored geometry of the scene this file has
// re-laid out, so a config save can write the file the user authored. first
// pins the element array it was taken from: after a config reload the scene is
// a different slice, and restoring the old geometry into it would scramble the
// file the author just wrote.
var menuTilesSnapshot struct {
	scene int
	elems []Element
	first *Element
	valid bool
}

// menuTilesApply re-lays the scene at idx out as tiles. It is idempotent and
// safe to call for any scene: anything that is not a Tools menu is left alone.
func menuTilesApply(config *Config, idx int) {
	if config == nil {
		return
	}
	// A previously restyled scene goes back to its authored geometry first: the
	// snapshot only ever holds one scene, and a stale one must not be lost.
	menuTilesRestore(config)
	if idx < 0 || idx >= len(config.Scenes) {
		return
	}
	scene := config.Scenes[idx]
	if !menuTilesScene(scene) {
		return
	}
	layout, ok := menuTilesLayoutFor(config, scene, scene.Elements)
	if !ok || len(layout.rects) == 0 {
		return
	}

	authored := make([]Element, len(scene.Elements))
	copy(authored, scene.Elements)
	menuTilesSnapshot.scene = idx
	menuTilesSnapshot.elems = authored
	menuTilesSnapshot.first = &config.Scenes[idx].Elements[0]
	menuTilesSnapshot.valid = true

	for i := range config.Scenes[idx].Elements {
		rect, placed := layout.rects[i]
		if !placed {
			continue
		}
		elem := &config.Scenes[idx].Elements[i]
		elem.X, elem.Y = rect.X, rect.Y
		elem.Width = StringOrInt(strconv.Itoa(int(rect.W)))
		elem.Height = StringOrInt(strconv.Itoa(int(rect.H)))
		elem.Style = menuTilesStyle
		if icon := menuTilesIcon(*elem); icon != "" {
			elem.Icon = icon
		}
	}
}

// menuTilesRestore puts the authored geometry back. Only the fields this file
// rewrites are restored, so runtime edits elsewhere in the element survive.
func menuTilesRestore(config *Config) {
	if !menuTilesSnapshot.valid {
		return
	}
	snap := menuTilesSnapshot
	menuTilesSnapshot.valid = false
	if config == nil || snap.scene < 0 || snap.scene >= len(config.Scenes) {
		return
	}
	elems := config.Scenes[snap.scene].Elements
	if len(elems) != len(snap.elems) || len(elems) == 0 || &elems[0] != snap.first {
		return
	}
	for i := range elems {
		elems[i].X = snap.elems[i].X
		elems[i].Y = snap.elems[i].Y
		elems[i].Width = snap.elems[i].Width
		elems[i].Height = snap.elems[i].Height
		elems[i].Style = snap.elems[i].Style
		elems[i].Icon = snap.elems[i].Icon
	}
}

// menuTilesDetachForSave restores the authored geometry for the duration of a
// config write and returns a function that re-applies the page. The generated
// tile layout must never reach jukaconfig.json.
func menuTilesDetachForSave(config *Config) func() {
	if !menuTilesSnapshot.valid {
		return func() {}
	}
	idx := menuTilesSnapshot.scene
	menuTilesRestore(config)
	return func() {
		if config != nil && idx >= 0 && idx < len(config.Scenes) {
			menuTilesApply(config, idx)
		}
	}
}

// renderMenuTilesDecor draws the page header and the section headings. It runs
// before the element loop, and the tiles are placed below the bands drawn here,
// so the page reads as one grid rather than headings floating over buttons.
func renderMenuTilesDecor(renderer *sdl.Renderer, config *Config, scene SceneConfig) {
	if renderer == nil || config == nil {
		return
	}
	layout, ok := menuTilesLayoutFor(config, scene, scene.Elements)
	if !ok {
		return
	}
	titleFont := getFontSized(config, "medium", menuTitleSize)
	if titleFont == nil {
		titleFont, _ = getCachedFont(config, "medium")
	}
	small, _ := getCachedFont(config, "small")
	if titleFont == nil {
		titleFont = small
	}

	padX := SpaceXL
	title := sceneDisplayName(scene.Name)
	if strings.TrimSpace(title) == "" {
		title = "Tools"
	}

	x := layout.panel.X + padX
	y := layout.titleY
	if titleFont != nil {
		box := int32(titleFont.Height()) + 10
		tint := appGridTileTint(title)
		fillRoundedRect(renderer, x, y, box, box, 12, WithAlpha(tint, 44))
		strokeRoundedRect(renderer, x, y, box, box, 12, 1, WithAlpha(tint, 150))
		DrawHomeIcon(renderer, "tools", insetRect(sdl.Rect{X: x, Y: y, W: box, H: box}, 8), false)
		x += box + 14
		renderText(renderer, config, titleFont, title, ColorTextPrimary(), x, y+4)
	}

	// The address every transfer row needs, and the state of the network the
	// tiles below depend on, sits on the title line where it is read first.
	if small != nil {
		status, col := "Offline", ColorToastWarn
		if ip := toolsIPAddress(); ip != "" {
			status, col = "WiFi · "+ip, ColorTextTertiary()
		}
		if sw, _, err := small.SizeUTF8(status); err == nil {
			renderText(renderer, config, small, status, col,
				layout.panel.X+layout.panel.W-padX-int32(sw), y+8)
		}
		sub := "Diagnostics, system tools and transfers"
		if !IsOnline() {
			sub = "Offline — connect to WiFi to transfer files"
		}
		renderText(renderer, config, small, sub, ColorTextTertiary(), layout.panel.X+padX, layout.subY)
	}

	bodyW := layout.panel.W - 2*padX
	for _, sec := range layout.sections {
		renderMenuSectionLabel(renderer, config, sec.title, menuTilesSectionGlyph(sec.title),
			layout.panel.X+padX, sec.y, bodyW)
	}
}

// menuTilesSectionGlyph pairs a section with a representative glyph, so a
// heading is recognisable before its small label is read.
func menuTilesSectionGlyph(title string) string {
	switch strings.ToUpper(strings.TrimSpace(title)) {
	case menuTilesSystem:
		return "terminal"
	case menuTilesDiagnostics:
		return "chip"
	case menuTilesUpdates:
		return "network"
	}
	return "apps"
}

// menuTileLabelFit picks the largest label size whose two lines still fit the
// space under the icon. A theme whose "small" font runs tall (30px glyphs at a
// 20px request) would otherwise wrap to one clipped line while its neighbours
// wrapped to two.
func menuTileLabelFit(config *Config, avail int32) (*ttf.Font, int32) {
	for _, size := range []int{22, 20, 18, 16} {
		font := getFontSized(config, "small", size)
		if font == nil {
			continue
		}
		lineH := int32(font.Height() + 1)
		if lineH*2 <= avail || size == 16 {
			return font, lineH
		}
	}
	font := appTileLabelFont(config)
	if font == nil {
		return nil, 0
	}
	return font, int32(font.Height() + 1)
}

// renderMenuSectionLabel draws a group heading: a tinted glyph chip, the
// uppercase title, and a hairline running to the panel edge.
func renderMenuSectionLabel(renderer *sdl.Renderer, config *Config, title, glyph string, x, y, w int32) {
	font := getFontSized(config, "small", 15)
	if font == nil {
		return
	}
	label := strings.ToUpper(title)
	_, th, _ := font.SizeUTF8(label)
	textY := y + 6
	textX := x
	if th > 0 {
		chip := int32(th) + 12
		chipY := y + (int32(th)+14-chip)/2
		tint := appGridTileTint(label)
		fillRoundedRect(renderer, x, chipY, chip, chip, chip/4, WithAlpha(tint, 38))
		strokeRoundedRect(renderer, x, chipY, chip, chip, chip/4, 1, WithAlpha(tint, 130))
		drawTileIcon(renderer, x+chip/2, chipY+chip/2, chip-6, glyph, tint, WithAlpha(tint, 38))
		textX += chip + 10
	}
	tw, _, _ := font.SizeUTF8(label)
	renderText(renderer, config, font, label, ColorTextTertiary(), textX, textY)
	if lx := textX + int32(tw) + 12; lx < x+w {
		renderer.SetDrawColor(ColorBorder.R, ColorBorder.G, ColorBorder.B, 80)
		renderer.FillRect(&sdl.Rect{X: lx, Y: textY + int32(th)/2, W: x + w - lx, H: 1})
	}
}

// renderMenuTile draws one shortcut tile: the icon in a tinted well, the tool's
// name wrapped underneath, the focus ring the rest of the shell uses, and the
// same eased press dip as every other button.
func renderMenuTile(renderer *sdl.Renderer, config *Config, elem Element, selected, hovered bool, hoverProgress float64, pressed bool) {
	if renderer == nil {
		return
	}
	w := getElementWidth(elem, 200)
	h := getElementHeight(elem, 110)
	if w <= 0 || h <= 0 {
		return
	}
	r := TileRadius

	ox, oy := elem.X, elem.Y
	if pressed {
		age := float64(sdl.GetTicks64() - pressStartTime)
		off := int32(math.Round(float64(PressOffset) * uiPressProgress(config, age, float64(pressDuration))))
		ox += off
		oy += off
	}

	focusCol := focusColorForAccent(accentColor)
	if selected {
		fillRoundedRect(renderer, ox-FocusGlow, oy-FocusGlow, w+2*FocusGlow, h+2*FocusGlow,
			r+FocusGlow, WithAlpha(focusCol, 24))
		strokeRoundedRect(renderer, ox-FocusRing, oy-FocusRing, w+2*FocusRing, h+2*FocusRing,
			r+FocusRing, FocusRing, focusCol)
	} else if hoverProgress > 0.01 {
		a := uint8(80 * uiClamp01(hoverProgress))
		strokeRoundedRect(renderer, ox-HoverRing, oy-HoverRing, w+2*HoverRing, h+2*HoverRing,
			r+HoverRing, HoverRing, WithAlpha(focusCol, a))
	}

	base := HomeCardColor()
	border := HomeBorderColor()
	if selected {
		base = HomeCardFocusColor()
		border = WithAlpha(focusCol, 200)
	} else if hovered {
		border = WithAlpha(focusCol, 120)
	}
	fillRoundedRect(renderer, ox, oy, w, h, r, base)
	strokeRoundedRect(renderer, ox, oy, w, h, r, 1, border)

	tint := appGridTileTint(elem.Text)
	iconCol := tint
	if selected {
		iconCol = focusCol
	}
	// The icon well leaves room for a two-line label: a name like "Playtime &
	// Achievements" is cut to one line by a bigger well, which reads as a bug
	// next to neighbours that wrap.
	box := int32(float64(h) * 0.32)
	if box > 44 {
		box = 44
	}
	if box < 32 {
		box = 32
	}
	wellX := ox + (w-box)/2
	wellY := oy + 12
	fillRoundedRect(renderer, wellX, wellY, box, box, 14, WithAlpha(tint, 40))
	strokeRoundedRect(renderer, wellX, wellY, box, box, 14, 1, WithAlpha(tint, 150))
	iconSize := box - 12
	if iconSize < 20 {
		iconSize = 20
	}
	drawTileIcon(renderer, wellX+box/2, wellY+box/2, iconSize, tileIconKind(elem), iconCol, WithAlpha(tint, 40))

	top := wellY + box + 4
	avail := oy + h - 8 - top
	if strings.TrimSpace(elem.Text) == "" {
		return
	}
	labelFont, lineH := menuTileLabelFit(config, avail)
	if labelFont == nil || lineH <= 0 {
		return
	}
	maxLines := int(avail / lineH)
	if maxLines < 1 {
		maxLines = 1
	}
	if maxLines > 2 {
		maxLines = 2
	}
	textCol := ColorTextPrimary()
	for li, line := range appTileLabelLines(labelFont, elem.Text, w-14, maxLines) {
		lw, _, _ := labelFont.SizeUTF8(line)
		renderText(renderer, config, labelFont, line, textCol, ox+(w-int32(lw))/2, top+int32(li)*lineH)
	}
}
