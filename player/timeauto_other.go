//go:build !windows

package main

// timeauto_other.go — elsewhere the system's own time service is not read:
// the Date & time row only says who sets the clock.

// systemTimeAutoRead reports the system's "set time automatically" switch:
// unknown here.
func systemTimeAutoRead() (on, known bool) { return false, false }
