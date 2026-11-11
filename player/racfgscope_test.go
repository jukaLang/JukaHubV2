package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// raScopeTestSetup points the journal at a temporary folder, turns the
// failsafe timer off and answers "nothing is running", so a test drives the
// scope by hand. It returns the folder.
func raScopeTestSetup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevPath := raScopeJournalPath
	prevFailsafe := raScopeFailsafe
	prevRA := raScopeRetroArchRunning
	prevChild := raScopeChildRunning
	prevMigration := raScopeMigrationCandidates
	raScopeJournalPath = func() string { return filepath.Join(dir, raScopeJournalName) }
	raScopeFailsafe = 0
	raScopeRetroArchRunning = func() bool { return false }
	raScopeChildRunning = func() bool { return false }
	raScopeMigrationCandidates = func() []string { return nil }
	t.Cleanup(func() {
		raScope.mu.Lock()
		if raScope.timer != nil {
			raScope.timer.Stop()
			raScope.timer = nil
		}
		raScopeJournalPath = prevPath
		raScopeFailsafe = prevFailsafe
		raScopeRetroArchRunning = prevRA
		raScopeChildRunning = prevChild
		raScopeMigrationCandidates = prevMigration
		raScope.mu.Unlock()
	})
	return dir
}

func raScopeWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func raScopeRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// raScopeRestoreNow runs the restore the way the hooks do, under the lock.
func raScopeRestoreNow(t *testing.T) int {
	t.Helper()
	raScope.mu.Lock()
	defer raScope.mu.Unlock()
	n, err := raScopeRestoreLocked()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	return n
}

func TestRaConfigSetAndUnset(t *testing.T) {
	// Appended when missing, with the trailing newline kept.
	out, changed := raConfigSet("video_fullscreen = \"true\"\n", "savestate_auto_load", "true")
	if !changed || out != "video_fullscreen = \"true\"\nsavestate_auto_load = \"true\"\n" {
		t.Fatalf("append: %q (changed %t)", out, changed)
	}
	// Replaced in place: indentation, the trailing comment and CRLF survive.
	out, changed = raConfigSet("  savestate_auto_save = \"false\" # mine\r\naudio_volume = \"0.5\"\r\n", "savestate_auto_save", "true")
	if !changed || out != "  savestate_auto_save = \"true\" # mine\r\naudio_volume = \"0.5\"\r\n" {
		t.Fatalf("replace: %q (changed %t)", out, changed)
	}
	// A boolean in another capitalisation is the same value: no rewrite.
	in := "savestate_auto_save = \"True\"\n"
	if out, changed = raConfigSet(in, "savestate_auto_save", "true"); changed || out != in {
		t.Fatalf("idempotent: %q (changed %t)", out, changed)
	}
	// A '#' inside a quoted path is part of the path, not a comment.
	if v, ok := raConfigGet("recording_output_directory = \"/mnt/SDCARD/My#Clips\"\n", "recording_output_directory"); !ok || v != "/mnt/SDCARD/My#Clips" {
		t.Fatalf("quoted value = %q (%t)", v, ok)
	}
	// The first occurrence is the one RetroArch applies.
	if v, _ := raConfigGet("# a = \"9\"\na = \"1\"\na = \"2\"\n", "a"); v != "1" {
		t.Fatalf("first occurrence = %q", v)
	}
	// Mixed line endings: each line keeps its own, and an LF line inside a
	// CRLF file is still a line of its own (never joined to its neighbour).
	out, changed = raConfigSet("a = \"1\"\r\nb = \"2\"\nc = \"3\"\r\n", "b", "9")
	if !changed || out != "a = \"1\"\r\nb = \"9\"\nc = \"3\"\r\n" {
		t.Fatalf("mixed endings set: %q (changed %t)", out, changed)
	}
	out, changed = raConfigSet("a = \"1\"\nb = \"2\"\r\n", "a", "0")
	if !changed || out != "a = \"0\"\nb = \"2\"\r\n" {
		t.Fatalf("mixed endings set before a CRLF line: %q (changed %t)", out, changed)
	}
	// Unset removes every line that sets the key and nothing else.
	out, changed = raConfigUnset("a = \"1\"\r\n# a = \"0\"\r\nb = \"2\"\r\na = \"3\"\r\n", "a")
	if !changed || out != "# a = \"0\"\r\nb = \"2\"\r\n" {
		t.Fatalf("unset: %q (changed %t)", out, changed)
	}
	if _, changed = raConfigUnset("b = \"2\"\n", "a"); changed {
		t.Fatal("unset of a missing key reported a change")
	}
}

// The whole round trip: the launch's values in, the journal on the card, and
// the user's file back exactly as it was once the game is over.
func TestRaScopeApplyRestoreRoundTrip(t *testing.T) {
	dir := raScopeTestSetup(t)
	cfg := filepath.Join(dir, "RetroArch", "retroarch.cfg")
	original := "# user config\nsavestate_auto_save = \"false\"\nvideo_scale = \"2\"\n"
	raScopeWrite(t, cfg, original)

	changed, err := raScopeApply(cfg, savestateResumeValues())
	if err != nil || !changed {
		t.Fatalf("apply: changed %t, err %v", changed, err)
	}
	text := raScopeRead(t, cfg)
	for _, key := range []string{savestateAutoSaveKey, savestateAutoLoadKey} {
		if v, ok := raConfigGet(text, key); !ok || v != "true" {
			t.Fatalf("%s = %q during the launch\n%s", key, v, text)
		}
	}
	if !fileExists(filepath.Join(dir, raScopeJournalName)) {
		t.Fatal("no journal on the card while the launch's values are in place")
	}

	if n := raScopeRestoreNow(t); n != 2 {
		t.Errorf("restored %d values, want 2", n)
	}
	if got := raScopeRead(t, cfg); got != original {
		t.Fatalf("after the game the config is\n%q\nwant\n%q", got, original)
	}
	if fileExists(filepath.Join(dir, raScopeJournalName)) {
		t.Fatal("journal left behind after a full restore")
	}
	// The first edit still leaves the pre-JukaHub copy beside the config.
	if got := raScopeRead(t, cfg+savestateBackupSuffix); got != original {
		t.Fatalf("backup = %q", got)
	}
}

// RetroArch rewrites its whole config on exit (config_save_on_exit): its own
// changes survive, JukaHub's keys go back, and a key the user changed in
// RetroArch's menu during the game keeps the user's new value.
func TestRaScopeRestoreAfterRetroArchRewrote(t *testing.T) {
	dir := raScopeTestSetup(t)
	cfg := filepath.Join(dir, "retroarch.cfg")
	raScopeWrite(t, cfg, "savestate_auto_load = \"false\"\nrecording_output_directory = \"default\"\n")

	if _, err := raScopeApply(cfg, savestateResumeValues()); err != nil {
		t.Fatal(err)
	}
	if _, err := raScopeApply(cfg, []raScopeValue{{Key: retroArchRecordingDirKey, Value: "/mnt/SDCARD/Recordings"}}); err != nil {
		t.Fatal(err)
	}
	// RetroArch's save: every key, its own order and spacing, a setting the
	// user changed in the game (video_scale), and the recording folder the
	// user picked in RetroArch's menu.
	raScopeWrite(t, cfg, "video_scale = \"3\"\n"+
		"recording_output_directory = \"/mnt/SDCARD/Clips\"\n"+
		"savestate_auto_save = \"true\"\n"+
		"savestate_auto_load = \"true\"\n")

	raScopeRestoreNow(t)
	text := raScopeRead(t, cfg)
	if v, _ := raConfigGet(text, "video_scale"); v != "3" {
		t.Errorf("RetroArch's own change was lost:\n%s", text)
	}
	if v, _ := raConfigGet(text, savestateAutoLoadKey); v != "false" {
		t.Errorf("auto-load = %q, want the user's false back\n%s", v, text)
	}
	if _, ok := raConfigGet(text, savestateAutoSaveKey); ok {
		t.Errorf("auto-save was never the user's, so it must be gone again:\n%s", text)
	}
	if v, _ := raConfigGet(text, retroArchRecordingDirKey); v != "/mnt/SDCARD/Clips" {
		t.Errorf("recording folder = %q, want the one the user picked in RetroArch\n%s", v, text)
	}
}

// A crash or a power cut mid-game leaves the journal; the next start puts the
// values back, and an earlier launch's record keeps the user's value even
// when a new launch edits the same key before anything was restored.
func TestRaScopeJournalRecovery(t *testing.T) {
	dir := raScopeTestSetup(t)
	cfg := filepath.Join(dir, "retroarch.cfg")
	raScopeWrite(t, cfg, "savestate_auto_load = \"false\"\n")

	if _, err := raScopeApply(cfg, savestateResumeValues()); err != nil {
		t.Fatal(err)
	}
	// No restore: the device lost power. A second launch now sees JukaHub's
	// own value in the file; it must not take that for the user's.
	if _, err := raScopeApply(cfg, savestateResumeValues()); err != nil {
		t.Fatal(err)
	}
	raScopeStartupRecover()
	deadline := time.Now().Add(5 * time.Second)
	for fileExists(filepath.Join(dir, raScopeJournalName)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	raScope.mu.Lock() // the recovery goroutine has finished once it lets go
	raScope.mu.Unlock()
	text := raScopeRead(t, cfg)
	if v, _ := raConfigGet(text, savestateAutoLoadKey); v != "false" {
		t.Fatalf("auto-load = %q after recovery, want false\n%s", v, text)
	}
	if _, ok := raConfigGet(text, savestateAutoSaveKey); ok {
		t.Fatalf("auto-save survived recovery:\n%s", text)
	}
	if !fileExists(filepath.Join(dir, raScopeMigratedName)) {
		t.Fatal("the one-time repair did not record that it ran")
	}
}

// Without a place for the journal nothing is edited: an edit nobody can undo is
// what the scope exists to prevent.
func TestRaScopeApplyNeedsTheJournal(t *testing.T) {
	dir := raScopeTestSetup(t)
	raScopeJournalPath = func() string { return "" }
	cfg := filepath.Join(dir, "retroarch.cfg")
	raScopeWrite(t, cfg, "savestate_auto_load = \"false\"\n")
	if _, err := raScopeApply(cfg, savestateResumeValues()); err == nil {
		t.Fatal("apply without a journal succeeded")
	}
	if got := raScopeRead(t, cfg); got != "savestate_auto_load = \"false\"\n" {
		t.Fatalf("config edited without a journal: %q", got)
	}
}

// A launch whose emulator never started is put back by the failsafe; one still
// running is left to its exit.
func TestRaScopeFailsafe(t *testing.T) {
	dir := raScopeTestSetup(t)
	cfg := filepath.Join(dir, "retroarch.cfg")
	raScopeWrite(t, cfg, "savestate_auto_load = \"false\"\n")
	raScopeFailsafe = 20 * time.Millisecond
	if _, err := raScopeApply(cfg, savestateResumeValues()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fileExists(filepath.Join(dir, raScopeJournalName)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	raScope.mu.Lock()
	raScope.mu.Unlock()
	if v, _ := raConfigGet(raScopeRead(t, cfg), savestateAutoLoadKey); v != "false" {
		t.Fatalf("failsafe did not restore: auto-load = %q", v)
	}

	// With a child on screen the restore waits for that child's exit.
	raScopeFailsafe = 0
	raScopeChildRunning = func() bool { return true }
	if _, err := raScopeApply(cfg, savestateResumeValues()); err != nil {
		t.Fatal(err)
	}
	raScopeFailsafeCheck()
	raScopeRestoreAfterExit()
	if v, _ := raConfigGet(raScopeRead(t, cfg), savestateAutoLoadKey); v != "true" {
		t.Fatalf("values went back under a running child: auto-load = %q", v)
	}
	raScopeChildRunning = func() bool { return false }
	raScopeRestoreAfterExit()
	if v, _ := raConfigGet(raScopeRead(t, cfg), savestateAutoLoadKey); v != "false" {
		t.Fatalf("exit did not restore: auto-load = %q", v)
	}
}

// A key already in the journal keeps the user's value while the file still
// holds JukaHub's earlier value; a value changed since starts a new record.
func TestRaScopeMerge(t *testing.T) {
	journal := []raScopeEdit{{Path: "/c", Key: "k", Had: true, Before: "false", Wrote: "true"}}
	got := raScopeMerge(journal, []raScopeEdit{{Path: "/c", Key: "K", Had: true, Before: "true", Wrote: "x"}})
	if len(got) != 1 || got[0].Before != "false" || got[0].Wrote != "x" || !got[0].Had {
		t.Fatalf("stale record merge = %+v", got)
	}
	got = raScopeMerge(journal, []raScopeEdit{{Path: "/c", Key: "k", Had: true, Before: "other", Wrote: "true"}})
	if len(got) != 1 || got[0].Before != "other" {
		t.Fatalf("changed value merge = %+v", got)
	}
	got = raScopeMerge(journal, []raScopeEdit{{Path: "/d", Key: "k", Wrote: "true"}})
	if len(got) != 2 || journal[0].Wrote != "true" {
		t.Fatalf("other file merge = %+v (journal %+v)", got, journal)
	}
}

func TestRaScopeMigrateText(t *testing.T) {
	legacy := append(savestateResumeValues(), raScopeValue{Key: retroArchRecordingDirKey, Value: "/mnt/SDCARD/Recordings"})
	cfg := "savestate_auto_save = \"true\"\n" +
		"savestate_auto_load = \"true\"\n" +
		"recording_output_directory = \"/mnt/SDCARD/Recordings\"\n" +
		"video_scale = \"3\"\n"
	// Before JukaHub: auto-save on (the user's own), auto-load off, no
	// recording line at all.
	backup := "savestate_auto_save = \"true\"\nsavestate_auto_load = \"false\"\nvideo_scale = \"2\"\n"
	out, changed := raScopeMigrateText(cfg, backup, legacy)
	if !changed {
		t.Fatal("repair changed nothing")
	}
	if v, _ := raConfigGet(out, savestateAutoSaveKey); v != "true" {
		t.Errorf("auto-save = %q, the user had it on before JukaHub", v)
	}
	if v, _ := raConfigGet(out, savestateAutoLoadKey); v != "false" {
		t.Errorf("auto-load = %q, want the backup's false", v)
	}
	if _, ok := raConfigGet(out, retroArchRecordingDirKey); ok {
		t.Errorf("recording folder kept, the backup had none:\n%s", out)
	}
	if v, _ := raConfigGet(out, "video_scale"); v != "3" {
		t.Errorf("an unmanaged key was reverted: video_scale = %q", v)
	}
	// A value the user set since (not JukaHub's) is never touched.
	in := "savestate_auto_load = \"false\"\n"
	if out, changed = raScopeMigrateText(in, "savestate_auto_load = \"true\"\n", legacy); changed || out != in {
		t.Errorf("repair touched a value JukaHub did not write: %q", out)
	}
}

// The one-time repair edits each config with a backup once, then never again.
func TestRaScopeMigrationRunsOnce(t *testing.T) {
	dir := raScopeTestSetup(t)
	cfg := filepath.Join(dir, "RetroArch", "retroarch.cfg")
	raScopeWrite(t, cfg, "savestate_auto_load = \"true\"\n")
	raScopeWrite(t, cfg+savestateBackupSuffix, "savestate_auto_load = \"false\"\n")
	raScopeMigrationCandidates = func() []string { return []string{cfg} }
	prevConfig := appConfig
	appConfig = &Config{Variables: Variables{Custom: map[string]interface{}{recordingsEnabledConfigKey: false}}}
	t.Cleanup(func() { appConfig = prevConfig })

	raScope.mu.Lock()
	raScopeMigrateLocked(raScopeLegacyValues())
	raScope.mu.Unlock()
	if v, _ := raConfigGet(raScopeRead(t, cfg), savestateAutoLoadKey); v != "false" {
		t.Fatalf("auto-load = %q after the repair", v)
	}
	// The user turns it on again in RetroArch: the repair must not undo that.
	raScopeWrite(t, cfg, "savestate_auto_load = \"true\"\n")
	raScope.mu.Lock()
	raScopeMigrateLocked(raScopeLegacyValues())
	raScope.mu.Unlock()
	if v, _ := raConfigGet(raScopeRead(t, cfg), savestateAutoLoadKey); v != "true" {
		t.Fatalf("the repair ran twice: auto-load = %q", v)
	}
}

// One RetroArch install: the config beside the binary first, a .retroarch one
// with it, another install's config never, and one file under two names once.
func TestRaScopeTargets(t *testing.T) {
	dir := t.TempDir()
	beside := filepath.Join(dir, "RetroArch", "retroarch.cfg")
	hidden := filepath.Join(dir, "RetroArch", ".retroarch", "retroarch.cfg")
	other := filepath.Join(dir, "Emus", "RetroArch", "retroarch.cfg")
	for _, p := range []string{beside, hidden, other} {
		raScopeWrite(t, p, "a = \"1\"\n")
	}
	missing := filepath.Join(dir, "nowhere", "retroarch.cfg")
	got := raScopeTargets([]string{missing, beside, hidden, beside, other})
	if strings.Join(got, "|") != beside+"|"+hidden {
		t.Fatalf("targets = %q", got)
	}
	if got := raScopeTargets([]string{missing}); len(got) != 0 {
		t.Fatalf("targets without a config = %q", got)
	}
}

// Every catalogue of TrimUI configs puts the one beside ra64.trimui first.
func TestRetroArchConfigCandidatesPreferTheBinaryFolder(t *testing.T) {
	t.Setenv("JUKAHUB_RETROARCH_CFG", "")
	cands := retroArchConfigCandidates()
	if len(cands) < 2 || cands[0] != "/mnt/SDCARD/RetroArch/retroarch.cfg" {
		t.Fatalf("first candidates = %q", cands)
	}
}
