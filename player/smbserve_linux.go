package main

// smbserve_linux.go — the SMB share (smbserve.go) runs on Linux, where
// JukaHub on the handheld is root and may take port 445 and 137. On a Linux
// computer without root the listen fails and the toast says why.

import (
	"errors"
	"syscall"
)

// smbServeSupported reports whether the share can run here.
func smbServeSupported() error { return nil }

// smbServeErrnoStatus maps the Linux-only errors of a file operation.
func smbServeErrnoStatus(err error) (uint32, bool) {
	switch {
	case errors.Is(err, syscall.EROFS):
		return smb2StatusAccessDenied, true
	case errors.Is(err, syscall.ENOTEMPTY):
		return smbSrvStatusDirNotEmpty, true
	case errors.Is(err, syscall.EISDIR):
		return smb2StatusFileIsADirectory, true
	}
	return 0, false
}

// smbServeDiskSpace is the card's size and free space for QUERY_INFO.
func smbServeDiskSpace(dir string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil || st.Bsize <= 0 {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs, true
}
