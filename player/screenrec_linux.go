package main

// screenrec_linux.go — the recorder's process (screenrec.go): its own group,
// its priority and the signals that stop it.

import (
	"os/exec"
	"syscall"
)

// screenRecConfigure makes the recorder lead its own process group and has
// the kernel interrupt it if JukaHub dies first, so ffmpeg closes the file
// instead of recording the firmware's menu until its -t. It must run before
// cmd.Start, from the main thread: the death signal follows the thread that
// started the child, and the main thread lives as long as JukaHub.
//
// That is checked, not assumed. Go ends a thread whose goroutine exits while
// locked to it, and several of JukaHub's goroutines do (libindex_watch_linux.go,
// sdmove_check.go, usbmtp_linux.go): a pool thread that forked ffmpeg may later
// be the one such a goroutine locks, and its end would interrupt a recording
// that is still wanted. The main goroutine is locked to the main thread for
// the life of the process (go-sdl2's init), so only there (thread ID = process
// ID) is the signal asked for; started from anywhere else the recorder goes
// without it, and ffmpeg's -t and the Cleanup hook still end it.
func screenRecConfigure(cmd *exec.Cmd) {
	processGroupConfigure(cmd)
	if cmd == nil || cmd.SysProcAttr == nil {
		return
	}
	if syscall.Gettid() == syscall.Getpid() {
		cmd.SysProcAttr.Pdeathsig = syscall.SIGINT
		return
	}
	logInfo("screenrec", "started off the main thread: no death signal for ffmpeg (its -t still caps it)")
}

// screenRecLower sets nice screenRecNice on the recorder's process group:
// PRIO_PGRP reaches every thread it has, and threads it starts later inherit
// it, so the game always comes first. Best effort.
func screenRecLower(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Setpriority(syscall.PRIO_PGRP, cmd.Process.Pid, screenRecNice); err != nil {
		logInfo("screenrec", "nice %d: %v", screenRecNice, err)
	}
}

// screenRecSignal interrupts (kill false) or kills the recorder's group.
func screenRecSignal(cmd *exec.Cmd, kill bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	sig := syscall.SIGINT
	if kill {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}
