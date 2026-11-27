package main

// powerprofile_profiles_test.go — PWR-6's per-system and per-power-source
// profiles: the most specific profile wins, a game's own CPU limit still wins
// over every profile, and the options sheet names the profile in force.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPowerProfilesChooseTheMostSpecific(t *testing.T) {
	cfg := powerProfileConfig{
		Enabled:        true,
		MaxFreqKHz:     1_500_000,
		OnBattery:      "battery",
		OnCharger:      "performance",
		SystemProfiles: map[string]string{"psp": "performance", "game boy advance": "balanced"},
	}
	type want struct {
		balanced bool
		khz      int64
		origin   string
	}
	cases := []struct {
		name        string
		opts        gameOptions
		system      string
		onBattery   bool
		sourceKnown bool
		want        want
	}{
		{"global", gameOptions{}, "", false, false, want{false, 1_500_000, "global"}},
		{"battery source", gameOptions{}, "", true, true, want{true, gameCPUBatteryCeilingKHz, "on battery"}},
		{"charger source", gameOptions{}, "", false, true, want{false, 0, "on charger"}},
		{"system beats source", gameOptions{}, "Game Boy Advance", true, true, want{true, 1_500_000, "Game Boy Advance"}},
		{"system is case-insensitive", gameOptions{}, "psp", true, true, want{false, 0, "psp"}},
		{"game beats system", gameOptions{CPUProfile: "balanced"}, "PSP", false, true, want{true, 1_500_000, "this game"}},
		{"game limit is last", gameOptions{MaxFreqKHz: 1_800_000}, "Game Boy Advance", true, true, want{true, 1_800_000, "Game Boy Advance"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, origin := launchPowerProfileConfig(cfg, tc.opts, tc.system, tc.onBattery, tc.sourceKnown)
			if got.Balanced != tc.want.balanced || got.MaxFreqKHz != tc.want.khz || origin != tc.want.origin {
				t.Fatalf("got balanced=%v ceiling=%d origin=%q; want balanced=%v ceiling=%d origin=%q",
					got.Balanced, got.MaxFreqKHz, origin, tc.want.balanced, tc.want.khz, tc.want.origin)
			}
		})
	}
}

func TestPowerProfileConfigReadsSystemAndSourceKeys(t *testing.T) {
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		"powerProfileOnBattery": "battery",
		"powerProfileOnCharger": "performance",
		"powerProfileSystems": map[string]interface{}{
			"PSP":              "performance",
			"Game Boy Advance": "balanced",
			"broken":           "fastest",
		},
	}}}
	cfg := loadPowerProfileConfig(config)
	if cfg.OnBattery != "battery" || cfg.OnCharger != "performance" {
		t.Fatalf("source profiles = %q/%q", cfg.OnBattery, cfg.OnCharger)
	}
	if got := cfg.SystemProfiles["psp"]; got != "performance" {
		t.Fatalf("PSP profile = %q", got)
	}
	if got := cfg.SystemProfiles["game boy advance"]; got != "balanced" {
		t.Fatalf("GBA profile = %q", got)
	}
	if _, ok := cfg.SystemProfiles["broken"]; ok {
		t.Fatal("an unknown profile name was kept")
	}

	// The custom editor needs a spelling it can type, not only a JSON object.
	text := loadPowerProfileConfig(&Config{Variables: Variables{Custom: map[string]interface{}{
		"powerProfileSystems": "PSP=performance; GBA=balanced",
	}}})
	if text.SystemProfiles["psp"] != "performance" || text.SystemProfiles["gba"] != "balanced" {
		t.Fatalf("text profiles = %+v", text.SystemProfiles)
	}
}

func TestGameOptionsNamesTheProfileInForce(t *testing.T) {
	cfg := powerProfileConfig{
		Enabled:        true,
		MaxFreqKHz:     1_500_000,
		OnBattery:      "battery",
		OnCharger:      "performance",
		SystemProfiles: map[string]string{"psp": "performance"},
	}
	if got := gameOptionsCPUProfileValueFor(gameOptions{}, "PSP", cfg, true, true); got != "Auto · Performance (PSP)" {
		t.Fatalf("system label = %q", got)
	}
	if got := gameOptionsCPUProfileValueFor(gameOptions{}, "GBA", cfg, true, true); got != "Auto · Battery saver (on battery)" {
		t.Fatalf("battery label = %q", got)
	}
	if got := gameOptionsCPUProfileValueFor(gameOptions{}, "GBA", cfg, false, true); got != "Auto · Performance (on charger)" {
		t.Fatalf("charger label = %q", got)
	}
	if got := gameOptionsCPUProfileValueFor(gameOptions{CPUProfile: "balanced"}, "PSP", cfg, true, true); got != "Balanced" {
		t.Fatalf("game label = %q", got)
	}
}

// The system profile must reach the real launch path, not only the resolver:
// beginLaunchPowerProfile sees a ROM path, names its console and applies that
// console's profile to the fake cpufreq tree.
func TestLaunchAppliesTheSystemProfileFromTheROMPath(t *testing.T) {
	target := filepath.Join("Roms", "PSP", "Game.iso")
	system := powerLaunchSystem(target)
	if system == "" {
		t.Fatal("powerLaunchSystem did not name the PSP folder")
	}
	root := t.TempDir()
	gov := fakeCPUWithFreq(t, root, 0, "1608000", "2000000", true)
	if err := os.WriteFile(gov, []byte("ondemand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevRoot := cpufreqRoot
	cpufreqRoot = root
	resetLaunchPowerProfile()
	t.Cleanup(func() {
		cpufreqRoot = prevRoot
		resetLaunchPowerProfile()
	})
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		"powerProfileMaxFreq": int64(1_500_000),
		"powerProfileSystems": map[string]interface{}{system: "balanced"},
	}}}
	release := beginLaunchPowerProfile(config, target)
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "schedutil" {
		t.Fatalf("governor during a %s launch = %q, want the system's balanced schedutil", system, raw)
	}
	max := filepath.Join(filepath.Dir(gov), "scaling_max_freq")
	if got := readKHzFile(max); got != 1_500_000 {
		t.Fatalf("ceiling = %d, want the global 1.5 GHz", got)
	}
	release()
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "ondemand" {
		t.Fatalf("restored governor = %q, want the boot ondemand", raw)
	}
}
