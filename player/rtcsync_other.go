//go:build !linux

package main

// rtcsync_other.go — no hardware clock to keep outside Linux: Windows and
// macOS keep their own time (rtcsync_linux.go).

// rtcSyncAfterNetTime does nothing here.
func rtcSyncAfterNetTime(clockAction) {}

// rtcStartupReport does nothing here.
func rtcStartupReport() {}
