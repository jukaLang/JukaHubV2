package main

// cfgdoc_test.go — config as code: secrets never reach the exported file,
// flattening keeps settings and leaves runtime state out, an import is
// previewed item by item and merged through the normal setting path, and
// scenes merge into the design file without losing anything else in it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cfgdocTestIsolate puts back the global state a merge touches: the palette,
// the interface sounds and the accessibility settings.
func cfgdocTestIsolate(t *testing.T) {
	t.Helper()
	palette := capturePalette()
	muted, level, levelSet := bootSfxMuted.Load(), bootSfxLevel.Load(), bootSfxLevelSet.Load()
	prevA11y := a11y
	t.Cleanup(func() {
		a11y = prevA11y
		a11yRefreshFonts()
		bootSfxMuted.Store(muted)
		bootSfxLevel.Store(level)
		bootSfxLevelSet.Store(levelSet)
		restorePalette(palette)
	})
}

func TestCfgdocExportNeverCarriesSecrets(t *testing.T) {
	shipped := cfgdocShipped{Source: "test.json", Version: "1.0", Stamp: "abc",
		Values: map[string]interface{}{"chat_password": "default-pass"}}
	current := map[string]interface{}{
		"chat_password": "hunter2", "groq_api_key": "sk-123", "github_token": "ghp_x",
		"mailbox": "ENC:abcd", "weatherUnit": "F",
	}
	doc := cfgdocBuild("v9", shipped, cfgdocDiff(current, shipped.Values), cfgdocSceneDelta{})
	data, err := cfgdocMarshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"hunter2", "sk-123", "ghp_x", "ENC:abcd", "default-pass"} {
		if strings.Contains(text, secret) {
			t.Errorf("the document carries %q:\n%s", secret, text)
		}
	}
	if n := strings.Count(text, cfgdocRedacted); n != 5 {
		t.Errorf("%d values redacted, want 5 (four secrets and one secret default):\n%s", n, text)
	}
	if doc.Sections.BehaviorOverrides["weatherUnit"] != "F" || doc.SchemaVersion != cfgdocSchemaVersion ||
		doc.GeneratedBy != "JukaHub v9" || doc.Defaults.Stamp != "abc" {
		t.Errorf("document = %+v", doc)
	}
	// The secret names stay listed, so you can see you set one.
	if _, ok := doc.Sections.BehaviorOverrides["groq_api_key"]; !ok {
		t.Error("a secret's name was dropped from the document")
	}

	parsed, err := cfgdocParse(data)
	if err != nil {
		t.Fatalf("the exported document does not parse back: %v", err)
	}
	if len(cfgdocDocValues(parsed)) != len(current) {
		t.Errorf("parsed back %v", cfgdocDocValues(parsed))
	}
	// A redacted value is never written over the real secret.
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"groq_api_key": "real"}
	if err := cfgdocSetValue(config, "groq_api_key", cfgdocRedacted); err == nil || config.Variables.Custom["groq_api_key"] != "real" {
		t.Error("a redacted value was written over the real secret")
	}
	for _, bad := range []string{`{"schemaVersion": 0}`, `{"schemaVersion": 99}`, `not json`,
		`{"schemaVersion": 1, "sections": {"theme": {"": 1}}}`} {
		if _, err := cfgdocParse([]byte(bad)); err == nil {
			t.Errorf("cfgdocParse accepted %s", bad)
		}
	}
}

func TestCfgdocFlattenKeepsSettingsOnly(t *testing.T) {
	var v Variables
	v.Fullscreen = true
	v.ScreenWidth = 960
	v.Custom = map[string]interface{}{
		"Fullscreen":   false, // a capitalised mirror of the typed setting
		"playtime_v1":  map[string]interface{}{"total": 1.0},
		"search_query": "cats", // what a text field holds
		"wifi_status":  "Connected",
		"_internal":    1.0,
		"myOwnSetting": "on",
	}
	flat := cfgdocFlatten(v, map[string]bool{"search_query": true})
	if flat["myOwnSetting"] != "on" {
		t.Errorf("a setting of the user's own is missing: %v", flat["myOwnSetting"])
	}
	if flat["fullscreen"] != true {
		t.Errorf("fullscreen = %v, want the typed value in force", flat["fullscreen"])
	}
	for _, gone := range []string{"Fullscreen", "playtime_v1", "search_query", "wifi_status", "_internal", "screenWidth"} {
		if _, ok := flat[gone]; ok {
			t.Errorf("%s is runtime state or a device fact, not a setting", gone)
		}
	}
	if s, _ := flat["buttonColor"].(string); !strings.HasPrefix(s, "#") {
		t.Errorf("colours are exported as #rrggbb, got %v", flat["buttonColor"])
	}
}

func TestCfgdocImportIsPreviewedThenMerged(t *testing.T) {
	cfgdocTestIsolate(t)
	shipped := cfgdocShipped{Source: "test.json", Stamp: "now", Values: map[string]interface{}{
		"weatherUnit": "C", "fullscreen": false, "gridColumns": float64(4),
	}}
	current := map[string]interface{}{"weatherUnit": "C", "fullscreen": false, "gridColumns": float64(5), "myNote": "a"}
	foreign := `D:\Games`
	if IsWindows() {
		foreign = "/mnt/SDCARD/Roms"
	}
	doc := cfgdocDocument{
		SchemaVersion: cfgdocSchemaVersion,
		Device:        cfgdocDevice{Platform: "another-platform", Name: "Brick"},
		Defaults:      cfgdocDefaultsStamp{Stamp: "then", Values: map[string]interface{}{"weatherUnit": "K"}},
		Sections: cfgdocSections{
			InputProfiles: map[string]interface{}{},
			UILayouts:     cfgdocLayouts{Home: map[string]interface{}{"homeArrangement": "grid"}},
			Theme:         map[string]interface{}{"theme_preset": "nord"},
			BehaviorOverrides: map[string]interface{}{
				"weatherUnit": "F", "fullscreen": true, "groq_api_key": cfgdocRedacted,
				"fileExplorerRoot": foreign, "myNote": "a", "screenWidth": float64(640),
			},
		},
		Removed: []string{"gridColumns"},
	}
	plan := cfgdocPlanImport("x.json", doc, current, shipped)
	items := map[string]cfgdocImportItem{}
	for _, it := range plan.Items {
		items[it.Key] = it
	}
	if _, ok := items["myNote"]; ok {
		t.Error("a setting that already matches is offered")
	}
	checks := []struct {
		key             string
		accept, blocked bool
	}{
		{"weatherUnit", true, false},
		{"homeArrangement", true, false},
		{"theme_preset", true, false},
		{"fullscreen", false, false}, // a device setting from another device: off, but allowed
		{"groq_api_key", false, true},
		{"fileExplorerRoot", false, true},
		{"screenWidth", false, true},
		{"gridColumns", true, false},
	}
	for _, c := range checks {
		it, ok := items[c.key]
		if !ok {
			t.Errorf("%s is not in the preview", c.key)
			continue
		}
		if it.Accept != c.accept || it.Blocked != c.blocked {
			t.Errorf("%s: accept %v blocked %v (%s), want %v %v", c.key, it.Accept, it.Blocked, it.Note, c.accept, c.blocked)
		}
		if (it.Blocked || !it.Accept) && it.Note == "" {
			t.Errorf("%s is held back without a reason", c.key)
		}
	}
	if it := items["gridColumns"]; !it.Remove || it.Value != float64(4) {
		t.Errorf("gridColumns = %+v, want a reset to the shipped 4", it)
	}
	if len(plan.Upstream) != 1 || plan.Upstream[0].Key != "weatherUnit" {
		t.Errorf("upstream = %+v, want the default that changed since the file was made", plan.Upstream)
	}

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"GridColumns": 5.0}
	config.Variables.GridColumns = 5
	config.Variables.WeatherUnit = "C"
	applied, failed := cfgdocApplyImport(config, plan, shipped)
	if applied != 4 || len(failed) != 0 {
		t.Fatalf("applied %d, failed %v; want the four accepted items", applied, failed)
	}
	if config.Variables.WeatherUnit != "F" || config.Variables.Custom["weatherUnit"] != "F" {
		t.Errorf("weatherUnit = %q / %v", config.Variables.WeatherUnit, config.Variables.Custom["weatherUnit"])
	}
	if config.Variables.GridColumns != 4 {
		t.Errorf("gridColumns = %d, want the shipped 4", config.Variables.GridColumns)
	}
	if _, stale := config.Variables.Custom["GridColumns"]; stale {
		t.Error("the capitalised mirror survived and would be read back over the import")
	}
	if config.Variables.Fullscreen {
		t.Error("an item that was off was applied")
	}
	if themePresetName(config) != "nord" || homeArrangement(config) != homeArrangementGrid {
		t.Errorf("theme %q, arrangement %q", themePresetName(config), homeArrangement(config))
	}
}

func TestCfgdocWriteScenesReplacesAndAdds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jukaconfig.json")
	original := `{"title": "kept", "scenes": [{"name": "Main", "elements": []}, {"name": "Tools", "elements": []}]}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	scenes := []cfgdocImportScene{
		{Name: "Tools", Raw: json.RawMessage(`{"name": "Tools", "elements": [{"type": "label", "text": "new"}]}`), Accept: true},
		{Name: "Mine", Raw: json.RawMessage(`{"name": "Mine", "elements": []}`), Accept: true},
		{Name: "Skipped", Raw: json.RawMessage(`{"name": "Skipped", "elements": []}`), Accept: false},
		{Name: "Liar", Raw: json.RawMessage(`{"name": "Other", "elements": []}`), Accept: true},
	}
	n, err := cfgdocWriteScenes(path, scenes)
	if err != nil || n != 2 {
		t.Fatalf("wrote %d scenes (%v), want Tools replaced and Mine added", n, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	names, byName, err := cfgdocSceneMap(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "Main,Tools,Mine" || !strings.Contains(string(byName["Tools"]), "new") {
		t.Errorf("scenes now %v: %s", names, data)
	}
	var file map[string]interface{}
	if err := json.Unmarshal(data, &file); err != nil || file["title"] != "kept" {
		t.Errorf("the rest of the design file was lost: %s", data)
	}
}

func TestCfgdocSecretKeyNames(t *testing.T) {
	for key, secret := range map[string]bool{
		"groq_api_key": true, "openaiApiKey": true, "wifiPassword": true, "github_token": true,
		"clientSecret": true, "inputProfileHotkey": false, "weatherUnit": false, "keyboardLayout": false,
	} {
		if got := cfgdocIsSecretKey(key); got != secret {
			t.Errorf("cfgdocIsSecretKey(%q) = %v, want %v", key, got, secret)
		}
	}
}
