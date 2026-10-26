package main

// browserscroll.go — reading-position memory for the reader.
//
// A long issue or article is read across several pages: you scroll to a
// paragraph, follow a link, and press Back — and the reader used to drop you at
// the top, so the paragraph you were reading is a full D-pad scroll away again.
// This remembers where you were on each page and returns you there on Back,
// Forward and Reload, the way a browser does.
//
// The memory is session-only, keyed by the page's address (the same normalized
// URL the history and page cache use), and bounded so a long browsing session
// cannot grow it without limit. Fresh navigation — a typed address, a link, the
// Home button — still starts at the top, because that is a new place, not a
// return.
//
// The scroll row cannot be restored at load time: the page is fetched on a
// goroutine and its layout (with it the real scroll maximum) does not exist
// until it arrives, and the renderer clamps the scroll to 0 while the page is
// still on the wire. So a load arms a *pending* restore, and the renderer
// applies it — on the render thread, clamped to the page's length — on the
// first frame the styled page is actually drawn. Touching the scroll from the
// fetch goroutine would be a data race; this way there is none.

import "strings"

// browserScrollMemoryMax bounds the remembered pages. Old entries are dropped
// in insertion order; a reader who has moved on does not need the position on a
// page from fifty pages ago, and the handheld has a megabyte to spare, not a
// gigabyte.
const browserScrollMemoryMax = 40

// browserScrollMemory holds the last reading row per address, with a FIFO of
// keys for bounded eviction. Only the render/event thread reads or writes it.
var browserScrollMemory = struct {
	rows map[string]int32
	order []string
}{rows: map[string]int32{}}

// browserPendingScroll is an armed restore: the address it belongs to and the
// row to return to once that page is laid out. It is consumed by the renderer.
type browserPendingScroll struct {
	address string
	row     int32
	active  bool
}

var browserPending browserPendingScroll

// browserScrollReset drops every remembered position and any armed restore. It
// runs on a test reset so one test's reading position cannot leak into another's
// page.
func browserScrollReset() {
	browserScrollMemory.rows = map[string]int32{}
	browserScrollMemory.order = nil
	browserPending = browserPendingScroll{}
}

// browserScrollMemorySave records the reading row for an address. Storing 0 is
// meaningful — it says "this page was left at the top" — so a page that was read
// from the start returns there rather than to a stale row.
func browserScrollMemorySave(address string, row int32) {
	address = strings.TrimSpace(address)
	if address == "" {
		return
	}
	if row < 0 {
		row = 0
	}
	if _, exists := browserScrollMemory.rows[address]; !exists {
		browserScrollMemory.order = append(browserScrollMemory.order, address)
	}
	browserScrollMemory.rows[address] = row
	for len(browserScrollMemory.order) > browserScrollMemoryMax {
		oldest := browserScrollMemory.order[0]
		browserScrollMemory.order = browserScrollMemory.order[1:]
		delete(browserScrollMemory.rows, oldest)
	}
}

// browserScrollMemoryGet is the remembered row for an address, or 0 when the
// page has never been left before (a first visit starts at the top).
func browserScrollMemoryGet(address string) int32 {
	if row, ok := browserScrollMemory.rows[strings.TrimSpace(address)]; ok {
		return row
	}
	return 0
}

// browserScrollPendingSet arms a restore for an address to its remembered row.
// It runs on Back, Forward and Reload — a return to a page — never on a fresh
// navigation.
func browserScrollPendingSet(address string) {
	address = strings.TrimSpace(address)
	browserPending = browserPendingScroll{address: address, row: browserScrollMemoryGet(address), active: address != ""}
}

// browserScrollPendingClear disarms any restore, so a fresh navigation cannot
// inherit a return that was armed for a page the reader never went back to.
func browserScrollPendingClear() { browserPending = browserPendingScroll{} }

// browserScrollPendingApply consumes an armed restore on the render thread: it
// sets the scroll to the remembered row, clamped to the page's current length,
// and disarms. It only fires for the address the restore was armed for and only
// once the page is laid out (the caller passes the real scrollMax). A page that
// changed length since it was left is clamped, never scrolled out of range.
func browserScrollPendingApply(address string, scrollMax int32) {
	if !browserPending.active {
		return
	}
	if browserPending.address != strings.TrimSpace(address) {
		// The reader navigated somewhere else before this restore fired;
		// it is stale, so drop it rather than yanking an unrelated page.
		browserPending.active = false
		return
	}
	row := browserPending.row
	if row < 0 {
		row = 0
	}
	if row > scrollMax {
		row = scrollMax
	}
	textBrowserScrollY = row
	textBrowserScrollVelocity = 0
	textBrowserScrollCooldown = 0
	browserPending.active = false
}
