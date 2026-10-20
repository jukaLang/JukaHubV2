package main

// batteryhealth.go — battery wear, for the Diagnostics report.
//
// The charge percentage says nothing about wear: a worn cell reaches "100%"
// while holding a fraction of its design capacity, and that is what makes a
// handheld die after forty minutes. The kernel already exposes the numbers, so
// Diagnostics reports them and says when they matter.
//
// Everything here is read-only. Kernel files differ between devices — some
// expose charge_* in µAh, others energy_* in µWh — so the parser takes a reader
// and both naming schemes, and a missing file is "unknown" rather than zero.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type batteryHealth struct {
	DesignCapacity int // mAh (charge_*) or mWh (energy_*)
	FullCapacity   int // same unit as DesignCapacity
	HealthPct      int // -1 when the kernel does not expose both capacities
	Cycles         int // -1 when cycle_count is absent
	Status         string
	Findings       []string
}

// batteryHealthThresholdPct is where a cell is worn enough to mention.
const batteryHealthThresholdPct = 80

// batteryHealthCycleThreshold is a soft warning line; Li-ion packs are usually
// rated for 300–500 full cycles.
const batteryHealthCycleThreshold = 500

// parseBatteryHealth builds the report from a reader, so every edge case is a
// map literal in a test instead of a device that has to be worn out first.
func parseBatteryHealth(read func(name string) (string, bool)) batteryHealth {
	h := batteryHealth{HealthPct: -1, Cycles: -1}

	number := func(name string) (int, bool) {
		raw, ok := read(name)
		if !ok {
			return 0, false
		}
		v, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || v <= 0 {
			return 0, false
		}
		return v, true
	}

	// Charge-based batteries report µAh; energy-based ones µWh. Both are scaled
	// to whole units so the report reads in mAh/mWh rather than millions.
	design, designOK := number("charge_full_design")
	full, fullOK := number("charge_full")
	if !designOK || !fullOK {
		if d, ok := number("energy_full_design"); ok {
			if f, ok2 := number("energy_full"); ok2 {
				design, full, designOK, fullOK = d, f, true, true
			}
		}
	}
	if designOK {
		h.DesignCapacity = design / 1000
	}
	if fullOK {
		h.FullCapacity = full / 1000
	}
	if designOK && fullOK && design > 0 {
		pct := (full*100 + design/2) / design
		if pct > 100 {
			pct = 100 // a fresh cell can read slightly above design
		}
		h.HealthPct = pct
	}

	if cycles, ok := number("cycle_count"); ok {
		h.Cycles = cycles
	}
	if status, ok := read("health"); ok {
		h.Status = strings.TrimSpace(status)
	}

	if h.HealthPct >= 0 && h.HealthPct < batteryHealthThresholdPct {
		h.Findings = append(h.Findings, fmt.Sprintf(
			"Battery capacity is %d%% of design (%d of %d mAh) — runtime is reduced; consider replacing the pack.",
			h.HealthPct, h.FullCapacity, h.DesignCapacity))
	}
	if h.Cycles >= batteryHealthCycleThreshold {
		h.Findings = append(h.Findings, fmt.Sprintf(
			"Battery has %d charge cycles (packs are usually rated for 300–500).", h.Cycles))
	}
	switch strings.ToLower(h.Status) {
	case "overheat", "dead", "failure", "cold":
		h.Findings = append(h.Findings, "Battery reports health \""+h.Status+"\" — stop charging it and have the pack checked.")
	}
	return h
}

// readBatteryHealthFile reads one file inside the detected battery directory.
// Only direct children with plain names are read, so nothing here can follow a
// path from the config.
func readBatteryHealthFile(dir string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if dir == "" || name == "" || strings.ContainsAny(name, `/\`) {
			return "", false
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", false
		}
		return string(data), true
	}
}

// batteryHealthReport reads the device's battery. A desktop without a battery
// returns an empty report, which Diagnostics simply omits.
func batteryHealthReport() batteryHealth {
	return parseBatteryHealth(readBatteryHealthFile(batterySysfsDir()))
}

// batteryHealthRelevant reports whether there is anything to print.
func batteryHealthRelevant(h batteryHealth) bool {
	return h.DesignCapacity > 0 || h.FullCapacity > 0 || h.HealthPct >= 0 || h.Cycles >= 0 || h.Status != ""
}

// batteryHealthLine is the one-line Diagnostics form.
func batteryHealthLine(h batteryHealth) string {
	var parts []string
	if h.HealthPct >= 0 {
		parts = append(parts, fmt.Sprintf("%d%% health", h.HealthPct))
	}
	if h.DesignCapacity > 0 && h.FullCapacity > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d mAh", h.FullCapacity, h.DesignCapacity))
	}
	if h.Cycles >= 0 {
		parts = append(parts, fmt.Sprintf("%d cycles", h.Cycles))
	}
	if strings.TrimSpace(h.Status) != "" {
		parts = append(parts, h.Status)
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, ", ")
}
