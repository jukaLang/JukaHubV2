//go:build linux && arm64

// A read-only probe by default. --round-trip temporarily changes brightness
// by one raw step and restores the original level before exiting.
// Build with GOOS=linux GOARCH=arm64 CGO_ENABLED=0 and run on the handheld.
package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func dispProbeCall(f *os.File, op, value uintptr) (uintptr, error) {
	args := [4]uintptr{0, value, 0, 0}
	raw, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), op, uintptr(unsafe.Pointer(&args[0])))
	if errno != 0 {
		return 0, errno
	}
	return raw, nil
}

func runProbe() error {
	f, err := os.OpenFile("/dev/disp", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := dispProbeCall(f, 0x103, 0)
	if err != nil {
		return fmt.Errorf("GET brightness: %w", err)
	}
	if len(os.Args) < 2 || os.Args[1] != "--round-trip" {
		fmt.Printf("raw_brightness=%d\n", before)
		return nil
	}
	if before == 0 || before > 255 {
		return fmt.Errorf("unsafe starting brightness %d", before)
	}
	target := before + 1
	if target > 255 {
		target = before - 1
	}
	defer func() {
		if _, err := dispProbeCall(f, 0x102, before); err != nil {
			fmt.Fprintln(os.Stderr, "RESTORE brightness:", err)
		}
	}()
	if _, err := dispProbeCall(f, 0x102, target); err != nil {
		return fmt.Errorf("SET brightness: %w", err)
	}
	after, err := dispProbeCall(f, 0x103, 0)
	if err != nil {
		return fmt.Errorf("GET after SET: %w", err)
	}
	fmt.Printf("before=%d target=%d readback=%d\n", before, target, after)
	return nil
}

func main() {
	if err := runProbe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
