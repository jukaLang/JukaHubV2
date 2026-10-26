package main

// browserscroll_test.go — reading-position memory.
//
// Two things must hold: the arithmetic (save, get, clamp, stale-clear) is
// right on its own, and the wiring is right end to end — reading a page,
// leaving it, and pressing Back returns to the paragraph you were on.

import (
	"strings"
	"testing"
)

func TestBrowserScrollMemoryStoresAndBounds(t *testing.T) {
	browserScrollReset()
	t.Cleanup(browserScrollReset)

	// An unseen page reads as the top.
	if got := browserScrollMemoryGet("https://example.com/never"); got != 0 {
		t.Fatalf("unknown page row = %d, want 0", got)
	}

	browserScrollMemorySave("https://example.com/a", 42)
	if got := browserScrollMemoryGet("https://example.com/a"); got != 42 {
		t.Fatalf("saved row = %d, want 42", got)
	}
	// A blank address is never stored.
	browserScrollMemorySave("   ", 7)
	if got := browserScrollMemoryGet(""); got != 0 {
		t.Fatalf("blank address stored a row: %d", got)
	}
	// A negative row is clamped to the top.
	browserScrollMemorySave("https://example.com/neg", -5)
	if got := browserScrollMemoryGet("https://example.com/neg"); got != 0 {
		t.Fatalf("negative row stored as %d, want 0", got)
	}

	// The memory is bounded: pushing past the cap drops the oldest entries.
	for i := 0; i < browserScrollMemoryMax+10; i++ {
		browserScrollMemorySave("https://example.com/p"+itoa(i), int32(i))
	}
	if len(browserScrollMemory.rows) > browserScrollMemoryMax {
		t.Fatalf("memory grew to %d entries, want <= %d", len(browserScrollMemory.rows), browserScrollMemoryMax)
	}
	if _, kept := browserScrollMemory.rows["https://example.com/p0"]; kept {
		t.Fatal("the oldest entry was not evicted")
	}
}

func TestBrowserScrollPendingAppliesClampedAndStale(t *testing.T) {
	browserScrollReset()
	t.Cleanup(browserScrollReset)
	prevY := textBrowserScrollY
	t.Cleanup(func() { textBrowserScrollY = prevY })

	browserScrollMemorySave("https://example.com/a", 42)

	// Armed for A, applied for A → the scroll returns to 42.
	browserScrollPendingSet("https://example.com/a")
	textBrowserScrollY = 0
	browserScrollPendingApply("https://example.com/a", 100)
	if textBrowserScrollY != 42 {
		t.Fatalf("restored row = %d, want 42", textBrowserScrollY)
	}
	if browserPending.active {
		t.Fatal("the restore did not disarm after firing")
	}
	// Applying again is a no-op once consumed.
	textBrowserScrollY = 0
	browserScrollPendingApply("https://example.com/a", 100)
	if textBrowserScrollY != 0 {
		t.Fatalf("a consumed restore fired again: row %d", textBrowserScrollY)
	}

	// A page that shrank since it was left is clamped, never scrolled past
	// the end.
	browserScrollPendingSet("https://example.com/a") // row 42
	browserScrollPendingApply("https://example.com/a", 10)
	if textBrowserScrollY != 10 {
		t.Fatalf("over-long restore = %d, want clamped to 10", textBrowserScrollY)
	}

	// A restore armed for one page but applied for another is stale and is
	// dropped without moving the reader.
	browserScrollPendingSet("https://example.com/a")
	textBrowserScrollY = 5
	browserScrollPendingApply("https://example.com/other", 100)
	if textBrowserScrollY != 5 {
		t.Fatalf("a stale restore moved the scroll to %d", textBrowserScrollY)
	}
	if browserPending.active {
		t.Fatal("a stale restore stayed armed")
	}

	// Disarming (a fresh navigation) means apply does nothing.
	browserScrollPendingSet("https://example.com/a")
	browserScrollPendingClear()
	textBrowserScrollY = 3
	browserScrollPendingApply("https://example.com/a", 100)
	if textBrowserScrollY != 3 {
		t.Fatalf("a cleared restore fired: row %d", textBrowserScrollY)
	}
}

// End to end: read a page, follow a link to another, press Back — you are put
// back where you were, not at the top.
func TestBrowserBackRestoresReadingPosition(t *testing.T) {
	if testing.Short() {
		t.Skip("navigation probe skipped in -short mode")
	}
	resetBrowserStateForTest()
	config := layoutTestConfig(t)
	variable := "browser_scroll_memory"
	elem := browserCursorTestElement(variable)

	// A page with enough rows to scroll well past the top. The marker word is
	// unique to the loaded content (it never appears in the "Loading <url>"
	// stub), so waiting on it means the real page has arrived, not just the
	// loading placeholder whose URL happens to contain "pagea".
	longPage := func(title, marker string) browserPage {
		var sb strings.Builder
		sb.WriteString("<body>")
		for i := 0; i < 120; i++ {
			sb.WriteString("<p>" + marker + " line of body text to read and scroll</p>")
		}
		sb.WriteString("</body>")
		return browserPage{
			Text:     "Title: " + title + "\n\n" + marker,
			FinalURL: "https://example.com/" + title,
			Doc:      parseWebPage(sb.String(), "", "https://example.com/"+title),
		}
	}
	restoreFetch := setBrowserPageFetcherForTest(func(rawURL string) browserPage {
		if strings.Contains(rawURL, "pageb") {
			return longPage("pageb", "BETA-MARKER")
		}
		return longPage("pagea", "ALPHA-MARKER")
	})
	t.Cleanup(func() {
		waitForBrowserFetches()
		restoreFetch()
		setBrowserDocument(variable, nil)
		resetBrowserStateForTest()
	})

	// Open page A and read partway down it.
	loadTextBrowserURL(config, elem, "https://example.com/pagea")
	waitBrowserVariable(t, config, variable, func(s string) bool { return strings.Contains(s, "ALPHA-MARKER") })
	textBrowserScrollY = 42

	// Follow a link to page B: a fresh navigation starts at the top, and A's
	// position is remembered.
	loadTextBrowserURL(config, elem, "https://example.com/pageb")
	waitBrowserVariable(t, config, variable, func(s string) bool { return strings.Contains(s, "BETA-MARKER") })
	if textBrowserScrollY != 0 {
		t.Fatalf("a fresh navigation did not start at the top: scroll %d", textBrowserScrollY)
	}
	if got := browserScrollMemoryGet("https://example.com/pagea"); got != 42 {
		t.Fatalf("page A's position was not remembered: %d", got)
	}

	// Back to page A. browserBack resolves the element from the scene and
	// then calls loadTextBrowserURLAt(address, false); driving that directly
	// exercises the same path without needing a Browser scene wired up.
	loadTextBrowserURLAt(config, elem, "https://example.com/pagea", false)
	waitBrowserVariable(t, config, variable, func(s string) bool { return strings.Contains(s, "ALPHA-MARKER") })
	if doc := browserDocumentFor(variable); doc == nil {
		t.Fatal("Back did not load page A")
	}
	if !browserPending.active || browserPending.address != "https://example.com/pagea" {
		t.Fatalf("Back did not arm a restore for page A: %+v", browserPending)
	}
	if browserPending.row != 42 {
		t.Fatalf("armed restore row = %d, want 42", browserPending.row)
	}
	// The renderer applies it once the layout is known; do that here.
	layout := browserPageLayout(config, elem)
	if layout == nil {
		t.Fatal("page A has no layout to restore into")
	}
	browserScrollPendingApply(textBrowserAddress(config), browserScrollMax(elem, layout))
	if textBrowserScrollY != 42 {
		t.Fatalf("Back restored scroll to %d, want 42", textBrowserScrollY)
	}
}
