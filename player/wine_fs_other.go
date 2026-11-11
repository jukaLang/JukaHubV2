//go:build !linux

package main

// wineFSTakesLinks has no file-system type to read off Linux: the caller
// tests with a real link.
func wineFSTakesLinks(path string) (links, known bool) { return false, false }
