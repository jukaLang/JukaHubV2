package main

import "testing"

// TestAccessibilityOffersPowerMode pins the row the Phase 5.4 gap needed: the
// named Power Mode is reachable from the Accessibility panel, not only Quick
// Settings, and it reports the config's saved mode.
func TestAccessibilityOffersPowerMode(t *testing.T) {
	prevConfig := appConfig
	appConfig = &Config{Variables: Variables{PowerMode: PowerModeBalanced}}
	t.Cleanup(func() { appConfig = prevConfig })

	found := false
	for _, row := range a11yRows() {
		if row.kind == a11yRowPowerMode {
			found = true
			if row.value != "Balanced" {
				t.Errorf("Power mode row value = %q, want Balanced", row.value)
			}
			if row.label == "" || row.detail == "" {
				t.Error("Power mode row needs a label and a detail line like every other row")
			}
		}
	}
	if !found {
		t.Fatal("the Accessibility panel has no Power mode row")
	}
}

// TestAccessibilityPowerModeCycles: a left/right press steps the saved mode
// through the same order Quick Settings uses, and it is persisted.
func TestAccessibilityPowerModeCycles(t *testing.T) {
	prevConfig := appConfig
	cfg := &Config{Variables: Variables{PowerMode: PowerModeDefault}}
	appConfig = cfg
	t.Cleanup(func() { appConfig = prevConfig })

	// Place the cursor on the Power mode row.
	idx := -1
	for i, row := range a11yRows() {
		if row.kind == a11yRowPowerMode {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no Power mode row to adjust")
	}
	prevCursor := a11y.cursor
	a11y.cursor = idx
	t.Cleanup(func() { a11y.cursor = prevCursor })

	// Step forward: Default -> Battery Saver.
	a11yAdjust(1, cfg)
	if got := currentPowerMode(cfg); got != PowerModeBatterySaver {
		t.Fatalf("after one forward step mode = %q, want %q", got, PowerModeBatterySaver)
	}
	// Battery Saver must be the low-power verdict, which is the point of the mode.
	if !effectiveLowPower(cfg) {
		t.Fatal("Battery Saver did not force Low Power")
	}
	// Step forward again: -> Balanced, which releases Low Power.
	a11yAdjust(1, cfg)
	if got := currentPowerMode(cfg); got != PowerModeBalanced {
		t.Fatalf("after two steps mode = %q, want %q", got, PowerModeBalanced)
	}
	if effectiveLowPower(cfg) {
		t.Fatal("Balanced should not force Low Power")
	}
}
