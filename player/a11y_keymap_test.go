package main

// a11y_keymap_test.go — the general keymap layer and the vision toggles. The swap flags cover the two
// classic presets; the map is what makes remapping a button a data change
// instead of a branch in every scene: it is parsed from the saved string,
// applied before dispatch, and can swallow a button outright.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestA11yKeymapParsesAndRoundTrips(t *testing.T) {
	keys := a11yParseKeymap("select:none,start:select")
	if got := keys[sdl.CONTROLLER_BUTTON_BACK]; got != a11yDisabledButton {
		t.Fatalf("select target = %d, want the disabled marker", got)
	}
	if got := a11yKeymapString(keys); got != "select:none,start:select" {
		t.Fatalf("round trip = %q, want both entries in name order", got)
	}

	// A typo costs one entry, not the map.
	keys = a11yParseKeymap("pedal:start,select:start")
	if _, ok := keys[sdl.CONTROLLER_BUTTON_BACK]; !ok {
		t.Fatal("a valid entry after an unknown one was dropped")
	}
	if len(keys) != 1 {
		t.Fatalf("unknown names produced %d entries", len(keys))
	}
	// An identity mapping is noise, not a remap.
	if len(a11yParseKeymap("a:a")) != 0 {
		t.Fatal("identity mapping was stored")
	}
}

func TestA11yKeymapAppliesBeforeSwaps(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	a11y.keys = map[uint8]uint8{
		sdl.CONTROLLER_BUTTON_BACK: sdl.CONTROLLER_BUTTON_A,
	}
	a11y.swapAB = true
	// Select is mapped to A, then the A/B preset renames it to B. The order
	// matters: the keymap says which logical action, the preset says which
	// face button that action sits on.
	got, dropped := a11yResolveButton(sdl.CONTROLLER_BUTTON_BACK)
	if dropped {
		t.Fatal("a mapped button was swallowed")
	}
	if got != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("select mapped to %d, want B after the preset", got)
	}

	// A disabled button is swallowed before any preset can revive it.
	a11y.keys[sdl.CONTROLLER_BUTTON_BACK] = a11yDisabledButton
	if _, dropped := a11yResolveButton(sdl.CONTROLLER_BUTTON_BACK); !dropped {
		t.Fatal("a disabled button still reached a handler")
	}
}

// Dropping has to happen in the event rewrite, not only in the resolver: the
// remap function reports "consume this event" with its bool.
func TestA11yDisabledButtonIsConsumedBeforeDispatch(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	a11y.keys = map[uint8]uint8{sdl.CONTROLLER_BUTTON_BACK: a11yDisabledButton}

	press := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_BACK)
	if !a11yRemapEvent(press) {
		t.Fatal("a disabled button press was not consumed")
	}
	// Other buttons still pass through unchanged.
	other := chordEvent(sdl.CONTROLLERBUTTONDOWN, sdl.CONTROLLER_BUTTON_START)
	if a11yRemapEvent(other) {
		t.Fatal("Start was consumed while only Select is disabled")
	}
	if other.Button != sdl.CONTROLLER_BUTTON_START {
		t.Fatalf("Start became %d", other.Button)
	}
}

func TestA11ySelectSettingSurvivesSaveAndLoad(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	a11ySetSelectKeyMode(a11ySelectSwapped)
	a11yStore(cfg)
	if raw := cfg.Variables.Custom[a11yKeyKeymap]; raw != "select:start,start:select" {
		t.Fatalf("stored keymap = %v", raw)
	}

	a11y = a11ySettings{fontScale: 1.0}
	a11yLoad(cfg)
	if got := a11ySelectKeyMode(); got != a11ySelectSwapped {
		t.Fatalf("reloaded mode = %q", got)
	}
}

func TestA11yLayoutPresetSetsBothSwaps(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	if got := a11yLayoutPresetLabel(); got != "Standard" {
		t.Fatalf("initial preset = %q", got)
	}
	for i, row := range a11yRows() {
		if row.kind == a11yRowLayoutPreset {
			a11y.cursor = i
		}
	}
	a11yAdjust(1, cfg)
	if !a11y.swapAB || !a11y.swapXY {
		t.Fatalf("Nintendo preset left swaps at ab=%v xy=%v", a11y.swapAB, a11y.swapXY)
	}
	if got := a11yLayoutPresetLabel(); got != "Nintendo" {
		t.Fatalf("preset label after applying = %q", got)
	}
	a11yAdjust(1, cfg)
	if a11y.swapAB || a11y.swapXY {
		t.Fatal("Standard preset did not clear both swaps")
	}

	// One swap is a custom layout, not a preset.
	a11y.swapAB = true
	if got := a11yLayoutPresetLabel(); got != "Custom" {
		t.Fatalf("single swap label = %q, want Custom", got)
	}
}

func TestA11YVisionSettingsLoadStoreAndApply(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{
		a11yKeyReduceTransparency: "true",
		a11yKeyIncreaseContrast:   "true",
	}
	a11yLoad(config)
	if !a11y.reduceTransparency || !a11y.increaseContrast {
		t.Fatalf("vision toggles did not load: reduce=%v increase=%v", a11y.reduceTransparency, a11y.increaseContrast)
	}
	if glassFocusRingWidth != 5 {
		t.Fatalf("focus ring width = %d after load, want 5", glassFocusRingWidth)
	}

	a11y.reduceTransparency = false
	a11y.increaseContrast = false
	a11yStore(config)
	if got := config.Variables.Custom[a11yKeyReduceTransparency]; got != false {
		t.Fatalf("stored reduce transparency = %v, want false", got)
	}
	if got := config.Variables.Custom[a11yKeyIncreaseContrast]; got != false {
		t.Fatalf("stored increase contrast = %v, want false", got)
	}
}

func TestA11YReduceTransparencyMakesEveryGlassTokenOpaque(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	if got := glassA(13).A; got == 255 {
		t.Fatal("glassA was already opaque before Reduce Transparency")
	}
	a11y.reduceTransparency = true
	for _, alpha := range []uint8{13, 16, 20, 26, 45, 210, 235} {
		if got := glassA(alpha).A; got != 255 {
			t.Fatalf("glassA(%d).A = %d, want 255 with Reduce Transparency", alpha, got)
		}
	}
}

func TestA11YIncreaseContrastStrengthensGlassAndRaisesTextToSevenToOne(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	normalAlpha := glassA(13).A
	a11y.increaseContrast = true
	a11yApplyVision()

	if glassFocusRingWidth != 5 {
		t.Fatalf("focus ring width = %d, want 5", glassFocusRingWidth)
	}
	if got := glassA(13).A; got <= normalAlpha {
		t.Fatalf("glass alpha = %d, want stronger than %d", got, normalAlpha)
	}

	for name, ink := range map[string]sdl.Color{
		"secondary": ColorTextSecondary(),
		"tertiary":  ColorTextTertiary(),
		"muted":     ColorTextMuted(),
		"accent":    ColorTextAccent(),
		"success":   SuccessInk(),
		"danger":    DangerInk(),
	} {
		for _, surface := range []sdl.Color{ColorBackground, ColorSurface, ColorSurfaceAlt} {
			if got := a11yContrastRatio(ink, surface); got < 7.0 {
				t.Fatalf("%s text contrast = %.2f:1, want >= 7:1", name, got)
			}
		}
	}

	// The same accessors must keep their authored values once the switch is off.
	a11y.increaseContrast = false
	a11yApplyVision()
	if got := ColorTextTertiary(); got != textTertiaryColor {
		t.Fatal("tertiary text stayed altered after Increase Contrast was disabled")
	}
	if glassFocusRingWidth != 3 {
		t.Fatalf("focus ring width = %d after disabling, want 3", glassFocusRingWidth)
	}
}

func TestA11YVisionSettingsRowsReflectAndToggleState(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	setReduceTransparency(nil, true)
	setIncreaseContrast(nil, true)
	if !a11y.reduceTransparency || !a11y.increaseContrast {
		t.Fatal("Settings helpers did not update the vision state")
	}

	values := map[string]string{}
	for _, row := range a11yRows() {
		values[row.label] = row.value
	}
	if values["Reduce transparency"] != "On" || values["Increase contrast"] != "On" {
		t.Fatalf("panel rows do not reflect the toggles: %#v", values)
	}
}

func a11yContrastRatio(a, b sdl.Color) float64 {
	la, lb := wcagLuminance(a), wcagLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestA11YBoldTextSelectsSemiBoldForEveryRole(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	if got := homeUIFontForRole(homeUIFontFile); got != homeUIFontFile {
		t.Fatalf("normal UI face = %q, want %q", got, homeUIFontFile)
	}
	a11y.boldText = true
	for _, configured := range []string{homeUIFontFile, "Inter-Bold.ttf", "custom.ttf"} {
		if got := homeUIFontForRole(configured); got != homeUIFontBoldFile {
			t.Fatalf("bold UI face for %q = %q, want %q", configured, got, homeUIFontBoldFile)
		}
	}

	cfg := &Config{}
	a11yStore(cfg)
	if got := cfg.Variables.Custom[a11yKeyBoldText]; got != true {
		t.Fatalf("stored bold text = %v, want true", got)
	}
	a11y = a11ySettings{fontScale: 1.0}
	a11yLoad(cfg)
	if !a11y.boldText {
		t.Fatal("bold text did not survive a config round trip")
	}
	for _, row := range a11yRows() {
		if row.kind == a11yRowBoldText && row.value != "On" {
			t.Fatalf("bold text row = %q, want On", row.value)
		}
	}
}
