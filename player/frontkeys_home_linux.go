//go:build linux

package main

// frontkeys_home_linux.go — reading the Smart Pro S's Home key from the kernel
// (frontkeys_home.go has the rest). The devices come from
// /proc/bus/input/devices (frontHomePickDevices); each is opened read-only and
// never grabbed, so the firmware and a running game still see every press.
// Records are decoded with volumebuttons.go's parser, which knows both the 16-
// and the 24-byte input_event layouts, as the power key's reader does
// (powerkey_linux.go).

import (
	"os"
	"path/filepath"
	"time"
)

// frontHomeStartReaders opens every Home key device and starts a reader for
// each; override > 0 is the one code to read (homeKeyCode). The reader
// goroutine (frontHomeStart).
func frontHomeStartReaders(override int) {
	data, err := os.ReadFile(evdevDevicesPath)
	if err != nil {
		logInfo("controls", "Home key: cannot list the input devices: %v", err)
		return
	}
	handlers, code := frontHomePickDevices(evdevParseDevices(string(data)), override)
	if len(handlers) == 0 {
		logInfo("controls", "Home key: no input device sends KEY_HOMEPAGE or KEY_HOME; set homeKeyCode to the code evtest shows")
		return
	}
	for _, handler := range handlers {
		path := filepath.Join("/dev/input", handler)
		f, err := os.Open(path)
		if err != nil {
			logInfo("controls", "Home key: cannot open %s: %v", path, err)
			continue
		}
		frontHome.found.Store(true)
		logInfo("controls", "Home key: reading key %d from %s", code, path)
		go frontHomeDeviceLoop(f, uint16(code))
	}
}

// frontHomeDeviceLoop turns one device's presses of code into taps: down then
// up within frontHomeTapMax. Auto-repeat (value 2) is ignored. A read error
// (the device went away) ends it quietly.
func frontHomeDeviceLoop(f *os.File, code uint16) {
	defer f.Close()
	defer func() {
		if r := recover(); r != nil {
			logWarn("controls", "Home key reader stopped: %v", r)
		}
	}()
	var downAt time.Time
	buf := make([]byte, 24*32)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			size := inputRecordSize(len(chunk))
			for i := 0; i+size <= len(chunk); i += size {
				typ, c, value, ok := parseInputEvent(chunk[i : i+size])
				if !ok || typ != evKey || c != code {
					continue
				}
				switch value {
				case 1:
					downAt = time.Now()
				case 0:
					if !downAt.IsZero() && time.Since(downAt) <= frontHomeTapMax {
						frontHomeTap()
					}
					downAt = time.Time{}
				}
			}
		}
		if err != nil {
			return
		}
	}
}
