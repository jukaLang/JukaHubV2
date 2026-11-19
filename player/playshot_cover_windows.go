//go:build windows

package main

// playshot_cover_windows.go — reading the game's screen for a gameplay cover
// (playshot_cover.go) on a PC: the inside of the game's window as it shows on
// the screen. Only the window in front, and only when it belongs to the game
// JukaHub started (its process or one the process started), so a browser the
// user switched to is never taken for the game. The read runs per-monitor DPI
// aware, so the window's size and the screen's pixels agree on a scaled
// display. A game in exclusive full screen may read as black; the dull test
// then waits for a better frame, and gives up with the session.

import (
	"errors"
	"fmt"
	"image"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	playShotUser32                 = windows.NewLazySystemDLL("user32.dll")
	playShotGdi32                  = windows.NewLazySystemDLL("gdi32.dll")
	procPlayShotGetClientRect      = playShotUser32.NewProc("GetClientRect")
	procPlayShotClientToScreen     = playShotUser32.NewProc("ClientToScreen")
	procPlayShotIsIconic           = playShotUser32.NewProc("IsIconic")
	procPlayShotGetDC              = playShotUser32.NewProc("GetDC")
	procPlayShotReleaseDC          = playShotUser32.NewProc("ReleaseDC")
	procPlayShotSetThreadDpiCtx    = playShotUser32.NewProc("SetThreadDpiAwarenessContext")
	procPlayShotCreateCompatibleDC = playShotGdi32.NewProc("CreateCompatibleDC")
	procPlayShotCreateDIBSection   = playShotGdi32.NewProc("CreateDIBSection")
	procPlayShotSelectObject       = playShotGdi32.NewProc("SelectObject")
	procPlayShotBitBlt             = playShotGdi32.NewProc("BitBlt")
	procPlayShotDeleteObject       = playShotGdi32.NewProc("DeleteObject")
	procPlayShotDeleteDC           = playShotGdi32.NewProc("DeleteDC")
)

const (
	// playShotSrcCopy is SRCCOPY. Not CAPTUREBLT: it makes the mouse pointer
	// flicker over the game, and the composed desktop has every window anyway.
	playShotSrcCopy = 0x00CC0020
	// playShotDPIPerMonitorV2 is DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2,
	// ((DPI_AWARENESS_CONTEXT)-4).
	playShotDPIPerMonitorV2 = ^uintptr(3)
	// playShotMaxSide bounds a window side read (an 8K panel is 7680).
	playShotMaxSide = 8192
	// playShotParentHops bounds the walk from the window's process up to the
	// game JukaHub started (a launcher script, then the emulator).
	playShotParentHops = 8
)

// playShotPoint is POINT.
type playShotPoint struct {
	X, Y int32
}

// playShotBitmapInfo is BITMAPINFO with its one (unused) colour.
type playShotBitmapInfo struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
	Colors        [1]uint32
}

// playShotBitmapInfoHeaderSize is sizeof(BITMAPINFOHEADER).
const playShotBitmapInfoHeaderSize = 40

// playShotGameProcess reports whether pid is a game JukaHub started, or a
// process one of them started.
func playShotGameProcess(pid uint32) bool {
	if pid == 0 || pid == uint32(os.Getpid()) {
		return false
	}
	games := map[uint32]bool{}
	for _, cmd := range liveScreenChildren() {
		if cmd != nil && cmd.Process != nil && cmd.Process.Pid > 0 {
			games[uint32(cmd.Process.Pid)] = true
		}
	}
	if len(games) == 0 {
		return false
	}
	if games[pid] {
		return true
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	parents := map[uint32]uint32{}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
	}
	cur := pid
	for i := 0; i < playShotParentHops; i++ {
		p, ok := parents[cur]
		if !ok || p == 0 || p == cur {
			return false
		}
		if games[p] {
			return true
		}
		cur = p
	}
	return false
}

// playShotGrab is the inside of the game's window as it shows now. Any
// goroutine: it keeps its OS thread for the per-thread DPI setting.
func playShotGrab() (*image.RGBA, error) {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return nil, errors.New("no window is in front")
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil {
		return nil, err
	}
	if !playShotGameProcess(pid) {
		return nil, errors.New("the window in front is not the game")
	}
	if r, _, _ := procPlayShotIsIconic.Call(uintptr(hwnd)); r != 0 {
		return nil, errors.New("the game is minimised")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if procPlayShotSetThreadDpiCtx.Find() == nil {
		// Windows 10 1607 and later; before it the read is as the process is.
		if old, _, _ := procPlayShotSetThreadDpiCtx.Call(playShotDPIPerMonitorV2); old != 0 {
			defer procPlayShotSetThreadDpiCtx.Call(old)
		}
	}
	var rc windows.Rect
	if r, _, _ := procPlayShotGetClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rc))); r == 0 {
		return nil, errors.New("the game window has no size")
	}
	// Both corners on the screen: for a game that is not DPI aware on a
	// scaled display the client size is in its own units, the screen's
	// pixels are what is read.
	pt, end := playShotPoint{X: rc.Left, Y: rc.Top}, playShotPoint{X: rc.Right, Y: rc.Bottom}
	if r, _, _ := procPlayShotClientToScreen.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pt))); r == 0 {
		return nil, errors.New("the game window has no place on the screen")
	}
	if r, _, _ := procPlayShotClientToScreen.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&end))); r == 0 {
		return nil, errors.New("the game window has no place on the screen")
	}
	w, h := int(end.X-pt.X), int(end.Y-pt.Y)
	if w < playShotMinSide || h < playShotMinSide || w > playShotMaxSide || h > playShotMaxSide {
		return nil, fmt.Errorf("the game window is %d×%d", w, h)
	}
	screen, _, _ := procPlayShotGetDC.Call(0)
	if screen == 0 {
		return nil, errors.New("the screen cannot be read")
	}
	defer procPlayShotReleaseDC.Call(0, screen)
	mem, _, _ := procPlayShotCreateCompatibleDC.Call(screen)
	if mem == 0 {
		return nil, errors.New("no memory picture for the read")
	}
	defer procPlayShotDeleteDC.Call(mem)
	// A top-down 32-bit picture: rows in reading order, blue-green-red-unused.
	bi := playShotBitmapInfo{
		Size:     playShotBitmapInfoHeaderSize,
		Width:    int32(w),
		Height:   -int32(h),
		Planes:   1,
		BitCount: 32,
	}
	var bits unsafe.Pointer
	bmp, _, _ := procPlayShotCreateDIBSection.Call(screen, uintptr(unsafe.Pointer(&bi)), 0,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, errors.New("no memory picture for the read")
	}
	defer procPlayShotDeleteObject.Call(bmp)
	old, _, _ := procPlayShotSelectObject.Call(mem, bmp)
	r, _, _ := procPlayShotBitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), screen,
		uintptr(pt.X), uintptr(pt.Y), playShotSrcCopy)
	procPlayShotSelectObject.Call(mem, old)
	if r == 0 {
		return nil, errors.New("the screen could not be copied")
	}
	src := unsafe.Slice((*byte)(bits), w*h*4)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(src); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = src[i+2], src[i+1], src[i], 255
	}
	return img, nil
}
