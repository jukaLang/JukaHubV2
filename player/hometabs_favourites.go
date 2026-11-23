package main

// hometabs_favourites.go — the Favourites tab of the Tabs Home (JukaOS v2):
// favourite games and apps, and the heart a tile shows.
//
// A game is a favourite when it carries the Favorite tag (gmeta_store.go) —
// the same tag the Games library's details page sets and its tag filter shows,
// so favouriting a game on Home or in the library is one thing. Everything
// else (apps, consoles, collections, a game the library cannot place) is kept
// in collections.json's "favourites" list (hometabs_collections.go).
//
// homeTabsIsFavourite is asked per tile per frame: it reads one map, rebuilt
// at most once a frame and only when the tags, the list or the library
// changed.

import (
	"sort"
	"strings"
)

// homeTabsFavKey is what the favourite set was built from.
type homeTabsFavKey struct {
	tagRev    uint64
	tagLoaded bool
	listGen   uint64
	libGen    uint64
}

var homeTabsFav struct {
	set   map[string]bool
	key   homeTabsFavKey
	valid bool
	frame uint64
}

// homeTabsTagState reads the game tags' change counter and whether their
// file is loaded, without loading it (that happens off the render thread,
// gmetaPreload). That load holds the lock while it reads the card, so the
// frame does not wait for it: until it is done the last answer stands.
func homeTabsTagState() (uint64, bool) {
	if !gmetaStore.mu.TryLock() {
		return homeTabsFav.key.tagRev, homeTabsFav.key.tagLoaded
	}
	defer gmetaStore.mu.Unlock()
	return gmetaStore.rev, gmetaStore.loaded
}

// homeTabsFavSync rebuilds the favourite set when what it was built from
// changed; at most once a frame.
func homeTabsFavSync() {
	f := &homeTabsFav
	if f.valid && f.frame == frameCount {
		return
	}
	f.frame = frameCount
	if !homeTabsLib.ready && !homeTabsLib.scanning {
		// A heart on any tab needs the library to know a game's tag.
		homeTabsLibrary()
	}
	rev, loaded := homeTabsTagState()
	key := homeTabsFavKey{tagRev: rev, tagLoaded: loaded, listGen: homeTabsColGen(), libGen: homeTabsLib.gen}
	if f.valid && key == f.key {
		return
	}
	set := map[string]bool{}
	for _, it := range homeTabsFavList() {
		set[it.Ref] = true
	}
	if loaded && homeTabsLib.ready {
		for path := range gmetaTaggedPaths(homeTabsLib.report.Systems, gmetaTagFavorite) {
			set[homeTabsGameRef(path)] = true
		}
	} else if !loaded {
		gmetaPreload()
	}
	f.set, f.key, f.valid = set, key, true
}

// homeTabsIsFavourite reports whether ref is a favourite (hometabs_model.go).
func homeTabsIsFavourite(ref string) bool {
	if ref == "" {
		return false
	}
	homeTabsFavSync()
	return homeTabsFav.set[ref]
}

// homeTabsTagGame is the library game a game ref names, when its tag can
// carry the favourite: a game the library knows, with a console and a title.
func homeTabsTagGame(ref string) (libraryGame, bool) {
	if !strings.HasPrefix(ref, homeTabRefGame) {
		return libraryGame{}, false
	}
	g, ok := homeTabsGameByPath(strings.TrimPrefix(ref, homeTabRefGame))
	if !ok || strings.TrimSpace(g.System) == "" || strings.TrimSpace(g.Title) == "" {
		return libraryGame{}, false
	}
	return g, true
}

// homeTabsSetFavourite favourites or unfavourites an item and says so.
func homeTabsSetFavourite(it homeTabItem, on bool) {
	if it.Ref == "" {
		return
	}
	if g, ok := homeTabsTagGame(it.Ref); ok {
		gmetaSetTag(g.System, g.Title, gmetaTagFavorite, on)
		// A copy kept in the list from before the library knew the game
		// would make the heart stay on.
		homeTabsFavListSet(it.Ref, "", false)
	} else if !homeTabsFavListSet(it.Ref, homeTabsItemTitle(it), on) && on && len(homeTabsFavList()) >= homeTabsColFavMax {
		ShowToast("Favourites are full — remove one first", ToastKindWarning)
		return
	}
	homeTabsFav.valid = false
	homeTabsTouch()
	if on {
		ShowToast("Added to Favourites", ToastKindSuccess)
		PlaySuccessSound()
		return
	}
	ShowToast("Removed from Favourites", ToastKindInfo)
}

// homeTabsSavedMediaCount is how many videos, channels and files the media
// Favorites screen holds.
func homeTabsSavedMediaCount() int {
	favoritesMutex.Lock()
	defer favoritesMutex.Unlock()
	return len(favoritesStore.Videos) + len(favoritesStore.IPTV) + len(favoritesStore.Files)
}

// homeTabsFavouriteItems is the Favourites tab (hometabs_model.go): the
// favourite games A–Z, then the other favourites in the order they were
// added, then "Saved videos" (the media Favorites screen) when it has any.
func homeTabsFavouriteItems(c *Config) []homeTabItem {
	homeTabsLibrary()
	homeTabsFavSync()
	var games []homeTabItem
	listed := map[string]bool{}
	for ref := range homeTabsFav.set {
		if g, ok := homeTabsTagGame(ref); ok {
			games = append(games, homeTabsGameItem(g))
			listed[ref] = true
		}
	}
	// By the library's name order: a leading "The" ignored (libsort_articles.go).
	sort.SliceStable(games, func(i, j int) bool {
		return librarySortKey(games[i].Title) < librarySortKey(games[j].Title)
	})
	out := games
	for _, fav := range homeTabsFavList() {
		if listed[fav.Ref] {
			continue
		}
		listed[fav.Ref] = true
		out = append(out, homeTabsResolveOrMissing(c, fav.Ref, fav.Title))
	}
	if n := homeTabsSavedMediaCount(); n > 0 {
		out = append(out, homeTabItem{
			Key:      "x:saved-videos",
			Title:    "Saved videos",
			Sub:      pluralizeCount(n, "item", "items"),
			Glyph:    homeTabsGlyph("favorites", "heart"),
			Activate: homeTabsOpenSavedMedia,
		})
	}
	if len(out) == 0 {
		out = append(out, homeTabItem{
			Key:   "x:favourites-help",
			Title: "No favourites yet",
			Sub:   hintLine(FooterHint{"X", "Add to Favourites"}),
			Glyph: homeTabsGlyph("heart", "star"),
			Dim:   true,
			Activate: func(*Config) {
				ShowToast("Highlight any game or app, then "+hintLine(FooterHint{"X", "Add to Favourites"}), ToastKindInfo)
			},
		})
	}
	return out
}

// homeTabsOpenSavedMedia opens the media Favorites screen.
func homeTabsOpenSavedMedia(c *Config) {
	idx := sceneIndexExact(c, "Favorites")
	if idx < 0 {
		ShowToast("Favorites is not part of this setup", ToastKindInfo)
		return
	}
	startMenuCloseOverlays()
	if idx != currentSceneIndex {
		changeSceneTo(c, idx)
	}
}
