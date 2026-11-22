//go:build linux

package main

// bootenv_linux.go — finding, reading and writing U-Boot's environment for
// Silent boot (bootenv.go).
//
// The partitions are found by NAME with the boot logo's finder
// (bootLogoPartition: /dev/by-name, the kernel command line's partitions=
// list and the GPT names in sysfs must agree) and refused unless they sit on
// the handheld's internal eMMC, not on the disk JukaHub runs from
// (bootLogoRefuseDevice). Reads come from the device, not the page cache; a
// write is one exclusive, synced write of the whole copy at the start of the
// partition U-Boot is NOT loading, read back and compared byte for byte.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// bootEnvRedundNames are the names a firmware gives the second copy.
var bootEnvRedundNames = []string{"env-redund", "env_redund"}

func bootEnvPlatformSupported() bool { return true }

// bootEnvFind finds the partition called name on the internal eMMC; "" when no
// source names it.
func bootEnvFind(name string) (string, error) {
	dev, ok, err := bootLogoPartition(name)
	if err != nil {
		return "", fmt.Errorf("%w (%v)", errBootEnvNoStore, err)
	}
	if !ok {
		return "", nil
	}
	if reason := bootLogoRefuseDevice(name, dev); reason != "" {
		return "", fmt.Errorf("%w (%s)", errBootEnvNoStore, reason)
	}
	return dev, nil
}

// bootEnvReadDev reads up to limit bytes from the start of a partition, from
// the device rather than the cache.
func bootEnvReadDev(dev string, limit int) ([]byte, error) {
	size, err := bootLogoPartSize(dev)
	if err != nil {
		return nil, err
	}
	n := int64(limit)
	if size < n {
		n = size
	}
	f, err := os.Open(dev)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	bootLogoFlushBlock(f)
	bootLogoDropCache(f)
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// bootEnvLoadPlatform reads both copies, the running kernel's command line and
// the start of the kernel image, and leaves the rest to bootEnvPlanFrom.
func bootEnvLoadPlatform() (bootEnvPlan, error) {
	dev0, err := bootEnvFind("env")
	if err != nil {
		return bootEnvPlan{}, err
	}
	if dev0 == "" {
		return bootEnvPlan{}, fmt.Errorf("%w (no partition named env)", errBootEnvNoStore)
	}
	dev1, name1 := "", ""
	for _, name := range bootEnvRedundNames {
		d, err := bootEnvFind(name)
		if err != nil {
			return bootEnvPlan{}, err
		}
		if d == "" {
			continue
		}
		if dev1 != "" {
			return bootEnvPlan{}, fmt.Errorf("%w (both %s and %s are named)", errBootEnvNoStore, name1, name)
		}
		dev1, name1 = d, name
	}
	raw0, err := bootEnvReadDev(dev0, bootEnvReadMax)
	if err != nil {
		return bootEnvPlan{}, fmt.Errorf("reading env (%s): %w", dev0, err)
	}
	var raw1 []byte
	if dev1 != "" {
		if bootLogoSameDevice(dev0, dev1) {
			return bootEnvPlan{}, fmt.Errorf("%w (env and %s are the same device)", errBootEnvNoStore, name1)
		}
		if raw1, err = bootEnvReadDev(dev1, bootEnvReadMax); err != nil {
			return bootEnvPlan{}, fmt.Errorf("reading %s (%s): %w", name1, dev1, err)
		}
	}
	cmdline, cerr := os.ReadFile(filepath.Join(bootLogoProcRoot, "cmdline"))
	// The kernel image's header is read only to look: a level named there
	// would follow the environment's on the command line and win.
	var head []byte
	if dev, err := bootEnvFind("boot"); err == nil && dev != "" {
		if h, err := bootEnvReadDev(dev, bootEnvImageHead); err == nil {
			head = h
		}
	}
	return bootEnvPlanFrom([2]string{"env", name1}, [2]string{dev0, dev1}, raw0, raw1, string(cmdline), cerr == nil, head)
}

// bootEnvWriteCopyPlatform writes data over the start of one copy's partition
// and reads it back. expect, when given, is what the partition must still
// hold (the bytes the write was prepared from); touched reports whether the
// partition was opened for writing at all.
func bootEnvWriteCopyPlatform(p bootEnvPlan, slot int, expect, data []byte) (touched bool, err error) {
	if slot < 0 || slot > 1 || p.Size <= 0 || len(data) != p.Size {
		return false, fmt.Errorf("refusing a write of %d bytes to copy %d", len(data), slot)
	}
	name, dev := p.Names[slot], p.Devs[slot]
	// The name must still lead to the same partition on the eMMC.
	now, err := bootEnvFind(name)
	if err != nil {
		return false, err
	}
	if now == "" || !bootLogoSameDevice(now, dev) {
		return false, fmt.Errorf("%s moved from %s while JukaHub was preparing; nothing was written", name, dev)
	}
	if expect != nil {
		cur, err := bootEnvReadDev(dev, len(expect))
		if err != nil {
			return false, fmt.Errorf("could not read %s again, so nothing was written: %w", name, err)
		}
		if !bytes.Equal(cur, expect) {
			return false, errBootEnvChanged
		}
	}
	// O_EXCL on a block device is an exclusive open: it fails while anything
	// has the partition mounted or open exclusively.
	f, err := os.OpenFile(dev, os.O_WRONLY|os.O_SYNC|os.O_EXCL, 0)
	if err != nil {
		return false, err
	}
	n, err := f.WriteAt(data, 0)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if serr := f.Sync(); err == nil {
		err = serr
	}
	bootLogoDropCache(f)
	bootLogoFlushBlock(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return true, err
	}
	back, err := bootEnvReadDev(dev, len(data))
	if err != nil {
		return true, fmt.Errorf("could not read %s back: %w", name, err)
	}
	if !bytes.Equal(back, data) {
		return true, fmt.Errorf("%s did not read back as written", name)
	}
	return true, nil
}
