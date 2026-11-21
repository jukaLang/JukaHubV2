//go:build !linux

package main

// tailscaled_other.go — the built-in Tailscale off the handheld
// (tailscaled.go): nothing is installed or run here; Tailscale for Windows is
// found where its installer puts it, for a PATH that does not name it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// tailscaledSocketPath is unused off Linux: JukaHub runs no daemon here.
func tailscaledSocketPath() string { return "" }

// tailscaledSystemCLI is Tailscale for Windows' tailscale.exe, or "".
func tailscaledSystemCLI() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	for _, env := range []string{"ProgramFiles", "ProgramW6432"} {
		dir := os.Getenv(env)
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, "Tailscale", "tailscale.exe"); fileExists(p) {
			return p
		}
	}
	return ""
}

func tailscaledLowerPriority(cmd *exec.Cmd) {}

func tailscaledKillStale(pidFile, home string) {}
