//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DRM_IOCTL_SET_MASTER and DRM_IOCTL_DROP_MASTER: _IO('d', 0x1e) and
// _IO('d', 0x1f) from the kernel's drm.h.
const (
	drmIoctlSetMaster  = 0x641e
	drmIoctlDropMaster = 0x641f
)

// drmCardFDs lists this process's open descriptors on a DRM primary node
// (/dev/dri/cardN). Render nodes cannot hold master and are left out.
func drmCardFDs() []int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil
	}
	var fds []int
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil || !strings.HasPrefix(target, "/dev/dri/card") {
			continue
		}
		fds = append(fds, fd)
	}
	return fds
}

func drmIoctl(fd int, req uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, 0); errno != 0 {
		return errno
	}
	return nil
}

// drmDropMaster drops master on every card descriptor that holds it and
// returns those; a descriptor that is not master answers EINVAL and is skipped.
func drmDropMaster() []int {
	var dropped []int
	for _, fd := range drmCardFDs() {
		if err := drmIoctl(fd, drmIoctlDropMaster); err == nil {
			dropped = append(dropped, fd)
		}
	}
	return dropped
}

// drmSetMaster takes master back on the descriptors drmDropMaster returned.
func drmSetMaster(fds []int) error {
	var failed []string
	for _, fd := range fds {
		if err := drmIoctl(fd, drmIoctlSetMaster); err != nil {
			failed = append(failed, fmt.Sprintf("fd %d: %v", fd, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return nil
}
