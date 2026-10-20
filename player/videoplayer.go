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
	mu            sync.Mutex
	phase         string // idle | downloading | playing | paused | error
	progress      float64
	progressBase  float64
	volume        float64
	speed         float64
	downloadSpeed float64 // bytes per second, 0 when unknown
	current       *sdl.Texture
	currentRaw    []byte
	frameSeq      uint64
	uploadedSeq   uint64
	width         int32
	height        int32
	// sourceWidth/sourceHeight are the probed size before the decode cap.
	// The decoder scales the source down to width×height when they differ.
	sourceWidth         int32
	sourceHeight        int32
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

// Global embedded video player state.
var embeddedPlayer = &VideoPlayerState{phase: "idle", volume: 1.0, speed: 1.0}

// lastResumeSaveTicks throttles periodic resume-position writes.
var lastResumeSaveTicks uint64

var videoTextureLogCount int

// Video frame queue (kept for compatibility; decoder currently writes
// directly into currentRaw).
var videoFrameQueue chan *sdl.Texture
var videoFrameQueueSize = 4

// Control button hitboxes for the video overlay.
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
	subs        sdl.Rect
	settings    sdl.Rect
	fullscreen  sdl.Rect
}{}

// Video chrome state: full screen hides the controls until the user interacts,
// and the settings sheet lists subtitles / full screen / speed.
var (
	videoFullscreen   bool
	videoChromeUntil  uint64
	videoSettingsOpen bool
	videoSettingsItem int
)

// videoChromeVisible reports whether the controls should be drawn. While not in
// full screen they are always visible; in full screen they fade after a moment.
func videoChromeVisible() bool {
	if !videoFullscreen {
		return true
	}
	return sdl.GetTicks64() < videoChromeUntil
}

// revealVideoChrome shows the controls in full screen for a few seconds.
func revealVideoChrome() {
	videoChromeUntil = sdl.GetTicks64() + 3000
}

// toggleVideoFullscreen switches between the scene element and full screen.
func toggleVideoFullscreen() {
	videoFullscreen = !videoFullscreen
	videoSettingsOpen = false
	if videoFullscreen {
		revealVideoChrome()
	}
}

// toggleVideoSettings opens or closes the settings sheet.
func toggleVideoSettings() {
	videoSettingsOpen = !videoSettingsOpen
	videoSettingsItem = 0
}

// playbackSession owns one decode pipeline.
type playbackSession struct {
	id       uint64
	source   string
	pageURL  string
	tempFile bool

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

	// videoDecoderWG tracks the goroutines that own an ffmpeg process so tests
	// (and shutdown paths) can wait until they have really exited before a
	// shared SDL renderer is destroyed.
	videoDecoderWG sync.WaitGroup
)

// waitForVideoDecoders waits until every playback goroutine has returned,
// bounded by timeout. It is used by tests that tear down a renderer.
func waitForVideoDecoders(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		videoDecoderWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

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

func (s *playbackSession) finish() {
	if !s.isCurrent() {
		return
	}
	StopEmbeddedPlayback()
}

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

var videoTempDir = defaultVideoTempDir

func defaultVideoTempDir() string {
	if P().IsHandheld() {
		if dir, err := P().DataDir(); err == nil && dir != "" {
			videos := filepath.Join(dir, "video")
			if err := os.MkdirAll(videos, 0o755); err == nil {
				return videos
			}
		}
	}
	return os.TempDir()
}

func tempVideoPath() string {
	return filepath.Join(videoTempDir(), fmt.Sprintf("jukahub-video-%d.mp4", time.Now().UnixNano()))
}

func removeTempVideo(path string) {
	if path == "" {
		return
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "jukahub-video-") || !strings.HasSuffix(strings.ToLower(base), ".mp4") {
		return
	}
	tempDir, err := filepath.Abs(videoTempDir())
	if err != nil {
		return
	}
	fileDir, err := filepath.Abs(filepath.Dir(path))
	if err != nil || fileDir != tempDir {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[VIDEO] could not remove temp download: %v", err)
	}
}

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

func clampPosInt(v int64) int {
	const maxVideoDimension = 16384
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
	if avgFps > 0 {
		fps = avgFps
	}
	log.Printf("[VIDEO] ffprobe result: %dx%d, dur=%.1f, fps=%.1f", w, h, dur, fps)
	return w, h, dur, fps
}

// ffmpegDecoderArgs builds the rawvideo decoder command. When the source is
// larger than the decode target it uses a scale filter in ffmpeg (cheaper than
// decoding 4K frames and letting the GPU shrink them); otherwise it asks for
// the native size directly.
func ffmpegDecoderArgs(path string, width, height int32, scaling bool, startSec float64) []string {
	args := []string{}
	if startSec > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSec))
	}
	args = append(args, "-i", path)
	if scaling {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d:flags=bilinear", width, height))
	} else {
		args = append(args, "-s", fmt.Sprintf("%dx%d", width, height))
	}
	return append(args,
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-",
	)
}

func startFFmpegDecoder(sess *playbackSession, ffmpegPath, path string, width, height int32, startSec float64) {
	embeddedPlayer.mu.Lock()
	srcW, srcH := embeddedPlayer.sourceWidth, embeddedPlayer.sourceHeight
	embeddedPlayer.mu.Unlock()
	scaling := srcW > 0 && srcH > 0 && (srcW != width || srcH != height)

	args := ffmpegDecoderArgs(path, width, height, scaling, startSec)
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

	embeddedPlayer.mu.Lock()
	fps := embeddedPlayer.frameRate
	embeddedPlayer.mu.Unlock()
	if fps <= 0 {
		fps = 30
	}
	frameDuration := time.Duration(float64(time.Second) / fps)
	frameDeadline := time.Now()
	wasPaused := false

	// NOTE: playback speed is intentionally kept at 1x here. Applying
	// embeddedPlayer.speed to the frame pacing without also re-timing the
	// audio decoder causes progressive A/V drift, so real speed support
	// requires restarting both pipelines (a seek). The speed field remains
	// cosmetic until that is implemented.
	for {
		select {
		case <-sess.ctx.Done():
			return
		default:
		}
		embeddedPlayer.mu.Lock()
		paused := embeddedPlayer.phase == "paused"
		embeddedPlayer.mu.Unlock()
		if paused {
			wasPaused = true
			time.Sleep(15 * time.Millisecond)
			continue
		}
		if wasPaused {
			frameDeadline = time.Now().Add(frameDuration)
			wasPaused = false
		}
		if _, err := io.ReadFull(pipe, buf); err != nil {
			break
		}
		frameCount++

		// Publish the decoded frame by handing the buffer to the renderer and
		// taking the previous one back, instead of copying every frame. A
		// 720p RGBA frame is 3.6 MB; at 30fps the copy alone burned ~110 MB/s
		// of memory bandwidth on a device with a single 1 GB pool.
		embeddedPlayer.mu.Lock()
		previous := embeddedPlayer.currentRaw
		embeddedPlayer.currentRaw = buf
		embeddedPlayer.width = width
		embeddedPlayer.height = height
		embeddedPlayer.lastFrameTime = time.Now()
		embeddedPlayer.frameSeq++
		embeddedPlayer.mu.Unlock()

		// Reuse the retired buffer. The renderer only touches a published
		// buffer while holding the mutex, so by the time it is recycled the
		// upload has finished; if the renderer never got to it, that frame is
		// simply dropped (the correct behaviour when the UI is behind).
		if cap(previous) >= frameSize {
			buf = previous[:frameSize]
		} else {
			buf = make([]byte, frameSize)
		}

		if frameCount > 1 {
			frameDeadline = frameDeadline.Add(frameDuration)
			wait := time.Until(frameDeadline)
			if wait > 0 {
				time.Sleep(wait)
			} else if -wait > 250*time.Millisecond {
				frameDeadline = time.Now()
			}
		} else {
			frameDeadline = time.Now().Add(frameDuration)
			log.Printf("[VIDEO] First frame received: %dx%d, %d bytes, fps=%.1f, frameDur=%v", width, height, len(buf), fps, frameDuration)
		}
	}
}

func startFFmpegAudio(sess *playbackSession, ffmpegPath, path string, startSec float64) {
	rate := GetVideoAudioRate()
	args := []string{}
	if startSec > 0 {
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

	// A new video may have different dimensions (a scaled 1080p source vs a
	// native 480p one). Reusing a texture of the old size made SDL reject the
	// upload; recreate it instead so every source plays after the first.
	if embeddedPlayer.current != nil {
		_, _, tw, th, qerr := embeddedPlayer.current.Query()
		if qerr != nil || tw != w || th != h {
			embeddedPlayer.current.Destroy()
			embeddedPlayer.current = nil
			embeddedPlayer.uploadedSeq = 0
		}
	}
	if embeddedPlayer.current == nil {
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

func StartEmbeddedPlayback(config *Config, url string) {
	StartEmbeddedPlaybackAt(config, url, 0)
}

func StartEmbeddedPlaybackAt(config *Config, url string, startSec float64) {
	startEmbeddedPlayback(config, url, startSec, false)
}

func startEmbeddedPlayback(config *Config, url string, startSec float64, tempFile bool) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	pageURL := currentPlaybackURL
	if pageURL == "" {
		pageURL = url
		// Direct StartEmbeddedPlayback calls (queue/home cards) must still
		// know their source, or Watch Party would see an empty URL and think
		// the room had switched streams.
		currentPlaybackURL = pageURL
	}
	sess := beginPlaybackSession(url, pageURL, tempFile)
	// If a Watch Party is open, opening a video makes it the room's stream.
	// Remote peers get the original page URL, not the resolved CDN URL.
	partyPublishStart(pageURL, startSec)

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
	embeddedPlayer.downloadSpeed = 0
	embeddedPlayer.lastErr = ""
	embeddedPlayer.duration = 0
	embeddedPlayer.currentRaw = nil
	embeddedPlayer.frameSeq = 0
	embeddedPlayer.uploadedSeq = 0
	embeddedPlayer.sourceWidth = 0
	embeddedPlayer.sourceHeight = 0
	embeddedPlayer.totalPausedDuration = 0
	embeddedPlayer.pauseStartTime = time.Time{}
	embeddedPlayer.mu.Unlock()

	videoDecoderWG.Add(1)
	go func() {
		defer videoDecoderWG.Done()
		ffmpegPath := getToolPath("ffmpeg", config)
		ffprobePath := getToolPath("ffprobe", config)

		srcW, srcH, dur, fps := probeVideoInfo(ffprobePath, url)
		if !sess.isCurrent() {
			return
		}
		// Cap the decode size at the panel's resolution (or 1080p on desktop)
		// before allocating any frame buffers.
		maxW, maxH := videoDecodeMaxSize(IsTrimuiDevice() || P().IsHandheld())
		w, h := videoDecodeSize(srcW, srcH, maxW, maxH)

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
		embeddedPlayer.sourceWidth = srcW
		embeddedPlayer.sourceHeight = srcH
		embeddedPlayer.duration = dur
		embeddedPlayer.frameRate = fps
		embeddedPlayer.decoderStartTime = time.Now()
		embeddedPlayer.progressBase = progressBase
		embeddedPlayer.progress = progressBase
		embeddedPlayer.mu.Unlock()

		// Sidecar / embedded subtitle tracks belong to this source; load them
		// before the first frame so the CC button knows what it can turn on.
		prepareVideoSubtitles(config, url)

		go startFFmpegAudio(sess, ffmpegPath, url, startSec)
		startFFmpegDecoder(sess, ffmpegPath, url, w, h, startSec)

		sess.finish()
	}()
}

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
	ClearVideoAudioQueue()

	embeddedPlayer.mu.Lock()
	prevPhase := embeddedPlayer.phase
	embeddedPlayer.phase = "idle"
	embeddedPlayer.currentRaw = nil
	embeddedPlayer.frameSeq = 0
	embeddedPlayer.uploadedSeq = 0
	embeddedPlayer.downloadSpeed = 0
	embeddedPlayer.mu.Unlock()

	if prevPhase != "idle" {
		clearVideoFrameQueue()
	}

	// Remember a deliberate stop so Watch Party does not reopen the room's
	// stream the moment the poll sees it again.
	partyNoteStop(currentPlaybackURL)
	currentPlaybackURL = ""
	// Leaving playback also leaves full screen and the settings sheet behind.
	videoFullscreen = false
	videoSettingsOpen = false
	setSubtitlesEnabled(false)

	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "idle"
	videoPlaybackPhaseAt = sdl.GetTicks64()
	videoPlaybackProgress = 0
	videoPlaybackCmd = nil
	embeddedPlaybackPath = ""
	videoPlaybackMutex.Unlock()
}

func stopVideoPlayback() {
	StopEmbeddedPlayback()
}

func ToggleVideoPlayback() {
	embeddedPlayer.mu.Lock()

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
	embeddedPlayer.mu.Unlock()
	// Publish after releasing the state lock: partyPublishPlayback snapshots
	// the player and would deadlock if called underneath it.
	partyPublishPlayback()
}

func toggleVideoPlayback() {
	ToggleVideoPlayback()
}

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

	videoDecoderWG.Add(1)
	go func() {
		defer videoDecoderWG.Done()
		go startFFmpegAudio(next, ffmpegPath, prev.source, seekSec)
		startFFmpegDecoder(next, ffmpegPath, prev.source, width, height, seekSec)
		next.finish()
	}()
	partyPublishPlayback()
}

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

func GetEmbeddedPlayerState() *VideoPlayerState {
	return embeddedPlayer
}

type videoStateSnapshot struct {
	phase               string
	progress            float64
	progressBase        float64
	duration            float64
	volume              float64
	speed               float64
	downloadSpeed       float64
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
		downloadSpeed:       embeddedPlayer.downloadSpeed,
		width:               embeddedPlayer.width,
		height:              embeddedPlayer.height,
		hasRaw:              embeddedPlayer.currentRaw != nil,
		decoderStartTime:    embeddedPlayer.decoderStartTime,
		pauseStartTime:      embeddedPlayer.pauseStartTime,
		totalPausedDuration: embeddedPlayer.totalPausedDuration,
	}
}

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

// computeVideoControlLayout fills videoControlRects for the given geometry.
// It runs every frame regardless of the overlay cache, so mouse/touch hit
// testing keeps working when the cached overlay path is used.
func computeVideoControlLayout(x, y, w, h int32, state videoStateSnapshot) {
	barH := int32(72)
	barY := y + h - barH

	backBtnSize := int32(30)
	badgeY := y + 12
	backX := x + w - backBtnSize - 18
	videoControlRects.back = sdl.Rect{X: backX, Y: badgeY - 2, W: backBtnSize, H: backBtnSize}

	seekBarY := barY - 16
	seekBarW := w - 48
	seekBarX := x + 24
	videoControlRects.progress = sdl.Rect{X: seekBarX, Y: seekBarY - 8, W: seekBarW, H: 20}

	btnSize := int32(36)
	gap := int32(10)
	groupW := 8*btnSize + 7*gap
	startX := x + (w-groupW)/2
	btnY := barY + (barH-btnSize)/2

	videoControlRects.stop = sdl.Rect{X: startX, Y: btnY, W: btnSize, H: btnSize}
	startX += btnSize + gap
	videoControlRects.seekBack = sdl.Rect{X: startX, Y: btnY, W: btnSize, H: btnSize}
	startX += btnSize + gap
	videoControlRects.playPause = sdl.Rect{X: startX, Y: btnY, W: btnSize, H: btnSize}
	startX += btnSize + gap
	videoControlRects.seekFwd = sdl.Rect{X: startX, Y: btnY, W: btnSize, H: btnSize}
	startX += btnSize + gap
	videoControlRects.speed = sdl.Rect{X: startX, Y: btnY, W: btnSize, H: btnSize}
	videoControlRects.speedCycle = videoControlRects.speed
	startX += btnSize + gap
	videoControlRects.subs = sdl.Rect{X: startX, Y: btnY, W: btnSize, H: btnSize}
	startX += btnSize + gap
	videoControlRects.settings = sdl.Rect{X: startX, Y: btnY, W: btnSize, H: btnSize}
	startX += btnSize + gap
	videoControlRects.fullscreen = sdl.Rect{X: startX, Y: btnY, W: btnSize, H: btnSize}

	volX := x + 18
	videoControlRects.volDown = sdl.Rect{X: volX, Y: btnY, W: btnSize, H: btnSize}
	videoControlRects.volUp = sdl.Rect{X: volX + btnSize + 6, Y: btnY, W: btnSize, H: btnSize}

	if state.phase == "paused" {
		overlayBtnSize := int32(80)
		videoControlRects.overlayPlay = sdl.Rect{
			X: x + (w-overlayBtnSize)/2,
			Y: y + (h-overlayBtnSize)/2,
			W: overlayBtnSize,
			H: overlayBtnSize,
		}
	} else {
		videoControlRects.overlayPlay = sdl.Rect{}
	}
}

// RenderVideoOverlay renders the current video frame and on-screen controls.
func RenderVideoOverlay(renderer *sdl.Renderer, config *Config, elem Element) {
	state := snapshotVideoState()
	if state.phase == "idle" {
		return
	}

	// Full-screen loader shown during video download.
	if state.phase == "downloading" {
		renderer.SetDrawColor(0, 0, 0, 180)
		renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})

		font, _ := getCachedFont(config, "small")
		if font == nil {
			return
		}

		loaderText := fmt.Sprintf("Downloading... %d%%", int(state.progress*100))
		textW, textH, _ := font.SizeUTF8(loaderText)
		renderText(renderer, config, font, loaderText, ColorTextPrimary(),
			(screenWidth-int32(textW))/2, (screenHeight-int32(textH))/2)

		// Estimated time remaining (guard against division by zero).
		if state.duration > 0 && state.progress > 0 {
			etaSec := int(state.duration * (1 - state.progress) / state.progress)
			if etaSec > 0 {
				etaText := formatTime(float64(etaSec))
				renderText(renderer, config, font, "ETA: "+etaText, ColorTextTertiary(),
					(screenWidth-int32(textW))/2, (screenHeight-int32(textH))/2+40)
			}
		}

		// Download speed.
		if spd := formatDownloadSpeed(state.downloadSpeed); spd != "" {
			renderText(renderer, config, font, "Downloading at "+spd, ColorTextTertiary(),
				(screenWidth-int32(textW))/2, (screenHeight-int32(textH))/2+70)
		}
		return
	}

	x, y := elem.X, elem.Y
	w := getElementWidth(elem, screenWidth)
	h := getElementHeight(elem, screenHeight)
	// Full screen ignores the element frame and uses the whole panel.
	if videoFullscreen {
		x, y, w, h = 0, 0, screenWidth, screenHeight
	}

	// Update playback progress from elapsed wall time.
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
		state.progress = fraction

		// Throttled resume-position safety net.
		if now := sdl.GetTicks64(); now-lastResumeSaveTicks > 5000 {
			lastResumeSaveTicks = now
			if src := currentPlaybackURL; src != "" && state.duration > 0 {
				updateRecentPosition(src, fraction*state.duration)
				saveFavorites()
			}
		}
	}

	// Layout the controls first so hit testing works even when the cached
	// overlay texture is used.
	computeVideoControlLayout(x, y, w, h, state)

	UpdateCurrentTexture(renderer)

	embeddedPlayer.mu.Lock()
	currentTex := embeddedPlayer.current
	embeddedPlayer.mu.Unlock()

	if currentTex == nil && state.phase == "playing" {
		videoTextureLogCount++
		if videoTextureLogCount%120 == 0 {
			log.Printf("[VIDEO] currentTex is nil after UpdateCurrentTexture, raw=%v, w=%d, h=%d",
				state.hasRaw, state.width, state.height)
		}
	}

	// Full screen with the chrome faded out: just the picture and subtitles.
	if videoFullscreen && !videoChromeVisible() {
		drawVideoFrameArea(renderer, currentTex, x, y, w, h)
		renderVideoSubtitles(renderer, config, x, y, w, h, state.progress*state.duration, 48)
		return
	}

	cachedOverlay := vkEnsureVideoOverlayCache(renderer, config, w, h)
	if cachedOverlay != nil {
		// Live video frame first, then the cached static layer over it, then
		// the state-dependent controls on top of both.
		drawVideoFrameArea(renderer, currentTex, x, y, w, h)
		renderer.Copy(cachedOverlay,
			&sdl.Rect{X: 0, Y: 0, W: w, H: h},
			&sdl.Rect{X: x, Y: y, W: w, H: h})
		drawVideoOverlayLive(renderer, config, state, x, y, w, h)
	} else {
		drawVideoOverlayFallback(renderer, config, state, currentTex, x, y, w, h)
	}
}

// renderVideoFullscreen draws the embedded player over the entire panel, hiding
// the scene header, elements and footer behind it.
func renderVideoFullscreen(renderer *sdl.Renderer, config *Config) {
	if renderer == nil {
		return
	}
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight})
	RenderVideoOverlay(renderer, config, Element{Type: "video", X: 0, Y: 0})
}

// drawVideoFrameArea draws the current video frame, or a dark backdrop with a
// spinner while the decoder has not produced one yet (downloading or buffering),
// so playback never looks like a dead black rectangle.
func drawVideoFrameArea(renderer *sdl.Renderer, currentTex *sdl.Texture, x, y, w, h int32) {
	if currentTex != nil {
		renderer.Copy(currentTex, nil, &sdl.Rect{X: x, Y: y, W: w, H: h})
		return
	}
	renderer.SetDrawColor(8, 10, 16, 220)
	renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
	renderSpinner(renderer, x+w/2, y+h/2, 40, ColorTextPrimary())
}

// drawVideoOverlayStatic draws the part of the overlay that never changes while
// a video is on screen: readability gradients, the control bar and the seek
// track. The overlay cache renders this once per element size and theme change;
// everything state-dependent is drawn per frame by drawVideoOverlayLive.
// (ox, oy) is the origin the layer is drawn at: the element position on screen,
// or (0, 0) when rendering into the element-sized cache texture.
func drawVideoOverlayStatic(renderer *sdl.Renderer, ox, oy, w, h int32) {
	// Top / bottom gradients for readability.
	topGradH := int32(70)
	for s := int32(0); s < topGradH; s += 3 {
		a := uint8(clampInt32(90*(10-int32(float64(s)*10.0/float64(topGradH))), 0, 90))
		renderer.SetDrawColor(0, 0, 0, a)
		renderer.FillRect(&sdl.Rect{X: ox, Y: oy + s, W: w, H: 3})
	}
	bottomGradH := int32(140)
	bottomY := oy + h - bottomGradH
	for s := int32(0); s < bottomGradH; s += 3 {
		a := uint8(clampInt32(int32(180.0*float64(s)/float64(bottomGradH)), 0, 180))
		renderer.SetDrawColor(0, 0, 0, a)
		renderer.FillRect(&sdl.Rect{X: ox, Y: bottomY + s, W: w, H: 3})
	}

	// Bottom control bar.
	barH := int32(72)
	barY := oy + h - barH
	fillRoundedRect(renderer, ox+8, barY, w-16, barH, 16, WithAlpha(ColorSurfacePanel, 220))

	// Seek track; the filled part and thumb change every frame and are drawn
	// by drawVideoOverlayLive.
	seekBarH := int32(6)
	seekBarY := barY - 16
	seekBarW := w - 48
	seekBarX := ox + 24
	fillRoundedRect(renderer, seekBarX, seekBarY, seekBarW, seekBarH, seekBarH/2,
		sdl.Color{R: 40, G: 40, B: 50, A: 255})
	renderer.SetDrawColor(255, 255, 255, 30)
	renderer.FillRect(&sdl.Rect{X: seekBarX, Y: seekBarY, W: seekBarW, H: 1})
}

// drawVideoOverlayLive draws everything that changes while a video plays: the
// phase badge, back button, volume, seek fill and thumb, the transport buttons
// (stop, rewind, play/pause, forward, speed), the time readout and the big play
// button while paused. It is drawn every frame on top of the static layer.
func drawVideoOverlayLive(renderer *sdl.Renderer, config *Config, state videoStateSnapshot, x, y, w, h int32) {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	accent := ColorInfo

	barH := int32(72)
	barY := y + h - barH

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
	fillRoundedRect(renderer, badgeX, badgeY, clampInt32(int32(phaseW)+18, 1, 10000), 26, 13,
		WithAlpha(ColorSurfaceRaised, 230))
	renderText(renderer, config, font, phaseText, phaseCol, badgeX+9, badgeY+6)

	// Top-right: back button + volume text.
	volText := formatPercentSimple(state.volume)
	volW, _, _ := font.SizeUTF8(volText)
	backRect := videoControlRects.back
	fillRoundedRect(renderer, backRect.X, backRect.Y, backRect.W, backRect.H, 8,
		WithAlpha(ColorSurfaceRaised, 180))
	drawCloseIcon(renderer, backRect.X+backRect.W/2, backRect.Y+backRect.H/2, 11, ColorTextPrimary())
	renderText(renderer, config, font, volText, ColorTextPrimary(), x+w-int32(volW)-58, y+18)

	// Seek fill + thumb.
	seekBarH := int32(6)
	seekBarY := barY - 16
	seekBarW := w - 48
	seekBarX := x + 24
	if state.progress > 0 {
		filledW := int32(float64(seekBarW) * state.progress)
		if filledW > 0 {
			fillRoundedRect(renderer, seekBarX, seekBarY, clampInt32(filledW, 0, seekBarW),
				seekBarH, seekBarH/2, accent)
		}
	}
	thumbX := seekBarX + int32(float64(seekBarW)*state.progress)
	fillCircle(renderer, thumbX, seekBarY+seekBarH/2, 7, accent)

	// Control buttons.
	drawIconBtn(renderer, iconStop, videoControlRects.stop.X, videoControlRects.stop.Y, videoControlRects.stop.W)
	drawIconBtn(renderer, iconSeekBack, videoControlRects.seekBack.X, videoControlRects.seekBack.Y, videoControlRects.seekBack.W)
	if state.phase == "paused" {
		drawIconBtn(renderer, iconPlay, videoControlRects.playPause.X, videoControlRects.playPause.Y, videoControlRects.playPause.W)
	} else {
		drawIconBtn(renderer, iconPause, videoControlRects.playPause.X, videoControlRects.playPause.Y, videoControlRects.playPause.W)
	}
	drawIconBtn(renderer, iconSeekFwd, videoControlRects.seekFwd.X, videoControlRects.seekFwd.Y, videoControlRects.seekFwd.W)
	speedLabel := fmt.Sprintf("%.1fx", state.speed)
	drawSpeedBtn(renderer, config, font, speedLabel,
		videoControlRects.speed.X, videoControlRects.speed.Y, videoControlRects.speed.W)

	drawIconBtn(renderer, iconVolLow, videoControlRects.volDown.X, videoControlRects.volDown.Y, videoControlRects.volDown.W)
	drawIconBtn(renderer, iconVolHigh, videoControlRects.volUp.X, videoControlRects.volUp.Y, videoControlRects.volUp.W)

	// Subtitles / settings / full screen buttons.
	drawCCBtn(renderer, config, font, videoControlRects.subs.X, videoControlRects.subs.Y,
		videoControlRects.subs.W, subtitlesEnabled())
	drawIconBtn(renderer, iconSettings, videoControlRects.settings.X, videoControlRects.settings.Y,
		videoControlRects.settings.W)
	drawIconBtn(renderer, iconFullscreen, videoControlRects.fullscreen.X, videoControlRects.fullscreen.Y,
		videoControlRects.fullscreen.W)

	// Active subtitle cue above the control bar.
	currentTime := state.progress * state.duration
	renderVideoSubtitles(renderer, config, x, y, w, h, currentTime, barH+28)

	// Settings sheet sits above the control bar.
	if videoSettingsOpen {
		drawVideoSettingsSheet(renderer, config, x, y, w, h)
	}

	timeText := formatTime(currentTime) + " / " + formatTime(state.duration)
	tw, th, _ := font.SizeUTF8(timeText)
	renderText(renderer, config, font, timeText, ColorTextPrimary(),
		x+w-int32(tw)-18, barY+(barH-int32(th))/2)

	// Big centered play button when paused.
	if state.phase == "paused" {
		overlayBtn := videoControlRects.overlayPlay
		cx := overlayBtn.X + overlayBtn.W/2
		cy := overlayBtn.Y + overlayBtn.H/2
		UIButton(renderer, config, overlayBtn, "", UIButtonStyle{
			Variant:  UIButtonCustom,
			Fill:     WithAlpha(ColorSurfacePanel, 200),
			Border:   WithAlpha(accent, 180),
			Pill:     true,
			NoShadow: true,
		}, UIButtonState{})
		s := overlayBtn.W / 5
		renderer.SetDrawColor(accent.R, accent.G, accent.B, accent.A)
		renderer.DrawLine(cx-s, cy-s, cx-s, cy+s)
		renderer.DrawLine(cx-s, cy+s, cx+s-2, cy)
		renderer.DrawLine(cx+s-2, cy, cx-s, cy-s)
	}
}

// drawVideoOverlayFallback draws the entire overlay from scratch. Used when
// the cached overlay texture is unavailable.
func drawVideoOverlayFallback(renderer *sdl.Renderer, config *Config, state videoStateSnapshot,
	currentTex *sdl.Texture, x, y, w, h int32) {
	drawVideoFrameArea(renderer, currentTex, x, y, w, h)
	drawVideoOverlayStatic(renderer, x, y, w, h)
	drawVideoOverlayLive(renderer, config, state, x, y, w, h)
}

// playerIcon identifies a geometry-drawn control icon.
type playerIcon int

const (
	iconStop playerIcon = iota
	iconSeekBack
	iconPlay
	iconPause
	iconSeekFwd
	iconVolLow
	iconVolHigh
	iconSettings
	iconFullscreen
)

func iconCol() sdl.Color {
	return ColorTextPrimary()
}

func drawIconBtn(renderer *sdl.Renderer, icon playerIcon, x, y, size int32) sdl.Rect {
	UIButton(renderer, nil, sdl.Rect{X: x, Y: y, W: size, H: size}, "", UIButtonStyle{
		Variant:  UIButtonCustom,
		Fill:     WithAlpha(ColorSurfaceRaised, 180),
		Border:   WithAlpha(ColorBorderSubtle, 120),
		Radius:   size / 5,
		NoShadow: true,
	}, UIButtonState{})
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
	case iconSettings:
		// Three sliders with offset knobs.
		s := size / 3
		w := s * 2
		left := cx - s
		for i, off := range []int32{-s / 2, s / 4, -s / 6} {
			y := cy - s + int32(i)*s
			fillRoundedRect(renderer, left, y, w, 2, 1, col)
			knobOff := off
			if knobOff < -s+3 {
				knobOff = -s + 3
			}
			if knobOff > s-3 {
				knobOff = s - 3
			}
			fillCircle(renderer, cx+knobOff, y+1, 2, col)
		}
	case iconFullscreen:
		// Four corner brackets.
		s := size / 3
		arm := s / 2
		for _, c := range [4][2]int32{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
			px := cx + c[0]*s
			py := cy + c[1]*s
			fillRoundedRect(renderer, px-c[0]*arm, py-1, arm, 2, 1, col)
			fillRoundedRect(renderer, px-1, py-c[1]*arm, 2, arm, 1, col)
		}
	}
	return sdl.Rect{X: x, Y: y, W: size, H: size}
}

// drawCCBtn draws the closed-caption toggle; it lights up when subtitles are on.
func drawCCBtn(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, size int32, on bool) {
	bg := WithAlpha(ColorSurfaceRaised, 180)
	fg := ColorTextPrimary()
	if on {
		bg = WithAlpha(ColorInfo, 220)
		fg = sdl.Color{R: 12, G: 14, B: 20, A: 255}
	}
	UIButton(renderer, config, sdl.Rect{X: x, Y: y, W: size, H: size}, "CC", UIButtonStyle{
		Variant:    UIButtonCustom,
		Fill:       bg,
		HoverFill:  bg,
		FocusFill:  bg,
		Border:     WithAlpha(ColorBorderSubtle, 120),
		LabelColor: fg,
		Font:       font,
		Radius:     size / 5,
		NoShadow:   true,
	}, UIButtonState{})
}

// Layout of the settings sheet. The row metrics are shared by the renderer and
// the click hit test, so a tap always lands on the row it is drawn over.
const (
	videoSettingsSheetRows = 4
	videoSettingsRowH      = int32(48)
	videoSettingsTopPad    = int32(20)
)

// videoSettingsSheetRect places the sheet in the bottom-right corner of the
// video element, leaving room for the control bar underneath.
func videoSettingsSheetRect(x, y, w, h int32) sdl.Rect {
	sheetW := int32(360)
	// Top padding, the rows, then a caption line and bottom padding.
	sheetH := int32(videoSettingsSheetRows)*videoSettingsRowH + 68
	if sheetW > w-32 {
		sheetW = w - 32
	}
	sheetX := x + w - sheetW - 24
	sheetY := y + h - 72 - sheetH - 16
	if sheetY < y+16 {
		sheetY = y + 16
	}
	return sdl.Rect{X: sheetX, Y: sheetY, W: sheetW, H: sheetH}
}

// drawVideoSettingsSheet draws the subtitles / full screen / speed sheet.
func drawVideoSettingsSheet(renderer *sdl.Renderer, config *Config, x, y, w, h int32) {
	sheet := videoSettingsSheetRect(x, y, w, h)
	fillRoundedRect(renderer, sheet.X, sheet.Y, sheet.W, sheet.H, 14, WithAlpha(ColorSurfacePanel, 240))
	strokeRoundedRect(renderer, sheet.X, sheet.Y, sheet.W, sheet.H, 14, 1, WithAlpha(ColorBorder, 160))

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	subState := "Off"
	if subtitlesEnabled() {
		subState = "On"
	}
	if !subtitlesAvailable() {
		subState = "none found"
	}
	fullscreenState := "Off"
	if videoFullscreen {
		fullscreenState = "On"
	}
	rows := []string{
		"Subtitles: " + subState,
		"Full screen: " + fullscreenState,
		fmt.Sprintf("Speed: %.2gx", snapshotVideoState().speed),
		"Close",
	}
	for i, label := range rows {
		rowY := sheet.Y + videoSettingsTopPad + int32(i)*videoSettingsRowH
		row := sdl.Rect{X: sheet.X + 10, Y: rowY, W: sheet.W - 20, H: videoSettingsRowH - 6}
		if i == videoSettingsItem {
			fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, 8, WithAlpha(accentColor, 60))
		}
		renderText(renderer, config, font, label, ColorTextPrimary(), row.X+14, row.Y+(row.H-int32(font.Height()))/2)
	}

	// The source line explains what was loaded (sidecar name or "embedded").
	// It sits inside the sheet so it can never overlap the control bar.
	status := subtitleStatusLine()
	statusFont, _ := getCachedFont(config, "small")
	if statusFont != nil {
		if sw, _, _ := statusFont.SizeUTF8(status); int32(sw) > sheet.W-28 {
			status = ellipsize(statusFont, status, sheet.W-28)
		}
		statusY := sheet.Y + videoSettingsTopPad + int32(videoSettingsSheetRows)*videoSettingsRowH + 8
		renderText(renderer, config, statusFont, status, ColorTextTertiary(), sheet.X+14, statusY)
	}
}

func drawSpeedBtn(renderer *sdl.Renderer, config *Config, font *ttf.Font, label string, x, y, size int32) sdl.Rect {
	fillRoundedRect(renderer, x, y, size, size, size/5, WithAlpha(ColorSurfaceRaised, 180))
	renderer.SetDrawColor(255, 255, 255, 25)
	renderer.FillRect(&sdl.Rect{X: x + 3, Y: y + 1, W: size - 6, H: 1})
	if font != nil {
		lw, lh, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, ColorTextPrimary(),
			x+(size-int32(lw))/2, y+(size-int32(lh))/2)
	}
	return sdl.Rect{X: x, Y: y, W: size, H: size}
}

func drawSpeaker(renderer *sdl.Renderer, cx, cy, size int32, col sdl.Color, arcs int) {
	s := size / 3
	bodyW := s * 3 / 4
	bodyH := s * 3 / 2
	bodyX := cx - s*3/2 + s/3
	fillRoundedRect(renderer, bodyX, cy-bodyH/2, bodyW, bodyH, 2, col)
	cone := [3]pt{{x: bodyX + bodyW, y: cy - bodyH/2}, {x: bodyX + bodyW, y: cy + bodyH/2}, {x: bodyX + bodyW + s, y: cy}}
	fillTriangleFilled(renderer, cone[0], cone[1], cone[2], col)
	arcX := bodyX + bodyW + s
	if arcs >= 1 {
		drawArc(renderer, arcX, cy, s*3/4, 0.9, col)
	}
	if arcs >= 2 {
		drawArc(renderer, arcX, cy, s*3/4+s/2, 0.9, col)
	}
}

func drawArc(renderer *sdl.Renderer, cx, cy, r int32, half float64, col sdl.Color) {
	renderer.SetDrawColor(col.R, col.G, col.B, col.A)
	steps := 10
	for i := 0; i < steps; i++ {
		a1 := -half + 2*half*float64(i)/float64(steps)
		a2 := -half + 2*half*float64(i+1)/float64(steps)
		renderer.DrawLine(
			clampInt32(cx+int32(float64(r)*math.Cos(a1)), -2000, 8000),
			clampInt32(cy+int32(float64(r)*math.Sin(a1)), -2000, 8000),
			clampInt32(cx+int32(float64(r)*math.Cos(a2)), -2000, 8000),
			clampInt32(cy+int32(float64(r)*math.Sin(a2)), -2000, 8000))
	}
}

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

// formatDownloadSpeed formats a bytes-per-second value for the loader text.
func formatDownloadSpeed(bytesPerSec float64) string {
	if bytesPerSec <= 0 {
		return ""
	}
	const (
		kib = 1024.0
		mib = 1024.0 * 1024.0
	)
	switch {
	case bytesPerSec >= mib:
		return fmt.Sprintf("%.1f MiB/s", bytesPerSec/mib)
	case bytesPerSec >= kib:
		return fmt.Sprintf("%.1f KiB/s", bytesPerSec/kib)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	}
}

// HandleVideoControlClick processes mouse clicks on the video overlay controls.
// It reports whether the click was consumed, so callers can stop a click from
// reaching the scene elements underneath the player.
func HandleVideoControlClick(mx, my int32) bool {
	state := snapshotVideoState()
	if state.phase == "idle" {
		return false
	}

	// In full screen a tap anywhere brings the controls back first.
	if videoFullscreen && !videoChromeVisible() {
		revealVideoChrome()
		return true
	}
	if videoFullscreen {
		revealVideoChrome()
	}

	// The settings sheet owns every click while it is open.
	if videoSettingsOpen {
		sheet := videoSettingsSheetRect(0, 0, screenWidth, screenHeight)
		if pointInRect(mx, my, sheet) {
			idx := int((my - (sheet.Y + videoSettingsTopPad)) / videoSettingsRowH)
			if idx < 0 {
				idx = 0
			}
			if idx >= videoSettingsSheetRows {
				idx = videoSettingsSheetRows - 1
			}
			videoSettingsItem = idx
			videoSettingsActivate(idx)
			return true
		}
		videoSettingsOpen = false
		return true
	}

	if pointInRect(mx, my, videoControlRects.subs) {
		if subtitlesAvailable() {
			on := toggleSubtitles()
			if on {
				showToast("Subtitles on", ToastInfo())
			} else {
				showToast("Subtitles off", ToastInfo())
			}
		} else {
			showToast("No subtitles found for this video", ToastInfo())
		}
		return true
	} else if pointInRect(mx, my, videoControlRects.settings) {
		videoSettingsOpen = true
		videoSettingsItem = 0
		return true
	} else if pointInRect(mx, my, videoControlRects.fullscreen) {
		toggleVideoFullscreen()
		return true
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
	return true
}

// videoSettingsActivate runs the action for one settings sheet row.
func videoSettingsActivate(idx int) {
	switch idx {
	case 0:
		if subtitlesAvailable() {
			toggleSubtitles()
		} else {
			showToast("No subtitles found for this video", ToastInfo())
		}
	case 1:
		toggleVideoFullscreen()
		videoSettingsOpen = !videoSettingsOpen
		if videoSettingsOpen {
			revealVideoChrome()
		}
	case 2:
		speeds := []float64{0.5, 0.75, 1.0, 1.25, 1.5, 2.0}
		current := snapshotVideoState().speed
		next := speeds[0]
		for _, s := range speeds {
			if s > current {
				next = s
				break
			}
		}
		SetPlaybackSpeed(next)
	case 3:
		videoSettingsOpen = false
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
	case sdl.K_f:
		toggleVideoFullscreen()
	case sdl.K_c:
		if subtitlesAvailable() {
			toggleSubtitles()
		} else {
			showToast("No subtitles found for this video", ToastInfo())
		}
	case sdl.K_s:
		videoSettingsOpen = !videoSettingsOpen
		videoSettingsItem = 0
	case sdl.K_ESCAPE:
		// Escape leaves full screen (and the settings sheet) before it ends
		// playback, so a mistaken tap does not kill the video.
		if videoSettingsOpen {
			videoSettingsOpen = false
			return
		}
		if videoFullscreen {
			videoFullscreen = false
			return
		}
		StopEmbeddedPlayback()
	case sdl.K_q:
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
