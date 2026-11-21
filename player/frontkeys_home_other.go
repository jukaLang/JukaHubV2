//go:build !linux

package main

// frontkeys_home_other.go — no Home key reader off Linux: only the Smart Pro S
// has the key, and frontHomeStart never gets here elsewhere. The rows stay
// hidden (frontHome.found).

// frontHomeStartReaders opens nothing.
func frontHomeStartReaders(int) {}
