package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
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

var videoTextureLogCount int

// Video frame queue for decoder → renderer handoff.
var videoFrameQueue chan *sdl.Texture
var videoFrameQueueSize = 4

// Control button hitboxes for the video overlay
var videoControlRects = struct {
	playPause  sdl.Rect
	stop       sdl.Rect
	seekBack   sdl.Rect
	seekFwd    sdl.Rect
	volDown    sdl.Rect
	volUp      sdl.Rect
	progress   sdl.Rect
	speed      sdl.Rect
	speedCycle sdl.Rect
	back       sdl.Rect
}{}

// ffmpegCtx is used to cancel the ffmpeg decoder when playback stops.
var ffmpegCtx context.CancelFunc
var ffmpegCmd *exec.Cmd

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

func probeVideoInfo(ffprobePath, path string) (int32, int32, float64, float64) {
	if ffprobePath == "" {
		return 1280, 720, 0, 30
	}
	cmd := exec.Command(ffprobePath, "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate,avg_frame_rate,duration",
		"-of", "default=noprint_wrappers=1", path)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[VIDEO] ffprobe failed: %v", err)
		return 1280, 720, 0, 30
	}
	s := string(out)
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
		if strings.HasPrefix(line, "width=") {
			if v, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "width="))); v > 0 {
				w = int32(v)
			}
		} else if strings.HasPrefix(line, "height=") {
			if v, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "height="))); v > 0 {
				h = int32(v)
			}
		} else if strings.HasPrefix(line, "duration=") {
			if v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "duration=")), 64); v > 0 {
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

func startFFmpegDecoder(ffmpegPath, path string, width, height int32) {
	ctx, cancel := context.WithCancel(context.Background())
	ffmpegCtx = cancel

	args := []string{
		"-re",
		"-i", path,
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-",
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
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
	ffmpegCmd = cmd

	frameSize := int(width * height * 4)
	if frameSize <= 0 {
		cmd.Wait()
		return
	}
	buf := make([]byte, frameSize)
	frameCount := 0

	for {
		_, err := io.ReadFull(pipe, buf)
		if err != nil {
			break
		}
		frameCount++

		embeddedPlayer.mu.Lock()
		embeddedPlayer.currentRaw = make([]byte, len(buf))
		copy(embeddedPlayer.currentRaw, buf)
		embeddedPlayer.width = width
		embeddedPlayer.height = height
		embeddedPlayer.lastFrameTime = time.Now()
		embeddedPlayer.mu.Unlock()

		if frameCount == 1 {
			log.Printf("[VIDEO] First frame received: %dx%d, %d bytes", width, height, len(buf))
		}
	}

	cmd.Wait()
}

func startFFmpegAudio(ffmpegPath, path string) {
	ctx, cancel := context.WithCancel(context.Background())
	audioCtx = cancel

	args := []string{
		"-i", path,
		"-f", "s16le",
		"-ac", "2",
		"-ar", "44100",
		"-",
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
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
	audioCmd = cmd

	// Audio buffer for SDL2 callback
	const audioBufferSize = 4096
	buf := make([]int16, audioBufferSize)

	for {
		n, err := io.ReadFull(pipe, (*[audioBufferSize]byte)(unsafe.Pointer(&buf[0]))[:])
		if err != nil {
			break
		}
		// Audio data is read but not played yet
		// TODO: Feed to SDL2 audio callback queue
		_ = n
	}

	cmd.Wait()
}

var audioCtx context.CancelFunc
var audioCmd *exec.Cmd

// UpdateCurrentTexture uploads the current raw RGBA frame to an SDL2 texture.
func UpdateCurrentTexture(renderer *sdl.Renderer) {
	if renderer == nil {
		return
	}
	videoTextureLogCount++
	if videoTextureLogCount%120 == 0 {
		log.Printf("[VIDEO] UpdateCurrentTexture called, raw=%v, w=%d, h=%d", embeddedPlayer.currentRaw != nil, embeddedPlayer.width, embeddedPlayer.height)
	}
	embeddedPlayer.mu.Lock()
	if embeddedPlayer.currentRaw == nil {
		embeddedPlayer.mu.Unlock()
		return
	}
	w := embeddedPlayer.width
	h := embeddedPlayer.height
	raw := embeddedPlayer.currentRaw
	tex := embeddedPlayer.current
	embeddedPlayer.mu.Unlock()

	if w <= 0 || h <= 0 || len(raw) != int(w*h*4) {
		return
	}

	if tex == nil {
		newTex, err := renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_STREAMING, w, h)
		if err != nil {
			log.Printf("[VIDEO] CreateTexture failed: %v (w=%d h=%d)", err, w, h)
			return
		}
		tex = newTex
		log.Printf("[VIDEO] Created texture %dx%d", w, h)
		embeddedPlayer.mu.Lock()
		if embeddedPlayer.current == nil {
			embeddedPlayer.current = tex
		} else {
			tex.Destroy()
			tex = embeddedPlayer.current
		}
		embeddedPlayer.mu.Unlock()
	}

	if err := tex.Update(nil, unsafe.Pointer(&raw[0]), int(w)*4); err != nil {
		log.Printf("[VIDEO] Update texture failed: %v", err)
		tex.Destroy()
		embeddedPlayer.mu.Lock()
		if embeddedPlayer.current == tex {
			embeddedPlayer.current = nil
		}
		embeddedPlayer.mu.Unlock()
	} else {
		videoTextureLogCount++
		if videoTextureLogCount%120 == 0 {
			log.Printf("[VIDEO] Updated texture with %d bytes", len(raw))
		}
	}
}

// StartEmbeddedPlayback begins the embedded FFmpeg decoder pipeline for the given URL.
func StartEmbeddedPlayback(config *Config, url string) {
	StopEmbeddedPlayback()
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
	embeddedPlayer.mu.Unlock()

	go func() {
		ffmpegPath := getToolPath("ffmpeg", config)
		ffprobePath := getToolPath("ffprobe", config)

		w, h, dur, fps := probeVideoInfo(ffprobePath, url)

		videoPlaybackMutex.Lock()
		videoPlaybackPhase = "playing"
		videoPlaybackProgress = 1.0
		videoPlaybackMutex.Unlock()

		embeddedPlayer.mu.Lock()
		embeddedPlayer.phase = "playing"
		embeddedPlayer.width = w
		embeddedPlayer.height = h
		embeddedPlayer.duration = dur
		embeddedPlayer.frameRate = fps
		embeddedPlayer.decoderStartTime = time.Now()
		embeddedPlayer.progressBase = 0
		embeddedPlayer.progress = 0
		embeddedPlayer.mu.Unlock()

		startFFmpegDecoder(ffmpegPath, url, w, h)
		// Audio is not yet piped to SDL2; video-only for now.

		// If decoder exits, stop playback
		StopEmbeddedPlayback()
	}()
}

// StopEmbeddedPlayback halts the embedded decoder and clears state.
func StopEmbeddedPlayback() {
	if ffmpegCtx != nil {
		ffmpegCtx()
		ffmpegCtx = nil
	}
	if audioCtx != nil {
		audioCtx()
		audioCtx = nil
	}
	if ffmpegCmd != nil {
		ffmpegCmd.Process.Kill()
		ffmpegCmd.Wait()
		ffmpegCmd = nil
	}
	if audioCmd != nil {
		audioCmd.Process.Kill()
		audioCmd.Wait()
		audioCmd = nil
	}

	embeddedPlayer.mu.Lock()
	prevPhase := embeddedPlayer.phase
	embeddedPlayer.phase = "idle"
	embeddedPlayer.currentRaw = nil
	embeddedPlayer.mu.Unlock()

	if prevPhase != "idle" {
		clearVideoFrameQueue()
	}

	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "idle"
	videoPlaybackProgress = 0
	videoPlaybackCmd = nil
	path := embeddedPlaybackPath
	embeddedPlaybackPath = ""
	videoPlaybackMutex.Unlock()

	if path != "" {
		os.Remove(path)
	}
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
	case "paused":
		embeddedPlayer.totalPausedDuration += time.Since(embeddedPlayer.pauseStartTime).Seconds()
		embeddedPlayer.pauseStartTime = time.Time{}
		embeddedPlayer.phase = "playing"
	}
}

// toggleVideoPlayback is the unexported alias used by main.go menu buttons.
func toggleVideoPlayback() {
	ToggleVideoPlayback()
}

// SeekVideo seeks to the given fraction (0..1) by restarting ffmpeg with -ss.
func SeekVideo(fraction float64) {
	videoPlaybackMutex.Lock()
	url := embeddedPlaybackPath
	videoPlaybackMutex.Unlock()

	embeddedPlayer.mu.Lock()
	if embeddedPlayer.phase == "idle" {
		embeddedPlayer.mu.Unlock()
		return
	}
	width := embeddedPlayer.width
	height := embeddedPlayer.height
	duration := embeddedPlayer.duration
	embeddedPlayer.mu.Unlock()

	if url == "" || width <= 0 || height <= 0 {
		return
	}

	ffmpegPath := getToolPath("ffmpeg", appConfig)
	if ffmpegPath == "" {
		return
	}

	if ffmpegCtx != nil {
		ffmpegCtx()
		ffmpegCtx = nil
	}

	embeddedPlayer.mu.Lock()
	embeddedPlayer.progress = fraction
	if embeddedPlayer.progress < 0 {
		embeddedPlayer.progress = 0
	}
	if embeddedPlayer.progress > 1 {
		embeddedPlayer.progress = 1
	}
	embeddedPlayer.progressBase = embeddedPlayer.progress
	embeddedPlayer.mu.Unlock()

	go func() {
		seekSec := fraction * duration
		args := []string{
			"-re",
			"-ss", fmt.Sprintf("%.3f", seekSec),
			"-i", url,
			"-f", "rawvideo",
			"-pix_fmt", "rgba",
			"-s", fmt.Sprintf("%dx%d", width, height),
			"-",
		}
		cmd := exec.Command(ffmpegPath, args...)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return
		}
		ffmpegCmd = cmd

		embeddedPlayer.mu.Lock()
		embeddedPlayer.decoderStartTime = time.Now()
		embeddedPlayer.mu.Unlock()

		frameSize := int(width * height * 4)
		buf := make([]byte, frameSize)

		for {
			_, err := io.ReadFull(pipe, buf)
			if err != nil {
				break
			}

			embeddedPlayer.mu.Lock()
			embeddedPlayer.currentRaw = make([]byte, len(buf))
			copy(embeddedPlayer.currentRaw, buf)
			embeddedPlayer.width = width
			embeddedPlayer.height = height
			embeddedPlayer.lastFrameTime = time.Now()
			embeddedPlayer.mu.Unlock()
		}

		cmd.Wait()
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
	state := GetEmbeddedPlayerState()
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
			log.Printf("[VIDEO] currentTex is nil after UpdateCurrentTexture, raw=%v, w=%d, h=%d", state.currentRaw != nil, state.width, state.height)
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
		a := uint8(90 * (1.0 - float64(s)/float64(topGradH)))
		renderer.SetDrawColor(0, 0, 0, a)
		renderer.FillRect(&sdl.Rect{X: x, Y: y + s, W: w, H: 3})
	}
	bottomGradH := int32(140)
	bottomY := y + h - bottomGradH
	for s := int32(0); s < bottomGradH; s += 3 {
		a := uint8(180 * (float64(s) / float64(bottomGradH)))
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
	fillRoundedRect(renderer, badgeX, badgeY, int32(phaseW)+18, 26, 13, WithAlpha(ColorSurfaceRaised, 230))
	renderText(renderer, config, font, phaseText, phaseCol, badgeX+9, badgeY+6)

	// Top-right: back and volume.
	volText := formatPercentSimple(state.volume)
	volW, _, _ := font.SizeUTF8(volText)
	backW, _, _ := font.SizeUTF8("✕")
	backX := x + w - int32(backW) - 26
	fillRoundedRect(renderer, backX-2, badgeY-2, int32(backW)+20, 30, 10, WithAlpha(ColorSurfaceRaised, 180))
	renderText(renderer, config, font, "✕", ColorTextPrimary(), backX+6, badgeY+4)
	videoControlRects.back = sdl.Rect{X: backX - 2, Y: badgeY - 2, W: int32(backW) + 20, H: 30}
	renderText(renderer, config, font, volText, ColorTextPrimary(), x+w-int32(volW)-50, y+18)

	// Progress bar sits just above the control bar.
	seekBarH := int32(6)
	seekBarY := barY - 16
	seekBarW := w - 48
	seekBarX := x + 24
	fillRoundedRect(renderer, seekBarX, seekBarY, seekBarW, seekBarH, seekBarH/2, sdl.Color{R: 40, G: 40, B: 50, A: 255})
	if state.progress > 0 {
		filledW := int32(float64(seekBarW) * state.progress)
		if filledW > 0 {
			fillRoundedRect(renderer, seekBarX, seekBarY, filledW, seekBarH, seekBarH/2, accent)
		}
	}
	renderer.SetDrawColor(255, 255, 255, 30)
	renderer.FillRect(&sdl.Rect{X: seekBarX, Y: seekBarY, W: seekBarW, H: 1})
	thumbX := seekBarX + int32(float64(seekBarW)*state.progress)
	fillCircle(renderer, thumbX, seekBarY+seekBarH/2, 7, accent)
	videoControlRects.progress = sdl.Rect{X: seekBarX, Y: seekBarY - 8, W: seekBarW, H: 20}

	// Control buttons row, centered within the bar.
	btnSize := int32(36)
	gap := int32(10)
	// Order: stop, seek back, play/pause, seek forward, speed.
	groupW := 5*btnSize + 4*gap
	startX := x + (w-groupW)/2
	btnY := barY + (barH-btnSize)/2

	videoControlRects.stop = drawIconBtn(renderer, config, font, "⏹", startX, btnY, btnSize, state)
	startX += btnSize + gap
	videoControlRects.seekBack = drawIconBtn(renderer, config, font, "⏪", startX, btnY, btnSize, state)
	startX += btnSize + gap
	playIcon := "⏸"
	if state.phase == "paused" {
		playIcon = "⏵"
	}
	videoControlRects.playPause = drawIconBtn(renderer, config, font, playIcon, startX, btnY, btnSize, state)
	startX += btnSize + gap
	videoControlRects.seekFwd = drawIconBtn(renderer, config, font, "⏩", startX, btnY, btnSize, state)
	startX += btnSize + gap
	speedLabel := fmt.Sprintf("%.1fx", state.speed)
	videoControlRects.speed = drawIconBtn(renderer, config, font, speedLabel, startX, btnY, btnSize, state)

	// Volume +/- on the left of the bar.
	volX := x + 18
	videoControlRects.volDown = drawIconBtn(renderer, config, font, "🔉", volX, btnY, btnSize, state)
	videoControlRects.volUp = drawIconBtn(renderer, config, font, "🔊", volX+btnSize+6, btnY, btnSize, state)

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

func drawIconBtn(renderer *sdl.Renderer, config *Config, font *ttf.Font, icon string, x, y, size int32, state *VideoPlayerState) sdl.Rect {
	_ = state
	fillRoundedRect(renderer, x, y, size, size, size/5, WithAlpha(ColorSurfaceRaised, 180))
	renderer.SetDrawColor(255, 255, 255, 25)
	renderer.FillRect(&sdl.Rect{X: x + 3, Y: y + 1, W: size - 6, H: 1})
	textY := y + (size-int32(font.Height()))/2
	renderText(renderer, config, font, icon, ColorTextPrimary(), x+size/2-int32(len([]rune(icon))*8)/2, textY)
	return sdl.Rect{X: x, Y: y, W: size, H: size}
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
	state := GetEmbeddedPlayerState()
	if state.phase == "idle" {
		return
	}

	if pointInRect(mx, my, videoControlRects.playPause) {
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
	state := GetEmbeddedPlayerState()
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
	state := GetEmbeddedPlayerState()
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
