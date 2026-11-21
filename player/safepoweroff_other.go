//go:build !linux

package main

// safepoweroff_other.go — Safe power off is the Linux handheld's
// (safepoweroff.go, safepoweroff_linux.go): a desktop powers itself off.

import "time"

// safePowerOffRun does nothing here.
func safePowerOffRun(verb string, deadline time.Time) func() { return func() {} }
