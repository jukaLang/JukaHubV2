package main

import (
	"encoding/json"
	"testing"
)

func TestVariablesHexColorUnmarshal(t *testing.T) {
	data := []byte(`{"buttonColor":"#1c2230","labelColor":"#e8eefc","inputColor":"#141a26","fullscreen":false}`)
	var v Variables
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal hex: %v", err)
	}
	if v.ButtonColor.R != 28 || v.ButtonColor.G != 34 || v.ButtonColor.B != 48 {
		t.Fatalf("buttonColor got %+v, want {28 34 48}", v.ButtonColor)
	}
	if v.LabelColor.R != 232 || v.LabelColor.B != 252 {
		t.Fatalf("labelColor got %+v", v.LabelColor)
	}

	// Object form must still work.
	data2 := []byte(`{"buttonColor":{"r":1,"g":2,"b":3}}`)
	var v2 Variables
	if err := json.Unmarshal(data2, &v2); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	if v2.ButtonColor.R != 1 || v2.ButtonColor.G != 2 || v2.ButtonColor.B != 3 {
		t.Fatalf("buttonColor2 got %+v", v2.ButtonColor)
	}
}

func TestLoadRealConfig(t *testing.T) {
	cfg, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig(jukaconfig.json): %v", err)
	}
	if len(cfg.Scenes) == 0 {
		t.Fatal("expected at least one scene")
	}
	// Theme colors must have been parsed from their hex strings.
	if cfg.Variables.ButtonColor.R == 0 && cfg.Variables.ButtonColor.G == 0 && cfg.Variables.ButtonColor.B == 0 {
		t.Fatalf("buttonColor not parsed from hex: %+v", cfg.Variables.ButtonColor)
	}
}
