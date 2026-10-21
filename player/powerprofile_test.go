package main

// powerprofile_test.go — the governor switch, against a fake /sys tree.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCPU creates <root>/cpuN/cpufreq/{scaling_governor,available_governors}.
func fakeCPU(t *testing.T, root string, index int, current, available string, writable bool) string {
	t.Helper()
	dir := filepath.Join(root, "cpu"+testItoa(index), "cpufreq")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gov := filepath.Join(dir, "scaling_governor")
	if err := os.WriteFile(gov, []byte(current+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "available_governors"), []byte(available+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if !writable {
		os.Chmod(gov, 0o444)
	}
	return gov
}

func testItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestCPUGovernorPathsOrdered(t *testing.T) {
	root := t.TempDir()
	fakeCPU(t, root, 0, "schedutil", "schedutil performance", true)
	fakeCPU(t, root, 2, "schedutil", "schedutil performance", true)
	fakeCPU(t, root, 10, "schedutil", "schedutil performance", true)
	// Not a CPU entry, and a CPU without cpufreq: both must be ignored.
	os.MkdirAll(filepath.Join(root, "cpuidle"), 0o755)
	os.MkdirAll(filepath.Join(root, "cpu1"), 0o755)

	paths := cpuGovernorPaths(root)
	if len(paths) != 3 {
		t.Fatalf("paths = %v", paths)
	}
	// Numeric order: cpu10 last, not between cpu0 and cpu2.
	if !strings.Contains(paths[0], "cpu0") || !strings.Contains(paths[1], "cpu2") || !strings.Contains(paths[2], "cpu10") {
		t.Errorf("order = %v", paths)
	}
	if cpuGovernorPaths(filepath.Join(root, "missing")) != nil {
		t.Error("a missing root returned paths")
	}
}

func TestChooseGovernor(t *testing.T) {
	available := []string{"schedutil", "ondemand", "powersave", "performance"}
	if got := chooseGovernor(powerProfilePerformance, available); got != "performance" {
		t.Errorf("performance = %q", got)
	}
	if got := chooseGovernor(powerProfileBattery, available); got != "powersave" {
		t.Errorf("battery = %q", got)
	}
	// Battery falls back through schedutil then ondemand, and says so when the
	// CPU offers neither.
	if got := chooseGovernor(powerProfileBattery, []string{"schedutil", "performance"}); got != "schedutil" {
		t.Errorf("battery fallback = %q", got)
	}
	if got := chooseGovernor(powerProfileBattery, []string{"ondemand"}); got != "ondemand" {
		t.Errorf("battery second fallback = %q", got)
	}
	if got := chooseGovernor(powerProfileBattery, []string{"interactive"}); got != "" {
		t.Errorf("unknown governor list = %q, want none", got)
	}
	if got := chooseGovernor(powerProfilePerformance, nil); got != "" {
		t.Errorf("empty list = %q", got)
	}
}

func TestApplyPowerProfile(t *testing.T) {
	root := t.TempDir()
	gov0 := fakeCPU(t, root, 0, "schedutil", "schedutil powersave performance", true)
	gov1 := fakeCPU(t, root, 1, "schedutil", "schedutil powersave performance", true)
	prev := cpufreqRoot
	cpufreqRoot = root
	t.Cleanup(func() { cpufreqRoot = prev })

	applied, err := applyPowerProfile(powerProfilePerformance, powerProfileConfig{Enabled: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) != 2 || applied[0] != "performance" {
		t.Errorf("applied = %v", applied)
	}
	for _, path := range []string{gov0, gov1} {
		raw, _ := os.ReadFile(path)
		if strings.TrimSpace(string(raw)) != "performance" {
			t.Errorf("%s = %q", path, raw)
		}
	}

	if _, err := applyPowerProfile(powerProfileBattery, powerProfileConfig{Enabled: true}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	raw, _ := os.ReadFile(gov0)
	if strings.TrimSpace(string(raw)) != "powersave" {
		t.Errorf("battery governor = %q", raw)
	}
}

func TestApplyPowerProfileOverridesAndErrors(t *testing.T) {
	root := t.TempDir()
	gov := fakeCPU(t, root, 0, "schedutil", "schedutil performance", true)
	prev := cpufreqRoot
	cpufreqRoot = root
	t.Cleanup(func() { cpufreqRoot = prev })

	// A configured name that the CPU advertises wins.
	cfg := powerProfileConfig{Enabled: true, PerfGovernor: "schedutil"}
	if _, err := applyPowerProfile(powerProfilePerformance, cfg); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "schedutil" {
		t.Errorf("override governor = %q", raw)
	}
	// A configured name the CPU does not offer is ignored, not forced.
	cfg.PerfGovernor = "turbo"
	if _, err := applyPowerProfile(powerProfilePerformance, cfg); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "performance" {
		t.Errorf("unknown override was forced: %q", raw)
	}

	// Read-only governor: the error names the reason, and nothing was changed.
	readonly := t.TempDir()
	fakeCPU(t, readonly, 0, "schedutil", "schedutil performance", false)
	cpufreqRoot = readonly
	if _, err := applyPowerProfile(powerProfilePerformance, powerProfileConfig{Enabled: true}); err == nil {
		t.Error("a read-only governor was accepted")
	} else if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only error = %v", err)
	}

	// No cpufreq at all.
	cpufreqRoot = t.TempDir()
	if _, err := applyPowerProfile(powerProfilePerformance, powerProfileConfig{Enabled: true}); err == nil {
		t.Error("a device without cpufreq applied a profile")
	}
}

func TestProbePowerProfiles(t *testing.T) {
	prevRoot := cpufreqRoot
	root := t.TempDir()
	fakeCPU(t, root, 0, "schedutil", "schedutil powersave performance", true)
	cpufreqRoot = root
	t.Cleanup(func() { cpufreqRoot = prevRoot })

	state := probePowerProfiles(powerProfileConfig{Enabled: true})
	if !state.Supported || state.Current != "schedutil" {
		t.Errorf("state = %+v", state)
	}
	if line := powerProfileLine(nil); !strings.Contains(line, "performance on launch") {
		t.Errorf("line = %q", line)
	}

	disabled := probePowerProfiles(powerProfileConfig{Enabled: false})
	if disabled.Supported || !strings.Contains(disabled.Reason, "off") {
		t.Errorf("disabled state = %+v", disabled)
	}

	readonlyRoot := t.TempDir()
	fakeCPU(t, readonlyRoot, 0, "schedutil", "schedutil performance", false)
	cpufreqRoot = readonlyRoot
	ro := probePowerProfiles(powerProfileConfig{Enabled: true})
	if ro.Supported || !strings.Contains(ro.Reason, "read-only") {
		t.Errorf("read-only state = %+v", ro)
	}
}

func TestPowerProfileConfigDefaults(t *testing.T) {
	cfg := loadPowerProfileConfig(nil)
	if !cfg.Enabled {
		t.Error("profiles must default to on")
	}
	off := loadPowerProfileConfig(&Config{Variables: Variables{Custom: map[string]interface{}{
		"powerProfiles":       "false",
		"powerProfilePerf":    "boost",
		"powerProfileBattery": "eco",
	}}})
	if off.Enabled || off.PerfGovernor != "boost" || off.BatteryGovernor != "eco" {
		t.Errorf("configured = %+v", off)
	}
}

// A launch that cannot switch profiles must still return a usable restore
// function, and the depth counter must not be left raised.
func TestBeginGamePowerProfileUnsupported(t *testing.T) {
	prevRoot := cpufreqRoot
	cpufreqRoot = t.TempDir() // no CPUs
	t.Cleanup(func() { cpufreqRoot = prevRoot })

	launchPowerProfile.mu.Lock()
	launchPowerProfile.depth = 0
	launchPowerProfile.mu.Unlock()

	restore := beginGamePowerProfile(nil)
	restore()
	restore() // idempotent: a second call must not underflow
	launchPowerProfile.mu.Lock()
	depth := launchPowerProfile.depth
	launchPowerProfile.mu.Unlock()
	if depth != 0 {
		t.Errorf("depth = %d after restore", depth)
	}
}
