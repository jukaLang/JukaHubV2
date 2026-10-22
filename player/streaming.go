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
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	streamReachMu.Lock()
	streamReach = map[string]time.Time{}
	streamReachMu.Unlock()
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

// streamAttempt is one extraction strategy. The order is deliberate: measured
// against current YouTube, the web/ios/tv/mweb clients return no muxed format
// at all from a standalone yt-dlp without a JavaScript runtime, while the
// android clients still do. The web-based clients are therefore only the last
// resort (they do work when a JS runtime is installed), and each attempt gets
// cheaper and more permissive as it goes.
type streamAttempt struct {
	name string
	args []string
}

func streamAttempts(maxHeight int) []streamAttempt {
	muxed := streamFormatSelector(maxHeight)
	// 18 is the 360p muxed MP4 every client still serves; asking for it first
	// is the last chance before the caller downloads the whole file.
	anyMuxed := "18/" + muxed
	return []streamAttempt{
		{
			name: "android",
			args: []string{
				"--extractor-args", "youtube:player_client=android",
				"-f", muxed,
			},
		},
		{
			name: "android_vr",
			args: []string{
				"--extractor-args", "youtube:player_client=android_vr",
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
			// No extractor overrides: yt-dlp's own client list also fetches
			// the webpage, which is the path that works once a JavaScript
			// runtime is available next to the helper tools.
			name: "default clients",
			args: []string{
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
	// A killed extractor must not keep Wait blocked on a pipe its child still
	// holds open: preload cancels these processes whenever the user presses Play.
	cmd.WaitDelay = 3 * time.Second
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
func streamableSource(ctx context.Context, config *Config, ytDlpPath, source string) (string, bool) {
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
	ctx, cancel := context.WithTimeout(ctx, streamResolveBudget)
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
func probePlayableStream(ctx context.Context, ffprobePath, url string) bool {
	if ffprobePath == "" {
		return false
	}
	if snap, ok := cachedProbe(url); ok {
		return snap.width > 0 && snap.height > 0
	}
	// A URL that a previous window already reached is not probed again: the
	// second ffprobe is a second of waiting before the first frame.
	if streamRecentlyReachable(url) {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fields, err := ffprobeStream(ctx, ffprobePath, url)
	if err != nil {
		// ffprobe failing to describe a URL does not mean the decoder cannot
		// play it: the handheld's build rejected signed googlevideo links while
		// ffmpeg streamed them fine, so every preloaded card was thrown away and
		// the video was downloaded instead. Confirm the CDN accepts the URL
		// before giving up on streaming.
		if probeStreamReachable(ctx, url) {
			rememberStreamReachable(url)
			log.Printf("[STREAM] ffprobe could not describe %s (%v); the CDN accepts it, streaming anyway", sanitizeLogURL(url), err)
			return true
		}
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

// streamReachTTL mirrors the probe cache: a URL the CDN accepted stays trusted
// for the same short window.
const streamReachTTL = 2 * time.Minute

var (
	streamReachMu sync.Mutex
	streamReach   = map[string]time.Time{}
)

// streamRecentlyReachable reports whether the CDN answered for this URL.
func streamRecentlyReachable(url string) bool {
	key := streamResolutionKey(url)
	if key == "" {
		return false
	}
	streamReachMu.Lock()
	defer streamReachMu.Unlock()
	at, ok := streamReach[key]
	if !ok {
		return false
	}
	if time.Since(at) > streamReachTTL {
		delete(streamReach, key)
		return false
	}
	return true
}

// rememberStreamReachable notes that the CDN served a range of this URL.
func rememberStreamReachable(url string) {
	key := streamResolutionKey(url)
	if key == "" {
		return
	}
	streamReachMu.Lock()
	streamReach[key] = time.Now()
	streamReachMu.Unlock()
}

// probeStreamReachable fetches the first kilobyte of a resolved stream with the
// same client identity yt-dlp used. A 200/206 carrying a media type proves the
// signed URL is live even when ffprobe cannot open it.
func probeStreamReachable(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", streamUserAgent)
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return false
	}
	ctype := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	return strings.HasPrefix(ctype, "video/") || strings.HasPrefix(ctype, "audio/") ||
		strings.HasPrefix(ctype, "application/mp4") || strings.Contains(ctype, "octet-stream")
}

// isHTTPMediaURL reports whether a source is fetched over HTTP(S), where the
// client identity matters to the CDN.
func isHTTPMediaURL(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// tryStreamPlayback starts the embedded player on a direct stream. started is
// false when the caller should fall back to downloading the file with yt-dlp;
// cancelled is true when the user backed out while the stream was resolving, in
// which case falling back would start a download they just declined.
func tryStreamPlayback(ctx context.Context, config *Config, ytDlpPath, url string, startSec float64) (started, cancelled bool) {
	// A file on the SD card never needs a download round-trip.
	if st, err := os.Stat(url); err == nil && !st.IsDir() {
		startEmbeddedPlayback(config, url, startSec, false)
		return true, false
	}
	_, wasCached := cachedDirectStream(url)
	direct, ok := streamableSource(ctx, config, ytDlpPath, url)
	if !ok {
		return false, ctx.Err() != nil
	}
	ffprobePath := ""
	if config != nil {
		ffprobePath = getToolPath("ffprobe", config)
	}
	if !probePlayableStream(ctx, ffprobePath, direct) {
		forgetDirectStream(url)
		// A cached URL can expire while it sits in the cache: resolve once
		// more for a fresh one before giving up on streaming.
		if !wasCached || ctx.Err() != nil {
			return false, ctx.Err() != nil
		}
		direct, ok = streamableSource(ctx, config, ytDlpPath, url)
		if !ok || !probePlayableStream(ctx, ffprobePath, direct) {
			return false, ctx.Err() != nil
		}
	}
	log.Printf("[STREAM] playing %s directly (no download)", sanitizeLogURL(url))
	startEmbeddedPlayback(config, direct, startSec, false)
	return true, false
}

// videoStreamPreloaded reports whether Play on this source will start without
// waiting for yt-dlp: its stream URL is already resolved (the background
// prefetch followed the focus ring) or the source is playable as-is. The media
// lists draw a lightning bolt and tint the title for these.
func videoStreamPreloaded(url string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	if isDirectStreamURL(url) {
		return true
	}
	_, ok := cachedDirectStream(url)
	return ok
}

// --- Preparing a video -------------------------------------------------------
//
// Pressing Play on a cold card spends seconds in yt-dlp before the decoder has
// anything to show. The media screens put up a modal spinner for that window and
// ignore navigation, so the handheld cannot scroll off the item the user just
// chose (the grid used to keep moving behind a black player). B cancels.

type videoPrepareState struct {
	active bool
	seq    uint64
	title  string
	cancel context.CancelFunc
}

var (
	videoPrepareMu sync.Mutex
	videoPrepare   videoPrepareState
)

// beginVideoPrepare starts (or replaces) the loading state and returns the
// context the resolution must honour plus a sequence number for finishing.
func beginVideoPrepare(title string) (uint64, context.Context) {
	// The batch preload is cancelled here: the video the user is waiting for
	// must not share the handheld with a background extraction.
	cancelPrefetchWork()
	videoPrepareMu.Lock()
	if videoPrepare.cancel != nil {
		videoPrepare.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	videoPrepare.seq++
	videoPrepare.active = true
	videoPrepare.title = strings.TrimSpace(title)
	videoPrepare.cancel = cancel
	seq := videoPrepare.seq
	videoPrepareMu.Unlock()
	return seq, ctx
}

// finishVideoPrepare clears the loading state for this attempt. An older
// attempt finishing late must not clear a newer one's spinner.
func finishVideoPrepare(seq uint64) {
	videoPrepareMu.Lock()
	if videoPrepare.seq == seq {
		if videoPrepare.cancel != nil {
			videoPrepare.cancel()
		}
		videoPrepare = videoPrepareState{seq: seq}
	}
	videoPrepareMu.Unlock()
}

// videoPrepareActive reports whether a video is being resolved.
func videoPrepareActive() bool {
	videoPrepareMu.Lock()
	defer videoPrepareMu.Unlock()
	return videoPrepare.active
}

// videoPrepareStatus returns the active state and the title being loaded.
func videoPrepareStatus() (bool, string) {
	videoPrepareMu.Lock()
	defer videoPrepareMu.Unlock()
	return videoPrepare.active, videoPrepare.title
}

// cancelVideoPrepare stops the pending resolution (B / Esc). It reports whether
// anything was in flight.
func cancelVideoPrepare() bool {
	videoPrepareMu.Lock()
	defer videoPrepareMu.Unlock()
	if !videoPrepare.active {
		return false
	}
	if videoPrepare.cancel != nil {
		videoPrepare.cancel()
	}
	videoPrepare = videoPrepareState{seq: videoPrepare.seq}
	return true
}

// toolPathRunnable reports whether a resolved tool path is a real executable.
// getToolPath returns the bare name when the tool is not installed (so callers
// can report it), and running that bare name would fail on every card.
func toolPathRunnable(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, `/\\`) {
		st, err := os.Stat(trimmed)
		return err == nil && !st.IsDir()
	}
	_, err := exec.LookPath(trimmed)
	return err == nil
}

// --- Preloading --------------------------------------------------------------
//
// Preloading resolves a window of cards ahead of the focus ring so the
// lightning-bolt marker means what it says: Play starts the video instead of
// waiting for an extractor.
//
// It runs a small pool of ONE JOB PER LINK: two extractors on a handheld, three
// elsewhere. A single process walking the whole window looks cheaper, but it
// makes the fourth card wait for the first three, and that queue is exactly
// where preloading stopped keeping up on a TrimUI. Per-link jobs mean each card
// is marked as soon as its own extraction finishes, in whatever order that is.
//
// The pool is hard-bounded so the app can never be buried in yt-dlp processes:
// at most prefetchWorkerCount() children exist at any moment across every
// window, each has a fixed budget, its process is killed when the context is
// cancelled, the whole batch is cancelled the instant the user presses Play,
// and a slot is only released when its child has really exited. Windows overlap
// so a new one is not stuck behind the previous window's budget, but a window
// never kills the extractions it already started: on the handheld those were
// routinely the cards that ended up being played.

const (
	// prefetchSettleDelay lets a burst of navigation finish before work starts;
	// resolving a window the user already scrolled past is pure waste.
	prefetchSettleDelay = 250 * time.Millisecond
	// prefetchRetryDelay is how long a queued window waits while the video the
	// user pressed Play on is still being resolved.
	prefetchRetryDelay = 400 * time.Millisecond
	// prefetchWindowMax bounds how many links one window may queue.
	prefetchWindowMax = 8
	// prefetchMemoryFloorMB keeps preloading off a device that is already tight
	// on RAM: yt-dlp needs tens of megabytes next to the decoder and the UI.
	prefetchMemoryFloorMB = 200
	// prefetchSingleWorkerMB is the free-memory band where only one extractor
	// runs, so a device with the decoder already loaded never runs two.
	prefetchSingleWorkerMB = 420
	// prefetchJobBudget bounds one link, including its retry attempts.
	prefetchJobBudget = 45 * time.Second
)

// prefetchMemoryOK reports whether there is room for a helper process next to
// the UI and the decoder. Non-Linux hosts always pass: the floor is about the
// handheld, not the dev machine.
func prefetchMemoryOK() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	return freeMemoryMB() >= prefetchMemoryFloorMB
}

// freeMemoryMB is the available RAM in MB, or a value that never blocks preload
// when /proc/meminfo cannot be read.
func freeMemoryMB() uint64 {
	info := readProcMeminfo()
	if info.MemTotal == 0 {
		return ^uint64(0)
	}
	return info.MemAvail / 1024
}

// prefetchWorkerCount is how many extractors may run at once. Handhelds get two:
// one keeps the focused card fast while the other works ahead, and two Python
// builds still fit beside the decoder on a 1 GB device.
func prefetchWorkerCount() int {
	limit := 3
	if IsTrimuiDevice() || P().IsHandheld() {
		limit = 2
	}
	if runtime.GOOS == "linux" && freeMemoryMB() < prefetchSingleWorkerMB && limit > 1 {
		limit = 1
	}
	return limit
}

var (
	prefetchMu         sync.Mutex
	prefetchPending    []string
	prefetchConfig     *Config
	prefetchGen        uint64
	prefetchWorkerOnce sync.Once
	prefetchWake       = make(chan struct{}, 1)

	// prefetchWindows holds the cancel func of every window still running.
	// Play cancels all of them; a newer window does not, because the
	// extractions already in flight are for the cards around the same focus
	// ring and killing them threw away the seconds spent on them.
	prefetchWindows   = map[uint64]context.CancelFunc{}
	prefetchWindowSeq uint64

	// prefetchSlotPool is one process-wide bound shared by every window, so
	// overlapping windows cannot double the number of yt-dlp children on a
	// handheld with 1 GB of RAM.
	prefetchSlots     chan struct{}
	prefetchSlotsOnce sync.Once
)

// prefetchWindowSize is how many cards ahead of the focus ring are resolved.
func prefetchWindowSize() int {
	if IsTrimuiDevice() || P().IsHandheld() {
		return 6
	}
	return prefetchWindowMax
}

// prefetchFirstResult warms the first search result, which is the card the focus
// ring starts on and therefore the most likely Play.
func prefetchFirstResult(config *Config, videos []VideoInfo) {
	prefetchVideos(config, videos, 0)
}

// prefetchFocusedResult warms the highlighted card and the window behind it.
func prefetchFocusedResult(config *Config, videos []VideoInfo) {
	prefetchVideos(config, videos, focusedResultIndex)
}

// prefetchVideos queues the focused card and the next few after it.
func prefetchVideos(config *Config, videos []VideoInfo, focus int) {
	if len(videos) == 0 {
		return
	}
	if focus < 0 {
		focus = 0
	}
	if focus >= len(videos) {
		focus = len(videos) - 1
	}
	end := focus + prefetchWindowSize()
	if end > len(videos) {
		end = len(videos)
	}
	urls := make([]string, 0, end-focus)
	for _, v := range videos[focus:end] {
		if u := strings.TrimSpace(v.GetURL()); u != "" {
			urls = append(urls, u)
		}
	}
	prefetchSources(config, urls)
}

// prefetchTargets drops work that would not change a card: sources that are
// already resolved, that just failed, or that are local files. Direct URLs are
// cached on the way so they show the ready mark immediately.
func prefetchTargets(urls []string) []string {
	targets := make([]string, 0, len(urls))
	seen := make(map[string]bool, len(urls))
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		if st, err := os.Stat(u); err == nil && !st.IsDir() {
			continue // a file on the card needs no resolution
		}
		if isDirectStreamURL(u) {
			rememberDirectStream(u, u)
			continue
		}
		if _, ok := cachedDirectStream(u); ok {
			continue
		}
		if streamRecentlyFailed(u) {
			continue
		}
		targets = append(targets, u)
	}
	return targets
}

// prefetchSources queues a set of page URLs for background resolution. The
// newest window replaces the queue: the cards around the focus ring are the
// ones the user may actually play.
func prefetchSources(config *Config, urls []string) {
	targets := prefetchTargets(urls)
	if len(targets) == 0 {
		return
	}
	if len(targets) > prefetchWindowMax {
		targets = targets[:prefetchWindowMax]
	}
	prefetchMu.Lock()
	prefetchPending = targets
	prefetchConfig = config
	prefetchGen++
	prefetchMu.Unlock()
	ensurePrefetchWorker()
	select {
	case prefetchWake <- struct{}{}:
	default:
	}
}

func ensurePrefetchWorker() {
	prefetchWorkerOnce.Do(func() { go prefetchWorker() })
}

// prefetchWorker is the only place a window is started. It settles after a
// navigation burst, takes the newest queue, and resolves it with a bounded pool.
func prefetchWorker() {
	for range prefetchWake {
		time.Sleep(prefetchSettleDelay)
		// Never compete with the video the user is waiting for. The queued
		// window is left in place and retried: dropping it here is why the
		// Shorts feed had nothing resolved ahead of the one playing, so every
		// next press waited for yt-dlp again.
		if videoPrepareActive() {
			time.Sleep(prefetchRetryDelay)
			select {
			case prefetchWake <- struct{}{}:
			default:
			}
			continue
		}
		prefetchMu.Lock()
		batch := prefetchPending
		config := prefetchConfig
		gen := prefetchGen
		prefetchPending = nil
		prefetchMu.Unlock()
		if len(batch) == 0 {
			continue
		}
		// Windows overlap: a window waits on the shared slot pool, so the next
		// one can start while an older one's last extractions finish. With one
		// blocking loop the newest window queued behind the previous window's
		// 45 s budget, which is how preloading fell behind on the handheld.
		go prefetchResolveWindow(config, gen, batch)
	}
}

// prefetchSuperseded reports whether a newer window took over while this one was
// running. The newest window wins; old links are dropped rather than finished.
func prefetchSuperseded(gen uint64) bool {
	prefetchMu.Lock()
	defer prefetchMu.Unlock()
	return prefetchGen != gen
}

// registerPrefetchWindow records a running window's cancel func and returns its
// id.
func registerPrefetchWindow(cancel context.CancelFunc) uint64 {
	prefetchMu.Lock()
	defer prefetchMu.Unlock()
	prefetchWindowSeq++
	prefetchWindows[prefetchWindowSeq] = cancel
	return prefetchWindowSeq
}

// unregisterPrefetchWindow forgets a window that finished on its own.
func unregisterPrefetchWindow(id uint64) {
	prefetchMu.Lock()
	delete(prefetchWindows, id)
	prefetchMu.Unlock()
}

// cancelPrefetchWork stops every running window and every child process in it,
// freeing the CPU and the helper processes for the video the user just asked
// for. It is called when playback starts.
func cancelPrefetchWork() {
	prefetchMu.Lock()
	draining := make([]context.CancelFunc, 0, len(prefetchWindows))
	for id, cancel := range prefetchWindows {
		draining = append(draining, cancel)
		delete(prefetchWindows, id)
	}
	prefetchMu.Unlock()
	for _, cancel := range draining {
		cancel()
	}
}

// prefetchSlotPool returns the shared child-process budget, sized once from the
// device's memory and handheld status.
func prefetchSlotPool() chan struct{} {
	prefetchSlotsOnce.Do(func() {
		prefetchSlots = make(chan struct{}, prefetchWorkerCount())
	})
	return prefetchSlots
}

// prefetchResolveWindow resolves one window with the shared child budget. Each
// link gets its own yt-dlp, so a card is marked the moment its extraction lands
// instead of after the whole window.
func prefetchResolveWindow(config *Config, gen uint64, sources []string) {
	if !prefetchMemoryOK() {
		log.Printf("[STREAM] preload skipped: low memory (%d MB free)", freeMemoryMB())
		return
	}
	ytDlpPath := ""
	ffprobePath := ""
	if config != nil {
		ytDlpPath = getToolPath("yt-dlp", config)
		ffprobePath = getToolPath("ffprobe", config)
	}
	if !toolPathRunnable(ytDlpPath) {
		return
	}
	// A missing ffprobe must not reject every card: with no probe the result is
	// cached as-is, which is what the player falls back to anyway.
	if !toolPathRunnable(ffprobePath) {
		ffprobePath = ""
	}
	prefetchResolveWindowWith(gen, sources, func(ctx context.Context, source string) bool {
		return prefetchResolveOne(ctx, config, ytDlpPath, ffprobePath, source)
	})
}

// prefetchResolveWindowWith is the window's scheduling core, with the resolver
// injected so the cancellation rules can be tested without subprocesses.
func prefetchResolveWindowWith(gen uint64, sources []string, resolve func(context.Context, string) bool) {
	if len(sources) == 0 || resolve == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	windowID := registerPrefetchWindow(cancel)
	defer func() {
		cancel()
		unregisterPrefetchWindow(windowID)
	}()

	slots := prefetchSlotPool()
	var wg sync.WaitGroup
	var resolved int32
	queue := sources

dispatch:
	for len(queue) > 0 {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			break dispatch
		}
		if ctx.Err() != nil {
			<-slots
			break
		}
		// Play outranks the rest of this queue; it cancels the context and the
		// jobs below notice within one extraction attempt.
		if videoPrepareActive() {
			<-slots
			break dispatch
		}
		// A newer window took over: stop queueing this window's remaining
		// cards, but let the extractions already running finish. They are for
		// the cards around the same focus ring, and cancelling them is what
		// threw away every window's last results.
		if prefetchSuperseded(gen) {
			<-slots
			break dispatch
		}
		source := queue[0]
		queue = queue[1:]
		if _, ok := cachedDirectStream(source); ok {
			<-slots
			continue
		}
		wg.Add(1)
		go func(source string) {
			defer wg.Done()
			defer func() { <-slots }()
			if resolve(ctx, source) {
				atomic.AddInt32(&resolved, 1)
			}
		}(source)
	}

	// Wait for the jobs already running instead of killing them. Cancelling
	// first ended every window by killing its last prefetchWorkerCount()
	// extractions — on the handheld the card the user was about to play was
	// routinely one of them, so preloading never marked anything. Each job is
	// bounded by prefetchJobBudget and by ctx, so this cannot hang the pool.
	wg.Wait()
	cancel()

	if n := atomic.LoadInt32(&resolved); n > 0 {
		log.Printf("[STREAM] preloaded %d/%d card(s)", n, len(sources))
	}
}

// prefetchResolveOne resolves, checks and caches one link. It never records a
// failure for a cancelled job: a cancelled extraction says nothing about the
// video, and remembering it would hide the card for a whole cooldown.
func prefetchResolveOne(ctx context.Context, config *Config, ytDlpPath, ffprobePath, source string) bool {
	jobCtx, cancel := context.WithTimeout(ctx, prefetchJobBudget)
	defer cancel()

	direct, ok := resolveDirectStream(jobCtx, config, ytDlpPath, source)
	if ctx.Err() != nil {
		return false // superseded or Play: leave the cache alone
	}
	if !ok || direct == "" {
		rememberStreamFailure(source)
		return false
	}
	// Probing here is what makes Play immediate: the player would otherwise
	// spend the first seconds of the "instant" start waiting for ffprobe.
	if ffprobePath != "" && !probePlayableStream(jobCtx, ffprobePath, direct) {
		if ctx.Err() == nil {
			rememberStreamFailure(source)
		}
		return false
	}
	rememberDirectStream(source, direct)
	return true
}
