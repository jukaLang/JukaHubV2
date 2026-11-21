package main

// cardcheck_linux.go — the two system calls behind Card check (cardcheck.go):
// the kernel's log, read through syslog(2) the way dmesg reads it, and the
// errno that marks a read the card itself failed.

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// syslog(2) actions (linux/syslog.h).
const (
	cardCheckSyslogReadAll    = 3
	cardCheckSyslogSizeBuffer = 10
)

// cardCheckKernelLog is the kernel's log ring as dmesg prints it.
func cardCheckKernelLog() (string, error) {
	size, err := unix.Klogctl(cardCheckSyslogSizeBuffer, nil)
	if err != nil || size <= 0 {
		size = 1 << 20
	}
	if size > 8<<20 {
		size = 8 << 20
	}
	buf := make([]byte, size)
	n, err := unix.Klogctl(cardCheckSyslogReadAll, buf)
	if err != nil {
		return "", err
	}
	if n < 0 || n > len(buf) {
		return "", errors.New("the kernel log came back malformed")
	}
	return string(buf[:n]), nil
}

// cardCheckIsIOError reports an error the card itself caused: EIO, and the
// ones a card that went away gives.
func cardCheckIsIOError(err error) bool {
	return errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.ENODEV) ||
		errors.Is(err, syscall.EBADMSG)
}
