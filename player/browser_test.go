package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unsafe"

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

// The injected Browser scene must fit whatever panel it opens on: an element
// that runs past the viewport is unreachable on a smaller handheld.
func TestInjectedBrowserSceneFitsTheScreen(t *testing.T) {
	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 544}, {800, 480}} {
		withScreen(t, size.w, size.h)
		config := &Config{}
		ensureBrowserScene(config)
		if len(config.Scenes) != 1 {
			t.Fatalf("%dx%d: injected %d scenes, want 1", size.w, size.h, len(config.Scenes))
		}
		for _, elem := range config.Scenes[0].Elements {
			w := getElementWidth(elem, 0)
			h := getElementHeight(elem, 0)
			if w <= 0 || h <= 0 {
				t.Errorf("%dx%d: %s element has no size (%dx%d)", size.w, size.h, elem.Text, w, h)
				continue
			}
			if elem.X < 0 || elem.Y < 0 || elem.X+w > size.w || elem.Y+h > size.h {
				t.Errorf("%dx%d: %s element %+v (%dx%d) escapes the viewport", size.w, size.h, elem.Text, elem, w, h)
			}
		}
	}
}

// The injected scene must render: the page panel, the address bar and the
// button row are all drawn by the same renderer as the rest of the UI.
func TestInjectedBrowserSceneRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	withScreen(t, 1280, 720)
	ensureBrowserScene(config)
	idx := sceneIndexExact(config, browserSceneName)
	if idx < 0 {
		t.Fatal("Browser scene was not injected")
	}
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	prevScene, prevConfig := currentSceneIndex, appConfig
	currentSceneIndex, appConfig = idx, config
	t.Cleanup(func() { currentSceneIndex, appConfig = prevScene, prevConfig })

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderScene(renderer, config, config.Scenes[idx])

	// The address bar and Go button are the browser's own hit targets; they
	// only exist once renderTextBrowser has drawn the web layout.
	elem, ok := findBrowserElement(config)
	if !ok {
		t.Fatal("no web element in the injected scene")
	}
	if addr, goBtn := textBrowserToolbarRects(elem); addr.W <= 0 || goBtn.W <= 0 {
		t.Fatalf("toolbar not laid out: address %+v go %+v", addr, goBtn)
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

// Only http(s) links survive. The extractor used a scheme blocklist before,
// which a href like " jav&#x09;ascript:" or "JaVaScRiPt:" could slip past; the
// parsed-scheme allowlist below rejects every one of those spellings.
func TestExtractBrowserLinksAllowsOnlyHTTP(t *testing.T) {
	base, err := url.Parse("https://example.com/page")
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	source := `<a href="https://example.com/ok">ok</a>` +
		`<a href="/relative">relative</a>` +
		`<a href="javascript:alert(1)">js</a>` +
		`<a href="JaVaScRiPt:alert(2)">js upper</a>` +
		`<a href=" jav&#x09;ascript:alert(3)">js tab</a>` +
		`<a href="data:text/html;base64,PHNjcmlwdD4=">data</a>` +
		`<a href="file:///etc/passwd">file</a>` +
		`<a href="mailto:someone@example.com">mail</a>`

	links := extractBrowserLinks(source, base)
	for _, link := range links {
		if link.URL != "https://example.com/ok" && link.URL != "https://example.com/relative" {
			t.Errorf("non-http link leaked through: %q", link.URL)
		}
	}
	if len(links) != 2 {
		t.Fatalf("links = %+v, want the http and relative ones only", links)
	}
}

// The address bar doubles as a search box: typing a host opens it, typing
// anything that is not a host searches for it.
func TestBrowserAddressBarSearchesBareWords(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"example.com", "https://example.com"},
		{"https://example.com/page", "https://example.com/page"},
		{"192.168.1.4:8080", "https://192.168.1.4:8080"},
		{"weather in paris", browserSearchURL + "weather+in+paris"},
		{"jukahub", browserSearchURL + "jukahub"},
	}
	for _, tc := range cases {
		got, err := normalizeTextBrowserURL(tc.in)
		if err != nil {
			t.Errorf("normalizeTextBrowserURL(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeTextBrowserURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := normalizeTextBrowserURL("   "); err == nil {
		t.Error("an empty address must be an error, not a search")
	}
}

// A page is wrapped once and reused until the text or the width changes. This
// is what keeps a long article at 20 fps on a 1 GB handheld instead of
// re-measuring every word each frame.
func TestTextBrowserWrapCacheReusesPerWidth(t *testing.T) {
	resetTextBrowserWrapCache()
	t.Cleanup(resetTextBrowserWrapCache)
	page := "alpha beta\n" + strings.Repeat("word ", 40)

	first := textBrowserWrappedLines(page, nil, 300)
	second := textBrowserWrappedLines(page, nil, 300)
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("no lines produced")
	}
	if &first[0] != &second[0] {
		t.Fatal("the same page at the same width was wrapped again")
	}
	wider := textBrowserWrappedLines(page, nil, 600)
	if &wider[0] == &first[0] {
		t.Fatal("a different width reused the old wrap")
	}
	resetTextBrowserWrapCache()
	afterReset := textBrowserWrappedLines(page, nil, 300)
	if &afterReset[0] == &first[0] {
		t.Fatal("resetTextBrowserWrapCache kept the old wrap")
	}
}

// The reader text is capped so a giant article cannot grow the page buffer and
// the wrap on a device with 1 GB of RAM.
func TestCapTextBrowserTextTruncatesLongPages(t *testing.T) {
	short := "a short page"
	if got := capTextBrowserText(short); got != short {
		t.Errorf("short page changed to %q", got)
	}
	long := strings.Repeat("line of page text\n", (textBrowserMaxTextBytes/17)+100)
	got := capTextBrowserText(long)
	if !strings.Contains(got, "Page truncated") {
		t.Fatal("oversized page was not marked as truncated")
	}
	if len(got) > textBrowserMaxTextBytes+200 {
		t.Fatalf("capped page is still %d bytes", len(got))
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
	defer destroyTestRenderer(renderer)
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

// ── D-pad link focus ─────────────────────────────────────────────────────

func browserCursorTestElement(variable string) Element {
	return Element{
		Type: "textbrowser", Text: "web", Variable: variable, Font: "small",
		X: 24, Y: 72, Width: "1232", Height: "496",
	}
}

func resetBrowserCursorForTest() {
	browserLinkCursor = browserLinkCursorState{}
	textBrowserScrollY = 0
	textBrowserScrollVelocity = 0
	textBrowserScrollCooldown = 0
}

// Left/Right walk the page's links in reading order and scroll the focused one
// into view; a fresh page starts with no link picked.
func TestBrowserLinkCursorWalksThePage(t *testing.T) {
	config := layoutTestConfig(t)
	variable := "browser_cursor_test"
	doc := parseWebPage("<body><p>"+strings.Repeat("filler text ", 120)+
		`</p><p><a href="/one">one</a> and <a href="/two">two</a></p></body>`,
		"", "https://example.com/page")
	setBrowserDocument(variable, doc)
	resetBrowserCursorForTest()
	t.Cleanup(func() {
		setBrowserDocument(variable, nil)
		resetBrowserCursorForTest()
	})
	elem := browserCursorTestElement(variable)
	layout := browserPageLayout(config, elem)
	if layout == nil || len(layout.Links) != 2 {
		t.Fatalf("layout links = %+v", layout)
	}
	if got := browserFocusedLink(elem, layout); got != -1 {
		t.Fatalf("a fresh page focused link %d", got)
	}

	if !browserMovePageLink(config, elem, 1) {
		t.Fatal("Right was not handled")
	}
	if got := browserLinkCursor.cursor; got != 0 {
		t.Fatalf("Right focused %d, want the first link", got)
	}
	downLinkRow := layout.Links[0].Row
	downLinks := layout.Rows[downLinkRow]
	if len(downLinks.Runs) == 0 {
		t.Fatal("link row has no runs")
	}
	visible := browserVisibleRows(elem, layout)
	if int(textBrowserScrollY) > downLinkRow || int(textBrowserScrollY)+visible <= downLinkRow {
		t.Fatalf("scroll %d does not show link row %d (visible %d)", textBrowserScrollY, downLinkRow, visible)
	}

	browserMovePageLink(config, elem, 1)
	if got := browserLinkCursor.cursor; got != 1 {
		t.Fatalf("a second Right focused %d, want the second link", got)
	}
	browserMovePageLink(config, elem, 1)
	if got := browserLinkCursor.cursor; got != 0 {
		t.Fatalf("Right past the last link focused %d, want a wrap to 0", got)
	}
	browserMovePageLink(config, elem, -1)
	if got := browserLinkCursor.cursor; got != 1 {
		t.Fatalf("Left from the first link focused %d, want a wrap to the last", got)
	}

	// A new page never inherits the old cursor position.
	setBrowserDocument(variable, parseWebPage(`<body><p><a href="/x">x</a> <a href="/y">y</a></p></body>`, "", "https://example.com/other"))
	if got := browserFocusedLink(elem, browserPageLayout(config, elem)); got != -1 {
		t.Fatalf("the new page kept a link cursor (%d)", got)
	}
}

// Only http(s) destinations can be followed, and relative hrefs resolve
// against the page they came from.
func TestBrowserResolveLink(t *testing.T) {
	cases := []struct {
		base, href string
		want       string
		ok         bool
	}{
		{"https://example.com/docs/page", "/issues/7", "https://example.com/issues/7", true},
		{"https://example.com/docs/page", "next?x=1", "https://example.com/docs/next?x=1", true},
		{"https://example.com/docs/page", "https://other.example/x#frag", "https://other.example/x", true},
		{"https://example.com", "javascript:alert(1)", "", false},
		{"https://example.com", "mailto:hi@example.com", "", false},
		{"https://example.com", "file:///etc/passwd", "", false},
		{"https://example.com", "#anchor", "", false},
		{"https://example.com", "   ", "", false},
	}
	for _, tc := range cases {
		got, ok := browserResolveLink(tc.base, tc.href)
		if ok != tc.ok || got != tc.want {
			t.Errorf("browserResolveLink(%q, %q) = (%q, %v), want (%q, %v)",
				tc.base, tc.href, got, ok, tc.want, tc.ok)
		}
	}
}

// The toolbar reserves a Search chip beside Go, and the three controls never
// overlap or leave the element.
func TestBrowserToolbarHasSearchChip(t *testing.T) {
	elem := browserCursorTestElement(browserContentVariable)
	address, search, goBtn := textBrowserToolbarLayout(elem)
	width := getElementWidth(elem, 1100)
	for _, r := range []sdl.Rect{address, search, goBtn} {
		if r.W <= 0 || r.H <= 0 {
			t.Fatalf("empty toolbar control: %+v", r)
		}
		if r.X < elem.X || r.X+r.W > elem.X+width {
			t.Fatalf("toolbar control %+v escapes the panel", r)
		}
	}
	if address.X+address.W > search.X || search.X+search.W > goBtn.X {
		t.Fatalf("toolbar controls overlap: address %+v search %+v go %+v", address, search, goBtn)
	}
	if got := textBrowserSearchRect(elem); got != search {
		t.Fatalf("search hit area = %+v, want %+v", got, search)
	}
	legacyAddress, legacyGo := textBrowserToolbarRects(elem)
	if legacyAddress != address || legacyGo != goBtn {
		t.Fatal("the two-rect accessor disagrees with the three-rect layout")
	}
}

// Searching must not overwrite the address of the page on screen: the query
// has its own variable, so cancelling the keyboard leaves the browser alone.
func TestBrowserSearchTypingKeepsTheAddress(t *testing.T) {
	config := &Config{}
	config.Scenes = []SceneConfig{{Name: browserSceneName, Elements: []Element{
		{Type: "textbrowser", Text: "web", Variable: browserContentVariable},
	}}}
	config.Variables.Custom = map[string]interface{}{
		textBrowserURLVariable: "https://example.com/page",
	}

	prevScene, prevElem, prevBuffer := activeSceneIndex, activeElementIndex, inputTextBuffer
	prevSearch := textBrowserSearchEditing
	t.Cleanup(func() {
		activeSceneIndex, activeElementIndex = prevScene, prevElem
		inputTextBuffer = prevBuffer
		textBrowserSearchEditing = prevSearch
	})
	activeSceneIndex, activeElementIndex = 0, 0
	inputTextBuffer = "jukahub release notes"
	textBrowserSearchEditing = true

	updateInputVariable(config)
	if got, _ := config.Variables.Custom[browserSearchVariable].(string); got != "jukahub release notes" {
		t.Fatalf("search buffer = %q", got)
	}
	if got := config.Variables.Custom[textBrowserURLVariable]; got != "https://example.com/page" {
		t.Fatalf("the search overwrote the address: %v", got)
	}
	searchURL, err := normalizeTextBrowserURL("jukahub release notes")
	if err != nil || searchURL != browserSearchURL+"jukahub+release+notes" {
		t.Fatalf("search query normalised to %q, %v", searchURL, err)
	}
}

// The scene's controller path reaches the page link cursor: Right/Left are
// consumed by the browser instead of scrolling the element behind it.
func TestBrowserControllerWalksLinks(t *testing.T) {
	config := layoutTestConfig(t)
	variable := "browser_pad_test"
	doc := parseWebPage(`<body><p><a href="/one">one</a></p><p><a href="/two">two</a></p></body>`, "", "https://example.com/pad")
	setBrowserDocument(variable, doc)
	resetBrowserCursorForTest()
	t.Cleanup(func() {
		setBrowserDocument(variable, nil)
		resetBrowserCursorForTest()
	})
	elem := browserCursorTestElement(variable)

	right := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_RIGHT}
	left := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_LEFT}
	if !handleBrowserController(nil, right, config, elem) {
		t.Fatal("Right was not consumed by the browser")
	}
	if got := browserLinkCursor.cursor; got != 0 {
		t.Fatalf("Right focused %d, want 0", got)
	}
	if !handleBrowserController(nil, right, config, elem) {
		t.Fatal("a second Right was not consumed")
	}
	if got := browserLinkCursor.cursor; got != 1 {
		t.Fatalf("second Right focused %d, want 1", got)
	}
	if !handleBrowserController(nil, left, config, elem) {
		t.Fatal("Left was not consumed")
	}
	if got := browserLinkCursor.cursor; got != 0 {
		t.Fatalf("Left focused %d, want 0", got)
	}

	// Non-web text panels keep their old scroll-only model.
	plain := Element{Type: "textbrowser", Text: "system", Variable: variable}
	if handleBrowserController(nil, right, config, plain) {
		t.Fatal("the web controller handled a plain-text panel")
	}
}

// The focused link is drawn, not only tracked: the page must change when the
// cursor moves, otherwise the D-pad would be selecting invisible things.
func TestBrowserLinkFocusIsDrawn(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := layoutTestConfig(t)
	variable := "browser_focus_draw"
	doc := parseWebPage(`<body><p><a href="/one">first link</a></p><p><a href="/two">second link</a></p></body>`, "", "https://example.com/page")
	setBrowserDocument(variable, doc)
	resetBrowserCursorForTest()
	t.Cleanup(func() {
		setBrowserDocument(variable, nil)
		resetBrowserCursorForTest()
	})
	elem := browserCursorTestElement(variable)

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	capture := func() []byte {
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		renderTextBrowser(renderer, config, elem)
		buf := make([]byte, probeW*probeH*4)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
			t.Skipf("ReadPixels unavailable: %v", err)
		}
		return buf
	}
	plain := capture()
	if !browserMovePageLink(config, elem, 1) {
		t.Fatal("Right was not handled")
	}
	focused := capture()
	diff := 0
	for i := range plain {
		if plain[i] != focused[i] {
			diff++
		}
	}
	if diff < 200 {
		t.Fatalf("the focused link changed only %d bytes of the frame", diff)
	}
}

// The kinetic-scroll clamp must use the styled page's row count. Measuring a
// web page as 22px text lines used to snap every scroll on it back to the top.
func TestTextBrowserScrollLimitUsesTheLayout(t *testing.T) {
	config := layoutTestConfig(t)
	variable := "browser_scroll_limit"
	doc := parseWebPage("<body>"+strings.Repeat("<p>a line of page text</p>", 120)+"</body>", "", "https://example.com/long")
	setBrowserDocument(variable, doc)
	t.Cleanup(func() { setBrowserDocument(variable, nil) })
	elem := browserCursorTestElement(variable)
	layout := browserPageLayout(config, elem)
	if layout == nil {
		t.Fatal("no layout")
	}
	if got, want := textBrowserScrollLimit(config, elem), browserScrollMax(elem, layout); got != want || got <= 0 {
		t.Fatalf("web scroll limit = %d, want %d", got, want)
	}

	// A plain-text panel keeps the 22px-line rule.
	plain := Element{Type: "textbrowser", Text: "system", Variable: "plain_scroll_limit"}
	config.Variables.Custom[plain.Variable] = strings.Repeat("x\n", 400)
	if got := textBrowserScrollLimit(config, plain); got <= 0 {
		t.Fatalf("plain text scroll limit = %d", got)
	}
}

// The injected scene carries the Search chip, and seven chips still fit the
// smallest panel the app targets.
func TestBrowserSceneHasSearchChip(t *testing.T) {
	config := &Config{}
	ensureBrowserScene(config)
	found := false
	for _, elem := range config.Scenes[0].Elements {
		if elem.Trigger == "browser_search" {
			found = true
		}
	}
	if !found {
		t.Fatal("Browser scene has no Search chip")
	}
}
