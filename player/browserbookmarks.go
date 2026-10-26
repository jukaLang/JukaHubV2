package main

// browserbookmarks.go — the pages the reader was sent back to.
//
// A handheld browser without bookmarks is a one-way street: a support link in
// a GitHub issue, a release-notes page, a doc someone pasted in chat — all of
// them cost a full retyping session on a D-pad to reach a second time. Saving
// the page on screen is the whole feature, and it has to survive a relaunch
// because the app is quit every time the device sleeps.
//
// The file is data/browser-bookmarks.json, written the same way the cookie jar
// is written: through a temporary file and a rename, so power loss mid-write
// cannot leave half a document. It is not a session token, so it is written
// 0644 rather than 0600 — a bookmark is meant to be read, and a second user on
// a shared Linux install can have their own list.
//
// Unlike the cookie jar, a bookmark is the user's own data, not something a
// site handed out: it is never pruned by a policy, never aged, and never
// dropped behind the user's back. The only thing that removes one is a press.

import (
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// browserBookmarkSchemaVersion versions the file so a later build can
	// tell an old document from a new one instead of guessing at fields.
	browserBookmarkSchemaVersion = 1
	// browserBookmarkMaxRecords is a ceiling rather than a policy: a runaway
	// loop saving pages could otherwise grow the file without bound. Sorting
	// before the cut keeps truncation deterministic, so the same list is
	// rewritten the same way instead of churning on every save.
	browserBookmarkMaxRecords = 64
	// browserBookmarkTitleMax keeps one row from swallowing the panel: a page
	// title is whatever the site wrote, and some sites write a whole sentence.
	browserBookmarkTitleMax = 120
)

// browserBookmark is one saved page as it is held in memory and written out.
// Title is the page's own <title> when the styled document had one, otherwise
// the host — a bookmark whose label is a bare URL is a bookmark nobody can
// pick out of a list.
type browserBookmark struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	// Added is when the page was saved, not when it was last opened. The list
	// is ordered by it, newest first, so saving something new always lands on
	// top where the D-pad already is.
	Added string `json:"added,omitempty"`
}

// browserBookmarkFile is the on-disk document.
type browserBookmarkFile struct {
	SchemaVersion int               `json:"schemaVersion"`
	SavedAt       string            `json:"savedAt,omitempty"`
	Bookmarks     []browserBookmark `json:"bookmarks"`
}

var browserBookmarks = struct {
	mu     sync.Mutex
	loaded bool
	list   []browserBookmark
	// path is resolved on first use and cached, so a save cannot land in a
	// directory that moved underneath it (tests point this at a temp dir).
	path string
}{}

// browserBookmarkPath is an indirection so the suite never reads or writes the
// developer's real data directory — the same seam the cookie jar uses.
var browserBookmarkPath = defaultBrowserBookmarkPath

func defaultBrowserBookmarkPath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "browser-bookmarks.json")
}

// browserBookmarkPathLocked resolves and caches the file path. The caller
// holds browserBookmarks.mu.
func browserBookmarkPathLocked() string {
	if browserBookmarks.path == "" {
		browserBookmarks.path = browserBookmarkPath()
	}
	return browserBookmarks.path
}

// browserBookmarksList returns the saved pages, newest first. The list is
// copied so a caller cannot mutate the store without the lock.
func browserBookmarksList() []browserBookmark {
	browserBookmarks.mu.Lock()
	defer browserBookmarks.mu.Unlock()
	browserBookmarksLoadLocked()
	out := make([]browserBookmark, len(browserBookmarks.list))
	copy(out, browserBookmarks.list)
	return out
}

// browserBookmarksCount is how many pages are saved, for the chip's label.
func browserBookmarksCount() int {
	browserBookmarks.mu.Lock()
	defer browserBookmarks.mu.Unlock()
	browserBookmarksLoadLocked()
	return len(browserBookmarks.list)
}

// browserBookmarkHas reports whether a page is already saved, so the Save chip
// can say "Saved" instead of inviting a second, duplicate press.
func browserBookmarkHas(address string) bool {
	key := browserBookmarkKey(address)
	if key == "" {
		return false
	}
	browserBookmarks.mu.Lock()
	defer browserBookmarks.mu.Unlock()
	browserBookmarksLoadLocked()
	for _, mark := range browserBookmarks.list {
		if browserBookmarkKey(mark.URL) == key {
			return true
		}
	}
	return false
}

// browserBookmarkKey normalises a URL into the identity a bookmark is stored
// under. Scheme and host are lower-cased, a trailing slash on a bare host is
// dropped, and the fragment is removed: "#section" is the same page, and
// saving it twice would put two rows that open the same document in the list.
// An unparseable address keeps its trimmed text, so it still compares equal to
// itself rather than vanishing.
func browserBookmarkKey(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed == nil {
		return strings.ToLower(address)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	if parsed.Path == "/" {
		parsed.Path = ""
	}
	return parsed.String()
}

// browserBookmarkAdd saves a page, newest first. Saving a page that is already
// saved refreshes its title and moves it to the top rather than duplicating it:
// a user who presses Save twice means "keep this", not "keep two of this". It
// reports whether the list changed.
func browserBookmarkAdd(address, title string) bool {
	address = strings.TrimSpace(address)
	key := browserBookmarkKey(address)
	if key == "" {
		return false
	}
	title = browserBookmarkLabel(title, address)

	browserBookmarks.mu.Lock()
	defer browserBookmarks.mu.Unlock()
	browserBookmarksLoadLocked()

	for i, mark := range browserBookmarks.list {
		if browserBookmarkKey(mark.URL) != key {
			continue
		}
		// Already there: refresh the label if the page now has a better one,
		// and bring it back to the top where the pad is looking.
		changed := false
		if title != "" && mark.Title != title {
			browserBookmarks.list[i].Title = title
			changed = true
		}
		if i != 0 {
			mark = browserBookmarks.list[i]
			browserBookmarks.list = append(browserBookmarks.list[:i], browserBookmarks.list[i+1:]...)
			browserBookmarks.list = append([]browserBookmark{mark}, browserBookmarks.list...)
			changed = true
		}
		if changed {
			browserBookmarksSaveLocked()
		}
		return changed
	}

	entry := browserBookmark{Title: title, URL: address, Added: time.Now().UTC().Format(time.RFC3339)}
	browserBookmarks.list = append([]browserBookmark{entry}, browserBookmarks.list...)
	sort.SliceStable(browserBookmarks.list, func(a, b int) bool {
		// Newest first by the stamp; entries with no stamp sort last, and a
		// stable sort keeps equal stamps in insertion order.
		if browserBookmarks.list[a].Added == "" || browserBookmarks.list[b].Added == "" {
			return browserBookmarks.list[a].Added != ""
		}
		return browserBookmarks.list[a].Added > browserBookmarks.list[b].Added
	})
	if len(browserBookmarks.list) > browserBookmarkMaxRecords {
		log.Printf("[browser] %d bookmarks saved, keeping the newest %d",
			len(browserBookmarks.list), browserBookmarkMaxRecords)
		browserBookmarks.list = browserBookmarks.list[:browserBookmarkMaxRecords]
	}
	browserBookmarksSaveLocked()
	return true
}

// browserBookmarkRemove drops one saved page and reports whether it was there.
// The file is rewritten with what is left, or deleted when that was the last
// one — an empty list should not leave a second place to look.
func browserBookmarkRemove(address string) bool {
	key := browserBookmarkKey(address)
	if key == "" {
		return false
	}
	browserBookmarks.mu.Lock()
	defer browserBookmarks.mu.Unlock()
	browserBookmarksLoadLocked()
	kept := browserBookmarks.list[:0]
	removed := false
	for _, mark := range browserBookmarks.list {
		if browserBookmarkKey(mark.URL) == key {
			removed = true
			continue
		}
		kept = append(kept, mark)
	}
	if !removed {
		return false
	}
	browserBookmarks.list = append([]browserBookmark(nil), kept...)
	browserBookmarksSaveLocked()
	return true
}

// browserBookmarkLabel is the name a bookmark is listed under. A page with a
// real title keeps it; a page with none falls back to the host, because a list
// of bare URLs is unreadable on a 5-inch screen. Both are cut to the row's
// budget so one long title cannot push the next row's off the panel.
func browserBookmarkLabel(title, address string) string {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = browserLinkHost(address)
	}
	return truncate(title, browserBookmarkTitleMax)
}

// browserBookmarksLoadLocked reads the saved list on first use. The caller
// holds browserBookmarks.mu. A missing file is the normal first run; a broken
// one is logged and ignored, so a bad edit costs the bookmarks and nothing
// else — the next save overwrites it.
func browserBookmarksLoadLocked() {
	if browserBookmarks.loaded {
		return
	}
	browserBookmarks.loaded = true
	path := browserBookmarkPathLocked()
	if strings.TrimSpace(path) == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[browser] cannot read %s: %v", path, err)
		}
		return
	}
	var file browserBookmarkFile
	if err := json.Unmarshal(raw, &file); err != nil {
		log.Printf("[browser] ignoring %s: %v", path, err)
		return
	}
	if file.SchemaVersion > browserBookmarkSchemaVersion {
		log.Printf("[browser] %s is from a newer build (schema %d)", path, file.SchemaVersion)
		return
	}
	browserBookmarks.list = make([]browserBookmark, 0, len(file.Bookmarks))
	for _, mark := range file.Bookmarks {
		address := strings.TrimSpace(mark.URL)
		if address == "" {
			continue
		}
		browserBookmarks.list = append(browserBookmarks.list, browserBookmark{
			Title: browserBookmarkLabel(mark.Title, address),
			URL:   address,
			Added: mark.Added,
		})
	}
	sort.SliceStable(browserBookmarks.list, func(a, b int) bool {
		if browserBookmarks.list[a].Added == "" || browserBookmarks.list[b].Added == "" {
			return browserBookmarks.list[a].Added != ""
		}
		return browserBookmarks.list[a].Added > browserBookmarks.list[b].Added
	})
	if len(browserBookmarks.list) > browserBookmarkMaxRecords {
		browserBookmarks.list = browserBookmarks.list[:browserBookmarkMaxRecords]
	}
}

// browserBookmarksSaveMu serialises every write of the file, so a save from
// two routes cannot fight over the same temporary path.
var browserBookmarksSaveMu sync.Mutex

// browserBookmarksSaveLocked writes the list through a temporary file. The
// caller holds browserBookmarks.mu.
func browserBookmarksSaveLocked() {
	path := browserBookmarkPathLocked()
	if strings.TrimSpace(path) == "" {
		return
	}
	if len(browserBookmarks.list) == 0 {
		// Nothing to remember. Deleting is what makes "no bookmarks" true in
		// one place rather than two.
		browserBookmarksRemoveFile(path)
		return
	}
	browserBookmarksSaveMu.Lock()
	defer browserBookmarksSaveMu.Unlock()
	data, err := json.MarshalIndent(browserBookmarkFile{
		SchemaVersion: browserBookmarkSchemaVersion,
		SavedAt:       time.Now().UTC().Format(time.RFC3339),
		Bookmarks:     browserBookmarks.list,
	}, "", "  ")
	if err != nil {
		log.Printf("[browser] cannot encode bookmarks: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[browser] cannot create %s: %v", filepath.Dir(path), err)
		return
	}
	tmp := path + ".tmp"
	// 0644 on purpose: a bookmark is the user's own list, not a session token.
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		log.Printf("[browser] cannot write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[browser] cannot replace %s: %v", path, err)
	}
}

// browserBookmarksRemoveFile deletes the file, tolerating one that is already
// gone. It is only called with the store's lock held.
func browserBookmarksRemoveFile(path string) {
	browserBookmarksSaveMu.Lock()
	defer browserBookmarksSaveMu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[browser] cannot remove %s: %v", path, err)
	}
}

// browserBookmarksResetForTest throws the in-memory list away without touching
// the card, and points the next load at a fresh path. Tests use it to start
// from a clean list; the user-facing removal is browserBookmarkRemove.
func browserBookmarksResetForTest(path string) func() {
	browserBookmarks.mu.Lock()
	prevPath := browserBookmarks.path
	prevList := browserBookmarks.list
	prevLoaded := browserBookmarks.loaded
	browserBookmarks.path = path
	browserBookmarks.list = nil
	browserBookmarks.loaded = false
	browserBookmarks.mu.Unlock()
	return func() {
		browserBookmarks.mu.Lock()
		browserBookmarks.path = prevPath
		browserBookmarks.list = prevList
		browserBookmarks.loaded = prevLoaded
		browserBookmarks.mu.Unlock()
	}
}
