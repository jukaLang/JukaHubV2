package main

// synth.go — the Synth page: a pocket groovebox for the handheld.
//
// Four pages, switched with L1 / R1 (Tab on a keyboard):
//
//   - Pattern: a 16-step grid of seven lanes (four drums, bass, lead and
//     chords). A sets a step, Y / X move a melodic step's note through the
//     scale (accent and ratchet on a drum lane), A on a lane's name mutes it,
//     L2 / R2 pick one of eight patterns.
//   - Play: the face buttons and the D-pad are seven pads in the song's
//     scale, so nothing played is ever out of key. L2 / R2 change the
//     instrument; with "Record from Play" on, notes land in the pattern while
//     it plays.
//   - Sound: the instrument's preset and every parameter (wave, filter,
//     envelope, glide, detune, echo...), or the drum kit's tune / decay /
//     level per drum.
//   - Mix: tempo (A taps it), swing, key, scale, pattern length, chaining,
//     echo, volume, and the actions — copy, clear, a random beat, the demo
//     and Export to WAV (Downloads/music, where the Music player finds it).
//
// Start (Space) plays and stops wherever you are. The page opens its own
// stereo device while it is on screen (the boot chime's pattern) and closes
// it about a second after the page stops being drawn, so the Synth never
// plays behind another screen or a game. The project saves itself to
// synth.json a couple of seconds after each change and when the page closes.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	synthSceneName = "Synth"
	synthStoreFile = "synth.json"
	// synthIdleStop is how long the page may go undrawn before its device
	// closes: long enough for a slow idle frame, short enough that leaving
	// the page stops the music.
	synthIdleStop  = 1200 * time.Millisecond
	synthBlock     = 256
	synthSaveDelay = 2 * time.Second
	synthArmWindow = 3 * time.Second
)

const (
	synthTabPattern = iota
	synthTabPlay
	synthTabSound
	synthTabMix
	synthTabCount
)

var synthTabNames = [synthTabCount]string{"Pattern", "Play", "Sound", "Mix"}

// synthLaneColors tint each lane's steps, pads and sliders.
var synthLaneColors = [synthLanes]sdl.Color{
	{R: 255, G: 112, B: 92, A: 255},
	{R: 255, G: 192, B: 84, A: 255},
	{R: 112, G: 222, B: 172, A: 255},
	{R: 232, G: 124, B: 222, A: 255},
	{R: 92, G: 172, B: 255, A: 255},
	{R: 172, G: 142, B: 255, A: 255},
	{R: 255, G: 150, B: 186, A: 255},
}

// synthInstColor is the colour of an instrument (the drums take the kick's).
func synthInstColor(inst int) sdl.Color {
	switch inst {
	case synthInstBass:
		return synthLaneColors[synthLaneBass]
	case synthInstLead:
		return synthLaneColors[synthLaneLead]
	case synthInstChords:
		return synthLaneColors[synthLaneChord]
	}
	return synthLaneColors[synthLaneKick]
}

// synth is the shared state: the project, which the audio goroutine reads
// while it renders, and the engine of the open device. mu guards both.
var synth struct {
	mu      sync.Mutex
	proj    synthProject
	loaded  bool
	eng     *synthEngine
	running bool
	failAt  time.Time
	dirty   bool
	editAt  time.Time
}

// synthUI is the page's own state. Main thread only.
var synthUI struct {
	tab      int
	lane     int
	step     int // -1 is the lane's name cell
	inst     int
	soundRow int
	mixRow   int
	lastNote [synthLanes]int
	armed    string
	armedAt  time.Time
	padFlash [7]time.Time
	taps     []time.Time
	failShow bool
}

var (
	synthLastSeen  atomic.Int64
	synthExporting atomic.Bool
	synthSaving    atomic.Bool
)

func init() {
	synthUI.inst = synthInstBass
	registerScene(synthScene{sceneBase{name: synthSceneName}})
}

// ── Project access ───────────────────────────────────────────────────────

// synthEnsureLoaded reads synth.json the first time the page opens. A
// missing or unreadable file opens the demo project instead.
func synthEnsureLoaded() {
	synth.mu.Lock()
	defer synth.mu.Unlock()
	if synth.loaded {
		return
	}
	synth.loaded = true
	p := synthDefaultProject()
	if data, err := os.ReadFile(synthStoreFile); err == nil {
		// The defaults fill fields an older file lacks, but not the patterns:
		// a cleared step is saved as {} and must not get the demo's back.
		loaded := synthDefaultProject()
		loaded.Patterns = [synthPatterns]synthPattern{}
		if err := json.Unmarshal(data, &loaded); err != nil {
			logWarn("synth", "synth.json is unreadable, opening the demo: %v", err)
		} else {
			p = loaded
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		logWarn("synth", "synth.json: %v", err)
	}
	p.sanitize()
	synth.proj = p
}

// synthSnapshot is a copy of the project for one frame's drawing.
func synthSnapshot() synthProject {
	synth.mu.Lock()
	defer synth.mu.Unlock()
	return synth.proj
}

// synthEdit changes the project under the lock and schedules a save.
func synthEdit(fn func(p *synthProject, e *synthEngine)) {
	synth.mu.Lock()
	fn(&synth.proj, synth.eng)
	synth.dirty = true
	synth.editAt = time.Now()
	synth.mu.Unlock()
}

// synthWithEngine runs fn on the live engine, if the device is open.
func synthWithEngine(fn func(p *synthProject, e *synthEngine)) {
	synth.mu.Lock()
	if synth.eng != nil {
		fn(&synth.proj, synth.eng)
	}
	synth.mu.Unlock()
}

// synthMaybeSave writes the project once edits have settled (or at once when
// forced), off the calling thread.
func synthMaybeSave(force bool) {
	synth.mu.Lock()
	if !synth.dirty || (!force && time.Since(synth.editAt) < synthSaveDelay) {
		synth.mu.Unlock()
		return
	}
	if !synthSaving.CompareAndSwap(false, true) {
		synth.mu.Unlock()
		if force {
			// A save is writing: this one runs when it is done, so leaving the
			// page never drops the last edits.
			go func() {
				for i := 0; i < 100 && synthSaving.Load(); i++ {
					time.Sleep(20 * time.Millisecond)
				}
				synthMaybeSave(true)
			}()
		}
		return
	}
	p := synth.proj
	synth.dirty = false
	synth.mu.Unlock()
	go func() {
		defer synthSaving.Store(false)
		data, err := json.Marshal(&p)
		if err == nil {
			err = AtomicWrite(synthStoreFile, data, 0o644)
		}
		if err != nil {
			logWarn("synth", "saving synth.json: %v", err)
			synth.mu.Lock()
			synth.dirty = true
			synth.mu.Unlock()
		}
	}()
}

// ── Live view ────────────────────────────────────────────────────────────

// synthLiveView is what the page draws from the engine.
type synthLiveView struct {
	open     bool
	playing  bool
	step     int
	firedPat int
	nextPat  int
	flash    [synthLanes]float32
	scope    [synthScopeLen]float32
	peak     float32
}

func synthLive() synthLiveView {
	var v synthLiveView
	synth.mu.Lock()
	defer synth.mu.Unlock()
	e := synth.eng
	if e == nil {
		return v
	}
	v.open = true
	v.playing = e.playing
	v.step = e.playingStep()
	v.firedPat = e.firedPat
	v.nextPat = e.pat
	v.flash = e.flash
	v.peak = e.peak
	for i := 0; i < synthScopeLen; i++ {
		v.scope[i] = e.scope[(e.scopePos+i)%synthScopeLen]
	}
	return v
}

// synthHoldsScreenAwake keeps the idle screen away while a pattern plays:
// someone listening to a loop is not idle (screensaver.go).
func synthHoldsScreenAwake() bool {
	if time.Since(time.Unix(0, synthLastSeen.Load())) > synthIdleStop {
		return false
	}
	synth.mu.Lock()
	defer synth.mu.Unlock()
	return synth.eng != nil && synth.eng.playing
}

// synthAnimating keeps the frame rate up while the playhead moves (anim.go).
func synthAnimating() bool { return synthHoldsScreenAwake() }

// ── Audio device ─────────────────────────────────────────────────────────

// synthTouch marks the page as drawn and opens the device when it is not.
func synthTouch() {
	synthLastSeen.Store(time.Now().UnixNano())
	synth.mu.Lock()
	start := !synth.running && (synth.failAt.IsZero() || time.Since(synth.failAt) > 5*time.Second)
	if start {
		synth.running = true
	}
	synth.mu.Unlock()
	if start {
		go synthAudioLoop()
	}
}

// synthLocked runs fn under synth.mu, releasing it even if fn panics: the
// audio loop's recover must not find the lock still held.
func synthLocked(fn func()) {
	synth.mu.Lock()
	defer synth.mu.Unlock()
	fn()
}

// synthAudioLoop owns the Synth's device: it keeps a few blocks queued
// ahead of the speaker until the page has not been drawn for synthIdleStop.
func synthAudioLoop() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("synth", "audio loop stopped: %v", r)
		}
		synth.mu.Lock()
		synth.eng = nil
		synth.running = false
		synth.mu.Unlock()
		synthMaybeSave(true)
	}()
	desired := sdl.AudioSpec{Freq: 48000, Format: sdl.AUDIO_S16LSB, Channels: 2, Samples: 512}
	var obtained sdl.AudioSpec
	dev, err := outOpen(&desired, &obtained, sdl.AUDIO_ALLOW_FREQUENCY_CHANGE)
	if err != nil || dev == 0 {
		logWarn("synth", "no audio device: %v", err)
		synth.mu.Lock()
		synth.failAt = time.Now()
		synth.mu.Unlock()
		return
	}
	defer outClose(dev)
	rate := int(obtained.Freq)
	if rate <= 0 {
		rate = int(desired.Freq)
	}
	eng := newSynthEngine(rate)
	synth.mu.Lock()
	eng.proj = &synth.proj
	synth.eng = eng
	synth.failAt = time.Time{}
	synth.mu.Unlock()

	fbuf := make([]float32, synthBlock*2)
	bbuf := make([]byte, synthBlock*4)
	// Two device buffers ahead (at least six blocks): a GC pause or a slow
	// frame must not drain it; ~40 ms of latency on a pad press.
	ahead := uint32(max(6*synthBlock, 2*int(obtained.Samples)) * 4)
	started := false
	for time.Since(time.Unix(0, synthLastSeen.Load())) < synthIdleStop {
		for outQueued(dev) < ahead {
			synthLocked(func() { eng.render(fbuf, synthBlock) })
			for i, v := range fbuf {
				s := int16(math.Max(-1, math.Min(1, float64(v))) * 32767)
				binary.LittleEndian.PutUint16(bbuf[2*i:], uint16(s))
			}
			if err := outQueue(dev, bbuf); err != nil {
				logWarn("synth", "queue: %v", err)
				return
			}
			publishAudioLevel(bbuf, rate, 2)
		}
		if !started {
			outPause(dev, false)
			started = true
		}
		time.Sleep(2 * time.Millisecond)
	}
	synthLocked(eng.silence)
	outClear(dev)
}

// ── Actions ──────────────────────────────────────────────────────────────

// synthTogglePlay is Start: play from the top, or stop.
func synthTogglePlay() {
	played := false
	synthWithEngine(func(p *synthProject, e *synthEngine) {
		if e.playing {
			e.stop()
			return
		}
		e.start()
		played = true
	})
	if played {
		trophyEvent(tkSynthPlay)
	}
}

// synthAudition sounds a lane's step on its own (setting a step, a pad, a
// sound being shaped).
func synthAudition(lane int, st synthStep) {
	synthWithEngine(func(p *synthProject, e *synthEngine) {
		e.updateRT()
		gate := 0
		if inst := synthLaneInst(lane); inst != synthInstDrums {
			gate = int(e.baseStepSamples() * 2 * e.rt[inst].length)
		}
		e.playLane(lane, st, gate)
	})
}

// synthEditStep changes the focused step and plays it.
func synthEditStep(fn func(st *synthStep, lane int)) {
	lane, step := synthUI.lane, synthUI.step
	var st synthStep
	synthEdit(func(p *synthProject, e *synthEngine) {
		cell := &p.Patterns[p.Current].Lanes[lane][step]
		fn(cell, lane)
		st = *cell
	})
	if st.On {
		synthAudition(lane, st)
	}
}

// synthSetPattern moves the edited pattern; while one loop plays the engine
// follows at the end of the bar.
func synthSetPattern(dir int) {
	synthEdit(func(p *synthProject, e *synthEngine) {
		p.Current = (p.Current + dir + synthPatterns) % synthPatterns
	})
}

// synthPadPlay plays pad i (0..6) on the Play page and records it when asked.
func synthPadPlay(i int) {
	if i < 0 || i >= len(synthUI.padFlash) {
		return
	}
	synthUI.padFlash[i] = time.Now()
	inst := synthUI.inst
	lane, st := synthPadLaneStep(inst, i)
	var rec bool
	synthWithEngine(func(p *synthProject, e *synthEngine) {
		e.updateRT()
		gate := 0
		if inst != synthInstDrums {
			gate = int(e.baseStepSamples() * 2 * e.rt[inst].length)
		}
		if p.RecLive && e.playing {
			pat, step := e.recordStep()
			if step < synthClampInt(p.Steps, 4, synthMaxSteps) {
				p.Patterns[pat].Lanes[lane][step] = st
				rec = true
				// Landing on the next step, the sequencer plays it in a few
				// milliseconds; sounding it now as well would double it.
				if step != e.playingStep() || pat != e.firedPat {
					return
				}
			}
		}
		e.playLane(lane, st, gate)
	})
	if rec {
		synth.mu.Lock()
		synth.dirty = true
		synth.editAt = time.Now()
		synth.mu.Unlock()
	}
}

// synthPadLaneStep is what pad i plays for instrument inst: a drum (pads
// 4..6 are the kick, snare and hat accented) or scale degree i.
func synthPadLaneStep(inst, i int) (int, synthStep) {
	if inst == synthInstDrums {
		if i < synthDrumLanes {
			return i, synthStep{On: true}
		}
		return i - synthDrumLanes, synthStep{On: true, Accent: true}
	}
	lane := synthLaneBass
	switch inst {
	case synthInstLead:
		lane = synthLaneLead
	case synthInstChords:
		lane = synthLaneChord
	}
	return lane, synthStep{On: true, Note: i}
}

// synthArm asks for a second press before a destructive action.
func synthArm(id, prompt string) bool {
	if synthUI.armed == id && time.Since(synthUI.armedAt) < synthArmWindow {
		synthUI.armed = ""
		return true
	}
	synthUI.armed, synthUI.armedAt = id, time.Now()
	ShowToast(prompt, ToastKindWarning)
	return false
}

// synthRandomBeat writes a fresh groove into the current pattern: four on the
// floor with variations, backbeat, busy hats, and a bass line that walks the
// scale on the kicks.
func synthRandomBeat(p *synthProject) {
	pat := &p.Patterns[p.Current]
	*pat = synthPattern{}
	steps := synthClampInt(p.Steps, 4, synthMaxSteps)
	set := func(lane, s, n int, acc bool) {
		if s < steps {
			pat.Lanes[lane][s] = synthStep{On: true, Note: n, Accent: acc}
		}
	}
	for s := 0; s < steps; s++ {
		switch {
		case s%4 == 0:
			if s == 0 || rand.IntN(6) > 0 {
				set(synthLaneKick, s, 0, s == 0)
			}
		case s%2 == 0 && rand.IntN(5) == 0:
			set(synthLaneKick, s, 0, false)
		case rand.IntN(12) == 0:
			set(synthLaneKick, s, 0, false)
		}
		if s%8 == 4 {
			set(synthLaneSnare, s, 0, s%16 == 12)
		} else if rand.IntN(14) == 0 {
			set(synthLaneSnare, s, 0, false)
		}
		if s%2 == 0 || rand.IntN(3) > 0 {
			set(synthLaneHat, s, 0, s%4 == 2)
			if rand.IntN(16) == 0 {
				pat.Lanes[synthLaneHat][s].Ratchet = true
			}
		}
		if s%8 == 4 && rand.IntN(2) == 0 {
			set(synthLaneClap, s, 0, false)
		}
	}
	deg := 0
	for s := 0; s < steps; s++ {
		if pat.Lanes[synthLaneKick][s].On || (s%2 == 1 && rand.IntN(4) == 0) {
			if s > 0 {
				deg += []int{-2, -1, 0, 0, 1, 2, 4, -3}[rand.IntN(8)]
				deg = synthClampInt(deg, -3, 7)
			}
			set(synthLaneBass, s, deg, s == 0)
		}
	}
	for s := 0; s < steps; s += 2 + rand.IntN(3) {
		if rand.IntN(3) > 0 {
			set(synthLaneLead, s, rand.IntN(8), false)
		}
	}
	set(synthLaneChord, 0, []int{0, 3, 5}[rand.IntN(3)], false)
	if steps > 8 {
		set(synthLaneChord, 8, []int{0, 4, 5, 6}[rand.IntN(4)], false)
	}
}

// synthSurprise gives instrument inst a random but usable sound, keeping its
// level, octave and pan.
func synthSurprise(p *synthProject, inst int) {
	if inst == synthInstDrums {
		for i := range p.Drums {
			d := &p.Drums[i]
			d.Tune = 0.25 + rand.Float64()*0.5
			d.Decay = 0.1 + rand.Float64()*0.7
			d.Delay = []float64{0, 0, 0.1, 0.25}[rand.IntN(4)]
		}
		return
	}
	t := &p.Tones[inst]
	t.Wave = rand.IntN(synthWaveCount)
	t.Cutoff = 0.15 + rand.Float64()*0.65
	t.Reso = rand.Float64() * 0.8
	t.EnvAmt = rand.Float64() * 0.8
	t.Attack = []float64{0, 0, 0.05, 0.2, 0.5}[rand.IntN(5)]
	t.Decay = 0.1 + rand.Float64()*0.7
	t.Sustain = rand.Float64() * 0.9
	t.Release = 0.05 + rand.Float64()*0.55
	t.Length = 0.2 + rand.Float64()*0.6
	t.Glide = []float64{0, 0, 0.15, 0.3}[rand.IntN(4)]
	t.Detune = []float64{0, 0, 0.3, 0.6}[rand.IntN(4)]
	t.Delay = rand.Float64() * 0.5
}

// synthExportWAV renders the song (four bars of the pattern, or the chain
// twice) plus the echo's tail into Downloads/music, off the main thread.
func synthExportWAV() {
	if !synthExporting.CompareAndSwap(false, true) {
		ShowToast("Already exporting", ToastKindInfo)
		return
	}
	proj := synthSnapshot()
	ShowToast("Exporting to WAV…", ToastKindInfo)
	go func() {
		defer synthExporting.Store(false)
		path, secs, err := synthRenderWAV(proj)
		if err != nil {
			logWarn("synth", "export: %v", err)
			ShowToast("Could not export: "+err.Error(), ToastKindWarning)
			return
		}
		trophyEvent(tkSynthExport)
		ShowToast(fmt.Sprintf("Saved %s (%d s) to Downloads/music", filepath.Base(path), secs), ToastKindSuccess)
	}()
}

func synthRenderWAV(proj synthProject) (string, int, error) {
	const rate = 44100
	eng := newSynthEngine(rate)
	eng.proj = &proj
	bars := 4
	if proj.Chain > 1 {
		bars = proj.Chain * 2
	}
	steps := synthClampInt(proj.Steps, 4, synthMaxSteps)
	var song float64
	for b := 0; b < bars; b++ {
		for s := 0; s < steps; s++ {
			song += eng.stepSamples(s)
		}
	}
	songFrames := int(song)
	frames := songFrames + 2*rate
	if frames > 10*60*rate {
		frames = 10 * 60 * rate
	}
	eng.start()
	pcm := make([]byte, 44, 44+frames*4)
	buf := make([]float32, 512*2)
	for done := 0; done < frames; {
		n := min(512, frames-done)
		if eng.playing && done >= songFrames {
			eng.stop()
		}
		if eng.playing && done+n > songFrames {
			n = songFrames - done // stop exactly at the end of the last bar
		}
		eng.render(buf, n)
		for i := 0; i < 2*n; i++ {
			s := int16(math.Max(-1, math.Min(1, float64(buf[i]))) * 32767)
			pcm = binary.LittleEndian.AppendUint16(pcm, uint16(s))
		}
		done += n
	}
	dataLen := len(pcm) - 44
	copy(pcm[0:], "RIFF")
	binary.LittleEndian.PutUint32(pcm[4:], uint32(36+dataLen))
	copy(pcm[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(pcm[16:], 16)
	binary.LittleEndian.PutUint16(pcm[20:], 1) // PCM
	binary.LittleEndian.PutUint16(pcm[22:], 2)
	binary.LittleEndian.PutUint32(pcm[24:], rate)
	binary.LittleEndian.PutUint32(pcm[28:], rate*4)
	binary.LittleEndian.PutUint16(pcm[32:], 4)
	binary.LittleEndian.PutUint16(pcm[34:], 16)
	copy(pcm[36:], "data")
	binary.LittleEndian.PutUint32(pcm[40:], uint32(dataLen))

	dir := downloadsMusicDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	var path string
	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("Synth %03d.wav", i))
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			path = cand
			break
		}
	}
	if path == "" {
		return "", 0, errors.New("the music folder already holds 999 Synth exports")
	}
	if err := AtomicWrite(path, pcm, 0o644); err != nil {
		return "", 0, err
	}
	return path, frames / rate, nil
}

// ── Rows of the Sound and Mix pages ──────────────────────────────────────

const (
	synthRowSlider = iota
	synthRowChoice
	synthRowAction
)

// synthRow is one adjustable line. A slider's get/set work in 0..1; a choice
// steps with step; an action runs act on A.
type synthRow struct {
	label string
	kind  int
	get   func(p *synthProject) float64
	set   func(p *synthProject, v float64)
	step  func(p *synthProject, dir int)
	text  func(p *synthProject) string
	act   func()
}

func synthSlider(label string, get func(p *synthProject) float64, set func(p *synthProject, v float64), text func(p *synthProject) string) synthRow {
	return synthRow{label: label, kind: synthRowSlider, get: get, set: set, text: text}
}

func synthChoice(label string, step func(p *synthProject, dir int), text func(p *synthProject) string) synthRow {
	return synthRow{label: label, kind: synthRowChoice, step: step, text: text}
}

func synthAction(label string, act func()) synthRow {
	return synthRow{label: label, kind: synthRowAction, act: act}
}

func synthPct(v float64) string { return strconv.Itoa(int(math.Round(v*100))) + "%" }

func synthSecs(s float64) string {
	if s < 1 {
		return strconv.Itoa(int(math.Round(s*1000))) + " ms"
	}
	return strconv.FormatFloat(s, 'f', 1, 64) + " s"
}

// synthToneRow is a slider on a melodic tone's field.
func synthToneRow(inst int, label string, field func(t *synthTone) *float64, text func(v float64) string) synthRow {
	return synthSlider(label,
		func(p *synthProject) float64 { return *field(&p.Tones[inst]) },
		func(p *synthProject, v float64) { *field(&p.Tones[inst]) = synthClamp01(v) },
		func(p *synthProject) string { return text(*field(&p.Tones[inst])) })
}

// synthDrumRow is a slider on one drum's field.
func synthDrumRow(d int, label string, field func(x *synthDrum) *float64) synthRow {
	return synthSlider(label,
		func(p *synthProject) float64 { return *field(&p.Drums[d]) },
		func(p *synthProject, v float64) { *field(&p.Drums[d]) = synthClamp01(v) },
		func(p *synthProject) string { return synthPct(*field(&p.Drums[d])) })
}

// synthDrumEcho is the loudest drum's echo send; the row sets all four.
func synthDrumEcho(p *synthProject) float64 {
	m := 0.0
	for _, d := range p.Drums {
		m = math.Max(m, d.Delay)
	}
	return m
}

// synthSoundRows are the Sound page's lines for instrument inst.
func synthSoundRows(inst int) []synthRow {
	rows := make([]synthRow, 0, 18)
	preset := synthChoice("Preset",
		func(p *synthProject, dir int) {
			n := len(synthDrumKits)
			if inst != synthInstDrums {
				n = len(synthPresets[inst])
			}
			synthApplyPreset(p, inst, (p.Tones[inst].Preset+dir+n)%n)
		},
		func(p *synthProject) string { return synthPresetName(p, inst) })
	if inst == synthInstDrums {
		preset.label = "Kit"
		rows = append(rows, preset)
		for d := 0; d < synthDrumLanes; d++ {
			name := synthLaneNames[d]
			rows = append(rows,
				synthDrumRow(d, name+" tune", func(x *synthDrum) *float64 { return &x.Tune }),
				synthDrumRow(d, name+" decay", func(x *synthDrum) *float64 { return &x.Decay }),
				synthDrumRow(d, name+" level", func(x *synthDrum) *float64 { return &x.Volume }),
			)
		}
		rows = append(rows, synthSlider("Drum echo", synthDrumEcho,
			func(p *synthProject, v float64) {
				for i := range p.Drums {
					p.Drums[i].Delay = synthClamp01(v)
				}
			},
			func(p *synthProject) string { return synthPct(synthDrumEcho(p)) }))
	} else {
		rows = append(rows, preset, synthChoice("Wave",
			func(p *synthProject, dir int) {
				p.Tones[inst].Wave = (p.Tones[inst].Wave + dir + synthWaveCount) % synthWaveCount
			},
			func(p *synthProject) string {
				return synthWaveNames[synthClampInt(p.Tones[inst].Wave, 0, synthWaveCount-1)]
			}))
		rows = append(rows,
			synthToneRow(inst, "Cutoff", func(t *synthTone) *float64 { return &t.Cutoff }, func(v float64) string {
				hz := 40 * math.Pow(400, v)
				if hz >= 1000 {
					return strconv.FormatFloat(hz/1000, 'f', 1, 64) + " kHz"
				}
				return strconv.Itoa(int(hz)) + " Hz"
			}),
			synthToneRow(inst, "Resonance", func(t *synthTone) *float64 { return &t.Reso }, synthPct),
			synthToneRow(inst, "Filter env", func(t *synthTone) *float64 { return &t.EnvAmt }, synthPct),
			synthToneRow(inst, "Attack", func(t *synthTone) *float64 { return &t.Attack }, func(v float64) string { return synthSecs(0.001 * math.Pow(1500, v)) }),
			synthToneRow(inst, "Decay", func(t *synthTone) *float64 { return &t.Decay }, func(v float64) string { return synthSecs(0.02 * math.Pow(150, v)) }),
			synthToneRow(inst, "Sustain", func(t *synthTone) *float64 { return &t.Sustain }, synthPct),
			synthToneRow(inst, "Release", func(t *synthTone) *float64 { return &t.Release }, func(v float64) string { return synthSecs(0.01 * math.Pow(300, v)) }),
			synthToneRow(inst, "Length", func(t *synthTone) *float64 { return &t.Length }, func(v float64) string {
				return strconv.FormatFloat(0.1+1.9*v, 'f', 1, 64) + " steps"
			}),
			synthToneRow(inst, "Glide", func(t *synthTone) *float64 { return &t.Glide }, synthPct),
			synthToneRow(inst, "Detune", func(t *synthTone) *float64 { return &t.Detune }, func(v float64) string {
				return strconv.Itoa(int(math.Round(35*v))) + " cents"
			}),
			synthChoice("Octave",
				func(p *synthProject, dir int) {
					p.Tones[inst].Octave = synthClampInt(p.Tones[inst].Octave+dir, -2, 2)
				},
				func(p *synthProject) string {
					o := p.Tones[inst].Octave
					if o > 0 {
						return "+" + strconv.Itoa(o)
					}
					return strconv.Itoa(o)
				}),
			synthToneRow(inst, "Level", func(t *synthTone) *float64 { return &t.Volume }, synthPct),
			synthToneRow(inst, "Pan", func(t *synthTone) *float64 { return &t.Pan }, func(v float64) string {
				switch c := int(math.Round((v - 0.5) * 200)); {
				case c < 0:
					return "L " + strconv.Itoa(-c)
				case c > 0:
					return "R " + strconv.Itoa(c)
				}
				return "Centre"
			}),
			synthToneRow(inst, "Echo", func(t *synthTone) *float64 { return &t.Delay }, synthPct),
		)
	}
	rows = append(rows, synthAction("Surprise me", func() {
		synthEdit(func(p *synthProject, e *synthEngine) { synthSurprise(p, inst) })
		synthAuditionInst(inst)
	}))
	return rows
}

// synthAuditionInst plays instrument inst's root (the kick for the drums).
func synthAuditionInst(inst int) {
	lane, st := synthPadLaneStep(inst, 0)
	synthAudition(lane, st)
}

// synthMixRows are the Mix page's lines: the song's settings in the left
// column, then the actions.
func synthMixRows() []synthRow {
	tempo := synthChoice("Tempo",
		func(p *synthProject, dir int) { p.BPM = synthClampInt(p.BPM+dir, synthMinBPM, synthMaxBPM) },
		func(p *synthProject) string { return strconv.Itoa(p.BPM) + " BPM" })
	tempo.act = synthTapTempo
	return []synthRow{
		tempo,
		synthSlider("Swing",
			func(p *synthProject) float64 { return p.Swing },
			func(p *synthProject, v float64) { p.Swing = synthClamp01(v) },
			func(p *synthProject) string { return synthPct(0.5 + p.Swing*0.25) }),
		synthChoice("Key",
			func(p *synthProject, dir int) { p.Key = (p.Key + dir + 12) % 12 },
			func(p *synthProject) string { return synthKeyNames[synthClampInt(p.Key, 0, 11)] }),
		synthChoice("Scale",
			func(p *synthProject, dir int) {
				n := len(synthScales)
				p.Scale = (p.Scale + dir + n) % n
			},
			func(p *synthProject) string { return synthScales[synthClampInt(p.Scale, 0, len(synthScales)-1)].name }),
		synthChoice("Steps",
			func(p *synthProject, dir int) { p.Steps = synthClampInt(p.Steps+dir, 4, synthMaxSteps) },
			func(p *synthProject) string { return strconv.Itoa(p.Steps) }),
		synthChoice("Play",
			func(p *synthProject, dir int) { p.Chain = synthClampInt(p.Chain+dir, 1, synthPatterns) },
			func(p *synthProject) string {
				if p.Chain <= 1 {
					return "Loop one pattern"
				}
				return "Patterns 1-" + strconv.Itoa(p.Chain)
			}),
		synthChoice("Echo time",
			func(p *synthProject, dir int) {
				n := len(synthDelayTimes)
				p.DelayTime = (p.DelayTime + dir + n) % n
			},
			func(p *synthProject) string {
				return synthDelayTimes[synthClampInt(p.DelayTime, 0, len(synthDelayTimes)-1)].name
			}),
		synthSlider("Echo repeats",
			func(p *synthProject) float64 { return p.DelayFB / 0.9 },
			func(p *synthProject, v float64) { p.DelayFB = synthClamp01(v) * 0.9 },
			func(p *synthProject) string { return synthPct(p.DelayFB / 0.9) }),
		synthSlider("Volume",
			func(p *synthProject) float64 { return p.Master },
			func(p *synthProject, v float64) { p.Master = synthClamp01(v) },
			func(p *synthProject) string { return synthPct(p.Master) }),
		synthChoice("Record from Play",
			func(p *synthProject, dir int) { p.RecLive = !p.RecLive },
			func(p *synthProject) string {
				if p.RecLive {
					return "On"
				}
				return "Off"
			}),
		synthAction("Copy pattern to next", func() {
			var to int
			synthEdit(func(p *synthProject, e *synthEngine) {
				to = (p.Current + 1) % synthPatterns
				p.Patterns[to] = p.Patterns[p.Current]
				p.Current = to
			})
			ShowToast("Copied to pattern "+strconv.Itoa(to+1), ToastKindSuccess)
		}),
		synthAction("Clear pattern", func() {
			cur := synthSnapshot().Current
			if !synthArm("clear", "Press A again to clear pattern "+strconv.Itoa(cur+1)) {
				return
			}
			synthEdit(func(p *synthProject, e *synthEngine) { p.Patterns[p.Current] = synthPattern{} })
		}),
		synthAction("Random beat", func() {
			synthEdit(func(p *synthProject, e *synthEngine) { synthRandomBeat(p) })
		}),
		synthAction("Load the demo", func() {
			if !synthArm("demo", "Press A again to replace this project with the demo") {
				return
			}
			synthEdit(func(p *synthProject, e *synthEngine) {
				*p = synthDefaultProject()
				if e != nil {
					e.silence()
				}
			})
		}),
		synthAction("Export to WAV", synthExportWAV),
	}
}

// synthTapTempo sets the tempo from the last few A presses on the Tempo row.
func synthTapTempo() {
	now := time.Now()
	if n := len(synthUI.taps); n > 0 && now.Sub(synthUI.taps[n-1]) > 2*time.Second {
		synthUI.taps = synthUI.taps[:0]
	}
	synthUI.taps = append(synthUI.taps, now)
	if len(synthUI.taps) > 5 {
		synthUI.taps = synthUI.taps[len(synthUI.taps)-5:]
	}
	n := len(synthUI.taps)
	if n < 2 {
		ShowToast("Tap A in time to set the tempo", ToastKindInfo)
		return
	}
	avg := synthUI.taps[n-1].Sub(synthUI.taps[0]).Seconds() / float64(n-1)
	if avg <= 0 {
		return
	}
	bpm := synthClampInt(int(math.Round(60/avg)), synthMinBPM, synthMaxBPM)
	synthEdit(func(p *synthProject, e *synthEngine) { p.BPM = bpm })
}

// synthAdjustRow moves row r by dir; big is X / Y's coarse step.
func synthAdjustRow(r synthRow, dir int, big bool) {
	switch r.kind {
	case synthRowSlider:
		d := 0.05
		if big {
			d = 0.2
		}
		synthEdit(func(p *synthProject, e *synthEngine) { r.set(p, r.get(p)+d*float64(dir)) })
	case synthRowChoice:
		if big && r.label == "Tempo" {
			dir *= 10
		}
		synthEdit(func(p *synthProject, e *synthEngine) { r.step(p, dir) })
	}
}

// ── Input ────────────────────────────────────────────────────────────────

// synth input actions, whatever device produced them.
const (
	synthInNone = iota
	synthInUp
	synthInDown
	synthInLeft
	synthInRight
	synthInA
	synthInX
	synthInY
	synthInPlay
	synthInTabPrev
	synthInTabNext
	synthInPrev // L2
	synthInNext // R2
	synthInBack
)

func synthSceneActive(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == synthSceneName
}

// synthShoulder takes L2 / R2 (shoulders.go routes them here).
func synthShoulder(s padShoulder) {
	switch s {
	case padL1:
		synthDo(synthInTabPrev, appConfig)
	case padR1:
		synthDo(synthInTabNext, appConfig)
	case padL2:
		synthDo(synthInPrev, appConfig)
	case padR2:
		synthDo(synthInNext, appConfig)
	}
}

// synthPadIndex maps a direction / face action to a Play pad: the D-pad is
// pads 1-4 (left, up, right, down), Y X A are 5-7 (left, top, right).
func synthPadIndex(in int) int {
	switch in {
	case synthInLeft:
		return 0
	case synthInUp:
		return 1
	case synthInRight:
		return 2
	case synthInDown:
		return 3
	case synthInY:
		return 4
	case synthInX:
		return 5
	case synthInA:
		return 6
	}
	return -1
}

// synthDo runs one action on the page.
func synthDo(in int, config *Config) {
	switch in {
	case synthInBack:
		synthWithEngine(func(p *synthProject, e *synthEngine) { e.stop() })
		synthMaybeSave(true)
		goBackScene(config)
		return
	case synthInPlay:
		synthTogglePlay()
		return
	case synthInTabPrev, synthInTabNext:
		d := 1
		if in == synthInTabPrev {
			d = -1
		}
		synthUI.tab = (synthUI.tab + d + synthTabCount) % synthTabCount
		synthUI.armed = ""
		return
	}
	switch synthUI.tab {
	case synthTabPattern:
		synthDoPattern(in)
	case synthTabPlay:
		switch in {
		case synthInPrev:
			synthUI.inst = (synthUI.inst + synthInsts - 1) % synthInsts
		case synthInNext:
			synthUI.inst = (synthUI.inst + 1) % synthInsts
		default:
			synthPadPlay(synthPadIndex(in))
		}
	case synthTabSound:
		rows := synthSoundRows(synthUI.inst)
		synthDoRows(in, rows, &synthUI.soundRow, func() { synthAuditionInst(synthUI.inst) })
	case synthTabMix:
		synthDoRows(in, synthMixRows(), &synthUI.mixRow, nil)
	}
}

func synthDoPattern(in int) {
	p := synthSnapshot()
	steps := synthClampInt(p.Steps, 4, synthMaxSteps)
	if synthUI.step >= steps {
		synthUI.step = steps - 1
	}
	switch in {
	case synthInUp:
		synthUI.lane = (synthUI.lane + synthLanes - 1) % synthLanes
	case synthInDown:
		synthUI.lane = (synthUI.lane + 1) % synthLanes
	case synthInLeft:
		synthUI.step--
		if synthUI.step < -1 {
			synthUI.step = steps - 1
		}
	case synthInRight:
		synthUI.step++
		if synthUI.step >= steps {
			synthUI.step = -1
		}
	case synthInPrev:
		synthSetPattern(-1)
	case synthInNext:
		synthSetPattern(1)
	case synthInA:
		if synthUI.step < 0 {
			lane := synthUI.lane
			synthEdit(func(p *synthProject, e *synthEngine) { p.Mute[lane] = !p.Mute[lane] })
			return
		}
		synthEditStep(func(st *synthStep, lane int) {
			if st.On {
				st.On = false
				return
			}
			*st = synthStep{On: true, Note: synthUI.lastNote[lane]}
		})
	case synthInY, synthInX:
		if synthUI.step < 0 {
			return
		}
		up := in == synthInY
		synthEditStep(func(st *synthStep, lane int) {
			if lane < synthDrumLanes {
				if !st.On {
					*st = synthStep{On: true}
				}
				if up {
					st.Accent = !st.Accent
				} else {
					st.Ratchet = !st.Ratchet
				}
				return
			}
			if !st.On {
				*st = synthStep{On: true, Note: synthUI.lastNote[lane]}
			} else if up {
				st.Note = synthClampInt(st.Note+1, synthMinDegree, synthMaxDegree)
			} else {
				st.Note = synthClampInt(st.Note-1, synthMinDegree, synthMaxDegree)
			}
			synthUI.lastNote[lane] = st.Note
		})
	}
}

// synthDoRows drives a list of rows: Up / Down pick, Left / Right adjust,
// X / Y adjust in big steps, A runs the row (or auditions).
func synthDoRows(in int, rows []synthRow, cur *int, audition func()) {
	if len(rows) == 0 {
		return
	}
	*cur = synthClampInt(*cur, 0, len(rows)-1)
	r := rows[*cur]
	switch in {
	case synthInUp:
		*cur = (*cur + len(rows) - 1) % len(rows)
	case synthInDown:
		*cur = (*cur + 1) % len(rows)
	case synthInLeft:
		synthAdjustRow(r, -1, false)
	case synthInRight:
		synthAdjustRow(r, 1, false)
	case synthInX:
		synthAdjustRow(r, -1, true)
	case synthInY:
		synthAdjustRow(r, 1, true)
	case synthInA:
		switch {
		case r.act != nil:
			r.act()
		case audition != nil:
			audition()
		case r.kind == synthRowChoice:
			synthAdjustRow(r, 1, false)
		}
	case synthInPrev, synthInNext:
		if synthUI.tab == synthTabSound {
			d := 1
			if in == synthInPrev {
				d = -1
			}
			synthUI.inst = (synthUI.inst + d + synthInsts) % synthInsts
			*cur = 0
		} else if in == synthInPrev {
			synthSetPattern(-1)
		} else {
			synthSetPattern(1)
		}
	}
}

func synthPadAction(e *sdl.ControllerButtonEvent) int {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return synthInUp
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		return synthInDown
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		return synthInLeft
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		return synthInRight
	case sdl.CONTROLLER_BUTTON_A:
		return synthInA
	case sdl.CONTROLLER_BUTTON_X:
		return synthInX
	case sdl.CONTROLLER_BUTTON_Y:
		return synthInY
	case sdl.CONTROLLER_BUTTON_START:
		return synthInPlay
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		return synthInTabPrev
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		return synthInTabNext
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		return synthInBack
	}
	return synthInNone
}

func synthKeyAction(e *sdl.KeyboardEvent) int {
	shift := e.Keysym.Mod&sdl.KMOD_SHIFT != 0
	switch e.Keysym.Sym {
	case sdl.K_UP:
		return synthInUp
	case sdl.K_DOWN:
		return synthInDown
	case sdl.K_LEFT:
		return synthInLeft
	case sdl.K_RIGHT:
		return synthInRight
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_a:
		return synthInA
	case sdl.K_x, sdl.K_MINUS, sdl.K_KP_MINUS:
		return synthInX
	case sdl.K_y, sdl.K_EQUALS, sdl.K_PLUS, sdl.K_KP_PLUS:
		return synthInY
	case sdl.K_SPACE:
		return synthInPlay
	case sdl.K_TAB:
		if shift {
			return synthInTabPrev
		}
		return synthInTabNext
	case sdl.K_LEFTBRACKET, sdl.K_PAGEUP:
		return synthInPrev
	case sdl.K_RIGHTBRACKET, sdl.K_PAGEDOWN:
		return synthInNext
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		return synthInBack
	}
	return synthInNone
}

type synthScene struct{ sceneBase }

func (synthScene) render(r *sdl.Renderer, c *Config) bool { renderSynth(r, c); return true }

func (synthScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return true
	}
	// The number keys play the pads on the Play page.
	if synthUI.tab == synthTabPlay && e.Keysym.Sym >= sdl.K_1 && e.Keysym.Sym <= sdl.K_7 {
		if e.Repeat == 0 {
			synthPadPlay(int(e.Keysym.Sym - sdl.K_1))
		}
		return true
	}
	in := synthKeyAction(e)
	if in == synthInNone {
		return true
	}
	// A held arrow walks the grid; a held note or Space does not retrigger.
	if e.Repeat != 0 && in != synthInUp && in != synthInDown && in != synthInLeft && in != synthInRight {
		return true
	}
	if e.Repeat != 0 && synthUI.tab == synthTabPlay {
		return true
	}
	synthDo(in, c)
	return true
}

func (synthScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	// A held D-pad repeats (navrepeat.go): fine for walking the grid, but on
	// the Play page it would machine-gun the pad.
	if e.Which == padRepeatWhich && synthUI.tab == synthTabPlay {
		return true
	}
	in := synthPadAction(e)
	if in == synthInNone {
		return false
	}
	synthDo(in, c)
	return true
}

// synthEnsureScene gives the page its scene, in memory, like the Calculator
// (toolshub.go calls it at startup and after every reload).
func synthEnsureScene(config *Config) {
	if config == nil || sceneIndexExact(config, synthSceneName) >= 0 {
		return
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        synthSceneName,
		Icon:        "S",
		Description: "Pocket synthesizer and groovebox",
	})
}

// synthFooterHints names the keys of the page on show (extras.go).
func synthFooterHints() []FooterHint {
	play := FooterHint{Button: "Start / Space", Label: "Play / Stop"}
	tabs := FooterHint{Button: "L1/R1 / Tab", Label: "Page"}
	back := FooterHint{Button: "B", Label: "Back"}
	switch synthUI.tab {
	case synthTabPlay:
		return []FooterHint{
			{Button: "D-Pad", Label: "Pads 1-4"},
			{Button: "Y X A", Label: "Pads 5-7"},
			{Button: "L2/R2 / [ ]", Label: "Instrument"},
			play, tabs, back,
		}
	case synthTabSound, synthTabMix:
		other := FooterHint{Button: "L2/R2 / [ ]", Label: "Pattern"}
		a := FooterHint{Button: "A", Label: "Run"}
		if synthUI.tab == synthTabSound {
			other.Label = "Instrument"
			a.Label = "Listen"
		}
		return []FooterHint{
			{Button: "D-Pad", Label: "Choose / Adjust"},
			{Button: "X / Y", Label: "Big step"},
			a, other, play, tabs, back,
		}
	}
	if synthUI.step < 0 {
		return []FooterHint{
			{Button: "D-Pad", Label: "Move"},
			{Button: "A", Label: "Mute"},
			{Button: "L2/R2 / [ ]", Label: "Pattern"},
			play, tabs, back,
		}
	}
	if synthUI.lane < synthDrumLanes {
		return []FooterHint{
			{Button: "A", Label: "Step"},
			{Button: "Y", Label: "Accent"},
			{Button: "X", Label: "Ratchet"},
			{Button: "L2/R2 / [ ]", Label: "Pattern"},
			play, tabs, back,
		}
	}
	return []FooterHint{
		{Button: "A", Label: "Step"},
		{Button: "Y", Label: "Note up"},
		{Button: "X", Label: "Note down"},
		{Button: "L2/R2 / [ ]", Label: "Pattern"},
		play, tabs, back,
	}
}

// ── Drawing ──────────────────────────────────────────────────────────────

func synthTextW(font *ttf.Font, s string) int32 {
	if font == nil {
		return 0
	}
	w, _, _ := font.SizeUTF8(s)
	return int32(w)
}

func synthFontH(font *ttf.Font) int32 {
	if font == nil {
		return 16
	}
	return int32(font.Height())
}

// renderSynth draws the page.
func renderSynth(renderer *sdl.Renderer, config *Config) {
	synthEnsureLoaded()
	synthTouch()
	p := synthSnapshot()
	live := synthLive()

	sub := "Pattern " + strconv.Itoa(p.Current+1) + " · " + strconv.Itoa(p.BPM) + " BPM · " +
		synthKeyNames[synthClampInt(p.Key, 0, 11)] + " " + synthScales[synthClampInt(p.Scale, 0, len(synthScales)-1)].name
	status, statusInk := "Stopped", sdl.Color{}
	switch {
	case live.playing && p.RecLive:
		status, statusInk = "Recording", sdl.Color{R: 255, G: 110, B: 100, A: 255}
	case live.playing:
		status, statusInk = "Playing", sdl.Color{R: 120, G: 224, B: 150, A: 255}
	case !live.open:
		synth.mu.Lock()
		failed := !synth.failAt.IsZero()
		synth.mu.Unlock()
		if failed {
			status, statusInk = "No sound device", ColorToastWarn
			if !synthUI.failShow {
				synthUI.failShow = true
				ShowToast("The Synth could not open the speaker; it will keep trying", ToastKindWarning)
			}
		}
	}
	header := tbxHeader("Synth", sub, "synth", status, statusInk)
	f := tbxBeginFrame(renderer, config, header)
	body := f.bodyRect()
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)

	small, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")

	tabsH := int32(40)
	synthDrawTabs(renderer, config, medium, sdl.Rect{X: body.X, Y: body.Y, W: body.W, H: tabsH})
	stripH := int32(56)
	gap := int32(12)
	content := sdl.Rect{X: body.X, Y: body.Y + tabsH + gap, W: body.W, H: body.H - tabsH - stripH - 2*gap}
	if content.H > 40 {
		switch synthUI.tab {
		case synthTabPattern:
			synthDrawPattern(renderer, config, small, content, &p, &live)
		case synthTabPlay:
			synthDrawPlay(renderer, config, small, medium, content, &p)
		case synthTabSound:
			synthDrawRows(renderer, config, small, content, &p, synthSoundRows(synthUI.inst), synthUI.soundRow, synthInstColor(synthUI.inst), true)
		case synthTabMix:
			synthDrawRows(renderer, config, small, content, &p, synthMixRows(), synthUI.mixRow, glassAccent(), false)
		}
	}
	synthDrawStrip(renderer, config, small, sdl.Rect{X: body.X, Y: body.Y + body.H - stripH, W: body.W, H: stripH}, &p, &live)
	tbxEndFrame(renderer, config)
	synthMaybeSave(false)
}

// synthDrawTabs draws the four page pills.
func synthDrawTabs(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect) {
	w := min32((rect.W-3*10)/synthTabCount, 170)
	for i := 0; i < synthTabCount; i++ {
		r := sdl.Rect{X: rect.X + int32(i)*(w+10), Y: rect.Y, W: w, H: rect.H}
		ink := ColorTextSecondary()
		if i == synthUI.tab {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(glassAccent(), 210))
			ink = ColorIconDark
		} else {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, glassFill())
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, 1, glassEdge())
		}
		if font != nil {
			label := synthTabNames[i]
			tw := synthTextW(font, label)
			renderText(renderer, config, font, label, ink, r.X+(r.W-tw)/2, r.Y+(r.H-synthFontH(font))/2)
		}
	}
}

// synthStepRect is step s's cell in a grid starting at gx with the given
// pitch; every fourth step starts a little further right.
func synthStepRect(gx, y, pitch, groupGap, h int32, s int) sdl.Rect {
	x := gx + int32(s)*pitch + int32(s/4)*groupGap
	return sdl.Rect{X: x, Y: y, W: pitch - 4, H: h}
}

func synthDrawPattern(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, p *synthProject, live *synthLiveView) {
	steps := synthClampInt(p.Steps, 4, synthMaxSteps)
	infoH := int32(30)
	rowH := min32((rect.H-infoH)/synthLanes, 58)
	if rowH < 20 {
		rowH = 20
	}
	nameW := min32(124, rect.W/7)
	gx := rect.X + nameW + 10
	groupGap := int32(8)
	groups := int32((steps + 3) / 4)
	pitch := (rect.W - nameW - 10 - groupGap*(groups-1)) / int32(steps)
	pat := &p.Patterns[p.Current]
	showHead := live.playing && live.firedPat == p.Current && live.step >= 0 && live.step < steps

	if showHead {
		col := synthStepRect(gx, rect.Y-4, pitch, groupGap, rowH*synthLanes+4, live.step)
		fillRoundedRect(renderer, col.X-2, col.Y, col.W+4, col.H, 8, sdl.Color{R: 255, G: 255, B: 255, A: 26})
	}
	for lane := 0; lane < synthLanes; lane++ {
		y := rect.Y + int32(lane)*rowH
		ch := rowH - 6
		color := synthLaneColors[lane]
		muted := p.Mute[lane]
		// The lane's name cell (A mutes it).
		nr := sdl.Rect{X: rect.X, Y: y, W: nameW, H: ch}
		fillRoundedRect(renderer, nr.X, nr.Y, nr.W, nr.H, 10, glassFill())
		dot := WithAlpha(color, 255)
		if muted {
			dot = WithAlpha(color, 70)
		} else if fl := live.flash[lane]; fl > 0.05 {
			fillRoundedRect(renderer, nr.X, nr.Y, nr.W, nr.H, 10, WithAlpha(color, uint8(60*fl)))
		}
		fillCircle(renderer, nr.X+16, nr.Y+ch/2, 6, dot)
		if font != nil {
			ink := ColorTextPrimary()
			label := synthLaneNames[lane]
			if muted {
				ink = ColorTextTertiary()
				label += " (off)"
			}
			label = fitTextCached(font, label, nr.W-34)
			renderText(renderer, config, font, label, ink, nr.X+30, nr.Y+(ch-synthFontH(font))/2)
		}
		if synthUI.lane == lane && synthUI.step < 0 {
			drawGlassFocusRing(renderer, nr, 10)
		}
		for s := 0; s < steps; s++ {
			c := synthStepRect(gx, y, pitch, groupGap, ch, s)
			st := pat.Lanes[lane][s]
			if !st.On {
				a := uint8(14)
				if s%4 == 0 {
					a = 24
				}
				fillRoundedRect(renderer, c.X, c.Y, c.W, c.H, 7, sdl.Color{R: 255, G: 255, B: 255, A: a})
			} else {
				a := uint8(200)
				if muted {
					a = 80
				} else if showHead && s == live.step {
					a = 255
				}
				fillRoundedRect(renderer, c.X, c.Y, c.W, c.H, 7, WithAlpha(color, a))
				if st.Accent {
					fillCircle(renderer, c.X+c.W-8, c.Y+8, 3, sdl.Color{R: 255, G: 255, B: 255, A: 230})
				}
				if st.Ratchet {
					for k := int32(0); k < 2; k++ {
						fillRoundedRect(renderer, c.X+6+k*8, c.Y+c.H-9, 5, 5, 2, sdl.Color{R: 20, G: 20, B: 30, A: 200})
					}
				}
				if lane >= synthDrumLanes && font != nil && c.W >= 30 {
					name := synthNoteName(synthDegreeMidi(p, synthLaneInst(lane), st.Note))
					if tw := synthTextW(font, name); tw <= c.W-4 {
						renderText(renderer, config, font, name, ColorIconDark, c.X+(c.W-tw)/2, c.Y+(c.H-synthFontH(font))/2)
					}
				}
			}
			if synthUI.lane == lane && synthUI.step == s {
				drawGlassFocusRing(renderer, c, 7)
			}
		}
	}
	// What the cursor is on.
	if font == nil {
		return
	}
	info := synthLaneNames[synthUI.lane]
	if synthUI.step < 0 {
		if p.Mute[synthUI.lane] {
			info += " · A turns the lane back on"
		} else {
			info += " · A mutes the lane"
		}
	} else {
		info += " · Step " + strconv.Itoa(synthUI.step+1)
		if st := pat.Lanes[synthUI.lane][synthUI.step]; st.On {
			if synthUI.lane >= synthDrumLanes {
				m := synthDegreeMidi(p, synthLaneInst(synthUI.lane), st.Note)
				info += " · " + synthNoteName(m)
				if synthUI.lane == synthLaneChord {
					info += " chord"
				}
			}
			if st.Accent {
				info += " · Accent"
			}
			if st.Ratchet {
				info += " · Ratchet"
			}
		}
	}
	y := rect.Y + rowH*synthLanes + 4
	renderText(renderer, config, font, info, ColorTextSecondary(), rect.X, y)
	if live.playing && live.nextPat != p.Current && p.Chain <= 1 {
		msg := "Pattern " + strconv.Itoa(p.Current+1) + " starts with the next bar"
		renderText(renderer, config, font, msg, ColorTextTertiary(), rect.X+rect.W-synthTextW(font, msg), y)
	}
}

// synthPadRects places the seven pads like the buttons that play them: a
// cross on the left (the D-pad) and Y / X / A of a diamond on the right.
func synthPadRects(rect sdl.Rect) ([7]sdl.Rect, sdl.Rect) {
	// The cross is three pads and two gaps tall (3s + s/4).
	s := min32(rect.H*4/13, 104)
	g := s / 8
	cy := rect.Y + rect.H/2
	lx := rect.X + rect.W/4
	rx := rect.X + rect.W*3/4
	at := func(cx, cy int32) sdl.Rect { return sdl.Rect{X: cx - s/2, Y: cy - s/2, W: s, H: s} }
	d := s + g
	pads := [7]sdl.Rect{
		at(lx-d, cy), at(lx, cy-d), at(lx+d, cy), at(lx, cy+d),
		at(rx-d, cy), at(rx, cy-d), at(rx+d, cy),
	}
	return pads, at(rx, cy+d)
}

func synthDrawPlay(renderer *sdl.Renderer, config *Config, small, medium *ttf.Font, rect sdl.Rect, p *synthProject) {
	inst := synthUI.inst
	color := synthInstColor(inst)
	// The instrument chips.
	synthDrawInstChips(renderer, config, small, sdl.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: 34}, inst)
	area := sdl.Rect{X: rect.X, Y: rect.Y + 44, W: rect.W, H: rect.H - 44}
	padArea := area
	padArea.H -= synthFontH(small) + 8
	pads, bRect := synthPadRects(padArea)
	keys := [7]string{"1", "2", "3", "4", "Y · 5", "X · 6", "A · 7"}
	now := time.Now()
	for i, r := range pads {
		lane, st := synthPadLaneStep(inst, i)
		fill := WithAlpha(color, 70)
		if fl := 1 - now.Sub(synthUI.padFlash[i]).Seconds()/0.25; fl > 0 {
			fill = WithAlpha(color, uint8(70+185*fl))
		}
		fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 18, fill)
		strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, 18, 2, WithAlpha(color, 200))
		label := synthLaneNames[lane]
		if inst != synthInstDrums {
			label = synthNoteName(synthDegreeMidi(p, inst, st.Note))
		} else if st.Accent {
			label += "+"
		}
		if medium != nil {
			tw := synthTextW(medium, label)
			renderText(renderer, config, medium, label, ColorTextPrimary(), r.X+(r.W-tw)/2, r.Y+(r.H-synthFontH(medium))/2-6)
		}
		if small != nil {
			k := keys[i]
			tw := synthTextW(small, k)
			renderText(renderer, config, small, k, ColorTextTertiary(), r.X+(r.W-tw)/2, r.Y+r.H-synthFontH(small)-6)
		}
	}
	// B stays Back, drawn dim where it sits on the pad.
	strokeRoundedRect(renderer, bRect.X, bRect.Y, bRect.W, bRect.H, 18, 1, glassEdge())
	if small != nil {
		tw := synthTextW(small, "B · Back")
		renderText(renderer, config, small, "B · Back", ColorTextTertiary(), bRect.X+(bRect.W-tw)/2, bRect.Y+(bRect.H-synthFontH(small))/2)
		line := synthPresetName(p, inst) + " · " + synthKeyNames[synthClampInt(p.Key, 0, 11)] + " " +
			synthScales[synthClampInt(p.Scale, 0, len(synthScales)-1)].name
		if p.RecLive {
			line += " · Notes are recorded while the pattern plays"
		}
		renderText(renderer, config, small, line, ColorTextSecondary(), area.X, area.Y+area.H-synthFontH(small))
	}
}

func synthDrawInstChips(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, cur int) {
	x := rect.X
	for i := 0; i < synthInsts; i++ {
		label := synthInstNames[i]
		w := synthTextW(font, label) + 36
		r := sdl.Rect{X: x, Y: rect.Y, W: w, H: rect.H}
		c := synthInstColor(i)
		if i == cur {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, WithAlpha(c, 220))
		} else {
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, r.H/2, glassFill())
			fillCircle(renderer, r.X+14, r.Y+r.H/2, 4, c)
		}
		if font != nil {
			ink := ColorTextSecondary()
			if i == cur {
				ink = ColorIconDark
			}
			renderText(renderer, config, font, label, ink, r.X+22, r.Y+(r.H-synthFontH(font))/2)
		}
		x += w + 8
	}
	if font != nil {
		hint := hintKeyFor("L2/R2 / [ ]", "Instrument") + " changes the instrument"
		renderText(renderer, config, font, hint, ColorTextTertiary(), rect.X+rect.W-synthTextW(font, hint), rect.Y+(rect.H-synthFontH(font))/2)
	}
}

// synthDrawRows draws the Sound or Mix page: rows in two columns, each a
// label, a slider (or the choice) and its value.
func synthDrawRows(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, p *synthProject, rows []synthRow, cur int, color sdl.Color, chips bool) {
	if chips {
		synthDrawInstChips(renderer, config, font, sdl.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: 34}, synthUI.inst)
		rect.Y += 44
		rect.H -= 44
	}
	n := len(rows)
	if n == 0 {
		return
	}
	perCol := (n + 1) / 2
	if !chips {
		perCol = 10 // Mix: the settings on the left, the actions on the right
	}
	rowH := min32(rect.H/int32(perCol), 44)
	if rowH < 22 {
		rowH = 22
	}
	colGap := int32(24)
	colW := (rect.W - colGap) / 2
	cur = synthClampInt(cur, 0, n-1)
	for i, r := range rows {
		col, row := i/perCol, i%perCol
		box := sdl.Rect{X: rect.X + int32(col)*(colW+colGap), Y: rect.Y + int32(row)*rowH, W: colW, H: rowH - 6}
		focused := i == cur
		drawGlassSurface(renderer, box, 10, glassState{Focused: focused, NoLift: true})
		if font == nil {
			continue
		}
		ty := box.Y + (box.H-synthFontH(font))/2
		labelW := box.W * 2 / 5
		ink := ColorTextPrimary()
		if r.kind == synthRowAction {
			ink = ColorTextAccent()
		}
		renderText(renderer, config, font, fitTextCached(font, r.label, labelW-16), ink, box.X+14, ty)
		val := ""
		if r.text != nil {
			val = r.text(p)
		}
		vw := synthTextW(font, val)
		renderText(renderer, config, font, val, ColorTextSecondary(), box.X+box.W-14-vw, ty)
		if r.kind == synthRowSlider && r.get != nil {
			tx := box.X + labelW
			tw := box.W - labelW - 14 - max32(vw, 64) - 14
			if tw > 30 {
				th := int32(6)
				tyy := box.Y + box.H/2 - th/2
				fillRoundedRect(renderer, tx, tyy, tw, th, th/2, sdl.Color{R: 255, G: 255, B: 255, A: 30})
				fw := int32(float64(tw) * synthClamp01(r.get(p)))
				if fw > 0 {
					fillRoundedRect(renderer, tx, tyy, fw, th, th/2, WithAlpha(color, 230))
				}
				fillCircle(renderer, tx+fw, tyy+th/2, 7, WithAlpha(color, 255))
			}
		}
	}
	if !chips && n > perCol {
		// Under the actions: where an export goes.
		y := rect.Y + int32(n-perCol)*rowH + 8
		x := rect.X + colW + colGap + 4
		renderText(renderer, config, font, "Exports go to Downloads/music, where Music plays them.", ColorTextTertiary(), x, y)
		if synthExporting.Load() {
			renderText(renderer, config, font, "Exporting…", ColorTextAccent(), x, y+synthFontH(font)+4)
		}
	}
}

// synthDrawStrip is the bottom band on every page: the eight pattern slots
// and the output scope.
func synthDrawStrip(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect, p *synthProject, live *synthLiveView) {
	box := rect.H - 8
	x := rect.X
	for i := 0; i < synthPatterns; i++ {
		r := sdl.Rect{X: x, Y: rect.Y + 4, W: box, H: box}
		playing := live.playing && live.firedPat == i
		switch {
		case playing:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, WithAlpha(glassAccent(), 200))
		case !p.Patterns[i].empty():
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, sdl.Color{R: 255, G: 255, B: 255, A: 40})
		default:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, 10, glassFill())
		}
		if i == p.Current {
			strokeRoundedRectAt(renderer, r, homeFocusGlide.apply(r), 10, 3, glassAccent())
		}
		if font != nil {
			n := strconv.Itoa(i + 1)
			ink := ColorTextSecondary()
			if playing {
				ink = ColorIconDark
			}
			renderText(renderer, config, font, n, ink, r.X+(r.W-synthTextW(font, n))/2, r.Y+(r.H-synthFontH(font))/2)
		}
		x += box + 8
	}
	// The scope.
	sc := sdl.Rect{X: x + 16, Y: rect.Y + 4, W: rect.X + rect.W - x - 16, H: box}
	if sc.W < 60 {
		return
	}
	fillRoundedRect(renderer, sc.X, sc.Y, sc.W, sc.H, 10, glassFill())
	mid := sc.Y + sc.H/2
	pts := make([]sdl.Point, 0, 128)
	for i := int32(0); i < 128; i++ {
		v := live.scope[int(i)*synthScopeLen/128]
		pts = append(pts, sdl.Point{X: sc.X + 8 + i*(sc.W-16)/127, Y: mid - int32(float32(sc.H/2-4)*v)})
	}
	c := glassAccent()
	_ = renderer.SetDrawColor(c.R, c.G, c.B, 230)
	_ = renderer.DrawLines(pts)
}
