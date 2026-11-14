//go:build !linux

package main

import "time"

func foreignScreenSuspended() bool { return false }
func foreignScreenTick(time.Time)  {}
