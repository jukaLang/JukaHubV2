package main

// memstat.go — how much memory JukaHub itself uses.
//
// "How much of the RAM does this app use?" is answered by the process's
// resident memory: the pages of RAM it holds right now, VmRSS on Linux and
// the working set on Windows (memstat_linux.go, memstat_windows.go; other
// systems report nothing, memstat_other.go). The Hardware screen shows it
// beside what the system has left (systool_hw.go), About shows the same line,
// and the log gets a fuller line now and then (adaptive.go memoryLogTick):
// resident memory, the Go heap, the textures the caches account for and the
// system's free memory, so a bug report says what the app held.

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
)

// selfMemStat is one reading of the process's own memory, in bytes.
type selfMemStat struct {
	rss     uint64 // resident: VmRSS on Linux, the working set on Windows
	swap    uint64 // Linux: VmSwap, what the kernel moved out to swap
	private uint64 // Windows: PrivateUsage, the memory committed to the process alone
	ok      bool
}

// memShort is a memory size the way the read-outs show it: whole megabytes
// below a gigabyte ("92 MB"), one decimal from there ("1.4 GB").
func memShort(b uint64) string {
	const mb = 1 << 20
	if b < 1000*mb {
		return fmt.Sprintf("%d MB", (b+mb/2)/mb)
	}
	return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
}

// selfMemoryText is the short read-out — "JukaHub 92 MB · 410 MB free" —
// without the free part when the system's figures are unknown, and "" when
// the process's own reading is. used and total are the system's memory in
// use and in all (sysToolMemory), so "free" is what is available to programs.
func selfMemoryText(st selfMemStat, used, total uint64, sysOK bool) string {
	if !st.ok || st.rss == 0 {
		return ""
	}
	s := "JukaHub " + memShort(st.rss)
	if sysOK && total > used {
		s += " · " + memShort(total-used) + " free"
	}
	return s
}

// selfMemCache is the last reading, for the render thread, which must not
// read files: About asks on every frame. A reading older than two seconds is
// refreshed on a goroutine while the old one is still shown.
var selfMemCache struct {
	mu          sync.Mutex
	stat        selfMemStat
	used, total uint64
	sysOK       bool
	at          time.Time
	busy        bool
}

// selfMemoryCachedText is selfMemoryText from the cache, "" until the first
// reading is in. Any goroutine; it never blocks on a read.
func selfMemoryCachedText() string {
	c := &selfMemCache
	c.mu.Lock()
	if !c.busy && time.Since(c.at) > 2*time.Second {
		c.busy = true
		go selfMemoryRefresh()
	}
	st, used, total, sysOK := c.stat, c.used, c.total, c.sysOK
	c.mu.Unlock()
	return selfMemoryText(st, used, total, sysOK)
}

// selfMemoryRefresh takes a reading into the cache.
func selfMemoryRefresh() {
	st := readSelfMemory()
	used, total, sysOK := sysToolMemory()
	c := &selfMemCache
	c.mu.Lock()
	c.stat, c.used, c.total, c.sysOK = st, used, total, sysOK
	c.at = time.Now()
	c.busy = false
	c.mu.Unlock()
}

// memoryReportLine is the log's memory line. It adds up the render thread's
// texture caches, so it runs on the main thread (memoryLogTick); the reads
// behind it are two small /proc files on Linux and one call on Windows.
func memoryReportLine() string {
	var parts []string
	if st := readSelfMemory(); st.ok {
		self := "JukaHub " + memShort(st.rss) + " resident"
		switch {
		case st.swap > 0:
			self += " (+" + memShort(st.swap) + " swapped out)"
		case st.private > 0:
			self += " (" + memShort(st.private) + " private)"
		}
		parts = append(parts, self)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	parts = append(parts, fmt.Sprintf("Go heap %s (runtime %s from the OS)", memShort(ms.HeapAlloc), memShort(ms.Sys)))
	tex := collectTextureMemoryStats().Total() + gifWallTextureBytes() + liquidTextureBytes()
	if tex > 0 {
		parts = append(parts, "textures ~"+memShort(uint64(tex)))
	}
	if used, total, ok := sysToolMemory(); ok && total > used {
		parts = append(parts, "system "+memShort(total-used)+" free of "+memShort(total))
	}
	return "memory: " + strings.Join(parts, ", ")
}
