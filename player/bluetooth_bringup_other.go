//go:build !linux

package main

import "syscall"

// btHCIUp has no HCI socket to ask here; the callers fall back to the
// bluetoothctl probe, and Bluetooth pairing is a Linux feature anyway.
func btHCIUp() (up bool, ok bool) { return false, false }

// btDetachedAttr has nothing to set on a system without the Linux bring-up.
func btDetachedAttr() *syscall.SysProcAttr { return nil }
