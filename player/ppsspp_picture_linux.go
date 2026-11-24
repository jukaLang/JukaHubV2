//go:build linux

package main

// ppsspp_picture_linux.go — the framebuffer driver's own word on where a
// pixel's alpha is, for the PSP picture check (ppsspp_picture.go).

import "os"

// ppssppPictureAlphaByte is the byte of a 32-bit pixel that holds alpha, from
// fb_var_screeninfo, or -1 when the framebuffer has none or the driver does
// not say.
func ppssppPictureAlphaByte(f *os.File) int {
	v := make([]byte, castFBVarInfoSize)
	if castIoctl(f, castFBIOGetVScreenInfo, v) != nil {
		return -1
	}
	return ppssppPictureAlphaFromVar(v)
}
