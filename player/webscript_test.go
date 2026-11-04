package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRunWebPageScriptsInlineWritesReachPage(t *testing.T) {
	src := `<html><body><script>document.write("<p>Written by script</p>");</script></body></html>`
	base, _ := url.Parse("http://example.test/")
	out := runWebPageScripts(src, base)
	if !strings.Contains(out, "<p>Written by script</p>") {
		t.Fatalf("document.write output missing from page: %s", out)
	}
	if strings.Contains(out, "<script>") {
		t.Errorf("script tag should be replaced by its output: %s", out)
	}
}

func TestRunWebPageScriptsFetchesExternalScript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app.js" {
			w.Header().Set("Content-Type", "application/javascript")
			io.WriteString(w, `document.write("<b>External ran</b>");`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	base, _ := url.Parse(server.URL + "/")
	src := `<body><script src="/app.js"></script></body>`
	out := runWebPageScripts(src, base)
	if !strings.Contains(out, "<b>External ran</b>") {
		t.Fatalf("external script output missing: %s", out)
	}
}

func TestBrowseWebPageRunsScriptsBeforeRendering(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><head><title>Script Page</title></head><body><main><h1>Static heading</h1><script>document.write("<p>Script paragraph</p>");</script></main></body></html>`)
	}))
	defer server.Close()

	page := browseWebPageResult(server.URL)
	if !strings.Contains(page.Text, "Script paragraph") {
		t.Errorf("script output missing from plain text: %q", page.Text)
	}
	if page.Doc == nil {
		t.Fatal("page did not produce a rendered document")
	}
	found := false
	for _, block := range page.Doc.Blocks {
		for _, run := range block.Runs {
			if strings.Contains(run.Text, "Script paragraph") {
				found = true
			}
		}
	}
	if !found {
		t.Error("script output missing from the rendered document")
	}
}

func TestRunWebPageScriptsTimeoutDoesNotHang(t *testing.T) {
	src := `<body><script>while (true) {}</script><p>After loop</p></body>`
	base, _ := url.Parse("http://example.test/")
	start := time.Now()
	out := runWebPageScripts(src, base)
	elapsed := time.Since(start)
	if elapsed > webScriptBudget+3*time.Second {
		t.Fatalf("runaway script held the page for %s", elapsed)
	}
	if !strings.Contains(out, "After loop") {
		t.Errorf("page content after a stopped script was lost: %s", out)
	}
}

// A script cannot grow the page without bound: document.write output stops at
// webScriptMaxOutput, and the page after the script survives.
func TestRunWebPageScriptsCapsDocumentWrite(t *testing.T) {
	src := `<body><script>var s = "x"; for (var i = 0; i < 16; i++) { s = s + s; } ` +
		`while (true) { document.write(s); }</script><p>After writes</p></body>`
	base, _ := url.Parse("http://example.test/")
	out := runWebPageScripts(src, base)
	if len(out) > webScriptMaxOutput+len(src) {
		t.Fatalf("page grew to %d bytes; document.write is capped at %d", len(out), webScriptMaxOutput)
	}
	if !strings.Contains(out, "After writes") {
		t.Errorf("page content after a capped script was lost")
	}
}

// Runaway recursion stops at the stack cap instead of growing the heap.
func TestRunWebPageScriptsStopsDeepRecursion(t *testing.T) {
	src := `<body><script>function f(n) { return f(n + 1) + 1; } f(0);</script><p>After recursion</p></body>`
	base, _ := url.Parse("http://example.test/")
	start := time.Now()
	out := runWebPageScripts(src, base)
	if time.Since(start) > webScriptRunTimeout+time.Second {
		t.Fatalf("recursion ran for %s; the stack cap should stop it at once", time.Since(start))
	}
	if !strings.Contains(out, "After recursion") {
		t.Errorf("page content after the recursion was lost")
	}
}
