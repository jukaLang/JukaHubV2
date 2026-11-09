//go:build !windows

package main

// winbatch_other.go — batch files run only on Windows; the shared launchers
// ask for one only there.

import (
	"errors"
	"os/exec"
)

// winBatchCommand refuses: there is no cmd.exe here.
func winBatchCommand(script string, args ...string) (*exec.Cmd, error) {
	return nil, errors.New("batch files run only on Windows")
}
