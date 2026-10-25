package main

// httpcache_test.go — the conditional-request cache behind fetchBounded and
// httpGetTextContext. The behaviour that matters is not "a map was written":
// it is that an unchanged resource costs one 304 and returns the previous body,
// that changed content is always adopted, and that a server answering 304 to a
// request we have no copy of is reported rather than silently accepted.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConditionalCacheAnswers304WithTheCachedBody(t *testing.T) {
	resetConditionalCacheForTest()
	var full, revalidated int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"feed-v1"` {
			atomic.AddInt32(&revalidated, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt32(&full, 1)
		w.Header().Set("ETag", `"feed-v1"`)
		w.Write([]byte("the feed body"))
	}))
	defer srv.Close()

	if got := httpGetTextContext(context.Background(), srv.URL, 2*time.Second); got != "the feed body" {
		t.Fatalf("first fetch = %q", got)
	}
	if got := httpGetTextContext(context.Background(), srv.URL, 2*time.Second); got != "the feed body" {
		t.Fatalf("second fetch = %q, want the cached body served for the 304", got)
	}
	if full != 1 || revalidated != 1 {
		t.Fatalf("requests: full=%d revalidated=%d, want 1 and 1", full, revalidated)
	}
}

func TestConditionalCacheUsesLastModified(t *testing.T) {
	resetConditionalCacheForTest()
	var revalidated int32
	const stamp = "Wed, 21 Oct 2015 07:28:00 GMT"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Modified-Since") == stamp {
			atomic.AddInt32(&revalidated, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", stamp)
		w.Write([]byte("dated body"))
	}))
	defer srv.Close()

	first := httpGetTextContext(context.Background(), srv.URL, 2*time.Second)
	second := httpGetTextContext(context.Background(), srv.URL, 2*time.Second)
	if first != "dated body" || second != "dated body" {
		t.Fatalf("bodies = %q / %q", first, second)
	}
	if revalidated != 1 {
		t.Fatalf("If-Modified-Since requests = %d, want 1", revalidated)
	}
}

// New content must replace the cached copy, not be swallowed because a
// validator was sent.
func TestConditionalCacheAdoptsChangedContent(t *testing.T) {
	resetConditionalCacheForTest()
	var version int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := atomic.LoadInt32(&version)
		etag := `"v1"`
		if v > 0 {
			etag = `"v2"`
		}
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		if v == 0 {
			w.Write([]byte("old"))
		} else {
			w.Write([]byte("new"))
		}
	}))
	defer srv.Close()

	if got := httpGetTextContext(context.Background(), srv.URL, 2*time.Second); got != "old" {
		t.Fatalf("first = %q", got)
	}
	atomic.StoreInt32(&version, 1)
	if got := httpGetTextContext(context.Background(), srv.URL, 2*time.Second); got != "new" {
		t.Fatalf("second = %q, want the changed body", got)
	}
}

// A server insisting "not modified" for a URL we hold nothing for is a
// contract violation: report it instead of handing back an empty body as if
// the resource were empty.
func TestFetchBoundedRejects304WithoutACopy(t *testing.T) {
	resetConditionalCacheForTest()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	prev := allowedPatchHosts["127.0.0.1"]
	allowedPatchHosts["127.0.0.1"] = true
	t.Cleanup(func() { allowedPatchHosts["127.0.0.1"] = prev })

	if _, err := fetchBounded(context.Background(), srv.Client(), srv.URL, 4096); err == nil {
		t.Fatal("a 304 with no cached copy must be an error")
	}

	// The plain-text helper uses the default client, so it gets a plain
	// server: an uncacheable 304 must not look like an empty document.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer plain.Close()
	if got := httpGetTextContext(context.Background(), plain.URL, 2*time.Second); got != "" {
		t.Fatalf("text fetch = %q, want an empty result for an uncacheable 304", got)
	}
}

// The release manifest path revalidates too: one full download, then a 304.
func TestFetchBoundedRevalidatesTheManifest(t *testing.T) {
	resetConditionalCacheForTest()
	var full, revalidated int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"manifest-1"` {
			atomic.AddInt32(&revalidated, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt32(&full, 1)
		w.Header().Set("ETag", `"manifest-1"`)
		w.Write([]byte(`{"version":"1.2.3"}`))
	}))
	defer srv.Close()

	prev := allowedPatchHosts["127.0.0.1"]
	allowedPatchHosts["127.0.0.1"] = true
	t.Cleanup(func() { allowedPatchHosts["127.0.0.1"] = prev })

	client := srv.Client()
	first, err := fetchBounded(context.Background(), client, srv.URL, 4096)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	second, err := fetchBounded(context.Background(), client, srv.URL, 4096)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if string(first) != `{"version":"1.2.3"}` || string(second) != string(first) {
		t.Fatalf("bodies = %q / %q", first, second)
	}
	if full != 1 || revalidated != 1 {
		t.Fatalf("requests: full=%d revalidated=%d, want 1 and 1", full, revalidated)
	}
}

// A response without validators cannot be revalidated, so it must not leave a
// body behind that a later 304 could resurrect.
func TestConditionalCacheDropsUnvalidatedBodies(t *testing.T) {
	resetConditionalCacheForTest()
	c := newHTTPResponseCache(4)
	c.store("https://example.com/a", `"v1"`, "", []byte("cached"))
	if _, ok := c.body("https://example.com/a"); !ok {
		t.Fatal("validated body was not stored")
	}
	c.store("https://example.com/a", "", "", []byte("no validators"))
	if _, ok := c.body("https://example.com/a"); ok {
		t.Fatal("a body without validators must not stay cached")
	}
	c.store("https://example.com/a", `"v1"`, "", []byte("v1"))
	c.store("https://example.com/a", `"v2"`, "", []byte("v2"))
	if _, lastModified := c.validators("https://example.com/a"); lastModified != "" {
		t.Fatalf("Last-Modified = %q, want empty", lastModified)
	}
	if got, _ := c.body("https://example.com/a"); string(got) != "v2" {
		t.Fatalf("body = %q, want the newest", got)
	}
}

// The map is bounded: a device that browses many feeds must not grow it
// without limit, and the oldest URL is the one that leaves.
func TestConditionalCacheIsBounded(t *testing.T) {
	c := newHTTPResponseCache(3)
	for _, url := range []string{"a", "b", "c", "d"} {
		c.store(url, `"`+url+`"`, "", []byte(url))
	}
	if got := c.size(); got != 3 {
		t.Fatalf("size = %d, want the 3-entry cap", got)
	}
	if _, ok := c.body("a"); ok {
		t.Fatal("the oldest entry survived the cap")
	}
	if _, ok := c.body("d"); !ok {
		t.Fatal("the newest entry was dropped")
	}
}

// Large bodies are streamed, not kept: the cache must never hold an archive.
func TestConditionalCacheSkipsLargeBodies(t *testing.T) {
	c := newHTTPResponseCache(4)
	big := make([]byte, httpCacheMaxBody+1)
	c.store("https://example.com/archive", `"v1"`, "", big)
	if _, ok := c.body("https://example.com/archive"); ok {
		t.Fatal("an oversized body was cached")
	}
}

// The cache is reached from scene work goroutines on several screens.
func TestConditionalCacheIsSafeForConcurrentUse(t *testing.T) {
	c := newHTTPResponseCache(8)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			url := "https://example.com/feed"
			c.store(url, `"v"`, "", []byte("body"))
			c.validators(url)
			c.body(url)
		}(i)
	}
	wg.Wait()
	if _, ok := c.body("https://example.com/feed"); !ok {
		t.Fatal("concurrent use lost the entry")
	}
}

// no-store forbids keeping the body: a private or one-shot response must not
// turn into something a later 304 can serve back.
func TestConditionalCacheHonoursNoStore(t *testing.T) {
	resetConditionalCacheForTest()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"private"`)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Write([]byte("secret"))
	}))
	defer srv.Close()

	if got := httpGetTextContext(context.Background(), srv.URL, 2*time.Second); got != "secret" {
		t.Fatalf("first fetch = %q", got)
	}
	if _, ok := conditionalCache.body(srv.URL); ok {
		t.Fatal("a no-store response was cached")
	}
	if etag, _ := conditionalCache.validators(srv.URL); etag != "" {
		t.Fatalf("validators kept for a no-store response: %q", etag)
	}
}
