package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// TestMusicHistoryRoundTripAndPrune: the listening log (musichistory.go) reads
// back what it wrote, keeps 400 days and no more than its entry bound.
func TestMusicHistoryRoundTripAndPrune(t *testing.T) {
	now := time.Now()
	if now.Before(clockFloor()) {
		t.Skip("the clock is not set: the age prune is off by design")
	}
	path := filepath.Join(t.TempDir(), musicHistoryFileName)
	old := musicListen{Path: filepath.Join("music", "old.mp3"), Start: now.AddDate(0, 0, -musicHistoryKeepDays-1).Unix(), Secs: 60}
	kept := musicListen{Path: filepath.Join("music", "Café", "song.flac"), Title: "Song", Artist: "Ünïcode",
		Start: now.Add(-time.Hour).Unix(), Secs: 245, Pos: 245.3, Dur: 245.3, Done: true}
	if err := musicHistoryWriteFile(path, []musicListen{old, kept}); err != nil {
		t.Fatal(err)
	}
	got, err := musicHistoryReadFile(path)
	if err != nil || len(got) != 2 || got[0] != old || got[1] != kept {
		t.Fatalf("read back %+v, %v", got, err)
	}
	if got = musicHistoryPrune(got, now); len(got) != 1 || got[0] != kept {
		t.Fatalf("the 400-day prune kept %+v", got)
	}
	many := make([]musicListen, musicHistoryMaxEntries+5)
	for i := range many {
		many[i] = musicListen{Path: "/m/" + strconv.Itoa(i) + ".mp3", Start: now.Unix(), Secs: 30}
	}
	if many = musicHistoryPrune(many, now); len(many) != musicHistoryMaxEntries || many[0].Path != "/m/5.mp3" {
		t.Fatalf("the entry bound kept %d, oldest %q", len(many), many[0].Path)
	}
}

// TestParseFFprobeChapters: ffprobe's chapter list of an .m4b (MED-9) comes back
// sorted and bounded, with bad entries dropped and an untitled one named; a
// file with one chapter has no list; Next and Previous step inside it.
func TestParseFFprobeChapters(t *testing.T) {
	out := []byte(`{"chapters": [
		{"id": 1, "time_base": "1/1000", "start": 600000, "start_time": "600.000000", "end": 1200000, "end_time": "1200.000000", "tags": {"title": "Two\tThe Storm"}},
		{"id": 0, "time_base": "1/1000", "start": 0, "start_time": "0.000000", "end": 600000, "end_time": "600.000000", "tags": {"title": "One"}},
		{"id": 2, "start_time": "1200.000000", "end_time": "1800.000000"},
		{"id": 3, "start_time": "NaN", "end_time": "1900.000000", "tags": {"title": "bad"}},
		{"id": 4, "start_time": "1900.000000", "end_time": "1850.000000", "tags": {"title": "backwards"}}
	]}`)
	got := parseFFprobeChapters(out)
	if len(got) != 3 || got[0].Title != "One" || got[1].Title != "Two The Storm" || got[2].Title != "Chapter 3" ||
		got[1].Start != 600 || got[2].End != 1800 {
		t.Fatalf("chapters = %+v", got)
	}
	if one := parseFFprobeChapters([]byte(`{"chapters":[{"start_time":"0","end_time":"60"}]}`)); one != nil {
		t.Errorf("one chapter gave a list: %+v", one)
	}
	if bad := parseFFprobeChapters([]byte(`{"chapters": [`)); bad != nil {
		t.Errorf("broken JSON gave %+v", bad)
	}
	long := parseFFprobeChapters([]byte(`{"chapters":[{"start_time":"0","end_time":"1","tags":{"TITLE":"` +
		strings.Repeat("x", 500) + `"}},{"start_time":"1","end_time":"2"}]}`))
	if len(long) != 2 || len([]rune(long[0].Title)) > audioChapterTitleRunes {
		t.Errorf("long title kept: %d runes", len([]rune(long[0].Title)))
	}
	for _, c := range []struct {
		pos    float64
		delta  int
		want   int
		inFile bool
	}{
		{10, 1, 1, true},    // into chapter two
		{1500, 1, 0, false}, // past the last: the playlist's own Next
		{700, -1, 1, true},  // well into chapter two: its start
		{601, -1, 0, true},  // its opening seconds: chapter one
		{1, -1, 0, false},   // chapter one's opening: the previous file
	} {
		if i, ok := audioChapterStep(got, c.pos, c.delta); ok != c.inFile || (ok && i != c.want) {
			t.Errorf("step %+d at %.0f s = %d, %v; want %d, %v", c.delta, c.pos, i, ok, c.want, c.inFile)
		}
	}
}
