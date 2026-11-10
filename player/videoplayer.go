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
func seekVideoBySeconds(state videoStateSnapshot, seconds float64) {
	if state.duration <= 0 {
		return
	}
	// The picture says how far it went (videochrome.go): a skip from the pad
	// or a shoulder otherwise shows only as a jump in the track.
	vcNoteSeek(seconds)
	SeekVideo(clampSeekFraction(state.progress, state.duration, seconds))
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
	if playbackQueue.shorts {
		// A short belongs to the feed: startShortAt keeps auto-advance armed.
		startShortAt(config, next)
	} else {
		playVideoInfo(config, playbackQueue.videos[next])
	}
	PlayActivateSound()
	return true
}

// cyclePlaybackSpeed steps to the next preset speed.
func cyclePlaybackSpeed() {
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
}

// toggleSubtitlesWithFeedback toggles subtitles and reports the result, since
// the button gives no other visible confirmation.
func toggleSubtitlesWithFeedback() {
	wasEnabled := subtitlesEnabled()
	if toggleSubtitles() {
		showToast("Subtitles on", ToastInfo())
	} else if wasEnabled {
		showToast("Subtitles off", ToastInfo())
	} else {
		showToast(subtitleStatusLine(), ToastInfo())
	}
}

// moveVideoSettingsItem steps the settings sheet selection, wrapping.
func moveVideoSettingsItem(delta int) {
	videoSettingsItem = (videoSettingsItem + delta + videoSettingsSheetRows) % videoSettingsSheetRows
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
	// opens) has no ring on screen, so A must not press it.
	if !videoControlDrawn(videoControlFocus) {
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
		ToggleVideoPlayback()
	}
	revealVideoChrome()
}

// videoChromeVisible reports whether the controls should be drawn. While not in
// full screen they are always visible; in full screen they fade after a moment,
// except while the settings sheet is open — the pad still drives it, so it must
// not vanish under the user.
func videoChromeVisible() bool {
	if !videoFullscreen || videoSettingsOpen {
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

func beginPlaybackSession(source, pageURL string, tempFile bool) *playbackSession {
	// A new session (a seek, the next video) supersedes an end the render
	// thread has not handled yet: that flag belongs to the session being
	// replaced and must not stop this one.
	playbackEnded.Store(false)
	persistSessionProgress(playbackCurrentSession())
	// Decided once per session, before the lock: it is a stat of the page path.
	local := !tempFile && mediaPageIsLocalFile(pageURL)

	playbackMu.Lock()
	prev := playbackCurrent
	playbackSeq++
	s := &playbackSession{id: playbackSeq, source: source, pageURL: pageURL, tempFile: tempFile, local: local}
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
			"-rw_timeout", "15000000")
	}
	// The firmware's own ffprobe cannot open https; such a URL is handed to
	// it through the loopback relay (ffmpegtls.go).
	args = append(args,
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate,avg_frame_rate,duration",
		"-of", "default=noprint_wrappers=1", ffmpegReadableInput(ffprobePath, path))
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
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

// ffmpegDecoderArgs builds the rawvideo decoder command. When the source is
// larger than the decode target it uses a scale filter in ffmpeg (cheaper than
// decoding 4K frames and letting the GPU shrink them); otherwise it asks for
// the native size directly.
func ffmpegDecoderArgs(path string, width, height int32, scaling bool, startSec float64) []string {
	// The decoder's stderr goes to errors.txt: without these, every start
	// (and every retry) wrote ffmpeg's banner and configure line there,
	// burying the one line that said why the stream failed.
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if startSec > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSec))
	}
	args = append(args, "-i", path)
	if scaling {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d:flags=bilinear", width, height))
	} else {
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

func startFFmpegDecoder(sess *playbackSession, ffmpegPath, path string, width, height int32, startSec float64) {
	embeddedPlayer.mu.Lock()
	srcW, srcH := embeddedPlayer.sourceWidth, embeddedPlayer.sourceHeight
	embeddedPlayer.mu.Unlock()
	scaling := srcW > 0 && srcH > 0 && (srcW != width || srcH != height)

	args := ffmpegDecoderArgs(ffmpegReadableInput(ffmpegPath, path), width, height, scaling, startSec)
	cmd := exec.CommandContext(sess.ctx, ffmpegPath, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		logWarn("video", "ffmpeg stdout pipe failed: %v", err)
		return
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		logWarn("video", "ffmpeg failed to start: %v", err)
		return
	}
	if !sess.registerVideoCmd(cmd) {
		cmd.Wait()
		return
	}
	defer sess.clearVideoCmd(cmd)
	defer cmd.Wait()

	// 1.5 bytes per pixel: the Y plane (w*h) then U and V quarters each. The
	// product is formed in int64 and capped before it narrows: width and height
	// come from the container's header, and a 32-bit int product would wrap for
	// a large one, handing make a buffer far smaller than the frames ffmpeg
	// then writes into it.
	frameBytes := int64(width) * int64(height) * 3 / 2
	if frameBytes <= 0 {
		// A probe that reported no usable size leaves nothing to size by.
		return
	}
	if frameBytes > maxVideoFrameBytes {
		logWarn("video", "refusing a %dx%d frame buffer", width, height)
		return
	}
	frameSize := int(frameBytes)
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
			logDebug("video", "First frame received: %dx%d, %d bytes, fps=%.1f, frameDur=%v", width, height, len(buf), fps, frameDuration)
		}
	}

	// The pipe closed. A cancelled session (stop, seek, next video) is not an
	// end of stream; only a decoder that ran to EOF raises the flag.
	if sess.ctx.Err() == nil && playbackCurrentSession() == sess && frameCount > 0 {
		logDebug("video", "Reached end of stream after %d frames", frameCount)
		sess.ended.Store(true)
		playbackEnded.Store(true)
	}
}

func startFFmpegAudio(sess *playbackSession, ffmpegPath, path string, startSec float64) {
	rate := GetVideoAudioRate()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if startSec > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSec))
	}
	args = append(args, "-i", ffmpegReadableInput(ffmpegPath, path),
		"-f", "s16le",
		"-ac", "2",
		"-ar", fmt.Sprintf("%d", rate),
		"-",
	)
	cmd := exec.CommandContext(sess.ctx, ffmpegPath, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		logWarn("audio", "ffmpeg audio pipe failed: %v", err)
		return
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		logWarn("audio", "ffmpeg audio failed to start: %v", err)
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
	if raw == nil || w <= 0 || h <= 0 || len(raw) != int(w*h*3/2) {
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
	// A stream is starting, so this ceiling is now the one on screen. The
	// Quality row reads it back if a later switch is cancelled before it plays,
	// which is what keeps the row describing the picture rather than the press.
	commitVideoQualityPlayback(pageURL, effectivePlaybackMaxHeight(config, pageURL))
	// A new session owns the picture again: the floating window must not keep
	// showing the previous video's scene.
	videoMiniClear()
	// The top bar names this session from its page URL (see statusbar_media.go).
	rememberVideoPage(pageURL)
	sess := beginPlaybackSession(url, pageURL, tempFile)
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
		// A new video plays, whatever the shared device was left in.
		PauseVideoAudio(false)

		// Sidecar / embedded subtitle tracks belong to this source. They load
		// beside the decoders, not before them: finding an embedded track
		// means ffmpeg reading the whole file, and a large MKV used to sit on
		// a black screen for that long while the clock (decoderStartTime,
		// already set) ran on, leaving progress and cues out of step.
		go prepareVideoSubtitles(sess, config, url)

		go startFFmpegAudio(sess, ffmpegPath, url, startSec)
		startFFmpegDecoder(sess, ffmpegPath, url, w, h, startSec)

		sess.finish()
	}()
}

func StopEmbeddedPlayback() {
	playbackEnded.Store(false)
	playbackMu.Lock()
	sess := playbackCurrent
	playbackCurrent = nil
	playbackMu.Unlock()

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
	// stream the moment the poll sees it again.
	partyNoteStop(currentPlaybackURL)
	currentPlaybackURL = ""
	// The next session records where it was started from afresh.
	mediaForgetPlayOrigin()
	// Nothing is playing any more, so the top bar drops its now-playing slot.
	forgetVideoPage()
	// Leaving playback also leaves full screen, the settings sheet and the
	// floating window behind.
	videoFullscreen = false
	videoSettingsOpen = false
	videoControlFocus = vcNone
	videoMiniClear()
	setSubtitlesEnabled(false)
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
	// A seek resumes playback, so the device resumes too. Seeking from pause
	// left it paused under a "playing" player: the picture ran on in silence
	// and the audio decoder blocked on a queue that never drained.
	PauseVideoAudio(false)

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

// computeVideoControlLayout fills videoControlRects for the given geometry
// (vcLayout, videochrome.go). It runs every frame regardless of the overlay
// cache, so mouse/touch hit testing keeps working when the cached overlay path
// is used, and it records the window itself: a windowed player's hit test is
// bounded by it, so the scene around the video keeps its own taps.
func computeVideoControlLayout(config *Config, x, y, w, h int32, state videoStateSnapshot) {
	vcLayout(config, x, y, w, h, state)
}

// videoAdvanceProgress moves the stored position forward from wall-clock time
// and writes the throttled resume position. The in-scene overlay and the
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
		// The session's own page URL, not currentPlaybackURL: that switches
		// to the next video the moment Play is pressed, while this one keeps
		// playing through the resolve, and its position was written into the
		// next video's history entry every five seconds.
		if sess := playbackCurrentSession(); sess != nil && sess.pageURL != "" && state.duration > 0 {
			updateRecentPosition(sess.pageURL, fraction*state.duration)
			saveFavorites()
		}
	}
}

// RenderVideoOverlay renders the current video frame and on-screen controls.
// videoSyncPlaybackEnd closes a session whose decoder hit the end of the file.
// It runs on the render thread so the phase change, the frame-queue flush and
// the repaint all happen in one place, and it is called from the main loop so a
// video that finished while its scene was off screen still releases the player.
func videoSyncPlaybackEnd() {
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
	} else if upNextAfterPlayback(shortsAutoplay) {
		// Up Next holds the hand-off. It is taken on the main loop, once the
		// player this video is releasing is idle again.
		upNextAdvancePending = true
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

	// Full screen with the chrome faded out: just the picture and subtitles.
	if videoFullscreen && !videoChromeVisible() {
		videoSettingsSheetDrawn = sdl.Rect{}
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
// The overlay cache renders this once per element size and theme change;
// everything state-dependent is drawn per frame by drawVideoOverlayLive.
// (ox, oy) is the origin the layer is drawn at: the element position on screen,
// or (0, 0) when rendering into the element-sized cache texture.
func drawVideoOverlayStatic(renderer *sdl.Renderer, ox, oy, w, h int32) {
	vcDrawScrims(renderer, ox, oy, w, h)
}

// drawVideoOverlayLive draws everything that changes while a video plays — the
// glass bar and its controls, the chip and title, the subtitle cue, the
// settings sheet and the paused picture's play button (vcDrawChrome) — every
// frame on top of the static layer.
func drawVideoOverlayLive(renderer *sdl.Renderer, config *Config, state videoStateSnapshot, x, y, w, h int32) {
	vcDrawChrome(renderer, config, state, x, y, w, h)
}

// drawVideoOverlayFallback draws the entire overlay from scratch. Used when
// the cached overlay texture is unavailable.
func drawVideoOverlayFallback(renderer *sdl.Renderer, config *Config, state videoStateSnapshot,
	currentTex *sdl.Texture, x, y, w, h int32) {
	drawVideoFrameArea(renderer, currentTex, x, y, w, h)
	drawVideoOverlayStatic(renderer, x, y, w, h)
	drawVideoOverlayLive(renderer, config, state, x, y, w, h)
}

// Layout of the settings sheet. The row metrics are shared by the renderer and
// the click hit test, so a tap always lands on the row it is drawn over.
const (
	videoSettingsSheetRows = 6
	videoSettingsRowH      = int32(48)
	videoSettingsTopPad    = int32(20)
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

	// In full screen a tap anywhere brings the controls back first — except
	// while the stream opens, when the loader's close button is always drawn.
	if videoFullscreen && !videoChromeVisible() && state.phase != "downloading" {
		revealVideoChrome()
		return true
	}
	if videoFullscreen {
		revealVideoChrome()
	}

	// Windowed, the player covers only the rect its controls were laid out in.
	// Clicks on the rest of the scene stay the scene's, and clicks inside the
	// window reach the controls — they used to be drawn there but unreachable, so
	// the only working close was Stop in the bar, and only in full screen. This
	// gate sits after the settings sheet below, which is modal over the panel.
	if !videoFullscreen && !videoSettingsOpen && !pointInRect(mx, my, videoControlBounds) {
		return false
	}

	// The settings sheet owns every click while it is open. Its rows are hit
	// where they were drawn (videoSettingsSheetDrawn): the windowed player
	// draws the sheet inside its own rect.
	if videoSettingsOpen {
		if idx := vcSheetRow(mx, my); idx >= 0 {
			videoSettingsItem = idx
			videoSettingsActivate(idx)
			return true
		}
		videoSettingsOpen = false
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
	case vcHit(mx, my, r.fullscreen):
		videoControlFocus = vcFullscreen
		toggleVideoFullscreen()
	case vcHit(mx, my, r.overlayPlay), vcHit(mx, my, r.playPause):
		videoControlFocus = vcPlayPause
		ToggleVideoPlayback()
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
		SeekVideo(float64(mx-r.progress.X) / float64(r.progress.W))
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
	}
	return true
}

// videoSettingsActivate runs the action for one settings sheet row.
func videoSettingsActivate(idx int) {
	switch idx {
	case 0:
		toggleSubtitlesWithFeedback()
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
		cycleVideoQuality(appConfig)
	case 4:
		// The queue outlives the results screen it was filled from, so this is
		// the one place it can be emptied once those rows are gone.
		if n := upNextClear(); n > 0 {
			PlayBackSound()
			ShowToast(fmt.Sprintf("Up Next cleared (%d)", n), ToastKindInfo)
		} else {
			ShowToast("Up Next is empty", ToastKindInfo)
		}
	case 5:
		videoSettingsOpen = false
	}
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
	if h := videoQualityOverrideFor(currentPlaybackURL); h > 0 {
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
	if h := videoQualityOverrideFor(currentPlaybackURL); h > 0 {
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
	url := currentPlaybackURL
	if strings.TrimSpace(url) == "" {
		showToast("Open a video to change quality", ToastInfo())
		return
	}
	previous := videoQualityOverrideFor(url)
	next := nextVideoQualityHeight(previous)
	setVideoQualityOverride(url, next)
	showToast("Quality: "+videoQualityLabel(config), ToastInfo())
	if state := snapshotVideoState(); state.phase != "idle" {
		restartVideoAtQuality(config, url, state.progress*state.duration, previous)
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

	// While the video floats in its mini window the scene keeps the keyboard:
	// Escape and B navigate back, arrows scroll the list under the window.
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

	// The settings sheet is modal over the player while it is open.
	if videoSettingsOpen {
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
			// close it again.
			if e.Repeat == 0 {
				videoSettingsOpen = false
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
		moveVideoControlFocus(-1)
		PlayNavSound()
	case sdl.K_RIGHT:
		moveVideoControlFocus(1)
		PlayNavSound()
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
			videoSettingsOpen = !videoSettingsOpen
			videoSettingsItem = 0
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

	// The scene owns the pad while the video floats in its mini window.
	if videoMiniVisible() {
		return false
	}
	// Nor may an invisible player keep it (see HandleVideoKeyInput).
	if mediaSessionOrphaned(appConfig) {
		mediaResolveOrphan(appConfig)
		return false
	}

	if videoSettingsOpen {
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
			videoSettingsOpen = false
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
		moveVideoControlFocus(-1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		moveVideoControlFocus(1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		AdjustVolume(0.1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		AdjustVolume(-0.1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		// Shoulders skip through the clip; speed lives on the overlay button
		// and the 1-4 keys, where an accidental brush cannot change it.
		seekVideoBySeconds(state, -10)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		seekVideoBySeconds(state, 10)
	case sdl.CONTROLLER_BUTTON_X:
		toggleSubtitlesWithFeedback()
	case sdl.CONTROLLER_BUTTON_Y:
		videoSettingsOpen = !videoSettingsOpen
		videoSettingsItem = 0
	default:
		return false
	}
	revealVideoChrome()
	return true
}

func pointInRect(px, py int32, r sdl.Rect) bool {
	return px >= r.X && px <= r.X+r.W && py >= r.Y && py <= r.Y+r.H
}
