package main

// game_library_ui.go — the Games library.
//
// A full-screen page in Home's language, on Home's backdrop under the top bar,
// in two levels. The first is a grid of glass buttons, one per console (with a
// leading Continue button built from the recents file): each carries the
// system's glyph, its name, a game count, a cover meter and the first cover it
// has; the Pixel names and Pixel cards looks (X on the grid) make the name in
// pixel letters the art instead (game_library_pixel_look.go). A (or a click)
// opens a console's page: its games (list, 3D shelf or
// cover column) beside a large box-art preview. On that page A opens a game,
// X opens LAN play (netplay_ui.go), E hands the selected game to its tool
// (handoff.go: profiles, compatibility or the emulator page), L2 / R2 (and
// Right in a column) jump by letter (game_library_scrub.go); B goes back to
// the consoles. L1 fetches missing covers and Y rescans on both levels; B on
// the grid closes. It is a modal overlay like the Emulators panel, so it
// works on every config, including the desktop one with no Roms folder.

import (
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

type gameLibRowHit struct {
	index int
	rect  sdl.Rect
}

type gameLibrarySystemsKey struct {
	scan       uint64
	query      string
	showHidden bool
	tag        string
	gmetaRev   uint64
}

type gameLibraryPanel struct {
	open       bool
	lib        gameLibraryReport
	recents    []recentGame
	system     int
	cursor     int
	scroll     int
	focus      int    // 0 = the consoles grid, 1 = the selected console's games
	logoSystem string // console whose bitmap title last assembled
	logoAt     uint64
	// gridCols is the consoles grid's column count as last drawn, which is what
	// Up and Down step by; sysScroll is the first grid row on screen.
	gridCols  int
	sysScroll int
	// backRect is the page header (glyph and title): a click there goes one
	// level back, the way B does.
	backRect sdl.Rect
	// query narrows the whole library to the games whose title, console or
	// file name matches. Empty is the normal console-by-console browser.
	query    string
	artCache map[string]*sdl.Texture
	// artOrder/artSize/artBytes bound that cache: a library of hundreds of
	// games loaded one cover per focused row and never released one, which is
	// tens of megabytes of textures on a card with a large collection. Order is
	// oldest first; size is each entry's RGBA bytes (0 for a cached miss).
	artOrder []string
	artSize  map[string]int64
	artBytes int64
	// artSeen is the artwork-fetch generation whose images are in artCache. A
	// newer generation means the cache holds "missing" entries that now exist.
	artSeen uint64
	// scanning is true while the card walk and build run off the render thread.
	// scanGen makes a result from a closed/reopened panel harmless.
	scanning bool
	scanGen  uint64
	// systemsCache is currentGameSystems' answer for one generation of scan,
	// query, hidden/view setting and tag revision. Without it the panel rebuilt
	// the recents list and stat'd every recent game several times per frame.
	systemsCache []librarySystem
	systemsKey   gameLibrarySystemsKey
	systemsValid bool

	systemHits []gameLibRowHit
	gameHits   []gameLibRowHit
	// artRect is the preview frame, filled by the renderer for click tests.
	artRect sdl.Rect
	// listRect is the games column of a console's page as last drawn: the
	// letter-scrub bubble sits over its middle (game_library_scrub.go).
	listRect sdl.Rect
}

type libraryArtDecodeResult struct {
	path       string
	generation uint64
	picture    *image.NRGBA
	err        error
}

// libraryArtDecodeState allows only one focused cover decode at a time. The
// decoded picture is handed back for texture upload on the render thread; a
// focus change cancels queued work and discards any finished stale result.
type libraryArtDecodeState struct {
	mu         sync.Mutex
	path       string
	generation uint64
	pending    bool
	pendingGen uint64
	cancel     context.CancelFunc
	done       *libraryArtDecodeResult
}

var gameLib gameLibraryPanel
var libraryArtDecode libraryArtDecodeState

const continueSystemName = "Continue"

// librarySystemsForUI returns the console list with the Continue row first.
// Recents are a view over the same games, not a second library, so launching
// one goes through exactly the same resolution path.
func librarySystemsForUI(lib gameLibraryReport, recents []recentGame) []librarySystem {
	systems := make([]librarySystem, 0, len(lib.Systems)+1)
	if len(recents) > 0 {
		recentGames := make([]libraryGame, 0, len(recents))
		for _, it := range recents {
			title := strings.TrimSpace(it.Title)
			if title == "" {
				title = gameDisplayTitle(it.Path)
			}
			art := romCoverPath(scannedROM{
				Path:   it.Path,
				System: it.System,
			}, libraryArtworkRoot())
			if it.System == androidLibrarySystem {
				// An Android game's cover is named per APK (every phone APK is base.apk).
				art = androidLibraryCoverPath(it.Path)
			} else if it.System == html5LibrarySystem {
				art = html5CoverPath(it.Path)
			}
			game := libraryGame{
				Path:   it.Path,
				System: it.System,
				Title:  title,
				Art:    art,
				HasArt: artworkCoverPresent(art),
			}
			libraryDecorateGame(&game)
			recentGames = append(recentGames, game)
		}
		systems = append(systems, librarySystem{
			Name:    continueSystemName,
			Games:   recentGames,
			WithArt: countCoveredGames(recentGames),
		})
	}
	systems = append(systems, lib.Systems...)
	return systems
}

// libraryArtworkRoot is a seam for tests: the artwork directory normally lives
// under the app data folder, which a unit test cannot write to.
var libraryArtworkRoot = romArtworkDir

func currentGameSystems() []librarySystem {
	key := gameLibrarySystemsKey{
		scan:       gameLib.scanGen,
		query:      gameLib.query,
		showHidden: libraryShowHidden(),
		tag:        gmetaFilterTag(),
		gmetaRev:   gmetaRev(),
	}
	if gameLib.systemsValid && gameLib.systemsKey == key {
		return gameLib.systemsCache
	}
	// The view filter is first: a hidden game stays out of search, tags and the
	// Continue row unless "Show hidden" is on. The tag filter (Select's menu or
	// T, gmeta_ui.go) narrows what the search leaves; it returns the list untouched
	// when no tag is chosen.
	systems := filterHiddenLibrarySystems(librarySystemsForUI(gameLib.lib, gameLib.recents), key.showHidden)
	systems = gmetaFilterSystems(filterLibrarySystems(systems, gameLib.query))
	systems = uiModeKidSystems(systems) // Kid mode: the games for kids only (uimode_kids.go)
	gameLib.systemsCache, gameLib.systemsKey, gameLib.systemsValid = systems, key, true
	return systems
}

func invalidateGameLibrarySystemsCache() {
	gameLib.systemsCache = nil
	gameLib.systemsValid = false
}

// normalizeGameSearch folds a query the way the matcher compares it: case does
// not matter, and a run of spaces counts as one.
func normalizeGameSearch(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}

// filterLibrarySystems narrows the console list to one search. A game matches
// on its title, its console or its file name, so "zelda", "gba" and "snes" all
// find the same cart. A console whose own name matches keeps every game in it —
// typing a console is a request for that console. The Continue row is dropped
// while searching because it is a view over the same games and would list every
// hit twice.
func filterLibrarySystems(systems []librarySystem, query string) []librarySystem {
	needle := normalizeGameSearch(query)
	if needle == "" {
		return systems
	}
	filtered := make([]librarySystem, 0, len(systems))
	for _, system := range systems {
		if system.Name == continueSystemName {
			continue
		}
		consoleMatch := strings.Contains(normalizeGameSearch(system.Name), needle) || strings.Contains(normalizeGameSearch(consoleDisplayName(system.Name)), needle)
		games := make([]libraryGame, 0, len(system.Games))
		for _, game := range system.Games {
			if consoleMatch ||
				strings.Contains(normalizeGameSearch(game.Label()), needle) ||
				strings.Contains(normalizeGameSearch(filepath.Base(game.Path)), needle) {
				games = append(games, game)
			}
		}
		if len(games) == 0 {
			continue
		}
		filtered = append(filtered, librarySystem{
			Name:    system.Name,
			Games:   games,
			WithArt: countCoveredGames(games),
		})
	}
	return filtered
}

// gameLibraryQuery is the active search text, empty when browsing.
func gameLibraryQuery() string { return gameLib.query }

// setGameLibraryQuery applies a new search and keeps the highlight on a real
// row. The filter runs live while the on-screen keyboard is open, so this is
// called once per keystroke.
func setGameLibraryQuery(query string) {
	query = strings.TrimSpace(query)
	if query == gameLib.query {
		return
	}
	gameLib.query = query
	invalidateGameLibrarySystemsCache()
	gameLib.system, gameLib.cursor, gameLib.scroll = 0, 0, 0
	gameLibraryFocusFiltered()
}

// gameLibraryFocusFiltered picks the level a new search or tag filter shows:
// the grid of the consoles that have hits, so the counts on their buttons say
// where the matches are, or — when they are all on one console — that
// console's games straight away.
func gameLibraryFocusFiltered() {
	gameLib.focus = 0
	if gameLib.query == "" && gmetaFilterTag() == "" {
		return
	}
	if systems := currentGameSystems(); len(systems) == 1 && len(systems[0].Games) > 0 {
		gameLib.focus = 1
	}
}

// gameLibraryBack is B (and Esc): one level at a time. A console's page goes
// back to the grid; on the grid a search, then a tag filter, is dropped
// before the library closes, since a filtered grid looks like a broken one.
func gameLibraryBack() {
	if gameLib.focus == 1 {
		gameLib.focus = 0
		PlayBackSound()
		return
	}
	if gameLib.query != "" {
		setGameLibraryQuery("")
		PlayBackSound()
		return
	}
	if gmetaClearFilter() {
		return
	}
	closeGameLibraryPanel()
}

func selectedLibrarySystem() (librarySystem, bool) {
	systems := currentGameSystems()
	if gameLib.system < 0 || gameLib.system >= len(systems) {
		return librarySystem{}, false
	}
	return systems[gameLib.system], true
}

// selectedLibraryGame returns the highlighted game, clamped to the system.
func selectedLibraryGame() (libraryGame, bool) {
	system, ok := selectedLibrarySystem()
	if !ok || len(system.Games) == 0 {
		return libraryGame{}, false
	}
	if gameLib.cursor < 0 {
		gameLib.cursor = 0
	}
	if gameLib.cursor >= len(system.Games) {
		gameLib.cursor = len(system.Games) - 1
	}
	return system.Games[gameLib.cursor], true
}

// A built-in game (JukaLand, the Synth) opens its own scene directly; there
// is no emulator, ROM file or save-state page to prepare first. An arcade
// minigame (Snake, Pong…) opens the Games screen already playing it.
func openBuiltInLibraryGame(game libraryGame, config *Config) bool {
	builtIn, ok := builtInLibraryGameFor(game)
	if !ok {
		return false
	}
	idx := sceneIndexExact(config, builtIn.scene)
	if idx < 0 {
		ShowToast(builtIn.title+" is unavailable in this configuration", ToastKindWarning)
		return true
	}
	if game.Path == gameRandomPick {
		trophyEvent(tkGameRandom)
	}
	gameRandomPick = ""
	closeGameLibraryPanel()
	changeSceneTo(config, idx)
	// Queued only when the switch was taken (switchScene ignores one asked
	// for during a fade), else a later visit would start the game unasked.
	// Already on the Games screen it starts on the next frame.
	if builtIn.arcade != gameNone && (idx == currentSceneIndex || (transitionPhase == "fade-out" && pendingSceneIndex == idx)) {
		queueGameStart(config, builtIn.arcade)
	}
	PlayActivateSound()
	return true
}

func scanGameLibrary() gameLibraryReport {
	lib := buildGameLibrary(scanROMLibrary(romLibraryRoots()), libraryArtworkRoot())
	libraryAddHTML5Games(&lib, nil, html5LibraryRoots(), html5DataRoot())
	libraryAddBuiltInGames(&lib, builtInScenesPresent(appConfig), builtInCoverRoot())
	return lib
}

func openGameLibraryPanel() {
	// One modal at a time: the library replaces the panels that can open it.
	emuPanel.open = false
	qs.open = false
	toolsPanel.open = false
	closeGameLibraryArt()
	gameLib.open = true
	gameLib.focus = 0
	gameLib.system = 0
	gameLib.cursor = 0
	gameLib.scroll = 0
	gameLib.sysScroll = 0
	gameLib.query = ""
	gameLib.logoSystem = ""
	gameLib.logoAt = 0
	resetGameRandomPicker()
	resetGameLibMenu()
	// A fresh visit shows every game, and the per-game data loads in the
	// background so a game's page opens without a read.
	gmetaLibraryOpened()
	gameLib.artSeen = artworkFetchGeneration()
	gameLib.systemHits = nil
	gameLib.gameHits = nil
	invalidateGameLibrarySystemsCache()
	gameLib.recents = recentGames()
	// Keep the previous scan on screen while the new one runs; first open shows
	// the scanning state instead of freezing the render loop on the card walk.
	startGameLibraryScan()
	// The header carries the clip count — the same figure Settings → General
	// reports — so opening the library is when the ask starts. The scan runs
	// as scene work off this goroutine and the header reads the finished
	// figure when it lands; the census is reused for 30 seconds, so flapping
	// the panel open does not re-walk the card.
	snapClipCountRefresh(snapPreviewRoot(appConfig))
	PlayActivateSound()
}

// gameLibraryShelfWant is the console a press asked the library to open on
// (the Godot and Xbox tiles' A), the scan it waits for, and what to say when
// that console has no games once the scan is in. Main thread.
var gameLibraryShelfWant struct {
	system string
	gen    uint64
	empty  string
}

// openGameLibraryOnSystem opens the library on system's games. The scan the
// open starts runs in the background, so the shelf is picked from the last
// scan at once when it has the console, and again when the new scan lands
// (gameLibraryShelfAfterScan): a first open after boot, or games just copied
// in, are only in the new one. empty is the toast when there are none.
func openGameLibraryOnSystem(system, empty string) {
	openGameLibraryPanel()
	found := gameLibrarySelectSystem(system)
	if !gameLib.scanning {
		if !found && empty != "" {
			ShowToast(empty, ToastKindInfo)
		}
		return
	}
	gameLibraryShelfWant.system, gameLibraryShelfWant.gen, gameLibraryShelfWant.empty = system, gameLib.scanGen, empty
}

// gameLibrarySelectSystem opens system's games when the current list has
// some, and reports whether it did.
func gameLibrarySelectSystem(system string) bool {
	for i, s := range currentGameSystems() {
		if s.Name == system && len(s.Games) > 0 {
			gameLib.system = i
			gameLib.cursor, gameLib.scroll = 0, 0
			gameLib.focus = 1
			return true
		}
	}
	return false
}

// gameLibraryShelfAfterScan finishes openGameLibraryOnSystem once scan gen
// is in. The console is picked again by name: the new scan may list the
// consoles in another order.
func gameLibraryShelfAfterScan(gen uint64) {
	want := gameLibraryShelfWant
	if want.system == "" || want.gen != gen {
		return
	}
	gameLibraryShelfWant.system = ""
	if !gameLibrarySelectSystem(want.system) && want.empty != "" {
		ShowToast(want.empty, ToastKindInfo)
	}
}

func closeGameLibraryPanel() {
	if !gameLib.open {
		return
	}
	// The options and random sheets and the menu cannot outlive the library
	// they belong to.
	gameOptsUI.open = false
	gameOptsUI.rowHits = nil
	resetGameRandomPicker()
	resetGameLibMenu()
	// So can a game's page, its tag sheet and the tag filter's chooser; the
	// page's screenshots are textures and go with it.
	gmetaCloseAll()
	gameLibraryScrubReset()
	gameLib.open = false
	// A scan that finishes after this must not write into a closed panel or a
	// later visit that has already started its own scan.
	gameLib.scanGen++
	gameLib.scanning = false
	libIndexForgetEarly() // an unchecked last library is not a scan (libindex.go)
	invalidateGameLibrarySystemsCache()
	closeGameLibraryArt()
	artStyleBgFree() // console backgrounds' textures (artstyle.go)
	PlayBackSound()
}

// closeGameLibraryArt frees the preview textures. They belong to the current
// renderer, so keeping them across a renderer recreation would dangle.
func closeGameLibraryArt() {
	// A preview belongs to the panel and to the focused game: closing the panel
	// (or losing the art cache to a finished fetch) ends the decoder.
	snapPreviewStop()
	resetLibraryArtDecode()
	for path, tex := range gameLib.artCache {
		if tex != nil {
			tex.Destroy()
		}
		delete(gameLib.artCache, path)
	}
	gameLib.artOrder = nil
	gameLib.artSize = nil
	gameLib.artBytes = 0
	gameLib.artRect = sdl.Rect{}
}

// startGameLibraryScan walks the card and builds the report on a goroutine. The
// panel keeps its previous list on screen until the result is posted back; a
// generation check discards a result from a closed panel or an older rescan.
func startGameLibraryScan() {
	if gameLib.scanning {
		return
	}
	gameLib.scanGen++
	gen := gameLib.scanGen
	gameLib.scanning = true
	invalidateGameLibrarySystemsCache()
	roots := append([]string(nil), romLibraryRoots()...)
	html5Roots := append([]string(nil), html5LibraryRoots()...)
	html5DemoRoot := html5DataRoot()
	artRoot := libraryArtworkRoot()
	builtInScenes := builtInScenesPresent(appConfig)
	librarySortArticlesSync(appConfig) // the build sorts off this thread (libsort_articles.go)
	// Copy the playtime map on the render thread; the background build must not
	// read it while the ticker is writing it. The snapshot is never nil, so the
	// goroutine never falls back to reading the live ledger.
	seconds := libraryPlaytimeSnapshot()
	early := gameLib.lib.Total == 0
	go func() {
		// With nothing on screen yet, the last library the index kept shows
		// at once while the card is checked (LIB-1, libindex.go).
		libIndexShowEarly(gen, roots, early)
		// The folders' signature before the walk lets the automatic box-art
		// pass use this scan while nothing changed (LIB-19, boxart_auto.go).
		signature := boxArtLibrarySignature(roots)
		// Through the library index: only folders that changed are read.
		report := libIndexScan(roots)
		romScanPublish(roots, signature, report.Games)
		// Overlays/<Console> folders for the consoles found (useroverlays.go).
		userOverlaysAfterScan(roots, report.Games)
		// Resolved once against the whole scan, so the Android row below gets
		// no bare-title figure a ROM console already took.
		seconds := libraryPlaytimeForTitles(seconds, libraryScanTitleConsoles(report.Games))
		lib := buildGameLibraryWith(report, artRoot, seconds)
		// The APK Store's Android games join as one more console (AND-16).
		libraryAddAndroidGames(&lib, seconds)
		libraryAddHTML5Games(&lib, seconds, html5Roots, html5DemoRoot)
		libraryAddBuiltInGames(&lib, builtInScenes, builtInCoverRoot())
		libIndexSaveLibrary(roots, lib)
		// Games on the network drives join their consoles' shelves, after the
		// index kept the card's own (nas_index.go).
		libraryAddNetworkGames(&lib, seconds)
		tbxPost(func() {
			keep := libIndexSelectionNow()
			if !applyGameLibraryScan(&gameLib, gen, lib, recentGames()) {
				return
			}
			clampGameLibrarySelection()
			// The selected game stays selected in the refreshed list.
			keep.restore()
			// A search or tag set while the first scan ran (a phone hand-off, a
			// Nearby share) was judged against an empty library; with the games
			// in, its only console opens the way it would have.
			if gameLib.focus == 0 && gameLib.system == 0 && (gameLib.query != "" || gmetaFilterTag() != "") {
				gameLibraryFocusFiltered()
			}
			// The Random game plugin's press that opened the library rolls now.
			gameRandomAfterScan(gen)
			// A tile's A that opened the library on a console (openGameLibraryOnSystem).
			gameLibraryShelfAfterScan(gen)
		})
	}()
}

// applyGameLibraryScan installs a finished scan only when it belongs to the
// panel's current generation. It is separated so the stale-result rule has a
// test without needing a live SDL loop.
func applyGameLibraryScan(panel *gameLibraryPanel, gen uint64, lib gameLibraryReport, recents []recentGame) bool {
	if panel == nil || gen != panel.scanGen {
		return false
	}
	panel.lib = lib
	panel.recents = recents
	panel.scanning = false
	panel.systemsCache = nil
	panel.systemsValid = false
	return true
}

// clampGameLibrarySelection keeps the highlight on a real row after a scan or
// a view change shrinks the visible list.
func clampGameLibrarySelection() {
	systems := currentGameSystems()
	if gameLib.system >= len(systems) {
		gameLib.system = len(systems) - 1
	}
	if gameLib.system < 0 {
		gameLib.system = 0
	}
	if system, ok := selectedLibrarySystem(); ok && gameLib.cursor >= len(system.Games) {
		gameLib.cursor = len(system.Games) - 1
	}
	if gameLib.cursor < 0 {
		gameLib.cursor = 0
	}
	gameLib.scroll = 0
}

// refreshGameLibrary starts a background rescan; callers can keep the panel
// interactive while it runs.
func refreshGameLibrary() {
	// Games were added or Y asked: every folder is listed again (libindex.go).
	libIndexNoteChange("")
	startGameLibraryScan()
}

func gameLibraryStatus() string {
	if gameLib.scanning {
		if gameLib.lib.Total == 0 {
			return "Scanning the card…"
		}
		return "Scanning for changes… · " + pluralizeCount(gameLib.lib.Total, "game", "games") + " already found"
	}
	if tag := gmetaFilterTag(); tag != "" && gameLib.query == "" {
		games := 0
		for _, system := range currentGameSystems() {
			games += len(system.Games)
		}
		return "Showing " + tag + " · " + pluralizeCount(games, "game", "games") + " — " + hintKeyFor("B", "Back") + " shows every game"
	}
	if query := gameLib.query; query != "" {
		games, systems := 0, 0
		for _, system := range currentGameSystems() {
			systems++
			games += len(system.Games)
		}
		if games == 0 {
			return "No match for \"" + query + "\" — press " + hintKeyFor("S / R1", "Search") + " to change it"
		}
		return pluralizeCount(games, "match", "matches") + " for \"" + query + "\" in " + pluralizeCount(systems, "system", "systems")
	}
	if gameLib.lib.Total == 0 {
		if len(gameLib.recents) > 0 {
			return pluralizeCount(len(gameLib.recents), "recent game", "recent games") + " — no game folders found, Y rescans"
		}
		return "No games yet — copy ROMs or HTML5 games to the card, then press Y"
	}
	if hidden := libraryHiddenCount(gameLib.lib); hidden > 0 && len(currentGameSystems()) == 0 {
		return pluralizeCount(hidden, "hidden game", "hidden games") + " — Game options → Show hidden reveals them"
	}
	onCard, onNet := nasLibraryStatusSplit(gameLib.lib.Total) // network drives' games apart (nas_index.go)
	status := pluralizeCount(onCard, "game", "games") + " in " + pluralizeCount(len(gameLib.lib.Systems), "system", "systems") + onNet
	if withArt, coverTotal := libraryCoverTotals(gameLib.lib); coverTotal > 0 {
		status += " · " + itoa(coverPercent(withArt, coverTotal)) + "% with covers"
	}
	// The clip count, from the same census the Settings row reads — the figure
	// that matters most on this screen, because this is where the previews
	// play. It stays off the line until the first scan lands, the way the row
	// stays plain, and a card with none says so in words rather than as a
	// zero that reads like a broken figure.
	if withClip, _, known := snapClipCount(); known {
		if withClip == 0 {
			status += " · no clips"
		} else {
			status += " · " + pluralizeCount(withClip, "clip", "clips")
		}
	}
	if hidden := libraryHiddenCount(gameLib.lib); hidden > 0 {
		status += " · " + itoa(hidden) + " hidden"
	}
	if len(gameLib.recents) > 0 {
		status += " · " + itoa(len(gameLib.recents)) + " recent"
	}
	if artworkFetchRunning() {
		done, total, _ := artworkFetchProgress()
		status += " · fetching " + itoa(done) + "/" + itoa(total)
	}
	return status
}

// gameLibraryGridStep is where one D-pad step lands in the consoles grid of n
// buttons in cols columns, or -1 when the step leads off the grid. Left and
// Right run through the buttons in reading order, so Right at the end of a
// row continues on the next one. Down from the row above a short last row
// lands on that row's last button rather than nowhere.
func gameLibraryGridStep(index, n, cols, dx, dy int) int {
	if n <= 0 {
		return -1
	}
	if cols < 1 {
		cols = 1
	}
	next := index + dx + dy*cols
	if dy > 0 && next >= n && index/cols < (n-1)/cols {
		next = n - 1
	}
	if next < 0 || next >= n || next == index {
		return -1
	}
	return next
}

// gameLibraryMove handles D-pad navigation: across the consoles grid, or in
// the selected console's games (where Left — Up on the 3D shelf — goes back
// to the grid).
func gameLibraryMove(dx, dy int) {
	systems := currentGameSystems()
	if len(systems) == 0 {
		return
	}
	if gameLib.focus == 0 {
		next := gameLibraryGridStep(gameLib.system, len(systems), gameLib.gridCols, dx, dy)
		if next < 0 {
			return
		}
		gameLib.system = next
		gameLib.cursor = 0
		gameLib.scroll = 0
		PlayNavSound()
		return
	}
	// Cover Flow and the Cube have their own steps (game_library_cube.go).
	if gameLibraryLayoutMove(dx, dy) {
		return
	}
	if libraryLayoutMode() == libraryLayoutHorizontal {
		if dy < 0 {
			gameLib.focus = 0
			PlayNavSound()
			return
		}
		system, ok := selectedLibrarySystem()
		if !ok || len(system.Games) == 0 || dx == 0 {
			return
		}
		gameLib.cursor = (gameLib.cursor + dx + len(system.Games)) % len(system.Games)
		PlayNavSound()
		return
	}
	if dx < 0 {
		gameLib.focus = 0
		PlayNavSound()
		return
	}
	if dx > 0 {
		// A column has no row to the right: Right jumps to the next letter
		// (game_library_scrub.go).
		gameLibraryScrub(1)
		return
	}
	system, ok := selectedLibrarySystem()
	if !ok || len(system.Games) == 0 {
		return
	}
	if dy == 0 {
		return
	}
	gameLib.cursor += dy
	if gameLib.cursor < 0 {
		gameLib.cursor = len(system.Games) - 1
	}
	if gameLib.cursor >= len(system.Games) {
		gameLib.cursor = 0
	}
	PlayNavSound()
}

// fetchMissingLibraryArtwork starts a background download for every scanned
// game without a local cover, or stops one that is already running. The scan is
// repeated here instead of reusing the panel's snapshot because a card can be
// updated while the panel is open; that also makes the action resumable, since
// only the covers still missing are queued.
func fetchMissingLibraryArtwork() {
	if artworkFetchRunning() {
		if stopArtworkFetch() {
			ShowToast("Stopping the artwork fetch…", ToastKindInfo)
		} else {
			ShowToast(artworkFetchSummary(), ToastKindInfo)
		}
		return
	}
	artRoot := libraryArtworkRoot()
	if strings.TrimSpace(artRoot) == "" {
		ShowToast("The artwork folder is unavailable.", ToastKindWarning)
		return
	}
	report := libIndexScan(romLibraryRoots())
	missing := romsMissingArtwork(report.Games, artRoot)
	if len(missing) == 0 {
		// Settings → Game art → Art type: screenshots or title screens too (artstyle_fetch.go).
		if !artStyleFillQueue(report.Games, 0) {
			ShowToast("Every scanned game already has box art.", ToastKindSuccess)
		}
		return
	}
	// The console on screen first, then every console in turns, so the one
	// being looked at fills in at once and none waits behind a big one.
	first := ""
	if system, ok := selectedLibrarySystem(); ok {
		first = system.Name
	}
	if !startLibraryArtworkFetch(artworkFetchOrder(missing, first)) {
		ShowToast("An artwork fetch is already running.", ToastKindInfo)
		return
	}
	ShowToast("Fetching "+pluralizeCount(len(missing), "missing cover", "missing covers")+" — "+hintKeyFor("L1 / F", "Stop")+" stops and resumes", ToastKindInfo)
	artStyleFillQueue(report.Games, len(missing)) // queued behind the covers (artstyle_fetch.go)
}

// recountLibraryArtwork refreshes the HasArt flags and the completeness counts
// after a fetch. The card is not rescanned: only the stat calls change, and
// they run once per completed fetch rather than on every frame.
func recountLibraryArtwork() {
	total, with := 0, 0
	root := libraryArtworkRoot()
	for i := range gameLib.lib.Systems {
		system := &gameLib.lib.Systems[i]
		for j := range system.Games {
			g := &system.Games[j]
			if playShotIsCover(g.Art) {
				// A picture from the game gives way to a cover found since
				// (playshot_cover.go).
				g.Art = romCoverPath(scannedROM{Path: g.Path, System: g.System}, root)
			}
			g.HasArt = artworkCoverPresent(g.Art)
		}
		system.WithArt = countCoveredGames(system.Games)
		total += len(system.Games)
		with += system.WithArt
	}
	gameLib.lib.Total = total
	gameLib.lib.WithArt = with
	invalidateGameLibrarySystemsCache()
}

// handleGameLibraryKey consumes every key while the panel is open so the scene
// underneath never reacts behind the modal.
func handleGameLibraryKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !gameLib.open {
		return false
	}
	// The sheets are stacked on the library, so they read every key first.
	if gameOptsUI.open {
		return handleGameOptionsKey(e)
	}
	if gameRandomUI.open {
		return handleGameRandomKey(e)
	}
	if gameLibMenu.open {
		return handleGameLibMenuKey(e)
	}
	// A game's page, its tag sheet and the tag filter (gmeta_ui.go).
	if gmetaSheetOpen() {
		return gmetaHandleKey(e, config)
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		gameLibraryMove(0, -1)
	case sdl.K_DOWN:
		gameLibraryMove(0, 1)
	case sdl.K_LEFT:
		gameLibraryMove(-1, 0)
	case sdl.K_RIGHT:
		gameLibraryMove(1, 0)
	case sdl.K_PAGEUP:
		// L2 / R2's twins: the previous / next letter (game_library_scrub.go).
		gameLibraryScrub(-1)
	case sdl.K_PAGEDOWN:
		gameLibraryScrub(1)
	case sdl.K_RETURN, sdl.K_SPACE:
		// A game opens its page (box art, details, tags, save states) with
		// Play focused; a console opens its list.
		gmetaOpenSelected()
	case sdl.K_t:
		gmetaOpenFilterPicker()
	case sdl.K_e:
		// A game's actions belong to a console's page; on the grid no game is
		// highlighted, only the console.
		if game, ok := selectedLibraryGame(); ok && gameLib.focus == 1 {
			openGameLibraryHandoff(game)
		}
	case sdl.K_o:
		openGameOptions()
	case sdl.K_r:
		openGameRandomPicker()
	case sdl.K_m:
		// Select's twin: the library's menu (game_library_menu.go).
		openGameLibMenu()
	case sdl.K_x:
		if gameLib.focus == 1 {
			libraryOpenNetplay()
		} else {
			gameLibraryLookPress() // the next Console look
		}
	case sdl.K_f:
		fetchMissingLibraryArtwork()
	case sdl.K_y:
		refreshGameLibrary()
		ShowToast(gameLibraryStatus(), ToastKindInfo)
	case sdl.K_s, sdl.K_SLASH:
		vkOpenGameSearch()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		gameLibraryBack()
	}
	return true
}

func ctrlGameLibrary(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return false
	}
	// The on-screen keyboard (the search, a game's description or notes, a new
	// tag) takes the pad while it is up: the overlay router asks the library
	// first, and the library used to keep the D-pad and A from it.
	if virtualKeyboardActive {
		return false
	}
	if gameOptsUI.open {
		return ctrlGameOptions(e)
	}
	if gameRandomUI.open {
		return ctrlGameRandom(e)
	}
	if gameLibMenu.open {
		return ctrlGameLibMenu(e)
	}
	// A game's page, its tag sheet and the tag filter (gmeta_ui.go).
	if gmetaSheetOpen() {
		return gmetaHandlePad(e, config)
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		gameLibraryMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		gameLibraryMove(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		gameLibraryMove(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		gameLibraryMove(1, 0)
	case sdl.CONTROLLER_BUTTON_A:
		gmetaOpenSelected()
	case sdl.CONTROLLER_BUTTON_BACK:
		// Select is the library's menu: a random game, the tag filter, search,
		// box art and a rescan (game_library_menu.go). Those used to sit on
		// the stick clicks, which the Smart Pro does not have. The handheld's
		// Menu key arrives as BACK too and keeps going back, like B.
		if gameLibSelectPressed(e) {
			openGameLibMenu()
		} else {
			gameLibraryBack()
		}
	case sdl.CONTROLLER_BUTTON_LEFTSTICK, sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		// The stick clicks (a Brick's F1 / F2) have no job here: the library
		// is modal, and nothing a screen needs sits on them (stickclicks.go).
	case sdl.CONTROLLER_BUTTON_X:
		if gameLib.focus == 1 {
			libraryOpenNetplay()
		} else {
			gameLibraryLookPress() // the next Console look
		}
	case sdl.CONTROLLER_BUTTON_START:
		openGameOptions()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		fetchMissingLibraryArtwork()
	case sdl.CONTROLLER_BUTTON_Y:
		refreshGameLibrary()
		ShowToast(gameLibraryStatus(), ToastKindInfo)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		// R1 searches. The shoulders are otherwise blocked while this panel is
		// open (shoulders.go), so the trigger is free here.
		vkOpenGameSearch()
	case sdl.CONTROLLER_BUTTON_B:
		gameLibraryBack()
	default:
		return false
	}
	return true
}

// handleGameLibraryMouse opens a console's page from its button, opens a game,
// goes one level back from the page header (B's path), or closes the library
// from the top bar. The page fills the screen, so a click anywhere else is
// simply taken.
func handleGameLibraryMouse(mx, my int32, config *Config) bool {
	if !gameLib.open {
		return false
	}
	if gameOptsUI.open {
		return handleGameOptionsMouse(mx, my)
	}
	if gameRandomUI.open {
		return handleGameRandomMouse(mx, my)
	}
	if gameLibMenu.open {
		return handleGameLibMenuMouse(mx, my)
	}
	if gmetaSheetOpen() {
		return gmetaHandleMouse(mx, my, config)
	}
	// The hit lists belong to the level on screen: the grid's buttons and a
	// page's games are never both drawn, so neither list is stale here.
	for _, hit := range gameLib.systemHits {
		if gameLib.focus == 0 && pointInRect(mx, my, hit.rect) {
			if hit.index != gameLib.system {
				gameLib.system = hit.index
				gameLib.cursor = 0
				gameLib.scroll = 0
			}
			// A console button is pushed, not just highlighted.
			gmetaOpenSelected()
			return true
		}
	}
	for _, hit := range gameLib.gameHits {
		if gameLib.focus == 1 && pointInRect(mx, my, hit.rect) {
			gameLib.cursor = hit.index
			gmetaOpenSelected()
			return true
		}
	}
	if gameLib.backRect.W > 0 && pointInRect(mx, my, gameLib.backRect) {
		gameLibraryBack()
		return true
	}
	// The top bar is drawn but its buttons sit under the library; a click on
	// it leaves the library (the old "click outside the card"), so a mouse
	// alone is never stuck here.
	if my < headerBarHeight() {
		closeGameLibraryPanel()
	}
	return true
}

// The two box-art ceilings. One cover is a few hundred KB at the size the
// preview draws it, so the handheld budget holds well over a screen's worth of
// scrolling history while a large collection cannot pin a hundred megabytes of
// card RAM.
const (
	libraryArtHandheldBudget = int64(32 << 20)
	libraryArtDesktopBudget  = int64(96 << 20)
)

// libraryArtBudgetForce pins the handheld/desktop branch: 0 derives it from the
// device, 1 forces the handheld ceiling, -1 the desktop one. It exists so the
// scene-navigation memory harness can verify the 32 MB path on any host (and in
// CI's -race job) instead of measuring whichever branch the machine happens to
// take. It is atomic because the eviction path reads it from the render loop
// while a test may still be painting the panel.
var libraryArtBudgetForce atomic.Int32

// libraryArtOnHandheld reports which ceiling applies on this device.
func libraryArtOnHandheld() bool {
	switch libraryArtBudgetForce.Load() {
	case 1:
		return true
	case -1:
		return false
	}
	return IsTrimuiDevice() || P().IsHandheld()
}

// libraryArtBudgetBytes caps the box-art textures held by the panel, using the
// handheld ceiling on a handheld and the desktop one elsewhere.
func libraryArtBudgetBytes() int64 {
	if libraryArtOnHandheld() {
		return libraryArtHandheldBudget
	}
	return libraryArtDesktopBudget
}

// libraryArtMaxTextures also bounds the count, so a library whose covers are
// all missing (cached as nil, a few bytes each) cannot grow the map either.
const libraryArtMaxTextures = 256

// resetArtCache starts the art cache empty. Called on first use and wherever
// the cache is dropped wholesale (a finished artwork fetch).
func (p *gameLibraryPanel) resetArtCache() {
	p.artCache = map[string]*sdl.Texture{}
	p.artSize = map[string]int64{}
	p.artOrder = nil
	p.artBytes = 0
}

// rememberArt stores a cover texture (nil for a failed load, so a missing file
// is not re-read every frame) and evicts the oldest entries past the budget.
func (p *gameLibraryPanel) rememberArt(path string, tex *sdl.Texture, w, h int32) {
	// The three maps have to exist together. A caller (or a test) that dropped
	// only artCache still leaves the accounting usable instead of panicking on
	// a nil map write.
	if p.artCache == nil {
		p.artCache = map[string]*sdl.Texture{}
	}
	if p.artSize == nil {
		p.artSize = map[string]int64{}
	}
	bytes := int64(w) * int64(h) * 4
	p.artCache[path] = tex
	p.artSize[path] = bytes
	p.artOrder = append(p.artOrder, path)
	p.artBytes += bytes

	budget := libraryArtBudgetBytes()
	for len(p.artOrder) > 1 &&
		(len(p.artOrder) > libraryArtMaxTextures || p.artBytes > budget) {
		oldest := p.artOrder[0]
		p.artOrder = p.artOrder[1:]
		if old, ok := p.artCache[oldest]; ok {
			if old != nil {
				old.Destroy()
			}
			delete(p.artCache, oldest)
		}
		p.artBytes -= p.artSize[oldest]
		delete(p.artSize, oldest)
	}
}

// artTexture loads and caches a box-art texture. A failed load is cached as a
// nil so a missing file does not hit the disk on every frame.
func artTexture(renderer *sdl.Renderer, path string) *sdl.Texture {
	if renderer == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	if tex, ok := gameLib.artCache[path]; ok {
		return tex
	}
	surface, err := img.Load(path)
	if err != nil {
		gameLib.rememberArt(path, nil, 0, 0)
		return nil
	}
	tex, _ := renderer.CreateTextureFromSurface(surface)
	surface.Free()
	var w, h int32
	if tex != nil {
		if _, _, tw, th, qerr := tex.Query(); qerr == nil {
			w, h = tw, th
		}
	}
	gameLib.rememberArt(path, tex, w, h)
	return tex
}

const libraryArtDecodeMaxEdge = 512

var errLibraryArtDimensions = errors.New("cover dimensions are invalid or exceed the decode limit")

func libraryArtDimensionsAllowed(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	return int64(w) <= imageDecodeMaxPixels()/int64(h)
}

// setLibraryArtFocus invalidates a result as soon as the highlighted cover
// changes. If a decoder is running, it gets a cancellation signal; the next
// cover waits for that single worker to finish before it starts.
func setLibraryArtFocus(path string) {
	path = strings.TrimSpace(path)
	libraryArtDecode.mu.Lock()
	if path != libraryArtDecode.path {
		libraryArtDecode.path = path
		libraryArtDecode.generation++
		libraryArtDecode.done = nil
		if libraryArtDecode.cancel != nil {
			libraryArtDecode.cancel()
		}
	}
	libraryArtDecode.mu.Unlock()
}

// resetLibraryArtDecode drops any picture waiting for upload and makes an
// in-flight result stale when the panel closes or its textures are cleared.
func resetLibraryArtDecode() {
	libraryArtDecode.mu.Lock()
	libraryArtDecode.path = ""
	libraryArtDecode.generation++
	libraryArtDecode.done = nil
	if libraryArtDecode.cancel != nil {
		libraryArtDecode.cancel()
	}
	libraryArtDecode.mu.Unlock()
}

// requestLibraryArtDecode starts one bounded background decode for the
// currently focused game. It is cheap to call from the render loop.
func requestLibraryArtDecode(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}

	libraryArtDecode.mu.Lock()
	if path != libraryArtDecode.path || libraryArtDecode.pending || libraryArtDecode.done != nil {
		libraryArtDecode.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	generation := libraryArtDecode.generation
	libraryArtDecode.pending = true
	libraryArtDecode.pendingGen = generation
	libraryArtDecode.cancel = cancel
	libraryArtDecode.mu.Unlock()

	go func() {
		picture, err := decodeLibraryArt(path, ctx)
		libraryArtDecode.mu.Lock()
		if libraryArtDecode.pendingGen == generation {
			libraryArtDecode.pending = false
			libraryArtDecode.pendingGen = 0
			libraryArtDecode.cancel = nil
		}
		if libraryArtDecode.generation == generation && libraryArtDecode.path == path {
			libraryArtDecode.done = &libraryArtDecodeResult{
				path: path, generation: generation, picture: picture, err: err,
			}
		}
		libraryArtDecode.mu.Unlock()
	}()
}

// takeLibraryArtDecode transfers a finished picture to the render thread once.
func takeLibraryArtDecode(path string) (*libraryArtDecodeResult, bool) {
	libraryArtDecode.mu.Lock()
	defer libraryArtDecode.mu.Unlock()
	result := libraryArtDecode.done
	if result == nil || result.path != path || result.generation != libraryArtDecode.generation {
		return nil, false
	}
	libraryArtDecode.done = nil
	return result, true
}

// decodeLibraryArt decodes and shrinks one cover away from the render thread.
// The header is checked before allocating pixels, and the output is capped to
// 512px on its longest side so both the temporary image and GPU texture stay
// small even when a downloaded cover is poster-sized.
func decodeLibraryArt(path string, ctx context.Context) (*image.NRGBA, error) {
	if !imageDecodeSem.Acquire(ctx, 1) {
		return nil, ctx.Err()
	}
	defer imageDecodeSem.Release(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var picture image.Image
	if config, _, cfgErr := image.DecodeConfig(file); cfgErr == nil {
		if !libraryArtDimensionsAllowed(config.Width, config.Height) {
			return nil, errLibraryArtDimensions
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if picture, _, err = image.Decode(file); err != nil {
			return nil, err
		}
	} else {
		// Go has no WebP decoder, and a local cover can be a .webp; SDL_image
		// reads it (and caps the pixel count itself). The size check below
		// still applies to what it returns.
		if picture, err = imgvDecodeSDL(path); err != nil {
			return nil, cfgErr
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bounds := picture.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if !libraryArtDimensionsAllowed(w, h) {
		return nil, errLibraryArtDimensions
	}
	if edge := max(w, h); edge > libraryArtDecodeMaxEdge {
		scale := float64(libraryArtDecodeMaxEdge) / float64(edge)
		w = max(1, int(math.Round(float64(w)*scale)))
		h = max(1, int(math.Round(float64(h)*scale)))
	}
	return imgvScale(picture, w, h), nil
}

// libraryArtTexture uploads a prepared cover on the render thread. Until the
// worker finishes (or when a cover is missing/broken), callers draw the normal
// monogram placeholder without waiting for disk or image decoding.
func libraryArtTexture(renderer *sdl.Renderer, path string) *sdl.Texture {
	path = strings.TrimSpace(path)
	setLibraryArtFocus(path)
	if renderer == nil || path == "" {
		return nil
	}
	if gameLib.artCache == nil {
		gameLib.resetArtCache()
	}
	if tex, ok := gameLib.artCache[path]; ok {
		return tex
	}
	requestLibraryArtDecode(path)
	result, ready := takeLibraryArtDecode(path)
	if !ready {
		return nil
	}
	if result.err != nil || result.picture == nil {
		gameLib.rememberArt(path, nil, 0, 0)
		return nil
	}
	tex := imgvTexture(renderer, result.picture)
	var w, h int32
	if tex != nil {
		if _, _, tw, th, err := tex.Query(); err == nil {
			w, h = tw, th
		}
	}
	gameLib.rememberArt(path, tex, w, h)
	return tex
}

// drawLibraryArt draws box art fitted inside rect, or a monogram placeholder
// when the game has no downloaded image yet.
func drawLibraryArt(renderer *sdl.Renderer, config *Config, rect sdl.Rect, game libraryGame) {
	defer nasLibraryArtBadge(renderer, game, rect) // a network drive's game (nas_index.go)
	fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 14, WithAlpha(ColorSurfaceRow, 160))
	strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 14, 1, ColorBorderDefault)

	// A video snap for this game wins over its cover while it plays: the box
	// holds one picture, and the clip is the more useful one. Until the first
	// frame arrives the cover stays, so entering the library never flashes a
	// grey box.
	if snap := snapPreviewTexture(renderer, game.Path); snap != nil {
		setLibraryArtFocus("")
		drawSnapFrame(renderer, config, rect, snap)
		return
	}

	tex := libraryArtTexture(renderer, artStyleArtPath(game)) // box art, or the Art type's picture (artstyle.go)
	if tex != nil {
		_, _, tw, th, err := tex.Query()
		if err == nil && tw > 0 && th > 0 {
			inner := rect.W - 16
			if rect.H-16 < inner {
				inner = rect.H - 16
			}
			dw := inner
			dh := int32(float64(inner) * float64(th) / float64(tw))
			if dh > inner {
				dh = inner
				dw = int32(float64(inner) * float64(tw) / float64(th))
			}
			dst := sdl.Rect{
				X: rect.X + (rect.W-dw)/2,
				Y: rect.Y + (rect.H-dh)/2,
				W: dw,
				H: dh,
			}
			artStyleDrawFitted(renderer, tex, rect, dst) // Art width / corner radius (artstyle.go)
			return
		}
	}

	// Placeholder: initials on an accent tint.
	font, _ := getCachedFont(config, "large")
	if font == nil {
		return
	}
	initials := libraryArtInitials(game.Title)
	tw, th, _ := font.SizeUTF8(initials)
	renderText(renderer, config, font, initials,
		WithAlpha(installHighlight(), 230),
		rect.X+(rect.W-int32(tw))/2, rect.Y+(rect.H-int32(th))/2)
}

// libraryArtInitials is the placeholder's monogram: the first letter or digit
// of the first two words, without a release-list number in front ("0381 - 007
// - From Russia with Love" showed "0-": a digit and the dash).
func libraryArtInitials(title string) string {
	title = strings.TrimSpace(title)
	if bare, ok := artworkStripReleaseNumber(title); ok {
		title = bare
	}
	words := strings.FieldsFunc(title, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '’'
	})
	var out []rune
	for _, w := range words {
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// drawCoverCompletenessRing draws a ring filled clockwise to percent, with the
// percentage inside it. Line segments keep it independent of SDL_gfx, and one
// completed arc is cheap enough to redraw with the panel.
func drawCoverCompletenessRing(renderer *sdl.Renderer, config *Config, cx, cy, radius int32, percent int) {
	if renderer == nil || radius <= 4 {
		return
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	// Alpha is used for the unfinished part of the ring, and the caller may
	// have left the renderer in an opaque mode.
	renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	const segments = 48
	filled := segments * percent / 100
	if percent > 0 && filled == 0 {
		filled = 1
	}
	accent := installHighlight()
	// Two concentric circles give the ring a 2px stroke on a 720p panel.
	for _, r := range []int32{radius, radius - 1} {
		for i := 0; i < segments; i++ {
			a0 := 2*math.Pi*float64(i)/segments - math.Pi/2
			a1 := 2*math.Pi*float64(i+1)/segments - math.Pi/2 + 0.03
			// The unfinished track stays faint: it is a hint, not a value. The
			// segments overlap slightly at their joins, so this is drawn twice
			// per pixel and the alpha has to stay low to avoid a bright ring.
			col, alpha := ColorBorderDefault, uint8(24)
			if i < filled {
				col, alpha = accent, 240
			}
			renderer.SetDrawColor(col.R, col.G, col.B, alpha)
			renderer.DrawLine(
				cx+int32(float64(r)*math.Cos(a0)), cy+int32(float64(r)*math.Sin(a0)),
				cx+int32(float64(r)*math.Cos(a1)), cy+int32(float64(r)*math.Sin(a1)))
		}
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	label := itoa(percent) + "%"
	tw, th, _ := font.SizeUTF8(label)
	if int32(tw) > 2*radius-8 {
		// "100%" does not fit inside the ring on the compact canvas; the number
		// alone still reads as the completion figure.
		label = itoa(percent)
		tw, th, _ = font.SizeUTF8(label)
	}
	if int32(tw) > 2*radius-8 {
		// Still wider than the ring (a 24px "100" in the 22px ring the panel
		// header leaves room for): a caption size keeps it inside the stroke.
		if caption := FontForRole(config, TypeCaption); caption != nil {
			font = caption
			tw, th, _ = font.SizeUTF8(label)
		}
	}
	renderText(renderer, config, font, label, ColorTextSecondary(),
		cx-int32(tw)/2, cy-int32(th)/2)
}

const (
	// gameLibraryRowGap is the space between two game rows.
	gameLibraryRowGap = int32(6)
	// gameLibraryRowMin is the games column's design pitch, kept as the floor
	// so small type does not pack the rows tighter than the panel was drawn.
	gameLibraryRowMin = int32(58)
	// gameLibraryDetailSize is the design size of the "System · file" line
	// under a title: a step under the title, as secondary text.
	gameLibraryDetailSize = 18
)

// gameLibraryRows is the games column's geometry for the type in use.
type gameLibraryRows struct {
	rowH      int32 // pitch, gap included
	titleY    int32 // the title's offset inside a row
	detailY   int32 // the detail line's offset inside a row
	visible   int   // rows drawn
	showRange bool  // the "1-6 of 40" line is drawn under the rows
	rangeY    int32 // its offset from the column's top
}

// gameLibraryRowLayout measures the games column from the fonts it is drawn
// with. Every shipped config leaves "small" and "medium" at the 24px default,
// and the fixed 58px pitch the column had was 16px short of a 24px title over
// a 24px detail line: "PlayStation Portable · game.iso" ran into the next row,
// under its background and against its title. A list longer than the column
// also keeps a line free under its last row for the range, which used to be
// drawn across that row.
func gameLibraryRowLayout(bodyH, titleH, detailH, rangeH int32, total int) gameLibraryRows {
	const padTop, lineGap, padBottom = int32(6), int32(2), int32(6)
	content := titleH + lineGap + detailH
	l := gameLibraryRows{rowH: padTop + content + padBottom + gameLibraryRowGap}
	if l.rowH < gameLibraryRowMin {
		l.rowH = gameLibraryRowMin
	}
	// The two lines sit in the middle of the row's box, so the floor's spare
	// pixels split above and below instead of pooling under the detail line.
	l.titleY = (l.rowH - gameLibraryRowGap - content) / 2
	l.detailY = l.titleY + titleH + lineGap
	l.visible = int(bodyH / l.rowH)
	if total > l.visible {
		l.showRange = true
		l.rangeY = bodyH - rangeH
		l.visible = int((bodyH - rangeH - 4) / l.rowH)
	}
	if l.visible < 1 {
		l.visible = 1
	}
	return l
}

// librarySystemGlyph is the icon a console's button and page header carry:
// the system's own glyph (iconemu.go), and the library's extra rows' glyphs.
func librarySystemGlyph(name string) string {
	switch name {
	case continueSystemName:
		return "continue"
	case builtInLibrarySystem:
		return "gamepad"
	case androidLibrarySystem:
		return "phone"
	case html5LibrarySystem:
		return "globe"
	}
	return iconEmuGlyphFor(name)
}

// librarySystemCoverScan bounds the search for a console button's cover, so a
// big console without covers costs a few comparisons per frame, not a walk.
const librarySystemCoverScan = 64

// librarySystemCover is the cover a console's button shows: its first game
// with box art on the card, "" while none has any.
func librarySystemCover(system librarySystem) string {
	if system.WithArt == 0 {
		return ""
	}
	for i, game := range system.Games {
		if i >= librarySystemCoverScan {
			break
		}
		if game.HasArt && strings.TrimSpace(game.Art) != "" {
			return game.Art
		}
	}
	return ""
}

// gameLibraryTileMeta is the line under a console button's name.
func gameLibraryTileMeta(system librarySystem) string {
	n := len(system.Games)
	switch {
	case gameLib.query != "":
		return pluralizeCount(n, "match", "matches")
	case system.Name == continueSystemName:
		return pluralizeCount(n, "recent game", "recent games")
	}
	return pluralizeCount(n, "game", "games")
}

// gameLibrarySystemStatus is a console page's subtitle: its count and cover
// share, or the library's status line while a scan, search or tag filter is
// what the page is showing.
func gameLibrarySystemStatus(system librarySystem) string {
	if gameLib.scanning || gameLib.query != "" || gmetaFilterTag() != "" {
		return gameLibraryStatus()
	}
	status := gameLibraryTileMeta(system)
	if n := len(system.Games); n > 0 && system.Name != builtInLibrarySystem {
		status += " · " + itoa(coverPercent(system.WithArt, n)) + "% with covers"
	}
	status += " · sorted by " + strings.ToLower(librarySortLabel())
	if artworkFetchRunning() {
		done, total, _ := artworkFetchProgress()
		status += " · fetching " + itoa(done) + "/" + itoa(total)
	}
	return status
}

// gameLibraryGrid is the consoles grid's geometry for one body rectangle.
type gameLibraryGrid struct {
	cols, rows   int // rows is how many fit on screen at once
	tileW, tileH int32
	gap          int32
}

// gameLibraryGridLayout fits the console buttons to the page: two to five
// columns by width (four on the 1280-wide handhelds), and as many rows as fit
// at roughly Home's tile proportions, stretched so the rows on screen end
// exactly at the footer — the grid fills the page instead of floating in it.
func gameLibraryGridLayout(area sdl.Rect) gameLibraryGrid {
	g := gameLibraryGrid{gap: 16}
	if area.W < 800 {
		g.gap = 12
	}
	g.cols = int(area.W / 280)
	if g.cols < 2 {
		g.cols = 2
	}
	if g.cols > 5 {
		g.cols = 5
	}
	g.tileW = (area.W - int32(g.cols-1)*g.gap) / int32(g.cols)
	target := g.tileW * 55 / 100
	if target < 104 {
		target = 104
	}
	if target > 190 {
		target = 190
	}
	g.rows = int((area.H + g.gap + (target+g.gap)/2) / (target + g.gap))
	if g.rows < 1 {
		g.rows = 1
	}
	for {
		g.tileH = (area.H - int32(g.rows-1)*g.gap) / int32(g.rows)
		if g.rows == 1 || g.tileH >= 96 {
			break
		}
		g.rows--
	}
	return g
}

// renderGameLibraryGrid draws the first level: one glass button per console.
func renderGameLibraryGrid(renderer *sdl.Renderer, config *Config, systems []librarySystem, area sdl.Rect) {
	gameLib.systemHits = gameLib.systemHits[:0]
	grid := gameLibraryGridLayout(area)
	gameLib.gridCols = grid.cols
	if len(systems) == 0 {
		message := "No games yet"
		detail := "Put ROMs in Roms/<system> or HTML5 games in Games/HTML5/<game>, then press Y to rescan."
		if hidden := libraryHiddenCount(gameLib.lib); hidden > 0 {
			message = "All games are hidden"
			detail = "Game options → Show hidden reveals them again."
		}
		if gameLib.scanning {
			message = "Scanning the card…"
			detail = "The library opens as soon as the scan finishes."
		}
		if query := gameLib.query; query != "" {
			message = "No match for \"" + query + "\""
			detail = "Press S to change the search, or B to clear it and browse."
		} else if tag := gmetaFilterTag(); tag != "" {
			message = "No game is tagged " + tag
			detail = "Open a game (A) and choose Tags · B shows every game again."
		}
		UIEmptyState(renderer, config, area, "@gamepad", message, detail)
		return
	}

	totalRows := (len(systems) + grid.cols - 1) / grid.cols
	row := gameLib.system / grid.cols
	if row < gameLib.sysScroll {
		gameLib.sysScroll = row
	}
	if row >= gameLib.sysScroll+grid.rows {
		gameLib.sysScroll = row - grid.rows + 1
	}
	if gameLib.sysScroll > totalRows-grid.rows {
		gameLib.sysScroll = totalRows - grid.rows
	}
	if gameLib.sysScroll < 0 {
		gameLib.sysScroll = 0
	}
	first := gameLib.sysScroll * grid.cols
	last := first + grid.rows*grid.cols
	if last > len(systems) {
		last = len(systems)
	}

	// The buttons' covers load one at a time on the library's background
	// decoder, the focused button's first, then the rest on screen in order;
	// a button whose cover is not in the cache yet shows its bitmap name. No
	// frame waits on the card.
	wanted := ""
	if cover := librarySystemCover(systems[gameLib.system]); cover != "" {
		if _, cached := gameLib.artCache[cover]; !cached {
			wanted = cover
		}
	}
	for i := first; i < last && wanted == ""; i++ {
		if cover := librarySystemCover(systems[i]); cover != "" {
			if _, cached := gameLib.artCache[cover]; !cached {
				wanted = cover
			}
		}
	}
	// The pixel looks show no covers, so they load none.
	if wanted != "" && libraryConsoleLookMode() == libraryLookCovers {
		libraryArtTexture(renderer, wanted)
	}

	// Dim unselected art (homelook.go): every cover but the focused button's
	// is greyed, easing as the focus moves.
	dimOn := homeDimArtOn(config)
	if dimOn {
		gameLibDimEase.note("grid", 0, gameLib.system, uiMotionEnabled(config))
	}
	for i := first; i < last; i++ {
		col, line := (i-first)%grid.cols, (i-first)/grid.cols
		rect := sdl.Rect{
			X: area.X + int32(col)*(grid.tileW+grid.gap),
			Y: area.Y + int32(line)*(grid.tileH+grid.gap),
			W: grid.tileW, H: grid.tileH,
		}
		gameLib.systemHits = append(gameLib.systemHits, gameLibRowHit{index: i, rect: rect})
		dim := 0.0
		if dimOn {
			dim = gameLibDimEase.dim(i)
		}
		// In the saved Console look (game_library_pixel_look.go).
		drawGameLibraryConsoleButton(renderer, config, systems[i], rect, i == gameLib.system, dim)
	}
	// A pixel look's focused name flies in over every button.
	drawGameLibraryPixelFlight(renderer, area)
	// Rows above or below this screenful: the edge rows fade into the page
	// (listfade.go), unless the focused button is on that row.
	if totalRows > grid.rows {
		shown := sdl.Rect{X: area.X, Y: area.Y, W: area.W, H: int32(grid.rows)*(grid.tileH+grid.gap) - grid.gap}
		size := listFadeSize(shown.H)
		near, far := int32(0), int32(0)
		if gameLib.sysScroll > 0 && row != gameLib.sysScroll {
			near = size
		}
		if gameLib.sysScroll+grid.rows < totalRows && row != gameLib.sysScroll+grid.rows-1 {
			far = size
		}
		drawListEdgeFadeSized(renderer, shown, near, far)
	}

	// More rows than the page holds: a slim bar in the right margin says
	// where this screenful sits.
	if totalRows > grid.rows {
		trackX := area.X + area.W + (screenWidth-area.X-area.W)/2 - 2
		trackH := area.H
		fillRoundedRect(renderer, trackX, area.Y, 4, trackH, 2, homeGlass(24))
		thumbH := trackH * int32(grid.rows) / int32(totalRows)
		if thumbH < 24 {
			thumbH = 24
		}
		thumbY := area.Y + (trackH-thumbH)*int32(gameLib.sysScroll)/int32(totalRows-grid.rows)
		fillRoundedRect(renderer, trackX, thumbY, 4, thumbH, 2, WithAlpha(glassAccent(), 220))
	}
}

// drawGameLibrarySystemTile paints one console button in Home's tile language:
// the glass card with Home's glow and ring when focused, the system's glyph
// in an icon badge top-left, the name anchored bottom-left over its game count
// and cover meter, and a cover slot along the right edge. dim greys the cover
// (Dim unselected art, homelook.go).
func drawGameLibrarySystemTile(renderer *sdl.Renderer, config *Config, system librarySystem, rect sdl.Rect, focused bool, dim float64) {
	if focused {
		drawHomeFocusGlow(renderer, rect)
	}
	drawGlassTile(renderer, rect, rect, focused, false)
	pad := rect.H / 9
	if pad < 10 {
		pad = 10
	}
	if pad > 18 {
		pad = 18
	}
	accent := glassAccent()

	textX := rect.X + pad
	textRight := rect.X + rect.W - pad
	slotH := rect.H - 2*pad
	if slotW := slotH * 3 / 4; rect.W-slotW-3*pad >= 120 {
		slot := sdl.Rect{X: rect.X + rect.W - pad - slotW, Y: rect.Y + pad, W: slotW, H: slotH}
		drawGameLibraryTileCover(renderer, config, system, slot, focused, dim)
		textRight = slot.X - pad
	}
	textW := textRight - textX

	// A step under Home's 48px badge on a 160px button, so a long console
	// name keeps room for two lines under it.
	badge := rect.H * 26 / 100
	if badge < 28 {
		badge = 28
	}
	if badge > homeIconSize {
		badge = homeIconSize
	}
	glyphBox := drawIconBadge(renderer, sdl.Rect{X: textX, Y: rect.Y + pad, W: badge, H: badge}, false)
	ink := ColorTextPrimary()
	if focused {
		ink = accent
	}
	drawHomeGlyphTinted(renderer, librarySystemGlyph(system.Name), glyphBox, ink)

	// Bottom up: the cover meter, the count, then the name over them.
	bottom := rect.Y + rect.H - pad
	if n := len(system.Games); n > 0 && system.Name != continueSystemName && system.Name != builtInLibrarySystem {
		fillRoundedRect(renderer, textX, bottom-3, textW, 3, 1, homeGlass(40))
		if filled := textW * int32(coverPercent(system.WithArt, n)) / 100; filled > 0 {
			fillRoundedRect(renderer, textX, bottom-3, filled, 3, 1, WithAlpha(accent, 220))
		}
		bottom -= 3 + 8
	}
	if metaFont := FontForRole(config, TypeLabel); metaFont != nil {
		bottom -= int32(metaFont.Height())
		drawGlassText(renderer, config, metaFont, ellipsize(metaFont, gameLibraryTileMeta(system), textW),
			ColorTextMuted(), textX, bottom)
		bottom -= 2
	}
	nameTop := rect.Y + pad + badge + 4
	font, lineH, lines := glassTileLabel(config, consoleDisplayName(system.Name), textW, bottom-nameTop, 20)
	if font == nil || len(lines) == 0 {
		// Too little room under the badge for a line (large text, a small
		// canvas): the name still shows, on one line over the badge's corner,
		// rather than leaving a button nobody can read.
		font = FontForRole(config, TypeCaption)
		if font == nil {
			return
		}
		lineH, lines = int32(font.Height()), []string{ellipsize(font, consoleDisplayName(system.Name), textW)}
	}
	y := bottom - int32(len(lines))*lineH
	for _, line := range lines {
		drawGlassText(renderer, config, font, line, ColorTextPrimary(), textX, y)
		y += lineH
	}
}

// drawGameLibraryTileCover fills a console button's cover slot: the first
// cover the console has, once the decoder has it in the cache, else the
// console's name in the library's bitmap lettering — assembling square by
// square on the focused button.
func drawGameLibraryTileCover(renderer *sdl.Renderer, config *Config, system librarySystem, slot sdl.Rect, focused bool, dim float64) {
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, slot.X, slot.Y, slot.W, slot.H, homeIconRadius, homeGlass(12))
	strokeRoundedRect(renderer, slot.X, slot.Y, slot.W, slot.H, homeIconRadius, 1, homeGlass(18))
	if cover := librarySystemCover(system); cover != "" {
		if tex := gameLib.artCache[cover]; tex != nil {
			if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
				dst := gaFit(tw, th, slot.W-8, slot.H-8)
				dst.X += slot.X + 4
				dst.Y += slot.Y + 4
				homeLookCopyDim(renderer, tex, nil, dst, dim, 255)
				return
			}
		}
	}
	if focused && gameLib.logoSystem != system.Name {
		gameLib.logoSystem = system.Name
		gameLib.logoAt = sdl.GetTicks64()
	}
	animate := focused && !effectiveReducedMotion(config)
	elapsed := uint64(470)
	if animate {
		elapsed = sdl.GetTicks64() - gameLib.logoAt
	}
	color := WithAlpha(ColorTextPrimary(), 150)
	if focused {
		color = glassAccent()
	}
	drawConsolePixelLogo(renderer, system.Name, slot, 4, color, elapsed, animate)
}

// gameLibraryFooterHints is the footer for the level on screen.
func gameLibraryFooterHints(config *Config) []FooterHint {
	if gameLib.lib.Total == 0 && len(gameLib.recents) == 0 {
		return []FooterHint{{"Y", "Rescan"}, {"B", "Close"}}
	}
	artHint := FooterHint{"L1 / F", "Fill Gaps"}
	if artworkFetchRunning() {
		artHint = FooterHint{"L1 / F", "Stop"}
	}
	if gameLib.focus == 1 {
		// B second: the planner drops hints from the end on a narrow
		// footer, and the way back must be the last to go.
		hints := []FooterHint{{"A", "Open"}, {"B", "Consoles"}}
		// The Cube's Left/Right change console (game_library_cube.go).
		hints = append(hints, libraryLayoutPageHints()...)
		// Select's menu holds the random pick and the tag filter, which no
		// button of their own reaches on every handheld (game_library_menu.go).
		hints = append(hints, FooterHint{"Select / M", "Menu"})
		// X is only advertised while LAN Play is on: a footer that offers a
		// switched-off feature teaches a button that answers with a toast.
		if netplayEnabled(config) {
			hints = append(hints, FooterHint{"X", "LAN Play"})
		}
		hints = append(hints, FooterHint{"Start / O", "Options"})
		// L2 / R2 jump by letter (game_library_scrub.go).
		if scrub, ok := gameLibraryScrubHint(); ok {
			hints = append(hints, scrub)
		}
		return append(hints, FooterHint{"S / R1", "Search"}, artHint, FooterHint{"Y", "Rescan"})
	}
	back := FooterHint{"B", "Close"}
	if gameLib.query != "" || gmetaFilterTag() != "" {
		back = FooterHint{"B", "Show All"}
	}
	// X steps the Console look (game_library_pixel_look.go).
	return []FooterHint{{"A", "Open"}, back, {"Select / M", "Menu"}, {"S / R1", "Search"},
		{"X", "Look"}, artHint, {"Y", "Rescan"}}
}

// renderGameLibraryPanel draws the library page: Home's backdrop under the top
// bar, the page header, the level on screen (the consoles grid or one
// console's games) and the footer band.
func renderGameLibraryPanel(renderer *sdl.Renderer, config *Config) {
	if !gameLib.open || renderer == nil {
		return
	}
	// A fetch that finished after the last frame turned placeholder tiles into
	// real covers. The textures must be released on the render thread, so the
	// reload happens here rather than in the downloader.
	if gen := artworkFetchGeneration(); gen != gameLib.artSeen {
		closeGameLibraryArt()
		recountLibraryArtwork()
		gameLib.artSeen = gen
		// The counters move only here, after a completed or stopped fetch, so a
		// long download does not appear to do nothing.
		ShowToast(artworkFetchSummary(), ToastKindInfo)
	}
	if gameLib.artCache == nil {
		gameLib.resetArtCache()
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	smallH := int32(font.Height())

	// The library is a page, not a card over a dimmed scene: everything under
	// the top bar is repainted with Home's backdrop, so the bar (clock,
	// battery, bell) stays and the library fills the rest of the screen.
	top := headerBarHeight()
	drawAppBackdropBand(renderer, config, sdl.Rect{X: 0, Y: top, W: screenWidth, H: screenHeight - top})

	systems := currentGameSystems()
	if gameLib.system >= len(systems) {
		gameLib.system = len(systems) - 1
	}
	if gameLib.system < 0 {
		gameLib.system = 0
	}
	system, hasSystem := selectedLibrarySystem()
	if !hasSystem {
		gameLib.focus = 0
	}
	// A console's background, and the Background art style's picture (artstyle.go).
	artStyleDrawLibraryBackdrop(renderer, config, system, hasSystem && gameLib.focus == 1, sdl.Rect{X: 0, Y: top, W: screenWidth, H: screenHeight - top})

	margin := scaleX(homeSideMargin, screenWidth)
	innerW := screenWidth - 2*margin
	header := UIPanelHeader("Games", gameLibraryStatus(), "gamepad")
	if gameLib.focus == 1 {
		header = UIPanelHeader(consoleDisplayName(system.Name), gameLibrarySystemStatus(system), librarySystemGlyph(system.Name))
	}
	header.X, header.Y, header.W = margin, top+6, innerW
	band := uiPageHeaderMetricsFor(config, header)
	UIPageHeaderDraw(renderer, config, header)
	// The header's glyph and title are a back button for the mouse: a
	// console's page goes to the grid, the grid closes (or drops a filter).
	gameLib.backRect = sdl.Rect{X: header.X, Y: header.Y, W: innerW / 2, H: band.Height}

	// The active search sits on the title line so it is visible at a glance
	// instead of being hidden in the subdued status line underneath.
	const ringReserve = int32(96)
	if query := gameLib.query; query != "" {
		label := ellipsize(font, "Search: "+query, innerW/2-ringReserve)
		w, _, _ := font.SizeUTF8(label)
		renderText(renderer, config, font, label, accentColor,
			margin+innerW-ringReserve-int32(w), header.Y+(band.WellBox-smallH)/2)
	}
	// Cover completeness at a glance: a ring that fills as the library (or, on
	// a console's page, that console) gains art.
	withArt, coverTotal := libraryCoverTotals(gameLib.lib)
	if gameLib.focus == 1 {
		withArt, coverTotal = system.WithArt, len(system.Games)
		if system.Name == builtInLibrarySystem {
			coverTotal = 0
		}
	}
	if coverTotal > 0 {
		ringR := int32(26)
		if half := band.WellBox / 2; half > 8 && half < ringR {
			ringR = half
		}
		drawCoverCompletenessRing(renderer, config,
			margin+innerW-ringR, header.Y+band.WellBox/2, ringR, coverPercent(withArt, coverTotal))
	}

	footer := sceneFooterBand()
	bodyY := header.Y + band.Height + 16
	body := sdl.Rect{X: margin, Y: bodyY, W: innerW, H: footer.Y - 14 - bodyY}
	if gameLib.focus == 0 {
		// No game is in view on the grid, so no snap plays and no preview is
		// a click target.
		snapPreviewStop()
		gameLib.gameHits = gameLib.gameHits[:0]
		gameLib.artRect = sdl.Rect{}
		renderGameLibraryGrid(renderer, config, systems, body)
	} else {
		gameLib.systemHits = gameLib.systemHits[:0]
		// Back on the grid, however it gets there, the focused console's
		// letters fly in again (game_library_pixel_look.go).
		gameLib.logoSystem = ""
		renderGameLibraryGames(renderer, config, system, body)
		// The letter a jump landed on, over the games (game_library_scrub.go).
		drawGameLibraryScrubBubble(renderer, config, gameLib.listRect)
	}
	narrateGameLibrary(systems, system, hasSystem) // spoken navigation (narrate_hooks.go)
	gameWarmNoteLibrary()                          // the highlighted game's files warmed up (gamewarm.go)

	peerDrawFooter(renderer, config, gameLibraryFooterHints(config))
	// A game's page, its tag sheet and the tag filter's chooser sit on the
	// library, under the random and options sheets they can open.
	gmetaRender(renderer, config)
	renderGameLibMenu(renderer, config)
	renderGameRandomPicker(renderer, config)
	renderGameOptionsPanel(renderer, config)
}

// renderGameLibraryGames draws a console's page: its games in the chosen
// layout on the left and the focused game's box art (or video snap) large on
// the right.
func renderGameLibraryGames(renderer *sdl.Renderer, config *Config, system librarySystem, body sdl.Rect) {
	font, _ := getCachedFont(config, "small")
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	// The preview takes about a third of the page, square, and is dropped on a
	// narrow canvas rather than squeezing the list.
	const gap = int32(24)
	artW := body.W * 34 / 100
	if artW > body.H-70 {
		artW = body.H - 70
	}
	if body.W < 760 || artW < 120 {
		artW = 0
	}
	artW = artStylePreviewWidth(body, artW) // Game art off, Background style, Art width (artstyle.go)
	gameW := body.W - artW - gap
	if artW == 0 || gameW < 280 || libraryLayoutWide(libraryLayoutMode()) {
		// Cover Flow and the Cube draw the big cover themselves.
		gameW, artW = body.W, 0
	}
	gameX := body.X
	artX := gameX + gameW + gap
	bodyY, bodyH := body.Y, body.H
	gameLib.listRect = sdl.Rect{X: gameX, Y: bodyY, W: gameW, H: bodyH}

	// The "System · file" line under each title is secondary, so it is set a
	// step smaller than the title; the row is measured from both fonts.
	detailFont := getFontSized(config, "small", gameLibraryDetailSize)
	if detailFont == nil {
		detailFont = font
	}
	rows := gameLibraryRowLayout(bodyH, medH, int32(detailFont.Height()), smallH, len(system.Games))
	gameRowH := rows.rowH
	visibleGames := rows.visible
	if len(system.Games) > 0 {
		if gameLib.cursor >= len(system.Games) {
			gameLib.cursor = len(system.Games) - 1
		}
		if gameLib.cursor < 0 {
			gameLib.cursor = 0
		}
		if gameLib.cursor < gameLib.scroll {
			gameLib.scroll = gameLib.cursor
		}
		if gameLib.cursor >= gameLib.scroll+visibleGames {
			gameLib.scroll = gameLib.cursor - visibleGames + 1
		}
		if gameLib.scroll > len(system.Games)-visibleGames {
			gameLib.scroll = len(system.Games) - visibleGames
		}
		if gameLib.scroll < 0 {
			gameLib.scroll = 0
		}
	} else {
		gameLib.scroll = 0
	}
	gameLib.gameHits = gameLib.gameHits[:0]
	if len(system.Games) == 0 {
		message := "No games yet"
		detail := "Put ROMs in Roms/<system> or HTML5 games in Games/HTML5/<game>, then press Y to rescan."
		if hidden := libraryHiddenCount(gameLib.lib); hidden > 0 {
			message = "All games are hidden"
			detail = "Game options → Show hidden reveals them again."
		}
		if system.Name == continueSystemName {
			detail = "Launch a game and it appears here for one-press resume."
		}
		UIEmptyState(renderer, config, sdl.Rect{X: gameX, Y: bodyY, W: gameW, H: bodyH - 8},
			"@gamepad", message, detail)
	} else if layout := libraryLayoutMode(); layout != libraryLayoutList {
		renderGameCoverBrowser(renderer, config, system, gameX, bodyY, gameW, bodyH, layout)
	} else {
		for i := 0; i < visibleGames; i++ {
			idx := gameLib.scroll + i
			if idx >= len(system.Games) {
				break
			}
			rect := sdl.Rect{X: gameX, Y: bodyY + int32(i)*gameRowH, W: gameW, H: gameRowH - gameLibraryRowGap}
			gameLib.gameHits = append(gameLib.gameHits, gameLibRowHit{index: idx, rect: rect})
			// Glass rows, Home's ring on the focused one; NoLift keeps the
			// ring inside the gap between rows.
			drawGlassSurface(renderer, rect, 12, glassState{Focused: idx == gameLib.cursor, NoLift: true})
			game := system.Games[idx]
			// The focused row's long title scrolls (titlescroll.go).
			titleScrollText(renderer, config, medium, game.Label(), ColorTextPrimary(),
				rect.X+14, rect.Y+rows.titleY, rect.W-28, idx == gameLib.cursor && gameLib.focus == 1)
			detail := consoleDisplayName(game.System) + " · " + filepath.Base(game.Path)
			if builtIn, ok := builtInLibraryGameFor(game); ok {
				detail = builtIn.detail
			}
			if game.Hidden {
				detail += " · Hidden"
			}
			// A per-game clock is worth seeing in the list: it explains why
			// this title runs cooler than the one above it.
			if power := gameOptionsPowerSummary(gameOptionsFor(game.Path)); power != "" {
				detail += " · " + power
			}
			// A network drive's game says where it is, or Offline (nas_index.go).
			detail += nasLibraryRowNote(game.Path)
			renderText(renderer, config, detailFont, ellipsize(detailFont, detail, rect.W-28),
				ColorTextTertiary(), rect.X+14, rect.Y+rows.detailY)
			nasLibraryDimRow(renderer, rect, game.Path)
		}
		// Games above or below the rows on screen: the edge rows fade into
		// the page (listfade.go), unless the cursor is on that row.
		if shown := min(visibleGames, len(system.Games)-gameLib.scroll); shown > 0 {
			area := sdl.Rect{X: gameX, Y: bodyY, W: gameW, H: int32(shown)*gameRowH - gameLibraryRowGap}
			size := listFadeSize(area.H)
			near, far := int32(0), int32(0)
			if gameLib.scroll > 0 && gameLib.cursor != gameLib.scroll {
				near = size
			}
			if gameLib.scroll+visibleGames < len(system.Games) && gameLib.cursor != gameLib.scroll+visibleGames-1 {
				far = size
			}
			drawListEdgeFadeSized(renderer, area, near, far)
		}
		if gameLib.scroll > 0 || gameLib.scroll+visibleGames < len(system.Games) {
			rng := itoa(gameLib.scroll+1) + "-" + itoa(gameLib.scroll+visibleGames) + " of " + itoa(len(system.Games))
			renderText(renderer, config, font, rng, ColorTextTertiary(),
				gameX, bodyY+bodyH-smallH)
		}
	}

	// ── Box art / video snap preview ────────────────────────────────
	// One call per frame decides whether the focused game's snap should run,
	// so every way the focus can move (pad, mouse, search, rescan) is covered by
	// the same rule instead of each path remembering to arm it.
	if artStyleHidesPreview() {
		snapPreviewStop() // Game art off / Background style: no preview to play it in (artstyle.go)
	} else {
		snapPreviewSync(time.Now())
	}
	if artW <= 0 {
		gameLib.artRect = sdl.Rect{}
		return
	}
	preview := libraryGame{}
	if len(system.Games) > 0 {
		preview = system.Games[gameLib.cursor]
	}
	rect := sdl.Rect{X: artX, Y: bodyY, W: artW, H: artStylePreviewHeight(artW, bodyH)} // square unless Art width is wider (artstyle.go)
	gameLib.artRect = rect
	drawLibraryArt(renderer, config, rect, preview)
	if preview.Label() != "" {
		label := ellipsize(medium, preview.Label(), artW)
		renderText(renderer, config, medium, label, ColorTextPrimary(), artX, bodyY+rect.H+12)
		// Fitted like the title: "PlayStation Portable" alone is wider than a
		// narrow preview column.
		renderText(renderer, config, detailFont, ellipsize(detailFont, consoleDisplayName(preview.System), artW), ColorTextTertiary(),
			artX, bodyY+rect.H+12+medH+2)
	}
}
