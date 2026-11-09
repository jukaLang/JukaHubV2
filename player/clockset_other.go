//go:build !linux

package main

// clockset_other.go — Windows and macOS own their clocks: JukaHub only
// reports how far off they are (ntpsync.go) and never sets them.

import (
	"errors"
	"time"
)

// clockSetPermitted reports whether this process may set the clock: never
// here.
func clockSetPermitted() bool { return false }

// clockSetSystem refuses: the operating system sets this clock.
func clockSetSystem(time.Time) error {
	return errors.New("the operating system sets the clock here")
}
