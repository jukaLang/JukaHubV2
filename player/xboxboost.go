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
//   - memory clock: a memory controller with a devfreq node (a name with
//     "dram", "ddr" or "dmc") the same way — an emulator of a 3D console
//     waits on memory as much as on the CPU, and a DRAM governor that clocks
//     down between bursts costs it frames;
//   - the card: the read-ahead of the disk the game is read from goes up to
//     xboxBoostReadAheadKB where it is lower (the kernel's default is
//     128 KB), so a disc image streamed during play — a level, a video, the
//     music — is read in long runs instead of many small ones;
//   - memory: JukaHub's textures and byte caches are emptied just before the
//     start (main thread, while nothing draws them); beside the launch the Go
//     heap is handed back, the warm speech recognizer, the media library's
//     scan and the video link prefetch stop, and the kernel's page cache is
//     dropped and compacted (memFreeKernel). Every game start already stops
//     the offline AI and the Piper voice (voiceReleaseForGame). On a 1 GB
//     handheld this decides whether the game's working set sits in RAM or in
//     swap — and a start in the first minutes after power-on sets that swap
//     up at once rather than at its quiet moment (xboxBoostSwapEarly);
//   - priority: the game's process group runs at nice xboxBoostNice, ahead of
//     whatever JukaHub or the firmware does in the background;
//     JukaHub's own threads step back to nice 10 and the idle I/O class, and
//     on a chip with a faster cluster (the Smart Pro S) the game is held to
//     it (xboxboost_hub_linux.go);
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
// It began with the Xbox; GameCube, Wii and PS2 games joined where the
// handheld plays them (dolphin.go, ps2.go), and on 2026-11-21 ("improve
// performance of all emulators...especially on Trimui Smart Pro and Trimui
// Brick") the consoles the A133P only just plays joined on that chip: PSP,
// Dreamcast (with NAOMI and Atomiswave), Saturn, Jaguar, N64 and DS. The
// Smart Pro S plays those without it.
//
// Settings → Games & emulators → "Heavy-game boost" (xboxBoost, on by default;
// the key keeps its first name) turns it off. On a PC, and on the handheld for
// every other game, nothing here runs.
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
	// xboxBoostReadAheadKB is the read-ahead (KB) the card a boosted game is
	// read from gets: what the JukaHub OS images give it at boot.
	xboxBoostReadAheadKB = 1024
)

// xboxBoostDevfreqRoot is where a GPU's or memory controller's devfreq node
// is looked for; xboxBoostBlockRoot where a disk's queue settings are.
var (
	xboxBoostDevfreqRoot = "/sys/class/devfreq"
	xboxBoostBlockRoot   = "/sys/block"
)

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
	writes   []saverSysfsWrite // cores, GPU, memory, card, in the order made
	wifi     []string          // radios whose power saving went off ("iw:wlan0")
	details  []string          // everything done, for the log
	cores    int               // parked cores brought online
	gpu      bool              // the GPU's governor or floor raised
	mem      bool              // the memory controller's governor or floor raised
	disk     bool              // the card's read-ahead raised
	reniced  bool              // the game's group runs at xboxBoostNice
	pinned   string            // the fast cluster's CPUs the game is held to
	stepped  bool              // JukaHub's threads stepped back
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
// whole machine as much, and so do the consoles the A133P only just plays
// (xboxBoostA133PHeavy).
func xboxBoostKindForGame(game libraryGame) xboxBoostKind {
	if strings.EqualFold(strings.TrimSpace(game.System), emuXboxEntryName) {
		return xboxBoostGame
	}
	if dolphinPlaysSystemHere(game.System) || ps2PlaysSystemHere(game.System) || xboxBoostA133PHeavy(game.System) {
		return xboxBoostGame
	}
	return xboxBoostNone
}

// xboxBoostA133PConsoles are the consoles the A133P (Smart Pro, Brick, Brick
// Pro, Hammer) only just plays, by canonicalCompatSystem's names and the
// library's; xboxBoostA133PFolders the folder names (lower case) neither
// knows.
var (
	xboxBoostA133PConsoles = map[string]bool{
		"PlayStation Portable": true, "Dreamcast": true, "Saturn": true,
		"Nintendo 64": true, "Nintendo DS": true, "Atari Jaguar": true,
	}
	xboxBoostA133PFolders = map[string]bool{
		"naomi": true, "naomi2": true, "atomiswave": true, "awave": true,
		"ss": true, "ds": true, "jaguar": true, "atarijaguar": true,
	}
)

// xboxBoostOnA133P reports a TrimUI handheld on the A133P.
func xboxBoostOnA133P() bool {
	return xboxBoostSupported() && deviceHardware(DetectDevice()).SoC == socA133P
}

// xboxBoostA133PHeavy reports whether system's games get the boost because
// this handheld is an A133P one.
func xboxBoostA133PHeavy(system string) bool {
	return xboxBoostOnA133P() && xboxBoostSystemIn(system, xboxBoostA133PConsoles, xboxBoostA133PFolders)
}

// xboxBoostSystemIn reports whether system — a library name ("N64"),
// NextUI's "Nintendo 64 (N64)" or an emulator folder's name — is one of
// consoles or folders. Pure.
func xboxBoostSystemIn(system string, consoles, folders map[string]bool) bool {
	s := strings.TrimSpace(system)
	if s == "" {
		return false
	}
	names := []string{s}
	if i := strings.LastIndex(s, "("); i > 0 && strings.HasSuffix(s, ")") {
		names = append(names, strings.TrimSpace(s[i+1:len(s)-1]), strings.TrimSpace(s[:i]))
	}
	for _, n := range names {
		if consoles[canonicalCompatSystem(n)] || consoles[n] || folders[strings.ToLower(n)] {
			return true
		}
	}
	return false
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
	s.applyLocked(xboxBoostHardwarePlan(target))
	xboxBoostOps.Unlock()
	if xboxBoostSwapEarly() {
		s.detail("swap set up before its quiet moment")
	}
	// Nothing draws until the game is gone: every texture and byte cache goes
	// now and is made again on the way back.
	evictAllCachesForPressure()
	s.detail("JukaHub's caches emptied")
	go s.background(before)
	return s
}

// xboxBoostSwapEarly starts the swap setup (memswap.go) when a boosted game
// starts before that setup's quiet moment — in the first minutes after
// power-on, or a game resumed at power-on — so a 1 GB handheld has its
// compressed swap, or the swap file an earlier start wrote, before the game's
// working set needs it. A swap file still to be written stops as the game
// takes the screen and is tried again at the next quiet moment, as memswap.go
// does for any game. Main thread (the switch is read from the config).
func xboxBoostSwapEarly() bool {
	if !memSwapSupported() || memSwap.asked.Load() || !memSwapEnabled(appConfig) {
		return false
	}
	memSwap.asked.Store(true)
	memSwapApply(true)
	return true
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
		switch {
		case filepath.Base(w.Path) == "online":
			s.cores++
		case filepath.Base(w.Path) == "read_ahead_kb":
			s.disk = true
		case xboxBoostMemName(filepath.Base(filepath.Dir(w.Path))):
			s.mem = true
		default:
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
	// Not for a game that already ended: the sync and the dropped pages
	// would only slow JukaHub's own screen (after a GreenOvercast start that
	// closed within 3 s, the failure card stalled 1.5 s while this ran,
	// JH-AT4EEE).
	s.mu.Lock()
	gone := s.ended
	s.mu.Unlock()
	if !gone && memFreeKernel() {
		s.detail("page cache dropped")
	}
	// JukaHub's own threads behind the game, unless it already ended
	// (xboxboost_hub_linux.go; put back in restoreLocked, which runs under
	// the same lock, so the two never cross).
	xboxBoostOps.Lock()
	s.mu.Lock()
	over := s.restored
	s.mu.Unlock()
	if !over {
		if n := xboxBoostStepBack(); n > 0 {
			s.mu.Lock()
			s.stepped = true
			s.details = append(s.details, fmt.Sprintf("JukaHub's %d threads behind the game", n))
			s.mu.Unlock()
		}
	}
	xboxBoostOps.Unlock()
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
	// The fast cluster first: the script has not started the emulator yet,
	// which inherits it (xboxboost_hub_linux.go).
	if cpus := xboxBoostPin(pid); cpus != "" {
		s.mu.Lock()
		s.pinned = cpus
		s.details = append(s.details, "game held to the fast cluster (CPUs "+cpus+")")
		s.mu.Unlock()
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
	if s.mem {
		parts = append(parts, "memory at full speed")
	}
	if s.disk {
		parts = append(parts, "card read-ahead raised")
	}
	parts = append(parts, "JukaHub's memory freed")
	if s.reniced {
		parts = append(parts, "first on the CPU")
	}
	if s.pinned != "" {
		parts = append(parts, "on the fast cores")
	}
	if s.stepped {
		parts = append(parts, "JukaHub stepped back")
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
	xboxBoostStepForward()
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
	xboxBoost.status = "Last boosted game: " + summary
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
// online switch, a devfreq GPU's or memory controller's governor or floor, or
// a disk's read-ahead (/sys/block/<disk>/queue/read_ahead_kb).
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
		name := filepath.Base(dir)
		return filepath.Dir(dir) == filepath.Clean(xboxBoostDevfreqRoot) &&
			(strings.Contains(strings.ToLower(name), "gpu") || xboxBoostMemName(name))
	case "read_ahead_kb":
		disk := filepath.Dir(dir)
		return filepath.Base(dir) == "queue" && filepath.Dir(disk) == filepath.Clean(xboxBoostBlockRoot) &&
			xboxBoostDiskName(filepath.Base(disk))
	}
	return false
}

// xboxBoostMemName reports a devfreq device name that is a memory
// controller's ("dramfreq", "dmc", "ddr…"), not a GPU's.
func xboxBoostMemName(name string) bool {
	name = strings.ToLower(name)
	if strings.Contains(name, "gpu") {
		return false
	}
	return strings.Contains(name, "dram") || strings.Contains(name, "ddr") || strings.Contains(name, "dmc")
}

// xboxBoostDiskName reports a block device name the boost may tune: letters
// and digits only (mmcblk0, sda, nvme0n1).
func xboxBoostDiskName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
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

// xboxBoostSettingsRow is Settings → Games & emulators → "Heavy-game boost",
// below Extra memory (swap). It began as "Xbox boost"; the second line names
// the consoles it covers on this handheld (xboxBoostWhat).
func xboxBoostSettingsRow() setRow {
	return setRow{
		ID: xboxBoostSetting, Label: "Heavy-game boost", Kind: setKindToggle,
		Visible: func(*Config) bool { return xboxBoostSupported() },
		Detail:  xboxBoostSettingDetail,
		Get:     xboxBoostEnabled,
		Set: func(c *Config, on bool) {
			setStoreBool(c, xboxBoostSetting, on)
			setToast("Heavy-game boost: " + setOnOff(on) + " · from the next " + xboxBoostWhat() + " game or stream")
		},
	}
}

// xboxBoostWhat names the games the boost is for: GameCube, Wii and PS2 as
// well where the handheld plays them, and on the A133P the consoles it only
// just plays.
func xboxBoostWhat() string {
	names := []string{"Xbox"}
	if dolphinPlaysHere() {
		names = append(names, "GameCube", "Wii")
	}
	if ps2PlaysHere() {
		names = append(names, "PS2")
	}
	if xboxBoostOnA133P() {
		names = append(names, "PSP", "Dreamcast", "Saturn", "N64", "DS")
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
	return xboxBoostWhat() + " games and streams get every core, the GPU and memory at full speed, and JukaHub's memory"
}
