//go:build linux

package main

import (
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// bzOpenPTY opens a pseudo-terminal for a bluetoothctl session
// (bluetooth_devices.go): the master end stays with JukaHub, the other end
// becomes the child's terminal. On a terminal bluetoothctl behaves exactly as
// it does for a person typing, printing each line as it happens instead of
// leaving it in a pipe buffer until it exits.
//
// The ioctls run through SyscallConn rather than on master.Fd(): Fd switches
// the descriptor to blocking mode and takes it out of Go's poller, after which
// a Read waiting on the master could no longer be ended by closing it.
func bzOpenPTY() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	rc, err := master.SyscallConn()
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	n := -1
	var ioErr error
	ctlErr := rc.Control(func(fd uintptr) {
		if ioErr = unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0); ioErr != nil {
			return
		}
		if n, ioErr = unix.IoctlGetInt(int(fd), unix.TIOCGPTN); ioErr != nil {
			return
		}
		// A wide terminal keeps readline from wrapping a long line with cursor
		// codes in the middle of it.
		_ = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: 50, Col: 250})
	})
	if ctlErr == nil {
		ctlErr = ioErr
	}
	if ctlErr != nil {
		_ = master.Close()
		return nil, nil, ctlErr
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

// bzPTYAttr makes the terminal the child's controlling terminal, in a session
// of its own (Ctty is the child's standard input).
func bzPTYAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}
