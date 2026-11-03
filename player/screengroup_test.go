package main

// Tests for the screen hand-off around a launcher. On Windows the group check
// is a documented no-op, so what is asserted there is the settle and the
// counter; the POSIX group behaviour is pinned in screengroup_linux_test.go.

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func waitCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "exit", "0")
	}
	return exec.Command("sh", "-c", "exit 0")
}

func TestScreenChildGroupConfigureMatchesThePlatform(t *testing.T) {
	cmd := waitCommand()
	screenChildGroupConfigure(cmd)
	configured := screenChildGroupConfigured(cmd)
	if runtime.GOOS == "windows" {
		if configured || cmd.SysProcAttr != nil {
			t.Fatal("Windows has no POSIX process group, but the child was configured as if it did")
		}
		return
	}
	if !configured {
		t.Fatal("a POSIX child was not put in its own process group, so a wrapper that backgrounds the emulator would hand the screen back early")
	}
}

func TestScreenChildReapedHoldsTheScreenThroughTheSettle(t *testing.T) {
	prev := runningGameChildren.Load()
	runningGameChildren.Store(0)
	t.Cleanup(func() { runningGameChildren.Store(prev) })

	cmd := waitCommand()
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start the helper: %v", err)
	}
	noteGameChildStarted(cmd)
	if !gameScreenSuspended() {
		t.Fatal("a started screen child did not park the UI")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		screenChildReaped(cmd)
		close(done)
	}()

	// During the settle the screen must still be parked: drawing here is what
	// flashed the JukaHub UI over the firmware's restore.
	time.Sleep(screenSettleDelay / 2)
	if !gameScreenSuspended() {
		t.Fatal("the screen was handed back before the settle finished")
	}
	select {
	case <-done:
	case <-time.After(screenSettleDelay * 4):
		t.Fatal("the reaper never released the screen")
	}
	if gameScreenSuspended() {
		t.Fatal("the screen stayed parked after the child was reaped")
	}
}

func TestScreenChildReapedToleratesNoChild(t *testing.T) {
	prev := runningGameChildren.Load()
	runningGameChildren.Store(0)
	t.Cleanup(func() { runningGameChildren.Store(prev) })
	// A launch that failed before Start still reaches the reaper on some paths;
	// it must not block on a nil process or decrement below zero.
	screenChildReaped(nil)
	if runningGameChildren.Load() != 0 {
		t.Fatalf("counter = %d, want it clamped at zero for a nil child", runningGameChildren.Load())
	}
}

func sleeperCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "timeout", "/t", "30")
	}
	return exec.Command("sleep", "30")
}

func TestScreenChildSignalToleratesNoChild(t *testing.T) {
	if err := screenChildSignal(nil, screenSignalInterrupt); err != nil {
		t.Fatalf("interrupt on a nil child returned %v", err)
	}
	if err := screenChildKill(nil); err != nil {
		t.Fatalf("kill on a nil child returned %v", err)
	}
	// A command that never started has no process; the exit path still reaches
	// it, because a failed launch is registered before Start is attempted on
	// some routes.
	if err := screenChildKill(exec.Command("juka-command-that-does-not-exist")); err != nil {
		t.Fatalf("kill on an unstarted child returned %v", err)
	}
}

func TestScreenChildKillStopsTheProcess(t *testing.T) {
	cmd := sleeperCommand()
	screenChildGroupConfigure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start the sleeper: %v", err)
	}
	if err := screenChildKill(cmd); err != nil {
		t.Fatalf("kill failed: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the process was still alive five seconds after a kill")
	}
}
