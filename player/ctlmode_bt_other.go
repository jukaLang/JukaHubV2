//go:build !linux

package main

// ctlmode_bt_other.go — no BlueZ here: the Bluetooth gamepad link is the
// handheld's (Linux) only.

// ctlBlePlatform says why the link cannot run on this system.
func ctlBlePlatform() (bool, string) {
	return false, "The Bluetooth gamepad needs the handheld's Bluetooth (Linux with BlueZ)"
}
