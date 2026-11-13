package main

// avsync.go — the picture keeps to the sound.
//
// Juka (2026-11-12): "when downloading some videos, the video and the audio
// sometimes doesn't match up, but sometimes it does." The embedded player runs
// two ffmpeg processes, one for the picture and one for the sound, and nothing
// ever tied them together. The sound played at the device's pace; the picture
// at a wall clock started on its own first frame. Five things pulled them
// apart, each on some videos only:
//
//   - The picture was timed by ffprobe's avg_frame_rate, but ffmpeg wrote its
//     raw frames at the rate it picked itself (r_frame_rate, for a constant
//     output). On a variable-rate file — many downloads and phone clips — the
//     two differ by a fraction of a percent: seconds of drift after ten
//     minutes. The decoder is now told the rate (fps filter), so frame n is at
//     n / fps exactly.
//   - The streams of one file need not start together (a joined download, an
//     HLS copy, AAC priming). Each decoder began at its own first sample. Now
//     both start at the seek point: the fps filter repeats the first frame and
//     aresample pads silence up to it.
//   - Any hiccup over 250 ms (a slow keyframe, the UI busy, Wi-Fi) reset the
//     picture's clock to "now", so the picture fell behind for good, a little
//     more each time. Now a late picture reads its frames as fast as they come
//     until it is back on the sound.
//   - A seek started the new sound behind up to a second of the old position's
//     audio still queued in the device. The queue is now cleared before the new
//     sound's first buffer.
//   - The two processes start at different speeds (a scaled picture opens
//     slower), so the offset between them was whatever the race gave.
//
// So the sound is the master clock: the picture waits for the sound's first
// buffer (briefly), then shows frame n when the device has played n / fps
// seconds, read from what the device has consumed (videoAudioClock). Without
// sound — none in the file, its decoder ended, a device that stopped taking
// data — the picture runs on the wall clock as before.

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// avSyncSnap is an error between the picture and the sound that is fixed
	// at once (the picture waits or catches up); a smaller one is taken away
	// over about avSyncSlewTau, which also smooths over the device taking its
	// data a whole buffer (~85 ms) at a time.
	avSyncSnap    = 0.25
	avSyncSlewTau = 0.6
	// avSyncAudioWait bounds how long the first frame waits for the sound.
	avSyncAudioWait = 2 * time.Second
	// avSyncAudioStall is how long the device may take nothing before the
	// picture stops following it (a stalled device must not freeze the video).
	avSyncAudioStall = time.Second
	// avSyncWallLate is how late a picture on the wall clock may run before
	// its clock is moved to it (the process was paused by a game, say).
	avSyncWallLate = 1.0
	// avSyncHeadsetDelay is what a Bluetooth headset adds after JukaHub's own
	// mixer lead (audioroute.go): BlueALSA's buffer and the A2DP codec.
	avSyncHeadsetDelay = 150 * time.Millisecond
	// The frame rates the decoder is pinned to; anything outside is a probe
	// that misread the stream, and 30 is used.
	avSyncMinFPS = 1.0
	avSyncMaxFPS = 120.0
)

// avFiltersOff is raised when the ffmpeg in use lacks the fps or aresample
// filter (or their options): the decoders then run without them, as before.
var avFiltersOff atomic.Bool

// avSync is one playback session's clock.
type avSync struct {
	// Set by the sound decoder.
	audioStarted atomic.Bool // its first buffer is queued
	audioDone    atomic.Bool // it ended (end of file, error, no sound)

	// mark is the device clock's count where this session's sound begins
	// (ClearVideoAudioQueueMark), stored before audioStarted is raised.
	mark atomic.Int64

	// The picture decoder's own, used from its goroutine only.
	origin       time.Time // when media time 0 was (pauses added)
	lastConsumed int64     // the device's consumed count last read
	lastMove     time.Time // when the device last took data
	lastAdjust   time.Time // when the clock was last pulled to the sound
	following    bool      // the clock was on the sound at the last read
}

// avClampFPS is the rate the picture is decoded and timed at.
func avClampFPS(fps float64) float64 {
	if math.IsNaN(fps) || fps < avSyncMinFPS || fps > avSyncMaxFPS {
		return 30
	}
	return fps
}

// avRateArg writes fps for ffmpeg. Six decimals put its rate within a
// millionth of a frame of the one the frames are timed by.
func avRateArg(fps float64) string {
	return strconv.FormatFloat(fps, 'f', 6, 64)
}

// avVideoFilters is the picture decoder's filters: the scale when there is one
// (first, so a repeated frame is not scaled again), then the pinned rate,
// starting at the seek point. A live stream (no duration) is not padded to its
// start: one odd stream (a timed-ID3 track, a timestamp wrap) could make that
// pad any length, played as a frozen picture.
func avVideoFilters(fps float64, scale string, live bool) string {
	var f []string
	if scale != "" {
		f = append(f, scale)
	}
	if !avFiltersOff.Load() {
		if live {
			f = append(f, "fps="+avRateArg(fps))
		} else {
			f = append(f, "fps="+avRateArg(fps)+":start_time=0")
		}
	}
	return strings.Join(f, ",")
}

// avAudioFilter is the sound decoder's filter: silence up to the seek point
// (not for a live stream, as above) and over any gap in the stream, so its
// samples line up with the picture's frames. "" when the filters are off.
func avAudioFilter(live bool) string {
	switch {
	case avFiltersOff.Load():
		return ""
	case live:
		return "aresample=async=1"
	}
	return "aresample=async=1:first_pts=0"
}

// avFilterMissing reports whether ffmpeg's stderr says a filter (or an option
// of one) used here does not exist in this build.
func avFilterMissing(stderr string) bool {
	if strings.Contains(stderr, "No such filter") {
		return true
	}
	if !strings.Contains(stderr, "not found") {
		return false
	}
	for _, opt := range []string{"'start_time'", "'first_pts'", "'async'"} {
		if strings.Contains(stderr, opt) {
			return true
		}
	}
	return false
}

// avNoteFilterMissing turns the filters off for good when stderr, from a run
// that used them (filtered), says they are missing, and reports whether it did:
// the caller then tries once more without them. The picture and the sound
// decoders both fail that way at once, so each one's answer is its own.
func avNoteFilterMissing(stderr string, filtered bool) bool {
	if !filtered || !avFilterMissing(stderr) {
		return false
	}
	if !avFiltersOff.Swap(true) {
		logWarn("video", "this ffmpeg has no fps/aresample filter; playing without them (sync may drift)")
	}
	return true
}

// avOutputDelay is the time between the device taking a buffer and the
// listener hearing it. On a headset, chunk is still the video device's own
// buffer size, not the headset's: close enough for an estimate.
func avOutputDelay(chunk time.Duration) time.Duration {
	outAudio.mu.Lock()
	mixed := outAudio.mixDev != 0
	outAudio.mu.Unlock()
	if mixed {
		return chunk + outMixLead + avSyncHeadsetDelay
	}
	return chunk
}

// awaitAudio holds the first frame until the sound has started, its decoder
// gave up, or avSyncAudioWait passed.
func (c *avSync) awaitAudio(ctx context.Context) {
	deadline := time.Now().Add(avSyncAudioWait)
	for !c.audioStarted.Load() && !c.audioDone.Load() && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// start puts media time 0 at now: the first frame shows now.
func (c *avSync) start(now time.Time) {
	c.origin, c.lastMove, c.lastAdjust = now, now, now
}

// paused moves the clock past a pause of d.
func (c *avSync) paused(d time.Duration) {
	c.origin = c.origin.Add(d)
	c.lastMove = c.lastMove.Add(d)
	c.lastAdjust = c.lastAdjust.Add(d)
}

// audioSec is how many seconds of this session's sound have been heard, and
// whether the sound leads the clock now.
func (c *avSync) audioSec(now time.Time) (float64, bool) {
	if !c.audioStarted.Load() {
		return 0, false
	}
	consumed, skipped, queued, bps, chunk, ok := videoAudioClock()
	if !ok || bps <= 0 {
		return 0, false
	}
	// Only the device taking data counts as the sound moving on: a skipped
	// buffer moves the position, not the device.
	if consumed != c.lastConsumed {
		c.lastConsumed = consumed
		c.lastMove = now
	}
	if c.audioDone.Load() && queued == 0 {
		return 0, false // the sound is over; the picture plays on alone
	}
	if now.Sub(c.lastMove) > avSyncAudioStall {
		return 0, false
	}
	pos := consumed + skipped - c.mark.Load()
	if pos < 0 {
		pos = 0
	}
	return float64(pos)/bps - avOutputDelay(chunk).Seconds(), true
}

// now is the media time (seconds since the session's start position) the
// picture should be at.
func (c *avSync) now(t time.Time) float64 {
	clock := t.Sub(c.origin).Seconds()
	a, ok := c.audioSec(t)
	if !ok {
		c.following = false
		return clock
	}
	diff := a - clock
	if c.following && math.Abs(diff) <= avSyncSnap {
		// A share of the error for the time since the last pull, so the
		// rate does not depend on how often the clock is read.
		k := t.Sub(c.lastAdjust).Seconds() / avSyncSlewTau
		if k > 1 {
			k = 1
		}
		diff *= k
	}
	c.lastAdjust = t
	c.origin = c.origin.Add(-time.Duration(diff * float64(time.Second)))
	c.following = true
	return t.Sub(c.origin).Seconds()
}

// wait sleeps until frame time due (seconds since the start position), and
// returns at once for a frame that is late. paused is polled while it waits; a
// pause moves the clock past itself. It reports false when ctx ended.
func (c *avSync) wait(ctx context.Context, due float64, paused func() bool) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		if paused() {
			from := time.Now()
			for paused() {
				if ctx.Err() != nil {
					return false
				}
				time.Sleep(15 * time.Millisecond)
			}
			c.paused(time.Since(from))
			continue
		}
		now := time.Now()
		ahead := due - c.now(now)
		if ahead <= 0.002 {
			if !c.following && -ahead > avSyncWallLate {
				// No sound to catch up with: carry on from here.
				c.origin = now.Add(-time.Duration(due * float64(time.Second)))
			}
			return true
		}
		sleep := time.Duration(ahead * float64(time.Second))
		if sleep > 30*time.Millisecond {
			sleep = 30 * time.Millisecond
		}
		time.Sleep(sleep)
	}
}
