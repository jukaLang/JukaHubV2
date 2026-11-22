package main

// openwith_ui.go — the "Open with" sheet and the Default apps list
// (openwith.go has the handlers and the defaults).
//
// The sheet is drawn and driven like the emulator picker (emupicker.go): a
// card over whatever screen asked, rows with a name and a line under it, the
// D-pad and the wheel to choose, a click on a row to run it and a click off
// the card to cancel. It rides the picker's hooks in main.go — the picker's
// handlers hand over to this file while the sheet is up — so it works over
// Files, the text page and the reader alike.
//
// Open mode (a file): A opens the file with the row; X makes the row the
// type's default and opens it; Y forgets the default. A click within
// owPickClickGuard of the rows changing chooses nothing; a scene change
// closes the sheet (switchScene → owPickClose).
// Choose mode (a type from Default apps): A or X makes the row the default.
// List mode (Default apps): A changes the highlighted type's default, X
// resets it. B / Back / Esc closes; from Choose it goes back to the list.

import (
	"path/filepath"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type owPickMode uint8

const (
	owModeOpen owPickMode = iota
	owModeChoose
	owModeList
)

var owPick struct {
	open bool
	mode owPickMode
	ctx  string // "files", "editor", "hex", "reader", "defaults"
	// path is the file (open mode) or owTypeProbe(ext) (choose mode).
	path  string
	entry FileEntry
	ext   string
	title string
	// binary: the file is bytes, not text (no Text editor row).
	binary bool
	// saved is the type's default when the sheet opened ("" = JukaHub's
	// choice); choice is JukaHub's own viewer for it.
	saved  string
	choice string
	// asIsName / asIsID: with no default, what takes A on the type when that
	// is not JukaHub's choice (owAsBefore) and its row ("" when not listed).
	asIsName string
	asIsID   string
	rows     []owHandler
	more     []owHandler
	// list mode: the saved extensions, and the list's row while a type's
	// default is being chosen (Back returns to it).
	list       []string
	listCursor int

	cursor  int
	scroll  int
	visible int
	panel   sdl.Rect
	rowHits []gameLibRowHit
	eatUp   bool
	// openedAt is when the rows last changed under the pointer: a click
	// right after (the one that opened the sheet, a double-click's second
	// half) does not choose a row.
	openedAt time.Time
}

// owPickClickGuard is how long after the rows change a click is ignored.
const owPickClickGuard = 400 * time.Millisecond

func owPickOpen() bool { return owPick.open }

func owPickClose() {
	owPick.open = false
	owPick.rows, owPick.more, owPick.list = nil, nil, nil
	owPick.rowHits = owPick.rowHits[:0]
}

func owPickCancel() {
	if !owPick.open {
		return
	}
	if owPick.mode == owModeChoose {
		// Back to the list the type was chosen from.
		owOpenDefaultsList(appConfig)
		PlayBackSound()
		return
	}
	owPickClose()
	PlayBackSound()
}

// owOpenPicker opens "Open <file> with" for a file the caller has resolved.
// ctx is where it was asked from (owBuiltinIDs leaves that viewer out).
func owOpenPicker(config *Config, path string, entry FileEntry, ctx string) {
	if config == nil || path == "" {
		return
	}
	// One question at a time: the emulator picker may already be asking.
	if emuPickUI.open {
		return
	}
	owPrimeNames(config)
	binary := owFileIsBinary(path)
	ext := owExt(path)
	saved := owDefaultFor(config, ext)
	if owIsAppID(saved) {
		if _, ok := owResolve(saved, path, ctx); !ok {
			saved = "" // an app that is gone is not offered as the default
		}
	}
	owPick.open = true
	owPick.mode = owModeOpen
	owPick.ctx = ctx
	owPick.path = path
	owPick.entry = entry
	owPick.ext = ext
	owPick.title = filepath.Base(path)
	owPick.binary = binary
	owPick.saved = saved
	owPick.choice = owJukaChoice(path, binary)
	owPick.rows, owPick.more = owBuildRows(path, binary, ctx, saved)
	owPickNoteAsIs(config)
	owPick.cursor, owPick.scroll = 0, 0
	owPick.rowHits = owPick.rowHits[:0]
	owPick.openedAt = time.Now()
	if len(owPick.rows) == 0 {
		owPickClose()
		showToast("Nothing on this device opens "+filepath.Base(path), ToastInfo())
		return
	}
	PlayActivateSound()
}

// owPickNoteAsIs works out, for a type with no default, what takes A on it
// when that is not JukaHub's choice (owAsBefore), and which row that is: the
// header names it and the row wears the badge.
func owPickNoteAsIs(config *Config) {
	owPick.asIsName, owPick.asIsID = "", ""
	if owPick.saved != "" {
		return
	}
	owNoteImplicit(owPick.ext, owPick.path)
	name, ok := owAsBefore(config, owPick.ext)
	if !ok {
		return
	}
	owPick.asIsName = name
	if op, ok := owImplicitSeen[owPick.ext]; ok {
		owPick.asIsID = op.ID
		return
	}
	if ko, ok := koreaderApp(); ok {
		for _, h := range owPick.rows {
			if h.Kind == owKindApp && isPathWithin(h.App.Launch, ko.Dir) {
				owPick.asIsID = h.ID
				return
			}
		}
	}
}

// owOpenChooser opens the sheet for a type from the Default apps list.
func owOpenChooser(config *Config, ext string) {
	if config == nil || ext == "" {
		return
	}
	probe := owTypeProbe(ext)
	binary := owTypeLooksBinary(probe)
	saved := owDefaultFor(config, ext)
	if owPick.mode == owModeList {
		owPick.listCursor = owPick.cursor
	}
	owPick.open = true
	owPick.mode = owModeChoose
	owPick.ctx = "defaults"
	owPick.path = probe
	owPick.entry = FileEntry{}
	owPick.ext = ext
	owPick.title = ext + " files"
	owPick.binary = binary
	owPick.saved = saved
	owPick.choice = owJukaChoice(probe, binary)
	owPick.rows, owPick.more = owBuildRows(probe, binary, "defaults", saved)
	owPickNoteAsIs(config)
	owPick.cursor, owPick.scroll = 0, 0
	owPick.rowHits = owPick.rowHits[:0]
	owPick.openedAt = time.Now()
	PlayActivateSound()
}

// owShowDefaultApps opens Default apps from a menu or the search.
func owShowDefaultApps(config *Config) {
	if config == nil || emuPickUI.open {
		return
	}
	owPrimeNames(config)
	owPickClose()
	owPick.cursor = 0
	owOpenDefaultsList(config)
	PlayActivateSound()
}

// owOpenDefaultsList opens Default apps: every saved type and its app.
func owOpenDefaultsList(config *Config) {
	if emuPickUI.open {
		return
	}
	if owPick.open && owPick.mode == owModeChoose {
		owPick.cursor = owPick.listCursor
	}
	owPick.open = true
	owPick.mode = owModeList
	owPick.ctx = "defaults"
	owPick.path, owPick.ext, owPick.title = "", "", "Default apps"
	owPick.entry = FileEntry{}
	owPick.rows, owPick.more = nil, nil
	owPick.list = owSortedDefaults(config)
	owPick.cursor = clampIndex(owPick.cursor, len(owPick.list))
	owPick.scroll = 0
	owPick.rowHits = owPick.rowHits[:0]
	owPick.openedAt = time.Now()
}

// owPickRows is how many rows the sheet shows.
func owPickRows() int {
	if owPick.mode == owModeList {
		return len(owPick.list)
	}
	return len(owPick.rows)
}

func owPickMove(delta int) {
	n := owPickRows()
	if n == 0 || delta == 0 {
		return
	}
	next := clampIndex(owPick.cursor+delta, n)
	if next == owPick.cursor {
		return
	}
	owPick.cursor = next
	PlayNavSound()
}

// owPickCurrent is the highlighted handler (open and choose modes).
func owPickCurrent() (owHandler, bool) {
	if owPick.mode == owModeList || owPick.cursor < 0 || owPick.cursor >= len(owPick.rows) {
		return owHandler{}, false
	}
	return owPick.rows[owPick.cursor], true
}

// owExpandMore replaces the "Another app…" row with the apps it stands for.
func owExpandMore() {
	for i, h := range owPick.rows {
		if h.Kind != owKindMore {
			continue
		}
		rows := make([]owHandler, 0, len(owPick.rows)-1+len(owPick.more))
		rows = append(rows, owPick.rows[:i]...)
		rows = append(rows, owPick.more...)
		rows = append(rows, owPick.rows[i+1:]...)
		owPick.rows, owPick.more = rows, nil
		owPick.cursor = i
		// The old rows' rectangles must not answer a click before the next
		// frame draws the new ones.
		owPick.rowHits = owPick.rowHits[:0]
		owPick.openedAt = time.Now()
		PlayActivateSound()
		return
	}
}

// owSetDefault saves h as the type's default. JukaHub's own choice is saved
// as no default at all, unless something else would take the type without
// one (owNoDefaultGoesElsewhere: an app or KOReader): then the viewer is
// saved by name, so A really opens it.
func owSetDefault(config *Config, h owHandler) bool {
	if owPick.ext == "" {
		showToast("This file has no extension to remember an app for", ToastInfo())
		return false
	}
	if h.ID == owPick.choice && !owNoDefaultGoesElsewhere(config, owPick.ext) {
		owDefaultsSet(config, owPick.ext, "")
		showToast(owPick.ext+" files open in JukaHub's "+h.Name+" again", ToastInfo())
		return true
	}
	owDefaultsSet(config, owPick.ext, h.ID)
	delete(owMissingWarned, owPick.ext)
	name := h.Name
	switch h.Kind {
	case owKindSystem:
		name = "Windows' app"
	case owKindBuiltin:
		name = "JukaHub's " + h.Name
	}
	showToast(owPick.ext+" files open in "+name+" · "+hintSentence("%s → Open with… to change", "Y", "Actions"), ToastSuccess())
	return true
}

// owResetToast is what a reset says: the type opens in JukaHub's viewer
// again, or, where something else takes it without a default, "as before"
// and where. choice is JukaHub's viewer for it ("" when the list does not
// know it: no file).
func owResetToast(config *Config, ext, choice string) string {
	if name, ok := owAsBefore(config, ext); ok {
		return ext + " files open as before, in " + name
	}
	if choice == "" {
		return ext + " files open in JukaHub again"
	}
	return ext + " files open in JukaHub's " + owBuiltinNames[choice] + " again"
}

// owPickActivate is A (always = false) or X (always = true) on a row.
func owPickActivate(config *Config, always bool) {
	if !owPick.open {
		return
	}
	if owPick.mode == owModeList {
		if owPick.cursor < 0 || owPick.cursor >= len(owPick.list) {
			return
		}
		ext := owPick.list[owPick.cursor]
		if always {
			// X resets the type: no default.
			owDefaultsSet(config, ext, "")
			PlayActivateSound()
			showToast(owResetToast(config, ext, ""), ToastInfo())
			owOpenDefaultsList(config)
			if len(owPick.list) == 0 {
				owPickClose()
			}
			return
		}
		owOpenChooser(config, ext)
		return
	}
	h, ok := owPickCurrent()
	if !ok {
		return
	}
	switch h.Kind {
	case owKindMore:
		owExpandMore()
		return
	case owKindGet:
		// The App Center is another screen: the text page would go with
		// the scene change (switchScene → fileEditorForget).
		if owEditorRefuses(h) {
			return
		}
		owLeaveEditor(h)
		owPickClose()
		PlayActivateSound()
		acOpenCenter(config, h.App.AppID)
		return
	}
	if owPick.mode == owModeChoose {
		if owSetDefault(config, h) {
			PlayActivateSound()
			owOpenDefaultsList(config)
		}
		return
	}
	if owEditorRefuses(h) {
		return
	}
	// A script from Downloads runs on a second press (chatConfirmRun): the
	// sheet stays up for it, and owRun does not ask a third time.
	confirmed := false
	if h.ID == owScript {
		if !chatConfirmRun(owPick.path) {
			return
		}
		confirmed = true
	}
	if always {
		if filesROMSystem(owPick.path) != "" {
			// A game keeps the library's emulator (owOpenWithDefault); this
			// press still opens it once.
			showToast("Games always start in the library's emulator · Game options → Emulator changes it", ToastInfo())
		} else if !owSetDefault(config, h) {
			return
		}
	}
	path, entry := owPick.path, owPick.entry
	owLeaveEditor(h)
	owPickClose()
	PlayActivateSound()
	owRun(config, h, path, entry, confirmed)
}

// owEditorRefuses keeps the text page when the row would take it away with
// unsaved changes: one of JukaHub's viewers takes the screen, and the App
// Center is another scene. An app or Windows opens over it and leaves it be.
func owEditorRefuses(h owHandler) bool {
	if !owFromEditor(owPick.ctx) || (h.Kind != owKindBuiltin && h.Kind != owKindGet) ||
		!fileEditor.open || !fileEditorDirty() {
		return false
	}
	PlayErrorSound()
	showToast("Save or discard the changes first, then open it elsewhere", ToastWarn())
	return true
}

// owLeaveEditor closes the text page (nothing unsaved: owEditorRefuses went
// first) before one of JukaHub's viewers or the App Center takes over.
func owLeaveEditor(h owHandler) {
	if owFromEditor(owPick.ctx) && (h.Kind == owKindBuiltin || h.Kind == owKindGet) && fileEditor.open {
		fileEditorForget()
	}
}

// owFromEditor reports a sheet opened from the text page, as the text
// editor ("editor") or the hex editor ("hex").
func owFromEditor(ctx string) bool {
	return ctx == "editor" || ctx == "hex"
}

// owPickReset is Y in open mode: the type loses its default and the sheet
// stays up, its rows reordered.
func owPickReset(config *Config) {
	if !owPick.open || owPick.mode != owModeOpen || owPick.ext == "" {
		return
	}
	if owDefaultFor(config, owPick.ext) == "" {
		where := "JukaHub's " + owBuiltinNames[owPick.choice]
		if name, ok := owAsBefore(config, owPick.ext); ok {
			where = name
		}
		showToast(owPick.ext+" files have no default · they open in "+where, ToastInfo())
		return
	}
	owDefaultsSet(config, owPick.ext, "")
	PlayActivateSound()
	showToast(owResetToast(config, owPick.ext, owPick.choice), ToastInfo())
	owPick.saved = ""
	owPick.rows, owPick.more = owBuildRows(owPick.path, owPick.binary, owPick.ctx, "")
	owPickNoteAsIs(config)
	owPick.cursor, owPick.scroll = 0, 0
	owPick.rowHits = owPick.rowHits[:0]
	owPick.openedAt = time.Now()
}

// --- Input (called from emupicker.go's handlers) ----------------------------

func owPickKey(e *sdl.KeyboardEvent) bool {
	if e == nil || !owPick.open {
		return false
	}
	// A held key repeats only for walking the rows: a repeat must never be a
	// second A or X.
	if e.Type != sdl.KEYDOWN || (e.Repeat != 0 && e.Keysym.Sym != sdl.K_UP && e.Keysym.Sym != sdl.K_DOWN) {
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		owPickMove(-1)
	case sdl.K_DOWN:
		owPickMove(1)
	case sdl.K_PAGEUP:
		owPickMove(-max(1, owPick.visible))
	case sdl.K_PAGEDOWN:
		owPickMove(max(1, owPick.visible))
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE, sdl.K_a:
		owPickActivate(appConfig, false)
	case sdl.K_x:
		owPickActivate(appConfig, true)
	case sdl.K_y:
		owPickReset(appConfig)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		owPickCancel()
	}
	return true
}

func owPickController(e *sdl.ControllerButtonEvent) bool {
	if e == nil || !owPick.open {
		return false
	}
	if e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		owPickMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		owPickMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		owPickMove(-max(1, owPick.visible))
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		owPickMove(max(1, owPick.visible))
	case sdl.CONTROLLER_BUTTON_A:
		owPickActivate(appConfig, false)
	case sdl.CONTROLLER_BUTTON_X:
		owPickActivate(appConfig, true)
	case sdl.CONTROLLER_BUTTON_Y:
		owPickReset(appConfig)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		owPickCancel()
	}
	return true
}

// owPickMouse: a row runs (A), a click off the card cancels.
func owPickMouse(mx, my int32) bool {
	if !owPick.open {
		return false
	}
	owPick.eatUp = true
	if time.Since(owPick.openedAt) < owPickClickGuard {
		// The click that opened the sheet, or the second half of a
		// double-click on the row before: not a choice in the new rows.
		return true
	}
	for _, hit := range owPick.rowHits {
		if pointInRect(mx, my, hit.rect) {
			owPick.cursor = hit.index
			owPickActivate(appConfig, false)
			return true
		}
	}
	if owPick.panel.W > 0 && pointInRect(mx, my, owPick.panel) {
		return true
	}
	owPickCancel()
	return true
}

// owPickEatRelease reports (once) whether a left-button release belongs to
// the sheet.
func owPickEatRelease() bool {
	eat := owPick.open || owPick.eatUp
	owPick.eatUp = false
	return eat
}

// --- Drawing ----------------------------------------------------------------

// owPickHeader is the card's title and subtitle.
func owPickHeader(config *Config) (string, string) {
	switch owPick.mode {
	case owModeList:
		n := len(owPick.list)
		if n == 0 {
			return "Default apps", "None yet · on a file, Open with… then Always use"
		}
		return "Default apps", pluralizeCount(n, "file type opens", "file types open") + " in a chosen app"
	case owModeChoose:
		return "Open " + owPick.title + " with", "Choose the default"
	}
	sub := "JukaHub's choice: " + owBuiltinNames[owPick.choice]
	switch {
	case owPick.saved != "":
		name, _ := owHandlerName(owPick.saved)
		sub = "Default: " + name
	case owPick.asIsName != "":
		sub = "Opens in " + owPick.asIsName + " · no default"
	}
	return "Open with", sub
}

// owListRowText is a Default apps row: the type, and what opens it.
func owListRowText(config *Config, ext string) (string, string) {
	id := owDefaultFor(config, ext)
	name, installed := owHandlerName(id)
	if !installed {
		gone := " · not on the card · "
		if !owIsAppID(id) {
			gone = " · not on this device · "
		}
		fallback := "JukaHub opens them"
		if as, ok := owAsBefore(config, ext); ok {
			fallback = as + " opens them"
		}
		return ext + " files", name + gone + fallback
	}
	if id == owSystem {
		return ext + " files", "Windows' app"
	}
	kind := "JukaHub"
	if owIsAppID(id) {
		kind = "App"
	}
	return ext + " files", name + " · " + kind
}

// renderOwPick draws the sheet above whatever screen opened it.
func renderOwPick(renderer *sdl.Renderer, config *Config) {
	if !owPick.open || renderer == nil || config == nil {
		return
	}
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const pad = OverlayPadX
	rowH := max32(58, medH+smallH+24)
	title, subtitle := owPickHeader(config)
	header := UIPanelHeader(title, subtitle, "files")
	rows := owPickRows()
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := int32(14) + smallH
	// The open sheet names the file on its own line: it is the user's text,
	// drawn plain (never a $variable).
	nameH := int32(0)
	if owPick.mode == owModeOpen {
		nameH = smallH + 10
	}
	panelW := int32(640)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	shown := rows
	if shown < 1 {
		shown = 1
	}
	panelH := headerH + nameH + rowH*int32(shown) + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	owPick.panel = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	innerW := panelW - 2*pad
	bodyY := panelY + headerH
	if nameH > 0 {
		filesDrawPlainText(renderer, config, font, ellipsize(font, owPick.title, innerW), ColorTextSecondary(), panelX+pad, bodyY)
		bodyY += nameH
	}
	visible := int((panelH - headerH - nameH - footerH) / rowH)
	if visible < 1 {
		visible = 1
	}
	if visible > rows {
		visible = rows
	}
	if owPick.cursor < owPick.scroll {
		owPick.scroll = owPick.cursor
	}
	if owPick.cursor >= owPick.scroll+visible {
		owPick.scroll = owPick.cursor - visible + 1
	}
	if owPick.scroll > rows-visible {
		owPick.scroll = rows - visible
	}
	if owPick.scroll < 0 {
		owPick.scroll = 0
	}
	owPick.visible = visible
	owPick.rowHits = owPick.rowHits[:0]
	focusColor := installHighlight()
	if rows == 0 {
		renderText(renderer, config, font, ellipsize(font, hintSentence("On a file: %s → Open with…, then Always use", "Y", "Actions"), innerW),
			ColorTextTertiary(), panelX+pad, bodyY+12)
	}
	for i := 0; i < visible; i++ {
		idx := owPick.scroll + i
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i)*rowH, W: innerW, H: rowH - 8}
		owPick.rowHits = append(owPick.rowHits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == owPick.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}
		name, detail, badge := "", "", ""
		if owPick.mode == owModeList {
			name, detail = owListRowText(config, owPick.list[idx])
		} else {
			h := owPick.rows[idx]
			name, detail = h.Name, h.Detail
			switch {
			case owPick.saved != "":
				if h.ID == owPick.saved {
					badge = "Default"
				}
			case owPick.asIsName != "":
				// Something other than JukaHub's viewer takes the type.
				if owPick.asIsID != "" && h.ID == owPick.asIsID {
					badge = "Opens now"
				}
			case h.ID == owPick.choice:
				badge = "JukaHub's choice"
			}
		}
		nameRoom := rect.W - 28
		if badge != "" {
			tw, _ := measureText(font, badge)
			bw, bh := tw+20, smallH+6
			bx := rect.X + rect.W - 14 - bw
			ink := glassAccent()
			fillRoundedRect(renderer, bx, rect.Y+8, bw, bh, bh/2, WithAlpha(ink, 50))
			renderText(renderer, config, font, badge, ink, bx+10, rect.Y+8+3)
			nameRoom -= bw + 10
		}
		// App names and file types come from the card: plain text.
		filesDrawPlainText(renderer, config, medium, ellipsize(medium, name, nameRoom), ColorTextPrimary(), rect.X+14, rect.Y+8)
		filesDrawPlainText(renderer, config, font, ellipsize(font, detail, rect.W-28), ColorTextTertiary(), rect.X+14, rect.Y+8+medH+2)
	}

	var hint string
	switch owPick.mode {
	case owModeList:
		hint = hintLine(FooterHint{"D-Pad", "Type"}, FooterHint{"A", "Change"}, FooterHint{"X", "Reset"}, FooterHint{"B", "Close"})
	case owModeChoose:
		hint = hintLine(FooterHint{"D-Pad", "App"}, FooterHint{"A", "Make default"}, FooterHint{"B", "Back"})
	default:
		open, always := FooterHint{"A", "Open"}, FooterHint{"X", "Always use"}
		if h, ok := owPickCurrent(); ok {
			switch h.Kind {
			case owKindMore:
				open, always = FooterHint{"A", "Show"}, FooterHint{}
			case owKindGet:
				open, always = FooterHint{"A", "App Center"}, FooterHint{}
			}
		}
		if owPick.ext == "" || filesROMSystem(owPick.path) != "" {
			always = FooterHint{}
		}
		reset := FooterHint{}
		if owPick.saved != "" {
			reset = FooterHint{"Y", "JukaHub's choice"}
			if owNoDefaultGoesElsewhere(config, owPick.ext) {
				// Without a default an app or KOReader may take it.
				reset = FooterHint{"Y", "No default"}
			}
		}
		hint = hintLine(FooterHint{"D-Pad", "App"}, open, always, reset, FooterHint{"B", "Cancel"})
		if tw, _ := measureText(font, hint); tw > innerW {
			hint = hintLine(open, always, reset, FooterHint{"B", "Cancel"})
		}
	}
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextSecondary(),
		panelX+pad, panelY+panelH-footerH+2)
}
