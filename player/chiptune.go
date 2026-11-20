package main

// chiptune.go — game soundtracks in the music player.
//
// NSF (NES), SPC (SNES), GBS (Game Boy), VGM/VGZ/GYM (Sega), HES (PC Engine),
// KSS (MSX), AY (ZX Spectrum), SAP (Atari 8-bit) and SID (C64) files, plus the
// tracker modules (MOD, XM, S3M, IT) of the demo scene, play in the ordinary
// music player: same queue, same shuffle and repeat, same Now Playing, same
// screen-off mode and the same background playback under games. Only the
// decoder's input side differs (chiptune_decode.go): ffmpeg's libgme demuxer
// for the console formats, libopenmpt for modules and JukaHub's own SID
// player (sidplay_*.go) for the C64.
//
// One file, many songs. An NSF holds a game's whole soundtrack. The player
// addresses one song of such a file as "<file>#<n>" (n from 1): every list,
// the queue, favorites, the resume store, history and saved playlists already
// key on the path string, so a song is a path like any other. A real file
// named "x.nsf#3" has the extension ".nsf#3", which no list ever accepted, so
// the two cannot be confused. The few places that open or stat a playlist
// path ask chiptuneFileOf for the real file first. Song rows are named
// "<title>.nsf", so the player's usual "name without extension" shows the title.
//
// Opening one multi-song file (Files, history) plays that file's songs as an
// album, starting at the song its header names; a folder or a saved playlist
// lists every song of every file in it.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// chiptuneGMEExts are the formats ffmpeg plays through libgme.
var chiptuneGMEExts = map[string]bool{
	".nsf": true, ".nsfe": true, ".spc": true, ".gbs": true, ".vgm": true, ".vgz": true,
	".gym": true, ".hes": true, ".kss": true, ".ay": true, ".sap": true,
}

// chiptuneSIDExts are played by sidplay_*.go.
var chiptuneSIDExts = map[string]bool{".sid": true, ".psid": true}

// chiptuneTrackerExts are tracker modules, played through libopenmpt.
var chiptuneTrackerExts = map[string]bool{".mod": true, ".xm": true, ".s3m": true, ".it": true, ".mptm": true}

// chiptuneMultiExts are the formats with a song list (expanded into rows).
var chiptuneMultiExts = map[string]bool{
	".nsf": true, ".nsfe": true, ".gbs": true, ".ay": true, ".sap": true, ".sid": true, ".psid": true,
}

// chiptuneExpandLimit bounds the rows a folder of soundtracks expands into;
// files past it stay one row (their header's starting song).
const chiptuneExpandLimit = 3000

// chiptuneExpandBudget bounds the time one playlist spends reading headers on
// the caller's thread, which is the main thread when a file is opened from
// Files: an HVSC folder holds hundreds of SIDs. Files not reached in time stay
// one row (their starting song); the file that was opened is always expanded.
const chiptuneExpandBudget = 150 * time.Millisecond

// chiptuneSplitPath splits a song address "<file>#<n>" into the file and the
// song. ok is false for anything else, including URLs and files whose format
// has no song list.
func chiptuneSplitPath(p string) (file string, song int, ok bool) {
	i := strings.LastIndexByte(p, '#')
	if i <= 0 || i == len(p)-1 || len(p)-i > 5 || strings.Contains(p, "://") {
		return p, 0, false
	}
	n := 0
	for _, c := range p[i+1:] {
		if c < '0' || c > '9' {
			return p, 0, false
		}
		n = n*10 + int(c-'0')
	}
	file = p[:i]
	if n < 1 || !chiptuneMultiExts[strings.ToLower(filepath.Ext(file))] {
		return p, 0, false
	}
	return file, n, true
}

// chiptuneSongPath is the address of song n of file.
func chiptuneSongPath(file string, n int) string {
	return file + "#" + strconv.Itoa(n)
}

// chiptuneFileOf is the file on the card behind a playlist path.
func chiptuneFileOf(path string) string {
	if file, _, ok := chiptuneSplitPath(path); ok {
		return file
	}
	return path
}

// chiptuneExt is the lower-case extension of the file behind path.
func chiptuneExt(path string) string {
	return strings.ToLower(filepath.Ext(chiptuneFileOf(path)))
}

// chiptuneIsFile reports a soundtrack, a module or one song of a soundtrack:
// isAudioFile's addition.
func chiptuneIsFile(name string) bool {
	if strings.Contains(name, "://") {
		return false
	}
	ext := chiptuneExt(name)
	switch {
	case chiptuneGMEExts[ext], chiptuneSIDExts[ext]:
		return true
	case chiptuneTrackerExts[ext]:
		// A Go project's go.mod is not a ProTracker module.
		return !strings.EqualFold(filepath.Base(name), "go.mod")
	}
	return false
}

// chiptuneHandles reports a soundtrack whose tags and length come from its
// header (chiptune_meta.go) rather than from ffprobe: the libgme formats and SID.
func chiptuneHandles(path string) bool {
	if strings.Contains(path, "://") {
		return false
	}
	ext := chiptuneExt(path)
	return chiptuneGMEExts[ext] || chiptuneSIDExts[ext]
}

// chiptuneDecodes reports a file whose decoder input chiptune_decode.go
// builds: chiptuneHandles plus the tracker modules.
func chiptuneDecodes(path string) bool {
	return chiptuneHandles(path) || (chiptuneIsFile(path) && chiptuneTrackerExts[chiptuneExt(path)])
}

// chiptuneSongOf is the song a path plays: its "#n", else the file's starting
// song.
func chiptuneSongOf(path string, meta chiptuneMeta) int {
	if _, n, ok := chiptuneSplitPath(path); ok {
		return n
	}
	if meta.Start >= 1 {
		return meta.Start
	}
	return 1
}

// chiptuneSongName is a song row's name: its title, else "<game> · Song n",
// with the file's extension so the player's display trims it like a file name.
func chiptuneSongName(file string, meta chiptuneMeta, n int) string {
	title := meta.title(n)
	if title == "" {
		game := meta.Game
		if game == "" {
			game = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		}
		title = fmt.Sprintf("%s · Song %d", game, n)
	}
	title = strings.ReplaceAll(title, "\n", " ")
	return title + filepath.Ext(file)
}

// chiptuneSongTracks is one row per song of a multi-song file.
func chiptuneSongTracks(file string, meta chiptuneMeta) []musicTrack {
	out := make([]musicTrack, 0, meta.Songs)
	for n := 1; n <= meta.Songs; n++ {
		out = append(out, musicTrack{Path: chiptuneSongPath(file, n),
			Name: chiptuneSongName(file, meta, n), Duration: meta.seconds(n)})
	}
	return out
}

// chiptuneMultiFile reports a real (not "#n") file of a multi-song format.
func chiptuneMultiFile(path string) bool {
	if _, _, ok := chiptuneSplitPath(path); ok {
		return false
	}
	return chiptuneMultiExts[strings.ToLower(filepath.Ext(path))] && !strings.Contains(path, "://")
}

// chiptuneExpandTracks is the playlist hook (musicLoadPlaylist and
// musicPlayPlaylist): multi-song files become one row per song, and start
// (the row to play first) follows. startPath is what was opened: when it is
// one multi-song file (or a song of one), the list is that file alone.
//
// The headers are read here, on the caller's thread: an NSF/GBS/AY/SAP/SID
// header is a few hundred bytes, and chiptuneExpandBudget bounds the time a big
// folder may take. Single-song files (SPC, VGM…) keep their row; their titles
// and lengths are read on a goroutine (chiptuneFillTags).
func chiptuneExpandTracks(tracks []musicTrack, start int, startPath string) ([]musicTrack, int) {
	var singles []string
	multi := false
	for _, t := range tracks {
		switch {
		case chiptuneMultiFile(t.Path):
			multi = true
		case chiptuneHandles(t.Path):
			if _, _, song := chiptuneSplitPath(t.Path); !song {
				singles = append(singles, t.Path)
			}
		}
	}
	if len(singles) > 0 {
		go chiptuneFillTags(singles)
	}
	if startPath != "" {
		file, song, isSong := chiptuneSplitPath(startPath)
		if chiptuneMultiFile(file) {
			if meta, ok := chiptuneMetaFor(file); ok && meta.Songs > 1 {
				if !isSong {
					song = meta.Start
				}
				if song < 1 || song > meta.Songs {
					song = 1
				}
				return chiptuneSongTracks(file, meta), song - 1
			}
		}
	}
	if !multi {
		return tracks, start
	}
	out := make([]musicTrack, 0, len(tracks))
	newStart := 0
	deadline := time.Now().Add(chiptuneExpandBudget)
	for i, t := range tracks {
		if i == start {
			newStart = len(out)
		}
		if chiptuneMultiFile(t.Path) && len(out) < chiptuneExpandLimit &&
			(i == start || time.Now().Before(deadline)) {
			if meta, ok := chiptuneMetaFor(t.Path); ok && meta.Songs > 1 {
				if i == start && meta.Start >= 1 && meta.Start <= meta.Songs {
					newStart = len(out) + meta.Start - 1
				}
				out = append(out, chiptuneSongTracks(t.Path, meta)...)
				continue
			}
		}
		out = append(out, t)
	}
	return out, newStart
}

// chiptuneFillTags reads the tags of single-song soundtracks (an SPC folder)
// and gives their rows a title and a length. It runs behind the playlist's
// installation; a row it does not find any more is simply skipped.
func chiptuneFillTags(paths []string) {
	type fill struct {
		name string
		secs float64
	}
	found := make(map[string]fill, len(paths))
	for _, p := range paths {
		meta, ok := chiptuneMetaFor(p)
		if !ok {
			continue
		}
		f := fill{secs: meta.seconds(1)}
		if meta.title(1) != "" {
			f.name = chiptuneSongName(p, meta, 1)
		}
		found[dlPathKey(p)] = f
	}
	if len(found) == 0 {
		return
	}
	music.mu.Lock()
	defer music.mu.Unlock()
	for i := range music.tracks {
		f, ok := found[dlPathKey(music.tracks[i].Path)]
		if !ok {
			continue
		}
		music.tracks[i].Duration = f.secs
		if f.name != "" {
			music.tracks[i].Name = f.name
		}
		if i == music.current {
			music.duration = f.secs
		}
	}
}

// chiptuneProbeTrack is musicProbeDuration for a soundtrack: the header's
// length (or the default) instead of ffprobe, which would report one looping
// song. It also names the row after the song's title.
func chiptuneProbeTrack(path string) float64 {
	meta, ok := chiptuneMetaFor(path)
	secs := chiptuneDefaultSeconds
	song := 1
	if ok {
		song = chiptuneSongOf(path, meta)
		secs = meta.seconds(song)
	}
	name := ""
	if ok && meta.title(song) != "" {
		name = chiptuneSongName(chiptuneFileOf(path), meta, song)
	}
	music.mu.Lock()
	defer music.mu.Unlock()
	for i := range music.tracks {
		if !sameAudioPath(music.tracks[i].Path, path) {
			continue
		}
		music.tracks[i].Duration = secs
		if name != "" {
			music.tracks[i].Name = name
		}
		if i == music.current {
			music.duration = secs
		}
	}
	return secs
}

// chiptuneNowPlayingMeta is Now Playing's second line for a soundtrack:
// "SNES · Song 3 of 24 · Chrono Trigger · Yasunori Mitsuda". It never reads
// the disk (render thread); "" until the decoder has read the header.
func chiptuneNowPlayingMeta(path string) string {
	if !chiptuneHandles(path) {
		return ""
	}
	meta, ok := chiptuneMetaCached(path)
	if !ok {
		return ""
	}
	song := chiptuneSongOf(path, meta)
	parts := make([]string, 0, 4)
	if meta.System != "" {
		parts = append(parts, meta.System)
	}
	if meta.Songs > 1 {
		parts = append(parts, fmt.Sprintf("Song %d of %d", song, meta.Songs))
	}
	if meta.Game != "" && meta.Game != meta.title(song) {
		parts = append(parts, meta.Game)
	}
	if meta.Artist != "" {
		parts = append(parts, meta.Artist)
	}
	return strings.Join(parts, "   ·   ")
}

// chiptuneLibraryTags is the Media index's tags for a soundtrack (mlib.go's
// slow pass), read from the header instead of ffprobe: the game is the album
// and the console the album artist, so an SPC folder is one album however
// many composers it credits, and the Artists shelf lists consoles. A
// multi-song file is one entry titled after the game; playing it lists its songs.
func chiptuneLibraryTags(path string) (mlibTags, bool) {
	if !chiptuneHandles(path) {
		return mlibTags{}, false
	}
	meta, ok := chiptuneMetaFor(path)
	if !ok {
		return mlibTags{}, false
	}
	t := mlibTags{album: meta.Game, albumArtist: meta.System, artist: meta.Artist}
	if meta.Songs > 1 {
		t.title = meta.Game
		for n := 1; n <= meta.Songs; n++ {
			t.duration += meta.seconds(n)
		}
	} else {
		t.title = meta.title(1)
		t.duration = meta.seconds(1)
	}
	return t, true
}

// chiptuneSaid remembers the plain messages already shown this run, so a
// folder of NSFs on an ffmpeg without libgme says it once, not per song.
var chiptuneSaid sync.Map

// chiptuneSayOnce shows msg once per run (and logs it).
func chiptuneSayOnce(msg string) {
	if _, said := chiptuneSaid.LoadOrStore(msg, true); said {
		return
	}
	logWarn("chiptune", "%s", msg)
	showToast(msg, ToastWarn())
}
