package main

// musiclibrary_test.go pins the playback rules the player gained: repeat
// modes, a shuffle walk that visits every track once, a play-next queue, and
// favorites that survive a restart. All of it runs without SDL or ffmpeg.

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestRepeatModesCycle(t *testing.T) {
	if got := nextMusicRepeat(musicRepeatOff); got != musicRepeatAll {
		t.Errorf("off -> %v, want all", got)
	}
	if got := nextMusicRepeat(musicRepeatAll); got != musicRepeatOne {
		t.Errorf("all -> %v, want one", got)
	}
	if got := nextMusicRepeat(musicRepeatOne); got != musicRepeatOff {
		t.Errorf("one -> %v, want off", got)
	}
	for mode, want := range map[musicRepeatMode]string{
		musicRepeatOff: "Repeat off",
		musicRepeatAll: "Repeat all",
		musicRepeatOne: "Repeat one",
	} {
		if got := mode.label(); got != want {
			t.Errorf("label(%d) = %q, want %q", mode, got, want)
		}
	}
	if musicRepeatOne.short() != "One" {
		t.Errorf("short label for repeat one = %q", musicRepeatOne.short())
	}
}

func TestBuildShuffleOrderVisitsEveryTrackOnce(t *testing.T) {
	for _, n := range []int{1, 2, 5, 17} {
		for _, current := range []int{0, n / 2, n - 1} {
			order := buildShuffleOrder(n, current, rand.New(rand.NewSource(7)))
			if len(order) != n {
				t.Fatalf("n=%d: order has %d entries", n, len(order))
			}
			if order[0] != current {
				t.Errorf("n=%d current=%d: order starts at %d", n, current, order[0])
			}
			seen := make([]bool, n)
			for _, idx := range order {
				if idx < 0 || idx >= n {
					t.Fatalf("n=%d: order entry %d out of range: %v", n, idx, order)
				}
				if seen[idx] {
					t.Fatalf("n=%d: track %d appears twice: %v", n, idx, order)
				}
				seen[idx] = true
			}
		}
	}
	if buildShuffleOrder(0, 0, nil) != nil {
		t.Error("empty list should have no order")
	}
	// A current index outside the list is clamped instead of panicking.
	if order := buildShuffleOrder(3, 9, nil); len(order) != 3 || order[0] != 0 {
		t.Errorf("out-of-range current produced %v", order)
	}
}

func TestShuffleOrderIsDeterministicForASeed(t *testing.T) {
	a := buildShuffleOrder(12, 3, rand.New(rand.NewSource(42)))
	b := buildShuffleOrder(12, 3, rand.New(rand.NewSource(42)))
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same seed produced different orders: %v vs %v", a, b)
		}
	}
}

// musicTestState builds a state with n tracks for the rule tests.
func musicTestState(n int) *musicState {
	m := &musicState{current: 0}
	for i := 0; i < n; i++ {
		m.tracks = append(m.tracks, musicTrack{Path: filepath.Join("music", string(rune('a'+i))+".mp3")})
	}
	return m
}

func TestStepSequentialRules(t *testing.T) {
	m := musicTestState(3)
	m.current = 1

	if idx, ok := m.musicStepLocked(1, false); !ok || idx != 2 {
		t.Errorf("next = %d,%v want 2,true", idx, ok)
	}
	m.current = 2
	if idx, ok := m.musicStepLocked(1, false); !ok || idx != 0 {
		t.Errorf("manual next at the end must wrap, got %d,%v", idx, ok)
	}
	if idx, ok := m.musicStepLocked(1, true); ok {
		t.Errorf("natural next at the end with repeat off must stop, got %d", idx)
	}
	m.repeatMode = musicRepeatAll
	if idx, ok := m.musicStepLocked(1, true); !ok || idx != 0 {
		t.Errorf("natural next with repeat all must wrap, got %d,%v", idx, ok)
	}
	m.current = 0
	if idx, ok := m.musicStepLocked(-1, false); ok && idx != 2 {
		t.Errorf("prev from the first track = %d, want 2", idx)
	}
}

func TestStepShuffleWalksTheOrderThenStops(t *testing.T) {
	m := musicTestState(4)
	m.shuffle = true
	m.order = buildShuffleOrder(4, 0, rand.New(rand.NewSource(3)))
	m.orderPos = 0
	m.current = m.order[0]

	visited := map[int]bool{m.current: true}
	for i := 0; i < 3; i++ {
		idx, ok := m.musicStepLocked(1, true)
		if !ok {
			t.Fatalf("shuffle stopped early at step %d", i)
		}
		visited[idx] = true
		m.current = idx
	}
	if len(visited) != 4 {
		t.Fatalf("shuffle visited %d of 4 tracks: %v", len(visited), visited)
	}
	if _, ok := m.musicStepLocked(1, true); ok {
		t.Error("shuffle must stop after the last track when repeat is off")
	}
	m.repeatMode = musicRepeatAll
	if _, ok := m.musicStepLocked(1, true); !ok {
		t.Error("shuffle must wrap when repeat all is on")
	}
	m.repeatMode = musicRepeatOff
	m.orderPos = 2
	if _, ok := m.musicStepLocked(-1, false); !ok {
		t.Error("shuffle prev must step back through the order")
	}
}

func TestQueuePlaysNextAndDropsStaleEntries(t *testing.T) {
	m := musicTestState(3)
	first := m.tracks[2].Path
	second := m.tracks[1].Path

	if !m.musicQueuePathLocked(first, true) {
		t.Fatal("queueing a track failed")
	}
	if m.musicQueuePathLocked(first, true) {
		t.Error("queueing the same path twice was accepted")
	}
	if !m.musicQueuePathLocked(second, false) {
		t.Fatal("queueing behind failed")
	}
	if idx, ok := m.musicPopQueueLocked(); !ok || idx != 2 {
		t.Fatalf("first queue pop = %d,%v want 2,true", idx, ok)
	}
	if idx, ok := m.musicPopQueueLocked(); !ok || idx != 1 {
		t.Fatalf("second queue pop = %d,%v want 1,true", idx, ok)
	}
	if _, ok := m.musicPopQueueLocked(); ok {
		t.Error("empty queue still returned a track")
	}
	if m.queueLen != 0 {
		t.Errorf("cached queue length = %d, want 0", m.queueLen)
	}

	// A queued file that is no longer in the folder must not stall the queue.
	m.musicQueuePathLocked(filepath.Join("gone", "missing.mp3"), true)
	if _, ok := m.musicPopQueueLocked(); ok {
		t.Error("a stale queue entry was played")
	}
	if len(m.queue) != 0 {
		t.Errorf("stale entries left in the queue: %v", m.queue)
	}
}

// TestMusicNextPrefersTheQueue drives the real transport entry point with the
// decode hook stubbed, so the queue-before-folder rule is pinned end to end.
func TestMusicNextPrefersTheQueue(t *testing.T) {
	prevHook := musicPlayTrackHook
	var played []int
	musicPlayTrackHook = func(idx int) { played = append(played, idx) }
	t.Cleanup(func() { musicPlayTrackHook = prevHook })

	music.mu.Lock()
	prevTracks, prevCurrent := music.tracks, music.current
	prevQueue, prevShuffle, prevRepeat := music.queue, music.shuffle, music.repeatMode
	music.mu.Unlock()
	t.Cleanup(func() {
		music.mu.Lock()
		music.tracks, music.current = prevTracks, prevCurrent
		music.queue, music.shuffle, music.repeatMode = prevQueue, prevShuffle, prevRepeat
		music.queueLen = len(prevQueue)
		music.mu.Unlock()
	})

	music.mu.Lock()
	music.tracks = nil
	music.current = 0
	for _, name := range []string{"a.mp3", "b.mp3", "c.mp3"} {
		music.tracks = append(music.tracks, musicTrack{Path: name})
	}
	music.queue = nil
	music.shuffle = false
	music.repeatMode = musicRepeatOff
	music.musicQueuePathLocked("c.mp3", true)
	music.mu.Unlock()

	musicNext()
	if len(played) != 1 || played[0] != 2 {
		t.Fatalf("next after queueing c played %v, want [2]", played)
	}

	played = nil
	musicNext()
	if len(played) != 1 || played[0] != 1 {
		t.Fatalf("next with an empty queue played %v, want [1]", played)
	}
}

// ── Favorites ──────────────────────────────────────────────────────────

func withTempFavorites(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), musicFavoritesFileName)
	prevPath := musicFavoritesFilePath
	musicFavoritesFilePath = func() string { return path }
	t.Cleanup(func() {
		musicFavoritesFilePath = prevPath
		musicFavMu.Lock()
		musicFavData.loaded = false
		musicFavData.paths = nil
		musicFavMu.Unlock()
	})
	return path
}

func TestFavoritesPersistAndToggle(t *testing.T) {
	path := withTempFavorites(t)
	track := filepath.Join("music", "song.mp3")

	if musicIsFavorite(track) {
		t.Fatal("track starts as a favorite")
	}
	if !musicToggleFavorite(track) {
		t.Fatal("first toggle did not favorite the track")
	}
	if !musicIsFavorite(track) || musicFavoriteCount() != 1 {
		t.Fatal("favorite was not recorded")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("favorites file was not written: %v", err)
	}

	// Reload from disk the way a restart does.
	musicFavMu.Lock()
	musicFavData.loaded = false
	musicFavData.paths = nil
	musicFavMu.Unlock()
	if !musicIsFavorite(track) {
		t.Fatal("favorite did not survive a reload")
	}

	if musicToggleFavorite(track) {
		t.Fatal("second toggle did not remove the favorite")
	}
	if musicIsFavorite(track) || musicFavoriteCount() != 0 {
		t.Fatal("favorite removal was not recorded")
	}
}

func TestFavoritesTolerateCorruptStore(t *testing.T) {
	path := withTempFavorites(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if musicIsFavorite("music/a.mp3") {
		t.Fatal("a corrupt store invented a favorite")
	}
	if !musicToggleFavorite("music/a.mp3") {
		t.Fatal("toggling a favorite over a corrupt store failed")
	}
}

func TestFavoriteKeyNormalisesPaths(t *testing.T) {
	withTempFavorites(t)
	abs, err := filepath.Abs(filepath.Join("music", "song.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	musicToggleFavorite(abs)
	if !musicIsFavorite(filepath.Join(".", "music", "song.mp3")) {
		t.Fatal("the same file opened through a relative path is not seen as a favorite")
	}
}
