package main

import (
	"math"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestStickCalParseAndSane(t *testing.T) {
	cal, ok := parseStickCal("0.120, 0.050,-0.020,0.930")
	if !ok || cal.DZ != 0.12 || cal.CX != 0.05 || cal.CY != -0.02 || cal.Reach != 0.93 {
		t.Fatalf("parse = %+v ok=%v", cal, ok)
	}
	if back, ok := parseStickCal(cal.String()); !ok || back != cal {
		t.Errorf("round trip: %q → %+v", cal.String(), back)
	}
	for _, bad := range []string{"", "0.1,0.2,0.3", "a,b,c,d", "0.1,0.2,0.3,0.4,0.5"} {
		if _, ok := parseStickCal(bad); ok {
			t.Errorf("parsed malformed %q", bad)
		}
	}
	// Out-of-range values are clamped, never trusted.
	wild, _ := parseStickCal("5,-3,NaN,0.1")
	if wild.DZ != stickCalMaxDZ || wild.CX != -stickCalMaxCentre || wild.CY != 0 {
		t.Errorf("wild values not clamped: %+v", wild)
	}
	if wild.Reach-wild.DZ < stickCalMinSpan-1e-9 {
		t.Errorf("no usable range left: %+v", wild)
	}
}

func TestStickCalShape(t *testing.T) {
	def := stickCalDefault()
	if x, y := def.shape(0.1, 0.05); x != 0 || y != 0 {
		t.Errorf("inside the dead zone = %v, %v", x, y)
	}
	if x, y := def.shape(1, 0); math.Abs(x-1) > 1e-9 || y != 0 {
		t.Errorf("full tilt = %v, %v; want 1, 0", x, y)
	}
	// A stick resting off centre reads 0 there, and its reach is full tilt.
	cal := stickCal{DZ: 0.1, CX: 0.2, CY: -0.1, Reach: 0.7}
	if x, y := cal.shape(0.2, -0.1); x != 0 || y != 0 {
		t.Errorf("at its resting spot = %v, %v", x, y)
	}
	if x, y := cal.shape(0.2+0.7, -0.1); math.Abs(x-1) > 1e-9 || math.Abs(y) > 1e-9 {
		t.Errorf("at its reach = %v, %v; want 1, 0", x, y)
	}
	if x, _ := cal.shape(1, -0.1); x > 1+1e-9 {
		t.Errorf("past the reach = %v, want at most 1", x)
	}
	// The zone is round: a diagonal inside it is still nothing.
	if x, y := def.shape(0.1, 0.1); x != 0 || y != 0 {
		t.Errorf("diagonal inside the zone = %v, %v", x, y)
	}
}

func TestStickCalLookupCache(t *testing.T) {
	c := &Config{}
	if stickCalStored(c, stickLeft) || stickCalFor(c, stickLeft) != stickCalDefault() {
		t.Error("an empty config is not the defaults")
	}
	c.Variables.Custom = map[string]interface{}{stickCalKeys[stickLeft]: "0.080,0.010,0.020,0.950"}
	if got := stickCalFor(c, stickLeft); got.DZ != 0.08 || !stickCalStored(c, stickLeft) {
		t.Errorf("stored value not read: %+v", got)
	}
	// A changed value is picked up (the cache is keyed on the text).
	c.Variables.Custom[stickCalKeys[stickLeft]] = "0.200,0,0,1"
	if got := stickCalFor(c, stickLeft); got.DZ != 0.2 {
		t.Errorf("changed value not re-read: %+v", got)
	}
	if stickCalStored(c, stickRight) {
		t.Error("the right stick borrowed the left one's calibration")
	}
	if stickCalFor(c, 7) != stickCalDefault() {
		t.Error("an unknown stick is not the defaults")
	}
}

// TestStickNavButtonDefault: with no calibration the left stick steps the
// focus where the old fixed threshold (12000) did, give or take a little.
func TestStickNavButtonDefault(t *testing.T) {
	old := stickAxes
	defer func() { stickAxes = old }()
	stickAxes = [4]float64{}
	config := &Config{}
	if _, ok := stickNavButton(config, sdl.CONTROLLER_AXIS_LEFTX, 11000); ok {
		t.Error("a third of the travel stepped the focus")
	}
	if btn, ok := stickNavButton(config, sdl.CONTROLLER_AXIS_LEFTX, 13000); !ok || btn != sdl.CONTROLLER_BUTTON_DPAD_RIGHT {
		t.Errorf("0.4 right = %v %v, want D-pad right", btn, ok)
	}
	stickAxes = [4]float64{}
	if btn, ok := stickNavButton(config, sdl.CONTROLLER_AXIS_LEFTY, -20000); !ok || btn != sdl.CONTROLLER_BUTTON_DPAD_UP {
		t.Errorf("stick up = %v %v, want D-pad up", btn, ok)
	}
	if _, ok := stickNavButton(config, sdl.CONTROLLER_AXIS_RIGHTX, 32000); ok {
		t.Error("the right stick stepped the focus")
	}
	// Each axis on its own: a push down a list leaning 0.3 sideways does not
	// step sideways (the old per-axis tolerance).
	if _, ok := stickNavButton(config, sdl.CONTROLLER_AXIS_LEFTX, 9830); ok {
		t.Error("a 0.3 sideways lean stepped sideways")
	}
	// A stick calibrated to rest a third of the way right does not step.
	config.Variables.Custom = map[string]interface{}{stickCalKeys[stickLeft]: "0.080,0.330,0.000,1.000"}
	stickAxes = [4]float64{}
	if _, ok := stickNavButton(config, sdl.CONTROLLER_AXIS_LEFTX, 11000); ok {
		t.Error("a calibrated drifting stick at rest stepped the focus")
	}
}

func TestAxisDeflectedOnCalibration(t *testing.T) {
	old := appConfig
	defer func() { appConfig = old }()
	appConfig = &Config{}
	if axisDeflectedOn(sdl.CONTROLLER_AXIS_LEFTX, 5000) {
		t.Error("0.15 counted as input with the defaults")
	}
	if !axisDeflectedOn(sdl.CONTROLLER_AXIS_LEFTX, 7000) {
		t.Error("0.21 did not count as input with the defaults")
	}
	appConfig.Variables.Custom = map[string]interface{}{stickCalKeys[stickRight]: "0.080,0.000,0.300,1.000"}
	if axisDeflectedOn(sdl.CONTROLLER_AXIS_RIGHTY, 9830) {
		t.Error("a calibrated stick resting at 0.3 counted as input")
	}
	if !axisDeflectedOn(sdl.CONTROLLER_AXIS_RIGHTY, 0) {
		t.Error("0.3 away from its resting spot did not count as input")
	}
	// Triggers keep the fixed threshold.
	if axisDeflectedOn(sdl.CONTROLLER_AXIS_TRIGGERLEFT, 100) || !axisDeflectedOn(sdl.CONTROLLER_AXIS_TRIGGERLEFT, 32000) {
		t.Error("trigger threshold changed")
	}
}

func TestStickCalOctants(t *testing.T) {
	cases := []struct {
		dx, dy float64
		want   int
	}{{-1, 0, 0}, {-0.7, -0.7, 1}, {0, -1, 2}, {0.7, -0.7, 3}, {1, 0, 4}, {0.7, 0.7, 5}, {0, 1, 6}, {-0.7, 0.7, 7}}
	for _, c := range cases {
		if got := stickCalOctantOf(c.dx, c.dy); got != c.want {
			t.Errorf("octant(%v, %v) = %d, want %d", c.dx, c.dy, got, c.want)
		}
	}
}

// stickCalCircle rolls a stick round a circle of radius r around (cx, cy).
func stickCalCircle(roll *stickCalRollStats, r float64, steps int) {
	for i := 0; i < steps; i++ {
		a := 2 * math.Pi * float64(i) / float64(steps)
		roll.add(r*math.Cos(a), r*math.Sin(a))
	}
}

func TestStickCalResult(t *testing.T) {
	var r1, r2 stickCalRest
	r1.add(0.10, 0.00)
	r1.add(0.12, 0.02)
	r2.add(0.14, 0.00)
	r2.add(0.14, 0.01)
	var roll stickCalRollStats
	stickCalCircle(&roll, 0.9, 64)
	cal, note, doubtful := stickCalResult(r1, r2, roll, stickCalDefault())
	if note != "" || doubtful {
		t.Errorf("unexpected note %q (doubtful %v)", note, doubtful)
	}
	if math.Abs(cal.CX-0.125) > 1e-9 || math.Abs(cal.CY-0.0075) > 1e-9 {
		t.Errorf("centre = %v, %v; want the middle of the two rests", cal.CX, cal.CY)
	}
	if math.Abs(cal.Reach-0.9) > 1e-9 {
		t.Errorf("reach = %v, want 0.9", cal.Reach)
	}
	// Both resting spots and their jitter sit inside the dead zone.
	for _, p := range [][2]float64{{0.10, 0}, {0.12, 0.02}, {0.14, 0}, {0.14, 0.01}} {
		if x, y := cal.shape(p[0], p[1]); x != 0 || y != 0 {
			t.Errorf("resting sample %v reads %v, %v", p, x, y)
		}
	}
	if cal.DZ > 0.15 {
		t.Errorf("dead zone %v is far bigger than the measured spread", cal.DZ)
	}

	// Half a roll keeps the old reach and says so.
	var half stickCalRollStats
	for i := 0; i < 16; i++ {
		a := math.Pi * float64(i) / 16
		half.add(0.9*math.Cos(a), 0.9*math.Sin(a))
	}
	prev := stickCal{DZ: 0.12, Reach: 0.8}
	cal, note, doubtful = stickCalResult(r1, r2, half, prev)
	if cal.Reach != 0.8 || note == "" || !doubtful {
		t.Errorf("short roll: reach %v note %q doubtful %v, want 0.8, a note, doubtful", cal.Reach, note, doubtful)
	}
	// Without a roll the two rests say little: the dead zone in use is kept
	// as a minimum.
	if cal.DZ < prev.DZ {
		t.Errorf("short roll shrank the dead zone to %v", cal.DZ)
	}

	// Rests far apart are doubtful.
	var far stickCalRest
	far.add(-0.12, 0.05)
	if _, note, doubtful := stickCalResult(r1, far, roll, stickCalDefault()); note == "" || !doubtful {
		t.Errorf("rests 0.25 apart: note %q doubtful %v", note, doubtful)
	}
}

func TestStickCalRollReach(t *testing.T) {
	// A hurried direction (0.5 against 0.9 elsewhere) does not cut the reach.
	var r stickCalRollStats
	stickCalCircle(&r, 0.9, 64)
	r[2] = 0.65
	if reach, ok := r.reached(); !ok || math.Abs(reach-0.9) > 1e-9 {
		t.Errorf("reach with one short direction = %v %v, want 0.9", reach, ok)
	}
	// Directions under the rim minimum do not count at all.
	var shallow stickCalRollStats
	stickCalCircle(&shallow, 0.5, 64)
	if _, ok := shallow.reached(); ok || shallow.directions() != 0 {
		t.Errorf("a half-depth roll counted: %d directions", shallow.directions())
	}
	// A gate that reaches 0.8 on the diagonals and 1 on the axes: the
	// diagonals set the reach.
	var gate stickCalRollStats
	for o := 0; o < stickCalOctants; o++ {
		gate[o] = 1
		if o%2 == 1 {
			gate[o] = 0.82
		}
	}
	if reach, ok := gate.reached(); !ok || reach != 0.82 {
		t.Errorf("square-ish gate reach = %v %v, want 0.82", reach, ok)
	}
}

func TestStickCalRestTouched(t *testing.T) {
	var still, moved, held stickCalRest
	still.add(0.02, -0.01)
	still.add(0.03, 0.0)
	moved.add(0, 0)
	moved.add(0.4, 0)
	held.add(0.3, 0.05)
	held.add(0.31, 0.05)
	if still.touched() || !moved.touched() || !held.touched() {
		t.Errorf("touched: still %v moved %v held %v", still.touched(), moved.touched(), held.touched())
	}
}

func TestStickDeadzoneByHand(t *testing.T) {
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{
		stickCalKeys[stickLeft]:      "0.080,0.050,0.000,0.900",
		stickDeadzoneKeys[stickLeft]: "20",
	}
	cal, measured, manual := stickCalEntry(c, stickLeft)
	if !measured || !manual || cal.DZ != 0.2 || cal.CX != 0.05 || cal.Reach != 0.9 {
		t.Fatalf("hand-picked dead zone over a calibration = %+v measured %v manual %v", cal, measured, manual)
	}
	// A new calibration replaces the hand-picked dead zone.
	stickCalStore(c, stickLeft, stickCal{DZ: 0.1, CX: 0.01, CY: 0.02, Reach: 0.95})
	if cal, _, manual := stickCalEntry(c, stickLeft); manual || cal.DZ != 0.1 {
		t.Errorf("after a calibration: %+v manual %v", cal, manual)
	}
	// Choosing a stop on an uncalibrated stick is not a calibration.
	stickDeadzoneSet(c, stickRight, 25)
	if cal, measured, manual := stickCalEntry(c, stickRight); measured || !manual || cal.DZ != 0.25 {
		t.Errorf("hand-picked only: %+v measured %v manual %v", cal, measured, manual)
	}
	stickCalReset(c)
	if stickCalStored(c, stickLeft) || stickCalFor(c, stickRight) != stickCalDefault() {
		t.Error("reset left a calibration behind")
	}
}

func TestStickDeadzoneOptions(t *testing.T) {
	opts := stickDeadzoneOptions(0.07)
	i := stickDeadzoneIndex(opts, 0.07)
	if i < 1 || opts[i].Value != "7" || opts[i-1].Value != "5" || opts[i+1].Value != "8" {
		t.Errorf("a measured 7%% is not slotted between 5%% and 8%%: %v", opts)
	}
	if got := stickDeadzoneOptions(0.15); len(got) != len(stickDeadzoneStops) {
		t.Errorf("a stop was duplicated: %v", got)
	}
	if got := stickDeadzoneOptions(0.6); got[len(got)-1].Value != "60" {
		t.Errorf("a dead zone past the last stop is not at the end: %v", got)
	}
}

// TestStickCalFlow walks the screen's steps with a fake clock and fake sticks.
func TestStickCalFlow(t *testing.T) {
	oldClock, oldAxes, oldUI := stickCalClock, stickAxes, stickCalUI
	defer func() { stickCalClock, stickAxes, stickCalUI = oldClock, oldAxes, oldUI }()
	now := time.Unix(5000, 0)
	stickCalClock = func() time.Time { return now }
	config := &Config{}
	step := func(d time.Duration) {
		now = now.Add(d)
		stickCalTick(config, now)
	}

	stickAxes = [4]float64{0.05, 0.01, -0.02, 0.03}
	stickCalBegin(stickCalRest1)
	for i := 0; i < 30; i++ {
		step(80 * time.Millisecond)
	}
	if stickCalUI.step != stickCalRoll {
		t.Fatalf("after the first rest the step is %d, want the roll", stickCalUI.step)
	}
	// Roll both sticks round.
	for i := 0; i < 80; i++ {
		a := 2 * math.Pi * float64(i) / 20
		stickAxes = [4]float64{0.05 + 0.85*math.Cos(a), 0.01 + 0.85*math.Sin(a), -0.02 + 0.9*math.Cos(a), 0.03 + 0.9*math.Sin(a)}
		step(70 * time.Millisecond)
	}
	if stickCalUI.step != stickCalRest2 {
		t.Fatalf("after the roll the step is %d, want the second rest", stickCalUI.step)
	}
	// A thumb still on the left stick starts the rest over.
	stickCalUI.restarted = false
	stickAxes = [4]float64{0.6, 0, -0.02, 0.03}
	step(700 * time.Millisecond)
	stickAxes = [4]float64{0.05, 0.01, -0.02, 0.03}
	step(80 * time.Millisecond)
	if !stickCalUI.restarted || stickCalUI.step != stickCalRest2 {
		t.Fatalf("a touched rest did not start over (restarted=%v step=%d)", stickCalUI.restarted, stickCalUI.step)
	}
	stickAxes = [4]float64{0.06, 0.01, -0.02, 0.04}
	for i := 0; i < 40; i++ {
		step(80 * time.Millisecond)
	}
	if stickCalUI.step != stickCalDone {
		t.Fatalf("after the second rest the step is %d, want the result", stickCalUI.step)
	}
	l, r := stickCalUI.result[stickLeft], stickCalUI.result[stickRight]
	if math.Abs(l.CX-0.055) > 0.02 || math.Abs(r.CY-0.035) > 0.02 {
		t.Errorf("centres = %+v / %+v", l, r)
	}
	if math.Abs(l.Reach-0.85) > 0.05 || math.Abs(r.Reach-0.9) > 0.05 {
		t.Errorf("reaches = %v / %v, want about 0.85 / 0.9", l.Reach, r.Reach)
	}
	if stickCalUI.notes[stickLeft] != "" || stickCalUI.notes[stickRight] != "" {
		t.Errorf("unexpected notes %q", stickCalUI.notes)
	}
}
