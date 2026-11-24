//go:build !linux

package main

// plugperms_other.go — off Linux a plugin's script cannot be walled off the
// network (plugperms.go): the page says so.

import "os/exec"

func plugNetworkBlockable() bool { return false }

func plugSandbox(*exec.Cmd, []string) bool { return false }

func plugSandboxRefused(error) bool { return false }
