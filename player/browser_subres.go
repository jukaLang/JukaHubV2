package main

// browser_subres.go — a page's style sheets and pictures, sooner and once.
//
// A page used to fetch its subresources in strict turns: the markup, then the
// whole script pass (seconds on a handheld), then its style sheets, then the
// parse, and its pictures only after all of that — even when the markup the
// server sent had been drawn meanwhile (browserPreview), that early page had
// no pictures. Every page also fetched its site's style sheets again, though
// the page before had just used the same ones. Modern browsers start what the
// markup names at once (a preload scanner) and keep what recent pages used in
// a memory cache. The reader now does both, on its own scale:
//
//   - The style sheets the server's markup links start downloading as soon as
//     it is here, beside the script pass (browserPrefetchSheets). The load's
//     sheet cache (webCachedStylesheet) hands each one to whoever asks first
//     and makes the others wait for that download instead of starting another.
//   - The page drawn while its scripts run gets its pictures and web fonts
//     too (browserPreview), the ones on screen first. Its pictures go through
//     the load's picture cache (webLoadImageBytes), so the finished page takes
//     what the early one fetched — or is still fetching — instead of fetching
//     it again, and the early page stops starting new pictures the moment the
//     finished page's own pass begins.
//   - What a page fetched is remembered for the next pages for 15 minutes:
//     its style sheets (up to 8 MB) and its small pictures — logos, icons,
//     thumbnails up to 512 KB, 8 MB in all. A blocked address is never served
//     from memory, a page whose site has ad filtering off keeps sheets of its
//     own, Reload fetches everything again, and leaving the Browser lets it
//     all go (browserReleaseMemory).

import (
	"context"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// browserSubresTTL is how long a remembered sheet or picture is served.
	browserSubresTTL = 15 * time.Minute
	// The memory budgets: all sheets, one sheet, all pictures, one picture.
	browserSheetMemoryCap      = 8 << 20
	browserSheetMemoryMaxEntry = 2 << 20
	browserImageMemoryCap      = 8 << 20
	browserImageMemoryMaxEntry = 512 << 10
	// browserPrefetchSheetsMax bounds the sheets started before the parse,
	// six at a time.
	browserPrefetchSheetsMax = 24
)

// browserMemEntry is one remembered sheet or picture.
type browserMemEntry struct {
	css  string
	body []byte
	size int
	at   time.Time
	used uint64
}

// browserSubresMemory is the reader's memory of recent pages' sheets and
// pictures, by address. Any thread.
var browserSubresMemory struct {
	mu         sync.Mutex
	sheets     map[string]*browserMemEntry
	images     map[string]*browserMemEntry
	sheetBytes int
	imageBytes int
	tick       uint64
}

// webLoadCachesKey is the context key of one page load's picture cache.
type webLoadCachesKey struct{}

// webLoadCaches is what one page load shares between the page drawn while
// its scripts run and the page they build.
type webLoadCaches struct {
	// fresh is a Reload: nothing is served from the reader's memory.
	fresh bool
	mu    sync.Mutex
	// images holds the pictures this load fetched; pending the ones being
	// fetched, closed when the download ends.
	images  map[string][]byte
	pending map[string]chan struct{}
	// final is set when the finished page's own image pass begins: the early
	// page's pass starts no new picture after it.
	final atomic.Bool
}

// webWithLoadCaches gives a page load's context its sheet and picture caches.
// fresh (Reload) keeps the reader's memory out of this load.
func webWithLoadCaches(ctx context.Context, fresh bool) context.Context {
	ctx = webWithSheetCache(ctx)
	return context.WithValue(ctx, webLoadCachesKey{}, &webLoadCaches{fresh: fresh, images: make(map[string][]byte, 32)})
}

// webCarryLoadCaches returns parent carrying the load caches ctx has: the
// finished page's pictures and fonts run on the navigation's own context, past
// the load's deadline, but share what the load fetched.
func webCarryLoadCaches(parent, ctx context.Context) context.Context {
	if sheets, ok := ctx.Value(webSheetCacheKey{}).(*webSheetCache); ok {
		parent = context.WithValue(parent, webSheetCacheKey{}, sheets)
	}
	if lc := webLoadCachesFrom(ctx); lc != nil {
		parent = context.WithValue(parent, webLoadCachesKey{}, lc)
	}
	return parent
}

// webLoadCachesFrom returns the load's caches, or nil outside a page load.
func webLoadCachesFrom(ctx context.Context) *webLoadCaches {
	if ctx == nil {
		return nil
	}
	lc, _ := ctx.Value(webLoadCachesKey{}).(*webLoadCaches)
	return lc
}

// webLoadFresh reports whether this load must not use the reader's memory.
func webLoadFresh(ctx context.Context) bool {
	lc := webLoadCachesFrom(ctx)
	return lc != nil && lc.fresh
}

// browserPrefetchSheets starts the style sheets html links downloading into
// the load's sheet cache, six at a time, and returns at once: the preview and
// the page find them there, fetched or arriving.
func browserPrefetchSheets(ctx context.Context, html string, pageURL *url.URL) {
	if pageURL == nil {
		return
	}
	go func() {
		sheets := webLinkedStylesheets(html, pageURL)
		if len(sheets) > browserPrefetchSheetsMax {
			sheets = sheets[:browserPrefetchSheetsMax]
		}
		page := pageURL.String()
		slots := make(chan struct{}, 6)
		for _, sheet := range sheets {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func(address string) {
				defer func() { <-slots }()
				// The fetch parses what the network sent (imports, url()s): a bug
				// there must not take the hub down from a goroutine nothing else
				// recovers.
				defer func() {
					if r := recover(); r != nil {
						logWarn("browser", "style sheet prefetch failed: %v", r)
					}
				}()
				webCachedStylesheet(ctx, page, address)
			}(sheet.url)
		}
	}()
}

// browserSheetMemoryKey is a sheet's place in the memory: its address, and
// whether ad filtering was off for the page — the imports a sheet folds in are
// filtered for the page that asked.
func browserSheetMemoryKey(pageURL, address string) string {
	if browserAdBlockOff(pageURL) {
		return address + "\x00off"
	}
	return address
}

// browserSheetRecall returns a sheet the reader fetched for a recent page.
func browserSheetRecall(ctx context.Context, pageURL, address string) (string, bool) {
	if webLoadFresh(ctx) || browserRequestBlocked(pageURL, address) {
		return "", false
	}
	key := browserSheetMemoryKey(pageURL, address)
	m := &browserSubresMemory
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.sheets[key]
	if e == nil {
		return "", false
	}
	if time.Since(e.at) > browserSubresTTL {
		delete(m.sheets, key)
		m.sheetBytes -= e.size
		return "", false
	}
	m.tick++
	e.used = m.tick
	return e.css, true
}

// browserSheetKeep remembers a fetched sheet for the next pages.
func browserSheetKeep(pageURL, address, css string) {
	if css == "" || len(css) > browserSheetMemoryMaxEntry {
		return
	}
	key := browserSheetMemoryKey(pageURL, address)
	m := &browserSubresMemory
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sheets == nil {
		m.sheets = make(map[string]*browserMemEntry, 32)
	}
	if old := m.sheets[key]; old != nil {
		m.sheetBytes -= old.size
	}
	m.tick++
	m.sheets[key] = &browserMemEntry{css: css, size: len(css), at: time.Now(), used: m.tick}
	m.sheetBytes += len(css)
	m.sheetBytes = browserMemTrim(m.sheets, m.sheetBytes, browserSheetMemoryCap)
}

// browserImageRecall returns a picture the reader fetched for a recent page,
// when it fits in maxBytes.
func browserImageRecall(ctx context.Context, pageURL, address string, maxBytes int64) ([]byte, bool) {
	if webLoadFresh(ctx) || browserRequestBlocked(pageURL, address) {
		return nil, false
	}
	m := &browserSubresMemory
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.images[address]
	if e == nil {
		return nil, false
	}
	if time.Since(e.at) > browserSubresTTL {
		delete(m.images, address)
		m.imageBytes -= e.size
		return nil, false
	}
	if int64(len(e.body)) > maxBytes {
		return nil, false
	}
	m.tick++
	e.used = m.tick
	return e.body, true
}

// browserImageKeep remembers a small fetched picture for the next pages.
func browserImageKeep(address string, body []byte) {
	if len(body) == 0 || len(body) > browserImageMemoryMaxEntry {
		return
	}
	m := &browserSubresMemory
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.images == nil {
		m.images = make(map[string]*browserMemEntry, 64)
	}
	if old := m.images[address]; old != nil {
		m.imageBytes -= old.size
	}
	m.tick++
	m.images[address] = &browserMemEntry{body: body, size: len(body), at: time.Now(), used: m.tick}
	m.imageBytes += len(body)
	m.imageBytes = browserMemTrim(m.images, m.imageBytes, browserImageMemoryCap)
}

// browserMemTrim drops the least recently used entries until total fits in
// limit, and returns the new total. The caller holds the memory's lock.
func browserMemTrim(entries map[string]*browserMemEntry, total, limit int) int {
	for total > limit && len(entries) > 0 {
		oldest, key := uint64(0), ""
		for k, e := range entries {
			if key == "" || e.used < oldest {
				oldest, key = e.used, k
			}
		}
		total -= entries[key].size
		delete(entries, key)
	}
	return total
}

// browserSheetMemoryClear forgets every remembered sheet and picture: the
// Browser is letting its memory go (browserReleaseMemory).
func browserSheetMemoryClear() {
	m := &browserSubresMemory
	m.mu.Lock()
	m.sheets, m.images = nil, nil
	m.sheetBytes, m.imageBytes = 0, 0
	m.mu.Unlock()
}

// webLoadImageBytes is fetchWebImageBytes through the load's picture cache and
// the reader's memory: a picture the load already fetched is handed over, one
// being fetched is waited for, and a small one a recent page fetched is not
// fetched again.
func webLoadImageBytes(ctx context.Context, pageURL, address string, maxBytes int64) []byte {
	if maxBytes <= 0 {
		return nil
	}
	lc := webLoadCachesFrom(ctx)
	if lc == nil {
		return browserFetchImageRemembered(ctx, pageURL, address, maxBytes)
	}
	lc.mu.Lock()
	body, have := lc.images[address]
	wait, busy := lc.pending[address]
	if !have && !busy {
		if lc.pending == nil {
			lc.pending = make(map[string]chan struct{}, 16)
		}
		done := make(chan struct{})
		lc.pending[address] = done
		lc.mu.Unlock()
		body = browserFetchImageRemembered(ctx, pageURL, address, maxBytes)
		lc.mu.Lock()
		if len(body) > 0 {
			lc.images[address] = body
		}
		delete(lc.pending, address)
		lc.mu.Unlock()
		close(done)
		return body
	}
	lc.mu.Unlock()
	if busy {
		// Another pass is fetching it: its result, or a try of our own when
		// that one failed (it may have had less of the budget to spend).
		select {
		case <-wait:
		case <-ctx.Done():
			return nil
		}
		lc.mu.Lock()
		body, have = lc.images[address]
		lc.mu.Unlock()
		if !have {
			return fetchWebImageBytes(ctx, pageURL, address, maxBytes)
		}
	}
	if int64(len(body)) > maxBytes {
		return nil
	}
	return body
}

// browserFetchImageRemembered fetches a picture, serving a small one a recent
// page fetched from memory and remembering what it fetches.
func browserFetchImageRemembered(ctx context.Context, pageURL, address string, maxBytes int64) []byte {
	if body, ok := browserImageRecall(ctx, pageURL, address, maxBytes); ok {
		return body
	}
	body := fetchWebImageBytes(ctx, pageURL, address, maxBytes)
	browserImageKeep(address, body)
	return body
}
