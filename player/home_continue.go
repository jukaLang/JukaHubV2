package main

// home_continue.go — Home's Continue card: what Play resumes and what the card
// shows while it waits.
//
// Play used to call FavoriteItem.Play on the newest timeline item, which went
// wrong in exactly the cases a handheld meets most: a file opened "in the
// explorer" (xdg-open, which the device does not have, so nothing happened), a
// local video routed through the web resolver (which needs yt-dlp even for a
// file on the card, so offline it failed), and music, which was never put on
// the timeline at all — the card kept offering an old video while a song was
// playing. homePlayRecent resumes each kind itself:
//
//   - a video at its saved position (a local file straight into the embedded
//     player, a web video through the resolver, with a plain message offline);
//   - music: the loaded session is unpaused and its player opened, a recorded
//     track restarts in the player (which keeps long tracks' positions);
//   - a podcast episode, a channel, a game or a file in the viewer it belongs
//     to.
//
// The card previews what it resumes: a video shows a frame (its thumbnail, or a
// frame ffmpeg grabs from a local file at the resume point), music an audio
// graph built once per track from the file's own levels (animated while it
// plays), a game its box art. Previews are built off the render thread, cached
// on disk, and uploaded as one texture the size of the card with its corners
// already rounded, so drawing one is a single copy.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// ── What Continue resumes ────────────────────────────────────────────────

// homeContinueItem is what the card resumes: the music session when a track is
// loaded (playing or paused), otherwise the newest item on the timeline.
func homeContinueItem() (FavoriteItem, bool) {
	if track, _, _, ok := homeMusicSession(); ok {
		return FavoriteItem{Type: "audio", Data: FileEntry{Name: track.Name, Path: track.Path}, Timestamp: time.Now()}, true
	}
	items := getRecentItems()
	if len(items) == 0 {
		return FavoriteItem{}, false
	}
	return items[0], true
}

// homeMusicSession reports the track the music player has loaded, whether it
// is playing, and how far in it is. ok is false when nothing is loaded.
func homeMusicSession() (track musicTrack, playing bool, progress float32, ok bool) {
	music.mu.Lock()
	defer music.mu.Unlock()
	if music.current < 0 || music.current >= len(music.tracks) || (!music.playing && !music.paused) {
		return musicTrack{}, false, 0, false
	}
	track = music.tracks[music.current]
	if music.duration > 0 && music.position > 0 {
		progress = float32(music.position / music.duration)
		if progress > 1 {
			progress = 1
		}
	}
	return track, music.playing && !music.paused, progress, true
}

// homeLastNotedTrack is the track last put on the timeline, so a session is
// recorded once, not once per frame.
var homeLastNotedTrack string

// homeNoteMusicSession puts the music player's current track on the Continue
// timeline. It runs from Home's per-frame view data, so a song started from
// Files, the Music screen or search is on the card as soon as Home is shown.
func homeNoteMusicSession() {
	track, _, _, ok := homeMusicSession()
	if !ok || track.Path == "" || track.Path == homeLastNotedTrack {
		return
	}
	homeLastNotedTrack = track.Path
	homeRecordRecentAudio(track.Path, track.Name)
}

// homeRecordRecentAudio adds a song to the recent timeline (de-duplicated by
// path, capped like the other recents) and persists it.
func homeRecordRecentAudio(path, name string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	favoritesMutex.Lock()
	kept := make([]FavoriteItem, 0, len(favoritesStore.Recent)+1)
	kept = append(kept, FavoriteItem{Type: "audio", Data: FileEntry{Name: name, Path: path}, Timestamp: time.Now()})
	for _, item := range favoritesStore.Recent {
		if item.Type == "audio" {
			if fe, ok := item.Data.(FileEntry); ok && fe.Path == path {
				continue
			}
		}
		kept = append(kept, item)
	}
	const maxRecent = 20
	if len(kept) > maxRecent {
		kept = kept[:maxRecent]
	}
	favoritesStore.Recent = kept
	favoritesMutex.Unlock()
	homePersistFavorites()
}

// homePersistFavorites writes the timeline through the debounced user-config
// writer: this runs from Home's frame, where a synchronous config write would
// stall the render thread. The lists are copied under the lock so the writer's
// goroutine never shares a slice the main thread is still updating.
func homePersistFavorites() {
	if userConfigCache == nil {
		saveFavorites()
		return
	}
	favoritesMutex.Lock()
	fs := FavoritesStore{
		Videos: append([]FavoriteItem(nil), favoritesStore.Videos...),
		Recent: append([]FavoriteItem(nil), favoritesStore.Recent...),
		Files:  append([]FavoriteItem(nil), favoritesStore.Files...),
		IPTV:   append([]FavoriteItem(nil), favoritesStore.IPTV...),
	}
	favoritesMutex.Unlock()
	userConfigCache.Favorites = &fs
	saveUserConfigDebounced(userConfigCache)
}

// homeContinueResume plays what the card shows. It reports false when there is
// nothing to resume, and the card then opens Media instead.
func homeContinueResume(config *Config) bool {
	it, ok := homeContinueItem()
	if !ok {
		return false
	}
	homeContinueInvalidate()
	homePlayRecent(config, it)
	return true
}

// homeContinueCache is the card's last snapshot. A song playing now is noted
// on the timeline when the snapshot is rebuilt, so it is still offered after
// the player stops.
var homeContinueCache struct {
	at    uint64
	state HomeContinueState
	valid bool
}

// homeContinueRefreshMs is how stale the card may be: progress and "2m ago"
// labels move slowly, and a press invalidates the snapshot at once.
const homeContinueRefreshMs = 400

// homeContinueSnapshot returns the Continue card's state, rebuilding it when
// the last one is older than homeContinueRefreshMs.
func homeContinueSnapshot() HomeContinueState {
	now := sdl.GetTicks64()
	if homeContinueCache.valid && now-homeContinueCache.at < homeContinueRefreshMs {
		return homeContinueCache.state
	}
	homeNoteMusicSession()
	st := homeContinueEmptyState()
	if it, ok := homeContinueItem(); ok {
		st = homeContinueStateFor(it)
	}
	homeContinueCache.state, homeContinueCache.at, homeContinueCache.valid = st, now, true
	return st
}

// homeContinueStartsGames reports whether the first-run Continue card leads to
// the game library rather than to Media: on a handheld, playing the library is
// what the device is picked up for; on the desktop, Media is.
func homeContinueStartsGames() bool {
	return homeOnHandheld()
}

// homeContinueEmptyState is the Continue card before anything has been played.
// The largest card on the screen used to read "Nothing played recently" over
// an "Open Media" pill on every new device; it now says what the card will
// hold and offers the first step, which A takes (homeActivate opens the same
// destination the pill names).
func homeContinueEmptyState() HomeContinueState {
	if homeContinueStartsGames() {
		return HomeContinueState{
			Chip:     "Get started",
			Title:    "Your next game starts here",
			Subtitle: "What you play shows up here, ready to resume.",
			Action:   "Browse games",
		}
	}
	return HomeContinueState{
		Chip:     "Get started",
		Title:    "Your next video starts here",
		Subtitle: "What you watch shows up here, ready to resume.",
		Action:   "Open Media",
	}
}

// homeContinueInvalidate makes the next frame rebuild the card.
func homeContinueInvalidate() {
	homeContinueCache.valid = false
}

// homeIsLocalMedia reports whether a source is a file on this device rather
// than an address to stream.
func homeIsLocalMedia(src string) bool {
	s := strings.TrimSpace(src)
	if s == "" || strings.Contains(s, "://") {
		return false
	}
	info, err := os.Stat(s)
	return err == nil && !info.IsDir()
}

// homePlayRecent resumes one timeline item (Continue, search's Media history).
func homePlayRecent(config *Config, it FavoriteItem) {
	if config == nil {
		config = appConfig
	}
	switch it.Type {
	case "video":
		v, ok := it.Data.(VideoInfo)
		if !ok {
			break
		}
		if v.Position <= 0 {
			v.Position = favoriteResumePosition(v.GetURL())
		}
		src := strings.TrimSpace(v.GetURL())
		if homeIsLocalMedia(src) {
			homeResumeLocalVideo(config, v, src)
			return
		}
		if !IsOnline() {
			showToast("No internet connection — this video streams from the web", ToastWarn())
			return
		}
		playVideoInfo(config, v)
		return
	case "audio":
		fe, ok := it.Data.(FileEntry)
		if !ok || fe.Path == "" {
			break
		}
		if track, _, _, loaded := homeMusicSession(); loaded && sameAudioPath(track.Path, fe.Path) {
			// The track is already loaded: Continue picks it up where it is
			// (unpausing it) and brings the player forward.
			music.mu.Lock()
			paused := music.paused
			music.mu.Unlock()
			if paused {
				musicTogglePause()
			}
			openMusicPlayerScene(config)
			return
		}
		if !isHTTPMediaURL(fe.Path) {
			if _, err := os.Stat(fe.Path); err != nil {
				showToast("That song is no longer on this device: "+fe.Name, ToastWarn())
				return
			}
		} else if !IsOnline() {
			showToast(offlinePlainNotice, ToastWarn())
			return
		}
		openAudioPlayer(config, fe.Path)
		return
	case "podcast", "iptv":
		if !IsOnline() {
			showToast(offlinePlainNotice, ToastWarn())
			return
		}
	case "file":
		if fe, ok := it.Data.(FileEntry); ok && fe.Path != "" {
			name := fe.Name
			if name == "" {
				name = filepath.Base(fe.Path)
			}
			homeSearchOpenFile(config, fe.Path, name, fe.IsDir)
			return
		}
	}
	it.Play(config)
}

// homeResumeLocalVideo plays a video file from the card at its saved position.
// It goes straight to the embedded player: the web resolver needs yt-dlp even
// for a local path, which is what made Continue do nothing offline.
func homeResumeLocalVideo(config *Config, v VideoInfo, path string) {
	start := v.Position
	if v.Duration > 0 && start >= v.Duration*0.95 {
		start = 0
	}
	musicStopIfPlaying()
	ensureVideoScene(config)
	addRecentVideo(v)
	currentPlaybackURL = path
	go func() {
		// Resolve (and install, if needed) before playing: a missing ffmpeg
		// shows the install card instead of failing silently in the decoder.
		if _, err := helperToolPath("ffmpeg", config); err != nil {
			showToast(toolUnavailableMessage("ffmpeg", err), ToastError())
			return
		}
		StartEmbeddedPlaybackAt(config, path, start)
	}()
}

// ── What the card shows ──────────────────────────────────────────────────

// homeWaveBars is how many bars the audio graph has.
const homeWaveBars = 48

// homeContinueStateFor fills the card for one item.
func homeContinueStateFor(it FavoriteItem) HomeContinueState {
	st := HomeContinueState{
		HasRecent: true,
		Title:     it.Label(),
		Subtitle:  recentSourceLabel(it),
		Progress:  homeRecentProgress(it),
		Chip:      "Continue",
		Action:    "Resume",
	}
	switch it.Type {
	case "video":
		st.Kind, st.Chip = "video", "Continue watching"
		if v, ok := it.Data.(VideoInfo); ok {
			// The saved position is on the subtitle so the resume point reads
			// at a glance: "Video · 2h ago · 12:34 / 45:00".
			if v.Duration > 0 && v.Position > 0 {
				pos := math.Min(v.Position, v.Duration)
				st.Subtitle = fmt.Sprintf("%s · %s / %s", st.Subtitle, formatTime(pos), formatTime(v.Duration))
				st.Remaining = formatTime(v.Duration-pos) + " left"
			}
			st.Preview = homeVideoPreviewJob(v)
		}
	case "audio":
		st.Kind, st.Chip, st.Action = "music", "Continue listening", "Play"
		if fe, ok := it.Data.(FileEntry); ok {
			st.Title = homeTrackTitle(fe)
			if track, playing, progress, loaded := homeMusicSession(); loaded && sameAudioPath(track.Path, fe.Path) {
				st.Live = playing
				st.Progress = progress
				st.Chip = "Now playing"
				st.Action = "Open player"
				st.Subtitle = "Music · playing"
				if !playing {
					st.Chip, st.Action, st.Subtitle = "Paused", "Resume", "Music · paused"
				}
			} else {
				st.Subtitle = "Music"
				if !it.Timestamp.IsZero() {
					st.Subtitle += " · " + timeAgo(it.Timestamp)
				}
			}
			if dir := filepath.Base(filepath.Dir(fe.Path)); dir != "" && dir != "." && !isHTTPMediaURL(fe.Path) {
				st.Subtitle += " · " + dir
			}
			st.Wave, st.WaveReal = homeTrackWave(fe.Path)
			st.HasWave = true
		}
	case "podcast":
		st.Kind, st.Chip, st.Action = "music", "Continue listening", "Play"
		if ep, ok := it.Data.(podcastEpisode); ok {
			st.Wave, _ = homeTrackWave(ep.URL)
			st.HasWave = true
		}
	case "game":
		st.Kind, st.Chip, st.Action = "game", "Continue playing", "Play"
		if g, ok := it.Data.(recentGame); ok {
			st.Preview = homeArtPreviewJob(g)
		}
	case "iptv":
		st.Chip, st.Action = "Continue watching", "Watch"
	case "file":
		st.Chip, st.Action = "Recently opened", "Open"
	}
	return st
}

// homeTrackTitle is a track's display title: the name the player gave it, or
// the file name without its extension and track-number prefix.
func homeTrackTitle(fe FileEntry) string {
	name := strings.TrimSpace(fe.Name)
	if name == "" || strings.EqualFold(name, filepath.Base(fe.Path)) {
		name = strings.TrimSuffix(filepath.Base(fe.Path), filepath.Ext(fe.Path))
	}
	if ext := filepath.Ext(name); ext != "" && isAudioFile(name) {
		name = strings.TrimSuffix(name, ext)
	}
	return strings.TrimSpace(name)
}

// ── Preview frames ───────────────────────────────────────────────────────

// homePreviewJob says how to make one preview: download a thumbnail, grab a
// frame from a local video, or load a box-art file.
type homePreviewJob struct {
	Key  string
	URLs []string
	File string  // local video (frame grab) or picture (box art)
	At   float64 // seconds into a local video
	Art  bool    // File is a picture, not a video
}

// homeVideoPreviewJob describes a video's preview frame.
func homeVideoPreviewJob(v VideoInfo) homePreviewJob {
	src := strings.TrimSpace(v.GetURL())
	if homeIsLocalMedia(src) {
		info, err := os.Stat(src)
		if err != nil {
			return homePreviewJob{}
		}
		at := v.Position
		if at <= 0 {
			// A fresh file: a frame a little way in, past the black fade-in.
			at = 8
			if v.Duration > 0 {
				at = math.Min(at, v.Duration*0.1)
			}
		}
		return homePreviewJob{
			Key:  fmt.Sprintf("f:%s|%d|%d|%d", filepath.Clean(src), info.Size(), info.ModTime().Unix(), int(at)),
			File: src,
			At:   at,
		}
	}
	urls := realThumbnailCandidates(v)
	if len(urls) == 0 {
		return homePreviewJob{}
	}
	return homePreviewJob{Key: "v:" + urls[0], URLs: urls}
}

// homeArtPreviewJob describes a game's box art.
func homeArtPreviewJob(g recentGame) homePreviewJob {
	root := libraryArtworkRoot()
	art := romArtworkPath(scannedROM{Path: g.Path, System: g.System}, root)
	if !artworkCoverPresent(art) {
		return homePreviewJob{}
	}
	info, err := os.Stat(art)
	if err != nil {
		return homePreviewJob{}
	}
	return homePreviewJob{Key: fmt.Sprintf("a:%s|%d", art, info.ModTime().Unix()), File: art, Art: true}
}

// homePreviewKey names one preview at one size. It is a comparable struct, not
// a formatted string, so the per-frame lookups allocate nothing.
type homePreviewKey struct {
	key  string
	w, h int32
}

// diskKey is the shared cache key for the preview's PNG.
func (k homePreviewKey) diskKey() string {
	return fmt.Sprintf("%s%s|%dx%d", homePreviewDiskPrefix, k.key, k.w, k.h)
}

// homePreviewState holds the one preview the card shows: the PNG the worker
// made and the texture uploaded from it.
type homePreviewState struct {
	mu      sync.Mutex
	pending map[homePreviewKey]bool
	failed  map[homePreviewKey]time.Time
	key     homePreviewKey // the preview png belongs to
	png     []byte
	// Render thread only:
	texKey homePreviewKey
	tex    *sdl.Texture
	texW   int32
	texH   int32
	owner  *sdl.Renderer
}

var homePreview = homePreviewState{pending: map[homePreviewKey]bool{}, failed: map[homePreviewKey]time.Time{}}

const (
	homePreviewDiskPrefix = "hcont:"
	homePreviewTTL        = 30 * 24 * time.Hour
	homePreviewRetryAfter = 2 * time.Minute
	homePreviewTimeout    = 20 * time.Second
)

// homeRequestPreview starts building a preview of w×h unless it is ready, in
// flight or recently failed. It is cheap to call every frame.
func homeRequestPreview(job homePreviewJob, w, h int32) {
	if job.Key == "" || w <= 0 || h <= 0 {
		return
	}
	key := homePreviewKey{key: job.Key, w: w, h: h}
	homePreview.mu.Lock()
	if homePreview.key == key || homePreview.pending[key] {
		homePreview.mu.Unlock()
		return
	}
	if at, ok := homePreview.failed[key]; ok && time.Since(at) < homePreviewRetryAfter {
		homePreview.mu.Unlock()
		return
	}
	homePreview.pending[key] = true
	homePreview.mu.Unlock()
	go homeBuildPreview(job, key, int(w), int(h))
}

// homeBuildPreview makes the PNG for one preview (disk cache first) and
// publishes it for the render thread.
func homeBuildPreview(job homePreviewJob, key homePreviewKey, w, h int) {
	data := homePreviewDiskGet(key.diskKey())
	if len(data) == 0 {
		var src image.Image
		var err error
		switch {
		case job.File != "" && job.Art:
			src, err = homeDecodeFile(job.File)
		case job.File != "":
			src, err = homeGrabFrame(job.File, job.At, w, h)
		default:
			err = io.EOF
			for _, u := range job.URLs {
				raw, ferr := fetchThumbnailBytes(u)
				if ferr != nil || len(raw) == 0 {
					continue
				}
				if pic, _, derr := image.Decode(bytes.NewReader(raw)); derr == nil {
					src, err = pic, nil
					break
				}
			}
		}
		if err == nil && src != nil {
			data, err = homePreviewPNG(src, w, h, int(homeTileRadius))
		}
		if err != nil || len(data) == 0 {
			homePreview.mu.Lock()
			delete(homePreview.pending, key)
			homePreview.failed[key] = time.Now()
			homePreview.mu.Unlock()
			return
		}
		homePreviewDiskPut(key.diskKey(), data)
	}
	homePreview.mu.Lock()
	delete(homePreview.pending, key)
	homePreview.key, homePreview.png = key, data
	homePreview.mu.Unlock()
}

// homePreviewDiskGet reads a preview PNG from the shared cache; diskKey is
// homePreviewKey.diskKey, which already carries the "hcont:" prefix.
func homePreviewDiskGet(diskKey string) []byte {
	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		return nil
	}
	data, err := cacheGet(db, diskKey)
	if err != nil {
		return nil
	}
	return data
}

func homePreviewDiskPut(diskKey string, data []byte) {
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		_ = cacheSet(db, diskKey, data, homePreviewTTL)
	}
}

// homeDecodeFile decodes a picture file.
func homeDecodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pic, _, err := image.Decode(bufio.NewReader(f))
	return pic, err
}

// homeFFmpeg is the installed ffmpeg, or "" — a preview is never worth a
// download, so the card waits for the tool the app installs anyway.
func homeFFmpeg() string {
	path := strings.TrimSpace(getToolPath("ffmpeg", nil))
	if !toolPathRunnable(path) {
		return ""
	}
	return path
}

// homeGrabFrame asks ffmpeg for one frame of a local video at `at` seconds,
// already scaled to cover w×h.
func homeGrabFrame(path string, at float64, w, h int) (image.Image, error) {
	ffmpeg := homeFFmpeg()
	if ffmpeg == "" {
		return nil, fmt.Errorf("ffmpeg is not installed")
	}
	tmp, err := os.CreateTemp("", "jukahub-continue-*.jpg")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)
	scale := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d", w, h, w, h)
	for _, seek := range []float64{at, 0} {
		if err := runQuietCommand(ffmpeg, []string{
			"-loglevel", "error",
			"-ss", fmt.Sprintf("%.2f", seek),
			"-i", path,
			"-frames:v", "1",
			"-vf", scale,
			"-y", name,
		}, homePreviewTimeout); err != nil {
			continue
		}
		if pic, derr := homeDecodeFile(name); derr == nil {
			return pic, nil
		}
	}
	return nil, fmt.Errorf("no frame from %s", filepath.Base(path))
}

// homePreviewPNG scales src to cover w×h (cropping the longer side), rounds the
// corners to the card's radius with an anti-aliased edge, and encodes a PNG.
func homePreviewPNG(src image.Image, w, h, radius int) ([]byte, error) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 || w <= 0 || h <= 0 {
		return nil, fmt.Errorf("empty picture")
	}
	scale := math.Max(float64(w)/float64(sw), float64(h)/float64(sh))
	offX := (float64(sw)*scale - float64(w)) / 2
	offY := (float64(sh)*scale - float64(h)) / 2
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := b.Min.Y + int((float64(y)+0.5+offY)/scale)
		if sy >= b.Max.Y {
			sy = b.Max.Y - 1
		}
		for x := 0; x < w; x++ {
			sx := b.Min.X + int((float64(x)+0.5+offX)/scale)
			if sx >= b.Max.X {
				sx = b.Max.X - 1
			}
			r, g, bl, _ := src.At(sx, sy).RGBA()
			alpha := homeCornerAlpha(x, y, w, h, radius)
			i := dst.PixOffset(x, y)
			// Premultiplied RGBA, as image.RGBA stores it.
			dst.Pix[i+0] = uint8(float64(r>>8) * alpha)
			dst.Pix[i+1] = uint8(float64(g>>8) * alpha)
			dst.Pix[i+2] = uint8(float64(bl>>8) * alpha)
			dst.Pix[i+3] = uint8(255 * alpha)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// homeCornerAlpha is the coverage of pixel (x, y) in a w×h rect with rounded
// corners: 1 inside, 0 outside, a one-pixel ramp along the curve.
func homeCornerAlpha(x, y, w, h, radius int) float64 {
	if radius <= 0 {
		return 1
	}
	cx, cy := -1.0, -1.0
	switch {
	case x < radius && y < radius:
		cx, cy = float64(radius), float64(radius)
	case x >= w-radius && y < radius:
		cx, cy = float64(w-radius), float64(radius)
	case x < radius && y >= h-radius:
		cx, cy = float64(radius), float64(h-radius)
	case x >= w-radius && y >= h-radius:
		cx, cy = float64(w-radius), float64(h-radius)
	default:
		return 1
	}
	dx := float64(x) + 0.5 - cx
	dy := float64(y) + 0.5 - cy
	d := math.Sqrt(dx*dx + dy*dy)
	a := float64(radius) - d + 0.5
	if a <= 0 {
		return 0
	}
	if a >= 1 {
		return 1
	}
	return a
}

// homePreviewTexture returns the uploaded preview for job at w×h, uploading the
// worker's PNG on first use. nil while it is being made (or when there is
// none); the card then draws its ambient artwork.
func homePreviewTexture(renderer *sdl.Renderer, job homePreviewJob, w, h int32) (*sdl.Texture, int32, int32) {
	if renderer == nil || job.Key == "" {
		return nil, 0, 0
	}
	key := homePreviewKey{key: job.Key, w: w, h: h}
	if homePreview.owner != nil && homePreview.owner != renderer {
		// Textures belong to the renderer that made them, and SDL frees them
		// with it: destroying one now would touch freed memory, so the pointer
		// is only forgotten.
		homePreview.tex, homePreview.texKey, homePreview.owner = nil, homePreviewKey{}, nil
	}
	if homePreview.tex != nil && homePreview.texKey == key {
		return homePreview.tex, homePreview.texW, homePreview.texH
	}
	homePreview.mu.Lock()
	data := homePreview.png
	ready := homePreview.key == key
	homePreview.mu.Unlock()
	if !ready || len(data) == 0 {
		return nil, 0, 0
	}
	// Decoding a picture spends the same per-frame budget the thumbnails do.
	if !allowThumbnailDecode(frameCount, thumbnailDecodeBudgetPerFrame()) {
		return nil, 0, 0
	}
	rw, err := sdl.RWFromMem(data)
	if err != nil {
		return nil, 0, 0
	}
	tex, err := img.LoadTextureRW(renderer, rw, false)
	rw.Close()
	if err != nil || tex == nil {
		return nil, 0, 0
	}
	homeDropPreviewTexture()
	_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
	homePreview.tex, homePreview.texKey, homePreview.owner = tex, key, renderer
	homePreview.texW, homePreview.texH = w, h
	return tex, w, h
}

// homeDropPreviewTexture frees the card's preview texture (a new item, a
// renderer change, shutdown).
func homeDropPreviewTexture() {
	if homePreview.tex != nil {
		homePreview.tex.Destroy()
	}
	homePreview.tex, homePreview.texKey, homePreview.owner = nil, homePreviewKey{}, nil
}

// ── Audio graph ──────────────────────────────────────────────────────────

// homeWaveState caches each track's audio graph: the real levels once ffmpeg
// has read the file, in memory and on disk.
type homeWaveState struct {
	mu      sync.Mutex
	bars    map[string][homeWaveBars]float32
	order   []string
	pending map[string]bool
	failed  map[string]time.Time
}

var homeWave = homeWaveState{
	bars:    map[string][homeWaveBars]float32{},
	pending: map[string]bool{},
	failed:  map[string]time.Time{},
}

const (
	homeWaveDiskPrefix = "hwave:"
	homeWaveCacheMax   = 24
	// homeWaveRate and homeWaveMaxSeconds bound the decode: 2 kHz mono is
	// plenty for a level graph, and an hour-long mix is summarised from its
	// first 15 minutes rather than decoded whole on a handheld.
	homeWaveRate       = 2000
	homeWaveMaxSeconds = 900
	homeWaveTimeout    = 90 * time.Second
)

// homeTrackWave returns a track's audio graph and whether it is the track's
// real levels. Until ffmpeg has read the file (or for a stream, which is never
// downloaded just to draw it) the graph is a stable pattern derived from the
// path, so the card never flickers between shapes.
func homeTrackWave(path string) ([homeWaveBars]float32, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return homeSyntheticWave(path), false
	}
	homeWave.mu.Lock()
	bars, ok := homeWave.bars[path]
	busy := homeWave.pending[path]
	failedAt, failed := homeWave.failed[path]
	homeWave.mu.Unlock()
	if ok {
		return bars, true
	}
	if !busy && (!failed || time.Since(failedAt) > homePreviewRetryAfter) && homeIsLocalMedia(path) {
		homeWave.mu.Lock()
		homeWave.pending[path] = true
		homeWave.mu.Unlock()
		go homeBuildWave(path)
	}
	return homeSyntheticWave(path), false
}

// homeBuildWave measures one track and publishes its graph.
func homeBuildWave(path string) {
	info, err := os.Stat(path)
	key := ""
	if err == nil {
		key = fmt.Sprintf("%s%s|%d|%d", homeWaveDiskPrefix, path, info.Size(), info.ModTime().Unix())
	}
	var bars [homeWaveBars]float32
	got := false
	if key != "" {
		if db, derr := openSharedCache("jukahub.cache"); derr == nil {
			if raw, cerr := cacheGet(db, key); cerr == nil && len(raw) == homeWaveBars {
				for i, b := range raw {
					bars[i] = float32(b) / 255
				}
				got = true
			}
		}
	}
	if !got && err == nil {
		bars, got = homeMeasureWave(path)
		if got && key != "" {
			raw := make([]byte, homeWaveBars)
			for i, v := range bars {
				raw[i] = uint8(math.Round(float64(v) * 255))
			}
			if db, derr := openSharedCache("jukahub.cache"); derr == nil {
				_ = cacheSet(db, key, raw, homePreviewTTL)
			}
		}
	}
	homeWave.mu.Lock()
	defer homeWave.mu.Unlock()
	delete(homeWave.pending, path)
	if !got {
		homeWave.failed[path] = time.Now()
		return
	}
	if _, exists := homeWave.bars[path]; !exists {
		homeWave.order = append(homeWave.order, path)
		if len(homeWave.order) > homeWaveCacheMax {
			delete(homeWave.bars, homeWave.order[0])
			homeWave.order = homeWave.order[1:]
		}
	}
	homeWave.bars[path] = bars
}

// homeMeasureWave decodes the track to low-rate mono PCM through ffmpeg and
// turns it into homeWaveBars levels (peak RMS per slice, normalised).
func homeMeasureWave(path string) ([homeWaveBars]float32, bool) {
	var bars [homeWaveBars]float32
	ffmpeg := homeFFmpeg()
	if ffmpeg == "" {
		return bars, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), homeWaveTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-t", fmt.Sprint(homeWaveMaxSeconds),
		"-i", path, "-vn", "-ac", "1", "-ar", fmt.Sprint(homeWaveRate), "-f", "s16le", "-")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return bars, false
	}
	if err := cmd.Start(); err != nil {
		return bars, false
	}
	levels := homeWaveLevels(bufio.NewReaderSize(out, 32<<10), homeWaveRate/4)
	_ = cmd.Wait()
	if len(levels) < 4 {
		return bars, false
	}
	return homeWaveBucket(levels), true
}

// homeWaveLevels reads little-endian 16-bit samples and returns the RMS of
// every window of n samples.
func homeWaveLevels(r io.Reader, n int) []float32 {
	if n <= 0 {
		n = 500
	}
	levels := make([]float32, 0, 1024)
	var sum float64
	count := 0
	buf := make([]byte, 4096)
	carry := -1
	for {
		k, err := r.Read(buf)
		for i := 0; i < k; i++ {
			if carry < 0 {
				carry = int(buf[i])
				continue
			}
			sample := int16(uint16(carry) | uint16(buf[i])<<8)
			carry = -1
			v := float64(sample) / 32768
			sum += v * v
			count++
			if count == n {
				levels = append(levels, float32(math.Sqrt(sum/float64(n))))
				sum, count = 0, 0
			}
		}
		if err != nil {
			break
		}
	}
	if count > n/4 {
		levels = append(levels, float32(math.Sqrt(sum/float64(count))))
	}
	return levels
}

// homeWaveBucket folds window levels into the graph's bars: the loudest window
// in each slice, normalised to the track's peak and eased so quiet passages
// still show.
func homeWaveBucket(levels []float32) [homeWaveBars]float32 {
	var bars [homeWaveBars]float32
	if len(levels) == 0 {
		return bars
	}
	peak := float32(0)
	for i := range bars {
		lo := i * len(levels) / homeWaveBars
		hi := (i + 1) * len(levels) / homeWaveBars
		if hi <= lo {
			hi = lo + 1
		}
		if hi > len(levels) {
			hi = len(levels)
		}
		m := float32(0)
		for _, v := range levels[lo:hi] {
			if v > m {
				m = v
			}
		}
		bars[i] = m
		if m > peak {
			peak = m
		}
	}
	if peak <= 0 {
		return bars
	}
	for i, v := range bars {
		bars[i] = float32(0.12 + 0.88*math.Sqrt(float64(v/peak)))
	}
	return bars
}

// homeSyntheticWave is a stable stand-in graph for a track: a smooth random
// walk seeded from its path, so the same song always draws the same shape.
func homeSyntheticWave(path string) [homeWaveBars]float32 {
	var bars [homeWaveBars]float32
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	seed := h.Sum32() | 1
	level := 0.5
	for i := range bars {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		step := float64(seed%1000)/1000 - 0.5
		level += step * 0.45
		if level < 0.18 {
			level = 0.18 + (0.18 - level)
		}
		if level > 1 {
			level = 1 - (level - 1)
		}
		bars[i] = float32(level)
	}
	return bars
}

// drawHomeAudioGraph paints the track's levels as rounded bars mirrored about
// the rect's centre line. While the track plays the bars breathe, each on its
// own phase, which reads as a live equalizer without decoding audio twice.
func drawHomeAudioGraph(renderer *sdl.Renderer, rect sdl.Rect, bars *[homeWaveBars]float32, live bool, color sdl.Color) {
	if renderer == nil || bars == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	gap := max32(2, rect.W/int32(homeWaveBars)/3)
	barW := (rect.W - gap*int32(homeWaveBars-1)) / int32(homeWaveBars)
	if barW < 2 {
		barW = 2
	}
	t := float64(sdl.GetTicks64()) / 1000
	mid := rect.Y + rect.H/2
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, color)
	for i := 0; i < homeWaveBars; i++ {
		v := float64(bars[i])
		if live {
			v *= 0.55 + 0.45*math.Abs(math.Sin(t*2.3+float64(i)*0.6))*math.Abs(math.Cos(t*1.1+float64(i)*0.23))
		}
		bh := int32(v * float64(rect.H))
		if bh < 3 {
			bh = 3
		}
		x := rect.X + int32(i)*(barW+gap)
		if x+barW > rect.X+rect.W {
			break
		}
		if live {
			// Animated heights change every frame; plain fills keep the shape
			// cache from filling with one texture per bar height.
			_ = renderer.FillRect(&sdl.Rect{X: x, Y: mid - bh/2, W: barW, H: bh})
			continue
		}
		fillRoundedRect(renderer, x, mid-bh/2, barW, bh, barW/2, color)
	}
}
