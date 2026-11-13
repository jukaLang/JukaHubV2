package main

// emupicker.go — "Which emulator?" when more than one can play a game.
//
// A console often has several emulators on the card or the PC: CrossMix ships
// a GBA folder and an mGBA one, Windows may have RetroArch's core beside
// standalone mGBA, a PlayStation game fits PCSX-ReARMed and DuckStation.
// findGameLaunchers (game_library.go) lists every usable one; when there are
// two or more, starting the game (Games library, Continue, Files) opens this
// small sheet instead of silently taking the first:
//
//	A  start the game with the highlighted emulator (remembered as the
//	   highlight for next time)
//	Y  always use it for this game — no more asking; the game options sheet's
//	   Emulator row sets it back to "Ask each time"
//	B  cancel
//
// A per-system pin in game-launchers.json still wins over the question, as it
// did before, and a single match starts at once.

import (
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

var emuPickUI struct {
	open    bool
	game    libraryGame
	options []emuEntry
	// details are the lines under the names, worked out once on open:
	// retroArchLauncher reads a launch script, too slow for every frame.
	details []string
	cursor  int
	scroll  int
	visible int
	then    func(launcher string)
	panel   sdl.Rect
	rowHits []gameLibRowHit
	// eatUp swallows the release of a click the sheet took (emuPickEatRelease).
	eatUp   bool
}

// gameLauncherPinned is the launcher a game was set to always use, or "" when
// none is set or its file is gone (then the picker asks again).
func gameLauncherPinned(opts gameOptions) string {
	if p := strings.TrimSpace(opts.Emulator); p != "" && regularFile(p) {
		return p
	}
	return ""
}

// resolveGameLauncherFor is resolveGameLauncher for one game: the emulator it
// always uses, else the one picked last, else the system's. The save-state
// list, the diagnosis and Reset game read the emulator the game really runs
// in through it. Safe off the main thread.
func resolveGameLauncherFor(game libraryGame) string {
	if game.System == androidLibrarySystem || game.System == html5LibrarySystem {
		return resolveGameLauncher(game.System)
	}
	opts := gameOptionsFor(game.Path)
	if p := gameLauncherPinned(opts); p != "" {
		return p
	}
	if configured := configuredGameLauncher(game.System, loadGameLauncherOverrides(gameLauncherOverridesPath())); configured != "" {
		return configured
	}
	if last := strings.TrimSpace(opts.LastEmulator); last != "" && regularFile(last) {
		return last
	}
	return resolveGameLauncher(game.System)
}

// chooseGameLauncher runs launch with the emulator game should start in. When
// several fit and none is pinned it asks first (the sheet below) and launch
// runs on the answer; B runs nothing. An empty launcher means none was found:
// the caller says so, as before. Main thread.
func chooseGameLauncher(game libraryGame, launch func(launcher string)) {
	if launch == nil {
		return
	}
	if emuPickUI.open {
		// One question at a time: a second start (a voice command, Continue
		// behind it) must not replace the game the sheet is asking about.
		return
	}
	if game.System == androidLibrarySystem || game.System == html5LibrarySystem {
		launch(resolveGameLauncher(game.System))
		return
	}
	opts := gameOptionsFor(game.Path)
	if p := gameLauncherPinned(opts); p != "" {
		launch(p)
		return
	}
	if configured := configuredGameLauncher(game.System, loadGameLauncherOverrides(gameLauncherOverridesPath())); configured != "" {
		launch(configured)
		return
	}
	options := findGameLaunchers(game.System, emulatorEntries())
	switch len(options) {
	case 0:
		launch("")
		return
	case 1:
		launch(options[0].Launch)
		return
	}
	emuPickUI.open = true
	emuPickUI.game = game
	emuPickUI.options = options
	emuPickUI.details = make([]string, len(options))
	for i, e := range options {
		emuPickUI.details[i] = emuPickDetail(e)
	}
	emuPickUI.cursor = 0
	emuPickUI.scroll = 0
	emuPickUI.visible = 0
	emuPickUI.then = launch
	emuPickUI.rowHits = emuPickUI.rowHits[:0]
	last := filepath.Clean(strings.TrimSpace(opts.LastEmulator))
	for i, e := range options {
		if opts.LastEmulator != "" && strings.EqualFold(filepath.Clean(e.Launch), last) {
			emuPickUI.cursor = i
			break
		}
	}
	PlayActivateSound()
}

// emuPickOpen reports whether the sheet is up (main.go routes input to it).
func emuPickOpen() bool { return emuPickUI.open }

func emuPickClose() {
	emuPickUI.open = false
	emuPickUI.then = nil
	emuPickUI.options = nil
	emuPickUI.details = nil
	emuPickUI.rowHits = emuPickUI.rowHits[:0]
}

func emuPickCancel() {
	if !emuPickUI.open {
		return
	}
	emuPickClose()
	PlayBackSound()
}

// emuPickChoose starts the game with the highlighted emulator; always pins it
// to the game.
func emuPickChoose(always bool) {
	if !emuPickUI.open || emuPickUI.cursor < 0 || emuPickUI.cursor >= len(emuPickUI.options) {
		return
	}
	choice := emuPickUI.options[emuPickUI.cursor]
	game, then := emuPickUI.game, emuPickUI.then
	emuPickClose()
	updateGameOptions(game.Path, func(o gameOptions) gameOptions {
		o.LastEmulator = choice.Launch
		if always {
			o.Emulator = choice.Launch
		}
		return o
	})
	if always {
		ShowToast(game.Label()+" always starts in "+choice.Name+" · Game options → Emulator to change", ToastKindInfo)
	}
	if then != nil {
		then(choice.Launch)
	}
}

func emuPickMove(delta int) {
	n := len(emuPickUI.options)
	if n == 0 || delta == 0 {
		return
	}
	next := emuPickUI.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= n {
		next = n - 1
	}
	if next == emuPickUI.cursor {
		return
	}
	emuPickUI.cursor = next
	PlayNavSound()
}

// handleEmuPickKey owns every key while the sheet is up.
func handleEmuPickKey(e *sdl.KeyboardEvent) bool {
	if e == nil || !emuPickUI.open {
		return false
	}
	if e.Type != sdl.KEYDOWN {
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		emuPickMove(-1)
	case sdl.K_DOWN:
		emuPickMove(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		emuPickChoose(false)
	case sdl.K_y:
		emuPickChoose(true)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		emuPickCancel()
	}
	return true
}

// handleEmuPickController owns the pad while the sheet is up.
func handleEmuPickController(e *sdl.ControllerButtonEvent) bool {
	if e == nil || !emuPickUI.open {
		return false
	}
	if e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		emuPickMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		emuPickMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		emuPickChoose(false)
	case sdl.CONTROLLER_BUTTON_Y:
		emuPickChoose(true)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		emuPickCancel()
	}
	return true
}

// handleEmuPickMouse: a row starts the game, a click off the card cancels.
func handleEmuPickMouse(mx, my int32) bool {
	if !emuPickUI.open {
		return false
	}
	emuPickUI.eatUp = true
	for _, hit := range emuPickUI.rowHits {
		if pointInRect(mx, my, hit.rect) {
			emuPickUI.cursor = hit.index
			emuPickChoose(false)
			return true
		}
	}
	if emuPickUI.panel.W > 0 && pointInRect(mx, my, emuPickUI.panel) {
		return true
	}
	emuPickCancel()
	return true
}

// gameEmulatorLabel is the options sheet's Emulator value: the pinned
// emulator's name, else "Ask each time" when several fit, else "Automatic".
func gameEmulatorLabel(pinned string, choices []emuEntry) string {
	pinned = strings.TrimSpace(pinned)
	if pinned == "" {
		if len(choices) > 1 {
			return "Ask each time"
		}
		return "Automatic"
	}
	for _, e := range choices {
		if strings.EqualFold(filepath.Clean(e.Launch), filepath.Clean(pinned)) {
			return e.Name
		}
	}
	if !regularFile(pinned) {
		return "Ask each time (" + filepath.Base(pinned) + " is gone)"
	}
	return filepath.Base(pinned)
}

// cycleGameEmulator steps along "Ask each time" and the emulators that fit,
// without wrapping (like the sheet's other ladders). A pinned launcher no
// longer in the list steps back to asking.
func cycleGameEmulator(pinned string, choices []emuEntry, delta int) string {
	ladder := make([]string, 0, len(choices)+1)
	ladder = append(ladder, "")
	for _, e := range choices {
		ladder = append(ladder, e.Launch)
	}
	pinned = strings.TrimSpace(pinned)
	idx := -1
	for i, v := range ladder {
		if v == pinned || (v != "" && pinned != "" && strings.EqualFold(filepath.Clean(v), filepath.Clean(pinned))) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ladder) {
		idx = len(ladder) - 1
	}
	return ladder[idx]
}

// emuPickEatRelease reports (once) whether a left-button release belongs to
// the sheet: it is up, or the press that closed it was on it.
func emuPickEatRelease() bool {
	eat := emuPickUI.open || emuPickUI.eatUp
	emuPickUI.eatUp = false
	return eat
}

// emuPickDetail is the line under an emulator's name: what kind it is and
// where it lives (the last two parts of its path).
func emuPickDetail(e emuEntry) string {
	launch := filepath.Clean(e.Launch)
	where := filepath.Base(launch)
	if dir := filepath.Base(filepath.Dir(launch)); dir != "" && dir != "." && dir != string(filepath.Separator) {
		where = dir + string(filepath.Separator) + where
	}
	kind := "Standalone emulator"
	if retroArchLauncher(e.Launch) || strings.EqualFold(filepath.Ext(e.Launch), ".dll") {
		kind = "RetroArch"
	}
	if e.Version != "" {
		kind += " " + e.Version
	}
	return kind + " · " + where
}

// renderEmuPick draws the sheet above whatever screen started the game.
func renderEmuPick(renderer *sdl.Renderer, config *Config) {
	if !emuPickUI.open || renderer == nil || config == nil {
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
	header := UIPanelHeader("Which emulator?", emuPickUI.game.Label()+" · "+emuPickUI.game.System, "gamepad")
	rows := len(emuPickUI.options)
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := int32(14) + smallH
	panelW := int32(620)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := headerH + rowH*int32(rows) + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	emuPickUI.panel = card

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)
	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	bodyY := panelY + headerH
	innerW := panelW - 2*pad
	visible := int((panelH - headerH - footerH) / rowH)
	if visible < 1 {
		visible = 1
	}
	if visible > rows {
		visible = rows
	}
	if emuPickUI.cursor < emuPickUI.scroll {
		emuPickUI.scroll = emuPickUI.cursor
	}
	if emuPickUI.cursor >= emuPickUI.scroll+visible {
		emuPickUI.scroll = emuPickUI.cursor - visible + 1
	}
	if emuPickUI.scroll > rows-visible {
		emuPickUI.scroll = rows - visible
	}
	if emuPickUI.scroll < 0 {
		emuPickUI.scroll = 0
	}
	emuPickUI.visible = visible
	emuPickUI.rowHits = emuPickUI.rowHits[:0]
	focusColor := installHighlight()
	for i := 0; i < visible; i++ {
		idx := emuPickUI.scroll + i
		e := emuPickUI.options[idx]
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i)*rowH, W: innerW, H: rowH - 8}
		emuPickUI.rowHits = append(emuPickUI.rowHits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == emuPickUI.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}
		renderText(renderer, config, medium, ellipsize(medium, e.Name, rect.W-28), ColorTextPrimary(),
			rect.X+14, rect.Y+8)
		detail := ""
		if idx < len(emuPickUI.details) {
			detail = emuPickUI.details[idx]
		}
		renderText(renderer, config, font, ellipsize(font, detail, rect.W-28), ColorTextTertiary(),
			rect.X+14, rect.Y+8+medH+2)
	}

	hint := hintLine(FooterHint{"D-Pad", "Emulator"}, FooterHint{"A", "Play"},
		FooterHint{"Y", "Always use"}, FooterHint{"B", "Cancel"})
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextSecondary(),
		panelX+pad, panelY+panelH-footerH+2)
}
