package main

// bootchime.go — the startup and shutdown chimes.
//
// Both are synthesised, not recorded: a few kilobytes of code instead of a
// megabyte of audio in the binary, rendered at whatever rate the device opens.
// The startup chime follows the splash's choreography (splash.go): a breath of
// filtered air as the glass pane rises, a warm D-flat major-ninth pad swelling
// under it, four glass bells climbing left to right as the wordmark comes into
// focus, and a three-note sparkle as the glint runs across it, all in a soft
// stereo room. The shutdown chime is its mirror: three bells falling back to
// the root over a pad that closes. Bells are two-operator FM with a harmonic
// 3:1 ratio, so they ring like glass rather than like a church bell, plus the
// faint second mode of a struck bar for the strike itself. Everything sits
// above 110 Hz, where the handheld's speakers actually play.
//
// The synthesis streams: a goroutine renders a block (about 20 ms) at a time
// and keeps roughly 150 ms queued on the chime's own stereo SDL device, so the
// first sound leaves within milliseconds of the request and the work is spread
// over the chime instead of landing on the boot as one burst. The device is
// closed once the tail has played. The UI sounds keep their own mono device
// (sound.go), so the teardown's "stop playback" step never cuts a chime.
//
// The scores and constants are mirrored by branding/sound/chime_reference.c,
// which renders the same chimes to WAV for listening; change both together.

import (
	"math"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// bootChimeEnabledKey is the Custom setting for the chimes: on unless the
// user turned it off. They also stay quiet while Interface sounds are off.
const bootChimeEnabledKey = "startupSound"

// bootChimeOn reports whether the chimes play for this config.
func bootChimeOn(c *Config) bool {
	if c == nil || !setUISoundsOn(c) || quietModeOn() || quietModeFromConfig(c) {
		return false
	}
	if v, ok := customValue(c.Variables.Custom, bootChimeEnabledKey); ok {
		return bootSfxParseBool(v, true)
	}
	return true
}

// bootChimeStart plays the startup chime as the splash comes up.
func bootChimeStart(config *Config) {
	chimeStart(config, chimeStartScore, chimeStartSeconds, chimeStartGain)
}

// shutdownChimeStart plays the shutdown chime as the teardown card appears.
// A stop requested by a signal (the firmware powering off) gets no chime: that
// exit is meant to be quick.
func shutdownChimeStart(config *Config) {
	if exitRequestedBySignal.Load() {
		return
	}
	chimeStart(config, chimeStopScore, chimeStopSeconds, chimeStopGain)
}

// shutdownChimeWait holds the exit until every chime has rung out and closed
// its device, so SDL is never shut down under one. It is bounded: a wedged
// audio device costs a moment, never the exit.
func shutdownChimeWait() {
	chimeState.mu.Lock()
	playing := append([]*chimePlayback(nil), chimeState.playing...)
	chimeState.mu.Unlock()
	// Every chime gives up chimeGrace after its own length (chimeRun), so this
	// bound always outlasts the longest one.
	deadline := time.After(time.Duration((chimeStartSeconds + chimeGrace + 0.5) * float64(time.Second)))
	for _, p := range playing {
		select {
		case <-p.done:
		case <-deadline:
			return
		}
	}
}

// bootChimePreview plays the startup chime from Settings, unless one is
// already sounding.
func bootChimePreview(c *Config) {
	chimeState.mu.Lock()
	busy := len(chimeState.playing) > 0
	chimeState.mu.Unlock()
	if !busy {
		chimeStart(c, chimeStartScore, chimeStartSeconds, chimeStartGain)
	}
}

// ── Scores ──────────────────────────────────────────────────────────────

type chimeKind uint8

const (
	chimePad chimeKind = iota
	chimeBell
	chimeAir
)

// chimeNote is one event of a chime.
type chimeNote struct {
	kind     chimeKind
	at       float64 // start, seconds into the chime
	freq     float64 // pitch, Hz (unused by air)
	length   float64 // total length, seconds
	attack   float64 // raised-cosine rise, seconds
	release  float64 // raised-cosine fall at the end, seconds
	decay    float64 // time constant: a pad settles towards its sustain, a bell dies away
	level    float64 // peak before the master gain
	pan      float64 // -1 left … +1 right
	index    float64 // bell: FM index at the strike
	indexTau float64 // bell: how fast the brightness fades, seconds
}

// Equal-tempered pitches, A4 = 440 Hz.
const (
	chimeDb4 = 277.1826
	chimeAb4 = 415.3047
	chimeC5  = 523.2511
	chimeDb5 = 554.3653
	chimeEb5 = 622.2540
	chimeF5  = 698.4565
	chimeAb5 = 830.6094
	chimeDb6 = 1108.7305
	chimeEb6 = 1244.5079
	chimeAb6 = 1661.2188
	chimeDb7 = 2217.4610
	chimeEb7 = 2489.0159
	chimeAb7 = 3322.4376
)

// chimeStartScore: air, the D-flat major-ninth pad (Db Ab C Eb F), the
// climbing bells (Ab Db Eb Ab, left to right) and the sparkle.
var chimeStartScore = []chimeNote{
	{chimeAir, 0.00, 0, 1.05, 0.36, 0.70, 0, 0.034, 0, 0, 0},
	{chimePad, 0.04, chimeDb4, 2.90, 0.55, 1.40, 0.9, 0.081, -0.30, 0, 0},
	{chimePad, 0.06, chimeAb4, 2.88, 0.55, 1.40, 0.9, 0.070, 0.25, 0, 0},
	{chimePad, 0.08, chimeC5, 2.86, 0.60, 1.40, 0.9, 0.054, -0.15, 0, 0},
	{chimePad, 0.10, chimeEb5, 2.84, 0.60, 1.40, 0.9, 0.054, 0.35, 0, 0},
	{chimePad, 0.12, chimeF5, 2.82, 0.65, 1.40, 0.9, 0.049, 0.05, 0, 0},
	{chimeBell, 0.30, chimeAb5, 1.60, 0.006, 0.30, 0.50, 0.150, -0.40, 1.4, 0.09},
	{chimeBell, 0.42, chimeDb6, 1.60, 0.006, 0.30, 0.50, 0.140, -0.15, 1.4, 0.09},
	{chimeBell, 0.54, chimeEb6, 1.70, 0.006, 0.30, 0.55, 0.135, 0.15, 1.4, 0.09},
	{chimeBell, 0.66, chimeAb6, 2.10, 0.006, 0.40, 0.80, 0.150, 0.40, 1.3, 0.11},
	{chimeBell, 0.84, chimeDb7, 0.70, 0.004, 0.20, 0.16, 0.055, -0.55, 0.9, 0.05},
	{chimeBell, 0.88, chimeEb7, 0.70, 0.004, 0.20, 0.16, 0.050, 0.00, 0.9, 0.05},
	{chimeBell, 0.92, chimeAb7, 0.70, 0.004, 0.20, 0.16, 0.045, 0.55, 0.9, 0.05},
}

// chimeStopScore: a closing pad and three bells falling back to the root,
// right to left.
var chimeStopScore = []chimeNote{
	{chimePad, 0.00, chimeDb4, 1.70, 0.08, 1.20, 0.6, 0.050, -0.30, 0, 0},
	{chimePad, 0.00, chimeAb4, 1.70, 0.08, 1.20, 0.6, 0.044, 0.25, 0, 0},
	{chimePad, 0.00, chimeF5, 1.60, 0.08, 1.10, 0.6, 0.032, 0.05, 0, 0},
	{chimeBell, 0.02, chimeAb5, 1.20, 0.006, 0.30, 0.42, 0.140, 0.35, 1.3, 0.09},
	{chimeBell, 0.17, chimeEb5, 1.30, 0.006, 0.30, 0.48, 0.140, 0.00, 1.3, 0.09},
	{chimeBell, 0.32, chimeDb5, 1.55, 0.006, 0.45, 0.70, 0.150, -0.35, 1.2, 0.11},
}

const (
	chimeStartSeconds = 3.30 // the start score's length, tail included
	chimeStopSeconds  = 2.10
	// The master gains put each chime's peak near 0.56 of full scale
	// (about -5 dBFS) at 48 kHz, before the interface-sound level.
	chimeStartGain = 1.55
	chimeStopGain  = 1.50

	chimeBellRatio  = 3.0    // FM modulator : carrier — harmonic, so glass rather than bronze
	chimeBarRatio   = 2.756  // a struck bar's second mode, heard only in the strike
	chimeBarLevel   = 0.08   // its level relative to the bell
	chimeBarTau     = 0.07   // and how fast it dies, seconds
	chimeDetune     = 0.0029 // the pad's two oscillators sit ±5 cents apart
	chimePadSustain = 0.72   // the level a pad settles to after its swell
	chimeHighPassHz = 110.0  // nothing below what the speakers can play
	chimeRoom       = 0.80   // reverb size (0..1)
	chimeDamp       = 0.45   // reverb high-frequency damping (0..1)
	chimeReverbIn   = 0.015  // the reverb's input gain (Freeverb's fixed gain)
	chimeReverbWet  = 0.35   // wet level against the dry signal
	chimeTailFade   = 0.18   // the last moment fades to exact silence, seconds
	chimeBlock      = 1024   // frames rendered per block (~21 ms at 48 kHz)
	chimeTableSize  = 4096   // samples per wavetable cycle
	chimeQueueAhead = 150 * time.Millisecond
	chimeGrace      = 1.0 // seconds a chime may overrun its length before it gives up
)

// ── Wavetables ──────────────────────────────────────────────────────────

var chimeTables struct {
	once sync.Once
	sin  [chimeTableSize + 1]float64
	pad  [chimeTableSize + 1]float64 // fundamental, octave and twelfth
}

func chimeTablesInit() {
	chimeTables.once.Do(func() {
		for i := 0; i <= chimeTableSize; i++ {
			x := 2 * math.Pi * float64(i) / chimeTableSize
			chimeTables.sin[i] = math.Sin(x)
			chimeTables.pad[i] = (math.Sin(x) + 0.30*math.Sin(2*x) + 0.11*math.Sin(3*x)) / 1.41
		}
	})
}

// chimeLookup reads a one-cycle table at a phase given in cycles (any real
// number), with linear interpolation.
func chimeLookup(tab *[chimeTableSize + 1]float64, cycles float64) float64 {
	x := (cycles - math.Floor(cycles)) * chimeTableSize
	i := int(x)
	if i >= chimeTableSize {
		// A phase a hair below a whole cycle can round up to it.
		i, x = 0, 0
	}
	return tab[i] + (tab[i+1]-tab[i])*(x-float64(i))
}

// chimeRcos is a raised cosine rising from 0 at x = 0 to 1 at x = 1.
func chimeRcos(x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	return 0.5 - 0.5*chimeLookup(&chimeTables.sin, x*0.5+0.25)
}

// ── Synthesis ───────────────────────────────────────────────────────────

// chimeVoice is one note's running state.
type chimeVoice struct {
	n              chimeNote
	start, length  int     // in frames
	gl, gr         float64 // constant-power pan gains
	p1, p2, pm, pb float64 // oscillator phases, in cycles
	dec, kdec      float64 // the note's decay and its per-frame factor
	idx, kidx      float64 // a bell's FM index and its per-frame factor
	bar, kbar      float64 // the strike partial's level and its factor
	hi, lo         float64 // air: the left noise band's two one-pole filters
	hi2, lo2       float64 // and the right one's
	ah, al         float64 // their coefficients
	drift, vib     float64 // a pad's slow pitch drift and its phase offset
	seed           uint32  // air: the noise generator
}

type chimeComb struct {
	buf   []float64
	pos   int
	store float64
}

type chimeAllpass struct {
	buf []float64
	pos int
}

// Freeverb's delay lengths at 44.1 kHz; the right side runs chimeSpread
// samples longer, which is what makes the room stereo.
var (
	chimeCombTune    = [8]int{1116, 1188, 1277, 1356, 1422, 1491, 1557, 1617}
	chimeAllpassTune = [4]int{556, 441, 341, 225}
)

const chimeSpread = 23

// chimeSynth renders one chime as interleaved stereo S16LE, block by block.
type chimeSynth struct {
	voices     []chimeVoice
	comb       [2][8]chimeComb
	allpass    [2][4]chimeAllpass
	fb, damp   float64
	rate       float64
	hpA        float64
	hl, hr     float64 // high-pass outputs
	xl, xr     float64 // and their previous inputs
	gain       float64
	frames     int
	pos        int
	mixL, mixR [chimeBlock]float64
}

func newChimeSynth(score []chimeNote, rate int, seconds, gain float64) *chimeSynth {
	chimeTablesInit()
	sr := float64(rate)
	c := &chimeSynth{
		voices: make([]chimeVoice, len(score)),
		rate:   sr,
		frames: int(math.Ceil(seconds * sr)),
		gain:   gain,
		fb:     chimeRoom*0.28 + 0.7,
		damp:   chimeDamp * 0.4,
		hpA:    1 / (1 + 2*math.Pi*chimeHighPassHz/sr),
	}
	for k, n := range score {
		v := &c.voices[k]
		v.n = n
		v.start = int(n.at * sr)
		v.length = int(n.length * sr)
		v.gl = math.Cos((n.pan + 1) * math.Pi / 4)
		v.gr = math.Sin((n.pan + 1) * math.Pi / 4)
		if n.kind == chimeAir {
			v.gl, v.gr = 0.70710678, 0.70710678
		}
		v.dec, v.kdec = 1, 1
		if n.decay > 0 {
			v.kdec = math.Exp(-1 / (n.decay * sr))
		}
		v.idx, v.kidx = n.index, 1
		if n.indexTau > 0 {
			v.kidx = math.Exp(-1 / (n.indexTau * sr))
		}
		v.bar = chimeBarLevel
		v.kbar = math.Exp(-1 / (chimeBarTau * sr))
		v.al = 1 - math.Exp(-2*math.Pi*380/sr)
		v.vib = float64(k) * 0.37
		v.drift = 1
		v.seed = 0x9E3779B9 ^ uint32(k+1)*0x85EBCA6B
	}
	scale := sr / 44100
	for side := 0; side < 2; side++ {
		for i, tune := range chimeCombTune {
			c.comb[side][i].buf = make([]float64, int(float64(tune+side*chimeSpread)*scale))
		}
		for i, tune := range chimeAllpassTune {
			c.allpass[side][i].buf = make([]float64, int(float64(tune+side*chimeSpread)*scale))
		}
	}
	return c
}

// chimeNoise is a xorshift step mapped to -1..1.
func chimeNoise(s *uint32) float64 {
	*s ^= *s << 13
	*s ^= *s >> 17
	*s ^= *s << 5
	return float64(*s)/4294967295.0*2 - 1
}

// voiceAdd mixes frames [from, to) of the chime's timeline from one voice into
// the block mix.
func (c *chimeSynth) voiceAdd(v *chimeVoice, from, to int) {
	n := &v.n
	sr := c.rate
	a, b := from, to
	if v.start > a {
		a = v.start
	}
	if end := v.start + v.length; end < b {
		b = end
	}
	for f := a; f < b; f++ {
		i := f - v.start
		t := float64(i) / sr
		e := chimeRcos(t/n.attack) * chimeRcos((n.length-t)/n.release) * n.level
		var l, r float64
		switch n.kind {
		case chimePad:
			e *= chimePadSustain + (1-chimePadSustain)*v.dec
			if i&63 == 0 {
				v.drift = 1 + 0.0009*chimeLookup(&chimeTables.sin, 0.31*t+v.vib)
			}
			v.p1 += n.freq * (1 + chimeDetune) * v.drift / sr
			v.p2 += n.freq * (1 - chimeDetune) * v.drift / sr
			if v.p1 >= 1 {
				v.p1--
			}
			if v.p2 >= 1 {
				v.p2--
			}
			l = 0.5 * (chimeLookup(&chimeTables.pad, v.p1) + chimeLookup(&chimeTables.pad, v.p2))
			r = l
		case chimeBell:
			e *= v.dec
			v.pm += n.freq * chimeBellRatio / sr
			v.p1 += n.freq / sr
			v.pb += n.freq * chimeBarRatio / sr
			if v.pm >= 1 {
				v.pm--
			}
			if v.p1 >= 1 {
				v.p1--
			}
			if v.pb >= 1 {
				v.pb--
			}
			l = chimeLookup(&chimeTables.sin, v.p1+v.idx*chimeLookup(&chimeTables.sin, v.pm)*(1/(2*math.Pi))) +
				v.bar*chimeLookup(&chimeTables.sin, v.pb)
			r = l
			v.idx *= v.kidx
			v.bar *= v.kbar
		case chimeAir:
			// Two independent noise bands, one per side; the upper edge
			// opens from 1.2 kHz to 7 kHz as the swell rises.
			if i&31 == 0 {
				v.ah = 1 - math.Exp(-2*math.Pi*(1200+5800*chimeRcos(t/0.50))/sr)
			}
			nl, nr := chimeNoise(&v.seed), chimeNoise(&v.seed)
			v.hi += v.ah * (nl - v.hi)
			v.lo += v.al * (nl - v.lo)
			v.hi2 += v.ah * (nr - v.hi2)
			v.lo2 += v.al * (nr - v.lo2)
			l = (v.hi - v.lo) * 2.2
			r = (v.hi2 - v.lo2) * 2.2
		}
		v.dec *= v.kdec
		c.mixL[f-from] += e * l * v.gl
		c.mixR[f-from] += e * r * v.gr
	}
}

// reverbSide runs one side of the room: eight damped combs in parallel, then
// four allpasses in series.
func (c *chimeSynth) reverbSide(side int, in float64) float64 {
	x := 0.0
	for i := range c.comb[side] {
		cb := &c.comb[side][i]
		y := cb.buf[cb.pos]
		cb.store = y*(1-c.damp) + cb.store*c.damp
		cb.buf[cb.pos] = in + cb.store*c.fb
		if cb.pos++; cb.pos >= len(cb.buf) {
			cb.pos = 0
		}
		x += y
	}
	for i := range c.allpass[side] {
		ap := &c.allpass[side][i]
		b := ap.buf[ap.pos]
		y := b - x
		ap.buf[ap.pos] = x + b*0.5
		if ap.pos++; ap.pos >= len(ap.buf) {
			ap.pos = 0
		}
		x = y
	}
	return x
}

// render appends the next block (at most chimeBlock frames) to dst as
// interleaved S16LE stereo. It returns dst unchanged once the chime is over.
func (c *chimeSynth) render(dst []byte) []byte {
	todo := c.frames - c.pos
	if todo > chimeBlock {
		todo = chimeBlock
	}
	if todo <= 0 {
		return dst
	}
	c.mixL = [chimeBlock]float64{}
	c.mixR = [chimeBlock]float64{}
	for k := range c.voices {
		c.voiceAdd(&c.voices[k], c.pos, c.pos+todo)
	}
	for j := 0; j < todo; j++ {
		dl, dr := c.mixL[j], c.mixR[j]
		in := (dl + dr) * chimeReverbIn
		outL := dl + chimeReverbWet*c.reverbSide(0, in)
		outR := dr + chimeReverbWet*c.reverbSide(1, in)
		c.hl = c.hpA * (c.hl + outL - c.xl)
		c.xl = outL
		c.hr = c.hpA * (c.hr + outR - c.xr)
		c.xr = outR
		fade := chimeRcos(float64(c.frames-c.pos-j)/c.rate/chimeTailFade) * c.gain
		dst = chimeAppendS16(dst, c.hl*fade)
		dst = chimeAppendS16(dst, c.hr*fade)
	}
	c.pos += todo
	return dst
}

// chimeAppendS16 appends one sample as signed 16-bit little-endian, clamped
// just short of full scale.
func chimeAppendS16(dst []byte, v float64) []byte {
	if v > 0.98 {
		v = 0.98
	} else if v < -0.98 {
		v = -0.98
	}
	s := int16(math.Round(v * 32767))
	return append(dst, byte(s), byte(uint16(s)>>8))
}

// ── Playback ────────────────────────────────────────────────────────────

// chimePlayback is one chime on its own device; done closes once the device
// is closed (or the chime could not start).
type chimePlayback struct {
	done chan struct{}
}

var chimeState struct {
	mu      sync.Mutex
	playing []*chimePlayback
}

// chimeStart plays a score at the interface-sound level, if the chimes are on.
// It returns at once.
func chimeStart(config *Config, score []chimeNote, seconds, gain float64) {
	if !bootChimeOn(config) {
		return
	}
	level := setUISoundLevel(config)
	if level <= 0 {
		return
	}
	// The same curve as the UI sounds: the slider's lower half stays useful.
	g := float64(level) / 100
	gain *= g * g
	p := &chimePlayback{done: make(chan struct{})}
	// The deadline starts now, before the device is opened, so a slow open
	// counts against it and shutdownChimeWait's bound always holds.
	deadline := time.Now().Add(time.Duration((seconds + chimeGrace) * float64(time.Second)))
	chimeState.mu.Lock()
	chimeState.playing = append(chimeState.playing, p)
	chimeState.mu.Unlock()
	go func() {
		defer func() {
			chimeState.mu.Lock()
			for i, q := range chimeState.playing {
				if q == p {
					chimeState.playing = append(chimeState.playing[:i], chimeState.playing[i+1:]...)
					break
				}
			}
			chimeState.mu.Unlock()
			close(p.done)
		}()
		chimeRun(score, seconds, gain, deadline)
	}()
}

// chimeRun opens a stereo device, streams the chime into it a block ahead of
// the speaker, waits for the tail to play and closes the device, giving up at
// deadline.
func chimeRun(score []chimeNote, seconds, gain float64, deadline time.Time) {
	desired := sdl.AudioSpec{
		Freq:     48000,
		Format:   sdl.AUDIO_S16LSB,
		Channels: 2,
		Samples:  1024,
	}
	var obtained sdl.AudioSpec
	dev, err := sdl.OpenAudioDevice("", false, &desired, &obtained, sdl.AUDIO_ALLOW_FREQUENCY_CHANGE)
	if err != nil || dev == 0 {
		logDebug("chime", "no audio device: %v", err)
		return
	}
	defer sdl.CloseAudioDevice(dev)
	rate := int(obtained.Freq)
	if rate <= 0 {
		rate = int(desired.Freq)
	}
	synth := newChimeSynth(score, rate, seconds, gain)
	ahead := uint32(float64(rate*4) * chimeQueueAhead.Seconds())
	buf := make([]byte, 0, chimeBlock*4)
	started := false
	for time.Now().Before(deadline) {
		finished := false
		for sdl.GetQueuedAudioSize(dev) < ahead {
			buf = synth.render(buf[:0])
			if len(buf) == 0 {
				finished = true
				break
			}
			if err := sdl.QueueAudio(dev, buf); err != nil {
				logDebug("chime", "queue: %v", err)
				return
			}
			// The LEDs follow the chime like they follow every other sound.
			publishAudioLevel(buf, rate, 2)
		}
		if !started {
			sdl.PauseAudioDevice(dev, false)
			started = true
		}
		if finished {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for sdl.GetQueuedAudioSize(dev) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// The device's own buffer is still playing when the queue reads empty.
	time.Sleep(60 * time.Millisecond)
}
