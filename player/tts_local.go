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

// ttsLocal is the device's voice, when it has a working one.
func ttsLocal() (ttsLocalVoice, bool) {
	ttsLocalState.mu.Lock()
	defer ttsLocalState.mu.Unlock()
	if !ttsLocalState.checked {
		ttsLocalState.voice, ttsLocalState.ok = ttsLocalFind()
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
	samples, rate, err := ttsLocalSynth(ctx, v, text, gender)
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
				logWarn("tts", "Piper stopped part-way: %v", err)
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
