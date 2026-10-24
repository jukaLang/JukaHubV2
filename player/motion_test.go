package main

import (
	"encoding/json"
	"testing"
)

// The motion model (motion.go): one config field per motion role, presets
// that fill all four, and defaults that keep every config written before the
// fields existed behaving exactly as before.

func TestNormalizeMotionConfigDefaults(t *testing.T) {
	var cfg Config
	normalizeMotionConfig(&cfg)
	if cfg.Variables.Transition != TransitionFade {
		t.Errorf("default transition = %q, want %q", cfg.Variables.Transition, TransitionFade)
	}
	if cfg.Variables.FocusMotion != FocusMotionScale {
		t.Errorf("default focusMotion = %q, want %q", cfg.Variables.FocusMotion, FocusMotionScale)
	}
	if cfg.Variables.IdleMotion != IdleMotionAmbient {
		t.Errorf("default idleMotion = %q, want %q", cfg.Variables.IdleMotion, IdleMotionAmbient)
	}
	if cfg.Variables.ListScroll != ListScrollSnap {
		t.Errorf("default listScroll = %q, want %q", cfg.Variables.ListScroll, ListScrollSnap)
	}
}

func TestNormalizeMotionConfigUnknownFallsBack(t *testing.T) {
	cfg := &Config{}
	cfg.Variables.Transition = "flip"
	cfg.Variables.FocusMotion = "glow"   // renderer not built yet -> scale
	cfg.Variables.IdleMotion = "orbit"   // unknown -> ambient
	cfg.Variables.ListScroll = "elastic" // unknown -> snap
	normalizeMotionConfig(cfg)
	if cfg.Variables.Transition != TransitionFade ||
		cfg.Variables.FocusMotion != FocusMotionScale ||
		cfg.Variables.IdleMotion != IdleMotionAmbient ||
		cfg.Variables.ListScroll != ListScrollSnap {
		t.Errorf("unknown values = %q/%q/%q/%q, want all defaults",
			cfg.Variables.Transition, cfg.Variables.FocusMotion,
			cfg.Variables.IdleMotion, cfg.Variables.ListScroll)
	}
}

func TestNormalizeMotionConfigKeepsKnownValues(t *testing.T) {
	cfg := &Config{}
	cfg.Variables.Transition = TransitionInstant
	cfg.Variables.FocusMotion = FocusMotionNone
	cfg.Variables.IdleMotion = IdleMotionDrift
	cfg.Variables.ListScroll = ListScrollAccelerated
	normalizeMotionConfig(cfg)
	if cfg.Variables.Transition != TransitionInstant ||
		cfg.Variables.FocusMotion != FocusMotionNone ||
		cfg.Variables.IdleMotion != IdleMotionDrift ||
		cfg.Variables.ListScroll != ListScrollAccelerated {
		t.Errorf("known values were rewritten: %+v", cfg.Variables)
	}
}

func TestApplyMotionPresets(t *testing.T) {
	cases := map[string]struct {
		Transition, FocusMotion, IdleMotion, ListScroll string
	}{
		MotionPresetCalm:       {TransitionFade, FocusMotionNone, IdleMotionNone, ListScrollSnap},
		MotionPresetCinematic:  {TransitionFade, FocusMotionScale, IdleMotionBreathe, ListScrollFree},
		MotionPresetSnappy:     {TransitionInstant, FocusMotionScale, IdleMotionNone, ListScrollAccelerated},
		MotionPresetReducedAll: {TransitionInstant, FocusMotionNone, IdleMotionNone, ListScrollSnap},
		MotionPresetDefault:    {TransitionFade, FocusMotionScale, IdleMotionAmbient, ListScrollSnap},
	}
	for preset, want := range cases {
		cfg := &Config{}
		if !ApplyMotionPreset(cfg, preset) {
			t.Fatalf("preset %q rejected", preset)
		}
		if cfg.Variables.Transition != want.Transition ||
			cfg.Variables.FocusMotion != want.FocusMotion ||
			cfg.Variables.IdleMotion != want.IdleMotion ||
			cfg.Variables.ListScroll != want.ListScroll {
			t.Errorf("preset %q = %+v, want %+v", preset, cfg.Variables, want)
		}
	}
	cfg := &Config{}
	if ApplyMotionPreset(cfg, "warp-speed") {
		t.Errorf("unknown preset %q accepted", "warp-speed")
	}
}

func TestPresetConfigsRoundTripThroughNormalize(t *testing.T) {
	for _, preset := range []string{MotionPresetCalm, MotionPresetSnappy, MotionPresetReducedAll} {
		cfg := &Config{}
		if !ApplyMotionPreset(cfg, preset) {
			t.Fatalf("preset %q rejected", preset)
		}
		before := cfg.Variables
		normalizeMotionConfig(cfg)
		if cfg.Variables.Transition != before.Transition ||
			cfg.Variables.FocusMotion != before.FocusMotion ||
			cfg.Variables.IdleMotion != before.IdleMotion ||
			cfg.Variables.ListScroll != before.ListScroll {
			t.Errorf("preset %q did not survive normalization: %+v -> %+v",
				preset, before, cfg.Variables)
		}
	}
}

func TestMotionFocusEnabledFollowsConfig(t *testing.T) {
	if !motionFocusEnabled(nil) {
		t.Error("nil config should keep focus motion (default)")
	}
	cfg := &Config{}
	if !motionFocusEnabled(cfg) {
		t.Error("unset focusMotion should keep the scale lift")
	}
	cfg.Variables.FocusMotion = FocusMotionNone
	if motionFocusEnabled(cfg) {
		t.Error("focusMotion none should drop the eased lift")
	}
}

// Motion JSON round trip: the builder and hand-edited configs must read back
// the same per-role fields they wrote.
func TestMotionFieldsRoundTripJSON(t *testing.T) {
	const raw = `{"variables":{
		"transition":"instant","focusMotion":"none",
		"idleMotion":"breathe","listScroll":"accelerated"}}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Variables.Transition != TransitionInstant ||
		cfg.Variables.FocusMotion != FocusMotionNone ||
		cfg.Variables.IdleMotion != IdleMotionBreathe ||
		cfg.Variables.ListScroll != ListScrollAccelerated {
		t.Errorf("round trip lost motion fields: %+v", cfg.Variables)
	}
}
