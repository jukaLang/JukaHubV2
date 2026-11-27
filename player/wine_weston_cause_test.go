package main

// wine_weston_cause_test.go — the card's reason for a display that stopped on a
// library this firmware has but too old: which function, which library, which
// version (JH-6G5TZF, JH-2S4CSH).

import (
	"debug/elf"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testOldZlibTree writes the two files the Smart Pro's log named: a module
// built against ZLIB_1.2.9 and the firmware's older libz.so.1, which defines
// the versions before it and so is too old rather than missing.
func testOldZlibTree(t *testing.T) (module, libz string) {
	t.Helper()
	dir := t.TempDir()
	module = filepath.Join(dir, "libpng16.so.16")
	libz = filepath.Join(dir, "libz.so.1")
	write := func(path string, data []byte) {
		if err := os.WriteFile(path, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(libz, buildTestELF(t, testELF{
		machine: elf.EM_AARCH64,
		soname:  "libz.so.1",
		defines: []string{"ZLIB_1.2.0", "ZLIB_1.2.5"},
		exports: []string{"inflate", "crc32"},
	}))
	write(module, buildTestELF(t, testELF{
		machine: elf.EM_AARCH64,
		needed:  []string{"libc.so.6", "libz.so.1"},
		needs:   map[string][]string{"libz.so.1": {"ZLIB_1.2.0", "ZLIB_1.2.9"}},
		soname:  "libpng16.so.16",
		imports: []testImport{{name: "inflateValidate", version: "ZLIB_1.2.9"}},
	}))
	return module, libz
}

// TestWestonLoadFailureNamesAnOldLibrary is the device's line: the card's
// reason and the report's finding both name inflateValidate, libz.so.1 and
// ZLIB_1.2.9, so a report says why instead of "Weston stopped while it was
// starting".
func TestWestonLoadFailureNamesAnOldLibrary(t *testing.T) {
	module, libz := testOldZlibTree(t)
	line := "[20:30:58.500] " + westonLoadLine + module + ": undefined symbol: inflateValidate, version ZLIB_1.2.9"
	got := westonLoadFailureLine(line)
	if got == nil {
		t.Fatal("an undefined symbol at a version was not read as an old library")
	}
	if got.module != "libpng16.so.16" || got.lib != "libz.so.1" || got.sym != "inflateValidate" || got.ver != "ZLIB_1.2.9" {
		t.Fatalf("wrong cause: module %q, library %q, symbol %q, version %q", got.module, got.lib, got.sym, got.ver)
	}
	stuck := &wineDisplayStuckError{reason: got.Error(), old: got}
	for _, want := range []string{"libz.so.1", "inflateValidate", "ZLIB_1.2.9", "too old"} {
		if !strings.Contains(stuck.Error(), want) {
			t.Errorf("card reason does not say %q: %s", want, stuck.Error())
		}
	}
	if strings.Contains(got.Error(), libz) || strings.Contains(got.Error(), module) {
		t.Errorf("the reason should name the files, not their folders: %s", got.Error())
	}
	issues := launchFailureIssues(stuck)
	if len(issues) != 1 {
		t.Fatalf("the report carries %d findings, want 1", len(issues))
	}
	for _, want := range []string{"libz.so.1", "inflateValidate", "ZLIB_1.2.9"} {
		if !strings.Contains(issues[0].Title, want) {
			t.Errorf("the finding's title does not say %q: %s", want, issues[0].Title)
		}
	}
	if !strings.Contains(issues[0].Detail, "libpng16.so.16") {
		t.Errorf("the finding does not name the module: %s", issues[0].Detail)
	}
	// The report's title is built from the finding's title: it has to fit.
	if len(issues[0].Title) > bugrepTitleMax-40 {
		t.Errorf("the finding's title is too long for a report title: %d runes", len(issues[0].Title))
	}
}

// TestWestonLoadFailureReadsTheVersionForm covers the loader's other wording
// for the same failure, and that only the run's own lines are read: an older
// session's failure in the same log is not this launch's.
func TestWestonLoadFailureReadsTheVersionForm(t *testing.T) {
	module, _ := testOldZlibTree(t)
	logPath := filepath.Join(t.TempDir(), "session.log")
	old := westonLoadLine + module + ": undefined symbol: inflateValidate, version ZLIB_1.2.9\n"
	cur := westonLoadLine + module + ": version `ZLIB_1.2.9' not found (required by " + module + ")\n"
	if err := os.WriteFile(logPath, []byte(old+cur), 0o644); err != nil {
		t.Fatal(err)
	}
	got := westonLoadFailure(logPath, int64(len(old)))
	if got == nil {
		t.Fatal("the version form was not read as an old library")
	}
	if got.lib != "libz.so.1" || got.ver != "ZLIB_1.2.9" {
		t.Fatalf("wrong cause: library %q, version %q", got.lib, got.ver)
	}
	if got.sym != "" {
		t.Fatalf("a missing version named a function: %q", got.sym)
	}
	if !strings.Contains(got.Error(), "ZLIB_1.2.9") || !strings.Contains(got.Error(), "libz.so.1") {
		t.Fatalf("reason does not name the version and the library: %s", got.Error())
	}
	if issues := got.issue(); issues.Level != 0 || !strings.Contains(issues.Title, "ZLIB_1.2.9") {
		t.Fatalf("wrong finding: %+v", issues)
	}
}

// TestWestonLoadFailureIgnoresOtherLines keeps every other reason a display
// stops on the generic sentence: a missing file and a crash are not an old
// library, and the card must not claim one.
func TestWestonLoadFailureIgnoresOtherLines(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "session.log")
	text := "Starting Weston\n" +
		westonLoadLine + "libjpeg.so.8: cannot open shared object file: No such file or directory\n" +
		"wp_weston: segfault\n"
	if err := os.WriteFile(logPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := westonLoadFailure(logPath, 0); got != nil {
		t.Fatalf("a failure that is not an old library was reported as one: %s", got.Error())
	}
}
