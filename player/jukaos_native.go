package main

// The standalone image carries this marker in its read-only root filesystem.
// A card copied onto stock firmware does not become a native OS by accident.

import (
	"os"
	"runtime"
	"strings"
	"sync"
)

const jukaosNativeMarker = "/etc/jukahub-os-release"
const jukaosNativeRebootMarker = "/tmp/jukahub-native-reboot"

var jukaosNativeActive = sync.OnceValue(func() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile(jukaosNativeMarker)
	return err == nil && strings.Contains(string(data), "JUKAHUB_OS_NATIVE=1\n")
})

// jukaosNativeBaseLabel says what the standalone image is built on, from the
// BASE= line of its marker: plastron (Brick) or the Smart Pro's own kernel.
func jukaosNativeBaseLabel() string {
	data, err := os.ReadFile(jukaosNativeMarker)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			switch strings.TrimSpace(line) {
			case "BASE=smartpro-stock-kernel":
				return "JukaHub boots on the Smart Pro's own kernel"
			case "BASE=plastron":
				return "JukaHub boots on plastron"
			}
		}
	}
	return "JukaHub boots as the system"
}

func jukaosNativeBootSeconds() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	value, _, _ := strings.Cut(string(data), " ")
	return strings.TrimSpace(value)
}
