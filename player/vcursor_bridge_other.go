//go:build !linux

package main

// vcursor_bridge_other.go — Mouse in games and apps is a Linux handheld
// feature (vcursor_bridge_linux.go): it lends the pad to a game through evdev
// and uinput, which Windows and macOS do not have. A PC has a mouse of its
// own, so the switch is hidden there and the parked loop's tick does nothing.

import "errors"

// vcursorBridgeSupported reports whether this device can lend its pad to a
// game as a mouse: never here.
func vcursorBridgeSupported() bool { return false }

// vcursorBridgeTick is the parked loop's tick: nothing to watch here.
func vcursorBridgeTick(*Config) {}

// wineInputStart lends the pad to a Windows-app session on the handheld; a PC
// has its own mouse and keyboard.
func wineInputStart(p mbridgeParams, quit func()) (func(), error) {
	return func() {}, errors.New("the pad is only lent as a mouse on the Linux handheld")
}

// wineInputActive reports a Windows-app session holding the pad: never here.
func wineInputActive() bool { return false }

// wineInputLive lets a session's mouse and keys through once its program
// started: there is no such session here.
func wineInputLive() {}
