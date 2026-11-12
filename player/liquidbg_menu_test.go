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
	if row.badge != "On" || row.action == nil || liquidLevelOf(config) != liquidLevelGentle {
		t.Fatalf("new setup should show liquid on at Gentle: row=%+v level=%v", row, liquidLevelOf(config))
	}

	row.action(config)
	if got := liquidMenuRow(t, config).badge; got != "Off" {
		t.Fatalf("menu after disabling liquid shows %q, want Off", got)
	}
	if liquidLevelOf(config) != liquidLevelOff || config.Variables.Custom[liquidResumeKey] != "gentle" {
		t.Fatalf("disabling did not save Off and Gentle: %+v", config.Variables.Custom)
	}

	row.action(config)
	if liquidLevelOf(config) != liquidLevelGentle || liquidMenuRow(t, config).badge != "On" {
		t.Fatalf("re-enabling should restore Gentle: %+v", config.Variables.Custom)
	}

	liquidLevelRow().Choose(config, "lively")
	row.action(config)
	row.action(config)
	if liquidLevelOf(config) != liquidLevelLively || config.Variables.Custom[liquidResumeKey] != "lively" {
		t.Fatalf("re-enabling should restore Lively: %+v", config.Variables.Custom)
	}
}
