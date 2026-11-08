//go:build linux || darwin || freebsd || netbsd || openbsd

package main

// bootsync_unix.go — the last step of the teardown on the handheld.

import "syscall"

// bootSyncFilesystems asks the kernel to write every dirty buffer to its
// device. The stores the user cannot regenerate are already written with an
// fsync (AtomicWrite), but the cache database, logs and the rest are not, and
// the stock launcher may power the handheld off moments after JukaHub exits.
// It reports whether there was anything to do on this platform.
func bootSyncFilesystems() bool {
	syscall.Sync()
	return true
}
