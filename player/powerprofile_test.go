package main

// powerprofile_test.go — the governor switch and the exit-time CPU restore,
// against a fake /sys tree.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCPU creates <root>/cpuN/cpufreq/{scaling_governor,scaling_available_governors},
// the two files a real kernel exposes.
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
	if err := os.WriteFile(filepath.Join(dir, "scaling_available_governors"), []byte(available+"\n"), 0o444); err != nil {
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

// resetLaunchPowerProfile puts the nested-launch bookkeeping back to neutral.
func resetLaunchPowerProfile() {
	launchPowerProfile.mu.Lock()
	launchPowerProfile.depth = 0
	launchPowerProfile.warned = false
	launchPowerProfile.ceiling = nil
	launchPowerProfile.governors = nil
	launchPowerProfile.mu.Unlock()
}

// A CPU that also offers powersave must come back on the governor it ran before
// the launch: restoring the "battery" pick put the hub on powersave — its lowest
// clock — after every game.
func TestLaunchRestoresTheBootGovernorNotPowersave(t *testing.T) {
	root := t.TempDir()
	gov := fakeCPU(t, root, 0, "ondemand", "ondemand powersave performance schedutil", true)
	prevRoot := cpufreqRoot
	cpufreqRoot = root
	resetLaunchPowerProfile()
	t.Cleanup(func() {
		cpufreqRoot = prevRoot
		resetLaunchPowerProfile()
	})

	release := beginGamePowerProfile(nil)
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "performance" {
		t.Fatalf("governor during the launch = %q, want performance", raw)
	}
	release()
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "ondemand" {
		t.Fatalf("governor after the launch = %q, want the boot governor ondemand", raw)
	}

	// A configured battery governor is still honoured on the way back.
	config := &Config{Variables: Variables{Custom: map[string]interface{}{"powerProfileBattery": "powersave"}}}
	release = beginGamePowerProfile(config)
	release()
	if raw, _ := os.ReadFile(gov); strings.TrimSpace(string(raw)) != "powersave" {
		t.Fatalf("governor with powerProfileBattery set = %q, want powersave", raw)
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

// A two-cluster chip (the Smart Pro S's A523): a level from the fast
// policy's table becomes the same share of the slow one's, on a point it
// offers; a policy with the same top keeps the value as it is.
func TestCpufreqScaleKHzTwoClusters(t *testing.T) {
	root := t.TempDir()
	slow := filepath.Dir(fakeCPUWithFreq(t, root, 0, "1416000", "1416000", true))
	if err := os.WriteFile(filepath.Join(slow, "scaling_available_frequencies"), []byte("408000 720000 1008000 1200000 1416000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fast := filepath.Dir(fakeCPUWithFreq(t, root, 4, "2000000", "2000000", true))
	if got := cpufreqFastestFirst(cpufreqPolicyDirs(root)); len(got) != 2 || got[0] != fast {
		t.Fatalf("policies = %v, want the fast one first", got)
	}
	for _, c := range []struct{ khz, want int64 }{{1_000_000, 720_000}, {2_000_000, 1_416_000}, {408_000, 408_000}} {
		if got := cpufreqScaleKHz(slow, c.khz, 2_000_000); got != c.want {
			t.Fatalf("%d on the slow cluster = %d, want %d", c.khz, got, c.want)
		}
	}
	if got := cpufreqScaleKHz(fast, 1_200_000, 2_000_000); got != 1_200_000 {
		t.Fatalf("same-top policy changed %d to %d", 1_200_000, got)
	}
}

// TestAutoPerfLoadAndSteps covers Auto performance's pure parts: /proc/stat
// load (one busy core counts), the level table under a limit, and the step
// rule — up at once, down only after quiet samples and never inside the hold.
func TestAutoPerfLoadAndSteps(t *testing.T) {
	a := parseAutoPerfProcStat([]byte("cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 50 0 50 400 0 0 0 0\ncpu1 50 0 50 400 0 0 0 0\nintr 1 2 3\n"))
	b := parseAutoPerfProcStat([]byte("cpu  200 0 100 900 0 0 0 0 0 0\ncpu0 140 0 50 410 0 0 0 0\ncpu1 60 0 50 490 0 0 0 0\n"))
	maxBusy, allBusy, ok := autoPerfBusy(a, b)
	if !ok || maxBusy < 89 || maxBusy > 91 || allBusy < 49 || allBusy > 51 {
		t.Fatalf("busy = %.1f/%.1f ok=%v, want 90/50", maxBusy, allBusy, ok)
	}

	dir := filepath.Dir(fakeCPU(t, t.TempDir(), 0, "schedutil", "schedutil", true))
	if err := os.WriteFile(filepath.Join(dir, "scaling_available_frequencies"), []byte("1800000 600000 1200000 2000000 1200000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := autoPerfLevelTable(dir, 1_900_000); len(got) != 3 || got[0] != 600_000 || got[2] != 1_800_000 {
		t.Fatalf("levels = %v, want 600000 1200000 1800000", got)
	}

	spec := autoPerfSpec(autoPerfClassGame)
	var st autoPerfAdapt
	now := time.Unix(1000, 0)
	if got := autoPerfNext(1, 0, 3, 97, 60, spec, false, now, &st); got != 3 {
		t.Fatalf("pegged core: level %d, want 3 (two steps)", got)
	}
	if got := autoPerfNext(2, 0, 3, 90, 60, spec, true, now, &st); got != 2 {
		t.Fatalf("hot chip stepped up to %d", got)
	}
	idx := 3
	for i := 0; i < autoPerfDownSamples; i++ {
		now = now.Add(autoPerfTick)
		idx = autoPerfNext(idx, 0, 3, 10, 5, spec, false, now, &st)
	}
	if idx != 3 {
		t.Fatalf("stepped down inside the hold: %d", idx)
	}
	now = now.Add(autoPerfBounceHold)
	for i := 0; i < autoPerfDownSamples; i++ {
		now = now.Add(autoPerfTick)
		idx = autoPerfNext(idx, 0, 3, 10, 5, spec, false, now, &st)
	}
	if idx != 2 {
		t.Fatalf("quiet CPU: level %d, want one step down to 2", idx)
	}
	if got := autoPerfSettled(map[int64]time.Duration{600_000: 50 * time.Second, 1_800_000: 20 * time.Second, 2_000_000: 5 * time.Second}); got != 1_800_000 {
		t.Fatalf("settled = %d, want the highest level with a quarter of the time", got)
	}
}
