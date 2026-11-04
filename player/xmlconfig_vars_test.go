package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Digit-only text in a typed string setting stays text. Coerced to a number
// it failed the typed decode of <variables>, and every custom variable was
// dropped along with it.
func TestXMLStringSettingsKeepTheirTextAndCustomVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jukaconfig.xml")
	doc := `<jukaconfig><title>T</title><variables>
<playbackResolution>720</playbackResolution>
<accentColor>000000</accentColor>
<swapABButtons>true</swapABButtons>
</variables><scenes><scene name="Main"></scene></scenes></jukaconfig>`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadXMLConfig(path)
	if err != nil {
		t.Fatalf("LoadXMLConfig: %v", err)
	}
	if got := cfg.Variables.PlaybackResolution; got != "720" {
		t.Errorf("playbackResolution = %q, want 720", got)
	}
	if got := cfg.Variables.AccentColor; got != "000000" {
		t.Errorf("accentColor = %q, want the text as written", got)
	}
	if v, ok := cfg.Variables.Custom["swapABButtons"]; !ok || v != true {
		t.Errorf("custom variable dropped: swapABButtons = %v (present %v)", v, ok)
	}
}
