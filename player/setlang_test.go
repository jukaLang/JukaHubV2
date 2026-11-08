package main

// setlang_test.go — the subtitle languages Settings offers: every name drawable
// in the bundled Inter (a name in a script Inter lacks would draw as a row of
// boxes), every code stored once, and the carousel and the full list agreeing.

import (
	"strings"
	"testing"
)

func TestSubtitleLanguageNamesDrawInInter(t *testing.T) {
	for _, o := range setSubtitleAllOptions() {
		for _, text := range []string{o.Label, o.Note} {
			for _, r := range text {
				if !fontCoversRune("Inter-Regular.ttf", r) {
					t.Errorf("%q (%s) has %q (U+%04X), which Inter cannot draw", text, o.Value, r, r)
				}
			}
		}
	}
}

func TestSubtitleLanguageCodesAreUniqueAndQuickStopsComeFirst(t *testing.T) {
	seen := map[string]bool{}
	for _, l := range setSubtitleLanguages {
		code := strings.ToLower(l.Code)
		if code == "" || seen[code] {
			t.Errorf("code %q is empty or listed twice", l.Code)
		}
		seen[code] = true
		if strings.TrimSpace(l.Native) == "" || strings.TrimSpace(l.English) == "" {
			t.Errorf("%s: needs a native and an English name", l.Code)
		}
	}

	quick := setSubtitleQuickOptions()
	if len(quick) != setSubtitleQuickCount+1 {
		t.Fatalf("the carousel has %d stops, want Default and %d languages", len(quick), setSubtitleQuickCount)
	}
	if quick[0].Value != "" || quick[0].Label != setSubtitleDefaultLabel {
		t.Fatalf("the first stop is %+v, want the default", quick[0])
	}
	all := setSubtitleAllOptions()
	if len(all) != len(setSubtitleLanguages)+1 {
		t.Fatalf("the list has %d entries, want Default and every language", len(all))
	}
	for i, o := range quick {
		if all[i].Value != o.Value || all[i].Label != o.Label {
			t.Errorf("stop %d is %+v on the carousel but %+v in the list", i, o, all[i])
		}
	}
	for _, o := range all {
		if strings.TrimSpace(o.Note) == "" {
			t.Errorf("%q has no second line in the list", o.Label)
		}
	}
}

func TestSubtitleLabelNamesWhatIsStored(t *testing.T) {
	for code, want := range map[string]string{
		"":      setSubtitleDefaultLabel,
		"  ":    setSubtitleDefaultLabel,
		"es":    "Español",
		"ES":    "Español",
		"ja":    "Japanese (ja)",
		"pt-BR": "Português (Brasil)",
		"xx-YY": "xx-YY",
	} {
		if got := setSubtitleLabel(code); got != want {
			t.Errorf("setSubtitleLabel(%q) = %q, want %q", code, got, want)
		}
	}
}
