package main

// DetectDeviceFirstRun is called once from main init order after any early
// logging config is ready. It used to re-read /proc/device-tree/model and run
// the marketing-name classifier on it alone, which meant it could disagree with
// DetectDevice on exactly the images that need the extra sources — a Brick whose
// tree names only the SoC. It now records the same answer the rest of the app
// uses.

import (
	"sync"
)

var (
	detectedDevice     DeviceModel
	detectedDeviceOnce sync.Once
)

func DetectDeviceFirstRun() {
	if detectedDevice != "" {
		return
	}
	detectedDeviceOnce.Do(func() {
		detectedDevice = DetectDevice()
	})
}
