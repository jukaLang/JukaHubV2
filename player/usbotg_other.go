//go:build !linux

package main

// usbotg_other.go — the USB & OTG screen's mount calls off Linux.
//
// Mounting a drive by hand only makes sense on the handheld; Windows (and
// macOS) mount sticks themselves. These stubs keep the shared code compiling
// and answer a clear error if anything reaches them.

import "errors"

var errUSBOtgHandheldOnly = errors.New("USB drive tools only work on the handheld")

func usbOtgMountFS(dev, target, fstype, opts string, readOnly bool) error {
	return errUSBOtgHandheldOnly
}

func usbOtgUnmount(target string, lazy bool) error { return errUSBOtgHandheldOnly }

func usbOtgSync() {}

func usbOtgErrKind(err error) string { return "" }
