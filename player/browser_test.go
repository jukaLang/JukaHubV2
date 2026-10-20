package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestEnsureBrowserSceneIsIdempotentAndWired(t *testing.T) {
	config := &Config{}
	ensureBrowserScene(config)
	ensureBrowserScene(config)
	if len(config.Scenes) != 1 {
		t.Fatalf("Browser scene added %d times, want 1", len(config.Scenes))
	}
	scene := config.Scenes[0]
	if scene.Name != browserSceneName {
		t.Fatalf("scene name = %q, want %q", scene.Name, browserSceneName)
	}
	var hasWeb, hasBack, hasIssues bool
	for _, elem := range scene.Elements {
		switch {
		case elem.Type == "textbrowser" && textBrowserElementSource(elem) == textBrowserSourceWeb:
			hasWeb = true
			if elem.Variable != browserContentVariable {
				t.Errorf("browser variable = %q, want %q", elem.Variable, browserContentVariable)
			}
		case elem.Trigger == "browser_back":
			hasBack = true
		case elem.Trigger == "browser_issues":
			hasIssues = true
		}
	}
	if !hasWeb {
		t.Fatal("Browser scene has no web textbrowser element")
	}
	if !hasBack || !hasIssues {
		t.Fatalf("Browser scene is missing quick-link triggers: back=%v issues=%v", hasBack, hasIssues)
	}

	// A config that already has a Browser scene is never overwritten.
	existing := &Config{Scenes: []SceneConfig{{Name: browserSceneName, Description: "custom"}}}
	ensureBrowserScene(existing)
	if len(existing.Scenes) != 1 || existing.Scenes[0].Description != "custom" {
		t.Fatal("ensureBrowserScene overwrote an existing scene")
	}
}

func TestExtractBrowserLinksResolvesAndFilters(t *testing.T) {
	base, err := url.Parse("https://github.com/jukaLang/JukaHubV2/issues")
	if err != nil {
		t.Fatal(err)
	}
	source := `
		<nav><a class="x" href="/jukaLang/JukaHubV2/issues/12">Crash on boot</a></nav>
		<a href='https://example.com/docs?x=1&amp;y=2'>Docs</a>
		<a href="#comments">Jump</a>
		<a href="javascript:void(0)">Nope</a>
		<a href="mailto:help@example.com">Mail</a>
		<a href="/jukaLang/JukaHubV2/issues/12">Crash again</a>
		<a href="/jukaLang/JukaHubV2/issues/new"><span>New</span> issue</a>
	`
	links := extractBrowserLinks(source, base)
	if len(links) != 3 {
		t.Fatalf("links = %+v, want 3 usable anchors", links)
	}
	if links[0].URL != "https://github.com/jukaLang/JukaHubV2/issues/12" {
		t.Errorf("relative link resolved to %q", links[0].URL)
	}
	if links[0].Text != "Crash on boot" {
		t.Errorf("anchor text = %q", links[0].Text)
	}
	if links[1].URL != "https://example.com/docs?x=1&y=2" {
		t.Errorf("entity-encoded URL = %q", links[1].URL)
	}
	if links[2].Text != "New issue" {
		t.Errorf("nested markup label = %q, want %q", links[2].Text, "New issue")
	}
}

func TestHtmlTagAttribute(t *testing.T) {
	cases := []struct {
		tag, name, want string
	}{
		{`a href="/one"`, "href", "/one"},
		{`a class='x' href='/two'`, "href", "/two"},
		{`a HREF=/three`, "href", "/three"},
		{`a data-href="/no" href="/yes"`, "href", "/yes"},
		{`a class="x"`, "href", ""},
	}
	for _, tc := range cases {
		if got := htmlTagAttribute(tc.tag, tc.name); got != tc.want {
			t.Errorf("htmlTagAttribute(%q, %q) = %q, want %q", tc.tag, tc.name, got, tc.want)
		}
	}
}

func TestBrowserHistoryBackForward(t *testing.T) {
	resetBrowserStateForTest()
	browserRecordURL("https://example.com/a")
	browserRecordURL("https://example.com/b")
	browserRecordURL("https://example.com/c")

	if got, ok := browserHistoryStep(-1); !ok || got != "https://example.com/b" {
		t.Fatalf("back = %q, %v; want b", got, ok)
	}
	if got, ok := browserHistoryStep(-1); !ok || got != "https://example.com/a" {
		t.Fatalf("back = %q, %v; want a", got, ok)
	}
	if _, ok := browserHistoryStep(-1); ok {
		t.Fatal("back past the first page must fail")
	}
	if got, ok := browserHistoryStep(1); !ok || got != "https://example.com/b" {
		t.Fatalf("forward = %q, %v; want b", got, ok)
	}

	// A new navigation truncates the forward branch.
	browserRecordURL("https://example.com/d")
	if got, ok := browserHistoryStep(1); ok {
		t.Fatalf("truncated forward entry was kept: %q", got)
	}
	if got, ok := browserHistoryStep(-1); !ok || got != "https://example.com/b" {
		t.Fatalf("back after new navigation = %q, %v; want b", got, ok)
	}
}

func TestBrowserHistoryDeduplicatesCurrentPage(t *testing.T) {
	resetBrowserStateForTest()
	browserRecordURL("https://example.com/a")
	browserRecordURL("https://example.com/a")
	if got, ok := browserHistoryStep(-1); ok {
		t.Fatalf("duplicate navigation created an entry: %q", got)
	}
}

func TestBrowseWebPageResultExtractsReadableTextAndLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><head><title>Issues</title></head><body>
			<h1>Open issues</h1>
			<p>Everything is working.</p>
			<a href="/issues/7">Fix the launcher</a>
		</body></html>`)
	}))
	defer server.Close()

	page := browseWebPageResult(server.URL)
	if !strings.Contains(page.Text, "Title: Issues") || !strings.Contains(page.Text, "Everything is working.") {
		t.Fatalf("reader text = %q", page.Text)
	}
	if len(page.Links) != 1 {
		t.Fatalf("links = %+v, want one", page.Links)
	}
	if page.Links[0].URL != server.URL+"/issues/7" || page.Links[0].Text != "Fix the launcher" {
		t.Fatalf("link = %+v", page.Links[0])
	}
	if page.FinalURL != server.URL {
		t.Fatalf("final URL = %q, want %q", page.FinalURL, server.URL)
	}
}

func TestBrowserLinkPanelRendersSelectableRows(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	resetBrowserStateForTest()
	setBrowserLinks(browserContentVariable, []browserLink{
		{Text: "Fix the launcher", URL: "https://github.com/jukaLang/JukaHubV2/issues/7"},
		{Text: "Release notes", URL: "https://github.com/jukaLang/JukaHubV2/releases"},
		{Text: "Docs", URL: "https://github.com/jukaLang/JukaHubV2/tree/main/docs"},
	})
	browserLinkPanel = browserLinkPanelState{open: true, variable: browserContentVariable}
	t.Cleanup(func() { resetBrowserStateForTest() })

	snapshotHomeGlobals(t)
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	renderBrowserLinkPanel(renderer, &Config{})

	if browserLinkPanelRect.W <= 0 || browserLinkPanelRect.H <= 0 {
		t.Fatalf("panel rect not set: %+v", browserLinkPanelRect)
	}
	if len(browserLinkRowHits) != 3 {
		t.Fatalf("rendered %d link rows, want 3", len(browserLinkRowHits))
	}
	for _, hit := range browserLinkRowHits {
		r := hit.rect
		if r.X < browserLinkPanelRect.X || r.Y < browserLinkPanelRect.Y ||
			r.X+r.W > browserLinkPanelRect.X+browserLinkPanelRect.W ||
			r.Y+r.H > browserLinkPanelRect.Y+browserLinkPanelRect.H {
			t.Errorf("link row %+v escapes panel %+v", r, browserLinkPanelRect)
		}
	}
}

func TestBrowserLinkStoreKeepsPagesSeparate(t *testing.T) {
	resetBrowserStateForTest()
	setBrowserLinks("browser_content", []browserLink{{Text: "One", URL: "https://example.com/1"}})
	setBrowserLinks("other_content", []browserLink{{Text: "Two", URL: "https://example.com/2"}})
	if got := browserLinksFor("browser_content"); len(got) != 1 || got[0].Text != "One" {
		t.Fatalf("browser links = %+v", got)
	}
	if got := browserLinksFor("other_content"); len(got) != 1 || got[0].Text != "Two" {
		t.Fatalf("other links = %+v", got)
	}
	// The returned slice is a copy; mutating it must not corrupt the store.
	got := browserLinksFor("browser_content")
	got[0].Text = "Changed"
	if again := browserLinksFor("browser_content"); again[0].Text != "One" {
		t.Fatalf("link store was mutated through its getter: %+v", again)
	}
}
