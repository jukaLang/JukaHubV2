package main

// pads.go — the Pads page: a 64-pad sample sequencer for the handheld. Put
// sounds on pads, tap out a beat,
// chop a recording into slices, build patterns, chain them into a song and
// play the mix with effects on the analog sticks.
//
// Six pages, switched with L1 / R1 (Tab on a keyboard):
//
//   - Play: a bank of sixteen pads (four banks, A-D, 64 pads). The D-pad
//     walks them and A plays the focused one; Up / Down past the edge goes
//     to the next bank. Select turns on finger drumming: the D-pad plays one
//     row of four and Y X B A the row under it, L2 / R2 move to the next two
//     rows. With "Record live hits" on, hits land in the pattern while it
//     plays.
//   - Beat: the pattern's grid for the bank's sixteen pads, sixteen steps at
//     a time (patterns are 16 to 128 steps). A sets a step (hold A and move
//     to paint the row), Y / X move its note a semitone, Select accents it.
//   - Pad: the focused pad's sound and settings: volume, pitch and fine
//     tune, a one-knob filter, trim, fades, reverse, play mode (one-shot,
//     hold, loop), choke group, pan, echo and reverb sends, lo-fi, mute, and
//     Chop (equal slices or at the hits it hears) onto the following pads.
//   - Song: eight patterns, their length, copy / clear, and the song: a chain
//     of patterns played in order when the song plays.
//   - FX: which effect each of the eight stick directions plays (filter,
//     tempo bend, fade out, echo, reverb, stutter, crush, auto-pan, tape
//     stop), and latched amounts for a handheld without sticks.
//   - Mix: tempo (A taps it), swing, master, compressor, echo and reverb,
//     live recording, the starter kit, and Export to WAV (Downloads/music).
//
// Start (Space) plays and stops wherever you are. Like the Synth, the page
// opens its own stereo device while it is drawn and closes it about a second
// after it stops being drawn. The project saves itself to pads.json a couple
// of seconds after each change and when the page closes; the sounds stay
// where they are (only their paths are saved).

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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	padsSceneName = "Pads"
	padsStoreFile = "pads.json"
	// padsIdleStop is how long the page may go undrawn before its device
	// closes, as the Synth's.
	padsIdleStop  = 1200 * time.Millisecond
	padsBlock     = 256
	padsSaveDelay = 2 * time.Second
	padsArmWindow = 3 * time.Second
)

const (
	padsTabPlay = iota
	padsTabBeat
	padsTabPad
	padsTabSong
	padsTabFX
	padsTabMix
	padsTabCount
)

var padsTabNames = [padsTabCount]string{"Play", "Beat", "Pad", "Song", "FX", "Mix"}

// padsBankColors tint each bank's pads.
var padsBankColors = [padsBanks]sdl.Color{
	{R: 255, G: 128, B: 96, A: 255},
	{R: 92, G: 196, B: 255, A: 255},
	{R: 132, G: 226, B: 150, A: 255},
	{R: 214, G: 140, B: 255, A: 255},
}

// padsSt is the shared state: the project, which the audio goroutine reads
// while it renders, the engine of the open device and the loaded sounds.
// mu guards all of it.
var padsSt struct {
	mu      sync.Mutex
	proj    padsProject
	loaded  bool
	eng     *padsEngine
	running bool
	failAt  time.Time
	dirty   bool
	editAt  time.Time
	samples map[string]*padsSample
	loading map[string]bool
	failed  map[string]string
	bytes   int64
	preview string
}

// padsHeld is a live press holding a hold or loop pad: a pad button or a
// key, polled every frame until it is let go.
type padsHeld struct {
	id   int
	btn  int // controller button, or -1
	scan int // keyboard scancode, or -1
}

// padsUI is the page's own state. Main thread only.
var padsUI struct {
	tab       int
	bank      int // the bank on show (Play and Beat)
	cur       int // focused pad in the bank (Play)
	pad       int // the pad the Pad page edits (0..63)
	row       int // Beat cursor: pad in the bank
	step      int // Beat cursor: step
	drum      bool
	drumPair  int // finger drumming: bank*2 + half
	padRow    int
	songRow   int
	songTop   int
	fxRow     int
	mixRow    int
	lastNote  int
	painting  bool
	paintOn   bool
	armed     string
	armedAt   time.Time
	taps      []time.Time
	flash     [padsCount]time.Time
	held      []padsHeld
	clip      padsPad
	hasClip   bool
	failShow  bool
	br        padsBrowser
	stickFxOn [padsDirCount]float64
}

// padsBrowser is the sample browser over the page.
type padsBrowser struct {
	open   bool
	dir    string // "" is the list of sources
	rows   []padsBrowseRow
	cur    int
	top    int
	stack  []int // cursor of each folder above
	target int   // the pad a pick goes to
	err    string
}

var (
	padsLastSeen  atomic.Int64
	padsExporting atomic.Bool
	padsSaving    atomic.Bool
)

func init() {
	padsSt.samples = map[string]*padsSample{}
	padsSt.loading = map[string]bool{}
	padsSt.failed = map[string]string{}
	registerScene(padsScene{sceneBase{name: padsSceneName}})
}

// ── Project access ───────────────────────────────────────────────────────

func padsStorePath() string { return osAppsDataPath(padsStoreFile) }

// padsEnsureLoaded reads pads.json the first time the page opens; a missing
// or unreadable file opens the starter project.
func padsEnsureLoaded() {
	padsSt.mu.Lock()
	if padsSt.loaded {
		padsSt.mu.Unlock()
		return
	}
	padsSt.loaded = true
	p := padsDefaultProject()
	if data, err := os.ReadFile(padsStorePath()); err == nil {
		loaded := padsDefaultProject()
		// A cleared pattern or pad is saved empty and must not get the
		// starter's back.
		loaded.Patterns = [padsPatterns]padsPattern{}
		loaded.Song = nil
		if err := json.Unmarshal(data, &loaded); err != nil {
			logWarn("pads", "pads.json is unreadable, opening the starter project: %v", err)
		} else {
			p = loaded
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		logWarn("pads", "pads.json: %v", err)
	}
	p.sanitize()
	padsSt.proj = p
	padsSt.mu.Unlock()
	padsRequestAll()
}

// padsSnapshot is a copy of the project for one frame's drawing. Pattern hits
// and the song are replaced on edit, never changed in place, so sharing their
// backing arrays is safe.
func padsSnapshot() padsProject {
	padsSt.mu.Lock()
	defer padsSt.mu.Unlock()
	return padsSt.proj
}

// padsEdit changes the project under the lock and schedules a save.
func padsEdit(fn func(p *padsProject, e *padsEngine)) {
	padsSt.mu.Lock()
	fn(&padsSt.proj, padsSt.eng)
	padsSt.dirty = true
	padsSt.editAt = time.Now()
	padsSt.mu.Unlock()
}

// padsWithEngine runs fn on the live engine, if the device is open.
func padsWithEngine(fn func(p *padsProject, e *padsEngine)) {
	padsSt.mu.Lock()
	defer padsSt.mu.Unlock()
	if padsSt.eng != nil {
		fn(&padsSt.proj, padsSt.eng)
	}
}

// padsMaybeSave writes the project once edits have settled (or at once when
// forced), off the calling thread.
func padsMaybeSave(force bool) {
	padsSt.mu.Lock()
	if !padsSt.dirty || (!force && time.Since(padsSt.editAt) < padsSaveDelay) {
		padsSt.mu.Unlock()
		return
	}
	if !padsSaving.CompareAndSwap(false, true) {
		padsSt.mu.Unlock()
		if force {
			go func() {
				for i := 0; i < 100 && padsSaving.Load(); i++ {
					time.Sleep(20 * time.Millisecond)
				}
				padsMaybeSave(true)
			}()
		}
		return
	}
	p := padsSt.proj
	padsSt.dirty = false
	padsSt.mu.Unlock()
	go func() {
		defer padsSaving.Store(false)
		data, err := json.Marshal(&p)
		if err == nil {
			path := padsStorePath()
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			err = AtomicWrite(path, data, 0o644)
		}
		if err != nil {
			logWarn("pads", "saving pads.json: %v", err)
			padsSt.mu.Lock()
			padsSt.dirty = true
			padsSt.mu.Unlock()
		}
	}()
}

// ── Live view ────────────────────────────────────────────────────────────

// padsLiveView is what the page draws from the engine.
type padsLiveView struct {
	open     bool
	playing  bool
	step     int
	firedPat int
	nextPat  int
	songPos  int
	flash    [padsCount]float32
	fx       [padsFxCount]float64
	scope    [padsScopeLen]float32
	peak     float32
}

func padsLive() padsLiveView {
	var v padsLiveView
	padsSt.mu.Lock()
	defer padsSt.mu.Unlock()
	e := padsSt.eng
	if e == nil {
		return v
	}
	v.open = true
	v.playing = e.playing
	v.step = e.playingStep()
	v.firedPat = e.firedPat
	v.nextPat = e.pat
	v.songPos = e.songPos
	v.flash = e.flash
	v.fx = e.fx
	v.peak = e.peak
	for i := 0; i < padsScopeLen; i++ {
		v.scope[i] = e.scope[(e.scopePos+i)%padsScopeLen]
	}
	return v
}

// padsHoldsScreenAwake keeps the idle screen away while a beat plays.
func padsHoldsScreenAwake() bool {
	if time.Since(time.Unix(0, padsLastSeen.Load())) > padsIdleStop {
		return false
	}
	padsSt.mu.Lock()
	defer padsSt.mu.Unlock()
	return padsSt.eng != nil && padsSt.eng.playing
}

// padsAnimating keeps the frame rate up while the playhead moves, a pad
// flashes or a stick plays an effect (anim.go).
func padsAnimating() bool {
	if padsHoldsScreenAwake() {
		return true
	}
	if time.Since(time.Unix(0, padsLastSeen.Load())) > padsIdleStop {
		return false
	}
	return len(padsUI.held) > 0 || padsUI.stickFxOn != [padsDirCount]float64{}
}

// padsOpenRecently reports whether the page was drawn within the idle window
// (background music stays quiet meanwhile, bgmusic.go).
func padsOpenRecently() bool {
	return time.Since(time.Unix(0, padsLastSeen.Load())) < padsIdleStop
}

// ── Audio device ─────────────────────────────────────────────────────────

// padsTouch marks the page as drawn and opens the device when it is not.
func padsTouch() {
	padsLastSeen.Store(time.Now().UnixNano())
	padsSt.mu.Lock()
	start := !padsSt.running && (padsSt.failAt.IsZero() || time.Since(padsSt.failAt) > 5*time.Second)
	if start {
		padsSt.running = true
	}
	padsSt.mu.Unlock()
	if start {
		go padsAudioLoop()
	}
}

func padsLocked(fn func()) {
	padsSt.mu.Lock()
	defer padsSt.mu.Unlock()
	fn()
}

// padsAudioLoop owns the device: a few blocks queued ahead of the speaker
// until the page has not been drawn for padsIdleStop.
func padsAudioLoop() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("pads", "audio loop stopped: %v", r)
		}
		padsSt.mu.Lock()
		padsSt.eng = nil
		padsSt.running = false
		padsSt.mu.Unlock()
		padsMaybeSave(true)
	}()
	desired := sdl.AudioSpec{Freq: 48000, Format: sdl.AUDIO_S16LSB, Channels: 2, Samples: 512}
	var obtained sdl.AudioSpec
	dev, err := outOpen(&desired, &obtained, sdl.AUDIO_ALLOW_FREQUENCY_CHANGE)
	if err != nil || dev == 0 {
		logWarn("pads", "no audio device: %v", err)
		padsSt.mu.Lock()
		padsSt.failAt = time.Now()
		padsSt.mu.Unlock()
		return
	}
	defer outClose(dev)
	rate := int(obtained.Freq)
	if rate <= 0 {
		rate = int(desired.Freq)
	}
	padsSt.mu.Lock()
	eng := newPadsEngine(rate, padsSt.samples)
	eng.proj = &padsSt.proj
	padsSt.eng = eng
	padsSt.failAt = time.Time{}
	padsSt.mu.Unlock()

	fbuf := make([]float32, padsBlock*2)
	bbuf := make([]byte, padsBlock*4)
	// Two device buffers ahead (at least six blocks): ~40 ms from a press to
	// the speaker, with room for a slow frame.
	ahead := uint32(max(6*padsBlock, 2*int(obtained.Samples)) * 4)
	started := false
	for time.Since(time.Unix(0, padsLastSeen.Load())) < padsIdleStop {
		for outQueued(dev) < ahead {
			padsLocked(func() { eng.render(fbuf, padsBlock) })
			for i, v := range fbuf {
				s := int16(math.Max(-1, math.Min(1, float64(v))) * 32767)
				binary.LittleEndian.PutUint16(bbuf[2*i:], uint16(s))
			}
			if err := outQueue(dev, bbuf); err != nil {
				logWarn("pads", "queue: %v", err)
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
	padsLocked(eng.silence)
	outClear(dev)
}

// ── Actions ──────────────────────────────────────────────────────────────

// padsTogglePlay is Start: play from the top, or stop.
func padsTogglePlay() {
	padsWithEngine(func(p *padsProject, e *padsEngine) {
		if e.playing {
			e.stop()
			return
		}
		e.start()
	})
}

// padsArm asks for a second press before a destructive action.
func padsArm(id, prompt string) bool {
	if padsUI.armed == id && time.Since(padsUI.armedAt) < padsArmWindow {
		padsUI.armed = ""
		return true
	}
	padsUI.armed, padsUI.armedAt = id, time.Now()
	ShowToast(prompt, ToastKindWarning)
	return false
}

// padsPlayPad plays pad live. held is the press holding a hold or loop pad
// (id 0: a tap). With live recording on and record set (a pad played, not a
// sound auditioned), the hit lands in the pattern.
func padsPlayPad(pad int, held padsHeld, record bool) {
	if pad < 0 || pad >= padsCount {
		return
	}
	padsUI.flash[pad] = time.Now()
	padsUI.pad = pad
	var (
		path    string
		missing bool
		rec     bool
		holds   bool
	)
	padsWithEngine(func(p *padsProject, e *padsEngine) {
		d := &p.Pads[pad]
		path = d.Sample
		smp := e.samples[d.Sample]
		if smp == nil {
			missing = d.Sample != ""
			return
		}
		gate := 0
		if d.Mode != padsModeOneShot {
			gate = -1
			holds = held.id != 0
		}
		if record && p.RecLive && e.playing {
			pat, step := e.recordStep()
			pp := &p.Patterns[pat]
			if step < padsStepsValid(pp.Steps) {
				pp.setHit(padsHit{Pad: pad, Step: step}, true)
				rec = true
				// Landing on the next step, the sequencer plays it in a moment;
				// sounding it now as well would double it.
				if step != e.playingStep() || pat != e.firedPat {
					holds = false
					return
				}
			}
		}
		if gate == -1 && held.id == 0 {
			gate = int(e.baseStepSamples() * 2)
		}
		e.trigger(pad, d, smp, 0, 0.9, gate, held.id)
	})
	if rec {
		padsSt.mu.Lock()
		padsSt.dirty = true
		padsSt.editAt = time.Now()
		padsSt.mu.Unlock()
	}
	if holds {
		padsUI.held = append(padsUI.held, held)
	}
	if missing {
		if st := padsSampleState(path); st != "loading" && st != "" {
			ShowToast("This pad's sound could not be loaded: "+st, ToastKindWarning)
		}
		padsRequest(path, nil)
	}
}

// padsPollHeld lets go of hold and loop pads whose button or key is up.
func padsPollHeld() {
	if len(padsUI.held) == 0 {
		return
	}
	var keys []uint8
	keep := padsUI.held[:0]
	for _, h := range padsUI.held {
		down := false
		if h.btn >= 0 && gameController != nil {
			down = gameController.Button(sdl.GameControllerButton(h.btn)) != 0
		}
		if h.scan >= 0 {
			if keys == nil {
				keys = sdl.GetKeyboardState()
			}
			down = down || (h.scan < len(keys) && keys[h.scan] != 0)
		}
		if down {
			keep = append(keep, h)
			continue
		}
		id := h.id
		padsWithEngine(func(p *padsProject, e *padsEngine) { e.releaseLive(id) })
	}
	padsUI.held = keep
}

// padsAHeld reports whether A (or Enter) is still down, for painting a row
// on the Beat page.
func padsAHeld() bool {
	if gameController != nil && gameController.Button(sdl.CONTROLLER_BUTTON_A) != 0 {
		return true
	}
	keys := sdl.GetKeyboardState()
	for _, sc := range []sdl.Scancode{sdl.SCANCODE_RETURN, sdl.SCANCODE_KP_ENTER} {
		if int(sc) < len(keys) && keys[sc] != 0 {
			return true
		}
	}
	return false
}

// padsAudition plays the pad being edited.
func padsAudition(pad int) {
	padsPlayPad(pad, padsHeld{}, false)
}

// padsPreview plays a file from the browser.
func padsPreview(path string) {
	padsSt.mu.Lock()
	padsSt.preview = path
	padsSt.mu.Unlock()
	play := func(smp *padsSample, err error) {
		if err != nil {
			ShowToast("Could not open this sound: "+err.Error(), ToastKindWarning)
			return
		}
		padsWithEngine(func(p *padsProject, e *padsEngine) {
			if padsSt.preview != path {
				return
			}
			d := padsBlankPad()
			e.trigger(-1, &d, smp, 0, 0.9, 0, 0)
		})
	}
	padsRequest(path, play)
}

// padsAssign puts the sound at path on pad, with its trim reset, and loads it.
func padsAssign(pad int, path, label string) {
	if pad < 0 || pad >= padsCount {
		return
	}
	name := strings.TrimSpace(label)
	if len(name) > 40 {
		name = name[:40]
	}
	padsEdit(func(p *padsProject, e *padsEngine) {
		d := &p.Pads[pad]
		if d.Sample == "" {
			*d = padsBlankPad()
		}
		d.Sample, d.Name = path, name
		d.Start, d.End, d.Reverse = 0, 1, false
	})
	padsRequest(path, func(smp *padsSample, err error) {
		if err != nil {
			ShowToast("Could not load "+name+": "+err.Error(), ToastKindWarning)
			return
		}
		padsWithEngine(func(p *padsProject, e *padsEngine) {
			d := &p.Pads[pad]
			if d.Sample == path {
				e.trigger(pad, d, smp, 0, 0.9, int(e.baseStepSamples()*2), 0)
			}
		})
	})
}

// padsChop cuts pad's sound into the project's chop count, onto pad and the
// pads after it.
func padsChop(pad int) {
	n := padsChopCounts[padsClampInt(padsSnapshot().Chop, 0, len(padsChopCounts)-1)]
	wrote := 0
	padsEdit(func(p *padsProject, e *padsEngine) {
		var smp *padsSample
		if s := p.Pads[pad].Sample; s != "" {
			smp = padsSampleFor(s)
		}
		wrote = padsChopInto(p, pad, n, p.ChopMode == 1, smp)
	})
	if wrote == 0 {
		ShowToast("Put a sound on this pad first, then chop it", ToastKindInfo)
		return
	}
	ShowToast(fmt.Sprintf("Chopped into %d pads, %s to %s", wrote, padsPadLabel(pad), padsPadLabel(pad+wrote-1)), ToastKindSuccess)
}

// padsRandomBeat writes a groove into the current pattern on the bank's
// first four pads that have a sound: a kick, a snare, hats and a fill.
func padsRandomBeat(p *padsProject, bank int) {
	var roles []int
	for i := bank * padsPerBank; i < (bank+1)*padsPerBank && len(roles) < 4; i++ {
		if p.Pads[i].Sample != "" {
			roles = append(roles, i)
		}
	}
	if len(roles) == 0 {
		return
	}
	pat := &p.Patterns[p.Current]
	steps := padsStepsValid(pat.Steps)
	var hits []padsHit
	add := func(role, s int, acc bool) {
		if role < len(roles) {
			hits = append(hits, padsHit{Pad: roles[role], Step: s, Accent: acc})
		}
	}
	for s := 0; s < steps; s++ {
		switch {
		case s%16 == 0:
			add(0, s, true)
		case s%4 == 0 && rand.IntN(3) == 0, s%2 == 1 && rand.IntN(10) == 0:
			add(0, s, false)
		}
		if s%8 == 4 {
			add(1, s, s%16 == 12)
		} else if rand.IntN(16) == 0 {
			add(1, s, false)
		}
		if s%2 == 0 || rand.IntN(4) == 0 {
			add(2, s, s%4 == 2)
		}
		if s%16 >= 12 && rand.IntN(3) == 0 {
			add(3, s, false)
		}
	}
	pat.Hits = padsCleanHits(hits, steps)
}

// padsExportWAV renders the song in the background.
func padsExportWAV() {
	if !padsExporting.CompareAndSwap(false, true) {
		ShowToast("Already exporting", ToastKindInfo)
		return
	}
	padsSt.mu.Lock()
	proj := padsSt.proj
	samples := make(map[string]*padsSample, len(padsSt.samples))
	for k, v := range padsSt.samples {
		samples[k] = v
	}
	waiting := len(padsSt.loading)
	padsSt.mu.Unlock()
	if waiting > 0 {
		ShowToast("Some sounds are still loading; they will be silent in this export", ToastKindWarning)
	}
	ShowToast("Exporting to WAV…", ToastKindInfo)
	go func() {
		defer padsExporting.Store(false)
		path, secs, err := padsRenderWAV(proj, samples)
		if err != nil {
			logWarn("pads", "export: %v", err)
			ShowToast("Could not export: "+err.Error(), ToastKindWarning)
			return
		}
		ShowToast(fmt.Sprintf("Saved %s (%d s) to Downloads/music", filepath.Base(path), secs), ToastKindSuccess)
	}()
}

// padsTapTempo sets the tempo from the last few A presses on the Tempo row.
func padsTapTempo() {
	now := time.Now()
	if n := len(padsUI.taps); n > 0 && now.Sub(padsUI.taps[n-1]) > 2*time.Second {
		padsUI.taps = padsUI.taps[:0]
	}
	padsUI.taps = append(padsUI.taps, now)
	if len(padsUI.taps) > 5 {
		padsUI.taps = padsUI.taps[len(padsUI.taps)-5:]
	}
	n := len(padsUI.taps)
	if n < 2 {
		ShowToast("Tap A in time to set the tempo", ToastKindInfo)
		return
	}
	avg := padsUI.taps[n-1].Sub(padsUI.taps[0]).Seconds() / float64(n-1)
	if avg <= 0 {
		return
	}
	bpm := padsClampInt(int(math.Round(60/avg)), padsMinBPM, padsMaxBPM)
	padsEdit(func(p *padsProject, e *padsEngine) { p.BPM = bpm })
}

// padsSetPattern moves the edited pattern; a looping pattern follows at its
// end.
func padsSetPattern(dir int) {
	padsEdit(func(p *padsProject, e *padsEngine) {
		p.Current = (p.Current + dir + padsPatterns) % padsPatterns
	})
}

// padsSetBank shows bank b (and keeps the cursors on the same place in it).
func padsSetBank(b int) {
	padsUI.bank = (b + padsBanks) % padsBanks
	padsUI.pad = padsUI.bank*padsPerBank + padsUI.cur
}

// ── Stick effects ────────────────────────────────────────────────────────

// padsStickLeftIsFx reports whether the left stick plays effects on the page
// on show (Play and FX); elsewhere it walks like the D-pad.
func padsStickLeftIsFx() bool {
	return !padsUI.br.open && (padsUI.tab == padsTabPlay || padsUI.tab == padsTabFX)
}

// padsOwnsPad reports whether the Pads page has the pad to itself: nothing
// floats over it.
func padsOwnsPad(config *Config) bool {
	if !padsSceneActive(config) {
		return false
	}
	switch {
	case virtualKeyboardActive, tb.startOpen, nc.open, globalSearchOpen, shortcutsOpen, clipSearchOpen, wpOpen,
		weatherPopoverOpen, alarmFiringActive():
		return false
	}
	return !inmapOverlayOpen() && !triggerOwnedByOverlay() && !modalPanelBlocksShoulders()
}

// padsStickAxis takes the sticks while they play effects, so they do not
// also step the focus (controller.go).
func padsStickAxis(config *Config, axis uint8) bool {
	if !padsOwnsPad(config) {
		return false
	}
	switch axis {
	case sdl.CONTROLLER_AXIS_RIGHTX, sdl.CONTROLLER_AXIS_RIGHTY:
		return true
	case sdl.CONTROLLER_AXIS_LEFTX, sdl.CONTROLLER_AXIS_LEFTY:
		return padsStickLeftIsFx()
	}
	return false
}

// padsUpdateStickFX reads the sticks and hands the engine each effect's
// amount: the furthest any of its directions leans, or its latched amount.
func padsUpdateStickFX(config *Config, p *padsProject) {
	var dirs [padsDirCount]float64
	if padsOwnsPad(config) {
		shape := func(v float64) float64 { return padsClamp01((v - 0.12) / 0.85) }
		if padsStickLeftIsFx() {
			lx, ly := stickShaped(config, stickLeft)
			dirs[padsDirLUp], dirs[padsDirLDown] = shape(-ly), shape(ly)
			dirs[padsDirLLeft], dirs[padsDirLRight] = shape(-lx), shape(lx)
		}
		rx, ry := stickShaped(config, stickRight)
		dirs[padsDirRUp], dirs[padsDirRDown] = shape(-ry), shape(ry)
		dirs[padsDirRLeft], dirs[padsDirRRight] = shape(-rx), shape(rx)
	}
	padsUI.stickFxOn = dirs
	var want [padsFxCount]float64
	for d, amt := range dirs {
		if fx := p.FX[d]; fx > padsFxOff && fx < padsFxCount && amt > want[fx] {
			want[fx] = amt
		}
	}
	for fx := padsFxOff + 1; fx < padsFxCount; fx++ {
		want[fx] = math.Max(want[fx], padsClamp01(p.Latch[fx]))
	}
	padsWithEngine(func(_ *padsProject, e *padsEngine) { e.fxWant = want })
}

// ── Rows of the Pad, Song, FX and Mix pages ──────────────────────────────

const (
	padsRowSlider = iota
	padsRowChoice
	padsRowAction
)

// padsRow is one adjustable line. A slider's get/set work in 0..1; a choice
// steps with step; an action runs act on A. live is an extra meter (FX).
type padsRow struct {
	label string
	kind  int
	get   func(p *padsProject) float64
	set   func(p *padsProject, v float64)
	step  func(p *padsProject, dir int)
	text  func(p *padsProject) string
	act   func()
	live  func() float64
	a     func() // A on a slider or choice (audition, tap tempo, latch)
}

func padsSlider(label string, get func(p *padsProject) float64, set func(p *padsProject, v float64), text func(p *padsProject) string) padsRow {
	return padsRow{label: label, kind: padsRowSlider, get: get, set: set, text: text}
}

func padsChoice(label string, step func(p *padsProject, dir int), text func(p *padsProject) string) padsRow {
	return padsRow{label: label, kind: padsRowChoice, step: step, text: text}
}

func padsAction(label string, act func()) padsRow {
	return padsRow{label: label, kind: padsRowAction, act: act}
}

func padsPct(v float64) string { return strconv.Itoa(int(math.Round(v*100))) + "%" }

// padsPadSlider is a 0..1 setting of pad i.
func padsPadSlider(i int, label string, field func(d *padsPad) *float64, text func(v float64) string) padsRow {
	if text == nil {
		text = padsPct
	}
	r := padsSlider(label,
		func(p *padsProject) float64 { return *field(&p.Pads[i]) },
		func(p *padsProject, v float64) { *field(&p.Pads[i]) = padsClamp01(v) },
		func(p *padsProject) string { return text(*field(&p.Pads[i])) })
	r.a = func() { padsAudition(i) }
	return r
}

// padsSecsOf is a trim point as time into pad i's sound.
func padsSecsOf(i int, frac float64) string {
	padsSt.mu.Lock()
	smp := padsSampleFor(padsSt.proj.Pads[i].Sample)
	padsSt.mu.Unlock()
	if smp == nil || smp.rate <= 0 {
		return padsPct(frac)
	}
	s := frac * float64(len(smp.data)) / float64(smp.rate)
	return strconv.FormatFloat(s, 'f', 2, 64) + " s"
}

// padsPadRows are the Pad page's rows for pad i.
func padsPadRows(i int) []padsRow {
	i = padsClampInt(i, 0, padsCount-1)
	aud := func() { padsAudition(i) }
	choice := func(label string, step func(d *padsPad, dir int), text func(d *padsPad) string) padsRow {
		r := padsChoice(label,
			func(p *padsProject, dir int) { step(&p.Pads[i], dir) },
			func(p *padsProject) string { return text(&p.Pads[i]) })
		r.a = aud
		return r
	}
	sound := padsAction("Sound", func() { padsOpenBrowser(i) })
	sound.text = func(p *padsProject) string {
		d := &p.Pads[i]
		if d.Sample == "" {
			return "Empty · A picks one"
		}
		name := d.Name
		if name == "" {
			name = filepath.Base(d.Sample)
		}
		return name
	}
	rows := []padsRow{
		sound,
		padsPadSlider(i, "Volume", func(d *padsPad) *float64 { return &d.Volume }, nil),
		choice("Pitch", func(d *padsPad, dir int) { d.Pitch = padsClampInt(d.Pitch+dir, -padsMaxNote, padsMaxNote) },
			func(d *padsPad) string { return padsSemis(d.Pitch) }),
		choice("Fine tune", func(d *padsPad, dir int) { d.Fine = padsClampInt(d.Fine+5*dir, -50, 50) },
			func(d *padsPad) string { return fmt.Sprintf("%+d cents", d.Fine) }),
		padsPadSlider(i, "Filter", func(d *padsPad) *float64 { return &d.Filter }, padsFilterText),
		padsPadSlider(i, "Start", func(d *padsPad) *float64 { return &d.Start }, func(v float64) string { return padsSecsOf(i, v) }),
		padsPadSlider(i, "End", func(d *padsPad) *float64 { return &d.End }, func(v float64) string { return padsSecsOf(i, v) }),
		padsPadSlider(i, "Fade in", func(d *padsPad) *float64 { return &d.FadeIn }, nil),
		padsPadSlider(i, "Fade out", func(d *padsPad) *float64 { return &d.FadeOut }, nil),
		choice("Reverse", func(d *padsPad, dir int) { d.Reverse = !d.Reverse }, func(d *padsPad) string { return padsOnOff(d.Reverse) }),
		choice("Play mode", func(d *padsPad, dir int) { d.Mode = (d.Mode + dir + padsModeCount) % padsModeCount },
			func(d *padsPad) string { return padsModeNames[padsClampInt(d.Mode, 0, padsModeCount-1)] }),
		choice("Choke group", func(d *padsPad, dir int) { d.Choke = (d.Choke + dir + padsChokes + 1) % (padsChokes + 1) },
			func(d *padsPad) string {
				if d.Choke == 0 {
					return "Off"
				}
				return "Group " + strconv.Itoa(d.Choke)
			}),
		padsPadSlider(i, "Pan", func(d *padsPad) *float64 { return &d.Pan }, padsPanText),
		padsPadSlider(i, "Echo send", func(d *padsPad) *float64 { return &d.Delay }, nil),
		padsPadSlider(i, "Reverb send", func(d *padsPad) *float64 { return &d.Reverb }, nil),
		padsPadSlider(i, "Lo-fi", func(d *padsPad) *float64 { return &d.LoFi }, func(v float64) string {
			if v < 0.01 {
				return "Off"
			}
			return padsPct(v)
		}),
		choice("Mute", func(d *padsPad, dir int) { d.Mute = !d.Mute }, func(d *padsPad) string { return padsOnOff(d.Mute) }),
		padsChoice("Chop into", func(p *padsProject, dir int) {
			p.Chop = (p.Chop + dir + len(padsChopCounts)) % len(padsChopCounts)
		}, func(p *padsProject) string {
			return strconv.Itoa(padsChopCounts[padsClampInt(p.Chop, 0, len(padsChopCounts)-1)]) + " pads"
		}),
		padsChoice("Chop at", func(p *padsProject, dir int) { p.ChopMode = 1 - padsClampInt(p.ChopMode, 0, 1) },
			func(p *padsProject) string {
				if p.ChopMode == 1 {
					return "The hits it hears"
				}
				return "Equal slices"
			}),
		padsAction("Chop now", func() { padsChop(i) }),
		padsAction("Copy pad", func() {
			padsUI.clip, padsUI.hasClip = padsSnapshot().Pads[i], true
			ShowToast("Copied "+padsPadLabel(i), ToastKindInfo)
		}),
		padsAction("Paste pad", func() {
			if !padsUI.hasClip {
				ShowToast("Copy a pad first", ToastKindInfo)
				return
			}
			clip := padsUI.clip
			padsEdit(func(p *padsProject, e *padsEngine) { p.Pads[i] = clip })
			padsRequest(clip.Sample, nil)
		}),
		padsAction("Clear pad", func() {
			if !padsArm("clear-pad", hintSentence("Press %s again to clear ", "A", "Clear")+padsPadLabel(i)) {
				return
			}
			padsEdit(func(p *padsProject, e *padsEngine) {
				p.Pads[i] = padsBlankPad()
				for k := range p.Patterns {
					pat := &p.Patterns[k]
					hits := make([]padsHit, 0, len(pat.Hits))
					for _, h := range pat.Hits {
						if h.Pad != i {
							hits = append(hits, h)
						}
					}
					pat.Hits = hits
				}
			})
		}),
	}
	return rows
}

func padsOnOff(b bool) string {
	if b {
		return "On"
	}
	return "Off"
}

func padsSemis(n int) string {
	if n == 0 {
		return "0 st"
	}
	return fmt.Sprintf("%+d st", n)
}

func padsFilterText(v float64) string {
	switch {
	case v < 0.48:
		return "Low-pass " + padsPct(1-v/0.48)
	case v > 0.52:
		return "High-pass " + padsPct((v-0.52)/0.48)
	}
	return "Open"
}

func padsPanText(v float64) string {
	switch {
	case v < 0.48:
		return "L " + padsPct((0.5-v)*2)
	case v > 0.52:
		return "R " + padsPct((v-0.5)*2)
	}
	return "Centre"
}

// padsSongSettingRows are the Song page's left column.
func padsSongSettingRows() []padsRow {
	return []padsRow{
		padsChoice("Start plays", func(p *padsProject, dir int) { p.SongMode = !p.SongMode }, func(p *padsProject) string {
			if p.SongMode {
				return "The song"
			}
			return "The pattern, looped"
		}),
		padsChoice("Pattern", func(p *padsProject, dir int) {
			p.Current = (p.Current + dir + padsPatterns) % padsPatterns
		}, func(p *padsProject) string { return strconv.Itoa(p.Current + 1) }),
		padsChoice("Pattern length", func(p *padsProject, dir int) {
			pat := &p.Patterns[p.Current]
			pat.Steps = padsStepsValid(pat.Steps + 16*dir)
			pat.Hits = padsCleanHits(pat.Hits, pat.Steps)
		}, func(p *padsProject) string {
			n := padsStepsValid(p.Patterns[p.Current].Steps)
			return fmt.Sprintf("%d steps (%d bars)", n, n/16)
		}),
		padsAction("Copy to the next empty pattern", func() {
			to := -1
			padsEdit(func(p *padsProject, e *padsEngine) {
				for k := 1; k < padsPatterns; k++ {
					j := (p.Current + k) % padsPatterns
					if p.Patterns[j].empty() {
						p.Patterns[j] = padsPattern{Steps: p.Patterns[p.Current].Steps, Hits: append([]padsHit(nil), p.Patterns[p.Current].Hits...)}
						to = j
						return
					}
				}
			})
			if to < 0 {
				ShowToast("Every pattern has hits; clear one first", ToastKindInfo)
				return
			}
			ShowToast(fmt.Sprintf("Copied to pattern %d", to+1), ToastKindSuccess)
		}),
		padsAction("Clear pattern", func() {
			if !padsArm("clear-pattern", hintSentence("Press %s again to clear this pattern", "A", "Clear")) {
				return
			}
			padsEdit(func(p *padsProject, e *padsEngine) { p.Patterns[p.Current].Hits = nil })
		}),
		padsAction("Random beat", func() {
			if !padsArm("random", hintSentence("Press %s again to replace this pattern with a random beat", "A", "Replace")) {
				return
			}
			bank := padsUI.bank
			padsEdit(func(p *padsProject, e *padsEngine) { padsRandomBeat(p, bank) })
		}),
		padsAction("Add pattern to the song", func() {
			full := false
			padsEdit(func(p *padsProject, e *padsEngine) {
				if len(p.Song) >= padsSongMax {
					full = true
					return
				}
				p.Song = append(append([]int(nil), p.Song...), p.Current)
			})
			if full {
				ShowToast(fmt.Sprintf("A song holds up to %d slots", padsSongMax), ToastKindInfo)
			}
		}),
		padsAction("Clear song", func() {
			if !padsArm("clear-song", hintSentence("Press %s again to clear the song", "A", "Clear")) {
				return
			}
			padsEdit(func(p *padsProject, e *padsEngine) { p.Song = nil })
		}),
	}
}

// padsFxRows are the FX page: the eight directions, then the latches.
func padsFxRows(live *padsLiveView) []padsRow {
	rows := make([]padsRow, 0, padsDirCount+padsFxCount)
	for d := 0; d < padsDirCount; d++ {
		r := padsChoice(padsDirNames[d], func(p *padsProject, dir int) {
			p.FX[d] = (p.FX[d] + dir + padsFxCount) % padsFxCount
		}, func(p *padsProject) string { return padsFxNames[padsClampInt(p.FX[d], 0, padsFxCount-1)] })
		r.live = func() float64 { return padsUI.stickFxOn[d] }
		rows = append(rows, r)
	}
	for fx := padsFxOff + 1; fx < padsFxCount; fx++ {
		r := padsSlider("Hold "+strings.ToLower(padsFxNames[fx][:1])+padsFxNames[fx][1:],
			func(p *padsProject) float64 { return p.Latch[fx] },
			func(p *padsProject, v float64) { p.Latch[fx] = padsClamp01(v) },
			func(p *padsProject) string {
				if p.Latch[fx] < 0.01 {
					return "Off"
				}
				return padsPct(p.Latch[fx])
			})
		fxv := live.fx[fx]
		r.live = func() float64 { return fxv }
		r.a = func() {
			padsEdit(func(p *padsProject, e *padsEngine) {
				if p.Latch[fx] > 0 {
					p.Latch[fx] = 0
				} else {
					p.Latch[fx] = 1
				}
			})
		}
		rows = append(rows, r)
	}
	return rows
}

// padsMixRows are the Mix page.
func padsMixRows() []padsRow {
	tempo := padsChoice("Tempo", func(p *padsProject, dir int) {
		p.BPM = padsClampInt(p.BPM+dir, padsMinBPM, padsMaxBPM)
	}, func(p *padsProject) string { return strconv.Itoa(p.BPM) + " BPM" })
	tempo.a = padsTapTempo
	return []padsRow{
		tempo,
		padsSlider("Swing", func(p *padsProject) float64 { return p.Swing / 0.6 },
			func(p *padsProject, v float64) { p.Swing = padsClamp01(v) * 0.6 },
			func(p *padsProject) string { return padsPct(p.Swing / 0.6) }),
		padsSlider("Master", func(p *padsProject) float64 { return p.Master },
			func(p *padsProject, v float64) { p.Master = padsClamp01(v) },
			func(p *padsProject) string { return padsPct(p.Master) }),
		padsSlider("Compressor", func(p *padsProject) float64 { return p.Comp },
			func(p *padsProject, v float64) { p.Comp = padsClamp01(v) },
			func(p *padsProject) string {
				if p.Comp < 0.01 {
					return "Off"
				}
				return padsPct(p.Comp)
			}),
		padsChoice("Echo time", func(p *padsProject, dir int) {
			p.DelayTime = padsClampInt(p.DelayTime+dir, 0, len(synthDelayTimes)-1)
		}, func(p *padsProject) string {
			return synthDelayTimes[padsClampInt(p.DelayTime, 0, len(synthDelayTimes)-1)].name
		}),
		padsSlider("Echo feedback", func(p *padsProject) float64 { return p.DelayFB / 0.9 },
			func(p *padsProject, v float64) { p.DelayFB = padsClamp01(v) * 0.9 },
			func(p *padsProject) string { return padsPct(p.DelayFB / 0.9) }),
		padsSlider("Reverb size", func(p *padsProject) float64 { return p.RevSize },
			func(p *padsProject, v float64) { p.RevSize = padsClamp01(v) },
			func(p *padsProject) string { return padsPct(p.RevSize) }),
		padsChoice("Record live hits", func(p *padsProject, dir int) { p.RecLive = !p.RecLive },
			func(p *padsProject) string {
				if p.RecLive {
					return "On while playing"
				}
				return "Off"
			}),
		padsAction("Export to WAV", padsExportWAV),
		padsAction("Starter kit on this bank", func() {
			bank := padsUI.bank
			if !padsArm("kit", hintSentence("Press %s again to put the starter kit on bank ", "A", "Load")+padsBankName(bank)) {
				return
			}
			padsEdit(func(p *padsProject, e *padsEngine) {
				for k, id := range padsKitIDs {
					p.Pads[bank*padsPerBank+k] = padsKitPad(id)
				}
			})
			padsRequestAll()
		}),
		padsAction("Clear this bank", func() {
			bank := padsUI.bank
			if !padsArm("clear-bank", hintSentence("Press %s again to clear bank ", "A", "Clear")+padsBankName(bank)) {
				return
			}
			padsEdit(func(p *padsProject, e *padsEngine) {
				for k := 0; k < padsPerBank; k++ {
					p.Pads[bank*padsPerBank+k] = padsBlankPad()
				}
			})
		}),
		padsAction("New project", func() {
			if !padsArm("new", hintSentence("Press %s again to start over with the starter project", "A", "Start over")) {
				return
			}
			padsEdit(func(p *padsProject, e *padsEngine) {
				*p = padsDefaultProject()
				if e != nil {
					e.silence()
				}
			})
			padsRequestAll()
		}),
	}
}

// padsAdjustRow moves row r by dir; big is X / Y's coarse step.
func padsAdjustRow(r padsRow, dir int, big bool) {
	switch r.kind {
	case padsRowSlider:
		d := 0.02
		if big {
			d = 0.1
		}
		padsEdit(func(p *padsProject, e *padsEngine) { r.set(p, r.get(p)+d*float64(dir)) })
	case padsRowChoice:
		if big {
			switch r.label {
			case "Tempo":
				dir *= 10
			case "Pitch":
				dir *= 12 // an octave
			}
		}
		padsEdit(func(p *padsProject, e *padsEngine) { r.step(p, dir) })
	}
}

// padsDoRows drives a list of rows: Up / Down pick, Left / Right adjust, X / Y
// adjust in big steps, A runs the row.
func padsDoRows(in int, rows []padsRow, cur *int) {
	if len(rows) == 0 {
		return
	}
	*cur = padsClampInt(*cur, 0, len(rows)-1)
	r := rows[*cur]
	switch in {
	case padsInUp:
		*cur = (*cur + len(rows) - 1) % len(rows)
	case padsInDown:
		*cur = (*cur + 1) % len(rows)
	case padsInLeft:
		padsAdjustRow(r, -1, false)
	case padsInRight:
		padsAdjustRow(r, 1, false)
	case padsInX:
		padsAdjustRow(r, -1, true)
	case padsInY:
		padsAdjustRow(r, 1, true)
	case padsInA:
		switch {
		case r.act != nil:
			r.act()
		case r.a != nil:
			r.a()
		case r.kind == padsRowChoice:
			padsAdjustRow(r, 1, false)
		}
	}
}
