package main

// cachebudget.go — byte ceilings for the caches that only counted entries.
//
// An entry cap is a byte cap only when every entry is the same size, and none
// of the caches below qualify. The audit that prompted this file found the same
// shape five times: a map with a count limit, holding objects whose size varies
// by an order of magnitude or more, so the memory ceiling was whatever the
// largest entries happened to be multiplied by the count.
//
// The inventory of every in-memory texture and byte cache follows. "Bytes" is
// what bounds it after this pass; a cache already holding a byte budget before
// it keeps the one it had.
//
//	Cache                     Where              Bound                      Worst case before
//	TextCache                 textcache.go       entries + bytes            ok
//	homeTextCache             homelayout.go      items + bytes              ok
//	shapeTextures             shapeCache.go      entries + bytes            ok
//	thumbnailTextures         thumbstore.go      bytes                      ok
//	library art               game_library_ui.go bytes                      ok
//	appIconTextures           appicons.go        entries + bytes  (new)     ~16 MB at 256px icons
//	chatImages                chat.go            entries + bytes  (new)     48 MB decoded RGBA each
//	chatAvatars               chat.go            entries + bytes  (new)     768 KB, fixed 64px
//	thumbnailDataCache        thumbstore.go      entries + bytes  (new)     512 × up to 16 MB
//	conditionalCache bodies   httpcache.go       entries + bytes  (new)     64 × 1 MiB = 64 MB
//	iconAtlas                 iconatlas.go       one fixed strip            ok (1024×64 = 256 KB)
//	ambientGlowCache          backgroundfx.go    one fixed texture          ok (256 KB)
//	video overlay/UI layer    vkeyboard.go       one layer per canvas       ok (freed with renderer)
//	textBrowserWrapCache      textbrowser.go     exactly one page           ok (no growth)
//	lyricCache                lyrics.go          exactly one file           ok (no growth)
//
// The remaining count-bounded maps are string caches, and their entry caps are
// byte caps by construction: every key or value is a short string with a hard
// length limit (textMeasureEntries, sanitizeEntries and textFitCache cap at
// maxTextFitLen = 240 bytes, appLabelCache holds at most appLabelCacheMax lines
// of a tile label, appIconPathCache holds file paths). Their worst case is a few
// hundred kilobytes together, which is why they are listed here rather than
// given machinery.
//
// byteOrderedStore is the accounting shared by the five fixes. It is an
// insertion-ordered map with a byte ceiling: the oldest entry leaves first, and
// the newest is always kept so a caller holding the value it just inserted never
// gets a dangling pointer back. It owns no lock — each cache keeps its own,
// which is the contract the call sites already had (some are render-thread only,
// some are shared with download goroutines).

import "sync/atomic"

// cacheBudgetByDevice picks the budget for the device this process runs on. The
// handheld's 1 GB is shared with the emulator it launches, so its ceilings are
// the small ones.
func cacheBudgetByDevice(handheldBytes, desktopBytes int64) int64 {
	if IsTrimuiDevice() || P().IsHandheld() {
		return handheldBytes
	}
	return desktopBytes
}

// sceneBudgetForce pins the handheld/desktop branch of the scene-owned texture
// budgets — rendered text, home labels and shapes — the way
// libraryArtBudgetForce pins the box-art one: 0 derives the branch from the
// device, 1 forces the handheld ceilings, -1 the desktop ones. The scene
// navigation harness sets 1, so a desktop host measures the limits the card
// ships with instead of the ones it happens to have.
//
// These three are the caches a scene pass fully decides, which is why they get
// their own seam: the harness tabulates each of them as a column, and a column
// measured against a desktop ceiling says nothing about the 1 GB device.
var sceneBudgetForce atomic.Int32

// sceneBudgetByDevice is cacheBudgetByDevice with the harness seam in front of
// it. Everything the harness pins goes through here, so one Store covers text,
// labels and shapes and a test cannot pin two of the three by accident.
func sceneBudgetByDevice(handheldBytes, desktopBytes int64) int64 {
	switch sceneBudgetForce.Load() {
	case 1:
		return handheldBytes
	case -1:
		return desktopBytes
	}
	return cacheBudgetByDevice(handheldBytes, desktopBytes)
}

// The five budgets added by the cache-budget pass, next to each other so the
// audit reads in one place. Each is a ceiling on bytes held, never on entries
// alone; the per-cache count caps stay as a second, cheaper bound. The numbers
// are named because the tests check that a budget really does bind before the
// count cap its cache used to rely on.
const (
	// appIconTextureHandheldBudget bounds the launcher's icon textures. An icon
	// is up to 256×256 RGBA (256 KB), and the 64-entry cap allowed 16 MB of
	// them, which is more than a handheld should spend on a launcher grid.
	appIconTextureHandheldBudget = 8 << 20
	appIconTextureDesktopBudget  = 32 << 20

	// chatImageTextureHandheldBudget bounds decoded chat images. One 4000×3000
	// photo is 48 MB as RGBA, and the 16-entry cap allowed sixteen of those.
	chatImageTextureHandheldBudget = 24 << 20
	chatImageTextureDesktopBudget  = 64 << 20

	// chatAvatarTextureBudget bounds avatars. They are a fixed 64×64 (16 KB), so
	// the 48-entry cap is already 768 KB; the budget is the ceiling that holds
	// if that size ever changes.
	chatAvatarTextureBudget = 4 << 20

	// thumbnailDataHandheldBudget bounds the downloaded thumbnail bytes handed
	// to the renderer. A thumbnail payload is tens of kilobytes, but the
	// prefetcher also takes whatever the URL serves, so 512 entries had no real
	// ceiling.
	thumbnailDataHandheldBudget = 16 << 20
	thumbnailDataDesktopBudget  = 48 << 20

	// httpCacheHandheldBudget bounds the bodies kept for conditional requests.
	// Feeds and release manifests are a few hundred kilobytes each; the
	// 64-entry cap multiplied by the 1 MiB body limit was 64 MB.
	httpCacheHandheldBudget = 8 << 20
	httpCacheDesktopBudget  = 16 << 20
)

func appIconTextureBudgetBytes() int64 {
	return cacheBudgetByDevice(appIconTextureHandheldBudget, appIconTextureDesktopBudget)
}

func chatImageTextureBudgetBytes() int64 {
	return cacheBudgetByDevice(chatImageTextureHandheldBudget, chatImageTextureDesktopBudget)
}

func chatAvatarTextureBudgetBytes() int64 {
	return cacheBudgetByDevice(chatAvatarTextureBudget, chatAvatarTextureBudget)
}

func thumbnailDataBudgetBytes() int64 {
	return cacheBudgetByDevice(thumbnailDataHandheldBudget, thumbnailDataDesktopBudget)
}

func httpCacheBudgetBytes() int64 {
	return cacheBudgetByDevice(httpCacheHandheldBudget, httpCacheDesktopBudget)
}

// byteOrderedStore is an insertion-ordered map bounded by entries and by bytes.
// Zero (or negative) maxItems/maxBytes means "no bound from that side", which
// the unit tests use to isolate one rule at a time.
type byteOrderedStore[K comparable, V any] struct {
	entries  map[K]V
	order    []K
	bytes    int64
	maxItems int
	maxBytes int64
	weigh    func(V) int64
	release  func(V)
}

func newByteOrderedStore[K comparable, V any](maxItems int, maxBytes int64, weigh func(V) int64, release func(V)) *byteOrderedStore[K, V] {
	if maxItems < 1 {
		maxItems = 1
	}
	return &byteOrderedStore[K, V]{
		entries:  make(map[K]V),
		maxItems: maxItems,
		maxBytes: maxBytes,
		weigh:    weigh,
		release:  release,
	}
}

// weight is the store's idea of an entry's footprint. A nil or negative answer
// becomes zero so a buggy weigher cannot inflate the accounting and evict a
// cache down to nothing.
func (s *byteOrderedStore[K, V]) weight(v V) int64 {
	if s.weigh == nil {
		return 0
	}
	if n := s.weigh(v); n > 0 {
		return n
	}
	return 0
}

// get returns an entry without changing its place in the order: these caches
// are insertion-ordered, not LRU, so a hot key still ages out eventually
// instead of pinning the cache.
func (s *byteOrderedStore[K, V]) get(k K) (V, bool) {
	v, ok := s.entries[k]
	return v, ok
}

func (s *byteOrderedStore[K, V]) has(k K) bool {
	_, ok := s.entries[k]
	return ok
}

// put stores an entry and evicts from the front of the order until both bounds
// hold. Re-inserting a key refreshes its value in place, so the order slice
// holds exactly one copy of every key.
func (s *byteOrderedStore[K, V]) put(k K, v V) {
	if old, ok := s.entries[k]; ok {
		// A replaced value is released, never compared: the value types here
		// include slices, which are not comparable, and every caller that stores
		// a texture into an existing key is replacing it on purpose.
		s.bytes -= s.weight(old)
		if s.release != nil {
			s.release(old)
		}
	} else {
		s.order = append(s.order, k)
	}
	s.entries[k] = v
	s.bytes += s.weight(v)
	s.evict()
}

// drop removes one key. The order slice is scanned, which is what the previous
// per-cache implementations did too: these maps hold tens of entries.
func (s *byteOrderedStore[K, V]) drop(k K) bool {
	old, ok := s.entries[k]
	if !ok {
		return false
	}
	s.bytes -= s.weight(old)
	if s.release != nil {
		s.release(old)
	}
	delete(s.entries, k)
	for i, key := range s.order {
		if key == k {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return true
}

func (s *byteOrderedStore[K, V]) clear() {
	if s.release != nil {
		for _, v := range s.entries {
			s.release(v)
		}
	}
	s.entries = make(map[K]V)
	s.order = s.order[:0]
	s.bytes = 0
}

// evict pops the oldest entries until the two bounds hold. The newest entry is
// never dropped: the caller that just stored it is drawing with it.
func (s *byteOrderedStore[K, V]) evict() {
	for {
		overCount := s.maxItems > 0 && len(s.entries) > s.maxItems
		overBytes := s.maxBytes > 0 && s.bytes > s.maxBytes
		if !overCount && !overBytes {
			return
		}
		if len(s.entries) <= 1 {
			return
		}
		oldest := s.order[0]
		s.order = s.order[1:]
		if old, ok := s.entries[oldest]; ok {
			s.bytes -= s.weight(old)
			if s.release != nil {
				s.release(old)
			}
			delete(s.entries, oldest)
		}
	}
}

// setBudget moves the byte ceiling and evicts down to it, keeping the newest
// entry. Each cache's device budget is read once at construction, so the
// session-memory harness — which has to measure the handheld ceilings on a
// desktop — cannot reach a live store by changing the budget function alone. It
// goes through evict(), so an entry being drawn is never dropped.
//
// Callers hold whatever lock their cache normally uses.
func (s *byteOrderedStore[K, V]) setBudget(maxBytes int64) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	s.maxBytes = maxBytes
	s.evict()
}

// stats reports the two numbers the diagnostics and the tests care about.
func (s *byteOrderedStore[K, V]) stats() (entries int, bytes int64) {
	return len(s.entries), s.bytes
}
