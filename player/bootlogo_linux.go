//go:build linux

package main

// bootlogo_linux.go — finding and opening the firmware's boot logo.
//
// Allwinner's firmware (the Tina Linux the TrimUI handhelds run) loads the boot
// logo in U-Boot from a small FAT partition named boot-resource (bootloader on
// some boards) as bootlogo.bmp; a few boards keep it in a raw partition named
// bootlogo instead, which is just the BMP's bytes. Both are found here by
// their NAME, from every source that names partitions:
//
//   - /dev/by-name/<name>, the links Tina's init (and udev) create;
//   - the kernel command line's partitions=name@mmcblk0p1:… list, which the
//     Allwinner bootloader writes;
//   - PARTNAME= in /sys/class/block/*/uevent, from a GPT.
//
// Every source that answers must name the same block device, and a name no
// source knows is simply absent. A partition number is never assumed. A
// partition is only ever opened when it sits on the handheld's internal eMMC
// (sysfs says "MMC", not "SD"), not on the disk JukaHub itself runs from, and
// does not hold the running system.
//
// A FAT store is mounted read-only to look and read-write only for a write,
// under a private folder in /tmp, and always unmounted again; a store the
// system has mounted already is used where it is — after checking that the
// folder really holds that partition — and remounted writable for the write
// and read-only again after, if that is how it was found. The logo is
// rewritten in place: opened without O_TRUNC or O_CREATE, the same length
// written over it, synced. A raw store is written at offset 0 over exactly the
// BMP's own length, through an exclusive open. Before every read the kernel is
// asked to drop what it has cached of the file or the device, so a read-back
// comes from the storage, not from memory.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The Linux mount flags and ioctl used here. They are part of the kernel ABI
// and the same on every architecture, so they are spelled out instead of
// depending on which of them the frozen syscall package happens to export.
const (
	bootLogoMSRdonly      = 0x1
	bootLogoMSNosuid      = 0x2
	bootLogoMSNodev       = 0x4
	bootLogoMSNoexec      = 0x8
	bootLogoMSSync        = 0x10
	bootLogoMSRemount     = 0x20
	bootLogoMSMandlock    = 0x40
	bootLogoMSDirsync     = 0x80
	bootLogoMSNosymfollow = 0x100
	bootLogoMSNoatime     = 0x400
	bootLogoMSNodiratime  = 0x800
	bootLogoMSRelatime    = 0x200000
	bootLogoMSStrictatime = 0x1000000
	bootLogoMSLazytime    = 0x2000000
	bootLogoMNTDetach     = 0x2
	bootLogoBLKFLSBUF     = 0x1261 // _IO(0x12, 97): flush a block device's buffers
)

// bootLogoFATNames are the FAT partitions the logo is loaded from;
// bootLogoRawName is the raw variant.
var bootLogoFATNames = []string{"boot-resource", "bootloader"}

const bootLogoRawName = "bootlogo"

// bootLogoPrivatePrefix names the folders private mounts are made in.
const bootLogoPrivatePrefix = "jukahub-bootlogo-"

// errBootLogoStillWritable is a close that could not put a system mount back
// to read-only: the write stands, and the user is told what was left.
var errBootLogoStillWritable = errors.New("the boot partition stayed writable")

// Overridable for tests: the roots the discovery reads, where private mounts
// go, the mount calls, and what counts as a block device.
var (
	bootLogoProcRoot  = "/proc"
	bootLogoSysRoot   = "/sys"
	bootLogoDevRoot   = "/dev"
	bootLogoMountBase = ""
	bootLogoMount     = syscall.Mount
	bootLogoUnmount   = syscall.Unmount
	bootLogoIsBlock   = func(fi os.FileInfo) bool {
		return fi.Mode()&os.ModeDevice != 0 && fi.Mode()&os.ModeCharDevice == 0
	}
)

// More test seams: the disk guard, the check that a mount or an open file
// really is on the partition, the paths whose disk is never touched, and the
// pause between attempts to make a mount read-only again.
var (
	bootLogoDiskGuard    = bootLogoCheckDisk
	bootLogoSameFS       = bootLogoSameFSDefault
	bootLogoAppPaths     = bootLogoAppPathsDefault
	bootLogoRemountPause = 200 * time.Millisecond
)

func bootLogoPlatformSupported() bool { return true }

// ── Partitions by name ───────────────────────────────────────────────────

// bootLogoDevice returns the path of the block device called dev (a kernel
// name such as mmcblk0p1) under the device root, or "" when it is not one.
func bootLogoDevice(dev string) string {
	if dev == "" || strings.ContainsAny(dev, "/\\") || dev == "." || dev == ".." {
		return ""
	}
	path := filepath.Join(bootLogoDevRoot, dev)
	fi, err := os.Stat(path)
	if err != nil || !bootLogoIsBlock(fi) {
		return ""
	}
	return path
}

// bootLogoByName resolves /dev/by-name/<name>. The link must lead to a block
// device inside the device root.
func bootLogoByName(name string) string {
	resolved, err := filepath.EvalSymlinks(filepath.Join(bootLogoDevRoot, "by-name", name))
	if err != nil {
		return ""
	}
	root, err := filepath.EvalSymlinks(bootLogoDevRoot)
	if err != nil {
		root = bootLogoDevRoot
	}
	if !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return ""
	}
	fi, err := os.Stat(resolved)
	if err != nil || !bootLogoIsBlock(fi) {
		return ""
	}
	return resolved
}

// bootLogoCmdlineParts parses the partitions= list of a kernel command line
// ("boot-resource@mmcblk0p1:env@mmcblk0p2:…") into name → device, and the
// names it gives two different devices (a doubt, never a pick).
func bootLogoCmdlineParts(cmdline string) (map[string]string, map[string]bool) {
	parts := map[string]string{}
	conflicts := map[string]bool{}
	for _, field := range strings.Fields(cmdline) {
		value, ok := strings.CutPrefix(field, "partitions=")
		if !ok {
			continue
		}
		for _, entry := range strings.Split(value, ":") {
			name, dev, ok := strings.Cut(entry, "@")
			if !ok || name == "" || dev == "" {
				continue
			}
			if prev, seen := parts[name]; seen && prev != dev {
				conflicts[name] = true
			}
			parts[name] = dev
		}
	}
	return parts, conflicts
}

// bootLogoUevent parses a sysfs uevent file's KEY=VALUE lines.
func bootLogoUevent(data string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key != "" {
			out[key] = value
		}
	}
	return out
}

// bootLogoSysfsParts lists the devices whose GPT name is name. A name sysfs
// knows with no block device to match it is an error, not a skipped entry: the
// store is never guessed at.
func bootLogoSysfsParts(name string) ([]string, error) {
	dir := filepath.Join(bootLogoSysRoot, "class", "block")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var out []string
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "uevent"))
		if err != nil {
			continue
		}
		ev := bootLogoUevent(string(data))
		if ev["PARTNAME"] != name {
			continue
		}
		dev := ev["DEVNAME"]
		if dev == "" {
			dev = e.Name()
		}
		path := bootLogoDevice(dev)
		if path == "" {
			return nil, fmt.Errorf("%w (sysfs names %s %q, but there is no block device for it)", errBootLogoNoStore, dev, name)
		}
		out = append(out, path)
	}
	return out, nil
}

// bootLogoSameDevice reports whether two paths are the same block device.
func bootLogoSameDevice(a, b string) bool {
	if a == b {
		return true
	}
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	sa, okA := fa.Sys().(*syscall.Stat_t)
	sb, okB := fb.Sys().(*syscall.Stat_t)
	if okA && okB && sa.Rdev != 0 && sb.Rdev != 0 {
		return sa.Rdev == sb.Rdev
	}
	return os.SameFile(fa, fb)
}

// bootLogoGuardPaths are files whose filesystems are never a boot logo store:
// the running system's root and the SD card JukaHub runs from. A command line
// that numbered the eMMC and the card differently from the kernel would
// otherwise point a partition name at the card.
var bootLogoGuardPaths = func() []string {
	paths := []string{"/"}
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, exe)
	}
	return paths
}

// bootLogoGuarded reports whether dev holds one of the guarded filesystems.
func bootLogoGuarded(dev string) bool {
	fi, err := os.Stat(dev)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Rdev == 0 {
		return false
	}
	for _, p := range bootLogoGuardPaths() {
		pi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if ps, ok := pi.Sys().(*syscall.Stat_t); ok && uint64(ps.Dev) == uint64(st.Rdev) {
			return true
		}
	}
	return false
}

// bootLogoRdev is the device number of the block device at path.
func bootLogoRdev(path string) (uint64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Rdev == 0 {
		return 0, false
	}
	return uint64(st.Rdev), true
}

// bootLogoFSDev is the device number of the filesystem holding path.
func bootLogoFSDev(path string) (uint64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

// bootLogoDiskOf names the disk a block device number belongs to (mmcblk0 for
// mmcblk0p1), through /sys/dev/block/<major>:<minor>: a partition's folder
// holds a "partition" file and sits inside its disk's folder.
func bootLogoDiskOf(rdev uint64) (string, error) {
	link := filepath.Join(bootLogoSysRoot, "dev", "block", fmt.Sprintf("%d:%d", unix.Major(rdev), unix.Minor(rdev)))
	dir, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, "partition")); err == nil {
		dir = filepath.Dir(dir)
	}
	return filepath.Base(dir), nil
}

// bootLogoCheckDiskOf refuses the partition with device number rdev unless it
// is on the handheld's internal eMMC — never a card, a USB disk, a loop file
// or a mapped device — and shares that disk with none of app (the filesystems
// JukaHub runs from).
func bootLogoCheckDiskOf(rdev uint64, app []uint64) error {
	disk, err := bootLogoDiskOf(rdev)
	if err != nil {
		return fmt.Errorf("%w (no disk found for device %d:%d: %v)", errBootLogoNoStore, unix.Major(rdev), unix.Minor(rdev), err)
	}
	if !strings.HasPrefix(disk, "mmcblk") {
		return fmt.Errorf("%w (%s is not the internal eMMC)", errBootLogoNoStore, disk)
	}
	kind := ""
	if data, err := os.ReadFile(filepath.Join(bootLogoSysRoot, "block", disk, "device", "type")); err == nil {
		kind = strings.TrimSpace(string(data))
	}
	if kind != "MMC" {
		return fmt.Errorf("%w (%s is a %q device, not the internal eMMC)", errBootLogoNoStore, disk, kind)
	}
	for _, d := range app {
		if other, err := bootLogoDiskOf(d); err == nil && other == disk {
			return fmt.Errorf("%w (%s also holds JukaHub's own files)", errBootLogoNoStore, disk)
		}
	}
	return nil
}

// bootLogoCheckDisk is bootLogoCheckDiskOf for the block device at dev, against
// the disks of the paths JukaHub runs from.
func bootLogoCheckDisk(dev string) error {
	rdev, ok := bootLogoRdev(dev)
	if !ok {
		return fmt.Errorf("%w (%s has no device number)", errBootLogoNoStore, dev)
	}
	var app []uint64
	for _, p := range bootLogoAppPaths() {
		if d, ok := bootLogoFSDev(p); ok && d != 0 {
			app = append(app, d)
		}
	}
	return bootLogoCheckDiskOf(rdev, app)
}

// bootLogoAppPathsDefault are the paths whose disk is JukaHub's own: the
// executable, the SD card's root and the folder the backups go to.
func bootLogoAppPathsDefault() []string {
	var paths []string
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, exe)
	}
	if root := strings.TrimSpace(bootLogoSDRootFn()); root != "" {
		paths = append(paths, root)
	}
	if dir, err := bootLogoDataDirFn(); err == nil {
		paths = append(paths, dir, filepath.Dir(dir))
	}
	return paths
}

// bootLogoSameFSDefault reports whether the filesystem holding the open file f
// (or path, when f is nil) is the block device dev.
func bootLogoSameFSDefault(path string, f *os.File, dev string) bool {
	want, ok := bootLogoRdev(dev)
	if !ok {
		return false
	}
	var fi os.FileInfo
	var err error
	if f != nil {
		fi, err = f.Stat()
	} else {
		fi, err = os.Stat(path)
	}
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && uint64(st.Dev) == want
}

// bootLogoRefuseDevice is why the partition called name, on dev, is never
// opened, or "": it holds the running system or the SD card, or it is not on
// the internal eMMC.
func bootLogoRefuseDevice(name, dev string) string {
	if bootLogoGuarded(dev) {
		return fmt.Sprintf("%s (%s) holds the running system or the SD card; not touched", name, dev)
	}
	if err := bootLogoDiskGuard(dev); err != nil {
		return fmt.Sprintf("%s (%s): %v", name, dev, err)
	}
	return ""
}

// bootLogoPartition finds the block device of the partition called name. ok is
// false when no source names it; an error means the sources disagree.
func bootLogoPartition(name string) (dev string, ok bool, err error) {
	type answer struct{ from, dev string }
	var answers []answer
	if d := bootLogoByName(name); d != "" {
		answers = append(answers, answer{"/dev/by-name", d})
	}
	if data, rerr := os.ReadFile(filepath.Join(bootLogoProcRoot, "cmdline")); rerr == nil {
		parts, conflicts := bootLogoCmdlineParts(string(data))
		if conflicts[name] {
			return "", false, fmt.Errorf("%w (the kernel command line puts %q on two devices)", errBootLogoAmbiguous, name)
		}
		if kdev, found := parts[name]; found {
			d := bootLogoDevice(kdev)
			if d == "" {
				return "", false, fmt.Errorf("%w (the kernel command line names %s for %q, which is no block device)", errBootLogoNoStore, kdev, name)
			}
			answers = append(answers, answer{"the kernel command line", d})
		}
	}
	sysfs, serr := bootLogoSysfsParts(name)
	if serr != nil {
		return "", false, serr
	}
	for _, d := range sysfs {
		answers = append(answers, answer{"sysfs", d})
	}
	if len(answers) == 0 {
		return "", false, nil
	}
	for _, a := range answers[1:] {
		if !bootLogoSameDevice(answers[0].dev, a.dev) {
			return "", false, fmt.Errorf("%w (%s puts %q on %s, %s on %s)", errBootLogoAmbiguous, answers[0].from, name, answers[0].dev, a.from, a.dev)
		}
	}
	return answers[0].dev, true, nil
}

// ── Mounts ───────────────────────────────────────────────────────────────

// bootLogoMountEntry is one line of /proc/mounts.
type bootLogoMountEntry struct {
	Source  string
	Target  string
	FSType  string
	Options []string
}

// bootLogoUnescape decodes the octal escapes /proc/mounts uses for spaces,
// tabs, newlines and backslashes in paths.
func bootLogoUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// bootLogoParseMounts parses /proc/mounts.
func bootLogoParseMounts(data string) []bootLogoMountEntry {
	var out []bootLogoMountEntry
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		out = append(out, bootLogoMountEntry{
			Source:  bootLogoUnescape(f[0]),
			Target:  bootLogoUnescape(f[1]),
			FSType:  f[2],
			Options: strings.Split(f[3], ","),
		})
	}
	return out
}

// bootLogoPrivateMountDir reports whether a mount point is one of JukaHub's
// own: a folder os.MkdirTemp made for it under the mount base.
func bootLogoPrivateMountDir(target string) bool {
	base := bootLogoMountBase
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Dir(filepath.Clean(target)) == filepath.Clean(base) &&
		strings.HasPrefix(filepath.Base(target), bootLogoPrivatePrefix)
}

// bootLogoSweepStaleMounts unmounts private mounts a JukaHub killed in the
// middle of an operation left behind, so none is mistaken for the system's
// or left holding the partition writable.
func bootLogoSweepStaleMounts() {
	data, err := os.ReadFile(filepath.Join(bootLogoProcRoot, "mounts"))
	if err != nil {
		return
	}
	for _, m := range bootLogoParseMounts(string(data)) {
		if !bootLogoPrivateMountDir(m.Target) {
			continue
		}
		if err := bootLogoUnmountDir(m.Target); err != nil {
			logWarn("bootlogo", "a stale mount at %s: %v", m.Target, err)
			continue
		}
		logInfo("bootlogo", "removed a stale mount at %s", m.Target)
	}
}

// bootLogoFindMount finds where the system has mounted dev already. JukaHub's
// own private mounts never count (bootLogoSweepStaleMounts removes them).
func bootLogoFindMount(dev string) (bootLogoMountEntry, bool) {
	data, err := os.ReadFile(filepath.Join(bootLogoProcRoot, "mounts"))
	if err != nil {
		return bootLogoMountEntry{}, false
	}
	for _, m := range bootLogoParseMounts(string(data)) {
		if bootLogoPrivateMountDir(m.Target) {
			continue
		}
		if strings.HasPrefix(m.Source, "/") && bootLogoSameDevice(m.Source, dev) {
			return m, true
		}
	}
	return bootLogoMountEntry{}, false
}

// bootLogoHasOption reports whether a mount carries an option.
func bootLogoHasOption(options []string, want string) bool {
	for _, o := range options {
		if o == want {
			return true
		}
	}
	return false
}

// bootLogoFlagsFor maps a mount's options to the per-mount flags a remount
// must repeat, or the kernel would clear them.
func bootLogoFlagsFor(options []string) uintptr {
	var flags uintptr
	for _, o := range options {
		switch o {
		case "ro":
			flags |= bootLogoMSRdonly
		case "nosuid":
			flags |= bootLogoMSNosuid
		case "nodev":
			flags |= bootLogoMSNodev
		case "noexec":
			flags |= bootLogoMSNoexec
		case "sync":
			flags |= bootLogoMSSync
		case "mand":
			flags |= bootLogoMSMandlock
		case "dirsync":
			flags |= bootLogoMSDirsync
		case "nosymfollow":
			flags |= bootLogoMSNosymfollow
		case "noatime":
			flags |= bootLogoMSNoatime
		case "nodiratime":
			flags |= bootLogoMSNodiratime
		case "relatime":
			flags |= bootLogoMSRelatime
		case "strictatime":
			flags |= bootLogoMSStrictatime
		case "lazytime":
			flags |= bootLogoMSLazytime
		}
	}
	return flags
}

// bootLogoUnmountDir unmounts a private mount and removes its folder. A busy
// mount is detached instead, which the kernel finishes once nothing holds it.
func bootLogoUnmountDir(dir string) error {
	err := bootLogoUnmount(dir, 0)
	if err != nil {
		if derr := bootLogoUnmount(dir, bootLogoMNTDetach); derr != nil {
			return fmt.Errorf("unmount %s: %v", dir, err)
		}
	}
	_ = os.Remove(dir)
	return nil
}

// bootLogoOpenNoAtime opens a file to read without touching its access time —
// a read through a writable mount would otherwise write to the partition.
// O_NOATIME needs the file's owner, so it falls back to a plain open.
func bootLogoOpenNoAtime(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOATIME, 0)
	if errors.Is(err, syscall.EPERM) {
		f, err = os.Open(path)
	}
	return f, err
}

// bootLogoDropCache asks the kernel to forget what it has cached of an open
// file or device (clean after a sync), so the next read comes from the
// storage. Where that cannot be done the cache stays: the check is weaker,
// the write is not.
func bootLogoDropCache(f *os.File) {
	err := unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
	if err != nil && !errors.Is(err, syscall.ENOSYS) && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ESPIPE) {
		logDebug("bootlogo", "dropping the cached pages of %s: %v", f.Name(), err)
	}
}

// ── The FAT store ────────────────────────────────────────────────────────

// bootLogoFATTarget is bootlogo.bmp on a mounted FAT partition.
type bootLogoFATTarget struct {
	where     bootLogoWhere
	mountDir  string
	path      string
	ownMount  bool    // mounted here, unmounted on Close
	system    bool    // the system's mount: checked to hold the partition before every use
	remounted uintptr // non-zero: an existing read-only mount made writable; these flags put it back
	closed    bool
}

func (t *bootLogoFATTarget) Where() bootLogoWhere { return t.where }

// checkMount makes sure a mount the system made still holds the partition: a
// folder named in /proc/mounts can have been mounted over since.
func (t *bootLogoFATTarget) checkMount() error {
	if t.system && !bootLogoSameFS(t.mountDir, nil, t.where.Device) {
		return fmt.Errorf("%w (%s no longer holds %s)", errBootLogoNoStore, t.mountDir, t.where.Device)
	}
	return nil
}

// checkFile makes sure an open file on the system's mount is on the partition.
func (t *bootLogoFATTarget) checkFile(f *os.File) error {
	if t.system && !bootLogoSameFS("", f, t.where.Device) {
		return fmt.Errorf("%w (%s is not on %s)", errBootLogoNoStore, t.path, t.where.Device)
	}
	return nil
}

func (t *bootLogoFATTarget) Read() ([]byte, error) {
	if t.closed {
		return nil, errBootLogoClosed
	}
	if err := t.checkMount(); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(t.path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", t.where.File)
	}
	if fi.Size() > bootLogoMaxBytes {
		return nil, fmt.Errorf("%w (%s is %d bytes)", errBootLogoFormat, t.where.File, fi.Size())
	}
	f, err := bootLogoOpenNoAtime(t.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := t.checkFile(f); err != nil {
		return nil, err
	}
	bootLogoDropCache(f)
	// One byte over the limit is enough to see that the file grew past it.
	return io.ReadAll(io.LimitReader(f, bootLogoMaxBytes+1))
}

func (t *bootLogoFATTarget) WriteInPlace(data []byte) error {
	if t.closed {
		return errBootLogoClosed
	}
	if err := t.checkMount(); err != nil {
		return err
	}
	fi, err := os.Lstat(t.path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() != int64(len(data)) {
		return fmt.Errorf("refusing a write that would change %s's length (%d → %d bytes)", t.where.File, fi.Size(), len(data))
	}
	// No O_CREATE and no O_TRUNC: the file keeps its clusters, so the
	// allocation table is not touched. Besides the data, what changes is the
	// directory entry's modification time and archive bit — and, for as long
	// as the partition is mounted writable, the FAT driver's dirty flag in
	// the boot sector, which it clears again on the unmount or the remount
	// back to read-only.
	f, err := os.OpenFile(t.path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if err := t.checkFile(f); err != nil {
		_ = f.Close()
		return err
	}
	n, err := f.WriteAt(data, 0)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if err == nil {
		bootLogoDropCache(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (t *bootLogoFATTarget) Close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	switch {
	case t.ownMount:
		return bootLogoUnmountDir(t.mountDir)
	case t.remounted != 0:
		// A busy partition refuses the remount for a moment: try three
		// times, then say so — the write stands, but the user should know the
		// partition stays writable until the next restart.
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(bootLogoRemountPause)
			}
			syscall.Sync()
			if err = bootLogoMount("", t.mountDir, "", t.remounted, ""); err == nil {
				return nil
			}
		}
		return fmt.Errorf("%w (could not make %s read-only again: %v)", errBootLogoStillWritable, t.mountDir, err)
	}
	return nil
}

// bootLogoFindFile returns the stored name of want in dir, matched without
// regard to case the way FAT itself matches names.
func bootLogoFindFile(dir, want string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !strings.EqualFold(e.Name(), want) {
			continue
		}
		if !e.Type().IsRegular() {
			return "", fmt.Errorf("%s is not a file", e.Name())
		}
		return e.Name(), nil
	}
	return "", fmt.Errorf("%w (no %s on it)", errBootLogoNoStore, want)
}

// bootLogoOpenFAT opens bootlogo.bmp on the FAT partition dev: where the system
// mounted it, or on a private mount (read-only unless write).
func bootLogoOpenFAT(dev, part string, write bool) (*bootLogoFATTarget, error) {
	t := &bootLogoFATTarget{where: bootLogoWhere{Kind: bootLogoKindFile, Device: dev, Part: part}}
	if m, ok := bootLogoFindMount(dev); ok {
		if m.FSType != "vfat" && m.FSType != "msdos" {
			return nil, fmt.Errorf("%w (%s is mounted as %s, not FAT)", errBootLogoNoStore, part, m.FSType)
		}
		// /proc/mounts names a folder, not what is mounted on it now.
		if !bootLogoSameFS(m.Target, nil, dev) {
			return nil, fmt.Errorf("%w (%s, where %s is mounted, holds another filesystem)", errBootLogoNoStore, m.Target, part)
		}
		t.mountDir, t.system = m.Target, true
		if write && bootLogoHasOption(m.Options, "ro") {
			flags := bootLogoFlagsFor(m.Options)
			rw := (flags &^ bootLogoMSRdonly) | bootLogoMSRemount
			if err := bootLogoMount(m.Source, m.Target, m.FSType, rw, ""); err != nil {
				return nil, fmt.Errorf("could not make %s writable: %w", part, err)
			}
			t.remounted = flags | bootLogoMSRdonly | bootLogoMSRemount
		}
	} else {
		dir, err := os.MkdirTemp(bootLogoMountBase, bootLogoPrivatePrefix)
		if err != nil {
			return nil, err
		}
		flags := uintptr(bootLogoMSNosuid | bootLogoMSNodev | bootLogoMSNoexec)
		if write {
			flags |= bootLogoMSNoatime
		} else {
			flags |= bootLogoMSRdonly
		}
		if err := bootLogoMount(dev, dir, "vfat", flags, ""); err != nil {
			_ = os.Remove(dir)
			return nil, fmt.Errorf("%w (%s would not mount as FAT: %v)", errBootLogoNoStore, part, err)
		}
		t.mountDir, t.ownMount = dir, true
	}
	name, err := bootLogoFindFile(t.mountDir, bootLogoFileName)
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("%s: %w", part, err)
	}
	t.where.File = name
	t.path = filepath.Join(t.mountDir, name)
	return t, nil
}

// ── The raw store ────────────────────────────────────────────────────────

// bootLogoRawTarget is a BMP written straight into a partition.
type bootLogoRawTarget struct {
	where  bootLogoWhere
	length int // the BMP's length, from its own size field
}

// bootLogoPartSize is a block device's size in bytes, from sysfs.
func bootLogoPartSize(dev string) (int64, error) {
	resolved, err := filepath.EvalSymlinks(dev)
	if err != nil {
		resolved = dev
	}
	data, err := os.ReadFile(filepath.Join(bootLogoSysRoot, "class", "block", filepath.Base(resolved), "size"))
	if err != nil {
		return 0, err
	}
	sectors, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || sectors <= 0 {
		return 0, fmt.Errorf("no size for %s", dev)
	}
	return sectors * 512, nil
}

// bootLogoFlushBlock drops a block device's cached buffers so the next read
// comes from the device. Best-effort: a regular file (the tests) refuses it.
func bootLogoFlushBlock(f *os.File) {
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), bootLogoBLKFLSBUF, 0)
}

// bootLogoOpenRaw checks that the partition dev starts with a BMP whose size
// field fits it, and opens it.
func bootLogoOpenRaw(dev, part string) (*bootLogoRawTarget, error) {
	size, err := bootLogoPartSize(dev)
	if err != nil {
		return nil, fmt.Errorf("%w (%s has no readable size: %v)", errBootLogoNoStore, part, err)
	}
	f, err := os.Open(dev)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hdr := make([]byte, 14)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, err
	}
	if hdr[0] != 'B' || hdr[1] != 'M' {
		return nil, fmt.Errorf("%w (the %s partition does not start with a BMP)", errBootLogoNoStore, part)
	}
	length := int(binary.LittleEndian.Uint32(hdr[2:6]))
	if length < 14+40 || int64(length) > size || length > bootLogoMaxBytes {
		return nil, fmt.Errorf("%w (the BMP on %s claims %d bytes in a %d-byte partition)", errBootLogoFormat, part, length, size)
	}
	return &bootLogoRawTarget{where: bootLogoWhere{Kind: bootLogoKindRaw, Device: dev, Part: part}, length: length}, nil
}

func (t *bootLogoRawTarget) Where() bootLogoWhere { return t.where }

func (t *bootLogoRawTarget) Read() ([]byte, error) {
	f, err := os.Open(t.where.Device)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	bootLogoFlushBlock(f)
	bootLogoDropCache(f)
	buf := make([]byte, t.length)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (t *bootLogoRawTarget) WriteInPlace(data []byte) error {
	if len(data) != t.length {
		return fmt.Errorf("refusing a write that would change the logo's length (%d → %d bytes)", t.length, len(data))
	}
	// O_EXCL on a block device is an exclusive open: it fails while anything
	// has the partition mounted.
	f, err := os.OpenFile(t.where.Device, os.O_WRONLY|os.O_SYNC|os.O_EXCL, 0)
	if err != nil {
		return err
	}
	n, err := f.WriteAt(data, 0)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if err == nil {
		bootLogoDropCache(f)
	}
	bootLogoFlushBlock(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (t *bootLogoRawTarget) Close() error { return nil }

// ── Discovery ────────────────────────────────────────────────────────────

// bootLogoOpenPlatform finds the firmware's boot logo and opens it for one
// operation (write=false mounts a FAT store read-only). It looks at every
// store the names allow and opens one only when exactly one of them holds a
// BMP at all and that BMP is one the installer accepts: a second store with a
// logo in it — even one JukaHub would refuse — means nobody can say which of
// the two the bootloader shows.
func bootLogoOpenPlatform(write bool) (bootLogoTarget, error) {
	bootLogoSweepStaleMounts()
	type store struct{ kind, part, dev string }
	var found, holders []store
	var notes []string
	for _, name := range bootLogoFATNames {
		dev, ok, err := bootLogoPartition(name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if reason := bootLogoRefuseDevice(name, dev); reason != "" {
			notes = append(notes, reason)
			continue
		}
		t, err := bootLogoOpenFAT(dev, name, false)
		if err != nil {
			notes = append(notes, err.Error())
			continue
		}
		data, rerr := t.Read()
		cerr := t.Close()
		s := store{bootLogoKindFile, name, dev}
		switch {
		case rerr != nil:
			if errors.Is(rerr, errBootLogoFormat) {
				// A bootlogo.bmp too large to read is a logo there all the same.
				holders = append(holders, s)
			}
			notes = append(notes, fmt.Sprintf("%s: %v", name, rerr))
		case cerr != nil:
			return nil, cerr
		default:
			if len(data) >= 2 && data[0] == 'B' && data[1] == 'M' {
				holders = append(holders, s)
			}
			if _, verr := bootLogoValidate(data); verr != nil {
				notes = append(notes, fmt.Sprintf("%s: %v", name, verr))
				continue
			}
			found = append(found, s)
		}
	}
	dev, ok, err := bootLogoPartition(bootLogoRawName)
	if err != nil {
		return nil, err
	}
	if ok {
		if reason := bootLogoRefuseDevice(bootLogoRawName, dev); reason != "" {
			notes = append(notes, reason)
		} else {
			s := store{bootLogoKindRaw, bootLogoRawName, dev}
			t, err := bootLogoOpenRaw(dev, bootLogoRawName)
			switch {
			case err != nil:
				if errors.Is(err, errBootLogoFormat) {
					// It starts with "BM"; only its size does not fit.
					holders = append(holders, s)
				}
				notes = append(notes, err.Error())
			default:
				holders = append(holders, s)
				if data, rerr := t.Read(); rerr != nil {
					notes = append(notes, fmt.Sprintf("%s: %v", bootLogoRawName, rerr))
				} else if _, verr := bootLogoValidate(data); verr != nil {
					notes = append(notes, fmt.Sprintf("%s: %v", bootLogoRawName, verr))
				} else {
					found = append(found, s)
				}
			}
		}
	}
	if len(holders) > 1 || len(found) > 1 {
		all := holders
		if len(found) > len(all) {
			all = found
		}
		names := make([]string, 0, len(all))
		for _, s := range all {
			names = append(names, s.part+" ("+s.dev+")")
		}
		return nil, fmt.Errorf("%w (%s)", errBootLogoAmbiguous, strings.Join(names, ", "))
	}
	if len(found) == 0 {
		if len(notes) == 0 {
			return nil, fmt.Errorf("%w (no partition named boot-resource, bootloader or bootlogo)", errBootLogoNoStore)
		}
		// Every refusal goes to the log; the row says "a format JukaHub won't
		// rewrite" when any store held such a logo, else "no store".
		for _, n := range notes {
			logInfo("bootlogo", "store refused: %s", n)
		}
		return nil, bootLogoFirstReason(notes)
	}
	s := found[0]
	if s.kind == bootLogoKindRaw {
		t, err := bootLogoOpenRaw(s.dev, s.part)
		if err != nil {
			return nil, err
		}
		return t, nil
	}
	t, err := bootLogoOpenFAT(s.dev, s.part, write)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// bootLogoFirstReason turns the refusals of a discovery that found nothing
// into one error: a format refusal when any store held a logo JukaHub will not
// rewrite, otherwise "no store", with every note attached for the log.
func bootLogoFirstReason(notes []string) error {
	kind := errBootLogoNoStore
	for _, n := range notes {
		if strings.Contains(n, errBootLogoFormat.Error()) {
			kind = errBootLogoFormat
			break
		}
	}
	return fmt.Errorf("%w (%s)", kind, strings.Join(notes, "; "))
}

// errBootLogoClosed is returned by a target used after Close.
var errBootLogoClosed = errors.New("the boot logo store is closed")
