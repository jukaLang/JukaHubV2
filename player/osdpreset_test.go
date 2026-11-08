package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole safety argument for this manager is that it owns display switches
// and never a hotkey. A bind is a numeric joypad code, and the code for "hold
// Select and press X" differs per firmware, so a guessed one silently breaks a
// control the user relies on.
func TestOSDPresetsNeverWriteInputBindings(t *testing.T) {
	if len(osdPresetValues) == 0 {
		t.Fatal("no presets with values")
	}
	for preset, pairs := range osdPresetValues {
		if len(pairs) == 0 {
			t.Fatalf("%s has no keys", preset)
		}
		for _, pair := range pairs {
			if strings.HasPrefix(strings.ToLower(pair[0]), "input") {
				t.Fatalf("%s writes %q: hotkey binds must not be guessed", preset, pair[0])
			}
		}
	}
	for _, name := range osdPresetOrder {
		if name == "unmanaged" {
			continue
		}
		if _, ok := osdPresetValues[name]; !ok {
			t.Fatalf("%q is in the cycle order but has no values", name)
		}
		if _, ok := osdPresetLabels[name]; !ok {
			t.Fatalf("%q has no label", name)
		}
	}
}

func TestEnsureOSDPresetTouchesOnlyItsKeys(t *testing.T) {
	cfg := "# managed by hand\r\nvideo_font_enable = \"true\"  # keep me\r\ninput_menu_toggle_btn = \"5\"\r\nfps_show = \"false\"\r\n"
	out, changed := ensureOSDPreset(cfg, "quiet")
	if !changed {
		t.Fatal("quiet should change the config")
	}
	for _, want := range []string{
		`video_font_enable = "false"`,
		`# keep me`,
		`input_menu_toggle_btn = "5"`,
		`menu_enable_widgets = "false"`,
		`framecount_show = "false"`,
		`memory_show = "false"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The file used CRLF; the edit must not mix line endings.
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Errorf("line endings were not preserved:\n%q", out)
	}

	again, changedAgain := ensureOSDPreset(out, "quiet")
	if changedAgain {
		t.Error("applying the same preset twice must be a no-op")
	}
	if again != out {
		t.Error("the text changed on the second pass")
	}
}

func TestEnsureOSDPresetUnmanagedLeavesTheTextAlone(t *testing.T) {
	cfg := "video_font_enable = \"false\"\n"
	for _, preset := range []string{"unmanaged", "", "nonsense"} {
		out, changed := ensureOSDPreset(cfg, preset)
		if changed || out != cfg {
			t.Fatalf("%q changed the config: %q", preset, out)
		}
	}
}

func TestApplyOSDPresetToFileKeepsOneBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retroarch.cfg")
	original := "video_font_enable = \"true\"\ninput_menu_toggle_btn = \"5\"\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := applyOSDPresetToFile(path, "stats")
	if err != nil || !changed {
		t.Fatalf("first apply: changed=%v err=%v", changed, err)
	}
	backup, err := os.ReadFile(path + savestateBackupSuffix)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want the original text", backup)
	}
	body, _ := os.ReadFile(path)
	for _, want := range []string{
		`fps_show = "true"`, `framecount_show = "true"`, `memory_show = "true"`,
		`video_font_enable = "true"`, `input_menu_toggle_btn = "5"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}

	changed, err = applyOSDPresetToFile(path, "stats")
	if err != nil || changed {
		t.Fatalf("second apply: changed=%v err=%v, want false", changed, err)
	}
	// Overwriting the backup with the rewritten file would lose the only copy of
	// the user's own settings.
	backup, _ = os.ReadFile(path + savestateBackupSuffix)
	if string(backup) != original {
		t.Error("the backup was overwritten by a later apply")
	}
}

func TestPrepareOSDPresetSkipsStandaloneEmulators(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retroarch.cfg")
	if err := os.WriteFile(path, []byte("fps_show = \"false\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldCandidates := osdPresetCandidates
	osdPresetCandidates = func() []string { return []string{path} }
	oldConfig := appConfig
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{osdPresetConfigKey: "stats"}}}
	defer func() {
		osdPresetCandidates = oldCandidates
		appConfig = oldConfig
	}()

	if prepareOSDPreset("/usr/bin/mgba") {
		t.Fatal("a standalone emulator owns its own OSD and must be skipped")
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "fps_show = \"true\"") {
		t.Fatal("the config was rewritten for a standalone emulator")
	}

	if !prepareOSDPreset("/mnt/SDCARD/RetroArch/retroarch") {
		t.Fatal("a RetroArch launch should find and configure the config")
	}
	body, _ = os.ReadFile(path)
	if !strings.Contains(string(body), `fps_show = "true"`) {
		t.Fatalf("the preset was not applied:\n%s", body)
	}
	// With no choice saved, the launch path must leave the file alone.
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	before, _ := os.ReadFile(path)
	if prepareOSDPreset("/mnt/SDCARD/RetroArch/retroarch") {
		t.Fatal("an unmanaged preset must not touch the config")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("the config changed even though the preset is unmanaged")
	}
}

// The row has to exist on the page the settings screen actually builds, twice
// over must not add it twice, and the label the row substitutes has to be
// seeded or the row would print its own placeholder.
func TestOSDPresetRowIsInjectedOnce(t *testing.T) {
	config := &Config{}
	ensureSettingsWidgets(config)
	ensureOSDPresetSetting(config)
	ensureOSDPresetSetting(config)

	scene := sceneByName(config, smartSettingsScene)
	if scene == nil {
		t.Fatal("the Smart Widgets page was not created")
	}
	rows := 0
	for _, el := range scene.Elements {
		if strings.EqualFold(strings.TrimSpace(el.Trigger), "cycle_osd_preset") {
			rows++
			if !strings.Contains(el.Text, "$osdPresetLabel") {
				t.Errorf("row text %q does not name the label it substitutes", el.Text)
			}
			if el.Y < 40 {
				t.Errorf("row y=%d is off the page", el.Y)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("%d OSD rows on the page, want 1", rows)
	}
	if config.Variables.Custom["osdPresetLabel"] == nil {
		t.Fatal("the substituted label was not seeded")
	}
}

func TestOSDPresetChoiceCyclesAndRoundTrips(t *testing.T) {
	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	got := "unmanaged"
	for _, want := range []string{"quiet", "minimal", "stats", "unmanaged"} {
		got = nextOSDPreset(got)
		if got != want {
			t.Fatalf("next(%q) = %q, want %q", got, got, want)
		}
	}
	// An unknown stored value is normalised to "unmanaged" first, so the cycle
	// continues from there instead of picking an arbitrary preset.
	if got := nextOSDPreset("bogus"); got != "quiet" {
		t.Fatalf("next(bogus) = %q, want quiet", got)
	}

	label := setOSDPresetChoice(config, "stats")
	if label != osdPresetLabels["stats"] {
		t.Fatalf("label = %q", label)
	}
	if osdPresetForConfig(config) != "stats" {
		t.Fatal("the choice did not round-trip")
	}
	if config.Variables.Custom[osdPresetConfigKey+"Label"] != label {
		t.Fatal("the row label was not seeded")
	}
	if osdPresetForConfig(nil) != "unmanaged" {
		t.Fatal("a nil config must mean unmanaged")
	}
}
