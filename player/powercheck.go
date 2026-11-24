package main

// powercheck.go — the battery life check (Settings → Power & battery, Tools):
// how long the last boot took, and what Home costs while nothing happens,
// with still frames on and with them off (stillframes.go), measured on the
// handheld itself so docs/PERFORMANCE.md can hold real numbers (PERF-1).
//
// A run puts Home on screen and leaves it alone for two phases of
// powerCheckPhase each, after powerCheckSettle to let it calm down: still
// frames forced on, then forced off. During each it reads, every
// powerCheckSample, the battery's draw (power_now, else current × voltage:
// gameLabWattsIn, the Game Lab's reading), and at both ends JukaHub's own CPU
// time and the whole system's (/proc on Linux), and counts the frames
// presented. Any press, a game starting, or the charger (the draw then reads
// as charging) ends or limits the run, and the result says which. The
// newest powerCheckKeep results stay in <data>/power-check.json; each run
// also writes a short Markdown report into "JukaHub reports" whose table rows
// go straight into docs/PERFORMANCE.md.
//
// Nothing here was run on a device from this desk.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	powerCheckSceneName = "PowerCheck"
	powerCheckFileName  = "power-check.json"
	powerCheckSettle    = 6 * time.Second
	powerCheckPhase     = 60 * time.Second
	powerCheckSample    = 2 * time.Second
	powerCheckKeep      = 10
	powerCheckMinWatts  = 3
)

// powerCheckPhaseResult is one phase's numbers. Zero means "not measured"
// for the draw; CPUKnown says whether the CPU figures are real.
type powerCheckPhaseResult struct {
	Still    bool    `json:"still"`
	Seconds  float64 `json:"seconds"`
	Watts    float64 `json:"watts,omitempty"`
	Samples  int     `json:"samples,omitempty"`
	OwnCPU   float64 `json:"ownCpu,omitempty"`
	AllCPU   float64 `json:"allCpu,omitempty"`
	CPUKnown bool    `json:"cpuKnown,omitempty"`
	FPS      float64 `json:"fps"`
}

// powerCheckResult is one run.
type powerCheckResult struct {
	ID      string                  `json:"id"`
	Device  string                  `json:"device"`
	Version string                  `json:"version"`
	Date    string                  `json:"date,omitempty"`
	Boot    string                  `json:"boot,omitempty"`
	Phases  []powerCheckPhaseResult `json:"phases"`
	Note    string                  `json:"note,omitempty"`
	Report  string                  `json:"report,omitempty"`
}

// powerCPUStat is a reading of the CPU counters: JukaHub's own ticks, and
// the whole system's busy and total ticks.
type powerCPUStat struct {
	own   uint64
	busy  uint64
	total uint64
	at    time.Time
}

// The run's states.
const (
	powerCheckIdle = iota
	powerCheckSettling
	powerCheckMeasuring
)

// powerCheck is the run and the results. Main thread, except mu for the
// history the screen and the file share.
var powerCheck struct {
	state      int
	phase      int
	settleAt   time.Time
	until      time.Time
	nextSample time.Time
	started    time.Time
	watts      []float64
	cpu0       powerCPUStat
	cpuOK      bool
	frames0    uint64
	results    []powerCheckPhaseResult
	note       string
	battery    string

	mu      sync.Mutex
	loaded  bool
	history []powerCheckResult
}

// powerCheckRunning reports a run under way.
func powerCheckRunning() bool { return powerCheck.state != powerCheckIdle }

// powerCheckPath is the history file.
func powerCheckPath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, powerCheckFileName)
}

// powerCheckLoad reads the history once. Any goroutine.
func powerCheckLoad() []powerCheckResult {
	powerCheck.mu.Lock()
	defer powerCheck.mu.Unlock()
	if !powerCheck.loaded {
		powerCheck.loaded = true
		if p := powerCheckPath(); p != "" {
			var list []powerCheckResult
			if _, err := readJSONWithBackup(p, &list); err == nil {
				if len(list) > powerCheckKeep {
					list = list[:powerCheckKeep]
				}
				powerCheck.history = list
			}
		}
	}
	return append([]powerCheckResult(nil), powerCheck.history...)
}

// powerCheckStore puts a run first in the history and writes the file. Off
// the main thread.
func powerCheckStore(r powerCheckResult) {
	powerCheckLoad()
	powerCheck.mu.Lock()
	list := append([]powerCheckResult{r}, powerCheck.history...)
	if len(list) > powerCheckKeep {
		list = list[:powerCheckKeep]
	}
	powerCheck.history = list
	data, err := json.MarshalIndent(list, "", "  ")
	powerCheck.mu.Unlock()
	if p := powerCheckPath(); p != "" && err == nil {
		if err := AtomicWrite(p, append(data, '\n'), 0o644); err != nil {
			logWarn("power", "battery life check: could not save the result: %v", err)
		}
	}
}

// ── The run ──────────────────────────────────────────────────────────────

// powerCheckStart begins a run: Home on screen, still frames forced on.
// Main thread.
func powerCheckStart(config *Config) {
	if powerCheckRunning() || config == nil {
		return
	}
	home := homeSceneIndex(config)
	if home < 0 {
		ShowToast("This setup has no Home screen to measure", ToastKindWarning)
		return
	}
	powerCheck.battery = batterySysfsDir()
	powerCheck.note = ""
	switch {
	case powerCheck.battery == "":
		powerCheck.note = "No battery to read here: the draw is not measured, only the CPU and the frames"
	case batteryChargingIn(powerSupplyRoot):
		powerCheck.note = "On the charger: the draw is not measured — unplug it and run the check again for watts"
	}
	powerCheck.results = nil
	powerCheck.phase = 0
	powerCheckBeginSettle(time.Now(), 1)
	if currentSceneIndex != home {
		changeSceneTo(config, home)
	}
	logInfo("power", "battery life check: started")
}

// powerCheckBeginSettle waits for Home to calm down with still frames forced
// to force (1 on, -1 off).
func powerCheckBeginSettle(now time.Time, force int32) {
	stillForce.Store(force)
	powerCheck.state = powerCheckSettling
	powerCheck.settleAt, powerCheck.until = now, now.Add(powerCheckSettle)
}

// powerCheckTick moves the run on; the still-frames gate calls it before
// every frame and at least once a second while Home is quiet. Main thread.
func powerCheckTick(config *Config, now time.Time) {
	if powerCheck.state == powerCheckIdle {
		return
	}
	switch {
	case saverPressQueued():
		powerCheckEnd(config, "A button was pressed, so the check stopped early")
		return
	case gameScreenSuspended():
		powerCheckEnd(config, "A game started, so the check stopped early")
		return
	}
	onHome := config != nil && currentSceneIndex == homeSceneIndex(config) && transitionPhase == "none"
	switch powerCheck.state {
	case powerCheckSettling:
		if !onHome {
			// Still on its way to Home: the settling starts once it is there.
			if now.Sub(powerCheck.settleAt) > 30*time.Second {
				powerCheckEnd(config, "Home did not come up, so the check stopped")
				return
			}
			powerCheck.until = now.Add(powerCheckSettle)
			return
		}
		if now.Before(powerCheck.until) {
			return
		}
		powerCheck.state = powerCheckMeasuring
		powerCheck.started, powerCheck.until, powerCheck.nextSample = now, now.Add(powerCheckPhase), now
		powerCheck.watts = powerCheck.watts[:0]
		powerCheck.cpu0, powerCheck.cpuOK = powerCheckCPUNow()
		powerCheck.frames0 = stillPresented.Load()
	case powerCheckMeasuring:
		if !onHome {
			powerCheckEnd(config, "Home was left, so the check stopped early")
			return
		}
		if !now.Before(powerCheck.nextSample) {
			powerCheck.nextSample = now.Add(powerCheckSample)
			if powerCheck.battery != "" && !batteryChargingIn(powerSupplyRoot) {
				if w, ok := gameLabWattsIn(powerCheck.battery); ok {
					powerCheck.watts = append(powerCheck.watts, w)
				}
			}
		}
		if now.Before(powerCheck.until) {
			return
		}
		powerCheck.results = append(powerCheck.results, powerCheckPhaseOf(now))
		powerCheck.phase++
		if powerCheck.phase < 2 {
			powerCheckBeginSettle(now, -1)
			return
		}
		powerCheckEnd(config, "")
	}
}

// powerCheckPhaseOf is the phase just measured.
func powerCheckPhaseOf(now time.Time) powerCheckPhaseResult {
	secs := now.Sub(powerCheck.started).Seconds()
	r := powerCheckPhaseResult{Still: stillForce.Load() == 1, Seconds: math.Round(secs*10) / 10}
	if secs > 0 {
		r.FPS = math.Round(float64(stillPresented.Load()-powerCheck.frames0)/secs*10) / 10
	}
	if len(powerCheck.watts) >= powerCheckMinWatts {
		sorted := append([]float64(nil), powerCheck.watts...)
		sort.Float64s(sorted)
		r.Watts = math.Round(sorted[len(sorted)/2]*1000) / 1000
		r.Samples = len(sorted)
	}
	if cpu1, ok := powerCheckCPUNow(); ok && powerCheck.cpuOK {
		r.OwnCPU, r.AllCPU = powerCheckCPUShares(powerCheck.cpu0, cpu1)
		r.CPUKnown = true
	}
	return r
}

// powerCheckCPUShares turns two readings into JukaHub's CPU (percent of one
// core) and the whole system's (percent of all cores). Pure.
func powerCheckCPUShares(a, b powerCPUStat) (own, all float64) {
	wall := b.at.Sub(a.at).Seconds()
	if wall > 0 && b.own >= a.own {
		own = float64(b.own-a.own) / powerCheckTicksPerSecond / wall * 100
	}
	if b.total > a.total && b.busy >= a.busy {
		all = float64(b.busy-a.busy) / float64(b.total-a.total) * 100
	}
	return math.Round(own*10) / 10, math.Round(all*10) / 10
}

// powerCheckEnd finishes a run (why says why it ended early, "" when it ran
// to the end): still frames follow the setting again, the result is kept
// and reported, and the check's screen comes back. Main thread.
func powerCheckEnd(config *Config, why string) {
	stillForce.Store(0)
	powerCheck.state = powerCheckIdle
	r := powerCheckResult{
		ID:      time.Now().Format("20060102-150405"),
		Device:  pkgDeviceName(),
		Version: versionString(config),
		Date:    vfDateNow(),
		Boot:    startupBootSummary(),
		Phases:  append([]powerCheckPhaseResult(nil), powerCheck.results...),
		Note:    strings.TrimSpace(strings.Trim(powerCheck.note+". "+why, ". ")),
	}
	if why != "" && len(r.Phases) == 0 {
		logInfo("power", "battery life check: stopped (%s)", why)
		ShowToast(why, ToastKindInfo)
		tbxOpenScene(config, powerCheckSceneName)
		return
	}
	text := powerCheckReport(r)
	dir := osAppsUserDir(devCheckReportDir)
	name := "battery-life-" + powerCheckSafeName(r.Device) + "-" + r.ID + ".md"
	r.Report = devCheckReportDir + "/" + name
	logInfo("power", "battery life check: %s", powerCheckSummary(r))
	go func() {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
				logWarn("power", "battery life check: could not write the report: %v", err)
			}
		}
		powerCheckStore(r)
		tbxPost(func() { powerCheckScreen.gen++ })
	}()
	tbxOpenScene(config, powerCheckSceneName)
	PlaySuccessSound()
	msg := "Battery life check done"
	if why != "" {
		msg = why
	}
	ShowToast(msg+" · report in "+devCheckReportDir, ToastKindSuccess)
}

// powerCheckSafeName is a device name fit for a file name. Pure.
func powerCheckSafeName(s string) string {
	slug := saveRootSlug(s)
	if slug == "root" {
		return "device"
	}
	return slug
}

// ── Words ────────────────────────────────────────────────────────────────

// powerCheckPhaseWords is one phase on one line.
func powerCheckPhaseWords(p powerCheckPhaseResult) string {
	parts := []string{}
	if p.Watts > 0 {
		parts = append(parts, fmt.Sprintf("%.2f W", p.Watts))
	}
	if p.CPUKnown {
		parts = append(parts, fmt.Sprintf("JukaHub %.1f%% CPU", p.OwnCPU))
	}
	parts = append(parts, fmt.Sprintf("%.1f frames/s", p.FPS))
	return strings.Join(parts, " · ")
}

// powerCheckSummary is a run in one line: still frames' saving first.
func powerCheckSummary(r powerCheckResult) string {
	var on, off *powerCheckPhaseResult
	for i := range r.Phases {
		if r.Phases[i].Still {
			on = &r.Phases[i]
		} else {
			off = &r.Phases[i]
		}
	}
	switch {
	case on != nil && off != nil && on.Watts > 0 && off.Watts > 0:
		return fmt.Sprintf("idle Home %.2f W with still frames, %.2f W without (%s)", on.Watts, off.Watts, powerCheckPercent(on.Watts, off.Watts))
	case on != nil && off != nil && on.CPUKnown && off.CPUKnown:
		return fmt.Sprintf("idle Home: JukaHub %.1f%% CPU with still frames, %.1f%% without", on.OwnCPU, off.OwnCPU)
	case on != nil && off != nil:
		return fmt.Sprintf("idle Home: %.1f frames/s with still frames, %.1f without", on.FPS, off.FPS)
	case on != nil:
		return "idle Home with still frames: " + powerCheckPhaseWords(*on)
	}
	return "no complete phase"
}

// powerCheckPercent is "-28%" for a drop from b to a. Pure.
func powerCheckPercent(a, b float64) string {
	if b <= 0 {
		return "—"
	}
	return fmt.Sprintf("%+.0f%%", (a-b)/b*100)
}

// powerCheckReport is the Markdown report: the boot, then one row per phase
// in docs/PERFORMANCE.md's table.
func powerCheckReport(r powerCheckResult) string {
	var b strings.Builder
	b.WriteString("# JukaHub battery life check\n\n")
	fmt.Fprintf(&b, "- Device: %s\n- JukaHub: %s\n", r.Device, r.Version)
	if r.Date != "" {
		fmt.Fprintf(&b, "- Date: %s\n", r.Date)
	}
	if r.Boot != "" {
		fmt.Fprintf(&b, "- Last boot: %s\n", r.Boot)
	} else {
		b.WriteString("- Last boot: not measured on this system (JukaHub was not started by this boot, or this is not Linux)\n")
	}
	if r.Note != "" {
		fmt.Fprintf(&b, "- Note: %s\n", r.Note)
	}
	b.WriteString("\nHome left alone; each phase measured for a minute after a few seconds to settle.\n\n")
	b.WriteString("| Device | Build | Home idle | Battery draw | JukaHub CPU (one core) | Whole CPU | Frames/s |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, p := range r.Phases {
		mode := "still frames off"
		if p.Still {
			mode = "still frames on"
		}
		watts, own, all := "not measured", "not measured", "not measured"
		if p.Watts > 0 {
			watts = fmt.Sprintf("%.2f W (%d readings)", p.Watts, p.Samples)
		}
		if p.CPUKnown {
			own, all = fmt.Sprintf("%.1f %%", p.OwnCPU), fmt.Sprintf("%.1f %%", p.AllCPU)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %.1f |\n", r.Device, r.Version, mode, watts, own, all, p.FPS)
	}
	fmt.Fprintf(&b, "\nSummary: %s.\n\nPaste the table rows into docs/PERFORMANCE.md.\n", powerCheckSummary(r))
	return b.String()
}
