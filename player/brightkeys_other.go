//go:build !linux

package main

// brightkeys_other.go — Menu + volume off Linux (brightkeys.go): no kernel
// input devices are read there, so nothing is ever asked.

import "os"

func brightKeysMenuHeldNow(*os.File) (held, ok bool) { return false, false }

func brightKeysKeymonRunning() bool { return false }
