package main

// Tests for the crash reports: telling a crashed session from a clean one,
// filing and pruning the reports, the excerpt a bug report carries, and the
// exit status after a crash.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCrashHasReport(t *testing.T) {
	cases := []struct {
		data string
		want bool
	}{
		{"", false},
		{"JukaHub session started 2026-11-09", false},
		{"JukaHub session started 2026-11-09\n", false},
		{"JukaHub session started 2026-11-09\n  \n", false},
		{"JukaHub session started 2026-11-09\npanic: boom\n", true},
		{"JukaHub session started 2026-11-09\nfatal error: concurrent map writes\n", true},
	}
	for _, c := range cases {
		if got := crashHasReport([]byte(c.data)); got != c.want {
			t.Errorf("crashHasReport(%q) = %v, want %v", c.data, got, c.want)
		}
	}
}

func TestCrashCollectFilesReportsAndPrunes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, mod time.Time) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	for i := 0; i < 6; i++ {
		write("crash-2026110"+string(rune('1'+i))+"-100000.txt", "old report", base.Add(time.Duration(i)*time.Hour))
	}
	clean := write(crashSessionPrefix+"100.txt", "JukaHub session started …\n", base.Add(10*time.Hour))
	crashAt := base.Add(11 * time.Hour)
	write(crashSessionPrefix+"200.txt", "JukaHub session started …\npanic: boom\n\ngoroutine 1 [running]:\nmain.main()\n", crashAt)

	archived, at := crashCollect(dir)
	if archived == "" || !at.Equal(crashAt) {
		t.Fatalf("crashed session not filed: %q %v", archived, at)
	}
	if want := "crash-" + crashAt.Format("20060102-150405") + ".txt"; filepath.Base(archived) != want {
		t.Fatalf("report name %q, want %q", filepath.Base(archived), want)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Fatalf("a clean session's file must be removed: %v", err)
	}
	crashPrune(dir, crashKeep, archived)
	reports := crashReports(dir)
	if len(reports) != crashKeep {
		t.Fatalf("kept %d reports, want %d: %v", len(reports), crashKeep, reports)
	}
	if !sameFilePath(reports[0], archived) {
		t.Fatalf("the newest report must come first and stay: %v", reports)
	}
	for _, r := range reports {
		if strings.Contains(r, "20261101-100000") || strings.Contains(r, "20261102-100000") {
			t.Fatalf("an oldest report survived the prune: %v", reports)
		}
	}
}

func TestCrashPruneKeepsTheNewReportWhateverItsClock(t *testing.T) {
	dir := t.TempDir()
	good := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	for i := 0; i < crashKeep; i++ {
		p := filepath.Join(dir, "crash-2026110"+string(rune('1'+i))+"-100000.txt")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, good.Add(time.Duration(i)*time.Hour), good.Add(time.Duration(i)*time.Hour))
	}
	// Written after a cold boot without an RTC: its time sorts first.
	epoch := time.Date(2020, 1, 1, 0, 0, 30, 0, time.Local)
	fresh := filepath.Join(dir, "crash-20200101-000030.txt")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(fresh, epoch, epoch)
	crashPrune(dir, crashKeep, fresh)
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("the report this start made must survive: %v", err)
	}
	if n := len(crashReports(dir)); n != crashKeep {
		t.Fatalf("kept %d, want %d", n, crashKeep)
	}
}

func TestCrashSummary(t *testing.T) {
	text := "JukaHub session started 2026-11-09 14:00:00 +0000 · v · b · linux/arm64 · pid 7\n" +
		"panic: runtime error: index out of range [3] with length 3 [recovered]\n" +
		"\tpanic: runtime error: index out of range [3] with length 3\n\n" +
		"goroutine 1 [running]:\n" +
		"main.renderHome(...)\n\t/src/player/home.go:120 +0x1c\n" +
		"main.main()\n\t/src/player/main.go:3300 +0x2a4\n\n" +
		"goroutine 9 [select]:\nmain.netmonLoop()\n\t/src/player/netmon.go:40 +0x10\n"
	got := crashSummary(text)
	for _, want := range []string{"panic: runtime error: index out of range", "goroutine 1 [running]:", "main.renderHome"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "netmonLoop") || strings.Contains(got, "JukaHub session started") {
		t.Errorf("summary carries more than the crashing goroutine:\n%s", got)
	}
	if long := crashSummary("JukaHub session\npanic: x\n\ngoroutine 1 [running]:\n" + strings.Repeat("main.f()\n\t/x.go:1\n", 2000)); len(long) > crashSummaryMax+64 {
		t.Errorf("summary is not bounded: %d bytes", len(long))
	}
}

func TestProcessExitCodeAfterACrash(t *testing.T) {
	prev := crashInProgress.Load()
	t.Cleanup(func() { crashInProgress.Store(prev) })
	crashInProgress.Store(false)
	if got := processExitCode(); got != 0 {
		t.Fatalf("a slow quit exits %d, want 0", got)
	}
	crashInProgress.Store(true)
	if got := processExitCode(); got != crashExitCode {
		t.Fatalf("a crash exits %d, want %d", got, crashExitCode)
	}
}
