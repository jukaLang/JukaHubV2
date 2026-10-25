package main

// netplay_gate_test.go — the LAN Play switch and the Quick Settings rows that
// expose it and the LED policy. The switch used to be a dead config field with
// no writer; these tests pin that the row writes it, the gate honours it, and a
// missing value (an upgrade) keeps the feature on.

import (
	"testing"
)

func TestNetplayEnabledDefaultsAndOverrides(t *testing.T) {
	if !netplayEnabled(nil) {
		t.Fatal("a nil config (startup) must offer LAN play")
	}
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	cfg.Variables.NetPlayEnabled = true
	if !netplayEnabled(cfg) {
		t.Fatal("the shipped true default was ignored")
	}
	// The custom copy is what persists in jukauser.json and outranks the typed
	// field, so a user's Off survives a config reload.
	cfg.Variables.Custom[netplayEnabledKey] = false
	if netplayEnabled(cfg) {
		t.Fatal("a stored Off was ignored")
	}
	cfg.Variables.Custom[netplayEnabledKey] = "true"
	if !netplayEnabled(cfg) {
		t.Fatal("a hand-edited \"true\" was not honoured")
	}
}

func TestSetNetplayEnabledWritesBothCopies(t *testing.T) {
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	setNetplayEnabled(cfg, false)
	if cfg.Variables.NetPlayEnabled {
		t.Fatal("typed field was not written")
	}
	if v, ok := cfg.Variables.Custom[netplayEnabledKey]; !ok || v != false {
		t.Fatalf("custom copy = %v (present %v), want false", v, ok)
	}
	setNetplayEnabled(cfg, true)
	if !netplayEnabled(cfg) {
		t.Fatal("turning it back on did not take")
	}
}

func TestQuickSettingsNetplayToggleAndLEDCycle(t *testing.T) {
	prevQS := qs
	prevCFG := appConfig
	prevWrites := userConfigWritesEnabled
	userConfigWritesEnabled = false
	t.Cleanup(func() {
		qs = prevQS
		appConfig = prevCFG
		userConfigWritesEnabled = prevWrites
	})

	qsInit()
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	cfg.Variables.NetPlayEnabled = true
	appConfig = cfg
	qsSyncFromConfig(cfg)

	idx := -1
	for i := range qs.toggles {
		if qs.toggles[i].Action == qsActionNetplay {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("Quick Settings has no LAN Play row")
	}
	if !qs.toggles[idx].Value {
		t.Fatal("LAN Play row does not reflect the enabled config")
	}
	qsToggleRow(idx)
	if netplayEnabled(cfg) {
		t.Fatal("the row did not switch LAN play off")
	}
	if qs.toggles[idx].Value {
		t.Fatal("the row still shows On after switching off")
	}
	qsToggleRow(idx)
	if !netplayEnabled(cfg) {
		t.Fatal("the row did not switch LAN play back on")
	}

	// The LED cycle writes the policy into the custom map the LED loop reads.
	if len(qs.cycles) <= qsCycleRowLED {
		t.Fatalf("Quick Settings has %d cycle rows, want the LED row", len(qs.cycles))
	}
	qs.cycles[qsCycleRowLED].Value = ledPolicyIndex(ledPolicyFromConfig(cfg))
	qsStepCycle(qsCycleRowLED, 1)
	if got := ledPolicyFromConfig(cfg); got != ledPolicyBattery {
		t.Fatalf("LED cycle wrote %q, want battery (audio is the default)", got)
	}
	// And it comes back around.
	for i := 0; i < len(ledPolicyOrder); i++ {
		qsStepCycle(qsCycleRowLED, 1)
	}
	if got := ledPolicyFromConfig(cfg); got != ledPolicyBattery {
		t.Fatalf("LED cycle did not return to battery: %q", got)
	}
}
