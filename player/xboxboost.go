package main

// xboxboost.go — Xbox boost: the whole handheld for an Xbox game or an Xbox
// stream.
//
// Juka (2026-11-19): "Improve XBOX performance". The original Xbox runs on the
// Smart Pro through hakuX (hakux.go), an experimental port on a 1 GB, four-core
// A133P with a PowerVR GE8300; Xbox Cloud Gaming and the console at home stream
// through GreenOvercast (greenovercast.go), whose picture is decoded on the CPU.
// Neither has a frame to spare. JukaHub cannot make the emulator or the decoder
// faster; what it can do is hand them the whole machine and take none of it back
// while they run. For the length of one launch:
//
//   - CPU: every core online (a firmware or CFW battery mode may park some),
//     the performance governor at the top clock even with "Full speed while
//     playing" off (a global clock cap and the game's own CPU options still
//     win: powerprofile.go applies them after), and Automatic CPU speed hands
//     off (autoperf.go), so the clock never steps down under a game that is
//     waiting on its GPU;
//   - GPU: a GPU with a devfreq node (a name with "gpu" under
//     /sys/class/devfreq) runs its performance governor, or with its floor
//     raised to its ceiling where it has none;
//   - memory: JukaHub's textures and byte caches are emptied just before the
//     start (main thread, while nothing draws them); beside the launch the Go
//     heap is handed back, the warm speech recognizer, the media library's
//     scan and the video link prefetch stop, and the kernel's page cache is
//     dropped and compacted (memFreeKernel). Every game start already stops
//     the offline AI and the Piper voice (voiceReleaseForGame). On a 1 GB
//     handheld this decides whether the game's working set sits in RAM or in
//     swap;
//   - priority: the game's process group runs at nice xboxBoostNice, ahead of
//     whatever JukaHub or the firmware does in the background;
//   - "Hey Juka" stays closed in an Xbox game (wakeword.go): its recognizer
//     holds 100–150 MB and a loud game keeps it decoding on a core;
//   - streams only: Wi-Fi power saving off, as Moonlight's hand-off does (a
//     radio that dozes between beacons delivers the frames in bursts).
//
// Everything changed is put back once the game's process group is gone, and
// only where the value is still the one written (saverRestoreWrites: newest
// first, compare-and-restore) — a launch script or the user may have changed
// it since, and theirs wins. Changes and restores are serialised
// (xboxBoostOps), and a launch that begins while the last one's restore is
// still waiting runs that restore first, so two sessions never undo each
// other. A crash marker in the temp directory lets the next start put back
// what a JukaHub that died mid-game left (xboxBoostRecover, from main before
// the boot CPU values are recorded).
//
// Settings → Games & emulators → "Xbox boost" (xboxBoost, on by default) turns
// it off. On a PC, and on the handheld for every other game, nothing here runs.
//
// What it does not do: change hakuX's or GreenOvercast's own settings (render
// scale, frame skip and stream resolution are in their own menus), clock
// anything past what it advertises, or close anything the user left running.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// xboxBoostSetting is the Settings switch (on unless set to false).
	xboxBoostSetting = "xboxBoost"
	// xboxBoostNice is the boosted game's process group's priority: ahead of
	// background work, short of starving the firmware's own services.
	xboxBoostNice = -5
	// xboxBoostSpeechAfter is how long after the start the warm recognizer is
	// stopped: the parked loop closes the wake word's microphone first.
	xboxBoostSpeechAfter = time.Second
	// xboxBoostApplyWait bounds how long a restore waits for the start's
	// background half (its Wi-Fi switch) to finish.
	xboxBoostApplyWait = 5 * time.Second
	// xboxBoostExitWait bounds the exit hook's wait for restores under way.
	xboxBoostExitWait = 2 * time.Second
)

// xboxBoostDevfreqRoot is where a GPU's devfreq node is looked for.
var xboxBoostDevfreqRoot = "/sys/class/devfreq"

// xboxBoostKind is what a launch is, for the boost.
type xboxBoostKind int

const (
	xboxBoostNone   xboxBoostKind = iota // not an Xbox launch
	xboxBoostGame                        // hakuX playing a disc
	xboxBoostStream                      // GreenOvercast: Xbox Cloud Gaming or the console at home
)

// xboxBoostSession is one boosted launch. Every method is a no-op on nil, so a
// launch that is not boosted calls them all the same.
type xboxBoostSession struct {
	kind    xboxBoostKind
	target  string
	label   string
	begun   time.Time
	applied chan struct{} // closed when the start's background half is done

	mu       sync.Mutex
	writes   []saverSysfsWrite // cores and GPU, in the order they were made
	wifi     []string          // radios whose power saving went off ("iw:wlan0")
	details  []string          // everything done, for the log
	cores    int               // parked cores brought online
	gpu      bool              // the GPU's governor or floor raised
	reniced  bool              // the game's group runs at xboxBoostNice
	cpuOwn   bool              // the game's own CPU profile or limit is in charge
	ended    bool              // finish ran
	restored bool              // restore ran (or the session is past it)
}

// xboxBoostMarker is the crash marker: what to put back if JukaHub dies first.
type xboxBoostMarker struct {
	Writes []saverSysfsWrite `json:"writes,omitempty"`
	WiFi   []string          `json:"wifi,omitempty"`
}

var xboxBoost struct {
	mu      sync.Mutex
	active  *xboxBoostSession // the launch running now
	pending *xboxBoostSession // an ended launch whose restore has not run yet
	armed   string            // the launch target whose power profile is the boost's
	status  string            // the Settings row's line once a session has run
	hooked  bool              // the exit hook is registered (main thread)
}

// xboxBoostOps serialises every change and restore of the values the boost
// writes, and the crash marker.
var xboxBoostOps sync.Mutex

// xboxBoostLive is true while a boosted launch runs (the wake word reads it
// every parked frame).
var xboxBoostLive atomic.Bool

// xboxBoostRestoring counts restores under way, for the exit hook.
var xboxBoostRestoring sync.WaitGroup

// ── Which launches ──────────────────────────────────────────────────────────

// xboxBoostEnabled reads the switch; on by default.
func xboxBoostEnabled(c *Config) bool {
	if c == nil {
		return true
	}
	return customBool(c.Variables.Custom, xboxBoostSetting, true)
}

// xboxBoostSupported: the handheld only. A PC's clocks, priorities and memory
// are its OS's business.
func xboxBoostSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice()
}

// xboxBoostKindForGame is a library game's kind: the Xbox row's games are
// hakuX's (the handheld's only Xbox emulator). GameCube and Wii games, on a
// handheld that plays them (dolphin.go), and PS2 games (ps2.go) need the
// whole machine as much.
func xboxBoostKindForGame(game libraryGame) xboxBoostKind {
	if strings.EqualFold(strings.TrimSpace(game.System), emuXboxEntryName) {
		return xboxBoostGame
	}
	if dolphinPlaysSystemHere(game.System) || ps2PlaysSystemHere(game.System) {
		return xboxBoostGame
	}
	return xboxBoostNone
}

// xboxBoostKindForPort is a port launch's kind: GreenOvercast, by its script's
// name or its port.json folder — the X1 pill, Cloud Gaming and the library's
// Ports row all start it through startPortScript.
func xboxBoostKindForPort(script string) xboxBoostKind {
	if !xboxBoostSupported() {
		return xboxBoostNone
	}
	if x1PortName(filepath.Base(script)) {
		return xboxBoostStream
	}
	if dir := portMetaFor(script).Dir; dir != "" && x1PortName(filepath.Base(dir)) {
		return xboxBoostStream
	}
	return xboxBoostNone
}

// xboxBoostHolds reports whether target's launch is the boost's: its power
// profile goes to full speed (beginLaunchPowerProfile) and Automatic CPU speed
// leaves it alone (autoPerfLaunchBegin). Any goroutine.
func xboxBoostHolds(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	xboxBoost.mu.Lock()
	defer xboxBoost.mu.Unlock()
	return xboxBoost.armed == target
}

// xboxBoostPowerConfig is the launch profile a boosted launch runs with: on,
// and the performance governor rather than a dynamic one. A clock cap the user
// set (powerProfileMaxFreq) stays — it may be there for a reason the boost
// cannot see — and the game's own options are applied after this.
func xboxBoostPowerConfig(cfg powerProfileConfig) powerProfileConfig {
	cfg.Enabled = true
	cfg.Balanced = false
	return cfg
}

// ── A boosted launch ────────────────────────────────────────────────────────

// xboxBoostBegin starts boosting a launch of kind (target is the file
// launched, label names it in the log) and returns its session, or nil when
// nothing is boosted: not an Xbox launch, a PC, the switch off, or a boosted
// launch already running. Main thread, just before beginLaunchPowerProfile:
// the cores come online before the launch profile sets their governor, and
// JukaHub's textures can only be freed here.
func xboxBoostBegin(kind xboxBoostKind, target, label string) *xboxBoostSession {
	if kind == xboxBoostNone || !xboxBoostSupported() || !xboxBoostEnabled(appConfig) {
		return nil
	}
	target = strings.TrimSpace(target)
	label = strings.TrimSpace(label)
	if label == "" {
		label = strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	}
	s := &xboxBoostSession{kind: kind, target: target, label: label, begun: time.Now(), applied: make(chan struct{})}
	// The game's own CPU options win over the boost's (gameLaunchPowerConfig
	// runs after xboxBoostPowerConfig); the summary says so.
	opts := gameOptionsFor(target)
	if p := normaliseGameCPUProfile(opts.CPUProfile); (p != "" && p != "performance") || opts.MaxFreqKHz > 0 {
		s.cpuOwn = true
	}
	xboxBoost.mu.Lock()
	if xboxBoost.active != nil {
		other := xboxBoost.active.label
		xboxBoost.mu.Unlock()
		logInfo("xbox", "boost: %s is still boosted, so %s starts without it", other, label)
		return nil
	}
	xboxBoost.active = s
	xboxBoost.armed = target
	prev := xboxBoost.pending
	xboxBoost.pending = nil
	xboxBoost.mu.Unlock()
	xboxBoostLive.Store(true)

	before := readProcMeminfo().MemAvail / 1024
	xboxBoostOps.Lock()
	// The last launch's restore, if it is still waiting for its background
	// half: now, so it cannot park a core this launch counts on.
	prev.restoreLocked()
	s.applyLocked(xboxBoostHardwarePlan())
	xboxBoostOps.Unlock()
	// Nothing draws until the game is gone: every texture and byte cache goes
	// now and is made again on the way back.
	evictAllCachesForPressure()
	s.detail("JukaHub's caches emptied")
	go s.background(before)
	return s
}

// applyLocked makes the planned writes, recording each one that took. The
// marker is written before the first change, so a crash at any point leaves
// enough to put the values back. Under xboxBoostOps.
func (s *xboxBoostSession) applyLocked(plan []saverSysfsWrite) {
	if s == nil || len(plan) == 0 {
		return
	}
	xboxBoostWriteMarker(xboxBoostMarker{Writes: plan})
	s.mu.Lock()
	for _, w := range plan {
		if err := os.WriteFile(w.Path, []byte(w.Written+"\n"), 0o644); err != nil {
			s.details = append(s.details, fmt.Sprintf("%s left as it is (%v)", w.Path, err))
			continue
		}
		// What the kernel reports back is what the restore compares against.
		if got := saverReadTrimmed(w.Path); got != "" {
			w.Written = got
		}
		s.writes = append(s.writes, w)
		s.details = append(s.details, fmt.Sprintf("%s %s → %s", w.Path, w.Original, w.Written))
		if filepath.Base(w.Path) == "online" {
			s.cores++
		} else {
			s.gpu = true
		}
	}
	marker := s.markerLocked()
	s.mu.Unlock()
	xboxBoostSaveMarker(marker)
}

// background is the start's slow half, beside the launch: the memory JukaHub
// can give back and, for a stream, the Wi-Fi switch. before is MemAvailable
// (MB) before the boost began.
func (s *xboxBoostSession) background(before uint64) {
	defer close(s.applied)
	debug.FreeOSMemory()
	prefetchMu.Lock()
	windows := len(prefetchWindows)
	prefetchMu.Unlock()
	if windows > 0 {
		cancelPrefetchWork()
		s.detail("video link prefetch stopped")
	}
	if mlibScanning.Load() {
		mlibCancelScan()
		s.detail("media library scan stopped")
	}
	if s.kind == xboxBoostStream {
		s.wifiOff()
	}
	// The wake word's microphone closes on the parked loop's next tick; its
	// recognizer can go once that has happened.
	if wait := xboxBoostSpeechAfter - time.Since(s.begun); wait > 0 {
		time.Sleep(wait)
	}
	if sttServeStopIdle("a boosted game needs the memory", true) {
		s.detail("speech recognizer stopped")
	}
	if memFreeKernel() {
		s.detail("page cache dropped")
	}
	// The game is loading beside all this, so the two figures are what was
	// there, not what was freed.
	after := readProcMeminfo().MemAvail / 1024
	s.mu.Lock()
	line := strings.Join(s.details, "; ")
	s.mu.Unlock()
	logInfo("xbox", "boost on for %s: %s; %d MB available (%d MB before the boost)", s.label, line, after, before)
}

// wifiOff switches the radios' power saving off for a stream, unless the
// stream already ended, in which case they go straight back on.
func (s *xboxBoostSession) wifiOff() {
	xboxBoostOps.Lock()
	defer xboxBoostOps.Unlock()
	s.mu.Lock()
	over := s.restored
	s.mu.Unlock()
	if over {
		return
	}
	radios := xboxBoostWiFiOff()
	if len(radios) == 0 {
		return
	}
	s.mu.Lock()
	s.wifi = append(s.wifi, radios...)
	s.details = append(s.details, "Wi-Fi power saving off ("+strings.Join(radios, ", ")+")")
	marker := s.markerLocked()
	s.mu.Unlock()
	xboxBoostSaveMarker(marker)
}

// started is called once the game's process has started: its process group
// goes ahead of the background.
func (s *xboxBoostSession) started(pid int) {
	if s == nil || pid <= 0 {
		return
	}
	if err := xboxBoostRenice(pid); err != nil {
		s.detail(fmt.Sprintf("priority unchanged (%v)", err))
		return
	}
	s.mu.Lock()
	s.reniced = true
	s.details = append(s.details, fmt.Sprintf("game at priority %d", xboxBoostNice))
	s.mu.Unlock()
}

// detail records one thing the boost did, for the log. Any goroutine.
func (s *xboxBoostSession) detail(line string) {
	if s == nil || line == "" {
		return
	}
	s.mu.Lock()
	s.details = append(s.details, line)
	s.mu.Unlock()
}

// markerLocked is the crash marker for what the session holds now. Under s.mu.
func (s *xboxBoostSession) markerLocked() xboxBoostMarker {
	return xboxBoostMarker{
		Writes: append([]saverSysfsWrite(nil), s.writes...),
		WiFi:   append([]string(nil), s.wifi...),
	}
}

// summaryLocked is the Settings row's account of the session: "full CPU
// speed · GPU at full speed · JukaHub's memory freed · first on the CPU".
// Under s.mu.
func (s *xboxBoostSession) summaryLocked() string {
	parts := []string{"full CPU speed"}
	if s.cpuOwn {
		parts[0] = "the game's own CPU setting"
	}
	switch {
	case s.cores == 1:
		parts = append(parts, "1 parked core woken")
	case s.cores > 1:
		parts = append(parts, fmt.Sprintf("%d parked cores woken", s.cores))
	}
	if s.gpu {
		parts = append(parts, "GPU at full speed")
	}
	parts = append(parts, "JukaHub's memory freed")
	if s.reniced {
		parts = append(parts, "first on the CPU")
	}
	if len(s.wifi) > 0 {
		parts = append(parts, "Wi-Fi power saving off")
	}
	return strings.Join(parts, " · ")
}

// end finishes the session once the game is gone (or never started): the
// launch is no longer the boost's and the restore runs beside the caller,
// which may be the main thread. Safe to call twice.
func (s *xboxBoostSession) end() {
	if !s.finish() {
		return
	}
	xboxBoostRestoring.Add(1)
	go func() {
		defer xboxBoostRestoring.Done()
		s.restore(xboxBoostApplyWait)
	}()
}

// finish marks the session over, disarms it and leaves it pending for its
// restore; false when it already was over.
func (s *xboxBoostSession) finish() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return false
	}
	s.ended = true
	s.mu.Unlock()
	xboxBoost.mu.Lock()
	if xboxBoost.active == s {
		xboxBoost.active = nil
		xboxBoost.armed = ""
		xboxBoostLive.Store(false)
	}
	xboxBoost.pending = s
	xboxBoost.mu.Unlock()
	return true
}

// restore puts back what the session changed, after waiting up to wait for
// the start's background half (a stream's radios may still be switching).
func (s *xboxBoostSession) restore(wait time.Duration) {
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

// restoreLocked is restore's body; it runs once per session. Under
// xboxBoostOps.
func (s *xboxBoostSession) restoreLocked() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.restored {
		s.mu.Unlock()
		return
	}
	s.restored = true
	summary := s.summaryLocked()
	writes, wifi := s.writes, s.wifi
	s.writes, s.wifi = nil, nil
	s.mu.Unlock()
	n := saverRestoreWrites(writes)
	xboxBoostWiFiOn(wifi)
	xboxBoostRemoveMarker()
	radios := ""
	if len(wifi) > 0 {
		radios = ", Wi-Fi power saving back on"
	}
	logInfo("xbox", "boost off after %s of %s: %d of %d values put back%s",
		time.Since(s.begun).Round(time.Second), s.label, n, len(writes), radios)
	xboxBoost.mu.Lock()
	if xboxBoost.pending == s {
		xboxBoost.pending = nil
	}
	xboxBoost.status = "Last Xbox session: " + summary
	xboxBoost.mu.Unlock()
}

// ── Crash marker ────────────────────────────────────────────────────────────

func xboxBoostMarkerPath() string {
	return filepath.Join(os.TempDir(), "jukahub-xbox-boost.json")
}

func xboxBoostWriteMarker(m xboxBoostMarker) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	if err := os.WriteFile(xboxBoostMarkerPath(), data, 0o600); err != nil {
		logWarn("xbox", "could not record the values to put back: %v", err)
	}
}

// xboxBoostSaveMarker writes the marker, or removes it when nothing is left
// to put back.
func xboxBoostSaveMarker(m xboxBoostMarker) {
	if len(m.Writes) == 0 && len(m.WiFi) == 0 {
		xboxBoostRemoveMarker()
		return
	}
	xboxBoostWriteMarker(m)
}

func xboxBoostRemoveMarker() {
	if err := os.Remove(xboxBoostMarkerPath()); err != nil && !os.IsNotExist(err) {
		logWarn("xbox", "could not remove %s: %v", xboxBoostMarkerPath(), err)
	}
}

// xboxBoostRecover puts back what a JukaHub that died during a boosted game
// left changed, and registers the exit hook. main calls it at start-up, on the
// main thread, before captureBootCPUSettings.
func xboxBoostRecover() {
	if !xboxBoost.hooked {
		xboxBoost.hooked = true
		Cleanup = append(Cleanup, xboxBoostShutdown)
	}
	data, err := os.ReadFile(xboxBoostMarkerPath())
	if err != nil {
		return
	}
	var m xboxBoostMarker
	if json.Unmarshal(data, &m) == nil {
		var writes []saverSysfsWrite
		for _, w := range m.Writes {
			// Only the files the boost writes: the marker sits in a shared
			// temp directory.
			if xboxBoostWritable(w.Path) {
				writes = append(writes, w)
			}
		}
		n := saverRestoreWrites(writes)
		xboxBoostWiFiOn(m.WiFi)
		if n > 0 || len(m.WiFi) > 0 {
			logInfo("xbox", "put back %d value(s) and %d radio(s) an interrupted Xbox boost left changed", n, len(m.WiFi))
		}
	}
	xboxBoostRemoveMarker()
}

// xboxBoostWritable reports whether p is a file the boost writes: a CPU's
// online switch or a devfreq GPU's governor or floor.
func xboxBoostWritable(p string) bool {
	if p == "" || p != filepath.Clean(p) {
		return false
	}
	dir, base := filepath.Dir(p), filepath.Base(p)
	switch base {
	case "online":
		name := filepath.Base(dir)
		return filepath.Dir(dir) == filepath.Clean(cpufreqRoot) && len(name) > 3 &&
			strings.HasPrefix(name, "cpu") && strings.Trim(name[3:], "0123456789") == ""
	case "governor", "min_freq":
		return filepath.Dir(dir) == filepath.Clean(xboxBoostDevfreqRoot) &&
			strings.Contains(strings.ToLower(filepath.Base(dir)), "gpu")
	}
	return false
}

// xboxBoostShutdown is the exit hook: a boosted game still running when
// JukaHub quits is put back now, and restores under way get a moment.
func xboxBoostShutdown() {
	xboxBoost.mu.Lock()
	s := xboxBoost.active
	xboxBoost.mu.Unlock()
	if s.finish() {
		s.restore(0)
	}
	done := make(chan struct{})
	go func() {
		xboxBoostRestoring.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(xboxBoostExitWait):
	}
}

// ── Settings ────────────────────────────────────────────────────────────────

// xboxBoostSettingsRow is Settings → Games & emulators → "Xbox boost", below
// Extra memory (swap). It boosts GameCube and Wii games too, where the
// handheld plays them (dolphin.go); the second line says so there.
func xboxBoostSettingsRow() setRow {
	return setRow{
		ID: xboxBoostSetting, Label: "Xbox boost", Kind: setKindToggle,
		Visible: func(*Config) bool { return xboxBoostSupported() },
		Detail:  xboxBoostSettingDetail,
		Get:     xboxBoostEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, xboxBoostSetting, on)
			setToast("Xbox boost: " + setOnOff(on) + " · from the next " + xboxBoostWhat() + " game or stream")
		},
	}
}

// xboxBoostWhat names the games the boost is for: GameCube, Wii and PS2 as
// well where the handheld plays them.
func xboxBoostWhat() string {
	names := []string{"Xbox"}
	if dolphinPlaysHere() {
		names = append(names, "GameCube", "Wii")
	}
	if ps2PlaysHere() {
		names = append(names, "PS2")
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// xboxBoostSettingDetail is the row's second line. Cheap: drawn every frame.
func xboxBoostSettingDetail(c *Config) string {
	if !xboxBoostEnabled(c) {
		return xboxBoostWhat() + " games and streams start like any other game"
	}
	xboxBoost.mu.Lock()
	st := xboxBoost.status
	xboxBoost.mu.Unlock()
	if st != "" {
		return st
	}
	return xboxBoostWhat() + " games and streams get every core and the GPU at full speed, and JukaHub's memory"
}
