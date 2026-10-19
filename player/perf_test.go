package main

// Tests for the low-memory device tuning in perf.go and the bounded texture
// caches. These are pure-logic checks: no renderer or device is required.

import (
	"container/list"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestGCSettingsForDevice(t *testing.T) {
	tests := []struct {
		name     string
		ramMB    uint64
		handheld bool
		ok       bool
		percent  int
		limitMB  int64
	}{
		{"handheld with unknown RAM", 0, true, true, 60, 384},
		{"1GB linux", 1024, false, true, 60, 384},
		{"2GB linux", 2048, false, true, 80, 768},
		{"8GB desktop", 8192, false, false, 0, 0},
		{"unknown desktop", 0, false, false, 0, 0},
		{"4GB handheld", 4096, true, false, 0, 0},
	}
	for _, tc := range tests {
		got, ok := gcSettingsFor(tc.ramMB, tc.handheld)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.Percent != tc.percent || got.MemoryLimit != tc.limitMB<<20 {
			t.Errorf("%s: settings = %+v, want GOGC=%d limit=%dMB", tc.name, got, tc.percent, tc.limitMB)
		}
		// A limit under 256 MB risks a GC death spiral once SDL textures and
		// the current video frame are accounted for.
		if got.MemoryLimit < 256<<20 {
			t.Errorf("%s: memory limit %d MB is too tight", tc.name, got.MemoryLimit>>20)
		}
	}
}

func TestCachePressureEvictorDebounces(t *testing.T) {
	var e cachePressureEvictor
	if !e.shouldAct(MemCritical, 1000) {
		t.Fatal("first critical observation must clean up")
	}
	if e.shouldAct(MemCritical, 1000+evictCriticalCooldownMS-1) {
		t.Error("repeated cleanup inside the critical cooldown")
	}
	if !e.shouldAct(MemCritical, 1000+evictCriticalCooldownMS) {
		t.Error("cleanup should resume after the critical cooldown")
	}

	// Escalation acts immediately, even right after a warning cleanup.
	if !e.shouldAct(MemWarning, 2000) {
		t.Fatal("warning cleanup should run on a new level")
	}
	if e.shouldAct(MemWarning, 2100) {
		t.Error("warning cleanup repeated inside its cooldown")
	}
	if !e.shouldAct(MemCritical, 2200) {
		t.Error("escalation to critical should act immediately")
	}

	// Returning to normal re-arms the evictor, so the next dip cleans up at
	// once instead of waiting out a stale cooldown.
	if e.shouldAct(MemNormal, 2300) {
		t.Error("normal pressure must not evict")
	}
	if !e.shouldAct(MemCritical, 2400) {
		t.Error("critical after normal should act immediately")
	}

	// A clock reset (suspend/resume, tests) must not deadlock cleanup for a
	// whole cooldown.
	if !e.shouldAct(MemCritical, 100) {
		t.Error("clock reset should allow cleanup")
	}
}

func TestVideoDecodeSize(t *testing.T) {
	tests := []struct {
		name             string
		w, h, maxW, maxH int32
		wantW, wantH     int32
	}{
		{"720p fits", 1280, 720, 1280, 720, 1280, 720},
		{"1080p capped to panel", 1920, 1080, 1280, 720, 1280, 720},
		{"4K capped to panel", 3840, 2160, 1280, 720, 1280, 720},
		{"small source untouched", 320, 180, 1280, 720, 320, 180},
		{"desktop cap", 3840, 2160, 1920, 1080, 1920, 1080},
		{"unknown size", 0, 0, 1280, 720, 0, 0},
	}
	for _, tc := range tests {
		gotW, gotH := videoDecodeSize(tc.w, tc.h, tc.maxW, tc.maxH)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Errorf("%s: videoDecodeSize(%d,%d) = %dx%d, want %dx%d",
				tc.name, tc.w, tc.h, gotW, gotH, tc.wantW, tc.wantH)
		}
		if gotW&1 != 0 || gotH&1 != 0 {
			t.Errorf("%s: %dx%d is not even; rawvideo needs even dimensions", tc.name, gotW, gotH)
		}
		if gotW > tc.maxW || gotH > tc.maxH {
			t.Errorf("%s: %dx%d exceeds the %dx%d box", tc.name, gotW, gotH, tc.maxW, tc.maxH)
		}
	}

	// Portrait video keeps its aspect ratio inside the landscape box.
	pw, ph := videoDecodeSize(1080, 1920, 1280, 720)
	if ph != 720 {
		t.Errorf("portrait height = %d, want 720", ph)
	}
	if want := int32(1080 * 720 / 1920); pw != want&^1 {
		t.Errorf("portrait width = %d, want %d", pw, want&^1)
	}
}

func TestThumbnailTextureCacheEvictsLRU(t *testing.T) {
	cache := newThumbnailTextureCache(1000) // 400 bytes per 10x10 RGBA texture
	cache.destroy = func(*sdl.Texture) {}

	cache.Put("a", nil, 10, 10)
	cache.Put("b", nil, 10, 10)
	if got := cache.Bytes(); got != 800 {
		t.Fatalf("bytes after two entries = %d, want 800", got)
	}
	// Touch "a" so "b" becomes the least recently used entry.
	cache.Get("a")
	cache.Put("c", nil, 10, 10)
	if got := cache.Len(); got != 2 {
		t.Errorf("cache holds %d entries, want 2 after eviction", got)
	}
	if cache.contains("b") {
		t.Error("least recently used entry b should have been evicted")
	}
	if !cache.contains("a") || !cache.contains("c") {
		t.Error("recently used entries a and c should have survived")
	}
	if got := cache.Bytes(); got > 1000 {
		t.Errorf("cache bytes = %d, over the 1000 byte budget", got)
	}

	// A single oversized texture is kept rather than returning a dangling
	// pointer to the caller that just loaded it.
	big := newThumbnailTextureCache(100)
	big.destroy = func(*sdl.Texture) {}
	big.Put("huge", nil, 100, 100) // 40 000 bytes
	if big.Len() != 1 {
		t.Errorf("oversized entry count = %d, want 1", big.Len())
	}

	// Replacing a key must not double-count its bytes.
	replace := newThumbnailTextureCache(1000)
	replace.destroy = func(*sdl.Texture) {}
	replace.Put("k", nil, 10, 10)
	replace.Put("k", nil, 10, 10)
	if got := replace.Bytes(); got != 400 {
		t.Errorf("bytes after replacing one key = %d, want 400", got)
	}

	// Clear releases everything and resets accounting.
	calls := 0
	clear := newThumbnailTextureCache(1000)
	clear.destroy = func(*sdl.Texture) { calls++ }
	clear.Put("x", nil, 10, 10)
	clear.Put("y", nil, 10, 10)
	clear.Clear()
	if clear.Len() != 0 || clear.Bytes() != 0 {
		t.Errorf("cache not empty after Clear: len=%d bytes=%d", clear.Len(), clear.Bytes())
	}
	if calls != 2 {
		t.Errorf("destroy called %d times, want 2", calls)
	}
}

func TestFFmpegDecoderArgsScaling(t *testing.T) {
	scaled := ffmpegDecoderArgs("input.mp4", 1280, 720, true, 0)
	if !containsArgPair(scaled, "-vf", "scale=1280:720:flags=bilinear") {
		t.Errorf("scaled decode args = %v, want a scale filter", scaled)
	}
	if containsArg(scaled, "-s") {
		t.Errorf("scaled decode args = %v, must not also set an output size", scaled)
	}

	native := ffmpegDecoderArgs("input.mp4", 1920, 1080, false, 12.5)
	if !containsArgPair(native, "-s", "1920x1080") {
		t.Errorf("native decode args = %v, want -s 1920x1080", native)
	}
	if containsArg(native, "-vf") {
		t.Errorf("native decode args = %v, must not add a scale filter", native)
	}
	if !containsArgPair(native, "-ss", "12.500") {
		t.Errorf("native decode args = %v, want the seek position", native)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// BenchmarkFramePublishCopy measures the per-frame memcpy the decoder used to
// do before handing a frame to the renderer (720p RGBA). BenchmarkFramePublish*
// exist to show the cost that the buffer swap removes; they are not run by the
// normal test suite.
func BenchmarkFramePublishCopy(b *testing.B) {
	const size = 1280 * 720 * 4
	src := make([]byte, size)
	dst := make([]byte, size)
	b.SetBytes(size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(dst, src)
	}
}

var benchBufferSink []byte

func BenchmarkFramePublishSwap(b *testing.B) {
	const size = 1280 * 720 * 4
	a := make([]byte, size)
	c := make([]byte, size)
	b.SetBytes(size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a, c = c, a
	}
	benchBufferSink = a
	benchBufferSink = c
}

// BenchmarkDecodedFrameBytes contrasts a 4K frame with the capped 720p frame.
func BenchmarkDecodedFrameBytes(b *testing.B) {
	for i := 0; i < b.N; i++ {
		w, h := videoDecodeSize(3840, 2160, 1280, 720)
		_ = int64(w) * int64(h) * 4
	}
}

func TestHomeTextCacheIsBounded(t *testing.T) {
	store := homeTextCacheStore{
		entries:  map[homeTextKey]*homeTextCacheEntry{},
		lru:      list.New(),
		maxBytes: 1000,
		maxItems: 2,
	}
	key := func(s string) homeTextKey { return homeTextKey{text: s} }
	store.put(key("a"), nil, 10, 10) // 400 bytes
	store.put(key("b"), nil, 10, 10)
	if len(store.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(store.entries))
	}
	// Touch "a", then insert a third entry: "b" is the LRU victim.
	store.lru.MoveToFront(store.entries[key("a")].elem)
	store.put(key("c"), nil, 10, 10)
	if _, ok := store.entries[key("b")]; ok {
		t.Error("least recently used label b should have been evicted")
	}
	if _, ok := store.entries[key("a")]; !ok {
		t.Error("recently used label a should have survived")
	}
	if got := store.bytes; got != 800 {
		t.Errorf("bytes = %d, want 800 after eviction", got)
	}

	// The byte budget evicts even when the item count is under the cap.
	small := homeTextCacheStore{
		entries:  map[homeTextKey]*homeTextCacheEntry{},
		lru:      list.New(),
		maxBytes: 500,
		maxItems: 100,
	}
	small.put(key("x"), nil, 10, 10)
	small.put(key("y"), nil, 10, 10)
	if got := small.bytes; got > 500 {
		t.Errorf("bytes = %d, over the 500 byte budget", got)
	}
	if len(small.entries) != 1 {
		t.Errorf("entries = %d, want 1 after byte eviction", len(small.entries))
	}
}

func TestTextureMemoryStatsTotal(t *testing.T) {
	s := textureMemoryStats{TextCache: 1, HomeText: 2, Thumbnails: 3, VideoOverlay: 4, VideoFrame: 5, UILayer: 6}
	if got := s.Total(); got != 21 {
		t.Errorf("Total() = %d, want 21", got)
	}
	// The accounting helper must be safe before any renderer has run.
	if got := collectTextureMemoryStats(); got.Total() < 0 {
		t.Errorf("collectTextureMemoryStats() = %d, want a non-negative total", got.Total())
	}
	logTextureMemoryStats("test") // exercises the profiling path used by JUKAHUB_TEXSTATS
}

func TestTextCacheTracksBytes(t *testing.T) {
	cache := NewTextCache(2)
	color := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	cache.Put(nil, "one", color, nil, 10, 10)
	cache.Put(nil, "two", color, nil, 10, 10)
	if got := cache.Bytes(); got != 800 {
		t.Fatalf("bytes after two entries = %d, want 800", got)
	}
	cache.Put(nil, "three", color, nil, 10, 10) // evicts "one"
	if got := cache.Bytes(); got != 800 {
		t.Errorf("bytes after eviction = %d, want 800", got)
	}
	cache.TrimTo(1)
	if got := cache.Bytes(); got != 400 {
		t.Errorf("bytes after TrimTo(1) = %d, want 400", got)
	}
	cache.Clear()
	if got := cache.Bytes(); got != 0 {
		t.Errorf("bytes after Clear = %d, want 0", got)
	}
}
