//go:build !linux

package main

// gpuboost_other.go — GPU boost is the handheld's (gpuboost.go): off Linux
// there is no devfreq node for it to raise.

func gpuBoostPlan() []saverSysfsWrite { return nil }

func gpuBoostNodePresent() bool { return false }
