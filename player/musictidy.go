package main

// musictidy.go — Music settings → "Tidy the Music folder" (on unless off).
//
// Songs copied onto the card tend to land straight in Music: a few loose
// files here, an album folder dropped beside the artist folders there. After
// a library scan has read the tags (mlibRunScan, once mlibEnrich is done),
// the top level of the Music folder is put in order, on the scan's own
// goroutine:
//
//   - a song loose at the top level moves into Music/Singles/, with a lyrics
//     file or picture of the same name;
//   - an album folder put straight into Music — audio files and their extras
//     only, no folders inside, every song's tags naming the same artist (the
//     album artist where the file has one: mlibApplyTags) and the same album
//     — moves to Music/<Artist>/<Album>. It moves as a whole, so its covers,
//     .cue, .lrc and .m3u files travel with it and their relative paths stay
//     right.
//
// Nothing is ever overwritten: a name already taken is skipped and logged.
// Left alone: a folder with folders inside (an artist folder already, or a
// disc set), a folder named after its own artist, Singles itself, Audiobooks
// and everything outside Music, songs whose tags are not read yet (the next
// scan has them) or disagree, files the scan did not see or that changed size
// since (still being copied), any file of the music queue, and a PC's Music
// folder (the handheld's card only). The
// saved playlists and favourites that name a moved song follow it; .m3u
// files elsewhere are left as they are. The library index is updated in
// place, so nothing is probed twice and the shelf shows the new places at once.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

const (
	// musicTidyKey is the setting (on unless "false").
	musicTidyKey = "musicTidyFolder"
	// musicTidySingles is the folder loose songs move into.
	musicTidySingles = "Singles"
	// musicTidyMaxMoves bounds one pass, so a card with thousands of loose
	// songs is sorted over a few scans rather than in one long stall.
	musicTidyMaxMoves = 300
	// musicTidyNameMax keeps a folder name inside what FAT and exFAT take.
	musicTidyNameMax = 120
)

// The Music settings panel's rows of this file and ingame_music.go
// (musicsettings.go asks musicExtrasPanelValue / musicExtrasPanelAdjust).
const (
	msRowMusicTidy musicSettingsRowKind = 200 + iota
	msRowGameQuiet
)

var (
	musicTidyOn    atomic.Bool
	musicTidyBound atomic.Bool
	// musicTidyPinned are the path keys other settings name (musicTidyBind).
	musicTidyPinned atomic.Pointer[[]string]
)

// musicTidyEnabled reports the setting.
func musicTidyEnabled(config *Config) bool {
	if config == nil {
		return true
	}
	return customBool(config.Variables.Custom, musicTidyKey, true)
}

// musicTidySet stores the setting and saves it.
func musicTidySet(config *Config, on bool) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[musicTidyKey] = boolString(on)
	musicTidyBind(config)
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// musicTidyBind publishes the setting for the scan's goroutine, which never
// reads the config. Main thread (musicGameBind, once a second).
func musicTidyBind(config *Config) {
	if config == nil {
		return
	}
	// Files other settings name by path stay where they are: the background
	// music's song or folder (bgmusic.go) and the alarms' music ringtones
	// (alarmStore, main thread), which would otherwise fall back to a chime.
	var pinned []string
	if p := bgmSelectedPathIn(config); p != "" {
		pinned = append(pinned, dlPathKey(p))
	}
	for _, a := range alarmStore.Alarms {
		if p := alarmMusicPath(a.Sound); p != "" {
			pinned = append(pinned, dlPathKey(p))
		}
	}
	musicTidyPinned.Store(&pinned)
	musicTidyOn.Store(musicTidyEnabled(config))
	musicTidyBound.Store(true)
}

// musicTidyRoots are the Music folders the library looks in by default that
// exist: the card's Music, on the handheld only (like cardtidy.go). A PC's
// Music folder is left alone: other players index it by path, and it may be
// synced to the cloud.
func musicTidyRoots() []string {
	if !IsTrimuiDevice() {
		return nil
	}
	var out []string
	for _, r := range mlibDefaultRoots() {
		if !strings.EqualFold(filepath.Base(r), "Music") {
			continue
		}
		if st, err := os.Stat(r); err == nil && st.IsDir() {
			out = append(out, r)
		}
	}
	return out
}

// musicTidyAfterScan is the pass (mlibRunScan, after the tags are read). Any
// goroutine but the main one: it moves files.
func musicTidyAfterScan(ctx context.Context) {
	// Before the first bind the setting is unknown: wait for the next scan.
	if !musicTidyBound.Load() || !musicTidyOn.Load() || ctx.Err() != nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logWarn("musictidy", "tidying the Music folder failed: %v", r)
		}
	}()
	roots := musicTidyRoots()
	if len(roots) == 0 {
		return
	}
	mlibState.mu.Lock()
	items := append([]mlibItem(nil), mlibState.items...)
	mlibState.mu.Unlock()
	byDir := make(map[string][]mlibItem)
	for _, it := range items {
		if it.Video || it.Book {
			continue
		}
		k := dlPathKey(filepath.Dir(it.Path))
		byDir[k] = append(byDir[k], it)
	}
	busy := musicTidyBusyPaths()
	moves := make(map[string]string)
	for _, root := range roots {
		musicTidyRoot(ctx, root, byDir, busy, moves)
	}
	if len(moves) == 0 {
		return
	}
	logInfo("musictidy", "tidied the Music folder: %d file(s) moved", len(moves))
	musicTidyFollow(moves)
}

// musicTidyBusyPaths is the queue's files: a track that is playing, or will
// next, keeps its path — also while the player is stopped, since Play starts
// the same queue again by path. The pinned files and folders join them.
func musicTidyBusyPaths() map[string]bool {
	music.mu.Lock()
	out := make(map[string]bool, len(music.tracks))
	for _, t := range music.tracks {
		out[dlPathKey(t.Path)] = true
	}
	music.mu.Unlock()
	if p := musicTidyPinned.Load(); p != nil {
		for _, k := range *p {
			out[k] = true
		}
	}
	return out
}

// musicTidyRoot tidies one Music folder's top level.
func musicTidyRoot(ctx context.Context, root string, byDir map[string][]mlibItem, busy map[string]bool, moves map[string]string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	// Singles as the card already spells it: on FAT "singles" is the same
	// folder, and the index must name the files the way the next scan reads
	// them.
	singles := filepath.Join(root, musicTidySingles)
	for _, e := range entries {
		if e.IsDir() && strings.EqualFold(e.Name(), musicTidySingles) {
			singles = filepath.Join(root, e.Name())
			break
		}
	}
	loose := musicTidyKnown(byDir[dlPathKey(root)])
	for _, e := range entries {
		if ctx.Err() != nil || len(moves) >= musicTidyMaxMoves {
			return
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(root, name)
		if e.Type().IsRegular() {
			// Only a song the scan has seen whole: one still being copied
			// (by USB, the network or a phone) waits for the next scan.
			if !isAudioFile(name) || busy[dlPathKey(full)] || !musicTidySettled(e, loose[dlPathKey(full)]) {
				continue
			}
			if err := os.MkdirAll(singles, 0o755); err != nil {
				logWarn("musictidy", "could not make %s: %v", singles, err)
				return
			}
			if musicTidyMove(full, filepath.Join(singles, name), moves) {
				musicTidySidecars(root, name, singles, moves)
			}
			continue
		}
		if !e.IsDir() || strings.EqualFold(name, musicTidySingles) || strings.EqualFold(name, "Audiobooks") {
			continue
		}
		musicTidyAlbum(root, full, byDir[dlPathKey(full)], busy, moves)
	}
}

// musicTidySidecarExts are the files of a loose song's name that go with it.
var musicTidySidecarExts = []string{".lrc", ".jpg", ".jpeg", ".png", ".webp", ".cue"}

// musicTidySidecars moves song's same-named lyrics and pictures beside it.
func musicTidySidecars(dir, song, dest string, moves map[string]string) {
	stem := strings.TrimSuffix(song, filepath.Ext(song))
	for _, ext := range musicTidySidecarExts {
		for _, name := range []string{stem + ext, stem + strings.ToUpper(ext)} {
			src := filepath.Join(dir, name)
			if st, err := os.Lstat(src); err != nil || !st.Mode().IsRegular() {
				continue
			}
			musicTidyMove(src, filepath.Join(dest, name), moves)
			break
		}
	}
}

// musicTidyMove renames src to dst unless dst exists (logged and skipped).
// Same card, so a rename: nothing is copied, and a file that cannot be
// renamed stays where it was.
func musicTidyMove(src, dst string, moves map[string]string) bool {
	if _, err := os.Lstat(dst); err == nil {
		logInfo("musictidy", "left %s where it is: %s already exists", src, dst)
		return false
	}
	if err := os.Rename(src, dst); err != nil {
		logWarn("musictidy", "could not move %s to %s: %v", src, dst, err)
		return false
	}
	moves[src] = dst
	return true
}

// musicTidyKnown is one folder's library items by path key.
func musicTidyKnown(items []mlibItem) map[string]mlibItem {
	out := make(map[string]mlibItem, len(items))
	for _, it := range items {
		out[dlPathKey(it.Path)] = it
	}
	return out
}

// musicTidySettled reports whether e is the file the library found (it is the
// zero item when the index does not know the file) at the size the scan saw,
// so a copy still being written is not moved.
func musicTidySettled(e os.DirEntry, it mlibItem) bool {
	if it.Path == "" {
		return false
	}
	info, err := e.Info()
	return err == nil && info.Size() == it.Size
}

// musicTidyAlbum moves one folder straight under root to root/Artist/Album
// when it is an album by the rules at the top.
func musicTidyAlbum(root, dir string, known []mlibItem, busy map[string]bool, moves map[string]string) {
	if busy[dlPathKey(dir)] {
		return // the background music's folder
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var audio []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			return // albums inside: an artist folder already (or a disc set)
		}
		if e.Type().IsRegular() && isAudioFile(e.Name()) {
			audio = append(audio, e)
		}
	}
	if len(audio) == 0 {
		return
	}
	tags := musicTidyKnown(known)
	artist, album := "", ""
	for _, e := range audio {
		key := dlPathKey(filepath.Join(dir, e.Name()))
		if busy[key] {
			return
		}
		it := tags[key]
		if !musicTidySettled(e, it) || !it.Probed {
			return // tags not read yet, or still being copied: the next scan
		}
		a, al := strings.TrimSpace(it.Artist), strings.TrimSpace(it.Album)
		if a == "" || al == "" {
			return
		}
		if artist == "" {
			artist, album = a, al
			continue
		}
		if !strings.EqualFold(a, artist) || !strings.EqualFold(al, album) {
			return // a compilation or a mixed folder: left as it is
		}
	}
	artistDir, albumDir := musicTidyName(artist), musicTidyName(album)
	if artistDir == "" || albumDir == "" || strings.EqualFold(filepath.Base(dir), artistDir) {
		return // a folder named after its artist is that artist's already
	}
	if strings.EqualFold(artistDir, musicTidySingles) || strings.EqualFold(artistDir, "Audiobooks") {
		return // an artist tagged "Audiobooks" would turn the songs into books
	}
	// The artist's folder as the card already spells it (FAT ignores case).
	artistPath := filepath.Join(root, artistDir)
	if rootEntries, err := os.ReadDir(root); err == nil {
		for _, e := range rootEntries {
			if e.IsDir() && strings.EqualFold(e.Name(), artistDir) {
				artistPath = filepath.Join(root, e.Name())
				break
			}
		}
	}
	dest := filepath.Join(artistPath, albumDir)
	if _, err := os.Lstat(dest); err == nil {
		logInfo("musictidy", "left %s where it is: %s already exists", dir, dest)
		return
	}
	_, statErr := os.Lstat(artistPath)
	made := os.IsNotExist(statErr)
	if err := os.MkdirAll(artistPath, 0o755); err != nil {
		logWarn("musictidy", "could not make %s: %v", artistPath, err)
		return
	}
	if err := os.Rename(dir, dest); err != nil {
		logWarn("musictidy", "could not move %s to %s: %v", dir, dest, err)
		if made {
			_ = os.Remove(artistPath) // the folder made for it, still empty
		}
		return
	}
	logInfo("musictidy", "moved album folder %s to %s", dir, dest)
	for _, e := range entries {
		if e.Type().IsRegular() {
			moves[filepath.Join(dir, e.Name())] = filepath.Join(dest, e.Name())
		}
	}
}

// musicTidyName makes a tag usable as a folder name on any card: no path
// separators or characters FAT refuses, no leading or trailing dots or
// spaces, not too long. "" when nothing is left.
func musicTidyName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .")
	for len(out) > musicTidyNameMax {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	out = strings.Trim(out, " .")
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}

// musicTidyFollow points everything that named a moved file at its new place:
// the library index (in place: tags and pictures stay), the saved playlists,
// the favourites and the saved listening places.
func musicTidyFollow(moves map[string]string) {
	byKey := make(map[string]string, len(moves))
	for src, dst := range moves {
		byKey[dlPathKey(src)] = dst
	}
	mlibState.mu.Lock()
	changed := false
	for i := range mlibState.items {
		if dst, ok := byKey[dlPathKey(mlibState.items[i].Path)]; ok {
			mlibState.items[i].Path = dst
			changed = true
		}
	}
	mlibState.mu.Unlock()
	if changed {
		mlibRebuildModel()
		mlibSave()
	}

	musicPlaylistState.mu.Lock()
	musicPlaylistsLoadLocked()
	plChanged := false
	for i := range musicPlaylistState.items {
		// A fresh track list per changed playlist: a snapshot handed out
		// earlier may still be reading the old one.
		var tracks []musicPlaylistEntry
		for j, tr := range musicPlaylistState.items[i].Tracks {
			dst, ok := byKey[dlPathKey(tr.Path)]
			if !ok {
				continue
			}
			if tracks == nil {
				tracks = append([]musicPlaylistEntry(nil), musicPlaylistState.items[i].Tracks...)
			}
			tracks[j].Path = dst
		}
		if tracks != nil {
			musicPlaylistState.items[i].Tracks = tracks
			plChanged = true
		}
	}
	if plChanged {
		musicPlaylistsSaveLocked()
	}
	musicPlaylistState.mu.Unlock()

	musicFavMu.Lock()
	musicFavoritesLoadLocked()
	var renamed [][2]string
	for p := range musicFavData.paths {
		if dst, ok := byKey[dlPathKey(p)]; ok {
			renamed = append(renamed, [2]string{p, audioResumeKey(dst)})
		}
	}
	for _, r := range renamed {
		delete(musicFavData.paths, r[0])
		musicFavData.paths[r[1]] = true
	}
	if len(renamed) > 0 {
		_ = musicFavoritesSaveLocked()
	}
	musicFavMu.Unlock()

	// Saved places (audioresume.go): a long mix or a podcast kept in Music
	// resumes where it was after the move too.
	audioPrefs.mu.Lock()
	audioPrefsLoadLocked()
	var places [][2]string
	for p := range audioPrefs.entries {
		if dst, ok := byKey[dlPathKey(p)]; ok {
			places = append(places, [2]string{p, audioResumeKey(dst)})
		}
	}
	for _, r := range places {
		audioPrefs.entries[r[1]] = audioPrefs.entries[r[0]]
		delete(audioPrefs.entries, r[0])
	}
	if len(places) > 0 {
		_ = audioPrefsSaveLocked()
	}
	audioPrefs.mu.Unlock()
}

// ── Music settings rows ────────────────────────────────────────────────────

// musicExtrasPanelValue is the right-hand text of this file's and
// ingame_music.go's rows; ok false for any other row.
func musicExtrasPanelValue(kind musicSettingsRowKind, config *Config) (string, bool) {
	switch kind {
	case msRowMusicTidy:
		return onOff(musicTidyEnabled(config)), true
	case msRowGameQuiet:
		return onOff(ingameQuietEnabled(config)), true
	}
	return "", false
}

// musicExtrasPanelAdjust flips one of those rows; false for any other row.
func musicExtrasPanelAdjust(kind musicSettingsRowKind, config *Config) bool {
	switch kind {
	case msRowMusicTidy:
		on := !musicTidyEnabled(config)
		musicTidySet(config, on)
		showToast("Tidy the Music folder: "+onOff(on), ToastInfo())
	case msRowGameQuiet:
		on := !ingameQuietEnabled(config)
		ingameQuietSet(config, on)
		showToast("Quiet the game while music plays: "+onOff(on), ToastInfo())
	default:
		return false
	}
	PlayNavSound()
	return true
}
