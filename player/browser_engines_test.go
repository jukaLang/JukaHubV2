package main

import (
	"encoding/base64"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// brwTestConfig is a config whose custom block holds the given key/value pairs.
func brwTestConfig(kv ...string) *Config {
	c := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	for i := 0; i+1 < len(kv); i += 2 {
		c.Variables.Custom[kv[i]] = kv[i+1]
	}
	return c
}

// An unknown or unusable choice must still search somewhere.
func TestBrwEngineForFallsBackToStartpage(t *testing.T) {
	custom := "https://searx.example/search?q=%s"
	cases := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"nil config", nil, "startpage"},
		{"unset", brwTestConfig(), "startpage"},
		{"google", brwTestConfig(brwEngineKey, "google"), "google"},
		{"case and spaces", brwTestConfig(brwEngineKey, "  Bing "), "bing"},
		{"unknown id", brwTestConfig(brwEngineKey, "altavista"), "startpage"},
		{"custom without an address", brwTestConfig(brwEngineKey, "custom"), "startpage"},
		{"custom with an address", brwTestConfig(brwEngineKey, "custom", brwEngineCustomKey, custom), "custom"},
		{"the builder's casing", brwTestConfig("BrowserSearchEngine", "startpage"), "startpage"},
	}
	for _, tc := range cases {
		if got := brwEngineFor(tc.cfg).ID; got != tc.want {
			t.Errorf("%s: engine %q, want %q", tc.name, got, tc.want)
		}
	}
	// A value of another type (a hand-edited file) is not a choice.
	odd := brwTestConfig()
	odd.Variables.Custom[brwEngineKey] = 42
	if got := brwEngineFor(odd).ID; got != "startpage" {
		t.Errorf("a number as the engine: %q, want startpage", got)
	}
}

func TestBrwEngineCatalogIsUsable(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range brwEngines {
		if e.ID == "" || e.Name == "" || e.Note == "" || seen[e.ID] {
			t.Errorf("engine %+v: empty field or duplicate id", e)
		}
		seen[e.ID] = true
		if strings.Count(e.Search, "%s") != 1 {
			t.Errorf("%s: results address %q needs exactly one %%s", e.ID, e.Search)
		}
		for _, address := range []string{e.Home, brwSearchURLWith(e, "test")} {
			u, err := url.Parse(address)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" {
				t.Errorf("%s: %q is not an https address", e.ID, address)
			}
		}
	}
	for _, id := range []string{brwEngineDefault, brwEngineWallAlt, "google"} {
		if !seen[id] {
			t.Errorf("the catalog lost %q", id)
		}
	}
	if seen[brwEngineCustomID] {
		t.Error("the catalog must not hold the custom engine's id")
	}
	if g, _ := brwEngineByID("google"); !g.Wall {
		t.Error("Google's results need JavaScript: it must be marked Wall")
	}
	if s := brwWallAltEngine(); s.Wall || s.ID != "startpage" {
		t.Errorf("the wall's way round is %+v, want Startpage", s)
	}
}

func TestBrwSearchURLEscapesTheWords(t *testing.T) {
	cases := []struct {
		engine, query, want string
	}{
		{"duckduckgo", "trimui smart pro", "https://lite.duckduckgo.com/lite/?q=trimui+smart+pro"},
		{"google", "a&b=c", "https://www.google.com/search?q=a%26b%3Dc"},
		{"startpage", "  spaced  ", "https://www.startpage.com/sp/search?query=spaced"},
		{"yahoo", "ü", "https://search.yahoo.com/search?p=%C3%BC"},
		{"wikipedia", "Go (programming language)", "https://en.m.wikipedia.org/w/index.php?search=Go+%28programming+language%29"},
	}
	for _, tc := range cases {
		e, ok := brwEngineByID(tc.engine)
		if !ok {
			t.Fatalf("engine %q is missing from the catalog", tc.engine)
		}
		if got := brwSearchURLWith(e, tc.query); got != tc.want {
			t.Errorf("%s %q: %q, want %q", tc.engine, tc.query, got, tc.want)
		}
	}
	// Words in the path of a custom address are a path segment.
	path := brwEngine{Search: "https://example.com/find/%s/all"}
	if got, want := brwSearchURLWith(path, "a b/c"), "https://example.com/find/a%20b%2Fc/all"; got != want {
		t.Errorf("path template: %q, want %q", got, want)
	}
	// No words: the engine's start page, not an empty search.
	def := brwDefaultEngine()
	if got := brwEngineAddress(def, "  "); got != def.Home {
		t.Errorf("an empty query opened %q, want the start page", got)
	}
}

// Every way into a search follows the setting: the address bar and the Search
// keyboard (normalize), and Home search.
func TestBrwSearchFollowsTheSetting(t *testing.T) {
	prev, prevPrompt := appConfig, browserSearchPrompt
	t.Cleanup(func() { appConfig, browserSearchPrompt = prev, prevPrompt })

	appConfig = brwTestConfig(brwEngineKey, "google")
	want := "https://www.google.com/search?q=x+y"
	if got := textBrowserSearchURL("x y"); got != want {
		t.Errorf("textBrowserSearchURL: %q, want %q", got, want)
	}
	if got, err := normalizeTextBrowserURL("x y"); err != nil || got != want {
		t.Errorf("normalize of a phrase: %q, %v, want %q", got, err, want)
	}
	if got := homeSearchWebURL(" x y "); got != want {
		t.Errorf("homeSearchWebURL: %q, want %q", got, want)
	}
	brwSyncSearchPrompt(appConfig)
	if browserSearchPrompt != "Search Google" {
		t.Errorf("the Search keyboard says %q, want %q", browserSearchPrompt, "Search Google")
	}

	appConfig = nil
	if got := textBrowserSearchURL("x"); got != "https://www.startpage.com/sp/search?query=x" {
		t.Errorf("no config: %q, want Startpage", got)
	}
}

func TestBrwCustomTemplate(t *testing.T) {
	cases := []struct {
		raw, want string
		ok        bool
	}{
		{"https://searx.example/search?q=%s", "https://searx.example/search?q=%s", true},
		{"searx.example/search?q=", "https://searx.example/search?q=%s", true},
		{"https://example.com/find/%s/all", "https://example.com/find/%s/all", true},
		{"http://localhost:8888/search?q=%s", "http://localhost:8888/search?q=%s", true},
		{"http://192.168.1.5:8080/?q=", "http://192.168.1.5:8080/?q=%s", true},
		{"https://example.com/search", "", false},
		{"ftp://example.com/?q=%s", "", false},
		{"https://user:pw@example.com/?q=%s", "", false},
		{"https://https://searx.example/search?q=%s", "", false},
		{"searx/search?q=%s", "", false},
		{"https://", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := brwCustomTemplate(tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%q: (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
	c := brwTestConfig(brwEngineKey, "custom", brwEngineCustomKey, "https://www.searx.example/search?q=")
	e := brwEngineFor(c)
	if e.ID != brwEngineCustomID || e.Name != "searx.example" || e.Home != "https://www.searx.example/" {
		t.Errorf("custom engine %+v", e)
	}
	if got := brwSearchURLFor(c, "a b"); got != "https://www.searx.example/search?q=a+b" {
		t.Errorf("custom search %q", got)
	}
}

func TestBrwHomeURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"follows the default engine", brwTestConfig(), "https://www.startpage.com/"},
		{"follows the chosen engine", brwTestConfig(brwEngineKey, "google"), "https://www.google.com/"},
		{"a typed host", brwTestConfig(brwHomePageKey, "news.ycombinator.com"), "https://news.ycombinator.com"},
		{"a full address", brwTestConfig(brwHomePageKey, "http://example.com/start?x=1"), "http://example.com/start?x=1"},
		{"a word is not an address", brwTestConfig(brwEngineKey, "bing", brwHomePageKey, "news"), "https://www.bing.com/"},
		{"another scheme", brwTestConfig(brwHomePageKey, "file:///etc/passwd"), "https://www.startpage.com/"},
		{"no config", nil, "https://www.startpage.com/"},
	}
	for _, tc := range cases {
		if got := brwHomeURL(tc.cfg); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Handoff skips a search engine's start page (the Browser at rest), not a home
// page of the user's own.
func TestBrwIsStartPage(t *testing.T) {
	c := brwTestConfig(brwHomePageKey, "https://news.example/")
	cases := []struct {
		address string
		want    bool
	}{
		{"https://lite.duckduckgo.com/lite/", true},
		{"https://www.google.com", true},
		{"https://search.brave.com/", true},
		{"https://news.example/", false},
		{"https://example.com/article", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := brwIsStartPage(c, tc.address); got != tc.want {
			t.Errorf("%q: %v, want %v", tc.address, got, tc.want)
		}
	}
}

func TestBrwSearchClickTarget(t *testing.T) {
	bing := "https://www.bing.com/ck/a?!&&p=abc&u=a1" +
		base64.RawURLEncoding.EncodeToString([]byte("https://example.com/page?x=1&y=2")) + "&ntb=1"
	yahoo := "https://r.search.yahoo.com/_ylt=Awr/RV=2/RE=1700000000/RO=10/RU=https%3a%2f%2fexample.com%2fdocs%2f/RK=2/RS=abc-"
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"duckduckgo", "https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa%3Fb%3Dc&rut=x", "https://example.com/a?b=c", true},
		{"google", "https://www.google.com/url?q=https://example.com/x&sa=U", "https://example.com/x", true},
		{"bing", bing, "https://example.com/page?x=1&y=2", true},
		{"yahoo", yahoo, "https://example.com/docs/", true},
		{"a page that only looks like one", "https://example.com/l/?uddg=https%3A%2F%2Fevil.example", "https://example.com/l/?uddg=https%3A%2F%2Fevil.example", false},
		{"a script target", "https://duckduckgo.com/l/?uddg=javascript%3Aalert(1)", "https://duckduckgo.com/l/?uddg=javascript%3Aalert(1)", false},
		{"a broken Bing payload", "https://www.bing.com/ck/a?u=a1!!!", "https://www.bing.com/ck/a?u=a1!!!", false},
	}
	for _, tc := range cases {
		got, ok := brwSearchClickTarget(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	// A Bing or Yahoo link to a YouTube video still plays in Media.
	video := "https://www.bing.com/ck/a?u=a1" +
		base64.RawURLEncoding.EncodeToString([]byte("https://www.youtube.com/watch?v=dQw4w9WgXcQ"))
	if link, ok := brwYouTubeVideo(video); !ok || link.ID != "dQw4w9WgXcQ" {
		t.Errorf("Bing-wrapped YouTube link: %+v, %v", link, ok)
	}
}

func TestBrwSearchWall(t *testing.T) {
	search := "https://www.google.com/search?q=trimui"
	cases := []struct {
		name, landed, body string
		wall               bool
	}{
		{"the enablejs landing", "https://www.google.com/httpservice/retry/enablejs?sei=abc", "", true},
		{"the robot check", "https://www.google.com/sorry/index?continue=x", "", true},
		{"a refresh the reader did not follow", search, `<noscript><meta content="0;url=/httpservice/retry/enablejs?sei=a" http-equiv="refresh"></noscript>`, true},
		{"Google's home page", "https://www.google.com/", "<form action=/search>", false},
		{"results without a wall", search, "<a href=/url?q=https://example.com>", false},
		{"another engine", "https://www.startpage.com/sp/search?query=trimui", "/httpservice/retry/enablejs", false},
	}
	for _, tc := range cases {
		landed, err := url.Parse(tc.landed)
		if err != nil {
			t.Fatal(err)
		}
		f, ok := brwSearchWall(search, landed, []byte(tc.body))
		if ok != tc.wall {
			t.Errorf("%s: wall %v, want %v", tc.name, ok, tc.wall)
			continue
		}
		if ok && (f.Kind != brwFailWall || f.URL != search || f.Title == "" || f.Detail == "") {
			t.Errorf("%s: card %+v", tc.name, f)
		}
	}
	if _, ok := brwSearchWall(search, nil, nil); ok {
		t.Error("a load with no landing is not a wall")
	}
	// The card offers a way round: Startpage on the handheld, the desktop's
	// own browser first on a PC.
	buttons := brwButtonsFor(brwFailure{Kind: brwFailWall, URL: search}, false)
	first := brwActWallSearch
	if brwHasDesktopBrowser() {
		first = brwActWallBrowser
	}
	if len(buttons) != 3 || buttons[0].action != first {
		t.Fatalf("wall buttons %+v", buttons)
	}
	for _, b := range buttons {
		if b.label == "" || b.icon == "" {
			t.Errorf("wall button %+v has no label or icon", b)
		}
	}
}

func TestBrwSearchQueryOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://www.google.com/search?q=trimui+smart&btnK=Google+Search", "trimui smart"},
		{"https://www.startpage.com/sp/search?query=a%26b", "a&b"},
		{"https://search.yahoo.com/search?p=yahoo", "yahoo"},
		{"https://en.m.wikipedia.org/w/index.php?search=Go", "Go"},
		{"https://www.google.com/", ""},
	}
	for _, tc := range cases {
		if got := brwSearchQueryOf(tc.in); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Settings → Browser: the home page carousel and list, with saved pages.
func TestBrwHomeSettingsRows(t *testing.T) {
	restore := browserBookmarksResetForTest(filepath.Join(t.TempDir(), "browser-bookmarks.json"))
	t.Cleanup(restore)
	if !browserBookmarkAdd("https://example.com/news", "News") {
		t.Fatal("could not save a page")
	}
	c := brwTestConfig(textBrowserURLVariable, "https://example.com/article")

	options := brwHomeOptions(c)
	if len(options) != 2 || options[0].Value != "" || options[0].Label != "Startpage start page" || options[1].Label != "News" {
		t.Fatalf("carousel %+v", options)
	}
	if got := brwHomeCurrent(c); got != 0 {
		t.Errorf("following the engine is stop %d, want 0", got)
	}

	brwSetHome(c, "https://example.com/news")
	if got := brwHomeCurrent(c); got != 1 {
		t.Errorf("the saved page is stop %d, want 1", got)
	}
	if got := brwHomeURL(c); got != "https://example.com/news" {
		t.Errorf("Home opens %q", got)
	}
	if !brwIsHomePage(c, "https://example.com/news#top") {
		t.Error("a #fragment is the same home page")
	}

	sheet := brwHomeSheet(c)
	var labels []string
	for _, o := range sheet {
		labels = append(labels, o.Label)
	}
	want := []string{"Startpage start page", "Page open in the Browser", "News", "Type an address…"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Errorf("sheet %q, want %q", labels, want)
	}
	if got := brwHomeSheetValue(c); got != "https://example.com/news" {
		t.Errorf("the sheet ticks %q", got)
	}

	// Text that is not an address: the row says so and Home opens the start page.
	brwSetHome(c, "news")
	if got := brwHomeCurrent(c); got != -1 {
		t.Errorf("a word is stop %d, want -1", got)
	}
	if got := brwHomeValue(c); got != "Not a web address" {
		t.Errorf("the row reads %q", got)
	}
	if got := brwHomeURL(c); got != brwDefaultEngine().Home {
		t.Errorf("Home opens %q, want the start page", got)
	}
}

func TestBrwEngineSettingsRows(t *testing.T) {
	c := brwTestConfig(brwEngineKey, "bing")
	options := brwEngineOptions(c)
	if len(options) != len(brwEngines) {
		t.Fatalf("carousel has %d stops, want %d", len(options), len(brwEngines))
	}
	if i := brwEngineCurrent(c); i < 0 || options[i].Value != "bing" {
		t.Errorf("current stop %d", i)
	}
	sheet := brwEngineSheet(c)
	if last := sheet[len(sheet)-1]; last.Value != brwSheetTypeEngine {
		t.Errorf("the sheet ends with %+v, want the custom address row", last)
	}

	// An unusable custom address: Startpage answers and the row says why.
	c = brwTestConfig(brwEngineKey, "custom", brwEngineCustomKey, "https://")
	if i := brwEngineCurrent(c); i < 0 || brwEngineOptions(c)[i].Value != "startpage" {
		t.Errorf("an unusable custom address is stop %d", i)
	}
	if detail := brwEngineDetail(c); !strings.Contains(detail, "isn't usable") {
		t.Errorf("the row does not say the custom address is unusable: %q", detail)
	}

	// A usable one is a stop of its own.
	c.Variables.Custom[brwEngineCustomKey] = "https://searx.example/search?q=%s"
	options = brwEngineOptions(c)
	if i := brwEngineCurrent(c); i != len(options)-1 || options[i].Value != brwEngineCustomID {
		t.Errorf("the custom engine is stop %d of %+v", i, options)
	}
}

// A typed address applies on submit only, and only when it is usable: anything
// else leaves the home page and the engine as they were.
func TestBrwPrefApply(t *testing.T) {
	restore := browserBookmarksResetForTest(filepath.Join(t.TempDir(), "browser-bookmarks.json"))
	prevPrompt := browserSearchPrompt
	t.Cleanup(func() {
		restore()
		browserSearchPrompt = prevPrompt
	})
	c := brwTestConfig(brwEngineKey, "bing", brwHomePageKey, "https://example.com/old")

	if _, warn := brwPrefApply(c, brwPrefEngine, "searx/search?q="); !warn {
		t.Error("an unusable search address was not refused")
	}
	if got := brwEngineFor(c).ID; got != "bing" {
		t.Errorf("a refused address changed the engine to %q", got)
	}
	if msg, warn := brwPrefApply(c, brwPrefEngine, "  "); warn || !strings.Contains(msg, "unchanged") {
		t.Errorf("an empty search address: %q, %v", msg, warn)
	}
	if msg, warn := brwPrefApply(c, brwPrefEngine, "searx.example/search?q="); warn || !strings.Contains(msg, "searx.example") {
		t.Errorf("a usable search address: %q, %v", msg, warn)
	}
	if e := brwEngineFor(c); e.ID != brwEngineCustomID || e.Search != "https://searx.example/search?q=%s" {
		t.Errorf("the custom engine is %+v", e)
	}

	if _, warn := brwPrefApply(c, brwPrefHome, "news"); !warn {
		t.Error("a word was accepted as the home page")
	}
	if got := brwHomeChoice(c); got != "https://example.com/old" {
		t.Errorf("a refused home page replaced the old one: %q", got)
	}
	if _, warn := brwPrefApply(c, brwPrefHome, "news.ycombinator.com"); warn {
		t.Error("a host was refused as the home page")
	}
	if got := brwHomeURL(c); got != "https://news.ycombinator.com" {
		t.Errorf("Home opens %q", got)
	}
	// Stepping to another stop keeps the typed address on the carousel.
	brwSetHome(c, "")
	found := false
	for _, o := range brwHomeOptions(c) {
		found = found || o.Value == "https://news.ycombinator.com"
	}
	if !found {
		t.Error("the typed home page left the carousel once another stop was chosen")
	}
	if _, warn := brwPrefApply(c, brwPrefHome, ""); warn || brwHomeChoice(c) != "" {
		t.Error("an empty home page must follow the engine")
	}

	// A copy of a key in other capitals (the web builder's) cannot shadow a
	// new value.
	c.Variables.Custom["BrowserHomePage"] = "https://stale.example/"
	brwSetHome(c, "https://fresh.example/")
	if got := brwHomeURL(c); got != "https://fresh.example/" {
		t.Errorf("Home opens %q after a capitalised copy", got)
	}
	if _, ok := c.Variables.Custom["BrowserHomePage"]; ok {
		t.Error("the capitalised copy was kept")
	}
}

// Right on the Home page row visits every stop once and comes back to the
// start page: choosing a stop must not reorder the row.
func TestBrwHomeCarouselCycles(t *testing.T) {
	restore := browserBookmarksResetForTest(filepath.Join(t.TempDir(), "browser-bookmarks.json"))
	t.Cleanup(restore)
	for _, page := range []string{"https://a.example/", "https://b.example/", "https://c.example/"} {
		if !browserBookmarkAdd(page, page) {
			t.Fatalf("could not save %s", page)
		}
	}
	c := brwTestConfig(brwHomeTypedKey, "https://typed.example/")
	n := len(brwHomeOptions(c))
	if n != 5 {
		t.Fatalf("the row has %d stops, want 5", n)
	}
	visited := map[string]bool{}
	for step := 0; step < n; step++ {
		options := brwHomeOptions(c)
		next := options[(brwHomeCurrent(c)+1)%len(options)].Value
		if visited[next] {
			t.Fatalf("step %d came back to %q before visiting every stop", step, next)
		}
		visited[next] = true
		brwSetHome(c, next)
	}
	if brwHomeChoice(c) != "" {
		t.Errorf("after a full turn the row is on %q, want the start page", brwHomeChoice(c))
	}
}
