package main

// mediaextract.go — keeping a video's sound as a track.
//
// "Save audio only" asks yt-dlp for the soundtrack of a web video (downloads.go,
// the long-standing m4a path). A video already on the card needs no network
// for that: ffmpeg copies its audio stream into an .m4a in the music folder in
// seconds, without re-encoding — AAC in an MP4, which is what a YouTube
// download carries. Sound in another codec (Opus in a WebM or an MKV) cannot
// be copied into an .m4a, so it is encoded to AAC instead, which takes longer
// but still runs on the handheld. The track is filed in the Downloads library
// with the video's title, channel and picture, so the music player and Media
// list it like any saved track.
//
// One extraction runs at a time, like the download queue: ffmpeg on the
// handheld competes with whatever is playing. A copy still reads the whole
// container, so its time limit grows with the file; a copy that runs out of
// time is not followed by an encode, which would read it all again for much
// longer. Quitting JukaHub stops a running extraction (a Cleanup hook), so
// ffmpeg does not go on writing to the card after the app is gone.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// mediaExtractCopyBase and mediaExtractCopyMax bound a stream copy. It is
	// limited by how fast the card reads, so the limit grows with the file
	// (mediaExtractCopyTimeout): a 3 GB film needs minutes, not seconds.
	mediaExtractCopyBase = 2 * time.Minute
	mediaExtractCopyMax  = 20 * time.Minute
	// mediaExtractEncodeTimeout bounds an AAC encode of a long video on the
	// handheld's cores.
	mediaExtractEncodeTimeout = 30 * time.Minute
	// mediaExtractMinBytes is the smallest track worth keeping once ffmpeg
	// has exited cleanly: an .m4a's header and a fraction of a second of
	// sound. The download queue's larger floor guards against error pages,
	// which a local ffmpeg run cannot produce, and it refused clips of about
	// a second.
	mediaExtractMinBytes = 4 << 10
)

// mediaExtracting is raised while an extraction runs.
var mediaExtracting atomic.Bool

// mediaExtractCancel holds the running extraction's cancel, for the Cleanup
// hook; nil while none runs.
var mediaExtractCancel struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

var (
	// errMediaNoAudio marks a video without a sound track.
	errMediaNoAudio = errors.New("this video has no sound track")
	// errMediaTooShort marks a track too short to keep.
	errMediaTooShort = errors.New("the sound track is too short to keep")
	// errMediaCopyTooLong marks a copy that ran out of time.
	errMediaCopyTooLong = errors.New("copying the sound track took too long")
	// errMediaCardFull marks a card without room for the track.
	errMediaCardFull = errors.New("the card is almost full")
	// errMediaNoFFmpeg marks an ffmpeg that is missing and could not be
	// installed.
	errMediaNoFFmpeg = errors.New("ffmpeg could not be installed")
)

func init() {
	// Quitting stops ffmpeg: it must not keep writing to the card after
	// JukaHub is gone. The staging folder it leaves is cleared by the next
	// extraction.
	Cleanup = append(Cleanup, mediaExtractStop)
}

// mediaExtractMeta is what the new track is filed with.
type mediaExtractMeta struct {
	Title     string
	Channel   string
	Duration  float64
	Thumbnail string
	ThumbFile string
	Source    string
}

// mediaExtractCopyTimeout is how long a stream copy of a file of size bytes
// may take: two minutes, plus a second for every 4 MB the card has to read,
// up to mediaExtractCopyMax.
func mediaExtractCopyTimeout(size int64) time.Duration {
	limit := mediaExtractCopyBase
	if size > 0 {
		limit += time.Duration(size/(4<<20)) * time.Second
	}
	if limit > mediaExtractCopyMax {
		limit = mediaExtractCopyMax
	}
	return limit
}

// mediaExtractArgs is the ffmpeg command line for one attempt: copy the first
// audio stream as it is, or encode it to AAC. Video, subtitle and data streams
// are left out, and the index goes to the front so the track starts playing
// before it is read to the end.
func mediaExtractArgs(in, out string, encode bool) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-i", in, "-map", "0:a:0", "-vn", "-sn", "-dn"}
	if encode {
		args = append(args, "-c:a", "aac", "-b:a", "160k")
	} else {
		args = append(args, "-c:a", "copy")
	}
	return append(args, "-movflags", "+faststart", out)
}

// mediaExtractRunner runs one ffmpeg command line; tests swap it so nothing
// is executed. A file without sound is recognised here, from everything
// ffmpeg printed: its answer spans several lines ("Stream map '0:a:0' matches
// no streams." then "To ignore this, add a trailing '?' to the map.", and on
// ffmpeg 7 more after that), so the last line alone never says it.
var mediaExtractRunner = func(ctx context.Context, ffmpeg string, args []string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr textTail
	cmd.Stderr = &stderr
	cmd.WaitDelay = 3 * time.Second
	schedHideWindow(cmd)
	if err := cmd.Run(); err != nil {
		if mediaExtractNoAudioText(stderr.buf.String()) {
			return fmt.Errorf("%w (%v)", errMediaNoAudio, err)
		}
		if line := stderr.lastLine(); line != "" {
			return fmt.Errorf("%w: %s", err, line)
		}
		return err
	}
	return nil
}

// mediaExtractNoAudioText reports ffmpeg's words for an input without a sound
// track: the map that matches nothing (every version) or an output left with
// no stream at all.
func mediaExtractNoAudioText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "matches no streams") || strings.Contains(lower, "does not contain any stream")
}

// mediaExtractNoAudio reports an attempt that failed because the file has no
// sound track, which no second attempt can change.
func mediaExtractNoAudio(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errMediaNoAudio) || mediaExtractNoAudioText(err.Error())
}

// mediaExtractMetaFor gathers what the track is filed with: the video's own
// Downloads entry when it has one (title, channel, length, picture, page),
// else the title the player or the card showed, else the file name.
func mediaExtractMetaFor(videoPath, title, page string) mediaExtractMeta {
	meta := mediaExtractMeta{Title: strings.TrimSpace(title), Source: strings.TrimSpace(page)}
	if e, ok := dlLibraryFindPath(videoPath); ok {
		meta.Title = dlFirstNonEmpty(e.Title, meta.Title)
		meta.Channel = e.Channel
		meta.Duration = e.Duration
		meta.Thumbnail = e.Thumbnail
		meta.ThumbFile = e.ThumbFile
		meta.Source = dlFirstNonEmpty(e.Source, meta.Source)
	}
	if meta.Title == "" {
		meta.Title = dlTitleFromFile(videoPath)
	}
	return meta
}

// mediaExtractAudio starts saving a local video's sound as a track and reports
// whether it started. A refusal is said in a toast and leaves the caller's
// sheet open; a start is said at once, and again when the track is filed or
// why not. It plays no sound itself: the pad and key paths of the sheets that
// call it play their own.
func mediaExtractAudio(config *Config, videoPath, title, page string) bool {
	videoPath = strings.TrimSpace(videoPath)
	if videoPath == "" || !regularFile(videoPath) {
		ShowToast("The video is not on the card any more", ToastKindWarning)
		return false
	}
	if video, ok := mlibMediaKind(videoPath); ok && !video {
		ShowToast("This is a sound file already", ToastKindInfo)
		return false
	}
	meta := mediaExtractMetaFor(videoPath, title, page)
	// The page's sound is saved already — extracted before, or downloaded
	// with "Save audio only": a second copy would only be "Title (2)".
	if meta.Source != "" {
		if _, ok := dlLibraryFindSource(meta.Source, false); ok {
			ShowToast(dlAlreadySavedNotice, ToastKindInfo)
			return false
		}
	}
	if !mediaExtracting.CompareAndSwap(false, true) {
		ShowToast("Already extracting a soundtrack — one at a time", ToastKindInfo)
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	mediaExtractCancel.mu.Lock()
	mediaExtractCancel.cancel = cancel
	mediaExtractCancel.mu.Unlock()
	ShowToast("Extracting the audio — it will be in Downloads and the music player", ToastKindInfo)
	go func() {
		defer func() {
			mediaExtractCancel.mu.Lock()
			mediaExtractCancel.cancel = nil
			mediaExtractCancel.mu.Unlock()
			cancel()
			mediaExtracting.Store(false)
		}()
		dest, err := mediaExtractRun(ctx, config, videoPath, meta)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				// JukaHub is quitting: nobody is left to read a toast.
				logDebug("downloads", "audio extraction from %s stopped", filepath.Base(videoPath))
				return
			}
			logWarn("downloads", "audio extraction from %s failed: %v", filepath.Base(videoPath), err)
			ShowToast(mediaExtractFailure(err), ToastKindWarning)
			return
		}
		logInfo("downloads", "extracted the audio of %s into %s", filepath.Base(videoPath), filepath.Base(dest))
		ShowToast("Audio saved: "+dlShortTitle(meta.Title, 40), ToastKindSuccess)
	}()
	return true
}

// mediaExtractStop cancels the running extraction, if any: the Cleanup hook
// that keeps ffmpeg from outliving the app.
func mediaExtractStop() {
	mediaExtractCancel.mu.Lock()
	cancel := mediaExtractCancel.cancel
	mediaExtractCancel.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// mediaExtractFailure words a failed extraction.
func mediaExtractFailure(err error) string {
	switch {
	case errors.Is(err, errMediaNoAudio):
		return "This video has no sound to extract"
	case errors.Is(err, errMediaTooShort):
		return "This clip's sound is too short to keep"
	case errors.Is(err, errMediaCopyTooLong), errors.Is(err, context.DeadlineExceeded):
		return "Extracting the audio took too long"
	case errors.Is(err, errMediaCardFull):
		return "The card is almost full — free some space, then extract again"
	case errors.Is(err, errMediaNoFFmpeg):
		return "Audio not extracted: ffmpeg could not be installed — try again online"
	}
	if line := downloadFailureLine(err); line != "" && line != "Download failed" {
		return "Audio not extracted: " + line
	}
	return "Audio not extracted"
}

// mediaExtractTrack writes the sound of videoPath into out: a stream copy, and
// an AAC encode when the copy is refused (a codec an .m4a cannot carry). The
// copy's time limit grows with the file. No encode follows a copy that ran out
// of time — it would read the whole file again, for much longer — nor an
// answer an encode cannot change: no sound track, or a track too short to
// keep. ctx is cancelled when JukaHub quits.
func mediaExtractTrack(ctx context.Context, ffmpeg, videoPath, out string) error {
	var size int64
	if info, err := os.Stat(videoPath); err == nil {
		size = info.Size()
	}
	attempt := func(encode bool, limit time.Duration) error {
		attemptCtx, cancel := context.WithTimeout(ctx, limit)
		defer cancel()
		_ = os.Remove(out)
		if err := mediaExtractRunner(attemptCtx, ffmpeg, mediaExtractArgs(videoPath, out, encode)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attemptCtx.Err() != nil {
				return attemptCtx.Err()
			}
			return err
		}
		// ffmpeg exited cleanly, so the file is real media; only a track
		// with next to no sound in it is not worth keeping.
		if info, err := os.Stat(out); err != nil || info.Size() < mediaExtractMinBytes {
			return errMediaTooShort
		}
		return nil
	}
	runErr := attempt(false, mediaExtractCopyTimeout(size))
	switch {
	case runErr == nil, ctx.Err() != nil, mediaExtractNoAudio(runErr), errors.Is(runErr, errMediaTooShort):
		// Done, stopped, or an answer an encode cannot change.
	case errors.Is(runErr, context.DeadlineExceeded):
		runErr = errMediaCopyTooLong
	default:
		logDebug("downloads", "audio copy refused (%v); encoding to AAC instead", runErr)
		runErr = attempt(true, mediaExtractEncodeTimeout)
	}
	if runErr != nil && mediaExtractNoAudio(runErr) {
		return errMediaNoAudio
	}
	return runErr
}

// mediaExtractRun is the extraction itself, on the caller's goroutine: stage
// the track in a private folder, copy or encode, check it, move it into the
// music folder under the video's title, and file it. ctx is cancelled when
// JukaHub quits.
func mediaExtractRun(ctx context.Context, config *Config, videoPath string, meta mediaExtractMeta) (string, error) {
	ffmpeg, err := helperToolPathCtx(ctx, "ffmpeg", config)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: %v", errMediaNoFFmpeg, err)
	}
	if err := downloadsRequireSpace(downloadsRoot(), freeBytes); err != nil {
		return "", fmt.Errorf("%w: %v", errMediaCardFull, err)
	}
	// The staging folder is its own: .partial belongs to the download queue,
	// which clears it before a launch's first transfer.
	stage := filepath.Join(downloadsRoot(), ".extract")
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", fmt.Errorf("cannot write to the SD card: %w", err)
	}
	defer os.RemoveAll(stage)
	out := filepath.Join(stage, "audio.m4a")
	if err := mediaExtractTrack(ctx, ffmpeg, videoPath, out); err != nil {
		return "", err
	}

	dir := downloadsMusicDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create the music folder: %w", err)
	}
	name := safeDownloadName(meta.Title)
	if name == "" {
		name = safeDownloadName(dlTitleFromFile(videoPath))
	}
	if name == "" {
		name = "Track"
	}
	// Never over a file that is there: a download of the same title can land
	// in the music folder at the same moment (placeDownloadedFile).
	dest, err := placeDownloadedFile(out, dir, name, ".m4a")
	if err != nil {
		return "", err
	}
	var trackSize int64
	if info, err := os.Stat(dest); err == nil {
		trackSize = info.Size()
	}
	entry := dlLibraryEntry{
		ID:        dlNewID(),
		Path:      dest,
		Video:     false,
		Title:     dlFirstNonEmpty(meta.Title, name),
		Channel:   meta.Channel,
		Duration:  meta.Duration,
		Thumbnail: meta.Thumbnail,
		Source:    meta.Source,
		Size:      trackSize,
		Added:     time.Now(),
	}
	// The video's poster comes along, so the track has its picture offline.
	if meta.ThumbFile != "" && regularFile(meta.ThumbFile) {
		if err := os.MkdirAll(dlThumbDir(), 0o755); err == nil {
			thumb := filepath.Join(dlThumbDir(), entry.ID+".jpg")
			if copyFile(meta.ThumbFile, thumb) == nil {
				entry.ThumbFile = thumb
			}
		}
	}
	dlLibraryLoadNow()
	dlLibraryAdd(entry)
	if entry.ThumbFile == "" {
		dlFetchPoster(entry)
	}
	return dest, nil
}
