package main

// skipaudit_test.go — keeps every skip in this suite an environment gate.
//
// A test that skips itself reports success to the suite while asserting
// nothing, so a skip where the *code* should have worked is a hole in the
// regression net that stays invisible: the previous audit found assert-shaped
// skips ("the header QR was not drawn" → skip), harness skips (a renderer that
// could not be created, which is how a whole render probe went unexecuted for
// months), and setup skips (a shipped font or config that failed to load).
//
// The fix for those is in the tests themselves — assert, or fail. This test is
// the part that keeps them fixed: every skip must carry a literal reason, and
// every reason must name a capability the machine genuinely lacks (no SDL
// video, no POSIX sh, no symlink privilege, no listener, no bundled tool, an
// opt-in switch, or the helper-process re-exec pattern). A new skip with any
// other reason fails here, which forces a decision instead of a silent hole.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// skipCallPattern matches a test or benchmark skipping itself. Its pieces are
// concatenated so this file does not match its own scan.
var skipCallPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])[tb]` + `\.Skip(?:f|Now)?\(`)

// skipReasonPattern captures the first string literal on the skip line, which
// is the reason for every call shape used here (`Skip("x")`, `Skipf("x", v)`).
var skipReasonPattern = regexp.MustCompile(`"([^"\\]*(?:\\.[^"\\]*)*)"`)

// skipGates are the only reasons a test in this suite may skip for. Each entry
// is matched as a substring of the reason text; the comment says which
// capability it stands for.
var skipGates = []string{
	// SDL's own subsystems: the machine has no video/ttf/image to draw with.
	"SDL video unavailable",
	"SDL_ttf unavailable",
	"SDL_image unavailable",

	// Developer opt-out for the heavy offscreen probes. CI never passes -short,
	// so these tests always run there.
	"-short mode",

	// Platform capabilities that genuinely differ between Windows and Linux.
	"symlink",
	"POSIX sh",
	"sh is not available",
	"executable bits are not meaningful on Windows",
	"the generated helper is a .bat on Windows",
	"Windows one-file builds unpack into the system TEMP folder",

	// Hosts configured for another runtime: the probe would fight the setup.
	"JUKAHUB_ANDROIDRT",
	"the native client runs on the TrimUI Linux build",
	"this machine has the handheld SD card mount",
	"this device manages startup apps itself",

	// Network: no listener, no route, or the loopback port is already taken.
	"no LAN address on this machine",
	"cannot listen",
	"cannot bind matching TCP port",
	"resolver unavailable",
	"unreachable address unexpectedly reachable",
	"environment reports network access",

	// External programs the probe shells out to, or cannot build a clip with.
	"ffmpeg",
	"cannot build a test clip",
	"system tar cannot extract this archive",

	// Opt-in probes that write previews or download a real release.
	"set JUKA_PREVIEW=1",
	"set JUKAHUB_TEST_UPDATE=1",
	// Opt-in visual audits that dump rendered scenes to a temp directory.
	"visual audit; run with JUKAHUB_UI_AUDIT=1",
	"visual audit; run with JUKAHUB_UI_AUDIT_COMPACT=1",

	// The helper-process pattern: the test re-execs itself, and the parent
	// instance is the one that skips.
	"spawns a helper process",
	"spawns ffmpeg",
	"helper process for ",

	// Host data that is not in the repository.
	"no Start Menu shortcut is available",
	"custom crypto key material in environment",
	"no discord token in the shipped config",
	"process listing unavailable here",
}

// skipReasonAllowed reports whether reason is one of the capability gates.
func skipReasonAllowed(reason string) bool {
	for _, gate := range skipGates {
		if strings.Contains(reason, gate) {
			return true
		}
	}
	return false
}

// TestSkipsAreEnvironmentGated is the audit, made permanent. Run it after
// touching any test that skips: the failure names the file, line and reason.
func TestSkipsAreEnvironmentGated(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob tests: %v", err)
	}
	if len(files) < 50 {
		// The suite is much larger than this; a short glob means the test is
		// running somewhere unexpected and the audit would check nothing.
		t.Fatalf("found only %d test files; the audit is running outside the package directory", len(files))
	}

	sites := 0
	for _, path := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !skipCallPattern.MatchString(line) {
				continue
			}
			sites++
			match := skipReasonPattern.FindStringSubmatch(line)
			if match == nil {
				t.Errorf("%s:%d: skip has no literal reason, so the audit cannot classify it: %s",
					path, i+1, strings.TrimSpace(line))
				continue
			}
			reason := match[1]
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s:%d: skip reason is empty", path, i+1)
				continue
			}
			if !skipReasonAllowed(reason) {
				t.Errorf("%s:%d: %q is not an environment gate.\n"+
					"Either the test should assert (or fail) instead of skipping, "+
					"or the machine really cannot run it — in which case add the "+
					"capability to skipGates in skipaudit_test.go and say why.",
					path, i+1, reason)
			}
		}
	}
	if sites < 30 {
		t.Fatalf("found only %d skip sites; the scanner missed the calls it exists to audit", sites)
	}
	t.Logf("audited %d skip sites across %d test files", sites, len(files))
}

// TestSkipAuditHasTeeth checks the audit against reasons it must reject and
// accept. Without this, a regex that stopped matching would let the audit pass
// while checking nothing.
func TestSkipAuditHasTeeth(t *testing.T) {
	for _, bad := range []string{
		"no font available",
		"header QR was not drawn",
		"renderer unavailable: %v",
		"another test left a child registered",
	} {
		if skipReasonAllowed(bad) {
			t.Errorf("%q must not be an allowed skip reason", bad)
		}
	}
	for _, good := range []string{
		"SDL video unavailable: no display",
		"render probe skipped in -short mode",
		"cannot create symlink: privilege not held",
		"ffmpeg is not installed on this machine",
		"helper process for TestThing",
	} {
		if !skipReasonAllowed(good) {
			t.Errorf("%q should be an allowed environment gate", good)
		}
	}
	if !skipCallPattern.MatchString("\tt.Skip(\"x\")") {
		t.Error("the skip-call scanner no longer matches a plain skip")
	}
	if !skipCallPattern.MatchString("\t\tb.Skipf(\"x: %v\", err)") {
		t.Error("the skip-call scanner no longer matches a benchmark skipf")
	}
}
