package main

// powermode_test.go — the named Power Mode selector. Its whole job is to
// answer for LowPower, ReducedMotion and the frame cadence from one value, so
// the tests pin the mapping, the cycle and the frame-rate override.

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestNormalizePowerMode(t *testing.T) {
	cases := [][]string{
		{"", PowerModeDefault},
		{"default", PowerModeDefault},
		{"  performance  ", PowerModePerformance},
		{"BATTERY", PowerModeBatterySaver},
		{"Balanced", PowerModeBalanced},
		{"turbo", PowerModeDefault}, // a typo must not invent a mode
		{"power", PowerModeDefault},
	}
	for _, tc := range cases {
		if got := normalizePowerMode(tc[0]); got != tc[1] {
			t.Errorf("normalize(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
}

func TestPowerModeLabels(t *testing.T) {
	cases := [][]string{
		{PowerModeDefault, "Default"},
		{PowerModeBatterySaver, "Battery Saver"},
		{PowerModeBalanced, "Balanced"},
		{PowerModePerformance, "Performance"},
		{"nonsense", "Default"},
	}
	for _, tc := range cases {
		if got := powerModeLabel(tc[0]); got != tc[1] {
			t.Errorf("label(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
}

// Each named mode answers for both levers; Default hands control back to the
// user's own toggles, including on a config that has never heard of Power Mode.
func TestEffectiveFlagsFollowMode(t *testing.T) {
	cases := []struct {
		mode                 string
		savedMotion, savedLP bool
		wantMotion, wantLP   bool
	}{
		{PowerModeDefault, false, false, false, false},
		{PowerModeDefault, true, true, true, true},
		{PowerModeBatterySaver, false, false, true, true}, // the overrides
		{PowerModeBatterySaver, true, true, true, true},
		{PowerModeBalanced, true, true, false, false},
		{PowerModePerformance, true, true, false, false},
	}
	for _, tc := range cases {
		cfg := &Config{Variables: Variables{
			ReducedMotion: tc.savedMotion,
			LowPower:      tc.savedLP,
			PowerMode:     tc.mode,
		}}
		if got := effectiveReducedMotion(cfg); got != tc.wantMotion {
			t.Errorf("mode %q: reduced motion = %v, want %v", tc.mode, got, tc.wantMotion)
		}
		if got := effectiveLowPower(cfg); got != tc.wantLP {
			t.Errorf("mode %q: low power = %v, want %v", tc.mode, got, tc.wantLP)
		}
		if tc.mode != PowerModeDefault {
			continue
		}
	}
	// A nil config is a Default config without levers: both flags off, motion
	// allowed — this is what a fresh test apartment runs.
	if effectiveLowPower(nil) || effectiveReducedMotion(nil) {
		t.Error("nil config must read Default, all levers off")
	}
	if motions := uiMotionEnabled(nil); !motions {
		t.Error("nil config must allow motion")
	}
}

// uiMotionEnabled is the shared gate: a named Battery Saver must silence
// motion even though the user's ReducedMotion toggle is still off.
func TestPowerModeGatesMotion(t *testing.T) {
	cfg := &Config{Variables: Variables{PowerMode: PowerModeBatterySaver}}
	if uiMotionEnabled(cfg) {
		t.Error("Battery Saver must gate motion")
	}
	cfg.Variables.PowerMode = PowerModePerformance
	if !uiMotionEnabled(cfg) {
		t.Error("Performance must keep motion")
	}
}

// The cycle walks Default → Battery Saver → Balanced → Performance and wraps
// back in both directions.
func TestNextPowerModeCyclesAndWraps(t *testing.T) {
	if got := nextPowerMode(PowerModeDefault, 1); got != PowerModeBatterySaver {
		t.Errorf("forward from Default = %q", got)
	}
	if got := nextPowerMode(PowerModePerformance, 1); got != PowerModeDefault {
		t.Errorf("forward from Performance = %q, want the wrap to Default", got)
	}
	if got := nextPowerMode(PowerModeDefault, -1); got != PowerModePerformance {
		t.Errorf("backward from Default = %q, want the wrap to Performance", got)
	}
	if got := cyclePowerModeTo(nil, "whatever"); got != "Default" {
		t.Errorf("cycle with a nil config reported %q, want Default", got)
	}
}

// Choosing a mode saves it and gives the frame rate controller the verdict.
func TestCyclePowerModeToSavesAndApplies(t *testing.T) {
	prev := frameRateCtrl
	frameRateCtrl = NewFrameRateController()
	t.Cleanup(func() { frameRateCtrl = prev })

	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	if got := cyclePowerModeTo(cfg, PowerModeBatterySaver); got != "Battery Saver" {
		t.Fatalf("cycle reported %q", got)
	}
	if cfg.Variables.PowerMode != PowerModeBatterySaver {
		t.Errorf("saved mode = %q", cfg.Variables.PowerMode)
	}
	if !frameRateCtrl.lowPower || frameRateCtrl.performancePinned {
		t.Errorf("controller lowPower=%v pinned=%v, want true/false",
			frameRateCtrl.lowPower, frameRateCtrl.performancePinned)
	}

	cyclePowerModeTo(cfg, PowerModePerformance)
	if frameRateCtrl.lowPower || !frameRateCtrl.performancePinned {
		t.Errorf("performance lowPower=%v pinned=%v, want false/true",
			frameRateCtrl.lowPower, frameRateCtrl.performancePinned)
	}

	cyclePowerModeTo(cfg, PowerModeDefault)
	if frameRateCtrl.lowPower || frameRateCtrl.performancePinned {
		t.Error("default mode must hand both levers back")
	}
}

// Performance keeps the fast cadence no matter how long the app sat idle;
// without it the controller drifts into deep idle as before.
func TestPerformancePinsTheCadence(t *testing.T) {
	fc := NewFrameRateController()
	fc.lastInputTime = sdl.GetTicks64() - uint64((time.Hour).Milliseconds())

	fc.Delay() // long idle: deep rate without Performance
	if mode := fc.CurrentMode(); mode != FrameRateDeepIdle {
		t.Errorf("idle mode = %v, want DeepIdle", mode)
	}

	fc.SetPerformance(true)
	fc.Delay()
	if mode := fc.CurrentMode(); mode != FrameRateActive {
		t.Errorf("pinned mode = %v, want Active", mode)
	}
	fc.SetPerformance(false)
	fc.Delay()
	if mode := fc.CurrentMode(); mode != FrameRateDeepIdle {
		t.Errorf("unpinned mode = %v, want DeepIdle again", mode)
	}
}

// Low Power still caps the delay at the idle profile without Performance:
// even deep idle returns the idle (not deep) delay while Low Power is on.
func TestLowPowerStillCapsWithoutPin(t *testing.T) {
	fc := NewFrameRateController()
	fc.lastInputTime = sdl.GetTicks64() - uint64((time.Hour).Milliseconds())
	frameProfile := fc.profile

	fc.SetLowPower(true)
	if d := fc.Delay(); d != frameProfile.Idle {
		t.Errorf("low-power delay = %dms, want the idle %dms", d, frameProfile.Idle)
	}
	fc.SetLowPower(false)
	if d := fc.Delay(); d != frameProfile.Deep {
		t.Errorf("deep-idle delay = %dms, want %dms", d, frameProfile.Deep)
	}
}

// inputVariableValue exposes the normalized mode so a label row or the web
// builder can show what is selected.
func TestInputVariableValuePowerMode(t *testing.T) {
	cfg := &Config{Variables: Variables{PowerMode: "BATTERY", Custom: map[string]interface{}{}}}
	if got := inputVariableValue(cfg, "powerMode"); got != PowerModeBatterySaver {
		t.Errorf("inputVariableValue = %q, want %q", got, PowerModeBatterySaver)
	}
}

// A saved mode survives the Variables decoder, and as a known key it never
// parks a duplicate in Custom.
func TestPowerModeSurvivesConfigRoundTrip(t *testing.T) {
	var v Variables
	if err := v.UnmarshalJSON([]byte(`{"powerMode":"performance"}`)); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if normalizePowerMode(v.PowerMode) != PowerModePerformance {
		t.Errorf("decoded powerMode = %q", v.PowerMode)
	}
	if _, ok := v.Custom["powerMode"]; ok {
		t.Error("known key leaked into Custom")
	}
}

// While a named mode is live, the pills for the levers it manages must show
// what actually happens, and pressing them must explain where the control
// lives instead of silently doing nothing.
func TestQuickSettingsManagedRows(t *testing.T) {
	prevQS := qs
	prevCFG := appConfig
	prevWrites := userConfigWritesEnabled
	userConfigWritesEnabled = false
	t.Cleanup(func() {
		qs = prevQS
		appConfig = prevCFG
		userConfigWritesEnabled = prevWrites
	})

	// The panel must be in its real row layout (the system tests build it
	// the same way); qsInit also verifies the wiring still finds the rows.
	qsInit()
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	// The user turned the levers off themselves, then picked Battery Saver.
	cfg.Variables.ReducedMotion = false
	cfg.Variables.LowPower = false
	cfg.Variables.PowerMode = PowerModeBatterySaver
	appConfig = cfg

	qsSyncFromConfig(cfg)
	lowIdx, motionIdx := -1, -1
	for i := range qs.toggles {
		switch qs.toggles[i].Action {
		case qsActionLowPower:
			lowIdx = i
		case qsActionReducedMotion:
			motionIdx = i
		}
	}
	if lowIdx < 0 || motionIdx < 0 {
		t.Fatal("quick settings lost the managed rows")
	}
	low, motion := &qs.toggles[lowIdx], &qs.toggles[motionIdx]
	if !low.Value || !motion.Value {
		t.Errorf("live pills = %v/%v, want both on under Battery Saver", low.Value, motion.Value)
	}

	// Stepping to Default must hand the pills back to the saved toggles.
	cyclePowerModeTo(cfg, PowerModeDefault)
	qsSyncFromConfig(cfg)
	if low.Value || motion.Value {
		t.Errorf("pills after Default = %v/%v, want both off", low.Value, motion.Value)
	}

	// And under Default the toggles flip again, so nothing was locked away.
	qsToggleRow(motionIdx)
	if !cfg.Variables.ReducedMotion {
		t.Error("the lever no longer answers after Default")
	}
	cfg.Variables.ReducedMotion = false

	// Back on a named mode, the press must not touch the raw flags.
	cfg.Variables.PowerMode = PowerModePerformance
	qsSyncFromConfig(cfg)
	raw := cfg.Variables.LowPower
	qsToggleRow(lowIdx)
	if cfg.Variables.LowPower != raw {
		t.Errorf("managed press changed LowPower %v -> %v", raw, cfg.Variables.LowPower)
	}
}
