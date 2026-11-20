package main

// sdmove_linux.go — the system calls behind "Move to a new SD card".
//
// Device numbers tell two mounts of one card apart from two cards, sysfs says
// which disk a filesystem lives on and whether that disk is the one the
// system started from, the first sectors say whether a card carries an
// Allwinner boot area, and posix_fadvise makes the verification read the new
// card instead of the copy still held in memory.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// sdMoveFileDev is the device number of the filesystem a file lives on.
func sdMoveFileDev(info os.FileInfo) (uint64, bool) {
	if info == nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

// sdMoveDevOf is sdMoveFileDev for a path.
func sdMoveDevOf(path string) (uint64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return sdMoveFileDev(info)
}

// sdMoveDiskOf names the block disk ("mmcblk0", "sda") holding path's
// filesystem, from its device number through /sys/dev/block. "" when the
// filesystem is not on a block device (tmpfs, overlay, a loop of a file).
func sdMoveDiskOf(path string) string {
	dev, ok := sdMoveDevOf(path)
	if !ok {
		return ""
	}
	link := filepath.Join("/sys/dev/block", strconv.FormatUint(uint64(unix.Major(dev)), 10)+":"+strconv.FormatUint(uint64(unix.Minor(dev)), 10))
	target, err := filepath.EvalSymlinks(link)
	if err != nil {
		return ""
	}
	if fileExists(filepath.Join(target, "partition")) {
		target = filepath.Dir(target)
	}
	name := filepath.Base(target)
	if !sdMoveSafeDiskName(name) {
		return ""
	}
	return name
}

// sdMoveSafeDiskName accepts a plain block device name, so it can never make
// a path that leaves /dev or /sys/block.
func sdMoveSafeDiskName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// sdMovePartitionCount counts a disk's partitions.
func sdMovePartitionCount(disk string) int {
	if !sdMoveSafeDiskName(disk) {
		return 0
	}
	entries, err := os.ReadDir(filepath.Join("/sys/block", disk))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), disk) && fileExists(filepath.Join("/sys/block", disk, e.Name(), "partition")) {
			n++
		}
	}
	return n
}

// sdMoveCardCID is an SD card's CID register (unique per card), "" when the
// disk is not an SD card or the kernel does not show it.
func sdMoveCardCID(disk string) string {
	if !sdMoveSafeDiskName(disk) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join("/sys/block", disk, "device", "cid"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// sdMoveHasBootArea reports whether a disk carries an Allwinner boot area: a
// boot0 ("eGON.BT0" at byte 4) or a secure-boot TOC0 ("TOC0.GLH" at byte 0)
// at 8 KiB or 128 KiB, where the boot ROM looks. A plain data card has
// neither; a JukaHub OS image (os/plastron, os/smartpro) and other system
// cards do.
func sdMoveHasBootArea(disk string) bool {
	if !sdMoveSafeDiskName(disk) {
		return false
	}
	f, err := os.Open("/dev/" + disk)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 16)
	for _, off := range []int64{8 << 10, 128 << 10} {
		if n, err := f.ReadAt(buf, off); err != nil || n < len(buf) {
			continue
		}
		if bytes.Equal(buf[4:12], []byte("eGON.BT0")) || bytes.Equal(buf[0:8], []byte("TOC0.GLH")) {
			return true
		}
	}
	return false
}

// sdMoveStatfs reports a filesystem's size, free space (for an unprivileged
// writer, as df does) and block (cluster) size.
func sdMoveStatfs(path string) (total, free, block int64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bs := int64(st.Bsize)
	if bs <= 0 {
		return 0, 0, 0, errors.New("the filesystem reported no block size")
	}
	return int64(st.Blocks) * bs, int64(st.Bavail) * bs, bs, nil
}

// sdMoveReadOnly reports a filesystem mounted read-only.
func sdMoveReadOnly(path string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false
	}
	return st.Flags&unix.ST_RDONLY != 0
}

// sdMoveDropCache tells the kernel to forget the cached pages of f, so the
// next read comes from the card itself. The file must be synced: only clean
// pages are dropped.
func sdMoveDropCache(f *os.File) {
	if f == nil {
		return
	}
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}

// sdMoveDropAllCaches flushes everything and drops the page cache once,
// before the verification pass (JukaHub runs as root on the handheld; a
// refusal only leaves the per-file advice above).
func sdMoveDropAllCaches() {
	unix.Sync()
	_ = os.WriteFile("/proc/sys/vm/drop_caches", []byte("1\n"), 0o644)
}

// sdMoveSyncAll flushes every filesystem's dirty data and metadata, so every
// rename done so far is on the card before the journal says so.
func sdMoveSyncAll() { unix.Sync() }

// sdMoveLowPriority makes the calling OS thread a background one: nice 19 and
// the idle I/O class. The caller locks its goroutine to the thread first and
// never unlocks it, so the thread ends with the goroutine and is not handed
// back to other work.
func sdMoveLowPriority() {
	tid := unix.Gettid()
	_ = unix.Setpriority(unix.PRIO_PROCESS, tid, 19)
	const ioprioWhoProcess, ioprioClassIdle, ioprioClassShift = 1, 3, 13
	_, _, _ = unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(tid), uintptr(ioprioClassIdle<<ioprioClassShift))
}

// sdMoveFatalWriteErr reports a destination error that ends the run rather
// than one file: the card is full, read-only, or gone.
func sdMoveFatalWriteErr(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, syscall.ENOSPC):
		return "The new card is full.", true
	case errors.Is(err, syscall.EROFS):
		return "The new card became read-only — the system does that when it finds an error on the card.", true
	case errors.Is(err, syscall.ENODEV), errors.Is(err, syscall.ENXIO), errors.Is(err, syscall.ESHUTDOWN):
		return "The new card went away — is the reader still plugged in?", true
	case errors.Is(err, syscall.EIO):
		return "The new card reported a read/write error.", true
	}
	return "", false
}
