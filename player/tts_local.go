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
