package main

// gmeta_ui.go — a game's own page in the Games library.
//
// A on a game used to start it straight away. It now opens the game's page, a
// launcher in the console sense: the box art (or the video snap), what the
// libretro database knows about it (genre, developer, year, players), the
// rating, the tags and the description the user wrote, how long it has been
// played on this device and when last, its RetroAchievements progress, the
// local milestones it has earned, and — for RetroArch games — the save states
// with their screenshots, so a game can be started from any of them. Play is
// focused, so A, A still starts a game in two presses.
//
// Also here: the tag sheet (stacked on the page), the library's tag filter
// (L3 / T: show only the Favorites, the Backlog, a custom tag …) and the tick
// that celebrates the hour milestones through the trophy card.
//
// Everything slow — the save-state folders, the screenshots, the CRC, the
// libretro lookup, the RetroAchievements cache — is read on goroutines and
// posted back to the main loop (tbxPost); the page draws from memory only.

import (
	"context"
	"image"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// The page's buttons, in the order the row shows them.
const (
	gmetaBtnPlay = iota
	gmetaBtnFavorite
	gmetaBtnTags
	gmetaBtnRate
	gmetaBtnAbout
	gmetaBtnNotes
	gmetaBtnCount
)

// gmetaLookupKey is the custom setting that lets the library look games up in
// the libretro database (on by default).
const gmetaLookupKey = "gameInfoLookup"

// gmetaLookupEnabled is a test seam: the suite never asks libretro-database.
var gmetaLookupEnabled = true

// gmetaBackgroundLoads is a test seam too: a page opened by an input test does
// not start goroutines that read the developer's emulator and save folders.
var gmetaBackgroundLoads = true

// gmetaDetails is the open page.
type gmetaDetails struct {
	open    bool
	game    libraryGame
	rec     gmetaRecord
	gen     int
	loading bool
	// lookup is what the details line says while there are no details yet.
	lookup string
	states []gmetaState
	isRA   bool
	resume bool
	ra     string
	// coreNote warns that some states were made with another build of the
	// core (corebuilds_history.go); "" when none were.
	coreNote string
	// zone is the row with the focus: 0 the buttons, 1 the save states.
	zone   int
	button int
	state  int
	first  int
	// launched is when the page last started its game; the page reloads its
	// states and playtime once the game is over.
	launched  time.Time
	card      sdl.Rect
	btnRects  [gmetaBtnCount]sdl.Rect
	stateHits []gameLibRowHit
	thumbs    map[string]*sdl.Texture
	pending   map[string]*image.NRGBA
	wrap      tbxWrapCache
	cancel    context.CancelFunc
}

var gmetaUI gmetaDetails

// gmetaGen numbers the pages opened, so a late answer for a closed page is
// dropped.
var gmetaGen int

// gmetaTagUI is the tag sheet stacked on the page.
var gmetaTagUI struct {
	open   bool
	cursor int
	tags   []string
	rows   []gameLibRowHit
	card   sdl.Rect
}

// gmetaPickRow is one choice of the library's tag filter.
type gmetaPickRow struct {
	tag   string
	label string
	count int
}

// gmetaPick is the tag filter's chooser, stacked on the library.
var gmetaPick struct {
	open   bool
	cursor int
	rows   []gmetaPickRow
	hits   []gameLibRowHit
	card   sdl.Rect
}

// gmetaFilter is the library's active tag filter and the set of paths it
// keeps, rebuilt when the store or the library changes (a rescan makes a new
// list, so its first console's address changes even when the count does not).
var gmetaFilter struct {
	tag   string
	paths map[string]bool
	rev   uint64
	total int
	scan  *librarySystem
}

// ── Settings ────────────────────────────────────────────────────────────────

// gmetaAutoLookup reports whether opening a game may look it up in the libretro
// database when online.
func gmetaAutoLookup(config *Config) bool {
	if config == nil {
		return true
	}
	if v, ok := customValue(config.Variables.Custom, gmetaLookupKey); ok {
		return coerceBool(v)
	}
	return true
}

// gmetaSetAutoLookup stores the switch (the Settings row).
func gmetaSetAutoLookup(config *Config, on bool) {
	setStoreBool(config, gmetaLookupKey, on)
}

// ── Library hooks (game_library_ui.go) ───────────────────────────────────────

// gmetaSheetOpen reports whether one of this file's sheets owns the library's
// input.
func gmetaSheetOpen() bool { return gmetaUI.open || gmetaPick.open }

// gmetaLibraryOpened resets the filter as the library opens and loads the
// store in the background, so the first page opens without a read.
func gmetaLibraryOpened() {
	gmetaFilter.tag = ""
	gmetaFilter.paths = nil
	gmetaPick.open = false
	gmetaPreload()
}

// gmetaCloseAll closes the page, its tag sheet and the filter chooser, and
// frees the page's textures (they belong to the renderer).
func gmetaCloseAll() {
	gmetaCloseDetails(false)
	gmetaPick.open = false
}

// gmetaFilterTag is the active tag filter ("" when off).
func gmetaFilterTag() string { return gmetaFilter.tag }

// gmetaSetFilter shows only the games carrying tag ("" shows everything) and
// puts the highlight on the first of them.
func gmetaSetFilter(tag string) {
	gmetaFilter.tag = tag
	gmetaFilter.paths = nil
	gameLib.system, gameLib.cursor, gameLib.scroll = 0, 0, 0
	if tag != "" {
		gameLib.focus = 1
	} else {
		gameLib.focus = 0
	}
}

// gmetaClearFilter drops the filter; it reports whether there was one, so Back
// clears a filter before it leaves the library.
func gmetaClearFilter() bool {
	if gmetaFilter.tag == "" {
		return false
	}
	gmetaSetFilter("")
	PlayBackSound()
	return true
}

// gmetaFilterSystems narrows the console list to the filter's games. Without a
// filter it returns systems untouched.
func gmetaFilterSystems(systems []librarySystem) []librarySystem {
	if gmetaFilter.tag == "" {
		return systems
	}
	rev := gmetaRev()
	var scan *librarySystem
	if len(gameLib.lib.Systems) > 0 {
		scan = &gameLib.lib.Systems[0]
	}
	if gmetaFilter.paths == nil || rev != gmetaFilter.rev || gameLib.lib.Total != gmetaFilter.total || scan != gmetaFilter.scan {
		gmetaFilter.paths = gmetaTaggedPaths(gameLib.lib.Systems, gmetaFilter.tag)
		gmetaFilter.rev = rev
		gmetaFilter.total = gameLib.lib.Total
		gmetaFilter.scan = scan
	}
	return gmetaFilterLibrary(systems, gmetaFilter.paths)
}

// gmetaOpenSelected is A in the library: on a console it moves to its games, on
// a game it opens the game's page.
func gmetaOpenSelected() bool {
	game, ok := selectedLibraryGame()
	if !ok {
		return false
	}
	if gameLib.focus == 0 {
		gameLib.focus = 1
		PlayNavSound()
		return true
	}
	gmetaOpenDetails(game)
	return true
}

// ── The page ────────────────────────────────────────────────────────────────

// gmetaOpenDetails opens a game's page and starts loading what it shows.
func gmetaOpenDetails(game libraryGame) {
	gmetaCloseDetails(false)
	gmetaGen++
	gen := gmetaGen
	rec, _ := gmetaGet(game.System, game.Title)
	ctx, cancel := context.WithCancel(context.Background())
	lookup := gmetaLookupEnabled && gmetaAutoLookup(appConfig) && rec.Scraped == ""
	gmetaUI = gmetaDetails{
		open:    true,
		game:    game,
		rec:     rec,
		gen:     gen,
		loading: true,
		resume:  savestateResumeEnabled(),
		thumbs:  map[string]*sdl.Texture{},
		pending: map[string]*image.NRGBA{},
		cancel:  cancel,
	}
	if lookup {
		gmetaUI.lookup = "Looking up the genre and year…"
	}
	PlayActivateSound()
	gmetaStartLoad(ctx, gen, game, rec, lookup, false)
}

// gmetaStartLoad reads the page's data in the background: the local part (save
// states, screenshots, CRC, RetroAchievements) and, when asked, the libretro
// lookup, which may wait on the network.
func gmetaStartLoad(ctx context.Context, gen int, game libraryGame, rec gmetaRecord, lookup, force bool) {
	if !gmetaBackgroundLoads {
		gmetaUI.loading = false
		return
	}
	online := IsOnline()
	go gmetaLoadLocal(ctx, gen, game, rec)
	if lookup {
		go gmetaLoadLookup(ctx, gen, game, rec, online, force)
	}
}

// gmetaLoadLocal finds the save states and the RetroAchievements progress, then
// decodes the state screenshots one by one.
func gmetaLoadLocal(ctx context.Context, gen int, game libraryGame, rec gmetaRecord) {
	launcher := resolveGameLauncher(game.System)
	isRA := launcher != "" && retroArchLauncher(launcher)
	var states []gmetaState
	if isRA {
		states = gmetaListStates(game.Path, gmetaStateDirs(game.Path))
	}
	ra := gmetaRAProgress(game)
	crc := rec.CRC
	if crc == "" {
		if crcs := gmetaROMCRCs(game.Path); len(crcs) > 0 {
			crc = crcs[0]
		}
	}
	tbxPost(func() {
		if crc != "" && rec.CRC == "" {
			// A renamed file takes over its old record here (gmetaAttachCRC).
			stored := gmetaAttachCRC(game.System, game.Title, crc)
			if gmetaUI.open && gmetaUI.gen == gen {
				gmetaUI.rec = stored
			}
		}
		if !gmetaUI.open || gmetaUI.gen != gen {
			return
		}
		gmetaUI.loading = false
		gmetaUI.isRA = isRA
		gmetaUI.states = states
		if len(states) == 0 {
			gmetaUI.coreNote = ""
		}
		gmetaUI.ra = ra
		if gmetaUI.state >= len(states) {
			gmetaUI.state = 0
		}
		if len(states) == 0 && gmetaUI.zone == 1 {
			gmetaUI.zone = 0
		}
	})
	// The core-build line comes after the strip is on screen: it reads the
	// launcher, the history and (once per binary) the core's checksum
	// (corebuilds_history.go).
	if len(states) > 0 && ctx.Err() == nil {
		coreNote := coreStatesNote(launcher, game.System, states)
		tbxPost(func() {
			if gmetaUI.open && gmetaUI.gen == gen {
				gmetaUI.coreNote = coreNote
			}
		})
	}
	for _, st := range states {
		if st.Thumb == "" {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		pic, err := gmetaLoadThumb(ctx, st.Thumb)
		if err != nil {
			continue
		}
		path := st.Thumb
		tbxPost(func() {
			if gmetaUI.open && gmetaUI.gen == gen {
				gmetaUI.pending[path] = pic
			}
		})
	}
}

// gmetaLoadLookup matches the game in the libretro database and stores what it
// found. The answer is kept even when the page has closed meanwhile.
func gmetaLoadLookup(ctx context.Context, gen int, game libraryGame, rec gmetaRecord, online, force bool) {
	collection := gmetaCollectionFor(game.System)
	if force {
		gmetaForgetIndex(collection)
	}
	crcs := gmetaROMCRCs(game.Path)
	if rec.CRC != "" && !gmetaContains(crcs, rec.CRC) {
		crcs = append(crcs, rec.CRC)
	}
	idx, res := gmetaIndexFor(ctx, collection, online)
	stem := strings.TrimSuffix(filepath.Base(game.Path), filepath.Ext(game.Path))
	info, found := gmetaLookup(idx, crcs, stem)
	if ctx.Err() != nil && !found {
		return
	}
	tbxPost(func() {
		current := gmetaUI.open && gmetaUI.gen == gen
		switch {
		case found:
			stored := gmetaUpdate(game.System, game.Title, func(r *gmetaRecord) {
				r.Genre, r.Developer, r.Year, r.Players = info.Genre, info.Developer, info.Year, info.Players
				r.Scraped = gmetaStamp()
			})
			if current {
				gmetaUI.rec = stored
				gmetaUI.lookup = ""
			}
		case res == gmetaFetchOK || res == gmetaFetchNone:
			// Looked, and the database has nothing for it: remembered, so the
			// page does not ask again every time it opens (Y still does).
			stored := gmetaUpdate(game.System, game.Title, func(r *gmetaRecord) {
				r.Scraped = gmetaStamp()
			})
			if current {
				gmetaUI.rec = stored
				gmetaUI.lookup = "Not in the libretro database"
			}
		case res == gmetaFetchOffline:
			if current {
				gmetaUI.lookup = "Offline — the genre and year load when you are connected"
			}
		default:
			if current {
				gmetaUI.lookup = "The libretro database did not answer — Y tries again"
			}
		}
	})
}

// gmetaLooseTitle folds a title for comparing names from different sources:
// RetroAchievements writes "Legend of Zelda, The: …" where a ROM says "The
// Legend of Zelda - …", so the articles are dropped as well.
func gmetaLooseTitle(s string) string {
	words := strings.Fields(artworkNormalizeTitle(s))
	kept := words[:0]
	for _, w := range words {
		if w != "the" && w != "a" && w != "an" {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}

// gmetaRAProgress is the game's RetroAchievements line, from the cached sync
// ("" when the user is not signed in).
func gmetaRAProgress(game libraryGame) string {
	if !raSettingsReady() {
		return ""
	}
	raEnsureCacheLoaded()
	if IsOnline() {
		raMaybeRefresh(false)
	}
	raState.mu.Lock()
	progress := raState.cache.Progress
	raState.mu.Unlock()
	want := gmetaLooseTitle(game.Title)
	for _, p := range progress {
		if p.MaxPossible <= 0 || gmetaLooseTitle(p.Title) != want {
			continue
		}
		line := strconv.Itoa(p.NumAwarded) + " of " + strconv.Itoa(p.MaxPossible) + " achievements · " +
			strconv.Itoa(raAwardPercent(p.NumAwarded, p.MaxPossible)) + "%"
		if kind := strings.TrimSpace(p.HighestAwardKind); kind != "" {
			line += " · " + strings.ReplaceAll(kind, "-", " ")
		}
		return line
	}
	return "No RetroAchievements progress for this game yet"
}

// gmetaCloseDetails closes the page and frees its screenshots.
func gmetaCloseDetails(sound bool) {
	if gmetaUI.cancel != nil {
		gmetaUI.cancel()
	}
	for path, tex := range gmetaUI.thumbs {
		if tex != nil {
			tex.Destroy()
		}
		delete(gmetaUI.thumbs, path)
	}
	wasOpen := gmetaUI.open
	gmetaUI = gmetaDetails{}
	gmetaTagUI.open = false
	if sound && wasOpen {
		PlayBackSound()
	}
}

// gmetaReload reads the page's local data again (after the game was played: its
// states and its time changed).
func gmetaReload() {
	if !gmetaUI.open {
		return
	}
	for path, tex := range gmetaUI.thumbs {
		if tex != nil {
			tex.Destroy()
		}
		delete(gmetaUI.thumbs, path)
	}
	if gmetaUI.cancel != nil {
		gmetaUI.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	gmetaUI.cancel = cancel
	gmetaUI.loading = true
	gmetaStartLoad(ctx, gmetaUI.gen, gmetaUI.game, gmetaUI.rec, false, false)
}

// gmetaRefresh is Y: look the game up again, and re-read its states.
func gmetaRefresh() {
	if !gmetaUI.open {
		return
	}
	if !IsOnline() {
		ShowToast(offlinePlainNotice, ToastKindWarning)
	}
	for path, tex := range gmetaUI.thumbs {
		if tex != nil {
			tex.Destroy()
		}
		delete(gmetaUI.thumbs, path)
	}
	if gmetaUI.cancel != nil {
		gmetaUI.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	gmetaUI.cancel = cancel
	gmetaUI.loading = true
	gmetaUI.lookup = "Looking up the genre and year…"
	PlayActivateSound()
	gmetaStartLoad(ctx, gmetaUI.gen, gmetaUI.game, gmetaUI.rec, gmetaLookupEnabled, true)
}

// gmetaPlay starts the game: from its auto state (RetroArch resumes it), or
// from a numbered slot made the auto state first (gmeta_states.go).
func gmetaPlay(st *gmetaState) {
	game := gmetaUI.game
	launcher := resolveGameLauncher(game.System)
	if launcher == "" {
		ShowToast("No emulator found for "+game.System+" — add one on the Emulators screen", ToastKindWarning)
		return
	}
	suffix := ""
	if st != nil {
		if !gmetaUI.resume {
			ShowToast("Starting from a save needs save-state resume (savestateResume in the config)", ToastKindWarning)
			return
		}
		if err := gmetaStageState(*st); err != nil {
			ShowToast("Cannot prepare "+gmetaStateLabel(*st)+": "+firstLine(err.Error()), ToastKindError)
			return
		}
		suffix = " from " + strings.ToLower(gmetaStateLabel(*st))
		if st.Slot < 0 {
			suffix = " where you left off"
		}
	}
	if err := launchLibraryGame(game, launcher); err != nil {
		ShowToast("Cannot launch "+game.Title+": "+err.Error(), ToastKindError)
		return
	}
	gameLib.recents = recentGames()
	gmetaUI.launched = time.Now()
	PlaySuccessSound()
	ShowToast("Playing "+game.Title+suffix, ToastKindSuccess)
}

// gmetaToggleTag adds or removes a tag on the page's game. Adding Completed is
// a milestone.
func gmetaToggleTag(tag string) {
	g := gmetaUI.game
	on := !gmetaHasTag(gmetaUI.rec, tag)
	gmetaUI.rec = gmetaSetTag(g.System, g.Title, tag, on)
	if on {
		PlayActivateSound()
		ShowToast(g.Title+" · "+tag, ToastKindSuccess)
	} else {
		PlayBackSound()
		ShowToast(g.Title+" · no longer "+tag, ToastKindInfo)
	}
	if on && tag == gmetaTagCompleted {
		celebrate := false
		gmetaUI.rec = gmetaUpdate(g.System, g.Title, func(r *gmetaRecord) {
			if r.Milestones == nil {
				r.Milestones = map[string]string{}
			}
			if r.Milestones[gmetaMilestoneCompleted] == "" {
				r.Milestones[gmetaMilestoneCompleted] = gmetaStamp()
				celebrate = true
			}
		})
		if m, ok := gmetaMilestoneByID(gmetaMilestoneCompleted); ok && celebrate {
			gmetaCelebrate(m, g.Title)
		}
	}
}

// gmetaCycleRating steps the rating 1 … 5 and back to unrated.
func gmetaCycleRating() {
	g := gmetaUI.game
	gmetaUI.rec = gmetaUpdate(g.System, g.Title, func(r *gmetaRecord) {
		r.Rating = (r.Rating + 1) % 6
	})
	PlayNavSound()
	if gmetaUI.rec.Rating == 0 {
		ShowToast("Rating cleared", ToastKindInfo)
		return
	}
	ShowToast("Rated "+strconv.Itoa(gmetaUI.rec.Rating)+" of 5", ToastKindInfo)
}

// gmetaEditText opens the keyboard for the description or the notes.
func gmetaEditText(notes bool) {
	g := gmetaUI.game
	gen := gmetaUI.gen
	title, placeholder, seed := "Description", "What is this game about?", gmetaUI.rec.Description
	if notes {
		title, placeholder, seed = "Notes", "Where you are, codes, what to try next…", gmetaUI.rec.Notes
	}
	PlayActivateSound()
	tbxOpenPrompt(title, placeholder, seed, func(text string) {
		stored := gmetaUpdate(g.System, g.Title, func(r *gmetaRecord) {
			if notes {
				r.Notes = text
			} else {
				r.Description = text
			}
		})
		if gmetaUI.open && gmetaUI.gen == gen {
			gmetaUI.rec = stored
		}
		if text == "" {
			ShowToast(title+" cleared", ToastKindInfo)
			return
		}
		ShowToast(title+" saved", ToastKindSuccess)
	})
}

// gmetaActivateButton presses one of the page's buttons.
func gmetaActivateButton(i int) {
	switch i {
	case gmetaBtnPlay:
		gmetaPlay(nil)
	case gmetaBtnFavorite:
		gmetaToggleTag(gmetaTagFavorite)
	case gmetaBtnTags:
		gmetaOpenTags()
	case gmetaBtnRate:
		gmetaCycleRating()
	case gmetaBtnAbout:
		gmetaEditText(false)
	case gmetaBtnNotes:
		gmetaEditText(true)
	}
}

// gmetaDetailsAction is the page's input.
func gmetaDetailsAction(act tbxAction) {
	switch act {
	case tbxActBack:
		gmetaCloseDetails(true)
	case tbxActUp:
		if gmetaUI.zone == 0 && len(gmetaUI.states) > 0 {
			gmetaUI.zone = 1
			PlayNavSound()
		}
	case tbxActDown:
		if gmetaUI.zone == 1 {
			gmetaUI.zone = 0
			PlayNavSound()
		}
	case tbxActLeft, tbxActRight:
		step := 1
		if act == tbxActLeft {
			step = -1
		}
		if gmetaUI.zone == 1 {
			if next := gmetaUI.state + step; next >= 0 && next < len(gmetaUI.states) {
				gmetaUI.state = next
				PlayNavSound()
			}
			return
		}
		if next := gmetaUI.button + step; next >= 0 && next < gmetaBtnCount {
			gmetaUI.button = next
			PlayNavSound()
		}
	case tbxActPageUp, tbxActPageDown:
		if gmetaUI.zone == 1 && len(gmetaUI.states) > 0 {
			if act == tbxActPageUp {
				gmetaUI.state = 0
			} else {
				gmetaUI.state = len(gmetaUI.states) - 1
			}
			PlayNavSound()
		}
	case tbxActConfirm:
		if gmetaUI.zone == 1 && gmetaUI.state >= 0 && gmetaUI.state < len(gmetaUI.states) {
			st := gmetaUI.states[gmetaUI.state]
			gmetaPlay(&st)
			return
		}
		gmetaActivateButton(gmetaUI.button)
	case tbxActX:
		gmetaToggleTag(gmetaTagFavorite)
	case tbxActY:
		gmetaRefresh()
	}
}

// ── The tag sheet ────────────────────────────────────────────────────────────

// gmetaOpenTags opens the tag sheet over the page.
func gmetaOpenTags() {
	gmetaTagUI.open = true
	gmetaTagUI.tags = gmetaAllTags()
	gmetaTagUI.cursor = 0
	PlayActivateSound()
}

// gmetaNewTag asks for a new tag's name, makes it and puts it on the game.
func gmetaNewTag() {
	g := gmetaUI.game
	gen := gmetaUI.gen
	tbxOpenPrompt("New tag", "Co-op, RPG, For the kids …", "", func(text string) {
		name := gmetaAddTag(text)
		if name == "" {
			if strings.TrimSpace(text) != "" {
				ShowToast("There are already "+strconv.Itoa(gmetaMaxTags)+" custom tags", ToastKindWarning)
			}
			return
		}
		stored := gmetaSetTag(g.System, g.Title, name, true)
		if gmetaUI.open && gmetaUI.gen == gen {
			gmetaUI.rec = stored
			gmetaTagUI.tags = gmetaAllTags()
			for i, t := range gmetaTagUI.tags {
				if t == name {
					gmetaTagUI.cursor = i
				}
			}
		}
		ShowToast(g.Title+" · "+name, ToastKindSuccess)
	})
}

// gmetaTagAction is the tag sheet's input. The last row makes a new tag.
func gmetaTagAction(act tbxAction) {
	n := len(gmetaTagUI.tags) + 1
	switch act {
	case tbxActUp:
		if gmetaTagUI.cursor > 0 {
			gmetaTagUI.cursor--
			PlayNavSound()
		}
	case tbxActDown:
		if gmetaTagUI.cursor < n-1 {
			gmetaTagUI.cursor++
			PlayNavSound()
		}
	case tbxActConfirm:
		if gmetaTagUI.cursor < len(gmetaTagUI.tags) {
			gmetaToggleTag(gmetaTagUI.tags[gmetaTagUI.cursor])
			return
		}
		gmetaNewTag()
	case tbxActBack:
		gmetaTagUI.open = false
		PlayBackSound()
	}
}

// ── The filter chooser ───────────────────────────────────────────────────────

// gmetaOpenFilterPicker is L3 / T in the library: choose a tag to show only its
// games, or All games.
func gmetaOpenFilterPicker() bool {
	if gameOptsUI.open || gameRandomUI.open || gmetaUI.open {
		return false
	}
	counts := gmetaTagCounts(gameLib.lib.Systems)
	rows := []gmetaPickRow{{label: "All games", count: gameLib.lib.Total}}
	cursor := 0
	for _, t := range gmetaAllTags() {
		c := counts[t]
		if c == 0 && !gmetaIsBuiltin(t) {
			continue
		}
		if t == gmetaFilter.tag {
			cursor = len(rows)
		}
		rows = append(rows, gmetaPickRow{tag: t, label: t, count: c})
	}
	gmetaPick.open = true
	gmetaPick.rows = rows
	gmetaPick.cursor = cursor
	PlayActivateSound()
	return true
}

// gmetaPickChoose applies the highlighted filter.
func gmetaPickChoose() {
	if gmetaPick.cursor < 0 || gmetaPick.cursor >= len(gmetaPick.rows) {
		return
	}
	row := gmetaPick.rows[gmetaPick.cursor]
	if row.tag != "" && row.count == 0 {
		ShowToast("No game is tagged "+row.label+" yet — open a game and press Tags", ToastKindInfo)
		return
	}
	gmetaPick.open = false
	gmetaSetFilter(row.tag)
	PlayActivateSound()
}

// gmetaPickAction is the chooser's input.
func gmetaPickAction(act tbxAction) {
	switch act {
	case tbxActUp:
		if gmetaPick.cursor > 0 {
			gmetaPick.cursor--
			PlayNavSound()
		}
	case tbxActDown:
		if gmetaPick.cursor < len(gmetaPick.rows)-1 {
			gmetaPick.cursor++
			PlayNavSound()
		}
	case tbxActConfirm:
		gmetaPickChoose()
	case tbxActBack:
		gmetaPick.open = false
		PlayBackSound()
	}
}

// ── Input routing (the library calls these while gmetaSheetOpen) ────────────

// gmetaHandleKey routes a key to the chooser, the tag sheet or the page.
func gmetaHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	if gmetaPick.open {
		gmetaPickAction(tbxKeyAction(e))
		return true
	}
	if !gmetaUI.open {
		return false
	}
	if gmetaTagUI.open {
		gmetaTagAction(tbxKeyAction(e))
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_t:
		gmetaOpenTags()
		return true
	case sdl.K_d:
		gmetaEditText(false)
		return true
	case sdl.K_n:
		gmetaEditText(true)
		return true
	case sdl.K_o:
		openGameOptions()
		return true
	case sdl.K_f:
		gmetaToggleTag(gmetaTagFavorite)
		return true
	}
	gmetaDetailsAction(tbxKeyAction(e))
	return true
}

// gmetaHandlePad routes a controller press the same way. Start opens the
// game's options sheet over the page.
func gmetaHandlePad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	if gmetaPick.open {
		gmetaPickAction(tbxPadAction(e))
		return true
	}
	if !gmetaUI.open {
		return false
	}
	if gmetaTagUI.open {
		gmetaTagAction(tbxPadAction(e))
		return true
	}
	if e.Button == sdl.CONTROLLER_BUTTON_START {
		openGameOptions()
		return true
	}
	gmetaDetailsAction(tbxPadAction(e))
	return true
}

// gmetaHandleMouse routes a click or a tap: rows and buttons act, a click
// outside a sheet closes it.
func gmetaHandleMouse(mx, my int32, config *Config) bool {
	if gmetaPick.open {
		for _, hit := range gmetaPick.hits {
			if pointInRect(mx, my, hit.rect) {
				gmetaPick.cursor = hit.index
				gmetaPickChoose()
				return true
			}
		}
		if gmetaPick.card.W == 0 || !pointInRect(mx, my, gmetaPick.card) {
			gmetaPick.open = false
			PlayBackSound()
		}
		return true
	}
	if !gmetaUI.open {
		return false
	}
	if gmetaTagUI.open {
		for _, hit := range gmetaTagUI.rows {
			if pointInRect(mx, my, hit.rect) {
				gmetaTagUI.cursor = hit.index
				gmetaTagAction(tbxActConfirm)
				return true
			}
		}
		if gmetaTagUI.card.W == 0 || !pointInRect(mx, my, gmetaTagUI.card) {
			gmetaTagUI.open = false
			PlayBackSound()
		}
		return true
	}
	for i, r := range gmetaUI.btnRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			gmetaUI.zone, gmetaUI.button = 0, i
			gmetaActivateButton(i)
			return true
		}
	}
	for _, hit := range gmetaUI.stateHits {
		if pointInRect(mx, my, hit.rect) && hit.index < len(gmetaUI.states) {
			gmetaUI.zone, gmetaUI.state = 1, hit.index
			st := gmetaUI.states[hit.index]
			gmetaPlay(&st)
			return true
		}
	}
	if gmetaUI.card.W > 0 && pointInRect(mx, my, gmetaUI.card) {
		return true
	}
	gmetaCloseDetails(true)
	return true
}

// ── Drawing ─────────────────────────────────────────────────────────────────

// gmetaRender draws whichever sheets are open, over the library.
func gmetaRender(renderer *sdl.Renderer, config *Config) {
	if renderer == nil {
		return
	}
	if gmetaUI.open {
		gmetaRenderDetails(renderer, config)
		if gmetaTagUI.open {
			gmetaRenderTags(renderer, config)
		}
	}
	if gmetaPick.open {
		gmetaRenderPick(renderer, config)
	}
}

// gmetaMetaLine is "Platformer · Nintendo · 1991 · 2 players".
func gmetaMetaLine(rec gmetaRecord) string {
	parts := make([]string, 0, 4)
	for _, p := range []string{rec.Genre, rec.Developer, rec.Year, gmetaPlayersLabel(rec.Players)} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// gmetaButtonLabel is a button's label for the page's record.
func gmetaButtonLabel(i int) string {
	switch i {
	case gmetaBtnPlay:
		return "Play"
	case gmetaBtnFavorite:
		if gmetaHasTag(gmetaUI.rec, gmetaTagFavorite) {
			return "Favorited"
		}
		return "Favorite"
	case gmetaBtnTags:
		return "Tags"
	case gmetaBtnRate:
		if gmetaUI.rec.Rating > 0 {
			return "Rated " + strconv.Itoa(gmetaUI.rec.Rating) + "/5"
		}
		return "Rate"
	case gmetaBtnAbout:
		return "Description"
	case gmetaBtnNotes:
		return "Notes"
	}
	return ""
}

// gmetaStripHeight is the save-state strip's height for fonts.
func gmetaStripHeight(fonts tbxFonts) int32 {
	if !gmetaUI.loading && len(gmetaUI.states) == 0 {
		return fonts.tinyH + 8 + fonts.smallH
	}
	return fonts.tinyH + 8 + gmetaCardThumbH + 8 + fonts.smallH + 2 + fonts.tinyH + 8
}

// The save-state cards: a 16:9 screenshot over the slot and its time.
const (
	gmetaCardW      = int32(176)
	gmetaCardThumbH = int32(99)
)

// gmetaRenderDetails draws the page.
func gmetaRenderDetails(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	// Screenshots decoded since the last frame become textures here, on the
	// render thread.
	for path, pic := range gmetaUI.pending {
		if tex := imgvTexture(renderer, pic); tex != nil {
			gmetaUI.thumbs[path] = tex
		}
		delete(gmetaUI.pending, path)
	}
	if !gmetaUI.launched.IsZero() && time.Since(gmetaUI.launched) > 3*time.Second {
		// The loop was parked while the game ran: its states and its time
		// moved, so they are read again.
		gmetaUI.launched = time.Time{}
		gmetaReload()
	}
	g := gmetaUI.game
	cardW := int32(1100)
	if cardW > screenWidth-48 {
		cardW = screenWidth - 48
	}
	cardH := screenHeight - 56
	if cardH < 400 {
		cardH = screenHeight - 16
	}
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	gmetaUI.card = card
	sub := g.System + " · " + filepath.Base(g.Path)
	header := UIPanelHeader(g.Label(), sub, iconEmuGlyphFor(g.System))
	headerH := UIPanelHeaderBand(config, header, 12)
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	const pad = OverlayPadX
	innerX := card.X + pad
	innerW := card.W - 2*pad
	hintY := card.Y + card.H - fonts.smallH - 14
	btnH := int32(46)
	btnY := hintY - 14 - btnH
	topY := card.Y + headerH
	stripH := int32(0)
	if gmetaUI.loading || gmetaUI.isRA {
		stripH = gmetaStripHeight(fonts)
	}
	stripY := btnY - 16 - stripH
	if stripH > 0 && stripY-16-topY < 140 {
		stripH = 0 // a short canvas keeps the details and the buttons
	}
	topH := btnY - 16 - topY
	if stripH > 0 {
		topH = stripY - 16 - topY
	}
	artSize := min(int32(300), topH, innerW*3/10)
	if artSize < 96 {
		artSize = 0
	}
	infoX := innerX
	if artSize > 0 {
		drawLibraryArt(renderer, config, sdl.Rect{X: innerX, Y: topY, W: artSize, H: artSize}, g)
		infoX = innerX + artSize + 24
	}
	gmetaDrawInfo(renderer, config, fonts, sdl.Rect{X: infoX, Y: topY, W: innerX + innerW - infoX, H: topH})
	if stripH > 0 {
		gmetaDrawStates(renderer, config, fonts, sdl.Rect{X: innerX, Y: stripY, W: innerW, H: stripH})
	} else {
		gmetaUI.stateHits = gmetaUI.stateHits[:0]
	}
	gmetaDrawButtons(renderer, config, fonts, innerX, btnY, innerW, btnH)

	var hint string
	if gmetaUI.zone == 1 && len(gmetaUI.states) > 0 {
		hint = hintLine(FooterHint{"A", "Play from this save"}, FooterHint{"Left/Right", "Choose"},
			FooterHint{"Down", "Buttons"}, FooterHint{"B", "Back"})
	} else {
		// "/ O": the keyboard opens the options with O (gmetaHandleKey).
		hints := []FooterHint{{"A", gmetaButtonLabel(gmetaUI.button)}, {"X", "Favorite"}, {"Y", "Look up again"},
			{"Start / O", "Options"}}
		if len(gmetaUI.states) > 0 {
			hints = append(hints, FooterHint{"Up", "Saves"})
		}
		hint = hintLine(append(hints, FooterHint{"B", "Back"})...)
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, innerW), ColorTextSecondary(), innerX, hintY)
}

// gmetaDrawInfo draws the right-hand column: the details line, the rating and
// tags, the description, then the playtime, achievements and milestones at the
// bottom.
func gmetaDrawInfo(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect) {
	if r.W < 80 || r.H < 60 {
		return
	}
	rec := gmetaUI.rec
	y := r.Y
	meta := gmetaMetaLine(rec)
	metaInk := ColorTextSecondary()
	if meta == "" {
		meta, metaInk = gmetaUI.lookup, ColorTextTertiary()
	}
	if meta != "" {
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, meta, r.W), metaInk, r.X, y)
	}
	y += fonts.bodyH + 12

	// Rating and tags share a line: five stars, then a chip per tag.
	rowH := max32(fonts.tinyH+8, 20)
	cy := y + rowH/2
	x := r.X
	const star = int32(9)
	for i := 1; i <= 5; i++ {
		ink := glassA(60)
		if i <= rec.Rating {
			ink = glassAccent()
		}
		fillStar(renderer, x+star, cy, star, ink)
		x += 2*star + 4
	}
	x += 12
	for i, t := range rec.Tags {
		tw, th := measureText(fonts.tiny, t)
		w := tw + 20
		if x+w > r.X+r.W {
			more := "+" + strconv.Itoa(len(rec.Tags)-i)
			renderText(renderer, config, fonts.tiny, more, ColorTextTertiary(), x, cy-th/2)
			break
		}
		h := th + 8
		fillRoundedRect(renderer, x, cy-h/2, w, h, h/2, WithAlpha(glassAccent(), 40))
		strokeRoundedRect(renderer, x, cy-h/2, w, h, h/2, 1, WithAlpha(glassAccent(), 110))
		renderText(renderer, config, fonts.tiny, t, ColorTextPrimary(), x+10, cy-th/2)
		x += w + 8
	}
	y += rowH + 14

	// The bottom block: playtime, achievements, milestones.
	lineH := fonts.smallH + 6
	bottomH := lineH + fonts.tinyH + 10
	if gmetaUI.ra != "" {
		bottomH += lineH
	}
	bottomY := r.Y + r.H - bottomH

	// The description fills what is left; an empty one says how to write it.
	desc := strings.TrimSpace(rec.Description)
	descInk := ColorTextSecondary()
	if desc == "" {
		desc, descInk = "No description yet — press Description (or D) to write one.", ColorTextTertiary()
	}
	if notes := strings.TrimSpace(rec.Notes); notes != "" {
		desc += "\n\nNotes: " + notes
	}
	textLineH := fonts.smallH + 3
	if maxLines := int((bottomY - 10 - y) / textLineH); maxLines > 0 {
		lines := gmetaUI.wrap.get(fonts.small, desc, r.W)
		for i, line := range lines {
			if i >= maxLines {
				break
			}
			if i == maxLines-1 && i < len(lines)-1 {
				line = ellipsize(fonts.small, line+" …", r.W)
			}
			renderText(renderer, config, fonts.small, line, descInk, r.X, y)
			y += textLineH
		}
	}

	y = bottomY
	secs := playtime.data.Games[gmetaUI.game.Title]
	played := "Not played on this device yet"
	if secs > 0 {
		played = "Played " + formatPlaytime(secs)
	}
	for _, it := range gameLib.recents {
		if it.Path == gmetaUI.game.Path && !it.PlayedAt.IsZero() {
			played += " · last played " + gmetaWhen(it.PlayedAt, time.Now())
			break
		}
	}
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, played, r.W), ColorTextSecondary(), r.X, y)
	y += lineH
	if gmetaUI.ra != "" {
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, "RetroAchievements: "+gmetaUI.ra, r.W),
			ColorTextSecondary(), r.X, y)
		y += lineH
	}
	// The milestones: a filled dot for each one earned on this device.
	x = r.X
	cy = y + fonts.tinyH/2 + 2
	for _, m := range gmetaMilestones {
		label := m.Short
		tw, th := measureText(fonts.tiny, label)
		if x+12+tw > r.X+r.W {
			break
		}
		earned := rec.Milestones[m.ID] != ""
		if earned {
			fillCircle(renderer, x+5, cy, 5, glassAccent())
			renderText(renderer, config, fonts.tiny, label, ColorTextSecondary(), x+14, cy-th/2)
		} else {
			fillCircle(renderer, x+5, cy, 5, glassA(40))
			renderText(renderer, config, fonts.tiny, label, ColorTextTertiary(), x+14, cy-th/2)
		}
		x += 14 + tw + 16
	}
}

// gmetaDrawStates draws the save-state strip.
func gmetaDrawStates(renderer *sdl.Renderer, config *Config, fonts tbxFonts, r sdl.Rect) {
	gmetaUI.stateHits = gmetaUI.stateHits[:0]
	renderText(renderer, config, fonts.tiny, "SAVE STATES", ColorTextTertiary(), r.X, r.Y)
	y := r.Y + fonts.tinyH + 8
	if gmetaUI.loading {
		renderText(renderer, config, fonts.small, "Looking for save states…", ColorTextTertiary(), r.X, y)
		return
	}
	states := gmetaUI.states
	if len(states) == 0 {
		renderText(renderer, config, fonts.small,
			ellipsize(fonts.small, "No save states yet — save in RetroArch's menu and they show up here.", r.W),
			ColorTextTertiary(), r.X, y)
		return
	}
	const gap = int32(12)
	visible := int((r.W + gap) / (gmetaCardW + gap))
	if visible < 1 {
		visible = 1
	}
	if gmetaUI.state < gmetaUI.first {
		gmetaUI.first = gmetaUI.state
	}
	if gmetaUI.state >= gmetaUI.first+visible {
		gmetaUI.first = gmetaUI.state - visible + 1
	}
	if gmetaUI.first > len(states)-visible {
		gmetaUI.first = max(0, len(states)-visible)
	}
	cardH := r.Y + r.H - y
	now := time.Now()
	x := r.X
	for i := gmetaUI.first; i < len(states) && i < gmetaUI.first+visible; i++ {
		st := states[i]
		rect := sdl.Rect{X: x, Y: y, W: gmetaCardW, H: cardH}
		focused := gmetaUI.zone == 1 && gmetaUI.state == i
		UIListRow(renderer, rect, 12, focused, false)
		thumb := sdl.Rect{X: rect.X + 6, Y: rect.Y + 6, W: rect.W - 12, H: gmetaCardThumbH - 6}
		fillRoundedRect(renderer, thumb.X, thumb.Y, thumb.W, thumb.H, 8, WithAlpha(ColorBackground, 200))
		if tex := gmetaUI.thumbs[st.Thumb]; tex != nil {
			if _, _, tw, th, err := tex.Query(); err == nil && tw > 0 && th > 0 {
				dw, dh := thumb.W, int32(int64(thumb.W)*int64(th)/int64(tw))
				if dh > thumb.H {
					dh, dw = thumb.H, int32(int64(thumb.H)*int64(tw)/int64(th))
				}
				dst := sdl.Rect{X: thumb.X + (thumb.W-dw)/2, Y: thumb.Y + (thumb.H-dh)/2, W: dw, H: dh}
				renderer.Copy(tex, nil, &dst)
			}
		} else {
			icon := thumb.H / 3
			DrawHomeIcon(renderer, "disc", sdl.Rect{X: thumb.X + (thumb.W-icon)/2, Y: thumb.Y + (thumb.H-icon)/2, W: icon, H: icon}, false)
		}
		ty := rect.Y + gmetaCardThumbH + 6
		renderText(renderer, config, fonts.small, ellipsize(fonts.small, gmetaStateLabel(st), rect.W-16), ColorTextPrimary(), rect.X+8, ty)
		renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, gmetaWhen(st.Time, now), rect.W-16), ColorTextTertiary(),
			rect.X+8, ty+fonts.smallH+2)
		gmetaUI.stateHits = append(gmetaUI.stateHits, gameLibRowHit{index: i, rect: rect})
		x += gmetaCardW + gap
	}
	right := r.X + r.W
	if more := len(states) - (gmetaUI.first + visible); more > 0 {
		label := strconv.Itoa(more) + " more ›"
		lw, _ := measureText(fonts.tiny, label)
		renderText(renderer, config, fonts.tiny, label, ColorTextTertiary(), r.X+r.W-lw, r.Y)
		right -= lw + 16
	}
	// States made with a core build that has since been replaced may not
	// load: one line beside the heading says so (corebuilds_history.go).
	if note := gmetaUI.coreNote; note != "" {
		hw, _ := measureText(fonts.tiny, "SAVE STATES")
		x := r.X + hw + 16
		if w := right - x; w > 80 {
			renderText(renderer, config, fonts.tiny, ellipsize(fonts.tiny, note, w), WarningInk(), x, r.Y)
		}
	}
}

// gmetaDrawButtons draws the button row; a narrow canvas gets the smaller type
// before anything is left out.
func gmetaDrawButtons(renderer *sdl.Renderer, config *Config, fonts tbxFonts, x, y, w, h int32) {
	const gap = int32(10)
	font, minW := fonts.body, int32(110)
	total := func() int32 {
		sum := int32(0)
		for i := 0; i < gmetaBtnCount; i++ {
			sum += tbxButtonWidth(font, gmetaButtonLabel(i), minW) + gap
		}
		return sum - gap
	}
	if total() > w {
		font, minW = fonts.small, 84
	}
	bx := x
	for i := 0; i < gmetaBtnCount; i++ {
		label := gmetaButtonLabel(i)
		bw := tbxButtonWidth(font, label, minW)
		if bx+bw > x+w {
			gmetaUI.btnRects[i] = sdl.Rect{}
			continue
		}
		rect := sdl.Rect{X: bx, Y: y, W: bw, H: h}
		gmetaUI.btnRects[i] = rect
		focused := gmetaUI.zone == 0 && gmetaUI.button == i && !gmetaTagUI.open
		tbxDrawButton(renderer, config, font, rect, label, i == gmetaBtnPlay, focused, false)
		bx += bw + gap
	}
}

// gmetaRenderTags draws the tag sheet: a row per tag with its box ticked when
// the game carries it, and "New tag…" last.
func gmetaRenderTags(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	header := UIPanelHeader("Tags", gmetaUI.game.Label(), "filter")
	headerH := UIPanelHeaderBand(config, header, 10)
	rowH := int32(48)
	n := len(gmetaTagUI.tags) + 1
	footerH := fonts.smallH + 20
	cardW := min(int32(560), screenWidth-64)
	maxH := screenHeight - 48
	cardH := min(headerH+int32(n)*rowH+footerH+8, maxH)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	gmetaTagUI.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	listY := card.Y + headerH
	visible := int((card.Y + card.H - footerH - 8 - listY) / rowH)
	if visible < 1 {
		visible = 1
	}
	first := 0
	if gmetaTagUI.cursor >= visible {
		first = gmetaTagUI.cursor - visible + 1
	}
	gmetaTagUI.rows = gmetaTagUI.rows[:0]
	for i := first; i < n && i < first+visible; i++ {
		rect := sdl.Rect{X: card.X + OverlayPadX, Y: listY + int32(i-first)*rowH, W: card.W - 2*OverlayPadX, H: rowH - 6}
		focused := i == gmetaTagUI.cursor
		UIListRow(renderer, rect, glassRowRadius, focused, false)
		box := sdl.Rect{X: rect.X + 14, Y: rect.Y + (rect.H-22)/2, W: 22, H: 22}
		label := "New tag…"
		if i < len(gmetaTagUI.tags) {
			label = gmetaTagUI.tags[i]
			if gmetaHasTag(gmetaUI.rec, label) {
				fillRoundedRect(renderer, box.X, box.Y, box.W, box.H, 6, glassAccent())
				drawHomeGlyphTinted(renderer, "check", insetRect(box, 4), ColorIconDark)
			} else {
				strokeRoundedRect(renderer, box.X, box.Y, box.W, box.H, 6, 2, glassA(90))
			}
		} else {
			DrawHomeIcon(renderer, "plus", box, false)
		}
		_, th := measureText(fonts.body, label)
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, label, rect.W-60), ColorTextPrimary(),
			box.X+box.W+14, rect.Y+(rect.H-th)/2)
		gmetaTagUI.rows = append(gmetaTagUI.rows, gameLibRowHit{index: i, rect: rect})
	}
	hint := hintLine(FooterHint{"A", "Tick / untick"}, FooterHint{"B", "Done"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}

// gmetaRenderPick draws the library's tag filter chooser.
func gmetaRenderPick(renderer *sdl.Renderer, config *Config) {
	fonts, ok := tbxLoadFonts(config)
	if !ok {
		return
	}
	header := UIPanelHeader("Show", "Only the games with one tag, or all of them", "filter")
	headerH := UIPanelHeaderBand(config, header, 10)
	rowH := int32(48)
	n := len(gmetaPick.rows)
	footerH := fonts.smallH + 20
	cardW := min(int32(520), screenWidth-64)
	cardH := min(headerH+int32(n)*rowH+footerH+8, screenHeight-48)
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	gmetaPick.card = card
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	drawOverlayCardAccent(renderer, card.X, card.Y, card.W)
	UIPageHeaderDraw(renderer, config, overlayHeaderOnCard(header, card))

	listY := card.Y + headerH
	visible := int((card.Y + card.H - footerH - 8 - listY) / rowH)
	if visible < 1 {
		visible = 1
	}
	first := 0
	if gmetaPick.cursor >= visible {
		first = gmetaPick.cursor - visible + 1
	}
	gmetaPick.hits = gmetaPick.hits[:0]
	for i := first; i < n && i < first+visible; i++ {
		row := gmetaPick.rows[i]
		rect := sdl.Rect{X: card.X + OverlayPadX, Y: listY + int32(i-first)*rowH, W: card.W - 2*OverlayPadX, H: rowH - 6}
		UIListRow(renderer, rect, glassRowRadius, i == gmetaPick.cursor, false)
		count := strconv.Itoa(row.count)
		cw, ch := measureText(fonts.small, count)
		renderText(renderer, config, fonts.small, count, ColorTextTertiary(), rect.X+rect.W-14-cw, rect.Y+(rect.H-ch)/2)
		label := row.label
		if row.tag == gmetaFilter.tag {
			label += "  ·  showing"
		}
		_, th := measureText(fonts.body, label)
		ink := ColorTextPrimary()
		if row.tag != "" && row.count == 0 {
			ink = ColorTextTertiary()
		}
		renderText(renderer, config, fonts.body, ellipsize(fonts.body, label, rect.W-cw-48), ink, rect.X+14, rect.Y+(rect.H-th)/2)
		gmetaPick.hits = append(gmetaPick.hits, gameLibRowHit{index: i, rect: rect})
	}
	hint := hintLine(FooterHint{"A", "Show"}, FooterHint{"B", "Close"})
	renderText(renderer, config, fonts.small, ellipsize(fonts.small, hint, card.W-2*OverlayPadX), ColorTextSecondary(),
		card.X+OverlayPadX, card.Y+card.H-footerH+4)
}

// ── Milestone tick ──────────────────────────────────────────────────────────

// gmetaTickState is what the tick saw last time.
var gmetaTickState struct {
	rev  uint64
	at   time.Time
	path string
	secs int64
}

// gmetaTick runs every frame from the main loop. It is a compare unless the
// playtime ledger moved, and touches the store only when the last game played
// has an hour or more on it: then the hour milestones it crossed are recorded
// and celebrated (gmetaCheckPlaytime).
func gmetaTick(config *Config) {
	if !playtime.loaded || playtime.rev == gmetaTickState.rev {
		return
	}
	gmetaTickState.rev = playtime.rev
	now := time.Now()
	if now.Sub(gmetaTickState.at) < 2*time.Second {
		return
	}
	gmetaTickState.at = now
	recent := recentGames()
	if len(recent) == 0 {
		return
	}
	last := recent[0]
	secs := playtime.data.Games[last.Title]
	if last.Path == gmetaTickState.path && secs == gmetaTickState.secs {
		return
	}
	gmetaTickState.path, gmetaTickState.secs = last.Path, secs
	if len(gmetaMilestones) < 2 || secs < gmetaMilestones[1].Seconds {
		return
	}
	gmetaCheckPlaytime(libraryGame{Path: last.Path, System: last.System, Title: last.Title}, secs)
}
