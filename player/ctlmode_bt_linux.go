//go:build linux

package main

// ctlmode_bt_linux.go — the Bluetooth gamepad link runs where BlueZ does.

// ctlBlePlatform reports whether this build can drive BlueZ at all; whether
// a bus, an adapter and bluetoothd are there is ctlBleProbe's to say.
func ctlBlePlatform() (bool, string) { return true, "" }
