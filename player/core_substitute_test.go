package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// SEC-20: a label can be captured in a screenshot or shared by the companion,
// so substitution must leave a protected name or an encrypted value as its
// placeholder instead of printing the stored value.
func TestSubstituteVarsProtectsSecrets(t *testing.T) {
	vars := map[string]interface{}{
		"player_name":      "Alex",
		"discord_token":    "hunter2",           // protected word
		"wifi_password":    "pa55word",          // protected word
		"apiKey":           "sk-live-123",       // protected phrase
		"ha_url":           "http://home.local", // protected name
		"toolſ_path":       "/mnt/private",      // non-ASCII folds onto tools_path
		"livebuilder_code": "246810",            // protected phrase
		"encrypted":        "ENC:3f9a",          // an encrypted value
		"battery":          87,
	}
	cases := []struct {
		text string
		want string
	}{
		{"Hello $player_name", "Hello Alex"},
		{"$discord_token", "$discord_token"},
		{"$wifi_password", "$wifi_password"},
		{"$apiKey", "$apiKey"},
		{"$ha_url", "$ha_url"},
		{"$toolſ_path", "$toolſ_path"},
		{"$livebuilder_code", "$livebuilder_code"},
		{"$encrypted", "$encrypted"},
		{"$missing", "$missing"},
		{"no variables", "no variables"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := substituteVars(tc.text, vars); got != tc.want {
			t.Errorf("substituteVars(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// An ordinary label is still rendered: the guard must not turn every variable
// into its placeholder.
func TestSubstituteVarsAllowsPlainLabels(t *testing.T) {
	vars := map[string]interface{}{"wifi_status": "Connected", "battery": 87}
	if got, want := substituteVars("$wifi_status ($battery%)", vars), "Connected (87%)"; got != want {
		t.Fatalf("substituteVars = %q, want %q", got, want)
	}
}

// TestUICataloguesParse checks every interface-language catalogue (i18n.go):
// it decodes, names its own language, has no empty value, keeps each key's
// placeholders, draws only characters the bundled Inter has, and never maps a
// value that is itself a key onto something else (renderText may translate a
// string fitTextCached already translated).
func TestUICataloguesParse(t *testing.T) {
	for _, l := range uiLanguages {
		if l.Code == uiLanguageDefault {
			continue
		}
		data, err := uiCatalogFiles.ReadFile("i18n/" + l.Code + ".json")
		if err != nil {
			t.Fatalf("%s: %v", l.Code, err)
		}
		var f uiCatalogFile
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatalf("%s: %v", l.Code, err)
		}
		if f.Language != l.Code || len(f.Strings) < 300 {
			t.Fatalf("%s: language %q with %d strings", l.Code, f.Language, len(f.Strings))
		}
		for k, v := range f.Strings {
			if v == "" || k == "" {
				t.Errorf("%s: empty entry %q -> %q", l.Code, k, v)
			}
			if string(uiSortedBytes(uiPlaceholders(k))) != string(uiSortedBytes(uiPlaceholders(v))) {
				t.Errorf("%s: placeholders differ: %q -> %q", l.Code, k, v)
			}
			if again, ok := f.Strings[v]; ok && again != v {
				t.Errorf("%s: %q -> %q, which translates again to %q", l.Code, k, v, again)
			}
			for _, r := range v {
				if r > 0x7f && !fontCoversRune(homeUIFontFile, r) {
					t.Errorf("%s: %q uses %q, which Inter cannot draw", l.Code, k, r)
				}
			}
		}
	}

	t.Cleanup(func() { uiSetLanguage(uiLanguageDefault) })
	uiSetLanguage("es")
	if uiLanguageCode() != "es" || uiT("Settings") == "Settings" {
		t.Fatalf("Spanish not in force: %q", uiT("Settings"))
	}
	if got := uiT("Weather: On"); got == "Weather: On" {
		t.Errorf("label: value join not translated: %q", got)
	}
	if got := uiT("Score: 12"); got == "Score: 12" || !strings.Contains(got, "12") {
		t.Errorf("pattern not applied: %q", got)
	}
	if got := uiT("some game title"); got != "some game title" {
		t.Errorf("content changed: %q", got)
	}
	uiSetLanguage(uiLanguageDefault)
	if got := uiT("Settings"); got != "Settings" {
		t.Errorf("English is not the identity: %q", got)
	}
}
