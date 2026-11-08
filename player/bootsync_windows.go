//go:build windows

package main

// bootsync_windows.go — Windows flushes a closed file's cache itself, and the
// stores that matter are fsynced as they are written (AtomicWrite), so the
// teardown has no sync step here.

// bootSyncFilesystems reports that there is nothing to do on this platform.
func bootSyncFilesystems() bool { return false }
