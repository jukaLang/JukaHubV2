package main

// reader_state.go — where each book was left.
//
// The reader used to forget everything when it closed: the page, the scroll
// position, the text size and any bookmarks. Books are long, and a handheld is
// put down mid-chapter. This store keeps one entry per document in
// reader-state.json, bounded like the other small data files, and writes it
// through the same atomic temp+rename path as the config.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	readerStateFileName = "reader-state.json"
	readerStateVersion  = 1
	// readerStateMaxBooks bounds the store. A card can hold more books than a
	// person will finish, and the oldest entries are the least useful.
	readerStateMaxBooks = 200
)

// readerBookState is one document's remembered position.
type readerBookState struct {
	Path      string  `json:"path"`
	Page      int     `json:"page,omitempty"`
	Pages     int     `json:"pages,omitempty"`
	Scroll    float64 `json:"scroll,omitempty"`
	TextSize  int     `json:"textSize,omitempty"`
	TextMode  bool    `json:"textMode,omitempty"`
	Fit       int     `json:"fit,omitempty"`
	Zoom      float64 `json:"zoom,omitempty"`
	Bookmarks []int   `json:"bookmarks,omitempty"`
	Updated   string  `json:"updated,omitempty"`
}

type readerStateFile struct {
	Version int               `json:"version"`
	Books   []readerBookState `json:"books"`
}

var (
	readerStatePath = defaultReaderStatePath
	readerState     struct {
		mu     sync.Mutex
		loaded bool
		books  map[string]readerBookState
	}
)

func defaultReaderStatePath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		dir = "."
	}
	return filepath.Join(dir, readerStateFileName)
}

// readerStateKey normalises a path for the in-memory index. Absolute paths are
// compared where possible; Windows paths are case-insensitive.
func readerStateKey(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

func (s *readerStateFile) normalise() {
	for i := range s.Books {
		s.Books[i].Path = strings.TrimSpace(s.Books[i].Path)
		s.Books[i].Bookmarks = normaliseReaderBookmarks(s.Books[i].Bookmarks)
	}
}

func readerStateLoadLocked() {
	if readerState.loaded {
		return
	}
	readerState.loaded = true
	readerState.books = map[string]readerBookState{}
	data, err := os.ReadFile(readerStatePath())
	if err != nil {
		return
	}
	var file readerStateFile
	if json.Unmarshal(data, &file) != nil {
		return
	}
	file.normalise()
	for _, book := range file.Books {
		if key := readerStateKey(book.Path); key != "" {
			readerState.books[key] = book
		}
	}
}

// readerBookFor returns the remembered state for a document.
func readerBookFor(path string) (readerBookState, bool) {
	key := readerStateKey(path)
	if key == "" {
		return readerBookState{}, false
	}
	readerState.mu.Lock()
	defer readerState.mu.Unlock()
	readerStateLoadLocked()
	book, ok := readerState.books[key]
	if !ok {
		return readerBookState{}, false
	}
	book.Bookmarks = append([]int(nil), book.Bookmarks...)
	return book, true
}

// readerSaveBook records one document's state and persists the bounded store.
func readerSaveBook(book readerBookState) {
	key := readerStateKey(book.Path)
	if key == "" {
		return
	}
	book.Path = filepath.Clean(strings.TrimSpace(book.Path))
	book.Bookmarks = normaliseReaderBookmarks(book.Bookmarks)
	book.Updated = time.Now().UTC().Format(time.RFC3339)

	readerState.mu.Lock()
	defer readerState.mu.Unlock()
	readerStateLoadLocked()
	readerState.books[key] = book

	books := make([]readerBookState, 0, len(readerState.books))
	for _, b := range readerState.books {
		books = append(books, b)
	}
	sort.SliceStable(books, func(i, j int) bool {
		if books[i].Updated == books[j].Updated {
			return books[i].Path < books[j].Path
		}
		return books[i].Updated > books[j].Updated
	})
	if len(books) > readerStateMaxBooks {
		for _, old := range books[readerStateMaxBooks:] {
			delete(readerState.books, readerStateKey(old.Path))
		}
		books = books[:readerStateMaxBooks]
	}
	data, err := json.MarshalIndent(readerStateFile{Version: readerStateVersion, Books: books}, "", "  ")
	if err != nil {
		Log().Warn("reader state could not be encoded", "err", err)
		return
	}
	if err := AtomicWrite(readerStatePath(), data, 0644); err != nil {
		Log().Warn("reader state could not be saved", "path", readerStatePath(), "err", err)
	}
}

// readerProgressLabel is the compact progress shown beside a book in Files.
func readerProgressLabel(path string) string {
	book, ok := readerBookFor(path)
	if !ok || (book.Page <= 1 && len(book.Bookmarks) == 0) {
		return ""
	}
	label := ""
	if book.Pages > 0 && book.Page > 0 {
		label = fmt.Sprintf("p. %d/%d", book.Page, book.Pages)
	} else if book.Page > 0 {
		label = fmt.Sprintf("p. %d", book.Page)
	}
	if n := len(book.Bookmarks); n > 0 {
		mark := "marks"
		if n == 1 {
			mark = "mark"
		}
		if label != "" {
			label += " · "
		}
		label += fmt.Sprintf("%d %s", n, mark)
	}
	return label
}

func normaliseReaderBookmarks(pages []int) []int {
	out := make([]int, 0, len(pages))
	for _, page := range pages {
		if page > 0 {
			out = append(out, page)
		}
	}
	sort.Ints(out)
	kept := out[:0]
	for _, page := range out {
		if len(kept) == 0 || kept[len(kept)-1] != page {
			kept = append(kept, page)
		}
	}
	return kept
}

// readerBookmarkToggle adds or removes a page and reports whether it is now
// marked.
func readerBookmarkToggle(pages []int, page int) ([]int, bool) {
	if page <= 0 {
		return pages, false
	}
	out := normaliseReaderBookmarks(pages)
	for i, p := range out {
		if p == page {
			return append(out[:i], out[i+1:]...), false
		}
	}
	out = append(out, page)
	sort.Ints(out)
	return out, true
}

// readerBookmarkNext returns the nearest bookmark after (dir > 0) or before
// (dir < 0) page.
func readerBookmarkNext(pages []int, page, dir int) (int, bool) {
	pages = normaliseReaderBookmarks(pages)
	if dir < 0 {
		for i := len(pages) - 1; i >= 0; i-- {
			if pages[i] < page {
				return pages[i], true
			}
		}
		return 0, false
	}
	for _, p := range pages {
		if p > page {
			return p, true
		}
	}
	return 0, false
}
