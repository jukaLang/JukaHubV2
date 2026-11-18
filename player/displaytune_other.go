//go:build !linux

package main

// displaytune_other.go — no panel picture nodes off Linux: JukaHub draws
// contrast, exposure and colour temperature itself (displaytune_soft.go) and
// the Saturation row stays greyed with the reason.

import "errors"

func displayTuneProbe(string) bool { return false }

func displayTuneRead(string) (int, bool) { return 0, false }

func displayTuneWrite(string, int) error {
	return errors.New("the display driver's picture settings are Linux only")
}
