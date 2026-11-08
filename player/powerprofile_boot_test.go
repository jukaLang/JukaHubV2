package main

// powerprofile_boot_test.go — the exit-time CPU restore, against a fake /sys.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Whatever a launch or a child left the CPU on, the teardown must put back the
// governor and the frequency ceiling the firmware booted with.
func TestBootCPUSettingsRestorePutsTheFirmwareBack(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		fakeCPUWithFreq(t, root, i, "1800000", "2000000", true)
	}
	prev := cpufreqRoot
	cpufreqRoot = root
	t.Cleanup(func() {
		cpufreqRoot = prev
		resetBootCPUSettings()
	})

	captureBootCPUSettings()

	// Simulate what a game launch (or a child that never returned) leaves.
	for _, path := range cpuGovernorPaths(root) {
		if err := writeGovernor(path, "performance"); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range cpufreqDirs(root) {
		if err := os.WriteFile(filepath.Join(dir, "scaling_max_freq"), []byte("2000000\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	detail, err := restoreBootCPUSettings()
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !strings.Contains(detail, "schedutil") {
		t.Fatalf("detail = %q, want it to name the restored governor", detail)
	}

	for _, path := range cpuGovernorPaths(root) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(raw)); got != "schedutil" {
			t.Fatalf("%s = %q, want the boot governor back", path, got)
		}
	}
	for _, dir := range cpufreqDirs(root) {
		raw, err := os.ReadFile(filepath.Join(dir, "scaling_max_freq"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(raw)); got != "1800000" {
			t.Fatalf("%s ceiling = %q, want the boot ceiling back", dir, got)
		}
	}
}

// A launch that never returned (the hub died mid-game) left its marker: the next
// start puts the values back before recording what the firmware booted with,
// writes nothing outside cpufreqRoot whatever the marker says, and removes it.
func TestBootCPUMarkerRecoversAnInterruptedLaunch(t *testing.T) {
	root := t.TempDir()
	gov := fakeCPUWithFreq(t, root, 0, "1800000", "2000000", true)
	maxFreq := filepath.Join(filepath.Dir(gov), "scaling_max_freq")
	outside := filepath.Join(t.TempDir(), "scaling_governor")
	if err := os.WriteFile(outside, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "launch-cpufreq.json")
	prevRoot, prevPath, prevEnabled := cpufreqRoot, bootCPUMarkerPath, bootCPUMarkerEnabled
	cpufreqRoot = root
	bootCPUMarkerPath = func() string { return marker }
	bootCPUMarkerEnabled = func() bool { return true }
	t.Cleanup(func() {
		cpufreqRoot, bootCPUMarkerPath, bootCPUMarkerEnabled = prevRoot, prevPath, prevEnabled
		resetBootCPUSettings()
	})

	// What a launch leaves while the game runs: performance at the top clock,
	// with the values it replaced in the marker.
	bootCPUMarkerWrite(
		[]cpuGovernorSave{{path: gov, value: "schedutil"}, {path: outside, value: "performance"}},
		[]cpuFrequencySave{{path: maxFreq, value: "1800000"}},
	)
	if err := writeGovernor(gov, "performance"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(maxFreq, []byte("2000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resetBootCPUSettings()
	captureBootCPUSettings()

	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "schedutil" {
		t.Fatalf("governor = %q, want the pre-launch schedutil back", strings.TrimSpace(string(raw)))
	}
	if raw, _ := os.ReadFile(maxFreq); strings.TrimSpace(string(raw)) != "1800000" {
		t.Fatalf("ceiling = %q, want the pre-launch 1800000 back", strings.TrimSpace(string(raw)))
	}
	if raw, _ := os.ReadFile(outside); strings.TrimSpace(string(raw)) != "untouched" {
		t.Fatal("a marker entry outside cpufreqRoot was written")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the marker outlived its recovery")
	}
	// And what the boot recorded is the firmware's, not the crashed launch's.
	bootCPUSettings.mu.Lock()
	recorded := ""
	if len(bootCPUSettings.governors) > 0 {
		recorded = bootCPUSettings.governors[0].value
	}
	bootCPUSettings.mu.Unlock()
	if recorded != "schedutil" {
		t.Fatalf("boot recorded %q as the firmware governor", recorded)
	}
}

// Only a governor name or a frequency, in the expected file under the cpufreq
// root, is accepted from the shared temp directory.
func TestBootCPUMarkerSafe(t *testing.T) {
	prev := cpufreqRoot
	cpufreqRoot = "/sys/devices/system/cpu"
	t.Cleanup(func() { cpufreqRoot = prev })
	gov := "/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"
	ceil := "/sys/devices/system/cpu/cpufreq/policy0/scaling_max_freq"
	for _, tc := range []struct {
		entry bootCPUMarkerEntry
		file  string
		ok    bool
	}{
		{bootCPUMarkerEntry{Path: gov, Value: "schedutil"}, "scaling_governor", true},
		{bootCPUMarkerEntry{Path: ceil, Value: "1800000"}, "scaling_max_freq", true},
		{bootCPUMarkerEntry{Path: ceil, Value: "fast"}, "scaling_max_freq", false},
		{bootCPUMarkerEntry{Path: gov, Value: "schedutil; reboot"}, "scaling_governor", false},
		{bootCPUMarkerEntry{Path: "/etc/scaling_governor", Value: "schedutil"}, "scaling_governor", false},
		{bootCPUMarkerEntry{Path: "/sys/devices/system/cpu/../../etc/scaling_governor", Value: "x"}, "scaling_governor", false},
		{bootCPUMarkerEntry{Path: gov, Value: ""}, "scaling_governor", false},
		{bootCPUMarkerEntry{Path: ceil, Value: "1800000"}, "scaling_governor", false},
	} {
		if got := bootCPUMarkerSafe(tc.entry, tc.file); got != tc.ok {
			t.Errorf("bootCPUMarkerSafe(%+v, %s) = %v, want %v", tc.entry, tc.file, got, tc.ok)
		}
	}
}

// A desktop has no cpufreq at all; the teardown says so instead of claiming a
// restore it did not make.
func TestRestoreBootCPUSettingsWithoutCpufreq(t *testing.T) {
	prev := cpufreqRoot
	cpufreqRoot = t.TempDir()
	t.Cleanup(func() {
		cpufreqRoot = prev
		resetBootCPUSettings()
	})

	captureBootCPUSettings()
	detail, err := restoreBootCPUSettings()
	if err != nil {
		t.Fatalf("restore on a desktop: %v", err)
	}
	if !strings.Contains(detail, "no cpufreq") {
		t.Fatalf("detail = %q, want an honest no-cpufreq line", detail)
	}
}
