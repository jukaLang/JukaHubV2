//go:build !linux

package main

// ledscreen_other.go — no framebuffer off Linux: the Screen colours mode
// shows the theme's accent (ledscreen.go).

import "time"

func ledScreenSampleFB(time.Time, *[3]float64, *[3]float64) bool { return false }

func ledScreenFBRelease() {}
