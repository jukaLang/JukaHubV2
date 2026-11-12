package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func httpGetText(url string, timeout time.Duration) string {
	return httpGetTextContext(context.Background(), url, timeout)
}

// httpGetTextContext is httpGetText tied to a caller's context. Scene work uses
// it so leaving the screen closes the request instead of waiting out the
// timeout; the timeout still bounds a request nobody cancels.
func httpGetTextContext(ctx context.Context, url string, timeout time.Duration) string {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "JukaHub/0.4")
	// Podcast feeds and quote endpoints rarely change between screen visits,
	// so revalidate instead of downloading them again.
	applyConditionalHeaders(req, url)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if body, ok := conditionalBody(url); ok {
			return string(body)
		}
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return ""
	}
	storeConditionalResponse(url, resp, data)
	return string(data)
}

// hasMediaExtension reports whether a URL/path points at a directly playable
// media file (ignoring any query string).
func hasMediaExtension(s string) bool {
	u := s
	if i := strings.Index(s, "?"); i >= 0 {
		u = s[:i]
	}
	ext := strings.ToLower(filepath.Ext(u))
	for _, e := range mediaExtensions {
		if e == ext {
			return true
		}
	}
	return false
}

// playStream plays a direct stream URL (rtsp://, http://, ...) in the
// embedded player. ffmpeg reads the stream itself; ffplay is no longer used
// because the bundled binary needs X11, which the handheld does not have.
func playStream(config *Config, url string) {
	recordPlayed(config, url)
	logDebug("morefeatures", "playStream: %s", sanitizeLogURL(url))
	ensureVideoScene(config)
	setPlaybackPageURL(url)
	go func() {
		// Resolve (and install, if needed) before playing: a missing ffmpeg
		// shows the install card instead of failing silently in the decoder.
		if _, err := helperToolPath("ffmpeg", config); err != nil {
			logError("morefeatures", "playStream: ffmpeg unavailable: %v", err)
			showToast(toolUnavailableMessage("ffmpeg", err), ToastError())
			return
		}
		if _, err := helperToolPath("ffprobe", config); err != nil {
			logDebug("morefeatures", "playStream: ffprobe unavailable, using defaults: %v", err)
		}
		StartEmbeddedPlayback(config, url)
	}()
}

// playSmartURL plays a URL: directly if it is a media file, otherwise via
// yt-dlp (which handles YouTube, Twitter, TikTok, Instagram, ...).
func playSmartURL(config *Config, url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	if isAudioFile(url) || isAudioURL(url) {
		openAudioPlayer(config, url)
		return
	}
	if hasMediaExtension(url) {
		playStream(config, url)
		return
	}
	// Sanity-check the URL before handing it to yt-dlp (CodeQL #40). We only
	// allow http/https URLs here; local paths are handled by playStream.
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		// Never log the raw URL: it can carry tokens in its query string.
		logWarn("morefeatures", "security: playSmartURL: rejecting non-http URL %q", sanitizeLogURL(url))
		return
	}
	if strings.ContainsAny(url, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\n\r") {
		logWarn("morefeatures", "security: playSmartURL: rejecting URL with control characters %q", sanitizeLogURL(url))
		return
	}
	if len(url) > 2048 {
		logWarn("morefeatures", "security: playSmartURL: rejecting overlong URL (%d bytes)", len(url))
		return
	}
	// Validate the extra args the same way executeYouTubeSearch does, so a
	// malicious cookies_file / api_key value cannot alter the invocation.
	if err := validateYtDlpArgs(ytDlpExtraArgsSlice(config)); err != nil {
		logWarn("morefeatures", "security: playSmartURL: rejecting unsafe extra args: %v", err)
		return
	}
	// playVideoInfo downloads with yt-dlp and decodes in the embedded player,
	// which is the same path YouTube search results use.
	v := VideoInfo{URL: url}
	playVideoInfo(config, v)
	// A bare link has no title: name a YouTube video in Recents instead of
	// showing its address (ytoembed.go).
	ytFillMissingTitle(v)
}

// Live TV playlist parsing, categorisation, search and favorites live in
// tvchannels.go (loadIPTV is the scene entry point).

// ---------------------------------------------------------------------------
// Podcasts (RSS)
// ---------------------------------------------------------------------------

func rssTag(block, tag string) string {
	re := regexp.MustCompile(`(?is)<` + tag + `[^>]*>([^<]*)</` + tag + `>`)
	if m := re.FindStringSubmatch(block); m != nil {
		return strings.TrimSpace(stripCDATA(m[1]))
	}
	return ""
}

func rssEnclosureURL(block string) string {
	if m := regexp.MustCompile(`(?is)<enclosure[^>]*\burl\s*=\s*["']([^"']+)["']`).FindStringSubmatch(block); m != nil {
		return m[1]
	}
	if m := regexp.MustCompile(`(?is)<media:[^>]*\burl\s*=\s*["']([^"']+)["']`).FindStringSubmatch(block); m != nil {
		return m[1]
	}
	return ""
}

func stripCDATA(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<![CDATA[")
	s = strings.TrimSuffix(s, "]]>")
	return strings.TrimSpace(s)
}

func parseRSSFeed(url string) []FileEntry {
	return parseRSSFeedContext(context.Background(), url)
}

// parseRSSFeedContext reads one feed under the caller's context, so leaving
// the podcasts screen stops the remaining feeds instead of finishing all four.
func parseRSSFeedContext(ctx context.Context, url string) []FileEntry {
	// Falls back to the last-known-good copy when the network is unreachable,
	// so an offline boot still lists the feed instead of an empty screen.
	data := httpGetTextFallbackContext(ctx, url, 12*time.Second)
	if data == "" {
		return nil
	}
	re := regexp.MustCompile(`(?is)<item\b[^>]*>(.*?)</item>`)
	var items []FileEntry
	for _, m := range re.FindAllStringSubmatch(data, -1) {
		block := m[1]
		title := rssTag(block, "title")
		enc := rssEnclosureURL(block)
		if title == "" || enc == "" {
			continue
		}
		items = append(items, FileEntry{Name: title, Path: enc})
	}
	return items
}

var curatedPodcasts = []string{
	"https://feeds.npr.org/510289/podcast.xml",
	"https://feeds.npr.org/510310/podcast.xml",
	"https://feeds.acast.com/public/shows/the-daily",
	"https://feeds.megaphone.fm/vergecast",
}

// loadPodcasts loads the curated feeds for the podcasts scene. It runs as
// scene work: four serial feeds can take half a minute on a weak connection,
// and none of them should survive the screen that asked for them.
func loadPodcasts(config *Config) {
	runSceneTask(func(ctx context.Context) {
		var entries []FileEntry
		for _, feed := range curatedPodcasts {
			if ctx.Err() != nil {
				return
			}
			entries = append(entries, parseRSSFeedContext(ctx, feed)...)
		}
		if ctx.Err() != nil {
			return
		}
		publishCustom("podcast_entries", entries)
		focusedFileIndex = 0
	})
}

func loadPodcastURL(config *Config, url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	publishCustom("podcast_entries", parseRSSFeed(url))
	focusedFileIndex = 0
}

// ---------------------------------------------------------------------------
// Crypto & Stock tickers
// ---------------------------------------------------------------------------

func fetchTickerPrice(ctx context.Context, symbol string) (float64, bool) {
	url := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s", strings.ToUpper(strings.TrimSpace(symbol)))
	txt := httpGetTextContext(ctx, url, 8*time.Second)
	var ya struct {
		Chart struct {
			Result []struct {
				Meta struct {
					RegularMarketPrice float64
				}
			}
		}
	}
	if json.Unmarshal([]byte(txt), &ya) == nil && len(ya.Chart.Result) > 0 {
		return ya.Chart.Result[0].Meta.RegularMarketPrice, true
	}
	return 0, false
}

// customTickerList is the saved symbol list. Loaded back from jukauser.json it
// is a []interface{}, not the []string this session wrote, so the old
// assertion found nothing after a restart: the saved symbols vanished from the
// board and the next "add" replaced the whole list with one entry.
func customTickerList(config *Config) []string {
	if config == nil {
		return nil
	}
	switch v := config.Variables.Custom["custom_tickers"].(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// fetchTickers refreshes the crypto/stock board for the scene that asked for
// it. The endpoints are independent, so they share one round trip instead of
// three serial ones; a failed endpoint is data ("n/a"), not a reason to stop
// the others. Leaving the scene is.
func fetchTickers(config *Config) {
	if config == nil {
		return
	}
	// Read on the calling (render) thread: the task runs on its own goroutine,
	// and reading the live Custom map there raced every write to it.
	custom := customTickerList(config)
	runSceneTask(func(ctx context.Context) {
		symbols := make([]string, 0, len(custom))
		for _, raw := range custom {
			if sym := strings.TrimSpace(raw); sym != "" {
				symbols = append(symbols, sym)
			}
		}

		// Each fetch owns one index, so the board keeps its order (BTC/ETH,
		// AAPL, then the user's symbols) whether or not the network is slow.
		// The two fixed rows are allocated, then one slot is added per symbol:
		// `2+len(symbols)` as a length is a sum the analyser cannot bound, and
		// this keeps the same length without ever forming it.
		lines := make([]string, 2)
		for range symbols {
			lines = append(lines, "")
		}
		fns := make([]func(context.Context) error, 0, len(lines))
		fns = append(fns, func(ctx context.Context) error {
			txt := httpGetTextContext(ctx, "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin,ethereum&vs_currencies=usd", 8*time.Second)
			var cg map[string]map[string]float64
			if json.Unmarshal([]byte(txt), &cg) != nil {
				return nil
			}
			var sb strings.Builder
			if b, ok := cg["bitcoin"]["usd"]; ok {
				sb.WriteString(fmt.Sprintf("BTC: $%.0f\n", b))
			}
			if e, ok := cg["ethereum"]["usd"]; ok {
				sb.WriteString(fmt.Sprintf("ETH: $%.0f\n", e))
			}
			lines[0] = sb.String()
			return nil
		}, func(ctx context.Context) error {
			y := httpGetTextContext(ctx, "https://query1.finance.yahoo.com/v8/finance/chart/AAPL", 8*time.Second)
			var ya struct {
				Chart struct {
					Result []struct {
						Meta struct {
							RegularMarketPrice float64
						}
					}
				}
			}
			if json.Unmarshal([]byte(y), &ya) == nil && len(ya.Chart.Result) > 0 {
				lines[1] = fmt.Sprintf("AAPL: $%.2f\n", ya.Chart.Result[0].Meta.RegularMarketPrice)
			}
			return nil
		})
		for i, sym := range symbols {
			index := 2 + i
			symbol := sym
			fns = append(fns, func(ctx context.Context) error {
				if p, ok := fetchTickerPrice(ctx, symbol); ok {
					lines[index] = fmt.Sprintf("%s: $%.2f\n", strings.ToUpper(symbol), p)
				} else {
					lines[index] = fmt.Sprintf("%s: n/a\n", strings.ToUpper(symbol))
				}
				return nil
			})
		}
		if err := runCancelableGroup(ctx, fns...); err != nil {
			// The scene was left; a publish now would land on the next screen.
			return
		}
		if ctx.Err() != nil {
			return
		}
		var sb strings.Builder
		for _, line := range lines {
			sb.WriteString(line)
		}
		if sb.Len() == 0 {
			sb.WriteString("Unable to fetch tickers (offline?)")
		}
		publishCustom("ticker_text", sb.String())
	})
}

// ---------------------------------------------------------------------------
// Network Speed Meter
// ---------------------------------------------------------------------------

const netSpeedSampleBytes = 5 * 1024 * 1024

var (
	netSpeedRunActive atomic.Bool
	netSpeedHTTP      = &http.Client{Timeout: 20 * time.Second}
	netSpeedURL       = "https://cachefly.cachefly.net/100mb.test"
)

// measureNetSpeed reads a bounded download sample and rejects HTTP errors and
// short responses instead of reporting an error page as a speed measurement.
func measureNetSpeed(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, netSpeedSampleBytes))
	if err != nil {
		return "", err
	}
	if n != netSpeedSampleBytes {
		return "", fmt.Errorf("download ended early (%d of %d bytes)", n, netSpeedSampleBytes)
	}
	dur := time.Since(start).Seconds()
	if dur <= 0 {
		dur = 0.001
	}
	mbps := (float64(n) * 8 / dur) / 1e6
	trophyEvent(tkSpeedTest)
	trophyEventPeak(tkSpeedBest, int64(mbps))
	return fmt.Sprintf("Downloaded %.1f MiB in %.2fs\nSpeed: %.1f Mbit/s", float64(n)/(1024*1024), dur, mbps), nil
}

// Cancelled runs do not publish into a scene the user has left.
func resetNetSpeedOnSceneEntry(config *Config, scene SceneConfig) {
	for _, element := range scene.Elements {
		if element.Type == "textlog" && element.Variable == "netspeed_text" {
			if text, _ := config.Variables.Custom["netspeed_text"].(string); strings.HasPrefix(text, "Measuring download speed…") {
				delete(config.Variables.Custom, "netspeed_text")
			}
			return
		}
	}
}

func runNetSpeed(config *Config) {
	if !netSpeedRunActive.CompareAndSwap(false, true) {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom["netspeed_text"] = "Measuring download speed…\nDownloading a 5 MiB sample. Please wait."
	started := runSceneTask(func(ctx context.Context) {
		defer netSpeedRunActive.Store(false)
		result, err := measureNetSpeed(ctx, netSpeedHTTP, netSpeedURL)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			result = "Speed test failed: " + err.Error()
		}
		publishCustom("netspeed_text", result)
	})
	if !started {
		netSpeedRunActive.Store(false)
		config.Variables.Custom["netspeed_text"] = "Speed test could not start. Press Run to try again."
	}
}

// ---------------------------------------------------------------------------
// Read/Write Benchmark
// ---------------------------------------------------------------------------

// benchOutcome is one phase's measured throughput, in MB/s.
type benchOutcome struct {
	write float64
	read  float64
}

// benchProgress is where a phase has got to: the measurement's name and the
// bytes it has moved so far. The screen used to sit on "Running benchmark…"
// until both phases finished, which on a slow card is many seconds of a panel
// that looks hung.
type benchProgress struct {
	label string
	done  int64
	total int64
}

// benchProgressKey carries the phase's progress sink on the run's context, so
// the phase functions can report without knowing where the text is drawn, and
// a phase run without a screen (a test) simply reports nothing.
type benchProgressKey struct{}

func withBenchProgress(ctx context.Context, sink func(benchProgress)) context.Context {
	return context.WithValue(ctx, benchProgressKey{}, sink)
}

// reportBenchProgress publishes one step of the current phase. Phases call it
// per block they move; callers that do not want the traffic can drop the sink.
func reportBenchProgress(ctx context.Context, label string, done, total int64) {
	sink, _ := ctx.Value(benchProgressKey{}).(func(benchProgress))
	if sink == nil {
		return
	}
	sink(benchProgress{label: label, done: done, total: total})
}

// benchProgressText is the panel's live line while a phase is running. It keeps
// the same first line as the idle message so the panel does not jump.
func benchProgressText(p benchProgress) string {
	pct := 100
	if p.total > 0 {
		pct = int(p.done * 100 / p.total)
		if pct > 100 {
			pct = 100
		}
	}
	if pct < 0 {
		pct = 0
	}
	return fmt.Sprintf("Running benchmark…\n%s: %d%%", p.label, pct)
}

// benchSequential writes a 50 MB file as one stream and reads it back, which is
// the shape of a large file copy.
func benchSequential(ctx context.Context) (benchOutcome, error) {
	path := ".jukabench.tmp"
	f, err := os.Create(path)
	if err != nil {
		return benchOutcome{}, err
	}
	discard := func() {
		f.Close()
		os.Remove(path)
	}
	sz := 50 * 1024 * 1024
	buf := make([]byte, 1024*1024)
	for i := range buf {
		buf[i] = byte(i)
	}
	start := time.Now()
	for written := 0; written < sz; written += len(buf) {
		if ctx.Err() != nil {
			// Leaving the screen must not leave a 50 MB file behind.
			discard()
			return benchOutcome{}, ctx.Err()
		}
		f.Write(buf)
		reportBenchProgress(ctx, "Sequential write", int64(written+len(buf)), int64(sz))
	}
	f.Sync()
	d1 := time.Since(start)
	if ctx.Err() != nil {
		discard()
		return benchOutcome{}, ctx.Err()
	}
	f.Seek(0, 0)
	r := bufio.NewReader(f)
	start = time.Now()
	var read int64
	for {
		if ctx.Err() != nil {
			discard()
			return benchOutcome{}, ctx.Err()
		}
		n, e := r.Read(buf)
		if n > 0 {
			read += int64(n)
			reportBenchProgress(ctx, "Sequential read", read, int64(sz))
		}
		if e != nil {
			break
		}
	}
	d2 := time.Since(start)
	discard()
	if ctx.Err() != nil {
		return benchOutcome{}, ctx.Err()
	}
	return benchOutcome{
		write: float64(sz) / d1.Seconds() / 1e6,
		read:  float64(sz) / d2.Seconds() / 1e6,
	}, nil
}

// benchRandom performs random-access I/O benchmarking: it writes 10 MB of
// random data to a 50 MB file at spread-out offsets, then reads 10 MB of
// random positions, which is the shape of many small files.
func benchRandom(ctx context.Context) (benchOutcome, error) {
	const fileSize = 50 * 1024 * 1024
	const ioBytes = 10 * 1024 * 1024
	blockSize := 4 * 1024
	path := ".jukarandbench.tmp"

	f, err := os.Create(path)
	if err != nil {
		return benchOutcome{}, err
	}
	if err := f.Truncate(fileSize); err != nil {
		f.Close()
		os.Remove(path)
		return benchOutcome{}, err
	}
	discard := func() {
		f.Close()
		os.Remove(path)
	}

	rnd := make([]byte, blockSize)
	offsets := make([]int64, ioBytes/blockSize)
	for i := range offsets {
		offsets[i] = int64(i) * int64(fileSize) / int64(len(offsets))
	}

	start := time.Now()
	for i, off := range offsets {
		if ctx.Err() != nil {
			discard()
			return benchOutcome{}, ctx.Err()
		}
		if _, err := rand.Read(rnd); err != nil {
			rnd = []byte{byte(off), byte(off >> 8), byte(off >> 16)}
		}
		if _, err := f.WriteAt(rnd, off); err != nil {
			discard()
			return benchOutcome{}, fmt.Errorf("write: %w", err)
		}
		reportBenchProgress(ctx, "Random write", int64(i+1)*int64(blockSize), ioBytes)
	}
	f.Sync()
	dWrite := time.Since(start)
	if ctx.Err() != nil {
		discard()
		return benchOutcome{}, ctx.Err()
	}

	readBuf := make([]byte, blockSize)
	start = time.Now()
	for i, off := range offsets {
		if ctx.Err() != nil {
			discard()
			return benchOutcome{}, ctx.Err()
		}
		if _, err := f.ReadAt(readBuf, off); err != nil {
			discard()
			return benchOutcome{}, fmt.Errorf("read: %w", err)
		}
		reportBenchProgress(ctx, "Random read", int64(i+1)*int64(blockSize), ioBytes)
	}
	dRead := time.Since(start)

	discard()
	if ctx.Err() != nil {
		return benchOutcome{}, ctx.Err()
	}
	return benchOutcome{
		write: float64(ioBytes) / dWrite.Seconds() / 1e6,
		read:  float64(ioBytes) / dRead.Seconds() / 1e6,
	}, nil
}

// benchPhases are the two measurements a Run performs. They are variables so
// the screen's combined run can be tested without writing 100 MB per case.
var (
	benchSequentialPhase = benchSequential
	benchRandomPhase     = benchRandom
)

// benchRunActive keeps two measurements from racing on one card. The screen
// starts measuring as it opens, so a Run press on the way in would otherwise
// put a second 100 MB pass on top of the first and the two would report each
// other's contention as the device's speed.
var benchRunActive atomic.Bool

// benchRunLabel is what the screen's one Run button says while a measurement is
// in flight, in place of its configured text.
const benchRunLabel = "Running…"

// benchmarkButtonFace is the label and the busy flag a scene button draws with.
// The Benchmark and Net Speed Run buttons are busy during a measurement —
// which on a slow card is seconds — so it is drawn disabled and relabelled
// instead of sitting there looking like a second press would do something. The
// render path and handleTrigger both read this one rule.
func benchmarkButtonFace(elem Element) (label string, busy bool) {
	if elem.Type == "button" && ((elem.Trigger == "benchmark_run" && benchRunActive.Load()) ||
		(elem.Trigger == "netspeed_run" && netSpeedRunActive.Load())) {
		return benchRunLabel, true
	}
	return elem.Text, false
}

// runBenchmark measures both phases from the screen's one Run press. They used
// to be two buttons — Run and Random — so a run said only half the story and the
// random phase was measured only if someone remembered the second button. Both
// phases now run automatically, one after the other, and the panel shows all
// four numbers (or which phase failed).
//
// The screen also starts this on entry, so the panel is measuring the moment it
// appears. Each phase publishes its own progress line as it moves its bytes.
func runBenchmark(config *Config) {
	if !benchRunActive.CompareAndSwap(false, true) {
		// Already measuring: the progress line on screen is the answer.
		return
	}
	started := runSceneTask(func(ctx context.Context) {
		// The Benchmark screen draws the same run from structured progress
		// (systool_bench.go); the text below is for a config-driven panel.
		// The end is reported after the run flag is free, so Run again works
		// the moment the screen says the run is over.
		benchRun := sysToolBenchBegin()
		defer func() { sysToolBenchEnd(benchRun, ctx.Err() != nil) }()
		defer benchRunActive.Store(false)
		var lastPublish time.Time
		ctx = withBenchProgress(ctx, func(p benchProgress) {
			if ctx.Err() != nil {
				return
			}
			sysToolBenchStep(p)
			// The panel is a string in the text cache: repainting it on every
			// megabyte would miss the cache dozens of times a second. Four
			// updates per second read as live and cost almost nothing to draw.
			if p.done < p.total && time.Since(lastPublish) < 250*time.Millisecond {
				return
			}
			lastPublish = time.Now()
			publishCustom("benchmark_text", benchProgressText(p))
		})
		publishCustom("benchmark_text", "Running benchmark…")
		lines := make([]string, 0, 4)

		seq, err := benchSequentialPhase(ctx)
		if ctx.Err() != nil {
			return
		}
		sysToolBenchPhase(true, seq, err)
		if err != nil {
			lines = append(lines, "Sequential: "+err.Error())
		} else {
			if recordErr := recordPowerBenchmark("sequential", seq.write, seq.read, config.Version); recordErr != nil {
				logWarn("bench", "could not record result: %v", recordErr)
			}
			lines = append(lines,
				fmt.Sprintf("Write: %.1f MB/s", seq.write),
				fmt.Sprintf("Read:  %.1f MB/s", seq.read))
		}

		rnd, err := benchRandomPhase(ctx)
		if ctx.Err() != nil {
			return
		}
		sysToolBenchPhase(false, rnd, err)
		if err != nil {
			lines = append(lines, "Random: "+err.Error())
		} else {
			lines = append(lines,
				fmt.Sprintf("Random write: %.1f MB/s", rnd.write),
				fmt.Sprintf("Random read:  %.1f MB/s", rnd.read))
		}

		publishCustom("benchmark_text", strings.Join(lines, "\n"))
	})
	if !started {
		// Shutting down refuses new tasks; the flag must not stay taken.
		benchRunActive.Store(false)
	}
}

// benchmarkAutoStartOnOpen measures as soon as a scene that shows the benchmark
// panel becomes active, and reports whether it did. It keys off the panel rather
// than the scene name, so a renamed scene — or a hand-edited config that keeps
// the panel — still measures, and a scene that dropped the panel does not.
func benchmarkAutoStartOnOpen(config *Config, scene SceneConfig) bool {
	for _, e := range scene.Elements {
		if e.Type == "textlog" && e.Variable == "benchmark_text" {
			runBenchmark(config)
			return true
		}
	}
	return false
}

// runRandomBenchmark is the random phase alone, kept for a hand-edited config
// that still has the old Random button.
func runRandomBenchmark(config *Config) {
	runSceneTask(func(ctx context.Context) {
		rnd, err := benchRandomPhase(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			publishCustom("benchmark_text", "Random Error: "+err.Error())
			return
		}
		publishCustom("benchmark_text", fmt.Sprintf(
			"Random Write: %.1f MB/s\nRandom Read:  %.1f MB/s", rnd.write, rnd.read))
	})
}

// ---------------------------------------------------------------------------
// Terminal
// ---------------------------------------------------------------------------

// runTerminal runs cmdStr through the shell (cmd /c, sh -c) and publishes its
// output as terminal_text. Trust boundary (SEC-12): the string is a shell
// command by design. It comes from the terminal_cmd / emulator_cmd custom
// variables the terminal_run / emulator_run triggers read (core.go), so it is
// either authored by the scene or typed by the user on the device; a design
// pushed through the Live Builder that presets either variable for those
// triggers waits for the owner's approval first (livebuilder.go,
// liveBuilderShellTriggers).
func runTerminal(config *Config, cmdStr string) {
	go func() {
		var c *exec.Cmd
		if runtime.GOOS == "windows" {
			c = exec.Command("cmd", "/c", cmdStr)
		} else {
			c = exec.Command("sh", "-c", cmdStr)
		}
		out, err := c.CombinedOutput()
		if err != nil {
			out = append(out, []byte("\n"+err.Error())...)
		}
		s := string(out)
		if len(s) > 4000 {
			s = s[len(s)-4000:]
		}
		publishCustom("terminal_text", s)
	}()
}

// ---------------------------------------------------------------------------
// Cron / Task Scheduler (read-only viewer)
// ---------------------------------------------------------------------------

// taskOutputLimit bounds command output even on machines with many tasks.
const taskOutputLimit = 2 * 1024 * 1024

type taskOutputBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *taskOutputBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := taskOutputLimit - b.Len()
	if remaining < n {
		b.truncated = true
		if remaining < 0 {
			remaining = 0
		}
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

// queryWindowsTasks runs the local, read-only Task Scheduler query. A timeout
// and bounded output keep a stalled or unusually large result off the UI thread.
func queryWindowsTasks(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "schtasks", "/query", "/fo", "CSV", "/nh")
	// No console window flashing over the UI (sched_windows.go).
	schedHideWindow(cmd)
	var output taskOutputBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
	}
	if output.truncated {
		return nil, fmt.Errorf("Task Scheduler output exceeded the 2 MiB limit")
	}
	return output.Bytes(), nil
}

// formatWindowsTasks uses field positions rather than localized column titles.
// Limit the list and say it is partial even when a large font leaves fewer
// rows visible in the textlog.
func formatWindowsTasks(data []byte) (string, error) {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	r.FieldsPerRecord = -1
	var rows []string
	count := 0
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if len(record) != 3 {
			return "", fmt.Errorf("unexpected Task Scheduler CSV row with %d fields", len(record))
		}
		count++
		if len(rows) < 10 {
			rows = append(rows, strings.Join(record, " | "))
		}
	}
	if count == 0 {
		return "No scheduled tasks returned by Windows Task Scheduler.", nil
	}
	return fmt.Sprintf("Windows Task Scheduler: %d tasks; first entries shown.\n%s", count, strings.Join(rows, "\n")), nil
}

var windowsTaskQuery = queryWindowsTasks
var windowsTaskRequest atomic.Uint64

func loadWindowsTasks(config *Config) {
	id := windowsTaskRequest.Add(1)
	config.Variables.Custom["cron_text"] = "Loading Windows scheduled tasks..."
	runSceneTask(func(ctx context.Context) {
		data, err := windowsTaskQuery(ctx)
		text := ""
		if err == nil {
			text, err = formatWindowsTasks(data)
		}
		if err != nil {
			text = "Cannot load Windows scheduled tasks: " + firstLine(err.Error())
		}
		if ctx.Err() == nil && windowsTaskRequest.Load() == id {
			publishCustom("cron_text", text)
		}
	})
}

func loadCron(config *Config) {
	if runtime.GOOS == "windows" {
		loadWindowsTasks(config)
		return
	}
	var sb strings.Builder
	if data, err := os.ReadFile("/etc/crontab"); err == nil {
		sb.WriteString("=== /etc/crontab ===\n" + string(data) + "\n")
	}
	if out, err := exec.Command("crontab", "-l").Output(); err == nil {
		sb.WriteString("=== crontab -l ===\n" + string(out) + "\n")
	}
	if fis, err := os.ReadDir("/etc/cron.d"); err == nil {
		for _, f := range fis {
			if d, err := os.ReadFile(filepath.Join("/etc/cron.d", f.Name())); err == nil {
				sb.WriteString("=== /etc/cron.d/" + f.Name() + " ===\n" + string(d) + "\n")
			}
		}
	}
	if out, err := exec.Command("systemctl", "list-timers", "--no-pager").Output(); err == nil {
		sb.WriteString("=== systemd timers ===\n" + string(out) + "\n")
	}
	if sb.Len() == 0 {
		sb.WriteString("No cron/systemd data found on this system.")
	}
	publishCustom("cron_text", sb.String())
}

// ---------------------------------------------------------------------------
// Full Weather screen text
// ---------------------------------------------------------------------------

func weatherScreenText(config *Config) string {
	wxMutex.Lock()
	ready := weatherReady
	days := append([]WeatherDay(nil), weatherDaily...)
	wxMutex.Unlock()
	if !ready || len(days) == 0 {
		// Switched off in Settings is not the same as offline: say which.
		if !weatherIsEnabled() {
			return weatherOffMessage
		}
		return "Weather unavailable (offline?)"
	}
	unit := weatherUnit(config)
	conv := func(c float64) float64 { return weatherTemp(c, unit) }
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("7-Day Forecast (%s)\n\n", unit))
	for _, d := range days {
		day := d.Date
		if t, err := time.Parse("2006-01-02", d.Date); err == nil {
			day = t.Format("Mon")
		}
		line := fmt.Sprintf("%s   %.0f° / %.0f°   %s", day, conv(d.TMax), conv(d.TMin), weatherCodeDesc(d.Code))
		if d.Pop > 0 {
			line += fmt.Sprintf("  (%d%% rain)", d.Pop)
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// textlog element (multi-line read-only display)
// ---------------------------------------------------------------------------

// tailLines returns the last max lines so a fixed-height log panel shows the
// most recent entries instead of pinning the view to the oldest ones.
func tailLines(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	return lines[len(lines)-max:]
}

// textlogEmptyState is the state card an empty variable-backed textlog draws.
// Each variable that can reach renderTextLog has its own line so a panel never
// claims to be loading something it is not: the Weather screen used to read
// "Reading hardware…" because that sentence was hardcoded for all of them.
// Variables without a specific line fall back to a neutral message.
func textlogEmptyState(variable string) (title, body string) {
	switch variable {
	case "hw_text":
		return "Reading hardware…", "Press Refresh to load a new snapshot."
	case "weather_screen_text":
		return "Loading forecast…", "Press Refresh to fetch the weather again."
	case "netspeed_text":
		return "Speed test not started.", "Press Run to measure download speed (5 MiB)."
	case "benchmark_text":
		return "Running benchmark…", "Press Run to measure the disk again."
	case "cron_text":
		return "Scheduled tasks not loaded.", "Press Refresh to scan scheduled tasks."
	case "startup_text":
		return "Startup items not loaded.", "Press Refresh or Startup to scan startup items."
	case "plugin_list":
		return "No plugins installed.", "Install a package to add plugins."
	case "terminal_text":
		return "No output yet.", "Run a command to see its output here."
	default:
		return "Nothing to show yet.", "Press Refresh to load this panel."
	}
}

func renderTextLog(renderer *sdl.Renderer, config *Config, element Element) {
	elemW := getElementWidth(element, 1100)
	elemH := getElementHeight(element, 480)
	drawPanel(renderer, element.X, element.Y, elemW, elemH, PanelFill(235), accentColor)
	if element.Variable == "weather_screen_text" {
		renderWeatherSceneBackground(renderer, config,
			sdl.Rect{X: element.X + 4, Y: element.Y + 4, W: elemW - 8, H: elemH - 8})
	}

	// The ticker board gets a dedicated, richer layout.
	if element.Variable == "ticker_text" {
		renderTickerBoard(renderer, config, element, elemW, elemH)
		return
	}

	text := ""
	if element.Variable == "log_text" {
		text = appLog.String()
	} else {
		text, _ = config.Variables.Custom[element.Variable].(string)
	}
	// An empty textlog otherwise renders as a bare panel: give it a state card
	// while its value has not arrived. The copy used to be hardcoded to
	// "Reading hardware…", which is what put that line on the Weather screen —
	// nine different variables share this path, so each gets its own words.
	if text == "" && element.Variable != "log_text" {
		title, body := textlogEmptyState(element.Variable)
		UIEmptyState(renderer, config, sdl.Rect{X: element.X, Y: element.Y, W: elemW, H: elemH},
			"@apps", title, body)
		return
	}
	font, _ := getCachedFont(config, element.Font)
	if font == nil {
		font, _ = getCachedFont(config, "small")
	}
	if font == nil {
		return
	}
	lines := strings.Split(text, "\n")
	y := element.Y + 8
	lineH := int32(22)
	if (element.Variable == "cron_text" || element.Variable == cronStartupVar) && int32(font.Height()) > lineH {
		lineH = int32(font.Height())
	}
	// The live log panel must end at the newest line; starting from the head
	// would hide recent entries behind the panel's bottom edge.
	if element.Variable == "log_text" {
		if visible := int((elemH - 16) / lineH); visible > 0 {
			lines = tailLines(lines, visible)
		}
	}
	for i, ln := range lines {
		if (element.Variable == "cron_text" || element.Variable == cronStartupVar) && y+lineH > element.Y+elemH-8 {
			break
		}
		if y > element.Y+elemH-16 {
			break
		}
		// subtle alternating row stripe
		if i%2 == 0 {
			fillRoundedRect(renderer, element.X+6, y, elemW-12, lineH-2, 4, GlossFill(4))
		}
		// severity-aware coloring
		col := ColorTextSecondary()
		lower := strings.ToLower(ln)
		switch {
		case strings.Contains(lower, "[error]") || strings.Contains(lower, "error:"):
			col = DangerInk()
		case strings.Contains(lower, "[warn]") || strings.Contains(lower, "[warning]"):
			col = WarningInk()
		case strings.Contains(lower, "[info]"):
			col = InfoInk()
		case strings.Contains(lower, "[ok]") || strings.Contains(lower, "[success]"):
			col = SuccessInk()
		}
		if element.Variable == "cron_text" || element.Variable == cronStartupVar {
			ln = ellipsize(font, ln, elemW-24)
		}
		renderText(renderer, config, font, ln, col, element.X+12, y)
		y += lineH
	}
}

// renderTickerBoard renders the crypto/stock ticker values as a grid of cards,
// each showing the symbol and its current price in a clean, readable layout.
func renderTickerBoard(renderer *sdl.Renderer, config *Config, element Element, elemW, elemH int32) {
	text, _ := config.Variables.Custom[element.Variable].(string)
	lines := strings.Split(strings.TrimSpace(text), "\n")

	symFont, _ := getCachedFont(config, "medium")
	priceFont, _ := getCachedFont(config, "large")
	if priceFont == nil {
		priceFont = symFont
	}
	if symFont == nil {
		return
	}

	// Single status/error line (no symbol:value pairs) -> centered message.
	if len(lines) == 1 && !strings.Contains(lines[0], ":") {
		renderText(renderer, config, symFont, lines[0], ColorTextTertiary(), element.X+16, element.Y+elemH/2-10)
		return
	}

	cols := int32(2)
	cardW := (elemW - 30) / cols
	cardH := int32(96)
	gap := int32(10)
	for i, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		col := int32(i) % cols
		row := int32(i) / cols
		cx := element.X + 10 + col*(cardW+gap)
		cy := element.Y + 10 + row*(cardH+gap)
		drawCard(renderer, cx, cy, cardW, cardH, 12)

		sym := ln
		val := ""
		if idx := strings.Index(ln, ":"); idx >= 0 {
			sym = strings.TrimSpace(ln[:idx])
			val = strings.TrimSpace(ln[idx+1:])
		}
		renderText(renderer, config, symFont, sym, ColorTextAccent(), cx+16, cy+14)

		valColor := ColorTextPrimary()
		if strings.Contains(strings.ToLower(val), "n/a") {
			valColor = ColorTextTertiary()
		}
		if priceFont != nil {
			renderText(renderer, config, priceFont, val, valColor, cx+16, cy+cardH-30)
		}
	}
}
