//go:build !linux

package main

// powercheck_other.go — the battery life check off Linux: no /proc to read,
// so the CPU columns say "not measured" (powercheck.go).

const powerCheckTicksPerSecond = 100

func powerCheckCPUNow() (powerCPUStat, bool) { return powerCPUStat{}, false }
