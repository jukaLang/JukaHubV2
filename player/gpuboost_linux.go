//go:build linux

package main

// gpuboost_linux.go — the hardware half of GPU boost (gpuboost.go): Xbox
// boost's GPU plan (xboxboost_linux.go), and whether the device has a node for
// it at all.

import (
	"os"
	"strings"
)

// gpuBoostPlan is the GPU's devfreq writes for one boosted launch: Xbox boost's
// own plan, read fresh, so a node already at performance is left out.
func gpuBoostPlan() []saverSysfsWrite { return xboxBoostGPUPlan(xboxBoostDevfreqRoot) }

// gpuBoostNodePresent reports whether a GPU devfreq node exists — a name with
// "gpu" under the devfreq class, the rule xboxBoostGPUPlan applies — so the
// Settings row is only offered where the boost can do something.
func gpuBoostNodePresent() bool {
	entries, err := os.ReadDir(xboxBoostDevfreqRoot)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name()), "gpu") {
			return true
		}
	}
	return false
}
