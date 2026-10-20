package main

// Tests for persistent terminal history and completion. Both are pure or
// file-backed, so the behaviour that matters — what survives a restart and what
// Tab inserts — is verified without a screen.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTermHistory(t *testing.T) {
	in := "ls\n\n  pwd  \ncd roms\r\ncd roms\nsysinfo\n"
	got := parseTermHistory(in)
	want := []string{"ls", "pwd", "cd roms", "sysinfo"}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsed %v, want %v", got, want)
		}
	}

	if got := parseTermHistory(""); len(got) != 0 {
		t.Fatalf("empty file parsed as %v", got)
	}
}

func TestParseTermHistoryCapsToNewest(t *testing.T) {
	var lines []string
	for i := 0; i < termHistoryMax+20; i++ {
		lines = append(lines, "echo "+string(rune('a'+i%26))+" "+string(rune('0'+i%10))+" "+string(rune('0'+i/10)))
	}
	got := parseTermHistory(strings.Join(lines, "\n"))
	if len(got) != termHistoryMax {
		t.Fatalf("kept %d entries, want the cap of %d", len(got), termHistoryMax)
	}
	if got[len(got)-1] != lines[len(lines)-1] {
		t.Fatalf("newest command was dropped: %q", got[len(got)-1])
	}
}

func TestTermHistoryRoundTrip(t *testing.T) {
	prevPath := termHistoryPathFn
	prevTerm := term
	dir := t.TempDir()
	termHistoryPathFn = func() string { return filepath.Join(dir, "terminal_history") }
	term = terminalState{}
	t.Cleanup(func() {
		termHistoryPathFn = prevPath
		term = prevTerm
	})

	termRecordHistory("ls")
	termRecordHistory("ls") // consecutive duplicate is not stored twice
	termRecordHistory("cd roms")
	termRecordHistory("   ")

	if len(term.history) != 2 {
		t.Fatalf("in-memory history = %v, want 2 entries", term.history)
	}

	path := termHistoryPathFn()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("history file was not written: %v", err)
	}
	if string(data) != "ls\ncd roms\n" {
		t.Fatalf("history file = %q", data)
	}

	// A fresh session reads the same commands back, newest last.
	term.history = nil
	if got := termLoadHistory(); len(got) != 2 || got[0] != "ls" || got[1] != "cd roms" {
		t.Fatalf("reloaded history = %v", got)
	}
}

func TestTermLoadHistoryMissingFile(t *testing.T) {
	prevPath := termHistoryPathFn
	termHistoryPathFn = func() string { return filepath.Join(t.TempDir(), "nope", "terminal_history") }
	t.Cleanup(func() { termHistoryPathFn = prevPath })

	if got := termLoadHistory(); len(got) != 0 {
		t.Fatalf("missing history file returned %v", got)
	}
}

func TestTermCompletions(t *testing.T) {
	files := []string{"saves/", "savestate.bin", "roms/", "readme.txt"}
	history := []string{"ssh on", "ls", "neofetch"}

	tests := []struct {
		name        string
		input       string
		wantInput   string
		wantMatches int
	}{
		{"unique command", "who", "whoami ", 1},
		{"unique command is case-insensitive", "Who", "whoami ", 1},
		{"short prefix completes when unique", "cl", "clear ", 1},
		{"ambiguous command with nothing shared stays put", "c", "c", 4}, // calc, cat, cd, clear
		{"history command completes", "neo", "neofetch ", 1},
		{"ambiguous file argument extends to the shared prefix", "cat sav", "cat saves", 2},
		{"file argument with one match", "cat read", "cat readme.txt", 1},
		{"trailing space with no prefix lists the directory", "cat ", "cat ", 4},
		{"no match leaves the line alone", "zzz", "zzz", 0},
	}
	for _, tc := range tests {
		got, matches := termCompletions(tc.input, files, history)
		if got != tc.wantInput {
			t.Errorf("%s: completed %q, want %q", tc.name, got, tc.wantInput)
		}
		if len(matches) != tc.wantMatches {
			t.Errorf("%s: %d matches (%v), want %d", tc.name, len(matches), matches, tc.wantMatches)
		}
	}
}

func TestTermCompletionDoesNotDuplicateHistory(t *testing.T) {
	// A command in the history that is also built in must appear once.
	_, matches := termCompletions("cl", nil, []string{"clear", "clear"})
	seen := map[string]int{}
	for _, m := range matches {
		seen[m]++
	}
	if seen["clear"] != 1 {
		t.Fatalf("clear appeared %d times in %v", seen["clear"], matches)
	}
}
