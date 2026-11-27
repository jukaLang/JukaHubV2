package main

// The Music scene's library view uses the media index, so a file found in
// Media and a file opened here have the same album and artwork.

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

type musicShelfRow struct {
	kind  string // artist, album, book or track; History's in musichistory.go
	key   string
	title string
	sub   string
	art   string
	path  string
	dur   float64
}

// The shelves past Radio: every song on the card, and the folders they sit
// in. They are last so the constants the History and Radio shelves use keep
// their numbers.
const (
	musicShelfTabTracks  = 5
	musicShelfTabFolders = 6
)

var musicShelf struct {
	open, fromShelf bool
	tab             int // albums, artists, audiobooks, history, radio, tracks, folders (musicShelfTabs)
	artist, album   string
	folder          string // the folder open from the Folders shelf
	search          string // the filter over title, artist and album
	cursor, scroll  int
	gen             uint64
	progressRev     uint64
	chapterRev      uint64
	histRev         uint64 // the musicHistoryRev the History rows were built at
	histReady       bool   // the listening history has been read
	histArmed       bool   // Clear listening history was chosen once
	histArtist      bool   // the open artist came from Top artists: Back returns there
	rows            []musicShelfRow
	hits            []sdl.Rect
	tabs            [musicShelfTabs]sdl.Rect
	back            sdl.Rect
	searchBtn       sdl.Rect
	up, down        sdl.Rect
}

func musicShelfOpen(config *Config) {
	musicShelf.open = true
	musicShelf.fromShelf = false
	musicShelf.artist, musicShelf.album, musicShelf.folder, musicShelf.search = "", "", "", ""
	musicShelf.cursor, musicShelf.scroll = 0, 0
	musicShelf.gen = 0
	mlibStartScan(config, false)
	openMusicPlayerScene(config)
}

func musicShelfBookKey(m *mlibModel, g mlibGroup) string {
	if len(g.items) == 0 {
		return ""
	}
	common := filepath.Dir(m.items[g.items[0]].Path)
	for _, i := range g.items[1:] {
		for !isPathWithin(filepath.Dir(m.items[i].Path), common) {
			parent := filepath.Dir(common)
			if parent == common {
				return common
			}
			common = parent
		}
	}
	return common
}

// musicNaturalLess compares two names the way a person reads them: runs of
// digits are numbers, so "Track 2" comes before "Track 10", and letters
// compare without regard to case. Equal numbers with leading zeros keep the
// shorter spelling first, so the order is still total.
func musicNaturalLess(a, b string) bool {
	for ia, ib := 0, 0; ia < len(a) && ib < len(b); {
		if a[ia] >= '0' && a[ia] <= '9' && b[ib] >= '0' && b[ib] <= '9' {
			ja, jb := ia, ib
			for ja < len(a) && a[ja] >= '0' && a[ja] <= '9' {
				ja++
			}
			for jb < len(b) && b[jb] >= '0' && b[jb] <= '9' {
				jb++
			}
			na := strings.TrimLeft(a[ia:ja], "0")
			nb := strings.TrimLeft(b[ib:jb], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c < 0
			}
			if ja-ia != jb-ib {
				return ja-ia < jb-ib
			}
			ia, ib = ja, jb
			continue
		}
		ra, sa := utf8.DecodeRuneInString(a[ia:])
		rb, sb := utf8.DecodeRuneInString(b[ib:])
		la, lb := unicode.ToLower(ra), unicode.ToLower(rb)
		if la != lb {
			return la < lb
		}
		ia += sa
		ib += sb
	}
	return len(a) < len(b)
}

// musicShelfMatch reports whether any of the fields contains the search, case
// folded. An empty search matches everything; title, artist and album are the
// three answers the Music search promises.
func musicShelfMatch(search string, fields ...string) bool {
	search = strings.ToLower(strings.TrimSpace(search))
	if search == "" {
		return true
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), search) {
			return true
		}
	}
	return false
}

// musicShelfItemMatches is the search over one song.
func musicShelfItemMatches(it mlibItem, search string) bool {
	return musicShelfMatch(search, it.Title, it.Artist, it.Album)
}

// musicShelfItemSub is a song's second line: its artist and album.
func musicShelfItemSub(it mlibItem) string {
	sub := strings.TrimSpace(strings.Trim(it.Artist+" · "+it.Album, " ·"))
	if sub == "" {
		sub = filepath.Base(filepath.Dir(it.Path))
	}
	return sub
}

// musicShelfSongRow is one song's row. key is the album or folder it plays
// with; an empty key means the whole Tracks shelf.
func musicShelfSongRow(it mlibItem, key string) musicShelfRow {
	return musicShelfRow{kind: "track", key: key, title: it.Title, sub: musicShelfItemSub(it),
		path: it.Path, dur: it.Duration}
}

// musicShelfTracksAll is the Tracks shelf: every song on the card, natural
// sorted by title, with the search applied.
func musicShelfTracksAll(m *mlibModel, search string) []musicShelfRow {
	rows := make([]musicShelfRow, 0, len(m.items))
	for _, it := range m.items {
		if it.Video || !musicShelfItemMatches(it, search) {
			continue
		}
		rows = append(rows, musicShelfSongRow(it, ""))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].title != rows[j].title {
			return musicNaturalLess(rows[i].title, rows[j].title)
		}
		return musicNaturalLess(rows[i].path, rows[j].path)
	})
	return rows
}

// musicShelfFolderName is a folder's label: its own name, or the drive path
// when the folder is a root.
func musicShelfFolderName(dir string) string {
	name := filepath.Base(filepath.Clean(dir))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return dir
	}
	return name
}

// musicShelfFolders lists the folders that hold songs, natural sorted, with
// the search applied to the songs inside them.
func musicShelfFolders(m *mlibModel, search string) []musicShelfRow {
	counts := map[string]int{}
	for _, it := range m.items {
		if it.Video || !musicShelfItemMatches(it, search) {
			continue
		}
		counts[filepath.Dir(it.Path)]++
	}
	rows := make([]musicShelfRow, 0, len(counts))
	for dir, n := range counts {
		rows = append(rows, musicShelfRow{kind: "folder", key: dir, title: musicShelfFolderName(dir),
			sub: strconv.Itoa(n) + " track(s)", path: dir})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].title != rows[j].title {
			return musicNaturalLess(rows[i].title, rows[j].title)
		}
		return musicNaturalLess(rows[i].key, rows[j].key)
	})
	return rows
}

// musicShelfFolderSongs is one folder's songs, natural sorted by title.
func musicShelfFolderSongs(m *mlibModel, folder, search string) []musicShelfRow {
	rows := make([]musicShelfRow, 0, 16)
	for _, it := range m.items {
		if it.Video || filepath.Dir(it.Path) != folder || !musicShelfItemMatches(it, search) {
			continue
		}
		rows = append(rows, musicShelfSongRow(it, folder))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].title != rows[j].title {
			return musicNaturalLess(rows[i].title, rows[j].title)
		}
		return musicNaturalLess(rows[i].path, rows[j].path)
	})
	return rows
}

func musicShelfBuildRows(m *mlibModel, tab int, artist, album, folder, search string) []musicShelfRow {
	if m == nil {
		return nil
	}
	if folder != "" {
		return musicShelfFolderSongs(m, folder, search)
	}
	if tab == musicShelfTabTracks {
		return musicShelfTracksAll(m, search)
	}
	if tab == musicShelfTabFolders {
		return musicShelfFolders(m, search)
	}
	if album != "" {
		if gi, ok := m.albumIdx[album]; ok {
			g := m.albums[gi]
			rows := make([]musicShelfRow, 0, len(g.items))
			for _, i := range g.items {
				it := m.items[i]
				if !musicShelfItemMatches(it, search) {
					continue
				}
				row := musicShelfSongRow(it, album)
				row.sub, row.art = g.sub, g.art
				rows = append(rows, row)
			}
			return rows
		}
		return nil
	}
	if tab == 2 {
		rows := make([]musicShelfRow, 0, len(m.books))
		for _, g := range m.books {
			if !musicShelfMatch(search, g.title, g.sub) {
				continue
			}
			key := musicShelfBookKey(m, g)
			progress := audioBookProgress(key)
			count := len(g.items)
			if len(g.items) == 1 && len(g.items) > 0 {
				if ch := audioChaptersFor(m.items[g.items[0]].Path); len(ch) > 0 {
					count = len(ch)
				}
			}
			sub := g.sub + " · " + strconv.Itoa(count) + " chapter(s)"
			if progress.Finished {
				sub += " · Finished"
			} else if progress.Path != "" {
				sub += " · Continue"
				if chapter := audioResumeChapter(progress.Path); chapter != "" {
					sub += " at " + chapter // a chapter inside one file (audiobook_chapters.go)
				}
			}
			rows = append(rows, musicShelfRow{kind: "book", key: g.key, title: g.title, sub: sub, art: g.art})
		}
		return rows
	}
	if tab == 1 && artist == "" {
		byArtist := map[string]musicShelfRow{}
		for _, g := range m.albums {
			if !musicShelfMatch(search, g.sub) {
				continue
			}
			key := strings.ToLower(g.sub)
			row := byArtist[key]
			if row.kind == "" {
				row = musicShelfRow{kind: "artist", key: key, title: g.sub, art: g.art}
			}
			n, _ := strconv.Atoi(row.sub)
			row.sub = strconv.Itoa(n + 1)
			byArtist[key] = row
		}
		rows := make([]musicShelfRow, 0, len(byArtist))
		for _, row := range byArtist {
			row.sub += " album(s)"
			rows = append(rows, row)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
		return rows
	}
	rows := make([]musicShelfRow, 0, len(m.albums))
	for _, g := range m.albums {
		if artist != "" && strings.ToLower(g.sub) != artist {
			continue
		}
		if !musicShelfMatch(search, g.title, g.sub) {
			continue
		}
		rows = append(rows, musicShelfRow{kind: "album", key: g.key, title: g.title,
			sub: g.sub + " · " + strconv.Itoa(len(g.items)) + " track(s)", art: g.art})
	}
	return rows
}

func musicShelfRefresh() {
	if musicShelf.tab == musicShelfTabHistory {
		musicShelfRefreshHistory() // works before the library is read, too
		return
	}
	if musicShelf.tab == musicShelfTabRadio {
		musicShelfRefreshRadio() // the directory and your stations (radiodir_ui.go)
		return
	}
	m := mlibCurrentModel()
	if m == nil {
		musicShelf.rows = nil
		return
	}
	progressRev := audioBookRevision.Load()
	chapterRev := audioChapterRevision.Load()
	if musicShelf.gen == m.gen && (musicShelf.tab != 2 || (musicShelf.progressRev == progressRev && musicShelf.chapterRev == chapterRev)) {
		return
	}
	musicShelf.rows = musicShelfBuildRows(m, musicShelf.tab, musicShelf.artist, musicShelf.album, musicShelf.folder, musicShelf.search)
	musicShelf.gen = m.gen
	musicShelf.progressRev = progressRev
	musicShelf.chapterRev = chapterRev
	if musicShelf.cursor >= len(musicShelf.rows) {
		musicShelf.cursor = len(musicShelf.rows) - 1
	}
	if musicShelf.cursor < 0 {
		musicShelf.cursor = 0
	}
}

func musicShelfInvalidate() {
	musicShelf.gen = 0
	musicShelf.cursor, musicShelf.scroll = 0, 0
	musicShelf.histArmed = false
}

func musicShelfPlayGroup(config *Config, key string, book bool, startPath string) {
	m := mlibCurrentModel()
	if m == nil {
		return
	}
	var g mlibGroup
	if book {
		gi, ok := m.bookIdx[key]
		if !ok {
			return
		}
		g = m.books[gi]
	} else {
		gi, ok := m.albumIdx[key]
		if !ok {
			return
		}
		g = m.albums[gi]
	}
	tracks := make([]musicTrack, 0, len(g.items))
	start := 0
	bookKey := ""
	if book {
		bookKey = musicShelfBookKey(m, g)
		if p := audioBookProgress(bookKey); !p.Finished && startPath == "" {
			startPath = p.Path
		}
	}
	for _, i := range g.items {
		it := m.items[i]
		if startPath != "" && sameAudioPath(it.Path, startPath) {
			start = len(tracks)
		}
		tracks = append(tracks, musicTrack{Path: it.Path, Name: it.Title, Duration: it.Duration})
	}
	if !musicPlayPlaylist(tracks, start) {
		return
	}
	music.mu.Lock()
	music.bookKey = bookKey
	if book {
		music.shuffle = false
		music.order = nil
		music.repeatMode = musicRepeatOff
	}
	music.mu.Unlock()
	musicShelf.open = false
	musicShelf.fromShelf = true
	if !gameLib.open {
		closeGameLibraryArt()
	}
	openMusicPlayerScene(config)
	musicPlayTrack(start)
}

// musicShelfPlayRows plays a shelf's rows as they are now: the Tracks shelf,
// or one folder, so a search plays only what it found.
func musicShelfPlayRows(config *Config, rows []musicShelfRow, startPath string) {
	tracks := make([]musicTrack, 0, len(rows))
	start := 0
	for _, row := range rows {
		if row.kind != "track" || row.path == "" {
			continue
		}
		if startPath != "" && sameAudioPath(row.path, startPath) {
			start = len(tracks)
		}
		tracks = append(tracks, musicTrack{Path: row.path, Name: row.title, Duration: row.dur})
	}
	if len(tracks) == 0 || !musicPlayPlaylist(tracks, start) {
		return
	}
	music.mu.Lock()
	music.bookKey = ""
	music.mu.Unlock()
	musicShelf.open = false
	musicShelf.fromShelf = true
	if !gameLib.open {
		closeGameLibraryArt()
	}
	openMusicPlayerScene(config)
	musicPlayTrack(start)
}

func musicShelfChoose(config *Config) {
	musicShelfRefresh()
	if musicShelf.cursor < 0 || musicShelf.cursor >= len(musicShelf.rows) {
		return
	}
	row := musicShelf.rows[musicShelf.cursor]
	switch row.kind {
	case "heading":
		musicShelfSkipHeading(1) // a tap on a section's name
		return
	case "artist":
		musicShelf.artist = row.key
		musicShelfInvalidate()
		musicShelf.histArtist = false
	case "album":
		musicShelf.album = row.key
		musicShelfInvalidate()
	case "folder":
		musicShelf.folder = row.key
		musicShelfInvalidate()
	case "book":
		musicShelfPlayGroup(config, row.key, true, "")
	case "track":
		switch {
		case row.key == "":
			musicShelfPlayRows(config, musicShelf.rows, row.path)
		case row.key == musicShelf.folder:
			musicShelfPlayRows(config, musicShelf.rows, row.path)
		default:
			musicShelfPlayGroup(config, row.key, false, row.path)
		}
	case "listen", "topartist", "clearhistory":
		musicShelfChooseHistory(config, row)
	case "nowplaying", "radiodir", "station":
		// What plays is Play / Stop in place, with its own click
		// (radiodir_ui.go).
		if musicShelfChooseNow(row) {
			return
		}
		musicShelfChooseRadio(config, row)
	}
	PlayActivateSound()
}

func musicShelfBack(config *Config) {
	if musicShelf.folder != "" {
		musicShelf.folder = ""
		musicShelfInvalidate()
	} else if musicShelf.album != "" {
		musicShelf.album = ""
		musicShelfInvalidate()
	} else if musicShelf.artist != "" {
		musicShelf.artist = ""
		if musicShelf.histArtist {
			musicShelf.tab, musicShelf.histArtist = musicShelfTabHistory, false
		}
		musicShelfInvalidate()
	} else if musicShelf.search != "" {
		musicShelf.search = ""
		musicShelfInvalidate()
	} else {
		musicShelf.open = false
		musicShelf.fromShelf = false
		if !gameLib.open {
			closeGameLibraryArt()
		}
		musicLeavePlayer(config)
		goBackScene(config)
	}
	PlayBackSound()
}

func musicShelfTab(delta int) {
	musicShelf.tab = (musicShelf.tab + delta + musicShelfTabs) % musicShelfTabs
	musicShelf.artist, musicShelf.album, musicShelf.folder, musicShelf.search = "", "", "", ""
	musicShelfInvalidate()
	PlayNavSound()
}

// musicShelfSearchPrompt opens the on-screen keyboard on the current search.
// Done replaces the filter; an empty answer clears it.
func musicShelfSearchPrompt() {
	osPromptOpen("Search music", "Title, artist or album", "Search", musicShelf.search, 80, func(typed string) {
		typed = strings.TrimSpace(typed)
		if typed == musicShelf.search {
			return
		}
		musicShelf.search = typed
		musicShelf.cursor, musicShelf.scroll = 0, 0
		musicShelfInvalidate()
	})
}

func musicShelfMove(delta int) {
	musicShelfRefresh()
	if len(musicShelf.rows) == 0 {
		return
	}
	musicShelf.cursor += delta
	if musicShelf.cursor < 0 {
		musicShelf.cursor = 0
	}
	if musicShelf.cursor >= len(musicShelf.rows) {
		musicShelf.cursor = len(musicShelf.rows) - 1
	}
	musicShelfSkipHeading(delta)
	musicShelfDisarm()
	PlayNavSound()
}

func musicShelfHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if !musicShelf.open || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		musicShelfMove(-1)
	case sdl.K_DOWN:
		musicShelfMove(1)
	case sdl.K_LEFT, sdl.K_PAGEUP:
		musicShelfTab(-1)
	case sdl.K_RIGHT, sdl.K_PAGEDOWN:
		musicShelfTab(1)
	case sdl.K_RETURN:
		musicShelfChoose(config)
	case sdl.K_ESCAPE:
		musicShelfBack(config)
	case sdl.K_y:
		musicShelfSearchPrompt()
	case sdl.K_F5:
		mlibStartScan(config, true)
	case sdl.K_x:
		// The Now playing row's player, as X on a pad (radiodir_ui.go).
		if musicShelfNowFocused() {
			radioOpenPlayer(config, true)
		}
	default:
		return true
	}
	return true
}

func musicShelfHandlePad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if !musicShelf.open || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		musicShelfMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		musicShelfMove(1)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		musicShelfTab(-1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER, sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		musicShelfTab(1)
	case sdl.CONTROLLER_BUTTON_A:
		musicShelfChoose(config)
	case sdl.CONTROLLER_BUTTON_B:
		musicShelfBack(config)
	case sdl.CONTROLLER_BUTTON_X:
		// On the Now playing row X opens the player (radiodir_ui.go).
		if musicShelfNowFocused() {
			radioOpenPlayer(config, true)
		} else {
			mlibStartScan(config, true)
		}
	case sdl.CONTROLLER_BUTTON_Y:
		musicShelfSearchPrompt()
	default:
		return true
	}
	return true
}

func musicShelfClick(mx, my int32, config *Config) bool {
	if !musicShelf.open {
		return false
	}
	if pointInRect(mx, my, musicShelf.back) {
		musicShelfBack(config)
		return true
	}
	if pointInRect(mx, my, musicShelf.up) {
		musicShelfMove(-1)
		return true
	}
	if pointInRect(mx, my, musicShelf.down) {
		musicShelfMove(1)
		return true
	}
	if pointInRect(mx, my, musicShelf.searchBtn) {
		musicShelfSearchPrompt()
		return true
	}
	for i, rect := range musicShelf.tabs {
		if pointInRect(mx, my, rect) {
			musicShelf.tab = i
			musicShelf.artist, musicShelf.album, musicShelf.folder, musicShelf.search = "", "", "", ""
			musicShelfInvalidate()
			return true
		}
	}
	for i, rect := range musicShelf.hits {
		if pointInRect(mx, my, rect) {
			musicShelf.cursor = musicShelf.scroll + i
			musicShelfChoose(config)
			return true
		}
	}
	return true
}

func musicShelfWheel(delta int32) bool {
	// The iPod layout's lists scroll the same way (musicpod.go).
	if musicPodWheel(delta) {
		return true
	}
	if !musicShelf.open {
		return false
	}
	// One row a notch, whatever the wheel reports (±2 or ±3 on some mice).
	switch {
	case delta > 0:
		musicShelfMove(-1)
	case delta < 0:
		musicShelfMove(1)
	}
	return true
}

func renderMusicShelf(renderer *sdl.Renderer, config *Config) {
	musicShelfRefresh()
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	font, _ := getCachedFont(config, "small")
	big, _ := getCachedFont(config, "medium")
	if font == nil {
		renderStatusBar(renderer, config)
		return
	}
	if big == nil {
		big = font
	}
	x, y := int32(24), headerBarHeight()+22
	w := screenWidth - 2*x
	drawCard(renderer, x, y, w, screenHeight-y-56, 18)
	title := "Music"
	if musicShelf.folder != "" {
		title = musicShelfFolderName(musicShelf.folder)
	} else if musicShelf.album != "" {
		title = "Tracks"
	} else if musicShelf.artist != "" {
		title = "Albums by " + musicShelf.artist
	}
	if musicShelf.search != "" {
		title += " · " + musicShelf.search
	}
	renderText(renderer, config, big, ellipsize(big, title, w-48), ColorTextPrimary(), x+22, y+18)
	labels := [musicShelfTabs]string{"Albums", "Artists", "Audiobooks", "History", "Radio", "Tracks", "Folders"}
	chipY := y + 66
	chipW := (w - 44 - 10*(musicShelfTabs-1)) / musicShelfTabs
	for i, label := range labels {
		rect := sdl.Rect{X: x + 22 + int32(i)*(chipW+10), Y: chipY, W: chipW, H: 42}
		musicShelf.tabs[i] = rect
		col := WithAlpha(ColorPanelRaised, 205)
		if i == musicShelf.tab {
			col = WithAlpha(ColorCardFocus, 245)
		}
		fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, col)
		renderText(renderer, config, font, label, ColorTextPrimary(), rect.X+12, rect.Y+11)
	}
	listY := chipY + 55
	rowH := int32(70)
	visible := int((screenHeight - 68 - listY) / rowH)
	if visible < 1 {
		visible = 1
	}
	if musicShelf.cursor < musicShelf.scroll {
		musicShelf.scroll = musicShelf.cursor
	}
	if musicShelf.cursor >= musicShelf.scroll+visible {
		musicShelf.scroll = musicShelf.cursor - visible + 1
	}
	if musicShelf.scroll < 0 {
		musicShelf.scroll = 0
	}
	musicShelf.hits = musicShelf.hits[:0]
	if len(musicShelf.rows) == 0 {
		msg := "Scanning your music…"
		if musicShelf.tab == musicShelfTabHistory {
			msg = musicHistoryEmptyLine()
		} else if !mlibScanning.Load() {
			switch {
			case musicShelf.search != "":
				msg = "No music matches your search. Press Y to change it."
			case musicShelf.folder != "":
				msg = "No tracks in this folder."
			case musicShelf.album != "":
				msg = "No tracks found in this album."
			case musicShelf.tab == 2:
				msg = "No audiobooks yet. Add audio to an Audiobooks folder."
			case musicShelf.tab == 1 && musicShelf.artist == "":
				msg = "No artists here yet. Add music to the Music folder."
			case musicShelf.tab == musicShelfTabTracks || musicShelf.tab == musicShelfTabFolders:
				msg = "No tracks here yet. Add music to the Music folder."
			default:
				msg = "No albums here yet. Add music to the Music folder."
			}
		}
		renderText(renderer, config, font, msg, ColorTextSecondary(), x+28, listY+22)
	}
	// The Radio shelf's Now playing row and its station read as they are now
	// (radiodir_ui.go).
	np, npOK := musicNow()
	for i := musicShelf.scroll; i < len(musicShelf.rows) && i < musicShelf.scroll+visible; i++ {
		row := radioShelfNowRow(musicShelf.rows[i], np, npOK)
		rect := sdl.Rect{X: x + 16, Y: listY + int32(i-musicShelf.scroll)*rowH, W: w - 32, H: rowH - 6}
		if row.kind == "heading" {
			// A History section's name, under the rows before it.
			renderText(renderer, config, big, ellipsize(big, row.title, rect.W-16), ColorTextSecondary(), rect.X+8, rect.Y+rowH-12-int32(big.Height()))
			musicShelf.hits = append(musicShelf.hits, rect)
			continue
		}
		if i == musicShelf.cursor {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(ColorCardFocus, 255))
		}
		art := sdl.Rect{X: rect.X + 8, Y: rect.Y + 6, W: 52, H: 52}
		fillRoundedRect(renderer, art.X, art.Y, art.W, art.H, 7, WithAlpha(ColorPanelRaised, 220))
		if tex := artTexture(renderer, row.art); tex != nil {
			_, _, tw, th, err := tex.Query()
			if err == nil && tw > 0 && th > 0 {
				dst := gaFit(tw, th, art.W, art.H)
				dst.X += art.X
				dst.Y += art.Y
				_ = renderer.Copy(tex, nil, &dst)
			}
		}
		if row.kind == "nowplaying" && npOK {
			drawHomeGlyphTinted(renderer, radioShelfNowGlyph(np), sdl.Rect{X: art.X + 14, Y: art.Y + 14, W: art.W - 28, H: art.H - 28}, ColorTextPrimary())
		}
		tx := art.X + art.W + 15
		renderText(renderer, config, font, ellipsize(font, row.title, rect.W-(tx-rect.X)-16), ColorTextPrimary(), tx, rect.Y+8)
		renderText(renderer, config, font, ellipsize(font, row.sub, rect.W-(tx-rect.X)-16), ColorTextSecondary(), tx, rect.Y+34)
		musicShelf.hits = append(musicShelf.hits, rect)
	}
	footer := "A Open · L1/R1 Shelves · Y Search · X Rescan"
	if musicShelf.search != "" {
		footer = "Search: " + musicShelf.search + " · Y Change · B Clear"
	}
	if now, ok := musicShelfNowFooter(); ok {
		footer = now // the Now playing row's Play / Stop and player
	}
	renderText(renderer, config, font, footer, ColorTextSecondary(), x+24, screenHeight-46)
	musicShelf.searchBtn = sdl.Rect{X: x + w - 300, Y: screenHeight - 52, W: 92, H: 34}
	musicShelf.up = sdl.Rect{X: x + w - 180, Y: screenHeight - 52, W: 34, H: 34}
	musicShelf.down = sdl.Rect{X: x + w - 140, Y: screenHeight - 52, W: 34, H: 34}
	musicShelf.back = sdl.Rect{X: x + w - 92, Y: screenHeight - 52, W: 76, H: 34}
	for _, step := range []struct {
		rect  sdl.Rect
		label string
	}{{musicShelf.up, "↑"}, {musicShelf.down, "↓"}} {
		fillRoundedRect(renderer, step.rect.X, step.rect.Y, step.rect.W, step.rect.H, 8, WithAlpha(ColorPanelRaised, 220))
		renderText(renderer, config, font, step.label, ColorTextPrimary(), step.rect.X+10, step.rect.Y+8)
	}
	fillRoundedRect(renderer, musicShelf.back.X, musicShelf.back.Y, musicShelf.back.W, musicShelf.back.H, 8, WithAlpha(ColorPanelRaised, 220))
	renderText(renderer, config, font, "Back", ColorTextPrimary(), musicShelf.back.X+14, musicShelf.back.Y+8)
	fillRoundedRect(renderer, musicShelf.searchBtn.X, musicShelf.searchBtn.Y, musicShelf.searchBtn.W, musicShelf.searchBtn.H, 8, WithAlpha(ColorPanelRaised, 220))
	renderText(renderer, config, font, "Search", ColorTextPrimary(), musicShelf.searchBtn.X+14, musicShelf.searchBtn.Y+8)
	renderStatusBar(renderer, config)
}
