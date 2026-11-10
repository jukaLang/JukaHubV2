package main

// jland_input.go — the JukaLand session: one world shared by the JukaLand
// screen and the browser's offline page, the input that drives it and the
// saving that keeps it.
//
// Held input (walking, a held mine or build button, a held mouse button) is
// polled once per frame, because the event loop only forwards presses to a
// screen: the first JukaLand latched a D-pad press as "keep walking" and never
// saw the release, so the player walked off forever. Presses (jump, a single
// mine or build, a hotbar step, aim, back) still arrive as events through
// jlandHandlePad / jlandHandleKey, which every input path calls.
//
// The face buttons are polled through the same A/B swap and accessibility
// remap the events go through, so "hold Y" means the button the player sees as
// Y on a TrimUI too.

import (
	"math"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// jlandRuntime is the live session: the world, the camera, input state between
// frames and the render thread's scratch space. Only the main thread touches
// it.
type jlandRuntime struct {
	game *jlandGame

	// The camera's top-left in tiles, and the view it was last fitted to.
	camX     float64
	camY     float64
	camReady bool
	view     sdl.Rect
	tile     int32

	lastTick uint64
	lastSave uint64

	// A jump press waiting for the next tick, and the hold-to-repeat clocks.
	jumpPressed bool
	mineNext    uint64
	buildNext   uint64

	// Pointer (mouse or finger) state between frames.
	ptrX        int32
	ptrY        int32
	ptrSeen     bool
	ptrMovedAt  uint64
	ptrLeft     bool
	ptrRight    bool
	ptrCaptured bool
	buildMode   bool
	walking     bool
	walkTargetX float64
	walkStuck   float64
	walkLastX   float64
	noPointer   sdl.Rect

	// Right-stick aim, accumulated between whole-cell steps.
	stickX float64
	stickY float64

	// keyboard is true when the last input came from the keyboard, so the
	// hints name keys instead of pad buttons.
	keyboard bool

	// The JukaLand screen's pause sheet.
	paused     bool
	pauseFocus int
	pauseRects [3]sdl.Rect
	newArmedAt uint64

	invalidUntil uint64
	slotShownAt  uint64

	// Render-thread scratch, reused every frame.
	lights     []jlandLight
	parts      [96]jlandParticle
	partNext   int
	burstSeed  int
	hotbar     sdl.Rect
	hotbarSlot int32
	hotbarGap  int32
	oreCounts  [4]int
	oreLabels  [4]string
	toolMine   sdl.Rect
	toolBuild  sdl.Rect
}

var jlandRT jlandRuntime

// jlandOutcome is what a press asks of the screen that forwarded it.
type jlandOutcome int

const (
	jlandIgnored jlandOutcome = iota
	jlandHandled
	jlandWantsBack
	jlandWantsPause
)

// jlandSession returns the world, loading the saved one (or making a new one)
// the first time.
func jlandSession() *jlandGame {
	if jlandRT.game != nil {
		return jlandRT.game
	}
	var g *jlandGame
	if userConfigCache != nil {
		g = jlandFromState(userConfigCache.JukaLand)
	}
	if g == nil {
		g = newJlandGame(time.Now().UnixNano())
	}
	jlandRT.game = g
	jlandRT.camReady = false
	return g
}

// jlandPersist hands the world to the debounced jukauser.json writer when it
// changed. The save carries the live settings (userConfigFromVariables), not
// the copy loaded at boot: the first JukaLand saved userConfigCache and put
// back every setting changed since the app started.
func jlandPersist(config *Config) {
	g := jlandRT.game
	if g == nil || !g.dirty {
		return
	}
	snap := g.snapshot()
	if snap == nil {
		return
	}
	g.dirty = false
	jlandRT.lastSave = sdl.GetTicks64()
	if userConfigCache != nil {
		userConfigCache.JukaLand = snap
	}
	if config == nil {
		config = appConfig
	}
	if user := userConfigFromVariables(config); user != nil {
		user.JukaLand = snap
		saveUserConfigDebounced(user)
	}
}

// jlandNewWorld replaces the world with a fresh one and saves it.
func jlandNewWorld(config *Config) {
	jlandRT.game = newJlandGame(time.Now().UnixNano())
	jlandRT.camReady = false
	jlandRT.parts = [96]jlandParticle{}
	jlandRT.walking = false
	jlandPersist(config)
}

// pointerActive reports whether a mouse or a finger was used recently, which
// is when the Mine / Build switch is worth its space.
func (rt *jlandRuntime) pointerActive(now uint64) bool {
	return rt.ptrMovedAt != 0 && now >= rt.ptrMovedAt && now-rt.ptrMovedAt < 8000
}

// jlandInputBlocked reports whether something above the game owns the input:
// a menu, a panel, a keyboard or a scene fade. Polled input stops then, so the
// player does not walk while the D-pad is driving the Start menu.
func jlandInputBlocked() bool {
	return virtualKeyboardActive || tb.startOpen || nc.open || qs.open || globalSearchOpen ||
		controlsLegend.open || shortcutsOpen || clipSearchOpen || homeCalendarOpen || weatherPopoverOpen ||
		imageViewerPath != "" || transitionPhase != "none" || modalPanelBlocksShoulders() ||
		browserLinkPanel.open || browserCookiePanel.open || browserBookmarkPanel.open || brwInfo.open
}

// jlandKeysCaptured reports whether JukaLand owns the keyboard and the touch
// screen, so the desktop's Q / E scene-switch keys (E builds in JukaLand) and
// the scene-switch swipes (core.go handleSwipe) must stay quiet.
func jlandKeysCaptured(config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return false
	}
	return config.Scenes[currentSceneIndex].Name == "JukaLand" || brwGameActive()
}

// jlandPadHeld is the set of logical buttons held on the pad, after the same
// A/B and X/Y swaps (padFaceButton) and remap the press events go through.
func jlandPadHeld(config *Config, pad *sdl.GameController) [32]bool {
	var held [32]bool
	if pad == nil {
		return held
	}
	handheld := P().IsHandheld()
	for phys := 0; phys < len(held) && phys < int(sdl.CONTROLLER_BUTTON_MAX); phys++ {
		if pad.Button(sdl.GameControllerButton(phys)) == 0 {
			continue
		}
		b := padFaceButton(config, normalizeHandheldBackButton(uint8(phys), handheld))
		if logical := a11yRemapButton(b); int(logical) < len(held) {
			held[logical] = true
		}
	}
	return held
}

// jlandPoll reads the held keys and pad: walking, a held jump (to swim) and a
// held mine or build.
func jlandPoll(config *Config, g *jlandGame, dt float64) (c jlandControls, mine, build bool) {
	rt := &jlandRT
	keys := sdl.GetKeyboardState()
	if len(keys) > 90 {
		if keys[sdl.SCANCODE_A] != 0 || keys[sdl.SCANCODE_LEFT] != 0 {
			c.MoveX--
		}
		if keys[sdl.SCANCODE_D] != 0 || keys[sdl.SCANCODE_RIGHT] != 0 {
			c.MoveX++
		}
		c.JumpHeld = keys[sdl.SCANCODE_SPACE] != 0 || keys[sdl.SCANCODE_W] != 0
		mine = keys[sdl.SCANCODE_F] != 0
		build = keys[sdl.SCANCODE_E] != 0
	}
	keyboardMove := c.MoveX != 0
	pad := gameController
	if pad == nil {
		return c, mine, build
	}
	dx, _ := a11yHeldNavDirection(func(b uint8) bool {
		return pad.Button(sdl.GameControllerButton(b)) != 0
	})
	c.MoveX += float64(dx)
	if ax := float64(pad.Axis(sdl.GameControllerAxis(sdl.CONTROLLER_AXIS_LEFTX))) / 32767; math.Abs(ax) > 0.35 {
		c.MoveX += ax
	}
	if c.MoveX != 0 && !keyboardMove {
		rt.keyboard = false
		// Walking with the pad hands aiming back to the dial unless the mouse
		// is still being used.
		if g.AimMode == jlandAimPointer && sdl.GetTicks64()-rt.ptrMovedAt > 1500 {
			g.AimMode = jlandAimArc
		}
	}
	held := jlandPadHeld(config, pad)
	c.JumpHeld = c.JumpHeld || held[sdl.CONTROLLER_BUTTON_A]
	mine = mine || held[sdl.CONTROLLER_BUTTON_Y]
	build = build || held[sdl.CONTROLLER_BUTTON_X]

	// The right stick moves a free cursor around the player.
	rx := float64(pad.Axis(sdl.GameControllerAxis(sdl.CONTROLLER_AXIS_RIGHTX))) / 32767
	ry := float64(pad.Axis(sdl.GameControllerAxis(sdl.CONTROLLER_AXIS_RIGHTY))) / 32767
	if math.Abs(rx) < 0.35 && math.Abs(ry) < 0.35 {
		rt.stickX, rt.stickY = 0, 0
		return c, mine, build
	}
	rt.stickX += rx * dt * 9
	rt.stickY += ry * dt * 9
	for rt.stickX >= 1 {
		g.nudgeRelative(1, 0)
		rt.stickX--
	}
	for rt.stickX <= -1 {
		g.nudgeRelative(-1, 0)
		rt.stickX++
	}
	for rt.stickY >= 1 {
		g.nudgeRelative(0, 1)
		rt.stickY--
	}
	for rt.stickY <= -1 {
		g.nudgeRelative(0, -1)
		rt.stickY++
	}
	return c, mine, build
}

// jlandPointer follows the mouse (or a finger): hovering aims at the cell under
// it, a left press mines (builds while the Build switch is on), a right press
// builds, a press out of reach walks the player there, and the hotbar and the
// switch take their own taps.
func jlandPointer(config *Config, g *jlandGame, view sdl.Rect, now uint64, mine, build *bool) {
	rt := &jlandRT
	px, py, state := sdl.GetMouseState()
	x, y := physicalToLogical(px, py)
	left := state&sdl.ButtonLMask() != 0
	right := state&sdl.ButtonRMask() != 0
	first := !rt.ptrSeen
	moved := x != rt.ptrX || y != rt.ptrY
	pressedL := left && !rt.ptrLeft
	pressedR := right && !rt.ptrRight
	rt.ptrX, rt.ptrY, rt.ptrLeft, rt.ptrRight, rt.ptrSeen = x, y, left, right, true
	if first {
		return
	}
	inside := pointInRect(x, y, view) && !(rt.noPointer.W > 0 && pointInRect(x, y, rt.noPointer))
	if !left && !right {
		rt.ptrCaptured = false
	}
	if pressedL || pressedR {
		rt.ptrCaptured = inside
	}
	if !inside && !rt.ptrCaptured {
		return
	}
	if moved || pressedL || pressedR {
		rt.ptrMovedAt = now
		rt.keyboard = true
	}

	// The HUD answers its own taps.
	if pressedL {
		switch {
		case rt.toolMine.W > 0 && pointInRect(x, y, rt.toolMine):
			rt.buildMode = false
			rt.ptrCaptured = false
			PlayNavSound()
			return
		case rt.toolBuild.W > 0 && pointInRect(x, y, rt.toolBuild):
			rt.buildMode = true
			rt.ptrCaptured = false
			PlayNavSound()
			return
		case rt.hotbar.W > 0 && pointInRect(x, y, rt.hotbar):
			for i := range jlandHotbar {
				if pointInRect(x, y, jlandSlotRect(rt.hotbar, rt.hotbarSlot, rt.hotbarGap, i)) {
					jlandSelectSlot(i)
				}
			}
			rt.ptrCaptured = false
			return
		}
	}

	vp := jlandViewportFor(view)
	tx := int(jlandFloorDiv(x-view.X+vp.camPX, vp.tile))
	ty := int(jlandFloorDiv(y-view.Y+vp.camPY, vp.tile))
	if moved || pressedL || pressedR || g.AimMode == jlandAimPointer {
		g.pointAt(tx, ty)
	}
	if pressedL || pressedR {
		buildPress := pressedR || rt.buildMode
		switch {
		case g.inReach(tx, ty) && buildPress:
			jlandBuild(false)
			rt.buildNext = now + 260
		case g.inReach(tx, ty):
			jlandMine(false)
			rt.mineNext = now + 260
		case pressedL:
			// Out of reach: walk there, jumping the steps on the way.
			rt.walking = true
			rt.walkTargetX = float64(tx) + 0.5
			rt.walkStuck = 0
			rt.walkLastX = g.P.X
		}
	}
	if rt.ptrCaptured {
		if right || (left && rt.buildMode) {
			*build = true
		} else if left {
			*mine = true
		}
	}
}

// jlandTap is a click or a tap that arrived as an event. A finger never shows
// in the polled mouse state (the app replays a tap as a click, see
// pushTapClick), and a click shorter than a frame can slip between two polls;
// a press the poll is already following is left to it. It reports whether the
// point was the game's.
func jlandTap(view sdl.Rect, mx, my int32) bool {
	rt := &jlandRT
	if !pointInRect(mx, my, view) || (rt.noPointer.W > 0 && pointInRect(mx, my, rt.noPointer)) {
		return false
	}
	if _, _, state := sdl.GetMouseState(); state&sdl.ButtonLMask() != 0 {
		return true
	}
	g := jlandSession()
	rt.ptrMovedAt = sdl.GetTicks64()
	rt.keyboard = true
	switch {
	case rt.toolMine.W > 0 && pointInRect(mx, my, rt.toolMine):
		rt.buildMode = false
		PlayNavSound()
		return true
	case rt.toolBuild.W > 0 && pointInRect(mx, my, rt.toolBuild):
		rt.buildMode = true
		PlayNavSound()
		return true
	case rt.hotbar.W > 0 && pointInRect(mx, my, rt.hotbar):
		for i := range jlandHotbar {
			if pointInRect(mx, my, jlandSlotRect(rt.hotbar, rt.hotbarSlot, rt.hotbarGap, i)) {
				jlandSelectSlot(i)
			}
		}
		return true
	}
	vp := jlandViewportFor(view)
	tx := int(jlandFloorDiv(mx-view.X+vp.camPX, vp.tile))
	ty := int(jlandFloorDiv(my-view.Y+vp.camPY, vp.tile))
	g.pointAt(tx, ty)
	switch {
	case g.inReach(tx, ty) && rt.buildMode:
		jlandBuild(false)
	case g.inReach(tx, ty):
		jlandMine(false)
	default:
		rt.walking = true
		rt.walkTargetX = float64(tx) + 0.5
		rt.walkStuck = 0
		rt.walkLastX = g.P.X
	}
	return true
}

// jlandSceneClick takes a click or a tap on the JukaLand screen: a pause-sheet
// row while paused, the world otherwise. The header (its Back pill) is handled
// before this runs, and the footer band is not part of the world.
func jlandSceneClick(mx, my int32, config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) ||
		config.Scenes[currentSceneIndex].Name != "JukaLand" {
		return false
	}
	if jlandRT.paused {
		for i, rect := range jlandRT.pauseRects {
			if rect.W > 0 && pointInRect(mx, my, rect) {
				jlandRT.pauseFocus = i
				jlandPauseActivate(config, i)
				break
			}
		}
		return true
	}
	jlandTap(jlandSceneView(), mx, my)
	return true
}

// jlandSyncPointer records where the pointer is and which buttons are down
// without acting on them.
func jlandSyncPointer() {
	px, py, state := sdl.GetMouseState()
	x, y := physicalToLogical(px, py)
	rt := &jlandRT
	rt.ptrX, rt.ptrY, rt.ptrSeen = x, y, true
	rt.ptrLeft = state&sdl.ButtonLMask() != 0
	rt.ptrRight = state&sdl.ButtonRMask() != 0
	rt.ptrCaptured = false
}

// jlandTick advances the session one frame: input, physics, camera, chips and
// autosave. paused freezes the world but keeps the input state current, so a
// button held across a pause does not fire as a fresh press afterwards.
func jlandTick(config *Config, view sdl.Rect, paused bool) {
	g := jlandSession()
	rt := &jlandRT
	now := sdl.GetTicks64()
	dt := 1.0 / 30
	// The first frame after the game was off screen: the click or tap that
	// opened it may still be held, and must not also dig a hole.
	fresh := rt.lastTick == 0 || now-rt.lastTick > 250
	if !fresh && now > rt.lastTick {
		dt = float64(now-rt.lastTick) / 1000
	}
	rt.lastTick = now
	rt.view = view
	rt.tile = jlandTileSize(view)

	var c jlandControls
	var mine, build bool
	switch {
	case paused || jlandInputBlocked():
		// Keep the pointer's button state so a release during the pause is
		// not read as a press when it ends.
		jlandSyncPointer()
		rt.jumpPressed = false
	case fresh:
		jlandSyncPointer()
		c, _, _ = jlandPoll(config, g, dt)
	default:
		c, mine, build = jlandPoll(config, g, dt)
		jlandPointer(config, g, view, now, &mine, &build)
	}
	if rt.jumpPressed {
		c.Jump = true
		rt.jumpPressed = false
	}

	if rt.walking {
		switch diff := rt.walkTargetX - g.P.X; {
		case c.MoveX != 0, paused:
			rt.walking = false
		case math.Abs(diff) < 0.2:
			rt.walking = false
		default:
			c.MoveX = math.Copysign(1, diff)
			if math.Abs(g.P.X-rt.walkLastX) < 0.01 {
				rt.walkStuck += dt
			} else {
				rt.walkStuck = 0
			}
			rt.walkLastX = g.P.X
			if rt.walkStuck > 0.12 && g.P.OnGround {
				c.Jump = true
			}
			if rt.walkStuck > 1.6 {
				rt.walking = false
			}
		}
	}

	if !paused {
		g.advance(dt, c)
		jlandStepParticles(dt)
		if mine {
			if rt.mineNext == 0 || now >= rt.mineNext {
				jlandMine(rt.mineNext != 0)
				rt.mineNext = now + 170
			}
		} else {
			rt.mineNext = 0
		}
		if build {
			if rt.buildNext == 0 || now >= rt.buildNext {
				jlandBuild(rt.buildNext != 0)
				rt.buildNext = now + 170
			}
		} else {
			rt.buildNext = 0
		}
	}

	// The camera eases toward the player and stops at the world's edges.
	t := float64(rt.tile)
	viewW, viewH := float64(view.W)/t, float64(view.H)/t
	w := g.World
	cx := g.P.X - viewW/2
	cy := g.P.Y - jlandPlayerH/2 - viewH/2
	if float64(w.W) <= viewW {
		cx = (float64(w.W) - viewW) / 2
	} else {
		cx = math.Max(0, math.Min(cx, float64(w.W)-viewW))
	}
	if float64(w.H)+3 <= viewH {
		cy = (float64(w.H) - viewH) / 2
	} else {
		cy = math.Max(-3, math.Min(cy, float64(w.H)-viewH))
	}
	if !rt.camReady {
		rt.camX, rt.camY, rt.camReady = cx, cy, true
	} else if !paused {
		k := 1 - math.Exp(-dt*9)
		rt.camX += (cx - rt.camX) * k
		rt.camY += (cy - rt.camY) * k
	}

	if g.dirty && now-rt.lastSave > 5000 {
		jlandPersist(config)
	}
}

// jlandMine breaks the aimed cell. A repeat (a held button) stays silent when
// it fails, so holding against bedrock is not a buzz.
func jlandMine(repeat bool) {
	g := jlandRT.game
	if g == nil {
		return
	}
	tx, ty := g.target()
	was, res := g.breakAt(tx, ty)
	if res == jlandOK {
		jlandBurst(tx, ty, was)
		PlayNavSound()
		return
	}
	jlandRT.invalidUntil = sdl.GetTicks64() + 250
	if !repeat {
		PlayBackSound()
	}
}

// jlandBuild places the selected block on the aimed cell.
func jlandBuild(repeat bool) {
	g := jlandRT.game
	if g == nil {
		return
	}
	tx, ty := g.target()
	if g.placeAt(tx, ty) == jlandOK {
		PlayActivateSound()
		return
	}
	jlandRT.invalidUntil = sdl.GetTicks64() + 250
	if !repeat {
		PlayBackSound()
	}
}

// jlandSelectSlot picks a hotbar slot and shows the block's name.
func jlandSelectSlot(i int) {
	g := jlandSession()
	if i < 0 || i >= len(jlandHotbar) || i == g.Slot {
		jlandRT.slotShownAt = sdl.GetTicks64()
		return
	}
	g.Slot = i
	g.dirty = true
	jlandRT.slotShownAt = sdl.GetTicks64()
	PlayNavSound()
}

// jlandStepSlot steps the hotbar.
func jlandStepSlot(delta int) {
	g := jlandSession()
	g.cycleSlot(delta)
	g.dirty = true
	jlandRT.slotShownAt = sdl.GetTicks64()
	PlayNavSound()
}

// jlandTurn faces the player one way (walking is polled; a tap only turns).
func jlandTurn(dir int) {
	g := jlandSession()
	g.P.Facing = dir
	jlandRT.walking = false
}

// jlandHandlePad answers a pad press. B (and Select) and Start are left to the
// screen that forwarded the press: the JukaLand screen leaves or pauses, the
// browser goes back to its offline card.
func jlandHandlePad(config *Config, e *sdl.ControllerButtonEvent) jlandOutcome {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return jlandIgnored
	}
	g := jlandSession()
	rt := &jlandRT
	rt.keyboard = false
	now := sdl.GetTicks64()
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		return jlandWantsBack
	case sdl.CONTROLLER_BUTTON_START:
		return jlandWantsPause
	case sdl.CONTROLLER_BUTTON_A:
		rt.jumpPressed = true
	case sdl.CONTROLLER_BUTTON_Y:
		jlandMine(false)
		rt.mineNext = now + 300
	case sdl.CONTROLLER_BUTTON_X:
		jlandBuild(false)
		rt.buildNext = now + 300
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		jlandStepSlot(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		jlandStepSlot(1)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		g.stepAim(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		g.stepAim(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		jlandTurn(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		jlandTurn(1)
	case sdl.CONTROLLER_BUTTON_LEFTSTICK:
		g.AimMode = jlandAimArc
		g.Aim = jlandArcDefault
	}
	return jlandHandled
}

// jlandHandleKey answers a key press. Esc / Backspace / B ask to leave and P
// asks to pause, as on the pad.
func jlandHandleKey(config *Config, e *sdl.KeyboardEvent) jlandOutcome {
	if e == nil || e.Type != sdl.KEYDOWN {
		return jlandIgnored
	}
	g := jlandSession()
	rt := &jlandRT
	rt.keyboard = true
	now := sdl.GetTicks64()
	sym := e.Keysym.Sym
	switch {
	case sym == sdl.K_ESCAPE || sym == sdl.K_BACKSPACE || sym == sdl.K_b:
		return jlandWantsBack
	case sym == sdl.K_p:
		return jlandWantsPause
	case sym == sdl.K_SPACE || sym == sdl.K_w:
		if e.Repeat == 0 {
			rt.jumpPressed = true
		}
	case sym == sdl.K_UP:
		g.stepAim(-1)
	case sym == sdl.K_DOWN:
		g.stepAim(1)
	case sym == sdl.K_LEFT || sym == sdl.K_a:
		jlandTurn(-1)
	case sym == sdl.K_RIGHT || sym == sdl.K_d:
		jlandTurn(1)
	case sym == sdl.K_f:
		if e.Repeat == 0 {
			jlandMine(false)
			rt.mineNext = now + 300
		}
	case sym == sdl.K_e:
		if e.Repeat == 0 {
			jlandBuild(false)
			rt.buildNext = now + 300
		}
	case sym == sdl.K_z || sym == sdl.K_LEFTBRACKET:
		jlandStepSlot(-1)
	case sym == sdl.K_c || sym == sdl.K_RIGHTBRACKET:
		jlandStepSlot(1)
	case sym == sdl.K_TAB:
		rt.buildMode = !rt.buildMode
		rt.ptrMovedAt = now
		PlayNavSound()
	case sym == sdl.K_0:
		jlandSelectSlot(9)
	case sym >= sdl.K_1 && sym <= sdl.K_9:
		jlandSelectSlot(int(sym - sdl.K_1))
	}
	return jlandHandled
}

// jlandPadHints / jlandKeyHints are the controls the footer names, in the
// order they are dropped from the end when the footer runs out of room.
var (
	jlandPadHints = []FooterHint{
		{Button: "D-Pad", Label: "Move"}, {Button: "A", Label: "Jump"}, {Button: "Y", Label: "Mine"},
		{Button: "X", Label: "Build"}, {Button: "L1/R1", Label: "Block"}, {Button: "B", Label: "Back"},
		{Button: "Up/Down", Label: "Aim"}, {Button: "Start", Label: "Pause"},
	}
	jlandKeyHints = []FooterHint{
		{Button: "A/D", Label: "Move"}, {Button: "Space", Label: "Jump"}, {Button: "F", Label: "Mine"},
		{Button: "E", Label: "Build"}, {Button: "1-0", Label: "Block"}, {Button: "Esc", Label: "Back"},
		{Button: "Up/Down", Label: "Aim"}, {Button: "P", Label: "Pause"},
	}
	jlandPauseHints = []FooterHint{
		{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Select"}, {Button: "B", Label: "Resume"},
	}
)

// jlandSceneHints are the JukaLand screen's footer hints for the input in use,
// as a copy the footer may trim.
func jlandSceneHints() []FooterHint {
	hints := jlandPadHints
	switch {
	case jlandRT.paused:
		hints = jlandPauseHints
	case jlandRT.keyboard:
		hints = jlandKeyHints
	}
	return append([]FooterHint(nil), hints...)
}
