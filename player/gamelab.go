package main

// gamelab.go — Game Lab: one game played in two setups, so the player can see
// which emulator or speed setting suits it on this device.
//
// A setup is an emulator (one of the game's own launcher choices: a launch
// script, a RetroArch core started directly, a standalone emulator) and the
// CPU settings a launch runs with (gameoptions.go: Auto, Battery saver,
// Balanced, Performance, and an optional clock limit). The Lab keeps two, A
// and B, per game. "Run A" starts the game the usual way (launchLibraryGame)
// with A's emulator and, for that one launch, A's CPU settings; the player
// plays a minute or two and quits, and the run's frame rate, sound glitches
// and battery draw are measured (gamelab_measure.go) and kept here. "Use A"
// then makes A the game's own setup, through the same per-game options the
// emulator picker and the options sheet write.
//
// How a run's settings reach its launch without sticking. The emulator is the
// launcher handed to launchLibraryGame, so nothing about it is stored. The CPU
// settings are an override gameOptionsFor lays over the stored options
// (gameLabOptions), live only between this run's launch-settings hook
// (gameLaunchRegisterPrepare) and its start (gameLaunchRegisterStarted): the
// power profile and Automatic CPU speed read the game's options in that
// stretch and nowhere later, and the whole stretch runs on the main thread in
// one call, so nothing else can save the override as the game's own. It also
// goes when the launch fails (the Run caller's error, and a check posted from
// the hook that sees no start), after gameLabLiveFor whatever happens, and it
// never outlives JukaHub: it is memory only.
//
// The Run press waits for its launch (gameLabPendingFor): Game Ready, a save
// download or a private glibc install can hold the launch and start it again
// later, and the run must still be recognised then — by its game and its
// emulator.
//
// Results are kept per game (keyed like the game options, by ROM path) in
// data/game-lab/labs.json, versioned; a missing or broken file reads as "no
// Lab yet". Re-running a setup replaces its result, and changing a setup drops
// the result it no longer describes. Sharing a setup is gamelab_share.go; the
// screen is gamelab_ui.go.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func init() {
	gameLaunchRegisterPrepare(gameLabPrepare)
	gameLaunchRegisterStarted(gameLabStarted)
	gameLaunchRegisterExited(gameLabExited)
}

const (
	gameLabSchemaVersion = 1
	gameLabDirName       = "game-lab"
	// gameLabFileMax bounds the results file read.
	gameLabFileMax = 2 << 20
	// gameLabMaxGames bounds the games kept: the oldest Lab goes first.
	gameLabMaxGames = 400
	// gameLabShortRun: a run that played less than this is kept but marked
	// too short to judge.
	gameLabShortRun = 45 * time.Second
	// gameLabPendingFor is how long a Run press waits for its launch to reach
	// the launch settings; gameLabLiveFor how long the CPU override may then
	// wait for the start.
	gameLabPendingFor = 10 * time.Minute
	gameLabLiveFor    = time.Minute
	// The verdict's thresholds: a speed difference of two points of full
	// speed, five points of slow seconds, half a sound glitch a minute, five
	// per cent of power.
	gameLabSpeedStep  = 0.02
	gameLabSmoothStep = 0.05
	gameLabAudioStep  = 0.5
	gameLabPowerStep  = 0.05
)

// The Lab's two sides.
const (
	gameLabSideA = "a"
	gameLabSideB = "b"
)

// gameLabSetup is one side of a comparison.
type gameLabSetup struct {
	Launcher string `json:"launcher"`
	// Name is the emulator as the picker names it; Kind "core", "retroarch"
	// or "standalone"; Core the libretro core when known.
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
	Core string `json:"core,omitempty"`
	// Profile and MaxFreqKHz are gameOptions' CPUProfile and MaxFreqKHz.
	Profile    string `json:"cpu_profile,omitempty"`
	MaxFreqKHz int64  `json:"max_freq_khz,omitempty"`
}

// gameLabResult is what one run measured. A zero figure with its "why" set is
// "not measured", never a measurement of zero.
type gameLabResult struct {
	Played float64 `json:"played_s"`
	Short  bool    `json:"short,omitempty"`

	// FPS against Target, by FPSMethod ("frames" counted on the screen, or
	// "log": RetroArch's own average); SlowShare is the share of the counted
	// seconds under 90 % of the game's rate (frames only).
	FPS        float64 `json:"fps,omitempty"`
	Target     float64 `json:"target,omitempty"`
	FPSMethod  string  `json:"fps_method,omitempty"`
	FPSSeconds float64 `json:"fps_seconds,omitempty"`
	SlowShare  float64 `json:"slow_share,omitempty"`
	FPSWhy     string  `json:"fps_why,omitempty"`

	// Audio: RetroArch's log was read. Glitches counts the underrun and
	// recovery lines its sound driver (or ALSA) wrote; SoundLost is "Will
	// continue without sound"; UnderrunPct is the "close to underrun" share
	// builds that report buffer statistics write when the game closes.
	Audio       bool    `json:"audio,omitempty"`
	Glitches    int     `json:"glitches,omitempty"`
	SoundLost   bool    `json:"sound_lost,omitempty"`
	UnderrunPct float64 `json:"underrun_pct,omitempty"`
	HasUnderrun bool    `json:"has_underrun,omitempty"`
	AudioWhy    string  `json:"audio_why,omitempty"`

	// Battery: the median draw in watts, else the level's fall per hour.
	Watts      float64 `json:"watts,omitempty"`
	PctPerHour float64 `json:"pct_per_hour,omitempty"`
	BatteryWhy string  `json:"battery_why,omitempty"`

	// The core RetroArch's log says it loaded, and its version when the
	// core's file name carries one.
	Core        string `json:"core,omitempty"`
	CoreVersion string `json:"core_version,omitempty"`
}

// gameLabEntry is one game's Lab.
type gameLabEntry struct {
	System  string         `json:"system"`
	Title   string         `json:"title"`
	A       gameLabSetup   `json:"a"`
	B       gameLabSetup   `json:"b"`
	ResultA *gameLabResult `json:"result_a,omitempty"`
	ResultB *gameLabResult `json:"result_b,omitempty"`
	// Winner is the side the player applied with Use, and WinnerName its
	// setup's name for the page's chip; "" until then.
	Winner     string `json:"winner,omitempty"`
	WinnerName string `json:"winner_name,omitempty"`
	// Seq orders the entries (the handheld's clock may be wrong).
	Seq uint64 `json:"seq"`
}

// gameLabFile is the document on disk.
type gameLabFile struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Seq           uint64                   `json:"seq"`
	Games         map[string]*gameLabEntry `json:"games"`
}

// result is side's result, or nil.
func (e *gameLabEntry) result(side string) *gameLabResult {
	if side == gameLabSideB {
		return e.ResultB
	}
	return e.ResultA
}

// setup is side's setup.
func (e *gameLabEntry) setup(side string) gameLabSetup {
	if side == gameLabSideB {
		return e.B
	}
	return e.A
}

// clone is a deep copy, so the screen's copy and the store's never share a
// result.
func (e *gameLabEntry) clone() gameLabEntry {
	out := *e
	if e.ResultA != nil {
		r := *e.ResultA
		out.ResultA = &r
	}
	if e.ResultB != nil {
		r := *e.ResultB
		out.ResultB = &r
	}
	return out
}

// ── The store ────────────────────────────────────────────────────────────

var gameLabPath = func() string {
	dir := gameLabDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "labs.json")
}

// gameLabDir is data/game-lab, "" without a data folder.
func gameLabDir() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, gameLabDirName)
}

// gameLabStore holds every game's Lab once read. Main thread and goroutines.
// readOnly: the file is a newer build's, which this one must not overwrite.
var gameLabStore struct {
	sync.Mutex
	loaded   bool
	loading  bool
	readOnly bool
	seq      uint64
	games    map[string]*gameLabEntry
}

// gameLabWriteMu keeps the file writes in order, outside the store's lock.
var gameLabWriteMu sync.Mutex

// gameLabReadFile reads the results; a missing, oversized, broken or newer
// file reads as no Lab yet.
func gameLabReadFile(path string) gameLabFile {
	file := gameLabFile{Games: map[string]*gameLabEntry{}}
	if path == "" {
		return file
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > gameLabFileMax {
		return file
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return file
	}
	var read gameLabFile
	if err := json.Unmarshal(raw, &read); err != nil {
		logWarn("gamelab", "ignoring %s: %v", path, err)
		return file
	}
	if read.SchemaVersion < 1 || read.SchemaVersion > gameLabSchemaVersion {
		logWarn("gamelab", "ignoring %s: schema %d", path, read.SchemaVersion)
		if read.SchemaVersion > gameLabSchemaVersion {
			file.SchemaVersion = read.SchemaVersion // kept: see gameLabSave
		}
		return file
	}
	file.Seq = read.Seq
	for key, e := range read.Games {
		key = gameOptionsKey(key)
		if key == "" || e == nil || strings.TrimSpace(e.A.Launcher) == "" || strings.TrimSpace(e.B.Launcher) == "" {
			continue
		}
		e.A.Profile = normaliseGameCPUProfile(e.A.Profile)
		e.B.Profile = normaliseGameCPUProfile(e.B.Profile)
		if e.Winner != gameLabSideA && e.Winner != gameLabSideB {
			e.Winner, e.WinnerName = "", ""
		}
		file.Games[key] = e
		if e.Seq > file.Seq {
			file.Seq = e.Seq
		}
	}
	return file
}

// gameLabEnsureLoaded reads the file once. Never on the main thread.
func gameLabEnsureLoaded() {
	gameLabStore.Lock()
	done := gameLabStore.loaded
	gameLabStore.Unlock()
	if done {
		return
	}
	file := gameLabReadFile(gameLabPath())
	gameLabStore.Lock()
	if !gameLabStore.loaded {
		gameLabStore.games, gameLabStore.seq, gameLabStore.loaded = file.Games, file.Seq, true
		gameLabStore.readOnly = file.SchemaVersion > gameLabSchemaVersion
	}
	gameLabStore.loading = false
	gameLabStore.Unlock()
}

// gameLabPeek is the main thread's look at a game's Lab: a copy, and whether
// there is one. Before the file is read it starts the read and says none.
func gameLabPeek(path string) (gameLabEntry, bool) {
	key := gameOptionsKey(path)
	gameLabStore.Lock()
	defer gameLabStore.Unlock()
	if !gameLabStore.loaded {
		if !gameLabStore.loading {
			gameLabStore.loading = true
			go gameLabEnsureLoaded()
		}
		return gameLabEntry{}, false
	}
	e := gameLabStore.games[key]
	if e == nil {
		return gameLabEntry{}, false
	}
	return e.clone(), true
}

// gameLabWinnerName is the page's chip: the setup the player applied. Main
// thread, memory only.
func gameLabWinnerName(path string) string {
	e, ok := gameLabPeek(path)
	if !ok || e.Winner == "" {
		return ""
	}
	return e.WinnerName
}

// gameLabUpdate changes a game's Lab in memory (creating it when create is
// set) and writes the file on a goroutine. false when the store is not read
// yet or there is no such Lab. Any thread.
func gameLabUpdate(path string, create bool, change func(e *gameLabEntry)) bool {
	key := gameOptionsKey(path)
	if key == "" {
		return false
	}
	gameLabStore.Lock()
	if !gameLabStore.loaded {
		gameLabStore.Unlock()
		return false
	}
	e := gameLabStore.games[key]
	if e == nil {
		if !create {
			gameLabStore.Unlock()
			return false
		}
		e = &gameLabEntry{}
		gameLabStore.games[key] = e
	}
	change(e)
	gameLabStore.seq++
	e.Seq = gameLabStore.seq
	gameLabTrimLocked()
	gameLabStore.Unlock()
	go gameLabSave()
	return true
}

// gameLabTrimLocked drops the oldest Labs past gameLabMaxGames.
func gameLabTrimLocked() {
	for len(gameLabStore.games) > gameLabMaxGames {
		oldest, seq := "", uint64(math.MaxUint64)
		for k, e := range gameLabStore.games {
			if e.Seq < seq {
				oldest, seq = k, e.Seq
			}
		}
		if oldest == "" {
			return
		}
		delete(gameLabStore.games, oldest)
	}
}

// gameLabSave writes the store as it is now. Its own goroutine; each save
// writes the latest state, so their order cannot lose a change.
func gameLabSave() {
	gameLabWriteMu.Lock()
	defer gameLabWriteMu.Unlock()
	gameLabStore.Lock()
	if gameLabStore.readOnly {
		// A newer JukaHub's results stay as they are: this session's Lab
		// lives in memory only.
		gameLabStore.Unlock()
		return
	}
	file := gameLabFile{SchemaVersion: gameLabSchemaVersion, Seq: gameLabStore.seq, Games: gameLabStore.games}
	data, err := json.MarshalIndent(file, "", "  ")
	gameLabStore.Unlock()
	if err != nil {
		return
	}
	path := gameLabPath()
	if path == "" || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
		logWarn("gamelab", "cannot save the Lab results: %v", err)
	}
}

// ── Setups ───────────────────────────────────────────────────────────────

// gameLabProfileWord is a profile's name: "Auto" for none.
func gameLabProfileWord(p string) string { return gameCPUProfileLabel(p) }

// gameLabSetupName is a setup as one line: "mGBA · Balanced · 1.6 GHz".
func gameLabSetupName(s gameLabSetup) string {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = filepath.Base(s.Launcher)
	}
	out := name + " · " + gameLabProfileWord(s.Profile)
	if s.MaxFreqKHz > 0 {
		out += " · " + formatGameCeiling(s.MaxFreqKHz)
	}
	return out
}

// gameLabSameLauncher reports whether two launcher paths are one file.
func gameLabSameLauncher(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// gameLabInUse reports whether the game's own options still start it with
// setup s (the emulator it always uses and its CPU settings): what "in use"
// and the page's chip may claim after Use, once the options sheet or the
// emulator picker changed the game since.
func gameLabInUse(s gameLabSetup, opts gameOptions) bool {
	return gameLabSameLauncher(opts.Emulator, s.Launcher) &&
		normaliseGameCPUProfile(opts.CPUProfile) == normaliseGameCPUProfile(s.Profile) &&
		opts.MaxFreqKHz == s.MaxFreqKHz
}

// gameLabOtherProfile is B's profile when the game has one emulator: the
// same core at a setting worth comparing with A's.
func gameLabOtherProfile(p string) string {
	switch normaliseGameCPUProfile(p) {
	case "battery":
		return "performance"
	case "performance":
		return "balanced"
	}
	return "battery"
}

// ── The one-launch override ──────────────────────────────────────────────

// gameLabOverride is the CPU settings one Lab launch runs with.
type gameLabOverride struct {
	key        string
	profile    string
	maxFreqKHz int64
	at         time.Time
}

// gameLabLive is the override while its launch is between its settings and
// its start; nil otherwise.
var gameLabLive atomic.Pointer[gameLabOverride]

// gameLabOptions is gameoptions.go's hook: gameOptionsFor's answer for key,
// with a Lab run's CPU settings while its launch reads them. It runs under
// the options' lock, so it takes no lock of its own.
func gameLabOptions(key string, opts gameOptions) gameOptions {
	o := gameLabLive.Load()
	if o == nil || o.key != key {
		return opts
	}
	if time.Since(o.at) > gameLabLiveFor {
		gameLabLive.CompareAndSwap(o, nil)
		return opts
	}
	opts.CPUProfile = o.profile
	opts.MaxFreqKHz = o.maxFreqKHz
	return opts
}

// ── Runs ─────────────────────────────────────────────────────────────────

// gameLabRun is one Lab launch, from the Run press to its result.
type gameLabRun struct {
	key     string
	game    libraryGame
	side    string
	setup   gameLabSetup
	armedAt time.Time
	// Set by the launch hooks, under gameLabState's lock.
	prepared bool
	launchAt time.Time
	port     int // RetroArch's command port for the frame count; 0 none
	retro    bool
	started  bool
	startAt  time.Time
	// superseded: another library launch came after this run's game was
	// gone but before its end was reported; the logs are that launch's now.
	superseded bool
	// end gets the logs read when the game's process group is gone.
	end chan gameLabEnd
}

// gameLabEnd is what the reaper hands the measuring goroutine.
type gameLabEnd struct {
	ranFor time.Duration
	raLog  string // RetroArch's log of this launch ("" none)
	outLog string // the launch script's output ("" none)
}

// gameLabState is the run in progress, at most one. Any goroutine.
var gameLabState struct {
	sync.Mutex
	run *gameLabRun
}

// gameLabRunning reports the side of game's run being played or measured.
// A run still waiting for its launch (held by a card, or a download first)
// is not one yet: another Run press replaces it. Memory only.
func gameLabRunning(path string) (string, bool) {
	key := gameOptionsKey(path)
	gameLabState.Lock()
	defer gameLabState.Unlock()
	r := gameLabState.run
	if r == nil || r.key != key || !r.prepared {
		return "", false
	}
	return r.side, true
}

// gameLabBusy reports whether a run is being played or measured.
func gameLabBusy() bool {
	gameLabState.Lock()
	defer gameLabState.Unlock()
	return gameLabState.run != nil && gameLabState.run.prepared
}

// gameLabClear ends run (when it is still the current one) and drops its
// override.
func gameLabClear(run *gameLabRun) {
	gameLabState.Lock()
	if gameLabState.run == run {
		gameLabState.run = nil
	}
	gameLabState.Unlock()
	if o := gameLabLive.Load(); o != nil && run != nil && o.key == run.key {
		gameLabLive.CompareAndSwap(o, nil)
	}
}

// gameLabDropPending forgets a Run press whose launch has not reached its
// settings (a card or a download still holds it) when the Lab that made it
// closes: a later start of the game from its page is the player's own, with
// the game's own CPU settings, and is not measured. Main thread.
func gameLabDropPending() {
	gameLabState.Lock()
	if r := gameLabState.run; r != nil && !r.prepared {
		gameLabState.run = nil
		logInfo("gamelab", "run %s of %s dropped: the Lab closed before its launch", strings.ToUpper(r.side), filepath.Base(r.game.Path))
	}
	gameLabState.Unlock()
}

// gameLabStartRun starts game with side's setup for a measured run. Main
// thread. A launch that fails ends the run at once.
func gameLabStartRun(game libraryGame, side string, setup gameLabSetup) error {
	if gameLabBusy() {
		return fmt.Errorf("another Lab run is still waiting to finish")
	}
	run := &gameLabRun{
		key:     gameOptionsKey(game.Path),
		game:    game,
		side:    side,
		setup:   setup,
		armedAt: time.Now(),
		end:     make(chan gameLabEnd, 1),
	}
	gameLabState.Lock()
	gameLabState.run = run
	gameLabState.Unlock()
	logInfo("gamelab", "run %s of %s: %s", strings.ToUpper(side), filepath.Base(game.Path), gameLabSetupName(setup))
	if err := launchLibraryGame(game, setup.Launcher); err != nil {
		gameLabClear(run)
		return err
	}
	return nil
}

// gameLabPrepare is the launch-settings hook: this launch is the Lab run's
// when its game and emulator match. It turns the override on and, for a
// RetroArch game, RetroArch's command port for the frame count (scoped to
// this launch, as the shader speed check does).
func gameLabPrepare(launcher string, game libraryGame) {
	key := gameOptionsKey(game.Path)
	gameLabState.Lock()
	run := gameLabState.run
	switch {
	case run == nil:
		gameLabState.Unlock()
		return
	case run.started:
		// A launch while the run's end is not in yet (the reaper assembles a
		// time-lapse before it reports): the logs it writes are not the run's.
		run.superseded = true
		gameLabState.Unlock()
		return
	case !run.prepared && time.Since(run.armedAt) > gameLabPendingFor:
		gameLabState.run = nil
		gameLabState.Unlock()
		return
	case run.prepared || run.key != key || !gameLabSameLauncher(run.setup.Launcher, launcher):
		gameLabState.Unlock()
		return
	}
	run.prepared, run.launchAt = true, time.Now()
	gameLabState.Unlock()
	gameLabLive.Store(&gameLabOverride{key: key, profile: run.setup.Profile, maxFreqKHz: run.setup.MaxFreqKHz, at: time.Now()})
	retro := retroArchLauncher(launcher)
	port := 0
	if retro {
		port = gameLabCommandPort(launcher)
	}
	gameLabState.Lock()
	run.port, run.retro = port, retro
	gameLabState.Unlock()
	// launchLibraryGame runs on to the start in this same call: by the time
	// the main loop runs this, the run has started or its launch failed.
	tbxPost(func() { gameLabPrepareSettled(run) })
}

// gameLabCommandPort scopes RetroArch's command interface on for this launch
// and returns its port; 0 when frames cannot be counted here.
func gameLabCommandPort(launcher string) int {
	if !shaderSpeedFramesPossible() {
		return 0
	}
	port := 0
	for _, path := range raScopeTargets(raLaunchCandidates(launcher, savestateResumeCandidates())) {
		if _, err := raScopeApply(path, []raScopeValue{{Key: "network_cmd_enable", Value: "true"}}); err != nil {
			continue
		}
		if port == 0 {
			if raw, err := os.ReadFile(path); err == nil {
				port = networkCmdPort(string(raw))
			}
		}
	}
	return port
}

// gameLabPrepareSettled ends a run whose launch reached its settings but never
// started (the launch failed after them). Main thread.
func gameLabPrepareSettled(run *gameLabRun) {
	gameLabState.Lock()
	failed := gameLabState.run == run && !run.started
	gameLabState.Unlock()
	if !failed {
		return
	}
	gameLabClear(run)
	logInfo("gamelab", "run %s of %s did not start", strings.ToUpper(run.side), filepath.Base(run.game.Path))
}

// gameLabStarted is the start hook: the override has done its work, and the
// measuring begins.
func gameLabStarted(game libraryGame, launcher string, _ *exec.Cmd) {
	key := gameOptionsKey(game.Path)
	gameLabState.Lock()
	run := gameLabState.run
	if run == nil || !run.prepared || run.started || run.key != key || !gameLabSameLauncher(run.setup.Launcher, launcher) {
		gameLabState.Unlock()
		return
	}
	run.started, run.startAt = true, time.Now()
	gameLabState.Unlock()
	if o := gameLabLive.Load(); o != nil && o.key == key {
		gameLabLive.CompareAndSwap(o, nil)
	}
	go gameLabWatch(run)
}

// gameLabExited is the end hook (the reaper goroutine): the run's logs are
// read now, before anything else can empty them, and handed to its watch.
func gameLabExited(game libraryGame, _ string, ranFor time.Duration, _ error) {
	key := gameOptionsKey(game.Path)
	gameLabState.Lock()
	run := gameLabState.run
	ok := run != nil && run.started && run.key == key
	superseded := ok && run.superseded
	gameLabState.Unlock()
	if !ok {
		return
	}
	end := gameLabEnd{ranFor: ranFor}
	if dir := gameDiagLogDir(); dir != "" && !superseded {
		if run.retro {
			end.raLog = gameLabReadLog(filepath.Join(dir, "retroarch.log"), run.launchAt)
		}
		end.outLog = gameLabReadLog(filepath.Join(dir, launcherLogName), run.launchAt)
	}
	select {
	case run.end <- end:
	default:
	}
}

// gameLabFinish stores a run's result and tells the screen. The watch
// goroutine.
func gameLabFinish(run *gameLabRun, result gameLabResult) {
	gameLabEnsureLoaded()
	saved := gameLabUpdate(run.game.Path, true, func(e *gameLabEntry) {
		if e.System == "" {
			e.System, e.Title = run.game.System, run.game.Title
		}
		if e.A.Launcher == "" {
			e.A = run.setup
		}
		if e.B.Launcher == "" {
			e.B = run.setup
		}
		r := result
		// The result belongs to the setup it measured: a side changed while
		// the game ran takes the measured setup back.
		if run.side == gameLabSideB {
			e.B, e.ResultB = run.setup, &r
		} else {
			e.A, e.ResultA = run.setup, &r
		}
	})
	gameLabClear(run)
	if !saved {
		logWarn("gamelab", "the result of run %s could not be kept", strings.ToUpper(run.side))
	}
	tbxPost(func() { gameLabResultIn(run.game, run.side, result) })
}

// ── Verdict ──────────────────────────────────────────────────────────────

// gameLabSpeedShare is a result's speed as a share of full speed (capped at
// 1), and whether it was measured.
func gameLabSpeedShare(r *gameLabResult) (float64, bool) {
	if r == nil || r.FPS <= 0 || r.Target <= 0 {
		return 0, false
	}
	return math.Min(r.FPS/r.Target, 1), true
}

// gameLabGlitchRate is a result's sound glitches a minute, and whether sound
// was checked. Lost sound counts as far worse than any rate.
func gameLabGlitchRate(r *gameLabResult) (float64, bool) {
	if r == nil || !r.Audio {
		return 0, false
	}
	if r.SoundLost {
		return 1e6, true
	}
	minutes := math.Max(r.Played/60, 0.25)
	return float64(r.Glitches) / minutes, true
}

// gameLabVerdict compares two results: the line under the table, and the side
// it favours ("" when they are even or cannot be compared). Pure.
func gameLabVerdict(a, b *gameLabResult) (string, string) {
	switch {
	case a == nil && b == nil:
		return "Run A and B for a minute or two each to compare them.", ""
	case b == nil:
		return "Now run B to compare.", ""
	case a == nil:
		return "Now run A to compare.", ""
	case a.Short && b.Short:
		return "Both runs were too short to judge; play each for at least a minute.", ""
	case a.Short:
		return "Run A was too short to judge; play it for at least a minute.", ""
	case b.Short:
		return "Run B was too short to judge; play it for at least a minute.", ""
	}
	var points [2][]string
	var score [2]int
	note := func(side int, weight int, what string) {
		points[side] = append(points[side], what)
		score[side] += weight
	}
	compared := false
	if sa, ok := gameLabSpeedShare(a); ok {
		if sb, ok := gameLabSpeedShare(b); ok {
			compared = true
			switch {
			case math.Abs(sb-sa) >= gameLabSpeedStep:
				side, best, worst := 0, sa, sb
				if sb > sa {
					side, best, worst = 1, sb, sa
				}
				what := "runs faster"
				if best >= shaderSpeedFullShare && worst < shaderSpeedFullShare {
					what = "holds full speed"
				}
				note(side, 4, what)
			case a.FPSMethod == "frames" && b.FPSMethod == "frames" && math.Abs(b.SlowShare-a.SlowShare) >= gameLabSmoothStep:
				side := 0
				if b.SlowShare < a.SlowShare {
					side = 1
				}
				note(side, 3, "is smoother")
			}
		}
	}
	if ga, ok := gameLabGlitchRate(a); ok {
		if gb, ok := gameLabGlitchRate(b); ok {
			compared = true
			switch {
			case a.SoundLost != b.SoundLost:
				side := 0
				if a.SoundLost {
					side = 1
				}
				note(side, 3, "keeps its sound")
			case math.Abs(gb-ga) >= gameLabAudioStep:
				side := 0
				if gb < ga {
					side = 1
				}
				note(side, 2, "has fewer sound glitches")
			}
		}
	}
	pa, pb := a.Watts, b.Watts
	if pa <= 0 || pb <= 0 {
		pa, pb = a.PctPerHour, b.PctPerHour
	}
	if pa > 0 && pb > 0 {
		compared = true
		hi, lo, side := pa, pb, 1
		if pa < pb {
			hi, lo, side = pb, pa, 0
		}
		if saving := (hi - lo) / hi; saving >= gameLabPowerStep {
			note(side, 1, fmt.Sprintf("uses %.0f%% less power", saving*100))
		}
	}
	names := [2]string{"A", "B"}
	join := func(side int) string {
		list := points[side]
		switch len(list) {
		case 0:
			return ""
		case 1:
			return names[side] + " " + list[0]
		}
		return names[side] + " " + strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
	}
	switch {
	case !compared:
		return "Nothing was measured on both runs, so they cannot be compared.", ""
	case len(points[0]) == 0 && len(points[1]) == 0:
		return "A and B ran about the same here.", ""
	case len(points[0]) == 0:
		return join(1) + ".", gameLabSideB
	case len(points[1]) == 0:
		return join(0) + ".", gameLabSideA
	}
	line := join(0) + "; " + join(1) + "."
	switch {
	case score[0] > score[1]:
		return line, gameLabSideA
	case score[1] > score[0]:
		return line, gameLabSideB
	}
	return line, ""
}

// ── Applying a setup ─────────────────────────────────────────────────────

// gameLabApply makes side's setup the game's own: its emulator (the per-game
// choice the emulator picker and the options sheet keep) and its CPU
// settings. Main thread.
func gameLabApply(game libraryGame, side string) {
	e, ok := gameLabPeek(game.Path)
	if !ok {
		return
	}
	s := e.setup(side)
	if !gameLauncherStillUsable(s.Launcher) {
		ShowToast(s.Name+" is no longer installed, so it cannot be used", ToastKindWarning)
		return
	}
	if gameLabBusy() {
		ShowToast("Wait for the Lab run to finish first", ToastKindInfo)
		return
	}
	updateGameOptions(game.Path, func(o gameOptions) gameOptions {
		o.Emulator = s.Launcher
		o.LastEmulator = s.Launcher
		o.CPUProfile = s.Profile
		o.MaxFreqKHz = s.MaxFreqKHz
		return o
	})
	name := gameLabSetupName(s)
	gameLabUpdate(game.Path, false, func(e *gameLabEntry) {
		e.Winner, e.WinnerName = side, name
	})
	logInfo("gamelab", "%s now uses setup %s: %s", filepath.Base(game.Path), strings.ToUpper(side), name)
	ShowToast(game.Label()+" now starts with "+name, ToastKindSuccess)
}
