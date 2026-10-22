//go:build windows

package main

// processAlive reports whether pid still exists. Windows one-file helpers keep
// unpacking into the system TEMP folder, so JukaHub never purges an _MEI
// directory there and "not running" is the safe answer for this build.
func processAlive(pid int) bool { return false }
