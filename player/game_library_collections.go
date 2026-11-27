package main

// game_library_collections.go — Home's collections as rows of the Games
// library.
//
// Both surfaces show the same collections from the same places: the automatic
// ones the library and the games' metadata fill (autocoll.go — Last played,
// Most played, All games, Recently added, Never played, Multiplayer, and the
// per-genre, per-decade and per-player-count folders), and the user's own in
// collections.json (hometabs_collections.go). Settings → Automatic
// collections gates the automatic ones for both: the rows are built from
// autoCollIDs, the very list Home's tab builds, so a switch turned off there is
// off here on the next frame — the panel's cache key carries
// autoCollSwitchKey and the store's generation.
//
// A collection is a librarySystem with Collection set: the console grid draws
// it, opening it lists its games with the console page's own game list, and A
// starts one through launchLibraryGame, the path every row uses. A collection
// whose items are not games (an app, a console, a Home card) contributes only
// its games; the library has no row for the rest.
//
// Nothing here caps a collection at a tag count: an automatic folder is a view
// over the whole listing and a user collection is the store's bounded item
// list (homeTabsColItemsMax), so a collection of forty games lists forty.

import (
	"strconv"
	"strings"
)

// libraryCollectionRow is one collection as the library lists it.
type libraryCollectionRow struct {
	ID    string
	Name  string
	Games []libraryGame
}

// libraryCollectionRows turns collections into grid rows. Empty collections
// are left out: a row that opens onto nothing is not a collection.
func libraryCollectionRows(rows []libraryCollectionRow) []librarySystem {
	out := make([]librarySystem, 0, len(rows))
	for _, r := range rows {
		if len(r.Games) == 0 {
			continue
		}
		games := append([]libraryGame(nil), r.Games...)
		out = append(out, librarySystem{
			Name:       r.Name,
			Collection: r.ID,
			Games:      games,
			WithArt:    countCoveredGames(games),
		})
	}
	return out
}

// userCollectionRow resolves a user collection's items to the library's own
// games: a "game:" ref is looked up by path, anything else (an app, a console,
// another collection) has no row here. Duplicates are dropped and every game
// the collection holds is kept.
func userCollectionRow(col homeTabsCollection, byPath map[string]libraryGame) libraryCollectionRow {
	row := libraryCollectionRow{ID: col.ID, Name: col.Name}
	seen := make(map[string]bool, len(col.Items))
	for _, it := range col.Items {
		if !strings.HasPrefix(it.Ref, homeTabRefGame) {
			continue
		}
		path := strings.TrimPrefix(it.Ref, homeTabRefGame)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if g, ok := byPath[path]; ok {
			row.Games = append(row.Games, g)
		}
	}
	return row
}

// gameLibraryCollections is the rows the library shows, in Home's order.
func gameLibraryCollections(c *Config, base []librarySystem) []libraryCollectionRow {
	// Reading the listing is what Home's tab does: it starts the scan off this
	// thread and answers with what is ready, so the collections exist even when
	// the library is the first screen drawn and Home has never been opened.
	homeTabsLibrary()
	if !homeTabsLib.ready {
		// Nothing to open yet: the rows come with a later frame, and the panel's
		// key carries ready and the listing's generation so it rebuilds then.
		return nil
	}
	byPath := make(map[string]libraryGame, gameLib.lib.Total)
	for _, system := range base {
		for _, g := range system.Games {
			byPath[g.Path] = g
		}
	}
	rows := make([]libraryCollectionRow, 0, len(autoCollSwitches)+len(homeTabsColList()))
	for _, id := range autoCollIDs(c) {
		games := autoCollGames(id)
		// The library's own objects when it has them, so a row's art and its
		// hidden and Kid-mode filtering match the console pages'.
		mapped := make([]libraryGame, 0, len(games))
		for _, g := range games {
			if own, ok := byPath[g.Path]; ok {
				mapped = append(mapped, own)
			} else {
				mapped = append(mapped, g)
			}
		}
		rows = append(rows, libraryCollectionRow{ID: id, Name: autoCollName(id), Games: mapped})
	}
	for _, col := range homeTabsColList() {
		rows = append(rows, userCollectionRow(col, byPath))
	}
	return rows
}

// withGameLibraryCollections puts the collection rows after the Continue row,
// where they read as views of the library before the consoles.
func withGameLibraryCollections(systems []librarySystem, c *Config) []librarySystem {
	rows := libraryCollectionRows(gameLibraryCollections(c, systems))
	if len(rows) == 0 {
		return systems
	}
	at := 0
	if len(systems) > 0 && systems[0].Name == continueSystemName {
		at = 1
	}
	out := make([]librarySystem, 0, len(systems)+len(rows))
	out = append(out, systems[:at]...)
	out = append(out, rows...)
	out = append(out, systems[at:]...)
	return out
}

// gameLibraryCollectionsKey changes when Home's collections would, so the
// panel rebuilds its rows: a switch turned off in Settings, a collection
// edited in Home, or the metadata the automatic folders are built from.
func gameLibraryCollectionsKey(c *Config) string {
	// ready and the listing's generation matter as much as the switches: the
	// rows appear when Home's listing lands, and a rescan moves their games.
	ready := "0"
	if homeTabsLib.ready {
		ready = "1"
	}
	return autoCollSwitchKey(c) + "|" + strconv.FormatUint(homeTabsColGen(), 10) +
		"|" + ready + "|" + strconv.FormatUint(homeTabsLib.gen, 10)
}
