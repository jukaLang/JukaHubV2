package main

// usbotg_linux.go — the mount syscalls behind the USB & OTG screen.
//
// JukaHub runs as root on the handheld, so it mounts and unmounts USB drives
// itself instead of shelling out to mount(8), whose busybox build differs
// between firmwares. Errors are folded into a few kinds the caller acts on: a
// filesystem the kernel lacks or a wrong guess moves on to the next type, a
// write-protected drive is retried read-only, a busy mount is detached lazily.

import (
	"errors"
	"syscall"
)

// usbOtgMountFS mounts dev at target. nosuid/nodev because a stick is not
// trusted to carry setuid programs or device nodes.
func usbOtgMountFS(dev, target, fstype, opts string, readOnly bool) error {
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV)
	if readOnly {
		flags |= syscall.MS_RDONLY
	}
	return syscall.Mount(dev, target, fstype, flags, opts)
}

// usbOtgUnmount unmounts target; lazy detaches it now and lets the kernel
// finish when the last open file closes.
func usbOtgUnmount(target string, lazy bool) error {
	flags := 0
	if lazy {
		flags = syscall.MNT_DETACH
	}
	return syscall.Unmount(target, flags)
}

// usbOtgSync flushes every filesystem's dirty pages.
func usbOtgSync() { syscall.Sync() }

// usbOtgErrKind names what a mount error means for the next step: "nofs"
// (the kernel has no such filesystem), "wrongfs" (not this type, or an option
// it refuses), "readonly", "busy", or "" for anything else.
func usbOtgErrKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, syscall.ENODEV):
		return "nofs"
	case errors.Is(err, syscall.EINVAL):
		return "wrongfs"
	case errors.Is(err, syscall.EROFS), errors.Is(err, syscall.EACCES):
		return "readonly"
	case errors.Is(err, syscall.EBUSY):
		return "busy"
	}
	return ""
}
