package main

// musicplaylists.go — saved playlists for the audio player.
//
// The player already has favourites and a session queue (playbackqueue.go), and
// the folder it loaded is a playlist by accident. What it could not do is keep a
// named list — "Road Trip", "Relaxing" — across restarts. This store is that
// list: plain JSON next to the other app data, atomic writes, a bounded number
// of playlists and tracks, de-duplicated by name (saving the same name again
// replaces the list) and by path (a track appears once).
//
// It stores paths and titles, not the audio: a card whose files were moved will
// skip the missing ones when the playlist plays, exactly like the folder player.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	musicPlaylistFileName   = "music-playlists.json"
	musicPlaylistLimit      = 40
	musicPlaylistTrackLimit = 500
)

var (
	errMusicPlaylistNoName  = errors.New("name the playlist first")
	errMusicPlaylistEmpty   = errors.New("the queue is empty")
	errMusicPlaylistNoSpace = errors.New("no room for another playlist")
)

type musicPlaylistEntry struct {
	Path     string  `json:"path"`
	Name     string  `json:"name"`
	Duration float64 `json:"duration,omitempty"`
}

type musicPlaylist struct {
	Name    string               `json:"name"`
	Tracks  []musicPlaylistEntry `json:"tracks"`
	Updated time.Time            `json:"updated"`
}

type musicPlaylistsFile struct {
	Playlists []musicPlaylist `json:"playlists"`
}

var musicPlaylistState struct {
	mu     sync.Mutex
	loaded bool
	items  []musicPlaylist
}

// musicPlaylistFilePath is a variable so tests can point the store at a temp
// file instead of the app's own data folder.
var musicPlaylistFilePath = defaultMusicPlaylistFilePath

// musicPlaylistSelectionIndex is the row's cursor; it is UI state, not saved.
var musicPlaylistSelectionIndex int

func defaultMusicPlaylistFilePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, musicPlaylistFileName)
}

// sanitizeMusicPlaylist trims a name and drops entries that carry no path.
func sanitizeMusicPlaylist(in musicPlaylist) (musicPlaylist, bool) {
	out := musicPlaylist{Name: strings.TrimSpace(in.Name), Updated: in.Updated}
	if out.Name == "" {
		return out, false
	}
	seen := make(map[string]bool, len(in.Tracks))
	for _, tr := range in.Tracks {
		path := strings.TrimSpace(tr.Path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if tr.Name == "" {
			tr.Name = filepath.Base(path)
		}
		out.Tracks = append(out.Tracks, musicPlaylistEntry{Path: path, Name: tr.Name, Duration: tr.Duration})
		if len(out.Tracks) >= musicPlaylistTrackLimit {
			break
		}
	}
	if len(out.Tracks) == 0 {
		return out, false
	}
	return out, true
}

// sanitizeMusicPlaylists keeps the newest copy of a repeated name.
func sanitizeMusicPlaylists(in []musicPlaylist) []musicPlaylist {
	out := make([]musicPlaylist, 0, len(in))
	seen := map[string]int{}
	for _, pl := range in {
		clean, ok := sanitizeMusicPlaylist(pl)
		if !ok {
			continue
		}
		key := strings.ToLower(clean.Name)
		if idx, dup := seen[key]; dup {
			out[idx] = clean
			continue
		}
		seen[key] = len(out)
		out = append(out, clean)
		if len(out) >= musicPlaylistLimit {
			break
		}
	}
	return out
}

func musicPlaylistsLoadLocked() {
	if musicPlaylistState.loaded {
		return
	}
	musicPlaylistState.loaded = true
	path := musicPlaylistFilePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var file musicPlaylistsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		logWarn("music", "%s is unreadable (%v); starting with no saved playlists", path, err)
		return
	}
	musicPlaylistState.items = sanitizeMusicPlaylists(file.Playlists)
}

func musicPlaylistsSaveLocked() {
	path := musicPlaylistFilePath()
	data, err := json.MarshalIndent(musicPlaylistsFile{Playlists: musicPlaylistState.items}, "", "  ")
	if err != nil {
		logWarn("music", "cannot encode playlists: %v", err)
		return
	}
	if err := AtomicWrite(path, data, 0o644); err != nil {
		logWarn("music", "cannot save playlists to %s: %v", path, err)
	}
}

// musicPlaylistsSnapshot is the list as it was at the call: callers never hold
// the store's lock while they draw or measure.
func musicPlaylistsSnapshot() []musicPlaylist {
	musicPlaylistState.mu.Lock()
	defer musicPlaylistState.mu.Unlock()
	musicPlaylistsLoadLocked()
	out := make([]musicPlaylist, len(musicPlaylistState.items))
	copy(out, musicPlaylistState.items)
	return out
}

func musicPlaylistNamed(name string) (musicPlaylist, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return musicPlaylist{}, false
	}
	for _, pl := range musicPlaylistsSnapshot() {
		if strings.EqualFold(pl.Name, name) {
			return pl, true
		}
	}
	return musicPlaylist{}, false
}

// musicSaveQueueAsPlaylist turns the player's current list into a named
// playlist. Saving an existing name replaces that list, which is how a rename
// works without a second command.
func musicSaveQueueAsPlaylist(name string) (musicPlaylist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return musicPlaylist{}, errMusicPlaylistNoName
	}
	music.mu.Lock()
	queue := make([]musicTrack, len(music.tracks))
	copy(queue, music.tracks)
	music.mu.Unlock()
	if len(queue) == 0 {
		return musicPlaylist{}, errMusicPlaylistEmpty
	}

	pl := musicPlaylist{Name: name, Updated: time.Now()}
	for _, tr := range queue {
		path := strings.TrimSpace(tr.Path)
		if path == "" {
			continue
		}
		pl.Tracks = append(pl.Tracks, musicPlaylistEntry{Path: path, Name: tr.Name, Duration: tr.Duration})
		if len(pl.Tracks) >= musicPlaylistTrackLimit {
			break
		}
	}
	clean, ok := sanitizeMusicPlaylist(pl)
	if !ok {
		return musicPlaylist{}, errMusicPlaylistEmpty
	}

	musicPlaylistState.mu.Lock()
	defer musicPlaylistState.mu.Unlock()
	musicPlaylistsLoadLocked()
	replaced := false
	for i := range musicPlaylistState.items {
		if strings.EqualFold(musicPlaylistState.items[i].Name, clean.Name) {
			musicPlaylistState.items[i] = clean
			musicPlaylistSelectionIndex = i
			replaced = true
			break
		}
	}
	if !replaced {
		if len(musicPlaylistState.items) >= musicPlaylistLimit {
			return musicPlaylist{}, errMusicPlaylistNoSpace
		}
		musicPlaylistState.items = append(musicPlaylistState.items, clean)
		musicPlaylistSelectionIndex = len(musicPlaylistState.items) - 1
	}
	musicPlaylistsSaveLocked()
	return clean, nil
}

// musicSelectedPlaylist is the name the settings row is pointing at.
func musicSelectedPlaylist() (musicPlaylist, bool) {
	items := musicPlaylistsSnapshot()
	if len(items) == 0 {
		return musicPlaylist{}, false
	}
	idx := musicPlaylistSelectionIndex
	if idx < 0 {
		idx = 0
	}
	if idx >= len(items) {
		idx = len(items) - 1
	}
	musicPlaylistSelectionIndex = idx
	return items[idx], true
}

// musicCyclePlaylistSelection moves the row's cursor, wrapping at both ends.
func musicCyclePlaylistSelection(delta int) (musicPlaylist, bool) {
	items := musicPlaylistsSnapshot()
	if len(items) == 0 {
		musicPlaylistSelectionIndex = 0
		return musicPlaylist{}, false
	}
	idx := musicPlaylistSelectionIndex + delta
	if len(items) > 0 {
		idx %= len(items)
		if idx < 0 {
			idx += len(items)
		}
	}
	musicPlaylistSelectionIndex = idx
	return items[idx], true
}

// musicPlaySavedPlaylist loads the named list into the player and starts the
// first track. Missing files are skipped by the player itself, so a playlist
// whose card was reorganised still plays what is still there.
func musicPlaySavedPlaylist(config *Config, name string) bool {
	pl, ok := musicPlaylistNamed(name)
	if !ok || len(pl.Tracks) == 0 {
		return false
	}
	tracks := make([]musicTrack, 0, len(pl.Tracks))
	for _, tr := range pl.Tracks {
		tracks = append(tracks, musicTrack{Path: tr.Path, Name: tr.Name, Duration: tr.Duration})
	}
	if !musicPlayPlaylist(tracks, 0) {
		return false
	}
	openMusicPlayerScene(config)
	musicPlayTrack(0)
	return true
}
