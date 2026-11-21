package main

// hometabs_model.go — the shared vocabulary of the Tabs Home (JukaOS v2).
//
// The Tabs Home is a Home arrangement (homeArrangementTabs) with a strip of
// tabs in the middle of the top bar — Recent, Main, Games, Entertainment,
// Favourites, Collections — that L1 / R1 walk through. Each tab is a page of
// items drawn in the view the user picked for that tab (grid, row, list,
// carousel…).
//
// Two halves meet here:
//
//   - the shell (hometabs.go and its siblings) draws the strip, the pages and
//     the views, owns focus and input, and provides Recent, Main and
//     Entertainment;
//   - the providers (hometabs_games.go, hometabs_collections*.go) fill Games,
//     Favourites and Collections and own the item sheet (favourite, add to a
//     collection, hide…).
//
// Neither half reaches into the other's state: they meet through the types
// and functions declared here, and through these provider functions (defined
// in the providers' files, called by the shell):
//
//	homeTabsGamesItems(c *Config) []homeTabItem       // the Games tab
//	homeTabsFavouriteItems(c *Config) []homeTabItem   // the Favourites tab
//	homeTabsCollectionItems(c *Config) []homeTabItem  // the Collections tab (folders)
//	homeTabsIsFavourite(ref string) bool              // a heart on a tile
//	homeTabsItemSheetOpen(c *Config, tab, parent string, it homeTabItem)
//	    // X on an item: Open / Favourite / Add to collection / Remove…;
//	    // parent is the FolderView of the sub-page the item was on ("" = top)
//	homeTabsItemSheetActive() bool                    // the sheet owns input

import (
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
)

// The tab IDs, in their default order. They are also the keys of the per-tab
// settings (homeTabsViewKey) and of homeTabsItemSheetOpen's tab argument.
const (
	homeTabRecent        = "recent"
	homeTabMain          = "main"
	homeTabGames         = "games"
	homeTabEntertainment = "entertainment"
	homeTabFavourites    = "favourites"
	homeTabCollections   = "collections"
)

// homeTabOrder is the default tab order. Tools (hometabs_tools.go) is off
// until it is switched on (homeTabHiddenByDefault).
var homeTabOrder = [...]string{
	homeTabRecent, homeTabMain, homeTabGames, homeTabEntertainment, homeTabFavourites, homeTabCollections,
	homeTabTools,
}

// The views a page can be drawn in (homeTabItem lists, any tab or folder).
const (
	homeTabViewGrid     = "grid"     // square tiles, scrolls up and down
	homeTabViewRow      = "row"      // tiles in rows, scrolls left and right
	homeTabViewList     = "list"     // one line per item, big preview beside
	homeTabViewCarousel = "carousel" // one big centred tile, neighbours peek in
	homeTabViewCompact  = "compact"  // small icons, many per row
)

// homeTabViews is the order a "change view" control steps through. Backdrop
// and Minimal are hometabs_backdrop.go's and hometabs_minimal.go's.
var homeTabViews = [...]string{homeTabViewGrid, homeTabViewRow, homeTabViewList, homeTabViewCarousel, homeTabViewCompact,
	homeTabViewBackdrop, homeTabViewMinimal}

// Item refs. An item that can be favourited or put into a collection carries
// a Ref in one of these forms; the homeTiles grammar (home_tiles.go) is reused
// for apps and Home cards so a ref means the same thing everywhere:
//
//	app:scene:<scene>   a screen          (homeTileScenePrefix)
//	app:do:<entry>      a Start menu row  (homeTileActionPrefix)
//	<card id>           a built-in Home card (continue, media, emulator…)
//	game:<path>         one game, by its ROM / launcher path
//	sys:<system>        one console of the game library ("Nintendo 64")
//	col:<id>            a collection (never inside another collection)
const (
	homeTabRefGame       = "game:"
	homeTabRefSystem     = "sys:"
	homeTabRefCollection = "col:"
)

// homeTabItem is one thing a page shows.
type homeTabItem struct {
	// Key is unique within its page and stable across rebuilds, so focus
	// survives a refresh: usually the Ref, or "x:<name>" for an item that has
	// no ref.
	Key string
	// Ref is how the item is favourited or collected; "" = neither.
	Ref string
	// Title is the name; Sub a second line ("Nintendo 64", "12 games").
	Title string
	Sub   string
	// Glyph names the vector icon drawn when there is no Art.
	Glyph string
	// Art returns a cover or icon texture, or nil while it is not loaded yet
	// (the glyph is drawn then). It is called every frame the item is shown:
	// it must be a cache lookup, never a load. nil = always the glyph.
	Art func() *sdl.Texture
	// Badge is a short corner reading ("New", "Install", "12").
	Badge string
	// Dim draws the item muted (an emulator that is not installed…).
	Dim bool
	// Activate runs on A. Ignored for a folder.
	Activate func(c *Config)
	// Folder items open a sub-page with Children (a collection, the Store).
	// B on the sub-page comes back. FolderView is the key under which the
	// sub-page's view is remembered ("col:<id>", "store"); "" = the tab's.
	Folder     bool
	Children   func(c *Config) []homeTabItem
	FolderView string
}

// homeTabsContentGen changes whenever anything a page shows may have changed
// (a collection edited, a favourite added, a library scan landing). The shell
// rebuilds the page it shows when it moves; providers call homeTabsTouch,
// which is safe from any goroutine (a scan finishing in the background).
var homeTabsContentGen atomic.Uint64

// homeTabsTouch tells the shell to rebuild its pages.
func homeTabsTouch() { homeTabsContentGen.Add(1) }
