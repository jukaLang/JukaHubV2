//go:build !linux

package main

// portmaster_splash_other.go — PortMaster runs on the Linux handheld only.

import "os/exec"

func portEndOrphanHelpers(cmd *exec.Cmd) []string { return nil }

func pmSplashWatchStart(cmd *exec.Cmd) *pmSplashWatch { return nil }
