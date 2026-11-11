//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// bootLogoTestRoots points the discovery at a temporary /proc, /sys and /dev
// in which regular files stand in for block devices, and makes any mount call
// a failure unless the test installs its own.
func bootLogoTestRoots(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"proc", "sys/class/block", "dev/by-name", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldProc, oldSys, oldDev, oldBase := bootLogoProcRoot, bootLogoSysRoot, bootLogoDevRoot, bootLogoMountBase
	oldMount, oldUnmount, oldBlock := bootLogoMount, bootLogoUnmount, bootLogoIsBlock
	oldGuard, oldSameFS, oldPause := bootLogoDiskGuard, bootLogoSameFS, bootLogoRemountPause
	t.Cleanup(func() {
		bootLogoProcRoot, bootLogoSysRoot, bootLogoDevRoot, bootLogoMountBase = oldProc, oldSys, oldDev, oldBase
		bootLogoMount, bootLogoUnmount, bootLogoIsBlock = oldMount, oldUnmount, oldBlock
		bootLogoDiskGuard, bootLogoSameFS, bootLogoRemountPause = oldGuard, oldSameFS, oldPause
	})
	bootLogoProcRoot = filepath.Join(root, "proc")
	bootLogoSysRoot = filepath.Join(root, "sys")
	bootLogoDevRoot = filepath.Join(root, "dev")
	bootLogoMountBase = filepath.Join(root, "tmp")
	bootLogoIsBlock = func(fi os.FileInfo) bool { return fi.Mode().IsRegular() }
	bootLogoMount = func(string, string, string, uintptr, string) error { return errors.New("unexpected mount") }
	bootLogoUnmount = func(string, int) error { return errors.New("unexpected unmount") }
	// Regular files stand in for devices and folders for filesystems: they
	// carry no device numbers, so the disk guard and the "is this folder
	// really the partition" check are answered here, and tested on their own.
	bootLogoDiskGuard = func(string) error { return nil }
	bootLogoSameFS = func(string, *os.File, string) bool { return true }
	bootLogoRemountPause = 0
	return root
}

// addBootLogoPart makes /dev/<dev> (content, padded to size bytes), its sysfs
// size and uevent (PARTNAME=name) and the /dev/by-name/<name> link.
func addBootLogoPart(t *testing.T, root, dev, name string, content []byte, size int) string {
	t.Helper()
	path := filepath.Join(root, "dev", dev)
	data := make([]byte, size)
	copy(data, content)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sys := filepath.Join(root, "sys", "class", "block", dev)
	if err := os.MkdirAll(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sys, "size"), []byte(fmt.Sprintf("%d\n", size/512)), 0o644); err != nil {
		t.Fatal(err)
	}
	uevent := "MAJOR=179\nMINOR=1\nDEVNAME=" + dev + "\nDEVTYPE=partition\nPARTNAME=" + name + "\n"
	if err := os.WriteFile(filepath.Join(sys, "uevent"), []byte(uevent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../"+dev, filepath.Join(root, "dev", "by-name", name)); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeFATMounts mounts a folder in place of a FAT filesystem: a mount copies
// the folder's files into the mount point, an unmount copies them back.
type fakeFATMounts struct {
	backing map[string]string // device (resolved) → the folder standing in for its filesystem
	mounted map[string]string // mount point → device
	mounts  []uintptr         // flags of every fresh mount
	remount []uintptr         // flags of every remount attempt
	unmount int
	failRO  int // remounts back to read-only that fail (busy) before one works
}

func newFakeFATMounts() *fakeFATMounts {
	return &fakeFATMounts{backing: map[string]string{}, mounted: map[string]string{}}
}

func copyFlatDir(t *testing.T, from, to string) {
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func (m *fakeFATMounts) install(t *testing.T) {
	bootLogoMount = func(source, target, fstype string, flags uintptr, _ string) error {
		if flags&bootLogoMSRemount != 0 {
			m.remount = append(m.remount, flags)
			if flags&bootLogoMSRdonly != 0 && m.failRO > 0 {
				m.failRO--
				return syscall.EBUSY
			}
			return nil
		}
		dev, err := filepath.EvalSymlinks(source)
		if err != nil {
			return err
		}
		dir, ok := m.backing[dev]
		if !ok || fstype != "vfat" {
			return syscall.EINVAL
		}
		copyFlatDir(t, dir, target)
		m.mounted[target] = dev
		m.mounts = append(m.mounts, flags)
		return nil
	}
	bootLogoUnmount = func(target string, _ int) error {
		dev, ok := m.mounted[target]
		if !ok {
			return syscall.EINVAL
		}
		copyFlatDir(t, target, m.backing[dev])
		entries, _ := os.ReadDir(target)
		for _, e := range entries {
			_ = os.Remove(filepath.Join(target, e.Name()))
		}
		delete(m.mounted, target)
		m.unmount++
		return nil
	}
}

func TestBootLogoCmdlineParts(t *testing.T) {
	got, conflicts := bootLogoCmdlineParts("earlyprintk=sunxi-uart console=ttyS0 partitions=boot-resource@mmcblk0p1:env@mmcblk0p2:broken:@x:boot@mmcblk0p4 root=/dev/mmcblk0p5")
	want := map[string]string{"boot-resource": "mmcblk0p1", "env": "mmcblk0p2", "boot": "mmcblk0p4"}
	if len(got) != len(want) || len(conflicts) != 0 {
		t.Fatalf("got %v, conflicts %v", got, conflicts)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q (all %v)", k, got[k], v, got)
		}
	}
	if parts, _ := bootLogoCmdlineParts("console=ttyS0 root=/dev/mmcblk0p5"); len(parts) != 0 {
		t.Fatal("a command line without partitions= named some")
	}
	// A name given twice: the same device is a repeat, another one a doubt.
	_, conflicts = bootLogoCmdlineParts("partitions=boot-resource@mmcblk0p1:boot-resource@mmcblk0p1:env@mmcblk0p2:env@mmcblk0p3")
	if conflicts["boot-resource"] || !conflicts["env"] {
		t.Fatalf("conflicts %v, want only env", conflicts)
	}
}

func TestBootLogoUeventMountsAndFlags(t *testing.T) {
	ev := bootLogoUevent("MAJOR=179\nMINOR=1\nDEVNAME=mmcblk0p1\nPARTNAME=boot-resource\n\n")
	if ev["DEVNAME"] != "mmcblk0p1" || ev["PARTNAME"] != "boot-resource" {
		t.Fatalf("uevent %v", ev)
	}
	mounts := bootLogoParseMounts("/dev/mmcblk0p1 /mnt/boot\\040res vfat ro,nosuid,relatime,fmask=0022 0 0\nproc /proc proc rw 0 0\nshort line\n")
	if len(mounts) != 2 {
		t.Fatalf("mounts %v", mounts)
	}
	m := mounts[0]
	if m.Source != "/dev/mmcblk0p1" || m.Target != "/mnt/boot res" || m.FSType != "vfat" || !bootLogoHasOption(m.Options, "ro") {
		t.Fatalf("mount %+v", m)
	}
	if got, want := bootLogoFlagsFor(m.Options), uintptr(bootLogoMSRdonly|bootLogoMSNosuid|bootLogoMSRelatime); got != want {
		t.Fatalf("flags %#x, want %#x", got, want)
	}
	if got := bootLogoUnescape(`a\134b\011c`); got != "a\\b\tc" {
		t.Fatalf("unescape %q", got)
	}
}

func TestBootLogoPartitionSources(t *testing.T) {
	root := bootLogoTestRoots(t)
	p1 := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)

	dev, ok, err := bootLogoPartition("boot-resource")
	if err != nil || !ok || !bootLogoSameDevice(dev, p1) {
		t.Fatalf("by-name + sysfs: %q %v %v", dev, ok, err)
	}
	if _, ok, err := bootLogoPartition("bootlogo"); ok || err != nil {
		t.Fatalf("an absent partition: %v %v", ok, err)
	}

	cmdline := filepath.Join(root, "proc", "cmdline")
	if err := os.WriteFile(cmdline, []byte("console=ttyS0 partitions=boot-resource@mmcblk0p1:env@mmcblk0p2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dev, ok, err := bootLogoPartition("boot-resource"); err != nil || !ok || !bootLogoSameDevice(dev, p1) {
		t.Fatalf("three sources agreeing: %q %v %v", dev, ok, err)
	}

	// The command line puts the name on another device: a doubt, not a pick.
	if err := os.WriteFile(filepath.Join(root, "dev", "mmcblk0p2"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cmdline, []byte("partitions=boot-resource@mmcblk0p2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bootLogoPartition("boot-resource"); !errors.Is(err, errBootLogoAmbiguous) {
		t.Fatalf("disagreeing sources: %v", err)
	}
	// And a device that does not exist is no store.
	if err := os.WriteFile(cmdline, []byte("partitions=boot-resource@mmcblk0p7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bootLogoPartition("boot-resource"); !errors.Is(err, errBootLogoNoStore) {
		t.Fatalf("a missing device: %v", err)
	}
	if bootLogoDevice("../etc/passwd") != "" || bootLogoDevice("") != "" {
		t.Fatal("a device name with a path in it was accepted")
	}

	// A by-name link that leaves the device root is ignored.
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(root, "dev", "by-name", "bootloader")); err != nil {
		t.Fatal(err)
	}
	if got := bootLogoByName("bootloader"); got != "" {
		t.Fatalf("a link out of /dev resolved to %q", got)
	}
	// A regular file stands in for a device here; it carries no device
	// number, so the guard on the system's own filesystems cannot match it.
	if bootLogoGuarded(p1) || bootLogoGuarded(filepath.Join(root, "missing")) {
		t.Fatal("the guard matched a file that holds no filesystem")
	}
}

func TestBootLogoRawStore(t *testing.T) {
	root := bootLogoTestRoots(t)
	stock := testStockLogo()
	path := addBootLogoPart(t, root, "mmcblk0p9", "bootlogo", stock, 1<<20)

	target, err := bootLogoOpenPlatform(false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer target.Close()
	if w := target.Where(); w.Kind != bootLogoKindRaw || w.Part != "bootlogo" {
		t.Fatalf("where %+v", w)
	}
	data, err := target.Read()
	if err != nil || !bytes.Equal(data, stock) {
		t.Fatalf("read %d bytes, %v", len(data), err)
	}
	info, err := bmpParse(stock)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := bmpPatch(stock, info, testGradient(info.Width, info.Height))
	if err != nil {
		t.Fatal(err)
	}
	if err := target.WriteInPlace(patched); err != nil {
		t.Fatalf("write: %v", err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(disk) != 1<<20 || !bytes.Equal(disk[:len(patched)], patched) || bytes.Count(disk[len(patched):], []byte{0}) != len(disk)-len(patched) {
		t.Fatal("the raw write did not land exactly over the BMP")
	}
	if err := target.WriteInPlace(patched[:100]); err == nil {
		t.Fatal("a write of another length was accepted")
	}
}

func TestBootLogoRawStoreTooLargeForItsPartition(t *testing.T) {
	root := bootLogoTestRoots(t)
	stock := testStockLogo()
	lying := append([]byte(nil), stock...)
	lying[2], lying[3], lying[4], lying[5] = 0, 0, 0x20, 0 // 2 MiB in a 1 MiB partition
	addBootLogoPart(t, root, "mmcblk0p9", "bootlogo", lying, 1<<20)
	if _, err := bootLogoOpenPlatform(false); !errors.Is(err, errBootLogoFormat) {
		t.Fatalf("err = %v", err)
	}
}

func TestBootLogoFATStoreThroughAPrivateMount(t *testing.T) {
	root := bootLogoTestRoots(t)
	stock := testStockLogo()
	fat := filepath.Join(root, "fatimg")
	if err := os.MkdirAll(fat, 0o755); err != nil {
		t.Fatal(err)
	}
	// FAT matches names without case: the stored name is kept.
	if err := os.WriteFile(filepath.Join(fat, "BOOTLOGO.BMP"), stock, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fat, "magic.bin"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	dev := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
	mounts := newFakeFATMounts()
	resolved, err := filepath.EvalSymlinks(dev)
	if err != nil {
		t.Fatal(err)
	}
	mounts.backing[resolved] = fat
	mounts.install(t)

	target, err := bootLogoOpenPlatform(true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if w := target.Where(); w.Kind != bootLogoKindFile || w.File != "BOOTLOGO.BMP" || w.Part != "boot-resource" {
		t.Fatalf("where %+v", w)
	}
	data, err := target.Read()
	if err != nil || !bytes.Equal(data, stock) {
		t.Fatalf("read: %v", err)
	}
	info, _ := bmpParse(stock)
	patched, _ := bmpPatch(stock, info, testGradient(info.Width, info.Height))
	if err := target.WriteInPlace(patched[:len(patched)-1]); err == nil {
		t.Fatal("a write that changes the file's length was accepted")
	}
	if err := target.WriteInPlace(patched); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(fat, "BOOTLOGO.BMP")); !bytes.Equal(got, patched) {
		t.Fatal("the write did not reach the filesystem")
	}
	if got, _ := os.ReadFile(filepath.Join(fat, "magic.bin")); string(got) != "keep me" {
		t.Fatal("another file on the partition changed")
	}
	if len(mounts.mounts) != 2 || mounts.mounts[0]&bootLogoMSRdonly == 0 || mounts.mounts[1]&bootLogoMSRdonly != 0 {
		t.Fatalf("mount flags %#x: want the look read-only and the write read-write", mounts.mounts)
	}
	if mounts.unmount != 2 || len(mounts.mounted) != 0 {
		t.Fatalf("%d unmounts, %d still mounted", mounts.unmount, len(mounts.mounted))
	}
	if left, _ := os.ReadDir(filepath.Join(root, "tmp")); len(left) != 0 {
		t.Fatalf("private mount folders left behind: %v", left)
	}
	if _, err := target.Read(); err == nil {
		t.Fatal("a closed store was read")
	}
}

func TestBootLogoFATStoreMountedReadOnlyBySystem(t *testing.T) {
	root := bootLogoTestRoots(t)
	stock := testStockLogo()
	dev := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
	sysmnt := filepath.Join(root, "sysmnt")
	if err := os.MkdirAll(sysmnt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysmnt, "bootlogo.bmp"), stock, 0o644); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("%s %s vfat ro,nosuid,relatime,fmask=0022 0 0\n", dev, sysmnt)
	if err := os.WriteFile(filepath.Join(root, "proc", "mounts"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	mounts := newFakeFATMounts()
	mounts.install(t)

	target, err := bootLogoOpenPlatform(true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if data, err := target.Read(); err != nil || !bytes.Equal(data, stock) {
		t.Fatalf("read: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(mounts.mounts) != 0 {
		t.Fatalf("a system mount was mounted again: %#x", mounts.mounts)
	}
	keep := uintptr(bootLogoMSNosuid | bootLogoMSRelatime | bootLogoMSRemount)
	if len(mounts.remount) != 2 || mounts.remount[0] != keep || mounts.remount[1] != keep|bootLogoMSRdonly {
		t.Fatalf("remounts %#x, want %#x then %#x", mounts.remount, keep, keep|bootLogoMSRdonly)
	}
}

func TestBootLogoDiscoveryRefusals(t *testing.T) {
	t.Run("nothing", func(t *testing.T) {
		bootLogoTestRoots(t)
		if _, err := bootLogoOpenPlatform(false); !errors.Is(err, errBootLogoNoStore) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("two stores", func(t *testing.T) {
		root := bootLogoTestRoots(t)
		stock := testStockLogo()
		fat := filepath.Join(root, "fatimg")
		if err := os.MkdirAll(fat, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fat, "bootlogo.bmp"), stock, 0o644); err != nil {
			t.Fatal(err)
		}
		dev := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
		addBootLogoPart(t, root, "mmcblk0p9", "bootlogo", stock, 1<<20)
		mounts := newFakeFATMounts()
		resolved, _ := filepath.EvalSymlinks(dev)
		mounts.backing[resolved] = fat
		mounts.install(t)
		_, err := bootLogoOpenPlatform(true)
		if !errors.Is(err, errBootLogoAmbiguous) {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "boot-resource") || !strings.Contains(err.Error(), "bootlogo") {
			t.Fatalf("the refusal does not name both stores: %v", err)
		}
	})
	t.Run("no bootlogo.bmp", func(t *testing.T) {
		root := bootLogoTestRoots(t)
		fat := filepath.Join(root, "fatimg")
		if err := os.MkdirAll(fat, 0o755); err != nil {
			t.Fatal(err)
		}
		dev := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
		mounts := newFakeFATMounts()
		resolved, _ := filepath.EvalSymlinks(dev)
		mounts.backing[resolved] = fat
		mounts.install(t)
		if _, err := bootLogoOpenPlatform(false); !errors.Is(err, errBootLogoNoStore) {
			t.Fatalf("err = %v", err)
		}
		if mounts.unmount != 1 || len(mounts.mounted) != 0 {
			t.Fatal("the look left the partition mounted")
		}
	})
	t.Run("a palette logo", func(t *testing.T) {
		root := bootLogoTestRoots(t)
		fat := filepath.Join(root, "fatimg")
		if err := os.MkdirAll(fat, 0o755); err != nil {
			t.Fatal(err)
		}
		pal := testBMP(testBMPSpec{w: 400, h: 300, bits: 8, header: 40})
		if err := os.WriteFile(filepath.Join(fat, "bootlogo.bmp"), pal, 0o644); err != nil {
			t.Fatal(err)
		}
		dev := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
		mounts := newFakeFATMounts()
		resolved, _ := filepath.EvalSymlinks(dev)
		mounts.backing[resolved] = fat
		mounts.install(t)
		_, err := bootLogoOpenPlatform(false)
		if !errors.Is(err, errBootLogoFormat) {
			t.Fatalf("err = %v", err)
		}
		if got := bootLogoReason(err); got != bootLogoSentence(errBootLogoFormat.Error()) {
			t.Fatalf("reason %q", got)
		}
	})
}

// bootLogoFakeBlock adds /sys/dev/block/<major>:<minor> for a device of the
// fake sysfs tree, the way the kernel lays a partition out inside its disk.
func bootLogoFakeBlock(t *testing.T, root string, major, minor uint32, disk, part, diskType string) {
	t.Helper()
	dir := filepath.Join(root, "sys", "devices", "host", disk)
	if part != "" {
		dir = filepath.Join(dir, part)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "partition"), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	links := filepath.Join(root, "sys", "dev", "block")
	if err := os.MkdirAll(links, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(links, fmt.Sprintf("%d:%d", major, minor))); err != nil {
		t.Fatal(err)
	}
	if diskType != "" {
		typeDir := filepath.Join(root, "sys", "block", disk, "device")
		if err := os.MkdirAll(typeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(typeDir, "type"), []byte(diskType+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Only a partition of the internal eMMC, on a disk JukaHub does not run from,
// is ever opened.
func TestBootLogoDiskGuard(t *testing.T) {
	root := bootLogoTestRoots(t)
	bootLogoFakeBlock(t, root, 179, 1, "mmcblk0", "mmcblk0p1", "MMC")
	bootLogoFakeBlock(t, root, 179, 2, "mmcblk0", "mmcblk0p2", "")
	bootLogoFakeBlock(t, root, 179, 65, "mmcblk1", "mmcblk1p1", "SD")
	bootLogoFakeBlock(t, root, 8, 1, "sda", "sda1", "")
	bootLogoFakeBlock(t, root, 7, 0, "loop0", "", "")
	emmc, sd := unix.Mkdev(179, 1), unix.Mkdev(179, 65)

	if err := bootLogoCheckDiskOf(emmc, []uint64{sd}); err != nil {
		t.Fatalf("an eMMC partition with the app on the card: %v", err)
	}
	refuse := map[string]struct {
		dev uint64
		app []uint64
	}{
		"a partition of the SD card":          {sd, nil},
		"a USB disk":                          {unix.Mkdev(8, 1), nil},
		"a loop device":                       {unix.Mkdev(7, 0), nil},
		"a device sysfs does not know":        {unix.Mkdev(179, 9), nil},
		"the disk JukaHub runs from":          {emmc, []uint64{unix.Mkdev(179, 2)}},
		"the app's own partition on the eMMC": {emmc, []uint64{emmc}},
	}
	for name, c := range refuse {
		if err := bootLogoCheckDiskOf(c.dev, c.app); !errors.Is(err, errBootLogoNoStore) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	// A regular file has no device number: refused, never guessed at.
	plain := filepath.Join(root, "dev", "plain")
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bootLogoCheckDisk(plain); !errors.Is(err, errBootLogoNoStore) {
		t.Errorf("a file without a device number: %v", err)
	}

	// Discovery honours the guard: a refused partition is never mounted.
	addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
	bootLogoDiskGuard = func(string) error { return fmt.Errorf("%w (test refusal)", errBootLogoNoStore) }
	_, err := bootLogoOpenPlatform(false)
	if !errors.Is(err, errBootLogoNoStore) || !strings.Contains(err.Error(), "test refusal") {
		t.Fatalf("err = %v", err)
	}
}

func TestBootLogoDiscoveryMoreRefusals(t *testing.T) {
	t.Run("a refused FAT logo beside a raw one", func(t *testing.T) {
		root := bootLogoTestRoots(t)
		fat := filepath.Join(root, "fatimg")
		if err := os.MkdirAll(fat, 0o755); err != nil {
			t.Fatal(err)
		}
		pal := testBMP(testBMPSpec{w: 400, h: 300, bits: 8, header: 40})
		if err := os.WriteFile(filepath.Join(fat, "bootlogo.bmp"), pal, 0o644); err != nil {
			t.Fatal(err)
		}
		dev := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
		addBootLogoPart(t, root, "mmcblk0p9", "bootlogo", testStockLogo(), 1<<20)
		mounts := newFakeFATMounts()
		resolved, _ := filepath.EvalSymlinks(dev)
		mounts.backing[resolved] = fat
		mounts.install(t)
		// The raw store alone is acceptable, but the bootloader may be
		// showing the FAT one: two logos are a doubt.
		if _, err := bootLogoOpenPlatform(false); !errors.Is(err, errBootLogoAmbiguous) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a sysfs name without a device", func(t *testing.T) {
		root := bootLogoTestRoots(t)
		sys := filepath.Join(root, "sys", "class", "block", "mmcblk0p3")
		if err := os.MkdirAll(sys, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sys, "uevent"), []byte("DEVNAME=mmcblk0p3\nPARTNAME=bootlogo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := bootLogoPartition("bootlogo"); !errors.Is(err, errBootLogoNoStore) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a name on two devices in the command line", func(t *testing.T) {
		root := bootLogoTestRoots(t)
		addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
		if err := os.WriteFile(filepath.Join(root, "proc", "cmdline"), []byte("partitions=boot-resource@mmcblk0p1:boot-resource@mmcblk0p2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := bootLogoPartition("boot-resource"); !errors.Is(err, errBootLogoAmbiguous) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a system mount that holds another filesystem", func(t *testing.T) {
		root := bootLogoTestRoots(t)
		dev := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
		sysmnt := filepath.Join(root, "sysmnt")
		if err := os.MkdirAll(sysmnt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sysmnt, "bootlogo.bmp"), testStockLogo(), 0o644); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf("%s %s vfat rw,relatime 0 0\n", dev, sysmnt)
		if err := os.WriteFile(filepath.Join(root, "proc", "mounts"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
		bootLogoSameFS = func(string, *os.File, string) bool { return false }
		if _, err := bootLogoOpenPlatform(true); !errors.Is(err, errBootLogoNoStore) {
			t.Fatalf("err = %v", err)
		}
	})
}

// A private mount left by a JukaHub killed mid-operation is unmounted and its
// folder removed before the next look, and never taken for the system's.
func TestBootLogoSweepsStaleMounts(t *testing.T) {
	root := bootLogoTestRoots(t)
	dev := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
	stale := filepath.Join(root, "tmp", bootLogoPrivatePrefix+"12345")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "tmp", "someone-else")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := fmt.Sprintf("%s %s vfat rw 0 0\n%s %s vfat rw 0 0\n", dev, stale, dev, other)
	if err := os.WriteFile(filepath.Join(root, "proc", "mounts"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, ok := bootLogoFindMount(dev); !ok || m.Target != other {
		t.Fatalf("found %+v %v, want the other mount and never the stale private one", m, ok)
	}
	var unmounted []string
	bootLogoUnmount = func(target string, _ int) error {
		unmounted = append(unmounted, target)
		return nil
	}
	bootLogoSweepStaleMounts()
	if len(unmounted) != 1 || unmounted[0] != stale {
		t.Fatalf("unmounted %v, want only %s", unmounted, stale)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("the stale mount's folder is still there")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("a folder that is not JukaHub's was removed")
	}
}

// An install on a store the system mounted read-only puts the mount back to
// read-only; when the partition stays busy it says so instead of plain success.
func TestBootLogoInstallReportsAPartitionLeftWritable(t *testing.T) {
	root := bootLogoTestRoots(t)
	stock := testStockLogo()
	dev := addBootLogoPart(t, root, "mmcblk0p1", "boot-resource", nil, 4096)
	sysmnt := filepath.Join(root, "sysmnt")
	if err := os.MkdirAll(sysmnt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysmnt, "bootlogo.bmp"), stock, 0o644); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("%s %s vfat ro,relatime 0 0\n", dev, sysmnt)
	if err := os.WriteFile(filepath.Join(root, "proc", "mounts"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	mounts := newFakeFATMounts()
	mounts.failRO = 3
	mounts.install(t)
	oldSupported, oldData, oldRoot := bootLogoSupportedFn, bootLogoDataDirFn, bootLogoSDRootFn
	t.Cleanup(func() { bootLogoSupportedFn, bootLogoDataDirFn, bootLogoSDRootFn = oldSupported, oldData, oldRoot })
	bootLogoSupportedFn = func() bool { return true }
	bootLogoDataDirFn = func() (string, error) { return filepath.Join(root, "data", "bootlogo"), nil }
	bootLogoSDRootFn = func() string { return filepath.Join(root, "sd") }

	msg, err := bootLogoInstall(testArtwork())
	var warn *bootLogoWarning
	if !errors.As(err, &warn) || !strings.Contains(warn.msg, "writable") || !strings.Contains(msg, "installed") {
		t.Fatalf("install: %q, %v", msg, err)
	}
	got, _ := os.ReadFile(filepath.Join(sysmnt, "bootlogo.bmp"))
	if len(got) != len(stock) || bytes.Equal(got, stock) {
		t.Fatal("the logo was not written")
	}
	if st := bootLogoLoadState(); !st.Installed || st.Interrupted {
		t.Fatalf("state %+v", st)
	}
	// One remount to write, then three tries to put read-only back.
	if len(mounts.remount) != 4 || mounts.remount[0]&bootLogoMSRdonly != 0 {
		t.Fatalf("remounts %#x", mounts.remount)
	}
	for _, f := range mounts.remount[1:] {
		if f&bootLogoMSRdonly == 0 {
			t.Fatalf("remounts %#x: the tries after the write must be read-only", mounts.remount)
		}
	}
	if len(mounts.mounts) != 0 {
		t.Fatalf("a system mount was mounted again: %#x", mounts.mounts)
	}
}
