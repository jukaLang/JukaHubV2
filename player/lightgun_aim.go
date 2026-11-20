package main

// lightgun_aim.go — the device-free half of the light gun: the engine that
// turns the pad's evdev events into the gun mouse's events, and the aim maths
// the practice target in the settings uses too, so the speed and the area
// feel the same there as in the game.
//
// Two ways to aim:
//
//   - Move: the stick's tilt is the crosshair's speed, on the in-app
//     pointer's curve (vcursorCurve) up to the game's speed setting. A device
//     without sticks (the Brick family) aims this way with the D-pad and its
//     speed ramp.
//   - Point: the stick's position is a spot in the aim area — at rest the
//     middle, pushed all the way an edge (the round stick is stretched to the
//     area's square corners). The mouse is relative, so the engine keeps
//     where the pointer is: an anchor pushes it far past the top-left corner
//     (SDL keeps a pointer inside its window), and every move after that is
//     counted. A light smoothing takes the stick's jitter out.
//
// The trigger and reload are the left and right mouse buttons, from R1 / L1
// or R2 / L2 (a key on some pads, an axis on the TrimUI's: latched like the
// games mouse's wheel triggers). R3 asks for an anchor and the crosshair back
// in the middle of the area.

import (
	"math"
	"time"
)

// The pad buttons the gun reads besides the ones vcursor_evdev.go names
// (input-event-codes.h).
const (
	lightgunBtnTL2    = 0x138
	lightgunBtnTR2    = 0x139
	lightgunBtnThumbR = 0x13e
	// lightgunAnchorPush is how far the anchor pushes up and left, in counts:
	// past the top-left corner from anywhere on any panel.
	lightgunAnchorPush = 8000
	// lightgunPointSmooth is the Point target's smoothing time constant.
	lightgunPointSmooth = 0.03
)

// lightgunRecoilPulse is the buzz of one shot (the game's and the practice
// target's): longer than a navigation tick, so it is never skipped as one.
var lightgunRecoilPulse = hapticPulse{strength: 0.55, ms: 32}

// lightgunSpeeds are the Move speeds at full tilt, panel pixels per second:
// the 1280-wide Smart Pro screen crossed in about 4, 2.7, 1.8, 1.35 and 1 s.
var lightgunSpeeds = [lightgunSpeedMax]float64{320, 480, 700, 950, 1280}

// lightgunSpeedPx is a speed setting in pixels per second.
func lightgunSpeedPx(speed int) float64 {
	if speed < lightgunSpeedMin || speed > lightgunSpeedMax {
		speed = lightgunSpeedDefault
	}
	return lightgunSpeeds[speed-1]
}

// lightgunAimParams is what the engine needs, read on the main thread when
// the gun is made: the game's settings, the right stick's calibration,
// whether the device has sticks, and the panel's size in pixels.
type lightgunAimParams struct {
	Set    lightgunSettings
	CalR   stickCal
	Sticks bool
	W, H   int
}

// lightgunSessionParams is one launch's gun.
type lightgunSessionParams struct {
	Path   string
	Recoil bool
	Aim    lightgunAimParams
}

// lightgunArea is the aim area for Point on a w × h screen: x, y, width and
// height in pixels. "picture" is a 4:3 picture as tall as the screen in its
// middle (narrower screens: as wide). Pure.
func lightgunArea(s lightgunSettings, w, h float64) (float64, float64, float64, float64) {
	switch s.Area {
	case "full":
		return 0, 0, w, h
	case "custom":
		aw, ah := w*float64(s.AreaW)/100, h*float64(s.AreaH)/100
		cx, cy := w/2+w*float64(s.AreaX)/100, h/2+h*float64(s.AreaY)/100
		x := clampFloat(cx-aw/2, 0, math.Max(0, w-aw))
		y := clampFloat(cy-ah/2, 0, math.Max(0, h-ah))
		return x, y, aw, ah
	}
	aw, ah := h*4/3, h
	if aw > w {
		aw, ah = w, w*3/4
	}
	return (w - aw) / 2, (h - ah) / 2, aw, ah
}

// lightgunSquare stretches a stick position inside the unit circle onto the
// unit square, so pushing a diagonal reaches the area's corner. Pure.
func lightgunSquare(x, y float64) (float64, float64) {
	m := math.Hypot(x, y)
	big := math.Max(math.Abs(x), math.Abs(y))
	if m == 0 || big == 0 {
		return 0, 0
	}
	k := m / big
	return clampFloat(x*k, -1, 1), clampFloat(y*k, -1, 1)
}

// lightgunPointSpot is where Point puts the crosshair for a calibrated stick
// position, in a w × h screen's pixels. Pure.
func lightgunPointSpot(s lightgunSettings, sx, sy, w, h float64) (float64, float64) {
	ax, ay, aw, ah := lightgunArea(s, w, h)
	qx, qy := lightgunSquare(sx, sy)
	x := ax + aw/2 + qx*aw/2
	y := ay + ah/2 + qy*ah/2
	return clampFloat(x, ax, ax+aw-1), clampFloat(y, ay, ay+ah-1)
}

// lightgunMoveVelocity is Move's velocity for a calibrated stick position,
// pixels per second. Pure.
func lightgunMoveVelocity(s lightgunSettings, sx, sy float64) (float64, float64) {
	m := math.Hypot(sx, sy)
	if m < vcursorCreep {
		return 0, 0
	}
	k := vcursorCurve(math.Min(m, 1)) / m * lightgunSpeedPx(s.Speed)
	return sx * k, sy * k
}

// lightgunDpadVelocity is Move's velocity from a held D-pad (dx, dy each -1,
// 0 or 1): the in-app pointer's ramp, scaled to the game's speed. Pure.
func lightgunDpadVelocity(s lightgunSettings, dx, dy int, held time.Duration) (float64, float64) {
	vx, vy := vcursorDpadVelocity(dx, dy, held)
	k := lightgunSpeedPx(s.Speed) / vcursorMaxSpeed
	return vx * k, vy * k
}

// lightgunEngine turns pad events into the gun mouse's events. It does no
// I/O: the session feeds it what it read and writes what it returns.
type lightgunEngine struct {
	p      lightgunAimParams
	ranges [18]evdevAbsRange
	abs    [18]int32
	keys   [16]bool
	// latch is the R2 / L2 axis read as a button (index 0 L2, 1 R2).
	latch [2]bool
	// btn is the mouse's left (trigger) and right (reload) button held.
	btn [2]bool
	// recenter: R3 was pressed; the session anchors.
	recenter bool
	// shot: the trigger went down this tick (the recoil buzz).
	shot bool
	// pos is where the pointer is, once an anchor made it known; centre
	// asks the next tick to move it to the middle of the aim area.
	pos    [2]float64
	known  bool
	centre bool
	// aim is Point's smoothed target; acc the motion not sent yet.
	aim    [2]float64
	acc    [2]float64
	dpadAt time.Time
	last   time.Time
}

// setRange records an axis's range and value (EVIOCGABS when the pad opens).
func (g *lightgunEngine) setRange(axis uint16, r evdevAbsRange, value int32) {
	if int(axis) < len(g.ranges) {
		g.ranges[axis] = r
		g.abs[axis] = value
	}
}

// setKeys loads the buttons held now (EVIOCGKEY).
func (g *lightgunEngine) setKeys(bitmap []byte) {
	for i := range g.keys {
		g.keys[i] = evdevKeyBit(bitmap, uint16(mbridgeKeyBase+i))
	}
}

// key reports whether a pad button is held.
func (g *lightgunEngine) key(code uint16) bool {
	i := int(code) - mbridgeKeyBase
	return i >= 0 && i < len(g.keys) && g.keys[i]
}

// feed takes one event read from the pad.
func (g *lightgunEngine) feed(ev evdevEvent, now time.Time) {
	switch ev.Type {
	case evdevEvKey:
		i := int(ev.Code) - mbridgeKeyBase
		if i < 0 || i >= len(g.keys) || ev.Value == 2 {
			return
		}
		down := ev.Value != 0
		if ev.Code == lightgunBtnThumbR && down && !g.keys[i] {
			g.recenter = true
		}
		g.keys[i] = down
	case evdevEvAbs:
		if int(ev.Code) >= len(g.abs) {
			return
		}
		hat := ev.Code == evdevAbsHat0X || ev.Code == evdevAbsHat0Y
		idle := g.abs[evdevAbsHat0X] == 0 && g.abs[evdevAbsHat0Y] == 0
		g.abs[ev.Code] = ev.Value
		if hat && idle && ev.Value != 0 {
			g.dpadAt = now
		}
	}
}

// held reports a gun button ("r1", "r2", "l1", "l2") held: a key, or the
// trigger axis past its latch.
func (g *lightgunEngine) held(name string) bool {
	switch name {
	case "r2":
		return g.key(lightgunBtnTR2) || g.latch[1]
	case "l1":
		return g.key(evdevBtnTL)
	case "l2":
		return g.key(lightgunBtnTL2) || g.latch[0]
	}
	return g.key(evdevBtnTR)
}

// stick reads a stick axis as -1..1 (0 for an axis the pad does not have).
func (g *lightgunEngine) stick(axis uint16) float64 {
	return g.ranges[axis].stick(g.abs[axis])
}

// button moves one mouse button to down, appending the event when it changes.
func (g *lightgunEngine) button(i int, down bool, out []evdevEvent) []evdevEvent {
	if g.btn[i] == down {
		return out
	}
	g.btn[i] = down
	return append(out, evdevEvent{Type: evdevEvKey, Code: evdevMouseButtons[i], Value: evdevBool(down)})
}

// anchorEvents is the push past the top-left corner; the session writes it as
// a report of its own, then calls anchored.
func (g *lightgunEngine) anchorEvents(out []evdevEvent) []evdevEvent {
	return append(out,
		evdevEvent{Type: evdevEvRel, Code: evdevRelX, Value: -lightgunAnchorPush},
		evdevEvent{Type: evdevEvRel, Code: evdevRelY, Value: -lightgunAnchorPush})
}

// anchored records the pointer in the corner, and asks for the middle of the
// aim area next.
func (g *lightgunEngine) anchored() {
	g.pos = [2]float64{}
	g.known, g.centre = true, true
	g.acc = [2]float64{}
}

// releaseAll lets go of both mouse buttons (the game is paused or ending).
func (g *lightgunEngine) releaseAll(out []evdevEvent) []evdevEvent {
	out = g.button(0, false, out)
	return g.button(1, false, out)
}

// moveBy appends a whole-count move and keeps pos inside the screen, as SDL
// keeps its pointer.
func (g *lightgunEngine) moveBy(dx, dy int32, out []evdevEvent) []evdevEvent {
	if dx != 0 {
		out = append(out, evdevEvent{Type: evdevEvRel, Code: evdevRelX, Value: dx})
	}
	if dy != 0 {
		out = append(out, evdevEvent{Type: evdevEvRel, Code: evdevRelY, Value: dy})
	}
	w, h := float64(g.p.W), float64(g.p.H)
	g.pos[0] = clampFloat(g.pos[0]+float64(dx), 0, math.Max(0, w-1))
	g.pos[1] = clampFloat(g.pos[1]+float64(dy), 0, math.Max(0, h-1))
	return out
}

// moveTo appends the move from pos to (x, y).
func (g *lightgunEngine) moveTo(x, y float64, out []evdevEvent) []evdevEvent {
	return g.moveBy(int32(math.Round(x-g.pos[0])), int32(math.Round(y-g.pos[1])), out)
}

// tick advances the engine to now: the buttons, then the motion.
func (g *lightgunEngine) tick(now time.Time, out []evdevEvent) []evdevEvent {
	dt := clampFloat(now.Sub(g.last).Seconds(), 0, 0.05)
	g.last = now
	for side, axis := range [2]uint16{evdevAbsZ, evdevAbsRZ} {
		v := g.ranges[axis].trigger(g.abs[axis])
		switch {
		case !g.latch[side] && v >= mbridgeTrigDown:
			g.latch[side] = true
		case g.latch[side] && v < mbridgeTrigUp:
			g.latch[side] = false
		}
	}
	fire := g.held(g.p.Set.Trigger)
	if fire && !g.btn[0] {
		g.shot = true
	}
	out = g.button(0, fire, out)
	out = g.button(1, g.held(g.p.Set.Reload), out)

	w, h := float64(g.p.W), float64(g.p.H)
	if g.centre && g.known {
		g.centre = false
		ax, ay, aw, ah := lightgunArea(g.p.Set, w, h)
		cx, cy := ax+aw/2, ay+ah/2
		g.aim = [2]float64{cx, cy}
		return g.moveTo(cx, cy, out)
	}
	if g.p.Sticks && g.p.Set.Aim == "point" {
		if !g.known {
			// Where the pointer is will be known after the first anchor.
			return out
		}
		sx, sy := g.p.CalR.shape(g.stick(evdevAbsRX), g.stick(evdevAbsRY))
		tx, ty := lightgunPointSpot(g.p.Set, sx, sy, w, h)
		k := 1 - math.Exp(-dt/lightgunPointSmooth)
		g.aim[0] += (tx - g.aim[0]) * k
		g.aim[1] += (ty - g.aim[1]) * k
		return g.moveTo(g.aim[0], g.aim[1], out)
	}
	var vx, vy float64
	if g.p.Sticks {
		sx, sy := g.p.CalR.shape(g.stick(evdevAbsRX), g.stick(evdevAbsRY))
		vx, vy = lightgunMoveVelocity(g.p.Set, sx, sy)
	} else {
		dx, dy := evdevHatDir(g.abs[evdevAbsHat0X]), evdevHatDir(g.abs[evdevAbsHat0Y])
		vx, vy = lightgunDpadVelocity(g.p.Set, dx, dy, now.Sub(g.dpadAt))
	}
	g.acc[0] += vx * dt
	g.acc[1] += vy * dt
	nx, ny := int32(g.acc[0]), int32(g.acc[1])
	g.acc[0] -= float64(nx)
	g.acc[1] -= float64(ny)
	return g.moveBy(nx, ny, out)
}
