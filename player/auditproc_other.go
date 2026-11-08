//go:build !windows

package main

import "os/exec"

// auditConfigureHidden is a no-op off Windows: there is no console to hide.
func auditConfigureHidden(cmd *exec.Cmd) {}

// auditConfigureDetached puts a GUI tool in its own process group, so it is
// not signalled with JukaHub and outlives the screen that started it.
func auditConfigureDetached(cmd *exec.Cmd) {
	screenChildGroupConfigure(cmd)
}
