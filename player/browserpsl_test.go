package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
)

// A page may not set a cookie for a public suffix: without a list the jar let
// evil.com set one for every .com site and any .co.uk host one for all of
// co.uk, and the browser keeps cookies on the card.
func TestBrowserJarRefusesPublicSuffixCookies(t *testing.T) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: browserPublicSuffixes{}})
	if err != nil {
		t.Fatal(err)
	}
	set := func(rawURL string, c *http.Cookie) {
		u, _ := url.Parse(rawURL)
		jar.SetCookies(u, []*http.Cookie{c})
	}
	sent := func(rawURL, name string) bool {
		u, _ := url.Parse(rawURL)
		for _, c := range jar.Cookies(u) {
			if c.Name == name {
				return true
			}
		}
		return false
	}

	set("https://evil.com/", &http.Cookie{Name: "tld", Value: "x", Domain: "com"})
	set("https://evil.co.uk/", &http.Cookie{Name: "sld", Value: "x", Domain: "co.uk"})
	set("https://evil.github.io/", &http.Cookie{Name: "host", Value: "x", Domain: "github.io"})
	set("https://www.example.com/", &http.Cookie{Name: "ok", Value: "x", Domain: "example.com"})

	if sent("https://victim.com/", "tld") {
		t.Error("a cookie set for .com reached another .com site")
	}
	if sent("https://victim.co.uk/", "sld") {
		t.Error("a cookie set for .co.uk reached another co.uk site")
	}
	if sent("https://victim.github.io/", "host") {
		t.Error("a cookie set for github.io reached another github.io site")
	}
	if !sent("https://api.example.com/", "ok") {
		t.Error("an ordinary domain cookie no longer reaches its own subdomains")
	}
	if got := (browserPublicSuffixes{}).PublicSuffix("www.bbc.co.uk"); got != "co.uk" {
		t.Errorf("PublicSuffix(www.bbc.co.uk) = %q, want co.uk", got)
	}
	if got := (browserPublicSuffixes{}).PublicSuffix("router"); got != "router" {
		t.Errorf("PublicSuffix(router) = %q, want the single label", got)
	}
}
