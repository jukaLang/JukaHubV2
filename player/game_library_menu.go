package main

// game_library_menu.go — the Games Library's menu: Select on the pad, M on a
// keyboard.
//
// The tag filter and the random pick used to sit on the stick clicks, L3 and
// R3. The original Smart Pro's sticks do not press in and a Brick's F1 / F2
// keys do their own job (stickclicks.go), so on most handhelds neither was
// within reach, while the footer kept naming them. The menu gathers them with
// the library's other actions, each row naming the button that also does it,
// the way the Browser's Select menu does (brw_menu.go): a random game from
// the console on screen or from the whole library (rolled at once, no second
// sheet), the tag filter, search, the box-art fetch, a rescan and, on the
// consoles grid, the next console look. It opens again on the row used last,
// so Select then A rolls another random game.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// gameLibMenuID is one of the menu's actions.
type gameLibMenuID int

const (
	glmRandomHere gameLibMenuID = iota
	glmRandomAll
	glmTags
	glmSearch
	glmArt
	glmRescan
	glmAudit
	glmNextUp
	glmJournal
	glmLook
)

// gameLibMenuRow is a row as drawn: its action, its title, a line about it,
// and the button that does the same from the library ("" when none does).
type gameLibMenuRow struct {
	id     gameLibMenuID
	title  string
	detail string
	button string
}

// gameLibMenu is the open menu. last survives closing: the row the menu
// opens on next time.
var gameLibMenu struct {
	open    bool
	cursor  int
	scroll  int
	last    gameLibMenuID
	rows    []gameLibMenuRow
	panel   sdl.Rect
	rowHits []gameLibRowHit
}

// gameLibMenuRows lists the menu for the level on screen.
func gameLibMenuRows() []gameLibMenuRow {
	var rows []gameLibMenuRow
	for _, scope := range buildGameRandomScopes() {
		row := gameLibMenuRow{id: glmRandomAll, title: "Random game from the whole library", detail: scope.detail, button: "R"}
		if scope.console != "" {
			row.id, row.title = glmRandomHere, "Random game from "+scope.console
		}
		rows = append(rows, row)
	}
	tags := gameLibMenuRow{id: glmTags, title: "Show only a tag…", detail: "Favorites, Backlog or a tag of your own", button: "T"}
	if tag := gmetaFilterTag(); tag != "" {
		tags.detail = "Showing " + tag + " · pick another, or All games"
	}
	search := gameLibMenuRow{id: glmSearch, title: "Search", detail: "A game's title, its console or its file name", button: "S / R1"}
	if gameLib.query != "" {
		search.detail = "Searching \"" + gameLib.query + "\" · type another"
	}
	art := gameLibMenuRow{id: glmArt, title: "Fill missing box art", button: "L1 / F",
		detail: itoa(gameLib.lib.WithArt) + " of " + pluralizeCount(gameLib.lib.Total, "game has", "games have") + " box art"}
	if artworkFetchRunning() {
		art.title, art.detail = "Stop the box-art fetch", "It carries on where it stopped next time"
	}
	nextUp := gameLibMenuRow{id: glmNextUp, title: "Next up from your backlog",
		detail: "No backlog games yet — set a status on a game's page"}
	if game, days, ok := gameNextUpPick(gameLib.lib); ok {
		nextUp.detail = game.Label() + " · " + gameWaitLabel(days)
	}
	rows = append(rows, tags, search, art,
		gameLibMenuRow{id: glmRescan, title: "Rescan the card", detail: "Look for games added or removed since", button: "Y"},
		gameLibMenuRow{id: glmAudit, title: "Audit the library (DAT)", detail: "No-Intro/Redump: missing games, bad dumps, duplicates, renames"},
		gameLibMenuRow{id: glmJournal, title: "Play Journal", detail: "Sessions, statuses, most-played and your year in games"},
		nextUp)
	if gameLib.focus == 0 {
		// X steps the look on the grid only; on a console's page X is LAN Play.
		rows = append(rows, gameLibMenuRow{id: glmLook, title: "Next console look",
			detail: "Now: " + libraryConsoleLookLabel(), button: "X"})
	}
	return rows
}

// openGameLibMenu shows the menu over the library, on the row used last. It
// reports false when a sheet already owns the library.
func openGameLibMenu() bool {
	if !gameLib.open || gameLibMenu.open || gameOptsUI.open || gameRandomUI.open || gmetaSheetOpen() {
		return false
	}
	gameLibMenu.rows = gameLibMenuRows()
	gameLibMenu.cursor, gameLibMenu.scroll = 0, 0
	for i, row := range gameLibMenu.rows {
		if row.id == gameLibMenu.last {
			gameLibMenu.cursor = i
			break
		}
	}
	gameLibMenu.rowHits = gameLibMenu.rowHits[:0]
	gameLibMenu.panel = sdl.Rect{}
	gameLibMenu.open = true
	PlayActivateSound()
	return true
}

// closeGameLibMenu puts the menu away; sound is false when a row's own
// action makes the sound.
func closeGameLibMenu(sound bool) {
	if !gameLibMenu.open {
		return
	}
	gameLibMenu.open = false
	gameLibMenu.rowHits = gameLibMenu.rowHits[:0]
	if sound {
		PlayBackSound()
	}
}

// resetGameLibMenu closes the menu without a sound (the library closing or
// opening). The row used last is kept.
func resetGameLibMenu() {
	gameLibMenu.open = false
	gameLibMenu.rows = nil
	gameLibMenu.rowHits = gameLibMenu.rowHits[:0]
}

func gameLibMenuMove(delta int) {
	n := len(gameLibMenu.rows)
	if n == 0 || delta == 0 {
		return
	}
	gameLibMenu.cursor = ((gameLibMenu.cursor+delta)%n + n) % n
	PlayNavSound()
}

// gameLibMenuChoose runs row i: the menu closes first, so a keyboard or a
// sheet the action opens is the only thing on screen.
func gameLibMenuChoose(i int) {
	if i < 0 || i >= len(gameLibMenu.rows) {
		return
	}
	row := gameLibMenu.rows[i]
	gameLibMenu.last = row.id
	closeGameLibMenu(false)
	switch row.id {
	case glmRandomHere, glmRandomAll:
		// Rolled now from the scopes as they stand (a scan may have landed
		// since the menu opened); chooseGameRandomScope says what it picked.
		scopes := buildGameRandomScopes()
		for j, scope := range scopes {
			if (scope.console != "") == (row.id == glmRandomHere) {
				gameRandomUI = gameRandomPickerState{open: true, scopes: scopes}
				chooseGameRandomScope(j)
				return
			}
		}
		ShowToast("No games to pick from yet — copy ROMs and press "+hintKeyFor("Y", "Rescan")+" to rescan", ToastKindInfo)
	case glmTags:
		gmetaOpenFilterPicker()
	case glmSearch:
		vkOpenGameSearch()
	case glmArt:
		fetchMissingLibraryArtwork()
	case glmRescan:
		refreshGameLibrary()
		ShowToast(gameLibraryStatus(), ToastKindInfo)
	case glmAudit:
		romAuditStart()
		openCommunityPanel(appConfig, communityPageROMAudit)
	case glmNextUp:
		game, days, ok := gameNextUpPick(gameLib.lib)
		if !ok {
			ShowToast("No backlog games yet — open a game's page and set its status to Backlog", ToastKindInfo)
			return
		}
		gmetaOpenDetails(game)
		ShowToast("Next up: "+game.Label()+" · "+gameWaitLabel(days), ToastKindInfo)
	case glmJournal:
		openCommunityPanel(appConfig, communityPageJournal)
	case glmLook:
		gameLibraryLookPress()
	}
}

// gameLibSelectPressed reports whether a BACK press is the Select button
// itself. On a handheld the Menu key arrives as BACK as well — folded into it
// (normalizeHandheldBackButton, padEventRaw keeps the GUIDE) or sent for a
// Menu tap where Menu means Back (menuBackWhich, menushot.go) — and Menu
// keeps going back. A repeat or a control profile's press is the button it
// names (bootlock.go reads them the same way).
func gameLibSelectPressed(e *sdl.ControllerButtonEvent) bool {
	if e.Which == menuBackWhich {
		return false
	}
	if e.Which == padRepeatWhich || e.Which == inmapSynthWhich {
		return true
	}
	return padEventRaw != sdl.CONTROLLER_BUTTON_GUIDE
}

// ctrlGameLibMenu takes every pad press while the menu is up. Select, the
// button that opened it, closes it again.
func ctrlGameLibMenu(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !gameLibMenu.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		gameLibMenuMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		gameLibMenuMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		gameLibMenuChoose(gameLibMenu.cursor)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		closeGameLibMenu(true)
	}
	return true
}

// handleGameLibMenuKey is the keyboard's twin: M, the key that opened it,
// closes it like Esc.
func handleGameLibMenuKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !gameLibMenu.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		gameLibMenuMove(-1)
	case sdl.K_DOWN:
		gameLibMenuMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		gameLibMenuChoose(gameLibMenu.cursor)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE, sdl.K_m:
		closeGameLibMenu(true)
	}
	return true
}

// handleGameLibMenuMouse runs a clicked row; a click outside the card puts
// the menu away, one on the card is taken.
func handleGameLibMenuMouse(mx, my int32) bool {
	if !gameLibMenu.open {
		return false
	}
	for _, hit := range gameLibMenu.rowHits {
		if pointInRect(mx, my, hit.rect) {
			gameLibMenu.cursor = hit.index
			gameLibMenuChoose(hit.index)
			return true
		}
	}
	if gameLibMenu.panel.W > 0 && pointInRect(mx, my, gameLibMenu.panel) {
		return true
	}
	closeGameLibMenu(true)
	return true
}

// gameLibMenuKey is a row's button in this frame's words (inputhints.go). A
// key only a keyboard has (R, T) is named on a keyboard only.
func gameLibMenuKey(button, label string) string {
	if button == "" {
		return ""
	}
	key := hintKeyFor(button, label)
	if !hintsUseKeyboard() && hintClassOf(key, brickHasFunctionKeys()) == hintClassKey {
		return ""
	}
	return key
}

// renderGameLibMenu draws the menu over the library: one row per action with
// its button on the right, the highlighted row's line under the list.
func renderGameLibMenu(renderer *sdl.Renderer, config *Config) {
	if !gameLibMenu.open || renderer == nil {
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
	rows := gameLibMenu.rows
	if len(rows) == 0 {
		return
	}
	if gameLibMenu.cursor >= len(rows) {
		gameLibMenu.cursor = len(rows) - 1
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const pad = OverlayPadX
	rowH := max32(52, medH+22)
	subtitle := "Every console"
	if gameLib.focus == 1 {
		if system, ok := selectedLibrarySystem(); ok {
			subtitle = system.Name
		}
	}
	header := UIPanelHeader("Library", subtitle, "list")
	headerH := UIPanelHeaderBand(config, header, 14)
	// The highlighted row's line, then the hint line.
	footerH := 2*smallH + 30
	panelW := int32(660)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := headerH + rowH*int32(len(rows)) + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	visible := int((panelH - headerH - footerH) / rowH)
	if visible < 1 {
		visible = 1
	}
	if gameLibMenu.cursor < gameLibMenu.scroll {
		gameLibMenu.scroll = gameLibMenu.cursor
	}
	if gameLibMenu.cursor >= gameLibMenu.scroll+visible {
		gameLibMenu.scroll = gameLibMenu.cursor - visible + 1
	}
	if gameLibMenu.scroll > len(rows)-visible {
		gameLibMenu.scroll = len(rows) - visible
	}
	if gameLibMenu.scroll < 0 {
		gameLibMenu.scroll = 0
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	gameLibMenu.panel = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + headerH
	innerW := panelW - 2*pad
	gameLibMenu.rowHits = gameLibMenu.rowHits[:0]
	focusColor := installHighlight()
	for i := gameLibMenu.scroll; i < len(rows) && i < gameLibMenu.scroll+visible; i++ {
		row := rows[i]
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i-gameLibMenu.scroll)*rowH, W: innerW, H: rowH - 6}
		gameLibMenu.rowHits = append(gameLibMenu.rowHits, gameLibRowHit{index: i, rect: rect})
		selected := i == gameLibMenu.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}
		titleW := rect.W - 28
		if key := gameLibMenuKey(row.button, row.title); key != "" {
			kw, _ := measureText(font, key)
			renderText(renderer, config, font, key, ColorTextTertiary(), rect.X+rect.W-14-kw, rect.Y+(rect.H-smallH)/2)
			titleW -= kw + 16
		}
		renderText(renderer, config, medium, ellipsize(medium, row.title, titleW), ColorTextPrimary(),
			rect.X+14, rect.Y+(rect.H-medH)/2)
	}

	lineY := panelY + panelH - footerH + 6
	if c := gameLibMenu.cursor; c >= 0 && c < len(rows) {
		renderText(renderer, config, font, ellipsize(font, rows[c].detail, innerW), ColorTextSecondary(), panelX+pad, lineY)
	}
	hint := hintLine(FooterHint{"D-Pad", "Choose"}, FooterHint{"A", "Open"}, FooterHint{"B", "Close"})
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextTertiary(), panelX+pad, lineY+smallH+8)
}
