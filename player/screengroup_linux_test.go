//go:build linux

package main

// screengroup_linux_test.go pins the behaviour the TrimUI relies on: a launch
// script that starts RetroArch and returns must keep the screen parked while
// the grandchild it left behind is still alive.

import (
	"os/exec"
	"testing"
	"time"
)

func TestScreenChildGroupTracksABackgroundedGrandchild(t *testing.T) {
	prev := runningGameChildren.Load()
	runningGameChildren.Store(0)
	t.Cleanup(func() { runningGameChildren.Store(prev) })

	// The shell exits immediately; the sleep it backgrounded stays in the
	// shell's process group for about a second.
	cmd := exec.Command("sh", "-c", "sleep 1 >/dev/null 2>&1 &")
	screenChildGroupConfigure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start the wrapper: %v", err)
	}
	noteGameChildStarted(cmd)
	_ = cmd.Wait()
	if !screenChildGroupAlive(cmd) {
		t.Fatal("the group was reported empty while the backgrounded emulator was still alive")
	}
	screenChildReaped(cmd)
	if gameScreenSuspended() {
		t.Fatal("the screen stayed parked after the group drained")
	}
	if screenChildGroupAlive(cmd) {
		t.Fatal("the group was still alive after the reaper returned, so a later launch would inherit a parked UI")
	}
}

func TestScreenChildKillStopsABackgroundedGrandchild(t *testing.T) {
	prev := runningGameChildren.Load()
	runningGameChildren.Store(0)
	t.Cleanup(func() { runningGameChildren.Store(prev) })

	cmd := exec.Command("sh", "-c", "sleep 30 >/dev/null 2>&1 &")
	screenChildGroupConfigure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start the wrapper: %v", err)
	}
	noteGameChildStarted(cmd)
	_ = cmd.Wait()
	if !screenChildGroupAlive(cmd) {
		t.Fatal("the grandchild was not in the wrapper's group, so a kill could not reach it")
	}
	if err := screenChildKill(cmd); err != nil {
		t.Fatalf("kill failed: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for screenChildGroupAlive(cmd) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if screenChildGroupAlive(cmd) {
		t.Fatal("a backgrounded emulator survived the kill; it would keep the framebuffer after JukaHub exits")
	}
	noteGameChildExited(cmd)
}
