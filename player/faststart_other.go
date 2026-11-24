//go:build !linux

package main

// faststart_other.go — the card's read-ahead and the step back of Faster game
// starts are the handheld's (faststart_linux.go); the launch batch
// (racfgscope.go) works everywhere.

func fastStartReadAheadPlan(paths ...string) []saverSysfsWrite { return nil }

func fastStartApply(plan []saverSysfsWrite) []saverSysfsWrite { return nil }

func fastStartStepBack() int { return 0 }

func fastStartStepForward() {}
