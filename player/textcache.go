package main

import (
	"container/list"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// textCacheEntry holds a pre-rendered text texture.
type textCacheEntry struct {
	texture *sdl.Texture
	width   int32
	height  int32
	bytes   int64
	// element is this key's node in the LRU list (front = most recent).
	element *list.Element
}

// textCacheKey is the cache identity: font, exact text and colour. It is a
// comparable struct rather than a concatenated string, so a lookup no longer
// allocates a key (the old %p+text+colour string cost 2 allocations and
// ~420ns on every visible string, every frame).
type textCacheKey struct {
	font  *ttf.Font
	text  string
	color sdl.Color
}

// TextCache is an LRU texture cache for rendered text.
// On TSP (1GB RAM), we cap at 512 entries to avoid memory pressure.
type TextCache struct {
	mu         sync.RWMutex
	entries    map[textCacheKey]*textCacheEntry
	order      *list.List // front = most recently used
	maxSize    int
	totalBytes int64 // approximate texture memory (RGBA), for diagnostics
}

// NewTextCache creates a cache with the given maximum entries.
// A non-positive limit is clamped to 1: a zero-sized cache would otherwise
// never evict (the eviction loop needs an entry to remove) and grow without
// bound while every Put destroyed nothing.
func NewTextCache(maxSize int) *TextCache {
	if maxSize < 1 {
		maxSize = 1
	}
	return &TextCache{
		entries: make(map[textCacheKey]*textCacheEntry, maxSize),
		order:   list.New(),
		maxSize: maxSize,
	}
}

func makeTextCacheKey(font *ttf.Font, text string, color sdl.Color) textCacheKey {
	return textCacheKey{font: font, text: text, color: color}
}

// Get retrieves a cached texture. Returns nil if not found.
func (tc *TextCache) Get(font *ttf.Font, text string, color sdl.Color) *textCacheEntry {
	key := makeTextCacheKey(font, text, color)
	tc.mu.Lock()
	defer tc.mu.Unlock()
	entry, ok := tc.entries[key]
	if !ok {
		return nil
	}
	tc.order.MoveToFront(entry.element)
	return entry
}

// Put adds a texture to the cache, evicting LRU entries if needed.
func (tc *TextCache) Put(font *ttf.Font, text string, color sdl.Color, texture *sdl.Texture, w, h int32) {
	key := makeTextCacheKey(font, text, color)
	tc.mu.Lock()
	defer tc.mu.Unlock()

	entryBytes := int64(w) * int64(h) * 4

	// If already exists, update it
	if entry, ok := tc.entries[key]; ok {
		if entry.texture != nil {
			entry.texture.Destroy()
		}
		tc.totalBytes += entryBytes - entry.bytes
		entry.texture = texture
		entry.width = w
		entry.height = h
		entry.bytes = entryBytes
		tc.order.MoveToFront(entry.element)
		return
	}

	// Evict LRU while at capacity.
	for len(tc.entries) >= tc.maxSize {
		oldest := tc.order.Back()
		if oldest == nil {
			break
		}
		evictKey := oldest.Value.(textCacheKey)
		tc.order.Remove(oldest)
		if entry, ok := tc.entries[evictKey]; ok {
			if entry.texture != nil {
				entry.texture.Destroy()
			}
			tc.totalBytes -= entry.bytes
			delete(tc.entries, evictKey)
		}
	}

	entry := &textCacheEntry{
		texture: texture,
		width:   w,
		height:  h,
		bytes:   entryBytes,
	}
	entry.element = tc.order.PushFront(key)
	tc.entries[key] = entry
	tc.totalBytes += entryBytes
}

// Clear destroys all cached textures and resets the cache.
func (tc *TextCache) Clear() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for key, entry := range tc.entries {
		if entry.texture != nil {
			entry.texture.Destroy()
		}
		delete(tc.entries, key)
	}
	tc.order.Init()
	tc.totalBytes = 0
}

// Len returns the current number of cached entries.
func (tc *TextCache) Len() int {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return len(tc.entries)
}

// Bytes returns the approximate texture memory held by the cache (RGBA).
func (tc *TextCache) Bytes() int64 {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.totalBytes
}

// TrimTo evicts LRU entries until the cache is at most keepEntries in size.
// This is called under memory pressure to release GPU textures.
func (tc *TextCache) TrimTo(keepEntries int) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for len(tc.entries) > keepEntries {
		oldest := tc.order.Back()
		if oldest == nil {
			return
		}
		evictKey := oldest.Value.(textCacheKey)
		tc.order.Remove(oldest)
		if entry, ok := tc.entries[evictKey]; ok {
			if entry.texture != nil {
				entry.texture.Destroy()
			}
			tc.totalBytes -= entry.bytes
			delete(tc.entries, evictKey)
		}
	}
}

// --- Global text cache instances ---

var (
	// globalTextCache is the main text texture cache.
	// On TSP: 512 entries (conservative for 1GB RAM).
	// On Windows: 2048 entries (plenty of memory).
	globalTextCache *TextCache

	textCacheOnce sync.Once
)

// GetTextCache returns the global text texture cache, creating it on first use.
func GetTextCache() *TextCache {
	textCacheOnce.Do(func() {
		maxSize := 512
		if IsWindows() {
			maxSize = 2048
		}
		globalTextCache = NewTextCache(maxSize)
	})
	return globalTextCache
}

// ClearTextCache destroys all cached textures. Call on theme change or shutdown.
func ClearTextCache() {
	if globalTextCache != nil {
		globalTextCache.Clear()
	}
	// Wrapped text depends on the font metrics behind these textures, so the
	// launcher's cached labels go with them.
	clearAppLabelCache()
}
