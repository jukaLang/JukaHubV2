//go:build !linux

package main

// xboxboost_hub_other.go — JukaHub stepping back and the fast cluster are the
// handheld's (xboxboost_hub_linux.go).

func xboxBoostStepBack() int { return 0 }

func xboxBoostStepForward() {}

func xboxBoostPin(pid int) string { return "" }
