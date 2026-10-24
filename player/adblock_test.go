package main

// Ad-blocking tests: the predicate must catch known ad/tracker hosts and
// unmistakable tracker paths without swallowing ordinary content, and a page
// load must not open a connection to a blocked subresource at all.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserURLIsAdMatchesHostsAndPaths(t *testing.T) {
	blocked := []string{
		"https://securepubads.g.doubleclick.net/tag/js/gpt.js",
		"https://www.google-analytics.com/analytics.js",
		"https://pagead2.googlesyndication.com/pagead/js/adsbygoogle.js",
		"https://static.criteo.net/js/ld/publishertag.js",
		"https://s.amazon-adsystem.com/iu3?d=1",
		"https://connect.facebook.net/en_US/fbevents.js",
		"https://www.facebook.com/tr?id=1&ev=PageView",
		"https://cdn.taboola.com/libtrc/loader.js",
		"https://example.com/ads/banner.png",
		"https://example.com/tracking/pixel.gif",
		"https://example.com/gtag/js?id=G-1",
		"https://example.com/collect?e=pageview",
	}
	for _, raw := range blocked {
		if !browserURLIsAd(raw) {
			t.Errorf("%s was not recognised as an ad/tracker request", raw)
		}
	}

	allowed := []string{
		"https://github.com/jukaLang/JukaHubV2",
		"https://cdn.jsdelivr.net/npm/htmx.org/dist/htmx.min.js",
		"https://notdoubleclick.net/logo.png",
		"https://example.com/tracks/song.mp3",
		"https://example.com/adsense-guide",
		"https://example.com/articles/a-good-read",
		"https://example.com/assets/app.css",
		"",
	}
	for _, raw := range allowed {
		if browserURLIsAd(raw) {
			t.Errorf("%s was wrongly blocked", raw)
		}
	}
}

func TestBrowserLoadSkipsAdSubresources(t *testing.T) {
	var hits = map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<html><head><title>Ads</title>
				<link rel="stylesheet" href="/pixel/pixel.css">
				<script src="/gtag/js?id=G-1"></script>
				</head><body><h1>Readable</h1>
				<p>body text</p>
				<img src="/ads/banner.png">
				<img src="/legit.png">
				</body></html>`)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer server.Close()

	page := browseWebPageResult(server.URL + "/page")
	for _, path := range []string{"/pixel/pixel.css", "/gtag/js", "/ads/banner.png"} {
		if hits[path] != 0 {
			t.Errorf("%s was requested %d time(s) despite being an ad/tracker", path, hits[path])
		}
	}
	if hits["/legit.png"] == 0 {
		t.Error("the page's own image was not requested")
	}
	if page.Blocked != 3 {
		t.Errorf("blocked = %d, want 3 (stylesheet, script, image)", page.Blocked)
	}
	if !strings.Contains(page.Text, "Blocked: 3") {
		t.Errorf("reader text does not report the blocked requests: %.160q", page.Text)
	}
}

// The strip pass must remove only ad scripts: a page whose content is produced
// by its own script still has to render.
func TestStripBlockedWebScriptsKeepsOwnScripts(t *testing.T) {
	base := adblockTestURL(t, "https://example.com/")
	source := `<html><body><script src="/ads/tracker.js"></script>` +
		`<script>document.write("hello")</script>` +
		`<script src="/app.js"></script></body></html>`
	got := stripBlockedWebScripts(source, base)
	if strings.Contains(got, "/ads/tracker.js") {
		t.Fatalf("ad script survived: %s", got)
	}
	if !strings.Contains(got, `document.write("hello")`) || !strings.Contains(got, "/app.js") {
		t.Fatalf("legitimate scripts were removed: %s", got)
	}
}

func TestBrowserAdBlockCountResetsPerPage(t *testing.T) {
	resetBrowserAdBlockCount()
	if got := browserBlockedRequests(); got != 0 {
		t.Fatalf("fresh count = %d, want 0", got)
	}
	if !browserRequestBlocked("https://example.com/page", "https://www.google-analytics.com/collect") {
		t.Fatal("tracker request was not blocked")
	}
	if got := browserBlockedRequests(); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	resetBrowserAdBlockCount()
	if got := browserBlockedRequests(); got != 0 {
		t.Fatalf("count after reset = %d, want 0", got)
	}
}

// browserAdBlockTestStore points the exception list at a temporary file so a
// test never touches the device's data directory.
func browserAdBlockTestStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "browser-block.json")
	prevPath := browserAdBlockSitesPath
	browserAdBlockSitesPath = func() string { return path }
	resetBrowserAdBlockSitesForTest()
	t.Cleanup(func() {
		browserAdBlockSitesPath = prevPath
		resetBrowserAdBlockSitesForTest()
	})
	return path
}

// A site exception covers its whole host (www and not), spares that page's
// requests, leaves every other site filtered, and survives a reload from disk.
func TestBrowserAdBlockSiteException(t *testing.T) {
	path := browserAdBlockTestStore(t)
	resetBrowserAdBlockCount()

	if browserAdBlockOff("https://example.com/page") {
		t.Fatal("a fresh install must filter every site")
	}
	if err := setBrowserAdBlockOff("https://www.example.com/article", true); err != nil {
		t.Fatalf("setBrowserAdBlockOff: %v", err)
	}
	if !browserAdBlockOff("https://example.com/other") || !browserAdBlockOff("https://www.example.com/") {
		t.Fatal("the exception did not cover the whole host")
	}
	adURL := "https://www.google-analytics.com/collect"
	if browserRequestBlocked("https://example.com/article", adURL) {
		t.Fatal("an excepted page still had a request blocked")
	}
	if got := browserBlockedRequests(); got != 0 {
		t.Fatalf("excepted page counted %d blocked request(s)", got)
	}
	if !browserRequestBlocked("https://other.example/article", adURL) {
		t.Fatal("the exception leaked to another site")
	}

	// The exception is on disk and loads again in a new process.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the exception file was not written: %v", err)
	}
	if !strings.Contains(string(raw), `"example.com"`) {
		t.Fatalf("exception file = %s", raw)
	}
	resetBrowserAdBlockSitesForTest()
	if !browserAdBlockOff("example.com/news") {
		t.Fatal("the saved exception was not loaded back")
	}

	// Script stripping follows the same switch: an excepted page keeps the
	// scripts a filtered page drops.
	source := `<html><body><script src="/ads/tracker.js"></script></body></html>`
	if got := stripBlockedWebScripts(source, adblockTestURL(t, "https://example.com/article")); !strings.Contains(got, "/ads/tracker.js") {
		t.Fatalf("excepted page lost its script: %s", got)
	}
	if got := stripBlockedWebScripts(source, adblockTestURL(t, "https://other.example/article")); strings.Contains(got, "/ads/tracker.js") {
		t.Fatalf("filtered page kept the ad script: %s", got)
	}

	if err := setBrowserAdBlockOff("https://example.com", false); err != nil {
		t.Fatalf("turning filtering back on: %v", err)
	}
	if browserAdBlockOff("https://example.com/article") {
		t.Fatal("the exception was not removed")
	}
}

// An excepted page loads every subresource and reports that filtering is off
// instead of a misleading "blocked: 0".
func TestBrowserLoadFetchesEverythingForExceptedSite(t *testing.T) {
	browserAdBlockTestStore(t)
	hits := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<html><head><title>Ads</title>
				<link rel="stylesheet" href="/pixel/pixel.css">
				<script src="/gtag/js?id=G-1"></script>
				</head><body><h1>Readable</h1><img src="/ads/banner.png"></body></html>`)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer server.Close()

	if err := setBrowserAdBlockOff(server.URL, true); err != nil {
		t.Fatalf("setBrowserAdBlockOff: %v", err)
	}
	page := browseWebPageResult(server.URL + "/page")
	for _, path := range []string{"/pixel/pixel.css", "/gtag/js", "/ads/banner.png"} {
		if hits[path] == 0 {
			t.Errorf("%s was never requested on an excepted page", path)
		}
	}
	if page.Blocked != 0 {
		t.Errorf("blocked = %d, want 0 for an excepted page", page.Blocked)
	}
	if !strings.Contains(page.Text, "Ad filtering is off for this site") {
		t.Errorf("reader text does not say filtering is off: %.160q", page.Text)
	}
	if strings.Contains(page.Text, "Blocked: 0") {
		t.Errorf("reader text reported a count for an excepted page: %.160q", page.Text)
	}
}

// The toolbar view is fed by the page that actually loaded, so the count is
// the one the load produced rather than a stale number from an earlier page.
func TestBrowserAdBlockViewFollowsThePage(t *testing.T) {
	browserAdBlockTestStore(t)
	config, elem, _ := browserCacheFixture(t, func(rawURL string) browserPage {
		return browserPage{Text: "body", FinalURL: rawURL, Blocked: 4}
	})

	loadTextBrowserURLAt(config, elem, "https://example.com/a", true)
	waitBrowserText(t, config, func(s string) bool { return s == "body" })
	view := browserAdBlockViewFor(elem.Variable)
	if view.Blocked != 4 || view.Site != "example.com" || view.Off {
		t.Fatalf("view = %+v, want example.com with 4 blocked", view)
	}
	if got := browserAdBlockLabel(view); got != "4 BLOCKED" {
		t.Fatalf("toolbar label = %q", got)
	}

	// An excepted site reports the switch, not a misleading zero.
	if err := setBrowserAdBlockOff("https://example.com", true); err != nil {
		t.Fatalf("setBrowserAdBlockOff: %v", err)
	}
	config.Variables.Custom[browserContentVariable] = ""
	loadTextBrowserURLAt(config, elem, "https://example.com/b", true)
	waitBrowserText(t, config, func(s string) bool { return s == "body" })
	view = browserAdBlockViewFor(elem.Variable)
	if !view.Off || view.Blocked != 4 {
		t.Fatalf("view after excepting the site = %+v", view)
	}
	if got := browserAdBlockLabel(view); got != "FILTER OFF" {
		t.Fatalf("toolbar label = %q, want FILTER OFF", got)
	}
}

func TestBrowserAdBlockLabel(t *testing.T) {
	cases := []struct {
		view browserAdBlockView
		want string
	}{
		{browserAdBlockView{Blocked: 3}, "3 BLOCKED"},
		{browserAdBlockView{}, "0 BLOCKED"},
		{browserAdBlockView{Blocked: 150}, "99+ BLOCKED"},
		{browserAdBlockView{Off: true, Blocked: 3}, "FILTER OFF"},
	}
	for _, tc := range cases {
		if got := browserAdBlockLabel(tc.view); got != tc.want {
			t.Errorf("label(%+v) = %q, want %q", tc.view, got, tc.want)
		}
	}
}

// The Filter button toggles the site on screen and reloads it, and the toolbar
// state follows without waiting for another navigation.
func TestBrowserToggleAdFilterIsPerSite(t *testing.T) {
	browserAdBlockTestStore(t)
	resetBrowserStateForTest()
	prevFetcher := browserPageFetcher
	browserPageFetcher = func(raw string) browserPage {
		return browserPage{Text: "Title: page\n", FinalURL: raw}
	}
	t.Cleanup(func() {
		browserPageFetcher = prevFetcher
		resetBrowserStateForTest()
	})

	config := &Config{Variables: Variables{Custom: map[string]interface{}{
		textBrowserURLVariable: "https://example.com/page",
	}}}
	config.Scenes = []SceneConfig{{Name: browserSceneName, Elements: []Element{
		{Type: "textbrowser", Text: "web", Variable: browserContentVariable},
	}}}

	if !browserToggleAdFilter(config) {
		t.Fatal("the toggle did not handle the press")
	}
	if !browserAdBlockOff("https://example.com/elsewhere") {
		t.Fatal("filtering was not turned off for the site")
	}
	if browserAdBlockOff("https://other.example/") {
		t.Fatal("the toggle excepted an unrelated site")
	}
	view := browserAdBlockViewFor(browserContentVariable)
	if view.Site != "example.com" || !view.Off {
		t.Fatalf("toolbar state = %+v, want example.com with filtering off", view)
	}

	if !browserToggleAdFilter(config) {
		t.Fatal("the second toggle did not handle the press")
	}
	if browserAdBlockOff("https://example.com/page") {
		t.Fatal("filtering stayed off for the site")
	}
}

func adblockTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return parsed
}
