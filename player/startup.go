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

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
}{began: time.Now()}

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
	return b.String()
}

// startupLog writes the report once. It is called after the first frame, so the
// numbers are what the user actually waited for.
func startupLog() {
	log.Print(startupReport())
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

// startupMarkBoot logs one line per launch at debug level for the log viewer,
// keeping Info for the summary.
func startupMarkBoot() {
	startupMarkNow("pre-sdl")
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
type startupState struct {
	Version    int       `json:"version"`
	LastLaunch time.Time `json:"lastLaunch"`
	TotalMS    int64     `json:"totalMs"`
	TargetMS   int64     `json:"targetMs"`
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
	raw, err := os.ReadFile(path)
	if err != nil {
		return startupState{}, false
	}
	var state startupState
	if err := json.Unmarshal(raw, &state); err != nil || state.Version != startupStateVersion {
		return startupState{}, false
	}
	return state, true
}

func saveStartupState(path string, state startupState) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	return ok && warmStartWithin(state.LastLaunch, now)
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

// runDeferredStartup drains the queue on the main thread after Present. A task
// that takes long enough to be interesting is logged, because "boot felt slow"
// is usually one of these, not the renderer.
func runDeferredStartup() {
	deferredStartup.mu.Lock()
	tasks := deferredStartup.tasks
	deferredStartup.tasks = nil
	deferredStartup.mu.Unlock()
	for _, task := range tasks {
		started := time.Now()
		task.fn()
		if took := time.Since(started); took > 20*time.Millisecond {
			log.Printf("[startup] %s after first frame took %dms", task.name, took.Milliseconds())
		}
	}
}
