package main

// powerprofile.go — the Fn-style power switch: performance while a game or port
// runs, battery mode again when it returns to the hub.
//
// On the handheld the CPU governor is the only lever that matters: a 1.4 GHz
// A133P on `powersave` stutters in PPSSPP, and on `performance` it drains the
// battery while the hub sits idle. The switch is deliberately scoped to a
// launch — JukaHub raises the profile before starting a child process and drops
// it back when that child exits — so the hub never runs at full tilt.
//
// Reality check: writing scaling_governor needs root on most firmwares. The
// code probes for a writable governor first and reports what it found, so an
// unsupported firmware gets one honest line instead of a switch that pretends.
// Nothing here loops forever or fights the system: the profile is applied once
// per launch and restored once per exit.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// powerProfile is which side of the switch is wanted.
type powerProfile int

const (
	powerProfileBattery powerProfile = iota
	powerProfilePerformance
)

func (p powerProfile) String() string {
	if p == powerProfilePerformance {
		return "performance"
	}
	return "battery"
}

// powerProfileConfig is the saved setting. Both governor names are optional
// overrides for firmwares that use their own names.
type powerProfileConfig struct {
	Enabled         bool
	PerfGovernor    string
	BatteryGovernor string
}

// loadPowerProfileConfig reads the custom keys. The feature is on by default:
// when the governor is not writable it does nothing, so being on costs nothing.
func loadPowerProfileConfig(config *Config) powerProfileConfig {
	cfg := powerProfileConfig{Enabled: true}
	if config == nil {
		return cfg
	}
	if v, ok := customValue(config.Variables.Custom, "powerProfiles"); ok {
		cfg.Enabled = coerceBool(v)
	}
	if v, ok := customString(config.Variables.Custom, "powerProfilePerf"); ok {
		cfg.PerfGovernor = strings.TrimSpace(v)
	}
	if v, ok := customString(config.Variables.Custom, "powerProfileBattery"); ok {
		cfg.BatteryGovernor = strings.TrimSpace(v)
	}
	return cfg
}

// cpufreqRoot is a variable so tests can point at a fake sysfs tree.
var cpufreqRoot = "/sys/devices/system/cpu"

// cpuGovernorPaths lists the scaling_governor files for every CPU, sorted so
// the order is stable (cpu10 must not sort before cpu2 by accident: the names
// are sorted numerically by the index in the directory name).
func cpuGovernorPaths(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	type cpu struct {
		index int
		path  string
	}
	var cpus []cpu
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "cpu") {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimPrefix(name, "cpu"))
		if err != nil || idx < 0 {
			continue
		}
		path := filepath.Join(root, name, "cpufreq", "scaling_governor")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		cpus = append(cpus, cpu{index: idx, path: path})
	}
	sort.Slice(cpus, func(i, j int) bool { return cpus[i].index < cpus[j].index })
	paths := make([]string, 0, len(cpus))
	for _, c := range cpus {
		paths = append(paths, c.path)
	}
	return paths
}

// chooseGovernor picks the governor for a profile from what the CPU advertises.
// The preferred name is used when available; otherwise the first sensible
// fallback is taken, and "" means this CPU cannot be switched.
func chooseGovernor(profile powerProfile, available []string) string {
	have := make(map[string]bool, len(available))
	for _, name := range available {
		have[strings.TrimSpace(name)] = true
	}
	preferred := []string{"powersave", "schedutil", "ondemand"}
	if profile == powerProfilePerformance {
		preferred = []string{"performance"}
	}
	for _, name := range preferred {
		if have[name] {
			return name
		}
	}
	return ""
}

// availableGovernors reads the list a CPU advertises.
func availableGovernors(governorPath string) []string {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(governorPath), "available_governors"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

// readCurrentGovernors returns the governors in use, or "" when they differ.
func readCurrentGovernors(paths []string) string {
	current := ""
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		name := strings.TrimSpace(string(raw))
		if current == "" {
			current = name
			continue
		}
		if current != name {
			return ""
		}
	}
	return current
}

// powerProfileTarget is the governor a profile would write, using the
// configured override when it is one the CPU actually offers.
func powerProfileTarget(profile powerProfile, govPath string, cfg powerProfileConfig) string {
	available := availableGovernors(govPath)
	override := cfg.BatteryGovernor
	if profile == powerProfilePerformance {
		override = cfg.PerfGovernor
	}
	if override != "" {
		for _, name := range available {
			if name == override {
				return override
			}
		}
	}
	return chooseGovernor(profile, available)
}

// writeGovernor writes one governor file; it fails with the OS error, which the
// caller turns into "not permitted on this firmware" rather than a silent no-op.
func writeGovernor(path, governor string) error {
	return os.WriteFile(path, []byte(governor+"\n"), 0o644)
}

// applyPowerProfile switches every CPU. It returns the governor names it set,
// and an error naming the first CPU that refused.
func applyPowerProfile(profile powerProfile, cfg powerProfileConfig) ([]string, error) {
	paths := cpuGovernorPaths(cpufreqRoot)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no cpufreq governors on this device")
	}
	var applied []string
	for _, path := range paths {
		target := powerProfileTarget(profile, path, cfg)
		if target == "" {
			return applied, fmt.Errorf("this CPU offers no %s governor", profile)
		}
		if err := writeGovernor(path, target); err != nil {
			return applied, fmt.Errorf("governor is read-only on this firmware")
		}
		applied = append(applied, target)
	}
	return applied, nil
}

// powerProfileState is the panel/log view of the switch.
type powerProfileState struct {
	Enabled   bool
	Supported bool
	Reason    string
	Current   string
}

// probePowerProfiles reports whether the switch can do anything here.
func probePowerProfiles(cfg powerProfileConfig) powerProfileState {
	state := powerProfileState{Enabled: cfg.Enabled}
	if !cfg.Enabled {
		state.Reason = "off in settings"
		return state
	}
	paths := cpuGovernorPaths(cpufreqRoot)
	if len(paths) == 0 {
		state.Reason = "no cpufreq governors on this device"
		return state
	}
	state.Current = readCurrentGovernors(paths)
	for _, path := range paths {
		target := powerProfileTarget(powerProfilePerformance, path, cfg)
		if target == "" {
			continue
		}
		// A write is the only honest test on sysfs, and a probe must not change
		// the governor. The permission bits are the next best signal: sysfs
		// reports the real mode, and a file the user cannot write is a clear no.
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.Mode().Perm()&0o222 != 0 {
			state.Supported = true
			return state
		}
	}
	state.Reason = "governor is read-only (needs root on this firmware)"
	return state
}

// launchPowerProfile holds the profile applied for the running child, so a
// second launch cannot restore the wrong one when the first exits.
var launchPowerProfile struct {
	mu     sync.Mutex
	depth  int
	warned bool
}

// beginGamePowerProfile raises the performance profile and returns a function
// that puts the battery profile back. It is safe to call without an emulator
// ever starting: an unusable firmware only produces one log line.
func beginGamePowerProfile(config *Config) func() {
	cfg := loadPowerProfileConfig(config)
	if !cfg.Enabled {
		return func() {}
	}
	// A machine with no cpufreq at all (a desktop, a CI runner) has nothing to
	// switch, and saying so on every launch turned an expected "not here" into
	// a warning toast and a log line. The depth counter still runs so nested
	// launches stay balanced; nothing is written, logged or toasted. A handheld
	// whose governor files exist but refuse the write still gets the one
	// warning below, because that one is a fact about the firmware.
	switchable := len(cpuGovernorPaths(cpufreqRoot)) > 0
	launchPowerProfile.mu.Lock()
	launchPowerProfile.depth++
	first := launchPowerProfile.depth == 1
	launchPowerProfile.mu.Unlock()

	// The restore always drops the depth it raised, whether or not the
	// governor could be switched: otherwise one unsupported launch would leave
	// the counter raised for the rest of the session.
	release := func() {
		launchPowerProfile.mu.Lock()
		launchPowerProfile.depth--
		last := launchPowerProfile.depth <= 0
		if last {
			launchPowerProfile.depth = 0
		}
		launchPowerProfile.mu.Unlock()
		if !last || !switchable {
			return
		}
		if _, err := applyPowerProfile(powerProfileBattery, cfg); err != nil {
			logPatch("power profile: could not restore (%v)", err)
			return
		}
		logPatch("power profile: battery restored")
	}

	if !first || !switchable {
		return release
	}
	if _, err := applyPowerProfile(powerProfilePerformance, cfg); err != nil {
		launchPowerProfile.mu.Lock()
		warn := !launchPowerProfile.warned
		launchPowerProfile.warned = true
		launchPowerProfile.mu.Unlock()
		logPatch("power profile: %v", err)
		if warn {
			ShowToast("Performance mode unavailable: "+err.Error(), ToastKindInfo)
		}
		return release
	}
	logPatch("power profile: performance for the running game")
	return release
}

// powerProfileLine is the one-line status for the Emulators/Ports panels.
func powerProfileLine(config *Config) string {
	state := probePowerProfiles(loadPowerProfileConfig(config))
	if !state.Enabled {
		return "power profiles: " + state.Reason
	}
	if !state.Supported {
		return "power profiles: " + state.Reason
	}
	if state.Current == "" {
		return "performance on launch · battery on return"
	}
	return "now " + state.Current + " · performance on launch"
}
