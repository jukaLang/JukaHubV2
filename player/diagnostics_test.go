package main

// diagnostics_test.go pins the support facts in DiagnosticsText. The report is
// what users paste into a bug report and what the Patch export writes, so it has
// to carry enough to triage a device (free space, connectivity, detected
// emulators, pending update) while still leaking nothing private.

import (
	"path/filepath"
	"strings"
	"testing"
)

func withTempPatchState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prevFn, prevDir := patchStateDirFn, patchStateDir
	patchStateDirFn = func() (string, error) { return dir, nil }
	patchStateDir = dir
	t.Cleanup(func() {
		patchStateDirFn = prevFn
		patchStateDir = prevDir
	})
	return dir
}

func TestDiagnosticsTextCarriesSupportFacts(t *testing.T) {
	dir := withTempPatchState(t)
	text := DiagnosticsText(&Config{Version: "1.2.3"})

	for _, want := range []string{
		"JukaHub diagnostics",
		"Version      : ",
		"1.2.3",
		"Platform     : ",
		"Device       : ",
		"Storage free : ",
		"Network      : ",
		"Backups      : ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics is missing %q:\n%s", want, text)
		}
	}
	// A failed probe is reported as "unknown" rather than dropped: a device that
	// cannot read its own free space is itself support information.
	sawStorage := strings.Contains(text, "Storage free : unknown")
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "Storage free : ") && strings.HasSuffix(line, " MiB") {
			sawStorage = true
		}
	}
	if !sawStorage {
		t.Fatalf("storage line carries neither a size nor an explicit unknown:\n%s", text)
	}
	// The report must not carry the paths it probed.
	if strings.Contains(text, dir) {
		t.Fatalf("diagnostics leaked the state path %q:\n%s", dir, text)
	}
	if strings.Contains(text, string(filepath.Separator)+"Users"+string(filepath.Separator)) {
		t.Fatalf("diagnostics leaked a user path:\n%s", text)
	}
}

// TestDiagnosticsTextAddsAFactOnlyWhenItMatters keeps the report short: a
// pending update and config errors are only mentioned when they exist.
func TestDiagnosticsTextAddsAFactOnlyWhenItMatters(t *testing.T) {
	withTempPatchState(t)
	patchStateMu.Lock()
	prevTag, prevAt := patchState.AvailableUpdateTag, patchState.AvailableUpdateAt
	patchState.AvailableUpdateTag, patchState.AvailableUpdateAt = "", ""
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		patchState.AvailableUpdateTag, patchState.AvailableUpdateAt = prevTag, prevAt
		patchStateMu.Unlock()
	})

	text := DiagnosticsText(nil)
	if strings.Contains(text, "Update ready") {
		t.Fatalf("diagnostics advertises an update with none pending:\n%s", text)
	}

	patchStateMu.Lock()
	patchState.AvailableUpdateTag = "JukaHub-20261020154603"
	patchState.AvailableUpdateAt = "2026-10-20T15:46:03Z"
	patchStateMu.Unlock()
	text = DiagnosticsText(nil)
	if !strings.Contains(text, "Update ready : 2026-10-20") {
		t.Fatalf("diagnostics does not name the pending release:\n%s", text)
	}
}
