package main

// pads_engine.go — the Pads page's sound: the project model (64 pads in four
// banks, eight patterns of 16 to 128 steps, a song chain, the stick effects)
// and the engine that turns it into stereo samples.
//
// Pads is a sample sequencer: put sounds on pads, tap out a beat, chop a recording into slices and
// chain patterns into a song, with performance effects on the analog sticks.
// Nothing here touches SDL or the screen: pads.go owns the device and the
// page, pads_samples.go loads the sounds, and the same engine renders the live
// output and a WAV export.
//
// The DSP is kept cheap enough for the A133P at 48 kHz: sixteen voices with
// linear interpolation, a TPT state-variable filter per voice (coefficients
// set when the voice starts), per-pad lo-fi, a tempo-synced ping-pong echo, a
// small Freeverb-style reverb, the stick effects on the master and a peak
// compressor with a soft clipper last.

import (
	"math"
	"sort"
	"strconv"
)

const (
	padsBanks      = 4
	padsPerBank    = 16
	padsCount      = padsBanks * padsPerBank
	padsPatterns   = 8
	padsMinSteps   = 16
	padsMaxSteps   = 128
	padsMinBPM     = 40
	padsMaxBPM     = 240
	padsVoices     = 16
	padsChokes     = 4
	padsSongMax    = 64
	padsMaxNote    = 24
	padsScopeLen   = 512
	padsMaxDelaySe = 2.0 // echo buffer seconds: a dotted 1/4 at 40 BPM is 2.25 s, clamped
	padsHistSe     = 1.0 // the stutter's memory
)

// Play modes: a one-shot plays to its end; a hold pad sounds while its button
// is down (in a pattern, until its next hit); a loop pad repeats its region
// the same way.
const (
	padsModeOneShot = iota
	padsModeHold
	padsModeLoop
	padsModeCount
)

var padsModeNames = [padsModeCount]string{"One-shot", "Hold", "Loop"}

// Stick effects. Each of the eight stick directions plays one of them; how
// far the stick leans is how much.
const (
	padsFxOff = iota
	padsFxLowPass
	padsFxHighPass
	padsFxTempoDown
	padsFxTempoUp
	padsFxFadeOut
	padsFxEcho
	padsFxReverb
	padsFxStutter
	padsFxCrush
	padsFxPan
	padsFxTapeStop
	padsFxCount
)

var padsFxNames = [padsFxCount]string{
	"Off", "Low-pass filter", "High-pass filter", "Tempo down", "Tempo up", "Fade out",
	"Echo", "Reverb", "Stutter", "Crush", "Auto-pan", "Tape stop",
}

// The eight stick directions.
const (
	padsDirLUp = iota
	padsDirLDown
	padsDirLLeft
	padsDirLRight
	padsDirRUp
	padsDirRDown
	padsDirRLeft
	padsDirRRight
	padsDirCount
)

var padsDirNames = [padsDirCount]string{
	"Left stick up", "Left stick down", "Left stick left", "Left stick right",
	"Right stick up", "Right stick down", "Right stick left", "Right stick right",
}

// ── Project model ────────────────────────────────────────────────────────

// padsPad is one pad: which sound it plays and how. Sample is a file path or
// a built-in sound ("kit:kick"); Start / End trim it as fractions of its
// length, so a chop is a set of pads sharing one file.
type padsPad struct {
	Sample  string  `json:"sample,omitempty"`
	Name    string  `json:"name,omitempty"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Volume  float64 `json:"vol"`
	Pitch   int     `json:"pitch,omitempty"` // semitones
	Fine    int     `json:"fine,omitempty"`  // cents
	Filter  float64 `json:"filter"`          // 0.5 open, lower low-pass, higher high-pass
	FadeIn  float64 `json:"fadeIn,omitempty"`
	FadeOut float64 `json:"fadeOut,omitempty"`
	Reverse bool    `json:"rev,omitempty"`
	Mode    int     `json:"mode,omitempty"`
	Choke   int     `json:"choke,omitempty"` // 0 none, 1..padsChokes
	Pan     float64 `json:"pan"`
	Delay   float64 `json:"delay,omitempty"`
	Reverb  float64 `json:"reverb,omitempty"`
	LoFi    float64 `json:"lofi,omitempty"`
	Mute    bool    `json:"mute,omitempty"`
}

// padsHit is one hit in a pattern: pad, step, a note in semitones above the
// pad's pitch and an accent.
type padsHit struct {
	Pad    int  `json:"p"`
	Step   int  `json:"s"`
	Note   int  `json:"n,omitempty"`
	Accent bool `json:"a,omitempty"`
}

// padsPattern is a pattern: its length and its hits, sorted by step then pad.
// Hits is replaced, never changed in place, so a snapshot taken for drawing
// stays valid while the page edits.
type padsPattern struct {
	Steps int       `json:"steps"`
	Hits  []padsHit `json:"hits,omitempty"`
}

// padsProject is everything Pads saves.
type padsProject struct {
	Version   int                         `json:"version"`
	BPM       int                         `json:"bpm"`
	Swing     float64                     `json:"swing"`
	Master    float64                     `json:"master"`
	Comp      float64                     `json:"compressor"`
	DelayTime int                         `json:"delayTime"`
	DelayFB   float64                     `json:"delayFeedback"`
	RevSize   float64                     `json:"reverbSize"`
	Current   int                         `json:"current"`
	SongMode  bool                        `json:"songMode"`
	Song      []int                       `json:"song,omitempty"`
	RecLive   bool                        `json:"recordLive"`
	Pads      [padsCount]padsPad          `json:"pads"`
	Patterns  [padsPatterns]padsPattern   `json:"patterns"`
	FX        [padsDirCount]int           `json:"stickFx"`
	Latch     [padsFxCount]float64        `json:"latchFx"`
	Chop      int                         `json:"chop"`
	ChopMode  int                         `json:"chopMode"`
}

const padsProjectVersion = 1

func padsClamp01(v float64) float64 {
	if v != v || v < 0 { // NaN too
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func padsClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// padsBankName is "A".."D".
func padsBankName(b int) string {
	return string(rune('A' + padsClampInt(b, 0, padsBanks-1)))
}

// padsPadLabel is a pad's short name: "A1".."D16".
func padsPadLabel(i int) string {
	i = padsClampInt(i, 0, padsCount-1)
	return padsBankName(i/padsPerBank) + strconv.Itoa(i%padsPerBank+1)
}

// padsBlankPad is an empty pad with neutral settings.
func padsBlankPad() padsPad {
	return padsPad{End: 1, Volume: 0.8, Filter: 0.5, Pan: 0.5}
}

// padsStepsValid rounds a pattern length to a whole number of bars.
func padsStepsValid(n int) int {
	n = padsClampInt(n, padsMinSteps, padsMaxSteps)
	return (n + 15) / 16 * 16
}

// sanitize clamps everything a hand-edited or older file could get wrong.
func (p *padsProject) sanitize() {
	p.Version = padsProjectVersion
	p.BPM = padsClampInt(p.BPM, padsMinBPM, padsMaxBPM)
	p.Swing = math.Min(padsClamp01(p.Swing), 0.6)
	p.Master = padsClamp01(p.Master)
	p.Comp = padsClamp01(p.Comp)
	p.DelayTime = padsClampInt(p.DelayTime, 0, len(synthDelayTimes)-1)
	p.DelayFB = math.Min(padsClamp01(p.DelayFB), 0.9)
	p.RevSize = padsClamp01(p.RevSize)
	p.Current = padsClampInt(p.Current, 0, padsPatterns-1)
	p.Chop = padsClampInt(p.Chop, 0, len(padsChopCounts)-1)
	p.ChopMode = padsClampInt(p.ChopMode, 0, 1)
	for i := range p.Pads {
		d := &p.Pads[i]
		d.Start = padsClamp01(d.Start)
		d.End = padsClamp01(d.End)
		if d.End <= d.Start+0.001 {
			d.Start, d.End = 0, 1
		}
		d.Volume = padsClamp01(d.Volume)
		d.Pitch = padsClampInt(d.Pitch, -padsMaxNote, padsMaxNote)
		d.Fine = padsClampInt(d.Fine, -50, 50)
		d.Filter = padsClamp01(d.Filter)
		d.FadeIn = padsClamp01(d.FadeIn)
		d.FadeOut = padsClamp01(d.FadeOut)
		d.Mode = padsClampInt(d.Mode, 0, padsModeCount-1)
		d.Choke = padsClampInt(d.Choke, 0, padsChokes)
		d.Pan = padsClamp01(d.Pan)
		d.Delay = padsClamp01(d.Delay)
		d.Reverb = padsClamp01(d.Reverb)
		d.LoFi = padsClamp01(d.LoFi)
		if len(d.Name) > 64 {
			d.Name = d.Name[:64]
		}
	}
	for i := range p.Patterns {
		pat := &p.Patterns[i]
		pat.Steps = padsStepsValid(pat.Steps)
		pat.Hits = padsCleanHits(pat.Hits, pat.Steps)
	}
	song := make([]int, 0, len(p.Song))
	for _, s := range p.Song {
		if len(song) >= padsSongMax {
			break
		}
		song = append(song, padsClampInt(s, 0, padsPatterns-1))
	}
	p.Song = song
	for i := range p.FX {
		p.FX[i] = padsClampInt(p.FX[i], 0, padsFxCount-1)
	}
	for i := range p.Latch {
		p.Latch[i] = padsClamp01(p.Latch[i])
	}
}

// padsCleanHits drops hits out of range and duplicates, and sorts the rest.
func padsCleanHits(hits []padsHit, steps int) []padsHit {
	out := make([]padsHit, 0, len(hits))
	seen := make(map[int]bool, len(hits))
	for _, h := range hits {
		if h.Pad < 0 || h.Pad >= padsCount || h.Step < 0 || h.Step >= steps {
			continue
		}
		key := h.Step*padsCount + h.Pad
		if seen[key] {
			continue
		}
		seen[key] = true
		h.Note = padsClampInt(h.Note, -padsMaxNote, padsMaxNote)
		out = append(out, h)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Step != out[b].Step {
			return out[a].Step < out[b].Step
		}
		return out[a].Pad < out[b].Pad
	})
	return out
}

// hitAt finds pad's hit on step, or -1.
func (pat *padsPattern) hitAt(pad, step int) int {
	for i, h := range pat.Hits {
		if h.Pad == pad && h.Step == step {
			return i
		}
	}
	return -1
}

// setHit writes (or with on=false removes) a hit, replacing the slice.
func (pat *padsPattern) setHit(h padsHit, on bool) {
	hits := make([]padsHit, 0, len(pat.Hits)+1)
	for _, x := range pat.Hits {
		if x.Pad == h.Pad && x.Step == h.Step {
			continue
		}
		hits = append(hits, x)
	}
	if on {
		hits = append(hits, h)
	}
	pat.Hits = padsCleanHits(hits, padsStepsValid(pat.Steps))
}

// empty reports whether the pattern has no hits.
func (pat *padsPattern) empty() bool { return len(pat.Hits) == 0 }

// padsChopCounts are the slice counts a chop offers.
var padsChopCounts = []int{4, 8, 16}

// ── Default project ──────────────────────────────────────────────────────

// padsKitIDs are the built-in sounds, in the order they fill bank A.
var padsKitIDs = [padsPerBank]string{
	"kit:kick", "kit:snare", "kit:hat", "kit:clap",
	"kit:kick-deep", "kit:snare-tight", "kit:hat-open", "kit:clap-long",
	"kit:bass-1", "kit:bass-4", "kit:bass-5", "kit:bass-7",
	"kit:lead-1", "kit:lead-5", "kit:chord-1", "kit:chord-6",
}

// padsKitPad is a pad playing built-in sound id.
func padsKitPad(id string) padsPad {
	d := padsBlankPad()
	d.Sample = id
	d.Name = padsKitName(id)
	switch id {
	case "kit:hat", "kit:hat-open":
		d.Choke = 1 // the open hat stops when the closed one plays
		d.Pan = 0.42
	case "kit:clap", "kit:clap-long":
		d.Reverb = 0.25
	case "kit:lead-1", "kit:lead-5":
		d.Delay = 0.3
		d.Volume = 0.7
	case "kit:chord-1", "kit:chord-6":
		d.Reverb = 0.3
		d.Volume = 0.7
	}
	return d
}

// padsKitName is a built-in sound's display name.
func padsKitName(id string) string {
	names := map[string]string{
		"kit:kick": "Kick", "kit:snare": "Snare", "kit:hat": "Hat", "kit:clap": "Clap",
		"kit:kick-deep": "Deep kick", "kit:snare-tight": "Tight snare", "kit:hat-open": "Open hat",
		"kit:clap-long": "Long clap", "kit:bass-1": "Bass I", "kit:bass-4": "Bass IV",
		"kit:bass-5": "Bass V", "kit:bass-7": "Bass VII", "kit:lead-1": "Lead I",
		"kit:lead-5": "Lead V", "kit:chord-1": "Chord i", "kit:chord-6": "Chord VI",
	}
	if n, ok := names[id]; ok {
		return n
	}
	return id
}

// padsDefaultProject is a new project: the built-in kit on bank A, a beat in
// pattern 1 and the stick effects laid out.
func padsDefaultProject() padsProject {
	p := padsProject{
		Version:   padsProjectVersion,
		BPM:       92,
		Swing:     0.08,
		Master:    0.75,
		Comp:      0.3,
		DelayTime: 2,
		DelayFB:   0.35,
		RevSize:   0.5,
		Chop:      2,
	}
	for i := range p.Pads {
		p.Pads[i] = padsBlankPad()
	}
	for i, id := range padsKitIDs {
		p.Pads[i] = padsKitPad(id)
	}
	for i := range p.Patterns {
		p.Patterns[i].Steps = 16
	}
	p.FX = [padsDirCount]int{
		padsFxHighPass, padsFxLowPass, padsFxTapeStop, padsFxStutter,
		padsFxReverb, padsFxEcho, padsFxCrush, padsFxPan,
	}
	padsDemoBeat(&p.Patterns[0])
	p.Song = []int{0}
	return p
}

// padsDemoBeat writes a laid-back beat on the kit into pat.
func padsDemoBeat(pat *padsPattern) {
	var hits []padsHit
	add := func(pad, step int, acc bool) { hits = append(hits, padsHit{Pad: pad, Step: step, Accent: acc}) }
	for _, s := range []int{0, 7, 10} {
		add(0, s, s == 0)
	}
	add(1, 4, false)
	add(1, 12, true)
	for s := 0; s < 16; s += 2 {
		add(2, s, s%4 == 2)
	}
	add(6, 14, false)
	add(8, 0, false)
	add(10, 10, false)
	add(14, 0, false)
	pat.Steps = 16
	pat.Hits = padsCleanHits(hits, 16)
}

// ── Engine ───────────────────────────────────────────────────────────────

// padsSample is a decoded sound: mono 16-bit at its own rate. It is never
// changed once stored, so the engine and an export may share it.
type padsSample struct {
	data []int16
	rate int
}

type padsVoice struct {
	on        bool
	pad       int
	smp       *padsSample
	pos, inc  float64
	lo, hi    float64
	loop      bool
	wrapped   bool // a loop went round: its fade-in is done
	gate      int  // output frames left before a hold / loop voice lets go; -1 held live
	live      int  // the live press holding it (0 none)
	releasing bool
	rel       float64
	relStep   float64
	gain      float64
	panL      float64
	panR      float64
	sendD     float64
	sendR     float64
	fadeIn    float64 // source frames
	fadeOut   float64
	choke     int
	age       uint64
	// filter (TPT state-variable)
	fMode      int
	a1, a2, a3 float64
	k          float64
	ic1, ic2   float64
	// lo-fi
	crush   float64
	crushPh float64
	crushV  float64
	bits    float64
}

// padsComb and padsAllpass are the reverb's parts.
type padsComb struct {
	buf   []float32
	pos   int
	store float64
}

type padsAllpass struct {
	buf []float32
	pos int
}

type padsEngine struct {
	rate    float64
	proj    *padsProject
	samples map[string]*padsSample

	voices [padsVoices]padsVoice
	ageCtr uint64

	playing   bool
	pat       int // pattern to play at the next step
	step      int // next step to fire
	cur       int // step that fired last
	firedPat  int
	songPos   int
	untilStep float64
	stepLen   float64
	sinceFire float64

	delayL, delayR []float32
	dpos           int
	dlpL, dlpR     float64

	combL, combR [4]padsComb
	apL, apR     [2]padsAllpass

	histL, histR []float32
	hpos         int
	stutOn       bool
	stutStart    int
	stutLen      int
	stutPh       int

	// stick effects: target and smoothed amounts
	fxWant  [padsFxCount]float64
	fx      [padsFxCount]float64
	tape    float64
	panPh   float64
	mFiltL  [2]float64
	mFiltR  [2]float64
	hFiltL  [2]float64
	hFiltR  [2]float64
	crushPh float64
	crushL  float64
	crushR  float64
	compEnv float64

	flash    [padsCount]float32
	scope    [padsScopeLen]float32
	scopePos int
	peak     float32
}

func newPadsEngine(rate int, samples map[string]*padsSample) *padsEngine {
	if rate < 8000 {
		rate = 48000
	}
	e := &padsEngine{rate: float64(rate), samples: samples, tape: 1}
	n := int(e.rate * padsMaxDelaySe)
	e.delayL = make([]float32, n)
	e.delayR = make([]float32, n)
	h := int(e.rate * padsHistSe)
	e.histL = make([]float32, h)
	e.histR = make([]float32, h)
	scale := e.rate / 44100
	for i, l := range [4]int{1116, 1188, 1277, 1356} {
		e.combL[i].buf = make([]float32, max(1, int(float64(l)*scale)))
		e.combR[i].buf = make([]float32, max(1, int(float64(l+23)*scale)))
	}
	for i, l := range [2]int{556, 441} {
		e.apL[i].buf = make([]float32, max(1, int(float64(l)*scale)))
		e.apR[i].buf = make([]float32, max(1, int(float64(l+23)*scale)))
	}
	return e
}

// baseStepSamples is a sixteenth at the project's tempo.
func (e *padsEngine) baseStepSamples() float64 {
	bpm := 120
	if e.proj != nil {
		bpm = padsClampInt(e.proj.BPM, padsMinBPM, padsMaxBPM)
	}
	return e.rate * 60 / float64(bpm) / 4
}

// stepSamples is step i's length with swing: even steps long, odd short.
func (e *padsEngine) stepSamples(i int) float64 {
	base := e.baseStepSamples()
	sw := 0.0
	if e.proj != nil {
		sw = math.Min(padsClamp01(e.proj.Swing), 0.6) * 0.5
	}
	if i%2 == 0 {
		return base * (1 + sw)
	}
	return base * (1 - sw)
}

// patternSteps is pattern i's length.
func (e *padsEngine) patternSteps(i int) int {
	if e.proj == nil {
		return 16
	}
	return padsStepsValid(e.proj.Patterns[padsClampInt(i, 0, padsPatterns-1)].Steps)
}

// start plays from the top: the song's first slot in song mode, else the
// pattern being edited.
func (e *padsEngine) start() {
	if e.proj == nil {
		return
	}
	e.playing = true
	e.step, e.cur, e.songPos = 0, -1, 0
	e.untilStep = 0
	e.pat = e.proj.Current
	if e.proj.SongMode && len(e.proj.Song) > 0 {
		e.pat = padsClampInt(e.proj.Song[0], 0, padsPatterns-1)
	}
	e.firedPat = e.pat
}

// stop stops the sequencer and lets sounding hold / loop voices go.
func (e *padsEngine) stop() {
	e.playing = false
	e.cur = -1
	for i := range e.voices {
		v := &e.voices[i]
		if v.on && (v.loop || v.gate != 0) {
			e.release(v, 0.01)
		}
	}
}

// silence cuts everything at once (the device closing, a new project).
func (e *padsEngine) silence() {
	e.playing = false
	e.cur = -1
	for i := range e.voices {
		e.voices[i].on = false
	}
	clear(e.delayL)
	clear(e.delayR)
	clear(e.histL)
	clear(e.histR)
	for i := range e.combL {
		clear(e.combL[i].buf)
		clear(e.combR[i].buf)
		e.combL[i].store, e.combR[i].store = 0, 0
	}
	for i := range e.apL {
		clear(e.apL[i].buf)
		clear(e.apR[i].buf)
	}
	e.stutOn = false
}

func (e *padsEngine) release(v *padsVoice, secs float64) {
	if v.releasing {
		return
	}
	v.releasing = true
	v.rel = 1
	v.relStep = 1 / math.Max(1, secs*e.rate)
}

// releaseLive lets go of the voices a live press holds.
func (e *padsEngine) releaseLive(id int) {
	for i := range e.voices {
		v := &e.voices[i]
		if v.on && v.live == id {
			v.live = 0
			e.release(v, 0.012)
		}
	}
}

// trigger starts pad (its settings in d) with note semitones and velocity
// vel. gate is how long a hold / loop pad sounds in output frames, -1 while
// a live press (live) holds it. pad -1 is a preview with no flash or choke.
func (e *padsEngine) trigger(pad int, d *padsPad, smp *padsSample, note int, vel float64, gate, live int) {
	if d == nil || smp == nil || len(smp.data) < 32 || smp.rate <= 0 {
		return
	}
	n := float64(len(smp.data))
	lo := math.Floor(padsClamp01(d.Start) * n)
	hi := math.Floor(padsClamp01(d.End) * n)
	if hi > n-1 {
		hi = n - 1
	}
	if hi-lo < 16 {
		return
	}
	if pad >= 0 {
		// A choke group stops its other pads; a hold or loop pad stops its own
		// voice before it starts again.
		for i := range e.voices {
			v := &e.voices[i]
			if !v.on {
				continue
			}
			if (d.Choke > 0 && v.choke == d.Choke) || (v.pad == pad && d.Mode != padsModeOneShot) {
				e.release(v, 0.004)
			}
		}
		e.flash[pad] = 1
	}
	// A free voice, else one already letting go, else the oldest.
	pick := -1
	for i := range e.voices {
		if !e.voices[i].on {
			pick = i
			break
		}
	}
	if pick < 0 {
		for i := range e.voices {
			if e.voices[i].releasing && (pick < 0 || e.voices[i].age < e.voices[pick].age) {
				pick = i
			}
		}
	}
	if pick < 0 {
		pick = 0
		for i := range e.voices {
			if e.voices[i].age < e.voices[pick].age {
				pick = i
			}
		}
	}
	semis := float64(d.Pitch+note) + float64(d.Fine)/100
	inc := float64(smp.rate) / e.rate * math.Pow(2, semis/12)
	e.ageCtr++
	vol := padsClamp01(d.Volume)
	pan := padsClamp01(d.Pan)
	v := padsVoice{
		on: true, pad: pad, smp: smp, lo: lo, hi: hi, inc: inc, pos: lo,
		gain: vol * vol * vel, panL: math.Cos(pan * math.Pi / 2), panR: math.Sin(pan * math.Pi / 2),
		sendD: padsClamp01(d.Delay), sendR: padsClamp01(d.Reverb), choke: d.Choke, age: e.ageCtr,
		live: live, bits: 16, crush: 1,
	}
	if pad < 0 {
		v.choke = 0
	}
	if d.Reverse {
		v.pos, v.inc = hi-1, -inc
	}
	switch d.Mode {
	case padsModeLoop:
		v.loop = true
		v.gate = gate
	case padsModeHold:
		v.gate = gate
	}
	if v.gate == 0 && d.Mode != padsModeOneShot {
		v.gate = int(e.baseStepSamples())
	}
	region := hi - lo
	// Fades are up to two seconds of the sound; a short ramp at each end
	// always keeps a trimmed start or end from clicking.
	v.fadeIn = math.Min(region/2, math.Max(d.FadeIn*d.FadeIn*2*float64(smp.rate), 0.0005*float64(smp.rate)))
	v.fadeOut = math.Min(region/2, math.Max(d.FadeOut*d.FadeOut*2*float64(smp.rate), 0.002*float64(smp.rate)))
	if lf := padsClamp01(d.LoFi); lf > 0.01 {
		v.crush = 1 + lf*lf*15
		v.bits = 16 - lf*11
	}
	// The one-knob filter: below the middle a low-pass closing toward 80 Hz,
	// above it a high-pass opening toward 4 kHz.
	f := padsClamp01(d.Filter)
	var fc float64
	switch {
	case f < 0.48:
		v.fMode = 1
		fc = 80 * math.Pow(250, f/0.48)
	case f > 0.52:
		v.fMode = 2
		fc = 20 * math.Pow(200, (f-0.52)/0.48)
	}
	if v.fMode != 0 {
		fc = math.Min(fc, e.rate*0.45)
		g := math.Tan(math.Pi * fc / e.rate)
		v.k = 1.2
		v.a1 = 1 / (1 + g*(g+v.k))
		v.a2 = g * v.a1
		v.a3 = g * v.a2
	}
	e.voices[pick] = v
}

// voiceSample is one output frame of voice v; speed is the tape speed.
func (e *padsEngine) voiceSample(v *padsVoice, speed float64) float64 {
	d := v.smp.data
	p := v.pos
	if !v.loop && ((v.inc > 0 && p >= v.hi-1) || (v.inc < 0 && p <= v.lo)) {
		v.on = false
		return 0
	}
	i := int(p)
	if i < 0 {
		i = 0
	}
	if i >= len(d)-1 {
		i = len(d) - 2
	}
	fr := p - float64(i)
	a, b := float64(d[i]), float64(d[i+1])
	s := (a + (b-a)*fr) / 32768
	var done, left float64
	if v.inc > 0 {
		done, left = p-v.lo, v.hi-p
	} else {
		done, left = v.hi-p, p-v.lo
	}
	env := 1.0
	if v.fadeIn > 0 && done < v.fadeIn && !v.wrapped {
		env = done / v.fadeIn
	}
	if !v.loop && v.fadeOut > 0 && left < v.fadeOut {
		env *= left / v.fadeOut
	}
	if v.releasing {
		v.rel -= v.relStep
		if v.rel <= 0 {
			v.on = false
			return 0
		}
		env *= v.rel
	}
	if v.gate > 0 {
		v.gate--
		if v.gate == 0 {
			e.release(v, 0.01)
		}
	}
	if v.crush > 1 {
		v.crushPh++
		if v.crushPh >= v.crush {
			v.crushPh -= v.crush
			v.crushV = s
		}
		s = v.crushV
	}
	if v.bits < 15.5 {
		q := math.Exp2(v.bits - 1)
		s = math.Round(s*q) / q
	}
	if v.fMode != 0 {
		v3 := s - v.ic2
		v1 := v.a1*v.ic1 + v.a2*v3
		v2 := v.ic2 + v.a2*v.ic1 + v.a3*v3
		v.ic1 = 2*v1 - v.ic1
		v.ic2 = 2*v2 - v.ic2
		if v.fMode == 1 {
			s = v2
		} else {
			s = s - v.k*v1 - v2
		}
	}
	v.pos += v.inc * speed
	if v.loop {
		span := v.hi - v.lo
		if span >= 2 {
			for v.pos >= v.hi {
				v.pos -= span
				v.wrapped = true
			}
			for v.pos < v.lo {
				v.pos += span
				v.wrapped = true
			}
		}
	}
	return s * env * v.gain
}

// hitGate is how long a hold / loop hit sounds: until the same pad's next hit
// in the pattern, or the pattern's end.
func (e *padsEngine) hitGate(pat *padsPattern, h padsHit, steps int) int {
	next := steps
	for _, x := range pat.Hits {
		if x.Pad == h.Pad && x.Step > h.Step {
			next = x.Step
			break
		}
	}
	var frames float64
	for s := h.Step; s < next; s++ {
		frames += e.stepSamples(s)
	}
	return max(1, int(frames))
}

// fireStep plays the hits on the current step.
func (e *padsEngine) fireStep() {
	p := e.proj
	pat := &p.Patterns[padsClampInt(e.pat, 0, padsPatterns-1)]
	steps := padsStepsValid(pat.Steps)
	e.cur, e.firedPat = e.step, e.pat
	e.sinceFire = 0
	for _, h := range pat.Hits {
		if h.Step > e.step {
			break
		}
		if h.Step != e.step {
			continue
		}
		d := &p.Pads[padsClampInt(h.Pad, 0, padsCount-1)]
		if d.Mute || d.Sample == "" {
			continue
		}
		vel := 0.8
		if h.Accent {
			vel = 1
		}
		gate := 0
		if d.Mode != padsModeOneShot {
			gate = e.hitGate(pat, h, steps)
		}
		e.trigger(h.Pad, d, e.samples[d.Sample], h.Note, vel, gate, 0)
	}
}

// advance moves to the next step; at a pattern's end the song moves on, or
// the loop follows the pattern being edited.
func (e *padsEngine) advance() {
	e.step++
	if e.step < e.patternSteps(e.pat) {
		return
	}
	e.step = 0
	p := e.proj
	if p.SongMode && len(p.Song) > 0 {
		e.songPos = (e.songPos + 1) % len(p.Song)
		e.pat = padsClampInt(p.Song[e.songPos], 0, padsPatterns-1)
		return
	}
	e.pat = p.Current
}

// recordStep is where a live hit lands: the step playing, or the next one
// when the press came in its second half.
func (e *padsEngine) recordStep() (pat, step int) {
	if e.cur < 0 {
		return e.pat, 0
	}
	if e.sinceFire < e.stepLen/2 {
		return e.firedPat, e.cur
	}
	steps := e.patternSteps(e.firedPat)
	return e.firedPat, (e.cur + 1) % steps
}

// playingStep is the step the playhead is on, or -1.
func (e *padsEngine) playingStep() int {
	if !e.playing {
		return -1
	}
	return e.cur
}

// padsSVF is one step of the master's state-variable filter (state st,
// coefficients from padsSVFCoef, k fixed at 1.4); hp picks the high-pass.
func padsSVF(st *[2]float64, x, a1, a2, a3 float64, hp bool) float64 {
	v3 := x - st[1]
	v1 := a1*st[0] + a2*v3
	v2 := st[1] + a2*st[0] + a3*v3
	st[0] = 2*v1 - st[0]
	st[1] = 2*v2 - st[1]
	if hp {
		return x - 1.4*v1 - v2
	}
	return v2
}

func padsSVFCoef(fc, rate float64) (float64, float64, float64) {
	fc = math.Min(math.Max(fc, 10), rate*0.45)
	g := math.Tan(math.Pi * fc / rate)
	a1 := 1 / (1 + g*(g+1.4))
	return a1, g * a1, g * g * a1
}

// reverb runs the Freeverb-style tank for one frame.
func (e *padsEngine) reverb(in, room, damp float64) (float64, float64) {
	var l, r float64
	for i := range e.combL {
		l += padsCombStep(&e.combL[i], in, room, damp)
		r += padsCombStep(&e.combR[i], in, room, damp)
	}
	for i := range e.apL {
		l = padsAllpassStep(&e.apL[i], l)
		r = padsAllpassStep(&e.apR[i], r)
	}
	return l, r
}

func padsCombStep(c *padsComb, in, room, damp float64) float64 {
	out := float64(c.buf[c.pos])
	c.store = out*(1-damp) + c.store*damp
	c.buf[c.pos] = float32(in + c.store*room)
	c.pos++
	if c.pos >= len(c.buf) {
		c.pos = 0
	}
	return out
}

func padsAllpassStep(a *padsAllpass, in float64) float64 {
	b := float64(a.buf[a.pos])
	a.buf[a.pos] = float32(in + b*0.5)
	a.pos++
	if a.pos >= len(a.buf) {
		a.pos = 0
	}
	return b - in
}

// render fills out with frames of interleaved stereo.
func (e *padsEngine) render(out []float32, frames int) {
	if e.proj == nil || len(out) < 2*frames {
		return
	}
	p := e.proj
	// Smooth the stick effects once per block so a stick moves them without
	// zipper noise.
	for i := range e.fx {
		e.fx[i] += (padsClamp01(e.fxWant[i]) - e.fx[i]) * 0.35
		if e.fx[i] < 0.002 {
			e.fx[i] = 0
		}
	}
	fx := &e.fx
	base := e.baseStepSamples()
	delaySamples := int(base * synthDelayTimes[padsClampInt(p.DelayTime, 0, len(synthDelayTimes)-1)].steps)
	dl := len(e.delayL)
	delaySamples = padsClampInt(delaySamples, 1, dl-1)
	fb := math.Min(padsClamp01(p.DelayFB), 0.9)
	if a := fx[padsFxEcho]; a > 0 {
		fb = math.Max(fb, 0.45+0.45*a)
	}
	room := 0.7 + 0.27*padsClamp01(p.RevSize)
	if a := fx[padsFxReverb]; a > 0 {
		room = math.Max(room, 0.8+0.17*a)
	}
	master := padsClamp01(p.Master)
	master = master * master * 1.4
	comp := padsClamp01(p.Comp)
	thr := 1 - 0.75*comp
	ratio := 1 + 7*comp
	makeup := 1 + comp*0.8
	att := math.Exp(-1 / (0.004 * e.rate))
	relc := math.Exp(-1 / (0.12 * e.rate))
	var lpA, lpB, lpC, hpA, hpB, hpC float64
	if a := fx[padsFxLowPass]; a > 0 {
		lpA, lpB, lpC = padsSVFCoef(18000*math.Pow(150.0/18000, a), e.rate)
	}
	if a := fx[padsFxHighPass]; a > 0 {
		hpA, hpB, hpC = padsSVFCoef(20*math.Pow(200, a), e.rate)
	}
	tempoMul := (1 - 0.5*fx[padsFxTempoDown]) * (1 + 0.6*fx[padsFxTempoUp])
	tapeWant := 1 - 0.97*fx[padsFxTapeStop]
	crushA := fx[padsFxCrush]
	crushStep := 1 + crushA*crushA*24
	crushQ := math.Exp2(15 - 11*crushA)
	panA := fx[padsFxPan]
	fade := 1 - fx[padsFxFadeOut]
	stutA := fx[padsFxStutter]
	hl := len(e.histL)
	var peak float32
	for i := 0; i < frames; i++ {
		e.tape += (tapeWant - e.tape) * 0.0004
		speed := e.tape
		if e.playing {
			if e.untilStep <= 0 {
				e.fireStep()
				e.stepLen = e.stepSamples(e.step)
				e.untilStep += e.stepLen
				e.advance()
			}
			e.untilStep -= tempoMul * speed
			e.sinceFire += tempoMul * speed
		}
		var l, r, sd, sr float64
		for vi := range e.voices {
			v := &e.voices[vi]
			if !v.on {
				continue
			}
			s := e.voiceSample(v, speed)
			if s == 0 {
				continue
			}
			l += s * v.panL
			r += s * v.panR
			sd += s * v.sendD
			sr += s * v.sendR
		}
		if a := fx[padsFxEcho]; a > 0 {
			sd += (l + r) * 0.5 * a
		}
		if a := fx[padsFxReverb]; a > 0 {
			sr += (l + r) * 0.5 * a
		}
		// Ping-pong echo with a damped feedback path.
		ri := e.dpos - delaySamples
		if ri < 0 {
			ri += dl
		}
		dL, dR := float64(e.delayL[ri]), float64(e.delayR[ri])
		e.dlpL += (dR - e.dlpL) * 0.4
		e.dlpR += (dL - e.dlpR) * 0.4
		e.delayL[e.dpos] = float32(sd + e.dlpL*fb)
		e.delayR[e.dpos] = float32(e.dlpR * fb)
		e.dpos++
		if e.dpos >= dl {
			e.dpos = 0
		}
		l += dL * 0.6
		r += dR * 0.6
		if sr != 0 || e.peak > 0.0001 {
			rl, rr := e.reverb(sr*0.03, room, 0.3)
			l += rl
			r += rr
		}
		// The stutter repeats the last slice it heard: an eighth, a sixteenth
		// or a thirty-second as the stick leans further.
		e.histL[e.hpos] = float32(l)
		e.histR[e.hpos] = float32(r)
		if stutA > 0.1 {
			if !e.stutOn {
				div := 1.0
				switch {
				case stutA > 0.8:
					div = 4
				case stutA > 0.45:
					div = 2
				}
				e.stutLen = padsClampInt(int(base*2/div), 64, hl-1)
				e.stutStart = (e.hpos - e.stutLen + hl) % hl
				e.stutPh = 0
				e.stutOn = true
			}
			j := (e.stutStart + e.stutPh) % hl
			l, r = float64(e.histL[j]), float64(e.histR[j])
			e.stutPh = (e.stutPh + 1) % e.stutLen
		} else {
			e.stutOn = false
		}
		e.hpos++
		if e.hpos >= hl {
			e.hpos = 0
		}
		if lpA != 0 {
			l = padsSVF(&e.mFiltL, l, lpA, lpB, lpC, false)
			r = padsSVF(&e.mFiltR, r, lpA, lpB, lpC, false)
		}
		if hpA != 0 {
			l = padsSVF(&e.hFiltL, l, hpA, hpB, hpC, true)
			r = padsSVF(&e.hFiltR, r, hpA, hpB, hpC, true)
		}
		if crushA > 0 {
			e.crushPh++
			if e.crushPh >= crushStep {
				e.crushPh -= crushStep
				e.crushL = math.Round(l*crushQ) / crushQ
				e.crushR = math.Round(r*crushQ) / crushQ
			}
			l, r = e.crushL, e.crushR
		}
		if panA > 0 {
			e.panPh += 1 / (4 * base)
			if e.panPh >= 1 {
				e.panPh--
			}
			sn := math.Sin(2 * math.Pi * e.panPh)
			l *= 1 - panA*(0.5+0.5*sn)
			r *= 1 - panA*(0.5-0.5*sn)
		}
		l *= fade * master
		r *= fade * master
		if comp > 0.01 {
			lv := math.Max(math.Abs(l), math.Abs(r))
			if lv > e.compEnv {
				e.compEnv = lv + (e.compEnv-lv)*att
			} else {
				e.compEnv = lv + (e.compEnv-lv)*relc
			}
			g := makeup
			if e.compEnv > thr {
				g *= (thr + (e.compEnv-thr)/ratio) / e.compEnv
			}
			l *= g
			r *= g
		}
		l = synthSoftClip(l)
		r = synthSoftClip(r)
		out[2*i] = float32(l)
		out[2*i+1] = float32(r)
		m := float32((l + r) * 0.5)
		e.scope[e.scopePos] = m
		e.scopePos = (e.scopePos + 1) % padsScopeLen
		if m < 0 {
			m = -m
		}
		if m > peak {
			peak = m
		}
	}
	e.peak = peak
	for i := range e.flash {
		e.flash[i] *= 0.88
	}
}
