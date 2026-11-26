package main

// radiodir.go — the internet radio directory: stations from Radio Browser.
//
// Radio Browser (radio-browser.info) is a free, community-run list of about
// 50 000 stations with a public JSON API and no key. The Radio directory
// screen (radiodir_ui.go) asks it for the most voted stations, a country's,
// a genre's (a tag) or a name search; A plays a station in the music player
// and X keeps it in the user's stations — the IPStream library
// (ipstreams.json), which the Music screen's Radio shelf, the IPStream panel's
// Radio tab and the idle screen's radio (saverradio.go) all read.
//
// The API is spread over a few servers. de1 is asked first; when it does not
// answer, the others are found the way the API asks clients to: the addresses
// of all.api.radio-browser.info, each looked up back to its name, plus a short
// built-in list. Only https, only *.api.radio-browser.info, a descriptive
// User-Agent, at most radioDirLimit stations a list. The last lists are kept
// in radiodir.json in the data folder, so the screen still shows them offline.
//
// Everything a station says is outside data: names and tags are cut to one
// short line, and only an http(s) link that is a stream (not a .pls/.m3u
// playlist, which ffmpeg cannot follow) is kept.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// radioDirLimit caps one list.
	radioDirLimit = 200
	// radioDirCacheFile keeps the last lists for offline viewing.
	radioDirCacheFile = "radiodir.json"
	// radioDirCacheLists is how many lists the file keeps.
	radioDirCacheLists = 16
	// radioDirFetchMax bounds one answer (200 stations are ~250 KB).
	radioDirFetchMax = 4 << 20
	// radioDirFirstHost is asked first.
	radioDirFirstHost = "de1.api.radio-browser.info"
	// radioDirAllHosts lists every server in DNS.
	radioDirAllHosts = "all.api.radio-browser.info"
)

// radioDirBuiltinHosts are tried after DNS's list.
var radioDirBuiltinHosts = []string{"de2.api.radio-browser.info", "fi1.api.radio-browser.info", "de1.api.radio-browser.info"}

// radioStation is one station as the screen keeps it.
type radioStation struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Country string `json:"cc,omitempty"`
	Tags    string `json:"tags,omitempty"`
	Codec   string `json:"codec,omitempty"`
	Bitrate int    `json:"kbps,omitempty"`
	ID      string `json:"id,omitempty"`
}

// radioCountry is one entry of the country list.
type radioCountry struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Count int    `json:"n"`
}

// radioDirAPIStation is the part of Radio Browser's station JSON read here.
type radioDirAPIStation struct {
	Name        string          `json:"name"`
	URL         string          `json:"url"`
	URLResolved string          `json:"url_resolved"`
	UUID        string          `json:"stationuuid"`
	CountryCode string          `json:"countrycode"`
	Tags        string          `json:"tags"`
	Codec       string          `json:"codec"`
	Bitrate     json.RawMessage `json:"bitrate"`
	LastCheckOK json.RawMessage `json:"lastcheckok"`
}

// radioDirOneLine makes outside text one short line: control characters and
// runs of spaces go, and it is cut to maxRunes.
func radioDirOneLine(s string, maxRunes int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 || r == 0xfeff
	}), " ")
	if r := []rune(s); len(r) > maxRunes {
		s = strings.TrimSpace(string(r[:maxRunes]))
	}
	return s
}

// radioDirStreamURL returns link when it is an http(s) stream the player can
// open: no spaces or control characters, a host, at most 1024 bytes, not a
// playlist file; "" otherwise.
func radioDirStreamURL(link string) string {
	link = strings.TrimSpace(link)
	if link == "" || len(link) > 1024 || !isHTTPMediaURL(link) || saverRadioPlaylistLink(link) {
		return ""
	}
	for _, r := range link {
		if r <= 0x20 || r == 0x7f {
			return ""
		}
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return ""
	}
	return link
}

// radioDirRawInt reads a JSON number that may come as a string.
func radioDirRawInt(raw json.RawMessage) int {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		if n > 1_000_000 {
			return 0
		}
	}
	return n
}

// radioDirParseStations reads a station list answer into the stations the
// player can open, each link once, at most radioDirLimit. Pure: the answer is
// outside data.
func radioDirParseStations(data []byte) ([]radioStation, error) {
	var raw []radioDirAPIStation
	if len(data) > radioDirFetchMax {
		return nil, errors.New("the answer is too large")
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]radioStation, 0, min(len(raw), radioDirLimit))
	for _, s := range raw {
		if ok := strings.TrimSpace(string(s.LastCheckOK)); ok == "0" || ok == `"0"` {
			continue
		}
		link := radioDirStreamURL(s.URLResolved)
		if link == "" {
			link = radioDirStreamURL(s.URL)
		}
		if link == "" || seen[link] {
			continue
		}
		seen[link] = true
		name := radioDirOneLine(s.Name, 80)
		if name == "" {
			name = channelNameFromURL(link)
		}
		cc := strings.ToUpper(strings.TrimSpace(s.CountryCode))
		if !radioDirCountryCodeValid(cc) {
			cc = ""
		}
		id := ""
		if u := strings.ToLower(strings.TrimSpace(s.UUID)); len(u) <= 40 && strings.Trim(u, "0123456789abcdef-") == "" {
			id = u
		}
		out = append(out, radioStation{
			Name: name, URL: link, Country: cc,
			Tags:    radioDirOneLine(strings.ReplaceAll(s.Tags, ",", ", "), 60),
			Codec:   strings.ToUpper(radioDirOneLine(s.Codec, 12)),
			Bitrate: radioDirRawInt(s.Bitrate),
			ID:      id,
		})
		if len(out) >= radioDirLimit {
			break
		}
	}
	return out, nil
}

// radioDirParseCountries reads the country list answer: ISO code, name and
// station count, the codes valid and each once, most stations first.
func radioDirParseCountries(data []byte) ([]radioCountry, error) {
	var raw []struct {
		Name  string          `json:"name"`
		Code  string          `json:"iso_3166_1"`
		Count json.RawMessage `json:"stationcount"`
	}
	if len(data) > radioDirFetchMax {
		return nil, errors.New("the answer is too large")
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []radioCountry
	for _, c := range raw {
		code := strings.ToUpper(strings.TrimSpace(c.Code))
		n := radioDirRawInt(c.Count)
		if !radioDirCountryCodeValid(code) || seen[code] || n <= 0 {
			continue
		}
		seen[code] = true
		name := radioDirOneLine(c.Name, 40)
		if name == "" {
			name = code
		}
		out = append(out, radioCountry{Code: code, Name: name, Count: n})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > 120 {
		out = out[:120]
	}
	return out, nil
}

// radioDirCountryCodeValid accepts two capital letters.
func radioDirCountryCodeValid(cc string) bool {
	return len(cc) == 2 && cc[0] >= 'A' && cc[0] <= 'Z' && cc[1] >= 'A' && cc[1] <= 'Z'
}

// radioDirHostValid accepts a Radio Browser API server name and nothing else:
// "<label>.api.radio-browser.info".
func radioDirHostValid(host string) bool {
	const suffix = ".api.radio-browser.info"
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	label := strings.TrimSuffix(host, suffix)
	if label == "" || len(label) > 30 || label == "all" {
		return false
	}
	for _, r := range label {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// ── Network ──────────────────────────────────────────────────────────────

var radioDirNet struct {
	once      sync.Once
	client    *http.Client
	mu        sync.Mutex
	preferred string    // the server that answered last
	found     []string  // servers DNS listed
	foundAt   time.Time // when (monotonic)
}

// radioDirHTTPClient talks https to Radio Browser's servers only, with the
// firmware CA fallback a handheld needs (bugrepRootCAs).
func radioDirHTTPClient() *http.Client {
	radioDirNet.once.Do(func() {
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			MaxIdleConns:          2,
			IdleConnTimeout:       30 * time.Second,
		}
		if pool := bugrepRootCAs(); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		radioDirNet.client = &http.Client{Transport: tr, Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 || req.URL.Scheme != "https" || !radioDirHostValid(req.URL.Hostname()) {
					return errors.New("redirect refused")
				}
				return nil
			}}
	})
	return radioDirNet.client
}

// radioDirUserAgent names the app, as the API asks.
func radioDirUserAgent() string {
	return "JukaHub/" + strings.TrimPrefix(versionString(nil), "v") + " (handheld launcher; radio directory)"
}

// radioDirDiscover lists the servers DNS knows: the addresses of
// all.api.radio-browser.info, each looked up back to its name. Kept an hour.
func radioDirDiscover(ctx context.Context) []string {
	radioDirNet.mu.Lock()
	if !radioDirNet.foundAt.IsZero() && time.Since(radioDirNet.foundAt) < time.Hour {
		found := append([]string(nil), radioDirNet.found...)
		radioDirNet.mu.Unlock()
		return found
	}
	radioDirNet.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var found []string
	seen := map[string]bool{}
	addrs, err := net.DefaultResolver.LookupHost(ctx, radioDirAllHosts)
	if err == nil {
		for _, a := range addrs {
			names, err := net.DefaultResolver.LookupAddr(ctx, a)
			if err != nil {
				continue
			}
			for _, n := range names {
				n = strings.ToLower(strings.TrimSuffix(n, "."))
				if radioDirHostValid(n) && !seen[n] {
					seen[n] = true
					found = append(found, n)
				}
			}
		}
	}
	rand.Shuffle(len(found), func(i, j int) { found[i], found[j] = found[j], found[i] })
	radioDirNet.mu.Lock()
	radioDirNet.found, radioDirNet.foundAt = found, time.Now()
	radioDirNet.mu.Unlock()
	return found
}

// radioDirGet fetches one API path ("stations/topvote", "countries", …) with
// its query from the first server that answers, and remembers that server.
func radioDirGet(ctx context.Context, apiPath string, query url.Values) ([]byte, error) {
	radioDirNet.mu.Lock()
	preferred := radioDirNet.preferred
	radioDirNet.mu.Unlock()
	tried := map[string]bool{}
	var lastErr error
	try := func(host string) ([]byte, bool) {
		if host == "" || tried[host] || !radioDirHostValid(host) || ctx.Err() != nil {
			return nil, false
		}
		tried[host] = true
		body, err := radioDirGetFrom(ctx, host, apiPath, query)
		if err != nil {
			lastErr = err
			return nil, false
		}
		radioDirNet.mu.Lock()
		radioDirNet.preferred = host
		radioDirNet.mu.Unlock()
		return body, true
	}
	for _, h := range []string{preferred, radioDirFirstHost} {
		if body, ok := try(h); ok {
			return body, nil
		}
	}
	for _, h := range append(radioDirDiscover(ctx), radioDirBuiltinHosts...) {
		if body, ok := try(h); ok {
			return body, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no Radio Browser server answered")
	}
	return nil, lastErr
}

// radioDirGetFrom is one request to one server.
func radioDirGetFrom(ctx context.Context, host, apiPath string, query url.Values) ([]byte, error) {
	u := url.URL{Scheme: "https", Host: host, Path: "/json/" + apiPath, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", radioDirUserAgent())
	req.Header.Set("Accept", "application/json")
	resp, err := radioDirHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", host, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, radioDirFetchMax+1))
	if err != nil {
		return nil, err
	}
	if len(body) > radioDirFetchMax {
		return nil, errors.New("the answer is too large")
	}
	return body, nil
}

// radioDirStationQuery is the common part of every station request.
func radioDirStationQuery() url.Values {
	q := url.Values{}
	q.Set("hidebroken", "true")
	q.Set("limit", fmt.Sprint(radioDirLimit))
	return q
}

// radioDirFetchList fetches one list by its key: "top", "cc:DE", "tag:jazz"
// or "q:<name>".
func radioDirFetchList(ctx context.Context, key string) ([]radioStation, error) {
	q := radioDirStationQuery()
	apiPath := ""
	switch {
	case key == "top":
		apiPath = "stations/topvote"
	case strings.HasPrefix(key, "cc:"):
		cc := strings.TrimPrefix(key, "cc:")
		if !radioDirCountryCodeValid(cc) {
			return nil, errors.New("not a country code")
		}
		apiPath = "stations/bycountrycodeexact/" + cc
		q.Set("order", "clickcount")
		q.Set("reverse", "true")
	case strings.HasPrefix(key, "tag:"):
		// The tag goes in the query, not the path: url.URL.String escapes
		// Path itself, so a path-escaped "hip hop" was sent as "hip%2520hop".
		apiPath = "stations/search"
		q.Set("tag", strings.TrimPrefix(key, "tag:"))
		q.Set("order", "clickcount")
		q.Set("reverse", "true")
	case strings.HasPrefix(key, "q:"):
		apiPath = "stations/search"
		q.Set("name", strings.TrimPrefix(key, "q:"))
		q.Set("order", "clickcount")
		q.Set("reverse", "true")
	default:
		return nil, errors.New("unknown list")
	}
	body, err := radioDirGet(ctx, apiPath, q)
	if err != nil {
		return nil, err
	}
	return radioDirParseStations(body)
}

// radioDirFetchCountries reads the country list.
func radioDirFetchCountries(ctx context.Context) ([]radioCountry, error) {
	q := url.Values{}
	q.Set("order", "stationcount")
	q.Set("reverse", "true")
	q.Set("hidebroken", "true")
	body, err := radioDirGet(ctx, "countries", q)
	if err != nil {
		return nil, err
	}
	return radioDirParseCountries(body)
}

// ── Offline copy ─────────────────────────────────────────────────────────

type radioDirCacheList struct {
	Seq      uint64         `json:"seq"`
	Stations []radioStation `json:"stations"`
}

type radioDirCacheData struct {
	Seq       uint64                       `json:"seq"`
	Lists     map[string]radioDirCacheList `json:"lists"`
	Countries []radioCountry               `json:"countries,omitempty"`
}

var radioDirCache struct {
	mu     sync.Mutex
	loaded bool
	data   radioDirCacheData
}

// radioDirCachePath is overridden by tests.
var radioDirCachePath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, radioDirCacheFile)
}

func radioDirCacheLoadLocked() {
	if radioDirCache.loaded {
		return
	}
	radioDirCache.loaded = true
	var d radioDirCacheData
	data, err := os.ReadFile(radioDirCachePath())
	if err == nil && len(data) <= 8*radioDirFetchMax {
		if json.Unmarshal(data, &d) != nil {
			d = radioDirCacheData{}
		}
	}
	if d.Lists == nil {
		d.Lists = map[string]radioDirCacheList{}
	}
	// The file is ours, but it is still read back as outside data.
	for k, l := range d.Lists {
		clean := make([]radioStation, 0, len(l.Stations))
		for _, s := range l.Stations {
			if s.URL = radioDirStreamURL(s.URL); s.URL == "" {
				continue
			}
			s.Name = radioDirOneLine(s.Name, 80)
			s.Tags = radioDirOneLine(s.Tags, 60)
			s.Codec = radioDirOneLine(s.Codec, 12)
			if !radioDirCountryCodeValid(s.Country) {
				s.Country = ""
			}
			clean = append(clean, s)
			if len(clean) >= radioDirLimit {
				break
			}
		}
		l.Stations = clean
		d.Lists[k] = l
	}
	radioDirCache.data = d
}

// radioDirCached is a list as last fetched, nil when none is kept.
func radioDirCached(key string) []radioStation {
	radioDirCache.mu.Lock()
	defer radioDirCache.mu.Unlock()
	radioDirCacheLoadLocked()
	if l, ok := radioDirCache.data.Lists[key]; ok {
		return append([]radioStation(nil), l.Stations...)
	}
	return nil
}

// radioDirCachedCountries is the country list as last fetched.
func radioDirCachedCountries() []radioCountry {
	radioDirCache.mu.Lock()
	defer radioDirCache.mu.Unlock()
	radioDirCacheLoadLocked()
	return append([]radioCountry(nil), radioDirCache.data.Countries...)
}

// radioDirCacheStore keeps a fetched list (and, with key "", the countries)
// and writes the file; the oldest lists go past radioDirCacheLists.
func radioDirCacheStore(key string, stations []radioStation, countries []radioCountry) {
	// The write lock first: the snapshots reach the file in the order they
	// were taken (two fetches can end together).
	radioDirCacheWriteMu.Lock()
	defer radioDirCacheWriteMu.Unlock()
	radioDirCache.mu.Lock()
	radioDirCacheLoadLocked()
	d := &radioDirCache.data
	if key != "" {
		d.Seq++
		d.Lists[key] = radioDirCacheList{Seq: d.Seq, Stations: stations}
		for len(d.Lists) > radioDirCacheLists {
			oldest, oldestSeq := "", ^uint64(0)
			for k, l := range d.Lists {
				if l.Seq < oldestSeq {
					oldest, oldestSeq = k, l.Seq
				}
			}
			delete(d.Lists, oldest)
		}
	}
	if countries != nil {
		d.Countries = countries
	}
	data, err := json.Marshal(d)
	radioDirCache.mu.Unlock()
	if err != nil {
		return
	}
	if err := AtomicWrite(radioDirCachePath(), data, 0o644); err != nil {
		logWarn("radiodir", "saving the station lists: %v", err)
	}
}

// radioDirCacheWriteMu keeps two writes of the file in order.
var radioDirCacheWriteMu sync.Mutex

// ── The user's stations ──────────────────────────────────────────────────

// radioDirSavedGroup is the IPStream group a saved station goes into: the
// panel shows it as a tab, and the idle screen's radio counts it as music.
const radioDirSavedGroup = "Radio"

// radioDirSavedSet is the links in the IPStream library.
func radioDirSavedSet() map[string]bool {
	set := map[string]bool{}
	for _, s := range ipStreamList() {
		set[strings.TrimSpace(s.URL)] = true
	}
	return set
}

// radioDirToggleSaved adds a station to the user's stations, or takes it out
// when it is there. It reports whether the station is saved now, and the
// error of a save that failed.
func radioDirToggleSaved(s radioStation) (bool, error) {
	if radioDirStreamURL(s.URL) == "" {
		return false, errors.New("this station has no playable link")
	}
	if radioDirSavedSet()[s.URL] {
		if !removeIPStreamByURL(s.URL) {
			return true, errors.New("could not remove the station")
		}
		return false, saveIPStreams()
	}
	if !addIPStream(radioDirOneLine(s.Name, 80), s.URL, radioDirSavedGroup) {
		return false, errors.New("could not add the station")
	}
	return true, saveIPStreams()
}

// radioDirSavedStations is the user's stations for the Radio shelf: the
// IPStream library's music and radio channels (saverRadioMusical), the
// directory's own group first.
func radioDirSavedStations() []IPStream {
	var mine, other []IPStream
	for _, s := range ipStreamList() {
		if !isHTTPMediaURL(s.URL) {
			continue
		}
		switch {
		case strings.EqualFold(s.Group, radioDirSavedGroup):
			mine = append(mine, s)
		case saverRadioMusical(s):
			other = append(other, s)
		}
	}
	return append(mine, other...)
}

// radioDirPlay plays stations[i] in the music player, with the rest of the
// list after it for Next/Previous; a station that does not answer is skipped
// for the next one (musicDecodeEnded). Main thread.
func radioDirPlay(config *Config, stations []radioStation, i int) bool {
	if i < 0 || i >= len(stations) {
		return false
	}
	// Kid mode streams nothing from the web, as openAudioPlayer refuses
	// (uimode.go; it says why). The Radio shelf is reachable there.
	if !uiModeStreamOK(stations[i].URL) {
		return false
	}
	// A saved .pls/.m3u (the IPStream library keeps them) is not a stream:
	// say so rather than tune in to the list's first station instead.
	if radioDirStreamURL(stations[i].URL) == "" {
		ShowToast("This station's link is a playlist file the player cannot open", ToastKindWarning)
		return false
	}
	if !IsOnline() && !netReconnecting() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
		return false
	}
	tracks := make([]musicTrack, 0, len(stations))
	start := 0
	for j, s := range stations {
		if radioDirStreamURL(s.URL) == "" {
			continue
		}
		if j == i {
			start = len(tracks)
		}
		tracks = append(tracks, musicTrack{Path: s.URL, Name: s.Name})
	}
	if len(tracks) == 0 || !musicPlayPlaylist(tracks, start) {
		return false
	}
	// Stations: Play / Pause is Play / Stop, and the player opens with the pad
	// on it, so A stops the radio (music_transport.go).
	musicMarkLive(tracks)
	openMusicPlayerScene(config)
	musicPlayTrack(start)
	musicCtlFocusPlay()
	return true
}
