package main

// onboarding_test.go — the first-run tour's per-step status lines and action
// labels. These are the strings the tour shows next to each step, including the
// time-zone line that reads the OS clock.

import (
	"strings"
	"testing"
)

func onboardingTestConfig() *Config {
	return &Config{Variables: Variables{Custom: map[string]interface{}{}}}
}

func TestOnboardingStatusLines(t *testing.T) {
	cfg := onboardingTestConfig()

	if got := onboardingStatus(cfg, "timezone"); !strings.HasPrefix(got, "Current time zone: ") || strings.TrimSpace(strings.TrimPrefix(got, "Current time zone: ")) == "" {
		t.Errorf("timezone status = %q", got)
	}
	if got := onboardingStatus(cfg, "wifi"); !strings.HasPrefix(got, "Network: ") {
		t.Errorf("wifi status = %q", got)
	}

	// The default theme is named rather than left blank, and a chosen preset
	// wins over it.
	if got := onboardingStatus(cfg, "theme"); got != "Current theme: dark" {
		t.Errorf("default theme status = %q", got)
	}
	cfg.Variables.Custom["theme_preset"] = "oled"
	if got := onboardingStatus(cfg, "theme"); got != "Current theme: oled" {
		t.Errorf("chosen theme status = %q", got)
	}

	// Before a scan the ROM step says so; after one it counts what was found.
	prevRoms := onboarding.roms
	t.Cleanup(func() { onboarding.roms = prevRoms })
	onboarding.roms = nil
	if got := onboardingStatus(cfg, "roms"); !strings.Contains(got, "not run yet") {
		t.Errorf("unscanned ROM status = %q", got)
	}
	onboarding.roms = &romScanReport{
		Games:  []scannedROM{{Path: "a.gba", System: "GBA"}, {Path: "b.gba", System: "GBA"}},
		Counts: map[string]int{"GBA": 2},
	}
	if got := onboardingStatus(cfg, "roms"); !strings.Contains(got, "2 ROM(s)") || !strings.Contains(got, "1 system") {
		t.Errorf("scanned ROM status = %q", got)
	}

	if got := onboardingStatus(cfg, ""); got != "" {
		t.Errorf("step without a status returned %q", got)
	}
}

func TestOnboardingActionLabels(t *testing.T) {
	for _, kind := range []string{"wifi", "timezone", "theme", "roms", "artwork"} {
		if got := onboardingActionLabel(kind); strings.TrimSpace(got) == "" {
			t.Errorf("step %q has no action label", kind)
		}
	}
	if got := onboardingActionLabel(""); got != "" {
		t.Errorf("plain step action label = %q, want none", got)
	}
}
