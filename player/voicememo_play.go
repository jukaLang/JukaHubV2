package main

// voicememo_play.go — Voice Memos' playback, and the other sound it holds
// back while a memo records or plays.
//
// A memo plays from its file on a goroutine, a fifth of a second at a time,
// with about half a second queued ahead: a thirty-minute memo is never read
// into RAM, and the main thread only reads the position under a lock. The
// output is opened through outOpen (audioroute.go), so a Bluetooth headset
// hears it, at the file's own rate and channel count with no changes allowed:
// SDL converts to whatever the hardware runs at, and nothing is resampled
// here. A WAV that is not 16-bit PCM (24-bit, float — a file someone copied
// in) is decoded whole with ttsDecodeWAV when it is small enough. The test's
// sample plays the same way from memory (voicememo_mictest.go).
//
// While a memo records or plays, or the Microphone test runs, the music
// player is paused without a sound and given back afterwards — the voice
// orb's recipe (voiceOrbHushMedia), handed to a ringing alarm when one rings
// at that moment — a playing video is paused the same way, and the menu
// music fades out (bgmQuietReason reads vmAudioBusy).

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// vmDecodeMax is the largest non-16-bit WAV decoded whole for playback.
	vmDecodeMax = 32 << 20
	// vmPlayTail is how long the device's own buffer plays on after the
	// queue reads empty.
	vmPlayTail = 80 * time.Millisecond
)

// vmAudioBusy is set while a memo records or plays or the test runs. The menu
// music's goroutine reads it (bgmQuietReason in bgmusic.go).
var vmAudioBusy atomic.Bool

// voiceMemoHoldsAudio reports whether Voice Memos is recording or playing, so
// the menu music stays quiet. Safe from any goroutine.
func voiceMemoHoldsAudio() bool { return vmAudioBusy.Load() }

// vmHush is what Voice Memos paused and must give back. Main thread only.
var vmHush struct {
	on    bool
	music bool
	video bool
}

// vmMediaSync holds the other sound back while Voice Memos makes or takes
// sound, and gives it back when it stops. Main thread; cheap, so every frame
// and every start and stop calls it.
func vmMediaSync() {
	busy := vm.rec != nil || vm.play != nil || vmTestBusy()
	vmAudioBusy.Store(busy)
	switch {
	case busy && !vmHush.on:
		vmHushMedia()
	case !busy && vmHush.on:
		vmRestoreMedia()
	}
}

// vmHushMedia pauses the music and a playing video silently (a microphone
// beside the speaker would record them; a memo would play over them). A video
// in a Watch Party plays on: pausing it would pause it for the whole room.
func vmHushMedia() {
	vmHush.on = true
	music.mu.Lock()
	playing := music.playing && !music.paused
	if playing {
		music.paused = true
	}
	music.mu.Unlock()
	if playing {
		ClearVideoAudioQueue()
		vmHush.music = true
	}
	if playerPhase() == "playing" && !partyActive() {
		ToggleVideoPlayback()
		vmHush.video = true
	}
}

// vmRestoreMedia lets what vmHushMedia paused play on. While an alarm rings
// it is handed to the ring, which resumes it when it ends: resuming it now
// would play it under the ring.
func vmRestoreMedia() {
	vmHush.on = false
	if alarmFiringActive() {
		if vmHush.music {
			alarmHeld.music = true
		}
		if vmHush.video {
			alarmHeld.video = true
		}
		vmHush.music, vmHush.video = false, false
		return
	}
	if vmHush.video {
		vmHush.video = false
		if playerPhase() == "paused" {
			ToggleVideoPlayback()
		}
	}
	if vmHush.music {
		vmHush.music = false
		music.mu.Lock()
		if music.playing && music.paused {
			music.paused = false
		}
		music.mu.Unlock()
	}
}

// ── Player ───────────────────────────────────────────────────────────────

// vmPlaySource is what a player plays: a memo's file (with its header) or
// mono 16-bit sound in memory.
type vmPlaySource struct {
	path    string
	wav     vmWAV
	size    int64
	mem     []byte
	memRate int
}

// vmPlayer is one playback. The main thread owns the pointer; the fields
// under mu are shared with the feeding goroutine.
type vmPlayer struct {
	file  string // the memo's file, "" for the test's sample
	total time.Duration

	mu      sync.Mutex
	bps     int64 // bytes of sound a second, once the goroutine knows
	dataLen int64
	fed     int64 // the position after everything queued
	queued  int64 // what was still queued at the last look
	paused  bool
	seekTo  int64 // -1: no seek waiting
	done    bool
	failed  string

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// vmStartPlayer starts playing src. total is the length shown until the
// goroutine has read the file.
func vmStartPlayer(file string, total time.Duration, src vmPlaySource) *vmPlayer {
	p := &vmPlayer{
		file:   file,
		total:  total,
		seekTo: -1,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go p.run(src)
	return p
}

// position is where playback is: what was queued minus what still waits.
func (p *vmPlayer) position() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bps <= 0 {
		return 0
	}
	at := p.fed - p.queued
	if at < 0 {
		at = 0
	}
	return time.Duration(at * int64(time.Second) / p.bps)
}

// length is the memo's length, from the file once it is open.
func (p *vmPlayer) length() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bps > 0 && p.dataLen > 0 {
		return time.Duration(p.dataLen * int64(time.Second) / p.bps)
	}
	return p.total
}

func (p *vmPlayer) isPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

func (p *vmPlayer) setPaused(paused bool) {
	p.mu.Lock()
	p.paused = paused
	p.mu.Unlock()
}

// seekBy moves playback by d, kept inside the memo. The new place shows at
// once; the goroutine drops what it had queued and reads on from there.
func (p *vmPlayer) seekBy(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bps <= 0 {
		return
	}
	at := p.fed - p.queued
	if p.seekTo >= 0 {
		at = p.seekTo
	}
	at += int64(d) * p.bps / int64(time.Second)
	if at < 0 {
		at = 0
	}
	if at > p.dataLen {
		at = p.dataLen
	}
	p.seekTo = at
	p.fed, p.queued = at, 0
}

// seekFraction moves playback to a fraction (0..1) of the memo: a click on
// the progress bar.
func (p *vmPlayer) seekFraction(f float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bps <= 0 || p.dataLen <= 0 {
		return
	}
	f = math.Max(0, math.Min(1, f))
	at := int64(f * float64(p.dataLen))
	p.seekTo = at
	p.fed, p.queued = at, 0
}

// finished reports whether playback ended, and why when it failed.
func (p *vmPlayer) finished() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done, p.failed
}

// stop ends playback; the goroutine closes the output. Safe to call twice.
func (p *vmPlayer) stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
}

// wait waits up to d for the goroutine to have closed the output (the exit
// path, before SDL's audio goes away).
func (p *vmPlayer) wait(d time.Duration) {
	select {
	case <-p.doneCh:
	case <-time.After(d):
	}
}

func (p *vmPlayer) end(failed string) {
	p.mu.Lock()
	p.done = true
	if p.failed == "" {
		p.failed = failed
	}
	p.mu.Unlock()
}

// run feeds the output until the sound ends or stop is called.
func (p *vmPlayer) run(src vmPlaySource) {
	defer close(p.doneCh)
	defer func() {
		if r := recover(); r != nil {
			logWarn("voicememo", "playback stopped: %v", r)
			p.end("playback stopped")
		}
	}()
	ra, dataOff, dataLen, rate, ch, closeFn, why := vmOpenSource(src)
	if closeFn != nil {
		defer closeFn()
	}
	if why != "" {
		p.end(why)
		return
	}
	frame := int64(2 * ch)
	bps := int64(rate) * frame
	dataLen -= dataLen % frame
	p.mu.Lock()
	p.bps, p.dataLen = bps, dataLen
	p.mu.Unlock()

	desired := sdl.AudioSpec{Freq: int32(rate), Format: sdl.AUDIO_S16SYS, Channels: uint8(ch), Samples: 1024}
	var obtained sdl.AudioSpec
	// No changes allowed: SDL converts the memo's rate and channels to the
	// hardware's, so nothing is resampled here.
	dev, err := outOpen(&desired, &obtained, 0)
	if err != nil || dev == 0 {
		logWarn("voicememo", "no audio output for playback: %v", err)
		p.end("no audio output")
		return
	}
	defer outClose(dev)

	chunk := make([]byte, max(bps/5/frame, 1)*frame)
	ahead := bps / 2
	pos := int64(0)
	eof := false
	devPaused := true // outOpen hands it over paused
	for {
		select {
		case <-p.stopCh:
			outClear(dev)
			return
		default:
		}
		p.mu.Lock()
		seek, paused := p.seekTo, p.paused
		p.seekTo = -1
		p.mu.Unlock()
		if seek >= 0 {
			outClear(dev)
			pos = seek - seek%frame
			eof = false
		}
		if paused {
			if !devPaused {
				outPause(dev, true)
				devPaused = true
			}
			p.publish(pos, int64(outQueued(dev)))
			time.Sleep(20 * time.Millisecond)
			continue
		}
		q := int64(outQueued(dev))
		for !eof && q < ahead {
			want := int64(len(chunk))
			if rem := dataLen - pos; rem < want {
				want = rem
			}
			if want <= 0 {
				eof = true
				break
			}
			n, rerr := ra.ReadAt(chunk[:want], dataOff+pos)
			n -= n % int(frame)
			if n > 0 {
				if qerr := outQueue(dev, chunk[:n]); qerr != nil {
					logWarn("voicememo", "playback: %v", qerr)
					p.end("the audio output is busy")
					return
				}
				pos += int64(n)
				q += int64(n)
			}
			if rerr != nil || int64(n) < want {
				eof = true
			}
		}
		if devPaused {
			outPause(dev, false)
			devPaused = false
		}
		q = int64(outQueued(dev))
		p.publish(pos, q)
		if eof && q == 0 {
			select {
			case <-p.stopCh:
			case <-time.After(vmPlayTail):
			}
			p.end("")
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// publish hands the position to the main thread. A seek asked for while this
// pass ran is left to show its own place.
func (p *vmPlayer) publish(fed, queued int64) {
	p.mu.Lock()
	if p.seekTo < 0 {
		p.fed, p.queued = fed, queued
	}
	p.mu.Unlock()
}

// vmOpenSource opens what a player reads: the sound's reader, where the
// sound starts and how long it is, its rate and channels, a close function,
// and a reason it cannot play ("" when it can).
func vmOpenSource(src vmPlaySource) (io.ReaderAt, int64, int64, int, int, func(), string) {
	if src.mem != nil {
		rate := src.memRate
		if rate <= 0 {
			rate = vmRate
		}
		return bytes.NewReader(src.mem), 0, int64(len(src.mem)), rate, 1, nil, ""
	}
	f, err := os.Open(src.path)
	if err != nil {
		return nil, 0, 0, 0, 0, nil, "the file is gone"
	}
	closeFn := func() { _ = f.Close() }
	if src.wav.streamable() {
		return f, src.wav.dataOff, src.wav.dataLen, src.wav.rate, src.wav.channels, closeFn, ""
	}
	if src.size > vmDecodeMax {
		return nil, 0, 0, 0, 0, closeFn, "this kind of WAV is too big to play here"
	}
	data, err := io.ReadAll(io.LimitReader(f, vmDecodeMax+1))
	if err != nil {
		return nil, 0, 0, 0, 0, closeFn, "the file could not be read"
	}
	samples, rate, err := ttsDecodeWAV(data, false)
	if err != nil || len(samples) == 0 {
		return nil, 0, 0, 0, 0, closeFn, "JukaHub cannot play this kind of WAV"
	}
	pcm := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(s))
	}
	return bytes.NewReader(pcm), 0, int64(len(pcm)), rate, 1, closeFn, ""
}

// ── Main-thread controls ─────────────────────────────────────────────────

// vmPlayToggle is A on a memo: play it, or pause / resume it when it is the
// one playing.
func vmPlayToggle(m vmMemo) {
	if p := vm.play; p != nil && p.file == m.file {
		p.setPaused(!p.isPaused())
		PlayNavSound()
		return
	}
	if !m.ok {
		ShowToast("This file is not a recording JukaHub can play", ToastKindWarning)
		PlayErrorSound()
		return
	}
	if vm.rec != nil {
		ShowToast("Stop the recording first", ToastKindInfo)
		return
	}
	vmPlayStop()
	src := vmPlaySource{path: vmMemoPath(m), wav: m.wav, size: m.size}
	vm.play = vmStartPlayer(m.file, m.dur, src)
	// The music goes quiet before the memo starts.
	vmMediaSync()
	PlayActivateSound()
}

// vmPlayStop stops the memo playing, if one is.
func vmPlayStop() {
	p := vm.play
	if p == nil {
		return
	}
	vm.play = nil
	p.stop()
	vmMediaSync()
}

// vmPlayRelease stops the memo in file if it is the one playing, and waits a
// moment for the goroutine to have closed it: Windows will not rename or
// delete a file that is open, and stop alone only asks the goroutine to end.
func vmPlayRelease(file string) {
	p := vm.play
	if p == nil || p.file != file {
		return
	}
	vmPlayStop()
	p.wait(time.Second)
}

// vmMemoPath is a memo's full path.
func vmMemoPath(m vmMemo) string {
	return filepath.Join(vm.dir, m.file)
}

// vmPlayLine is the playing memo's "0:12 / 1:23".
func vmPlayLine(p *vmPlayer) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%s / %s", vmClock(p.position()), vmClock(p.length()))
}
