//go:build !linux

package main

// displayguard_other.go — the display guard is the TrimUI's (Linux): another
// program drawing beside JukaHub is a framebuffer's problem, and a desktop's
// compositor already keeps windows apart. See displayguard_linux.go.

// displayGuardTick does nothing away from Linux.
func displayGuardTick() {}
