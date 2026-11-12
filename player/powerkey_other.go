//go:build !linux

package main

// powerkey_other.go — no power-key watcher off Linux: a PC's power button is
// the operating system's. The Settings row stays hidden (powerKeyWatcherLive).

// powerKeyStartDevices opens nothing.
func powerKeyStartDevices() bool { return false }
