//go:build linux

package main

// brightkeys_linux.go — the Linux half of Menu + volume (brightkeys.go): the
// kernel asked whether Menu is down, and the stock keymon looked for.

import (
	"os"
	"path/filepath"
	"strings"
)

// brightKeysMenuHeldNow asks the kernel whether Menu is down on dev right now
// (EVIOCGKEY), so a release the reader missed cannot leave Menu "held". ok is
// false when it could not ask (no device, or one that went away and was
// closed); the caller then counts no chord.
func brightKeysMenuHeldNow(dev *os.File) (held, ok bool) {
	if dev == nil {
		return false, false
	}
	rc, err := dev.SyscallConn()
	if err != nil {
		return false, false
	}
	bitmap := make([]byte, evdevKeyBitmapSize)
	var ioErr error
	if err := rc.Control(func(fd uintptr) {
		ioErr = mbridgeIoctlBuf(int(fd), evdevGetKey(evdevKeyBitmapSize), bitmap)
	}); err != nil || ioErr != nil {
		return false, false
	}
	return evdevKeyBit(bitmap, evdevBtnMode), true
}

// brightKeysKeymonRunning reports whether a process named keymon runs: the
// stock firmware's key daemon, which steps the brightness on Menu + volume
// itself (keymon.elf on the community firmwares counts too). Reads only
// /proc/<pid>/comm, which never waits on another process's memory.
func brightKeysKeymonRunning() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		checked++
		if checked > 1024 { // bounded: a handheld never runs this many
			break
		}
		data, err := os.ReadFile(filepath.Join("/proc", name, "comm"))
		if err != nil {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(string(data)), "keymon") {
			return true
		}
	}
	return false
}
