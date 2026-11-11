package main

// ffmpegtls.go — https for an ffmpeg that cannot speak it.
//
// The TrimUI firmware ships its own ffmpeg and ffprobe on PATH (6.1, built for
// the A133P SDK), and they were built without a TLS library: every https://
// input fails with "https protocol not found". getToolPath falls back to PATH
// when JukaHub's tools folder holds no ffmpeg of its own, and the tool check
// counts the firmware's copy as installed, so on a stock Smart Pro every
// YouTube stream reached a decoder that could not open it. A video only played
// when the stream probe gave up and the file was downloaded instead — which is
// why YouTube "sometimes" failed and sometimes worked.
//
// No second 80 MB binary is needed on the card: Go speaks TLS. The first time a
// tool is about to be handed an https input, `<tool> -protocols` says whether it
// can open one. When it cannot, the input becomes a loopback http:// address
// served by a small relay in this process, which fetches the real URL (Range
// requests passed through, so seeking and the mp4 index work) and streams it
// back. HLS playlists are rewritten on the way through, so their variants,
// keys and segments go through the relay too.
//
// The relay listens on 127.0.0.1 only and serves nothing but the URLs this
// process registered, behind unguessable tokens: it is not an open proxy.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Can this tool open https? ──────────────────────────────────────────────

// ffmpegTLSProbeTimeout bounds `-protocols`, which prints at once.
const ffmpegTLSProbeTimeout = 5 * time.Second

// ffmpegTLSEntry is one binary's answer, valid while the file is unchanged.
type ffmpegTLSEntry struct {
	size  int64
	mtime time.Time
	https bool
}

var (
	ffmpegTLSMu    sync.Mutex
	ffmpegTLSCache = map[string]ffmpegTLSEntry{}
	// ffmpegProtocolsRunner prints a tool's protocol list. Tests swap it.
	ffmpegProtocolsRunner = runFFmpegProtocols
)

// runFFmpegProtocols runs `<tool> -hide_banner -protocols`; ffmpeg and ffprobe
// both answer it.
func runFFmpegProtocols(ctx context.Context, tool string) (string, error) {
	cmd := exec.CommandContext(ctx, tool, "-hide_banner", "-protocols")
	cmd.WaitDelay = time.Second
	// No console window flashing up on Windows (sched_windows.go).
	schedHideWindow(cmd)
	out, err := cmd.Output()
	return string(out), err
}

// ffmpegProtocolsHaveHTTPS reads `-protocols` output. The "Input:" list names
// one protocol per line, and https is in it only when the build has TLS.
func ffmpegProtocolsHaveHTTPS(out string) bool {
	input := false
	for _, line := range strings.Split(out, "\n") {
		word := strings.TrimSpace(line)
		switch {
		case strings.EqualFold(word, "Input:"):
			input = true
		case strings.EqualFold(word, "Output:"):
			input = false
		case input && word == "https":
			return true
		}
	}
	return false
}

// ffmpegTLSReset forgets every probed binary (tests).
func ffmpegTLSReset() {
	ffmpegTLSMu.Lock()
	ffmpegTLSCache = map[string]ffmpegTLSEntry{}
	ffmpegTLSMu.Unlock()
}

// ffmpegToolSupportsHTTPS reports whether the ffmpeg-family tool at path can
// open an https:// input itself. The answer is cached per binary (path, size
// and modification time), so a replaced tool is asked again.
//
// A tool that cannot be stat'ed or probed counts as "no": the relay works with
// every ffmpeg that speaks plain http, so routing through it costs a loopback
// hop at worst, while trusting a TLS claim nobody made is what broke YouTube.
// The mutex is held across the probe on purpose: ffprobe and the two decoders
// start together, and one probe answers all three.
func ffmpegToolSupportsHTTPS(tool string) bool {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return false
	}
	info, err := os.Stat(tool)
	if err != nil || info.IsDir() {
		return false
	}
	ffmpegTLSMu.Lock()
	defer ffmpegTLSMu.Unlock()
	if e, ok := ffmpegTLSCache[tool]; ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) {
		return e.https
	}
	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTLSProbeTimeout)
	out, runErr := ffmpegProtocolsRunner(ctx, tool)
	cancel()
	https := runErr == nil && ffmpegProtocolsHaveHTTPS(out)
	ffmpegTLSCache[tool] = ffmpegTLSEntry{size: info.Size(), mtime: info.ModTime(), https: https}
	switch {
	case runErr != nil:
		logWarn("stream", "could not ask %s which protocols it opens (%v); https inputs go through the local relay", tool, runErr)
	case !https:
		logInfo("stream", "%s was built without https (as the TrimUI firmware's own copy is); https inputs go through the local relay", tool)
	}
	return https
}

// ffmpegReadableInput is what an ffmpeg-family tool at toolPath is handed for
// src: src itself, unless it is an https URL the tool cannot open, in which
// case it is the relay's loopback address for it. Every place that gives
// ffmpeg or ffprobe a network input goes through here.
func ffmpegReadableInput(toolPath, src string) string {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(src)), "https://") {
		return src
	}
	if ffmpegToolSupportsHTTPS(toolPath) {
		return src
	}
	relay, err := mediaRelay()
	if err != nil {
		logWarn("stream", "https relay unavailable (%v); %s goes to ffmpeg as is", err, sanitizeLogURL(src))
		return src
	}
	return relay.urlFor(strings.TrimSpace(src))
}

// ── The loopback relay ─────────────────────────────────────────────────────

const (
	// mediaRelayIdleTTL is how long a route lives unused. A paused video keeps
	// its route far longer than any signed URL stays valid anyway.
	mediaRelayIdleTTL = 30 * time.Minute
	// mediaRelayMaxRoutes caps the table: a live stream registers its new
	// segments on every playlist refresh.
	mediaRelayMaxRoutes = 4096
	// mediaRelayPlaylistMax is the most of a playlist read for rewriting; a
	// longer one goes through untouched.
	mediaRelayPlaylistMax = 8 << 20
	// mediaRelayNameMax bounds the readable name kept after the token.
	mediaRelayNameMax = 64
)

// mediaRelayRoute is one registered upstream URL. name is the readable tail
// of its relay address; a request with another tail is not this route (a
// relative reference resolved against a playlist that went out unrewritten).
type mediaRelayRoute struct {
	upstream string
	name     string
	lastUsed time.Time
}

// mediaRelayServer is the relay: a loopback HTTP server and its route table.
type mediaRelayServer struct {
	mu     sync.Mutex
	base   string                      // "http://127.0.0.1:port"
	routes map[string]*mediaRelayRoute // token -> route
	tokens map[string]string           // upstream URL -> token
	client *http.Client
	srv    *http.Server
}

var (
	mediaRelayMu   sync.Mutex
	mediaRelayInst *mediaRelayServer
)

// mediaRelay returns the process's relay, starting it on first use. A failed
// start is retried on the next call instead of being remembered.
func mediaRelay() (*mediaRelayServer, error) {
	mediaRelayMu.Lock()
	defer mediaRelayMu.Unlock()
	if mediaRelayInst != nil {
		return mediaRelayInst, nil
	}
	s, err := newMediaRelayServer()
	if err != nil {
		return nil, err
	}
	mediaRelayInst = s
	logInfo("stream", "https relay for the firmware's ffmpeg listening on %s", s.base)
	return s, nil
}

// mediaRelayStop closes the process's relay (tests; the process exit does it
// otherwise).
func mediaRelayStop() {
	mediaRelayMu.Lock()
	s := mediaRelayInst
	mediaRelayInst = nil
	mediaRelayMu.Unlock()
	if s != nil {
		s.Close()
	}
}

// newMediaRelayServer starts a relay on a free loopback port.
func newMediaRelayServer() (*mediaRelayServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	// The jar matters for live streams: some CDNs set a cookie on the playlist
	// and check it on every segment, a hand-off ffmpeg does itself when it
	// connects directly.
	jar, _ := cookiejar.New(nil)
	s := &mediaRelayServer{
		base:   "http://" + ln.Addr().String(),
		routes: map[string]*mediaRelayRoute{},
		tokens: map[string]string{},
		client: &http.Client{Transport: mediaRelayTransport(), Jar: jar},
	}
	s.srv = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// A decoder that seeks drops its connection mid-body; the server's
		// "broken pipe" lines would only fill errors.txt.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Close stops the relay; open streams are cut.
func (s *mediaRelayServer) Close() {
	if s == nil || s.srv == nil {
		return
	}
	_ = s.srv.Close()
}

// mediaRelayTransport is the upstream transport: the default one with a bound
// on the wait for response headers, and the firmware CA fallback the native
// YouTube client uses when the system store is empty (bugrepRootCAs).
func mediaRelayTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	tr := base.Clone()
	tr.ResponseHeaderTimeout = 20 * time.Second
	tr.MaxIdleConnsPerHost = 4
	if pool := bugrepRootCAs(); pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return tr
}

// mediaRelayRouteName is the readable tail of a route: the upstream file name
// (ffmpeg's HLS probe looks for ".m3u8"), reduced to safe characters.
func mediaRelayRouteName(upstream string) string {
	name := ""
	if u, err := url.Parse(upstream); err == nil {
		name = path.Base(u.Path)
	}
	if name == "" || name == "." || name == "/" {
		return "media"
	}
	cleaned := []byte(name)
	for i, c := range cleaned {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		default:
			cleaned[i] = '_'
		}
	}
	name = string(cleaned)
	if len(name) > mediaRelayNameMax {
		ext := path.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		name = name[:mediaRelayNameMax-len(ext)] + ext
	}
	return name
}

// mediaRelayNewToken is 128 random bits, hex. crypto/rand cannot fail since
// Go 1.24: it crashes the program rather than return an error.
func mediaRelayNewToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// urlFor registers upstream (once per URL) and returns its relay address.
func (s *mediaRelayServer) urlFor(upstream string) string {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[upstream]
	r := s.routes[token]
	if ok && r != nil {
		r.lastUsed = now
		return s.base + "/r/" + token + "/" + r.name
	}
	// Room is made before the new route goes in: pruned after, a tie in
	// last-used times (a coarse clock) could evict the route just created and
	// hand out an address that is already gone.
	if len(s.routes) >= mediaRelayMaxRoutes {
		s.pruneToLocked(now, mediaRelayMaxRoutes-1)
	}
	token = mediaRelayNewToken()
	r = &mediaRelayRoute{upstream: upstream, name: mediaRelayRouteName(upstream), lastUsed: now}
	s.routes[token] = r
	s.tokens[upstream] = token
	return s.base + "/r/" + token + "/" + r.name
}

// pruneLocked drops idle routes, then the least recently used ones until the
// table is within its cap. s.mu must be held.
func (s *mediaRelayServer) pruneLocked(now time.Time) {
	s.pruneToLocked(now, mediaRelayMaxRoutes)
}

// pruneToLocked is pruneLocked down to limit routes. s.mu must be held.
func (s *mediaRelayServer) pruneToLocked(now time.Time, limit int) {
	for token, r := range s.routes {
		if now.Sub(r.lastUsed) > mediaRelayIdleTTL {
			delete(s.routes, token)
			delete(s.tokens, r.upstream)
		}
	}
	for len(s.routes) > limit {
		oldest, oldestAt := "", now
		for token, r := range s.routes {
			if oldest == "" || r.lastUsed.Before(oldestAt) {
				oldest, oldestAt = token, r.lastUsed
			}
		}
		if oldest == "" {
			return
		}
		delete(s.tokens, s.routes[oldest].upstream)
		delete(s.routes, oldest)
	}
}

// lookup resolves a request path ("/r/<token>/<name>") to its upstream URL.
func (s *mediaRelayServer) lookup(reqPath string) (string, bool) {
	rest, ok := strings.CutPrefix(reqPath, "/r/")
	if !ok {
		return "", false
	}
	token, name, _ := strings.Cut(rest, "/")
	if token == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.routes[token]
	if r == nil || name != r.name {
		return "", false
	}
	r.lastUsed = time.Now()
	return r.upstream, true
}

// mediaRelayRequestHeaders are passed from ffmpeg to the CDN. Range is what
// seeking is made of; the rest is the client identity a signed URL or an
// Icecast server cares about.
var mediaRelayRequestHeaders = []string{
	"Range", "If-Range", "User-Agent", "Referer", "Cookie", "Accept",
	"Accept-Language", "Origin", "Icy-MetaData",
}

// mediaRelayResponseHeaders are passed from the CDN back to ffmpeg, along with
// every Icy-* header (a radio stream's metadata interval and name).
var mediaRelayResponseHeaders = []string{
	"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
	"Last-Modified", "ETag", "Cache-Control", "Expires",
}

// ServeHTTP relays one request.
func (s *mediaRelayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	upstream, ok := s.lookup(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, nil)
	if err != nil {
		http.Error(w, "bad upstream address", http.StatusBadGateway)
		return
	}
	for _, name := range mediaRelayRequestHeaders {
		if v := r.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		// The identity yt-dlp extracted the URL with; Go's own would be sent
		// otherwise, and some CDNs refuse it.
		req.Header.Set("User-Agent", streamUserAgent)
	}
	// Bytes as stored: Content-Encoding is not passed on, so a compressed
	// answer would reach ffmpeg (and the playlist rewrite) as garbage.
	req.Header.Set("Accept-Encoding", "identity")
	client := s.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			logWarn("stream", "relay could not reach %s: %v", sanitizeLogURL(upstream), err)
		}
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body := bufio.NewReaderSize(resp.Body, 64<<10)
	if r.Method == http.MethodGet && mediaRelayWholeBody(resp) &&
		mediaRelayLooksLikePlaylist(resp.Header.Get("Content-Type"), upstream, body) {
		s.servePlaylist(w, resp, body)
		return
	}
	for _, name := range mediaRelayResponseHeaders {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	for name, values := range resp.Header {
		if strings.HasPrefix(strings.ToLower(name), "icy-") {
			for _, v := range values {
				w.Header().Add(name, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	mediaRelayCopy(w, body)
}

// mediaRelayCopy streams the body to ffmpeg, flushing as it goes so a radio
// stream or a live segment is not held back in a buffer.
func mediaRelayCopy(w http.ResponseWriter, body io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			// The CDN dropped the transfer. Ending the response cleanly would
			// tell ffmpeg the stream ended (there is often no length to check it
			// against); aborting it is a read error ffmpeg retries or reports.
			panic(http.ErrAbortHandler)
		}
	}
}

// mediaRelayWholeBody reports whether resp carries the whole resource: a 200,
// or a 206 whose range is all of it. ffmpeg asks for "bytes=0-" even for a
// playlist, and a CDN answers that with a 206.
func mediaRelayWholeBody(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusOK:
		return true
	case http.StatusPartialContent:
		// "bytes 0-1233/1234"
		spec, ok := strings.CutPrefix(strings.TrimSpace(resp.Header.Get("Content-Range")), "bytes ")
		if !ok {
			return false
		}
		rng, total, ok := strings.Cut(spec, "/")
		if !ok {
			return false
		}
		first, last, ok := strings.Cut(rng, "-")
		if !ok || strings.TrimSpace(first) != "0" {
			return false
		}
		end, err1 := strconv.ParseInt(strings.TrimSpace(last), 10, 64)
		size, err2 := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
		return err1 == nil && err2 == nil && end+1 == size
	}
	return false
}

// mediaRelayPlaylistTypes are the HLS playlist MIME types ffmpeg's probe
// accepts.
var mediaRelayPlaylistTypes = []string{
	"application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl",
}

// mediaRelayIsPlaylistType reports whether a Content-Type names an HLS
// playlist.
func mediaRelayIsPlaylistType(contentType string) bool {
	ct, _, _ := strings.Cut(strings.ToLower(contentType), ";")
	ct = strings.TrimSpace(ct)
	for _, t := range mediaRelayPlaylistTypes {
		if ct == t {
			return true
		}
	}
	return false
}

// mediaRelayLooksLikePlaylist decides whether a response is an HLS playlist:
// by its type, its file extension, or a body that starts with #EXTM3U (a CDN
// that serves playlists as text/plain).
func mediaRelayLooksLikePlaylist(contentType, upstream string, body *bufio.Reader) bool {
	if mediaRelayIsPlaylistType(contentType) {
		return true
	}
	if u, err := url.Parse(upstream); err == nil {
		switch strings.ToLower(path.Ext(u.Path)) {
		case ".m3u8", ".m3u":
			return true
		}
	}
	head, _ := body.Peek(len(mediaRelayBOM) + 7)
	return bytes.HasPrefix(bytes.TrimPrefix(head, []byte(mediaRelayBOM)), []byte("#EXTM3U"))
}

// mediaRelayBOM is the UTF-8 byte order mark some playlist writers put first.
const mediaRelayBOM = "\uFEFF"

// servePlaylist rewrites an HLS playlist so every URI in it is a relay route,
// and serves it whole. A playlist too large to rewrite goes through as is.
func (s *mediaRelayServer) servePlaylist(w http.ResponseWriter, resp *http.Response, body io.Reader) {
	data, err := io.ReadAll(io.LimitReader(body, mediaRelayPlaylistMax+1))
	if err != nil {
		http.Error(w, "upstream playlist read failed", http.StatusBadGateway)
		return
	}
	contentType := resp.Header.Get("Content-Type")
	if !mediaRelayIsPlaylistType(contentType) {
		// ffmpeg's HLS probe refuses a playlist with neither the type nor the
		// extension; the route's name may not carry the extension.
		contentType = "application/vnd.apple.mpegurl"
	}
	if len(data) > mediaRelayPlaylistMax {
		// Too large to rewrite: what was read goes out, then the rest as it
		// arrives, rather than a cut-off playlist.
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		mediaRelayCopy(w, body)
		return
	}
	base := resp.Request.URL // after redirects: what relative URIs resolve against
	data = []byte(mediaRelayRewritePlaylist(string(data), base, s.urlFor))
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// mediaRelayURIAttr matches a quoted URI attribute in an HLS tag
// (#EXT-X-KEY, -MAP, -MEDIA, -I-FRAME-STREAM-INF, -PART, -PRELOAD-HINT, ...).
var mediaRelayURIAttr = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`([:,])URI="([^"]*)"`) })

// mediaRelayRewritePlaylist points every URI line and URI attribute of an HLS
// playlist at route(absolute URL), keeping each line's ending.
func mediaRelayRewritePlaylist(body string, base *url.URL, route func(string) string) string {
	var b strings.Builder
	b.Grow(len(body) + len(body)/2)
	// A byte order mark stays where it was, and the "#EXTM3U" after it is
	// still read as a tag rather than as a URI line.
	if rest, ok := strings.CutPrefix(body, mediaRelayBOM); ok {
		b.WriteString(mediaRelayBOM)
		body = rest
	}
	for _, raw := range strings.SplitAfter(body, "\n") {
		line := strings.TrimRight(raw, "\r\n")
		ending := raw[len(line):]
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			b.WriteString(raw)
			continue
		case strings.HasPrefix(trimmed, "#"):
			line = mediaRelayURIAttr().ReplaceAllStringFunc(line, func(m string) string {
				sub := mediaRelayURIAttr().FindStringSubmatch(m)
				return sub[1] + `URI="` + mediaRelayRewriteRef(sub[2], base, route) + `"`
			})
		default:
			line = mediaRelayRewriteRef(trimmed, base, route)
		}
		b.WriteString(line)
		b.WriteString(ending)
	}
	return b.String()
}

// mediaRelayRewriteRef resolves one playlist reference against the playlist's
// own URL and returns its relay route. Anything that is not http(s) once
// resolved (data:, skd:, a malformed line) is left as it was.
func mediaRelayRewriteRef(ref string, base *url.URL, route func(string) string) string {
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return ref
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return route(u.String())
	}
	return ref
}
