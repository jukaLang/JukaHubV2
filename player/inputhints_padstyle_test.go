package main

// inputhints_padstyle_test.go — the pad family names the hints use (INP-3):
// Xbox's A/B/X/Y, PlayStation's ✕/○/□/△ and Nintendo's swapped letters and
// shoulder names, chosen by Settings → Controls → Pad style.

import (
	"testing"
)

func TestHintPadFamilyBadgeRenamesTheFamilyButtons(t *testing.T) {
	cases := []struct {
		name string
		in   string
		fam  hintPadFamily
		want string
	}{
		{"xbox keeps its letters", "A", hintPadFamilyXbox, "A"},
		{"playstation face buttons", "A", hintPadFamilyPlayStation, "✕"},
		{"playstation circle", "B", hintPadFamilyPlayStation, "○"},
		{"playstation square", "X", hintPadFamilyPlayStation, "□"},
		{"playstation triangle", "Y", hintPadFamilyPlayStation, "△"},
		{"playstation shoulders already match", "L1", hintPadFamilyPlayStation, "L1"},
		{"playstation hold", "Hold B", hintPadFamilyPlayStation, "Hold ○"},
		{"playstation chord", "Select + R1", hintPadFamilyPlayStation, "Select + R1"},
		{"playstation alternatives", "A / B", hintPadFamilyPlayStation, "✕ / ○"},
		{"nintendo swaps A and B", "A", hintPadFamilyNintendo, "B"},
		{"nintendo swaps B back", "B", hintPadFamilyNintendo, "A"},
		{"nintendo swaps X and Y", "X", hintPadFamilyNintendo, "Y"},
		{"nintendo swaps Y back", "Y", hintPadFamilyNintendo, "X"},
		{"nintendo shoulders", "L1", hintPadFamilyNintendo, "L"},
		{"nintendo right shoulder", "R1", hintPadFamilyNintendo, "R"},
		{"nintendo triggers", "L2", hintPadFamilyNintendo, "ZL"},
		{"nintendo right trigger", "R2", hintPadFamilyNintendo, "ZR"},
		{"nintendo hold", "Hold A", hintPadFamilyNintendo, "Hold B"},
		{"nintendo chord", "Select + R1", hintPadFamilyNintendo, "Select + R"},
		{"keyboard pair untouched", "A/D", hintPadFamilyNintendo, "A/D"},
		{"empty stays empty", "", hintPadFamilyPlayStation, ""},
	}
	for _, tc := range cases {
		if got := hintPadFamilyBadge(tc.in, tc.fam); got != tc.want {
			t.Errorf("%s: %q in %v = %q, want %q", tc.name, tc.in, tc.fam, got, tc.want)
		}
	}
}

func TestHintPadFamilyForName(t *testing.T) {
	cases := []struct {
		name string
		want hintPadFamily
	}{
		{"Sony DualSense Wireless Controller", hintPadFamilyPlayStation},
		{"DUALSHOCK 4", hintPadFamilyPlayStation},
		{"PlayStation 5 Controller", hintPadFamilyPlayStation},
		{"Nintendo Switch Pro Controller", hintPadFamilyNintendo},
		{"Joy-Con (L) + (R)", hintPadFamilyNintendo},
		{"Switch Pro Controller", hintPadFamilyNintendo},
		{"Pro Controller", hintPadFamilyXbox}, // a bare "Pro Controller" is not Nintendo-specific
		{"Xbox Wireless Controller", hintPadFamilyXbox},
		{"8BitDo Pro 2", hintPadFamilyXbox},
		{"", hintPadFamilyXbox},
	}
	for _, tc := range cases {
		if got := hintPadFamilyForName(tc.name); got != tc.want {
			t.Errorf("%q = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHintPadStyleReading(t *testing.T) {
	c := &Config{}
	if got := hintPadStyleOf(c); got != hintPadStyleAuto {
		t.Errorf("no config = %q, want auto", got)
	}
	c.Variables.Custom = map[string]interface{}{}
	if got := hintPadStyleOf(c); got != hintPadStyleAuto {
		t.Errorf("no setting = %q, want auto", got)
	}
	c.Variables.Custom[hintPadStyleKey] = "PlayStation"
	if got := hintPadStyleOf(c); got != hintPadStylePlayStation {
		t.Errorf("PlayStation = %q", got)
	}
	if got := hintPadFamilyFor(c, "Xbox Wireless Controller"); got != hintPadFamilyPlayStation {
		t.Errorf("the setting must beat the pad's name, got %v", got)
	}
	c.Variables.Custom[hintPadStyleKey] = "nintendo"
	if got := hintPadStyleOf(c); got != hintPadStyleNintendo {
		t.Errorf("nintendo = %q", got)
	}
	c.Variables.Custom[hintPadStyleKey] = "steam-deck"
	if got := hintPadStyleOf(c); got != hintPadStyleAuto {
		t.Errorf("unknown style = %q, want auto", got)
	}
	if got := hintPadFamilyFor(c, "Nintendo Switch Pro Controller"); got != hintPadFamilyNintendo {
		t.Errorf("auto with a Nintendo pad = %v", got)
	}
	if got := hintPadFamilyFor(c, "Xbox Wireless Controller"); got != hintPadFamilyXbox {
		t.Errorf("auto with an Xbox pad = %v", got)
	}

	opts := hintPadStyleOptions(c)
	if len(opts) != 4 {
		t.Fatalf("options = %d, want Auto, Xbox, PlayStation, Nintendo", len(opts))
	}
	want := []string{"Auto", "Xbox", "PlayStation", "Nintendo"}
	for i, label := range want {
		if opts[i].Label != label {
			t.Errorf("option %d = %q, want %q", i, opts[i].Label, label)
		}
	}
}

func TestHintKeyInUsesThePadFamily(t *testing.T) {
	prev := hintPadFamilyNow.Load()
	t.Cleanup(func() { hintPadFamilyNow.Store(prev) })

	hintPadFamilyNow.Store(int32(hintPadFamilyPlayStation))
	if got := hintKeyIn("A", "Confirm", hintVocabPad, nil); got != "✕" {
		t.Errorf("PlayStation A = %q, want ✕", got)
	}
	if got := hintKeyIn("Y", "Delete", hintVocabPad, nil); got != "△" {
		t.Errorf("PlayStation Y = %q, want △", got)
	}

	// The cache holds the neutral label: a style change shows at once.
	hintPadFamilyNow.Store(int32(hintPadFamilyNintendo))
	if got := hintKeyIn("A", "Confirm", hintVocabPad, nil); got != "B" {
		t.Errorf("Nintendo A = %q, want B", got)
	}
	if got := hintKeyIn("R1", "Tab", hintVocabPad, nil); got != "R" {
		t.Errorf("Nintendo R1 = %q, want R", got)
	}

	// The keyboard vocabulary keeps its keys.
	hintPadFamilyNow.Store(int32(hintPadFamilyPlayStation))
	if got := hintKeyIn("A", "Confirm", hintVocabKeyboard, nil); got == "✕" || got == "B" {
		t.Errorf("keyboard A = %q, want the key, not a pad glyph", got)
	}
}
