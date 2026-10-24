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

// fakeCPUWithFreq also gives the policy the frequency files a real CPU has:
// the current ceiling and the chip's top OPP. writable=false makes both the
// governor and the ceiling read-only, the way a locked-down firmware behaves.
func fakeCPUWithFreq(t *testing.T, root string, index int, currentMax, topMax string, writable bool) string {
	t.Helper()
	gov := fakeCPU(t, root, index, "schedutil", "schedutil performance", writable)
	dir := filepath.Dir(gov)
	for name, value := range map[string]string{
		"scaling_max_freq": currentMax,
		"cpuinfo_max_freq": topMax,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !writable {
			os.Chmod(path, 0o444)
		}
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

// The 2 GHz overclock lives in the frequency ceiling: some firmware boots the
// TrimUI Smart Pro at 1.8 GHz although the chip advertises a 2 GHz top OPP, and
// the performance governor can only reach what scaling_max_freq allows.
func TestLaunchRaisesAndRestoresFrequencyCeiling(t *testing.T) {
	root := t.TempDir()
	gov := fakeCPUWithFreq(t, root, 0, "1608000", "2000000", true)
	prevRoot := cpufreqRoot
	cpufreqRoot = root
	resetLaunchPowerProfile()
	t.Cleanup(func() {
		cpufreqRoot = prevRoot
		resetLaunchPowerProfile()
	})

	dir := filepath.Dir(gov)
	maxPath := filepath.Join(dir, "scaling_max_freq")

	release := beginGamePowerProfile(nil)
	if got := readKHzFile(maxPath); got != 2_000_000 {
		t.Fatalf("ceiling = %d, want the 2 GHz top OPP", got)
	}
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "performance" {
		t.Fatalf("governor = %q, want performance", raw)
	}

	release()
	if got := readKHzFile(maxPath); got != 1_608_000 {
		t.Errorf("restored ceiling = %d, want the 1.608 GHz the firmware had", got)
	}
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "schedutil" {
		t.Errorf("restored governor = %q, want the boot governor schedutil", raw)
	}
}

// A configured cap below the top OPP wins — including when the firmware already
// runs at top speed, which is the case a cap exists for. An exactly matching
// ceiling is left untouched so the firmware value can be restored verbatim.
func TestFrequencyCeilingHonoursCapAndSkipsWhenAlreadyHigh(t *testing.T) {
	prevRoot := cpufreqRoot
	t.Cleanup(func() { cpufreqRoot = prevRoot })

	// A configured cap below the top OPP wins.
	root := t.TempDir()
	gov := fakeCPUWithFreq(t, root, 0, "1608000", "2000000", true)
	cpufreqRoot = root
	maxPath := filepath.Join(filepath.Dir(gov), "scaling_max_freq")
	saved, ceiling, err := applyFrequencyCeiling(powerProfileConfig{MaxFreqKHz: 1_800_000})
	if err != nil {
		t.Fatalf("raise: %v", err)
	}
	if ceiling != 1_800_000 || readKHzFile(maxPath) != 1_800_000 {
		t.Fatalf("ceiling = %d (%d on disk), want the configured 1.8 GHz", ceiling, readKHzFile(maxPath))
	}
	restoreFrequencyCeiling(saved)

	// Already at the top: no write, nothing to restore.
	root = t.TempDir()
	fakeCPUWithFreq(t, root, 0, "2000000", "2000000", true)
	cpufreqRoot = root
	saved, ceiling, err = applyFrequencyCeiling(powerProfileConfig{})
	if err != nil {
		t.Fatalf("raise at top: %v", err)
	}
	if len(saved) != 0 || ceiling != 2_000_000 {
		t.Errorf("saved = %v ceiling = %d, want no writes at 2 GHz", saved, ceiling)
	}

	// Booted at the top and asked for less: the ceiling is lowered. Leaving it
	// at 2 GHz would make every cap a no-op on a firmware already at full speed.
	root = t.TempDir()
	gov = fakeCPUWithFreq(t, root, 0, "2000000", "2000000", true)
	cpufreqRoot = root
	loweredPath := filepath.Join(filepath.Dir(gov), "scaling_max_freq")
	saved, ceiling, err = applyFrequencyCeiling(powerProfileConfig{MaxFreqKHz: 1_200_000})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if ceiling != 1_200_000 || readKHzFile(loweredPath) != 1_200_000 {
		t.Fatalf("lowered ceiling = %d (%d on disk), want 1.2 GHz", ceiling, readKHzFile(loweredPath))
	}
	if len(saved) != 1 {
		t.Errorf("saved %d ceilings, want the 2 GHz value to restore", len(saved))
	}
	restoreFrequencyCeiling(saved)
	if got := readKHzFile(loweredPath); got != 2_000_000 {
		t.Errorf("restored ceiling = %d, want the 2 GHz it booted with", got)
	}

	// A read-only ceiling is reported, not silently ignored.
	root = t.TempDir()
	fakeCPUWithFreq(t, root, 0, "1608000", "2000000", false)
	cpufreqRoot = root
	if _, _, err := applyFrequencyCeiling(powerProfileConfig{}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only ceiling error = %v", err)
	}
}

func TestParseProfileMaxFreq(t *testing.T) {
	cases := []struct {
		in   interface{}
		want int64
	}{
		{"", 0}, {"auto", 0}, {"MAX", 0},
		{"2000", 2_000_000}, {"1.8", 1_800_000}, {"1.8ghz", 1_800_000},
		{"2", 2_000_000}, {"1800", 1_800_000}, {"0.408", 408_000},
		{"2000000", 2_000_000}, {"1800000", 1_800_000},
		{"2000mhz", 2_000_000}, {"nonsense", 0}, {-5, 0},
	}
	for _, tc := range cases {
		if got := parseProfileMaxFreqKHz(tc.in); got != tc.want {
			t.Errorf("parse(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
	cfg := loadPowerProfileConfig(&Config{Variables: Variables{Custom: map[string]interface{}{
		"powerProfileMaxFreq": "1800",
	}}})
	if cfg.MaxFreqKHz != 1_800_000 {
		t.Errorf("configured ceiling = %d", cfg.MaxFreqKHz)
	}
}

func TestPowerProfileLineShowsCeiling(t *testing.T) {
	root := t.TempDir()
	fakeCPUWithFreq(t, root, 0, "1800000", "2000000", true)
	prevRoot := cpufreqRoot
	cpufreqRoot = root
	t.Cleanup(func() { cpufreqRoot = prevRoot })

	line := powerProfileLine(nil)
	if !strings.Contains(line, "max 2.0 GHz") || !strings.Contains(line, "performance on launch") {
		t.Errorf("line = %q", line)
	}
}

// resetLaunchPowerProfile puts the nested-launch bookkeeping back to neutral.
func resetLaunchPowerProfile() {
	launchPowerProfile.mu.Lock()
	launchPowerProfile.depth = 0
	launchPowerProfile.warned = false
	launchPowerProfile.ceiling = nil
	launchPowerProfile.mu.Unlock()
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
