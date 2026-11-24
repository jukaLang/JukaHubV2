//go:build !linux

package main

// ppsspp_picture_other.go — no framebuffer driver to ask off Linux: the PSP
// picture check (ppsspp_picture.go) runs only on the handheld.

import "os"

// ppssppPictureAlphaByte: no alpha channel known.
func ppssppPictureAlphaByte(*os.File) int { return -1 }
