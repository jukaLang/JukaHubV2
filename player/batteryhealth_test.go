package main

import (
	"strings"
	"testing"
)

func batteryHealthMapRead(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
}

func TestBatteryHealthAXP2202UsesNominalMAhWithoutClaimingWear(t *testing.T) {
	// These are the Smart Pro's stock AXP2202 readings at 71% charge.
	h := parseBatteryHealth(batteryHealthMapRead(map[string]string{
		"serial_number":      "xpower,axp2202\n",
		"energy_full_design": "5000\n",
		"charge_full":        "5000\n",
		"charge_counter":     "3550\n",
		"capacity":           "71\n",
		"health":             "Good\n",
		"temp":               "365\n",
	}))
	if h.DesignCapacity != 5000 || h.FullCapacity != 0 || h.HealthPct != -1 {
		t.Fatalf("AXP2202 capacity must be nominal, not a wear measurement: %+v", h)
	}
	if got := batteryHealthLine(h); !strings.Contains(got, "5000 mAh pack") || strings.Contains(got, "5/5000") {
		t.Fatalf("unexpected AXP2202 diagnostics line: %q", got)
	}
}

func TestBatteryHealthStandardMicroampHoursStillReportsWear(t *testing.T) {
	h := parseBatteryHealth(batteryHealthMapRead(map[string]string{
		"charge_full_design": "5000000",
		"charge_full":        "4000000",
	}))
	if h.DesignCapacity != 5000 || h.FullCapacity != 4000 || h.HealthPct != 80 {
		t.Fatalf("standard microamp-hour counters changed: %+v", h)
	}
}
