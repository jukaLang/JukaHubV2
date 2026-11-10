package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// webJSTestServer serves path → {content type, body} and returns the address of
// a page on it. httptest listens on loopback, which the page-script network
// layer allows for a loopback page.
func webJSTestServer(t *testing.T, routes map[string][2]string) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := routes[r.URL.RequestURI()]
		if !ok {
			route, ok = routes[r.URL.Path]
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", route[0])
		_, _ = w.Write([]byte(route[1]))
	}))
	t.Cleanup(srv.Close)
	page, err := url.Parse(srv.URL + "/page.html")
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// webJSTestPage is a page address that needs no server: for pages whose
// scripts make no requests.
func webJSTestPage(t *testing.T) *url.URL {
	t.Helper()
	page, err := url.Parse("https://example.test/dir/page.html")
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func webJSRun(t *testing.T, html string, page *url.URL) webJSPageResult {
	t.Helper()
	start := time.Now()
	res := webJSRunPage(html, page)
	if !res.Ran {
		t.Fatalf("the DOM pass did not run: %s", res.Reason)
	}
	if took := time.Since(start); took > webJSBudget+webJSSerializeGrace+2*time.Second {
		t.Fatalf("the pass took %s", took)
	}
	return res
}

func webJSWant(t *testing.T, html string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(html, want) {
			t.Errorf("result lacks %q:\n%s", want, html)
		}
	}
}

// The embedded prelude must compile in goja itself, not only in node: a
// syntax goja lacks would leave every page on the legacy pass.
func TestWebDOMPreludeCompilesInGoja(t *testing.T) {
	prog, err := webDOMPrelude()
	if err != nil || prog == nil {
		t.Fatalf("prelude does not compile: %v", err)
	}
	src, err := webDOMPreludeSource()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(src, "(function (global, N) { 'use strict';") {
		t.Fatalf("prelude wrapper changed: %.60q", src)
	}
}

func TestWebJSRunPageBuildsContentFromScript(t *testing.T) {
	res := webJSRun(t, `<!doctype html><html><head><title>t</title></head><body><div id="app">Loading</div>
<script>
var items = ['alpha', 'beta', 'gamma'];
document.getElementById('app').innerHTML = '<ul class="list">' + items.map(function (s, i) {
  return '<li data-i="' + i + '">' + s + '</li>';
}).join('') + '</ul>';
</script></body></html>`, webJSTestPage(t))
	webJSWant(t, res.HTML, `<ul class="list"><li data-i="0">alpha</li>`, `<li data-i="2">gamma</li></ul>`)
	if strings.Contains(res.HTML, "Loading") {
		t.Errorf("the placeholder survived:\n%s", res.HTML)
	}
	if strings.Contains(res.HTML, "items.map") {
		t.Errorf("a script body reached the renderer:\n%s", res.HTML)
	}
}

func TestWebJSRunPageDocumentWriteLandsWhereTheScriptWas(t *testing.T) {
	res := webJSRun(t, `<body><p id="a">a</p><script>document.write('<p id="w">' + document.getElementById('a').textContent + '-written</p>');</script><p>z</p></body>`, webJSTestPage(t))
	webJSWant(t, res.HTML, `<p id="w">a-written</p>`)
	if w, z := strings.Index(res.HTML, `id="w"`), strings.Index(res.HTML, `<p>z</p>`); w < 0 || z < 0 || w > z {
		t.Errorf("written markup is not before the next paragraph:\n%s", res.HTML)
	}
}

func TestWebJSRunPageFetchesJSONIntoTheDocument(t *testing.T) {
	page := webJSTestServer(t, map[string][2]string{
		"/api/items.json": {"application/json", `[{"name":"one"},{"name":"two"}]`},
	})
	res := webJSRun(t, `<body><ul id="list"></ul><script>
fetch('/api/items.json').then(function (r) { return r.json(); }).then(function (items) {
  items.forEach(function (it) {
    var li = document.createElement('li');
    li.textContent = it.name;
    document.getElementById('list').appendChild(li);
  });
});
</script></body>`, page)
	webJSWant(t, res.HTML, `<ul id="list"><li>one</li><li>two</li></ul>`)
}

func TestWebJSRunPageRunsExternalScriptsAndFastForwardsTimers(t *testing.T) {
	page := webJSTestServer(t, map[string][2]string{
		"/app.js": {"application/javascript", `window.parts = ['app'];
setTimeout(function () { parts.push('late'); document.body.setAttribute('data-parts', parts.join(',')); }, 5000);`},
	})
	start := time.Now()
	res := webJSRun(t, `<body><script src="/app.js"></script><script>parts.push('inline');</script></body>`, page)
	webJSWant(t, res.HTML, `data-parts="app,inline,late"`)
	// The five-second timer runs on the virtual clock: nothing is on the wire,
	// so the loop skips ahead instead of waiting.
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("the timer was waited for in real time (%s)", took)
	}
}

func TestWebJSRunPageLoadsAModuleGraph(t *testing.T) {
	page := webJSTestServer(t, map[string][2]string{
		"/js/main.js":   {"text/javascript", "import { greet } from './lib.js';\nimport config from './config.js';\ndocument.getElementById('out').textContent = greet(config.name);\n"},
		"/js/lib.js":    {"text/javascript", "export function greet(name) { return 'Hello, ' + name + '!'; }\n"},
		"/js/config.js": {"text/javascript", "export default { name: 'JukaHub' };\n"},
	})
	res := webJSRun(t, `<body><p id="out"></p><script type="module" src="/js/main.js"></script></body>`, page)
	webJSWant(t, res.HTML, `<p id="out">Hello, JukaHub!</p>`)
}

func TestWebJSRunPageReportsANavigation(t *testing.T) {
	res := webJSRun(t, `<body><script>location.replace('/next?from=start');</script></body>`, webJSTestPage(t))
	if res.Navigate != "https://example.test/next?from=start" {
		t.Fatalf("Navigate = %q", res.Navigate)
	}
}

func TestWebJSRunPageIgnoresAFragmentOnlyNavigation(t *testing.T) {
	res := webJSRun(t, `<body><script>location.hash = 'top'; location.href = location.href;</script></body>`, webJSTestPage(t))
	if res.Navigate != "" {
		t.Fatalf("Navigate = %q, want none", res.Navigate)
	}
}

// A public page may not send the reader, unasked, to this device or the LAN.
func TestWebJSRunPageRefusesANavigationIntoTheLAN(t *testing.T) {
	for _, target := range []string{"http://192.168.1.1/admin", "http://localhost:8080/", "http://router.lan/"} {
		res := webJSRun(t, `<body><script>location.href = '`+target+`';</script></body>`, webJSTestPage(t))
		if res.Navigate != "" {
			t.Errorf("a public page navigated to %s (Navigate = %q)", target, res.Navigate)
		}
	}
}

// A load the reader left before its scripts ran keeps its markup; the newer
// load runs.
func TestWebJSRunPageSkipsALoadTheReaderLeft(t *testing.T) {
	src := `<body><script>document.write('<p>x</p>')</script></body>`
	gen := webJSLoadGeneration()
	webJSStopRunning()
	res := webJSRunPageFor(src, webJSTestPage(t), gen, time.Time{})
	if res.Ran || res.HTML != src {
		t.Fatalf("a superseded load ran its scripts (ran=%v, reason %q)", res.Ran, res.Reason)
	}
	res = webJSRunPageFor(src, webJSTestPage(t), webJSLoadGeneration(), time.Time{})
	if !res.Ran {
		t.Fatalf("the current load did not run: %s", res.Reason)
	}
}

// A script navigation's next page only gets what is left of the load's time.
func TestWebJSRunPageNeedsTimeForAHop(t *testing.T) {
	src := `<body><script>document.write('<p>x</p>')</script></body>`
	res := webJSRunPageFor(src, webJSTestPage(t), webJSLoadGeneration(), time.Now().Add(time.Second))
	if res.Ran || res.HTML != src {
		t.Fatalf("a pass started with a second left (ran=%v, reason %q)", res.Ran, res.Reason)
	}
}

// A request that never answers (a long-poll) holds the loop only for the
// fetch patience: load still fires and the page comes back long before the
// budget.
func TestWebJSRunPageDoesNotWaitForALongPoll(t *testing.T) {
	prev := webJSFetchPatience
	webJSFetchPatience = 300 * time.Millisecond
	t.Cleanup(func() { webJSFetchPatience = prev })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/poll" {
			http.NotFound(w, r)
			return
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	// Cleanups run last-in first-out: the handler is released before Close
	// waits for it.
	t.Cleanup(func() { close(release) })
	page, err := url.Parse(srv.URL + "/page.html")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res := webJSRun(t, `<body><script>fetch('/poll');
addEventListener('load', function () { document.body.setAttribute('data-loaded', '1'); });</script></body>`, page)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the long-poll held the page for %s", took)
	}
	webJSWant(t, res.HTML, `data-loaded="1"`)
}

// The message a page's onerror sees is "Name: message", without goja's
// " at …" suffix, and a value whose toString throws cannot break the report.
func TestWebJSRunScriptErrorDescriptor(t *testing.T) {
	s := &webJSSession{rt: goja.New()}
	run := func(src string) *goja.Object {
		t.Helper()
		v := s.nRunScript(goja.FunctionCall{Arguments: []goja.Value{s.rt.ToValue(src), s.rt.ToValue("https://example.test/app.js")}})
		desc, ok := v.(*goja.Object)
		if !ok || !desc.Get("jhError").ToBoolean() {
			t.Fatalf("%q returned no error descriptor: %v", src, v)
		}
		return desc
	}
	desc := run("var a = 1;\nnull.x;")
	if msg := desc.Get("message").String(); !strings.HasPrefix(msg, "TypeError: ") || strings.Contains(msg, " at ") {
		t.Errorf("message = %q", msg)
	}
	if line := desc.Get("line"); line == nil || line.ToInteger() != 2 {
		t.Errorf("line = %v, want 2", line)
	}
	for src, want := range map[string]string{
		"throw new RangeError('too far')": "RangeError: too far",
		"throw 'plain'":                   "plain",
		"throw 42":                        "42",
		"throw {toString: function () { throw new Error('nope'); }}": "Script error.",
	} {
		if got := run(src).Get("message").String(); got != want {
			t.Errorf("%q reported %q, want %q", src, got, want)
		}
	}
}

func TestWebJSRunPageStopsARunawayScript(t *testing.T) {
	prevCall, prevBudget := webJSCallTimeout, webJSBudget
	webJSCallTimeout, webJSBudget = 300*time.Millisecond, 3*time.Second
	t.Cleanup(func() { webJSCallTimeout, webJSBudget = prevCall, prevBudget })
	start := time.Now()
	res := webJSRunPage(`<body><p id="p">x</p><script>while (true) {}</script><script>document.getElementById('p').textContent = 'after';</script></body>`, webJSTestPage(t))
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Fatalf("the runaway script held the page for %s", took)
	}
	if !res.Ran {
		t.Fatalf("the DOM pass did not run: %s", res.Reason)
	}
	webJSWant(t, res.HTML, `<p id="p">after</p>`)
}

func TestWebJSRunPageLeavesScriptlessPagesExactlyAlone(t *testing.T) {
	src := `<html><body><p>plain</p><script type="application/ld+json">{"a":1}</script></body></html>`
	res := webJSRunPage(src, webJSTestPage(t))
	if res.Ran || res.HTML != src {
		t.Fatalf("a page with nothing to run was rewritten (ran=%v):\n%s", res.Ran, res.HTML)
	}
}

func TestWebJSRunPageHonoursTheJavaScriptSetting(t *testing.T) {
	browserJavaScriptOff.Store(true)
	t.Cleanup(func() { browserJavaScriptOff.Store(false) })
	src := `<body><script>document.write('<p>x</p>')</script></body>`
	res := webJSRunPage(src, webJSTestPage(t))
	if res.Ran || res.HTML != src {
		t.Fatalf("JavaScript off still ran scripts (ran=%v):\n%s", res.Ran, res.HTML)
	}
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{"BrowserJavaScript": "false"}
	if browserJavaScriptSetting(c) {
		t.Error(`a capitalised "false" did not turn JavaScript off`)
	}
	c.Variables.Custom = map[string]interface{}{browserJavaScriptKey: true}
	if !browserJavaScriptSetting(c) {
		t.Error("true did not keep JavaScript on")
	}
	if !browserJavaScriptSetting(nil) {
		t.Error("JavaScript must default to on")
	}
}

func TestWebJSRunPageKeepsTheTextWhenScriptsWipeIt(t *testing.T) {
	src := `<body><article>` + strings.Repeat("Readable article text. ", 200) +
		`</article><script>document.body.innerHTML = '<div id="spinner">Loading</div>';</script></body>`
	res := webJSRunPage(src, webJSTestPage(t))
	if res.HTML != src {
		t.Fatalf("a script that emptied the page replaced it (reason %q)", res.Reason)
	}
}

func TestWebJSURLParse(t *testing.T) {
	cases := []struct {
		input, base, href, host, origin string
	}{
		{"../a/b?x=1#h", "https://Example.COM:443/dir/page.html", "https://example.com/a/b?x=1#h", "example.com", "https://example.com"},
		{"//cdn.test:8080/x.js", "http://site.test/", "http://cdn.test:8080/x.js", "cdn.test:8080", "http://cdn.test:8080"},
		{"https://h.test", "", "https://h.test/", "h.test", "https://h.test"},
		{"mailto:someone@example.com", "", "mailto:someone@example.com", "", "null"},
		// A '%' that starts no escape: kept as %25 in the path and fragment,
		// left alone in the query, where Go keeps the query raw.
		{"/100%/x", "https://example.test/", "https://example.test/100%25/x", "example.test", "https://example.test"},
		{"/a?q=50%", "https://example.test/", "https://example.test/a?q=50%", "example.test", "https://example.test"},
		{"#50%", "https://example.test/p", "https://example.test/p#50%25", "example.test", "https://example.test"},
	}
	for _, c := range cases {
		raw := webJSURLParse(c.input, c.base)
		var parts map[string]string
		if err := json.Unmarshal([]byte(raw), &parts); err != nil {
			t.Fatalf("%q against %q: %v (%q)", c.input, c.base, err, raw)
		}
		if parts["href"] != c.href || parts["host"] != c.host || parts["origin"] != c.origin {
			t.Errorf("%q against %q = %v", c.input, c.base, parts)
		}
	}
	for _, bad := range []string{"not a url", "http://"} {
		if got := webJSURLParse(bad, ""); got != "" {
			t.Errorf("%q parsed as %s", bad, got)
		}
	}
}

func TestWebJSDecodeText(t *testing.T) {
	if got, _ := webJSDecodeText([]byte("\xEF\xBB\xBFhi"), "utf-8", false, false); got != "hi" {
		t.Errorf("BOM not dropped: %q", got)
	}
	if got, _ := webJSDecodeText([]byte{0x80, 'a'}, "windows-1252", false, false); got != "€a" {
		t.Errorf("windows-1252 = %q", got)
	}
	if got, _ := webJSDecodeText([]byte{'h', 0, 'i', 0}, "utf-16le", false, false); got != "hi" {
		t.Errorf("utf-16le = %q", got)
	}
	if _, ok := webJSDecodeText([]byte{0xFF}, "utf-8", false, true); ok {
		t.Error("fatal decoding accepted invalid UTF-8")
	}
	if got, _ := webJSDecodeText([]byte{0xFF, 'a'}, "", false, false); got != "\uFFFDa" {
		t.Errorf("replacement = %q", got)
	}
}

func TestWebJSAtob(t *testing.T) {
	for in, want := range map[string]string{"aGk=": "hi", "aGk": "hi", " a G k = ": "hi", "": ""} {
		got, ok := webJSAtob(in)
		if !ok || string(got) != want {
			t.Errorf("atob(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"a", "a===", "*", "aG=k"} {
		if _, ok := webJSAtob(bad); ok {
			t.Errorf("atob(%q) accepted", bad)
		}
	}
}

func TestWebJSPageNeedsDOM(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{`<p>no scripts</p>`, false},
		{`<script type="application/ld+json">{}</script>`, false},
		{`<script>var a</script>`, true},
		{`<SCRIPT TYPE="text/javascript">x</SCRIPT>`, true},
		{`<script type="module" src="/a.js"></script>`, true},
		{`<script-card>no</script-card>`, false},
		{`<img data-src="/a.jpg" src="blank.gif">`, true},
		{`<template shadowrootmode="open"><p>x</p></template>`, true},
	}
	for _, c := range cases {
		if got := webJSPageNeedsDOM(c.src); got != c.want {
			t.Errorf("webJSPageNeedsDOM(%q) = %v", c.src, got)
		}
	}
}

func TestWebJSStorageQuota(t *testing.T) {
	key := webJSStorageKey("local", webJSTestPage(t))
	if !webJSStorageSet(key, `{"a":"1"}`) || webJSStorageGet(key) != `{"a":"1"}` {
		t.Fatal("a small store did not round-trip")
	}
	if webJSStorageSet(key, strings.Repeat("x", webJSStorageMaxBytes+1)) {
		t.Fatal("a store over the quota was accepted")
	}
	if !webJSStorageSet(key, "{}") || webJSStorageGet(key) != "" {
		t.Fatal("an empty store was kept")
	}
}
