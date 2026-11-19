//go:build linux

package main

// startup_linux.go — the kernel's side of the boot clock, and the files the
// first frame needs, asked for before anyone reads them.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// startupBootClockKnown reports whether startupBootClock reads the kernel's
// uptime here.
func startupBootClockKnown() bool { return true }

// startupBootClock is the kernel's uptime in seconds: CLOCK_BOOTTIME, the clock
// /proc/uptime prints (time asleep included). 0 when it cannot be read.
func startupBootClock() float64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err == nil {
		return float64(ts.Sec) + float64(ts.Nsec)/1e9
	}
	up, ok := bootOSReadUptime()
	if !ok {
		return 0
	}
	return up.Seconds()
}

// startupProcStart is the uptime at which this process was executed, from
// field 22 of /proc/self/stat (clock ticks since boot). An exec keeps the
// process, so JukaHub OS's re-exec into its session (jukaos_linux.go) is
// counted from the first start. 0 when unreadable.
func startupProcStart() float64 {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	return parseProcStatStart(string(data))
}

// startupBootloaderSeconds is the time U-Boot spent before the kernel, where
// it left its bootstage report in the device tree (/bootstage/<n>/mark, a
// big-endian 32-bit microsecond count each): the latest mark. 0 when there is
// no report.
func startupBootloaderSeconds() float64 {
	const root = "/proc/device-tree/bootstage"
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	var latest uint32
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name(), "mark"))
		if err != nil || len(data) != 4 {
			continue
		}
		if us := binary.BigEndian.Uint32(data); us > latest {
			latest = us
		}
	}
	// More than a minute is not a boot loader's time: a timer that was not
	// counting from power-on.
	if latest == 0 || latest > 60_000_000 {
		return 0
	}
	return float64(latest) / 1e6
}

// startupReadTmpFile reads a small file in /tmp, where anyone could have left
// something under the name: not through a symbolic link, not a pipe or a
// device (opened without waiting, and refused unless it is a regular file),
// and at most 256 bytes. ok is false for anything else.
func startupReadTmpFile(path string) (string, bool) {
	const maxLen = 256
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", false
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size > maxLen {
		return "", false
	}
	buf := make([]byte, maxLen)
	n, err := unix.Read(fd, buf)
	if err != nil || n <= 0 {
		return "", false
	}
	return string(buf[:n]), true
}

// ── Read-ahead ────────────────────────────────────────────────────────────
//
// On a cold card every file the first frame opens is a wait on the main thread:
// the config, the settings, the two Inter faces (close to 2 MB together), the
// update state. Asking the kernel for them at process start lets the card read
// them while SDL and the GPU driver start, so the reads that follow are copies
// from memory. POSIX_FADV_WILLNEED only queues read-ahead; nothing is read
// into this process, a missing file costs one failed open, and the goroutine
// touches no shared state.

// startupPrefetchFiles are relative to the folder JukaHub runs from.
var startupPrefetchFiles = []string{
	"jukaconfig.json",
	"jukauser.json",
	"Inter-Regular.ttf",
	"Inter-SemiBold.ttf",
	"patch/state.json",
	"data/startup-state.json",
	"data/game-recents.json",
}

func init() {
	if testing.Testing() {
		return
	}
	go startupPrefetch()
}

func startupPrefetch() {
	dir := ""
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	for _, name := range startupPrefetchFiles {
		path := name
		if dir != "" {
			path = filepath.Join(dir, name)
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		// One WILLNEED for the whole file is cut to the device's read-ahead
		// window (128 KiB by default), so the request goes in window-sized
		// steps; 2 MiB a file at most (the fonts are under 1 MiB).
		size := int64(2 << 20)
		if st, err := f.Stat(); err == nil && st.Size() < size {
			size = st.Size()
		}
		for off := int64(0); off < size; off += 128 << 10 {
			_ = unix.Fadvise(int(f.Fd()), off, 128<<10, unix.FADV_WILLNEED)
		}
		_ = f.Close()
	}
}
