//go:build !linux

package main

// cardcheck_other.go — Card check off Linux. The check is a handheld feature
// (cardCheckSupported is false here); these only keep the shared code
// compiling.

import "errors"

func cardCheckKernelLog() (string, error) {
	return "", errors.New("no kernel log on this system")
}

func cardCheckIsIOError(err error) bool { return false }
