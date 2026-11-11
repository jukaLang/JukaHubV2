//go:build windows

package main

// emuautoupdate_desktop_windows.go — what the PC's Emulator updates pass asks
// Windows: is a program running from an emulator's folder, and is this a
// laptop on its battery.

import (
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// emuProcessCache keeps one list of running programs for a moment: a look of
// the pass asks about every managed emulator in turn, and one list answers
// them all.
var emuProcessCache struct {
	mu     sync.Mutex
	at     time.Time
	images []string
}

// emuProcessUnder reports whether a running process's program file is inside
// dir: an emulator started by JukaHub, from the Start menu or from a shortcut
// alike.
func emuProcessUnder(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	for _, image := range emuProcessImages() {
		if emuPathWithin(image, dir) {
			return true
		}
	}
	return false
}

// emuProcessImages is the program files of the running processes, at most two
// seconds old.
func emuProcessImages() []string {
	emuProcessCache.mu.Lock()
	defer emuProcessCache.mu.Unlock()
	if emuProcessCache.at.IsZero() || time.Since(emuProcessCache.at) > 2*time.Second {
		emuProcessCache.images = emuListProcessImages()
		emuProcessCache.at = time.Now()
	}
	return emuProcessCache.images
}

// emuListProcessImages lists them. A process this user may not query (a
// service, another user's) is passed over — an emulator the user started is
// theirs to query.
func emuListProcessImages() []string {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	var out []string
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == 0 {
			continue
		}
		if image := emuProcessImage(entry.ProcessID, buf); image != "" {
			out = append(out, image)
		}
	}
	return out
}

// emuProcessImage is the full path of a process's program, read into buf; ""
// when it cannot be read.
func emuProcessImage(pid uint32, buf []uint16) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// emuSystemPowerStatus is Windows' SYSTEM_POWER_STATUS.
type emuSystemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var procEmuGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// emuDesktopOnBattery reports a laptop running on its battery: the charger is
// unplugged and a battery is there. Anything unknown counts as mains power, so
// a desktop is never mistaken for a laptop.
func emuDesktopOnBattery() bool {
	var s emuSystemPowerStatus
	if r, _, _ := procEmuGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s))); r == 0 {
		return false
	}
	// ACLineStatus: 0 unplugged, 1 plugged in, 255 unknown. BatteryFlag 128
	// means no system battery, 255 unknown.
	return s.ACLineStatus == 0 && s.BatteryFlag != 128 && s.BatteryFlag != 255
}
