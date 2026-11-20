package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVisionCacheBoundsEncodedMedia(t *testing.T) {
	visionCache.mu.Lock()
	old := visionCache.entries
	visionCache.entries = newByteOrderedStore[string, visionCacheEntry](
		visionCacheMax, 4<<20, visionCacheEntryBytes, nil,
	)
	visionCache.mu.Unlock()
	t.Cleanup(func() {
		visionCache.mu.Lock()
		visionCache.entries = old
		visionCache.mu.Unlock()
	})

	for _, key := range []string{"first", "second"} {
		visionCachePut(visionCacheEntry{key: key, picture: jukaVisionImage{Data: make([]byte, 1<<20)}})
	}
	clip := visionCacheEntry{key: "clip", hasClip: true}
	for range 3 {
		clip.clip.frames = append(clip.clip.frames, jukaVisionImage{Data: make([]byte, 1<<20)})
	}
	visionCachePut(clip)
	if _, ok := visionCacheGet("first"); ok {
		t.Fatal("old picture survived the byte limit")
	}
	if got, ok := visionCacheGet("clip"); !ok || len(got.clip.frames) != 3 {
		t.Fatal("recent video frames were not cached")
	}
	visionCache.mu.Lock()
	_, bytes := visionCache.entries.stats()
	visionCache.mu.Unlock()
	if bytes > 4<<20 {
		t.Fatalf("vision cache holds %d bytes, over its 4 MB test limit", bytes)
	}

	visionCachePut(visionCacheEntry{key: "oversized", picture: jukaVisionImage{Data: make([]byte, 5<<20)}})
	if _, ok := visionCacheGet("oversized"); ok {
		t.Fatal("oversized item entered the cache")
	}
	visionCacheClear()
	visionCache.mu.Lock()
	entries, bytes := visionCache.entries.stats()
	visionCache.mu.Unlock()
	if entries != 0 || bytes != 0 {
		t.Fatalf("vision cache after clear: %d entries, %d bytes", entries, bytes)
	}
}

func TestVisionFrameReadHasSizeLimit(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.jpg")
	if err := os.WriteFile(small, []byte("frame"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := visionReadFrame(small); err != nil || string(data) != "frame" {
		t.Fatalf("small frame = %q, %v", data, err)
	}
	large := filepath.Join(dir, "large.jpg")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(visionFrameReadMaxBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := visionReadFrame(large); !errors.Is(err, errVisionTooLarge) {
		t.Fatalf("large frame error = %v, want size limit", err)
	}
}

func TestVisionCacheKeepsLatestAfterChat(t *testing.T) {
	visionCache.mu.Lock()
	old := visionCache.entries
	visionCache.entries = newByteOrderedStore[string, visionCacheEntry](
		visionCacheMax, 8<<20, visionCacheEntryBytes, nil,
	)
	visionCache.mu.Unlock()
	t.Cleanup(func() {
		visionCache.mu.Lock()
		visionCache.entries = old
		visionCache.mu.Unlock()
	})
	for _, key := range []string{"one", "two", "three"} {
		visionCachePut(visionCacheEntry{key: key, picture: jukaVisionImage{Data: make([]byte, 1<<20)}})
	}
	visionCacheKeepLatest()
	if _, ok := visionCacheGet("one"); ok {
		t.Fatal("old AI media survived Chat close")
	}
	if _, ok := visionCacheGet("two"); ok {
		t.Fatal("second AI media survived Chat close")
	}
	if _, ok := visionCacheGet("three"); !ok {
		t.Fatal("newest AI media was lost")
	}
}
