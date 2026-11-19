//go:build !linux

package main

// bootframe_ubootlogo_other.go — no bootloader logo off Linux. JukaHub OS is
// Linux only (bootFrameUbootSupported is false here), so this never runs.

import "errors"

func bootFrameUbootOpen(bool) (bootLogoTarget, error) {
	return nil, errors.New("the bootloader's logo needs the handheld's Linux")
}
