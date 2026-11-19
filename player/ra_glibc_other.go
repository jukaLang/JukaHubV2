//go:build !linux

package main

import "os/exec"

// raGlibcNamespace: only Linux has mount namespaces, and only the TrimUI
// handhelds start RetroArch on JukaHub's C library (ra_glibc.go).
func raGlibcNamespace(*exec.Cmd) bool { return false }
