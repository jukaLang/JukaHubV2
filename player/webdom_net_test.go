package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The real-request tests below run against httptest servers, which listen on
// 127.0.0.1: their pages are loopback pages, whose scripts may reach anything.
// The public-page guard is tested through its decision functions and through
// requests it refuses before anything is dialed.

func webJSNetTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// webJSNetTestCookies gives a test an empty jar and leaves an empty one
// behind. TestMain already points the jar's file at a temporary directory.
func webJSNetTestCookies(t *testing.T) {
	t.Helper()
	browserResetCookies()
	t.Cleanup(browserResetCookies)
}

// webJSNetTestPairs flattens a header list for comparison.
func webJSNetTestPairs(pairs [][2]string) string {
	parts := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		parts = append(parts, kv[0]+"="+kv[1])
	}
	return strings.Join(parts, "|")
}

func webJSNetTestHeader(resp webJSResponse, name string) (string, bool) {
	for _, kv := range resp.Header {
		if kv[0] == name {
			return kv[1], true
		}
	}
	return "", false
}

// webJSNetTestJar is the cookies the shared jar would send to raw, by name.
func webJSNetTestJar(t *testing.T, raw string) map[string]string {
	t.Helper()
	jar := browserCookies()
	if jar == nil {
		t.Fatal("no cookie jar")
	}
	out := map[string]string{}
	for _, c := range jar.Cookies(webJSNetTestURL(t, raw)) {
		out[c.Name] = c.Value
	}
	return out
}

func TestWebJSNetPlaceOf(t *testing.T) {
	cases := []struct {
		ip   string
		want webJSNetPlace
	}{
		{"8.8.8.8", webJSNetPublic},
		{"93.184.216.34", webJSNetPublic},
		{"2606:4700:4700::1111", webJSNetPublic},
		{"172.32.0.1", webJSNetPublic},
		{"100.128.0.1", webJSNetPublic},
		{"::ffff:8.8.8.8", webJSNetPublic},
		{"64:ff9b::808:808", webJSNetPublic},
		{"127.0.0.1", webJSNetLoopback},
		{"127.8.9.10", webJSNetLoopback},
		{"::1", webJSNetLoopback},
		{"0.0.0.0", webJSNetLoopback},
		{"0.1.2.3", webJSNetLoopback},
		{"::", webJSNetLoopback},
		{"::ffff:127.0.0.1", webJSNetLoopback},
		{"64:ff9b::7f00:1", webJSNetLoopback},
		{"10.1.2.3", webJSNetPrivate},
		{"172.16.0.1", webJSNetPrivate},
		{"172.31.255.255", webJSNetPrivate},
		{"192.168.1.1", webJSNetPrivate},
		{"169.254.169.254", webJSNetPrivate},
		{"100.64.0.1", webJSNetPrivate},
		{"100.127.255.255", webJSNetPrivate},
		{"fc00::1", webJSNetPrivate},
		{"fd12:3456::1", webJSNetPrivate},
		{"fe80::1", webJSNetPrivate},
		{"fe80::1%eth0", webJSNetPrivate},
		{"::ffff:192.168.0.1", webJSNetPrivate},
		{"64:ff9b::c0a8:101", webJSNetPrivate},
		{"2002:c0a8:101::1", webJSNetPrivate},
		{"224.0.0.251", webJSNetPrivate},
		{"ff02::1", webJSNetPrivate},
		{"255.255.255.255", webJSNetPrivate},
	}
	for _, tc := range cases {
		ip, err := netip.ParseAddr(tc.ip)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.ip, err)
		}
		if got := webJSNetPlaceOf(ip); got != tc.want {
			t.Errorf("webJSNetPlaceOf(%s) = %d, want %d", tc.ip, got, tc.want)
		}
	}
	if got := webJSNetPlaceOf(netip.Addr{}); got != webJSNetLoopback {
		t.Errorf("the zero address = %d, want %d (refused unless anything goes)", got, webJSNetLoopback)
	}
}

func TestWebJSNetAllows(t *testing.T) {
	cases := []struct {
		guard webJSNetGuard
		place webJSNetPlace
		want  bool
	}{
		{webJSNetGuardPublic, webJSNetPublic, true},
		{webJSNetGuardPublic, webJSNetPrivate, false},
		{webJSNetGuardPublic, webJSNetLoopback, false},
		{webJSNetGuardLAN, webJSNetPublic, true},
		{webJSNetGuardLAN, webJSNetPrivate, true},
		{webJSNetGuardLAN, webJSNetLoopback, false},
		{webJSNetGuardLocal, webJSNetPublic, true},
		{webJSNetGuardLocal, webJSNetPrivate, true},
		{webJSNetGuardLocal, webJSNetLoopback, true},
	}
	for _, tc := range cases {
		if got := webJSNetAllows(tc.guard, tc.place); got != tc.want {
			t.Errorf("webJSNetAllows(%d, %d) = %v, want %v", tc.guard, tc.place, got, tc.want)
		}
	}
}

func TestWebJSNetPageGuard(t *testing.T) {
	cases := []struct {
		page string
		want webJSNetGuard
	}{
		{"https://example.com/news", webJSNetGuardPublic},
		{"https://www.bbc.co.uk/", webJSNetGuardPublic},
		{"http://192.168.1.1/admin", webJSNetGuardLAN},
		{"http://10.0.0.2:8080/", webJSNetGuardLAN},
		{"http://[fd00::5]/", webJSNetGuardLAN},
		{"http://printer.local/", webJSNetGuardLAN},
		{"http://router.lan/", webJSNetGuardLAN},
		{"http://nas/", webJSNetGuardLAN},
		{"http://127.0.0.1:8080/", webJSNetGuardLocal},
		{"http://localhost:3000/", webJSNetGuardLocal},
		{"http://LocalHost./", webJSNetGuardLocal},
		{"http://[::1]:9000/", webJSNetGuardLocal},
		{"http://192.168.1.1./", webJSNetGuardLAN},
		{"http://Router.LAN./", webJSNetGuardLAN},
		{"https://Example.COM./", webJSNetGuardPublic},
		// A name under .localhost may be answered by the network's DNS: it
		// proves nothing about where the page came from. Nor does a name
		// that only becomes localhost after a second dot is dropped.
		{"http://app.localhost/", webJSNetGuardPublic},
		{"http://app.localhost./", webJSNetGuardPublic},
		{"http://localhost../", webJSNetGuardPublic},
		// A numeric host in an odd form proves nothing about where it is.
		{"http://2130706433/", webJSNetGuardPublic},
		{"http://127.1/", webJSNetGuardPublic},
		{"data:text/html,hi", webJSNetGuardPublic},
		{"file:///tmp/page.html", webJSNetGuardPublic},
	}
	for _, tc := range cases {
		if got := webJSNetPageGuard(webJSNetTestURL(t, tc.page)); got != tc.want {
			t.Errorf("webJSNetPageGuard(%s) = %d, want %d", tc.page, got, tc.want)
		}
	}
	if got := webJSNetPageGuard(nil); got != webJSNetGuardPublic {
		t.Errorf("no page = %d, want public", got)
	}
}

func TestWebJSNetNameAllowed(t *testing.T) {
	cases := []struct {
		guard webJSNetGuard
		host  string
		want  bool
	}{
		{webJSNetGuardPublic, "example.com", true},
		{webJSNetGuardPublic, "8.8.8.8", true},
		{webJSNetGuardPublic, "", false},
		{webJSNetGuardPublic, "localhost", false},
		{webJSNetGuardPublic, "LOCALHOST.", false},
		{webJSNetGuardPublic, " LocalHost. ", false},
		{webJSNetGuardPublic, "app.localhost", false},
		{webJSNetGuardPublic, "app.localhost.", false},
		{webJSNetGuardPublic, "127.0.0.1", false},
		{webJSNetGuardPublic, "127.0.0.1.", false},
		{webJSNetGuardPublic, "[::ffff:127.0.0.1]", false},
		{webJSNetGuardPublic, "[::127.0.0.1]", false},
		{webJSNetGuardPublic, "[64:ff9b::a00:1]", false},
		{webJSNetGuardPublic, "[::ffff:0177.0.0.1]", false},
		{webJSNetGuardPublic, "Router.LAN.", false},
		{webJSNetGuardPublic, "10.0.0.1", false},
		{webJSNetGuardPublic, "::1", false},
		{webJSNetGuardPublic, "[::1]", false},
		{webJSNetGuardPublic, "169.254.169.254", false},
		{webJSNetGuardPublic, "127.1", false},
		{webJSNetGuardPublic, "0x7f.1", false},
		{webJSNetGuardPublic, "2130706433", false},
		{webJSNetGuardPublic, "0", false},
		{webJSNetGuardPublic, "nas", false},
		{webJSNetGuardPublic, "printer.local", false},
		{webJSNetGuardPublic, "box.home.arpa", false},
		{webJSNetGuardPublic, "svc.internal", false},
		{webJSNetGuardLAN, "192.168.1.20", true},
		{webJSNetGuardLAN, "nas", true},
		{webJSNetGuardLAN, "example.com", true},
		{webJSNetGuardLAN, "localhost", false},
		{webJSNetGuardLAN, "127.0.0.1", false},
		{webJSNetGuardLocal, "localhost", true},
		{webJSNetGuardLocal, "10.0.0.1", true},
	}
	for _, tc := range cases {
		if got := webJSNetNameAllowed(tc.guard, tc.host); got != tc.want {
			t.Errorf("webJSNetNameAllowed(%d, %q) = %v, want %v", tc.guard, tc.host, got, tc.want)
		}
	}
}

func TestWebJSNetCheckDial(t *testing.T) {
	cases := []struct {
		guard   webJSNetGuard
		address string
		allowed bool
	}{
		{webJSNetGuardPublic, "93.184.216.34:443", true},
		{webJSNetGuardPublic, "[2606:4700:4700::1111]:443", true},
		{webJSNetGuardPublic, "127.0.0.1:80", false},
		{webJSNetGuardPublic, "[::1]:80", false},
		{webJSNetGuardPublic, "192.168.1.1:80", false},
		{webJSNetGuardPublic, "[fe80::1%eth0]:80", false},
		{webJSNetGuardPublic, "[::ffff:127.0.0.1]:80", false},
		{webJSNetGuardLAN, "192.168.1.1:80", true},
		{webJSNetGuardLAN, "127.0.0.1:80", false},
		{webJSNetGuardLocal, "127.0.0.1:80", true},
		// The dialer always hands over an address; anything else is refused.
		{webJSNetGuardPublic, "not an address", false},
		{webJSNetGuardPublic, "example.com:80", false},
	}
	for _, tc := range cases {
		err := webJSNetCheckDial(tc.guard, tc.address)
		if tc.allowed && err != nil {
			t.Errorf("webJSNetCheckDial(%d, %s) = %v, want allowed", tc.guard, tc.address, err)
		}
		if !tc.allowed && !errors.Is(err, webJSNetErrPrivate) {
			t.Errorf("webJSNetCheckDial(%d, %s) = %v, want webJSNetErrPrivate", tc.guard, tc.address, err)
		}
	}
}

// TestWebJSNetTransportGuardsTheDial dials a real listener through each
// guard's transport: the check runs on the resolved address, inside the
// dialer, so no request path can skip it.
func TestWebJSNetTransportGuardsTheDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	for _, guard := range []webJSNetGuard{webJSNetGuardPublic, webJSNetGuardLAN} {
		conn, err := webJSNetTransport(guard).DialContext(context.Background(), "tcp", addr)
		if conn != nil {
			conn.Close()
		}
		if !errors.Is(err, webJSNetErrPrivate) {
			t.Errorf("guard %d dialed %s: err = %v, want webJSNetErrPrivate", guard, addr, err)
		}
	}
	conn, err := webJSNetTransport(webJSNetGuardLocal).DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("a loopback page's transport could not dial %s: %v", addr, err)
	}
	conn.Close()

	// The user's own proxy is reached wherever it is: the note the proxy hook
	// leaves vouches for exactly that one address.
	note := &webJSNetProxyNote{}
	note.set(addr)
	ctx := context.WithValue(context.Background(), webJSNetProxyKey{}, note)
	conn, err = webJSNetTransport(webJSNetGuardPublic).DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("the proxy dial was refused: %v", err)
	}
	conn.Close()
}

func TestWebJSNetProxyHookJudgesTheName(t *testing.T) {
	hook := webJSNetProxyFunc(webJSNetGuardPublic)
	for _, raw := range []string{"http://127.0.0.1/", "http://localhost:8080/", "http://192.168.0.1/", "http://nas/"} {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := hook(req); !errors.Is(err, webJSNetErrPrivate) {
			t.Errorf("proxy hook for %s: err = %v, want webJSNetErrPrivate", raw, err)
		}
	}
}

func TestWebJSNetOriginAndSite(t *testing.T) {
	cases := []struct {
		raw, origin, site string
	}{
		{"https://www.example.co.uk/a?b#c", "https://www.example.co.uk", "https://example.co.uk"},
		{"http://Example.COM:80/x", "http://example.com", "http://example.com"},
		{"https://Example.COM./a", "https://example.com", "https://example.com"},
		{"https://www.example.com.:443/", "https://www.example.com", "https://example.com"},
		{"https://example.com:8443/", "https://example.com:8443", "https://example.com"},
		{"https://a.github.io/repo", "https://a.github.io", "https://a.github.io"},
		{"http://127.0.0.1:8080/", "http://127.0.0.1:8080", "http://127.0.0.1"},
		{"http://[::1]:8080/", "http://[::1]:8080", "http://::1"},
		{"http://nas/", "http://nas", "http://nas"},
		{"data:text/plain,x", "null", ""},
		{"about:blank", "null", ""},
	}
	for _, tc := range cases {
		u := webJSNetTestURL(t, tc.raw)
		if got := webJSNetOrigin(u); got != tc.origin {
			t.Errorf("webJSNetOrigin(%s) = %q, want %q", tc.raw, got, tc.origin)
		}
		if got := webJSNetSite(u); got != tc.site {
			t.Errorf("webJSNetSite(%s) = %q, want %q", tc.raw, got, tc.site)
		}
	}
}

func TestWebJSNetReferrerAndOrigin(t *testing.T) {
	n := newWebJSNet(webJSNetTestURL(t, "https://user:pw@www.example.com/a/b?c=1#frag"), time.Time{})
	referrers := []struct {
		target, want string
	}{
		{"https://www.example.com/x", "https://www.example.com/a/b?c=1"},
		{"https://cdn.example.com/x", "https://www.example.com/"},
		{"http://www.example.com/x", ""},
	}
	for _, tc := range referrers {
		if got := n.referrerFor(webJSNetTestURL(t, tc.target)); got != tc.want {
			t.Errorf("referrerFor(%s) = %q, want %q", tc.target, got, tc.want)
		}
	}
	long := newWebJSNet(webJSNetTestURL(t, "https://www.example.com/?q="+strings.Repeat("a", webJSNetMaxReferrer)), time.Time{})
	if got := long.referrerFor(webJSNetTestURL(t, "https://www.example.com/x")); got != "https://www.example.com/" {
		t.Errorf("a long page address = %q, want the origin", got)
	}

	origins := []struct {
		target, method string
		cors           bool
		want           string
	}{
		{"https://www.example.com/x", http.MethodGet, true, ""},
		{"https://api.example.com/x", http.MethodGet, true, "https://www.example.com"},
		{"https://api.example.com/x", http.MethodGet, false, ""},
		{"https://www.example.com/x", http.MethodPost, true, "https://www.example.com"},
		{"https://api.example.com/x", http.MethodPost, false, "https://www.example.com"},
	}
	for _, tc := range origins {
		h := http.Header{}
		h.Set("Origin", "https://forged.example")
		n.hopHeaders(h, webJSNetTestURL(t, tc.target), tc.method, tc.cors)
		if got := h.Get("Origin"); got != tc.want {
			t.Errorf("Origin for %s %s (cors %v) = %q, want %q", tc.method, tc.target, tc.cors, got, tc.want)
		}
	}
}

func TestWebJSNetMethod(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", "GET", true},
		{"post", "POST", true},
		{"Patch", "PATCH", true},
		{"PROPFIND", "PROPFIND", true},
		{"trace", "", false},
		{"TRACK", "", false},
		{"CONNECT", "", false},
		{"GET X", "", false},
	}
	for _, tc := range cases {
		got, ok := webJSNetMethod(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("webJSNetMethod(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestWebJSNetDecodeDataURL(t *testing.T) {
	cases := []struct {
		raw, mediaType, body string
		ok                   bool
	}{
		{"data:,Hello%2C%20World%21", "text/plain;charset=US-ASCII", "Hello, World!", true},
		{"data:text/plain;base64,SGVsbG8=", "text/plain", "Hello", true},
		{"data:application/json;base64,eyJhIjoxfQ", "application/json", `{"a":1}`, true},
		{"DATA:text/html;charset=utf-8,%3Cb%3Ex%3C/b%3E#frag", "text/html;charset=utf-8", "<b>x</b>", true},
		{"data:;base64,SGk=", "text/plain;charset=US-ASCII", "Hi", true},
		{"data:;charset=utf-8,x", "text/plain;charset=utf-8", "x", true},
		{"data:text/plain;base64,SGV sbG8=", "text/plain", "Hello", true},
		{"data:text/plain; BASE64,SGk", "text/plain", "Hi", true},
		{"data:,100%", "text/plain;charset=US-ASCII", "100%", true},
		{"data:,a%2", "text/plain;charset=US-ASCII", "a%2", true},
		{"data:,a%zz", "text/plain;charset=US-ASCII", "a%zz", true},
		{"data:text/plain;base64,%%%", "", "", false},
		{"data:text/plain;base64,A", "", "", false},
		{"data:text/plain;base64,SGk=SGk=", "", "", false},
		{"data:text/plain", "", "", false},
		{"http://example.com/", "", "", false},
	}
	for _, tc := range cases {
		mediaType, body, why := webJSNetDecodeDataURL(tc.raw, webJSNetMaxBody)
		if (why == "") != tc.ok {
			t.Errorf("webJSNetDecodeDataURL(%q): why = %q, want ok %v", tc.raw, why, tc.ok)
			continue
		}
		if tc.ok && (mediaType != tc.mediaType || string(body) != tc.body) {
			t.Errorf("webJSNetDecodeDataURL(%q) = %q, %q; want %q, %q", tc.raw, mediaType, body, tc.mediaType, tc.body)
		}
	}
	if _, _, why := webJSNetDecodeDataURL("data:,"+strings.Repeat("a", 11), 10); why == "" {
		t.Error("a data: URL past the limit decoded")
	}
}

func TestWebJSNetDataURLResponse(t *testing.T) {
	n := newWebJSNet(webJSNetTestURL(t, "https://www.example.com/"), time.Time{})
	resp := n.do(context.Background(), webJSRequest{URL: "data:application/json,%7B%7D#x"})
	if resp.Err != "" || resp.Type != "basic" || resp.Status != http.StatusOK || resp.StatusText != "OK" {
		t.Fatalf("data: fetch = %+v", resp)
	}
	if resp.URL != "data:application/json,%7B%7D" || string(resp.Body) != "{}" {
		t.Errorf("data: fetch URL %q body %q", resp.URL, resp.Body)
	}
	if got := webJSNetTestPairs(resp.Header); got != "content-type=application/json" {
		t.Errorf("data: fetch headers = %s", got)
	}
	if requests, _, _ := n.stats(); requests != 0 {
		t.Errorf("a data: URL counted as %d network requests", requests)
	}
}

func TestWebJSNetCORSAllowed(t *testing.T) {
	origin := "https://www.example.com"
	mk := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			h.Add(kv[i], kv[i+1])
		}
		return h
	}
	cases := []struct {
		name         string
		h            http.Header
		credentialed bool
		want         bool
	}{
		{"star", mk("Access-Control-Allow-Origin", "*"), false, true},
		{"star with cookies", mk("Access-Control-Allow-Origin", "*"), true, false},
		{"exact", mk("Access-Control-Allow-Origin", origin), false, true},
		{"exact with cookies, no allow-credentials", mk("Access-Control-Allow-Origin", origin), true, false},
		{"exact with cookies and allow-credentials", mk("Access-Control-Allow-Origin", origin, "Access-Control-Allow-Credentials", "true"), true, true},
		{"another origin", mk("Access-Control-Allow-Origin", "https://evil.example"), false, false},
		{"another port", mk("Access-Control-Allow-Origin", origin+":8443"), false, false},
		{"missing", mk(), false, false},
		{"two headers", mk("Access-Control-Allow-Origin", origin, "Access-Control-Allow-Origin", "*"), false, false},
		{"a list", mk("Access-Control-Allow-Origin", origin+", https://b.example"), false, false},
	}
	for _, tc := range cases {
		if got := webJSNetCORSAllowed(tc.h, origin, tc.credentialed); got != tc.want {
			t.Errorf("%s: webJSNetCORSAllowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWebJSNetResponseHeader(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "text/plain")
	h.Set("X-Secret", "s")
	h.Set("X-Exposed", "e")
	h.Add("Set-Cookie", "a=1")
	h.Set("Access-Control-Expose-Headers", "X-Exposed")
	cases := []struct {
		name               string
		expose             string
		cors, credentialed bool
		want               string
	}{
		{"same origin", "X-Exposed", false, false, "access-control-expose-headers=X-Exposed|content-type=text/plain|x-exposed=e|x-secret=s"},
		{"cors", "X-Exposed", true, false, "content-type=text/plain|x-exposed=e"},
		{"cors, star", "*", true, false, "access-control-expose-headers=*|content-type=text/plain|x-exposed=e|x-secret=s"},
		{"cors, star, credentialed", "*", true, true, "content-type=text/plain"},
	}
	for _, tc := range cases {
		h.Set("Access-Control-Expose-Headers", tc.expose)
		if got := webJSNetTestPairs(webJSNetResponseHeader(h, tc.cors, tc.credentialed)); got != tc.want {
			t.Errorf("%s: headers = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestWebJSNetRequestHeader(t *testing.T) {
	pairs := [][2]string{
		{"Content-Type", "text/plain;charset=UTF-8"},
		{"Cookie", "a=b"},
		{"X-Custom", "1"},
		{"Sec-Fetch-Mode", "cors"},
		{"Proxy-Authorization", "Basic eA=="},
		{"Bad Name", "x"},
		{"X-Inject", "a\r\nInjected: 1"},
		{"Accept", "application/json"},
		{"X-HTTP-Method-Override", "TRACE"},
		{"Access-Control-Request-Method", "PUT"},
		{"Host", "evil.example"},
		{"User-Agent", "Evil/1.0"},
		{"Origin", "https://evil.example"},
		{"Referer", "https://evil.example/"},
	}
	h, simple := webJSNetRequestHeader(pairs, false)
	if len(h) != 3 || h.Get("Content-Type") != "text/plain;charset=UTF-8" || h.Get("X-Custom") != "1" || h.Get("Accept") != "application/json" {
		t.Errorf("cors headers = %v", h)
	}
	if simple {
		t.Error("X-Custom left the request simple")
	}
	h, simple = webJSNetRequestHeader(pairs, true)
	if len(h) != 2 || h.Get("Content-Type") == "" || h.Get("Accept") == "" || !simple {
		t.Errorf("no-cors headers = %v (simple %v), want only Content-Type and Accept", h, simple)
	}
	if _, simple := webJSNetRequestHeader([][2]string{{"Content-Type", "application/json"}}, false); simple {
		t.Error("a JSON body type left the request simple")
	}
}

func TestWebJSNetFetchSameOrigin(t *testing.T) {
	webJSNetTestCookies(t)
	var mu sync.Mutex
	var seen http.Header
	var seenHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/echo":
			mu.Lock()
			seen, seenHost = r.Header.Clone(), r.Host
			mu.Unlock()
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("X-Info", "i")
			http.SetCookie(w, &http.Cookie{Name: "t", Value: "1", Path: "/"})
			io.WriteString(w, "hello")
		case "/post":
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("X-Origin", r.Header.Get("Origin"))
			w.Write(body)
		}
	}))
	defer srv.Close()
	n := newWebJSNet(webJSNetTestURL(t, srv.URL+"/page?x=1#top"), time.Time{})
	ctx := context.Background()

	resp := n.do(ctx, webJSRequest{
		URL: srv.URL + "/echo?q=1#frag",
		Header: [][2]string{
			{"Cookie", "a=b"},
			{"Host", "evil.example"},
			{"Origin", "http://evil.example"},
			{"Referer", "http://evil.example/"},
			{"Sec-Fetch-Site", "none"},
			{"Proxy-Authorization", "x"},
			{"X-Custom", "ok"},
			{"User-Agent", "Evil/1.0"},
			{"Accept-Encoding", "br"},
		},
	})
	if resp.Err != "" {
		t.Fatalf("same-origin fetch failed: %s", resp.Err)
	}
	if resp.Type != "basic" || resp.Status != 200 || resp.StatusText != "OK" || resp.Redirected {
		t.Errorf("response = %+v", resp)
	}
	if resp.URL != srv.URL+"/echo?q=1" || string(resp.Body) != "hello" {
		t.Errorf("URL %q body %q", resp.URL, resp.Body)
	}
	if v, _ := webJSNetTestHeader(resp, "x-info"); v != "i" {
		t.Errorf("x-info = %q", v)
	}
	if _, ok := webJSNetTestHeader(resp, "set-cookie"); ok {
		t.Error("Set-Cookie reached the script")
	}

	mu.Lock()
	got, host := seen, seenHost
	mu.Unlock()
	checks := []struct {
		name, got, want string
	}{
		{"User-Agent", got.Get("User-Agent"), "JukaHub Browser/1.1"},
		{"Origin", got.Get("Origin"), ""},
		{"Referer", got.Get("Referer"), srv.URL + "/page?x=1"},
		{"Cookie", got.Get("Cookie"), ""},
		{"X-Custom", got.Get("X-Custom"), "ok"},
		{"Sec-Fetch-Site", got.Get("Sec-Fetch-Site"), ""},
		{"Proxy-Authorization", got.Get("Proxy-Authorization"), ""},
		{"Accept", got.Get("Accept"), "*/*"},
		{"Host", host, strings.TrimPrefix(srv.URL, "http://")},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("server saw %s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if got.Get("Accept-Encoding") == "br" {
		t.Error("the script's Accept-Encoding was sent")
	}
	// The response's cookie went into the jar: the request was the page's own.
	if c := n.cookieString(); c != "t=1" {
		t.Errorf("document.cookie = %q, want t=1", c)
	}

	resp = n.do(ctx, webJSRequest{Method: "POST", URL: srv.URL + "/post", Body: []byte("payload"), Header: [][2]string{{"Content-Type", "text/plain"}}})
	if resp.Err != "" || string(resp.Body) != "payload" {
		t.Fatalf("POST = %+v", resp)
	}
	if v, _ := webJSNetTestHeader(resp, "x-origin"); v != srv.URL {
		t.Errorf("a same-origin POST sent Origin %q, want %q", v, srv.URL)
	}
}

func TestWebJSNetFetchCrossOrigin(t *testing.T) {
	webJSNetTestCookies(t)
	page := httptest.NewServer(http.NotFoundHandler())
	defer page.Close()
	pageOrigin := page.URL
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Secret", "s")
		switch r.URL.Path {
		case "/star":
			w.Header().Set("Access-Control-Allow-Origin", "*")
		case "/origin":
			w.Header().Set("Access-Control-Allow-Origin", pageOrigin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Expose-Headers", "X-Exposed")
			w.Header().Set("X-Exposed", "e")
		case "/other":
			w.Header().Set("Access-Control-Allow-Origin", "http://example.com")
		}
		io.WriteString(w, "cookie="+r.Header.Get("Cookie")+";origin="+r.Header.Get("Origin")+";referer="+r.Header.Get("Referer"))
	}))
	defer api.Close()
	n := newWebJSNet(webJSNetTestURL(t, page.URL+"/article?id=7"), time.Time{})
	ctx := context.Background()

	resp := n.do(ctx, webJSRequest{URL: api.URL + "/star"})
	if resp.Err != "" || resp.Type != "cors" || resp.Status != 200 {
		t.Fatalf("shared answer = %+v", resp)
	}
	if want := "cookie=;origin=" + pageOrigin + ";referer=" + pageOrigin + "/"; string(resp.Body) != want {
		t.Errorf("body %q, want %q", resp.Body, want)
	}
	if v, _ := webJSNetTestHeader(resp, "content-type"); v != "text/plain" {
		t.Errorf("content-type = %q", v)
	}
	// Date and the CORS header itself are not safelisted; the server's own
	// Content-Length is, so it is not checked either way.
	for _, name := range []string{"x-secret", "date", "access-control-allow-origin"} {
		if _, ok := webJSNetTestHeader(resp, name); ok {
			t.Errorf("%s reached another origin's script", name)
		}
	}
	for _, path := range []string{"/none", "/other"} {
		if resp := n.do(ctx, webJSRequest{URL: api.URL + path}); resp.Err != "CORS" {
			t.Errorf("%s: Err = %q, want CORS", path, resp.Err)
		}
	}
	resp = n.do(ctx, webJSRequest{URL: api.URL + "/none", Mode: "no-cors"})
	if resp.Err != "" || resp.Type != "opaque" || resp.Status != 0 || len(resp.Body) != 0 || len(resp.Header) != 0 || resp.URL != "" {
		t.Errorf("no-cors answer = %+v, want an empty opaque one", resp)
	}
	resp = n.do(ctx, webJSRequest{URL: api.URL + "/origin"})
	if resp.Err != "" || resp.Type != "cors" {
		t.Fatalf("named-origin answer = %+v", resp)
	}
	if v, _ := webJSNetTestHeader(resp, "x-exposed"); v != "e" {
		t.Errorf("x-exposed = %q", v)
	}
	if _, ok := webJSNetTestHeader(resp, "x-secret"); ok {
		t.Error("an unexposed header reached the script")
	}
	if resp := n.do(ctx, webJSRequest{URL: api.URL + "/star", Mode: "same-origin"}); resp.Err == "" {
		t.Error("a same-origin-mode request reached another origin")
	}

	// With a cookie on the site (127.0.0.1 is one site for every port), only
	// credentials "include" sends it across origins, and then "*" no longer
	// shares the answer.
	n.setCookieString("sid=1")
	if resp := n.do(ctx, webJSRequest{URL: api.URL + "/star", Credentials: "include"}); resp.Err != "CORS" {
		t.Errorf("credentialed request to a * server: Err = %q, want CORS", resp.Err)
	}
	resp = n.do(ctx, webJSRequest{URL: api.URL + "/origin", Credentials: "include"})
	if resp.Err != "" || !strings.HasPrefix(string(resp.Body), "cookie=sid=1;") {
		t.Errorf("credentialed request = %+v, want the cookie sent and the answer shared", resp)
	}
	resp = n.do(ctx, webJSRequest{URL: api.URL + "/star"})
	if resp.Err != "" || !strings.HasPrefix(string(resp.Body), "cookie=;") {
		t.Errorf("default credentials = %+v, want no cookie across origins", resp)
	}
	// A request a browser would preflight goes without cookies instead.
	resp = n.do(ctx, webJSRequest{URL: api.URL + "/origin", Credentials: "include", Header: [][2]string{{"X-Custom", "1"}}})
	if resp.Err != "" || !strings.HasPrefix(string(resp.Body), "cookie=;") {
		t.Errorf("non-simple credentialed request = %+v, want no cookie", resp)
	}
}

func TestWebJSNetCookieSameSiteRule(t *testing.T) {
	webJSNetTestCookies(t)
	n := newWebJSNet(webJSNetTestURL(t, "https://www.example.com/page"), time.Time{})
	jar := browserCookies()
	if jar == nil {
		t.Fatal("no cookie jar")
	}
	jar.SetCookies(webJSNetTestURL(t, "https://example.com/"), []*http.Cookie{{Name: "sid", Value: "1", Domain: "example.com", Path: "/"}})
	names := func(cs []*http.Cookie) string {
		parts := make([]string, 0, len(cs))
		for _, c := range cs {
			parts = append(parts, c.Name+"="+c.Value)
		}
		return strings.Join(parts, ";")
	}
	include := &webJSNetCall{n: n, creds: "include"}
	cases := []struct {
		call   *webJSNetCall
		target string
		want   string
	}{
		{include, "https://api.example.com/x", "sid=1"},
		{include, "https://evil.example/", ""},
		{include, "http://api.example.com/", ""},
		{&webJSNetCall{n: n, creds: "same-origin"}, "https://api.example.com/x", ""},
		{&webJSNetCall{n: n, creds: "same-origin"}, "https://www.example.com/a", "sid=1"},
		{&webJSNetCall{n: n, creds: "omit"}, "https://www.example.com/a", ""},
	}
	for _, tc := range cases {
		if got := names(tc.call.Cookies(webJSNetTestURL(t, tc.target))); got != tc.want {
			t.Errorf("credentials %q to %s carried %q, want %q", tc.call.creds, tc.target, got, tc.want)
		}
		if tc.call.sent != (tc.want != "") {
			t.Errorf("credentials %q to %s: sent = %v", tc.call.creds, tc.target, tc.call.sent)
		}
	}

	// Writes follow the same rule.
	include.SetCookies(webJSNetTestURL(t, "https://evil.example/"), []*http.Cookie{{Name: "x", Value: "1", Path: "/"}})
	if got := webJSNetTestJar(t, "https://evil.example/"); len(got) != 0 {
		t.Errorf("a cross-site answer stored %v", got)
	}
	include.SetCookies(webJSNetTestURL(t, "https://api.example.com/"), []*http.Cookie{{Name: "y", Value: "2", Path: "/"}})
	if got := webJSNetTestJar(t, "https://api.example.com/"); got["y"] != "2" {
		t.Errorf("a same-site answer did not store its cookie: %v", got)
	}
	// A Secure cookie from plain http is refused even on the page's own site.
	plain := &webJSNetCall{n: newWebJSNet(webJSNetTestURL(t, "http://www.example.com/"), time.Time{}), creds: "include"}
	plain.SetCookies(webJSNetTestURL(t, "http://www.example.com/"), []*http.Cookie{{Name: "s", Value: "1", Path: "/", Secure: true}})
	if _, ok := webJSNetTestJar(t, "https://www.example.com/")["s"]; ok {
		t.Error("plain http set a Secure cookie")
	}
}

func TestWebJSNetDocumentCookie(t *testing.T) {
	webJSNetTestCookies(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "h", Value: "secret", Path: "/", HttpOnly: true})
		http.SetCookie(w, &http.Cookie{Name: "v", Value: "visible", Path: "/"})
	}))
	defer srv.Close()
	n := newWebJSNet(webJSNetTestURL(t, srv.URL+"/"), time.Time{})
	if resp := n.do(context.Background(), webJSRequest{URL: srv.URL + "/login"}); resp.Err != "" {
		t.Fatalf("login fetch failed: %s", resp.Err)
	}
	if got := n.cookieString(); got != "v=visible" {
		t.Errorf("document.cookie = %q, want only the script-visible cookie", got)
	}
	n.setCookieString("h=evil")
	if got := webJSNetTestJar(t, srv.URL+"/")["h"]; got != "secret" {
		t.Errorf("a script overwrote the HttpOnly cookie: h = %q", got)
	}
	n.setCookieString("v=changed")
	for _, line := range []string{
		"s=1; Secure",                       // Secure from a plain-http page
		"__Host-x=1; Path=/",                // __Host- without Secure
		"bad",                               // not a cookie at all
		"long=" + strings.Repeat("x", 4096), // past the per-cookie size
	} {
		n.setCookieString(line)
	}
	if got := n.cookieString(); got != "v=changed" {
		t.Errorf("document.cookie = %q, want v=changed", got)
	}
	n.setCookieString("v=; Max-Age=0")
	if got := n.cookieString(); got != "" {
		t.Errorf("after the delete document.cookie = %q, want empty", got)
	}

	sec := newWebJSNet(webJSNetTestURL(t, "https://www.example.com/"), time.Time{})
	jar := browserCookies()
	if jar == nil {
		t.Fatal("no cookie jar")
	}
	// A server's HttpOnly session for the whole site: a script on a subdomain
	// must not plant a same-named cookie next to it.
	jar.SetCookies(webJSNetTestURL(t, "https://example.com/"), []*http.Cookie{{Name: "session", Value: "s3", Domain: "example.com", Path: "/", Secure: true, HttpOnly: true}})
	for _, line := range []string{
		"__Host-a=1; Secure; Path=/",
		"__Host-b=1; Secure; Path=/; Domain=example.com",
		"__Secure-c=1",
		"__Secure-d=1; Secure",
		"session=evil",
	} {
		sec.setCookieString(line)
	}
	got := map[string]bool{}
	for _, pair := range strings.Split(sec.cookieString(), "; ") {
		got[pair] = true
	}
	if len(got) != 2 || !got["__Host-a=1"] || !got["__Secure-d=1"] {
		t.Errorf("document.cookie on https = %v, want __Host-a=1 and __Secure-d=1", got)
	}
	if v := webJSNetTestJar(t, "https://www.example.com/")["session"]; v != "s3" {
		t.Errorf("session = %q, want the server's s3", v)
	}

	// Past the per-page limit, assignments are ignored.
	capped := newWebJSNet(webJSNetTestURL(t, "https://capped.example/"), time.Time{})
	capped.setCookieString("first=1")
	capped.cookieSets = webJSNetMaxCookieSets
	capped.setCookieString("last=1")
	if got := capped.cookieString(); got != "first=1" {
		t.Errorf("document.cookie past the limit = %q, want first=1", got)
	}
}

func TestWebJSNetHostSpellings(t *testing.T) {
	hosts := []struct {
		in, host, cookie string
	}{
		{" Example.COM. ", "example.com", "example.com"},
		{"example.com..", "example.com.", "example.com."},
		{"[::1]", "::1", "::1"},
		{"xn--bcher-kva.example.", "xn--bcher-kva.example", "bücher.example"},
		{"XN--BCHER-KVA.EXAMPLE", "xn--bcher-kva.example", "bücher.example"},
		{"BÜCHER.example", "bücher.example", "bücher.example"},
		// The jar's own punycode of the upper-case spelling meets it too.
		{"xn--bcher-2pa.example", "xn--bcher-2pa.example", "bücher.example"},
		{"www.xn--mnchen-3ya.de", "www.xn--mnchen-3ya.de", "www.münchen.de"},
		// Not punycode (a missing digit, nothing encoded): kept as it is.
		{"xn--zz.example", "xn--zz.example", "xn--zz.example"},
		{"xn--abc-.example", "xn--abc-.example", "xn--abc-.example"},
	}
	for _, tc := range hosts {
		if got := webJSNetHost(tc.in); got != tc.host {
			t.Errorf("webJSNetHost(%q) = %q, want %q", tc.in, got, tc.host)
		}
		if got := webJSNetCookieHost(tc.in); got != tc.cookie {
			t.Errorf("webJSNetCookieHost(%q) = %q, want %q", tc.in, got, tc.cookie)
		}
	}
	domains := []struct {
		in, want string
	}{
		{"", ""},
		{".", ""},
		{".example.com", "example.com"},
		{".Example.COM.", "example.com"},
		{"..example.com", "example.com"},
		{" .xn--bcher-kva.example ", "bücher.example"},
	}
	for _, tc := range domains {
		if got := webJSNetCookieDomain(tc.in); got != tc.want {
			t.Errorf("webJSNetCookieDomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	puny := []struct {
		in, want string
		ok       bool
	}{
		{"bcher-kva", "bücher", true},
		{"BCHER-KVA", "BüCHER", true},
		{"mnchen-3ya", "münchen", true},
		{"tda", "ü", true},
		{"Proprostnemluvesky-uyb24dma41a", "Pročprostěnemluvíčesky", true},
		{"", "", false},
		{"-", "", false},
		{"-kva", "", false},
		{"abc-", "", false},
		{"zz", "", false},
		{"a-zz9", "", false},
		{"aü", "", false},
		{"99999999999999999999", "", false},
	}
	for _, tc := range puny {
		got, ok := webJSNetPunycode(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("webJSNetPunycode(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestWebJSNetCookieScopeSpellings(t *testing.T) {
	rec := func(host, domain string) browserCookieRecord {
		return browserCookieRecord{Host: host, Domain: domain, Path: "/", Name: "sid", Value: "1", HTTPOnly: true}
	}
	covers := []struct {
		rec  browserCookieRecord
		page string
		want bool
	}{
		{rec("example.com", ""), "Example.COM.", true},
		{rec("example.com.", ""), "example.com", true},
		{rec("example.com.", ""), "www.example.com", false},
		{rec("www.example.com.", ".Example.com."), "example.com", true},
		{rec("www.example.com.", ".Example.com."), "a.b.example.com.", true},
		{rec("www.example.com.", ".Example.com."), "badexample.com", false},
		{rec("www.example.com.", ".Example.com."), "example.com.evil", false},
		{rec("bücher.example", ""), "xn--bcher-kva.example", true},
		{rec("xn--bcher-kva.example.", ""), "BÜCHER.example", true},
		{rec("www.bücher.example", "xn--bcher-kva.example"), "bücher.example", true},
		{rec("bücher.example", ""), "xn--mnchen-3ya.example", false},
		{rec("::1", ""), "[::1]", true},
	}
	for _, tc := range covers {
		if got := webJSNetRecordCovers(tc.rec, webJSNetCookieHost(tc.page)); got != tc.want {
			t.Errorf("a record for %q (domain %q) covers %q = %v, want %v", tc.rec.Host, tc.rec.Domain, tc.page, got, tc.want)
		}
	}
	overlaps := []struct {
		page, domain string
		rec          browserCookieRecord
		want         bool
	}{
		{"example.com", "", rec("example.com.", ""), true},
		{"Example.COM.", "", rec("example.com", ""), true},
		{"www.example.com", "", rec("a.example.com", ".example.com."), true},
		{"www.example.com", ".EXAMPLE.com.", rec("a.example.com.", ""), true},
		{"www.example.com", "example.com", rec("x.example.com", "x.example.com"), true},
		{"www.example.com", "", rec("a.example.com", ""), false},
		{"xn--bcher-kva.example", "", rec("bücher.example", ""), true},
		{"bücher.example", "", rec("www.xn--bcher-kva.example", ".xn--bcher-kva.example"), true},
		{"example.org", "", rec("example.com", ""), false},
	}
	for _, tc := range overlaps {
		got := webJSNetScopesOverlap(webJSNetCookieHost(tc.page), webJSNetCookieDomain(tc.domain), tc.rec)
		if got != tc.want {
			t.Errorf("a script cookie on %q (domain %q) overlaps a record for %q (domain %q) = %v, want %v",
				tc.page, tc.domain, tc.rec.Host, tc.rec.Domain, got, tc.want)
		}
	}
}

// TestWebJSNetDocumentCookieHostSpellings stores HttpOnly cookies under one
// spelling of a host and reads and writes document.cookie under the others:
// the jar treats them all as one host, so the HttpOnly checks must too.
func TestWebJSNetDocumentCookieHostSpellings(t *testing.T) {
	webJSNetTestCookies(t)
	jar, ok := browserCookies().(*browserCookieJar)
	if !ok {
		t.Fatal("no cookie jar")
	}
	set := func(raw string, cookies ...*http.Cookie) {
		t.Helper()
		jar.SetCookies(webJSNetTestURL(t, raw), cookies)
	}
	values := func(raw, name string) string {
		t.Helper()
		var out []string
		for _, c := range jar.Cookies(webJSNetTestURL(t, raw)) {
			if c.Name == name {
				out = append(out, c.Value)
			}
		}
		return strings.Join(out, ",")
	}
	// The fully qualified spelling: the records keep "example.com.", the jar
	// files the cookies under example.com.
	set("https://example.com./login",
		&http.Cookie{Name: "h", Value: "secret", Path: "/", Secure: true, HttpOnly: true},
		&http.Cookie{Name: "v", Value: "visible", Path: "/", Secure: true})
	set("https://www.example.com./",
		&http.Cookie{Name: "dh", Value: "dsecret", Path: "/", Domain: ".example.com", Secure: true, HttpOnly: true})
	// One internationalised site, stored from both of its spellings.
	set("https://bücher.example/",
		&http.Cookie{Name: "ih", Value: "isecret", Path: "/", HttpOnly: true},
		&http.Cookie{Name: "iv", Value: "ivisible", Path: "/"})
	set("https://www.xn--bcher-kva.example/",
		&http.Cookie{Name: "idh", Value: "idsecret", Path: "/", Domain: "xn--bcher-kva.example", HttpOnly: true})
	// A port-less IPv6 literal and an upper-case Unicode host, which the jar
	// files under names the records cannot rebuild.
	set("http://[::1]/", &http.Cookie{Name: "h6", Value: "secret6", Path: "/", HttpOnly: true})
	set("http://[::1]:8080/", &http.Cookie{Name: "v6", Value: "visible6", Path: "/"})
	set("https://BÜCHER.example/", &http.Cookie{Name: "uh", Value: "usecret", Path: "/", HttpOnly: true})

	pages := []struct {
		page, want string
		attempts   []string
	}{
		{"https://example.com/", "v=visible", []string{"h=evil", "dh=evil", "dh=evil; Domain=example.com"}},
		{"https://example.com./", "v=visible", []string{"h=evil", "dh=evil"}},
		{"https://EXAMPLE.com.:8443/x", "v=visible", []string{"h=evil; Path=/x", "dh=evil; Domain=EXAMPLE.com"}},
		{"https://www.example.com/", "", []string{"dh=evil", "dh=evil; Domain=.example.com"}},
		{"https://xn--bcher-kva.example/", "iv=ivisible", []string{"ih=evil", "idh=evil"}},
		{"https://bücher.example/", "iv=ivisible", []string{"ih=evil", "idh=evil", "idh=evil; Domain=xn--bcher-kva.example"}},
		{"https://XN--BCHER-KVA.example./", "iv=ivisible", []string{"ih=evil", "idh=evil"}},
		{"https://BÜCHER.example/", "iv=ivisible", []string{"uh=evil"}},
		{"http://[::1]/", "v6=visible6", []string{"h6=evil"}},
		{"http://[::1]:8080/", "v6=visible6", nil},
	}
	// Every read first: the attempts below may legitimately store cookies
	// under other names, which would change what a later page reads.
	for _, tc := range pages {
		n := newWebJSNet(webJSNetTestURL(t, tc.page), time.Time{})
		if got := n.cookieString(); got != tc.want {
			t.Errorf("document.cookie on %s = %q, want %q", tc.page, got, tc.want)
		}
	}
	for _, tc := range pages {
		n := newWebJSNet(webJSNetTestURL(t, tc.page), time.Time{})
		for _, line := range tc.attempts {
			n.setCookieString(line)
		}
	}
	// What the server set is what every request still carries, alone: no
	// script replaced it, and none planted a same-named cookie beside it.
	held := []struct {
		at, name, want string
	}{
		{"https://example.com/", "h", "secret"},
		{"https://example.com/x", "h", "secret"},
		{"https://example.com/", "dh", "dsecret"},
		{"https://www.example.com/", "dh", "dsecret"},
		{"https://xn--bcher-kva.example/", "ih", "isecret"},
		{"https://xn--bcher-kva.example/", "idh", "idsecret"},
		{"https://www.xn--bcher-kva.example/", "idh", "idsecret"},
		{"http://[::1]/", "h6", "secret6"},
		{"https://BÜCHER.example/", "uh", "usecret"},
	}
	for _, tc := range held {
		if got := values(tc.at, tc.name); got != tc.want {
			t.Errorf("a request to %s carries %s = %q, want only the server's %q", tc.at, tc.name, got, tc.want)
		}
	}
}

// TestWebJSNetDocumentCookieRetiredJar: a Forget empties the records but not
// a jar a page still holds, which then must answer nothing.
func TestWebJSNetDocumentCookieRetiredJar(t *testing.T) {
	jar := newBrowserCookieJar("")
	if jar == nil {
		t.Fatal("no cookie jar")
	}
	page := webJSNetTestURL(t, "https://example.com/")
	jar.SetCookies(page, []*http.Cookie{
		{Name: "h", Value: "secret", Path: "/", HttpOnly: true},
		{Name: "v", Value: "1", Path: "/"},
	})
	if got := jar.webJSNetDocumentCookie(page); got != "v=1" {
		t.Fatalf("document.cookie = %q, want v=1", got)
	}
	jar.retire()
	if got := jar.webJSNetDocumentCookie(page); got != "" {
		t.Errorf("a retired jar's document.cookie = %q, want nothing", got)
	}
	if jar.webJSNetScriptSetCookie(page, &http.Cookie{Name: "x", Value: "1", Path: "/"}) {
		t.Error("a retired jar took a script's cookie")
	}
}

func TestWebJSNetTrailingDotIsTheSameHost(t *testing.T) {
	n := newWebJSNet(webJSNetTestURL(t, "https://example.com./a"), time.Time{})
	if n.origin != "https://example.com" {
		t.Errorf("origin = %q, want https://example.com", n.origin)
	}
	if !n.sameOrigin(webJSNetTestURL(t, "https://EXAMPLE.com/x")) || !n.sameSite(webJSNetTestURL(t, "https://www.example.com./")) {
		t.Error("example.com. and example.com compared as two hosts")
	}
	if n.sameOrigin(webJSNetTestURL(t, "https://example.com../")) {
		t.Error("example.com.. compared as example.com (the jar has it as another name)")
	}

	// The ad filter is asked about the name DNS would look up.
	local := newWebJSNet(webJSNetTestURL(t, "http://127.0.0.1:1/"), time.Time{})
	for _, raw := range []string{"https://doubleclick.net./x", "https://ads.DOUBLECLICK.net.:443/x"} {
		if why := local.refuse(webJSNetTestURL(t, raw)); why != webJSNetBlockedByFilter {
			t.Errorf("refuse(%s) = %q, want %q", raw, why, webJSNetBlockedByFilter)
		}
	}
	filtered := []struct {
		in, want string
	}{
		{"https://a.example.:8443/p?q=1", "https://a.example:8443/p?q=1"},
		{"https://a.example./", "https://a.example/"},
		{"https://a.example/", "https://a.example/"},
		{"https://[::1]/p", "https://[::1]/p"},
	}
	for _, tc := range filtered {
		if got := webJSNetFilterURL(webJSNetTestURL(t, tc.in)); got != tc.want {
			t.Errorf("webJSNetFilterURL(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWebJSNetMixedContent(t *testing.T) {
	ctx := context.Background()
	n := newWebJSNet(webJSNetTestURL(t, "https://www.example.com/"), time.Time{})
	if resp := n.do(ctx, webJSRequest{URL: "http://www.example.com/api"}); resp.Err == "" {
		t.Error("an https page fetched plain http")
	}
	if resp := n.fetchScript(ctx, "http://cdn.example.com/lib.js", false); resp.Err == "" {
		t.Error("an https page loaded a plain-http script")
	}
	if requests, _, _ := n.stats(); requests != 0 {
		t.Errorf("mixed content sent %d requests", requests)
	}
	for _, host := range []string{"localhost", "app.localhost", "127.0.0.1", "[::1]", "::ffff:127.0.0.1"} {
		if !webJSNetLoopbackName(host) {
			t.Errorf("webJSNetLoopbackName(%q) = false", host)
		}
	}
	for _, host := range []string{"example.com", "192.168.1.1", "127.1", "localhost.example.com"} {
		if webJSNetLoopbackName(host) {
			t.Errorf("webJSNetLoopbackName(%q) = true", host)
		}
	}
}

func TestWebJSNetBodyLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exact":
			w.Write(make([]byte, webJSNetMaxBody))
		case "/over":
			w.Write(make([]byte, webJSNetMaxBody+1))
		case "/declared":
			w.Header().Set("Content-Length", strconv.Itoa(webJSNetMaxBody+1))
			w.Write(make([]byte, webJSNetMaxBody+1))
		case "/script.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(bytes.Repeat([]byte(" "), webJSNetMaxScript+1))
		}
	}))
	defer srv.Close()
	page := webJSNetTestURL(t, srv.URL+"/")
	ctx := context.Background()
	n := newWebJSNet(page, time.Time{})
	if resp := n.do(ctx, webJSRequest{URL: srv.URL + "/exact"}); resp.Err != "" || len(resp.Body) != webJSNetMaxBody {
		t.Errorf("a body exactly at the limit: Err %q, %d bytes", resp.Err, len(resp.Body))
	}
	for _, path := range []string{"/over", "/declared"} {
		if resp := n.do(ctx, webJSRequest{URL: srv.URL + path}); resp.Err != "response too large" {
			t.Errorf("%s: Err = %q, want response too large", path, resp.Err)
		}
	}
	if resp := n.fetchScript(ctx, srv.URL+"/script.js", false); resp.Err != "response too large" {
		t.Errorf("an oversized script: Err = %q", resp.Err)
	}

	spent := newWebJSNet(page, time.Time{})
	spent.bytes = webJSNetMaxTotal
	if resp := spent.do(ctx, webJSRequest{URL: srv.URL + "/exact"}); resp.Err != webJSNetErrBudget.Error() {
		t.Errorf("a spent page: Err = %q", resp.Err)
	}
	nearly := newWebJSNet(page, time.Time{})
	nearly.bytes = webJSNetMaxTotal - 10
	if resp := nearly.do(ctx, webJSRequest{URL: srv.URL + "/exact"}); resp.Err != webJSNetErrBudget.Error() {
		t.Errorf("a download crossing the page budget: Err = %q", resp.Err)
	}
}

func TestWebJSNetRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "elsewhere")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redir":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			io.WriteString(w, "final")
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/away":
			http.Redirect(w, r, other.URL+"/plain", http.StatusFound)
		case "/to-filter":
			http.Redirect(w, r, "https://doubleclick.net/x", http.StatusFound)
		}
	}))
	defer srv.Close()
	page := webJSNetTestURL(t, srv.URL+"/")
	ctx := context.Background()

	n := newWebJSNet(page, time.Time{})
	resp := n.do(ctx, webJSRequest{URL: srv.URL + "/redir"})
	if resp.Err != "" || resp.Type != "basic" || resp.Status != 200 || !resp.Redirected || resp.URL != srv.URL+"/final" || string(resp.Body) != "final" {
		t.Errorf("followed redirect = %+v", resp)
	}
	if requests, _, _ := n.stats(); requests != 2 {
		t.Errorf("a redirect counted %d requests, want 2", requests)
	}
	if resp := n.do(ctx, webJSRequest{URL: srv.URL + "/redir", Redirect: "error"}); resp.Err == "" {
		t.Error("redirect: \"error\" followed a redirect")
	}
	resp = n.do(ctx, webJSRequest{URL: srv.URL + "/redir", Redirect: "manual"})
	if resp.Err != "" || resp.Type != "opaqueredirect" || resp.Status != 0 || resp.URL != srv.URL+"/redir" || len(resp.Body) != 0 {
		t.Errorf("manual redirect = %+v", resp)
	}

	loop := newWebJSNet(page, time.Time{})
	if resp := loop.do(ctx, webJSRequest{URL: srv.URL + "/loop"}); resp.Err == "" {
		t.Error("a redirect loop was followed to the end")
	}
	if requests, _, _ := loop.stats(); requests != 1+webJSNetMaxRedirects {
		t.Errorf("a redirect loop sent %d requests, want %d", requests, 1+webJSNetMaxRedirects)
	}

	if resp := n.do(ctx, webJSRequest{URL: srv.URL + "/away"}); resp.Err != "CORS" {
		t.Errorf("a redirect to an unshared origin: Err = %q, want CORS", resp.Err)
	}
	if resp := n.do(ctx, webJSRequest{URL: srv.URL + "/away", Mode: "no-cors"}); resp.Err != "" || resp.Type != "opaque" {
		t.Errorf("a no-cors redirect to another origin = %+v, want opaque", resp)
	}
	if resp := n.do(ctx, webJSRequest{URL: srv.URL + "/to-filter"}); resp.Err != webJSNetBlockedByFilter {
		t.Errorf("a redirect to an ad host: Err = %q", resp.Err)
	}
}

func TestWebJSNetRefusals(t *testing.T) {
	ctx := context.Background()
	// Nothing listens on port 1, and nothing below gets as far as dialing.
	n := newWebJSNet(webJSNetTestURL(t, "http://127.0.0.1:1/"), time.Time{})
	refused := []webJSRequest{
		{URL: "ftp://example.com/file"},
		{URL: "javascript:alert(1)"},
		{URL: "file:///etc/passwd"},
		{URL: "blob:http://127.0.0.1:1/abc"},
		{URL: "/relative"},
		{URL: "http://user:pw@127.0.0.1:1/"},
		{URL: "http://127.0.0.1:1/", Method: "TRACE"},
		{URL: "http://127.0.0.1:1/", Method: "CONNECT"},
		{URL: "http://127.0.0.1:1/", Method: "BAD METHOD"},
		{URL: "http://127.0.0.1:1/", Body: []byte("x")},
		{URL: "http://127.0.0.1:1/", Method: "HEAD", Body: []byte("x")},
		{URL: "http://127.0.0.1:1/", Method: "PUT", Mode: "no-cors"},
		{URL: "http://127.0.0.1:1/", Mode: "no-cors", Redirect: "manual"},
		{URL: "http://127.0.0.1:2/", Mode: "same-origin"},
		{URL: "http://127.0.0.1:1/", Method: "POST", Body: make([]byte, webJSNetMaxUpload+1)},
		{URL: "data:text/plain"},
	}
	for _, req := range refused {
		if resp := n.do(ctx, req); resp.Err == "" {
			t.Errorf("%s %s was not refused: %+v", req.Method, req.URL, resp)
		}
	}
	if requests, _, _ := n.stats(); requests != 0 {
		t.Errorf("refused requests counted %d sent", requests)
	}
	if resp := n.do(ctx, webJSRequest{URL: "https://doubleclick.net/x"}); resp.Err != webJSNetBlockedByFilter {
		t.Errorf("an ad host: Err = %q", resp.Err)
	}
	if _, _, blocked := n.stats(); blocked != 1 {
		t.Errorf("blocked = %d after one filtered request", blocked)
	}

	// A public page reaches neither this device nor the LAN, whatever the
	// address looks like. (A plain-http page: an https one would refuse
	// these as mixed content before the guard is asked.)
	public := newWebJSNet(webJSNetTestURL(t, "http://news.example/"), time.Time{})
	private := []string{
		"http://127.0.0.1:1/",
		"http://localhost/",
		"http://[::1]/",
		"http://192.168.1.1/",
		"http://10.0.0.1:8080/api",
		"http://169.254.169.254/latest/meta-data/",
		"http://router.lan/",
		"http://nas/",
		"http://2130706433/",
		"http://0x7f.1/",
	}
	for _, target := range private {
		if resp := public.do(ctx, webJSRequest{URL: target}); resp.Err != webJSNetBlockedPrivate {
			t.Errorf("a public page fetched %s: Err = %q", target, resp.Err)
		}
	}
	if requests, _, blocked := public.stats(); requests != 0 || blocked != len(private) {
		t.Errorf("public page stats: %d requests, %d blocked; want 0, %d", requests, blocked, len(private))
	}
	lan := newWebJSNet(webJSNetTestURL(t, "http://192.168.1.10/"), time.Time{})
	if resp := lan.do(ctx, webJSRequest{URL: "http://127.0.0.1:1/"}); resp.Err != webJSNetBlockedPrivate {
		t.Errorf("a LAN page reached this device: Err = %q", resp.Err)
	}

	late := newWebJSNet(webJSNetTestURL(t, "http://127.0.0.1:1/"), time.Now().Add(-time.Second))
	if resp := late.do(ctx, webJSRequest{URL: "http://127.0.0.1:1/"}); resp.Err == "" {
		t.Error("a request after the pass deadline was sent")
	}
	full := newWebJSNet(webJSNetTestURL(t, "http://127.0.0.1:1/"), time.Time{})
	full.requests = webJSNetMaxRequests
	if resp := full.do(ctx, webJSRequest{URL: "http://127.0.0.1:1/"}); resp.Err != "too many requests from this page" {
		t.Errorf("past the request limit: Err = %q", resp.Err)
	}
}

func TestWebJSNetConcurrentRequests(t *testing.T) {
	var cur, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := cur.Add(1)
		for {
			p := peak.Load()
			if now <= p || peak.CompareAndSwap(p, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	n := newWebJSNet(webJSNetTestURL(t, srv.URL+"/"), time.Time{})
	const total = 20
	var wg sync.WaitGroup
	failures := make(chan string, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := n.do(context.Background(), webJSRequest{URL: srv.URL + "/"})
			if resp.Err != "" || string(resp.Body) != "ok" {
				failures <- resp.Err
			}
		}()
	}
	wg.Wait()
	close(failures)
	for why := range failures {
		t.Errorf("a parallel request failed: %q", why)
	}
	if p := peak.Load(); p > webJSNetMaxParallel {
		t.Errorf("%d requests were on the wire at once, limit %d", p, webJSNetMaxParallel)
	}
	if requests, size, _ := n.stats(); requests != total || size != 2*total {
		t.Errorf("stats = %d requests, %d bytes; want %d, %d", requests, size, total, 2*total)
	}
}

// TestWebJSNetQueueIsBounded: with every connection busy, a request past the
// page's budget fails at once instead of queueing until its deadline, so a
// script calling fetch() in a loop cannot park a goroutine per call.
func TestWebJSNetQueueIsBounded(t *testing.T) {
	n := newWebJSNet(webJSNetTestURL(t, "http://127.0.0.1:1/"), time.Time{})
	for i := 0; i < webJSNetMaxParallel; i++ {
		n.sem <- struct{}{}
	}
	n.requests = webJSNetMaxRequests - 1
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if resp := n.do(short, webJSRequest{URL: "http://127.0.0.1:1/"}); resp.Err != "timed out waiting for a connection" {
		t.Errorf("a queued request: Err = %q, want a timeout", resp.Err)
	}
	if requests, _, _ := n.stats(); requests != webJSNetMaxRequests {
		t.Errorf("a queued request counted %d requests, want %d", requests, webJSNetMaxRequests)
	}
	long, cancelLong := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLong()
	start := time.Now()
	if resp := n.do(long, webJSRequest{URL: "http://127.0.0.1:1/"}); resp.Err != "too many requests from this page" {
		t.Errorf("past the budget: Err = %q, want too many requests", resp.Err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("a request past the budget waited %v for a connection", waited)
	}
}

func TestWebJSNetFetchScript(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same.js":
			w.Header().Set("Content-Type", "text/javascript")
			io.WriteString(w, "var same = 1;")
		case "/page.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<!doctype html><p>not a script")
		default:
			http.NotFound(w, r)
		}
	}))
	defer page.Close()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lib.js":
			w.Header().Set("Content-Type", "application/javascript")
			io.WriteString(w, "var lib = 2;")
		case "/mod.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			io.WriteString(w, "export const m = 3;")
		case "/data.json":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			io.WriteString(w, `{"secret":true}`)
		}
	}))
	defer cdn.Close()
	n := newWebJSNet(webJSNetTestURL(t, page.URL+"/"), time.Time{})
	ctx := context.Background()

	if resp := n.fetchScript(ctx, page.URL+"/same.js", false); resp.Err != "" || resp.Type != "basic" || string(resp.Body) != "var same = 1;" {
		t.Errorf("same-origin script = %+v", resp)
	}
	resp := n.fetchScript(ctx, cdn.URL+"/lib.js", false)
	if resp.Err != "" || resp.Type != "opaque" || resp.Status != 200 || string(resp.Body) != "var lib = 2;" {
		t.Errorf("cross-origin classic script = %+v, want it to run but stay opaque", resp)
	}
	// The runtime still gets the headers it decodes the script by.
	if v, _ := webJSNetTestHeader(resp, "content-type"); v != "application/javascript" {
		t.Errorf("cross-origin script content-type = %q", v)
	}
	if resp := n.fetchScript(ctx, cdn.URL+"/lib.js", true); resp.Err != "CORS" {
		t.Errorf("unshared module: Err = %q, want CORS", resp.Err)
	}
	if resp := n.fetchScript(ctx, cdn.URL+"/mod.js", true); resp.Err != "" || resp.Type != "cors" || string(resp.Body) != "export const m = 3;" {
		t.Errorf("shared module = %+v", resp)
	}
	if resp := n.fetchScript(ctx, page.URL+"/page.html", false); resp.Err == "" {
		t.Error("an HTML page was accepted as a script")
	}
	if resp := n.fetchScript(ctx, page.URL+"/missing.js", false); resp.Err != "HTTP 404" {
		t.Errorf("a missing script: Err = %q, want HTTP 404", resp.Err)
	}
	if resp := n.fetchScript(ctx, cdn.URL+"/data.json", false); resp.Err == "" {
		t.Error("a nosniff JSON answer was accepted as a script")
	}
	if resp := n.fetchScript(ctx, "data:text/javascript,var%20y%3D2", false); resp.Err != "" || resp.Type != "basic" || string(resp.Body) != "var y=2" {
		t.Errorf("data: script = %+v", resp)
	}
	if resp := n.fetchScript(ctx, "data:text/html,<p>x", false); resp.Err == "" {
		t.Error("a data: HTML document was accepted as a script")
	}
}
