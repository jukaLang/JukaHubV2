//go:build linux

package main

// memfree_linux.go — memfree.go's Linux steps: the kernel's caches, the
// loaded Piper voice (tts_local_other.go) and telling an orphan from
// JukaHub's own children.

import (
	"os"
	"strconv"
	"syscall"
	"time"
)

// memFreeOurDescendant reports whether pid runs under this JukaHub — a child,
// or a child's child — so it is JukaHub's own work rather than an orphan
// (memFreeCloseOrphans). Any goroutine.
func memFreeOurDescendant(pid int) bool {
	self := os.Getpid()
	for i := 0; i < 64 && pid > 1; i++ {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		st, ok := closeAllParseStat(data)
		if !ok {
			return false
		}
		if st.ppid == self {
			return true
		}
		pid = st.ppid
	}
	return false
}

// memFreeKernel writes dirty pages out, drops the clean page cache and the
// dentry and inode caches, and compacts memory, so the room MemAvailable
// promises is free at once instead of after reclaim (a model loading into
// fragmented memory stalls in it). The handheld only, where everything runs
// as root and the page cache is JukaHub's to drop (closeAllScanAllowed). It
// reports whether it ran. Off the main thread: sync may take a moment.
func memFreeKernel() bool {
	if !closeAllScanAllowed() {
		return false
	}
	syscall.Sync()
	_ = os.WriteFile("/proc/sys/vm/drop_caches", []byte("3\n"), 0o644)
	// Only kernels built with compaction have it; nothing to do otherwise.
	_ = os.WriteFile("/proc/sys/vm/compact_memory", []byte("1\n"), 0o644)
	return true
}

// memFreeStopIdlePiper unloads the warm Piper voice when no line uses it,
// nothing is being said and — unless forQuestion (the model starting for a
// question needs the room more than the voice that would read its answer)
// — it has not spoken for memFreeHelperIdle. It reports whether one was
// unloaded. Any goroutine.
func memFreeStopIdlePiper(forQuestion bool) bool {
	ttsPiperWarm.mu.Lock()
	p := ttsPiperWarm.p
	if p == nil || ttsPiperWarm.busy || ttsBusy.Load() || (!forQuestion && time.Since(ttsPiperWarm.lastUse) < memFreeHelperIdle) {
		ttsPiperWarm.mu.Unlock()
		return false
	}
	ttsPiperWarm.p = nil
	ttsPiperWarm.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	logDebug("tts", "Piper unloaded: memory is needed")
	return true
}
