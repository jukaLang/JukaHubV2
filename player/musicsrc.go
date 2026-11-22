package main

// musicsrc.go — where the iPod screen's lists come from: this device (the
// media library index), a Navidrome server (the Subsonic API,
// musicsrc_subsonic.go), a Jellyfin server (musicsrc_jellyfin.go), or all
// three at once with the duplicates folded together.
//
// Folding: an album that is on the card and on the server is one row, and
// its tracks come from the card first (no network, no cache), then Navidrome,
// then Jellyfin. Names are compared without case, punctuation or a leading
// "The", so "The Beatles" and "Beatles, The" meet. The library is the media
// index's JSON (mlib.go) plus the servers' answers kept in memory for a few
// minutes; nothing here writes a second database.
//
// Remote tracks play through the loopback proxy (musicproxy.go), whose
// addresses carry no password, so ffmpeg's command line and error output
// never hold one. The servers and accounts live in musicservers.json in the
// data folder (passwords and tokens encrypted, like webdav.json), and come
// from the phone (companion_music.go) or Jellyfin's Quick Connect.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type musicSrcKind string

const (
	musicSrcLocal     musicSrcKind = "local"
	musicSrcNavidrome musicSrcKind = "navidrome"
	musicSrcJellyfin  musicSrcKind = "jellyfin"
	musicSrcAll       musicSrcKind = "all"
)

func (k musicSrcKind) label() string {
	switch k {
	case musicSrcNavidrome:
		return "Navidrome"
	case musicSrcJellyfin:
		return "Jellyfin"
	case musicSrcAll:
		return "All sources"
	}
	return "This device"
}

// musicSourceKey is the Custom setting for the iPod screen's source.
const musicSourceKey = "musicSource"

func musicSourceFromConfig(config *Config) musicSrcKind {
	if config == nil {
		return musicSrcLocal
	}
	switch musicSrcKind(strings.ToLower(strings.TrimSpace(customStringValue(config.Variables.Custom, musicSourceKey)))) {
	case musicSrcNavidrome:
		return musicSrcNavidrome
	case musicSrcJellyfin:
		return musicSrcJellyfin
	case musicSrcAll:
		return musicSrcAll
	}
	return musicSrcLocal
}

func musicSetSource(config *Config, k musicSrcKind) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicSourceKey] = string(k)
	musicSrcCacheClear()
}

// musicSrcCycle steps through the sources.
func musicSrcCycle(k musicSrcKind, dir int) musicSrcKind {
	order := []musicSrcKind{musicSrcLocal, musicSrcNavidrome, musicSrcJellyfin, musicSrcAll}
	at := 0
	for i, v := range order {
		if v == k {
			at = i
		}
	}
	if dir < 0 {
		return order[(at+len(order)-1)%len(order)]
	}
	return order[(at+1)%len(order)]
}

// musicItem is one row of a library list from any source.
type musicItem struct {
	Kind     string // artist, album, track, genre, composer, playlist, movie, series, season, episode
	Src      musicSrcKind
	ID       string // the source's id; a local item's path or group key
	Title    string
	Artist   string
	Album    string
	AlbumID  string
	ArtistID string
	Genre    string
	Year     int
	Track    int
	Disc     int
	Duration float64
	Count    int    // songs or albums in a group
	Art      string // a picture: a local file, "nd:<id>" or "jf:<id>"
	Path     string // what plays: a local file or the proxy's address
	Suffix   string
	Size     int64
	Position float64 // where a video was left, seconds
	Played   bool
	Starred  bool
	MediaID  string // Jellyfin's media source of a video
	Direct   bool   // a Jellyfin video the handheld can play as it is
	Parts    []musicItem
}

// ──────────────────────────────────────────────────────────────────────
// Servers and accounts
// ──────────────────────────────────────────────────────────────────────

type musicNavidromeConf struct {
	URL      string `json:"url,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"` // encrypted
	Scrobble bool   `json:"scrobble"`
	Quality  int    `json:"quality,omitempty"` // kbps cap, 0 = the original file
}

type musicJellyfinConf struct {
	URL        string `json:"url,omitempty"`
	User       string `json:"user,omitempty"`
	UserID     string `json:"userId,omitempty"`
	Token      string `json:"token,omitempty"` // encrypted
	ServerID   string `json:"serverId,omitempty"`
	ServerName string `json:"serverName,omitempty"`
}

type musicLastFMConf struct {
	User    string `json:"user,omitempty"`
	Session string `json:"session,omitempty"` // encrypted
	APIKey  string `json:"apiKey,omitempty"`  // the player's own API account
	Secret  string `json:"secret,omitempty"`  // encrypted
}

type musicListenBrainzConf struct {
	User  string `json:"user,omitempty"`
	Token string `json:"token,omitempty"` // encrypted
}

// musicServersFile is musicservers.json.
type musicServersFile struct {
	V            int                   `json:"v"`
	DeviceID     string                `json:"deviceId,omitempty"`
	Navidrome    musicNavidromeConf    `json:"navidrome"`
	Jellyfin     musicJellyfinConf     `json:"jellyfin"`
	LastFM       musicLastFMConf       `json:"lastfm"`
	ListenBrainz musicListenBrainzConf `json:"listenbrainz"`
}

var musicServersStore struct {
	mu     sync.Mutex
	loaded bool
	v      musicServersFile
}

// musicServersPath is a test seam.
var musicServersPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "musicservers.json")
}

func musicServersLoadLocked() {
	if musicServersStore.loaded {
		return
	}
	musicServersStore.loaded = true
	p := musicServersPath()
	if p == "" {
		return
	}
	var v musicServersFile
	if _, err := readJSONWithBackup(p, &v); err == nil {
		musicServersStore.v = v
	} else if !os.IsNotExist(err) {
		logWarn("music", "ignoring musicservers.json: %v", err)
	}
}

// musicServersGet is the stored servers and accounts.
func musicServersGet() musicServersFile {
	musicServersStore.mu.Lock()
	defer musicServersStore.mu.Unlock()
	musicServersLoadLocked()
	return musicServersStore.v
}

// musicServersUpdate changes the store and writes it (0600: it holds tokens).
func musicServersUpdate(change func(*musicServersFile)) error {
	musicServersStore.mu.Lock()
	defer musicServersStore.mu.Unlock()
	musicServersLoadLocked()
	next := musicServersStore.v
	change(&next)
	next.V = 1
	p := musicServersPath()
	if p == "" {
		return errors.New("the settings folder is unavailable")
	}
	data, err := jsonMarshalIndent(next)
	if err != nil {
		return err
	}
	if err := AtomicWrite(p, data, 0o600); err != nil {
		return err
	}
	musicServersStore.v = next
	musicSrcCacheClear()
	return nil
}

// musicDeviceID is this handheld's id for the servers (Jellyfin lists its
// sessions by it), made once.
func musicDeviceID() string {
	if id := musicServersGet().DeviceID; id != "" {
		return id
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	id := "jukahub-" + hex.EncodeToString(b[:])
	_ = musicServersUpdate(func(s *musicServersFile) {
		if s.DeviceID == "" {
			s.DeviceID = id
		}
		id = s.DeviceID
	})
	return id
}

// musicDecryptField decrypts a stored secret ("" when none or unreadable).
func musicDecryptField(enc string) string {
	if enc == "" {
		return ""
	}
	v, err := DecryptString(enc)
	if err != nil {
		return ""
	}
	return v
}

// musicServersSummary is the Audio settings row's value.
func musicServersSummary() string {
	s := musicServersGet()
	var parts []string
	if s.Navidrome.URL != "" && s.Navidrome.User != "" {
		parts = append(parts, "Navidrome")
	}
	if s.Jellyfin.Token != "" {
		parts = append(parts, "Jellyfin")
	}
	if s.LastFM.Session != "" {
		parts = append(parts, "Last.fm")
	}
	if s.ListenBrainz.Token != "" {
		parts = append(parts, "ListenBrainz")
	}
	if len(parts) == 0 {
		return "None"
	}
	return strings.Join(parts, " · ")
}

// musicSrcReady reports whether a source can be asked.
func musicSrcReady(k musicSrcKind) bool {
	s := musicServersGet()
	switch k {
	case musicSrcNavidrome:
		return s.Navidrome.URL != "" && s.Navidrome.User != "" && s.Navidrome.Password != ""
	case musicSrcJellyfin:
		return s.Jellyfin.URL != "" && s.Jellyfin.Token != "" && s.Jellyfin.UserID != ""
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// HTTP
// ──────────────────────────────────────────────────────────────────────

// musicHTTPClient is an https client with the firmware CA fallback a
// handheld needs (bugrepRootCAs), for public services.
func musicHTTPClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   12 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
	}
	if pool := bugrepRootCAs(); pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

var musicServerHTTP struct {
	once sync.Once
	tls  *http.Client
	lan  *http.Client
}

// musicServerClient is the client for a user's own server: https with the CA
// fallback, or plain http that only dials home-network (and Tailscale)
// addresses, as WebDAV does: the password or token goes with every request,
// and over plain http on the internet anyone on the way could read it. It
// follows a redirect only to the same host. No overall timeout: streams last
// as long as a song; every call carries a context.
func musicServerClient(scheme string) *http.Client {
	musicServerHTTP.once.Do(func() {
		sameHost := func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || len(via) == 0 || !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				return http.ErrUseLastResponse
			}
			return nil
		}
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   12 * time.Second,
			ResponseHeaderTimeout: 45 * time.Second,
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
		}
		if pool := bugrepRootCAs(); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		musicServerHTTP.tls = &http.Client{Transport: tr, CheckRedirect: sameHost}
		lan := &http.Transport{
			DialContext:           webdavLANDial,
			ResponseHeaderTimeout: 45 * time.Second,
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
		}
		musicServerHTTP.lan = &http.Client{Transport: lan, CheckRedirect: sameHost}
	})
	if strings.EqualFold(scheme, "http") {
		return musicServerHTTP.lan
	}
	return musicServerHTTP.tls
}

// musicCleanServerURL checks a server address typed or sent from the phone:
// http(s), no user name in it, no query; a bare host is http (a server at
// home). The path is kept (a server behind a reverse proxy at /navidrome).
func musicCleanServerURL(raw string) (string, error) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "\"'`<>"))
	if s == "" {
		return "", errors.New("type the server's address first")
	}
	if len(s) > 400 {
		return "", errors.New("that address is too long")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("that is not a server address — it looks like http://192.168.1.20:4533")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("the address must start with http:// or https://")
	}
	if u.User != nil {
		return "", errors.New("put the user name and password in their own fields, not in the address")
	}
	if scheme == "http" && !webdavLANHost(u.Hostname()) {
		return "", errWebdavNotLAN
	}
	u.Scheme = scheme
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// ──────────────────────────────────────────────────────────────────────
// Now playing details of remote tracks
// ──────────────────────────────────────────────────────────────────────

// musicMeta is what the player knows about a track beyond its address.
type musicMeta struct {
	Title, Artist, Album, Art string
	Src                       musicSrcKind
	ID                        string
	Duration                  float64
	Track                     int
}

var musicMetaReg struct {
	mu sync.Mutex
	m  map[string]musicMeta
}

// musicMetaRemember keeps a playable item's details by its path.
func musicMetaRemember(it musicItem) {
	if it.Path == "" {
		return
	}
	musicMetaReg.mu.Lock()
	defer musicMetaReg.mu.Unlock()
	if musicMetaReg.m == nil || len(musicMetaReg.m) > 20000 {
		musicMetaReg.m = map[string]musicMeta{}
	}
	musicMetaReg.m[it.Path] = musicMeta{Title: it.Title, Artist: it.Artist, Album: it.Album, Art: it.Art,
		Src: it.Src, ID: it.ID, Duration: it.Duration, Track: it.Track}
}

// musicMetaFor is a track's details: a remote one's from the list it was
// played from, a file's from the media library.
func musicMetaFor(path string) (musicMeta, bool) {
	musicMetaReg.mu.Lock()
	meta, ok := musicMetaReg.m[path]
	musicMetaReg.mu.Unlock()
	if ok {
		return meta, true
	}
	if m := mlibCurrentModel(); m != nil {
		if i, found := m.byPath[dlPathKey(path)]; found {
			it := m.items[i]
			return musicMeta{Title: it.Title, Artist: it.Artist, Album: it.Album, Art: it.Art,
				Src: musicSrcLocal, ID: it.Path, Duration: it.Duration, Track: it.Track}, true
		}
	}
	return musicMeta{}, false
}

// ──────────────────────────────────────────────────────────────────────
// Queries
// ──────────────────────────────────────────────────────────────────────

// musicQuery is one list the iPod screen asks for. Parent is the row it was
// opened from (an artist's albums, an album's tracks); Text a search.
type musicQuery struct {
	What   string
	Parent musicItem
	Text   string
}

// musicSrcCache keeps the servers' answers for a few minutes, so Back and
// forth through a big library does not ask again each time.
var musicSrcCache struct {
	mu sync.Mutex
	m  map[string]musicSrcCached
}

type musicSrcCached struct {
	at    time.Time
	items []musicItem
}

const musicSrcCacheTTL = 10 * time.Minute

func musicSrcCacheClear() {
	musicSrcCache.mu.Lock()
	musicSrcCache.m = nil
	musicSrcCache.mu.Unlock()
}

func musicSrcCacheKey(src musicSrcKind, q musicQuery) string {
	return string(src) + "|" + q.What + "|" + string(q.Parent.Src) + ":" + q.Parent.ID + "|" + q.Parent.Title + "|" + q.Text
}

// musicFetch answers a query from one source, or from all of them folded
// together. Any goroutine but the main one: the servers are asked here.
func musicFetch(ctx context.Context, src musicSrcKind, q musicQuery) ([]musicItem, error) {
	if src != musicSrcLocal && q.What != "random" {
		key := musicSrcCacheKey(src, q)
		musicSrcCache.mu.Lock()
		c, ok := musicSrcCache.m[key]
		musicSrcCache.mu.Unlock()
		if ok && time.Since(c.at) < musicSrcCacheTTL {
			return c.items, nil
		}
		items, err := musicFetchFresh(ctx, src, q)
		if err == nil {
			musicSrcCache.mu.Lock()
			if musicSrcCache.m == nil || len(musicSrcCache.m) > 400 {
				musicSrcCache.m = map[string]musicSrcCached{}
			}
			musicSrcCache.m[key] = musicSrcCached{at: time.Now(), items: items}
			musicSrcCache.mu.Unlock()
		}
		return items, err
	}
	return musicFetchFresh(ctx, src, q)
}

func musicFetchFresh(ctx context.Context, src musicSrcKind, q musicQuery) ([]musicItem, error) {
	switch src {
	case musicSrcNavidrome:
		if !musicSrcReady(src) {
			return nil, errors.New("no Navidrome server yet — Settings › Music servers")
		}
		return ndFetch(ctx, q)
	case musicSrcJellyfin:
		if !musicSrcReady(src) {
			return nil, errors.New("no Jellyfin server yet — Settings › Music servers")
		}
		return jfFetch(ctx, q)
	case musicSrcAll:
		return musicFetchAll(ctx, q)
	}
	return musicLocalFetch(q), nil
}

// musicFetchAll asks every source that is set up and folds the answers.
func musicFetchAll(ctx context.Context, q musicQuery) ([]musicItem, error) {
	// A folded parent is asked through each of its parts.
	if len(q.Parent.Parts) > 0 {
		var lists [][]musicItem
		var firstErr error
		for _, part := range q.Parent.Parts {
			sub := q
			sub.Parent = part
			items, err := musicFetch(ctx, part.Src, sub)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			lists = append(lists, items)
		}
		if len(lists) == 0 && firstErr != nil {
			return nil, firstErr
		}
		return musicFold(lists), nil
	}
	// A row found in one source only is asked there alone: its id means
	// nothing to the others.
	if q.Parent.Kind != "" && q.Parent.Src != "" && q.Parent.Src != musicSrcAll {
		return musicFetch(ctx, q.Parent.Src, q)
	}
	srcs := []musicSrcKind{musicSrcLocal}
	for _, k := range []musicSrcKind{musicSrcNavidrome, musicSrcJellyfin} {
		if musicSrcReady(k) && k != musicSrcLocal && musicSrcConfigured(k) {
			srcs = append(srcs, k)
		}
	}
	type res struct {
		i     int
		items []musicItem
		err   error
	}
	out := make(chan res, len(srcs))
	for i, s := range srcs {
		go func(i int, s musicSrcKind) {
			items, err := musicFetch(ctx, s, q)
			out <- res{i, items, err}
		}(i, s)
	}
	lists := make([][]musicItem, len(srcs))
	var firstErr error
	for range srcs {
		r := <-out
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		lists[r.i] = r.items
	}
	folded := musicFold(lists)
	if len(folded) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return folded, nil
}

// musicSrcConfigured reports whether a server is set at all (ready may still
// fail on a refused password).
func musicSrcConfigured(k musicSrcKind) bool {
	s := musicServersGet()
	switch k {
	case musicSrcNavidrome:
		return s.Navidrome.URL != ""
	case musicSrcJellyfin:
		return s.Jellyfin.URL != ""
	}
	return true
}

// musicNorm is a name compared across sources: lower case, letters and digits
// only, a leading "the" dropped. Pure.
func musicNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if strings.HasPrefix(out, "the") && len(out) > 5 {
		out = out[3:]
	}
	return out
}

// musicFoldKey is what makes two rows the same thing. Pure.
func musicFoldKey(it musicItem) string {
	switch it.Kind {
	case "artist", "genre", "composer":
		return it.Kind + "|" + musicNorm(it.Title)
	case "album":
		return "album|" + musicNorm(it.Artist) + "|" + musicNorm(it.Title)
	case "track":
		if it.Track > 0 && it.Album != "" {
			return "track|" + musicNorm(it.Album) + "|" + strconv.Itoa(it.Disc) + "|" + strconv.Itoa(it.Track)
		}
		return "track|" + musicNorm(it.Artist) + "|" + musicNorm(it.Title)
	}
	// Playlists, films and shows from different servers stay apart.
	return string(it.Src) + "|" + it.Kind + "|" + it.ID
}

// musicFold merges lists in source order (earlier wins): a row found in two
// sources keeps the first one's fields and both as Parts. Pure.
func musicFold(lists [][]musicItem) []musicItem {
	var out []musicItem
	at := map[string]int{}
	for _, list := range lists {
		for _, it := range list {
			key := musicFoldKey(it)
			if i, ok := at[key]; ok {
				if len(out[i].Parts) == 0 {
					out[i].Parts = []musicItem{musicItemPart(out[i])}
				}
				out[i].Parts = append(out[i].Parts, musicItemPart(it))
				if out[i].Count < it.Count {
					out[i].Count = it.Count
				}
				if out[i].Art == "" {
					out[i].Art = it.Art
				}
				continue
			}
			at[key] = len(out)
			out = append(out, it)
		}
	}
	if len(lists) > 1 {
		sortItems(out)
	}
	return out
}

// musicItemPart is it without its own parts (a part never nests).
func musicItemPart(it musicItem) musicItem {
	it.Parts = nil
	return it
}

// sortItems orders a folded list the way a single source would.
func sortItems(items []musicItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Kind == "track" && b.Kind == "track" && a.Album != "" && strings.EqualFold(a.Album, b.Album) {
			if a.Disc != b.Disc {
				return a.Disc < b.Disc
			}
			if a.Track != b.Track {
				return a.Track < b.Track
			}
		}
		return musicNorm(a.Title) < musicNorm(b.Title)
	})
}

// musicItemTrackName is the name a track gets in the player's list: its
// title with the file's extension, which the player strips again (a title
// with a dot, "Mr. Brightside", would otherwise lose everything after it).
func musicItemTrackName(it musicItem) string {
	ext := strings.TrimPrefix(it.Suffix, ".")
	if ext == "" {
		ext = "mp3"
	}
	title := it.Title
	if title == "" {
		title = "Track"
	}
	return title + "." + ext
}

// musicPlayItems plays the tracks of items from start (an index into items)
// in the player, the rest of the list queued behind it. Main thread.
func musicPlayItems(config *Config, items []musicItem, start int, shuffle bool) bool {
	tracks := make([]musicTrack, 0, len(items))
	at := 0
	for i, it := range items {
		if it.Kind != "track" || it.Path == "" {
			continue
		}
		if i == start {
			at = len(tracks)
		}
		musicMetaRemember(it)
		tracks = append(tracks, musicTrack{Path: it.Path, Name: musicItemTrackName(it), Duration: it.Duration})
	}
	if len(tracks) == 0 {
		showToast("Nothing to play here", ToastInfo())
		return false
	}
	if shuffle {
		at = 0
	}
	if !musicPlayPlaylist(tracks, at) {
		return false
	}
	if shuffle {
		music.mu.Lock()
		music.shuffle = true
		music.order = buildShuffleOrder(len(music.tracks), mathrand.Intn(len(music.tracks)), nil)
		music.orderPos = 0
		if len(music.order) > 0 {
			at = music.order[0]
		}
		music.mu.Unlock()
	}
	openMusicPlayerScene(config)
	musicPlayTrack(at)
	return true
}

// jsonMarshalIndent is the store's encoding with a final newline.
func jsonMarshalIndent(v interface{}) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
