//go:build !linux

package main

// bootframe_other.go — no framebuffer outside Linux; JukaHub OS is Linux only
// (jukaosNativeActive), so the boot frame never asks here.

import "errors"

func bootFrameGrab() ([]byte, string, error) {
	return nil, "", errors.New("the boot frame needs the handheld's Linux")
}
