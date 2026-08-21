package main

import (
	"fmt"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// textCacheEntry holds a pre-rendered text texture.
type textCacheEntry struct {
	texture *sdl.Texture
	width   int32
	height  int32
}

// TextCache is an LRU texture cache for rendered text.
// On TSP (1GB RAM), we cap at 512 entries to avoid memory pressure.
type TextCache struct {
	mu      sync.RWMutex
	entries map[string]*textCacheEntry
	order   []string // LRU order: least recently used at index 0
	maxSize int
}

// NewTextCache creates a cache with the given maximum entries.
func NewTextCache(maxSize int) *TextCache {
	return &TextCache{
		entries: make(map[string]*textCacheEntry, maxSize),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
	}
}

// textCacheKey generates a cache key from font, text, and color.
func textCacheKey(font *ttf.Font, text string, color sdl.Color) string {
	// Use the font pointer's address string + text + color bytes. A simple
	// string(rune(uintptr)) conversion is not portable (Windows disallows
	// pointer→uintptr without unsafe), and it produced duplicate keys for
	// different fonts on 32-bit addresses.
	fontAddr := fmt.Sprintf("%p", font)
	return fontAddr + "|" + text + "|" +
		string([]byte{color.R, color.G, color.B, color.A})
}

// Get retrieves a cached texture. Returns nil if not found.
func (tc *TextCache) Get(font *ttf.Font, text string, color sdl.Color) *textCacheEntry {
	key := textCacheKey(font, text, color)
	tc.mu.RLock()
	entry, ok := tc.entries[key]
	tc.mu.RUnlock()
	if !ok {
		return nil
	}
	// Move to end of LRU (most recently used)
	tc.mu.Lock()
	tc.touchLocked(key)
	tc.mu.Unlock()
	return entry
}

// Put adds a texture to the cache, evicting LRU entries if needed.
func (tc *TextCache) Put(font *ttf.Font, text string, color sdl.Color, texture *sdl.Texture, w, h int32) {
	key := textCacheKey(font, text, color)
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// If already exists, update it
	if entry, ok := tc.entries[key]; ok {
		if entry.texture != nil {
			entry.texture.Destroy()
		}
		entry.texture = texture
		entry.width = w
		entry.height = h
		tc.touchLocked(key)
		return
	}

	// Evict LRU if at capacity
	for len(tc.order) >= tc.maxSize && len(tc.order) > 0 {
		evictKey := tc.order[0]
		tc.order = tc.order[1:]
		if entry, ok := tc.entries[evictKey]; ok {
			if entry.texture != nil {
				entry.texture.Destroy()
			}
			delete(tc.entries, evictKey)
		}
	}

	// Add new entry
	tc.entries[key] = &textCacheEntry{
		texture: texture,
		width:   w,
		height:  h,
	}
	tc.order = append(tc.order, key)
}

// touchLocked moves a key to the end of the LRU order (must hold write lock).
func (tc *TextCache) touchLocked(key string) {
	for i, k := range tc.order {
		if k == key {
			tc.order = append(tc.order[:i], tc.order[i+1:]...)
			tc.order = append(tc.order, key)
			return
		}
	}
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
	tc.order = tc.order[:0]
}

// Len returns the current number of cached entries.
func (tc *TextCache) Len() int {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return len(tc.entries)
}

// TrimTo evicts LRU entries until the cache is at most keepEntries in size.
// This is called under memory pressure to release GPU textures.
func (tc *TextCache) TrimTo(keepEntries int) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for len(tc.order) > keepEntries && len(tc.order) > 0 {
		evictKey := tc.order[0]
		tc.order = tc.order[1:]
		if entry, ok := tc.entries[evictKey]; ok {
			if entry.texture != nil {
				entry.texture.Destroy()
			}
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
}
