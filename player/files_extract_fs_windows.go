//go:build windows

package main

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procFeGetDiskFreeSpace is kernel32's GetDiskFreeSpaceW, which golang.org/x/sys
// does not wrap: the only call that gives a volume's cluster size.
var procFeGetDiskFreeSpace = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")

// feVolumeOf reads what the volume holding dir means for an extraction's plan
// (feVolume): its cluster from GetDiskFreeSpaceW and its file system's name
// from GetVolumeInformationW ("FAT32", "FAT", "exFAT", "NTFS"…). A volume that
// cannot be read is a zero feVolume, counted in 4 KiB units.
func feVolumeOf(dir string) feVolume {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return feVolume{}
	}
	var rootBuf [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumePathName(p, &rootBuf[0], uint32(len(rootBuf))); err != nil {
		return feVolume{}
	}
	root := &rootBuf[0]
	var v feVolume
	var nameBuf [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumeInformation(root, nil, 0, nil, nil, nil, &nameBuf[0], uint32(len(nameBuf))); err == nil {
		switch strings.ToUpper(windows.UTF16ToString(nameBuf[:])) {
		case "FAT", "FAT32":
			v.fat, v.fatLike = true, true
		case "EXFAT":
			v.fatLike = true
		}
	}
	var sectorsPerCluster, bytesPerSector, freeClusters, totalClusters uint32
	if r, _, _ := procFeGetDiskFreeSpace.Call(uintptr(unsafe.Pointer(root)),
		uintptr(unsafe.Pointer(&sectorsPerCluster)), uintptr(unsafe.Pointer(&bytesPerSector)),
		uintptr(unsafe.Pointer(&freeClusters)), uintptr(unsafe.Pointer(&totalClusters))); r != 0 {
		v.cluster = int64(sectorsPerCluster) * int64(bytesPerSector)
	}
	return v
}
