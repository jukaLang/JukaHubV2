//go:build !linux

package main

// usbpad_other.go — Controller mode off Linux. Only the handheld turns its
// port into a USB controller; on a computer the screen's entries are hidden
// (usbPadSupported) and these keep the shared code compiling.

import "errors"

func usbPadProbe() usbPadCaps {
	return usbPadCaps{problem: "controller mode works on the handheld only"}
}

func usbPadPlatformStart(usbPadStartConfig) (usbPadRunInfo, func(), error) {
	return usbPadRunInfo{}, nil, errors.New("controller mode works on the handheld only")
}

func usbPadClearStale() {}
