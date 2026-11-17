package main

// hometabs_collections.go — the Collections tab of the Tabs Home (JukaOS v2)
// and the store behind it.
//
// A collection is a list the user makes of games and apps (any item with a
// Ref: a game, a console, a screen, a Start menu row, a Home card). It is a
// folder on the Collections tab; the view it is shown in is remembered by the
// shell under its FolderView ("col:<id>"), so every collection keeps its own
// grid, row, list… choice.
//
// The store is collections.json in the data folder, beside favorites.json
// and the game recents:
//
//	{"version": 1,
//	 "collections": [{"id": "k3x9a1", "name": "Couch co-op",
//	                  "items": [{"ref": "game:/mnt/SDCARD/Roms/SNES/…", "title": "…"}],
//	                  "created": "2026-11-16T20:00:00Z"}],
//	 "favourites": [{"ref": "app:do:Steam", "title": "Steam"}]}
//
// "favourites" holds the favourites that are not games (apps, consoles,
// collections) and games the library cannot resolve yet; a library game is
// favourited through its Favorite tag (gmeta_store.go), the one the Games
// library's own filter shows. Writes are atomic (AtomicWrite: temp, fsync,
// rename, previous version kept as .bak); a damaged file comes back from the
// .bak, and the damaged copy is kept as collections.json.corrupt.
//
// After the user's collections come up to three automatic ones (badge
// "Auto"), read-only and built from the library: Most played, Recently added
// and Never played.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Store limits: a hand-edited file cannot make Home lay out thousands of
// folders, and a name fits on a tile.
const (
	homeTabsColVersion  = 1
	homeTabsColMax      = 100
	homeTabsColItemsMax = 500
	homeTabsColFavMax   = 500
	homeTabsColNameMax  = 40
	homeTabsColFileName = "collections.json"
)

// homeTabsColItem is one entry of a collection (or of the favourites list):
// the ref and the title it had when it was added, shown while the ref does
// not resolve.
type homeTabsColItem struct {
	Ref   string `json:"ref"`
	Title string `json:"title,omitempty"`
}

// homeTabsCollection is one collection.
type homeTabsCollection struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Items   []homeTabsColItem `json:"items"`
	Created time.Time         `json:"created"`
}

// homeTabsColFile is the document on disk.
type homeTabsColFile struct {
	Version     int                  `json:"version"`
	Collections []homeTabsCollection `json:"collections"`
	Favourites  []homeTabsColItem    `json:"favourites,omitempty"`
}

// homeTabsColStore is the loaded document. gen moves on every change.
var homeTabsColStore struct {
	mu     sync.Mutex
	loaded bool
	file   homeTabsColFile
	gen    uint64
	seq    uint64
}

// homeTabsColWrites orders the asynchronous saves: an older snapshot than
// the last one written is dropped.
var homeTabsColWrites struct {
	mu      sync.Mutex
	written uint64
}

// homeTabsColPath is where the store lives; a variable so a test can point
// it at a temporary folder.
var homeTabsColPath = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, homeTabsColFileName)
}

// homeTabsColCleanName tidies a collection name: one line, single spaces,
// at most homeTabsColNameMax characters.
func homeTabsColCleanName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > homeTabsColNameMax {
		name = strings.TrimSpace(string([]rune(name)[:homeTabsColNameMax]))
	}
	return name
}

// homeTabsColCleanItems drops blank refs, collections inside collections
// (when noCollections) and duplicates, and bounds the list.
func homeTabsColCleanItems(items []homeTabsColItem, noCollections bool, limit int) []homeTabsColItem {
	out := make([]homeTabsColItem, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		it.Ref = strings.TrimSpace(it.Ref)
		it.Title = strings.TrimSpace(it.Title)
		if it.Ref == "" || seen[it.Ref] {
			continue
		}
		if noCollections && strings.HasPrefix(it.Ref, homeTabRefCollection) {
			continue
		}
		seen[it.Ref] = true
		out = append(out, it)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// homeTabsColNormalize makes a loaded document safe to use.
func homeTabsColNormalize(f *homeTabsColFile) {
	f.Version = homeTabsColVersion
	cols := make([]homeTabsCollection, 0, len(f.Collections))
	ids := map[string]bool{}
	for _, col := range f.Collections {
		col.ID = strings.TrimSpace(col.ID)
		col.Name = homeTabsColCleanName(col.Name)
		if col.ID == "" || ids[col.ID] || strings.HasPrefix(col.ID, homeTabsAutoPrefix) {
			continue
		}
		if col.Name == "" {
			col.Name = "Collection"
		}
		ids[col.ID] = true
		col.Items = homeTabsColCleanItems(col.Items, true, homeTabsColItemsMax)
		cols = append(cols, col)
		if len(cols) >= homeTabsColMax {
			break
		}
	}
	f.Collections = cols
	f.Favourites = homeTabsColCleanItems(f.Favourites, false, homeTabsColFavMax)
}

// homeTabsColLoadFile reads the store. A missing file is an empty store; a
// damaged one comes back from its .bak when that parses, and the damaged
// copy is kept beside it as .corrupt so nothing the user made is lost.
func homeTabsColLoadFile(path string) homeTabsColFile {
	var f homeTabsColFile
	if strings.TrimSpace(path) == "" {
		homeTabsColNormalize(&f)
		return f
	}
	recovered, err := readJSONWithBackup(path, &f)
	if recovered || (err != nil && !os.IsNotExist(err)) {
		if data, rerr := os.ReadFile(path); rerr == nil {
			_ = writeFileSynced(path+".corrupt", data, 0o644)
		}
		if recovered {
			// The live file is the damaged one: put the good copy back, so
			// the next save's backup is not the damage. It is parsed afresh,
			// so nothing half-read from the damaged file mixes into it.
			if good, berr := os.ReadFile(path + ".bak"); berr == nil {
				var clean homeTabsColFile
				if json.Unmarshal(good, &clean) == nil {
					f = clean
					_ = writeFileSynced(path, good, 0o644)
				}
			}
			logWarn("collections", "collections were damaged; restored the last good copy")
		} else {
			logWarn("collections", "ignoring damaged collections (kept as %s.corrupt): %v", homeTabsColFileName, err)
			f = homeTabsColFile{}
		}
	}
	homeTabsColNormalize(&f)
	return f
}

// homeTabsColSaveFile writes the store atomically.
func homeTabsColSaveFile(path string, f homeTabsColFile) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	f.Version = homeTabsColVersion
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0o644)
}

// homeTabsColEnsureLocked loads the store once. The caller holds the lock.
func homeTabsColEnsureLocked() {
	if homeTabsColStore.loaded {
		return
	}
	homeTabsColStore.loaded = true
	homeTabsColStore.file = homeTabsColLoadFile(homeTabsColPath())
}

// homeTabsColCopy deep-copies the document.
func homeTabsColCopy(f homeTabsColFile) homeTabsColFile {
	out := homeTabsColFile{Version: f.Version}
	out.Collections = make([]homeTabsCollection, len(f.Collections))
	for i, col := range f.Collections {
		col.Items = append([]homeTabsColItem(nil), col.Items...)
		out.Collections[i] = col
	}
	out.Favourites = append([]homeTabsColItem(nil), f.Favourites...)
	return out
}

// homeTabsColChangedLocked saves the document after a change (on a
// goroutine, newest snapshot wins) and tells Home. The caller holds the
// lock and releases it after.
func homeTabsColChangedLocked() {
	homeTabsColStore.gen++
	homeTabsColStore.seq++
	snapshot, seq := homeTabsColCopy(homeTabsColStore.file), homeTabsColStore.seq
	path := homeTabsColPath()
	go func() {
		homeTabsColWrites.mu.Lock()
		defer homeTabsColWrites.mu.Unlock()
		if seq < homeTabsColWrites.written {
			return
		}
		if err := homeTabsColSaveFile(path, snapshot); err != nil {
			logWarn("collections", "cannot save the collections: %v", err)
			return
		}
		homeTabsColWrites.written = seq
	}()
	homeTabsTouch()
}

// homeTabsColGen is the store's change counter.
func homeTabsColGen() uint64 {
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	return homeTabsColStore.gen
}

// homeTabsColList is a copy of the user's collections.
func homeTabsColList() []homeTabsCollection {
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	return homeTabsColCopy(homeTabsColStore.file).Collections
}

// homeTabsColGet is a copy of one collection (only that one is copied: the
// tab asks once per collection).
func homeTabsColGet(id string) (homeTabsCollection, bool) {
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	i := homeTabsColIndexLocked(id)
	if i < 0 {
		return homeTabsCollection{}, false
	}
	col := homeTabsColStore.file.Collections[i]
	col.Items = append([]homeTabsColItem(nil), col.Items...)
	return col, true
}

// homeTabsColIndexLocked is the position of a collection, -1 when gone.
func homeTabsColIndexLocked(id string) int {
	for i, col := range homeTabsColStore.file.Collections {
		if col.ID == id {
			return i
		}
	}
	return -1
}

// homeTabsColNewID is an id no collection has.
func homeTabsColNewID() string {
	n := time.Now().UnixNano()
	for {
		id := strconv.FormatInt(n, 36)
		if homeTabsColIndexLocked(id) < 0 {
			return id
		}
		n++
	}
}

// homeTabsColCreate makes a collection and returns its id, or "" and why not.
func homeTabsColCreate(name string) (string, string) {
	name = homeTabsColCleanName(name)
	if name == "" {
		return "", "A collection needs a name"
	}
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	if len(homeTabsColStore.file.Collections) >= homeTabsColMax {
		return "", "You have " + strconv.Itoa(homeTabsColMax) + " collections, the most there can be"
	}
	id := homeTabsColNewID()
	homeTabsColStore.file.Collections = append(homeTabsColStore.file.Collections, homeTabsCollection{
		ID: id, Name: name, Items: []homeTabsColItem{}, Created: time.Now().UTC().Truncate(time.Second),
	})
	homeTabsColChangedLocked()
	return id, ""
}

// homeTabsColRename renames a collection.
func homeTabsColRename(id, name string) bool {
	name = homeTabsColCleanName(name)
	if name == "" {
		return false
	}
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	i := homeTabsColIndexLocked(id)
	if i < 0 {
		return false
	}
	if homeTabsColStore.file.Collections[i].Name == name {
		return true
	}
	homeTabsColStore.file.Collections[i].Name = name
	homeTabsColChangedLocked()
	return true
}

// homeTabsColDelete deletes a collection (and its favourite, if it had one).
func homeTabsColDelete(id string) bool {
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	i := homeTabsColIndexLocked(id)
	if i < 0 {
		return false
	}
	cols := homeTabsColStore.file.Collections
	homeTabsColStore.file.Collections = append(cols[:i:i], cols[i+1:]...)
	homeTabsColStore.file.Favourites = homeTabsColWithout(homeTabsColStore.file.Favourites, homeTabRefCollection+id)
	homeTabsColChangedLocked()
	return true
}

// homeTabsColWithout is items without ref.
func homeTabsColWithout(items []homeTabsColItem, ref string) []homeTabsColItem {
	out := make([]homeTabsColItem, 0, len(items))
	for _, it := range items {
		if it.Ref != ref {
			out = append(out, it)
		}
	}
	return out
}

// homeTabsColAdd adds an item to a collection. It reports whether it was
// added, and why not: already there, the collection is full, a collection
// cannot go inside another.
func homeTabsColAdd(id, ref, title string) (bool, string) {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return false, "This cannot go into a collection"
	case strings.HasPrefix(ref, homeTabRefCollection):
		return false, "A collection cannot go inside another collection"
	}
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	i := homeTabsColIndexLocked(id)
	if i < 0 {
		return false, "That collection is gone"
	}
	col := &homeTabsColStore.file.Collections[i]
	for _, it := range col.Items {
		if it.Ref == ref {
			return false, "Already in " + col.Name
		}
	}
	if len(col.Items) >= homeTabsColItemsMax {
		return false, col.Name + " is full"
	}
	col.Items = append(col.Items, homeTabsColItem{Ref: ref, Title: strings.TrimSpace(title)})
	homeTabsColChangedLocked()
	return true, ""
}

// homeTabsColHas reports whether a collection holds ref.
func homeTabsColHas(col homeTabsCollection, ref string) bool {
	for _, it := range col.Items {
		if it.Ref == ref {
			return true
		}
	}
	return false
}

// homeTabsColRemove takes an item out of a collection.
func homeTabsColRemove(id, ref string) bool {
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	i := homeTabsColIndexLocked(id)
	if i < 0 {
		return false
	}
	col := &homeTabsColStore.file.Collections[i]
	kept := homeTabsColWithout(col.Items, ref)
	if len(kept) == len(col.Items) {
		return false
	}
	col.Items = kept
	homeTabsColChangedLocked()
	return true
}

// homeTabsColMove moves an item delta places along its collection.
func homeTabsColMove(id, ref string, delta int) bool {
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	i := homeTabsColIndexLocked(id)
	if i < 0 || delta == 0 {
		return false
	}
	items := homeTabsColStore.file.Collections[i].Items
	from := -1
	for j, it := range items {
		if it.Ref == ref {
			from = j
			break
		}
	}
	to := from + delta
	if from < 0 || to < 0 || to >= len(items) {
		return false
	}
	it := items[from]
	if to > from {
		copy(items[from:to], items[from+1:to+1])
	} else {
		copy(items[to+1:from+1], items[to:from])
	}
	items[to] = it
	homeTabsColChangedLocked()
	return true
}

// homeTabsFavList is a copy of the stored (non-tag) favourites.
func homeTabsFavList() []homeTabsColItem {
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	return append([]homeTabsColItem(nil), homeTabsColStore.file.Favourites...)
}

// homeTabsFavListSet adds or removes a stored favourite; it reports whether
// anything changed.
func homeTabsFavListSet(ref, title string, on bool) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	homeTabsColStore.mu.Lock()
	defer homeTabsColStore.mu.Unlock()
	homeTabsColEnsureLocked()
	favs := homeTabsColStore.file.Favourites
	has := false
	for _, it := range favs {
		if it.Ref == ref {
			has = true
			break
		}
	}
	switch {
	case on == has:
		return false
	case on:
		if len(favs) >= homeTabsColFavMax {
			return false
		}
		homeTabsColStore.file.Favourites = append(favs, homeTabsColItem{Ref: ref, Title: strings.TrimSpace(title)})
	default:
		homeTabsColStore.file.Favourites = homeTabsColWithout(favs, ref)
	}
	homeTabsColChangedLocked()
	return true
}

// ── Resolving refs ────────────────────────────────────────────────────────

// homeTabsMissingItem is a ref that no longer answers: dimmed, "Missing",
// still on the page so the item sheet can remove it.
func homeTabsMissingItem(ref, title string) homeTabItem {
	if strings.TrimSpace(title) == "" {
		title = ref
		for _, p := range []string{homeTabRefGame, homeTabRefSystem} {
			if strings.HasPrefix(ref, p) {
				title = strings.TrimPrefix(ref, p)
			}
		}
		if strings.HasPrefix(ref, homeTabRefGame) {
			title = gameDisplayTitle(title)
		}
		if homeTileKindOf(ref) == homeTileApp {
			title = homeAppTitleFromRef(ref)
		}
	}
	return homeTabItem{
		Key:   ref,
		Ref:   ref,
		Title: title,
		Sub:   "Missing",
		Glyph: "apps",
		Dim:   true,
		Activate: func(*Config) {
			ShowToast(title+" is no longer here · "+hintLine(FooterHint{"X", "Options"}), ToastKindInfo)
		},
	}
}

// homeTabsResolve turns a stored ref into an item that works: a game of the
// library (or of the recents while the library is still being read), a
// console, a collection, an app or a Home card. ok=false: nothing answers.
func homeTabsResolve(c *Config, ref, title string) (homeTabItem, bool) {
	switch {
	case strings.HasPrefix(ref, homeTabRefGame):
		path := strings.TrimPrefix(ref, homeTabRefGame)
		if g, ok := homeTabsGameByPath(path); ok {
			return homeTabsGameItem(g), true
		}
		for _, r := range recentGames() {
			if r.Path == path {
				return homeTabsGameItem(libraryGame{Path: r.Path, System: r.System, Title: r.Title}), true
			}
		}
		if !homeTabsLib.ready {
			// The card is still being read: shown as it was saved.
			it := homeTabsMissingItem(ref, title)
			it.Sub, it.Dim = "", false
			it.Activate = func(*Config) { ShowToast("Still reading the card — try again in a moment", ToastKindInfo) }
			return it, true
		}
		return homeTabItem{}, false
	case strings.HasPrefix(ref, homeTabRefSystem):
		name := strings.TrimPrefix(ref, homeTabRefSystem)
		for _, system := range homeTabsLibrary().Systems {
			if system.Name == name {
				return homeTabsConsoleItem(system, homeTabsPlayable(name)), true
			}
		}
		for _, r := range homeTabsLib.runnable {
			if r == name {
				return homeTabsConsoleItem(librarySystem{Name: name}, true), true
			}
		}
		return homeTabItem{}, false
	case strings.HasPrefix(ref, homeTabRefCollection):
		return homeTabsCollectionItemFor(c, strings.TrimPrefix(ref, homeTabRefCollection))
	case homeTileKindOf(ref) == homeTileApp:
		return homeTabsAppItem(c, ref, "")
	case homeTileIsCard(ref):
		// The shell's own card item (hometabs_items.go): Continue, Browser,
		// Tools and the emulator card open through their functions, which
		// homeSwOpenCard (scene targets only) would silently skip.
		return homeTabsCard(c, HomeFocusID(ref), "", "")
	}
	return homeTabItem{}, false
}

// homeTabsResolveOrMissing is homeTabsResolve with the Missing stand-in.
func homeTabsResolveOrMissing(c *Config, ref, title string) homeTabItem {
	if it, ok := homeTabsResolve(c, ref, title); ok {
		return it
	}
	return homeTabsMissingItem(ref, title)
}

// homeTabsItemTitle is the title an item is stored with.
func homeTabsItemTitle(it homeTabItem) string {
	if it.Title != "" {
		return it.Title
	}
	return it.Ref
}

// ── The automatic collections ─────────────────────────────────────────────

const (
	homeTabsAutoPrefix    = "auto-"
	homeTabsAutoPlayed    = "auto-played"
	homeTabsAutoAdded     = "auto-added"
	homeTabsAutoUnplayed  = "auto-unplayed"
	homeTabsAutoMax       = 30
	homeTabsAutoUnplayMax = 120
)

// homeTabsIsAuto reports whether a collection id is an automatic one.
func homeTabsIsAuto(id string) bool { return strings.HasPrefix(id, homeTabsAutoPrefix) }

// homeTabsAutoName is an automatic collection's name, "" for an unknown id.
func homeTabsAutoName(id string) string {
	switch id {
	case homeTabsAutoPlayed:
		return "Most played"
	case homeTabsAutoAdded:
		return "Recently added"
	case homeTabsAutoUnplayed:
		return "Never played"
	}
	return ""
}

// homeTabsAutoGames is an automatic collection's games, from the library
// snapshot: the most played, the newest on the card, the never started.
func homeTabsAutoGames(id string) []libraryGame {
	var games []libraryGame
	recent := map[string]bool{}
	if id == homeTabsAutoUnplayed {
		for _, r := range recentGames() {
			recent[r.Path] = true
		}
	}
	for _, system := range filterHiddenLibrarySystems(homeTabsLibrary().Systems, libraryShowHidden()) {
		if system.Name == continueSystemName {
			continue
		}
		for _, g := range system.Games {
			switch id {
			case homeTabsAutoPlayed:
				if g.Playtime > 0 {
					games = append(games, g)
				}
			case homeTabsAutoAdded:
				if !g.AddedAt.IsZero() && system.Name != builtInLibrarySystem {
					games = append(games, g)
				}
			case homeTabsAutoUnplayed:
				if g.Playtime == 0 && g.LastPlayed.IsZero() && !recent[g.Path] && system.Name != builtInLibrarySystem {
					games = append(games, g)
				}
			}
		}
	}
	limit := homeTabsAutoMax
	switch id {
	case homeTabsAutoPlayed:
		sort.SliceStable(games, func(i, j int) bool { return games[i].Playtime > games[j].Playtime })
	case homeTabsAutoAdded:
		sort.SliceStable(games, func(i, j int) bool { return games[i].AddedAt.After(games[j].AddedAt) })
	case homeTabsAutoUnplayed:
		limit = homeTabsAutoUnplayMax
		sort.SliceStable(games, func(i, j int) bool {
			return strings.ToLower(games[i].Label()) < strings.ToLower(games[j].Label())
		})
	}
	if len(games) > limit {
		games = games[:limit]
	}
	return games
}

// ── The tab ───────────────────────────────────────────────────────────────

// homeTabsCollectionItemFor is a collection as a folder tile: its name, how
// many items, the first item's picture; the sub-page lists the items, each
// working as it does anywhere else (a ref that no longer resolves is shown
// dimmed as Missing). ok=false for a collection that is gone (or an empty
// automatic one).
func homeTabsCollectionItemFor(c *Config, id string) (homeTabItem, bool) {
	ref := homeTabRefCollection + id
	it := homeTabItem{Key: ref, Ref: ref, Glyph: homeTabsGlyph("folder"), Folder: true, FolderView: ref}
	if homeTabsIsAuto(id) {
		name := homeTabsAutoName(id)
		games := homeTabsAutoGames(id)
		if name == "" || len(games) == 0 {
			return homeTabItem{}, false
		}
		it.Title = name
		it.Sub = pluralizeCount(len(games), "game", "games")
		it.Badge = "Auto"
		for _, g := range games {
			if g.HasArt {
				it.Art = homeTabsArtFn(g.Art)
				break
			}
		}
		it.Children = func(*Config) []homeTabItem {
			list := homeTabsAutoGames(id)
			out := make([]homeTabItem, 0, len(list))
			for _, g := range list {
				out = append(out, homeTabsGameItem(g))
			}
			return out
		}
		return it, true
	}
	col, ok := homeTabsColGet(id)
	if !ok {
		return homeTabItem{}, false
	}
	it.Title = col.Name
	it.Sub = pluralizeCount(len(col.Items), "item", "items")
	if len(col.Items) > 0 {
		if first, ok := homeTabsResolve(c, col.Items[0].Ref, col.Items[0].Title); ok {
			it.Art = first.Art
		}
	}
	it.Children = func(c *Config) []homeTabItem {
		now, ok := homeTabsColGet(id)
		if !ok {
			return nil
		}
		out := make([]homeTabItem, 0, len(now.Items))
		for _, item := range now.Items {
			out = append(out, homeTabsResolveOrMissing(c, item.Ref, item.Title))
		}
		return out
	}
	return it, true
}

// homeTabsCollectionItems is the Collections tab (hometabs_model.go): "New
// collection" first, then the user's collections in the order they were
// made, then the automatic ones that have anything in them.
func homeTabsCollectionItems(c *Config) []homeTabItem {
	out := []homeTabItem{{
		Key:      "x:new-collection",
		Title:    "New collection",
		Sub:      "A list of your games and apps",
		Glyph:    homeTabsGlyph("plus", "add", "folder"),
		Activate: func(*Config) { homeTabsNewCollection("", "") },
	}}
	for _, col := range homeTabsColList() {
		if it, ok := homeTabsCollectionItemFor(c, col.ID); ok {
			out = append(out, it)
		}
	}
	for _, id := range []string{homeTabsAutoPlayed, homeTabsAutoAdded, homeTabsAutoUnplayed} {
		if it, ok := homeTabsCollectionItemFor(c, id); ok {
			out = append(out, it)
		}
	}
	return out
}
