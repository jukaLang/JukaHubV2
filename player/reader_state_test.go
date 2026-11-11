package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func resetReaderStateForTest(t *testing.T, path string) {
	t.Helper()
	prev := readerStatePath
	readerStatePath = func() string { return path }
	readerState.mu.Lock()
	readerState.loaded = false
	readerState.books = nil
	readerState.mu.Unlock()
	t.Cleanup(func() {
		readerStatePath = prev
		readerState.mu.Lock()
		readerState.loaded = false
		readerState.books = nil
		readerState.mu.Unlock()
	})
}

func TestReaderStatePersistsProgressAndBookmarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader-state.json")
	resetReaderStateForTest(t, path)
	book := filepath.Join(t.TempDir(), "The Long Dark.pdf")

	readerSaveBook(readerBookState{
		Path: book, Page: 7, Pages: 20, Scroll: 420, TextSize: 28,
		TextMode: true, Bookmarks: []int{7, 3, 7},
	})
	got, ok := readerBookFor(book)
	if !ok {
		t.Fatal("the saved book was not found")
	}
	if got.Page != 7 || got.Pages != 20 || got.TextSize != 28 || !got.TextMode || got.Scroll != 420 {
		t.Fatalf("saved book = %+v", got)
	}
	if len(got.Bookmarks) != 2 || got.Bookmarks[0] != 3 || got.Bookmarks[1] != 7 {
		t.Fatalf("bookmarks = %v, want [3 7]", got.Bookmarks)
	}
	if label := readerProgressLabel(book); !strings.Contains(label, "p. 7/20") || !strings.Contains(label, "2 marks") {
		t.Fatalf("progress label = %q", label)
	}
}

func TestReaderBookmarksToggleAndFindNeighbours(t *testing.T) {
	pages, added := readerBookmarkToggle([]int{8, 3}, 8)
	if added || len(pages) != 1 || pages[0] != 3 {
		t.Fatalf("toggle remove = %v, added=%v", pages, added)
	}
	pages, added = readerBookmarkToggle(pages, 12)
	if !added || len(pages) != 2 || pages[1] != 12 {
		t.Fatalf("toggle add = %v, added=%v", pages, added)
	}
	if page, ok := readerBookmarkNext(pages, 5, 1); !ok || page != 12 {
		t.Fatalf("next bookmark = %d, %v", page, ok)
	}
	if page, ok := readerBookmarkNext(pages, 12, -1); !ok || page != 3 {
		t.Fatalf("previous bookmark = %d, %v", page, ok)
	}
	if _, ok := readerBookmarkNext(pages, 1, -1); ok {
		t.Fatal("found a previous bookmark before the first one")
	}
}
