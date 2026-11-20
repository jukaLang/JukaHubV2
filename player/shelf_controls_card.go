package main

// shelf_controls_card.go — the shelves' launch gate: a DOS game started by an
// old start program is sent to its current one, and a ScummVM or DOS game's
// first start shows its controls first.
//
// A point-and-click adventure on a D-pad, or a PC game on a pad, is only
// playable once the player knows where the mouse and the keyboard went. So
// the first time such a game starts, a short card shows its control layout's
// chart (ctrlLayoutForGame: the game's own choice, its console's, or the
// shelf's default) and waits: A ("Got it") starts the game, B cancels it. The
// card is never shown again for that game ("seen" in shelves.json, keyed by
// the game's folder, so a new start program does not bring it back).
//
// The gate holds the launch, shows the card and calls launchLibraryGame again
// on A, with a pass for that game's path valid a minute (the launch path
// re-enters itself after a save download or a C library install, and must not
// meet the card twice). main.go routes the card's input and drawing on every
// screen (shelfCardGlobal below), so a launch from the list, the recents or
// Files shows it too; without that route the card would only show over the
// game page (gamePageRegisterOverlay), and a launch from elsewhere would go
// ahead with a one-time toast naming the three most important buttons. Only
// a RetroArch launch of DOSBox Pure or the ScummVM core gets a card: the
// charts describe those.

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// shelfCardGlobal says main.go routes the card's input and drawing
// (shelfCardKey, shelfCardPad, shelfCardMouseEvent, shelfCardRender) for
// every screen, not only the game page.
const shelfCardGlobal = true

// shelfCardPassFor is how long a released launch may re-enter the gate.
const shelfCardPassFor = time.Minute

var shelfCard struct {
	open     bool
	game     libraryGame
	launcher string
	layout   ctrlLayout
	rect     sdl.Rect
	// pass lets the launch the card released through once more.
	passPath  string
	passUntil time.Time
	// eatRelease swallows the mouse release after a press the card used.
	eatRelease bool
}

func init() {
	gameLaunchRegisterGate(shelfGate)
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "shelf-controls",
		IsOpen: func() bool { return shelfCard.open && !shelfCardGlobal },
		Key:    shelfCardKey,
		Pad:    shelfCardPad,
		Mouse:  shelfCardMouse,
		Render: shelfCardRender,
		Close:  func() { shelfCard.open = false },
	})
}

// shelfGate is the shelves' launch gate (gamelaunch_hooks.go).
func shelfGate(game libraryGame, launcher string) (bool, error) {
	if game.System != "DOS" && game.System != "ScummVM" {
		return false, nil
	}
	// A DOS folder game whose start program changed since this path was
	// listed starts the current one.
	if game.System == "DOS" {
		if start, ok := shelfDOSRedirect(game.Path); ok {
			moved := game
			moved.Path = start
			logInfo("library", "%s starts with %s now", game.Title, filepath.Base(start))
			return true, launchLibraryGame(moved, launcher)
		}
	}
	if strings.TrimSpace(launcher) == "" {
		return false, nil // the launch path's own "no emulator" answer
	}
	if shelfCard.passPath != "" && shelfSamePath(shelfCard.passPath, game.Path) && time.Now().Before(shelfCard.passUntil) {
		return false, nil
	}
	key := shelfGameKey(game.Path)
	if shelfSeen(key) {
		return false, nil
	}
	// The charts describe DOSBox Pure and the ScummVM core in RetroArch, where
	// the layouts are applied (ctrllayout_apply.go): a game another emulator
	// plays gets no card that would be wrong for it.
	core := "dosbox_pure"
	if game.System == "ScummVM" {
		core = "scummvm"
	}
	if !retroArchLauncher(launcher) || gameCoreFromLauncher(launcher, game.System) != core {
		return false, nil
	}
	layout, ok := ctrlLayoutForGame(game)
	if !ok || len(layout.Chart) == 0 {
		return false, nil
	}
	if shelfCard.open || (!shelfCardGlobal && !gmetaUI.open) {
		// No page to show the card on: the buttons that matter, once, after
		// the caller's own "Playing …" toast.
		shelfMarkSeen(key)
		msg := shelfCardToast(game, layout)
		tbxPost(func() { ShowToast(msg, ToastKindInfo) })
		return false, nil
	}
	shelfCard.open = true
	shelfCard.game = game
	shelfCard.launcher = launcher
	shelfCard.layout = layout
	shelfCard.rect = sdl.Rect{}
	PlayActivateSound()
	// Over the caller's "Playing …", which is not true yet.
	tbxPost(func() {
		if shelfCard.open {
			ShowToast("Here is how "+game.Label()+" plays — A to start", ToastKindInfo)
		}
	})
	return true, nil
}

// shelfDOSRedirect is the start program to run instead of path, when path is
// one the player switched the game away from ("", false otherwise: the
// current start, or another program of the folder opened on purpose).
func shelfDOSRedirect(path string) (string, bool) {
	consoleDir, system, ok := shelfConsoleOf(path)
	if !ok || system != "DOS" {
		return "", false
	}
	gameDir, _ := shelfGameDir(consoleDir, path, false)
	if gameDir == "" {
		return "", false
	}
	rel, err := filepath.Rel(gameDir, path)
	if err != nil {
		return "", false
	}
	replaced := false
	for _, r := range shelfDOSGet(gameDir).Replaced {
		if strings.EqualFold(r, filepath.ToSlash(rel)) {
			replaced = true
			break
		}
	}
	if !replaced {
		return "", false
	}
	start := shelfDOSStart(gameDir)
	if start == "" || shelfSamePath(start, path) || !regularFile(start) {
		return "", false
	}
	return start, true
}

// shelfCardToast is the one-line version of the chart: its first three lines.
func shelfCardToast(game libraryGame, layout ctrlLayout) string {
	parts := make([]string, 0, 3)
	for _, line := range layout.Chart {
		if len(parts) == 3 {
			break
		}
		parts = append(parts, line.Button+" — "+line.Action)
	}
	return game.Label() + ": " + strings.Join(parts, " · ")
}

// shelfCardAccept is "Got it": never again for this game, and the game starts.
func shelfCardAccept() {
	game, launcher := shelfCard.game, shelfCard.launcher
	shelfCard.open = false
	shelfMarkSeen(shelfGameKey(game.Path))
	shelfCard.passPath, shelfCard.passUntil = game.Path, time.Now().Add(shelfCardPassFor)
	if err := launchLibraryGame(game, launcher); err != nil {
		ShowToast("Cannot launch "+game.Title+": "+err.Error(), ToastKindError)
		return
	}
	gameLib.recents = recentGames()
	if gmetaUI.open && gmetaUI.game.Path == game.Path {
		gmetaUI.launched = time.Now()
	}
	PlaySuccessSound()
	ShowToast("Playing "+game.Label(), ToastKindSuccess)
}

// shelfCardCancel is B: no game, and the card comes back next time.
func shelfCardCancel() {
	shelfCard.open = false
	PlayBackSound()
}

func shelfCardAction(act tbxAction) {
	switch act {
	case tbxActConfirm:
		shelfCardAccept()
	case tbxActBack:
		shelfCardCancel()
	}
}

// shelfCardKey, shelfCardPad and shelfCardMouse are the card's input; true
// means it was used (an open card uses every press).
func shelfCardKey(e *sdl.KeyboardEvent) bool {
	if !shelfCard.open {
		return false
	}
	shelfCardAction(tbxKeyAction(e))
	return true
}

func shelfCardPad(e *sdl.ControllerButtonEvent) bool {
	if !shelfCard.open {
		return false
	}
	shelfCardAction(tbxPadAction(e))
	return true
}

// shelfCardMouse: a click on the card is "Got it", outside it cancels.
func shelfCardMouse(mx, my int32) bool {
	if !shelfCard.open {
		return false
	}
	shelfCard.eatRelease = true
	if shelfCard.rect.W > 0 && pointInRect(mx, my, shelfCard.rect) {
		shelfCardAccept()
	} else {
		shelfCardCancel()
	}
	return true
}

// shelfCardMouseEvent is main.go's route: a press acts, and the release that
// follows it is swallowed too, so the screen behind a cancelled card does not
// act on it.
func shelfCardMouseEvent(down bool, mx, my int32) bool {
	if down {
		return shelfCardMouse(mx, my)
	}
	if shelfCard.eatRelease {
		shelfCard.eatRelease = false
		return true
	}
	return false
}

// shelfCardRender draws the card: the layout's name, then its chart as two
// columns, buttons in the accent colour.
func shelfCardRender(renderer *sdl.Renderer, config *Config) {
	if !shelfCard.open {
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	glyph := "gamepad"
	if shelfCard.game.System == "ScummVM" {
		glyph = "scummvm"
	} else if shelfCard.game.System == "DOS" {
		glyph = "dos"
	}
	header := UIPanelHeader("Controls", shelfCard.game.Label(), glyph)
	headerH := UIPanelHeaderBand(config, header, 10)
	lines := shelfCard.layout.Chart
	lineH := fonts.bodyH + 10
	footerH := fonts.smallH + 20
	nameH := fonts.smallH + 14
	cardW := min(int32(620), screenWidth-64)
	cardH := min(headerH+nameH+int32(len(lines))*lineH+footerH+12, screenHeight-48)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	shelfCard.rect = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	x := card.X + OverlayPadX
	w := card.W - 2*OverlayPadX
	y := card.Y + headerH
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, shelfCard.layout.Name, w), ColorTextSecondary(), x, y)
	y += nameH
	// The button column is as wide as its widest label, up to 40 % of the card.
	colW := int32(0)
	for _, l := range lines {
		if tw, _ := measureText(fonts.body, l.Button); tw > colW {
			colW = tw
		}
	}
	colW = min(colW+18, w*2/5)
	bottom := card.Y + card.H - footerH - 4
	for _, l := range lines {
		if y+lineH > bottom {
			break
		}
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, l.Button, colW-12), glassAccent(), x, y)
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, l.Action, w-colW), ColorTextPrimary(), x+colW, y)
		y += lineH
	}
	hint := hintLine(FooterHint{"A", "Got it"}, FooterHint{"B", "Cancel"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, w), ColorTextSecondary(), x, card.Y+card.H-footerH+4)
}
