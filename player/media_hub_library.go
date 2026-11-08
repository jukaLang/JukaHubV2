package main

// media_hub_library.go — the Media screen's Library tab: the films, TV shows
// and music on the card (mlib.go) as rows of cards like For you — Continue
// watching, Recently added, Movies, TV shows, Music — and the lists a card
// opens: a show's episodes, an album's songs, every film.
//
// The tab reuses the screen's cards, rows, grid, details sheet and controls.
// What is its own is the drill-down — a small stack, so B goes back up one
// level before it leaves the screen — and the scan's states. Everything here
// works with the network off.

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// mlibCardKind is what a Library card stands for.
type mlibCardKind uint8

const (
	mlibCardNone mlibCardKind = iota
	// mlibCardVideo is a film or an episode: A plays it, resuming.
	mlibCardVideo
	// mlibCardTrack is a song: A plays its folder in the music player.
	mlibCardTrack
	// mlibCardShow and mlibCardAlbum open their episodes or songs.
	mlibCardShow
	mlibCardAlbum
	// The See all cards at the end of a row open the whole list.
	mlibCardAllMovies
	mlibCardAllShows
	mlibCardAllAlbums
)

// mlibCardOpens reports a card that opens a list rather than playing.
func mlibCardOpens(k mlibCardKind) bool { return k >= mlibCardShow }

// mlibDrill is one level of the Library's drill-down: the list, its name and
// what it holds, and where the ring and the scroll were when a deeper level
// was opened over it.
type mlibDrill struct {
	kind   mlibCardKind
	key    string
	title  string
	sub    string
	index  int
	scroll float64
}

// mlibNav is the open drill-down, innermost last; empty shows the rows.
// rowsScroll is the rows' page scroll, put back when the last list closes.
var mlibNav struct {
	stack      []mlibDrill
	rowsScroll float64
}

// mlibStatusCache keeps the scan line between frames; it is rebuilt only when
// the count it shows changes.
var mlibStatusCache struct {
	found int
	text  string
}

// mediaTabHasRows reports a tab drawn as rows of cards: For you, and the
// Library while no list is open.
func mediaTabHasRows(t mediaTab) bool {
	return t == mediaTabForYou || (t == mediaTabLibrary && len(mlibNav.stack) == 0)
}

// mlibEnterTab runs when the Library is shown: look for changes (a quick pass
// when no folder moved) and pick up where the player left the videos.
func mlibEnterTab(config *Config) {
	dlSettingsSync(config)
	mlibStartScan(config, false)
	go mlibSyncResume()
}

// mlibScanLine is the line the Library shows while a scan runs.
func mlibScanLine(found int) string {
	if mlibStatusCache.text == "" || mlibStatusCache.found != found {
		mlibStatusCache.found = found
		if found <= 0 {
			mlibStatusCache.text = "Looking for movies, shows and music…"
		} else {
			mlibStatusCache.text = "Scanning · " + pluralizeCount(found, "file", "files") + " found"
		}
	}
	return mlibStatusCache.text
}

// mlibEmptyBody says where the library looks, for its empty state.
func mlibEmptyBody() string {
	if IsTrimuiDevice() {
		return "Put movies in Videos, Movies or TV and songs in Music on the SD card."
	}
	return "Put movies in your Videos folder and songs in your Music folder."
}

// ── Cards ────────────────────────────────────────────────────────────────────

// mlibItemTitle is a file's title as its card shows it.
func mlibItemTitle(it mlibItem) string {
	title := strings.TrimSpace(it.Title)
	if title == "" && it.Show != "" {
		return "Episode " + strconv.Itoa(it.Episode)
	}
	if title == "" {
		return dlTitleFromFile(it.Path)
	}
	return title
}

// mlibItemMeta is a file card's muted line. inList is true inside a show or an
// album, where the card need not repeat the show or the album.
func mlibItemMeta(it mlibItem, inList bool) string {
	switch {
	case it.Show != "":
		if inList {
			return "Season " + strconv.Itoa(it.Season) + " · Episode " + strconv.Itoa(it.Episode)
		}
		return it.Show + " · S" + strconv.Itoa(it.Season) + " E" + strconv.Itoa(it.Episode)
	case !it.Video:
		if inList && it.Track > 0 {
			return "Track " + strconv.Itoa(it.Track)
		}
		if inList {
			return dlFirstNonEmpty(it.Artist, "Unknown artist")
		}
		return dlFirstNonEmpty(it.Artist, "Unknown artist") + " · " + dlFirstNonEmpty(it.Album, "Unknown album")
	case it.Year > 0:
		return "Movie · " + strconv.Itoa(it.Year)
	}
	return "Movie"
}

// mlibItemDetail is the details sheet's line about the file itself.
func mlibItemDetail(it mlibItem) string {
	return humanBytes(it.Size) + " · " + filepath.Base(filepath.Dir(it.Path))
}

// mlibItemCard is the card of one file of the library.
func mlibItemCard(m *mlibModel, i int, inList bool) mediaCard {
	it := m.items[i]
	c := mediaCard{
		title:    mlibItemTitle(it),
		path:     it.Path,
		mlibKind: mlibCardVideo,
		mlibKey:  it.Path,
		glyph:    "video",
	}
	if !it.Video {
		c.mlibKind = mlibCardTrack
		c.glyph = "music"
		c.artContain = true
	}
	c.video = VideoInfo{Title: c.title, Uploader: it.Artist, Duration: it.Duration, URL: it.Path}
	if it.Duration > 0 {
		c.duration = formatDuration(it.Duration)
	}
	if it.Video {
		c.progress = mediaProgressFraction(it.Position, it.Duration)
	}
	if it.Art != "" {
		c.art[0] = "file:" + it.Art
	}
	if it.Poster != "" {
		c.art[1] = "file:" + it.Poster
	}
	c.meta = mlibItemMeta(it, inList)
	c.detail = mlibItemDetail(it)
	return c
}

// mlibGroupCard is the card of a show or an album.
func mlibGroupCard(g mlibGroup, kind mlibCardKind) mediaCard {
	c := mediaCard{title: g.title, mlibKind: kind, mlibKey: g.key, glyph: "video"}
	c.video = VideoInfo{Title: g.title}
	if g.art != "" {
		c.art[0] = "file:" + g.art
	}
	if kind == mlibCardAlbum {
		c.glyph = "music"
		c.artContain = true
		c.meta = g.sub + " · " + pluralizeCount(len(g.items), "song", "songs")
		return c
	}
	c.meta = pluralizeCount(len(g.items), "episode", "episodes")
	if g.seasons > 1 {
		c.meta = pluralizeCount(g.seasons, "season", "seasons") + " · " + c.meta
	}
	return c
}

// mlibSeeAllCard ends a row that holds more than it shows.
func mlibSeeAllCard(kind mlibCardKind, n int, one, many string) mediaCard {
	return mediaCard{title: "See all", meta: pluralizeCount(n, one, many), mlibKind: kind, glyph: "grid"}
}

// mediaHubBuildLibraryRows builds the Library's rows from the model, keeping
// each row's focus and scroll across a rebuild.
func mediaHubBuildLibraryRows(prev []mediaRow) []mediaRow {
	m := mlibCurrentModel()
	if m == nil {
		return nil
	}
	rows := make([]mediaRow, 0, 5)
	if len(m.cont) > 0 {
		row := mediaRow{kind: mediaRowLibContinue, title: "Continue watching"}
		for _, i := range m.cont {
			if len(row.cards) >= mediaRowCards {
				break
			}
			row.cards = append(row.cards, mlibItemCard(m, i, false))
		}
		rows = append(rows, row)
	}
	if len(m.recent) > 0 {
		row := mediaRow{kind: mediaRowLibRecent, title: "Recently added"}
		for _, ref := range m.recent {
			if ref.album >= 0 {
				row.cards = append(row.cards, mlibGroupCard(m.albums[ref.album], mlibCardAlbum))
				continue
			}
			row.cards = append(row.cards, mlibItemCard(m, ref.item, false))
		}
		rows = append(rows, row)
	}
	if len(m.movies) > 0 {
		row := mediaRow{kind: mediaRowLibMovies, title: "Movies"}
		for _, i := range m.movies {
			if len(row.cards) >= mediaRowCards {
				row.cards = append(row.cards, mlibSeeAllCard(mlibCardAllMovies, len(m.movies), "movie", "movies"))
				break
			}
			row.cards = append(row.cards, mlibItemCard(m, i, false))
		}
		rows = append(rows, row)
	}
	if len(m.shows) > 0 {
		row := mediaRow{kind: mediaRowLibShows, title: "TV shows"}
		for _, g := range m.shows {
			if len(row.cards) >= mediaRowCards {
				row.cards = append(row.cards, mlibSeeAllCard(mlibCardAllShows, len(m.shows), "show", "shows"))
				break
			}
			row.cards = append(row.cards, mlibGroupCard(g, mlibCardShow))
		}
		rows = append(rows, row)
	}
	if len(m.albums) > 0 {
		row := mediaRow{kind: mediaRowLibMusic, title: "Music"}
		for _, g := range m.albums {
			if len(row.cards) >= mediaRowCards {
				row.cards = append(row.cards, mlibSeeAllCard(mlibCardAllAlbums, len(m.albums), "album", "albums"))
				break
			}
			row.cards = append(row.cards, mlibGroupCard(g, mlibCardAlbum))
		}
		rows = append(rows, row)
	}
	mediaCarryRowFocus(rows, prev)
	return rows
}

// mlibDrillCards builds the cards of an open list.
func mlibDrillCards(d mlibDrill) []mediaCard {
	m := mlibCurrentModel()
	if m == nil {
		return nil
	}
	var cards []mediaCard
	switch d.kind {
	case mlibCardAllMovies:
		cards = make([]mediaCard, 0, len(m.movies))
		for _, i := range m.movies {
			cards = append(cards, mlibItemCard(m, i, false))
		}
	case mlibCardAllShows:
		cards = make([]mediaCard, 0, len(m.shows))
		for _, g := range m.shows {
			cards = append(cards, mlibGroupCard(g, mlibCardShow))
		}
	case mlibCardAllAlbums:
		cards = make([]mediaCard, 0, len(m.albums))
		for _, g := range m.albums {
			cards = append(cards, mlibGroupCard(g, mlibCardAlbum))
		}
	case mlibCardShow:
		if gi, ok := m.showIdx[d.key]; ok {
			g := m.shows[gi]
			cards = make([]mediaCard, 0, len(g.items))
			for _, i := range g.items {
				cards = append(cards, mlibItemCard(m, i, true))
			}
		}
	case mlibCardAlbum:
		if gi, ok := m.albumIdx[d.key]; ok {
			g := m.albums[gi]
			cards = make([]mediaCard, 0, len(g.items))
			for _, i := range g.items {
				cards = append(cards, mlibItemCard(m, i, true))
			}
		}
	}
	return cards
}

// ── Opening, going back, playing ─────────────────────────────────────────────

// mlibOpenGroup opens the list a card stands for, one level deeper.
func mlibOpenGroup(config *Config, card mediaCard) {
	if !mlibCardOpens(card.mlibKind) {
		return
	}
	if n := len(mlibNav.stack); n > 0 {
		mlibNav.stack[n-1].index = mediaHub.index
		mlibNav.stack[n-1].scroll = mediaHub.scrollY
	} else {
		mlibNav.rowsScroll = mediaHub.scrollY
	}
	title := card.title
	switch card.mlibKind {
	case mlibCardAllMovies:
		title = "Movies"
	case mlibCardAllShows:
		title = "TV shows"
	case mlibCardAllAlbums:
		title = "Music"
	}
	mlibNav.stack = append(mlibNav.stack, mlibDrill{kind: card.mlibKind, key: card.mlibKey, title: title, sub: card.meta})
	mediaHub.index = 0
	mediaHub.scrollY = 0
	mediaHub.zone = mediaZoneContent
	mediaHub.sheet = mediaSheet{}
	mediaHub.builtAt = 0
	PlayActivateSound()
	if config != nil {
		mediaHubEnsureBuilt(config)
	}
}

// mlibDrillBack closes the innermost open list and puts the ring back where it
// was; false when no list is open.
func mlibDrillBack(config *Config) bool {
	n := len(mlibNav.stack)
	if n == 0 {
		return false
	}
	mlibNav.stack = mlibNav.stack[:n-1]
	if n > 1 {
		top := mlibNav.stack[n-2]
		mediaHub.index = top.index
		mediaHub.scrollY = top.scroll
	} else {
		mediaHub.index = 0
		mediaHub.scrollY = mlibNav.rowsScroll
	}
	mediaHub.zone = mediaZoneContent
	mediaHub.builtAt = 0
	PlayBackSound()
	if config != nil {
		mediaHubEnsureBuilt(config)
	}
	return true
}

// mlibAlbumFirst is the first song of an album, "" when it is gone.
func mlibAlbumFirst(key string) string {
	m := mlibCurrentModel()
	if m == nil {
		return ""
	}
	gi, ok := m.albumIdx[key]
	if !ok || len(m.albums[gi].items) == 0 {
		return ""
	}
	return m.items[m.albums[gi].items[0]].Path
}

// mlibPlayCard plays what a Library card stands for when the screen's own play
// path cannot: a song (its folder in the music player), an album (from its
// first song), a show (the episode in progress, else the next one), and the
// See all cards (which open their list). It reports whether the card was
// handled: a film or an episode is not, it plays like any file on the card.
func mlibPlayCard(config *Config, card mediaCard) bool {
	switch card.mlibKind {
	case mlibCardTrack:
		openAudioPlayer(config, card.path)
		return true
	case mlibCardAlbum:
		if path := mlibAlbumFirst(card.mlibKey); path != "" {
			openAudioPlayer(config, path)
		}
		return true
	case mlibCardShow:
		m := mlibCurrentModel()
		if m == nil {
			return true
		}
		gi, ok := m.showIdx[card.mlibKey]
		if !ok {
			return true
		}
		g := m.shows[gi]
		next := mlibNextEpisode(m, g)
		if next < 0 {
			return true
		}
		list := make([]mediaCard, 0, len(g.items))
		at := 0
		for j, i := range g.items {
			if i == next {
				at = j
			}
			list = append(list, mlibItemCard(m, i, true))
		}
		mediaHubPlay(config, list[at], list, at, false)
		return true
	case mlibCardAllMovies, mlibCardAllShows, mlibCardAllAlbums:
		mlibOpenGroup(config, card)
		return true
	}
	return false
}

// ── Drawing ──────────────────────────────────────────────────────────────────

// mediaHubDrawLibrary paints the Library tab: the open list under its header,
// the rows, or the scan's states.
func mediaHubDrawLibrary(renderer *sdl.Renderer, config *Config, rect sdl.Rect, online bool) {
	if n := len(mlibNav.stack); n > 0 {
		off := mlibDrawCrumb(renderer, config, rect, mlibNav.stack[n-1])
		rect.Y += off
		rect.H -= off
		mediaHubDrawGrid(renderer, config, rect, online)
		return
	}
	if m := mlibCurrentModel(); m == nil || len(m.items) == 0 {
		scanning, found, loaded := mlibStatus()
		if scanning || !loaded {
			mediaHubDrawState(renderer, config, rect, "", mlibScanLine(found), "", "", -1, true)
			return
		}
		mediaHubDrawState(renderer, config, rect, "@folder", "No movies, shows or music found",
			mlibEmptyBody(), "Scan again", 2, false)
		return
	}
	mediaHubDrawRows(renderer, config, rect, online)
}

// mlibDrawCrumb paints an open list's header — a Back chip naming the level it
// returns to, then the list's name and what it holds — and returns the height
// it took. The chip is the pointer's way back; the pad's is B.
func mlibDrawCrumb(renderer *sdl.Renderer, config *Config, rect sdl.Rect, d mlibDrill) int32 {
	font := getFontSized(config, "small", 16)
	if font == nil {
		return 0
	}
	h := int32(34)
	back := "Library"
	if n := len(mlibNav.stack); n > 1 {
		back = mlibNav.stack[n-2].title
	}
	back = ellipsize(font, back, 220)
	glyph := int32(16)
	bw, bh := measureText(font, back)
	chip := sdl.Rect{X: rect.X, Y: rect.Y, W: 12 + glyph + 8 + bw + 16, H: h}
	drawGlassSurface(renderer, chip, h/2, glassState{})
	DrawHomeIcon(renderer, "back", sdl.Rect{X: chip.X + 12, Y: chip.Y + (h-glyph)/2, W: glyph, H: glyph}, false)
	drawText(renderer, font, back, chip.X+12+glyph+8, chip.Y+(h-bh)/2, ColorTextSecondary(), textAlignLeft)
	mediaHub.hits = append(mediaHub.hits, mediaHit{kind: mediaHitStateAction, a: 3, rect: chip})
	x := chip.X + chip.W + 16
	avail := rect.X + rect.W - x
	title := ellipsize(font, d.title, avail)
	tw, th := measureText(font, title)
	drawText(renderer, font, title, x, chip.Y+(h-th)/2, ColorTextPrimary(), textAlignLeft)
	if small := getFontSized(config, "small", 14); small != nil && d.sub != "" {
		if left := avail - tw - 14; left > 60 {
			sub := ellipsize(small, d.sub, left)
			_, sh := measureText(small, sub)
			drawText(renderer, small, sub, x+tw+14, chip.Y+(h-sh)/2, ColorTextTertiary(), textAlignLeft)
		}
	}
	return h + 12
}
