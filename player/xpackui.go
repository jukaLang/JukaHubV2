package main

// xpackui.go — the Experience packs gallery.
//
// Every pack is a card with a miniature of the Home screen it makes — drawn
// from the pack's own palette, glows and arrangement, not a screenshot — and
// the focused pack's card on the right says, line by line, what switching to it
// changes. A applies it at once; Y takes the last switch back (the previous
// look, not "the default"); X exports the pack as a .jkpack to share; the last
// card imports one from the card or Downloads. The built-in packs are there
// from the first frame, the user's and the plugins' as soon as the folder has
// been read off the render thread.
//
// Pad: D-pad moves between cards (Right from the last column goes to the card's
// buttons), A applies or presses, X exports, Y undoes, B goes back. Keys:
// arrows, Enter/Space, X/E export, Y/U undo, I import, Esc/Backspace back.
// Mouse and touch: tap a card to focus it, tap it again or a button to act.

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
)

// xpackSceneName is the gallery's scene, injected in memory (xpackroute.go).
const xpackSceneName = "ExperiencePacks"

// xpackPreview is what a card's miniature Home is drawn with. live means the
// pack keeps the current theme, so the miniature uses the colours on screen.
type xpackPreview struct {
	live        bool
	bg, surface sdl.Color
	raised      sdl.Color
	accent      sdl.Color
	text        sdl.Color
	bloom       sdl.Color
	bloom2      sdl.Color
	success     sdl.Color
	arrangement string
}

// xpackCard is one card of the gallery, built from a pack when the list
// changes, never per frame.
type xpackCard struct {
	entry    xpackEntry
	isImport bool
	summary  string   // the line under the name
	byline   string   // where it comes from, who made it, its version
	lines    []string // what applying it changes
	pins     []string // the screens it pins that exist here (scene names)
	preview  xpackPreview
}

// xpackUI is the gallery's state and last frame's geometry.
var xpackUI struct {
	visit       context.Context
	gen         int
	cards       []xpackCard
	cols        int
	cursor      int
	top         int32
	inPane      bool
	button      int
	grid        sdl.Rect
	cardRects   []sdl.Rect
	buttonRects []sdl.Rect
	buttons     []xpackKitButton
	buttonsKey  xpackButtonsKey
	undoName    string
	undoOK      bool
	status      string
	descWrap    xpackKitWrap
	sheet       xpackKitSheet
	importsGen  int
}

// xpackImports is the import sheet's file list, read off the render thread.
var xpackImports struct {
	mu      sync.Mutex
	items   []xpackImportItem
	loading bool
	gen     int
}

// xpackBusy is set while an export or an import is writing files.
var xpackBusy atomic.Bool

// xpackOpenGallery opens the Experience packs screen (Settings, Quick Settings
// and the Tools hub call it).
func xpackOpenGallery(config *Config) {
	if config == nil {
		return
	}
	xpackEnsurePlatformScenes(config)
	idx := sceneIndexExact(config, xpackSceneName)
	if idx < 0 {
		return
	}
	qs.open = false
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	}
}

// xpackEnsureVisit refreshes the gallery when the screen is (re)entered.
func xpackEnsureVisit(c *Config) {
	ctx := sceneWorkContext()
	if ctx == xpackUI.visit {
		return
	}
	xpackUI.visit = ctx
	xpackUI.inPane, xpackUI.button = false, 0
	xpackUI.sheet.open = false
	// The folder is read only on a real visit: a render test drawing the page
	// must not start disk work that outlives it.
	if currentSceneWorkName() == xpackSceneName {
		xpackScanAsync()
	}
	xpackRefreshUndo(c)
	xpackRebuildCards(c)
	if id := xpackActiveID(c); id != "" {
		for i, card := range xpackUI.cards {
			if card.entry.Manifest.ID == id {
				xpackUI.cursor = i
				break
			}
		}
	}
}

// xpackRefreshUndo re-reads what Undo would take back, and the header's line.
func xpackRefreshUndo(c *Config) {
	xpackUI.undoName, xpackUI.undoOK = xpackUndoLabel(c)
	xpackUI.status = ""
	if name := xpackActiveName(c); name != "" {
		xpackUI.status = "Active: " + name
	}
	xpackUI.buttonsKey = xpackButtonsKey{}
}

// xpackRebuildCards rebuilds the cards from the current pack list, keeping the
// focus on the same pack.
func xpackRebuildCards(c *Config) {
	keep := ""
	if xpackUI.cursor >= 0 && xpackUI.cursor < len(xpackUI.cards) {
		keep = xpackUI.cards[xpackUI.cursor].entry.Manifest.ID
	}
	xpackUI.gen = xpackStoreGen()
	entries := xpackEntries()
	cards := make([]xpackCard, 0, len(entries)+1)
	for _, e := range entries {
		cards = append(cards, xpackMakeCard(c, e))
	}
	cards = append(cards, xpackCard{isImport: true})
	xpackUI.cards = cards
	for i, card := range cards {
		if keep != "" && card.entry.Manifest.ID == keep {
			xpackUI.cursor = i
		}
	}
	if xpackUI.cursor >= len(cards) {
		xpackUI.cursor = len(cards) - 1
	}
	if xpackUI.cursor < 0 {
		xpackUI.cursor = 0
	}
	xpackUI.buttonsKey = xpackButtonsKey{}
}

// xpackMakeCard describes a pack for the gallery (main thread: it reads the
// theme catalogue).
func xpackMakeCard(c *Config, e xpackEntry) xpackCard {
	card := xpackCard{entry: e, lines: xpackDescribeLines(c, e), preview: xpackPreviewFor(e)}
	m := e.Manifest
	var parts []string
	switch {
	case e.ThemePreset != nil:
		parts = append(parts, e.ThemePreset.Name)
	case m.Theme == xpackThemeDefault:
		parts = append(parts, "Hub palette")
	case m.Theme != "":
		if p, ok := themePresets[m.Theme]; ok {
			parts = append(parts, p.Name)
		}
	}
	if m.HomeLayout != "" && m.HomeLayout != homeArrangementCustom {
		parts = append(parts, homeArrangementLabel(m.HomeLayout))
	}
	if s := m.Sounds; s != nil {
		if s.Enabled != nil && !*s.Enabled {
			parts = append(parts, "quiet")
		} else {
			parts = append(parts, "sounds on")
		}
	}
	if len(e.Scenes) > 0 {
		parts = append(parts, xpackKitCount(len(e.Scenes), "screen", "screens"))
	}
	card.summary = strings.Join(parts, " · ")
	if card.summary == "" {
		card.summary = m.Description
	}
	by := "Built in"
	switch {
	case e.Source == xpackSourceUser:
		by = "Your pack"
	case strings.HasPrefix(e.Source, xpackSourcePlugin):
		by = "From the " + strings.TrimPrefix(e.Source, xpackSourcePlugin) + " plugin"
	}
	if m.Author != "" && e.Source != xpackSourceBuiltin {
		by += " · by " + m.Author
	}
	if m.Version != "" {
		by += " · v" + m.Version
	}
	card.byline = by
	for _, name := range m.Pinned {
		if sceneIndexExact(c, name) >= 0 {
			card.pins = append(card.pins, name)
		}
	}
	return card
}

// xpackDescribeLines is what applying e changes, one line per part.
func xpackDescribeLines(c *Config, e xpackEntry) []string {
	m := e.Manifest
	var lines []string
	switch {
	case e.ThemePreset != nil:
		lines = append(lines, "Theme: "+e.ThemePreset.Name+" (its own)")
	case m.Theme == xpackThemeDefault:
		lines = append(lines, "Theme: JukaHub's own palette")
	case m.Theme != "":
		if p, ok := themePresets[m.Theme]; ok {
			kind := ""
			if p.Kind == themeKindConversion {
				kind = " · total conversion"
			}
			lines = append(lines, "Theme: "+p.Name+kind)
		} else {
			lines = append(lines, "Theme: "+m.Theme+" (not installed)")
		}
	}
	switch m.HomeLayout {
	case homeArrangementCustom:
		lines = append(lines, "Home: your own layout")
	case homeArrangementDashboard, homeArrangementGrid:
		lines = append(lines, "Home: "+homeArrangementLabel(m.HomeLayout))
	}
	switch {
	case e.WallpaperPath != "":
		lines = append(lines, "Wallpaper: "+filepath.Base(e.WallpaperPath))
	case m.Wallpaper == xpackWallpaperNone:
		lines = append(lines, "Wallpaper: none, Home's own backdrop")
	}
	if s := m.Sounds; s != nil {
		if s.Enabled != nil && !*s.Enabled {
			lines = append(lines, "Interface sounds: off")
		} else {
			lines = append(lines, "Interface sounds: on · "+strconv.Itoa(s.Level)+"%")
		}
	}
	start := "Home"
	if m.StartScene != "" {
		start = sceneDisplayName(m.StartScene)
		if sceneIndexExact(c, m.StartScene) < 0 {
			start += " (not on this device)"
		}
	}
	lines = append(lines, "Opens on: "+start)
	switch m.Screensaver {
	case "dock":
		lines = append(lines, "Idle screen: the clock dock")
	case "stars":
		lines = append(lines, "Idle screen: the starfield")
	}
	if m.InputProfile != "" {
		lines = append(lines, "Controls: "+m.InputProfile+" profile")
	}
	if len(m.Pinned) > 0 {
		names := make([]string, 0, len(m.Pinned))
		for _, name := range m.Pinned {
			names = append(names, sceneDisplayName(name))
		}
		lines = append(lines, "Start menu: "+strings.Join(names, ", ")+" first")
	}
	if n := len(m.Favorites); n > 0 {
		lines = append(lines, "Pins "+xpackKitCount(n, "favourite", "favourites"))
	}
	for _, sc := range e.Scenes {
		lines = append(lines, "Adds a screen: "+sc.Name)
	}
	return lines
}

// xpackPreviewFor is the palette a card's miniature uses.
func xpackPreviewFor(e xpackEntry) xpackPreview {
	pv := xpackPreview{arrangement: e.Manifest.HomeLayout}
	var p ThemePreset
	switch {
	case e.ThemePreset != nil:
		p = *e.ThemePreset
	case e.Manifest.Theme == xpackThemeDefault:
		p = GetThemePreset("hub")
	case e.Manifest.Theme != "":
		found, ok := themePresets[e.Manifest.Theme]
		if !ok {
			pv.live = true
			return pv
		}
		p = found
	default:
		pv.live = true
		return pv
	}
	pv.bg = hexRGBA(p.Background)
	pv.surface = hexRGBA(p.SurfaceAlt)
	pv.raised = hexRGBA(p.SurfaceRaised)
	pv.accent = hexRGBA(p.Info)
	pv.text = hexRGBA(p.TextPrimary)
	pv.success = hexRGBA(p.Success)
	pv.bloom, pv.bloom2 = pv.accent, themeDefaultBloom2
	if c, ok := parseHexColor(p.Bloom); ok {
		pv.bloom = c
	}
	if c, ok := parseHexColor(p.Bloom2); ok {
		pv.bloom2 = c
	}
	if pv.arrangement == "" && p.Arrangement != "" {
		pv.arrangement = p.Arrangement
	}
	return pv
}

// xpackFocusedCard is the card the focus is on.
func xpackFocusedCard() (xpackCard, bool) {
	if xpackUI.cursor < 0 || xpackUI.cursor >= len(xpackUI.cards) {
		return xpackCard{}, false
	}
	return xpackUI.cards[xpackUI.cursor], true
}

// ── Actions ───────────────────────────────────────────────────────────────

// xpackApplyCard switches to a card's pack.
func xpackApplyCard(c *Config, card xpackCard) {
	note, err := xpackApply(c, card.entry)
	if err != nil {
		ShowToast("Could not switch packs: "+err.Error(), ToastKindWarning)
		return
	}
	PlayActivateSound()
	xpackRefreshUndo(c)
	msg := card.entry.Manifest.Name + " is on · Y undoes it"
	if note != "" {
		msg += " · skipped " + note
	}
	ShowToast(msg, ToastKindSuccess)
}

// xpackUndoPress takes the last pack switch back.
func xpackUndoPress(c *Config) {
	name, err := xpackUndo(c)
	if err != nil {
		ShowToast("Nothing to undo", ToastKindInfo)
		return
	}
	PlayBackSound()
	xpackRefreshUndo(c)
	ShowToast("Took back "+name+" · your previous look is back", ToastKindInfo)
}

// xpackExportFocused writes the focused pack to the card or Downloads.
func xpackExportFocused() {
	card, ok := xpackFocusedCard()
	if !ok || card.isImport {
		return
	}
	if !xpackBusy.CompareAndSwap(false, true) {
		ShowToast("Still writing the last pack", ToastKindInfo)
		return
	}
	entry, dir := card.entry, bkupExportDir()
	PlayActivateSound()
	ShowToast("Exporting "+entry.Manifest.Name+"…", ToastKindInfo)
	go func() {
		defer xpackBusy.Store(false)
		path, err := xpackExport(entry, dir)
		if err != nil {
			ShowToast("Export failed: "+firstLine(err.Error()), ToastKindWarning)
			return
		}
		ShowToast("Saved "+filepath.Base(path)+" in "+dir, ToastKindSuccess)
	}()
}

// xpackOpenImports opens the import sheet and lists the .jkpack files found
// off the render thread.
func xpackOpenImports() {
	xpackUI.sheet = xpackKitSheet{
		open:     true,
		title:    "Import a pack",
		subtitle: "Packs (.jkpack) on the card, in its backup folder or in Downloads",
	}
	xpackUI.importsGen = -1
	xpackImports.mu.Lock()
	if xpackImports.loading {
		xpackImports.mu.Unlock()
		return
	}
	xpackImports.loading = true
	xpackImports.gen++
	xpackImports.mu.Unlock()
	PlayActivateSound()
	go func() {
		items := xpackFindImports()
		xpackImports.mu.Lock()
		xpackImports.items, xpackImports.loading = items, false
		xpackImports.gen++
		xpackImports.mu.Unlock()
	}()
}

// xpackSyncImportRows rebuilds the sheet's rows when the file list changed.
func xpackSyncImportRows() {
	xpackImports.mu.Lock()
	gen, loading := xpackImports.gen, xpackImports.loading
	if gen == xpackUI.importsGen {
		xpackImports.mu.Unlock()
		return
	}
	items := append([]xpackImportItem(nil), xpackImports.items...)
	xpackImports.mu.Unlock()
	xpackUI.importsGen = gen
	rows := make([]xpackKitRow, 0, len(items)+1)
	switch {
	case loading:
		rows = append(rows, xpackKitRow{Kind: xpackKitItem, Label: "Looking for packs…", Disabled: true})
	case len(items) == 0:
		rows = append(rows, xpackKitRow{Kind: xpackKitItem, Label: "No .jkpack files found",
			Detail: "Copy one into " + bkupExportDir(), Disabled: true})
	}
	for _, it := range items {
		row := xpackKitRow{Kind: xpackKitItem, ID: it.Path, Label: it.Name,
			Detail: filepath.Base(it.Path) + " · " + humanBytes(it.Size) + " · " + it.ModTime.Format("Jan 2, 2006")}
		if it.Problem != "" {
			row.Detail, row.Tone, row.Disabled = "Can't use it: "+it.Problem, xpackKitToneWarn, true
		}
		rows = append(rows, row)
	}
	xpackUI.sheet.rows = rows
	xpackUI.sheet.list.clamp(rows)
}

// xpackImportFocused imports the file the sheet's focus is on.
func xpackImportFocused() {
	row, ok := xpackUI.sheet.list.focused(xpackUI.sheet.rows)
	if !ok || row.Disabled || row.ID == "" {
		return
	}
	if !xpackBusy.CompareAndSwap(false, true) {
		ShowToast("Still writing the last pack", ToastKindInfo)
		return
	}
	path := row.ID
	xpackUI.sheet.open = false
	PlayActivateSound()
	go func() {
		defer xpackBusy.Store(false)
		e, err := xpackImport(path)
		if err != nil {
			ShowToast("Could not import "+filepath.Base(path)+": "+firstLine(err.Error()), ToastKindWarning)
			return
		}
		ShowToast("Imported "+e.Manifest.Name+" · it is in the list now", ToastKindSuccess)
	}()
}

// xpackPress is A on the gallery.
func xpackPress(c *Config) {
	if xpackUI.inPane {
		xpackActivateButton(c, xpackUI.button)
		return
	}
	card, ok := xpackFocusedCard()
	if !ok {
		return
	}
	if card.isImport {
		xpackOpenImports()
		return
	}
	xpackApplyCard(c, card)
}

// xpackActivateButton presses the card's button i.
func xpackActivateButton(c *Config, i int) {
	if i < 0 || i >= len(xpackUI.buttons) {
		return
	}
	b := xpackUI.buttons[i]
	if b.Disabled {
		return
	}
	card, ok := xpackFocusedCard()
	if !ok {
		return
	}
	switch {
	case b.ID == "apply":
		xpackApplyCard(c, card)
	case b.ID == "undo":
		xpackUndoPress(c)
	case b.ID == "export":
		xpackExportFocused()
	case b.ID == "import":
		xpackOpenImports()
	case strings.HasPrefix(b.ID, "open:"):
		name := strings.TrimPrefix(b.ID, "open:")
		if idx := sceneIndexExact(c, name); idx >= 0 {
			if reason, blocked := offlineSceneBlock(name); blocked {
				ShowToast(reason, ToastKindWarning)
				return
			}
			PlayActivateSound()
			changeSceneTo(c, idx)
		}
	}
}

// xpackBack is B: from the buttons back to the cards, from the cards off the
// screen.
func xpackBack(c *Config) {
	if xpackUI.inPane {
		xpackUI.inPane = false
		PlayBackSound()
		return
	}
	goBackScene(c)
}

// xpackNav moves the focus: between cards in the grid, or between the focused
// card's buttons.
func xpackNav(c *Config, dx, dy int) {
	if xpackUI.inPane {
		next, moved := xpackKitMoveButton(xpackUI.button, len(xpackUI.buttons), dx, dy)
		switch {
		case moved:
			xpackUI.button = next
			PlayNavSound()
		case dx < 0 && xpackUI.button%2 == 0:
			xpackUI.inPane = false
			PlayNavSound()
		}
		return
	}
	cols := max(xpackUI.cols, 1)
	n := len(xpackUI.cards)
	switch {
	case dy < 0:
		if xpackUI.cursor-cols < 0 {
			focusEngine.ClearFocus()
			focusChromeBar()
			return
		}
		xpackUI.cursor -= cols
	case dy > 0:
		if xpackUI.cursor+cols >= n {
			if xpackUI.cursor/cols == (n-1)/cols {
				return
			}
			xpackUI.cursor = n - 1
		} else {
			xpackUI.cursor += cols
		}
	case dx < 0:
		if xpackUI.cursor%cols == 0 {
			return
		}
		xpackUI.cursor--
	case dx > 0:
		if xpackUI.cursor%cols == cols-1 || xpackUI.cursor == n-1 {
			xpackEnterPane()
			return
		}
		xpackUI.cursor++
	default:
		return
	}
	xpackUI.buttonsKey = xpackButtonsKey{}
	PlayNavSound()
}

// xpackEnterPane moves the focus onto the focused card's buttons.
func xpackEnterPane() {
	if len(xpackUI.buttons) == 0 {
		return
	}
	xpackUI.inPane, xpackUI.button = true, 0
	PlayNavSound()
}

// ── Input ─────────────────────────────────────────────────────────────────

func xpackKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	xpackEnsureVisit(c)
	if xpackUI.sheet.open {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			xpackSheetMove(-1)
		case sdl.K_DOWN:
			xpackSheetMove(1)
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			xpackImportFocused()
		case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
			xpackUI.sheet.open = false
			PlayBackSound()
		default:
			return false
		}
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		xpackNav(c, 0, -1)
	case sdl.K_DOWN:
		xpackNav(c, 0, 1)
	case sdl.K_LEFT:
		xpackNav(c, -1, 0)
	case sdl.K_RIGHT:
		xpackNav(c, 1, 0)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		xpackPress(c)
	case sdl.K_x, sdl.K_e:
		xpackExportFocused()
	case sdl.K_y, sdl.K_u:
		xpackUndoPress(c)
	case sdl.K_i:
		xpackOpenImports()
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		xpackBack(c)
	default:
		return false
	}
	return true
}

func xpackPad(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_GUIDE,
		sdl.CONTROLLER_BUTTON_LEFTSTICK, sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		return false
	}
	xpackEnsureVisit(c)
	if xpackUI.sheet.open {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			xpackSheetMove(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			xpackSheetMove(1)
		case sdl.CONTROLLER_BUTTON_A:
			xpackImportFocused()
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			xpackUI.sheet.open = false
			PlayBackSound()
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		xpackNav(c, 0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		xpackNav(c, 0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		xpackNav(c, -1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		xpackNav(c, 1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		xpackPress(c)
	case sdl.CONTROLLER_BUTTON_X:
		xpackExportFocused()
	case sdl.CONTROLLER_BUTTON_Y:
		xpackUndoPress(c)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		xpackBack(c)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		if !xpackUI.inPane && xpackUI.cursor > 0 {
			xpackUI.cursor--
			xpackUI.buttonsKey = xpackButtonsKey{}
			PlayNavSound()
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if !xpackUI.inPane && xpackUI.cursor < len(xpackUI.cards)-1 {
			xpackUI.cursor++
			xpackUI.buttonsKey = xpackButtonsKey{}
			PlayNavSound()
		}
	}
	return true
}

// xpackSheetMove steps the import sheet's focus.
func xpackSheetMove(delta int) {
	if xpackUI.sheet.list.move(xpackUI.sheet.rows, delta) {
		PlayNavSound()
	}
}

// xpackMouse handles a click or a tap on the gallery.
func xpackMouse(mx, my int32, c *Config) bool {
	xpackEnsureVisit(c)
	if xpackUI.sheet.open {
		if !pointInRect(mx, my, xpackUI.sheet.rect) {
			xpackUI.sheet.open = false
			PlayBackSound()
			return true
		}
		if i, _ := xpackUI.sheet.list.hit(mx, my); i >= 0 {
			if i == xpackUI.sheet.list.cursor {
				xpackImportFocused()
			} else if xpackKitFocusable(xpackUI.sheet.rows, i) {
				xpackUI.sheet.list.cursor = i
				PlayNavSound()
			}
		}
		return true
	}
	if i := xpackKitButtonHit(xpackUI.buttonRects, mx, my); i >= 0 {
		xpackUI.inPane, xpackUI.button = true, i
		xpackActivateButton(c, i)
		return true
	}
	if !pointInRect(mx, my, xpackUI.grid) {
		return false
	}
	for i, r := range xpackUI.cardRects {
		if r.W <= 0 || !pointInRect(mx, my, r) {
			continue
		}
		if i == xpackUI.cursor && !xpackUI.inPane {
			xpackPress(c)
		} else {
			xpackUI.cursor, xpackUI.inPane = i, false
			xpackUI.buttonsKey = xpackButtonsKey{}
			PlayNavSound()
		}
		return true
	}
	return false
}

// xpackWheel scrolls the cards a row per notch, or the open sheet.
func xpackWheel(dy int32) bool {
	if xpackUI.sheet.open {
		xpackUI.sheet.list.wheel(xpackUI.sheet.rows, dy)
		return true
	}
	cols := max(xpackUI.cols, 1)
	switch {
	case dy > 0 && xpackUI.cursor-cols >= 0:
		xpackUI.cursor -= cols
	case dy < 0 && xpackUI.cursor+cols < len(xpackUI.cards):
		xpackUI.cursor += cols
	default:
		return true
	}
	xpackUI.inPane = false
	xpackUI.buttonsKey = xpackButtonsKey{}
	return true
}

// xpackHints is the gallery's footer.
func xpackHints() []FooterHint {
	if xpackUI.sheet.open {
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Import"}, {Button: "B", Label: "Close"}}
	}
	if xpackUI.inPane {
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Packs"}}
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	if card, ok := xpackFocusedCard(); ok && card.isImport {
		hints = append(hints, FooterHint{Button: "A", Label: "Import"})
	} else {
		hints = append(hints, FooterHint{Button: "A", Label: "Apply"}, FooterHint{Button: "X", Label: "Export"})
	}
	if xpackUI.undoOK {
		hints = append(hints, FooterHint{Button: "Y", Label: "Undo"})
	}
	return append(hints, FooterHint{Button: "B", Label: "Back"})
}

// ── Drawing ───────────────────────────────────────────────────────────────

// xpackRender draws the gallery.
func xpackRender(renderer *sdl.Renderer, c *Config) {
	xpackEnsureVisit(c)
	if xpackStoreGen() != xpackUI.gen {
		xpackRebuildCards(c)
	}
	if xpackUI.sheet.open {
		xpackSyncImportRows()
	}
	f, ok := xpackKitLoadFonts(c)
	if !ok {
		drawAppBackdrop(renderer, c, screenWidth, screenHeight)
		xpackKitFinish(renderer, c)
		return
	}
	header := UIPanelHeader("Experience packs", "Theme, Home, sounds and start screen, switched together — and undone in one press", "sparkles")
	header.Status = xpackUI.status
	contentY, bottom, margin := xpackKitFrame(renderer, c, xpackSceneName, header)
	gridW := (screenWidth - 2*margin) * 58 / 100
	grid := sdl.Rect{X: margin, Y: contentY, W: gridW, H: bottom - contentY}
	paneX := margin + gridW + 18
	pane := sdl.Rect{X: paneX, Y: contentY, W: screenWidth - margin - paneX, H: bottom - contentY}
	xpackDrawGrid(renderer, c, f, grid)
	xpackDrawPane(renderer, c, f, pane)
	xpackUI.sheet.draw(renderer, c, f)
	xpackKitFinish(renderer, c)
}

// xpackDrawGrid draws the cards, scrolled so the focused one is in view.
func xpackDrawGrid(renderer *sdl.Renderer, c *Config, f xpackKitFonts, area sdl.Rect) {
	xpackUI.grid = area
	n := len(xpackUI.cards)
	cols := 2
	if area.W < 460 {
		cols = 1
	}
	xpackUI.cols = cols
	const gap = int32(14)
	cw := (area.W - gap*int32(cols-1)) / int32(cols)
	previewH := cw * 9 / 20
	ch := previewH + int32(f.title.Height()) + int32(f.small.Height()) + 34
	rows := (n + cols - 1) / cols
	if y := int32(xpackUI.cursor/cols) * (ch + gap); y < xpackUI.top {
		xpackUI.top = y
	} else if y+ch > xpackUI.top+area.H {
		xpackUI.top = y + ch - area.H
	}
	if total := int32(rows)*(ch+gap) - gap; xpackUI.top > total-area.H {
		xpackUI.top = total - area.H
	}
	if xpackUI.top < 0 {
		xpackUI.top = 0
	}
	if cap(xpackUI.cardRects) < n {
		xpackUI.cardRects = make([]sdl.Rect, n)
	}
	xpackUI.cardRects = xpackUI.cardRects[:n]
	// The clip leaves room for the focused card's lifted ring.
	clip := sdl.Rect{X: area.X - 6, Y: area.Y - 6, W: area.W + 12, H: area.H + 12}
	_ = renderer.SetClipRect(&clip)
	ring := !xpackUI.inPane && !xpackUI.sheet.open && !xpackKitChrome()
	for i, card := range xpackUI.cards {
		r := sdl.Rect{
			X: area.X + int32(i%cols)*(cw+gap),
			Y: area.Y + int32(i/cols)*(ch+gap) - xpackUI.top,
			W: cw,
			H: ch,
		}
		xpackUI.cardRects[i] = sdl.Rect{}
		if r.Y+r.H < area.Y || r.Y > area.Y+area.H {
			continue
		}
		xpackUI.cardRects[i] = r
		xpackDrawCard(renderer, c, f, card, r, previewH, ring && i == xpackUI.cursor, i == xpackUI.cursor)
	}
	_ = renderer.SetClipRect(nil)
}

// xpackDrawCard paints one card: the miniature (or the import well), the name
// and the summary line, and an "On" pill on the pack in force.
func xpackDrawCard(renderer *sdl.Renderer, c *Config, f xpackKitFonts, card xpackCard, r sdl.Rect, previewH int32, focused, selected bool) {
	if focused {
		drawGlassFocusGlow(renderer, r)
	}
	hovered := !focused && (selected || pointInRect(mouseX, mouseY, r))
	drawGlassSurface(renderer, r, homeTileRadius, glassState{Focused: focused, Hovered: hovered})
	const pad = int32(12)
	inner := sdl.Rect{X: r.X + pad, Y: r.Y + pad, W: r.W - 2*pad, H: previewH}
	title, detail := "Import a pack", "From a .jkpack on the card or in Downloads"
	if card.isImport {
		fillRoundedRect(renderer, inner.X, inner.Y, inner.W, inner.H, 12, glassA(8))
		strokeRoundedRect(renderer, inner.X, inner.Y, inner.W, inner.H, 12, 1, glassA(40))
		size := min(inner.H/2, 44)
		DrawHomeIcon(renderer, "plus", sdl.Rect{X: inner.X + (inner.W-size)/2, Y: inner.Y + (inner.H-size)/2, W: size, H: size}, focused)
	} else {
		xpackDrawPreview(renderer, c, card.preview, inner)
		title, detail = card.entry.Manifest.Name, card.summary
		if card.entry.Manifest.ID == xpackActiveID(c) {
			pw, _ := setPillSize(f.small, "On")
			setPillDraw(renderer, f.small, "On", inner.X+inner.W-pw-8, inner.Y+8, glassAccent(), contrastText(glassAccent()))
		}
	}
	ty := inner.Y + inner.H + 10
	ty = xpackKitText(renderer, f.title, title, r.X+pad, ty, r.W-2*pad, ColorTextPrimary())
	xpackKitText(renderer, f.small, detail, r.X+pad, ty+2, r.W-2*pad, ColorTextSecondary())
}

// xpackDrawPreview draws a miniature of the Home screen a pack makes: its
// backdrop and two glows, the header strip, the cards in the pack's
// arrangement with the first one ringed in the accent, and the footer band.
func xpackDrawPreview(renderer *sdl.Renderer, c *Config, pv xpackPreview, r sdl.Rect) {
	if r.W < 40 || r.H < 30 {
		return
	}
	bg, surface, raised, accent, text := pv.bg, pv.surface, pv.raised, pv.accent, pv.text
	bloom, bloom2 := pv.bloom, pv.bloom2
	if pv.live {
		bg, surface, raised = ColorBackground, ColorSurfaceAlt, ColorSurfaceRaised
		accent, text = glassAccent(), ColorTextPrimary()
		bloom, bloom2 = themeBackdropBlooms(c)
	}
	arrangement := pv.arrangement
	if arrangement == "" {
		arrangement = homeArrangement(c)
	}
	bg.A, surface.A, raised.A, accent.A, text.A = 255, 255, 255, 255, 255
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, bg)

	// The glows stay inside the miniature: clip to it within whatever clip the
	// caller set.
	hadClip := renderer.IsClipEnabled()
	prev := renderer.GetClipRect()
	clip := r
	if hadClip {
		clip = glassIntersect(prev, r)
	}
	if clip.W > 0 && clip.H > 0 {
		_ = renderer.SetClipRect(&clip)
		blob := r.W * 3 / 4
		homeGlowBlob(renderer, sdl.Rect{X: r.X - blob/3, Y: r.Y - blob/3, W: blob, H: blob}, bloom, 48)
		homeGlowBlob(renderer, sdl.Rect{X: r.X + r.W - blob*2/3, Y: r.Y + r.H - blob/2, W: blob, H: blob}, bloom2, 40)
		if hadClip {
			_ = renderer.SetClipRect(&prev)
		} else {
			_ = renderer.SetClipRect(nil)
		}
	}

	pad := max(r.W/24, 4)
	// Header: the brand pill on the left, the status cluster on the right.
	hh := max(r.H/12, 3)
	fillRoundedRect(renderer, r.X+pad, r.Y+pad, r.W/5, hh, hh/2, WithAlpha(text, 170))
	for i := int32(0); i < 3; i++ {
		fillRoundedRect(renderer, r.X+r.W-pad-(i+1)*(hh*2+3), r.Y+pad, hh*2, hh, hh/2, WithAlpha(text, 90))
	}
	body := sdl.Rect{X: r.X + pad, Y: r.Y + pad*2 + hh, W: r.W - 2*pad, H: r.H - pad*4 - hh*2}
	// Footer band.
	fillRoundedRect(renderer, r.X+pad, r.Y+r.H-pad-hh, r.W-2*pad, hh, hh/2, WithAlpha(text, 50))
	if body.W <= 0 || body.H <= 0 {
		return
	}
	gap := max(r.W/80, 2)
	// Resting cards are the palette's card surface, the focused one its raised
	// surface with the accent ring, as on Home.
	tile := func(t sdl.Rect, focus bool) {
		if t.W <= 0 || t.H <= 0 {
			return
		}
		rad := min(min(t.W, t.H)/5, 6)
		fill := WithAlpha(surface, 230)
		if focus {
			fill = WithAlpha(raised, 240)
		}
		fillRoundedRect(renderer, t.X, t.Y, t.W, t.H, rad, fill)
		strokeRoundedRect(renderer, t.X, t.Y, t.W, t.H, rad, 1, WithAlpha(text, 36))
		if focus {
			strokeRoundedRect(renderer, t.X, t.Y, t.W, t.H, rad, 2, accent)
		}
	}
	cellW := (body.W - 3*gap) / 4
	cellH := (body.H - 2*gap) / 3
	cell := func(col, row, spanC, spanR int32) sdl.Rect {
		return sdl.Rect{
			X: body.X + col*(cellW+gap),
			Y: body.Y + row*(cellH+gap),
			W: spanC*cellW + (spanC-1)*gap,
			H: spanR*cellH + (spanR-1)*gap,
		}
	}
	if arrangement == homeArrangementGrid {
		// The hero tile stands two rows tall in the first column.
		tile(cell(0, 0, 1, 2), true)
		for row := int32(0); row < 3; row++ {
			for col := int32(0); col < 4; col++ {
				if col == 0 && row < 2 {
					continue
				}
				tile(cell(col, row, 1, 1), false)
			}
		}
		return
	}
	// The dashboard (and the shipped custom layout it is designed around): a
	// wide Continue card with the Media card beside it, then the app tiles.
	tile(cell(0, 0, 2, 1), true)
	tile(cell(2, 0, 2, 1), false)
	for row := int32(1); row < 3; row++ {
		for col := int32(0); col < 4; col++ {
			tile(cell(col, row, 1, 1), false)
		}
	}
}

// xpackDrawSwatches draws the pack's palette as five chips and returns the y
// below them.
func xpackDrawSwatches(renderer *sdl.Renderer, pv xpackPreview, x, y int32) int32 {
	colors := [5]sdl.Color{pv.bg, pv.surface, pv.raised, pv.accent, pv.success}
	if pv.live {
		colors = [5]sdl.Color{ColorBackground, ColorSurfaceAlt, ColorSurfaceRaised, glassAccent(), ColorSuccess}
	}
	const d = int32(18)
	for i, col := range colors {
		cx := x + d/2 + int32(i)*(d+8)
		cy := y + d/2
		fillCircle(renderer, cx, cy, d/2+2, sdl.Color{R: 0, G: 0, B: 0, A: 110})
		fillCircle(renderer, cx, cy, d/2+1, sdl.Color{R: 255, G: 255, B: 255, A: 140})
		col.A = 255
		fillCircle(renderer, cx, cy, d/2, col)
	}
	return y + d + 4
}

// xpackButtonsKey is what the card's buttons were built for.
type xpackButtonsKey struct {
	pack, active, undo string
	undoOK, isImport   bool
	valid              bool
}

// xpackPaneButtons is the focused card's buttons, rebuilt when the focus or the
// pack in force changes.
func xpackPaneButtons(c *Config, card xpackCard) {
	key := xpackButtonsKey{
		pack:     card.entry.Manifest.ID,
		active:   xpackActiveID(c),
		undo:     xpackUI.undoName,
		undoOK:   xpackUI.undoOK,
		isImport: card.isImport,
		valid:    true,
	}
	if key == xpackUI.buttonsKey {
		return
	}
	xpackUI.buttonsKey = key
	var b []xpackKitButton
	if card.isImport {
		b = append(b, xpackKitButton{ID: "import", Label: "Choose a .jkpack", Primary: true})
	} else {
		label := "Apply"
		if card.entry.Manifest.ID == xpackActiveID(c) {
			label = "Apply again"
		}
		b = append(b, xpackKitButton{ID: "apply", Label: label, Primary: true})
		b = append(b, xpackKitButton{ID: "export", Label: "Export"})
	}
	if xpackUI.undoOK {
		b = append(b, xpackKitButton{ID: "undo", Label: "Undo " + xpackUI.undoName})
	}
	for i, name := range card.pins {
		if i >= 2 {
			break
		}
		b = append(b, xpackKitButton{ID: "open:" + name, Label: "Open " + sceneDisplayName(name)})
	}
	xpackUI.buttons = b
	if xpackUI.button >= len(b) {
		xpackUI.button = 0
	}
	if len(b) == 0 {
		xpackUI.inPane = false
	}
}

// xpackDrawPane draws the focused card's details on the right.
func xpackDrawPane(renderer *sdl.Renderer, c *Config, f xpackKitFonts, area sdl.Rect) {
	xpackUI.buttonRects = xpackUI.buttonRects[:0]
	card, ok := xpackFocusedCard()
	if !ok || area.W <= 80 || area.H <= 80 {
		return
	}
	xpackPaneButtons(c, card)
	UICard(renderer, area, DefaultUICardStyle())
	const pad = int32(20)
	x, w := area.X+pad, area.W-2*pad
	y := area.Y + 16
	bh := max(int32(f.body.Height())+20, 44)
	rows := int32((len(xpackUI.buttons) + 1) / 2)
	buttonsY := area.Y + area.H - pad - rows*(bh+10) + 10
	limit := buttonsY - 10
	if card.isImport {
		y = xpackKitText(renderer, f.big, "Import a pack", x, y, w, ColorTextPrimary()) + 10
		for _, line := range xpackUI.descWrap.get(f.body, "Packs travel as .jkpack files: copy one to the card's top level, its JukaHub Backups folder or your Downloads, then choose it here. It is copied into JukaHub's packs folder and validated before it can change anything.", w, 8) {
			if y+int32(f.body.Height()) > limit {
				break
			}
			y = xpackKitText(renderer, f.body, line, x, y, w, ColorTextSecondary()) + 2
		}
	} else {
		m := card.entry.Manifest
		y = xpackKitText(renderer, f.big, m.Name, x, y, w, ColorTextPrimary()) + 2
		y = xpackKitText(renderer, f.small, card.byline, x, y, w, ColorTextTertiary()) + 10
		pvH := min(w*9/20, area.H/4)
		xpackDrawPreview(renderer, c, card.preview, sdl.Rect{X: x, Y: y, W: w, H: pvH})
		y += pvH + 10
		y = xpackDrawSwatches(renderer, card.preview, x, y) + 6
		for _, line := range xpackUI.descWrap.get(f.body, m.Description, w, 3) {
			if y+int32(f.body.Height()) > limit {
				break
			}
			y = xpackKitText(renderer, f.body, line, x, y, w, ColorTextSecondary()) + 2
		}
		y += 6
		for _, line := range card.lines {
			if y+int32(f.small.Height()) > limit {
				break
			}
			y = xpackKitText(renderer, f.small, line, x, y, w, ColorTextPrimary()) + 3
		}
		for _, p := range card.entry.Problems {
			if y+int32(f.small.Height()) > limit {
				break
			}
			y = xpackKitText(renderer, f.small, "Skipped "+p, x, y, w, WarningInk()) + 3
		}
	}
	focus := -1
	if xpackUI.inPane && !xpackUI.sheet.open && !xpackKitChrome() {
		focus = xpackUI.button
	}
	xpackKitDrawButtons(renderer, c, f, xpackUI.buttons, &xpackUI.buttonRects, x, buttonsY, w, area.Y+area.H-pad+10, focus)
}

// ── Scene registration ────────────────────────────────────────────────────

type xpackScene struct{ sceneBase }

func (xpackScene) render(r *sdl.Renderer, c *Config) bool {
	xpackRender(r, c)
	return true
}

func (xpackScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool { return xpackKey(e, c) }

func (xpackScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return xpackPad(e, c)
}

func init() { registerScene(xpackScene{sceneBase{name: xpackSceneName}}) }
