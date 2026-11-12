//go:build linux

package main

// powerkey_linux.go — reading the power key from the kernel (powerkey.go has
// the rest). The devices come from /proc/bus/input/devices
// (powerKeyPickDevices: the PMIC's "axp2202-pek" on the Smart Pro); each is
// opened read-only and never grabbed, so the firmware's daemon and a running
// game still see every event. Records are decoded with volumebuttons.go's
// parser, which knows both the 16- and the 24-byte input_event layouts.
//
// A device that disappears (a read error) ends its reader quietly; the
// handheld's PMIC device never does.

import (
	"os"
	"path/filepath"
)

// powerKeyStartDevices opens every power-key device and starts a reader for
// each. It reports whether any could be opened.
func powerKeyStartDevices() bool {
	data, err := os.ReadFile(evdevDevicesPath)
	if err != nil {
		return false
	}
	opened := 0
	for _, handler := range powerKeyPickDevices(evdevParseDevices(string(data))) {
		path := filepath.Join("/dev/input", handler)
		f, err := os.Open(path)
		if err != nil {
			logDebug("power", "power key: cannot open %s: %v", path, err)
			continue
		}
		opened++
		logInfo("power", "power key: reading %s", path)
		go powerKeyDeviceLoop(f)
	}
	return opened > 0
}

// powerKeyDeviceLoop forwards KEY_POWER presses and releases from one device.
// Auto-repeat (value 2) is ignored: the hold timer covers a held key.
func powerKeyDeviceLoop(f *os.File) {
	defer f.Close()
	defer func() {
		if r := recover(); r != nil {
			logWarn("power", "power key reader stopped: %v", r)
		}
	}()
	buf := make([]byte, 24*32)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			size := inputRecordSize(len(chunk))
			for i := 0; i+size <= len(chunk); i += size {
				typ, code, value, ok := parseInputEvent(chunk[i : i+size])
				if !ok || typ != evKey || code != evdevKeyPower {
					continue
				}
				switch value {
				case 1:
					powerKeyEdge(true)
				case 0:
					powerKeyEdge(false)
				}
			}
		}
		if err != nil {
			return
		}
	}
}
