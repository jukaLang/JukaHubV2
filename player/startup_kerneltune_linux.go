//go:build linux

package main

// startup_kerneltune_linux.go — two kernel settings for a quick start, set
// before SDL starts on a TrimUI running its own firmware:
//
//   - RCU grace periods expedited (/sys/kernel/rcu_expedited = 1). Closing an
//     input device waits for a grace period, tens of milliseconds each at the
//     normal pace on the Allwinner 4.9 kernels, and SDL's joystick setup opens
//     and closes every input device; expedited, each wait is a few
//     milliseconds.
//   - The console log level at 1 (the first number of /proc/sys/kernel/printk).
//     The stock command line's loglevel=8 sends every driver message to the
//     board's serial port at 115200 baud, and the driver waits while it goes
//     out — the GPU, sound and input drivers JukaHub's start opens included.
//     The kernel's message buffer (dmesg) still gets every message.
//
// What was there is put back when JukaHub quits. The JukaHub OS boot hooks on
// a stock Smart Pro set both for the whole boot (stock_fastboot's
// jukahub-quietboot), and Silent boot (bootenv.go) starts the kernel at level
// 1: then there is nothing to change and nothing to put back.
// JUKAHUB_KERNEL_TUNE=0 leaves both alone (for a serial console, say).

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	kernelTuneRCUPath    = "/sys/kernel/rcu_expedited"
	kernelTunePrintkPath = "/proc/sys/kernel/printk"
)

var kernelTuneOnce sync.Once

// startupKernelTune sets both before SDL (main.go). Cheap: two small reads
// and at most two writes.
func startupKernelTune() {
	kernelTuneOnce.Do(func() {
		if os.Getenv("JUKAHUB_KERNEL_TUNE") == "0" || !IsTrimuiDevice() {
			return
		}
		var undo []func()
		var done []string
		if data, err := os.ReadFile(kernelTuneRCUPath); err == nil && strings.TrimSpace(string(data)) == "0" {
			if err := os.WriteFile(kernelTuneRCUPath, []byte("1\n"), 0o644); err == nil {
				undo = append(undo, func() { _ = os.WriteFile(kernelTuneRCUPath, []byte("0\n"), 0o644) })
				done = append(done, "RCU expedited")
			}
		}
		if data, err := os.ReadFile(kernelTunePrintkPath); err == nil {
			if fields := strings.Fields(string(data)); len(fields) > 0 {
				if level, err := strconv.Atoi(fields[0]); err == nil && level > 1 {
					// Only the first number (the console's level) is written.
					if err := os.WriteFile(kernelTunePrintkPath, []byte("1\n"), 0o644); err == nil {
						old := fields[0]
						undo = append(undo, func() { _ = os.WriteFile(kernelTunePrintkPath, []byte(old+"\n"), 0o644) })
						done = append(done, "console log level "+old+" → 1")
					}
				}
			}
		}
		if len(undo) == 0 {
			return
		}
		logInfo("startup", "kernel tune for the start: %s (put back at exit)", strings.Join(done, ", "))
		Cleanup = append(Cleanup, func() {
			for _, fn := range undo {
				fn()
			}
		})
	})
}
