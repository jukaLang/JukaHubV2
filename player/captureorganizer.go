package main

// captureorganizer.go — capture discovery and safe edits for the Tools page.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	captureMaxScreenshots = 240
	captureMaxRecordings  = 120
)

type capturePicture struct {
	path  string
	name  string
	group string
	at    time.Time
}

type captureRecording struct {
	path string
	name string
	at   time.Time
	size int64
	// duration is filled by the organizer's background ffprobe after the scan;
	// 0 means it has not been read (or ffprobe is unavailable).
	duration float64
}

type captureCollection struct {
	pictures   []capturePicture
	recordings []captureRecording
}

// capturePictureDirs reads JukaHub's screenshots, the card's Screenshot folder,
// and the older desktop folder. A configured screenshot directory is included.
func capturePictureDirs() []string {
	dirs := []string{screenshotDir(), screenshotAppDir()}
	if IsTrimuiDevice() {
		dirs = append(dirs, "/mnt/SDCARD/Screenshots")
	} else {
		dirs = append(dirs, screenshotPicturesDir())
	}
	return dirs
}

func captureRecordingDirs(config *Config) []string {
	return []string{recordingsDirFor(config), screenRecCrossMixOutput()}
}

func captureUniqueDirs(dirs []string) []string {
	seen := make(map[string]bool, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		clean := filepath.Clean(dir)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

// captureTimeLayout is the timestamp every capture name ends with.
const captureTimeLayout = "2006-01-02_15-04-05"

// captureStampSuffix trims the "-2" screenshotPath adds when two captures
// land in the same second. The timestamp's own dashes mean a trailing number
// is only a counter when what remains is a whole timestamp.
func captureStampSuffix(stamp string) string {
	if at := strings.LastIndexByte(stamp, '-'); at > 0 {
		if n := stamp[at+1:]; n != "" && strings.Trim(n, "0123456789") == "" {
			if _, err := time.Parse(captureTimeLayout, stamp[:at]); err == nil {
				return stamp[:at]
			}
		}
	}
	return stamp
}

// captureStampTitle finds <title><sep><timestamp> by trying every separator:
// the timestamp has an underscore of its own, so the last separator is often
// not the one before it, and a title may contain the separator too.
func captureStampTitle(stem, sep string) (string, bool) {
	for at := strings.Index(stem, sep); at > 0; {
		title := strings.TrimSpace(stem[:at])
		stamp := captureStampSuffix(stem[at+len(sep):])
		if title != "" {
			if _, err := time.Parse(captureTimeLayout, stamp); err == nil {
				return title, true
			}
		}
		next := strings.Index(stem[at+len(sep):], sep)
		if next < 0 {
			break
		}
		at += len(sep) + next
	}
	return "", false
}

// capturePictureGroup uses the title in new JukaHub game screenshots. Older
// screenshots did not record a game name and stay in a clearly labelled group.
// A one-level game folder and RetroArch's title + timestamp names also work.
func capturePictureGroup(name, folder string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if title, ok := strings.CutPrefix(stem, "game-"); ok {
		if title, ok := captureStampTitle(title, "_"); ok {
			return title
		}
	}
	if folder != "" {
		return folder
	}
	if strings.HasPrefix(stem, "game_") {
		return "Older game screenshots"
	}
	if strings.HasPrefix(stem, "screenshot_") {
		return "JukaHub screens"
	}
	for _, sep := range []string{" - ", "_", "-"} {
		if title, ok := captureStampTitle(stem, sep); ok {
			return title
		}
	}
	return "Other screenshots"
}

func captureIsPicture(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".bmp", ".webp":
		return true
	}
	return false
}

// captureScanPictures follows at most one real directory below each capture
// root. Symlinks are skipped so a gallery scan cannot wander around the card.
func captureScanPictures(dirs []string) []capturePicture {
	var found []capturePicture
	seen := make(map[string]bool)
	var scan func(string, string)
	scan = func(dir, folder string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if folder == "" {
					scan(path, entry.Name())
				}
				continue
			}
			if !captureIsPicture(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				continue
			}
			found = append(found, capturePicture{path: path, name: entry.Name(),
				group: capturePictureGroup(entry.Name(), folder), at: info.ModTime()})
		}
	}
	for _, dir := range captureUniqueDirs(dirs) {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		scan(dir, "")
	}
	sort.SliceStable(found, func(i, j int) bool {
		if !found[i].at.Equal(found[j].at) {
			return found[i].at.After(found[j].at)
		}
		return strings.ToLower(found[i].path) < strings.ToLower(found[j].path)
	})
	if len(found) > captureMaxScreenshots {
		found = found[:captureMaxScreenshots]
	}
	return found
}

func captureScanRecordings(dirs []string) []captureRecording {
	entries := scanRecordingDirs(dirs...)
	if len(entries) > captureMaxRecordings {
		entries = entries[:captureMaxRecordings]
	}
	found := make([]captureRecording, 0, len(entries))
	for _, entry := range entries {
		info, err := os.Lstat(entry.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		found = append(found, captureRecording{path: entry.Path, name: entry.Name,
			at: info.ModTime(), size: info.Size()})
	}
	return found
}

func captureScan(config *Config) captureCollection {
	return captureCollection{
		pictures:   captureScanPictures(capturePictureDirs()),
		recordings: captureScanRecordings(captureRecordingDirs(config)),
	}
}

func captureRecordingAllowed(path string, dirs []string) (os.FileInfo, error) {
	path = filepath.Clean(path)
	allowed := false
	for _, dir := range captureUniqueDirs(dirs) {
		if filepath.Dir(path) == dir {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, errors.New("this recording is outside the capture folders")
	}
	if path == screenRecCurrentPath() {
		return nil, errors.New("stop recording before editing this clip")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !isRecordingFile(path) || info.Size() == 0 {
		return nil, errors.New("this is not a finished recording")
	}
	return info, nil
}

func captureFreePath(dir, stem, ext string) (string, error) {
	for i := 1; i <= 999; i++ {
		name := stem + ext
		if i > 1 {
			name = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("too many recordings with the same name")
}

// captureRecordingMeta is the recording list's second line: its length when it
// has been read, then its size. Pure.
func captureRecordingMeta(clip captureRecording) string {
	meta := formatSize(clip.size)
	if clip.duration > 0 {
		meta = captureDisplayTime(clip.duration) + " · " + meta
	}
	return meta
}

// captureDeleteRecording removes one finished recording, after the same
// containment check as rename and trim: only a regular recording directly in
// one of JukaHub's capture folders, never the clip being recorded now.
func captureDeleteRecording(path string, dirs []string) error {
	if _, err := captureRecordingAllowed(path, dirs); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	syncDirectory(filepath.Dir(path))
	return nil
}

// captureDeleteWindow is how long the first Delete press stays armed. Long
// enough to be deliberate, short enough that a press remembered from minutes
// ago cannot remove a different intention's file.
const captureDeleteWindow = 6 * time.Second

// captureDeleteConfirm is the recordings list's two-press delete: the first
// press arms this clip, the second within the window removes it. Main thread.
type captureDeleteConfirm struct {
	path string
	at   time.Time
}

// armed records the first press and reports whether this is the second one
// for the same clip inside the window.
func (c *captureDeleteConfirm) armed(path string, now time.Time) bool {
	if c.path == path && !c.at.IsZero() && now.Sub(c.at) >= 0 && now.Sub(c.at) <= captureDeleteWindow {
		*c = captureDeleteConfirm{}
		return true
	}
	*c = captureDeleteConfirm{path: path, at: now}
	return false
}

// captureRenameRecording leaves the extension alone and refuses to replace an
// existing file. The recording is only moved within its original folder.
func captureRenameRecording(path, title string, dirs []string) (string, error) {
	if _, err := captureRecordingAllowed(path, dirs); err != nil {
		return "", err
	}
	title = screenRecTitleClean(title)
	if title == "" || title == "." || title == ".." {
		return "", errors.New("enter a recording name")
	}
	ext := filepath.Ext(path)
	if strings.EqualFold(title+ext, filepath.Base(path)) {
		return path, nil
	}
	to, err := captureFreePath(filepath.Dir(path), title, ext)
	if err != nil {
		return "", err
	}
	if err := os.Rename(path, to); err != nil {
		return "", err
	}
	return to, nil
}

// captureParseTime accepts seconds or mm:ss / hh:mm:ss for the two trim
// prompts. It rejects non-finite and very large values before ffmpeg sees them.
func captureParseTime(s string) (float64, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0, errors.New("use seconds or mm:ss")
	}
	secs := 0.0
	for i, part := range parts {
		if part == "" {
			return 0, errors.New("use seconds or mm:ss")
		}
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, errors.New("enter a valid time")
		}
		if i < len(parts)-1 && (value != math.Trunc(value) || value >= 60 && i > 0) {
			return 0, errors.New("use whole hours and minutes")
		}
		if len(parts) > 1 && i == len(parts)-1 && value >= 60 {
			return 0, errors.New("seconds must be less than 60")
		}
		secs = secs*60 + value
	}
	if secs > 24*60*60 {
		return 0, errors.New("time must be under 24 hours")
	}
	return secs, nil
}

func captureTimeArg(sec float64) string {
	return strconv.FormatFloat(sec, 'f', 3, 64)
}

// captureProbeDuration is optional: a clip can still be trimmed by entering
// times when ffprobe is unavailable.
func captureProbeDuration(ctx context.Context, probe, path string) float64 {
	if !toolPathRunnable(probe) {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := hiddenCommandContext(ctx, probe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil || len(out) > 128 {
		return 0
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0
	}
	return seconds
}

// captureTrimRun runs one trim into out. copyStreams keeps the source's codecs
// (no quality loss, and fast on a handheld); otherwise the clip is re-encoded,
// which gives an accurate start even for a fragmented recording.
func captureTrimRun(ctx context.Context, ffmpeg, path, out string, start, end float64, copyStreams bool) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-ss", captureTimeArg(start),
		"-i", path, "-t", captureTimeArg(end - start), "-map", "0:v:0", "-map", "0:a?"}
	if copyStreams {
		args = append(args, "-c", "copy", "-avoid_negative_ts", "make_zero")
	} else {
		args = append(args, "-c:v", "mpeg4", "-q:v", "5", "-threads", "1", "-c:a", "aac", "-b:a", "128k")
	}
	args = append(args, "-movflags", "+faststart", "-y", out)
	cmd := hiddenCommandContext(ctx, ffmpeg, args...)
	var stderr castTail
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := firstLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// captureTrimHasVideo checks that a stream-copy result really holds a video
// stream. A source whose codec the MP4 container cannot carry can make ffmpeg
// exit successfully with only audio, which is not a usable clip.
func captureTrimHasVideo(ctx context.Context, probe, path string) bool {
	if !toolPathRunnable(probe) {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := hiddenCommandContext(ctx, probe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// captureTrimRecording makes a new clip beside the source. It never edits or
// removes the original; any failed partial output is removed. A stream copy is
// tried first and kept when it produced a real video with a decodable duration;
// otherwise the clip is re-encoded, which also covers fragmented handheld
// recordings.
func captureTrimRecording(ctx context.Context, ffmpeg, path string, start, end float64, dirs []string) (string, error) {
	if !toolPathRunnable(ffmpeg) {
		return "", errors.New("FFmpeg is not installed; install it from Toolbox")
	}
	info, err := captureRecordingAllowed(path, dirs)
	if err != nil {
		return "", err
	}
	if start < 0 || end <= start+0.5 || math.IsNaN(start) || math.IsNaN(end) || math.IsInf(start, 0) || math.IsInf(end, 0) {
		return "", errors.New("end time must be at least one second after the start")
	}
	if free, err := freeBytes(filepath.Dir(path)); err == nil && free < info.Size()+50<<20 {
		return "", fmt.Errorf("need about %s free to keep the original while trimming", formatSize(info.Size()+50<<20))
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".jukahub-trim-*.mp4")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	_ = temp.Close()
	defer os.Remove(tempPath)
	probe := getToolPath("ffprobe", nil)
	length := end - start
	// Stream copy first: no re-encode means the source quality is untouched.
	// It is only kept when the result has a video stream and roughly the
	// requested length, because a codec the MP4 container cannot carry can
	// otherwise leave an audio-only file that ffmpeg still exits 0 on.
	copyErr := captureTrimRun(ctx, ffmpeg, path, tempPath, start, end, true)
	copied := copyErr == nil
	if copied {
		out, statErr := os.Stat(tempPath)
		copied = statErr == nil && out.Size() >= screenRecMinBytes && captureTrimHasVideo(ctx, probe, tempPath)
		if copied && length >= 1 {
			got := captureProbeDuration(ctx, probe, tempPath)
			copied = got <= 0 || (got >= length*0.5 && got <= length*2+2)
		}
	}
	if !copied {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		_ = os.Remove(tempPath)
		if copyErr != nil {
			logDebug("captures", "stream copy could not trim %s (%v); re-encoding", filepath.Base(path), copyErr)
		}
		if err := captureTrimRun(ctx, ffmpeg, path, tempPath, start, end, false); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("could not trim the recording: %s", err)
		}
	}
	if out, err := os.Stat(tempPath); err != nil || out.Size() < screenRecMinBytes {
		return "", errors.New("the chosen time range produced no video")
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + " (trimmed)"
	to, err := captureFreePath(dir, stem, ".mp4")
	if err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, to); err != nil {
		return "", err
	}
	return to, nil
}
