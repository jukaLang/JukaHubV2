package main

// streaming.go — start playback without downloading the whole file first.
//
// Videos used to be fully downloaded with yt-dlp before ffmpeg saw a single
// frame, which made waiting for a five-minute clip slow on the handheld's WiFi.
// ffmpeg can read HTTP(S) sources directly, so this resolves the page URL to a
// direct stream URL with `yt-dlp -g` (no media transfer) and hands that to the
// decoder. Resolutions are cached briefly because the signed media URLs expire
// after a few hours, and a background prefetch warms the cache for the
// highlighted card so pressing Play is instant.
//
// YouTube serves a different format list to each extractor client, and a client
// that works for one video can return nothing for the next (the standalone
// yt-dlp on the device has no JavaScript runtime, so it only sees a subset of
// formats). A single attempt therefore failed often enough that playback fell
// back to downloading the whole file; the attempts below are tried in order
// until one yields a muxed stream.

import (
	"bytes"
	"context"
	"errors"
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

// streamAttemptTimeout bounds a single extraction attempt. The helper is a
// frozen Python build, so even a successful run takes several seconds on the
// handheld; one hung attempt must not eat the whole playback budget.
const streamAttemptTimeout = 15 * time.Second

// streamResolveBudget is the total time spent resolving a source before the
// caller falls back to downloading.
const streamResolveBudget = 40 * time.Second

// streamFailureTTL is how long a failed prefetch is remembered. Only prefetch
// honours it: pressing Play always tries again, but scrolling a grid of
// unplayable cards must not spawn an extraction per card.
const streamFailureTTL = 3 * time.Minute

// streamUserAgent makes the extractor look like a desktop browser, which is
// what the CDN's signed media URLs are issued for.
const streamUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

type streamResolution struct {
	direct string
	at     time.Time
}

var (
	resolvedStreamsMu sync.Mutex
	resolvedStreams   = map[string]streamResolution{}

	streamFailuresMu sync.Mutex
	streamFailures   = map[string]time.Time{}
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
	// A resolution means the video is playable again; drop any stale failure.
	streamFailuresMu.Lock()
	delete(streamFailures, key)
	streamFailuresMu.Unlock()
}

// forgetDirectStream drops a cached URL (e.g. after the CDN rejected it).
func forgetDirectStream(url string) {
	resolvedStreamsMu.Lock()
	delete(resolvedStreams, streamResolutionKey(url))
	resolvedStreamsMu.Unlock()
}

// rememberStreamFailure notes that a source could not be resolved, so prefetch
// leaves it alone for a while.
func rememberStreamFailure(url string) {
	key := streamResolutionKey(url)
	if key == "" {
		return
	}
	streamFailuresMu.Lock()
	streamFailures[key] = time.Now()
	streamFailuresMu.Unlock()
}

// streamRecentlyFailed reports whether a prefetch of this source failed within
// streamFailureTTL.
func streamRecentlyFailed(url string) bool {
	return streamRecentlyFailedAt(url, time.Now())
}

func streamRecentlyFailedAt(url string, now time.Time) bool {
	key := streamResolutionKey(url)
	if key == "" {
		return false
	}
	streamFailuresMu.Lock()
	defer streamFailuresMu.Unlock()
	at, ok := streamFailures[key]
	if !ok {
		return false
	}
	if now.Sub(at) > streamFailureTTL {
		delete(streamFailures, key)
		return false
	}
	return true
}

// resetStreamCache clears every cached resolution. Used by tests.
func resetStreamCache() {
	resolvedStreamsMu.Lock()
	resolvedStreams = map[string]streamResolution{}
	resolvedStreamsMu.Unlock()
	streamFailuresMu.Lock()
	streamFailures = map[string]time.Time{}
	streamFailuresMu.Unlock()
	resetProbeCache()
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

// streamAttempt is one extraction strategy. The order matters: the first entry
// is the one that has worked in practice, the rest cover the videos and yt-dlp
// builds where it does not, and the last two ask for format 18 (the 360p muxed
// MP4 every client still serves) because a low-resolution stream that starts
// immediately beats a perfect one that never arrives.
type streamAttempt struct {
	name string
	args []string
}

func streamAttempts(maxHeight int) []streamAttempt {
	muxed := streamFormatSelector(maxHeight)
	anyMuxed := "18/" + muxed
	return []streamAttempt{
		{
			name: "android+web",
			args: []string{
				"--extractor-args", "youtube:player_client=android,web;youtube:player_skip=webpage",
				"-f", muxed,
			},
		},
		{
			// No extractor overrides: yt-dlp's own default client list is the
			// best maintained, and this attempt also fetches the webpage (the
			// previous one skips it), which covers the "client list returned
			// nothing" case and bot-check pages.
			name: "default clients",
			args: []string{
				"-f", muxed,
			},
		},
		{
			name: "android 360p",
			args: []string{
				"--extractor-args", "youtube:player_client=android",
				"-f", anyMuxed,
			},
		},
		{
			name: "web 360p",
			args: []string{
				"--extractor-args", "youtube:player_client=web",
				"-f", anyMuxed,
			},
		},
	}
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

// streamFailureReason picks the useful line out of yt-dlp's (noisy) stderr so a
// failed attempt explains itself in the log instead of only "exit status 1".
func streamFailureReason(stderr string) string {
	reason := ""
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		reason = line
		if strings.Contains(line, "ERROR:") {
			if i := strings.Index(line, "ERROR:"); i >= 0 {
				reason = strings.TrimSpace(line[i:])
			}
			break
		}
	}
	if len(reason) > 200 {
		reason = reason[:200] + "…"
	}
	return reason
}

// streamFailureFatal reports whether another extraction attempt is pointless
// because YouTube itself said the video cannot be played (as opposed to this
// client not offering a matching format).
func streamFailureFatal(reason string) bool {
	lower := strings.ToLower(reason)
	for _, marker := range []string{
		"video unavailable",
		"private video",
		"has been removed",
		"members-only",
		"age-restricted",
		"not a valid url",
		"unsupported url",
		"does not exist",
		"copyright",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ytDlpGetURL runs one `yt-dlp -g` attempt and returns the single media URL.
// Stderr is captured rather than inherited: yt-dlp's errors are the only way to
// tell "no such format" from "network down", and the old code logged neither.
func ytDlpGetURL(ctx context.Context, ytDlpPath string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, ytDlpPath, args...)
	cmd.Env = helperToolEnv(ytDlpPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil {
		return "", fmt.Errorf("%w (%s)", runErr, streamFailureReason(stderr.String()))
	}
	var urls []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			urls = append(urls, line)
		}
	}
	if len(urls) != 1 {
		return "", fmt.Errorf("got %d media URLs, want one muxed stream (%s)",
			len(urls), streamFailureReason(stderr.String()))
	}
	return urls[0], nil
}

// ytDlpURLRunner runs one extraction and returns the media URL. It is a
// parameter so the attempt order can be tested without subprocesses.
type ytDlpURLRunner func(ctx context.Context, args []string) (string, error)

// resolveDirectStream asks yt-dlp for a direct media URL, trying the client
// strategies in order until one yields a single muxed stream.
func resolveDirectStream(ctx context.Context, config *Config, ytDlpPath, source string) (string, bool) {
	if ytDlpPath == "" || source == "" {
		return "", false
	}
	direct, err := resolveWithRunner(ctx, source, playbackMaxHeight(config), ytDlpExtraArgsSlice(config),
		func(ctx context.Context, args []string) (string, error) {
			return ytDlpGetURL(ctx, ytDlpPath, args)
		})
	if err != nil {
		log.Printf("[STREAM] could not stream %s: %v", sanitizeLogURL(source), err)
		return "", false
	}
	return direct, true
}

// resolveWithRunner tries each strategy in turn. It stops early when YouTube
// says the video is unplayable, or when the caller's context expires.
func resolveWithRunner(ctx context.Context, source string, maxHeight int, extraArgs []string, run ytDlpURLRunner) (string, error) {
	base := []string{
		"-g",
		"--no-warnings",
		"--no-playlist",
		"--no-check-certificate",
		"--geo-bypass",
		"--user-agent", streamUserAgent,
	}
	var lastErr error
	for _, attempt := range streamAttempts(maxHeight) {
		if ctx.Err() != nil {
			break
		}
		args := make([]string, 0, len(base)+len(attempt.args)+len(extraArgs)+1)
		args = append(args, base...)
		args = append(args, attempt.args...)
		// Config-provided args come last so a user's cookies or API key win
		// over the defaults above.
		args = append(args, extraArgs...)
		args = append(args, source)

		attemptCtx, cancel := context.WithTimeout(ctx, streamAttemptTimeout)
		direct, err := run(attemptCtx, args)
		cancel()
		if err == nil {
			log.Printf("[STREAM] resolved %s with %s", sanitizeLogURL(source), attempt.name)
			return direct, nil
		}
		lastErr = fmt.Errorf("%s: %w", attempt.name, err)
		log.Printf("[STREAM] %s failed for %s: %v", attempt.name, sanitizeLogURL(source), err)
		if streamFailureFatal(err.Error()) {
			return "", lastErr
		}
	}
	if lastErr == nil {
		lastErr = errors.New("extraction cancelled before it could run")
	}
	return "", lastErr
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
	ctx, cancel := context.WithTimeout(context.Background(), streamResolveBudget)
	defer cancel()
	direct, ok := resolveDirectStream(ctx, config, ytDlpPath, source)
	if !ok {
		return "", false
	}
	rememberDirectStream(source, direct)
	return direct, true
}

// probePlayableStream checks that ffprobe can read the stream and reports video
// dimensions. It is deliberately short: a stream that cannot describe itself in
// a few seconds is not worth waiting for, and downloading is the safe fallback.
// The probe result is cached, so the player's own probe of the same URL costs
// nothing instead of a second network round-trip.
func probePlayableStream(ffprobePath, url string) bool {
	if ffprobePath == "" {
		return false
	}
	if snap, ok := cachedProbe(url); ok {
		return snap.width > 0 && snap.height > 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fields, err := ffprobeStream(ctx, ffprobePath, url)
	if err != nil {
		log.Printf("[STREAM] ffprobe rejected %s: %v", sanitizeLogURL(url), err)
		return false
	}
	if fields.width <= 0 || fields.height <= 0 {
		log.Printf("[STREAM] ffprobe found no video stream in %s", sanitizeLogURL(url))
		return false
	}
	rememberProbe(url, fields)
	return true
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
	_, wasCached := cachedDirectStream(url)
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
		// A cached URL can expire while it sits in the cache: resolve once
		// more for a fresh one before giving up on streaming.
		if !wasCached {
			return false
		}
		direct, ok = streamableSource(config, ytDlpPath, url)
		if !ok || !probePlayableStream(ffprobePath, direct) {
			return false
		}
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
	prefetchDirectStream(config, url)
}

// prefetchFocusedResult warms the stream URL for the highlighted card, so the
// next Play press does not wait for an extraction.
func prefetchFocusedResult(config *Config, videos []VideoInfo) {
	if focusedResultIndex < 0 || focusedResultIndex >= len(videos) {
		return
	}
	if url := videos[focusedResultIndex].GetURL(); url != "" {
		prefetchDirectStream(config, url)
	}
}

// Prefetch runs through a single worker. Navigation can request a new target
// several times a second, and the handheld has four cores; only the newest
// target is worth resolving, so requests replace each other while one runs.
var (
	prefetchMu      sync.Mutex
	prefetchWanted  string
	prefetchConfig  *Config
	prefetchRunning bool
)

// prefetchDirectStream resolves (but does not play) a source in the background.
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
	if streamRecentlyFailed(url) {
		return
	}
	prefetchMu.Lock()
	prefetchWanted = url
	prefetchConfig = config
	start := !prefetchRunning
	prefetchRunning = true
	prefetchMu.Unlock()
	if start {
		go prefetchWorker()
	}
}

func prefetchWorker() {
	for {
		prefetchMu.Lock()
		url := prefetchWanted
		config := prefetchConfig
		prefetchWanted = ""
		prefetchMu.Unlock()
		if url == "" {
			prefetchMu.Lock()
			// Another request may have arrived between the read and here.
			if prefetchWanted == "" {
				prefetchRunning = false
				prefetchMu.Unlock()
				return
			}
			prefetchMu.Unlock()
			continue
		}
		ytDlpPath := ""
		if config != nil {
			ytDlpPath = getToolPath("yt-dlp", config)
		}
		if ytDlpPath == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), streamResolveBudget)
		direct, ok := resolveDirectStream(ctx, config, ytDlpPath, url)
		cancel()
		if ok {
			rememberDirectStream(url, direct)
		} else {
			rememberStreamFailure(url)
		}
	}
}
