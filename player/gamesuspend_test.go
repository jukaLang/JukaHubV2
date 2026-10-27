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
//
// The registry half is what makes quitting safe on the handheld: an exit has
// to be able to stop the child that owns the framebuffer, otherwise the
// firmware is left on the game's last frame with no launcher behind it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGameScreenSuspensionCounter(t *testing.T) {
	t.Helper()
	if base := runningGameChildren.Load(); base != 0 {
		t.Fatalf("tests must not start with live screen-taking children, count %d", base)
	}
	if gameScreenSuspended() {
		t.Fatal("loop should not be parked at zero children")
	}

	noteGameChildStarted(nil)
	if !gameScreenSuspended() {
		t.Fatal("a live child must park the presentation loop")
	}

	// A second concurrent child (two games, or a game behind a timed
	// external app) keeps the park until both are gone.
	noteGameChildStarted(nil)
	noteGameChildExited(nil)
	if !gameScreenSuspended() {
		t.Fatal("exiting one of two children must not resume presentation")
	}

	noteGameChildExited(nil)
	if gameScreenSuspended() {
		t.Fatal("exiting the last child must resume presentation")
	}

	// A duplicate exit (a reaper racing a goroutine teardown) must clamp
	// instead of leaving a negative count that hides the next launch.
	noteGameChildExited(nil)
	if runningGameChildren.Load() != 0 {
		t.Fatalf("stale exit should clamp at zero, got %d", runningGameChildren.Load())
	}
	if gameScreenSuspended() {
		t.Fatal("stale exit must not leave the loop parked")
	}

	noteGameChildStarted(nil)
	noteGameChildExited(nil)
	if gameScreenSuspended() {
		t.Fatal("a launch after a stale exit must still round-trip")
	}
}

// TestExitGuardHelperProcess is the child used by the stop test below. It is a
// real process so the signal/kill/wait path is exercised end to end instead of
// mocked.
func TestExitGuardHelperProcess(t *testing.T) {
	if os.Getenv("JUKAHUB_EXITGUARD_HELPER") != "1" {
		t.Skip("helper process for TestStopScreenChildren")
	}
	time.Sleep(60 * time.Second)
}

// TestStopScreenChildrenStopsAndUnregisters is the "quit leaves no game behind"
// contract: a child recorded by a launch is reachable at exit, gets a quit
// request, and the reaper's note takes it out of the registry so the display
// handoff does not wait on it. The wait must be bounded even when the child
// ignores the request, hence the short grace and the hard kill inside.
func TestStopScreenChildrenStopsAndUnregisters(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a helper process")
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestExitGuardHelperProcess")
	cmd.Env = append(os.Environ(), "JUKAHUB_EXITGUARD_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	noteGameChildStarted(cmd)
	go func() {
		_ = cmd.Wait()
		noteGameChildExited(cmd)
	}()

	if live := liveScreenChildren(); len(live) != 1 || live[0] != cmd {
		t.Fatalf("a started child must be reachable at exit, registry has %d entries", len(live))
	}

	if stopped := stopScreenChildren(300 * time.Millisecond); stopped != 1 {
		t.Fatalf("stopScreenChildren reported %d stopped children, want 1", stopped)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(liveScreenChildren()) == 0 && !gameScreenSuspended() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child still registered after shutdown (registry %d, parked %v)",
		len(liveScreenChildren()), gameScreenSuspended())
}

// TestStopScreenChildrenWithoutChildrenIsFree keeps the common exit path (no
// game running) from waiting on the grace period: quitting JukaHub from the
// home screen must be immediate.
func TestStopScreenChildrenWithoutChildrenIsFree(t *testing.T) {
	if len(liveScreenChildren()) != 0 {
		t.Fatalf("an earlier test left a child process registered")
	}
	started := time.Now()
	if stopped := stopScreenChildren(2 * time.Second); stopped != 0 {
		t.Fatalf("stopScreenChildren reported %d stopped children, want 0", stopped)
	}
	if took := time.Since(started); took > 250*time.Millisecond {
		t.Fatalf("an exit with no child waited %s", took)
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
		// The command is passed so the same launch is reachable from the exit
		// path; a site that registers the counter without it would park the
		// loop correctly but leave the game running after JukaHub quits.
		if !strings.Contains(text, "noteGameChildStarted(cmd)") {
			t.Errorf("%s starts a screen-taking child but never calls noteGameChildStarted(cmd)", name)
		}
		if !strings.Contains(text, "noteGameChildExited(cmd)") {
			t.Errorf("%s reaps a screen-taking child but never calls noteGameChildExited(cmd)", name)
		}
	}
}
