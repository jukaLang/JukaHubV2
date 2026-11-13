package main

import "testing"

func liquidMenuRow(t *testing.T, config *Config) startMenuEntry {
	t.Helper()
	for _, group := range startMenuGroups(config) {
		if group.name != "Device" {
			continue
		}
		for _, entry := range group.items {
			if entry.name == "Liquid background" {
				return entry
			}
		}
	}
	t.Fatal("Device menu has no Liquid background switch")
	return startMenuEntry{}
}

func TestLiquidMenuToggleRestoresLastActiveLevel(t *testing.T) {
	notifTestKeepState(t)
	clearToastAfterTest(t)
	config := &Config{}
	row := liquidMenuRow(t, config)
	if row.badge != "Off" || row.action == nil || liquidLevelOf(config) != liquidLevelOff {
		t.Fatalf("new setup should show liquid off: row=%+v level=%v", row, liquidLevelOf(config))
	}

	row.action(config)
	if liquidLevelOf(config) != liquidLevelMedium || liquidMenuRow(t, config).badge != "On" {
		t.Fatalf("first switch-on should pick Medium: %+v", config.Variables.Custom)
	}

	row.action(config)
	if liquidLevelOf(config) != liquidLevelOff || config.Variables.Custom[liquidResumeKey] != "medium" {
		t.Fatalf("disabling did not save Off and Medium: %+v", config.Variables.Custom)
	}

	liquidLevelRow().Choose(config, "lively")
	row.action(config)
	row.action(config)
	if liquidLevelOf(config) != liquidLevelLively || config.Variables.Custom[liquidResumeKey] != "lively" {
		t.Fatalf("re-enabling should restore Lively: %+v", config.Variables.Custom)
	}

	// R2 walks Off → Slight → Medium → Lively → Off.
	for _, want := range []liquidLevel{liquidLevelOff, liquidLevelSlight, liquidLevelMedium, liquidLevelLively} {
		liquidCycle(config)
		if got := liquidLevelOf(config); got != want {
			t.Fatalf("R2 cycle: got %v, want %v", got, want)
		}
	}
	// A config saved before the rename keeps its water.
	if parseLiquidLevel("gentle") != liquidLevelMedium {
		t.Fatal(`"gentle" should read as Medium`)
	}
	// R2 is the water's only where the screen's footer gives it no job.
	if liquidR2Taken([]FooterHint{{Button: "L1/R1", Label: "Tabs"}}) ||
		!liquidR2Taken([]FooterHint{{Button: "L2/R2 / - / +", Label: "Zoom"}}) {
		t.Fatal("liquidR2Taken misread a footer row")
	}
}
