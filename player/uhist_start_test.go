package main

// uhist_start_test.go pins REL-17: the version history's one line about how a
// build started here, read from startcheck.go's and updrescue.go's facts.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUhistStartNoteReadsStartRecords(t *testing.T) {
	dir := upchTestPatchDir(t)
	versions := filepath.Join(dir, "versions")
	if err := os.MkdirAll(versions, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(versions, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const passed = "JukaHub-20261127000000"
	if got := uhistStartNote(passed); got != "" {
		t.Fatalf("a device with no records says %q, want nothing", got)
	}
	if got := uhistStartNote(""); got != "" {
		t.Fatalf("an empty tag says %q, want nothing", got)
	}

	write(startCheckFactName, `{"tag":"JukaHub-20261127000000","model":"trimui-smart-pro","date":"2026-11-27"}`)
	if got, want := uhistStartNote(passed), "Started fine on this Smart Pro on 2026-11-27"; got != want {
		t.Fatalf("passed start = %q, want %q", got, want)
	}
	// The record names one build; another saved version does not inherit it.
	if got := uhistStartNote("JukaHub-20260101000000"); got != "" {
		t.Fatalf("an unrelated saved version says %q, want nothing", got)
	}

	// vfDateNow writes "" on a factory clock, and a model can be missing.
	write(startCheckFactName, `{"tag":"JukaHub-20261127000000","model":"trimui-brick"}`)
	if got, want := uhistStartNote(passed), "Started fine on this Brick"; got != want {
		t.Fatalf("undated passed start = %q, want %q", got, want)
	}
	write(startCheckFactName, `{"tag":"JukaHub-20261127000000","date":"2026-02-30"}`)
	if got, want := uhistStartNote(passed), "Started fine on this device"; got != want {
		t.Fatalf("implausible date = %q, want %q", got, want)
	}

	// launch.sh's rescue fact names the release it took back as "to".
	const failed = "JukaHub-20261125000000"
	write(startCheckFactName, `{"tag":"JukaHub-20260101000000","model":"trimui-smart-pro"}`)
	write(updRescueFactName, `{"from":"JukaHub-20261101000000","to":"JukaHub-20261125000000","reason":"start","model":"trimui-smart-pro","date":"2026-11-25","notice":true}`)
	if got, want := uhistStartNote(failed), "Didn't start on this Smart Pro on 2026-11-25 — not offered again"; got != want {
		t.Fatalf("failed start = %q, want %q", got, want)
	}
	if got := uhistStartNote(passed); got != "" {
		t.Fatalf("the passed build inherited the failed one's line: %q", got)
	}
	write(updRescueFactName, `{"to":"JukaHub-20261125000000"}`)
	if got, want := uhistStartNote(failed), "Didn't start on this device — not offered again"; got != want {
		t.Fatalf("undated failed start = %q, want %q", got, want)
	}
}
