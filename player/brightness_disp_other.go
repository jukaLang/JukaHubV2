//go:build !linux

package main

import "errors"

// errDispUnsupported: the Allwinner display driver (brightness_disp_linux.go)
// only exists on Linux; elsewhere these stubs keep the controller's switch
// building, and brightnessFindBackend never picks "disp".
var errDispUnsupported = errors.New("the Allwinner display driver is Linux-only")

func dispBrightnessRaw() (int, error) { return 0, errDispUnsupported }

func dispSetBrightnessRaw(int) error { return errDispUnsupported }

func dispBrightnessAvailable() bool { return false }
