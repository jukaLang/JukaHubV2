package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// attemptSelector returns the -f selector of an attempt.
func attemptSelector(t *testing.T, attempt streamAttempt) string {
	t.Helper()
	for i, arg := range attempt.args {
		if arg == "-f" {
			if i+1 >= len(attempt.args) {
				t.Fatalf("%s has a dangling -f", attempt.name)
			}
			return attempt.args[i+1]
		}
	}
	t.Fatalf("%s has no -f selector", attempt.name)
	return ""
}

// Every strategy must ask for one file that already contains audio: a "+"
// selector hands back two URLs, which the player cannot feed to one ffmpeg
// input, and the download fallback is exactly what streaming avoids.
func TestStreamAttemptsAskForOneMuxedStream(t *testing.T) {
	attempts := streamAttempts(720)
	if len(attempts) < 3 {
		t.Fatalf("attempts = %d, want several client strategies", len(attempts))
	}
	seen := map[string]bool{}
	for _, attempt := range attempts {
		if attempt.name == "" {
			t.Error("attempt without a name: its failures would be anonymous in the log")
		}
		if seen[attempt.name] {
			t.Errorf("duplicate attempt name %q", attempt.name)
		}
		seen[attempt.name] = true
		selector := attemptSelector(t, attempt)
		if strings.Contains(selector, "+") {
			t.Errorf("%s selector %q merges two tracks", attempt.name, selector)
		}
		if !strings.Contains(selector, "height<=720") {
			t.Errorf("%s selector %q ignores the resolution ceiling", attempt.name, selector)
		}
		if !strings.Contains(selector, "vcodec!=none") || !strings.Contains(selector, "acodec!=none") {
			t.Errorf("%s selector %q does not require audio and video in one format", attempt.name, selector)
		}
	}

	// The fallbacks ask for format 18 first: the 360p muxed MP4 every client
	// still serves is far better than a download that never starts.
	fallback := false
	for _, attempt := range attempts {
		if strings.HasPrefix(attemptSelector(t, attempt), "18/") {
			fallback = true
		}
	}
	if !fallback {
		t.Error("no attempt falls back to format 18")
	}

	// The first attempt must be the client that actually returns a muxed
	// format without a JavaScript runtime; a web client there would burn
	// seconds on every uncached play only to fail.
	if !strings.Contains(strings.Join(attempts[0].args, " "), "player_client=android") {
		t.Errorf("first attempt = %v, want the android client", attempts[0].args)
	}
}

func TestStreamAttemptsUseDistinctClients(t *testing.T) {
	attempts := streamAttempts(720)
	// One attempt must leave the client list alone: forcing a client is what
	// caused the device to fall back to downloading.
	wildcard := false
	for _, attempt := range attempts {
		if !strings.Contains(strings.Join(attempt.args, " "), "player_client") {
			wildcard = true
		}
	}
	if !wildcard {
		t.Fatal("no attempt uses yt-dlp's default client list")
	}
}

func TestStreamFormatSelectorCeiling(t *testing.T) {
	if got := streamFormatSelector(0); !strings.Contains(got, "height<=720") {
		t.Errorf("streamFormatSelector(0) = %q, want the 720 fallback", got)
	}
	if got := streamFormatSelector(360); !strings.Contains(got, "height<=360") {
		t.Errorf("streamFormatSelector(360) = %q", got)
	}
}

func TestStreamFailureReasonPrefersErrorLine(t *testing.T) {
	stderr := "WARNING: something noisy\n[download] 12%\nERROR: [youtube] abc123: Requested format is not available. Use --list-formats\n"
	got := streamFailureReason(stderr)
	if !strings.Contains(got, "Requested format is not available") {
		t.Fatalf("reason = %q, want the yt-dlp error", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("reason spans lines: %q", got)
	}
	if strings.HasPrefix(got, "ERROR:") == false {
		t.Errorf("reason = %q, want it to start at ERROR:", got)
	}
}

func TestStreamFailureReasonFallsBackToLastLine(t *testing.T) {
	if got := streamFailureReason("\n  \nbroken pipe\n"); got != "broken pipe" {
		t.Fatalf("reason = %q", got)
	}
	if got := streamFailureReason(""); got != "" {
		t.Fatalf("empty stderr should give an empty reason, got %q", got)
	}
	long := strings.Repeat("x", 500)
	if got := streamFailureReason(long); len(got) > 210 {
		t.Errorf("reason not truncated: %d chars", len(got))
	}
}

func TestStreamFailureFatal(t *testing.T) {
	fatal := []string{
		"ERROR: [youtube] x: Private video. Sign in if you've been granted access",
		"ERROR: [youtube] x: Video unavailable",
		"ERROR: Unsupported URL: https://example.com/",
	}
	for _, in := range fatal {
		if !streamFailureFatal(in) {
			t.Errorf("streamFailureFatal(%q) = false, want a stop", in)
		}
	}
	retryable := []string{
		"ERROR: [youtube] x: Requested format is not available",
		"ERROR: unable to download video data: HTTP Error 403: Forbidden",
		"i/o timeout",
		"WARNING: [youtube] No supported JavaScript runtime could be found",
	}
	for _, in := range retryable {
		if streamFailureFatal(in) {
			t.Errorf("streamFailureFatal(%q) = true, another client may still work", in)
		}
	}
}

func TestResolveWithRunnerFallsThroughAttempts(t *testing.T) {
	var calls [][]string
	run := func(ctx context.Context, args []string) (string, error) {
		calls = append(calls, args)
		if len(calls) == 1 {
			return "", errors.New("exit status 1 (ERROR: [youtube] x: Requested format is not available)")
		}
		return "https://cdn.example/media?id=2", nil
	}
	extra := []string{"--cookies", "/tmp/cookies.txt"}
	got, err := resolveWithRunner(context.Background(), "https://youtu.be/x", 720, extra, run)
	if err != nil {
		t.Fatalf("resolveWithRunner: %v", err)
	}
	if got != "https://cdn.example/media?id=2" {
		t.Fatalf("url = %q", got)
	}
	if len(calls) != 2 {
		t.Fatalf("attempts = %d, want a retry after the first failure", len(calls))
	}
	for i, args := range calls {
		if args[0] != "-g" {
			t.Errorf("attempt %d does not start with -g: %v", i, args[:1])
		}
		if args[len(args)-1] != "https://youtu.be/x" {
			t.Errorf("attempt %d does not end with the source URL: %q", i, args[len(args)-1])
		}
		tail := args[len(args)-3:]
		if strings.Join(tail, " ") != "--cookies /tmp/cookies.txt https://youtu.be/x" {
			t.Errorf("config args are not passed last: %v", tail)
		}
	}
}

func TestResolveWithRunnerStopsOnFatalError(t *testing.T) {
	calls := 0
	run := func(ctx context.Context, args []string) (string, error) {
		calls++
		return "", errors.New("ERROR: [youtube] x: Private video. Sign in if you've been granted access")
	}
	if _, err := resolveWithRunner(context.Background(), "https://youtu.be/x", 720, nil, run); err == nil {
		t.Fatal("expected an error for a private video")
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1: another client cannot unlock a private video", calls)
	}
}

func TestResolveWithRunnerReportsLastFailure(t *testing.T) {
	run := func(ctx context.Context, args []string) (string, error) {
		return "", errors.New("exit status 1 (ERROR: requested format is not available)")
	}
	attempts := streamAttempts(720)
	_, err := resolveWithRunner(context.Background(), "https://youtu.be/x", 720, nil, run)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), attempts[len(attempts)-1].name) {
		t.Errorf("error %q does not name the last strategy tried", err)
	}
}

// A cancelled context must not keep spawning extractions.
func TestResolveWithRunnerRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	run := func(ctx context.Context, args []string) (string, error) {
		calls++
		return "", errors.New("should not run")
	}
	if _, err := resolveWithRunner(ctx, "https://youtu.be/x", 720, nil, run); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 0 {
		t.Fatalf("attempts = %d, want 0 after cancellation", calls)
	}
}

func TestStreamFailureCooldownExpires(t *testing.T) {
	resetStreamCache()
	t.Cleanup(resetStreamCache)
	const url = "https://youtu.be/x"
	if streamRecentlyFailed(url) {
		t.Fatal("fresh cache reports a failure")
	}
	rememberStreamFailure(url)
	if !streamRecentlyFailed(url) {
		t.Fatal("prefetch should skip a source that just failed")
	}
	if !streamRecentlyFailedAt(url, time.Now().Add(streamFailureTTL-time.Second)) {
		t.Fatal("cooldown ended too early")
	}
	if streamRecentlyFailedAt(url, time.Now().Add(streamFailureTTL+time.Second)) {
		t.Fatal("cooldown never ends")
	}
	// A later success clears the cooldown so the prefetch resumes.
	rememberDirectStream(url, "https://cdn.example/media")
	if streamRecentlyFailed(url) {
		t.Fatal("a successful resolution must clear the failure")
	}
	if _, ok := cachedDirectStream(url); !ok {
		t.Fatal("direct URL was not cached")
	}
}

func TestParseProbeOutput(t *testing.T) {
	out := "width=640\nheight=360\navg_frame_rate=30000/1001\nr_frame_rate=30/1\nduration=171.34\n"
	got := parseProbeOutput(out)
	if got.width != 640 || got.height != 360 {
		t.Fatalf("size = %dx%d", got.width, got.height)
	}
	if got.duration < 171 || got.duration > 172 {
		t.Fatalf("duration = %f", got.duration)
	}
	if got.fps < 29.9 || got.fps > 30.0 {
		t.Fatalf("fps = %f, want avg_frame_rate to win", got.fps)
	}

	// An audio-only or unreadable source must not look like a video.
	if fields := parseProbeOutput(""); fields.width != 0 || fields.height != 0 {
		t.Fatalf("empty output = %+v, want zero dimensions", fields)
	}
}

func TestParseBatchLine(t *testing.T) {
	const stream = "https://rr1---sn-bvvbax4pcxg-gq0s.googlevideo.com/videoplayback?itag=18"
	line := "lcjGBhLi_qQ\thttps://www.youtube.com/watch?v=lcjGBhLi_qQ\tavc1.42001E\tmp4a.40.2\t" + stream
	res, ok := parseBatchLine(line)
	if !ok {
		t.Fatalf("valid line rejected: %q", line)
	}
	if res.id != "lcjGBhLi_qQ" || res.pageURL != "https://www.youtube.com/watch?v=lcjGBhLi_qQ" || res.stream != stream {
		t.Fatalf("parsed %+v", res)
	}

	// A single ffmpeg input plays one URL, so a one-track format must be
	// rejected rather than streamed silently or blank.
	if _, ok := parseBatchLine("id\tpage\tavc1\tnone\thttps://cdn/a.mp4"); ok {
		t.Error("a video-only format was accepted")
	}
	if _, ok := parseBatchLine("id\tpage\tnone\tmp4a\thttps://cdn/a.m4a"); ok {
		t.Error("an audio-only format was accepted")
	}
	// Error lines and partial lines are not results.
	if _, ok := parseBatchLine("ERROR: [youtube] x: Video unavailable"); ok {
		t.Error("an error line was accepted")
	}
	if _, ok := parseBatchLine("id\tpage\tavc1\tmp4a"); ok {
		t.Error("a line without a URL was accepted")
	}
	if _, ok := parseBatchLine("id\tpage\tavc1\tmp4a\tNA"); ok {
		t.Error("a placeholder URL was accepted")
	}
}

func TestYouTubeVideoID(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=lcjGBhLi_qQ":      "lcjGBhLi_qQ",
		"https://www.youtube.com/watch?feature=x&v=abc123": "abc123",
		"https://youtu.be/-KPeZqUch08":                     "-KPeZqUch08",
		"https://www.youtube.com/shorts/Zz9x1":             "Zz9x1",
		"https://m.youtube.com/watch?v=qq":                 "qq",
		"https://vimeo.com/12345":                          "",
		"not a url":                                        "",
	}
	for in, want := range cases {
		if got := youtubeVideoID(in); got != want {
			t.Errorf("youtubeVideoID(%q) = %q, want %q", in, got, want)
		}
	}
}

// Both URL shapes the media lists can hold must map back to the resolved stream.
func TestBatchSourceIndexMapsResults(t *testing.T) {
	sources := []string{
		"https://www.youtube.com/watch?v=ready",
		"https://youtu.be/-KPeZqUch08",
		"https://www.youtube.com/watch?v=dead",
	}
	ix := newBatchSourceIndex(sources)

	if got := ix.sourceFor("https://www.youtube.com/watch?v=ready", "ready"); got != sources[0] {
		t.Errorf("page-URL result mapped to %q", got)
	}
	// The short-link source is matched by video id: yt-dlp reports the watch URL.
	if got := ix.sourceFor("https://www.youtube.com/watch?v=-KPeZqUch08", "-KPeZqUch08"); got != sources[1] {
		t.Errorf("short-link result mapped to %q, want the match by video id", got)
	}
	// A result nobody queued must not be mistaken for a source.
	if got := ix.sourceFor("https://www.youtube.com/watch?v=stranger", "stranger"); got != "" {
		t.Errorf("an unqueued video mapped to %q", got)
	}
}

// Only cards whose state would change may reach a batch.
func TestPrefetchTargetsSkipsKnownWork(t *testing.T) {
	resetStreamCache()
	t.Cleanup(resetStreamCache)

	ready := "https://www.youtube.com/watch?v=ready"
	failed := "https://www.youtube.com/watch?v=failed"
	cold := "https://www.youtube.com/watch?v=cold"
	rememberDirectStream(ready, "https://cdn/ready.mp4")
	rememberStreamFailure(failed)

	got := prefetchTargets([]string{ready, failed, cold, cold, "  ", "https://cdn/direct.m3u8"})
	if len(got) != 1 || got[0] != cold {
		t.Fatalf("targets = %v, want only the cold source", got)
	}
	// The direct HLS link was cached on the way through, so it shows as ready.
	if !videoStreamPreloaded("https://cdn/direct.m3u8") {
		t.Error("a direct stream URL should be ready without a helper run")
	}
}

// getToolPath hands back a bare name when a helper is not installed. Running
// that name would fail on every card, so preload must only trust ffprobe when
// the path is a real executable.
func TestToolPathRunnableRejectsPlaceholderNames(t *testing.T) {
	if toolPathRunnable("") {
		t.Error("an empty path reported as runnable")
	}
	if toolPathRunnable(filepath.Join(t.TempDir(), "definitely-missing-tool")) {
		t.Error("a missing tool path reported as runnable")
	}
	if toolPathRunnable(t.TempDir()) {
		t.Error("a directory reported as runnable")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	if !toolPathRunnable(exe) {
		t.Errorf("the running binary at %s reported as not runnable", exe)
	}
}

func TestVideoStreamPreloadedReflectsResolution(t *testing.T) {
	resetStreamCache()
	t.Cleanup(resetStreamCache)

	if videoStreamPreloaded("") {
		t.Fatal("an empty source reported as preloaded")
	}
	page := "https://www.youtube.com/watch?v=abc123"
	if videoStreamPreloaded(page) {
		t.Fatal("an unresolved page URL reported as preloaded")
	}
	rememberDirectStream(page, "https://cdn.example/media.mp4?expire=1")
	if !videoStreamPreloaded(page) {
		t.Fatal("a resolved stream must show as preloaded")
	}
	forgetDirectStream(page)
	if videoStreamPreloaded(page) {
		t.Fatal("forgetting the resolution must clear the mark")
	}
	// Sources ffmpeg opens directly never need yt-dlp, so they are always ready.
	if !videoStreamPreloaded("https://cdn.example/live.m3u8") {
		t.Fatal("an HLS URL must count as preloaded")
	}
}

func TestVideoPrepareLifecycle(t *testing.T) {
	cancelVideoPrepare() // a previous test may have left it active
	t.Cleanup(func() { cancelVideoPrepare() })

	seq, ctx := beginVideoPrepare("  A clip  ")
	active, title := videoPrepareStatus()
	if !active || title != "A clip" {
		t.Fatalf("status = (%v, %q), want active with a trimmed title", active, title)
	}
	if ctx.Err() != nil {
		t.Fatal("the resolution context was already cancelled")
	}
	finishVideoPrepare(seq)
	if videoPrepareActive() {
		t.Fatal("finishing did not clear the spinner")
	}

	// A slow older attempt finishing late must not clear a newer spinner.
	oldSeq, _ := beginVideoPrepare("first")
	newSeq, _ := beginVideoPrepare("second")
	finishVideoPrepare(oldSeq)
	if !videoPrepareActive() {
		t.Fatal("a stale finish cleared the newer loading state")
	}
	finishVideoPrepare(newSeq)
	if videoPrepareActive() {
		t.Fatal("the current finish did not clear the loading state")
	}

	// B / Esc cancels the in-flight resolution and unblocks navigation.
	seq, ctx = beginVideoPrepare("cancelled")
	if !cancelVideoPrepare() {
		t.Fatal("cancel reported nothing in flight")
	}
	if ctx.Err() == nil {
		t.Fatal("cancel did not cancel the resolution context")
	}
	if videoPrepareActive() {
		t.Fatal("cancel left the spinner up")
	}
	if cancelVideoPrepare() {
		t.Fatal("a second cancel reported work in flight")
	}
	finishVideoPrepare(seq) // harmless after a cancel
}

// A preloaded card draws the bolt and a yellow title; a cold one keeps the
// normal title colour. This is the only place the marker is user-visible.
func TestSearchResultsMarkPreloadedCard(t *testing.T) {
	f := newProbeFrame(t)
	r, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(r)
	resetStreamCache()
	t.Cleanup(resetStreamCache)

	ready := VideoInfo{Title: "Preloaded clip", URL: "https://www.youtube.com/watch?v=ready"}
	cold := VideoInfo{Title: "Cold clip", URL: "https://www.youtube.com/watch?v=cold"}
	rememberDirectStream(ready.URL, "https://cdn.example/ready.mp4")

	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{"videos": []VideoInfo{ready, cold}}
	el := Element{Type: "searchresults", Variable: "videos", Columns: 1, Rows: 6, X: 0, Y: 0, Width: "1200", Height: "600", Font: "small"}

	r.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	r.Clear()
	renderSearchResults(r, cfg, el)
	if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}

	// Rows are 100px tall with a 14px gap, so row 0 is y 0..100 and row 1 is
	// y 114..214. Only the preloaded row may use the warning yellow.
	readyPixels := f.countColor(ColorWarning, 0, 0, 1100, 100)
	coldPixels := f.countColor(ColorWarning, 0, 114, 1100, 214)
	if readyPixels < 40 {
		t.Errorf("preloaded row has %d marker/yellow pixels, want the bolt and title", readyPixels)
	}
	if coldPixels != 0 {
		t.Errorf("cold row painted %d preload-yellow pixels", coldPixels)
	}
}

// While a video is being resolved the shared loading card names it and says how
// to get out, instead of showing a generic status line.
func TestLoadingCardShowsPreparingVideo(t *testing.T) {
	f := newProbeFrame(t)
	r, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(r)

	seq, _ := beginVideoPrepare("A clip title that is long enough to be ellipsized inside the card")
	defer finishVideoPrepare(seq)

	r.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	r.Clear()
	renderLoadingCard(r, &Config{})
	if err := r.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}

	// The status line and the cancel hint are the bright grey text tones; the
	// card itself is much darker.
	statusText := sdl.Color{R: 226, G: 234, B: 246}
	if got := f.countColor(statusText, 0, 0, probeW-1, probeH-1); got < 20 {
		t.Errorf("status line painted %d pixels, want the video's title", got)
	}
	hintText := sdl.Color{R: 150, G: 162, B: 184}
	if got := f.countColor(hintText, 0, 0, probeW-1, probeH-1); got < 20 {
		t.Errorf("cancel hint painted %d pixels, want a visible B / Esc hint", got)
	}
}

// The player's second probe of the same remote URL must not hit the network.
func TestProbeCacheServesRemoteSourcesOnly(t *testing.T) {
	resetProbeCache()
	t.Cleanup(resetProbeCache)
	remote := "https://cdn.example/media.mp4?expire=1"
	rememberProbe(remote, probeFields{width: 640, height: 360, duration: 12, fps: 30})
	got, ok := cachedProbe(remote)
	if !ok || got.width != 640 || got.height != 360 {
		t.Fatalf("cachedProbe = %+v, %v", got, ok)
	}
	if _, ok := cachedProbe("/mnt/SDCARD/movie.mp4"); ok {
		t.Fatal("local files must be probed again, not cached")
	}
	rememberProbe("/mnt/SDCARD/movie.mp4", probeFields{width: 640, height: 360})
	if _, ok := cachedProbe("/mnt/SDCARD/movie.mp4"); ok {
		t.Fatal("rememberProbe cached a local path")
	}
	// Dimensionaless probes are not worth caching: the player would scale from
	// made-up defaults.
	rememberProbe("https://cdn.example/audio.m4a", probeFields{duration: 30})
	if _, ok := cachedProbe("https://cdn.example/audio.m4a"); ok {
		t.Fatal("cached a probe with no video dimensions")
	}
}
