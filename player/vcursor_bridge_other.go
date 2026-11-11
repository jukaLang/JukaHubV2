//go:build !linux

package main

// vcursor_bridge_other.go — Mouse in games and apps is a Linux handheld
// feature (vcursor_bridge_linux.go): it lends the pad to a game through evdev
// and uinput, which Windows and macOS do not have. A PC has a mouse of its
// own, so the switch is hidden there and the parked loop's tick does nothing.

// vcursorBridgeSupported reports whether this device can lend its pad to a
// game as a mouse: never here.
func vcursorBridgeSupported() bool { return false }

// vcursorBridgeTick is the parked loop's tick: nothing to watch here.
func vcursorBridgeTick(*Config) {}
