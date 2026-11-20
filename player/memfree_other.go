//go:build !linux

package main

// memfree_other.go — no page cache of ours to drop and no warm Piper to stop
// off Linux: memfree.go's other steps still run.

func memFreeKernel() bool { return false }

func memFreeStopIdlePiper(forQuestion bool) bool { return false }

func memFreeOurDescendant(pid int) bool { return false }
