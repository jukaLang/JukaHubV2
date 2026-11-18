//go:build !linux

package main

// DRM master exists only on Linux; drmHandoffInit never enables the handoff
// elsewhere (SDL has no KMSDRM driver there).

func drmDropMaster() []int { return nil }

func drmSetMaster([]int) error { return nil }
