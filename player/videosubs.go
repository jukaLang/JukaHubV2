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
	"html"
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
	mu       sync.Mutex
	cues     []subtitleCue
	enabled  bool
	status   string // human-readable source or download status
	owner    *playbackSession
	language string
	request  uint64
	cancel   context.CancelFunc
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
	return strings.TrimSpace(html.UnescapeString(s))
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
func prepareVideoSubtitles(sess *playbackSession, config *Config, source string) {
	if sess == nil || isNetworkSource(sess.pageURL) || isNetworkSource(source) {
		// Cues for network sources come from fetchNetworkSubtitles, which may
		// already have finished; do not clear them here.
		return
	}
	cues, label := loadSidecarSubtitles(source)
	if len(cues) == 0 {
		ffmpegPath := ""
		if config != nil {
			ffmpegPath = getToolPath("ffmpeg", config)
		}
		cues, label = extractEmbeddedSubtitles(ffmpegPath, source)
	}
	videoSubs.mu.Lock()
	if videoSubs.owner == sess {
		videoSubs.cues = cues
		videoSubs.status = label
	}
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

// subtitleLanguage defaults to English. Only literal language codes are accepted:
// yt-dlp interprets its argument as a regex, so do not let configuration inject
// an unintended selector.
func subtitleLanguage(config *Config) (string, error) {
	lang := ""
	if config != nil {
		lang = strings.TrimSpace(config.Variables.SubtitleLanguage)
	}
	if lang == "" {
		return "en", nil
	}
	for _, r := range lang {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return "", fmt.Errorf("invalid subtitle language code")
		}
	}
	return lang, nil
}

// resetVideoSubtitles transfers ownership before a new source starts.
func resetVideoSubtitles(sess *playbackSession) {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if videoSubs.cancel != nil {
		videoSubs.cancel()
		videoSubs.cancel = nil
	}
	videoSubs.request++
	videoSubs.owner = sess
	videoSubs.cues = nil
	videoSubs.status = ""
	videoSubs.language = ""
}

// fetchNetworkSubtitles downloads only the requested text track. Results from
// canceled requests or a previous playback session are never installed.
var subtitleDownload = downloadNetworkSubtitles

func fetchNetworkSubtitles(ctx context.Context, sess *playbackSession, request uint64, config *Config, lang string) {
	cues, status := subtitleDownload(ctx, config, sess.pageURL, lang)
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if videoSubs.owner != sess || videoSubs.request != request || !videoSubs.enabled || ctx.Err() != nil {
		return
	}
	videoSubs.cancel()
	videoSubs.cancel = nil
	videoSubs.cues = cues
	videoSubs.status = status
	videoSubs.language = lang
}

func downloadNetworkSubtitles(ctx context.Context, config *Config, pageURL, lang string) ([]subtitleCue, string) {
	dir, err := os.MkdirTemp("", "juka-subs-")
	if err != nil {
		return nil, "Could not create subtitle download directory"
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ytDlpPath := getToolPath("yt-dlp", config)
	args := subtitleDownloadArgs(config, dir, pageURL, lang)
	cmd := exec.CommandContext(ctx, ytDlpPath, args...)
	cmd.Env = helperToolEnv(ytDlpPath)
	cmd.WaitDelay = 3 * time.Second
	if _, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[SUBS] yt-dlp subtitle fetch failed: %v", err)
		return nil, "Subtitle download failed"
	}
	path := pickSubtitleFile(dir)
	if path == "" {
		return nil, "No subtitles found for " + lang
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "Could not read downloaded subtitles"
	}
	cues := parseSubtitles(string(data))
	if len(cues) == 0 {
		return nil, "Downloaded subtitles could not be read"
	}
	return cues, "yt-dlp " + lang
}

func subtitleDownloadArgs(config *Config, dir, pageURL, lang string) []string {
	args := []string{"--skip-download", "--no-playlist", "--write-subs", "--write-auto-subs",
		"--sub-langs", lang, "--sub-format", "vtt/srt", "-o", filepath.Join(dir, "subs")}
	args = append(args, ytDlpExtraArgsSlice(config)...)
	return append(args, "--", pageURL)
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
	return toggleSubtitlesForConfig(appConfig)
}

func toggleSubtitlesForConfig(config *Config) bool {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if videoSubs.enabled {
		videoSubs.enabled = false
		if videoSubs.cancel != nil {
			videoSubs.cancel()
			videoSubs.cancel = nil
			videoSubs.request++
			videoSubs.status = ""
		}
		return false
	}
	if len(videoSubs.cues) > 0 {
		if videoSubs.owner == nil || !isNetworkSource(videoSubs.owner.pageURL) {
			videoSubs.enabled = true
			return true
		}
		lang, err := subtitleLanguage(config)
		if err != nil {
			videoSubs.status = err.Error()
			return false
		}
		if lang == videoSubs.language {
			videoSubs.enabled = true
			return true
		}
		videoSubs.cues = nil
	}
	return startNetworkSubtitleDownloadLocked(config)
}

// startNetworkSubtitleDownloadLocked starts a request for the current owner.
func startNetworkSubtitleDownloadLocked(config *Config) bool {
	sess := videoSubs.owner
	if sess == nil || !isNetworkSource(sess.pageURL) || sess.ctx.Err() != nil {
		return false
	}
	lang, err := subtitleLanguage(config)
	if err != nil {
		videoSubs.status = err.Error()
		return false
	}
	videoSubs.enabled = true
	videoSubs.status = "Downloading subtitles: " + lang
	videoSubs.request++
	ctx, cancel := context.WithCancel(sess.ctx)
	videoSubs.cancel = cancel
	go fetchNetworkSubtitles(ctx, sess, videoSubs.request, config, lang)
	return true
}

func startEnabledNetworkSubtitles(config *Config) {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if videoSubs.enabled {
		startNetworkSubtitleDownloadLocked(config)
	}
}

// setSubtitlesEnabled forces the CC state (used when a source has no cues).
func setSubtitlesEnabled(on bool) {
	videoSubs.mu.Lock()
	videoSubs.enabled = on
	if !on && videoSubs.cancel != nil {
		videoSubs.cancel()
		videoSubs.cancel = nil
		videoSubs.request++
	}
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
		if seconds >= cue.Start && seconds < cue.End {
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
	if w <= 32 || h <= 0 {
		return
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		lines = append(lines, wrapTextLines(font, line, w-32, 20)...)
	}
	lineH := int32(font.Height()) + 4
	maxLines := int((h - 16) / lineH)
	if maxLines < 1 || len(lines) == 0 {
		return
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	top := y + h - bottomInset - int32(len(lines))*lineH
	if top < y+8 {
		top = y + 8
	}
	var maxWidth int32
	for _, line := range lines {
		lineW, _, _ := font.SizeUTF8(line)
		maxWidth = max32(maxWidth, int32(lineW))
	}
	fillRoundedRect(renderer, x+(w-maxWidth)/2-8, top-2, maxWidth+16, int32(len(lines))*lineH+4, 6, sdl.Color{R: 0, G: 0, B: 0, A: 170})
	for i, line := range lines {
		lineW, _, _ := font.SizeUTF8(line)
		lx := x + (w-int32(lineW))/2
		ly := top + int32(i)*lineH
		renderText(renderer, config, font, line, sdl.Color{R: 255, G: 255, B: 255, A: 255}, lx, ly)
	}
}

// subtitleStatusLine is the settings-panel description of the loaded track.
func subtitleStatusLine() string {
	videoSubs.mu.Lock()
	defer videoSubs.mu.Unlock()
	if len(videoSubs.cues) == 0 {
		if videoSubs.status != "" {
			return videoSubs.status
		}
		return "No subtitles loaded"
	}
	state := "Off"
	if videoSubs.enabled {
		state = "On"
	}
	return fmt.Sprintf("Subtitles: %s (%s, %d cues)", state, videoSubs.status, len(videoSubs.cues))
}
