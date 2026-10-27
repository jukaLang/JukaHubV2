package main

// The reader's cookie jar, exercised the way a site uses it: a redirect hands
// out a session cookie, the page that follows only renders with it, and the
// stylesheet and image the page references are only served to that session.
//
// These use the real fetchers over httptest servers, so a change that drops the
// jar from any one request (page, stylesheet, image, script) fails here.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// cookieServer is one site: /start redirects to /page with a session cookie,
// and everything else refuses to answer without it.
type cookieServer struct {
	pageHits  int
	sheetHits int
	imageHits int
	scriptHit int
	sawCookie map[string]bool
	mu        sync.Mutex
}

func (c *cookieServer) record(what string, r *http.Request) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sawCookie == nil {
		c.sawCookie = map[string]bool{}
	}
	cookie, err := r.Cookie("session")
	c.sawCookie[what] = err == nil && cookie.Value == "abc123"
	return c.sawCookie[what]
}

func (c *cookieServer) counts() (page, sheet, image, script int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pageHits, c.sheetHits, c.imageHits, c.scriptHit
}

func newCookieServer(t *testing.T) (*cookieServer, *httptest.Server) {
	t.Helper()
	state := &cookieServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/"})
		http.Redirect(w, r, "/page", http.StatusFound)
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.pageHits++
		state.mu.Unlock()
		if !state.record("page", r) {
			http.Error(w, "no session", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// 1x1 PNG, so the image pass has something it can really decode.
		w.Write([]byte(`<html><head><title>Members page</title>` +
			`<link rel="stylesheet" href="/style.css">` +
			`</head><body>` +
			`<h1>Welcome back</h1><p>The cookie survived the redirect.</p>` +
			`<img src="/badge.png" alt="badge">` +
			`</body></html>`))
	})
	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.sheetHits++
		state.mu.Unlock()
		if !state.record("style", r) {
			http.Error(w, "no session", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/css")
		w.Write([]byte(`h1 { color: #ff0000; }`))
	})
	mux.HandleFunc("/badge.png", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.imageHits++
		state.mu.Unlock()
		if !state.record("image", r) {
			http.Error(w, "no session", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(pixelPNG())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return state, srv
}

// pixelPNG is the smallest valid image the decoder accepts.
func pixelPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
		0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
		0x0d, 0x0a, 0x2d, 0xb4,
		0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
}

// The whole point: a cookie set on a redirect is sent on the hop that follows,
// and the page — including its stylesheet and image — renders.
func TestBrowserCookieSurvivesARedirectAndReachesSubresources(t *testing.T) {
	resetBrowserStateForTest()
	state, srv := newCookieServer(t)

	page := browseWebPageResult(srv.URL + "/start")
	if !strings.Contains(page.Text, "Welcome back") {
		t.Fatalf("page did not render through the cookie:\n%s", page.Text)
	}
	if page.FinalURL != srv.URL+"/page" {
		t.Errorf("final URL = %q, want the redirect target", page.FinalURL)
	}

	state.mu.Lock()
	saw := map[string]bool{}
	for k, v := range state.sawCookie {
		saw[k] = v
	}
	state.mu.Unlock()
	for _, what := range []string{"page", "style", "image"} {
		if !saw[what] {
			t.Errorf("the %s request went out without the session cookie", what)
		}
	}
	pageHits, sheetHits, imageHits, _ := state.counts()
	if pageHits != 1 || sheetHits != 1 || imageHits != 1 {
		t.Errorf("requests: page=%d sheet=%d image=%d, want one each", pageHits, sheetHits, imageHits)
	}
}

// Cookies last for the session: a page loaded later in the same run sends them
// without visiting the redirect again.
func TestBrowserCookiesPersistAcrossPageLoads(t *testing.T) {
	resetBrowserStateForTest()
	state, srv := newCookieServer(t)

	if page := browseWebPageResult(srv.URL + "/start"); !strings.Contains(page.Text, "Welcome back") {
		t.Fatalf("first load failed:\n%s", page.Text)
	}
	second := browseWebPageResult(srv.URL + "/page")
	if !strings.Contains(second.Text, "Welcome back") {
		t.Fatalf("second load lost the session:\n%s", second.Text)
	}
	if pageHits, _, _, _ := state.counts(); pageHits != 2 {
		t.Errorf("page requests = %d, want 2", pageHits)
	}
}

// A site that refuses without the cookie must still be refused when no cookie
// was ever handed out, so the test above is not passing on a page that does not
// check.
func TestBrowserWithoutACookieIsRefused(t *testing.T) {
	resetBrowserStateForTest()
	_, srv := newCookieServer(t)

	page := browseWebPageResult(srv.URL + "/page")
	if strings.Contains(page.Text, "Welcome back") {
		t.Fatalf("a page that requires a session rendered without one:\n%s", page.Text)
	}
	if !strings.Contains(page.Text, "403") && !strings.Contains(page.Text, "Could not load") {
		t.Errorf("refusal not reported to the reader:\n%s", page.Text)
	}
}

// The page's own scripts are fetched through the jar too: a site that gates its
// script on the session would otherwise run nothing.
func TestBrowserScriptFetchesUseTheJar(t *testing.T) {
	resetBrowserStateForTest()
	var mu sync.Mutex
	var sawCookie bool
	var hits int

	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body><p>Scripts below.</p>` +
			`<script src="/app.js"></script></body></html>`))
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		if c, err := r.Cookie("session"); err == nil && c.Value == "abc123" {
			sawCookie = true
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`document.write("<p>script text</p>");`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	browseWebPageResult(srv.URL + "/start")

	mu.Lock()
	defer mu.Unlock()
	if hits == 0 {
		// The page serves a <script src>; if the reader never requests it, the
		// cookie-on-subresource behaviour below cannot happen and this test
		// would pass without checking it.
		t.Fatal("the reader did not request the page's <script src>")
	}
	if !sawCookie {
		t.Error("the script request went out without the session cookie")
	}
}

// The jar itself: a cookie is handed back for its host and path only, and the
// reset helper really empties it.
func TestBrowserCookieJarScopeAndReset(t *testing.T) {
	resetBrowserStateForTest()
	jar := browserCookies()
	if jar == nil {
		t.Fatal("the browser has no cookie jar")
	}

	siteURL := mustParseURL(t, "http://site-a.example/readme")
	jar.SetCookies(siteURL, []*http.Cookie{{Name: "k", Value: "v", Path: "/"}})
	if got := jar.Cookies(siteURL); len(got) != 1 || got[0].Value != "v" {
		t.Fatalf("jar returned %v, want the cookie that was set", got)
	}

	// Cookies are scoped by host, never by port, so a cookie for one site is
	// not offered to another even on the same machine.
	for _, other := range []string{"http://site-b.example/readme", "http://sub.site-a.example/readme", "https://site-a.example.evil.example/readme"} {
		if got := jar.Cookies(mustParseURL(t, other)); len(got) != 0 {
			t.Errorf("a cookie was offered to %s: %v", other, got)
		}
	}
	// A different path on the same host still gets it: the path attribute is
	// "/", which covers everything below it.
	if got := jar.Cookies(mustParseURL(t, "http://site-a.example/other")); len(got) != 1 {
		t.Errorf("a sibling path lost the cookie: %v", got)
	}

	browserResetCookies()
	if got := browserCookies().Cookies(siteURL); len(got) != 0 {
		t.Errorf("reset left %d cookie(s) behind", len(got))
	}
}
