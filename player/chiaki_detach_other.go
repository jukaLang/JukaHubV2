//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !windows

package main

import "os/exec"

// configureChiakiDetached is a no-op on platforms where the handoff cannot run
// anyway (Chiaki-ng is a TrimUI Linux arm64 package).
func configureChiakiDetached(cmd *exec.Cmd) {}
