package main

// synth_engine.go — the Synth's sound: the project model (patterns, sounds,
// song settings) and the engine that turns it into stereo samples.
//
// The Synth is a pocket groovebox in the spirit of HUM on the TrimUI Brick:
// four drum lanes and three melodic lanes in a 16-step sequencer, a live
// play surface on the face buttons and a sound page per instrument. Nothing
// here touches SDL or the screen: synth.go owns the device and the page, and
// the same engine renders the live output and a WAV export.
//
// The DSP is deliberately small and cheap enough for the Brick's A133P at
// 48 kHz: polyBLEP oscillators, a TPT state-variable low-pass per voice with
// its coefficients updated every 32 samples, linear-attack exponential
// envelopes, four synthesized drums (no samples ship with it), a ping-pong
// echo and a soft clipper on the master.

import (
	"math"
)

const (
	synthLanes      = 7
	synthMaxSteps   = 16
	synthPatterns   = 8
	synthDrumLanes  = 4
	synthInsts      = 4
	synthMinBPM     = 40
	synthMaxBPM     = 240
	synthCtrlBlock  = 32
	synthScopeLen   = 512
	synthMaxDelaySe = 2.0 // seconds of echo buffer: 6 steps at 40 BPM is 2.25 s, clamped
)

// Lanes, top to bottom in the grid.
const (
	synthLaneKick = iota
	synthLaneSnare
	synthLaneHat
	synthLaneClap
	synthLaneBass
	synthLaneLead
	synthLaneChord
)

// Instruments: the drum kit and the three melodic voices. Tones[0] is unused
// apart from its Preset, the drum kit chosen.
const (
	synthInstDrums = iota
	synthInstBass
	synthInstLead
	synthInstChords
)

// Oscillator waves.
const (
	synthWaveSaw = iota
	synthWaveSquare
	synthWaveTriangle
	synthWaveSine
	synthWaveCount
)

var (
	synthLaneNames = [synthLanes]string{"Kick", "Snare", "Hat", "Clap", "Bass", "Lead", "Chords"}
	synthInstNames = [synthInsts]string{"Drums", "Bass", "Lead", "Chords"}
	synthWaveNames = [synthWaveCount]string{"Saw", "Square", "Triangle", "Sine"}
	synthKeyNames  = [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
)

// synthScale is a scale as semitones above the root.
type synthScale struct {
	name  string
	steps []int
}

var synthScales = []synthScale{
	{"Minor", []int{0, 2, 3, 5, 7, 8, 10}},
	{"Major", []int{0, 2, 4, 5, 7, 9, 11}},
	{"Dorian", []int{0, 2, 3, 5, 7, 9, 10}},
	{"Phrygian", []int{0, 1, 3, 5, 7, 8, 10}},
	{"Mixolydian", []int{0, 2, 4, 5, 7, 9, 10}},
	{"Harmonic minor", []int{0, 2, 3, 5, 7, 8, 11}},
	{"Minor pentatonic", []int{0, 3, 5, 7, 10}},
	{"Major pentatonic", []int{0, 2, 4, 7, 9}},
	{"Blues", []int{0, 3, 5, 6, 7, 10}},
}

// synthDelayTimes are the echo times, in sequencer steps (sixteenths).
var synthDelayTimes = []struct {
	name  string
	steps float64
}{
	{"1/16", 1}, {"1/8", 2}, {"Dotted 1/8", 3}, {"1/4", 4}, {"Dotted 1/4", 6},
}

// ── Project model ────────────────────────────────────────────────────────

// synthStep is one cell of a lane. Note is a scale degree above the key's
// root (melodic lanes only); Accent plays louder and brighter; Ratchet plays
// the drum twice in the step.
type synthStep struct {
	On      bool `json:"on,omitempty"`
	Note    int  `json:"n,omitempty"`
	Accent  bool `json:"a,omitempty"`
	Ratchet bool `json:"r,omitempty"`
}

type synthPattern struct {
	Lanes [synthLanes][synthMaxSteps]synthStep `json:"lanes"`
}

// empty reports whether no step of the pattern is on.
func (p *synthPattern) empty() bool {
	for l := range p.Lanes {
		for s := range p.Lanes[l] {
			if p.Lanes[l][s].On {
				return false
			}
		}
	}
	return true
}

// synthTone is a melodic instrument's sound. Every float is 0..1 and mapped to
// its real range by the engine, so the page can draw them all as one slider.
type synthTone struct {
	Preset  int     `json:"preset"`
	Wave    int     `json:"wave"`
	Cutoff  float64 `json:"cutoff"`
	Reso    float64 `json:"reso"`
	EnvAmt  float64 `json:"env"`
	Attack  float64 `json:"attack"`
	Decay   float64 `json:"decay"`
	Sustain float64 `json:"sustain"`
	Release float64 `json:"release"`
	Length  float64 `json:"length"`
	Glide   float64 `json:"glide"`
	Detune  float64 `json:"detune"`
	Octave  int     `json:"octave"`
	Volume  float64 `json:"volume"`
	Pan     float64 `json:"pan"`
	Delay   float64 `json:"delay"`
}

// synthDrum is one drum's sound.
type synthDrum struct {
	Tune   float64 `json:"tune"`
	Decay  float64 `json:"decay"`
	Volume float64 `json:"volume"`
	Pan    float64 `json:"pan"`
	Delay  float64 `json:"delay"`
}

// synthProject is everything the Synth saves: the song settings, the sounds
// and the eight patterns.
type synthProject struct {
	Version   int                         `json:"version"`
	BPM       int                         `json:"bpm"`
	Swing     float64                     `json:"swing"`
	Key       int                         `json:"key"`
	Scale     int                         `json:"scale"`
	Steps     int                         `json:"steps"`
	Chain     int                         `json:"chain"`
	DelayTime int                         `json:"delayTime"`
	DelayFB   float64                     `json:"delayFeedback"`
	Master    float64                     `json:"master"`
	Current   int                         `json:"current"`
	RecLive   bool                        `json:"recordLive"`
	Mute      [synthLanes]bool            `json:"mute"`
	Tones     [synthInsts]synthTone       `json:"tones"`
	Drums     [synthDrumLanes]synthDrum   `json:"drums"`
	Patterns  [synthPatterns]synthPattern `json:"patterns"`
}

const synthProjectVersion = 1

func synthClamp01(v float64) float64 {
	if v != v || v < 0 { // NaN too
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func synthClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sanitize pulls every field of a loaded project back into range, so a
// hand-edited or damaged synth.json can never index past a table or feed the
// filter a NaN.
func (p *synthProject) sanitize() {
	p.Version = synthProjectVersion
	if p.BPM == 0 {
		p.BPM = 112
	}
	p.BPM = synthClampInt(p.BPM, synthMinBPM, synthMaxBPM)
	p.Swing = synthClamp01(p.Swing)
	p.Key = synthClampInt(p.Key, 0, 11)
	p.Scale = synthClampInt(p.Scale, 0, len(synthScales)-1)
	if p.Steps == 0 {
		p.Steps = synthMaxSteps
	}
	p.Steps = synthClampInt(p.Steps, 4, synthMaxSteps)
	p.Chain = synthClampInt(p.Chain, 1, synthPatterns)
	p.DelayTime = synthClampInt(p.DelayTime, 0, len(synthDelayTimes)-1)
	p.DelayFB = math.Min(synthClamp01(p.DelayFB), 0.9)
	p.Master = synthClamp01(p.Master)
	p.Current = synthClampInt(p.Current, 0, synthPatterns-1)
	p.Tones[0].Preset = synthClampInt(p.Tones[0].Preset, 0, len(synthDrumKits)-1)
	for i := synthInstBass; i < synthInsts; i++ {
		t := &p.Tones[i]
		t.Preset = synthClampInt(t.Preset, 0, len(synthPresets[i])-1)
		t.Wave = synthClampInt(t.Wave, 0, synthWaveCount-1)
		for _, f := range []*float64{&t.Cutoff, &t.Reso, &t.EnvAmt, &t.Attack, &t.Decay, &t.Sustain,
			&t.Release, &t.Length, &t.Glide, &t.Detune, &t.Volume, &t.Pan, &t.Delay} {
			*f = synthClamp01(*f)
		}
		t.Octave = synthClampInt(t.Octave, -2, 2)
	}
	for i := range p.Drums {
		d := &p.Drums[i]
		for _, f := range []*float64{&d.Tune, &d.Decay, &d.Volume, &d.Pan, &d.Delay} {
			*f = synthClamp01(*f)
		}
	}
	for pi := range p.Patterns {
		for l := range p.Patterns[pi].Lanes {
			for s := range p.Patterns[pi].Lanes[l] {
				st := &p.Patterns[pi].Lanes[l][s]
				st.Note = synthClampInt(st.Note, synthMinDegree, synthMaxDegree)
			}
		}
	}
}

// Scale degrees a step may hold: two octaves down to three up of a
// heptatonic scale, which keeps every note inside MIDI 0..127 for any octave.
const (
	synthMinDegree = -14
	synthMaxDegree = 21
)

// synthPreset is a named starting sound for one melodic instrument.
type synthPreset struct {
	name string
	tone synthTone
}

// synthPresets are the factory sounds, by instrument (index 0, the drums, has
// kits instead: synthDrumKits).
var synthPresets = [synthInsts][]synthPreset{
	synthInstDrums: nil,
	synthInstBass: {
		{"Acid", synthTone{Wave: synthWaveSaw, Cutoff: 0.22, Reso: 0.78, EnvAmt: 0.62, Attack: 0, Decay: 0.32, Sustain: 0.08, Release: 0.15, Length: 0.3, Glide: 0.18, Volume: 0.78, Pan: 0.5, Delay: 0.05}},
		{"Sub", synthTone{Wave: synthWaveSine, Cutoff: 0.55, Reso: 0.05, EnvAmt: 0.05, Attack: 0.08, Decay: 0.5, Sustain: 0.85, Release: 0.2, Length: 0.45, Glide: 0.05, Volume: 0.9, Pan: 0.5}},
		{"Pluck", synthTone{Wave: synthWaveSquare, Cutoff: 0.28, Reso: 0.35, EnvAmt: 0.5, Attack: 0, Decay: 0.25, Sustain: 0, Release: 0.18, Length: 0.3, Volume: 0.8, Pan: 0.5, Delay: 0.08}},
		{"Reese", synthTone{Wave: synthWaveSaw, Cutoff: 0.33, Reso: 0.2, EnvAmt: 0.15, Attack: 0.06, Decay: 0.6, Sustain: 0.8, Release: 0.25, Length: 0.5, Detune: 0.55, Volume: 0.75, Pan: 0.5}},
	},
	synthInstLead: {
		{"Square lead", synthTone{Wave: synthWaveSquare, Cutoff: 0.55, Reso: 0.3, EnvAmt: 0.3, Attack: 0.05, Decay: 0.4, Sustain: 0.6, Release: 0.3, Length: 0.35, Glide: 0.1, Volume: 0.6, Pan: 0.42, Delay: 0.3}},
		{"Saw lead", synthTone{Wave: synthWaveSaw, Cutoff: 0.5, Reso: 0.45, EnvAmt: 0.4, Attack: 0.03, Decay: 0.35, Sustain: 0.55, Release: 0.3, Length: 0.35, Glide: 0.12, Detune: 0.25, Volume: 0.55, Pan: 0.42, Delay: 0.3}},
		{"Bell", synthTone{Wave: synthWaveTriangle, Cutoff: 0.85, Reso: 0.1, EnvAmt: 0.2, Attack: 0, Decay: 0.55, Sustain: 0, Release: 0.6, Length: 0.3, Volume: 0.7, Pan: 0.6, Delay: 0.45}},
		{"Chip", synthTone{Wave: synthWaveSquare, Cutoff: 1, Reso: 0, EnvAmt: 0, Attack: 0, Decay: 0.2, Sustain: 0.5, Release: 0.05, Length: 0.3, Volume: 0.45, Pan: 0.5, Delay: 0.2}},
	},
	synthInstChords: {
		{"Pad", synthTone{Wave: synthWaveSaw, Cutoff: 0.4, Reso: 0.15, EnvAmt: 0.2, Attack: 0.55, Decay: 0.7, Sustain: 0.75, Release: 0.65, Length: 0.85, Detune: 0.4, Volume: 0.5, Pan: 0.5, Delay: 0.3}},
		{"Stab", synthTone{Wave: synthWaveSaw, Cutoff: 0.32, Reso: 0.5, EnvAmt: 0.6, Attack: 0, Decay: 0.25, Sustain: 0, Release: 0.2, Length: 0.25, Volume: 0.55, Pan: 0.55, Delay: 0.4}},
		{"Organ", synthTone{Wave: synthWaveSine, Cutoff: 0.7, Reso: 0, EnvAmt: 0, Attack: 0.03, Decay: 0.5, Sustain: 0.9, Release: 0.15, Length: 0.6, Detune: 0.15, Volume: 0.55, Pan: 0.5, Delay: 0.1}},
		{"Keys", synthTone{Wave: synthWaveTriangle, Cutoff: 0.6, Reso: 0.1, EnvAmt: 0.25, Attack: 0, Decay: 0.5, Sustain: 0.3, Release: 0.4, Length: 0.5, Volume: 0.6, Pan: 0.5, Delay: 0.2}},
	},
}

// synthDrumKits are the drum presets: one sound per drum lane.
var synthDrumKits = []struct {
	name  string
	drums [synthDrumLanes]synthDrum
}{
	{"808", [synthDrumLanes]synthDrum{
		{Tune: 0.45, Decay: 0.7, Volume: 0.9, Pan: 0.5},
		{Tune: 0.5, Decay: 0.4, Volume: 0.7, Pan: 0.5, Delay: 0.05},
		{Tune: 0.5, Decay: 0.15, Volume: 0.45, Pan: 0.62},
		{Tune: 0.5, Decay: 0.4, Volume: 0.6, Pan: 0.42, Delay: 0.15},
	}},
	{"Punchy", [synthDrumLanes]synthDrum{
		{Tune: 0.6, Decay: 0.35, Volume: 0.95, Pan: 0.5},
		{Tune: 0.65, Decay: 0.3, Volume: 0.75, Pan: 0.5},
		{Tune: 0.7, Decay: 0.08, Volume: 0.4, Pan: 0.6},
		{Tune: 0.6, Decay: 0.25, Volume: 0.6, Pan: 0.45},
	}},
	{"Lo-fi", [synthDrumLanes]synthDrum{
		{Tune: 0.3, Decay: 0.5, Volume: 0.85, Pan: 0.5},
		{Tune: 0.35, Decay: 0.55, Volume: 0.6, Pan: 0.5, Delay: 0.2},
		{Tune: 0.25, Decay: 0.3, Volume: 0.35, Pan: 0.65, Delay: 0.1},
		{Tune: 0.3, Decay: 0.6, Volume: 0.55, Pan: 0.4, Delay: 0.3},
	}},
}

// synthApplyPreset loads preset i into instrument inst (the drum kit for
// the drums), keeping the octave the user chose.
func synthApplyPreset(p *synthProject, inst, i int) {
	if inst == synthInstDrums {
		i = synthClampInt(i, 0, len(synthDrumKits)-1)
		p.Tones[0].Preset = i
		p.Drums = synthDrumKits[i].drums
		return
	}
	if inst < synthInstBass || inst >= synthInsts {
		return
	}
	list := synthPresets[inst]
	i = synthClampInt(i, 0, len(list)-1)
	oct := p.Tones[inst].Octave
	p.Tones[inst] = list[i].tone
	p.Tones[inst].Preset = i
	p.Tones[inst].Octave = oct
}

// synthPresetName is the name of instrument inst's current preset or kit.
func synthPresetName(p *synthProject, inst int) string {
	if inst == synthInstDrums {
		return synthDrumKits[synthClampInt(p.Tones[0].Preset, 0, len(synthDrumKits)-1)].name
	}
	list := synthPresets[inst]
	return list[synthClampInt(p.Tones[inst].Preset, 0, len(list)-1)].name
}

// synthDefaultProject is the project a first visit opens: a short A-minor
// groove in patterns 1 and 2, so Start makes music straight away.
func synthDefaultProject() synthProject {
	p := synthProject{
		Version:   synthProjectVersion,
		BPM:       112,
		Swing:     0.12,
		Key:       9,
		Steps:     synthMaxSteps,
		Chain:     1,
		DelayTime: 2,
		DelayFB:   0.35,
		Master:    0.8,
	}
	synthApplyPreset(&p, synthInstDrums, 0)
	synthApplyPreset(&p, synthInstBass, 0)
	synthApplyPreset(&p, synthInstLead, 0)
	synthApplyPreset(&p, synthInstChords, 0)
	synthDemoPatterns(&p)
	return p
}

// synthDemoPatterns writes the demo groove into patterns 1 and 2.
func synthDemoPatterns(p *synthProject) {
	on := func(pat, lane, step, note int, accent bool) {
		p.Patterns[pat].Lanes[lane][step] = synthStep{On: true, Note: note, Accent: accent}
	}
	for pat := 0; pat < 2; pat++ {
		p.Patterns[pat] = synthPattern{}
		for s := 0; s < 16; s += 4 {
			on(pat, synthLaneKick, s, 0, s == 0)
		}
		on(pat, synthLaneSnare, 4, 0, false)
		on(pat, synthLaneSnare, 12, 0, true)
		for s := 0; s < 16; s += 2 {
			on(pat, synthLaneHat, s, 0, false)
			on(pat, synthLaneHat, s+1, 0, s%4 == 2)
		}
		on(pat, synthLaneClap, 12, 0, false)
		// Bass: root pulses with a climb to the fifth.
		for i, n := range []int{0, -7, 0, 2, 0, -7, 4, 3} {
			on(pat, synthLaneBass, i*2, n, i == 0)
		}
		// Chords: i and VI, then VII and i in the second pattern.
		if pat == 0 {
			on(pat, synthLaneChord, 0, 0, false)
			on(pat, synthLaneChord, 8, 5, false)
		} else {
			on(pat, synthLaneChord, 0, 6, false)
			on(pat, synthLaneChord, 8, 0, false)
		}
	}
	for _, s := range [][2]int{{0, 4}, {3, 2}, {6, 4}, {8, 7}, {11, 6}, {14, 4}} {
		on(0, synthLaneLead, s[0], s[1], false)
	}
	for _, s := range [][2]int{{0, 6}, {3, 4}, {6, 2}, {10, 3}, {12, 4}} {
		on(1, synthLaneLead, s[0], s[1], s[0] == 12)
	}
	// The second pattern's kick skips a beat for a little push.
	p.Patterns[1].Lanes[synthLaneKick][8] = synthStep{}
	p.Patterns[1].Lanes[synthLaneKick][10] = synthStep{On: true}
	p.Patterns[1].Lanes[synthLaneHat][15] = synthStep{On: true, Ratchet: true}
}

// synthDegreeMidi is the MIDI note of scale degree deg for instrument inst.
func synthDegreeMidi(p *synthProject, inst, deg int) int {
	sc := synthScales[synthClampInt(p.Scale, 0, len(synthScales)-1)].steps
	n := len(sc)
	oct := deg / n
	idx := deg % n
	if idx < 0 {
		idx += n
		oct--
	}
	base := 48
	switch inst {
	case synthInstBass:
		base = 36
	case synthInstLead:
		base = 60
	}
	oc := 0
	if inst >= synthInstBass && inst < synthInsts {
		oc = p.Tones[inst].Octave
	}
	m := base + p.Key + 12*(oct+oc) + sc[idx]
	return synthClampInt(m, 0, 127)
}

// synthNoteName spells a MIDI note ("A3").
func synthNoteName(m int) string {
	m = synthClampInt(m, 0, 127)
	o := m/12 - 1
	name := synthKeyNames[m%12]
	if o < 0 {
		return name + "-" + string(rune('0'-o))
	}
	return name + string(rune('0'+o))
}

func synthMidiHz(m float64) float64 { return 440 * math.Pow(2, (m-69)/12) }

// ── Engine ───────────────────────────────────────────────────────────────

const (
	synthEnvOff = iota
	synthEnvAttack
	synthEnvDecay
	synthEnvRelease
)

// synthVoice is one melodic voice: two oscillators, a low-pass and its
// envelopes.
type synthVoice struct {
	stage   int
	level   float64
	fenv    float64
	gate    int // samples until release; 0 = already released
	freq    float64
	target  float64
	ph1     float64
	ph2     float64
	vel     float64
	accent  bool
	age     uint64
	ic1     float64
	ic2     float64
	a1      float64
	a2      float64
	a3      float64
	k       float64
	ctrlCnt int
}

// synthDrumVoice is one drum's running hit.
type synthDrumVoice struct {
	active bool
	t      int
	vel    float64
	ph     float64
	metal  [6]float64
	hp1    float64
	hp1x   float64
	hp2    float64
	hp2x   float64
	bp1    float64
	bp2    float64
}

// synthToneRT is a tone's parameters in engine units, worked out once per
// block.
type synthToneRT struct {
	wave      int
	aInc      float64
	dCoef     float64
	rCoef     float64
	fCoef     float64
	sus       float64
	cutHz     float64
	k         float64
	envOct    float64
	detune    float64
	gain      float64
	panL      float64
	panR      float64
	send      float64
	glideCoef float64
	length    float64
}

// synthEngine renders a project. Its caller serialises access: the live
// device holds synth.mu around render and every note call.
type synthEngine struct {
	rate   float64
	proj   *synthProject
	pools  [synthInsts][]synthVoice
	drums  [synthDrumLanes]synthDrumVoice
	rt     [synthInsts]synthToneRT
	ageCtr uint64
	noise  uint32

	playing   bool
	step      int
	pat       int
	firedPat  int // the pattern of the step sounding now
	untilStep float64
	stepLen   float64 // the current step's length in samples (swing applied)
	ratchetIn [synthDrumLanes]int
	lastFreq  [synthInsts]float64

	delayL []float32
	delayR []float32
	dpos   int
	dlpL   float64
	dlpR   float64

	scope    [synthScopeLen]float32
	scopePos int
	flash    [synthLanes]float32
	peak     float32
}

func newSynthEngine(rate int) *synthEngine {
	if rate < 8000 {
		rate = 48000
	}
	e := &synthEngine{rate: float64(rate), noise: 0x9e3779b9}
	e.pools[synthInstBass] = make([]synthVoice, 2)
	e.pools[synthInstLead] = make([]synthVoice, 3)
	e.pools[synthInstChords] = make([]synthVoice, 9)
	n := int(e.rate * synthMaxDelaySe)
	e.delayL = make([]float32, n)
	e.delayR = make([]float32, n)
	return e
}

func (e *synthEngine) rnd() float64 {
	x := e.noise
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	e.noise = x
	return float64(int32(x)) / 2147483648.0
}

// baseStepSamples is one sixteenth at the project's tempo.
func (e *synthEngine) baseStepSamples() float64 {
	bpm := 112
	if e.proj != nil {
		bpm = synthClampInt(e.proj.BPM, synthMinBPM, synthMaxBPM)
	}
	return e.rate * 60 / float64(bpm) / 4
}

// stepSamples is step i's length: swing lengthens the even steps and shortens
// the odd ones by the same amount, so a pair always spans two sixteenths.
func (e *synthEngine) stepSamples(i int) float64 {
	base := e.baseStepSamples()
	sw := 0.0
	if e.proj != nil {
		sw = synthClamp01(e.proj.Swing) * 0.5
		// An odd bar's last step has no partner to borrow from: it stays
		// straight, so the next bar starts on time.
		if n := synthClampInt(e.proj.Steps, 4, synthMaxSteps); n%2 == 1 && i == n-1 {
			sw = 0
		}
	}
	if i%2 == 0 {
		return base * (1 + sw)
	}
	return base * (1 - sw)
}

// start begins playback at the top of the current pattern (or of the chain).
func (e *synthEngine) start() {
	if e.proj == nil {
		return
	}
	e.playing = true
	e.step = 0
	e.pat = e.proj.Current
	if e.proj.Chain > 1 {
		e.pat = 0
	}
	e.untilStep = 0
}

// stop ends playback and lets every held note ring out.
func (e *synthEngine) stop() {
	e.playing = false
	for i := range e.ratchetIn {
		e.ratchetIn[i] = 0
	}
	for inst := range e.pools {
		for v := range e.pools[inst] {
			if e.pools[inst][v].stage != synthEnvOff {
				e.pools[inst][v].stage = synthEnvRelease
				e.pools[inst][v].gate = 0
			}
		}
	}
}

// panic silences everything at once (leaving the page, a project load).
func (e *synthEngine) silence() {
	e.playing = false
	for inst := range e.pools {
		for v := range e.pools[inst] {
			e.pools[inst][v] = synthVoice{}
		}
	}
	for d := range e.drums {
		e.drums[d] = synthDrumVoice{}
	}
	for i := range e.delayL {
		e.delayL[i], e.delayR[i] = 0, 0
	}
	e.dlpL, e.dlpR = 0, 0
}

// updateRT converts the project's tones into engine units.
func (e *synthEngine) updateRT() {
	if e.proj == nil {
		return
	}
	r := e.rate
	for inst := synthInstBass; inst < synthInsts; inst++ {
		t := &e.proj.Tones[inst]
		rt := &e.rt[inst]
		rt.wave = synthClampInt(t.Wave, 0, synthWaveCount-1)
		att := 0.001 * math.Pow(1500, synthClamp01(t.Attack)) // 1 ms .. 1.5 s
		dec := 0.02 * math.Pow(150, synthClamp01(t.Decay))    // 20 ms .. 3 s
		rel := 0.01 * math.Pow(300, synthClamp01(t.Release))  // 10 ms .. 3 s
		rt.aInc = 1 / (att * r)
		rt.dCoef = math.Exp(-1 / (dec * r / 3))
		rt.rCoef = math.Exp(-1 / (rel * r / 3))
		rt.fCoef = math.Exp(-1 / (dec * r / 2.5))
		rt.sus = synthClamp01(t.Sustain)
		rt.cutHz = 40 * math.Pow(400, synthClamp01(t.Cutoff)) // 40 Hz .. 16 kHz
		rt.k = 2 - 1.9*synthClamp01(t.Reso)                   // Q 0.5 .. 10
		rt.envOct = 6 * synthClamp01(t.EnvAmt)
		rt.detune = math.Pow(2, 0.35*synthClamp01(t.Detune)/12) // up to 35 cents
		vol := synthClamp01(t.Volume)
		rt.gain = vol * vol * 0.55
		if inst == synthInstChords {
			rt.gain *= 0.75 // three voices per chord
		}
		pan := synthClamp01(t.Pan)
		rt.panL = math.Cos(pan * math.Pi / 2)
		rt.panR = math.Sin(pan * math.Pi / 2)
		rt.send = synthClamp01(t.Delay)
		if g := synthClamp01(t.Glide); g > 0.001 {
			sec := 0.3 * g * g
			rt.glideCoef = 1 - math.Exp(-1/(sec*r))
		} else {
			rt.glideCoef = 1
		}
		rt.length = 0.1 + 1.9*synthClamp01(t.Length)
	}
}

// noteOn starts a melodic note: inst's pool gives a free voice, else its
// oldest. Bass and lead are monophonic in feel: the note before is released,
// and with glide the new one slides from where the last one was.
func (e *synthEngine) noteOn(inst, midi int, vel float64, accent bool, gateSamples int) {
	if inst < synthInstBass || inst >= synthInsts || e.proj == nil {
		return
	}
	pool := e.pools[inst]
	if len(pool) == 0 {
		return
	}
	pick := -1
	for i := range pool {
		if pool[i].stage == synthEnvOff {
			pick = i
			break
		}
	}
	if pick < 0 {
		pick = 0
		for i := range pool {
			if pool[i].age < pool[pick].age {
				pick = i
			}
		}
	}
	mono := inst == synthInstBass || inst == synthInstLead
	if mono {
		for i := range pool {
			if i != pick && pool[i].stage != synthEnvOff && pool[i].stage != synthEnvRelease {
				pool[i].stage = synthEnvRelease
				pool[i].gate = 0
			}
		}
	}
	hz := synthMidiHz(float64(midi))
	e.ageCtr++
	v := &pool[pick]
	start := hz
	if mono && e.rt[inst].glideCoef < 1 && e.lastFreq[inst] > 0 {
		start = e.lastFreq[inst]
	}
	*v = synthVoice{stage: synthEnvAttack, fenv: 1, gate: gateSamples, freq: start, target: hz, vel: vel, accent: accent, age: e.ageCtr, ph2: 0.37}
	if gateSamples <= 0 {
		v.gate = 1
	}
	e.lastFreq[inst] = hz
}

// chordOn plays the triad on scale degree deg: deg, deg+2 and deg+4.
func (e *synthEngine) chordOn(deg int, vel float64, accent bool, gateSamples int) {
	for _, d := range [3]int{0, 2, 4} {
		e.noteOn(synthInstChords, synthDegreeMidi(e.proj, synthInstChords, deg+d), vel, accent, gateSamples)
	}
}

// drumHit starts drum d.
func (e *synthEngine) drumHit(d int, vel float64) {
	if d < 0 || d >= synthDrumLanes {
		return
	}
	e.drums[d] = synthDrumVoice{active: true, vel: vel}
}

// playLane sounds lane's step st now (the sequencer, live play and the
// page's audition all come through here).
func (e *synthEngine) playLane(lane int, st synthStep, gateSamples int) {
	if e.proj == nil {
		return
	}
	vel := 0.78
	if st.Accent {
		vel = 1
	}
	if lane >= 0 && lane < synthLanes {
		e.flash[lane] = 1
	}
	switch {
	case lane < synthDrumLanes:
		e.drumHit(lane, vel)
	case lane == synthLaneChord:
		e.chordOn(st.Note, vel, st.Accent, gateSamples)
	default:
		inst := synthLaneInst(lane)
		e.noteOn(inst, synthDegreeMidi(e.proj, inst, st.Note), vel, st.Accent, gateSamples)
	}
}

// synthLaneInst is the instrument that plays a lane.
func synthLaneInst(lane int) int {
	switch lane {
	case synthLaneBass:
		return synthInstBass
	case synthLaneLead:
		return synthInstLead
	case synthLaneChord:
		return synthInstChords
	}
	return synthInstDrums
}

// fireStep plays every unmuted lane's step at the playhead.
func (e *synthEngine) fireStep() {
	p := e.proj
	e.firedPat = synthClampInt(e.pat, 0, synthPatterns-1)
	pat := &p.Patterns[e.firedPat]
	stepLen := e.stepSamples(e.step)
	for lane := 0; lane < synthLanes; lane++ {
		if p.Mute[lane] {
			continue
		}
		st := pat.Lanes[lane][e.step]
		if !st.On {
			continue
		}
		gate := 0
		if inst := synthLaneInst(lane); inst != synthInstDrums {
			gate = int(stepLen * e.rt[inst].length)
		}
		e.playLane(lane, st, gate)
		if lane < synthDrumLanes && st.Ratchet {
			e.ratchetIn[lane] = int(stepLen / 2)
		}
	}
}

// advance moves the playhead one step; at the end of the bar it follows the
// chain, or picks up a pattern change the page asked for.
func (e *synthEngine) advance() {
	steps := synthClampInt(e.proj.Steps, 4, synthMaxSteps)
	e.step++
	if e.step >= steps {
		e.step = 0
		if e.proj.Chain > 1 {
			e.pat = (e.pat + 1) % synthClampInt(e.proj.Chain, 1, synthPatterns)
		} else {
			e.pat = e.proj.Current
		}
	}
}

// recordStep is the step a live note lands on: the one playing, or the next
// one when the press came in its second half.
func (e *synthEngine) recordStep() (pat, step int) {
	steps := synthClampInt(e.proj.Steps, 4, synthMaxSteps)
	step = e.step - 1 // fireStep has already advanced past the sounding step
	if step < 0 {
		step = steps - 1
	}
	pat = e.firedPat
	if e.stepLen > 0 && e.untilStep < e.stepLen/2 {
		step++
		if step >= steps {
			// The next step is the top of the next bar, which may be another
			// pattern of the chain.
			step = 0
			pat = synthClampInt(e.pat, 0, synthPatterns-1)
		}
	}
	return pat, step
}

// playingStep is the step sounding now, or -1 when stopped.
func (e *synthEngine) playingStep() int {
	if !e.playing {
		return -1
	}
	steps := synthClampInt(e.proj.Steps, 4, synthMaxSteps)
	s := e.step - 1
	if s < 0 {
		s = steps - 1
	}
	return s
}

func synthPolyBLEP(t, dt float64) float64 {
	if dt <= 0 {
		return 0
	}
	if t < dt {
		t /= dt
		return t + t - t*t - 1
	}
	if t > 1-dt {
		t = (t - 1) / dt
		return t*t + t + t + 1
	}
	return 0
}

func synthOsc(wave int, ph, dt float64) float64 {
	switch wave {
	case synthWaveSquare:
		v := 1.0
		if ph >= 0.5 {
			v = -1
		}
		ph2 := ph + 0.5
		if ph2 >= 1 {
			ph2--
		}
		return v + synthPolyBLEP(ph, dt) - synthPolyBLEP(ph2, dt)
	case synthWaveTriangle:
		return 1 - 4*math.Abs(ph-0.5)
	case synthWaveSine:
		return math.Sin(2 * math.Pi * ph)
	}
	return 2*ph - 1 - synthPolyBLEP(ph, dt)
}

// voiceSample advances one voice by a sample and returns its output.
func (e *synthEngine) voiceSample(v *synthVoice, rt *synthToneRT) float64 {
	// Envelope.
	switch v.stage {
	case synthEnvAttack:
		v.level += rt.aInc
		if v.level >= 1 {
			v.level = 1
			v.stage = synthEnvDecay
		}
	case synthEnvDecay:
		v.level = rt.sus + (v.level-rt.sus)*rt.dCoef
	case synthEnvRelease:
		v.level *= rt.rCoef
		if v.level < 0.0002 {
			v.stage = synthEnvOff
			v.level = 0
			return 0
		}
	}
	if v.gate > 0 {
		v.gate--
		if v.gate == 0 && v.stage != synthEnvRelease {
			v.stage = synthEnvRelease
		}
	}
	v.fenv *= rt.fCoef
	// Pitch.
	if v.freq != v.target {
		v.freq += (v.target - v.freq) * rt.glideCoef
		if math.Abs(v.freq-v.target) < 0.01 {
			v.freq = v.target
		}
	}
	// Filter coefficients at control rate.
	if v.ctrlCnt <= 0 {
		v.ctrlCnt = synthCtrlBlock
		cut := rt.cutHz * math.Exp2(rt.envOct*v.fenv)
		if v.accent {
			cut *= 1.5
		}
		if lim := e.rate * 0.45; cut > lim {
			cut = lim
		}
		if cut < 20 {
			cut = 20
		}
		g := math.Tan(math.Pi * cut / e.rate)
		k := rt.k
		if v.accent && k > 0.3 {
			k -= 0.15
		}
		v.k = k
		v.a1 = 1 / (1 + g*(g+k))
		v.a2 = g * v.a1
		v.a3 = g * v.a2
	}
	v.ctrlCnt--
	// Oscillators.
	dt := v.freq / e.rate
	s := synthOsc(rt.wave, v.ph1, dt)
	v.ph1 += dt
	if v.ph1 >= 1 {
		v.ph1--
	}
	if rt.detune > 1.0001 {
		dt2 := dt * rt.detune
		s = 0.6 * (s + synthOsc(rt.wave, v.ph2, dt2))
		v.ph2 += dt2
		if v.ph2 >= 1 {
			v.ph2--
		}
	}
	// TPT state-variable low-pass (Zavalishin / Simper).
	v3 := s - v.ic2
	v1 := v.a1*v.ic1 + v.a2*v3
	v2 := v.ic2 + v.a2*v.ic1 + v.a3*v3
	v.ic1 = 2*v1 - v.ic1
	v.ic2 = 2*v2 - v.ic2
	return v2 * v.level * v.vel
}

// drumSample advances drum d by a sample.
func (e *synthEngine) drumSample(d int, dv *synthDrumVoice, prm *synthDrum) float64 {
	ts := float64(dv.t) / e.rate
	dv.t++
	tune := math.Pow(2, (synthClamp01(prm.Tune)-0.5)*1.5)
	dec := synthClamp01(prm.Decay)
	var out, amp float64
	switch d {
	case synthLaneKick:
		f := 50 * tune * (1 + 3.2*math.Exp(-ts/0.03))
		dv.ph += f / e.rate
		if dv.ph >= 1 {
			dv.ph--
		}
		amp = math.Exp(-ts / (0.06 + 0.5*dec))
		out = math.Sin(2*math.Pi*dv.ph) * amp
		if ts < 0.004 {
			out += e.rnd() * 0.35 * (1 - ts/0.004)
		}
		out = math.Tanh(out * 1.4)
	case synthLaneSnare:
		f := 185 * tune * (1 + 0.6*math.Exp(-ts/0.012))
		dv.ph += f / e.rate
		if dv.ph >= 1 {
			dv.ph--
		}
		tone := math.Sin(2*math.Pi*dv.ph) * math.Exp(-ts/0.045)
		n := e.rnd()
		a := 1 / (1 + 2*math.Pi*1400*tune/e.rate)
		dv.hp1 = a * (dv.hp1 + n - dv.hp1x)
		dv.hp1x = n
		amp = math.Exp(-ts / (0.05 + 0.22*dec))
		out = 0.55*tone + 0.75*dv.hp1*amp
		if a := math.Exp(-ts / 0.045); a > amp {
			amp = a
		}
	case synthLaneHat:
		var metal float64
		for i, r := range [6]float64{205.3, 304.4, 369.6, 522.7, 540, 800} {
			dv.metal[i] += r * 1.7 * tune / e.rate
			if dv.metal[i] >= 1 {
				dv.metal[i]--
			}
			if dv.metal[i] < 0.5 {
				metal++
			} else {
				metal--
			}
		}
		x := metal/6*0.55 + e.rnd()*0.6
		a := 1 / (1 + 2*math.Pi*6500/e.rate)
		dv.hp1 = a * (dv.hp1 + x - dv.hp1x)
		dv.hp1x = x
		dv.hp2 = a * (dv.hp2 + dv.hp1 - dv.hp2x)
		dv.hp2x = dv.hp1
		amp = math.Exp(-ts / (0.012 + 0.3*dec*dec))
		out = dv.hp2 * amp * 1.6
	case synthLaneClap:
		n := e.rnd()
		// Band-pass around 1.1 kHz (a fixed SVF, Q about 2).
		g := math.Tan(math.Pi * 1100 * tune / e.rate)
		k := 0.5
		a1 := 1 / (1 + g*(g+k))
		a2 := g * a1
		a3 := g * a2
		v3 := n - dv.bp2
		v1 := a1*dv.bp1 + a2*v3
		v2 := dv.bp2 + a2*dv.bp1 + a3*v3
		dv.bp1 = 2*v1 - dv.bp1
		dv.bp2 = 2*v2 - dv.bp2
		env := 0.0
		for _, at := range [3]float64{0, 0.011, 0.022} {
			if ts >= at {
				if v := math.Exp(-(ts - at) / 0.006); v > env {
					env = v
				}
			}
		}
		tail := 0.0
		if ts >= 0.022 {
			tail = 0.7 * math.Exp(-(ts-0.022)/(0.06+0.3*dec))
		}
		if tail > env {
			env = tail
		}
		amp = env
		out = v1 * env * 2.2
	}
	if amp < 0.0004 && dv.t > int(e.rate*0.03) {
		dv.active = false
	}
	return out * dv.vel
}

// render fills out (interleaved stereo, len >= 2*frames) with the next frames
// of sound, running the sequencer sample-accurately.
func (e *synthEngine) render(out []float32, frames int) {
	if e.proj == nil || len(out) < 2*frames {
		return
	}
	p := e.proj
	e.updateRT()
	delaySamples := int(e.baseStepSamples() * synthDelayTimes[synthClampInt(p.DelayTime, 0, len(synthDelayTimes)-1)].steps)
	dl := len(e.delayL)
	if delaySamples >= dl {
		delaySamples = dl - 1
	}
	if delaySamples < 1 {
		delaySamples = 1
	}
	fb := math.Min(synthClamp01(p.DelayFB), 0.9)
	master := synthClamp01(p.Master)
	master = master * master * 1.4
	var peak float32
	for i := 0; i < frames; i++ {
		if e.playing {
			if e.untilStep <= 0 {
				e.fireStep()
				e.stepLen = e.stepSamples(e.step)
				e.untilStep += e.stepLen
				e.advance()
			}
			e.untilStep--
		}
		for d := range e.ratchetIn {
			if e.ratchetIn[d] > 0 {
				e.ratchetIn[d]--
				if e.ratchetIn[d] == 0 {
					e.drumHit(d, 0.7)
				}
			}
		}
		var l, r, sl, sr float64
		for inst := synthInstBass; inst < synthInsts; inst++ {
			rt := &e.rt[inst]
			pool := e.pools[inst]
			var sum float64
			for vi := range pool {
				if pool[vi].stage != synthEnvOff {
					sum += e.voiceSample(&pool[vi], rt)
				}
			}
			if sum == 0 {
				continue
			}
			sum *= rt.gain
			l += sum * rt.panL
			r += sum * rt.panR
			sl += sum * rt.panL * rt.send
			sr += sum * rt.panR * rt.send
		}
		for d := range e.drums {
			dv := &e.drums[d]
			if !dv.active {
				continue
			}
			prm := &p.Drums[d]
			s := e.drumSample(d, dv, prm)
			vol := synthClamp01(prm.Volume)
			s *= vol * vol * 0.9
			pan := synthClamp01(prm.Pan)
			pl, pr := math.Cos(pan*math.Pi/2), math.Sin(pan*math.Pi/2)
			l += s * pl
			r += s * pr
			snd := synthClamp01(prm.Delay)
			sl += s * pl * snd
			sr += s * pr * snd
		}
		// Ping-pong echo with a damped feedback path.
		ri := e.dpos - delaySamples
		if ri < 0 {
			ri += dl
		}
		dL, dR := float64(e.delayL[ri]), float64(e.delayR[ri])
		e.dlpL += (dR - e.dlpL) * 0.4
		e.dlpR += (dL - e.dlpR) * 0.4
		e.delayL[e.dpos] = float32((sl+sr)*0.5 + e.dlpL*fb)
		e.delayR[e.dpos] = float32(e.dlpR * fb)
		e.dpos++
		if e.dpos >= dl {
			e.dpos = 0
		}
		l += dL * 0.7
		r += dR * 0.7
		l = synthSoftClip(l * master)
		r = synthSoftClip(r * master)
		out[2*i] = float32(l)
		out[2*i+1] = float32(r)
		m := float32((l + r) * 0.5)
		e.scope[e.scopePos] = m
		e.scopePos = (e.scopePos + 1) % synthScopeLen
		if m < 0 {
			m = -m
		}
		if m > peak {
			peak = m
		}
	}
	e.peak = peak
	for i := range e.flash {
		e.flash[i] *= 0.9
	}
}

// synthSoftClip is a rational tanh: transparent at low level, rounding off
// toward ±1 instead of wrapping.
func synthSoftClip(x float64) float64 {
	if x > 3 {
		return 1
	}
	if x < -3 {
		return -1
	}
	x2 := x * x
	return x * (27 + x2) / (27 + 9*x2)
}
