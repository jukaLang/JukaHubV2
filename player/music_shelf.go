package main

// The Music scene's library view uses the media index, so a file found in
// Media and a file opened here have the same album and artwork.

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

type musicShelfRow struct {
	kind  string // artist, album, book or track
	key   string
	title string
	sub   string
	art   string
	path  string
}

var musicShelf struct {
	open, fromShelf bool
	tab             int // albums, artists, audiobooks
	artist, album   string
	cursor, scroll  int
	gen             uint64
	progressRev     uint64
	rows            []musicShelfRow
	hits            []sdl.Rect
	tabs            [3]sdl.Rect
	back            sdl.Rect
	up, down        sdl.Rect
}

func musicShelfOpen(config *Config) {
	musicShelf.open = true
	musicShelf.fromShelf = false
	musicShelf.artist, musicShelf.album = "", ""
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

func musicShelfBuildRows(m *mlibModel, tab int, artist, album string) []musicShelfRow {
	if m == nil {
		return nil
	}
	if album != "" {
		if gi, ok := m.albumIdx[album]; ok {
			g := m.albums[gi]
			rows := make([]musicShelfRow, 0, len(g.items))
			for _, i := range g.items {
				it := m.items[i]
				rows = append(rows, musicShelfRow{kind: "track", key: album, title: it.Title,
					sub: g.sub, art: g.art, path: it.Path})
			}
			return rows
		}
		return nil
	}
	if tab == 2 {
		rows := make([]musicShelfRow, 0, len(m.books))
		for _, g := range m.books {
			key := musicShelfBookKey(m, g)
			progress := audioBookProgress(key)
			sub := g.sub + " · " + strconv.Itoa(len(g.items)) + " chapter(s)"
			if progress.Finished {
				sub += " · Finished"
			} else if progress.Path != "" {
				sub += " · Continue"
			}
			rows = append(rows, musicShelfRow{kind: "book", key: g.key, title: g.title, sub: sub, art: g.art})
		}
		return rows
	}
	if tab == 1 && artist == "" {
		byArtist := map[string]musicShelfRow{}
		for _, g := range m.albums {
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
		rows = append(rows, musicShelfRow{kind: "album", key: g.key, title: g.title,
			sub: g.sub + " · " + strconv.Itoa(len(g.items)) + " track(s)", art: g.art})
	}
	return rows
}

func musicShelfRefresh() {
	m := mlibCurrentModel()
	if m == nil {
		musicShelf.rows = nil
		return
	}
	progressRev := audioBookRevision.Load()
	if musicShelf.gen == m.gen && (musicShelf.tab != 2 || musicShelf.progressRev == progressRev) {
		return
	}
	musicShelf.rows = musicShelfBuildRows(m, musicShelf.tab, musicShelf.artist, musicShelf.album)
	musicShelf.gen = m.gen
	musicShelf.progressRev = progressRev
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

func musicShelfChoose(config *Config) {
	musicShelfRefresh()
	if musicShelf.cursor < 0 || musicShelf.cursor >= len(musicShelf.rows) {
		return
	}
	row := musicShelf.rows[musicShelf.cursor]
	switch row.kind {
	case "artist":
		musicShelf.artist = row.key
		musicShelfInvalidate()
	case "album":
		musicShelf.album = row.key
		musicShelfInvalidate()
	case "book":
		musicShelfPlayGroup(config, row.key, true, "")
	case "track":
		musicShelfPlayGroup(config, row.key, false, row.path)
	}
	PlayActivateSound()
}

func musicShelfBack(config *Config) {
	if musicShelf.album != "" {
		musicShelf.album = ""
		musicShelfInvalidate()
	} else if musicShelf.artist != "" {
		musicShelf.artist = ""
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
	musicShelf.tab = (musicShelf.tab + delta + 3) % 3
	musicShelf.artist, musicShelf.album = "", ""
	musicShelfInvalidate()
	PlayNavSound()
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
	case sdl.K_F5:
		mlibStartScan(config, true)
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
		mlibStartScan(config, true)
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
	for i, rect := range musicShelf.tabs {
		if pointInRect(mx, my, rect) {
			musicShelf.tab = i
			musicShelf.artist, musicShelf.album = "", ""
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
	if !musicShelf.open {
		return false
	}
	if delta != 0 {
		musicShelfMove(-int(delta))
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
	if musicShelf.album != "" {
		title = "Tracks"
	} else if musicShelf.artist != "" {
		title = "Albums by " + musicShelf.artist
	}
	renderText(renderer, config, big, ellipsize(big, title, w-48), ColorTextPrimary(), x+22, y+18)
	labels := [3]string{"Albums", "Artists", "Audiobooks"}
	chipY := y + 66
	chipW := (w - 64) / 3
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
		if !mlibScanning.Load() {
			switch {
			case musicShelf.album != "":
				msg = "No tracks found in this album."
			case musicShelf.tab == 2:
				msg = "No audiobooks yet. Add audio to an Audiobooks folder."
			case musicShelf.tab == 1 && musicShelf.artist == "":
				msg = "No artists here yet. Add music to the Music folder."
			default:
				msg = "No albums here yet. Add music to the Music folder."
			}
		}
		renderText(renderer, config, font, msg, ColorTextSecondary(), x+28, listY+22)
	}
	for i := musicShelf.scroll; i < len(musicShelf.rows) && i < musicShelf.scroll+visible; i++ {
		row := musicShelf.rows[i]
		rect := sdl.Rect{X: x + 16, Y: listY + int32(i-musicShelf.scroll)*rowH, W: w - 32, H: rowH - 6}
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
		tx := art.X + art.W + 15
		renderText(renderer, config, font, ellipsize(font, row.title, rect.W-(tx-rect.X)-16), ColorTextPrimary(), tx, rect.Y+8)
		renderText(renderer, config, font, ellipsize(font, row.sub, rect.W-(tx-rect.X)-16), ColorTextSecondary(), tx, rect.Y+34)
		musicShelf.hits = append(musicShelf.hits, rect)
	}
	renderText(renderer, config, font, "A Open · L1/R1 Shelves · X Rescan", ColorTextSecondary(), x+24, screenHeight-46)
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
	renderStatusBar(renderer, config)
}
