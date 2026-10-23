package main

// browser_cache_test.go covers the in-memory page cache and the load path that
// uses it: Back must not go back to the network, Reload must, a failed fetch
// must never be remembered, and the cache must stay inside its bounds.

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// browserCacheFixture gives a test a config and element wired to the browser
// content variable, with the network replaced by fn.
func browserCacheFixture(t *testing.T, fn func(rawURL string) browserPage) (*Config, Element, *int32) {
	t.Helper()
	resetBrowserStateForTest()
	var calls int32
	prev := browserPageFetcher
	browserPageFetcher = func(rawURL string) browserPage {
		atomic.AddInt32(&calls, 1)
		return fn(rawURL)
	}
	t.Cleanup(func() {
		browserPageFetcher = prev
		resetBrowserStateForTest()
	})
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{}
	return config, Element{Type: "textbrowser", Variable: browserContentVariable}, &calls
}

// waitBrowserText drains background publishes until the content matches.
func waitBrowserText(t *testing.T, config *Config, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		drainCustom(config)
		if text, _ := config.Variables.Custom[browserContentVariable].(string); ok(text) {
			return text
		}
		if time.Now().After(deadline) {
			text, _ := config.Variables.Custom[browserContentVariable].(string)
			t.Fatalf("page never arrived, content = %q", text)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBrowserCacheServesBackWithoutNetwork(t *testing.T) {
	config, elem, calls := browserCacheFixture(t, func(rawURL string) browserPage {
		return browserPage{
			Text:     "Title: A\nURL: " + rawURL + "\n\npage body A",
			FinalURL: rawURL,
			Links:    []browserLink{{Text: "next", URL: "https://example.com/b"}},
		}
	})

	loadTextBrowserURLAt(config, elem, "https://example.com/a", true)
	waitBrowserText(t, config, func(s string) bool { return strings.Contains(s, "page body A") })
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("first load fetched %d times, want 1", got)
	}

	// A second visit to the same address is a map lookup, and it is published
	// in the same call rather than over a goroutine: Back lands immediately.
	config.Variables.Custom[browserContentVariable] = "stale content"
	start := time.Now()
	loadTextBrowserURLAt(config, elem, "https://example.com/a", true)
	text, _ := config.Variables.Custom[browserContentVariable].(string)
	if !strings.Contains(text, "page body A") {
		t.Fatalf("cached page was not shown synchronously: %q", text)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("cache hit took %v", elapsed)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("cache hit went to the network (%d fetches)", got)
	}
	if links := browserLinksFor(browserContentVariable); len(links) != 1 {
		t.Fatalf("cached links = %d, want 1", len(links))
	}
}

func TestBrowserCacheEvictsLeastRecentlyUsed(t *testing.T) {
	resetBrowserStateForTest()
	t.Cleanup(resetBrowserStateForTest)

	address := func(i int) string { return fmt.Sprintf("https://example.com/%d", i) }
	for i := 0; i < browserCacheMaxEntries; i++ {
		browserCachePage(address(i), browserPage{Text: "page", FinalURL: address(i)})
	}
	// Touch the oldest page so it is no longer the eviction candidate.
	if _, ok := browserCachedPage(address(0)); !ok {
		t.Fatal("page 0 was not cached")
	}
	browserCachePage("https://example.com/fresh", browserPage{Text: "fresh", FinalURL: "https://example.com/fresh"})

	if _, ok := browserCachedPage(address(0)); !ok {
		t.Fatal("the recently used page was evicted")
	}
	if _, ok := browserCachedPage(address(1)); ok {
		t.Fatal("the least recently used page survived eviction")
	}
	if entries, _ := browserCacheSize(); entries > browserCacheMaxEntries {
		t.Fatalf("cache holds %d entries, over its %d limit", entries, browserCacheMaxEntries)
	}
}

// A page that failed has no final URL; caching it would make Reload pointless
// and make a dropped connection stick until the app restarts.
func TestBrowserFailedLoadIsNotCached(t *testing.T) {
	resetBrowserStateForTest()
	t.Cleanup(resetBrowserStateForTest)
	browserCachePage("https://example.com/down", browserPage{Text: "Could not load https://example.com/down"})
	if _, ok := browserCachedPage("https://example.com/down"); ok {
		t.Fatal("a failed load was cached")
	}
}

func TestBrowserReloadBypassesCache(t *testing.T) {
	version := "first version"
	config, elem, calls := browserCacheFixture(t, func(rawURL string) browserPage {
		return browserPage{Text: "body " + version, FinalURL: rawURL}
	})

	loadTextBrowserURLAt(config, elem, "https://example.com/a", false)
	waitBrowserText(t, config, func(s string) bool { return strings.Contains(s, "first version") })

	version = "second version"
	reloadTextBrowserURL(config, elem, "https://example.com/a")
	if text, _ := config.Variables.Custom[browserContentVariable].(string); !strings.HasPrefix(text, "Loading ") {
		t.Fatalf("reload did not go back to the network: %q", text)
	}
	waitBrowserText(t, config, func(s string) bool { return strings.Contains(s, "second version") })
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("reload fetched %d times, want 2", got)
	}
	// The refreshed copy replaces the old one, so Back is instant again.
	if page, ok := browserCachedPage("https://example.com/a"); !ok || !strings.Contains(page.Text, "second version") {
		t.Fatalf("cache after reload = %q, %v", page.Text, ok)
	}
}

func TestBrowserCacheClearedWithBrowserState(t *testing.T) {
	resetBrowserStateForTest()
	browserCachePage("https://example.com/a", browserPage{Text: "page", FinalURL: "https://example.com/a"})
	if entries, _ := browserCacheSize(); entries != 1 {
		t.Fatalf("cache holds %d entries, want 1", entries)
	}
	resetBrowserStateForTest()
	if entries, bytes := browserCacheSize(); entries != 0 || bytes != 0 {
		t.Fatalf("reset left %d entries / %d bytes in the cache", entries, bytes)
	}
}

// browserPageWeight has to count the image bytes, otherwise a page of photos
// looks free to the eviction policy.
func TestBrowserPageWeightCountsImages(t *testing.T) {
	doc := &webDocument{
		Images: map[string][]byte{
			"a": make([]byte, 300),
			"b": make([]byte, 200),
		},
	}
	page := browserPage{Text: "12345", Doc: doc}
	if got := browserPageWeight(page); got < 500 {
		t.Fatalf("weight = %d, want at least the 500 image bytes", got)
	}
}
