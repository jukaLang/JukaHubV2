package main

// browsercookies.go — the reader's cookie jar.
//
// A page load is not one request: the reader fetches the HTML, then its
// stylesheets, images and scripts, each on its own. Sites that hand out a
// session cookie only work when all of those requests share one cookie store —
// a consent answer, an age gate, or a redirect that only sticks once the cookie
// is sent back. They also need the store consulted between redirect hops, which
// net/http does by itself when the client has a Jar: every hop's Set-Cookie is
// stored and the next hop's request is built from the jar.
//
// The jar is deliberately in memory only. The reader cannot submit a form, so
// it cannot log in, and a cookie file on the SD card would outlive whatever it
// was for and turn into a privacy surface for no benefit. Cookies live exactly
// as long as the app does; a relaunch starts clean.
//
// Only the browser's own requests use the jar. Feeds, update checks, the ticker
// and the podcast fetchers keep their own clients, so a cookie a page set can
// never ride along on an unrelated request.

import (
	"net/http"
	"net/http/cookiejar"
	"sync"
	"time"
)

// browserJar is the process-lifetime cookie store. The zero value is usable:
// the first request builds the jar, and a build failure (no public suffix list
// available) simply leaves cookies off rather than failing the page.
var browserJar = struct {
	mu   sync.Mutex
	jar  http.CookieJar
	dead bool
}{}

// browserCookies returns the shared jar, creating it on first use.
func browserCookies() http.CookieJar {
	browserJar.mu.Lock()
	defer browserJar.mu.Unlock()
	if browserJar.jar != nil || browserJar.dead {
		return browserJar.jar
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		// Without a jar there are no cookies, but a page still loads.
		browserJar.dead = true
		return nil
	}
	browserJar.jar = jar
	return jar
}

// browserResetCookies drops every cookie, which is how the browser tests start
// from a clean session. Nothing in the app calls it: leaving the browser or
// emptying the page cache must not sign the user out of a page that handed out
// a session.
func browserResetCookies() {
	browserJar.mu.Lock()
	if jar, err := cookiejar.New(nil); err == nil {
		browserJar.jar = jar
		browserJar.dead = false
	} else {
		browserJar.jar = nil
		browserJar.dead = true
	}
	browserJar.mu.Unlock()
}

// browserHTTPClient builds the client every browser request goes through, so a
// cookie set by one request is visible to the next — including across a
// redirect, where the client stores each hop's Set-Cookie before following it.
//
// The caller keeps its own timeout and redirect policy; only the jar is shared.
func browserHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Jar: browserCookies()}
}
