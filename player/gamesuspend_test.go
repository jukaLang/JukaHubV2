package main

// gamesuspend_test.go pins the launch-blink fix. The bug it prevents: a
// JukaHub frame presented while a game/emulator draws to the shared handheld
// framebuffer makes the panel blink between the UI and the game. The fix has
// two halves and the tests guard both:
//
//   - the counter contract: starts/exits bracket a live child, and a stale or
//     duplicated exit never flips the count negative (which would park the
//     main loop forever, a full display freeze);
//   - the wiring: every launch site that starts a screen-taking child pairs
//     noteGameChildStarted with noteGameChildExited, so blinking (or a
//     permanently parked loop) cannot return through a new or edited launcher.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGameScreenSuspensionCounter(t *testing.T) {
	t.Helper()
	if base := runningGameChildren.Load(); base != 0 {
		t.Fatalf("tests must not start with live screen-taking children, count %d", base)
	}
	if gameScreenSuspended() {
		t.Fatal("loop should not be parked at zero children")
	}

	noteGameChildStarted()
	if !gameScreenSuspended() {
		t.Fatal("a live child must park the presentation loop")
	}

	// A second concurrent child (two games, or a game behind a timed
	// external app) keeps the park until both are gone.
	noteGameChildStarted()
	noteGameChildExited()
	if !gameScreenSuspended() {
		t.Fatal("exiting one of two children must not resume presentation")
	}

	noteGameChildExited()
	if gameScreenSuspended() {
		t.Fatal("exiting the last child must resume presentation")
	}

	// A duplicate exit (a reaper racing a goroutine teardown) must clamp
	// instead of leaving a negative count that hides the next launch.
	noteGameChildExited()
	if runningGameChildren.Load() != 0 {
		t.Fatalf("stale exit should clamp at zero, got %d", runningGameChildren.Load())
	}
	if gameScreenSuspended() {
		t.Fatal("stale exit must not leave the loop parked")
	}

	noteGameChildStarted()
	noteGameChildExited()
	if gameScreenSuspended() {
		t.Fatal("a launch after a stale exit must still round-trip")
	}
}

// screenTakingLaunchSites are the launch paths that hand the display to a
// child and must therefore suspend JukaHub's presentation for the child's
// life. A site missing here re-introduces the launch blink on the handheld;
// one missing a marker risks the park never lifting.
func TestEveryScreenTakingLaunchSuspendsPresentation(t *testing.T) {
	t.Helper()
	sites := []string{
		"emulators.go",
		"gameprofiles.go",
		"windowsapps.go",
		"game_library.go",
		"netplay.go",
		"ports.go",
		"apps.go",
	}
	for _, name := range sites {
		body, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(body)
		if !strings.Contains(text, "noteGameChildStarted()") {
			t.Errorf("%s starts a screen-taking child but never calls noteGameChildStarted", name)
		}
		if !strings.Contains(text, "noteGameChildExited()") {
			t.Errorf("%s reaps a screen-taking child but never calls noteGameChildExited", name)
		}
	}
}
