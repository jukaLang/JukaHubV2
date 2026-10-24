package main

// Tests for the log export rotation and the byte-capped in-memory ring.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapLogDataKeepsNewestWholeLines(t *testing.T) {
	data := []byte("first\nsecond\nthird\n")
	cases := []struct {
		name     string
		maxBytes int64
		want     string
	}{
		{"no cap", 0, string(data)},
		{"room for everything", 100, string(data)},
		{"drops the oldest lines", 12, "third\n"},
		{"fits exactly", 6, "third\n"},
		{"keeps the last bytes", 4, "ird\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(capLogData(data, tc.maxBytes)); got != tc.want {
				t.Fatalf("capLogData(..., %d) = %q, want %q", tc.maxBytes, got, tc.want)
			}
		})
	}

	// No newline at all: the cap still applies.
	if got := string(capLogData([]byte("abcdefghij"), 4)); got != "ghij" {
		t.Fatalf("no-newline cap = %q, want %q", got, "ghij")
	}
}

func TestWriteRotatedLogKeepsOneBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.txt")

	if err := writeRotatedLog(path, []byte("first run\n"), 1<<20); err != nil {
		t.Fatalf("first export: %v", err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("the first export should not create a backup")
	}

	if err := writeRotatedLog(path, []byte("second run\n"), 1<<20); err != nil {
		t.Fatalf("second export: %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "second run\n" {
		t.Fatalf("current log = %q, want the newest export", current)
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != "first run\n" {
		t.Fatalf("backup = %q, want the previous export", backup)
	}

	// A third export replaces the one backup instead of growing a chain.
	if err := writeRotatedLog(path, []byte("third run\n"), 1<<20); err != nil {
		t.Fatalf("third export: %v", err)
	}
	backup, _ = os.ReadFile(path + ".1")
	if string(backup) != "second run\n" {
		t.Fatalf("after the third export, backup = %q", backup)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatal("only one backup should be kept")
	}
}

func TestWriteRotatedLogCapsAndSkipsEmptyBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeRotatedLog(path, []byte("first\nsecond\nthird\n"), 7); err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("an empty previous file is not worth a backup")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "third\n" {
		t.Fatalf("capped export = %q, want the newest whole line", got)
	}
}

func TestLogBufferCapsBytesAsWellAsLines(t *testing.T) {
	b := &logBuffer{max: 100, maxBytes: 40}
	for _, line := range []string{
		"aaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccc",
	} {
		b.Write([]byte(line + "\n"))
	}
	got := b.String()
	if len(got) > 40 {
		t.Fatalf("ring holds %d bytes, want <= 40", len(got))
	}
	if !strings.Contains(got, "cccc") || strings.Contains(got, "aaaa") {
		t.Fatalf("ring = %q, want only the newest line", got)
	}

	// A single line larger than the byte budget keeps its tail rather than
	// every earlier line.
	b = &logBuffer{max: 10, maxBytes: 10}
	b.Write([]byte("0123456789ABCDEF\n"))
	if got := b.String(); got != "6789ABCDEF" {
		t.Fatalf("overlong line = %q, want its tail", got)
	}

	// The line cap still applies on its own.
	b = &logBuffer{max: 2}
	for _, line := range []string{"a", "b", "c"} {
		b.Write([]byte(line + "\n"))
	}
	if got := b.String(); got != "b\nc" {
		t.Fatalf("line-capped ring = %q, want the two newest lines", got)
	}
	b.Clear()
	if got := b.String(); got != "" {
		t.Fatalf("Clear left %q", got)
	}
}
