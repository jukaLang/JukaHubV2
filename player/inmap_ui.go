package main

// inmap_ui.go — the Control profiles screen, and the scene plumbing it shares
// with Local play (hotseat_ui.go).
//
// Every profile is a row: the five built in, the user's files and the ones
// plugins registered. The card beside the list shows the focused profile —
// who owns it, where it applies, what it does, every line the reader or the
// validator could not use (with its line and column) and each mapping as
// "trigger → target" — and its buttons: Use, Edit (the file opens in Files'
// editor and the list reloads when the editor closes; a built-in profile is
// copied first), Duplicate, Export and Delete. The last row imports a profile
// someone shared: a .jkinput file in Downloads, on the card or in home.
//
// Pad: Up/Down choose, A uses the profile (or imports), Right reaches its
// buttons, Y edits, X duplicates, L1/R1 page, B goes back. Keys: arrows,
// Enter, Delete, Page Up/Down, Esc/Backspace. Mouse and touch: tap a row, a
// button or a file to import; the wheel scrolls.

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// inmapProfilesScene is the Control profiles screen's scene name.
const inmapProfilesScene = "InputProfiles"

// inmapCardButton is one button of the profile card.
type inmapCardButton uint8

const (
	inmapCardUse inmapCardButton = iota
	inmapCardEdit
	inmapCardDuplicate
	inmapCardExport
	inmapCardDelete
)

// inmapUIMapLine is one mapping as the card shows it, already fitted.
type inmapUIMapLine struct {
	trigger string
	target  string
	ok      bool
}

// inmapUIProblemLine is one problem, wrapped to the card.
type inmapUIProblemLine struct {
	lines   []string
	warning bool
}

// inmapImportFile is one file the import sheet offers.
type inmapImportFile struct {
	path string
	name string
	dir  string
}

// inmapUIState is the screen's focus, its cached text and the last frame's
// geometry.
type inmapUIState struct {
	visit   context.Context
	cursor  int
	inCard  bool
	button  int
	listTop int32
	// editing is set while Files' editor is open on a profile, so the list
	// reloads when it closes. confirm is the profile a first Delete armed.
	editing bool
	confirm *inmapProfile
	// The list and its second lines, rebuilt when the registry changes.
	gen      int
	list     []*inmapProfile
	rowLines []string
	// The focused card's text, rebuilt when the focus, the list or the
	// card's width changes.
	cardFor  *inmapProfile
	cardGen  int
	cardW    int32
	cardMeta string
	cardDesc []string
	cardMaps []inmapUIMapLine
	cardErrs []inmapUIProblemLine
	moreN    int
	moreText string
	helpW    int32
	help     []string
	// The last frame's geometry, for the mouse.
	rowRects    []sdl.Rect
	buttonRects []sdl.Rect
	buttonIDs   []inmapCardButton
	listRect    sdl.Rect
}

var inmapUI = inmapUIState{gen: -1}

// inmapImportUI is the import sheet. The folder scan runs in the background
// and hands its result over under mu.
var inmapImportUI struct {
	open    bool
	cursor  int
	top     int
	mu      sync.Mutex
	loading bool
	files   []inmapImportFile
	rows    []sdl.Rect
	card    sdl.Rect
}

// The headers' icons, built once.
var (
	inmapHeaderGlyph = func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
		DrawHomeIcon(r, "gamepad", box, false)
	}
	inmapImportGlyph = func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
		DrawHomeIcon(r, "download", box, false)
	}
)

// inmapImportHelpText is the card the import row shows.
const inmapImportHelpText = "Someone shared a control profile? Put the .jkinput file in Downloads, " +
	"on the card or in your home folder, then press A here to pick it. Imported profiles land in " +
	"the profiles folder, where Files can open and change them."

// ── Scenes ───────────────────────────────────────────────────────────────

// inmapEnsureScenes adds the Control profiles and Local play scenes in memory
// (at startup and after every reload), like ensureAlarmScene: no shipped
// config carries them and the user's config file is never rewritten for it.
func inmapEnsureScenes(config *Config) {
	if config == nil {
		return
	}
	for _, name := range []string{inmapProfilesScene, hotseatScene, stickCalScene} {
		if sceneIndexExact(config, name) < 0 {
			config.Scenes = append(config.Scenes, SceneConfig{Name: name})
		}
	}
}

// inmapSceneDisplayName is the header name of the two screens (extras.go's
// sceneDisplayName asks first), "" for any other scene.
func inmapSceneDisplayName(name string) string {
	switch name {
	case inmapProfilesScene:
		return "Control profiles"
	case hotseatScene:
		return "Local play"
	case stickCalScene:
		return "Stick calibration"
	}
	return ""
}

// inmapOpenProfiles opens the Control profiles screen.
func inmapOpenProfiles(config *Config) {
	if config == nil {
		return
	}
	inmapEnsureScenes(config)
	idx := sceneIndexExact(config, inmapProfilesScene)
	if idx < 0 || idx == currentSceneIndex {
		return
	}
	qs.open = false
	changeSceneTo(config, idx)
}

// inmapScenesClick routes a click on either screen (main.go).
func inmapScenesClick(mx, my int32, config *Config) bool {
	switch inmapCurrentScene(config) {
	case inmapProfilesScene:
		return inmapUIClick(mx, my, config)
	case hotseatScene:
		return hotseatUIClick(mx, my, config)
	case stickCalScene:
		return stickCalClick(mx, my, config)
	}
	return false
}

// inmapScenesWheel routes the mouse wheel on either screen (main.go).
func inmapScenesWheel(config *Config, dy int32) bool {
	switch inmapCurrentScene(config) {
	case inmapProfilesScene:
		inmapUIWheel(dy)
		return true
	case hotseatScene, stickCalScene:
		return true
	}
	return false
}

// inmapScenesFooterHints is the footer of either screen (extras.go's
// footerHints asks first).
func inmapScenesFooterHints(name string) ([]FooterHint, bool) {
	switch name {
	case inmapProfilesScene:
		return inmapUIHints(), true
	case hotseatScene:
		return hotseatUIHints(), true
	case stickCalScene:
		return stickCalHints(), true
	}
	return nil, false
}

// ── State ────────────────────────────────────────────────────────────────

// inmapUIEnsureVisit resets the focus when the screen is entered and reloads
// the profile files in the background. A redraw that is not a visit (a render
// test) reads nothing from the disk.
func inmapUIEnsureVisit() {
	ctx := sceneWorkContext()
	if ctx == inmapUI.visit {
		return
	}
	inmapUI.visit = ctx
	inmapUI.inCard, inmapUI.button, inmapUI.confirm, inmapUI.listTop = false, 0, nil, 0
	inmapImportUI.open = false
	if currentSceneWorkName() == inmapProfilesScene {
		runSceneTask(func(context.Context) { inmapReloadFiles() })
	}
}

// inmapUIRefresh rebuilds the cached list when the registry changed, keeping
// the focus on the same profile.
func inmapUIRefresh() {
	gen := inmapGeneration()
	if gen == inmapUI.gen && inmapUI.list != nil {
		return
	}
	var focused *inmapProfile
	if inmapUI.cursor >= 0 && inmapUI.cursor < len(inmapUI.list) {
		focused = inmapUI.list[inmapUI.cursor]
	}
	inmapUI.gen = gen
	inmapUI.list = inmapProfileList()
	inmapUI.rowLines = inmapUI.rowLines[:0]
	for _, p := range inmapUI.list {
		inmapUI.rowLines = append(inmapUI.rowLines, inmapRowLine(p))
	}
	if focused != nil {
		for i, p := range inmapUI.list {
			if p == focused || (p.name == focused.name && p.path == focused.path) {
				inmapUI.cursor = i
				break
			}
		}
	}
	inmapUI.cursor = clampIndex(inmapUI.cursor, len(inmapUI.list)+1)
	inmapUI.cardFor = nil
}

// inmapRowLine is a row's second line: its problems, or what it is for.
func inmapRowLine(p *inmapProfile) string {
	if n := p.errorCount(); n > 0 {
		return inmapCount(n, "line", "lines") + " cannot be used · " + inmapCount(p.usableBindings(), "mapping", "mappings")
	}
	if p.description != "" {
		return p.description
	}
	return inmapCount(p.usableBindings(), "mapping", "mappings") + inmapScopeSuffix(p)
}

// inmapCount writes "1 mapping" or "3 mappings".
func inmapCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// inmapUIFocused is the focused profile, nil on the import row.
func inmapUIFocused() *inmapProfile {
	if inmapUI.cursor >= 0 && inmapUI.cursor < len(inmapUI.list) {
		return inmapUI.list[inmapUI.cursor]
	}
	return nil
}

// inmapProfileIcon is a row's glyph: who owns the profile.
func inmapProfileIcon(p *inmapProfile) string {
	switch {
	case p.builtin:
		return "gamepad"
	case p.external:
		return "plugin"
	}
	return "edit"
}

// The two button rows a card can have, built once: the card is drawn every
// frame and its callers only read them.
var (
	inmapCardButtonsOwned = []inmapCardButton{inmapCardUse, inmapCardEdit, inmapCardDuplicate, inmapCardExport, inmapCardDelete}
	inmapCardButtonsOther = []inmapCardButton{inmapCardUse, inmapCardEdit, inmapCardDuplicate, inmapCardExport}
)

// inmapCardButtons are the buttons a profile's card offers: Delete only on
// the user's own files.
func inmapCardButtons(p *inmapProfile) []inmapCardButton {
	if inmapOwnedByUser(p) {
		return inmapCardButtonsOwned
	}
	return inmapCardButtonsOther
}

// inmapCardButtonLabel is a card button's label.
func inmapCardButtonLabel(p *inmapProfile, b inmapCardButton) string {
	switch b {
	case inmapCardUse:
		return "Use"
	case inmapCardEdit:
		if inmapOwnedByUser(p) {
			return "Edit"
		}
		return "Edit copy"
	case inmapCardDuplicate:
		return "Duplicate"
	case inmapCardExport:
		return "Export"
	case inmapCardDelete:
		if inmapUI.confirm == p {
			return "Delete?"
		}
		return "Delete"
	}
	return ""
}

// inmapHotkeyStatus is the header's read-out.
func inmapHotkeyStatus() string {
	if inmapHotkeyEnabled() {
		return "Switch: hold Select + R1"
	}
	return "Switch hotkey off"
}

// ── Actions ──────────────────────────────────────────────────────────────

// inmapUIMove steps the list focus by delta rows (the import row included).
func inmapUIMove(delta int) {
	n := len(inmapUI.list) + 1
	next := clampIndex(inmapUI.cursor+delta, n)
	if next == inmapUI.cursor {
		return
	}
	inmapUI.cursor = next
	inmapUI.confirm = nil
	PlayNavSound()
}

// inmapUIUp is Up on the list: the row above, or the top bar from the first.
func inmapUIUp() {
	inmapUI.inCard = false
	if inmapUI.cursor <= 0 {
		focusEngine.ClearFocus()
		focusChromeBar()
		return
	}
	inmapUIMove(-1)
}

// inmapUIEnterCard moves the focus onto the card's buttons.
func inmapUIEnterCard() {
	if inmapUIFocused() == nil {
		return
	}
	inmapUI.inCard = true
	inmapUI.button = 0
	PlayNavSound()
}

// inmapUIButtonMove steps the card's button focus; left of the first button
// is the list again.
func inmapUIButtonMove(delta int) {
	p := inmapUIFocused()
	if p == nil {
		inmapUI.inCard = false
		return
	}
	next := inmapUI.button + delta
	if next < 0 {
		inmapUI.inCard = false
		PlayNavSound()
		return
	}
	if next >= len(inmapCardButtons(p)) {
		return
	}
	inmapUI.button = next
	PlayNavSound()
}

// inmapUIActivate is A on the list: use the profile, or open the import
// sheet on the last row.
func inmapUIActivate(c *Config) {
	if p := inmapUIFocused(); p != nil {
		inmapUIAction(c, inmapCardUse)
		return
	}
	inmapImportOpen()
}

// inmapUIPress is A on a card button.
func inmapUIPress(c *Config) {
	p := inmapUIFocused()
	if p == nil {
		return
	}
	buttons := inmapCardButtons(p)
	if inmapUI.button < 0 || inmapUI.button >= len(buttons) {
		return
	}
	inmapUIAction(c, buttons[inmapUI.button])
}

// inmapUIAction runs one of the card's actions on the focused profile.
func inmapUIAction(c *Config, b inmapCardButton) {
	p := inmapUIFocused()
	if p == nil {
		return
	}
	if b != inmapCardDelete {
		inmapUI.confirm = nil
	}
	switch b {
	case inmapCardUse:
		PlayActivateSound()
		inmapChoose(c, p, true)
	case inmapCardEdit:
		inmapUIEdit(p)
	case inmapCardDuplicate:
		q, err := inmapDuplicate(p)
		if err != nil {
			ShowToast("Could not copy "+p.name+": "+firstLine(err.Error()), ToastKindError)
			return
		}
		PlayActivateSound()
		inmapUIFocus(q)
		if q != nil {
			ShowToast("Copied to "+q.name+" — Edit opens it in the editor", ToastKindSuccess)
		}
	case inmapCardExport:
		path, err := inmapExport(p)
		if err != nil {
			ShowToast("Could not export "+p.name+": "+firstLine(err.Error()), ToastKindError)
			return
		}
		PlayActivateSound()
		ShowToast("Saved "+filepath.Base(path)+" in "+filepath.Dir(path), ToastKindSuccess)
	case inmapCardDelete:
		if !inmapOwnedByUser(p) {
			return
		}
		if inmapUI.confirm != p {
			inmapUI.confirm = p
			PlayNavSound()
			ShowToast("Press Delete again to remove "+p.name, ToastKindWarning)
			return
		}
		inmapUI.confirm = nil
		if err := inmapDelete(c, p); err != nil {
			ShowToast("Could not delete "+p.name+": "+firstLine(err.Error()), ToastKindError)
			return
		}
		PlayBackSound()
		inmapUI.inCard = false
		ShowToast("Deleted "+p.name, ToastKindInfo)
	}
}

// inmapUIEdit opens a profile's file in Files' editor. A built-in or plugin
// profile is copied into the profiles folder first, and the copy is opened.
func inmapUIEdit(p *inmapProfile) {
	target := p
	if !inmapOwnedByUser(p) {
		q, err := inmapDuplicate(p)
		if err != nil || q == nil {
			msg := "the copy could not be read back"
			if err != nil {
				msg = firstLine(err.Error())
			}
			ShowToast("Could not copy "+p.name+": "+msg, ToastKindError)
			return
		}
		target = q
		inmapUIFocus(q)
		ShowToast("Editing a copy: "+q.name, ToastKindInfo)
	}
	if fileEditorOpen(target.path) {
		inmapUI.editing = true
		PlayActivateSound()
	}
}

// inmapUIFocus puts the list focus on p.
func inmapUIFocus(p *inmapProfile) {
	if p == nil {
		return
	}
	inmapUIRefresh()
	for i, q := range inmapUI.list {
		if q == p {
			inmapUI.cursor = i
			return
		}
	}
}

// inmapUIBack is B: from the card to the list, from the list off the screen.
func inmapUIBack(c *Config) {
	if inmapUI.inCard {
		inmapUI.inCard = false
		PlayBackSound()
		return
	}
	goBackScene(c)
}

// ── Import sheet ─────────────────────────────────────────────────────────

// inmapImportOpen opens the sheet and looks for profile files off the render
// thread.
func inmapImportOpen() {
	inmapImportUI.open = true
	inmapImportUI.cursor, inmapImportUI.top = 0, 0
	inmapImportUI.mu.Lock()
	inmapImportUI.loading = true
	inmapImportUI.files = nil
	inmapImportUI.mu.Unlock()
	dirs := inmapImportDirs()
	profilesDir, _ := inmapProfilesDir()
	scan := func(context.Context) {
		paths := inmapScanImports(dirs, profilesDir)
		files := make([]inmapImportFile, 0, len(paths))
		for _, path := range paths {
			files = append(files, inmapImportFile{path: path, name: filepath.Base(path), dir: filepath.Dir(path)})
		}
		inmapImportUI.mu.Lock()
		inmapImportUI.files = files
		inmapImportUI.loading = false
		inmapImportUI.mu.Unlock()
	}
	if !runSceneTask(scan) {
		inmapImportUI.mu.Lock()
		inmapImportUI.loading = false
		inmapImportUI.mu.Unlock()
	}
	PlayActivateSound()
}

// inmapImportClose closes the sheet.
func inmapImportClose() {
	inmapImportUI.open = false
	PlayBackSound()
}

// inmapImportFiles is the scan's result so far.
func inmapImportFiles() ([]inmapImportFile, bool) {
	inmapImportUI.mu.Lock()
	defer inmapImportUI.mu.Unlock()
	return inmapImportUI.files, inmapImportUI.loading
}

// inmapImportMove steps the sheet's focus.
func inmapImportMove(delta int) {
	files, _ := inmapImportFiles()
	next := clampIndex(inmapImportUI.cursor+delta, len(files))
	if next != inmapImportUI.cursor {
		inmapImportUI.cursor = next
		PlayNavSound()
	}
}

// inmapImportPick imports the focused file.
func inmapImportPick() {
	files, _ := inmapImportFiles()
	if inmapImportUI.cursor < 0 || inmapImportUI.cursor >= len(files) {
		return
	}
	f := files[inmapImportUI.cursor]
	p, err := inmapImport(f.path)
	if err != nil {
		ShowToast("Could not import "+f.name+": "+firstLine(err.Error()), ToastKindError)
		return
	}
	inmapImportUI.open = false
	PlayActivateSound()
	inmapUIFocus(p)
	ShowToast("Imported "+p.name, ToastKindSuccess)
}

// ── Input ────────────────────────────────────────────────────────────────

func inmapUIPad(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_GUIDE,
		sdl.CONTROLLER_BUTTON_LEFTSTICK, sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		return false
	}
	inmapUIEnsureVisit()
	inmapUIRefresh()
	if inmapImportUI.open {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			inmapImportMove(-1)
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			inmapImportMove(1)
		case sdl.CONTROLLER_BUTTON_A:
			inmapImportPick()
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			inmapImportClose()
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		inmapUIUp()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		inmapUI.inCard = false
		inmapUIMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		if inmapUI.inCard {
			inmapUIButtonMove(-1)
		}
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if inmapUI.inCard {
			inmapUIButtonMove(1)
		} else {
			inmapUIEnterCard()
		}
	case sdl.CONTROLLER_BUTTON_A:
		if inmapUI.inCard {
			inmapUIPress(c)
		} else {
			inmapUIActivate(c)
		}
	case sdl.CONTROLLER_BUTTON_Y:
		inmapUIAction(c, inmapCardEdit)
	case sdl.CONTROLLER_BUTTON_X:
		inmapUIAction(c, inmapCardDuplicate)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		inmapUI.inCard = false
		inmapUIMove(-5)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		inmapUI.inCard = false
		inmapUIMove(5)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		inmapUIBack(c)
	}
	return true
}

func inmapUIKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	inmapUIEnsureVisit()
	inmapUIRefresh()
	if inmapImportUI.open {
		switch e.Keysym.Sym {
		case sdl.K_UP:
			inmapImportMove(-1)
		case sdl.K_DOWN:
			inmapImportMove(1)
		case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			inmapImportPick()
		case sdl.K_ESCAPE, sdl.K_BACKSPACE:
			inmapImportClose()
		}
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		inmapUIUp()
	case sdl.K_DOWN:
		inmapUI.inCard = false
		inmapUIMove(1)
	case sdl.K_LEFT:
		if inmapUI.inCard {
			inmapUIButtonMove(-1)
		}
	case sdl.K_RIGHT, sdl.K_TAB:
		if inmapUI.inCard {
			inmapUIButtonMove(1)
		} else {
			inmapUIEnterCard()
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if inmapUI.inCard {
			inmapUIPress(c)
		} else {
			inmapUIActivate(c)
		}
	case sdl.K_DELETE:
		inmapUIAction(c, inmapCardDelete)
	case sdl.K_PAGEUP:
		inmapUIMove(-5)
	case sdl.K_PAGEDOWN:
		inmapUIMove(5)
	case sdl.K_HOME:
		inmapUIMove(-len(inmapUI.list) - 1)
	case sdl.K_END:
		inmapUIMove(len(inmapUI.list) + 1)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		inmapUIBack(c)
	default:
		return false
	}
	return true
}

// inmapUIClick handles a click or a tap on the screen.
func inmapUIClick(mx, my int32, c *Config) bool {
	inmapUIRefresh()
	if inmapImportUI.open {
		for i, r := range inmapImportUI.rows {
			if r.W > 0 && pointInRect(mx, my, r) {
				inmapImportUI.cursor = i
				inmapImportPick()
				return true
			}
		}
		if !pointInRect(mx, my, inmapImportUI.card) {
			inmapImportClose()
		}
		return true
	}
	for i, r := range inmapUI.buttonRects {
		if r.W > 0 && hitTouchTarget(r, mx, my) && i < len(inmapUI.buttonIDs) {
			inmapUI.inCard = true
			inmapUI.button = i
			inmapUIAction(c, inmapUI.buttonIDs[i])
			return true
		}
	}
	for i, r := range inmapUI.rowRects {
		if r.W <= 0 || !pointInRect(mx, my, r) || !pointInRect(mx, my, inmapUI.listRect) {
			continue
		}
		if i == inmapUI.cursor && !inmapUI.inCard {
			inmapUIActivate(c)
			return true
		}
		inmapUI.cursor = i
		inmapUI.inCard = false
		inmapUI.confirm = nil
		PlayNavSound()
		return true
	}
	return true
}

// inmapUIWheel moves the focus three rows per notch.
func inmapUIWheel(dy int32) {
	if inmapImportUI.open {
		switch {
		case dy > 0:
			inmapImportMove(-1)
		case dy < 0:
			inmapImportMove(1)
		}
		return
	}
	switch {
	case dy > 0:
		inmapUIMove(-3)
	case dy < 0:
		inmapUIMove(3)
	}
}

// inmapUIHints is the screen's footer.
func inmapUIHints() []FooterHint {
	switch {
	case inmapImportUI.open:
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Import"}, {Button: "B", Label: "Close"}}
	case inmapUI.inCard:
		return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Press"}, {Button: "B", Label: "List"}}
	case inmapUI.cursor >= len(inmapUI.list):
		return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Import"}, {Button: "B", Label: "Back"}}
	}
	return []FooterHint{{Button: "D-Pad", Label: "Navigate"}, {Button: "A", Label: "Use"},
		{Button: "Y", Label: "Edit"}, {Button: "X", Label: "Duplicate"}, {Button: "B", Label: "Back"}}
}

// ── Drawing ──────────────────────────────────────────────────────────────

// inmapUIRender draws the screen.
func inmapUIRender(renderer *sdl.Renderer, c *Config) {
	drawAppBackdrop(renderer, c, screenWidth, screenHeight)
	inmapUIEnsureVisit()
	if inmapUI.editing && !fileEditor.open {
		// The editor closed over a profile: read the folder again.
		inmapUI.editing = false
		inmapReloadFiles()
	}
	inmapEnsureValid(c)
	inmapUIRefresh()
	title := FontForRole(c, TypeBody)
	small := FontForRole(c, TypeLabel)
	big := FontForRole(c, TypeHeading)
	body := FontForRole(c, TypeLabel)
	if title == nil || small == nil || big == nil || body == nil {
		renderStatusBar(renderer, c)
		renderFooter(renderer, c)
		return
	}
	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + scaleY(8, screenHeight)
	bottom := screenHeight - bottomBarHeight(inmapProfilesScene) - 16
	header := UIPageHeader{
		Title:     "Control profiles",
		Subtitle:  "What each button does, on every screen or just some",
		TitleSize: menuTitleSize,
		Glyph:     inmapHeaderGlyph,
		Status:    inmapHotkeyStatus(),
		X:         margin,
		Y:         top,
		W:         screenWidth - 2*margin,
	}
	contentY := UIPageHeaderDraw(renderer, c, header) + 16
	const gap = int32(20)
	listW := (screenWidth - 2*margin) * 40 / 100
	list := sdl.Rect{X: margin, Y: contentY, W: listW, H: bottom - contentY}
	card := sdl.Rect{X: margin + listW + gap, Y: contentY, W: screenWidth - 2*margin - listW - gap, H: bottom - contentY}
	inmapUIDrawList(renderer, title, small, list)
	inmapUIDrawCard(renderer, c, small, big, body, card)
	if inmapImportUI.open {
		inmapUIDrawImport(renderer, c, title, small)
	}
	renderStatusBar(renderer, c)
	renderFooter(renderer, c)
}

// inmapUIDrawList draws the profile rows and the import row.
func inmapUIDrawList(renderer *sdl.Renderer, title, small *ttf.Font, list sdl.Rect) {
	inmapUI.listRect = list
	n := len(inmapUI.list) + 1
	th, sh := int32(title.Height()), int32(small.Height())
	rowH := th + sh + 24
	if rowH < 64 {
		rowH = 64
	}
	const gap = int32(8)
	if cap(inmapUI.rowRects) < n {
		inmapUI.rowRects = make([]sdl.Rect, n)
	}
	inmapUI.rowRects = inmapUI.rowRects[:n]
	fy := int32(inmapUI.cursor) * (rowH + gap)
	if fy < inmapUI.listTop {
		inmapUI.listTop = fy
	}
	if fy+rowH > inmapUI.listTop+list.H {
		inmapUI.listTop = fy + rowH - list.H
	}
	if inmapUI.listTop < 0 {
		inmapUI.listTop = 0
	}
	chrome := chromeTier(focusEngine.ElementIndex()) != 0
	effective, chosen := inmapEffective(), inmapChosen()
	clip := list
	_ = renderer.SetClipRect(&clip)
	for i := 0; i < n; i++ {
		r := sdl.Rect{X: list.X, Y: list.Y + int32(i)*(rowH+gap) - inmapUI.listTop, W: list.W, H: rowH}
		inmapUI.rowRects[i] = r
		if r.Y+r.H < list.Y || r.Y > list.Y+list.H {
			continue
		}
		selected := i == inmapUI.cursor
		focused := selected && !inmapUI.inCard && !chrome && !inmapImportUI.open
		hovered := !selected && pointInRect(mouseX, mouseY, r)
		switch {
		case focused:
			UIListRow(renderer, r, glassRowRadius, true, false)
		case selected:
			// The card belongs to this row: keep it marked while the card's
			// buttons hold the focus.
			drawGlassSurface(renderer, r, glassRowRadius, glassState{Hovered: true, NoLift: true})
			fillRoundedRect(renderer, r.X+6, r.Y+14, 4, r.H-28, 2, glassAccent())
		default:
			drawGlassSurface(renderer, r, glassRowRadius, glassState{Hovered: hovered, NoLift: true})
		}
		badge := rowH - 26
		if badge > 40 {
			badge = 40
		}
		glyph := drawGlassBadge(renderer, sdl.Rect{X: r.X + 16, Y: r.Y + (r.H-badge)/2, W: badge, H: badge})
		x := r.X + 16 + badge + 14
		right := r.X + r.W - 16
		ty := r.Y + (r.H-th-sh-2)/2
		if i >= len(inmapUI.list) {
			DrawHomeIcon(renderer, "download", glyph, selected)
			drawText(renderer, title, ellipsize(title, "Import a profile", right-x), x, ty, ColorTextPrimary(), textAlignLeft)
			drawText(renderer, small, ellipsize(small, "A .jkinput file someone shared", right-x), x, ty+th+2, ColorTextSecondary(), textAlignLeft)
			continue
		}
		p := inmapUI.list[i]
		DrawHomeIcon(renderer, inmapProfileIcon(p), glyph, selected)
		pill := ""
		switch p {
		case effective:
			pill = "On"
		case chosen:
			pill = "Chosen"
		}
		if pill != "" {
			pw, ph := measureText(small, pill)
			pr := sdl.Rect{X: right - pw - 20, Y: r.Y + (r.H-ph-8)/2, W: pw + 20, H: ph + 8}
			fillRoundedRect(renderer, pr.X, pr.Y, pr.W, pr.H, pr.H/2, WithAlpha(glassAccent(), 60))
			strokeRoundedRect(renderer, pr.X, pr.Y, pr.W, pr.H, pr.H/2, 1, glassAccent())
			drawText(renderer, small, pill, pr.X+10, pr.Y+4, ColorTextPrimary(), textAlignLeft)
			right = pr.X - 10
		}
		avail := right - x
		if avail < 40 {
			avail = 40
		}
		drawText(renderer, title, ellipsize(title, p.name, avail), x, ty, ColorTextPrimary(), textAlignLeft)
		line, ink := "", ColorTextSecondary()
		if i < len(inmapUI.rowLines) {
			line = inmapUI.rowLines[i]
		}
		if p.errorCount() > 0 {
			ink = WarningInk()
		}
		drawText(renderer, small, ellipsize(small, line, avail), x, ty+th+2, ink, textAlignLeft)
	}
	_ = renderer.SetClipRect(nil)
}

// inmapUIBuildCard rebuilds the focused card's text when the focus, the list
// or the card's width changed, so drawing a frame builds no strings.
func inmapUIBuildCard(p *inmapProfile, body, small *ttf.Font, w int32) {
	if inmapUI.cardFor == p && inmapUI.cardGen == inmapUI.gen && inmapUI.cardW == w {
		return
	}
	inmapUI.cardFor, inmapUI.cardGen, inmapUI.cardW = p, inmapUI.gen, w
	owner := "Built in"
	switch {
	case p.external:
		owner = "From a plugin"
	case !p.builtin:
		owner = "Yours · " + filepath.Base(p.path)
	}
	scope := "Every screen"
	if !p.everywhere() {
		scope = "On" + strings.TrimPrefix(inmapScopeSuffix(p), " ·")
	}
	inmapUI.cardMeta = owner + " · " + scope + " · " + inmapCount(p.usableBindings(), "mapping", "mappings")
	desc := p.description
	if desc == "" {
		desc = "No description"
	}
	inmapUI.cardDesc = wrapTextLines(body, desc, w, 2)
	col := w * 46 / 100
	inmapUI.cardMaps = inmapUI.cardMaps[:0]
	for i := range p.bindings {
		b := &p.bindings[i]
		trig := inmapTriggerLabel(b.trigger)
		if b.scenes != nil {
			names := make([]string, 0, len(b.scenes))
			for _, s := range b.scenes {
				names = append(names, sceneDisplayName(s))
			}
			trig += " · " + strings.Join(names, ", ")
		}
		inmapUI.cardMaps = append(inmapUI.cardMaps, inmapUIMapLine{
			trigger: ellipsize(body, trig, col-12),
			target:  ellipsize(body, "→ "+inmapTargetLabel(b.target), w-col),
			ok:      b.ok,
		})
	}
	inmapUI.cardErrs = inmapUI.cardErrs[:0]
	for _, pr := range p.problems {
		text := "Line " + strconv.Itoa(pr.line) + ", column " + strconv.Itoa(pr.col) + ": " + pr.msg
		inmapUI.cardErrs = append(inmapUI.cardErrs, inmapUIProblemLine{
			lines:   wrapTextLines(small, text, w, 2),
			warning: pr.warning,
		})
	}
	inmapUI.moreN, inmapUI.moreText = -1, ""
}

// inmapUIMoreText is "+ N more", rebuilt only when N changes.
func inmapUIMoreText(n int) string {
	if n != inmapUI.moreN {
		inmapUI.moreN = n
		inmapUI.moreText = "+ " + strconv.Itoa(n) + " more"
	}
	return inmapUI.moreText
}

// inmapUIDrawCard draws the focused profile's card, or the import row's help.
func inmapUIDrawCard(renderer *sdl.Renderer, c *Config, small, big, body *ttf.Font, card sdl.Rect) {
	inmapUI.buttonRects = inmapUI.buttonRects[:0]
	inmapUI.buttonIDs = inmapUI.buttonIDs[:0]
	if card.W <= 0 || card.H <= 0 {
		return
	}
	UICard(renderer, card, DefaultUICardStyle())
	const pad = int32(22)
	x := card.X + pad
	w := card.W - 2*pad
	y := card.Y + 18
	bottom := card.Y + card.H - pad
	bh := int32(body.Height()) + 2
	sh := int32(small.Height())
	p := inmapUIFocused()
	if p == nil {
		drawText(renderer, big, ellipsize(big, "Import a profile", w), x, y, ColorTextPrimary(), textAlignLeft)
		y += int32(big.Height()) + 10
		if inmapUI.helpW != w || inmapUI.help == nil {
			inmapUI.helpW = w
			inmapUI.help = wrapTextLines(body, inmapImportHelpText, w, 6)
		}
		for _, line := range inmapUI.help {
			drawText(renderer, body, line, x, y, ColorTextSecondary(), textAlignLeft)
			y += bh
		}
		return
	}
	inmapUIBuildCard(p, body, small, w)
	drawText(renderer, big, ellipsize(big, p.name, w), x, y, ColorTextPrimary(), textAlignLeft)
	y += int32(big.Height()) + 4
	drawText(renderer, small, ellipsize(small, inmapUI.cardMeta, w), x, y, ColorTextTertiary(), textAlignLeft)
	y += sh + 10
	for _, line := range inmapUI.cardDesc {
		drawText(renderer, body, line, x, y, ColorTextSecondary(), textAlignLeft)
		y += bh
	}
	y += 8

	buttons := inmapCardButtons(p)
	const bgap = int32(10)
	bw := (w - bgap*int32(len(buttons)-1)) / int32(len(buttons))
	btnH := sh + 18
	if btnH < 40 {
		btnH = 40
	}
	chrome := chromeTier(focusEngine.ElementIndex()) != 0
	for i, id := range buttons {
		r := sdl.Rect{X: x + int32(i)*(bw+bgap), Y: y, W: bw, H: btnH}
		UIButton(renderer, c, r, inmapCardButtonLabel(p, id), UIButtonStyle{Font: small}, UIButtonState{
			Focused: inmapUI.inCard && i == inmapUI.button && !chrome && !inmapImportUI.open,
			Hovered: pointInRect(mouseX, mouseY, r),
		})
		inmapUI.buttonRects = append(inmapUI.buttonRects, r)
		inmapUI.buttonIDs = append(inmapUI.buttonIDs, id)
	}
	y += btnH + 18

	// What could not be used comes first: it is why someone opens the card.
	if len(inmapUI.cardErrs) > 0 && y+sh+24 < bottom {
		UISectionLabel(renderer, c, "Problems", x, y, w, nil)
		y += sh + 20
		for _, pr := range inmapUI.cardErrs {
			ink := ColorTextTertiary()
			if !pr.warning {
				ink = WarningInk()
			}
			for _, line := range pr.lines {
				if y+sh > bottom {
					break
				}
				drawText(renderer, small, line, x, y, ink, textAlignLeft)
				y += sh + 2
			}
			y += 4
		}
		y += 6
	}
	if len(inmapUI.cardMaps) == 0 || y+sh+24 >= bottom {
		return
	}
	UISectionLabel(renderer, c, "Mappings", x, y, w, nil)
	y += sh + 20
	col := x + w*46/100
	for i, m := range inmapUI.cardMaps {
		if y+bh > bottom {
			break
		}
		if y+2*bh > bottom && i < len(inmapUI.cardMaps)-1 {
			drawText(renderer, small, inmapUIMoreText(len(inmapUI.cardMaps)-i), x, y, ColorTextTertiary(), textAlignLeft)
			break
		}
		ink := ColorTextPrimary()
		if !m.ok {
			ink = WarningInk()
		}
		drawText(renderer, body, m.trigger, x, y, ink, textAlignLeft)
		drawText(renderer, body, m.target, col, y, ColorTextSecondary(), textAlignLeft)
		y += bh
	}
}

// inmapUIDrawImport draws the import sheet over the screen.
func inmapUIDrawImport(renderer *sdl.Renderer, c *Config, title, small *ttf.Font) {
	drawOverlayScrim(renderer)
	w := min32(screenWidth-2*scaleX(homeSideMargin, screenWidth), 760)
	h := min32(screenHeight-160, 480)
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	inmapImportUI.card = card
	drawOverlayCard(renderer, card)
	header := overlayHeaderOnCard(UIPageHeader{
		Title:     "Import a profile",
		Subtitle:  "A .jkinput file in Downloads, on the card or in home",
		TitleSize: menuTitleSize,
		Glyph:     inmapImportGlyph,
	}, card)
	y := UIPageHeaderDraw(renderer, c, header) + 14
	files, loading := inmapImportFiles()
	body := sdl.Rect{X: card.X + OverlayPadX, Y: y, W: card.W - 2*OverlayPadX, H: card.Y + card.H - OverlayPadX - y}
	inmapImportUI.rows = inmapImportUI.rows[:0]
	switch {
	case loading:
		UIEmptyState(renderer, c, body, "", "Looking for profiles…", "")
		return
	case len(files) == 0:
		UIEmptyState(renderer, c, body, "@download", "No profiles to import",
			"Put a .jkinput file in Downloads, on the card or in your home folder")
		return
	}
	th, sh := int32(title.Height()), int32(small.Height())
	rowH := th + sh + 18
	const gap = int32(6)
	visible := int((body.H + gap) / (rowH + gap))
	if visible < 1 {
		visible = 1
	}
	if inmapImportUI.cursor < inmapImportUI.top {
		inmapImportUI.top = inmapImportUI.cursor
	}
	if inmapImportUI.cursor >= inmapImportUI.top+visible {
		inmapImportUI.top = inmapImportUI.cursor - visible + 1
	}
	for i := inmapImportUI.top; i < len(files) && i < inmapImportUI.top+visible; i++ {
		r := sdl.Rect{X: body.X, Y: body.Y + int32(i-inmapImportUI.top)*(rowH+gap), W: body.W, H: rowH}
		for len(inmapImportUI.rows) < i {
			inmapImportUI.rows = append(inmapImportUI.rows, sdl.Rect{})
		}
		inmapImportUI.rows = append(inmapImportUI.rows, r)
		UIListRow(renderer, r, glassRowRadius, i == inmapImportUI.cursor, pointInRect(mouseX, mouseY, r))
		f := files[i]
		drawText(renderer, title, ellipsize(title, f.name, r.W-32), r.X+16, r.Y+9, ColorTextPrimary(), textAlignLeft)
		drawText(renderer, small, ellipsize(small, f.dir, r.W-32), r.X+16, r.Y+11+th, ColorTextTertiary(), textAlignLeft)
	}
}

// ── Scene registration ───────────────────────────────────────────────────

type inmapProfilesModule struct{ sceneBase }

func (inmapProfilesModule) render(r *sdl.Renderer, c *Config) bool {
	inmapUIRender(r, c)
	return true
}

func (inmapProfilesModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return inmapUIKey(e, c)
}

func (inmapProfilesModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return inmapUIPad(e, c)
}

func init() { registerScene(inmapProfilesModule{sceneBase{name: inmapProfilesScene}}) }
