//go:build !linux

package main

// ramsearch_other.go — the Memory panel needs /proc/<pid>/mem, which only the
// Linux build has. Every other platform keeps the panel, its parsing and its
// scan arithmetic (they are platform-neutral and tested there) and reports the
// missing backend when a target is picked.

import "errors"

// ramOpenProcess fails on platforms without a process-memory backend.
func ramOpenProcess(pid int) (ramAccess, error) {
	return nil, errors.New("reading another process's memory is only supported on Linux")
}
