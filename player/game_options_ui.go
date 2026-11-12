package main

// game_options_ui.go — the per-game options sheet in the library.
//
// The library is a browser: A plays, X opens LAN play, L1 fills covers. The
// settings that belong to one title rather than the whole card live behind
// Start (or O on a keyboard): a CPU profile and ceiling for that game, its
// RetroArch video filter (videofilter.go) and extra arguments for its
// launcher. Nothing here writes to the emulator or the firmware
// directly — the sheet stores a small JSON record and the launch path
// (game_library.go) applies it for that child only.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	gameOptionsRowTitle = iota
	gameOptionsRowHidden
	gameOptionsRowSort
	gameOptionsRowLayout
	gameOptionsRowShowHidden
	gameOptionsRowCPUProfile
	gameOptionsRowCeiling
	gameOptionsRowVideo
	gameOptionsRowArgs
	gameOptionsRowOpenIn
	gameOptionsRowDiagnose
	gameOptionsRowCovers
	gameOptionsRowReset
	gameOptionsRowCount
)

// The sheet's second step: a short list of answers that replaces the rows
// for a destructive action, with the safe answer first or last as the action
// needs (gameOptionsChoiceRows).
const (
	gameOptionsChoiceNone = iota
	gameOptionsChoiceReset
	gameOptionsChoiceCovers
)

var gameOptsUI struct {
	open   bool
	game   libraryGame
	cursor int
	scroll int
	// visible is how many rows the last paint fit; gameOptionsMove uses it to
	// keep the cursor on the card on a short screen.
	visible int
	opts    gameOptions
	panel   sdl.Rect
	// rowHits are the clickable rows, filled by the renderer.
	rowHits []gameLibRowHit
	// choice is the open second step (gameOptionsChoice*); mainCursor and
	// mainScroll put the highlight back on its row when it closes.
	choice     int
	mainCursor int
	mainScroll int
	// coverFiles are the unused covers the cleanup found, waiting for the
	// answer; coverWait is a scan started from this sheet.
	coverFiles []artCleanupFile
	coverBytes int64
	coverWait  bool
}

// gameArgsBuffer is what the on-screen keyboard edits for the arguments row.
// It is separate from gameOptsUI.opts so a dismissed keyboard changes nothing.
var gameArgsBuffer string

func gameOptionsRowName(row int) string {
	switch row {
	case gameOptionsRowTitle:
		return "Display title"
	case gameOptionsRowHidden:
		return "Visibility"
	case gameOptionsRowSort:
		return "Library sort"
	case gameOptionsRowLayout:
		return "Library view"
	case gameOptionsRowShowHidden:
		return "Show hidden"
	case gameOptionsRowCPUProfile:
		return "CPU profile"
	case gameOptionsRowCeiling:
		return "CPU limit"
	case gameOptionsRowVideo:
		return "Video filter"
	case gameOptionsRowArgs:
		return "Extra arguments"
	case gameOptionsRowOpenIn:
		return "Open in…"
	case gameOptionsRowDiagnose:
		return "Check why it won't start"
	case gameOptionsRowCovers:
		return "Clean up covers"
	case gameOptionsRowReset:
		return "Reset game…"
	}
	return ""
}

// gameOptionsRowHint is the second line under a row label, explaining what the
// value does rather than repeating it.
func gameOptionsRowHint(row int) string {
	switch row {
	case gameOptionsRowTitle:
		return "Shown in the library; the ROM file keeps its name"
	case gameOptionsRowHidden:
		return "Hide this game; Show hidden brings it back"
	case gameOptionsRowSort:
		return "Order of every console's games"
	case gameOptionsRowLayout:
		return "List, a horizontal cover row, or a vertical cover column"
	case gameOptionsRowShowHidden:
		return "Reveal games hidden from the library"
	case gameOptionsRowCPUProfile:
		return "Battery saver, Balanced or Performance while this game runs"
	case gameOptionsRowCeiling:
		return "Clock ceiling for this game, over the profile's; restored on exit"
	case gameOptionsRowVideo:
		return "RetroArch's picture for this game; looks need shaders on the card"
	case gameOptionsRowArgs:
		return "Added after the ROM path, for launchers that read options"
	case gameOptionsRowOpenIn:
		return "Hand this game to its tool: profile, compatibility or emulator"
	case gameOptionsRowDiagnose:
		return "Missing BIOS, core or game files, and where to copy them"
	case gameOptionsRowCovers:
		return "Find downloaded box art no game in the library uses"
	case gameOptionsRowReset:
		return "Settings, core overrides, remaps, DIP switches, high scores"
	}
	return ""
}

// gameOptionsRowValue is the right-hand value of a row.
func gameOptionsRowValue(row int, opts gameOptions) string {
	switch row {
	case gameOptionsRowTitle:
		if title := strings.TrimSpace(gameOptsUI.game.DisplayTitle); title != "" {
			return title
		}
		return "Default"
	case gameOptionsRowHidden:
		if gameOptsUI.game.Hidden {
			return "Hidden"
		}
		return "Visible"
	case gameOptionsRowSort:
		return librarySortLabel()
	case gameOptionsRowLayout:
		return libraryLayoutLabel()
	case gameOptionsRowShowHidden:
		if libraryShowHidden() {
			return "Shown"
		}
		return "Only visible"
	case gameOptionsRowCPUProfile:
		return gameCPUProfileLabel(opts.CPUProfile)
	case gameOptionsRowCeiling:
		return formatGameCeiling(opts.MaxFreqKHz)
	case gameOptionsRowVideo:
		return gameVideoFilterValue(opts.VideoFilter)
	case gameOptionsRowArgs:
		if strings.TrimSpace(opts.Args) == "" {
			return "None"
		}
		return strings.TrimSpace(opts.Args)
	case gameOptionsRowOpenIn:
		return "→"
	case gameOptionsRowDiagnose:
		return "Check"
	case gameOptionsRowCovers:
		if gameOptsUI.coverWait || artCleanupBusy() {
			return "Looking…"
		}
		return "→"
	case gameOptionsRowReset:
		return "Choose…"
	}
	return ""
}

// gameOptionsChoiceRows is the number of answers the open second step offers.
func gameOptionsChoiceRows() int {
	switch gameOptsUI.choice {
	case gameOptionsChoiceReset:
		return 3
	case gameOptionsChoiceCovers:
		return 2
	}
	return 0
}

// gameOptionsChoiceText is one answer's label and the line under it.
func gameOptionsChoiceText(row int) (string, string) {
	switch gameOptsUI.choice {
	case gameOptionsChoiceReset:
		switch row {
		case 0:
			return "Reset settings", "Options, core overrides, remaps, DIP switches, high scores"
		case 1:
			return "Reset settings and delete save states", "Also its save states, their screenshots and arcade NVRAM"
		case 2:
			return "Cancel", "Keep everything as it is"
		}
	case gameOptionsChoiceCovers:
		switch row {
		case 0:
			return "Move to the trash", artCleanupSummary(len(gameOptsUI.coverFiles), gameOptsUI.coverBytes)
		case 1:
			return "Cancel", "Keep every cover"
		}
	}
	return "", ""
}

// gameOptionsChoiceNote is the line under the answers.
func gameOptionsChoiceNote() string {
	switch gameOptsUI.choice {
	case gameOptionsChoiceReset:
		return "The ROM, battery saves and playtime stay; removed files go to reset-trash."
	case gameOptionsChoiceCovers:
		return "Only JukaHub's own game-art folder; the trash keeps them for a while."
	}
	return ""
}

// openGameOptionsChoice replaces the rows with a second step's answers.
func openGameOptionsChoice(choice int) {
	gameOptsUI.mainCursor = gameOptsUI.cursor
	gameOptsUI.mainScroll = gameOptsUI.scroll
	gameOptsUI.choice = choice
	// The first answer is the highlighted one: "Reset settings" keeps the
	// states, and "Move to the trash" is what the user just asked for — the
	// trash keeps the covers, so nothing is lost on a slip.
	gameOptsUI.cursor = 0
	gameOptsUI.scroll = 0
	PlayActivateSound()
}

// closeGameOptionsChoice goes back to the rows.
func closeGameOptionsChoice(sound bool) {
	gameOptsUI.choice = gameOptionsChoiceNone
	gameOptsUI.cursor = gameOptsUI.mainCursor
	gameOptsUI.scroll = gameOptsUI.mainScroll
	gameOptsUI.coverFiles = nil
	gameOptsUI.coverBytes = 0
	if sound {
		PlayBackSound()
	}
}

// activateGameOptionsChoice runs the highlighted answer.
func activateGameOptionsChoice() {
	choice, row := gameOptsUI.choice, gameOptsUI.cursor
	files := gameOptsUI.coverFiles
	closeGameOptionsChoice(false)
	switch choice {
	case gameOptionsChoiceReset:
		switch row {
		case 0:
			PlayActivateSound()
			runGameReset(gameOptsUI.game, false)
		case 1:
			PlayActivateSound()
			runGameReset(gameOptsUI.game, true)
		default:
			PlayBackSound()
		}
	case gameOptionsChoiceCovers:
		if row == 0 {
			PlayActivateSound()
			runArtCleanup(files)
			return
		}
		PlayBackSound()
	}
}

// gameOptionsPollCovers picks up a cover scan this sheet started and opens its
// question, or says there is nothing to clean. Render thread.
func gameOptionsPollCovers() {
	if !gameOptsUI.coverWait {
		return
	}
	files, bytes, why, ok := artCleanupTake()
	if !ok {
		return
	}
	gameOptsUI.coverWait = false
	switch {
	case why != "":
		ShowToast(why, ToastKindInfo)
	case len(files) == 0:
		ShowToast("No unused covers · every cover belongs to a game", ToastKindSuccess)
	case !gameOptsUI.open || gameOptsUI.choice != gameOptionsChoiceNone:
		ShowToast(artCleanupSummary(len(files), bytes)+" · Clean up covers moves them", ToastKindInfo)
	default:
		openGameOptionsChoice(gameOptionsChoiceCovers)
		gameOptsUI.coverFiles = files
		gameOptsUI.coverBytes = bytes
	}
}

// openGameOptions opens the sheet for the highlighted game. It returns false
// when the library has no game to edit, so the caller can fall through.
func openGameOptions() bool {
	if !gameLib.open || gameOptsUI.open {
		return false
	}
	game, ok := selectedLibraryGame()
	if !ok {
		ShowToast("No game selected", ToastKindInfo)
		return true
	}
	// The sheet always edits the highlighted title, whichever column has the
	// focus; its header names the game so opening it from the console list is
	// unambiguous.
	if game.ContentKey == "" {
		libraryDecorateGame(&game)
	}
	gameOptsUI.open = true
	gameOptsUI.game = game
	gameOptsUI.cursor = 0
	gameOptsUI.scroll = 0
	gameOptsUI.visible = 0
	gameOptsUI.opts = gameOptionsFor(game.Path)
	gameOptsUI.rowHits = gameOptsUI.rowHits[:0]
	gameOptsUI.choice = gameOptionsChoiceNone
	gameOptsUI.coverFiles = nil
	gameOptsUI.coverWait = false
	PlayActivateSound()
	return true
}

func closeGameOptions() {
	if !gameOptsUI.open {
		return
	}
	gameOptsUI.open = false
	gameOptsUI.rowHits = gameOptsUI.rowHits[:0]
	gameOptsUI.choice = gameOptionsChoiceNone
	gameOptsUI.coverFiles = nil
	PlayBackSound()
}

// gameOptionsRows is how many rows the sheet shows now: its options, or the
// open second step's answers.
func gameOptionsRows() int {
	if n := gameOptionsChoiceRows(); n > 0 {
		return n
	}
	if gameOptsUI.game.System == html5LibrarySystem {
		// The first five rows are title, visibility, sort and view. Emulator
		// launch options and reset controls do not apply to browser games.
		return gameOptionsRowShowHidden + 1
	}
	return gameOptionsRowCount
}

// gameOptionsMove keeps the highlight on a real row.
func gameOptionsMove(delta int) {
	if delta == 0 {
		return
	}
	rows := gameOptionsRows()
	gameOptsUI.cursor += delta
	if gameOptsUI.cursor < 0 {
		gameOptsUI.cursor = 0
	}
	if gameOptsUI.cursor >= rows {
		gameOptsUI.cursor = rows - 1
	}
	if gameOptsUI.visible > 0 {
		if gameOptsUI.cursor < gameOptsUI.scroll {
			gameOptsUI.scroll = gameOptsUI.cursor
		}
		if gameOptsUI.cursor >= gameOptsUI.scroll+gameOptsUI.visible {
			gameOptsUI.scroll = gameOptsUI.cursor - gameOptsUI.visible + 1
		}
	}
	PlayNavSound()
}

// adjustGameOptions changes the highlighted value with Left/Right.
func adjustGameOptions(delta int) {
	if delta == 0 || gameOptsUI.choice != gameOptionsChoiceNone {
		return
	}
	switch gameOptsUI.cursor {
	case gameOptionsRowCeiling:
		next := cycleGameCeiling(gameOptsUI.opts.MaxFreqKHz, delta)
		if next == gameOptsUI.opts.MaxFreqKHz {
			return
		}
		gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
			o.MaxFreqKHz = next
			return o
		})
	case gameOptionsRowCPUProfile:
		next := cycleGameCPUProfile(gameOptsUI.opts.CPUProfile, delta)
		if next == gameOptsUI.opts.CPUProfile {
			return
		}
		gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
			o.CPUProfile = next
			return o
		})
	case gameOptionsRowVideo:
		next := cycleGameVideoFilter(gameOptsUI.opts.VideoFilter, delta)
		if next == gameOptsUI.opts.VideoFilter {
			return
		}
		gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
			o.VideoFilter = next
			return o
		})
	case gameOptionsRowSort:
		cycleLibrarySort(delta)
		refreshLibraryView()
		ShowToast("Sorted by "+strings.ToLower(librarySortLabel()), ToastKindInfo)
	case gameOptionsRowLayout:
		cycleLibraryLayout(delta)
		gameLib.scroll = 0
		ShowToast("Library view: "+libraryLayoutLabel(), ToastKindInfo)
	case gameOptionsRowShowHidden:
		setLibraryShowHidden(delta > 0)
		refreshLibraryView()
		if libraryShowHidden() {
			ShowToast("Hidden games shown", ToastKindInfo)
		} else {
			ShowToast("Hidden games concealed", ToastKindInfo)
		}
	case gameOptionsRowHidden:
		toggleGameHidden()
	default:
		return
	}
	PlayNavSound()
}

// activateGameOptionsRow runs the highlighted row.
func activateGameOptionsRow() {
	if gameOptsUI.choice != gameOptionsChoiceNone {
		activateGameOptionsChoice()
		return
	}
	switch gameOptsUI.cursor {
	case gameOptionsRowTitle:
		vkOpenGameTitle()
	case gameOptionsRowHidden:
		toggleGameHidden()
	case gameOptionsRowSort:
		cycleLibrarySort(1)
		refreshLibraryView()
		ShowToast("Sorted by "+strings.ToLower(librarySortLabel()), ToastKindInfo)
	case gameOptionsRowLayout:
		cycleLibraryLayout(1)
		gameLib.scroll = 0
		ShowToast("Library view: "+libraryLayoutLabel(), ToastKindInfo)
	case gameOptionsRowShowHidden:
		setLibraryShowHidden(!libraryShowHidden())
		refreshLibraryView()
		if libraryShowHidden() {
			ShowToast("Hidden games shown", ToastKindInfo)
		} else {
			ShowToast("Hidden games concealed", ToastKindInfo)
		}
	case gameOptionsRowCeiling, gameOptionsRowCPUProfile, gameOptionsRowVideo:
		adjustGameOptions(1)
	case gameOptionsRowArgs:
		vkOpenGameArgs()
	case gameOptionsRowOpenIn:
		openGameLibraryHandoffFromSheet()
	case gameOptionsRowDiagnose:
		// The answer opens as the launch-failure card (gamediag.go).
		game := gameOptsUI.game
		closeGameOptions()
		gameDiagRunManual(game)
	case gameOptionsRowCovers:
		if gameOptsUI.coverWait || artCleanupBusy() {
			ShowToast("Still looking for unused covers…", ToastKindInfo)
			return
		}
		PlayActivateSound()
		gameOptsUI.coverWait = startArtCleanupScan()
	case gameOptionsRowReset:
		if gameOptsUI.game.System == androidLibrarySystem {
			// An Android game keeps its data in the app runtime, not in
			// RetroArch's folders: only the sheet's own record goes.
			resetGameOptions()
			return
		}
		openGameOptionsChoice(gameOptionsChoiceReset)
	}
}

// applyGameArgs commits the keyboard buffer to the highlighted game.
func applyGameArgs() {
	if !gameOptsUI.open {
		return
	}
	gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
		o.Args = gameArgsBuffer
		return o
	})
	if gameOptsUI.opts.Args == "" {
		ShowToast("No extra arguments for "+gameOptsUI.game.Title, ToastKindInfo)
		return
	}
	ShowToast("Arguments saved for "+gameOptsUI.game.Title, ToastKindSuccess)
}

// resetGameOptions drops both overrides for the highlighted game.
func resetGameOptions() {
	if gameOptsUI.opts.empty() {
		ShowToast("This game already uses the defaults.", ToastKindInfo)
		return
	}
	gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(gameOptions) gameOptions {
		return gameOptions{}
	})
	ShowToast("Defaults restored for "+gameOptsUI.game.Title, ToastKindSuccess)
}

// handleGameOptionsKey consumes every key while the sheet is open, so the list
// behind it neither scrolls nor launches anything.
func handleGameOptionsKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !gameOptsUI.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		gameOptionsMove(-1)
	case sdl.K_DOWN:
		gameOptionsMove(1)
	case sdl.K_LEFT:
		adjustGameOptions(-1)
	case sdl.K_RIGHT:
		adjustGameOptions(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		activateGameOptionsRow()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		gameOptionsBack()
	}
	return true
}

// gameOptionsBack is B: out of a second step back to the rows, else the sheet
// closes.
func gameOptionsBack() {
	if gameOptsUI.choice != gameOptionsChoiceNone {
		closeGameOptionsChoice(true)
		return
	}
	closeGameOptions()
}

func ctrlGameOptions(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !gameOptsUI.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		gameOptionsMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		gameOptionsMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		adjustGameOptions(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		adjustGameOptions(1)
	case sdl.CONTROLLER_BUTTON_A:
		activateGameOptionsRow()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		gameOptionsBack()
	default:
		return false
	}
	return true
}

// handleGameOptionsMouse clicks a row or dismisses the sheet. Clicks are
// consumed even when they land on the card, so the library does not act on the
// same tap.
func handleGameOptionsMouse(mx, my int32) bool {
	if !gameOptsUI.open {
		return false
	}
	for _, hit := range gameOptsUI.rowHits {
		if pointInRect(mx, my, hit.rect) {
			gameOptsUI.cursor = hit.index
			activateGameOptionsRow()
			return true
		}
	}
	if gameOptsUI.panel.W > 0 && pointInRect(mx, my, gameOptsUI.panel) {
		return true
	}
	gameOptionsBack()
	return true
}

// renderGameOptionsPanel draws the sheet over the library.
func renderGameOptionsPanel(renderer *sdl.Renderer, config *Config) {
	if !gameOptsUI.open || renderer == nil {
		return
	}
	gameOptionsPollCovers()
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
	// Measured from the type: at the shipped 24px roles the fixed 58px row put
	// each hint 12px into the row below (8 above, 2 between, 6 under, 8 gap).
	rowH := max32(58, medH+smallH+24)
	// The title band is the shared page header, built before the card is sized so
	// the panel and the bar drawn into it measure the same height.
	subtitle := gameOptsUI.game.Label()
	if system := strings.TrimSpace(gameOptsUI.game.System); system != "" {
		subtitle += " · " + system
	}
	title := "Game options"
	switch gameOptsUI.choice {
	case gameOptionsChoiceReset:
		title = "Reset game"
	case gameOptionsChoiceCovers:
		title = "Clean up covers"
		subtitle = "Box art no game in the library uses"
	}
	header := UIPanelHeader(title, subtitle, "gamepad")
	rows := gameOptionsRows()
	choosing := gameOptsUI.choice != gameOptionsChoiceNone
	headerH := UIPanelHeaderBand(config, header, 14)
	noteH := int32(12) + smallH
	footerH := int32(14) + smallH
	panelW := int32(620)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := headerH + rowH*int32(rows) + noteH + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	gameOptsUI.panel = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	// A second dim layer separates the sheet from the list it sits on.
	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + headerH
	innerW := panelW - 2*pad
	// The panel is capped to the screen, so a short display shows a window of
	// the rows rather than drawing the last ones through the card edge.
	bodyH := panelH - headerH - noteH - footerH
	visible := int(bodyH / rowH)
	if visible < 1 {
		visible = 1
	}
	if visible > rows {
		visible = rows
	}
	if gameOptsUI.cursor >= rows {
		gameOptsUI.cursor = rows - 1
	}
	if gameOptsUI.scroll > rows-visible {
		gameOptsUI.scroll = rows - visible
	}
	if gameOptsUI.scroll < 0 {
		gameOptsUI.scroll = 0
	}
	if gameOptsUI.cursor < gameOptsUI.scroll {
		gameOptsUI.scroll = gameOptsUI.cursor
	}
	if gameOptsUI.cursor >= gameOptsUI.scroll+visible {
		gameOptsUI.scroll = gameOptsUI.cursor - visible + 1
	}
	gameOptsUI.visible = visible
	gameOptsUI.rowHits = gameOptsUI.rowHits[:0]
	focusColor := installHighlight()
	for i := 0; i < visible; i++ {
		idx := gameOptsUI.scroll + i
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i)*rowH, W: innerW, H: rowH - 8}
		gameOptsUI.rowHits = append(gameOptsUI.rowHits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == gameOptsUI.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}

		if choosing {
			// A second step's answer: its label and what it does, full width.
			name, line := gameOptionsChoiceText(idx)
			renderText(renderer, config, medium, ellipsize(medium, name, rect.W-28), ColorTextPrimary(),
				rect.X+14, rect.Y+8)
			renderText(renderer, config, font, ellipsize(font, line, rect.W-28), ColorTextTertiary(),
				rect.X+14, rect.Y+8+medH+2)
			continue
		}
		renderText(renderer, config, medium, gameOptionsRowName(idx), ColorTextPrimary(),
			rect.X+14, rect.Y+8)
		// Leave room for the right-aligned value instead of letting a long hint
		// run underneath it.
		renderText(renderer, config, font, ellipsize(font, gameOptionsRowHint(idx), rect.W-28-170), ColorTextTertiary(),
			rect.X+14, rect.Y+8+medH+2)

		value := gameOptionsRowValue(idx, gameOptsUI.opts)
		valueColor := ColorTextSecondary()
		if idx == gameOptionsRowTitle && strings.TrimSpace(gameOptsUI.game.DisplayTitle) != "" {
			valueColor = focusColor
		}
		if idx == gameOptionsRowHidden && gameOptsUI.game.Hidden {
			valueColor = focusColor
		}
		if idx == gameOptionsRowCeiling && gameOptsUI.opts.MaxFreqKHz > 0 {
			valueColor = focusColor
		}
		if (idx == gameOptionsRowCPUProfile && gameOptsUI.opts.CPUProfile != "") ||
			(idx == gameOptionsRowVideo && gameOptsUI.opts.VideoFilter != "") {
			valueColor = focusColor
		}
		if idx == gameOptionsRowArgs && strings.TrimSpace(gameOptsUI.opts.Args) != "" {
			valueColor = focusColor
		}
		// Ladders and toggles show which way they move; a static value would
		// leave Left/Right undiscoverable.
		if selected && (idx == gameOptionsRowCeiling || idx == gameOptionsRowCPUProfile ||
			idx == gameOptionsRowVideo || idx == gameOptionsRowSort || idx == gameOptionsRowLayout ||
			idx == gameOptionsRowShowHidden || idx == gameOptionsRowHidden) {
			value = "‹  " + value + "  ›"
		}
		value = ellipsize(font, value, innerW/2)
		vw, _, _ := font.SizeUTF8(value)
		renderText(renderer, config, font, value, valueColor,
			rect.X+rect.W-14-int32(vw), rect.Y+(rect.H-smallH)/2)
	}

	noteY := bodyY + rowH*int32(visible) + 4
	note := "Display, visibility, sort and view are saved; launch rows apply to this game only."
	if gameOptsUI.game.System == html5LibrarySystem {
		note = "Display, visibility, sort and view are saved for browser games."
	}
	if choosing {
		note = gameOptionsChoiceNote()
	}
	renderText(renderer, config, font, ellipsize(font, note, innerW),
		ColorTextTertiary(), panelX+pad, noteY)

	hint := hintLine(FooterHint{"D-Pad", "Row"}, FooterHint{"A", "Change"},
		FooterHint{"B", "Back"})
	if choosing {
		hint = hintLine(FooterHint{"D-Pad", "Row"}, FooterHint{"A", "Choose"},
			FooterHint{"B", "Cancel"})
	}
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextSecondary(),
		panelX+pad, panelY+panelH-footerH+2)
}
