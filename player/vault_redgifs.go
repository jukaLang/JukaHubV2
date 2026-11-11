package main

// vault_redgifs.go — the Vault tab's RedGifs feed.
//
// The tab used to run yt-dlp's RedGifsSearch extractor over a browse URL. That
// extractor drops the tag while it prepares the API query (it sends
// search_text, and the site's search returns mostly unrelated results for it),
// so every query came back as the same unfiltered page — and on the handheld
// the request sometimes answered 400 ("Unable to download JSON metadata").
//
// The site's own JSON API is reachable from the same network without the
// extractor binary, and its tags= parameter is a real filter: tags=asian
// returns only Asian-tagged gifs. So the tab talks to the API directly, keeps
// the tag filter on locally as well, and leaves yt-dlp to the single video it
// already resolves well.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	redGifsAPIBase = "https://api.redgifs.com/v2"
	// redGifsPageSize is one API request. The API caps count at 100; 80 fills
	// several grid rows without a second round trip on handheld Wi-Fi.
	redGifsPageSize = 80
	// redGifsUserAgent identifies the app like a desktop browser: the API
	// refuses unknown agents.
	redGifsUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	// redGifsTokenTTL is how long a temporary API token is reused before it is
	// asked for again. The endpoint needs no account and no key.
	redGifsTokenTTL = 6 * time.Hour
	// mediaHubVaultDefaultTag is what the tab shows before a search: the feed
	// opens on Asian content instead of the site's global front page.
	mediaHubVaultDefaultTag = "asian"
	// vaultPagingPrefix marks the paging command of a Vault list, so the
	// follow-up page goes back to the API instead of being rewritten into a
	// yt-dlp search by buildMediaPageCommand.
	vaultPagingPrefix = "vault:"
)

var redGifsHTTP = &http.Client{Timeout: 20 * time.Second}

// redGifsBlockedWords keeps the feed to what this tab is for. A gif carrying
// any of these tags is dropped before it reaches the grid.
var redGifsBlockedWords = []string{"gay", "trans", "transgender", "shemale", "femboy", "twink", "yaoi"}

// redGifsGif is one entry of the API's search answer, trimmed to the fields a
// card uses.
type redGifsGif struct {
	ID         string   `json:"id"`
	CreateDate int64    `json:"createDate"`
	Tags       []string `json:"tags"`
	UserName   string   `json:"userName"`
	Duration   float64  `json:"duration"`
	Views      int64    `json:"views"`
	Likes      int64    `json:"likes"`
	URLs       struct {
		HD        string `json:"hd"`
		SD        string `json:"sd"`
		Poster    string `json:"poster"`
		Thumbnail string `json:"thumbnail"`
	} `json:"urls"`
}

// redGifsSearchPage is the search envelope: the page's gifs plus how many
// pages the tag has, so the grid knows when the list is exhausted.
type redGifsSearchPage struct {
	Gifs  []redGifsGif `json:"gifs"`
	Pages int          `json:"pages"`
	Total int          `json:"total"`
}

var redGifsAuth = struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}{}

// redGifsToken returns a temporary API token, reusing the cached one until it
// nears expiry. The token is what the site's own web client asks for on load;
// the mutex is held across the request so two parallel loads cannot both start
// a token fetch.
func redGifsToken(ctx context.Context) (string, error) {
	redGifsAuth.mu.Lock()
	defer redGifsAuth.mu.Unlock()
	if redGifsAuth.token != "" && time.Now().Before(redGifsAuth.expires) {
		return redGifsAuth.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, redGifsAPIBase+"/auth/temporary", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("user-agent", redGifsUserAgent)
	req.Header.Set("accept", "application/json")
	req.Header.Set("referer", "https://www.redgifs.com/")
	resp, err := redGifsHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth answered HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Token) == "" {
		return "", fmt.Errorf("auth returned no token")
	}
	redGifsAuth.token = payload.Token
	redGifsAuth.expires = time.Now().Add(redGifsTokenTTL)
	return payload.Token, nil
}

// redGifsFetchTagPage asks the API for one page of a tag, newest first. The
// tags= parameter is the one that actually filters; search_text does not.
func redGifsFetchTagPage(ctx context.Context, tag string, page, count int) (redGifsSearchPage, error) {
	var out redGifsSearchPage
	token, err := redGifsToken(ctx)
	if err != nil {
		return out, err
	}
	if page < 1 {
		page = 1
	}
	if count < 1 {
		count = redGifsPageSize
	}
	q := url.Values{}
	q.Set("tags", tag)
	q.Set("order", "latest")
	q.Set("type", "g")
	q.Set("count", strconv.Itoa(count))
	q.Set("page", strconv.Itoa(page))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, redGifsAPIBase+"/gifs/search?"+q.Encode(), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("user-agent", redGifsUserAgent)
	req.Header.Set("accept", "application/json")
	req.Header.Set("referer", "https://www.redgifs.com/")
	req.Header.Set("origin", "https://www.redgifs.com")
	resp, err := redGifsHTTP.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("search answered HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

// redGifsNormalizeTag lowercases a tag and turns separators into spaces, so
// "Asian-Woman", "asian_woman" and "Asian Woman" compare the same.
func redGifsNormalizeTag(tag string) string {
	t := strings.ToLower(strings.TrimSpace(tag))
	t = strings.NewReplacer("-", " ", "_", " ").Replace(t)
	return strings.Join(strings.Fields(t), " ")
}

// redGifsTagMatches reports whether one of the gif's tags is the query tag or
// a longer tag that starts with it ("asian" matches "Asian Woman").
func redGifsTagMatches(tags []string, query string) bool {
	q := redGifsNormalizeTag(query)
	if q == "" {
		return true
	}
	for _, raw := range tags {
		t := redGifsNormalizeTag(raw)
		if t == q || strings.HasPrefix(t, q+" ") {
			return true
		}
	}
	return false
}

// redGifsTagBlocked reports whether the gif carries a tag the tab excludes.
func redGifsTagBlocked(tags []string) bool {
	for _, raw := range tags {
		for _, word := range strings.Fields(redGifsNormalizeTag(raw)) {
			for _, blocked := range redGifsBlockedWords {
				if word == blocked {
					return true
				}
			}
		}
	}
	return false
}

// redGifsVideoInfos turns the API's gifs into grid cards, keeping only the
// gifs that carry the requested tag and no excluded one. A gif with no direct
// stream is skipped: every card must be playable without an extractor.
func redGifsVideoInfos(gifs []redGifsGif, query string) []VideoInfo {
	out := make([]VideoInfo, 0, len(gifs))
	for _, g := range gifs {
		id := strings.TrimSpace(g.ID)
		stream := strings.TrimSpace(g.URLs.HD)
		if stream == "" {
			stream = strings.TrimSpace(g.URLs.SD)
		}
		if id == "" || stream == "" {
			continue
		}
		if redGifsTagBlocked(g.Tags) || !redGifsTagMatches(g.Tags, query) {
			continue
		}
		thumb := strings.TrimSpace(g.URLs.Thumbnail)
		if thumb == "" {
			thumb = strings.TrimSpace(g.URLs.Poster)
		}
		out = append(out, VideoInfo{
			ID:         id,
			Title:      redGifsTitle(g),
			Uploader:   strings.TrimSpace(g.UserName),
			Channel:    strings.TrimSpace(g.UserName),
			Duration:   g.Duration,
			Thumbnail:  thumb,
			ViewCount:  g.Views,
			UploadDate: redGifsUploadDate(g.CreateDate),
			// The direct stream is the URL to play: yt-dlp's generic
			// extractor downloads a plain media link, and WebpageURL stays
			// empty so nothing has to resolve the watch page first.
			URL: stream,
		})
	}
	return out
}

// redGifsTitle is the card's headline: the gif's leading tags, which say more
// than the poster's name, with the name as the metadata line.
func redGifsTitle(g redGifsGif) string {
	tags := make([]string, 0, 3)
	for _, t := range g.Tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		tags = append(tags, t)
		if len(tags) == 3 {
			break
		}
	}
	if len(tags) > 0 {
		return strings.Join(tags, " · ")
	}
	if user := strings.TrimSpace(g.UserName); user != "" {
		return user
	}
	return "RedGifs " + g.ID
}

// redGifsUploadDate renders the API's creation timestamp in yt-dlp's format.
func redGifsUploadDate(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format("20060102")
}

// vaultFeedQuery is the tag the Vault tab shows: the last search, or the
// default Asian tag when nothing has been searched yet.
func vaultFeedQuery(config *Config) string {
	if config != nil {
		if q, ok := config.Variables.Custom[mediaHubVaultSearchQuery].(string); ok {
			if tag := strings.TrimSpace(q); tag != "" {
				return tag
			}
		}
	}
	return mediaHubVaultDefaultTag
}

// redGifsFirstWord is the single tag a phrase describes: the first word of a
// multi-word query, or the query itself when it is already one word.
func redGifsFirstWord(query string) string {
	fields := strings.Fields(strings.TrimSpace(query))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// vaultPagingCommand is the remembered paging command for a Vault list.
func vaultPagingCommand(tag string) string { return vaultPagingPrefix + tag }

// vaultQueryFromPagingCommand recovers the tag from a remembered Vault list,
// so paging knows the request never belonged to yt-dlp.
func vaultQueryFromPagingCommand(command string) (string, bool) {
	if strings.HasPrefix(command, vaultPagingPrefix) {
		return strings.TrimPrefix(command, vaultPagingPrefix), true
	}
	return "", false
}

// mediaHubLoadVaultFeed starts the first page of a Vault list.
func mediaHubLoadVaultFeed(config *Config, tag string) {
	runSceneTask(func(ctx context.Context) {
		loadVaultPage(ctx, config, tag, 1, false)
	})
}

// mediaHubLoadVaultPage starts a follow-up page (apiPage is 1-based; a first
// page is 1) that appends to the list already on screen.
func mediaHubLoadVaultPage(config *Config, tag string, apiPage int, appendMode bool) {
	runSceneTask(func(ctx context.Context) {
		loadVaultPage(ctx, config, tag, apiPage, appendMode)
	})
}

// loadVaultPage is the blocking body behind the two wrappers. It runs in the
// scene's task scope, so leaving Media cancels the request and publishes
// nothing into the next screen.
func loadVaultPage(parent context.Context, config *Config, tag string, apiPage int, appendMode bool) {
	if parent == nil {
		parent = context.Background()
	}
	if config == nil || parent.Err() != nil {
		return
	}
	gen := beginMediaSearch(config, mediaPHQuery)
	defer finishMediaSearch(config)
	defer clearMediaSearchCancel(mediaPHQuery, gen)
	// stale is true once a newer load owns the Vault list.
	stale := func() bool { return mediaSearchSuperseded(mediaPHQuery, gen) }

	ctx, cancel := context.WithTimeout(parent, mediaSearchTimeout)
	registerMediaSearchCancel(mediaPHQuery, cancel)
	defer cancel()

	if appendMode {
		// The paging state counts pages from zero; the API counts from one.
		markMediaPageRequested(mediaPHQuery, apiPage-1)
	}

	page, err := redGifsFetchTagPage(ctx, tag, apiPage, redGifsPageSize)
	// A typed phrase is not a tag: the site's tags are single words, so a
	// query like "asian girl" matches nothing and falls back to its first
	// word, which is the tag it was describing.
	if err == nil && len(page.Gifs) == 0 && apiPage == 1 {
		if first := redGifsFirstWord(tag); first != tag {
			if alt, altErr := redGifsFetchTagPage(ctx, first, 1, redGifsPageSize); altErr == nil && len(alt.Gifs) > 0 {
				tag, page = first, alt
			}
		}
	}
	if !appendMode {
		// Remembered once the tag is settled, so paging follows the tag that
		// actually answered.
		rememberMediaPaging(mediaPHQuery, vaultPagingCommand(tag))
		markMediaPageRequested(mediaPHQuery, 0)
	}
	if err != nil {
		if ctx.Err() != nil || stale() {
			// A cancelled load still has to release the in-flight flag, or the
			// grid's Next chip would stay on "Loading…" forever.
			markMediaPageSettled(mediaPHQuery, false)
			return
		}
		markMediaPageSettled(mediaPHQuery, false)
		logWarn("vault", "feed page %d for tag %q failed: %v", apiPage, tag, err)
		reason := "The video service did not answer — try again"
		if appendMode {
			// The loaded pages stay readable; only the next page failed.
			ShowToast(reason, ToastKindWarning)
			return
		}
		publishCustom(mediaPHQuery, []VideoInfo{})
		focusedResultIndex = -1
		resetResultActions()
		publishCustom("search_error", reason)
		return
	}

	videos := redGifsVideoInfos(page.Gifs, tag)
	lastPage := page.Pages > 0 && apiPage >= page.Pages

	if appendMode {
		current, _ := config.Variables.Custom[mediaPHQuery].([]VideoInfo)
		merged, added := mergeMediaResults(current, videos)
		markMediaPageSettled(mediaPHQuery, lastPage || added == 0)
		if stale() {
			return
		}
		if added == 0 {
			ShowToast("No more results", ToastKindInfo)
			return
		}
		publishCustom(mediaPHQuery, merged)
		publishCustom("search_error", nil)
		focusedResultIndex = len(current)
		prefetchVideos(config, merged, focusedResultIndex)
		return
	}

	markMediaPageSettled(mediaPHQuery, lastPage)
	if stale() {
		return
	}
	publishCustom(mediaPHQuery, videos)
	publishCustom("search_error", nil)
	focusedResultIndex = -1
	resetResultActions()
	prefetchFirstResult(config, videos)
}
