//go:build !linux

package main

// xboxboost_other.go — Xbox boost is the handheld's (xboxboost.go): off Linux
// there are no cores, GPU nodes, priorities or radios for it to touch.

func xboxBoostHardwarePlan(target string) []saverSysfsWrite { return nil }

func xboxBoostRenice(pid int) error { return nil }

func xboxBoostWiFiOff() []string { return nil }

func xboxBoostWiFiOn(radios []string) {}
