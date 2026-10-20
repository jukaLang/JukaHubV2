//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd

package main

import "os/exec"

func configureGameChild(cmd *exec.Cmd) {}
