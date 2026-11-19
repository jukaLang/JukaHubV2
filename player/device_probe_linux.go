//go:build linux

package main

import (
	"os"
	"strconv"
	"syscall"
)

// deviceFileStampExtra adds what Linux knows beyond size and modification
// time: the change time (a copy that kept the old modification time still
// changes it), the inode and the device, so a firmware that put a new launcher
// in place is told from the old one.
func deviceFileStampExtra(fi os.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return ":" + strconv.FormatInt(st.Ctim.Nano(), 10) + ":" + strconv.FormatUint(uint64(st.Ino), 10) +
		":" + strconv.FormatUint(uint64(st.Dev), 10)
}
