package main

// captureorganizer.go — capture discovery and safe edits for the Tools page.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
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

// capturePictureGroup uses the title in new JukaHub game screenshots. Older
// screenshots did not record a game name and stay in a clearly labelled group.
// A one-level game folder and RetroArch's title + timestamp names also work.
func capturePictureGroup(name, folder string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if strings.HasPrefix(stem, "game-") {
		if at := strings.LastIndex(stem, "_"); at > len("game-") {
			if _, err := time.Parse("2006-01-02_15-04-05", stem[at+1:]); err == nil {
				if title := strings.TrimSpace(stem[len("game-"):at]); title != "" {
					return title
				}
			}
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
		if at := strings.LastIndex(stem, sep); at > 0 {
			if _, err := time.Parse("2006-01-02_15-04-05", stem[at+len(sep):]); err == nil {
				return strings.TrimSpace(stem[:at])
			}
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
	out, err := exec.CommandContext(ctx, probe, "-v", "error", "-show_entries", "format=duration",
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

// captureTrimRecording makes a new clip beside the source. It never edits or
// removes the original; any failed partial output is removed. Re-encoding
// gives an accurate start time, including for fragmented handheld recordings.
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
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-ss", captureTimeArg(start),
		"-i", path, "-t", captureTimeArg(end - start), "-map", "0:v:0", "-map", "0:a?",
		"-c:v", "mpeg4", "-q:v", "5", "-threads", "1", "-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart", "-y", tempPath}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr castTail
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := firstLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("could not trim the recording: %s", msg)
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
