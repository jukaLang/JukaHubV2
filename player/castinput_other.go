//go:build !linux

package main

// castinput_other.go — no built-in pad to write into: a Screen Share viewer's
// presses reach JukaHub's own screens through SDL (castinput.go), not games.

import "errors"

func castPadDeviceOpen() (castPadDevice, error) {
	return nil, errors.New("games hear a viewer's presses only on the handheld")
}

func castInputRecover() {}
