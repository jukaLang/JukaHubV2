package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// diagnostics_bundle_test.go pins the support export: it has to carry enough to
// triage a failure (the log tail) while scrubbing what must never leave the
// device (credentials, account names).

func TestRedactDiagnosticTextRemovesCredentials(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"token assignment", "discord token=abc123, retrying", "discord token=[redacted], retrying"},
		{"api key with colon", "groq api_key: sk-live-8891", "groq api_key: [redacted]"},
		{"password field", `login password="hunter2" failed`, "login password=[redacted] failed"},
		{"bearer header", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9", "Authorization: [redacted]"},
		{"bare bearer", "sent bearer abcdefghijkl", "sent bearer [redacted]"},
		{"url userinfo", "GET https://media:secret@example.org/a.mp4", "GET https://[credentials]@example.org/a.mp4"},
		{"unix home", "open /home/stepan/.config/jukauser.json", "open ~/.config/jukauser.json"},
		{"mac home", "open /Users/alice/Library/x", "open ~/Library/x"},
		{"windows home", `open C:\Users\stepan\AppData\Roaming\x`, `open C:\Users\[user]\AppData\Roaming\x`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactDiagnosticText(tt.in); got != tt.want {
				t.Errorf("redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedactDiagnosticTextLeavesDiagnosticsAlone(t *testing.T) {
	// Ordinary support lines must survive untouched, or the tail stops being
	// useful exactly when it matters.
	for _, line := range []string{
		"[netplay] listening on port 55435",
		"[startup] splash 750ms, first frame 1.9s",
		"error: could not start RetroArch: executable file not found",
		"[wifi] wlan0 obtained 192.168.1.24",
	} {
		if got := redactDiagnosticText(line); got != line {
			t.Errorf("redact(%q) = %q, want it unchanged", line, got)
		}
	}
}

func TestDiagnosticLogTailKeepsTheNewestLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < diagnosticsLogTailLines+50; i++ {
		b.WriteString("line " + itoa(i) + "\n")
	}
	tail := diagnosticLogTail(b.String())
	lines := strings.Split(tail, "\n")
	if len(lines) != diagnosticsLogTailLines {
		t.Fatalf("tail has %d lines, want %d", len(lines), diagnosticsLogTailLines)
	}
	if !strings.Contains(lines[len(lines)-1], "line "+itoa(diagnosticsLogTailLines+49)) {
		t.Errorf("tail does not end with the newest line: %q", lines[len(lines)-1])
	}
	if strings.Contains(tail, "line 0\n") {
		t.Error("tail kept the oldest line instead of trimming it")
	}
}

func TestDiagnosticLogTailCapsLongLinesAtTheNewestEnd(t *testing.T) {
	long := strings.Repeat("x", diagnosticsLogTailMaxBytes)
	tail := diagnosticLogTail("old\n" + long)
	if len(tail) > diagnosticsLogTailMaxBytes {
		t.Errorf("tail is %d bytes, cap is %d", len(tail), diagnosticsLogTailMaxBytes)
	}
	if strings.HasPrefix(tail, "old") {
		t.Error("the byte cap kept the old line; the newest end should win")
	}
}

func TestDiagnosticsBundleCarriesReportAndRedactedLog(t *testing.T) {
	withTempPatchState(t)
	appLog.Clear()
	t.Cleanup(appLog.Clear)
	_, _ = appLog.Write([]byte("boot ok\n"))
	_, _ = appLog.Write([]byte("discord token=abc123 failed\n"))
	_, _ = appLog.Write([]byte("open /home/stepan/games/x\n"))

	bundle := DiagnosticsBundleText(&Config{Version: "9.9.9"})
	if !strings.Contains(bundle, "JukaHub diagnostics") {
		t.Fatalf("bundle does not start with the report:\n%s", bundle)
	}
	if !strings.Contains(bundle, "Version      : v9.9.9") {
		t.Errorf("bundle lost the version line:\n%s", bundle)
	}
	if !strings.Contains(bundle, "Recent log") || !strings.Contains(bundle, "boot ok") {
		t.Fatalf("bundle has no log section:\n%s", bundle)
	}
	for _, leaked := range []string{"abc123", "/home/stepan"} {
		if strings.Contains(bundle, leaked) {
			t.Errorf("bundle leaked %q:\n%s", leaked, bundle)
		}
	}
	if !strings.Contains(bundle, "Review this file before sharing it") {
		t.Error("bundle does not tell the user to review it before sharing")
	}
}

func TestDiagnosticsBundleSaysWhenTheLogIsEmpty(t *testing.T) {
	withTempPatchState(t)
	appLog.Clear()
	t.Cleanup(appLog.Clear)
	bundle := DiagnosticsBundleText(nil)
	if !strings.Contains(bundle, "the in-memory log is empty") {
		t.Fatalf("empty log is not explained:\n%s", bundle)
	}
}

// TestOpExportDiagnosticsWritesTheBundle is the button path: the Patch action
// must produce the bundle (report + log), not the short report it wrote before.
func TestOpExportDiagnosticsWritesTheBundle(t *testing.T) {
	dir := withTempPatchState(t)
	appLog.Clear()
	t.Cleanup(appLog.Clear)
	_, _ = appLog.Write([]byte("launch failed: executable file not found\n"))

	if err := OpExportDiagnostics(context.Background(), &Config{Version: "2.0.0"}); err != nil {
		t.Fatalf("OpExportDiagnostics: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "reports"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no diagnostics report was written: %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(dir, "reports", entries[len(entries)-1].Name()))
	if readErr != nil {
		t.Fatalf("read report: %v", readErr)
	}
	text := string(data)
	if !strings.Contains(text, "JukaHub diagnostics") || !strings.Contains(text, "Recent log") {
		t.Errorf("the exported file is not the bundle:\n%s", text)
	}
	if !strings.Contains(text, "launch failed: executable file not found") {
		t.Errorf("the exported file lost the log tail:\n%s", text)
	}
}

func TestExportDiagnosticsBundleWritesIntoReports(t *testing.T) {
	dir := withTempPatchState(t)
	appLog.Clear()
	t.Cleanup(appLog.Clear)
	_, _ = appLog.Write([]byte("hello from the log\n"))

	path, err := ExportDiagnosticsBundle(&Config{Version: "1.0.0"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if want := filepath.Join(dir, "reports"); !strings.HasPrefix(path, want) {
		t.Errorf("bundle written to %s, want a file under %s", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if !strings.Contains(string(data), "hello from the log") || !strings.Contains(string(data), "JukaHub diagnostics") {
		t.Errorf("bundle content is not report + log:\n%s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat bundle: %v", err)
	}
	// Windows ignores Unix permission bits on create, so the check only means
	// something on the device and on Linux/macOS hosts.
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("bundle mode = %o, want 600 (it is shared deliberately, not world-readable)", mode)
		}
	}
}

// TestDiagnosticsBundleCarriesTheCookieSession pins the two cookie facts a bug
// report needs: when the session on the card began — the oldest stamp still
// there, with the year and its age in days so a retention contradiction is
// visible at a glance — and the window that prunes it. Both are the figures
// Settings → General shows, so the reporter recognises what they saw, and the
// section sits with the report, before the log a maintainer scrolls to.
func TestDiagnosticsBundleCarriesTheCookieSession(t *testing.T) {
	withTempPatchState(t)
	path := useTempBrowserCookiePath(t)
	prevConfig := appConfig
	appConfig = retentionConfig(0) // keep everything while the file loads
	t.Cleanup(func() { appConfig = prevConfig })

	writeCookieFile(t, path, []browserCookieRecord{
		{Host: "example.com", Path: "/", Name: "session", Value: "a", FirstSeen: ageInDays(40)},
	})
	simulateBrowserRelaunch()
	since, ok := browserSessionSince()
	if !ok {
		t.Fatal("the jar holds a stamped cookie but reports no session start")
	}

	bundle := DiagnosticsBundleText(retentionConfig(30))
	want := "Session start: " + since.Local().Format("Jan 2 2006") + " (40 days ago)"
	if !strings.Contains(bundle, want) {
		t.Errorf("bundle lacks %q:\n%s", want, bundle)
	}
	if !strings.Contains(bundle, "Retention    : 30 days") {
		t.Errorf("bundle lacks the retention window:\n%s", bundle)
	}
	if i, j := strings.Index(bundle, "Saved cookies"), strings.Index(bundle, "Recent log"); i < 0 || j < 0 || i > j {
		t.Errorf("the cookie section is not between the report and the log (i=%d, j=%d)", i, j)
	}
}

// TestDiagnosticsBundleReportsNoSessionHonestly: a card with no cookies says
// so instead of quoting a zero date, and the shipped window spells itself the
// way Settings spells it, so the report cannot be read as a policy that was
// never chosen.
func TestDiagnosticsBundleReportsNoSessionHonestly(t *testing.T) {
	withTempPatchState(t)
	useTempBrowserCookiePath(t)

	bundle := DiagnosticsBundleText(nil)
	if !strings.Contains(bundle, "Session start: none stored") {
		t.Errorf("the empty jar's bundle lacks its line:\n%s", bundle)
	}
	if !strings.Contains(bundle, "Retention    : Keep") {
		t.Errorf("bundle does not spell the shipped window as Keep:\n%s", bundle)
	}
}
