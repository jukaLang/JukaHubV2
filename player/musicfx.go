package main

// musicfx.go — the player's sound effects: Tape Mode and the stereo effects.
//
// Tape Mode makes any song sound as if it came off a Walkman: the transport's
// wow and flutter (a slow and a fast pitch wobble), tape hiss, the soft
// saturation of a hot recording, the high-frequency roll-off of a worn head,
// and the tape speed (varispeed: pitch and tempo together, like the speed
// trimmer of a real deck). The stereo effects are a Freeverb-style reverb, a
// ping-pong delay (each echo jumps to the other ear), a chorus and a flanger.
//
// Everything except the tape speed runs here in Go, on the decoded PCM, just
// before it is queued (musicDecode). That costs a few percent of one core on
// the A133P and lets a setting change without a new ffmpeg command line. The
// tape speed is an ffmpeg filter (asetrate/aresample, musicTapeFilter): it
// changes how fast the track is read, so the position maths has to know it.
//
// The queue holds about a second of audio, so a change would only be heard a
// second later. A change therefore restarts the decoder where the listener
// is (musicRestartAtHeard), debounced so a held D-pad restarts it once.
//
// Bit-perfect output bypasses all of it, like the equaliser.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// musicFXKey is the Custom setting that holds every effect as one line:
// "tape=1;wow=35;…" (musicFXFormat).
const musicFXKey = "musicFX"

// musicFXParams is one set of effect settings. The amounts are percentages
// (0..100); TapeSpeed is the tape speed in percent (50..150, 100 = 1.00×) and
// DelayMs the ping-pong delay time.
type musicFXParams struct {
	Tape       bool
	Wow        int
	Hiss       int
	Saturation int
	RollOff    int
	TapeSpeed  int
	Reverb     int
	Room       int
	Delay      int
	DelayMs    int
	Feedback   int
	Chorus     int
	Flanger    int
}

// musicFXDefaults is the factory setting: everything off, with the tape
// controls at the levels a Walkman sounds like once Tape Mode is switched on.
func musicFXDefaults() musicFXParams {
	return musicFXParams{Wow: 35, Hiss: 22, Saturation: 30, RollOff: 40, TapeSpeed: 100,
		Room: 60, DelayMs: 320, Feedback: 40}
}

// active reports whether any effect changes the sound.
func (p musicFXParams) active() bool {
	return p.Tape || p.Reverb > 0 || p.Delay > 0 || p.Chorus > 0 || p.Flanger > 0
}

// tapeFactor is how many seconds of the track one second of output reads:
// the tape speed while Tape Mode is on, else 1.
func (p musicFXParams) tapeFactor() float64 {
	if !p.Tape || p.TapeSpeed == 100 || p.TapeSpeed <= 0 {
		return 1
	}
	return float64(p.TapeSpeed) / 100
}

// clamp keeps every field inside the range the panel offers.
func (p musicFXParams) clamp() musicFXParams {
	pct := func(v int) int { return clampInt(v, 0, 100) }
	p.Wow, p.Hiss, p.Saturation, p.RollOff = pct(p.Wow), pct(p.Hiss), pct(p.Saturation), pct(p.RollOff)
	p.Reverb, p.Room, p.Delay, p.Chorus, p.Flanger = pct(p.Reverb), pct(p.Room), pct(p.Delay), pct(p.Chorus), pct(p.Flanger)
	p.Feedback = clampInt(p.Feedback, 0, 90)
	p.TapeSpeed = clampInt(p.TapeSpeed, 50, 150)
	p.DelayMs = clampInt(p.DelayMs, 60, 900)
	return p
}

// musicFXFormat is the config form of p. Pure.
func musicFXFormat(p musicFXParams) string {
	tape := 0
	if p.Tape {
		tape = 1
	}
	return fmt.Sprintf("tape=%d;wow=%d;hiss=%d;sat=%d;roll=%d;tspeed=%d;rev=%d;room=%d;delay=%d;dms=%d;fb=%d;chorus=%d;flanger=%d",
		tape, p.Wow, p.Hiss, p.Saturation, p.RollOff, p.TapeSpeed, p.Reverb, p.Room, p.Delay, p.DelayMs,
		p.Feedback, p.Chorus, p.Flanger)
}

// musicFXParse reads the config form. Unknown or broken fields keep their
// defaults, so a hand-edited line can never leave the player silent. Pure.
func musicFXParse(raw string) musicFXParams {
	p := musicFXDefaults()
	for _, part := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "tape":
			p.Tape = n != 0
		case "wow":
			p.Wow = n
		case "hiss":
			p.Hiss = n
		case "sat":
			p.Saturation = n
		case "roll":
			p.RollOff = n
		case "tspeed":
			p.TapeSpeed = n
		case "rev":
			p.Reverb = n
		case "room":
			p.Room = n
		case "delay":
			p.Delay = n
		case "dms":
			p.DelayMs = n
		case "fb":
			p.Feedback = n
		case "chorus":
			p.Chorus = n
		case "flanger":
			p.Flanger = n
		}
	}
	return p.clamp()
}

// musicFXLive is the setting the decoder reads, published by musicFXSet. The
// render thread owns the config; the decoder only ever loads this pointer.
var musicFXLive atomic.Pointer[musicFXParams]

// musicFXLoaded keeps the config read once per run.
var musicFXLoaded atomic.Bool

// musicFXNow is the current setting (the config's on first use).
func musicFXNow() musicFXParams {
	if p := musicFXLive.Load(); p != nil {
		return *p
	}
	if !musicFXLoaded.Load() {
		musicFXLoaded.Store(true)
		p := musicFXDefaults()
		if appConfig != nil {
			if raw := customStringValue(appConfig.Variables.Custom, musicFXKey); strings.TrimSpace(raw) != "" {
				p = musicFXParse(raw)
			}
		}
		musicFXLive.Store(&p)
		return p
	}
	return musicFXDefaults()
}

// musicFXTapeFactor is the tape speed the next decoder reads at: 1 when Tape
// Mode is off or the output is bit-perfect.
func musicFXTapeFactor() float64 {
	if musicEQ.bitPerfect {
		return 1
	}
	return musicFXNow().tapeFactor()
}

// musicTapeFilter is the ffmpeg chain that plays the track at factor× speed
// with the pitch following, as a tape does: the stream is brought to rate,
// relabelled as faster or slower, and brought back. "" at 1×. Pure.
func musicTapeFilter(factor float64, rate int32) string {
	if factor <= 0 || math.Abs(factor-1) < 0.005 {
		return ""
	}
	if rate <= 0 {
		rate = 44100
	}
	shifted := int(math.Round(float64(rate) * factor))
	return fmt.Sprintf("aresample=%d,asetrate=%d,aresample=%d", rate, shifted, rate)
}

// musicFXSet stores a new setting, publishes it to the decoder and asks for a
// restart so it is heard at once. Main thread.
func musicFXSet(config *Config, p musicFXParams) {
	p = p.clamp()
	old := musicFXNow()
	musicFXLive.Store(&p)
	musicFXLoaded.Store(true)
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom[musicFXKey] = musicFXFormat(p)
	}
	if old != p {
		musicFXRestartSoon()
	}
}

// musicFXRestart debounces the restart: a held key changes a value many times
// a second, and each restart would drop the audio for a moment.
var musicFXRestart struct {
	mu  sync.Mutex
	gen int
}

const musicFXRestartDelay = 260 * time.Millisecond

func musicFXRestartSoon() {
	musicFXRestart.mu.Lock()
	musicFXRestart.gen++
	gen := musicFXRestart.gen
	musicFXRestart.mu.Unlock()
	time.AfterFunc(musicFXRestartDelay, func() {
		musicFXRestart.mu.Lock()
		current := gen == musicFXRestart.gen
		musicFXRestart.mu.Unlock()
		if current {
			tbxPost(musicRestartAtHeard)
		}
	})
}

// musicRestartAtHeard restarts the decoder at the place the listener hears,
// not at the decoded position (which runs the queue's length ahead): a change
// of effect, tape speed or AutoEQ profile does not skip a second of the song.
// Main thread.
func musicRestartAtHeard() {
	music.mu.Lock()
	cur, playing := music.current, music.playing
	path := ""
	if cur >= 0 && cur < len(music.tracks) {
		path = music.tracks[cur].Path
	}
	music.mu.Unlock()
	if path == "" || !playing {
		return
	}
	musicStartDecode(path, musicHeardPosition())
}

// musicHeardPosition is the place in the track the listener hears: the
// decoded position less what the queue still holds, in the track's seconds.
func musicHeardPosition() float64 {
	music.mu.Lock()
	pos, factor := music.position, music.mediaRate
	music.mu.Unlock()
	if factor <= 0 {
		factor = 1
	}
	if _, _, queued, bps, _, ok := videoAudioClock(); ok && bps > 0 {
		pos -= float64(queued) / bps * factor
	}
	if pos < 0 {
		pos = 0
	}
	return pos
}

// musicFXStatus is the one-line summary the player and the iPod screen show.
func musicFXStatus() string {
	if musicEQ.bitPerfect {
		return "Bit-perfect"
	}
	p := musicFXNow()
	var parts []string
	if p.Tape {
		label := "Tape"
		if p.TapeSpeed != 100 {
			label += " " + formatSpeedLabel(p.tapeFactor())
		}
		parts = append(parts, label)
	}
	for _, fx := range []struct {
		on   bool
		name string
	}{{p.Reverb > 0, "Reverb"}, {p.Delay > 0, "Delay"}, {p.Chorus > 0, "Chorus"}, {p.Flanger > 0, "Flanger"}} {
		if fx.on {
			parts = append(parts, fx.name)
		}
	}
	if len(parts) == 0 {
		return "FX off"
	}
	return strings.Join(parts, " + ")
}

// musicFXPreset is a named starting point Y steps through in the panel.
type musicFXPreset struct {
	name  string
	apply func(*musicFXParams)
}

var musicFXPresets = []musicFXPreset{
	{"Off", func(p *musicFXParams) { *p = musicFXDefaults() }},
	{"Walkman", func(p *musicFXParams) {
		*p = musicFXDefaults()
		p.Tape = true
	}},
	{"Worn cassette", func(p *musicFXParams) {
		*p = musicFXDefaults()
		p.Tape, p.Wow, p.Hiss, p.Saturation, p.RollOff = true, 70, 45, 45, 65
	}},
	{"Concert hall", func(p *musicFXParams) {
		*p = musicFXDefaults()
		p.Reverb, p.Room = 45, 88
	}},
	{"Space echo", func(p *musicFXParams) {
		*p = musicFXDefaults()
		p.Tape, p.Wow, p.Hiss = true, 20, 10
		p.Delay, p.DelayMs, p.Feedback, p.Reverb, p.Room = 42, 380, 55, 20, 70
	}},
	{"Dream", func(p *musicFXParams) {
		*p = musicFXDefaults()
		p.Chorus, p.Reverb, p.Room = 50, 35, 75
	}},
	{"Jet", func(p *musicFXParams) {
		*p = musicFXDefaults()
		p.Flanger = 65
	}},
	{"Slow + reverb", func(p *musicFXParams) {
		*p = musicFXDefaults()
		p.Tape, p.Wow, p.Hiss, p.Saturation, p.RollOff, p.TapeSpeed = true, 10, 0, 10, 20, 85
		p.Reverb, p.Room = 40, 80
	}},
}

// ──────────────────────────────────────────────────────────────────────
// DSP
// ──────────────────────────────────────────────────────────────────────

// fxLine is a delay line read at fractional delays. After push, the newest
// sample is 0 samples back.
type fxLine struct {
	buf []float64
	pos int
}

func newFXLine(n int) fxLine {
	if n < 4 {
		n = 4
	}
	return fxLine{buf: make([]float64, n)}
}

func (l *fxLine) push(v float64) {
	l.buf[l.pos] = v
	l.pos++
	if l.pos == len(l.buf) {
		l.pos = 0
	}
}

// at is the sample d samples behind the newest, linearly interpolated.
func (l *fxLine) at(d float64) float64 {
	n := len(l.buf)
	if d < 0 {
		d = 0
	}
	if lim := float64(n - 2); d > lim {
		d = lim
	}
	i := int(d)
	f := d - float64(i)
	a := l.pos - 1 - i
	if a < 0 {
		a += n
	}
	b := a - 1
	if b < 0 {
		b += n
	}
	return l.buf[a]*(1-f) + l.buf[b]*f
}

// fxComb and fxAllpass are Freeverb's two building blocks (Jezar's public
// domain design): eight damped combs in parallel, four allpasses in series.
type fxComb struct {
	buf   []float64
	pos   int
	store float64
}

func (c *fxComb) process(in, feedback, damp float64) float64 {
	out := c.buf[c.pos]
	c.store = out*(1-damp) + c.store*damp
	c.buf[c.pos] = in + c.store*feedback
	c.pos++
	if c.pos == len(c.buf) {
		c.pos = 0
	}
	return out
}

type fxAllpass struct {
	buf []float64
	pos int
}

func (a *fxAllpass) process(in float64) float64 {
	held := a.buf[a.pos]
	out := held - in
	a.buf[a.pos] = in + held*0.5
	a.pos++
	if a.pos == len(a.buf) {
		a.pos = 0
	}
	return out
}

// Freeverb's tunings at 44.1 kHz; the right channel's are 23 samples longer.
var (
	fxCombTuning    = [8]int{1116, 1188, 1277, 1356, 1422, 1491, 1557, 1617}
	fxAllpassTuning = [4]int{556, 441, 341, 225}
)

const fxStereoSpread = 23

// musicFXEngine processes one decoder's stream. It is owned by that decoder's
// goroutine; only the settings come from outside (musicFXLive).
type musicFXEngine struct {
	fs     float64
	p      musicFXParams
	params *musicFXParams // the pointer p was read from, to notice a change

	// Tape.
	wowLine      [2]fxLine
	wowPhase     float64
	flutPhase    float64
	flut2Phase   float64
	drift        float64
	driftTarget  float64
	driftCount   int
	roll1, roll2 [2]float64
	rollAlpha    float64
	satK, satN   float64
	hissLP       [2]float64
	hissAmp      float64
	rng          uint32

	// Stereo effects.
	combs     [2][8]fxComb
	allpasses [2][4]fxAllpass
	roomFB    float64
	echoL     fxLine
	echoR     fxLine
	echoLP    [2]float64
	chorus    [2]fxLine
	chPhase   float64
	flanger   [2]fxLine
	flPhase   float64

	// Output limiter.
	env, envRel float64

	// carry holds the bytes of a frame a read split, buf the joined input.
	carry []byte
	buf   []byte
}

// newMusicFXEngine sizes the lines for the device rate.
func newMusicFXEngine(rate int32) *musicFXEngine {
	fs := float64(rate)
	if fs <= 0 {
		fs = 44100
	}
	e := &musicFXEngine{fs: fs, rng: 0x9E3779B9}
	ms := func(v float64) int { return int(v*fs/1000) + 4 }
	for ch := 0; ch < 2; ch++ {
		e.wowLine[ch] = newFXLine(ms(40))
		e.chorus[ch] = newFXLine(ms(40))
		e.flanger[ch] = newFXLine(ms(12))
	}
	e.envRel = math.Exp(-1 / (0.12 * fs))
	return e
}

// allocReverb makes the reverb's combs and allpasses the first time it is
// turned on: a decoder starts with every song, and most play with no reverb.
func (e *musicFXEngine) allocReverb() {
	if e.combs[0][0].buf != nil {
		return
	}
	scale := e.fs / 44100
	for ch := 0; ch < 2; ch++ {
		spread := 0
		if ch == 1 {
			spread = fxStereoSpread
		}
		for i, n := range fxCombTuning {
			e.combs[ch][i].buf = make([]float64, int(float64(n+spread)*scale)+1)
		}
		for i, n := range fxAllpassTuning {
			e.allpasses[ch][i].buf = make([]float64, int(float64(n+spread)*scale)+1)
		}
	}
}

// allocEcho makes the ping-pong lines (just under a second each) on first use.
func (e *musicFXEngine) allocEcho() {
	if e.echoL.buf != nil {
		return
	}
	n := int(920*e.fs/1000) + 4
	e.echoL = newFXLine(n)
	e.echoR = newFXLine(n)
}

// refresh recomputes what depends on the settings when they changed.
func (e *musicFXEngine) refresh() {
	ptr := musicFXLive.Load()
	if ptr == e.params && ptr != nil {
		return
	}
	e.params = ptr
	if ptr != nil {
		e.p = *ptr
	} else {
		e.p = musicFXNow()
	}
	p := e.p
	// Roll-off: 20 kHz at 0 %, 2.5 kHz at 100 % (two one-pole stages).
	fc := 20000 * math.Pow(2500.0/20000.0, float64(p.RollOff)/100)
	if fc > e.fs*0.45 {
		fc = e.fs * 0.45
	}
	e.rollAlpha = 1 - math.Exp(-2*math.Pi*fc/e.fs)
	e.satK = 1 + 5*float64(p.Saturation)/100
	e.satN = 1 / math.Tanh(e.satK)
	h := float64(p.Hiss) / 100
	e.hissAmp = 0.03 * h * math.Sqrt(h)
	e.roomFB = 0.7 + 0.28*float64(p.Room)/100
	if p.Reverb > 0 {
		e.allocReverb()
	}
	if p.Delay > 0 {
		e.allocEcho()
	}
}

// noise is a xorshift32 sample in [-1, 1).
func (e *musicFXEngine) noise() float64 {
	x := e.rng
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	e.rng = x
	return float64(int32(x)) / 2147483648.0
}

// processS16 runs the effects over stereo S16 bytes and returns the whole
// frames to queue (a frame split across two reads is kept for the next call).
// The returned slice is the engine's own buffer, valid until the next call.
func (e *musicFXEngine) processS16(in []byte) []byte {
	e.buf = append(e.buf[:0], e.carry...)
	e.buf = append(e.buf, in...)
	whole := len(e.buf) &^ 3
	e.carry = append(e.carry[:0], e.buf[whole:]...)
	frames := e.buf[:whole]
	e.refresh()
	if !e.p.active() {
		return frames
	}
	for i := 0; i+3 < len(frames); i += 4 {
		l := float64(int16(uint16(frames[i])|uint16(frames[i+1])<<8)) / 32768
		r := float64(int16(uint16(frames[i+2])|uint16(frames[i+3])<<8)) / 32768
		l, r = e.frame(l, r)
		frames[i], frames[i+1] = fxS16(l)
		frames[i+2], frames[i+3] = fxS16(r)
	}
	return frames
}

// fxS16 is one float sample as little-endian S16 bytes.
func fxS16(v float64) (byte, byte) {
	s := int32(math.Round(v * 32767))
	if s > 32767 {
		s = 32767
	} else if s < -32768 {
		s = -32768
	}
	u := uint16(int16(s))
	return byte(u), byte(u >> 8)
}

// frame is the whole chain for one stereo frame.
func (e *musicFXEngine) frame(l, r float64) (float64, float64) {
	p := &e.p
	if p.Tape {
		l, r = e.tape(l, r)
	}
	if p.Chorus > 0 {
		l, r = e.chorusFrame(l, r)
	}
	if p.Flanger > 0 {
		l, r = e.flangerFrame(l, r)
	}
	if p.Delay > 0 {
		l, r = e.echoFrame(l, r)
	}
	if p.Reverb > 0 {
		l, r = e.reverbFrame(l, r)
	}
	// A peak limiter with instant attack: reverb and echoes add energy to a
	// master that already peaks at 0 dBFS.
	a := math.Max(math.Abs(l), math.Abs(r))
	if a > e.env {
		e.env = a
	} else {
		e.env = e.env*e.envRel + a*(1-e.envRel)
	}
	if e.env > 0.97 {
		g := 0.97 / e.env
		l *= g
		r *= g
	}
	return l, r
}

// tape is Tape Mode: wow and flutter, saturation, roll-off and hiss.
func (e *musicFXEngine) tape(l, r float64) (float64, float64) {
	p := &e.p
	if p.Wow > 0 {
		depth := float64(p.Wow) / 100
		// Wow ~0.6 Hz, flutter ~7 Hz and ~13 Hz, and a slow random drift of the
		// capstan, all in milliseconds of delay modulation.
		e.wowPhase += 2 * math.Pi * 0.6 / e.fs
		e.flutPhase += 2 * math.Pi * 7.1 / e.fs
		e.flut2Phase += 2 * math.Pi * 13.3 / e.fs
		if e.wowPhase > 2*math.Pi {
			e.wowPhase -= 2 * math.Pi
		}
		if e.flutPhase > 2*math.Pi {
			e.flutPhase -= 2 * math.Pi
		}
		if e.flut2Phase > 2*math.Pi {
			e.flut2Phase -= 2 * math.Pi
		}
		if e.driftCount--; e.driftCount <= 0 {
			e.driftCount = int(e.fs / 4)
			e.driftTarget = e.noise()
		}
		e.drift += (e.driftTarget - e.drift) * (4 / e.fs)
		ms := 8 + depth*(1.6*math.Sin(e.wowPhase)+0.12*math.Sin(e.flutPhase)+0.05*math.Sin(e.flut2Phase)+0.4*e.drift)
		d := ms * e.fs / 1000
		e.wowLine[0].push(l)
		e.wowLine[1].push(r)
		l = e.wowLine[0].at(d)
		r = e.wowLine[1].at(d)
	}
	if p.Saturation > 0 {
		mix := float64(p.Saturation) / 100
		l = l*(1-mix) + mix*math.Tanh(e.satK*l)*e.satN
		r = r*(1-mix) + mix*math.Tanh(e.satK*r)*e.satN
	}
	if p.RollOff > 0 {
		a := e.rollAlpha
		e.roll1[0] += a * (l - e.roll1[0])
		e.roll2[0] += a * (e.roll1[0] - e.roll2[0])
		e.roll1[1] += a * (r - e.roll1[1])
		e.roll2[1] += a * (e.roll1[1] - e.roll2[1])
		l, r = e.roll2[0], e.roll2[1]
	}
	if e.hissAmp > 0 {
		// White noise softened above ~9 kHz, different in each ear.
		const lp = 0.7
		e.hissLP[0] += lp * (e.noise() - e.hissLP[0])
		e.hissLP[1] += lp * (e.noise() - e.hissLP[1])
		l += e.hissLP[0] * e.hissAmp
		r += e.hissLP[1] * e.hissAmp
	}
	return l, r
}

// chorusFrame thickens the sound with a slowly moving 18–23 ms copy, the
// right ear a quarter cycle behind the left.
func (e *musicFXEngine) chorusFrame(l, r float64) (float64, float64) {
	m := float64(e.p.Chorus) / 100
	e.chPhase += 2 * math.Pi * 0.9 / e.fs
	if e.chPhase > 2*math.Pi {
		e.chPhase -= 2 * math.Pi
	}
	dl := (18 + 5*(0.5+0.5*math.Sin(e.chPhase))) * e.fs / 1000
	dr := (18 + 5*(0.5+0.5*math.Sin(e.chPhase+math.Pi/2))) * e.fs / 1000
	e.chorus[0].push(l)
	e.chorus[1].push(r)
	wl, wr := e.chorus[0].at(dl), e.chorus[1].at(dr)
	return l*(1-0.35*m) + 0.7*m*wl, r*(1-0.35*m) + 0.7*m*wr
}

// flangerFrame sweeps a 1–3.5 ms copy with feedback: the jet-plane comb.
func (e *musicFXEngine) flangerFrame(l, r float64) (float64, float64) {
	m := float64(e.p.Flanger) / 100
	e.flPhase += 2 * math.Pi * 0.22 / e.fs
	if e.flPhase > 2*math.Pi {
		e.flPhase -= 2 * math.Pi
	}
	d := (1 + 2.5*(0.5+0.5*math.Sin(e.flPhase))) * e.fs / 1000
	const fb = 0.55
	wl := e.flanger[0].at(d)
	wr := e.flanger[1].at(d)
	e.flanger[0].push(l + fb*wl)
	e.flanger[1].push(r + fb*wr)
	norm := 1 / (1 + 0.5*m)
	return (l + m*wl) * norm, (r + m*wr) * norm
}

// echoFrame is the ping-pong delay: the first echo in the left ear, the next
// in the right, each a little darker and quieter.
func (e *musicFXEngine) echoFrame(l, r float64) (float64, float64) {
	m := float64(e.p.Delay) / 100 * 0.8
	fb := float64(e.p.Feedback) / 100
	d := float64(e.p.DelayMs) * e.fs / 1000
	el := e.echoL.at(d)
	er := e.echoR.at(d)
	const damp = 0.45
	e.echoLP[0] += damp * (el - e.echoLP[0])
	e.echoLP[1] += damp * (er - e.echoLP[1])
	el, er = e.echoLP[0], e.echoLP[1]
	e.echoL.push((l+r)*0.5 + fb*er)
	e.echoR.push(fb * el)
	return l + m*el, r + m*er
}

// reverbFrame is Freeverb: the room size sets the combs' feedback.
func (e *musicFXEngine) reverbFrame(l, r float64) (float64, float64) {
	m := float64(e.p.Reverb) / 100
	in := (l + r) * 0.015
	const damp = 0.25
	var ol, or float64
	for i := 0; i < 8; i++ {
		ol += e.combs[0][i].process(in, e.roomFB, damp)
		or += e.combs[1][i].process(in, e.roomFB, damp)
	}
	for i := 0; i < 4; i++ {
		ol = e.allpasses[0][i].process(ol)
		or = e.allpasses[1][i].process(or)
	}
	return l*(1-0.3*m) + m*ol, r*(1-0.3*m) + m*or
}
