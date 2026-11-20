package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestStoreShotTextureBudgetProtectsCurrentFrame(t *testing.T) {
	entries := map[string]*pstoreTexEntry{
		"old":     {used: 2, bytes: 6},
		"visible": {used: 5, bytes: 4},
	}
	used := int64(10)
	if !storeShotTextureMakeRoom(entries, &used, 40, 10, 5, 5) {
		t.Fatal("an old screenshot should make room for a new one")
	}
	if _, ok := entries["old"]; ok || used != 4 {
		t.Fatalf("old screenshot was not evicted: used=%d entries=%v", used, entries)
	}
	if storeShotTextureMakeRoom(entries, &used, 40, 8, 5, 5) {
		t.Fatal("a screenshot drawn this frame must not be evicted")
	}
	if used != 4 || len(entries) != 1 {
		t.Fatal("refusing the new screenshot changed the visible one")
	}
	if storeShotTextureMakeRoom(entries, &used, 40, 8, 9, 6) {
		t.Fatal("a single screenshot larger than the byte budget was accepted")
	}
}

func TestThumbnailTextureCacheAlsoBoundsEntryCount(t *testing.T) {
	cache := newThumbnailTextureCache(1 << 20)
	cache.maxEntries = 2
	destroyed := 0
	cache.destroy = func(*sdl.Texture) { destroyed++ }
	cache.Put("first", nil, 1, 1)
	cache.Put("second", nil, 1, 1)
	cache.Put("third", nil, 1, 1)
	if _, ok := cache.entries["first"]; ok {
		t.Fatal("oldest thumbnail survived the entry ceiling")
	}
	if cache.Len() != 2 || cache.Bytes() != 8 || destroyed != 1 {
		t.Fatalf("entry ceiling failed: entries=%d bytes=%d destroyed=%d", cache.Len(), cache.Bytes(), destroyed)
	}
	cache.ClearKeys(map[string]struct{}{"second": {}})
	if cache.Len() != 1 || cache.Bytes() != 4 || destroyed != 2 {
		t.Fatalf("scene thumbnail cleanup failed: entries=%d bytes=%d destroyed=%d", cache.Len(), cache.Bytes(), destroyed)
	}
}

func TestMediaThumbTrackingPrunesOnlyUncachedKeys(t *testing.T) {
	const texKey = "media-prune-live-texture.jpg"
	const dataKey = "media-prune-live-data.jpg"
	const pendingKey = "media-prune-pending.jpg"
	const staleKey = "media-prune-stale.jpg"
	oldTracked, oldAsked := mediaThumbKeys, mediaThumbAsked
	cache := getThumbnailTextures()
	cache.Put(texKey, nil, 1, 1)
	thumbnailDataSet(dataKey, []byte{1})
	t.Cleanup(func() {
		cache.ClearKeys(map[string]struct{}{texKey: {}})
		clearThumbnailDataKeys(map[string]struct{}{dataKey: {}})
		mediaThumbKeys, mediaThumbAsked = oldTracked, oldAsked
	})
	mediaThumbKeys = map[string]struct{}{texKey: {}, dataKey: {}, pendingKey: {}, staleKey: {}}
	mediaThumbAsked = map[string]uint64{pendingKey: 1}

	mediaPruneThumbKeys()
	for _, key := range []string{texKey, dataKey, pendingKey} {
		if _, ok := mediaThumbKeys[key]; !ok {
			t.Fatalf("still-live thumbnail %q lost its exit tracking", key)
		}
	}
	if _, ok := mediaThumbKeys[staleKey]; ok {
		t.Fatal("stale thumbnail still consumes tracking memory")
	}
}
