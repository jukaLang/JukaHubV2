//go:build !linux

package main

import (
	"errors"
	"os"
	"syscall"
)

// bzOpenPTY has no pseudo-terminal to offer here; bluetoothctl sessions fall
// back to plain pipes (and Bluetooth pairing is a Linux feature anyway).
func bzOpenPTY() (*os.File, *os.File, error) {
	return nil, nil, errors.New("no pseudo-terminal on this system")
}

// bzPTYAttr has nothing to set without a terminal.
func bzPTYAttr() *syscall.SysProcAttr { return nil }
