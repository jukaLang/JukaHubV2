//go:build !windows

package main

// emuWinRegistryDirs is the Windows install-registry lookup
// (emuwinregistry_windows.go); other platforms have no such registry.
func emuWinRegistryDirs(names []string) []string { return nil }
