package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixture is the graphics sequence in the Smart Pro's 2026-11-26
// RetroArch log for the legal GameCube Hardware Test homebrew DOL.
func TestDolphinGraphicsDiagnosisUsesAcceptedContext(t *testing.T) {
	path := filepath.Join("testdata", "dolphin-gles-probe.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if issue, ok := gameDiagGPULogIssue(lines); ok {
		t.Fatalf("accepted ES 3.0 misdiagnosed as desktop OpenGL: %+v", issue)
	}
	const realFailure = "[libretro WARN] 10:08:566 DolphinLibretro/Boot.cpp:340 W[COMMON]: Suppressed popup: Warning - GPU: OGL ERROR: Does your video card support OpenGL 2.0?"
	if got := dolphinVideoFailureIn(path); got != realFailure {
		t.Fatalf("Dolphin failure = %q, want the error after ES 3.0 was chosen", got)
	}
	selected := -1
	for i, line := range lines {
		if dolphinVideoSelectedContextLine(line) {
			selected = i
			break
		}
	}
	if selected < 0 {
		t.Fatal("fixture has no accepted context")
	}
	if got := dolphinVideoFailureLines(lines[:selected+1]); got != "" {
		t.Fatalf("rejected candidates before ES 3.0 counted as a failure: %q", got)
	}
	if issue, ok := gameDiagGPULogIssue(lines[:selected+1]); ok {
		t.Fatalf("rejected candidates before ES 3.0 were diagnosed after acceptance: %+v", issue)
	}
	if got := dolphinVideoFailureLines(lines[:selected]); got == "" {
		t.Fatal("a start without any accepted context lost its failure")
	}
	if _, ok := gameDiagGPULogIssue(lines[:selected]); !ok {
		t.Fatal("a start without any accepted context lost its GPU diagnosis")
	}
}
