//go:build linux || darwin || freebsd || netbsd || openbsd

package main

import "syscall"

// syscallStatfs is the platform statfs struct used by freeBytes.
type syscallStatfs struct {
	Bsize  int64
	Blocks int64
	Bfree  int64
	Bavail int64
}

// statfs wraps the platform syscall.
func statfs(path string, st *syscallStatfs) error {
	var raw syscall.Statfs_t
	if err := syscall.Statfs(path, &raw); err != nil {
		return err
	}
	st.Bsize = int64(raw.Bsize)
	st.Blocks = int64(raw.Blocks)
	st.Bfree = int64(raw.Bfree)
	st.Bavail = int64(raw.Bavail)
	return nil
}
