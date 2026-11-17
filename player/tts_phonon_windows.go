//go:build windows

package main

// tts_phonon_windows.go — the natural offline voice is for the TrimUI
// handhelds (tts_phonon.go); a PC has Windows' own voice.

import (
	"context"
	"errors"
)

var errTTSPhononWindows = errors.New("the natural voice is for TrimUI handhelds")

func ttsPhononStream(context.Context, ttsLocalVoice, string, func(context.Context) (string, bool), func([]int16, int) bool) (int, []string, error) {
	return 0, nil, errTTSPhononWindows
}

func ttsPhononSynth(context.Context, ttsLocalVoice, string, string) ([]int16, int, error) {
	return nil, 0, errTTSPhononWindows
}

func ttsPhononBench(context.Context, string, ttsPhononManifest) (float64, error) {
	return 0, errTTSPhononWindows
}

func ttsPhononPrewarm() {}

func ttsPhononWarmStop() {}
