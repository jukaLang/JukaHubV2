package main

// adaptive_test.go pins the frame-rate policy: the handheld profile must cap
// the cadence below 60fps (the TSP cannot sustain it), and LowPower must pick
// the slowest cadence instead of being mistaken for an animation.

import "testing"

func TestFrameProfileCapsHandheldCadence(t *testing.T) {
	handheld := frameProfileFor(true)
	desktop := frameProfileFor(false)

	for _, mode := range []FrameRateMode{FrameRateActive, FrameRateIdle, FrameRateDeepIdle} {
		if d := handheld.delayFor(mode, false); d < 33 {
			t.Errorf("handheld %v delay = %dms, want at least 33ms (~30fps cap)", mode, d)
		}
	}
	if handheld.Active <= desktop.Active {
		t.Errorf("handheld active delay %dms must be longer than desktop %dms", handheld.Active, desktop.Active)
	}
	if !(handheld.Active <= handheld.Idle && handheld.Idle <= handheld.Deep) {
		t.Errorf("handheld cadence must get slower when idle: %+v", handheld)
	}
	if got := handheld.fps(FrameRateActive); got > 30 {
		t.Errorf("handheld active cadence = %dfps, want at most 30", got)
	}
	if got := desktop.fps(FrameRateActive); got < 50 {
		t.Errorf("desktop active cadence = %dfps, want a full-speed profile", got)
	}
}

func TestLowPowerCapsActiveCadence(t *testing.T) {
	for _, handheld := range []bool{true, false} {
		p := frameProfileFor(handheld)
		for _, mode := range []FrameRateMode{FrameRateActive, FrameRateIdle, FrameRateDeepIdle} {
			if got := p.delayFor(mode, true); got != p.Idle {
				t.Errorf("handheld=%v %v with low power = %dms, want the idle cap %dms", handheld, mode, got, p.Idle)
			}
		}
	}
}

// LowPower used to be passed to SetAnimationActive, which forced 60fps and
// made the setting do the opposite of its name. It must win over an active
// animation.
func TestLowPowerBeatsAnimation(t *testing.T) {
	fc := &FrameRateController{
		profile:       frameProfileFor(true),
		currentMode:   FrameRateDeepIdle,
		deepThreshold: 1 << 60, // stay out of the deep-idle branch for this test
	}
	fc.SetLowPower(true)
	fc.SetAnimationActive(true)
	if got := fc.Delay(); got != fc.profile.Idle {
		t.Fatalf("Delay with LowPower and an animation = %dms, want the idle cap %dms", got, fc.profile.Idle)
	}
}
