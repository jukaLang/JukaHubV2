package main

// hometabs_games.go — the Games tab of the Tabs Home (JukaOS v2), and the
// pieces the Favourites and Collections tabs share with it: the library
// snapshot Home reads, the cover cache its tiles draw from, and how a game,
// a console or an app is turned into a homeTabItem.
//
// The Games tab lists, in this order:
//
//   - the consoles of the game library, one tile each ("sys:<system>"), the
//     ones played most recently first and the rest in the library's own
//     order (consoleOrder, then A–Z) — the same order the Games library
//     shows, so a console sits where the user already looks for it;
//   - JukaGames: the games JukaHub carries itself (Games, JukaLand, the
//     Synth), badged "Built-in";
//   - Remote play: PS Remote Play, Xbox Remote Play (X1, xremote.go), Steam;
//   - the other places games come from: Minecraft, PortMaster, Windows Apps;
//   - the Emulators manager and the whole Games library.
//
// Emulator cleaning (Custom "homeTabsEmuClean", on by default): a console
// with no games is left out, and a console with games but nothing to run
// them is shown dimmed with an Install badge whose A opens the Emulators page
// on that console (a game set to always use an emulator of its own counts as
// something to run them, homeTabsPinnedPlayable). Off ("Show all
// emulators"), every console the device has an emulator for is listed, empty
// ones reading "0 games".
//
// The library is the Games library's own scan (game_library_ui.go): when the
// library panel has scanned, Home adopts that result; otherwise Home runs the
// same scan in the background and refreshes it every few minutes while the
// tabs ask for it. Nothing here touches the disk on the render thread.

import (
	"context"
	"image"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// homeTabsEmuCleanKey is the Custom key of emulator cleaning: "auto" (the
// default) hides consoles without games, "all" shows every console.
const (
	homeTabsEmuCleanKey  = "homeTabsEmuClean"
	homeTabsEmuCleanAuto = "auto"
	homeTabsEmuCleanAll  = "all"
)

// homeTabsEmuCleanOn reports whether consoles without games are hidden.
func homeTabsEmuCleanOn(c *Config) bool {
	if c == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(customStringValue(c.Variables.Custom, homeTabsEmuCleanKey))) {
	case homeTabsEmuCleanAll, "off", "false", "0", "no":
		return false
	}
	return true
}

// homeTabsSetEmuClean stores the switch and rebuilds the tab.
func homeTabsSetEmuClean(c *Config, on bool) {
	value := homeTabsEmuCleanAll
	if on {
		value = homeTabsEmuCleanAuto
	}
	setStoreString(c, homeTabsEmuCleanKey, value)
	homeTabsTouch()
}

// ── The library snapshot ──────────────────────────────────────────────────

// How often Home rescans the card while the tabs keep asking, and how often
// it asks again which consoles have an emulator (a cheap, cached question).
const (
	homeTabsLibRescan   = 5 * time.Minute
	homeTabsLaunchEvery = 30 * time.Second
)

// homeTabsLib is the library as Home sees it. Render thread only: the
// background work hands its results over through tbxPost.
var homeTabsLib struct {
	report gameLibraryReport
	byPath map[string]libraryGame
	ready  bool
	at     time.Time
	gen    uint64

	scanning bool
	// adopted is the Games library scan last taken over (its first console
	// and its total), so the same result is not adopted twice.
	adopted      *librarySystem
	adoptedTotal int

	// playable says, per console of the snapshot, whether something can run
	// its games; runnable is every console the device has an emulator for.
	playable  map[string]bool
	runnable  []string
	launchAt  time.Time
	launching bool
}

// homeTabsLibrary returns the library snapshot, adopting a fresher Games
// library scan and starting a background scan when there is none yet or it
// is old. Cheap to call on every page rebuild.
func homeTabsLibrary() gameLibraryReport {
	s := &homeTabsLib
	if !gameLib.scanning && len(gameLib.lib.Systems) > 0 {
		first := &gameLib.lib.Systems[0]
		if first != s.adopted || gameLib.lib.Total != s.adoptedTotal {
			s.adopted, s.adoptedTotal = first, gameLib.lib.Total
			// Without the network drives' games: their tiles would start a
			// game that is not on the card (nas_index.go).
			homeTabsInstallLibrary(nasLibraryCardOnly(gameLib.lib))
			s.launchAt = time.Time{}
		}
	}
	if !s.scanning && !gameLib.scanning && (!s.ready || time.Since(s.at) >= homeTabsLibRescan) {
		homeTabsStartScan()
	}
	if s.ready && !s.launching && time.Since(s.launchAt) >= homeTabsLaunchEvery {
		homeTabsStartLauncherCheck()
	}
	return s.report
}

// homeTabsWatchState is what homeTabsWatch last saw.
var homeTabsWatchState struct {
	tagRev    uint64
	tagLoaded bool
}

// homeTabsWatch runs about once a second while the Tabs Home is on screen
// (homeTabsProvidersRender): the shell rebuilds a page only when told, so
// this is what brings in a Games library scan made meanwhile, the periodic
// rescan and emulator check, and a game favourited in the library.
func homeTabsWatch() {
	if frameCount%60 != 0 || !homeTabsLib.ready || !homeTabsOn() {
		return
	}
	homeTabsLibrary()
	// The tag file finishing its load counts too: a Favourites page built
	// before it had no favourite games yet.
	if rev, loaded := homeTabsTagState(); rev != homeTabsWatchState.tagRev || loaded != homeTabsWatchState.tagLoaded {
		homeTabsWatchState.tagRev, homeTabsWatchState.tagLoaded = rev, loaded
		homeTabsTouch()
	}
}

// homeTabsInstallLibrary makes lib the snapshot (render thread).
func homeTabsInstallLibrary(lib gameLibraryReport) {
	s := &homeTabsLib
	s.report = lib
	s.byPath = make(map[string]libraryGame, lib.Total)
	for _, system := range lib.Systems {
		for _, game := range system.Games {
			s.byPath[game.Path] = game
		}
	}
	s.ready = true
	s.at = time.Now()
	s.gen++
	homeTabsTouch()
}

// homeTabsStartScan runs the Games library's scan on a goroutine: the same
// steps startGameLibraryScan takes, with everything read from the config and
// the playtime ledger captured here on the render thread first.
func homeTabsStartScan() {
	s := &homeTabsLib
	s.scanning = true
	// The Games library scan in hand now: only a newer one landing while
	// this runs makes this result stale.
	var base *librarySystem
	if len(gameLib.lib.Systems) > 0 {
		base = &gameLib.lib.Systems[0]
	}
	roots := append([]string(nil), romLibraryRoots()...)
	html5Roots := append([]string(nil), html5LibraryRoots()...)
	html5DemoRoot := html5DataRoot()
	artRoot := libraryArtworkRoot()
	builtInScenes := builtInScenesPresent(appConfig)
	seconds := libraryPlaytimeSnapshot()
	// The favourites read the game tags; their file loads off this thread.
	gmetaPreload()
	early := !s.ready
	go func() {
		// The last library the index kept, while the first scan runs, and
		// the scan through the index (LIB-1, libindex.go).
		libIndexHomeEarly(roots, early)
		report := libIndexScan(roots)
		seconds := libraryPlaytimeForTitles(seconds, libraryScanTitleConsoles(report.Games))
		lib := buildGameLibraryWith(report, artRoot, seconds)
		libraryAddAndroidGames(&lib, seconds)
		libraryAddHTML5Games(&lib, seconds, html5Roots, html5DemoRoot)
		libraryAddBuiltInGames(&lib, builtInScenes, builtInCoverRoot())
		libIndexSaveLibrary(roots, lib)
		playable, runnable := homeTabsLaunchers(lib.Systems, nil)
		tbxPost(func() {
			s.scanning = false
			// A Games library scan that landed meanwhile (and was adopted) is
			// newer than this; one adopted before this started is older.
			if !gameLib.scanning && len(gameLib.lib.Systems) > 0 {
				if first := &gameLib.lib.Systems[0]; first != base && first == s.adopted {
					s.at = time.Now()
					return
				}
			}
			homeTabsInstallLibrary(lib)
			s.playable, s.runnable = playable, runnable
			s.launchAt = time.Now()
		})
	}()
}

// homeTabsStartLauncherCheck asks again, off the render thread, which
// consoles something can run: an emulator installed or removed since the
// scan changes the Install badges without a rescan of the card.
func homeTabsStartLauncherCheck() {
	s := &homeTabsLib
	s.launching = true
	s.launchAt = time.Now()
	// Names only: the report's rows may be the Games library's own, which
	// the render thread updates in place (recountLibraryArtwork). The games'
	// consoles come from byPath instead: a map of copies, made when the
	// snapshot was installed and never written after.
	systems := make([]librarySystem, 0, len(s.report.Systems))
	for _, system := range s.report.Systems {
		systems = append(systems, librarySystem{Name: system.Name})
	}
	byPath := s.byPath
	go func() {
		playable, runnable := homeTabsLaunchers(systems, byPath)
		tbxPost(func() {
			s.launching = false
			if homeTabsSameLaunchers(s.playable, s.runnable, playable, runnable) {
				return
			}
			s.playable, s.runnable = playable, runnable
			homeTabsTouch()
		})
	}()
}

// homeTabsSystemPlayable reports whether something can start a console's
// games: the built-in, Android and HTML5 rows always can; a console needs a
// launcher (one pinned for the console or a detected emulator). A game pinned
// to an emulator of its own is homeTabsPinnedPlayable's. J2ME games always
// can: their start sets up Java and FreeJ2ME-Plus, or says what is missing
// (j2me.go). Off the render thread.
func homeTabsSystemPlayable(system string) bool {
	switch system {
	case builtInLibrarySystem, androidLibrarySystem, html5LibrarySystem, j2meLibrarySystem:
		return true
	case wasm4System:
		// The first start installs the WASM-4 core where JukaHub can (wasm4.go).
		if wasm4CanInstall() {
			return true
		}
	}
	return resolveGameLauncher(system) != ""
}

// homeTabsLaunchers is homeTabsSystemPlayable for every console of systems,
// with the per-game pins on top (homeTabsPinnedPlayable: byPath names the
// snapshot's games, nil means systems carry their games), and the consoles
// the device has an emulator for. Off the render thread; the answer is kept
// in homeTabsLib and asked again every homeTabsLaunchEvery.
func homeTabsLaunchers(systems []librarySystem, byPath map[string]libraryGame) (map[string]bool, []string) {
	playable := make(map[string]bool, len(systems))
	for _, system := range systems {
		playable[system.Name] = homeTabsSystemPlayable(system.Name)
	}
	homeTabsPinnedPlayable(playable, systems, byPath)
	names := map[string]bool{}
	for _, s := range romSystemAliases {
		if strings.TrimSpace(s.Name) != "" {
			names[s.Name] = true
		}
	}
	for name := range systemLauncherAliases {
		names[name] = true
	}
	var runnable []string
	for name := range names {
		switch name {
		case builtInLibrarySystem, androidLibrarySystem, html5LibrarySystem, continueSystemName:
			continue
		}
		if resolveGameLauncher(name) != "" {
			runnable = append(runnable, name)
		}
	}
	sort.Strings(runnable)
	return playable, runnable
}

// The per-game pins one launcher check reads (LIB-22): at most homeTabsPinsMax
// games set to always use an emulator (game options → Emulator), and at most
// homeTabsPinStatMax of their files looked at. The pins are the in-memory game
// options, not the card, so a card of thousands of games costs a short walk
// over the few games that have a pin.
const (
	homeTabsPinsMax    = 512
	homeTabsPinStatMax = 32
)

// homeTabsPinnedPlayable marks playable a console that nothing plays by
// itself when one of its games is pinned to an emulator whose file is there
// (gameLauncherPinned, emupicker.go): such a console's games start, so it
// gets no Install badge. byPath finds a pinned game's console; when it is nil
// the games of systems are walked instead (a fresh scan, before the snapshot
// exists). Off the render thread.
func homeTabsPinnedPlayable(playable map[string]bool, systems []librarySystem, byPath map[string]libraryGame) {
	unplayable := 0
	for _, ok := range playable {
		if !ok {
			unplayable++
		}
	}
	if unplayable == 0 {
		return
	}
	pins := make(map[string]gameOptions, 8)
	gameOptionsMu.Lock()
	ensureGameOptionsLocked()
	for key, opts := range gameOptionsStore {
		if strings.TrimSpace(opts.Emulator) == "" {
			continue
		}
		pins[key] = opts
		if len(pins) >= homeTabsPinsMax {
			break
		}
	}
	gameOptionsMu.Unlock()
	if len(pins) == 0 {
		return
	}
	// The console of each pinned game, for the consoles still unplayable.
	consoleOf := make(map[string]string, len(pins))
	if byPath != nil {
		for key := range pins {
			if g, ok := byPath[key]; ok {
				consoleOf[key] = g.System
			}
		}
	} else {
		for _, system := range systems {
			if ok, known := playable[system.Name]; !known || ok {
				continue
			}
			for _, g := range system.Games {
				if key := gameOptionsKey(g.Path); key != "" {
					if _, pinned := pins[key]; pinned {
						consoleOf[key] = system.Name
					}
				}
			}
		}
	}
	looked := 0
	for key, system := range consoleOf {
		if ok, known := playable[system]; !known || ok {
			continue
		}
		if looked >= homeTabsPinStatMax {
			return
		}
		looked++
		if gameLauncherPinned(pins[key]) != "" {
			playable[system] = true
			unplayable--
			if unplayable == 0 {
				return
			}
		}
	}
}

// homeTabsSameLaunchers reports whether a launcher check changed nothing.
func homeTabsSameLaunchers(aPlay map[string]bool, aRun []string, bPlay map[string]bool, bRun []string) bool {
	if len(aPlay) != len(bPlay) || len(aRun) != len(bRun) {
		return false
	}
	for k, v := range aPlay {
		if w, ok := bPlay[k]; !ok || w != v {
			return false
		}
	}
	for i := range aRun {
		if aRun[i] != bRun[i] {
			return false
		}
	}
	return true
}

// homeTabsPlayable reports whether a console of the snapshot can be played.
// While the first check is still running a console counts as playable, so
// no Install badge flashes up on every console at boot.
func homeTabsPlayable(system string) bool {
	if homeTabsLib.playable == nil {
		return true
	}
	ok, known := homeTabsLib.playable[system]
	return !known || ok
}

// homeTabsGameByPath finds a game of the snapshot by its path.
func homeTabsGameByPath(path string) (libraryGame, bool) {
	g, ok := homeTabsLib.byPath[path]
	return g, ok
}

// ── Covers ────────────────────────────────────────────────────────────────

// Covers on the tabs are small: a tile is at most a few hundred pixels, so
// the decode is shrunk to homeTabsArtEdge and at most homeTabsArtMax of them
// are kept as textures (about 12 MB at the cap). A texture nobody drew for
// homeTabsArtIdle frames is freed.
const (
	homeTabsArtEdge   = 256
	homeTabsArtMax    = 48
	homeTabsArtQueue  = 64
	homeTabsArtIdle   = 600
	homeTabsArtUpload = 2
	homeTabsArtRetry  = 2 * time.Minute
)

// homeTabsCovers is the tabs' cover cache. One worker decodes the queue in the
// background; the render thread uploads finished pictures (a couple a frame)
// in homeTabsArtPump and serves textures from tex.
var homeTabsCovers = struct {
	mu      sync.Mutex
	queue   []string
	queued  map[string]bool
	done    map[string]*image.NRGBA
	failed  map[string]time.Time
	working bool
	// Render thread only:
	tex   map[string]*sdl.Texture
	used  map[string]uint64
	owner *sdl.Renderer
}{
	queued: map[string]bool{},
	done:   map[string]*image.NRGBA{},
	failed: map[string]time.Time{},
	tex:    map[string]*sdl.Texture{},
	used:   map[string]uint64{},
}

// homeTabsArtFn is an item's Art: a cache lookup that queues the cover when
// it is not there yet. nil for no cover.
func homeTabsArtFn(path string) func() *sdl.Texture {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return func() *sdl.Texture { return homeTabsArtLookup(path) }
}

// homeTabsArtLookup is the texture for path, or nil while it is decoded.
// Render thread; never touches the disk.
func homeTabsArtLookup(path string) *sdl.Texture {
	a := &homeTabsCovers
	if tex := a.tex[path]; tex != nil {
		a.used[path] = frameCount
		return tex
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.queued[path] || a.done[path] != nil {
		return nil
	}
	if at, failed := a.failed[path]; failed && time.Since(at) < homeTabsArtRetry {
		return nil
	}
	if len(a.queue) >= homeTabsArtQueue {
		return nil
	}
	a.queued[path] = true
	a.queue = append(a.queue, path)
	if !a.working {
		a.working = true
		go homeTabsArtWork()
	}
	return nil
}

// homeTabsArtWork decodes the queue, newest request first (the tiles on
// screen now), until it is empty.
func homeTabsArtWork() {
	a := &homeTabsCovers
	for {
		a.mu.Lock()
		if len(a.queue) == 0 {
			a.working = false
			a.mu.Unlock()
			return
		}
		path := a.queue[len(a.queue)-1]
		a.queue = a.queue[:len(a.queue)-1]
		a.mu.Unlock()

		pic, err := decodeLibraryArt(path, context.Background())
		if err == nil && pic != nil {
			b := pic.Bounds()
			w, h := b.Dx(), b.Dy()
			if edge := max(w, h); edge > homeTabsArtEdge {
				w = max(1, w*homeTabsArtEdge/edge)
				h = max(1, h*homeTabsArtEdge/edge)
				pic = imgvScale(pic, w, h)
			}
		}
		a.mu.Lock()
		delete(a.queued, path)
		if err != nil || pic == nil {
			a.failed[path] = time.Now()
		} else {
			a.done[path] = pic
		}
		a.mu.Unlock()
	}
}

// homeTabsArtPump uploads finished covers and frees the ones no tile has
// drawn for a while. Called once a frame from the overlay pass (render
// thread), whether or not Home is showing.
func homeTabsArtPump(renderer *sdl.Renderer) {
	a := &homeTabsCovers
	if renderer == nil {
		return
	}
	if a.owner != nil && a.owner != renderer {
		// Textures die with the renderer that made them: forget them.
		a.tex = map[string]*sdl.Texture{}
		a.used = map[string]uint64{}
		a.owner = nil
	}
	// Taken under the lock, uploaded outside it: the worker keeps adding.
	var paths []string
	var pics []*image.NRGBA
	a.mu.Lock()
	for path, pic := range a.done {
		if len(paths) >= homeTabsArtUpload {
			break
		}
		paths = append(paths, path)
		pics = append(pics, pic)
	}
	for _, path := range paths {
		delete(a.done, path)
	}
	a.mu.Unlock()
	for i, path := range paths {
		tex := imgvTexture(renderer, pics[i])
		if tex == nil {
			a.mu.Lock()
			a.failed[path] = time.Now()
			a.mu.Unlock()
			continue
		}
		_ = tex.SetBlendMode(sdl.BLENDMODE_BLEND)
		if old := a.tex[path]; old != nil {
			old.Destroy()
		}
		a.tex[path] = tex
		a.used[path] = frameCount
		a.owner = renderer
	}
	if frameCount%60 != 0 && len(a.tex) <= homeTabsArtMax {
		return
	}
	// Idle textures go; past the cap, the least recently drawn go first.
	// (The pass runs after Home has drawn, so "this frame" is up to date.)
	for path, at := range a.used {
		if frameCount-at > homeTabsArtIdle {
			homeTabsArtDrop(path)
		}
	}
	for len(a.tex) > homeTabsArtMax {
		oldest, oldestAt := "", ^uint64(0)
		for path, at := range a.used {
			if at < oldestAt {
				oldest, oldestAt = path, at
			}
		}
		// Never one drawn this frame: a page showing more covers than the
		// cap keeps them all rather than decoding them again every frame.
		if oldest == "" || oldestAt == frameCount {
			break
		}
		homeTabsArtDrop(oldest)
	}
}

// homeTabsArtDrop frees one cover texture (render thread).
func homeTabsArtDrop(path string) {
	a := &homeTabsCovers
	if tex := a.tex[path]; tex != nil {
		tex.Destroy()
	}
	delete(a.tex, path)
	delete(a.used, path)
}

// ── Items ─────────────────────────────────────────────────────────────────

// homeTabsGlyph is the first of names the icon set knows ("apps" when none).
func homeTabsGlyph(names ...string) string {
	for _, n := range names {
		if n != "" && iconKnown(n) {
			return n
		}
	}
	return "apps"
}

// homeTabsGameRef is a game's item ref.
func homeTabsGameRef(path string) string { return homeTabRefGame + path }

// homeTabsSystemRef is a console's item ref.
func homeTabsSystemRef(system string) string { return homeTabRefSystem + system }

// homeTabsGameItem is one game as a tile: its cover, its console under the
// name, A plays it the way the library does.
func homeTabsGameItem(g libraryGame) homeTabItem {
	ref := homeTabsGameRef(g.Path)
	it := homeTabItem{
		Key:   ref,
		Ref:   ref,
		Title: g.Label(),
		Sub:   g.System,
		Glyph: homeTabsGlyph(librarySystemGlyph(g.System), "gamepad"),
	}
	if b, ok := builtInLibraryGameFor(g); ok {
		it.Sub = b.detail
		it.Glyph = homeTabsGlyph(b.glyph, "gamepad")
	}
	if g.HasArt {
		it.Art = homeTabsArtFn(g.Art)
	}
	game := g
	it.Activate = func(c *Config) { homeTabsPlayGame(c, game) }
	return it
}

// homeTabsPlayGame starts a game from Home: a built-in game opens its
// screen; any other goes through the emulator picker and the library's
// launch (recent.go's path, the one Continue uses).
func homeTabsPlayGame(c *Config, g libraryGame) {
	if isBuiltInLibraryGame(g) {
		openBuiltInLibraryGame(g, c)
		return
	}
	playRecentGame(recentGame{Path: g.Path, System: g.System, Title: g.Title})
}

// homeTabsConsoleItem is one console as a tile. playable false (games but
// nothing to run them) dims it and turns A into the install offer.
func homeTabsConsoleItem(system librarySystem, playable bool) homeTabItem {
	ref := homeTabsSystemRef(system.Name)
	name := system.Name
	n := len(system.Games)
	it := homeTabItem{
		Key:   ref,
		Ref:   ref,
		Title: name,
		Sub:   pluralizeCount(n, "game", "games"),
		Glyph: homeTabsGlyph(librarySystemGlyph(name), "gamepad"),
		Art:   homeTabsArtFn(librarySystemCover(system)),
	}
	switch {
	case n > 0 && !playable:
		it.Dim = true
		it.Badge = "Install"
		it.Activate = func(*Config) { homeTabsOfferInstall(name) }
	case n == 0:
		it.Activate = func(*Config) { ShowToast(name+" has no games on this device yet", ToastKindInfo) }
	default:
		it.Activate = func(*Config) { homeTabsOpenConsole(name) }
	}
	return it
}

// homeTabsConsoles is the Games tab's console tiles: the snapshot's consoles
// (not the built-in row, which JukaGames lists game by game), recently played
// first, and with cleaning off every console the device can run.
func homeTabsConsoles(c *Config) []homeTabItem {
	lib := homeTabsLibrary()
	systems := filterHiddenLibrarySystems(lib.Systems, libraryShowHidden())
	clean := homeTabsEmuCleanOn(c)
	rank := map[string]int{}
	for i, g := range recentGames() {
		if _, seen := rank[g.System]; !seen {
			rank[g.System] = i
		}
	}
	type entry struct {
		system librarySystem
		order  int
	}
	var list []entry
	have := map[string]bool{}
	for i, system := range systems {
		if system.Name == builtInLibrarySystem || system.Name == continueSystemName {
			continue
		}
		if clean && len(system.Games) == 0 {
			continue
		}
		have[system.Name] = true
		list = append(list, entry{system: system, order: i})
	}
	if !clean {
		extra := make([]librarySystem, 0, len(homeTabsLib.runnable))
		for _, name := range homeTabsLib.runnable {
			if !have[name] {
				extra = append(extra, librarySystem{Name: name})
			}
		}
		sortLibrarySystems(extra)
		for i, system := range extra {
			list = append(list, entry{system: system, order: len(systems) + i})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		ri, iok := rank[list[i].system.Name]
		rj, jok := rank[list[j].system.Name]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return list[i].order < list[j].order
	})
	out := make([]homeTabItem, 0, len(list))
	for _, e := range list {
		out = append(out, homeTabsConsoleItem(e.system, len(e.system.Games) == 0 || homeTabsPlayable(e.system.Name)))
	}
	return out
}

// homeTabsOpenConsole opens the Games library straight on a console's games.
// On the first visit the library has no scan of its own yet: it starts from
// Home's snapshot (built the same way) while its own scan runs.
func homeTabsOpenConsole(system string) {
	openGameLibraryPanel()
	if len(gameLib.lib.Systems) == 0 && homeTabsLib.ready {
		gameLib.lib = homeTabsLib.report
		invalidateGameLibrarySystemsCache()
	}
	for i, s := range currentGameSystems() {
		if s.Name == system {
			gameLib.system = i
			gameLib.focus = 1
			gameLib.cursor = 0
			gameLib.scroll = 0
			return
		}
	}
}

// homeTabsOfferInstall is A on a console that has games but nothing to run
// them: the Emulators page opens on that console's tile, where X (or A)
// installs it; a console the page has no tile for says what it needs.
func homeTabsOfferInstall(system string) {
	openEmulatorPanel()
	entries := emulatorEntries()
	if i := homeTabsEmuEntryIndex(system, entries); i >= 0 {
		emuPanel.cursor = i
		// emuInstallHintLabel is the page's A label (emuFooterHints).
		if label := emuInstallHintLabel(entries[i]); label == "Install" || label == "Get" {
			ShowToast("Nothing plays "+system+" games yet · "+hintLine(FooterHint{"A", label}), ToastKindInfo)
			return
		}
	}
	if hint := emuLibraryLauncherHint(system); hint != "" {
		ShowToast(hint, ToastKindInfo)
		return
	}
	ShowToast("Nothing plays "+system+" games yet — add an emulator here", ToastKindInfo)
}

// homeTabsEmuEntryIndex finds the Emulators page tile for a console: a tile
// named like the console or one of its emulator and folder names. -1 when
// there is none.
func homeTabsEmuEntryIndex(system string, entries []emuEntry) int {
	want := map[string]bool{}
	for _, n := range gameLauncherCandidates(system) {
		want[n] = true
	}
	for _, f := range gameLauncherFolders(system) {
		if n := normaliseLauncherName(f); n != "" {
			want[n] = true
		}
	}
	for i, e := range entries {
		if !e.Pinned && want[normaliseLauncherName(e.Name)] {
			return i
		}
	}
	for i, e := range entries {
		if e.Pinned {
			continue
		}
		for _, g := range launcherNameGroups(e.Name) {
			if want[g] {
				return i
			}
		}
	}
	return -1
}

// homeTabsAppIndex caches the Start menu's entries by ref for one frame:
// resolving a page of app refs walks the menu once, not once per ref.
var homeTabsAppIndex struct {
	frame   uint64
	built   bool
	entries map[string]startMenuEntry
}

// homeTabsAppEntry is homeAppEntryFor through that cache.
func homeTabsAppEntry(c *Config, ref string) (startMenuEntry, bool) {
	ix := &homeTabsAppIndex
	if !ix.built || ix.frame != frameCount {
		ix.built, ix.frame = true, frameCount
		ix.entries = map[string]startMenuEntry{}
		entries, _ := homeAppEntries(c)
		for _, e := range entries {
			ix.entries[strings.ToLower(homeAppRefFor(e))] = e
		}
	}
	e, ok := ix.entries[strings.ToLower(ref)]
	return e, ok
}

// homeTabsAppItem is an app ref as a tile ("app:scene:…", "app:do:…"),
// false when nothing on this device answers to it. Xbox Remote Play and
// Steam are Start menu rows too (startmenu.go), so their refs
// resolve here and in homeActivateApp alike.
func homeTabsAppItem(c *Config, ref, sub string) (homeTabItem, bool) {
	entry, ok := homeTabsAppEntry(c, ref)
	if !ok {
		return homeTabItem{}, false
	}
	ref = homeAppRefFor(entry)
	it := homeTabItem{Key: ref, Ref: ref, Title: entry.name, Sub: sub, Glyph: homeTabsGlyph(startMenuGlyph(entry))}
	it.Activate = func(c *Config) { homeActivateApp(c, ref) }
	return it, true
}

// homeTabsJukaGames is JukaGames: the games JukaHub carries, each with its
// cover when the library installed one.
func homeTabsJukaGames(c *Config) []homeTabItem {
	var out []homeTabItem
	for _, scene := range []string{"Games", "JukaLand", synthSceneName, solitaireSceneName} {
		it, ok := homeTabsAppItem(c, homeTileScenePrefix+scene, "JukaGames")
		if !ok {
			continue
		}
		it.Badge = "Built-in"
		for _, b := range builtInLibraryGames {
			// The Games screen's arcade minigames share its scene; the
			// Games tile keeps its own glyph and they get tiles below.
			if b.scene != scene || b.arcade != gameNone {
				continue
			}
			it.Glyph = homeTabsGlyph(b.glyph, it.Glyph)
			if g, found := homeTabsGameByPath(b.path); found && g.HasArt {
				it.Art = homeTabsArtFn(g.Art)
			}
		}
		out = append(out, it)
	}
	// Each arcade minigame (Snake, Pong…) as a tile with its cover: A plays
	// it straight away (openBuiltInLibraryGame). Only once the library has
	// listed it, which a configuration without the Games screen never does.
	for _, b := range builtInLibraryGames {
		if b.arcade == gameNone {
			continue
		}
		if g, found := homeTabsGameByPath(b.path); found {
			it := homeTabsGameItem(g)
			it.Badge = "Built-in"
			out = append(out, it)
		}
	}
	return out
}

// homeTabsGamesItems is the Games tab (hometabs_model.go).
func homeTabsGamesItems(c *Config) []homeTabItem {
	out := homeTabsConsoles(c)
	out = append(out, homeTabsJukaGames(c)...)
	// Remote play, then the other places games come from, then the tools.
	rows := []struct{ ref, sub string }{
		{homeTileActionPrefix + "PS Remote Play", "Remote play"},
		{homeTileActionPrefix + xboxRemotePlayEntryName, "Remote play"},
		{homeTileActionPrefix + moonlightEntryName, "Your gaming PC"},
		{homeTileActionPrefix + cloudGamingEntryName, "Game Pass · GeForce NOW"},
		{homeTileActionPrefix + steamEntryName, "Your Steam library"},
		{homeTileActionPrefix + minecraftEntryName, "Every way to play"},
		{homeTileActionPrefix + portsStoreEntryName, "Download ports"},
		{homeTileActionPrefix + homebrewEntryName, "New games from itch.io"},
		{homeTileActionPrefix + portMasterEntryName, "PC ports"},
		{homeTileActionPrefix + windowsAppsEntryName, "Windows games"},
		{homeTileActionPrefix + "Emulators", "Install and update"},
		{homeTileActionPrefix + gamesLibraryEntryName, "Every game, by console"},
	}
	for _, r := range rows {
		if it, ok := homeTabsAppItem(c, r.ref, r.sub); ok {
			out = append(out, it)
		}
	}
	return out
}
