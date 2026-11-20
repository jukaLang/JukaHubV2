package main

// narrate_voice.go — narration's sound: the device's voice, made one short
// piece at a time and kept.
//
// A line is said in pieces ("Vibration" · "on" · "Switch with A."), each made
// and kept on its own, so the pieces that come back on every screen — "on",
// "off", "Open with A.", the section names — are made once and then play at
// once. Pieces are kept twice:
//
//   - in memory, at the narration speed, within a budget
//     (narrMemBudget, cachebudget.go's byteOrderedStore);
//   - on the card, as the voice made them, in narrate-cache/ beside Juka's
//     own voice-cache/ (narrDiskMaxFiles, narrDiskMaxBytes; the least recently
//     heard go first). Juka's 160-line cache is not used: narration's names
//     would push Juka's everyday answers out of it.
//
// The voice is made by one worker goroutine, one piece at a time, never cut
// off part-way: stopping Piper mid-line ends the process (it would owe a line),
// and the next focus would wait for the model to load again. A piece nobody
// waits for any more is still finished and kept; pieces asked for later are
// made newest first. On the handheld the worker borrows Juka's loaded Piper
// the way the voice cache's seeding does (narrate_other.go), so an answer of
// Juka's that comes meanwhile waits a moment for it instead of loading a
// second Piper; on a PC one Windows PowerShell stays loaded with the speech
// synthesiser (narrate_windows.go).
//
// Speed: the voice speaks at its own pace and the pieces are time-stretched
// (narrStretch, WSOLA: overlapping windows matched by correlation, so the
// pitch stays) — one rate for every engine, and the card keeps one copy of
// each piece whatever the speed.
//
// While the user rests, the worker makes what is likely next: the focused
// control's neighbours, then the common words (narrAheadQueue), one piece at a
// time, never while Juka speaks or the voice orb is up.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// narrDiskMaxFiles and narrDiskMaxBytes bound narrate-cache/.
	narrDiskMaxFiles       = 900
	narrDiskMaxBytes int64 = 16 << 20
	// narrDiskPruneEvery is how many new files go in between two prunes.
	narrDiskPruneEvery = 40
	// narrPieceMaxChars is the longest piece kept on the card.
	narrPieceMaxChars = 80
	// narrSynthTimeout bounds one piece (a cold Piper loads its model first).
	narrSynthTimeout = 40 * time.Second
	// narrAheadMax bounds the pieces waiting to be made ahead.
	narrAheadMax = 64
)

// errNarrNoVoice: this device has no voice narration can use yet.
var errNarrNoVoice = errors.New("no voice for narration")

// errNarrGameUp: a game is running; nothing is made until it ends.
var errNarrGameUp = errors.New("a game is running")

// errNarrVoiceBusy: a line of Juka's held the voice for the whole wait, or the
// piece was dropped from a full queue. Not worth a message.
var errNarrVoiceBusy = errors.New("the voice is busy")

// errNarrNoRoom: too little memory is free to load the voice for narration.
var errNarrNoRoom = errors.New("too little free memory to load the narration voice")

// narrMemClip is one piece at the narration speed, at the voice's rate.
type narrMemClip struct {
	samples []int16
	rate    int
}

// narrMem is the in-memory store. Any goroutine, under mu.
var narrMem struct {
	mu    sync.Mutex
	store *byteOrderedStore[string, narrMemClip]
}

// narrMemBudget is the in-memory store's bound: about two minutes of speech
// at the handheld's 16 kHz.
func narrMemBudget() int64 { return cacheBudgetByDevice(4<<20, 16<<20) }

func narrMemGet(key string) (narrMemClip, bool) {
	narrMem.mu.Lock()
	defer narrMem.mu.Unlock()
	if narrMem.store == nil {
		return narrMemClip{}, false
	}
	return narrMem.store.get(key)
}

func narrMemPut(key string, c narrMemClip) {
	narrMem.mu.Lock()
	defer narrMem.mu.Unlock()
	if narrMem.store == nil {
		narrMem.store = newByteOrderedStore[string, narrMemClip](1200, narrMemBudget(),
			func(c narrMemClip) int64 { return int64(len(c.samples))*2 + 64 }, nil)
	}
	narrMem.store.put(key, c)
}

// narrMemClear empties the store (the rate changed: every piece is stale).
func narrMemClear() {
	narrMem.mu.Lock()
	defer narrMem.mu.Unlock()
	if narrMem.store != nil {
		narrMem.store.clear()
	}
}

// narrSpeakableText is a piece as a voice should read it.
func narrSpeakableText(s string) string {
	s = strings.NewReplacer("·", ",", "—", ",", "–", ",", "…", "", "•", ",").Replace(s)
	return strings.TrimSpace(ttsSpeakable(s))
}

// narrVoiceIDMemo remembers the voice's cache name for a minute: it stats the
// model file.
var narrVoiceIDMemo struct {
	mu   sync.Mutex
	key  string
	id   string
	when time.Time
}

// narrVoiceID is the device voice's name for the caches.
func narrVoiceID(v ttsLocalVoice, gender string) string {
	key := v.kind + "\x00" + v.path + "\x00" + v.model + "\x00" + gender
	narrVoiceIDMemo.mu.Lock()
	defer narrVoiceIDMemo.mu.Unlock()
	if narrVoiceIDMemo.key == key && time.Since(narrVoiceIDMemo.when) < time.Minute {
		return narrVoiceIDMemo.id
	}
	id := ttsCacheLocalID(v, gender)
	narrVoiceIDMemo.key, narrVoiceIDMemo.id, narrVoiceIDMemo.when = key, id, time.Now()
	return id
}

// narrGenderName is the chosen Windows voice, as tts_local_windows.go reads it.
func narrGenderName() string {
	switch narrGender.Load() {
	case 1:
		return "female"
	case 2:
		return "male"
	}
	return ""
}

// narrClip is text's sound at the narration speed: from memory, from the
// card, or made by the worker. keep puts a short piece on the card.
func narrClip(ctx context.Context, text string, keep bool) ([]int16, int, error) {
	text = narrSpeakableText(text)
	if text == "" {
		return nil, 0, nil
	}
	v, ok := ttsLocal()
	if !ok {
		return nil, 0, errNarrNoVoice
	}
	gender := narrGenderName()
	id := narrVoiceID(v, gender)
	speed := int(narrRate.Load())
	mkey := id + "\x00" + strconv.Itoa(speed) + "\x00" + text
	if c, ok := narrMemGet(mkey); ok {
		return c.samples, c.rate, nil
	}
	keep = keep && len(text) <= narrPieceMaxChars
	if keep {
		if raw, rate, ok := narrDiskLoad(id, text); ok {
			c := narrMemClip{samples: narrStretch(raw, rate, speed), rate: rate}
			narrMemPut(mkey, c)
			return c.samples, c.rate, nil
		}
	}
	ch := narrSynthAsk(narrSynthJob{text: text, keep: keep, voice: v, gender: gender, id: id, speed: speed, mkey: mkey}, true)
	select {
	case r := <-ch:
		return r.samples, r.rate, r.err
	case <-ctx.Done():
		narrSynthForget(mkey, ch)
		return nil, 0, ctx.Err()
	}
}

// ── The worker ───────────────────────────────────────────────────────────

// narrSynthJob is one piece to make.
type narrSynthJob struct {
	text   string
	keep   bool
	voice  ttsLocalVoice
	gender string
	id     string
	speed  int
	mkey   string
	ahead  bool // made ahead: nobody waits
	asked  time.Time
}

// narrSynthResult is a made piece, or why it was not.
type narrSynthResult struct {
	samples []int16
	rate    int
	err     error
}

// narrSynthQ is the worker's queue. Any goroutine, under mu.
var narrSynthQ struct {
	mu      sync.Mutex
	jobs    []narrSynthJob
	waiters map[string][]chan narrSynthResult
	busy    bool // the worker is making a piece
	running bool
	wake    chan struct{}
}

// narrSynthAsk queues job (once per piece) and returns where its answer
// arrives (nil when wait is false).
func narrSynthAsk(job narrSynthJob, wait bool) chan narrSynthResult {
	job.asked = time.Now()
	narrSynthQ.mu.Lock()
	if narrSynthQ.waiters == nil {
		narrSynthQ.waiters = map[string][]chan narrSynthResult{}
		narrSynthQ.wake = make(chan struct{}, 1)
	}
	var ch chan narrSynthResult
	if wait {
		ch = make(chan narrSynthResult, 1)
		narrSynthQ.waiters[job.mkey] = append(narrSynthQ.waiters[job.mkey], ch)
	}
	queued := false
	for i := range narrSynthQ.jobs {
		if narrSynthQ.jobs[i].mkey == job.mkey {
			queued = true
			if !job.ahead {
				// Asked for now: it goes to the end, which is made first.
				j := narrSynthQ.jobs[i]
				j.ahead, j.asked = false, job.asked
				narrSynthQ.jobs = append(append(narrSynthQ.jobs[:i:i], narrSynthQ.jobs[i+1:]...), j)
			}
			break
		}
	}
	if !queued {
		narrSynthQ.jobs = append(narrSynthQ.jobs, job)
		if n := len(narrSynthQ.jobs) - narrAheadMax; n > 0 {
			// The oldest go; whoever waits for one hears so at once.
			for _, old := range narrSynthQ.jobs[:n] {
				for _, w := range narrSynthQ.waiters[old.mkey] {
					w <- narrSynthResult{err: errNarrVoiceBusy}
				}
				delete(narrSynthQ.waiters, old.mkey)
			}
			narrSynthQ.jobs = narrSynthQ.jobs[n:]
		}
	}
	if !narrSynthQ.running {
		narrSynthQ.running = true
		go narrSynthWorker()
	}
	wake := narrSynthQ.wake
	narrSynthQ.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
	return ch
}

// narrSynthForget takes back a wait nobody needs any more (its line was cut
// off): the piece is then made only when nothing else is asked for.
func narrSynthForget(mkey string, ch chan narrSynthResult) {
	narrSynthQ.mu.Lock()
	defer narrSynthQ.mu.Unlock()
	ws := narrSynthQ.waiters[mkey]
	for i, w := range ws {
		if w == ch {
			ws = append(ws[:i:i], ws[i+1:]...)
			break
		}
	}
	if len(ws) == 0 {
		delete(narrSynthQ.waiters, mkey)
	} else {
		narrSynthQ.waiters[mkey] = ws
	}
}

// narrSynthDropUnwaitedLocked forgets the pieces nobody waits for (made
// ahead, or of a line already cut off). narrSynthQ.mu is held.
func narrSynthDropUnwaitedLocked() {
	kept := narrSynthQ.jobs[:0]
	for _, j := range narrSynthQ.jobs {
		if len(narrSynthQ.waiters[j.mkey]) > 0 {
			kept = append(kept, j)
		}
	}
	narrSynthQ.jobs = kept
}

// narrSynthIdle reports whether the worker has nothing to do.
func narrSynthIdle() bool {
	narrSynthQ.mu.Lock()
	defer narrSynthQ.mu.Unlock()
	return !narrSynthQ.busy && len(narrSynthQ.jobs) == 0
}

// narrSynthWorker makes the queued pieces, newest first.
func narrSynthWorker() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("narrate", "the narration voice stopped: %v", r)
			narrSynthQ.mu.Lock()
			narrSynthQ.running = false
			narrSynthQ.busy = false
			narrSynthQ.mu.Unlock()
		}
	}()
	for {
		narrSynthQ.mu.Lock()
		if len(narrSynthQ.jobs) == 0 {
			wake := narrSynthQ.wake
			narrSynthQ.mu.Unlock()
			t := time.NewTimer(2 * time.Minute)
			select {
			case <-wake:
				t.Stop()
				continue
			case <-t.C:
			}
			// Idle for a while: the goroutine goes, unless a piece came in
			// just now (the next ask starts another).
			narrSynthQ.mu.Lock()
			if len(narrSynthQ.jobs) == 0 {
				narrSynthQ.running = false
				narrSynthQ.mu.Unlock()
				return
			}
			narrSynthQ.mu.Unlock()
			continue
		}
		// A piece nobody waits for (made ahead, or of a line already cut off)
		// is made only while narration is on with nothing in front, and
		// waits while Juka speaks or the orb is up; else it goes.
		if !narrOn.Load() || narrFront.Load() || gameScreenSuspended() {
			narrSynthDropUnwaitedLocked()
			if len(narrSynthQ.jobs) == 0 {
				narrSynthQ.mu.Unlock()
				continue
			}
		}
		quiet := !ttsBusy.Load() && !narrOrbUp.Load()
		pick := -1
		for i := len(narrSynthQ.jobs) - 1; i >= 0; i-- {
			if quiet || len(narrSynthQ.waiters[narrSynthQ.jobs[i].mkey]) > 0 {
				pick = i
				break
			}
		}
		if pick < 0 {
			narrSynthQ.mu.Unlock()
			time.Sleep(200 * time.Millisecond)
			continue
		}
		job := narrSynthQ.jobs[pick]
		narrSynthQ.jobs = append(narrSynthQ.jobs[:pick], narrSynthQ.jobs[pick+1:]...)
		narrSynthQ.busy = true
		narrSynthQ.mu.Unlock()

		res := narrSynthMake(job)

		narrSynthQ.mu.Lock()
		narrSynthQ.busy = false
		waiters := narrSynthQ.waiters[job.mkey]
		delete(narrSynthQ.waiters, job.mkey)
		narrSynthQ.mu.Unlock()
		for _, w := range waiters {
			w <- res
		}
		if len(waiters) == 0 {
			// Made ahead: the narrator may hand over the next piece.
			narrWake()
		}
	}
}

// narrSynthMake makes one piece: the card first (another speed may have
// asked for it), then the voice.
func narrSynthMake(job narrSynthJob) narrSynthResult {
	if c, ok := narrMemGet(job.mkey); ok {
		return narrSynthResult{samples: c.samples, rate: c.rate}
	}
	raw, rate, ok := []int16(nil), 0, false
	if job.keep {
		raw, rate, ok = narrDiskLoad(job.id, job.text)
	}
	whole := true
	if !ok {
		if gameScreenSuspended() {
			// No voice is loaded over a running game.
			return narrSynthResult{err: errNarrGameUp}
		}
		ctx, cancel := context.WithTimeout(context.Background(), narrSynthTimeout)
		var err error
		started := time.Now()
		raw, rate, whole, err = narrSynth(ctx, job.voice, job.text, job.gender)
		cancel()
		logDebug("narrate", "made %q in %s (ahead: %v)", job.text, time.Since(started).Round(time.Millisecond), job.ahead)
		if err != nil {
			return narrSynthResult{err: err}
		}
		if len(raw) == 0 || rate <= 0 {
			return narrSynthResult{err: errors.New("the voice made no sound")}
		}
		if job.keep && whole {
			narrDiskStore(job.id, job.text, raw, rate)
		}
	}
	c := narrMemClip{samples: narrStretch(raw, rate, job.speed), rate: rate}
	if whole {
		// A piece cut short is heard this once and kept nowhere.
		narrMemPut(job.mkey, c)
	}
	return narrSynthResult{samples: c.samples, rate: c.rate}
}

// narrSynth asks the device's voice for text: the platform's kept-loaded
// voice when there is one, else the voice the way Juka's lines use it. whole
// is false when the voice stopped part-way (the sound is still given).
func narrSynth(ctx context.Context, v ttsLocalVoice, text, gender string) ([]int16, int, bool, error) {
	if samples, rate, handled, err := narrSynthPlatform(ctx, v, text, gender); handled {
		if errors.Is(err, errTTSPartial) && len(samples) > 0 {
			return samples, rate, false, nil
		}
		return samples, rate, err == nil, err
	}
	var out []int16
	outRate := 0
	given := false
	next := func(context.Context) (string, bool) {
		if given {
			return "", false
		}
		given = true
		return text, true
	}
	err := ttsLocalSpeakChunks(ctx, v, next, gender, func(samples []int16, rate int) bool {
		if outRate == 0 {
			outRate = rate
		}
		if rate == outRate {
			out = append(out, samples...)
		}
		return true
	})
	if err != nil && !errors.Is(err, errTTSPartial) {
		return nil, 0, false, err
	}
	return out, outRate, err == nil, nil
}

// narrNoVoice is told once a run that narration has nothing to speak with.
func narrNoVoice(err error) {
	if errors.Is(err, errNarrGameUp) || errors.Is(err, errNarrVoiceBusy) {
		return
	}
	if errors.Is(err, errNarrNoRoom) {
		narrPlayEarcon(narrEarconWait)
		logDebug("narrate", "no memory free to load the narration voice")
		return
	}
	if !errors.Is(err, errNarrNoVoice) {
		logWarn("narrate", "the narration voice failed: %v", err)
		return
	}
	narrPlayEarcon(narrEarconWait)
	if narrVoiceWatching.Load() {
		return // the download is followed already
	}
	narrVoiceMissingOnce.Do(func() {
		tbxPost(func() { narrVoiceMissing(appConfig) })
	})
}

// narrVoiceMissingOnce tells about the missing voice once a run.
var narrVoiceMissingOnce sync.Once

// ── Making ahead ─────────────────────────────────────────────────────────

// narrAhead holds what to make while the user rests. Any goroutine, under mu.
var narrAhead struct {
	mu    sync.Mutex
	near  []string // the focused control's neighbours (replaced on each focus)
	queue []string // the common words, made once
}

// narrAheadNear replaces the neighbours to make.
func narrAheadNear(names []string) {
	narrAhead.mu.Lock()
	narrAhead.near = append(narrAhead.near[:0], names...)
	narrAhead.mu.Unlock()
}

// narrAheadQueue adds common pieces to make once.
func narrAheadQueue(texts []string) {
	narrAhead.mu.Lock()
	defer narrAhead.mu.Unlock()
	for _, t := range texts {
		if t = strings.TrimSpace(t); t != "" && len(narrAhead.queue) < 4*narrAheadMax {
			narrAhead.queue = append(narrAhead.queue, t)
		}
	}
}

// narrBackground hands the worker one piece to make ahead when it is idle,
// and reports whether it did. The narrator's goroutine, between lines.
func narrBackground(_ *narrItem) bool {
	if !narrOn.Load() || narrFront.Load() || narrOrbUp.Load() || ttsBusy.Load() || !narrSynthIdle() {
		return false
	}
	v, ok := ttsLocal()
	if !ok {
		return false
	}
	for {
		narrAhead.mu.Lock()
		var text string
		switch {
		case len(narrAhead.near) > 0:
			text = narrAhead.near[0]
			narrAhead.near = narrAhead.near[1:]
		case len(narrAhead.queue) > 0:
			text = narrAhead.queue[0]
			narrAhead.queue = narrAhead.queue[1:]
		}
		narrAhead.mu.Unlock()
		if text == "" {
			return false
		}
		text = narrSpeakableText(text)
		if text == "" || len(text) > narrPieceMaxChars {
			continue
		}
		gender := narrGenderName()
		id := narrVoiceID(v, gender)
		speed := int(narrRate.Load())
		mkey := id + "\x00" + strconv.Itoa(speed) + "\x00" + text
		if _, ok := narrMemGet(mkey); ok {
			continue
		}
		narrSynthAsk(narrSynthJob{text: text, keep: true, voice: v, gender: gender, id: id, speed: speed, mkey: mkey, ahead: true}, false)
		// The narrator looks again once that piece is made or something
		// is asked of it.
		narrSleep(400 * time.Millisecond)
		return true
	}
}

// ── The card ─────────────────────────────────────────────────────────────

// narrDisk serialises the folder's writes. Any goroutine, under mu.
var narrDisk struct {
	mu     sync.Mutex
	stored int
}

// narrDiskDir is narrate-cache/ in the app's data folder ("" without one).
func narrDiskDir() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "narrate-cache")
}

// narrDiskPath is where voiceID's piece text is kept.
func narrDiskPath(voiceID, text string) string {
	dir := narrDiskDir()
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(voiceID + "\x00" + text))
	return filepath.Join(dir, hex.EncodeToString(sum[:12])+".wav")
}

// narrDiskLoad is a kept piece. A file that is not a whole WAV is removed.
func narrDiskLoad(voiceID, text string) ([]int16, int, bool) {
	path := narrDiskPath(voiceID, text)
	if path == "" {
		return nil, 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false
	}
	samples, rate, err := ttsDecodeWAV(data, false)
	if err != nil || len(samples) == 0 || rate <= 0 || !ttsWAVComplete(data) {
		_ = os.Remove(path)
		return nil, 0, false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now) // heard: kept longest
	return samples, rate, true
}

// narrDiskStore keeps a piece, whole or not at all.
func narrDiskStore(voiceID, text string, samples []int16, rate int) {
	path := narrDiskPath(voiceID, text)
	if path == "" || len(samples) == 0 || rate <= 0 || len(samples) > rate*ttsCacheMaxSeconds {
		return
	}
	pcm := make([]byte, len(samples)*2)
	for i, v := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	wav := vkEncodeWAV(pcm, rate)
	narrDisk.mu.Lock()
	defer narrDisk.mu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, wav, 0o644); err != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return
	}
	narrDisk.stored++
	if narrDisk.stored%narrDiskPruneEvery == 1 {
		narrDiskPruneLocked(dir)
	}
}

// narrDiskPruneLocked keeps the folder within its bounds, the most recently
// heard first. narrDisk.mu is held.
func narrDiskPruneLocked(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type kept struct {
		path string
		size int64
		mod  time.Time
	}
	var files []kept
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		switch {
		case strings.HasSuffix(e.Name(), ".part"):
			if time.Since(info.ModTime()) > time.Minute {
				_ = os.Remove(path)
			}
		case strings.HasSuffix(e.Name(), ".wav"):
			files = append(files, kept{path, info.Size(), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	var total int64
	for i, f := range files {
		total += f.size
		if i >= narrDiskMaxFiles || total > narrDiskMaxBytes {
			_ = os.Remove(f.path)
		}
	}
}

// ── Speed ────────────────────────────────────────────────────────────────

// narrStretch plays in at pct percent of its speed without changing its
// pitch (WSOLA). 100 (or close) gives in back. Pure.
func narrStretch(in []int16, rate, pct int) []int16 {
	if pct >= 97 && pct <= 103 || pct < 50 || pct > 300 || rate <= 0 || len(in) == 0 {
		return in
	}
	speed := float64(pct) / 100
	n := rate * 24 / 1000 // one window, about 24 ms
	n &^= 1
	if n < 64 {
		return in
	}
	hs := n / 2            // the output hop
	tol := rate * 5 / 1000 // how far a window may move to match
	if len(in) < 2*n+2*tol {
		return in
	}
	win := make([]float64, n)
	for i := range win {
		// A periodic Hann window: at half-window hops they sum to one.
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
	}
	outLen := int(float64(len(in))/speed) + n
	acc := make([]float64, outLen)
	prev, k := 0, 0
	for {
		outPos := k * hs
		if outPos+n > outLen {
			break
		}
		start := int(float64(outPos) * speed)
		if k > 0 {
			// The window that continues the last one naturally is the
			// pattern; the start within ±tol of the ideal that matches it
			// best is taken.
			nat := prev + hs
			if nat+hs > len(in) {
				break
			}
			lo, hi := start-tol, start+tol
			if lo < 0 {
				lo = 0
			}
			if hi+n > len(in) {
				hi = len(in) - n
			}
			if lo > hi {
				break
			}
			best, bestC := lo, math.Inf(-1)
			for p := lo; p <= hi; p += 2 {
				c := 0.0
				for i := 0; i < hs; i += 2 {
					c += float64(in[nat+i]) * float64(in[p+i])
				}
				if c > bestC {
					best, bestC = p, c
				}
			}
			start = best
		}
		if start+n > len(in) {
			break
		}
		for i := 0; i < n; i++ {
			acc[outPos+i] += win[i] * float64(in[start+i])
		}
		prev = start
		k++
	}
	if k == 0 {
		return in
	}
	end := (k-1)*hs + n
	if end > len(acc) {
		end = len(acc)
	}
	out := make([]int16, end)
	for i := range out {
		v := acc[i]
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return out
}
