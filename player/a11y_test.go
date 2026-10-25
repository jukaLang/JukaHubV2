package main

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func resetA11yForTest() {
	a11y = a11ySettings{fontScale: 1.0}
	a11yChordReset()
}

func TestA11yScaleFontSize(t *testing.T) {
	defer resetA11yForTest()

	a11y = a11ySettings{fontScale: 1.0}
	if got := a11yScaleFontSize(24); got != 24 {
		t.Fatalf("scale 1.0 changed the size: got %d, want 24", got)
	}

	a11y = a11ySettings{fontScale: 1.3}
	if got := a11yScaleFontSize(20); got != 26 {
		t.Fatalf("1.3 x 20 = %d, want 26", got)
	}

	// A scale must never produce an unreadable face, and an unset scale must
	// behave like 1.0 so a zero-value Config (tests, early startup) is safe.
	a11y = a11ySettings{fontScale: 0.85}
	if got := a11yScaleFontSize(5); got != 8 {
		t.Fatalf("tiny fonts must clamp to 8px, got %d", got)
	}
	a11y = a11ySettings{}
	if got := a11yScaleFontSize(20); got != 20 {
		t.Fatalf("zero scale must not resize text, got %d", got)
	}
}

func TestA11yClampScale(t *testing.T) {
	if got := a11yClampScale(9); got != a11yMaxScale {
		t.Fatalf("clamp high: got %v, want %v", got, a11yMaxScale)
	}
	if got := a11yClampScale(0.1); got != a11yMinScale {
		t.Fatalf("clamp low: got %v, want %v", got, a11yMinScale)
	}
	if got := a11yClampScale(0); got != 1.0 {
		t.Fatalf("zero scale: got %v, want 1.0", got)
	}
	if got := a11yClampScale(1.153); got != 1.15 {
		t.Fatalf("scales are stored to 1 decimal place: got %v", got)
	}
}

func TestA11yRemapButton(t *testing.T) {
	defer resetA11yForTest()

	a11y = a11ySettings{}
	if got := a11yRemapButton(sdl.CONTROLLER_BUTTON_A); got != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("no remap changed A into %d", got)
	}

	a11y = a11ySettings{swapAB: true}
	cases := map[uint8]uint8{
		sdl.CONTROLLER_BUTTON_A: sdl.CONTROLLER_BUTTON_B,
		sdl.CONTROLLER_BUTTON_B: sdl.CONTROLLER_BUTTON_A,
		sdl.CONTROLLER_BUTTON_X: sdl.CONTROLLER_BUTTON_X,
	}
	for in, want := range cases {
		if got := a11yRemapButton(in); got != want {
			t.Fatalf("swap A/B: %d -> %d, want %d", in, got, want)
		}
	}

	a11y = a11ySettings{swapXY: true}
	cases = map[uint8]uint8{
		sdl.CONTROLLER_BUTTON_X: sdl.CONTROLLER_BUTTON_Y,
		sdl.CONTROLLER_BUTTON_Y: sdl.CONTROLLER_BUTTON_X,
		sdl.CONTROLLER_BUTTON_A: sdl.CONTROLLER_BUTTON_A,
	}
	for in, want := range cases {
		if got := a11yRemapButton(in); got != want {
			t.Fatalf("swap X/Y: %d -> %d, want %d", in, got, want)
		}
	}

	// Both swaps together must stay a permutation, not collapse two buttons
	// onto one: A/B and X/Y are independent pairs.
	a11y = a11ySettings{swapAB: true, swapXY: true}
	seen := map[uint8]bool{}
	for _, b := range []uint8{
		sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B,
		sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y,
	} {
		mapped := a11yRemapButton(b)
		if seen[mapped] {
			t.Fatalf("button %d mapped onto an already used button %d", b, mapped)
		}
		seen[mapped] = true
	}
}

func TestA11yRemapEventOnlyOnPress(t *testing.T) {
	defer resetA11yForTest()
	a11y = a11ySettings{swapAB: true}

	down := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}
	a11yRemapEvent(down)
	if down.Button != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("button down was not remapped: got %d", down.Button)
	}

	up := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONUP, Button: sdl.CONTROLLER_BUTTON_A}
	a11yRemapEvent(up)
	if up.Button != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("button up must pass through untouched, got %d", up.Button)
	}

	// Nil is safe: the main loop may hand over an empty event.
	a11yRemapEvent(nil)
}

func TestA11yColorblindPresetsAreInstalled(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range ListThemePresets() {
		listed[name] = true
	}
	for _, mode := range a11yColorblindModes {
		if mode.preset == "" {
			continue
		}
		if _, ok := themePresets[mode.preset]; !ok {
			t.Fatalf("colour-blind palette %q has no theme preset", mode.preset)
		}
		if !listed[mode.preset] {
			t.Fatalf("palette %q is missing from the theme gallery order", mode.preset)
		}
		if mode.label == "" {
			t.Fatalf("palette %q has no label", mode.preset)
		}
	}
	if a11yColorblindPreset("nonsense") != "" {
		t.Fatal("an unknown colour-blind key must not resolve to a palette")
	}
	if a11yColorblindPreset("") != "" {
		t.Fatal(`"off" must resolve to no palette`)
	}
}

func TestA11yLoadFromConfig(t *testing.T) {
	defer resetA11yForTest()

	// The JSON loader decodes numbers as float64 and booleans as bool, while a
	// hand-edited or XML config supplies strings. Both must load.
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		a11yKeyFontScale:  float64(1.15),
		a11yKeyContrast:   true,
		a11yKeySwapAB:     "true",
		a11yKeySwapXY:     false,
		a11yKeyColorblind: "deuteranopia",
	}}}
	a11y = a11ySettings{fontScale: 1.0}
	a11yLoad(config)

	if a11y.fontScale != 1.15 {
		t.Fatalf("font scale: got %v, want 1.15", a11y.fontScale)
	}
	if !a11y.highContrast {
		t.Fatal("high contrast did not load")
	}
	if !a11y.swapAB || a11y.swapXY {
		t.Fatalf("swap flags: got ab=%v xy=%v, want true/false", a11y.swapAB, a11y.swapXY)
	}
	if a11y.colorblind != "deuteranopia" {
		t.Fatalf("colour-blind mode: got %q", a11y.colorblind)
	}

	// An out-of-range scale is clamped instead of breaking every label.
	a11y = a11ySettings{fontScale: 1.0}
	a11yLoad(&Config{Variables: Variables{Custom: map[string]interface{}{
		a11yKeyFontScale: "8",
	}}})
	if a11y.fontScale != a11yMaxScale {
		t.Fatalf("out-of-range scale: got %v, want %v", a11y.fontScale, a11yMaxScale)
	}

	// Unknown palette names fall back to "off" rather than a missing preset.
	a11y = a11ySettings{fontScale: 1.0}
	a11yLoad(&Config{Variables: Variables{Custom: map[string]interface{}{
		a11yKeyColorblind: "chartreuse",
	}}})
	if a11y.colorblind != "" {
		t.Fatalf("unknown palette: got %q, want empty", a11y.colorblind)
	}
}

func TestA11yRowsReportCurrentSettings(t *testing.T) {
	defer resetA11yForTest()

	a11y = a11ySettings{fontScale: 1.3, highContrast: true, swapAB: true, colorblind: "tritanopia", oneHanded: true, chordButton: "r1", slowRepeat: true}
	rows := a11yRows()
	if len(rows) != 11 {
		t.Fatalf("row count: got %d, want 11", len(rows))
	}
	if rows[0].value != "130%" {
		t.Fatalf("text size row: got %q", rows[0].value)
	}
	if rows[1].value != "On" {
		t.Fatalf("contrast row: got %q", rows[1].value)
	}
	if rows[2].value != "Tritanopia" {
		t.Fatalf("palette row: got %q", rows[2].value)
	}
	if rows[3].value != "Custom" {
		t.Fatalf("layout preset row: got %q, want Custom with a single swap", rows[3].value)
	}
	if rows[4].value != "Swapped" || rows[5].value != "Normal" {
		t.Fatalf("swap rows: got %q / %q", rows[4].value, rows[5].value)
	}
	if rows[6].value != "Default" {
		t.Fatalf("select row: got %q", rows[6].value)
	}
	if rows[7].value != "On" {
		t.Fatalf("one-handed row: got %q", rows[7].value)
	}
	if rows[8].value != "R1" {
		t.Fatalf("chord button row: got %q", rows[8].value)
	}
	if rows[9].value != "Slow" {
		t.Fatalf("key repeat row: got %q", rows[9].value)
	}
}

// ── One-handed mode ───────────────────────────────────────────────────────

func chordEvent(kind uint32, button uint8) *sdl.ControllerButtonEvent {
	return &sdl.ControllerButtonEvent{Type: kind, Button: button}
}

// A tap on the chord button arms the next direction, and the direction arrives
// as a face button. The latch is one-shot, so the following press is a normal
// d-pad move again.
func TestA11yOneHandedChordMapsOneDirection(t *testing.T) {
	defer resetA11yForTest()
	a11y = a11ySettings{fontScale: 1.0, oneHanded: true, chordButton: "l1"}
	mod := uint8(sdl.CONTROLLER_BUTTON_LEFTSHOULDER)

	if !a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONDOWN, mod)) {
		t.Fatal("a chord tap must be consumed, not fire the shoulder action")
	}
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONUP, mod))
	if !a11yChordState.latched {
		t.Fatal("the tap did not arm the chord")
	}

	down := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_DPAD_UP)
	if a11yRemapEvent(down) {
		t.Fatal("the direction press must reach the handlers")
	}
	if down.Button != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("Up became %d, want A", down.Button)
	}
	if a11yChordState.latched {
		t.Fatal("the latch survived its one press")
	}

	up := chordEvent(sdl.CONTROLLERBUTTONUP, sdl.CONTROLLER_BUTTON_DPAD_UP)
	a11yRemapEvent(up)
	if up.Button != sdl.CONTROLLER_BUTTON_A {
		t.Fatalf("the release became %d, want it mirrored to A", up.Button)
	}

	// A second direction without a new tap is an ordinary d-pad move.
	again := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_DPAD_DOWN)
	a11yRemapEvent(again)
	if again.Button != sdl.CONTROLLER_BUTTON_DPAD_DOWN {
		t.Fatalf("a spent latch remapped %d", again.Button)
	}
}

// Every direction has a face button, and B/X/Y are reachable too: one thumb
// can walk the UI without ever crossing to the face buttons.
func TestA11yOneHandedCoversAllDirections(t *testing.T) {
	cases := map[uint8]uint8{
		sdl.CONTROLLER_BUTTON_DPAD_UP:    sdl.CONTROLLER_BUTTON_A,
		sdl.CONTROLLER_BUTTON_DPAD_DOWN:  sdl.CONTROLLER_BUTTON_B,
		sdl.CONTROLLER_BUTTON_DPAD_LEFT:  sdl.CONTROLLER_BUTTON_X,
		sdl.CONTROLLER_BUTTON_DPAD_RIGHT: sdl.CONTROLLER_BUTTON_Y,
	}
	t.Cleanup(resetA11yForTest)
	for dir, want := range cases {
		resetA11yForTest()
		a11y = a11ySettings{fontScale: 1.0, oneHanded: true, chordButton: "r1"}
		a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER))
		e := chordEvent(sdl.CONTROLLERBUTTONDOWN, dir)
		a11yRemapEvent(e)
		if e.Button != want {
			t.Fatalf("direction %d became %d, want %d", dir, e.Button, want)
		}
	}
}

// Tapping the chord button twice cancels instead of leaving a latch armed.
func TestA11yOneHandedChordCancelsOnSecondTap(t *testing.T) {
	defer resetA11yForTest()
	a11y = a11ySettings{fontScale: 1.0, oneHanded: true, chordButton: "l1"}
	mod := uint8(sdl.CONTROLLER_BUTTON_LEFTSHOULDER)
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONDOWN, mod))
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONUP, mod))
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONDOWN, mod))
	if a11yChordState.latched {
		t.Fatal("a second tap must cancel the chord")
	}
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONUP, mod))
	up := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_DPAD_UP)
	a11yRemapEvent(up)
	if up.Button != sdl.CONTROLLER_BUTTON_DPAD_UP {
		t.Fatalf("a cancelled chord still remapped to %d", up.Button)
	}
}

// Holding the modifier works the same as tapping it once: the next direction
// is a face button and the latch does not stay armed after it.
func TestA11yOneHandedHeldModifierDoesNotStick(t *testing.T) {
	defer resetA11yForTest()
	a11y = a11ySettings{fontScale: 1.0, oneHanded: true, chordButton: "l1"}
	mod := uint8(sdl.CONTROLLER_BUTTON_LEFTSHOULDER)
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONDOWN, mod))
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_DPAD_RIGHT))
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONUP, sdl.CONTROLLER_BUTTON_DPAD_RIGHT))
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONUP, mod))

	after := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_DPAD_RIGHT)
	a11yRemapEvent(after)
	if after.Button != sdl.CONTROLLER_BUTTON_DPAD_RIGHT {
		t.Fatalf("the chord stayed armed after the modifier was released: %d", after.Button)
	}
}

// The chord output goes through the A/B swap rules, so a player who also needs
// the Japanese button order keeps it.
func TestA11yOneHandedChordRespectsSwaps(t *testing.T) {
	defer resetA11yForTest()
	a11y = a11ySettings{fontScale: 1.0, oneHanded: true, chordButton: "l1", swapAB: true}
	a11yRemapEvent(chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_LEFTSHOULDER))
	e := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_DPAD_UP)
	a11yRemapEvent(e)
	if e.Button != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("Up became %d, want B after the swap", e.Button)
	}
}

// The chord has to reach real handlers, not only the remap function: an armed
// chord plus D-pad Down is B, and B closes the panel that is open.
func TestA11yOneHandedChordDrivesThePanel(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	a11y = a11ySettings{fontScale: 1.0, oneHanded: true, chordButton: "l1", open: true}
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	mod := chordEvent(sdl.CONTROLLERBUTTONDOWN, uint8(sdl.CONTROLLER_BUTTON_LEFTSHOULDER))
	if !a11yRemapEvent(mod) {
		t.Fatal("the chord tap was not consumed")
	}
	down := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_DPAD_DOWN)
	a11yRemapEvent(down)
	if down.Button != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("Down became %d, want B", down.Button)
	}
	if !handleControllerEvent(down, config) {
		t.Fatal("the remapped B was not routed to the panel")
	}
	if a11y.open {
		t.Fatal("B did not close the accessibility panel")
	}
}

// With the mode off, nothing about the d-pad or the shoulders changes.
func TestA11yOneHandedOffLeavesEventsAlone(t *testing.T) {
	defer resetA11yForTest()
	a11y = a11ySettings{fontScale: 1.0, chordButton: "l1"}
	mod := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_LEFTSHOULDER)
	if a11yRemapEvent(mod) {
		t.Fatal("the shoulder press was consumed while the mode is off")
	}
	if a11yChordState.latched {
		t.Fatal("the chord armed while the mode is off")
	}
	down := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_DPAD_UP)
	a11yRemapEvent(down)
	if down.Button != sdl.CONTROLLER_BUTTON_DPAD_UP {
		t.Fatalf("d-pad was remapped with the mode off: %d", down.Button)
	}
}

// The chord button is a real setting, saved and loaded like the rest.
func TestA11yOneHandedSettingsRoundTrip(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	a11y = a11ySettings{fontScale: 1.0, oneHanded: true, chordButton: "back", slowRepeat: true}
	a11yStore(config)

	a11y = a11ySettings{fontScale: 1.0}
	a11yLoad(config)
	if !a11y.oneHanded || a11y.chordButton != "back" || !a11y.slowRepeat {
		t.Fatalf("round-trip lost the one-handed settings: %+v", a11y)
	}

	// An unknown stored button falls back to L1 instead of disabling the mode.
	a11yLoad(&Config{Variables: Variables{Custom: map[string]interface{}{
		a11yKeyChordBtn: "pedal",
	}}})
	if a11y.chordButton != "l1" {
		t.Fatalf("unknown chord button loaded as %q, want l1", a11y.chordButton)
	}
}

func TestA11ySlowRepeatDoublesDelays(t *testing.T) {
	defer resetA11yForTest()
	a11y = a11ySettings{fontScale: 1.0}
	if got := a11yRepeatMs(navRepeatDelayMs); got != navRepeatDelayMs {
		t.Fatalf("normal repeat: got %d, want %d", got, navRepeatDelayMs)
	}
	a11y.slowRepeat = true
	if got := a11yRepeatMs(navRepeatDelayMs); got != navRepeatDelayMs*2 {
		t.Fatalf("slow repeat: got %d, want %d", got, navRepeatDelayMs*2)
	}
}

// Cycling the chord button wraps through every option and survives a restart
// of the panel cursor.
func TestA11yChordButtonCycles(t *testing.T) {
	defer resetA11yForTest()
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	a11y = a11ySettings{fontScale: 1.0, chordButton: "l1"}
	// The panel edits the focused row, so park the cursor on the chord row.
	for i, row := range a11yRows() {
		if row.kind == a11yRowChordButton {
			a11y.cursor = i
		}
	}
	seen := map[string]bool{}
	for i := 0; i < len(a11yChordButtons); i++ {
		seen[a11y.chordButton] = true
		a11yAdjust(1, config)
	}
	if !seen["l1"] || a11y.chordButton != "l1" {
		t.Fatalf("cycling did not visit every button and return: %+v", seen)
	}
	for _, option := range a11yChordButtons {
		if !seen[option.key] {
			t.Fatalf("cycling skipped %q", option.key)
		}
	}
}

// TestFontScalingReachesRealFonts proves the setting is wired into the font
// loader rather than only stored: the same logical font must come back taller
// once the scale is raised.
func TestFontScalingReachesRealFonts(t *testing.T) {
	newProbeFrame(t) // initialises SDL_ttf for the font loader
	prev := a11y.fontScale
	t.Cleanup(func() { a11y.fontScale = prev })

	config := &Config{Variables: Variables{FontSizes: map[string]int{"small": 16}}}
	a11y.fontScale = 1.0
	base, baseSize := getCachedFont(config, "small")
	if base == nil {
		t.Skip("no font available in this checkout")
	}
	a11y.fontScale = 1.3
	scaled, scaledSize := getCachedFont(config, "small")
	if scaled == nil {
		t.Fatal("scaled font failed to load")
	}
	if baseSize != 16 || scaledSize != 21 {
		t.Fatalf("reported sizes: base %d, scaled %d, want 16 and 21", baseSize, scaledSize)
	}
	if scaled.Height() <= base.Height() {
		t.Fatalf("scaled font is not taller: %d vs %d", scaled.Height(), base.Height())
	}
}

func TestStartMenuExposesAccessibility(t *testing.T) {
	found := false
	for _, entry := range getStartMenuApps(&Config{}) {
		if entry.name == "Accessibility" {
			found = entry.action != nil
		}
	}
	if !found {
		t.Fatal("the Start menu has no working Accessibility entry")
	}
}

func TestControllerRoutesToAccessibilityPanel(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	a11y = a11ySettings{fontScale: 1.0, open: true}
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	down := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN}
	if !handleControllerEvent(down, config) {
		t.Fatal("the modal panel did not consume the D-pad press")
	}
	if a11y.cursor != 1 {
		t.Fatalf("cursor: got %d, want 1", a11y.cursor)
	}

	// Back on the text-size row, Right and Left walk the scale.
	a11y.cursor = 0
	right := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_RIGHT}
	handleControllerEvent(right, config)
	if a11y.fontScale != 1.15 {
		t.Fatalf("Right on text size: got %v, want 1.15", a11y.fontScale)
	}
	left := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_LEFT}
	handleControllerEvent(left, config)
	if a11y.fontScale != 1.0 {
		t.Fatalf("Left on text size: got %v, want 1.0", a11y.fontScale)
	}
}

func TestContrastPaletteSelectionAndRestore(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		"theme_preset": "ocean",
	}}}
	if got := a11yPaletteName(config); got != "" {
		t.Fatalf("palette with everything off returned %q, want no change", got)
	}

	// Turning contrast on must record the theme to come back to.
	a11y.highContrast = true
	if got := a11yPaletteName(config); got != "highcontrast" {
		t.Fatalf("high contrast selected %q", got)
	}
	a11yRememberTheme(config)
	config.Variables.Custom["theme_preset"] = "highcontrast"

	a11y.highContrast = false
	if got := a11yPaletteName(config); got != "ocean" {
		t.Fatalf("turning contrast off selected %q, want ocean", got)
	}

	// A theme the user picks afterwards is never overwritten by a stale memory.
	config.Variables.Custom["theme_preset"] = "matrix"
	if got := a11yPaletteName(config); got != "" {
		t.Fatalf("a user-chosen theme was overridden with %q", got)
	}

	// Colour-blind palettes beat high contrast, and each maps to its own theme.
	a11y.highContrast = true
	a11y.colorblind = "tritanopia"
	if got := a11yPaletteName(config); got != "cb-tritanopia" {
		t.Fatalf("colour-blind mode selected %q", got)
	}
}

func TestA11ySaveWritesSettingsForReload(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	a11y = a11ySettings{fontScale: 1.15, highContrast: true, colorblind: "protanopia", swapAB: true}
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	a11ySave(config)

	// A fresh launch reads the same map back; this is the round-trip the device
	// performs on reboot.
	a11y = a11ySettings{fontScale: 1.0}
	a11yLoad(config)
	if a11y.fontScale != 1.15 || !a11y.highContrast || a11y.colorblind != "protanopia" || !a11y.swapAB {
		t.Fatalf("round-trip lost settings: %+v", a11y)
	}
}

// TestRenderAccessibilityPanelFitsScreen draws the real card offscreen and
// checks that it lands inside the 720p viewport with every row hit-testable.
// Layout arithmetic that only looks right in code is the usual way a panel ends
// up clipped on the device.
func TestRenderAccessibilityPanelFitsScreen(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		resetA11yForTest()
	})

	// The Smart Pro renders 1280x720, its compact layout 960x720, and the
	// smallest supported canvas is 480p. Every row has to stay reachable at the
	// largest text size on all three.
	for _, size := range [][2]int32{{1280, 720}, {960, 720}, {640, 480}} {
		for _, scale := range []float64{1.0, 1.3, 1.45} {
			screenWidth, screenHeight = size[0], size[1]
			a11y = a11ySettings{fontScale: scale, open: true}
			frame := newProbeFrame(t)

			renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
			if err != nil {
				t.Skipf("software renderer unavailable: %v", err)
			}
			renderer.SetDrawColor(frame.bg.R, frame.bg.G, frame.bg.B, 255)
			renderer.Clear()
			renderAccessibilityPanel(renderer, &Config{})
			if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
				destroyTestRenderer(renderer)
				t.Skipf("renderer.ReadPixels unavailable: %v", err)
			}

			r := a11yPanelRect
			if r.W <= 0 || r.H <= 0 {
				destroyTestRenderer(renderer)
				t.Fatalf("%dx%d @ %.2f: panel rect was not measured: %+v", size[0], size[1], scale, r)
			}
			if r.X < 0 || r.Y < 0 || r.X+r.W > size[0] || r.Y+r.H > size[1] {
				destroyTestRenderer(renderer)
				t.Fatalf("%dx%d @ %.2f: panel %+v does not fit the viewport", size[0], size[1], scale, r)
			}
			if got, all := len(a11yRowHits), len(a11yRows()); got == 0 || got > all {
				destroyTestRenderer(renderer)
				t.Fatalf("%dx%d @ %.2f: hit areas %d of %d", size[0], size[1], scale, got, all)
			}
			// The scrim covers the viewport, so "nothing drawn" means the panel
			// bailed out early. Sample the card's own fill.
			mid := frame.at(r.X+r.W/2, r.Y+r.H/2)
			painted := !(mid.R == frame.bg.R && mid.G == frame.bg.G && mid.B == frame.bg.B)
			destroyTestRenderer(renderer)
			if !painted {
				t.Fatalf("%dx%d @ %.2f: panel body was not painted", size[0], size[1], scale)
			}
		}
	}
}

func TestPanelReflectsThePaletteThatIsOnScreen(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	defer closeAccessibilityPanel()

	// A colour-blind palette chosen from the gallery shows up as selected.
	openAccessibilityPanel(&Config{Variables: Variables{Custom: map[string]interface{}{
		"theme_preset": "cb-protanopia",
	}}})
	if a11y.colorblind != "protanopia" || a11y.highContrast {
		t.Fatalf("panel state after gallery palette: colorblind=%q contrast=%v", a11y.colorblind, a11y.highContrast)
	}
	closeAccessibilityPanel()

	// Picking an ordinary theme afterwards means no palette of ours is active,
	// even though the stored flag was set earlier.
	a11y.colorblind = "protanopia"
	openAccessibilityPanel(&Config{Variables: Variables{Custom: map[string]interface{}{
		"theme_preset": "ocean",
	}}})
	if a11y.colorblind != "" || a11y.highContrast {
		t.Fatalf("panel still claims a palette is active: colorblind=%q contrast=%v", a11y.colorblind, a11y.highContrast)
	}
}

func TestOpenAccessibilityPanelClosesOtherOverlays(t *testing.T) {
	defer resetA11yForTest()
	defer func() { chiakiPanel = chiakiPanelState{} }()

	chiakiPanel = chiakiPanelState{open: true}
	qs.open = true
	openAccessibilityPanel(&Config{Variables: Variables{Custom: map[string]interface{}{}}})
	if !a11y.open {
		t.Fatal("panel did not open")
	}
	if chiakiPanel.open || qs.open {
		t.Fatalf("panel must be exclusive, chiaki=%v qs=%v", chiakiPanel.open, qs.open)
	}
	closeAccessibilityPanel()
	if a11y.open {
		t.Fatal("panel did not close")
	}
}
