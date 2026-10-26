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

// focusA11yButtonRow puts the cursor on the row that edits one button.
func focusA11yButtonRow(t *testing.T, button uint8) {
	t.Helper()
	for i, row := range a11yRows() {
		if row.kind == a11yRowButton && row.button == button {
			a11y.cursor = i
			return
		}
	}
	t.Fatalf("no panel row for button %d", button)
}

func focusA11yPresetRow(t *testing.T) {
	t.Helper()
	for i, row := range a11yRows() {
		if row.kind == a11yRowLayoutPreset {
			a11y.cursor = i
			return
		}
	}
	t.Fatal("no Button layout row")
}

func TestA11yButtonRowsRemapAndDisable(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	focusA11yButtonRow(t, sdl.CONTROLLER_BUTTON_A)
	if got := a11yRows()[a11y.cursor].value; got != "A" {
		t.Fatalf("default value for A = %q", got)
	}

	// One step right is the next action in the list, and an explicit entry is
	// saved rather than left to the preset.
	a11yAdjust(1, cfg)
	if got := a11y.keys[sdl.CONTROLLER_BUTTON_A]; got != sdl.CONTROLLER_BUTTON_B {
		t.Fatalf("A mapped to %d, want B", got)
	}
	if a11yRemapButton(sdl.CONTROLLER_BUTTON_A) != sdl.CONTROLLER_BUTTON_B {
		t.Fatal("the panel entry did not reach the dispatch layer")
	}
	focusA11yButtonRow(t, sdl.CONTROLLER_BUTTON_A)
	if got := a11yRows()[a11y.cursor].value; got != "B" {
		t.Fatalf("row value after remap = %q", got)
	}

	// Left steps back to identity, which must drop the entry entirely.
	a11yAdjust(-1, cfg)
	if _, ok := a11y.keys[sdl.CONTROLLER_BUTTON_A]; ok {
		t.Fatal("identity mapping was stored instead of removed")
	}

	// The last stop in the other direction is Disabled, and it swallows the
	// press before any handler sees it.
	a11yAdjust(-1, cfg)
	if got := a11y.keys[sdl.CONTROLLER_BUTTON_A]; got != a11yDisabledButton {
		t.Fatalf("wrap-around target = %d, want the disabled marker", got)
	}
	if _, dropped := a11yResolveButton(sdl.CONTROLLER_BUTTON_A); !dropped {
		t.Fatal("a disabled face button still reached a handler")
	}
	focusA11yButtonRow(t, sdl.CONTROLLER_BUTTON_A)
	if got := a11yRows()[a11y.cursor].value; got != "Disabled" {
		t.Fatalf("row value for a disabled button = %q", got)
	}
}

func TestA11yButtonRowWinsOverPreset(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	a11y.swapAB = true
	focusA11yButtonRow(t, sdl.CONTROLLER_BUTTON_A)
	if got := a11yRows()[a11y.cursor].value; got != "B" {
		t.Fatalf("A under the Nintendo preset = %q, want B", got)
	}

	// The row starts from what the button sends today (B) and moves on from
	// there, then clears the preset so the entry is not rewritten again.
	a11yAdjust(1, cfg)
	if a11y.swapAB {
		t.Fatal("the swap preset survived an explicit remap of A")
	}
	if got := a11yRemapButton(sdl.CONTROLLER_BUTTON_A); got != sdl.CONTROLLER_BUTTON_X {
		t.Fatalf("A sends %d after one step, want X", got)
	}
	if got := a11yLayoutPresetLabel(); got != "Custom" {
		t.Fatalf("preset label = %q, want Custom", got)
	}
}

func TestA11yPresetRowClearsPerButtonRemaps(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	a11y.keys = map[uint8]uint8{sdl.CONTROLLER_BUTTON_A: sdl.CONTROLLER_BUTTON_Y}
	if got := a11yLayoutPresetLabel(); got != "Custom" {
		t.Fatalf("label with an entry = %q, want Custom", got)
	}

	focusA11yPresetRow(t)
	a11yAdjust(1, cfg)
	if !a11y.swapAB || !a11y.swapXY {
		t.Fatal("the preset did not apply the Nintendo pair")
	}
	if len(a11y.keys) != 0 {
		t.Fatalf("the preset left %d per-button entries behind", len(a11y.keys))
	}
	if got := a11yLayoutPresetLabel(); got != "Nintendo" {
		t.Fatalf("label after applying = %q", got)
	}

	a11yAdjust(1, cfg)
	if a11y.swapAB || a11y.swapXY {
		t.Fatal("Standard did not clear both swaps")
	}
	if got := a11yLayoutPresetLabel(); got != "Standard" {
		t.Fatalf("label after Standard = %q", got)
	}
}

func TestA11yButtonRowsCoverEveryPanelButton(t *testing.T) {
	resetA11yForTest()
	t.Cleanup(resetA11yForTest)

	covered := map[uint8]bool{}
	for _, row := range a11yRows() {
		if row.kind == a11yRowButton {
			covered[row.button] = true
			if row.label == "" || row.value == "" {
				t.Fatalf("row for button %d is missing a label or value", row.button)
			}
		}
	}
	for _, button := range a11yPanelButtons {
		if !covered[button] {
			t.Fatalf("no row for panel button %d", button)
		}
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
