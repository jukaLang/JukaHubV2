//go:build windows

package main

// Test-only process memory reading — see memoryrss_linux_test.go for why the
// RSS matters at all. Windows reports the resident set as the working set.

import (
	"syscall"
	"unsafe"
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS. Two DWORDs then
// SIZE_Ts: Go aligns uintptr the same way the C struct does on both 32- and
// 64-bit builds, so the layout matches without padding fields.
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

var procGetProcessMemoryInfo = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

// currentProcessRSSBytes returns this process's working set, or 0 when the API
// is unavailable.
func currentProcessRSSBytes() uint64 {
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var counters processMemoryCounters
	counters.cb = uint32(unsafe.Sizeof(counters))
	ok, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		unsafe.Sizeof(counters),
	)
	if ok == 0 {
		return 0
	}
	return uint64(counters.workingSetSize)
}
