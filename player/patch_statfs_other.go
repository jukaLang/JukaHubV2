//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package main

import "errors"

// syscallStatfs is a stub on platforms without a POSIX statfs.
type syscallStatfs struct {
	Bsize  int64
	Blocks int64
	Bfree  int64
	Bavail int64
}

// statfs is unavailable on this platform; freeBytes reports an error.
func statfs(path string, st *syscallStatfs) error {
	return errors.New("statfs not supported on this platform")
}
