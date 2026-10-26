package main

// The line under the page names the hosts the saved cookies belong to, so the
// count on the Forget chip is attached to a site instead of being a bare
// number. These tests pin what the line says, how a jar's records collapse into
// hosts, and that drawing the browser really puts that line on screen.

import (
	"net/http"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// testCookie is a plain session cookie for the given path.
func testCookie(name string) *http.Cookie {
	return &http.Cookie{Name: name, Value: "v", Path: "/"}
}

// TestBrowserCookieSitesGroupByHost walks a jar holding three sites, one of
// them behind a www. prefix, and checks the hosts come out busiest first with
// ties broken by name.
func TestBrowserCookieSitesGroupByHost(t *testing.T) {
	useTempBrowserCookiePath(t)
	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://www.youtube.com/watch"), []*http.Cookie{
		testCookie("session"),
		testCookie("consent"),
	})
	jar.SetCookies(mustURL(t, "https://github.com/login"), []*http.Cookie{
		testCookie("logged_in"),
	})
	// A domain-scoped cookie belongs to the domain, not to the page that set
	// it: mail.google.com is not the site, google.com is.
	jar.SetCookies(mustURL(t, "https://mail.google.com/"), []*http.Cookie{
		{Name: "scoped", Value: "v", Path: "/", Domain: ".google.com"},
	})

	sites := browserCookieSites()
	want := []browserCookieSite{
		{Domain: "youtube.com", Count: 2},
		{Domain: "github.com", Count: 1},
		{Domain: "google.com", Count: 1},
	}
	if len(sites) != len(want) {
		t.Fatalf("sites = %+v, want %+v", sites, want)
	}
	for i := range want {
		// Domain and counts are what this test pins; whole-struct equality
		// would also compare Since, a timestamp the jar stamped when the
		// cookie was set and the test cannot predict. It is checked for
		// being there instead — a live jar's site has an age to show.
		if sites[i].Domain != want[i].Domain || sites[i].Count != want[i].Count {
			t.Fatalf("site %d = %+v, want domain/count %+v (all: %+v)", i, sites[i], want[i], sites)
		}
		if sites[i].Since.IsZero() {
			t.Fatalf("site %d (%s) carries no session start", i, sites[i].Domain)
		}
	}

	// The names on screen are the same records the count is built from.
	total := 0
	for _, s := range sites {
		total += s.Count
	}
	if got := browserCookiesCount(); got != total {
		t.Fatalf("count %d does not match the %d cookies behind %d sites", got, total, len(sites))
	}
}

// TestBrowserCookieSitesLine pins the wording: the singular form, the comma
// list, the "+N more" tail that keeps a busy jar off the strip.
func TestBrowserCookieSitesLine(t *testing.T) {
	cases := []struct {
		sites []browserCookieSite
		want  string
	}{
		{nil, "No saved cookies"},
		{[]browserCookieSite{}, "No saved cookies"},
		{[]browserCookieSite{{Domain: "example.com", Count: 2}}, "Saved cookie: example.com"},
		{[]browserCookieSite{{Domain: "youtube.com", Count: 2}, {Domain: "github.com", Count: 1}},
			"Saved cookies: youtube.com, github.com"},
		{[]browserCookieSite{
			{Domain: "a.example", Count: 4},
			{Domain: "b.example", Count: 3},
			{Domain: "c.example", Count: 2},
			{Domain: "d.example", Count: 1},
			{Domain: "e.example", Count: 1},
		}, "Saved cookies: a.example, b.example, c.example, +2 more"},
	}
	for _, c := range cases {
		if got := browserCookieSitesLine(c.sites); got != c.want {
			t.Errorf("browserCookieSitesLine(%d sites) = %q, want %q", len(c.sites), got, c.want)
		}
	}
}

// TestBrowserPageNamesTheCookieSites is the wiring: drawing the web panel puts
// the line on screen, in the site order the jar reports, and a Forget empties
// it back to the empty state on the next frame.
func TestBrowserPageNamesTheCookieSites(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	useTempBrowserCookiePath(t)

	config := layoutTestConfig(t)
	ensureBrowserScene(config)
	chip := forgetChipElement(t, config)

	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://www.youtube.com/watch"), []*http.Cookie{
		testCookie("session"),
		testCookie("consent"),
	})
	jar.SetCookies(mustURL(t, "https://github.com/login"), []*http.Cookie{
		testCookie("logged_in"),
	})

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	elem := browserCursorTestElement("browser_cookie_sites")
	renderTextBrowser(renderer, config, elem)

	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Skip("no font available for the text cache probe")
	}
	want := "Saved cookies: youtube.com, github.com"
	if GetTextCache().Get(font, want, ColorTextSecondary()) == nil {
		t.Fatalf("the page did not draw %q", want)
	}
	// The chip and the strip describe the same jar.
	if chip.Text != "Forget 3" {
		t.Fatalf("chip under the newer line reads %q, want \"Forget 3\"", chip.Text)
	}

	// Forget leaves nothing to name, and the strip says so rather than
	// disappearing.
	if _, err := browserForgetCookies(); err != nil {
		t.Fatalf("forget: %v", err)
	}
	ClearTextCache()
	renderTextBrowser(renderer, config, elem)
	if GetTextCache().Get(font, "No saved cookies", ColorTextTertiary()) == nil {
		t.Fatal("an empty jar left the strip silent instead of saying so")
	}
	if chip.Text != "Forget" {
		t.Fatalf("after Forget the chip reads %q", chip.Text)
	}
}

// TestBrowserCookieStripFitsTheSmallPanel draws the browser on an 800x480
// handheld too: the strip is inside the page panel, so it must not overflow it
// or paint over the chip row underneath.
func TestBrowserCookieStripFitsTheSmallPanel(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	useTempBrowserCookiePath(t)
	withScreen(t, 800, 480)

	config := &Config{}
	ensureBrowserScene(config)
	elem, ok := findBrowserElement(config)
	if !ok {
		t.Fatal("no web element in the injected scene")
	}

	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{
		testCookie("session"),
	})
	renderTextBrowser(renderer, config, elem)

	// The strip lives in the last band of the panel: its text must fit in the
	// panel's width once ellipsized, which is what keeps it off the chips.
	font, _ := getCachedFont(config, "small")
	if font == nil {
		t.Skip("no font available")
	}
	line := ellipsize(font, browserCookieSitesLine(browserCookieSites()), getElementWidth(elem, 0)-24)
	if w, _, _ := font.SizeUTF8(line); int32(w) > getElementWidth(elem, 0)-24 {
		t.Fatalf("the strip line %q is %dpx wide in a %dpx panel", line, w, getElementWidth(elem, 0))
	}
}
