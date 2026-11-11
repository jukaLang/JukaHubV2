package main

// stickcal_ui.go — the Stick calibration screen (Settings → Controls →
// Calibrate sticks).
//
// Three short steps, about ten seconds in all, for both sticks at once:
//
//  1. Let go — both sticks untouched: where each one rests and how much it
//     jitters there.
//  2. Roll — both sticks turned round their rims: how far each one reaches in
//     eight directions. The shortest direction that went out to the edge
//     counts, so every direction can reach full tilt.
//  3. Let go again — a stick never settles in exactly the same spot twice;
//     the second rest shows how far apart its resting spots are.
//
// The dead zone is then sized to cover both resting spots and their jitter,
// with a margin, around the middle of the two (stickCalResult). The result is
// drawn with the live sticks moving through it, and saved with A; B cancels.
// A doubtful result (a short roll, a stick resting far off centre, two rests
// far apart) is explained under its stick, and Measure again is focused
// instead of Save. The firmware's own calibration app sets the hardware range
// for every program; this one tunes JukaHub: stick navigation, the virtual
// cursor and what counts as the stick moving (stickcal.go).
//
// A rest step whose values spread too wide, or settle too far off centre, had
// a thumb on a stick: it starts over and says so, a few times at most. While
// the screen is up the sticks move nothing else (virtualcursor.go asks
// stickCalOwnsSticks); a menu or panel opened over it cancels a measurement,
// and so does a controller coming or going.
//
// Pad: D-pad Left/Right choose a button, Up reaches the top bar, A presses, B
// goes back (cancelling a measurement first). Keys: arrows, Enter,
// Esc/Backspace. Mouse and touch: the buttons. The scene hooks (creation,
// header name, footer, clicks, wheel) ride with the other Controls screens in
// inmap_ui.go.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// stickCalScene is the screen's scene name.
const stickCalScene = "StickCalibration"

// stickCalStep is where the screen is.
type stickCalStep int

const (
	stickCalIntro stickCalStep = iota // the current calibration, Start
	stickCalRest1                     // let go
	stickCalRoll                      // roll both sticks round
	stickCalRest2                     // let go again
	stickCalDone                      // the result, Save
)

const (
	// stickCalSettle1 / stickCalSettle2 are the starts of the rest steps that
	// are not measured: the thumb is still leaving the stick (longer after the
	// roll, where it was busy).
	stickCalSettle1 = 600 * time.Millisecond
	stickCalSettle2 = time.Second
	// stickCalRestTime is how long a rest step measures.
	stickCalRestTime = 1500 * time.Millisecond
	// stickCalRollTime is how long the roll lasts.
	stickCalRollTime = 5 * time.Second
	// stickCalTouched is the spread of a rest step's values, and
	// stickCalRestOff the distance of its centre from the middle, that mean a
	// thumb was on a stick: the step starts over.
	stickCalTouched = 0.25
	stickCalRestOff = 0.25
	// stickCalMaxRestarts is how often a rest step starts over before the
	// measurement gives up.
	stickCalMaxRestarts = 4
	// stickCalOctants is how many directions the roll tracks, and
	// stickCalMinOctants how many must go out to the edge.
	stickCalOctants    = 8
	stickCalMinOctants = 6
	// stickCalRimMin is how far out a direction must go to count as rolled.
	stickCalRimMin = 0.6
	// stickCalDZFloor is the smallest measured dead zone: two rests cannot see
	// every spot a stick may settle in.
	stickCalDZFloor = 0.08
	// stickCalOffCentre and stickCalRestsApart make a result doubtful.
	stickCalOffCentre  = 0.15
	stickCalRestsApart = 0.15
)

// stickCalRest is one rest step's samples of one stick: the box they fall in.
type stickCalRest struct {
	minX, maxX, minY, maxY float64
	n                      int
}

func (r *stickCalRest) add(x, y float64) {
	if r.n == 0 {
		r.minX, r.maxX, r.minY, r.maxY = x, x, y, y
	} else {
		r.minX, r.maxX = math.Min(r.minX, x), math.Max(r.maxX, x)
		r.minY, r.maxY = math.Min(r.minY, y), math.Max(r.maxY, y)
	}
	r.n++
}

// centre is the middle of the box.
func (r stickCalRest) centre() (float64, float64) {
	return (r.minX + r.maxX) / 2, (r.minY + r.maxY) / 2
}

// noise is how far a sample can sit from the centre: half the box diagonal.
func (r stickCalRest) noise() float64 {
	return math.Hypot((r.maxX-r.minX)/2, (r.maxY-r.minY)/2)
}

// spread is the box's longer side.
func (r stickCalRest) spread() float64 {
	return math.Max(r.maxX-r.minX, r.maxY-r.minY)
}

// touched reports a rest that cannot be a stick left alone: its values moved
// about, or settled far from the middle.
func (r stickCalRest) touched() bool {
	if r.n == 0 {
		return false
	}
	cx, cy := r.centre()
	return r.spread() > stickCalTouched || math.Hypot(cx, cy) > stickCalRestOff
}

// stickCalRollStats is the roll of one stick: the furthest distance from the
// rest centre reached in each of eight directions (0 = left, then clockwise
// on screen: up-left, up, …). The length is stickCalOctants, spelled out: the
// offline checker reads "[name]T" in a type declaration as type parameters.
type stickCalRollStats [8]float64

func (r *stickCalRollStats) add(dx, dy float64) {
	m := math.Hypot(dx, dy)
	if m < stickCalRimMin {
		return
	}
	o := stickCalOctantOf(dx, dy)
	if m > r[o] {
		r[o] = m
	}
}

// stickCalOctantOf is the direction a vector points in.
func stickCalOctantOf(dx, dy float64) int {
	a := math.Atan2(dy, dx) // -π..π, 0 = right, π/2 = down (SDL's Y grows down)
	return int(math.Floor((a+math.Pi)/(2*math.Pi)*stickCalOctants+0.5)) % stickCalOctants
}

// directions is how many directions went out to the edge.
func (r stickCalRollStats) directions() int {
	n := 0
	for _, m := range r {
		if m > 0 {
			n++
		}
	}
	return n
}

// reached is the stick's reach: the shortest of the directions that went out
// to the edge, leaving out one rolled clearly shorter than the rest (under
// 80% of the median), which is a hurried thumb rather than the stick. ok is
// false when too few directions remain.
func (r stickCalRollStats) reached() (float64, bool) {
	var ms []float64
	for _, m := range r {
		if m > 0 {
			ms = append(ms, m)
		}
	}
	if len(ms) < stickCalMinOctants {
		return 0, false
	}
	sort.Float64s(ms)
	median := ms[len(ms)/2]
	shortest, n := math.Inf(1), 0
	for _, m := range ms {
		if m >= 0.8*median {
			n++
			shortest = math.Min(shortest, m)
		}
	}
	if n < stickCalMinOctants {
		return 0, false
	}
	return math.Min(shortest, 1), true
}

// stickCalResult turns one stick's measurements into its calibration. note
// says anything worth knowing; doubtful asks for a second measurement before
// saving. prev is the calibration in use, kept for what was not measured.
func stickCalResult(r1, r2 stickCalRest, roll stickCalRollStats, prev stickCal) (cal stickCal, note string, doubtful bool) {
	c1x, c1y := r1.centre()
	c2x, c2y := r2.centre()
	cal = stickCal{CX: (c1x + c2x) / 2, CY: (c1y + c2y) / 2, Reach: prev.Reach}
	// Every resting spot seen lies within apart/2 + noise of the middle.
	apart := math.Hypot(c1x-c2x, c1y-c2y)
	noise := math.Max(r1.noise(), r2.noise())
	cal.DZ = math.Max((apart/2+noise)*1.3+0.05, stickCalDZFloor)
	reach, rolled := roll.reached()
	if rolled {
		cal.Reach = reach
	} else {
		// Without a roll between the rests their difference says little
		// about where the stick settles: keep at least the dead zone in use.
		cal.DZ = math.Max(cal.DZ, prev.DZ)
	}
	cal = cal.sane()
	switch {
	case !rolled:
		note = "Not rolled out to the edge: reach kept"
	case math.Hypot(cal.CX, cal.CY) > stickCalOffCentre:
		note = "Rests off centre: was a thumb on it?"
	case apart > stickCalRestsApart:
		note = "Rests in different spots: try again"
	case cal.DZ >= 0.3:
		note = "Rests unevenly: a big dead zone keeps it still"
	}
	doubtful = !rolled || apart > stickCalRestsApart || math.Hypot(cal.CX, cal.CY) > stickCalOffCentre
	return cal, note, doubtful
}

// stickCalUI is the screen's state; main thread only.
var stickCalUI struct {
	visit     context.Context
	step      stickCalStep
	stepAt    time.Time // when the step (or its restart) began
	button    int
	restarted bool // a rest step started over because a stick was touched
	restarts  int  // how often the step under way started over
	rest1     [2]stickCalRest
	rest2     [2]stickCalRest
	roll      [2]stickCalRollStats
	result    [2]stickCal
	notes     [2]string
	// The last frame's buttons, for the mouse.
	buttonRects []sdl.Rect
}

// stickCalClock is the screen's clock; tests replace it.
var stickCalClock = time.Now

// The header's icon.
var stickCalHeaderGlyph = func(r *sdl.Renderer, box sdl.Rect, _ sdl.Color) {
	DrawHomeIcon(r, "gamepad", box, false)
}

// stickCalOpen opens the screen (Settings → Controls → Calibrate sticks).
func stickCalOpen(config *Config) {
	if config == nil {
		return
	}
	inmapEnsureScenes(config)
	idx := sceneIndexExact(config, stickCalScene)
	if idx < 0 || idx == currentSceneIndex {
		return
	}
	qs.open = false
	changeSceneTo(config, idx)
}

// stickCalOnScreen reports whether the screen is the scene on display.
func stickCalOnScreen(config *Config) bool {
	return inmapCurrentScene(config) == stickCalScene
}

// stickCalOwnsSticks reports whether the sticks are being measured or shown:
// they must not move the focus or the virtual cursor meanwhile. A menu or a
// panel over the screen gets them back.
func stickCalOwnsSticks(config *Config) bool {
	return stickCalOnScreen(config) && !inmapOverlayOpen()
}

// stickHardware reports whether this device has analog sticks at all: the
// Brick family has none.
func stickHardware() bool {
	switch DetectDevice() {
	case DeviceTrimuiBrick, DeviceTrimuiHammer:
		return false
	}
	return true
}

// stickCalPadPresent reports whether there is a controller to measure.
func stickCalPadPresent() bool {
	return gameController != nil || sdl.NumJoysticks() > 0
}

// stickCalEnsureVisit resets the screen when it is entered.
func stickCalEnsureVisit() {
	ctx := sceneWorkContext()
	if ctx == stickCalUI.visit {
		return
	}
	stickCalUI.visit = ctx
	stickCalUI.step, stickCalUI.button, stickCalUI.restarted = stickCalIntro, 0, false
}

// stickCalMeasuring reports a measuring step.
func stickCalMeasuring() bool {
	switch stickCalUI.step {
	case stickCalRest1, stickCalRoll, stickCalRest2:
		return true
	}
	return false
}

// stickCalBegin enters a step.
func stickCalBegin(step stickCalStep) {
	stickCalUI.step = step
	stickCalUI.stepAt = stickCalClock()
	stickCalUI.restarts = 0
	switch step {
	case stickCalRest1:
		stickCalUI.rest1 = [2]stickCalRest{}
		stickCalUI.rest2 = [2]stickCalRest{}
		stickCalUI.roll = [2]stickCalRollStats{}
	case stickCalRest2:
		stickCalUI.rest2 = [2]stickCalRest{}
	}
}

// stickCalCancel drops a measurement under way, back to the start.
func stickCalCancel() {
	stickCalUI.step, stickCalUI.button, stickCalUI.restarted = stickCalIntro, 0, false
}

// stickCalDeviceChanged answers a controller coming or going: a measurement
// under way would carry on with the wrong pad, or with none.
func stickCalDeviceChanged() {
	if stickCalMeasuring() {
		stickCalCancel()
		ShowToast("Controller changed: measure the sticks again", ToastKindWarning)
	}
}

// stickCalSettle is the unmeasured start of the rest step under way.
func stickCalSettle() time.Duration {
	if stickCalUI.step == stickCalRest2 {
		return stickCalSettle2
	}
	return stickCalSettle1
}

// stickCalSample feeds the sticks' current values into the step under way.
func stickCalSample(now time.Time) {
	step := stickCalUI.step
	switch step {
	case stickCalRoll:
	case stickCalRest1, stickCalRest2:
		if now.Sub(stickCalUI.stepAt) < stickCalSettle() {
			return
		}
	default:
		return
	}
	axes := stickLiveAxes()
	for s := 0; s < 2; s++ {
		x, y := axes[2*s], axes[2*s+1]
		switch step {
		case stickCalRest1:
			stickCalUI.rest1[s].add(x, y)
		case stickCalRoll:
			cx, cy := stickCalUI.rest1[s].centre()
			stickCalUI.roll[s].add(x-cx, y-cy)
		case stickCalRest2:
			stickCalUI.rest2[s].add(x, y)
		}
	}
}

// stickCalSampleEvent is stickNoteAxis's hook: every stick event while the
// screen measures is a sample, so a quick flick between two frames still
// counts in the roll.
func stickCalSampleEvent() {
	if !stickCalMeasuring() || !stickCalOwnsSticks(appConfig) {
		return
	}
	stickCalSample(stickCalClock())
}

// stickCalTick samples once per frame (a stick held still sends no events)
// and moves the steps on.
func stickCalTick(config *Config, now time.Time) {
	if !stickCalMeasuring() {
		return
	}
	// The Start menu or a panel took the pad: the sticks are not being
	// measured any more.
	if inmapOverlayOpen() {
		stickCalCancel()
		return
	}
	stickCalSample(now)
	elapsed := now.Sub(stickCalUI.stepAt)
	switch stickCalUI.step {
	case stickCalRest1, stickCalRest2:
		rest := &stickCalUI.rest1
		if stickCalUI.step == stickCalRest2 {
			rest = &stickCalUI.rest2
		}
		if rest[0].touched() || rest[1].touched() {
			stickCalUI.restarts++
			if stickCalUI.restarts > stickCalMaxRestarts {
				stickCalCancel()
				ShowToast("The sticks never settled: keep both thumbs off them, or check them in the Controller test", ToastKindWarning)
				return
			}
			*rest = [2]stickCalRest{}
			stickCalUI.stepAt = now
			stickCalUI.restarted = true
			return
		}
		if elapsed < stickCalSettle()+stickCalRestTime {
			return
		}
		if stickCalUI.step == stickCalRest1 {
			stickCalUI.restarted = false
			stickCalBegin(stickCalRoll)
			PlayNavSound()
			return
		}
		stickCalFinish(config)
	case stickCalRoll:
		if elapsed >= stickCalRollTime {
			stickCalUI.restarted = false
			stickCalBegin(stickCalRest2)
			PlayNavSound()
		}
	}
}

// stickCalFinish computes both results and shows them. A doubtful result
// focuses Measure again, so a hasty A does not save it.
func stickCalFinish(config *Config) {
	doubt := false
	for s := 0; s < 2; s++ {
		var d bool
		stickCalUI.result[s], stickCalUI.notes[s], d = stickCalResult(stickCalUI.rest1[s], stickCalUI.rest2[s],
			stickCalUI.roll[s], stickCalFor(config, s))
		doubt = doubt || d
	}
	stickCalUI.step, stickCalUI.button, stickCalUI.restarted = stickCalDone, 0, false
	if doubt {
		stickCalUI.button = 1
	}
	PlaySuccessSound()
}

// stickCalStepFraction is how far the step under way is, 0..1.
func stickCalStepFraction(now time.Time) float64 {
	elapsed := now.Sub(stickCalUI.stepAt)
	var f float64
	switch stickCalUI.step {
	case stickCalRest1, stickCalRest2:
		f = float64(elapsed) / float64(stickCalSettle()+stickCalRestTime)
	case stickCalRoll:
		f = float64(elapsed) / float64(stickCalRollTime)
	default:
		return 0
	}
	return clampFloat(f, 0, 1)
}

// ── Actions ──────────────────────────────────────────────────────────────

// stickCalButtons are the buttons of the step on screen.
func stickCalButtons() []string {
	switch stickCalUI.step {
	case stickCalIntro:
		return []string{"Start", "Reset to default"}
	case stickCalDone:
		return []string{"Save", "Measure again"}
	}
	return nil
}

// stickCalStart begins a measurement.
func stickCalStart() {
	if !stickCalPadPresent() {
		ShowToast("Connect a controller to calibrate its sticks", ToastKindWarning)
		return
	}
	PlayActivateSound()
	stickCalUI.restarted = false
	stickCalBegin(stickCalRest1)
}

// stickCalPress presses button i of the step on screen.
func stickCalPress(c *Config, i int) {
	switch stickCalUI.step {
	case stickCalIntro:
		if i == 0 {
			stickCalStart()
			return
		}
		_, ml, hl := stickCalEntry(c, stickLeft)
		_, mr, hr := stickCalEntry(c, stickRight)
		if !ml && !hl && !mr && !hr {
			ShowToast("The sticks already use the defaults", ToastKindInfo)
			return
		}
		stickCalReset(c)
		PlayBackSound()
		ShowToast("Stick calibration reset to the defaults", ToastKindInfo)
	case stickCalDone:
		if i == 0 {
			for s := 0; s < 2; s++ {
				stickCalStore(c, s, stickCalUI.result[s])
			}
			trophyEvent(tkStickCal)
			PlaySuccessSound()
			ShowToast("Stick calibration saved", ToastKindSuccess)
			stickCalUI.step, stickCalUI.button = stickCalIntro, 0
			return
		}
		stickCalStart()
	}
}

// stickCalMove walks the buttons.
func stickCalMove(dir int) {
	n := len(stickCalButtons())
	next := stickCalUI.button + dir
	if n == 0 || next < 0 || next >= n {
		return
	}
	stickCalUI.button = next
	PlayNavSound()
}

// stickCalBack cancels a measurement or a result, or leaves the screen.
func stickCalBack(c *Config) {
	if stickCalUI.step != stickCalIntro {
		stickCalCancel()
		PlayBackSound()
		return
	}
	goBackScene(c)
}

// ── Input ────────────────────────────────────────────────────────────────

func stickCalPad(e *sdl.ControllerButtonEvent, c *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	stickCalEnsureVisit()
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_START, sdl.CONTROLLER_BUTTON_GUIDE:
		return false
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		// Up reaches the top bar, as on the other Controls screens.
		if !stickCalMeasuring() {
			focusEngine.ClearFocus()
			focusChromeBar()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		stickCalMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		stickCalMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		stickCalPress(c, stickCalUI.button)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		stickCalBack(c)
	}
	return true
}

func stickCalKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	stickCalEnsureVisit()
	switch e.Keysym.Sym {
	case sdl.K_LEFT:
		stickCalMove(-1)
	case sdl.K_RIGHT, sdl.K_TAB:
		stickCalMove(1)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		if e.Repeat == 0 {
			stickCalPress(c, stickCalUI.button)
		}
	case sdl.K_ESCAPE, sdl.K_BACKSPACE:
		stickCalBack(c)
	default:
		return false
	}
	return true
}

// stickCalClick handles a click or a tap (inmap_ui.go routes it).
func stickCalClick(mx, my int32, c *Config) bool {
	stickCalEnsureVisit()
	for i, r := range stickCalUI.buttonRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			stickCalUI.button = i
			stickCalPress(c, i)
			return true
		}
	}
	return false
}

// stickCalHints is the footer.
func stickCalHints() []FooterHint {
	if stickCalMeasuring() {
		return []FooterHint{{Button: "B", Label: "Cancel"}}
	}
	back := "Back"
	if stickCalUI.step == stickCalDone {
		back = "Cancel"
	}
	return []FooterHint{{Button: "D-Pad", Label: "Choose"}, {Button: "A", Label: "Press"}, {Button: "B", Label: back}}
}

// ── Drawing ──────────────────────────────────────────────────────────────

// stickCalInstruction is the header's second line: what to do now.
func stickCalInstruction() string {
	switch stickCalUI.step {
	case stickCalRest1:
		if stickCalUI.restarted {
			return "Keep both thumbs off the sticks…"
		}
		return "Let go of both sticks…"
	case stickCalRoll:
		return "Roll both sticks slowly round their edges, all the way round"
	case stickCalRest2:
		if stickCalUI.restarted {
			return "Let go of both sticks: keep both thumbs off them…"
		}
		return "Let go of both sticks again…"
	case stickCalDone:
		return "Resting sticks should sit inside their rings. Save to use it"
	}
	if !stickCalPadPresent() {
		return "Connect a controller to calibrate its sticks"
	}
	return "About ten seconds: let go, roll both sticks round, let go again"
}

// stickCalStepLabel names the measuring step over the progress bar.
func stickCalStepLabel() string {
	switch stickCalUI.step {
	case stickCalRest1:
		return "Step 1 of 3 · Let go"
	case stickCalRoll:
		return "Step 2 of 3 · Roll"
	case stickCalRest2:
		return "Step 3 of 3 · Let go again"
	}
	return ""
}

// stickCalShown is the calibration a stick's card draws: the fresh result on
// the result step, the one in use otherwise.
func stickCalShown(c *Config, stick int) stickCal {
	if stickCalUI.step == stickCalDone {
		return stickCalUI.result[stick]
	}
	return stickCalFor(c, stick)
}

// stickCalCardLines are the two lines under a stick's box, and whether the
// second one is a warning.
func stickCalCardLines(c *Config, stick int, axes [4]float64) (string, string, bool) {
	cal := stickCalShown(c, stick)
	x, y := axes[2*stick], axes[2*stick+1]
	switch stickCalUI.step {
	case stickCalRest1, stickCalRest2:
		rest := stickCalUI.rest1[stick]
		if stickCalUI.step == stickCalRest2 {
			rest = stickCalUI.rest2[stick]
		}
		jitter := "measuring…"
		if rest.n > 0 {
			jitter = fmt.Sprintf("jitter %.2f", rest.noise())
		}
		return fmt.Sprintf("%+.2f  %+.2f", x, y), jitter, false
	case stickCalRoll:
		dirs := stickCalUI.roll[stick].directions()
		return fmt.Sprintf("%+.2f  %+.2f", x, y), fmt.Sprintf("%d of %d directions out to the edge", dirs, stickCalOctants), false
	}
	first := "Dead zone " + stickPercent(cal.DZ) + fmt.Sprintf(" · rests at %+.2f %+.2f", cal.CX, cal.CY)
	if stickCalUI.step == stickCalDone {
		if note := stickCalUI.notes[stick]; note != "" {
			return first, note, true
		}
		return first, "Reaches " + stickPercent(cal.Reach) + " in every direction", false
	}
	_, measured, manual := stickCalEntry(c, stick)
	switch {
	case measured && manual:
		return first, "Dead zone set by hand · reaches " + stickPercent(cal.Reach), false
	case measured:
		return first, "Reaches " + stickPercent(cal.Reach), false
	case manual:
		return "Dead zone " + stickPercent(cal.DZ) + " (set by hand)", "Not calibrated", false
	}
	return "Dead zone " + stickPercent(cal.DZ) + " (default)", "Not calibrated", false
}

// stickCalRender draws the screen.
func stickCalRender(renderer *sdl.Renderer, c *Config) {
	drawAppBackdrop(renderer, c, screenWidth, screenHeight)
	stickCalEnsureVisit()
	now := stickCalClock()
	stickCalTick(c, now)
	title := FontForRole(c, TypeBody)
	small := FontForRole(c, TypeLabel)
	if title == nil || small == nil {
		renderStatusBar(renderer, c)
		renderFooter(renderer, c)
		return
	}
	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + scaleY(8, screenHeight)
	bottom := screenHeight - bottomBarHeight(stickCalScene) - 16
	header := UIPageHeader{
		Title:     "Stick calibration",
		Subtitle:  stickCalInstruction(),
		TitleSize: menuTitleSize,
		Glyph:     stickCalHeaderGlyph,
		Status:    stickCalStatusText(c),
		X:         margin,
		Y:         top,
		W:         screenWidth - 2*margin,
	}
	contentY := UIPageHeaderDraw(renderer, c, header) + 16
	body := sdl.Rect{X: margin, Y: contentY, W: screenWidth - 2*margin, H: bottom - contentY}

	sh := int32(small.Height())
	bandH := sh + 22
	if bandH < 44 {
		bandH = 44
	}
	const gap = int32(24)
	cardH := body.H - bandH - 16
	cardW := (body.W - gap) / 2
	axes := stickLiveAxes()
	for s := 0; s < 2; s++ {
		r := sdl.Rect{X: body.X + int32(s)*(cardW+gap), Y: body.Y, W: cardW, H: cardH}
		stickCalDrawCard(renderer, c, title, small, r, s, axes)
	}

	y := body.Y + cardH + 16
	stickCalUI.buttonRects = stickCalUI.buttonRects[:0]
	if stickCalMeasuring() {
		w := body.W / 2
		x := body.X + (body.W-w)/2
		drawText(renderer, small, stickCalStepLabel(), body.X+body.W/2, y, ColorTextSecondary(), textAlignCenter)
		barY := y + sh + 8
		fillRoundedRect(renderer, x, barY, w, 8, 4, glassA(36))
		if f := stickCalStepFraction(now); f > 0 {
			fillRoundedRect(renderer, x, barY, int32(float64(w)*f), 8, 4, accentColor)
		}
	} else {
		labels := stickCalButtons()
		n := int32(len(labels))
		if n == 0 {
			n = 1
		}
		btnW := (body.W - gap*(n-1)) / n
		if btnW > 300 {
			btnW = 300
		}
		x := body.X + (body.W-(btnW*n+gap*(n-1)))/2
		chrome := chromeTier(focusEngine.ElementIndex()) != 0
		for i, label := range labels {
			r := sdl.Rect{X: x + int32(i)*(btnW+gap), Y: y, W: btnW, H: bandH}
			style := UIButtonStyle{Font: title}
			if i == 0 {
				style.Variant = UIButtonPrimary
			}
			UIButton(renderer, c, r, label, style, UIButtonState{
				Focused:  stickCalUI.button == i && !chrome,
				Hovered:  pointInRect(mouseX, mouseY, r),
				Disabled: i == 0 && stickCalUI.step == stickCalIntro && !stickCalPadPresent(),
			})
			stickCalUI.buttonRects = append(stickCalUI.buttonRects, r)
		}
	}
	renderStatusBar(renderer, c)
	renderFooter(renderer, c)
}

// stickCalDrawCard draws one stick's card: its name, the live box and the
// numbers under it.
func stickCalDrawCard(renderer *sdl.Renderer, c *Config, title, small *ttf.Font, r sdl.Rect, stick int, axes [4]float64) {
	drawGlassSurface(renderer, r, glassControlRadius, glassState{NoLift: true})
	th, sh := int32(title.Height()), int32(small.Height())
	drawText(renderer, title, stickNames[stick], r.X+18, r.Y+14, ColorTextPrimary(), textAlignLeft)
	textH := 2*(sh+4) + 10
	size := min32(r.W-36, r.H-(14+th+12)-textH-8)
	if size < 60 {
		return
	}
	box := sdl.Rect{X: r.X + (r.W-size)/2, Y: r.Y + 14 + th + 12, W: size, H: size}
	stickCalDrawBox(renderer, box, stick, stickCalShown(c, stick), axes)
	first, second, warn := stickCalCardLines(c, stick, axes)
	cx := r.X + r.W/2
	ty := box.Y + box.H + 10
	drawText(renderer, small, ellipsize(small, first, r.W-24), cx, ty, ColorTextSecondary(), textAlignCenter)
	if second != "" {
		col := ColorTextTertiary()
		if warn {
			col = ColorToastWarn
		}
		drawText(renderer, small, ellipsize(small, second, r.W-24), cx, ty+sh+4, col, textAlignCenter)
	}
}

// stickCalDrawBox draws a stick's travel: the rim, the reach and dead zone
// rings around where it rests, the directions the roll took out to the edge,
// and the live dot (dim while inside the dead zone).
func stickCalDrawBox(renderer *sdl.Renderer, box sdl.Rect, stick int, cal stickCal, axes [4]float64) {
	fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 14, WithAlpha(ColorBorderDefault, 26))
	strokeRoundedRect(renderer, box.X, box.Y, box.W, box.H, 14, 1, ColorBorderDefault)
	cx, cy := box.X+box.W/2, box.Y+box.H/2
	radius := float64(box.W/2 - 12)
	_ = renderer.SetDrawColor(ColorBorderDefault.R, ColorBorderDefault.G, ColorBorderDefault.B, 90)
	_ = renderer.DrawLine(box.X+10, cy, box.X+box.W-10, cy)
	_ = renderer.DrawLine(cx, box.Y+10, cx, box.Y+box.H-10)
	strokeCircle(renderer, cx, cy, int32(radius), WithAlpha(ColorBorderDefault, 140))

	ccx, ccy := cx+int32(cal.CX*radius), cy+int32(cal.CY*radius)
	if cal.Reach < 0.995 {
		strokeCircle(renderer, ccx, ccy, int32(cal.Reach*radius), WithAlpha(accentColor, 110))
	}
	dz := int32(cal.DZ * radius)
	fillCircle(renderer, ccx, ccy, dz, WithAlpha(accentColor, 46))
	strokeCircle(renderer, ccx, ccy, dz, WithAlpha(accentColor, 210))

	if stickCalUI.step == stickCalRoll || stickCalUI.step == stickCalDone {
		for o := 0; o < stickCalOctants; o++ {
			a := float64(o)/stickCalOctants*2*math.Pi - math.Pi
			px := cx + int32(math.Cos(a)*radius)
			py := cy + int32(math.Sin(a)*radius)
			col := WithAlpha(ColorTextTertiary(), 120)
			if stickCalUI.roll[stick][o] > 0 {
				col = accentColor
			}
			fillCircle(renderer, px, py, 5, col)
		}
	}

	x, y := axes[2*stick], axes[2*stick+1]
	dot := ColorTextPrimary()
	if sx, sy := cal.shape(x, y); sx == 0 && sy == 0 {
		dot = ColorTextTertiary()
	}
	fillCircle(renderer, cx+int32(x*radius), cy+int32(y*radius), 7, dot)
}

// ── Scene registration ───────────────────────────────────────────────────

type stickCalModule struct{ sceneBase }

func (stickCalModule) render(r *sdl.Renderer, c *Config) bool {
	stickCalRender(r, c)
	return true
}

func (stickCalModule) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return stickCalKey(e, c)
}

func (stickCalModule) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return stickCalPad(e, c)
}

func init() { registerScene(stickCalModule{sceneBase{name: stickCalScene}}) }
