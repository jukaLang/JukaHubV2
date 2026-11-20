//go:build !windows

package main

// narrate_other.go — narration's pieces on the handheld: the loaded Piper,
// borrowed the way the voice cache's seeding borrows it (tts_local_other.go).
//
// While narration holds Piper it is marked as seeding, so a line of Juka's
// that wants Piper meanwhile waits the moment it takes (ttsPiperWarmTake)
// instead of loading a second ~120 MB Piper beside it. Each use counts as use:
// the voice stays loaded while the user browses, and goes ttsPiperWarmIdle
// after (narration keeps the longer typed wait, narrSayItem). While a line of
// Juka's holds Piper the piece waits for it; with no memory to load Piper it
// is not said (a soft tone) — never a second, cold Piper beside the first.
// Another engine (espeak on a Linux PC) goes the way Juka's lines go
// (narrSynth's fallback).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// narrSynthPlatform speaks text with the loaded Piper. handled is false when
// another way should be tried.
func narrSynthPlatform(ctx context.Context, v ttsLocalVoice, text, _ string) ([]int16, int, bool, error) {
	if v.kind != "piper" || v.model == "" {
		return nil, 0, false, nil
	}
	clean := func(c string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(c, "\x00", "")), " ")
	}
	line := clean(text)
	if line == "" {
		return nil, 0, true, nil
	}
	// Piper busy with a line of Juka's (or the voice cache's): wait for it.
	// Juka's way would load a second, cold ~120 MB Piper beside it.
	p, done, why := narrPiperBorrow(v)
	for p == nil && errors.Is(why, errNarrVoiceBusy) {
		if gameScreenSuspended() {
			return nil, 0, true, errNarrGameUp
		}
		select {
		case <-ctx.Done():
			return nil, 0, true, errNarrVoiceBusy
		case <-time.After(50 * time.Millisecond):
		}
		p, done, why = narrPiperBorrow(v)
	}
	if why != nil {
		// No room to load it: a soft tone, not a cold Piper for every piece.
		return nil, 0, true, why
	}
	if p == nil {
		// It would not start (an odd Piper build): Juka's way may still work.
		return nil, 0, false, nil
	}
	var out []int16
	rate := 0
	// The CPU is raised as for a line of Juka's, but without its per-line
	// "first sound after" log, which would fill the log a row at a time.
	voiceBoostHold(3 * time.Second)
	_, healthy, err := ttsPiperStreamWarm(ctx, p, line, func(context.Context) (string, bool) { return "", false }, clean,
		func(samples []int16, r int) bool {
			if rate == 0 {
				rate = r
			}
			if r == rate {
				out = append(out, samples...)
			}
			return true
		}, false)
	narrPiperGive(p, done, healthy)
	if err != nil && len(out) == 0 && !errors.Is(err, errTTSPartial) {
		if gameScreenSuspended() {
			// A game started and took Piper: nothing more now.
			return nil, 0, true, errNarrGameUp
		}
		if ctx.Err() != nil {
			return nil, 0, true, ctx.Err()
		}
		logWarn("narrate", "Piper could not say a piece: %v", err)
		return nil, 0, false, nil
	}
	if err != nil {
		// Part of it: heard once, kept nowhere (narrSynthMake).
		return out, rate, true, errTTSPartial
	}
	return out, rate, true, nil
}

// narrPiperBorrow takes the loaded Piper (starting it when there is room) and
// marks it seeding. Without one, why is errNarrVoiceBusy while a line of
// Juka's has it (or is about to take it), errNarrNoRoom when there is no
// memory to load it, and nil when it would not start.
func narrPiperBorrow(v ttsLocalVoice) (*ttsPiperProc, chan struct{}, error) {
	ttsPiperWarm.mu.Lock()
	defer ttsPiperWarm.mu.Unlock()
	if ttsPiperWarm.busy || ttsBusy.Load() {
		return nil, nil, errNarrVoiceBusy
	}
	p := ttsPiperWarm.p
	if p != nil {
		select {
		case <-p.exited:
			p = nil
		default:
		}
	}
	if p == nil || p.key != ttsPiperKey(v) {
		if freeMemoryMB() < voicePiperWarmMB+ttsPiperLowWaterMB {
			return nil, nil, errNarrNoRoom
		}
		if p = ttsPiperWarmStartLocked(v); p == nil {
			return nil, nil, nil
		}
	}
	// A path left over from an earlier borrower goes with its file.
	for drained := false; !drained; {
		select {
		case path, ok := <-p.out:
			if !ok {
				// It is going: a new one starts on the next try.
				return nil, nil, errNarrVoiceBusy
			}
			if wav := filepath.Clean(path); filepath.Dir(wav) == filepath.Clean(p.dir) {
				_ = os.Remove(wav)
			}
		default:
			drained = true
		}
	}
	done := make(chan struct{})
	ttsPiperWarm.busy = true
	ttsPiperWarm.seeding = done
	return p, done, nil
}

// narrPiperGive hands Piper back. One that still owes lines is ended.
func narrPiperGive(p *ttsPiperProc, done chan struct{}, healthy bool) {
	ttsPiperWarm.mu.Lock()
	if ttsPiperWarm.p == p {
		ttsPiperWarm.busy = false
		ttsPiperWarm.lastUse = time.Now()
		if !healthy {
			ttsPiperWarm.p = nil
		}
	}
	if ttsPiperWarm.seeding == done {
		ttsPiperWarm.seeding = nil
	}
	ttsPiperWarm.mu.Unlock()
	close(done)
	if !healthy {
		_ = p.cmd.Process.Kill()
	}
}
