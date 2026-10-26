package main

// onboarding_test.go — the first-run tour's per-step status lines and action
// labels. These are the strings the tour shows next to each step, including the
// time-zone line that reads the OS clock.

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
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
	for _, kind := range []string{"wifi", "timezone", "theme", "roms", "artwork", "tools"} {
		if got := onboardingActionLabel(kind); strings.TrimSpace(got) == "" {
			t.Errorf("step %q has no action label", kind)
		}
	}
	if got := onboardingActionLabel(""); got != "" {
		t.Errorf("plain step action label = %q, want none", got)
	}
}

// TestTourToolsStepRenders draws the last step on both the panel and a 480p
// handheld. The step's body is the longest in the tour, so a card that cannot
// take it would show up here rather than on a device.
func TestTourToolsStepRenders(t *testing.T) {
	prevW, prevH := screenWidth, screenHeight
	prevOnboarding := onboarding
	prevTools, prevQS := toolsPanel, qs
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		onboarding = prevOnboarding
		toolsPanel, qs = prevTools, prevQS
	})
	toolsPanel.open = false
	onboardingStart()
	onboarding.step = len(onboardingSteps) - 1
	cfg := onboardingTestConfig()

	for _, size := range []struct{ w, h int32 }{{probeW, probeH}, {800, 480}} {
		screenWidth, screenHeight = size.w, size.h
		renderFunProbe(t, func(r *sdl.Renderer) { renderOnboarding(r, cfg) })
		if onboarding.next.W == 0 {
			t.Fatalf("%dx%d: the step recorded no Next button", size.w, size.h)
		}
		if onboarding.action.W == 0 {
			t.Fatalf("%dx%d: the tools step lost its action button", size.w, size.h)
		}
	}
}

// TestTourPointsAtToolsPages: the tour ends on the two Tools pages a new user
// cannot guess. The step names both, reports their live state (so the panel's
// install count and posture are visible before it is opened), and its action
// opens the Tools panel instead of describing it.
func TestTourPointsAtToolsPages(t *testing.T) {
	prevOnboarding := onboarding
	prevTools := toolsPanel
	prevQS := qs
	t.Cleanup(func() {
		onboarding = prevOnboarding
		toolsPanel = prevTools
		qs = prevQS
	})

	step := -1
	for i, s := range onboardingSteps {
		if s.kind == "tools" {
			step = i
		}
	}
	if step < 0 {
		t.Fatal("the tour has no step naming the Toolbox and Security & trust")
	}
	if step != len(onboardingSteps)-1 {
		t.Errorf("the tools step is %d of %d, want the last step", step+1, len(onboardingSteps))
	}
	for _, word := range []string{"Toolbox", "Security & trust"} {
		if !strings.Contains(onboardingSteps[step].body, word) {
			t.Errorf("the step's body does not mention %q", word)
		}
	}

	// Recorded rather than assumed: the package's other probes may leave a
	// modal open, and this test only owns the one it opens itself.
	coveredBefore := onboardingCoveredByOverlay()

	cfg := onboardingTestConfig()
	status := onboardingStatus(cfg, "tools")
	if !strings.Contains(status, "Toolbox: ") || !strings.Contains(status, "Security: ") {
		t.Errorf("tools status = %q, want both pages", status)
	}

	onboardingStart()
	onboarding.step = step
	onboardingPerformAction(cfg)
	if !toolsPanel.open {
		t.Fatal("the tools step's action did not open the Tools panel")
	}
	// The panel is a modal, so the tour must step aside while it is open and
	// come back to the same step afterwards.
	if !onboardingCoveredByOverlay() {
		t.Fatal("an open Tools panel did not cover the tour")
	}
	toolsPanel.open = false
	if got := onboardingCoveredByOverlay(); got != coveredBefore {
		t.Fatal("the tour is still covered after the Tools panel closed")
	}
	if onboarding.step != step || !onboarding.active {
		t.Fatal("closing Tools did not leave the tour on its tools step")
	}
}
