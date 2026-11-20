//go:build linux

package main

// tvmode_linux.go — the TV-mode probe reads the real sysfs (tvmode_sysfs.go
// says what and why). A probe is a few dozen small file reads; it runs on the
// TV-mode goroutine every two seconds, and on the drivers page's own
// goroutine when that page asks.

import (
	"os"
	"sort"
	"strings"
)

// tvProbeSupported says this system has a sysfs to watch.
const tvProbeSupported = true

// tvProbeSystem reads the running system's video-out status. fbCheck also
// counts a framebuffer that left the panel's size as a display (the watch
// asks for it only when fb0 had the panel's size at its first reading).
func tvProbeSystem(fbCheck bool) tvEvidence {
	w, h := 0, 0
	if fbCheck {
		hw := deviceHardwareCurrent()
		w, h = int(hw.PanelW), int(hw.PanelH)
	}
	return tvProbeRoot("/sys", w, h)
}

// tvSysfsClassNames lists /sys/class once for the log: on a model whose
// video-out path is unknown, the classes the kernel has are the first clue to
// where a cable shows up.
func tvSysfsClassNames() string {
	entries, err := os.ReadDir("/sys/class")
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}
