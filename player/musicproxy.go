package main

// musicproxy.go — the loopback proxy that remote music and Jellyfin video
// play through, the song cache behind it, and "Download offline".
//
// Why a proxy: ffmpeg (the music decoder and the video player) is handed
// http://127.0.0.1:47533/… instead of the server's address, so the password
// token or access token never appears on a command line, in errors.txt or in
// a bug report; the proxy adds it on the way out. It also caches: a song is
// fetched into the cache as fast as the network allows while it plays
// (ffmpeg reads the growing file), so the next play, a seek, or a Wi-Fi drop
// mid-song costs nothing. The cache keeps the newest songs under a size cap
// (Settings › Offline cache, 2 GB by default) and is only a cache.
//
// "Download offline" is different: it saves the original file into
// Music/Navidrome (or Music/Jellyfin, Videos/Jellyfin) under Artist/Album,
// where the media library finds it like any other song, and the folded
// library then plays the card's copy first.
//
// Routes: /nd/<server>/<id>.<ext> and /jf/<server>/<id>.<ext> (a song),
// /jfv/<server>/<id>/<media source>.<ext> (a video as it is, with ranges),
// /jfh/<server>/<id>/… (the server's HLS transcode, playlists and segments
// passed through, so a seek in a transcoded film costs a segment, not the
// whole film). <server> is a short hash of the address and account: a
// stale address from another server is refused.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	musicProxyPreferredAddr = "127.0.0.1:47533"
	musicCacheKey           = "musicCacheMB"
	musicCacheDefaultMB     = 2048
	// musicFillAhead is how far ahead of the download a range may start and
	// still wait for it; further ahead (an M4A's index at the end, an MP3's
	// ID3v1 tag) is fetched from the server on its own.
	musicFillAhead = 512 << 10
)

var musicProxy struct {
	once  sync.Once
	addr  string
	mu    sync.Mutex
	fills map[string]*musicCacheFill
}

// musicProxyStart starts the proxy the first time and returns its address.
// The preferred port keeps the addresses (and so the saved places, history
// and playlists keyed by them) the same from one run to the next.
func musicProxyStart() string {
	musicProxy.once.Do(func() {
		ln, err := net.Listen("tcp", musicProxyPreferredAddr)
		if err != nil {
			ln, err = net.Listen("tcp", "127.0.0.1:0")
		}
		if err != nil {
			logWarn("music", "proxy: cannot listen: %v", err)
			return
		}
		musicProxy.addr = ln.Addr().String()
		srv := &http.Server{Handler: http.HandlerFunc(musicProxyServe), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logWarn("music", "proxy stopped: %v", err)
			}
		}()
	})
	return musicProxy.addr
}

// musicServerTag is a short name for a server and account. Pure.
func musicServerTag(prefix, serverURL, user string) string {
	sum := sha256.Sum256([]byte(prefix + "\n" + strings.ToLower(serverURL) + "\n" + user))
	return hex.EncodeToString(sum[:4])
}

func musicProxyBase() string {
	addr := musicProxyStart()
	if addr == "" {
		addr = musicProxyPreferredAddr
	}
	return "http://" + addr
}

// musicProxyTrackURL is where a remote song plays from.
func musicProxyTrackURL(src musicSrcKind, srv, id, suffix string) string {
	route := "nd"
	if src == musicSrcJellyfin {
		route = "jf"
	}
	return musicProxyBase() + "/" + route + "/" + srv + "/" + url.PathEscape(id) + "." + musicSafeExt(suffix)
}

// musicProxyVideoURL is where a Jellyfin video plays from: mode "d" as it
// is, "t" the server's transcode.
func musicProxyVideoURL(srv, id, mediaID, mode, suffix string) string {
	if mode == "d" {
		ext := musicSafeExt(suffix)
		switch ext {
		case "mp4", "m4v", "mkv", "webm", "mov", "ts":
		default:
			ext = "mkv" // the player recognises a video by its extension
		}
		m := mediaID
		if m == "" {
			m = "-"
		}
		return musicProxyBase() + "/jfv/" + srv + "/" + url.PathEscape(id) + "/" + url.PathEscape(m) + "." + ext
	}
	q := url.Values{"MediaSourceId": {mediaID}, "VideoCodec": {"h264"}, "AudioCodec": {"aac"},
		"MaxWidth": {"1280"}, "MaxHeight": {"720"}, "VideoBitrate": {"3000000"}, "AudioBitrate": {"160000"},
		"MaxAudioChannels": {"2"}, "SegmentContainer": {"ts"}, "DeviceId": {musicDeviceID()},
		"PlaySessionId": {musicPlaySession()}}
	return musicProxyBase() + "/jfh/" + srv + "/" + url.PathEscape(id) + "/master.m3u8?" + q.Encode()
}

var musicSessionCounter atomic.Uint64

// musicPlaySession is a fresh Jellyfin play session id.
func musicPlaySession() string {
	return fmt.Sprintf("jh%x%x", time.Now().UnixNano(), musicSessionCounter.Add(1))
}

// musicSafeExt keeps an extension to a few lower-case letters and digits.
func musicSafeExt(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
	var b strings.Builder
	for _, r := range ext {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 5 {
			break
		}
	}
	if b.Len() == 0 {
		return "mp3"
	}
	return b.String()
}

// ── Serving ─────────────────────────────────────────────────────────────────

func musicProxyServe(w http.ResponseWriter, r *http.Request) {
	// Only this device's own players: a page that rebinds a name to 127.0.0.1
	// arrives with its own Host.
	if h := r.Host; h != musicProxy.addr && h != musicProxyPreferredAddr {
		http.Error(w, "not here", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/", 3)
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	route, srv, rest := parts[0], parts[1], parts[2]
	switch route {
	case "nd", "jf":
		musicProxySong(w, r, route, srv, rest)
	case "jfv", "jfh":
		musicProxyVideo(w, r, route, srv, rest)
	default:
		http.NotFound(w, r)
	}
}

// musicUpstream builds the request to the real server for a route.
type musicUpstream func(ctx context.Context, rangeHeader string) (*http.Response, error)

// musicUpstreamFor is the upstream of a song (route nd or jf), or nil when
// the address belongs to another server or account.
func musicUpstreamFor(route, srv, id string) musicUpstream {
	switch route {
	case "nd":
		c, err := ssNewClient()
		if err != nil || c.srv != srv {
			return nil
		}
		return func(ctx context.Context, rg string) (*http.Response, error) {
			return musicGet(ctx, c.base.Scheme, c.streamURL(id), "", rg)
		}
	case "jf":
		c, err := jfNewClient()
		if err != nil || c.srv != srv {
			return nil
		}
		path, q := c.audioPath(id)
		return func(ctx context.Context, rg string) (*http.Response, error) {
			return musicGet(ctx, c.base.Scheme, musicJoinURL(c.base, path, q), jfAuthHeader(c.token), rg)
		}
	}
	return nil
}

// musicJoinURL is base + path + query.
func musicJoinURL(base *url.URL, path string, q url.Values) string {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	return u.String()
}

// musicGet is one upstream GET with an optional Authorization header and
// Range.
func musicGet(ctx context.Context, scheme, rawURL, auth, rangeHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	req.Header.Set("User-Agent", "JukaHub/"+strings.TrimPrefix(versionString(nil), "v"))
	return musicServerClient(scheme).Do(req)
}

// musicProxySong serves a song: from the cache, from the download that is
// filling the cache, or straight from the server.
func musicProxySong(w http.ResponseWriter, r *http.Request, route, srv, rest string) {
	dot := strings.LastIndex(rest, ".")
	if dot <= 0 {
		http.NotFound(w, r)
		return
	}
	id, err := url.PathUnescape(rest[:dot])
	ext := musicSafeExt(rest[dot+1:])
	if err != nil || id == "" || strings.ContainsAny(id, "/\\") {
		http.NotFound(w, r)
		return
	}
	up := musicUpstreamFor(route, srv, id)
	cachePath := musicCachePath(route, srv, id, ext)
	if f, err := os.Open(cachePath); err == nil {
		defer f.Close()
		now := time.Now()
		_ = os.Chtimes(cachePath, now, now)
		if st, err := f.Stat(); err == nil {
			w.Header().Set("Content-Type", musicContentType(ext))
			http.ServeContent(w, r, filepath.Base(cachePath), st.ModTime(), f)
			return
		}
	}
	if up == nil {
		http.Error(w, "this song belongs to another server", http.StatusNotFound)
		return
	}
	if musicCacheLimit() <= 0 || cachePath == "" {
		musicProxyPass(w, r, up)
		return
	}
	fill := musicCacheFillFor(cachePath, up)
	start, end, ranged := musicParseRange(r.Header.Get("Range"))
	if fill.waitHeader(r.Context()) != nil {
		fill.leave()
		musicProxyPass(w, r, up)
		return
	}
	if ranged && start > fill.progress()+musicFillAhead {
		// Far ahead of the download: ask the server for that part alone.
		fill.leave()
		musicProxyPass(w, r, up)
		return
	}
	fill.serve(w, r, start, end, ranged, ext)
}

// musicProxyPass copies one upstream answer through, Range and all.
func musicProxyPass(w http.ResponseWriter, r *http.Request, up musicUpstream) {
	resp, err := up(r.Context(), r.Header.Get("Range"))
	if err != nil {
		http.Error(w, "the server did not answer", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

// musicProxyVideo passes a Jellyfin video through (route jfv: the file with
// ranges; jfh: the HLS transcode, any path under the item).
func musicProxyVideo(w http.ResponseWriter, r *http.Request, route, srv, rest string) {
	c, err := jfNewClient()
	if err != nil || c.srv != srv {
		http.Error(w, "this video belongs to another server", http.StatusNotFound)
		return
	}
	idEsc, tail, ok := strings.Cut(rest, "/")
	id, err := url.PathUnescape(idEsc)
	if !ok || err != nil || id == "" || strings.ContainsAny(id, "/\\") {
		http.NotFound(w, r)
		return
	}
	var target string
	switch route {
	case "jfv":
		media := strings.TrimSuffix(tail, filepath.Ext(tail))
		media, _ = url.PathUnescape(media)
		path, q := c.videoPath(id, media, true, 0, "")
		if media == "-" || media == "" {
			q.Del("MediaSourceId")
		}
		target = musicJoinURL(c.base, path, q)
	default:
		// The transcode's playlists name their segments relative to
		// themselves, so everything under the item maps one to one.
		if strings.Contains(tail, "..") {
			http.NotFound(w, r)
			return
		}
		u := *c.base
		u.Path = strings.TrimRight(u.Path, "/") + "/Videos/" + url.PathEscape(id) + "/"
		u.RawPath = ""
		target = u.String() + tail
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
	}
	auth := jfAuthHeader(c.token)
	musicProxyPass(w, r, func(ctx context.Context, rg string) (*http.Response, error) {
		return musicGet(ctx, c.base.Scheme, target, auth, rg)
	})
}

// musicParseRange reads "bytes=start-" or "bytes=start-end". Pure.
func musicParseRange(h string) (start, end int64, ok bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(h, "bytes=") || strings.Contains(h, ",") {
		return 0, -1, false
	}
	a, b, found := strings.Cut(strings.TrimPrefix(h, "bytes="), "-")
	if !found || a == "" {
		return 0, -1, false
	}
	s, err := strconv.ParseInt(strings.TrimSpace(a), 10, 64)
	if err != nil || s < 0 {
		return 0, -1, false
	}
	e := int64(-1)
	if strings.TrimSpace(b) != "" {
		if v, err := strconv.ParseInt(strings.TrimSpace(b), 10, 64); err == nil && v >= s {
			e = v
		}
	}
	return s, e, true
}

func musicContentType(ext string) string {
	switch ext {
	case "flac":
		return "audio/flac"
	case "ogg", "oga", "opus":
		return "audio/ogg"
	case "m4a", "m4b", "aac", "mp4":
		return "audio/mp4"
	case "wav":
		return "audio/wav"
	}
	return "audio/mpeg"
}

// ── The cache ───────────────────────────────────────────────────────────────

// musicCacheDir is where cached songs live.
func musicCacheDir() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "music-cache")
}

func musicCachePath(route, srv, id, ext string) string {
	dir := musicCacheDir()
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, route, srv, hex.EncodeToString(sum[:10])+"."+ext)
}

// musicCacheLimitMB is the setting the main thread publishes for the proxy.
var musicCacheLimitMB atomic.Int64

func init() { musicCacheLimitMB.Store(musicCacheDefaultMB) }

// musicCacheLimit is the cap in bytes, 0 when caching is off.
func musicCacheLimit() int64 { return musicCacheLimitMB.Load() << 20 }

// musicCacheBind reads the setting (main thread).
func musicCacheBind(config *Config) {
	mb := int64(musicCacheDefaultMB)
	if config != nil {
		if raw := strings.TrimSpace(customStringValue(config.Variables.Custom, musicCacheKey)); raw != "" {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v >= 0 {
				mb = v
			}
		}
	}
	musicCacheLimitMB.Store(mb)
}

// musicCacheSteps are the sizes Settings offers.
var musicCacheSteps = []int64{0, 512, 1024, 2048, 4096, 8192}

func musicCacheSetMB(config *Config, mb int64) {
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom[musicCacheKey] = strconv.FormatInt(mb, 10)
	}
	musicCacheLimitMB.Store(mb)
	go musicCacheTrim()
}

// musicCacheUsage is the cache's size in bytes and files.
func musicCacheUsage() (int64, int) {
	var total int64
	n := 0
	_ = filepath.Walk(musicCacheDir(), func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && !strings.HasSuffix(p, ".part") {
			total += info.Size()
			n++
		}
		return nil
	})
	return total, n
}

// musicCacheTrim drops the songs played longest ago until the cache fits.
func musicCacheTrim() {
	limit := musicCacheLimit()
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var files []entry
	var total int64
	_ = filepath.Walk(musicCacheDir(), func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if strings.HasSuffix(p, ".part") && time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(p) // a download a crash left behind
				return nil
			}
			files = append(files, entry{p, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	if total <= limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	musicProxy.mu.Lock()
	busy := map[string]bool{}
	for k := range musicProxy.fills {
		busy[k] = true
		busy[k+".part"] = true
	}
	musicProxy.mu.Unlock()
	for _, f := range files {
		if total <= limit {
			break
		}
		if busy[f.path] {
			continue
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// musicCacheClear empties the cache.
func musicCacheClear() {
	musicProxy.mu.Lock()
	busy := len(musicProxy.fills) > 0
	musicProxy.mu.Unlock()
	if busy {
		// Songs still downloading keep their folder; the rest goes.
		_ = filepath.Walk(musicCacheDir(), func(p string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() && !strings.HasSuffix(p, ".part") {
				_ = os.Remove(p)
			}
			return nil
		})
		return
	}
	_ = os.RemoveAll(musicCacheDir())
}

// musicCacheFill is one song being downloaded into the cache while any
// number of readers stream it.
type musicCacheFill struct {
	path    string
	mu      sync.Mutex
	cond    *sync.Cond
	size    int64 // -1 while unknown or when the server does not say
	ctype   string
	head    bool // the server answered (size and type are known)
	written int64
	done    bool
	err     error
	readers int
	cancel  context.CancelFunc
}

// musicCacheFillFor joins the download of path, starting it if none runs.
func musicCacheFillFor(path string, up musicUpstream) *musicCacheFill {
	musicProxy.mu.Lock()
	defer musicProxy.mu.Unlock()
	if musicProxy.fills == nil {
		musicProxy.fills = map[string]*musicCacheFill{}
	}
	if f, ok := musicProxy.fills[path]; ok {
		f.mu.Lock()
		f.readers++
		f.mu.Unlock()
		return f
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &musicCacheFill{path: path, size: -1, readers: 1, cancel: cancel}
	f.cond = sync.NewCond(&f.mu)
	musicProxy.fills[path] = f
	go f.run(ctx, up)
	return f
}

func (f *musicCacheFill) finish(err error) {
	f.mu.Lock()
	f.done, f.err, f.head = true, err, true
	f.cond.Broadcast()
	f.mu.Unlock()
	musicProxy.mu.Lock()
	if musicProxy.fills[f.path] == f {
		delete(musicProxy.fills, f.path)
	}
	musicProxy.mu.Unlock()
}

// run downloads the whole song into path.part and renames it when complete.
func (f *musicCacheFill) run(ctx context.Context, up musicUpstream) {
	part := f.path + ".part"
	resp, err := up(ctx, "")
	if err != nil {
		f.finish(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.finish(fmt.Errorf("the server answered %d", resp.StatusCode))
		return
	}
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		f.finish(err)
		return
	}
	out, err := os.Create(part)
	if err != nil {
		f.finish(err)
		return
	}
	f.mu.Lock()
	f.size = resp.ContentLength
	f.ctype = resp.Header.Get("Content-Type")
	f.head = true
	f.cond.Broadcast()
	f.mu.Unlock()
	buf := make([]byte, 64<<10)
	var werr error
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				werr = err
				break
			}
			f.mu.Lock()
			f.written += int64(n)
			f.cond.Broadcast()
			f.mu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			werr = rerr
			break
		}
	}
	cerr := out.Close()
	f.mu.Lock()
	complete := werr == nil && cerr == nil && (f.size < 0 || f.written == f.size)
	f.mu.Unlock()
	if !complete {
		_ = os.Remove(part)
		if werr == nil {
			werr = errors.New("the download stopped early")
		}
		f.finish(werr)
		return
	}
	if err := os.Rename(part, f.path); err != nil {
		_ = os.Remove(part)
		f.finish(err)
		return
	}
	// Readers still streaming read path.part by its open handle; a reader
	// that opens now finds the finished file.
	f.finish(nil)
	go musicCacheTrim()
}

// waitHeader waits until the server has answered (or failed).
func (f *musicCacheFill) waitHeader(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() {
		f.mu.Lock()
		f.cond.Broadcast()
		f.mu.Unlock()
	})
	defer stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for !f.head && ctx.Err() == nil {
		f.cond.Wait()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.done && f.err != nil && f.written == 0 {
		return f.err
	}
	return nil
}

func (f *musicCacheFill) progress() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written
}

// leave drops a reader; the last one leaving a download that is not yet
// half done stops it (a skipped song is not worth the rest of the download).
func (f *musicCacheFill) leave() {
	f.mu.Lock()
	f.readers--
	stop := f.readers <= 0 && !f.done && (f.size <= 0 || f.written < f.size/2)
	f.mu.Unlock()
	if stop {
		f.cancel()
	}
}

// serve streams bytes start..end (end -1: to the end) of the song as they
// arrive.
func (f *musicCacheFill) serve(w http.ResponseWriter, r *http.Request, start, end int64, ranged bool, ext string) {
	defer f.leave()
	f.mu.Lock()
	size, ctype := f.size, f.ctype
	f.mu.Unlock()
	if ctype == "" || strings.HasPrefix(ctype, "application/octet") {
		ctype = musicContentType(ext)
	}
	w.Header().Set("Content-Type", ctype)
	if size >= 0 {
		w.Header().Set("Accept-Ranges", "bytes")
		if end < 0 || end >= size {
			end = size - 1
		}
		if start > end && size > 0 {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		if ranged {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	} else {
		if start > 0 {
			// A stream of unknown length (a transcode) cannot start midway.
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead {
		return
	}
	in, err := os.Open(f.path + ".part")
	if err != nil {
		// Finished meanwhile: the cache file.
		if in, err = os.Open(f.path); err != nil {
			return
		}
	}
	defer in.Close()
	ctx := r.Context()
	stop := context.AfterFunc(ctx, func() {
		f.mu.Lock()
		f.cond.Broadcast()
		f.mu.Unlock()
	})
	defer stop()
	buf := make([]byte, 64<<10)
	pos := start
	flusher, _ := w.(http.Flusher)
	for end < 0 || pos <= end {
		f.mu.Lock()
		for f.written <= pos && !f.done && ctx.Err() == nil {
			f.cond.Wait()
		}
		avail, done, ferr := f.written, f.done, f.err
		f.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if avail <= pos {
			if done {
				_ = ferr // the reader sees the stream end early
				return
			}
			continue
		}
		want := avail - pos
		if end >= 0 && end-pos+1 < want {
			want = end - pos + 1
		}
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		n, err := in.ReadAt(buf[:want], pos)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			pos += int64(n)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil && err != io.EOF {
			return
		}
	}
}

// ── Download offline ────────────────────────────────────────────────────────

// musicDownloads is the queue of "Download offline" jobs. Its fields are
// read by the screen; the worker goroutine writes them under mu.
var musicDownloads struct {
	mu      sync.Mutex
	queue   []musicItem
	running bool
	done    int
	failed  int
	current string
}

// musicDownloadStatus is a one-line state for Settings and the iPod screen.
func musicDownloadStatus() string {
	musicDownloads.mu.Lock()
	defer musicDownloads.mu.Unlock()
	if musicDownloads.running {
		return fmt.Sprintf("Downloading %s · %d left", musicDownloads.current, len(musicDownloads.queue)+1)
	}
	if musicDownloads.done+musicDownloads.failed > 0 {
		s := fmt.Sprintf("%d downloaded", musicDownloads.done)
		if musicDownloads.failed > 0 {
			s += fmt.Sprintf(", %d failed", musicDownloads.failed)
		}
		return s
	}
	return "Nothing downloading"
}

// musicDownloadOffline queues items (songs, films, episodes of a server) to
// be saved on the card. Main thread.
func musicDownloadOffline(items []musicItem) int {
	n := 0
	musicDownloads.mu.Lock()
	for _, it := range items {
		if it.Src != musicSrcNavidrome && it.Src != musicSrcJellyfin {
			continue
		}
		if it.Kind != "track" && it.Kind != "movie" && it.Kind != "episode" {
			continue
		}
		if dest := musicOfflinePath(it); dest != "" {
			if _, err := os.Stat(dest); err == nil {
				continue // already on the card
			}
		}
		musicDownloads.queue = append(musicDownloads.queue, it)
		n++
	}
	start := !musicDownloads.running && len(musicDownloads.queue) > 0
	if start {
		musicDownloads.running = true
		musicDownloads.done, musicDownloads.failed = 0, 0
	}
	musicDownloads.mu.Unlock()
	if start {
		go musicDownloadWorker()
	}
	return n
}

func musicDownloadWorker() {
	for {
		musicDownloads.mu.Lock()
		if len(musicDownloads.queue) == 0 {
			musicDownloads.running = false
			done, failed := musicDownloads.done, musicDownloads.failed
			musicDownloads.mu.Unlock()
			tbxPost(func() {
				if failed > 0 {
					showToast(fmt.Sprintf("Downloaded %d, %d failed", done, failed), ToastWarn())
				} else {
					showToast(fmt.Sprintf("Downloaded %d for offline", done), ToastSuccess())
				}
				// The library finds the new files and folds them in.
				mlibStartScan(appConfig, false)
			})
			return
		}
		it := musicDownloads.queue[0]
		musicDownloads.queue = musicDownloads.queue[1:]
		musicDownloads.current = it.Title
		musicDownloads.mu.Unlock()
		err := musicDownloadOne(it)
		musicDownloads.mu.Lock()
		if err != nil {
			musicDownloads.failed++
			logWarn("music", "download offline %q: %v", it.Title, err)
		} else {
			musicDownloads.done++
		}
		musicDownloads.mu.Unlock()
	}
}

// musicMediaRoot is the card's folder of a kind ("Music" or "Videos").
func musicMediaRoot(kind string) string {
	for _, r := range mlibDefaultRoots() {
		if strings.EqualFold(filepath.Base(r), kind) {
			return r
		}
	}
	return ""
}

// musicOfflineName keeps a name usable as a file or folder name.
func musicOfflineName(s, fallback string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(strings.TrimSpace(b.String()), ".")
	if out == "" {
		out = fallback
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

// musicOfflinePath is where a downloaded item goes.
func musicOfflinePath(it musicItem) string {
	src := "Navidrome"
	if it.Src == musicSrcJellyfin {
		src = "Jellyfin"
	}
	ext := musicSafeExt(it.Suffix)
	if it.Video {
		root := musicMediaRoot("Videos")
		if root == "" {
			return ""
		}
		dir := filepath.Join(root, src)
		if it.Kind == "episode" && it.Album != "" {
			dir = filepath.Join(dir, musicOfflineName(it.Album, "Show"))
		}
		return filepath.Join(dir, musicOfflineName(it.Title, it.ID)+"."+ext)
	}
	root := musicMediaRoot("Music")
	if root == "" {
		return ""
	}
	name := musicOfflineName(it.Title, it.ID)
	if it.Track > 0 {
		name = fmt.Sprintf("%02d - %s", it.Track, name)
	}
	return filepath.Join(root, src, musicOfflineName(it.Artist, "Unknown artist"),
		musicOfflineName(it.Album, "Unknown album"), name+"."+ext)
}

// musicDownloadOne saves one item: from the cache when it is there, else the
// original file from the server.
func musicDownloadOne(it musicItem) error {
	dest := musicOfflinePath(it)
	if dest == "" {
		return errors.New("no Music or Videos folder on this device")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var up musicUpstream
	switch it.Src {
	case musicSrcNavidrome:
		c, err := ssNewClient()
		if err != nil {
			return err
		}
		if cached := musicCachePath("nd", c.srv, it.ID, musicSafeExt(it.Suffix)); c.conf.Quality == 0 && musicCopyFile(cached, dest) == nil {
			return nil
		}
		up = func(ctx context.Context, rg string) (*http.Response, error) {
			return musicGet(ctx, c.base.Scheme, c.downloadURL(it.ID), "", rg)
		}
	case musicSrcJellyfin:
		c, err := jfNewClient()
		if err != nil {
			return err
		}
		if !it.Video {
			if musicCopyFile(musicCachePath("jf", c.srv, it.ID, musicSafeExt(it.Suffix)), dest) == nil {
				return nil
			}
		}
		path, q := c.audioPath(it.ID)
		if it.Video {
			path, q = c.videoPath(it.ID, it.MediaID, true, 0, "")
		}
		target := musicJoinURL(c.base, path, q)
		auth := jfAuthHeader(c.token)
		up = func(ctx context.Context, rg string) (*http.Response, error) {
			return musicGet(ctx, c.base.Scheme, target, auth, rg)
		}
	default:
		return errors.New("not a server's song")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	resp, err := up(ctx, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, resp.Body)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && resp.ContentLength > 0 && n != resp.ContentLength {
		err = errors.New("the download stopped early")
	}
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, dest)
}

// musicCopyFile copies a cached song to dest.
func musicCopyFile(src, dest string) error {
	if src == "" {
		return os.ErrNotExist
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, dest)
}
