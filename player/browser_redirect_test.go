package main

// browser_redirect_test.go — the browser's redirects, end to end.
//
// Two kinds reach a reader: the HTTP hops the client follows, and the
// <meta http-equiv="refresh"> a page ships in its own markup. The second used to
// be dropped — the stub arrives with a 200 and a body, so nothing follows it,
// and the reader was left on a page whose whole content was "this page has
// moved", with the link that would have continued stripped as page furniture.
// These tests pin that a real redirect is followed, that a page merely asking to
// reload itself is not, that a loop ends, and that the address bar ends up
// naming the page actually on screen.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// redirectServer answers a meta-refresh stub on /old with the real page on /new,
// and counts how many page requests it saw.
func redirectServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Count pages, not the favicon probe the loader also makes: only a
		// request for a page says anything about how many hops were walked.
		if r.URL.Path == "/old" || r.URL.Path == "/new" {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/old":
			io.WriteString(w, `<html><head><title>Moved</title>`+
				`<meta http-equiv="refresh" content="0; url=/new"></head>`+
				`<body><p>this page has moved</p></body></html>`)
		case "/new":
			io.WriteString(w, `<html><head><title>New home</title></head>`+
				`<body><p>the real page</p><a href="next">next</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The reader lands on the target, its links resolve against the target, and the
// load reports where it ended rather than where it started.
func TestBrowserFollowsMetaRefresh(t *testing.T) {
	srv, hits := redirectServer(t)

	page := browseWebPageResult(srv.URL + "/old")

	if strings.Contains(page.Text, "this page has moved") {
		t.Fatalf("the reader was left on the redirect stub:\n%s", page.Text)
	}
	if !strings.Contains(page.Text, "the real page") {
		t.Fatalf("the redirect target was not read:\n%s", page.Text)
	}
	if want := srv.URL + "/new"; page.FinalURL != want {
		t.Fatalf("FinalURL = %q, want %q", page.FinalURL, want)
	}
	if want := srv.URL + "/next"; len(page.Links) != 1 || page.Links[0].URL != want {
		t.Fatalf("links = %+v, want only %q", page.Links, want)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("the server saw %d page requests, want the stub and its target", got)
	}
}

// A page that asks to be reloaded every half hour, names a non-web scheme, or
// names nothing at all is not a redirect: the reader stays where it is.
func TestBrowserMetaRefreshIgnoresAutoRefreshAndBadTargets(t *testing.T) {
	for _, content := range []string{
		"30; url=/later",             // a slow auto-refresh, not a redirect
		"0",                          // refresh with no target
		"0; url=javascript:alert(1)", // never a page this browser can draw
		"0; url=ftp://example.com/x", // not a web scheme
		"url=/next",                  // no delay means no such form
		"",                           // empty content
	} {
		page := `<html><head><meta http-equiv="refresh" content="` + content + `"></head><body>x</body></html>`
		base, _ := url.Parse("https://example.com/a")
		if got := browserMetaRefreshTarget(page, base); got != "" {
			t.Errorf("content %q produced a redirect to %q, want none", content, got)
		}
	}
}

// The refresh parser covers what pages actually ship: quoted, unquoted, spaced
// and upper-cased "URL=" forms, resolved against the page it came from.
func TestBrowserMetaRefreshResolvesRelativeAndQuotedTargets(t *testing.T) {
	base, _ := url.Parse("https://example.com/docs/a/index.html")
	tests := []struct {
		content string
		want    string
	}{
		{"0; url=../b", "https://example.com/docs/b"},
		{"0;URL='/c'", "https://example.com/c"},
		{"0.5; url=/quick", "https://example.com/quick"},
		{"0; url='d'", "https://example.com/docs/a/d"},
		{"0; url=https://other.example/x", "https://other.example/x"},
	}
	for _, tt := range tests {
		page := `<meta http-equiv="refresh" content="` + tt.content + `">`
		if got := browserMetaRefreshTarget(page, base); got != tt.want {
			t.Errorf("content %q resolved to %q, want %q", tt.content, got, tt.want)
		}
	}
}

// Two pages that point at each other must end, not keep the reader loading.
func TestBrowserMetaRefreshLoopIsBounded(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/a" && r.URL.Path != "/b" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		target := "/b"
		if r.URL.Path == "/b" {
			target = "/a"
		}
		io.WriteString(w, `<html><head><meta http-equiv="refresh" content="0; url=`+target+`"></head>`+
			`<body><p>hop</p></body></html>`)
	}))
	defer srv.Close()

	page := browseWebPageResult(srv.URL + "/a")

	if page.FinalURL == "" {
		t.Fatal("a redirect loop produced no page at all")
	}
	if got, limit := hits.Load(), int32(textBrowserMetaRedirectLimit+1); got > limit {
		t.Fatalf("the loop made %d requests, want at most %d", got, limit)
	}
}

// A page that only refreshes itself is shown once, not re-fetched.
func TestBrowserMetaRefreshToItselfIsLoadedOnce(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/self" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><head><meta http-equiv="refresh" content="0"></head>`+
			`<body><p>live dashboard</p></body></html>`)
	}))
	defer srv.Close()

	page := browseWebPageResult(srv.URL + "/self")
	if !strings.Contains(page.Text, "live dashboard") {
		t.Fatalf("the page was not read:\n%s", page.Text)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("the server saw %d requests, want 1", got)
	}
}

// The address bar and history name the page the redirect landed on, once, and
// never while the reader is typing an address of their own.
func TestBrowserLandedURLMovesTheAddressBar(t *testing.T) {
	resetBrowserStateForTest()
	t.Cleanup(resetBrowserStateForTest)

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	const from, to = "https://example.com/old", "https://example.com/new"
	config.Variables.Custom[textBrowserURLVariable] = from
	browserRecordURL(from)
	browserRememberLandedURL(from, to)

	prevEditing := textBrowserAddressEditing
	t.Cleanup(func() { textBrowserAddressEditing = prevEditing })

	textBrowserAddressEditing = true
	browserSyncLandedURL(config)
	if got := textBrowserAddress(config); got != from {
		t.Fatalf("the address was rewritten while it was being edited: %q", got)
	}

	textBrowserAddressEditing = false
	browserSyncLandedURL(config)
	if got := textBrowserAddress(config); got != to {
		t.Fatalf("address = %q, want the redirect target", got)
	}
	if got, ok := browserHistoryStep(0); !ok || got != to {
		t.Fatalf("history entry = %q, want the redirect target", got)
	}
}

// The whole path: a load whose fetch lands somewhere else leaves the address in
// need of correction, and the render pass applies it.
func TestBrowserLoadRecordsTheLandedURL(t *testing.T) {
	resetBrowserStateForTest()
	t.Cleanup(resetBrowserStateForTest)

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	variable := "browser_landed"
	restoreFetch := setBrowserPageFetcherForTest(func(rawURL string) browserPage {
		return browserPage{
			Text:     "Title: landed\n\nthe redirected page",
			FinalURL: "https://example.com/landed",
		}
	})
	t.Cleanup(func() {
		waitForBrowserFetches()
		restoreFetch()
	})

	elem := Element{Type: "textbrowser", Text: "web", Variable: variable}
	loadTextBrowserURL(config, elem, "https://example.com/start")
	waitBrowserVariable(t, config, variable, func(s string) bool {
		return strings.Contains(s, "the redirected page")
	})
	// The correction is the render pass's job, not the fetch goroutine's.
	if got := textBrowserAddress(config); got != "https://example.com/start" {
		t.Fatalf("the fetch rewrote the address itself: %q", got)
	}
	browserSyncLandedURL(config)
	if got := textBrowserAddress(config); got != "https://example.com/landed" {
		t.Fatalf("address = %q, want the landed URL", got)
	}
}

// A correction left over from a load the reader navigated away from is dropped
// rather than applied to the page they moved to.
func TestBrowserStaleLandedURLIsIgnored(t *testing.T) {
	resetBrowserStateForTest()
	t.Cleanup(resetBrowserStateForTest)

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{textBrowserURLVariable: "https://example.com/elsewhere"}
	browserRememberLandedURL("https://example.com/old", "https://example.com/new")

	browserSyncLandedURL(config)
	if got := textBrowserAddress(config); got != "https://example.com/elsewhere" {
		t.Fatalf("a stale correction moved the address to %q", got)
	}
	if from, target := browserTakeLandedURL(); from != "" || target != "" {
		t.Fatalf("the stale correction is still pending: %q -> %q", from, target)
	}
}

// A slow network must not be able to hold the reader on a redirect chain: the
// hops share the load's one deadline rather than each starting a fresh one.
func TestBrowserRedirectHopsShareTheDeadline(t *testing.T) {
	if textBrowserFetchTimeout <= 0 {
		t.Fatal("the fetch timeout is not set")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := fetchTextBrowserOnce(ctx, browserHTTPClient(time.Second), "https://example.com/"); err == nil {
		t.Fatal("an expired deadline still produced a document")
	} else if !strings.Contains(err.Error(), "Timed out") {
		t.Fatalf("error = %v, want the timeout wording", err)
	}
}
