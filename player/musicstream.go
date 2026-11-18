package main

// musicstream.go — how the music player tells a radio or podcast stream that
// went quiet from one that ended (backlog MED-36).
//
// musicFFmpegArgs gives an http(s) input the video decoders' read timeout and
// reconnect (ffmpegStreamInputArgs, videorecover.go). When the input then stays
// quiet, ffmpeg gives up on it — and exits cleanly, as at the end of a file. So
// the decoder loop measures how long its reads waited and keeps ffmpeg's last
// stderr lines, and mediaDecoderStalled decides exactly as it does for a
// video: a stall ends as "Playback stopped early — press Play to resume" (the
// cut-short path of musicDecodeEnded) instead of silence, or of counting as
// the end of the episode. An HLS stream hides a dead network from ffmpeg's own
// timeouts; the video decoder's read watch (mediaReadWatch) stops that one
// after mediaStallGiveUp.

import (
	"os/exec"
	"time"
)

// musicStreamWatch is one music decoder's stall bookkeeping. A nil one (a file
// on the card) does nothing. Its methods run on the decoder's goroutine; the
// read watch has its own.
type musicStreamWatch struct {
	cmd    *exec.Cmd
	tail   *lockedTailBuffer
	watch  *mediaReadWatch
	readAt time.Time
	// lastWait and prevWait are how long the last two reads waited. ffmpeg
	// writes what it still holds when its input gives up and then closes the
	// pipe, so the long wait can be the read before the one that found it
	// closed.
	lastWait, prevWait time.Duration
}

// musicNewStreamWatch prepares cmd (not started yet) for an http(s) path: its
// stderr goes to errors.txt, as the video decoders' does, and its last lines are
// kept. nil for anything else.
func musicNewStreamWatch(path string, cmd *exec.Cmd) *musicStreamWatch {
	if cmd == nil || !isHTTPMediaURL(path) {
		return nil
	}
	s := &musicStreamWatch{cmd: cmd, tail: &lockedTailBuffer{max: 8 << 10}}
	cmd.Stderr = ffmpegStderrSink{tail: s.tail}
	// With the stderr copy, Wait also waits for that pipe; a child that left it
	// open must not hold the decoder's goroutine for good.
	cmd.WaitDelay = 2 * time.Second
	return s
}

// start arms the read watch once ffmpeg runs.
func (s *musicStreamWatch) start() {
	if s == nil || s.cmd.Process == nil {
		return
	}
	proc := s.cmd.Process
	s.watch = startMediaReadWatch(nil, nil, musicIsPaused, func() { _ = proc.Kill() })
}

// begin and end bracket one pipe read.
func (s *musicStreamWatch) begin() {
	if s == nil {
		return
	}
	s.readAt = time.Now()
	s.watch.begin()
}

func (s *musicStreamWatch) end() {
	if s == nil {
		return
	}
	s.watch.end()
	s.prevWait, s.lastWait = s.lastWait, time.Since(s.readAt)
}

// stop ends the read watch; the decoder calls it when its loop is over.
func (s *musicStreamWatch) stop() {
	if s != nil {
		s.watch.Stop()
	}
}

// stalled reports, once ffmpeg is reaped, whether the stream of generation gen
// went quiet rather than ended, and logs why. A decoder that never played a
// sample is not a stall (musicDecodeEnded counts it as a failed track), nor is
// one the user stopped or replaced.
func (s *musicStreamWatch) stalled(gen int) bool {
	if s == nil {
		return false
	}
	music.mu.Lock()
	current := gen == music.gen
	played := music.pcmBytes > 0
	position, duration := music.position, music.duration
	music.mu.Unlock()
	if !current || !played {
		return false
	}
	wait := max(s.lastWait, s.prevWait)
	stderr := s.tail.String()
	fired := s.watch.Fired()
	if !mediaDecoderStalled(1, true, false, position, duration, wait, stderr, fired) {
		return false
	}
	if fired {
		logWarn("musicplayer", "stream stalled at %.0f s: no sound for %.0f s, the decoder was stopped", position, wait.Seconds())
	} else {
		logWarn("musicplayer", "stream stalled at %.0f s: %s", position, mediaStallReason(stderr))
	}
	return true
}

// musicIsPaused reports the player paused (the read watch's pause test).
func musicIsPaused() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.paused
}
