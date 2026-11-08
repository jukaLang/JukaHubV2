//go:build linux || darwin || freebsd || netbsd || openbsd

package main

// alarm_proc_unix.go — the POSIX half of an alarm ringing over a game.
//
// A game and JukaHub share one framebuffer on the handheld, so the alarm cannot
// be drawn over a game that keeps presenting frames: the panel would flicker
// between the two. The ring pauses the game instead (SIGSTOP to its process
// group, the same group the exit path signals), draws over its last frame, and
// lets it carry on (SIGCONT) the moment the alarm is snoozed or dismissed.

import (
	"os/exec"
	"syscall"
)

// alarmSignalChild pauses (stop) or resumes a screen-taking child and, when it
// leads its own process group, everything it started.
func alarmSignalChild(cmd *exec.Cmd, stop bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	sig := syscall.SIGCONT
	if stop {
		sig = syscall.SIGSTOP
	}
	if screenChildGroupConfigured(cmd) {
		if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil {
			return nil
		}
	}
	return cmd.Process.Signal(sig)
}

// alarmCanPauseChildren reports whether a running game can be paused under the
// alarm on this platform.
func alarmCanPauseChildren() bool { return true }

// alarmHideConsole is a no-op off Windows: a decoder has no window here.
func alarmHideConsole(cmd *exec.Cmd) {}
