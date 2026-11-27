package main

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestMusicNaturalLess(t *testing.T) {
	ordered := []string{
		"Album 2",
		"Album 02",
		"Album 10",
		"Track 1",
		"Track 2",
		"Track 10",
		"Track 100",
	}
	for i := 1; i < len(ordered); i++ {
		if !musicNaturalLess(ordered[i-1], ordered[i]) {
			t.Errorf("musicNaturalLess(%q, %q) = false, want true", ordered[i-1], ordered[i])
		}
		if musicNaturalLess(ordered[i], ordered[i-1]) {
			t.Errorf("musicNaturalLess(%q, %q) = true, want false", ordered[i], ordered[i-1])
		}
	}
	if musicNaturalLess("Track 2", "Track 2") {
		t.Error("musicNaturalLess is not a strict order")
	}
}

func musicShelfTestModel() *mlibModel {
	audio := func(path, title, artist, album string) mlibItem {
		return mlibItem{Path: filepath.FromSlash(path), Title: title, Artist: artist, Album: album, Duration: 120}
	}
	return &mlibModel{items: []mlibItem{
		audio("music/Album 10/Track 1.mp3", "Track 1", "Artist", "Ten"),
		audio("music/Album 2/Track 10.mp3", "Track 10", "Artist", "Two"),
		audio("music/Album 2/Track 2.mp3", "Track 2", "Artist", "Two"),
		audio("music/Other/Alpha.mp3", "Alpha", "Beta", "Other"),
		{Path: filepath.FromSlash("music/Other/Film.mp4"), Title: "Film", Video: true},
	}}
}

func musicShelfRowTitles(rows []musicShelfRow) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.title
	}
	return out
}

func musicShelfWantTitles(t *testing.T, got []musicShelfRow, want ...string) {
	t.Helper()
	titles := musicShelfRowTitles(got)
	if len(titles) != len(want) {
		t.Fatalf("titles = %v, want %v", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("titles = %v, want %v", titles, want)
		}
	}
}

func TestMusicShelfTracksAndFolders(t *testing.T) {
	m := musicShelfTestModel()

	tracks := musicShelfBuildRows(m, musicShelfTabTracks, "", "", "", "")
	musicShelfWantTitles(t, tracks, "Alpha", "Track 1", "Track 2", "Track 10")
	for _, row := range tracks {
		if row.kind != "track" || row.key != "" {
			t.Fatalf("Tracks row = %+v, want a whole-library track", row)
		}
	}

	folders := musicShelfBuildRows(m, musicShelfTabFolders, "", "", "", "")
	musicShelfWantTitles(t, folders, "Album 2", "Album 10", "Other")
	if folders[0].kind != "folder" || folders[0].sub != "2 track(s)" {
		t.Fatalf("first folder = %+v, want Album 2 with two tracks", folders[0])
	}

	albumTwo := filepath.FromSlash("music/Album 2")
	songs := musicShelfBuildRows(m, musicShelfTabFolders, "", "", albumTwo, "")
	musicShelfWantTitles(t, songs, "Track 2", "Track 10")
	for _, row := range songs {
		if row.kind != "track" || row.key != albumTwo {
			t.Fatalf("folder song = %+v, want a track of %s", row, albumTwo)
		}
	}
}

func TestMusicShelfSearch(t *testing.T) {
	m := musicShelfTestModel()

	cases := []struct {
		tab    int
		search string
		want   []string
	}{
		{musicShelfTabTracks, "beta", []string{"Alpha"}},
		{musicShelfTabTracks, "ten", []string{"Track 1"}},
		{musicShelfTabTracks, "two", []string{"Track 2", "Track 10"}},
		{musicShelfTabTracks, "track 2", []string{"Track 2"}},
		{musicShelfTabTracks, "missing", nil},
		{musicShelfTabFolders, "beta", []string{"Other"}},
		{musicShelfTabFolders, "two", []string{"Album 2"}},
		{musicShelfTabFolders, "missing", nil},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("tab%d/%s", tc.tab, tc.search), func(t *testing.T) {
			musicShelfWantTitles(t, musicShelfBuildRows(m, tc.tab, "", "", "", tc.search), tc.want...)
		})
	}

	albumTwo := filepath.FromSlash("music/Album 2")
	musicShelfWantTitles(t, musicShelfBuildRows(m, musicShelfTabFolders, "", "", albumTwo, "track 10"), "Track 10")
}

func TestMusicShelfSearchOnLargeLibrary(t *testing.T) {
	m := &mlibModel{items: make([]mlibItem, 0, 2000)}
	for i := 0; i < 2000; i++ {
		m.items = append(m.items, mlibItem{
			Path:   filepath.FromSlash(fmt.Sprintf("music/Album %d/Song %d.mp3", i/10, i)),
			Title:  fmt.Sprintf("Song %d", i),
			Artist: fmt.Sprintf("Artist %d", i%40),
			Album:  fmt.Sprintf("Album %d", i/10),
		})
	}
	rows := musicShelfBuildRows(m, musicShelfTabTracks, "", "", "", "song 1999")
	musicShelfWantTitles(t, rows, "Song 1999")
	folders := musicShelfBuildRows(m, musicShelfTabFolders, "", "", "", "album 199")
	if len(folders) != 1 || folders[0].title != "Album 199" || folders[0].sub != "10 track(s)" {
		t.Fatalf("folders matching album 199 = %+v, want Album 199 with ten tracks", folders)
	}
}
