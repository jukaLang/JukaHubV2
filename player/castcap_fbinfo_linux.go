//go:build linux

package main

// castcap_fbinfo_linux.go — the framebuffer driver's own description of the
// panel, for Screen Share's game reader (castcap_fb.go). The ioctl knows what
// sysfs does not: where the colour channels sit in a pixel.

import (
	"encoding/binary"
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	castFBIOGetVScreenInfo = 0x4600 // FBIOGET_VSCREENINFO
	castFBIOGetFScreenInfo = 0x4602 // FBIOGET_FSCREENINFO
	// The kernel structs are 160 bytes (var) and 68/80 bytes (fix, 32/64-bit);
	// the buffers are sized for the larger layout.
	castFBVarInfoSize = 160
	castFBFixInfoSize = 80
)

// castIoctl asks the framebuffer driver to fill buf. The buffers are heap
// slices, which never move while the call runs.
func castIoctl(f *os.File, req uintptr, buf []byte) error {
	if f == nil || len(buf) == 0 {
		return errors.New("no framebuffer to ask")
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return errno
	}
	return nil
}

// castFBIoctlGeom reads the panel's geometry and pixel format from the driver.
func castFBIoctlGeom(f *os.File) (castFBGeom, error) {
	v := make([]byte, castFBVarInfoSize)
	if err := castIoctl(f, castFBIOGetVScreenInfo, v); err != nil {
		return castFBGeom{}, err
	}
	fix := make([]byte, castFBFixInfoSize)
	if err := castIoctl(f, castFBIOGetFScreenInfo, fix); err != nil {
		return castFBGeom{}, err
	}
	return castFBGeomFromInfo(v, fix, int(unsafe.Sizeof(uintptr(0)))), nil
}

// castFBIoctlPan is the current pan offset: which half of a double-buffered
// panel is on screen.
func castFBIoctlPan(f *os.File) (int, int, error) {
	v := make([]byte, castFBVarInfoSize)
	if err := castIoctl(f, castFBIOGetVScreenInfo, v); err != nil {
		return 0, 0, err
	}
	return int(binary.LittleEndian.Uint32(v[16:])), int(binary.LittleEndian.Uint32(v[20:])), nil
}
