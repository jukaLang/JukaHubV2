//go:build !linux

package main

// startup_other.go — no kernel boot clock outside Linux: the boot shares, the
// first-frame marker and the Settings summary stay empty there.

func startupBootClockKnown() bool { return false }

func startupBootClock() float64 { return 0 }

func startupProcStart() float64 { return 0 }

func startupBootloaderSeconds() float64 { return 0 }

func startupReadTmpFile(string) (string, bool) { return "", false }
