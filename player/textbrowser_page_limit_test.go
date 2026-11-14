package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTextBrowserPageLimitScalesWithAvailableMemory(t *testing.T) {
	cases := []struct {
		availableMB uint64
		wantMiB     int
	}{
		{0, 8},
		{63, 8},
		{64, 8},
		{128, 16},
		{320, 40},
		{512, 64},
		{4096, 64},
		{^uint64(0), 64},
	}
	for _, tc := range cases {
		got := textBrowserPageLimitFor(tc.availableMB)
		if want := tc.wantMiB << 20; got != want {
			t.Fatalf("available %d MB: limit = %d MiB, want %d MiB", tc.availableMB, got>>20, tc.wantMiB)
		}
	}
}

func TestTextBrowserTruncationNoteReportsTheChosenLimit(t *testing.T) {
	note := textBrowserTruncationNote(24 << 20)
	if !strings.Contains(note, "24 MiB") {
		t.Fatalf("truncation note did not report the chosen limit: %q", note)
	}
	if strings.Contains(note, "8 MiB") {
		t.Fatalf("truncation note kept the old 8 MiB limit: %q", note)
	}
}

func TestTextBrowserFetchTimeoutScalesWithBudget(t *testing.T) {
	for _, tc := range []struct {
		limitMiB int
		want     time.Duration
	}{
		{8, 20 * time.Second},
		{16, 28 * time.Second},
		{64, 76 * time.Second},
	} {
		if got := textBrowserFetchTimeoutFor(tc.limitMiB << 20); got != tc.want {
			t.Errorf("%d MiB budget: timeout = %s, want %s", tc.limitMiB, got, tc.want)
		}
	}
}

func TestLargePageBeyondOldLimitFetchesAndParses(t *testing.T) {
	limit := textBrowserPageLimitBytes()
	const oldLimit = 8 << 20
	if limit <= oldLimit {
		t.Skipf("memory-derived limit is %d MiB; no room above the old 8 MiB bound", limit>>20)
	}
	marker := "BEYOND-OLD-LIMIT"
	padding := oldLimit + 1<<20
	body := "<!doctype html><html><body><p>Start</p><!--" +
		strings.Repeat("x", padding) + "--><p>" + marker + "</p></body></html>"
	if len(body) > limit {
		t.Skipf("test page is %d MiB, above the current %d MiB budget", len(body)>>20, limit>>20)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	doc, err := fetchTextBrowserDocument(ctx, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("fetch %d-byte page: %v", len(body), err)
	}
	if doc.Truncated {
		t.Fatalf("page of %d bytes was truncated at a %d MiB limit", len(doc.Body), doc.Limit>>20)
	}
	t.Logf("fetched %d bytes with a %d MiB budget; truncated=%t; old limit=%d", len(doc.Body), doc.Limit>>20, doc.Truncated, oldLimit)
	if len(doc.Body) != len(body) || len(doc.Body) <= oldLimit {
		t.Fatalf("fetched %d bytes, want all %d (> %d)", len(doc.Body), len(body), oldLimit)
	}
	if !strings.Contains(string(doc.Body), marker) {
		t.Fatalf("content after the old 8 MiB mark was lost from the fetched body")
	}
	parsed := parseWebPage(string(doc.Body), "", doc.URL.String())
	if !webDocumentHasText(parsed, marker) {
		t.Fatalf("reader output did not contain %q from beyond the old 8 MiB mark", marker)
	}
	readerText := browserReaderText("Large page", srv.URL, 0, "reader text", 0, false, doc.Truncated, doc.Limit)
	if strings.Contains(readerText, "Page truncated") {
		t.Fatalf("unexpected truncation note for a complete %d-byte page", len(doc.Body))
	}
	page := browseWebPageResult(browserNavigation{URL: srv.URL}, webJSLoadGeneration())
	if page.Fail != nil {
		t.Fatalf("reader load failed: %s", page.Text)
	}
	if !webDocumentHasText(page.Doc, marker) {
		t.Fatalf("the loaded reader document lost %q", marker)
	}
	if strings.Contains(page.Text, "Page truncated") {
		t.Fatalf("the reader showed a truncation note for a complete page: %q", page.Text)
	}
}

func webDocumentHasText(doc *webDocument, want string) bool {
	if doc == nil {
		return false
	}
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if strings.Contains(run.Text, want) {
				return true
			}
		}
	}
	return false
}
