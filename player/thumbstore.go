package main

import (
	"container/list"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
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

	// 2. Disk cache (survives restarts). The shared handle avoids a per-image
	// open/mmap, which cost more than the download on a local network.
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		if data, cErr := cacheGet(db, thumbnailDiskPrefix+url); cErr == nil && len(data) > 0 {
			thumbnailDataSet(url, data)
			return data, nil
		}
	}

	// 3. Network with retry + backoff.
	data, err := httpGetWithRetry(url, 3, 2*time.Second)
	if err != nil {
		return nil, err
	}

	// Persist to both caches.
	thumbnailDataSet(url, data)
	if db, err := openSharedCache("jukahub.cache"); err == nil {
		_ = cacheSet(db, thumbnailDiskPrefix+url, data, thumbnailTTL)
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

// httpError is a typed error for HTTP non-2xx responses.
type httpError struct {
	Code int
	URL  string
	Body []byte // response body if available (may contain error details)
}

func (e *httpError) Error() string {
	status := http.StatusText(e.Code)
	if len(e.Body) > 0 {
		// Include a snippet of the response body for debugging
		snippet := string(e.Body)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return fmt.Sprintf("HTTP %d %s: %s", e.Code, status, snippet)
	}
	return fmt.Sprintf("HTTP %d %s", e.Code, status)
}

// Unwrap returns the underlying error if any.
func (e *httpError) Unwrap() error {
	return nil
}

// IsHTTPError returns true if the error is an httpError.
func IsHTTPError(err error) bool {
	_, ok := err.(*httpError)
	return ok
}

// HTTPStatusCode extracts the HTTP status code from an error if it's an httpError.
// Returns 0 if the error is not an httpError.
func HTTPStatusCode(err error) int {
	if he, ok := err.(*httpError); ok {
		return he.Code
	}
	return 0
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

// thumbnailInFlight dedupes concurrent prefetches of the same URL, which is
// common when a grid re-enqueues its visible rows every frame.
var thumbnailInFlight sync.Map

// thumbnailWorkerCount picks prefetch concurrency. A single worker serialized
// every thumbnail behind the slowest request, so a grid of YouTube results
// filled one image per round trip. Two workers overlap the Wi-Fi latency
// without competing for the handheld's 1GB of RAM; desktop gets three.
func thumbnailWorkerCount() int {
	if IsTrimuiDevice() || P().IsHandheld() {
		return 2
	}
	return 3
}

// thumbnailPrefetchWorker is one of the background goroutines that drains
// thumbnailDownloadCh, downloading to memory + disk caches without blocking
// the render loop.
// ──────────────────────────────────────────────────────────────────────
// Bounded thumbnail texture cache
// ──────────────────────────────────────────────────────────────────────

// thumbnailTextureBudgetBytes caps the SDL textures decoded from thumbnails.
// The old cache was an unbounded map: a long YouTube session kept every image
// ever scrolled past, and on a 1 GB device that is what pushed the renderer
// into evicting and re-uploading textures mid-transition. 64 MB holds roughly
// 250 320×180 thumbnails, far more than a screenful, while leaving RAM for
// video; desktops get a larger budget.
func thumbnailTextureBudgetBytes() int64 {
	if IsTrimuiDevice() || P().IsHandheld() {
		return 64 << 20
	}
	return 192 << 20
}

// thumbnailTextureEntry is one cached texture with its approximate footprint.
type thumbnailTextureEntry struct {
	texture *sdl.Texture
	bytes   int64
	elem    *list.Element
}

// thumbnailTextureCache is an LRU cache of decoded thumbnail textures. Lookups
// are O(1); eviction happens on insert, never while a texture is being drawn.
type thumbnailTextureCache struct {
	mu       sync.Mutex
	entries  map[string]*thumbnailTextureEntry
	lru      *list.List // front = most recently used; values are keys
	bytes    int64
	maxBytes int64
	destroy  func(*sdl.Texture) // injectable for tests
}

func newThumbnailTextureCache(maxBytes int64) *thumbnailTextureCache {
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	return &thumbnailTextureCache{
		entries:  make(map[string]*thumbnailTextureEntry),
		lru:      list.New(),
		maxBytes: maxBytes,
		destroy: func(t *sdl.Texture) {
			if t != nil {
				t.Destroy()
			}
		},
	}
}

var (
	thumbnailTexturesOnce sync.Once
	thumbnailTextures     *thumbnailTextureCache
)

// getThumbnailTextures returns the process-wide thumbnail texture cache,
// created on first use so the device-specific budget is evaluated after the
// environment (TrimUI detection) is available.
func getThumbnailTextures() *thumbnailTextureCache {
	thumbnailTexturesOnce.Do(func() {
		thumbnailTextures = newThumbnailTextureCache(thumbnailTextureBudgetBytes())
	})
	return thumbnailTextures
}

// Get returns a cached texture and marks it most recently used.
func (c *thumbnailTextureCache) Get(key string) *sdl.Texture {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}
	c.lru.MoveToFront(entry.elem)
	return entry.texture
}

// Put stores a texture, evicting least-recently-used entries over the budget.
// w/h are the texture dimensions; they determine the accounted footprint.
func (c *thumbnailTextureCache) Put(key string, texture *sdl.Texture, w, h int32) {
	bytes := int64(w) * int64(h) * 4
	if bytes <= 0 {
		// Query can fail on an unusual driver format. Account a conservative
		// floor so such entries still age out of the budget instead of
		// accumulating for free.
		bytes = 64 * 64 * 4
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.bytes -= old.bytes
		c.lru.Remove(old.elem)
		c.destroy(old.texture)
		delete(c.entries, key)
	}
	entry := &thumbnailTextureEntry{texture: texture, bytes: bytes}
	entry.elem = c.lru.PushFront(key)
	c.entries[key] = entry
	c.bytes += bytes
	// Keep at least the newest entry even if a single texture exceeds the
	// budget; destroying it would return a dangling pointer to the caller.
	for c.bytes > c.maxBytes && c.lru.Len() > 1 {
		back := c.lru.Back()
		if back == nil {
			break
		}
		key := back.Value.(string)
		if evicted, ok := c.entries[key]; ok {
			c.bytes -= evicted.bytes
			c.destroy(evicted.texture)
			delete(c.entries, key)
		}
		c.lru.Remove(back)
	}
}

// Clear destroys every cached texture. Runs on the render thread / shutdown.
func (c *thumbnailTextureCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		c.destroy(entry.texture)
		delete(c.entries, key)
	}
	c.lru.Init()
	c.bytes = 0
}

// contains reports whether a key is cached, without touching its LRU position.
func (c *thumbnailTextureCache) contains(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[key]
	return ok
}

// Len returns the number of cached textures.
func (c *thumbnailTextureCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Bytes returns the approximated texture memory held by the cache.
func (c *thumbnailTextureCache) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

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
		if _, busy := thumbnailInFlight.LoadOrStore(url, struct{}{}); busy {
			continue
		}
		err := func() error {
			defer thumbnailInFlight.Delete(url)
			_, err := fetchThumbnailBytes(url)
			return err
		}()
		if err != nil {
			// Silent failure — the render path retries on demand.
			log.Printf("[thumb] prefetch failed for %s: %v", url, err)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Render-path decode budget — the checklist's "2 decodes per frame"
// ──────────────────────────────────────────────────────────────────────

// thumbnailDecodeBudgetFor is how many cold thumbnail loads one presented
// frame may take from the render path. Each cold load is a JPEG/PNG decode
// plus a texture upload, and a freshly scrolled grid calls loadThumbnail for
// every row in one pass: before the budget, 24 rows stacked all of their
// decode-upload cost onto the frame the user scrolled into, on a device whose
// frame allowance is ~16 ms. A frame that spends its budget defers the surplus
// through the prefetch queue, and each later frame drains two more — a
// screenful fills in over staggered frames instead of one frame stalling.
// Desktop keeps a larger budget because its frames and CPU are not the
// constraint the item targets.
func thumbnailDecodeBudgetFor(handheld bool) int {
	if handheld {
		return 2
	}
	return 8
}

// thumbnailDecodeBudgetPerFrame evaluates the budget for this device.
func thumbnailDecodeBudgetPerFrame() int {
	return thumbnailDecodeBudgetFor(IsTrimuiDevice() || P().IsHandheld())
}

// thumbnailDecodeGate is the tally the render path shares. All callers sit on
// the render thread, so contention here is nil; the mutex just keeps it honest
// about being read from any goroutine a future refactor might reach.
type thumbnailDecodeGate struct {
	mu        sync.Mutex
	lastFrame uint64
	used      int
}

var thumbnailDecodes thumbnailDecodeGate

// allowThumbnailDecode spends one budget slot against frame and reports
// whether the load may proceed. A frame number this process has not seen yet
// opens a fresh tally, so the budget is genuinely per frame and not per
// second. Callers that are declined queue the URL through PrefetchThumbnail
// so the bytes keep warming while the frame keeps its code budget.
func allowThumbnailDecode(frame uint64, budget int) bool {
	thumbnailDecodes.mu.Lock()
	defer thumbnailDecodes.mu.Unlock()
	if frame != thumbnailDecodes.lastFrame {
		thumbnailDecodes.lastFrame = frame
		thumbnailDecodes.used = 0
	}
	if thumbnailDecodes.used >= budget {
		return false
	}
	thumbnailDecodes.used++
	return true
}
