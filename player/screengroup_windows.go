//go:build windows

package main

// screengroup_windows.go — Windows has no POSIX process groups, and the desktop
// launchers (shortcuts, .bat wrappers, emulator executables) already keep the
// process alive for as long as the window is open, so the reaper's group check
// is a no-op here.

import (
	"os"
	"os/exec"
)

// screenChildGroupConfigure leaves the environment alone too: a desktop
// emulator gets the one JukaHub was started with (childenv.go is about the
// handheld's launch.sh).
func screenChildGroupConfigure(cmd *exec.Cmd) {}

func processGroupConfigure(cmd *exec.Cmd) {}

func screenChildGroupConfigured(cmd *exec.Cmd) bool { return false }

// screenSignal mirrors the POSIX set; Windows only has interrupt and kill.
type screenSignal int

const (
	screenSignalInterrupt screenSignal = iota
	screenSignalKill
)

// screenChildSignal has no group to reach on Windows: the emulator's own
// process is the one to stop.
func screenChildSignal(cmd *exec.Cmd, kind screenSignal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// A game JukaHub stops did not fail to start (gamelaunch_watch.go).
	gameLaunchNoteStop(cmd)
	if kind == screenSignalKill {
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(os.Interrupt)
}

func screenChildKill(cmd *exec.Cmd) error { return screenChildSignal(cmd, screenSignalKill) }

func screenChildGroupAlive(cmd *exec.Cmd) bool { return false }
