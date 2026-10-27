package main

// Regression tests for the per-frame cost fixes. They fail if the caches start
// doing disk I/O or holding unbounded state again.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The File Explorer toolbar redraws every frame; its path must come from the
// cache instead of stat-ing the SD card. Removing the directory proves the
// value is cached (a fresh stat would fall back to the root).
func TestFileExplorerToolbarPathIsCached(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "Music")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	prevRaw, prevDisplay := feDisplayCache.raw, feDisplayCache.display
	t.Cleanup(func() {
		feDisplayCache.raw, feDisplayCache.display = prevRaw, prevDisplay
	})
	feDisplayCache.raw, feDisplayCache.display = "", ""

	config := &Config{Variables: Variables{FileExplorerRoot: dir, Custom: map[string]interface{}{}}}
	config.Variables.Custom["fe_path"] = sub
	if got := feDisplayPath(config); !samePath(got, sub) {
		t.Fatalf("display path = %q, want %q", got, sub)
	}
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	if got := feDisplayPath(config); !samePath(got, sub) {
		t.Fatalf("display path after removal = %q, want the cached %q", got, sub)
	}

	// A new directory must invalidate the cached display value.
	other := filepath.Join(dir, "Videos")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	config.Variables.Custom["fe_path"] = other
	if got := feDisplayPath(config); !samePath(got, other) {
		t.Fatalf("display path after navigation = %q, want %q", got, other)
	}
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

// Chat images used to accumulate one texture and one downloaded buffer per
// image, for the life of the process. The cache is bounded by count and, since
// the cache-budget pass, by bytes: sixteen textures is not a ceiling when one
// photo can be 48 MB of decoded RGBA.
func TestChatImageCacheIsBounded(t *testing.T) {
	chatImageMutex.Lock()
	prevImages := chatImages
	chatImages = newByteOrderedStore[string, chatImageTexture](
		chatImageCacheMax, 0,
		func(t chatImageTexture) int64 { return t.bytes },
		nil,
	)
	chatImageMutex.Unlock()
	t.Cleanup(func() {
		chatImageMutex.Lock()
		chatImages = prevImages
		chatImageMutex.Unlock()
	})

	chatImageMutex.Lock()
	for i := 0; i < chatImageCacheMax+12; i++ {
		url := "http://example.com/" + strconv.Itoa(i) + ".png"
		chatImages.put(url, chatImageTexture{})
	}
	size, _ := chatImages.stats()
	_, oldestKept := chatImages.get("http://example.com/0.png")
	_, newestKept := chatImages.get("http://example.com/" + strconv.Itoa(chatImageCacheMax+11) + ".png")
	chatImageMutex.Unlock()

	if size > chatImageCacheMax {
		t.Fatalf("chat image cache holds %d textures, want at most %d", size, chatImageCacheMax)
	}
	if oldestKept {
		t.Fatal("the oldest chat image was not evicted")
	}
	if !newestKept {
		t.Fatal("the newest chat image was evicted")
	}
}

// Low Power is the default on the handhelds; the animated backdrop must drop
// to its reduced variant instead of drawing every blob and particle.
func TestDynamicBackgroundRespectsLowPower(t *testing.T) {
	prev := appConfig
	t.Cleanup(func() { appConfig = prev })

	appConfig = &Config{Variables: Variables{LowPower: true}}
	if !dynamicBgLowPower() {
		t.Fatal("dynamicBgLowPower = false with LowPower set")
	}
	appConfig.Variables.LowPower = false
	if dynamicBgLowPower() {
		t.Fatal("dynamicBgLowPower = true with LowPower clear")
	}
	appConfig = nil
	if dynamicBgLowPower() {
		t.Fatal("dynamicBgLowPower = true with no config")
	}
}
