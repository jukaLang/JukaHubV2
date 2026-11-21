package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func a11ySettingsSectionRows(t *testing.T, c *Config) []setRow {
	t.Helper()
	idx := setSectionIndex(a11ySettingsSection)
	if idx < 0 {
		t.Fatalf("Settings has no %q section", a11ySettingsSection)
	}
	return setSectionRows(c, setSections()[idx])
}

// TestA11YAccessibilitySectionCoversEveryPanelRow is A11Y-6's contract: the
// section exists in the rail and carries every row the modal has, so no
// accessibility setting is reachable only from the panel.
func TestA11YAccessibilitySectionCoversEveryPanelRow(t *testing.T) {
	resetA11yForTest()
	defer resetA11yForTest()

	idx := setSectionIndex(a11ySettingsSection)
	if idx < 0 {
		t.Fatalf("Settings has no %q section", a11ySettingsSection)
	}
	section := setSections()[idx]
	if section.Title != a11yPanelTitle {
		t.Fatalf("section title = %q, want %q", section.Title, a11yPanelTitle)
	}
	rows := setSectionRows(&Config{}, section)
	seen := map[string]bool{}
	for _, row := range rows {
		if row.ID == "" {
			t.Fatalf("%q has a row with no id (search and deep links need one)", section.Title)
		}
		if seen[row.ID] {
			t.Fatalf("%q has two rows with id %q", section.Title, row.ID)
		}
		seen[row.ID] = true
	}
	for _, panel := range a11yRows() {
		found := false
		for _, row := range rows {
			if row.Label == panel.label {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("panel row %q is not in Settings → Accessibility", panel.label)
		}
	}
}

// TestA11YAccessibilityRowsChangeLiveState drives the real rows: a change must
// reach the state the handlers read and be stored in the config.
func TestA11YAccessibilityRowsChangeLiveState(t *testing.T) {
	old := a11y
	defer func() { a11y = old; a11yChordReset() }()
	resetA11yForTest()

	cfg := &Config{}
	rows := a11ySettingsSectionRows(t, cfg)
	find := func(id string) setRow {
		for _, row := range rows {
			if row.ID == id {
				return row
			}
		}
		t.Fatalf("no %q row in Settings → Accessibility", id)
		return setRow{}
	}

	// Audio channels kept the Mono audio switch's ID.
	channels := find("monoAudio")
	if channels.Current(cfg) != audioChStereo {
		t.Fatal("audio channels start on something other than Stereo")
	}
	channels.Choose(cfg, "mono-left")
	if a11y.audioChannels != audioChMonoLeft {
		t.Fatal("audio channels row did not reach the live state")
	}
	if cfg.Variables.Custom[a11yKeyAudioChannels] != "mono-left" || cfg.Variables.Custom[a11yKeyMonoAudio] != true {
		t.Fatalf("audio channels not stored: %v / %v", cfg.Variables.Custom[a11yKeyAudioChannels], cfg.Variables.Custom[a11yKeyMonoAudio])
	}

	find("audioBalance").Choose(cfg, "-40")
	if a11y.audioBalance != -40 {
		t.Fatalf("audio balance = %d, want -40", a11y.audioBalance)
	}

	find("captionSize").Choose(cfg, "2")
	if a11y.captionSize != 2 || a11yCaptionSizeLabel() != "Large" {
		t.Fatalf("caption size = %d (%q)", a11y.captionSize, a11yCaptionSizeLabel())
	}

	find("longPress").Choose(cfg, "600")
	if a11y.longPressMs != 600 || a11yLongPressMs() != 600 {
		t.Fatalf("long press = %d, handler reads %d", a11y.longPressMs, a11yLongPressMs())
	}

	find("oneHanded").Set(cfg, true)
	if !a11y.oneHanded {
		t.Fatal("one-handed row did not reach the input layer")
	}

	find("button"+a11yButtonKey(sdl.CONTROLLER_BUTTON_A)).Choose(cfg, "y")
	if got := a11y.keys[sdl.CONTROLLER_BUTTON_A]; got != sdl.CONTROLLER_BUTTON_Y {
		t.Fatalf("A sends %d, want Y", got)
	}

	find("selectKey").Choose(cfg, a11ySelectSwapped)
	if a11ySelectKeyMode() != a11ySelectSwapped {
		t.Fatalf("Select mode = %q", a11ySelectKeyMode())
	}

	find("stickyChords").Set(cfg, true)
	if !a11y.stickyChords {
		t.Fatal("sticky chords row did not reach the chord layer")
	}

	// Reset asks first: Keep changes nothing, Reset resets.
	previousSheet := tbxSheet
	defer func() { tbxSheet = previousSheet }()
	find("a11yReset").Open(cfg)
	if !tbxSheet.open || len(tbxSheet.spec.buttons) != 2 || tbxSheet.spec.buttons[0].label != "Keep" {
		t.Fatalf("reset did not ask first: %+v", tbxSheet.spec.buttons)
	}
	tbxSheetActivate(0)
	if a11y.audioChannels != audioChMonoLeft || !a11y.stickyChords {
		t.Fatal("Keep reset the settings anyway")
	}
	find("a11yReset").Open(cfg)
	tbxSheetActivate(1)
	if a11y.audioChannels != audioChStereo || a11y.oneHanded || a11y.stickyChords || a11y.longPressMs != 450 {
		t.Fatalf("reset left state behind: %+v", a11y)
	}
	if len(a11y.keys) != 0 || a11ySelectKeyMode() != a11ySelectDefault {
		t.Fatalf("reset left the keymap behind: %v", a11y.keys)
	}
	if cfg.Variables.Custom[a11yKeyMonoAudio] != false || cfg.Variables.Custom[a11yKeyLongPress] != 450 {
		t.Fatalf("reset not stored: %v", cfg.Variables.Custom)
	}
}
