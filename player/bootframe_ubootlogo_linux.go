//go:build linux

package main

// bootframe_ubootlogo_linux.go — finding and opening the bootloader's logo on
// the SD card JukaHub OS booted from (bootframe_ubootlogo.go).
//
// The card's partition table copies the internal eMMC's names, so a partition
// called bootloader exists on both disks and bootlogo_linux.go's discovery
// (which refuses anything but the eMMC) is not the way in. Here the disk comes
// first: the one holding the running system and the data folder, which sysfs
// must call an SD card; then its single FAT logo partition by name. The FAT
// mount, the read and the in-place write are bootlogo_linux.go's.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// bootFrameUbootParts lists the named partitions sysfs knows, each with the
// disk it is on ("" when sysfs cannot place it).
func bootFrameUbootParts() []bootFrameUbootPart {
	dir := filepath.Join(bootLogoSysRoot, "class", "block")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []bootFrameUbootPart
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "uevent"))
		if err != nil {
			continue
		}
		ev := bootLogoUevent(string(data))
		if ev["DEVTYPE"] != "partition" || ev["PARTNAME"] == "" {
			continue
		}
		p := bootFrameUbootPart{Name: ev["PARTNAME"], Dev: ev["DEVNAME"]}
		if p.Dev == "" {
			p.Dev = e.Name()
		}
		major, errA := strconv.ParseUint(ev["MAJOR"], 10, 32)
		minor, errB := strconv.ParseUint(ev["MINOR"], 10, 32)
		if errA == nil && errB == nil {
			p.Num = unix.Mkdev(uint32(major), uint32(minor))
			if disk, err := bootLogoDiskOf(p.Num); err == nil {
				p.Disk = disk
			}
		}
		out = append(out, p)
	}
	return out
}

// bootFrameUbootOpen opens bootlogo.bmp on the card's FAT logo partition: the
// partition bootFrameUbootPick allows, whose device node really is that
// device. write mounts it writable (bootLogoOpenFAT).
func bootFrameUbootOpen(write bool) (bootLogoTarget, error) {
	rootDev, okRoot := bootLogoFSDev("/")
	dataDev, okData := bootLogoFSDev(jukaosNativeDataDir())
	if !okRoot || !okData || rootDev == 0 || dataDev == 0 {
		return nil, errors.New("the system and the data folder have no device numbers")
	}
	rootDisk, _ := bootLogoDiskOf(rootDev)
	dataDisk, _ := bootLogoDiskOf(dataDev)
	kind := ""
	if rootDisk != "" {
		if data, err := os.ReadFile(filepath.Join(bootLogoSysRoot, "block", rootDisk, "device", "type")); err == nil {
			kind = strings.TrimSpace(string(data))
		}
	}
	p, err := bootFrameUbootPick(rootDev, dataDev, rootDisk, dataDisk, kind, bootFrameUbootParts())
	if err != nil {
		return nil, err
	}
	dev := bootLogoDevice(p.Dev)
	if dev == "" {
		return nil, fmt.Errorf("no block device for %s (%s)", p.Dev, p.Name)
	}
	if num, ok := bootLogoRdev(dev); !ok || num != p.Num {
		return nil, fmt.Errorf("%s is not device %d:%d", dev, unix.Major(p.Num), unix.Minor(p.Num))
	}
	// A private mount a killed JukaHub left behind goes first.
	bootLogoSweepStaleMounts()
	t, err := bootLogoOpenFAT(dev, p.Name, write)
	if err != nil {
		return nil, err
	}
	return t, nil
}
