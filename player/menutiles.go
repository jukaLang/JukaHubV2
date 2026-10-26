package main

// menutiles.go — the classic menu scenes drawn as launcher pages.
//
// The config's "Misc" scene (shown as "Tools") and its "Settings" scene are
// menus of shortcuts: every element is a button that opens another screen.
// Rendered as authored they were a wall of wide, ragged buttons with no icons
// and no title, which reads as a leftover from an older shell next to the Home
// and Apps grids.
//
// Instead of asking every config author to hand-place tiles, the scene is laid
// out here, once, when it is entered: grouped into sections, sized to the
// content panel, and given the same icon tiles the Apps launcher uses. Each
// menu's sections, grouping rules and glyphs live in a menuPageProfile, so a
// new menu is a profile rather than another copy of this layout. The element
// order never changes, so focus indices and the persisted per-scene focus keep
// working; only X/Y/width/height/style/icon are rewritten.
//
// Two rules keep this honest:
//   - The layout is written into the in-memory config, so a save would leak it
//     into the user's file. saveConfig detaches first (menuTilesDetachForSave)
//     and reattaches after, so jukaconfig.json keeps the authored geometry.
//   - When the tiles cannot fit the panel (a small screen with a long menu),
//     the layout is refused and the scene renders exactly as authored. A page
//     that does not fit is worse than the buttons it replaced.

import (
	"sort"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
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

// The Settings menu is the same page of shortcuts as Tools, so it uses the same
// layout and only needs its own grouping: its rows are categories of settings,
// not tools.
const (
	menuSettingsAppearance = "APPEARANCE"
	menuSettingsSound      = "SOUND"
	menuSettingsNetwork    = "CONNECTIVITY"
	menuSettingsSystem     = "SYSTEM"
)

var menuSettingsSectionOrder = []string{
	menuSettingsAppearance,
	menuSettingsSound,
	menuSettingsNetwork,
	menuSettingsSystem,
}

// menuPageProfile is how one menu is turned into a launcher page: which
// sections it shows, which rows belong in them, the glyph each row and each
// heading gets, and the line under the title. Adding a menu is one profile, not
// another copy of the layout.
//
// group and icon are matched against the label and the trigger, so a row the
// config author invents still lands somewhere sensible instead of being
// dropped: menuTilesLayoutFor falls back to the profile's last section.
type menuPageProfile struct {
	sections    []string
	group       func(Element) string
	icon        func(Element) string
	sectionIcon func(string) string
	subtitle    string
	// offlineSubtitle replaces subtitle when the device has no link. It is
	// empty on a page that has nothing to do with the network.
	offlineSubtitle string
	// glyph names the header icon (a DrawHomeIcon key) and status is the
	// header's right-hand read-out, nil when the page has none.
	glyph  string
	status func() (string, sdl.Color)
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

// menuPageProfileFor decides whether a scene is a page of shortcut buttons and,
// if so, which profile lays it out. A scene that still uses the tile home
// layout, or that carries a list/text element, keeps the generic renderer.
func menuPageProfileFor(scene SceneConfig) (menuPageProfile, bool) {
	if scene.Layout != "" || len(scene.Elements) < 4 {
		return menuPageProfile{}, false
	}
	profile, ok := menuPageProfileNamed(scene.Name)
	if !ok {
		return menuPageProfile{}, false
	}
	buttons := 0
	for _, elem := range scene.Elements {
		if elem.Type != "button" || strings.TrimSpace(elem.Trigger) == "" {
			return menuPageProfile{}, false
		}
		buttons++
	}
	if buttons < 4 {
		return menuPageProfile{}, false
	}
	return profile, true
}

// menuPageProfileNamed resolves a menu by its scene name alone. The body needs
// four shortcuts to be laid out as a page, but the header belongs to the scene
// whether or not its body qualifies yet, so the two lookups are separate.
func menuPageProfileNamed(name string) (menuPageProfile, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "misc", "tools":
		return toolsMenuPage, true
	case "settings":
		return settingsMenuPage, true
	}
	return menuPageProfile{}, false
}

// menuTilesScene reports whether a scene is drawn as a launcher page.
func menuTilesScene(scene SceneConfig) bool {
	_, ok := menuPageProfileFor(scene)
	return ok
}

// toolsMenuPage is the Tools page: system tools, diagnostics, transfers.
var toolsMenuPage = menuPageProfile{
	sections:        menuTilesSectionOrder,
	group:           toolsMenuGroup,
	icon:            toolsMenuIcon,
	sectionIcon:     menuTilesSectionGlyph,
	subtitle:        "Diagnostics, system tools and transfers",
	offlineSubtitle: "Offline — connect to WiFi to transfer files",
	glyph:           "tools",
	status:          toolsPageStatus,
}

// settingsMenuPage is the Settings menu: the same page, grouped by what each
// category changes.
var settingsMenuPage = menuPageProfile{
	sections:    menuSettingsSectionOrder,
	group:       settingsMenuGroup,
	icon:        settingsMenuIcon,
	sectionIcon: settingsSectionGlyph,
	subtitle:    "Appearance, connectivity and system preferences",
	glyph:       "settings",
}

// toolsPageStatus is the Tools header's read-out: whether transfers can reach
// the device at all, and where.
func toolsPageStatus() (string, sdl.Color) {
	if ip := toolsIPAddress(); ip != "" {
		return "WiFi · " + ip, ColorTextTertiary()
	}
	return "Offline", ColorToastWarn
}

// toolsMenuGroup sorts a tool into a section by what it does, not by where it
// happens to sit in the config. The keywords are matched against the label and
// the trigger so a custom menu ("Fan Control" → change_scene:Fan) still lands
// somewhere sensible instead of being dropped.
func toolsMenuGroup(elem Element) string {
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

// settingsMenuGroup sorts a settings row by what it changes. The fallback is
// SYSTEM, so a category the config author adds later is still reachable.
func settingsMenuGroup(elem Element) string {
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
	case has("appearance", "theme", "wallpaper", "display", "layout", "colour", "color",
		"font", "brightness", "animation", "icon", "splash", "boot", "led", "screensaver"):
		return menuSettingsAppearance
	case has("sound", "audio", "volume", "music", "equalizer", " eq", "eq "):
		return menuSettingsSound
	case has("wifi", "wi-fi", "network", "bluetooth", "lan", "ftp", "ssh", "remote",
		"companion", "cast", "stream", "proxy", "netplay"):
		return menuSettingsNetwork
	}
	return menuSettingsSystem
}

// settingsMenuIcon names the glyph a settings category draws. Unlike the Tools
// page there is no empty answer: a settings row always reads as a gear at
// worst.
func settingsMenuIcon(elem Element) string {
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
	case has("preset", "wallpaper", "boot", "splash"):
		return "star"
	case has("theme", "appearance", "colour", "color", "display"):
		return "palette"
	case has("plugin", "extension", "add-on", "addon"):
		return "box"
	case has("sound", "audio", "volume", "music", "equalizer"):
		return "mic"
	case has("wifi", "wi-fi", "network", "bluetooth", "lan"):
		return "network"
	case has("update", "upgrade"):
		return "update"
	case has("storage", "disk", "space"):
		return "disk"
	case has("log"):
		return "logs"
	case has("accessib"):
		return "chart"
	case has("about", "credit", "version"):
		return "star"
	}
	return "gear"
}

// settingsSectionGlyph pairs a settings section with a representative glyph, so
// the heading is recognisable before its small label is read.
func settingsSectionGlyph(title string) string {
	switch strings.ToUpper(strings.TrimSpace(title)) {
	case menuSettingsAppearance:
		return "palette"
	case menuSettingsSound:
		return "mic"
	case menuSettingsNetwork:
		return "network"
	}
	return "gear"
}

// toolsMenuIcon names the glyph a tool draws in its tile. An empty result lets
// the launcher's own label/trigger mapping decide.
func toolsMenuIcon(elem Element) string {
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
	profile, ok := menuPageProfileFor(scene)
	if !ok {
		return menuTilesLayout{}, false
	}
	var l menuTilesLayout

	top := HomeTopBarH + 8
	bottom := screenHeight - bottomBarHeight(scene.Name) - 16
	l.panel = sdl.Rect{X: 24, Y: top, W: screenWidth - 48, H: bottom - top}
	if l.panel.W < 260 || l.panel.H < 220 {
		return menuTilesLayout{}, false
	}

	padX := SpaceXL
	l.titleY = l.panel.Y + SpaceLG
	// The title band is the shared page header: it measures itself, and the
	// body starts where its draw says the header ends.
	header := uiPageHeaderMetricsFor(config, menuTilesHeader(config, scene, 0, 0, 0))
	l.subY = l.titleY + header.SubtitleY
	l.bodyTop = l.titleY + header.Height + 18

	sectionH := int32(28)
	if f := getFontSized(config, "small", 15); f != nil {
		sectionH = int32(f.Height()) + 12
		if sectionH < 26 {
			sectionH = 26
		}
	}

	// Group the buttons, preserving the config's order inside each group.
	grouped := make(map[string][]int, len(profile.sections))
	for i, elem := range elems {
		if elem.Type != "button" {
			continue
		}
		group := profile.group(elem)
		grouped[group] = append(grouped[group], i)
	}
	sections := make([]menuTilesSection, 0, len(profile.sections))
	known := make(map[string]bool, len(profile.sections))
	for _, title := range profile.sections {
		known[title] = true
		if len(grouped[title]) > 0 {
			sections = append(sections, menuTilesSection{title: title, elems: grouped[title]})
		}
	}
	// A group the profile does not name (a menu whose resolver grew a section
	// before the profile did) still appears, below the ones it does, in a
	// stable order so the page does not reshuffle between frames.
	var extra []string
	for title := range grouped {
		if !known[title] {
			extra = append(extra, title)
		}
	}
	sort.Strings(extra)
	for _, title := range extra {
		sections = append(sections, menuTilesSection{title: title, elems: grouped[title]})
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

	// fixedFor is the height a column count spends on everything that is not a
	// tile: the section bands and the gaps between and after the rows. Counting
	// it exactly (rather than approximating it for the whole page at once) is
	// what keeps the tallest menu from running over the footer.
	fixedFor := func(cols int) (fixed int32, rows int) {
		for _, s := range sections {
			r := (len(s.elems) + cols - 1) / cols
			rows += r
			fixed += sectionH + int32(r-1)*l.gap + l.gap
		}
		return fixed, rows
	}

	// Columns come from the width first (a tile narrower than a readable label
	// is not worth showing), then widen only if the rows would not fit the
	// panel height. Widening past the widest section would only add empty
	// columns, which is why a sparse menu (Settings has two rows per section)
	// does not spread two tiles across six columns.
	longest := 1
	for _, s := range sections {
		if len(s.elems) > longest {
			longest = len(s.elems)
		}
	}
	widest := menuTilesMaxCols
	if longest < widest {
		widest = longest
	}
	cols := int((bodyW + l.gap) / (menuTilesMinW + l.gap))
	if cols > widest {
		cols = widest
	}
	if cols < menuTilesMinCols {
		cols = menuTilesMinCols
	}
	// A menu whose every section holds a single row of tiles still uses the
	// minimum grid rather than one column of very wide cards.
	limit := widest
	if limit < cols {
		limit = cols
	}
	tileH := int32(0)
	chosen := 0
	for c := cols; c <= limit; c++ {
		fixed, rows := fixedFor(c)
		if rows <= 0 {
			continue
		}
		room := availH - fixed
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

	// A menu with few rows (Settings is two short sections) would otherwise
	// leave all of its spare height in one heap under the last tile; centring
	// the block keeps the page looking composed at every size.
	fixed, rows := fixedFor(l.cols)
	y := l.bodyTop
	if used := fixed + int32(rows)*l.tileH; used < availH {
		y += (availH - used) / 2
	}
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
		secRows := int32((len(sec.elems) + l.cols - 1) / l.cols)
		y += secRows*l.tileH + (secRows-1)*l.gap + l.gap
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
	profile, ok := menuPageProfileFor(scene)
	if !ok {
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
		if icon := profile.icon(*elem); icon != "" {
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

// menuTilesHeader builds the page's header bar. The layout uses it to reserve
// the title band and the draw uses the same struct to fill it, so the body can
// never start under the subtitle.
func menuTilesHeader(config *Config, scene SceneConfig, x, y, w int32) UIPageHeader {
	profile, _ := menuPageProfileNamed(scene.Name)
	title := sceneDisplayName(scene.Name)
	if strings.TrimSpace(title) == "" {
		title = "Menu"
	}
	sub := profile.subtitle
	if profile.offlineSubtitle != "" && !IsOnline() {
		sub = profile.offlineSubtitle
	}
	status, statusCol := "", sdl.Color{}
	if profile.status != nil {
		status, statusCol = profile.status()
	}
	h := UIPageHeader{
		X: x, Y: y, W: w,
		Title:       title,
		Subtitle:    sub,
		Status:      status,
		StatusColor: statusCol,
		TitleSize:   menuTitleSize,
	}
	if profile.glyph != "" {
		glyph := profile.glyph
		h.Glyph = func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			DrawHomeIcon(r, glyph, box, false)
		}
	}
	return h
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
	padX := SpaceXL
	UIPageHeaderDraw(renderer, config, menuTilesHeader(config, scene,
		layout.panel.X+padX, layout.titleY, layout.panel.W-2*padX))

	profile, _ := menuPageProfileFor(scene)
	bodyW := layout.panel.W - 2*padX
	for _, sec := range layout.sections {
		glyph := profile.sectionIcon(sec.title)
		UISectionLabel(renderer, config, sec.title, layout.panel.X+padX, sec.y, bodyW,
			func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
				drawTileIcon(r, box.X+box.W/2, box.Y+box.H/2, box.W, glyph, tint, WithAlpha(tint, 38))
			})
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

// renderMenuTile draws one shortcut tile through the shared category-tile
// component: the icon in a tinted well, the tool's name wrapped underneath, the
// focus ring the rest of the shell uses, and the same eased press dip as every
// other button.
func renderMenuTile(renderer *sdl.Renderer, config *Config, elem Element, selected, hovered bool, hoverProgress float64, pressed bool) {
	if renderer == nil {
		return
	}
	w := getElementWidth(elem, 200)
	h := getElementHeight(elem, 110)
	if w <= 0 || h <= 0 {
		return
	}
	kind := tileIconKind(elem)
	UICategoryTileDraw(renderer, config, UICategoryTile{
		Rect:          sdl.Rect{X: elem.X, Y: elem.Y, W: w, H: h},
		Label:         elem.Text,
		Focused:       selected,
		Hovered:       hovered,
		Pressed:       pressed,
		HoverProgress: hoverProgress,
		Glyph: func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			drawTileIcon(r, box.X+box.W/2, box.Y+box.H/2, box.W, kind, tint, WithAlpha(tint, 40))
		},
	})
}
