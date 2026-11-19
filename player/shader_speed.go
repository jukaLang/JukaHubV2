package main

// shader_speed.go — does the game still hold full speed with the look the
// player chose (Scanlines, LCD grid, CRT, Pixel transparency — chosen or
// picked by Auto)? Measured from the frames the game
// really put on screen while it was played; never guessed from a shader's
// name or a menu benchmark.
//
// Two ways to measure, best first:
//
//  1. Played frames (the Linux handheld; the counter is in
//     shader_speed_linux.go). RetroArch's command port is on for the launch
//     (scoped, like the in-game menu's). Once a second the check asks
//     GET_STATUS, and between two answers that both say PLAYING it counts the
//     framebuffer's page flips. A stretch is left out when JukaHub paused the
//     game in it (the in-game menu, the game assistant, an alarm:
//     raCoopPause/raCoopResume call shaderSpeedFreezeNote), while an overlay,
//     an alarm, the sleep or a quit is on, during the first seconds after the
//     launch (loading), and when the game ran faster than its own rate
//     (fast-forward). Sixteen seconds of clean play make a reading, and the
//     counter stops there. A screen that never flips (a driver that does not
//     pan) sends the check to the second way.
//  2. RetroArch's own frame timing (the PC build, a driver that does not pan,
//     a game quit before the count was done). gamediag.go has RetroArch write
//     its log for every library launch; closing a game, RetroArch logs
//     "Average monitor Hz: 59.94 Hz. (0.5 % frame time deviation, 2048
//     samples)." — the rate it presented its last 2048 frames at. It is read
//     once the game's process group is gone (shaderSpeedGameEnded, from
//     gameDiagLaunch.exited). Threaded video turns that estimate off, and
//     RetroArch gives none before 4096 frames.
//
// Full speed is 97 % of the game's rate (50 for a PAL release of a home
// console that showed no more than that and for the Virtual Boy, else 60) or
// better; a reading over
// 110 % of 60 is fast-forward or a faster screen, not the game, and is
// dropped. Results are kept per game in data/shader-speed.json
// (the frame count is preferred when both exist). The options sheet shows the
// last one on its Shader speed row, and when a game ends with a new verdict a
// toast says it once JukaHub is back on screen.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// shaderSpeedTick spaces the GET_STATUS questions; the flip count is read
	// at each answer.
	shaderSpeedTick    = time.Second
	shaderSpeedAskWait = 400 * time.Millisecond
	// shaderSpeedSkipStart is the loading after a launch, and shaderSpeedSettle
	// the time after RetroArch first says PLAYING; neither is counted.
	shaderSpeedSkipStart = 7 * time.Second
	shaderSpeedSettle    = 2 * time.Second
	// shaderSpeedWant is the clean play a reading from frames takes, and
	// shaderSpeedEnough the least a game that ended sooner still gives one at.
	shaderSpeedWant   = 16 * time.Second
	shaderSpeedEnough = 10 * time.Second
	// shaderSpeedNoFlips is clean play without a single flip: the driver does
	// not pan the framebuffer, so the log is read instead.
	shaderSpeedNoFlips = 6 * time.Second
	// shaderSpeedFramesMax ends a count that never got its clean play (a game
	// paused most of the time).
	shaderSpeedFramesMax = 10 * time.Minute
	// shaderSpeedOrphanWait is how long JukaHub may be back on screen without
	// the game's end being reported (a launch that never started), and
	// shaderSpeedEndWait the same for a game that was seen running: the reaper
	// reports its end only after its other work, a time-lapse film assembled
	// first among it (game_library.go), which can take minutes.
	shaderSpeedOrphanWait = 90 * time.Second
	shaderSpeedEndWait    = 15 * time.Minute
	// shaderSpeedLogNeeds is about how long RetroArch must run before it
	// estimates its frame rate at all (4096 frames, plus loading).
	shaderSpeedLogNeeds = 80 * time.Second
	// shaderSpeedLogTail is how much of the end of RetroArch's log is read.
	shaderSpeedLogTail = 64 << 10
	// The verdict's shares of the game's rate: full speed from 97 %, and
	// past 110 % of the 60 Hz the screens run at it is fast-forward (or a
	// faster screen), not the game.
	shaderSpeedFullShare = 0.97
	shaderSpeedFastShare = 1.10
	shaderSpeedScreenHz  = 60.0
	// shaderSpeedUneven is the frame time deviation (per cent) past which
	// RetroArch's samples hold a pause (one frame seconds long).
	shaderSpeedUneven = 100.0
	// The toast waits this long after the game, and at most the max for the
	// toast before it to go.
	shaderSpeedNoticeAfter = 1500 * time.Millisecond
	shaderSpeedNoticeMax   = 20 * time.Second
)

type shaderSpeedResult struct {
	Mode      string    `json:"mode"`
	FPS       float64   `json:"fps,omitempty"`
	Target    float64   `json:"target,omitempty"`
	Frames    uint64    `json:"frames,omitempty"`
	Seconds   float64   `json:"seconds,omitempty"`
	At        time.Time `json:"at"`
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
	// Method is how the reading was taken: "frames" (page flips counted while
	// the game was played) or "log" (RetroArch's own average); Deviation is a
	// log reading's frame time deviation, per cent. Older entries have neither.
	Method    string  `json:"method,omitempty"`
	Deviation float64 `json:"deviation,omitempty"`
}

// shaderSpeedRun is one launch being measured.
type shaderSpeedRun struct {
	gen      uint64
	rom      string
	mode     string
	port     int // RetroArch's command port; 0 when frames cannot be counted
	launchAt time.Time
	// end gets the launch's RetroArch log once the game is gone.
	end chan string
	// measuring is true until the launch has its result (the store's lock).
	measuring bool
}

var shaderSpeedPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "shader-speed.json")
}

// shaderSpeedStore holds the results and the launch being measured. gen moves
// on with every library launch (shaderSpeedCancel), which ends the check of
// the one before.
var shaderSpeedStore struct {
	sync.Mutex
	loaded  bool
	loading bool
	gen     uint64
	values  map[string]shaderSpeedResult
	active  *shaderSpeedRun
}

// shaderSpeedWriteMu keeps the file writes in order, outside the store's lock.
var shaderSpeedWriteMu sync.Mutex

// shaderSpeedFreezes moves on each time JukaHub pauses a game or gives it
// back (raCoopPause/raCoopResume): a stretch it moved in is not counted.
var shaderSpeedFreezes atomic.Uint64

// shaderSpeedFreezeNote is gamepause_ra.go's hook. Any goroutine.
func shaderSpeedFreezeNote() { shaderSpeedFreezes.Add(1) }

// shaderSpeedHeld reports what keeps the game from playing on its own right
// now: JukaHub's overlay or an alarm over it, the sleep, a quit. Any goroutine.
// The sleep also counts for a moment after the wake: it stops the game
// without raCoopPause, and a suspend can fall wholly between two answers,
// where powerSleepActive alone never shows it.
func shaderSpeedHeld() bool {
	return gameAssistOverlayActive() || alarmFiringActive() || powerSleepActive() ||
		powerSleepJustWoke(3*shaderSpeedTick) || gmQuitting.Load()
}

// ── Results ──────────────────────────────────────────────────────────────

func shaderSpeedReadFile() map[string]shaderSpeedResult {
	values := map[string]shaderSpeedResult{}
	if path := shaderSpeedPath(); path != "" {
		if b, err := os.ReadFile(path); err == nil && len(b) <= 2<<20 {
			_ = json.Unmarshal(b, &values)
		}
	}
	if values == nil { // the file said null
		values = map[string]shaderSpeedResult{}
	}
	return values
}

// shaderSpeedEnsureLoaded reads the results file once. It reads the card, so
// never on the main thread: shaderSpeedPeek starts it on a goroutine.
func shaderSpeedEnsureLoaded() {
	shaderSpeedStore.Lock()
	done := shaderSpeedStore.loaded
	shaderSpeedStore.Unlock()
	if done {
		return
	}
	values := shaderSpeedReadFile()
	shaderSpeedStore.Lock()
	if !shaderSpeedStore.loaded {
		shaderSpeedStore.values, shaderSpeedStore.loaded = values, true
	}
	shaderSpeedStore.Unlock()
}

// shaderSpeedPeek is the main thread's look at a game's result: the stored
// one for this look, and whether a launch of it is being measured now. It
// never touches the card.
func shaderSpeedPeek(rom, mode string) (r shaderSpeedResult, ok, measuring bool) {
	shaderSpeedStore.Lock()
	defer shaderSpeedStore.Unlock()
	if a := shaderSpeedStore.active; a != nil && a.measuring && a.rom == rom && a.mode == mode {
		measuring = true
	}
	if !shaderSpeedStore.loaded {
		if !shaderSpeedStore.loading {
			shaderSpeedStore.loading = true
			go shaderSpeedEnsureLoaded()
		}
		return r, false, measuring
	}
	r, ok = shaderSpeedStore.values[rom]
	return r, ok && r.Mode == mode, measuring
}

// shaderSpeedPrev is the stored result for rom with this look. Off the main
// thread.
func shaderSpeedPrev(rom, mode string) (shaderSpeedResult, bool) {
	shaderSpeedEnsureLoaded()
	shaderSpeedStore.Lock()
	defer shaderSpeedStore.Unlock()
	r, ok := shaderSpeedStore.values[rom]
	return r, ok && r.Mode == mode
}

// shaderSpeedSave stores a result and writes the file. Off the main thread;
// the file is written outside the store's lock, so the options sheet never
// waits for the card.
func shaderSpeedSave(rom string, result shaderSpeedResult) {
	shaderSpeedEnsureLoaded()
	shaderSpeedWriteMu.Lock()
	defer shaderSpeedWriteMu.Unlock()
	shaderSpeedStore.Lock()
	shaderSpeedStore.values[rom] = result
	b, err := json.MarshalIndent(shaderSpeedStore.values, "", "  ")
	shaderSpeedStore.Unlock()
	if err != nil {
		return
	}
	if path := shaderSpeedPath(); path != "" {
		if err := AtomicWrite(path, append(b, '\n'), 0o644); err != nil {
			logWarn("shader", "cannot save the speed check: %v", err)
		}
	}
}

// shaderSpeedHandhelds are systems with no PAL timing: a "(Europe)" game for
// them runs at 60 like any other (extensions, and folder names).
var shaderSpeedHandhelds = map[string]bool{
	"gb": true, "gbc": true, "gba": true, "nds": true, "gg": true, "lnx": true, "lynx": true,
	"ngp": true, "ngc": true, "ngpc": true, "ws": true, "wsc": true, "pce": true, "psp": true,
}

// shaderSpeedTarget is the rate a game is guessed to run at from its name:
// 50 for a PAL release of a home console and for the Virtual Boy (50.27 Hz
// everywhere), else 60.
func shaderSpeedTarget(rom string) float64 {
	name := strings.ToLower(filepath.Base(rom))
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	folder := strings.ToLower(filepath.Base(filepath.Dir(rom)))
	if i := strings.LastIndexByte(folder, '('); i >= 0 && strings.HasSuffix(folder, ")") {
		folder = folder[i+1 : len(folder)-1] // "Game Boy Advance (GBA)"
	}
	if ext == "vb" || folder == "vb" {
		return 50
	}
	if !strings.Contains(name, "(europe)") && !strings.Contains(name, "(pal)") && !strings.Contains(name, "[pal]") {
		return 60
	}
	if shaderSpeedHandhelds[ext] || shaderSpeedHandhelds[folder] {
		return 60
	}
	return 50
}

// shaderSpeedRateFor settles the rate a reading is judged against: a game
// guessed PAL that showed well over 50 frames a second is a 60 Hz one.
func shaderSpeedRateFor(guess, fps float64) float64 {
	if guess < 60 && fps >= (guess+60)/2 {
		return 60
	}
	return guess
}

// shaderSpeedFull is the verdict of an available result.
func shaderSpeedFull(r shaderSpeedResult) bool {
	return r.Available && r.Target > 0 && r.FPS >= r.Target*shaderSpeedFullShare
}

// ── What the player reads ────────────────────────────────────────────────

// shaderSpeedLabel is the options sheet's Shader speed row: its value and
// its line of detail. Main thread.
func shaderSpeedLabel(rom, mode string) (string, string) {
	if !videoFilterIsLook(mode) {
		return "Not a shader", "Choose a look and play to measure its speed"
	}
	r, ok, measuring := shaderSpeedPeek(rom, mode)
	switch {
	case measuring:
		return "Measuring…", "Counting the frames you play; the answer comes when the game ends"
	case !ok, !r.Available && r.Reason == "counter":
		// "counter": left by builds whose check could never read a thing.
		return "Not measured", "Play with this look for a minute to check its speed"
	case !r.Available && r.Reason == "short":
		return "Not measured", "The last game was too short to measure; play a little longer"
	case !r.Available:
		return "Unavailable", shaderSpeedReasonText(r.Reason)
	}
	detail := fmt.Sprintf("%.1f of %.0f FPS over %s of play", r.FPS, r.Target, pluralizeCount(int(math.Round(r.Seconds)), "second", "seconds"))
	if shaderSpeedFull(r) {
		return "Full speed", detail
	}
	return "Below full speed", detail + " · " + shaderSpeedSlowHint(mode)
}

// shaderSpeedSlowHint is what to try when a look is too slow. PT's LCD Dot
// effect costs the most per pixel; its Grid effect is nearly free
// (SkyWalker541's README), so that is the first thing to change.
func shaderSpeedSlowHint(mode string) string {
	if mode == "pt" {
		return "Set Pixel effect to Grid in Settings → Emulation → Tune look, or pick a lighter look"
	}
	return "Sharp or Smooth may keep full speed"
}

// shaderSpeedReasonText says in words why a launch gave no reading.
func shaderSpeedReasonText(reason string) string {
	switch reason {
	case "threaded":
		return "RetroArch's threaded video hides its frame timing"
	case "uneven":
		return "Pauses near the end of play spoiled the reading; play on a while before quitting"
	case "fast":
		return "The screen ran faster than the game, so there is no reading"
	}
	return "Neither the screen nor RetroArch reported the frames played"
}

// shaderSpeedNotice is the toast after a game with a new verdict.
func shaderSpeedNotice(mode string, r shaderSpeedResult) string {
	look := videoFilterModeLabel(mode)
	if shaderSpeedFull(r) {
		return fmt.Sprintf("%s keeps this game at full speed — %.1f of %.0f FPS.", look, r.FPS, r.Target)
	}
	return fmt.Sprintf("With %s this game runs at %.0f of %.0f FPS, below full speed. %s.", look, r.FPS, r.Target, shaderSpeedSlowHint(mode))
}

// shaderSpeedNotify raises the notice once JukaHub is on screen again and the
// toast before it (the while-you-were-playing summary, a CPU limit note) has
// had its time. Its own goroutine; a newer launch drops it.
func shaderSpeedNotify(gen uint64, msg string) {
	time.Sleep(shaderSpeedNoticeAfter)
	deadline := time.Now().Add(shaderSpeedNoticeMax)
	for {
		if !shaderSpeedCurrent(gen) {
			return
		}
		if (!gameScreenSuspended() && shaderSpeedToastFree()) || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if gameScreenSuspended() {
		return
	}
	ShowToast(msg, ToastKindInfo)
}

// shaderSpeedToastFree reports whether no toast is on screen now.
func shaderSpeedToastFree() bool {
	currentToastMu.Lock()
	defer currentToastMu.Unlock()
	if strings.TrimSpace(currentToast.Message) == "" || currentToast.Duration == 0 {
		return true
	}
	return sdl.GetTicks64()-currentToast.StartedAt > currentToast.Duration
}

// ── Launch and end ───────────────────────────────────────────────────────

// prepareShaderSpeedCheck runs after prepareVideoFilter confirmed a look was
// applied (game_library.go). Where frames can be counted it scopes
// RetroArch's command port on for this launch; the check itself runs on a
// goroutine until the game is gone.
func prepareShaderSpeedCheck(launcher, rom string) {
	mode := videoFilterLaunchMode(rom)
	if !videoFilterIsLook(mode) || !retroArchLauncher(launcher) {
		return
	}
	port := 0
	if shaderSpeedFramesPossible() {
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
	}
	shaderSpeedStart(rom, mode, port)
}

func shaderSpeedStart(rom, mode string, port int) {
	run := &shaderSpeedRun{rom: rom, mode: mode, port: port, launchAt: time.Now(), end: make(chan string, 1), measuring: true}
	shaderSpeedStore.Lock()
	shaderSpeedStore.gen++
	run.gen = shaderSpeedStore.gen
	shaderSpeedStore.active = run
	shaderSpeedStore.Unlock()
	go shaderSpeedWatch(run)
}

// shaderSpeedCancel ends the check of the last launch: every library launch
// calls it first (game_library.go), before anything empties RetroArch's log.
func shaderSpeedCancel() uint64 {
	shaderSpeedStore.Lock()
	shaderSpeedStore.gen++
	shaderSpeedStore.active = nil
	gen := shaderSpeedStore.gen
	shaderSpeedStore.Unlock()
	return gen
}

func shaderSpeedCurrent(gen uint64) bool {
	shaderSpeedStore.Lock()
	defer shaderSpeedStore.Unlock()
	return shaderSpeedStore.gen == gen
}

// shaderSpeedDone marks run's result as in.
func shaderSpeedDone(run *shaderSpeedRun) {
	shaderSpeedStore.Lock()
	run.measuring = false
	shaderSpeedStore.Unlock()
}

// shaderSpeedGameEnded is gamediag.go's hook (gameDiagLaunch.exited, on the
// reaper goroutine once the game's process group is gone): the launch of rom
// that started at started is over, and logPath is its RetroArch log ("" when
// none was set up). A port, a game without a look and an older launch of the
// same game are no one's.
func shaderSpeedGameEnded(rom, logPath string, started time.Time) {
	shaderSpeedStore.Lock()
	run := shaderSpeedStore.active
	shaderSpeedStore.Unlock()
	if run == nil || rom == "" || run.rom != rom {
		return
	}
	// prepareGameDiag runs just after prepareShaderSpeedCheck in the same
	// launch; an older launch of the same game started before this one.
	if started.Before(run.launchAt) || started.Sub(run.launchAt) > time.Minute {
		return
	}
	select {
	case run.end <- logPath:
	default:
	}
}

// ── The check ────────────────────────────────────────────────────────────

// shaderSpeedWatch is one launch's check, on its own goroutine: the frame
// count while the game plays, then RetroArch's log if the count gave nothing,
// then the result and the notice.
func shaderSpeedWatch(run *shaderSpeedRun) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("shader", "the speed check stopped: %v", r)
		}
	}()
	prev, hadPrev := shaderSpeedPrev(run.rom, run.mode)
	guess := shaderSpeedTarget(run.rom)
	fr := &shaderSpeedFrames{run: run, guess: guess, on: run.port > 0, freezes: shaderSpeedFreezes.Load()}
	defer fr.stop()
	ticker := time.NewTicker(shaderSpeedTick)
	defer ticker.Stop()
	logPath, ended, ran := "", false, false
	var backAt time.Time
watch:
	for {
		select {
		case logPath = <-run.end:
			ended = true
			break watch
		case <-ticker.C:
		}
		if !shaderSpeedCurrent(run.gen) {
			return
		}
		now := time.Now()
		suspended := gameScreenSuspended()
		ran = ran || suspended
		if suspended || now.Sub(run.launchAt) < shaderSpeedSkipStart {
			backAt = time.Time{}
		} else if backAt.IsZero() {
			backAt = now
		} else if back := now.Sub(backAt); back > shaderSpeedOrphanWait && (!ran || back > shaderSpeedEndWait) {
			logInfo("shader", "no end of %s was reported; the speed check closes without RetroArch's log", filepath.Base(run.rom))
			break watch
		}
		if fr.on {
			fr.tick(now)
		}
	}
	fr.stop()
	if !shaderSpeedCurrent(run.gen) {
		return
	}
	lasted := time.Since(run.launchAt)
	result := shaderSpeedResult{Mode: run.mode, Target: guess, At: time.Now()}
	switch {
	case fr.result != nil:
		result = *fr.result // stored when the count finished
	case fr.seconds >= shaderSpeedEnough.Seconds():
		result = fr.reading()
	case !ended:
		// The game's end never came (a launch that did not start): nothing
		// to say about it.
		shaderSpeedDone(run)
		return
	default:
		reading, found := shaderSpeedLogReading{}, false
		if text, ok := shaderSpeedReadLog(logPath, run.gen); ok {
			reading, found = shaderSpeedParseLog(text)
		}
		fps, rate, reason := shaderSpeedLogFPS(reading, found, guess)
		switch {
		case reason == "":
			result.FPS, result.Target, result.Available, result.Method = fps, rate, true, "log"
			result.Frames = uint64(reading.Samples)
			result.Seconds = float64(reading.Samples) / fps
			result.Deviation = reading.Deviation
		case fr.flips > 0 || (run.port > 0 && !fr.ruledOut && (!fr.playing.IsZero() || lasted < shaderSpeedLogNeeds)):
			// The screen's frames were counting, or could have: a longer
			// game gives a reading. A RetroArch that never once answered
			// PLAYING in a long game (a build without GET_STATUS, a command
			// port it does not read) will not in a longer one either.
			result.Reason = "short"
		case reason == "threaded":
			result.Reason = reason // said whatever the game's length
		case lasted < shaderSpeedLogNeeds:
			result.Reason = "short" // too few frames for RetroArch's estimate
		default:
			result.Reason = reason
		}
	}
	if fr.result == nil {
		// A launch that gave nothing keeps the reading an earlier one gave.
		if result.Available || !hadPrev || !prev.Available {
			shaderSpeedSave(run.rom, result)
		}
	}
	shaderSpeedDone(run)
	if !result.Available {
		logInfo("shader", "%s with %s: no speed reading (%s)", filepath.Base(run.rom), run.mode, result.Reason)
		return
	}
	if fr.result == nil {
		logInfo("shader", "%s with %s: %.1f of %.0f FPS (%s)", filepath.Base(run.rom), run.mode, result.FPS, result.Target, result.Method)
	}
	if !hadPrev || !prev.Available || shaderSpeedFull(prev) != shaderSpeedFull(result) {
		shaderSpeedNotify(run.gen, shaderSpeedNotice(run.mode, result))
	}
}

// shaderSpeedFrames is the played-frame count of one launch. The watch
// goroutine only.
type shaderSpeedFrames struct {
	run     *shaderSpeedRun
	guess   float64 // shaderSpeedTarget
	on      bool    // still counting
	counter *shaderFlipCounter
	playing time.Time // when RetroArch first said PLAYING
	freezes uint64    // shaderSpeedFreezes at the last answer
	// state: 0 not in clean play, 1 the fast poll just started, 2 a stretch
	// is open since at, when the counter stood at mark.
	state   int
	at      time.Time
	mark    uint64
	flips   uint64  // flips in the clean stretches
	seconds float64 // and their length
	fast    float64 // seconds of stretches dropped as faster than the screen
	result  *shaderSpeedResult
	// ruledOut: this screen cannot be counted (no framebuffer, no flips, or
	// flips faster than any game), so only RetroArch's log can tell.
	ruledOut bool
}

func (fr *shaderSpeedFrames) stop() {
	fr.on = false
	fr.counter.close()
	fr.counter = nil
}

// reading is the result of the stretches counted so far.
func (fr *shaderSpeedFrames) reading() shaderSpeedResult {
	fps := float64(fr.flips) / fr.seconds
	return shaderSpeedResult{
		Mode:      fr.run.mode,
		FPS:       fps,
		Target:    shaderSpeedRateFor(fr.guess, fps),
		Frames:    fr.flips,
		Seconds:   fr.seconds,
		At:        time.Now(),
		Available: true,
		Method:    "frames",
	}
}

// tick is one GET_STATUS answer: the end of a stretch, and the start of the
// next one.
func (fr *shaderSpeedFrames) tick(now time.Time) {
	since := now.Sub(fr.run.launchAt)
	if since < shaderSpeedSkipStart {
		return
	}
	if since > shaderSpeedFramesMax || !gameScreenSuspended() {
		fr.stop()
		return
	}
	reply, err := askRetroArchNetCmd(fr.run.port, "GET_STATUS", shaderSpeedAskWait)
	playing := err == nil && raStatusWord(reply) == "PLAYING"
	if playing && fr.playing.IsZero() {
		fr.playing = time.Now()
	}
	freezes := shaderSpeedFreezes.Load()
	clean := playing && freezes == fr.freezes && !shaderSpeedHeld() && time.Since(fr.playing) >= shaderSpeedSettle
	fr.freezes = freezes
	if !clean {
		fr.state = 0
		fr.counter.setFast(false)
		return
	}
	if fr.counter == nil {
		c, err := shaderSpeedFlipsOpen()
		if err != nil {
			logInfo("shader", "cannot count the screen's frames (%v); the speed check reads RetroArch's log instead", err)
			fr.stop()
			fr.ruledOut = true
			return
		}
		fr.counter = c
	}
	fr.counter.setFast(true)
	flips, at := fr.counter.count(), time.Now()
	if !fr.counter.alive() {
		// The poll ended on its own (the driver refused the ioctl): its count
		// stands still, and this stretch would read as frames not shown. What
		// was counted before stays.
		logInfo("shader", "the screen's frame counter stopped; the speed check keeps %.0f s counted before it", fr.seconds)
		fr.ruledOut = fr.flips == 0
		fr.stop()
		return
	}
	switch fr.state {
	case 0:
		// The fast poll starts now: this stretch is not counted yet.
		fr.state = 1
		return
	case 1:
		fr.state = 2
	default:
		secs := at.Sub(fr.at).Seconds()
		delta := flips - fr.mark
		switch {
		case secs <= 0:
		case float64(delta)/secs <= shaderSpeedScreenHz*shaderSpeedFastShare:
			fr.flips += delta
			fr.seconds += secs
		default:
			fr.fast += secs // fast-forward
		}
	}
	fr.mark, fr.at = flips, at
	if fr.flips == 0 && (fr.seconds >= shaderSpeedNoFlips.Seconds() || fr.fast >= 2*shaderSpeedNoFlips.Seconds()) {
		// Not one flip, or only ever more than the screen shows: this driver
		// does not pan once per frame.
		logInfo("shader", "the screen's page flips do not follow the game (%.0f s counted, %.0f s too fast); the speed check reads RetroArch's log instead", fr.seconds, fr.fast)
		fr.seconds = 0
		fr.stop()
		fr.ruledOut = true
		return
	}
	if fr.seconds >= shaderSpeedWant.Seconds() {
		r := fr.reading()
		fr.stop()
		fr.result = &r
		shaderSpeedSave(fr.run.rom, r)
		shaderSpeedDone(fr.run)
		logInfo("shader", "%s with %s: %.1f of %.0f FPS (frames)", filepath.Base(fr.run.rom), fr.run.mode, r.FPS, r.Target)
	}
}

// ── RetroArch's log ──────────────────────────────────────────────────────

// shaderSpeedReadLog reads the end of the finished game's RetroArch log.
func shaderSpeedReadLog(path string, gen uint64) (string, bool) {
	if path == "" || !shaderSpeedCurrent(gen) {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false
	}
	if size := info.Size(); size > shaderSpeedLogTail {
		if _, err := f.Seek(size-shaderSpeedLogTail, io.SeekStart); err != nil {
			return "", false
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, shaderSpeedLogTail))
	if err != nil {
		return "", false
	}
	// The next library launch empties this log (prepareGameDiag) only after
	// it has moved the generation on (shaderSpeedCancel comes first in
	// launchLibraryGame): still current now, the text read is this game's.
	if !shaderSpeedCurrent(gen) {
		return "", false
	}
	return string(data), true
}

// shaderSpeedLogReading is what RetroArch's log says about the frame timing
// of the game it closed: the average rate it presented frames at, how uneven
// they were (per cent) and over how many, or why it measured nothing.
type shaderSpeedLogReading struct {
	Hz        float64
	Deviation float64
	Samples   int
	Reason    string // "threaded" or "short" when there is no rate
}

// shaderSpeedHzMarker starts RetroArch's estimate, in any of its prefixes
// ("[INFO] [Video]: ", "RetroArch [INFO] :: ", none).
const shaderSpeedHzMarker = "average monitor hz:"

// shaderSpeedParseLog finds RetroArch's frame-timing estimate in a log. When
// it gave several (a driver restart), the one over the most samples wins, the
// last on a tie. Pure.
func shaderSpeedParseLog(text string) (shaderSpeedLogReading, bool) {
	var best shaderSpeedLogReading
	found := false
	reason := ""
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(line)
		if i := strings.Index(lower, shaderSpeedHzMarker); i >= 0 {
			if r, ok := shaderSpeedParseHz(lower[i+len(shaderSpeedHzMarker):]); ok && (!found || r.Samples >= best.Samples) {
				best, found = r, true
			}
			continue
		}
		switch {
		case strings.Contains(lower, "monitor fps estimation is disabled for threaded video"):
			reason = "threaded"
		case strings.Contains(lower, "does not have enough samples for monitor refresh rate estimation"):
			reason = "short"
		}
	}
	if found {
		return best, true
	}
	if reason != "" {
		return shaderSpeedLogReading{Reason: reason}, true
	}
	return shaderSpeedLogReading{}, false
}

// shaderSpeedParseHz reads " 59.941540 hz. (0.512 % frame time deviation,
// 2048 samples)." (lower case; newer builds say "based on 2048 last
// samples"). The rate is required, the rest optional. Pure.
func shaderSpeedParseHz(rest string) (shaderSpeedLogReading, bool) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return shaderSpeedLogReading{}, false
	}
	hz, err := strconv.ParseFloat(strings.TrimSuffix(fields[0], "hz"), 64)
	if err != nil || math.IsNaN(hz) || hz <= 0 || hz > 1000 {
		return shaderSpeedLogReading{}, false
	}
	r := shaderSpeedLogReading{Hz: hz}
	open := strings.IndexByte(rest, '(')
	if open < 0 {
		return r, true
	}
	inner := rest[open+1:]
	if end := strings.IndexByte(inner, ')'); end >= 0 {
		inner = inner[:end]
	}
	words := strings.Fields(strings.NewReplacer("%", " ", ",", " ").Replace(inner))
	if len(words) > 0 {
		if dev, err := strconv.ParseFloat(words[0], 64); err == nil && dev >= 0 && !math.IsInf(dev, 0) {
			r.Deviation = dev
		}
	}
	past := false
	for _, w := range words {
		if w == "deviation" {
			past = true
			continue
		}
		if past {
			if n, err := strconv.Atoi(w); err == nil && n > 0 {
				r.Samples = n
				break
			}
		}
	}
	return r, true
}

// shaderSpeedLogFPS turns RetroArch's estimate into played frames per second
// and the rate they are judged against, or says why it cannot ("" when it
// can). guess is shaderSpeedTarget's.
//
// The estimate is the mean of the last 2048 frame times, and a pause in them
// (JukaHub's menu over the game, the game given back) is one frame seconds
// long: it drags the mean down and shows as a deviation far past any real
// stutter. One such frame is taken back out — with n samples, mean m and
// standard deviation s, n−1 equal frames and one long one put the others at
// m − s/√n — and a corrected rate is believed only when it says full speed:
// more than one pause is under-corrected, so a lower one proves nothing.
func shaderSpeedLogFPS(r shaderSpeedLogReading, found bool, guess float64) (fps, rate float64, reason string) {
	if !found {
		return 0, guess, "none"
	}
	if r.Hz <= 0 {
		if r.Reason != "" {
			return 0, guess, r.Reason
		}
		return 0, guess, "none"
	}
	fps = r.Hz
	if r.Deviation > shaderSpeedUneven {
		n := r.Samples
		if n < 2 {
			n = 2048
		}
		m := 1 / r.Hz
		a := m - (r.Deviation/100)*m/math.Sqrt(float64(n))
		if a <= 0 {
			return 0, guess, "uneven"
		}
		fps = 1 / a
		if fps < shaderSpeedRateFor(guess, fps)*shaderSpeedFullShare {
			return 0, guess, "uneven"
		}
	}
	if fps > shaderSpeedScreenHz*shaderSpeedFastShare {
		return 0, guess, "fast"
	}
	return fps, shaderSpeedRateFor(guess, fps), ""
}
