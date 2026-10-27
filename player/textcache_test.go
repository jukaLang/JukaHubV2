package main

import (
	"fmt"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

var textCacheTestColor = sdl.Color{R: 255, G: 255, B: 255, A: 255}

// Nil textures are valid here: the cache only destroys them when present, so
// eviction logic can be tested without an SDL renderer.
func putText(tc *TextCache, text string) {
	tc.Put(nil, text, textCacheTestColor, nil, 10, 10)
}

func hasText(tc *TextCache, text string) bool {
	return tc.Get(nil, text, textCacheTestColor) != nil
}

// At capacity the least recently used entry is evicted, not the oldest one.
func TestTextCacheEvictsLRU(t *testing.T) {
	tc := NewTextCache(3)
	putText(tc, "a")
	putText(tc, "b")
	putText(tc, "c")

	// Touch "a" so "b" becomes the least recently used entry.
	if !hasText(tc, "a") {
		t.Fatal("a missing before eviction")
	}

	putText(tc, "d")

	if tc.Len() != 3 {
		t.Fatalf("Len = %d, want 3", tc.Len())
	}
	if hasText(tc, "b") {
		t.Error("b should have been evicted as least recently used")
	}
	for _, key := range []string{"a", "c", "d"} {
		if !hasText(tc, key) {
			t.Errorf("%q should still be cached", key)
		}
	}
}

// TrimTo releases the oldest entries first and keeps the most recent ones.
func TestTextCacheTrimToKeepsNewest(t *testing.T) {
	tc := NewTextCache(10)
	for _, s := range []string{"a", "b", "c", "d"} {
		putText(tc, s)
	}
	if !hasText(tc, "a") {
		t.Fatal("a missing")
	}
	tc.TrimTo(2)
	if tc.Len() != 2 {
		t.Fatalf("Len = %d after TrimTo(2), want 2", tc.Len())
	}
	if hasText(tc, "b") || hasText(tc, "c") {
		t.Error("TrimTo evicted the wrong entries")
	}
	if !hasText(tc, "a") || !hasText(tc, "d") {
		t.Error("TrimTo should keep the most recently used entries")
	}
}

func TestTextCacheClearEmpties(t *testing.T) {
	tc := NewTextCache(4)
	putText(tc, "a")
	putText(tc, "b")
	tc.Clear()
	if tc.Len() != 0 {
		t.Fatalf("Len = %d after Clear, want 0", tc.Len())
	}
	if hasText(tc, "a") {
		t.Error("Clear left a cached entry")
	}
	putText(tc, "c")
	if !hasText(tc, "c") || tc.Len() != 1 {
		t.Error("cache unusable after Clear")
	}
}

// A non-positive limit must not turn into an unbounded cache.
func TestTextCacheClampsZeroCapacity(t *testing.T) {
	for _, size := range []int{0, -5} {
		tc := NewTextCache(size)
		if tc.maxSize < 1 {
			t.Fatalf("maxSize = %d for requested %d, want at least 1", tc.maxSize, size)
		}
		for _, s := range []string{"a", "b", "c"} {
			putText(tc, s)
		}
		if tc.Len() > 1 {
			t.Errorf("Len = %d with capacity %d, want at most 1", tc.Len(), size)
		}
	}
}

// The byte budget is the cap that actually matters for text: a wrapped web
// paragraph or a chat bubble is far larger than an ordinary label, so a cache
// bounded only by entry count can hold tens of megabytes.
func TestTextCacheEvictsByByteBudget(t *testing.T) {
	const entryPx = 20 // 20x20 RGBA = 1600 bytes per entry
	budget := int64(10 * entryPx * entryPx * 4)
	tc := NewTextCacheWithBudget(500, budget)

	first := "label-0"
	newest := first
	for i := 0; i < 40; i++ {
		newest = fmt.Sprintf("label-%d", i)
		tc.Put(nil, newest, textCacheTestColor, nil, entryPx, entryPx)
		if got := tc.Bytes(); got > budget {
			t.Fatalf("after %d puts Bytes = %d, budget is %d", i+1, got, budget)
		}
	}
	wantEntries := int(budget / (entryPx * entryPx * 4))
	if tc.Len() != wantEntries {
		t.Fatalf("Len = %d, want %d entries for the budget", tc.Len(), wantEntries)
	}
	// The eviction is LRU: the newest label survives, the first one is gone.
	if !hasText(tc, newest) {
		t.Errorf("newest label %q was evicted", newest)
	}
	if hasText(tc, first) {
		t.Error("the first label should have aged out of the byte budget")
	}
}

// A label bigger than the whole budget is still cached: it is the string being
// drawn this frame, and refusing to cache it would re-rasterise it every frame.
func TestTextCacheKeepsOneOversizedLabel(t *testing.T) {
	tc := NewTextCacheWithBudget(8, 1024)
	tc.Put(nil, "wide", textCacheTestColor, nil, 100, 100) // 40000 bytes
	if !hasText(tc, "wide") {
		t.Fatal("an oversized label was not cached")
	}
	if tc.Len() != 1 {
		t.Fatalf("Len = %d, want just the oversized label", tc.Len())
	}
}

// The live cache used by every drawn string must carry a real budget: an
// unbounded one is the regression this guards.
func TestAppTextCacheHasABudget(t *testing.T) {
	cache := GetTextCache()
	if cache == nil {
		t.Fatal("GetTextCache returned nil")
	}
	want := textCacheBudgetBytes()
	if got := cache.Limit(); got != want || got <= 0 {
		t.Fatalf("text cache budget = %d, want %d", got, want)
	}
	if want > 128<<20 {
		t.Fatalf("budget %d MB is larger than the handheld could ever afford", want>>20)
	}
}

// SetBudget is how the memory harness puts the live singleton on the handheld
// ceiling: the cache already exists and was built with the host's number, so the
// seam in front of textCacheBudgetBytes cannot reach it. Lowering the ceiling
// evicts immediately rather than on the next Put — a harness that measured a
// half-tightened cache would report a limit it is not enforcing — and raising it
// afterwards never brings an evicted label back.
func TestTextCacheSetBudgetEvictsDownToTheNewCeiling(t *testing.T) {
	tc := NewTextCacheWithBudget(64, 1<<20)
	for i := 0; i < 32; i++ {
		tc.Put(nil, fmt.Sprintf("label-%d", i), textCacheTestColor, nil, 100, 100) // 40 KB each
	}
	if tc.Bytes() == 0 {
		t.Fatal("the fixture cached nothing")
	}

	tc.SetBudget(80 << 10) // two labels
	if got := tc.Limit(); got != 80<<10 {
		t.Fatalf("Limit = %d, want the 80 KB it was set to", got)
	}
	if got := tc.Bytes(); got > 80<<10 {
		t.Fatalf("the cache holds %d bytes after the budget was lowered to %d", got, 80<<10)
	}
	if got := tc.Len(); got > 2 {
		t.Fatalf("%d entries survived an 80 KB budget of 40 KB labels", got)
	}
	kept := tc.Len()

	tc.SetBudget(1 << 20)
	if tc.Len() != kept {
		t.Fatalf("raising the budget changed the entry count %d→%d: evicted labels came back", kept, tc.Len())
	}
}

// Key identity covers text and color, so a theme change cannot serve stale
// textures painted in the previous color.
func TestTextCacheKeysIncludeTextAndColor(t *testing.T) {
	tc := NewTextCache(4)
	tc.Put(nil, "same", textCacheTestColor, nil, 10, 10)
	other := sdl.Color{R: 10, G: 20, B: 30, A: 255}
	if tc.Get(nil, "same", other) != nil {
		t.Error("a different color must be a different cache entry")
	}
	if tc.Get(nil, "other", textCacheTestColor) != nil {
		t.Error("a different string must be a different cache entry")
	}
}
