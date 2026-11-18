package main

// fan.go — the Smart Pro S (TG5050) cooling fan.
//
// The fan is the BSP's pwm-fan cooling device: /sys/class/thermal/
// cooling_deviceN with type "pwm-fan", max_state 31 (32 PWM steps, 31 = 100%
// duty) and cur_state. Two firmware facts shape the writer:
//
//   - Firmware 1.0.2's driver added user_max_state and rejects (EINVAL, or
//     silently drops) any cur_state above it, and TrimUI's hardwareservice can
//     lower that cap. So user_max_state is raised to max_state BEFORE cur_state
//     is written, never after (spruceOS #1673/#1786).
//   - Something else may already drive the fan: TrimUI's hardwareservice, a
//     CFW script (spruce's adaptive_fan.py / thermal-watchdog) or a kernel
//     thermal zone bound to the cooling device.
//
// Ownership rule: Auto (the default) leaves the fan to whatever already drives
// it and runs JukaHub's own curve only when nothing does. Quiet and Max are the
// user's explicit choice, so JukaHub writes the fan then, and puts it back when
// the user returns to Auto or JukaHub exits. Whatever the mode or owner, a chip
// at fanEmergencyC gets full speed until it cools to fanEmergencyClearC: no
// setting can cook the SoC. There is deliberately no "Off" mode.
//
// Everything runs on the fan goroutine (2 s tick, never parked: a game in front
// is when the fan matters most). The Settings row only reads a snapshot.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// fanModeKey is the Custom setting of the Settings row.
	fanModeKey = "fanMode"

	fanModeAuto  = "auto"
	fanModeQuiet = "quiet"
	fanModeMax   = "max"

	// fanTick is how often the curve is evaluated.
	fanTick = 2 * time.Second
	// fanOwnerEvery is how often the process list and thermal bindings are
	// re-read to find who owns the fan.
	fanOwnerEvery = 30 * time.Second
	// fanHysteresisC is how far the chip must cool below a step's start before
	// the fan steps down: no thrash at a boundary.
	fanHysteresisC = 3.0
	// fanEmergencyC / fanEmergencyClearC bound the override that runs the fan
	// flat out whatever the mode or owner.
	fanEmergencyC      = 85.0
	fanEmergencyClearC = 78.0
	// fanFightLimit is how many times in a row a manual mode may find its level
	// changed by someone else before it re-asserts only every fanOwnerEvery.
	fanFightLimit = 5
)

// fanStepShares are the spinning steps as shares of max_state: spruce's six
// levels on the TG5050 (20, 22, 24, 26, 28, 31 of 31). Below about 20 of 31
// the fan stalls rather than spinning slowly, so there is no lower step.
var fanStepShares = []float64{20.0 / 31, 22.0 / 31, 24.0 / 31, 26.0 / 31, 28.0 / 31, 1}

// fanAutoOnC / fanQuietOnC are where each step starts (°C): Auto is silent
// below 50 °C and flat out by 75 °C; Quiet waits for 62 °C. fanFloorOnC is the
// floor every mode keeps (Quiet included).
var (
	fanAutoOnC  = []float64{50, 55, 60, 65, 70, 75}
	fanQuietOnC = []float64{62, 68, 72, 76, 79, 82}
	fanFloorOnC = []float64{70, 70, 75, 75, 80, 80}
)

// fanDevice is the probed cooling device.
type fanDevice struct {
	dir      string // /sys/class/thermal/cooling_deviceN
	kind     string // its type, "pwm-fan"
	maxState int
	userMax  bool // the 1.0.2 user_max_state cap exists
}

var fanProbeOnce struct {
	once sync.Once
	dev  fanDevice
	ok   bool
}

// fanProbe finds the fan, once: the Smart Pro S only, and only when a cooling
// device whose type names a fan is there with a usable max_state.
func fanProbe() (fanDevice, bool) {
	fanProbeOnce.once.Do(func() {
		if runtime.GOOS != "linux" || !IsTrimuiDevice() {
			return
		}
		dev, ok := fanFind("/sys/class/thermal")
		if !ok {
			if DetectDevice() == DeviceTrimuiSmartProS {
				logWarn("fan", "no pwm-fan cooling device found; fan control off")
			}
			return
		}
		if DetectDevice() != DeviceTrimuiSmartProS {
			// A fan on a model that has none is a misidentified board or an
			// unknown one: report it, never write it.
			logInfo("fan", "%s (%s) found on %s; JukaHub leaves it alone", filepath.Base(dev.dir), dev.kind, deviceDisplayName(DetectDevice()))
			return
		}
		fanProbeOnce.dev, fanProbeOnce.ok = dev, true
		logInfo("fan", "%s: type=%s max_state=%d user_max_state=%v", filepath.Base(dev.dir), dev.kind, dev.maxState, dev.userMax)
	})
	return fanProbeOnce.dev, fanProbeOnce.ok
}

// fanFind looks under root for a cooling device whose type names a fan.
func fanFind(root string) (fanDevice, bool) {
	types, _ := filepath.Glob(filepath.Join(root, "cooling_device*", "type"))
	for _, t := range types {
		data, err := os.ReadFile(t)
		if err != nil {
			continue
		}
		kind := strings.TrimSpace(string(data))
		if !strings.Contains(strings.ToLower(kind), "fan") {
			continue
		}
		dir := filepath.Dir(t)
		maxState, ok := fanReadInt(filepath.Join(dir, "max_state"))
		if !ok || maxState <= 0 {
			continue
		}
		if _, ok := fanReadInt(filepath.Join(dir, "cur_state")); !ok {
			continue
		}
		_, err = os.Stat(filepath.Join(dir, "user_max_state"))
		return fanDevice{dir: dir, kind: kind, maxState: maxState, userMax: err == nil}, true
	}
	return fanDevice{}, false
}

func fanReadInt(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return n, err == nil
}

// fanAvailable reports whether this handheld has a fan JukaHub may drive.
func fanAvailable() bool {
	_, ok := fanProbe()
	return ok
}

// fanSteps turns the shares into cur_state values for this max_state.
// Pure.
func fanSteps(maxState int) []int {
	out := make([]int, len(fanStepShares))
	for i, s := range fanStepShares {
		v := int(s*float64(maxState) + 0.5)
		if v < 1 {
			v = 1
		}
		if v > maxState {
			v = maxState
		}
		out[i] = v
	}
	return out
}

// fanCurveStep is the step (0 = off, 1..len(on)) for temp, given the step the
// fan is on now: it steps up as soon as temp reaches a step's start and steps
// down only once temp is hyst below it. Pure.
func fanCurveStep(on []float64, temp float64, cur int, hyst float64) int {
	up, down := 0, 0
	for _, t := range on {
		if temp >= t {
			up++
		}
		if temp+hyst >= t {
			down++
		}
	}
	switch {
	case up > cur:
		return up
	case down < cur:
		return down
	}
	return cur
}

// fanMode is the mode in force; the Settings row stores it and the goroutine
// reads it.
var fanModeNow atomic.Value // string

func fanModeLoad() string {
	if v, ok := fanModeNow.Load().(string); ok {
		return v
	}
	return fanModeAuto
}

// fanNormaliseMode maps a stored spelling to a mode; anything else is Auto.
func fanNormaliseMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case fanModeQuiet:
		return fanModeQuiet
	case fanModeMax, "full":
		return fanModeMax
	}
	return fanModeAuto
}

func fanModeFromConfig(c *Config) string {
	if c == nil {
		return fanModeAuto
	}
	return fanNormaliseMode(customStringValue(c.Variables.Custom, fanModeKey))
}

// fanShown is what the goroutine last did, for the Settings row and the
// drivers page.
var fanShown struct {
	mu       sync.Mutex
	tempC    float64
	tempOK   bool
	level    int // cur_state read back; -1 unknown
	owner    string
	writing  bool // JukaHub is driving the fan now
	emerg    bool
	fighting bool
	err      string
	seen     bool // the goroutine has published once
}

// fanCtl is the goroutine's own state. mu also orders the exit restore after
// the last write.
var fanCtl struct {
	mu       sync.Mutex
	started  bool
	stopped  bool
	taken    bool
	savedCur int
	savedCap int // -1: no cap node or unreadable
	step     int // the curve's current step
	wrote    int // last cur_state written; -1 none
	fights   int
	failed   bool // a write failed: logged once, retried every fanOwnerEvery
	failedAt time.Time
}

func init() {
	fanModeNow.Store(fanModeAuto)
	bootOnShutdown(fanRelease)
	registerDriverStatus(driverRowFan, fanDriverStatus)
}

// startFanControl reads the saved mode (main thread) and starts the fan
// goroutine when there is a fan. startThermalMonitor calls it.
func startFanControl(c *Config) {
	fanModeNow.Store(fanModeFromConfig(c))
	if runtime.GOOS != "linux" || DetectDevice() != DeviceTrimuiSmartProS {
		return
	}
	fanCtl.mu.Lock()
	if fanCtl.started {
		fanCtl.mu.Unlock()
		return
	}
	fanCtl.started = true
	fanCtl.mu.Unlock()
	go func() {
		dev, ok := fanProbe()
		if !ok {
			return
		}
		fanLoop(dev)
	}()
}

// fanLoop is the controller. It never parks: the fan matters most while a
// game holds the screen.
func fanLoop(dev fanDevice) {
	ticker := time.NewTicker(fanTick)
	defer ticker.Stop()
	var owner string
	var ownerAt time.Time
	lastOwner, lastMode := "?", ""
	emerg := false
	for {
		now := time.Now()
		if ownerAt.IsZero() || now.Sub(ownerAt) >= fanOwnerEvery {
			owner, ownerAt = fanFirmwareOwner(dev), now
		}
		mode := fanModeLoad()
		if owner != lastOwner || mode != lastMode {
			who := owner
			if who == "" {
				who = "nobody"
			}
			logInfo("fan", "mode %s; firmware owner: %s", mode, who)
			lastOwner, lastMode = owner, mode
		}
		temp, tempOK := cpuTempValueC()
		if tempOK && temp >= fanEmergencyC && !emerg {
			emerg = true
			logWarn("fan", "chip at %.0f °C: fan to full speed until %.0f °C", temp, fanEmergencyClearC)
		} else if emerg && (!tempOK || temp < fanEmergencyClearC) {
			emerg = false
			logInfo("fan", "chip back under %.0f °C: emergency speed off", fanEmergencyClearC)
		}
		if !fanStep(dev, mode, owner, temp, tempOK, emerg) {
			return
		}
		<-ticker.C
	}
}

// fanStep evaluates one tick. It returns false once the exit restore ran.
func fanStep(dev fanDevice, mode, owner string, temp float64, tempOK, emerg bool) bool {
	steps := fanSteps(dev.maxState)
	fanCtl.mu.Lock()
	defer fanCtl.mu.Unlock()
	if fanCtl.stopped {
		return false
	}
	want := -1 // leave the fan alone
	switch {
	case emerg || mode == fanModeMax:
		want = dev.maxState
		fanCtl.step = len(steps)
	case mode == fanModeQuiet && !tempOK:
		// No temperature: a middle step is safe and quiet enough.
		fanCtl.step = len(steps) / 2
		want = steps[fanCtl.step-1]
	case mode == fanModeQuiet || (owner == "" && tempOK):
		on := fanAutoOnC
		if mode == fanModeQuiet {
			on = fanQuietOnC
		}
		step := fanCurveStep(on, temp, fanCtl.step, fanHysteresisC)
		if floor := fanCurveStep(fanFloorOnC, temp, 0, 0); floor > step {
			step = floor
		}
		fanCtl.step = step
		want = 0
		if step > 0 {
			want = steps[step-1]
		}
	}
	cur, curOK := fanReadInt(filepath.Join(dev.dir, "cur_state"))
	if want < 0 {
		if fanCtl.taken {
			fanRestoreLocked(dev, temp, tempOK)
		}
		fanPublish(temp, tempOK, cur, curOK, owner, false, emerg)
		return true
	}
	if !fanCtl.taken {
		fanCtl.savedCur = cur
		if !curOK {
			fanCtl.savedCur = -1
		}
		fanCtl.savedCap = -1
		if dev.userMax {
			if v, ok := fanReadInt(filepath.Join(dev.dir, "user_max_state")); ok {
				fanCtl.savedCap = v
			}
		}
		fanCtl.taken, fanCtl.wrote, fanCtl.fights = true, -1, 0
	}
	// Someone else changed what JukaHub wrote: count it, and after a few ticks
	// in a row only win back every fanOwnerEvery (a fan that flips every tick
	// is worse than one that sits at the firmware's level). The emergency
	// speed always wins.
	if fanCtl.wrote >= 0 && curOK {
		if cur == fanCtl.wrote {
			fanCtl.fights = 0
		} else {
			fanCtl.fights++
			if fanCtl.fights == fanFightLimit {
				logWarn("fan", "something else keeps changing the fan (%s); re-asserting every %s", owner, fanOwnerEvery)
			}
		}
	}
	retryEvery := int(fanOwnerEvery / fanTick)
	switch {
	case curOK && cur == want:
		// Already there (whoever set it): that is the level to defend, or
		// the next tick would count it as a fight.
		fanCtl.wrote, fanCtl.failed = want, false
	case !emerg && fanCtl.fights > fanFightLimit && fanCtl.fights%retryEvery != 0:
	case fanCtl.failed && !emerg && time.Since(fanCtl.failedAt) < fanOwnerEvery:
		// A node that refuses writes is retried rarely, not every tick.
	default:
		if err := fanWriteLevel(dev, want); err != nil {
			if !fanCtl.failed {
				logWarn("fan", "could not set the fan to %d: %v", want, err)
			}
			fanCtl.failed, fanCtl.failedAt = true, time.Now()
		} else {
			fanCtl.failed = false
			fanCtl.wrote = want
			cur, curOK = want, true
		}
	}
	fanPublish(temp, tempOK, cur, curOK, owner, true, emerg)
	return true
}

// fanWriteLevel sets cur_state, raising the 1.0.2 cap first when it would
// refuse the level. Caller holds fanCtl.mu. A cap that cannot be raised still
// gets the fan as fast as it allows (the emergency's best effort), and the
// error is returned.
func fanWriteLevel(dev fanDevice, level int) error {
	if dev.userMax {
		capPath := filepath.Join(dev.dir, "user_max_state")
		if c, ok := fanReadInt(capPath); !ok || c < level {
			if err := writeSysfsInt(capPath, dev.maxState); err != nil {
				if ok && c > 0 {
					_ = writeSysfsInt(filepath.Join(dev.dir, "cur_state"), c)
				}
				return fmt.Errorf("user_max_state: %w", err)
			}
		}
	}
	return writeSysfsInt(filepath.Join(dev.dir, "cur_state"), level)
}

// fanRestoreLocked puts back the level and cap the fan had before JukaHub
// wrote it: cur_state first, while the raised cap still allows it, then the
// cap. The level saved minutes ago may be "off": a chip that is hot now keeps
// at least the floor curve's level, so a hand-back to a daemon that only
// writes on a threshold change (or to nobody, at exit) never parks a hot SoC
// with a stopped fan. Caller holds fanCtl.mu.
func fanRestoreLocked(dev fanDevice, temp float64, tempOK bool) {
	if !fanCtl.taken {
		return
	}
	fanCtl.taken, fanCtl.wrote, fanCtl.fights = false, -1, 0
	level := fanCtl.savedCur
	if tempOK && level >= 0 {
		if floor := fanCurveStep(fanFloorOnC, temp, 0, 0); floor > 0 {
			if v := fanSteps(dev.maxState)[floor-1]; v > level {
				level = v
			}
		}
	}
	if level >= 0 {
		if err := writeSysfsInt(filepath.Join(dev.dir, "cur_state"), level); err != nil {
			logWarn("fan", "could not restore cur_state %d: %v", level, err)
		}
	}
	if dev.userMax && fanCtl.savedCap >= 0 {
		if err := writeSysfsInt(filepath.Join(dev.dir, "user_max_state"), fanCtl.savedCap); err != nil {
			logWarn("fan", "could not restore user_max_state %d: %v", fanCtl.savedCap, err)
		}
	}
	logInfo("fan", "fan handed back (cur_state %d, user_max_state %d)", level, fanCtl.savedCap)
}

// fanRelease is the exit hook: stop the goroutine and hand the fan back. Safe
// to call twice (the shutdown hook and the forced-exit restore both do).
func fanRelease() {
	dev, ok := fanProbeDone()
	var temp float64
	var tempOK bool
	if ok {
		// Read before the lock: the hand-back keeps a hot chip's floor.
		temp, tempOK = cpuTempValueC()
	}
	fanCtl.mu.Lock()
	defer fanCtl.mu.Unlock()
	fanCtl.stopped = true
	if ok {
		fanRestoreLocked(dev, temp, tempOK)
	}
}

// fanProbeDone is fanProbe without running it: the exit hook must not start a
// probe on a device that never had one.
func fanProbeDone() (fanDevice, bool) {
	fanCtl.mu.Lock()
	started := fanCtl.started
	fanCtl.mu.Unlock()
	if !started {
		return fanDevice{}, false
	}
	return fanProbe()
}

func fanPublish(temp float64, tempOK bool, cur int, curOK bool, owner string, writing, emerg bool) {
	fanShown.mu.Lock()
	defer fanShown.mu.Unlock()
	fanShown.tempC, fanShown.tempOK = temp, tempOK
	fanShown.level = -1
	if curOK {
		fanShown.level = cur
	}
	fanShown.owner, fanShown.writing, fanShown.emerg = owner, writing, emerg
	fanShown.seen = true
	fanShown.fighting = fanCtl.fights >= fanFightLimit
	fanShown.err = ""
	if fanCtl.failed {
		fanShown.err = "the fan refuses writes"
	}
}

// fanDaemonNames are processes known to drive the TG5050 fan: TrimUI's
// hardwareservice (it also lowers user_max_state) and spruceOS's scripts.
var fanDaemonNames = []string{
	"hardwareservice", "adaptive_fan", "thermal-watchdog", "thermal_watchdog",
	"fancontrol", "fan_control", "fan-control", "fanctl", "thermald",
}

// fanFirmwareOwner names what already drives the fan, or "" when nothing
// does: a running fan daemon, or a kernel thermal zone with the fan bound to
// it and its governor active.
func fanFirmwareOwner(dev fanDevice) string {
	if name := fanDaemonRunning(); name != "" {
		return name
	}
	devReal, err := filepath.EvalSymlinks(dev.dir)
	if err != nil {
		devReal = dev.dir
	}
	links, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/cdev[0-9]*")
	for _, l := range links {
		if strings.Contains(filepath.Base(l), "_") {
			continue // cdevN_trip_point, cdevN_weight
		}
		real, err := filepath.EvalSymlinks(l)
		if err != nil || real != devReal {
			continue
		}
		zone := filepath.Dir(l)
		if mode := strings.TrimSpace(fanReadString(filepath.Join(zone, "mode"))); mode == "disabled" {
			continue
		}
		policy := strings.TrimSpace(fanReadString(filepath.Join(zone, "policy")))
		if policy == "user_space" {
			continue
		}
		kind := strings.TrimSpace(fanReadString(filepath.Join(zone, "type")))
		return "kernel " + kind + " (" + policy + ")"
	}
	return ""
}

// fanDaemonRunning scans /proc for a fan daemon, every fanOwnerEvery, on the
// fan goroutine.
func fanDaemonRunning() string { return procNameRunning(fanDaemonNames) }

// procNameRunning returns the first of names found in a running process's
// argv[0] or argv[1] (a script runs as "python3 adaptive_fan.py"), or "". A
// few hundred small reads; call it off the render thread.
func procNameRunning(names []string) string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	self := strconv.Itoa(os.Getpid())
	for _, e := range entries {
		name := e.Name()
		if name == self || name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", name, "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		args := strings.SplitN(string(data), "\x00", 3)
		for i := 0; i < len(args) && i < 2; i++ {
			base := strings.ToLower(filepath.Base(args[i]))
			for _, d := range names {
				if strings.Contains(base, d) {
					return d
				}
			}
		}
	}
	return ""
}

func fanReadString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// ── Settings ──────────────────────────────────────────────────────────────

func fanModeLabel(m string) string {
	switch m {
	case fanModeQuiet:
		return "Quiet"
	case fanModeMax:
		return "Max"
	}
	return "Auto"
}

var fanModes = []string{fanModeAuto, fanModeQuiet, fanModeMax}

// fanSettingsRow is Settings' Fan row, shown only on a handheld with a fan.
func fanSettingsRow() setRow {
	return setRow{
		ID: fanModeKey, Label: "Fan", Kind: setKindChoice,
		Visible: func(*Config) bool { return fanAvailable() },
		Detail:  fanSettingDetail,
		Options: func(*Config) []setOption {
			return []setOption{
				{Value: fanModeAuto, Label: "Auto", Note: "The firmware's fan control, or JukaHub's curve when there is none"},
				{Value: fanModeQuiet, Label: "Quiet", Note: "Slower until the chip is hot; full speed above 80 °C"},
				{Value: fanModeMax, Label: "Max", Note: "Full speed all the time"},
			}
		},
		Current: func(c *Config) int {
			m := fanModeFromConfig(c)
			for i, v := range fanModes {
				if v == m {
					return i
				}
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			m := fanNormaliseMode(value)
			setStoreString(c, fanModeKey, m)
			fanModeNow.Store(m)
			setToast("Fan: " + fanModeLabel(m))
		},
	}
}

// fanSettingDetail is the row's second line, from the goroutine's snapshot.
func fanSettingDetail(c *Config) string {
	fanShown.mu.Lock()
	defer fanShown.mu.Unlock()
	if !fanShown.seen {
		// Before the first tick: nothing read yet, not "0%".
		return fanModeLabel(fanModeFromConfig(c))
	}
	now := ""
	if fanShown.tempOK {
		now = fmt.Sprintf("%.0f °C", fanShown.tempC)
	}
	if fanShown.level >= 0 {
		if now != "" {
			now += " · "
		}
		now += fmt.Sprintf("fan %d%%", fanLevelPercent(fanShown.level))
	}
	var why string
	switch {
	case fanShown.err != "":
		why = "Not working: " + fanShown.err
	case fanShown.emerg:
		why = "Hot: full speed until it cools"
	case fanShown.fighting:
		why = "The firmware keeps changing the fan"
	case fanModeFromConfig(c) == fanModeAuto && fanShown.owner != "" && !fanShown.writing:
		why = "Auto: the firmware drives the fan"
	case fanModeFromConfig(c) == fanModeAuto && !fanShown.writing && fanShown.owner == "" && !fanShown.tempOK:
		why = "Auto: no temperature reading; the fan is left as it is"
	case fanModeFromConfig(c) == fanModeAuto:
		why = "Auto: JukaHub's curve"
	default:
		why = fanModeLabel(fanModeFromConfig(c))
	}
	if now == "" {
		return why
	}
	return why + " · " + now
}

// fanLevelPercent is a cur_state as a share of max_state.
func fanLevelPercent(level int) int {
	dev, ok := fanProbe()
	if !ok || dev.maxState <= 0 {
		return 0
	}
	return level * 100 / dev.maxState
}

// fanDriverStatus is the Fan line of Settings → Drivers & hardware.
func fanDriverStatus() driverStatus {
	hw := deviceHardwareCurrent()
	dev, ok := fanProbe()
	if !ok {
		if hw.HasFan {
			return driverStatus{State: driverMissing, Driver: "pwm-fan", Detail: "no fan cooling device"}
		}
		return driverStatus{State: driverNotUsed, Detail: "no fan on this model"}
	}
	fanShown.mu.Lock()
	level, owner, writing, failed := fanShown.level, fanShown.owner, fanShown.writing, fanShown.err
	if !fanShown.seen {
		level = -1
	}
	fanShown.mu.Unlock()
	st := driverStatus{State: driverOK, Driver: dev.kind, Node: dev.dir}
	who := "JukaHub"
	if !writing && owner != "" {
		who = owner
	}
	st.Detail = fmt.Sprintf("%s · driven by %s", fanModeLabel(fanModeLoad()), who)
	if level >= 0 {
		st.Detail += fmt.Sprintf(" · %d/%d", level, dev.maxState)
	}
	if failed != "" {
		st.State, st.Detail = driverProblem, failed
	}
	return st
}
