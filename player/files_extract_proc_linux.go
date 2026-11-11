//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// feToolProcAttr makes an unpacking program die with JukaHub: a 7zz left
// running after the launcher quits would go on writing into a staging folder
// that nothing will move into place.
func feToolProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

// feRenameNoReplace moves from to to only if nothing has the name to: a name
// found free can be taken again before the move. A filesystem without
// renameat2's RENAME_NOREPLACE reserves the name first (feRenameByReserving).
func feRenameNoReplace(from, to string) error {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return feRenameByReserving(from, to)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
