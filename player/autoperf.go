package main

// autoperf.go — Auto performance: the CPU clock follows the app in front.
//
// The launch profile (powerprofile.go) is a switch: full speed while a game
// runs, the firmware's setting while the hub is up. Auto performance is the
// dial between those ends. A light ticker, off the render thread, looks every
// autoPerfTick at what is on screen and how busy the CPU is, and moves the
// clock ceiling (scaling_max_freq) one operating point at a time:
//
//   - Home and the menus start low, a video or the music player in the middle,
//     the Synth and HTML5 games high, and a game, emulator, port or app at the
//     top the first time it runs;
//   - a busy CPU (one core above autoPerfUpBusy, or all of them together above
//     autoPerfUpAll) steps up at once — two steps when it is pegged — and a
//     quiet one steps down only after autoPerfDownSamples quiet samples in a
//     row, never right after a step up, and much later after a bounce;
//   - a warm chip or battery (or the kernel's thermal limit being active)
//     never steps up, and no level goes over the heat guard's dial
//     (heatguard.go), which steps the clock down while either is hot;
//   - each app's settled clock is remembered (data/auto-performance.json), so
//     the next start of that game or screen begins where it ended up.
//
// What it does not do:
//
//   - touch the governor. The launch profile, the idle screens
//     (saver_power.go) and the voice assistant's boost (voiceboost.go) own it,
//     and Auto stands aside while the idle saver or the voice boost is in
//     force. Only the ceiling moves, and only when the level changes: there is
//     no write per tick;
//   - fight anyone. Every value it writes is remembered with the value it
//     replaced; if the ceiling is no longer the one Auto wrote (a game's own
//     tool, a script, the user), Auto leaves that app alone until the next one
//     — the heat guard still lowers it while the chip is hot;
//   - override a choice. A game with its own CPU profile or CPU limit
//     (gameoptions.go) runs exactly as chosen, under the heat guard's dial,
//     and with "Full speed while playing" off Auto only ever lowers the clock;
//   - pretend. A firmware whose scaling_max_freq is not writable (no root),
//     a PC, or a CPU without cpufreq gets one reason in the Settings row and
//     nothing else.
//
// Restores: leaving an app puts its ceiling back (compare-and-restore, so a
// launch's own restore always wins); a launch starting takes the hub's lowered
// ceiling back first, so what the launch records to restore is the firmware's
// value and not Auto's; the exit puts everything back; and a crash leaves a
// marker in the temp directory that the next start replays before it records
// the boot settings (captureBootCPUSettings).
//
// Besides following a load, a session is one of two other kinds. A boosted
// launch (CPU boost in games, cpuboost.go) holds the top of its table — under
// a dynamic governor the floor there too, and every parked core online — and
// only the heat dial moves it. A session JukaHub does not clock (its own CPU
// setting, another tool's ceiling, Automatic CPU speed off) is still followed,
// for the heat guard alone (heatHoldLocked). Either way the session's records
// and the guard's go into the one crash marker.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// autoPerfSetting is the Settings switch (on unless set to false).
	autoPerfSetting = "autoPerformance"

	autoPerfTick     = 2 * time.Second
	autoPerfHubEvery = 500 * time.Millisecond

	// Busy thresholds, in percent of a sample. A single busy core counts: the
	// hub's renderer and most emulators are one thread, which reads as 25% of
	// the whole 4-core chip while it is starving.
	autoPerfUpBusy   = 80.0
	autoPerfUpAll    = 70.0
	autoPerfJumpBusy = 95.0

	// Quiet samples in a row before a step down, and the holds that keep a
	// load that comes and goes from see-sawing the clock.
	autoPerfDownSamples = 3
	autoPerfUpHold      = 10 * time.Second
	autoPerfBounce      = 30 * time.Second
	autoPerfBounceHold  = 60 * time.Second

	// Learning: an app must run this long before its level is remembered;
	// the file is written at most this often (and on exit); at most this many
	// apps are kept, the least recently used dropped first.
	autoPerfLearnMin  = 20 * time.Second
	autoPerfSaveEvery = 30 * time.Second
	autoPerfMaxApps   = 300

	// autoPerfLaunchWait is how long a launch that has not started its child
	// yet keeps Auto's hands off the CPU.
	autoPerfLaunchWait = 15 * time.Second
)

// autoPerfClass is the kind of app in front; it sets where a never-seen app
// starts and how low Auto may take it.
type autoPerfClass int

const (
	autoPerfClassMenu  autoPerfClass = iota // Home, menus, Settings, the dock
	autoPerfClassMedia                      // video and music playback
	autoPerfClassHeavy                      // in-hub real-time apps: Synth, HTML5 games
	autoPerfClassGame                       // a game, emulator, port or app the hub launched
)

// autoPerfClassSpec is one class's start, floor and "quiet" thresholds.
// StartKHz 0 is the top level.
type autoPerfClassSpec struct {
	StartKHz int64
	FloorKHz int64
	DownMax  float64 // busiest core below this…
	DownAll  float64 // …and the whole chip below this is a quiet sample
}

func autoPerfSpec(c autoPerfClass) autoPerfClassSpec {
	switch c {
	case autoPerfClassMedia:
		return autoPerfClassSpec{StartKHz: 1_400_000, FloorKHz: 1_000_000, DownMax: 35, DownAll: 30}
	case autoPerfClassHeavy:
		return autoPerfClassSpec{StartKHz: 0, FloorKHz: 1_200_000, DownMax: 35, DownAll: 30}
	case autoPerfClassGame:
		// Games keep more headroom: a dropped frame is worse than a warm hand.
		return autoPerfClassSpec{StartKHz: 0, FloorKHz: 1_000_000, DownMax: 40, DownAll: 35}
	}
	return autoPerfClassSpec{StartKHz: 1_200_000, FloorKHz: 816_000, DownMax: 35, DownAll: 30}
}

// ── State ────────────────────────────────────────────────────────────────

// autoPerfAdapt is the hysteresis memory of one app's session.
type autoPerfAdapt struct {
	DownRun       int
	UpAt          time.Time
	DownAt        time.Time
	HoldDownUntil time.Time
}

// autoPerfSession is the app Auto is following.
type autoPerfSession struct {
	active  bool
	key     string
	label   string
	class   autoPerfClass
	manual  bool // the game's own CPU setting is in charge
	foreign bool // someone else moved the ceiling: hands off until the next app
	dirs    []string
	levels  []int64 // ascending kHz
	idx     int
	floor   int
	top     int
	writes  []saverSysfsWrite
	adapt   autoPerfAdapt
	at      map[int64]time.Duration
	last    time.Time

	// What the context asked for: a session that made itself manual (an
	// emulator on the userspace governor) is not begun again every tick.
	reqManual bool
	reqBoost  bool
	// CPU boost (cpuboost.go): the level is the heat dial's, the floor goes
	// with it under a dynamic governor (minRaise), cores counts parked cores
	// it woke.
	boost    bool
	minRaise bool
	cores    int
	// The heat guard's own records in a session JukaHub does not clock
	// (heatguard.go heatHoldLocked), and whether a refusal was logged.
	heat    []saverSysfsWrite
	heatErr bool
}

// autoPerfLearned is one app's remembered clock.
type autoPerfLearned struct {
	KHz  int64 `json:"khz"`
	Used int64 `json:"used,omitempty"` // unix seconds, for eviction
}

type autoPerfFile struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Apps          map[string]autoPerfLearned `json:"apps"`
}

var autoPerf struct {
	mu      sync.Mutex
	started bool
	stopped bool
	denied  bool

	// A launch on its way: the game's key (or "" to name it after the child
	// process), whether it has its own CPU setting or is boosted, and when it
	// began.
	launchSet    bool
	launchKey    string
	launchLabel  string
	launchManual bool
	launchBoost  bool
	launchAt     time.Time

	prev map[string]autoPerfCPUTimes
	sess autoPerfSession

	learned       map[string]autoPerfLearned
	learnedLoaded bool
	dirty         bool
	savedAt       time.Time
}

// The main thread's view, published for the ticker.
var (
	autoPerfOn        atomic.Bool  // the Settings switch
	autoPerfBoostOK   atomic.Bool  // "Full speed while playing": may Auto go above the firmware's ceiling
	autoPerfGlobalCap atomic.Int64 // powerProfileMaxFreq, 0 = none
	autoPerfHubNext   time.Time    // main thread only
)

var autoPerfHub struct {
	mu    sync.Mutex
	key   string
	label string
	class autoPerfClass
}

// autoPerfShown is the line the Settings row shows, and why Auto cannot run.
var autoPerfShown struct {
	mu     sync.Mutex
	line   string
	reason string
}

// autoPerfSaveMu serialises the file writes (the ticker and the exit hook).
var autoPerfSaveMu sync.Mutex

// ── Settings ─────────────────────────────────────────────────────────────

// autoPerfEnabled reads the switch; on by default, like the launch profile:
// where the clock cannot be changed it does nothing.
func autoPerfEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, autoPerfSetting, true)
}

// autoPerfSetEnabled stores the switch and lets the ticker know at once.
func autoPerfSetEnabled(c *Config, on bool) {
	setStoreBool(c, autoPerfSetting, on)
	autoPerfOn.Store(on)
}

// autoPerfSupported: the handheld only. A PC's clock is the OS's business.
func autoPerfSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice()
}

// autoPerfSettingDetail is the Settings row's second line.
func autoPerfSettingDetail(c *Config) string {
	if !autoPerfSupported() {
		return "Only on the handheld"
	}
	autoPerfShown.mu.Lock()
	line, reason := autoPerfShown.line, autoPerfShown.reason
	autoPerfShown.mu.Unlock()
	switch {
	case reason != "":
		return "Not available: " + reason
	case !autoPerfEnabled(c):
		return "Off: the CPU speed only changes for games"
	case line != "":
		return "Now: " + line
	}
	return "Slower on menus, faster for games and video, and learns what each app needs"
}

func autoPerfSetLine(line string) {
	autoPerfShown.mu.Lock()
	autoPerfShown.line = line
	autoPerfShown.mu.Unlock()
}

func autoPerfSetReason(reason string) {
	autoPerfShown.mu.Lock()
	autoPerfShown.reason = reason
	autoPerfShown.line = ""
	autoPerfShown.mu.Unlock()
}

// ── Main-thread hook ─────────────────────────────────────────────────────

// autoPerfFrame runs on the event thread every frame (main loop, also while a
// game owns the screen) and does real work twice a second: it publishes the
// setting and which in-hub screen is up, and starts the ticker the first time.
func autoPerfFrame(config *Config, now time.Time) {
	if now.Before(autoPerfHubNext) || !autoPerfSupported() {
		return
	}
	autoPerfHubNext = now.Add(autoPerfHubEvery)
	autoPerfOn.Store(autoPerfEnabled(config))
	pcfg := loadPowerProfileConfig(config)
	autoPerfBoostOK.Store(pcfg.Enabled)
	autoPerfGlobalCap.Store(pcfg.MaxFreqKHz)
	// CPU boost's row, and the heat guard's: its stop, and a line waiting
	// for the player (cpuboost.go, heatguard.go).
	cpuBoostModeNow.Store(cpuBoostMode(config))
	heatGuardFrame(config, now)
	key, label, class := autoPerfHubContext(config)
	autoPerfHub.mu.Lock()
	autoPerfHub.key, autoPerfHub.label, autoPerfHub.class = key, label, class
	autoPerfHub.mu.Unlock()
	autoPerfStart()
}

// autoPerfHubContext names the in-hub app on screen. Event thread only: it
// reads the scene index and the screen flags the loop owns.
func autoPerfHubContext(config *Config) (string, string, autoPerfClass) {
	switch {
	case html5GameActive():
		return "hub:html5", "HTML5 game", autoPerfClassHeavy
	case videoHoldsScreenAwake():
		return "hub:video", "Video", autoPerfClassMedia
	case synthSceneActive(config):
		return "hub:synth", "Synth", autoPerfClassHeavy
	case padsSceneActive(config):
		return "hub:pads", "Pads", autoPerfClassHeavy
	case musicSceneActive():
		return "hub:music", "Music", autoPerfClassMedia
	case ss.active || ambientIsActive():
		return "hub:idle", "Idle screen", autoPerfClassMenu
	}
	name := ""
	if config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		name = strings.TrimSpace(config.Scenes[currentSceneIndex].Name)
	}
	if name == "" {
		name = "Home"
	}
	return "hub:scene:" + name, name, autoPerfClassMenu
}

// autoPerfStart starts the ticker once and registers the exit hook. Event
// thread (Cleanup is the main loop's list).
func autoPerfStart() {
	autoPerf.mu.Lock()
	if autoPerf.started || autoPerf.stopped {
		autoPerf.mu.Unlock()
		return
	}
	autoPerf.started = true
	autoPerf.mu.Unlock()
	Cleanup = append(Cleanup, autoPerfShutdown)
	go autoPerfLoop()
}

// ── Launch hooks (powerprofile.go) ───────────────────────────────────────

// autoPerfLaunchBegin is called by beginLaunchPowerProfile before the launch
// records the CPU values it will restore: the hub's lowered ceiling goes back
// first, and the coming game's key is noted so its session is named after the
// ROM rather than the emulator binary.
func autoPerfLaunchBegin(target string) {
	if !autoPerfSupported() {
		return
	}
	target = strings.TrimSpace(target)
	manual, boost := false, false
	key, label := "", ""
	if target != "" {
		opts := gameOptionsFor(target)
		// CPU boost holds the game at the top clock the heat allows
		// (cpuboost.go); the heavy-game boost's games are among them unless
		// CPU boost is off (xboxboost.go: a game waiting on its GPU must not
		// read as quiet). Otherwise the game's own CPU setting is in charge.
		boost = cpuBoostWanted(target, opts)
		manual = !boost && (normaliseGameCPUProfile(opts.CPUProfile) != "" || opts.MaxFreqKHz > 0 || xboxBoostHolds(target))
		key = "game:" + gameOptionsKey(target)
		label = strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	}
	autoPerf.mu.Lock()
	defer autoPerf.mu.Unlock()
	if autoPerf.stopped {
		return
	}
	autoPerfEndLocked(time.Now())
	autoPerf.launchSet, autoPerf.launchKey, autoPerf.launchLabel = true, key, label
	autoPerf.launchManual, autoPerf.launchBoost, autoPerf.launchAt = manual, boost, time.Now()
}

// autoPerfYield is called by beginGamePowerProfileWith (every launch,
// including Wine's, which skips beginLaunchPowerProfile): the hub's ceiling
// goes back before the launch reads it. A launch already noted keeps its key.
func autoPerfYield() {
	if !autoPerfSupported() {
		return
	}
	autoPerf.mu.Lock()
	defer autoPerf.mu.Unlock()
	if autoPerf.stopped {
		return
	}
	now := time.Now()
	launchPowerProfile.mu.Lock()
	depth := launchPowerProfile.depth
	launchPowerProfile.mu.Unlock()
	if autoPerf.sess.active && !strings.HasPrefix(autoPerf.sess.key, "hub:") &&
		(depth > 0 || runningGameChildren.Load() > 0) {
		return // a launch inside a running one: that game's session stays
	}
	autoPerfEndLocked(now)
	if !autoPerf.launchSet || now.Sub(autoPerf.launchAt) > 2*time.Second {
		autoPerf.launchSet, autoPerf.launchKey, autoPerf.launchLabel = true, "", ""
		autoPerf.launchManual, autoPerf.launchBoost, autoPerf.launchAt = false, false, now
	}
}

// autoPerfLaunchEnd is called by the outermost launch's release just before
// it writes its own ceiling back: the game's session ends now (its ceiling
// goes back to the launch's value) instead of on the next tick, when the
// launch's unconditional restore could already have put back a value equal to
// Auto's last write — which the compare would then take for Auto's and
// "restore" to the dead launch's raised ceiling.
func autoPerfLaunchEnd() {
	if !autoPerfSupported() {
		return
	}
	autoPerf.mu.Lock()
	defer autoPerf.mu.Unlock()
	if autoPerf.stopped || !autoPerf.sess.active || strings.HasPrefix(autoPerf.sess.key, "hub:") {
		return
	}
	autoPerfEndLocked(time.Now())
	autoPerf.launchSet, autoPerf.launchKey, autoPerf.launchLabel, autoPerf.launchManual = false, "", "", false
	autoPerf.launchBoost = false
}

// ── The ticker ───────────────────────────────────────────────────────────

func autoPerfLoop() {
	if reason := autoPerfProbe(); reason != "" {
		autoPerf.mu.Lock()
		autoPerf.denied = true
		autoPerf.mu.Unlock()
		autoPerfSetReason(reason)
		logInfo("power", "auto performance off: %s", reason)
		return
	}
	t := time.NewTicker(autoPerfTick)
	defer t.Stop()
	for range t.C {
		if !autoPerfTickOnce(time.Now()) {
			return
		}
	}
}

// autoPerfProbe says why Auto cannot run here, or "" when it can. Opening a
// sysfs file for writing and closing it writes nothing; it is how a refusal
// shows without changing the clock.
func autoPerfProbe() string {
	dirs := autoPerfPolicyDirs()
	if len(dirs) == 0 {
		return "this device has no CPU speed control"
	}
	for _, dir := range dirs {
		f, err := os.OpenFile(filepath.Join(dir, "scaling_max_freq"), os.O_WRONLY, 0)
		if err != nil {
			return "this firmware does not let apps change the CPU speed"
		}
		f.Close()
	}
	return ""
}

// autoPerfPolicyDirs lists each cpufreq policy once (cpuN/cpufreq is a link
// to a shared policy directory on the A133P), the fastest first: its table is
// the level table, and every other policy (the A523's second cluster) follows
// it at the same share of its own range (autoPerfWriteLocked).
func autoPerfPolicyDirs() []string {
	return cpufreqFastestFirst(cpufreqPolicyDirs(cpufreqRoot))
}

// autoPerfTickOnce is one sample. It reports false once Auto has stopped.
func autoPerfTickOnce(now time.Time) bool {
	data, _ := os.ReadFile("/proc/stat")
	cur := parseAutoPerfProcStat(data)

	autoPerf.mu.Lock()
	if autoPerf.stopped {
		autoPerf.mu.Unlock()
		return false
	}
	maxBusy, allBusy, sampled := autoPerfBusy(autoPerf.prev, cur)
	autoPerf.prev = cur
	// The heat dial moves every tick, whoever clocks the CPU (heatguard.go).
	heatGuardTickLocked(now)
	switch {
	case autoPerf.denied:
		// Refused once this run: nothing more to try.
	case saverLowPowerEngaged() || voiceBoostActive():
		// Their turn: the idle screen or Juka's boost own the clock and put
		// Auto's value back when they are done.
		autoPerf.sess.adapt.DownRun = 0
		autoPerf.sess.last = now
	default:
		key, label, class, manual, boost, wait := autoPerfContextLocked(now)
		if !boost && !autoPerfOn.Load() {
			// Automatic CPU speed off: the app in front is still followed,
			// for the heat guard alone.
			manual = true
		}
		s := &autoPerf.sess
		switch {
		case wait:
			// A launch is starting its child: hands off until it is up.
		case !s.active || key != s.key || manual != s.reqManual || boost != s.reqBoost:
			// From one hub screen to another the ceiling is handed over as
			// it is, rather than put back and lowered again a moment later.
			keep := s.active && !s.manual && !s.foreign && !s.boost && !manual &&
				strings.HasPrefix(s.key, "hub:") && strings.HasPrefix(key, "hub:")
			carry := autoPerfFinishLocked(now, keep)
			autoPerfBeginLocked(now, key, label, class, manual, boost, carry)
		default:
			autoPerfAdaptLocked(now, maxBusy, allBusy, sampled)
		}
	}
	heatGuardAfterLocked(now)
	save := autoPerfSaveLocked(now, false)
	autoPerf.mu.Unlock()
	if save != nil {
		save()
	}
	return true
}

// autoPerfContextLocked names the app in front. boost reports a CPU boost
// launch (cpuboost.go); wait reports a launch that has not started its child
// yet, or a boosted one whose launch script may still be setting the clock.
func autoPerfContextLocked(now time.Time) (key, label string, class autoPerfClass, manual, boost, wait bool) {
	launchPowerProfile.mu.Lock()
	depth := launchPowerProfile.depth
	launchPowerProfile.mu.Unlock()
	if depth > 0 || runningGameChildren.Load() > 0 || foreignScreenSuspended() {
		if autoPerf.launchSet && autoPerf.launchKey != "" {
			if autoPerf.launchBoost && now.Sub(autoPerf.launchAt) < cpuBoostSettle && !autoPerf.sess.active {
				return "", "", autoPerfClassGame, false, false, true
			}
			return autoPerf.launchKey, autoPerf.launchLabel, autoPerfClassGame, autoPerf.launchManual, autoPerf.launchBoost, false
		}
		key, label = autoPerfChildKey()
		return key, label, autoPerfClassGame, false, false, false
	}
	if autoPerf.launchSet {
		if now.Sub(autoPerf.launchAt) < autoPerfLaunchWait && !autoPerf.sess.active {
			return "", "", autoPerfClassMenu, false, false, true
		}
		autoPerf.launchSet, autoPerf.launchKey, autoPerf.launchLabel, autoPerf.launchManual = false, "", "", false
		autoPerf.launchBoost = false
	}
	autoPerfHub.mu.Lock()
	key, label, class = autoPerfHub.key, autoPerfHub.label, autoPerfHub.class
	autoPerfHub.mu.Unlock()
	if key == "" {
		key, label, class = "hub:scene:Home", "Home", autoPerfClassMenu
	}
	return key, label, class, false, false, false
}

// autoPerfChildKey names a launch that came without a ROM path (an app
// shortcut, Wine, a firmware-started emulator) after its program.
func autoPerfChildKey() (string, string) {
	cmds := liveScreenChildren()
	if len(cmds) == 0 {
		return "app:other", "App"
	}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Path < cmds[j].Path })
	c := cmds[0]
	name := filepath.Base(c.Path)
	if n := len(c.Args); n > 1 {
		if last := filepath.Base(c.Args[n-1]); last != "" && last != "." && !strings.HasPrefix(last, "-") {
			name += " " + last
		}
	}
	if len(name) > 120 {
		name = strings.ToValidUTF8(name[:120], "")
	}
	return "app:" + name, name
}

// autoPerfBeginLocked starts following an app: its level table, its floor,
// and the learned (or class) starting level, written once. A boosted launch
// starts at the top of its table; a session JukaHub does not clock is only
// held under the heat dial. Neither goes over the dial.
func autoPerfBeginLocked(now time.Time, key, label string, class autoPerfClass, manual, boost bool, carry []saverSysfsWrite) {
	autoPerf.sess = autoPerfSession{
		active: true, key: key, label: label, class: class, manual: manual,
		reqManual: manual, reqBoost: boost,
		at: map[int64]time.Duration{}, last: now, writes: carry,
	}
	s := &autoPerf.sess
	heatGuardSessionBeginLocked(label, class)
	why := ""
	if boost && !manual {
		if cpuBoostGovernor(autoPerfPolicyDirs()) == "userspace" {
			// The emulator sets its own clock (NextUI's minarch).
			manual, why = true, " · the emulator sets its own clock"
		} else {
			s.boost = true
		}
	}
	if manual {
		// Not ours to set: anything handed over goes back first, and the
		// heat guard alone holds it.
		s.manual = true
		autoPerfRestoreWrites(s.writes)
		autoPerfRemoveMarker()
		s.writes = nil
		s.dirs = autoPerfPolicyDirs()
		switch {
		case why != "":
			autoPerfSetLine(label + why)
		case !autoPerfOn.Load():
			autoPerfSetLine("")
		case xboxBoostLive.Load() && class == autoPerfClassGame:
			autoPerfSetLine(label + " · heavy-game boost")
		default:
			autoPerfSetLine(label + " · its own CPU setting")
		}
		heatHoldLocked(s, heatCapKHzLocked())
		return
	}
	if s.boost {
		// Before the policies are listed: a cluster whose cores are all
		// parked has no policy link until one comes up.
		s.cores = cpuBoostCoresOnlineLocked(s)
	}
	s.dirs = autoPerfPolicyDirs()
	if len(s.dirs) == 0 {
		s.foreign = true
		return
	}
	heatGuardRefreshLevelsLocked()
	limit := autoPerfGlobalCap.Load()
	if !autoPerfBoostOK.Load() {
		// "Full speed while playing" is off: never above what the firmware
		// runs at — the value Auto found, not one it wrote itself.
		maxPath := filepath.Join(s.dirs[0], "scaling_max_freq")
		cur := readKHzFile(maxPath)
		for _, w := range s.writes {
			if w.Path == maxPath {
				if v, err := strconv.ParseInt(w.Original, 10, 64); err == nil && v > 0 {
					cur = v
				}
			}
		}
		if cur > 0 && (limit <= 0 || cur < limit) {
			limit = cur
		}
	}
	s.levels = autoPerfLevelTable(s.dirs[0], limit)
	if len(s.levels) == 0 {
		s.foreign = true
		return
	}
	spec := autoPerfSpec(class)
	s.top = len(s.levels) - 1
	s.floor = autoPerfIndexAtLeast(s.levels, spec.FloorKHz)
	start := s.top
	if s.boost {
		// A dynamic governor idles the clock between frames: the floor goes
		// with the level (autoPerfWriteLocked).
		s.minRaise = cpuBoostGovernor(s.dirs) != "performance"
	} else {
		if spec.StartKHz > 0 {
			start = autoPerfIndexAtLeast(s.levels, spec.StartKHz)
		}
		autoPerfLoadLocked()
		if l, ok := autoPerf.learned[key]; ok && l.KHz > 0 {
			start = autoPerfIndexAtLeast(s.levels, l.KHz)
			l.Used = now.Unix()
			autoPerf.learned[key] = l
		}
		start = clampInt(start, s.floor, s.top)
	}
	start = min(start, autoPerfHeatIdxLocked(s))
	s.idx = start
	if autoPerfWriteLocked(s.levels[start]) {
		autoPerfSetLine(autoPerfLineLocked(s))
	}
}

// autoPerfHeatIdxLocked is the highest level of the session's table the heat
// dial allows: its top while the dial limits nothing.
func autoPerfHeatIdxLocked(s *autoPerfSession) int {
	capKHz := heatCapKHzLocked()
	if capKHz <= 0 || len(s.levels) == 0 {
		return s.top
	}
	return min(s.top, autoPerfIndexAtMost(s.levels, capKHz))
}

// autoPerfLineLocked is the Settings line for the session's level.
func autoPerfLineLocked(s *autoPerfSession) string {
	if s.idx < 0 || s.idx >= len(s.levels) {
		return s.label
	}
	clock := formatFrequencyKHz(s.levels[s.idx])
	line := s.label + " · " + clock
	switch {
	case s.boost && xboxBoostLive.Load() && s.class == autoPerfClassGame:
		line = s.label + " · heavy-game boost · " + clock
	case s.boost:
		line = s.label + " · CPU boost · " + clock
	}
	if s.idx < s.top && s.idx == autoPerfHeatIdxLocked(s) {
		line += " (cooling)"
	}
	return line
}

// autoPerfAdaptLocked is one tick's decision for the app in front: a level
// from the load (when a CPU sample came in), never over the heat dial; a
// boosted launch's level is the dial's own; a session JukaHub does not clock
// is held under the dial (heatHoldLocked).
func autoPerfAdaptLocked(now time.Time, maxBusy, allBusy float64, sampled bool) {
	s := &autoPerf.sess
	if len(s.levels) > 0 && s.idx >= 0 && s.idx < len(s.levels) {
		s.at[s.levels[s.idx]] += now.Sub(s.last)
	}
	s.last = now
	if s.manual || s.foreign || len(s.levels) == 0 {
		heatHoldLocked(s, heatCapKHzLocked())
		return
	}
	heatIdx := autoPerfHeatIdxLocked(s)
	next := heatIdx
	if !s.boost {
		next = s.idx
		if sampled {
			// A warm chip or battery steps no higher (heatguard.go).
			hot := heatGuardHoldsLocked()
			if !hot && (maxBusy >= autoPerfUpBusy || allBusy >= autoPerfUpAll) {
				if active, known := cpuThermalThrottleState(); known && active {
					hot = true
				}
			}
			next = autoPerfNext(s.idx, s.floor, s.top, maxBusy, allBusy, autoPerfSpec(s.class), hot, now, &s.adapt)
		}
		next = min(next, heatIdx)
	}
	if next == s.idx {
		return
	}
	if autoPerfWriteLocked(s.levels[next]) {
		logDebug("power", "auto performance: %s %s → %s (busiest core %.0f%%, all %.0f%%)",
			s.label, formatFrequencyKHz(s.levels[s.idx]), formatFrequencyKHz(s.levels[next]), maxBusy, allBusy)
		s.idx = next
		autoPerfSetLine(autoPerfLineLocked(s))
	}
}

// autoPerfNext is the pure step decision: the level index for the next tick.
func autoPerfNext(idx, floor, top int, maxBusy, allBusy float64, spec autoPerfClassSpec, hot bool, now time.Time, st *autoPerfAdapt) int {
	if floor > top {
		floor = top
	}
	idx = clampInt(idx, floor, top)
	up := maxBusy >= autoPerfUpBusy || allBusy >= autoPerfUpAll
	down := maxBusy < spec.DownMax && allBusy < spec.DownAll
	switch {
	case up:
		st.DownRun = 0
		if hot || idx >= top {
			return idx
		}
		step := 1
		if maxBusy >= autoPerfJumpBusy {
			step = 2
		}
		// Up again soon after a step down: the lower level was not enough,
		// so stay up for longer this time.
		if !st.DownAt.IsZero() && now.Sub(st.DownAt) < autoPerfBounce {
			st.HoldDownUntil = now.Add(autoPerfBounceHold)
		} else {
			st.HoldDownUntil = now.Add(autoPerfUpHold)
		}
		st.UpAt = now
		return clampInt(idx+step, floor, top)
	case down:
		st.DownRun++
		if st.DownRun < autoPerfDownSamples || idx <= floor || now.Before(st.HoldDownUntil) {
			return idx
		}
		st.DownRun = 0
		st.DownAt = now
		return idx - 1
	}
	st.DownRun = 0
	return idx
}

// autoPerfLevelTable is the policy's operating points in ascending order, up
// to limit (0 = no limit; a limit below every point keeps the lowest). A
// policy without a table gets four even steps between its min and max.
func autoPerfLevelTable(dir string, limit int64) []int64 {
	levels := saverAvailableKHz(dir)
	if len(levels) == 0 {
		lo := readKHzFile(filepath.Join(dir, "cpuinfo_min_freq"))
		hi := readKHzFile(filepath.Join(dir, "cpuinfo_max_freq"))
		if lo <= 0 || hi <= lo {
			return nil
		}
		for i := int64(0); i <= 4; i++ {
			levels = append(levels, lo+(hi-lo)*i/4)
		}
	}
	sort.Slice(levels, func(i, j int) bool { return levels[i] < levels[j] })
	out := make([]int64, 0, len(levels))
	for _, f := range levels {
		if n := len(out); n > 0 && out[n-1] == f {
			continue
		}
		if limit > 0 && f > limit && len(out) > 0 {
			break
		}
		out = append(out, f)
		if limit > 0 && f > limit {
			break // the limit is below every point: the lowest one only
		}
	}
	return out
}

// autoPerfIndexAtLeast is the first level at or above khz, else the top.
func autoPerfIndexAtLeast(levels []int64, khz int64) int {
	for i, f := range levels {
		if f >= khz {
			return i
		}
	}
	return len(levels) - 1
}

// autoPerfIndexAtMost is the last level at or below khz, else the lowest.
func autoPerfIndexAtMost(levels []int64, khz int64) int {
	idx := 0
	for i, f := range levels {
		if f <= khz {
			idx = i
		}
	}
	return idx
}

// autoPerfWriteLocked moves every policy's ceiling to khz and, for a boosted
// session under a dynamic governor, its floor with it. It reports whether the
// level is now in force; a ceiling or floor someone else moved, or a refusal,
// takes Auto's hands off this app (and a refusal off the rest of the run), and
// from then on only the heat guard holds it (heatHoldLocked).
func autoPerfWriteLocked(khz int64) bool {
	s := &autoPerf.sess
	// khz is a point of the first (fastest) policy's table; a second cluster
	// with its own table gets the same share of its range.
	var refTop int64
	if len(s.dirs) > 1 {
		refTop = advertisedMaxKHz(s.dirs[0])
	}
	heatCap := heatCapKHzLocked()
	for _, dir := range s.dirs {
		maxPath := filepath.Join(dir, "scaling_max_freq")
		minPath := filepath.Join(dir, "scaling_min_freq")
		cur := saverReadTrimmed(maxPath)
		if cur == "" {
			continue
		}
		curMin := saverReadTrimmed(minPath)
		if !autoPerfRecordHolds(s.writes, maxPath, cur) || !autoPerfRecordHolds(s.writes, minPath, curMin) {
			s.foreign = true
			logInfo("power", "auto performance: the CPU ceiling was changed by something else; leaving %s alone", s.label)
			autoPerfSetLine(s.label + " · set by another tool")
			return false
		}
		target := cpufreqScaleKHz(dir, khz, refTop)
		floor := int64(-1) // -1: the floor stays as it is
		lo, _ := strconv.ParseInt(curMin, 10, 64)
		if i := autoPerfRecordIndex(s.writes, minPath); i >= 0 {
			// A floor this session moved goes back toward the value found,
			// never over the new ceiling.
			if v, err := strconv.ParseInt(s.writes[i].Original, 10, 64); err == nil {
				lo = v
			}
			floor = min(lo, target)
		}
		switch {
		case s.boost && s.minRaise:
			// CPU boost: the floor at the level too, so a dynamic governor
			// cannot idle the clock between frames.
			floor = target
		case floor < 0 && lo > 0 && target < lo:
			if heatCap > 0 && khz <= heatCap {
				floor = target // the heat dial goes under a raised floor too
			} else {
				target = lo // the kernel refuses a ceiling under the floor
			}
		}
		if _, err := autoPerfSetPairLocked(&s.writes, maxPath, cur, target, minPath, curMin, floor); err != nil {
			s.foreign = true
			if os.IsPermission(err) {
				autoPerf.denied = true
				autoPerfSetReason("this firmware does not let apps change the CPU speed")
			}
			logWarn("power", "auto performance: could not set the CPU ceiling (%v)", err)
			return false
		}
		// Written stays what was written, not the read-back: a thermal clamp
		// lowers what scaling_max_freq reports (autoPerfOurs).
	}
	return true
}

// autoPerfRecordIndex is the record of path in recs, or -1.
func autoPerfRecordIndex(recs []saverSysfsWrite, path string) int {
	for i := range recs {
		if recs[i].Path == path {
			return i
		}
	}
	return -1
}

// autoPerfRecordHolds reports whether path still holds what recs say was
// written there: true when nothing was, or it cannot be read.
func autoPerfRecordHolds(recs []saverSysfsWrite, path, cur string) bool {
	i := autoPerfRecordIndex(recs, path)
	return i < 0 || cur == "" || autoPerfOurs(cur, recs[i].Written)
}

// autoPerfSetPairLocked moves one policy's ceiling to wantMax and, with
// wantMin ≥ 0, its floor to wantMin, in the order the kernel takes them:
// going down the floor first, going up the ceiling first (the 4.9 kernel
// refuses a ceiling under the floor at every step). Each value is recorded in
// recs the first time it changes. It reports whether anything was written.
func autoPerfSetPairLocked(recs *[]saverSysfsWrite, maxPath, curMax string, wantMax int64, minPath, curMin string, wantMin int64) (bool, error) {
	type op struct{ path, cur, want string }
	ops := []op{{maxPath, curMax, strconv.FormatInt(wantMax, 10)}}
	if wantMin >= 0 && curMin != "" {
		floor := op{minPath, curMin, strconv.FormatInt(wantMin, 10)}
		if c, err := strconv.ParseInt(curMax, 10, 64); err == nil && wantMax < c {
			ops = []op{floor, ops[0]}
		} else {
			ops = append(ops, floor)
		}
	}
	wrote := false
	for _, o := range ops {
		w, err := autoPerfWriteRecordedLocked(recs, o.path, o.cur, o.want)
		if err != nil {
			return wrote, err
		}
		wrote = wrote || w
	}
	return wrote, nil
}

// autoPerfWriteRecordedLocked writes want to path, recording the value it
// replaced in recs the first time; the crash marker is written first, so a
// crash between the two leaves enough behind. A value that already reads as
// the one written (a thermal clamp may read lower) is not written again.
func autoPerfWriteRecordedLocked(recs *[]saverSysfsWrite, path, cur, want string) (bool, error) {
	i := autoPerfRecordIndex(*recs, path)
	if i < 0 && cur == want {
		return false, nil
	}
	if i >= 0 && (*recs)[i].Written == want && autoPerfOurs(cur, want) {
		return false, nil
	}
	prev := cur // what a refused write leaves the record comparing with
	if i < 0 {
		*recs = append(*recs, saverSysfsWrite{Path: path, Original: cur, Written: want})
		i = len(*recs) - 1
	} else {
		prev = (*recs)[i].Written
		(*recs)[i].Written = want
	}
	autoPerfSaveMarkerLocked()
	if err := os.WriteFile(path, []byte(want+"\n"), 0o644); err != nil {
		// Nothing changed: the record goes back to what is there.
		(*recs)[i].Written = prev
		autoPerfSaveMarkerLocked()
		return false, err
	}
	return true, nil
}

// autoPerfSaveMarkerLocked writes the crash marker for what the session in
// front holds: its own records and the heat guard's.
func autoPerfSaveMarkerLocked() {
	s := &autoPerf.sess
	all := make([]saverSysfsWrite, 0, len(s.writes)+len(s.heat))
	all = append(all, s.writes...)
	all = append(all, s.heat...)
	autoPerfWriteMarker(all)
}

// autoPerfOurs reports whether a ceiling still holds the value Auto wrote.
// The kernel's thermal clamp lowers what scaling_max_freq reports without
// changing the value set, so a lower reading while the clamp is on is still
// Auto's — otherwise every hot moment would read as another tool's change and
// leave Auto's value behind after the app.
func autoPerfOurs(cur, written string) bool {
	if cur == written {
		return true
	}
	c, err1 := strconv.ParseInt(cur, 10, 64)
	w, err2 := strconv.ParseInt(written, 10, 64)
	if err1 != nil || err2 != nil || c <= 0 || c >= w {
		return false
	}
	active, known := cpuThermalThrottleState()
	return known && active
}

// autoPerfEndLocked stops following the app: its level is learned and its
// ceiling put back where it is still the one Auto wrote.
func autoPerfEndLocked(now time.Time) { autoPerfFinishLocked(now, false) }

// autoPerfFinishLocked ends the session. With keep, the ceiling stays as it
// is and its records (and crash marker) are returned for the next session to
// carry on from; otherwise they are restored and nil is returned.
func autoPerfFinishLocked(now time.Time, keep bool) []saverSysfsWrite {
	s := &autoPerf.sess
	if !s.active {
		autoPerf.sess = autoPerfSession{}
		return nil
	}
	if len(s.levels) > 0 && s.idx >= 0 && s.idx < len(s.levels) && !s.last.IsZero() {
		s.at[s.levels[s.idx]] += now.Sub(s.last)
	}
	if !s.manual && !s.foreign && !s.boost && s.key != "" {
		var total time.Duration
		for _, d := range s.at {
			total += d
		}
		if total >= autoPerfLearnMin {
			if khz := autoPerfSettled(s.at); khz > 0 {
				autoPerfLoadLocked()
				autoPerf.learned[s.key] = autoPerfLearned{KHz: khz, Used: now.Unix()}
				autoPerfTrimLocked()
				autoPerf.dirty = true
			}
		}
	}
	heat := heatGuardSessionEndLocked()
	if s.boost {
		var top int64
		if s.top >= 0 && s.top < len(s.levels) {
			top = s.levels[s.top]
		}
		cpuBoostSessionEnd(s.label, top, s.cores, heat)
	}
	writes, held := s.writes, s.heat
	autoPerf.sess = autoPerfSession{}
	// The heat guard's values first: they sit over whatever the session or a
	// launch script left.
	autoPerfRestoreWrites(held)
	if keep {
		autoPerfWriteMarker(writes)
		return writes
	}
	autoPerfRestoreWrites(writes)
	autoPerfRemoveMarker()
	return nil
}

// autoPerfSettled is the level an app settled at: the highest one that held
// at least a quarter of its time, so a game that spent a while at the top for
// its busy scenes starts there next time rather than at its menu's clock.
func autoPerfSettled(at map[int64]time.Duration) int64 {
	var total time.Duration
	keys := make([]int64, 0, len(at))
	for k, d := range at {
		total += d
		keys = append(keys, k)
	}
	if total <= 0 {
		return 0
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] > keys[j] })
	for _, k := range keys {
		if at[k]*4 >= total {
			return k
		}
	}
	return 0
}

// autoPerfRestoreWrites undoes writes, newest first, only where the value is
// still Auto's: a launch's or the user's value wins.
func autoPerfRestoreWrites(writes []saverSysfsWrite) int {
	restored := 0
	var again []saverSysfsWrite
	for i := len(writes) - 1; i >= 0; i-- {
		w := writes[i]
		if w.Path == "" || w.Original == "" || w.Original == w.Written {
			continue
		}
		if !autoPerfOurs(saverReadTrimmed(w.Path), w.Written) {
			continue
		}
		if err := os.WriteFile(w.Path, []byte(w.Original+"\n"), 0o644); err != nil {
			// A ceiling going back under a floor that is still raised (or a
			// floor over a ceiling still lowered) waits for the other one:
			// the 4.9 kernel refuses it until then.
			again = append(again, w)
			continue
		}
		restored++
	}
	for _, w := range again {
		if err := os.WriteFile(w.Path, []byte(w.Original+"\n"), 0o644); err != nil {
			logWarn("power", "auto performance: could not restore %s (%v)", filepath.Base(w.Path), err)
			continue
		}
		restored++
	}
	return restored
}

// ── Exit ─────────────────────────────────────────────────────────────────

// autoPerfStop puts the ceiling back and ends the ticker. restoreBootCPUSettings
// calls it first, so nothing Auto does can land after the exit's restore. No
// file is written here: the forced exit runs it under a deadline.
func autoPerfStop() {
	autoPerf.mu.Lock()
	defer autoPerf.mu.Unlock()
	if autoPerf.stopped {
		return
	}
	autoPerf.stopped = true
	autoPerfEndLocked(time.Now())
}

// autoPerfShutdown is the exit hook: stop, then save what was learned.
func autoPerfShutdown() {
	autoPerfStop()
	autoPerf.mu.Lock()
	save := autoPerfSaveLocked(time.Now(), true)
	autoPerf.mu.Unlock()
	if save != nil {
		save()
	}
}

// ── Crash marker ─────────────────────────────────────────────────────────

var autoPerfMarkerPath = func() string {
	return filepath.Join(os.TempDir(), "jukahub-auto-cpufreq.json")
}

// autoPerfMarkerOn gates the marker to the real cpufreq tree (tests use fake ones).
func autoPerfMarkerOn() bool { return cpufreqRoot == "/sys/devices/system/cpu" }

func autoPerfWriteMarker(writes []saverSysfsWrite) {
	if !autoPerfMarkerOn() {
		return
	}
	if len(writes) == 0 {
		autoPerfRemoveMarker()
		return
	}
	data, err := json.Marshal(saverPowerMarker{Writes: writes})
	if err != nil {
		return
	}
	if err := os.WriteFile(autoPerfMarkerPath(), data, 0o600); err != nil {
		logWarn("power", "auto performance: could not record the CPU setting to restore (%v)", err)
	}
}

func autoPerfRemoveMarker() {
	if !autoPerfMarkerOn() {
		return
	}
	if err := os.Remove(autoPerfMarkerPath()); err != nil && !os.IsNotExist(err) {
		logWarn("power", "auto performance: could not remove the crash marker (%v)", err)
	}
}

// autoPerfRecover puts back a ceiling, a floor or a parked core a crashed run
// left changed (Automatic CPU speed, CPU boost, the heat guard).
// captureBootCPUSettings calls it before the launch marker's recovery: each
// value is restored only where it is still Auto's, so the order is safe. The
// marker is in the temp directory, which is shared: only the files
// autoPerfMarkerEntrySafe allows are ever written.
func autoPerfRecover() {
	if !autoPerfMarkerOn() {
		return
	}
	data, err := os.ReadFile(autoPerfMarkerPath())
	if err != nil {
		return
	}
	defer autoPerfRemoveMarker()
	var marker saverPowerMarker
	if json.Unmarshal(data, &marker) != nil {
		return
	}
	var safe []saverSysfsWrite
	for _, w := range marker.Writes {
		if autoPerfMarkerEntrySafe(w) {
			safe = append(safe, w)
		}
	}
	if n := autoPerfRestoreWrites(safe); n > 0 {
		logInfo("power", "auto performance: restored %d CPU setting(s) an interrupted run left changed", n)
	}
}

// autoPerfMarkerEntrySafe reports whether a marker entry names a file Auto,
// CPU boost or the heat guard writes — a policy's scaling_max_freq or
// scaling_min_freq inside cpufreqRoot, or a CPU's online switch — with values
// that fit it.
func autoPerfMarkerEntrySafe(w saverSysfsWrite) bool {
	switch base := filepath.Base(w.Path); base {
	case "scaling_max_freq", "scaling_min_freq":
		return bootCPUMarkerSafe(bootCPUMarkerEntry{Path: w.Path, Value: w.Original}, base) &&
			bootCPUMarkerSafe(bootCPUMarkerEntry{Path: w.Path, Value: w.Written}, base)
	case "online":
		return xboxBoostWritable(w.Path) && (w.Original == "0" || w.Original == "1") &&
			(w.Written == "0" || w.Written == "1")
	}
	return false
}

// ── CPU load ─────────────────────────────────────────────────────────────

// autoPerfCPUTimes is one /proc/stat line: busy and total jiffies.
type autoPerfCPUTimes struct {
	Busy  uint64
	Total uint64
}

// parseAutoPerfProcStat reads the "cpu" and "cpuN" lines. Busy is everything
// but idle and iowait; guest time is already inside user, so only the first
// eight columns are summed.
func parseAutoPerfProcStat(data []byte) map[string]autoPerfCPUTimes {
	out := map[string]autoPerfCPUTimes{}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		var vals []uint64
		for _, f := range fields[1:] {
			if len(vals) == 8 {
				break
			}
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				vals = nil
				break
			}
			vals = append(vals, v)
		}
		if len(vals) < 4 {
			continue
		}
		var total uint64
		for _, v := range vals {
			total += v
		}
		idle := vals[3]
		if len(vals) > 4 {
			idle += vals[4]
		}
		busy := uint64(0)
		if total > idle {
			busy = total - idle
		}
		out[fields[0]] = autoPerfCPUTimes{Busy: busy, Total: total}
	}
	return out
}

// autoPerfBusy compares two samples: the busiest core's and the whole chip's
// load in percent. ok is false without a usable whole-chip delta (the first
// sample, or a counter that went backwards).
func autoPerfBusy(prev, cur map[string]autoPerfCPUTimes) (maxBusy, allBusy float64, ok bool) {
	pct := func(a, b autoPerfCPUTimes) (float64, bool) {
		if b.Total <= a.Total || b.Busy < a.Busy {
			return 0, false
		}
		dt := b.Total - a.Total
		db := b.Busy - a.Busy
		if db > dt {
			db = dt
		}
		return float64(db) * 100 / float64(dt), true
	}
	for name, b := range cur {
		a, seen := prev[name]
		if !seen {
			continue
		}
		p, good := pct(a, b)
		if !good {
			continue
		}
		if name == "cpu" {
			allBusy, ok = p, true
		} else if p > maxBusy {
			maxBusy = p
		}
	}
	if ok && maxBusy < allBusy {
		maxBusy = allBusy // a single-core machine lists no cpuN worth more
	}
	return maxBusy, allBusy, ok
}

// ── What was learned ─────────────────────────────────────────────────────

var autoPerfLearnPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "auto-performance.json")
}

// autoPerfLoadLocked reads the file once. Missing is empty; damaged falls
// back to the .bak AtomicWrite keeps, then to empty — never an error the
// user sees, since all it costs is a first launch at the class's level.
func autoPerfLoadLocked() {
	if autoPerf.learnedLoaded {
		return
	}
	autoPerf.learnedLoaded = true
	autoPerf.learned = map[string]autoPerfLearned{}
	path := autoPerfLearnPath()
	if path == "" {
		return
	}
	var file autoPerfFile
	if _, err := readJSONWithBackup(path, &file); err != nil {
		return
	}
	for k, v := range file.Apps {
		if k == "" || len(k) > 600 || v.KHz < 100_000 || v.KHz > 10_000_000 {
			continue
		}
		autoPerf.learned[k] = v
	}
	autoPerfTrimLocked()
}

// autoPerfTrimLocked keeps the autoPerfMaxApps most recently used apps.
func autoPerfTrimLocked() {
	extra := len(autoPerf.learned) - autoPerfMaxApps
	if extra <= 0 {
		return
	}
	keys := make([]string, 0, len(autoPerf.learned))
	for k := range autoPerf.learned {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return autoPerf.learned[keys[i]].Used < autoPerf.learned[keys[j]].Used })
	for _, k := range keys[:extra] {
		delete(autoPerf.learned, k)
	}
}

// autoPerfSaveLocked returns the write to run once the lock is released, or
// nil when nothing is due: at most every autoPerfSaveEvery, or now when forced.
func autoPerfSaveLocked(now time.Time, force bool) func() {
	if !autoPerf.dirty || (!force && now.Sub(autoPerf.savedAt) < autoPerfSaveEvery) {
		return nil
	}
	autoPerf.dirty = false
	autoPerf.savedAt = now
	snap := make(map[string]autoPerfLearned, len(autoPerf.learned))
	for k, v := range autoPerf.learned {
		snap[k] = v
	}
	return func() {
		path := autoPerfLearnPath()
		if path == "" {
			return
		}
		data, err := json.MarshalIndent(autoPerfFile{SchemaVersion: 1, Apps: snap}, "", "  ")
		if err != nil {
			return
		}
		autoPerfSaveMu.Lock()
		defer autoPerfSaveMu.Unlock()
		if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
			logWarn("power", "auto performance: could not save what it learned (%v)", err)
		}
	}
}
