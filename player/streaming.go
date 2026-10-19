package main

// streaming.go — start playback without downloading the whole file first.
//
// Videos used to be fully downloaded with yt-dlp before ffmpeg saw a single
// frame, which made waiting for a five-minute clip slow on the handheld's WiFi.
// ffmpeg can read HTTP(S) sources directly, so this resolves the page URL to a
// direct stream URL with `yt-dlp -g` (no media transfer) and hands that to the
// decoder. Resolutions are cached briefly because the signed media URLs expire
// after a few hours, and a background prefetch warms the cache for the first
// search result so pressing Play is instant.

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// streamResolutionTTL bounds how long a resolved direct URL is reused. The URLs
// are signed by the CDN and stop working after a few hours; a short window is
// plenty to cover prefetch -> play -> seek.
const streamResolutionTTL = 20 * time.Minute

type streamResolution struct {
	direct string
	at     time.Time
}

var (
	resolvedStreamsMu sync.Mutex
	resolvedStreams   = map[string]streamResolution{}
)

// isDirectStreamURL reports whether ffmpeg can open a URL without yt-dlp.
func isDirectStreamURL(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return false
	}
	for _, scheme := range []string{"rtsp://", "rtmp://", "rtp://", "udp://", "mms://"} {
		if strings.HasPrefix(s, scheme) {
			return true
		}
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return false
	}
	// Strip query/fragment before looking at the extension: HLS manifests and
	// progressive files often carry signed query strings.
	cut := strings.IndexAny(s, "?#")
	path := s
	if cut >= 0 {
		path = s[:cut]
	}
	for _, ext := range []string{".m3u8", ".mp4", ".m4v", ".webm", ".mkv", ".mov", ".ts", ".m4a", ".mp3", ".aac", ".ogg"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// streamResolutionKey normalizes a source URL for the cache.
func streamResolutionKey(url string) string {
	return strings.TrimSpace(url)
}

// cachedDirectStream returns a cached direct URL for a source, if still valid.
func cachedDirectStream(url string) (string, bool) {
	key := streamResolutionKey(url)
	if key == "" {
		return "", false
	}
	resolvedStreamsMu.Lock()
	defer resolvedStreamsMu.Unlock()
	entry, ok := resolvedStreams[key]
	if !ok || entry.direct == "" {
		return "", false
	}
	if time.Since(entry.at) > streamResolutionTTL {
		delete(resolvedStreams, key)
		return "", false
	}
	return entry.direct, true
}

// rememberDirectStream caches a resolved URL.
func rememberDirectStream(url, direct string) {
	key := streamResolutionKey(url)
	if key == "" || direct == "" {
		return
	}
	resolvedStreamsMu.Lock()
	resolvedStreams[key] = streamResolution{direct: direct, at: time.Now()}
	resolvedStreamsMu.Unlock()
}

// forgetDirectStream drops a cached URL (e.g. after the CDN rejected it).
func forgetDirectStream(url string) {
	resolvedStreamsMu.Lock()
	delete(resolvedStreams, streamResolutionKey(url))
	resolvedStreamsMu.Unlock()
}

// resetStreamCache clears every cached resolution. Used by tests.
func resetStreamCache() {
	resolvedStreamsMu.Lock()
	resolvedStreams = map[string]streamResolution{}
	resolvedStreamsMu.Unlock()
}

// streamFormatSelector is the yt-dlp selector used for streaming. It prefers a
// single muxed file: two separate URLs (video + audio) would need a second
// ffmpeg input, so those fall back to the download path instead.
func streamFormatSelector(maxHeight int) string {
	if maxHeight <= 0 {
		maxHeight = 720
	}
	return fmt.Sprintf("best[height<=%d][ext=mp4]/best[height<=%d][vcodec!=none][acodec!=none]/best[height<=%d]",
		maxHeight, maxHeight, maxHeight)
}

// playbackMaxHeight reads the playback resolution ceiling from the config.
func playbackMaxHeight(config *Config) int {
	if config == nil {
		return 720
	}
	res := strings.TrimSpace(config.Variables.PlaybackResolution)
	if res == "" || res == "best" {
		return 720
	}
	var h int
	if _, err := fmt.Sscanf(res, "%d", &h); err == nil && h > 0 {
		return h
	}
	return 720
}

// resolveDirectStream asks yt-dlp for a direct media URL. It returns false when
// yt-dlp yields zero or several URLs (the caller then downloads instead).
func resolveDirectStream(ctx context.Context, ytDlpPath, url string, maxHeight int) (string, bool) {
	if ytDlpPath == "" || url == "" {
		return "", false
	}
	args := []string{
		"-g",
		"--no-warnings",
		"--no-playlist",
		"--no-check-certificate",
		"--geo-bypass",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"--extractor-args", "youtube:player_client=android,web;youtube:player_skip=webpage",
		"-f", streamFormatSelector(maxHeight),
		url,
	}
	cmd := exec.CommandContext(ctx, ytDlpPath, args...)
	cmd.Env = helperToolEnv(ytDlpPath)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[STREAM] yt-dlp -g failed for %s: %v", sanitizeLogURL(url), err)
		return "", false
	}
	var urls []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			urls = append(urls, line)
		}
	}
	if len(urls) != 1 {
		log.Printf("[STREAM] yt-dlp -g returned %d URLs for %s; downloading instead",
			len(urls), sanitizeLogURL(url))
		return "", false
	}
	return urls[0], true
}

// streamableSource returns a URL ffmpeg can read directly, using the cache when
// possible. Direct stream links (RTSP, HLS, progressive files) are used as-is.
func streamableSource(config *Config, ytDlpPath, source string) (string, bool) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", false
	}
	if direct, ok := cachedDirectStream(source); ok {
		return direct, true
	}
	if isDirectStreamURL(source) {
		rememberDirectStream(source, source)
		return source, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	direct, ok := resolveDirectStream(ctx, ytDlpPath, source, playbackMaxHeight(config))
	if !ok {
		return "", false
	}
	rememberDirectStream(source, direct)
	return direct, true
}

// probePlayableStream checks that ffprobe can read the stream and reports video
// dimensions. It is deliberately short: a stream that cannot describe itself in
// a few seconds is not worth waiting for, and downloading is the safe fallback.
func probePlayableStream(ffprobePath, url string) bool {
	if ffprobePath == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobePath, "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "default=noprint_wrappers=1",
		url)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[STREAM] ffprobe rejected %s: %v", sanitizeLogURL(url), err)
		return false
	}
	text := string(out)
	return strings.Contains(text, "width=") && strings.Contains(text, "height=")
}

// tryStreamPlayback starts the embedded player on a direct stream. It reports
// whether playback was started; a false result means the caller should fall back
// to downloading the file with yt-dlp.
func tryStreamPlayback(config *Config, ytDlpPath, url string, startSec float64) bool {
	// A file on the SD card never needs a download round-trip.
	if st, err := os.Stat(url); err == nil && !st.IsDir() {
		startEmbeddedPlayback(config, url, startSec, false)
		return true
	}
	direct, ok := streamableSource(config, ytDlpPath, url)
	if !ok {
		return false
	}
	ffprobePath := ""
	if config != nil {
		ffprobePath = getToolPath("ffprobe", config)
	}
	if !probePlayableStream(ffprobePath, direct) {
		forgetDirectStream(url)
		return false
	}
	log.Printf("[STREAM] playing %s directly (no download)", sanitizeLogURL(url))
	startEmbeddedPlayback(config, direct, startSec, false)
	return true
}

// prefetchFirstResult warms the direct-stream cache for the first search result,
// which is the card the focus ring starts on and therefore the most likely Play.
func prefetchFirstResult(config *Config, videos []VideoInfo) {
	if len(videos) == 0 {
		return
	}
	url := videos[0].GetURL()
	if url == "" {
		return
	}
	go prefetchDirectStream(config, url)
}

// prefetchDirectStream resolves (but does not play) a source in the background
// so a later Play press skips the resolution round-trip.
func prefetchDirectStream(config *Config, url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	if _, ok := cachedDirectStream(url); ok {
		return
	}
	if isDirectStreamURL(url) {
		rememberDirectStream(url, url)
		return
	}
	ytDlpPath := ""
	if config != nil {
		ytDlpPath = getToolPath("yt-dlp", config)
	}
	if ytDlpPath == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if direct, ok := resolveDirectStream(ctx, ytDlpPath, url, playbackMaxHeight(config)); ok {
		rememberDirectStream(url, direct)
	}
}
