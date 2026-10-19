package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Audio player
//
// The player is opened from the Files scene: picking an audio file starts a
// playlist made of every audio file in that folder. Playback decodes with the
// bundled ffmpeg into raw PCM and queues it on the SDL2 device the video
// player already uses (see QueueVideoAudio in sound.go). ffplay used to be
// spawned here, but the bundled ffplay is built against X11 and aborts on the
// handheld, so it was silent on the device.
// ──────────────────────────────────────────────────────────────────────

const (
	visualBars   = 32
	visualBarGap = 4
	visualMaxH   = 120
)

// audioFileExtensions are the extensions the playlist accepts.
var audioFileExtensions = []string{".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".opus", ".wma"}

type musicTrack struct {
	Path     string
	Name     string
	Duration float64 // seconds, 0 until ffprobe answers
}

type musicState struct {
	tracks   []musicTrack
	current  int
	playing  bool
	paused   bool
	volume   float64
	position float64 // seconds into the current track
	duration float64 // seconds, 0 while unknown
	dir      string  // folder the playlist was built from
	cursor   int     // highlighted playlist row
	repeat   bool

	// Long-form extras (audiobooks, podcasts).
	speed      float64   // listening speed, 0.75..2 (see audioSpeedSteps)
	sleepUntil time.Time // zero when no sleep timer is armed
	sleepGen   int       // invalidates a timer armed before the last stop
	lyrics     []lyricLine
	showLyrics bool
	lastSave   time.Time // last resume write, to keep SD card writes rare

	visualizer [visualBars]float64

	mu       sync.Mutex
	ready    bool
	gen      int       // bumps on every stop/start so stale decoders stay quiet
	proc     *exec.Cmd // running ffmpeg decoder
	pcmStart float64   // decode offset in seconds
	pcmBytes int64     // bytes decoded since pcmStart
	rate     int32     // device sample rate of the running decoder
}

var music = musicState{current: -1, volume: 0.8}

// ──────────────────────────────────────────────────────────────────────
// Playlist helpers
// ──────────────────────────────────────────────────────────────────────

// isAudioFile reports whether a file name is handled by the audio player.
func isAudioFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, want := range audioFileExtensions {
		if ext == want {
			return true
		}
	}
	return false
}

// audioFilesIn lists the audio files of one folder, sorted by name.
func audioFilesIn(dir string) []FileEntry {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []FileEntry
	for _, e := range entries {
		if e.IsDir() || !isAudioFile(e.Name()) {
			continue
		}
		out = append(out, FileEntry{Name: e.Name(), Path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func sameAudioPath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
	}
	return strings.EqualFold(a, b)
}

// musicLoadPlaylist replaces the playlist with the audio files of dir and
// returns the index of startPath (0 when it is unknown or empty).
func musicLoadPlaylist(dir, startPath string) int {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	files := audioFilesIn(dir)
	tracks := make([]musicTrack, 0, len(files))
	start := 0
	for _, f := range files {
		if startPath != "" && sameAudioPath(f.Path, startPath) {
			start = len(tracks)
		}
		tracks = append(tracks, musicTrack{Path: f.Path, Name: f.Name})
	}

	music.mu.Lock()
	music.tracks = tracks
	music.dir = dir
	music.cursor = start
	music.current = -1
	music.playing = false
	music.paused = false
	music.position = 0
	music.duration = 0
	music.ready = true
	music.mu.Unlock()
	return start
}

// ──────────────────────────────────────────────────────────────────────
// Scene plumbing
// ──────────────────────────────────────────────────────────────────────

// ensureMusicScene returns the index of the audio player scene, adding it to
// the config when the user's jukaconfig.json predates the player.
func ensureMusicScene(config *Config) int {
	if config == nil {
		return -1
	}
	for i, scene := range config.Scenes {
		if scene.Name == "MusicPlayer" {
			return i
		}
	}
	config.Scenes = append(config.Scenes, SceneConfig{
		Name:        "MusicPlayer",
		Icon:        "~",
		Description: "Audio player",
		Background:  "#0B0F17",
	})
	return len(config.Scenes) - 1
}

func openMusicPlayerScene(config *Config) {
	idx := ensureMusicScene(config)
	if idx < 0 {
		return
	}
	if idx != currentSceneIndex {
		changeSceneTo(config, idx)
	}
}

// openAudioPlayer starts the file at path and switches to the player, using
// every audio file in its folder as the playlist.
func openAudioPlayer(config *Config, path string) {
	dir := filepath.Dir(path)
	idx := musicLoadPlaylist(dir, path)
	if len(music.tracks) == 0 {
		showToast("No audio files in "+dir, ToastInfo())
		return
	}
	openMusicPlayerScene(config)
	musicPlayTrack(idx)
}

// openAudioPlayerBrowser opens the player for a folder. It only restarts
// playback when nothing from that folder is playing already.
func openAudioPlayerBrowser(config *Config, dir string) {
	if strings.TrimSpace(dir) == "" {
		dir = musicDefaultDir(config)
	}
	music.mu.Lock()
	alreadyPlaying := music.playing && sameAudioPath(music.dir, dir)
	music.mu.Unlock()
	if alreadyPlaying {
		openMusicPlayerScene(config)
		return
	}
	idx := musicLoadPlaylist(dir, "")
	if len(music.tracks) == 0 {
		showToast("No audio files in "+dir, ToastInfo())
		openMusicPlayerScene(config)
		return
	}
	openMusicPlayerScene(config)
	musicPlayTrack(idx)
}

// musicCurrentDir reports the folder the playlist was built from.
func musicCurrentDir() string {
	music.mu.Lock()
	defer music.mu.Unlock()
	return music.dir
}

// musicDefaultDir returns the folder to browse when the player is opened cold:
// the last playlist folder, else the configured Files root (e.g. /media on the
// handheld), else the working directory.
func musicDefaultDir(config *Config) string {
	if dir := musicCurrentDir(); strings.TrimSpace(dir) != "" {
		return dir
	}
	if config != nil {
		if root := strings.TrimSpace(config.Variables.FileExplorerRoot); root != "" {
			if st, err := os.Stat(root); err == nil && st.IsDir() {
				return root
			}
		}
	}
	return "."
}

// musicSceneEnter prepares the player when its scene is opened. An active
// playlist survives leaving and re-entering the scene.
func musicSceneEnter() {
	music.mu.Lock()
	ready := music.ready
	dir := music.dir
	if music.speed <= 0 {
		music.speed = audioSavedSpeed()
	}
	music.mu.Unlock()
	if ready {
		return
	}
	if strings.TrimSpace(dir) == "" {
		dir = musicDefaultDir(appConfig)
	}
	musicLoadPlaylist(dir, "")
}

// ──────────────────────────────────────────────────────────────────────
// Transport
// ──────────────────────────────────────────────────────────────────────

// musicPlayTrackHook, when set, replaces the real starter. Tests install one
// so no decoder process is spawned.
var musicPlayTrackHook func(int)

func musicPlayTrack(index int) {
	if musicPlayTrackHook != nil {
		musicPlayTrackHook(index)
		return
	}
	musicPlayTrackNow(index)
}

func musicPlayTrackNow(index int) {
	music.mu.Lock()
	if index < 0 || index >= len(music.tracks) {
		music.mu.Unlock()
		return
	}
	track := music.tracks[index]
	previousPath, previousPos, previousDur := "", 0.0, 0.0
	if music.current >= 0 && music.current < len(music.tracks) {
		previousPath = music.tracks[music.current].Path
		previousPos, previousDur = music.position, music.duration
	}
	music.current = index
	music.cursor = index
	music.playing = true
	music.paused = false
	music.duration = track.Duration
	resume := audioStartPosition(audioResumePosition(track.Path), track.Duration)
	music.position = resume
	music.lyrics = loadLyricsFor(track.Path)
	music.lastSave = time.Now()
	music.mu.Unlock()

	if previousPath != "" && !sameAudioPath(previousPath, track.Path) {
		audioRememberPosition(previousPath, previousPos, previousDur)
	}

	musicStartDecode(track.Path, resume)
	PlayActivateSound()
	go musicProbeDuration(track.Path)
}

// musicProbeDuration fills in the track length once ffprobe answers. It never
// triggers a download: a missing ffprobe only leaves the duration blank.
func musicProbeDuration(path string) {
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	probePath := getToolPath("ffprobe", cfg)
	if probePath == "" {
		return
	}
	if info, err := os.Stat(probePath); err != nil || info.Size() == 0 {
		return
	}
	_, _, _, dur := probeVideoInfo(probePath, path)
	if dur <= 0 {
		return
	}
	music.mu.Lock()
	defer music.mu.Unlock()
	if music.current >= 0 && music.current < len(music.tracks) &&
		sameAudioPath(music.tracks[music.current].Path, path) {
		music.tracks[music.current].Duration = dur
		music.duration = dur
		return
	}
	for i := range music.tracks {
		if sameAudioPath(music.tracks[i].Path, path) {
			music.tracks[i].Duration = dur
		}
	}
}

// musicDecodeHook, when set, replaces the ffmpeg decoder. Tests install one so
// seeking and transport logic can be checked without spawning processes.
var musicDecodeHook func(path string, startSec float64, gen int)

// musicStartDecode runs one ffmpeg decoder for path from startSec. It stops
// whatever was playing before.
func musicStartDecode(path string, startSec float64) {
	music.mu.Lock()
	music.gen++
	gen := music.gen
	music.pcmBytes = 0
	music.pcmStart = startSec
	music.position = startSec
	prev := music.proc
	music.proc = nil
	music.mu.Unlock()

	if prev != nil && prev.Process != nil {
		_ = prev.Process.Kill()
	}
	ClearVideoAudioQueue()
	if musicDecodeHook != nil {
		musicDecodeHook(path, startSec, gen)
		return
	}
	go musicDecode(path, startSec, gen)
}

// musicDecode streams decoded PCM into the SDL audio device. It paces itself
// through QueueVideoAudio (which blocks while the queue is full) and honours
// pause without dropping samples.
func musicDecode(path string, startSec float64, gen int) {
	cfg := appConfig
	if cfg == nil {
		cfg = &Config{}
	}
	ffmpegPath, err := helperToolPath("ffmpeg", cfg)
	if err != nil {
		log.Printf("[ERROR] musicDecode: ffmpeg unavailable: %v", err)
		showToast(toolUnavailableMessage("ffmpeg", err), ToastError())
		return
	}
	rate := GetVideoAudioRate()
	if rate <= 0 {
		rate = 44100
	}
	music.mu.Lock()
	speed := music.speed
	music.mu.Unlock()
	if speed <= 0 {
		speed = 1
	}
	args := musicFFmpegArgs(path, startSec, speed, rate)

	cmd := execCommand(ffmpegPath, args...)
	if cmd == nil {
		return
	}
	cmd.Stderr = nil
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[ERROR] musicDecode: stdout pipe: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[ERROR] musicDecode: start ffmpeg: %v", err)
		showToast("Could not start audio playback", ToastError())
		return
	}

	music.mu.Lock()
	if gen != music.gen {
		music.mu.Unlock()
		_ = cmd.Process.Kill()
		return
	}
	music.proc = cmd
	music.rate = rate
	music.mu.Unlock()

	buf := make([]byte, 32*1024)
	// One scratch buffer for the volume-adjusted copy. QueueVideoAudio hands
	// the bytes to SDL, which copies them, so the buffer is free to reuse
	// immediately; allocating 32KB per read was pure GC pressure while music
	// plays for hours.
	pcm := make([]byte, len(buf))
	var savePos, saveDur float64
loop:
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 {
			var vol float64
			for {
				music.mu.Lock()
				stale := gen != music.gen || !music.playing
				paused := music.paused
				vol = music.volume
				music.mu.Unlock()
				if stale {
					break loop
				}
				if !paused {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			copy(pcm[:n], buf[:n])
			QueueVideoAudio(pcm[:n], vol)

			music.mu.Lock()
			if gen == music.gen {
				music.pcmBytes += int64(n)
				elapsed := musicMediaElapsed(music.pcmStart, music.pcmBytes, rate, speed)
				if music.duration <= 0 || elapsed < music.duration {
					music.position = elapsed
				} else {
					music.position = music.duration
				}
				// Persist a long-form position every ten seconds so a crash, a
				// battery pull or a launcher restart does not lose the chapter.
				if time.Since(music.lastSave) > 10*time.Second {
					music.lastSave = time.Now()
					savePos, saveDur = elapsed, music.duration
				}
			}
			music.mu.Unlock()
			if savePos > 0 {
				audioRememberPosition(path, savePos, saveDur)
				savePos = 0
			}
		}
		if readErr != nil {
			break
		}
	}

	_ = cmd.Wait()

	music.mu.Lock()
	finished := gen == music.gen && music.playing
	reachedEnd := false
	if gen == music.gen {
		music.proc = nil
		music.playing = false
		if music.duration > 0 {
			music.position = music.duration
			reachedEnd = true
		}
	}
	next := music.current + 1
	total := len(music.tracks)
	repeat := music.repeat
	music.mu.Unlock()

	if !finished {
		return
	}
	if reachedEnd {
		// A finished book or episode starts from the top next time.
		audioForgetPosition(path)
	}
	if repeat {
		musicPlayTrack(music.current)
		return
	}
	if next < total {
		musicPlayTrack(next)
		return
	}
	ClearVideoAudioQueue()
}

// musicStop halts playback and clears the audio queue.
func musicStop() {
	music.mu.Lock()
	music.gen++
	music.sleepGen++ // a stop cancels any armed sleep timer
	music.sleepUntil = time.Time{}
	proc := music.proc
	music.proc = nil
	music.playing = false
	music.paused = false
	music.pcmBytes = 0
	path, pos, dur := "", 0.0, 0.0
	if music.current >= 0 && music.current < len(music.tracks) {
		path, pos, dur = music.tracks[music.current].Path, music.position, music.duration
	}
	music.position = 0
	music.mu.Unlock()

	if proc != nil && proc.Process != nil {
		_ = proc.Process.Kill()
	}
	ClearVideoAudioQueue()
	// Keep the place for long-form audio; short music is forgotten by the
	// resume rule when it is played again.
	audioRememberPosition(path, pos, dur)
}

// ──────────────────────────────────────────────────────────────────────
// Long-form playback: speed, sleep timer, lyrics
// ──────────────────────────────────────────────────────────────────────

// musicSetSpeed changes the listening speed and restarts the decoder at the
// same position (ffmpeg's atempo filter is applied at start).
func musicSetSpeed(v float64) float64 {
	v = audioClampSpeed(v)
	music.mu.Lock()
	if music.speed == v {
		music.mu.Unlock()
		return v
	}
	music.speed = v
	playing := music.playing
	paused := music.paused
	path, pos := "", 0.0
	if music.current >= 0 && music.current < len(music.tracks) {
		path, pos = music.tracks[music.current].Path, music.position
	}
	music.mu.Unlock()

	audioSaveSpeed(v)
	if playing && path != "" {
		music.mu.Lock()
		music.paused = paused
		music.mu.Unlock()
		musicStartDecode(path, pos)
	}
	return v
}

// musicCycleSpeed steps through the speed presets. delta is +1 (faster) or -1
// (slower); it wraps around the list.
func musicCycleSpeed(delta int) float64 {
	music.mu.Lock()
	cur := music.speed
	music.mu.Unlock()
	idx := 0
	best := math.MaxFloat64
	for i, s := range audioSpeedSteps {
		if d := math.Abs(s - cur); d < best {
			best, idx = d, i
		}
	}
	n := len(audioSpeedSteps)
	idx = ((idx+delta)%n + n) % n
	return musicSetSpeed(audioSpeedSteps[idx])
}

// musicSetSleepTimer arms a stop after d (0 cancels). The timer lives on its
// own goroutine: playback continues when the player scene is left, and the
// timer has to fire anyway.
func musicSetSleepTimer(d time.Duration) {
	music.mu.Lock()
	music.sleepGen++
	gen := music.sleepGen
	if d <= 0 {
		music.sleepUntil = time.Time{}
		music.mu.Unlock()
		return
	}
	music.sleepUntil = time.Now().Add(d)
	music.mu.Unlock()

	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		<-timer.C
		music.mu.Lock()
		valid := gen == music.sleepGen
		playing := music.playing
		music.mu.Unlock()
		if valid && playing {
			musicStop()
			showToast("Sleep timer stopped playback", ToastInfo())
		}
	}()
}

// musicCycleSleepTimer cycles off -> 15 -> 30 -> 60 minutes and reports the
// armed number of minutes (0 = off).
func musicCycleSleepTimer() int {
	remaining := musicSleepRemaining()
	switch {
	case remaining == 0:
		musicSetSleepTimer(15 * time.Minute)
		return 15
	case remaining <= 15*time.Minute:
		musicSetSleepTimer(30 * time.Minute)
		return 30
	case remaining <= 30*time.Minute:
		musicSetSleepTimer(60 * time.Minute)
		return 60
	default:
		musicSetSleepTimer(0)
		return 0
	}
}

// musicSleepRemaining is how long the armed timer has left (0 when none).
func musicSleepRemaining() time.Duration {
	music.mu.Lock()
	defer music.mu.Unlock()
	if music.sleepUntil.IsZero() {
		return 0
	}
	left := time.Until(music.sleepUntil)
	if left < 0 {
		return 0
	}
	return left
}

// musicToggleLyrics shows or hides the .lrc panel. It returns false when the
// current track has no lyrics file.
func musicToggleLyrics() bool {
	music.mu.Lock()
	defer music.mu.Unlock()
	if len(music.lyrics) == 0 {
		return false
	}
	music.showLyrics = !music.showLyrics
	return true
}

// musicStopIfPlaying stops background audio before a video takes the device.
func musicStopIfPlaying() {
	music.mu.Lock()
	playing := music.playing
	music.mu.Unlock()
	if playing {
		musicStop()
	}
}

func musicTogglePause() {
	music.mu.Lock()
	if !music.playing {
		hasTrack := music.current >= 0 && music.current < len(music.tracks)
		index := music.current
		music.mu.Unlock()
		if hasTrack {
			musicPlayTrack(index)
		}
		return
	}
	music.paused = !music.paused
	paused := music.paused
	path, pos, dur := "", 0.0, 0.0
	if music.current >= 0 && music.current < len(music.tracks) {
		path, pos, dur = music.tracks[music.current].Path, music.position, music.duration
	}
	music.mu.Unlock()
	if paused {
		audioRememberPosition(path, pos, dur)
	}

	// Pausing is done by the decoder (it stops queueing) plus dropping what is
	// still buffered, so no audio trails after the tap. The shared SDL device is
	// deliberately left running: pausing it while the decoder is blocked on a
	// full queue would deadlock both goroutines.
	if paused {
		ClearVideoAudioQueue()
		PlayBackSound()
	} else {
		PlayActivateSound()
	}
}

// musicSeek jumps to a position in seconds, restarting the decoder at the new
// offset. The queue is cleared so no audio from the old position survives.
func musicSeek(seconds float64) {
	music.mu.Lock()
	if music.current < 0 || music.current >= len(music.tracks) {
		music.mu.Unlock()
		return
	}
	if music.duration > 0 {
		if seconds < 0 {
			seconds = 0
		}
		if seconds > music.duration {
			seconds = music.duration
		}
	} else if seconds < 0 {
		seconds = 0
	}
	path := music.tracks[music.current].Path
	music.position = seconds
	music.paused = false
	music.mu.Unlock()

	musicStartDecode(path, seconds)
}

// musicSeekBy moves the position by delta seconds (positive is forward).
func musicSeekBy(delta float64) {
	music.mu.Lock()
	pos := music.position
	music.mu.Unlock()
	musicSeek(pos + delta)
}

func musicSetVolume(v float64) {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	music.mu.Lock()
	music.volume = v
	music.mu.Unlock()
}

func musicAdjustVolume(delta float64) {
	music.mu.Lock()
	v := music.volume + delta
	music.mu.Unlock()
	musicSetVolume(v)
}

func musicNext() {
	music.mu.Lock()
	total := len(music.tracks)
	current := music.current
	music.mu.Unlock()
	if total == 0 {
		return
	}
	next := current + 1
	if next >= total {
		next = 0
	}
	musicPlayTrack(next)
}

func musicPrev() {
	music.mu.Lock()
	total := len(music.tracks)
	current := music.current
	music.mu.Unlock()
	if total == 0 {
		return
	}
	prev := current - 1
	if prev < 0 {
		prev = total - 1
	}
	musicPlayTrack(prev)
}

// ──────────────────────────────────────────────────────────────────────
// Visualizer (synthetic bars inside the artwork tile)
// ──────────────────────────────────────────────────────────────────────

func musicUpdateVisualizer() {
	music.mu.Lock()
	active := music.playing && !music.paused
	music.mu.Unlock()
	if !active {
		for i := range music.visualizer {
			music.visualizer[i] *= 0.9
		}
		return
	}

	now := float64(time.Now().UnixNano()) / 1e9
	for i := range music.visualizer {
		freq1 := math.Sin(now*3.0+float64(i)*0.5) * 0.3
		freq2 := math.Sin(now*5.0+float64(i)*0.8) * 0.2
		freq3 := math.Sin(now*7.0+float64(i)*1.2) * 0.15
		target := (0.4 + freq1 + freq2 + freq3) * float64(visualMaxH)
		diff := target - music.visualizer[i]
		music.visualizer[i] += diff * 0.15
		if music.visualizer[i] < 0 {
			music.visualizer[i] = 0
		}
		if music.visualizer[i] > float64(visualMaxH) {
			music.visualizer[i] = float64(visualMaxH)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Rendering
// ──────────────────────────────────────────────────────────────────────

// Rects of the interactive parts, recomputed every frame so clicks and touches
// hit exactly what is drawn. rowStart is the playlist index drawn in rows[0],
// so a click on a scrolled list plays the track the user sees.
var musicRects = struct {
	prev, playPause, next, stop sdl.Rect
	volDown, volUp, seek        sdl.Rect
	art                         sdl.Rect // artwork tile; tapping toggles the lyrics view
	rows                        []sdl.Rect
	rowStart                    int
	valid                       bool
}{}

const musicRowHeight = 46

func renderMusicPlayer(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(11, 15, 23, 255)
	renderer.Clear()

	font, _ := getCachedFont(config, "small")
	medFont, _ := getCachedFont(config, "medium")
	bigFont, _ := getCachedFont(config, "big")
	if font == nil {
		return
	}
	if medFont == nil {
		medFont = font
	}
	if bigFont == nil {
		bigFont = medFont
	}

	// Header.
	const headerH = int32(56)
	renderer.SetDrawColor(17, 23, 37, 255)
	renderer.FillRect(&sdl.Rect{X: 0, Y: 0, W: screenWidth, H: headerH})
	renderer.SetDrawColor(255, 255, 255, 18)
	renderer.FillRect(&sdl.Rect{X: 0, Y: headerH - 1, W: screenWidth, H: 1})
	renderText(renderer, config, medFont, "Audio Player", ColorTextPrimary(), 24, (headerH-int32(medFont.Height()))/2)
	back := "B  Back"
	bw, _, _ := font.SizeUTF8(back)
	renderText(renderer, config, font, back, ColorTextTertiary(), screenWidth-int32(bw)-24, (headerH-int32(font.Height()))/2)

	music.mu.Lock()
	tracks := append([]musicTrack(nil), music.tracks...)
	cur := music.current
	cursor := music.cursor
	playing := music.playing
	paused := music.paused
	position := music.position
	duration := music.duration
	volume := music.volume
	dir := music.dir
	speed := music.speed
	lyrics := music.lyrics
	showLyrics := music.showLyrics
	music.mu.Unlock()
	if speed <= 0 {
		speed = 1
	}
	sleepLeft := musicSleepRemaining()

	musicRects.rows = musicRects.rows[:0]
	musicRects.valid = true

	// ── Now playing card ──
	cardX := int32(24)
	cardY := headerH + 20
	cardW := screenWidth - 2*cardX
	cardH := int32(292)
	drawCard(renderer, cardX, cardY, cardW, cardH, 18)

	// Artwork tile with the synthetic visualizer inside.
	artSize := int32(236)
	artX := cardX + 24
	artY := cardY + (cardH-artSize)/2
	fillRoundedRect(renderer, artX, artY, artSize, artSize, 16, WithAlpha(ColorPanelRaised, 255))
	strokeRoundedRect(renderer, artX, artY, artSize, artSize, 16, 1, WithAlpha(ColorBorder, 200))
	musicUpdateVisualizer()
	musicRects.art = sdl.Rect{X: artX, Y: artY, W: artSize, H: artSize}
	if showLyrics && len(lyrics) > 0 {
		renderLyricsTile(renderer, config, font, lyrics, position, artX, artY, artSize)
	} else {
		barW := int32(6)
		barGap := int32(7)
		bars := 12
		totalW := int32(bars)*(barW+barGap) - barGap
		barBase := artY + artSize - 34
		for i := 0; i < bars; i++ {
			h := int32(music.visualizer[(i*visualBars/bars)%visualBars] * 1.1)
			if h < 6 {
				h = 6
			}
			// Leave room for the state caption above the bars.
			if h > artSize-116 {
				h = artSize - 116
			}
			bx := artX + (artSize-totalW)/2 + int32(i)*(barW+barGap)
			col := WithAlpha(accentColor, 220)
			if !playing || paused {
				col = WithAlpha(accentColor, 110)
			}
			fillRoundedRect(renderer, bx, barBase-h, barW, h, 3, col)
		}
	}
	stateLabel := "Paused"
	if !playing {
		stateLabel = "Stopped"
	} else if !paused {
		stateLabel = "Playing"
	}
	if showLyrics && len(lyrics) > 0 {
		stateLabel = "Lyrics"
	}
	sw, _, _ := font.SizeUTF8(stateLabel)
	renderText(renderer, config, font, stateLabel, ColorTextTertiary(),
		artX+(artSize-int32(sw))/2, artY+18)

	// Title and meta.
	textX := artX + artSize + 32
	textW := cardX + cardW - 24 - textX

	title := "Nothing playing"
	meta := "Pick a song from the playlist below"
	if cur >= 0 && cur < len(tracks) {
		title = tracks[cur].Name
		title = strings.TrimSuffix(title, filepath.Ext(title))
		meta = fmt.Sprintf("Track %d of %d", cur+1, len(tracks))
		if dir != "" {
			meta += "   ·   " + dir
		}
	}
	title = ellipsize(bigFont, title, textW)
	renderText(renderer, config, bigFont, title, ColorTextPrimary(), textX, cardY+30)
	meta = ellipsize(font, meta, textW)
	renderText(renderer, config, font, meta, ColorTextTertiary(), textX, cardY+30+int32(bigFont.Height())+8)

	// Playback status: speed, sleep timer and lyric availability. Keeping this
	// on its own line means the meta line stays stable while the timer counts
	// down instead of re-flowing the title.
	status := formatSpeedLabel(speed)
	if sleepLeft > 0 {
		status += "   ·   Sleep " + formatSleepRemaining(sleepLeft)
	}
	if len(lyrics) > 0 {
		if showLyrics {
			status += "   ·   Lyrics on"
		} else {
			status += "   ·   Lyrics ready"
		}
	}
	statusY := cardY + 30 + int32(bigFont.Height()) + 8 + int32(font.Height()) + 6
	renderText(renderer, config, font, ellipsize(font, status, textW), ColorTextSecondary(), textX, statusY)

	// Progress bar with a thumb and times on either side.
	seekY := cardY + 138
	seekX := textX
	seekW := textW
	fillRoundedRect(renderer, seekX, seekY, seekW, 8, 4, ColorTrack)
	frac := 0.0
	if duration > 0 {
		frac = position / duration
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	if frac > 0 {
		fillRoundedRect(renderer, seekX, seekY, int32(float64(seekW)*frac), 8, 4, accentColor)
	}
	thumbX := seekX + int32(float64(seekW)*frac)
	fillRoundedRect(renderer, thumbX-7, seekY-3, 14, 14, 7, ColorTextPrimary())
	musicRects.seek = sdl.Rect{X: seekX, Y: seekY - 10, W: seekW, H: 28}

	elapsedText := formatTime(position)
	totalText := "--:--"
	if duration > 0 {
		totalText = formatTime(duration)
	}
	renderText(renderer, config, font, elapsedText, ColorTextSecondary(), seekX, seekY+18)
	tw, _, _ := font.SizeUTF8(totalText)
	renderText(renderer, config, font, totalText, ColorTextSecondary(), seekX+seekW-int32(tw), seekY+18)

	// Transport row.
	btnY := seekY + 62
	btnH := int32(54)
	iconFont := medFont
	playIcon := ">"
	if playing && !paused {
		playIcon = "II"
	}
	musicRects.prev = musicButton(renderer, config, iconFont, textX, btnY, 54, btnH, "|<", false)
	musicRects.playPause = musicButton(renderer, config, iconFont, textX+66, btnY, 76, btnH, playIcon, true)
	musicRects.next = musicButton(renderer, config, iconFont, textX+154, btnY, 54, btnH, ">|", false)
	musicRects.stop = musicButton(renderer, config, iconFont, textX+220, btnY, 54, btnH, "[]", false)

	// Volume controls, right aligned in the card. The label sits left of the
	// buttons so a long value ("Vol 100%") never slides underneath them.
	volUpX := cardX + cardW - 24 - 44
	volDownX := volUpX - 52
	musicRects.volUp = musicButton(renderer, config, iconFont, volUpX, btnY, 44, btnH, "+", false)
	musicRects.volDown = musicButton(renderer, config, iconFont, volDownX, btnY, 44, btnH, "-", false)
	volLabel := fmt.Sprintf("Vol %d%%", int(math.Round(volume*100)))
	vw, _, _ := font.SizeUTF8(volLabel)
	renderText(renderer, config, font, volLabel, ColorTextSecondary(), volDownX-int32(vw)-14, btnY+16)

	// ── Playlist card ──
	listY := cardY + cardH + 20
	listH := screenHeight - listY - 56
	if listH < 120 {
		listH = 120
	}
	drawCard(renderer, cardX, listY, cardW, listH, 18)

	header := "Playlist"
	if len(tracks) > 0 {
		header = fmt.Sprintf("Playlist   ·   %d track", len(tracks))
		if len(tracks) != 1 {
			header += "s"
		}
	}
	renderText(renderer, config, font, ellipsize(font, header, cardW-48), ColorTextSecondary(), cardX+24, listY+16)

	rowsTop := listY + 54
	visible := int((listH - 66) / musicRowHeight)
	if visible < 1 {
		visible = 1
	}
	start := 0
	if cursor >= visible {
		start = cursor - visible + 1
	}
	end := start + visible
	if end > len(tracks) {
		end = len(tracks)
	}
	musicRects.rowStart = start
	if len(tracks) == 0 {
		empty := "No audio files here. Open Files, enter a folder and pick a song."
		ew, _, _ := font.SizeUTF8(empty)
		renderText(renderer, config, font, ellipsize(font, empty, cardW-48), ColorTextTertiary(),
			cardX+(cardW-int32(ew))/2, rowsTop+28)
	} else {
		for i := start; i < end; i++ {
			rowY := rowsTop + int32(i-start)*musicRowHeight
			row := sdl.Rect{X: cardX + 16, Y: rowY, W: cardW - 32, H: musicRowHeight - 6}
			if i == cursor {
				fillRoundedRect(renderer, row.X, row.Y, row.W, row.H, 10, WithAlpha(ColorCardFocus, 255))
			}
			if i == cur {
				renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 220)
				renderer.FillRect(&sdl.Rect{X: row.X + 3, Y: row.Y + 8, W: 4, H: row.H - 16})
			}
			num := strconv.Itoa(i+1) + "."
			renderText(renderer, config, font, num, ColorTextTertiary(), row.X+18, row.Y+(row.H-int32(font.Height()))/2)
			name := tracks[i].Name
			name = strings.TrimSuffix(name, filepath.Ext(name))
			nameX := row.X + 62
			nameW := row.W - 62 - 96
			col := ColorTextPrimary()
			if i == cur {
				col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 255}
			}
			renderText(renderer, config, font, ellipsize(font, name, nameW), col, nameX, row.Y+(row.H-int32(font.Height()))/2)
			if d := tracks[i].Duration; d > 0 {
				dur := formatTime(d)
				dw, _, _ := font.SizeUTF8(dur)
				renderText(renderer, config, font, dur, ColorTextTertiary(), row.X+row.W-int32(dw)-18, row.Y+(row.H-int32(font.Height()))/2)
			}
			musicRects.rows = append(musicRects.rows, row)
		}
	}

	// Footer hints.
	hint := "ENTER Play   SPACE Pause   LEFT/RIGHT Seek   -/+ Volume   [ ] Speed   T Sleep   L Lyrics   B Back"
	hw, _, _ := font.SizeUTF8(hint)
	renderText(renderer, config, font, hint, sdl.Color{R: 110, G: 125, B: 150, A: 170},
		(screenWidth-int32(hw))/2, screenHeight-36)
}

// musicButton draws one rounded transport button and returns its rect.
func musicButton(renderer *sdl.Renderer, config *Config, font *ttf.Font, x, y, w, h int32, label string, primary bool) sdl.Rect {
	fill := WithAlpha(ColorPanelRaised, 255)
	fg := ColorTextPrimary()
	border := WithAlpha(ColorBorder, 200)
	if primary {
		fill = accentColor
		fg = sdl.Color{R: 10, G: 14, B: 22, A: 255}
		border = accentColor
	}
	fillRoundedRect(renderer, x, y, w, h, 12, fill)
	strokeRoundedRect(renderer, x, y, w, h, 12, 1, border)
	if font != nil {
		lw, lh, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, fg, x+(w-int32(lw))/2, y+(h-int32(lh))/2)
	}
	return sdl.Rect{X: x, Y: y, W: w, H: h}
}

// HandleMusicClick processes mouse/touch clicks on the audio player. It reports
// whether the click was consumed.
func HandleMusicClick(mx, my int32) bool {
	if !musicRects.valid {
		return false
	}
	if pointInRect(mx, my, musicRects.art) {
		if !musicToggleLyrics() {
			showToast("No .lrc file next to this track", ToastInfo())
		}
		return true
	}
	if pointInRect(mx, my, musicRects.playPause) {
		musicTogglePause()
		return true
	}
	if pointInRect(mx, my, musicRects.prev) {
		musicPrev()
		PlayNavSound()
		return true
	}
	if pointInRect(mx, my, musicRects.next) {
		musicNext()
		PlayNavSound()
		return true
	}
	if pointInRect(mx, my, musicRects.stop) {
		musicStop()
		PlayBackSound()
		return true
	}
	if pointInRect(mx, my, musicRects.volUp) {
		musicAdjustVolume(0.1)
		PlayNavSound()
		return true
	}
	if pointInRect(mx, my, musicRects.volDown) {
		musicAdjustVolume(-0.1)
		PlayNavSound()
		return true
	}
	if pointInRect(mx, my, musicRects.seek) && musicRects.seek.W > 0 {
		frac := float64(mx-musicRects.seek.X) / float64(musicRects.seek.W)
		music.mu.Lock()
		dur := music.duration
		music.mu.Unlock()
		if dur > 0 {
			musicSeek(dur * frac)
		}
		return true
	}
	for i, row := range musicRects.rows {
		if pointInRect(mx, my, row) {
			musicPlayTrack(musicRects.rowStart + i)
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────
// Input
// ──────────────────────────────────────────────────────────────────────

func handleMusicInput(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}

	switch e.Keysym.Sym {
	case sdl.K_UP:
		music.mu.Lock()
		if music.cursor > 0 {
			music.cursor--
		}
		music.mu.Unlock()
		PlayNavSound()
	case sdl.K_DOWN:
		music.mu.Lock()
		if music.cursor < len(music.tracks)-1 {
			music.cursor++
		}
		music.mu.Unlock()
		PlayNavSound()
	case sdl.K_RETURN, sdl.K_SPACE:
		if e.Keysym.Sym == sdl.K_SPACE {
			musicTogglePause()
			return
		}
		music.mu.Lock()
		idx := music.cursor
		music.mu.Unlock()
		musicPlayTrack(idx)
	case sdl.K_p:
		musicTogglePause()
	case sdl.K_n:
		musicNext()
	case sdl.K_b:
		musicPrev()
	case sdl.K_s:
		musicStop()
	case sdl.K_l:
		if !musicToggleLyrics() {
			showToast("No .lrc file next to this track", ToastInfo())
		}
	case sdl.K_LEFTBRACKET:
		musicCycleSpeed(-1)
		PlayNavSound()
	case sdl.K_RIGHTBRACKET:
		musicCycleSpeed(1)
		PlayNavSound()
	case sdl.K_t:
		minutes := musicCycleSleepTimer()
		if minutes == 0 {
			showToast("Sleep timer off", ToastInfo())
		} else {
			showToast(fmt.Sprintf("Sleep timer: %d min", minutes), ToastSuccess())
		}
		PlayNavSound()
	case sdl.K_LEFT:
		musicSeekBy(-10)
	case sdl.K_RIGHT:
		musicSeekBy(10)
	case sdl.K_MINUS, sdl.K_KP_MINUS:
		musicAdjustVolume(-0.1)
	case sdl.K_EQUALS, sdl.K_PLUS, sdl.K_KP_PLUS:
		musicAdjustVolume(0.1)
	case sdl.K_ESCAPE:
		goBackScene(config)
		PlayBackSound()
	}
}

// ──────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────

// formatSpeedLabel renders a listening speed without trailing zeros:
// 1 -> "1x", 1.25 -> "1.25x".
func formatSpeedLabel(speed float64) string {
	s := strconv.FormatFloat(speed, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s + "x"
}

// formatSleepRemaining renders the time left on the sleep timer, rounding up so
// it never shows "0m" while audio is still playing.
func formatSleepRemaining(d time.Duration) string {
	mins := int(math.Ceil(d.Minutes()))
	if mins >= 60 {
		return fmt.Sprintf("%dh %02dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
}

// renderLyricsTile draws the synced lyric around the current playback position
// inside the artwork square: the active line in the accent colour, the
// neighbours dimmed above and below. It stays in the artwork slot so the rest
// of the player layout never shifts when lyrics are toggled.
func renderLyricsTile(renderer *sdl.Renderer, config *Config, font *ttf.Font, lines []lyricLine, pos float64, x, y, size int32) {
	if renderer == nil || font == nil || len(lines) == 0 {
		return
	}
	idx := lyricIndexAt(lines, pos)
	lineH := int32(font.Height()) + 10
	midY := y + size/2 - lineH/2
	inner := size - 28

	for off := -2; off <= 2; off++ {
		i := idx + off
		if i < 0 || i >= len(lines) {
			continue
		}
		col := WithAlpha(ColorTextTertiary(), 150)
		if off == 0 {
			col = sdl.Color{R: accentColor.R, G: accentColor.G, B: accentColor.B, A: 255}
		} else if off == -1 || off == 1 {
			col = WithAlpha(ColorTextSecondary(), 190)
		}
		text := ellipsize(font, strings.TrimSpace(lines[i].Text), inner)
		if text == "" {
			continue
		}
		tw, _, _ := font.SizeUTF8(text)
		renderText(renderer, config, font, text, col,
			x+(size-int32(tw))/2, midY+int32(off)*lineH)
	}

	if idx < 0 {
		text := "…"
		tw, _, _ := font.SizeUTF8(text)
		renderText(renderer, config, font, text, WithAlpha(ColorTextTertiary(), 160),
			x+(size-int32(tw))/2, y+size-40)
	}
}

func execCommand(name string, args ...string) *exec.Cmd {
	return P().CommandContext(context.Background(), name, args...)
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlMusicPlayer(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		music.mu.Lock()
		if music.cursor > 0 {
			music.cursor--
		}
		music.mu.Unlock()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		music.mu.Lock()
		if music.cursor < len(music.tracks)-1 {
			music.cursor++
		}
		music.mu.Unlock()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		// A plays the highlighted playlist row.
		music.mu.Lock()
		idx := music.cursor
		music.mu.Unlock()
		musicPlayTrack(idx)
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		musicTogglePause()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(appConfig)
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		musicSeekBy(-10)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		musicSeekBy(10)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		musicPrev()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		musicNext()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_LEFTSTICK:
		if !musicToggleLyrics() {
			showToast("No .lrc file next to this track", ToastInfo())
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		minutes := musicCycleSleepTimer()
		if minutes == 0 {
			showToast("Sleep timer off", ToastInfo())
		} else {
			showToast(fmt.Sprintf("Sleep timer: %d min", minutes), ToastSuccess())
		}
		PlayNavSound()
	default:
		return false
	}
	return true
}

// Scene registration.
type musicScene struct{ sceneBase }

func (musicScene) render(r *sdl.Renderer, c *Config) bool { renderMusicPlayer(r, c); return true }

func (musicScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	handleMusicInput(e, c)
	return true
}

func (musicScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlMusicPlayer(e)
}

func init() { registerScene(musicScene{sceneBase{name: "MusicPlayer"}}) }
