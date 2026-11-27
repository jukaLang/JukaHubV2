package main

// autocoll.go — the automatic collections of the Tabs Home's Collections tab
// (EmulationStation's "auto collections" on Batocera), the small
// store two of them need, and the play count on the game page.
//
// The collections are read-only folders built from the library Home already
// holds (homeTabsLibrary, hidden games left out the way the Games library
// leaves them out), so nothing here scans the card:
//
//   - Last played: the games started last, newest first. The order is a
//     launch counter kept here (game-seen.json "played"), never the clock: a
//     TrimUI boots before NTP, and a time from before the clock was set would
//     put an old game first. Games started before the counter existed follow
//     in the recents' order.
//   - Most played: the most hours, from the playtime ledger.
//   - All games: every console in one list, A to Z.
//   - Recently added: the games JukaHub met last, by the library scan they
//     first turned up in (game-seen.json "seen"), never by the file's time —
//     the clock again, and a card filled from a PC keeps the PC's times. A
//     game is known by its content key (the cartridge's bytes), so a renamed
//     or moved file is not "new". The games there when the store was made are
//     its baseline and never "recent".
//   - Never played: no playtime, no launch, not in the recents.
//   - Multiplayer: the games whose details (the game page's libretro lookup,
//     gmeta) say two players or more.
//   - a folder per genre the details know for three games or more.
//   - a folder per decade (1980s, 1990s…) and per player count (1, 2, 3+),
//     from the same details: the year and players fields gmeta_dat.go reads.
//     A game can sit in a genre, a decade and a player-count folder at once;
//     the folders are views over one listing, not copies of it.
//
// The grouping itself is autoCollGroupGames: one pass over the listing and one
// metadata lookup, free of the store and the caches, so a test can feed it a
// fixture and call it again when the details arrive late.
//
// They sit on the Collections tab above the user's own collections, each with
// its own remembered view ("col:<id>", like any collection). Settings → Games
// & emulators → Automatic collections turns each on or off (Custom
// "autoCollections", a comma list; unset means Last played, Most played, All
// games, Recently added and Never played — the last two were on the tab
// before the switches). Off only takes the folder off the tab: a favourited
// one still opens.
//
// The store is data/game-seen.json:
//
//	{"version": 1, "based": true, "scans": 4, "plays": 87,
//	 "seen":   {"<key>": 0, "<key>": 4},
//	 "played": {"/mnt/SDCARD/Roms/GBA/Kirby.gba": 87}}
//
// "seen" maps a game's key (a 64-bit hash of its content key) to the scan
// that first listed it, 0 for the baseline; "played" maps a ROM path to the
// launch counter at its last start (the newest autoCollPlayedMax kept).
// libIndexSaveLibrary hands every finished library build to
// autoCollNoteLibrary, off the render thread; a launch is counted by a
// Started hook (gamelaunch_hooks.go). Writes are atomic and ordered, newest
// snapshot last.
//
// "Played 5 times" on the game page is game_history.go's launch count, which
// every launch already records; this file only shows it.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	// autoCollSettingKey is the Custom key of the switches.
	autoCollSettingKey = "autoCollections"
	// autoCollFileName is the store in the data folder.
	autoCollFileName    = "game-seen.json"
	autoCollFileVersion = 1
	// Bounds: the games remembered as seen, the launches remembered, the
	// length of a ranked folder and of Never played, how many games a genre
	// needs and how many genre folders there can be.
	autoCollSeenMax     = 50000
	autoCollPlayedMax   = 200
	autoCollListMax     = 30
	autoCollUnplayedMax = 120
	autoCollGenreMin    = 3
	autoCollGenreMax    = 24
	autoCollGenreName   = 40
	// Decades and player counts are few by nature (a dozen decades at most,
	// three counts), so a folder holding one game is still worth showing when
	// its switch is on; the cap only guards a nonsense year or count.
	autoCollSmartMin = 1
	autoCollSmartMax = 24
	autoCollYearMin  = 1900
	autoCollYearMax  = 2100
)

// The folder ids added here; Most played, Recently added and Never played
// keep the ids hometabs_collections.go gave them, so a favourite of one
// still resolves.
const (
	autoCollIDLast       = "auto-last"
	autoCollIDAll        = "auto-all"
	autoCollIDBacklog    = "auto-backlog"
	autoCollIDPlaying    = "auto-playing"
	autoCollIDBeaten     = "auto-beaten"
	autoCollIDMulti      = "auto-multi"
	autoCollGenrePrefix  = "auto-genre-"
	autoCollDecadePrefix = "auto-decade-"
	autoCollPlayerPrefix = "auto-players-"
)

// autoCollSwitch is one switch of the Settings sheet: the name stored in
// the Custom list, its label and line, and whether it is on by default.
type autoCollSwitch struct {
	name, label, note string
	on                bool
}

// autoCollSwitches in the order the tab lists their folders.
var autoCollSwitches = []autoCollSwitch{
	{"last", "Last played", "The games you started last, newest first", true},
	{"played", "Most played", "The games with the most hours", true},
	{"all", "All games", "Every console in one list, A to Z", true},
	// On by default: the Collections tab showed both (with Most played)
	// before the switches existed, so an update does not take them away.
	{"added", "Recently added", "The games that turned up on the card last", true},
	{"unplayed", "Never played", "The games you have not started yet", true},
	{"backlog", "Backlog", "Games you tagged Backlog, waiting longest first", true},
	{"playing", "Playing now", "Games you tagged Playing, most recent first", true},
	{"beaten", "Beaten this year", "Games tagged Completed this year", true},
	{"multi", "Multiplayer", "Two players or more, from the games' details", false},
	{"genres", "Genres", "A folder per genre with three games or more, from the games' details", false},
	{"decades", "Decades", "A folder per decade (1980s, 1990s…), from the games' details", false},
	{"players", "Player count", "A folder for 1, 2 and 3+ players, from the games' details", false},
}

// autoCollSwitchID is the folder a switch shows ("" for the genres, which
// are many).
func autoCollSwitchID(name string) string {
	switch name {
	case "last":
		return autoCollIDLast
	case "played":
		return homeTabsAutoPlayed
	case "all":
		return autoCollIDAll
	case "added":
		return homeTabsAutoAdded
	case "unplayed":
		return homeTabsAutoUnplayed
	case "backlog":
		return autoCollIDBacklog
	case "playing":
		return autoCollIDPlaying
	case "beaten":
		return autoCollIDBeaten
	case "multi":
		return autoCollIDMulti
	}
	return ""
}

// ── Settings ──────────────────────────────────────────────────────────────

// autoCollEnabled is the set of switches that are on.
func autoCollEnabled(c *Config) map[string]bool {
	out := map[string]bool{}
	var raw interface{}
	ok := false
	if c != nil && c.Variables.Custom != nil {
		raw, ok = c.Variables.Custom[autoCollSettingKey]
	}
	if !ok {
		for _, s := range autoCollSwitches {
			if s.on {
				out[s.name] = true
			}
		}
		return out
	}
	for _, part := range strings.Split(fmt.Sprint(raw), ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		for _, s := range autoCollSwitches {
			if s.name == name {
				out[name] = true
			}
		}
	}
	return out
}

// autoCollSwitchKey is which switches are on, in the tab's order: a library
// cache key changes with it, so hiding an automatic collection takes effect
// on the next frame without a restart.
func autoCollSwitchKey(c *Config) string {
	en := autoCollEnabled(c)
	names := make([]string, 0, len(autoCollSwitches))
	for _, s := range autoCollSwitches {
		if en[s.name] {
			names = append(names, s.name)
		}
	}
	return strings.Join(names, ",")
}

// autoCollSetSwitch turns one switch on or off and rebuilds Home's pages.
// Everything off is stored as "none": an empty value would read as unset.
func autoCollSetSwitch(c *Config, name string, on bool) {
	en := autoCollEnabled(c)
	if on {
		en[name] = true
	} else {
		delete(en, name)
	}
	names := make([]string, 0, len(autoCollSwitches))
	for _, s := range autoCollSwitches {
		if en[s.name] {
			names = append(names, s.name)
		}
	}
	value := strings.Join(names, ",")
	if value == "" {
		value = "none"
	}
	setStoreString(c, autoCollSettingKey, value)
	homeTabsTouch()
}

// autoCollSettingsRow is Settings → Games & emulators → Automatic collections.
func autoCollSettingsRow() setRow {
	return setRow{
		ID: autoCollSettingKey, Label: "Automatic collections", Kind: setKindAction,
		Detail: setStatic("Folders on Home's Collections tab and in the Games library that fill themselves"),
		Value: func(c *Config) string {
			n := len(autoCollEnabled(c))
			if n == 0 {
				return "Off"
			}
			return strconv.Itoa(n) + " on"
		},
		Open: autoCollOpenSheet,
	}
}

// autoCollSheetOptions are the sheet's rows: each switch and its state.
func autoCollSheetOptions(c *Config) []setOption {
	en := autoCollEnabled(c)
	out := make([]setOption, 0, len(autoCollSwitches))
	for _, s := range autoCollSwitches {
		out = append(out, setOption{Value: s.name, Label: s.label + " · " + setOnOff(en[s.name]), Note: s.note})
	}
	return out
}

// autoCollOpenSheet is the row's press: the Settings list sheet with one row
// per switch. Choosing a row flips it and leaves the sheet open on it, so
// several can be changed in one visit; B closes it.
func autoCollOpenSheet(c *Config) {
	var choose func(string)
	choose = func(name string) {
		autoCollSetSwitch(c, name, !autoCollEnabled(c)[name])
		s := &setHub.sheet
		cursor, scroll := s.cursor, s.scroll
		s.open, s.choose = true, choose
		s.options = autoCollSheetOptions(c)
		s.cursor, s.scroll = cursor, scroll
		setHub.dirty = true
	}
	setSheetOpen(&setHub.sheet, "Automatic collections",
		hintLine(FooterHint{"A", "On or off"})+" · they appear on Home's Collections tab and in the Games library",
		autoCollSheetOptions(c), "", choose)
}

// ── The store ─────────────────────────────────────────────────────────────

// autoCollFile is the document on disk.
type autoCollFile struct {
	Version int `json:"version"`
	// Based is set once the baseline library was filed: the games already
	// on the card when the store was made, which are never "recent".
	Based  bool              `json:"based,omitempty"`
	Scans  uint64            `json:"scans"`
	Plays  uint64            `json:"plays"`
	Seen   map[string]uint64 `json:"seen"`
	Played map[string]uint64 `json:"played,omitempty"`
}

// autoCollStore is the loaded document; rev moves on every change.
var autoCollStore struct {
	mu      sync.Mutex
	loaded  bool
	loading bool
	file    autoCollFile
	rev     uint64
	seq     uint64
}

// autoCollWrites orders the asynchronous saves: an older snapshot than the
// last one written is dropped.
var autoCollWrites struct {
	mu      sync.Mutex
	written uint64
}

// autoCollPath is where the store lives; "" while tests run (no test points
// it anywhere: the library tests build libraries, which must not file them
// in the developer's data folder).
var autoCollPath = func() string {
	if testing.Testing() {
		return ""
	}
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, autoCollFileName)
}

// autoCollReadFile reads the store. A missing or damaged file is an empty
// store whose next library is the baseline again, so it never floods Recently
// added; the atomic save's .bak is tried first.
func autoCollReadFile(path string) autoCollFile {
	f := autoCollFile{Version: autoCollFileVersion}
	if strings.TrimSpace(path) != "" {
		var saved autoCollFile
		if _, err := readJSONWithBackup(path, &saved); err != nil {
			if !os.IsNotExist(err) {
				logWarn("collections", "ignoring a damaged %s: %v", autoCollFileName, err)
			}
		} else if saved.Version == autoCollFileVersion {
			f = saved
		}
	}
	if f.Seen == nil {
		f.Seen = map[string]uint64{}
	}
	if f.Played == nil {
		f.Played = map[string]uint64{}
	}
	return f
}

// autoCollEnsure loads the store. Off the render thread only: the file is
// read outside the lock, so the tab never waits on the card.
func autoCollEnsure() {
	autoCollStore.mu.Lock()
	loaded := autoCollStore.loaded
	autoCollStore.mu.Unlock()
	if loaded {
		return
	}
	f := autoCollReadFile(autoCollPath())
	autoCollStore.mu.Lock()
	if !autoCollStore.loaded {
		autoCollStore.loaded = true
		autoCollStore.file = f
		autoCollStore.rev++
	}
	autoCollStore.loading = false
	autoCollStore.mu.Unlock()
	homeTabsTouch()
}

// autoCollLoadAsync starts the load from the render thread, once.
func autoCollLoadAsync() {
	autoCollStore.mu.Lock()
	start := !autoCollStore.loaded && !autoCollStore.loading
	if start {
		autoCollStore.loading = true
	}
	autoCollStore.mu.Unlock()
	if start {
		go autoCollEnsure()
	}
}

// autoCollChangedLocked saves the document after a change, on a goroutine,
// newest snapshot wins. The caller holds the lock.
func autoCollChangedLocked() {
	autoCollStore.rev++
	autoCollStore.seq++
	path := autoCollPath()
	if path == "" {
		return
	}
	autoCollStore.file.Version = autoCollFileVersion
	data, err := json.Marshal(autoCollStore.file)
	if err != nil {
		logWarn("collections", "cannot encode %s: %v", autoCollFileName, err)
		return
	}
	seq := autoCollStore.seq
	go func() {
		autoCollWrites.mu.Lock()
		defer autoCollWrites.mu.Unlock()
		if seq <= autoCollWrites.written {
			return
		}
		if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
			logWarn("collections", "cannot save %s: %v", autoCollFileName, err)
			return
		}
		autoCollWrites.written = seq
	}()
}

// autoCollGameKey is how the store knows a game: a hash of its content key
// (the cartridge's bytes, game_library_view.go), or of its path when the
// build gave it none (an Android or HTML5 game, a port).
func autoCollGameKey(g libraryGame) string {
	key := g.ContentKey
	if key == "" {
		key = "path:" + g.Path
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return strconv.FormatUint(h.Sum64(), 36)
}

// autoCollNoteLibrary files the games of a finished library build under the
// scan they first turned up in. libIndexSaveLibrary calls it after every
// build (the Games library's and Home's), off the render thread.
func autoCollNoteLibrary(lib gameLibraryReport) {
	if lib.Total == 0 || autoCollPath() == "" {
		return
	}
	keys := make([]string, 0, lib.Total)
	for _, system := range lib.Systems {
		if system.Name == continueSystemName {
			continue
		}
		for _, g := range system.Games {
			keys = append(keys, autoCollGameKey(g))
		}
	}
	if len(keys) == 0 {
		return
	}
	autoCollEnsure()
	autoCollStore.mu.Lock()
	defer autoCollStore.mu.Unlock()
	f := &autoCollStore.file
	if f.Seen == nil {
		f.Seen = map[string]uint64{}
	}
	var mark uint64
	if f.Based {
		mark = f.Scans + 1
	}
	added := 0
	for _, key := range keys {
		if _, ok := f.Seen[key]; ok {
			continue
		}
		if len(f.Seen) >= autoCollSeenMax {
			break
		}
		f.Seen[key] = mark
		added++
	}
	if added == 0 && f.Based {
		return
	}
	if !f.Based {
		f.Based = true
		logInfo("collections", "recently added: %d games on the card are the baseline", added)
	} else {
		f.Scans = mark
		logInfo("collections", "recently added: %d new games", added)
	}
	autoCollChangedLocked()
	homeTabsTouch()
}

// autoCollNotePlayed moves a game to the front of Last played. Off the
// render thread (the Started hook runs it on a goroutine).
func autoCollNotePlayed(path string) {
	path = strings.TrimSpace(path)
	if path == "" || autoCollPath() == "" {
		return
	}
	autoCollEnsure()
	autoCollStore.mu.Lock()
	defer autoCollStore.mu.Unlock()
	f := &autoCollStore.file
	if f.Played == nil {
		f.Played = map[string]uint64{}
	}
	f.Plays++
	f.Played[path] = f.Plays
	for len(f.Played) > autoCollPlayedMax {
		oldest, at := "", uint64(0)
		for p, n := range f.Played {
			if oldest == "" || n < at || (n == at && p < oldest) {
				oldest, at = p, n
			}
		}
		delete(f.Played, oldest)
	}
	autoCollChangedLocked()
	homeTabsTouch()
}

func init() {
	gameLaunchRegisterStarted(func(game libraryGame, _ string, _ *exec.Cmd) {
		path := game.Path
		go autoCollNotePlayed(path)
	})
	gamePageRegisterOpened(autoCollPlaysOpened)
	gamePageRegisterStatus(gamePageStatus{ID: "plays", Order: 50, Chip: autoCollPlaysChip})
}

// ── The play count chip ───────────────────────────────────────────────────

// autoCollPlays is the open page's launch count, read on a goroutine when
// the page opens (game_history.go may still have to load its file). Render
// thread only.
var autoCollPlays struct {
	path string
	n    int
}

// autoCollPlaysOpened reads the count for the page that opened.
func autoCollPlaysOpened(game libraryGame) {
	autoCollPlays.path, autoCollPlays.n = game.Path, 0
	if isBuiltInLibraryGame(game) || strings.TrimSpace(game.Title) == "" {
		return
	}
	system, title, path := game.System, game.Title, game.Path
	go func() {
		n := gameHistoryFor(system, title).Launches
		tbxPost(func() {
			if autoCollPlays.path == path {
				autoCollPlays.n = n
			}
		})
	}()
}

// autoCollPlaysChip is "Played 5 times" in the page's details column.
func autoCollPlaysChip(game libraryGame) (string, int, bool) {
	n := autoCollPlays.n
	if autoCollPlays.path != game.Path || n <= 0 {
		return "", 0, false
	}
	if n == 1 {
		return "Played once", gamePageToneNeutral, true
	}
	return "Played " + strconv.Itoa(n) + " times", gamePageToneNeutral, true
}

// ── The collections ───────────────────────────────────────────────────────

// autoCollSig is what the built collections were made from.
type autoCollSig struct {
	libGen   uint64
	hidden   bool
	gmetaRev uint64
	gmetaOK  bool
	storeRev uint64
	storeOK  bool
	recents  int
	recent0  string
	// Kid mode and its whole consoles (uimode_kids.go).
	kid         bool
	kidConsoles string
}

// autoCollGenre is one genre folder.
type autoCollGenre struct {
	id, name string
	games    []libraryGame
}

// autoCollIdx holds the collections built from one library snapshot; each
// list is made the first time it is asked for. Render thread only.
var autoCollIdx struct {
	sig         autoCollSig
	built       bool
	games       []libraryGame
	lists       map[string][]libraryGame
	genres      []autoCollGenre
	decades     []autoCollGenre
	players     []autoCollGenre
	detailsDone bool
}

// autoCollSync starts a fresh build when the library, the details, the
// store or the recents moved. It reads the same listing every Home tab
// reads: homeTabsLibrary, hidden games filtered as the library filters them.
func autoCollSync() {
	lib := homeTabsLibrary()
	recents := recentGames()
	sig := autoCollSig{libGen: homeTabsLib.gen, hidden: libraryShowHidden(), recents: len(recents), kid: uiModeIsKid()}
	if len(recents) > 0 {
		sig.recent0 = recents[0].Path
	}
	if sig.kid {
		sig.kidConsoles = strings.Join(uiModeSnapshot().KidConsoles, "|")
	}
	gmetaStore.mu.Lock()
	sig.gmetaRev, sig.gmetaOK = gmetaStore.rev, gmetaStore.loaded
	gmetaStore.mu.Unlock()
	autoCollStore.mu.Lock()
	sig.storeRev, sig.storeOK = autoCollStore.rev, autoCollStore.loaded
	autoCollStore.mu.Unlock()
	if !sig.storeOK {
		autoCollLoadAsync()
	}
	if autoCollIdx.built && autoCollIdx.sig == sig {
		return
	}
	autoCollIdx.sig, autoCollIdx.built = sig, true
	autoCollIdx.lists = map[string][]libraryGame{}
	autoCollIdx.genres, autoCollIdx.decades, autoCollIdx.players = nil, nil, nil
	autoCollIdx.detailsDone = false
	games := make([]libraryGame, 0, lib.Total)
	// Kid mode keeps the kids' games only (uimode_kids.go), as on the Games tab.
	for _, system := range uiModeKidSystems(filterHiddenLibrarySystems(lib.Systems, sig.hidden)) {
		if system.Name == continueSystemName {
			continue
		}
		games = append(games, system.Games...)
	}
	autoCollIdx.games = games
}

// autoCollIDs is the automatic folders the tab lists, in order: the
// switches that are on, a genre switch standing for every genre folder.
func autoCollIDs(c *Config) []string {
	autoCollSync()
	if autoCollNeedsDetails(c) {
		autoCollDetails()
	}
	return autoCollIDList(c)
}

// autoCollNeedsDetails reports whether a switch that reads the games' details
// is on: with all of them off, a fresh listing costs no metadata pass.
func autoCollNeedsDetails(c *Config) bool {
	en := autoCollEnabled(c)
	return en["multi"] || en["genres"] || en["decades"] || en["players"]
}

// autoCollIDList is autoCollIDs over the collections already built: the
// switches as they are now, without touching the library or the store, so a
// test can hand it a fixture and a config.
func autoCollIDList(c *Config) []string {
	en := autoCollEnabled(c)
	ids := make([]string, 0, len(autoCollSwitches))
	for _, s := range autoCollSwitches {
		if !en[s.name] {
			continue
		}
		switch s.name {
		case "genres", "decades", "players":
			// One switch stands for many folders.
			for _, g := range autoCollFoldersForSwitch(s.name) {
				ids = append(ids, g.id)
			}
		default:
			if id := autoCollSwitchID(s.name); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// autoCollFoldersForSwitch is the folders a many-folder switch shows.
func autoCollFoldersForSwitch(name string) []autoCollGenre {
	switch name {
	case "genres":
		return autoCollIdx.genres
	case "decades":
		return autoCollIdx.decades
	case "players":
		return autoCollIdx.players
	}
	return nil
}

// autoCollName is an automatic folder's name, "" for an id that is not one
// (a genre the details no longer know).
func autoCollName(id string) string {
	switch id {
	case autoCollIDLast:
		return "Last played"
	case homeTabsAutoPlayed:
		return "Most played"
	case autoCollIDAll:
		return "All games"
	case homeTabsAutoAdded:
		return "Recently added"
	case homeTabsAutoUnplayed:
		return "Never played"
	case autoCollIDBacklog:
		return "Backlog"
	case autoCollIDPlaying:
		return "Playing now"
	case autoCollIDBeaten:
		return "Beaten this year"
	case autoCollIDMulti:
		return "Multiplayer"
	}
	if autoCollIsSmartID(id) {
		autoCollSync()
		autoCollDetails()
		for _, list := range [][]autoCollGenre{autoCollIdx.genres, autoCollIdx.decades, autoCollIdx.players} {
			for _, g := range list {
				if g.id == id {
					return g.name
				}
			}
		}
	}
	return ""
}

// autoCollIsSmartID reports whether an id names a folder the metadata makes.
func autoCollIsSmartID(id string) bool {
	return strings.HasPrefix(id, autoCollGenrePrefix) ||
		strings.HasPrefix(id, autoCollDecadePrefix) ||
		strings.HasPrefix(id, autoCollPlayerPrefix)
}

// autoCollGames is an automatic folder's games. The slice is shared with the
// cache: callers read it only.
func autoCollGames(id string) []libraryGame {
	autoCollSync()
	if list, ok := autoCollIdx.lists[id]; ok {
		return list
	}
	var list []libraryGame
	switch {
	case id == autoCollIDLast:
		list = autoCollLastPlayed()
	case id == homeTabsAutoPlayed:
		for _, g := range autoCollIdx.games {
			if g.Playtime > 0 {
				list = append(list, g)
			}
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Playtime > list[j].Playtime })
		list = autoCollCut(list, autoCollListMax)
	case id == autoCollIDAll:
		list = append([]libraryGame(nil), autoCollIdx.games...)
		autoCollSortAZ(list)
	case id == homeTabsAutoAdded:
		list = autoCollRecentlyAdded()
	case id == homeTabsAutoUnplayed:
		list = autoCollNeverPlayed()
	case id == autoCollIDBacklog:
		list = autoCollTagged(gmetaTagBacklog, false)
	case id == autoCollIDPlaying:
		list = autoCollTagged(gmetaTagPlaying, false)
	case id == autoCollIDBeaten:
		list = autoCollTagged(gmetaTagCompleted, true)
	case id == autoCollIDMulti || autoCollIsSmartID(id):
		// autoCollDetails files these lists itself.
		autoCollDetails()
		if have, ok := autoCollIdx.lists[id]; ok {
			return have
		}
	}
	autoCollIdx.lists[id] = list
	return list
}

// autoCollTagged is one of the journal folders: games carrying a tag in their
// saved game page. Beaten this year also needs a completion stamp this year;
// a game marked Completed before January is not new news.
//
// These read the same tags the game page's status sheet writes, so there is no
// second copy of the status to keep in sync. A game whose page was never
// opened has no tag and is not in any of the three folders.
func autoCollTagged(tag string, thisYear bool) []libraryGame {
	type entry struct {
		game      libraryGame
		completed time.Time
	}
	var found []entry
	year := time.Now().Year()
	for _, g := range autoCollIdx.games {
		rec, ok := gmetaGet(g.System, g.Title)
		if !ok || !gmetaHasTag(rec, tag) {
			continue
		}
		e := entry{game: g}
		if thisYear {
			e.completed = autoCollCompletionTime(rec)
			if e.completed.Year() != year {
				continue
			}
		}
		found = append(found, e)
	}
	switch tag {
	case gmetaTagBacklog:
		// Waiting longest first, so the list doubles as a "what next?" queue.
		sort.SliceStable(found, func(i, j int) bool {
			if !found[i].game.AddedAt.Equal(found[j].game.AddedAt) {
				return found[i].game.AddedAt.Before(found[j].game.AddedAt)
			}
			return strings.ToLower(found[i].game.Label()) < strings.ToLower(found[j].game.Label())
		})
	case gmetaTagPlaying:
		sort.SliceStable(found, func(i, j int) bool { return found[i].game.LastPlayed.After(found[j].game.LastPlayed) })
	default:
		sort.SliceStable(found, func(i, j int) bool { return found[i].completed.After(found[j].completed) })
	}
	list := make([]libraryGame, 0, len(found))
	for _, e := range found {
		list = append(list, e.game)
	}
	return autoCollCut(list, autoCollListMax)
}

// autoCollCompletionTime is when a game was tagged Completed, recorded by the
// game page when the tag is first set.
func autoCollCompletionTime(rec gmetaRecord) time.Time {
	stamp := rec.Milestones[gmetaMilestoneCompleted]
	if strings.TrimSpace(stamp) == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return time.Time{}
	}
	return at
}

// autoCollCut bounds a list.
func autoCollCut(list []libraryGame, max int) []libraryGame {
	if len(list) > max {
		return list[:max]
	}
	return list
}

// autoCollByPath maps the listing's games by ROM path.
func autoCollByPath() map[string]libraryGame {
	out := make(map[string]libraryGame, len(autoCollIdx.games))
	for _, g := range autoCollIdx.games {
		out[g.Path] = g
	}
	return out
}

// autoCollPlayedCopy is the store's launch counters (empty until it loaded).
func autoCollPlayedCopy() map[string]uint64 {
	autoCollStore.mu.Lock()
	defer autoCollStore.mu.Unlock()
	out := make(map[string]uint64, len(autoCollStore.file.Played))
	for p, n := range autoCollStore.file.Played {
		out[p] = n
	}
	return out
}

// autoCollLastPlayed is Last played: by the launch counter, then the recents
// the counter has not seen, in their own order.
func autoCollLastPlayed() []libraryGame {
	byPath := autoCollByPath()
	type ranked struct {
		g libraryGame
		n uint64
	}
	var counted []ranked
	for path, n := range autoCollPlayedCopy() {
		if g, ok := byPath[path]; ok {
			counted = append(counted, ranked{g, n})
		}
	}
	sort.Slice(counted, func(i, j int) bool { return counted[i].n > counted[j].n })
	out := make([]libraryGame, 0, autoCollListMax)
	have := map[string]bool{}
	for _, r := range counted {
		out = append(out, r.g)
		have[r.g.Path] = true
	}
	for _, r := range recentGames() {
		if g, ok := byPath[r.Path]; ok && !have[r.Path] {
			out = append(out, g)
			have[r.Path] = true
		}
	}
	return autoCollCut(out, autoCollListMax)
}

// autoCollRecentlyAdded is Recently added: the games first listed by a later
// scan than the baseline, the newest scan first, A to Z within one.
func autoCollRecentlyAdded() []libraryGame {
	type ranked struct {
		g libraryGame
		n uint64
	}
	var list []ranked
	autoCollStore.mu.Lock()
	seen := autoCollStore.file.Seen
	for _, g := range autoCollIdx.games {
		if g.System == builtInLibrarySystem {
			continue
		}
		if n := seen[autoCollGameKey(g)]; n > 0 {
			list = append(list, ranked{g, n})
		}
	}
	autoCollStore.mu.Unlock()
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return strings.ToLower(list[i].g.Label()) < strings.ToLower(list[j].g.Label())
	})
	out := make([]libraryGame, 0, min(len(list), autoCollListMax))
	for _, r := range list {
		out = append(out, r.g)
	}
	return autoCollCut(out, autoCollListMax)
}

// autoCollNeverPlayed is Never played: no hours, no launch counted, not in
// the recents; A to Z.
func autoCollNeverPlayed() []libraryGame {
	recent := map[string]bool{}
	for _, r := range recentGames() {
		recent[r.Path] = true
	}
	played := autoCollPlayedCopy()
	var list []libraryGame
	for _, g := range autoCollIdx.games {
		if g.System == builtInLibrarySystem || g.Playtime > 0 || !g.LastPlayed.IsZero() || recent[g.Path] || played[g.Path] > 0 {
			continue
		}
		list = append(list, g)
	}
	autoCollSortAZ(list)
	return autoCollCut(list, autoCollUnplayedMax)
}

// autoCollMeta is the part of a game's details the smart folders read.
type autoCollMeta struct {
	Genre   string
	Year    string
	Players string
}

// autoCollSmartGroups is what one listing's metadata makes.
type autoCollSmartGroups struct {
	multi   []libraryGame
	genres  []autoCollGenre
	decades []autoCollGenre
	players []autoCollGenre
}

// autoCollGroupGames groups a library listing by the details each game has:
// the multiplayer list, a folder per genre, per decade and per player count.
// It is the whole decision, free of the store and the caches, so a test can
// feed it fixtures, and call it again when details arrive late or change.
// A game can land in a genre, a decade and a player-count folder at once: the
// folders are views over the one listing, so nothing is copied per folder and
// no game is stopped from being in two of them.
func autoCollGroupGames(games []libraryGame, meta func(libraryGame) (autoCollMeta, bool)) autoCollSmartGroups {
	var out autoCollSmartGroups
	genres := map[string]*autoCollGenre{}
	decades := map[string]*autoCollGenre{}
	players := map[string]*autoCollGenre{}
	for _, g := range games {
		m, ok := meta(g)
		if !ok {
			continue
		}
		count := autoCollPlayers(m.Players)
		if count >= 2 {
			out.multi = append(out.multi, g)
		}
		if name := autoCollGenreLabel(m.Genre); name != "" {
			autoCollAddGroup(genres, autoCollGenrePrefix+autoCollSlug(name), name, g)
		}
		if year := autoCollYear(m.Year); year > 0 {
			decade := year / 10 * 10
			name := strconv.Itoa(decade) + "s"
			autoCollAddGroup(decades, autoCollDecadePrefix+name, name, g)
		}
		if count > 0 {
			bucket, name := 1, "1 player"
			switch {
			case count == 2:
				bucket, name = 2, "2 players"
			case count >= 3:
				bucket, name = 3, "3+ players"
			}
			autoCollAddGroup(players, autoCollPlayerPrefix+strconv.Itoa(bucket), name, g)
		}
	}
	autoCollSortAZ(out.multi)
	out.genres = autoCollFolderList(genres, autoCollGenreMin, autoCollGenreMax)
	out.decades = autoCollFolderList(decades, autoCollSmartMin, autoCollSmartMax)
	out.players = autoCollFolderList(players, autoCollSmartMin, autoCollSmartMax)
	return out
}

// autoCollAddGroup files a game under one folder id, making the folder first.
func autoCollAddGroup(groups map[string]*autoCollGenre, id, name string, g libraryGame) {
	grp := groups[id]
	if grp == nil {
		grp = &autoCollGenre{id: id, name: name}
		groups[id] = grp
	}
	grp.games = append(grp.games, g)
}

// autoCollFolderList turns a pass's groups into the folders the tab lists:
// those of at least min games, the biggest when there are more than max, then
// by name with each folder's games A to Z.
func autoCollFolderList(groups map[string]*autoCollGenre, min, max int) []autoCollGenre {
	out := make([]autoCollGenre, 0, len(groups))
	for _, grp := range groups {
		if len(grp.games) >= min {
			out = append(out, *grp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].games) != len(out[j].games) {
			return len(out[i].games) > len(out[j].games)
		}
		return out[i].id < out[j].id
	})
	if len(out) > max {
		out = out[:max]
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].name) < strings.ToLower(out[j].name)
	})
	for i := range out {
		autoCollSortAZ(out[i].games)
	}
	return out
}

// autoCollDetails files Multiplayer, the genre folders, the decade folders and
// the player-count folders from the games' details. Only the consoles whose
// details carry something these read are looked up, so a large card costs a
// title fold per game of those consoles only.
func autoCollDetails() {
	if autoCollIdx.detailsDone {
		return
	}
	autoCollIdx.detailsDone = true
	gmetaStore.mu.Lock()
	if !gmetaStore.loaded {
		gmetaStore.mu.Unlock()
		// Loaded off this thread; the build's signature notices it landed.
		go func() {
			gmetaStore.mu.Lock()
			gmetaEnsureLocked()
			gmetaStore.mu.Unlock()
			homeTabsTouch()
		}()
		return
	}
	consoles := map[string]bool{}
	for _, rec := range gmetaStore.games {
		if rec != nil && (strings.TrimSpace(rec.Genre) != "" || strings.TrimSpace(rec.Year) != "" || autoCollPlayers(rec.Players) > 0) {
			consoles[strings.ToLower(strings.TrimSpace(rec.System))] = true
		}
	}
	meta := func(g libraryGame) (autoCollMeta, bool) {
		if len(consoles) == 0 || !consoles[strings.ToLower(strings.TrimSpace(g.System))] {
			return autoCollMeta{}, false
		}
		rec := gmetaStore.games[gmetaKey(g.System, g.Title)]
		if rec == nil {
			return autoCollMeta{}, false
		}
		return autoCollMeta{Genre: rec.Genre, Year: rec.Year, Players: rec.Players}, true
	}
	groups := autoCollGroupGames(autoCollIdx.games, meta)
	gmetaStore.mu.Unlock()

	autoCollIdx.lists[autoCollIDMulti] = groups.multi
	autoCollIdx.genres, autoCollIdx.decades, autoCollIdx.players = groups.genres, groups.decades, groups.players
	for _, list := range [][]autoCollGenre{groups.genres, groups.decades, groups.players} {
		for i := range list {
			autoCollIdx.lists[list[i].id] = list[i].games
		}
	}
}

// autoCollYear reads a game's year ("1991", "1991-08-23", "c. 1991"): the
// first four-digit year in it that could be one, 0 when there is none.
func autoCollYear(text string) int {
	year, digits := 0, 0
	for _, r := range text + " " {
		if r >= '0' && r <= '9' {
			if digits < 4 {
				year = year*10 + int(r-'0')
				digits++
			}
			continue
		}
		if digits == 4 && year >= autoCollYearMin && year <= autoCollYearMax {
			return year
		}
		year, digits = 0, 0
	}
	return 0
}

// autoCollPlayers reads a player count ("2", "1-4", "4 players"): the
// largest number in it, 0 when there is none.
func autoCollPlayers(text string) int {
	best, cur, in := 0, 0, false
	for _, r := range text + " " {
		if r >= '0' && r <= '9' {
			if cur < 1000 {
				cur = cur*10 + int(r-'0')
			}
			in = true
			continue
		}
		if in && cur > best {
			best = cur
		}
		cur, in = 0, false
	}
	return best
}

// autoCollGenreLabel is the genre a folder is named after: the first part of
// a compound one ("Action / Platformer" is Action), on one line, bounded.
func autoCollGenreLabel(genre string) string {
	if i := strings.IndexAny(genre, "/,;|"); i >= 0 {
		genre = genre[:i]
	}
	genre = strings.Join(strings.Fields(genre), " ")
	if utf8.RuneCountInString(genre) > autoCollGenreName {
		genre = strings.TrimSpace(string([]rune(genre)[:autoCollGenreName]))
	}
	return genre
}

// autoCollSlug is a genre's id part: lower-case letters and digits joined by
// dashes ("Role-Playing (RPG)" is "role-playing-rpg"), apostrophes dropped,
// so spellings that differ only in case or punctuation are one folder. A name
// with no such letters (another script) is known by a hash of itself.
func autoCollSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		case r == '\'' || r == '’':
			// "Shoot'em" is "shootem".
		default:
			dash = true
		}
	}
	if b.Len() == 0 {
		h := fnv.New64a()
		_, _ = h.Write([]byte(strings.ToLower(name)))
		return "x" + strconv.FormatUint(h.Sum64(), 36)
	}
	return b.String()
}

// autoCollAZ sorts games by their shown name with the keys folded once.
type autoCollAZ struct {
	games []libraryGame
	keys  []string
}

func (s autoCollAZ) Len() int { return len(s.games) }
func (s autoCollAZ) Less(i, j int) bool {
	if s.keys[i] != s.keys[j] {
		return s.keys[i] < s.keys[j]
	}
	return s.games[i].Path < s.games[j].Path
}
func (s autoCollAZ) Swap(i, j int) {
	s.games[i], s.games[j] = s.games[j], s.games[i]
	s.keys[i], s.keys[j] = s.keys[j], s.keys[i]
}

// autoCollSortAZ orders games A to Z by the name the library shows.
func autoCollSortAZ(games []libraryGame) {
	keys := make([]string, len(games))
	for i, g := range games {
		keys[i] = strings.ToLower(g.Label())
	}
	sort.Sort(autoCollAZ{games: games, keys: keys})
}
