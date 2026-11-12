//go:build linux

package main

// bluetooth_bringup_linux.go — the two things the Bluetooth bring-up and the
// top bar's button need from Linux itself: the adapters' HCI_UP flag, read
// with one ioctl instead of a bluetoothctl run, and a child started in a
// session of its own.

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// btHCIGetDevList is HCIGETDEVLIST, _IOR('H', 210, int): the same request
	// on every architecture JukaHub ships for.
	btHCIGetDevList = 0x800448d2
	// btHCIMaxDevs bounds the adapters one reading returns.
	btHCIMaxDevs = 16
	// btHCIFlagUp is HCI_UP, bit 0 of an adapter's flags. The kernel clears
	// it for an adapter that is only up for BlueZ's automatic power-off, so
	// it is exactly bluetoothctl's "Powered".
	btHCIFlagUp = 1 << 0
)

// btHCIDevReq is the kernel's struct hci_dev_req: a 16-bit adapter id, then
// (after two bytes of padding) its 32-bit flags.
type btHCIDevReq struct {
	DevID  uint16
	_      uint16
	DevOpt uint32
}

// btHCIDevListReq is struct hci_dev_list_req: the number of slots in, the
// number of adapters out, and the array, which starts at offset 4 because its
// elements are 4-byte aligned.
type btHCIDevListReq struct {
	DevNum uint16
	_      uint16
	Reqs   [btHCIMaxDevs]btHCIDevReq
}

// btHCIUp reports whether any Bluetooth adapter is powered. ok is false when
// the kernel could not be asked (no Bluetooth sockets, a sandbox); a kernel
// with no adapter answers (false, true).
func btHCIUp() (up bool, ok bool) {
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
	if err != nil {
		return false, false
	}
	defer unix.Close(fd)
	var req btHCIDevListReq
	req.DevNum = btHCIMaxDevs
	// The pointer is converted in the call expression itself, so the request
	// stays put for the length of the system call.
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(btHCIGetDevList), uintptr(unsafe.Pointer(&req))); errno != 0 {
		return false, false
	}
	n := int(req.DevNum)
	if n > btHCIMaxDevs {
		n = btHCIMaxDevs
	}
	for i := 0; i < n; i++ {
		if req.Reqs[i].DevOpt&btHCIFlagUp != 0 {
			return true, true
		}
	}
	return false, true
}

// btDetachedAttr starts a child in a session of its own: a daemon or a
// firmware script started for the Bluetooth bring-up has to outlive JukaHub,
// and a signal meant for JukaHub's process group (Ctrl+C in a terminal, the
// firmware stopping the app) must not take the user's Bluetooth down with it.
func btDetachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
