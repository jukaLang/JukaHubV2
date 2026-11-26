package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
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
	// awaitFrame: a decoder started (a video, a seek, a speed change) and has
	// not shown its first frame. The position waits for it
	// (videoAdvanceProgress) instead of running ahead on the wall clock and
	// jumping back when the picture arrives.
	awaitFrame bool
	// mutedLevel is the level a mute put aside (videoToggleMute): unmuting
	// brings it back, and so does the next video.
	mutedLevel float64
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

// Control button hitboxes for the video overlay, laid out every frame by
// vcLayout (videochrome.go). An empty rect is a control the layout dropped.
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
	close       sdl.Rect
	subs        sdl.Rect
	settings    sdl.Rect
	fullscreen  sdl.Rect
	mute        sdl.Rect
	volBar      sdl.Rect
	prev        sdl.Rect
	next        sdl.Rect
}{}

// videoControlBounds is the window the last layout was computed for: the video
// element, or the whole panel in full screen. A windowed player only claims taps
// inside it, so the scene around the video keeps its own targets.
var videoControlBounds sdl.Rect

// Video chrome state: full screen hides the controls until the user interacts,
// and the settings sheet lists subtitles / full screen / speed.
var (
	videoFullscreen   bool
	videoChromeUntil  uint64
	videoSettingsOpen bool
	videoSettingsItem int
	// videoControlFocus is the control the D-pad ring currently sits on. Zero
	// value means "no ring": a touch or the A button then uses the default
	// action instead of a stale selection from the last video.
	videoControlFocus videoControlID
)

// videoControlID names one on-screen player button. The ring walks them in
// drawing order, so it moves left to right along the bar.
type videoControlID int

// maxVideoFrameBytes caps the buffer startFFmpegDecoder allocates for one YUV
// frame. The decode size is already capped by videoDecodeMaxSize, so this is the
// backstop that keeps a header-declared size from reaching make as an int.
const maxVideoFrameBytes = int64(64 << 20)

const (
	vcNone videoControlID = iota
	vcStop
	vcSeekBack
	vcPlayPause
	vcSeekFwd
	vcSpeed
	vcSubs
	vcSettings
	vcFullscreen
	// The two corner actions. Close ends playback where it stands; back steps
	// the player down a size (full screen -> window -> floating mini) without
	// ending it. They used to be one button — an X that meant "back" — so backing
	// out of a windowed video was the only option and nothing could close it
	// except Stop at the far end of the bar.
	vcClose
	vcBack
	// The glass bar's additions (videochrome.go): the speaker that mutes, and
	// previous / next while the video came from a list.
	vcMute
	vcPrev
	vcNext
)

// videoControlEntry pairs a control with the rect it was laid out at.
type videoControlEntry struct {
	id   videoControlID
	rect sdl.Rect
}

// videoControlsInOrder returns the drawn buttons in reading order: the bar left
// to right, then the two corner buttons. The level bar and the seek track are
// pointer targets (Up/Down and the shoulders do their jobs), and a control the
// layout dropped for room is not in the ring.
func videoControlsInOrder() []videoControlEntry {
	all := [...]videoControlEntry{
		{vcMute, videoControlRects.mute},
		{vcPrev, videoControlRects.prev},
		{vcSeekBack, videoControlRects.seekBack},
		{vcPlayPause, videoControlRects.playPause},
		{vcSeekFwd, videoControlRects.seekFwd},
		{vcNext, videoControlRects.next},
		{vcSpeed, videoControlRects.speed},
		{vcSubs, videoControlRects.subs},
		{vcSettings, videoControlRects.settings},
		{vcFullscreen, videoControlRects.fullscreen},
		{vcBack, videoControlRects.back},
		{vcClose, videoControlRects.close},
	}
	out := make([]videoControlEntry, 0, len(all))
	for _, e := range all {
		if e.rect.W > 0 && e.rect.H > 0 {
			out = append(out, e)
		}
	}
	return out
}

// videoControlDrawn reports whether the control is in this frame's layout.
func videoControlDrawn(id videoControlID) bool {
	if id == vcNone {
		return false
	}
	for _, e := range videoControlsInOrder() {
		if e.id == id {
			return true
		}
	}
	return false
}

// moveVideoControlFocus steps the ring by delta, wrapping at both ends. With no
// ring yet, the first press lands on the nearest end of the bar.
func moveVideoControlFocus(delta int) {
	items := videoControlsInOrder()
	if len(items) == 0 {
		return
	}
	idx := -1
	for i, it := range items {
		if it.id == videoControlFocus {
			idx = i
			break
		}
	}
	if idx < 0 {
		// No ring yet: start from play/pause, the button users reach for, so
		// the first press lands on rewind or forward next to it instead of on
		// Stop at the far end of the bar.
		for i, it := range items {
			if it.id == vcPlayPause {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		idx = 0
	}
	idx = (idx + delta + len(items)) % len(items)
	videoControlFocus = items[idx].id
}

// clampSeekFraction converts a relative skip into an absolute clamped fraction.
func clampSeekFraction(progress, duration, seconds float64) float64 {
	if duration <= 0 {
		return 0
	}
	fraction := progress + seconds/duration
	if fraction < 0 {
		return 0
	}
	if fraction > 1 {
		return 1
	}
	return fraction
}

// seekVideoBySeconds seeks relative to the current position, clamped to the
// clip. A media player's skip buttons are counted in seconds, not percent:
// five percent of a ten-minute video is half a minute, which overshot badly.
// Presses in quick succession gather (vcSeekBy, videomotion.go): the handle,
// the clock and the flash ("+30 s") move at once, and the decoders start
// again once, a moment after the last press, instead of on every press.
func seekVideoBySeconds(state videoStateSnapshot, seconds float64) {
	vcSeekBy(state, seconds)
}

// playbackQueue remembers the list the current video came from, so L2/R2 can
// step through it without leaving the player.
type playbackQueueState struct {
	videos []VideoInfo
	index  int
	shorts bool
}

var playbackQueue playbackQueueState

// setPlaybackQueue records the list behind v: the Shorts feed first (a short
// always belongs to it), then the search results of the scene that launched
// the video. Anything else leaves the queue empty.
func setPlaybackQueue(config *Config, v VideoInfo) {
	playbackQueue = playbackQueueState{index: -1}
	url := v.GetURL()
	if config == nil || url == "" {
		return
	}
	if vids := shortsVideos(config); len(vids) > 0 {
		for i := range vids {
			if vids[i].GetURL() == url {
				playbackQueue = playbackQueueState{videos: vids, index: i, shorts: true}
				return
			}
		}
	}
	if currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) {
		if vids, ok := getSceneVideos(config, config.Scenes[currentSceneIndex]); ok {
			for i := range vids {
				if vids[i].GetURL() == url {
					playbackQueue = playbackQueueState{videos: vids, index: i}
					return
				}
			}
		}
	}
}

// skipPlayback moves to the previous/next video of the list that launched the
// current one. The ends of the list answer with a message, not a dead press.
func skipPlayback(config *Config, delta int) bool {
	// A one-entry list is a single video (a link from the browser names its
	// title that way): there is nothing to skip to either.
	if config == nil || playbackQueue.index < 0 || len(playbackQueue.videos) <= 1 {
		ShowToast("No list to skip through", ToastKindInfo)
		return true
	}
	next := playbackQueue.index + delta
	if next < 0 || next >= len(playbackQueue.videos) {
		if delta > 0 {
			ShowToast("No more videos in this list", ToastKindInfo)
		} else {
			ShowToast("This is the first video", ToastKindInfo)
		}
		return true
	}
	// The next video of the same list keeps the speed (videoSpeedMem).
	videoSpeedCarryTo(playbackQueue.videos[next].GetURL())
	if playbackQueue.shorts {
		// A short belongs to the feed: startShortAt keeps auto-advance armed.
		startShortAt(config, next)
	} else {
		playVideoInfo(config, playbackQueue.videos[next])
	}
	PlayActivateSound()
	return true
}

// cyclePlaybackSpeed steps to the next preset speed, wrapping from 2× back to
// 0.5×. The change is real: both decoders restart at the new speed
// (SetPlaybackSpeed).
func cyclePlaybackSpeed() {
	current := snapshotVideoState().speed
	next := videoSpeedSteps[0]
	for _, s := range videoSpeedSteps {
		if s > current {
			next = s
			break
		}
	}
	SetPlaybackSpeed(next)
}

// toggleSubtitlesWithFeedback toggles subtitles and reports the result, since
// the button gives no other visible confirmation.
func toggleSubtitlesWithFeedback() {
	result, msg := toggleSubtitlesFor(appConfig)
	subtitleToggleFeedback(result, msg)
}

// subtitleToggleFeedback says what a press of CC did. A fetch is announced as
// started ("Getting English subtitles…") and announces itself again when it
// lands (videosubs.go), so "on" is only said when cues are really there.
func subtitleToggleFeedback(result subtitleToggle, msg string) {
	switch result {
	case subtitleToggleOff:
		showToast("Subtitles off", ToastInfo())
	case subtitleToggleOn:
		trophyEvent(tkSubtitles) // shown at once; a fetch counts when it lands (videosubs.go)
		showToast("Subtitles on", ToastInfo())
	default:
		if strings.TrimSpace(msg) == "" {
			msg = subtitleStatusLine()
		}
		showToast(msg, ToastInfo())
	}
}

// activateVideoControl runs the control under the ring. With no ring, A keeps
// its old meaning (play/pause) so a single press still does the obvious thing.
func activateVideoControl() {
	state := snapshotVideoState()
	if state.phase == "idle" {
		return
	}
	// A control the layout dropped (previous / next once the list is gone,
	// the speed pill on a narrow player, everything but close while the stream
	// opens) has no ring on screen, so A must not press it — nor a ring whose
	// controls have stepped aside (videomotion.go): A is play/pause then.
	if !videoControlDrawn(videoControlFocus) || !videoChromeVisible() {
		videoControlFocus = vcNone
	}
	switch videoControlFocus {
	case vcStop:
		mediaEndSession(appConfig)
	case vcSeekBack:
		PlayNavSound()
		seekVideoBySeconds(state, -10)
	case vcSeekFwd:
		PlayNavSound()
		seekVideoBySeconds(state, 10)
	case vcSpeed:
		PlayActivateSound()
		cyclePlaybackSpeed()
	case vcSubs:
		PlayActivateSound()
		toggleSubtitlesWithFeedback()
	case vcSettings:
		PlayActivateSound()
		toggleVideoSettings()
	case vcFullscreen:
		PlayActivateSound()
		toggleVideoFullscreen()
	case vcClose:
		// Close ends the session on the spot: the video stops and the screen
		// under it stays, with no window left behind (a file opened from
		// Files returns to Files, see mediaEndSession).
		PlayBackSound()
		mediaEndSession(appConfig)
	case vcBack:
		PlayActivateSound()
		videoBackOut(appConfig)
	case vcMute:
		PlayActivateSound()
		videoToggleMute()
	case vcPrev:
		// skipPlayback answers for itself: a sound when it moves, a toast at
		// the ends of the list.
		skipPlayback(appConfig, -1)
	case vcNext:
		skipPlayback(appConfig, 1)
	default:
		PlayActivateSound()
		vcTogglePlayback()
	}
	revealVideoChrome()
}

// videoChromeVisible reports whether the controls are up (or on their way
// up). While a video plays they step aside a few seconds after the last
// input — in the scene's window as well as in full screen, so the bar no
// longer covers the foot of every film — and they stay while it is paused,
// opening or failed, while the settings sheet is open (the pad still drives
// it) and while a seek gathers. They fade rather than cut (videomotion.go).
func videoChromeVisible() bool {
	return vcChromeShown()
}

// revealVideoChrome brings the controls up for vcIdleHideMS.
func revealVideoChrome() {
	videoChromeUntil = sdl.GetTicks64() + vcIdleHideMS
}

// toggleVideoFullscreen switches between the scene element and full screen.
func toggleVideoFullscreen() {
	videoFullscreen = !videoFullscreen
	videoSettingsOpen = false
	revealVideoChrome()
}

// toggleVideoSettings opens or closes the settings sheet (on its first page).
func toggleVideoSettings() {
	videoSettingsToggle()
}

// playbackSession owns one decode pipeline.
type playbackSession struct {
	id       uint64
	source   string
	pageURL  string
	tempFile bool
	// local is true when the page itself is a file on the card (a video opened
	// from Files or the Downloads library) rather than a stream link. Leaving
	// the player ends a local session; a stream floats into the mini window.
	local bool
	// shown is raised the first time the player draws this session, so the
	// orphan check never closes a file the user has not seen yet (a scene
	// change that could not run left it behind another screen).
	shown atomic.Bool
	// ended is raised with playbackEnded when this session's decoder ran to
	// the end of the file. Such a session is closed by the render thread
	// (videoSyncPlaybackEnd), which hands off to the next Short, Up Next or
	// the screen that opened the file; finish() must not close it first.
	ended atomic.Bool
	// av keeps the picture on the sound (avsync.go).
	av avSync
	// speed is the rate both decoders play at (1 = normal). It is set before
	// the decoders start and never changes after: a new speed is a new
	// session (videoRestartAt). Read by the decoders only.
	speed float64

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

	// playbackEnded is raised by the decoder when ffmpeg reaches the end of the
	// file. It is consumed on the render thread, which tears the session down:
	// the phase used to stay "playing" forever, leaving a frozen last frame and
	// a player that kept swallowing input over the scene underneath it.
	playbackEnded atomic.Bool

	// mediaRetiredFrames holds frame textures a stopped session left behind,
	// guarded by embeddedPlayer.mu. The texture used to stay allocated after
	// Stop (about 1.4 MB of video memory for a 720p frame) until the next video
	// happened to replace it.
	mediaRetiredFrames []*sdl.Texture
)

// mediaReleaseRetiredFrames destroys the frame textures stopped sessions left
// behind. It runs on the render thread from the main loop, the only goroutine
// allowed to destroy a texture of the live renderer.
func mediaReleaseRetiredFrames() {
	embeddedPlayer.mu.Lock()
	retired := mediaRetiredFrames
	mediaRetiredFrames = nil
	embeddedPlayer.mu.Unlock()
	for _, tex := range retired {
		if tex != nil {
			tex.Destroy()
		}
	}
}

func beginPlaybackSession(source, pageURL string, tempFile bool, speed float64) *playbackSession {
	return beginPlaybackSessionAfter(nil, source, pageURL, tempFile, speed)
}

// beginPlaybackSessionAfter is beginPlaybackSession for a restart of after (a
// seek, a speed change, videoRestartAt): it starts nothing and returns nil when
// after is no longer the session on screen. A decoder goroutine restarts its own
// session (the lighter decode, an ffmpeg without the speed filters) while the
// main thread may stop the player or start another video meanwhile; the old
// video must not come back over that. A nil after replaces whatever plays.
func beginPlaybackSessionAfter(after *playbackSession, source, pageURL string, tempFile bool, speed float64) *playbackSession {
	if after != nil && !after.isCurrent() {
		return nil
	}
	// A new session (a seek, the next video) supersedes an end the render
	// thread has not handled yet: that flag belongs to the session being
	// replaced and must not stop this one.
	playbackEnded.Store(false)
	persistSessionProgress(playbackCurrentSession())
	// Decided once per session, before the lock: it is a stat of the page path.
	local := !tempFile && mediaPageIsLocalFile(pageURL)

	playbackMu.Lock()
	prev := playbackCurrent
	if after != nil && prev != after {
		// Replaced or stopped while the progress above was saved.
		playbackMu.Unlock()
		return nil
	}
	playbackSeq++
	s := &playbackSession{id: playbackSeq, source: source, pageURL: pageURL, tempFile: tempFile, local: local, speed: avSpeedFactor(speed)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	playbackCurrent = s
	playbackMu.Unlock()

	if prev != nil {
		prev.stop()
		if prev.tempFile && !samePlaybackSource(prev.source, source) {
			if tempFile && dlSamePage(prev.pageURL, pageURL) {
				// The same video fetched again (another quality, a retry):
				// the new download replaces this one, and only the copy being
				// watched is worth keeping.
				removeTempVideo(prev.source)
			} else {
				// The finished download is kept for offline play when the
				// setting allows it (dl_library.go); otherwise it is deleted
				// as before.
				dlReleaseTempPlayback(prev.source, prev.pageURL)
			}
		}
	}
	return s
}

// mediaPageIsLocalFile reports whether a session's page is a file on disk: no
// URL scheme and a regular file at that path. A stream that had to be
// downloaded first keeps its web page URL, so it is still a stream.
func mediaPageIsLocalFile(page string) bool {
	page = strings.TrimSpace(page)
	if page == "" || strings.Contains(page, "://") {
		return false
	}
	info, err := os.Stat(page)
	return err == nil && info.Mode().IsRegular()
}

// mediaSessionIsLocal reports whether the session on screen plays a file
// from the card rather than a stream.
func mediaSessionIsLocal() bool {
	s := playbackCurrentSession()
	return s != nil && s.local
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
	// A session that reached the end of its file is left to the render
	// thread: stopping it here cleared playbackEnded before the main loop saw
	// it, so the end-of-video hand-offs only ran when the render thread won a
	// race of a few microseconds.
	if s.ended.Load() && playbackEnded.Load() {
		return
	}
	stopEmbeddedPlayback(s)
}

// playbackStopOwns reports whether a stop for stopped may clear the player.
// A nil stopped means the current session (the screen's Stop action); a
// non-nil one may only clear itself, never a play that replaced it.
func playbackStopOwns(stopped, current *playbackSession) bool {
	return stopped == nil || stopped == current
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
		logWarn("video", "could not remove temp download: %v", err)
	}
}

// removeTempVideoArtifacts deletes a temp download together with everything
// yt-dlp staged around it — the .part file, per-format pieces, .ytdl state.
// Only the finished file was removed before, and a cancelled or failed fetch
// left its partial video on the card for good (each attempt has a new name).
func removeTempVideoArtifacts(tempPath string) {
	removeTempVideo(tempPath)
	base := filepath.Base(tempPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if !strings.HasPrefix(stem, "jukahub-video-") {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(tempPath), stem+".*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// sweepTempVideos removes the temp downloads a crash or a power cut left
// behind. Nothing plays at boot, so every jukahub-video file is an orphan then.
func sweepTempVideos() {
	matches, _ := filepath.Glob(filepath.Join(videoTempDir(), "jukahub-video-*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// lockedTailBuffer collects a helper's output for a progress parser that reads
// it while the helper is still writing. A bare bytes.Buffer was read every
// 200 ms by the waiting loop while exec's copier appended to it — a data race
// — and it kept the whole log of a five-minute download.
type lockedTailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *lockedTailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if b.max > 0 && len(b.buf) > b.max {
		b.buf = append(b.buf[:0], b.buf[len(b.buf)-b.max:]...)
	}
	return len(p), nil
}

func (b *lockedTailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
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

// probeFields is what ffprobe tells us about a source. Missing values are zero
// so callers can tell "not in the container" from a real number.
type probeFields struct {
	width, height int32
	duration, fps float64
}

type probeSnapshot struct {
	probeFields
	at time.Time
}

// probeCacheTTL keeps a probe result long enough to cover resolve -> probe ->
// play. Without it the same remote URL is probed twice in a row (once to check
// the stream, once by the player), which cost several seconds on the handheld.
const probeCacheTTL = 2 * time.Minute

var (
	probeCacheMu sync.Mutex
	probeCache   = map[string]probeSnapshot{}
)

// probeCacheKey is empty for local files, which are cheap to probe and can
// change without the path changing.
func probeCacheKey(path string) string {
	lower := strings.ToLower(strings.TrimSpace(path))
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return path
	}
	return ""
}

func cachedProbe(path string) (probeFields, bool) {
	key := probeCacheKey(path)
	if key == "" {
		return probeFields{}, false
	}
	probeCacheMu.Lock()
	defer probeCacheMu.Unlock()
	snap, ok := probeCache[key]
	if !ok {
		return probeFields{}, false
	}
	if time.Since(snap.at) > probeCacheTTL {
		delete(probeCache, key)
		return probeFields{}, false
	}
	return snap.probeFields, true
}

func rememberProbe(path string, fields probeFields) {
	key := probeCacheKey(path)
	if key == "" || fields.width <= 0 || fields.height <= 0 {
		return
	}
	probeCacheMu.Lock()
	probeCache[key] = probeSnapshot{probeFields: fields, at: time.Now()}
	probeCacheMu.Unlock()
}

// parseProbeOutput reads ffprobe's default=noprint_wrappers=1 key=value output.
// avg_frame_rate wins over r_frame_rate when both are present because it is
// what actually plays for variable-frame-rate sources.
func parseProbeOutput(s string) probeFields {
	var fields probeFields
	rRate, avgRate := 0.0, 0.0
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
		switch {
		case strings.HasPrefix(line, "width="):
			if v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "width=")), 10, 32); err == nil && v > 0 {
				fields.width = int32(clampPosInt(v))
			}
		case strings.HasPrefix(line, "height="):
			if v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "height=")), 10, 32); err == nil && v > 0 {
				fields.height = int32(clampPosInt(v))
			}
		case strings.HasPrefix(line, "duration="):
			if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "duration=")), 64); err == nil && v > 0 {
				fields.duration = v
			}
		case strings.HasPrefix(line, "r_frame_rate="):
			rRate = parseRate(strings.TrimPrefix(line, "r_frame_rate="))
		case strings.HasPrefix(line, "avg_frame_rate="):
			avgRate = parseRate(strings.TrimPrefix(line, "avg_frame_rate="))
		}
	}
	switch {
	case avgRate > 0:
		fields.fps = avgRate
	case rRate > 0:
		fields.fps = rRate
	}
	return fields
}

// ffprobeStream asks ffprobe for the first video stream's size, duration and
// frame rate. A nil error with zero dimensions means the source has no video.
func ffprobeStream(ctx context.Context, ffprobePath, path string) (probeFields, error) {
	args := []string{"-v", "error"}
	if isHTTPMediaURL(path) {
		// A signed CDN URL was issued for the browser identity yt-dlp used
		// when it extracted it. ffprobe sends "Lavf/..." by default, which
		// some CDNs answer with 403 — the handheld then rejected a stream
		// the decoder itself plays, and fell back to downloading the file.
		args = append(args, "-user_agent", streamUserAgent,
			"-rw_timeout", ffmpegReadTimeoutArg()) // the decoders' bound too (videorecover.go)
	}
	// The firmware's own ffprobe cannot open https; such a URL is handed to
	// it through the loopback relay (ffmpegtls.go).
	args = append(args,
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate,avg_frame_rate,duration",
		"-of", "default=noprint_wrappers=1")
	input := ffmpegReadableInput(ffprobePath, path)
	cmd := exec.CommandContext(ctx, ffprobePath, append(args, input)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	// A certificate it could not check (no CA bundle where its OpenSSL looks)
	// sends https through the relay from now on; ask once more that way, so
	// this very video streams instead of downloading.
	if err != nil && ctx.Err() == nil && ffmpegNoteTLSFailure(stderr.String()) {
		if retry := ffmpegReadableInput(ffprobePath, path); retry != input {
			stderr.Reset()
			cmd = exec.CommandContext(ctx, ffprobePath, append(args, retry)...)
			cmd.Stderr = &stderr
			out, err = cmd.Output()
		}
	}
	if err != nil {
		// "exit status 1" alone says nothing about why a stream was
		// rejected; ffprobe's first stderr line is the reason.
		if reason := firstNonEmptyLine(stderr.String()); reason != "" {
			return probeFields{}, fmt.Errorf("%w (%s)", err, reason)
		}
		return probeFields{}, err
	}
	return parseProbeOutput(string(out)), nil
}

// firstNonEmptyLine returns the first non-blank line of a tool's stderr, which
// is where ffmpeg-family tools put the actual error.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 160 {
				line = line[:160] + "…"
			}
			return line
		}
	}
	return ""
}

func probeVideoInfo(ffprobePath, path string) (int32, int32, float64, float64) {
	if snap, ok := cachedProbe(path); ok {
		fps := snap.fps
		if fps <= 0 {
			fps = 30
		}
		logDebug("video", "ffprobe (cached): %dx%d, dur=%.1f, fps=%.1f", snap.width, snap.height, snap.duration, fps)
		return snap.width, snap.height, snap.duration, fps
	}
	if ffprobePath == "" {
		logWarn("video", "ffprobe missing; using default 1280x720@30 probe values")
		return 1280, 720, 0, 30
	}
	// A remote URL needs a bound: ffprobe sits on the connection until the
	// server answers, and playback must not hang on a dead CDN link.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fields, err := ffprobeStream(ctx, ffprobePath, path)
	if err != nil {
		logWarn("video", "ffprobe failed for %s: %v", sanitizeLogURL(path), err)
		return 1280, 720, 0, 30
	}
	if fields.width <= 0 || fields.height <= 0 {
		fields.width, fields.height = 1280, 720
	}
	fps := fields.fps
	if fps <= 0 {
		fps = 30
	}
	rememberProbe(path, fields)
	logDebug("video", "ffprobe result: %dx%d, dur=%.1f, fps=%.1f", fields.width, fields.height, fields.duration, fps)
	return fields.width, fields.height, fields.duration, fps
}

// videoLagging reports whether the picture has stayed over a second behind the
// sound for three seconds without catching up: a decoder that cannot keep up,
// not a hiccup (a slow keyframe, the UI busy) or a slow start (the sound of a
// seek often plays before the picture's first frame) that the picture catches
// up from by itself. since is when the current run of late frames began and
// from how late the picture was then; decoder goroutine.
func videoLagging(clock *avSync, since *time.Time, from *float64, now time.Time) bool {
	if clock.late <= 1 || !clock.following {
		*since = time.Time{}
		return false
	}
	if since.IsZero() || now.Sub(*since) < 3*time.Second {
		if since.IsZero() {
			*since, *from = now, clock.late
		}
		return false
	}
	if clock.late < *from-0.25 {
		// Still behind, but closing in: a new run from here.
		*since, *from = now, clock.late
		return false
	}
	return true
}

// videoLightDecode is raised the first time the picture fell a second behind
// the sound at a speed above 1× (the decoder could not keep up, startFFmpegDecoder):
// from then on, until JukaHub restarts, a decoder at such a speed skips the
// frames nothing refers to, which costs smoothness rather than sync.
var videoLightDecode atomic.Bool

// ffmpegDecoderArgs builds the rawvideo decoder command. When the source is
// larger than the decode target it uses a scale filter in ffmpeg (cheaper than
// decoding 4K frames and letting the GPU shrink them); otherwise it asks for
// the native size directly. inputOpts go in front of -i (the identity a
// resolved stream is asked for with, ffmpegStreamInputArgs). speed is the
// playback speed (1 = normal); startSec is media time.
func ffmpegDecoderArgs(path string, inputOpts []string, width, height int32, scaling bool, startSec, fps float64, live bool, speed float64) []string {
	// The decoder's stderr goes to errors.txt: without these, every start
	// (and every retry) wrote ffmpeg's banner and configure line there,
	// burying the one line that said why the stream failed.
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if startSec > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSec))
	}
	if avSpeedFactor(speed) > 1 {
		if IsTrimuiDevice() || P().IsHandheld() {
			// Faster, the handheld decodes speed × the frames per second.
			// The deblocking of frames nothing refers to is skipped: no error
			// can spread from them, and at 1.5–2× it is not visible.
			args = append(args, "-skip_loop_filter", "noref")
		}
		if videoLightDecode.Load() {
			// This device fell behind at such a speed: those frames are not
			// decoded at all (the fps filter repeats a neighbour instead).
			args = append(args, "-skip_frame", "noref")
		}
	}
	args = append(args, inputOpts...)
	args = append(args, "-i", path)
	// The rate is pinned to the one the frames are timed by, from the seek
	// point on (avsync.go): left to itself ffmpeg picked its own. A speed
	// other than 1× retimes the frames first (setpts), so the rate stays the
	// source's whatever the speed.
	scale := ""
	if scaling {
		scale = fmt.Sprintf("scale=%d:%d:flags=bilinear", width, height)
	}
	if vf := avVideoFilters(fps, scale, live, speed); vf != "" {
		args = append(args, "-vf", vf)
	}
	if avFiltersOff.Load() {
		args = append(args, "-r", avRateArg(fps))
	}
	if !scaling {
		args = append(args, "-s", fmt.Sprintf("%dx%d", width, height))
	}
	// Planar IYUV (yuv420p) instead of RGBA: width is even and the stride is
	// width, so the three planes are packed back to back in the raw stream. The
	// decoder pipe drops from 4 to 1.5 bytes per pixel, and SDL's GLES2 driver
	// converts IYUV→RGB in a shader on the GPU instead of ffmpeg doing the
	// conversion on the CPU.
	return append(args,
		"-f", "rawvideo",
		"-pix_fmt", "yuv420p",
		"-",
	)
}

// startFFmpegDecoder runs the picture decoder for one session until its pipe
// closes. It returns how the decoder failed — no frame at all, or a stream
// resolved from a page that broke off well before its end — or nil for the end
// of the source and for a session that was stopped or replaced. The caller
// closes the session either way and hands a failure to mediaRecoverStream
// (videorecover.go), which used to be a player that silently vanished.
func startFFmpegDecoder(sess *playbackSession, ffmpegPath, path string, width, height int32, startSec float64) *mediaStreamFailure {
	embeddedPlayer.mu.Lock()
	srcW, srcH := embeddedPlayer.sourceWidth, embeddedPlayer.sourceHeight
	duration := embeddedPlayer.duration
	fps := avClampFPS(embeddedPlayer.frameRate)
	embeddedPlayer.mu.Unlock()
	scaling := srcW > 0 && srcH > 0 && (srcW != width || srcH != height)
	pageStream := !sess.tempFile && !sess.local && mediaPageStream(path, sess.pageURL)
	// Frames come out in output time at fps whatever the speed; each one is
	// speed seconds of the source per 1/fps (avsync.go).
	speed := avSpeedFactor(sess.speed)
	failure := func(frames int, position float64, reason string) *mediaStreamFailure {
		return &mediaStreamFailure{
			source:   path,
			pageURL:  sess.pageURL,
			tempFile: sess.tempFile,
			local:    sess.local,
			startSec: startSec,
			position: position,
			duration: duration,
			frames:   frames,
			reason:   reason,
			// Read now: closing the session forgets both.
			origin:   mediaPlayOriginName(),
			floating: videoMiniActive(),
		}
	}

	filtered := !avFiltersOff.Load()
	args := ffmpegDecoderArgs(ffmpegReadableInput(ffmpegPath, path), ffmpegStreamInputArgs(path, sess.pageURL),
		width, height, scaling, startSec, fps, duration <= 0, speed)
	cmd := exec.CommandContext(sess.ctx, ffmpegPath, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		if sess.ctx.Err() != nil {
			return nil // stopped meanwhile: not a failure
		}
		logWarn("video", "ffmpeg stdout pipe failed: %v", err)
		f := failure(0, startSec, ffmpegStartReason(err))
		f.startErr = true
		return f
	}
	// ffmpeg's stderr still goes to errors.txt; its last lines are also kept,
	// because they are the only place that says why a stream would not play.
	stderrTail := &lockedTailBuffer{max: 8 << 10}
	cmd.Stderr = ffmpegStderrSink{tail: stderrTail}
	// With the stderr copy above, Wait also waits for that pipe; a child that
	// left it open must not hold the session's goroutine for good.
	cmd.WaitDelay = 2 * time.Second

	if err := cmd.Start(); err != nil {
		// Start answers a session stopped meanwhile (Close, a second seek, a
		// game taking the screen) with the context's error: that is no failure.
		if sess.ctx.Err() != nil {
			return nil
		}
		logWarn("video", "ffmpeg failed to start: %v", err)
		f := failure(0, startSec, ffmpegStartReason(err))
		f.startErr = true
		return f
	}
	if !sess.registerVideoCmd(cmd) {
		cmd.Wait()
		return nil
	}
	defer sess.clearVideoCmd(cmd)
	defer cmd.Wait()

	// 1.5 bytes per pixel: the Y plane (w*h) then U and V quarters each. The
	// product is formed in int64 and capped before it narrows: width and height
	// come from the container's header, and a 32-bit int product would wrap for
	// a large one, handing make a buffer far smaller than the frames ffmpeg
	// then writes into it.
	frameBytes := int64(width) * int64(height) * 3 / 2
	if frameBytes <= 0 || frameBytes > maxVideoFrameBytes {
		// A probe that reported no usable size leaves nothing to size by, and
		// a header-declared giant is refused. ffmpeg is stopped first: nobody
		// will read its pipe, and the deferred Wait would sit on it for good.
		logWarn("video", "refusing a %dx%d frame buffer", width, height)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		// A download would have the same picture: nothing takes this over.
		f := failure(0, startSec, fmt.Sprintf("its picture size (%dx%d) is too large", width, height))
		f.unplayable = true
		return f
	}
	frameSize := int(frameBytes)
	buf := make([]byte, frameSize)
	frameCount := 0
	var lastWait time.Duration
	clock := &sess.av
	isPaused := func() bool {
		embeddedPlayer.mu.Lock()
		defer embeddedPlayer.mu.Unlock()
		return embeddedPlayer.phase == "paused"
	}
	// A read of an http(s) input that hangs — HLS on a network that went
	// silently dead, which ffmpeg's own timeouts do not end — stops ffmpeg
	// after mediaStallGiveUp, and the stall is reported below
	// (videorecover.go). Stopped when the loop is over, and on every return.
	var watch *mediaReadWatch
	if isHTTPMediaURL(path) {
		watch = startMediaReadWatch(sess.ctx.Done(), clock, isPaused, func() { _ = cmd.Process.Kill() })
		defer watch.Stop()
	}

	// Each frame is read, held until the clock reaches it (the sound's, when
	// there is sound: avsync.go), then shown. A late frame is shown at once
	// and the next read straight away, so a picture that fell behind catches
	// up instead of staying behind. The clock runs in output time: a speed
	// other than 1× was applied by both decoders (setpts and atempo), so the
	// frames are paced exactly as at 1×.
	lagNoted, lateSince, lateFrom := false, time.Time{}, 0.0
	for {
		if sess.ctx.Err() != nil {
			return nil
		}
		if isPaused() {
			from := time.Now()
			for isPaused() && sess.ctx.Err() == nil {
				time.Sleep(15 * time.Millisecond)
			}
			if frameCount > 0 {
				clock.paused(time.Since(from))
			}
			continue
		}
		readAt := time.Now()
		watch.begin()
		_, err := io.ReadFull(pipe, buf)
		watch.end()
		if err != nil {
			// How long the pipe kept us waiting before it closed tells a stall
			// (ffmpeg gave up on a quiet input) from an end (mediaDecoderStalled).
			lastWait = time.Since(readAt)
			break
		}
		frameCount++
		if frameCount == 1 {
			// The first frame waits for the sound's first buffer, so both
			// start together, then starts the clock.
			clock.awaitAudio(sess.ctx)
			if sess.ctx.Err() != nil {
				return nil // stopped or replaced while it waited
			}
			clock.start(time.Now())
			// The progress bar and the subtitles count from the first
			// frame too, not from before ffmpeg opened the file; the bar
			// held still until now (awaitFrame).
			if playbackCurrentSession() == sess {
				embeddedPlayer.mu.Lock()
				if embeddedPlayer.phase == "playing" {
					embeddedPlayer.decoderStartTime = time.Now()
					embeddedPlayer.totalPausedDuration = 0
				}
				embeddedPlayer.awaitFrame = false
				embeddedPlayer.mu.Unlock()
			}
		} else if !clock.wait(sess.ctx, float64(frameCount-1)/fps, isPaused) {
			return nil
		} else if speed != 1 && !lagNoted && videoLagging(clock, &lateSince, &lateFrom, time.Now()) {
			// Faster, the decoder has more to do; once per session the log
			// says whether this device keeps up at this speed.
			lagNoted = true
			logInfo("video", "at %sx the picture runs %.1f s behind the sound: the decoder cannot keep up (%dx%d, fps=%.2f)",
				avNumArg(speed), clock.late, width, height, fps)
			// The first time, the video starts again where the sound is with
			// the lighter decode (videoLightDecode); a picture that cannot
			// keep up would otherwise fall further behind for good.
			if speed > 1 && sess.isCurrent() && !videoLightDecode.Swap(true) {
				logInfo("video", "decoding lighter above 1x from now on (-skip_frame noref)")
				if videoRestartAt(sess, snapshotVideoState().progress, 0, true) {
					return nil
				}
			}
		}

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
		// The main loop wakes for it and shows it at once (videoFrameWake);
		// set under the lock, so an upload can never clear it before the
		// frame it stands for is there.
		videoFrameFresh.Store(true)
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

		if frameCount == 1 {
			logDebug("video", "First frame received: %dx%d, %d bytes, fps=%.3f, speed=%sx, sound=%v", width, height, frameSize, fps, avNumArg(speed), clock.audioStarted.Load())
			if pageStream {
				// Streaming works here: the run of streams that would not
				// open starts again (videorecover.go).
				mediaNoteStreamOpened(sess.pageURL)
			}
			// Trophies: a Live TV channel showed its first picture (every channel
			// in the library has a group). A quality switch restarts a live
			// stream at zero too, so the same channel again does not count.
			if g, _ := tvRowMeta(sess.pageURL); g != "" && startSec == 0 && trophyFirstFor(tkTVPlay, sess.pageURL) {
				trophyEvent(tkTVPlay)
				trophyEventDistinct(tkTVChannels, sess.pageURL)
			}
		}
	}

	// The pipe closed. Wait for the exit, and with it the last of ffmpeg's
	// stderr: whether this was the end of the file or a stream that would not
	// open (or broke off) is in there. The deferred Wait then has nothing left
	// to do.
	watch.Stop()
	_ = cmd.Wait()
	// A cancelled session (stop, seek, next video) is neither an end of stream
	// nor a failure — checked after the Wait, so a Stop during it counts too.
	if sess.ctx.Err() != nil || playbackCurrentSession() != sess {
		return nil
	}
	// Media time: each frame delivered stood for speed / fps of the source.
	position := startSec + float64(frameCount)/fps*speed
	stderr := stderrTail.String()
	if watch.Fired() && frameCount == 0 {
		// It hung opening the input: a failure with the watch's reason, not
		// ffmpeg's (it was stopped before it could say anything).
		logWarn("video", "stream stalled: no picture for %.0f s, the decoder was stopped", lastWait.Seconds())
		return failure(0, position, mediaWatchdogReason())
	}
	if frameCount == 0 && avNoteSpeedMissing(stderr, speed) {
		// This ffmpeg cannot change the speed: the same point again at 1×.
		videoSpeedUnavailable(sess, startSec)
		return nil
	}
	if frameCount == 0 && avNoteFilterMissing(stderr, filtered) {
		// This ffmpeg has no fps filter: once more without it.
		return startFFmpegDecoder(sess, ffmpegPath, path, width, height, startSec)
	}
	if mediaDecoderFailed(startSec, position, duration, frameCount, pageStream, stderr) {
		// A certificate failure routes the next attempt through the relay.
		ffmpegNoteTLSFailure(stderr)
		return failure(frameCount, position, ffmpegFailureReason(stderr))
	}
	// A stream that went quiet ran out its read timeout and reconnects
	// (ffmpegStreamInputArgs), or hung until the watch stopped it: that is no
	// end either.
	if mediaDecoderStalled(frameCount, isHTTPMediaURL(path), pageStream, position, duration, lastWait, stderr, watch.Fired()) {
		if watch.Fired() {
			logWarn("video", "stream stalled: no picture for %.0f s, the decoder was stopped", lastWait.Seconds())
			return failure(frameCount, position, mediaWatchdogReason())
		}
		logWarn("video", "stream stalled: the decoder waited %.0f s before its input closed", lastWait.Seconds())
		return failure(frameCount, position, mediaStallReason(stderr))
	}
	// Only a decoder that ran to the end raises the flag.
	if frameCount > 0 {
		logDebug("video", "Reached end of stream after %d frames", frameCount)
		sess.ended.Store(true)
		playbackEnded.Store(true)
	}
	return nil
}

func startFFmpegAudio(sess *playbackSession, ffmpegPath, path string, startSec float64) {
	// The picture stops waiting for, and following, a sound that is over.
	defer sess.av.audioDone.Store(true)
	filtered := !avFiltersOff.Load()
	stderr, got := runFFmpegAudio(sess, ffmpegPath, path, startSec)
	if !got && sess.ctx.Err() == nil && avNoteSpeedMissing(stderr, sess.speed) {
		// No atempo in this ffmpeg: both decoders start again at 1×.
		videoSpeedUnavailable(sess, startSec)
		return
	}
	if !got && sess.ctx.Err() == nil && avNoteFilterMissing(stderr, filtered) {
		// This ffmpeg has no aresample options: once more without them.
		_, _ = runFFmpegAudio(sess, ffmpegPath, path, startSec)
	}
}

// runFFmpegAudio decodes the sound of one session into the video device until
// the end or a stop. It returns the end of ffmpeg's stderr and whether any
// sound was queued.
func runFFmpegAudio(sess *playbackSession, ffmpegPath, path string, startSec float64) (string, bool) {
	rate := GetVideoAudioRate()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if startSec > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSec))
	}
	// The same identity as the picture decoder (ffmpegStreamInputArgs).
	args = append(args, ffmpegStreamInputArgs(path, sess.pageURL)...)
	args = append(args, "-i", ffmpegReadableInput(ffmpegPath, path))
	// The audio track picked in the sheet (videotracks.go); none keeps ffmpeg's.
	args = append(args, videoTracksAudioMapArgs(sess)...)
	// Sample 0 is the seek point, with silence over any gap, so the samples
	// line up with the picture's frames (avsync.go); atempo then plays them at
	// the session's speed, keeping the pitch.
	embeddedPlayer.mu.Lock()
	live := embeddedPlayer.duration <= 0
	embeddedPlayer.mu.Unlock()
	if af := avAudioFilter(live, sess.speed); af != "" {
		args = append(args, "-af", af)
	}
	args = append(args,
		"-f", "s16le",
		"-ac", "2",
		"-ar", fmt.Sprintf("%d", rate),
		"-",
	)
	cmd := exec.CommandContext(sess.ctx, ffmpegPath, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		logWarn("audio", "ffmpeg audio pipe failed: %v", err)
		return "", false
	}
	stderrTail := &lockedTailBuffer{max: 4 << 10}
	cmd.Stderr = ffmpegStderrSink{tail: stderrTail}
	cmd.WaitDelay = 2 * time.Second

	if err := cmd.Start(); err != nil {
		logWarn("audio", "ffmpeg audio failed to start: %v", err)
		return "", false
	}
	if !sess.registerAudioCmd(cmd) {
		cmd.Wait()
		return "", false
	}
	defer sess.clearAudioCmd(cmd)

	const audioBufferSize = 8192
	buf := make([]byte, audioBufferSize)
	queued := false

	for {
		if sess.ctx.Err() != nil {
			break
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
		if n > 0 && sess.ctx.Err() == nil {
			if !queued {
				// A seek replaced a session whose sound is still queued (up
				// to a second of it): played first, it put this sound that
				// far behind the picture. The clock counts from here.
				sess.av.mark.Store(ClearVideoAudioQueueMark())
			}
			QueueVideoAudio(buf[:n], vol)
			if !queued {
				queued = true
				sess.av.audioStarted.Store(true)
			}
		}
		if err != nil {
			break
		}
	}
	_ = cmd.Wait()
	return stderrTail.String(), queued
}

// UpdateCurrentTexture uploads the current raw RGBA frame to an SDL2 texture.
func UpdateCurrentTexture(renderer *sdl.Renderer) {
	if renderer == nil {
		return
	}
	// A player on screen asks for its frame every time it draws: only then
	// may a new frame wake the main loop early (videoFrameWake).
	videoUploadAt.Store(sdl.GetTicks64())
	embeddedPlayer.mu.Lock()
	defer embeddedPlayer.mu.Unlock()

	raw := embeddedPlayer.currentRaw
	w := embeddedPlayer.width
	h := embeddedPlayer.height
	if raw == nil || w <= 0 || h <= 0 || len(raw) != int(w*h*3/2) {
		return
	}
	if embeddedPlayer.phase == "paused" {
		// Nothing new is shown while paused: a frame read just before the
		// pause must not keep waking the loop.
		videoFrameFresh.Store(false)
		return
	}
	if embeddedPlayer.frameSeq == embeddedPlayer.uploadedSeq && embeddedPlayer.current != nil {
		videoFrameFresh.Store(false)
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
		// IYUV: SDL's GLES2 driver converts the planar texture to RGB in a
		// shader during Copy, so the YUV→RGB work lands on the GPU instead of
		// the software path. ffmpeg output planes are packed: Y, then U, then V.
		tex, err := renderer.CreateTexture(uint32(sdl.PIXELFORMAT_IYUV), sdl.TEXTUREACCESS_STREAMING, w, h)
		if err != nil {
			logWarn("video", "CreateTexture failed: %v (w=%d h=%d)", err, w, h)
			return
		}
		embeddedPlayer.current = tex
		logDebug("video", "Created IYUV texture %dx%d", w, h)
	}

	tex := embeddedPlayer.current
	yBytes, uvBytes := int(w*h), int(w*h)/4
	uStart, vStart := yBytes, yBytes+uvBytes
	if err := tex.UpdateYUV(nil, raw[:uStart], int(w),
		raw[uStart:vStart], int(w)/2,
		raw[vStart:], int(w)/2); err != nil {
		logWarn("video", "Update YUV texture failed: %v", err)
		tex.Destroy()
		embeddedPlayer.current = nil
		return
	}
	embeddedPlayer.uploadedSeq = embeddedPlayer.frameSeq
	videoFrameFresh.Store(false)
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
	pageURL := playbackPageURL()
	if pageURL == "" {
		pageURL = url
		// Direct StartEmbeddedPlayback calls (queue/home cards) must still
		// know their source, or Watch Party would see an empty URL and think
		// the room had switched streams.
		setPlaybackPageURL(pageURL)
	}
	// Trophies: once per video. A quality switch, the download fallback and a
	// Watch Party resync restart the page whose start is committed just below.
	if _, same := playingVideoQuality(pageURL); !same {
		trophyEvent(tkVideoPlay)
	}
	// A stream is starting, so this ceiling is now the one on screen. The
	// Quality row reads it back if a later switch is cancelled before it plays,
	// which is what keeps the row describing the picture rather than the press.
	commitVideoQualityPlayback(pageURL, effectivePlaybackMaxHeight(config, pageURL))
	// A new session owns the picture again: the floating window must not keep
	// showing the previous video's scene.
	videoMiniClear()
	// The top bar names this session from its page URL (see statusbar_media.go).
	rememberVideoPage(pageURL)
	// The same video starting again (a quality switch, the download that took
	// over a stream, a Watch Party resync) and the next one of the list the
	// player moved on to keep the chosen speed; anything else starts at 1×.
	startSpeed := videoSpeedForStart(pageURL)
	sess := beginPlaybackSession(url, pageURL, tempFile, startSpeed)
	// Only fetch remote subtitles when CC is enabled. The original page URL
	// remains available even if the stream itself is a temporary local file.
	resetVideoSubtitles(sess)
	startEnabledNetworkSubtitles(config)
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
	// The level the last video was left at carries over (every video used to
	// start at full volume). A mute does not: the new video starts at the
	// level from before it, so it is never silent by surprise.
	if embeddedPlayer.volume <= 0.001 {
		embeddedPlayer.volume = embeddedPlayer.mutedLevel
		if embeddedPlayer.volume <= 0.001 {
			embeddedPlayer.volume = 1
		}
	}
	embeddedPlayer.speed = startSpeed
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

		// Read before the player lock (partyActive takes the party's).
		refusal := videoSpeedRefusal(dur <= 0)
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
		embeddedPlayer.awaitFrame = true
		// The speed is read in the same step that leaves "downloading": a
		// press while the stream opened (SetPlaybackSpeed) changed it.
		speed := avSpeedFactor(embeddedPlayer.speed)
		refused := speed != 1 && refusal != ""
		if refused {
			speed = 1
		}
		embeddedPlayer.speed = speed
		embeddedPlayer.mu.Unlock()
		// Before the decoders start, which read it.
		sess.speed = speed
		if refused {
			videoSpeedRemember(pageURL, 1)
			ShowToast(refusal, ToastKindInfo)
		}
		// A new video plays, whatever the shared device was left in.
		PauseVideoAudio(false)

		// Sidecar / embedded subtitle tracks belong to this source. They load
		// beside the decoders, not before them: finding an embedded track
		// means ffmpeg reading the whole file, and a large MKV used to sit on
		// a black screen for that long while the clock (decoderStartTime,
		// already set) ran on, leaving progress and cues out of step.
		go prepareVideoSubtitles(sess, config, url)

		go startFFmpegAudio(sess, ffmpegPath, url, startSec)
		failure := startFFmpegDecoder(sess, ffmpegPath, url, w, h, startSec)

		sess.finish()
		// A decoder that could not play its source no longer just closes the
		// player: a web video falls back to the download, anything else says
		// why (videorecover.go).
		mediaRecoverStream(config, failure)
	}()
}

func StopEmbeddedPlayback() {
	stopEmbeddedPlayback(nil)
}

func stopEmbeddedPlayback(stopped *playbackSession) {
	playbackMu.Lock()
	sess := playbackCurrent
	if stopped == nil {
		stopped = sess
	}
	if !playbackStopOwns(stopped, sess) {
		playbackMu.Unlock()
		if stopped != nil {
			stopped.stop()
		}
		return
	}
	playbackCurrent = nil
	playbackMu.Unlock()
	playbackEnded.Store(false)

	if sess != nil {
		persistSessionProgress(sess)
		sess.stop()
		if sess.tempFile {
			// A download that played to here is a whole file: it is kept in
			// the Downloads library when the setting allows it, otherwise
			// removed (dlReleaseTempPlayback, dl_library.go).
			dlReleaseTempPlayback(sess.source, sess.pageURL)
		}
	}
	ClearVideoAudioQueue()
	// The device is shared with the music player: a video stopped while paused
	// left it paused, and the next song queued into a device that never
	// played — silence, then a frozen UI on the next skip.
	PauseVideoAudio(false)

	embeddedPlayer.mu.Lock()
	prevPhase := embeddedPlayer.phase
	embeddedPlayer.phase = "idle"
	embeddedPlayer.currentRaw = nil
	embeddedPlayer.frameSeq = 0
	embeddedPlayer.uploadedSeq = 0
	embeddedPlayer.downloadSpeed = 0
	embeddedPlayer.awaitFrame = false
	videoFrameFresh.Store(false)
	// The frame texture is retired, not destroyed: Stop runs on decoder
	// goroutines too, and a texture may only be destroyed on the render thread
	// (mediaReleaseRetiredFrames, called from the main loop).
	if embeddedPlayer.current != nil {
		mediaRetiredFrames = append(mediaRetiredFrames, embeddedPlayer.current)
		embeddedPlayer.current = nil
	}
	embeddedPlayer.mu.Unlock()

	if prevPhase != "idle" {
		clearVideoFrameQueue()
	}

	// Remember a deliberate stop so Watch Party does not reopen the room's
	// stream the moment the poll sees it again. Use the stopped session's own
	// page: a newer play may already own the shared page URL.
	page := ""
	if sess != nil {
		page = sess.pageURL
	}
	partyNoteStop(page)
	// Only clear the shared page and the opener when they still belong to
	// this session. A newer play may have set its page before its session was
	// installed; its state is not ours to erase.
	switch {
	case sess == nil:
		clearPlaybackPageURL()
		mediaForgetPlayOrigin()
		forgetVideoPage()
	case page != "" && playbackPageURL() == page:
		clearPlaybackPageURLIf(page)
		mediaForgetPlayOrigin()
		forgetVideoPage()
	case page == "" && playbackPageURL() == "":
		clearPlaybackPageURL()
		mediaForgetPlayOrigin()
		forgetVideoPage()
	}
	// Leaving playback also leaves full screen, the settings sheet and the
	// floating window behind.
	videoFullscreen = false
	videoSettingsOpen = false
	videoControlFocus = vcNone
	videoMiniClear()
	// CC is the user's choice for the session: stopping ends this video's
	// fetch and drops its cues, and the next video comes up with its own
	// subtitles when CC is on (videosubs.go).
	resetVideoSubtitles(nil)

	videoPlaybackMutex.Lock()
	videoPlaybackPhase = "idle"
	videoPlaybackPhaseAt = sdl.GetTicks64()
	videoPlaybackProgress = 0
	videoPlaybackCmd = nil
	embeddedPlaybackPath = ""
	videoPlaybackMutex.Unlock()
}

// mediaStopForScreenChild ends playback when a game or an external app takes
// the screen: the parked main loop no longer draws the player, and the video's
// audio would otherwise go on over the game. It is safe off the render thread,
// like every other Stop caller (the decoder's own finish).
func mediaStopForScreenChild() {
	// The Library's scan would run ffmpeg and ffprobe beside the game; the
	// next visit to the Library carries on where it stopped.
	mlibCancelScan()
	if playerPhase() == "idle" {
		return
	}
	logDebug("video", "a game or app took the screen; stopping playback")
	StopEmbeddedPlayback()
}

func ToggleVideoPlayback() {
	embeddedPlayer.mu.Lock()

	// The audio device is paused and resumed after the state lock is let go,
	// so the player lock is never held while waiting on the audio one.
	pauseAudio, resumeAudio := false, false
	switch embeddedPlayer.phase {
	case "playing":
		embeddedPlayer.phase = "paused"
		embeddedPlayer.pauseStartTime = time.Now()
		pauseAudio = true
	case "paused":
		embeddedPlayer.totalPausedDuration += time.Since(embeddedPlayer.pauseStartTime).Seconds()
		embeddedPlayer.pauseStartTime = time.Time{}
		embeddedPlayer.phase = "playing"
		resumeAudio = true
	}
	embeddedPlayer.mu.Unlock()
	if pauseAudio {
		PauseVideoAudio(true)
	} else if resumeAudio {
		PauseVideoAudio(false)
	}
	// Publish after releasing the state lock: partyPublishPlayback snapshots
	// the player and would deadlock if called underneath it.
	partyPublishPlayback()
}

// SeekVideo plays the video on screen from fraction of its length, at the
// speed it plays at.
func SeekVideo(fraction float64) {
	if videoRestartAt(playbackCurrentSession(), fraction, 0, false) {
		partyPublishPlayback()
	}
}

// videoRestartAt replaces prev, the session on screen, with one that plays the
// same source from fraction of its length at speed (0 keeps the current one).
// A seek and a speed change are both this: the two decoders start again
// together at the new point, the sound with its atempo and the picture with its
// setpts (avsync.go). keepPaused leaves a paused video paused — the new
// decoders wait for Play, with the last picture on screen — where a seek
// resumes it. It reports whether a new session started.
func videoRestartAt(prev *playbackSession, fraction, speed float64, keepPaused bool) bool {
	if prev == nil {
		return false
	}

	embeddedPlayer.mu.Lock()
	phase := embeddedPlayer.phase
	width := embeddedPlayer.width
	height := embeddedPlayer.height
	duration := embeddedPlayer.duration
	if speed <= 0 {
		speed = embeddedPlayer.speed
	}
	embeddedPlayer.mu.Unlock()

	if phase == "idle" || width <= 0 || height <= 0 || duration <= 0 || appConfig == nil {
		return false
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}

	ffmpegPath := getToolPath("ffmpeg", appConfig)
	if ffmpegPath == "" {
		return false
	}

	speed = avSpeedFactor(speed)
	if speed != 1 && videoSpeedRefusal(false) != "" {
		// A Watch Party joined while this played faster: its seeks bring
		// the video back to 1×, the speed the room keeps.
		speed = 1
	}
	paused := keepPaused && phase == "paused"
	seekSec := fraction * duration
	next := beginPlaybackSessionAfter(prev, prev.source, prev.pageURL, prev.tempFile, speed)
	if next == nil {
		return false // stopped or replaced meanwhile (a decoder goroutine's restart)
	}

	now := time.Now()
	embeddedPlayer.mu.Lock()
	if !next.isCurrent() {
		// Stopped or replaced between the start above and here: that
		// stop's (or that video's) player state is not overwritten.
		embeddedPlayer.mu.Unlock()
		return false
	}
	if paused {
		// Still paused, from now: Play adds the pause up as it always does,
		// and the first frame restarts the clock anyway.
		embeddedPlayer.phase = "paused"
		embeddedPlayer.pauseStartTime = now
	} else {
		embeddedPlayer.phase = "playing"
		embeddedPlayer.pauseStartTime = time.Time{}
	}
	embeddedPlayer.speed = speed
	embeddedPlayer.decoderStartTime = now
	embeddedPlayer.totalPausedDuration = 0
	embeddedPlayer.progressBase = fraction
	embeddedPlayer.progress = fraction
	// The bar stays at the new point until its first frame (awaitFrame).
	embeddedPlayer.awaitFrame = true
	embeddedPlayer.mu.Unlock()
	if !paused {
		// A seek resumes playback, so the device resumes too. Seeking from
		// pause left it paused under a "playing" player: the picture ran on in
		// silence and the audio decoder blocked on a queue that never drained.
		PauseVideoAudio(false)
	}

	videoDecoderWG.Add(1)
	go func() {
		defer videoDecoderWG.Done()
		go startFFmpegAudio(next, ffmpegPath, prev.source, seekSec)
		failure := startFFmpegDecoder(next, ffmpegPath, prev.source, width, height, seekSec)
		next.finish()
		// A stream that will not reopen at the new position (a signed URL
		// that expired during a long pause) downloads from there instead.
		mediaRecoverStream(appConfig, failure)
	}()
	return true
}

// SetPlaybackSpeed plays the video on screen at speed (1 = normal; clamped to
// avSpeedMin..avSpeedMax). Both decoders start again at the current position,
// as for a seek, and a paused video stays paused. While the stream is still
// opening the choice is kept for its start. A live stream, a Watch Party and
// an ffmpeg without the speed filters stay at 1×, with a toast saying why.
func SetPlaybackSpeed(speed float64) {
	speed = avSpeedFactor(speed)
	embeddedPlayer.mu.Lock()
	phase := embeddedPlayer.phase
	embeddedPlayer.mu.Unlock()
	if phase == "idle" {
		return
	}
	if speed != 1 {
		// "Live" is only known once the stream is open; the start checks
		// that one itself.
		live := phase != "downloading" && snapshotVideoState().duration <= 0
		if why := videoSpeedRefusal(live); why != "" {
			ShowToast(why, ToastKindInfo)
			return
		}
	}
	sess := playbackCurrentSession()
	page := playbackPageURL()
	if sess != nil {
		page = sess.pageURL
	}
	embeddedPlayer.mu.Lock()
	if embeddedPlayer.phase == "downloading" {
		// No decoder yet: the start reads this when the stream opens.
		embeddedPlayer.speed = speed
		embeddedPlayer.mu.Unlock()
		videoSpeedRemember(page, speed)
		return
	}
	current := embeddedPlayer.speed
	embeddedPlayer.mu.Unlock()
	if avSpeedFactor(current) == speed {
		return
	}
	state := snapshotVideoState()
	at := state.progress
	if target, ok := vcSeekTake(); ok {
		// A seek still gathering: the new speed starts there, in the same
		// restart (videomotion.go).
		at = target
	}
	if videoRestartAt(sess, at, speed, true) {
		// What the new session really plays at (videoRestartAt has the
		// last word, a Watch Party joined meanwhile).
		now := snapshotVideoState().speed
		videoSpeedRemember(page, now)
		logInfo("video", "speed %sx at %.1f s", avNumArg(now), at*state.duration)
	}
}

// videoSpeedSteps are the speeds the pill and the settings row step through.
var videoSpeedSteps = []float64{0.5, 0.75, 1.0, 1.25, 1.5, 2.0}

// videoSpeedRefusal says why the video cannot play at another speed than 1×,
// or "" when it can. live is a stream with no length.
func videoSpeedRefusal(live bool) string {
	switch {
	case avSpeedOff.Load():
		return "This ffmpeg cannot change the speed"
	case live:
		// A live stream has no "later" to play ahead into.
		return "Live streams play at 1×"
	case partyActive():
		// The room keeps everyone at one position; a faster guest would be
		// pulled back every few seconds.
		return "A Watch Party plays at 1×"
	}
	return ""
}

// videoSpeedMem decides which start keeps the chosen speed. Starts run on
// resolve goroutines and choices on the main thread, so it has its own lock.
//
// The speed lasts while the same video plays — a quality switch, the
// download that takes over a broken stream, a Watch Party resync and a
// reopen right after it are the same page starting again — and it carries
// to the next video of the list when the player itself moves on (L2/R2,
// previous/next, Up Next, the Shorts feed's autoplay): someone at 1.5×
// through a lecture or podcast series wants the next part at 1.5× too. A
// video picked from a screen starts at 1×: speed is a choice about one piece
// of content, and a stuck 2× on the next music video is a surprise.
var videoSpeedMem struct {
	mu    sync.Mutex
	page  string  // the video the speed belongs to
	next  string  // the list's next video the player handed off to
	speed float64 // the speed chosen for page
}

// videoSpeedForStart is the speed a video starting on page begins at, and
// makes page the one the speed belongs to.
func videoSpeedForStart(page string) float64 {
	page = strings.TrimSpace(page)
	videoSpeedMem.mu.Lock()
	defer videoSpeedMem.mu.Unlock()
	speed := 1.0
	if page != "" && (page == videoSpeedMem.page || page == videoSpeedMem.next) {
		speed = avSpeedFactor(videoSpeedMem.speed)
	}
	videoSpeedMem.page, videoSpeedMem.next, videoSpeedMem.speed = page, "", speed
	return speed
}

// videoSpeedRemember records the speed chosen for page.
func videoSpeedRemember(page string, speed float64) {
	videoSpeedMem.mu.Lock()
	videoSpeedMem.page, videoSpeedMem.speed = strings.TrimSpace(page), avSpeedFactor(speed)
	videoSpeedMem.mu.Unlock()
}

// videoSpeedCarryTo names the next video of the list the player hands off
// to: its start keeps the speed (videoSpeedForStart).
func videoSpeedCarryTo(next string) {
	videoSpeedMem.mu.Lock()
	videoSpeedMem.next = strings.TrimSpace(next)
	videoSpeedMem.mu.Unlock()
}

// videoSpeedUnavailable plays sess on at 1× from at (media seconds) after one
// of its decoders found this ffmpeg without setpts or atempo
// (avNoteSpeedMissing): the video plays, the pill says 1×, and a toast says
// why. Decoder goroutine; the session it replaces is its own.
func videoSpeedUnavailable(sess *playbackSession, at float64) {
	if sess == nil || sess.ctx.Err() != nil || !sess.isCurrent() {
		return // the other decoder of this session got here first
	}
	embeddedPlayer.mu.Lock()
	duration := embeddedPlayer.duration
	embeddedPlayer.mu.Unlock()
	fraction := 0.0
	if duration > 0 {
		fraction = at / duration
	}
	videoSpeedRemember(sess.pageURL, 1)
	ShowToast("This ffmpeg cannot change the speed", ToastKindInfo)
	if !videoRestartAt(sess, fraction, 1, true) && sess.isCurrent() {
		// Nothing to restart (a live stream has no position to go back
		// to): the speed is put back so the pill does not claim one. A
		// session that replaced this one meanwhile keeps its own.
		embeddedPlayer.mu.Lock()
		embeddedPlayer.speed = 1
		embeddedPlayer.mu.Unlock()
	}
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
	awaitFrame          bool
	frameSeq            uint64
	frameRate           float64
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
		awaitFrame:          embeddedPlayer.awaitFrame,
		frameSeq:            embeddedPlayer.frameSeq,
		frameRate:           embeddedPlayer.frameRate,
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

// computeVideoControlLayout fills videoControlRects for the given geometry
// (vcLayout, videochrome.go). It runs every frame regardless of the overlay
// cache, so mouse/touch hit testing keeps working when the cached overlay path
// is used, and it records the window itself: a windowed player's hit test is
// bounded by it, so the scene around the video keeps its own taps.
func computeVideoControlLayout(config *Config, x, y, w, h int32, state videoStateSnapshot) {
	vcLayout(config, x, y, w, h, state)
}

// videoAdvanceProgress moves the stored position forward from wall-clock time
// (times the playback speed) and writes the throttled resume position, both in
// media time. The in-scene overlay and the
// floating mini window (videomini.go) both call it, so the position keeps
// moving and the resume save keeps running whichever one is drawing.
func videoAdvanceProgress(state *videoStateSnapshot) {
	if state.duration <= 0 || state.phase != "playing" {
		return
	}
	elapsed := time.Since(state.decoderStartTime).Seconds()
	pausedTime := state.totalPausedDuration
	if !state.pauseStartTime.IsZero() {
		pausedTime += time.Since(state.pauseStartTime).Seconds()
	}
	effectiveElapsed := elapsed - pausedTime
	if effectiveElapsed < 0 {
		effectiveElapsed = 0
	}
	if state.awaitFrame && effectiveElapsed < videoAwaitFrameMaxSec {
		// A decoder that has not shown its first frame (a start, a seek, a
		// speed change): the position stays where it starts. Counting from
		// the press ran the bar ahead, and the first frame then pulled it
		// back by however long ffmpeg took to open the stream. A picture
		// that never comes cannot freeze the clock: past a few seconds it
		// counts on as before.
		return
	}
	// Wall time is output time; at 2× each second of it is two of the video,
	// so the bar, the clock, the subtitles and the resume point all move at
	// the speed the decoders play at.
	fraction := state.progressBase + effectiveElapsed*avSpeedFactor(state.speed)/state.duration
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
		// The session's own page URL, not playbackPageURL(): that switches
		// to the next video the moment Play is pressed, while this one keeps
		// playing through the resolve, and its position was written into the
		// next video's history entry every five seconds.
		if sess := playbackCurrentSession(); sess != nil && sess.pageURL != "" && state.duration > 0 {
			updateRecentPosition(sess.pageURL, fraction*state.duration)
			// Debounced: this runs from the frame, and a synchronous
			// jukauser.json write (several fsyncs) every five seconds made
			// playback and the controls stutter (home_continue.go).
			homePersistFavorites()
		}
	}
}

// RenderVideoOverlay renders the current video frame and on-screen controls.
// videoSyncPlaybackEnd closes a session whose decoder hit the end of the file.
// It runs on the render thread so the phase change, the frame-queue flush and
// the repaint all happen in one place, and it is called from the main loop so a
// video that finished while its scene was off screen still releases the player.
func videoSyncPlaybackEnd() {
	// Seeks gathered from the shoulders or a drag start here once they settle
	// (videomotion.go): every frame, whoever draws the video.
	vcSeekTick()
	// A session nothing on screen draws any more (the scene was left without
	// Back: the BACK/Menu button, the header's back pill, Home, the Start menu)
	// gets a visible home or ends here, once per frame, whatever path left it.
	if mediaSessionOrphaned(appConfig) {
		mediaResolveOrphan(appConfig)
	}
	if !playbackEnded.Swap(false) {
		return
	}
	if playerPhase() == "idle" {
		return
	}
	// The flag is for the session that reached its end. One that replaced it
	// in the meantime (raised a moment before a seek or the next video began)
	// is not over.
	if sess := playbackCurrentSession(); sess != nil && !sess.ended.Load() {
		return
	}
	logDebug("video", "Playback finished; returning to the scene")
	if shortsAutoplay {
		// A short reached its natural end: the main loop starts the next one
		// instead of dropping the user back on the grid.
		shortsAutoAdvance = true
		// At the speed this one played at (videoSpeedMem).
		if vids := shortsVideos(appConfig); len(vids) > 0 {
			if next, ok := nextShortIndex(currentShortIdx, len(vids)); ok {
				videoSpeedCarryTo(vids[next].GetURL())
			}
		}
	} else if upNextAfterPlayback(shortsAutoplay) {
		// Up Next holds the hand-off. It is taken on the main loop, once the
		// player this video is releasing is idle again.
		upNextAdvancePending = true
		if items := upNextSnapshot(); len(items) > 0 {
			videoSpeedCarryTo(items[0].GetURL())
		}
	}
	// A file opened from Files was shown on the Media scene only because that
	// scene owns the video element; when it ends the user goes back to Files.
	returnToOpener := mediaReturnsToOpener(appConfig)
	StopEmbeddedPlayback()
	if returnToOpener {
		goBackScene(appConfig)
	}
}

func RenderVideoOverlay(renderer *sdl.Renderer, config *Config, elem Element) {
	videoSyncPlaybackEnd()
	state := snapshotVideoState()
	if state.phase == "idle" {
		return
	}
	if sess := playbackCurrentSession(); sess != nil {
		sess.shown.Store(true)
	}

	x, y := elem.X, elem.Y
	w := getElementWidth(elem, screenWidth)
	h := getElementHeight(elem, screenHeight)
	// Full screen ignores the element frame and uses the whole panel.
	if videoFullscreen {
		x, y, w, h = 0, 0, screenWidth, screenHeight
	}

	// While the stream is being opened the player shows a spinner in its own
	// rect, with close the one control: the rest of the bar is not drawn yet,
	// so it must not take taps either.
	if state.phase == "downloading" {
		computeVideoControlLayout(config, x, y, w, h, state)
		closeRect := videoControlRects.close
		videoControlRects = vcNoRects
		videoControlRects.close = closeRect
		// The settings sheet is not drawn over the loader, so it is closed
		// rather than left driving rows nobody can see (a Quality change
		// reopens the stream this way).
		videoSettingsOpen = false
		videoSettingsSheetDrawn = sdl.Rect{}
		vcDrawLoading(renderer, config, state, x, y, w, h)
		vcRoundButton(renderer, closeRect, "close", videoControlFocus == vcClose)
		return
	}

	// Update playback progress from elapsed wall time.
	videoAdvanceProgress(&state)

	// Layout the controls first so hit testing works even when the cached
	// overlay texture is used.
	computeVideoControlLayout(config, x, y, w, h, state)

	UpdateCurrentTexture(renderer)

	embeddedPlayer.mu.Lock()
	currentTex := embeddedPlayer.current
	embeddedPlayer.mu.Unlock()

	if currentTex == nil && state.phase == "playing" {
		videoTextureLogCount++
		if videoTextureLogCount%120 == 0 {
			logDebug("video", "currentTex is nil after UpdateCurrentTexture, raw=%v, w=%d, h=%d",
				state.hasRaw, state.width, state.height)
		}
	}

	// The picture, then the controls at the strength their fade has reached
	// — away while a video plays untouched, in the window as in full screen
	// (vcChromeTick, videomotion.go): the cached scrims, then the bar and its
	// buttons — and last what sits over both: subtitles, the paused button,
	// the volume pill, the settings sheet.
	vis := vcChromeTick(state, sdl.Rect{X: x, Y: y, W: w, H: h})
	drawVideoFrameArea(renderer, currentTex, x, y, w, h)
	if vis > 0 {
		vcDrawScrimsFaded(renderer, config, x, y, w, h, vis)
		vcDrawChromeFaded(renderer, config, state, x, y, w, h, vis)
	}
	vcDrawOverlays(renderer, config, state, x, y, w, h, vis, currentTex != nil)
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
		// The picture keeps its own shape: a Short (9:16) or a 4:3 clip used
		// to be stretched to the width of the player, which on the Media
		// screen's wide element squashed every face.
		dst := sdl.Rect{X: x, Y: y, W: w, H: h}
		embeddedPlayer.mu.Lock()
		vw, vh := embeddedPlayer.width, embeddedPlayer.height
		embeddedPlayer.mu.Unlock()
		if fit := mediaFitRect(dst, vw, vh); fit != dst {
			renderer.SetDrawColor(0, 0, 0, 255)
			renderer.FillRect(&dst)
			dst = fit
		}
		renderer.Copy(currentTex, nil, &dst)
		return
	}
	renderer.SetDrawColor(8, 10, 16, 220)
	renderer.FillRect(&sdl.Rect{X: x, Y: y, W: w, H: h})
	renderSpinner(renderer, x+w/2, y+h/2, 40, ColorTextPrimary())
}

// mediaFitRect letterboxes a vw×vh picture inside box, keeping its aspect
// ratio. An unknown size keeps the whole box, as before, and a difference of a
// couple of pixels is rounding rather than a reason to draw bars.
func mediaFitRect(box sdl.Rect, vw, vh int32) sdl.Rect {
	if vw <= 0 || vh <= 0 || box.W <= 0 || box.H <= 0 {
		return box
	}
	w := box.W
	h := int32(int64(box.W) * int64(vh) / int64(vw))
	if h > box.H {
		h = box.H
		w = int32(int64(box.H) * int64(vw) / int64(vh))
	}
	if box.W-w <= 2 && box.H-h <= 2 {
		return box
	}
	return sdl.Rect{X: box.X + (box.W-w)/2, Y: box.Y + (box.H-h)/2, W: w, H: h}
}

// drawVideoOverlayStatic draws the part of the overlay that never changes while
// a video is on screen: the readability scrims (vcDrawScrims, videochrome.go).
// The overlay cache renders this once per element size and theme change, and
// RenderVideoOverlay copies it at the controls' fade (vcDrawScrimsFaded);
// everything state-dependent is drawn per frame over it (vcDrawChromeFaded,
// vcDrawOverlays). (ox, oy) is the origin the layer is drawn at: the element
// position on screen, or (0, 0) when rendering into the element-sized cache
// texture.
func drawVideoOverlayStatic(renderer *sdl.Renderer, ox, oy, w, h int32) {
	vcDrawScrims(renderer, ox, oy, w, h)
}

// Layout of the settings sheet. The renderer records the rows it drew and
// their height (videoSettingsSheetRowH: a small window narrows them, down to
// videoSettingsRowMinH, before the sheet would cover the bar), and the click
// hit test reads them back, so a tap always lands on the row it is drawn over.
const (
	videoSettingsRowH    = int32(48)
	videoSettingsRowMinH = int32(34)
	videoSettingsTopPad  = int32(20)
)

// drawChevronLeft is a line chevron pointing left: the calendar popover's
// previous-month arrow (calendarpopover.go).
func drawChevronLeft(renderer *sdl.Renderer, cx, cy, r int32, col sdl.Color) {
	renderer.SetDrawColor(col.R, col.G, col.B, col.A)
	renderer.DrawLine(cx+r/2, cy-r, cx-r/2, cy)
	renderer.DrawLine(cx-r/2, cy, cx+r/2, cy+r)
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

	// While the video plays in the floating window the scene owns the panel;
	// the window's own taps are routed by handleVideoMiniClick.
	if videoMiniVisible() {
		return false
	}
	// Nothing on screen draws this session: its last layout's hit rects are
	// stale, and the tap belongs to the scene. The session is given a visible
	// home (or closed) first.
	if mediaSessionOrphaned(appConfig) {
		mediaResolveOrphan(appConfig)
		return false
	}

	// Windowed, the player covers only the rect its controls were laid out in.
	// Clicks on the rest of the scene stay the scene's, and clicks inside the
	// window reach the controls — they used to be drawn there but unreachable, so
	// the only working close was Stop in the bar, and only in full screen. The
	// settings sheet below is modal over the panel, so it skips this gate.
	if !videoFullscreen && !videoSettingsOpen && !pointInRect(mx, my, videoControlBounds) {
		return false
	}

	// A tap while the controls are away brings them back first, in the
	// window as in full screen — except while the stream opens, when the
	// loader's close button is always drawn.
	if !videoChromeVisible() && state.phase != "downloading" {
		revealVideoChrome()
		return true
	}
	revealVideoChrome()

	// The settings sheet owns every click while it is open. Its rows are hit
	// where they were drawn (videoSettingsSheetDrawn): the windowed player
	// draws the sheet inside its own rect. A tap on the sheet's margin presses
	// nothing and keeps it open; a tap beside it closes it.
	if videoSettingsOpen {
		switch idx := vcSheetRow(mx, my); {
		case idx >= 0:
			videoSettingsItem = idx
			videoSettingsActivate(idx)
		case idx == vcSheetMargin:
		default:
			videoSettingsOpen = false
		}
		return true
	}

	// A click also moves the D-pad ring, so the two input paths agree on what
	// is selected instead of the ring jumping when the D-pad is next used.
	r := videoControlRects
	switch {
	case vcHit(mx, my, r.subs):
		videoControlFocus = vcSubs
		toggleSubtitlesWithFeedback()
	case vcHit(mx, my, r.settings):
		videoControlFocus = vcSettings
		videoSettingsOpen = true
		videoSettingsItem = 0
		videoSettingsPage = videoSheetMain
	case vcHit(mx, my, r.fullscreen):
		videoControlFocus = vcFullscreen
		toggleVideoFullscreen()
	case vcHit(mx, my, r.overlayPlay), vcHit(mx, my, r.playPause):
		videoControlFocus = vcPlayPause
		vcTogglePlayback()
	case vcHit(mx, my, r.seekBack):
		videoControlFocus = vcSeekBack
		seekVideoBySeconds(state, -10)
	case vcHit(mx, my, r.seekFwd):
		videoControlFocus = vcSeekFwd
		seekVideoBySeconds(state, 10)
	case vcHit(mx, my, r.prev):
		videoControlFocus = vcPrev
		skipPlayback(appConfig, -1)
	case vcHit(mx, my, r.next):
		videoControlFocus = vcNext
		skipPlayback(appConfig, 1)
	case vcHit(mx, my, r.mute):
		videoControlFocus = vcMute
		videoToggleMute()
	case vcHit(mx, my, r.volBar):
		videoVolumeAt(mx, r.volBar)
	case vcHit(mx, my, r.progress):
		// A press on the track starts a drag: the handle follows the
		// pointer with its time over it, and the video seeks once, on the
		// release (vcSeekTick). Every move used to restart ffmpeg — and the
		// drag never started at all, so seeking was tap-only.
		isDraggingSeekBar = true
		seekBarDragRect = r.progress
		vcScrubBegin(float64(mx-r.progress.X) / float64(r.progress.W))
	case vcHit(mx, my, r.speedCycle):
		videoControlFocus = vcSpeed
		cyclePlaybackSpeed()
	case vcHit(mx, my, r.close):
		videoControlFocus = vcClose
		PlayBackSound()
		mediaEndSession(appConfig)
	case vcHit(mx, my, r.back):
		// Backing out keeps the video: full screen steps down to the window,
		// and the window steps down to the floating mini player.
		videoControlFocus = vcBack
		videoBackOut(appConfig)
	default:
		// The picture itself: play / pause, and a double click full screen
		// (videomotion.go).
		vcPictureClick(mx, my, state)
	}
	return true
}

// HandleVideoKeyInput processes keyboard input for video controls. It reports
// whether the key belonged to the player: a consumed key must not also scroll
// the scene underneath the video, which is what made the grid move while a
// video was on screen.
// The per-video quality ladder the settings sheet cycles through. Auto defers
// to the global playbackResolution setting; the rest pin a yt-dlp ceiling to
// the video on screen.
var videoQualityLadder = []int{0, 360, 480, 720, 1080}

// nextVideoQualityHeight steps the quality ladder, wrapping back to Auto.
func nextVideoQualityHeight(current int) int {
	for i, h := range videoQualityLadder {
		if h == current {
			return videoQualityLadder[(i+1)%len(videoQualityLadder)]
		}
	}
	return videoQualityLadder[0]
}

// videoQualityCeilingHeight is the ceiling the Quality row names: the per-video
// pin when there is one, otherwise the global playbackResolution (which treats
// "best" and an empty value as 720p).
func videoQualityCeilingHeight(config *Config) int {
	if h := videoQualityOverrideFor(playbackPageURL()); h > 0 {
		return h
	}
	return playbackMaxHeight(config)
}

// deliveredVideoHeight is the height of the stream the player is actually
// decoding. It is the probed size of what yt-dlp handed over, before the decode
// cap, so it answers "what did we really get" rather than "what did we ask
// for": a 720p ceiling that only had a 480p rendition behind it reads 480.
//
// 0 means the player cannot answer: nothing is loaded, the probe has not
// finished, or playback stopped.
func deliveredVideoHeight() int {
	embeddedPlayer.mu.Lock()
	defer embeddedPlayer.mu.Unlock()
	switch embeddedPlayer.phase {
	case "playing", "paused":
		return int(embeddedPlayer.sourceHeight)
	}
	return 0
}

// videoQualityRowLabel is the Quality row's text: the ceiling it names, plus the
// resolution the stream on screen actually got when the two differ. A ceiling is
// a request — the rendition behind it can be smaller — so "Auto (720p)" over a
// 480p stream was describing a picture that was never delivered.
//
// The delivered half is left out when it matches the ceiling (repeating the same
// number is noise) and when the player has nothing to report, so the row never
// claims a stream that is not on screen. The toast that announces a switch keeps
// using videoQualityLabel: at that moment the new ceiling is set but the new
// stream has not started, so the delivered figure would still be the old one's.
func videoQualityRowLabel(config *Config) string {
	label := videoQualityLabel(config)
	got := deliveredVideoHeight()
	if got <= 0 || got == videoQualityCeilingHeight(config) {
		return label
	}
	return fmt.Sprintf("%s · got %dp", label, got)
}

// videoQualityLabel is the text shown in the settings sheet's Quality row. An
// active per-video pin names itself, otherwise the global playbackResolution
// ceiling is reported as Auto.
func videoQualityLabel(config *Config) string {
	if h := videoQualityOverrideFor(playbackPageURL()); h > 0 {
		return fmt.Sprintf("%dp (this video)", h)
	}
	res := strings.TrimSpace(config.Variables.PlaybackResolution)
	if res == "" || res == "best" {
		return "Auto (720p)"
	}
	if h, err := strconv.Atoi(res); err == nil && h > 0 {
		return fmt.Sprintf("Auto (%dp)", h)
	}
	return "Auto (720p)"
}

// cycleVideoQuality moves the Quality row through Auto → 360p → 480p → 720p →
// 1080p. While a video is playing or being prepared the change replays from
// the current position so the new ceiling is used immediately; with nothing
// loaded it only updates the label (and a later video picks it fresh).
func cycleVideoQuality(config *Config) {
	url := playbackPageURL()
	if strings.TrimSpace(url) == "" {
		showToast("Open a video to change quality", ToastInfo())
		return
	}
	previous := videoQualityOverrideFor(url)
	next := nextVideoQualityHeight(previous)
	setVideoQualityOverride(url, next)
	showToast("Quality: "+videoQualityLabel(config), ToastInfo())
	if state := snapshotVideoState(); state.phase != "idle" {
		pos := state.progress * state.duration
		if target, ok := vcSeekTake(); ok {
			// The new stream starts where a gathering seek was going.
			pos = target * state.duration
		}
		restartVideoAtQuality(config, url, pos, previous)
	}
}

// resolveQualityStream is the resolve behind a quality switch. It is a variable
// so the switch — including what it does when the resolve does not reach the
// player — can be exercised without a network or the helper tools. The switch
// only cares whether the picture came back, so the "was the link really tried"
// half of the load result stops here.
var resolveQualityStream = func(prepareSeq uint64, prepareCtx context.Context, config *Config, url string, startSec float64, logLabel string) bool {
	return resolveAndStartPlayback(prepareSeq, prepareCtx, config, url, startSec, logLabel).Played()
}

// restartVideoAtQuality re-resolves and replays the current page URL at its
// position, through the same load path playVideoInfo uses, but it keeps the
// playback queue and does not record a second play event.
//
// The loading state begins on the press, before the tools are resolved, exactly
// as it does for Play: re-resolving a stream is a wait like any other, so it
// must not leave the grid or the settings sheet live behind a spinner. B
// cancels it, and because the resolve stops before the player is touched, the
// video that was already playing carries on.
//
// previous is the ceiling the switch replaced. If the switch never reaches the
// player, the row goes back to the ceiling the picture was started with (or to
// previous when that is not known), so it never names a stream that is not on
// screen.
func restartVideoAtQuality(config *Config, url string, pos float64, previous int) {
	if config == nil || strings.TrimSpace(url) == "" {
		return
	}
	// The loading card names the source the same way the top bar does.
	prepareSeq, prepareCtx := beginVideoPrepare(sanitizeLogURL(url))
	go func() {
		if resolveQualityStream(prepareSeq, prepareCtx, config, url, pos, "restartVideoAtQuality") {
			return
		}
		// The switch never reached the player: B cancelled it, or the resolve
		// failed before the stream opened. The Quality row was already moved to
		// the new ceiling on the press, so put back the ceiling the picture on
		// screen was started with; the row and the stream then agree again.
		revertVideoQualitySwitch(prepareSeq, url, previous)
	}()
}

func HandleVideoKeyInput(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	state := snapshotVideoState()
	if state.phase == "idle" {
		return false
	}

	// While the video floats, its Back shortcut runs before scene dispatch
	// (videomini.go); the scene keeps every other key.
	if videoMiniVisible() {
		return false
	}
	// A player nothing draws must not keep the keyboard: that was the "glitch"
	// after leaving a video opened from Files — arrows changed the volume of
	// an invisible video instead of moving the file list.
	if mediaSessionOrphaned(appConfig) {
		mediaResolveOrphan(appConfig)
		return false
	}

	// The settings sheet is modal over the player while it is open. The
	// controls stay a while after it closes.
	if videoSettingsOpen {
		revealVideoChrome()
		switch e.Keysym.Sym {
		case sdl.K_UP:
			moveVideoSettingsItem(-1)
			PlayNavSound()
			return true
		case sdl.K_DOWN:
			moveVideoSettingsItem(1)
			PlayNavSound()
			return true
		case sdl.K_RETURN, sdl.K_SPACE:
			videoSettingsActivate(videoSettingsItem)
			PlayActivateSound()
			return true
		case sdl.K_ESCAPE, sdl.K_b, sdl.K_s, sdl.K_y:
			// Not on a repeat: the held S or Y that opened the sheet would
			// close it again. Escape and B step back from the Save offline
			// page first; S and Y, the keys that open the sheet, close it.
			if e.Repeat == 0 {
				if videoSettingsPage != videoSheetMain && (e.Keysym.Sym == sdl.K_ESCAPE || e.Keysym.Sym == sdl.K_b) {
					videoSettingsBack()
				} else {
					videoSettingsOpen = false
				}
				PlayBackSound()
			}
			return true
		}
	}

	// Shift + arrows mirror the shoulders: seek through the clip, or step to
	// the next/previous video of the list that launched it.
	if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
		switch e.Keysym.Sym {
		case sdl.K_LEFT:
			seekVideoBySeconds(state, -10)
		case sdl.K_RIGHT:
			seekVideoBySeconds(state, 10)
		case sdl.K_DOWN, sdl.K_n:
			skipPlayback(appConfig, 1)
		case sdl.K_UP, sdl.K_p:
			skipPlayback(appConfig, -1)
		default:
			return false
		}
		revealVideoChrome()
		return true
	}

	switch e.Keysym.Sym {
	case sdl.K_SPACE, sdl.K_RETURN:
		activateVideoControl()
	case sdl.K_LEFT:
		// With the controls away the first press only brings them back,
		// ringing Play (vcWakeChrome).
		if vcWakeChrome() {
			return true
		}
		moveVideoControlFocus(-1)
		PlayNavSound()
	case sdl.K_RIGHT:
		if vcWakeChrome() {
			return true
		}
		moveVideoControlFocus(1)
		PlayNavSound()
	case sdl.K_k:
		// The keys desktop players taught everyone: K plays and pauses, J and
		// L skip ten seconds, M mutes.
		if e.Repeat == 0 {
			PlayActivateSound()
			vcTogglePlayback()
		}
	case sdl.K_j:
		seekVideoBySeconds(state, -10)
	case sdl.K_l:
		seekVideoBySeconds(state, 10)
	case sdl.K_m:
		if e.Repeat == 0 {
			videoToggleMute()
		}
	case sdl.K_f:
		// The toggles act once per press, like the pad's buttons: a held
		// key's auto-repeat flickered them.
		if e.Repeat == 0 {
			toggleVideoFullscreen()
		}
	case sdl.K_c, sdl.K_x:
		// X and Y are the pad's captions and settings buttons, and the
		// keyboard's X and Y do the same: X used to fall through to the Media
		// screen's search shortcut (main.go) behind the playing video.
		if e.Repeat == 0 {
			toggleSubtitlesWithFeedback()
		}
	case sdl.K_s, sdl.K_y:
		if e.Repeat == 0 {
			videoSettingsToggle()
		}
	case sdl.K_ESCAPE:
		// Escape steps down a size instead of ending playback: full screen
		// shrinks back into the scene's window, and the window shrinks into the
		// floating mini player on the previous screen.
		videoBackOut(appConfig)
	case sdl.K_q:
		mediaEndSession(appConfig)
	case sdl.K_n:
		// Next / previous video of the list that launched this one. Plain keys,
		// so one press skips a short without a chord; the pad does the same on
		// its triggers.
		skipPlayback(appConfig, 1)
	case sdl.K_p:
		skipPlayback(appConfig, -1)
	case sdl.K_UP, sdl.K_DOWN:
		// The volume pill answers; the rest of the controls stay as they are.
		step := 0.1
		if e.Keysym.Sym == sdl.K_DOWN {
			step = -step
		}
		vcStepVolume(step)
		return true
	case sdl.K_1:
		SetPlaybackSpeed(0.5)
	case sdl.K_2:
		SetPlaybackSpeed(1.0)
	case sdl.K_3:
		SetPlaybackSpeed(1.5)
	case sdl.K_4:
		SetPlaybackSpeed(2.0)
	default:
		return false
	}
	revealVideoChrome()
	return true
}

// HandleVideoControllerInput processes gamepad input for video controls. Like
// the key handler it reports consumption, so D-pad presses move the control
// ring instead of also scrolling the scene behind the player.
func HandleVideoControllerInput(e *sdl.ControllerButtonEvent) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	state := snapshotVideoState()
	if state.phase == "idle" {
		return false
	}

	// The scene owns the pad while the video floats, except for Back handled
	// earlier by screenPadPress (videomini.go).
	if videoMiniVisible() {
		return false
	}
	// Nor may an invisible player keep it (see HandleVideoKeyInput).
	if mediaSessionOrphaned(appConfig) {
		mediaResolveOrphan(appConfig)
		return false
	}

	if videoSettingsOpen {
		// The controls stay a while after the sheet closes, rather than
		// fading the moment B leaves it.
		revealVideoChrome()
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_DPAD_UP:
			moveVideoSettingsItem(-1)
			PlayNavSound()
			return true
		case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
			moveVideoSettingsItem(1)
			PlayNavSound()
			return true
		case sdl.CONTROLLER_BUTTON_A:
			videoSettingsActivate(videoSettingsItem)
			PlayActivateSound()
			return true
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
			// B steps back from the Save offline page, then closes.
			videoSettingsBack()
			PlayBackSound()
			return true
		}
	}

	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A:
		activateVideoControl()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_GUIDE:
		// B steps down a size (full screen -> window -> mini player) instead
		// of stopping; the Stop control still ends playback. The handheld's
		// Menu/Select button (BACK, and GUIDE before it is normalised) is the
		// same Back: it used to fall through to the scene underneath, which
		// left the Media screen with the video still playing and nothing
		// drawing it.
		videoBackOut(appConfig)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		// With the controls away the first press only brings them back,
		// ringing Play (vcWakeChrome).
		if vcWakeChrome() {
			return true
		}
		moveVideoControlFocus(-1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		if vcWakeChrome() {
			return true
		}
		moveVideoControlFocus(1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		// The volume pill answers; the rest of the controls stay as they are.
		vcStepVolume(0.1)
		return true
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		vcStepVolume(-0.1)
		return true
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		// Shoulders skip through the clip; speed lives on the overlay button
		// and the 1-4 keys, where an accidental brush cannot change it.
		seekVideoBySeconds(state, -10)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		seekVideoBySeconds(state, 10)
	case sdl.CONTROLLER_BUTTON_X:
		toggleSubtitlesWithFeedback()
	case sdl.CONTROLLER_BUTTON_Y:
		videoSettingsToggle()
	default:
		return false
	}
	revealVideoChrome()
	return true
}

func pointInRect(px, py int32, r sdl.Rect) bool {
	return px >= r.X && px <= r.X+r.W && py >= r.Y && py <= r.Y+r.H
}
