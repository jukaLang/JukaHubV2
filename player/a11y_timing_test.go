package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestA11YTimingSettingsDriveHandlers(t *testing.T) {
	oldLong, oldDelay, oldRate, oldToast := a11y.longPressMs, a11y.repeatDelayMs, a11y.repeatRateMs, a11y.toastPercent
	defer func() {
		a11y.longPressMs, a11y.repeatDelayMs, a11y.repeatRateMs, a11y.toastPercent = oldLong, oldDelay, oldRate, oldToast
	}()
	a11y.longPressMs, a11y.repeatDelayMs, a11y.repeatRateMs, a11y.toastPercent = 600, 400, 90, 150
	if got := a11yLongPressMs(); got != 600 {
		t.Fatalf("long press = %d", got)
	}
	if got := a11yRepeatDelayMs(); got != 400 {
		t.Fatalf("repeat delay = %d", got)
	}
	if got := a11yRepeatRateMs(); got != 90 {
		t.Fatalf("repeat rate = %d", got)
	}
	if got := a11yToastMs(2000); got != 3000 {
		t.Fatalf("toast duration = %d, want 3000", got)
	}
}

func TestA11YStickyChordStaysArmed(t *testing.T) {
	old, oldState := a11y, a11yChordState
	defer func() { a11y, a11yChordState = old, oldState }()
	a11y.oneHanded, a11y.chordButton = true, "l1"
	a11y.stickyChords = true
	a11yChordState = a11yChordRuntime{latched: true}
	e := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_UP}
	a11yRemapEvent(e)
	if !a11yChordState.latched {
		t.Fatal("sticky chord disarmed after one direction")
	}
	a11y.stickyChords = false
	a11yChordState.latched = true
	e = &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN}
	a11yRemapEvent(e)
	if a11yChordState.latched {
		t.Fatal("ordinary chord stayed armed after one direction")
	}
}

func TestA11YTimingRowsAndStore(t *testing.T) {
	labels := map[string]bool{}
	for _, row := range a11yRows() {
		labels[row.label] = true
	}
	for _, want := range []string{"Long press", "Repeat delay", "Repeat speed", "Toast duration", "Sticky chords"} {
		if !labels[want] {
			t.Fatalf("Accessibility has no %q row", want)
		}
	}
	old := a11y
	defer func() { a11y = old }()
	a11y.longPressMs, a11y.repeatDelayMs, a11y.repeatRateMs = 600, 400, 90
	a11y.toastPercent, a11y.stickyChords = 150, true
	cfg := &Config{}
	a11yStore(cfg)
	for key, want := range map[string]interface{}{
		a11yKeyLongPress: 600, a11yKeyRepeatDelay: 400, a11yKeyRepeatRate: 90,
		a11yKeyToastDuration: 150, a11yKeyStickyChords: true,
	} {
		if got := cfg.Variables.Custom[key]; got != want {
			t.Fatalf("%s = %v, want %v", key, got, want)
		}
	}
}
