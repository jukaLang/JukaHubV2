//go:build linux

package main

// clockset_linux.go — setting the system clock (ntpsync.go). Linux lets root
// set it with settimeofday(2); the TrimUI runs apps as root. No shell and no
// `date -s`: the system call takes the exact time and cannot be misparsed.

import (
	"os"
	"syscall"
	"time"
)

// clockSetPermitted reports whether this process may set the clock.
func clockSetPermitted() bool { return os.Geteuid() == 0 }

// clockSetSystem sets the system clock to t.
func clockSetSystem(t time.Time) error {
	tv := syscall.NsecToTimeval(t.UnixNano())
	return syscall.Settimeofday(&tv)
}
