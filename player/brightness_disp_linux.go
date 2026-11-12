//go:build linux

package main

// brightness_disp_linux.go — the Allwinner display driver's backlight.
//
// The TrimUI Smart Pro and Brick (tg5040, Allwinner A133P) run Allwinner's BSP
// kernel, whose display driver ("disp2") owns the panel's backlight. The stock
// firmware, MinUI/NextUI and spruceOS all set it with an ioctl on /dev/disp:
//
//	DISP_LCD_SET_BRIGHTNESS (0x102)  arg -> unsigned long[4]{screen, level, 0, 0}
//	DISP_LCD_GET_BRIGHTNESS (0x103)  arg -> unsigned long[4]{screen, 0, 0, 0}, returns the level
//
// The level runs 0..255 and 0 switches the backlight off, so every write is
// kept at 1 or above (and the controller's floor keeps it well above that).
// The driver copies the four unsigned longs from the pointer; a [4]uintptr has
// that layout on every Linux ABI, where unsigned long is pointer-sized.
//
// The device is opened once and kept open for the session: on some Allwinner
// kernels the last close of /dev/disp resets the display's layers, and the
// ports that open it per call never had to find out on these boards.
//
// Nothing here was run on a device from this desk: the commands and their
// layout come from the sunxi driver's header and the ports that use them.

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

const (
	dispDevicePath          = "/dev/disp"
	dispLCDSetBrightness    = 0x102
	dispLCDGetBrightness    = 0x103
	dispLCDBacklightEnable  = 0x104
	dispLCDBacklightDisable = 0x105
)

// dispDevice is /dev/disp, opened on first use and kept open. mu also keeps
// the poll's reading and the worker's write from interleaving.
var dispDevice struct {
	mu sync.Mutex
	f  *os.File
}

// dispIoctl runs one display-driver command for screen 0 with value as its
// argument, and returns what the driver returned.
func dispIoctl(cmd, value uintptr) (uintptr, error) {
	dispDevice.mu.Lock()
	defer dispDevice.mu.Unlock()
	if dispDevice.f == nil {
		f, err := os.OpenFile(dispDevicePath, os.O_RDWR, 0)
		if err != nil {
			return 0, err
		}
		dispDevice.f = f
	}
	args := [4]uintptr{0, value, 0, 0}
	r, _, errno := syscall.Syscall(syscall.SYS_IOCTL, dispDevice.f.Fd(), cmd, uintptr(unsafe.Pointer(&args[0])))
	if errno != 0 {
		return 0, fmt.Errorf("%s ioctl %#x: %w", dispDevicePath, cmd, errno)
	}
	return r, nil
}

// dispBrightnessRaw reads the backlight level (0..255).
func dispBrightnessRaw() (int, error) {
	r, err := dispIoctl(dispLCDGetBrightness, 0)
	if err != nil {
		return 0, err
	}
	// One past the top is clamped (a driver may count its stored level from
	// 1); anything further is not a level at all.
	if r > dispBrightnessMax+1 {
		return 0, fmt.Errorf("%s answered %d, not a backlight level", dispDevicePath, r)
	}
	if r > dispBrightnessMax {
		r = dispBrightnessMax
	}
	return int(r), nil
}

// dispSetBrightnessRaw writes the level, kept inside 1..255 whatever it is
// asked: 0 would switch the backlight off.
func dispSetBrightnessRaw(raw int) error {
	if raw < 1 {
		raw = 1
	}
	if raw > dispBrightnessMax {
		raw = dispBrightnessMax
	}
	_, err := dispIoctl(dispLCDSetBrightness, uintptr(raw))
	return err
}

// dispBacklightOff switches the backlight itself off for a dark screen
// (devpower.go):
//
//	DISP_LCD_BACKLIGHT_DISABLE (0x105)  arg -> unsigned long[4]{screen, 0, 0, 0}
//
// A driver that refuses the command gets level 0 instead (which the setter
// above never writes), and the level it had is returned for dispBacklightOn;
// 0 means the driver's switch did it.
func dispBacklightOff() (int, error) {
	if _, err := dispIoctl(dispLCDBacklightDisable, 0); err == nil {
		return 0, nil
	}
	raw, err := dispBrightnessRaw()
	if err != nil {
		return 0, err
	}
	if raw <= 0 {
		return 0, fmt.Errorf("%s reports no backlight level to come back to", dispDevicePath)
	}
	if _, err := dispIoctl(dispLCDSetBrightness, 0); err != nil {
		return 0, err
	}
	return raw, nil
}

// dispBacklightOn undoes dispBacklightOff: DISP_LCD_BACKLIGHT_ENABLE (0x104),
// and the remembered level when the fallback took it to 0.
func dispBacklightOn(raw int) error {
	_, err := dispIoctl(dispLCDBacklightEnable, 0)
	if raw > 0 {
		return dispSetBrightnessRaw(raw)
	}
	return err
}

// dispBrightnessAvailable reports whether /dev/disp answers the brightness
// query with a lit backlight. A driver without the query answers 0 (its
// default return), which is no answer: JukaHub is drawing, so the backlight is
// on.
func dispBrightnessAvailable() bool {
	raw, err := dispBrightnessRaw()
	return err == nil && raw > 0
}
