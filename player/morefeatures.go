package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
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
	log.Printf("playStream: %s", sanitizeLogURL(url))
	ensureVideoScene(config)
	currentPlaybackURL = url
	go func() {
		// Resolve (and install, if needed) before playing: a missing ffmpeg
		// shows the install card instead of failing silently in the decoder.
		if _, err := helperToolPath("ffmpeg", config); err != nil {
			log.Printf("[ERROR] playStream: ffmpeg unavailable: %v", err)
			showToast(toolUnavailableMessage("ffmpeg", err), ToastError())
			return
		}
		if _, err := helperToolPath("ffprobe", config); err != nil {
			log.Printf("[DEBUG] playStream: ffprobe unavailable, using defaults: %v", err)
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
	if hasMediaExtension(url) {
		playStream(config, url)
		return
	}
	// Sanity-check the URL before handing it to yt-dlp (CodeQL #40). We only
	// allow http/https URLs here; local paths are handled by playStream.
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		// Never log the raw URL: it can carry tokens in its query string.
		log.Printf("[SECURITY] playSmartURL: rejecting non-http URL %q", sanitizeLogURL(url))
		return
	}
	if strings.ContainsAny(url, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\n\r") {
		log.Printf("[SECURITY] playSmartURL: rejecting URL with control characters %q", sanitizeLogURL(url))
		return
	}
	if len(url) > 2048 {
		log.Printf("[SECURITY] playSmartURL: rejecting overlong URL (%d bytes)", len(url))
		return
	}
	// Validate the extra args the same way executeYouTubeSearch does, so a
	// malicious cookies_file / api_key value cannot alter the invocation.
	if err := validateYtDlpArgs(ytDlpExtraArgsSlice(config)); err != nil {
		log.Printf("[SECURITY] playSmartURL: rejecting unsafe extra args: %v", err)
		return
	}
	// playVideoInfo downloads with yt-dlp and decodes in the embedded player,
	// which is the same path YouTube search results use.
	playVideoInfo(config, VideoInfo{URL: url})
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

// fetchTickers refreshes the crypto/stock board for the scene that asked for
// it. The endpoints are independent, so they share one round trip instead of
// three serial ones; a failed endpoint is data ("n/a"), not a reason to stop
// the others. Leaving the scene is.
func fetchTickers(config *Config) {
	runSceneTask(func(ctx context.Context) {
		if config == nil {
			return
		}
		custom, _ := config.Variables.Custom["custom_tickers"].([]string)
		symbols := make([]string, 0, len(custom))
		for _, raw := range custom {
			if sym := strings.TrimSpace(raw); sym != "" {
				symbols = append(symbols, sym)
			}
		}

		// Each fetch owns one index, so the board keeps its order (BTC/ETH,
		// AAPL, then the user's symbols) whether or not the network is slow.
		lines := make([]string, 2+len(symbols))
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

func runNetSpeed(config *Config) {
	runSceneTask(func(ctx context.Context) {
		url := "https://cachefly.cachefly.net/100mb.test"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return
		}
		start := time.Now()
		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() == nil {
				publishCustom("netspeed_text", "Error: "+err.Error())
			}
			return
		}
		n, copyErr := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if ctx.Err() != nil {
			// The test was cancelled: report nothing, the screen is gone.
			return
		}
		if copyErr != nil {
			publishCustom("netspeed_text", "Error: "+copyErr.Error())
			return
		}
		dur := time.Since(start).Seconds()
		if dur <= 0 {
			dur = 0.001
		}
		mbps := (float64(n) * 8 / dur) / 1e6
		publishCustom("netspeed_text", fmt.Sprintf(
			"Downloaded %.1f MB in %.2fs\nSpeed: %.1f Mbit/s", float64(n)/1e6, dur, mbps))
	})
}

// ---------------------------------------------------------------------------
// Read/Write Benchmark
// ---------------------------------------------------------------------------

func runBenchmark(config *Config) {
	runSceneTask(func(ctx context.Context) {
		path := ".jukabench.tmp"
		f, err := os.Create(path)
		if err != nil {
			if ctx.Err() == nil {
				publishCustom("benchmark_text", "Error: "+err.Error())
			}
			return
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
				return
			}
			f.Write(buf)
		}
		f.Sync()
		d1 := time.Since(start)
		if ctx.Err() != nil {
			discard()
			return
		}
		f.Seek(0, 0)
		r := bufio.NewReader(f)
		start = time.Now()
		for {
			if ctx.Err() != nil {
				discard()
				return
			}
			_, e := r.Read(buf)
			if e != nil {
				break
			}
		}
		d2 := time.Since(start)
		discard()
		if ctx.Err() != nil {
			return
		}
		writeMB := float64(sz) / d1.Seconds() / 1e6
		readMB := float64(sz) / d2.Seconds() / 1e6
		if err := recordPowerBenchmark("sequential", writeMB, readMB, config.Version); err != nil {
			log.Printf("[BENCH] could not record result: %v", err)
		}
		publishCustom("benchmark_text", fmt.Sprintf(
			"Write: %.1f MB/s\nRead:  %.1f MB/s", writeMB, readMB))
	})
}

// runRandomBenchmark performs random-access I/O benchmarking: it writes
// 10 MB of random data to a temp file at random offsets, then reads 10 MB
// of random positions, reporting MB/s for each phase.
func runRandomBenchmark(config *Config) {
	runSceneTask(func(ctx context.Context) {
		const fileSize = 50 * 1024 * 1024
		const ioBytes = 10 * 1024 * 1024
		blockSize := 4 * 1024
		path := ".jukarandbench.tmp"

		f, err := os.Create(path)
		if err != nil {
			if ctx.Err() == nil {
				publishCustom("benchmark_text", "Random Error: "+err.Error())
			}
			return
		}
		if err := f.Truncate(fileSize); err != nil {
			f.Close()
			os.Remove(path)
			if ctx.Err() == nil {
				publishCustom("benchmark_text", "Random Error: "+err.Error())
			}
			return
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
		for _, off := range offsets {
			if ctx.Err() != nil {
				discard()
				return
			}
			if _, err := rand.Read(rnd); err != nil {
				rnd = []byte{byte(off), byte(off >> 8), byte(off >> 16)}
			}
			if _, err := f.WriteAt(rnd, off); err != nil {
				discard()
				publishCustom("benchmark_text", "Random Write Error: "+err.Error())
				return
			}
		}
		f.Sync()
		dWrite := time.Since(start)
		if ctx.Err() != nil {
			discard()
			return
		}

		readBuf := make([]byte, blockSize)
		start = time.Now()
		for _, off := range offsets {
			if ctx.Err() != nil {
				discard()
				return
			}
			if _, err := f.ReadAt(readBuf, off); err != nil {
				discard()
				publishCustom("benchmark_text", "Random Read Error: "+err.Error())
				return
			}
		}
		dRead := time.Since(start)

		discard()
		if ctx.Err() != nil {
			return
		}
		publishCustom("benchmark_text", fmt.Sprintf(
			"Random Write: %.1f MB/s\nRandom Read:  %.1f MB/s",
			float64(ioBytes)/dWrite.Seconds()/1e6, float64(ioBytes)/dRead.Seconds()/1e6))
	})
}

// ---------------------------------------------------------------------------
// Terminal
// ---------------------------------------------------------------------------

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

func loadCron(config *Config) {
	if runtime.GOOS == "windows" {
		publishCustom("cron_text", "Cron is not available on Windows.")
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
// Unzip (.zip files in File Explorer)
// ---------------------------------------------------------------------------

func unzipFile(src string) error {
	// Only archives selected inside the browsable File Explorer tree are opened.
	resolved, err := resolveExplorerEntry(appConfig, src)
	if err != nil {
		return err
	}
	src = resolved
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	dest := strings.TrimSuffix(src, filepath.Ext(src)) + "_unzipped"
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	destPrefix := filepath.Clean(dest) + string(os.PathSeparator)
	for _, f := range r.File {
		// Mitigate Zip Slip: refuse entries whose cleaned path escapes dest.
		cleanedP := filepath.Join(destPrefix, filepath.Clean(f.Name))
		if !strings.HasPrefix(cleanedP, destPrefix) {
			rc, openErr := f.Open()
			if openErr == nil {
				rc.Close()
			}
			return fmt.Errorf("refusing to extract zip entry with unsafe path %q", f.Name)
		}

		p := cleanedP
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		out, err := os.Create(p)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
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
		return "Measuring speed…", "Press Refresh to run a new sample."
	case "benchmark_text":
		return "Running benchmark…", "Press Refresh to start it again."
	case "cron_text", "startup_text":
		return "Nothing scheduled.", "Press Refresh to rescan the crontab."
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
			"·", title, body)
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
	// The live log panel must end at the newest line; starting from the head
	// would hide recent entries behind the panel's bottom edge.
	if element.Variable == "log_text" {
		if visible := int((elemH - 16) / lineH); visible > 0 {
			lines = tailLines(lines, visible)
		}
	}
	for i, ln := range lines {
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
			col = ColorDanger
		case strings.Contains(lower, "[warn]") || strings.Contains(lower, "[warning]"):
			col = ColorWarning
		case strings.Contains(lower, "[info]"):
			col = ColorInfo
		case strings.Contains(lower, "[ok]") || strings.Contains(lower, "[success]"):
			col = ColorSuccess
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
