package main

// motion.go — motion is a config property, not a build constant.
//
// The shell used to have one motion opinion: everything eased, and only the
// global ReducedMotion switch could switch it off. Motion presets were a
// "reduced motion" bool plus a string wealth of hardcoded durations.
//
// The motion model now has one field per motion role, each with a small set of
// renderable behaviors (more may be added by layouts and presets later):
//
//   transition:  instant | fade — how scene changes arrive
//   focusMotion: scale | none   — how the focused card answers the D-pad
//   idleMotion:  none | breathe | drift | ambient — the idle screen's motion
//   listScroll:  snap | free | accelerated — list scrolling feel
//
// An empty value means "the default for this build", so every config written
// before this field existed keeps its current behavior. Unknown spellings are
// normalized to the default rather than rejected: a handheld config written by
// hand or by the builder must never stop JukaHub from starting.

import "strings"

// enum values for Variables.Transition
const (
	TransitionInstant = "instant"
	TransitionFade    = "fade"
)

// enum values for Variables.FocusMotion
const (
	FocusMotionScale = "scale"
	FocusMotionNone  = "none"
)

// enum values for Variables.IdleMotion
const (
	IdleMotionNone    = "none"
	IdleMotionBreathe = "breathe"
	IdleMotionDrift   = "drift"
	IdleMotionAmbient = "ambient"
)

// enum values for Variables.ListScroll
const (
	ListScrollSnap        = "snap"
	ListScrollFree        = "free"
	ListScrollAccelerated = "accelerated"
)

// Motion presets. A preset is an opinion about all four roles at once, so a
// layout, a theme or the builder can set one string instead of four fields.
// The values mirror the design pillars: calm is flat, cinematic is rich,
// snappy keeps focus motion but removes waiting, default is the build's
// current behavior.
const (
	MotionPresetDefault    = "default"
	MotionPresetCalm       = "calm"
	MotionPresetCinematic  = "cinematic"
	MotionPresetSnappy     = "snappy"
	MotionPresetReducedAll = "reduced-motion"
)

// motionRoleValue resolves one role's value to a known enum, mapping both
// "unset" and "unknown" to defaultValue.
func motionRoleValue(value, defaultValue string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return defaultValue
	}
	return v
}

// normalizeMotionConfig canonicalizes the motion fields in place. It runs from
// finalizeConfig, so the same defaults apply to configs loaded from disk, from
// the XML importer, and from configs built up in tests.
func normalizeMotionConfig(cfg *Config) {
	if cfg == nil {
		return
	}
	switch t := motionRoleValue(cfg.Variables.Transition, TransitionFade); t {
	case TransitionInstant, TransitionFade:
		cfg.Variables.Transition = t
	default:
		cfg.Variables.Transition = TransitionFade
	}
	switch f := motionRoleValue(cfg.Variables.FocusMotion, FocusMotionScale); f {
	case FocusMotionScale, FocusMotionNone:
		cfg.Variables.FocusMotion = f
	default:
		// glow/outline have renderers in the design slate but not in this
		// build yet; they read as scale rather than vanishing the lift.
		cfg.Variables.FocusMotion = FocusMotionScale
	}
	switch i := motionRoleValue(cfg.Variables.IdleMotion, IdleMotionAmbient); i {
	case IdleMotionNone, IdleMotionBreathe, IdleMotionDrift, IdleMotionAmbient:
		cfg.Variables.IdleMotion = i
	default:
		// The default follows the existing screensaver/ambient toggles, which is
		// what "" meant before this field existed.
		cfg.Variables.IdleMotion = IdleMotionAmbient
	}
	switch s := motionRoleValue(cfg.Variables.ListScroll, ListScrollSnap); s {
	case ListScrollSnap, ListScrollFree, ListScrollAccelerated:
		cfg.Variables.ListScroll = s
	default:
		cfg.Variables.ListScroll = ListScrollSnap
	}
}

// motionInstant reports whether scene changes should snap instead of fading.
func motionInstant(cfg *Config) bool {
	return cfg != nil && cfg.Variables.Transition == TransitionInstant
}

// motionFocusEnabled reports whether the focused card's eased scale lift is
// allowed. focusMotion none keeps the highlight (colour, press feedback) but
// drops the growth, which is what motion-sensitive users ask for without
// losing focus visibility.
func motionFocusEnabled(cfg *Config) bool {
	return cfg == nil || cfg.Variables.FocusMotion != FocusMotionNone
}
