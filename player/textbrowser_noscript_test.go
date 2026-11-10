package main

import (
	"net/url"
	"strings"
	"testing"
)

// A <meta> refresh inside <noscript> is for browsers that run no JavaScript.
// With page scripts on, the reader is not one of them and loads the page itself
// (Google's "turn on JavaScript" redirect is the best-known case); with them
// off it follows the refresh as before.
func TestBrowserMetaRefreshSkipsNoscriptWithScriptsOn(t *testing.T) {
	wasOff := browserJavaScriptOff.Load()
	t.Cleanup(func() { browserJavaScriptOff.Store(wasOff) })
	base, err := url.Parse("https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	noscript := `<html><head><noscript><meta http-equiv="refresh" content="0; url=/nojs"></noscript></head><body>x</body></html>`
	both := `<html><head><noscript><meta http-equiv="refresh" content="0; url=/nojs"></noscript>` +
		`<meta http-equiv="refresh" content="0; url=/moved"></head></html>`
	plain := `<meta http-equiv="refresh" content="0; url=/moved">`

	browserJavaScriptOff.Store(false)
	if got := browserMetaRefreshTarget(noscript, base); got != "" {
		t.Errorf("scripts on: the <noscript> refresh was followed to %q", got)
	}
	if got := browserMetaRefreshTarget(both, base); got != "https://example.com/moved" {
		t.Errorf("scripts on: got %q, want the refresh outside <noscript>", got)
	}
	if got := browserMetaRefreshTarget(plain, base); got != "https://example.com/moved" {
		t.Errorf("scripts on: an ordinary refresh gave %q", got)
	}

	browserJavaScriptOff.Store(true)
	if got := browserMetaRefreshTarget(noscript, base); got != "https://example.com/nojs" {
		t.Errorf("scripts off: got %q, want the <noscript> refresh followed", got)
	}
}

func TestBrowserInsideNoscript(t *testing.T) {
	page := asciiLower(`<head><noscript><META http-equiv="refresh" content="0;url=/nojs"></noscript><meta name="x"></head><noscript>`)
	for _, tc := range []struct {
		marker string
		want   bool
	}{
		{`<meta http-equiv`, true},
		{`<meta name`, false},
		{`<head>`, false},
		{`</head>`, false},
	} {
		i := strings.Index(page, tc.marker)
		if i < 0 {
			t.Fatalf("marker %q missing from the test page", tc.marker)
		}
		if got := browserInsideNoscript(page, i); got != tc.want {
			t.Errorf("%q: inside <noscript> = %v, want %v", tc.marker, got, tc.want)
		}
	}
	// The trailing unclosed <noscript> covers everything after it, and
	// out-of-range offsets are simply outside.
	if !browserInsideNoscript(page, len(page)) {
		t.Error("the end of the page lies inside the unclosed <noscript>")
	}
	if browserInsideNoscript(page, 0) || browserInsideNoscript(page, len(page)+1) || browserInsideNoscript(page, -1) {
		t.Error("an offset outside the page reported as inside <noscript>")
	}
}
