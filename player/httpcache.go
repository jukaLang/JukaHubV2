package main

// httpcache.go — conditional requests for the shared fetchers.
//
// Feeds and release metadata change rarely, but the app refetches them on every
// scene entry or update check. Remembering the last successful body next to its
// ETag / Last-Modified lets the next request be a conditional one: an unchanged
// resource answers 304 with no body, which on a handheld's metered connection is
// the difference between a few hundred bytes and a few hundred kilobytes.
//
// The cache is deliberately process-lifetime and in-memory: it is an
// optimisation, not state. A restart costs one full fetch, and nothing here can
// outlive the build that wrote it (an ETag from an older decoder would be a
// correctness risk, not just a stale read).

import (
	"net/http"
	"strings"
	"sync"
)

const (
	// httpCacheMaxEntries bounds the map; the oldest insert is dropped first.
	// Feeds, tickers and release metadata are a handful of URLs.
	httpCacheMaxEntries = 64
	// httpCacheMaxBody is the largest body worth keeping. Anything larger is
	// streamed to its consumer (a package archive, a video) and is not a
	// candidate for conditional caching anyway.
	httpCacheMaxBody = 1 << 20 // 1 MiB
)

type httpCacheEntry struct {
	etag         string
	lastModified string
	body         []byte
}

// httpResponseCache keeps validated bodies. It is bounded twice: by entry count
// and by bytes. The count alone was a byte ceiling only if every body were the
// same size, and a feed and a release manifest are not — 64 entries at the 1 MiB
// body limit was 64 MB of RAM by accident.
//
// The store owns the map and the order; mu serialises access, because scene work
// goroutines store and read from several screens.
type httpResponseCache struct {
	mu      sync.Mutex
	entries *byteOrderedStore[string, httpCacheEntry]
}

// newHTTPResponseCache builds a count-bounded cache with no byte budget, which
// is what the unit tests of the entry rule want.
func newHTTPResponseCache(max int) *httpResponseCache {
	return newHTTPResponseCacheWithBudget(max, 0)
}

// newHTTPResponseCacheWithBudget is the app's constructor: entries and bytes.
func newHTTPResponseCacheWithBudget(max int, maxBytes int64) *httpResponseCache {
	if max < 1 {
		max = 1
	}
	return &httpResponseCache{entries: newByteOrderedStore[string, httpCacheEntry](
		max,
		maxBytes,
		func(e httpCacheEntry) int64 { return int64(len(e.body)) },
		nil,
	)}
}

// validators returns the stored ETag and Last-Modified for a URL.
func (c *httpResponseCache) validators(url string) (etag, lastModified string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries.get(url)
	if !ok {
		return "", ""
	}
	return e.etag, e.lastModified
}

// body returns the last successful body for a URL, used to answer a 304.
func (c *httpResponseCache) body(url string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries.get(url)
	if !ok || len(e.body) == 0 {
		return nil, false
	}
	out := make([]byte, len(e.body))
	copy(out, e.body)
	return out, true
}

// store records a successful body with its validators. A response without
// either validator cannot be revalidated, so it is not stored (the previous
// copy is dropped: it belongs to a version that is gone).
func (c *httpResponseCache) store(url, etag, lastModified string, body []byte) {
	etag = strings.TrimSpace(etag)
	lastModified = strings.TrimSpace(lastModified)
	c.mu.Lock()
	defer c.mu.Unlock()
	if etag == "" && lastModified == "" || len(body) == 0 || len(body) > httpCacheMaxBody {
		// A body without a validator cannot be revalidated, and an oversized
		// one is streamed rather than cached: either way the old copy belongs
		// to a version that is gone.
		c.entries.drop(url)
		return
	}
	copied := make([]byte, len(body))
	copy(copied, body)
	c.entries.put(url, httpCacheEntry{etag: etag, lastModified: lastModified, body: copied})
}

// reset drops everything. Tests use it between cases; the app never needs it.
func (c *httpResponseCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries.clear()
}

func (c *httpResponseCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, _ := c.entries.stats()
	return entries
}

// bytesHeld is the body memory the cache is accounted for.
func (c *httpResponseCache) bytesHeld() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, bytes := c.entries.stats()
	return bytes
}

// clear drops every body. The memory pressure path calls it: a refetch costs
// one round trip, which beats holding 8 MB of feeds the screen is not showing.
func (c *httpResponseCache) clear() { c.reset() }

// conditionalCache holds the validators for every URL fetched through the
// shared helpers below.
var conditionalCache = newHTTPResponseCacheWithBudget(httpCacheMaxEntries, httpCacheBudgetBytes())

// applyConditionalHeaders turns req into a conditional request when this
// process already holds a validated copy of the same URL.
func applyConditionalHeaders(req *http.Request, raw string) {
	if req == nil || raw == "" {
		return
	}
	etag, lastModified := conditionalCache.validators(raw)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
}

// storeConditionalResponse remembers a successful response's validators and
// body for the next conditional request. A 304 carries no body and is handled
// by the caller from the cache.
func storeConditionalResponse(url string, resp *http.Response, body []byte) {
	if resp == nil || resp.StatusCode != http.StatusOK {
		return
	}
	// no-store is the one directive that forbids keeping the body at all, so
	// honour it instead of turning a private response into a cached copy.
	if strings.Contains(strings.ToLower(resp.Header.Get("Cache-Control")), "no-store") {
		conditionalCache.store(url, "", "", nil)
		return
	}
	conditionalCache.store(url, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), body)
}

// conditionalBody answers a 304 from the cache. ok is false when the server
// answered "not modified" for a URL this process has no body for — a contract
// violation the caller should report rather than retry blindly.
func conditionalBody(url string) ([]byte, bool) {
	return conditionalCache.body(url)
}

// resetConditionalCacheForTest clears the cache between cases.
func resetConditionalCacheForTest() {
	conditionalCache.reset()
}
