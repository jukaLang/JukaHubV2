//go:build !windows

package main

// sched_other.go — the non-Windows stand-ins for sched_windows.go. The handheld
// and Linux desktops have no registry or Startup folders; their startup items
// are read from the boot scripts and autostart folders in sched.go.

import (
	"errors"
	"os/exec"
)

// schedHideWindow is a no-op: there is no console window to hide.
func schedHideWindow(cmd *exec.Cmd) {}

func schedRegistryStartupItems() []schedItem { return nil }

func schedStartupFolderItems() []schedItem { return nil }

func schedSetApproved(key, name string, enabled bool) error {
	return errors.New("startup approval is a Windows setting")
}
