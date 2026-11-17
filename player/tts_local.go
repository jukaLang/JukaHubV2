package main

// tts_local.go — the device's own voice, whichever it is (tts_local_windows.go,
// tts_local_other.go), found once and remembered.
//
// Looking for it is a few file checks, done the first time Juka speaks or
// Settings shows the Voice row, and again when "Hear Juka" is pressed (a voice
// installed since then is found). One that fails twice in a row is not used
// again this run: a broken engine would otherwise start a process, wait and
// fail on every answer.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ttsLocalVoice is a speech engine on this device.
type ttsLocalVoice struct {
	name  string // what Settings calls it
	path  string // the program
	kind  string // "sapi", or the engine's program name
	model string // piper's voice model
}

// ttsLocalState caches the search. Any goroutine.
var ttsLocalState struct {
	mu      sync.Mutex
	checked bool
	voice   ttsLocalVoice
	ok      bool
	fails   int
}

// ttsLocalFailLimit is how many failures in a row retire the engine.
const ttsLocalFailLimit = 2

// errTTSPartial: the device's voice said only part of the line.
var errTTSPartial = errors.New("the device's voice stopped part-way")

// errTTSFellBack: the line was heard, but from Piper standing in for the
// natural voice, so tts_cache.go must not keep it as the natural voice's.
var errTTSFellBack = errors.New("Piper spoke for the natural voice")

// ttsTalkAt is when a conversation with Juka last showed signs of life — the
// orb up, listening, thinking or answering — as time since ttsTalkEpoch (0:
// never). A loaded Piper stays until ttsPiperWarmIdle after both this and
// its last line (tts_local_other.go), so a slow offline answer does not
// find the voice gone. Any goroutine.
var ttsTalkAt atomic.Int64

// ttsTalkEpoch is ttsTalkAt's zero, on the monotonic clock (the handheld's
// wall clock jumps when NTP arrives).
var ttsTalkEpoch = time.Now()

// ttsTalkTyped is set while the device's voice last spoke a Chat reply or a
// Voice command page answer: the next question there is typed, or asked
// without the orb that keeps the voice loaded, so the voice waits longer
// before it lets go. Any goroutine.
var ttsTalkTyped atomic.Bool

// ttsKeepWarm notes that a conversation is still going. Any goroutine; cheap
// enough for every frame.
func ttsKeepWarm() { ttsTalkAt.Store(int64(time.Since(ttsTalkEpoch)) | 1) }

// ttsTalkLast is when ttsKeepWarm was last called (zero time: never).
func ttsTalkLast() time.Time {
	at := ttsTalkAt.Load()
	if at == 0 {
		return time.Time{}
	}
	return ttsTalkEpoch.Add(time.Duration(at))
}

// ttsLocal is the device's voice, when it has a working one.
func ttsLocal() (ttsLocalVoice, bool) {
	ttsLocalState.mu.Lock()
	defer ttsLocalState.mu.Unlock()
	if !ttsLocalState.checked {
		// The natural voice first, when it is installed and wanted
		// (tts_phonon_install.go); Piper or the PC's voice otherwise.
		if v, ok := ttsPhononVoice(); ok {
			ttsLocalState.voice, ttsLocalState.ok = v, true
		} else {
			ttsLocalState.voice, ttsLocalState.ok = ttsLocalFind()
		}
		ttsLocalState.checked = true
	}
	if ttsLocalState.fails >= ttsLocalFailLimit {
		return ttsLocalVoice{}, false
	}
	return ttsLocalState.voice, ttsLocalState.ok
}

// ttsLocalRecheck forgets the search and the failures ("Hear Juka").
func ttsLocalRecheck() {
	ttsLocalState.mu.Lock()
	ttsLocalState.checked = false
	ttsLocalState.fails = 0
	ttsLocalState.mu.Unlock()
}

// ttsLocalSpeak renders text with the device's voice, counting its failures.
func ttsLocalSpeak(ctx context.Context, v ttsLocalVoice, text, gender string) ([]int16, int, error) {
	var samples []int16
	var rate int
	var err error
	if v.kind == "phonon" {
		// The natural voice counts its own failures (ttsPhononNoteFail);
		// Piper says the line when it cannot.
		samples, rate, err = ttsPhononSynth(ctx, v, text, gender)
		if err != nil && ctx.Err() == nil {
			if pv, ok := ttsManagedVoice(); ok {
				logWarn("tts", "the natural voice failed, Piper speaks instead: %v", err)
				v = pv
				samples, rate, err = ttsLocalSynth(ctx, v, text, gender)
			}
		}
	} else {
		samples, rate, err = ttsLocalSynth(ctx, v, text, gender)
	}
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	ttsLocalState.mu.Lock()
	if err != nil {
		ttsLocalState.fails++
	} else {
		ttsLocalState.fails = 0
	}
	ttsLocalState.mu.Unlock()
	if err != nil {
		logWarn("tts", "the device's voice (%s) failed: %v", v.kind, err)
	}
	return samples, rate, err
}

// ttsLocalSpeakChunks speaks chunks with the device's voice, handing each
// piece of sound to emit in order (emit returns false to stop). next gives
// the chunks in order, waiting while a live answer is still being written
// (tts_live.go); false ends them. Piper makes them one chunk at a time from
// one process, each as it arrives; when that cannot start or makes nothing —
// an older Piper without --output_dir, say — and for every other engine, the
// chunks are rendered as one line.
func ttsLocalSpeakChunks(ctx context.Context, v ttsLocalVoice, next func(context.Context) (string, bool), gender string, emit func([]int16, int) bool) error {
	// What Piper was given is kept, for the whole-line fallback.
	var given []string
	record := func(ctx context.Context) (string, bool) {
		c, ok := next(ctx)
		if ok {
			given = append(given, c)
		}
		return c, ok
	}
	if v.kind == "phonon" {
		// The natural voice (tts_phonon.go) streams like Piper. When it
		// fails, Piper says what it had not said yet, then the rest.
		n, rest, err := ttsPhononStream(ctx, v, gender, next, emit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}
		pv, ok := ttsManagedVoice()
		if !ok {
			if n > 0 {
				logWarn("tts", "the natural voice stopped part-way: %v", err)
				return errTTSPartial
			}
			return err
		}
		logWarn("tts", "the natural voice failed after %d pieces, Piper goes on: %v", n, err)
		replay := func(ctx context.Context) (string, bool) {
			if len(rest) > 0 {
				c := rest[0]
				rest = rest[1:]
				return c, true
			}
			return next(ctx)
		}
		if err := ttsLocalSpeakChunks(ctx, pv, replay, gender, emit); err != nil {
			if n > 0 && ctx.Err() == nil {
				return errTTSPartial
			}
			return err
		}
		return errTTSFellBack
	}
	if v.kind == "piper" {
		n, err := ttsPiperStream(ctx, v, record, emit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if n > 0 || err == nil {
			ttsLocalState.mu.Lock()
			ttsLocalState.fails = 0
			ttsLocalState.mu.Unlock()
			if err != nil {
				// Some of it was heard: not a failure of the voice, but
				// not the whole line either (tts_cache.go keeps only that).
				logWarn("tts", "Piper stopped part-way: %v", err)
				return errTTSPartial
			}
			return nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			// It ran out of time: rendering the whole line would only make
			// the user wait as long again.
			ttsLocalState.mu.Lock()
			ttsLocalState.fails++
			ttsLocalState.mu.Unlock()
			logWarn("tts", "Piper timed out: %v", err)
			return err
		}
		logWarn("tts", "Piper chunk by chunk failed, rendering the whole line: %v", err)
	}
	// The whole line: what Piper already had, and the rest once it has all
	// arrived.
	for ctx.Err() == nil {
		if _, ok := record(ctx); !ok {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(strings.Join(given, "")) == "" {
		return nil
	}
	samples, rate, err := ttsLocalSpeak(ctx, v, strings.Join(given, " "), gender)
	if err != nil {
		return err
	}
	emit(samples, rate)
	return nil
}
