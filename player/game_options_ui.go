package main

// game_options_ui.go — the per-game options sheet in the library.
//
// The library is a browser: A plays, X opens LAN play, L1 fills covers. The
// settings that belong to one title rather than the whole card live behind
// Start (or O on a keyboard): a CPU ceiling for that game and extra arguments
// for its launcher. Nothing here writes to the emulator or the firmware
// directly — the sheet stores a small JSON record and the launch path
// (game_library.go) applies it for that child only.

import (
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	gameOptionsRowCeiling = iota
	gameOptionsRowArgs
	gameOptionsRowReset
	gameOptionsRowCount
)

var gameOptsUI struct {
	open   bool
	game   libraryGame
	cursor int
	opts   gameOptions
	panel  sdl.Rect
	// rowHits are the clickable rows, filled by the renderer.
	rowHits []gameLibRowHit
}

// gameArgsBuffer is what the on-screen keyboard edits for the arguments row.
// It is separate from gameOptsUI.opts so a dismissed keyboard changes nothing.
var gameArgsBuffer string

func gameOptionsRowName(row int) string {
	switch row {
	case gameOptionsRowCeiling:
		return "CPU limit"
	case gameOptionsRowArgs:
		return "Extra arguments"
	case gameOptionsRowReset:
		return "Reset"
	}
	return ""
}

// gameOptionsRowHint is the second line under a row label, explaining what the
// value does rather than repeating it.
func gameOptionsRowHint(row int) string {
	switch row {
	case gameOptionsRowCeiling:
		return "Clock ceiling for this game, restored when it exits"
	case gameOptionsRowArgs:
		return "Added after the ROM path, for launchers that read options"
	case gameOptionsRowReset:
		return "Back to the global power profile and no extra arguments"
	}
	return ""
}

// gameOptionsRowValue is the right-hand value of a row.
func gameOptionsRowValue(row int, opts gameOptions) string {
	switch row {
	case gameOptionsRowCeiling:
		return formatGameCeiling(opts.MaxFreqKHz)
	case gameOptionsRowArgs:
		if strings.TrimSpace(opts.Args) == "" {
			return "None"
		}
		return strings.TrimSpace(opts.Args)
	case gameOptionsRowReset:
		if opts.empty() {
			return "Defaults"
		}
		return "Restore defaults"
	}
	return ""
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
	gameOptsUI.open = true
	gameOptsUI.game = game
	gameOptsUI.cursor = 0
	gameOptsUI.opts = gameOptionsFor(game.Path)
	gameOptsUI.rowHits = gameOptsUI.rowHits[:0]
	PlayActivateSound()
	return true
}

func closeGameOptions() {
	if !gameOptsUI.open {
		return
	}
	gameOptsUI.open = false
	gameOptsUI.rowHits = gameOptsUI.rowHits[:0]
	PlayBackSound()
}

// gameOptionsMove keeps the highlight on a real row.
func gameOptionsMove(delta int) {
	if delta == 0 {
		return
	}
	gameOptsUI.cursor += delta
	if gameOptsUI.cursor < 0 {
		gameOptsUI.cursor = 0
	}
	if gameOptsUI.cursor >= gameOptionsRowCount {
		gameOptsUI.cursor = gameOptionsRowCount - 1
	}
	PlayNavSound()
}

// adjustGameOptions changes the highlighted value with Left/Right.
func adjustGameOptions(delta int) {
	if gameOptsUI.cursor != gameOptionsRowCeiling || delta == 0 {
		return
	}
	next := cycleGameCeiling(gameOptsUI.opts.MaxFreqKHz, delta)
	if next == gameOptsUI.opts.MaxFreqKHz {
		return
	}
	gameOptsUI.opts = updateGameOptions(gameOptsUI.game.Path, func(o gameOptions) gameOptions {
		o.MaxFreqKHz = next
		return o
	})
	PlayNavSound()
}

// activateGameOptionsRow runs the highlighted row.
func activateGameOptionsRow() {
	switch gameOptsUI.cursor {
	case gameOptionsRowCeiling:
		adjustGameOptions(1)
	case gameOptionsRowArgs:
		vkOpenGameArgs()
	case gameOptionsRowReset:
		resetGameOptions()
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
		closeGameOptions()
	}
	return true
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
		closeGameOptions()
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
	closeGameOptions()
	return true
}

// renderGameOptionsPanel draws the sheet over the library.
func renderGameOptionsPanel(renderer *sdl.Renderer, config *Config) {
	if !gameOptsUI.open || renderer == nil {
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
	rowH := int32(58)
	headerH := int32(18) + medH + int32(4) + smallH + int32(14)
	noteH := int32(12) + smallH
	footerH := int32(14) + smallH
	panelW := int32(620)
	if panelW > screenWidth-64 {
		panelW = screenWidth - 64
	}
	panelH := headerH + rowH*gameOptionsRowCount + noteH + footerH
	if panelH > screenHeight-24 {
		panelH = screenHeight - 24
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	gameOptsUI.panel = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	// A second dim layer separates the sheet from the list it sits on.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(0, 0, 0, 150)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	fillRoundedRect(renderer, panelX, panelY, panelW, panelH, 18, ColorPanelRaised)
	strokeRoundedRect(renderer, panelX, panelY, panelW, panelH, 18, 1, ColorBorderDefault)
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 110)
	renderer.FillRect(&sdl.Rect{X: panelX + 16, Y: panelY + 1, W: panelW - 32, H: 2})

	renderText(renderer, config, medium, "Game options", ColorTextPrimary(), panelX+pad, panelY+18)
	subtitle := gameOptsUI.game.Title
	if system := strings.TrimSpace(gameOptsUI.game.System); system != "" {
		subtitle += " · " + system
	}
	renderText(renderer, config, font, ellipsize(font, subtitle, panelW-2*pad), ColorTextTertiary(),
		panelX+pad, panelY+18+medH+4)

	bodyY := panelY + headerH
	innerW := panelW - 2*pad
	gameOptsUI.rowHits = gameOptsUI.rowHits[:0]
	focusColor := installHighlight()
	for i := 0; i < gameOptionsRowCount; i++ {
		rect := sdl.Rect{X: panelX + pad, Y: bodyY + int32(i)*rowH, W: innerW, H: rowH - 8}
		gameOptsUI.rowHits = append(gameOptsUI.rowHits, gameLibRowHit{index: i, rect: rect})
		selected := i == gameOptsUI.cursor
		bg := WithAlpha(ColorSurfaceRow, 165)
		if selected {
			bg = WithAlpha(focusColor, 46)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}

		renderText(renderer, config, medium, gameOptionsRowName(i), ColorTextPrimary(),
			rect.X+14, rect.Y+8)
		// Leave room for the right-aligned value instead of letting a long hint
		// run underneath it.
		renderText(renderer, config, font, ellipsize(font, gameOptionsRowHint(i), rect.W-28-170), ColorTextTertiary(),
			rect.X+14, rect.Y+8+medH+2)

		value := gameOptionsRowValue(i, gameOptsUI.opts)
		valueColor := ColorTextSecondary()
		if i == gameOptionsRowCeiling && gameOptsUI.opts.MaxFreqKHz > 0 {
			valueColor = focusColor
		}
		if i == gameOptionsRowArgs && strings.TrimSpace(gameOptsUI.opts.Args) != "" {
			valueColor = focusColor
		}
		// The ceiling is a ladder, so a highlighted row shows which way it
		// moves; a static value would leave Left/Right undiscoverable.
		if selected && i == gameOptionsRowCeiling {
			value = "‹  " + value + "  ›"
		}
		value = ellipsize(font, value, innerW/2)
		vw, _, _ := font.SizeUTF8(value)
		renderText(renderer, config, font, value, valueColor,
			rect.X+rect.W-14-int32(vw), rect.Y+(rect.H-smallH)/2)
	}

	noteY := bodyY + rowH*gameOptionsRowCount + 4
	renderText(renderer, config, font,
		ellipsize(font, "Applies to this launch only — the firmware ceiling comes back after.", innerW),
		ColorTextTertiary(), panelX+pad, noteY)

	hint := hintLine(FooterHint{"D-Pad", "Row"}, FooterHint{"A", "Change"},
		FooterHint{"B", "Back"})
	renderText(renderer, config, font, ellipsize(font, hint, innerW), ColorTextSecondary(),
		panelX+pad, panelY+panelH-footerH+2)
}
