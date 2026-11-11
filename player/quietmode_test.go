package main

import "testing"

func TestQuietModeHoldsToastsButNotAlarmResults(t *testing.T) {
	prev := quietModeOn()
	clearToastAfterTest(t)
	t.Cleanup(func() { quietModeActive.Store(prev) })

	cfg := &Config{}
	quietModeSet(cfg, true)
	if !quietModeOn() {
		t.Fatal("quiet mode did not turn on")
	}
	if got, _ := cfg.Variables.Custom[quietModeKey].(string); got != "true" {
		t.Fatalf("saved quiet mode = %q, want true", got)
	}

	showToastPrimitive("held", ToastKindInfo)
	if got := currentToast.Message; got != "" {
		t.Fatalf("a toast showed in quiet mode: %q", got)
	}

	// An alarm's outcome is safety feedback: the ring is over, but the user
	// still needs to know it was snoozed or dismissed.
	showToastAlways("Alarm dismissed", ToastInfo())
	if got := currentToast.Message; got != "Alarm dismissed" {
		t.Fatalf("alarm toast = %q, want it visible in quiet mode", got)
	}

	quietModeSet(cfg, false)
	showToastPrimitive("visible", ToastKindInfo)
	if got := currentToast.Message; got != "visible" {
		t.Fatalf("toast after quiet mode = %q, want visible", got)
	}
}

func TestQuietModeFromConfigAcceptsTheSavedShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  interface{}
		want bool
	}{
		{"missing", nil, false},
		{"bool true", true, true},
		{"bool false", false, false},
		{"string on", "on", true},
		{"string off", "OFF", false},
		{"number", float64(1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			if tc.raw != nil {
				cfg.Variables.Custom = map[string]interface{}{quietModeKey: tc.raw}
			}
			if got := quietModeFromConfig(cfg); got != tc.want {
				t.Fatalf("quietModeFromConfig(%v) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestQuickSettingsQuietToggleUsesTheLiveConfig(t *testing.T) {
	prevActive, prevApp := quietModeOn(), appConfig
	clearToastAfterTest(t)
	t.Cleanup(func() {
		quietModeActive.Store(prevActive)
		appConfig = prevApp
	})

	cfg := &Config{}
	appConfig = cfg
	if err := qsApplyToggle(&qsToggle{Action: qsActionQuiet}, true); err != nil {
		t.Fatal(err)
	}
	if !quietModeOn() {
		t.Fatal("Quick Settings did not turn quiet mode on")
	}
	if got, _ := cfg.Variables.Custom[quietModeKey].(string); got != "true" {
		t.Fatalf("Quick Settings saved quiet mode as %q", got)
	}
	if err := qsApplyToggle(&qsToggle{Action: qsActionQuiet}, false); err != nil {
		t.Fatal(err)
	}
	if quietModeOn() {
		t.Fatal("Quick Settings did not turn quiet mode off")
	}
}
