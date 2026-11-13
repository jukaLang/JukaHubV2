package main

// The focus bounce: when a button press moves the focus from one control to
// the next, the highlight (the accent ring and its soft glow) travels there
// instead of jumping, runs a little past the new control in the direction it
// was moving and swings back onto it — the bounce Juka asked for on Home on
// 2026-11-12, and on 2026-11-13 for every screen, on the Smart Pro too, with
// its strength in Settings → Display & theme → Focus bounce.
//
// Only the highlight moves. The control's own lighter fill and its lift change
// on the frame the focus does, so the eye sees where focus landed at once and
// the ring arrives a beat later.
//
// The main loop opens one window per frame (beginFrame before the scene,
// endFrame before Present), and every ring drawn through drawGlassFocusRing /
// drawGlassFocusGlow / drawHomeFocusRing / drawHomeFocusGlow and Home's other
// rings asks apply where to draw. Each distinct highlight drawn in a frame is
// a track, in draw order: the scene's ring is usually track 0 and a panel
// opened over it track 1, so moving in the panel bounces the panel's ring and
// leaves the scene's alone. A glow and a ring with the same centre share a
// track and its offset.
//
// A track glides only when its highlight jumped on a frame that had input: a
// list scrolling under a still focus, a card sliding in, a carousel settling
// move the ring with them and never start a bounce. A new screen (scene index)
// and a frame gap (a game, the screensaver) start every track in place.
//
// Why it was missing on the Smart Pro: the glide followed Reduce Motion, and
// the handheld's shipped config has reducedMotion on (trimui/
// jukaconfig.device.json) — the PC's does not. The bounce has its own setting
// now; Reduce Motion still turns off the lifts and the scene fades.

import (
	"math"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// focusBounceKey is the setting in Variables.Custom: off, subtle, normal,
// bouncy. Missing or unknown reads as normal.
const focusBounceKey = "focusBounce"

type focusBounceLevel int

const (
	focusBounceOff focusBounceLevel = iota
	focusBounceSubtle
	focusBounceNormal
	focusBounceBouncy
)

var focusBounceValues = [...]string{"off", "subtle", "normal", "bouncy"}

// focusBounceTune is one level's motion: the whole move in ms (travel,
// overshoot and settle), easeOutBack's constant (1.70158 crosses the target by
// about 10% of the distance at 60% of the time) and the cap on that crossing
// at the 1280-wide canvas, so a long jump does not overshoot by a gutter.
type focusBounceTune struct {
	ms      uint64
	back    float64
	maxOver int32
}

var focusBounceTunes = [...]focusBounceTune{
	focusBounceOff:    {},
	focusBounceSubtle: {ms: 220, back: 0.9, maxOver: 6},
	focusBounceNormal: {ms: 300, back: 1.70158, maxOver: 14},
	focusBounceBouncy: {ms: 420, back: 2.8, maxOver: 28},
}

// focusBounceLevelOf reads the setting (appConfig when config is nil).
func focusBounceLevelOf(config *Config) focusBounceLevel {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return focusBounceNormal
	}
	s, _ := config.Variables.Custom[focusBounceKey].(string)
	return parseFocusBounce(s)
}

func parseFocusBounce(s string) focusBounceLevel {
	s = strings.ToLower(strings.TrimSpace(s))
	for i, v := range focusBounceValues {
		if s == v {
			return focusBounceLevel(i)
		}
	}
	return focusBounceNormal
}

// focusGlideTracks is how many separate highlights one frame can bounce.
const focusGlideTracks = 4

// focusGlideTrack is one highlight's glide.
type focusGlideTrack struct {
	have bool // to holds the last frame's highlight
	to   homeGlideRect

	active bool
	off    homeGlideRect // where the ring was, from the new target, at start
	start  uint64

	// This frame's claim: its centre and the offset it settled on.
	cx, cy float64
	dx, dy float64
	dw, dh float64
}

type homeFocusGlideState struct {
	open    bool // between the main loop's beginFrame and endFrame
	enabled bool // the bounce is on this frame
	frameMS uint64
	tune    focusBounceTune

	// focusID is Home's focused control, set by HomeLayout.Render; the
	// console themes' ring pulse restarts when it changes (themeconsole.go).
	focusID HomeFocusID

	ctx       int    // the screen the tracks belong to
	lastInput uint64 // focusNavMark at the previous frame
	input     bool   // a direction step since the previous frame
	// pastLowPower: a moving ring may pass Low Power's frame-rate cap.
	pastLowPower bool

	tracks [focusGlideTracks]focusGlideTrack
	claims int // tracks claimed this frame
}

// homeGlideRect is a rect as centre and size, the space the glide moves in.
type homeGlideRect struct{ cx, cy, w, h float64 }

func homeGlideRectOf(r sdl.Rect) homeGlideRect {
	return homeGlideRect{
		cx: float64(r.X) + float64(r.W)/2, cy: float64(r.Y) + float64(r.H)/2,
		w: float64(r.W), h: float64(r.H),
	}
}

var homeFocusGlide homeFocusGlideState

// homeGlideEaseOutBack runs from 0 to 1, crossing 1 on the way.
func homeGlideEaseOutBack(t, c1 float64) float64 {
	t = uiClamp01(t)
	c3 := c1 + 1
	u := t - 1
	return 1 + c3*u*u*u + c1*u*u
}

// focusNavMark counts direction steps: D-pad presses (held-direction repeats
// and the stick's D-pad arrive as pushed D-pad presses) and arrow/Tab keys.
// Only these start a bounce. A, B and Start also move the focus (into a
// folder, back to the parent list, a panel opening) but that is a new place,
// not a step; the mouse, wheel scrolls and the idle-screen hold-off that
// marks "input" during playback are not steps either.
var focusNavMark uint64

// focusGlideNotePad notes a pad press from the main loop's event handler.
func focusGlideNotePad(button uint8) {
	switch button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP, sdl.CONTROLLER_BUTTON_DPAD_DOWN,
		sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		focusNavMark++
	}
}

// focusGlideNoteKey notes a key press from the main loop's event handler.
func focusGlideNoteKey(sym sdl.Keycode) {
	switch sym {
	case sdl.K_UP, sdl.K_DOWN, sdl.K_LEFT, sdl.K_RIGHT, sdl.K_TAB:
		focusNavMark++
	}
}

// focusGlideFrame opens the frame's window from the main loop.
func focusGlideFrame(config *Config, sceneIndex int) {
	g := &homeFocusGlide
	// Battery Saver keeps its frame-rate cap; Low Power (the handheld's
	// default) lets a moving ring have the active cadence.
	g.pastLowPower = currentPowerMode(config) != PowerModeBatterySaver
	g.beginFrame(uiClockMS(), sceneIndex, focusBounceLevelOf(config), focusNavMark)
}

// beginFrame opens the window for one frame. ctx is the screen on show;
// navMark is focusNavMark, compared with the previous frame's.
func (g *homeFocusGlideState) beginFrame(now uint64, ctx int, level focusBounceLevel, navMark uint64) {
	enabled := level > focusBounceOff && int(level) < len(focusBounceTunes)
	// A screen that was not drawn for a while (a game, the screensaver), a
	// different screen, or the bounce switched off: every highlight starts
	// where it is rather than flying in from where it was left.
	if !enabled || ctx != g.ctx || (g.frameMS > 0 && now > g.frameMS+500) {
		g.tracks = [focusGlideTracks]focusGlideTrack{}
	}
	g.open, g.enabled, g.frameMS, g.ctx = true, enabled, now, ctx
	if enabled {
		g.tune = focusBounceTunes[level]
	}
	g.input = navMark != g.lastInput
	g.lastInput = navMark
	g.claims = 0
}

// endFrame closes the window: rings drawn after it are left alone. A track
// nothing claimed this frame is gone (its panel closed, its focus hidden).
func (g *homeFocusGlideState) endFrame() {
	if !g.open {
		return
	}
	g.open = false
	for i := range g.tracks {
		t := &g.tracks[i]
		if i >= g.claims {
			*t = focusGlideTrack{}
			continue
		}
		if t.active && g.frameMS >= t.start+g.tune.ms {
			t.active = false
		}
	}
}

// animating reports whether a highlight is still travelling, so the frame
// rate keeps its interactive cadence until it lands.
func (g *homeFocusGlideState) animating() bool {
	if !g.enabled {
		return false
	}
	now := uiClockMS()
	for i := range g.tracks {
		if t := &g.tracks[i]; t.active && now < t.start+g.tune.ms+20 {
			return true
		}
	}
	return false
}

// at is where track t's glide stands at now, given its target (the current
// frame's highlight), in centre/size space.
func (g *homeFocusGlideState) at(t *focusGlideTrack, now uint64, target homeGlideRect) homeGlideRect {
	if !t.active || g.tune.ms == 0 {
		return target
	}
	p := float64(now-t.start) / float64(g.tune.ms)
	if now < t.start {
		p = 0
	}
	if p >= 1 {
		return target
	}
	// The ring eases its offset from the target, not a screen position: a
	// target that is itself moving (a carousel tile easing to the centre, a
	// list scrolling to keep the row in view) carries the ring with it, and
	// only the step between the two controls bounces.
	k := 1 - homeGlideEaseOutBack(p, g.tune.back) // 1 → past 0 → 0
	maxOver := float64(max32(2, scaleX(g.tune.maxOver, screenWidth)))
	axis := func(off float64) float64 {
		v := off * k
		// Past the target: keep the crossing small whatever the distance.
		if k < 0 && math.Abs(v) > maxOver {
			v = math.Copysign(maxOver, v)
		}
		return v
	}
	// The size eases without overshoot: a ring that balloons past a tile's
	// size reads as a glitch, a ring that slides past it reads as momentum.
	s := 1 - uiEaseOutCubic(p)
	return homeGlideRect{
		cx: target.cx + axis(t.off.cx),
		cy: target.cy + axis(t.off.cy),
		w:  target.w + t.off.w*s,
		h:  target.h + t.off.h*s,
	}
}

// focusGlideComparable reports whether two highlights are the same kind of
// control: a ring jumping from a list row to a small button in a panel that
// just took over is a new focus, not a step, and is drawn in place.
func focusGlideComparable(a, b homeGlideRect) bool {
	if a.w <= 0 || a.h <= 0 || b.w <= 0 || b.h <= 0 {
		return false
	}
	ra, rb := a.w*a.h, b.w*b.h
	return ra <= rb*6 && rb <= ra*6
}

// apply returns where to draw a focus highlight asked for at rect. The result
// is rect itself when nothing is gliding.
func (g *homeFocusGlideState) apply(rect sdl.Rect) sdl.Rect {
	if !g.open || !g.enabled || rect.W <= 0 || rect.H <= 0 {
		return rect
	}
	r := homeGlideRectOf(rect)
	// The same control's other layer (glow, then ring): same centre, same
	// offset.
	for i := 0; i < g.claims; i++ {
		t := &g.tracks[i]
		if math.Abs(r.cx-t.cx) <= 3 && math.Abs(r.cy-t.cy) <= 3 {
			return t.offset(rect)
		}
	}
	if g.claims >= len(g.tracks) {
		return rect
	}
	t := &g.tracks[g.claims]
	g.claims++
	t.cx, t.cy = r.cx, r.cy
	now := g.frameMS
	jumped := t.have && (math.Abs(r.cx-t.to.cx) > 2 || math.Abs(r.cy-t.to.cy) > 2)
	if jumped && g.input && focusGlideComparable(t.to, r) {
		// Start from wherever the highlight is now — mid-flight included — so
		// a quick second step carries on smoothly instead of restarting.
		cur := g.at(t, now, t.to)
		t.off = homeGlideRect{cx: cur.cx - r.cx, cy: cur.cy - r.cy, w: cur.w - r.w, h: cur.h - r.h}
		t.start, t.active = now, true
	}
	t.to, t.have = r, true
	cur := g.at(t, now, r)
	t.dx, t.dy = cur.cx-r.cx, cur.cy-r.cy
	t.dw, t.dh = cur.w-r.w, cur.h-r.h
	if t.active {
		// The move gets the active cadence even under Low Power (the
		// handheld's default): at its 24 fps idle cadence a 300 ms bounce
		// was seven frames and read as a jump. Battery Saver keeps its cap.
		if g.pastLowPower {
			requestFrameEveryPastLowPower(1)
		} else {
			requestFrameEvery(1)
		}
	}
	return t.offset(rect)
}

func (t *focusGlideTrack) offset(rect sdl.Rect) sdl.Rect {
	if t.dx == 0 && t.dy == 0 && t.dw == 0 && t.dh == 0 {
		return rect
	}
	w := math.Max(1, float64(rect.W)+t.dw)
	h := math.Max(1, float64(rect.H)+t.dh)
	cx := float64(rect.X) + float64(rect.W)/2 + t.dx
	cy := float64(rect.Y) + float64(rect.H)/2 + t.dy
	return sdl.Rect{
		X: int32(math.Round(cx - w/2)), Y: int32(math.Round(cy - h/2)),
		W: int32(math.Round(w)), H: int32(math.Round(h)),
	}
}

// ── Settings ─────────────────────────────────────────────────────────────

var focusBounceOptions = []setOption{
	{Value: "off", Label: "Off", Note: "The highlight jumps straight to the next button"},
	{Value: "subtle", Label: "Subtle", Note: "A quick slide with a slight swing"},
	{Value: "normal", Label: "Normal", Note: "Slides over, runs a little past and swings back"},
	{Value: "bouncy", Label: "Bouncy", Note: "A longer slide and a bigger swing"},
}

var focusBounceLabels = [...]string{"Off", "Subtle", "Normal", "Bouncy"}

// focusBounceRow is Settings → Display & theme → Focus bounce.
func focusBounceRow() setRow {
	return setRow{
		ID: focusBounceKey, Label: "Focus bounce", Kind: setKindChoice,
		Detail: func(*Config) string {
			return "How the highlight moves when you step to another button, on every screen"
		},
		Options: func(*Config) []setOption { return focusBounceOptions },
		Current: func(c *Config) int { return int(focusBounceLevelOf(c)) },
		Choose: func(c *Config, value string) {
			level := parseFocusBounce(value)
			setStoreString(c, focusBounceKey, focusBounceValues[level])
			setToast("Focus bounce: " + focusBounceLabels[level])
		},
	}
}
