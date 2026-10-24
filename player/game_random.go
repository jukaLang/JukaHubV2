package main

// game_random.go — the random game picker in the Games Library.
//
// A library of a thousand ROMs is a decision problem: the same twenty titles
// are the ones anyone scrolls past. The picker (CrossMix has one as its own
// app) rolls a game for you and lands the highlight on it — console, row and
// scroll position — so the pick is visible before anything starts; A then plays
// it, R rolls again.
//
// The roll can come from the console on screen or from every console at once.
// A small sheet asks which, because a hidden modifier would make the second
// scope undiscoverable on a pad. The roll never repeats the game already
// highlighted while another candidate exists, and it only picks from what the
// panel is showing, so an active search cannot send the highlight to a row that
// is filtered out.

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// gameRandomScope is one origin for a roll: a concrete console, or every console
// when console is empty.
type gameRandomScope struct {
	console string
	title   string
	detail  string
	games   []libraryGame
}

type gameRandomPickerState struct {
	open   bool
	cursor int
	scopes []gameRandomScope
	panel  sdl.Rect
	// rowHits are the clickable scope rows, filled by the renderer.
	rowHits []gameLibRowHit
}

var gameRandomUI gameRandomPickerState

// gameRandomIntn is a seam: the picker must not roll real dice in a unit test.
var gameRandomIntn = rand.Intn

// randomPickerConsoles is the library without the Continue row: recents are a
// view over games that are already in their console, so rolling from them would
// weight a recently played title twice.
func randomPickerConsoles() []librarySystem {
	systems := currentGameSystems()
	out := make([]librarySystem, 0, len(systems))
	for _, system := range systems {
		if system.Name == continueSystemName {
			continue
		}
		out = append(out, system)
	}
	return out
}

// randomPickerCurrentConsole names the console "this console" refers to. When
// the highlight sits on Continue, the console of the highlighted recent is the
// only meaningful answer; a recent whose console is no longer on the card has
// no row to jump to, so it yields false.
func randomPickerCurrentConsole() (librarySystem, bool) {
	if system, ok := selectedLibrarySystem(); ok && system.Name != continueSystemName {
		return system, true
	}
	game, ok := selectedLibraryGame()
	if !ok {
		return librarySystem{}, false
	}
	for _, system := range randomPickerConsoles() {
		if system.Name == game.System {
			return system, true
		}
	}
	return librarySystem{}, false
}

// buildGameRandomScopes describes the choices the sheet offers. The detail line
// counts what the roll would actually draw from, so it follows an active search
// instead of promising the whole card.
func buildGameRandomScopes() []gameRandomScope {
	var scopes []gameRandomScope
	if system, ok := randomPickerCurrentConsole(); ok && len(system.Games) > 0 {
		scopes = append(scopes, gameRandomScope{
			console: system.Name,
			title:   "This console",
			detail:  fmt.Sprintf("%s · %d game(s)", system.Name, len(system.Games)),
			games:   system.Games,
		})
	}
	var all []libraryGame
	consoles := 0
	for _, system := range randomPickerConsoles() {
		if len(system.Games) == 0 {
			continue
		}
		consoles++
		all = append(all, system.Games...)
	}
	if len(all) > 0 {
		detail := fmt.Sprintf("%d game(s) in %d console(s)", len(all), consoles)
		if gameLib.query != "" {
			detail = fmt.Sprintf("%d match(es) in %d console(s)", len(all), consoles)
		}
		scopes = append(scopes, gameRandomScope{
			title:  "Whole library",
			detail: detail,
			games:  all,
		})
	}
	return scopes
}

// openGameRandomPicker shows the scope sheet. It reports whether the sheet
// opened, so the caller can fall through when the library is empty.
func openGameRandomPicker() bool {
	if gameOptsUI.open {
		return false
	}
	scopes := buildGameRandomScopes()
	if len(scopes) == 0 {
		ShowToast("No games to pick from yet — copy ROMs and press Y to rescan", ToastKindInfo)
		return false
	}
	gameRandomUI = gameRandomPickerState{open: true, scopes: scopes}
	PlayActivateSound()
	return true
}

// resetGameRandomPicker closes the sheet without a sound: a pick and a cancel
// have their own.
func resetGameRandomPicker() {
	gameRandomUI = gameRandomPickerState{}
}

func closeGameRandomPicker() {
	if !gameRandomUI.open {
		return
	}
	resetGameRandomPicker()
	PlayBackSound()
}

func gameRandomMove(delta int) {
	if delta == 0 || len(gameRandomUI.scopes) == 0 {
		return
	}
	gameRandomUI.cursor += delta
	if gameRandomUI.cursor < 0 {
		gameRandomUI.cursor = len(gameRandomUI.scopes) - 1
	}
	if gameRandomUI.cursor >= len(gameRandomUI.scopes) {
		gameRandomUI.cursor = 0
	}
	PlayNavSound()
}

// randomLibraryGame picks one game, preferring anything except avoidPath while
// there is another candidate. That is what makes a second roll feel random
// instead of landing on the row already under the highlight.
func randomLibraryGame(games []libraryGame, avoidPath string) (libraryGame, bool) {
	if len(games) == 0 {
		return libraryGame{}, false
	}
	candidates := make([]libraryGame, 0, len(games))
	for _, game := range games {
		if avoidPath != "" && game.Path == avoidPath {
			continue
		}
		candidates = append(candidates, game)
	}
	if len(candidates) == 0 {
		candidates = games
	}
	index := gameRandomIntn(len(candidates))
	if index < 0 {
		index = 0
	}
	if index >= len(candidates) {
		index = len(candidates) - 1
	}
	return candidates[index], true
}

// jumpToLibraryGame moves the console, row and scroll position onto one game.
// It reports false when the game is not in the list on screen (a stale ROM
// removed between a rescan and the roll), so the caller can say so instead of
// moving the highlight somewhere unrelated.
func jumpToLibraryGame(game libraryGame) bool {
	for systemIndex, system := range currentGameSystems() {
		if system.Name == continueSystemName {
			continue
		}
		for gameIndex, candidate := range system.Games {
			if candidate.Path != game.Path {
				continue
			}
			gameLib.focus = 1
			gameLib.system = systemIndex
			gameLib.cursor = gameIndex
			gameLib.scroll = gameIndex
			return true
		}
	}
	return false
}

// chooseGameRandomScope rolls the highlighted scope and jumps to the result.
func chooseGameRandomScope(index int) bool {
	if index < 0 || index >= len(gameRandomUI.scopes) {
		return false
	}
	scope := gameRandomUI.scopes[index]
	previous, _ := selectedLibraryGame()
	game, ok := randomLibraryGame(scope.games, previous.Path)
	if !ok {
		resetGameRandomPicker()
		ShowToast("That list has no games to pick from", ToastKindInfo)
		return true
	}
	resetGameRandomPicker()
	if !jumpToLibraryGame(game) {
		ShowToast("That game is no longer on the card — press Y to rescan", ToastKindWarning)
		return true
	}
	ShowToast("Random: "+game.Title+" — A plays it, R for another", ToastKindInfo)
	PlayActivateSound()
	return true
}

// handleGameRandomKey consumes every key while the sheet is open, so the list
// behind it neither scrolls nor launches anything.
func handleGameRandomKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !gameRandomUI.open {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		gameRandomMove(-1)
	case sdl.K_DOWN:
		gameRandomMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		chooseGameRandomScope(gameRandomUI.cursor)
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		closeGameRandomPicker()
	}
	return true
}

func ctrlGameRandom(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN || !gameRandomUI.open {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		gameRandomMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		gameRandomMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		chooseGameRandomScope(gameRandomUI.cursor)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		closeGameRandomPicker()
	}
	return true
}

// handleGameRandomMouse clicks a scope or dismisses the sheet. A click on the
// card is consumed so the library does not launch the game behind it.
func handleGameRandomMouse(mx, my int32) bool {
	if !gameRandomUI.open {
		return false
	}
	for _, hit := range gameRandomUI.rowHits {
		if pointInRect(mx, my, hit.rect) {
			gameRandomUI.cursor = hit.index
			chooseGameRandomScope(hit.index)
			return true
		}
	}
	if gameRandomUI.panel.W > 0 && pointInRect(mx, my, gameRandomUI.panel) {
		return true
	}
	closeGameRandomPicker()
	return true
}

// renderGameRandomPicker draws the scope sheet over the library.
func renderGameRandomPicker(renderer *sdl.Renderer, config *Config) {
	if !gameRandomUI.open || renderer == nil {
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

	const pad = int32(22)
	rowH := int32(66)
	headerH := int32(18) + medH + int32(4) + smallH + int32(14)
	footerH := int32(14) + smallH
	panelW := int32(640)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := headerH + rowH*int32(len(gameRandomUI.scopes)) + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	gameRandomUI.panel = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	// A second dim layer separates the sheet from the library it sits on.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 150)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 18, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 18, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 16, Y: panelY + 1, W: panelW - 32, H: 2})

	renderText(renderer, config, medium, "Random game", ColorTextPrimary(), panelX+pad, panelY+18)
	subtitle := "Where should the pick come from?"
	if gameLib.query != "" {
		subtitle = "Picking from the current search"
	}
	renderText(renderer, config, font, ellipsize(font, subtitle, panelW-2*pad), ColorTextTertiary(),
		panelX+pad, panelY+18+medH+4)

	bodyY := panelY + headerH
	innerW := panelW - 2*pad
	gameRandomUI.rowHits = gameRandomUI.rowHits[:0]
	focusColor := installHighlight()
	for i, scope := range gameRandomUI.scopes {
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i)*rowH, W: innerW, H: rowH - 8}
		gameRandomUI.rowHits = append(gameRandomUI.rowHits, gameLibRowHit{index: i, rect: rect})
		selected := i == gameRandomUI.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}
		renderText(renderer, config, medium, scope.title, ColorTextPrimary(), rect.X+14, rect.Y+8)
		detailColor := ColorTextTertiary()
		if selected {
			detailColor = ColorTextSecondary()
		}
		renderText(renderer, config, font, ellipsize(font, strings.TrimSpace(scope.detail), rect.W-28),
			detailColor, rect.X+14, rect.Y+8+medH+4)
	}

	hint := hintLine(FooterHint{"D-Pad", "Choose"}, FooterHint{"A", "Pick"}, FooterHint{"B", "Cancel"})
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextSecondary(),
		panelX+pad, panelY+panelH-footerH+2)
}
