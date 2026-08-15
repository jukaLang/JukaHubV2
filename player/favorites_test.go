package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func clearFavoritesStore() {
	favoritesStore = FavoritesStore{
		Videos: []FavoriteItem{},
		Recent: []FavoriteItem{},
		Files:  []FavoriteItem{},
		IPTV:   []FavoriteItem{},
	}
	favoritesCurrentTab = 0
	favoritesFocusIndex = 0
}

func TestFavoriteItemLabelVideo(t *testing.T) {
	v := VideoInfo{Title: "Test Video", ID: "abc123"}
	item := FavoriteItem{Type: "video", Data: v}
	if item.Label() != "Test Video" {
		t.Errorf("expected 'Test Video', got %q", item.Label())
	}
}

func TestFavoriteItemLabelFile(t *testing.T) {
	fe := FileEntry{Name: "movie.mp4", Path: "/media/movie.mp4"}
	item := FavoriteItem{Type: "file", Data: fe}
	if item.Label() != "movie.mp4" {
		t.Errorf("expected 'movie.mp4', got %q", item.Label())
	}
}

func TestFavoriteItemLabelIPTV(t *testing.T) {
	fe := FileEntry{Name: "BBC One", Path: "http://example.com/stream.m3u8"}
	item := FavoriteItem{Type: "iptv", Data: fe}
	if item.Label() != "BBC One" {
		t.Errorf("expected 'BBC One', got %q", item.Label())
	}
}

func TestAddFavoriteVideo(t *testing.T) {
	clearFavoritesStore()
	v := VideoInfo{Title: "My Video", ID: "vid1"}
	addFavoriteVideo(v)
	if len(favoritesStore.Videos) != 1 {
		t.Fatalf("expected 1 video favorite, got %d", len(favoritesStore.Videos))
	}
	if favoritesStore.Videos[0].Type != "video" {
		t.Errorf("expected type 'video', got %q", favoritesStore.Videos[0].Type)
	}
	if favoritesStore.Videos[0].Label() != "My Video" {
		t.Errorf("expected label 'My Video', got %q", favoritesStore.Videos[0].Label())
	}
}

func TestAddFavoriteVideoDeduplicate(t *testing.T) {
	clearFavoritesStore()
	v1 := VideoInfo{Title: "First", ID: "vid1"}
	v2 := VideoInfo{Title: "Second", ID: "vid1"}
	addFavoriteVideo(v1)
	addFavoriteVideo(v2)
	if len(favoritesStore.Videos) != 1 {
		t.Fatalf("expected 1 video after dedup, got %d", len(favoritesStore.Videos))
	}
	if favoritesStore.Videos[0].Label() != "Second" {
		t.Errorf("expected 'Second' (most recent), got %q", favoritesStore.Videos[0].Label())
	}
}

func TestAddRecentFile(t *testing.T) {
	clearFavoritesStore()
	addRecentFile("/media/videos/movie.mp4")
	if len(favoritesStore.Recent) != 1 {
		t.Fatalf("expected 1 recent file, got %d", len(favoritesStore.Recent))
	}
	if favoritesStore.Recent[0].Type != "file" {
		t.Errorf("expected type 'file', got %q", favoritesStore.Recent[0].Type)
	}
	fe, ok := favoritesStore.Recent[0].Data.(FileEntry)
	if !ok {
		t.Fatal("expected FileEntry data")
	}
	if fe.Path != "/media/videos/movie.mp4" {
		t.Errorf("expected path '/media/videos/movie.mp4', got %q", fe.Path)
	}
}

func TestAddRecentIPTV(t *testing.T) {
	clearFavoritesStore()
	ch := FileEntry{Name: "CNN", Path: "http://example.com/cnn.m3u8"}
	addRecentIPTV(ch)
	if len(favoritesStore.IPTV) != 1 {
		t.Fatalf("expected 1 IPTV entry, got %d", len(favoritesStore.IPTV))
	}
	if favoritesStore.IPTV[0].Type != "iptv" {
		t.Errorf("expected type 'iptv', got %q", favoritesStore.IPTV[0].Type)
	}
}

func TestRemoveFavoriteAt(t *testing.T) {
	clearFavoritesStore()
	v1 := VideoInfo{Title: "A", ID: "a"}
	v2 := VideoInfo{Title: "B", ID: "b"}
	addFavoriteVideo(v1)
	addFavoriteVideo(v2)
	removeFavoriteAt(0, 0)
	if len(favoritesStore.Videos) != 1 {
		t.Fatalf("expected 1 video after remove, got %d", len(favoritesStore.Videos))
	}
	if favoritesStore.Videos[0].Label() != "A" {
		t.Errorf("expected remaining 'A', got %q", favoritesStore.Videos[0].Label())
	}
}

func TestSaveAndLoadFavorites(t *testing.T) {
	clearFavoritesStore()
	tmpFile := "test_favorites.json"
	v := VideoInfo{Title: "Persist Test", ID: "persist1"}
	addFavoriteVideo(v)
	saveFavoritesTo(tmpFile)

	clearFavoritesStore()
	if len(favoritesStore.Videos) != 0 {
		t.Fatal("expected empty store after clear")
	}
	loadFavoritesFrom(tmpFile)
	if len(favoritesStore.Videos) != 1 {
		t.Fatalf("expected 1 video after reload, got %d", len(favoritesStore.Videos))
	}
	if favoritesStore.Videos[0].Label() != "Persist Test" {
		t.Errorf("expected 'Persist Test', got %q", favoritesStore.Videos[0].Label())
	}

	os.Remove(tmpFile)
}

func TestGetCurrentFavorites(t *testing.T) {
	clearFavoritesStore()
	v := VideoInfo{Title: "Tab Video", ID: "tv1"}
	favoritesStore.Videos = append(favoritesStore.Videos, FavoriteItem{Type: "video", Data: v})

	favoritesCurrentTab = 0
	items := getCurrentFavorites()
	if len(items) != 1 {
		t.Fatalf("expected 1 item in videos tab, got %d", len(items))
	}

	favoritesCurrentTab = 1
	items = getCurrentFavorites()
	if len(items) != 0 {
		t.Errorf("expected 0 items in recent tab, got %d", len(items))
	}
}

func TestFavoritesStoreJSONRoundTrip(t *testing.T) {
	store := FavoritesStore{
		Videos: []FavoriteItem{
			{Type: "video", Data: VideoInfo{Title: "V1", ID: "v1"}, Timestamp: time.Now()},
		},
		Recent: []FavoriteItem{
			{Type: "file", Data: FileEntry{Name: "f1", Path: "/f1"}, Timestamp: time.Now()},
		},
		Files: []FavoriteItem{},
		IPTV: []FavoriteItem{
			{Type: "iptv", Data: FileEntry{Name: "ch1", Path: "http://ch1"}, Timestamp: time.Now()},
		},
	}
	data, err := json.Marshal(store)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded FavoritesStore
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(decoded.Videos) != 1 {
		t.Errorf("expected 1 video, got %d", len(decoded.Videos))
	}
	if len(decoded.Recent) != 1 {
		t.Errorf("expected 1 recent, got %d", len(decoded.Recent))
	}
	if len(decoded.IPTV) != 1 {
		t.Errorf("expected 1 iptv, got %d", len(decoded.IPTV))
	}
}
