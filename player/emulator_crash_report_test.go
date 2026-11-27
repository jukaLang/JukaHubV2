package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testExitError runs this test binary as a child that exits with a chosen
// status, so the report sees a real *exec.ExitError on every platform.
func testExitError(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestEmulatorCrashExitHelper")
	cmd.Env = append(os.Environ(), "JUKE_TEST_EXIT="+strconv.Itoa(code))
	return cmd.Run()
}

func TestEmulatorCrashExitHelper(t *testing.T) {
	if n, err := strconv.Atoi(os.Getenv("JUKE_TEST_EXIT")); err == nil && n != 0 {
		os.Exit(n)
	}
}

func TestEmulatorCrashClassify(t *testing.T) {
	for _, tc := range []struct {
		code, sig int
		stopped   bool
		want      string
	}{
		{0, 0, false, ""},
		{-1, 11, false, "crash"},
		{-1, 6, false, "crash"},
		{-1, 9, false, "out of memory"},
		{-1, 15, false, "stopped"},
		{1, 0, false, "failed"},
		{-1, 11, true, ""},
	} {
		kind, reason := emulatorCrashClassify(tc.code, tc.sig, tc.stopped)
		if kind != tc.want {
			t.Fatalf("classify(%d,%d,stopped=%v) = %q, want %q", tc.code, tc.sig, tc.stopped, kind, tc.want)
		}
		if kind != "" && reason == "" {
			t.Fatalf("classify(%d,%d) has no reason", tc.code, tc.sig)
		}
	}
}

func TestEmulatorCrashSavesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	old := emulatorCrashDir
	emulatorCrashDir = func() (string, error) { return dir, nil }
	defer func() { emulatorCrashDir = old }()

	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "launcher.log")
	if err := os.WriteFile(logPath, []byte("core loaded\nsegmentation fault\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	diag := &gameDiagLaunch{
		game:     libraryGame{Title: "A Game", System: "SNES"},
		launcher: "retroarch",
		outPath:  logPath,
		started:  now.Add(-3 * time.Minute),
		ended:    now,
		memMB:    412,
		rec: &launchRecord{
			Title: "A Game", System: "SNES", ROM: "/roms/a.sfc", Launcher: "retroarch",
			Program: "/usr/bin/retroarch", Args: []string{"-L", "snes.so"}, Started: now.Add(-3 * time.Minute), MemMB: 412,
		},
	}
	emulatorCrashSave(diag, nil)
	if len(emulatorCrashReports()) != 0 {
		t.Fatal("a clean exit wrote a crash report")
	}
	emulatorCrashSave(diag, testExitError(t, 7))
	reports := emulatorCrashReports()
	if len(reports) != 1 {
		t.Fatalf("reports = %v", reports)
	}
	data, err := os.ReadFile(reports[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"Kind: failed", "A Game", "SNES", "retroarch", "segmentation fault", "Exit status: 7"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report has no %q:\n%s", want, text)
		}
	}
	if summary := emulatorCrashSummary(80); summary == "" || !strings.HasPrefix(summary, "emulator-crash-") {
		t.Fatalf("summary = %q", summary)
	}

	// A full folder keeps only the newest ten reports.
	for i := 0; i < emuCrashKeep+2; i++ {
		path := filepath.Join(dir, emuCrashPrefix+"20260101-00000"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	emulatorCrashPrune(dir)
	if len(emulatorCrashReports()) != emuCrashKeep {
		t.Fatalf("after pruning = %d, want %d", len(emulatorCrashReports()), emuCrashKeep)
	}
}
