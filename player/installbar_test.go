package main

import (
	"strings"
	"testing"
	"time"
)

func TestInstallBarDoneDetailCanOverrideRestartText(t *testing.T) {
	installBar.mu.Lock()
	oldMessage, oldPhase, oldEnded := installBar.message, installBar.phase, installBar.endedAt
	oldGen := installBar.gen
	installBar.mu.Unlock()
	defer func() {
		installBar.mu.Lock()
		installBar.message, installBar.phase, installBar.endedAt = oldMessage, oldPhase, oldEnded
		installBar.gen = oldGen
		installBar.mu.Unlock()
	}()

	installBar.mu.Lock()
	installBar.visible = true
	installBar.gen++
	installBar.mu.Unlock()
	gen := func() uint64 { installBar.mu.Lock(); defer installBar.mu.Unlock(); return installBar.gen }()
	installBarDoneRunDetail(gen, "Verified and cached — no restart needed")
	_, _, phase, detail, _, _, _, _, _ := installBarSnapshot()
	if phase != installPhaseDone || detail != "Verified and cached — no restart needed" {
		t.Fatalf("done card = %q / %q", phase, detail)
	}
}

func TestInstallBarPercentAndTimeHelpers(t *testing.T) {
	for _, tc := range []struct {
		done, total, percent int64
		want                 string
	}{
		{1, 1000, 0, "0.1%"},
		{50, 1000, 5, "5.0%"},
		{500, 1000, 50, "50%"},
		{1000, 1000, 100, "100%"},
		{0, 0, 0, ""},
	} {
		if got := formatInstallPercent(tc.done, tc.total, tc.percent); got != tc.want {
			t.Fatalf("percent(%d,%d,%d) = %q, want %q", tc.done, tc.total, tc.percent, got, tc.want)
		}
	}
	if got := installETA(200, 1000, 100); got != 8*time.Second {
		t.Fatalf("ETA = %v, want 8s", got)
	}
	if got := installETA(1000, 1000, 100); got != 0 {
		t.Fatalf("ETA after completion = %v", got)
	}
	if got := formatInstallETA(45 * time.Second); got != "45s left" {
		t.Fatalf("ETA text = %q", got)
	}
	if got := formatInstallETA(3 * time.Minute); got != "3m left" {
		t.Fatalf("ETA text = %q", got)
	}
	if got := formatElapsed(12 * time.Second); got != "12s elapsed" {
		t.Fatalf("elapsed = %q", got)
	}
	if got := formatElapsed(2 * time.Minute); got != "2m elapsed" {
		t.Fatalf("elapsed = %q", got)
	}
}

func TestInstallBarDetailCarriesRateAndETA(t *testing.T) {
	installBar.mu.Lock()
	oldVisible, oldTool, oldPhase := installBar.visible, installBar.tool, installBar.phase
	oldDone, oldTotal, oldSpeed := installBar.done, installBar.total, installBar.speed
	oldMessage, oldEnded := installBar.message, installBar.endedAt
	installBar.mu.Unlock()
	defer func() {
		installBar.mu.Lock()
		installBar.visible, installBar.tool, installBar.phase = oldVisible, oldTool, oldPhase
		installBar.done, installBar.total, installBar.speed = oldDone, oldTotal, oldSpeed
		installBar.message, installBar.endedAt = oldMessage, oldEnded
		installBar.mu.Unlock()
	}()

	installBar.mu.Lock()
	installBar.visible = true
	installBar.tool = "ffmpeg"
	installBar.phase = installPhaseDownload
	installBar.done, installBar.total = 200, 1000
	installBar.speed = 100
	installBar.message = ""
	installBar.endedAt = time.Time{}
	installBar.mu.Unlock()

	_, _, _, detail, _, _, _, _, _ := installBarSnapshot()
	for _, want := range []string{"200 B", "100 B/s", "8s left"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail = %q, want %q", detail, want)
		}
	}
}
