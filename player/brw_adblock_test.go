package main

// brw_adblock_test.go — the broader filter must stay a filter for a reader:
// suffixes match on dot boundaries only, a site's own files load on its own
// pages, the path hints still apply there, and the switch and the per-site
// exception both let everything through.

import (
	"strings"
	"testing"
)

func TestBrowserAdHostsMoreIsClean(t *testing.T) {
	seen := map[string]bool{}
	var hostOnly []string
	for _, entry := range browserAdHosts {
		if !strings.Contains(entry, "/") {
			hostOnly = append(hostOnly, entry)
		}
	}
	for _, entry := range browserAdHostsMore {
		if entry != strings.ToLower(strings.TrimSpace(entry)) || entry == "" {
			t.Errorf("entry %q is not a trimmed lower-case host", entry)
		}
		if strings.ContainsAny(entry, "/:*? ") || strings.HasPrefix(entry, ".") || strings.HasSuffix(entry, ".") || !strings.Contains(entry, ".") {
			t.Errorf("entry %q is not a plain host suffix", entry)
		}
		if seen[entry] {
			t.Errorf("entry %q is listed twice", entry)
		}
		seen[entry] = true
		hostOnly = append(hostOnly, entry)
	}
	// No entry may be a subdomain of another one: it would never decide
	// anything and only makes the list longer to read.
	for _, entry := range browserAdHostsMore {
		for _, other := range hostOnly {
			if other != entry && strings.HasSuffix(entry, "."+other) {
				t.Errorf("entry %q is already covered by %q", entry, other)
			}
		}
	}
	if len(browserAdHostsMore) < 150 {
		t.Errorf("the curated list has %d entries, want at least 150", len(browserAdHostsMore))
	}
}

func TestBrowserAdHostListedMatchesOnDotBoundaries(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"doubleclick.net", true},
		{"ads.g.doubleclick.net", true},
		{"STATS.G.DOUBLECLICK.NET.", true},
		{"notdoubleclick.net", false},
		{"doubleclick.net.example.com", false},
		{"imasdk.googleapis.com", true},
		{"fonts.googleapis.com", false},
		{"cdn.taboola.com", true},
		{"mc.yandex.ru", true},
		{"yandex.ru", false},
		{"x.adskeeper.co.uk", true},
		{"co.uk", false},
		{"example.com", false},
		{"", false},
		{"localhost", false},
		{"::1", false},
	}
	for _, c := range cases {
		if got := browserAdHostListed(c.host); got != c.want {
			t.Errorf("browserAdHostListed(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestBrwRegistrableDomain(t *testing.T) {
	cases := [][2]string{
		{"www.example.com", "example.com"},
		{"example.com", "example.com"},
		{"a.b.news.example.co.uk", "example.co.uk"},
		{"user.github.io", "user.github.io"},
		{"com", "com"},
		{"192.168.1.4", "192.168.1.4"},
		{"Static.Criteo.NET.", "criteo.net"},
		{"", ""},
	}
	for _, c := range cases {
		if got := brwRegistrableDomain(c[0]); got != c[1] {
			t.Errorf("brwRegistrableDomain(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestBrwAdBlocksFirstPartyRule(t *testing.T) {
	cases := []struct {
		name, page, request string
		want                bool
	}{
		{"ad network on a news page", "https://news.example.com/story", "https://securepubads.g.doubleclick.net/tag/js/gpt.js", true},
		{"new list on a news page", "https://news.example.com/story", "https://cdn.taboolasyndication.com/libtrc/x.js", true},
		{"the ad company's own site", "https://www.criteo.com/about/", "https://static.criteo.com/images/logo.png", false},
		{"its own analytics on its own site", "https://twitter.com/home", "https://analytics.twitter.com/i/adsct", false},
		{"a sister domain is not the same site", "https://www.facebook.com/", "https://connect.facebook.net/en_US/sdk.js", true},
		{"path hints still apply first-party", "https://www.example.com/", "https://www.example.com/ads/banner.png", true},
		{"path hints apply on a listed site too", "https://www.criteo.com/", "https://www.criteo.com/pixel.gif", true},
		{"an ordinary first-party file", "https://www.example.com/", "https://www.example.com/static/app.js", false},
		{"an unknown page blocks the listed host", "", "https://www.google-analytics.com/analytics.js", true},
		{"not an ad at all", "https://news.example.com/", "https://images.example-cdn.com/photo.jpg", false},
	}
	for _, c := range cases {
		if got := brwAdBlocks(c.page, c.request); got != c.want {
			t.Errorf("%s: brwAdBlocks(%q, %q) = %v, want %v", c.name, c.page, c.request, got, c.want)
		}
	}
}

func TestBrowserRequestBlockedHonoursSwitchAndSiteException(t *testing.T) {
	page := "https://news.example.com/story"
	resetBrowserAdBlockSitesForTest()
	prev := browserBlockAdsOff.Load()
	t.Cleanup(func() {
		// The exception is written to the test's data file: take it out again
		// so no later test starts with it.
		_ = setBrowserAdBlockOff(page, false)
		resetBrowserAdBlockSitesForTest()
		browserBlockAdsOff.Store(prev)
	})
	browserBlockAdsOff.Store(false)

	ad := "https://ads.pubmatic.com/AdServer/js/pwt.js"
	if !browserRequestBlocked(page, ad) {
		t.Fatalf("an ad host on a news page was not blocked")
	}
	if browserRequestBlocked(page, "https://news.example.com/static/site.css") {
		t.Fatalf("the page's own stylesheet was blocked")
	}

	browserBlockAdsOff.Store(true)
	if browserRequestBlocked(page, ad) {
		t.Fatalf("the switch is off but the ad host was still blocked")
	}
	if !browserAdBlockOff(page) {
		t.Fatalf("browserAdBlockOff should report the switch")
	}
	if browserAdBlockSiteAllowed(page) {
		t.Fatalf("the switch must not read as a per-site exception")
	}
	browserBlockAdsOff.Store(false)

	if err := setBrowserAdBlockOff(page, true); err != nil {
		t.Fatalf("setBrowserAdBlockOff: %v", err)
	}
	if !browserAdBlockSiteAllowed("https://www.news.example.com/other") {
		t.Fatalf("the exception did not cover the www spelling of the site")
	}
	if browserRequestBlocked(page, ad) {
		t.Fatalf("ads were allowed on the site but the request was still blocked")
	}
	if !browserRequestBlocked("https://other.example.org/", ad) {
		t.Fatalf("an exception for one site leaked to another")
	}
}

func TestBrowserBlockAdsSettingDefaultsOn(t *testing.T) {
	if !browserBlockAdsSetting(nil) {
		t.Fatalf("a missing config must leave the filter on")
	}
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{}
	if !browserBlockAdsSetting(c) {
		t.Fatalf("an unset key must leave the filter on")
	}
	c.Variables.Custom[browserBlockAdsKey] = "false"
	if browserBlockAdsSetting(c) {
		t.Fatalf(`"false" must turn the filter off`)
	}
	c.Variables.Custom[browserBlockAdsKey] = true
	if !browserBlockAdsSetting(c) {
		t.Fatalf("a bool true must keep the filter on")
	}
}

func TestBrwAdBlockedLine(t *testing.T) {
	cases := []struct {
		blocked     int
		allowed, on bool
		want        string
	}{
		{14, false, true, "14 ads and trackers blocked"},
		{1, false, true, "1 ad or tracker blocked"},
		{0, false, true, "No ads or trackers found on this page"},
		{9, true, true, "Ads and trackers allowed on this site"},
		{9, false, false, "Ads and trackers are not blocked · Settings → Browser"},
	}
	for _, c := range cases {
		if got := brwAdBlockedLine(c.blocked, c.allowed, c.on); got != c.want {
			t.Errorf("brwAdBlockedLine(%d, %v, %v) = %q, want %q", c.blocked, c.allowed, c.on, got, c.want)
		}
	}
}
