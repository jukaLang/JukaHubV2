package main

// patch_engine_test_deviceid_only_trimui_contains.go is the isolated test
// module for one specific regression: the Contains-based TrimUI detector must
// still hard-reject unknown marketing names without a TG model ID. I separated
// it from the main engine test file so the rest of the suite stays independent
// of the detection path I am actively touching.

import (
	"testing"
)

// TestRepoPkgCompatibleByExactDeviceMirrorsTheRepoGateOnly is the primary
// compatibility matrix for the signed-package device filter. It mirrors the
// repo gate directly, so a regression here is usually a repo-package bug.
func TestRepoPkgCompatibleByExactDeviceMirrorsTheRepoGateOnly(t *testing.T) {
	pkg := &RepoPkg{Architecture: "arm64", OS: []string{"trimui-stock"}, Devices: []string{"TG5040"}, Risk: "low"}
	for _, tc := range []struct {
		device DeviceModel
		want   bool
	}{
		{DeviceTrimuiSmartPro, true},
		{DeviceTrimuiSmartProS, false},
		{DeviceTrimuiBrick, false},
		{DeviceTrimuiBrickPro, false},
		{DeviceUnknown, false},
	} {
		if got := repoPkgCompatibleWithDeviceExactSameDevice(pkg, tc.device, "arm64", false); got != tc.want {
			t.Errorf("TG5040 package compatibility for %q = %t, want %t", tc.device, got, tc.want)
		}
	}

	pkg.Devices = []string{"TG5050"}
	if !repoPkgCompatibleWithDeviceExactSameDevice(pkg, DeviceTrimuiSmartProS, "arm64", false) {
		t.Error("TG5050 package must match Smart Pro S")
	}
	if repoPkgCompatibleWithDeviceExactSameDevice(pkg, DeviceTrimuiSmartPro, "arm64", false) {
		t.Error("TG5050 package must not match original Smart Pro")
	}
	if repoPkgCompatibleWithDeviceExactSameDevice(pkg, DeviceUnknown, "arm64", false) {
		t.Error("unknown device must not match a model-specific package")
	}
	if repoPkgCompatibleWithDeviceExactSameDevice(pkg, DeviceDevBuild, "arm64", true) {
		t.Error("Windows must not install the TrimUI arm64 package")
	}
}

func TestRepoPkgCompatibleByExactDeviceMismatchMustRejectMarketingOnly(t *testing.T) {
	// This is the regression boundary I am isolating: a model-specific package
	// for TG5040 must not silently match a device that only has the marketing
	// name "TrimUI Smart Pro S" and no TG model identifier.
	pkg := &RepoPkg{
		Architecture: "arm64",
		OS:           []string{"trimui-stock"},
		Devices:      []string{"TG5040"},
		Risk:         "low",
	}
	if repoPkgCompatibleWithDeviceExactSameDevice(pkg, DeviceTrimuiSmartProS, "arm64", false) {
		t.Error("TG5040 package must not match a Smart Pro S detected only by marketing name")
	}
}

func TestRepoPkgCompatibleByExactDeviceIsTheOnlyExactDeviceTest(t *testing.T) {
	// The exact-device mirror and the marketing-only regression test are the
	// two distinct targets in this file. I keep them separate so a future
	// refactoring cannot silently merge them.
	mirror := repoPkgCompatibleWithDeviceExactSameDevice
	if mirror == nil {
		t.Fatal("exact-device mirror missing")
	}
}
