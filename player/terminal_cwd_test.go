package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTerminalState gives a test its own terminal session.
func withTerminalState(t *testing.T, cwd string) {
	t.Helper()
	prev := term
	prevHistory := termHistoryPathFn
	history := filepath.Join(t.TempDir(), "history")
	termHistoryPathFn = func() string { return history }
	term = terminalState{cwd: cwd, histIdx: -1}
	t.Cleanup(func() {
		if term.job != nil {
			term.job.cancel()
		}
		term = prev
		termHistoryPathFn = prevHistory
	})
}

func termLastLines(n int) string {
	from := len(term.lines) - n
	if from < 0 {
		from = 0
	}
	var out []string
	for _, l := range term.lines[from:] {
		out = append(out, l.text)
	}
	return strings.Join(out, "\n")
}

// `cd` moves the terminal, not the app: with os.Chdir every relative path the
// app writes (jukauser.json, alarms, notes) went to the user's folder instead.
func TestTerminalCdKeepsTheAppWorkingDirectory(t *testing.T) {
	start := t.TempDir()
	sub := filepath.Join(start, "games")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "readme.txt"), []byte("hello from games"), 0o644); err != nil {
		t.Fatal(err)
	}
	withTerminalState(t, start)
	appWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	termExecute("cd games")
	if term.cwd != sub {
		t.Fatalf("terminal directory = %q, want %q", term.cwd, sub)
	}
	if wd, _ := os.Getwd(); wd != appWD {
		t.Fatalf("cd moved the app's own working directory to %q", wd)
	}
	termExecute("pwd")
	if got := termLastLines(1); got != sub {
		t.Fatalf("pwd printed %q, want %q", got, sub)
	}
	termExecute("cat readme.txt")
	if got := termLastLines(1); got != "hello from games" {
		t.Fatalf("cat read %q relative to the wrong directory", got)
	}
	termExecute("cd readme.txt")
	if term.cwd != sub || !strings.Contains(termLastLines(1), "not a directory") {
		t.Fatalf("cd into a file moved the terminal (%q) or said nothing (%q)", term.cwd, termLastLines(1))
	}
}

// External commands run off the event thread: the call returns at once and the
// output arrives through termDrainJob.
func TestTerminalExternalCommandRunsInTheBackground(t *testing.T) {
	bin, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("no printf on this host")
	}
	_ = bin
	withTerminalState(t, t.TempDir())
	termExecute("printf hello-from-job")
	if term.job == nil {
		t.Fatal("the command did not start as a background job")
	}
	deadline := time.Now().Add(5 * time.Second)
	for term.job != nil && time.Now().Before(deadline) {
		termDrainJob()
		time.Sleep(10 * time.Millisecond)
	}
	if term.job != nil {
		t.Fatal("the job never finished")
	}
	// The echoed command lines contain the text too; only the last line is
	// the command's own output.
	if got := termLastLines(1); got != "hello-from-job" {
		t.Fatalf("last line = %q, want the command's output", got)
	}
}
