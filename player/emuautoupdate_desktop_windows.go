//go:build windows

package main

// emuautoupdate_desktop_windows.go — what the PC's Emulator updates pass asks
// Windows: is a program running from an emulator's folder, is this a laptop on
// its battery, when did the user last touch the keyboard or mouse, and does
// another program fill the screen.

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

// emuLastInputInfo is Windows' LASTINPUTINFO.
type emuLastInputInfo struct {
	CbSize uint32
	DwTime uint32
}

// Windows' QUERY_USER_NOTIFICATION_STATE values that mean another program owns
// the screen: a full-screen application, a full-screen Direct3D game, or the
// presentation settings switched on.
const (
	emuQUNSBusy                 = 2
	emuQUNSRunningD3DFullScreen = 3
	emuQUNSPresentationMode     = 4
)

var (
	modEmuKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procEmuGetSystemPowerStatus = modEmuKernel32.NewProc("GetSystemPowerStatus")
	procEmuGetTickCount         = modEmuKernel32.NewProc("GetTickCount")
	procEmuGetLastInputInfo     = windows.NewLazySystemDLL("user32.dll").NewProc("GetLastInputInfo")
	procEmuSHQueryUNS           = windows.NewLazySystemDLL("shell32.dll").NewProc("SHQueryUserNotificationState")
)

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

// emuDesktopSystemIdle is how long ago the user last pressed a key or moved
// the mouse in any window of this session (GetLastInputInfo), or -1 when
// Windows cannot say. Game controllers are not input to Windows here: JukaHub's
// own input age hears its controllers, and emuDesktopFullScreenApp the game
// another program plays.
func emuDesktopSystemIdle() time.Duration {
	if procEmuGetLastInputInfo.Find() != nil || procEmuGetTickCount.Find() != nil {
		return -1
	}
	li := emuLastInputInfo{CbSize: uint32(unsafe.Sizeof(emuLastInputInfo{}))}
	if r, _, _ := procEmuGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li))); r == 0 {
		return -1
	}
	now, _, _ := procEmuGetTickCount.Call()
	// Both are 32-bit millisecond tick counts, so the difference survives the
	// counter wrapping every 49.7 days. Windows may stamp an input a moment
	// after the tick count it hands out: that is no idle time at all.
	ms := int32(uint32(now) - li.DwTime)
	if ms < 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// emuDesktopFullScreenApp reports another program filling the screen — a game,
// a film, a slide show — or the presentation settings switched on
// (SHQueryUserNotificationState). JukaHub's own full-screen window answers
// "busy" too, and is not another program: the foreground window's process
// tells them apart. Anything Windows cannot say counts as not busy.
func emuDesktopFullScreenApp() bool {
	if procEmuSHQueryUNS.Find() != nil {
		return false
	}
	var state uint32
	if r, _, _ := procEmuSHQueryUNS.Call(uintptr(unsafe.Pointer(&state))); uint32(r) != 0 {
		return false // not S_OK
	}
	switch state {
	case emuQUNSPresentationMode:
		return true
	case emuQUNSBusy, emuQUNSRunningD3DFullScreen:
	default:
		return false
	}
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return true
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil {
		return true
	}
	return pid != windows.GetCurrentProcessId()
}
