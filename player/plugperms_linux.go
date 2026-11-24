package main

// plugperms_linux.go — the network wall for a plugin's script
// (plugperms.go): a script that was not allowed "network" starts in a
// network namespace of its own, which has no interface but a downed
// loopback. Making one needs root, which JukaHub is on the handheld; a
// kernel built without network namespaces refuses, and the script then runs
// as before, with the page saying so.

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// plugNetworkBlockable reports whether a script can be kept off the network
// here.
func plugNetworkBlockable() bool {
	return os.Geteuid() == 0 && !plugNetBlockFailed.Load()
}

// plugSandbox prepares cmd for a plugin allowed granted; it reports whether
// the network is walled off.
func plugSandbox(cmd *exec.Cmd, granted []string) bool {
	if plugHasPerm(granted, plugPermNetwork) || !plugNetworkBlockable() {
		return false
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNET
	return true
}

// plugSandboxRefused reports a start the kernel refused because of the
// namespace (no CONFIG_NET_NS, or no right to make one).
func plugSandboxRefused(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS)
}
