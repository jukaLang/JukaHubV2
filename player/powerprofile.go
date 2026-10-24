package main

// powerprofile.go — the Fn-style power switch: performance while a game or port
// runs, battery mode again when it returns to the hub.
//
// On the handheld the CPU policy is the lever that matters: the A133P running
// on `powersave` stutters in PPSSPP, and on `performance` it drains the battery
// while the hub sits idle. The switch is deliberately scoped to a launch —
// JukaHub raises the profile before starting a child process and drops it back
// when that child exits — so the hub never runs at full tilt.
//
// A governor alone is not the whole story: the frequency the performance
// governor may reach is capped by scaling_max_freq, and the TrimUI Smart Pro
// advertises a 2 GHz top OPP that some firmware boots below. The launch
// profile therefore lifts that ceiling too (performance only, restored on the
// way back), which is what turns "performance" into the chip's full 2 GHz.
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
// overrides for firmwares that use their own names. MaxFreqKHz caps the clock a
// launch may use; zero means the highest frequency the CPU advertises (the 2 GHz
// top OPP of the A133P).
type powerProfileConfig struct {
	Enabled         bool
	PerfGovernor    string
	BatteryGovernor string
	MaxFreqKHz      int64
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
	if v, ok := customValue(config.Variables.Custom, "powerProfileMaxFreq"); ok {
		cfg.MaxFreqKHz = parseProfileMaxFreqKHz(v)
	}
	return cfg
}

// parseProfileMaxFreqKHz reads the configured clock ceiling. "auto", "max"
// and "" keep the top frequency the CPU offers. A bare number is read by
// magnitude — "1.8" is GHz, "1800" is MHz, "1800000" is kHz — and a "mhz"/
// "ghz" suffix pins the unit, so the friendly and the sysfs spellings both work.
func parseProfileMaxFreqKHz(v interface{}) int64 {
	raw := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", v)))
	switch raw {
	case "", "0", "auto", "max", "highest":
		return 0
	}
	scale := 0.0 // inferred from the magnitude
	if strings.HasSuffix(raw, "ghz") {
		raw, scale = strings.TrimSuffix(raw, "ghz"), 1_000_000
	} else if strings.HasSuffix(raw, "mhz") {
		raw, scale = strings.TrimSuffix(raw, "mhz"), 1_000
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || f <= 0 {
		return 0
	}
	if scale == 0 {
		switch {
		case f < 100:
			scale = 1_000_000
		case f < 100_000:
			scale = 1_000
		default:
			scale = 1
		}
	}
	return int64(f * scale)
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

// cpufreqDirs returns the cpufreq directory of every CPU, in the same numeric
// order as cpuGovernorPaths.
func cpufreqDirs(root string) []string {
	paths := cpuGovernorPaths(root)
	dirs := make([]string, 0, len(paths))
	for _, p := range paths {
		dirs = append(dirs, filepath.Dir(p))
	}
	return dirs
}

// readKHzFile parses one sysfs frequency file, or 0 when it is missing.
func readKHzFile(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// advertisedMaxKHz is the highest clock the policy offers: cpuinfo_max_freq,
// falling back to the largest entry of scaling_available_frequencies.
func advertisedMaxKHz(cpufreqDir string) int64 {
	if top := readKHzFile(filepath.Join(cpufreqDir, "cpuinfo_max_freq")); top > 0 {
		return top
	}
	raw, err := os.ReadFile(filepath.Join(cpufreqDir, "scaling_available_frequencies"))
	if err != nil {
		return 0
	}
	var top int64
	for _, field := range strings.Fields(string(raw)) {
		if v, err := strconv.ParseInt(field, 10, 64); err == nil && v > top {
			top = v
		}
	}
	return top
}

// powerProfileCeilingKHz is the clock a launch may reach: the configured cap
// when it is below the chip's top OPP, else the top OPP itself. Zero means the
// policy exposes no frequencies to choose from.
func powerProfileCeilingKHz(cpufreqDir string, cfg powerProfileConfig) int64 {
	top := advertisedMaxKHz(cpufreqDir)
	if top <= 0 {
		return 0
	}
	if cfg.MaxFreqKHz > 0 && cfg.MaxFreqKHz < top {
		return cfg.MaxFreqKHz
	}
	return top
}

// formatFrequencyKHz renders a clock for the log and the panel line.
func formatFrequencyKHz(khz int64) string {
	if khz <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.1f GHz", float64(khz)/1_000_000)
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

// cpuFrequencySave remembers one scaling_max_freq so a launch can put back the
// ceiling the firmware booted with.
type cpuFrequencySave struct {
	path  string
	value string
}

// applyFrequencyCeiling writes the profile's ceiling to scaling_max_freq. The
// performance governor asks the policy for its maximum, so a firmware that
// ships scaling_max_freq below the chip's top OPP — a TrimUI Smart Pro booted
// at 1.8 GHz with a 2 GHz table — would otherwise keep a game at that lower
// clock whatever governor is set. A cap below the current ceiling is written
// too: that is what makes a per-game or global cap mean anything on a firmware
// already running at full speed. The policy clamps to its nearest supported
// frequency. It returns what it changed, the ceiling it aimed for, and the
// first refusal.
func applyFrequencyCeiling(cfg powerProfileConfig) ([]cpuFrequencySave, int64, error) {
	var saved []cpuFrequencySave
	var ceiling int64
	for _, dir := range cpufreqDirs(cpufreqRoot) {
		target := powerProfileCeilingKHz(dir, cfg)
		if target <= 0 {
			continue
		}
		ceiling = target
		path := filepath.Join(dir, "scaling_max_freq")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // this policy exposes no writable ceiling
		}
		current := strings.TrimSpace(string(raw))
		if cur, err := strconv.ParseInt(current, 10, 64); err == nil && cur == target {
			continue // already exactly the ceiling
		}
		if err := os.WriteFile(path, []byte(strconv.FormatInt(target, 10)+"\n"), 0o644); err != nil {
			return saved, ceiling, fmt.Errorf("frequency ceiling is read-only on this firmware")
		}
		saved = append(saved, cpuFrequencySave{path: path, value: current})
	}
	return saved, ceiling, nil
}

// restoreFrequencyCeiling puts every ceiling back exactly as it was found.
func restoreFrequencyCeiling(saved []cpuFrequencySave) {
	for _, s := range saved {
		if s.value == "" {
			continue
		}
		if err := os.WriteFile(s.path, []byte(s.value+"\n"), 0o644); err != nil {
			logPatch("power profile: could not restore ceiling (%v)", err)
		}
	}
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
	// Ceiling is the clock a launch may reach (0 when the policy exposes none).
	Ceiling int64
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
	if dirs := cpufreqDirs(cpufreqRoot); len(dirs) > 0 {
		state.Ceiling = powerProfileCeilingKHz(dirs[0], cfg)
	}
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
	mu      sync.Mutex
	depth   int
	warned  bool
	ceiling []cpuFrequencySave
}

// beginGamePowerProfile raises the performance profile and returns a function
// that puts the battery profile back. It is safe to call without an emulator
// ever starting: an unusable firmware only produces one log line.
func beginGamePowerProfile(config *Config) func() {
	return beginGamePowerProfileCap(config, 0)
}

// beginGamePowerProfileCap is beginGamePowerProfile with a ceiling for this one
// child. A per-game clock (gameoptions.go) wins over the global
// powerProfileMaxFreq for the length of that launch, and the firmware value is
// restored with the rest of the profile when the game exits. Zero keeps the
// global setting.
func beginGamePowerProfileCap(config *Config, maxFreqKHz int64) func() {
	cfg := loadPowerProfileConfig(config)
	if maxFreqKHz > 0 {
		cfg.MaxFreqKHz = maxFreqKHz
	}
	return beginGamePowerProfileWith(cfg)
}

// beginGamePowerProfileWith applies an already-loaded profile. Keeping the
// switch separate lets a launch override one field without re-reading config.
func beginGamePowerProfileWith(cfg powerProfileConfig) func() {
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
		var ceiling []cpuFrequencySave
		if last {
			launchPowerProfile.depth = 0
			// The ceiling belongs to the launch that raised it: nested launches
			// share it, and only the outermost one puts the firmware value back.
			ceiling = launchPowerProfile.ceiling
			launchPowerProfile.ceiling = nil
		}
		launchPowerProfile.mu.Unlock()
		if !last || !switchable {
			return
		}
		restoreFrequencyCeiling(ceiling)
		if _, err := applyPowerProfile(powerProfileBattery, cfg); err != nil {
			logPatch("power profile: could not restore (%v)", err)
			return
		}
		logPatch("power profile: battery restored")
	}

	if !first || !switchable {
		return release
	}
	// Lift the ceiling before the governor: the performance governor asks the
	// policy for its maximum, so a firmware that boots with scaling_max_freq
	// below the top OPP would otherwise pin the game to that lower clock.
	ceiling, ceilingKHz, ceilingErr := applyFrequencyCeiling(cfg)
	if _, err := applyPowerProfile(powerProfilePerformance, cfg); err != nil {
		// The governor was refused: leave the ceiling as it was too, so a
		// partial profile does not outlive this launch.
		restoreFrequencyCeiling(ceiling)
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
	launchPowerProfile.mu.Lock()
	launchPowerProfile.ceiling = ceiling
	launchPowerProfile.mu.Unlock()
	if ceilingErr != nil {
		logPatch("power profile: %v", ceilingErr)
	} else if ceilingKHz > 0 {
		logPatch("power profile: %s ceiling, performance for the running game", formatFrequencyKHz(ceilingKHz))
	} else {
		logPatch("power profile: performance for the running game")
	}
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
	line := ""
	if state.Current != "" {
		line = "now " + state.Current
	}
	if state.Ceiling > 0 {
		if line != "" {
			line += " · "
		}
		line += "max " + formatFrequencyKHz(state.Ceiling)
	}
	if line == "" {
		return "performance on launch · battery on return"
	}
	return line + " · performance on launch"
}
