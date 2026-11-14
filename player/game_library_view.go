package main

// game_library_view.go — the library's view state: how games are ordered, which
// ones are hidden, and the display names the user gave them.
//
// This is deliberately separate from gmeta_store.go and gameoptions.go. A
// gmeta record is keyed by console and title because tags and ratings belong to
// a game; launch options are keyed by path because they belong to one file.
// Hidden/display state belongs to the cartridge itself, so it is keyed by a
// content hash: renaming Kart.gba to Kart (USA).gba keeps both. Existing files
// are untouched and no saved data is migrated.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	libraryViewVersion = 1
	libraryViewMaxName = 80

	librarySortTitle        = "title"
	librarySortLast         = "last_played"
	librarySortAdded        = "recently_added"
	librarySortTime         = "playtime"
	librarySortRate         = "rating"
	librarySortYear         = "year"
	libraryLayoutList       = "list"
	libraryLayoutHorizontal = "covers_horizontal"
	libraryLayoutVertical   = "covers_vertical"
)

// librarySortChoices is the order the options sheet cycles through.
var librarySortChoices = []struct {
	Value string
	Label string
}{
	{librarySortTitle, "Title"},
	{librarySortLast, "Last played"},
	{librarySortAdded, "Recently added"},
	{librarySortTime, "Playtime"},
	{librarySortRate, "Rating"},
	{librarySortYear, "Year"},
}

// libraryGameView is what the user changed about one cartridge.
type libraryGameView struct {
	Hidden       bool   `json:"hidden,omitempty"`
	DisplayTitle string `json:"display_title,omitempty"`
}

type libraryViewFile struct {
	Version    int                        `json:"version"`
	Sort       string                     `json:"sort,omitempty"`
	Layout     string                     `json:"layout,omitempty"`
	ShowHidden bool                       `json:"show_hidden,omitempty"`
	Games      map[string]libraryGameView `json:"games,omitempty"`
}

var libraryViewStore struct {
	mu     sync.Mutex
	loaded bool
	data   libraryViewFile
}

// libraryViewPath is a variable so tests can use a temporary file.
var libraryViewPath = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "game-library.json")
}

func libraryViewEmpty() libraryViewFile {
	return libraryViewFile{Version: libraryViewVersion, Sort: librarySortTitle, Layout: libraryLayoutList, Games: map[string]libraryGameView{}}
}

func libraryViewNormalize(data *libraryViewFile) {
	if data == nil {
		return
	}
	data.Version = libraryViewVersion
	data.Sort = librarySortValue(data.Sort)
	data.Layout = libraryLayoutValue(data.Layout)
	if data.Games == nil {
		data.Games = map[string]libraryGameView{}
	}
	for key, view := range data.Games {
		if strings.TrimSpace(key) == "" {
			delete(data.Games, key)
			continue
		}
		view.DisplayTitle = libraryCleanDisplayTitle(view.DisplayTitle)
		if !view.Hidden && view.DisplayTitle == "" {
			delete(data.Games, key)
			continue
		}
		data.Games[key] = view
	}
}

func librarySortValue(mode string) string {
	mode = strings.TrimSpace(strings.ToLower(mode))
	for _, choice := range librarySortChoices {
		if choice.Value == mode {
			return mode
		}
	}
	return librarySortTitle
}

func libraryLayoutValue(layout string) string {
	switch layout {
	case libraryLayoutHorizontal, libraryLayoutVertical:
		return layout
	default:
		return libraryLayoutList
	}
}

func libraryLayoutMode() string {
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	return libraryLayoutValue(libraryViewStore.data.Layout)
}

func libraryLayoutLabel() string {
	switch libraryLayoutMode() {
	case libraryLayoutHorizontal:
		return "3D shelf"
	case libraryLayoutVertical:
		return "Cover column"
	default:
		return "List"
	}
}

func cycleLibraryLayout(delta int) string {
	choices := []string{libraryLayoutList, libraryLayoutHorizontal, libraryLayoutVertical}
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	idx := 0
	for i, choice := range choices {
		if choice == libraryViewStore.data.Layout {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(choices)) % len(choices)
	libraryViewStore.data.Layout = choices[idx]
	libraryViewSaveLocked()
	return choices[idx]
}

func libraryCleanDisplayTitle(title string) string {
	title = strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
	if runes := []rune(title); len(runes) > libraryViewMaxName {
		title = strings.TrimSpace(string(runes[:libraryViewMaxName]))
	}
	return title
}

func libraryViewEnsureLocked() {
	if libraryViewStore.loaded {
		return
	}
	libraryViewStore.loaded = true
	libraryViewStore.data = libraryViewEmpty()
	path := libraryViewPath()
	if strings.TrimSpace(path) == "" {
		return
	}
	var file libraryViewFile
	if _, err := readJSONWithBackup(path, &file); err != nil {
		if !os.IsNotExist(err) {
			logWarn("library", "ignoring %s: %v", path, err)
		}
		return
	}
	libraryViewStore.data = file
	libraryViewNormalize(&libraryViewStore.data)
}

func libraryViewSnapshot() libraryViewFile {
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	return libraryViewCopy(libraryViewStore.data)
}

func libraryViewCopy(data libraryViewFile) libraryViewFile {
	out := data
	out.Games = make(map[string]libraryGameView, len(data.Games))
	for key, view := range data.Games {
		out.Games[key] = view
	}
	return out
}

// libraryViewSaveLocked writes the current document. The caller holds the
// store lock; a view edit is a deliberate button press, so a synchronous atomic
// write is the right trade here (the file is a few hundred bytes).
func libraryViewSaveLocked() {
	path := libraryViewPath()
	if strings.TrimSpace(path) == "" {
		return
	}
	data, err := json.MarshalIndent(libraryViewStore.data, "", "  ")
	if err != nil {
		logWarn("library", "cannot encode the library view: %v", err)
		return
	}
	if err := AtomicWrite(path, append(data, '\n'), 0o644); err != nil {
		logWarn("library", "cannot save the library view: %v", err)
	}
}

func libraryViewGame(key string) libraryGameView {
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	return libraryViewStore.data.Games[key]
}

func setLibraryGameHidden(key string, hidden bool) {
	if strings.TrimSpace(key) == "" {
		return
	}
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	view := libraryViewStore.data.Games[key]
	view.Hidden = hidden
	libraryViewStore.data.Games[key] = view
	libraryViewNormalize(&libraryViewStore.data)
	libraryViewSaveLocked()
}

func setLibraryGameDisplayTitle(key, title string) {
	if strings.TrimSpace(key) == "" {
		return
	}
	title = libraryCleanDisplayTitle(title)
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	view := libraryViewStore.data.Games[key]
	view.DisplayTitle = title
	libraryViewStore.data.Games[key] = view
	libraryViewNormalize(&libraryViewStore.data)
	libraryViewSaveLocked()
}

func librarySortMode() string {
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	return librarySortValue(libraryViewStore.data.Sort)
}

func librarySortLabel() string {
	mode := librarySortMode()
	for _, choice := range librarySortChoices {
		if choice.Value == mode {
			return choice.Label
		}
	}
	return "Title"
}

// cycleLibrarySort steps through the modes and wraps, so both ends are
// reachable with one direction.
func cycleLibrarySort(delta int) string {
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	cur := librarySortValue(libraryViewStore.data.Sort)
	idx := 0
	for i, choice := range librarySortChoices {
		if choice.Value == cur {
			idx = i
			break
		}
	}
	idx = (idx + delta) % len(librarySortChoices)
	if idx < 0 {
		idx += len(librarySortChoices)
	}
	libraryViewStore.data.Sort = librarySortChoices[idx].Value
	libraryViewSaveLocked()
	return librarySortChoices[idx].Value
}

func libraryShowHidden() bool {
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	return libraryViewStore.data.ShowHidden
}

func setLibraryShowHidden(show bool) {
	libraryViewStore.mu.Lock()
	defer libraryViewStore.mu.Unlock()
	libraryViewEnsureLocked()
	libraryViewStore.data.ShowHidden = show
	libraryViewSaveLocked()
}

// ── Content identity ───────────────────────────────────────────────────────

const libraryContentViewBytes = 256 << 10

var libraryContentCache struct {
	mu   sync.Mutex
	keys map[string]string
}

// libraryContentKey hashes the cartridge's bytes. Reading a whole 4 GB ISO on
// every library open is not acceptable on a handheld, so files larger than two
// windows hash their size and first/last 256 KiB. The key is still the same
// after a move or rename, which is what preserving view state needs.
func libraryContentKey(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "path:" + path
	}
	stamp := fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())
	libraryContentCache.mu.Lock()
	if libraryContentCache.keys == nil {
		libraryContentCache.keys = map[string]string{}
	}
	if key, ok := libraryContentCache.keys[stamp]; ok {
		libraryContentCache.mu.Unlock()
		return key
	}
	libraryContentCache.mu.Unlock()

	h := sha256.New()
	fmt.Fprintf(h, "%d\n", info.Size())
	f, err := os.Open(path)
	if err != nil {
		return "path:" + path
	}
	defer f.Close()
	sample := int64(libraryContentViewBytes)
	if info.Size() <= 2*sample {
		_, _ = io.Copy(h, f)
	} else {
		if _, err := io.CopyN(h, f, sample); err != nil {
			return "path:" + path
		}
		if _, err := f.Seek(-sample, io.SeekEnd); err != nil {
			return "path:" + path
		}
		if _, err := io.CopyN(h, f, sample); err != nil {
			return "path:" + path
		}
	}
	key := hex.EncodeToString(h.Sum(nil))
	libraryContentCache.mu.Lock()
	libraryContentCache.keys[stamp] = key
	libraryContentCache.mu.Unlock()
	return key
}

// libraryDecorateGame applies the content-keyed view to one game.
func libraryDecorateGame(game *libraryGame) {
	if game == nil {
		return
	}
	game.ContentKey = libraryContentKey(game.Path)
	view := libraryViewGame(game.ContentKey)
	game.Hidden = view.Hidden
	game.DisplayTitle = view.DisplayTitle
}

// libraryDecorateGames adds recents, playtime, rating and year to a console's
// games, then sorts that console. The metadata stores keep their own keys; this
// only copies the fields the view needs.
func libraryDecorateGames(games []libraryGame, seconds map[string]int64) {
	played := map[string]time.Time{}
	for _, it := range recentGames() {
		if it.PlayedAt.After(played[it.Path]) {
			played[it.Path] = it.PlayedAt
		}
	}
	if seconds == nil {
		// Render-thread callers only: a background build is always handed a
		// snapshot (startGameLibraryScan), never reads the ledger itself.
		seconds = libraryPlaytimeSnapshot()
	}
	for i := range games {
		game := &games[i]
		libraryDecorateGame(game)
		game.LastPlayed = played[game.Path]
		// By console and title, plus a bare-title figure still left in the
		// map: libraryPlaytimeForTitles has moved or dropped every bare title
		// the decorated library holds on one console or more, so what is left
		// reaches at most one console (libraryPlaytimeSnapshot).
		key := playtimeGameKey(game.System, game.Title)
		game.Playtime = seconds[key]
		if _, title := playtimeGameKeyParts(key); title != key {
			game.Playtime += seconds[title]
		}
		if rec, ok := gmetaGet(game.System, game.Title); ok {
			game.Rating = rec.Rating
			game.Year = libraryParseYear(rec.Year)
		}
	}
	sortLibraryGames(games, librarySortMode())
}

func libraryParseYear(text string) int {
	text = strings.TrimSpace(text)
	if len(text) >= 4 {
		text = text[:4]
	}
	year, err := strconv.Atoi(text)
	if err != nil || year < 0 {
		return 0
	}
	return year
}

// sortLibraryGames orders one console. Missing values always sort last, so an
// unrated or never-played game does not push the user's rated games off the
// first screen. The title is the stable tie-breaker in every mode.
func sortLibraryGames(games []libraryGame, mode string) {
	mode = librarySortValue(mode)
	sort.SliceStable(games, func(i, j int) bool {
		a, b := games[i], games[j]
		switch mode {
		case librarySortLast:
			if !a.LastPlayed.Equal(b.LastPlayed) {
				if a.LastPlayed.IsZero() {
					return false
				}
				if b.LastPlayed.IsZero() {
					return true
				}
				return a.LastPlayed.After(b.LastPlayed)
			}
		case librarySortAdded:
			if !a.AddedAt.Equal(b.AddedAt) {
				if a.AddedAt.IsZero() {
					return false
				}
				if b.AddedAt.IsZero() {
					return true
				}
				return a.AddedAt.After(b.AddedAt)
			}
		case librarySortTime:
			if a.Playtime != b.Playtime {
				return a.Playtime > b.Playtime
			}
		case librarySortRate:
			if a.Rating != b.Rating {
				return a.Rating > b.Rating
			}
		case librarySortYear:
			if a.Year != b.Year {
				return a.Year > b.Year
			}
		}
		left, right := strings.ToLower(a.Label()), strings.ToLower(b.Label())
		if left == right {
			return a.Path < b.Path
		}
		return left < right
	})
}

// libraryHiddenCount is how many games the view hides.
func libraryHiddenCount(lib gameLibraryReport) int {
	n := 0
	for _, system := range lib.Systems {
		for _, game := range system.Games {
			if game.Hidden {
				n++
			}
		}
	}
	return n
}

// libraryPlaytimeSnapshot copies the game playtime ledger on the render
// thread, so a background scan can read it without racing the playtime ticker.
// Keys are "System|Title" (playtimeGameKey). A bare-title entry from an older
// save is resolved here for display: under its console when only one console
// is known for the title, kept bare when none is, and left out when two are,
// so a figure two consoles may share is shown for neither.
// It is never nil, so a background build handed it never falls back to
// reading the ledger off the render thread.
func libraryPlaytimeSnapshot() map[string]int64 {
	if !playtime.loaded || len(playtime.data.Games) == 0 {
		return map[string]int64{}
	}
	games := playtime.data.Games
	out := make(map[string]int64, len(games))
	var consoles map[string]map[string]bool
	for key, secs := range games {
		system, title := playtimeGameKeyParts(key)
		if system != "" {
			out[key] += secs
			continue
		}
		if consoles == nil {
			consoles = playtimeTitleConsoles(games, recentGames(), gameLib.lib.Systems)
		}
		switch known := consoles[title]; len(known) {
		case 0:
			out[key] += secs
		case 1:
			out[playtimeGameKey(playtimeOnlyConsole(known), title)] += secs
		}
	}
	return out
}

// libraryScanTitleConsoles maps each title of a scan to the consoles it is on,
// with the titles the library build gives the games (gameDisplayTitle).
func libraryScanTitleConsoles(games []scannedROM) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, rom := range games {
		system, title := playtimeGameKeyParts(playtimeGameKey(rom.System, gameDisplayTitle(rom.Path)))
		if system == "" || title == "" {
			continue
		}
		if out[title] == nil {
			out[title] = map[string]bool{}
		}
		out[title][system] = true
	}
	return out
}

// libraryPlaytimeForTitles resolves the bare-title entries a snapshot kept
// against the consoles of the library about to be decorated: a title there on
// exactly one console gets the figure under that console, a title on two or
// more shows it on none (Tetris on Game Boy and NES must not both claim it),
// and a title on none stays bare. Keyed entries pass through. The input is
// not changed. nil means "no snapshot yet" and takes one: render thread only.
func libraryPlaytimeForTitles(seconds map[string]int64, consoles map[string]map[string]bool) map[string]int64 {
	if seconds == nil {
		seconds = libraryPlaytimeSnapshot()
	}
	bare := false
	for key := range seconds {
		if system, _ := playtimeGameKeyParts(key); system == "" {
			bare = true
			break
		}
	}
	if !bare {
		return seconds
	}
	out := make(map[string]int64, len(seconds))
	for key, secs := range seconds {
		system, title := playtimeGameKeyParts(key)
		if system != "" {
			out[key] += secs
			continue
		}
		switch known := consoles[title]; len(known) {
		case 0:
			out[key] += secs
		case 1:
			out[playtimeGameKey(playtimeOnlyConsole(known), title)] += secs
		}
	}
	return out
}

// refreshLibraryView re-applies the saved view to the open panel without
// walking the card: sort, display titles and the hidden filter all change
// immediately after an options press.
func refreshLibraryView() {
	defer invalidateGameLibrarySystemsCache()
	if len(gameLib.lib.Systems) == 0 {
		return
	}
	// One snapshot for every console, resolved against the whole library.
	seconds := libraryPlaytimeForTitles(libraryPlaytimeSnapshot(),
		playtimeTitleConsoles(nil, nil, gameLib.lib.Systems))
	for i := range gameLib.lib.Systems {
		system := &gameLib.lib.Systems[i]
		libraryDecorateGames(system.Games, seconds)
		system.WithArt = countCoveredGames(system.Games)
	}
	systems := currentGameSystems()
	if len(systems) == 0 {
		gameLib.system, gameLib.cursor, gameLib.scroll = 0, 0, 0
		return
	}
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
}

// toggleGameHidden hides or restores the game the options sheet is for. The
// sheet closes when the last visible game disappears from under it; otherwise
// it stays up so the same A press can undo the change.
func toggleGameHidden() {
	if !gameOptsUI.open {
		return
	}
	game := gameOptsUI.game
	if game.ContentKey == "" {
		libraryDecorateGame(&game)
		gameOptsUI.game.ContentKey = game.ContentKey
	}
	next := !game.Hidden
	setLibraryGameHidden(game.ContentKey, next)
	gameOptsUI.game.Hidden = next
	refreshLibraryView()
	if next && !libraryShowHidden() {
		closeGameOptions()
		ShowToast("Hidden "+game.Label()+" — Game options → Show hidden brings it back", ToastKindInfo)
		return
	}
	if next {
		ShowToast("Hidden "+game.Label(), ToastKindInfo)
	} else {
		ShowToast(game.Label()+" is visible again", ToastKindSuccess)
	}
}

// applyGameDisplayTitle commits the keyboard's title for the options sheet's
// game. An empty field, or the file-derived title itself, clears the override;
// the ROM on disk is never renamed.
func applyGameDisplayTitle(text string) {
	if !gameOptsUI.open {
		return
	}
	game := gameOptsUI.game
	if game.ContentKey == "" {
		libraryDecorateGame(&game)
	}
	title := libraryCleanDisplayTitle(text)
	if strings.EqualFold(title, strings.TrimSpace(game.Title)) {
		title = ""
	}
	setLibraryGameDisplayTitle(game.ContentKey, title)
	game.DisplayTitle = title
	gameOptsUI.game.DisplayTitle = title
	refreshLibraryView()
	if title == "" {
		ShowToast("Display title reset for "+game.Title, ToastKindInfo)
		return
	}
	ShowToast("Display title set to "+title, ToastKindSuccess)
}

// filterHiddenLibrarySystems drops hidden games (and consoles left empty) when
// the user has not asked to see them. The Continue row is a view over the same
// games, so it is filtered too.
func filterHiddenLibrarySystems(systems []librarySystem, showHidden bool) []librarySystem {
	if showHidden {
		return systems
	}
	out := make([]librarySystem, 0, len(systems))
	for _, system := range systems {
		if len(system.Games) == 0 {
			continue
		}
		games := make([]libraryGame, 0, len(system.Games))
		for _, game := range system.Games {
			if !game.Hidden {
				games = append(games, game)
			}
		}
		if len(games) == 0 {
			continue
		}
		out = append(out, librarySystem{Name: system.Name, Games: games, WithArt: countCoveredGames(games)})
	}
	return out
}
