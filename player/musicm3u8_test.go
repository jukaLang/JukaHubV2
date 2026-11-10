package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetMusicPlaylistStore points the JSON store at a temp file for one test.
func resetMusicPlaylistStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := musicPlaylistFilePath
	musicPlaylistFilePath = func() string { return filepath.Join(dir, "music-playlists.json") }
	musicPlaylistState.mu.Lock()
	musicPlaylistState.loaded, musicPlaylistState.items = false, nil
	musicPlaylistState.mu.Unlock()
	t.Cleanup(func() {
		musicPlaylistFilePath = old
		musicPlaylistState.mu.Lock()
		musicPlaylistState.loaded, musicPlaylistState.items = false, nil
		musicPlaylistState.mu.Unlock()
	})
	return dir
}

func TestMusicPlaylistM3U8RoundTrip(t *testing.T) {
	dir := resetMusicPlaylistStore(t)
	musicDir := filepath.Join(dir, "Music")
	if err := os.MkdirAll(musicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(musicDir, "first song.mp3")
	second := filepath.Join(musicDir, "second.flac")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pl := musicPlaylist{Name: "Road Trip", Tracks: []musicPlaylistEntry{
		{Path: first, Name: "First", Duration: 61.4},
		{Path: second, Name: "Second", Duration: 90},
	}}
	musicPlaylistState.mu.Lock()
	musicPlaylistState.loaded = true
	musicPlaylistState.items = []musicPlaylist{pl}
	musicPlaylistState.mu.Unlock()

	out, err := musicPlaylistExport("Road Trip", dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "#EXTM3U\n#PLAYLIST:Road Trip\n") {
		t.Fatalf("header = %q", text)
	}
	if !strings.Contains(text, "#EXTINF:61,First\nMusic/first song.mp3\n") {
		t.Errorf("relative first entry missing: %q", text)
	}
	if !strings.Contains(text, "#EXTINF:90,Second\nMusic/second.flac\n") {
		t.Errorf("relative second entry missing: %q", text)
	}

	entries, missing := musicParseM3U8(data, dir)
	if missing != 0 || len(entries) != 2 {
		t.Fatalf("parsed %d entries, %d missing", len(entries), missing)
	}
	if entries[0].Name != "First" || entries[0].Duration != 61 {
		t.Errorf("first entry = %+v", entries[0])
	}
	if entries[1].Path != second {
		t.Errorf("relative path resolved to %q, want %q", entries[1].Path, second)
	}
}

func TestMusicParseM3U8SkipsMissingAndKeepsURLs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "here.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := "#EXTM3U\n" +
		"# a comment\n" +
		"#EXTINF:12,Here\nhere.mp3\n" +
		"#EXTINF:34,Gone\ngone.mp3\n" +
		"#EXTINF:-1,Radio\nhttps://example.test/live.mp3?x=1\n"
	entries, missing := musicParseM3U8([]byte(data), dir)
	if missing != 1 {
		t.Fatalf("missing = %d, want 1", missing)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Path != filepath.Join(dir, "here.mp3") {
		t.Errorf("relative entry = %q", entries[0].Path)
	}
	if entries[1].Path != "https://example.test/live.mp3?x=1" || entries[1].Name != "Radio" {
		t.Errorf("URL entry = %+v", entries[1])
	}
}

func TestMusicPlaylistImportStoresAndPlaysFromFile(t *testing.T) {
	dir := resetMusicPlaylistStore(t)
	musicDir := filepath.Join(dir, "Music")
	if err := os.MkdirAll(musicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	track := filepath.Join(musicDir, "one.mp3")
	if err := os.WriteFile(track, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "From PC.m3u8")
	body := "#EXTM3U\n#EXTINF:5,One\nMusic/one.mp3\n"
	if err := os.WriteFile(list, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pl, missing, err := musicPlaylistImport(list, "")
	if err != nil {
		t.Fatal(err)
	}
	if missing != 0 || pl.Name != "From PC" || len(pl.Tracks) != 1 {
		t.Fatalf("import = %+v, missing %d", pl, missing)
	}
	if saved, ok := musicPlaylistNamed("From PC"); !ok || len(saved.Tracks) != 1 || saved.Tracks[0].Path != track {
		t.Fatalf("stored playlist = %+v, ok %v", saved, ok)
	}
}

func TestIsMusicPlaylistFile(t *testing.T) {
	for name, want := range map[string]bool{
		"a.m3u8": true, "A.M3U": true, "a.mp3": false, "a.m3u8.txt": false,
	} {
		if got := isMusicPlaylistFile(name); got != want {
			t.Errorf("isMusicPlaylistFile(%q) = %v", name, got)
		}
	}
}
