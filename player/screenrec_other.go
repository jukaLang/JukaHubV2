//go:build !linux

package main

// screenrec_other.go — only Linux handhelds record (screenRecSupported);
// these keep screenrec.go building elsewhere.

import "os/exec"

func screenRecConfigure(cmd *exec.Cmd) { processGroupConfigure(cmd) }

func screenRecLower(*exec.Cmd) {}

func screenRecSignal(cmd *exec.Cmd, kill bool) {
	if kill && cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
