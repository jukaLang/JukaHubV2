package main

import (
	"archive/zip"
	"bufio"
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
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "JukaHub/0.4")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
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

// playStream launches ffplay directly on a stream URL (rtsp://, http://,
// or any URL ffplay understands) without going through yt-dlp.
func playStream(config *Config, url string) {
	recordPlayed(config, url)
	log.Printf("playStream: %s", sanitizeLogURL(url))
	go func() {
		// Resolve (and install, if needed) before playing: a missing ffplay
		// shows the install card instead of a silent exec failure.
		ff, err := helperToolPath("ffplay", config)
		if err != nil {
			log.Printf("[ERROR] playStream: ffplay unavailable: %v", err)
			showToast("ffplay not found and auto-install failed. Try again later.", ToastError())
			return
		}
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/c", ff, "-fs", "-autoexit", url)
		} else {
			cmd = exec.Command(ff, "-fs", "-autoexit", url)
		}
		cmd.Env = ffplayEnv(ff)
		if err := cmd.Run(); err != nil {
			log.Printf("playStream error: %v", err)
		}
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
	recordPlayed(config, url)
	go func() {
		// Resolve both tools first: helperToolPath downloads and verifies a
		// missing yt-dlp/ffplay (with the install card) instead of leaving the
		// user with a raw "executable file not found" later.
		ytp, err := helperToolPath("yt-dlp", config)
		if err != nil {
			log.Printf("[ERROR] playSmartURL: yt-dlp unavailable: %v", err)
			showToast("yt-dlp not found and auto-install failed. Try again later.", ToastError())
			return
		}
		ff, err := helperToolPath("ffplay", config)
		if err != nil {
			log.Printf("[ERROR] playSmartURL: ffplay unavailable: %v", err)
			showToast("ffplay not found and auto-install failed. Try again later.", ToastError())
			return
		}
		// Sanity-check the URL before interpolating it into a shell command
		// (CodeQL #40: command built from user-controlled sources). We only
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
		extraArgList := ytDlpExtraArgsSlice(config)
		if err := validateYtDlpArgs(extraArgList); err != nil {
			log.Printf("[SECURITY] playSmartURL: rejecting unsafe extra args: %v", err)
			return
		}
		// Two processes joined by an OS pipe instead of `sh -c "... | ..."`.
		// No shell parses the URL or any argument, so quoting can never turn
		// into command injection (CodeQL #40).
		ytArgs := []string{"-f", "best", "-o", "-"}
		ytArgs = append(ytArgs, extraArgList...)
		ytArgs = append(ytArgs, url)
		ytCmd := exec.Command(ytp, ytArgs...)

		ffArgs := []string{"-i", "pipe:0", "-autoexit"}
		if IsWindows() {
			ffArgs = append([]string{"-noborder", "-x", "1280", "-y", "720"}, ffArgs...)
		} else {
			ffArgs = append([]string{"-fs"}, ffArgs...)
		}
		ffCmd := exec.Command(ff, ffArgs...)
		ffCmd.Env = ffplayEnv(ff)

		pipe, err := ytCmd.StdoutPipe()
		if err != nil {
			log.Printf("playSmartURL: stdout pipe: %v", err)
			return
		}
		ffCmd.Stdin = pipe
		ytCmd.Stderr = os.Stderr
		ffCmd.Stderr = os.Stderr
		if err := ffCmd.Start(); err != nil {
			log.Printf("playSmartURL: ffplay start: %v", err)
			return
		}
		if err := ytCmd.Start(); err != nil {
			log.Printf("playSmartURL: yt-dlp start: %v", err)
			_ = ffCmd.Process.Kill()
			_ = ffCmd.Wait()
			return
		}
		_ = ytCmd.Wait()
		_ = ffCmd.Wait()
	}()
}

// ---------------------------------------------------------------------------
// IPTV (Live TV) - parse .m3u playlists from a tv/ folder
// ---------------------------------------------------------------------------

func parseM3U(path string) []FileEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseM3UContent(string(data))
}

func parseM3UContent(data string) []FileEntry {
	lines := strings.Split(data, "\n")
	var entries []FileEntry
	var curName string
	reExt := regexp.MustCompile(`(?i)#EXTINF.*,(.*)$`)
	reURL := regexp.MustCompile(`(?i)^https?://`)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#EXTM3U") {
			continue
		}
		if m := reExt.FindStringSubmatch(line); m != nil {
			curName = strings.TrimSpace(m[1])
			continue
		}
		if reURL.MatchString(line) {
			name := curName
			if name == "" {
				name = line
			}
			entries = append(entries, FileEntry{Name: name, Path: line})
			curName = ""
		}
	}
	return entries
}

func loadIPTV(config *Config) {
	tvDir := "tv"
	entries := []FileEntry{}
	if fis, err := os.ReadDir(tvDir); err == nil {
		for _, f := range fis {
			if f.IsDir() {
				continue
			}
			lower := strings.ToLower(f.Name())
			if strings.HasSuffix(lower, ".m3u") || strings.HasSuffix(lower, ".m3u8") {
				entries = append(entries, parseM3U(filepath.Join(tvDir, f.Name()))...)
			}
		}
	}
	// Fall back to (or supplement with) the public iptv-org playlist when no
	// local playlists are present.
	if len(entries) == 0 {
		url := "https://iptv-org.github.io/iptv/index.m3u"
		if db, err := cacheOpen("jukahub.cache"); err == nil {
			if cached, cErr := cacheGet(db, "iptv:"+url); cErr == nil && cached != nil {
				entries = append(entries, parseM3UContent(string(cached))...)
			} else {
				if data := httpGetText(url, 20*time.Second); data != "" {
					entries = append(entries, parseM3UContent(data)...)
					cacheSet(db, "iptv:"+url, []byte(data), 6*time.Hour)
				}
			}
			db.Close()
		} else {
			if data := httpGetText(url, 20*time.Second); data != "" {
				entries = append(entries, parseM3UContent(data)...)
			}
		}
	}
	publishCustom("iptv_entries", entries)
	focusedFileIndex = 0
}

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
	data := httpGetText(url, 12*time.Second)
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

func loadPodcasts(config *Config) {
	var entries []FileEntry
	for _, f := range curatedPodcasts {
		entries = append(entries, parseRSSFeed(f)...)
	}
	publishCustom("podcast_entries", entries)
	focusedFileIndex = 0
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

func fetchTickerPrice(symbol string) (float64, bool) {
	url := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s", strings.ToUpper(strings.TrimSpace(symbol)))
	txt := httpGetText(url, 8*time.Second)
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

func fetchTickers(config *Config) {
	go func() {
		var sb strings.Builder
		txt := httpGetText("https://api.coingecko.com/api/v3/simple/price?ids=bitcoin,ethereum&vs_currencies=usd", 8*time.Second)
		var cg map[string]map[string]float64
		if json.Unmarshal([]byte(txt), &cg) == nil {
			if b, ok := cg["bitcoin"]["usd"]; ok {
				sb.WriteString(fmt.Sprintf("BTC: $%.0f\n", b))
			}
			if e, ok := cg["ethereum"]["usd"]; ok {
				sb.WriteString(fmt.Sprintf("ETH: $%.0f\n", e))
			}
		}
		y := httpGetText("https://query1.finance.yahoo.com/v8/finance/chart/AAPL", 8*time.Second)
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
			sb.WriteString(fmt.Sprintf("AAPL: $%.2f\n", ya.Chart.Result[0].Meta.RegularMarketPrice))
		}
		// User-added custom tickers (stored as a slice of symbols).
		if list, ok := config.Variables.Custom["custom_tickers"].([]string); ok {
			for _, sym := range list {
				sym = strings.TrimSpace(sym)
				if sym == "" {
					continue
				}
				if p, ok := fetchTickerPrice(sym); ok {
					sb.WriteString(fmt.Sprintf("%s: $%.2f\n", strings.ToUpper(sym), p))
				} else {
					sb.WriteString(fmt.Sprintf("%s: n/a\n", strings.ToUpper(sym)))
				}
			}
		}
		if sb.Len() == 0 {
			sb.WriteString("Unable to fetch tickers (offline?)")
		}
		publishCustom("ticker_text", sb.String())
	}()
}

// ---------------------------------------------------------------------------
// Network Speed Meter
// ---------------------------------------------------------------------------

func runNetSpeed(config *Config) {
	go func() {
		url := "https://cachefly.cachefly.net/100mb.test"
		start := time.Now()
		resp, err := httpClient.Get(url)
		if err != nil {
			publishCustom("netspeed_text", "Error: "+err.Error())
			return
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		dur := time.Since(start).Seconds()
		if dur <= 0 {
			dur = 0.001
		}
		mbps := (float64(n) * 8 / dur) / 1e6
		publishCustom("netspeed_text", fmt.Sprintf(
			"Downloaded %.1f MB in %.2fs\nSpeed: %.1f Mbit/s", float64(n)/1e6, dur, mbps))
	}()
}

// ---------------------------------------------------------------------------
// Read/Write Benchmark
// ---------------------------------------------------------------------------

func runBenchmark(config *Config) {
	go func() {
		path := ".jukabench.tmp"
		f, err := os.Create(path)
		if err != nil {
			publishCustom("benchmark_text", "Error: "+err.Error())
			return
		}
		sz := 50 * 1024 * 1024
		buf := make([]byte, 1024*1024)
		for i := range buf {
			buf[i] = byte(i)
		}
		start := time.Now()
		for written := 0; written < sz; written += len(buf) {
			f.Write(buf)
		}
		f.Sync()
		d1 := time.Since(start)
		f.Seek(0, 0)
		r := bufio.NewReader(f)
		start = time.Now()
		for {
			_, e := r.Read(buf)
			if e != nil {
				break
			}
		}
		d2 := time.Since(start)
		f.Close()
		os.Remove(path)
		publishCustom("benchmark_text", fmt.Sprintf(
			"Write: %.1f MB/s\nRead:  %.1f MB/s",
			float64(sz)/d1.Seconds()/1e6, float64(sz)/d2.Seconds()/1e6))
	}()
}

// runRandomBenchmark performs random-access I/O benchmarking: it writes
// 10 MB of random data to a temp file at random offsets, then reads 10 MB
// of random positions, reporting MB/s for each phase.
func runRandomBenchmark(config *Config) {
	go func() {
		const fileSize = 50 * 1024 * 1024
		const ioBytes = 10 * 1024 * 1024
		blockSize := 4 * 1024
		path := ".jukarandbench.tmp"

		f, err := os.Create(path)
		if err != nil {
			publishCustom("benchmark_text", "Random Error: "+err.Error())
			return
		}
		if err := f.Truncate(fileSize); err != nil {
			f.Close()
			publishCustom("benchmark_text", "Random Error: "+err.Error())
			return
		}

		rnd := make([]byte, blockSize)
		offsets := make([]int64, ioBytes/blockSize)
		for i := range offsets {
			offsets[i] = int64(i) * int64(fileSize) / int64(len(offsets))
		}

		start := time.Now()
		for _, off := range offsets {
			if _, err := rand.Read(rnd); err != nil {
				rnd = []byte{byte(off), byte(off >> 8), byte(off >> 16)}
			}
			if _, err := f.WriteAt(rnd, off); err != nil {
				f.Close()
				os.Remove(path)
				publishCustom("benchmark_text", "Random Write Error: "+err.Error())
				return
			}
		}
		f.Sync()
		dWrite := time.Since(start)

		readBuf := make([]byte, blockSize)
		start = time.Now()
		for _, off := range offsets {
			if _, err := f.ReadAt(readBuf, off); err != nil {
				f.Close()
				os.Remove(path)
				publishCustom("benchmark_text", "Random Read Error: "+err.Error())
				return
			}
		}
		dRead := time.Since(start)

		f.Close()
		os.Remove(path)
		publishCustom("benchmark_text", fmt.Sprintf(
			"Random Write: %.1f MB/s\nRandom Read:  %.1f MB/s",
			float64(ioBytes)/dWrite.Seconds()/1e6, float64(ioBytes)/dRead.Seconds()/1e6))
	}()
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
	unit := config.Variables.WeatherUnit
	conv := func(c float64) float64 {
		if unit == "F" {
			return c*9/5 + 32
		}
		return c
	}
	var sb strings.Builder
	sb.WriteString("7-Day Forecast\n\n")
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
