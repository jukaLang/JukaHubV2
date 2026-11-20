package main

// gamelab_measure.go — what one Game Lab run measured: the frames the game
// put on screen, the sound glitches RetroArch logged, and the battery's draw.
//
// Frames. The shader speed check (shader_speed.go, shader_speed_linux.go)
// already counts the frames a RetroArch game really showed on the handheld:
// framebuffer page flips between two GET_STATUS answers that both say
// PLAYING, leaving out JukaHub's pauses, overlays, the sleep, loading and
// fast-forward. The Lab counts the same way with the same parts (the flip
// counter, the command port, the pause and hold signals), but over the whole
// run rather than sixteen seconds, and it also notes the seconds that fell
// under 90 % of the game's rate, which is what "smoother" means in the
// verdict. It does not use the shader check's own counter, whose reading is
// stored as the shader's. Where frames cannot be counted (the PC, a driver
// that does not pan) RetroArch's "Average monitor Hz" line in its log of the
// launch is read instead, exactly as the shader check reads it. A standalone
// emulator answers neither: its speed is "not measured", never guessed.
//
// Sound. Every library launch of RetroArch writes its log into JukaHub's data
// folder (gamediag.go). RetroArch's sound drivers recover from an underrun
// quietly in most builds (snd_pcm_recover with its silent flag); what does
// reach a log is counted: ALSA's own "underrun occurred" (alsa-lib prints it
// on the launch script's output, which JukaHub keeps), a driver's "Failed to
// recover" lines, and "Audio backend failed to write. Will continue without
// sound", which ends the game's sound. Builds that report audio buffer
// statistics at close ("Amount of time spent close to underrun: N %": older
// releases; current RetroArch prints it in debug builds only) give a share
// too. So zero glitches reads "none logged", not "none happened".
//
// Battery. Every few seconds while the game runs, after its loading, the
// battery's level and draw are read from sysfs: power_now, else current_now
// times voltage_now, each read in the unit that gives a draw a handheld can
// have (drivers disagree between µ and m). On the charger, or with no battery
// (a desktop), it is "not measured".

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// gameLabFramesMax ends the frame count of a long run.
	gameLabFramesMax = 10 * time.Minute
	// gameLabFramesEnough is the clean play a frame count needs to stand.
	gameLabFramesEnough = 10 * time.Second
	// gameLabSlowShare: a counted second under this share of the game's rate
	// is a slow one.
	gameLabSlowShare = 0.90
	// The battery is read every gameLabBatteryEvery from gameLabBatterySkip
	// after the start; gameLabBatteryMin readings make a draw, and a level
	// read gameLabLevelSpan apart that fell gameLabLevelDrop points a rate.
	gameLabBatterySkip  = 15 * time.Second
	gameLabBatteryEvery = 5 * time.Second
	gameLabBatteryMin   = 3
	gameLabBatteryKeep  = 2048
	gameLabLevelSpan    = 2 * time.Minute
	gameLabLevelDrop    = 2
	// gameLabWatchMax ends a watch whose game's end never came.
	gameLabWatchMax = 12 * time.Hour
	// gameLabLogTail is how much of the end of a log is read.
	gameLabLogTail = 512 << 10
)

// gameLabWatch measures one run on its own goroutine until the game's end,
// then stores the result.
func gameLabWatch(run *gameLabRun) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("gamelab", "the Lab run stopped: %v", r)
			gameLabClear(run)
		}
	}()
	guess := shaderSpeedTarget(run.game.Path)
	fr := &gameLabFrames{port: run.port, guess: guess, on: run.port > 0, launchAt: run.launchAt, freezes: shaderSpeedFreezes.Load()}
	defer fr.stop()
	bat := &gameLabBattery{}
	ticker := time.NewTicker(shaderSpeedTick)
	defer ticker.Stop()
	limit := time.NewTimer(gameLabWatchMax)
	defer limit.Stop()
	var end gameLabEnd
	var goneAt time.Time
	ended := false
watch:
	for {
		select {
		case end = <-run.end:
			ended = true
			break watch
		case <-limit.C:
			break watch
		case <-ticker.C:
		}
		now := time.Now()
		if !goneAt.IsZero() {
			continue
		}
		if gameScreenSuspended() {
			fr.tick(now)
			bat.tick(now, run.startAt)
		} else {
			// The game's process group is gone (screenChildReaped): its end,
			// by this clock; the reaper reports it only after its other work
			// (a time-lapse can take minutes). Nothing is measured after it:
			// a game started meanwhile is not this run's.
			goneAt = now
			fr.stop()
		}
	}
	fr.stop()
	if !ended {
		gameLabClear(run)
		return
	}
	played := end.ranFor
	if !goneAt.IsZero() && goneAt.After(run.startAt) {
		played = goneAt.Sub(run.startAt)
	}
	result := gameLabResult{Played: math.Round(played.Seconds()), Short: played < gameLabShortRun}
	gameLabFillSpeed(&result, fr, run, end, guess)
	gameLabFillAudio(&result, run, end)
	bat.fill(&result)
	if run.retro && end.raLog != "" {
		if m := gameDiagLogCoreRe.FindStringSubmatch(end.raLog); m != nil {
			result.Core, result.CoreVersion = parseCoreFile(filepath.Base(strings.ReplaceAll(strings.TrimSpace(m[1]), `\`, "/")))
		}
	}
	logInfo("gamelab", "run %s of %s: %.0f s played, %.1f fps (%s), %d sound glitches, %.2f W",
		strings.ToUpper(run.side), filepath.Base(run.game.Path), result.Played, result.FPS, result.FPSMethod, result.Glitches, result.Watts)
	gameLabFinish(run, result)
}

// ── Frames ───────────────────────────────────────────────────────────────

// gameLabFrames is one run's played-frame count. The watch goroutine only.
type gameLabFrames struct {
	port     int
	guess    float64
	launchAt time.Time
	on       bool
	counter  *shaderFlipCounter
	playing  time.Time
	freezes  uint64
	// state: 0 not in clean play, 1 the fast poll just started, 2 a stretch
	// is open since at, when the counter stood at mark.
	state    int
	at       time.Time
	mark     uint64
	flips    uint64
	seconds  float64
	slow     float64
	fast     float64
	ruledOut bool
}

func (fr *gameLabFrames) stop() {
	fr.on = false
	fr.counter.close()
	fr.counter = nil
}

// idle slows the poll while the count is not used.
func (fr *gameLabFrames) idle() {
	fr.state = 0
	fr.counter.setFast(false)
}

// tick is one GET_STATUS answer: the end of a stretch, and the start of the
// next one (shaderSpeedFrames.tick, counted for the whole run).
func (fr *gameLabFrames) tick(now time.Time) {
	if !fr.on {
		return
	}
	since := now.Sub(fr.launchAt)
	if since < shaderSpeedSkipStart {
		return
	}
	if since > gameLabFramesMax {
		fr.stop()
		return
	}
	reply, err := askRetroArchNetCmd(fr.port, "GET_STATUS", shaderSpeedAskWait)
	playing := err == nil && raStatusWord(reply) == "PLAYING"
	if playing && fr.playing.IsZero() {
		fr.playing = time.Now()
	}
	freezes := shaderSpeedFreezes.Load()
	clean := playing && freezes == fr.freezes && !shaderSpeedHeld() && time.Since(fr.playing) >= shaderSpeedSettle
	fr.freezes = freezes
	if !clean {
		fr.idle()
		return
	}
	if fr.counter == nil {
		c, err := shaderSpeedFlipsOpen()
		if err != nil {
			logInfo("gamelab", "cannot count the screen's frames (%v); RetroArch's log is read instead", err)
			fr.stop()
			fr.ruledOut = true
			return
		}
		fr.counter = c
	}
	fr.counter.setFast(true)
	flips, at := fr.counter.count(), time.Now()
	if !fr.counter.alive() {
		fr.ruledOut = fr.flips == 0
		fr.stop()
		return
	}
	switch fr.state {
	case 0:
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
			if float64(delta)/secs < fr.guess*gameLabSlowShare {
				fr.slow += secs
			}
		default:
			fr.fast += secs // fast-forward
		}
	}
	fr.mark, fr.at = flips, at
	if fr.flips == 0 && (fr.seconds >= shaderSpeedNoFlips.Seconds() || fr.fast >= 2*shaderSpeedNoFlips.Seconds()) {
		// This driver does not pan once per frame.
		fr.seconds, fr.slow = 0, 0
		fr.stop()
		fr.ruledOut = true
	}
}

// gameLabFillSpeed sets the run's speed: the frame count when it got enough
// clean play, else RetroArch's own average from its log, else why neither.
func gameLabFillSpeed(r *gameLabResult, fr *gameLabFrames, run *gameLabRun, end gameLabEnd, guess float64) {
	if fr.seconds >= gameLabFramesEnough.Seconds() && fr.flips > 0 {
		fps := float64(fr.flips) / fr.seconds
		r.FPS, r.Target, r.FPSMethod = round1(fps), shaderSpeedRateFor(guess, fps), "frames"
		r.FPSSeconds = math.Round(fr.seconds)
		r.SlowShare = math.Round(fr.slow/fr.seconds*1000) / 1000
		return
	}
	if !run.retro {
		r.FPSWhy = "standalone"
		return
	}
	reading, found := shaderSpeedParseLog(end.raLog)
	fps, rate, reason := shaderSpeedLogFPS(reading, found, guess)
	if reason == "" {
		r.FPS, r.Target, r.FPSMethod = round1(fps), rate, "log"
		if fps > 0 {
			r.FPSSeconds = math.Round(float64(reading.Samples) / fps)
		}
		return
	}
	r.FPSWhy = reason
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// ── Sound ────────────────────────────────────────────────────────────────

// gameLabAudioCount reads a log for sound trouble: the glitch lines, whether
// the sound was given up, and the "close to underrun" share when the build
// reports one. Pure.
func gameLabAudioCount(text string) (glitches int, lost bool, underrun float64, hasUnderrun bool) {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		lower := strings.ToLower(line)
		if i := strings.Index(lower, "close to underrun:"); i >= 0 {
			fields := strings.Fields(lower[i+len("close to underrun:"):])
			if len(fields) > 0 {
				if v, err := strconv.ParseFloat(strings.TrimSuffix(fields[0], "%"), 64); err == nil && v >= 0 && v <= 100 {
					underrun, hasUnderrun = v, true
				}
			}
			continue
		}
		switch {
		case strings.Contains(lower, "continue without sound"):
			lost = true
		case strings.Contains(lower, "underrun"), strings.Contains(lower, "xrun"),
			strings.Contains(lower, "alsa") && strings.Contains(lower, "recover"):
			glitches++
		}
	}
	return glitches, lost, underrun, hasUnderrun
}

// gameLabFillAudio sets the run's sound figures from its logs.
func gameLabFillAudio(r *gameLabResult, run *gameLabRun, end gameLabEnd) {
	if !run.retro {
		r.AudioWhy = "standalone"
		return
	}
	if end.raLog == "" {
		r.AudioWhy = "nolog"
		return
	}
	r.Audio = true
	r.Glitches, r.SoundLost, r.UnderrunPct, r.HasUnderrun = gameLabAudioCount(end.raLog)
	if end.outLog != "" {
		g, lost, _, _ := gameLabAudioCount(end.outLog)
		r.Glitches += g
		r.SoundLost = r.SoundLost || lost
	}
}

// gameLabReadLog reads the end of a log written for the launch that started
// at since; "" when there is none or it is older.
func gameLabReadLog(path string, since time.Time) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 || info.ModTime().Before(since.Add(-2*time.Second)) {
		return ""
	}
	if size := info.Size(); size > gameLabLogTail {
		if _, err := f.Seek(size-gameLabLogTail, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, gameLabLogTail))
	if err != nil {
		return ""
	}
	return string(data)
}

// ── Battery ──────────────────────────────────────────────────────────────

// gameLabBattery samples the battery while a run plays. The watch goroutine
// only.
type gameLabBattery struct {
	tried    bool
	dir      string
	next     time.Time
	charging bool
	firstPct int
	lastPct  int
	firstAt  time.Time
	lastAt   time.Time
	watts    []float64
}

func (b *gameLabBattery) tick(now, startAt time.Time) {
	if now.Sub(startAt) < gameLabBatterySkip || now.Before(b.next) {
		return
	}
	b.next = now.Add(gameLabBatteryEvery)
	if !b.tried {
		b.tried, b.dir = true, batterySysfsDir()
	}
	if b.dir == "" {
		return
	}
	if batteryChargingIn(powerSupplyRoot) {
		b.charging = true
		return
	}
	if pct := batteryPercentIn(b.dir); pct >= 0 {
		if b.firstAt.IsZero() {
			b.firstPct, b.firstAt = pct, now
		}
		b.lastPct, b.lastAt = pct, now
	}
	if w, ok := gameLabWattsIn(b.dir); ok && len(b.watts) < gameLabBatteryKeep {
		b.watts = append(b.watts, w)
	}
}

// fill sets the run's battery figures, or why there are none.
func (b *gameLabBattery) fill(r *gameLabResult) {
	switch {
	case b.charging:
		r.BatteryWhy = "charging"
		return
	case !b.tried:
		r.BatteryWhy = "short"
		return
	case b.dir == "":
		r.BatteryWhy = "nobattery"
		return
	}
	if len(b.watts) >= gameLabBatteryMin {
		sorted := append([]float64(nil), b.watts...)
		sort.Float64s(sorted)
		r.Watts = math.Round(sorted[len(sorted)/2]*100) / 100
	}
	if span := b.lastAt.Sub(b.firstAt); span >= gameLabLevelSpan && b.firstPct-b.lastPct >= gameLabLevelDrop {
		r.PctPerHour = round1(float64(b.firstPct-b.lastPct) / span.Hours())
	}
	if r.Watts == 0 && r.PctPerHour == 0 {
		r.BatteryWhy = "short"
	}
}

// gameLabWattsIn is the battery's draw now, in watts: power_now, else
// current times voltage.
func gameLabWattsIn(dir string) (float64, bool) {
	read := func(names ...string) float64 {
		for _, name := range names {
			if v, ok := readBatteryCounter(filepath.Join(dir, name)); ok && v != 0 {
				return math.Abs(float64(v))
			}
		}
		return 0
	}
	if p := read("power_now", "power_avg"); p > 0 {
		for _, scale := range []float64{1e6, 1e3} {
			if w := p / scale; gameLabWattsPlausible(w) {
				return w, true
			}
		}
	}
	return gameLabWattsFrom(read("current_now", "current_avg"), read("voltage_now", "voltage_avg"))
}

// gameLabWattsFrom is current times voltage in the units (µ or m) that give a
// battery voltage and a draw a handheld can have. Pure.
func gameLabWattsFrom(current, voltage float64) (float64, bool) {
	if current <= 0 || voltage <= 0 {
		return 0, false
	}
	for _, vs := range []float64{1e6, 1e3} {
		v := voltage / vs
		if v < 2.8 || v > 5.0 {
			continue
		}
		for _, is := range []float64{1e6, 1e3} {
			if w := v * current / is; gameLabWattsPlausible(w) {
				return w, true
			}
		}
	}
	return 0, false
}

// gameLabWattsPlausible bounds a handheld's draw while a game plays.
func gameLabWattsPlausible(w float64) bool { return w >= 0.2 && w <= 30 }
