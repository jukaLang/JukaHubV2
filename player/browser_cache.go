package main

// browser_cache.go — the in-memory page cache behind Back and Forward.
//
// The handheld is slow and its Wi-Fi is slower, so re-downloading the page the
// user just left made Back cost as much as the first visit. Worse, the page is
// parsed, laid out and its images decoded all over again. Pages are therefore
// kept in memory for the session, bounded by count and by bytes: a handful of
// long articles must not be able to eat the 1 GB the console shares with
// everything else.
//
// Nothing here touches the network or the renderer; the loader in
// textbrowser.go decides when a cached page is good enough and when Reload has
// to go back to the wire.

import (
	"container/list"
	"strings"
	"sync"
)

const (
	// browserCacheMaxEntries is how many pages are remembered. Eight covers a
	// typical reading session (issue list, a few issues, back) without keeping
	// a whole browsing history resident.
	browserCacheMaxEntries = 8
	// browserCacheMaxBytes bounds the text and decoded image bytes the cache
	// may hold. Images dominate; text is capped per page already.
	browserCacheMaxBytes = 12 << 20
)

// browserCacheEntry is one page plus the bytes it is accounted for.
type browserCacheEntry struct {
	address string
	page    browserPage
	weight  int
}

var browserCache = struct {
	mu    sync.Mutex
	order *list.List // front = least recently used
	items map[string]*list.Element
	bytes int
}{
	order: list.New(),
	items: map[string]*list.Element{},
}

// browserPageWeight approximates what one page costs in memory: its reader
// text, its fetched images and a per-block allowance for the parsed runs.
func browserPageWeight(page browserPage) int {
	weight := len(page.Text)
	if page.Doc != nil {
		weight += len(page.Doc.Blocks) * 128
		page.Doc.imageMu.Lock()
		for _, data := range page.Doc.Images {
			weight += len(data)
		}
		page.Doc.imageMu.Unlock()
	}
	return weight
}

// browserCachedPage returns a page remembered from an earlier visit.
func browserCachedPage(address string) (browserPage, bool) {
	browserCache.mu.Lock()
	defer browserCache.mu.Unlock()
	el, ok := browserCache.items[address]
	if !ok {
		return browserPage{}, false
	}
	browserCache.order.MoveToBack(el)
	return el.Value.(*browserCacheEntry).page, true
}

// browserCachePage remembers one successfully loaded page, evicting the least
// recently used entries until both limits hold. A failed load is never cached
// (its FinalURL is empty), so retrying after a dropped connection still tries
// the network.
func browserCachePage(address string, page browserPage) {
	address = trimBrowserCacheAddress(address)
	if address == "" || page.FinalURL == "" {
		return
	}
	entry := &browserCacheEntry{address: address, page: page, weight: browserPageWeight(page)}
	if entry.weight > browserCacheMaxBytes/2 {
		// A single page bigger than half the budget is not worth pinning the
		// cache on; it still draws, it just is not remembered.
		return
	}
	browserCache.mu.Lock()
	defer browserCache.mu.Unlock()
	if browserCache.order == nil {
		browserCache.order = list.New()
	}
	if browserCache.items == nil {
		browserCache.items = map[string]*list.Element{}
	}
	if el, ok := browserCache.items[address]; ok {
		old := el.Value.(*browserCacheEntry)
		browserCache.bytes -= old.weight
		browserCache.order.Remove(el)
		delete(browserCache.items, address)
	}
	browserCache.items[address] = browserCache.order.PushBack(entry)
	browserCache.bytes += entry.weight
	for len(browserCache.items) > browserCacheMaxEntries ||
		browserCache.bytes > browserCacheMaxBytes {
		front := browserCache.order.Front()
		if front == nil {
			break
		}
		old := front.Value.(*browserCacheEntry)
		browserCache.order.Remove(front)
		delete(browserCache.items, old.address)
		browserCache.bytes -= old.weight
	}
}

// browserForgetPage drops one address, which is what Reload wants: the visitor
// asked for the page, not for the copy that misbehaved last time.
func browserForgetPage(address string) {
	address = trimBrowserCacheAddress(address)
	browserCache.mu.Lock()
	defer browserCache.mu.Unlock()
	el, ok := browserCache.items[address]
	if !ok {
		return
	}
	old := el.Value.(*browserCacheEntry)
	browserCache.order.Remove(el)
	delete(browserCache.items, address)
	browserCache.bytes -= old.weight
}

// browserClearPageCache empties the cache (theme change, test reset, and the
// low-memory monitor's eviction path all use it).
func browserClearPageCache() {
	browserCache.mu.Lock()
	defer browserCache.mu.Unlock()
	browserCache.order = list.New()
	browserCache.items = map[string]*list.Element{}
	browserCache.bytes = 0
}

// browserCacheSize reports the entries and bytes held, for tests and the
// diagnostics screen.
func browserCacheSize() (entries, bytes int) {
	browserCache.mu.Lock()
	defer browserCache.mu.Unlock()
	return len(browserCache.items), browserCache.bytes
}

// trimBrowserCacheAddress keeps the cache key stable across the spellings the
// browser accepts for the same page.
func trimBrowserCacheAddress(address string) string {
	return strings.TrimSpace(address)
}
