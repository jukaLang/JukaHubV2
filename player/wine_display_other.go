//go:build !linux

package main

// wine_display_other.go — WestonPack is a Linux handheld runtime: there is no
// image to mount and no udev to label devices for elsewhere. On Windows the
// program opens its own windows (wine_display.go).

import "errors"

func wineMountWestonImage(image string, log wineLog) (string, bool, error) {
	return "", false, errors.New("WestonPack images are only mounted on the Linux handheld")
}

func wineUnmountWeston(dir string, log wineLog) {}

func wineReleaseLoop(dev string, log wineLog) {}

func wineLabelInputDevices(log wineLog) {}

func wineSettleInput(log wineLog) {}
