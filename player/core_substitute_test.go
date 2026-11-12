package main

import "testing"

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
