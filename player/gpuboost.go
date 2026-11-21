package main

// gpuboost.go — GPU boost in games.
//
// The TrimUI's GPU clock goes up while a game runs. JukaHub's CPU has its
// own (the launch power profile, Automatic CPU speed in autoperf.go); the GPU
// had Xbox boost alone (xboxboost.go), for Xbox and GameCube/Wii launches.
// Settings → Power & battery → "GPU boost in games" gives the same GPU step to
// other games, for the length of one launch:
//
//   - Auto (the default): the consoles whose emulators lean on the GPU or are
//     short of frames on this chip — N64, PSP, Dreamcast (and NAOMI and
//     Atomiswave, Flycast's too), Nintendo DS, Saturn, PlayStation, GameCube,
//     Wii and PS2 (when Xbox boost does not hold them already) — and ports;
//   - Always: every game and port;
//   - Off.
//
// What it changes: the GPU's devfreq node runs its performance governor, or its
// floor is raised to its ceiling where it has none — Xbox boost's own plan
// (xboxBoostGPUPlan), read fresh at each start, so a node already at
// performance is left alone. It is put back once the game's process group is
// gone, and only where the value is still the one written (saverRestoreWrites:
// a launch script's own change wins). Changes and restores run under Xbox
// boost's lock (xboxBoostOps), so the two never write the node at once, and a
// launch Xbox boost already holds is left to it. A launch that starts while the
// last one's restore has not run yet runs that restore first. A crash marker in
// the temp directory lets the next start put back what a JukaHub that died
// mid-game left (gpuBoostRecover, beside xboxBoostRecover in main).
//
// Library games come through the launch hooks (gamelaunch_hooks.go: Started
// and Exited), ports through startPortScript (ports.go: gpuBoostBeginPort and
// the session's end). CPU governors are never touched here: autoperf.go and
// powerprofile.go own them. On a PC, or a handheld whose GPU has no devfreq
// node, the row is hidden and nothing runs.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// gpuBoostKey is the setting: gpuBoostAuto (default), gpuBoostAlways or
	// gpuBoostOff.
	gpuBoostKey    = "gpuBoost"
	gpuBoostAuto   = "auto"
	gpuBoostAlways = "always"
	gpuBoostOff    = "off"
	// gpuBoostApplyWait bounds how long a restore waits for its start's writes.
	gpuBoostApplyWait = 3 * time.Second
	// gpuBoostExitWait bounds the exit hook's wait for restores under way.
	gpuBoostExitWait = 2 * time.Second
)

// gpuBoostHeavyConsoles are the consoles Auto boosts, by canonicalCompatSystem's
// names; gpuBoostHeavyFolders the folder names (lower case) it does not know.
var (
	gpuBoostHeavyConsoles = map[string]bool{
		"Nintendo 64": true, "PlayStation Portable": true, "Dreamcast": true, "Nintendo DS": true,
		"Saturn": true, "PlayStation": true, "GameCube": true, "Wii": true, "PlayStation 2": true,
	}
	gpuBoostHeavyFolders = map[string]bool{
		"ss": true, "ds": true, "naomi": true, "naomi2": true, "atomiswave": true, "awave": true,
		"ports": true, "port": true, "portmaster": true,
	}
)

// gpuBoostSession is one boosted launch. Every method is a no-op on nil, so a
// launch that is not boosted calls them all the same.
type gpuBoostSession struct {
	target  string
	label   string
	begun   time.Time
	applied chan struct{} // closed when the start's writes are done

	mu       sync.Mutex
	writes   []saverSysfsWrite // what took, in the order it was written
	ended    bool              // the launch is over: no write may follow
	restored bool              // restoreLocked ran
}

var gpuBoost struct {
	mu     sync.Mutex
	active *gpuBoostSession // the newest boosted launch
	status string           // the Settings row's line once a session has run
	hooked bool             // the exit hook is registered (main thread)
}

// gpuBoostRestoring counts restores under way, for the exit hook.
var gpuBoostRestoring sync.WaitGroup

// gpuBoostNode caches whether the device has a GPU devfreq node.
var gpuBoostNode struct {
	once sync.Once
	ok   bool
}

func init() {
	gameLaunchRegisterStarted(gpuBoostGameStarted)
	gameLaunchRegisterExited(gpuBoostGameExited)
}

// ── Which launches ──────────────────────────────────────────────────────────

// gpuBoostSupported: a TrimUI handheld whose GPU has a devfreq node.
func gpuBoostSupported() bool {
	if !xboxBoostSupported() {
		return false
	}
	gpuBoostNode.once.Do(func() { gpuBoostNode.ok = gpuBoostNodePresent() })
	return gpuBoostNode.ok
}

// gpuBoostMode reads the setting; Auto unless set.
func gpuBoostMode(c *Config) string {
	if c == nil {
		return gpuBoostAuto
	}
	raw, _ := customString(c.Variables.Custom, gpuBoostKey)
	switch m := strings.ToLower(strings.TrimSpace(raw)); m {
	case gpuBoostAlways, gpuBoostOff:
		return m
	}
	return gpuBoostAuto
}

// gpuBoostHeavySystem reports whether Auto boosts a console, by the library's
// name for it ("N64", "PS", NextUI's "Nintendo 64 (N64)") or a launcher's
// folder (Emus/PSP). Pure.
func gpuBoostHeavySystem(system string) bool {
	s := strings.TrimSpace(system)
	if s == "" {
		return false
	}
	names := []string{s}
	if i := strings.LastIndex(s, "("); i > 0 && strings.HasSuffix(s, ")") {
		names = append(names, strings.TrimSpace(s[i+1:len(s)-1]), strings.TrimSpace(s[:i]))
	}
	for _, n := range names {
		if gpuBoostHeavyConsoles[canonicalCompatSystem(n)] || gpuBoostHeavyFolders[strings.ToLower(n)] {
			return true
		}
	}
	return false
}

// ── A boosted launch ────────────────────────────────────────────────────────

// gpuBoostBegin boosts a launch that has just started — target is the file
// launched, label names it in the log, heavy whether Auto counts it — and
// returns its session, or nil when nothing is boosted: a PC or no GPU node, the
// setting, or a launch Xbox boost holds (it raises the GPU itself). Main
// thread; the writes run beside it.
func gpuBoostBegin(target, label string, heavy bool) *gpuBoostSession {
	if !gpuBoostSupported() {
		return nil
	}
	switch gpuBoostMode(appConfig) {
	case gpuBoostOff:
		return nil
	case gpuBoostAuto:
		if !heavy {
			return nil
		}
	}
	target = strings.TrimSpace(target)
	if xboxBoostHolds(target) {
		return nil
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	}
	s := &gpuBoostSession{target: target, label: label, begun: time.Now(), applied: make(chan struct{})}
	gpuBoost.mu.Lock()
	prev := gpuBoost.active
	gpuBoost.active = s
	gpuBoost.mu.Unlock()
	prev.finish()
	go s.apply(prev)
	return s
}

// gpuBoostBeginPort is gpuBoostBegin for startPortScript: every port counts as
// heavy; PortMaster's own menu (master) is not a game.
func gpuBoostBeginPort(path, name string, master bool) *gpuBoostSession {
	if master {
		return nil
	}
	return gpuBoostBegin(path, name, true)
}

// gpuBoostGameStarted is the library's Started hook (main thread).
func gpuBoostGameStarted(game libraryGame, launcher string, _ *exec.Cmd) {
	heavy := gpuBoostHeavySystem(game.System)
	if !heavy && launcher != "" {
		heavy = gpuBoostHeavySystem(filepath.Base(filepath.Dir(launcher)))
	}
	gpuBoostBegin(game.Path, game.Title, heavy)
}

// gpuBoostGameExited is the library's Exited hook (the reaper goroutine): the
// game's process group is gone, so its session ends.
func gpuBoostGameExited(game libraryGame, _ string, _ time.Duration, _ error) {
	gpuBoost.mu.Lock()
	s := gpuBoost.active
	gpuBoost.mu.Unlock()
	if s != nil && s.target == strings.TrimSpace(game.Path) {
		s.end()
	}
}

// apply makes the plan's writes, beside the launch. The marker is written
// before the first change, so a crash at any point leaves enough to put the
// values back. prev, the last session, is restored first if it has not been.
func (s *gpuBoostSession) apply(prev *gpuBoostSession) {
	defer close(s.applied)
	xboxBoostOps.Lock()
	defer xboxBoostOps.Unlock()
	// Its values must not be read as the firmware's.
	prev.restoreLocked()
	s.mu.Lock()
	over := s.ended
	s.mu.Unlock()
	if over {
		return
	}
	plan := gpuBoostPlan()
	if len(plan) == 0 {
		logInfo("gpu", "boost: nothing to raise for %s (the GPU already runs at full speed)", s.label)
		return
	}
	gpuBoostWriteMarker(plan)
	var done []saverSysfsWrite
	details := make([]string, 0, len(plan))
	for _, w := range plan {
		if err := os.WriteFile(w.Path, []byte(w.Written+"\n"), 0o644); err != nil {
			details = append(details, fmt.Sprintf("%s left as it is (%v)", w.Path, err))
			continue
		}
		// What the kernel reports back is what the restore compares against.
		if got := saverReadTrimmed(w.Path); got != "" {
			w.Written = got
		}
		done = append(done, w)
		details = append(details, fmt.Sprintf("%s %s → %s", w.Path, w.Original, w.Written))
	}
	s.mu.Lock()
	s.writes = done
	s.mu.Unlock()
	gpuBoostSaveMarker(done)
	logInfo("gpu", "boost on for %s: %s", s.label, strings.Join(details, "; "))
}

// finish marks the session over; false when it already was.
func (s *gpuBoostSession) finish() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return false
	}
	s.ended = true
	return true
}

// end finishes the session once the game is gone and puts the values back
// beside the caller. Safe to call twice.
func (s *gpuBoostSession) end() {
	if !s.finish() {
		return
	}
	gpuBoostRestoring.Add(1)
	go func() {
		defer gpuBoostRestoring.Done()
		s.restore(gpuBoostApplyWait)
	}()
}

// restore waits up to wait for the start's writes, then puts them back.
func (s *gpuBoostSession) restore(wait time.Duration) {
	if s == nil {
		return
	}
	if wait > 0 {
		select {
		case <-s.applied:
		case <-time.After(wait):
		}
	}
	xboxBoostOps.Lock()
	s.restoreLocked()
	xboxBoostOps.Unlock()
}

// restoreLocked is restore's body; it runs once per session, and a session it
// ran for writes nothing after. Under xboxBoostOps.
func (s *gpuBoostSession) restoreLocked() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.restored {
		s.mu.Unlock()
		return
	}
	s.restored, s.ended = true, true
	writes := s.writes
	s.writes = nil
	s.mu.Unlock()
	if len(writes) > 0 {
		n := saverRestoreWrites(writes)
		gpuBoostRemoveMarker()
		logInfo("gpu", "boost off after %s of %s: %d of %d values put back",
			time.Since(s.begun).Round(time.Second), s.label, n, len(writes))
	}
	gpuBoost.mu.Lock()
	if gpuBoost.active == s {
		gpuBoost.active = nil
	}
	if len(writes) > 0 {
		gpuBoost.status = "Last boosted: " + s.label + " (GPU at full speed)"
	}
	gpuBoost.mu.Unlock()
}

// ── Crash marker ────────────────────────────────────────────────────────────

// gpuBoostMarker is the crash marker: what to put back if JukaHub dies first.
type gpuBoostMarker struct {
	Writes []saverSysfsWrite `json:"writes,omitempty"`
}

func gpuBoostMarkerPath() string {
	return filepath.Join(os.TempDir(), "jukahub-gpu-boost.json")
}

func gpuBoostWriteMarker(writes []saverSysfsWrite) {
	data, err := json.Marshal(gpuBoostMarker{Writes: writes})
	if err != nil {
		return
	}
	if err := os.WriteFile(gpuBoostMarkerPath(), data, 0o600); err != nil {
		logWarn("gpu", "could not record the values to put back: %v", err)
	}
}

// gpuBoostSaveMarker writes the marker, or removes it when nothing is left to
// put back.
func gpuBoostSaveMarker(writes []saverSysfsWrite) {
	if len(writes) == 0 {
		gpuBoostRemoveMarker()
		return
	}
	gpuBoostWriteMarker(writes)
}

func gpuBoostRemoveMarker() {
	if err := os.Remove(gpuBoostMarkerPath()); err != nil && !os.IsNotExist(err) {
		logWarn("gpu", "could not remove %s: %v", gpuBoostMarkerPath(), err)
	}
}

// gpuBoostRecover puts back what a JukaHub that died during a boosted game
// left raised, and registers the exit hook. main calls it at start-up, on the
// main thread, beside xboxBoostRecover.
func gpuBoostRecover() {
	if !gpuBoost.hooked {
		gpuBoost.hooked = true
		Cleanup = append(Cleanup, gpuBoostShutdown)
	}
	data, err := os.ReadFile(gpuBoostMarkerPath())
	if err != nil {
		return
	}
	var m gpuBoostMarker
	if json.Unmarshal(data, &m) == nil {
		var writes []saverSysfsWrite
		for _, w := range m.Writes {
			// Only the GPU nodes the boost writes: the marker sits in a shared
			// temp directory.
			if filepath.Base(w.Path) != "online" && xboxBoostWritable(w.Path) {
				writes = append(writes, w)
			}
		}
		xboxBoostOps.Lock()
		n := saverRestoreWrites(writes)
		xboxBoostOps.Unlock()
		if n > 0 {
			logInfo("gpu", "put back %d GPU value(s) an interrupted GPU boost left raised", n)
		}
	}
	gpuBoostRemoveMarker()
}

// gpuBoostShutdown is the exit hook: a boosted game still running when JukaHub
// quits is put back now, and restores under way get a moment.
func gpuBoostShutdown() {
	gpuBoost.mu.Lock()
	s := gpuBoost.active
	gpuBoost.mu.Unlock()
	done := make(chan struct{})
	go func() {
		if s.finish() {
			s.restore(time.Second)
		}
		gpuBoostRestoring.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(gpuBoostExitWait):
	}
}

// ── Settings ────────────────────────────────────────────────────────────────

func gpuBoostOptions(*Config) []setOption {
	return []setOption{
		{Value: gpuBoostAuto, Label: "Auto", Note: "N64, PSP, Dreamcast, DS, Saturn, PlayStation, GameCube, Wii, PS2 and ports"},
		{Value: gpuBoostAlways, Label: "Always", Note: "Every game and port"},
		{Value: gpuBoostOff, Label: "Off", Note: "The GPU runs as the firmware sets it"},
	}
}

// gpuBoostSettingsRow is Settings → Power & battery → "GPU boost in games",
// shown on a handheld whose GPU has a devfreq node.
func gpuBoostSettingsRow() setRow {
	return setRow{
		ID: gpuBoostKey, Label: "GPU boost in games", Kind: setKindChoice,
		Visible: func(*Config) bool { return gpuBoostSupported() },
		Detail:  gpuBoostSettingDetail,
		Options: gpuBoostOptions,
		Current: func(c *Config) int {
			return ledOptionIndex(gpuBoostOptions(c), gpuBoostMode(c))
		},
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			opts := gpuBoostOptions(c)
			i := ledOptionIndex(opts, value)
			if i < 0 {
				return
			}
			setStoreString(c, gpuBoostKey, opts[i].Value)
			setToast("GPU boost in games: " + opts[i].Label + " · from the next game")
		},
	}
}

// gpuBoostSettingDetail is the row's second line. Cheap: drawn every frame.
func gpuBoostSettingDetail(c *Config) string {
	switch gpuBoostMode(c) {
	case gpuBoostOff:
		return "Games run the GPU at the speed the firmware picks"
	case gpuBoostAlways:
		return "Every game and port runs the GPU at full speed, put back after"
	}
	gpuBoost.mu.Lock()
	st := gpuBoost.status
	gpuBoost.mu.Unlock()
	if st != "" {
		return st
	}
	return "Heavy consoles and ports run the GPU at full speed, put back after"
}
