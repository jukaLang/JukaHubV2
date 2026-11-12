package main

// memstat_windows.go — the process's own memory from GetProcessMemoryInfo
// (memstat.go).

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// memProcessCountersEx is PROCESS_MEMORY_COUNTERS_EX (psapi.h). SIZE_T is
// pointer-sized, which uintptr is on every Windows target.
type memProcessCountersEx struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

// procMemK32GetProcessMemoryInfo is kernel32's export of psapi's
// GetProcessMemoryInfo (Windows 7 and later), so psapi.dll is not loaded for
// one call.
var procMemK32GetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// readSelfMemory reads the working set (what Task Manager calls the memory
// in use) and the private commit. Any goroutine.
func readSelfMemory() selfMemStat {
	if procMemK32GetProcessMemoryInfo.Find() != nil {
		return selfMemStat{}
	}
	var c memProcessCountersEx
	c.CB = uint32(unsafe.Sizeof(c))
	r, _, _ := procMemK32GetProcessMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&c)), uintptr(c.CB))
	if r == 0 || c.WorkingSetSize == 0 {
		return selfMemStat{}
	}
	return selfMemStat{rss: uint64(c.WorkingSetSize), private: uint64(c.PrivateUsage), ok: true}
}
