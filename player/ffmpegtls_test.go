package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The TrimUI firmware's own ffmpeg, as `-protocols` prints it: http is there,
// https is not (the build has no TLS library). This is the binary that made
// every YouTube stream fail with "https protocol not found".
const ffmpegProtocolsNoTLS = `Supported file protocols:
Input:
  async
  cache
  concat
  concatf
  crypto
  data
  fd
  ffrtmphttp
  file
  ftp
  gopher
  hls
  http
  httpproxy
  icecast
  mmsh
  mmst
  pipe
  rtmp
  rtmpt
  rtp
  srtp
  subfile
  tcp
  udp
  udplite
  unix
Output:
  crypto
  fd
  ffrtmphttp
  file
  ftp
  gopher
  http
  httpproxy
  icecast
  md5
  pipe
  prompeg
  rtmp
  rtmpt
  rtp
  srtp
  tcp
  tee
  udp
  udplite
  unix
`

// A desktop build (BtbN, gyan.dev) with a TLS library, Windows line endings.
const ffmpegProtocolsTLS = "Supported file protocols:\r\nInput:\r\n  async\r\n  file\r\n  hls\r\n  http\r\n" +
	"  https\r\n  tcp\r\n  tls\r\nOutput:\r\n  file\r\n  http\r\n  https\r\n  tls\r\n"

func TestFFmpegProtocolsHaveHTTPS(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"stock TrimUI build", ffmpegProtocolsNoTLS, false},
		{"desktop build with TLS", ffmpegProtocolsTLS, true},
		{"https listed for output only", "Input:\n  http\nOutput:\n  https\n", false},
		{"empty output", "", false},
		// "httpsx" is not https: the list is matched by whole name.
		{"name that only starts with https", "Input:\n  httpsx\nOutput:\n", false},
	}
	for _, c := range cases {
		if got := ffmpegProtocolsHaveHTTPS(c.out); got != c.want {
			t.Errorf("%s: ffmpegProtocolsHaveHTTPS = %v, want %v", c.name, got, c.want)
		}
	}
}

// ffmpegTLSTestTool writes a stand-in executable whose size and mtime key the
// capability cache, and points the probe at canned output.
func ffmpegTLSTestTool(t *testing.T, out string, runErr error) (path string, runs *int) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	count := 0
	runs = &count
	prevRunner := ffmpegProtocolsRunner
	ffmpegProtocolsRunner = func(ctx context.Context, tool string) (string, error) {
		count++
		return out, runErr
	}
	ffmpegTLSReset()
	t.Cleanup(func() {
		ffmpegProtocolsRunner = prevRunner
		ffmpegTLSReset()
	})
	return path, runs
}

// The probe runs once per binary: every stream after the first is answered
// from the cache, and a replaced binary (new size or time) is probed again.
func TestFFmpegToolSupportsHTTPSIsProbedOncePerBinary(t *testing.T) {
	tool, runs := ffmpegTLSTestTool(t, ffmpegProtocolsNoTLS, nil)
	for i := 0; i < 3; i++ {
		if ffmpegToolSupportsHTTPS(tool) {
			t.Fatal("a build without https in its input list reported https support")
		}
	}
	if *runs != 1 {
		t.Fatalf("the probe ran %d times for one binary, want 1", *runs)
	}
	// An update that replaces the binary changes what it can open.
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n# a newer build\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ffmpegTLSTestSetRunnerOutput(ffmpegProtocolsTLS)
	if !ffmpegToolSupportsHTTPS(tool) {
		t.Fatal("the replaced binary was not probed again")
	}
	if *runs != 2 {
		t.Fatalf("the probe ran %d times after the binary changed, want 2", *runs)
	}
}

// ffmpegTLSTestSetRunnerOutput swaps only the canned answer, keeping the
// counter the test installed.
func ffmpegTLSTestSetRunnerOutput(out string) {
	prev := ffmpegProtocolsRunner
	ffmpegProtocolsRunner = func(ctx context.Context, tool string) (string, error) {
		_, _ = prev(ctx, tool)
		return out, nil
	}
}

// A probe that cannot run says nothing about the binary, so the relay is used:
// it works with every ffmpeg that speaks plain http, while trusting a TLS
// claim nobody made is what broke YouTube.
func TestFFmpegToolSupportsHTTPSProbeFailureMeansRelay(t *testing.T) {
	tool, _ := ffmpegTLSTestTool(t, "", errors.New("exec format error"))
	if ffmpegToolSupportsHTTPS(tool) {
		t.Fatal("a failed probe was taken for https support")
	}
	if ffmpegToolSupportsHTTPS(filepath.Join(t.TempDir(), "missing-ffmpeg")) {
		t.Fatal("a tool that does not exist was taken for https support")
	}
}

func TestFFmpegReadableInput(t *testing.T) {
	noTLS, _ := ffmpegTLSTestTool(t, ffmpegProtocolsNoTLS, nil)
	t.Cleanup(mediaRelayStop)

	// Local files and plain http are opened by any build.
	for _, src := range []string{"/mnt/SDCARD/Videos/clip.mp4", `C:\Videos\clip.mkv`, "http://radio.example/stream.mp3", ""} {
		if got := ffmpegReadableInput(noTLS, src); got != src {
			t.Errorf("ffmpegReadableInput(%q) = %q, want it unchanged", src, got)
		}
	}

	src := "https://rr5---sn-abc.googlevideo.com/videoplayback?expire=1&itag=18&sig=xyz"
	got := ffmpegReadableInput(noTLS, src)
	if !strings.HasPrefix(got, "http://127.0.0.1:") || !strings.Contains(got, "/r/") {
		t.Fatalf("https input for a TLS-less ffmpeg = %q, want a loopback relay URL", got)
	}
	if strings.Contains(got, "googlevideo") || strings.Contains(got, "sig=") {
		t.Fatalf("relay URL %q leaks the signed upstream URL", got)
	}
	if !strings.HasSuffix(got, "/videoplayback") {
		t.Fatalf("relay URL %q lost the upstream file name", got)
	}
	// The same source maps to the same route: ffprobe, the video decoder and
	// the audio decoder all open it.
	if again := ffmpegReadableInput(noTLS, src); again != got {
		t.Fatalf("second relay URL %q differs from the first %q", again, got)
	}
	// A playlist keeps its extension: ffmpeg's HLS probe looks for it.
	hls := ffmpegReadableInput(noTLS, "https://cdn.example/live/master.m3u8?token=1")
	if !strings.HasSuffix(hls, "/master.m3u8") {
		t.Fatalf("playlist relay URL %q lost the .m3u8 name", hls)
	}

	// A build with TLS is handed the URL itself.
	withTLS, _ := ffmpegTLSTestTool(t, ffmpegProtocolsTLS, nil)
	if got := ffmpegReadableInput(withTLS, src); got != src {
		t.Fatalf("ffmpeg with TLS got %q, want the https URL itself", got)
	}
}

// mediaRelayTestServer starts a private relay whose upstream client trusts
// the test's TLS server.
func mediaRelayTestServer(t *testing.T, upstream *httptest.Server) *mediaRelayServer {
	t.Helper()
	s, err := newMediaRelayServer()
	if err != nil {
		t.Fatalf("relay did not start: %v", err)
	}
	s.client = upstream.Client()
	t.Cleanup(s.Close)
	return s
}

func mediaRelayTestGet(t *testing.T, url string, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	return resp, body
}

// Seeking is a Range request: the relay passes it through and hands back the
// CDN's partial answer, so ffmpeg's -ss and the mp4 index both work.
func TestMediaRelayPassesRangeRequests(t *testing.T) {
	media := bytes.Repeat([]byte("0123456789abcdef"), 6400) // 100 KiB
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "videoplayback", time.Unix(0, 0), bytes.NewReader(media))
	}))
	defer upstream.Close()
	s := mediaRelayTestServer(t, upstream)
	route := s.urlFor(upstream.URL + "/videoplayback?itag=18&sig=abc")

	resp, body := mediaRelayTestGet(t, route, map[string]string{"Range": "bytes=1000-1999"})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("ranged GET status %d, want 206", resp.StatusCode)
	}
	if cr := resp.Header.Get("Content-Range"); cr != fmt.Sprintf("bytes 1000-1999/%d", len(media)) {
		t.Fatalf("Content-Range %q", cr)
	}
	if !bytes.Equal(body, media[1000:2000]) {
		t.Fatalf("ranged body is %d bytes and differs from the source slice", len(body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("Content-Type %q, want the CDN's video/mp4", ct)
	}

	resp, body = mediaRelayTestGet(t, route, nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, media) {
		t.Fatalf("whole GET: status %d, %d bytes", resp.StatusCode, len(body))
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatal("Accept-Ranges was dropped: ffmpeg would treat the stream as unseekable")
	}
}

// The client identity ffmpeg sends is the one the CDN sees, and a request
// with none gets the browser identity yt-dlp extracted the URL with.
func TestMediaRelayForwardsClientIdentity(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("User-Agent")+"|"+r.Header.Get("Icy-MetaData"))
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	s := mediaRelayTestServer(t, upstream)
	route := s.urlFor(upstream.URL + "/stream")

	mediaRelayTestGet(t, route, map[string]string{"User-Agent": "Lavf/60.16.100", "Icy-MetaData": "1"})
	mediaRelayTestGet(t, route, map[string]string{"User-Agent": ""})

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(seen))
	}
	if seen[0] != "Lavf/60.16.100|1" {
		t.Fatalf("first request reached the CDN as %q", seen[0])
	}
	if seen[1] != streamUserAgent+"|" {
		t.Fatalf("a request without a User-Agent reached the CDN as %q", seen[1])
	}
}

// An HLS playlist is rewritten on the way through, so the variants, the
// audio rendition, the key and every segment — relative, root-relative or
// absolute — are fetched through the relay too.
func TestMediaRelayRewritesHLSPlaylists(t *testing.T) {
	var base string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hls/master.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\r\n"+
				"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"en\",URI=\"audio/en.m3u8\"\r\n"+
				"#EXT-X-STREAM-INF:BANDWIDTH=800000,AUDIO=\"aud\"\r\n"+
				"low/index\r\n"+
				"#EXT-X-STREAM-INF:BANDWIDTH=2000000\r\n"+
				base+"/hls/high/index.m3u8?sig=abc\r\n")
		case "/hls/low/index":
			// Neither the type nor the name says playlist: the body does.
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n"+
				"#EXT-X-KEY:METHOD=AES-128,URI=\"../keys/k1.bin\",IV=0x1\n"+
				"#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI=\"skd://fairplay-key\"\n"+
				"#EXTINF:4.0,\nseg0.ts\n#EXTINF:4.0,\n/abs/seg1.ts?token=1\n#EXT-X-ENDLIST\n")
		case "/hls/low/seg0.ts":
			_, _ = io.WriteString(w, "SEGMENT-ZERO")
		case "/hls/keys/k1.bin":
			_, _ = io.WriteString(w, "KEY-ONE")
		case "/abs/seg1.ts":
			_, _ = io.WriteString(w, "SEGMENT-ONE-"+r.URL.Query().Get("token"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	base = upstream.URL
	s := mediaRelayTestServer(t, upstream)

	resp, body := mediaRelayTestGet(t, s.urlFor(base+"/hls/master.m3u8"), map[string]string{"Range": "bytes=0-"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("master playlist status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Fatalf("master playlist Content-Type %q", ct)
	}
	master := string(body)
	if strings.Contains(master, base) || strings.Contains(master, "sig=abc") {
		t.Fatalf("master playlist still names the upstream:\n%s", master)
	}
	if !strings.Contains(master, "\r\n") {
		t.Fatal("the playlist's CRLF line endings were not kept")
	}
	var variant, audio string
	for _, line := range strings.Split(master, "\r\n") {
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			audio = mediaRelayTestAttr(t, line)
		case line != "" && !strings.HasPrefix(line, "#") && variant == "":
			variant = line
		}
	}
	if !strings.HasPrefix(variant, s.base+"/r/") || !strings.HasPrefix(audio, s.base+"/r/") {
		t.Fatalf("variant %q / audio %q are not relay URLs", variant, audio)
	}

	resp, body = mediaRelayTestGet(t, variant, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("media playlist status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Fatalf("a sniffed playlist went out as %q; ffmpeg's HLS probe needs the mpegurl type", ct)
	}
	media := string(body)
	var key string
	var segments []string
	for _, line := range strings.Split(media, "\n") {
		switch {
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			key = mediaRelayTestAttr(t, line)
		case strings.HasPrefix(line, "#EXT-X-SESSION-KEY:"):
			if got := mediaRelayTestAttr(t, line); got != "skd://fairplay-key" {
				t.Fatalf("a non-http key URI was rewritten to %q", got)
			}
		case line != "" && !strings.HasPrefix(line, "#"):
			segments = append(segments, line)
		}
	}
	if _, keyBody := mediaRelayTestGet(t, key, nil); string(keyBody) != "KEY-ONE" {
		t.Fatalf("key through the relay = %q", keyBody)
	}
	if len(segments) != 2 {
		t.Fatalf("media playlist has %d segments, want 2:\n%s", len(segments), media)
	}
	want := []string{"SEGMENT-ZERO", "SEGMENT-ONE-1"}
	for i, seg := range segments {
		if !strings.HasPrefix(seg, s.base+"/r/") {
			t.Fatalf("segment %d is %q, not a relay URL", i, seg)
		}
		if _, segBody := mediaRelayTestGet(t, seg, nil); string(segBody) != want[i] {
			t.Fatalf("segment %d through the relay = %q, want %q", i, segBody, want[i])
		}
	}
}

func mediaRelayTestAttr(t *testing.T, line string) string {
	t.Helper()
	i := strings.Index(line, `URI="`)
	if i < 0 {
		t.Fatalf("no URI attribute in %q", line)
	}
	rest := line[i+len(`URI="`):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatalf("unterminated URI attribute in %q", line)
	}
	return rest[:j]
}

// The relay is not a proxy for anything else: unknown routes are 404, only
// reads are served, and the CDN's own refusals reach ffmpeg unchanged.
func TestMediaRelayRefusesWhatItDoesNotServe(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusForbidden)
	}))
	defer upstream.Close()
	s := mediaRelayTestServer(t, upstream)

	if resp, _ := mediaRelayTestGet(t, s.base+"/r/0123456789abcdef0123456789abcdef/videoplayback", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route status %d, want 404", resp.StatusCode)
	}
	if resp, _ := mediaRelayTestGet(t, s.base+"/", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("root status %d, want 404", resp.StatusCode)
	}
	route := s.urlFor(upstream.URL + "/videoplayback")
	resp, err := http.Post(route, "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status %d, want 405", resp.StatusCode)
	}
	if resp, _ := mediaRelayTestGet(t, route, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an expired signed URL came back as %d, want the CDN's 403", resp.StatusCode)
	}
	// A relative reference resolved against a route (a playlist that went out
	// unrewritten) is not that route: it must not be answered with the
	// playlist itself.
	sibling := strings.TrimSuffix(route, "/videoplayback") + "/seg0.ts"
	if resp, _ := mediaRelayTestGet(t, sibling, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a route with another name answered %d, want 404", resp.StatusCode)
	}
}

// A 206 is the whole resource only when its range is: ffmpeg asks for
// "bytes=0-" even for a playlist, and only a complete body can be rewritten.
func TestMediaRelayWholeBody(t *testing.T) {
	cases := []struct {
		status int
		rng    string
		want   bool
	}{
		{http.StatusOK, "", true},
		{http.StatusPartialContent, "bytes 0-99/100", true},
		{http.StatusPartialContent, " bytes 0-0/1 ", true},
		{http.StatusPartialContent, "bytes 0-49/100", false},
		{http.StatusPartialContent, "bytes 10-99/100", false},
		{http.StatusPartialContent, "bytes 0-99/*", false},
		{http.StatusPartialContent, "", false},
		{http.StatusNotFound, "", false},
	}
	for _, c := range cases {
		resp := &http.Response{StatusCode: c.status, Header: http.Header{}}
		if c.rng != "" {
			resp.Header.Set("Content-Range", c.rng)
		}
		if got := mediaRelayWholeBody(resp); got != c.want {
			t.Errorf("status %d, Content-Range %q: whole body = %v, want %v", c.status, c.rng, got, c.want)
		}
	}
}

// A byte order mark in front of #EXTM3U neither hides the playlist nor turns
// its first line into a URI.
func TestMediaRelayPlaylistWithByteOrderMark(t *testing.T) {
	base, err := url.Parse("https://cdn.example/live/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	in := mediaRelayBOM + "#EXTM3U\n#EXTINF:4.0,\nseg0.ts\n"
	got := mediaRelayRewritePlaylist(in, base, func(abs string) string { return "R(" + abs + ")" })
	want := mediaRelayBOM + "#EXTM3U\n#EXTINF:4.0,\nR(https://cdn.example/live/seg0.ts)\n"
	if got != want {
		t.Fatalf("rewrite = %q, want %q", got, want)
	}
	if !mediaRelayLooksLikePlaylist("application/octet-stream", "https://cdn.example/live/index",
		bufio.NewReader(strings.NewReader(in))) {
		t.Fatal("a playlist starting with a byte order mark was not recognised")
	}
}

// Routes are reused per URL and the table cannot grow without bound: a live
// stream registers new segments on every playlist refresh.
func TestMediaRelayRouteTableIsBounded(t *testing.T) {
	s := &mediaRelayServer{base: "http://127.0.0.1:1", routes: map[string]*mediaRelayRoute{}, tokens: map[string]string{}}
	first := s.urlFor("https://cdn.example/a.ts")
	if again := s.urlFor("https://cdn.example/a.ts"); again != first {
		t.Fatalf("one URL got two routes: %q and %q", first, again)
	}
	var last, lastURL string
	for i := 0; i < mediaRelayMaxRoutes+50; i++ {
		last = fmt.Sprintf("https://cdn.example/live/seg%d.ts", i)
		lastURL = s.urlFor(last)
	}
	s.mu.Lock()
	n, m := len(s.routes), len(s.tokens)
	s.mu.Unlock()
	if n > mediaRelayMaxRoutes || n != m {
		t.Fatalf("route table holds %d routes / %d tokens, cap %d", n, m, mediaRelayMaxRoutes)
	}
	// The route just handed out survives the eviction that made room for it,
	// whatever the clock's resolution.
	if got, ok := s.lookup(strings.TrimPrefix(lastURL, s.base)); !ok || got != last {
		t.Fatalf("the newest route %q resolves to %q (%v)", lastURL, got, ok)
	}

	// Idle routes expire.
	s.mu.Lock()
	for _, r := range s.routes {
		r.lastUsed = time.Now().Add(-2 * mediaRelayIdleTTL)
	}
	s.pruneLocked(time.Now())
	n = len(s.routes)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d idle routes survived the prune", n)
	}
}

func TestMediaRelayRouteName(t *testing.T) {
	cases := map[string]string{
		"https://rr5---sn.googlevideo.com/videoplayback?expire=1": "videoplayback",
		"https://cdn.example/live/master.m3u8?token=1":            "master.m3u8",
		"https://cdn.example/":                                    "media",
		"https://cdn.example":                                     "media",
		"https://cdn.example/a%20b/c%3Fd.ts":                      "c_d.ts",
		"https://cdn.example/" + strings.Repeat("x", 200) + ".ts": strings.Repeat("x", 61) + ".ts",
	}
	for in, want := range cases {
		if got := mediaRelayRouteName(in); got != want {
			t.Errorf("mediaRelayRouteName(%q) = %q, want %q", in, got, want)
		}
	}
}
