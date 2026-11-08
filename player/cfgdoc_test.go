package main

// cfgdoc_test.go — config as code: the diff finds added, changed and removed
// settings and files them under the right section, secrets never reach the
// exported file, flattening keeps settings and leaves runtime state out, an
// import is previewed item by item and merged through the normal setting path,
// the defaults a new version changed are reported once, and scenes merge into
// the design file without losing anything else in it.

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

func TestCfgdocDiffFindsAddedChangedAndRemoved(t *testing.T) {
	defaults := map[string]interface{}{
		"weatherUnit": "C", "fullscreen": false, "theme_preset": "hub", "gridColumns": float64(4), "same": "x",
	}
	current := map[string]interface{}{
		"weatherUnit": "F", "fullscreen": "false", "gridColumns": float64(4), "same": "x",
		"groq_api_key": "sk-123", "homeArrangement": "grid",
	}
	changes := cfgdocDiff(current, defaults)
	want := []struct {
		key, kind, section string
		secret             bool
	}{
		{"homeArrangement", cfgdocAdded, cfgdocSectionLayouts, false},
		{"theme_preset", cfgdocRemoved, cfgdocSectionTheme, false},
		{"groq_api_key", cfgdocAdded, cfgdocSectionBehavior, true},
		{"weatherUnit", cfgdocChanged, cfgdocSectionBehavior, false},
	}
	if len(changes) != len(want) {
		t.Fatalf("changes = %+v, want %d (a \"false\" written as text equals false)", changes, len(want))
	}
	for i, w := range want {
		ch := changes[i]
		if ch.Key != w.key || ch.Kind != w.kind || ch.Section != w.section || ch.Secret != w.secret {
			t.Errorf("change %d = %+v, want %+v", i, ch, w)
		}
	}
	if changes[3].Old != "C" || changes[3].New != "F" {
		t.Errorf("weatherUnit = %v → %v, want C → F", changes[3].Old, changes[3].New)
	}
	for key, section := range map[string]string{
		"swapABButtons": cfgdocSectionInput, a11yKeySwapAB: cfgdocSectionInput, "inputProfile": cfgdocSectionInput,
		"homeArrangement": cfgdocSectionLayouts, "theme_background": cfgdocSectionTheme, a11yKeyFontScale: cfgdocSectionTheme,
		"buttonColor": cfgdocSectionTheme, "weatherUnit": cfgdocSectionBehavior,
	} {
		if got := cfgdocSectionOf(key); got != section {
			t.Errorf("%s is filed under %s, want %s", key, got, section)
		}
	}
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

func TestCfgdocUpstreamListsChangedDefaultsOnce(t *testing.T) {
	prev := cfgdocDefaultsPathOverride
	cfgdocDefaultsPathOverride = filepath.Join(t.TempDir(), cfgdocDefaultsFile)
	t.Cleanup(func() { cfgdocDefaultsPathOverride = prev })

	old := cfgdocShipped{Source: "test.json", Version: "1.0", Stamp: "one",
		Values: map[string]interface{}{"weatherUnit": "C", "gridColumns": float64(4)}}
	if _, changes := cfgdocUpstream(old); len(changes) != 0 {
		t.Fatalf("a first run reported %v", changes)
	}
	newer := cfgdocShipped{Source: "test.json", Version: "1.1", Stamp: "two",
		Values: map[string]interface{}{"weatherUnit": "F", "gridColumns": float64(4), "newKey": true}}
	rec, changes := cfgdocUpstream(newer)
	if rec.Version != "1.0" || len(changes) != 2 {
		t.Fatalf("since %q: %+v, want weatherUnit changed and newKey added", rec.Version, changes)
	}
	// The same build again reports the same list until it is acknowledged.
	if _, again := cfgdocUpstream(newer); len(again) != 2 {
		t.Errorf("a second look reported %v", again)
	}
	if err := cfgdocSaveRecord(newer); err != nil {
		t.Fatal(err)
	}
	if _, after := cfgdocUpstream(newer); len(after) != 0 {
		t.Errorf("after \"got it\" the list still says %v", after)
	}
	// The other platform's shipped config is not an update.
	other := newer
	other.Source, other.Stamp = "other.json", "three"
	if _, cross := cfgdocUpstream(other); len(cross) != 0 {
		t.Errorf("another platform's defaults were reported as an update: %v", cross)
	}
}

func TestCfgdocShippedConfigsCompareCleanly(t *testing.T) {
	cases := []struct {
		source string
		data   []byte
	}{
		{"jukaconfig.json", cfgdocShippedDesktop},
		{"trimui/jukaconfig.device.json", cfgdocShippedDevice},
	}
	for _, c := range cases {
		s := cfgdocDecodeShipped(c.source, c.data)
		if s.Err != nil || len(s.Values) == 0 || s.Stamp == "" {
			t.Fatalf("%s: %v (%d values)", c.source, s.Err, len(s.Values))
		}
		if changes := cfgdocDiff(s.Values, s.Values); len(changes) != 0 {
			t.Errorf("%s differs from itself: %v", c.source, changes)
		}
		if again := cfgdocDecodeShipped(c.source, c.data); again.Stamp != s.Stamp {
			t.Errorf("%s: the defaults stamp is not stable", c.source)
		}
	}
}

func TestCfgdocSceneDeltaIgnoresFormatting(t *testing.T) {
	shipped := []byte(`{"scenes": [{"name": "Main", "elements": [{"type": "label", "text": "Hi"}]}, {"name": "Old", "elements": []}]}`)
	disk := []byte(`{"scenes": [
	  {"elements": [{"text": "Hi", "type": "label"}], "name": "Main"},
	  {"name": "Mine", "elements": [{"type": "label", "text": "new"}]}
	]}`)
	delta, err := cfgdocDiffScenes(disk, shipped)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(delta.Names, ",") != "Mine" || !delta.Added["Mine"] || strings.Join(delta.Removed, ",") != "Old" {
		t.Errorf("delta = %+v, want Mine added and Old removed, Main unchanged", delta)
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
