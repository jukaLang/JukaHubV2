package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The Smart Pro's power_supply entries are symlinks, and DirEntry.IsDir is
// false for a link, so the battery used to read "unknown". These tests build a
// fake power_supply tree, symlinks included, and pin the lookup and the
// capacity fallback.
func TestBatterySysfsDirFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "power_supply")
	devices := filepath.Join(root, "devices")
	if err := os.MkdirAll(filepath.Join(devices, "axp2202-battery"), 0o755); err != nil {
		t.Fatal(err)
	}
	battery := filepath.Join(devices, "axp2202-battery")
	if err := os.WriteFile(filepath.Join(battery, "capacity"), []byte("82\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(battery, "status"), []byte("Discharging\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An AC adapter with no charge level must not win.
	if err := os.MkdirAll(filepath.Join(base, "axp2202-usb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(battery, filepath.Join(base, "axp2202-battery")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(base, "axp2202-usb"), filepath.Join(base, "axp2202-ac")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	dir := batterySysfsDirIn(base)
	if dir == "" {
		t.Fatal("battery directory was not found through the symlink")
	}
	if got := batteryPercentIn(dir); got != 82 {
		t.Fatalf("percent = %d, want 82", got)
	}
}

// Kernels without a `capacity` file still expose charge_now/charge_full.
func TestBatteryPercentFromChargeCounters(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "charge_now"), []byte("3000000"), 0o644)
	os.WriteFile(filepath.Join(dir, "charge_full"), []byte("4000000"), 0o644)
	if got := batteryPercentIn(dir); got != 75 {
		t.Fatalf("percent = %d, want 75", got)
	}
	if batteryLevelFile(dir) != "charge_now" {
		t.Errorf("level file = %q", batteryLevelFile(dir))
	}

	// A missing or zero denominator is unknown, not 0%.
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, "charge_now"), []byte("10"), 0o644)
	os.WriteFile(filepath.Join(empty, "charge_full"), []byte("0"), 0o644)
	if got := batteryPercentIn(empty); got != -1 {
		t.Errorf("percent with zero full = %d, want -1", got)
	}
	if got := batteryPercentIn(""); got != -1 {
		t.Errorf("percent with no dir = %d, want -1", got)
	}
}

// A battery-named entry without any readable level still surfaces, so the
// charging/status files remain usable even when the level really is unknown.
func TestBatterySysfsDirKeepsBatteryWithoutCapacity(t *testing.T) {
	base := t.TempDir()
	battery := filepath.Join(base, "battery")
	if err := os.MkdirAll(battery, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(battery, "status"), []byte("Charging\n"), 0o644)
	os.MkdirAll(filepath.Join(base, "ac"), 0o755)

	if got := batterySysfsDirIn(base); got != battery {
		t.Fatalf("dir = %q, want %q", got, battery)
	}
}
