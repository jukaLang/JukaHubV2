package main

// feedcache.go — an offline fallback for text feeds.
//
// The conditional cache in httpcache.go is deliberately process-lifetime: a
// restart costs one full fetch. That is fine as a bandwidth optimisation, but
// it does nothing for the case that actually matters on a handheld — the device
// boots on a train, nothing has an address yet, and every feed, ticker and
// quote endpoint returns "". The user sees an empty Podcasts screen even though
// a good copy of those feeds was on the card yesterday.
//
// This layer is the opposite trade: a small, disk-backed last-known-good copy
// of each feed body, kept for a bounded window and served only when the network
// fetch fails outright. It is never used to answer a *successful* request (the
// conditional cache and the network win first), so it cannot serve stale data
// while online. It is state, not an optimisation, which is why it lives apart
// from httpcache.go and rides the shared bbolt cache the thumbnails already use.

import (
	"context"
	"log"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	// feedFallbackPrefix namespaces feed bodies in the cache bucket so they
	// cannot collide with thumbnail or dictionary entries.
	feedFallbackPrefix = "feed:"
	// feedFallbackTTL is how long a last-known-good copy is kept. Long enough
	// to bridge a few days offline, short enough that a feed abandoned months
	// ago does not sit on the card forever.
	feedFallbackTTL = 30 * 24 * time.Hour
	// feedFallbackMaxBody bounds one stored feed. Podcast RSS and quote
	// endpoints are far smaller; anything bigger is not a feed.
	feedFallbackMaxBody = 1 << 20 // 1 MiB
)

// feedCacheDB returns the shared cache handle, or nil when it cannot be opened.
// The shared handle is reused across every caller, so this is a mutex-guarded
// map lookup after the first open, not a file open per fetch.
func feedCacheDB() *bolt.DB {
	db, err := openSharedCache("jukahub.cache")
	if err != nil {
		return nil
	}
	return db
}

// storeFeedFallback remembers a successful feed body for offline use. A body
// over the size cap, or an empty one, is not stored — it is not a feed.
func storeFeedFallback(url string, body []byte) {
	if url == "" || len(body) == 0 || len(body) > feedFallbackMaxBody {
		return
	}
	db := feedCacheDB()
	if db == nil {
		return
	}
	if err := cacheSet(db, feedFallbackPrefix+url, body, feedFallbackTTL); err != nil {
		log.Printf("[feedcache] store %s: %v", sanitizeLogURL(url), err)
	}
}

// loadFeedFallback returns the last-known-good body for a feed, if one is
// still inside its window. It is only consulted when the network fetch failed.
func loadFeedFallback(url string) (string, bool) {
	if url == "" {
		return "", false
	}
	db := feedCacheDB()
	if db == nil {
		return "", false
	}
	body, err := cacheGet(db, feedFallbackPrefix+url)
	if err != nil || len(body) == 0 {
		return "", false
	}
	return string(body), true
}

// httpGetTextFallbackContext mirrors httpGetTextContext but, on a transport
// error (as opposed to an HTTP error status), falls back to the last-known-good
// copy. Feed and ticker callers use this so an offline boot shows the last
// screen the user actually saw instead of nothing. A non-200 response is NOT a
// fallback trigger: a 404 means the feed is gone, and replaying an old copy
// would be a lie. Only "the network could not be reached" earns the fallback.
func httpGetTextFallbackContext(ctx context.Context, url string, timeout time.Duration) string {
	if body := httpGetTextContext(ctx, url, timeout); body != "" {
		// Remember the good copy for next time the network is away.
		storeFeedFallback(url, []byte(body))
		return body
	}
	// The fetch returned nothing. Distinguish "offline / unreachable" from a
	// handled error by consulting connectivity: if we believe we are offline,
	// serve the last-known-good copy rather than an empty screen.
	if !IsOnline() {
		if body, ok := loadFeedFallback(url); ok {
			log.Printf("[feedcache] offline, served cached %s", sanitizeLogURL(url))
			return body
		}
	}
	return ""
}
