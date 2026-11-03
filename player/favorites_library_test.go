package main

// favorites_library_test.go — the Favorites page is a library, not a flat link
// list. These pin the view model behind it: Continue only holds unfinished
// videos, saved videos inherit their resume point, Videos and Recent split into
// headings, and the list keeps the focused row in view.

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func videoFavorite(title, url string, position, duration float64) FavoriteItem {
	return FavoriteItem{Type: "video", Timestamp: time.Now(), Data: VideoInfo{
		Title: title, Uploader: title, WebpageURL: url, Position: position, Duration: duration,
	}}
}

func TestFavoritesInProgressSkipsFinishedAndUnstarted(t *testing.T) {
	now := time.Now()
	recent := []FavoriteItem{
		{Type: "video", Timestamp: now, Data: VideoInfo{Title: "mid", WebpageURL: "u1", Position: 100, Duration: 600}},
		{Type: "video", Timestamp: now, Data: VideoInfo{Title: "unstarted", WebpageURL: "u2"}},
		{Type: "video", Timestamp: now, Data: VideoInfo{Title: "finished", WebpageURL: "u3", Position: 595, Duration: 600}},
		{Type: "file", Timestamp: now, Data: FileEntry{Name: "notes.txt", Path: "/tmp/notes.txt"}},
	}
	got := favoritesInProgress(recent)
	if len(got) != 1 {
		t.Fatalf("Continue shelf = %d items, want only the video in progress", len(got))
	}
	if got[0].Label() != "mid" {
		t.Errorf("Continue row = %q, want mid", got[0].Label())
	}
}

// A saved video was written before it was ever played, so its own copy has no
// offset; the Continue timeline's offset has to fill it in or Resume starts
// over from zero.
func TestFavoritesWithResumeFillsSavedVideos(t *testing.T) {
	videos := []FavoriteItem{
		videoFavorite("saved", "https://youtu.be/a", 0, 600),
		videoFavorite("saved with own position", "https://youtu.be/b", 42, 600),
	}
	recent := []FavoriteItem{
		{Type: "video", Data: VideoInfo{WebpageURL: "https://youtu.be/a", Position: 120, Duration: 600}},
		{Type: "video", Data: VideoInfo{WebpageURL: "https://youtu.be/b", Position: 300, Duration: 600}},
	}
	got := favoritesWithResume(videos, recent)
	if v := got[0].Data.(VideoInfo); v.Position != 120 {
		t.Errorf("resume offset = %v, want the recent timeline's 120", v.Position)
	}
	if v := got[1].Data.(VideoInfo); v.Position != 42 {
		t.Errorf("resume offset = %v, want the saved copy's own 42", v.Position)
	}
}

func TestFavoritesVideosGroupByChannel(t *testing.T) {
	items := []FavoriteItem{
		videoFavorite("B one", "u1", 0, 0),
		videoFavorite("A one", "u2", 0, 0),
		videoFavorite("B two", "u3", 0, 0),
	}
	items[0].Data = VideoInfo{Title: "B one", Uploader: "bravo", WebpageURL: "u1"}
	items[1].Data = VideoInfo{Title: "A one", Uploader: "alpha", WebpageURL: "u2"}
	items[2].Data = VideoInfo{Title: "B two", Uploader: "bravo", WebpageURL: "u3"}

	groups := favoritesGroups(favTabVideos, items)
	if len(groups) != 2 || groups[0].label != "alpha" || groups[1].label != "bravo" {
		t.Fatalf("groups = %+v, want alpha then bravo", groups)
	}
	if len(groups[1].indexes) != 2 {
		t.Errorf("bravo holds %d rows, want 2", len(groups[1].indexes))
	}

	// A single channel needs no heading repeating every row.
	one := favoritesGroups(favTabVideos, items[1:2])
	if len(one) != 1 || one[0].label != "" {
		t.Errorf("single-channel groups = %+v, want one unlabelled group", one)
	}
}

func TestFavoritesRecentGroupsByDay(t *testing.T) {
	// Anchor to midday so a minus-26-hours offset cannot be read as two days
	// back when the suite runs just after midnight.
	now := time.Now()
	y, m, d := now.Date()
	midday := time.Date(y, m, d, 12, 0, 0, 0, now.Location())
	items := []FavoriteItem{
		{Type: "video", Timestamp: midday, Data: VideoInfo{Title: "today", WebpageURL: "u1"}},
		{Type: "video", Timestamp: midday.AddDate(0, 0, -1), Data: VideoInfo{Title: "yesterday", WebpageURL: "u2"}},
		{Type: "video", Timestamp: midday.AddDate(0, 0, -30), Data: VideoInfo{Title: "older", WebpageURL: "u3"}},
	}
	groups := favoritesGroups(favTabRecent, items)
	if len(groups) != 3 {
		t.Fatalf("groups = %+v, want three day buckets", groups)
	}
	for i, want := range []string{"Today", "Yesterday", "Earlier"} {
		if groups[i].label != want {
			t.Errorf("bucket %d = %q, want %q", i, groups[i].label, want)
		}
	}
}

// The focused row scrolls into view and never past the end of the content.
func TestFavoritesScrollKeepsFocusVisible(t *testing.T) {
	items := make([]FavoriteItem, 0, 12)
	for i := 0; i < 12; i++ {
		items = append(items, videoFavorite("v", "u", 0, 0))
	}
	list := sdl.Rect{X: 42, Y: 200, W: 1100, H: 200}
	lines := favoritesLines(favoritesGroups(favTabContinue, items), list)
	if len(lines) != len(items) {
		t.Fatalf("lines = %d, want %d", len(lines), len(items))
	}

	first := favoritesScrollShift(lines, 0, list)
	if first != 0 {
		t.Errorf("first row shift = %d, want 0", first)
	}
	last := len(items) - 1
	shift := favoritesScrollShift(lines, last, list)
	if shift <= 0 {
		t.Fatalf("last row shift = %d, want the list to scroll", shift)
	}
	top := lines[last].y - shift
	if top < list.Y || top+lines[last].h > list.Y+list.H {
		t.Errorf("focused row at %d..%d is outside the viewport %+v", top, top+lines[last].h, list)
	}
}

// Save is a first-class action beside Audio, Video and Chat.
func TestResultActionsIncludeSave(t *testing.T) {
	found := false
	for _, action := range resultActionOrder {
		if action == resultActionSave {
			found = true
		}
	}
	if !found {
		t.Fatalf("resultActionOrder = %v, want Save in it", resultActionOrder)
	}
	if label := resultActionLabel(resultActionSave); label != "Save" {
		t.Errorf("Save label = %q", label)
	}
	if got := len(resultActionOrder); got != resultActionCount-1 {
		t.Errorf("pills = %d, want one per action (%d)", got, resultActionCount-1)
	}
}
