//go:build linux

package main

import "syscall"

// wineFSTakesLinks tells from the file system's type whether a folder can
// hold Wine's symbolic links, without writing anything. known is false for a
// type this list does not name (exFAT through FUSE, for one), so the caller
// tests with a real link instead.
func wineFSTakesLinks(path string) (links, known bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, false
	}
	switch uint32(st.Type) {
	case 0x4d44, 0x2011bab0, 0x73717368, 0x9660:
		// FAT (vfat, msdos), exFAT, and the read-only SquashFS and ISO 9660.
		return false, true
	case 0xef53, 0x01021994, 0xf2f52010, 0x9123683e, 0x58465342, 0x794c7630:
		// ext2/3/4, tmpfs, F2FS, Btrfs, XFS and overlayfs.
		return true, true
	}
	return false, false
}
