package main

// store_durability_media_test.go — the stores that used to lose a user's data
// without saying anything.
//
// The music favorites, the audio resume positions, the starred TV channels, the
// IP stream library and the Power Tools snippet library all wrote through a temp
// file and a rename. The rename kept a crash from truncating the document, but
// nothing was kept to recover *from*, and every loader treated an unreadable
// file as an empty store: a card that dropped a write turned "my favorites"
// into "no favorites" with no message anywhere.
//
// They now save through AtomicWrite (temp + fsync + rename, previous version
// kept as .bak) and load through readJSONWithBackup, which prefers the live
// file, falls back to that .bak and logs the recovery. Each store below is saved
// twice through its own save function, so the backup is genuinely the previous
// version, and then the live file is truncated the way a power cut leaves it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mediaDurBackupHolds asserts the .bak beside path is the version before the
// last save: it must mention the earlier entry and must not already contain the
// newer one, or the test would pass on a backup that is just a second copy.
func mediaDurBackupHolds(t *testing.T, path, want, notWant string) {
	t.Helper()
	raw, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup beside %s: %v", filepath.Base(path), err)
	}
	if want != "" && !strings.Contains(string(raw), want) {
		t.Errorf("backup does not hold %q:\n%s", want, raw)
	}
	if notWant != "" && strings.Contains(string(raw), notWant) {
		t.Errorf("backup already holds %q, so it is not the previous version:\n%s", notWant, raw)
	}
}

// mediaDurNoTemp asserts the atomic write cleaned up after itself.
func mediaDurNoTemp(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("a temp file was left beside %s", filepath.Base(path))
	}
}

// mediaDurTruncate leaves the live file the way a power cut does: half a
// document, which no loader can parse.
func mediaDurTruncate(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"truncated":`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ── Music favorites ────────────────────────────────────────────────────────

func mediaDurMusicStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), musicFavoritesFileName)
	prevPath := musicFavoritesFilePath
	musicFavoritesFilePath = func() string { return path }
	musicFavMu.Lock()
	prevLoaded, prevPaths := musicFavData.loaded, musicFavData.paths
	musicFavData.loaded, musicFavData.paths = false, nil
	musicFavMu.Unlock()
	t.Cleanup(func() {
		musicFavoritesFilePath = prevPath
		musicFavMu.Lock()
		musicFavData.loaded, musicFavData.paths = prevLoaded, prevPaths
		musicFavMu.Unlock()
	})
	return path
}

func mediaDurForgetMusic() {
	musicFavMu.Lock()
	musicFavData.loaded, musicFavData.paths = false, nil
	musicFavMu.Unlock()
}

func TestMediaFavoritesStoreRecoversThePreviousList(t *testing.T) {
	path := mediaDurMusicStore(t)
	first := filepath.Join("music", "first.mp3")
	second := filepath.Join("music", "second.mp3")

	if !musicToggleFavorite(first) || !musicToggleFavorite(second) {
		t.Fatal("the toggles did not record both favorites")
	}
	mediaDurNoTemp(t, path)
	mediaDurBackupHolds(t, path, "first.mp3", "second.mp3")

	mediaDurTruncate(t, path)
	mediaDurForgetMusic()
	if !musicIsFavorite(first) {
		t.Fatal("the favorite was lost when the live file was truncated; the backup was not used")
	}
	if musicIsFavorite(second) {
		t.Error("the newer favorite came back, so the damaged live file was read instead of the backup")
	}
}

// ── Audio resume positions ─────────────────────────────────────────────────

func mediaDurAudioStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), audioResumeFileName)
	prevPath := audioResumeFilePath
	audioResumeFilePath = func() string { return path }
	audioPrefs.mu.Lock()
	prevLoaded, prevSpeed, prevEntries := audioPrefs.loaded, audioPrefs.speed, audioPrefs.entries
	audioPrefs.loaded, audioPrefs.speed, audioPrefs.entries = false, 0, nil
	audioPrefs.mu.Unlock()
	t.Cleanup(func() {
		audioResumeFilePath = prevPath
		audioPrefs.mu.Lock()
		audioPrefs.loaded, audioPrefs.speed, audioPrefs.entries = prevLoaded, prevSpeed, prevEntries
		audioPrefs.mu.Unlock()
	})
	return path
}

func mediaDurForgetAudio() {
	audioPrefs.mu.Lock()
	audioPrefs.loaded, audioPrefs.speed, audioPrefs.entries = false, 0, nil
	audioPrefs.mu.Unlock()
}

func TestMediaAudioResumeStoreRecoversSavedPositions(t *testing.T) {
	path := mediaDurAudioStore(t)
	audioRememberPosition("/books/first.mp3", 600, 3600)
	audioRememberPosition("/books/second.mp3", 900, 3600)

	mediaDurNoTemp(t, path)
	mediaDurBackupHolds(t, path, "first.mp3", "second.mp3")

	mediaDurTruncate(t, path)
	mediaDurForgetAudio()
	if got := audioResumePosition("/books/first.mp3"); got != 600 {
		t.Fatalf("position = %v after a truncated store, want 600 from the backup", got)
	}
	if got := audioResumePosition("/books/second.mp3"); got != 0 {
		t.Errorf("position = %v; the damaged live file was read instead of the backup", got)
	}
}

// ── Starred TV channels ────────────────────────────────────────────────────

func mediaDurTVStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), tvFavoritesFileName)
	prevPath := tvFavoritesFilePath
	tvFavoritesFilePath = func() string { return path }
	tvLib.mu.Lock()
	prevFavs, prevRead := tvLib.favs, tvLib.favsRead
	tvLib.favs, tvLib.favsRead = nil, false
	tvLib.mu.Unlock()
	t.Cleanup(func() {
		tvFavoritesFilePath = prevPath
		tvLib.mu.Lock()
		tvLib.favs, tvLib.favsRead = prevFavs, prevRead
		tvLib.mu.Unlock()
	})
	return path
}

func mediaDurSetTVFavs(favs map[string]TVFavorite) {
	tvLib.mu.Lock()
	tvLib.favs = favs
	tvLib.mu.Unlock()
}

func mediaDurForgetTVFavs() {
	tvLib.mu.Lock()
	tvLib.favs, tvLib.favsRead = nil, false
	tvLib.mu.Unlock()
}

func TestMediaTVFavoritesStoreRecoversStarredChannels(t *testing.T) {
	path := mediaDurTVStore(t)
	one := TVFavorite{URL: "http://example.com/one.m3u8", Name: "One", Group: tvOtherGroup}
	two := TVFavorite{URL: "http://example.com/two.m3u8", Name: "Two", Group: tvOtherGroup}

	mediaDurSetTVFavs(map[string]TVFavorite{one.URL: one})
	if err := saveTVFavorites(); err != nil {
		t.Fatalf("first save: %v", err)
	}
	mediaDurSetTVFavs(map[string]TVFavorite{one.URL: one, two.URL: two})
	if err := saveTVFavorites(); err != nil {
		t.Fatalf("second save: %v", err)
	}
	mediaDurNoTemp(t, path)
	mediaDurBackupHolds(t, path, "one.m3u8", "two.m3u8")

	mediaDurTruncate(t, path)
	mediaDurForgetTVFavs()
	tvLib.mu.Lock()
	tvLoadFavoritesLocked()
	_, hadOne := tvLib.favs[one.URL]
	_, hadTwo := tvLib.favs[two.URL]
	tvLib.mu.Unlock()
	if !hadOne {
		t.Fatal("the starred channel was lost when the live file was truncated; the backup was not used")
	}
	if hadTwo {
		t.Error("the newer star came back, so the damaged live file was read instead of the backup")
	}
}

// ── IP stream library ──────────────────────────────────────────────────────

func mediaDurIPStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ipStreamLibraryFileName)
	prevPath := ipStreamsFilePath
	ipStreamsFilePath = func() string { return path }
	ipStreamsMu.Lock()
	prevList, prevLoaded := ipStreams, ipStreamsLoaded
	ipStreams, ipStreamsLoaded = nil, false
	ipStreamsMu.Unlock()
	t.Cleanup(func() {
		ipStreamsFilePath = prevPath
		ipStreamsMu.Lock()
		ipStreams, ipStreamsLoaded = prevList, prevLoaded
		ipStreamsMu.Unlock()
	})
	return path
}

func mediaDurSetIPStreams(list []IPStream) {
	ipStreamsMu.Lock()
	ipStreams = list
	ipStreamsMu.Unlock()
}

func TestMediaIPStreamStoreRecoversTheLibrary(t *testing.T) {
	path := mediaDurIPStore(t)
	one := IPStream{Name: "One", URL: "http://example.com/one.m3u8"}
	two := IPStream{Name: "Two", URL: "http://example.com/two.m3u8"}

	mediaDurSetIPStreams([]IPStream{one})
	if err := saveIPStreams(); err != nil {
		t.Fatalf("first save: %v", err)
	}
	mediaDurSetIPStreams([]IPStream{one, two})
	if err := saveIPStreams(); err != nil {
		t.Fatalf("second save: %v", err)
	}
	mediaDurNoTemp(t, path)
	mediaDurBackupHolds(t, path, "one.m3u8", "two.m3u8")

	mediaDurTruncate(t, path)
	ipStreamsMu.Lock()
	ipStreams, ipStreamsLoaded = nil, false
	ipStreamsMu.Unlock()
	loadIPStreams()

	ipStreamsMu.Lock()
	defer ipStreamsMu.Unlock()
	if len(ipStreams) != 1 || ipStreams[0].URL != one.URL {
		t.Fatalf("library after recovery = %+v, want just %s", ipStreams, one.URL)
	}
}

// ── Power Tools snippets ───────────────────────────────────────────────────

func TestMediaPowerScriptsStoreRecoversTheSnippets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "power-scripts.json")
	disk := powerScript{Name: "Disk", Command: "df -h"}
	temp := powerScript{Name: "Temp", Command: "cat /sys/class/thermal/thermal_zone0/temp"}

	if err := savePowerScriptsTo(path, []powerScript{disk}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := savePowerScriptsTo(path, []powerScript{disk, temp}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	mediaDurNoTemp(t, path)
	mediaDurBackupHolds(t, path, `"Disk"`, `"Temp"`)

	mediaDurTruncate(t, path)
	got, err := loadPowerScriptsFrom(path)
	if err != nil {
		t.Fatalf("the backup was not used: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Disk" {
		t.Fatalf("snippets after recovery = %+v, want just the first version", got)
	}
}

// ── First run ──────────────────────────────────────────────────────────────

// TestMediaStoresStartEmptyWithoutAFile keeps the recovery path from making a
// fresh card depend on anything: no live file and no backup is an empty store,
// not an error and not a file the loader invented.
func TestMediaStoresStartEmptyWithoutAFile(t *testing.T) {
	musicPath := mediaDurMusicStore(t)
	if musicIsFavorite(filepath.Join("music", "none.mp3")) {
		t.Error("an empty music store reported a favorite")
	}

	audioPath := mediaDurAudioStore(t)
	if got := audioSavedSpeed(); got != 1 {
		t.Errorf("speed = %v with no store, want 1", got)
	}
	if got := audioResumePosition("/books/none.mp3"); got != 0 {
		t.Errorf("position = %v with no store, want 0", got)
	}

	tvPath := mediaDurTVStore(t)
	tvLib.mu.Lock()
	tvLoadFavoritesLocked()
	favs := len(tvLib.favs)
	tvLib.mu.Unlock()
	if favs != 0 {
		t.Errorf("%d starred channels with no store, want none", favs)
	}

	ipPath := mediaDurIPStore(t)
	loadIPStreams()
	ipStreamsMu.Lock()
	streams := len(ipStreams)
	ipStreamsMu.Unlock()
	if streams != 0 {
		t.Errorf("%d saved streams with no store, want none", streams)
	}

	absent := filepath.Join(t.TempDir(), "power-scripts.json")
	if scripts, err := loadPowerScriptsFrom(absent); err != nil || scripts != nil {
		t.Errorf("missing snippet store = %+v, %v; want an empty library and no error", scripts, err)
	}

	for _, path := range []string{musicPath, audioPath, tvPath, ipPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("loading created %s", filepath.Base(path))
		}
	}
}
