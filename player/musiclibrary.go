package main

// musiclibrary.go — the playback rules and the small library store the audio
// player was missing: repeat modes, a shuffle order that does not repeat a
// track until the list is exhausted, a play-next queue, and favorites that
// survive a restart.
//
// Everything here is SDL-free and works on plain values, so the rules can be
// tested headless. The player owns the mutex (music.mu) and calls the *Locked
// helpers with it held.

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────────────────
// Repeat and shuffle
// ──────────────────────────────────────────────────────────────────────

// musicRepeatMode is how playback continues when a track ends.
type musicRepeatMode int

const (
	musicRepeatOff musicRepeatMode = iota
	musicRepeatAll
	musicRepeatOne
)

// label is the long form used in the status line.
func (m musicRepeatMode) label() string {
	switch m {
	case musicRepeatAll:
		return "Repeat all"
	case musicRepeatOne:
		return "Repeat one"
	default:
		return "Repeat off"
	}
}

// short is the compact form used on the Repeat button.
func (m musicRepeatMode) short() string {
	switch m {
	case musicRepeatAll:
		return "All"
	case musicRepeatOne:
		return "One"
	default:
		return "Off"
	}
}

// nextMusicRepeat cycles off → all → one → off.
func nextMusicRepeat(m musicRepeatMode) musicRepeatMode {
	switch m {
	case musicRepeatOff:
		return musicRepeatAll
	case musicRepeatAll:
		return musicRepeatOne
	default:
		return musicRepeatOff
	}
}

// buildShuffleOrder returns a permutation of 0..n-1 that starts with current,
// so turning shuffle on mid-album does not restart the song that is playing.
// current is clamped; n <= 1 returns the trivial order.
func buildShuffleOrder(n, current int, rng *rand.Rand) []int {
	if n <= 0 {
		return nil
	}
	if current < 0 || current >= n {
		current = 0
	}
	order := make([]int, 0, n)
	order = append(order, current)
	rest := make([]int, 0, n-1)
	for i := 0; i < n; i++ {
		if i != current {
			rest = append(rest, i)
		}
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	return append(order, rest...)
}

// musicStepLocked resolves the next index for the transport.
//
// delta is +1 for next and -1 for previous. natural marks an automatic advance
// at the end of a track: with repeat off that stops at the end of the list,
// while a manual Next still wraps (what the old player did). With shuffle on
// the walk follows the shuffled order instead of the folder order.
func (m *musicState) musicStepLocked(delta int, natural bool) (int, bool) {
	n := len(m.tracks)
	if n == 0 {
		return 0, false
	}
	if n == 1 {
		return m.current, true
	}
	if m.shuffle && len(m.order) == n {
		pos := m.orderPos + delta
		if pos < 0 {
			pos = len(m.order) - 1
		}
		if pos >= len(m.order) {
			if natural {
				if m.repeatMode != musicRepeatAll {
					return 0, false
				}
			}
			pos = 0
		}
		m.orderPos = pos
		return m.order[pos], true
	}
	idx := m.current + delta
	if idx < 0 {
		idx = n - 1
	}
	if idx >= n {
		if natural && m.repeatMode != musicRepeatAll {
			return 0, false
		}
		idx = 0
	}
	return idx, true
}

// musicQueuePathLocked appends a track to the play-next queue. front puts it at
// the head so several "play next" presses play in reverse press order, which is
// what a queue means.
func (m *musicState) musicQueuePathLocked(path string, front bool) bool {
	path = filepath.Clean(path)
	if path == "" {
		return false
	}
	for _, q := range m.queue {
		if sameAudioPath(q, path) {
			return false
		}
	}
	if front {
		m.queue = append([]string{path}, m.queue...)
	} else {
		m.queue = append(m.queue, path)
	}
	m.queueLen = len(m.queue)
	return true
}

// musicPopQueueLocked removes the head of the queue and returns its index in
// the current playlist. Entries that no longer exist in the playlist are
// dropped, so a stale queue cannot stall the transport.
func (m *musicState) musicPopQueueLocked() (int, bool) {
	for len(m.queue) > 0 {
		head := m.queue[0]
		m.queue = m.queue[1:]
		m.queueLen = len(m.queue)
		for i, t := range m.tracks {
			if sameAudioPath(t.Path, head) {
				return i, true
			}
		}
	}
	m.queueLen = 0
	return 0, false
}

// ──────────────────────────────────────────────────────────────────────
// Favorites
// ──────────────────────────────────────────────────────────────────────

const musicFavoritesFileName = "musicfavorites.json"

// musicFavoritesData is the on-disk shape. A flat path list keeps the file
// readable and diffable, and makes a hand-edited entry harmless.
type musicFavoritesData struct {
	Tracks []string `json:"tracks"`
}

var (
	musicFavMu   sync.Mutex
	musicFavData struct {
		loaded bool
		paths  map[string]bool
	}
)

// musicFavoritesFilePath is a variable so tests can point the store at a temp
// file instead of the app data folder.
var musicFavoritesFilePath = defaultMusicFavoritesFilePath

func defaultMusicFavoritesFilePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, musicFavoritesFileName)
}

func musicFavoritesLoadLocked() {
	if musicFavData.loaded {
		return
	}
	musicFavData.loaded = true
	musicFavData.paths = make(map[string]bool)

	data, err := os.ReadFile(musicFavoritesFilePath())
	if err != nil {
		return
	}
	var stored musicFavoritesData
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	for _, p := range stored.Tracks {
		if p == "" {
			continue
		}
		musicFavData.paths[audioResumeKey(p)] = true
	}
}

func musicFavoritesSaveLocked() error {
	paths := make([]string, 0, len(musicFavData.paths))
	for p := range musicFavData.paths {
		paths = append(paths, p)
	}
	// Stable output: the file is meant to be readable and diffable.
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && paths[j] < paths[j-1]; j-- {
			paths[j], paths[j-1] = paths[j-1], paths[j]
		}
	}
	data, err := json.MarshalIndent(musicFavoritesData{Tracks: paths}, "", "  ")
	if err != nil {
		return err
	}
	path := musicFavoritesFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// musicIsFavorite reports whether a track is marked.
func musicIsFavorite(path string) bool {
	if path == "" {
		return false
	}
	musicFavMu.Lock()
	defer musicFavMu.Unlock()
	musicFavoritesLoadLocked()
	return musicFavData.paths[audioResumeKey(path)]
}

// musicToggleFavorite flips a track's favorite flag and returns the new state.
// The write is best-effort: a read-only card leaves the marker in memory for
// this session instead of failing the keypress.
func musicToggleFavorite(path string) bool {
	if path == "" {
		return false
	}
	musicFavMu.Lock()
	defer musicFavMu.Unlock()
	musicFavoritesLoadLocked()
	key := audioResumeKey(path)
	if musicFavData.paths[key] {
		delete(musicFavData.paths, key)
	} else {
		musicFavData.paths[key] = true
	}
	_ = musicFavoritesSaveLocked()
	return musicFavData.paths[key]
}

// musicFavoriteCount is the number of saved favorites (for the player header).
func musicFavoriteCount() int {
	musicFavMu.Lock()
	defer musicFavMu.Unlock()
	musicFavoritesLoadLocked()
	return len(musicFavData.paths)
}
