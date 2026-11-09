//go:build windows

package main

// winbatch_windows.go — starting a batch file on the command line
// winBatchCommandLine built (winbatch.go).

import (
	"os"
	"os/exec"
	"syscall"
)

// winBatchCommand is a command that runs script with args through cmd.exe.
// With SysProcAttr.CmdLine set, Go hands that line to CreateProcess exactly as
// built, instead of quoting Args by rules cmd.exe does not follow. The cmd.exe
// is picked once (winBatchComspec), so the program started and the one the
// line names are the same even when %ComSpec% is unusable.
func winBatchCommand(script string, args ...string) (*exec.Cmd, error) {
	comspec := winBatchComspec(os.Getenv("ComSpec"), os.Getenv("SystemRoot"))
	line, err := winBatchCommandLine(comspec, script, args)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	return cmd, nil
}
