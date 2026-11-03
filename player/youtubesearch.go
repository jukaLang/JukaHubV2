package main

// youtubesearch.go — the native YouTube search path.
//
// Every YouTube search used to start the yt-dlp binary: a one-file build that
// unpacks and boots a Python runtime before it sends a single request.
// Measured on a desktop, that startup is 0.7–1.3 s of a 2.3 s search, and a
// handheld CPU pays it again for every page of results. The site's own search
// endpoint answers the same question with one POST and a JSON body this
// process can parse, so the results grid no longer pays for an interpreter.
//
// The native path never guesses. It answers only a command it fully
// understands — a plain ytsearch with the stock flags, over a result window it
// can slice out of one response. Anything else (cookies, an API key, a custom
// command, a pasted link) runs through yt-dlp exactly as before, and a failed
// native attempt is not an error the user sees: it is the reason the fallback
// runs.
//
// Paging is native too. One response describes about twenty entries and
// carries the token that asks for the next twenty, so a window past the first
// response is built by following it (0.9 s measured per extra twenty) instead
// of starting the extractor to page. The fetched window is then kept for a
// couple of minutes per query: the second page of a grid is a slice of work
// already done, which is why next-page costs nothing rather than another
// search.
//
// A download-format cap is the one arguable case, and it is modelled on
// purpose: the cap is appended to every search command when the user picks a
// resolution in Settings, and a flat playlist carries no formats for it to
// choose between, so the list this path returns is the one yt-dlp would. The
// attempt is bounded and self-cancelling too (see nativeSearchAttemptBudget and
// nativeSearchBreaker): a network that cannot reach the endpoint must cost a
// few seconds once, not the whole search budget on every load.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// youtubeSearchEndpoint is the public web client's search API. The key is
	// the one every browser session embeds; it is not a secret, it carries no
	// account, and it grants nothing beyond what the site shows a visitor.
	youtubeSearchEndpoint = "https://www.youtube.com/youtubei/v1/search"
	youtubeWebAPIKey      = "AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8"
	// youtubeWebClientVersion is the web client build the request announces.
	// The endpoint rejects a request that names none; the value only has to be
	// a plausible build string.
	youtubeWebClientVersion = "2.20240101.00.00"

	// youtubeWebUserAgent is the browser every web request is dressed as: a
	// current mobile Chrome, because this is what the app runs next to on a
	// handheld and because a years-old desktop string is itself a signal that
	// the request did not come from a browser.
	youtubeWebUserAgent = "Mozilla/5.0 (Linux; Android 16; Pixel 9) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/154.0.0.0 Mobile Safari/537.36"

	// youtubeWebClientHints must agree with that user agent: a request that
	// claims Chrome 154 but reports an old hint set, or no hints at all, is the
	// odd one out. Client hints are how current Chrome describes itself.
	youtubeWebClientHints = `"Chromium";v="154", "Google Chrome";v="154", "Not)A;Brand";v="99"`

	// nativeSearchWindowLimit is the deepest result window this path will
	// build. One response describes about twenty entries, so anything past that
	// is fetched by following the response's continuation token — measured at
	// 0.9 s per extra twenty, against 2–3 s for starting the extractor to page.
	// The cap keeps one search's memory and request count bounded: twelve pages
	// of the five-result grid.
	nativeSearchWindowLimit = 60

	// nativeSearchMaxContinuations bounds how many extra requests one window
	// may cost, so a window cannot spend an unbounded number of round trips.
	nativeSearchMaxContinuations = 3

	// nativeSearchSessionLifetime is how long a fetched window serves later
	// pages of the same query. Paging a 20-entry grid used to re-run the whole
	// search HTTP request per page; a fresh window answers from memory, so the
	// second page costs nothing at all. Search rankings do not move inside a
	// couple of minutes, and the cache below still refreshes hourly.
	nativeSearchSessionLifetime = 2 * time.Minute

	// nativeSearchTimeout bounds the POST. A stalled endpoint must not hold the
	// grid, and it must not cost the user the fallback's startup on top of its
	// own wait.
	nativeSearchTimeout = 12 * time.Second

	// nativeSearchMaxBody caps how much of the response is buffered. The
	// endpoint sends about 700 KB; a hand-built or broken body must not become
	// a multi-megabyte allocation on a 1 GB device.
	nativeSearchMaxBody = 6 << 20
)

// nativeSearchURL is the endpoint this file POSTs to. It is a variable so the
// window logic can be exercised against a local server instead of the network.
var nativeSearchURL = youtubeSearchEndpoint

// nativeSearchSpec matches the yt-dlp search pseudo-URL a command carries.
var nativeSearchSpec = regexp.MustCompile(`^ytsearch(\d+):(.*)$`)

// cacheMediaSearchResults stores one page of results under its command's key,
// so a repeat visit — or the yt-dlp run after a failed native attempt — costs
// neither the network nor the extractor's startup.
func cacheMediaSearchResults(cacheKey string, videos []VideoInfo) {
	if cacheKey == "" || len(videos) == 0 {
		return
	}
	data, err := json.Marshal(videos)
	if err != nil {
		return
	}
	if db, cacheErr := openSharedCache("jukahub.cache"); cacheErr == nil {
		cacheSet(db, cacheKey, data, time.Hour)
	}
}

// nativeSearchAllowedArgs are the yt-dlp flags this path models, each mapped to
// whether it takes a value. A flag that changes what is fetched or how it is
// filtered is absent on purpose: such a command must run through yt-dlp, where
// its behaviour is exactly what the user configured.
var nativeSearchAllowedArgs = map[string]bool{
	"--flat-playlist":        false,
	"--dump-single-json":     false,
	"--skip-download":        false,
	"--quiet":                false,
	"--no-warnings":          false,
	"--ignore-errors":        false,
	"--no-playlist":          false,
	"--no-check-certificate": false,
	"--geo-bypass":           false,
	"--default-search":       true,
	"--playlist-start":       true,
	"--playlist-end":         true,
	"--socket-timeout":       true,
	// A playback-resolution cap names a download format, and a flat playlist
	// carries no formats at all, so the cap cannot change the list this path
	// returns. Refusing it sent every search of a user who picked a resolution
	// in Settings through the extractor binary: the cap is appended to every
	// search command, so the fast path was never taken at all.
	"-f":       true,
	"--format": true,
}

// nativeSearchRequest is one search the native path can answer: the query and
// the 1-based window of results the command asked for.
type nativeSearchRequest struct {
	query string
	start int
	end   int
}

// nativeSearchRequestFor reports whether command is a plain yt-dlp search the
// native path reproduces, and what it asks for. A false result is not an error:
// the caller runs the command through yt-dlp.
func nativeSearchRequestFor(command string) (nativeSearchRequest, bool) {
	tokens := splitShellArgs(command)
	if len(tokens) == 0 {
		return nativeSearchRequest{}, false
	}
	if tool := strings.ToLower(strings.TrimSuffix(filepath.Base(tokens[0]), ".exe")); tool != "yt-dlp" {
		return nativeSearchRequest{}, false
	}
	req := nativeSearchRequest{start: 1}
	windowEnded := false
	positional := ""
	flat, singleJSON := false, false
	for i := 1; i < len(tokens); i++ {
		arg := tokens[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if positional != "" {
				// A second positional argument is a link, a playlist or a
				// second query — none of which is a search to reproduce.
				return nativeSearchRequest{}, false
			}
			positional = arg
			continue
		}
		name, value, hasValue := arg, "", false
		if at := strings.IndexByte(arg, '='); at > 0 {
			name, value, hasValue = arg[:at], arg[at+1:], true
		}
		takesValue, allowed := nativeSearchAllowedArgs[name]
		if !allowed {
			return nativeSearchRequest{}, false
		}
		if takesValue && !hasValue {
			if i+1 >= len(tokens) {
				return nativeSearchRequest{}, false
			}
			i++
			value = tokens[i]
		}
		switch name {
		case "--flat-playlist":
			flat = true
		case "--dump-single-json":
			singleJSON = true
		case "--playlist-start":
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 1 {
				return nativeSearchRequest{}, false
			}
			req.start = n
		case "--playlist-end":
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 1 {
				return nativeSearchRequest{}, false
			}
			req.end = n
			windowEnded = true
		}
	}
	// Without these two the command is not asking for a metadata-only search,
	// so its output would not be what this path produces.
	if !flat || !singleJSON {
		return nativeSearchRequest{}, false
	}
	match := nativeSearchSpec.FindStringSubmatch(positional)
	if match == nil {
		return nativeSearchRequest{}, false
	}
	count, err := strconv.Atoi(match[1])
	if err != nil || count < 1 {
		return nativeSearchRequest{}, false
	}
	if !windowEnded {
		// Without a window the command asked for as many results as its
		// ytsearch count names — ten for the Shorts row, for example — so the
		// whole count is the window, not the default page size.
		req.end = count
	}
	if count < req.end {
		return nativeSearchRequest{}, false
	}
	req.query = strings.TrimSpace(match[2])
	if req.query == "" || req.end < req.start || req.end > nativeSearchWindowLimit {
		return nativeSearchRequest{}, false
	}
	return req, true
}

// sliceNativeSearchWindow returns the results the requested page asks for. The
// endpoint answers with more entries than one page, so a page is a slice of the
// same response rather than a second request.
func sliceNativeSearchWindow(videos []VideoInfo, start, end int) []VideoInfo {
	if start < 1 || end < start || len(videos) == 0 {
		return nil
	}
	if start > len(videos) {
		return nil
	}
	if end > len(videos) {
		end = len(videos)
	}
	return videos[start-1 : end]
}

// nativeSearchHTTPClient is kept separate from the thumbnail client: this one
// waits for a JSON body rather than an image, and its timeout is the search's
// own budget rather than the thumbnail preload's.
var nativeSearchHTTPClient = &http.Client{Timeout: nativeSearchTimeout}

const (
	// nativeSearchAttemptBudget is the one attempt's own deadline. The search's
	// context is much larger, and that difference is the bug this fixes: a
	// network that cannot complete the endpoint's TLS handshake used to burn
	// its whole search budget before the yt-dlp fallback was even started, so
	// the fallback cost the attempt on top of its own run. A healthy answer is
	// well inside one second.
	nativeSearchAttemptBudget = 5 * time.Second
	// nativeSearchFailureLimit and nativeSearchCooldown stop paying that
	// attempt on every load when the endpoint is unreachable at all: after two
	// failures in a row the native path stands down for the cooldown, and the
	// first success brings it straight back.
	nativeSearchFailureLimit = 2
	nativeSearchCooldown     = 10 * time.Minute
)

// errNativeSearchExhausted marks a continuation that had nothing left to add.
// It is the end of the list, not a failure of the endpoint.
var errNativeSearchExhausted = errors.New("no more results")

// errNativeSearchCoolingDown marks a skipped attempt. It is not shown to the
// user and not logged as a failure of the endpoint, because nothing was tried.
var errNativeSearchCoolingDown = errors.New("native search cooling down")

// nativeSearchBreaker remembers how the native endpoint has been behaving.
type nativeSearchBreaker struct {
	mu       sync.Mutex
	failures int
	until    time.Time
	now      func() time.Time
}

var nativeSearchGuard = nativeSearchBreaker{now: time.Now}

func (b *nativeSearchBreaker) clock() time.Time {
	if b.now == nil {
		return time.Now()
	}
	return b.now()
}

// coolingDown reports whether the native path is standing down right now.
func (b *nativeSearchBreaker) coolingDown() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.until.IsZero() {
		return false
	}
	return b.clock().Before(b.until)
}

// note records one attempt's outcome. A success clears the streak.
func (b *nativeSearchBreaker) note(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		b.failures = 0
		b.until = time.Time{}
		return
	}
	b.failures++
	if b.failures >= nativeSearchFailureLimit {
		b.until = b.clock().Add(nativeSearchCooldown)
	}
}

// nativeSearchSkipped reports whether an attempt was skipped by the cooldown
// rather than failing against the endpoint.
func nativeSearchSkipped(err error) bool { return errors.Is(err, errNativeSearchCoolingDown) }

// fetchNativeSearchAttempt returns the results the requested window asks for,
// under the attempt budget and the cooldown. A window that needs a continuation
// costs two requests, so the budget is enforced per request (see postNativeSearch)
// and this cap only bounds the whole attempt.
func fetchNativeSearchAttempt(ctx context.Context, req nativeSearchRequest) ([]VideoInfo, error) {
	if nativeSearchGuard.coolingDown() {
		return nil, errNativeSearchCoolingDown
	}
	attemptCtx, cancel := context.WithTimeout(ctx, nativeSearchTimeout)
	defer cancel()
	window, err := nativeSearchWindowFor(attemptCtx, req.query, req.end)
	// A search the user cancelled says nothing about the endpoint.
	if ctx.Err() == nil {
		nativeSearchGuard.note(err)
	}
	if err != nil {
		return nil, err
	}
	return sliceNativeSearchWindow(window, req.start, req.end), nil
}

// nativeSearchPage is one response's worth of results: the entries it
// described and the token that asks for the next twenty.
type nativeSearchPage struct {
	videos       []VideoInfo
	continuation string
}

// nativeSearchWindows remembers one query's results for a short while, so the
// pages of a grid are slices of one fetch rather than a new search each.
type nativeSearchWindowCache struct {
	mu    sync.Mutex
	query string
	page  nativeSearchPage
	at    time.Time
	now   func() time.Time
}

var nativeSearchWindows nativeSearchWindowCache

func (c *nativeSearchWindowCache) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// get returns the window for query when it is still fresh.
func (c *nativeSearchWindowCache) get(query string, now time.Time) (nativeSearchPage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.query != query || c.page.videos == nil {
		return nativeSearchPage{}, false
	}
	if now.Sub(c.at) > nativeSearchSessionLifetime {
		return nativeSearchPage{}, false
	}
	return c.page, true
}

func (c *nativeSearchWindowCache) put(query string, page nativeSearchPage, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.query = query
	c.page = page
	c.at = now
}

// nativeSearchWindowFor returns the first limit entries the endpoint has for
// query, in the order the site ranked them, following the continuation token
// when the window reaches past the twenty one response describes. A fresh
// window from a previous page of the same query answers without a request.
func nativeSearchWindowFor(ctx context.Context, query string, limit int) ([]VideoInfo, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("empty query")
	}
	if limit <= 0 || limit > nativeSearchWindowLimit {
		limit = nativeSearchWindowLimit
	}
	cache := &nativeSearchWindows
	now := cache.clock()

	page, fresh := cache.get(query, now)
	if fresh {
		if len(page.videos) >= limit || page.continuation == "" {
			return page.videos, nil
		}
		// The cached slice is shared with any other reader of this window, so
		// the continuation below appends into a copy rather than past the end
		// of an array someone else is reading.
		page.videos = append([]VideoInfo(nil), page.videos...)
	} else {
		page = nativeSearchPage{}
	}
	if len(page.videos) == 0 {
		fetched, err := postNativeSearch(ctx, nativeSearchRequestBody(query), false)
		if err != nil {
			return nil, err
		}
		page = fetched
	}
	rounds := 0
	for len(page.videos) < limit && page.continuation != "" && rounds < nativeSearchMaxContinuations {
		rounds++
		next, err := postNativeSearch(ctx, nativeSearchContinuationBody(page.continuation), true)
		if errors.Is(err, errNativeSearchExhausted) {
			// Remember the end, so the next page of this query costs nothing.
			page.continuation = ""
			break
		}
		if err != nil {
			// The twenty already parsed are real results: a failed
			// continuation must not throw them away.
			if len(page.videos) > 0 {
				break
			}
			return nil, err
		}
		if len(next.videos) == 0 {
			// A continuation that describes nothing has exhausted the list.
			page.continuation = ""
			break
		}
		page.videos = append(page.videos, next.videos...)
		page.continuation = next.continuation
	}
	cache.put(query, page, now)
	if len(page.videos) == 0 {
		return nil, errors.New("no video entries in the response")
	}
	return page.videos, nil
}

// postNativeSearch performs one request against the search endpoint and parses
// its body. Each request carries its own deadline: a window that needs a
// continuation must not inherit a budget the first request already spent, and a
// network that cannot reach the endpoint must still cost seconds, not minutes.
func postNativeSearch(ctx context.Context, body map[string]interface{}, continuation bool) (nativeSearchPage, error) {
	reqCtx, cancel := context.WithTimeout(ctx, nativeSearchAttemptBudget)
	defer cancel()
	raw, err := json.Marshal(body)
	if err != nil {
		return nativeSearchPage{}, err
	}
	endpoint := fmt.Sprintf("%s?key=%s&prettyPrint=false", nativeSearchURL, youtubeWebAPIKey)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nativeSearchPage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The body names the web client: the mobile and TV clients answer with a
	// different shape, and the embedded clients carry account context this
	// reader does not have. The headers are the mobile browser's, which is what
	// a page running on this device would send.
	setWebBrowserHeaders(req, "same-origin", "cors", "empty")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://www.youtube.com")
	req.Header.Set("X-Youtube-Client-Name", "1")
	req.Header.Set("X-Youtube-Client-Version", youtubeWebClientVersion)

	resp, err := nativeSearchHTTPClient.Do(req)
	if err != nil {
		return nativeSearchPage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nativeSearchPage{}, fmt.Errorf("search endpoint answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, nativeSearchMaxBody))
	if err != nil {
		return nativeSearchPage{}, err
	}
	page := parseNativeSearchPage(data)
	if continuation {
		page = parseNativeContinuationPage(data)
	}
	if len(page.videos) == 0 && page.continuation == "" {
		// A search with no entries is a failure worth falling back from; a
		// continuation with none after results have already arrived is simply
		// the end of the list.
		if continuation {
			return page, errNativeSearchExhausted
		}
		return nativeSearchPage{}, errors.New("no video entries in the response")
	}
	return page, nil
}

// nativeSearchContinuationBody asks for the next page of the same search.
func nativeSearchContinuationBody(token string) map[string]interface{} {
	return map[string]interface{}{
		"context":      nativeSearchClientContext(),
		"continuation": token,
	}
}

// nativeSearchRequestBody builds the POST body: the query plus the client
// context the endpoint requires.
func nativeSearchRequestBody(query string) map[string]interface{} {
	return map[string]interface{}{
		"context": nativeSearchClientContext(),
		"query":   query,
		"params":  nil,
	}
}

// setWebBrowserHeaders dresses a request as the mobile browser the web client
// names: the client hints, fetch metadata and language a real Chrome sends. A
// request that carries a user agent but none of them is the odd one out, which
// is exactly the fingerprint a bot check looks for.
//
// site, mode and dest are the call's own values: "none", "navigate", "document"
// for a page load; "same-origin", "cors", "empty" for the API call.
func setWebBrowserHeaders(req *http.Request, site, mode, dest string) {
	req.Header.Set("User-Agent", youtubeWebUserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Ch-Ua", youtubeWebClientHints)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?1")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Android"`)
	req.Header.Set("Sec-Fetch-Site", site)
	req.Header.Set("Sec-Fetch-Mode", mode)
	req.Header.Set("Sec-Fetch-Dest", dest)
}

// nativeSearchClientContext is the client block both bodies carry. The endpoint
// rejects a request that names no client, and a continuation must announce the
// same one that produced its token.
func nativeSearchClientContext() map[string]interface{} {
	return map[string]interface{}{
		"client": map[string]interface{}{
			"clientName":    "WEB",
			"clientVersion": youtubeWebClientVersion,
			"hl":            "en",
			"gl":            "US",
		},
	}
}

// nativeSearchResponse is the part of the endpoint's body the reader needs.
// Two shapes are accepted because the endpoint picks one by client: the
// two-column page every desktop browser gets, and the flat section list the
// same data comes back as when the page has no sidebar.
type nativeSearchResponse struct {
	Contents struct {
		TwoColumnSearchResultsRenderer struct {
			PrimaryContents struct {
				SectionListRenderer struct {
					Contents []nativeSearchSection `json:"contents"`
				} `json:"sectionListRenderer"`
			} `json:"primaryContents"`
		} `json:"twoColumnSearchResultsRenderer"`
		SectionListRenderer struct {
			Contents []nativeSearchSection `json:"contents"`
		} `json:"sectionListRenderer"`
	} `json:"contents"`
}

type nativeSearchSection struct {
	ItemSectionRenderer struct {
		Contents []nativeSearchItem `json:"contents"`
	} `json:"itemSectionRenderer"`
	// The section list mixes result sections with the continuation item, which
	// sits beside them rather than inside one: this is where the token that
	// asks for the next twenty results is actually found.
	ContinuationItemRenderer *struct {
		ContinuationEndpoint struct {
			ContinuationCommand struct {
				Token string `json:"token"`
			} `json:"continuationCommand"`
		} `json:"continuationEndpoint"`
	} `json:"continuationItemRenderer"`
}

type nativeSearchItem struct {
	VideoRenderer *nativeVideoRenderer `json:"videoRenderer"`
	// A continuation page wraps its results in an item section rather than
	// listing the video renderers directly, so an item may be a section.
	ItemSectionRenderer *struct {
		Contents []nativeSearchItem `json:"contents"`
	} `json:"itemSectionRenderer"`
	// The last item of a section (and of a continuation) is the token that
	// asks for the next twenty results.
	ContinuationItemRenderer *struct {
		ContinuationEndpoint struct {
			ContinuationCommand struct {
				Token string `json:"token"`
			} `json:"continuationCommand"`
		} `json:"continuationEndpoint"`
	} `json:"continuationItemRenderer"`
}

// nativeContinuationResponse is the body a continuation POST answers with: the
// same client appends the next items as a command, other shapes carry the list
// under continuationContents.
type nativeContinuationResponse struct {
	OnResponseReceivedCommands []struct {
		AppendContinuationItemsAction struct {
			ContinuationItems []nativeSearchItem `json:"continuationItems"`
		} `json:"appendContinuationItemsAction"`
	} `json:"onResponseReceivedCommands"`
	ContinuationContents struct {
		SectionListContinuationRenderer struct {
			Contents []nativeSearchItem `json:"contents"`
		} `json:"sectionListContinuationRenderer"`
	} `json:"continuationContents"`
}

// nativeText is a string the endpoint may send either as simpleText or as a
// list of runs (the parts it colours differently, such as a search term).
type nativeText struct {
	SimpleText string `json:"simpleText"`
	Runs       []struct {
		Text string `json:"text"`
	} `json:"runs"`
}

func (t nativeText) String() string {
	if len(t.Runs) == 0 {
		return strings.TrimSpace(t.SimpleText)
	}
	var b strings.Builder
	for _, run := range t.Runs {
		b.WriteString(run.Text)
	}
	return strings.TrimSpace(b.String())
}

type nativeVideoRenderer struct {
	VideoID           string     `json:"videoId"`
	Title             nativeText `json:"title"`
	LengthText        nativeText `json:"lengthText"`
	ViewCountText     nativeText `json:"viewCountText"`
	ShortViewCount    nativeText `json:"shortViewCountText"`
	OwnerText         nativeText `json:"ownerText"`
	PublishedTimeText nativeText `json:"publishedTimeText"`
	IsLiveNow         bool       `json:"isLiveNow"`
	Thumbnail         struct {
		Thumbnails []struct {
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"thumbnails"`
	} `json:"thumbnail"`
}

// parseNativeYouTubeSearch turns the endpoint's body into the entries the grid
// draws. Entries that are not videos (mixes, shelves, channels) are skipped.
func parseNativeYouTubeSearch(data []byte) []VideoInfo {
	return parseNativeSearchPage(data).videos
}

// parseNativeSearchPage is parseNativeYouTubeSearch plus the continuation token
// that asks the same search for the twenty results after these.
func parseNativeSearchPage(data []byte) nativeSearchPage {
	var resp nativeSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nativeSearchPage{}
	}
	sections := resp.Contents.TwoColumnSearchResultsRenderer.PrimaryContents.SectionListRenderer.Contents
	if len(sections) == 0 {
		sections = resp.Contents.SectionListRenderer.Contents
	}
	var videos []VideoInfo
	continuation := ""
	for _, section := range sections {
		sectionVideos, token := collectNativeSearchItems(section.ItemSectionRenderer.Contents)
		videos = append(videos, sectionVideos...)
		if token != "" {
			continuation = token
		}
		if section.ContinuationItemRenderer != nil {
			if token := strings.TrimSpace(section.ContinuationItemRenderer.ContinuationEndpoint.ContinuationCommand.Token); token != "" {
				continuation = token
			}
		}
	}
	return nativeSearchPage{videos: videos, continuation: continuation}
}

// parseNativeContinuationPage turns a continuation body into the next twenty
// entries and the token after them.
func parseNativeContinuationPage(data []byte) nativeSearchPage {
	var resp nativeContinuationResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nativeSearchPage{}
	}
	var videos []VideoInfo
	continuation := ""
	for _, command := range resp.OnResponseReceivedCommands {
		commandVideos, token := collectNativeSearchItems(command.AppendContinuationItemsAction.ContinuationItems)
		videos = append(videos, commandVideos...)
		if token != "" {
			continuation = token
		}
	}
	if len(videos) == 0 {
		flatVideos, token := collectNativeSearchItems(resp.ContinuationContents.SectionListContinuationRenderer.Contents)
		videos, continuation = flatVideos, token
	}
	return nativeSearchPage{videos: videos, continuation: continuation}
}

// collectNativeSearchItems reads the video entries and the continuation token
// out of one list of rendered items.
func collectNativeSearchItems(items []nativeSearchItem) ([]VideoInfo, string) {
	var videos []VideoInfo
	continuation := ""
	for _, item := range items {
		if item.VideoRenderer != nil {
			if video, ok := item.VideoRenderer.videoInfo(); ok {
				videos = append(videos, video)
			}
			continue
		}
		if item.ItemSectionRenderer != nil {
			nestedVideos, nestedToken := collectNativeSearchItems(item.ItemSectionRenderer.Contents)
			videos = append(videos, nestedVideos...)
			if nestedToken != "" {
				continuation = nestedToken
			}
			continue
		}
		if item.ContinuationItemRenderer != nil {
			if token := strings.TrimSpace(item.ContinuationItemRenderer.ContinuationEndpoint.ContinuationCommand.Token); token != "" {
				continuation = token
			}
		}
	}
	return videos, continuation
}

// videoInfo maps one rendered entry onto the shape the grid and the player
// already use, so nothing downstream knows which source produced it.
func (r *nativeVideoRenderer) videoInfo() (VideoInfo, bool) {
	id := strings.TrimSpace(r.VideoID)
	title := r.Title.String()
	if id == "" || title == "" {
		return VideoInfo{}, false
	}
	address := "https://www.youtube.com/watch?v=" + id
	return VideoInfo{
		ID:         id,
		Title:      title,
		Uploader:   r.OwnerText.String(),
		Channel:    r.OwnerText.String(),
		Duration:   nativeDurationSeconds(r.LengthText.String()),
		Thumbnail:  r.bestThumbnail(),
		ViewCount:  nativeViewCount(r.ViewCountText.String(), r.ShortViewCount.String()),
		WebpageURL: address,
		URL:        address,
	}, true
}

// bestThumbnail picks the widest image the entry offered, which is the one a
// full card wants; the grid scales it down itself.
func (r *nativeVideoRenderer) bestThumbnail() string {
	best, bestWidth := "", -1
	for _, thumb := range r.Thumbnail.Thumbnails {
		url := strings.TrimSpace(thumb.URL)
		if url == "" {
			continue
		}
		if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
			url = "https:" + strings.TrimPrefix(url, ":")
		}
		width := thumb.Width
		if width == 0 {
			width = thumb.Height
		}
		if width >= bestWidth {
			best, bestWidth = url, width
		}
	}
	return best
}

// nativeDurationSeconds reads a clock the site renders ("12:34", "1:02:03").
// A live entry has no length, which reads as unknown.
func nativeDurationSeconds(text string) float64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	parts := strings.Split(text, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	total := 0.0
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 0 {
			return 0
		}
		total = total*60 + float64(value)
	}
	return total
}

// nativeViewCount reads the count the entry shows. The long form ("1,234,567
// views") is exact, so it wins; the short form ("1.2M views") is a rounded
// fallback for entries that ship only that. Unknown counts stay 0, which the
// grid already draws as nothing.
func nativeViewCount(long, short string) int64 {
	digits := make([]byte, 0, 16)
	for i := 0; i < len(long); i++ {
		if long[i] >= '0' && long[i] <= '9' {
			digits = append(digits, long[i])
		}
	}
	if len(digits) > 0 {
		if value, err := strconv.ParseInt(string(digits), 10, 64); err == nil {
			return value
		}
	}
	text := strings.ToUpper(strings.TrimSpace(short))
	if text == "" {
		return 0
	}
	multiplier := 1.0
	switch {
	case strings.Contains(text, "B"):
		multiplier = 1e9
	case strings.Contains(text, "M"):
		multiplier = 1e6
	case strings.Contains(text, "K"):
		multiplier = 1e3
	}
	number := strings.Builder{}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c >= '0' && c <= '9') || c == '.' {
			number.WriteByte(c)
			continue
		}
		if number.Len() > 0 {
			break
		}
	}
	if number.Len() == 0 {
		return 0
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil {
		return 0
	}
	return int64(value * multiplier)
}
