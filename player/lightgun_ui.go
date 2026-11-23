package main

// lightgun_ui.go — where a game's light gun is turned on, tuned and tried: a
// screen over the game page (its More sheet's "Light gun" row), a chip on the
// page, and a practice target.
//
// The screen is a short list: the gun on or off for this game (the Light gun
// control layout, ctrllayout.go), how it aims (Move: the stick steers; Point:
// the stick's position is the spot), Move's speed or Point's aim area, which
// shoulder fires and which reloads, a buzz on each shot, the practice target,
// and whether known gun games get the gun by themselves. Left / Right change
// the focused row, A toggles it (or opens the practice), Y puts the game's
// settings back to their defaults, B goes back; the settings are saved when it
// closes. Under the list: what the focused row does, and how this game's core
// reads a gun (or that JukaHub does not know it, or that the emulator is a
// standalone one), and what the last launch could not set up.
//
// The practice target is drawn inside JukaHub, which has the screen there, so
// it reads the pad through SDL with the same aim maths the game's mouse uses
// (lightgun_aim.go): a target drifts across a picture the screen's shape, the
// trigger and reload buttons are the game's, six shots to a magazine. The
// right stick's pointer stays away meanwhile (vcursorScreenBlocks). A
// keyboard aims with the arrows, fires with Space and reloads with R; a click
// shoots where it lands.

import (
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The rows of the screen.
const (
	lgRowUse = iota
	lgRowAim
	lgRowSpeed
	lgRowArea
	lgRowAreaW
	lgRowAreaH
	lgRowAreaX
	lgRowAreaY
	lgRowTrigger
	lgRowReload
	lgRowRecoil
	lgRowTry
	lgRowAuto
)

// lightgunMagazine is the practice target's shots before a reload.
const lightgunMagazine = 6

// lightgunUI is the open screen. Main thread only.
var lightgunUI struct {
	open   bool
	game   libraryGame
	set    lightgunSettings
	dirty  bool
	rows   []int
	cursor int
	first  int
	hits   []gameLibRowHit
	card   sdl.Rect
	gen    int
	// What the game starts in, from a goroutine: answered is false until
	// then, found false when no emulator was found.
	answered  bool
	found     bool
	retroArch bool
	core      string
	// kind is the gun the game wants (lightgunGameKind), read at open.
	kind string
	pr   lightgunPractice
}

// lightgunPractice is the practice target's state.
type lightgunPractice struct {
	on         bool
	rect       sdl.Rect
	panelW     float64
	x, y       float64
	tx, ty     float64
	tvx, tvy   float64
	shots      int
	hits       int
	ammo       int
	flashUntil time.Time
	emptyUntil time.Time
	reloadAt   time.Time
	latch      [2]bool
	dpadAt     time.Time
	dpadHeld   bool
	last       time.Time
}

// lightgunPracticeOpen reports the practice target up: it owns the right
// stick (virtualcursor.go keeps its pointer away).
func lightgunPracticeOpen() bool { return lightgunUI.open && lightgunUI.pr.on }

// lightgunOffered reports whether a game can have the light gun: an emulated
// game of a console that had one.
func lightgunOffered(game libraryGame) bool {
	if !ctrlLayoutOffered(game) {
		return false
	}
	for _, s := range lightgunSystems {
		if lightgunSameSystem(s, game.System) {
			return true
		}
	}
	return false
}

// lightgunInUse reports the game played with the Light gun layout.
func lightgunInUse(game libraryGame) bool {
	l, ok := ctrlLayoutForGame(game)
	return ok && l.ID == lightgunLayoutID
}

// lightgunUIRows lists the rows for the settings as they are now.
func lightgunUIRows() []int {
	s := lightgunUI.set
	rows := []int{lgRowUse, lgRowAim}
	point := s.Aim == "point" && stickHardware()
	if point {
		rows = append(rows, lgRowArea)
		if s.Area == "custom" {
			rows = append(rows, lgRowAreaW, lgRowAreaH, lgRowAreaX, lgRowAreaY)
		}
	} else {
		rows = append(rows, lgRowSpeed)
	}
	return append(rows, lgRowTrigger, lgRowReload, lgRowRecoil, lgRowTry, lgRowAuto)
}

// lightgunUIOpen opens the screen for game.
func lightgunUIOpen(game libraryGame) {
	ui := &lightgunUI
	ui.open, ui.game = true, game
	ui.set = lightgunSettingsFor(game.Path)
	ui.dirty = false
	ui.pr = lightgunPractice{}
	ui.rows = lightgunUIRows()
	ui.cursor, ui.first = 0, 0
	ui.hits = ui.hits[:0]
	ui.answered, ui.found, ui.retroArch, ui.core = false, false, false, ""
	ui.kind = lightgunGameKind(game)
	ui.gen++
	gen := ui.gen
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("lightgun", "checking the game's emulator failed: %v", r)
			}
		}()
		retroArch, core, found := ctrlLayoutLauncherFacts(game, true)
		tbxPost(func() {
			if ui.open && ui.gen == gen {
				ui.answered, ui.found, ui.retroArch, ui.core = true, found, retroArch, core
			}
		})
	}()
}

// lightgunUIClose closes the screen, saving the settings when they changed.
func lightgunUIClose(sound bool) {
	ui := &lightgunUI
	if !ui.open {
		return
	}
	if ui.dirty {
		lightgunSaveSettings(ui.game.Path, ui.set)
		ui.dirty = false
	}
	ui.open, ui.pr.on = false, false
	if sound {
		PlayBackSound()
	}
}

// lightgunUISetUse turns the gun on or off for the game. Off leaves the
// console's own layout when it has one, or None when the console's is the
// gun. Either way the game is marked decided: no automatic gun later.
func lightgunUISetUse(on bool) {
	game := lightgunUI.game
	if on {
		if !ctrlLayoutSetGame(game.Path, lightgunLayoutID) {
			return
		}
	} else {
		if !ctrlLayoutSetGame(game.Path, "") {
			return
		}
		if lightgunInUse(game) {
			ctrlLayoutSetGame(game.Path, ctrlLayoutNone)
		}
	}
	lightgunMarkDecided(game.Path)
	PlayToggleSound()
	if on {
		ShowToast(game.Label()+": light gun on", ToastKindSuccess)
	} else {
		ShowToast(game.Label()+": light gun off", ToastKindInfo)
	}
}

// lightgunUIChange changes the focused row by dir (-1 or 1).
func lightgunUIChange(dir int) {
	ui := &lightgunUI
	if ui.cursor < 0 || ui.cursor >= len(ui.rows) {
		return
	}
	s := &ui.set
	step := func(v, by, lo, hi int) int {
		v += by * dir
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	before := *s
	switch ui.rows[ui.cursor] {
	case lgRowUse:
		lightgunUISetUse(!lightgunInUse(ui.game))
		return
	case lgRowAuto:
		if lightgunSetAutoAssign(!lightgunAutoAssignOn()) {
			PlayToggleSound()
		}
		return
	case lgRowTry:
		return
	case lgRowAim:
		if !stickHardware() {
			ShowToast("Without sticks the D-pad steers the crosshair", ToastKindInfo)
			return
		}
		if s.Aim == "point" {
			s.Aim = "move"
		} else {
			s.Aim = "point"
		}
	case lgRowSpeed:
		s.Speed = step(s.Speed, 1, lightgunSpeedMin, lightgunSpeedMax)
	case lgRowArea:
		areas := []string{"picture", "full", "custom"}
		i := 0
		for k, a := range areas {
			if a == s.Area {
				i = k
			}
		}
		s.Area = areas[(i+dir+len(areas))%len(areas)]
	case lgRowAreaW:
		s.AreaW = step(s.AreaW, 5, lightgunAreaMin, lightgunAreaMax)
	case lgRowAreaH:
		s.AreaH = step(s.AreaH, 5, lightgunAreaMin, lightgunAreaMax)
	case lgRowAreaX:
		s.AreaX = step(s.AreaX, 2, -lightgunShiftMax, lightgunShiftMax)
	case lgRowAreaY:
		s.AreaY = step(s.AreaY, 2, -lightgunShiftMax, lightgunShiftMax)
	case lgRowTrigger:
		if s.Trigger == "r2" {
			s.Trigger = "r1"
		} else {
			s.Trigger = "r2"
		}
	case lgRowReload:
		if s.Reload == "l2" {
			s.Reload = "l1"
		} else {
			s.Reload = "l2"
		}
	case lgRowRecoil:
		s.Recoil = !s.Recoil
	}
	if *s == before {
		return
	}
	ui.dirty = true
	PlayNavSound()
	focused := ui.rows[ui.cursor]
	ui.rows = lightgunUIRows()
	ui.cursor = 0
	for i, r := range ui.rows {
		if r == focused {
			ui.cursor = i
		}
	}
}

// lightgunUIAction is the list's input.
func lightgunUIAction(act tbxAction) {
	ui := &lightgunUI
	switch act {
	case tbxActUp:
		if ui.cursor > 0 {
			ui.cursor--
			PlayNavSound()
		}
	case tbxActDown:
		if ui.cursor < len(ui.rows)-1 {
			ui.cursor++
			PlayNavSound()
		}
	case tbxActPageUp, tbxActHome:
		if ui.cursor != 0 {
			ui.cursor = 0
			PlayNavSound()
		}
	case tbxActPageDown, tbxActEnd:
		if last := len(ui.rows) - 1; ui.cursor != last && last >= 0 {
			ui.cursor = last
			PlayNavSound()
		}
	case tbxActLeft:
		lightgunUIChange(-1)
	case tbxActRight:
		lightgunUIChange(1)
	case tbxActConfirm:
		if ui.cursor >= 0 && ui.cursor < len(ui.rows) && ui.rows[ui.cursor] == lgRowTry {
			lightgunPracticeStart()
			return
		}
		lightgunUIChange(1)
	case tbxActY:
		def := lightgunDefaults()
		def.Auto = ui.set.Auto
		if ui.set != def {
			ui.set, ui.dirty = def, true
			ui.rows = lightgunUIRows()
			ui.cursor = 0
			PlayActivateSound()
			ShowToast("Light gun settings back to the defaults for this game", ToastKindInfo)
		}
	case tbxActBack:
		lightgunUIClose(true)
	}
}

// lightgunUIKey and lightgunUIPad use every event while the screen is open.
func lightgunUIKey(e *sdl.KeyboardEvent) bool {
	if !lightgunUI.open {
		return false
	}
	if lightgunUI.pr.on {
		lightgunPracticeKey(e)
		return true
	}
	if e != nil && e.Type == sdl.KEYDOWN {
		lightgunUIAction(tbxKeyAction(e))
	}
	return true
}

func lightgunUIPad(e *sdl.ControllerButtonEvent) bool {
	if !lightgunUI.open {
		return false
	}
	if lightgunUI.pr.on {
		lightgunPracticePad(e)
		return true
	}
	if e != nil && e.Type == sdl.CONTROLLERBUTTONDOWN {
		lightgunUIAction(tbxPadAction(e))
	}
	return true
}

// lightgunUIMouse focuses a clicked row, activates a focused one, shoots in
// the practice, and closes on a click outside the card.
func lightgunUIMouse(mx, my int32) bool {
	ui := &lightgunUI
	if !ui.open {
		return false
	}
	if ui.pr.on {
		if pointInRect(mx, my, ui.pr.rect) {
			ui.pr.x, ui.pr.y = float64(mx-ui.pr.rect.X), float64(my-ui.pr.rect.Y)
			lightgunPracticeShoot(time.Now())
		} else if ui.card.W == 0 || !pointInRect(mx, my, ui.card) {
			lightgunPracticeStop()
		}
		return true
	}
	for _, hit := range ui.hits {
		if pointInRect(mx, my, hit.rect) {
			if ui.cursor == hit.index {
				lightgunUIAction(tbxActConfirm)
			} else {
				ui.cursor = hit.index
				PlayNavSound()
			}
			return true
		}
	}
	if ui.card.W == 0 || !pointInRect(mx, my, ui.card) {
		lightgunUIClose(true)
	}
	return true
}

// ── Wording ─────────────────────────────────────────────────────────────────

// lightgunSigned is a shift in percent: "Middle", "+4%", "-6%".
func lightgunSigned(v int) string {
	switch {
	case v == 0:
		return "Middle"
	case v > 0:
		return "+" + itoa(v) + "%"
	}
	return "-" + itoa(-v) + "%"
}

// lightgunOnOff is a switch's value.
func lightgunOnOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// lightgunAreaName names an aim area.
func lightgunAreaName(area string) string {
	switch area {
	case "full":
		return "Whole screen"
	case "custom":
		return "Custom"
	}
	return "Game picture (4:3)"
}

// lightgunRowText is a row's label, value and what it does (the note).
func lightgunRowText(row int) (label, value, about string) {
	ui := &lightgunUI
	s := ui.set
	switch row {
	case lgRowUse:
		return "Light gun for this game", lightgunOnOff(lightgunInUse(ui.game)),
			"Sets the Light gun control layout for this game; Off puts the console's buttons back"
	case lgRowAim:
		if !stickHardware() {
			return "Aim", "D-pad steers", "No sticks here: the D-pad moves the crosshair and is not the game's D-pad"
		}
		if s.Aim == "point" {
			return "Aim", "Point", "The stick's position is the spot: at rest the middle, pushed all the way an edge"
		}
		return "Aim", "Move", "The stick steers the crosshair: tilt further to move faster"
	case lgRowSpeed:
		return "Speed", itoa(s.Speed) + " of 5", "How fast the crosshair moves at full tilt"
	case lgRowArea:
		return "Aim area", lightgunAreaName(s.Area), "Where Point reaches: RetroArch aims at the game picture, not the whole screen"
	case lgRowAreaW:
		return "Area width", itoa(s.AreaW) + "%", "The aim area's width, as a share of the screen"
	case lgRowAreaH:
		return "Area height", itoa(s.AreaH) + "%", "The aim area's height, as a share of the screen"
	case lgRowAreaX:
		return "Area left or right", lightgunSigned(s.AreaX), "Moves the aim area's middle across the screen"
	case lgRowAreaY:
		return "Area up or down", lightgunSigned(s.AreaY), "Moves the aim area's middle up or down"
	case lgRowTrigger:
		return "Fire with", strings.ToUpper(s.Trigger), "The shoulder that pulls the trigger; the game does not see it"
	case lgRowReload:
		return "Reload with", strings.ToUpper(s.Reload), "Shoots off the screen, which is how these games reload"
	case lgRowRecoil:
		return "Buzz on each shot", lightgunOnOff(s.Recoil), "A short buzz of the motor with every shot"
	case lgRowTry:
		return "Try it", "", "A practice target with these settings: feel the speed before you play"
	case lgRowAuto:
		return "Known gun games get it by themselves", lightgunOnOff(lightgunAutoAssignOn()),
			"Duck Hunt, Time Crisis and other gun games start with the light gun once; Off here or on a game sticks"
	}
	return "", "", ""
}

// lightgunSupportText is how the game's core reads a gun, and whether that is
// a warning.
func lightgunSupportText() (string, bool) {
	ui := &lightgunUI
	switch {
	case !ui.answered:
		return "Looking up the game's emulator…", false
	case !ui.found:
		return "No emulator found for this game", true
	case !ui.retroArch:
		return "This emulator keeps its own controls · the light gun works in RetroArch", true
	}
	c, ok := lightgunCoreFor(ui.core, ui.game.System)
	if !ok {
		return ctrlLayoutCoreLabel(ui.core) + ": JukaHub does not know its light gun, so the layout changes nothing", true
	}
	gun := c.Gun
	if ui.kind == "justifier" {
		gun = "Justifier"
	}
	text := c.Label + " · " + gun + " on port " + itoa(c.Port)
	if !lightgunSessionSupported() {
		text += " · your mouse is the gun here"
	}
	return text, false
}

// ── The practice target ─────────────────────────────────────────────────────

func lightgunPracticeStart() {
	ui := &lightgunUI
	w, _ := lightgunPanelSize()
	ui.pr = lightgunPractice{on: true, panelW: float64(w), ammo: lightgunMagazine, last: time.Now()}
	ui.pr.rect = lightgunPracticeRect()
	ui.pr.x, ui.pr.y = float64(ui.pr.rect.W)/2, float64(ui.pr.rect.H)/2
	lightgunPracticeNewTarget(0)
	PlayActivateSound()
}

func lightgunPracticeStop() {
	lightgunUI.pr.on = false
	PlayBackSound()
}

// lightgunPracticeRect is where the practice picture is drawn: the screen's
// shape, as large as the card allows.
func lightgunPracticeRect() sdl.Rect {
	cardW := min(int32(980), screenWidth-48)
	maxW := cardW - 2*OverlayPadX
	maxH := screenHeight - 48 - 150
	w := maxW
	h := w * screenHeight / max32(screenWidth, 1)
	if h > maxH {
		h = maxH
		w = h * screenWidth / max32(screenHeight, 1)
	}
	return sdl.Rect{W: w, H: h}
}

// lightgunPracticeNewTarget puts the target somewhere new, drifting a little
// faster after each hit.
func lightgunPracticeNewTarget(hits int) {
	p := &lightgunUI.pr
	w, h := float64(p.rect.W), float64(p.rect.H)
	r := h / 14
	p.tx = r + rand.Float64()*math.Max(1, w-2*r)
	p.ty = r + rand.Float64()*math.Max(1, h-2*r)
	speed := w * (0.08 + 0.015*math.Min(float64(hits), 8))
	angle := rand.Float64() * 2 * math.Pi
	p.tvx, p.tvy = math.Cos(angle)*speed, math.Sin(angle)*speed
}

// lightgunPracticeShoot fires one shot at the crosshair.
func lightgunPracticeShoot(now time.Time) {
	p := &lightgunUI.pr
	if p.ammo <= 0 {
		p.emptyUntil = now.Add(700 * time.Millisecond)
		PlayBackSound()
		return
	}
	p.ammo--
	p.shots++
	if lightgunUI.set.Recoil {
		playHaptic(lightgunRecoilPulse)
	}
	r := float64(p.rect.H) / 14
	if math.Hypot(p.x-p.tx, p.y-p.ty) <= r {
		p.hits++
		p.flashUntil = now.Add(250 * time.Millisecond)
		PlayActivateSound()
		lightgunPracticeNewTarget(p.hits)
		return
	}
	PlayNavSound()
}

func lightgunPracticeReload(now time.Time) {
	p := &lightgunUI.pr
	if p.ammo == lightgunMagazine {
		return
	}
	p.ammo = lightgunMagazine
	p.reloadAt = now
	PlayToggleSound()
}

// lightgunPracticeKey: arrows aim (read each frame), Space fires, R reloads,
// C puts the crosshair in the middle (the spare shoulder's twin), Esc or B
// ends the practice.
func lightgunPracticeKey(e *sdl.KeyboardEvent) {
	if e == nil || e.Type != sdl.KEYDOWN || e.Repeat != 0 {
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		lightgunPracticeStop()
	case sdl.K_SPACE, sdl.K_RETURN:
		lightgunPracticeShoot(time.Now())
	case sdl.K_r:
		lightgunPracticeReload(time.Now())
	case sdl.K_c:
		p := &lightgunUI.pr
		p.x, p.y = float64(p.rect.W)/2, float64(p.rect.H)/2
		PlayNavSound()
	}
}

// lightgunPracticePad: the game's trigger and reload shoulders, the spare R
// shoulder (or R3, where the sticks click) to the middle, B (or Select) ends
// the practice. R2 and L2 are read as axes each frame.
func lightgunPracticePad(e *sdl.ControllerButtonEvent) {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return
	}
	s := lightgunUI.set
	now := time.Now()
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		lightgunPracticeStop()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if s.Trigger == "r1" {
			lightgunPracticeShoot(now)
		} else {
			lightgunPracticeCentre()
		}
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		if s.Reload == "l1" {
			lightgunPracticeReload(now)
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		lightgunPracticeCentre()
	}
}

// lightgunPracticeCentre puts the practice crosshair in the middle, as the
// spare R shoulder and R3 re-centre the game's (lightgun_aim.go).
func lightgunPracticeCentre() {
	p := &lightgunUI.pr
	p.x, p.y = float64(p.rect.W)/2, float64(p.rect.H)/2
	PlayNavSound()
}

// lightgunPracticeTick moves the crosshair and the target to now.
func lightgunPracticeTick(now time.Time) {
	p := &lightgunUI.pr
	s := lightgunUI.set
	dt := clampFloat(now.Sub(p.last).Seconds(), 0, 0.05)
	p.last = now
	w, h := float64(p.rect.W), float64(p.rect.H)
	// The screen's pixels to the practice picture's.
	scale := w / math.Max(1, p.panelW)

	// The analog shoulders, latched as the game's mouse latches them.
	if pad := gameController; pad != nil {
		for side, axis := range [2]sdl.GameControllerAxis{sdl.CONTROLLER_AXIS_TRIGGERLEFT, sdl.CONTROLLER_AXIS_TRIGGERRIGHT} {
			v := float64(pad.Axis(axis)) / 32767
			switch {
			case !p.latch[side] && v >= mbridgeTrigDown:
				p.latch[side] = true
				// R2 fires, or re-centres when R1 is the trigger.
				if side == 1 && s.Trigger == "r2" {
					lightgunPracticeShoot(now)
				} else if side == 1 {
					lightgunPracticeCentre()
				}
				if side == 0 && s.Reload == "l2" {
					lightgunPracticeReload(now)
				}
			case p.latch[side] && v < mbridgeTrigUp:
				p.latch[side] = false
			}
		}
	}

	keys := sdl.GetKeyboardState()
	kx, ky := 0.0, 0.0
	if int(sdl.SCANCODE_DOWN) < len(keys) {
		if keys[sdl.SCANCODE_LEFT] != 0 {
			kx--
		}
		if keys[sdl.SCANCODE_RIGHT] != 0 {
			kx++
		}
		if keys[sdl.SCANCODE_UP] != 0 {
			ky--
		}
		if keys[sdl.SCANCODE_DOWN] != 0 {
			ky++
		}
	}
	var vx, vy float64
	switch {
	case kx != 0 || ky != 0:
		m := math.Hypot(kx, ky)
		vx, vy = lightgunMoveVelocity(s, kx/m*0.8, ky/m*0.8)
	case stickHardware() && s.Aim == "point":
		sx, sy := stickShaped(appConfig, stickRight)
		p.x, p.y = lightgunPointSpot(s, sx, sy, w, h)
	case stickHardware():
		sx, sy := stickShaped(appConfig, stickRight)
		vx, vy = lightgunMoveVelocity(s, sx, sy)
	case gameController != nil:
		dx, dy := 0, 0
		pad := gameController
		if pad.Button(sdl.CONTROLLER_BUTTON_DPAD_LEFT) != 0 {
			dx--
		}
		if pad.Button(sdl.CONTROLLER_BUTTON_DPAD_RIGHT) != 0 {
			dx++
		}
		if pad.Button(sdl.CONTROLLER_BUTTON_DPAD_UP) != 0 {
			dy--
		}
		if pad.Button(sdl.CONTROLLER_BUTTON_DPAD_DOWN) != 0 {
			dy++
		}
		held := dx != 0 || dy != 0
		if held && !p.dpadHeld {
			p.dpadAt = now
		}
		p.dpadHeld = held
		vx, vy = lightgunDpadVelocity(s, dx, dy, now.Sub(p.dpadAt))
	}
	p.x = clampFloat(p.x+vx*scale*dt, 0, w-1)
	p.y = clampFloat(p.y+vy*scale*dt, 0, h-1)

	// The target drifts and bounces off the picture's edges.
	r := h / 14
	p.tx += p.tvx * dt
	p.ty += p.tvy * dt
	if p.tx < r || p.tx > w-r {
		p.tvx = -p.tvx
		p.tx = clampFloat(p.tx, r, w-r)
	}
	if p.ty < r || p.ty > h-r {
		p.tvy = -p.tvy
		p.ty = clampFloat(p.ty, r, h-r)
	}
}

// ── Drawing ─────────────────────────────────────────────────────────────────

// lightgunUIRender draws the screen: the list, or the practice target.
func lightgunUIRender(renderer *sdl.Renderer, config *Config) {
	ui := &lightgunUI
	if !ui.open || renderer == nil {
		return
	}
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	if ui.pr.on {
		lightgunPracticeRender(renderer, config, fonts)
		return
	}
	header := UIPanelHeader("Light gun", ui.game.Label(), "gamepad")
	headerH := UIPanelHeaderBand(config, header, 10)
	rowH := fonts.bodyH + 18
	footerH := fonts.smallH + 20
	noteH := 2*fonts.tinyH + 18
	n := len(ui.rows)
	cardW := min(int32(760), screenWidth-48)
	cardH := min(headerH+int32(n)*rowH+noteH+footerH+8, screenHeight-48)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	ui.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	listY := card.Y + headerH
	bottom := card.Y + card.H - footerH - noteH
	innerW := card.W - 2*OverlayPadX
	visible := int((bottom - listY) / rowH)
	if visible < 1 {
		visible = 1
	}
	if ui.cursor < ui.first {
		ui.first = ui.cursor
	}
	if ui.cursor >= ui.first+visible {
		ui.first = ui.cursor - visible + 1
	}
	ui.hits = ui.hits[:0]
	about := ""
	for i := ui.first; i < n && i < ui.first+visible; i++ {
		label, value, what := lightgunRowText(ui.rows[i])
		if i == ui.cursor {
			about = what
		}
		rect := sdl.Rect{X: card.X + OverlayPadX, Y: listY + int32(i-ui.first)*rowH, W: innerW, H: rowH - 6}
		UIListRow(renderer, rect, glassRowRadius, i == ui.cursor, false)
		vw := int32(0)
		if value != "" {
			vw, _ = measureText(fonts.body, value)
			renderText(renderer, config, fonts.body, value, glassAccent(), rect.X+rect.W-16-vw, rect.Y+(rect.H-fonts.bodyH)/2)
		}
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, label, rect.W-48-vw), ColorTextPrimary(), rect.X+16, rect.Y+(rect.H-fonts.bodyH)/2)
		ui.hits = append(ui.hits, gameLibRowHit{index: i, rect: rect})
	}

	// What the focused row does; how the core reads a gun, or the last
	// launch's trouble.
	noteY := bottom + 4
	renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, about, innerW), ColorTextSecondary(), card.X+OverlayPadX, noteY)
	second, warn := lightgunSupportText()
	if note := lightgunNote(ui.game.Path); note != "" {
		second, warn = "Last time: "+note, true
	} else if !warn && ui.answered {
		second += " · set for each start, not yet checked on a TrimUI"
	}
	ink := ColorTextTertiary()
	if warn {
		ink = ColorWarning
	}
	renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, second, innerW), ink, card.X+OverlayPadX, noteY+fonts.tinyH+4)

	act := "Change"
	if ui.cursor >= 0 && ui.cursor < n && ui.rows[ui.cursor] == lgRowTry {
		act = "Try it"
	}
	hint := hintLine(FooterHint{"Left/Right", "Change"}, FooterHint{"A", act}, FooterHint{"Y", "Defaults"}, FooterHint{"B", "Back"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, innerW), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}

// lightgunPracticeRender draws the practice target.
func lightgunPracticeRender(renderer *sdl.Renderer, config *Config, fonts tbxFonts) {
	ui := &lightgunUI
	p := &ui.pr
	s := ui.set
	now := time.Now()
	lightgunPracticeTick(now)
	requestFrameEvery(16)

	headerH := fonts.bodyH + 24
	footerH := fonts.smallH + 20
	cardW := p.rect.W + 2*OverlayPadX
	cardH := headerH + p.rect.H + fonts.tinyH + 16 + footerH
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	ui.card = card
	p.rect.X, p.rect.Y = card.X+OverlayPadX, card.Y+headerH
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)

	title := "Practice · " + itoa(p.hits) + " hits of " + itoa(p.shots) + " shots"
	renderText(renderer, config, fonts.body, ellipsize(fonts.body, title, card.W-2*OverlayPadX), ColorTextPrimary(), card.X+OverlayPadX, card.Y+12)
	ammo := "Ammo " + itoa(p.ammo) + " of " + itoa(lightgunMagazine)
	switch {
	case now.Before(p.emptyUntil):
		ammo = "Empty: reload with " + strings.ToUpper(s.Reload)
	case !p.reloadAt.IsZero() && now.Sub(p.reloadAt) < 600*time.Millisecond:
		ammo = "Reloaded"
	}
	aw, _ := measureText(fonts.small, ammo)
	renderText(renderer, config, fonts.small, ammo, glassAccent(), card.X+card.W-OverlayPadX-aw, card.Y+14)

	// The picture: the screen in small, the aim area on it for Point.
	r := p.rect
	fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 8, sdl.Color{R: 12, G: 16, B: 24, A: 235})
	if stickHardware() && s.Aim == "point" {
		ax, ay, aw2, ah := lightgunArea(s, float64(r.W), float64(r.H))
		strokeRoundedRect(renderer, r.X+int32(ax), r.Y+int32(ay), int32(aw2), int32(ah), 6, 2, WithAlpha(glassAccent(), 120))
	}
	tr := int32(float64(r.H) / 14)
	tx, ty := r.X+int32(p.tx), r.Y+int32(p.ty)
	ring := sdl.Color{R: 230, G: 70, B: 60, A: 255}
	if now.Before(p.flashUntil) {
		ring = ColorSuccess
	}
	fillCircle(renderer, tx, ty, tr, ring)
	fillCircle(renderer, tx, ty, tr*2/3, sdl.Color{R: 245, G: 245, B: 245, A: 255})
	fillCircle(renderer, tx, ty, tr/3, ring)

	// The crosshair.
	cx, cy := r.X+int32(p.x), r.Y+int32(p.y)
	renderer.SetDrawColor(255, 255, 255, 255)
	arm := int32(14)
	_ = renderer.FillRect(&sdl.Rect{X: cx - arm, Y: cy - 1, W: arm*2 + 1, H: 3})
	_ = renderer.FillRect(&sdl.Rect{X: cx - 1, Y: cy - arm, W: 3, H: arm*2 + 1})
	strokeRoundedRect(renderer, cx-arm/2-2, cy-arm/2-2, arm+4, arm+4, arm/2+2, 2, sdl.Color{R: 255, G: 255, B: 255, A: 255})

	how := "Move · speed " + itoa(s.Speed) + " of 5"
	switch {
	case !stickHardware():
		how = "D-pad steers · speed " + itoa(s.Speed) + " of 5"
	case s.Aim == "point":
		how = "Point · aim area: " + lightgunAreaName(s.Area)
	}
	renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, how+" · change it in the list", card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, r.Y+r.H+6)

	var hint string
	if hintsUseKeyboard() {
		hint = hintLine(FooterHint{"Arrows", "Aim"}, FooterHint{"Space", "Fire"}, FooterHint{"R", "Reload"}, FooterHint{"C", "Middle"}, FooterHint{"Esc", "Done"})
	} else {
		aim := FooterHint{"Right stick", "Aim"}
		if !stickHardware() {
			aim = FooterHint{"D-Pad", "Aim"}
		}
		// The spare R shoulder re-centres on every model; R3 only clicks on
		// some, so it is not named (stickclicks.go).
		hints := []FooterHint{aim, {strings.ToUpper(s.Trigger), "Fire"}, {strings.ToUpper(s.Reload), "Reload"},
			{strings.ToUpper(lightgunSpareShoulder(s)), "Middle"}}
		hint = hintLine(append(hints, FooterHint{"B", "Done"})...)
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}

// ── The game page (gamepage_more.go) ────────────────────────────────────────

// lightgunPage is whether the open page's game is a known gun game that may
// get the gun by itself, worked out when the page opens (the chip is drawn
// every frame). Main thread only.
var lightgunPage struct {
	path  string
	known bool
}

// lightgunChip is the page's chip for a known gun game that will get the gun
// at its next start: "Light gun game". While the gun is on, the Controls chip
// says "Controls: Light gun" already (ctrllayout_ui.go).
func lightgunChip(game libraryGame) (string, int, bool) {
	if !lightgunOffered(game) || !ctrlLayoutStoreLoaded() || !lightgunStoreLoaded() || lightgunInUse(game) {
		return "", 0, false
	}
	// A standalone emulator or a NextUI pak never gets the gun (ctrllayout_ui.go
	// works that out for its own chip when the page opens).
	if f := &ctrlLayoutPageFacts; f.path == game.Path && f.known && !f.retroArch {
		return "", 0, false
	}
	if lightgunPage.path == game.Path && lightgunPage.known && lightgunAutoAssignOn() &&
		ctrlLayoutGameChoice(game.Path) == "" && !lightgunDecided(game.Path) {
		return "Light gun game", gamePageToneNeutral, true
	}
	return "", 0, false
}

// lightgunRowDetail is the More row's second line.
func lightgunRowDetail(game libraryGame) string {
	if !lightgunInUse(game) {
		if _, ok := lightgunKnownGame(game); ok {
			return "Off · made for a light gun"
		}
		return "Off · aim with the stick in gun games"
	}
	s := lightgunSettingsFor(game.Path)
	switch {
	case !stickHardware():
		return "On · D-pad steers, speed " + itoa(s.Speed)
	case s.Aim == "point":
		return "On · Point, " + strings.ToLower(lightgunAreaName(s.Area))
	}
	return "On · Move, speed " + itoa(s.Speed)
}

func init() {
	gamePageRegisterExtra(gamePageExtra{
		ID:    "light-gun",
		Order: 41,
		Icon:  "gamepad",
		Row: func(game libraryGame) (string, string, bool) {
			if !lightgunOffered(game) {
				return "", "", false
			}
			return "Light gun", lightgunRowDetail(game), true
		},
		Open: lightgunUIOpen,
	})
	gamePageRegisterOverlay(gamePageOverlay{
		ID:     "light-gun",
		IsOpen: func() bool { return lightgunUI.open },
		Key:    lightgunUIKey,
		Pad:    lightgunUIPad,
		Mouse:  lightgunUIMouse,
		Render: lightgunUIRender,
		Close:  func() { lightgunUIClose(false) },
	})
	gamePageRegisterStatus(gamePageStatus{ID: "light-gun", Order: 41, Chip: lightgunChip})
	gamePageRegisterOpened(func(game libraryGame) {
		// The chip's promise is the gate's: a whole-title match that may get
		// the gun by itself (lightgunAutoGame).
		_, auto := lightgunAutoGame(game)
		lightgunPage.path, lightgunPage.known = game.Path, auto && lightgunOffered(game)
		if !lightgunStoreLoaded() {
			go lightgunStoreLoad()
		}
	})
}
