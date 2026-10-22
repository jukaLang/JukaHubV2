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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"runtime"
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
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
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

// --- Preloading --------------------------------------------------------------
//
// A yt-dlp run costs seconds before it prints anything, and almost all of that
// is the frozen Python build unpacking and starting. One process resolves a
// whole window of links (yt-dlp accepts several URLs and prints one JSON object
// per video), so preloading four cards costs one start instead of four. That is
// what lets the lightning-bolt marker keep up with browsing on the handheld.
//
// Everything here is single-process by design: a 1 GB TrimUI dies when two
// Python builds compete for RAM with the decoder, so the batch is one child, it
// is cancelled the moment the user presses Play, and it is skipped entirely when
// free memory is low.

const (
	// prefetchSettleDelay lets a burst of navigation finish before a batch
	// starts; resolving a window the user already scrolled past is pure waste
	// when a start costs seconds.
	prefetchSettleDelay = 300 * time.Millisecond
	// prefetchBatchMax bounds one batch. Each link still costs extraction time
	// inside the shared process, and a huge batch would delay the next window.
	prefetchBatchMax = 6
	// prefetchMemoryFloorMB keeps preloading off a device that is already tight
	// on RAM: yt-dlp needs tens of megabytes next to the decoder and the UI.
	prefetchMemoryFloorMB = 200
	// streamBatchBudget bounds one batch extraction.
	streamBatchBudget = 2 * time.Minute
)

// batchLine is one video's answer from a batch run: the id, the page URL and
// the selected muxed stream. The batch prints fields rather than `-j` JSON on
// purpose: a YouTube JSON dump is ~11 MB per video, which is far too much to
// buffer on a 1 GB handheld, while these five fields are a single short line.
type batchLine struct {
	id      string
	pageURL string
	stream  string
}

// batchPrintTemplate is the --print format runYtDlpBatch asks for. The codecs
// are printed so a video-only format can be rejected: one ffmpeg input plays one
// URL, so a separate video track would give silent playback.
const batchPrintTemplate = "%(id)s\t%(webpage_url)s\t%(vcodec)s\t%(acodec)s\t%(url)s"

// parseBatchLine reads one line of batchPrintTemplate output.
func parseBatchLine(line string) (batchLine, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) < 5 {
		return batchLine{}, false
	}
	res := batchLine{
		id:      strings.TrimSpace(fields[0]),
		pageURL: strings.TrimSpace(fields[1]),
		stream:  strings.TrimSpace(fields[4]),
	}
	if strings.TrimSpace(fields[2]) == "none" || strings.TrimSpace(fields[3]) == "none" {
		return batchLine{}, false // one track only: it would play silent or blank
	}
	if !strings.HasPrefix(res.stream, "http://") && !strings.HasPrefix(res.stream, "https://") {
		return batchLine{}, false
	}
	return res, true
}

// youtubeVideoID extracts the video id from the URL shapes YouTube hands out.
func youtubeVideoID(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	switch {
	case strings.HasSuffix(host, "youtu.be"):
		return strings.Trim(u.Path, "/")
	case strings.Contains(host, "youtube.com"):
		if v := u.Query().Get("v"); v != "" {
			return v
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live") {
			return parts[1]
		}
	}
	return ""
}

// batchSourceIndex maps the entries of a batch run back onto the sources that
// were queued. Page URL first, video id second, because yt-dlp normalizes (and
// sometimes rewrites) the URL it reports.
type batchSourceIndex struct {
	byPage map[string]string
	byID   map[string]string
}

func newBatchSourceIndex(sources []string) batchSourceIndex {
	ix := batchSourceIndex{
		byPage: make(map[string]string, len(sources)),
		byID:   make(map[string]string, len(sources)),
	}
	for _, s := range sources {
		ix.byPage[s] = s
		if id := youtubeVideoID(s); id != "" {
			ix.byID[id] = s
		}
	}
	return ix
}

// sourceFor returns the queued source a result belongs to, or "" when it is a
// video nobody asked for (a redirect, a playlist entry, ...).
func (ix batchSourceIndex) sourceFor(pageURL, id string) string {
	if pageURL != "" {
		if source, ok := ix.byPage[pageURL]; ok {
			return source
		}
	}
	if id != "" {
		if source, ok := ix.byID[id]; ok {
			return source
		}
	}
	return ""
}

// runYtDlpBatch resolves several sources with ONE yt-dlp process and hands every
// video to handle as its JSON line arrives. Streaming the lines matters: the
// focused card is first in the batch, so it is marked while the extractor is
// still working through the ones after it, instead of after the whole batch.
func runYtDlpBatch(ctx context.Context, config *Config, ytDlpPath string, sources []string, handle func(batchLine)) error {
	if ytDlpPath == "" || len(sources) == 0 {
		return nil
	}
	args := []string{
		"--simulate",
		"--print", batchPrintTemplate,
		"--no-warnings",
		"--no-playlist",
		"--no-check-certificate",
		"--geo-bypass",
		"--ignore-errors",
		"--user-agent", streamUserAgent,
		"--extractor-args", "youtube:player_client=android",
		// The same muxed selector the single-video path uses, so a batch hit and
		// a direct play pick the same format.
		"-f", streamFormatSelector(playbackMaxHeight(config)),
	}
	// Config-provided args last, so cookies or an API key win over the defaults.
	args = append(args, ytDlpExtraArgsSlice(config)...)
	args = append(args, sources...)

	ctx, cancel := context.WithTimeout(ctx, streamBatchBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, ytDlpPath, args...)
	cmd.Env = helperToolEnv(ytDlpPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	// One line is an id, a page URL and a signed media URL: a few KB. The cap
	// is generous but small enough that a runaway line cannot eat the handheld's
	// memory.
	scanner.Buffer(make([]byte, 0, 16*1024), 1<<20)
	for scanner.Scan() {
		if res, ok := parseBatchLine(scanner.Text()); ok {
			handle(res)
		}
	}
	if err := scanner.Err(); err != nil {
		// The pipe must be drained or the child blocks forever writing into a
		// full buffer, so stop it before waiting instead of hanging the worker.
		cancel()
		_ = cmd.Wait()
		return fmt.Errorf("read yt-dlp output: %w", err)
	}
	runErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr != nil {
		return fmt.Errorf("%w (%s)", runErr, streamFailureReason(stderr.String()))
	}
	return nil
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

// prefetchMemoryOK reports whether there is room for a helper process next to
// the UI and the decoder. Non-Linux hosts always pass: the floor is about the
// handheld, not the dev machine.
func prefetchMemoryOK() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	info := readProcMeminfo()
	if info.MemTotal == 0 {
		return true
	}
	return info.MemAvail/1024 >= prefetchMemoryFloorMB
}

var (
	prefetchMu         sync.Mutex
	prefetchPending    []string
	prefetchConfig     *Config
	prefetchWorkCancel context.CancelFunc
	prefetchWorkerOnce sync.Once
	prefetchWake       = make(chan struct{}, 1)
)

// prefetchWindowSize is how many cards ahead of the focus ring are resolved.
func prefetchWindowSize() int {
	if IsTrimuiDevice() || P().IsHandheld() {
		return 4
	}
	return prefetchBatchMax
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

// prefetchSources queues a set of page URLs for background resolution.
func prefetchSources(config *Config, urls []string) {
	targets := prefetchTargets(urls)
	if len(targets) == 0 {
		return
	}
	if len(targets) > prefetchBatchMax {
		targets = targets[:prefetchBatchMax]
	}
	prefetchMu.Lock()
	prefetchPending = targets // the newest window wins outright
	prefetchConfig = config
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

func prefetchWorker() {
	for range prefetchWake {
		time.Sleep(prefetchSettleDelay)
		prefetchMu.Lock()
		batch := prefetchPending
		config := prefetchConfig
		prefetchPending = nil
		prefetchMu.Unlock()
		if len(batch) == 0 {
			continue
		}
		// Never compete with the video the user is waiting for.
		if videoPrepareActive() {
			continue
		}
		prefetchBatch(config, batch)
	}
}

// setPrefetchCancel stores the cancel func of the running batch.
func setPrefetchCancel(cancel context.CancelFunc) {
	prefetchMu.Lock()
	prefetchWorkCancel = cancel
	prefetchMu.Unlock()
}

// cancelPrefetchWork stops the running batch, freeing the CPU and the helper
// process for a video the user just asked for.
func cancelPrefetchWork() {
	prefetchMu.Lock()
	cancel := prefetchWorkCancel
	prefetchWorkCancel = nil
	prefetchMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// prefetchBatch resolves a batch, probes each result and caches what is
// playable. The probe is part of preloading on purpose: the bolt promises Play
// starts immediately, and the player would otherwise still wait for ffprobe.
func prefetchBatch(config *Config, sources []string) {
	ytDlpPath := ""
	if config != nil {
		ytDlpPath = getToolPath("yt-dlp", config)
	}
	if ytDlpPath == "" {
		return
	}
	if !prefetchMemoryOK() {
		log.Printf("[STREAM] preload skipped: low memory")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	setPrefetchCancel(cancel)
	defer func() {
		cancel()
		setPrefetchCancel(nil)
	}()
	// Play may have been pressed between the worker's check and this point.
	if videoPrepareActive() {
		return
	}

	index := newBatchSourceIndex(sources)
	ffprobePath := ""
	if config != nil {
		ffprobePath = getToolPath("ffprobe", config)
	}
	// A missing ffprobe must not reject every card: with no probe the result is
	// cached as-is, which is exactly what the player falls back to anyway.
	if !toolPathRunnable(ffprobePath) {
		ffprobePath = ""
	}
	done := make(map[string]bool, len(sources))
	resolved := 0
	runErr := runYtDlpBatch(ctx, config, ytDlpPath, sources, func(res batchLine) {
		if ctx.Err() != nil || resolved == len(sources) {
			return
		}
		source := index.sourceFor(res.pageURL, res.id)
		if source == "" || done[source] {
			return
		}
		done[source] = true
		// The probe runs while the extractor is still working through the
		// remaining links: yt-dlp writes into the pipe buffer meanwhile.
		if ffprobePath != "" && !probePlayableStream(ctx, ffprobePath, res.stream) {
			rememberStreamFailure(source)
			return
		}
		rememberDirectStream(source, res.stream)
		resolved++
	})
	if ctx.Err() != nil {
		return // the user pressed Play; that video owns the process now
	}
	if runErr != nil {
		log.Printf("[STREAM] preload batch ended with: %v", runErr)
	}
	if resolved > 0 {
		log.Printf("[STREAM] preloaded %d/%d card(s)", resolved, len(sources))
	}
	// A source the batch could not resolve is left alone for a while so
	// scrolling does not re-ask for it; pressing Play still tries again.
	for _, source := range sources {
		if !done[source] {
			rememberStreamFailure(source)
		}
	}
}
