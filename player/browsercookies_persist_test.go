package main

// browsercookies_persist_test.go — the copy of the jar that lives on the card.
//
// The promise is narrow and testable: a cookie a page set is still there after
// the process that set it is gone, a cookie that expired in the meantime is
// not, and one press empties both the memory and the file.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// useTempBrowserCookiePath points the jar at a temporary file for one test and
// puts the suite's own path back afterwards.
func useTempBrowserCookiePath(t *testing.T) string {
	t.Helper()
	prev := browserCookiesPath
	path := filepath.Join(t.TempDir(), "browser-cookies.json")
	browserCookiesPath = func() string { return path }
	browserResetCookies()
	t.Cleanup(func() {
		browserCookiesPath = prev
		browserResetCookies()
	})
	return path
}

// simulateBrowserRelaunch throws the in-memory jar away without touching the
// file, which is exactly what quitting and starting the app does.
func simulateBrowserRelaunch() {
	browserJar.mu.Lock()
	browserJar.jar = nil
	browserJar.dead = false
	browserJar.mu.Unlock()
	browserForgetConfirm.armedAt = time.Time{}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func savedCookieFile(t *testing.T, path string) browserCookieFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return browserCookieFile{}
	}
	var file browserCookieFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
	return file
}

func savedCookieNames(t *testing.T, path string) []string {
	t.Helper()
	file := savedCookieFile(t, path)
	var names []string
	for _, rec := range file.Cookies {
		names = append(names, rec.Name)
	}
	return names
}

// The whole point: a session cookie set by a page is handed back on a request
// issued by the next run of the app.
func TestBrowserCookieSurvivesARelaunch(t *testing.T) {
	path := useTempBrowserCookiePath(t)

	jar := browserCookies()
	site := mustURL(t, "https://example.com/login")
	jar.SetCookies(site, []*http.Cookie{
		{Name: "session", Value: "abc123", Path: "/", HttpOnly: true, Secure: true},
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("setting a cookie did not write %s: %v", path, err)
	}
	// The file holds a session token, so only the owner may read it. FAT cards
	// have no mode bits, so this is only meaningful where the host honours them.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != browserCookieFileMode {
			t.Errorf("cookie file mode = %o, want %o", perm, browserCookieFileMode)
		}
	}

	// The document shape is part of the promise: a version, one timestamp, and
	// the attributes needed to rebuild the cookie.
	file := savedCookieFile(t, path)
	if file.SchemaVersion != browserCookieSchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", file.SchemaVersion, browserCookieSchemaVersion)
	}
	if _, err := time.Parse(time.RFC3339, file.SavedAt); err != nil {
		t.Errorf("savedAt %q is not RFC3339: %v", file.SavedAt, err)
	}
	if len(file.Cookies) != 1 {
		t.Fatalf("saved %d cookies, want 1", len(file.Cookies))
	}
	rec := file.Cookies[0]
	if rec.Host != "example.com" || rec.Name != "session" || rec.Value != "abc123" || rec.Path != "/" || !rec.Secure || !rec.HTTPOnly {
		t.Errorf("saved record lost an attribute: %+v", rec)
	}
	if rec.Expires != "" {
		t.Errorf("a session cookie was given an expiry: %q", rec.Expires)
	}

	simulateBrowserRelaunch()

	// A secure cookie is only offered over https, and the restored record must
	// keep that: rebuilding it as http would silently drop the session.
	revived := browserCookies()
	if got := revived.Cookies(mustURL(t, "https://example.com/account")); len(got) != 1 || got[0].Value != "abc123" {
		t.Fatalf("after relaunch the jar returned %v, want the saved session", got)
	}
	if got := revived.Cookies(mustURL(t, "http://example.com/account")); len(got) != 0 {
		t.Errorf("a secure cookie was sent over http after restore: %v", got)
	}
	if got := revived.Cookies(mustURL(t, "https://other.example/account")); len(got) != 0 {
		t.Errorf("the restored cookie leaked to another host: %v", got)
	}
}

// The end-to-end version of the promise, through the browser's own fetchers and
// not just the jar: sign in, quit, start again, and the page that requires the
// session still renders.
func TestBrowserSessionSurvivesARelaunch(t *testing.T) {
	useTempBrowserCookiePath(t)
	resetBrowserStateForTest()
	state, srv := newCookieServer(t)

	if page := browseWebPageResult(srv.URL + "/start"); !strings.Contains(page.Text, "Welcome back") {
		t.Fatalf("the sign-in load failed:\n%s", page.Text)
	}

	// Quit the app: the page cache and the in-memory jar go, the file stays.
	resetBrowserStateForTest()
	simulateBrowserRelaunch()
	state.mu.Lock()
	state.sawCookie["page"] = false
	state.mu.Unlock()

	page := browseWebPageResult(srv.URL + "/page")
	if !strings.Contains(page.Text, "Welcome back") {
		t.Fatalf("after relaunch the gated page no longer rendered:\n%s", page.Text)
	}
	state.mu.Lock()
	saw := state.sawCookie["page"]
	state.mu.Unlock()
	if !saw {
		t.Fatal("the page request went out without the restored session cookie")
	}
}

// A cookie that expired while the app was closed is dropped on restore instead
// of being carried forward, and an expired one is never written in the first
// place.
func TestBrowserCookieExpiryIsRespected(t *testing.T) {
	path := useTempBrowserCookiePath(t)

	jar := browserCookies()
	site := mustURL(t, "https://example.com/")
	jar.SetCookies(site, []*http.Cookie{
		{Name: "soon", Value: "1", Path: "/", Expires: time.Now().Add(200 * time.Millisecond)},
		{Name: "stale", Value: "1", Path: "/", Expires: time.Now().Add(-time.Hour)},
	})
	if names := savedCookieNames(t, path); len(names) != 1 || names[0] != "soon" {
		t.Fatalf("saved %v, want only the unexpired cookie", names)
	}

	time.Sleep(300 * time.Millisecond)
	simulateBrowserRelaunch()
	if got := browserCookies().Cookies(site); len(got) != 0 {
		t.Fatalf("an expired cookie came back after relaunch: %v", got)
	}
}

// A page that signs out (Set-Cookie with a past expiry, or MaxAge < 0) must not
// leave the dead cookie in the file — otherwise the next relaunch signs the
// user back in.
func TestBrowserCookieDeleteRemovesItFromTheFile(t *testing.T) {
	path := useTempBrowserCookiePath(t)

	jar := browserCookies()
	site := mustURL(t, "https://example.com/")
	jar.SetCookies(site, []*http.Cookie{{Name: "session", Value: "abc", Path: "/"}})
	if len(savedCookieNames(t, path)) != 1 {
		t.Fatalf("the session cookie was not saved")
	}

	jar.SetCookies(site, []*http.Cookie{{Name: "session", Value: "", Path: "/", MaxAge: -1}})
	if got := jar.Cookies(site); len(got) != 0 {
		t.Fatalf("the jar still holds a deleted cookie: %v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleting the last cookie left %s behind (err=%v)", path, err)
	}
}

// Forgetting is the user's escape hatch: it empties the jar, removes the file,
// and the next relaunch starts from nothing.
func TestBrowserForgetCookiesClearsMemoryAndCard(t *testing.T) {
	path := useTempBrowserCookiePath(t)

	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{
		{Name: "session", Value: "abc", Path: "/"},
		{Name: "consent", Value: "yes", Path: "/"},
	})
	if browserCookiesCount() != 2 {
		t.Fatalf("jar holds %d cookies, want 2", browserCookiesCount())
	}

	dropped, err := browserForgetCookies()
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if dropped != 2 {
		t.Errorf("forget reported %d cookies, want 2", dropped)
	}
	if got := browserCookiesCount(); got != 0 {
		t.Errorf("jar still holds %d cookies after forget", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the cookie file survived forget (err=%v)", err)
	}

	simulateBrowserRelaunch()
	if got := browserCookies().Cookies(mustURL(t, "https://example.com/")); len(got) != 0 {
		t.Fatalf("a forgotten cookie came back after relaunch: %v", got)
	}
}

// A browser opened for the first time has no jar yet, but the session from the
// last run is on the card. The Forget chip must see it — "No cookies are
// stored" while a file sits there would be a lie it then acts on.
func TestForgetCountsTheSavedSessionBeforeTheFirstPage(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	browserCookies().SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "session", Value: "abc", Path: "/"}})

	simulateBrowserRelaunch() // the app restarts; nothing has been fetched yet
	if got := browserCookiesCount(); got != 1 {
		t.Fatalf("count = %d before the first page load, want the saved cookie", got)
	}
	dropped, err := browserForgetCookies()
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if dropped != 1 {
		t.Errorf("reported %d cookies, want 1", dropped)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the saved session survived Forget (err=%v)", err)
	}
}

// A page load can still be on the wire when the user forgets the cookies. The
// stale jar must not write the session back a moment after it was deleted.
func TestBrowserForgetStopsAnInFlightJarFromSaving(t *testing.T) {
	path := useTempBrowserCookiePath(t)

	// This is the jar a request in flight is holding: a client built before the
	// Forget, with its own reference.
	inFlight := browserCookies()
	inFlight.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "session", Value: "abc", Path: "/"}})

	if _, err := browserForgetCookies(); err != nil {
		t.Fatalf("forget: %v", err)
	}
	inFlight.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "late", Value: "x", Path: "/"}})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a request that finished after Forget recreated %s (err=%v)", path, err)
	}
	if got := browserCookiesCount(); got != 0 {
		t.Errorf("the fresh jar holds %d cookies after Forget", got)
	}
}

// The Forget chip asks twice: one press only arms it, so a stray press in the
// chip row cannot sign the user out.
func TestBrowserForgetChipAsksTwice(t *testing.T) {
	useTempBrowserCookiePath(t)
	browserForgetConfirm.armedAt = time.Time{}
	t.Cleanup(func() { browserForgetConfirm.armedAt = time.Time{} })

	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "session", Value: "abc", Path: "/"}})

	config := &Config{}
	ensureBrowserScene(config)
	var forget bool
	for _, elem := range config.Scenes[0].Elements {
		if elem.Trigger == "browser_forget_cookies" {
			forget = true
		}
	}
	if !forget {
		t.Fatal("the Browser scene has no Forget chip")
	}

	if browserForgetCookiesAction(config) {
		t.Fatal("the first press forgot the session instead of arming the chip")
	}
	if browserCookiesCount() != 1 {
		t.Fatalf("the first press changed the jar (%d cookies left)", browserCookiesCount())
	}
	if !browserForgetCookiesAction(config) {
		t.Fatal("the second press did not forget the cookies")
	}
	if browserCookiesCount() != 0 {
		t.Fatalf("the second press left %d cookies", browserCookiesCount())
	}
}

// An armed chip disarms itself after the window, so a press minutes later is a
// first press again.
func TestBrowserForgetChipDisarmsAfterTheWindow(t *testing.T) {
	useTempBrowserCookiePath(t)
	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "session", Value: "abc", Path: "/"}})

	// Armed long ago: the next press must ask again rather than forget.
	browserForgetConfirm.armedAt = time.Now().Add(-2 * browserForgetConfirmWindow)
	t.Cleanup(func() { browserForgetConfirm.armedAt = time.Time{} })

	if browserForgetCookiesAction(&Config{}) {
		t.Fatal("a stale arm forgot the cookies without asking again")
	}
	if browserCookiesCount() != 1 {
		t.Fatalf("the jar was emptied by a stale arm")
	}
}

// A damaged file costs the session and nothing else: no error shown, no panic,
// and the next cookie still saves over it.
func TestBrowserCookieFileGarbageIsIgnored(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	simulateBrowserRelaunch()
	jar := browserCookies()
	if got := jar.Cookies(mustURL(t, "https://example.com/")); len(got) != 0 {
		t.Fatalf("a broken file produced cookies: %v", got)
	}
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "session", Value: "abc", Path: "/"}})
	if names := savedCookieNames(t, path); len(names) != 1 {
		t.Fatalf("the jar did not recover from the broken file, saved %v", names)
	}
}

// Two sites can use the same cookie name; the file has to keep them apart.
func TestBrowserCookieFileKeepsSitesApart(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://a.example/"), []*http.Cookie{{Name: "sid", Value: "a", Path: "/"}})
	jar.SetCookies(mustURL(t, "https://b.example/"), []*http.Cookie{{Name: "sid", Value: "b", Path: "/"}})

	simulateBrowserRelaunch()
	revived := browserCookies()
	a := revived.Cookies(mustURL(t, "https://a.example/"))
	b := revived.Cookies(mustURL(t, "https://b.example/"))
	if len(a) != 1 || a[0].Value != "a" {
		t.Errorf("site A got %v", a)
	}
	if len(b) != 1 || b[0].Value != "b" {
		t.Errorf("site B got %v", b)
	}
	if names := savedCookieNames(t, path); len(names) != 2 {
		t.Errorf("saved %v, want both sites' cookies", names)
	}
}

// Forgetting one site keeps the other sessions but must take the backup with
// it: the .bak still holds the forgotten site's cookies, and a damaged read
// would hand them back.
func TestBrowserForgettingOneSiteDropsTheBackup(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://a.example/"), []*http.Cookie{{Name: "sid", Value: "a", Path: "/"}})
	jar.SetCookies(mustURL(t, "https://b.example/"), []*http.Cookie{{Name: "sid", Value: "b", Path: "/"}})
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("no backup was written to delete: %v", err)
	}

	dropped, err := browserForgetSiteCookies("a.example")
	if err != nil || dropped == 0 {
		t.Fatalf("forget site: dropped=%d err=%v", dropped, err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("the backup survived a per-site forget (err=%v)", err)
	}
	if got := browserCookies().Cookies(mustURL(t, "https://a.example/")); len(got) != 0 {
		t.Errorf("the forgotten site still has cookies: %v", got)
	}
	if got := browserCookies().Cookies(mustURL(t, "https://b.example/")); len(got) != 1 {
		t.Errorf("the other site lost its session: %v", got)
	}
}

// A truncated file costs the newest cookie, not the session. The durable save
// keeps the previous version as .bak, so a card pulled mid-write — or a bad
// edit — comes back to the last good jar instead of signing the user out of
// every site. This pins both halves: that it recovers, and that it is the
// previous version that comes back rather than the half-written one.
func TestBrowserCookieSessionComesBackFromTheBackup(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "keep", Value: "one", Path: "/"}})
	// The second save makes the first version the backup.
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "newer", Value: "two", Path: "/"}})
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("the second save left no backup: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"cookies":[{"host":"exa`), 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	simulateBrowserRelaunch()
	revived := browserCookies()
	names := map[string]bool{}
	for _, c := range revived.Cookies(mustURL(t, "https://example.com/")) {
		names[c.Name] = true
	}
	if !names["keep"] {
		t.Fatalf("the backup's cookie did not come back: %v", names)
	}
	if names["newer"] {
		t.Errorf("the half-written version was restored: %v", names)
	}
}

// Forget is a promise about the card, not just about memory: the backup the
// durable save leaves beside the file has to go too, or the session it holds is
// still on the disk the user was told was cleared.
func TestBrowserForgetTakesTheBackupWithIt(t *testing.T) {
	path := useTempBrowserCookiePath(t)
	jar := browserCookies()
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "a", Value: "1", Path: "/"}})
	jar.SetCookies(mustURL(t, "https://example.com/"), []*http.Cookie{{Name: "b", Value: "2", Path: "/"}})
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("no backup was written to delete: %v", err)
	}

	if _, err := browserForgetCookies(); err != nil {
		t.Fatalf("forget: %v", err)
	}
	for _, p := range []string{path, path + ".bak"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived a Forget (err=%v)", p, err)
		}
	}
}

// The record's path attribute follows RFC 6265: an invalid or missing Path
// falls back to the request's directory, so a restored cookie still matches the
// same URLs it did before.
func TestBrowserCookiePathDefaults(t *testing.T) {
	cases := []struct{ attr, request, want string }{
		{"/", "/deep/page", "/"},
		{"/deep", "/deep/page", "/deep"},
		{"", "/deep/page", "/deep"},
		{"", "/page", "/"},
		{"", "/", "/"},
		{"relative", "/deep/page", "/deep"},
	}
	for _, c := range cases {
		if got := browserCookiePath(c.attr, c.request); got != c.want {
			t.Errorf("browserCookiePath(%q, %q) = %q, want %q", c.attr, c.request, got, c.want)
		}
	}
}
