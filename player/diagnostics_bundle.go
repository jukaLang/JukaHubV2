package main

// diagnostics_bundle.go — the support export that includes the recent log.
//
// DiagnosticsText stays deliberately small: it is also pasted into a bug-report
// URL and encoded into a QR code, so a log tail would not fit. A bundle is the
// file a user copies off the card instead, so it can carry the log — but only
// after secrets and personal paths are removed, and with a header that says
// what was removed, because the user is about to share it.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// diagnosticsLogTailLines bounds the interesting part of a 2000-line ring
	// buffer: a crash or a failed launch is almost always in the last few
	// hundred lines, and a support file stays small enough to attach.
	diagnosticsLogTailLines = 400
	// diagnosticsLogTailMaxBytes bounds the same tail when single lines are
	// long (stack traces, command output).
	diagnosticsLogTailMaxBytes = 96 << 10
	// diagnosticsBundleMaxBytes is the whole file: report + header + tail.
	diagnosticsBundleMaxBytes = 160 << 10
)

var (
	// diagnosticSecret matches "key: value" pairs whose name says the value is
	// a credential. The value runs to whitespace, a comma or a semicolon, which
	// is how these appear in log lines: "token=abc123," or "api_key: sk-...".
	diagnosticSecret = regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|auth[_-]?token|authorization|token|secret|password|passwd)\b(\s*[:=]\s*)(?:(?:bearer|basic)\s+)?("[^"]*"|'[^']*'|[^\s,;]+)`)
	// diagnosticBearer catches the credential in "Authorization: Bearer <x>"
	// even when the header name was already scrubbed.
	diagnosticBearer = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}`)
	// diagnosticURLUser strips "user:password@" from a URL before it is shared.
	diagnosticURLUser = regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`)
	// diagnosticHomeUnix and diagnosticHomeWin remove the account name from a
	// home directory so a report does not publish who the device belongs to.
	diagnosticHomeUnix = regexp.MustCompile(`(?:/home/|/Users/)[^/\s]+`)
	diagnosticHomeWin  = regexp.MustCompile(`(?i)[A-Z]:\\Users\\[^\\\s]+`)
)

// redactDiagnosticText removes credentials and account names from text that is
// about to leave the device. It is applied to log lines only: the report itself
// is built from facts that never contain either.
//
// This is a safety net, not a guarantee. Log lines can still name games,
// folders, network names and addresses, which is why the bundle says so and the
// user is expected to read the file before sharing it.
func redactDiagnosticText(text string) string {
	text = diagnosticURLUser.ReplaceAllString(text, "://[credentials]@")
	// The named field runs before the bare bearer token: "Authorization: Bearer
	// <jwt>" must become one [redacted], not "[redacted] [redacted]".
	text = diagnosticSecret.ReplaceAllString(text, "${1}${2}[redacted]")
	text = diagnosticBearer.ReplaceAllString(text, "${1} [redacted]")
	text = diagnosticHomeUnix.ReplaceAllString(text, "~")
	text = diagnosticHomeWin.ReplaceAllString(text, `C:\Users\[user]`)
	return text
}

// diagnosticLogTail returns the last few hundred log lines, newest end kept
// when the byte cap bites, and redacted.
func diagnosticLogTail(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return ""
	}
	if len(lines) > diagnosticsLogTailLines {
		lines = lines[len(lines)-diagnosticsLogTailLines:]
	}
	tail := strings.Join(lines, "\n")
	if len(tail) > diagnosticsLogTailMaxBytes {
		// Keep the end: the newest lines carry the failure being reported.
		tail = tail[len(tail)-diagnosticsLogTailMaxBytes:]
		if cut := strings.IndexByte(tail, '\n'); cut >= 0 {
			tail = tail[cut+1:]
		}
	}
	return redactDiagnosticText(tail)
}

// cookieSessionFacts is the saved session's two support facts: when the
// session on the card began, and how long the retention policy keeps it. They
// are the numbers a cookie bug report turns on — "I was signed out after two
// weeks" is one date and one window away — and they are the same figures
// Settings → General shows, so the reporter recognises what they saw. The date
// carries the year and its age in days (the row on screen has no room for
// either, but a report read weeks later needs both), so a retention
// contradiction — a 30-day window on a 40-day session — is visible without a
// round trip. An empty jar reports no session rather than a zero date, and a
// stamp ahead of the device's clock says so instead of quoting an age that
// cannot be right: handhelds with a reset clock are a real support case.
func cookieSessionFacts(config *Config) string {
	var sb strings.Builder
	sb.WriteString("Saved cookies\n")
	sb.WriteString("-------------\n")
	if since, ok := browserSessionSince(); ok {
		line := "Session start: " + since.Local().Format("Jan 2 2006")
		// The labels are padded to the report's own 13-column field, the
		// width "Config health: " uses.
		age := time.Since(since)
		if age < 0 {
			line += " (stamped in the future — check the clock)"
		} else {
			line += fmt.Sprintf(" (%d days ago)", int(age.Hours()/24))
		}
		sb.WriteString(line + "\n")
	} else {
		sb.WriteString("Session start: none stored\n")
	}
	sb.WriteString("Retention    : " + cookieRetentionLabel(cookieRetentionDays(config)) + "\n")
	return sb.String()
}

// DiagnosticsBundleText is the support file: the redacted report, the cookie
// session's date and window, and the redacted log tail. A bundle with no
// usable log still says so instead of ending silently, because "the log was
// empty" is itself a support fact.
func DiagnosticsBundleText(config *Config) string {
	var sb strings.Builder
	sb.WriteString(DiagnosticsText(config))
	// The cookie facts join the bundle only: the short report has to fit a
	// bug-report URL, and these are the two numbers a login report needs
	// without another round trip.
	sb.WriteString("\n" + cookieSessionFacts(config))
	sb.WriteString("\nRecent log\n----------\n")
	tail := diagnosticLogTail(appLog.String())
	if strings.TrimSpace(tail) == "" {
		sb.WriteString("(the in-memory log is empty — the app may have just started)\n")
	} else {
		sb.WriteString(tail)
		if !strings.HasSuffix(tail, "\n") {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nRedaction: credentials (tokens, API keys, passwords, bearer headers,\n" +
		"URL userinfo) and home-directory account names were removed from the log.\n" +
		"Review this file before sharing it: log lines can still name games,\n" +
		"folders, network names and addresses.\n")
	out := sb.String()
	if len(out) > diagnosticsBundleMaxBytes {
		out = out[:diagnosticsBundleMaxBytes]
	}
	return out
}

// ExportDiagnosticsBundle writes the bundle into the Patch reports directory
// and returns its path. The file is 0600: it may be shared deliberately, but it
// is not readable by other accounts on the device.
func ExportDiagnosticsBundle(config *Config) (string, error) {
	dir, err := PatchStateDir()
	if err != nil {
		return "", fmt.Errorf("diagnostics bundle: state directory: %w", err)
	}
	name := "diagnostics-" + time.Now().UTC().Format("20060102T150405Z") + ".txt"
	path := filepath.Join(dir, "reports", name)
	if err := os.WriteFile(path, []byte(DiagnosticsBundleText(config)), 0o600); err != nil {
		return "", fmt.Errorf("diagnostics bundle: write %s: %w", name, err)
	}
	return path, nil
}
