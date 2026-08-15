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
	resp, err := client.Get(url)
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

func quoteArg(s string) string {
	if runtime.GOOS == "windows" {
		return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
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
	recordPlayed(url)
	ff := getToolPath("ffplay", config)
	log.Printf("playStream: %s", url)
	go func() {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/c", ff, "-fs", "-autoexit", url)
		} else {
			cmd = exec.Command(ff, "-fs", "-autoexit", url)
		}
		if err := cmd.Run(); err != nil {
			log.Printf("playStream error: %v", err)
		}
	}()
}

// playWithMPV plays a URL through the MPV media player as an alternative
// backend to ffplay. MPV is launched fullscreen with no OSC and an IPC
// socket so the app can communicate with it if needed in the future.
func playWithMPV(config *Config, url string) {
	recordPlayed(url)
	mpv := findMPVPath()
	if mpv == "" {
		log.Printf("playWithMPV: mpv not found on this system")
		showToast("MPV not found. Install MPV or switch audio backend in Settings.", sdl.Color{R: 230, G: 80, B: 80, A: 255})
		return
	}
	ipcSocket := "/tmp/mpv-socket"
	if runtime.GOOS == "windows" {
		ipcSocket = filepath.Join(os.TempDir(), "mpv-socket")
	}
	log.Printf("playWithMPV: url=%s mpv=%s", url, mpv)
	go func() {
		cmd := exec.Command(mpv,
			"--fs",
			"--no-osc",
			"--input-ipc-server="+ipcSocket,
			"--",
			url,
		)
		cmd.Env = append(os.Environ(),
			"SDL_AUDIODRIVER=directsound",
		)
		if err := cmd.Run(); err != nil {
			log.Printf("playWithMPV error: %v", err)
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
		playVideoURL(config, url)
		return
	}
	recordPlayed(url)
	ytp := getToolPath("yt-dlp", config)
	ff := getToolPath("ffplay", config)
	go func() {
		cmdStr := fmt.Sprintf("%s -f best -o - %s | %s -", quotePath(ytp), quoteArg(url), quotePath(ff))
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/c", cmdStr)
		} else {
			cmd = exec.Command("sh", "-c", cmdStr)
		}
		if err := cmd.Run(); err != nil {
			log.Printf("playSmartURL error: %v", err)
		}
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
	lines := strings.Split(string(data), "\n")
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
	re := regexp.MustCompile(`(?is)<enclosure[^>]*url="([^"]+)"`)
	if m := re.FindStringSubmatch(block); m != nil {
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
	re := regexp.MustCompile(`(?is)<item>(.*?)</item>`)
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
	"https://feeds.bbci.co.uk/podcasts/rss/fooc",
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
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	dest := strings.TrimSuffix(src, filepath.Ext(src)) + "_unzipped"
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	for _, f := range r.File {
		p := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0755)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(p)
		if err != nil {
			rc.Close()
			return err
		}
		io.Copy(out, rc)
		out.Close()
		rc.Close()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Full Weather screen text
// ---------------------------------------------------------------------------

func weatherScreenText(config *Config) string {
	wxMutex.Lock()
	ready := weatherReady
	t := weatherTempC
	wxMutex.Unlock()
	if !ready {
		return "Weather unavailable (offline?)"
	}
	unit := config.Variables.WeatherUnit
	disp := t
	if unit == "F" {
		disp = t*9/5 + 32
	}
	return fmt.Sprintf("Current temperature:\n%.1f %s\n\nSource: IP geolocation +\nopen-meteo", disp, unit)
}

// ---------------------------------------------------------------------------
// textlog element (multi-line read-only display)
// ---------------------------------------------------------------------------

func renderTextLog(renderer *sdl.Renderer, config *Config, element Element) {
	elemW := getElementWidth(element, 1100)
	elemH := getElementHeight(element, 480)
	drawPanel(renderer, element.X, element.Y, elemW, elemH, sdl.Color{R: 16, G: 19, B: 26, A: 235}, accentColor)

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
	for _, ln := range lines {
		if y > element.Y+elemH-16 {
			break
		}
		renderText(renderer, config, font, ln, sdl.Color{R: 210, G: 218, B: 230, A: 255}, element.X+10, y)
		y += lineH
	}
}
