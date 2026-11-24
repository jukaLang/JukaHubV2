package main

// freefilms.go — free films for Media › Movies & TV: the public-domain
// classics in the Internet Archive's curated film collections.
//
// The Archive's Feature Films, Sci-Fi & Horror, Silent Films and classic
// cartoon collections hold films whose copyright has lapsed or that their
// owners released, kept by a library, and they stream from archive.org with no
// account. A row lists one collection, most watched first (the advanced search
// API); the copy a film plays from is chosen when it plays (the metadata API):
// the Archive's own H.264 MP4 when there is one, at most 720 lines because the
// handheld decodes in software, else another MP4, Ogg video or the original.
//
// Films play through the loopback proxy (musicproxy.go, routes iav and iai):
// the stock TrimUI ffmpeg cannot open https (ffmpegtls.go), an address ending
// in .mp4 streams without asking yt-dlp, and the address is the same from one
// run to the next, so Continue watching, Favorites, Up Next and the queue keep
// working. Pictures go the same way, because the thumbnail cache files every
// picture under an address that ends in .jpg (normalizeThumbnailURL).

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// freeFilmsSearchURL is the Archive's advanced search.
	freeFilmsSearchURL = "https://archive.org/advancedsearch.php"
	// freeFilmsRowSize is how many films a row shows before See all.
	freeFilmsRowSize = 24
	// freeFilmsPageSize is one page of a collection's See all list.
	freeFilmsPageSize = 60
	// freeFilmsListMax is the most films one list holds.
	freeFilmsListMax = 600
	// freeFilmsSearchMax and freeFilmsMetaMax bound the two answers.
	freeFilmsSearchMax = 2 << 20
	freeFilmsMetaMax   = 8 << 20
	// freeFilmsResolveTTL is how long a film's chosen copy is remembered.
	freeFilmsResolveTTL = 6 * time.Hour
	// freeFilmsTimeout bounds a search or a metadata request.
	freeFilmsTimeout = 20 * time.Second
)

// freeFilmShelf is one curated collection and the row it makes.
type freeFilmShelf struct {
	ID    string
	Title string
}

// freeFilmShelves are the rows, in order.
var freeFilmShelves = []freeFilmShelf{
	{ID: "feature_films", Title: "Free classic films"},
	{ID: "SciFi_Horror", Title: "Sci-fi & horror classics"},
	{ID: "silent_films", Title: "Silent films"},
	{ID: "classic_cartoons", Title: "Classic cartoons"},
}

// freeFilm is one title of a collection.
type freeFilm struct {
	ID    string
	Title string
	Year  int
	// Duration is in seconds, 0 when the collection does not say.
	Duration float64
}

// freeFilmsHTTP talks to the Archive only: https, and redirects only to its
// own data servers.
var freeFilmsHTTP = sync.OnceValue(func() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   12 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	if pool := bugrepRootCAs(); pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, CheckRedirect: freeFilmsRedirect}
})

// freeFilmsRedirect follows the Archive's hop from archive.org to the data
// server that holds a file, and nothing else.
func freeFilmsRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if !freeFilmsHostOK(req.URL) {
		return http.ErrUseLastResponse
	}
	return nil
}

// freeFilmsHostOK reports an https address on archive.org. Pure.
func freeFilmsHostOK(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "archive.org" || strings.HasSuffix(h, ".archive.org")
}

// freeFilmsValidID reports an Archive item identifier. Pure.
func freeFilmsValidID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '.' || r == '_' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// freeFilmsRequest is one GET to the Archive, with an optional Range.
func freeFilmsRequest(ctx context.Context, rawURL, rangeHeader string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !freeFilmsHostOK(u) {
		return nil, errors.New("not an Internet Archive address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	req.Header.Set("User-Agent", "JukaHub/"+strings.TrimPrefix(versionString(nil), "v"))
	return freeFilmsHTTP().Do(req)
}

// freeFilmsGet reads one bounded answer.
func freeFilmsGet(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, freeFilmsTimeout)
	defer cancel()
	resp, err := freeFilmsRequest(ctx, rawURL, "")
	if err != nil {
		return nil, errors.New("the Internet Archive did not answer")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the Internet Archive answered %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// ── Collections ─────────────────────────────────────────────────────────────

// freeFilmsSearch lists one page (from 1) of a collection, most watched
// first, with how many films the collection holds.
func freeFilmsSearch(ctx context.Context, collection string, page, rows int) ([]freeFilm, int, error) {
	q := url.Values{}
	q.Set("q", "collection:("+collection+") AND mediatype:(movies)")
	for _, f := range []string{"identifier", "title", "year", "runtime"} {
		q.Add("fl[]", f)
	}
	q.Set("sort[]", "downloads desc")
	q.Set("rows", strconv.Itoa(rows))
	q.Set("page", strconv.Itoa(page))
	q.Set("output", "json")
	data, err := freeFilmsGet(ctx, freeFilmsSearchURL+"?"+q.Encode(), freeFilmsSearchMax)
	if err != nil {
		return nil, 0, err
	}
	return freeFilmsParseSearch(data)
}

// freeFilmsParseSearch reads a search answer. Pure.
func freeFilmsParseSearch(data []byte) ([]freeFilm, int, error) {
	var r struct {
		Response struct {
			NumFound int                      `json:"numFound"`
			Docs     []map[string]interface{} `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, 0, errors.New("the Internet Archive sent something unexpected")
	}
	out := make([]freeFilm, 0, len(r.Response.Docs))
	for _, d := range r.Response.Docs {
		id := freeFilmsText(d["identifier"])
		if !freeFilmsValidID(id) {
			continue
		}
		f := freeFilm{ID: id, Title: dlnaCleanText(freeFilmsText(d["title"]))}
		if f.Title == "" {
			f.Title = id
		}
		f.Year = dlnaYear(freeFilmsText(d["year"]))
		f.Duration = freeFilmsRuntime(freeFilmsText(d["runtime"]))
		out = append(out, f)
	}
	return out, r.Response.NumFound, nil
}

// freeFilmsText is a metadata value as text: the Archive sends strings,
// numbers or lists of them. Pure.
func freeFilmsText(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case []interface{}:
		if len(t) > 0 {
			return freeFilmsText(t[0])
		}
	}
	return ""
}

// freeFilmsClock reads "H:MM:SS" or "MM:SS" into seconds, 0 when it cannot.
// Pure.
func freeFilmsClock(s string) float64 {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0
		}
		total = total*60 + v
	}
	return total
}

// freeFilmsRuntime reads a collection's runtime field — "1:23:45", "83 min",
// "83" (minutes) — into seconds, 0 when it cannot. Pure.
func freeFilmsRuntime(s string) float64 {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	if strings.Contains(s, ":") {
		return freeFilmsClock(s)
	}
	end := 0
	for end < len(s) && (s[end] == '.' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	v, err := strconv.ParseFloat(s[:end], 64)
	if err != nil || v <= 0 {
		return 0
	}
	unit := strings.TrimSpace(s[end:])
	switch {
	case strings.HasPrefix(unit, "h"):
		return v * 3600
	case strings.HasPrefix(unit, "s"):
		return v
	case strings.HasPrefix(unit, "m"), unit == "" && v < 1000:
		return v * 60
	}
	return v
}

// ── The copy a film plays from ──────────────────────────────────────────────

type freeFilmsTarget struct {
	url      string
	duration float64
	at       time.Time
}

var freeFilmsResolved struct {
	mu sync.Mutex
	m  map[string]freeFilmsTarget
}

// freeFilmsResolve is the address of the copy of a film the handheld plays,
// and its length (0 when unknown).
func freeFilmsResolve(ctx context.Context, id string) (string, float64, error) {
	if !freeFilmsValidID(id) {
		return "", 0, errors.New("not an Internet Archive film")
	}
	freeFilmsResolved.mu.Lock()
	t, ok := freeFilmsResolved.m[id]
	freeFilmsResolved.mu.Unlock()
	if ok && time.Since(t.at) < freeFilmsResolveTTL {
		return t.url, t.duration, nil
	}
	data, err := freeFilmsGet(ctx, "https://archive.org/metadata/"+url.PathEscape(id), freeFilmsMetaMax)
	if err != nil {
		return "", 0, err
	}
	name, dur, err := freeFilmsPickFile(data)
	if err != nil {
		return "", 0, err
	}
	target := "https://archive.org/download/" + url.PathEscape(id) + "/" + freeFilmsEscapePath(name)
	freeFilmsResolved.mu.Lock()
	if freeFilmsResolved.m == nil || len(freeFilmsResolved.m) > 500 {
		freeFilmsResolved.m = map[string]freeFilmsTarget{}
	}
	freeFilmsResolved.m[id] = freeFilmsTarget{url: target, duration: dur, at: time.Now()}
	freeFilmsResolved.mu.Unlock()
	return target, dur, nil
}

// freeFilmsEscapePath escapes each folder of a file's name in its item.
func freeFilmsEscapePath(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// freeFilmsPickFile chooses the copy to play from an item's metadata: the
// Archive's H.264 MP4, then another MPEG-4, the 512 kb MPEG-4, Ogg video, and
// last an original in another container — each preferring a picture of at
// most 720 lines, and never a file the Archive keeps private. Pure.
func freeFilmsPickFile(data []byte) (string, float64, error) {
	var m struct {
		Files []map[string]interface{} `json:"files"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", 0, errors.New("the Internet Archive sent something unexpected")
	}
	best, bestScore, bestDur, found := "", 0, 0.0, false
	for _, f := range m.Files {
		name := freeFilmsText(f["name"])
		if name == "" || strings.Contains(name, "..") || strings.HasPrefix(name, "/") || freeFilmsText(f["private"]) == "true" {
			continue
		}
		format := strings.ToLower(freeFilmsText(f["format"]))
		ext := strings.ToLower(path.Ext(name))
		mp4 := ext == ".mp4" || ext == ".m4v"
		score := 0
		switch {
		case format == "h.264" && mp4:
			score = 600
		case format == "mpeg4" && mp4:
			score = 500
		case format == "512kb mpeg4":
			score = 400
		case mp4:
			score = 350
		case format == "ogg video" || ext == ".ogv":
			score = 250
		case ext == ".mkv" || ext == ".webm" || ext == ".avi" || ext == ".mpeg" || ext == ".mpg" || ext == ".mov":
			score = 150
		default:
			continue
		}
		h, _ := strconv.Atoi(freeFilmsText(f["height"]))
		switch {
		case h > 720:
			score -= 120
		case h > 0:
			score += h / 20
		}
		if size, _ := strconv.ParseInt(freeFilmsText(f["size"]), 10, 64); size > 4<<30 {
			score -= 200
		}
		if !found || score > bestScore || (score == bestScore && name < best) {
			best, bestScore, found = name, score, true
			bestDur = freeFilmsLength(freeFilmsText(f["length"]))
		}
	}
	if !found {
		return "", 0, errors.New("this film has no copy the handheld can play")
	}
	return best, bestDur, nil
}

// freeFilmsLength reads a file's length: seconds ("5373.47") or a clock.
// Pure.
func freeFilmsLength(s string) float64 {
	if strings.Contains(s, ":") {
		return freeFilmsClock(s)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// ── Addresses the player and the cards use ──────────────────────────────────

// freeFilmsVideoURL is where a film plays from.
func freeFilmsVideoURL(id string) string {
	return musicProxyBase() + "/iav/ia/" + url.PathEscape(id) + ".mp4"
}

// freeFilmsArtURL is a film's picture.
func freeFilmsArtURL(id string) string {
	return musicProxyBase() + "/iai/ia/" + url.PathEscape(id) + ".jpg"
}

// freeFilmsProxy serves a film (route iav: its chosen copy, with ranges) or
// its picture (route iai) from the Archive.
func freeFilmsProxy(w http.ResponseWriter, r *http.Request, route, rest string) {
	dot := strings.LastIndex(rest, ".")
	if dot <= 0 {
		http.NotFound(w, r)
		return
	}
	id, err := url.PathUnescape(rest[:dot])
	if err != nil || !freeFilmsValidID(id) {
		http.NotFound(w, r)
		return
	}
	target := "https://archive.org/services/img/" + url.PathEscape(id)
	if route == "iav" {
		target, _, err = freeFilmsResolve(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	}
	musicProxyPass(w, r, func(ctx context.Context, rg string) (*http.Response, error) {
		return freeFilmsRequest(ctx, target, rg)
	})
}
