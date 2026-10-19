package main

// Checks for the UI keyframe layer: the track evaluator, the entrance state
// machine, the compositing path that fades a whole panel in one go, and the
// hover lift used by the home cards.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func TestUIKeyframeTracks(t *testing.T) {
	// Outside the track the ends clamp.
	if got := uiMenuDrop.at(-1); got != -18 {
		t.Errorf("before the first key = %v, want -18", got)
	}
	if got := uiMenuDrop.at(2); got != 0 {
		t.Errorf("after the last key = %v, want 0", got)
	}
	// Keys themselves are exact.
	for _, k := range uiMenuDrop {
		if got := uiMenuDrop.at(k.At); got != k.Value {
			t.Errorf("at key %.2f = %v, want %v", k.At, got, k.Value)
		}
	}
	// The overshoot key really goes past the resting value.
	if got := uiMenuDrop.at(0.62); got <= 0 {
		t.Errorf("overshoot value = %v, want > 0", got)
	}
	// The fade only ever moves forward.
	prev := -1.0
	for i := 0; i <= 40; i++ {
		v := uiFadeIn.at(float64(i) / 40)
		if v < prev {
			t.Fatalf("fade went backwards at t=%.2f: %v after %v", float64(i)/40, v, prev)
		}
		prev = v
	}
	if uiFadeIn.at(0) != 0 || uiFadeIn.at(1) != 1 {
		t.Errorf("fade endpoints = %v..%v, want 0..1", uiFadeIn.at(0), uiFadeIn.at(1))
	}
	// The scrim finishes fading before the panel's own entrance ends.
	if got := uiScrimProgress(0, uiMenuEnterMS); got != 0 {
		t.Errorf("scrim at t=0 = %v, want 0", got)
	}
	if got := uiScrimProgress(1, uiMenuEnterMS); got != 1 {
		t.Errorf("scrim at the end = %v, want 1", got)
	}
	settledScrim := float64(uiFadeMS) / float64(uiMenuEnterMS)
	if got := uiScrimProgress(settledScrim, uiMenuEnterMS); got < 0.99 {
		t.Errorf("scrim at the fade duration = %v, want ~1", got)
	}

	// A track with no keys is inert rather than panicking.
	if got := (uiKeyframe{}).at(0.5); got != 0 {
		t.Errorf("empty track = %v, want 0", got)
	}
}

func TestUIEnterStateMachine(t *testing.T) {
	var e uiEnter
	if got := e.progress(1234); got != 1 {
		t.Fatalf("inactive entrance progress = %v, want 1 (settled)", got)
	}
	e.begin(1000, 170)
	if got := e.progress(1000); got != 0 {
		t.Errorf("progress at start = %v, want 0", got)
	}
	if got := e.progress(1085); got < 0.45 || got > 0.55 {
		t.Errorf("progress halfway = %v, want about 0.5", got)
	}
	if got := e.progress(1170); got != 1 {
		t.Errorf("progress after the duration = %v, want 1", got)
	}
	if !e.settled() {
		t.Error("entrance should settle once it reaches the end")
	}
	// A restart plays again.
	e.begin(2000, 100)
	if got := e.progress(2000); got != 0 {
		t.Errorf("restarted progress = %v, want 0", got)
	}
}

func TestUIHoverEasesTowardTarget(t *testing.T) {
	var h uiHover
	if got := h.update(1, false); got != 1 {
		t.Fatalf("snapped weight = %v, want 1", got)
	}
	h.weight = 0
	first := h.update(1, true)
	if first <= 0 || first >= 1 {
		t.Fatalf("eased weight = %v, want strictly between 0 and 1", first)
	}
	prev := first
	for i := 0; i < 80; i++ {
		next := h.update(1, true)
		if next < prev {
			t.Fatalf("hover weight went backwards: %v after %v", next, prev)
		}
		prev = next
	}
	if prev != 1 {
		t.Errorf("hover weight settled at %v, want 1", prev)
	}
}

// compositeUILayer must really fade the layer and shift it: with alpha 0.5 the
// white panel lands at ~50% grey, and offsetY moves it without moving the rest
// of the frame.
func TestCompositeUILayerFadesAndSlides(t *testing.T) {
	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	withScreen(t, probeW, probeH)
	if uiLayerTexture(renderer) == nil {
		t.Skip("backend has no render targets; the panel draws unanimated")
	}

	drawPanel := func() {
		renderer.SetDrawColor(255, 255, 255, 255)
		renderer.FillRect(&sdl.Rect{X: 100, Y: 100, W: 200, H: 100})
	}

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	compositeUILayer(renderer, 0.5, 20, drawPanel)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}

	// Inside the shifted, half-opacity panel.
	c := f.at(200, 160)
	if c.R < 110 || c.R > 145 {
		t.Errorf("panel pixel = %+v, want about 50%% of white", c)
	}
	// Above the shifted panel the background must still show through, which
	// proves the layer moved and did not just fade in place.
	if !f.isBG(200, 105) {
		t.Errorf("pixel above the shifted panel = %+v, want the background", f.at(200, 105))
	}
}

// The Apps menu enters through the keyframe state: at t=0 nothing is painted,
// halfway it is translucent, and once settled it is the full menu. A fresh open
// must also keep the menu's hit geometry at the settled position.
func TestStartMenuEntranceFadesIn(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	idx := findSceneIndex(config, "Settings")
	if idx < 0 {
		t.Fatal("Settings scene missing")
	}

	prevOpen, prevEnter, prevRect := tb.startOpen, tb.enter, tb.startRect
	prevScene := currentSceneIndex
	t.Cleanup(func() {
		tb.startOpen, tb.enter, tb.startRect = prevOpen, prevEnter, prevRect
		currentSceneIndex = prevScene
		ClearTextCache()
	})

	renderMenu := func(tick uint64) *probeFrame {
		t.Helper()
		f := newProbeFrame(t)
		renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
		if err != nil {
			t.Skipf("software renderer unavailable: %v", err)
		}
		defer renderer.Destroy()
		withScreen(t, probeW, probeH)
		ClearTextCache()
		if uiLayerTexture(renderer) == nil {
			t.Skip("backend has no render targets; the menu draws unanimated")
		}

		previousClock := uiClockMS
		uiClockMS = func() uint64 { return tick }
		defer func() { uiClockMS = previousClock }()

		renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
		renderer.Clear()
		renderStartMenu(renderer, config)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
			t.Skipf("ReadPixels unavailable: %v", err)
		}
		return f
	}

	tb.startOpen = true
	tb.startRect = sdl.Rect{X: 20, Y: 12, W: 70, H: 22}
	currentSceneIndex = idx
	const start = 5000
	tb.enter.begin(start, uiMenuEnterMS)

	closed := renderMenu(start)
	mid := renderMenu(start + uiMenuEnterMS/2)
	settled := renderMenu(start + uiMenuEnterMS + 1)

	// Pick a point well inside the card's resting body.
	menuX, menuY := startMenuOrigin()
	px, py := menuX+60, menuY+260
	if !closed.isBG(px, py) {
		t.Errorf("menu painted at t=0: pixel %+v, want the background", closed.at(px, py))
	}
	if settled.isBG(px, py) {
		t.Fatalf("menu missing once settled: pixel %+v", settled.at(px, py))
	}
	if mid.at(px, py) == settled.at(px, py) {
		t.Errorf("menu did not fade: halfway pixel equals the settled pixel %+v", mid.at(px, py))
	}

	// The entrance is only visual: the clickable card sits at its final origin.
	wantX, wantY := startMenuOrigin()
	tb.enter.begin(start, uiMenuEnterMS)
	if x, y := startMenuOrigin(); x != wantX || y != wantY {
		t.Errorf("menu origin moved during the entrance: (%d,%d), want (%d,%d)", x, y, wantX, wantY)
	}
}

// Reduced motion snaps the menu in place instead of playing the entrance.
func TestStartMenuEntranceHonoursReducedMotion(t *testing.T) {
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	prevOpen, prevEnter, prevRect := tb.startOpen, tb.enter, tb.startRect
	t.Cleanup(func() {
		tb.startOpen, tb.enter, tb.startRect = prevOpen, prevEnter, prevRect
		ClearTextCache()
	})

	tb.startOpen = true
	tb.startRect = sdl.Rect{X: 20, Y: 12, W: 70, H: 22}
	tb.enter.begin(9000, uiMenuEnterMS)
	config.Variables.ReducedMotion = true

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	withScreen(t, probeW, probeH)
	ClearTextCache()

	previousClock := uiClockMS
	uiClockMS = func() uint64 { return 9000 }
	defer func() { uiClockMS = previousClock }()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderStartMenu(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	menuX, menuY := startMenuOrigin()
	if f.isBG(menuX+60, menuY+260) {
		t.Error("reduced motion should draw the settled menu immediately")
	}
	if !tb.enter.settled() {
		t.Error("reduced motion should cancel the entrance instead of leaving it pending")
	}
}

// The hover lift grows the focused card for drawing only; the hit rect stays
// put and the motion is skipped under reduced motion.
func TestHomeCardHoverLift(t *testing.T) {
	base := sdl.Rect{X: 100, Y: 100, W: 200, H: 100}
	if got := scaleHomeCard(base, 0); got != base {
		t.Errorf("lift 0 changed the rect: %+v", got)
	}
	grown := scaleHomeCard(base, 1)
	if grown.X != base.X-3 || grown.Y != base.Y-3 || grown.W != base.W+6 || grown.H != base.H+6 {
		t.Errorf("lift 1 = %+v, want the rect grown by 3px on every side", grown)
	}

	h := NewHomeLayout(probeW, probeH)
	prevLayout := homeLayout
	homeLayout = h
	t.Cleanup(func() { homeLayout = prevLayout })
	// Hit areas keep their settled geometry; only the drawn rect lifts.
	if r := h.Items[HomeFocusContinue].Rect; r.X != 32 || r.Y != 164 || r.W != 480 {
		t.Errorf("Continue hit rect moved: %+v", r)
	}
	if r := h.Items[HomeFocusFiles].Rect; r.W != 174 {
		t.Errorf("Files hit rect moved: %+v", r)
	}
}

func TestUIMotionGate(t *testing.T) {
	if !uiMotionEnabled(nil) || !uiMotionEnabled(&Config{}) {
		t.Error("motion should default to enabled")
	}
	quiet := &Config{}
	quiet.Variables.ReducedMotion = true
	if uiMotionEnabled(quiet) {
		t.Error("reduced motion must gate the entrance animations")
	}
	saver := &Config{}
	saver.Variables.LowPower = true
	if uiMotionEnabled(saver) {
		t.Error("battery saver must skip the entrance animations")
	}
}
