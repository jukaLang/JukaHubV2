package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withRaScopeTest points the launch journal at a temp file and turns its
// failsafe timer off, so a test can apply and restore deterministically.
func withRaScopeTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldPath, oldFailsafe := raScopeJournalPath, raScopeFailsafe
	raScopeJournalPath = func() string { return filepath.Join(dir, raScopeJournalName) }
	raScopeFailsafe = 0
	t.Cleanup(func() {
		raScopeJournalPath, raScopeFailsafe = oldPath, oldFailsafe
		raScope.mu.Lock()
		if raScope.timer != nil {
			raScope.timer.Stop()
			raScope.timer = nil
		}
		raScope.mu.Unlock()
	})
	return dir
}

func writeStandaloneConfig(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readStandaloneConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestStandaloneSaveOnExitScopesAndRestores: Flycast's two keys are turned on
// for one launch, journaled in Flycast's own `key = value` syntax, and put
// back after the game; an unrelated setting is never touched.
func TestStandaloneSaveOnExitScopesAndRestores(t *testing.T) {
	withRaScopeTest(t)
	dir := t.TempDir()
	launcher := filepath.Join(dir, "Flycast.exe")
	cfg := filepath.Join(dir, "emu.cfg")
	writeStandaloneConfig(t, cfg, "# user settings\nDreamcast.AutoSaveState = no\nDreamcast.AutoLoadState = no\nDreamcast.Region = 3\n")
	if err := os.WriteFile(launcher, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	prepareStandaloneSaveOnExit(launcher, []string{dir})

	got := readStandaloneConfig(t, cfg)
	if !strings.Contains(got, "Dreamcast.AutoSaveState = yes") || !strings.Contains(got, "Dreamcast.AutoLoadState = yes") {
		t.Fatalf("Flycast config after prepare:\n%s", got)
	}
	if !strings.Contains(got, "Dreamcast.Region = 3") {
		t.Fatalf("prepare changed an unrelated key:\n%s", got)
	}
	journal := raScopeLoadLocked()
	if len(journal.Edits) != 2 {
		t.Fatalf("journal has %d edits, want 2: %+v", len(journal.Edits), journal.Edits)
	}
	for _, e := range journal.Edits {
		if e.Format != raFormatEquals {
			t.Errorf("%s format = %q, want %q", e.Key, e.Format, raFormatEquals)
		}
	}
	if save, load := sessionSafeFlycastFlags(got); !save || !load {
		t.Errorf("Flycast flags after prepare = save %v, load %v; want both on", save, load)
	}

	if n, err := raScopeRestoreLocked(); err != nil || n == 0 {
		t.Fatalf("restore = %d, %v; want edits restored", n, err)
	}
	got = readStandaloneConfig(t, cfg)
	if !strings.Contains(got, "Dreamcast.AutoSaveState = no") || !strings.Contains(got, "Dreamcast.AutoLoadState = no") {
		t.Fatalf("Flycast config after restore:\n%s", got)
	}
	if !strings.Contains(got, "Dreamcast.Region = 3") {
		t.Fatalf("restore changed an unrelated key:\n%s", got)
	}
	if journal := raScopeLoadLocked(); len(journal.Edits) != 0 {
		t.Fatalf("journal not cleared: %+v", journal.Edits)
	}
}

// TestStandaloneSaveOnExitKeepsAPlayerChange: the player switched Flycast's
// save-on-exit off during the game, so the restore leaves that value alone
// while still putting back the key the player did not touch.
func TestStandaloneSaveOnExitKeepsAPlayerChange(t *testing.T) {
	withRaScopeTest(t)
	dir := t.TempDir()
	launcher := filepath.Join(dir, "flycast")
	cfg := filepath.Join(dir, "emu.cfg")
	writeStandaloneConfig(t, cfg, "Dreamcast.AutoSaveState = no\nDreamcast.AutoLoadState = no\n")
	if err := os.WriteFile(launcher, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	prepareStandaloneSaveOnExit(launcher, []string{dir})
	// The emulator's menu wrote its own value while the game was running.
	writeStandaloneConfig(t, cfg, strings.Replace(readStandaloneConfig(t, cfg), "Dreamcast.AutoSaveState = yes", "Dreamcast.AutoSaveState = no", 1))

	if _, err := raScopeRestoreLocked(); err != nil {
		t.Fatal(err)
	}
	got := readStandaloneConfig(t, cfg)
	if !strings.Contains(got, "Dreamcast.AutoSaveState = no") {
		t.Fatalf("player's AutoSaveState was overwritten:\n%s", got)
	}
	if !strings.Contains(got, "Dreamcast.AutoLoadState = no") {
		t.Fatalf("untouched AutoLoadState was not restored:\n%s", got)
	}
}

// TestStandaloneSaveOnExitJournalRestoresAtTheNextStart: the journal is on
// disk after the edit, so a crash that loses this process still puts the
// user's values back at the next start (raScopeStartupRecover calls the same
// restore).
func TestStandaloneSaveOnExitJournalRestoresAtTheNextStart(t *testing.T) {
	withRaScopeTest(t)
	dir := t.TempDir()
	launcher := filepath.Join(dir, "flycast")
	cfg := filepath.Join(dir, "emu.cfg")
	writeStandaloneConfig(t, cfg, "Dreamcast.AutoSaveState = no\n")
	if err := os.WriteFile(launcher, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepareStandaloneSaveOnExit(launcher, []string{dir})
	if !fileExists(filepath.Join(filepath.Dir(raScopeJournalPath()), raScopeJournalName)) {
		t.Fatal("the launch journal was not written before the edit")
	}
	// A new process starts: no in-memory state from the launch is left, and
	// the startup recovery reads the journal from disk.
	if n, err := raScopeRestoreLocked(); err != nil || n == 0 {
		t.Fatalf("startup restore = %d, %v", n, err)
	}
	if got := readStandaloneConfig(t, cfg); got != "Dreamcast.AutoSaveState = no\n" {
		t.Fatalf("config after a crash restore = %q", got)
	}
}

// TestStandaloneSaveOnExitSyntaxes: DraStic's equals syntax and Mednafen's
// space syntax are edited and restored with their own formatting, and their
// recipe flags then report save-on-exit on.
func TestStandaloneSaveOnExitSyntaxes(t *testing.T) {
	withRaScopeTest(t)
	dir := t.TempDir()

	dra := filepath.Join(dir, "drastic", "config", "drastic.cfg")
	writeStandaloneConfig(t, dra, "[General]\nenable_autosavestate = 0\n")
	draLauncher := filepath.Join(dir, "drastic", "drastic")
	if err := os.WriteFile(draLauncher, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepareStandaloneSaveOnExit(draLauncher, []string{filepath.Dir(draLauncher)})
	if got := readStandaloneConfig(t, dra); !strings.Contains(got, "enable_autosavestate = 1") {
		t.Fatalf("DraStic config after prepare:\n%s", got)
	}
	if save, load := sessionSafeDraSticFlags(readStandaloneConfig(t, dra)); !save || load {
		t.Errorf("DraStic flags = save %v, load %v; want save on and no load claim", save, load)
	}

	med := filepath.Join(dir, "mednafen", ".mednafen", "mednafen.cfg")
	writeStandaloneConfig(t, med, "; mednafen settings\nautosave 0\nsound 1\n")
	medLauncher := filepath.Join(dir, "mednafen", "mednafen")
	if err := os.WriteFile(medLauncher, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepareStandaloneSaveOnExit(medLauncher, []string{filepath.Dir(medLauncher)})
	if got := readStandaloneConfig(t, med); !strings.Contains(got, "autosave 1") || !strings.Contains(got, "sound 1") {
		t.Fatalf("Mednafen config after prepare:\n%s", got)
	}
	if save, load := sessionSafeMednafenFlags(readStandaloneConfig(t, med)); !save || !load {
		t.Errorf("Mednafen flags = save %v, load %v; want both on", save, load)
	}

	if n, err := raScopeRestoreLocked(); err != nil || n == 0 {
		t.Fatalf("restore = %d, %v", n, err)
	}
	if got := readStandaloneConfig(t, dra); !strings.Contains(got, "enable_autosavestate = 0") {
		t.Fatalf("DraStic config after restore:\n%s", got)
	}
	if got := readStandaloneConfig(t, med); !strings.Contains(got, "autosave 0") {
		t.Fatalf("Mednafen config after restore:\n%s", got)
	}
}

// TestStandaloneSaveOnExitKeepsUserTail: a rewritten line keeps its trailing
// comment and CRLF ending, and a key absent from the file is appended and
// removed again.
func TestStandaloneSaveOnExitKeepsUserTail(t *testing.T) {
	withRaScopeTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "emu.cfg")
	writeStandaloneConfig(t, path, "Dreamcast.AutoSaveState = no # the user's choice\r\n")
	changed, err := raScopeApplyFormat(path, sessionSafeSaveOnExitValues("flycast")[:1], raFormatEquals)
	if err != nil || !changed {
		t.Fatalf("apply = %v, %v", changed, err)
	}
	got := readStandaloneConfig(t, path)
	if !strings.Contains(got, "Dreamcast.AutoSaveState = yes # the user's choice\r\n") {
		t.Fatalf("line tail or CRLF lost:\n%q", got)
	}
	if _, err := raScopeRestoreLocked(); err != nil {
		t.Fatal(err)
	}
	if got := readStandaloneConfig(t, path); got != "Dreamcast.AutoSaveState = no # the user's choice\r\n" {
		t.Fatalf("restore = %q", got)
	}

	// A key the user never had is appended, then removed by the restore.
	path2 := filepath.Join(dir, "mednafen.cfg")
	writeStandaloneConfig(t, path2, "sound 1\n")
	if changed, err := raScopeApplyFormat(path2, []raScopeValue{{Key: "autosave", Value: "1"}}, raFormatSpace); err != nil || !changed {
		t.Fatalf("append = %v, %v", changed, err)
	}
	if got := readStandaloneConfig(t, path2); got != "sound 1\nautosave 1\n" {
		t.Fatalf("append = %q", got)
	}
	if _, err := raScopeRestoreLocked(); err != nil {
		t.Fatal(err)
	}
	if got := readStandaloneConfig(t, path2); got != "sound 1\n" {
		t.Fatalf("remove appended = %q", got)
	}
}
