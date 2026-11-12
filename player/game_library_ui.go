package main

// game_library_ui.go — the modal library browser.
//
// Three columns: consoles (with a leading Continue row built from the recents
// file), the games of the selected console, and a box-art preview. A launches,
// X opens LAN play (netplay_ui.go), E hands the selected game to its tool
// (handoff.go: profiles, compatibility or the emulator page), L1 fetches
// missing covers, Y rescans after copying new files onto the card, B closes.
// It is a modal overlay like the Emulators panel, so it works on every
// config, including the desktop one with no Roms folder.

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
	open    bool
	lib     gameLibraryReport
	recents []recentGame
	system  int
	cursor  int
	scroll  int
	focus   int // 0 = console column, 1 = games column
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
	panel        sdl.Rect

	systemHits []gameLibRowHit
	gameHits   []gameLibRowHit
	// artRect is the preview frame, filled by the renderer for click tests.
	artRect sdl.Rect
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
	// Continue row unless "Show hidden" is on. The tag filter (L3 / T,
	// gmeta_ui.go) narrows what the search leaves; it returns the list untouched
	// when no tag is chosen.
	systems := filterHiddenLibrarySystems(librarySystemsForUI(gameLib.lib, gameLib.recents), key.showHidden)
	systems = gmetaFilterSystems(filterLibrarySystems(systems, gameLib.query))
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
		consoleMatch := strings.Contains(normalizeGameSearch(system.Name), needle)
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
	if query == "" {
		gameLib.focus = 0
		return
	}
	// Typing a search is a request to look at the results, so the games column
	// takes the focus when there is anything in it.
	if system, ok := selectedLibrarySystem(); ok && len(system.Games) > 0 {
		gameLib.focus = 1
	}
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

func scanGameLibrary() gameLibraryReport {
	return buildGameLibrary(scanROMLibrary(romLibraryRoots()), libraryArtworkRoot())
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
	gameLib.query = ""
	resetGameRandomPicker()
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

func closeGameLibraryPanel() {
	if !gameLib.open {
		return
	}
	// The options and random sheets cannot outlive the library they belong to.
	gameOptsUI.open = false
	gameOptsUI.rowHits = nil
	resetGameRandomPicker()
	// So can a game's page, its tag sheet and the tag filter's chooser; the
	// page's screenshots are textures and go with it.
	gmetaCloseAll()
	gameLib.open = false
	// A scan that finishes after this must not write into a closed panel or a
	// later visit that has already started its own scan.
	gameLib.scanGen++
	gameLib.scanning = false
	invalidateGameLibrarySystemsCache()
	closeGameLibraryArt()
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
	artRoot := libraryArtworkRoot()
	// Copy the playtime map on the render thread; the background build must not
	// read it while the ticker is writing it.
	seconds := libraryPlaytimeSnapshot()
	go func() {
		report := scanROMLibrary(roots)
		lib := buildGameLibraryWith(report, artRoot, seconds)
		// The APK Store's Android games join as one more console (AND-16).
		libraryAddAndroidGames(&lib, seconds)
		tbxPost(func() {
			if !applyGameLibraryScan(&gameLib, gen, lib, recentGames()) {
				return
			}
			clampGameLibrarySelection()
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
		return "Showing " + tag + " · " + pluralizeCount(games, "game", "games") + " — B shows every game"
	}
	if query := gameLib.query; query != "" {
		games, systems := 0, 0
		for _, system := range currentGameSystems() {
			systems++
			games += len(system.Games)
		}
		if games == 0 {
			return "No match for \"" + query + "\" — press S to change it"
		}
		return pluralizeCount(games, "match", "matches") + " for \"" + query + "\" in " + pluralizeCount(systems, "console", "consoles")
	}
	if gameLib.lib.Total == 0 {
		if len(gameLib.recents) > 0 {
			return pluralizeCount(len(gameLib.recents), "recent game", "recent games") + " — no game folders found, Y rescans"
		}
		return "No games yet — copy them to the card's Roms folder, then press Y"
	}
	if hidden := libraryHiddenCount(gameLib.lib); hidden > 0 && len(currentGameSystems()) == 0 {
		return pluralizeCount(hidden, "hidden game", "hidden games") + " — Game options → Show hidden reveals them"
	}
	status := pluralizeCount(gameLib.lib.Total, "game", "games") + " in " + pluralizeCount(len(gameLib.lib.Systems), "console", "consoles")
	status += " · " + itoa(coverPercent(gameLib.lib.WithArt, gameLib.lib.Total)) + "% with covers"
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

// gameLibraryMove handles D-pad navigation between the columns.
func gameLibraryMove(dx, dy int) {
	systems := currentGameSystems()
	if len(systems) == 0 {
		return
	}
	if gameLib.focus == 0 {
		if dx > 0 {
			gameLib.focus = 1
			PlayNavSound()
			return
		}
		if dy != 0 {
			gameLib.system += dy
			if gameLib.system < 0 {
				gameLib.system = len(systems) - 1
			}
			if gameLib.system >= len(systems) {
				gameLib.system = 0
			}
			gameLib.cursor = 0
			gameLib.scroll = 0
			PlayNavSound()
		}
		return
	}
	if dx < 0 {
		gameLib.focus = 0
		PlayNavSound()
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
	report := scanROMLibrary(romLibraryRoots())
	missing := romsMissingArtwork(report.Games, artRoot)
	if len(missing) == 0 {
		ShowToast("Every scanned game already has box art.", ToastKindSuccess)
		return
	}
	if !startLibraryArtworkFetch(missing) {
		ShowToast("An artwork fetch is already running.", ToastKindInfo)
		return
	}
	ShowToast("Fetching "+pluralizeCount(len(missing), "missing cover", "missing covers")+" — L1 / F stops and resumes", ToastKindInfo)
}

// recountLibraryArtwork refreshes the HasArt flags and the completeness counts
// after a fetch. The card is not rescanned: only the stat calls change, and
// they run once per completed fetch rather than on every frame.
func recountLibraryArtwork() {
	total, with := 0, 0
	for i := range gameLib.lib.Systems {
		system := &gameLib.lib.Systems[i]
		for j := range system.Games {
			system.Games[j].HasArt = artworkCoverPresent(system.Games[j].Art)
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
	case sdl.K_RETURN, sdl.K_SPACE:
		// A game opens its page (box art, details, tags, save states) with
		// Play focused; a console opens its list.
		gmetaOpenSelected()
	case sdl.K_t:
		gmetaOpenFilterPicker()
	case sdl.K_e:
		if game, ok := selectedLibraryGame(); ok {
			openGameLibraryHandoff(game)
		}
	case sdl.K_o:
		openGameOptions()
	case sdl.K_r:
		openGameRandomPicker()
	case sdl.K_x:
		libraryOpenNetplay()
	case sdl.K_f:
		fetchMissingLibraryArtwork()
	case sdl.K_y:
		refreshGameLibrary()
		ShowToast(gameLibraryStatus(), ToastKindInfo)
	case sdl.K_s, sdl.K_SLASH:
		vkOpenGameSearch()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		if gameLib.query != "" {
			// The first Back clears the search instead of leaving the library:
			// a filtered list looks like a broken library otherwise.
			setGameLibraryQuery("")
			return true
		}
		// The same for a tag filter.
		if gmetaClearFilter() {
			return true
		}
		closeGameLibraryPanel()
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
	case sdl.CONTROLLER_BUTTON_LEFTSTICK:
		// L3 chooses a tag to show (Favorites, Backlog …); the stick click is
		// free here, like R3 for the random pick.
		gmetaOpenFilterPicker()
	case sdl.CONTROLLER_BUTTON_X:
		libraryOpenNetplay()
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
	case sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		// R3 rolls a random game. The stick click has no other binding in the
		// app, and a key that is not on the pad would leave handhelds without
		// the picker.
		openGameRandomPicker()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		if gameLib.query != "" {
			setGameLibraryQuery("")
			return true
		}
		if gmetaClearFilter() {
			return true
		}
		closeGameLibraryPanel()
	default:
		return false
	}
	return true
}

// handleGameLibraryMouse selects a console, launches a game, or closes the
// panel when the click lands outside it.
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
	if gmetaSheetOpen() {
		return gmetaHandleMouse(mx, my, config)
	}
	for _, hit := range gameLib.systemHits {
		if pointInRect(mx, my, hit.rect) {
			gameLib.focus = 0
			gameLib.system = hit.index
			gameLib.cursor = 0
			gameLib.scroll = 0
			return true
		}
	}
	for _, hit := range gameLib.gameHits {
		if pointInRect(mx, my, hit.rect) {
			gameLib.focus = 1
			gameLib.cursor = hit.index
			gmetaOpenSelected()
			return true
		}
	}
	if gameLib.panel.W > 0 && pointInRect(mx, my, gameLib.panel) {
		return true
	}
	closeGameLibraryPanel()
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
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, err
	}
	if !libraryArtDimensionsAllowed(config.Width, config.Height) {
		return nil, errLibraryArtDimensions
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	picture, _, err := image.Decode(file)
	if err != nil {
		return nil, err
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

	tex := libraryArtTexture(renderer, game.Art)
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
			renderer.Copy(tex, nil, &dst)
			return
		}
	}

	// Placeholder: initials on an accent tint.
	font, _ := getCachedFont(config, "large")
	if font == nil {
		return
	}
	label := strings.TrimSpace(game.Title)
	if label == "" {
		label = "?"
	}
	runes := []rune(label)
	initials := strings.ToUpper(string(runes[0]))
	if len(runes) > 1 {
		if idx := strings.IndexRune(label, ' '); idx >= 0 && idx+1 < len(runes) {
			initials += strings.ToUpper(string(runes[idx+1]))
		}
	}
	tw, th, _ := font.SizeUTF8(initials)
	renderText(renderer, config, font, initials,
		WithAlpha(installHighlight(), 230),
		rect.X+(rect.W-int32(tw))/2, rect.Y+(rect.H-int32(th))/2)
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

// gameLibrarySystemRowHeight is a console row's pitch for its name's line
// height: the name centred above the cover meter along the row's bottom edge,
// never under the 46px design height.
func gameLibrarySystemRowHeight(nameH int32) int32 {
	if h := nameH + 16; h > 46 {
		return h
	}
	return 46
}

// renderGameLibraryPanel draws the three-column browser.
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
	medium, _ := getCachedFont(config, "medium")
	if font == nil {
		return
	}
	if medium == nil {
		medium = font
	}
	smallH := int32(font.Height())
	medH := int32(medium.Height())

	const pad = OverlayPadX
	// Same header as every other panel: the shared page header, measured before
	// the columns are placed so the list starts under the bar rather than under
	// an assumption about it.
	header := UIPanelHeader("Games", gameLibraryStatus(), "gamepad")
	headerH := UIPanelHeaderBand(config, header, 14)
	footerH := int32(16) + smallH
	panelW := int32(1120)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	panelH := screenHeight - 56
	if panelH < 360 {
		panelH = screenHeight - 16
	}
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	gameLib.panel = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	drawOverlayCardAccent(renderer, panelX, panelY, panelW)

	header = overlayHeaderOnCard(header, sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH})
	band := uiPageHeaderMetricsFor(config, header)
	UIPageHeaderDraw(renderer, config, header)

	// The active search sits on the title line so it is visible at a glance
	// instead of being hidden in the subdued status line underneath. The line is
	// measured from the band, so the label stays centred on the title at any
	// accessibility size.
	if query := gameLib.query; query != "" {
		label := ellipsize(font, "Search: "+query, panelW/2)
		w, _, _ := font.SizeUTF8(label)
		const ringReserve = int32(96)
		renderText(renderer, config, font, label, accentColor,
			panelX+panelW-pad-ringReserve-int32(w), header.Y+(band.WellBox-smallH)/2)
	}

	// Cover completeness at a glance: a ring that fills as the library gains
	// art, with the percentage inside it.
	if gameLib.lib.Total > 0 {
		// The ring stays on the title line: at 26px it reached down into the
		// status line under it, whose long form runs the width of the card.
		ringR := int32(26)
		if half := band.WellBox / 2; half > 8 && half < ringR {
			ringR = half
		}
		drawCoverCompletenessRing(renderer, config,
			panelX+panelW-pad-ringR, header.Y+band.WellBox/2, ringR,
			coverPercent(gameLib.lib.WithArt, gameLib.lib.Total))
	}

	systems := currentGameSystems()
	if gameLib.system >= len(systems) {
		gameLib.system = len(systems) - 1
	}
	if gameLib.system < 0 {
		gameLib.system = 0
	}

	bodyY := panelY + headerH
	bodyH := panelH - headerH - footerH
	innerW := panelW - 2*pad

	artW := int32(250)
	if innerW < 760 {
		artW = 0 // narrow screens drop the preview instead of squeezing the list
	}
	sysW := innerW / 4
	if sysW < 190 {
		sysW = 190
	}
	if sysW > 300 {
		sysW = 300
	}
	gap := int32(16)
	gameW := innerW - sysW - artW - 2*gap
	if gameW < 200 {
		gameW = innerW - sysW - gap
		artW = 0
	}
	sysX := panelX + pad
	gameX := sysX + sysW + gap
	artX := gameX + gameW + gap

	focusColor := installHighlight()

	// ── Consoles ────────────────────────────────────────────────────────
	sysRowH := gameLibrarySystemRowHeight(medH)
	visibleSys := int(bodyH / sysRowH)
	if visibleSys < 1 {
		visibleSys = 1
	}
	gameLib.systemHits = gameLib.systemHits[:0]
	startSys := 0
	if gameLib.system >= visibleSys {
		startSys = gameLib.system - visibleSys + 1
	}
	for i := 0; i < visibleSys; i++ {
		idx := startSys + i
		if idx >= len(systems) {
			break
		}
		rect := sdl.Rect{X: sysX, Y: bodyY + int32(i)*sysRowH, W: sysW, H: sysRowH - 6}
		gameLib.systemHits = append(gameLib.systemHits, gameLibRowHit{index: idx, rect: rect})
		selected := idx == gameLib.system
		bg := WithAlpha(ColorSurfaceRow, 170)
		if selected {
			bg = WithAlpha(focusColor, 52)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
		if selected && gameLib.focus == 0 {
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, focusColor)
		}
		name := systems[idx].Name
		count := itoa(len(systems[idx].Games))
		cw, _, _ := font.SizeUTF8(count)
		renderText(renderer, config, medium, ellipsize(medium, name, rect.W-24-int32(cw)-12),
			ColorTextPrimary(), rect.X+12, rect.Y+(rect.H-medH)/2)
		renderText(renderer, config, font, count, ColorTextTertiary(),
			rect.X+rect.W-12-int32(cw), rect.Y+(rect.H-smallH)/2)
		// A thin meter under each console row: the share of that console's
		// covers that are already on the card.
		if games := len(systems[idx].Games); games > 0 {
			meterW := rect.W - 24
			meterY := rect.Y + rect.H - 5
			fillRoundedRect(renderer, rect.X+12, meterY, meterW, 3, 1, WithAlpha(ColorBorderDefault, 130))
			if filled := meterW * int32(coverPercent(systems[idx].WithArt, games)) / 100; filled > 0 {
				fillRoundedRect(renderer, rect.X+12, meterY, filled, 3, 1, WithAlpha(focusColor, 200))
			}
		}
	}

	// ── Games ───────────────────────────────────────────────────────────
	system, hasSystem := selectedLibrarySystem()
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
	if !hasSystem || len(system.Games) == 0 {
		message := "No games yet"
		detail := "Put games in the Roms folder on the card, one folder per console (Roms/GBA, Roms/PS), then press Y to rescan."
		if hidden := libraryHiddenCount(gameLib.lib); hidden > 0 {
			message = "All games are hidden"
			detail = "Game options → Show hidden reveals them again."
		}
		if gameLib.scanning && !hasSystem {
			message = "Scanning the card…"
			detail = "The library opens as soon as the scan finishes."
		}
		if strings.HasPrefix(system.Name, continueSystemName) {
			detail = "Launch a game and it appears here for one-press resume."
		}
		if query := gameLib.query; query != "" {
			message = "No match for \"" + query + "\""
			detail = "Press S to change the search, or B to clear it and browse."
		} else if tag := gmetaFilterTag(); tag != "" {
			message = "No game is tagged " + tag
			detail = "Open a game (A) and choose Tags · B shows every game again."
		}
		// The shared empty state (glyph, title, wrapped explanation), centred
		// in the games column like every other list's.
		UIEmptyState(renderer, config, sdl.Rect{X: gameX, Y: bodyY, W: gameW, H: bodyH - 8},
			"@gamepad", message, detail)
	} else {
		for i := 0; i < visibleGames; i++ {
			idx := gameLib.scroll + i
			if idx >= len(system.Games) {
				break
			}
			rect := sdl.Rect{X: gameX, Y: bodyY + int32(i)*gameRowH, W: gameW, H: gameRowH - gameLibraryRowGap}
			gameLib.gameHits = append(gameLib.gameHits, gameLibRowHit{index: idx, rect: rect})
			selected := idx == gameLib.cursor
			bg := WithAlpha(ColorSurfaceRow, 150)
			if selected {
				bg = WithAlpha(focusColor, 46)
			}
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, bg)
			if selected && gameLib.focus == 1 {
				strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, glassFocusRingWidth, focusColor)
			}
			game := system.Games[idx]
			renderText(renderer, config, medium, ellipsize(medium, game.Label(), rect.W-24),
				ColorTextPrimary(), rect.X+12, rect.Y+rows.titleY)
			detail := game.System + " · " + filepath.Base(game.Path)
			if game.Hidden {
				detail += " · Hidden"
			}
			// A per-game clock is worth seeing in the list: it explains why
			// this title runs cooler than the one above it.
			if opts := gameOptionsFor(game.Path); opts.MaxFreqKHz > 0 {
				detail += " · " + formatGameCeiling(opts.MaxFreqKHz)
			}
			renderText(renderer, config, detailFont, ellipsize(detailFont, detail, rect.W-24),
				ColorTextTertiary(), rect.X+12, rect.Y+rows.detailY)
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
	snapPreviewSync(time.Now())
	if artW > 0 {
		preview := libraryGame{}
		if hasSystem && len(system.Games) > 0 {
			preview = system.Games[gameLib.cursor]
		}
		artSize := artW
		if bodyH-70 < artSize {
			artSize = bodyH - 70
		}
		rect := sdl.Rect{X: artX, Y: bodyY, W: artSize, H: artSize}
		gameLib.artRect = rect
		drawLibraryArt(renderer, config, rect, preview)
		if preview.Label() != "" {
			label := ellipsize(font, preview.Label(), artW)
			renderText(renderer, config, font, label, ColorTextSecondary(), artX, bodyY+artSize+10)
			// Fitted like the title: at 24px "PlayStation Portable" alone is
			// wider than the 250px column, and ran past the card's edge.
			renderText(renderer, config, detailFont, ellipsize(detailFont, preview.System, artW), ColorTextTertiary(),
				artX, bodyY+artSize+10+smallH+2)
		}
	} else {
		gameLib.artRect = sdl.Rect{}
	}

	artHint := FooterHint{"L1 / F", "Fill Gaps"}
	if artworkFetchRunning() {
		artHint = FooterHint{"L1 / F", "Stop"}
	}
	// X is only advertised while LAN Play is on: a footer that offers a
	// switched-off feature teaches a button that answers with a toast.
	hints := []FooterHint{{"A", "Open"}, {"L3 / T", "Tags"}, {"R / R3", "Random"},
		{"Start / O", "Options"}, {"S / R1", "Search"}}
	if netplayEnabled(config) {
		hints = append(hints, FooterHint{"X", "LAN Play"})
	}
	hints = append(hints, artHint, FooterHint{"Y", "Rescan"}, FooterHint{"B", "Close"})
	hint := hintLine(hints...)
	if gameLib.lib.Total == 0 {
		hint = hintLine(FooterHint{"Y", "Rescan"}, FooterHint{"B", "Close"})
	}
	// The hints and the library folder share the footer line: the folder
	// takes what the hints leave, right-aligned, instead of being drawn
	// across them.
	footerY := panelY + panelH - footerH + 4
	hint = ellipsize(font, hint, panelW-2*pad)
	hintW, _ := measureText(font, hint)
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+pad, footerY)
	if avail := panelW - 2*pad - hintW - 24; artW > 0 && len(gameLib.lib.Roots) > 0 && avail > 120 {
		root := ellipsize(font, gameLib.lib.Roots[0], avail)
		rootW, _ := measureText(font, root)
		renderText(renderer, config, font, root, ColorTextTertiary(), panelX+panelW-pad-rootW, footerY)
	}
	// A game's page, its tag sheet and the tag filter's chooser sit on the
	// library, under the random and options sheets they can open.
	gmetaRender(renderer, config)
	renderGameRandomPicker(renderer, config)
	renderGameOptionsPanel(renderer, config)
}
