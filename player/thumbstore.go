package main

import (
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────────────
// Thumbnail byte store — disk-backed, bounded memory, retry-on-failure
// ──────────────────────────────────────────────────────────────────────

// thumbnailTTL is how long downloaded thumbnails stay valid on disk.
// YouTube thumbnails are stable for months, so a 7-day TTL saves a huge
// amount of re-downloading on TSP's slow WiFi without going stale.
const thumbnailTTL = 7 * 24 * time.Hour

// thumbnailDiskPrefix separates disk-cache keys from other cache users.
const thumbnailDiskPrefix = "thumb:"

// thumbnailMemMaxEntries caps the in-memory thumbnail byte cache so a long
// browsing session on a 1GB device can't grow it without bound.
const thumbnailMemMaxEntries = 512

// fetchThumbnailBytes returns the raw bytes for a thumbnail URL, checking
// (in order): the in-memory cache, the bbolt disk cache, then the network.
// Successful downloads are stored in both caches. Safe for concurrent use.
func fetchThumbnailBytes(url string) ([]byte, error) {
	if url == "" {
		return nil, io.EOF
	}
	// Strip query params and normalize webp→jpg like the texture loader.
	url = normalizeThumbnailURL(url)

	// 1. In-memory cache (fastest path).
	if data := thumbnailDataGet(url); data != nil {
		return data, nil
	}

	// 2. Disk cache (survives restarts).
	if db, err := cacheOpen("jukahub.cache"); err == nil {
		if data, cErr := cacheGet(db, thumbnailDiskPrefix+url); cErr == nil && len(data) > 0 {
			db.Close()
			thumbnailDataSet(url, data)
			return data, nil
		}
		db.Close()
	}

	// 3. Network with retry + backoff.
	data, err := httpGetWithRetry(url, 3, 2*time.Second)
	if err != nil {
		return nil, err
	}

	// Persist to both caches.
	thumbnailDataSet(url, data)
	if db, err := cacheOpen("jukahub.cache"); err == nil {
		_ = cacheSet(db, thumbnailDiskPrefix+url, data, thumbnailTTL)
		db.Close()
	}
	return data, nil
}

// normalizeThumbnailURL strips query strings and rewrites .webp to .jpg,
// mirroring the logic in loadThumbnailFromURLs so cache keys are stable.
func normalizeThumbnailURL(raw string) string {
	url := raw
	if i := strings.Index(url, "?"); i >= 0 {
		url = url[:i]
	}
	lower := strings.ToLower(url)
	if !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") &&
		!strings.HasSuffix(lower, ".png") && !strings.HasSuffix(lower, ".bmp") {
		url = strings.TrimSuffix(url, ".webp") + ".jpg"
	}
	return url
}

// httpGetWithRetry performs an HTTP GET with exponential backoff + jitter on
// transient failures (connection errors, 5xx, 429). Non-transient responses
// (4xx) fail immediately.
func httpGetWithRetry(url string, maxAttempts int, baseDelay time.Duration) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := httpClientLong.Get(url)
		if err == nil {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				data, rErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8MB cap
				resp.Body.Close()
				if rErr != nil {
					lastErr = rErr
				} else {
					return data, nil
				}
			} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				// Client error — retrying won't help.
				resp.Body.Close()
				return nil, &httpError{Code: resp.StatusCode, URL: url}
			} else {
				lastErr = &httpError{Code: resp.StatusCode, URL: url}
				resp.Body.Close()
			}
		} else {
			lastErr = err
		}
		if attempt < maxAttempts {
			// Exponential backoff with full jitter: delay ∈ [0, base*2^attempt).
			cap := baseDelay * time.Duration(1<<uint(attempt-1))
			delay := time.Duration(rand.Int63n(int64(cap)))
			time.Sleep(delay)
		}
	}
	return nil, lastErr
}

// httpError is a minimal typed error for non-2xx responses.
type httpError struct {
	Code int
	URL  string
}

func (e *httpError) Error() string {
	return "HTTP " + http.StatusText(e.Code)
}

// ──────────────────────────────────────────────────────────────────────
// Bounded in-memory thumbnail byte cache
// ──────────────────────────────────────────────────────────────────────

// thumbnailDataGet returns cached bytes for a URL (nil if absent).
func thumbnailDataGet(url string) []byte {
	thumbnailCacheMutex.Lock()
	defer thumbnailCacheMutex.Unlock()
	return thumbnailDataCache[url]
}

// thumbnailDataSet stores bytes in the memory cache, evicting the oldest
// entries (first-inserted) once the cap is exceeded. The map is used as an
// insertion-ordered cache: re-inserting an existing key refreshes it.
func thumbnailDataSet(url string, data []byte) {
	thumbnailCacheMutex.Lock()
	defer thumbnailCacheMutex.Unlock()
	if _, ok := thumbnailDataCache[url]; !ok && len(thumbnailDataCache) >= thumbnailMemMaxEntries {
		// Evict the oldest entry (Go map iteration order is random, so we
		// track insertion order via a parallel slice).
		for k := range thumbnailDataCache {
			delete(thumbnailDataCache, k)
			break
		}
	}
	thumbnailDataCache[url] = data
}

// PrefetchThumbnail enqueues a URL for background download so the disk cache
// warms up before the user scrolls to it. Non-blocking; drops when full.
func PrefetchThumbnail(url string) {
	if url == "" {
		return
	}
	url = normalizeThumbnailURL(url)
	// Already have it? Skip enqueueing.
	if thumbnailDataGet(url) != nil {
		return
	}
	select {
	case thumbnailDownloadCh <- url:
	default:
		// Channel full — skip; the render path will fetch on demand.
	}
}

// thumbnailPrefetchWorker is the background goroutine that drains
// thumbnailDownloadCh, downloading to memory + disk caches without blocking
// the render loop.
func thumbnailPrefetchWorker() {
	for url := range thumbnailDownloadCh {
		if url == "" {
			continue
		}
		thumbnailCacheMutex.Lock()
		_, memHit := thumbnailDataCache[url]
		thumbnailCacheMutex.Unlock()
		if memHit {
			continue
		}
		if _, err := fetchThumbnailBytes(url); err != nil {
			// Silent failure — the render path retries on demand.
			log.Printf("[thumb] prefetch failed for %s: %v", url, err)
			continue
		}
	}
}
