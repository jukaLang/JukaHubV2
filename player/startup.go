package main

// startup.go — how long boot takes, and how the splash fits inside it.
//
// The app used to play a fixed 3.6 s splash no matter how fast the rest of the
// boot was, so every launch cost at least that. The splash now gets whatever is
// left of a two-second budget: a warm device spends a few hundred milliseconds
// on it, a cold SD card skips it entirely because the budget is already gone.
//
// The timing marks are cheap (one slice append per phase) and always on. They
// answer "why is this slow?" from a single log line instead of guesswork, which
// matters on a handheld nobody can attach a profiler to.
//
// JUKAHUB_STARTUP_BENCH=1 makes the app exit after its first frame, so the boot
// path can be timed headless in CI or on a device over SSH.
// JUKAHUB_STARTUP_TARGET_MS overrides the budget (a slower card may prefer more
// splash, not less).
//
// The whole boot is measured too, not only JukaHub's part of it: on Linux the
// kernel's uptime at the process's own start (exec) and at the first frame say
// how much of "power to Home" was the system and how much was JukaHub. The
// first frame is announced in /tmp/jukahub-first-frame ("<uptime seconds>\n"),
// which JukaHub OS's launcher waits for before it starts the radios
// (os/plastron/launch.sh): that path and format are a contract.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// startupTargetDefaultMs is the budget from process start to usable UI.
	startupTargetDefaultMs = 2000
	// startupSplashMaxMs caps the splash even on a fast boot: the animation is
	// a greeting, not the point of the app. The whole 3.6 s arc is compressed
	// into this, so a short splash still shows the complete sequence.
	startupSplashMaxMs = 750
	// startupSplashMinMs is the shortest splash worth drawing. Below this the
	// flash of a warp animation reads as a glitch, so it is skipped instead.
	startupSplashMinMs = 250
	// startupFirstFrameReserveMs is held back for the first UI frame, which is
	// the most expensive one (every label is rasterised cold). Without it the
	// splash eats the budget and the app lands just over the target.
	startupFirstFrameReserveMs = 300
)

type startupMark struct {
	name string
	at   time.Duration
}

var startupClock = struct {
	mu    sync.Mutex
	began time.Time
	marks []startupMark
	// bootAtBegan is the kernel's uptime (the clock /proc/uptime reads) when
	// began was taken; procStart is the uptime at which this process was
	// executed (/proc/self/stat). Both are seconds, 0 where unknown.
	bootAtBegan float64
	procStart   float64
	// firstFrame is the uptime at the first presented frame and firstFrameAt
	// the wall time of it; zero before (startupFirstFrameReached).
	firstFrame   float64
	firstFrameAt time.Time
}{began: time.Now(), bootAtBegan: startupBootClock(), procStart: startupProcStart()}

// startupMarkNow records a phase boundary relative to process start.
func startupMarkNow(name string) {
	startupClock.mu.Lock()
	startupClock.marks = append(startupClock.marks, startupMark{name: name, at: time.Since(startupClock.began)})
	startupClock.mu.Unlock()
}

// startupElapsed is the time since the process started.
func startupElapsed() time.Duration { return time.Since(startupClock.began) }

// startupTarget is the boot budget, overridable for slow cards.
func startupTarget() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("JUKAHUB_STARTUP_TARGET_MS")); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms >= 500 && ms <= 30000 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return startupTargetDefaultMs * time.Millisecond
}

// splashAllotment returns how long the splash may run. elapsed is what boot has
// already cost before the splash, reserve is kept back for the first UI frame;
// the splash gets the rest of the budget, up to its own cap. It returns 0 when
// there is no meaningful time left, so a slow card opens the UI directly
// instead of adding an animation on top.
func splashAllotment(elapsed, target, reserve time.Duration) time.Duration {
	if target <= 0 {
		return 0
	}
	remaining := target - elapsed - reserve
	if remaining < startupSplashMinMs*time.Millisecond {
		return 0
	}
	if remaining > startupSplashMaxMs*time.Millisecond {
		return startupSplashMaxMs * time.Millisecond
	}
	return remaining
}

// splashReserve is the time held back for the first frame at this launch: the
// measured floor, or half of what boot has already cost when that is larger
// (a slow device has a slow first frame too).
func splashReserve(elapsed time.Duration) time.Duration {
	half := elapsed / 2
	if half > startupFirstFrameReserveMs*time.Millisecond {
		return half
	}
	return startupFirstFrameReserveMs * time.Millisecond
}

// startupBenchEnabled reports whether this run exits after its first frame.
func startupBenchEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_STARTUP_BENCH")))
	return v == "1" || v == "true" || v == "yes"
}

// startupReport is the one-line boot summary: the phases, then the total.
func startupReport() string {
	startupClock.mu.Lock()
	marks := append([]startupMark(nil), startupClock.marks...)
	startupClock.mu.Unlock()

	var b strings.Builder
	b.WriteString("startup:")
	for i, m := range marks {
		if i == 0 {
			fmt.Fprintf(&b, " %s=%dms", m.name, m.at.Milliseconds())
			continue
		}
		delta := m.at - marks[i-1].at
		fmt.Fprintf(&b, " %s=+%dms", m.name, delta.Milliseconds())
	}
	total := time.Since(startupClock.began)
	fmt.Fprintf(&b, " total=%dms target=%dms", total.Milliseconds(), startupTarget().Milliseconds())
	// The whole boot, where the kernel's clock is readable: how long the
	// system took to start JukaHub, and JukaHub from its exec (Go runtime and
	// package init included, which total= is not) to the first frame.
	if kernel, own, ok := startupBootShares(); ok {
		fmt.Fprintf(&b, " kernel→start=%.2fs start→first-frame=%.2fs", kernel, own)
	}
	// What the boot prefetch read ahead for this start (bootprefetch_record_*.go):
	// " prefetch=14.2MB/412ms", "=9.1/14.2MB" while it still reads, "=off",
	// "=none", or nothing on a start that is not a cold one.
	b.WriteString(bootPrefetchReportField())
	// How much of the first frame came ready from the last start's
	// (startup_framecache.go): "labels=41/44 icons=12/12".
	if s := frameCacheSummary(); s != "" {
		b.WriteString(" " + s)
	}
	return b.String()
}

// startupBootShares reports, for this process, the kernel's uptime at its exec
// and the time from there to the first frame, in seconds. ok is false before
// the first frame or where the kernel's clock is unknown (not Linux).
func startupBootShares() (kernel, own float64, ok bool) {
	startupClock.mu.Lock()
	start, first := startupClock.procStart, startupClock.firstFrame
	startupClock.mu.Unlock()
	if start <= 0 || first <= 0 || first < start {
		return 0, 0, false
	}
	return start, first - start, true
}

// ── The first frame ───────────────────────────────────────────────────────

var startupFirstFrameOnce sync.Once

// startupFirstFrameReached is the main loop's call right after its first
// Present. It records the kernel's uptime there, hands the collector back
// (startupGCRelease), and on Linux announces the frame in
// /tmp/jukahub-first-frame and in this boot's timing record. Once per process.
func startupFirstFrameReached() {
	startupFirstFrameOnce.Do(func() {
		up := startupBootClock()
		startupClock.mu.Lock()
		startupClock.firstFrame = up
		startupClock.firstFrameAt = time.Now()
		start := startupClock.procStart
		startupClock.mu.Unlock()
		// The labels and icons this frame drew are kept for the next start
		// (startup_framecache.go); the file is written beside the loop.
		frameCacheFirstFrame()
		startupGCRelease()
		bootPrefetchFirstFrame() // log this start's prefetch, record the next (bootprefetch_record_linux.go)
		// A bench run exits right after this frame: it is not Home coming up,
		// so it announces nothing and leaves the boot's record alone. The two
		// small files are written beside the loop, not on the render thread.
		if up > 0 && !startupBenchEnabled() {
			go func() {
				startupAnnounceFirstFrame(up)
				startupRecordBootTiming(start, up)
			}()
		}
	})
}

// startupFirstFrameSince is how long ago the first frame was presented, and
// false before it.
func startupFirstFrameSince() (time.Duration, bool) {
	startupClock.mu.Lock()
	at := startupClock.firstFrameAt
	startupClock.mu.Unlock()
	if at.IsZero() {
		return 0, false
	}
	return time.Since(at), true
}

// ── Power to Home ─────────────────────────────────────────────────────────
//
// The boot's own numbers live in a record in /tmp (a fresh tmpfs every boot),
// written by the first JukaHub of the boot to reach its first frame and tied
// to the kernel's boot id, so a relaunch later in the same boot (a setting
// that restarts JukaHub, an update) still reports the boot, not itself.

const (
	startupFirstFramePath  = "/tmp/jukahub-first-frame"
	startupBootTimingPath  = "/tmp/jukahub-boot-timing"
	startupReportPath      = "/tmp/jukahub-startup-report"
	startupBootTimingStart = 60.0 // seconds: a later first start was not a boot
)

// startupBootTiming is one boot's record: the uptime at the exec of the first
// JukaHub and at its first frame.
type startupBootTiming struct {
	BootID     string
	Start      float64
	FirstFrame float64
}

// startupClockTicks is the kernel's USER_HZ, which /proc reports times in. It
// is 100 on every Linux architecture JukaHub runs on.
const startupClockTicks = 100.0

// parseProcStatStart reads starttime (seconds since boot) out of a
// /proc/<pid>/stat line, 0 when it is not there. The command name (field 2)
// is in parentheses and may hold spaces and ')', so the fields are counted
// from the last ')'.
func parseProcStatStart(stat string) float64 {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0
	}
	fields := strings.Fields(stat[end+1:])
	// fields[0] is field 3 (state); starttime is field 22.
	const startField = 22 - 3
	if len(fields) <= startField {
		return 0
	}
	ticks, err := strconv.ParseUint(fields[startField], 10, 64)
	if err != nil || ticks == 0 {
		return 0
	}
	return float64(ticks) / startupClockTicks
}

func formatStartupBootTiming(t startupBootTiming) string {
	return fmt.Sprintf("%s %.3f %.3f\n", t.BootID, t.Start, t.FirstFrame)
}

func parseStartupBootTiming(text string) (startupBootTiming, bool) {
	fields := strings.Fields(text)
	if len(fields) != 3 {
		return startupBootTiming{}, false
	}
	start, err1 := strconv.ParseFloat(fields[1], 64)
	first, err2 := strconv.ParseFloat(fields[2], 64)
	// Written as "not (in range)" so a NaN, which ParseFloat accepts and
	// every comparison rejects, is refused too.
	if err1 != nil || err2 != nil || !(start > 0) || !(first >= start) || first > 1e7 {
		return startupBootTiming{}, false
	}
	return startupBootTiming{BootID: fields[0], Start: start, FirstFrame: first}, true
}

// startupRecordBootTiming writes this boot's record unless an earlier JukaHub
// of the same boot already did. Best-effort: /tmp missing or read-only only
// costs the Settings line.
func startupRecordBootTiming(start, first float64) {
	if !startupBootClockKnown() || start <= 0 || first < start {
		return
	}
	id := strings.TrimSpace(bootOSBootIDFn())
	if id == "" || strings.ContainsAny(id, " \t\n") {
		return
	}
	if data, ok := startupReadTmpFile(startupBootTimingPath); ok {
		if prev, ok := parseStartupBootTiming(data); ok && prev.BootID == id {
			return
		}
	}
	_ = startupWriteTmpFile(startupBootTimingPath, formatStartupBootTiming(startupBootTiming{BootID: id, Start: start, FirstFrame: first}))
}

// startupAnnounceFirstFrame writes /tmp/jukahub-first-frame: the uptime at the
// first frame, in /proc/uptime's own form ("12.34\n"), replaced atomically so
// a reader never sees half a number.
func startupAnnounceFirstFrame(up float64) {
	if !startupBootClockKnown() {
		return
	}
	if err := startupWriteTmpFile(startupFirstFramePath, strconv.FormatFloat(up, 'f', 2, 64)+"\n"); err != nil {
		logWarn("startup", "first-frame marker not written: %v", err)
	}
}

// startupWriteTmpFile replaces a small file in /tmp atomically. AtomicWrite is
// not used: it keeps a .bak and syncs, which a per-boot tmpfs file does not
// need. JukaHub runs as root on the handheld and /tmp is open to everyone, so
// the new file is created under a fresh random name that must not exist yet
// (os.CreateTemp: O_EXCL, never through a link someone left there), and the
// rename replaces whatever stood at path instead of writing through it.
func startupWriteTmpFile(path, text string) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.WriteString(text)
	if err == nil {
		err = f.Chmod(0o644) // CreateTemp makes it 0600; the launcher only reads it
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// startupBootSummary is the current boot in one line for Settings → JukaHub
// OS, e.g. "Power to Home 2.31 s · bootloader 0.70 s · system 1.00 s ·
// JukaHub 0.61 s" where U-Boot left its own timings in the device tree (the
// JukaHub OS cards). Without them only the time since the kernel started is
// known — the stock bootloader's seconds before it are not — so the line then
// says "Kernel to Home" instead of claiming the whole power-on. It is empty
// where it is unknown: not Linux, no first frame recorded this boot, or
// JukaHub was first started well after the boot (opened from another menu).
func startupBootSummary() string {
	if !startupBootClockKnown() {
		return ""
	}
	t, ok := startupBootTimingNow()
	if !ok || t.Start > startupBootTimingStart {
		return ""
	}
	own := t.FirstFrame - t.Start
	if loader := startupBootloaderSeconds(); loader > 0 {
		return fmt.Sprintf("Power to Home %.2f s · bootloader %.2f s · system %.2f s · JukaHub %.2f s",
			loader+t.FirstFrame, loader, t.Start, own)
	}
	return fmt.Sprintf("Kernel to Home %.2f s · system %.2f s · JukaHub %.2f s", t.FirstFrame, t.Start, own)
}

// startupBootTimingNow is this boot's record: the file when it belongs to this
// boot, else this process's own numbers once its first frame is up.
func startupBootTimingNow() (startupBootTiming, bool) {
	id := strings.TrimSpace(bootOSBootIDFn())
	if data, ok := startupReadTmpFile(startupBootTimingPath); ok && id != "" {
		if t, ok := parseStartupBootTiming(data); ok && t.BootID == id {
			return t, true
		}
	}
	start, own, ok := startupBootShares()
	if !ok {
		return startupBootTiming{}, false
	}
	return startupBootTiming{BootID: id, Start: start, FirstFrame: start + own}, true
}

// ── The collector during start-up ─────────────────────────────────────────
//
// Start-up allocates a lot that lives (the config, the scenes, fonts, label
// caches) and little that dies, so collections before the first frame mostly
// re-mark the same live heap on the core the boot is waiting for. The collector
// is off from the start of the app's own start-up (main.go, after the commands
// that run as their own short or background processes: the VPN daemon, `apt`)
// until the first frame, under a soft memory cap so a runaway start still
// collects, and the device's own settings (perf.go applyGCForDevice) are what
// comes back. GOGC/GOMEMLIMIT in the environment leave it alone, as
// applyGCForDevice does.

const (
	// startupGCHoldCap is the heap at which the collector runs anyway.
	startupGCHoldCap = 256 << 20
	// startupGCHoldMax gives the collector back even if no frame is ever
	// presented (a power-on that resumes a game at once, a wedged start).
	startupGCHoldMax = 8 * time.Second
)

var startupGC struct {
	mu      sync.Mutex
	held    bool
	done    bool
	percent int
	limit   int64
}

// startupGCHold switches the collector off for the start-up. main.go, once.
func startupGCHold() {
	if testing.Testing() || os.Getenv("GOGC") != "" || os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	startupGC.mu.Lock()
	defer startupGC.mu.Unlock()
	if startupGC.held || startupGC.done {
		return
	}
	startupGC.held = true
	startupGCTakeLocked()
	time.AfterFunc(startupGCHoldMax, startupGCRelease)
}

// startupGCTakeLocked remembers the settings in force as the ones to give back
// and turns the collector off under the cap.
func startupGCTakeLocked() {
	startupGC.percent = debug.SetGCPercent(-1)
	startupGC.limit = debug.SetMemoryLimit(-1) // a negative value only reads it
	debug.SetMemoryLimit(min(startupGC.limit, startupGCHoldCap))
}

// startupGCApply is how applyGCForDevice installs the device's settings.
// During the hold they become what the release restores (only the cap may
// tighten now) and the collector stays off; after it they apply at once. Both
// under the one lock: the 8 s release on its timer goroutine cannot land
// between "set" and "adopt" and put the stock settings back for the session,
// and the collector is never switched on and off again here (SetGCPercent(-1)
// waits for a collection the brief switch-on would have started).
func startupGCApply(percent int, limit int64) {
	startupGC.mu.Lock()
	defer startupGC.mu.Unlock()
	if startupGC.held && !startupGC.done {
		startupGC.percent, startupGC.limit = percent, limit
		debug.SetMemoryLimit(min(limit, startupGCHoldCap))
		return
	}
	debug.SetGCPercent(percent)
	debug.SetMemoryLimit(limit)
}

// startupGCRelease gives the collector back. Once; any goroutine.
func startupGCRelease() {
	startupGC.mu.Lock()
	defer startupGC.mu.Unlock()
	if startupGC.done {
		return
	}
	startupGC.done = true
	if !startupGC.held {
		return
	}
	startupGC.held = false
	debug.SetGCPercent(startupGC.percent)
	debug.SetMemoryLimit(startupGC.limit)
}

// startupLog writes the report once. It is called after the first frame, so the
// numbers are what the user actually waited for.
func startupLog() {
	report := startupReport()
	logInfo("startup", "%s", strings.TrimPrefix(report, "startup: "))
	// Keep the phase split available over SSH on a handheld. It is written
	// after Present on tmpfs, so diagnosing a slow start cannot delay Home.
	if IsTrimuiDevice() {
		go func() { _ = startupWriteTmpFile(startupReportPath, report+"\n") }()
	}
	// The bench (startup-bench.sh, or a device run with JUKAHUB_STARTUP_BENCH=1
	// that reads errors.txt) reads the line from stderr, which only warnings
	// reach on the handheld or under a quiet log level, so in bench mode it is
	// written there directly.
	if startupBenchEnabled() {
		fmt.Fprintln(os.Stderr, report)
	}
}

// startupBudgetNote explains a skipped splash in the log, which is the only
// evidence a user can send from the device.
func startupBudgetNote(elapsed time.Duration) string {
	target := startupTarget()
	if elapsed >= target {
		return "over budget before the splash; skipped"
	}
	if splashAllotment(elapsed, target, splashReserve(elapsed)) == 0 {
		return "not enough budget left for the splash; skipped"
	}
	return ""
}

// ── Warm starts ───────────────────────────────────────────────────────────
//
// A splash is a greeting for a fresh launch. Relaunching the app within a few
// minutes — after changing a setting, or when an update restarts it — has the
// font files and the card's directory entries still cached by the OS, so the
// same animation costs most of the remaining budget and teaches nothing.
//
// "Warm" is a proxy, not a promise: the only portable signal is when the last
// successful first frame happened. A restart within the window skips the
// splash; anything older gets the normal cold boot. On the device this is the
// difference between ~1.2 s and ~0.4 s for the common relaunch.

const (
	startupWarmWindow   = 10 * time.Minute
	startupStateVersion = 1
	startupStateEnv     = "JUKAHUB_STARTUP_STATE"
)

// startupState is the tiny marker written after a successful first frame.
// BootID is the kernel's id of the boot that frame was drawn in (empty where
// there is none): it tells a restart within one boot from the first start of
// the next even when the clock moved in between (bootos.go).
type startupState struct {
	Version    int       `json:"version"`
	LastLaunch time.Time `json:"lastLaunch"`
	TotalMS    int64     `json:"totalMs"`
	TargetMS   int64     `json:"targetMs"`
	BootID     string    `json:"bootId,omitempty"`
}

// Overridden by tests and by JUKAHUB_STARTUP_STATE, which lets the bench run
// with an isolated marker instead of the user's real one.
var startupStatePathOverride string

func startupStatePath() string {
	if raw := strings.TrimSpace(os.Getenv(startupStateEnv)); raw != "" {
		return raw
	}
	if startupStatePathOverride != "" {
		return startupStatePathOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "startup-state.json")
	}
	return ""
}

func loadStartupState(path string) (startupState, bool) {
	if strings.TrimSpace(path) == "" {
		return startupState{}, false
	}
	var state startupState
	// A damaged file comes back from the .bak the atomic save leaves beside it;
	// a file from another version is still refused, so the fallback cannot
	// resurrect a layout this build cannot read.
	if _, err := readJSONWithBackup(path, &state); err != nil || state.Version != startupStateVersion {
		return startupState{}, false
	}
	return state, true
}

func saveStartupState(path string, state startupState) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	// The launch timings are what make a warm start warm; losing them is not
	// fatal, but the write still goes through the same crash-safe path as the
	// stores a user cannot regenerate.
	return AtomicWrite(path, payload, 0o644)
}

// warmStartWithin reports whether lastLaunch is recent enough to call this
// launch warm. Future timestamps are rejected: a clock set back must not keep
// the splash away forever.
func warmStartWithin(lastLaunch, now time.Time) bool {
	if lastLaunch.IsZero() {
		return false
	}
	age := now.Sub(lastLaunch)
	return age >= 0 && age <= startupWarmWindow
}

// startupWarmStart reports whether the splash should be skipped. The bench
// mode forces the cold path so CI measures the same boot a first-time user
// gets; otherwise every run after the first would look artificially fast.
func startupWarmStart(now time.Time) bool {
	// The bench measures the cold path by default. JUKAHUB_BENCH_WARM=1 lets a
	// device run exercise the warm path with the same exit-after-first-frame
	// harness instead of needing a second script.
	if startupBenchEnabled() && !startupBenchWarm() {
		return false
	}
	state, ok := loadStartupState(startupStatePath())
	// A reboot empties the caches a warm start counts on, however recent the
	// last launch was (a quick power cycle is well inside the window), and the
	// launch after one is the device's boot (bootos.go launchPredatesBoot).
	return ok && warmStartWithin(state.LastLaunch, now) && !launchPredatesBoot(state.LastLaunch, state.BootID, now)
}

func startupBenchWarm() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("JUKAHUB_BENCH_WARM")))
	return v == "1" || v == "true" || v == "yes"
}

// startupRecordFirstFrame writes the marker used by the next launch. It is
// best-effort: a read-only card must never turn a completed boot into a crash.
func startupRecordFirstFrame(total time.Duration) {
	if startupBenchEnabled() {
		return
	}
	_ = saveStartupState(startupStatePath(), startupState{
		Version:    startupStateVersion,
		LastLaunch: time.Now(),
		TotalMS:    total.Milliseconds(),
		TargetMS:   startupTarget().Milliseconds(),
		BootID:     bootOSBootIDFn(),
	})
}

// ── After the first frame ─────────────────────────────────────────────────
//
// Weather, update checks, tool installation, the companion listener and the
// network monitor all touch the network. None of them are needed to paint the
// home screen, and starting them beside the first frame competes with cold
// font rasterisation for the same core. They are queued here and run once the
// first frame has been presented, so the splash and UI get the CPU first.

type startupTask struct {
	name string
	fn   func()
}

var deferredStartup struct {
	mu    sync.Mutex
	tasks []startupTask
}

func deferStartupTask(name string, fn func()) {
	if fn == nil {
		return
	}
	deferredStartup.mu.Lock()
	deferredStartup.tasks = append(deferredStartup.tasks, startupTask{name: name, fn: fn})
	deferredStartup.mu.Unlock()
}

// deferredStartupFrameBudget is how much of a frame the queued tasks may take
// before the rest wait for the next one.
const deferredStartupFrameBudget = 8 * time.Millisecond

// startupNativeWarmupDelay is how long the queued work waits after the first
// frame on JukaHub OS. Its launcher only starts the radios once that frame is
// up, so the network half of the queue could not get anywhere sooner, and the
// first presses on Home get the CPU instead of the update check, the box-art
// fetch and the plugin scan.
const startupNativeWarmupDelay = 3 * time.Second

// startupDeferredHeld reports whether the queue still waits. A power-on that
// resumes a game waits for the queue (quickresume.go), so it is not held then.
// Main thread.
func startupDeferredHeld() bool {
	return !quickBoot.pending && startupWarmupHeld()
}

// startupWarmupHeld reports whether optional warm-up (the queue above, the
// "Hey Juka" microphone) still waits on JukaHub OS: until the first frame and
// startupNativeWarmupDelay after it. Elsewhere it never waits.
func startupWarmupHeld() bool {
	if !jukaosNativeActive() {
		return false
	}
	since, ok := startupFirstFrameSince()
	if !ok {
		// No frame yet. A start that never presents one (a game took the
		// screen first) is not held for the whole session.
		return startupElapsed() < startupGCHoldMax+startupNativeWarmupDelay
	}
	return since < startupNativeWarmupDelay
}

// runDeferredStartupFor runs queued tasks on the main thread, in order, until
// budget is spent (always at least one) and reports whether any are left. The
// loop calls it once per frame after the first one: run back to back, the
// tasks (plugin scan, backups, servers, probes) froze the screen the user had
// just been shown, and the first press after boot waited for all of them. A
// task that takes long enough to be interesting is logged, because "boot felt
// slow" is usually one of these, not the renderer.
func runDeferredStartupFor(budget time.Duration) bool {
	if startupDeferredHeld() {
		return true
	}
	start := time.Now()
	for {
		deferredStartup.mu.Lock()
		if len(deferredStartup.tasks) == 0 {
			deferredStartup.mu.Unlock()
			return false
		}
		task := deferredStartup.tasks[0]
		deferredStartup.tasks = deferredStartup.tasks[1:]
		deferredStartup.mu.Unlock()
		started := time.Now()
		task.fn()
		if took := time.Since(started); took > 20*time.Millisecond {
			logInfo("startup", "%s after first frame took %dms", task.name, took.Milliseconds())
		}
		if time.Since(start) >= budget {
			deferredStartup.mu.Lock()
			more := len(deferredStartup.tasks) > 0
			deferredStartup.mu.Unlock()
			return more
		}
	}
}
