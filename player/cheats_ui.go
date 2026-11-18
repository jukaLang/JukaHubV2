package main

// cheats_ui.go — the Cheats sheet, opened from the game options sheet's
// Cheats row (game_options_ui.go) and drawn over it.
//
// Every cheat of the game's database file is a row: its description, its
// code under it, and On/Off on the right.
//
//	A      switch the highlighted cheat on or off (or run an action row)
//	L1/R1  a page up or down (Left/Right too, PageUp/PageDown on a keyboard)
//	Y      choose another cheat file, when several are close to the game
//	B      back to the game options
//
// Below the cheats are the action rows: "Choose another cheat file", "Turn
// every cheat off" and "Download cheat database" (or "Update", once JukaHub's
// own copy is there). With no database on the card the download is the only
// row, under "No cheats found for this game". The list is found on a goroutine
// (cheatsLoadFor reads retroarch.cfg, folders and maybe a 37 MB zip); the
// sheet says "Looking for cheats…" meanwhile and takes the answer in
// cheatsPoll. Choices go to the in-memory record at once and to the card when
// the sheet closes (cheats.go).

import (
	"strconv"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// The action rows after the cheats, in this order when shown.
const (
	cheatsActNone = iota
	cheatsActChoose
	cheatsActAllOff
	cheatsActDownload
)

var cheatsUI struct {
	open    bool
	game    libraryGame
	loading bool
	loadGen uint64
	// dbGen is cheatsDBGen when the list was asked for: a download finishing
	// while the sheet is up reloads it.
	dbGen uint64
	res   cheatsLoadResult
	// on is the set of switched-on cheats by key (chtCheat.key).
	on map[string]bool
	// picking shows the close files instead of the cheats; listCursor and
	// listScroll put the highlight back when it closes.
	picking    bool
	listCursor int
	listScroll int
	cursor     int
	scroll     int
	visible    int
	panel      sdl.Rect
	rowHits    []gameLibRowHit
}

// cheatsLoadBox hands a finished lookup from its goroutine to the render
// thread; gen drops the answer of a lookup the sheet no longer waits for.
var cheatsLoadBox struct {
	mu    sync.Mutex
	gen   uint64
	ready bool
	res   cheatsLoadResult
}

// cheatsSheetOpen reports whether the sheet is up (the options sheet routes
// input to it).
func cheatsSheetOpen() bool { return cheatsUI.open }

// cheatsOptionsValue is the Cheats row's value in the game options sheet.
func cheatsOptionsValue(romPath string) string {
	if n := cheatsEnabledCount(romPath); n > 0 {
		return strconv.Itoa(n) + " on"
	}
	return "Off"
}

// openCheatsSheet opens the sheet for one game. Render thread.
func openCheatsSheet(game libraryGame) {
	switch game.System {
	case androidLibrarySystem, html5LibrarySystem, portsLibrarySystem:
		ShowToast("Cheats are for games that start in RetroArch", ToastKindInfo)
		return
	}
	cheatsUI.open = true
	cheatsUI.game = game
	cheatsUI.res = cheatsLoadResult{}
	cheatsUI.on = map[string]bool{}
	if rec, ok := cheatsRecordFor(game.Path); ok {
		for _, k := range rec.On {
			cheatsUI.on[k] = true
		}
	}
	cheatsUI.picking = false
	cheatsUI.cursor = 0
	cheatsUI.scroll = 0
	cheatsUI.visible = 0
	cheatsUI.rowHits = cheatsUI.rowHits[:0]
	cheatsStartLoad(cheatRef{})
	PlayActivateSound()
}

// cheatsClose closes the sheet and writes the choices to the card.
func cheatsClose(sound bool) {
	if !cheatsUI.open {
		return
	}
	cheatsUI.open = false
	cheatsUI.loading = false
	cheatsUI.res = cheatsLoadResult{}
	cheatsUI.on = nil
	cheatsUI.rowHits = cheatsUI.rowHits[:0]
	cheatsLoadBox.mu.Lock()
	cheatsLoadBox.gen++
	cheatsLoadBox.ready = false
	cheatsLoadBox.res = cheatsLoadResult{}
	cheatsLoadBox.mu.Unlock()
	cheatsFlush()
	if sound {
		PlayBackSound()
	}
}

// cheatsStartLoad looks the game's cheats up on a goroutine; forced is a file
// picked in the sheet.
func cheatsStartLoad(forced cheatRef) {
	cheatsUI.loading = true
	cheatsUI.dbGen = cheatsDBGen.Load()
	cheatsLoadBox.mu.Lock()
	cheatsLoadBox.gen++
	gen := cheatsLoadBox.gen
	cheatsLoadBox.ready = false
	cheatsLoadBox.res = cheatsLoadResult{}
	cheatsLoadBox.mu.Unlock()
	cheatsUI.loadGen = gen
	game := cheatsUI.game
	go func() {
		res := cheatsLoadFor(game, forced)
		cheatsLoadBox.mu.Lock()
		if cheatsLoadBox.gen == gen {
			cheatsLoadBox.res = res
			cheatsLoadBox.ready = true
		}
		cheatsLoadBox.mu.Unlock()
	}()
}

// cheatsPoll takes a finished lookup, and reloads after a database download.
// Render thread, once a frame while the sheet is up.
func cheatsPoll() {
	if !cheatsUI.loading {
		if cheatsDBGen.Load() != cheatsUI.dbGen {
			cheatsStartLoad(cheatRef{})
		}
		return
	}
	cheatsLoadBox.mu.Lock()
	if !cheatsLoadBox.ready || cheatsLoadBox.gen != cheatsUI.loadGen {
		cheatsLoadBox.mu.Unlock()
		return
	}
	cheatsUI.res = cheatsLoadBox.res
	cheatsLoadBox.ready = false
	cheatsLoadBox.res = cheatsLoadResult{}
	cheatsLoadBox.mu.Unlock()
	cheatsUI.loading = false
	cheatsUI.picking = false
	// A file picked in the list keeps only the switched-on cheats it also
	// has: the record, and the options row's count, drop the rest.
	if res := cheatsUI.res; res.Chosen.valid() && len(res.Cheats) > 0 {
		if rec, ok := cheatsRecordFor(cheatsUI.game.Path); ok && rec.Source == res.Chosen.Source &&
			rec.Entry == res.Chosen.Entry && len(rec.On) != cheatsOnCount() {
			cheatsSaveChoice()
		}
	}
	if n := cheatsRowCount(); cheatsUI.cursor >= n {
		cheatsUI.cursor = n - 1
	}
	if cheatsUI.cursor < 0 {
		cheatsUI.cursor = 0
	}
}

// ── Rows ─────────────────────────────────────────────────────────────────

func cheatsActionShown(act int) bool {
	switch act {
	case cheatsActChoose:
		return len(cheatsUI.res.Cands) > 1
	case cheatsActAllOff:
		return len(cheatsUI.on) > 0
	case cheatsActDownload:
		return true
	}
	return false
}

func cheatsActionCount() int {
	n := 0
	for _, act := range [...]int{cheatsActChoose, cheatsActAllOff, cheatsActDownload} {
		if cheatsActionShown(act) {
			n++
		}
	}
	return n
}

// cheatsActionAt is the action of row i, cheatsActNone for a cheat row.
func cheatsActionAt(i int) int {
	i -= len(cheatsUI.res.Cheats)
	if i < 0 {
		return cheatsActNone
	}
	for _, act := range [...]int{cheatsActChoose, cheatsActAllOff, cheatsActDownload} {
		if !cheatsActionShown(act) {
			continue
		}
		if i == 0 {
			return act
		}
		i--
	}
	return cheatsActNone
}

// cheatsRowCount is how many rows the sheet shows now.
func cheatsRowCount() int {
	switch {
	case cheatsUI.loading:
		return 0
	case cheatsUI.picking:
		return len(cheatsUI.res.Cands)
	}
	return len(cheatsUI.res.Cheats) + cheatsActionCount()
}

// cheatsOnCount is how many cheats of the list are on.
func cheatsOnCount() int {
	n := 0
	for _, c := range cheatsUI.res.Cheats {
		if cheatsUI.on[c.key()] {
			n++
		}
	}
	return n
}

func cheatsActionText(act int) (string, string) {
	switch act {
	case cheatsActChoose:
		return "Choose another cheat file", strconv.Itoa(len(cheatsUI.res.Cands)) + " files in the database are close to this game"
	case cheatsActAllOff:
		return "Turn every cheat off", "The game starts without cheats"
	case cheatsActDownload:
		if cheatsDownloading.Load() {
			return "Downloading the cheat database…", "libretro's cheats.zip; the bar shows how far"
		}
		if cheatsUI.res.OwnZip {
			return "Update cheat database", "Download libretro's cheats.zip again (about 37 MB)"
		}
		return "Download cheat database", "libretro's cheats.zip, about 37 MB, into JukaHub's data folder"
	}
	return "", ""
}

// cheatsNote is the line under the rows.
func cheatsNote() string {
	res := cheatsUI.res
	switch {
	case cheatsUI.loading:
		return "Looking for cheats…"
	case cheatsUI.picking:
		return "Files in the cheat database whose name is close to this game's"
	case res.NotRA != "":
		return "This game starts in " + res.NotRA + "; cheats apply only to RetroArch games"
	case res.UserFile != "":
		return "RetroArch has its own cheat file for this game; it is used instead"
	case !res.DB:
		return "No cheats found for this game · no cheat database on the card yet"
	case len(res.Cheats) == 0 && res.Problem != "":
		return "No cheats found for this game · " + res.Problem
	case len(res.Cheats) == 0:
		return "No cheats found for this game"
	}
	return strconv.Itoa(cheatsOnCount()) + " of " + strconv.Itoa(len(res.Cheats)) +
		" on · applied when the game starts in RetroArch"
}

// ── Actions ──────────────────────────────────────────────────────────────

// cheatsSaveChoice stores the switched-on cheats of the shown file.
func cheatsSaveChoice() {
	rec, _ := cheatsRecordFor(cheatsUI.game.Path)
	var on []string
	seen := map[string]bool{}
	for _, c := range cheatsUI.res.Cheats {
		k := c.key()
		if cheatsUI.on[k] && !seen[k] {
			seen[k] = true
			on = append(on, k)
		}
	}
	rec.On = on
	if cheatsUI.res.Chosen.valid() {
		rec.Source = cheatsUI.res.Chosen.Source
		rec.Entry = cheatsUI.res.Chosen.Entry
	}
	cheatsSetRecord(cheatsUI.game.Path, rec)
}

func cheatsToggle(i int) {
	if i < 0 || i >= len(cheatsUI.res.Cheats) {
		return
	}
	k := cheatsUI.res.Cheats[i].key()
	if cheatsUI.on[k] {
		delete(cheatsUI.on, k)
	} else {
		cheatsUI.on[k] = true
	}
	cheatsSaveChoice()
	PlayNavSound()
}

func cheatsOpenPicker() {
	if cheatsUI.loading || cheatsUI.picking || len(cheatsUI.res.Cands) < 2 {
		return
	}
	cheatsUI.listCursor = cheatsUI.cursor
	cheatsUI.listScroll = cheatsUI.scroll
	cheatsUI.picking = true
	cheatsUI.cursor = 0
	for i, c := range cheatsUI.res.Cands {
		if c == cheatsUI.res.Chosen {
			cheatsUI.cursor = i
		}
	}
	cheatsUI.scroll = 0
	PlayActivateSound()
}

// cheatsPick makes candidate i the game's cheat file. Cheats with the same
// description and code stay on; the list reloads from the new file.
func cheatsPick(i int) {
	if i < 0 || i >= len(cheatsUI.res.Cands) {
		return
	}
	ref := cheatsUI.res.Cands[i]
	rec, _ := cheatsRecordFor(cheatsUI.game.Path)
	rec.Source = ref.Source
	rec.Entry = ref.Entry
	rec.Picked = true
	cheatsSetRecord(cheatsUI.game.Path, rec)
	cheatsUI.picking = false
	cheatsUI.cursor = 0
	cheatsUI.scroll = 0
	cheatsStartLoad(ref)
	PlayActivateSound()
}

// cheatsActivate is A on the highlighted row.
func cheatsActivate() {
	if !cheatsUI.open || cheatsUI.loading {
		return
	}
	i := cheatsUI.cursor
	if cheatsUI.picking {
		cheatsPick(i)
		return
	}
	switch cheatsActionAt(i) {
	case cheatsActNone:
		cheatsToggle(i)
	case cheatsActChoose:
		cheatsOpenPicker()
	case cheatsActAllOff:
		cheatsUI.on = map[string]bool{}
		cheatsSaveChoice()
		ShowToast("Every cheat is off for "+cheatsUI.game.Label(), ToastKindInfo)
		PlayNavSound()
		if n := cheatsRowCount(); cheatsUI.cursor >= n {
			cheatsUI.cursor = n - 1
		}
	case cheatsActDownload:
		if cheatsStartDownload() {
			PlayActivateSound()
		}
	}
}

// cheatsBack is B: out of the file list to the cheats, else the sheet closes.
func cheatsBack() {
	if cheatsUI.picking {
		cheatsUI.picking = false
		cheatsUI.cursor = cheatsUI.listCursor
		cheatsUI.scroll = cheatsUI.listScroll
		PlayBackSound()
		return
	}
	cheatsClose(true)
}

func cheatsMove(delta int) {
	n := cheatsRowCount()
	if n == 0 || delta == 0 {
		return
	}
	next := cheatsUI.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= n {
		next = n - 1
	}
	if next == cheatsUI.cursor {
		return
	}
	cheatsUI.cursor = next
	PlayNavSound()
}

// cheatsPage moves a screenful: long PlayStation lists run to hundreds.
func cheatsPage(dir int) {
	step := cheatsUI.visible
	if step < 1 {
		step = 1
	}
	cheatsMove(dir * step)
}

// ── Input ────────────────────────────────────────────────────────────────

// handleCheatsKey owns every key while the sheet is up.
func handleCheatsKey(e *sdl.KeyboardEvent) bool {
	if e == nil || !cheatsUI.open {
		return false
	}
	if e.Type != sdl.KEYDOWN {
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		cheatsMove(-1)
	case sdl.K_DOWN:
		cheatsMove(1)
	case sdl.K_PAGEUP, sdl.K_LEFT:
		cheatsPage(-1)
	case sdl.K_PAGEDOWN, sdl.K_RIGHT:
		cheatsPage(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		cheatsActivate()
	case sdl.K_y:
		cheatsOpenPicker()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		cheatsBack()
	}
	return true
}

// handleCheatsController owns the pad while the sheet is up.
func handleCheatsController(e *sdl.ControllerButtonEvent) bool {
	if e == nil || !cheatsUI.open {
		return false
	}
	if e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		cheatsMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		cheatsMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		cheatsPage(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		cheatsPage(1)
	case sdl.CONTROLLER_BUTTON_A:
		cheatsActivate()
	case sdl.CONTROLLER_BUTTON_Y:
		cheatsOpenPicker()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		cheatsBack()
	}
	return true
}

// handleCheatsMouse: a row is A on it, a click off the card is B.
func handleCheatsMouse(mx, my int32) bool {
	if !cheatsUI.open {
		return false
	}
	for _, hit := range cheatsUI.rowHits {
		if pointInRect(mx, my, hit.rect) {
			cheatsUI.cursor = hit.index
			cheatsActivate()
			return true
		}
	}
	if cheatsUI.panel.W > 0 && pointInRect(mx, my, cheatsUI.panel) {
		return true
	}
	cheatsBack()
	return true
}

// ── Drawing ──────────────────────────────────────────────────────────────

// renderCheatsSheet draws the sheet over the game options.
func renderCheatsSheet(renderer *sdl.Renderer, config *Config) {
	if !cheatsUI.open || renderer == nil || config == nil {
		return
	}
	cheatsPoll()
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
	subtitle := cheatsUI.game.Label()
	if cheatsUI.res.Chosen.valid() && !cheatsUI.loading {
		subtitle += " · " + cheatsUI.res.Chosen.name()
	}
	title := "Cheats"
	if cheatsUI.picking {
		title = "Choose a cheat file"
	}
	header := UIPanelHeader(title, subtitle, "gamepad")
	rows := cheatsRowCount()
	headerH := UIPanelHeaderBand(config, header, 14)
	noteH := int32(12) + smallH
	footerH := int32(14) + smallH
	bodyRows := rows
	if bodyRows < 1 {
		bodyRows = 1
	}
	panelW := int32(620)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := headerH + rowH*int32(bodyRows) + noteH + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	cheatsUI.panel = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + headerH
	innerW := panelW - 2*pad
	visible := int((panelH - headerH - noteH - footerH) / rowH)
	if visible < 1 {
		visible = 1
	}
	if visible > rows {
		visible = rows
	}
	if cheatsUI.cursor >= rows {
		cheatsUI.cursor = rows - 1
	}
	if cheatsUI.cursor < 0 {
		cheatsUI.cursor = 0
	}
	if cheatsUI.cursor < cheatsUI.scroll {
		cheatsUI.scroll = cheatsUI.cursor
	}
	if cheatsUI.cursor >= cheatsUI.scroll+visible {
		cheatsUI.scroll = cheatsUI.cursor - visible + 1
	}
	if cheatsUI.scroll > rows-visible {
		cheatsUI.scroll = rows - visible
	}
	if cheatsUI.scroll < 0 {
		cheatsUI.scroll = 0
	}
	cheatsUI.visible = visible
	cheatsUI.rowHits = cheatsUI.rowHits[:0]
	focusColor := installHighlight()
	for i := 0; i < visible; i++ {
		idx := cheatsUI.scroll + i
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i)*rowH, W: innerW, H: rowH - 8}
		cheatsUI.rowHits = append(cheatsUI.rowHits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == cheatsUI.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}
		name, detail, value := cheatsRowText(idx)
		nameRoom := rect.W - 28
		if value != "" {
			on := value == "On" || value == "In use"
			tw, _ := measureText(font, value)
			bw, bh := tw+20, smallH+6
			bx := rect.X + rect.W - 14 - bw
			by := rect.Y + (rect.H-bh)/2
			ink := ColorTextSecondary()
			if on {
				ink = focusColor
			}
			fillRoundedRect(renderer, bx, by, bw, bh, bh/2, WithAlpha(ink, 50))
			renderText(renderer, config, font, value, ink, bx+10, by+3)
			nameRoom -= bw + 10
		}
		renderText(renderer, config, medium, ellipsize(medium, name, nameRoom), ColorTextPrimary(),
			rect.X+14, rect.Y+8)
		renderText(renderer, config, font, ellipsize(font, detail, nameRoom), ColorTextTertiary(),
			rect.X+14, rect.Y+8+medH+2)
	}

	noteY := bodyY + rowH*int32(visible) + 4
	if rows == 0 {
		noteY = bodyY + 4
	}
	renderText(renderer, config, font, ellipsize(font, cheatsNote(), innerW),
		ColorTextTertiary(), panelX+pad, noteY)

	hint := hintLine(FooterHint{"D-Pad", "Cheat"}, FooterHint{"A", "On/Off"},
		FooterHint{"L1/R1", "Page"}, FooterHint{"B", "Back"})
	switch {
	case cheatsUI.picking:
		hint = hintLine(FooterHint{"D-Pad", "File"}, FooterHint{"A", "Use"}, FooterHint{"B", "Back"})
	case len(cheatsUI.res.Cands) > 1 && !cheatsUI.loading:
		hint = hintLine(FooterHint{"D-Pad", "Cheat"}, FooterHint{"A", "On/Off"},
			FooterHint{"L1/R1", "Page"}, FooterHint{"Y", "Other file"}, FooterHint{"B", "Back"})
	}
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextSecondary(),
		panelX+pad, panelY+panelH-footerH+2)
}

// cheatsRowText is a row's name, the line under it and its badge.
func cheatsRowText(idx int) (string, string, string) {
	res := cheatsUI.res
	if cheatsUI.picking {
		if idx < 0 || idx >= len(res.Cands) {
			return "", "", ""
		}
		c := res.Cands[idx]
		badge := ""
		if c == res.Chosen {
			badge = "In use"
		}
		return c.name(), c.folder(), badge
	}
	if idx >= 0 && idx < len(res.Cheats) {
		c := res.Cheats[idx]
		detail := strings.ReplaceAll(c.Code, "+", " + ")
		if strings.TrimSpace(detail) == "" {
			detail = "RetroArch memory cheat"
		}
		value := "Off"
		if cheatsUI.on[c.key()] {
			value = "On"
		}
		return c.label(idx), detail, value
	}
	name, detail := cheatsActionText(cheatsActionAt(idx))
	return name, detail, ""
}
