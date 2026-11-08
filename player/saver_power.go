package main

// saver_power.go — the idle screens' low-power state.
//
// A handheld left on the dock or the starfield is the device doing nothing,
// and it should cost as little as nothing can. Once an idle screen has been up
// for a few seconds this file:
//
//   - caps the CPU: scaling_max_freq drops to a low operating point (one that
//     still decodes music smoothly while a track plays on the dock) and, when
//     nothing plays at all, the governor goes to powersave. Every value is read
//     before it is written and put back exactly — on wake, on exit, and after a
//     crash (a marker in the temp directory is replayed by the next start);
//   - slows the frame rate to the idle cadence (10 fps for the starfield, 4 for
//     the dock, whose clock changes once a minute), while still answering a
//     press within one short poll, so the slow cadence never delays a wake;
//   - parks the background pollers (connectivity probe, audio-output probe,
//     weather, LED animation) through the same gate a running game uses.
//
// The CPU half coordinates with powerprofile.go rather than fighting it: it
// never engages while a game's launch profile is up, it restores a value only
// if the value is still the one it wrote (so a launch, the shutdown restore or
// the user changing the governor always wins), and it reuses the cpufreq
// helpers and the cpufreqRoot test seam from there.
//
// Everything here is a no-op off the handheld: a desktop has nothing to clock
// down, and its idle cadence is already handled by the adaptive frame rate.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// saverPowerSettle is how long an idle screen is up before it clocks down:
	// past the starfield's fade-in and the picture's first decode, and long
	// enough that glancing at the dock and waking it again costs nothing.
	saverPowerSettle = 3 * time.Second

	// saverPowerCheck throttles the decision; the state changes on events far
	// slower than a frame.
	saverPowerCheck = 500 * time.Millisecond

	// saverQuietFloorKHz is the lowest ceiling used while nothing plays: the
	// lowest operating point at or above it. 600 MHz keeps a 10 fps starfield
	// and the dock's clock fluid on the A133P.
	saverQuietFloorKHz = 600_000

	// saverAudioFloorKHz is the ceiling floor while music plays on the dock:
	// enough headroom that the decoder never underruns at the idle clock.
	saverAudioFloorKHz = 816_000

	// The idle frame budgets. The starfield animates; the dock's content
	// changes once a second at most.
	saverStarfieldFrameMS = uint32(100)
	saverDockFrameMS      = uint32(250)

	// saverWakePollMS is how often the idle sleep looks for a queued press.
	saverWakePollMS = uint32(20)
)

// saverPowerMode is the low-power profile in force.
type saverPowerMode int

const (
	saverPowerOff   saverPowerMode = iota // awake: nothing applied
	saverPowerQuiet                       // idle and silent: lowest ceiling, powersave
	saverPowerAudio                       // idle with music: a ceiling that keeps decoding smooth
)

// saverSysfsWrite is one value the idle state wrote and the value it replaced.
// The marker file stores the same records, so the next start can undo a write
// a crash left behind.
type saverSysfsWrite struct {
	Path     string `json:"path"`
	Original string `json:"original"`
	Written  string `json:"written"`
}

// saverPowerMarker is the crash marker's content.
type saverPowerMarker struct {
	Writes []saverSysfsWrite `json:"writes"`
}

var saverPower struct {
	mu      sync.Mutex
	mode    saverPowerMode
	writes  []saverSysfsWrite // in the order they were made
	since   time.Time         // when the current idle screen came up
	checked time.Time
	denied  bool // the firmware refused a write: do not retry this session
}

// saverPowerEngaged is the lock-free view of "an idle screen is in its
// low-power state", read by the frame sleep, the background pollers and the
// Wi-Fi chip's cadence.
var saverPowerEngaged atomic.Bool

// saverPowerHooked records that the exit hook has been registered.
var saverPowerHooked atomic.Bool

// saverPowerSupported reports whether this device gets the low-power state. A
// variable so the tests can turn it on against a fake cpufreq tree.
var saverPowerSupported = func() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice()
}

// saverPowerMarkerPath is where the crash marker lives: the temp directory is
// RAM on the handheld, so the marker dies with a reboot — which also resets the
// CPU — and is never written to the SD card.
var saverPowerMarkerPath = func() string {
	return filepath.Join(os.TempDir(), "jukahub-idle-cpufreq.json")
}

// saverLowPowerEngaged reports whether the idle low-power state is in force.
func saverLowPowerEngaged() bool { return saverPowerEngaged.Load() }

// saverPowerWant is the pure decision: the profile an idle screen should run.
// Nothing is lowered before the screen has settled, while an alarm rings,
// while a video is on screen (its decoder needs the clock), or while a launch
// profile owns the CPU.
func saverPowerWant(idleUp bool, idleFor time.Duration, alarm, video, launch, music bool) saverPowerMode {
	if !idleUp || idleFor < saverPowerSettle || alarm || video || launch {
		return saverPowerOff
	}
	if music {
		return saverPowerAudio
	}
	return saverPowerQuiet
}

// saverLaunchActive reports whether a game or an app owns the CPU profile.
func saverLaunchActive() bool {
	if gameScreenSuspended() {
		return true
	}
	launchPowerProfile.mu.Lock()
	depth := launchPowerProfile.depth
	launchPowerProfile.mu.Unlock()
	return depth > 0
}

// saverPowerTick runs once per frame on the event thread (screensaverAutoTick
// calls it) and moves the state to what the idle screens want.
func saverPowerTick(now time.Time) {
	idleUp := ss.active || ambientIsActive()
	saverPower.mu.Lock()
	switch {
	case !idleUp:
		saverPower.since = time.Time{}
	case saverPower.since.IsZero():
		saverPower.since = now
	}
	since := saverPower.since
	due := saverPower.checked.IsZero() || now.Sub(saverPower.checked) >= saverPowerCheck
	if due {
		saverPower.checked = now
	}
	mode := saverPower.mode
	saverPower.mu.Unlock()
	if !due {
		return
	}
	want := saverPowerOff
	if idleUp && saverPowerSupported() {
		_, _, _, playing, _ := ambientNowPlaying()
		want = saverPowerWant(true, now.Sub(since), alarmFiringActive(), videoHoldsScreenAwake(), saverLaunchActive(), playing)
	}
	if want != mode {
		saverPowerApply(want)
	}
}

// saverPowerWake leaves the low-power state at once. The wake paths call it
// before the first awake frame is drawn, so the UI never runs a frame at the
// idle clock.
func saverPowerWake() {
	saverPower.mu.Lock()
	saverPower.since = time.Time{}
	engaged := saverPower.mode != saverPowerOff
	saverPower.mu.Unlock()
	if engaged {
		saverPowerApply(saverPowerOff)
	}
}

// saverPowerApply moves to want: whatever the previous profile wrote is put
// back first, then the new profile's values are written.
func saverPowerApply(want saverPowerMode) {
	saverPower.mu.Lock()
	defer saverPower.mu.Unlock()
	if want == saverPower.mode {
		return
	}
	hadWrites := len(saverPower.writes) > 0
	saverPowerRestoreLocked()
	saverPower.mode = want
	if want == saverPowerOff {
		saverPowerEngaged.Store(false)
		if hadWrites {
			log.Printf("[IDLE] low power off: CPU settings restored")
		}
		return
	}
	saverPowerEngaged.Store(true)
	saverPowerHookExit()
	if saverPower.denied {
		return
	}
	plan := saverPowerPlan(cpufreqRoot, want)
	if len(plan) == 0 {
		log.Printf("[IDLE] low power on: frame rate and pollers only (no CPU ceiling to lower)")
		return
	}
	// The marker is written before the first value changes, so a crash at
	// any point leaves enough behind to put the CPU back.
	saverPowerWriteMarker(plan)
	for _, w := range plan {
		if err := os.WriteFile(w.Path, []byte(w.Written+"\n"), 0o644); err != nil {
			saverPower.denied = true
			log.Printf("[IDLE] low power: %s is read-only on this firmware (%v)", filepath.Base(w.Path), err)
			break
		}
		// The kernel may normalise what it was given; what it reports back
		// is what a later restore compares against.
		if got := saverReadTrimmed(w.Path); got != "" {
			w.Written = got
		}
		saverPower.writes = append(saverPower.writes, w)
	}
	if len(saverPower.writes) == 0 {
		saverPowerRemoveMarker()
		return
	}
	saverPowerWriteMarker(saverPower.writes)
	log.Printf("[IDLE] low power on: %s", saverPowerSummary(saverPower.writes))
}

// saverPowerRestoreLocked puts back every value the idle state wrote, newest
// first, and only where the value is still the one it wrote: a launch profile,
// the shutdown restore or the user may have changed it since, and theirs wins.
func saverPowerRestoreLocked() {
	if len(saverPower.writes) > 0 {
		saverRestoreWrites(saverPower.writes)
		saverPower.writes = nil
	}
	saverPowerRemoveMarker()
}

// saverRestoreWrites undoes writes in reverse order, compare-and-restore. It
// reports how many values were put back.
func saverRestoreWrites(writes []saverSysfsWrite) int {
	restored := 0
	for i := len(writes) - 1; i >= 0; i-- {
		w := writes[i]
		if w.Path == "" || w.Original == "" {
			continue
		}
		if cur := saverReadTrimmed(w.Path); cur != w.Written {
			continue
		}
		if err := os.WriteFile(w.Path, []byte(w.Original+"\n"), 0o644); err != nil {
			log.Printf("[IDLE] could not restore %s: %v", w.Path, err)
			continue
		}
		restored++
	}
	return restored
}

// saverPowerSummary is the log line for an engaged state.
func saverPowerSummary(writes []saverSysfsWrite) string {
	ceiling, governor := "", ""
	for _, w := range writes {
		switch filepath.Base(w.Path) {
		case "scaling_max_freq":
			if khz, err := strconv.ParseInt(w.Written, 10, 64); err == nil {
				ceiling = strconv.FormatInt(khz/1000, 10) + " MHz ceiling"
			}
		case "scaling_governor":
			governor = w.Written + " governor"
		}
	}
	parts := make([]string, 0, 2)
	for _, p := range []string{ceiling, governor} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "CPU limits applied"
	}
	return strings.Join(parts, ", ")
}

// ── The plan ─────────────────────────────────────────────────────────────

// saverPowerPlan lists the writes that take every CPU policy to want. A policy
// shared by several CPUs (cpuN/cpufreq is a link to one policyM directory on
// the A133P) is planned once.
func saverPowerPlan(root string, want saverPowerMode) []saverSysfsWrite {
	if want == saverPowerOff {
		return nil
	}
	var plan []saverSysfsWrite
	seen := make(map[string]bool)
	for _, dir := range cpufreqDirs(root) {
		key := dir
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			key = real
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		plan = append(plan, saverPolicyPlan(dir, want)...)
	}
	return plan
}

// saverPolicyPlan is one policy's writes, in the order they must happen: a
// floor above the new ceiling is lowered first (the kernel refuses a ceiling
// below the floor), then the ceiling, then the governor.
func saverPolicyPlan(dir string, want saverPowerMode) []saverSysfsWrite {
	var plan []saverSysfsWrite
	maxPath := filepath.Join(dir, "scaling_max_freq")
	maxRaw := saverReadTrimmed(maxPath)
	curMax, err := strconv.ParseInt(maxRaw, 10, 64)
	if err == nil && curMax > 0 {
		floor := int64(saverQuietFloorKHz)
		if want == saverPowerAudio {
			floor = saverAudioFloorKHz
		}
		target := saverIdleCeilingKHz(saverAvailableKHz(dir),
			readKHzFile(filepath.Join(dir, "cpuinfo_min_freq")),
			readKHzFile(filepath.Join(dir, "cpuinfo_max_freq")), floor)
		// Only ever lower: a ceiling the user or the firmware already set
		// below the idle one is left alone.
		if target > 0 && target < curMax {
			written := strconv.FormatInt(target, 10)
			minPath := filepath.Join(dir, "scaling_min_freq")
			minRaw := saverReadTrimmed(minPath)
			if curMin, err := strconv.ParseInt(minRaw, 10, 64); err == nil && curMin > target {
				plan = append(plan, saverSysfsWrite{Path: minPath, Original: minRaw, Written: written})
			}
			plan = append(plan, saverSysfsWrite{Path: maxPath, Original: maxRaw, Written: written})
		}
	}
	if want == saverPowerQuiet {
		govPath := filepath.Join(dir, "scaling_governor")
		cur := saverReadTrimmed(govPath)
		if cur != "" && cur != "powersave" && saverHasGovernor(govPath, "powersave") {
			plan = append(plan, saverSysfsWrite{Path: govPath, Original: cur, Written: "powersave"})
		}
	}
	return plan
}

// saverIdleCeilingKHz picks the idle ceiling: the lowest advertised operating
// point at or above floor. Without a table, the floor clamped to the CPU's own
// range. Zero means there is nothing safe to choose.
func saverIdleCeilingKHz(available []int64, cpuMin, cpuMax, floor int64) int64 {
	if len(available) > 0 {
		sorted := append([]int64(nil), available...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		for _, f := range sorted {
			if f >= floor {
				return f
			}
		}
		return sorted[len(sorted)-1]
	}
	if cpuMin <= 0 && cpuMax <= 0 {
		return 0
	}
	target := floor
	if target < cpuMin {
		target = cpuMin
	}
	if cpuMax > 0 && target > cpuMax {
		target = cpuMax
	}
	return target
}

// saverAvailableKHz reads a policy's operating points.
func saverAvailableKHz(dir string) []int64 {
	raw := saverReadTrimmed(filepath.Join(dir, "scaling_available_frequencies"))
	var out []int64
	for _, field := range strings.Fields(raw) {
		if v, err := strconv.ParseInt(field, 10, 64); err == nil && v > 0 {
			out = append(out, v)
		}
	}
	return out
}

// saverHasGovernor reports whether the policy offers a governor.
func saverHasGovernor(govPath, name string) bool {
	for _, g := range availableGovernors(govPath) {
		if g == name {
			return true
		}
	}
	return false
}

// saverReadTrimmed reads a small sysfs file, "" when it cannot be read.
func saverReadTrimmed(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// ── Crash marker ─────────────────────────────────────────────────────────

func saverPowerWriteMarker(writes []saverSysfsWrite) {
	data, err := json.Marshal(saverPowerMarker{Writes: writes})
	if err != nil {
		return
	}
	if err := os.WriteFile(saverPowerMarkerPath(), data, 0o600); err != nil {
		log.Printf("[IDLE] could not record the CPU settings to restore: %v", err)
	}
}

func saverPowerRemoveMarker() {
	if err := os.Remove(saverPowerMarkerPath()); err != nil && !os.IsNotExist(err) {
		log.Printf("[IDLE] could not remove %s: %v", saverPowerMarkerPath(), err)
	}
}

// saverPowerRecover undoes what a previous run left behind when it died in the
// low-power state, and registers the exit hook. main calls it before
// captureBootCPUSettings, so the "boot" values recorded for the shutdown are
// the real ones and not a crashed idle screen's.
func saverPowerRecover() {
	saverPowerHookExit()
	data, err := os.ReadFile(saverPowerMarkerPath())
	if err != nil {
		return
	}
	var marker saverPowerMarker
	if json.Unmarshal(data, &marker) == nil && len(marker.Writes) > 0 {
		if n := saverRestoreWrites(marker.Writes); n > 0 {
			log.Printf("[IDLE] restored %d CPU setting(s) an interrupted idle screen left lowered", n)
		}
	}
	saverPowerRemoveMarker()
}

// saverPowerHookExit registers the exit hook once: every exit that runs the
// deferred cleanup — the Exit button, a signal, a panic on the main loop —
// leaves the low-power state on its way out.
func saverPowerHookExit() {
	if saverPowerHooked.Swap(true) {
		return
	}
	Cleanup = append(Cleanup, func() { saverPowerApply(saverPowerOff) })
}

// ── Frame pacing ─────────────────────────────────────────────────────────

// saverPressEvents are the inputs that end an idle sleep early: anything that
// can wake the screen or press one of its buttons. Motion and axis noise do
// not count, so a drifting stick cannot keep the frame rate up.
var saverPressEvents = []uint32{
	uint32(sdl.QUIT),
	uint32(sdl.KEYDOWN),
	uint32(sdl.MOUSEBUTTONDOWN),
	uint32(sdl.MOUSEWHEEL),
	uint32(sdl.CONTROLLERBUTTONDOWN),
	uint32(sdl.JOYBUTTONDOWN),
	uint32(sdl.JOYHATMOTION), // a d-pad some pads report as a hat
	uint32(sdl.FINGERDOWN),
}

// saverPressQueued reports whether a press is waiting in SDL's queue. The
// caller pumps first; nothing is removed from the queue.
func saverPressQueued() bool {
	for _, t := range saverPressEvents {
		if sdl.HasEvent(t) {
			return true
		}
	}
	return false
}

// saverToastShowing reports whether a toast is on screen (a volume change on
// the dock raises one): its slide and fade get the normal cadence.
func saverToastShowing() bool {
	currentToastMu.Lock()
	t := currentToast
	currentToastMu.Unlock()
	if strings.TrimSpace(t.Message) == "" || t.Duration == 0 {
		return false
	}
	return sdl.GetTicks64()-t.StartedAt <= t.Duration
}

// saverFrameBudget is the idle frame budget in milliseconds, 0 when the normal
// cadence applies: awake, or while something is animating — a picture fading
// in, a toast sliding — which at four frames a second would look broken.
func saverFrameBudget(now time.Time) uint32 {
	if !saverLowPowerEngaged() {
		return 0
	}
	if saverBackdropFading(now) || saverToastShowing() {
		return 0
	}
	if ambientIsActive() {
		if saverDockPhotoFading(now) {
			return 0
		}
		return saverDockFrameMS
	}
	return saverStarfieldFrameMS
}

// saverFrameSleep is the main loop's end-of-frame sleep. Awake it is exactly
// sdl.Delay(normal). In the idle low-power state the frame waits for the idle
// budget instead, looking for a queued press every saverWakePollMS and
// returning the moment there is one: the press is handled on the very next
// iteration, so the slow cadence never costs the wake any latency.
func saverFrameSleep(normal uint32) {
	budget := saverFrameBudget(time.Now())
	if budget <= normal {
		sdl.Delay(normal)
		return
	}
	start := sdl.GetTicks64()
	for {
		elapsed := sdl.GetTicks64() - start
		if elapsed >= uint64(budget) {
			return
		}
		step := uint64(saverWakePollMS)
		if left := uint64(budget) - elapsed; left < step {
			step = left
		}
		sdl.Delay(uint32(step))
		sdl.PumpEvents()
		if saverPressQueued() {
			return
		}
	}
}

// saverPausesBackground reports whether the background pollers should park
// (backgroundwork.go): an idle screen in its low-power state has nobody
// looking at a forecast refresh or an LED animation.
func saverPausesBackground() bool { return saverLowPowerEngaged() }
