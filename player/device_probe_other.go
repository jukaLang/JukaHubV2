//go:build !linux

package main

import "os"

// deviceFileStampExtra has nothing to add off Linux: size and modification
// time identify the file (and no TrimUI launcher is ever scanned there).
func deviceFileStampExtra(os.FileInfo) string { return "" }
