//go:build !linux

package main

// gamewarm_other.go — "Warm up games before launch" is a Linux feature
// (gamewarm_linux.go): here nothing is warmed and the setting is hidden.

import "context"

const gameWarmSupported = false

func gameWarmLowPriority() {}

func gameWarmFile(ctx context.Context, path string, size int64) (int64, error) { return 0, nil }
