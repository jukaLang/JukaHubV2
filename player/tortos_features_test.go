package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAudiobookGroupingAndCommonResumeFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Music")
	bookOne := mlibNewItem(mlibFile{path: filepath.Join(root, "Audiobooks", "Author", "Book", "CD1", "01.mp3"), root: root})
	bookTwo := mlibNewItem(mlibFile{path: filepath.Join(root, "Audiobooks", "Author", "Book", "CD2", "02.mp3"), root: root})
	bookThree := mlibNewItem(mlibFile{path: filepath.Join(root, "Audiobooks", "Author", "Book", "CD1", "10.mp3"), root: root})
	bookFour := mlibNewItem(mlibFile{path: filepath.Join(root, "Audiobooks", "Author", "Book", "CD1", "02.mp3"), root: root})
	song := mlibNewItem(mlibFile{path: filepath.Join(root, "Artist", "Album", "01.mp3"), root: root})
	if !bookOne.Book || !bookTwo.Book || song.Book {
		t.Fatalf("book classification: %v, %v, %v", bookOne.Book, bookTwo.Book, song.Book)
	}
	if bookOne.Album != "Book" || bookOne.Artist != "Author" {
		t.Fatalf("book folders became %q / %q", bookOne.Artist, bookOne.Album)
	}
	m := mlibBuildModel([]mlibItem{bookOne, bookTwo, bookThree, bookFour, song}, nil)
	if len(m.books) != 1 || len(m.albums) != 1 || len(m.books[0].items) != 4 {
		t.Fatalf("unexpected shelves: %d books, %d albums", len(m.books), len(m.albums))
	}
	if got := m.items[m.books[0].items[1]].Path; got != bookFour.Path {
		t.Fatalf("chapter 2 sorted as %q", got)
	}
	if key := musicShelfBookKey(m, m.books[0]); key != filepath.Join(root, "Audiobooks", "Author", "Book") {
		t.Fatalf("resume folder = %q", key)
	}
}

func TestMediaIndexUpgradeKeepsVideoPosition(t *testing.T) {
	old := mlibIndexFile{Version: 1, Items: []mlibItem{{Path: "movie.mp4"}},
		Dirs:   map[string]mlibDirState{"Videos": {}},
		Resume: map[string]mlibResume{"movie.mp4": {Position: 93, Played: 10}}}
	next := mlibUpgradeIndex(old)
	if len(next.Items) != 0 || len(next.Dirs) != 0 || next.Resume["movie.mp4"].Position != 93 {
		t.Fatalf("index upgrade lost resume or kept stale entries: %+v", next)
	}
}

func TestAudiobookFinishedStatusPersists(t *testing.T) {
	oldPath := audioResumeFilePath
	path := filepath.Join(t.TempDir(), "audioresume.json")
	audioResumeFilePath = func() string { return path }
	audioPrefs.mu.Lock()
	oldLoaded, oldSpeed, oldEntries, oldBooks := audioPrefs.loaded, audioPrefs.speed, audioPrefs.entries, audioPrefs.books
	audioPrefs.loaded = false
	audioPrefs.mu.Unlock()
	t.Cleanup(func() {
		audioResumeFilePath = oldPath
		audioPrefs.mu.Lock()
		audioPrefs.loaded, audioPrefs.speed, audioPrefs.entries, audioPrefs.books = oldLoaded, oldSpeed, oldEntries, oldBooks
		audioPrefs.mu.Unlock()
	})
	book := filepath.Join(t.TempDir(), "Audiobooks", "Book")
	chapter := filepath.Join(book, "02.mp3")
	audioBookRemember(book, chapter, false)
	audioPrefs.mu.Lock()
	audioPrefs.loaded = false
	audioPrefs.mu.Unlock()
	if got := audioBookProgress(book); got.Path != chapter || got.Finished {
		t.Fatalf("chapter progress = %+v", got)
	}
	audioBookRemember(book, chapter, true)
	audioPrefs.mu.Lock()
	audioPrefs.loaded = false
	audioPrefs.mu.Unlock()
	if got := audioBookProgress(book); !got.Finished {
		t.Fatalf("finished progress = %+v", got)
	}
}

func TestGameHistoryRecoversInterruptedLaunch(t *testing.T) {
	oldPath := gameHistoryPath
	path := filepath.Join(t.TempDir(), "game-history.json")
	gameHistoryPath = func() string { return path }
	gameHistoryStore.mu.Lock()
	gameHistoryStore.loaded = false
	gameHistoryStore.data = gameHistoryFile{}
	gameHistoryStore.mu.Unlock()
	t.Cleanup(func() {
		gameHistoryPath = oldPath
		gameHistoryStore.mu.Lock()
		gameHistoryStore.loaded = false
		gameHistoryStore.data = gameHistoryFile{}
		gameHistoryStore.mu.Unlock()
	})
	game := libraryGame{Path: "game.rom", System: "GBA", Title: "Example"}
	first := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	gameHistoryLaunch(game, first)
	// A fresh load is what a restart does. The pending launch is retained,
	// without crediting the unknown time after the power loss.
	gameHistoryStore.mu.Lock()
	gameHistoryStore.loaded = false
	gameHistoryStore.mu.Unlock()
	recovered := gameHistoryFor(game.System, game.Title)
	if recovered.Launches != 1 || len(recovered.Sessions) != 1 || !recovered.Sessions[0].Interrupted || recovered.Sessions[0].Seconds != 0 {
		t.Fatalf("interrupted launch was not recovered: %+v", recovered)
	}
	second := time.Now().Add(-time.Minute).Truncate(time.Second)
	gameHistoryLaunch(game, second)
	gameHistoryFinishGap([]recentGame{{System: game.System, Title: game.Title, Path: game.Path, PlayedAt: second}}, second.Add(-time.Second), second.Add(59*time.Second))
	st := gameHistorySummarize(gameHistoryFor(game.System, game.Title), time.Now())
	if st.Launches != 2 || st.Sessions != 1 || st.Longest != 60 || st.Last7 != 60 || st.Last30 != 60 || st.Interrupted != 1 {
		t.Fatalf("session summary = %+v", st)
	}
}

func TestMusicBrainzCoverLookupAndScaling(t *testing.T) {
	const id = "12345678-1234-1234-1234-123456789abc"
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var picture bytes.Buffer
	if err := jpeg.Encode(&picture, img, nil); err != nil {
		t.Fatal(err)
	}
	var coverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("User-Agent"), "JukaHub") {
			t.Errorf("missing user agent")
		}
		switch r.URL.Path {
		case "/mb":
			q := r.URL.Query().Get("query")
			if !strings.Contains(q, "Album") || !strings.Contains(q, "Artist") {
				t.Errorf("wrong search query %q", q)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"release-groups": []map[string]any{{"id": id, "title": "Album", "score": 100}}})
		case "/cover/" + id + "/front-250":
			coverHits.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(picture.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oldMB, oldCover, oldClient := mlibMusicBrainzURL, mlibCoverArtURL, mlibOnlineArtClient
	mlibMusicBrainzURL, mlibCoverArtURL, mlibOnlineArtClient = server.URL+"/mb", server.URL+"/cover/", server.Client()
	t.Cleanup(func() { mlibMusicBrainzURL, mlibCoverArtURL, mlibOnlineArtClient = oldMB, oldCover, oldClient })
	data, gotID, err := mlibLookupAlbumArt(context.Background(), "Artist", "Album")
	if err != nil || gotID != id || coverHits.Load() != 1 {
		t.Fatalf("cover lookup: id=%q hits=%d err=%v", gotID, coverHits.Load(), err)
	}
	scaled, err := mlibScaleOnlineArt(data)
	if err != nil || len(scaled) == 0 {
		t.Fatalf("cover scaling: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(scaled)); err != nil {
		t.Fatalf("scaled image is not JPEG: %v", err)
	}
}

func TestRecentlyAddedSortAndCoverLayoutValues(t *testing.T) {
	now := time.Now()
	games := []libraryGame{
		{Title: "A", AddedAt: now.Add(-time.Hour)},
		{Title: "B", AddedAt: now},
		{Title: "C"},
	}
	sortLibraryGames(games, librarySortAdded)
	if games[0].Title != "B" || games[1].Title != "A" || games[2].Title != "C" {
		t.Fatalf("recently added order: %v", games)
	}
	if libraryLayoutValue("unknown") != libraryLayoutList || libraryLayoutValue(libraryLayoutVertical) != libraryLayoutVertical {
		t.Fatal("cover layout normalization failed")
	}
}
