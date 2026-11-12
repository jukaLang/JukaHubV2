package main

// tts_play.go — Juka's voice on the speaker.
//
// One session speaks one line. A producer goroutine turns its chunks into
// sound in order — Groq's voice, or the device's own when Groq cannot — while
// the session's goroutine feeds the sound to the UI sound device that sound.go
// opens (mono 16-bit at audioSampleRate, the device the alarm and the trophy
// chime queue on too). The next chunk is fetched while one plays; one finished
// chunk at most waits (ttsBuffered), so a reply stopped early has cost at most
// two requests it did not need.
//
// The feeder queues short slices (ttsSliceMs) and never lets more than
// ttsAheadMs wait in the device, so stopping is immediate: the generation
// check before each slice, and ttsSilence dropping what is queued. That queue
// is SDL's only one for the device — a plain FIFO, not a mixer — so a tap of
// the interface queued behind the voice would wait for it and then cut a hole
// into it. While a session feeds the device, bootSfxPlay hands its sounds to
// ttsMixUISound instead, and they are added into the voice's next slices; when
// the voice is waiting for a chunk, or done, they go out on their own. A
// trophy chime still queues behind the voice (it is rare and long) and the
// voice waits for it. A ringing alarm silences the voice for good: its own
// player clears the queue, and the feeder stops at its next slice.
//
// The voice plays at the level it came in, through the system volume as every
// sound does; the "Interface sounds" switch and slider are for the taps, not
// for Juka. While Juka speaks, the music and a video carry on at ttsDuckLevel
// of their volume (QueueVideoAudio asks ttsMediaDuck), easing down and back up
// rather than jumping. Their own queue is about a second long, which is about
// how long Groq takes to answer: the duck starts when the session starts, so
// it has arrived by the time the voice does.
//
// Every quarter second the feeder has the main loop look at what else ends a
// line (ttsWatch): Quiet Mode switched on, Chat's AI tab or the Voice command
// page left by a line read there, and music or a video starting to play.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// ttsSliceMs is one feed of the device and ttsAheadMs the most that waits
	// in it: under bootSfxMaxQueued together, so a UI sound that does reach
	// the queue is never dropped for the voice.
	ttsSliceMs = 25
	ttsAheadMs = 100
	// ttsPoll is how often the feeder looks at the queue.
	ttsPoll = 8 * time.Millisecond
	// ttsWatchEvery is how often the main loop checks what ends a line.
	ttsWatchEvery = 250 * time.Millisecond
	// ttsDrainMax bounds the wait for the last slices to play, and ttsStallMax
	// the wait for room in a queue that stopped draining (a stalled device):
	// longer than the longest trophy chime the voice may queue behind.
	ttsDrainMax = 400 * time.Millisecond
	ttsStallMax = 3 * time.Second
	// ttsBuffered is how many finished chunks may wait for the speaker.
	ttsBuffered = 1
	// ttsMixMax bounds the interface sounds waiting to be mixed (samples).
	ttsMixMax = 96000
	// ttsDuckLevel is the music's and the video's volume while Juka speaks.
	ttsDuckLevel = 0.3
)

// ttsSession is one line being spoken. Read-only once its goroutine starts.
type ttsSession struct {
	gen      uint64
	kind     ttsKind
	origin   ttsOrigin
	chunks   []string
	rate     int    // the UI device's
	key      string // the Groq key ("": Groq's voice is not asked)
	keyID    uint64
	voice    string // Groq's voice
	gender   string // that voice's, for a local voice that has both
	local    ttsLocalVoice
	hasLocal bool
}

// ttsReport is what a session found out, for the one-time notices and the
// Hear Juka row.
type ttsReport struct {
	gen         uint64
	origin      ttsOrigin
	keyID       uint64
	voice       string        // Groq's voice that was asked ("" when none was)
	spoke       bool          // some of it was heard
	local       bool          // the device's voice spoke (some of) it
	localFailed bool          // the device's voice was asked and failed
	fail        ttsFail       // why Groq's voice did not speak (all of) it
	wait        time.Duration // for a limit: how long Groq asked to wait
	daily       bool          // for a limit: the day's allowance
}

// ttsProduced is the producer's share of the report.
type ttsProduced struct {
	fail        ttsFail
	wait        time.Duration
	daily       bool
	local       bool
	localFailed bool
}

// ttsRun speaks one line: the producer fills clips, the feeder plays them.
func ttsRun(ctx context.Context, s *ttsSession, pre ttsFail) {
	rep := ttsReport{gen: s.gen, origin: s.origin, keyID: s.keyID, voice: s.voice, fail: pre}
	defer func() {
		if r := recover(); r != nil {
			logWarn("tts", "Juka's voice stopped: %v", r)
		}
		ttsSessionEnded(s.gen)
		tbxPost(func() { ttsReported(rep) })
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	clips := make(chan []int16, ttsBuffered)
	var prod ttsProduced
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				logWarn("tts", "Juka's voice could not be made: %v", r)
			}
		}()
		ttsProduce(ctx, s, clips, &prod)
	}()
	rep.spoke = ttsFeed(ctx, s, clips)
	cancel()
	<-done
	if prod.fail != ttsFailNone {
		rep.fail, rep.wait, rep.daily = prod.fail, prod.wait, prod.daily
	}
	rep.local, rep.localFailed = prod.local, prod.localFailed
}

// ttsProduce makes the chunks' sound in order and hands it to the feeder:
// Groq's voice while it answers, and once it cannot, the device's voice for
// everything left in one go — or nothing more, when there is none.
func ttsProduce(ctx context.Context, s *ttsSession, out chan<- []int16, res *ttsProduced) {
	defer close(out)
	groq := s.key != ""
	for i := 0; i < len(s.chunks) && ctx.Err() == nil; i++ {
		if groq {
			samples, rate, err := ttsGroqSpeak(ctx, s.key, s.voice, s.chunks[i])
			if err == nil {
				if clip := ttsClipFor(samples, rate, s.rate); clip != nil && !ttsSend(ctx, out, clip) {
					return
				}
				continue
			}
			if ctx.Err() != nil {
				return
			}
			logWarn("tts", "Groq's voice: %v", err)
			res.fail, res.wait, res.daily = ttsFailOf(err)
			ttsGateNote(s.keyID, res.fail, res.wait, res.daily)
			groq = false
		}
		if !s.hasLocal {
			return
		}
		samples, rate, err := ttsLocalSpeak(ctx, s.local, strings.Join(s.chunks[i:], " "), s.gender)
		if err != nil {
			res.localFailed = ctx.Err() == nil
			return
		}
		res.local = true
		if clip := ttsClipFor(samples, rate, s.rate); clip != nil {
			ttsSend(ctx, out, clip)
		}
		return
	}
}

// ttsSend hands a clip to the feeder, unless the line is stopped first.
func ttsSend(ctx context.Context, out chan<- []int16, clip []int16) bool {
	select {
	case out <- clip:
		return true
	case <-ctx.Done():
		return false
	}
}

// ttsFeed plays the clips as they come, a slice at a time, and reports
// whether anything was heard. It returns when the clips are played out, the
// line is stopped or replaced, or an alarm rings.
func ttsFeed(ctx context.Context, s *ttsSession, clips <-chan []int16) bool {
	slice := s.rate * ttsSliceMs / 1000
	if slice < 64 {
		slice = 64
	}
	ahead := uint32(s.rate * 2 * ttsAheadMs / 1000)
	buf := make([]byte, slice*2)
	acc := make([]int32, slice)
	var cur []int16
	pos := 0
	spoke := false
	watched := time.Now()
	moved := time.Now() // the last time a slice went in, or a clip arrived
	ttsMixOpen(s.gen)
	defer func() {
		ttsMixClose(s.gen)
		audioMutex.Lock()
		if ttsGenNow.Load() == s.gen {
			// Still the current line: what it queued has played, or a ringing
			// alarm cleared it. (A stop or a newer line saw to it otherwise.)
			ttsFed.Store(false)
		}
		audioMutex.Unlock()
	}()
	for {
		if ctx.Err() != nil || ttsGenNow.Load() != s.gen || alarmFiringActive() {
			return spoke
		}
		if time.Since(watched) >= ttsWatchEvery {
			watched = time.Now()
			gen, origin := s.gen, s.origin
			tbxPost(func() { ttsWatch(gen, origin) })
		}
		if pos >= len(cur) {
			select {
			case clip, ok := <-clips:
				if !ok {
					ttsDrain(ctx, s.gen)
					return spoke
				}
				cur, pos = clip, 0
				moved = time.Now()
				continue
			default:
			}
			// Waiting for the next chunk: the interface's sounds go out alone.
			ttsMixFlush(s.gen)
			time.Sleep(ttsPoll)
			continue
		}
		audioMutex.Lock()
		queued := sdl.GetQueuedAudioSize(audioDev)
		audioMutex.Unlock()
		if queued >= ahead {
			if time.Since(moved) > ttsStallMax {
				// The device stopped playing: the line ends rather than
				// holding the orb and the music's duck for ever.
				logWarn("tts", "the speaker stopped taking Juka's voice; giving up on this answer")
				return spoke
			}
			time.Sleep(ttsPoll)
			continue
		}
		n := len(cur) - pos
		if n > slice {
			n = slice
		}
		for i := 0; i < n; i++ {
			acc[i] = int32(cur[pos+i])
		}
		ttsMixTake(s.gen, acc[:n])
		for i := 0; i < n; i++ {
			v := ttsClampS16(acc[i])
			buf[2*i] = byte(v)
			buf[2*i+1] = byte(uint16(v) >> 8)
		}
		audioMutex.Lock()
		live := ttsGenNow.Load() == s.gen
		if live {
			_ = sdl.QueueAudio(audioDev, buf[:n*2])
			ttsFed.Store(true)
		}
		audioMutex.Unlock()
		if !live {
			return spoke
		}
		publishAudioLevel(buf[:n*2], s.rate, 1)
		spoke = true
		pos += n
		moved = time.Now()
	}
}

// ttsDrain waits for the line's last slices to play (bounded), so the line
// counts as spoken until it has been heard.
func ttsDrain(ctx context.Context, gen uint64) {
	deadline := time.Now().Add(ttsDrainMax)
	for time.Now().Before(deadline) && ctx.Err() == nil && ttsGenNow.Load() == gen {
		audioMutex.Lock()
		left := sdl.GetQueuedAudioSize(audioDev)
		audioMutex.Unlock()
		if left == 0 {
			break
		}
		time.Sleep(ttsPoll)
	}
}

// ttsClampS16 clamps a mixed sample to 16 bits.
func ttsClampS16(v int32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// ttsSilence drops what a stopped line still has in the UI device's queue and
// gives the interface its sounds back: any waiting to be mixed are queued on
// their own. Any goroutine.
func ttsSilence() {
	audioMutex.Lock()
	defer audioMutex.Unlock()
	if ttsFed.Swap(false) && audioAvailable {
		sdl.ClearQueuedAudio(audioDev)
	}
	ttsMixCloseLocked(0)
}

// ── Interface sounds mixed into the voice ────────────────────────────────

// ttsMix holds the interface sounds that arrive while a session feeds the
// device, until the feeder adds them into its next slices. Lock order:
// audioMutex, then ttsMix.mu — bootSfxPlay calls in holding audioMutex.
var ttsMix struct {
	mu      sync.Mutex
	gen     uint64  // the session mixing (0: none — sounds queue as usual)
	pending []int32 // samples waiting, summed where sounds overlap
}

// ttsMixUISound is bootSfxPlay's hook, called with audioMutex held and the
// sound already at the interface volume: while Juka speaks the sound joins
// the voice instead of queueing behind it. It reports whether it took it.
func ttsMixUISound(pcm []byte) bool {
	ttsMix.mu.Lock()
	defer ttsMix.mu.Unlock()
	if ttsMix.gen == 0 {
		return false
	}
	n := len(pcm) / 2
	if n > ttsMixMax {
		n = ttsMixMax
	}
	for len(ttsMix.pending) < n {
		ttsMix.pending = append(ttsMix.pending, 0)
	}
	for i := 0; i < n; i++ {
		ttsMix.pending[i] += int32(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
	}
	return true
}

// ttsMixOpen starts mixing the interface's sounds into session gen.
func ttsMixOpen(gen uint64) {
	ttsMix.mu.Lock()
	ttsMix.gen = gen
	ttsMix.pending = ttsMix.pending[:0]
	ttsMix.mu.Unlock()
}

// ttsMixTake adds the waiting sounds into acc (one slice of the voice) and
// takes them off the list.
func ttsMixTake(gen uint64, acc []int32) {
	ttsMix.mu.Lock()
	defer ttsMix.mu.Unlock()
	if ttsMix.gen != gen || len(ttsMix.pending) == 0 {
		return
	}
	k := len(acc)
	if k > len(ttsMix.pending) {
		k = len(ttsMix.pending)
	}
	for i := 0; i < k; i++ {
		acc[i] += ttsMix.pending[i]
	}
	ttsMix.pending = append(ttsMix.pending[:0], ttsMix.pending[k:]...)
}

// ttsMixFlush queues the waiting sounds on their own while session gen has
// no voice to mix them into.
func ttsMixFlush(gen uint64) {
	audioMutex.Lock()
	defer audioMutex.Unlock()
	ttsMix.mu.Lock()
	if ttsMix.gen != gen || len(ttsMix.pending) == 0 {
		ttsMix.mu.Unlock()
		return
	}
	pcm := ttsMixDrainLocked()
	ttsMix.mu.Unlock()
	if audioAvailable && len(pcm) > 0 {
		_ = sdl.QueueAudio(audioDev, pcm)
	}
}

// ttsMixClose ends session gen's mixing: the waiting sounds are queued and
// the interface queues its own again.
func ttsMixClose(gen uint64) {
	audioMutex.Lock()
	defer audioMutex.Unlock()
	ttsMixCloseLocked(gen)
}

// ttsMixCloseLocked is ttsMixClose with audioMutex held; gen 0 closes
// whichever session mixes.
func ttsMixCloseLocked(gen uint64) {
	ttsMix.mu.Lock()
	if ttsMix.gen == 0 || (gen != 0 && ttsMix.gen != gen) {
		ttsMix.mu.Unlock()
		return
	}
	ttsMix.gen = 0
	pcm := ttsMixDrainLocked()
	ttsMix.mu.Unlock()
	if audioAvailable && len(pcm) > 0 {
		_ = sdl.QueueAudio(audioDev, pcm)
	}
}

// ttsMixDrainLocked takes every waiting sample as S16 bytes. ttsMix.mu is
// held.
func ttsMixDrainLocked() []byte {
	if len(ttsMix.pending) == 0 {
		return nil
	}
	pcm := make([]byte, len(ttsMix.pending)*2)
	for i, v := range ttsMix.pending {
		s := ttsClampS16(v)
		pcm[2*i] = byte(s)
		pcm[2*i+1] = byte(uint16(s) >> 8)
	}
	ttsMix.pending = ttsMix.pending[:0]
	return pcm
}

// ── The music's duck ─────────────────────────────────────────────────────

// ttsDuck is the factor the music and a video play at now. Only
// QueueVideoAudio's hook touches it, with videoAudioMutex held.
var ttsDuck = 1.0

// ttsMediaDuck is QueueVideoAudio's hook: the factor for this buffer, a step
// closer to ttsDuckLevel while Juka speaks and to full volume once Juka is
// done.
func ttsMediaDuck() float64 {
	target := 1.0
	if ttsSounding.Load() {
		target = ttsDuckLevel
	}
	ttsDuck += (target - ttsDuck) * 0.35
	if d := target - ttsDuck; d < 0.01 && d > -0.01 {
		ttsDuck = target
	}
	return ttsDuck
}

// ── What else ends a line ────────────────────────────────────────────────

// ttsWatchMedia is what was playing at the last look. Main thread only.
var ttsWatchMedia struct {
	gen          uint64
	music, video bool
}

// ttsMediaPlaying reports whether music and a video are playing (not paused).
func ttsMediaPlaying() (bool, bool) {
	music.mu.Lock()
	m := music.playing && !music.paused
	music.mu.Unlock()
	return m, playerPhase() == "playing"
}

// ttsWatchBegin notes what plays as line gen starts. Main thread.
func ttsWatchBegin(gen uint64) {
	ttsWatchMedia.gen = gen
	ttsWatchMedia.music, ttsWatchMedia.video = ttsMediaPlaying()
}

// ttsWatch ends line gen when Quiet Mode came on, when the screen it was read
// on was left, or when music or a video started to play since the last look
// (the user wants to hear that now). Main thread.
func ttsWatch(gen uint64, origin ttsOrigin) {
	if ttsGenNow.Load() != gen {
		return
	}
	stop := quietModeOn()
	switch origin {
	case ttsFromChat:
		stop = stop || !chatWasVisible || chatActiveTabID() != chatTabAI
	case ttsFromPage:
		stop = stop || appConfig == nil || !voiceOnPage(appConfig)
	}
	m, v := ttsMediaPlaying()
	if ttsWatchMedia.gen == gen && ((m && !ttsWatchMedia.music) || (v && !ttsWatchMedia.video)) {
		stop = true
	}
	ttsWatchMedia.gen, ttsWatchMedia.music, ttsWatchMedia.video = gen, m, v
	if stop {
		jukaSpeakStop()
	}
}

// ttsReported takes a finished session's report on the main thread: the
// one-time notices, and the Hear Juka row's answer.
func ttsReported(rep ttsReport) {
	if rep.origin != ttsFromTest {
		switch {
		case rep.fail == ttsFailTerms && ttsGateTellOnce(rep.keyID, ttsFailTerms):
			ShowToast("Juka's voice needs a one-time OK on Groq — see Settings → Voice & AI", ToastKindWarning)
		case rep.fail == ttsFailLimit && rep.daily && ttsGateTellOnce(rep.keyID, ttsFailLimit):
			ShowToast("Juka's voice is resting — today's free Groq voice allowance is used up", ToastKindInfo)
		case rep.fail == ttsFailLimit && rep.wait >= time.Minute && ttsGateTellOnce(rep.keyID, ttsFailLimit):
			ShowToast("Juka's voice is resting for a few minutes — Groq's free voice limit was reached", ToastKindInfo)
		}
		return
	}
	// A test line stopped or replaced before its end (Hear Juka pressed
	// again) has nothing to report: "made no sound" would be wrong.
	if rep.gen != ttsGenNow.Load() {
		return
	}
	ttsTestOutcome(rep)
}
