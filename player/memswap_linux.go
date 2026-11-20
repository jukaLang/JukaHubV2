//go:build linux

package main

// memswap_linux.go — memswap.go's setup and teardown: zram, the swap file,
// swapon(2) and swapoff(2), swappiness.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	memSwapFlagPrefer = 0x8000 // SWAP_FLAG_PREFER
	memSwapPrioMask   = 0x7fff // SWAP_FLAG_PRIO_MASK
	memSwapZramPrio   = 100
	// memSwapOffMarginKB: swapoff runs only with what the area holds plus
	// this much free.
	memSwapOffMarginKB = 64 << 10
)

var errMemSwapStopped = errors.New("stopped for a game or the switch")

// memSwapRun switches JukaHub's swap on or off. memSwap.runMu held, off the
// main thread.
func memSwapRun(on bool) {
	st := memSwapLoadState()
	swaps := memSwapList()
	var ours []memSwapEntry
	for _, e := range swaps {
		if memSwapIsOurs(e, st) {
			ours = append(ours, e)
		}
	}
	if !on {
		memSwapTeardown(ours, st)
		return
	}
	if len(swaps) > 0 {
		text := memSwapOnText(swaps, len(ours) == len(swaps))
		memSwapSetStatus(text)
		logInfo("mem", "swap: %s (%d area(s) in /proc/swaps)", text, len(swaps))
		return
	}
	totalKB := readProcMeminfo().MemTotal
	dev, size, err := memSwapZram(totalKB)
	if err == nil {
		st.Zram, st.Boot = dev, memSwapReadLine("/proc/sys/kernel/random/boot_id")
		memSwapSaveState(st)
		memSwapSetStatus(fmt.Sprintf("On: %s compressed in RAM (zram)", memShort(size)))
		logInfo("mem", "swap: %s of compressed memory on %s (zram)", memShort(size), dev)
		return
	}
	logInfo("mem", "swap: no zram (%v); trying a swap file on the card", err)
	memSwapFileOn(st)
}

// memSwapIsOurs reports whether an active swap area is one JukaHub switched
// on: its file (by path, or by identity when /proc/swaps names it through
// another path: a symlink, a second mount of the card), or the zram device it
// set up in this boot.
func memSwapIsOurs(e memSwapEntry, st memSwapState) bool {
	if st.File != "" && (e.name == st.File || memSwapSameFile(e, st.File)) {
		return true
	}
	if dir := memSwapDir(); dir != "" {
		if p := filepath.Join(dir, memSwapFileName); filepath.Clean(e.name) == p || memSwapSameFile(e, p) {
			return true
		}
	}
	return st.Zram != "" && e.name == st.Zram && st.Boot != "" &&
		st.Boot == memSwapReadLine("/proc/sys/kernel/random/boot_id")
}

// memSwapSameFile reports whether the swap file area e is the file at p:
// the same device and inode (os.SameFile), whatever path each is named by.
// Partitions and zram devices are never a file in the data folder.
func memSwapSameFile(e memSwapEntry, p string) bool {
	if e.kind == "partition" {
		return false
	}
	a, err := os.Stat(e.name)
	if err != nil {
		return false
	}
	b, err := os.Stat(p)
	return err == nil && os.SameFile(a, b)
}

// memSwapActiveFile reports whether the file at p is an active swap area of
// swaps, by identity (memSwapSameFile). An area whose name cannot be looked
// up from here but ends in p's file name counts too: unlinking a live swap
// file is the one mistake memSwapRemoveFiles must never make.
func memSwapActiveFile(p string, swaps []memSwapEntry) bool {
	if _, err := os.Stat(p); err != nil {
		return false // nothing there to delete
	}
	for _, e := range swaps {
		if e.kind == "partition" {
			continue
		}
		if _, err := os.Stat(e.name); err != nil {
			if filepath.Base(e.name) == filepath.Base(p) {
				return true
			}
			continue
		}
		if memSwapSameFile(e, p) {
			return true
		}
	}
	return false
}

// memSwapTeardown switches JukaHub's swap off, deletes the file and puts
// the swappiness back.
func memSwapTeardown(ours []memSwapEntry, st memSwapState) {
	if len(ours) == 0 && st == (memSwapState{}) {
		memSwapRemoveFiles() // never set up, or set up by hand: no state to write
		memSwapSetStatus("")
		return
	}
	stuck := false
	for _, e := range ours {
		// swapoff brings every page back into RAM, and while it does the
		// kernel makes its caller the OOM killer's first choice
		// (set_current_oom_origin): with less free than the area holds, the
		// switch-off would end JukaHub instead of failing.
		if info := readProcMeminfo(); info.MemTotal > 0 && e.usedKB > 0 && info.MemAvail < e.usedKB+memSwapOffMarginKB {
			stuck = true
			logWarn("mem", "swap: %s holds %d MB and %d MB are free; it stays on until the next start", e.name, e.usedKB>>10, info.MemAvail>>10)
			continue
		}
		if err := memSwapCall(syscall.SYS_SWAPOFF, e.name, 0); err != nil {
			stuck = true
			logWarn("mem", "swap: could not switch %s off (%v); it stays on until the next start", e.name, err)
			continue
		}
		logInfo("mem", "swap: %s switched off", e.name)
		if e.zram() {
			base := filepath.Base(e.name)
			_ = os.WriteFile("/sys/block/"+base+"/reset", []byte("1\n"), 0o644)
			// A device JukaHub added goes with it (zram0 is the module's own).
			if n := strings.TrimPrefix(base, "zram"); n != "" && n != "0" {
				_ = os.WriteFile("/sys/class/zram-control/hot_remove", []byte(n+"\n"), 0o644)
			}
		}
	}
	if !stuck {
		memSwapRemoveFiles()
		st.File, st.Zram, st.Boot = "", "", ""
		// Off and on again is a request to try once more.
		st.Refused, st.Kernel, st.FS = "", "", ""
	}
	if st.Swappiness > 0 && st.File == "" {
		_ = os.WriteFile("/proc/sys/vm/swappiness", []byte(strconv.Itoa(st.Swappiness)+"\n"), 0o644)
		st.Swappiness = 0
	}
	memSwapSaveState(st)
	if stuck {
		memSwapSetStatus("Stays on until restart: memory is full")
		return
	}
	memSwapSetStatus("")
}

// memSwapRemoveFiles deletes the swap file and a cut write's leftover. Only
// while neither is active: swapoff first. A file that is still an active swap
// area under any name (memSwapActiveFile) is left in place.
func memSwapRemoveFiles() {
	dir := memSwapDir()
	if dir == "" {
		return
	}
	swaps := memSwapList()
	for _, p := range []string{filepath.Join(dir, memSwapFileName), filepath.Join(dir, memSwapFileName+".part")} {
		if memSwapActiveFile(p, swaps) {
			logWarn("mem", "swap: %s is still switched on as swap; not deleted", p)
			continue
		}
		if err := os.Remove(p); err == nil {
			logInfo("mem", "swap: deleted %s", p)
		}
	}
}

// memSwapZram sets up a free zram device as swap: half of the RAM, at most
// memSwapZramMaxMB, lz4 when offered. It returns the device and its size in
// bytes.
func memSwapZram(totalKB uint64) (string, uint64, error) {
	n, err := memZramFree()
	if err != nil {
		return "", 0, err
	}
	sys := "/sys/block/zram" + n
	undo := func() {
		_ = os.WriteFile(sys+"/reset", []byte("1\n"), 0o644)
		if n != "0" {
			_ = os.WriteFile("/sys/class/zram-control/hot_remove", []byte(n+"\n"), 0o644)
		}
	}
	// The algorithm must be chosen before the size.
	if alg, err := os.ReadFile(sys + "/comp_algorithm"); err == nil && strings.Contains(string(alg), "lz4") {
		_ = os.WriteFile(sys+"/comp_algorithm", []byte("lz4\n"), 0o644)
	}
	page := uint64(os.Getpagesize())
	size := min(totalKB*1024/2, memSwapZramMaxMB<<20) / page * page
	if size < 16<<20 {
		undo()
		return "", 0, errors.New("too little RAM for zram")
	}
	if err := os.WriteFile(sys+"/disksize", []byte(strconv.FormatUint(size, 10)+"\n"), 0o644); err != nil {
		undo()
		return "", 0, fmt.Errorf("zram%s disksize: %w", n, err)
	}
	dev := "/dev/zram" + n
	if err := memZramNode(dev, sys); err != nil {
		undo()
		return "", 0, err
	}
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	if err := memSwapWriteHeader(dev, memSwapHeader(int(page), size/page, uuid[:])); err != nil {
		undo()
		return "", 0, err
	}
	if err := memSwapCall(syscall.SYS_SWAPON, dev, memSwapFlagPrefer|memSwapZramPrio&memSwapPrioMask); err != nil {
		undo()
		return "", 0, fmt.Errorf("swapon %s: %w", dev, err)
	}
	return dev, size, nil
}

// memZramFree finds an unused zram device ("0" for zram0), loading the module
// or adding a device when needed.
func memZramFree() (string, error) {
	if !memSwapExists("/sys/block/zram0") && !memSwapExists("/sys/class/zram-control") {
		// Not built in: the module may be there.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := exec.CommandContext(ctx, "modprobe", "zram", "num_devices=1").CombinedOutput()
		cancel()
		if err != nil {
			return "", fmt.Errorf("this kernel has no zram (modprobe zram: %v %s)", err, firstLine(strings.TrimSpace(string(out))))
		}
		for i := 0; i < 20 && !memSwapExists("/sys/block/zram0"); i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if memSwapReadLine("/sys/block/zram0/disksize") == "0" {
		return "0", nil
	}
	// zram0 is in use (a RAM disk, say): a device of our own, kernels 4.2+.
	if n := memSwapReadLine("/sys/class/zram-control/hot_add"); n != "" {
		if _, err := strconv.Atoi(n); err == nil {
			return n, nil
		}
	}
	return "", errors.New("zram0 is already in use")
}

// memZramNode makes sure dev exists: without devtmpfs it is made from the
// numbers in sys/dev.
func memZramNode(dev, sys string) error {
	for i := 0; i < 20; i++ {
		if memSwapExists(dev) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	majMin := strings.SplitN(memSwapReadLine(sys+"/dev"), ":", 2)
	if len(majMin) != 2 {
		return fmt.Errorf("%s: no device numbers", sys)
	}
	maj, e1 := strconv.ParseUint(majMin[0], 10, 32)
	minor, e2 := strconv.ParseUint(majMin[1], 10, 32)
	if e1 != nil || e2 != nil {
		return fmt.Errorf("%s: bad device numbers", sys)
	}
	if err := syscall.Mknod(dev, syscall.S_IFBLK|0o600, int(unix.Mkdev(uint32(maj), uint32(minor)))); err != nil {
		return fmt.Errorf("mknod %s: %w", dev, err)
	}
	return nil
}

// memSwapFileOn switches on the swap file, writing it first when there is
// none: memswap.go says when and how.
func memSwapFileOn(st memSwapState) {
	dir := memSwapDir()
	if dir == "" {
		memSwapSetStatus("Could not turn on: no data folder")
		return
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		memSwapSetStatus("Could not turn on: " + err.Error())
		return
	}
	fsType := strconv.FormatInt(int64(fs.Type), 16)
	kernel := memSwapReadLine("/proc/sys/kernel/osrelease")
	if st.Refused != "" && st.Kernel == kernel && st.FS == fsType {
		memSwapSetStatus("The card's file system does not take a swap file")
		logInfo("mem", "swap: this card refused a swap file before (%s, fs %s); not writing it again", st.Refused, fsType)
		return
	}
	p := filepath.Join(dir, memSwapFileName)
	page := int64(os.Getpagesize())
	size := int64(0)
	if info, err := os.Stat(p); err == nil && memSwapFileReady(p, info.Size(), page) {
		size = info.Size() // written on an earlier start: switched on as it is
	} else {
		_ = os.Remove(p) // an unfinished or older file is replaced, never kept beside the new one
		// A write a power cut left behind gives its room back before the
		// card's free space is measured.
		if os.Remove(p+".part") == nil {
			_ = syscall.Statfs(dir, &fs)
		}
		freeMB := uint64(fs.Bavail) * uint64(fs.Bsize) >> 20
		mb := memSwapFileSizeMB(freeMB)
		if mb == 0 {
			_ = os.Remove(p)
			memSwapSetStatus(fmt.Sprintf("Needs %s free on the card", memShort((memSwapFileMB+memSwapReserveMB)<<20)))
			logInfo("mem", "swap: no swap file: %d MB free on the card, %d MB wanted", freeMB, memSwapFileMB+memSwapReserveMB)
			return
		}
		size = int64(mb) << 20
		started := time.Now()
		memSwapSetStatus("Preparing a swap file on the card…")
		logInfo("mem", "swap: writing a %d MB swap file to %s (fs %s)", mb, p, fsType)
		if err := memSwapWriteFile(p, size, page); err != nil {
			if errors.Is(err, errMemSwapStopped) {
				// A game came, or the switch went off: try again at the next
				// quiet moment — three times a run at most, so a busy player
				// does not get the card written over and over.
				if memSwap.cut.Add(1) < 3 {
					memSwap.asked.Store(false)
				}
				memSwapSetStatus("")
				logInfo("mem", "swap: swap file not finished (%v)", err)
				return
			}
			memSwapSetStatus("Could not turn on: " + err.Error())
			logWarn("mem", "swap: could not write the swap file: %v", err)
			return
		}
		logInfo("mem", "swap: swap file written in %s", time.Since(started).Round(time.Second))
	}
	if err := memSwapCall(syscall.SYS_SWAPON, p, 0); err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) && (errno == syscall.EINVAL || errno == syscall.ENOSYS || errno == syscall.EOPNOTSUPP) {
			// The file system cannot hold a swap file on this kernel: remove
			// it and do not write it again for this kernel and file system.
			_ = os.Remove(p)
			st.Refused, st.Kernel, st.FS = err.Error(), kernel, fsType
			memSwapSaveState(st)
			memSwapSetStatus("The card's file system does not take a swap file")
			logWarn("mem", "swap: the kernel refused a swap file on this card (fs %s, kernel %s): %v; file removed", fsType, kernel, err)
			return
		}
		memSwapSetStatus("Could not turn on: " + err.Error())
		logWarn("mem", "swap: swapon %s: %v", p, err)
		return
	}
	st.File, st.Refused = p, ""
	if prev, err := strconv.Atoi(memSwapReadLine("/proc/sys/vm/swappiness")); err == nil && prev > memSwapFileSwappiness {
		if os.WriteFile("/proc/sys/vm/swappiness", []byte(strconv.Itoa(memSwapFileSwappiness)+"\n"), 0o644) == nil {
			if st.Swappiness == 0 {
				st.Swappiness = prev
			}
		}
	}
	memSwapSaveState(st)
	memSwapSetStatus(fmt.Sprintf("On: %s swap file on the card", memShort(uint64(size))))
	logInfo("mem", "swap: %s swap file on the card at %s (swappiness %s)", memShort(uint64(size)), p, memSwapReadLine("/proc/sys/vm/swappiness"))
}

// memSwapFileReady reports whether p is a whole swap file of size bytes with
// its header in place.
func memSwapFileReady(p string, size, page int64) bool {
	if size < 10*page || size%page != 0 {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, page)
	if _, err := f.ReadAt(head, 0); err != nil {
		return false
	}
	return memSwapHeaderOK(head, uint64(size/page))
}

// memSwapWriteFile writes a whole swap file of size bytes at p: zeros in
// 1 MB pieces after the header, through p+".part" so a cut write never looks
// finished. Every 16 MB it syncs and drops what it wrote from the page cache,
// pauses a moment for the interface, and stops when memSwapShouldStop says.
func memSwapWriteFile(p string, size, page int64) error {
	part := p + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			f.Close()
			_ = os.Remove(part)
		}
	}()
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	chunk := make([]byte, 1<<20)
	copy(chunk, memSwapHeader(int(page), uint64(size/page), uuid[:]))
	for off := int64(0); off < size; {
		if memSwapShouldStop() {
			return errMemSwapStopped
		}
		n := min(int64(len(chunk)), size-off)
		if _, err := f.Write(chunk[:n]); err != nil {
			return err
		}
		if off == 0 {
			clear(chunk[:page]) // only the first page holds the header
		}
		off += n
		if off%(16<<20) == 0 || off == size {
			if err := f.Sync(); err != nil {
				return err
			}
			_ = unix.Fadvise(int(f.Fd()), 0, off, unix.FADV_DONTNEED)
			time.Sleep(50 * time.Millisecond)
		}
	}
	closed = true
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return err
	}
	if err := os.Rename(part, p); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}

// memSwapWriteHeader writes a swap header to the start of a block device.
func memSwapWriteHeader(dev string, header []byte) error {
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(header); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// memSwapCall is swapon(2) or swapoff(2) on p with flags.
func memSwapCall(trap uintptr, p string, flags int) error {
	ptr, err := syscall.BytePtrFromString(p)
	if err != nil {
		return err
	}
	if _, _, e := syscall.Syscall(trap, uintptr(unsafe.Pointer(ptr)), uintptr(flags), 0); e != 0 {
		return e
	}
	return nil
}

// memSwapReadLine is the first line of a small file, trimmed ("" on error).
func memSwapReadLine(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(firstLine(string(data)))
}

func memSwapExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
