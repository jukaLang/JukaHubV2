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
//
// It is bounded twice: by entry count, and by the texture bytes those entries
// hold. The count alone was not enough — most labels are a few KB, but this is
// also where a wrapped web-page paragraph, a PDF line, a terminal row or a chat
// bubble lands, and one of those can be a megabyte. 512 such entries were a
// multi-hundred-megabyte heap on a 1 GB device; the byte budget makes the worst
// case a fixed number the texture accounting can report.
type TextCache struct {
	mu         sync.RWMutex
	entries    map[textCacheKey]*textCacheEntry
	order      *list.List // front = most recently used
	maxSize    int
	maxBytes   int64 // 0 means "no byte budget" (unit tests)
	totalBytes int64 // approximate texture memory (RGBA), for diagnostics
}

// NewTextCache creates a cache with the given maximum entries.
// A non-positive limit is clamped to 1: a zero-sized cache would otherwise
// never evict (the eviction loop needs an entry to remove) and grow without
// bound while every Put destroyed nothing.
func NewTextCache(maxSize int) *TextCache {
	return NewTextCacheWithBudget(maxSize, 0)
}

// NewTextCacheWithBudget creates a cache bounded by entries and by bytes.
// A non-positive maxBytes leaves the byte side unbounded, which is what the
// unit tests of the LRU order want; the app always passes a budget.
func NewTextCacheWithBudget(maxSize int, maxBytes int64) *TextCache {
	if maxSize < 1 {
		maxSize = 1
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	return &TextCache{
		entries:  make(map[textCacheKey]*textCacheEntry, maxSize),
		order:    list.New(),
		maxSize:  maxSize,
		maxBytes: maxBytes,
	}
}

// The rendered-text ceilings. The handheld shares its 1 GB with the video
// decoder and SDL's frame pools, so it gets the tighter budget; a desktop can
// hold a bigger one for free. Named so the harness can assert it measured the
// handheld numbers rather than whichever branch the host took.
const (
	textBudgetHandheldBytes = int64(24 << 20)
	textBudgetDesktopBytes  = int64(64 << 20)
)

// textCacheBudgetBytes is the texture-memory ceiling for rendered text.
func textCacheBudgetBytes() int64 {
	return sceneBudgetByDevice(textBudgetHandheldBytes, textBudgetDesktopBytes)
}

// Limit reports the byte budget (0 when unbounded), for the texture log.
func (tc *TextCache) Limit() int64 {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.maxBytes
}

// SetBudget moves a live cache's byte ceiling and evicts down to it.
//
// The cache is a package singleton built on first use with the budget the
// machine reports, so a caller that has to measure another device's ceiling —
// the scene-navigation harness on a desktop — cannot get there by changing the
// budget function alone: the instance already exists. Ordinary code never calls
// this; the count cap and the device branch already decide the ceiling.
func (tc *TextCache) SetBudget(maxBytes int64) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.maxBytes = maxBytes
	// keep=0: nothing is being drawn across a budget change, so every entry is
	// fair game. A single label bigger than the whole budget still fits next
	// time it is inserted, which is the Put path's job.
	tc.evictLocked(0)
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
		tc.evictLocked(1)
		return
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
	// Evict after inserting, down to both caps. The entry just stored is the
	// label being drawn this frame, so it is never the one evicted.
	tc.evictLocked(1)
}

// evictLocked drops least-recently-used entries until the cache fits both its
// entry cap and its byte budget. keep is how many entries must survive (the
// one just inserted, so a single oversized label still gets cached).
func (tc *TextCache) evictLocked(keep int) {
	for tc.order.Len() > keep &&
		(len(tc.entries) > tc.maxSize || (tc.maxBytes > 0 && tc.totalBytes > tc.maxBytes)) {
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
		globalTextCache = NewTextCacheWithBudget(maxSize, textCacheBudgetBytes())
	})
	return globalTextCache
}

// --- Fitted-text cache ---

// fitTextToWidth binary-searches with TTF_SizeUTF8, which is a cgo call into
// FreeType: on the Smart Pro one label costs several of them, and the home
// frame measured ~40% of its time inside ellipsize because every card, chip
// and footer hint re-clipped the same unchanged label every frame.
//
// This cache keeps the answer per (font, text, width). The key holds the font
// pointer, not its contents: a theme change loads new fonts, so new keys are
// made and the old ones age out.
type textFitKey struct {
	font *ttf.Font
	text string
	maxW int32
}

var (
	textFitMu      sync.Mutex
	textFitEntries = make(map[textFitKey]string, 256)
	textFitOrder   = make([]textFitKey, 0, 256)
	textFitHits    int64
	textFitMisses  int64
)

// maxTextFitEntries bounds the cache. Labels are short and the working set is
// one screen's worth, so a few hundred entries cover every repeated frame.
const maxTextFitEntries = 512

// maxTextFitLen keeps one pathological string (a log line, a URL) from
// occupying the cache with copies.
const maxTextFitLen = 240

// fitTextCached returns fitTextToWidth's result from the cache when the same
// measurement has already been made.
func fitTextCached(font *ttf.Font, text string, maxW int32) string {
	if font == nil || maxW <= 0 || len(text) > maxTextFitLen {
		return fitTextToWidth(font, text, maxW)
	}
	key := textFitKey{font: font, text: text, maxW: maxW}
	textFitMu.Lock()
	if got, ok := textFitEntries[key]; ok {
		textFitHits++
		textFitMu.Unlock()
		return got
	}
	textFitMu.Unlock()

	fitted := fitTextToWidth(font, text, maxW)

	textFitMu.Lock()
	textFitMisses++
	if _, ok := textFitEntries[key]; !ok {
		if len(textFitEntries) >= maxTextFitEntries {
			// FIFO: drop the oldest half in one pass. Rebuilding a screen's
			// labels is a handful of misses, far cheaper than an LRU list.
			drop := textFitOrder[:len(textFitOrder)/2]
			for _, k := range drop {
				delete(textFitEntries, k)
			}
			textFitOrder = append(textFitOrder[:0], textFitOrder[len(drop):]...)
		}
		textFitEntries[key] = fitted
		textFitOrder = append(textFitOrder, key)
	}
	textFitMu.Unlock()
	return fitted
}

// clearTextFitCache forgets every fitted label. It runs with the text textures
// because both depend on the loaded font faces.
func clearTextFitCache() {
	textFitMu.Lock()
	textFitEntries = make(map[textFitKey]string, 256)
	textFitOrder = textFitOrder[:0]
	textFitHits, textFitMisses = 0, 0
	textFitMu.Unlock()

	textMeasureMu.Lock()
	textMeasureEntries = make(map[textMeasureKey]textMeasure, 256)
	textMeasureOrder = textMeasureOrder[:0]
	textMeasureHits = 0
	textMeasureMu.Unlock()

	sanitizeMu.Lock()
	sanitizeEntries = make(map[textMeasureKey]string, 256)
	sanitizeOrder = sanitizeOrder[:0]
	sanitizeHits = 0
	sanitizeMu.Unlock()
}

// textFitStats reports the cache's hit and miss counts (diagnostics).
func textFitStats() (hits, misses int64, entries int) {
	textFitMu.Lock()
	defer textFitMu.Unlock()
	return textFitHits, textFitMisses, len(textFitEntries)
}

// textMeasureKey identifies a plain measurement: the font and the exact string.
type textMeasureKey struct {
	font *ttf.Font
	text string
}

type textMeasure struct {
	w, h int32
}

var (
	textMeasureMu      sync.Mutex
	textMeasureEntries = make(map[textMeasureKey]textMeasure, 256)
	textMeasureOrder   = make([]textMeasureKey, 0, 256)
	textMeasureHits    int64
)

// maxTextMeasureEntries bounds the width cache. Layout code measures the same
// few dozen strings every frame (footer hints, card titles, the status line),
// so a few hundred entries carry a whole screen.
const maxTextMeasureEntries = 1024

// measureText is font.SizeUTF8 with the answer remembered. Layout code asks
// for the same strings on every frame — the home footer alone measured eight
// of them per frame — and each call is a FreeType trip through cgo.
func measureText(font *ttf.Font, text string) (int32, int32) {
	if font == nil || text == "" {
		return 0, 0
	}
	if len(text) > maxTextFitLen {
		w, h, err := font.SizeUTF8(text)
		if err != nil {
			return 0, 0
		}
		return int32(w), int32(h)
	}
	key := textMeasureKey{font: font, text: text}
	textMeasureMu.Lock()
	if got, ok := textMeasureEntries[key]; ok {
		textMeasureHits++
		textMeasureMu.Unlock()
		return got.w, got.h
	}
	textMeasureMu.Unlock()

	w, h, err := font.SizeUTF8(text)
	if err != nil {
		return 0, 0
	}
	m := textMeasure{w: int32(w), h: int32(h)}

	textMeasureMu.Lock()
	if _, ok := textMeasureEntries[key]; !ok {
		if len(textMeasureEntries) >= maxTextMeasureEntries {
			drop := textMeasureOrder[:len(textMeasureOrder)/2]
			for _, k := range drop {
				delete(textMeasureEntries, k)
			}
			textMeasureOrder = append(textMeasureOrder[:0], textMeasureOrder[len(drop):]...)
		}
		textMeasureEntries[key] = m
		textMeasureOrder = append(textMeasureOrder, key)
	}
	textMeasureMu.Unlock()
	return m.w, m.h
}

// textMeasureStats reports the width cache's hit count and size.
func textMeasureStats() (hits int64, entries int) {
	textMeasureMu.Lock()
	defer textMeasureMu.Unlock()
	return textMeasureHits, len(textMeasureEntries)
}

var (
	sanitizeMu      sync.Mutex
	sanitizeEntries = make(map[textMeasureKey]string, 256)
	sanitizeOrder   = make([]textMeasureKey, 0, 256)
	sanitizeHits    int64
)

// maxSanitizeEntries bounds the substitution cache.
const maxSanitizeEntries = 512

// sanitizeCached is sanitizeForFont with the answer remembered. Rendering one
// frame of static labels ran the character-coverage walk for every string,
// every frame: a cmap lookup plus a binary search per rune, which showed up as
// a fifth of the home frame's time on the desktop proxy and far more on the
// handheld. The result depends only on the face and the string.
func sanitizeCached(font *ttf.Font, text string) string {
	if font == nil || text == "" || len(text) > maxTextFitLen {
		return sanitizeForFont(font, text)
	}
	key := textMeasureKey{font: font, text: text}
	sanitizeMu.Lock()
	if got, ok := sanitizeEntries[key]; ok {
		sanitizeHits++
		sanitizeMu.Unlock()
		return got
	}
	sanitizeMu.Unlock()

	out := sanitizeForFont(font, text)

	sanitizeMu.Lock()
	if _, ok := sanitizeEntries[key]; !ok {
		if len(sanitizeEntries) >= maxSanitizeEntries {
			drop := sanitizeOrder[:len(sanitizeOrder)/2]
			for _, k := range drop {
				delete(sanitizeEntries, k)
			}
			sanitizeOrder = append(sanitizeOrder[:0], sanitizeOrder[len(drop):]...)
		}
		sanitizeEntries[key] = out
		sanitizeOrder = append(sanitizeOrder, key)
	}
	sanitizeMu.Unlock()
	return out
}

// sanitizeStats reports the substitution cache's hit count and size.
func sanitizeStats() (hits int64, entries int) {
	sanitizeMu.Lock()
	defer sanitizeMu.Unlock()
	return sanitizeHits, len(sanitizeEntries)
}

// ClearTextCache destroys all cached textures. Call on theme change or shutdown.
func ClearTextCache() {
	if globalTextCache != nil {
		globalTextCache.Clear()
	}
	// Wrapped text depends on the font metrics behind these textures, so the
	// launcher's cached labels go with them.
	clearAppLabelCache()
	clearTextFitCache()
}
