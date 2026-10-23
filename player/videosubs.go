package main

// videosubs.go — subtitles for the embedded player.
//
// The player could not show subtitles at all. This loads a sidecar .srt/.vtt
// next to a local video (or extracts the first embedded track with ffmpeg),
// keeps the cues in memory, and draws the active cue over the video. The CC
// button on the control bar toggles them.

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// subtitleCue is one timed subtitle entry.
type subtitleCue struct {
	Start float64 // seconds
	End   float64 // seconds
	Text  string  // newline-separated lines
}

var videoSubs struct {
	mu      sync.Mutex
	cues    []subtitleCue
	enabled bool
	status  string // human-readable source, e.g. "movie.srt"
}

// parseSubtitleTimestamp parses "00:01:02,500" / "00:01:02.500" / "01:02.500".
func parseSubtitleTimestamp(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	s = strings.Replace(s, ",", ".", 1)
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0, false
		}
		total = total*60 + v
	}
	return total, true
}

// cleanSubtitleText strips SRT/VTT styling that the renderer cannot draw.
func cleanSubtitleText(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	// Drop HTML-ish tags (<i>, <font ...>) and ASS overrides ({\an8}).
	for {
		start := strings.Index(s, "<")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], ">")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+1:]
	}
	for {
		start := strings.Index(s, "{")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+1:]
	}
	return strings.TrimSpace(s)
}

// parseSubtitles parses SubRip and WebVTT data into timed cues. Unparseable
// blocks are skipped rather than aborting the whole file.
func parseSubtitles(data string) []subtitleCue {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	var cues []subtitleCue
	for _, block := range strings.Split(data, "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) == 0 {
			continue
		}
		// Find the timing line ("start --> end", optional settings after end).
		timingIdx := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timingIdx = i
				break
			}
		}
		if timingIdx < 0 {
			continue
		}
		halves := strings.SplitN(lines[timingIdx], "-->", 2)
		if len(halves) != 2 {
			continue
		}
		start, okStart := parseSubtitleTimestamp(halves[0])
		endField := strings.TrimSpace(halves[1])
		if f := strings.Fields(endField); len(f) > 0 {
			endField = f[0]
		}
		end, okEnd := parseSubtitleTimestamp(endField)
		if !okStart || !okEnd || end <= start {
			continue
		}
		var textLines []string
		for _, line := range lines[timingIdx+1:] {
			cleaned := cleanSubtitleText(line)
			if cleaned == "" {
				continue
			}
			textLines = append(textLines, cleaned)
		}
		if len(textLines) == 0 {
			continue
		}
		cues = append(cues, subtitleCue{Start: start, End: end, Text: strings.Join(textLines, "\n")})
	}
	return cues
}

// subtitleSidecarCandidates lists the files checked next to a local video.
func subtitleSidecarCandidates(videoPath string) []string {
	ext := filepath.Ext(videoPath)
	base := strings.TrimSuffix(videoPath, ext)
	var out []string
	for _, e := range []string{".srt", ".vtt", ".SRT", ".VTT"} {
		out = append(out, base+e)
	}
	// "<name>.<lang>.srt" (e.g. movie.en.srt).
	if matches, err := filepath.Glob(base + ".*.srt"); err == nil {
		out = append(out, matches...)
	}
	return out
}

// loadSidecarSubtitles reads the first usable subtitle file next to the video.
func loadSidecarSubtitles(videoPath string) ([]subtitleCue, string) {
	for _, path := range subtitleSidecarCandidates(videoPath) {
		if st, err := os.Stat(path); err == nil && st.Size() > 0 {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if cues := parseSubtitles(string(data)); len(cues) > 0 {
				return cues, filepath.Base(path)
			}
		}
	}
	return nil, ""
}

// extractEmbeddedSubtitles pulls the first subtitle track out of a local file
// with ffmpeg. Containers without a text track simply return none.
func extractEmbeddedSubtitles(ffmpegPath, videoPath string) ([]subtitleCue, string) {
	if ffmpegPath == "" {
		return nil, ""
	}
	if st, err := os.Stat(videoPath); err != nil || st.IsDir() {
		return nil, ""
	}
	cmd := exec.Command(ffmpegPath, "-v", "error", "-i", videoPath, "-map", "0:s:0", "-f", "srt", "-")
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil, ""
	}
	cues := parseSubtitles(string(out))
	if len(cues) == 0 {
		return nil, ""
	}
	return cues, "embedded"
}

// prepareVideoSubtitles loads subtitles for a new playback source. Local files
// use a sidecar first and fall back to an embedded track; network streams have
// neither, so subtitles stay off with an explanatory status.
func prepareVideoSubtitles(config *Config, source string) {
	if isNetworkSource(source) {
		// Cues for network sources come from fetchNetworkSubtitles, which may
		// already have finished; do not clear them here.
		return
	}
	videoSubs.mu.Lock()
	videoSubs.cues = nil
	videoSubs.status = ""
	videoSubs.mu.Unlock()
	cues, label := loadSidecarSubtitles(source)
	if len(cues) == 0 {
		ffmpegPath := ""
		if config != nil {
			ffmpegPath = getToolPath("ffmpeg", config)
		}
		cues, label = extractEmbeddedSubtitles(ffmpegPath, source)
	}
	videoSubs.mu.Lock()
	videoSubs.cues = cues
	videoSubs.status = label
	videoSubs.mu.Unlock()
}

// isNetworkSource reports whether a playback source is a remote URL.
func isNetworkSource(source string) bool {
	lower := strings.ToLower(source)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "rtsp://") || strings.HasPrefix(lower, "rtmp://")
}

// pickSubtitleFile returns the first subtitle file yt-dlp wrote into dir,
// preferring WebVTT over SRT. It returns "" when none was written.
func pickSubtitleFile(dir string) string {
	for _, ext := range []string{".vtt", ".srt"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*"+ext))
		if len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

// fetchNetworkSubtitles asks yt-dlp for the page's own subtitles in the
// background. It downloads only text tracks (no media), and sets cues only if
// the playback session is still current when the download finishes.
func fetchNetworkSubtitles(sess *playbackSession, config *Config, pageURL string) {
	if sess == nil || config == nil || !isNetworkSource(pageURL) {
		return
	}
	ytDlpPath := getToolPath("yt-dlp", config)
	if ytDlpPath == "" {
		return
	}
	dir, err := os.MkdirTemp("", "juka-subs-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(sess.ctx, 2*time.Minute)
	defer cancel()
	args := []string{
		"--skip-download",
		"--no-playlist",
		"--write-subs",
		"--sub-langs", "en.*,en",
		"--sub-format", "vtt/srt/best",
		"-o", filepath.Join(dir, "subs"),
		pageURL,
	}
	args = append(args, ytDlpExtraArgsSlice(config)...)
	cmd := exec.CommandContext(ctx, ytDlpPath, args...)
	cmd.Env = helperToolEnv(ytDlpPath)
	cmd.WaitDelay = 3 * time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[SUBS] yt-dlp subtitle fetch failed: %v (%s)", err, strings.TrimSpace(string(out)))
		return
	}

	path := pickSubtitleFile(dir)
	if path == "" || !sess.isCurrent() {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	cues := parseSubtitles(string(data))
	if len(cues) == 0 || !sess.isCurrent() {
		return
	}
	videoSubs.mu.Lock()
	videoSubs.cues = cues
	videoSubs.status = "yt-dlp"
	videoSubs.mu.Unlock()
}

// subtitlesAvailable reports whether the current source has cues.
func subtitlesAvailable() bool {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	return len(videoSubs.cues) > 0
}

// subtitlesEnabled reports the CC toggle state.
func subtitlesEnabled() bool {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	return videoSubs.enabled
}

// toggleSubtitles flips CC and reports the new state.
func toggleSubtitles() bool {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if len(videoSubs.cues) == 0 {
		return false
	}
	videoSubs.enabled = !videoSubs.enabled
	return videoSubs.enabled
}

// setSubtitlesEnabled forces the CC state (used when a source has no cues).
func setSubtitlesEnabled(on bool) {
	videoSubs.mu.Lock()
	videoSubs.enabled = on
	videoSubs.mu.Unlock()
}

// subtitleTextAt returns the cue text covering the given playback position.
func subtitleTextAt(seconds float64) string {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if !videoSubs.enabled || len(videoSubs.cues) == 0 {
		return ""
	}
	for _, cue := range videoSubs.cues {
		if seconds >= cue.Start && seconds <= cue.End {
			return cue.Text
		}
	}
	return ""
}

// renderVideoSubtitles draws the active cue centred above the bottom edge.
// bottomInset is the distance from the element's bottom to the text baseline.
func renderVideoSubtitles(renderer *sdl.Renderer, config *Config, x, y, w, h int32, seconds float64, bottomInset int32) {
	text := subtitleTextAt(seconds)
	if text == "" || renderer == nil {
		return
	}
	font, _ := getCachedFont(config, "medium")
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	if font == nil {
		return
	}
	lines := strings.Split(text, "\n")
	lineH := int32(font.Height()) + 4
	top := y + h - bottomInset - int32(len(lines))*lineH
	if top < y+8 {
		top = y + 8
	}
	for i, line := range lines {
		lineW, _, _ := font.SizeUTF8(line)
		lx := x + (w-int32(lineW))/2
		ly := top + int32(i)*lineH
		// Soft shadow behind the text so it stays readable over bright frames.
		fillRoundedRect(renderer, lx-8, ly-2, int32(lineW)+16, lineH, 6, sdl.Color{R: 0, G: 0, B: 0, A: 150})
		renderText(renderer, config, font, line, sdl.Color{R: 245, G: 245, B: 245, A: 255}, lx, ly)
	}
}

// subtitleStatusLine is the settings-panel description of the loaded track.
func subtitleStatusLine() string {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if len(videoSubs.cues) == 0 {
		return "No subtitles found for this video"
	}
	state := "Off"
	if videoSubs.enabled {
		state = "On"
	}
	return fmt.Sprintf("Subtitles: %s (%s, %d cues)", state, videoSubs.status, len(videoSubs.cues))
}
