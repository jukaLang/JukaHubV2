package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureSavestateResumeAppendsMissingSwitches(t *testing.T) {
	in := "# stock config\nvideo_fullscreen = \"true\"\n"
	out, changed := ensureSavestateResume(in)
	if !changed {
		t.Fatal("missing switches reported no change")
	}
	for _, key := range []string{savestateAutoSaveKey, savestateAutoLoadKey} {
		if v, ok := retroArchConfigValue(out, key); !ok || !strings.EqualFold(v, "true") {
			t.Errorf("%s = %q (found %t), want true\n%s", key, v, ok, out)
		}
	}
	if !strings.Contains(out, "# stock config") || !strings.Contains(out, `video_fullscreen = "true"`) {
		t.Errorf("unrelated config was not preserved:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("appended config lost its trailing newline")
	}
}

func TestEnsureSavestateResumeReplacesFalseAndKeepsComments(t *testing.T) {
	in := "savestate_auto_save = \"false\" # keep this note\r\n" +
		"savestate_auto_load = \"false\"\r\n" +
		"audio_volume = \"0.5\"\r\n"
	out, changed := ensureSavestateResume(in)
	if !changed {
		t.Fatal("false switches reported no change")
	}
	if !strings.Contains(out, `savestate_auto_save = "true" # keep this note`) {
		t.Errorf("inline comment or value not preserved:\n%q", out)
	}
	if !strings.Contains(out, "\r\n") {
		t.Errorf("CRLF line endings were not preserved:\n%q", out)
	}
	if !strings.Contains(out, `audio_volume = "0.5"`) {
		t.Errorf("unrelated setting was dropped:\n%q", out)
	}
}

func TestEnsureSavestateResumeIsIdempotent(t *testing.T) {
	in := "savestate_auto_save = \"true\"\nsavestate_auto_load = \"true\"\n"
	out, changed := ensureSavestateResume(in)
	if changed {
		t.Fatal("already-enabled config reported a change")
	}
	if out != in {
		t.Fatalf("text changed without a change report:\n%q", out)
	}
}

func TestEnableSavestateResumeBacksUpOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retroarch.cfg")
	original := "savestate_auto_save = \"false\"\nsavestate_auto_load = \"false\"\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	changed, err := enableSavestateResume(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first enable reported no change")
	}
	backup, err := os.ReadFile(path + savestateBackupSuffix)
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want the original config", backup)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits; the mode is only meaningful on a
	// handheld or a Unix desktop.
	if !IsWindows() && info.Mode().Perm() != 0o640 {
		t.Errorf("config mode = %o, want 640", info.Mode().Perm())
	}

	// A second launch must not rewrite the file or replace the backup with the
	// already-edited text.
	changed, err = enableSavestateResume(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second enable rewrote an already-enabled config")
	}
	backup, _ = os.ReadFile(path + savestateBackupSuffix)
	if string(backup) != original {
		t.Errorf("backup was overwritten: %q", backup)
	}
}

func TestRetroArchLauncherDetection(t *testing.T) {
	dir := t.TempDir()
	direct := filepath.Join(dir, "RetroArch.exe")
	if err := os.WriteFile(direct, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /usr/bin/retroarch -L core.so \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	standalone := filepath.Join(dir, "mgba.sh")
	if err := os.WriteFile(standalone, []byte("#!/bin/sh\nexec mgba \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path string
		want bool
	}{
		{direct, true},
		{script, true},
		{"/mnt/SDCARD/Emus/RetroArch/launch.sh", true},
		{standalone, false},
		{"", false},
		{filepath.Join(dir, "absent.sh"), false},
	}
	for _, c := range cases {
		if got := retroArchLauncher(c.path); got != c.want {
			t.Errorf("retroArchLauncher(%q) = %t, want %t", c.path, got, c.want)
		}
	}
}

func TestPrepareSavestateResumeWritesTheDiscoveredConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	if err := os.WriteFile(cfgPath, []byte("savestate_auto_save = \"false\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec retroarch \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	prevCandidates := savestateResumeCandidates
	savestateResumeCandidates = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { savestateResumeCandidates = prevCandidates })

	if !prepareSavestateResume(launcher) {
		t.Fatal("prepareSavestateResume did not find the RetroArch config")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := retroArchConfigValue(string(raw), savestateAutoLoadKey); !ok || !strings.EqualFold(v, "true") {
		t.Errorf("auto-load = %q (found %t), want true\n%s", v, ok, raw)
	}
}

func TestPrepareSavestateResumeRespectsOptOut(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	if err := os.WriteFile(cfgPath, []byte("video_fullscreen = \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec retroarch \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	prevCandidates := savestateResumeCandidates
	savestateResumeCandidates = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { savestateResumeCandidates = prevCandidates })

	prevConfig := appConfig
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{
		savestateResumeConfigKey: false,
	}}}
	t.Cleanup(func() { appConfig = prevConfig })

	if prepareSavestateResume(launcher) {
		t.Error("resume was configured even though the user switched it off")
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), savestateAutoSaveKey) {
		t.Errorf("config was edited despite the opt-out:\n%s", raw)
	}
}

func TestPrepareSavestateResumeLeavesStandaloneEmulatorsAlone(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "retroarch.cfg")
	if err := os.WriteFile(cfgPath, []byte("video_fullscreen = \"true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "mgba.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec mgba \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	prevCandidates := savestateResumeCandidates
	savestateResumeCandidates = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { savestateResumeCandidates = prevCandidates })

	if prepareSavestateResume(launcher) {
		t.Error("a standalone emulator reported RetroArch resume as configured")
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), savestateAutoSaveKey) {
		t.Errorf("standalone launch edited retroarch.cfg:\n%s", raw)
	}
}
