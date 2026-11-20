//go:build !linux

package main

// sdmove_other.go — "Move to a new SD card" off Linux.
//
// The move is a handheld feature (sdMoveSupported is false here), so these
// only keep the shared code compiling: no device numbers, no boot areas, sizes
// through the Storage screen's reader.

import (
	"errors"
	"os"
)

func sdMoveFileDev(info os.FileInfo) (uint64, bool) { return 0, false }

func sdMoveDevOf(path string) (uint64, bool) { return 0, false }

func sdMoveDiskOf(path string) string { return "" }

func sdMovePartitionCount(disk string) int { return 0 }

func sdMoveCardCID(disk string) string { return "" }

func sdMoveHasBootArea(disk string) bool { return false }

func sdMoveStatfs(path string) (total, free, block int64, err error) {
	t, f, err := sysToolVolumeUsage(path)
	if err != nil {
		return 0, 0, 0, err
	}
	if t == 0 {
		return 0, 0, 0, errors.New("the drive reported no size")
	}
	return int64(t), int64(f), 4096, nil
}

func sdMoveReadOnly(path string) bool { return false }

func sdMoveDropCache(f *os.File) {}

func sdMoveDropAllCaches() {}

func sdMoveSyncAll() {}

func sdMoveLowPriority() {}

func sdMoveFatalWriteErr(err error) (string, bool) { return "", false }
