//go:build linux

package main

// usbstorage_linux.go — the checks USB storage mode (usbstorage.go) makes
// before JukaHub quits for the helper, so a card that cannot be lent out is
// said here, with JukaHub still on screen, rather than after a restart. The
// helper (usbstorage_helper.sh) checks the same things again once JukaHub
// has gone: these only spare the player a pointless round trip.
//
//   - The card is its own mount of a disk partition, under a plain path (the
//     helper takes no other), and JukaHub runs from it.
//   - The USB port can be a device (a UDC), and the kernel makes a
//     mass-storage function: created and removed again in a scratch gadget,
//     the way controller mode probes its functions (usbpad_linux.go), or the
//     legacy g_mass_storage module is there.
//   - Nothing is mounted inside the card, no disk image on it is in use, and
//     it is mounted nowhere else (a bind mount).
//   - A swap file on the card fits back into free memory.
//   - No process holds the card but the ones that go with JukaHub: JukaHub
//     and what it started, launch.sh and the shells between it and the
//     firmware's launcher loop (they end with launch.sh), and leftovers of
//     JukaHub's own (a program from its folder, which the helper asks to
//     stop). The loop itself stays alive (paused), so it must not hold the
//     card; a start made directly by the boot hook leaves that hook waiting
//     on the card, so it cannot lend it.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// usbStorageProbeGadget is the scratch gadget the kernel check makes and
// removes at once.
const usbStorageProbeGadget = "jukastor-probe"

// usbStorageMountTable reads the firmware's mount table (init's), not
// JukaHub's own: in OS mode JukaHub runs in a mount namespace of its own whose
// layers end with it, and the helper sees the firmware's. name is "mounts" or
// "mountinfo".
func usbStorageMountTable(name string) []string {
	data, err := os.ReadFile("/proc/1/" + name)
	if err != nil {
		data, err = os.ReadFile("/proc/self/" + name)
	}
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// usbStorageCheck answers whether the card can be lent out now. A worker: it
// reads every process's open files.
func usbStorageCheck() usbStoragePlan {
	var p usbStoragePlan
	root := fileServerCardRoot()
	if root == "" {
		p.problem = "No SD card was found."
		return p
	}
	p.mount = filepath.Clean(root)
	if !usbStorageMountNameOK(p.mount) {
		p.problem = "The SD card is mounted at " + p.mount + ", which this mode does not lend out."
		return p
	}
	appDir, err := P().ExecutableDir()
	if err != nil || !strings.HasPrefix(filepath.Clean(appDir), p.mount+"/") {
		p.problem = "JukaHub does not run from the SD card here."
		return p
	}
	appDir = filepath.Clean(appDir)
	dev, fsType, ok := usbStorageMountOf(p.mount)
	if !ok {
		p.problem = "The SD card is not a mount of its own at " + p.mount + "."
		return p
	}
	p.dev, p.fsType = dev, fsType
	if !usbStoragePartition(dev) {
		p.problem = "The SD card (" + dev + ") is not a disk partition this mode can lend out."
		return p
	}
	var st syscall.Statfs_t
	if syscall.Statfs(p.mount, &st) == nil && st.Blocks > 0 {
		p.size = formatBytes(st.Blocks * uint64(st.Bsize))
	}
	for _, check := range []func() string{
		usbStorageKernel,
		func() string { return usbStorageMountsInside(p.mount, dev) },
		func() string { return usbStorageSwapRoom(p.mount) },
		func() string { return usbStorageUsers(p.mount, appDir) },
	} {
		if why := check(); why != "" {
			p.problem = why
			return p
		}
	}
	return p
}

// usbStorageMountNameOK is the helper's rule for the mount point: a plain
// path under /mnt or /media (or /sdcard), nothing a shell pattern would read.
func usbStorageMountNameOK(m string) bool {
	if !(strings.HasPrefix(m, "/mnt/") || strings.HasPrefix(m, "/media/") || m == "/sdcard") || strings.Contains(m, "..") {
		return false
	}
	for _, r := range m {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/_.-", r)
		if !ok {
			return false
		}
	}
	return len(m) > len("/mnt/") && !strings.HasSuffix(m, "/")
}

// usbStorageMountOf is the device and file system type of the last mount at
// mount (the one on top).
func usbStorageMountOf(mount string) (dev, fsType string, ok bool) {
	for _, line := range usbStorageMountTable("mounts") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[1] == mount {
			dev, fsType, ok = f[0], f[2], true
		}
	}
	return dev, fsType, ok
}

// usbStoragePartition reports a block device the helper lends out: an SD or
// eMMC partition (mmcblkNpM) or a USB disk's (sdXN).
func usbStoragePartition(dev string) bool {
	name := strings.TrimPrefix(dev, "/dev/")
	if name == dev {
		return false
	}
	switch {
	case strings.HasPrefix(name, "mmcblk"):
		disk, part, found := strings.Cut(strings.TrimPrefix(name, "mmcblk"), "p")
		if !found || !usbStorageDigits(disk) || !usbStorageDigits(part) {
			return false
		}
	case strings.HasPrefix(name, "sd") && len(name) > 3:
		rest := strings.TrimLeft(name[2:], "abcdefghijklmnopqrstuvwxyz")
		if len(rest) == len(name)-2 || !usbStorageDigits(rest) {
			return false
		}
	default:
		return false
	}
	fi, err := os.Stat(dev)
	return err == nil && fi.Mode()&os.ModeDevice != 0 && fi.Mode()&os.ModeCharDevice == 0
}

func usbStorageDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// usbStorageKernel checks the port and the kernel's mass-storage function.
func usbStorageKernel() string {
	if mtpFirstUDC() == "" {
		return "This handheld's USB port cannot act as a USB device on this firmware."
	}
	if mtpEnsureConfigfs() == nil && usbStorageProbeFunction() {
		return ""
	}
	if usbStorageLegacyModule() {
		return ""
	}
	return "This firmware's kernel has no USB mass-storage function."
}

// usbStorageProbeFunction makes a mass-storage function in a scratch gadget
// and removes it again (making it loads the module where there is one).
// Under controller mode's lock, which its own probe and start hold.
func usbStorageProbeFunction() bool {
	usbPadGadgetMu.Lock()
	defer usbPadGadgetMu.Unlock()
	g := filepath.Join(mtpGadgetBase, usbStorageProbeGadget)
	if dirExists(g) {
		mtpRemoveGadgetTree(g)
	}
	if err := os.Mkdir(g, 0o755); err != nil && !os.IsExist(err) {
		return false
	}
	defer mtpRemoveGadgetTree(g)
	return usbPadTryFunction(g, "mass_storage.probe")
}

// usbStorageLegacyModule reports the kernel's own mass-storage gadget module,
// loaded or on disk.
func usbStorageLegacyModule() bool {
	if dirExists("/sys/module/g_mass_storage") {
		return true
	}
	rel := mtpReadAttr("/proc/sys/kernel/osrelease")
	if rel == "" || strings.ContainsAny(rel, "/\\") {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/lib/modules", rel, "modules.dep"))
	return err == nil && bytes.Contains(data, []byte("g_mass_storage.ko"))
}

// usbStorageMountsInside finds a mount inside the card, a disk image on it
// in use, or the card mounted a second time elsewhere.
func usbStorageMountsInside(mount, dev string) string {
	for _, line := range usbStorageMountTable("mounts") {
		if f := strings.Fields(line); len(f) >= 2 && strings.HasPrefix(f[1], mount+"/") {
			return "Something is mounted inside the SD card (" + f[1] + ")."
		}
	}
	files, _ := filepath.Glob("/sys/block/loop*/loop/backing_file")
	for _, f := range files {
		if b := mtpReadAttr(f); strings.HasPrefix(b, mount+"/") {
			return "A disk image on the SD card is in use (" + b + ")."
		}
	}
	majmin := mtpReadAttr(filepath.Join("/sys/class/block", filepath.Base(dev), "dev"))
	if majmin == "" {
		return ""
	}
	for _, line := range usbStorageMountTable("mountinfo") {
		f := strings.Fields(line)
		if len(f) >= 5 && f[2] == majmin && f[4] != mount && !strings.HasPrefix(f[4], mount+"/") {
			return "The SD card is also mounted at " + f[4] + "."
		}
	}
	return ""
}

// usbStorageSwapRoom: a swap file on the card goes off while it is lent out,
// and its pages need room in memory (a margin of 64 MB on top).
func usbStorageSwapRoom(mount string) string {
	var used uint64
	for _, e := range memSwapList() {
		if strings.HasPrefix(e.name, mount+"/") {
			used += e.usedKB
		}
	}
	if used == 0 {
		return ""
	}
	if readProcMeminfo().MemAvail < used+64*1024 {
		return "The swap file on the SD card holds more than fits in free memory right now. Close a game, or turn Extra memory (swap) off, then try again."
	}
	return ""
}

// usbStorageProc is one process as the holder check sees it.
type usbStorageProc struct {
	ppid int
	comm string
}

// usbStorageUsers names what holds the card and will not let go of it.
func usbStorageUsers(mount, appDir string) string {
	self := os.Getpid()
	procs := map[int]usbStorageProc{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		s := string(stat)
		open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if open < 0 || end < open {
			continue
		}
		f := strings.Fields(s[end+1:])
		if len(f) < 2 || f[0] == "Z" {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		procs[pid] = usbStorageProc{ppid: ppid, comm: s[open+1 : end]}
	}
	// The chain above JukaHub: launch.sh first. Shells up to the firmware's
	// loop end with launch.sh; the loop and what is above it stay.
	ends := map[int]bool{}
	stays := map[int]bool{}
	var chain []int
	for p, n := procs[self].ppid, 0; p > 1 && n < 32; n++ {
		chain = append(chain, p)
		p = procs[p].ppid
	}
	loopAt := -1
	for i, p := range chain {
		cmd := usbStorageCmdline(p)
		if strings.Contains(cmd, "jukahub-autostart") {
			return "JukaHub was started straight from the boot hook this time. Open it from the stock menu, then try again."
		}
		if loopAt < 0 && (strings.Contains(cmd, "runtrimui.sh") || strings.Contains(cmd, "trimui_launcher")) {
			loopAt = i
		}
	}
	for i, p := range chain {
		switch {
		case i == 0 || (loopAt > 0 && i < loopAt):
			ends[p] = true
		case i == loopAt:
			stays[p] = true
		}
	}
	// JukaHub's own children end with it.
	mine := func(pid int) bool {
		for p, n := pid, 0; p > 1 && n < 64; n++ {
			if p == self {
				return true
			}
			p = procs[p].ppid
		}
		return false
	}
	pids := make([]int, 0, len(procs))
	for pid := range procs {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	var held []string
	for _, pid := range pids {
		if pid == self || ends[pid] || mine(pid) || !usbStorageHolds(pid, mount) {
			continue
		}
		if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil && strings.HasPrefix(exe, appDir+"/") {
			continue // JukaHub's own leftover: the helper asks it to stop
		}
		who := fmt.Sprintf("%s (pid %d)", procs[pid].comm, pid)
		if stays[pid] {
			who = "the firmware's launcher, " + who
		}
		held = append(held, who)
	}
	if len(held) == 0 {
		return ""
	}
	more := ""
	if len(held) > 3 {
		more = fmt.Sprintf(" and %d more", len(held)-3)
		held = held[:3]
	}
	return "Other programs are using the SD card: " + strings.Join(held, ", ") + more + ". Close them (an SSH session, for one), then try again."
}

// usbStorageCmdline is a process's command line with spaces between the
// arguments.
func usbStorageCmdline(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(b), "\x00", " ")
}

// usbStorageHolds reports whether pid uses anything on the card: its working
// folder, root or program, an open file, or a mapped file.
func usbStorageHolds(pid int, mount string) bool {
	under := func(p string) bool { return p == mount || strings.HasPrefix(p, mount+"/") }
	base := fmt.Sprintf("/proc/%d", pid)
	for _, l := range []string{"cwd", "root", "exe"} {
		if t, err := os.Readlink(base + "/" + l); err == nil && under(t) {
			return true
		}
	}
	if fds, err := os.ReadDir(base + "/fd"); err == nil {
		for _, fd := range fds {
			if t, err := os.Readlink(base + "/fd/" + fd.Name()); err == nil && under(t) {
				return true
			}
		}
	}
	maps, err := os.ReadFile(base + "/maps")
	return err == nil && bytes.Contains(maps, []byte(" "+mount+"/"))
}
