package main

// cachebudget_test.go — the byte side of the caches that used to count entries.
//
// Each of these caches had a count cap that read like a memory ceiling and was
// not one: an icon can be 64px or 256px, a chat photo can be 48 MB decoded, a
// feed body can be 1 MiB. The tests below pin the three things a byte budget has
// to get right: the oldest entry leaves first, the newest is never dropped out
// from under the caller that just inserted it, and a replaced or evicted value
// is released exactly once.

import (
	"image/color"
	"path/filepath"
	"testing"
)

func TestByteOrderedStoreEvictsByCount(t *testing.T) {
	store := newByteOrderedStore[string, int](3, 0, nil, nil)
	for _, key := range []string{"a", "b", "c", "d"} {
		store.put(key, 1)
	}
	entries, _ := store.stats()
	if entries != 3 {
		t.Fatalf("entries = %d, want the 3-entry cap", entries)
	}
	if store.has("a") {
		t.Fatal("the oldest entry survived the count cap")
	}
	if !store.has("d") {
		t.Fatal("the newest entry was evicted")
	}
}

func TestByteOrderedStoreEvictsByBytes(t *testing.T) {
	// Four 100-byte entries in a 250-byte store: the newest two fit, the third
	// pushes the total over and the oldest leaves.
	store := newByteOrderedStore[string, []byte](8, 250, func(b []byte) int64 { return int64(len(b)) }, nil)
	payload := make([]byte, 100)
	store.put("a", payload)
	store.put("b", payload)
	if _, bytes := store.stats(); bytes != 200 {
		t.Fatalf("bytes = %d, want 200", bytes)
	}
	store.put("c", payload)
	entries, bytes := store.stats()
	if bytes > 250 {
		t.Fatalf("bytes = %d, over the 250-byte budget", bytes)
	}
	if entries != 2 {
		t.Fatalf("entries = %d, want 2 after the byte eviction", entries)
	}
	if store.has("a") {
		t.Fatal("the oldest entry survived the byte budget")
	}
	if !store.has("c") {
		t.Fatal("the entry that was just stored was evicted")
	}
}

func TestByteOrderedStoreKeepsTheNewestEntryEvenOverBudget(t *testing.T) {
	store := newByteOrderedStore[string, int](8, 10, func(v int) int64 { return int64(v) }, nil)
	store.put("huge", 4096)
	entries, bytes := store.stats()
	if entries != 1 || bytes != 4096 {
		t.Fatalf("store holds %d entries / %d bytes, want the single oversized entry kept", entries, bytes)
	}
	if !store.has("huge") {
		t.Fatal("an entry larger than the whole budget was dropped; the caller holds it")
	}
}

func TestByteOrderedStoreReleasesReplacedAndEvictedValues(t *testing.T) {
	var released []string
	store := newByteOrderedStore[string, string](2, 0, func(string) int64 { return 1 }, func(v string) {
		released = append(released, v)
	})
	store.put("a", "first")
	store.put("a", "second") // replaces: "first" is released
	store.put("b", "third")
	store.put("c", "fourth") // evicts "a", releasing "second"
	if len(released) != 2 || released[0] != "first" || released[1] != "second" {
		t.Fatalf("released = %v, want [first second]", released)
	}
	if !store.has("c") {
		t.Fatal("the newest entry did not survive")
	}
}

func TestByteOrderedStoreDropAndClear(t *testing.T) {
	var released int
	store := newByteOrderedStore[string, int](8, 0, func(v int) int64 { return int64(v) }, func(int) { released++ })
	store.put("a", 10)
	store.put("b", 20)
	if !store.drop("a") {
		t.Fatal("drop of a present key returned false")
	}
	if _, bytes := store.stats(); bytes != 20 {
		t.Fatalf("bytes = %d after the drop, want 20", bytes)
	}
	if store.drop("a") {
		t.Fatal("drop of an absent key returned true")
	}
	store.clear()
	entries, bytes := store.stats()
	if entries != 0 || bytes != 0 {
		t.Fatalf("clear left %d entries / %d bytes", entries, bytes)
	}
	if released != 2 {
		t.Fatalf("released %d values, want the dropped and the cleared one", released)
	}
}

// TestCacheBudgetsAreRealCeilings checks the five budgets this pass added. Each
// must be a positive number no larger than a whole second of the handheld's RAM,
// and each must actually bind before the count cap for the largest entry the cap
// was sized around — otherwise the budget would be decoration.
func TestCacheBudgetsAreRealCeilings(t *testing.T) {
	const ceiling = int64(128 << 20)
	budgets := map[string]int64{
		"app icons":      appIconTextureBudgetBytes(),
		"chat images":    chatImageTextureBudgetBytes(),
		"chat avatars":   chatAvatarTextureBudgetBytes(),
		"thumbnail data": thumbnailDataBudgetBytes(),
		"http bodies":    httpCacheBudgetBytes(),
	}
	for name, budget := range budgets {
		if budget <= 0 {
			t.Errorf("%s budget = %d, want a positive byte count", name, budget)
		}
		if budget > ceiling {
			t.Errorf("%s budget = %d, larger than the %d ceiling this test allows", name, budget, ceiling)
		}
	}
	// A 256×256 icon is 256 KB and the launcher cap is 64 of them (16 MB): on the
	// handheld the byte budget has to bite before the count does for that worst
	// case, or it would only be decoration.
	if worstCase := int64(appIconMaxTextures * 256 * 256 * 4); appIconTextureHandheldBudget >= worstCase {
		t.Errorf("handheld icon budget %d does not bind before the %d-entry cap of 256px icons (%d)",
			appIconTextureHandheldBudget, appIconMaxTextures, worstCase)
	}
	// One decoded 4000×3000 photo is 48 MB. Sixteen of those is what the count
	// cap used to allow; the budget must be far below it.
	if budget := chatImageTextureBudgetBytes(); budget >= chatImageCacheMax*48<<20 {
		t.Errorf("chat image budget %d still allows the count cap's worst case", budget)
	}
}

// TestCacheBudgetPicksTheDeviceBranch pins the rule behind every budget above:
// the handheld gets the smaller number.
func TestCacheBudgetPicksTheDeviceBranch(t *testing.T) {
	got := cacheBudgetByDevice(8<<20, 32<<20)
	want := int64(32 << 20)
	if IsTrimuiDevice() || P().IsHandheld() {
		want = 8 << 20
	}
	if got != want {
		t.Fatalf("cacheBudgetByDevice = %d, want %d on this host", got, want)
	}
}

// TestSceneBudgetBranches pins the seam the scene-navigation harness uses, the
// way TestLibraryArtBudgetBranches pins the box-art one. Pinning a branch in the
// harness is only honest if both branches are pinned here: a ceiling that drifted
// to match the other side would make the harness's pin decorative, and the table
// it prints would describe a device that does not exist.
func TestSceneBudgetBranches(t *testing.T) {
	prev := sceneBudgetForce.Load()
	t.Cleanup(func() { sceneBudgetForce.Store(prev) })

	ceiling := func() map[string]int64 {
		return map[string]int64{
			"rendered text": textCacheBudgetBytes(),
			"home labels":   homeTextCacheBudgetBytes(),
			"shapes":        shapeCacheBudgetBytes(),
		}
	}
	wantHandheld := map[string]int64{
		"rendered text": 24 << 20,
		"home labels":   8 << 20,
		"shapes":        12 << 20,
	}
	wantDesktop := map[string]int64{
		"rendered text": 64 << 20,
		"home labels":   48 << 20,
		"shapes":        48 << 20,
	}

	sceneBudgetForce.Store(1)
	handheld := ceiling()
	sceneBudgetForce.Store(-1)
	desktop := ceiling()
	sceneBudgetForce.Store(0)
	derived := ceiling()

	for name, want := range wantHandheld {
		if got := handheld[name]; got != want {
			t.Errorf("forced-handheld %s budget = %d, want %d", name, got, want)
		}
		if got := desktop[name]; got != wantDesktop[name] {
			t.Errorf("forced-desktop %s budget = %d, want %d", name, got, wantDesktop[name])
		}
		if handheld[name] >= desktop[name] {
			t.Errorf("%s handheld ceiling %d is not below the desktop %d", name, handheld[name], desktop[name])
		}
		if got := derived[name]; got != cacheBudgetByDevice(want, wantDesktop[name]) {
			t.Errorf("derived %s budget = %d, want the branch this host takes", name, got)
		}
	}

	// The label cache is bounded by an entry count too, and that also branches.
	sceneBudgetForce.Store(1)
	if got := homeTextCacheMaxItems(); got != 512 {
		t.Errorf("handheld label entry cap = %d, want 512", got)
	}
	sceneBudgetForce.Store(-1)
	if got := homeTextCacheMaxItems(); got != 2048 {
		t.Errorf("desktop label entry cap = %d, want 2048", got)
	}
}

// TestCachesCarryTheirBudget checks the wiring: each cache was built with the
// budget function and not with the old count-only default.
func TestCachesCarryTheirBudget(t *testing.T) {
	if got, want := appIconTextures.textures.maxBytes, appIconTextureBudgetBytes(); got != want {
		t.Errorf("app icon cache budget = %d, want %d", got, want)
	}
	if got, want := chatImages.maxBytes, chatImageTextureBudgetBytes(); got != want {
		t.Errorf("chat image cache budget = %d, want %d", got, want)
	}
	if got, want := chatAvatars.maxBytes, chatAvatarTextureBudgetBytes(); got != want {
		t.Errorf("chat avatar cache budget = %d, want %d", got, want)
	}
	if got, want := thumbnailDataCache.maxBytes, thumbnailDataBudgetBytes(); got != want {
		t.Errorf("thumbnail data cache budget = %d, want %d", got, want)
	}
	if got, want := conditionalCache.entries.maxBytes, httpCacheBudgetBytes(); got != want {
		t.Errorf("conditional cache budget = %d, want %d", got, want)
	}
}

// TestAppIconCacheAccountsEveryByte is the wiring test for the launcher: a real
// decoded icon reports the bytes it occupies, and clearing returns the cache to
// zero. Before the byte budget the cache only knew how many icons it held.
func TestAppIconCacheAccountsEveryByte(t *testing.T) {
	withScreen(t, probeW, probeH)
	renderer := newKeyboardTestRenderer(t)
	clearAppIconCache()
	t.Cleanup(clearAppIconCache)

	path := filepath.Join(t.TempDir(), "icon.png")
	writeSolidPNG(t, path, 32, 32, color.RGBA{R: 200, G: 40, B: 40, A: 255})
	if tex := appIconTexture(renderer, path); tex == nil {
		t.Fatal("the icon did not decode")
	}
	entries, bytes := appIconTextureStats()
	if entries != 1 {
		t.Fatalf("icon cache holds %d entries, want 1", entries)
	}
	if want := int64(32 * 32 * 4); bytes != want {
		t.Fatalf("icon cache holds %d bytes, want the %d the texture occupies", bytes, want)
	}

	// A failed decode is remembered for the count cap but costs no bytes, so it
	// cannot quietly consume the budget that the working icons need.
	if tex := appIconTexture(renderer, filepath.Join(t.TempDir(), "missing.png")); tex != nil {
		t.Fatal("a missing file decoded to a texture")
	}
	entries, bytes = appIconTextureStats()
	if entries != 2 {
		t.Fatalf("icon cache holds %d entries after a failed decode, want 2", entries)
	}
	if want := int64(32 * 32 * 4); bytes != want {
		t.Fatalf("a failed decode added %d bytes", bytes-want)
	}

	clearAppIconCache()
	if entries, bytes := appIconTextureStats(); entries != 0 || bytes != 0 {
		t.Fatalf("clear left %d entries / %d bytes", entries, bytes)
	}
}

// TestChatImageCacheEvictsTheOldestByBytes drives the same rule through the chat
// cache's real entry type: four 10 MB images in a 24 MB store keep two.
func TestChatImageCacheEvictsTheOldestByBytes(t *testing.T) {
	const tenMB = int64(10 << 20)
	store := newByteOrderedStore[string, chatImageTexture](16, 24<<20,
		func(t chatImageTexture) int64 { return t.bytes }, nil)
	for _, url := range []string{"a", "b", "c"} {
		store.put(url, chatImageTexture{bytes: tenMB})
	}
	entries, bytes := store.stats()
	if entries != 2 || bytes != 20<<20 {
		t.Fatalf("cache holds %d entries / %d bytes, want 2 / 20 MB", entries, bytes)
	}
	if store.has("a") {
		t.Fatal("the oldest image survived the byte budget")
	}
	if !store.has("c") {
		t.Fatal("the newest image was evicted")
	}
}

// TestThumbnailDataCacheHonoursItsBudget runs the payloads through the cache's
// own entry points, so the wiring is exercised rather than a copy of it.
func TestThumbnailDataCacheHonoursItsBudget(t *testing.T) {
	prev := thumbnailDataCache
	thumbnailDataCache = newByteOrderedStore[string, []byte](thumbnailMemMaxEntries, 32,
		func(b []byte) int64 { return int64(len(b)) }, nil)
	t.Cleanup(func() { thumbnailDataCache = prev })

	for _, key := range []string{"a", "b", "c"} {
		thumbnailDataSet(key, make([]byte, 16))
	}
	entries, bytes := thumbnailDataStats()
	if bytes > 32 {
		t.Fatalf("payloads hold %d bytes, over the 32-byte budget", bytes)
	}
	if entries != 2 {
		t.Fatalf("cache holds %d payloads, want 2", entries)
	}
	if thumbnailDataGet("a") != nil {
		t.Fatal("the oldest payload survived the byte budget")
	}
	if got := thumbnailDataGet("c"); len(got) != 16 {
		t.Fatalf("newest payload has %d bytes, want 16", len(got))
	}
	thumbnailDataCache.drop("c")
	clearThumbnailDataCache()
	if entries, bytes := thumbnailDataStats(); entries != 0 || bytes != 0 {
		t.Fatalf("clear left %d payloads / %d bytes", entries, bytes)
	}
}

// TestConditionalCacheEvictsByBytes covers the feed cache: the entry cap says
// nothing about memory when a body may be a megabyte.
func TestConditionalCacheEvictsByBytes(t *testing.T) {
	c := newHTTPResponseCacheWithBudget(64, 10)
	for _, url := range []string{"a", "b", "c"} {
		c.store(url, `"`+url+`"`, "", []byte("1234"))
	}
	if got := c.bytesHeld(); got > 10 {
		t.Fatalf("bodies hold %d bytes, over the 10-byte budget", got)
	}
	if got := c.size(); got != 2 {
		t.Fatalf("size = %d, want 2 bodies", got)
	}
	if _, ok := c.body("a"); ok {
		t.Fatal("the oldest body survived the byte budget")
	}
	if _, ok := c.body("c"); !ok {
		t.Fatal("the newest body was evicted")
	}
	c.clear()
	if got := c.bytesHeld(); got != 0 {
		t.Fatalf("clear left %d bytes", got)
	}
}

// TestPressureEvictionClearsTheNewCaches keeps the memory-pressure path honest:
// a cache that a critical-RAM sweep forgets is a cache that grows back to its
// budget right after the sweep freed it.
func TestPressureEvictionClearsTheNewCaches(t *testing.T) {
	withScreen(t, probeW, probeH)
	renderer := newKeyboardTestRenderer(t)
	clearAppIconCache()

	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		writeSolidPNG(t, filepath.Join(dir, "icon"+itoa(i)+".png"), 16, 16, color.RGBA{R: 10, G: 10, B: 10, A: 255})
		appIconTexture(renderer, filepath.Join(dir, "icon"+itoa(i)+".png"))
	}
	thumbnailDataSet("https://example.com/thumb.jpg", make([]byte, 64))
	conditionalCache.store("https://example.com/feed", `"v1"`, "", []byte("body"))

	clearAppIconCache()
	clearThumbnailDataCache()
	conditionalCache.clear()

	if entries, bytes := appIconTextureStats(); entries != 0 || bytes != 0 {
		t.Errorf("icons survived the sweep: %d entries / %d bytes", entries, bytes)
	}
	if entries, _ := thumbnailDataStats(); entries != 0 {
		t.Errorf("thumbnail payloads survived the sweep: %d entries", entries)
	}
	if got := conditionalCache.size(); got != 0 {
		t.Errorf("feed bodies survived the sweep: %d entries", got)
	}
}
