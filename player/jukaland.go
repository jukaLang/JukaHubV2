package main

// jukaland.go — the JukaLand screen.
//
// JukaLand is drawn under the shared header with the shared footer band
// floating over its bottom edge; the world, its rules and its input live in
// jland_world.go, jland_render.go and jland_input.go, which the browser's
// offline page uses too. B (or Esc) leaves like it does on every other screen,
// Start (or P) opens a small pause sheet with Resume, New world and Leave.
//
// The world is saved while you play (every few seconds after a change), when
// you leave the screen and when the app shuts down, through the same debounced
// jukauser.json writer the rest of the app uses.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// jukalandInited and initJukaLand answer the "jukaland_init" trigger older
// configs still carry on an Exit Game button: the world loads itself on the
// first frame, so the trigger only makes sure it is there.
var jukalandInited bool

func initJukaLand() {
	jlandSession()
	jukalandInited = true
}

// jlandPauseItems are the pause sheet's rows.
var jlandPauseItems = [...]string{"Resume", "New world", "Leave JukaLand"}

// jlandSceneView is the game's rectangle on the JukaLand screen: everything
// under the header.
func jlandSceneView() sdl.Rect {
	top := headerBarHeight()
	return sdl.Rect{X: 0, Y: top, W: screenWidth, H: screenHeight - top}
}

type jukaScene struct{ sceneBase }

func (jukaScene) render(r *sdl.Renderer, c *Config) bool {
	view := jlandSceneView()
	band := sceneFooterBand()
	// The footer band floats over the world's bottom edge; a click on it is
	// not a click on the ground under it.
	jlandRT.noPointer = band
	jlandTick(c, view, jlandRT.paused)
	jlandDraw(r, c, view, band.Y-12)
	renderStatusBar(r, c)
	renderFooter(r, c)
	if jlandRT.paused {
		jlandDrawPause(r, c)
	}
	return true
}

func (jukaScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return true
	}
	if jlandRT.paused {
		jlandPauseKey(e, c)
		return true
	}
	switch jlandHandleKey(c, e) {
	case jlandWantsBack:
		jlandLeave(c)
	case jlandWantsPause:
		jlandSetPaused(true)
	}
	return true
}

func (jukaScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	if jlandRT.paused {
		jlandPausePad(e, c)
		return true
	}
	switch jlandHandlePad(c, e) {
	case jlandWantsBack:
		jlandLeave(c)
	case jlandWantsPause:
		jlandSetPaused(true)
	}
	return true
}

func init() { registerScene(jukaScene{sceneBase{name: "JukaLand"}}) }

// jlandLeave saves and goes back to the screen JukaLand was opened from
// (goBackScene plays the back sound).
func jlandLeave(config *Config) {
	jlandRT.paused = false
	jlandPersist(config)
	goBackScene(config)
}

// jlandSceneDeparture runs when any scene is left (core.go's transition): the
// world is saved however the JukaLand screen was left — B, the header's Back
// pill, the Start menu, a notification.
func jlandSceneDeparture(config *Config, leaving string) {
	if leaving == "JukaLand" {
		jlandRT.paused = false
		jlandPersist(config)
	}
}

func jlandSetPaused(on bool) {
	jlandRT.paused = on
	jlandRT.pauseFocus = 0
	jlandRT.newArmedAt = 0
	if on {
		PlayActivateSound()
	} else {
		PlayBackSound()
	}
}

// jlandPauseActivate runs a pause-sheet row. New world asks twice: the world is
// the player's work.
func jlandPauseActivate(config *Config, i int) {
	switch i {
	case 0:
		jlandSetPaused(false)
	case 1:
		now := sdl.GetTicks64()
		if jlandRT.newArmedAt == 0 || now-jlandRT.newArmedAt > 4000 {
			jlandRT.newArmedAt = now
			PlayNavSound()
			return
		}
		jlandNewWorld(config)
		jlandSetPaused(false)
		ShowToast("A new world is ready", ToastKindSuccess)
	case 2:
		jlandLeave(config)
	}
}

func jlandPauseMove(delta int) {
	n := len(jlandPauseItems)
	jlandRT.pauseFocus = ((jlandRT.pauseFocus+delta)%n + n) % n
	jlandRT.newArmedAt = 0
	PlayNavSound()
}

func jlandPausePad(e *sdl.ControllerButtonEvent, config *Config) {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		jlandPauseMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		jlandPauseMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		jlandPauseActivate(config, jlandRT.pauseFocus)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_START:
		jlandSetPaused(false)
	}
}

func jlandPauseKey(e *sdl.KeyboardEvent, config *Config) {
	switch e.Keysym.Sym {
	case sdl.K_UP:
		jlandPauseMove(-1)
	case sdl.K_DOWN:
		jlandPauseMove(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		jlandPauseActivate(config, jlandRT.pauseFocus)
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_p, sdl.K_b:
		jlandSetPaused(false)
	}
}

// jlandDrawPause draws the pause sheet: the shared overlay card with three glass
// rows and a line naming what the world does while you are away.
func jlandDrawPause(r *sdl.Renderer, config *Config) {
	if r == nil || config == nil {
		return
	}
	font := FontForRole(config, TypeBody)
	hintFont, _ := getCachedFont(config, "small")
	if font == nil || hintFont == nil {
		return
	}
	header := UIPanelHeader("Paused", "Your world is saved as you play", "gamepad")
	headerH := UIPanelHeaderBand(config, header, 14)
	rowH := int32(56)
	gap := int32(12)
	hintH := int32(hintFont.Height()) + 20
	cardW := min(int32(560), screenWidth-48)
	cardH := headerH + int32(len(jlandPauseItems))*rowH + int32(len(jlandPauseItems)-1)*gap + hintH + 8
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}

	drawOverlayScrim(r)
	drawOverlayCard(r, card)
	drawOverlayCardAccent(r, card.X, card.Y, card.W)
	UIPageHeaderDraw(r, config, overlayHeaderOnCard(header, card))

	y := card.Y + headerH
	armed := jlandRT.newArmedAt != 0 && sdl.GetTicks64()-jlandRT.newArmedAt <= 4000
	for i, label := range jlandPauseItems {
		rect := sdl.Rect{X: card.X + OverlayPadX, Y: y, W: card.W - 2*OverlayPadX, H: rowH}
		text := label
		tint := sdl.Color{}
		if i == 1 && armed {
			text = "Press again to replace this world"
			tint = glassButtonTint(ColorDanger)
		}
		drawGlassButton(r, config, rect, text, font, tint, glassState{Focused: i == jlandRT.pauseFocus})
		jlandRT.pauseRects[i] = rect
		y += rowH + gap
	}
	hint := hintLine(FooterHint{Button: "A", Label: "Select"}, FooterHint{Button: "B / Start", Label: "Resume"})
	hw, _ := measureText(hintFont, hint)
	renderText(r, config, hintFont, hint, ColorTextSecondary(), card.X+(card.W-hw)/2, card.Y+card.H-hintH+6)
}
