package main

// gamemenu.go — the in-game menu: hold Select + L2 in any game or emulator
// JukaHub started (RetroArch, PPSSPP, a port) and the game pauses under a
// small glass menu: Resume game, Save state and Load state (a RetroArch game
// the library started: six slots with pictures, gamemenu_states.go), Ask
// Juka (with the game assistant on), Sleep and Quit to JukaHub.
//
// It is the game assistant's overlay in another mode, so it inherits all of
// its machinery and guard rails (gameassist.go): the pad watcher sees the
// chord and grabs the pad (gameassist_linux.go), the game's process group is
// stopped (SIGSTOP) while its last frame stays on screen dimmed, presses reach
// the menu through gaLink's queue, an alarm or the game ending takes it down,
// and the three-minute cap gives the game back. Nothing is asked of the
// emulator, which is why it works over any of them.
//
// Quit to JukaHub asks the game to quit the way the exit path does
// (gamesuspend.go stopScreenChildren): an interrupt to its process group —
// RetroArch and PPSSPP both treat it as quit, writing SRAM and settings on
// the way out — then a continue so the stopped game acts on it. The overlay
// hands the screen back to the game for its last frames, and once the group
// is gone the parked main loop shows JukaHub again. A game that ignores the
// interrupt is killed after gmQuitGrace.
//
// Sleep gives the game back as Resume does and puts the handheld to sleep the
// way its power button does (sleepNow, powersleep.go): every kind of game —
// RetroArch, PPSSPP, a port — is frozen with the rest of the system while it
// sleeps and carries on when it wakes.
//
// A Linux handheld only, like the assistant's overlay: on a PC nothing can be
// drawn over a game's own window.

import (
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// gameMenuKey is the Custom setting behind Settings → Controls →
	// "In-game menu" (on unless it is false).
	gameMenuKey = "gameMenu"
	// gmQuitGrace is how long a game asked to quit gets before its group is
	// killed: RetroArch writing a save state on exit takes a moment.
	gmQuitGrace = 6 * time.Second
)

// gmQuitting is set from Quit to JukaHub until the game is gone (or killed):
// neither chord reopens an overlay over a game writing its save on the way
// out (gameAssistParkedTick, gaOpen). Any goroutine.
var gmQuitting atomic.Bool

// gmItem is one row of the menu.
type gmItem int

const (
	gmResume gmItem = iota
	gmAsk
	gmQuit
	gmSave
	gmLoad
	gmSleep
)

// gameMenuEnabled reports Settings → Controls → In-game menu.
func gameMenuEnabled(c *Config) bool {
	if c == nil {
		return false
	}
	return customBool(c.Variables.Custom, gameMenuKey, true)
}

// gameMenuSettingsRow is the switch, shown on a handheld that can draw over a
// game (Settings → Controls, under the mouse rows: stickcal.go).
func gameMenuSettingsRow() setRow {
	return setRow{
		ID: gameMenuKey, Label: "In-game menu", Kind: setKindToggle,
		Detail:  setStatic("Hold Select + L2 in a game: resume, save or load, sleep, ask Juka or quit"),
		Visible: func(*Config) bool { return gameAssistSupported() },
		Get:     gameMenuEnabled,
		Set: func(c *Config, on bool) {
			gameAssistStore(c, gameMenuKey, on)
			setToast("In-game menu: " + setOnOff(on))
		},
	}
}

// gmItems is the menu's rows: the save slots only for a RetroArch game the
// library started (gmStatesAvailable), Ask Juka only with the game assistant
// on.
func gmItems(config *Config) []gmItem {
	items := []gmItem{gmResume}
	if gmStatesAvailable() {
		items = append(items, gmSave, gmLoad)
	}
	if gameAssistEnabled(config) {
		items = append(items, gmAsk)
	}
	return append(items, gmSleep, gmQuit)
}

// gmLabel is a row's words: its label and the smaller line under it.
func gmLabel(it gmItem) (string, string) {
	switch it {
	case gmAsk:
		return "Ask Juka", "Say what you need help with"
	case gmQuit:
		return "Quit to JukaHub", "Progress since your last save is lost"
	case gmSave:
		return "Save state", "Six slots, each with a picture and its date"
	case gmLoad:
		return "Load state", "Go back to a moment you saved"
	case gmSleep:
		return "Sleep", "The game carries on when the handheld wakes"
	}
	return "Resume game", ""
}

// gmShowMenu turns the overlay into the menu, from the assistant (Select +
// L2 over it, or B after the menu opened it): the orb goes, and so does
// whatever Juka was saying.
func gmShowMenu() {
	if voiceOrb.phase != orbHidden {
		voiceOrbCancelRequest()
		voiceOrbHideNow()
	}
	jukaSpeakStop()
	gaOverlay.mode = gaModeMenu
	gaOverlay.sel = 0
}

// gmPress acts on one press while the menu is up.
func gmPress(config *Config, b gaButton) {
	if gmPick.open {
		gmPickPress(b)
		return
	}
	items := gmItems(config)
	if gaOverlay.sel >= len(items) {
		gaOverlay.sel = len(items) - 1
	}
	switch b {
	case gaBtnUp:
		gaOverlay.sel = (gaOverlay.sel + len(items) - 1) % len(items)
		PlayNavSound()
	case gaBtnDown:
		gaOverlay.sel = (gaOverlay.sel + 1) % len(items)
		PlayNavSound()
	case gaBtnB, gaBtnMenu, gaBtnMenuChord:
		PlayBackSound()
		gaClose("the player went back", true)
	case gaBtnChord:
		// Select + R2 over the menu: straight to the assistant.
		if gameAssistEnabled(config) {
			gmAskJuka(config)
		}
	case gaBtnA:
		switch items[gaOverlay.sel] {
		case gmResume:
			PlayBackSound()
			gaClose("the player resumed", true)
		case gmAsk:
			gmAskJuka(config)
		case gmQuit:
			gmQuitGame()
		case gmSave, gmLoad:
			PlayActivateSound()
			gmPickOpen(items[gaOverlay.sel] == gmSave)
		case gmSleep:
			gmSleepNow()
		}
	}
}

// gmAskJuka swaps the menu for the assistant listening; B there comes back.
func gmAskJuka(config *Config) {
	PlayActivateSound()
	gaOverlay.mode = gaModeAsk
	gaOverlay.fromMenu = true
	gaListen(config)
}

// gmSleepNow gives the game back and puts the handheld to sleep: a stopped
// game would sit out the wake-up stopped, and the overlay's three-minute cap
// would end it the moment the handheld woke anyway.
func gmSleepNow() {
	PlayActivateSound()
	gaClose("the player put the handheld to sleep", true)
	sleepNow("game-menu")
}

// gmQuitGame asks the paused game to quit and hands it the screen for its way
// out: the interrupt is queued while it is stopped, the continue (gaClose →
// gaResume) delivers it.
func gmQuitGame() {
	gmQuitting.Store(true)
	cmds := liveScreenChildren()
	for _, cmd := range cmds {
		// The player left: not a game that failed to start (gamelaunch_watch.go).
		gameLaunchNoteStop(cmd)
		gmInterruptGame(cmd)
	}
	PlayActivateSound()
	gaClose("the player quit to JukaHub", true)
	// Children the overlay did not pause (none, normally) are continued too,
	// so every one acts on the interrupt.
	for _, cmd := range cmds {
		_ = alarmSignalChild(cmd, false)
	}
	go gmQuitWatch(cmds)
}

// gmQuitWatch kills what is left of the game after gmQuitGrace. An alarm that
// rings meanwhile pauses the game itself (alarmvol.go): the clock waits for
// it, so a game stopped by the ring is never killed mid-save.
func gmQuitWatch(cmds []*exec.Cmd) {
	defer gmQuitting.Store(false)
	defer func() {
		if r := recover(); r != nil {
			logWarn("gamemenu", "watching the game quit failed: %v", r)
		}
	}()
	deadline := time.Now().Add(gmQuitGrace)
	for time.Now().Before(deadline) {
		if !gmAnyLive(cmds) {
			return
		}
		if alarmFiringActive() || powerSleepActive() {
			// A sleep stops the game too (powersleep.go): no kill mid-save.
			deadline = time.Now().Add(gmQuitGrace)
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, cmd := range cmds {
		if gmAnyLive([]*exec.Cmd{cmd}) && cmd.Process != nil {
			logInfo("gamemenu", "the game (pid %d) did not quit within %s; killing its group", cmd.Process.Pid, gmQuitGrace)
			_ = screenChildKill(cmd)
		}
	}
}

// gmAnyLive reports whether any of cmds is still a live screen child.
func gmAnyLive(cmds []*exec.Cmd) bool {
	live := liveScreenChildren()
	for _, cmd := range cmds {
		if gaCmdIn(live, cmd) {
			return true
		}
	}
	return false
}

// gmController maps a Bluetooth pad's D-pad onto the menu (gameAssistController
// has A, B and the guide button).
func gmController(button uint8) (gaButton, bool) {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return gaBtnUp, true
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return gaBtnDown, true
	}
	return 0, false
}

// ── Drawing ──────────────────────────────────────────────────────────────

// gmDraw is the menu card in the middle of the screen: the game paused and
// which game it is, the rows, and the buttons that work it.
func gmDraw(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	if gmPick.open {
		gmPickDraw(renderer, fonts)
		return
	}
	items := gmItems(config)
	if gaOverlay.sel >= len(items) {
		gaOverlay.sel = len(items) - 1
	}
	pad := orbPad
	rowGap := int32(6)
	kicker := "GAME PAUSED"
	title := gaOverlay.title
	if title == "" {
		title = "Your game"
	}
	hint := "D-pad move · " + orbKeyName("A") + " select · " + orbKeyName("B") + " resume"

	w := screenWidth * 42 / 100
	if minW := scaleX(420, screenWidth); w < minW {
		w = minW
	}
	if w > screenWidth-4*pad {
		w = screenWidth - 4*pad
	}
	inner := w - 2*pad
	title = ellipsize(fonts.body, title, inner)

	rowH := func(it gmItem) int32 {
		_, sub := gmLabel(it)
		h := fonts.bodyH + 2*pad
		if sub != "" {
			h += 2 + fonts.smallH
		}
		return h
	}
	h := pad + fonts.tinyH + 2 + fonts.bodyH + pad
	for _, it := range items {
		h += rowH(it) + rowGap
	}
	h += pad/2 + fonts.smallH + pad
	if gaOverlay.system != "" {
		h += 2 + fonts.smallH
	}

	card := sdl.Rect{X: (screenWidth - w) / 2, Y: (screenHeight - h) / 2, W: w, H: h}
	fill := glassSheetFill()
	fill.A = 240
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, fill)
	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, orbBubbleRadius, 1, glassSheetEdge())

	x, y := card.X+pad, card.Y+pad
	drawText(renderer, fonts.tiny, kicker, x, y, glassAccent(), textAlignLeft)
	y += fonts.tinyH + 2
	drawText(renderer, fonts.body, title, x, y, ColorTextPrimary(), textAlignLeft)
	y += fonts.bodyH
	if gaOverlay.system != "" {
		drawText(renderer, fonts.small, ellipsize(fonts.small, gaOverlay.system, inner), x, y+2, ColorTextSecondary(), textAlignLeft)
		y += 2 + fonts.smallH
	}
	y += pad

	accent := glassAccent()
	for i, it := range items {
		label, sub := gmLabel(it)
		rh := rowH(it)
		row := sdl.Rect{X: card.X + pad/2, Y: y, W: card.W - pad, H: rh}
		if i == gaOverlay.sel {
			f := accent
			f.A = 56
			fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, orbBubbleRadius-4, f)
			strokeRoundedRect(renderer, row.X, row.Y, row.W, row.H, orbBubbleRadius-4, 2, accent)
		}
		ty := row.Y + pad
		drawText(renderer, fonts.body, ellipsize(fonts.body, label, inner-pad), row.X+pad, ty, ColorTextPrimary(), textAlignLeft)
		if sub != "" {
			drawText(renderer, fonts.small, ellipsize(fonts.small, sub, inner-pad), row.X+pad, ty+fonts.bodyH+2, ColorTextSecondary(), textAlignLeft)
		}
		y += rh + rowGap
	}
	y += pad / 2
	drawText(renderer, fonts.small, ellipsize(fonts.small, hint, inner), card.X+card.W/2, y, ColorTextSecondary(), textAlignCenter)
}
