package main

// setui.go — the Settings hub: one screen for every setting.
//
// A rail of sections on the left, the open section's rows on the right — the
// layout console settings use, because it shows where everything is at a
// glance and never needs more than two presses to reach a value. Moving
// through the rail previews each section; Right or A steps into its rows; B or
// Left steps back; B on the rail leaves Settings; L1/R1 jump between sections
// from anywhere. Rows are the declarative ones in setmodel.go: switches,
// carousels, links to other screens, text fields and read-outs.
//
// The scene answers to "Settings" and to the pages it replaced
// (SettingsGeneral, SettingsAppearance, SettingsSmart), which open it on their
// section, so search results, achievements and old configs still land on the
// right rows. The config elements those scenes carry are left in place — other
// features inject rows there and tests read them — but nothing draws them: the
// hub owns the whole page.
//
// Values are read twice a second and after every change, never per frame, and
// the page's geometry is measured from the fonts, so nothing is cut off on the
// 960-wide canvas or at 145% text size: labels ellipsize, the rail and the rows
// scroll, and carousels size themselves from their widest value.

import (
	"context"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// setHubSceneNames are the scenes the hub answers to.
var setHubSceneNames = [...]string{"Settings", "SettingsGeneral", "SettingsAppearance", "SettingsSmart"}

// setIsHubScene reports whether name is drawn by the hub.
func setIsHubScene(name string) bool {
	for _, n := range setHubSceneNames {
		if n == name {
			return true
		}
	}
	return false
}

// setRowView is one row as last read. The hub refreshes it twice a second and
// after every change; drawing only reads it.
type setRowView struct {
	visible  bool
	disabled bool
	detail   string
	value    string
	on       bool
	index    int
	options  []setOption
	swatch   sdl.Color
}

// setHubState is the hub's navigation and the last frame's geometry.
type setHubState struct {
	visit       context.Context // the scene visit this state belongs to
	scene       string
	cfg         *Config
	section     int
	lastSection int
	deepLink    bool
	// pendingRow is the row a search result asked for; setHubEnter applies it
	// after the first refresh, when the section's rows are known.
	pendingRow   string
	pendingLabel string
	row          int
	inPane       bool
	// follow keeps the focused row in view; the wheel turns it off until the
	// next D-pad or key press, so scrolling with the mouse is not undone.
	follow      bool
	scroll      float64
	target      float64
	railScroll  int32
	rows        []setRow
	views       []setRowView
	viewSection int
	refreshedAt uint64
	dirty       bool
	kbWasOpen   bool
	sheet       setSheetState
	railRects   []sdl.Rect
	rowRects    []sdl.Rect
	rowParts    []setCarouselParts
	rowClip     []uint8
	listRect    sdl.Rect
}

var setHub = setHubState{viewSection: -1}

// setHubRefreshMs is how often the rows re-read their values while the page is
// up: often enough that a probe (Bluetooth, the snap count) lands quickly, rare
// enough that reading a value never costs a frame.
const setHubRefreshMs = 500

// setHubFonts are the hub's type sizes for one frame. They are looked up every
// frame (a cache hit) because the text size setting can change them.
type setHubFonts struct {
	rail, title, sub, label, detail, value *ttf.Font
}

func setHubLoadFonts(c *Config) (setHubFonts, bool) {
	f := setHubFonts{
		rail:   FontForRole(c, TypeBody),
		title:  FontForRole(c, TypeHeading),
		sub:    FontForRole(c, TypeLabel),
		label:  FontForRole(c, TypeBody),
		detail: FontForRole(c, TypeLabel),
		value:  FontForRole(c, TypeBody),
	}
	ok := f.rail != nil && f.title != nil && f.sub != nil && f.label != nil && f.detail != nil && f.value != nil
	return f, ok
}

// setHubSceneName is the scene on screen.
func setHubSceneName(c *Config) string {
	if c == nil || currentSceneIndex < 0 || currentSceneIndex >= len(c.Scenes) {
		return ""
	}
	return c.Scenes[currentSceneIndex].Name
}

// ── Visits ────────────────────────────────────────────────────────────────

// setHubEnsureVisit starts a new visit when the scene was (re)entered: the
// scene-work generation changes on every transition, which is the one signal
// that covers every way of arriving here. A different config (a reload, or a
// test drawing its own) is a new visit too: the rows read from the old one.
func setHubEnsureVisit(c *Config) {
	scene := setHubSceneName(c)
	if ctx := sceneWorkContext(); ctx != setHub.visit || scene != setHub.scene || c != setHub.cfg {
		setHubEnter(c, ctx, scene)
	}
}

// setHubEnter resets the page for a visit: the last section used (or the one a
// legacy page name or setOpenSection asked for), the rail focused unless the
// visit was a deep link, and every source the rows read refreshed once.
func setHubEnter(c *Config, ctx context.Context, scene string) {
	setHub.visit = ctx
	setHub.scene = scene
	setHub.cfg = c
	section := setHub.lastSection
	deep := setHub.deepLink
	pendingRow, pendingLabel := setHub.pendingRow, setHub.pendingLabel
	setHub.pendingRow, setHub.pendingLabel = "", ""
	setHub.deepLink = false
	if id := setLegacySection(scene); id != "" {
		if i := setSectionIndex(id); i >= 0 {
			section, deep = i, true
		}
	}
	if section < 0 || section >= len(setSections()) {
		section = 0
	}
	setHub.section = section
	setHub.lastSection = section
	setHub.inPane = false
	setHub.row = 0
	setHub.follow = true
	setHub.scroll, setHub.target = 0, 0
	setHub.railScroll = 0
	setHub.sheet.open = false
	// The time-zone picker closes too, unless a deep link opened it for this
	// visit (tzpicker.go).
	tzPickerHubEnter()
	clockSheetHubEnter()
	setHub.viewSection = -1
	if c != nil {
		// A real arrival (the transition that opened this scene, not a redraw)
		// refreshes what the rows read in the background: Quick Settings
		// probes the radios and levels off-thread, and the Video snaps row gets
		// a fresh count. The page drains both as it refreshes. The cookie rows
		// read the jar live, so the old page's re-seed of its label is not
		// needed.
		if currentSceneWorkName() == scene {
			qsSyncFromConfig(c)
			snapClipCountRefresh(snapPreviewRoot(c))
		}
		a11ySyncFromTheme(c)
		LoadPlugins(c)
	}
	setHubRefresh(c)
	if deep {
		setHubEnterPane(false)
		setHubFocusRow(pendingRow, pendingLabel)
	}
	// Chat's "Add key" lands on the Groq key row with its sheet open
	// (groqkey_sheet.go).
	groqKeyHubEnter(c)
}

// ── Reading the rows ──────────────────────────────────────────────────────

// setHubRefresh re-reads the open section's rows.
func setHubRefresh(c *Config) {
	sections := setSections()
	if setHub.section < 0 || setHub.section >= len(sections) {
		setHub.section = 0
	}
	qsDrainProbe()
	// The legacy page's Video snaps value ($gameSnapsLabel) follows the count
	// the same way it did when that page was drawn, for anything that still
	// substitutes it.
	snapPreviewSyncRow(c)
	rows := setSectionRows(c, sections[setHub.section])
	setHub.rows = rows
	if cap(setHub.views) < len(rows) {
		setHub.views = make([]setRowView, len(rows))
	}
	setHub.views = setHub.views[:len(rows)]
	for i := range rows {
		setHub.views[i] = setHubReadRow(c, rows[i])
	}
	setHub.viewSection = setHub.section
	setHub.refreshedAt = sdl.GetTicks64()
	setHub.dirty = false
	setHubFixRow()
}

// setHubReadRow reads one row's state.
func setHubReadRow(c *Config, row setRow) setRowView {
	v := setRowView{index: -1}
	if c == nil {
		return v
	}
	v.visible = row.Visible == nil || row.Visible(c)
	if !v.visible {
		return v
	}
	v.disabled = row.Disabled != nil && row.Disabled(c)
	if row.Detail != nil {
		v.detail = row.Detail(c)
	}
	switch row.Kind {
	case setKindToggle:
		if row.Get != nil {
			v.on = row.Get(c)
		}
	case setKindChoice:
		if row.Options != nil {
			v.options = row.Options(c)
		}
		if row.Current != nil {
			v.index = row.Current(c)
		}
		if v.index >= 0 && v.index < len(v.options) {
			o := v.options[v.index]
			v.value = o.Label
			if col, ok := parseHexColor(o.Swatch); ok {
				v.swatch = col
			}
		} else {
			v.index = -1
			v.value = "—"
			if row.Value != nil {
				v.value = row.Value(c)
			}
			if colorSettingVariables[row.Field] {
				rgb := effectiveThemeColor(c, row.Field)
				v.swatch = sdl.Color{R: uint8(rgb.R), G: uint8(rgb.G), B: uint8(rgb.B), A: 255}
			}
		}
	default:
		if row.Value != nil {
			v.value = row.Value(c)
		}
	}
	return v
}

// setHubRowVisible reports whether row i is shown.
func setHubRowVisible(i int) bool {
	return i >= 0 && i < len(setHub.views) && setHub.views[i].visible
}

// setHubStep is the next shown row from i in dir, or -1.
func setHubStep(i, dir int) int {
	for j := i + dir; j >= 0 && j < len(setHub.views); j += dir {
		if setHub.views[j].visible {
			return j
		}
	}
	return -1
}

// setHubFixRow keeps the focus on a shown row after a refresh hid one.
func setHubFixRow() {
	if setHubRowVisible(setHub.row) {
		return
	}
	if next := setHubStep(setHub.row, 1); next >= 0 {
		setHub.row = next
		return
	}
	if prev := setHubStep(setHub.row, -1); prev >= 0 {
		setHub.row = prev
		return
	}
	setHub.row = 0
}

// ── Navigation ────────────────────────────────────────────────────────────

// setHubSelectSection opens section i in the pane.
func setHubSelectSection(c *Config, i int, sound bool) {
	n := len(setSections())
	if n == 0 {
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	if i == setHub.section {
		return
	}
	setHub.section = i
	setHub.lastSection = i
	setHub.row = 0
	setHub.follow = true
	setHub.scroll, setHub.target = 0, 0
	if setSections()[i].ID == "plugins" && c != nil {
		LoadPlugins(c)
	}
	setHubRefresh(c)
	setHub.row = setHubStep(-1, 1)
	if setHub.row < 0 {
		setHub.row = 0
	}
	if sound {
		PlayNavSound()
	}
}

// setOpenSettingRow opens Settings on one row: Home search's row results land
// on the exact switch instead of the first line of its section.
func setOpenSettingRow(c *Config, sectionID, rowID, label string) {
	if c == nil {
		return
	}
	section := setSectionIndex(sectionID)
	if section < 0 {
		return
	}
	if setIsHubScene(setHubSceneName(c)) {
		setHubSelectSection(c, section, false)
		if !setHubFocusRow(rowID, label) {
			setHubEnterPane(false)
		}
		return
	}
	if sceneIndexExact(c, "Settings") < 0 {
		setToast("That screen is not part of this setup")
		return
	}
	setHub.lastSection = section
	setHub.deepLink = true
	setHub.pendingRow, setHub.pendingLabel = rowID, label
	setOpenScene("Settings")(c)
}

// setHubFocusRow puts the focus on the row with this id, or — for a row that
// predates ids — on the one with this label. It reports whether it found a
// visible row; the caller leaves the first row focused when it did not.
func setHubFocusRow(id, label string) bool {
	for i := range setHub.rows {
		if i >= len(setHub.views) || !setHub.views[i].visible {
			continue
		}
		row := setHub.rows[i]
		if (id != "" && row.ID == id) || (id == "" && row.Label == label) {
			setHub.row = i
			setHub.inPane = true
			setHub.follow = true
			return true
		}
	}
	return false
}

// setHubEnterPane moves the focus from the rail onto the first row.
func setHubEnterPane(sound bool) {
	first := setHubStep(-1, 1)
	if first < 0 {
		return
	}
	setHub.inPane = true
	setHub.row = first
	setHub.follow = true
	if sound {
		PlayNavSound()
	}
}

// setHubLeavePane hands the focus back to the rail.
func setHubLeavePane() {
	setHub.inPane = false
	PlayBackSound()
}

// setHubMoveRow steps the focused row.
func setHubMoveRow(dir int) {
	setHub.follow = true
	if next := setHubStep(setHub.row, dir); next >= 0 {
		setHub.row = next
		PlayNavSound()
	}
}

// setHubRowTakesSides reports whether Left/Right change the focused row's value
// (switches and carousels) rather than move between the rail and the rows.
func setHubRowTakesSides() bool {
	i := setHub.row
	if !setHubRowVisible(i) || i >= len(setHub.rows) || setHub.views[i].disabled {
		return false
	}
	k := setHub.rows[i].Kind
	return k == setKindToggle || k == setKindChoice
}

// setHubAction is one navigation intent, whatever key or button produced it.
type setHubAction int

const (
	setActUp setHubAction = iota
	setActDown
	setActLeft
	setActRight
	setActOK
	setActBack
	setActPrevSection
	setActNextSection
	setActFirst
	setActLast
)

// setHubAct applies one intent.
func setHubAct(c *Config, act setHubAction) {
	switch act {
	case setActPrevSection:
		setHubSelectSection(c, setHub.section-1, true)
		return
	case setActNextSection:
		setHubSelectSection(c, setHub.section+1, true)
		return
	}
	if !setHub.inPane {
		switch act {
		case setActUp:
			if setHub.section == 0 {
				// The top bar (Back pill, clock, bell) is above the first
				// section, the way it is above every screen's first row.
				focusEngine.ClearFocus()
				focusChromeBar()
				return
			}
			setHubSelectSection(c, setHub.section-1, true)
		case setActDown:
			setHubSelectSection(c, setHub.section+1, true)
		case setActRight, setActOK:
			setHubEnterPane(true)
		case setActBack, setActLeft:
			if act == setActBack {
				goBackScene(c)
			}
		case setActFirst:
			setHubSelectSection(c, 0, true)
		case setActLast:
			setHubSelectSection(c, len(setSections())-1, true)
		}
		return
	}
	switch act {
	case setActUp:
		if setHubStep(setHub.row, -1) < 0 {
			// Above the first row is the top bar, as on every screen.
			focusEngine.ClearFocus()
			focusChromeBar()
			return
		}
		setHubMoveRow(-1)
	case setActDown:
		setHubMoveRow(1)
	case setActLeft:
		if setHubRowTakesSides() {
			setHubActivate(c, setHub.row, -1)
		} else {
			setHubLeavePane()
		}
	case setActRight:
		if setHubRowTakesSides() {
			setHubActivate(c, setHub.row, 1)
		}
	case setActOK:
		setHubActivate(c, setHub.row, 0)
	case setActBack:
		setHubLeavePane()
	case setActFirst:
		if first := setHubStep(-1, 1); first >= 0 {
			setHub.row = first
			setHub.follow = true
		}
	case setActLast:
		if last := setHubStep(len(setHub.views), -1); last >= 0 {
			setHub.row = last
			setHub.follow = true
		}
	}
}

// setHubActivate presses row i: dir 0 is A (or a tap), -1/+1 are Left/Right
// (or a tap on a carousel arrow).
func setHubActivate(c *Config, i, dir int) {
	if c == nil || !setHubRowVisible(i) || i >= len(setHub.rows) {
		return
	}
	row, v := setHub.rows[i], setHub.views[i]
	if v.disabled {
		ShowToast(setOr(v.detail, row.Label+" is not available right now"), ToastKindInfo)
		return
	}
	switch row.Kind {
	case setKindToggle:
		on := !v.on
		switch {
		case dir < 0:
			on = false
		case dir > 0:
			on = true
		}
		if on == v.on || row.Set == nil {
			return
		}
		if !row.Quiet {
			PlayToggleSound()
		}
		row.Set(c, on)
	case setKindChoice:
		if dir == 0 && row.Sheet != nil {
			current := ""
			if row.SheetValue != nil {
				current = row.SheetValue(c)
			}
			choose := row.Choose
			setSheetOpen(&setHub.sheet, setOr(row.SheetTitle, row.Label), v.detail, row.Sheet(c), current,
				func(value string) {
					if choose != nil {
						choose(c, value)
					}
					setHub.dirty = true
				})
			return
		}
		if dir == 0 && row.Field != "" {
			PlayActivateSound()
			setEditField(c, row.Field)
			return
		}
		n := len(v.options)
		if n == 0 || row.Choose == nil {
			return
		}
		if dir == 0 {
			dir = 1
		}
		next := 0
		switch {
		case v.index >= 0:
			next = setCycleIndex(v.index, dir, n)
			if row.Clamp && next != v.index+dir {
				// A level at its end stays there (setRow.Clamp).
				return
			}
		case dir < 0:
			next = n - 1
		}
		if !row.Quiet {
			PlayNavSound()
		}
		row.Choose(c, v.options[next].Value)
	case setKindAction:
		if dir != 0 || row.Open == nil {
			return
		}
		PlayActivateSound()
		row.Open(c)
	case setKindText:
		if dir != 0 || row.Field == "" {
			return
		}
		PlayActivateSound()
		setEditField(c, row.Field)
	default:
		return
	}
	setHub.dirty = true
}

// setEditField opens the on-screen keyboard on a text setting. The keyboard
// edits scene input elements, so the field's own input from the old Settings
// pages is used as the backing element; a config without one gets one, in
// memory, on the General page.
func setEditField(c *Config, variable string) {
	sceneIdx, elemIdx := setFieldElement(c, variable)
	if sceneIdx < 0 {
		setToast("That setting cannot be edited in this setup")
		return
	}
	handleInputSelection(nil, c, sceneIdx, elemIdx)
}

// setFieldElement finds (or adds) the input element a variable is edited
// through.
func setFieldElement(c *Config, variable string) (int, int) {
	if c == nil || variable == "" {
		return -1, -1
	}
	for si := range c.Scenes {
		for ei, el := range c.Scenes[si].Elements {
			if el.Type == "input" && strings.EqualFold(strings.TrimSpace(el.Variable), variable) {
				return si, ei
			}
		}
	}
	si := sceneIndexExact(c, "SettingsGeneral")
	if si < 0 {
		si = currentSceneIndex
	}
	if si < 0 || si >= len(c.Scenes) {
		return -1, -1
	}
	c.Scenes[si].Elements = append(c.Scenes[si].Elements, Element{
		Type:     "input",
		Variable: variable,
		X:        -4000,
		Y:        -4000,
		Width:    "8",
		Height:   "8",
	})
	return si, len(c.Scenes[si].Elements) - 1
}

// ── Drawing ───────────────────────────────────────────────────────────────

// setHubRender draws the page.
func setHubRender(renderer *sdl.Renderer, c *Config) {
	drawAppBackdrop(renderer, c, screenWidth, screenHeight)
	setHubEnsureVisit(c)
	f, ok := setHubLoadFonts(c)
	if !ok {
		renderStatusBar(renderer, c)
		renderFooter(renderer, c)
		return
	}
	// A typed value lands when the keyboard closes; read it then.
	if setHub.kbWasOpen && !virtualKeyboardActive {
		setHub.dirty = true
	}
	setHub.kbWasOpen = virtualKeyboardActive
	if setHub.dirty || setHub.viewSection != setHub.section || sdl.GetTicks64()-setHub.refreshedAt > setHubRefreshMs {
		setHubRefresh(c)
	}

	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + scaleY(8, screenHeight)
	bottom := screenHeight - bottomBarHeight(setHub.scene) - 16
	header := UIPanelHeader("Settings", "", "settings")
	header.X, header.Y, header.W = margin, top, screenWidth-2*margin
	header.Status = "Changes save as you make them"
	contentY := UIPageHeaderDraw(renderer, c, header) + 14
	railW := screenWidth * 22 / 100
	if railW < 200 {
		railW = 200
	}
	if railW > 290 {
		railW = 290
	}
	rail := sdl.Rect{X: margin, Y: contentY, W: railW, H: bottom - contentY}
	paneX := rail.X + rail.W + 20
	pane := sdl.Rect{X: paneX, Y: contentY, W: screenWidth - margin - paneX, H: bottom - contentY}

	// While the top bar holds the focus the page shows no ring of its own.
	chrome := chromeTier(focusEngine.ElementIndex()) != 0
	setHubDrawRail(renderer, c, f, rail, chrome)
	setHubDrawPane(renderer, c, f, pane, chrome)
	renderStatusBar(renderer, c)
	if setHub.sheet.open {
		setSheetDraw(renderer, c, &setHub.sheet)
	}
	tzPickerDraw(renderer, c)
	clockSheetDraw(renderer, c)
	// The footer goes last so its hints stay readable over a sheet's dim.
	renderFooter(renderer, c)
}

// setHubDrawRail draws the section list.
func setHubDrawRail(renderer *sdl.Renderer, c *Config, f setHubFonts, rail sdl.Rect, chrome bool) {
	sections := setSections()
	if rail.W <= 0 || rail.H <= 0 || len(sections) == 0 {
		return
	}
	itemH := int32(f.rail.Height()) + 22
	if itemH < 44 {
		itemH = 44
	}
	const gap = int32(4)
	total := int32(len(sections))*(itemH+gap) - gap
	selY := int32(setHub.section) * (itemH + gap)
	if selY < setHub.railScroll {
		setHub.railScroll = selY
	}
	if selY+itemH > setHub.railScroll+rail.H {
		setHub.railScroll = selY + itemH - rail.H
	}
	if maxScroll := total - rail.H; setHub.railScroll > maxScroll {
		setHub.railScroll = maxScroll
	}
	if setHub.railScroll < 0 {
		setHub.railScroll = 0
	}
	if cap(setHub.railRects) < len(sections) {
		setHub.railRects = make([]sdl.Rect, len(sections))
	}
	setHub.railRects = setHub.railRects[:len(sections)]

	accent := glassAccent()
	icon := itemH - 22
	if icon > 24 {
		icon = 24
	}
	if icon < 18 {
		icon = 18
	}
	textH := int32(f.rail.Height())
	clip := rail
	_ = renderer.SetClipRect(&clip)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	for i, s := range sections {
		r := sdl.Rect{X: rail.X, Y: rail.Y + int32(i)*(itemH+gap) - setHub.railScroll, W: rail.W, H: itemH}
		if r.Y+r.H < rail.Y || r.Y > rail.Y+rail.H {
			setHub.railRects[i] = sdl.Rect{}
			continue
		}
		setHub.railRects[i] = r
		selected := i == setHub.section
		focused := selected && !setHub.inPane && !chrome && !setHub.sheet.open
		switch {
		case selected:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassRowRadius, WithAlpha(accent, 34))
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassRowRadius, glassFillFocus())
			if focused {
				drawGlassFocusRing(renderer, r, glassRowRadius)
			} else {
				// The open section keeps a mark while the rows hold the focus.
				fillRoundedRect(renderer, r.X+6, r.Y+11, 4, r.H-22, 2, accent)
			}
		case pointInRect(mouseX, mouseY, r):
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassRowRadius, glassFillHover())
		}
		ix := r.X + 18
		DrawHomeIcon(renderer, s.Glyph, sdl.Rect{X: ix, Y: r.Y + (r.H-icon)/2, W: icon, H: icon}, selected)
		tx := ix + icon + 12
		ink := ColorTextSecondary()
		if selected {
			ink = ColorTextPrimary()
		}
		drawText(renderer, f.rail, ellipsize(f.rail, s.Title, r.X+r.W-12-tx), tx, r.Y+(r.H-textH)/2, ink, textAlignLeft)
	}
	_ = renderer.SetClipRect(nil)
}

// setHubDrawPane draws the open section: its title, its line and its rows.
func setHubDrawPane(renderer *sdl.Renderer, c *Config, f setHubFonts, pane sdl.Rect, chrome bool) {
	sections := setSections()
	if pane.W <= 0 || pane.H <= 0 || setHub.section < 0 || setHub.section >= len(sections) {
		return
	}
	sec := sections[setHub.section]
	th, sh := int32(f.title.Height()), int32(f.sub.Height())
	drawText(renderer, f.title, ellipsize(f.title, sec.Title, pane.W-8), pane.X+4, pane.Y, ColorTextPrimary(), textAlignLeft)
	drawText(renderer, f.sub, ellipsize(f.sub, sec.Subtitle, pane.W-8), pane.X+4, pane.Y+th+2, ColorTextMuted(), textAlignLeft)
	listTop := pane.Y + th + 2 + sh + 14
	list := sdl.Rect{X: pane.X, Y: listTop, W: pane.W, H: pane.Y + pane.H - listTop}
	setHub.listRect = list
	if list.H <= 0 {
		return
	}

	lh, dh := int32(f.label.Height()), int32(f.detail.Height())
	rowH := lh + dh + 22
	if rowH < 58 {
		rowH = 58
	}
	const gap = int32(8)
	n := len(setHub.rows)
	if n > len(setHub.views) {
		n = len(setHub.views)
	}
	if cap(setHub.rowRects) < n {
		setHub.rowRects = make([]sdl.Rect, n)
		setHub.rowParts = make([]setCarouselParts, n)
		setHub.rowClip = make([]uint8, n)
	}
	setHub.rowRects = setHub.rowRects[:n]
	setHub.rowParts = setHub.rowParts[:n]
	setHub.rowClip = setHub.rowClip[:n]

	// Rows in content space first, so the scroll can keep the focus in view.
	y := int32(0)
	focusTop, focusBottom := int32(-1), int32(-1)
	rowW := list.W - 12
	for i := 0; i < n; i++ {
		setHub.rowParts[i] = setCarouselParts{}
		setHub.rowClip[i] = 0
		if !setHub.views[i].visible {
			setHub.rowRects[i] = sdl.Rect{}
			continue
		}
		setHub.rowRects[i] = sdl.Rect{X: list.X, Y: y, W: rowW, H: rowH}
		if i == setHub.row {
			focusTop, focusBottom = y, y+rowH
		}
		y += rowH + gap
	}
	content := y - gap
	if content < 0 {
		content = 0
	}
	maxScroll := float64(content - list.H)
	if maxScroll < 0 {
		maxScroll = 0
	}
	if setHub.follow && setHub.inPane && focusTop >= 0 {
		if float64(focusTop) < setHub.target {
			setHub.target = float64(focusTop)
		}
		if float64(focusBottom) > setHub.target+float64(list.H) {
			setHub.target = float64(focusBottom - list.H)
		}
	}
	if setHub.target > maxScroll {
		setHub.target = maxScroll
	}
	if setHub.target < 0 {
		setHub.target = 0
	}
	if effectiveReducedMotion(c) {
		setHub.scroll = setHub.target
	} else {
		setHub.scroll += (setHub.target - setHub.scroll) * 0.35
		if d := setHub.target - setHub.scroll; d < 0.5 && d > -0.5 {
			setHub.scroll = setHub.target
		}
	}
	scroll := int32(setHub.scroll + 0.5)

	clip := list
	_ = renderer.SetClipRect(&clip)
	for i := 0; i < n; i++ {
		r := setHub.rowRects[i]
		if r.H == 0 {
			continue
		}
		r.Y += list.Y - scroll
		setHub.rowRects[i] = r
		if r.Y+r.H < list.Y || r.Y > list.Y+list.H {
			continue
		}
		focused := setHub.inPane && i == setHub.row && !chrome && !setHub.sheet.open
		setHub.rowParts[i] = setHubDrawRow(renderer, f, i, r, focused)
	}
	_ = renderer.SetClipRect(nil)
	if content > list.H {
		frac := 0.0
		if maxScroll > 0 {
			frac = setHub.scroll / maxScroll
		}
		drawScrollbar(renderer, list.X+list.W-4, list.Y, 3, list.H, float64(list.H)/float64(content), frac)
	}
}

// setHubDrawRow draws row i in r and returns its carousel's parts (zero for
// other kinds) for the mouse.
func setHubDrawRow(renderer *sdl.Renderer, f setHubFonts, i int, r sdl.Rect, focused bool) setCarouselParts {
	row, v := setHub.rows[i], setHub.views[i]
	hovered := !focused && pointInRect(mouseX, mouseY, r) && pointInRect(mouseX, mouseY, setHub.listRect)
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	if focused {
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, glassRowRadius, WithAlpha(glassAccent(), 26))
	}
	drawGlassSurface(renderer, r, glassRowRadius, glassState{
		Focused:  focused,
		Hovered:  hovered,
		NoLift:   true,
		Disabled: v.disabled && !focused,
	})

	const padX = int32(18)
	right := r.X + r.W - padX
	parts := setCarouselParts{}
	ctrlW := int32(0)
	clip := uint8(0)
	switch row.Kind {
	case setKindToggle:
		sw, sh := setSwitchSize(r.H)
		setSwitchDraw(renderer, sdl.Rect{X: right - sw, Y: r.Y + (r.H-sh)/2, W: sw, H: sh}, v.on, v.disabled)
		ctrlW = sw
	case setKindChoice:
		ch := r.H - 16
		if ch > 40 {
			ch = 40
		}
		// Sized from the widest stop so the arrows stay put while the value
		// changes; a stop that would not fit even at the cap is noted.
		need := setCarouselWidthFor(f.value, v.options, v.value, ch, 150, 0)
		w := need
		if limit := r.W * 45 / 100; w > limit {
			w = limit
			clip |= setClipValue
		}
		rect := sdl.Rect{X: right - w, Y: r.Y + (r.H-ch)/2, W: w, H: ch}
		hoverPart := 0
		if part, hit := setCarouselHit(setCarouselLayout(rect), mouseX, mouseY); hit {
			hoverPart = part
		}
		many := len(v.options) > 1 && !v.disabled
		canPrev, canNext := many, many
		if row.Clamp && v.index >= 0 {
			// A level at its end dims the arrow that has nowhere to go.
			canPrev = many && v.index > 0
			canNext = many && v.index < len(v.options)-1
		}
		parts = setCarouselDraw(renderer, f.value, rect, v.value, v.swatch, focused, canPrev, canNext, hoverPart)
		ctrlW = w
	case setKindAction, setKindText:
		ink := ColorTextSecondary()
		if focused {
			ink = glassAccent()
		}
		if row.Kind == setKindText {
			icon := int32(20)
			DrawHomeIcon(renderer, "edit", sdl.Rect{X: right - icon, Y: r.Y + (r.H-icon)/2, W: icon, H: icon}, focused)
			ctrlW = icon
		} else {
			setDrawChevron(renderer, right-6, r.Y+r.H/2, 7, 1, ink)
			ctrlW = 12
		}
		if v.value != "" {
			text := ellipsize(f.value, v.value, r.W*40/100)
			if text != v.value {
				clip |= setClipValue
			}
			tw, tth := measureText(f.value, text)
			drawText(renderer, f.value, text, right-ctrlW-12-tw, r.Y+(r.H-tth)/2, ColorTextSecondary(), textAlignLeft)
			ctrlW += 12 + tw
		}
	case setKindInfo:
		if v.value != "" {
			text := ellipsize(f.value, v.value, r.W*50/100)
			if text != v.value {
				clip |= setClipValue
			}
			tw, tth := measureText(f.value, text)
			drawText(renderer, f.value, text, right-tw, r.Y+(r.H-tth)/2, ColorTextSecondary(), textAlignLeft)
			ctrlW = tw
		}
	}

	x := r.X + padX
	avail := right - ctrlW - 16 - x
	if avail < 40 {
		avail = 40
	}
	labelInk, detailInk := ColorTextPrimary(), ColorTextMuted()
	if v.disabled {
		labelInk, detailInk = ColorTextTertiary(), WithAlpha(ColorTextTertiary(), 200)
	}
	lh, dh := int32(f.label.Height()), int32(f.detail.Height())
	label := ellipsize(f.label, row.Label, avail)
	if label != row.Label {
		clip |= setClipLabel
	}
	if v.detail == "" {
		setHubNoteClip(i, clip)
		drawText(renderer, f.label, label, x, r.Y+(r.H-lh)/2, labelInk, textAlignLeft)
		return parts
	}
	detail := ellipsize(f.detail, v.detail, avail)
	if detail != v.detail {
		clip |= setClipDetail
	}
	setHubNoteClip(i, clip)
	ty := r.Y + (r.H-lh-dh-2)/2
	drawText(renderer, f.label, label, x, ty, labelInk, textAlignLeft)
	drawText(renderer, f.detail, detail, x, ty+lh+2, detailInk, textAlignLeft)
	return parts
}

// The bits of setHubState.rowClip: which of a row's texts the last frame had
// to shorten. Labels may ellipsize at the largest text sizes by design; the
// render tests hold the rows that used to be cut off to never needing it at the
// default size, on either canvas.
const (
	setClipLabel uint8 = 1 << iota
	setClipDetail
	setClipValue
)

// setHubNoteClip records row i's clip bits for the last frame.
func setHubNoteClip(i int, clip uint8) {
	if i >= 0 && i < len(setHub.rowClip) {
		setHub.rowClip[i] = clip
	}
}

// ── Input ─────────────────────────────────────────────────────────────────

// setHubKey handles a key on the page.
func setHubKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	setHubEnsureVisit(c)
	if tzPicker.open {
		tzPickerKey(e)
		return true
	}
	if clockSheet.open {
		clockSheetKey(e, c)
		return true
	}
	if setHub.sheet.open {
		setSheetKey(&setHub.sheet, e.Keysym.Sym)
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		setHubAct(c, setActUp)
	case sdl.K_DOWN:
		setHubAct(c, setActDown)
	case sdl.K_LEFT:
		setHubAct(c, setActLeft)
	case sdl.K_RIGHT:
		setHubAct(c, setActRight)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		// Once per press: a held key's repeats must not arm and then confirm
		// a two-press action such as the boot logo (bootlogo.go).
		if e.Repeat == 0 {
			setHubAct(c, setActOK)
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		setHubAct(c, setActBack)
	case sdl.K_TAB:
		if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
			setHubAct(c, setActPrevSection)
		} else {
			setHubAct(c, setActNextSection)
		}
	case sdl.K_PAGEUP:
		setHubAct(c, setActPrevSection)
	case sdl.K_PAGEDOWN:
		setHubAct(c, setActNextSection)
	case sdl.K_HOME:
		setHubAct(c, setActFirst)
	case sdl.K_END:
		setHubAct(c, setActLast)
	default:
		return false
	}
	return true
}

// setHubPad handles a controller button on the page. Start, the guide button
// and the stick clicks keep their app-wide meanings (the Start menu, the
// Brick's help card and Quick Settings).
func setHubPad(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_GUIDE,
		sdl.CONTROLLER_BUTTON_LEFTSTICK, sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		return false
	}
	setHubEnsureVisit(c)
	if tzPicker.open {
		tzPickerPad(e.Button)
		return true
	}
	if clockSheet.open {
		clockSheetPad(e.Button, c)
		return true
	}
	if setHub.sheet.open {
		setSheetPad(&setHub.sheet, e.Button)
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		setHubAct(c, setActUp)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		setHubAct(c, setActDown)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		setHubAct(c, setActLeft)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		setHubAct(c, setActRight)
	case sdl.CONTROLLER_BUTTON_A:
		setHubAct(c, setActOK)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		setHubAct(c, setActBack)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		setHubAct(c, setActPrevSection)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		setHubAct(c, setActNextSection)
	}
	return true
}

// setHubMouse handles a click or a tap. Every click on the page is the hub's:
// the top bar has already had its turn (main.go asks the hub after it), and
// the config buttons under the page must never be pressed through it.
func setHubMouse(mx, my int32, c *Config) bool {
	setHubEnsureVisit(c)
	if tzPicker.open {
		tzPickerMouse(mx, my)
		return true
	}
	if clockSheet.open {
		clockSheetMouse(mx, my, c)
		return true
	}
	if setHub.sheet.open {
		setSheetMouse(&setHub.sheet, mx, my)
		return true
	}
	for i, r := range setHub.railRects {
		if r.W <= 0 || !pointInRect(mx, my, r) {
			continue
		}
		if i == setHub.section {
			if !setHub.inPane {
				setHubEnterPane(true)
			}
		} else {
			setHubSelectSection(c, i, true)
			setHub.inPane = false
		}
		return true
	}
	if !pointInRect(mx, my, setHub.listRect) {
		return true
	}
	for i, r := range setHub.rowRects {
		if r.W <= 0 || !pointInRect(mx, my, r) || !setHubRowVisible(i) {
			continue
		}
		setHub.inPane = true
		setHub.row = i
		dir := 0
		if i < len(setHub.rows) && setHub.rows[i].Kind == setKindChoice && i < len(setHub.rowParts) {
			if part, hit := setCarouselHit(setHub.rowParts[i], mx, my); hit {
				dir = part
			}
		}
		setHubActivate(c, i, dir)
		return true
	}
	return true
}

// setHubWheel scrolls the rows (or moves a sheet's cursor).
func setHubWheel(dy int32) {
	if tzPicker.open {
		tzPickerWheel(dy)
		return
	}
	if clockSheet.open {
		clockSheetWheel(dy)
		return
	}
	if setHub.sheet.open {
		setSheetWheel(&setHub.sheet, dy)
		return
	}
	setHub.follow = false
	setHub.target -= float64(dy) * 72
	if setHub.target < 0 {
		setHub.target = 0
	}
}

// setHubHints is the footer for what holds the focus.
func setHubHints() []FooterHint {
	if tzPicker.open {
		return tzPickerHints()
	}
	if clockSheet.open {
		return clockSheetHints()
	}
	if setHub.sheet.open {
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Close"}}
	}
	if !setHub.inPane {
		return []FooterHint{{Button: "D-Pad", Label: "Sections"}, {Button: "A", Label: "Open"},
			{Button: "L1/R1 / Tab", Label: "Jump"}, {Button: "B", Label: "Back"}}
	}
	back := FooterHint{Button: "B", Label: "Sections"}
	i := setHub.row
	if !setHubRowVisible(i) || i >= len(setHub.rows) {
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, back}
	}
	row := setHub.rows[i]
	switch row.Kind {
	case setKindToggle:
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Switch"}, back}
	case setKindChoice:
		a := "Next"
		switch {
		case row.Sheet != nil:
			a = "All"
		case row.Field != "":
			a = "Type"
		}
		return []FooterHint{{Button: "Left/Right", Label: "Change"}, {Button: "A", Label: a}, back}
	case setKindAction:
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Open"}, back}
	case setKindText:
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Edit"}, back}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, back}
}

// ── Routing hooks ─────────────────────────────────────────────────────────

// setSceneMouse routes a click while Settings or Plugins is on screen
// (main.go, after the top bar and the overlays have had their turn).
func setSceneMouse(mx, my int32, c *Config) bool {
	name := setHubSceneName(c)
	switch {
	case setIsHubScene(name):
		return setHubMouse(mx, my, c)
	case name == plugSceneName:
		return plugMouse(mx, my, c)
	}
	return false
}

// setSceneWheel routes the mouse wheel the same way.
func setSceneWheel(dy int32, c *Config) bool {
	name := setHubSceneName(c)
	switch {
	case setIsHubScene(name):
		setHubWheel(dy)
		return true
	case name == plugSceneName:
		plugWheel(dy)
		return true
	}
	return false
}

// setFooterHints is the footer of the Settings and Plugins screens
// (extras.go footerHints asks first), so the controls legend and the footer
// always describe what the focused control does.
func setFooterHints(scene SceneConfig) ([]FooterHint, bool) {
	switch {
	case setIsHubScene(scene.Name):
		return setHubHints(), true
	case scene.Name == plugSceneName:
		return plugHints(), true
	}
	return nil, false
}

// ── Scene registration ────────────────────────────────────────────────────

type setHubScene struct{ sceneBase }

func (setHubScene) render(r *sdl.Renderer, c *Config) bool {
	setHubRender(r, c)
	return true
}

func (setHubScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool { return setHubKey(e, c) }

func (setHubScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return setHubPad(e, c)
}

func init() {
	for _, name := range setHubSceneNames {
		registerScene(setHubScene{sceneBase{name: name}})
	}
}
