package main

// Tests for the battery-health report. The parser takes a reader, so a healthy
// pack, a worn one and a kernel with no cycle counter are all map literals.

import (
	"strings"
	"testing"
)

func batteryReader(files map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := files[name]
		return v, ok
	}
}

func TestParseBatteryHealthHealthy(t *testing.T) {
	h := parseBatteryHealth(batteryReader(map[string]string{
		"charge_full_design": "5000000",
		"charge_full":        "4800000",
		"cycle_count":        "42",
		"health":             "Good",
	}))
	if h.HealthPct != 96 {
		t.Fatalf("health = %d%%, want 96", h.HealthPct)
	}
	if h.DesignCapacity != 5000 || h.FullCapacity != 4800 {
		t.Fatalf("capacities = %d/%d mAh, want 4800/5000", h.FullCapacity, h.DesignCapacity)
	}
	if h.Cycles != 42 {
		t.Fatalf("cycles = %d, want 42", h.Cycles)
	}
	if len(h.Findings) != 0 {
		t.Fatalf("healthy pack produced findings: %v", h.Findings)
	}
	if line := batteryHealthLine(h); !strings.Contains(line, "96% health") || !strings.Contains(line, "4800/5000 mAh") {
		t.Fatalf("line = %q", line)
	}
}

func TestParseBatteryHealthWorn(t *testing.T) {
	h := parseBatteryHealth(batteryReader(map[string]string{
		"energy_full_design": "18500000",
		"energy_full":        "12950000", // 70%
		"cycle_count":        "612",
		"health":             "Overheat",
	}))
	if h.HealthPct != 70 {
		t.Fatalf("health = %d%%, want 70", h.HealthPct)
	}
	if len(h.Findings) != 3 {
		t.Fatalf("worn pack findings = %v, want capacity, cycles and heat", h.Findings)
	}
	joined := strings.Join(h.Findings, "\n")
	for _, want := range []string{"70% of design", "612 charge cycles", "Overheat"} {
		if !strings.Contains(joined, want) {
			t.Errorf("findings missing %q: %v", want, h.Findings)
		}
	}
}

func TestParseBatteryHealthWithoutCycleCounter(t *testing.T) {
	h := parseBatteryHealth(batteryReader(map[string]string{
		"charge_full_design": "5000000",
		"charge_full":        "5000000",
		"health":             "Good",
	}))
	if h.Cycles != -1 {
		t.Fatalf("cycles = %d, want -1 for an absent counter", h.Cycles)
	}
	if strings.Contains(batteryHealthLine(h), "cycles") {
		t.Fatalf("line names a cycle count the kernel does not expose: %q", batteryHealthLine(h))
	}
}

func TestParseBatteryHealthUnknownBattery(t *testing.T) {
	h := parseBatteryHealth(batteryReader(nil))
	if h.HealthPct != -1 || h.Cycles != -1 {
		t.Fatalf("empty battery = %+v", h)
	}
	if batteryHealthRelevant(h) {
		t.Fatal("an empty report must not be printed")
	}
	if got := batteryHealthLine(h); got != "unknown" {
		t.Fatalf("line = %q, want unknown", got)
	}
}

func TestParseBatteryHealthClampsAboveDesign(t *testing.T) {
	// A new cell can read a percent or two above its design capacity.
	h := parseBatteryHealth(batteryReader(map[string]string{
		"charge_full_design": "5000000",
		"charge_full":        "5100000",
	}))
	if h.HealthPct != 100 {
		t.Fatalf("health = %d%%, want it clamped to 100", h.HealthPct)
	}
}

func TestBatteryHealthFileReaderRejectsPaths(t *testing.T) {
	read := readBatteryHealthFile("/sys/class/power_supply/battery")
	if _, ok := read("../charge_full"); ok {
		t.Fatal("a path with a parent segment must not be read")
	}
	if _, ok := read(""); ok {
		t.Fatal("an empty name must not be read")
	}
}
