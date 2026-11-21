//go:build linux

package main

// tailscaled_linux.go — the handheld side of the built-in Tailscale
// (tailscaled.go): where the control socket goes, the daemon's priority, and
// clearing a daemon an earlier JukaHub left running.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// tailscaledSocketPath is the daemon's control socket. In the temp folder
// (tmpfs on the handheld), not the data folder: the card is FAT/exFAT, which
// cannot hold a socket.
func tailscaledSocketPath() string {
	return filepath.Join(os.TempDir(), "jukahub-tailscaled.sock")
}

// tailscaledSystemCLI is a computer's Tailscale outside PATH: none on Linux.
func tailscaledSystemCLI() string { return "" }

// tailscaledLowerPriority sets nice 10 on the daemon's process group (its
// own, syncthingProcAttr): below the UI and games, above Syncthing's 19.
// Best effort.
func tailscaledLowerPriority(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Setpriority(syscall.PRIO_PGRP, cmd.Process.Pid, 10); err != nil {
		logInfo("tailscale", "nice 10: %v", err)
	}
}

// tailscaledKillStale ends a tailscaled an earlier JukaHub started with this
// state folder. The pid file names it, and its command line must name the
// same state folder too, so a pid the system reused is never touched.
func tailscaledKillStale(pidFile, home string) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 || pid == os.Getpid() {
		_ = os.Remove(pidFile)
		return
	}
	proc := "/proc/" + strconv.Itoa(pid)
	cmdline, err := os.ReadFile(proc + "/cmdline")
	if err != nil || !bytes.Contains(cmdline, []byte("tailscaled")) ||
		!bytes.Contains(cmdline, []byte("--statedir="+home+"\x00")) {
		_ = os.Remove(pidFile)
		return
	}
	logInfo("tailscale", "stopping tailscaled pid %d left by an earlier run", pid)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	gone := false
	for i := 0; i < 30 && !gone; i++ {
		time.Sleep(100 * time.Millisecond)
		_, err := os.Stat(proc)
		gone = err != nil
	}
	if !gone {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	_ = os.Remove(pidFile)
}
