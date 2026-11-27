package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestSiteVideoHostMatching(t *testing.T) {
	if !siteHostMatches("https://www.example.org/clip/1", "example.org") {
		t.Fatal("a www URL did not match its domain")
	}
	if !siteHostMatches("https://cdn.example.org/v.mp4", "example.org") {
		t.Fatal("a subdomain did not match")
	}
	if siteHostMatches("https://example.com/clip", "example.org") {
		t.Fatal("a different domain matched")
	}
	if siteHostMatches("", "example.org") {
		t.Fatal("an empty URL matched")
	}
}

func TestSiteSearchTargetsPerSite(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"soundcloud.com", "scsearch20:test words"},
		{"bilibili.com", "bilisearch20:test words"},
		{"nicovideo.jp", "nicosearch20:test words"},
		{"dailymotion.com", "https://www.dailymotion.com/search/test%20words/videos"},
		{"redgifs.com", "https://www.redgifs.com/browse?tags=test+words"},
		{"youtube.com", "ytsearch20:test words"},
		// A site whose search form is unknown gets no guessed URL.
		{"vimeo.com", ""},
		{"example.org", ""},
	} {
		if got := siteSearchTarget(tc.host, "test words"); got != tc.want {
			t.Fatalf("%s => %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestSiteThumbURLComesFromTheVideoURL(t *testing.T) {
	got := siteThumbURL([]siteFormat{
		{URL: "https://media.example.org/LooseBronzeSifaka-mobile.mp4"},
		{URL: "https://media.example.org/LooseBronzeSifaka.mp4"},
	})
	if got != "https://media.example.org/LooseBronzeSifaka-poster.jpg" {
		t.Fatalf("poster = %q", got)
	}
	if siteThumbURL(nil) != "" {
		t.Fatal("no formats produced a poster")
	}
}

func TestSiteTagBrowseURLUsesTheTypedDomain(t *testing.T) {
	if got := siteTagBrowseURL("redgifs.com", "asian"); got != "https://www.redgifs.com/browse?tags=asian" {
		t.Fatalf("browse URL = %q", got)
	}
	if siteTagBrowseURL("", "asian") != "" || siteTagBrowseURL("example.org", "") != "" {
		t.Fatal("an empty site or query made a URL")
	}
}

// A typed domain with no known search form must not start a fetch that would
// "succeed" with the trending feed: the grid stays and the toast explains.
func TestSiteVideosSearchRefusesAnUnknownSite(t *testing.T) {
	old := siteVideoUI
	defer func() { siteVideoUI = old }()
	siteVideoUI.site = videoSite{Name: "Vimeo", Host: "vimeo.com"}
	siteVideoUI.query, siteVideoUI.loading, siteVideoUI.err = "trending", false, ""
	siteVideosSearchSubmit("cats playing piano")
	if siteVideoUI.query != "trending" || siteVideoUI.loading || siteVideoUI.err != "" {
		t.Fatalf("an unknown site's search started: %+v", siteVideoUI)
	}
}

func TestSiteRedGifsSearchAsksForTags(t *testing.T) {
	// RedGifs ignores search_text (what yt-dlp sends) and answers with the
	// trending feed; the typed word has to travel as a tag.
	if got := redGifsSearchQuery("asian"); got != "count=20&order=trending&tags=asian" {
		t.Fatalf("keyword query = %q", got)
	}
	if got := redGifsSearchQuery("trending"); got != "count=20&order=trending" {
		t.Fatalf("default query = %q", got)
	}
	for _, host := range []string{"redgifs.com", "www.redgifs.com", "*.redgifs.com"} {
		if !siteIsRedGifs(host) {
			t.Fatalf("%s is not RedGifs", host)
		}
	}
	if siteIsRedGifs("example.org") || siteIsRedGifs("") {
		t.Fatal("another site was taken for RedGifs")
	}
}

func TestSiteRedGifsVideosCarryPosterTitleAndLength(t *testing.T) {
	var gif redGifsGif
	gif.ID = "LooseBronzeSifaka"
	gif.Tags = []string{"Asian", "Blonde"}
	gif.Duration = 16
	gif.Views = 1200
	gif.URLs.Poster = "https://media.redgifs.com/LooseBronzeSifaka-poster.jpg"
	videos := siteRedGifsVideos([]redGifsGif{gif, {}}, func(raw string) string { return "cached:" + raw })
	if len(videos) != 1 {
		t.Fatalf("%d videos from one gif", len(videos))
	}
	got := videos[0]
	if got.Title != "Asian Blonde" || got.URL != "https://www.redgifs.com/watch/LooseBronzeSifaka" {
		t.Fatalf("card = %+v", got)
	}
	if got.Label != "16s · 1200 views" {
		t.Fatalf("label = %q", got.Label)
	}
	if got.Thumb != "cached:https://media.redgifs.com/LooseBronzeSifaka-poster.jpg" {
		t.Fatalf("thumb = %q", got.Thumb)
	}
}

func TestRedGifsTokenIsCachedAndCanBeRefreshed(t *testing.T) {
	oldValue, oldAt := redGifsToken.value, redGifsToken.at
	defer func() {
		redGifsToken.mu.Lock()
		redGifsToken.value, redGifsToken.at = oldValue, oldAt
		redGifsToken.mu.Unlock()
	}()
	redGifsToken.mu.Lock()
	redGifsToken.value, redGifsToken.at = "", time.Time{}
	redGifsToken.mu.Unlock()

	authCalls := 0
	read := func(link, _ string, into interface{}) error {
		if !strings.Contains(link, "auth/temporary") {
			return nil
		}
		authCalls++
		out := into.(*struct {
			Token string `json:"token"`
		})
		out.Token = "token-" + strconv.Itoa(authCalls)
		return nil
	}
	first, err := redGifsTemporaryToken(read, false)
	if err != nil || first != "token-1" {
		t.Fatalf("first token = %q, %v", first, err)
	}
	second, err := redGifsTemporaryToken(read, false)
	if err != nil || second != first || authCalls != 1 {
		t.Fatalf("cached token = %q, auth calls = %d", second, authCalls)
	}
	fresh, err := redGifsTemporaryToken(read, true)
	if err != nil || fresh != "token-2" || authCalls != 2 {
		t.Fatalf("refreshed token = %q, auth calls = %d", fresh, authCalls)
	}
}

func TestSiteVideosSearchBarIsClickable(t *testing.T) {
	oldScene, oldKB, oldConfig := currentSceneIndex, virtualKeyboardActive, appConfig
	defer func() {
		currentSceneIndex, virtualKeyboardActive, appConfig = oldScene, oldKB, oldConfig
	}()
	c := &Config{Scenes: []SceneConfig{{Name: "Home"}, {Name: siteVideosSceneName}}}
	appConfig, currentSceneIndex = c, 1
	siteVideoUI.searchRect = sdl.Rect{X: 10, Y: 20, W: 400, H: 40}
	virtualKeyboardActive = false
	if !siteVideosHandleClick(30, 40, c) || !virtualKeyboardActive {
		t.Fatal("clicking the search bar did not open the keyboard")
	}
	if siteVideosHandleClick(900, 900, c) {
		t.Fatal("a click outside the bar and cards was consumed")
	}
}

func TestSiteVideosSceneIsInjectedOnce(t *testing.T) {
	c := &Config{}
	ensureSiteVideosScene(c)
	if sceneIndexExact(c, siteVideosSceneName) < 0 {
		t.Fatal("the site video scene was not added")
	}
	n := len(c.Scenes)
	ensureSiteVideosScene(c)
	if len(c.Scenes) != n || sceneIndexExact(c, siteVideosSceneName) < 0 {
		t.Fatal("the site video scene was added twice")
	}
	if _, ok := lookupScene(siteVideosSceneName); !ok {
		t.Fatal("the site video scene module is not registered")
	}
}

func TestSiteVideosScreenText(t *testing.T) {
	siteVideoUI.site = videoSite{Name: "Example", Host: "example.org"}
	siteVideoUI.entries = nil
	empty := strings.Join(siteVideosLines(0), "\n")
	if !strings.Contains(empty, "Example") || !strings.Contains(empty, "No videos") {
		t.Fatalf("empty screen = %s", empty)
	}
	siteVideoUI.entries = []siteVideo{
		{Title: "Offline clip", URL: "/card/clip.mp4", Section: "Downloaded", Label: "offline"},
		{Title: "Liked", URL: "https://www.example.org/watch", Section: "Favorites", Label: "favorite"},
	}
	lines := strings.Join(siteVideosLines(1), "\n")
	for _, want := range []string{"Downloaded:", "Favorites:", "› Liked", "A plays"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("screen has no %q:\n%s", want, lines)
		}
	}
}
