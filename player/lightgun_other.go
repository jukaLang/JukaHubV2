//go:build !linux

package main

// lightgun_other.go — the light gun's mouse is made through Linux's uinput
// (lightgun_linux.go). A PC has a mouse of its own, and it is the gun: the
// layout and the launch settings still plug the gun into the right port with
// its trigger on the left button and the reload on the right one.

import "errors"

// lightgunSessionSupported reports whether this device makes the gun's mouse:
// never here.
func lightgunSessionSupported() bool { return false }

// lightgunActive reports a gun made for a launch: never here.
func lightgunActive() bool { return false }

// lightgunSessionStart makes nothing: the PC's mouse is the gun.
func lightgunSessionStart(lightgunSessionParams) error {
	return errors.New("the PC's own mouse is the light gun here")
}

// lightgunSessionLive has no gun to let move.
func lightgunSessionLive(string) {}

// lightgunSessionStop has no gun to remove.
func lightgunSessionStop(string, string) {}
