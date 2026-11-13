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
//
// A game can choose its own profile (gameoptions.go: Battery saver, Balanced,
// Performance, plus a CPU limit). Every launch that starts a file — library,
// ports, emulators, LAN play — goes through beginLaunchPowerProfile with that
// file's path, so the choice holds wherever the game is started from.

import (
	"encoding/json"
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
	// powerProfileBalanced is a launch on a dynamic governor rather than
	// performance: the per-game Balanced and Battery saver profiles
	// (gameoptions.go gameLaunchPowerConfig).
	powerProfileBalanced
)

func (p powerProfile) String() string {
	switch p {
	case powerProfilePerformance:
		return "performance"
	case powerProfileBalanced:
		return "balanced"
	}
	return "battery"
}

// powerProfileConfig is the saved setting. Both governor names are optional
// overrides for firmwares that use their own names. MaxFreqKHz caps the clock a
// launch may use; zero means the highest frequency the CPU advertises (the 2 GHz
// top OPP of the A133P). Balanced makes a launch use a dynamic governor
// (schedutil, interactive, ondemand…) instead of performance; only a per-game
// CPU profile sets it, so every other launch is unchanged.
type powerProfileConfig struct {
	Enabled         bool
	PerfGovernor    string
	BatteryGovernor string
	MaxFreqKHz      int64
	Balanced        bool
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
	switch profile {
	case powerProfilePerformance:
		preferred = []string{"performance"}
	case powerProfileBalanced:
		// A governor that follows the load, best first. A kernel offering none
		// of them still runs the game: performance under the profile's ceiling
		// is the closest thing it has.
		preferred = []string{"schedutil", "interactive", "ondemand", "conservative", "performance"}
	}
	for _, name := range preferred {
		if have[name] {
			return name
		}
	}
	return ""
}

// availableGovernors reads the list a CPU advertises. The kernel's file is
// scaling_available_governors; the old code read "available_governors", which
// no kernel has, so every real device answered "this CPU offers no performance
// governor" and the launch profile never switched anything.
func availableGovernors(governorPath string) []string {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(governorPath), "scaling_available_governors"))
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

// powerProfileTarget is the governor a profile would write, using the
// configured override when it is one the CPU actually offers.
func powerProfileTarget(profile powerProfile, govPath string, cfg powerProfileConfig) string {
	available := availableGovernors(govPath)
	override := cfg.BatteryGovernor
	switch profile {
	case powerProfilePerformance:
		override = cfg.PerfGovernor
	case powerProfileBalanced:
		// The battery override names the hub's idle governor (often
		// powersave), which would starve a game: Balanced always picks from
		// what the CPU offers.
		override = ""
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

// cpuGovernorSave remembers one scaling_governor, so the end of a launch puts
// back the governor the hub was running rather than a guess.
type cpuGovernorSave struct {
	path  string
	value string
}

// readGovernorSaves records the governor of every CPU. All or nothing: a
// partial record would restore some CPUs and leave others on performance.
func readGovernorSaves(paths []string) []cpuGovernorSave {
	saves := make([]cpuGovernorSave, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			return nil
		}
		saves = append(saves, cpuGovernorSave{path: p, value: value})
	}
	return saves
}

// restoreGovernors writes every recorded governor back, reporting the first
// refusal.
func restoreGovernors(saves []cpuGovernorSave) error {
	var first error
	for _, s := range saves {
		if err := writeGovernor(s.path, s.value); err != nil && first == nil {
			first = err
		}
	}
	return first
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

// launchPowerProfile holds the profile applied for the running child, so a
// second launch cannot restore the wrong one when the first exits.
var launchPowerProfile struct {
	mu        sync.Mutex
	depth     int
	warned    bool
	ceiling   []cpuFrequencySave
	governors []cpuGovernorSave
}

// beginGamePowerProfile raises the performance profile and returns a function
// that puts the battery profile back. It is safe to call without an emulator
// ever starting: an unusable firmware only produces one log line.
func beginGamePowerProfile(config *Config) func() {
	return beginLaunchPowerProfile(config, "")
}

// beginLaunchPowerProfile is the one entry for a launch tied to a file — a
// library game, a port script, an emulator, a LAN-play session (the start of
// MODERNIZATION PWR-6). target is the file launched; its per-game options
// (gameoptions.go: CPU profile and CPU limit) are folded into the global
// profile for the length of that launch, and the firmware values come back
// with the rest of the profile when it exits. A target with no options — or
// "" — launches exactly as beginGamePowerProfile does.
//
// (Not called launchPowerProfile: that name is the running launch's state
// below.)
func beginLaunchPowerProfile(config *Config, target string) func() {
	cfg := loadPowerProfileConfig(config)
	if strings.TrimSpace(target) != "" {
		cfg = gameLaunchPowerConfig(cfg, gameOptionsFor(target))
	}
	return beginGamePowerProfileWith(cfg)
}

// beginGamePowerProfileWith applies an already-loaded profile. Keeping the
// switch separate lets a launch override one field without re-reading config.
func beginGamePowerProfileWith(cfg powerProfileConfig) func() {
	// The voice assistant's short boost gives the CPU back first, so what
	// this launch records and later restores is the hub's own setting
	// (voiceboost.go).
	voiceBoostEnd()
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
	// Again now that the depth shows: a hold that slipped in since the first
	// call (Juka saying "Opening …") is undone, and later ones refuse.
	voiceBoostEnd()

	// The restore always drops the depth it raised, whether or not the
	// governor could be switched: otherwise one unsupported launch would leave
	// the counter raised for the rest of the session.
	release := func() {
		launchPowerProfile.mu.Lock()
		launchPowerProfile.depth--
		last := launchPowerProfile.depth <= 0
		var ceiling []cpuFrequencySave
		var governors []cpuGovernorSave
		if last {
			launchPowerProfile.depth = 0
			// The ceiling belongs to the launch that raised it: nested launches
			// share it, and only the outermost one puts the firmware value back.
			ceiling = launchPowerProfile.ceiling
			launchPowerProfile.ceiling = nil
			governors = launchPowerProfile.governors
			launchPowerProfile.governors = nil
		}
		launchPowerProfile.mu.Unlock()
		if !last || !switchable {
			return
		}
		// Whatever the restore manages, the launch is over: its crash marker
		// must not make a later boot "restore" values from before this one.
		defer bootCPUMarkerClear()
		restoreFrequencyCeiling(ceiling)
		// Without a configured battery governor the hub gets back exactly the
		// governor it ran before the launch. Writing the "battery" pick instead
		// put a hub that booted on schedutil or ondemand onto powersave — the
		// lowest clock — after every game, which made the menus crawl.
		if cfg.BatteryGovernor == "" && len(governors) > 0 {
			if err := restoreGovernors(governors); err != nil {
				logPatch("power profile: could not restore %s (%v)", governors[0].value, err)
				return
			}
			logPatch("power profile: %s restored", governors[0].value)
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
	// Lift the ceiling before the governor: the performance governor asks the
	// policy for its maximum, so a firmware that boots with scaling_max_freq
	// below the top OPP would otherwise pin the game to that lower clock.
	governors := readGovernorSaves(cpuGovernorPaths(cpufreqRoot))
	ceiling, ceilingKHz, ceilingErr := applyFrequencyCeiling(cfg)
	launchProfile := powerProfilePerformance
	if cfg.Balanced {
		launchProfile = powerProfileBalanced
	}
	if _, err := applyPowerProfile(launchProfile, cfg); err != nil {
		// The governor was refused: leave the ceiling as it was too, so a
		// partial profile does not outlive this launch — and put back any CPU
		// that did switch before the refusal.
		restoreFrequencyCeiling(ceiling)
		_ = restoreGovernors(governors)
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
	launchPowerProfile.governors = governors
	launchPowerProfile.mu.Unlock()
	// If the hub dies while the game holds the performance profile, the next
	// start puts these values back before it records the boot settings.
	bootCPUMarkerWrite(governors, ceiling)
	if ceilingErr != nil {
		logPatch("power profile: %v", ceilingErr)
	} else if ceilingKHz > 0 {
		logPatch("power profile: %s ceiling, %s for the running game", formatFrequencyKHz(ceilingKHz), launchProfile)
	} else {
		logPatch("power profile: %s for the running game", launchProfile)
	}
	return release
}

// bootCPUSettings remembers the governor and frequency ceiling the machine
// booted with. beginGamePowerProfile puts the firmware back after a game; this
// is the same promise for the hub's own exit, which also covers a child that
// died holding a performance profile and a shell that had already raised a
// governor before the hub was started.
var bootCPUSettings struct {
	mu        sync.Mutex
	captured  bool
	governors []cpuGovernorSave
	ceilings  []cpuFrequencySave
}

// captureBootCPUSettings records the governor of every CPU and each policy's
// scaling_max_freq. All-or-nothing per file: a value that cannot be read is
// simply not restored, rather than guessed.
func captureBootCPUSettings() {
	bootCPUSettings.mu.Lock()
	defer bootCPUSettings.mu.Unlock()
	if bootCPUSettings.captured {
		return
	}
	bootCPUSettings.captured = true
	// A previous run that died during a game left the performance profile
	// behind: undo it first, so what is recorded next is the firmware's.
	bootCPUMarkerRecover()

	paths := cpuGovernorPaths(cpufreqRoot)
	if len(paths) == 0 {
		return // a desktop or a CI runner: nothing to restore, nothing to log
	}
	bootCPUSettings.governors = readGovernorSaves(paths)
	for _, dir := range cpufreqDirs(cpufreqRoot) {
		path := filepath.Join(dir, "scaling_max_freq")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if value := strings.TrimSpace(string(raw)); value != "" {
			bootCPUSettings.ceilings = append(bootCPUSettings.ceilings, cpuFrequencySave{path: path, value: value})
		}
	}
}

// restoreBootCPUSettings puts the boot governor and ceiling back. It returns the
// line the teardown card shows and the first refusal, if any. A machine with no
// cpufreq says so instead of pretending it restored something.
func restoreBootCPUSettings() (string, error) {
	bootCPUSettings.mu.Lock()
	governors := bootCPUSettings.governors
	ceilings := bootCPUSettings.ceilings
	bootCPUSettings.mu.Unlock()

	if len(governors) == 0 && len(ceilings) == 0 {
		return "no cpufreq on this device", nil
	}

	var first error
	// Lift the ceiling after the governor, not before: writing a ceiling while
	// the governor is still performance would be clamped back by the policy.
	if err := restoreGovernors(governors); err != nil {
		first = err
	}
	for _, save := range ceilings {
		if save.value == "" {
			continue
		}
		if err := os.WriteFile(save.path, []byte(save.value+"\n"), 0o644); err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		return "", first
	}
	// The firmware's values are back, so what a launch recorded to undo is
	// moot: left behind by a game still holding the profile at exit, the
	// marker would make the next start "restore" that launch's old values.
	bootCPUMarkerClear()

	detail := ""
	if len(governors) > 0 {
		detail = fmt.Sprintf("%d CPUs on %s", len(governors), governors[0].value)
	}
	if len(ceilings) > 0 {
		if detail != "" {
			detail += " · "
		}
		detail += "ceiling restored"
	}
	logPatch("power profile: %s", detail)
	return detail, nil
}

// resetBootCPUSettings clears the capture. Tests switch cpufreqRoot between
// cases; the app never needs to forget what the firmware booted with.
func resetBootCPUSettings() {
	bootCPUSettings.mu.Lock()
	bootCPUSettings.captured = false
	bootCPUSettings.governors = nil
	bootCPUSettings.ceilings = nil
	bootCPUSettings.mu.Unlock()
}

// ── The launch profile's crash marker ────────────────────────────────────
//
// While a game holds the performance profile, the values it replaced are kept
// in a small marker in the temp directory (RAM on the handheld, so a reboot —
// which resets the CPU anyway — clears it). A hub that dies mid-game leaves the
// marker behind, and the next start puts those values back before it records
// what the firmware "booted with"; without it, that record was the crashed
// launch's performance governor, restored faithfully on every later exit. The
// idle screen's own marker (saver_power.go) follows the same rule.
//
// The temp directory is shared, so a marker is never trusted blindly: only a
// scaling_governor or scaling_max_freq file inside cpufreqRoot is written, and
// only with a governor name or a frequency.

// bootCPUMarkerPath is where the marker lives; a variable for tests.
var bootCPUMarkerPath = func() string {
	return filepath.Join(os.TempDir(), "jukahub-launch-cpufreq.json")
}

// bootCPUMarkerEnabled gates the marker to the real cpufreq tree, so a test
// pointing cpufreqRoot at a fake one never writes into the shared temp
// directory unless it asks to.
var bootCPUMarkerEnabled = func() bool { return cpufreqRoot == "/sys/devices/system/cpu" }

type bootCPUMarkerEntry struct {
	Path  string `json:"path"`
	Value string `json:"value"`
}

type bootCPUMarkerFile struct {
	Governors []bootCPUMarkerEntry `json:"governors"`
	Ceilings  []bootCPUMarkerEntry `json:"ceilings"`
}

// bootCPUMarkerWrite records what a launch replaced.
func bootCPUMarkerWrite(governors []cpuGovernorSave, ceilings []cpuFrequencySave) {
	if !bootCPUMarkerEnabled() || (len(governors) == 0 && len(ceilings) == 0) {
		return
	}
	var marker bootCPUMarkerFile
	for _, g := range governors {
		marker.Governors = append(marker.Governors, bootCPUMarkerEntry{Path: g.path, Value: g.value})
	}
	for _, c := range ceilings {
		marker.Ceilings = append(marker.Ceilings, bootCPUMarkerEntry{Path: c.path, Value: c.value})
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return
	}
	if err := os.WriteFile(bootCPUMarkerPath(), data, 0o600); err != nil {
		logPatch("power profile: could not record the values to restore (%v)", err)
	}
}

// bootCPUMarkerClear removes the marker once a launch has given the CPU back.
func bootCPUMarkerClear() {
	if !bootCPUMarkerEnabled() {
		return
	}
	if err := os.Remove(bootCPUMarkerPath()); err != nil && !os.IsNotExist(err) {
		logPatch("power profile: could not remove the crash marker (%v)", err)
	}
}

// bootCPUMarkerRecover puts back what a launch that never finished left
// behind, and removes the marker. It reports how many values it wrote.
func bootCPUMarkerRecover() int {
	if !bootCPUMarkerEnabled() {
		return 0
	}
	data, err := os.ReadFile(bootCPUMarkerPath())
	if err != nil {
		return 0
	}
	defer bootCPUMarkerClear()
	var marker bootCPUMarkerFile
	if json.Unmarshal(data, &marker) != nil {
		return 0
	}
	written := 0
	// Ceiling first, then the governor: the order the launch's own release
	// puts them back in.
	for _, c := range marker.Ceilings {
		if bootCPUMarkerSafe(c, "scaling_max_freq") && os.WriteFile(c.Path, []byte(c.Value+"\n"), 0o644) == nil {
			written++
		}
	}
	for _, g := range marker.Governors {
		if bootCPUMarkerSafe(g, "scaling_governor") && writeGovernor(g.Path, g.Value) == nil {
			written++
		}
	}
	if written > 0 {
		logPatch("power profile: restored %d CPU setting(s) an interrupted launch left raised", written)
	}
	return written
}

// bootCPUMarkerSafe reports whether a marker entry names the expected cpufreq
// file inside cpufreqRoot and carries a plausible value for it.
func bootCPUMarkerSafe(e bootCPUMarkerEntry, file string) bool {
	if filepath.Base(e.Path) != file || e.Value == "" || len(e.Value) > 32 {
		return false
	}
	rel, err := filepath.Rel(cpufreqRoot, e.Path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	for _, r := range e.Value {
		ok := (r >= '0' && r <= '9') || (file == "scaling_governor" && ((r >= 'a' && r <= 'z') || r == '_' || r == '-'))
		if !ok {
			return false
		}
	}
	return true
}
