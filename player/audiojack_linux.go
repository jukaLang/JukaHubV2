//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// headphoneJackEvdev reads the kernel's current jack switch bitmap without
// consuming an event or grabbing the input device. This is the stock Smart
// Pro's only jack-state interface. The event node is discovered from its
// advertised switch capabilities rather than a fixed event number.
func headphoneJackEvdev() bool {
	data, err := os.ReadFile(evdevDevicesPath)
	if err != nil {
		return false
	}
	for _, handler := range headphoneJackEventHandlers(evdevParseDevices(string(data))) {
		f, err := os.Open(filepath.Join("/dev/input", handler))
		if err != nil {
			continue
		}
		var switches [8]byte
		// EVIOCGSW(len) = _IOC(_IOC_READ, 'E', 0x1b, len).
		req := evdevIOC(evdevIOCRead, 'E', 0x1b, uint32(len(switches)))
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(req), uintptr(unsafe.Pointer(&switches[0])))
		_ = f.Close()
		if errno != 0 {
			continue
		}
		if switches[evdevSwHeadphoneInsert/8]&(1<<(evdevSwHeadphoneInsert%8)) != 0 ||
			switches[evdevSwLineoutInsert/8]&(1<<(evdevSwLineoutInsert%8)) != 0 {
			return true
		}
	}
	return false
}
