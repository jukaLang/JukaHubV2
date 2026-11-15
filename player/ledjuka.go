package main

// ledjuka.go — the stick lights show Juka.
//
// Whatever the lights normally show (a colour, the music, the battery…), they
// turn into Juka's while the voice orb or the in-game assistant is up: cyan and
// brighter with the user's voice while Juka listens, a slow violet breath while
// it thinks, and blue following Juka's own voice while it answers (amber when
// the answer is a problem, like the orb). The colours are the orb's own
// (orbLights in voiceorb.go), so the sticks and the screen agree. When the orb
// goes the lights ease back to the user's mode within about half a second.
//
// The TrimUI rings re-arm their effect on a hue change, which shows as a
// blink, while the level moves smoothly (ledanim.go). So the layer never
// changes hue on a lit ring: the shown colour fades to dark, the hue switches
// there, and the new colour fades up (ledJukaFrame). Within one Juka phase only
// the level moves.
//
// "React to Juka" (Settings → Stick lights) is Always (the default: the
// lights wake for Juka even when they are off), When lights are on, or Off.
// With the lights off, ledJukaTick starts the LED loop itself for Juka alone
// (ledJukaOnly: the user's mode is dark and the lights' microphone stays shut)
// and stops it shortly after Juka is done; stopping is the normal path, so the
// rings get the firmware's own colour back exactly as when the switch goes off.
//
// Over a game (backgroundPaused) the loop normally hands the lights back to the
// firmware; while the in-game assistant shows Juka it keeps drawing the layer,
// and hands them back once the layer has faded out.
//
// voiceOrb and the config are main-thread only: ledJukaTick reads them once a
// frame and publishes one small sample (ledJukaBus) the LED goroutine reads.

import (
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// ledKeyJuka is the custom key behind Settings → Stick lights → React to
	// Juka: ledJukaAlways (default), ledJukaWhenOn or ledJukaOff.
	ledKeyJuka    = "led_juka"
	ledJukaAlways = "always"
	ledJukaWhenOn = "on"
	ledJukaOff    = "off"

	// ledJukaStaleAfter is how long a published sample counts: the main loop
	// can run at 5 fps on a busy page, and a stalled one must not leave the
	// sticks on Juka's colour.
	ledJukaStaleAfter = 1500 * time.Millisecond
	// ledJukaReleaseAfter is how long after Juka is done a loop started for
	// Juka alone keeps running: the fade back (two dips) has to finish first.
	ledJukaReleaseAfter = 700 * time.Millisecond
	// ledJukaDipSec is one half of a hue change: the old colour fades out over
	// it, the new one fades in over it.
	ledJukaDipSec = 0.22
	// ledJukaRetryAfter is the pause after the lights would not start for
	// Juka (a device without them), so it is not retried every frame.
	ledJukaRetryAfter = time.Minute
	// ledJukaThinkPeriodMs is one slow breath while Juka thinks.
	ledJukaThinkPeriodMs = 2800
	// ledJukaListenRest and ledJukaSpeakRest are the glow in silence while
	// Juka listens and while it speaks; a voice brightens it to full.
	ledJukaListenRest = 0.3
	ledJukaSpeakRest  = 0.35
	// ledJukaShownLevel is the steady glow while an answer or a choice is up.
	ledJukaShownLevel = 0.6
	// ledJukaMinPeak is the dimmest channel peak the layer lights (Juka's
	// colours, and any colour it fades): below it the rounded channels lose the
	// hue (cyan at a peak of 2 reads as pure aqua), and the TrimUI rings would
	// arm that wrong hue at the bottom of a dip, then re-arm the right one — a
	// blink — 300 ms later (ledAnimHueTolerance in ledanim.go). At a peak of 7
	// a channel's rounding moves the hue by at most 36 of the 40 allowed.
	ledJukaMinPeak = 7
	// ledJukaModeRefresh is how often the tick re-reads React to Juka: unset,
	// the lookup misses and scans the whole Custom map twice.
	ledJukaModeRefresh = 500 * time.Millisecond
)

// ledJukaPhase is what Juka is doing, as far as the lights care.
type ledJukaPhase int

const (
	ledJukaIdle      ledJukaPhase = iota
	ledJukaListening              // the orb has the microphone
	ledJukaThinking               // recognising, or Juka is writing
	ledJukaSpeaking               // Juka's voice is playing
	ledJukaShown                  // an answer, a choice or a confirm is up, silent
)

// What the layer shows: the user's own mode, or one of Juka's hues.
const (
	ledJukaSrcUser = iota
	ledJukaSrcCyan
	ledJukaSrcViolet
	ledJukaSrcBlue
	ledJukaSrcAmber
)

// ledJukaHues are the orb's colours at full value, by source.
var ledJukaHues = [...][3]uint8{
	ledJukaSrcUser:   {},
	ledJukaSrcCyan:   {0, 210, 255},
	ledJukaSrcViolet: {156, 96, 255},
	ledJukaSrcBlue:   {64, 118, 255},
	ledJukaSrcAmber:  {255, 136, 24},
}

// ledJukaSrcFor is the hue a phase shows. Pure.
func ledJukaSrcFor(phase ledJukaPhase, warn bool) int {
	switch phase {
	case ledJukaListening:
		return ledJukaSrcCyan
	case ledJukaThinking:
		return ledJukaSrcViolet
	case ledJukaSpeaking, ledJukaShown:
		if warn {
			return ledJukaSrcAmber
		}
		return ledJukaSrcBlue
	}
	return ledJukaSrcUser
}

// ── Setting ────────────────────────────────────────────────────────────────

var ledJukaModeOrder = []struct {
	key, label, note string
}{
	{ledJukaAlways, "Always", "The lights wake for Juka even when they are off"},
	{ledJukaWhenOn, "When lights are on", "Only while the stick lights are on"},
	{ledJukaOff, "Off", "The lights never change for Juka"},
}

// ledJukaModeKey normalises a stored value; unknown means Always.
func ledJukaModeKey(raw string) string {
	wanted := strings.ToLower(strings.TrimSpace(raw))
	for _, m := range ledJukaModeOrder {
		if m.key == wanted {
			return m.key
		}
	}
	return ledJukaAlways
}

// ledJukaModeFromConfig reads React to Juka. Main thread.
func ledJukaModeFromConfig(config *Config) string {
	if config == nil {
		return ledJukaAlways
	}
	raw, _ := customString(config.Variables.Custom, ledKeyJuka)
	return ledJukaModeKey(raw)
}

func ledJukaRow() setRow {
	return setRow{
		ID: "ledJuka", Label: "React to Juka", Kind: setKindChoice, Clamp: true,
		Detail:  setStatic("Glow while Juka listens, thinks and answers, in games too"),
		Visible: ledSettingsAvailable,
		Options: func(*Config) []setOption {
			out := make([]setOption, 0, len(ledJukaModeOrder))
			for _, m := range ledJukaModeOrder {
				out = append(out, setOption{Value: m.key, Label: m.label, Note: m.note})
			}
			return out
		},
		Current: func(c *Config) int {
			key := ledJukaModeFromConfig(c)
			for i, m := range ledJukaModeOrder {
				if m.key == key {
					return i
				}
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			ledPanelEnsureKeys(c)
			c.Variables.Custom[ledKeyJuka] = ledJukaModeKey(value)
			ledJukaState.mode = "" // the tick reads it again on its next frame
			ledPanelPersist(c)
		},
	}
}

// ── Main thread: what Juka is doing ────────────────────────────────────────

// ledJukaSample is what the LED loop reads.
type ledJukaSample struct {
	Phase ledJukaPhase
	Voice float64 // the user's voice while listening, 0..1, gated and smoothed
	Warn  bool    // the answer is a problem: amber, like the orb
	At    time.Time
}

var ledJukaBus struct {
	mu sync.Mutex
	s  ledJukaSample
}

func currentLEDJukaSample() ledJukaSample {
	ledJukaBus.mu.Lock()
	defer ledJukaBus.mu.Unlock()
	return ledJukaBus.s
}

func publishLEDJukaSample(s ledJukaSample) {
	ledJukaBus.mu.Lock()
	ledJukaBus.s = s
	ledJukaBus.mu.Unlock()
}

// ledJukaOnly is set while the LED loop runs for Juka alone (the user's lights
// are off): the loop shows no mode of the user's and the lights' microphone
// stays shut. Any goroutine.
var ledJukaOnly atomic.Bool

// ledJukaState is the tick's own. Main thread.
var ledJukaState struct {
	phase    ledJukaPhase
	warn     bool
	follow   ledMicFollower
	voice    float64
	lastTick time.Time
	lastWant time.Time
	// forced: ledJukaTick started the loop (the lights were off) and stops it.
	forced  bool
	retryAt time.Time
	failed  bool
	// mode is React to Juka as last read (ledJukaModeRefresh); "" reads it now.
	mode   string
	modeAt time.Time
}

// ledJukaPhaseNow reads the orb (and the in-game assistant, which shares its
// phases). Juka still speaking after the orb went keeps the lights on it until
// the sentence ends; other speech (a notification read aloud) does not light
// them. Main thread.
func ledJukaPhaseNow(prev ledJukaPhase) (ledJukaPhase, bool) {
	up := voiceOrbActive()
	warn := up && (voiceOrb.tone == tbxToneWarn || voiceOrb.tone == tbxToneBad)
	if jukaSpeaking() && (up || gameAssistOverlayActive() || prev != ledJukaIdle) {
		return ledJukaSpeaking, warn
	}
	if !up {
		return ledJukaIdle, false
	}
	switch voiceOrb.phase {
	case orbListening:
		return ledJukaListening, false
	case orbThinking:
		return ledJukaThinking, false
	}
	return ledJukaShown, warn
}

// ledJukaTick publishes what Juka is doing and starts or stops a loop run for
// Juka alone. Main loop, once a frame (the parked game loop too); cheap while
// Juka is idle.
func ledJukaTick(config *Config, now time.Time) {
	st := &ledJukaState
	if st.mode == "" || now.Sub(st.modeAt) >= ledJukaModeRefresh || now.Before(st.modeAt) {
		st.mode, st.modeAt = ledJukaModeFromConfig(config), now
	}
	mode := st.mode
	phase, warn := ledJukaIdle, false
	allowed := mode == ledJukaAlways ||
		(mode == ledJukaWhenOn && config != nil && config.Variables.LEDSync)
	if allowed && !powerSleepActive() {
		phase, warn = ledJukaPhaseNow(st.phase)
		// When lights are on also needs a brightness above Off: the running
		// loop's copy of it, not the config parsed every frame.
		if phase != ledJukaIdle && mode == ledJukaWhenOn && currentLEDLoopSettings().brightness <= 0 {
			phase, warn = ledJukaIdle, false
		}
		if phase == ledJukaSpeaking && !voiceOrbActive() {
			// The orb went while Juka finishes the sentence: keep its colour.
			warn = st.warn
		}
	}
	dt := now.Sub(st.lastTick).Seconds()
	st.lastTick = now
	if phase == ledJukaListening {
		// The orb's own capture level (voiceorb.go), through the Microphone
		// mode's follower: the same floor, gain and easing (ledmic.go).
		if rms := voiceOrb.level; rms > 0 {
			st.voice = st.follow.step(rms, ledMicGain(currentLEDLoopSettings().micSensitivity), dt)
		}
	} else if st.voice != 0 || st.follow.level != 0 {
		// The floor is kept: it is the room's, and a fresh one would be set by
		// the first word of the next question.
		st.voice, st.follow.level = 0, 0
	}
	if phase != ledJukaIdle || st.phase != ledJukaIdle {
		publishLEDJukaSample(ledJukaSample{Phase: phase, Voice: st.voice, Warn: warn, At: now})
	}
	st.phase, st.warn = phase, warn
	ledJukaManageLoop(config, phase != ledJukaIdle && mode == ledJukaAlways, now)
}

// ledJukaManageLoop starts the LED loop for Juka when the lights are off, and
// stops it once Juka is done and the fade back has run. Main thread.
func ledJukaManageLoop(config *Config, want bool, now time.Time) {
	st := &ledJukaState
	if want {
		st.lastWant = now
	}
	userOn := config != nil && config.Variables.LEDSync
	if st.forced && (userOn || !LEDSyncRunning()) {
		// The user switched the lights on (the loop is theirs now), or
		// something else stopped it.
		st.forced = false
		ledJukaOnly.Store(false)
	}
	if want && !now.Before(st.retryAt) && !LEDSyncRunning() {
		ledJukaOnly.Store(!userOn)
		if err := SetLEDSync(true); err != nil {
			ledJukaOnly.Store(false)
			st.retryAt = now.Add(ledJukaRetryAfter)
			if !st.failed {
				st.failed = true
				LogScene("led").Debug("no stick lights for Juka", "err", err)
			}
			return
		}
		st.failed = false
		st.forced = !userOn
		return
	}
	if st.forced && !want && now.Sub(st.lastWant) >= ledJukaReleaseAfter {
		st.forced = false
		// The LED style panel may have taken the loop over for its preview
		// (ledJukaHandOver); it stops it itself.
		if !ledPanel.forcedSync {
			_ = SetLEDSync(false)
		}
		ledJukaOnly.Store(false)
	}
}

// ledJukaHandOver gives a loop started for Juka to the LED style panel, which
// would otherwise find the lights running and never stop them. Reports whether
// there was such a loop. Main thread.
func ledJukaHandOver() bool {
	st := &ledJukaState
	if !st.forced {
		return false
	}
	st.forced = false
	ledJukaOnly.Store(false)
	return LEDSyncRunning()
}

// ── LED loop: the layer ────────────────────────────────────────────────────

// ledJukaLayer is the LED loop's composition state. Locked because a loop
// started right after a stop may overlap the old one's last frame.
var ledJukaLayer struct {
	mu   sync.Mutex
	at   time.Time
	src  int     // what is shown: ledJukaSrcUser or one of Juka's hues
	fade float64 // how far the shown colour is faded in, 0..1
	glow float64 // Juka's eased level, 0..1
}

// ledJukaFresh is the sample's phase, idle once it is stale.
func ledJukaFresh(s ledJukaSample, now time.Time) ledJukaPhase {
	if now.Sub(s.At) > ledJukaStaleAfter {
		return ledJukaIdle
	}
	return s.Phase
}

// ledJukaHolds reports whether the layer has something to show, so the loop
// keeps drawing while a game owns the screen (the in-game assistant). LED loop.
func ledJukaHolds(now time.Time) bool {
	if ledJukaFresh(currentLEDJukaSample(), now) != ledJukaIdle {
		return true
	}
	l := &ledJukaLayer
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.src != ledJukaSrcUser && now.Sub(l.at) <= time.Second
}

// ledJukaTarget is Juka's level for one frame before easing. LED loop.
func ledJukaTarget(phase ledJukaPhase, voice float64, now time.Time) float64 {
	switch phase {
	case ledJukaListening:
		return ledJukaListenRest + (1-ledJukaListenRest)*voice
	case ledJukaThinking:
		// A raised cosine in perceived brightness, like Smooth: calm, no
		// corner for the eye to catch.
		p := math.Mod(float64(now.UnixMilli()), ledJukaThinkPeriodMs) / ledJukaThinkPeriodMs
		if p < 0 {
			p++
		}
		ease := (1 - math.Cos(2*math.Pi*p)) / 2
		return math.Pow(0.5+0.4*ease, ledSmoothGamma)
	case ledJukaSpeaking:
		level := 0.0
		if a := currentAudioSample(); now.Sub(a.At) <= ledSilenceAfter {
			level = levelFromRMS(a.RMS)
		}
		return ledJukaSpeakRest + (1-ledJukaSpeakRest)*level
	case ledJukaShown:
		return ledJukaShownLevel
	}
	return 0
}

// ledJukaFrame is one frame of the lights: the user's mode (ledFrameColor), or
// Juka's colour while the orb is up, with every hue change made through dark.
// userDark: a game owns the screen, and outside Juka the lights are the
// firmware's, not the user's mode. LED loop.
func ledJukaFrame(s ledLoopSettings, now time.Time, userDark bool) (uint8, uint8, uint8) {
	sample := currentLEDJukaSample()
	phase := ledJukaFresh(sample, now)
	want := ledJukaSrcFor(phase, sample.Warn)
	jukaOnly := ledJukaOnly.Load()

	l := &ledJukaLayer
	l.mu.Lock()
	defer l.mu.Unlock()
	dt := now.Sub(l.at).Seconds()
	l.at = now
	if dt < 0 || dt > 1 {
		// The first frame, or the first after a pause: the user's mode shows
		// at once, as it always did; Juka rises from dark.
		dt = ledFrameInterval.Seconds()
		l.src, l.glow, l.fade = want, 0, 0
		if want == ledJukaSrcUser {
			l.fade = 1
		}
	}
	if phase != ledJukaIdle {
		target := ledJukaTarget(phase, sample.Voice, now)
		tau := ledMicGlowFallSec
		if target > l.glow {
			tau = ledMicGlowRiseSec
		}
		l.glow += (target - l.glow) * (1 - math.Exp(-dt/tau))
	}

	var r, g, b uint8
	if l.src == ledJukaSrcUser {
		if !userDark && !jukaOnly {
			r, g, b = ledFrameColor(s, now)
		}
	} else {
		r, g, b = ledJukaColor(l.src, l.glow, s.brightness, jukaOnly)
	}
	if want != l.src {
		// Out through dark; a dark colour has nothing to fade.
		if r == 0 && g == 0 && b == 0 {
			l.fade = 0
		} else {
			l.fade -= dt / ledJukaDipSec
		}
		if l.fade <= 0 {
			l.fade = 0
			l.src = want
		}
	} else if l.fade < 1 {
		l.fade += dt / ledJukaDipSec
		if l.fade > 1 {
			l.fade = 1
		}
	}
	if l.fade >= 1 {
		return r, g, b
	}
	if l.fade <= 0 {
		return 0, 0, 0
	}
	e := l.fade * l.fade * (3 - 2*l.fade)
	if l.src != ledJukaSrcUser {
		// Made again at the faded level from the full hue, not the rounded
		// colour scaled down, so a dim frame keeps Juka's hue.
		return ledJukaColor(l.src, l.glow*e, s.brightness, jukaOnly)
	}
	ch := func(c uint8) uint8 { return uint8(math.Round(float64(c) * e)) }
	return ledJukaDarkFloor(ch(r), ch(g), ch(b))
}

// ledJukaDarkFloor is dark for a colour too dim to hold its hue
// (ledJukaMinPeak). Pure.
func ledJukaDarkFloor(r, g, b uint8) (uint8, uint8, uint8) {
	if max(r, g, b) < ledJukaMinPeak {
		return 0, 0, 0
	}
	return r, g, b
}

// ledJukaColor is one of Juka's hues at a level, trimmed by the user's
// brightness while the lights are theirs (a brightness of Off, or lights that
// are off, leave Juka at full). Pure.
func ledJukaColor(src int, level float64, brightness int, jukaOnly bool) (uint8, uint8, uint8) {
	if src <= ledJukaSrcUser || src >= len(ledJukaHues) {
		return 0, 0, 0
	}
	if level < 0 {
		level = 0
	}
	if level > 1 {
		level = 1
	}
	if !jukaOnly && brightness > 0 && brightness < 100 {
		level *= float64(brightness) / 100
	}
	hue := ledJukaHues[src]
	ch := func(c uint8) uint8 { return uint8(math.Round(float64(c) * level)) }
	return ledJukaDarkFloor(ch(hue[0]), ch(hue[1]), ch(hue[2]))
}
