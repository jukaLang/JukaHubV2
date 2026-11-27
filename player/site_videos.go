package main

// site_videos.go — the screen a video-site tile opens. It is the site's own
// place in Entertainment: videos saved from that domain, favourites, watch
// history and a toolbar for searching or pasting a link. Nothing is hard-coded
// to a particular site: the domain comes from the shortcut the user added.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type siteVideo struct {
	Title   string
	URL     string
	Section string
	Label   string
	Thumb   string // local poster file, "" when the site gave no thumbnail
}

var siteVideoUI struct {
	site    videoSite
	entries []siteVideo
	cursor  int
	loading bool
	err     string
	query   string
	cols    int
	// Hit rects from the last rendered frame: the search bar and each card.
	searchRect sdl.Rect
	cardRects  []sdl.Rect
	cardFirst  int
	lastClick  int
	fetchGen   uint64 // ignores a trending fetch that lands after a search
}

// siteHostMatches reports whether a URL belongs to the site's domain.
func siteHostMatches(raw, host string) bool {
	host = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(host), "*."))
	if host == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	name := strings.ToLower(u.Hostname())
	if name == "" {
		// A saved local file has no host; its Source is checked by callers.
		return false
	}
	return name == host || strings.HasSuffix(name, "."+host)
}

// siteVideosBuild gathers everything the app already knows from that domain.
func siteVideosBuild(site videoSite) []siteVideo {
	var out []siteVideo
	entries, _, _ := dlLibrarySnapshot()
	for _, e := range entries {
		if !e.Video || e.Path == "" || !siteHostMatches(e.Source, site.Host) {
			continue
		}
		title := e.Title
		if title == "" {
			title = videoTitleFromURL(e.Source)
		}
		out = append(out, siteVideo{Title: title, URL: e.Path, Section: "Downloaded", Label: "offline", Thumb: e.ThumbFile})
	}
	snap := favoritesSnapshot()
	for _, f := range snap.Videos {
		v, ok := f.Data.(VideoInfo)
		if !ok || !siteHostMatches(v.GetURL(), site.Host) {
			continue
		}
		out = append(out, siteVideo{Title: videoTitleFromURL(v.GetURL()), URL: v.GetURL(), Section: "Favorites", Label: "favorite"})
	}
	for _, f := range snap.Recent {
		v, ok := f.Data.(VideoInfo)
		if !ok || !siteHostMatches(v.GetURL(), site.Host) {
			continue
		}
		out = append(out, siteVideo{Title: videoTitleFromURL(v.GetURL()), URL: v.GetURL(), Section: "History", Label: "watched"})
	}
	return out
}

// videoTitleFromURL is a readable fallback for a link with no metadata.
func videoTitleFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	name := u.Path
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return u.Hostname()
	}
	return name
}

// siteDisplayName is what the screen calls a site: its name, or its domain.
func siteDisplayName(site videoSite) string {
	if name := strings.TrimSpace(site.Name); name != "" {
		return name
	}
	if host := strings.TrimSpace(site.Host); host != "" {
		return host
	}
	return "this site"
}

// siteTagBrowseURL is the browse?tags= page RedGifs serves and yt-dlp's
// RedGifsSearchIE understands. It is that site's form, not a generic one:
// another host answers it with a 404, so only a site known to use it is sent
// there.
func siteTagBrowseURL(host, query string) string {
	host = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(host), "*."))
	if host == "" || strings.TrimSpace(query) == "" {
		return ""
	}
	if !strings.HasPrefix(host, "www.") {
		host = "www." + host
	}
	return "https://" + host + "/browse?tags=" + url.QueryEscape(query)
}

type siteFormat struct {
	URL string `json:"url"`
}

type siteExtractEntry struct {
	ID         string       `json:"id"`
	Title      string       `json:"title"`
	WebpageURL string       `json:"webpage_url"`
	URL        string       `json:"url"`
	Uploader   string       `json:"uploader"`
	Duration   float64      `json:"duration"`
	Views      int64        `json:"view_count"`
	Formats    []siteFormat `json:"formats"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

// siteSearchPrefix is the search prefix a site's yt-dlp extractor exposes
// ("scsearch20:" asks SoundCloud for its first 20 matches). "" means the
// extractor has no keyword search of its own.
func siteSearchPrefix(host string) string {
	host = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(host), "*."))
	switch {
	case strings.HasSuffix(host, "soundcloud.com"):
		return "scsearch20:"
	case strings.HasSuffix(host, "bilibili.com"):
		return "bilisearch20:"
	case strings.HasSuffix(host, "nicovideo.jp"):
		return "nicosearch20:"
	case host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be":
		return "ytsearch20:"
	}
	return ""
}

// siteSearchPageURL is a site's own results page for a keyword. Only forms
// known to work are listed: a guessed path is a page yt-dlp cannot read, which
// is how a search on a typed domain used to "succeed" with nothing on screen.
// A host that is not here has no in-app search, and the screen says so.
func siteSearchPageURL(host, query string) string {
	host = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(host), "*."))
	query = strings.TrimSpace(query)
	if host == "" || query == "" {
		return ""
	}
	switch {
	case strings.HasSuffix(host, "dailymotion.com"):
		return "https://www.dailymotion.com/search/" + url.PathEscape(query) + "/videos"
	case siteIsRedGifs(host):
		return siteTagBrowseURL(host, query)
	}
	return ""
}

// siteSearchTarget is the yt-dlp operand for a keyword on a site, or "" when
// the site's search form is not known.
func siteSearchTarget(host, query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	if prefix := siteSearchPrefix(host); prefix != "" {
		return prefix + query
	}
	return siteSearchPageURL(host, query)
}

// siteThumbURL derives the poster image from the video URL yt-dlp returned.
// RedGifs-style hosts serve <name>-poster.jpg beside <name>.mp4; a site that
// does not follow that pattern simply has no thumbnail.
func siteThumbURL(formats []siteFormat) string {
	var fallback string
	for _, f := range formats {
		u := strings.TrimSpace(f.URL)
		if !strings.HasPrefix(u, "http") || !strings.HasSuffix(strings.ToLower(u), ".mp4") {
			continue
		}
		base := strings.TrimSuffix(u, u[len(u)-4:])
		// The mobile encode's "-mobile-poster.jpg" usually does not exist;
		// the full-size stream's poster does.
		if !strings.Contains(strings.ToLower(base), "-mobile") {
			return base + "-poster.jpg"
		}
		if fallback == "" {
			fallback = base + "-poster.jpg"
		}
	}
	return fallback
}

// siteThumbsDir is the cache for posters fetched with the results.
func siteThumbsDir() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "site-thumbs")
}

// siteCacheThumb fetches one poster into the cache and returns its local path.
// It runs on the fetch goroutine, never on the render thread. A failed image
// is simply omitted; the card falls back to no art rather than a placeholder.
func siteCacheThumb(raw string) string {
	if raw == "" {
		return ""
	}
	dir := siteThumbsDir()
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	name := hex.EncodeToString(sum[:16]) + ".jpg"
	path := filepath.Join(dir, name)
	if regularFile(path) {
		return path
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", downloadUserAgent)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || len(data) < 128 {
		return ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil || AtomicWrite(path, data, 0o644) != nil {
		return ""
	}
	return path
}

// siteIsRedGifs reports whether a host is RedGifs. Its own API answers a
// keyword search; yt-dlp's RedGifsSearchIE still asks for "search_text", which
// RedGifs now ignores, so a search came back as the trending feed and the grid
// looked like it had not changed at all.
func siteIsRedGifs(host string) bool {
	host = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(host), "*."))
	return host == "redgifs.com" || strings.HasSuffix(host, ".redgifs.com")
}

// redGifsGif is one video in the site's search answer.
type redGifsGif struct {
	ID       string   `json:"id"`
	Tags     []string `json:"tags"`
	UserName string   `json:"userName"`
	Duration float64  `json:"duration"`
	Views    int64    `json:"views"`
	URLs     struct {
		Poster string `json:"poster"`
	} `json:"urls"`
}

// redGifsSearchQuery is the API's query string. A typed word is a tag: "tags"
// is the parameter RedGifs filters on, while "search_text" (what yt-dlp sends)
// is ignored and answers with the trending feed. The screen's own "trending"
// load asks for that feed with no tag.
func redGifsSearchQuery(query string) string {
	values := url.Values{}
	values.Set("order", "trending")
	values.Set("count", "20")
	if q := strings.TrimSpace(query); q != "" && !strings.EqualFold(q, "trending") {
		values.Set("tags", q)
	}
	return values.Encode()
}

// siteRedGifsVideos maps the API's answer to cards. poster turns a picture URL
// into a cached file, so the mapping can be tested without a network.
func siteRedGifsVideos(gifs []redGifsGif, poster func(string) string) []siteVideo {
	out := make([]siteVideo, 0, len(gifs))
	for _, g := range gifs {
		id := strings.TrimSpace(g.ID)
		if id == "" {
			continue
		}
		title := strings.Join(g.Tags, " ")
		if title == "" {
			title = g.UserName
		}
		if title == "" {
			title = id
		}
		thumb := ""
		if poster != nil {
			thumb = poster(g.URLs.Poster)
		}
		out = append(out, siteVideo{
			Title: title, URL: "https://www.redgifs.com/watch/" + id, Section: "Recommended",
			Label: videoSiteLabel(g.Duration, g.Views), Thumb: thumb,
		})
	}
	return out
}

// siteRedGifsSearch asks the site's public API for one page of videos. The
// token is the anonymous one the site hands a first-time visitor.
func siteRedGifsSearch(ctx context.Context, query string) ([]siteVideo, error) {
	client := &http.Client{}
	readJSON := func(link, token string, into interface{}) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", downloadUserAgent)
		req.Header.Set("Referer", "https://www.redgifs.com/")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("redgifs answered %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return err
		}
		return json.Unmarshal(data, into)
	}
	var auth struct {
		Token string `json:"token"`
	}
	if err := readJSON("https://api.redgifs.com/v2/auth/temporary", "", &auth); err != nil {
		return nil, err
	}
	if auth.Token == "" {
		return nil, fmt.Errorf("redgifs gave no token")
	}
	var doc struct {
		GIFs []redGifsGif `json:"gifs"`
	}
	link := "https://api.redgifs.com/v2/gifs/search?" + redGifsSearchQuery(query)
	if err := readJSON(link, auth.Token, &doc); err != nil {
		return nil, err
	}
	return siteRedGifsVideos(doc.GIFs, siteCacheThumb), nil
}

// siteVideosFetch asks the site for a flat list of its videos. It runs off the
// event thread; the caller posts the result back with tbxPost.
func siteVideosFetch(site videoSite, query string) ([]siteVideo, error) {
	if siteIsRedGifs(site.Host) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		videos, err := siteRedGifsSearch(ctx, query)
		cancel()
		if err == nil {
			return videos, nil
		}
		// A keyword search has no honest fallback: yt-dlp would answer with
		// the trending feed. The default screen may still try it.
		if !strings.EqualFold(strings.TrimSpace(query), "trending") {
			return nil, err
		}
	}
	link := siteSearchTarget(site.Host, query)
	if link == "" {
		return nil, fmt.Errorf("no known search for %s — paste a video link instead", siteDisplayName(site))
	}
	tool := getToolPath("yt-dlp", appConfig)
	if tool == "" {
		return nil, fmt.Errorf("yt-dlp is not installed yet")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := hiddenCommandContext(ctx, tool,
		"--flat-playlist", "--dump-single-json", "--no-playlist", "--skip-download",
		"--no-check-certificate", "--playlist-end", "20", link)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var doc struct {
		Entries []siteExtractEntry `json:"entries"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	var videos []siteVideo
	for _, e := range doc.Entries {
		page := e.WebpageURL
		if page == "" {
			page = e.URL
		}
		if page == "" {
			continue
		}
		title := e.Title
		if strings.EqualFold(title, site.Name) || title == "" {
			title = e.Uploader
		}
		if title == "" {
			title = e.ID
		}
		poster := siteThumbURL(e.Formats)
		if poster == "" && len(e.Thumbnails) > 0 {
			poster = e.Thumbnails[len(e.Thumbnails)-1].URL
		}
		videos = append(videos, siteVideo{
			Title: title, URL: page, Section: "Recommended",
			Label: videoSiteLabel(e.Duration, e.Views),
			Thumb: siteCacheThumb(poster),
		})
	}
	return videos, nil
}

func videoSiteLabel(seconds float64, views int64) string {
	parts := make([]string, 0, 2)
	if seconds > 0 {
		parts = append(parts, fmt.Sprintf("%.0fs", seconds))
	}
	if views > 0 {
		parts = append(parts, fmt.Sprintf("%d views", views))
	}
	return strings.Join(parts, " · ")
}

// siteVideosLoad starts a background fetch and keeps the local rows visible
// while it runs. Results replace only the Recommended section.
func siteVideosLoad(query string) {
	site := siteVideoUI.site
	if site.Host == "" {
		return
	}
	if !IsOnline() {
		siteVideoUI.loading, siteVideoUI.err = false, "Offline — saved videos are still listed"
		return
	}
	if siteSearchTarget(site.Host, query) == "" {
		// Nothing is guessed: a site whose search form is unknown keeps its
		// saved rows on screen and says how to look for videos instead.
		siteVideoUI.loading = false
		siteVideoUI.err = "No known search for " + siteDisplayName(site) + " — paste a video link with X"
		return
	}
	siteVideoUI.fetchGen++
	gen := siteVideoUI.fetchGen
	siteVideoUI.loading, siteVideoUI.err, siteVideoUI.query = true, "", query
	if query != "trending" {
		ShowToast("Searching "+site.Host+" for "+query+"…", ToastKindInfo)
	}
	go func() {
		videos, err := siteVideosFetch(site, query)
		tbxPost(func() {
			if siteVideoUI.site.Host != site.Host || siteVideoUI.fetchGen != gen {
				return
			}
			siteVideoUI.loading = false
			if err != nil {
				siteVideoUI.err = firstLine(err.Error())
				ShowToast("Video search failed: "+siteVideoUI.err, ToastKindError)
				return
			}
			if query != "trending" {
				ShowToast(pluralizeCount(len(videos), "video", "videos")+" for "+query, ToastKindSuccess)
			}
			var kept []siteVideo
			for _, v := range siteVideoUI.entries {
				if v.Section != "Recommended" {
					kept = append(kept, v)
				}
			}
			siteVideoUI.entries = append(videos, kept...)
			siteVideoUI.cursor = 0
		})
	}()
}

// siteVideosSearchSubmit fetches keyword results into this screen.
func siteVideosSearchSubmit(raw string) {
	query := strings.TrimSpace(raw)
	if query == "" {
		return
	}
	if siteSearchTarget(siteVideoUI.site.Host, query) == "" {
		ShowToast("No known search for "+siteDisplayName(siteVideoUI.site)+" — paste a link with X", ToastKindInfo)
		return
	}
	siteVideosLoad(query)
}

// siteVideosOpen opens the site's screen from a tile.
func siteVideosOpen(site videoSite) {
	otherVideosSite = site
	siteVideoUI.site, siteVideoUI.entries, siteVideoUI.cursor = site, siteVideosBuild(site), 0
	siteVideoUI.err, siteVideoUI.query = "", "trending"
	siteVideosLoad("trending") // default videos, just like the Media tab
	// A site is a full scene, the way YouTube is: no popup card over Home.
	if appConfig == nil {
		return
	}
	ensureSiteVideosScene(appConfig)
	if idx := sceneIndexExact(appConfig, siteVideosSceneName); idx >= 0 {
		changeSceneTo(appConfig, idx)
	}
	PlayActivateSound()
}

func siteVideosMove(delta int) {
	n := len(siteVideoUI.entries)
	if n == 0 {
		return
	}
	siteVideoUI.cursor = (siteVideoUI.cursor + delta + n) % n
	PlayNavSound()
}

// siteVideosActivate plays the highlighted video, offline file or link.
func siteVideosActivate() {
	if siteVideoUI.cursor < 0 || siteVideoUI.cursor >= len(siteVideoUI.entries) {
		return
	}
	v := siteVideoUI.entries[siteVideoUI.cursor]
	playVideoURL(appConfig, v.URL)
}

// siteVideosLines is the screen's text, grouped by where each video came from.
func siteVideosLines(cursor int) []string {
	site := siteVideoUI.site
	title := site.Name
	if title == "" {
		title = site.Host
	}
	lines := []string{title + " · " + site.Host, ""}
	if siteVideoUI.loading {
		lines = append(lines, "Loading videos from "+site.Host+"…")
	}
	if siteVideoUI.query != "" && siteVideoUI.query != "trending" {
		lines = append(lines, "Search: "+siteVideoUI.query)
	}
	if siteVideoUI.err != "" {
		lines = append(lines, siteVideoUI.err)
	}
	if len(siteVideoUI.entries) == 0 {
		lines = append(lines,
			"No videos from this site are saved, favourited or watched yet.",
			"Search this site, or paste a link, to start.")
	} else {
		section := ""
		for i, v := range siteVideoUI.entries {
			if v.Section != section {
				section = v.Section
				lines = append(lines, "", section+":")
			}
			mark := " "
			if i == cursor {
				mark = "›"
			}
			lines = append(lines, mark+" "+v.Title+" · "+v.Label)
		}
	}
	lines = append(lines, "",
		"A plays the highlighted video · Y searches this site · X pastes a link")
	return lines
}

