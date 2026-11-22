package main

// musicsrc_local.go — the iPod screen's lists from this device: the media
// library index (mlib.go) grouped into artists, albums, songs, genres,
// composers, favourites and recent additions, the saved playlists, the
// audiobooks, and the films and shows of the video library.

import (
	"math/rand"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// musicLocalTrack is one song of the library as a row.
func musicLocalTrack(it mlibItem) musicItem {
	title := strings.TrimSpace(it.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(it.Path), filepath.Ext(it.Path))
	}
	return musicItem{Kind: "track", Src: musicSrcLocal, ID: it.Path, Title: title, Artist: it.Artist,
		Album: it.Album, Genre: it.Genre, Year: it.Year, Track: it.Track, Duration: it.Duration,
		Art: it.Art, Path: it.Path, Suffix: strings.TrimPrefix(strings.ToLower(filepath.Ext(it.Path)), "."),
		Size: it.Size}
}

func musicLocalAlbum(g mlibGroup, m *mlibModel) musicItem {
	it := musicItem{Kind: "album", Src: musicSrcLocal, ID: g.key, Title: g.title, Artist: g.sub,
		Count: len(g.items), Art: g.art}
	if len(g.items) > 0 {
		it.Year = m.items[g.items[0]].Year
		it.Genre = m.items[g.items[0]].Genre
	}
	return it
}

// musicLocalSong reports whether an item is a song (not a video, not a book
// chapter).
func musicLocalSong(it mlibItem) bool { return !it.Video && !it.Book }

// musicLocalFetch answers a query from the library. It only reads the
// published model, so any goroutine may call it.
func musicLocalFetch(q musicQuery) []musicItem {
	m := mlibCurrentModel()
	if m == nil {
		if q.What == "playlists" || q.What == "playlistTracks" {
			return musicLocalPlaylistFetch(nil, q)
		}
		return nil
	}
	switch q.What {
	case "artists":
		by := map[string]*musicItem{}
		var order []string
		for _, g := range m.albums {
			key := strings.ToLower(strings.TrimSpace(g.sub))
			row, ok := by[key]
			if !ok {
				row = &musicItem{Kind: "artist", Src: musicSrcLocal, ID: key, Title: g.sub, Art: g.art}
				by[key] = row
				order = append(order, key)
			}
			row.Count++
		}
		out := make([]musicItem, 0, len(order))
		for _, k := range order {
			out = append(out, *by[k])
		}
		sort.SliceStable(out, func(i, j int) bool { return musicNorm(out[i].Title) < musicNorm(out[j].Title) })
		return out
	case "artistAlbums":
		var out []musicItem
		for _, g := range m.albums {
			if strings.EqualFold(strings.TrimSpace(g.sub), strings.TrimSpace(q.Parent.Title)) {
				out = append(out, musicLocalAlbum(g, m))
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Year != out[j].Year {
				return out[i].Year < out[j].Year
			}
			return musicNorm(out[i].Title) < musicNorm(out[j].Title)
		})
		return out
	case "albums":
		out := make([]musicItem, 0, len(m.albums))
		for _, g := range m.albums {
			out = append(out, musicLocalAlbum(g, m))
		}
		sort.SliceStable(out, func(i, j int) bool { return musicNorm(out[i].Title) < musicNorm(out[j].Title) })
		return out
	case "albumTracks":
		gi, ok := m.albumIdx[q.Parent.ID]
		if !ok {
			// A folded album asks with its title and artist.
			gi, ok = m.albumIdx[mlibAlbumKey(q.Parent.Artist, q.Parent.Title)]
		}
		if !ok {
			return nil
		}
		g := m.albums[gi]
		out := make([]musicItem, 0, len(g.items))
		for _, i := range g.items {
			t := musicLocalTrack(m.items[i])
			if t.Art == "" {
				t.Art = g.art
			}
			out = append(out, t)
		}
		return out
	case "songs", "random":
		var out []musicItem
		for _, it := range m.items {
			if musicLocalSong(it) {
				out = append(out, musicLocalTrack(it))
			}
		}
		if q.What == "random" {
			rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
			if len(out) > 300 {
				out = out[:300]
			}
			return out
		}
		sort.SliceStable(out, func(i, j int) bool { return musicNorm(out[i].Title) < musicNorm(out[j].Title) })
		return out
	case "genres", "composers":
		counts := map[string]int{}
		names := map[string]string{}
		for _, it := range m.items {
			if !musicLocalSong(it) {
				continue
			}
			v := it.Genre
			if q.What == "composers" {
				v = it.Composer
			}
			for _, part := range musicSplitTag(v) {
				k := strings.ToLower(part)
				counts[k]++
				if _, ok := names[k]; !ok {
					names[k] = part
				}
			}
		}
		kind := "genre"
		if q.What == "composers" {
			kind = "composer"
		}
		out := make([]musicItem, 0, len(counts))
		for k, n := range counts {
			out = append(out, musicItem{Kind: kind, Src: musicSrcLocal, ID: names[k], Title: names[k], Count: n})
		}
		sort.SliceStable(out, func(i, j int) bool { return musicNorm(out[i].Title) < musicNorm(out[j].Title) })
		return out
	case "genreTracks", "composerTracks":
		var out []musicItem
		for _, it := range m.items {
			if !musicLocalSong(it) {
				continue
			}
			v := it.Genre
			if q.What == "composerTracks" {
				v = it.Composer
			}
			for _, part := range musicSplitTag(v) {
				if strings.EqualFold(part, q.Parent.Title) {
					out = append(out, musicLocalTrack(it))
					break
				}
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Artist != out[j].Artist {
				return musicNorm(out[i].Artist) < musicNorm(out[j].Artist)
			}
			if out[i].Album != out[j].Album {
				return musicNorm(out[i].Album) < musicNorm(out[j].Album)
			}
			return out[i].Track < out[j].Track
		})
		return out
	case "artistSongs":
		var out []musicItem
		for _, it := range m.items {
			if musicLocalSong(it) && strings.EqualFold(strings.TrimSpace(it.Artist), strings.TrimSpace(q.Parent.Title)) {
				out = append(out, musicLocalTrack(it))
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Album != out[j].Album {
				return musicNorm(out[i].Album) < musicNorm(out[j].Album)
			}
			return out[i].Track < out[j].Track
		})
		return out
	case "favorites":
		var out []musicItem
		for _, it := range m.items {
			if musicLocalSong(it) && musicIsFavorite(it.Path) {
				out = append(out, musicLocalTrack(it))
			}
		}
		return out
	case "recent":
		var out []musicItem
		for _, ref := range m.recent {
			if ref.album >= 0 && ref.album < len(m.albums) {
				out = append(out, musicLocalAlbum(m.albums[ref.album], m))
			}
		}
		return out
	case "search":
		return musicLocalSearch(m, q.Text)
	case "playlists", "playlistTracks":
		return musicLocalPlaylistFetch(m, q)
	case "books":
		out := make([]musicItem, 0, len(m.books))
		for _, g := range m.books {
			out = append(out, musicItem{Kind: "book", Src: musicSrcLocal, ID: g.key, Title: g.title,
				Artist: g.sub, Count: len(g.items), Art: g.art})
		}
		return out
	case "movies":
		out := make([]musicItem, 0, len(m.movies))
		for _, i := range m.movies {
			out = append(out, musicLocalVideo(m.items[i], "movie"))
		}
		return out
	case "shows":
		out := make([]musicItem, 0, len(m.shows))
		for _, g := range m.shows {
			out = append(out, musicItem{Kind: "series", Src: musicSrcLocal, ID: g.key, Title: g.title,
				Count: len(g.items), Art: g.art})
		}
		return out
	case "seasons":
		gi, ok := m.showIdx[q.Parent.ID]
		if !ok {
			return nil
		}
		g := m.shows[gi]
		count := map[int]int{}
		var order []int
		for _, i := range g.items {
			s := m.items[i].Season
			if _, seen := count[s]; !seen {
				order = append(order, s)
			}
			count[s]++
		}
		sort.Ints(order)
		out := make([]musicItem, 0, len(order))
		for _, s := range order {
			title := "Season " + strconv.Itoa(s)
			if s == 0 {
				title = "Specials"
			}
			out = append(out, musicItem{Kind: "season", Src: musicSrcLocal, ID: g.key + "\x00" + strconv.Itoa(s),
				Title: title, Count: count[s], Art: g.art, Track: s, Album: g.title})
		}
		return out
	case "episodes":
		key, season, _ := strings.Cut(q.Parent.ID, "\x00")
		gi, ok := m.showIdx[key]
		if !ok {
			return nil
		}
		want, _ := strconv.Atoi(season)
		var out []musicItem
		for _, i := range m.shows[gi].items {
			if m.items[i].Season == want {
				out = append(out, musicLocalVideo(m.items[i], "episode"))
			}
		}
		return out
	case "continue":
		out := make([]musicItem, 0, len(m.cont))
		for _, i := range m.cont {
			out = append(out, musicLocalVideo(m.items[i], "movie"))
		}
		return out
	}
	return nil
}

// musicLocalVideo is a film or an episode of the library as a row.
func musicLocalVideo(it mlibItem, kind string) musicItem {
	title := mlibItemTitle(it)
	if kind == "episode" && it.Episode > 0 {
		title = strconv.Itoa(it.Episode) + ". " + title
	}
	return musicItem{Kind: kind, Src: musicSrcLocal, ID: it.Path, Title: title, Year: it.Year,
		Duration: it.Duration, Art: dlFirstNonEmpty(it.Art, it.Poster), Path: it.Path,
		Position: it.Position, Video: true}
}

// musicSplitTag splits a multi-value tag ("Rock; Pop", "Rock / Pop").
func musicSplitTag(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	f := strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == '/' || r == '|' || r == '\x00' })
	out := f[:0]
	for _, p := range f {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// musicLocalSearch finds artists, albums and songs whose names contain text.
func musicLocalSearch(m *mlibModel, text string) []musicItem {
	q := strings.ToLower(strings.TrimSpace(text))
	if q == "" {
		return nil
	}
	var out []musicItem
	seenArtist := map[string]bool{}
	for _, g := range m.albums {
		if a := strings.ToLower(g.sub); strings.Contains(a, q) && !seenArtist[a] {
			seenArtist[a] = true
			out = append(out, musicItem{Kind: "artist", Src: musicSrcLocal, ID: a, Title: g.sub, Art: g.art})
		}
	}
	for _, g := range m.albums {
		if strings.Contains(strings.ToLower(g.title), q) {
			out = append(out, musicLocalAlbum(g, m))
		}
	}
	n := 0
	for _, it := range m.items {
		if !musicLocalSong(it) {
			continue
		}
		if strings.Contains(strings.ToLower(it.Title), q) {
			out = append(out, musicLocalTrack(it))
			if n++; n >= 80 {
				break
			}
		}
	}
	return out
}

// musicLocalPlaylistFetch lists the saved playlists (On-The-Go first) or one
// list's tracks.
func musicLocalPlaylistFetch(m *mlibModel, q musicQuery) []musicItem {
	if q.What == "playlists" {
		var out []musicItem
		for _, pl := range musicPlaylistsSnapshot() {
			row := musicItem{Kind: "playlist", Src: musicSrcLocal, ID: pl.Name, Title: pl.Name, Count: len(pl.Tracks)}
			if strings.EqualFold(pl.Name, musicOTGName) {
				out = append([]musicItem{row}, out...)
				continue
			}
			out = append(out, row)
		}
		return out
	}
	pl, ok := musicPlaylistNamed(q.Parent.ID)
	if !ok {
		return nil
	}
	out := make([]musicItem, 0, len(pl.Tracks))
	for _, tr := range pl.Tracks {
		row := musicItem{Kind: "track", Src: musicSrcLocal, ID: tr.Path, Title: musicTrackTitle(tr.Name),
			Duration: tr.Duration, Path: tr.Path, Suffix: strings.TrimPrefix(filepath.Ext(tr.Name), ".")}
		if meta, found := musicMetaFor(tr.Path); found {
			row.Title, row.Artist, row.Album, row.Art = dlFirstNonEmpty(meta.Title, row.Title), meta.Artist, meta.Album, meta.Art
			row.Src = meta.Src
		} else if m != nil {
			if i, found := m.byPath[dlPathKey(tr.Path)]; found {
				row = musicLocalTrack(m.items[i])
			}
		}
		out = append(out, row)
	}
	return out
}

// ── On-The-Go ───────────────────────────────────────────────────────────────

// musicOTGName is the iPod's quick list, kept as a saved playlist so the
// other layouts and the M3U8 export see it too.
const musicOTGName = "On-The-Go"

// musicOTGAdd appends tracks to On-The-Go and reports how many went in.
func musicOTGAdd(items []musicItem) int {
	pl, _ := musicPlaylistNamed(musicOTGName)
	pl.Name = musicOTGName
	have := map[string]bool{}
	for _, t := range pl.Tracks {
		have[t.Path] = true
	}
	added := 0
	for _, it := range items {
		if it.Kind != "track" || it.Path == "" || have[it.Path] {
			continue
		}
		musicMetaRemember(it)
		pl.Tracks = append(pl.Tracks, musicPlaylistEntry{Path: it.Path, Name: musicItemTrackName(it), Duration: it.Duration})
		have[it.Path] = true
		added++
	}
	if added == 0 {
		return 0
	}
	clean, ok := sanitizeMusicPlaylist(pl)
	if !ok {
		return 0
	}
	if err := musicPlaylistPut(clean); err != nil {
		showToast("On-The-Go: "+err.Error(), ToastWarn())
		return 0
	}
	return added
}
