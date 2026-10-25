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

type httpResponseCache struct {
	mu      sync.Mutex
	max     int
	order   []string
	entries map[string]httpCacheEntry
}

func newHTTPResponseCache(max int) *httpResponseCache {
	if max < 1 {
		max = 1
	}
	return &httpResponseCache{max: max, entries: map[string]httpCacheEntry{}}
}

// validators returns the stored ETag and Last-Modified for a URL.
func (c *httpResponseCache) validators(url string) (etag, lastModified string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[url]
	if !ok {
		return "", ""
	}
	return e.etag, e.lastModified
}

// body returns the last successful body for a URL, used to answer a 304.
func (c *httpResponseCache) body(url string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[url]
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
		delete(c.entries, url)
		c.dropOrderLocked(url)
		return
	}
	if _, exists := c.entries[url]; !exists {
		c.order = append(c.order, url)
		for len(c.order) > c.max {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
	}
	copied := make([]byte, len(body))
	copy(copied, body)
	c.entries[url] = httpCacheEntry{etag: etag, lastModified: lastModified, body: copied}
}

func (c *httpResponseCache) dropOrderLocked(url string) {
	for i, u := range c.order {
		if u == url {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// reset drops everything. Tests use it between cases; the app never needs it.
func (c *httpResponseCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order = nil
	c.entries = map[string]httpCacheEntry{}
}

func (c *httpResponseCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// conditionalCache holds the validators for every URL fetched through the
// shared helpers below.
var conditionalCache = newHTTPResponseCache(httpCacheMaxEntries)

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
