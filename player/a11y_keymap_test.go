package main

// a11y_keymap_test.go — the general keymap layer. The swap flags cover the two
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

func TestA11ySelectRowCyclesAndSwapsBothButtons(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	for i, row := range a11yRows() {
		if row.kind == a11yRowSelectKey {
			a11y.cursor = i
		}
	}
	if got := a11ySelectKeyMode(); got != a11ySelectDefault {
		t.Fatalf("initial mode = %q", got)
	}

	a11yAdjust(1, cfg)
	if got := a11ySelectKeyMode(); got != a11ySelectSwapped {
		t.Fatalf("after one press = %q, want Swapped", got)
	}
	if a11yRemapButton(sdl.CONTROLLER_BUTTON_BACK) != sdl.CONTROLLER_BUTTON_START ||
		a11yRemapButton(sdl.CONTROLLER_BUTTON_START) != sdl.CONTROLLER_BUTTON_BACK {
		t.Fatal("Swapped must keep both buttons reachable")
	}

	a11yAdjust(1, cfg)
	if got := a11ySelectKeyMode(); got != a11ySelectDisabled {
		t.Fatalf("after two presses = %q, want Disabled", got)
	}
	if _, dropped := a11yResolveButton(sdl.CONTROLLER_BUTTON_BACK); !dropped {
		t.Fatal("Disabled did not swallow Select")
	}
	if _, dropped := a11yResolveButton(sdl.CONTROLLER_BUTTON_START); dropped {
		t.Fatal("Disabled swallowed Start as well; the menu must stay reachable")
	}

	a11yAdjust(1, cfg)
	if got := a11ySelectKeyMode(); got != a11ySelectDefault {
		t.Fatalf("cycle did not return to Default: %q", got)
	}
	if len(a11y.keys) != 0 {
		t.Fatalf("Default left %d keymap entries behind", len(a11y.keys))
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
