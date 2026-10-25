package main

// Tests for the Recordings backend: the retroarch.cfg folder key, the folder
// resolution order, the capture scan, and the scene wiring in both shipped
// configs.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetRetroArchConfigStringOnlyTouchesItsKey(t *testing.T) {
	in := "recording_output_directory = \"default\" # writes here\r\naudio_volume = \"0.5\"\r\n"
	out, changed := setRetroArchConfigString(in, retroArchRecordingDirKey, "/mnt/SDCARD/Recordings")
	if !changed {
		t.Fatal("a different folder reported no change")
	}
	if !strings.Contains(out, "recording_output_directory = \"/mnt/SDCARD/Recordings\" # writes here\r\n") {
		t.Errorf("value or inline comment not preserved:\n%q", out)
	}
	if !strings.Contains(out, `audio_volume = "0.5"`) {
		t.Errorf("unrelated setting was dropped:\n%q", out)
	}
	if !strings.Contains(out, "\r\n") {
		t.Errorf("CRLF line endings were not preserved:\n%q", out)
	}

	again, changed := setRetroArchConfigString(out, retroArchRecordingDirKey, "/mnt/SDCARD/Recordings")
	if changed || again != out {
		t.Errorf("re-applying the same folder changed the text:\n%q", again)
	}
}

func TestSetRetroArchConfigStringAppendsAMissingKey(t *testing.T) {
	out, changed := setRetroArchConfigString(`audio_volume = "0.5"`, retroArchRecordingDirKey, "C:/Games/clips")
	if !changed {
		t.Fatal("a missing key reported no change")
	}
	if !strings.HasSuffix(out, "recording_output_directory = \"C:/Games/clips\"\n") {
		t.Errorf("key was not appended on its own line:\n%q", out)
	}
	if !strings.Contains(out, `audio_volume = "0.5"`) {
		t.Errorf("existing setting was dropped:\n%q", out)
	}
}

func TestSetRetroArchConfigStringEscapesQuotesAndBackslashes(t *testing.T) {
	out, _ := setRetroArchConfigString("", retroArchRecordingDirKey, `D:\clips\a "b"`)
	if !strings.Contains(out, `recording_output_directory = "D:\\clips\\a \"b\""`) {
		t.Errorf("value was not escaped for a quoted config line:\n%q", out)
	}
}

func TestRetroArchRecordingDirForUsesTheConfigDialect(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	absolute := filepath.Join(dir, "Movies")

	cases := []struct {
		name string
		cfg  string
		want string
	}{
		{"unset", `audio_volume = "0.5"`, ""},
		{"default", `recording_output_directory = "default"`, ""},
		{"disabled", `recording_output_directory = "disabled"`, ""},
		{"config relative", `recording_output_directory = ":captures"`, filepath.Join(dir, "captures")},
		{"absolute", `recording_output_directory = "` + filepath.ToSlash(absolute) + `"`, absolute},
	}
	for _, c := range cases {
		if got := retroArchRecordingDirFor(c.cfg, cfgPath); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRecordingsDirForPrefersTheUserThenTheConfig(t *testing.T) {
	card := t.TempDir()
	cfgPath := filepath.Join(card, "RetroArch", "retroarch.cfg")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(card, "clips")
	if err := os.WriteFile(cfgPath, []byte("recording_output_directory = \""+filepath.ToSlash(mine)+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	prevCandidates, prevRoot := recordingsCandidates, recordingsSDRoot
	recordingsCandidates = func() []string { return []string{cfgPath} }
	recordingsSDRoot = func() string { return card }
	t.Cleanup(func() { recordingsCandidates, recordingsSDRoot = prevCandidates, prevRoot })

	if got := recordingsDirFor(&Config{}); got != mine {
		t.Errorf("retroarch.cfg folder ignored: got %q, want %q", got, mine)
	}

	// The user's own setting wins over the emulator's.
	override := filepath.Join(card, "my-own-folder")
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		recordingsDirConfigKey: override,
	}}}
	if got := recordingsDirFor(config); got != override {
		t.Errorf("recordingsDir ignored: got %q, want %q", got, override)
	}

	// Nothing names a folder: the card's Recordings folder is the answer.
	if err := os.WriteFile(cfgPath, []byte(`audio_volume = "0.5"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := recordingsDirFor(&Config{}), filepath.Join(card, recordingsFolderName); got != want {
		t.Errorf("fallback = %q, want %q", got, want)
	}
}

func TestAbsoluteRecordingDirResolvesAgainstTheCard(t *testing.T) {
	card := t.TempDir()
	prevRoot := recordingsSDRoot
	recordingsSDRoot = func() string { return card }
	t.Cleanup(func() { recordingsSDRoot = prevRoot })

	if got, want := absoluteRecordingDir("Capture"), filepath.Join(card, "Capture"); got != want {
		t.Errorf("relative folder = %q, want %q", got, want)
	}
	if got := absoluteRecordingDir("   "); got != "" {
		t.Errorf("blank folder = %q, want an empty string", got)
	}
	absolute := filepath.Join(card, "elsewhere")
	if got := absoluteRecordingDir(absolute); got != absolute {
		t.Errorf("absolute folder = %q, want %q", got, absolute)
	}
}

func TestScanRecordingsListsNewestFirstAndSkipsTheRest(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int, at time.Time) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	write("old.mp4", 10, now.Add(-2*time.Hour))
	write("new.mkv", 10, now)
	write("middle.webm", 10, now.Add(-time.Hour))
	write("notes.txt", 10, now)
	write("empty.mp4", 0, now)
	if err := os.Mkdir(filepath.Join(dir, "folder.mp4"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := scanRecordings(dir)
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
		if !strings.HasPrefix(e.Path, dir) {
			t.Errorf("%q lives outside the folder: %s", e.Name, e.Path)
		}
	}
	want := []string{"new.mkv", "middle.webm", "old.mp4"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("scan = %v, want %v", names, want)
	}

	if entries := scanRecordings(filepath.Join(dir, "absent")); entries != nil {
		t.Errorf("a missing folder returned %d entries, want none", len(entries))
	}
	if entries := scanRecordings("  "); entries != nil {
		t.Error("a blank folder returned entries")
	}
}

func TestPrepareRecordingsWritesTheFolderAndBacksUpOnce(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	original := "recording_output_directory = \"default\"\naudio_volume = \"0.5\"\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec retroarch \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	clips := filepath.Join(dir, "Recordings")

	prevCandidates, prevRoot := recordingsCandidates, recordingsSDRoot
	recordingsCandidates = func() []string { return []string{cfgPath} }
	recordingsSDRoot = func() string { return dir }
	t.Cleanup(func() { recordingsCandidates, recordingsSDRoot = prevCandidates, prevRoot })
	prevConfig := appConfig
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{
		recordingsDirConfigKey: clips,
	}}}
	t.Cleanup(func() { appConfig = prevConfig })

	if !prepareRecordings(launcher) {
		t.Fatal("prepareRecordings did not find the RetroArch config")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := retroArchConfigValue(string(raw), retroArchRecordingDirKey); !ok || v != filepath.ToSlash(clips) {
		t.Errorf("recording folder = %q (found %t), want %q\n%s", v, ok, filepath.ToSlash(clips), raw)
	}
	if !strings.Contains(string(raw), `audio_volume = "0.5"`) {
		t.Errorf("unrelated setting was dropped:\n%s", raw)
	}
	if !dirExists(clips) {
		t.Error("the recording folder was not created")
	}
	backup, err := os.ReadFile(cfgPath + savestateBackupSuffix)
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want the original config", backup)
	}

	// A second launch must neither rewrite the file nor replace the backup.
	if !prepareRecordings(launcher) {
		t.Fatal("second prepareRecordings lost the config")
	}
	backup, _ = os.ReadFile(cfgPath + savestateBackupSuffix)
	if string(backup) != original {
		t.Errorf("backup was overwritten: %q", backup)
	}
}

func TestPrepareRecordingsLeavesAUserFolderAlone(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	mine := filepath.Join(dir, "mine")
	original := "recording_output_directory = \"" + filepath.ToSlash(mine) + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec retroarch \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	prevCandidates, prevRoot := recordingsCandidates, recordingsSDRoot
	recordingsCandidates = func() []string { return []string{cfgPath} }
	recordingsSDRoot = func() string { return dir }
	t.Cleanup(func() { recordingsCandidates, recordingsSDRoot = prevCandidates, prevRoot })

	if !prepareRecordings(launcher) {
		t.Fatal("prepareRecordings did not find the RetroArch config")
	}
	raw, _ := os.ReadFile(cfgPath)
	if string(raw) != original {
		t.Errorf("a folder the user picked was rewritten:\n%s", raw)
	}
	if _, err := os.Stat(cfgPath + savestateBackupSuffix); !os.IsNotExist(err) {
		t.Error("an unchanged config was backed up")
	}
}

func TestPrepareRecordingsRespectsOptOutAndStandaloneEmulators(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	if err := os.WriteFile(cfgPath, []byte(`audio_volume = "0.5"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	retro := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(retro, []byte("#!/bin/sh\nexec retroarch \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	standalone := filepath.Join(dir, "mgba.sh")
	if err := os.WriteFile(standalone, []byte("#!/bin/sh\nexec mgba \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	prevCandidates, prevRoot := recordingsCandidates, recordingsSDRoot
	recordingsCandidates = func() []string { return []string{cfgPath} }
	recordingsSDRoot = func() string { return dir }
	t.Cleanup(func() { recordingsCandidates, recordingsSDRoot = prevCandidates, prevRoot })

	if prepareRecordings(standalone) {
		t.Error("a standalone emulator reported the folder as configured")
	}
	if raw, _ := os.ReadFile(cfgPath); strings.Contains(string(raw), retroArchRecordingDirKey) {
		t.Errorf("standalone launch edited retroarch.cfg:\n%s", raw)
	}

	prevConfig := appConfig
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{
		recordingsEnabledConfigKey: false,
	}}}
	t.Cleanup(func() { appConfig = prevConfig })

	if prepareRecordings(retro) {
		t.Error("the folder was configured even though the user switched recordings off")
	}
	if raw, _ := os.ReadFile(cfgPath); strings.Contains(string(raw), retroArchRecordingDirKey) {
		t.Errorf("config was edited despite the opt-out:\n%s", raw)
	}
}

func TestLoadRecordingsPublishesTheList(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.mp4", "b.mkv"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		recordingsDirConfigKey: dir,
	}}}

	if got := loadRecordings(config); got != 2 {
		t.Errorf("loadRecordings = %d, want 2", got)
	}
	entries, ok := config.Variables.Custom[recordingsEntriesVar].([]FileEntry)
	if !ok || len(entries) != 2 {
		t.Fatalf("%s = %#v, want two entries", recordingsEntriesVar, config.Variables.Custom[recordingsEntriesVar])
	}
	if got, _ := config.Variables.Custom[recordingsDirVar].(string); got != dir {
		t.Errorf("%s = %q, want %q", recordingsDirVar, got, dir)
	}
	if msg := recordingsScanMessage(config, 0); !strings.Contains(msg, dir) {
		t.Errorf("empty message %q does not name the folder", msg)
	}
	if msg := recordingsScanMessage(config, 2); !strings.Contains(msg, "2 captures") {
		t.Errorf("count message = %q", msg)
	}
}

// TestRecordingsSceneIsWiredInBothConfigs keeps the scene, the list variable and
// the two buttons from drifting apart: the Media button must reach a scene that
// exists, and that scene must rescan with a trigger the code handles.
func TestRecordingsSceneIsWiredInBothConfigs(t *testing.T) {
	for _, name := range []string{"jukaconfig.json", "trimui/jukaconfig.device.json"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		idx := sceneIndexExact(&cfg, "Recordings")
		if idx < 0 {
			t.Fatalf("%s has no Recordings scene", name)
		}
		dl, ok := sceneDynamicListElement(cfg.Scenes[idx])
		if !ok || dl.Variable != recordingsEntriesVar {
			t.Fatalf("%s: Recordings list variable = %q, want %q", name, dl.Variable, recordingsEntriesVar)
		}
		byTrigger := map[string]bool{}
		for _, e := range cfg.Scenes[idx].Elements {
			if e.Type == "button" {
				byTrigger[e.Trigger] = true
			}
		}
		for _, want := range []string{triggerRecordingsScan, "change_scene:Tube"} {
			if !byTrigger[want] {
				t.Errorf("%s: Recordings scene has no button with trigger %q", name, want)
			}
		}

	}

	// The Apps launcher is how the screen is reached: the Media row is already
	// full (the IPStream chip is injected into it at runtime), so there is no
	// room for another button there and the applet table carries the entry.
	found := false
	for _, a := range builtinApplets {
		if a.Scene == "Recordings" {
			found = true
		}
	}
	if !found {
		t.Error("no built-in applet opens the Recordings scene")
	}
}
