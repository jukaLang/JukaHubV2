//go:build !linux

package main

// memswap_other.go — swap is the system's business off Linux handhelds
// (memSwapSupported is false there, so this never runs).

func memSwapRun(on bool) {}
