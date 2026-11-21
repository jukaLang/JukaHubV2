package main

// gamepin.go — a game pinned to Home,
// and the two other quick rows the game sheets gained with it.
//
// A pinned game is a tile of its own on the tile Homes — Carousel, Widgets
// and the console looks, which read home_tiles.go's list — under the ref
// "game:<ROM path>" (the Tabs Home's game ref, so a favourite, a collection
// and a pin all name a game the same way) and the kind homeTileGame. The
// tile shows the game's cover with its name, and A starts it the way the
// library does (homeTabsPlayGame: a built-in game opens its screen, any
// other goes through the emulator picker and the library's launch). On the
// Tabs Home the pinned games lead the Main tab (gamePinTabItems).
//
// Pin to Home / Unpin from Home is a row of the library's game options sheet
// (Start on a game, game_options_ui.go) and of the game page's More sheet.
// Edit Home removes a pinned tile like any other. A renamed game's pin
// follows the file and a deleted game's pin goes (gamePinRepath, called
// where game_rename.go and game_delete.go move the other per-game records).
//
// The two other rows, in both sheets:
//
//   - Remove from recents: the recent games (Home's Recent tab, the Continue
//     card, the game switcher) and the console Homes' order forget the game;
//     its saves and states stay (gswForget, the switcher's own X).
//   - Add to collection…: the Tabs Home's collection picker
//     (hometabs_collections_ui.go, homeTabsColAdd), opened over whatever
//     screen asked — it is drawn and fed input above every scene.

import (
	"path/filepath"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// homeTileGame is a pinned game's tile kind: above the kinds home_tiles.go
// numbers and the Switch Home's recent-game tile (homeTileRecent, 100).
const homeTileGame homeTileKind = 101

// gamePinRef is the tile ref of a pinned game.
func gamePinRef(path string) string { return homeTabRefGame + path }

// gamePinPathOf is the ROM path a game ref names, "" for any other ref.
func gamePinPathOf(ref string) string {
	if !strings.HasPrefix(ref, homeTabRefGame) {
		return ""
	}
	return strings.TrimSpace(ref[len(homeTabRefGame):])
}

// gamePinned reports whether the game at path is on Home's tile list.
// Render thread (homeTilesCurrent).
func gamePinned(c *Config, path string) bool {
	if c == nil || strings.TrimSpace(path) == "" {
		return false
	}
	return homeTilesIndex(homeTilesCurrent(c), gamePinRef(path)) >= 0
}

// gamePinWhere is the second half of the pin toast: where the pinned game
// shows on the Home layout in use.
func gamePinWhere(c *Config) string {
	switch arr := homeArrangement(c); {
	case arr == homeArrangementTabs:
		return "first on the Main tab"
	case homeArrangementIsDynamic(arr):
		return "a tile on Home"
	}
	return "on the Carousel and Widgets Home layouts"
}

// gamePinSet pins (on) or unpins the game at path, titled title for the
// toast. A new pin goes to the end of the tile list at one cell.
func gamePinSet(c *Config, path, title string, on bool) {
	path = strings.TrimSpace(path)
	if c == nil || path == "" {
		return
	}
	if strings.TrimSpace(title) == "" {
		title = gameDisplayTitle(path)
	}
	ref := gamePinRef(path)
	cur := homeTilesCurrent(c)
	at := homeTilesIndex(cur, ref)
	if on == (at >= 0) {
		if on {
			ShowToast(title+" is already pinned · "+gamePinWhere(c), ToastKindInfo)
		}
		return
	}
	next := make([]homeTile, 0, len(cur)+1)
	if on {
		if len(cur) >= homeTilesMax {
			ShowToast("Home is full · remove a tile in Edit Home first", ToastKindWarning)
			return
		}
		next = append(append(next, cur...), homeTile{Ref: ref, Size: homeTileDefaultSize(ref)})
	} else {
		next = append(append(next, cur[:at]...), cur[at+1:]...)
	}
	homeTilesSet(c, next)
	homeTabsTouch()
	gamePinForget(path)
	if on {
		PlaySuccessSound()
		ShowToast("Pinned "+title+" · "+gamePinWhere(c), ToastKindSuccess)
		return
	}
	ShowToast("Unpinned "+title+" from Home", ToastKindInfo)
}

// gamePinRepath moves a pin to a renamed file (newPath) or drops it (newPath
// ""): game_rename.go and game_delete.go call it with their other records.
// Render thread.
func gamePinRepath(oldPath, newPath string) {
	c := appConfig
	if c == nil || strings.TrimSpace(oldPath) == "" {
		return
	}
	cur := homeTilesCurrent(c)
	at := homeTilesIndex(cur, gamePinRef(oldPath))
	if at < 0 {
		return
	}
	next := append([]homeTile(nil), cur...)
	if strings.TrimSpace(newPath) == "" || homeTilesIndex(cur, gamePinRef(newPath)) >= 0 {
		next = append(next[:at], next[at+1:]...)
	} else {
		next[at].Ref = gamePinRef(newPath)
	}
	homeTilesSet(c, next)
	gamePinForget(oldPath)
	homeTabsTouch()
}

// ── What a pinned game is ────────────────────────────────────────────────

// gamePinInfo is a pinned game as its tile shows it: the game (ok when its
// console is known, so it can start), its name, its console's line and
// glyph, and its cover file ("" for none).
type gamePinInfo struct {
	game  libraryGame
	ok    bool
	title string
	sub   string
	glyph string
	art   string
	at    uint64
}

// gamePinCacheMS is how long a looked-up pin is believed: the library
// snapshot or the recents may name the game better a moment later.
const gamePinCacheMS = 3000

// gamePinCache holds the looked-up pins by path. Render thread only.
var gamePinCache = map[string]gamePinInfo{}

// gamePinForget drops a path's cached lookup (a pin, an unpin, a rename).
func gamePinForget(path string) { delete(gamePinCache, path) }

// gamePinGameFor finds the game at path: the library snapshot, the recent
// games, then the console its folder or extension names (filesROMSystem —
// no disk read). ok=false when no console is known for it.
func gamePinGameFor(path string) (libraryGame, bool) {
	if g, ok := homeTabsGameByPath(path); ok {
		return g, true
	}
	for _, r := range recentGames() {
		if r.Path == path {
			return libraryGame{Path: r.Path, System: r.System, Title: r.Title}, true
		}
	}
	if system := filesROMSystem(path); system != "" {
		return libraryGame{Path: path, System: system, Title: gameDisplayTitle(path)}, true
	}
	return libraryGame{Path: path, Title: gameDisplayTitle(path)}, false
}

// gamePinLookup is the pinned game at path, from the cache while it is
// fresh. Cheap to call every frame.
func gamePinLookup(path string) gamePinInfo {
	now := sdl.GetTicks64()
	if info, ok := gamePinCache[path]; ok && now-info.at < gamePinCacheMS {
		return info
	}
	g, ok := gamePinGameFor(path)
	info := gamePinInfo{game: g, ok: ok, title: strings.TrimSpace(g.Label()), at: max(now, 1)}
	if info.title == "" {
		info.title = gameDisplayTitle(path)
	}
	if info.title == "" {
		info.title = filepath.Base(path)
	}
	info.sub = consoleDisplayName(g.System)
	info.glyph = homeTabsGlyph(librarySystemGlyph(g.System), "gamepad")
	if b, built := builtInLibraryGameFor(g); built {
		info.sub = b.detail
		info.glyph = homeTabsGlyph(b.glyph, "gamepad")
	}
	switch {
	case g.HasArt:
		info.art = g.Art
	case ok:
		info.art = homeSwArtJobFor(recentGame{Path: g.Path, System: g.System, Title: g.Title}).File
	}
	if len(gamePinCache) > 4*homeTilesMax {
		clear(gamePinCache)
	}
	gamePinCache[path] = info
	return info
}

// gamePinTileLabel is a game tile's name, line and glyph for the tile
// layouts (home_dynbody.go dynRebuild); dim when its console is not known.
func gamePinTileLabel(ref string) (title, sub, icon string, dim bool) {
	info := gamePinLookup(gamePinPathOf(ref))
	return info.title, info.sub, info.glyph, !info.ok
}

// gamePinPlay starts the pinned game at path the way the library does.
func gamePinPlay(c *Config, path string) {
	if c == nil {
		c = appConfig
	}
	gamePinForget(path)
	info := gamePinLookup(path)
	switch {
	case !info.ok && !homeTabsLib.ready:
		// Neither the recents nor its folder name its console: the library
		// snapshot's scan (started here when there is none yet) will.
		homeTabsLibrary()
		ShowToast("Still reading the card — try again in a moment", ToastKindInfo)
	case !info.ok:
		ShowToast(info.title+" cannot start: its console is not known · Unpin it from Home", ToastKindWarning)
	default:
		homeTabsPlayGame(c, info.game)
	}
}

// gamePinTabItems are the pinned games as the Tabs Home's Main tab leads
// with them, in the tile list's order.
func gamePinTabItems(c *Config) []homeTabItem {
	if c == nil {
		return nil
	}
	var out []homeTabItem
	for _, t := range homeTilesCurrent(c) {
		path := gamePinPathOf(t.Ref)
		if path == "" {
			continue
		}
		info := gamePinLookup(path)
		ref := t.Ref
		it := homeTabItem{
			Key: ref, Ref: ref, Title: info.title, Sub: info.sub, Glyph: info.glyph,
			Badge: "Pinned", Dim: !info.ok,
			Activate: func(c *Config) { gamePinPlay(c, path) },
		}
		if info.art != "" {
			it.Art = homeTabsArtFn(info.art)
		}
		out = append(out, it)
	}
	return out
}

// gamePinPaint paints a pinned game's tile on the tile Homes: its cover
// cropped to the tile under a scrim with the name, or — while the cover is
// decoded, or for a game with none — an app tile with its console's glyph.
func (h *HomeLayout) gamePinPaint(renderer *sdl.Renderer, fonts HomeFonts, t homeDynTile, item HomeItem, draw sdl.Rect, focused, pressed bool) {
	if h == nil || renderer == nil || draw.W <= 0 || draw.H <= 0 {
		return
	}
	info := gamePinLookup(gamePinPathOf(t.Ref))
	var tex *sdl.Texture
	if info.art != "" {
		tex = homeTabsArtLookup(info.art)
	}
	if tex == nil {
		plain := item
		plain.Title, plain.Subtitle, plain.Icon = info.title, info.sub, info.glyph
		h.drawHomeAppTile(renderer, fonts, plain, draw, focused, pressed, !info.ok)
		return
	}
	if focused {
		drawHomeFocusGlow(renderer, draw)
	}
	drawGlassTile(renderer, item.Rect, draw, focused, pressed)
	homeTabsCopyCover(renderer, tex, draw, 255)
	from, to := continueScrim(255)
	fillVerticalScrimUntil(renderer, draw, draw.Y+draw.H*45/100, draw.Y+draw.H*78/100, from, to)
	title, sub := fonts.Tile, fonts.Meta
	if draw.W < scaleX(200, h.Width) {
		title, sub = fonts.Small, nil
	}
	if title == nil {
		return
	}
	pad := max32(scaleX(10, h.Width), draw.W/14)
	textW := draw.W - 2*pad
	if textW <= 0 {
		return
	}
	y := draw.Y + draw.H - pad - int32(title.Height())
	if sub != nil && info.sub != "" && draw.H >= scaleY(200, h.Height) {
		sh := int32(sub.Height())
		drawText(renderer, sub, ellipsize(sub, info.sub, textW), draw.X+pad, draw.Y+draw.H-pad-sh, ColorTextMuted(), textAlignLeft)
		y -= sh + scaleY(2, h.Height)
	}
	drawText(renderer, title, ellipsize(title, info.title, textW), draw.X+pad, y, ColorTextPrimary(), textAlignLeft)
}

// ── Remove from recents ──────────────────────────────────────────────────

// gameIsRecent reports whether the game at path is on the recent games.
func gameIsRecent(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	for _, r := range recentGames() {
		if r.Path == path {
			return true
		}
	}
	return false
}

// gameForgetRecent takes the game at path off every recent list: the recent
// games (Home's Recent tab, the Continue card, the game switcher, the
// library's recents) and the console Homes' order. Its saves stay. Render
// thread.
func gameForgetRecent(path, title string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if strings.TrimSpace(title) == "" {
		title = gameDisplayTitle(path)
	}
	gswForget(path)
	homeConsoleRecentsRepath(path, "")
	gameLib.recents = recentGames()
	homeTabsTouch()
	PlayBackSound()
	ShowToast(title+" removed from recent games · its saves are kept", ToastKindInfo)
}

// ── Add to collection ────────────────────────────────────────────────────

// gameAddToCollection opens the Tabs Home's collection picker for a game:
// its collections, each with a note when the game is in it already, and New
// collection…. The picker is drawn above every screen (main.go).
func gameAddToCollection(c *Config, g libraryGame) {
	if c == nil {
		c = appConfig
	}
	if strings.TrimSpace(g.Path) == "" {
		return
	}
	homeTabsItemSheetOpen(c, homeTabGames, "", homeTabsGameItem(g))
	if homeTabsItemSheetActive() {
		homeTabsSheetPage(homeTabsSheetPick)
	}
}

// ── The game sheets' rows ────────────────────────────────────────────────

// gamePinSheetName is a game options row's label (game_options_ui.go).
func gamePinSheetName(row int, g libraryGame) string {
	switch row {
	case gameOptionsRowPin:
		if gamePinned(appConfig, g.Path) {
			return "Unpin from Home"
		}
		return "Pin to Home"
	case gameOptionsRowAddCollection:
		return "Add to collection…"
	case gameOptionsRowForgetRecent:
		return "Remove from recents"
	}
	return ""
}

// gamePinSheetHint is a game options row's second line.
func gamePinSheetHint(row int, g libraryGame) string {
	switch row {
	case gameOptionsRowPin:
		if gamePinned(appConfig, g.Path) {
			return "Takes its tile off Home; the game stays in the library"
		}
		return "Its cover as a tile on Home, one press from playing"
	case gameOptionsRowAddCollection:
		return "Put it in one of your collections, or start a new one"
	case gameOptionsRowForgetRecent:
		if !gameIsRecent(g.Path) {
			return "It is not on the recent games"
		}
		return "Off Recent, Continue and the game switcher; saves stay"
	}
	return ""
}

// gamePinSheetValue is a game options row's right-hand value.
func gamePinSheetValue(row int, g libraryGame) string {
	switch row {
	case gameOptionsRowPin:
		if gamePinned(appConfig, g.Path) {
			return "Pinned"
		}
		return "→"
	case gameOptionsRowAddCollection:
		return "→"
	case gameOptionsRowForgetRecent:
		if !gameIsRecent(g.Path) {
			return "Not recent"
		}
		return "Remove"
	}
	return ""
}

// gamePinSheetActivate is A on one of the rows. The collection picker opens
// over the library once the options sheet is put away.
func gamePinSheetActivate(row int, g libraryGame) {
	switch row {
	case gameOptionsRowPin:
		gamePinSet(appConfig, g.Path, g.Label(), !gamePinned(appConfig, g.Path))
	case gameOptionsRowAddCollection:
		closeGameOptions()
		gameAddToCollection(appConfig, g)
	case gameOptionsRowForgetRecent:
		if !gameIsRecent(g.Path) {
			ShowToast(g.Label()+" is not on the recent games", ToastKindInfo)
			return
		}
		gameForgetRecent(g.Path, g.Label())
	}
}

// gamePinTabsSheetRows are the Tabs Home item sheet's rows for a game
// (hometabs_collections_ui.go): Pin to / Unpin from Home, and Remove from
// recents while it is a recent game. None for any other item.
func gamePinTabsSheetRows(c *Config, ref, title string) []homeTabsSheetRow {
	path := gamePinPathOf(ref)
	if path == "" {
		return nil
	}
	if c == nil {
		c = appConfig
	}
	pinned := gamePinned(c, path)
	label := "Pin to Home"
	if pinned {
		label = "Unpin from Home"
	}
	rows := []homeTabsSheetRow{{label: label, run: func(c *Config) {
		homeTabsSheetClose()
		gamePinSet(c, path, title, !pinned)
	}}}
	if gameIsRecent(path) {
		rows = append(rows, homeTabsSheetRow{label: "Remove from recents", run: func(*Config) {
			homeTabsSheetClose()
			gameForgetRecent(path, title)
		}})
	}
	return rows
}

// The game page's More sheet rows (gamepage_more.go).
func init() {
	gamePageRegisterExtra(gamePageExtra{
		ID: "pin", Order: 10, Icon: "home",
		Row: func(g libraryGame) (string, string, bool) {
			if strings.TrimSpace(g.Path) == "" {
				return "", "", false
			}
			return gamePinSheetName(gameOptionsRowPin, g), gamePinSheetHint(gameOptionsRowPin, g), true
		},
		Open: func(g libraryGame) { gamePinSheetActivate(gameOptionsRowPin, g) },
	})
	gamePageRegisterExtra(gamePageExtra{
		ID: "add-collection", Order: 11, Icon: "apps",
		Row: func(g libraryGame) (string, string, bool) {
			if strings.TrimSpace(g.Path) == "" {
				return "", "", false
			}
			return gamePinSheetName(gameOptionsRowAddCollection, g), gamePinSheetHint(gameOptionsRowAddCollection, g), true
		},
		Open: func(g libraryGame) { gameAddToCollection(appConfig, g) },
	})
	gamePageRegisterExtra(gamePageExtra{
		ID: "forget-recent", Order: 12, Icon: "history",
		Row: func(g libraryGame) (string, string, bool) {
			if !gameIsRecent(g.Path) {
				return "", "", false
			}
			return gamePinSheetName(gameOptionsRowForgetRecent, g), gamePinSheetHint(gameOptionsRowForgetRecent, g), true
		},
		Open: func(g libraryGame) { gameForgetRecent(g.Path, g.Label()) },
	})
}
