package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBatteryETAReadRichAXP2202Counters(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{
		"serial_number":      "xpower,axp2202\n",
		"capacity":           "71\n",
		"charge_counter":     "3550\n",
		"charge_full":        "5000\n",
		"energy_full_design": "5000\n",
		"time_to_empty_now":  "15000\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := batteryETAReadRich(dir)
	if r.chargeNowUAh != 3550000 || r.chargeFullUAh != 5000000 || r.energyFullUWh != 0 || r.tteRaw != 15000 {
		t.Fatalf("AXP2202 counters not normalized: %+v", r)
	}
}

func TestBatteryETAReadRichStandardCounters(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{
		"charge_now":         "3500000\n",
		"charge_full":        "5000000\n",
		"energy_full_design": "18000000\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := batteryETAReadRich(dir)
	if r.chargeNowUAh != 3500000 || r.chargeFullUAh != 5000000 || r.energyFullUWh != 18000000 {
		t.Fatalf("standard counters changed: %+v", r)
	}
}

func TestBatteryETAUnitForSmartProReading(t *testing.T) {
	lo, hi := batteryETAPlausible(71, false)
	if unit := batteryETAUnitFor(15000, lo, hi, 0, false); unit != 1 {
		t.Fatalf("15,000 at 71%% must mean seconds on this firmware, got %v seconds per unit", unit)
	}
}
