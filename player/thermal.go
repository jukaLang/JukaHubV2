package main

// thermal.go watches the handheld's CPU thermal cooling devices. The kernel's
// cooling state is a direct signal that thermal policy has reduced CPU
// capacity; temperature alone is not, since a warm SoC is not necessarily
// being throttled.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const thermalPollInterval = 10 * time.Second

// cpuThermalThrottleState reads the active state of CPU cooling devices. A
// missing thermal interface is unknown, not a claim that the CPU is clear.
func cpuThermalThrottleState() (active, known bool) {
	if runtime.GOOS != "linux" {
		return false, false
	}
	types, _ := filepath.Glob("/sys/class/thermal/cooling_device*/type")
	foundCPU, unreadable := false, false
	for _, typePath := range types {
		data, err := os.ReadFile(typePath)
		if err != nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(string(data)))
		if !strings.Contains(name, "cpu") && !strings.Contains(name, "processor") && !strings.Contains(name, "cpufreq") {
			continue
		}
		foundCPU = true
		stateBytes, err := os.ReadFile(filepath.Join(filepath.Dir(typePath), "cur_state"))
		if err != nil {
			unreadable = true
			continue
		}
		state, err := strconv.Atoi(strings.TrimSpace(string(stateBytes)))
		if err != nil || state < 0 {
			unreadable = true
			continue
		}
		if state > 0 {
			return true, true
		}
	}
	return false, foundCPU && !unreadable
}

// startThermalMonitor reports each transition into CPU thermal throttling
// once. Like the other device pollers, it parks whenever JukaHub is hidden.
func startThermalMonitor() {
	if runtime.GOOS != "linux" || !IsTrimuiDevice() {
		return
	}
	go func() {
		ticker := time.NewTicker(thermalPollInterval)
		defer ticker.Stop()
		wake := subscribeForegroundWake()
		wasKnown, wasActive := false, false
		sample := func() {
			tempC, tempOK := cpuTempValueC()
			active, known := cpuThermalThrottleState()
			if !known {
				return
			}
			if active && (!wasKnown || !wasActive) {
				if tempOK {
					logWarn("power", "CPU thermal throttling started at %.1f °C", tempC)
					ShowToast(fmt.Sprintf("CPU thermal limit active · %.0f °C", tempC), ToastKindWarning)
				} else {
					logWarn("power", "CPU thermal throttling started")
					ShowToast("CPU thermal limit active", ToastKindWarning)
				}
			} else if wasKnown && wasActive && !active {
				if tempOK {
					logInfo("power", "CPU thermal throttling ended at %.1f °C", tempC)
				} else {
					logInfo("power", "CPU thermal throttling ended")
				}
			}
			wasKnown, wasActive = true, active
		}
		if !backgroundPaused() {
			sample()
		}
		for {
			// A foreground wake can request an early refresh; while hidden the
			// monitor parks without reading sysfs.
			if !waitForPoll(ticker.C, wake) {
				continue
			}
			sample()
		}
	}()
}
