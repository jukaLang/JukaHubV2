package main

// stickcal.go — the analog sticks' calibration: where each stick rests, how
// far it reaches, and its dead zone.
//
// Handheld sticks are not centred. A TrimUI Smart Pro stick can rest a good
// way off centre, settle somewhere a little different after every release,
// and fall short of the rim in some directions. The firmware's calibration
// app sets the hardware range for every program; this one tunes what JukaHub
// itself does with the values it then gets:
//
//   - the left stick as a D-pad (controller.go handleControllerAxis →
//     stickNavButton),
//   - the virtual cursor's gesture, movement and scrolling (virtualcursor.go),
//   - what counts as "the stick moved" for the screensaver, the frame rate and
//     the button hints (axisDeflectedOn), and the controller test's rings.
//
// Chat's right-stick scroll and JukaLand read the raw values with their own
// thresholds, as before.
//
// Each stick keeps four numbers: its dead zone, its resting centre (X, Y) and
// its reach. Shaping (stickCal.shape) moves the centre to 0, reads anything
// inside the dead zone as exactly 0 (a round zone), and stretches the rest so
// the edge the stick actually reaches reads as full tilt. Stick navigation is
// checked per axis instead, so a slightly diagonal push down a list does not
// also step sideways.
//
// Settings → Controls has the calibration screen (stickcal_ui.go), which
// measures all four, and a dead zone row per stick. A measured calibration and
// a dead zone chosen by hand are stored apart (stickCalKeys, stickDeadzoneKeys):
// the hand-picked dead zone wins until the next calibration replaces it.
//
// Main thread only: the event loop writes stickAxes and the readers run there.

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	stickLeft  = 0
	stickRight = 1

	// stickCalDefaultDZ is the dead zone of a stick nobody calibrated.
	stickCalDefaultDZ = 0.15
	// stickCalMinDZ / stickCalMaxDZ bound a dead zone, measured or chosen.
	stickCalMinDZ = 0.03
	stickCalMaxDZ = 0.6
	// stickCalMaxCentre bounds a stored resting centre: further off than this
	// is a broken stick, not a calibration.
	stickCalMaxCentre = 0.5
	// stickCalMinReach is the shortest stored reach.
	stickCalMinReach = 0.4
	// stickCalMinSpan keeps the stretched range (reach − dead zone) usable.
	stickCalMinSpan = 0.2

	// stickNavThreshold is the share of the travel past the dead zone the left
	// stick must cover, along one axis, to step the focus like the D-pad
	// (stickNavLimit).
	stickNavThreshold = 0.25
	// stickWakeMargin is how far past its dead zone an axis must move to count
	// as input (wake, frame rate, hints); never less than the old fixed
	// threshold (axisWakeThreshold).
	stickWakeMargin = 0.03
)

// stickCalKeys are the Custom keys of the measured calibration, left then
// right: "deadzone,centreX,centreY,reach" (stickCal.String).
var stickCalKeys = [2]string{"stickCalLeft", "stickCalRight"}

// stickDeadzoneKeys are the Custom keys of a dead zone chosen by hand, in
// percent ("12").
var stickDeadzoneKeys = [2]string{"stickDeadzoneLeft", "stickDeadzoneRight"}

// stickNames name the sticks in messages.
var stickNames = [2]string{"Left stick", "Right stick"}

// stickCal is one stick's calibration, in shares of the axis range (-1..1).
type stickCal struct {
	DZ     float64 // dead zone radius around the centre
	CX, CY float64 // where the stick rests
	Reach  float64 // distance from the centre the stick reaches all round
}

// stickCalDefault is an uncalibrated stick.
func stickCalDefault() stickCal { return stickCal{DZ: stickCalDefaultDZ, Reach: 1} }

// sane clamps every field into its bounds, so a hand-edited or half-written
// value can make a stick sluggish but never dead or runaway.
func (c stickCal) sane() stickCal {
	fix := func(v, lo, hi, fallback float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fallback
		}
		return clampFloat(v, lo, hi)
	}
	c.DZ = fix(c.DZ, stickCalMinDZ, stickCalMaxDZ, stickCalDefaultDZ)
	c.CX = fix(c.CX, -stickCalMaxCentre, stickCalMaxCentre, 0)
	c.CY = fix(c.CY, -stickCalMaxCentre, stickCalMaxCentre, 0)
	c.Reach = fix(c.Reach, stickCalMinReach, 1, 1)
	if c.Reach-c.DZ < stickCalMinSpan {
		c.Reach = math.Min(1, c.DZ+stickCalMinSpan)
	}
	return c
}

// String is the stored form.
func (c stickCal) String() string {
	return fmt.Sprintf("%.3f,%.3f,%.3f,%.3f", c.DZ, c.CX, c.CY, c.Reach)
}

// parseStickCal reads the stored form; ok is false for an empty or malformed
// value (the stick then uses the defaults).
func parseStickCal(s string) (stickCal, bool) {
	parts := strings.Split(strings.TrimSpace(s), ",")
	if len(parts) != 4 {
		return stickCal{}, false
	}
	var v [4]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return stickCal{}, false
		}
		v[i] = f
	}
	return stickCal{DZ: v[0], CX: v[1], CY: v[2], Reach: v[3]}.sane(), true
}

// shape applies the calibration to one stick position (x, y in -1..1): the
// result is 0, 0 inside the dead zone and has length 1 at the reach.
func (c stickCal) shape(x, y float64) (float64, float64) {
	x -= c.CX
	y -= c.CY
	m := math.Hypot(x, y)
	if m <= c.DZ || m == 0 {
		return 0, 0
	}
	span := c.Reach - c.DZ
	if span < stickCalMinSpan {
		span = stickCalMinSpan
	}
	t := (m - c.DZ) / span
	if t > 1 {
		t = 1
	}
	return x / m * t, y / m * t
}

// stickCustomText reads a Custom value as text without fmt for the common
// cases: the readers below run on every stick event.
func stickCustomText(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return customStringValue(m, key)
}

// stickCalCache keeps the parsed calibration per stick; it is re-read only
// when the stored text changes (a save, a reset, a reloaded config).
var stickCalCache [2]struct {
	raw, rawDZ string
	cal        stickCal
	measured   bool
	manual     bool
	ok         bool
}

// stickCalEntry is a stick's calibration, whether it was measured and whether
// its dead zone was chosen by hand.
func stickCalEntry(config *Config, stick int) (cal stickCal, measured, manual bool) {
	if stick != stickLeft && stick != stickRight {
		return stickCalDefault(), false, false
	}
	raw, rawDZ := "", ""
	if config != nil {
		raw = stickCustomText(config.Variables.Custom, stickCalKeys[stick])
		rawDZ = stickCustomText(config.Variables.Custom, stickDeadzoneKeys[stick])
	}
	e := &stickCalCache[stick]
	if !e.ok || e.raw != raw || e.rawDZ != rawDZ {
		c, ok := parseStickCal(raw)
		if !ok {
			c = stickCalDefault()
		}
		hand := false
		if pct, err := strconv.ParseFloat(strings.TrimSpace(rawDZ), 64); err == nil {
			c.DZ = pct / 100
			c = c.sane()
			hand = true
		}
		e.raw, e.rawDZ, e.cal, e.measured, e.manual, e.ok = raw, rawDZ, c, ok, hand, true
	}
	return e.cal, e.measured, e.manual
}

// stickCalFor is a stick's calibration (the defaults when none is stored).
func stickCalFor(config *Config, stick int) stickCal {
	cal, _, _ := stickCalEntry(config, stick)
	return cal
}

// stickCalStored reports whether a stick has a measured calibration.
func stickCalStored(config *Config, stick int) bool {
	_, measured, _ := stickCalEntry(config, stick)
	return measured
}

// stickCalCustom is the Custom map, created when missing.
func stickCalCustom(config *Config) map[string]interface{} {
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	return config.Variables.Custom
}

// stickCalStore saves a stick's measured calibration; its dead zone replaces
// one chosen by hand.
func stickCalStore(config *Config, stick int, cal stickCal) {
	if config == nil || (stick != stickLeft && stick != stickRight) {
		return
	}
	m := stickCalCustom(config)
	m[stickCalKeys[stick]] = cal.sane().String()
	delete(m, stickDeadzoneKeys[stick])
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// stickDeadzoneSet saves a dead zone chosen by hand, in percent.
func stickDeadzoneSet(config *Config, stick, pct int) {
	if config == nil || (stick != stickLeft && stick != stickRight) {
		return
	}
	stickCalCustom(config)[stickDeadzoneKeys[stick]] = strconv.Itoa(pct)
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// stickCalReset forgets both sticks' calibration and hand-picked dead zones.
func stickCalReset(config *Config) {
	if config == nil || config.Variables.Custom == nil {
		return
	}
	for s := 0; s < 2; s++ {
		delete(config.Variables.Custom, stickCalKeys[s])
		delete(config.Variables.Custom, stickDeadzoneKeys[s])
	}
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// ── Live stick values ──────────────────────────────────────────────────────

// stickAxes is the latest value of each stick axis (LX, LY, RX, RY), -1..1,
// of the pad JukaHub runs on, as its axis events report them. Events only
// come when a value changes, so a steady tilt has to be read from here.
var stickAxes [4]float64

// stickNorm turns a raw axis value into -1..1.
func stickNorm(v int16) float64 {
	f := float64(v) / 32767
	if f < -1 {
		f = -1
	}
	return f
}

// stickNoteAxis records one axis sample. main.go calls it for every axis
// event, including the ones the wake filter and modal screens swallow, so a
// stick that came back to rest meanwhile is never left "held" here. A Local
// play guest's pad has sticks of its own: they are not this pad's.
func stickNoteAxis(e *sdl.ControllerAxisEvent) {
	if e == nil || int(e.Axis) >= len(stickAxes) {
		return
	}
	if primary := hotseatPrimaryID(); primary >= 0 && e.Which != primary {
		return
	}
	stickAxes[e.Axis] = stickNorm(e.Value)
	stickCalSampleEvent()
}

// stickLiveAxes reads the pad's sticks as they are now, falling back to the
// event values when no controller is open.
func stickLiveAxes() [4]float64 {
	if gameController == nil {
		return stickAxes
	}
	var out [4]float64
	for a := range out {
		out[a] = stickNorm(gameController.Axis(sdl.GameControllerAxis(a)))
	}
	return out
}

// stickResetAxes re-reads the sticks after the event loop could not follow
// them (a controller came or went, or the loop was away): from the pad when
// one is open, else as resting where their calibration says. Zeroing them
// would make a stick calibrated off centre read as tilted.
func stickResetAxes() {
	if gameController != nil {
		stickAxes = stickLiveAxes()
		return
	}
	for s := 0; s < 2; s++ {
		cal := stickCalFor(appConfig, s)
		stickAxes[2*s], stickAxes[2*s+1] = cal.CX, cal.CY
	}
}

// stickShaped is a stick's current position with its calibration applied.
func stickShaped(config *Config, stick int) (float64, float64) {
	if stick != stickLeft && stick != stickRight {
		return 0, 0
	}
	return stickCalFor(config, stick).shape(stickAxes[2*stick], stickAxes[2*stick+1])
}

// stickNavLimit is how far from where it rests, along one axis, the left
// stick must go to step the focus: the dead zone plus a quarter of the travel
// beyond it. With the defaults that is the old fixed threshold (0.3625 of the
// travel; 12000 / 32767 ≈ 0.366 was), and each axis is checked on its own, as
// before, so the sideways tolerance of a push down a list is unchanged.
func stickNavLimit(cal stickCal) float64 {
	return cal.DZ + stickNavThreshold*(cal.Reach-cal.DZ)
}

// stickNavButton is the D-pad direction one left-stick axis value points to,
// measured from where the stick rests, past stickNavLimit.
func stickNavButton(config *Config, axis uint8, value int16) (uint8, bool) {
	if axis != sdl.CONTROLLER_AXIS_LEFTX && axis != sdl.CONTROLLER_AXIS_LEFTY {
		return 0, false
	}
	cal := stickCalFor(config, stickLeft)
	centre := cal.CX
	if axis == sdl.CONTROLLER_AXIS_LEFTY {
		centre = cal.CY
	}
	d := stickNorm(value) - centre
	limit := stickNavLimit(cal)
	switch {
	case d > limit && axis == sdl.CONTROLLER_AXIS_LEFTX:
		return sdl.CONTROLLER_BUTTON_DPAD_RIGHT, true
	case d < -limit && axis == sdl.CONTROLLER_AXIS_LEFTX:
		return sdl.CONTROLLER_BUTTON_DPAD_LEFT, true
	case d > limit:
		return sdl.CONTROLLER_BUTTON_DPAD_DOWN, true
	case d < -limit:
		return sdl.CONTROLLER_BUTTON_DPAD_UP, true
	}
	return 0, false
}

// axisDeflectedOn reports whether one axis value is deliberate input: a stick
// axis past its dead zone measured from where it rests (and never closer than
// the old fixed threshold), a trigger past the fixed threshold
// (axisDeflected). A stick resting off centre used to count as held for ever
// — the screen never slept and the frame rate never dropped — until its value
// happened to change.
func axisDeflectedOn(axis uint8, v int16) bool {
	if int(axis) >= len(stickAxes) {
		return axisDeflected(v)
	}
	cal := stickCalFor(appConfig, int(axis)/2)
	centre := cal.CX
	if axis%2 == 1 {
		centre = cal.CY
	}
	limit := math.Max(cal.DZ+stickWakeMargin, float64(axisWakeThreshold)/32767)
	return math.Abs(stickNorm(v)-centre) > limit
}

// stickAxisCentre is where the calibration says a stick axis rests (0 for a
// trigger).
func stickAxisCentre(config *Config, axis int) float64 {
	if axis < 0 || axis >= len(stickAxes) {
		return 0
	}
	cal := stickCalFor(config, axis/2)
	if axis%2 == 1 {
		return cal.CY
	}
	return cal.CX
}

// ── Settings → Controls ────────────────────────────────────────────────────

// stickDeadzoneStops are the dead zone row's stops, in percent.
var stickDeadzoneStops = []int{5, 8, 10, 12, 15, 18, 20, 25, 30, 40, 50}

// stickPercent prints a share as a whole percentage.
func stickPercent(v float64) string {
	return strconv.Itoa(int(math.Round(v*100))) + "%"
}

// stickDeadzonePercent is a dead zone as a whole percentage.
func stickDeadzonePercent(dz float64) int { return int(math.Round(dz * 100)) }

// stickDeadzoneOptions are the stops as carousel options, with the dead zone
// in use slotted in when it is not one of them (a measured 7%), so the row
// shows it exactly and steps to its neighbours, not to an end of the list.
func stickDeadzoneOptions(dz float64) []setOption {
	cur := stickDeadzonePercent(dz)
	out := make([]setOption, 0, len(stickDeadzoneStops)+1)
	add := func(p int) {
		s := strconv.Itoa(p)
		out = append(out, setOption{Value: s, Label: s + "%"})
	}
	placed := false
	for _, p := range stickDeadzoneStops {
		if !placed && cur <= p {
			if cur < p {
				add(cur)
			}
			placed = true
		}
		add(p)
	}
	if !placed {
		add(cur)
	}
	return out
}

// stickDeadzoneIndex is the option holding the dead zone in use.
func stickDeadzoneIndex(opts []setOption, dz float64) int {
	want := strconv.Itoa(stickDeadzonePercent(dz))
	for i, o := range opts {
		if o.Value == want {
			return i
		}
	}
	return -1
}

// stickDeadzoneRow is the dead zone choice of one stick. The row shows the
// dead zone in use ("7%" when measured) and steps to the stops around it.
func stickDeadzoneRow(stick int) setRow {
	label := stickNames[stick] + " dead zone"
	return setRow{
		ID: stickDeadzoneKeys[stick], Label: label, Kind: setKindChoice,
		Detail: func(c *Config) string {
			cal, measured, manual := stickCalEntry(c, stick)
			switch {
			case manual:
				return "Set by hand · Calibrate sticks measures it"
			case measured:
				return fmt.Sprintf("Calibrated · rests at %+.2f %+.2f · reaches %s", cal.CX, cal.CY, stickPercent(cal.Reach))
			}
			return "How far the stick moves before JukaHub reacts"
		},
		Options: func(c *Config) []setOption { return stickDeadzoneOptions(stickCalFor(c, stick).DZ) },
		Current: func(c *Config) int {
			dz := stickCalFor(c, stick).DZ
			return stickDeadzoneIndex(stickDeadzoneOptions(dz), dz)
		},
		Value: func(c *Config) string { return stickPercent(stickCalFor(c, stick).DZ) },
		Choose: func(c *Config, value string) {
			pct, err := strconv.Atoi(value)
			if err != nil {
				return
			}
			stickDeadzoneSet(c, stick, pct)
			setToast(label + ": " + stickPercent(stickCalFor(c, stick).DZ))
		},
	}
}

// stickCalStatusText is the Calibrate row's read-out.
func stickCalStatusText(c *Config) string {
	l, r := stickCalStored(c, stickLeft), stickCalStored(c, stickRight)
	switch {
	case l && r:
		return "Calibrated"
	case l:
		return "Left only"
	case r:
		return "Right only"
	}
	return "Not yet"
}

// stickCalSettingsRows are the stick rows of Settings → Controls. A device
// without sticks (the Brick family) has none of them.
func stickCalSettingsRows() []setRow {
	hasSticks := func(*Config) bool { return stickHardware() }
	rows := []setRow{
		{
			// Moving both sticks at once brings up a pointer: left stick
			// moves, right stick scrolls, A clicks (virtualcursor.go).
			ID: vcursorKey, Label: "Virtual cursor", Kind: setKindToggle,
			Detail:  setStatic("Move both sticks at once to show a pointer; A clicks"),
			Visible: hasSticks,
			Get:     vcursorEnabled,
			Set: func(c *Config, on bool) {
				vcursorSetEnabled(c, on)
				setToast("Virtual cursor: " + setOnOff(on))
			},
		},
		{
			ID: "stickCalibrate", Label: "Calibrate sticks", Kind: setKindAction,
			Detail:  setStatic("Measure where both sticks rest and how far they reach"),
			Visible: hasSticks,
			Value:   stickCalStatusText,
			Open:    stickCalOpen,
		},
		stickDeadzoneRow(stickLeft),
		stickDeadzoneRow(stickRight),
	}
	rows[2].Visible, rows[3].Visible = hasSticks, hasSticks
	return rows
}
