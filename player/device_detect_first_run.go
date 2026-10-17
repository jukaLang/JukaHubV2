package main

// DetectDeviceFirstRun is called once from main init order after any early
// logging config is ready. On TrimUI devices it re-reads the device-tree model
// using a slightly broader but still conservative classifier so the detected
// device can be surfaced to the user before the main UI build.

import (
	"os"
	"runtime"
	"sync"
)

var (
	detectedDevice      DeviceModel
	detectedDeviceOnce sync.Once
	detectOnceInit      sync.Once
)

func DetectDeviceFirstRun() {
	// best-effort: if detection already ran, this is a no-op
	if detectedDevice != "" {
		return
	}
	detectOnceInit.Do(func() {
		detectedDeviceOnce = sync.Once{}
	})
	detectedDeviceOnce.Do(func() {
		raw, _ := os.ReadFile("/proc/device-tree/model")
		detectedDevice = detectDeviceForSystem(runtime.GOOS, string(raw))
	})
}
