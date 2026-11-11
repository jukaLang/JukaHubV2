package main

// Tests for bootOSMotion (bootos.go): the boot moves unless Reduce motion was
// chosen by hand or a battery saver is on — the handheld's out-of-box Reduce
// motion does not still it.

import "testing"

func TestBootMotionIgnoresTheOutOfBoxReduceMotion(t *testing.T) {
	prevSaver := batterySaverActive
	t.Cleanup(func() { batterySaverActive = prevSaver })
	batterySaverActive = false

	if !bootOSMotion(nil) {
		t.Fatal("no config, yet the boot is still")
	}
	c := &Config{}
	if !bootOSMotion(c) {
		t.Fatal("Reduce motion is off, yet the boot is still")
	}

	// Reduce motion on as it comes out of the box: the boot still moves away
	// from Windows, where that is the handheld's default rather than a choice.
	c.Variables.ReducedMotion = true
	if got, want := bootOSMotion(c), !IsWindows(); got != want {
		t.Fatalf("out-of-box Reduce motion: boot moves = %v, want %v", got, want)
	}

	// Switched by hand: the boot follows it.
	c.Variables.Custom = map[string]interface{}{bootOSMotionChosenKey: "true"}
	if bootOSMotion(c) {
		t.Fatal("Reduce motion chosen by hand did not still the boot")
	}
	c.Variables.ReducedMotion = false
	if !bootOSMotion(c) {
		t.Fatal("Reduce motion switched off by hand stilled the boot")
	}

	// A battery saver stills it whatever the switch says.
	c.Variables.Custom = nil
	batterySaverActive = true
	if bootOSMotion(c) {
		t.Fatal("the battery saver did not still the boot")
	}
}
