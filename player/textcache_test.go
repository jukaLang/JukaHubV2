package main

import (
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
