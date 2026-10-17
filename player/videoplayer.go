package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// VideoPlayerState holds the embedded video player runtime state.
type VideoPlayerState struct {
	mu                  sync.Mutex
	phase               string // idle | downloading | playing | paused | error
	progress            float64
	progressBase        float64
	volume              float64
	speed               float64
	current             *sdl.Texture
	currentRaw          []byte
	frameSeq            uint64 // incremented by the decoder for every new frame
	uploadedSeq         uint64 // last frame seq copied into the texture
	width               int32
	height              int32
	lastErr             string
	duration            float64
	frameRate           float64
	lastFrameTime       time.Time
	decoderStartTime    time.Time
	pauseStartTime      time.Time
	totalPausedDuration float64
	isDragging          bool
	dragStartX          int32
}

// Global embedded video player state
var embeddedPlayer = &VideoPlayerState{phase: "idle"}

// lastResumeSaveTicks throttles periodic resume-position writes so the recent
// store survives a crash without writing to disk every frame.
var lastResumeSaveTicks uint64

var videoTextureLogCount int

// Video frame queue for decoder → renderer handoff.
var videoFrameQueue chan *sdl.Texture
var videoFrameQueueSize = 4

// Control button hitboxes for the video overlay
var videoControlRects = struct {
	playPause   sdl.Rect
	overlayPlay sdl.Rect
	stop        sdl.Rect
	seekBack    sdl.Rect
	seekFwd     sdl.Rect
	volDown     sdl.Rect
	volUp       sdl.Rect
	progress    sdl.Rect
	speed       sdl.Rect
	speedCycle  sdl.Rect
	back        sdl.Rect
}{}

// A playback session owns one decode pipeline. Every start, seek or restart
// gets a new generation, and a stopped or superseded session can never touch
// the processes or UI state of a newer one. Without this, the goroutine for a
// finished video called StopEmbeddedPlayback() a moment later, tearing down
// the video that had just been started (the "video closes after a second"
// bug), and seeking killed the restarted pipeline the same way.
type playbackSession struct {
	id       uint64
	source   string // file path or URL being decoded
	pageURL  string // page the user opened (used for resume positions)
	tempFile bool   // downloaded copy owned by playWithTempFile

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	videoCmd *exec.Cmd
	audioCmd *exec.Cmd
}

var (
	playbackMu      sync.Mutex
	playbackCurrent *playbackSession
	playbackSeq     uint64
)

// beginPlaybackSession supersedes the running pipeline (if any) and returns the
// new one. The previous session is stopped, and its downloaded copy removed
// unless the new playback uses the same file.
func beginPlaybackSession(source, pageURL string, tempFile bool) *playbackSession {
	persistSessionProgress(playbackCurrentSession())

	playbackMu.Lock()
	prev := playbackCurrent
	playbackSeq++
	s := &playbackSession{id: playbackSeq, source: source, pageURL: pageURL, tempFile: tempFile}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	playbackCurrent = s
	playbackMu.Unlock()

	if prev != nil {
		prev.stop()
		if prev.tempFile && !samePlaybackSource(prev.source, source) {
			removeTempVideo(prev.source)
		}
	}
	return s
}

func playbackCurrentSession() *playbackSession {
	playbackMu.Lock()
	defer playbackMu.Unlock()
	return playbackCurrent
}

func samePlaybackSource(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
	}
	return strings.EqualFold(a, b)
}

// isCurrent reports whether this session is still the one on screen.
func (s *playbackSession) isCurrent() bool {
	if s == nil {
		return false
	}
	playbackMu.Lock()
	defer playbackMu.Unlock()
	return playbackCurrent == s
}

func (s *playbackSession) registerVideoCmd(cmd *exec.Cmd) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return false
	}
	s.videoCmd = cmd
	return true
}

func (s *playbackSession) registerAudioCmd(cmd *exec.Cmd) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return false
	}
	s.audioCmd = cmd
	return true
}

func (s *playbackSession) clearVideoCmd(cmd *exec.Cmd) {
	s.mu.Lock()
	if s.videoCmd == cmd {
		s.videoCmd = nil
	}
	s.mu.Unlock()
}

func (s *playbackSession) clearAudioCmd(cmd *exec.Cmd) {
	s.mu.Lock()
	if s.audioCmd == cmd {
		s.audioCmd = nil
	}
	s.mu.Unlock()
}

// stop cancels the session and kills its processes. The goroutines that own the
// commands call Wait exactly once, so stop never calls it (a double Wait would
// race with the decoder's own defer).
func (s *playbackSession) stop() {
	if s == nil {
		return
	}
	s.cancel()
	s.mu.Lock()
	vc, ac := s.videoCmd, s.audioCmd
	s.videoCmd, s.audioCmd = nil, nil
	s.mu.Unlock()
	if vc != nil && vc.Process != nil {
		vc.Process.Kill()
	}
	if ac != nil && ac.Process != nil {
		ac.Process.Kill()
	}
}

// finish is called by the pipeline goroutine when decoding ends. It only resets
// the UI when this session is still the active one; a superseded session must
// leave the newer playback untouched.
func (s *playbackSession) finish() {
	if !s.isCurrent() {
		return
	}
	StopEmbeddedPlayback()
}

// persistSessionProgress stores the resume position of a session before it is
// replaced. It uses the session's own page URL so a new video never overwrites
// the previous video's position.
func persistSessionProgress(s *playbackSession) {
	if s == nil || s.pageURL == "" {
		return
	}
	embeddedPlayer.mu.Lock()
	phase := embeddedPlayer.phase
	progress := embeddedPlayer.progress
	duration := embeddedPlayer.duration
	embeddedPlayer.mu.Unlock()
	if phase == "idle" || duration <= 0 {
		return
	}
	updateRecentPosition(s.pageURL, progress*duration)
	saveFavorites()
}

// removeTempVideo deletes a downloaded playback copy. Only files created by
// playWithTempFile are touched: stopping a video played from the SD card used
// to delete the user's own media file.
func removeTempVideo(path string) {
	if path == "" {
		return
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "jukahub-video-") || !strings.HasSuffix(strings.ToLower(base), ".mp4") {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[VIDEO] could not remove temp download: %v", err)
	}
}

// playerPhase returns the current phase under the state lock.
func playerPhase() string {
	embeddedPlayer.mu.Lock()
	defer embeddedPlayer.mu.Unlock()
	return embeddedPlayer.phase
}

func ensureVideoFrameQueue() {
	if videoFrameQueue == nil {
		videoFrameQueue = make(chan *sdl.Texture, videoFrameQueueSize)
	}
}

func clearVideoFrameQueue() {
	if videoFrameQueue != nil {
		for len(videoFrameQueue) > 0 {
			tex := <-videoFrameQueue
			if tex != nil {
				tex.Destroy()
			}
		}
	}
}

// clampPosInt keeps a probed frame dimension inside a sane range so a bad or
// hostile ffprobe header can never allocate a giant texture.
func clampPosInt(v int64) int {
	const (
		maxVideoDimension = 16384
	)
	if v < 1 {
		return 1
	}
	if v > maxVideoDimension {
		return maxVideoDimension
	}
	return int(v)
}

func probeVideoInfo(ffprobePath, path string) (int32, int32, float64, float64) {
	if ffprobePath == "" {
		log.Printf("[VIDEO] ffprobe missing; using default 1280x720@30 probe values")
		return 1280, 720, 0, 30
	}
	cmd := exec.Command(ffprobePath, "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate,avg_frame_rate,duration",
		"-of", "default=noprint_wrappers=1", path)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[VIDEO] ffprobe failed for %s: %v", sanitizeLogURL(path), err)
		return 1280, 720, 0, 30
	}
	s := strings.TrimSpace(string(out))
	w, h, dur, fps := int32(1280), int32(720), 0.0, 30.0
	avgFps := 0.0
	parseRate := func(v string) float64 {
		parts := strings.Split(strings.TrimSpace(v), "/")
		if len(parts) == 2 {
			num, _ := strconv.ParseFloat(parts[0], 64)
			den, _ := strconv.ParseFloat(parts[1], 64)
			if den > 0 && num > 0 {
				return num / den
			}
		}
		return 0
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "width=") {
			raw := strings.TrimPrefix(line, "width=")
			if v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32); err == nil && v > 0 {
				w = int32(clampPosInt(v))
			}
		} else if strings.HasPrefix(line, "height=") {
			raw := strings.TrimPrefix(line, "height=")
			if v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32); err == nil && v > 0 {
				h = int32(clampPosInt(v))
			}
		} else if strings.HasPrefix(line, "duration=") {
			raw := strings.TrimPrefix(line, "duration=")
			if v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && v > 0 {
				dur = v
			}
		} else if strings.HasPrefix(line, "r_frame_rate=") {
			if v := parseRate(strings.TrimPrefix(line, "r_frame_rate=")); v > 0 {
				fps = v
			}
		} else if strings.HasPrefix(line, "avg_frame_rate=") {
			if v := parseRate(strings.TrimPrefix(line, "avg_frame_rate=")); v > 0 {
				avgFps = v
			}
		}
	}
	// avg_frame_rate reflects the true playback rate for VFR content, whereas
	// r_frame_rate can be a nominal maximum that makes playback run too fast.
	if avgFps > 0 {
		fps = avgFps
	}
	log.Printf("[VIDEO] ffprobe result: %dx%d, dur=%.1f, fps=%.1f", w, h, dur, fps)
	return w, h, dur, fps
}

func startFFmpegDecoder(sess *playbackSession, ffmpegPath, path string, width, height int32, startSec float64) {
	// No -re flag: wall-clock frame pacing below handles real-time output.
	// -re interacts badly with pipe output on ARM64 Trimui builds.
	args := []string{}
	if startSec > 0 {
		// Fast (keyframe-accurate) seek before the input so resume starts at
		// the saved position instead of the beginning.
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSec))
	}
	args = append(args, "-i", path,
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-",
	)
	cmd := exec.CommandContext(sess.ctx, ffmpegPath, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[VIDEO] ffmpeg stdout pipe failed: %v", err)
		return
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Printf("[VIDEO] ffmpeg failed to start: %v", err)
		return
	}
	if !sess.registerVideoCmd(cmd) {
		cmd.Wait()
		return
	}
	defer sess.clearVideoCmd(cmd)
	defer cmd.Wait()

	frameSize := int(width * height * 4)
	if frameSize <= 0 {
		return
	}
	buf := make([]byte, frameSize)
	frameCount := 0
	// Compute the target inter-frame duration from the detected fps.
	// On ARM64 Trimui the -re flag may not pace reliably, so we do
	// explicit wall-clock pacing as a safety net.
	embeddedPlayer.mu.Lock()
	fps := embeddedPlayer.frameRate
	embeddedPlayer.mu.Unlock()
	if fps <= 0 {
		fps = 30
	}
	frameDuration := time.Duration(float64(time.Second) / fps)
	frameDeadline := time.Now()
	wasPaused := false

	for {
		select {
		case <-sess.ctx.Done():
			return
		default:
		}
		// True pause: while paused, stop consuming the pipe so ffmpeg blocks
		// and the video source stands still; on resume it continues exactly
		// where it left off, keeping A/V in sync with the audio.
		embeddedPlayer.mu.Lock()
		paused := embeddedPlayer.phase == "paused"
		embeddedPlayer.mu.Unlock()
		if paused {
			wasPaused = true
			time.Sleep(15 * time.Millisecond)
			continue
		}
		if wasPaused {
			// Resume from now: otherwise the deadline is far in the past and
			// the decoder bursts through a second of frames to catch up.
			frameDeadline = time.Now().Add(frameDuration)
			wasPaused = false
		}
		if _, err := io.ReadFull(pipe, buf); err != nil {
			break
		}
		frameCount++

		// Reuse the frame buffer instead of allocating 3-8 MB per frame; this
		// keeps the GC from stuttering playback. The upload path holds the same
		// mutex while copying into the texture, so no torn frames.
		embeddedPlayer.mu.Lock()
		if cap(embeddedPlayer.currentRaw) < len(buf) {
			embeddedPlayer.currentRaw = make([]byte, len(buf))
		}
		raw := embeddedPlayer.currentRaw[:len(buf)]
		copy(raw, buf)
		embeddedPlayer.currentRaw = raw
		embeddedPlayer.width = width
		embeddedPlayer.height = height
		embeddedPlayer.lastFrameTime = time.Now()
		embeddedPlayer.frameSeq++
		embeddedPlayer.mu.Unlock()

		// Pace to exactly 1x speed: sleep until the next frame is due.
		// This prevents the decoder from running ahead of real-time on
		// fast machines and provides a consistent floor on slow ones.
		if frameCount > 1 {
			frameDeadline = frameDeadline.Add(frameDuration)
			wait := time.Until(frameDeadline)
			if wait > 0 {
				time.Sleep(wait)
			} else if -wait > 250*time.Millisecond {
				// Fell far behind (slow decode or a stall). Resync the clock
				// instead of playing a burst of frames to catch up.
				frameDeadline = time.Now()
			}
		} else {
			frameDeadline = time.Now().Add(frameDuration)
			log.Printf("[VIDEO] First frame received: %dx%d, %d bytes, fps=%.1f, frameDur=%v", width, height, len(buf), fps, frameDuration)
		}
	}
}

func startFFmpegAudio(sess *playbackSession, ffmpegPath, path string, startSec float64) {
	// Match the exact sample rate the SDL device obtained so there is
	// zero resampling and perfect pitch. No -re: queue backpressure
	// paces the decoder to real-time.
	rate := GetVideoAudioRate()
	args := []string{}
	if startSec > 0 {
		// Fast seek before the input so resume/seek starts at the right
		// audio position instead of the beginning.
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSec))
	}
	args = append(args, "-i", path,
		"-f", "s16le",
		"-ac", "2",
		"-ar", fmt.Sprintf("%d", rate),
		"-",
	)
	cmd := exec.CommandContext(sess.ctx, ffmpegPath, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[AUDIO] ffmpeg audio pipe failed: %v", err)
		return
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Printf("[AUDIO] ffmpeg audio failed to start: %v", err)
		return
	}
	if !sess.registerAudioCmd(cmd) {
		cmd.Wait()
		return
	}
	defer sess.clearAudioCmd(cmd)
	defer cmd.Wait()

	// Feed decoded s16le stereo PCM straight to the dedicated video audio
	// device (same format, no resampling). The queue helper blocks when the
	// device is ~0.75 s behind, which paces ffmpeg to roughly realtime.
	const audioBufferSize = 8192
	buf := make([]byte, audioBufferSize)

	for {
		select {
		case <-sess.ctx.Done():
			return
		default:
		}
		embeddedPlayer.mu.Lock()
		vol := embeddedPlayer.volume
		paused := embeddedPlayer.phase == "paused"
		embeddedPlayer.mu.Unlock()
		// While paused, stop consuming the pipe (ffmpeg blocks) and stop
		// queueing; the device drains and goes quiet. On resume both continue
		// from the pause point so sound stays in sync with the picture.
		if paused {
			time.Sleep(15 * time.Millisecond)
			continue
		}
		n, err := io.ReadFull(pipe, buf)
		if n > 0 {
			QueueVideoAudio(buf[:n], vol)
		}
		if err != nil {
			break
		}
	}
}

// UpdateCurrentTexture uploads the current raw RGBA frame to an SDL2 texture.
// The copy runs under the state lock: the decoder writes the same buffer, and
// uploading it without the lock produced torn frames (the flicker/glitch users
// saw). Uploading only when frameSeq changed also avoids redundant work when
// the render loop runs faster than the video.
func UpdateCurrentTexture(renderer *sdl.Renderer) {
	if renderer == nil {
		return
	}
	embeddedPlayer.mu.Lock()
	defer embeddedPlayer.mu.Unlock()

	raw := embeddedPlayer.currentRaw
	w := embeddedPlayer.width
	h := embeddedPlayer.height
	if raw == nil || w <= 0 || h <= 0 || len(raw) != int(w*h*4) {
		return
	}
	if embeddedPlayer.phase == "paused" {
		return
	}
	if embeddedPlayer.frameSeq == embeddedPlayer.uploadedSeq && embeddedPlayer.current != nil {
		return
	}

	if embeddedPlayer.current == nil {
		// ffmpeg's "rgba" pixel format writes bytes in memory order R,G,B,A.
		// SDL_PIXELFORMAT_RGBA32 is the native-endian RGBA format: on the
		// little-endian hardware this project targets (TrimUI Smart Pro,
		// x86/ARM desktops) it is ABGR8888, whose memory order is R,G,B,A —
		// exactly what ffmpeg emits. Plain RGBA8888 would expect B,G,R,A in
		// memory, putting the alpha byte (255) in the red channel and making
		// the whole frame render red.
		tex, err := renderer.CreateTexture(uint32(sdl.PIXELFORMAT_RGBA32), sdl.TEXTUREACCESS_STREAMING, w, h)
		if err != nil {
			log.Printf("[VIDEO] CreateTexture failed: %v (w=%d h=%d)", err, w, h)
			return
		}
		embeddedPlayer.current = tex
		log.Printf("[VIDEO] Created texture %dx%d", w, h)
	}

	tex := embeddedPlayer.current
	if err := tex.Update(nil, unsafe.Pointer(&raw[0]), int(w)*4); err != nil {
		log.Printf("[VIDEO] Update texture failed: %v", err)
		tex.Destroy()
		embeddedPlayer.current = nil
		return
	}
	embeddedPlayer.uploadedSeq = embeddedPlayer.frameSeq
}

// StartEmbeddedPlayback begins the embedded FFmpeg decoder pipeline for the
// given URL from the beginning.
func StartEmbeddedPlayback(config *Config, url string) {
	StartEmbeddedPlaybackAt(config, url, 0)
}

// StartEmbeddedPlaybackAt begins the embedded FFmpeg decoder pipeline for the
// given URL, seeking to startSec when nonzero so the Continue card can resume.
func StartEmbeddedPlaybackAt(config *Config, url string, startSec float64) {
	startEmbeddedPlayback(config, url, startSec, false)
}

// startEmbeddedPlayback starts a new session. tempFile marks a downloaded copy
// that this session owns and may delete when it ends.
func startEmbeddedPlayback(config *Config, url string, startSec float64, tempFile bool) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	pageURL := currentPlaybackURL
	if pageURL == "" {
		pageURL = url
	}
	sess := beginPlaybackSession(url, pageURL, tempFile)

	videoPlaybackMutex.Lock()
	embeddedPlaybackPath = url
	videoPlaybackMutex.Unlock()
	ensureVideoFrameQueue()

	embeddedPlayer.mu.Lock()
	embeddedPlayer.phase = "downloading"
	embeddedPlayer.progress = 0
	embeddedPlayer.progressBase = 0
	embeddedPlayer.volume = 1.0
	embeddedPlayer.speed = 1.0
	embeddedPlayer.lastErr = ""
	embeddedPlayer.duration = 0
	embeddedPlayer.currentRaw = nil
	embeddedPlayer.frameSeq = 0
	embeddedPlayer.uploadedSeq = 0
	embeddedPlayer.totalPausedDuration = 0
	embeddedPlayer.pauseStartTime = time.Time{}
	embeddedPlayer.mu.Unlock()

	go func() {
		ffmpegPath := getToolPath("ffmpeg", config)
		ffprobePath := getToolPath("ffprobe", config)

		w, h, dur, fps := probeVideoInfo(ffprobePath, url)
		if !sess.isCurrent() {
			// A newer session (seek, restart or stop) took over while probing:
			// do not touch its state or start orphan processes.
			return
		}

		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "playing"
		videoPlaybackPhaseAt = sdl.GetTicks64()
		videoPlaybackProgress = 1.0
		videoPlaybackMutex.Unlock()

		progressBase := 0.0
		if startSec > 0 && dur > 0 {
			progressBase = startSec / dur
			if progressBase < 0 {
				progressBase = 0
			}
			if progressBase > 1 {
				progressBase = 1
			}
		}

		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = "playing"
		embeddedPlayer.width = w
		embeddedPlayer.height = h
		embeddedPlayer.duration = dur
		embeddedPlayer.frameRate = fps
		embeddedPlayer.decoderStartTime = time.Now()
		embeddedPlayer.progressBase = progressBase
		embeddedPlayer.progress = progressBase
		embeddedPlayer.mu.Unlock()

		// Audio and video are separate blocking decode loops. The audio
		// pipeline runs on its own goroutine — calling it inline here would
		// block until the audio ends and the video decoder would never start.
		go startFFmpegAudio(sess, ffmpegPath, url, startSec)
		startFFmpegDecoder(sess, ffmpegPath, url, w, h, startSec)

		// The pipeline ended (EOF, stop, or a newer session). finish() only
		// resets the UI when this session still owns the screen, so a stale
		// decoder can never tear down the video the user is watching.
		sess.finish()
	}()
}

// StopEmbeddedPlayback halts the embedded decoder and clears state.
func StopEmbeddedPlayback() {
	playbackMu.Lock()
	sess := playbackCurrent
	playbackCurrent = nil
	playbackMu.Unlock()

	if sess != nil {
		persistSessionProgress(sess)
		sess.stop()
		if sess.tempFile {
			removeTempVideo(sess.source)
		}
	}
	// Drop any audio still queued from the stopped video so it never bleeds
	// into the next playback.
	ClearVideoAudioQueue()

	embeddedPlayer.mu.Lock()
	prevPhase := embeddedPlayer.phase
	embeddedPlayer.phase = "idle"
	embeddedPlayer.currentRaw = nil
	embeddedPlayer.frameSeq = 0
	embeddedPlayer.uploadedSeq = 0
	embeddedPlayer.mu.Unlock()

	if prevPhase != "idle" {
		clearVideoFrameQueue()
	}

	currentPlaybackURL = ""

	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "idle"
	videoPlaybackPhaseAt = sdl.GetTicks64()
	videoPlaybackProgress = 0
	videoPlaybackCmd = nil
	embeddedPlaybackPath = ""
	videoPlaybackMutex.Unlock()
}

// stopVideoPlayback is the unexported alias used by main.go menu buttons.
func stopVideoPlayback() {
	StopEmbeddedPlayback()
}

// ToggleVideoPlayback switches between playing and paused.
func ToggleVideoPlayback() {
	embeddedPlayer.mu.Lock()
	defer embeddedPlayer.mu.Unlock()

	switch embeddedPlayer.phase {
	case "playing":
		embeddedPlayer.phase = "paused"
		embeddedPlayer.pauseStartTime = time.Now()
		PauseVideoAudio(true)
	case "paused":
		embeddedPlayer.totalPausedDuration += time.Since(embeddedPlayer.pauseStartTime).Seconds()
		embeddedPlayer.pauseStartTime = time.Time{}
		embeddedPlayer.phase = "playing"
		PauseVideoAudio(false)
	}
}

// toggleVideoPlayback is the unexported alias used by main.go menu buttons.
func toggleVideoPlayback() {
	ToggleVideoPlayback()
}

// SeekVideo seeks to the given fraction (0..1) by restarting the pipeline with
// -ss. The seek is a new session: the old decoder is superseded, so when its
// loop exits it cannot stop the restarted playback (which used to make seeking
// end the video a second later).
func SeekVideo(fraction float64) {
	prev := playbackCurrentSession()
	if prev == nil {
		return
	}

	embeddedPlayer.mu.Lock()
	if embeddedPlayer.phase == "idle" {
		embeddedPlayer.mu.Unlock()
		return
	}
	width := embeddedPlayer.width
	height := embeddedPlayer.height
	duration := embeddedPlayer.duration
	embeddedPlayer.mu.Unlock()

	if width <= 0 || height <= 0 || duration <= 0 || appConfig == nil {
		return
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}

	ffmpegPath := getToolPath("ffmpeg", appConfig)
	if ffmpegPath == "" {
		return
	}

	seekSec := fraction * duration
	next := beginPlaybackSession(prev.source, prev.pageURL, prev.tempFile)

	embeddedPlayer.mu.Lock()
	embeddedPlayer.phase = "playing"
	embeddedPlayer.decoderStartTime = time.Now()
	embeddedPlayer.pauseStartTime = time.Time{}
	embeddedPlayer.totalPausedDuration = 0
	embeddedPlayer.progressBase = fraction
	embeddedPlayer.progress = fraction
	embeddedPlayer.mu.Unlock()

	// The restarted decoders must not run on the caller's goroutine: SeekVideo
	// is invoked from the render loop, and decoding inline froze the whole UI
	// (no overlay, no input) for the rest of the video.
	go func() {
		// Restart audio at the seek position so sound stays in sync with the
		// restarted video decoder.
		go startFFmpegAudio(next, ffmpegPath, prev.source, seekSec)
		startFFmpegDecoder(next, ffmpegPath, prev.source, width, height, seekSec)
		next.finish()
	}()
}

// AdjustVolume changes volume by delta and clamps to 0..1.
func AdjustVolume(delta float64) {
	embeddedPlayer.mu.Lock()
	defer embeddedPlayer.mu.Unlock()
	embeddedPlayer.volume += delta
	if embeddedPlayer.volume < 0 {
		embeddedPlayer.volume = 0
	}
	if embeddedPlayer.volume > 1 {
		embeddedPlayer.volume = 1
	}
}

// SetPlaybackSpeed sets the playback speed (0.5, 1.0, 1.5, 2.0).
func SetPlaybackSpeed(speed float64) {
	embeddedPlayer.mu.Lock()
	defer embeddedPlayer.mu.Unlock()
	if speed < 0.25 {
		speed = 0.25
	}
	if speed > 4.0 {
		speed = 4.0
	}
	embeddedPlayer.speed = speed
}

// GetEmbeddedPlayerState returns a borrowed pointer to the current player
// state. The caller must not mutate fields without holding embeddedPlayer.mu.
func GetEmbeddedPlayerState() *VideoPlayerState {
	return embeddedPlayer
}

// videoStateSnapshot is a consistent copy of the player state for the render
// and input paths. Reading the live fields directly raced with the decoder:
// a half-updated progress/phase made the overlay flicker and the seek bar jump.
type videoStateSnapshot struct {
	phase               string
	progress            float64
	progressBase        float64
	duration            float64
	volume              float64
	speed               float64
	width               int32
	height              int32
	hasRaw              bool
	decoderStartTime    time.Time
	pauseStartTime      time.Time
	totalPausedDuration float64
}

func snapshotVideoState() videoStateSnapshot {
	embeddedPlayer.mu.Lock()
	defer embeddedPlayer.mu.Unlock()
	return videoStateSnapshot{
		phase:               embeddedPlayer.phase,
		progress:            embeddedPlayer.progress,
		progressBase:        embeddedPlayer.progressBase,
		duration:            embeddedPlayer.duration,
		volume:              embeddedPlayer.volume,
		speed:               embeddedPlayer.speed,
		width:               embeddedPlayer.width,
		height:              embeddedPlayer.height,
		hasRaw:              embeddedPlayer.currentRaw != nil,
		decoderStartTime:    embeddedPlayer.decoderStartTime,
		pauseStartTime:      embeddedPlayer.pauseStartTime,
		totalPausedDuration: embeddedPlayer.totalPausedDuration,
	}
}

// formatTime converts seconds to MM:SS.
func formatTime(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	m := int(seconds) / 60
	s := int(seconds) % 60
	return pad2(m) + ":" + pad2(s)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// RenderVideoOverlay renders the current video frame and on-screen controls.
func RenderVideoOverlay(renderer *sdl.Renderer, config *Config, elem Element) {
	state := snapshotVideoState()
	if state.phase == "idle" {
		return
	}

	// Update playback progress based on elapsed time
	if state.duration > 0 && state.phase == "playing" {
		elapsed := time.Since(state.decoderStartTime).Seconds()
		pausedTime := state.totalPausedDuration
		if !state.pauseStartTime.IsZero() {
			pausedTime += time.Since(state.pauseStartTime).Seconds()
		}
		effectiveElapsed := elapsed - pausedTime
		if effectiveElapsed < 0 {
			effectiveElapsed = 0
		}
		fraction := state.progressBase + effectiveElapsed/state.duration
		if fraction > 1 {
			fraction = 1
		}
		embeddedPlayer.mu.Lock()
		embeddedPlayer.progress = fraction
		embeddedPlayer.mu.Unlock()
		// Draw this frame with the value just computed instead of the snapshot
		// taken before it, so the bar never lags a frame behind the picture.
		state.progress = fraction

		// Throttled safety net: write the resume position every ~5s so an app
		// crash does not lose the watch position (StopEmbeddedPlayback always
		// writes the final position).
		if now := sdl.GetTicks64(); now-lastResumeSaveTicks > 5000 {
			lastResumeSaveTicks = now
			if src := currentPlaybackURL; src != "" && state.duration > 0 {
				updateRecentPosition(src, fraction*state.duration)
				saveFavorites()
			}
		}
	}

	x, y := elem.X, elem.Y
	w := getElementWidth(elem, screenWidth)
	h := getElementHeight(elem, screenHeight)

	// Upload raw frame to texture if needed
	UpdateCurrentTexture(renderer)

	// Re-read texture pointer after potential creation
	embeddedPlayer.mu.Lock()
	currentTex := embeddedPlayer.current
	embeddedPlayer.mu.Unlock()

	if currentTex == nil && state.phase == "playing" {
		videoTextureLogCount++
		if videoTextureLogCount%120 == 0 {
			log.Printf("[VIDEO] currentTex is nil after UpdateCurrentTexture, raw=%v, w=%d, h=%d", state.hasRaw, state.width, state.height)
		}
	}

	// Video frame area
	if currentTex != nil {
		renderer.Copy(currentTex, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
	} else if state.phase == "downloading" || state.phase == "error" {
		renderer.SetDrawColor(8, 10, 16, 220)
		renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
		if state.phase == "downloading" {
			renderSpinner(renderer, x+w/2, y+h/2, 40, ColorTextPrimary())
		}
	} else {
		renderer.SetDrawColor(8, 10, 16, 220)
		renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	// Use a cool cyan/blue accent for the video player so it never looks red,
	// regardless of the global theme accent color.
	accent := ColorInfo

	// Top / bottom gradients for readability.
	topGradH := int32(70)
	for s := int32(0); s < topGradH; s += 3 {
		a := uint8(clampInt32(90*(10-int32(float64(s)*10.0/float64(topGradH))), 0, 90))
		renderer.SetDrawColor(0, 0, 0, a)
		renderer.FillRect(&sdl.Rect{X: x, Y: y + s, W: w, H: 3})
	}
	bottomGradH := int32(140)
	bottomY := y + h - bottomGradH
	for s := int32(0); s < bottomGradH; s += 3 {
		a := uint8(clampInt32(int32(180.0*float64(s)/float64(bottomGradH)), 0, 180))
		renderer.SetDrawColor(0, 0, 0, a)
		renderer.FillRect(&sdl.Rect{X: x, Y: bottomY + s, W: w, H: 3})
	}

	// Bottom control bar.
	barH := int32(72)
	barY := y + h - barH
	fillRoundedRect(renderer, x+8, barY, w-16, barH, 16, WithAlpha(ColorSurfacePanel, 220))

	// Top-left phase badge.
	phaseText := "PLAYING"
	phaseCol := ColorTextPrimary()
	if state.phase == "paused" {
		phaseText = "PAUSED"
		phaseCol = ColorWarning
	} else if state.phase == "downloading" {
		phaseText = "LOADING"
		phaseCol = ColorInfo
	} else if state.phase == "error" {
		phaseText = "ERROR"
		phaseCol = ColorDanger
	}
	phaseW, _, _ := font.SizeUTF8(phaseText)
	badgeX := x + 16
	badgeY := y + 12
	fillRoundedRect(renderer, badgeX, badgeY, clampInt32(int32(phaseW)+18, 1, 10000), 26, 13, WithAlpha(ColorSurfaceRaised, 230))
	renderText(renderer, config, font, phaseText, phaseCol, badgeX+9, badgeY+6)

	// Top-right: back (SDL-drawn X) and volume text.
	volText := formatPercentSimple(state.volume)
	volW, _, _ := font.SizeUTF8(volText)
	backBtnSize := int32(30)
	backX := x + w - backBtnSize - 18
	fillRoundedRect(renderer, backX, badgeY-2, backBtnSize, backBtnSize, 8, WithAlpha(ColorSurfaceRaised, 180))
	drawCloseIcon(renderer, backX+backBtnSize/2, badgeY-2+backBtnSize/2, 11, ColorTextPrimary())
	videoControlRects.back = sdl.Rect{X: backX, Y: badgeY - 2, W: backBtnSize, H: backBtnSize}
	renderText(renderer, config, font, volText, ColorTextPrimary(), x+w-int32(volW)-58, y+18)

	// Progress bar sits just above the control bar.
	seekBarH := int32(6)
	seekBarY := barY - 16
	seekBarW := w - 48
	seekBarX := x + 24
	fillRoundedRect(renderer, seekBarX, seekBarY, seekBarW, seekBarH, seekBarH/2, sdl.Color{R: 40, G: 40, B: 50, A: 255})
	if state.progress > 0 {
		filledW := int32(float64(seekBarW) * state.progress)
		if filledW > 0 {
			fillRoundedRect(renderer, seekBarX, seekBarY, clampInt32(filledW, 0, seekBarW), seekBarH, seekBarH/2, accent)
		}
	}
	renderer.SetDrawColor(255, 255, 255, 30)
	renderer.FillRect(&sdl.Rect{X: seekBarX, Y: seekBarY, W: seekBarW, H: 1})
	thumbX := seekBarX + int32(float64(seekBarW)*state.progress)
	fillCircle(renderer, thumbX, seekBarY+seekBarH/2, 7, accent)
	videoControlRects.progress = sdl.Rect{X: seekBarX, Y: seekBarY - 8, W: seekBarW, H: 20}

	// Control buttons row, centered within the bar. Icons are SDL-drawn
	// geometry (never font glyphs: Inter has no media/emoji symbols, so
	// text-drawn buttons rendered as blank squares).
	btnSize := int32(36)
	gap := int32(10)
	// Order: stop, seek back, play/pause, seek forward, speed.
	groupW := 5*btnSize + 4*gap
	startX := x + (w-groupW)/2
	btnY := barY + (barH-btnSize)/2

	videoControlRects.stop = drawIconBtn(renderer, iconStop, startX, btnY, btnSize)
	startX += btnSize + gap
	videoControlRects.seekBack = drawIconBtn(renderer, iconSeekBack, startX, btnY, btnSize)
	startX += btnSize + gap
	if state.phase == "paused" {
		videoControlRects.playPause = drawIconBtn(renderer, iconPlay, startX, btnY, btnSize)
	} else {
		videoControlRects.playPause = drawIconBtn(renderer, iconPause, startX, btnY, btnSize)
	}
	startX += btnSize + gap
	videoControlRects.seekFwd = drawIconBtn(renderer, iconSeekFwd, startX, btnY, btnSize)
	startX += btnSize + gap
	speedLabel := fmt.Sprintf("%.1fx", state.speed)
	videoControlRects.speed = drawSpeedBtn(renderer, config, font, speedLabel, startX, btnY, btnSize)
	videoControlRects.speedCycle = videoControlRects.speed

	// Volume +/- on the left of the bar.
	volX := x + 18
	videoControlRects.volDown = drawIconBtn(renderer, iconVolLow, volX, btnY, btnSize)
	videoControlRects.volUp = drawIconBtn(renderer, iconVolHigh, volX+btnSize+6, btnY, btnSize)

	// Time on the right of the bar.
	currentTime := state.progress * state.duration
	timeText := formatTime(currentTime) + " / " + formatTime(state.duration)
	tw, th, _ := font.SizeUTF8(timeText)
	renderText(renderer, config, font, timeText, ColorTextPrimary(), x+w-int32(tw)-18, barY+(barH-int32(th))/2)

	// Big centered play/pause button when paused.
	if state.phase == "paused" {
		overlayBtnSize := int32(80)
		overlayBtnX := x + (w-overlayBtnSize)/2
		overlayBtnY := y + (h-overlayBtnSize)/2
		videoControlRects.overlayPlay = sdl.Rect{X: overlayBtnX, Y: overlayBtnY, W: overlayBtnSize, H: overlayBtnSize}
		fillCircle(renderer, overlayBtnX+overlayBtnSize/2, overlayBtnY+overlayBtnSize/2, overlayBtnSize/2, WithAlpha(ColorSurfacePanel, 200))
		strokeCircle(renderer, overlayBtnX+overlayBtnSize/2, overlayBtnY+overlayBtnSize/2, overlayBtnSize/2, WithAlpha(accent, 180))
		// play triangle
		cx := overlayBtnX + overlayBtnSize/2
		cy := overlayBtnY + overlayBtnSize/2
		s := overlayBtnSize / 5
		renderer.SetDrawColor(accent.R, accent.G, accent.B, accent.A)
		renderer.DrawLine(cx-s, cy-s, cx-s, cy+s)
		renderer.DrawLine(cx-s, cy+s, cx+s-2, cy)
		renderer.DrawLine(cx+s-2, cy, cx-s, cy-s)
	}
}

// playerIcon identifies a geometry-drawn control icon. Icons are rendered with
// SDL primitives only — never text glyphs — so they cannot turn into missing-
// glyph boxes when the font lacks media/emoji symbols.
type playerIcon int

const (
	iconStop playerIcon = iota
	iconSeekBack
	iconPlay
	iconPause
	iconSeekFwd
	iconVolLow
	iconVolHigh
)

// iconCol returns the icon color, brightening for contrast on the dark buttons.
func iconCol() sdl.Color {
	return ColorTextPrimary()
}

// drawIconBtn draws a square control button with a geometric icon and returns
// its rect (mouse hit target).
func drawIconBtn(renderer *sdl.Renderer, icon playerIcon, x, y, size int32) sdl.Rect {
	fillRoundedRect(renderer, x, y, size, size, size/5, WithAlpha(ColorSurfaceRaised, 180))
	renderer.SetDrawColor(255, 255, 255, 25)
	renderer.FillRect(&sdl.Rect{X: x + 3, Y: y + 1, W: size - 6, H: 1})
	cx := x + size/2
	cy := y + size/2
	col := iconCol()
	switch icon {
	case iconStop:
		s := size / 4
		fillRoundedRect(renderer, cx-s, cy-s, s*2, s*2, 2, col)
	case iconPlay:
		s := size / 3
		tri := [3]pt{{x: cx - s + 1, y: cy - s}, {x: cx - s + 1, y: cy + s}, {x: cx + s - 1, y: cy}}
		fillTriangleFilled(renderer, tri[0], tri[1], tri[2], col)
	case iconPause:
		s := size / 4
		barW := s * 3 / 4
		gap := s / 2
		fillRoundedRect(renderer, cx-s-gap/2, cy-s, barW, s*2, 2, col)
		fillRoundedRect(renderer, cx+gap/2, cy-s, barW, s*2, 2, col)
	case iconSeekBack:
		s := size / 3
		barW := s / 3
		barX := cx + s - barW
		fillRoundedRect(renderer, barX, cy-s, barW, s*2, 1, col)
		tri := [3]pt{{x: barX - 1, y: cy - s}, {x: barX - 1, y: cy + s}, {x: barX - s*2, y: cy}}
		fillTriangleFilled(renderer, tri[0], tri[1], tri[2], col)
	case iconSeekFwd:
		s := size / 3
		barW := s / 3
		barX := cx - s
		fillRoundedRect(renderer, barX, cy-s, barW, s*2, 1, col)
		tri := [3]pt{{x: barX + barW + 1, y: cy - s}, {x: barX + barW + 1, y: cy + s}, {x: barX + barW + s*2, y: cy}}
		fillTriangleFilled(renderer, tri[0], tri[1], tri[2], col)
	case iconVolLow:
		drawSpeaker(renderer, cx, cy, size, col, 1)
	case iconVolHigh:
		drawSpeaker(renderer, cx, cy, size, col, 2)
	}
	return sdl.Rect{X: x, Y: y, W: size, H: size}
}

// drawSpeedBtn draws the play-speed button (its label is real text, not a
// symbol, so it renders fine through the font).
func drawSpeedBtn(renderer *sdl.Renderer, config *Config, font *ttf.Font, label string, x, y, size int32) sdl.Rect {
	fillRoundedRect(renderer, x, y, size, size, size/5, WithAlpha(ColorSurfaceRaised, 180))
	renderer.SetDrawColor(255, 255, 255, 25)
	renderer.FillRect(&sdl.Rect{X: x + 3, Y: y + 1, W: size - 6, H: 1})
	if font != nil {
		lw, lh, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, ColorTextPrimary(), x+(size-int32(lw))/2, y+(size-int32(lh))/2)
	}
	return sdl.Rect{X: x, Y: y, W: size, H: size}
}

// drawSpeaker draws a small speaker body (rect + cone) plus n sound-wave arcs.
func drawSpeaker(renderer *sdl.Renderer, cx, cy, size int32, col sdl.Color, arcs int) {
	s := size / 3
	bodyW := s * 3 / 4
	bodyH := s * 3 / 2
	bodyX := cx - s*3/2 + s/3
	fillRoundedRect(renderer, bodyX, cy-bodyH/2, bodyW, bodyH, 2, col)
	// cone
	cone := [3]pt{{x: bodyX + bodyW, y: cy - bodyH/2}, {x: bodyX + bodyW, y: cy + bodyH/2}, {x: bodyX + bodyW + s, y: cy}}
	fillTriangleFilled(renderer, cone[0], cone[1], cone[2], col)
	// arcs (sound waves)
	arcX := bodyX + bodyW + s
	if arcs >= 1 {
		drawArc(renderer, arcX, cy, s*3/4, 0.9, col)
	}
	if arcs >= 2 {
		drawArc(renderer, arcX, cy, s*3/4+s/2, 0.9, col)
	}
}

// drawArc draws a short circular arc as dense line segments (radius r, angles
// from -half to +half radians around the +x axis).
func drawArc(renderer *sdl.Renderer, cx, cy, r int32, half float64, col sdl.Color) {
	renderer.SetDrawColor(col.R, col.G, col.B, col.A)
	steps := 10
	for i := 0; i < steps; i++ {
		a1 := -half + 2*half*float64(i)/float64(steps)
		a2 := -half + 2*half*float64(i+1)/float64(steps)
		renderer.DrawLine(clampInt32(cx+int32(float64(r)*math.Cos(a1)), -2000, 8000), clampInt32(cy+int32(float64(r)*math.Sin(a1)), -2000, 8000),
			clampInt32(cx+int32(float64(r)*math.Cos(a2)), -2000, 8000), clampInt32(cy+int32(float64(r)*math.Sin(a2)), -2000, 8000))
	}
}

// drawCloseIcon draws an X (close) glyph from two diagonal lines.
func drawCloseIcon(renderer *sdl.Renderer, cx, cy, r int32, col sdl.Color) {
	renderer.SetDrawColor(col.R, col.G, col.B, col.A)
	renderer.DrawLine(cx-r, cy-r, cx+r, cy+r)
	renderer.DrawLine(cx-r, cy+r, cx+r, cy-r)
}

func formatPercentSimple(v float64) string {
	p := int(v * 100)
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return strconv.Itoa(p) + "%"
}

// HandleVideoControlClick processes mouse clicks on the video overlay controls.
func HandleVideoControlClick(mx, my int32) {
	state := snapshotVideoState()
	if state.phase == "idle" {
		return
	}

	if pointInRect(mx, my, videoControlRects.overlayPlay) {
		ToggleVideoPlayback()
	} else if pointInRect(mx, my, videoControlRects.playPause) {
		ToggleVideoPlayback()
	} else if pointInRect(mx, my, videoControlRects.stop) {
		StopEmbeddedPlayback()
	} else if pointInRect(mx, my, videoControlRects.seekBack) {
		SeekVideo(state.progress - 0.05)
	} else if pointInRect(mx, my, videoControlRects.seekFwd) {
		SeekVideo(state.progress + 0.05)
	} else if pointInRect(mx, my, videoControlRects.volDown) {
		AdjustVolume(-0.1)
	} else if pointInRect(mx, my, videoControlRects.volUp) {
		AdjustVolume(0.1)
	} else if pointInRect(mx, my, videoControlRects.progress) {
		fraction := float64(mx-videoControlRects.progress.X) / float64(videoControlRects.progress.W)
		SeekVideo(fraction)
	} else if pointInRect(mx, my, videoControlRects.speedCycle) {
		speeds := []float64{0.5, 0.75, 1.0, 1.25, 1.5, 2.0}
		current := state.speed
		next := current
		for _, s := range speeds {
			if s > current {
				next = s
				break
			}
		}
		if next == current {
			next = speeds[0]
		}
		SetPlaybackSpeed(next)
	} else if pointInRect(mx, my, videoControlRects.back) {
		StopEmbeddedPlayback()
	}
}

// HandleVideoKeyInput processes keyboard input for video controls.
func HandleVideoKeyInput(e *sdl.KeyboardEvent) {
	if e.Type != sdl.KEYDOWN {
		return
	}
	state := snapshotVideoState()
	if state.phase == "idle" {
		return
	}

	switch e.Keysym.Sym {
	case sdl.K_SPACE, sdl.K_RETURN:
		ToggleVideoPlayback()
	case sdl.K_ESCAPE, sdl.K_q:
		StopEmbeddedPlayback()
	case sdl.K_LEFT:
		SeekVideo(state.progress - 0.02)
	case sdl.K_RIGHT:
		SeekVideo(state.progress + 0.02)
	case sdl.K_UP:
		AdjustVolume(0.1)
	case sdl.K_DOWN:
		AdjustVolume(-0.1)
	case sdl.K_1:
		SetPlaybackSpeed(0.5)
	case sdl.K_2:
		SetPlaybackSpeed(1.0)
	case sdl.K_3:
		SetPlaybackSpeed(1.5)
	case sdl.K_4:
		SetPlaybackSpeed(2.0)
	}
}

// HandleVideoControllerInput processes gamepad input for video controls.
func HandleVideoControllerInput(e *sdl.ControllerButtonEvent) {
	if e.Type != sdl.CONTROLLERBUTTONDOWN {
		return
	}
	state := snapshotVideoState()
	if state.phase == "idle" {
		return
	}

	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		ToggleVideoPlayback()
	case sdl.CONTROLLER_BUTTON_B:
		StopEmbeddedPlayback()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		SeekVideo(state.progress - 0.02)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		SeekVideo(state.progress + 0.02)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		AdjustVolume(0.1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		AdjustVolume(-0.1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		SetPlaybackSpeed(state.speed - 0.25)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		SetPlaybackSpeed(state.speed + 0.25)
	}
}

func pointInRect(px, py int32, r sdl.Rect) bool {
	return px >= r.X && px <= r.X+r.W && py >= r.Y && py <= r.Y+r.H
}
