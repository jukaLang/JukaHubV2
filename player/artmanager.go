package main

// artmanager.go — the Artwork Manager, a tool screen in Home's
// glass (tbxui.go's frame, focus list and sheets).
//
// One row per console with how many of its games have the shown picture type
// (box art, screenshot or title screen; it starts on Settings → Game art →
// Art type) and a bar, and a Fetch button beside it. On the console row A
// opens the console's games, each with which of box / screenshot / title it
// has; the Fetch button (Right, then A) queues the console's missing
// pictures, Y queues every console, X resets the console's artwork after a
// question. The top row switches the shown type, fetches all (or stops a
// fetch that runs) and "Clear done" hides the complete consoles.
//
// Fetching goes through the one art downloader and its queue
// (artstyle_fetch.go): progress is the header's, and L1 / F in the library
// stops the same run. Reset artwork moves only what JukaHub downloaded into
// its own art folders for that console — data/game-art/<System>, .snap and
// .title — to the reset trash (gamereset.go), and forgets the console's
// not-found answers so the next fetch asks again. It never touches the
// console folder's .media / Imgs / .res pictures, pictures taken from games
// (.gameplay) or "no cover" choices (.nocover).
//
// Opened from Settings → Games & emulators → Artwork Manager, Start → Tools
// and the library's options sheet (Artwork Manager): from the library, B on
// the console list goes back into the library on the console it was on.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	artManagerSceneName = "Artwork"
	artManagerEntryName = "Artwork Manager"
)

// artMgrGame is one game's pictures.
type artMgrGame struct {
	rom     scannedROM
	label   string
	id      string // the focus id, "g:<index>", made once per count
	box     bool
	snap    bool
	title   bool
	noCover bool
}

// has reports whether the game has a picture of kind.
func (g artMgrGame) has(kind string) bool {
	switch kind {
	case artTypeSnap:
		return g.snap
	case artTypeTitle:
		return g.title
	}
	return g.box
}

// artMgrConsole is one console's games and counts. total leaves out the
// games whose cover the user removed: they want no picture.
type artMgrConsole struct {
	system string
	// rowID and fetchID are the row's and its Fetch button's focus ids.
	rowID   string
	fetchID string
	games   []artMgrGame
	total   int
	box     int
	snap    int
	title   int
}

// count is how many of the console's games have a picture of kind.
func (c artMgrConsole) count(kind string) int {
	switch kind {
	case artTypeSnap:
		return c.snap
	case artTypeTitle:
		return c.title
	}
	return c.box
}

type artManagerState struct {
	visit   tbxVisit
	nav     tbxNav
	gameNav tbxNav
	// kind is the picture type the counts are for.
	kind     string
	hideDone bool
	// open is the console whose games are listed, "" for the console list.
	open     string
	consoles []artMgrConsole
	loaded   bool
	loading  bool
	again    bool
	// seenFetch is the art fetch generation the counts were made at.
	seenFetch uint64
	// fromLibrary: B on the console list goes back into the library, on
	// returnSystem.
	fromLibrary  bool
	returnSystem string
	// wantFocus is a console row to focus once the counts are in (the
	// console the library was on).
	wantFocus string
}

var artMgr artManagerState

// artManagerOpen opens the screen. Settings, the Start menu and the
// library's options sheet call it.
func artManagerOpen(config *Config) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	if !uiModeGate(uiFeatSetup) { // Kiosk / Kid mode keep the setup locked (uimode.go)
		return
	}
	if sceneIndexExact(config, artManagerSceneName) < 0 {
		config.Scenes = append(config.Scenes, SceneConfig{Name: artManagerSceneName})
	}
	artMgr.kind = artStyleType(config)
	artMgr.open = ""
	artMgr.fromLibrary, artMgr.wantFocus = false, ""
	tbxOpenScene(config, artManagerSceneName)
}

// artManagerOpenFromLibrary is the library options sheet's row: the library
// closes under the screen and comes back on B.
func artManagerOpenFromLibrary() {
	if !uiModeGate(uiFeatSetup) {
		return // Kiosk / Kid mode: the library stays open (artManagerOpen would refuse after closing it)
	}
	system := ""
	if s, ok := selectedLibrarySystem(); ok && gameLib.focus == 1 {
		system = s.Name
	}
	closeGameOptions()
	closeGameLibraryPanel()
	artManagerOpen(appConfig)
	artMgr.fromLibrary, artMgr.returnSystem = true, system
	artMgr.wantFocus = ""
	if system != "" {
		artMgr.wantFocus = "c:" + system
	}
}

// artManagerSettingsRow is Settings → Games & emulators' door to the screen.
func artManagerSettingsRow() setRow {
	return setRow{
		ID: "artManager", Label: artManagerEntryName, Kind: setKindAction,
		Detail: setStatic("Box art, screenshots and title screens per console: fetch what is missing, or start over"),
		Value: func(*Config) string {
			if done, total, running := artworkFetchProgress(); running {
				return "Fetching " + itoa(done) + " of " + itoa(total)
			}
			return ""
		},
		Open: artManagerOpen,
	}
}

// artManagerStartEntry is the Start menu's Tools row.
func artManagerStartEntry() startMenuEntry {
	return startMenuEntry{name: artManagerEntryName, icon: "A", glyph: "image", action: artManagerOpen}
}

type artManagerScene struct{ sceneBase }

func (artManagerScene) render(r *sdl.Renderer, c *Config) bool {
	if artMgr.visit.begin() {
		artMgr.nav.scroll = 0
		artMgrReload()
	} else if gen := artworkFetchGeneration(); artMgr.loaded && gen != artMgr.seenFetch {
		// A fetch finished: count again.
		artMgrReload()
	}
	if artMgr.open != "" {
		artMgrRenderGames(r, c)
	} else {
		artMgrRenderConsoles(r, c)
	}
	return true
}

func (artManagerScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	return tbxDispatch(c, tbxKeyAction(e), artMgrAction)
}

func (artManagerScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return tbxDispatch(c, tbxPadAction(e), artMgrAction)
}

func init() { registerScene(artManagerScene{sceneBase{name: artManagerSceneName}}) }

// ── Counting ─────────────────────────────────────────────────────────────

// artMgrReload counts every console's pictures again on a goroutine: the
// library index's scan, then one folder listing per console and type.
func artMgrReload() {
	if artMgr.loading {
		artMgr.again = true
		return
	}
	artMgr.loading = true
	gen := artworkFetchGeneration()
	root := libraryArtworkRoot()
	roots := romLibraryRoots()
	go func() {
		consoles := artMgrBuild(libIndexScan(roots).Games, root)
		tbxPost(func() {
			artMgr.loading = false
			artMgr.loaded = true
			artMgr.consoles = consoles
			artMgr.seenFetch = gen
			if artMgr.again {
				artMgr.again = false
				artMgrReload()
			}
		})
	}()
}

// artMgrBuild groups a scan by console and finds each game's pictures. Box
// art is JukaHub's download or the card's own picture (.media, Imgs, .res).
func artMgrBuild(games []scannedROM, root string) []artMgrConsole {
	type listing struct{ box, snap, title map[string]bool }
	lists := map[string]listing{}
	index := map[string]int{}
	var out []artMgrConsole
	for _, rom := range games {
		if !artStyleFetchable(rom) {
			continue
		}
		l, ok := lists[rom.System]
		if !ok {
			l = listing{box: artMgrNames(root, rom.System), snap: artStyleKindNames(root, artTypeSnap, rom.System),
				title: artStyleKindNames(root, artTypeTitle, rom.System)}
			lists[rom.System] = l
		}
		i, ok := index[rom.System]
		if !ok {
			i = len(out)
			index[rom.System] = i
			out = append(out, artMgrConsole{system: rom.System, rowID: "c:" + rom.System, fetchID: "f:" + rom.System})
		}
		file := strings.ToLower(romGameStem(rom.Path) + ".png")
		g := artMgrGame{rom: rom, label: gameDisplayTitle(rom.Path), noCover: artworkNoCover(rom, root)}
		g.box = l.box[file] || romLocalCoverPath(rom) != ""
		g.snap, g.title = l.snap[file], l.title[file]
		c := &out[i]
		c.games = append(c.games, g)
		if g.noCover {
			continue
		}
		c.total++
		if g.box {
			c.box++
		}
		if g.snap {
			c.snap++
		}
		if g.title {
			c.title++
		}
	}
	for i := range out {
		games := out[i].games
		sort.SliceStable(games, func(a, b int) bool {
			return strings.ToLower(games[a].label) < strings.ToLower(games[b].label)
		})
		for j := range games {
			games[j].id = "g:" + strconv.Itoa(j)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		return strings.ToLower(consoleDisplayName(out[a].system)) < strings.ToLower(consoleDisplayName(out[b].system))
	})
	return out
}

// artMgrNames lists JukaHub's downloaded box art of a console by lower-case
// file name.
func artMgrNames(root, system string) map[string]bool {
	out := map[string]bool{}
	if strings.TrimSpace(root) == "" {
		return out
	}
	entries, _ := os.ReadDir(filepath.Join(root, artCleanupDirName(system)))
	for _, e := range entries {
		if !e.IsDir() {
			out[strings.ToLower(e.Name())] = true
		}
	}
	return out
}

// artMgrConsoleOf is the counted console named system.
func artMgrConsoleOf(system string) (artMgrConsole, bool) {
	for _, c := range artMgr.consoles {
		if c.system == system {
			return c, true
		}
	}
	return artMgrConsole{}, false
}

// artMgrTotals sums the counts of every console for kind.
func artMgrTotals(kind string) (have, total int) {
	for _, c := range artMgr.consoles {
		have += c.count(kind)
		total += c.total
	}
	return have, total
}

// ── Actions ──────────────────────────────────────────────────────────────

// artMgrCycleKind steps the shown picture type.
func artMgrCycleKind() {
	i := artStyleOptionIndex(artStyleTypeOptions, artMgr.kind)
	artMgr.kind = artStyleTypeOptions[(i+1)%len(artStyleTypeOptions)].Value
	PlayNavSound()
}

// artMgrQueue queues the missing pictures of the shown type for the named
// consoles (nil: every console), one job per console.
func artMgrQueue(systems []string) {
	if !artMgr.loaded {
		ShowToast("Still counting your games…", ToastKindInfo)
		return
	}
	want := map[string]bool{}
	for _, s := range systems {
		want[s] = true
	}
	kind := artMgr.kind
	var jobs []artQueueJob
	games := 0
	for _, c := range artMgr.consoles {
		if systems != nil && !want[c.system] {
			continue
		}
		var roms []scannedROM
		for _, g := range c.games {
			if !g.noCover && !g.has(kind) {
				roms = append(roms, g.rom)
			}
		}
		if len(roms) > 0 {
			jobs = append(jobs, artQueueJob{system: c.system, kind: kind, roms: roms})
			games += len(roms)
		}
	}
	if len(jobs) == 0 {
		ShowToast("Nothing missing: every game has a "+artStyleTypeNoun(kind, false), ToastKindSuccess)
		return
	}
	if artQueueAdd(jobs...) == 0 {
		ShowToast("Already queued", ToastKindInfo)
		return
	}
	PlayActivateSound()
	where := "every console"
	if len(systems) == 1 {
		where = consoleDisplayName(systems[0])
	}
	ShowToast("Queued "+pluralizeCount(games, "missing "+artStyleTypeNoun(kind, false), "missing "+artStyleTypeNoun(kind, true))+
		" for "+where, ToastKindInfo)
}

// artMgrStop stops the running fetch and empties the queue.
func artMgrStop() {
	artQueueClear()
	if stopArtworkFetch() {
		ShowToast("Stopping the artwork fetch…", ToastKindInfo)
	}
}

// artMgrResetFiles lists what Reset artwork would move: the image files
// straight in the console's folders of JukaHub's art folder (box art,
// screenshots, title screens). Hidden files and sub-folders stay.
func artMgrResetFiles(system string) ([]artCleanupFile, int64) {
	root := libraryArtworkRoot()
	if strings.TrimSpace(root) == "" || strings.TrimSpace(system) == "" {
		return nil, 0
	}
	sys := artCleanupDirName(system)
	if !filepath.IsLocal(sys) || strings.ContainsAny(sys, `/\`) || strings.HasPrefix(sys, ".") {
		// One plain console folder only: never the art folder itself, a dot
		// folder (.gameplay, .nocover, .snap) or anything outside it.
		return nil, 0
	}
	dirs := []string{filepath.Join(root, sys)}
	for _, kind := range artStyleKinds {
		dirs = append(dirs, filepath.Join(root, artStyleKindDir(kind), sys))
	}
	var files []artCleanupFile
	var bytes int64
	for _, dir := range dirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || strings.HasPrefix(name, ".") || !artCleanupImage(name) {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			files = append(files, artCleanupFile{Path: filepath.Join(dir, name), System: system, Size: info.Size()})
			bytes += info.Size()
		}
	}
	return files, bytes
}

// artMgrAskReset asks before Reset artwork.
func artMgrAskReset(config *Config, system string) {
	if artworkFetchRunning() || artQueueLen() > 0 {
		ShowToast("Stop the artwork fetch first (Fetch all → Stop)", ToastKindInfo)
		return
	}
	files, bytes := artMgrResetFiles(system)
	name := consoleDisplayName(system)
	if len(files) == 0 {
		ShowToast("JukaHub has downloaded no artwork for "+name, ToastKindInfo)
		return
	}
	PlayActivateSound()
	tbxOpenSheet(config, tbxSheetSpec{
		title: "Reset artwork for " + name + "?", subtitle: pluralizeCount(len(files), "picture", "pictures") + ", " + humanBytes(bytes), icon: "image",
		body: "The box art, screenshots and title screens JukaHub downloaded for " + name +
			" go to the trash, and the next fetch looks every game up again. So do the other pictures in JukaHub's art folder for it: covers picked with Choose cover, sent from the phone or saved by the stores and Paint. Those don't come back with a fetch — the trash keeps them.\n\n" +
			"## Staying where they are\n• Pictures in the console's own folder (.media, Imgs, .res)\n• Pictures taken from games\n• Games set to show no cover",
		buttons: []tbxSheetButton{{label: "Cancel"}, {label: "Reset", primary: true, run: func() { artMgrRunReset(system, files) }}},
	})
}

// artMgrRunReset moves the files to the trash on a goroutine, forgets the
// console's not-found answers and counts again.
func artMgrRunReset(system string, files []artCleanupFile) {
	if artworkFetchRunning() || artQueueLen() > 0 {
		// A fetch started while the question was up (the automatic pass, the
		// library): it would write into the folders being emptied.
		ShowToast("Stop the artwork fetch first (Fetch all → Stop)", ToastKindInfo)
		return
	}
	if !gameResetBusy.CompareAndSwap(false, true) {
		ShowToast("A reset or cleanup is still moving files", ToastKindInfo)
		return
	}
	name := consoleDisplayName(system)
	collection := romCollectionForSystem(system)
	dest := libraryArtworkRoot()
	go func() {
		defer gameResetBusy.Store(false)
		dir, err := resetTrashNewDir("-covers") // pruned with the cover cleanups (gamereset.go)
		if err != nil {
			ShowToast("Cannot make the trash folder: "+firstLine(err.Error()), ToastKindError)
			return
		}
		list := make([]gameResetFile, 0, len(files))
		for _, f := range files {
			list = append(list, gameResetFile{Path: f.Path, Kind: "artwork"})
		}
		moved, failed := resetTrashMoveAll(dir, list, func(f gameResetFile) string {
			// <Sys>, .snap/<Sys> and .title/<Sys> apart ("snap_<Sys>"): the box
			// art, screenshot and title screen of a game share one file name.
			where := filepath.Base(filepath.Dir(f.Path))
			if rel, err := filepath.Rel(dest, filepath.Dir(f.Path)); err == nil && filepath.IsLocal(rel) {
				where = rel
			}
			return filepath.Join(resetTrashSlug(system), resetTrashSlug(where))
		})
		pruneResetTrash(dir)
		if collection != "" {
			artMgrForgetMisses(dest, collection)
		}
		logInfo("art", "reset artwork %s: %d moved, %d failed", system, moved, failed)
		tbxPost(func() {
			for _, f := range files {
				artworkDirForget(filepath.Dir(f.Path))
			}
			artStylePickReset()
			artMgrReload()
			if failed > 0 {
				ShowToast(pluralizeCount(moved, "picture", "pictures")+" moved to the trash · "+
					gameResetFilesWord(failed)+" could not be moved", ToastKindWarning)
				return
			}
			ShowToast("Artwork reset for "+name+" · "+pluralizeCount(moved, "picture", "pictures")+" in the trash", ToastKindSuccess)
		})
	}()
}

// artMgrForgetMisses drops a collection's not-found answers (box art,
// screenshots, title screens) from the not-found file. Off the render
// thread, with no fetch running (artMgrAskReset checks).
func artMgrForgetMisses(dest, collection string) {
	misses := loadArtworkMisses(dest)
	prefixes := []string{artworkMissKey(collection, "")}
	for _, kind := range artStyleKinds {
		prefixes = append(prefixes, artStyleMissKey(artStyleFetchFolder(kind), collection, ""))
	}
	changed := false
	for key := range misses {
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				delete(misses, key)
				changed = true
				break
			}
		}
	}
	if changed {
		if err := saveArtworkMisses(dest, misses); err != nil {
			logWarn("art", "cannot save the not-found cache: %v", err)
		}
	}
}

// artMgrAction is the screen's input after the sheet's.
func artMgrAction(config *Config, act tbxAction) bool {
	if artMgr.open != "" {
		return artMgrGamesAction(config, act)
	}
	nav := &artMgr.nav
	focus := nav.focus
	system := ""
	if strings.HasPrefix(focus, "c:") || strings.HasPrefix(focus, "f:") {
		system = focus[2:]
	}
	switch act {
	case tbxActConfirm:
		switch {
		case focus == "type":
			artMgrCycleKind()
		case focus == "all":
			if artworkFetchRunning() || artQueueLen() > 0 {
				artMgrStop()
			} else {
				artMgrQueue(nil)
			}
		case focus == "done":
			PlayNavSound()
			artMgr.hideDone = !artMgr.hideDone
		case strings.HasPrefix(focus, "c:"):
			PlayActivateSound()
			artMgr.open = system
			artMgr.gameNav.focus, artMgr.gameNav.scroll = "", 0
		case strings.HasPrefix(focus, "f:"):
			artMgrQueue([]string{system})
		}
		return true
	case tbxActY:
		artMgrQueue(nil)
		return true
	case tbxActX:
		if system != "" {
			artMgrAskReset(config, system)
		}
		return true
	case tbxActBack:
		if artMgr.fromLibrary {
			artMgr.fromLibrary = false
			goBackScene(config)
			if artMgr.returnSystem != "" && artMgr.returnSystem != continueSystemName {
				openGameLibraryOnSystem(artMgr.returnSystem, "")
			} else {
				openGameLibraryPanel()
			}
			return true
		}
		return false
	}
	return tbxNavAction(nav, act)
}

// artMgrGamesAction is the input on a console's games.
func artMgrGamesAction(config *Config, act tbxAction) bool {
	nav := &artMgr.gameNav
	system := artMgr.open
	switch act {
	case tbxActConfirm:
		switch focus := nav.focus; {
		case focus == "fetch":
			artMgrQueue([]string{system})
		case focus == "type":
			artMgrCycleKind()
		case focus == "reset":
			artMgrAskReset(config, system)
		case strings.HasPrefix(focus, "g:"):
			artMgrGameSheet(config, system, focus[2:])
		}
		return true
	case tbxActY:
		artMgrQueue([]string{system})
		return true
	case tbxActX:
		artMgrAskReset(config, system)
		return true
	case tbxActBack:
		PlayBackSound()
		artMgr.open = ""
		artMgr.nav.focus = "c:" + system
		artMgr.nav.reveal()
		return true
	}
	return tbxNavAction(nav, act)
}

// artMgrGameSheet tells where one game's pictures are.
func artMgrGameSheet(config *Config, system, id string) {
	c, ok := artMgrConsoleOf(system)
	i, err := strconv.Atoi(id)
	if !ok || err != nil || i < 0 || i >= len(c.games) {
		return
	}
	g := c.games[i]
	root := libraryArtworkRoot()
	line := func(label string, has bool, path string) string {
		if !has {
			return "• " + label + ": none yet"
		}
		return "• " + label + ": " + path
	}
	box := romLocalCoverPath(g.rom)
	if box == "" {
		box = romArtworkPath(g.rom, root)
	}
	body := "## Pictures\n" + line("Box art", g.box, box) + "\n" +
		line("Screenshot", g.snap, artStyleKindPath(g.rom, root, artTypeSnap)) + "\n" +
		line("Title screen", g.title, artStyleKindPath(g.rom, root, artTypeTitle))
	if g.noCover {
		body = "This game is set to show no cover (Game options → Restore cover brings covers back), so nothing is fetched for it.\n\n" + body
	}
	PlayActivateSound()
	tbxOpenSheet(config, tbxSheetSpec{title: g.label, subtitle: consoleDisplayName(system), icon: "image", body: body,
		buttons: []tbxSheetButton{{label: "OK", primary: true}}})
}

// artManagerFooterHints is the screen's footer; ok is false for any other
// scene.
func artManagerFooterHints(config *Config, name string) ([]FooterHint, bool) {
	if name != artManagerSceneName {
		return nil, false
	}
	if tbxSheetVisible(config) {
		return tbxSheetHints(), true
	}
	hints := []FooterHint{{Button: "D-Pad", Label: "Navigate"}}
	if artMgr.open != "" {
		if strings.HasPrefix(artMgr.gameNav.focus, "g:") {
			hints = append(hints, FooterHint{Button: "A", Label: "Details"})
		}
		return append(hints, FooterHint{Button: "Y", Label: "Fetch missing"}, FooterHint{Button: "X", Label: "Reset"}, FooterHint{Button: "B", Label: "Consoles"}), true
	}
	switch focus := artMgr.nav.focus; {
	case strings.HasPrefix(focus, "c:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Games"}, FooterHint{Button: "→", Label: "Fetch"}, FooterHint{Button: "X", Label: "Reset"})
	case strings.HasPrefix(focus, "f:"):
		hints = append(hints, FooterHint{Button: "A", Label: "Fetch"}, FooterHint{Button: "X", Label: "Reset"})
	default:
		hints = append(hints, FooterHint{Button: "A", Label: "Select"})
	}
	return append(hints, FooterHint{Button: "Y", Label: "Fetch all"}, FooterHint{Button: "B", Label: "Back"}), true
}

// ── Drawing ──────────────────────────────────────────────────────────────

// artMgrStatus is the header's read-out: the fetch's progress, or the
// library's total for the shown type.
func artMgrStatus() (string, sdl.Color) {
	if done, total, running := artworkFetchProgress(); running {
		s := "Fetching " + itoa(done) + " of " + itoa(total)
		if n := artQueueLen(); n > 0 {
			s += " · " + itoa(n) + " queued"
		}
		return s, ToastWarnInk()
	}
	if !artMgr.loaded {
		return "Counting…", ColorTextMuted()
	}
	have, total := artMgrTotals(artMgr.kind)
	if total > 0 && have == total {
		return "All " + itoa(total) + " complete", ToastSuccessInk()
	}
	return itoa(have) + " of " + itoa(total), ColorTextMuted()
}

// artMgrTypeLabel is the shown type as the type button says it.
func artMgrTypeLabel() string {
	return "Showing: " + artStyleOptionLabel(artStyleTypeOptions, artMgr.kind)
}

// artMgrButtons lays out a row of buttons from x and draws them; ids and
// labels pair up. It returns the row's height.
func artMgrButtons(renderer *sdl.Renderer, config *Config, fonts tbxFonts, nav *tbxNav, top, x, y int32, row int, ids, labels []string, primary string) int32 {
	const h = int32(44)
	bx := x
	for i, id := range ids {
		w := tbxButtonWidth(fonts.small, labels[i], 120)
		rect := sdl.Rect{X: bx, Y: y, W: w, H: h}
		screen := rect
		screen.Y += top
		tbxDrawButton(renderer, config, fonts.small, screen, labels[i], id == primary, nav.focused(id), false)
		nav.add(id, rect, row, false)
		bx += w + 12
	}
	return h
}

// artMgrRenderConsoles draws the console list.
func artMgrRenderConsoles(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	status, color := artMgrStatus()
	frame := tbxBeginFrame(renderer, config, tbxHeader(artManagerEntryName, "Box art, screenshots and title screens for every console", "image", status, color))
	body := frame.bodyRect()
	nav := &artMgr.nav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)

	allLabel := "Fetch all missing"
	if artworkFetchRunning() || artQueueLen() > 0 {
		allLabel = "Stop fetching"
	}
	doneLabel := "Clear done"
	if artMgr.hideDone {
		doneLabel = "Show all"
	}
	y += artMgrButtons(renderer, config, fonts, nav, top, x, y, 0,
		[]string{"type", "all", "done"}, []string{artMgrTypeLabel(), allLabel, doneLabel}, "all") + 14

	tbxDrawSection(renderer, config, "Consoles", "games", x, top+y, w)
	y += tbxSectionHeight
	rowH := tbxRowHeight(fonts)
	row := 1
	shown := 0
	for _, c := range artMgr.consoles {
		have := c.count(artMgr.kind)
		if artMgr.hideDone && have >= c.total {
			continue
		}
		shown++
		fetchW := tbxButtonWidth(fonts.small, "Fetch", 110)
		rect := sdl.Rect{X: x, Y: y, W: w - fetchW - 12, H: rowH}
		button := sdl.Rect{X: x + w - fetchW, Y: y + (rowH-44)/2, W: fetchW, H: 44}
		if screenY := top + y; screenY+rowH >= body.Y && screenY <= body.Y+body.H {
			screen := rect
			screen.Y += top
			artMgrDrawConsoleRow(renderer, config, fonts, screen, c, have, nav.focused(c.rowID))
			bs := button
			bs.Y += top
			tbxDrawButton(renderer, config, fonts.small, bs, "Fetch", false, nav.focused(c.fetchID), have >= c.total)
		}
		nav.add(c.rowID, rect, row, false)
		nav.add(c.fetchID, button, row, false)
		row++
		y += rowH + 8
	}
	if shown == 0 {
		title, text := "Counting your games…", "Each console's pictures are counted from the card."
		switch {
		case artMgr.loaded && len(artMgr.consoles) == 0:
			title, text = "No games found", "Put ROMs in Roms/<system>; the consoles appear here with their artwork."
		case artMgr.loaded:
			title, text = "Every console is complete", "Show all lists them again."
		}
		empty := sdl.Rect{X: x, Y: top + y, W: w, H: max(160, body.Y+body.H-(top+y))}
		tbxDrawEmpty(renderer, config, empty, "image", title, text)
		y += 160
	}
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	if artMgr.wantFocus != "" && artMgr.loaded {
		// Laid out now: the library's console can take the focus.
		nav.focus, artMgr.wantFocus = artMgr.wantFocus, ""
		if nav.index() < 0 {
			nav.focus = ""
			nav.end()
		}
		nav.reveal()
	}
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// artMgrDrawConsoleRow draws one console: its glyph, name, the have/total
// line with a bar, and a state pill.
func artMgrDrawConsoleRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, c artMgrConsole, have int, focused bool) {
	UIListRow(renderer, rect, glassRowRadius, focused, false)
	x := rect.X + 14
	badge := min(max(rect.H-24, 28), 44)
	box := drawGlassBadge(renderer, sdl.Rect{X: x, Y: rect.Y + (rect.H-badge)/2, W: badge, H: badge})
	DrawHomeIcon(renderer, librarySystemGlyph(c.system), box, focused)
	x += badge + 14

	pill, tone := "", tbxToneNeutral
	queued, running := artQueueState(c.system, artMgr.kind)
	switch {
	case running:
		pill, tone = "Fetching…", tbxToneAccent
	case queued:
		pill, tone = "Queued", tbxToneAccent
	case c.total > 0 && have >= c.total:
		pill, tone = "Complete", tbxToneGood
	case c.total > 0:
		pill = itoa(c.total-have) + " missing"
	}
	right := rect.X + rect.W - 16
	if pill != "" {
		p := tbxDrawPill(renderer, config, fonts.tiny, right, rect.Y+rect.H/2, pill, tone)
		right = p.X - 12
	}
	textW := max(right-x, 40)
	block := fonts.titleH + 6 + fonts.smallH
	y := rect.Y + (rect.H-block)/2
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, consoleDisplayName(c.system), textW), ColorTextPrimary(), x, y)
	counts := itoa(have) + "/" + itoa(c.total)
	cw, _ := measureText(fonts.small, counts)
	ly := y + fonts.titleH + 6
	renderText(renderer, config, fonts.small, counts, ColorTextMuted(), x, ly)
	barX := x + cw + 12
	barW := min(textW-cw-12, 220)
	if barW > 24 {
		barY := ly + fonts.smallH/2 - 3
		fillRoundedRect(renderer, barX, barY, barW, 6, 3, glassA(40))
		if c.total > 0 && have > 0 {
			fill := max(int32(int64(barW)*int64(have)/int64(c.total)), 6)
			fillRoundedRect(renderer, barX, barY, fill, 6, 3, glassAccent())
		}
	}
}

// artMgrRenderGames draws one console's games with the pictures each has.
func artMgrRenderGames(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	system := artMgr.open
	c, found := artMgrConsoleOf(system)
	status := ""
	if found {
		status = "Box " + itoa(c.box) + " · Shots " + itoa(c.snap) + " · Titles " + itoa(c.title) + " of " + itoa(c.total)
	}
	if done, total, running := artworkFetchProgress(); running {
		status = "Fetching " + itoa(done) + " of " + itoa(total)
	}
	frame := tbxBeginFrame(renderer, config, tbxHeader(consoleDisplayName(system), "Which pictures each game has", librarySystemGlyph(system), status, ColorTextMuted()))
	body := frame.bodyRect()
	nav := &artMgr.gameNav
	nav.begin(body.Y, body.H)
	tbxClip(renderer, body)
	top := body.Y - nav.scroll
	x, w := frame.x, frame.w
	y := int32(0)

	fetchLabel := "Fetch missing " + artStyleTypeNoun(artMgr.kind, true)
	y += artMgrButtons(renderer, config, fonts, nav, top, x, y, 0,
		[]string{"fetch", "type", "reset"}, []string{fetchLabel, artMgrTypeLabel(), "Reset artwork"}, "fetch") + 14

	rowH := fonts.titleH + 26
	for i, g := range c.games {
		rect := sdl.Rect{X: x, Y: y, W: w, H: rowH}
		id := g.id
		if screenY := top + y; screenY+rowH >= body.Y && screenY <= body.Y+body.H {
			screen := rect
			screen.Y += top
			artMgrDrawGameRow(renderer, config, fonts, screen, g, nav.focused(id))
		}
		nav.add(id, rect, i+1, false)
		y += rowH + 6
	}
	if !found || len(c.games) == 0 {
		tbxDrawEmpty(renderer, config, sdl.Rect{X: x, Y: top + y, W: w, H: 160}, "image", "No games", "This console has no games on the card now.")
		y += 160
	}
	nav.extend(y)
	tbxUnclip(renderer)
	nav.end()
	tbxDrawScrollbar(renderer, frame.x+frame.w+10, body.Y, body.H, nav.contentH, nav.scroll)
	tbxEndFrame(renderer, config)
}

// artMgrDrawGameRow draws one game: its title and three marks, Box, Shot
// and Title, lit for the pictures it has.
func artMgrDrawGameRow(renderer *sdl.Renderer, config *Config, fonts tbxFonts, rect sdl.Rect, g artMgrGame, focused bool) {
	UIListRow(renderer, rect, glassRowRadius, focused, false)
	right := rect.X + rect.W - 14
	marks := [...]struct {
		label string
		has   bool
	}{{"Title", g.title}, {"Shot", g.snap}, {"Box", g.box}}
	for _, m := range marks {
		tone := tbxToneNeutral
		if m.has && !g.noCover {
			tone = tbxToneGood
		}
		p := tbxDrawPill(renderer, config, fonts.tiny, right, rect.Y+rect.H/2, m.label, tone)
		right = p.X - 8
	}
	label := g.label
	color := ColorTextPrimary()
	if g.noCover {
		label += " · no cover by choice"
		color = ColorTextTertiary()
	}
	renderText(renderer, config, fonts.title, ellipsize(fonts.title, label, max(right-rect.X-30, 40)), color,
		rect.X+16, rect.Y+(rect.H-fonts.titleH)/2)
}
