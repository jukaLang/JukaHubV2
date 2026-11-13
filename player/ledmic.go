package main

// ledmic.go — the stick lights follow the microphone.
//
// LED mode "Microphone" (led_policy "mic") keeps the rings on with a soft,
// steady glow in the chosen colour and brightens them smoothly with what the
// handheld hears — the user talking (to Juka too: the orb's own capture feeds
// them, ledMicFeedVoice) and, a little, Juka answering. The colour stays put
// (ledMicColor): on the TrimUI rings a colour change is a visible blink.
//
// The microphone is the shared one (voicemic.go). micStream is main-thread
// only, so ledMicTick runs from the main loop each frame, reads what was
// captured, and publishes one small sample the LED goroutine reads; the LED
// loop never touches the device.
//
// Mic levels are nothing like a song's: a quiet room sits near -60 dBFS and a
// voice a metre away near -30. So the level is the RMS above a noise floor that
// follows the room (it drops at once to anything quieter and creeps up over
// several seconds, so a fan or hum fades out while speech does not), times the
// sensitivity the user picked, through the same log curve as the music mode;
// it rises within a frame or two and eases back over about half a second (and
// the LED loop eases again between samples), so the rings breathe
// with a voice instead of flickering on every syllable.
//
// Nothing is recorded or kept: each read is analysed and dropped. The device
// is open only while all of these hold — the lights are on, the mode is
// Microphone, JukaHub is on screen (a game, a dark idle screen or a minimised
// window closes it) and no voice feature is listening. The voice orb, Voice
// command, voice typing and voice shortcuts call micOpen, which first closes
// this stream (ledMicYield) so they get the codec to themselves; the lights
// take the microphone back a moment after the last of them closes.

import (
	"errors"
	"math"
	"strings"
	"sync"
	"time"
)

const (
	// ledKeyMicSensitivity is the custom key behind Settings → Mic sensitivity.
	ledKeyMicSensitivity = "led_mic_sensitivity"
	// ledKeyPolicyBeforeMic remembers the mode the voice switch replaced, so
	// switching it off goes back there.
	ledKeyPolicyBeforeMic = "led_policy_before_mic"
	// ledMicStaleAfter is how long a published sample stays valid: longer than
	// the music mode's, because the main loop can run at 5 fps on a busy page.
	ledMicStaleAfter = 500 * time.Millisecond
	// ledMicRetryAfter is the pause after the microphone would not open, so a
	// handheld without one is not probed every frame.
	ledMicRetryAfter = 30 * time.Second
	// ledMicResumeAfter is how long the lights wait after a voice feature lets
	// go of the microphone (its device may still be closing).
	ledMicResumeAfter = 1500 * time.Millisecond
	// ledMicWindow is how much of the newest audio one tick analyses: 50 ms,
	// so the rings follow what is heard now, not the backlog of a slow frame.
	ledMicWindow = micRate / 20
	// ledMicAttackSec and ledMicReleaseSec are how fast the level follows a
	// louder and a quieter sound (time constants).
	ledMicAttackSec  = 0.06
	ledMicReleaseSec = 0.4
	// ledMicRestLevel is the rings' glow in silence: the lights stay on in
	// Microphone mode and a voice brightens them from here to full.
	ledMicRestLevel = 0.4
	// ledMicGlowRiseSec and ledMicGlowFallSec ease the LED loop's 25 fps
	// frames between the main loop's samples (which can come at 5 fps).
	ledMicGlowRiseSec = 0.1
	ledMicGlowFallSec = 0.45
	// ledMicJukaShare is how much Juka's own voice brightens the rings while
	// it answers: a little less than the user's.
	ledMicJukaShare = 0.7
	// ledMicFloorRiseSec is the noise floor's time constant upwards through
	// room noise, ledMicFloorSlowSec under a sound well above it.
	ledMicFloorRiseSec = 6.0
	ledMicFloorSlowSec = 45.0
	// ledMicSettle is how long Microphone must stay chosen before the device
	// opens; ledMicNoneRetryAfter the pause when nothing can record at all;
	// ledMicDeadAfter how long an open device may give nothing before it is
	// reopened.
	ledMicSettle         = 500 * time.Millisecond
	ledMicNoneRetryAfter = 5 * time.Minute
	ledMicDeadAfter      = 3 * time.Second
)

// ledMicSensitivityOrder is the Mic sensitivity row, quietest first; gain is
// what the RMS above the floor is multiplied by.
var ledMicSensitivityOrder = []struct {
	key   string
	label string
	note  string
	gain  float64
}{
	{"low", "Low", "Loud sounds and singing up close", 3},
	{"normal", "Normal", "Talking near the handheld", 7},
	{"high", "High", "Quiet voices and sounds across the room", 16},
}

// ledMicSensitivityKey normalises a stored sensitivity; unknown means normal.
func ledMicSensitivityKey(raw string) string {
	wanted := strings.ToLower(strings.TrimSpace(raw))
	for _, s := range ledMicSensitivityOrder {
		if s.key == wanted {
			return s.key
		}
	}
	return "normal"
}

// ledMicSensitivityFromConfig reads the stored sensitivity. Main thread.
func ledMicSensitivityFromConfig(config *Config) string {
	if config == nil {
		return "normal"
	}
	raw, _ := customString(config.Variables.Custom, ledKeyMicSensitivity)
	return ledMicSensitivityKey(raw)
}

func ledMicGain(key string) float64 {
	key = ledMicSensitivityKey(key)
	for _, s := range ledMicSensitivityOrder {
		if s.key == key {
			return s.gain
		}
	}
	return 7
}

// ledMicSample is what the LED loop reads: the finished level and a pitch.
type ledMicSample struct {
	Level float64 // 0..1, gated, scaled and smoothed
	Freq  float64 // rough dominant frequency in Hz
	At    time.Time
}

var ledMicBus struct {
	mu sync.Mutex
	s  ledMicSample
}

func currentLEDMicSample() ledMicSample {
	ledMicBus.mu.Lock()
	defer ledMicBus.mu.Unlock()
	return ledMicBus.s
}

func publishLEDMicSample(s ledMicSample) {
	ledMicBus.mu.Lock()
	ledMicBus.s = s
	ledMicBus.mu.Unlock()
}

// ledMicState is the lights' own microphone. Main thread.
var ledMicState struct {
	stream *micStream
	// bufs are two read buffers used in turn: when a backlog takes several
	// reads and the last one is short, the one before still holds the
	// newest full window.
	bufs      [2][]byte
	retryAt   time.Time
	resumeAt  time.Time
	wantSince time.Time
	gotAt     time.Time
	failed    bool // the last open failed; logged once until one works
	follow    ledMicFollower
	lastTick  time.Time
	hadVoices bool
}

// ledMicFollower is the level maths, apart from the device so it can be
// reasoned about (and tested) alone.
type ledMicFollower struct {
	floor, level float64
	// freq is the pitch, smoothed so the colour does not jump band to band
	// on every syllable.
	freq float64
}

// step folds one RMS reading taken dt after the last into the level. Pure
// apart from the follower's own state.
func (f *ledMicFollower) step(rms, gain, dt float64) float64 {
	if dt <= 0 || dt > 1 {
		dt = 0.05
	}
	switch {
	case f.floor <= 0 || rms < f.floor:
		f.floor = rms
	case rms < 2*f.floor:
		// Room noise: the floor follows it within seconds.
		f.floor += (rms - f.floor) * (1 - math.Exp(-dt/ledMicFloorRiseSec))
	default:
		// Something clearly louder than the room — a voice, a held note — only
		// creeps the floor up, so a long song does not fade itself out.
		f.floor += (rms - f.floor) * (1 - math.Exp(-dt/ledMicFloorSlowSec))
	}
	signal := (rms - f.floor*1.25) * gain
	if signal < 0 {
		signal = 0
	}
	target := levelFromRMS(signal)
	// Eased both ways: quick to rise (a syllable shows within a frame or
	// two) and slow to fall, so the glow swells with a sentence instead of
	// snapping to every syllable.
	tau := ledMicReleaseSec
	if target >= f.level {
		tau = ledMicAttackSec
	}
	f.level += (target - f.level) * (1 - math.Exp(-dt/tau))
	if f.level < 0.002 {
		f.level = 0
	}
	return f.level
}

// pitch smooths a zero-crossing estimate in the log domain (about 0.3 s).
// Readings taken in silence are ignored: their crossings are noise. Pure
// apart from the follower's own state.
func (f *ledMicFollower) pitch(hz, dt float64) float64 {
	if hz <= 0 || f.level <= 0 {
		return f.freq
	}
	if f.freq <= 0 {
		f.freq = hz
		return hz
	}
	if dt <= 0 || dt > 1 {
		dt = 0.05
	}
	a := 1 - math.Exp(-dt/0.3)
	f.freq = math.Exp(math.Log(f.freq) + (math.Log(hz)-math.Log(f.freq))*a)
	return f.freq
}

// ledMicWanted reports whether the lights should be listening now.
func ledMicWanted() bool {
	return LEDSyncRunning() &&
		currentLEDLoopSettings().policy == ledPolicyMic &&
		!backgroundPaused() &&
		!powerSleepActive() &&
		micVoiceUsers.Load() == 0
}

// ledMicTick opens, reads or closes the lights' microphone. Main loop, once a
// frame; free when the mode is anything else.
func ledMicTick(now time.Time) {
	st := &ledMicState
	if micVoiceUsers.Load() > 0 {
		st.hadVoices = true
	} else if st.hadVoices {
		st.hadVoices = false
		st.resumeAt = now.Add(ledMicResumeAfter)
	}
	if !ledMicWanted() {
		st.wantSince = time.Time{}
		ledMicClose()
		return
	}
	if st.wantSince.IsZero() {
		st.wantSince = now
	}
	if st.stream == nil {
		// Cycling Quick Settings' LED Mode past Microphone must not open the
		// codec (and run the mixer setup) on the way through.
		if now.Sub(st.wantSince) < ledMicSettle || now.Before(st.retryAt) || now.Before(st.resumeAt) || micClosing.n.Load() > 0 {
			return
		}
		m, err := micOpenShared()
		if err != nil {
			st.retryAt = now.Add(ledMicRetryAfter)
			if errors.Is(err, errMicNone) {
				// Nothing that records exists: no point asking every 30 s.
				st.retryAt = now.Add(ledMicNoneRetryAfter)
			}
			if !st.failed {
				st.failed = true
				LogScene("led").Warn("no microphone for the lights", "err", err)
				ShowToast("Lights: no microphone could be opened", ToastKindWarning)
			}
			return
		}
		st.failed = false
		st.stream = m
		st.lastTick = now
		st.gotAt = now
		LogScene("led").Info("lights listening", "via", m.Via())
	}
	size := micReadMaxFrames * 2
	for i := range st.bufs {
		if cap(st.bufs[i]) < size {
			st.bufs[i] = make([]byte, size)
		}
	}
	// One Read moves at most two seconds; a frame that stalled longer leaves
	// more behind, and the newest audio is the part worth showing.
	cur, prev := st.bufs[0][:size], st.bufs[1][:size]
	n := st.stream.Read(cur)
	prevN := 0
	for i := 0; i < 3 && n == size; i++ {
		cur, prev, prevN = prev, cur, n
		n = st.stream.Read(cur)
	}
	window := ledMicWindow * 2
	var pcm []byte
	switch {
	case n >= window:
		pcm = cur[n-window : n]
	case prevN > 0:
		// The last read was short: the newest window spans both.
		joined := append(append(make([]byte, 0, window), prev[prevN-(window-n):prevN]...), cur[:n]...)
		pcm = joined
	default:
		pcm = cur[:n]
	}
	if len(pcm) < 4 {
		// A device that stops giving anything (arecord gone, the codec lost
		// over a suspend) is reopened rather than left dark until the switch
		// is flipped.
		if now.Sub(st.gotAt) > ledMicDeadAfter {
			LogScene("led").Info("lights' microphone gave nothing; reopening")
			ledMicClose()
			st.resumeAt = now.Add(ledMicResumeAfter)
		}
		return
	}
	st.gotAt = now
	frames := len(pcm) / 2
	dt := now.Sub(st.lastTick).Seconds()
	st.lastTick = now
	level := st.follow.step(rmsPCM16(pcm, frames, 1), ledMicGain(currentLEDLoopSettings().micSensitivity), dt)
	hz := st.follow.pitch(zeroCrossHz(pcm, micRate, 1), dt)
	publishLEDMicSample(ledMicSample{Level: level, Freq: hz, At: now})
}

// ledMicClose closes the lights' microphone; safe when it is not open.
// Main thread (and Cleanup).
func ledMicClose() {
	st := &ledMicState
	if st.stream == nil {
		return
	}
	st.stream.Close()
	st.stream = nil
	st.follow.level = 0
	publishLEDMicSample(ledMicSample{})
}

// ledMicFeedVoice lets the rings follow the user while a voice feature has
// the microphone (the orb, voice typing): the lights' own stream is closed
// then, and talking to Juka is exactly when they should react. rms is the
// newest captured level. Main thread, once a frame.
func ledMicFeedVoice(rms float64) {
	st := &ledMicState
	if st.stream != nil || !LEDSyncRunning() || currentLEDLoopSettings().policy != ledPolicyMic {
		return
	}
	now := time.Now()
	dt := now.Sub(st.lastTick).Seconds()
	st.lastTick = now
	level := st.follow.step(rms, ledMicGain(currentLEDLoopSettings().micSensitivity), dt)
	publishLEDMicSample(ledMicSample{Level: level, Freq: st.follow.freq, At: now})
}

// ledMicGlow is the LED loop's eased level for Microphone mode. LED loop.
var ledMicGlow struct {
	mu    sync.Mutex
	shown float64
	at    time.Time
}

// ledMicColor is one frame of Microphone mode: a steady glow in the chosen
// colour (a soft blue when none was picked) that brightens with the voice —
// the user's, and a little with Juka's answer. The hue never changes: on the
// TrimUI rings a new hue means re-arming the effect, which shows as a blink,
// while brightness goes through the scale node smoothly. LED loop.
func ledMicColor(s ledLoopSettings, level float64, now time.Time) (uint8, uint8, uint8) {
	if jukaSpeaking() {
		if a := currentAudioSample(); time.Since(a.At) <= ledSilenceAfter {
			if j := levelFromRMS(a.RMS) * ledMicJukaShare; j > level {
				level = j
			}
		}
	}
	g := &ledMicGlow
	g.mu.Lock()
	dt := now.Sub(g.at).Seconds()
	g.at = now
	if dt < 0 || dt > 1 {
		dt = 1 // first frame, or after a pause: no slow fade from stale state
	}
	tau := ledMicGlowFallSec
	if level > g.shown {
		tau = ledMicGlowRiseSec
	}
	g.shown += (level - g.shown) * (1 - math.Exp(-dt/tau))
	shown := g.shown
	g.mu.Unlock()
	r, gr, b := s.r, s.g, s.b
	if r == 0 && gr == 0 && b == 0 {
		r, gr, b = 0x38, 0x9C, 0xFF
	}
	hue, _ := ledAnimSplit(r, gr, b)
	v := ledMicRestLevel + (1-ledMicRestLevel)*shown
	ch := func(c uint8) uint8 { return uint8(math.Round(float64(c) * v)) }
	return ch(hue[0]), ch(hue[1]), ch(hue[2])
}

// ledMicYield is micOpen's first step: a voice feature takes the microphone
// from the lights. Main thread.
func ledMicYield() {
	if ledMicState.stream != nil {
		ledMicClose()
		ledMicState.hadVoices = true
	}
}

func init() {
	Cleanup = append(Cleanup, ledMicClose)
}

// ── Settings rows (placed on the Stick lights page, ledsettings.go) ────────

// ledMicRowsCache holds whether the handheld has both lights and a microphone
// (ledMicRowsVisible): the Settings page asks every frame and the answer needs
// a sysfs scan and SDL's device list. Main thread.
var ledMicRowsCache struct {
	at time.Time
	ok bool
}

func ledMicRowsVisible(*Config) bool {
	c := &ledMicRowsCache
	if now := time.Now(); c.at.IsZero() || now.Sub(c.at) >= 5*time.Second {
		c.at = now
		c.ok = ledSyncAvailable() && micPresence() != vkMicAbsent
	}
	return c.ok
}

// ledMicModeOn reports whether the lights are on and following the microphone.
func ledMicModeOn(c *Config) bool {
	return c != nil && c.Variables.LEDSync && ledPolicyFromConfig(c) == ledPolicyMic
}

// ledMicSetMode switches the lights to the microphone (turning them on), or
// back to the music. The same keys Quick Settings' LED Mode and the Light style
// panel write, saved the way the panel saves them.
func ledMicSetMode(c *Config, on bool) {
	if c == nil {
		return
	}
	ledPanelEnsureKeys(c)
	if !on {
		// Back to whatever the lights showed before (Battery, a custom
		// colour…), the music when that is unknown.
		back := ledPolicyAudio
		if raw, ok := customString(c.Variables.Custom, ledKeyPolicyBeforeMic); ok && ledPolicyKey(raw) != ledPolicyMic {
			back = ledPolicyKey(raw)
		}
		c.Variables.Custom[ledKeyPolicy] = back
		syncLEDLoopSettings(c)
		ledPanelPersist(c)
		if back == ledPolicyAudio {
			setToast("Lights follow the music again")
		} else {
			setToast("LED Mode: " + ledPolicyLabel(back))
		}
		return
	}
	if prev := ledPolicyFromConfig(c); prev != ledPolicyMic {
		c.Variables.Custom[ledKeyPolicyBeforeMic] = prev
	}
	c.Variables.Custom[ledKeyPolicy] = ledPolicyMic
	syncLEDLoopSettings(c)
	if !c.Variables.LEDSync {
		if err := SetLEDSync(true); err != nil {
			ShowToast("Lights: "+firstLine(err.Error()), ToastKindWarning)
			ledPanelPersist(c)
			return
		}
		c.Variables.LEDSync = true
		ledPanel.forcedSync = false
	}
	// A failed open earlier waits out its retry; the user asking again is a
	// reason to try now.
	ledMicState.retryAt = time.Time{}
	ledMicState.failed = false
	ledPanelPersist(c)
	setToast("Lights follow the microphone")
}

func ledMicSensitivityOptions(*Config) []setOption {
	out := make([]setOption, 0, len(ledMicSensitivityOrder))
	for _, s := range ledMicSensitivityOrder {
		out = append(out, setOption{Value: s.key, Label: s.label, Note: s.note})
	}
	return out
}

func ledMicSensitivityCurrent(c *Config) int {
	key := ledMicSensitivityFromConfig(c)
	for i, s := range ledMicSensitivityOrder {
		if s.key == key {
			return i
		}
	}
	return 1
}

func ledMicSensitivityRow() setRow {
	return setRow{
		ID: "ledmicSensitivity", Label: "Mic sensitivity", Kind: setKindChoice, Clamp: true,
		Detail:  setStatic("How loud a sound must be to light the sticks"),
		Visible: func(c *Config) bool { return ledMicModeOn(c) && ledMicRowsVisible(c) },
		Options: ledMicSensitivityOptions,
		Current: ledMicSensitivityCurrent,
		Choose: func(c *Config, value string) {
			if c == nil {
				return
			}
			ledPanelEnsureKeys(c)
			c.Variables.Custom[ledKeyMicSensitivity] = ledMicSensitivityKey(value)
			syncLEDLoopSettings(c)
			ledPanelPersist(c)
		},
	}
}
