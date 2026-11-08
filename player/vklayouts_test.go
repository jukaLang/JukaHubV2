package main

// Tests for the keyboard's languages (vklayouts.go): every layout is a usable
// grid, the casing rules hold (Turkish dotted I, Greek final sigma), the
// enabled list round-trips through the config, and the globe walks it.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// withKeyboardConfig points appConfig at a fresh config for the layout
// settings and restores the old one afterwards.
func withKeyboardConfig(t *testing.T) *Config {
	t.Helper()
	prev := appConfig
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	appConfig = cfg
	t.Cleanup(func() {
		appConfig = prev
		vkLayoutChanged()
	})
	return cfg
}

func TestVKLayoutsAreUsableGrids(t *testing.T) {
	seen := map[string]bool{}
	for i := range vkLayouts {
		l := &vkLayouts[i]
		if l.ID == "" || l.Name == "" || l.Native == "" || l.Short == "" || len(l.Lang) != 2 {
			t.Errorf("layout %d is missing a name or code: %+v", i, l)
		}
		if seen[strings.ToLower(l.ID)] {
			t.Errorf("layout id %q is used twice", l.ID)
		}
		seen[strings.ToLower(l.ID)] = true
		rows := vkLayoutRows(l)
		keys := map[string]bool{}
		for r, row := range rows {
			if len(row) < 7 || len(row) > 12 {
				t.Errorf("%s row %d has %d keys", l.ID, r, len(row))
			}
			for _, k := range row {
				if utf8.RuneCountInString(k) != 1 {
					t.Errorf("%s key %q is not one character", l.ID, k)
				}
				if keys[k] {
					t.Errorf("%s has %q twice", l.ID, k)
				}
				keys[k] = true
			}
		}
		if vkLayoutByID(strings.ToUpper(l.ID)) != l {
			t.Errorf("lookup by %q is not case-insensitive", l.ID)
		}
	}
	if vkLayoutByID(vkDefaultLayoutID) == nil {
		t.Fatal("the default layout is not in the list")
	}
}

func TestVKUpperKeyFollowsTheLanguage(t *testing.T) {
	tr := vkLayoutByID("tr")
	ru := vkLayoutByID("ru")
	el := vkLayoutByID("el")
	cases := []struct {
		layout     *vkLayout
		key, upper string
	}{
		{tr, "i", "İ"},
		{tr, "ı", "I"},
		{vkLayoutByID("en-US"), "i", "I"},
		{ru, "ж", "Ж"},
		{el, "ς", "Σ"},
		{el, ";", ";"},
		{vkLayoutByID("de"), "ß", "ß"},
	}
	for _, tc := range cases {
		if got := vkUpperKey(tc.layout, tc.key); got != tc.upper {
			t.Errorf("%s: upper(%q) = %q, want %q", tc.layout.ID, tc.key, got, tc.upper)
		}
	}
}

func TestVKAccentsPutTheLanguageFirst(t *testing.T) {
	fr := vkLayoutByID("fr")
	got := vkAccentsFor(fr, "e")
	if len(got) == 0 || got[0] != "é" {
		t.Fatalf("French e accents = %v, want é first", got)
	}
	if up := vkAccentsFor(fr, "E"); len(up) == 0 || up[0] != "É" {
		t.Fatalf("shifted E accents = %v, want É first", up)
	}
	if len(got) > vkMaxAccents {
		t.Fatalf("%d accents, cap is %d", len(got), vkMaxAccents)
	}
	if ru := vkAccentsFor(vkLayoutByID("ru"), "е"); len(ru) != 1 || ru[0] != "ё" {
		t.Fatalf("Russian е accents = %v, want only ё", ru)
	}
	if none := vkAccentsFor(vkLayoutByID("en-US"), "q"); len(none) != 0 {
		t.Fatalf("q has no accents, got %v", none)
	}
	if money := vkAccentsFor(vkLayoutByID("en-US"), "$"); len(money) == 0 || money[0] != "€" {
		t.Fatalf("$ variants = %v, want € first", money)
	}
}

func TestVKEnabledLayoutsRoundTripAndCycle(t *testing.T) {
	cfg := withKeyboardConfig(t)
	vkLayoutChanged()
	if got := vkEnabledLayouts(); len(got) != 1 || got[0].ID != vkDefaultLayoutID {
		t.Fatalf("fresh config enabled %v, want just %s", vkFormatLayoutIDs(got), vkDefaultLayoutID)
	}

	vkSetEnabledLayouts(cfg, []*vkLayout{vkLayoutByID("de"), vkLayoutByID("ru"), vkLayoutByID("en-US")})
	if raw, _ := cfg.Variables.Custom[vkKeyLayouts].(string); raw != "de,ru,en-US" {
		t.Fatalf("stored layouts %q", raw)
	}
	// The current layout was not in the new list, so the first one took over.
	if cur := vkCurrentLayout(); cur.ID != "de" {
		t.Fatalf("current layout %s, want de", cur.ID)
	}
	if next := vkCycleLayout(cfg); next.ID != "ru" {
		t.Fatalf("globe went to %s, want ru", next.ID)
	}
	vkCycleLayout(cfg)
	if next := vkCycleLayout(cfg); next.ID != "de" {
		t.Fatalf("globe did not wrap around: %s", next.ID)
	}

	// Junk, duplicates and unknown ids are dropped; nothing usable falls back.
	if got := vkFormatLayoutIDs(vkParseLayoutIDs(" de, xx ,DE,ru")); got != "de,ru" {
		t.Fatalf("parse kept %q", got)
	}
	if got := vkFormatLayoutIDs(vkParseLayoutIDs("nope")); got != vkDefaultLayoutID {
		t.Fatalf("unusable list parsed to %q", got)
	}
}

func TestVKRowsFollowTheLayout(t *testing.T) {
	resetKeyboardState(t)
	withScreen(t, 1280, 720)
	cfg := withKeyboardConfig(t)
	vkTarget = vkTargetScene

	vkSetEnabledLayouts(cfg, []*vkLayout{vkLayoutByID("de"), vkLayoutByID("en-US")})
	rows := vkRows()
	if r, _ := rowIndex(rows, "ü"); r < 0 {
		t.Fatal("German layout has no ü key")
	}
	if r, c := rowIndex(rows, "z"); r != 1 || c != 5 {
		t.Fatalf("German z at (%d,%d), want QWERTZ (1,5)", r, c)
	}
	if r, _ := rowIndex(rows, "GLOBE"); r != len(rows)-1 {
		t.Fatal("two languages should put the globe in the bottom row")
	}
	vkLayer = vkLayerSymbols
	if r, _ := rowIndex(vkRows(), "€"); r < 0 {
		t.Fatal("German symbols should offer € in place of $")
	}
	vkLayer = vkLayerLetters

	vkSetEnabledLayouts(cfg, []*vkLayout{vkLayoutByID("en-US")})
	if r, _ := rowIndex(vkRows(), "GLOBE"); r >= 0 {
		t.Fatal("a single language should not show the globe key")
	}
	if got := vkKeyLabel("SPACE"); got != "space" {
		t.Fatalf("space bar label %q with one language", got)
	}
}
