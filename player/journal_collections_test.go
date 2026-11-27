package main

import (
	"strings"
	"testing"
	"time"
)

// The three status folders read the game page's tags. These tests make sure a
// tag change moves a game between folders without a second store, that Beaten
// this year respects the completion date, and that a card shows the same
// status the folders used.
func TestJournalStatusFolders(t *testing.T) {
	const system = "Journal Test Console"
	oldGames := autoCollIdx.games
	defer func() { autoCollIdx.games = oldGames }()

	write := func(title, tag, stamp string) {
		t.Helper()
		gmetaUpdate(system, title, func(rec *gmetaRecord) {
			if tag != "" {
				rec.Tags = []string{tag}
			}
			if stamp != "" {
				if rec.Milestones == nil {
					rec.Milestones = map[string]string{}
				}
				rec.Milestones[gmetaMilestoneCompleted] = stamp
			}
		})
	}
	thisYear := time.Now().Format(time.RFC3339)
	lastYear := time.Date(time.Now().Year()-1, 6, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	write("Waiting", gmetaTagBacklog, "")
	write("In progress", gmetaTagPlaying, "")
	write("Fresh win", gmetaTagCompleted, thisYear)
	write("Old win", gmetaTagCompleted, lastYear)
	write("Untouched", "", "")

	names := []string{"Waiting", "In progress", "Fresh win", "Old win", "Untouched"}
	var games []libraryGame
	for i, name := range names {
		games = append(games, libraryGame{
			System:     system,
			Title:      name,
			Path:       "roms/" + name + ".gba",
			AddedAt:    time.Date(2020, 1, i+1, 0, 0, 0, 0, time.UTC),
			LastPlayed: time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC),
		})
	}
	autoCollIdx.games = games

	if got := autoCollTagged(gmetaTagBacklog, false); len(got) != 1 || got[0].Title != "Waiting" {
		t.Fatalf("Backlog = %+v", got)
	}
	if got := autoCollTagged(gmetaTagPlaying, false); len(got) != 1 || got[0].Title != "In progress" {
		t.Fatalf("Playing now = %+v", got)
	}
	beaten := autoCollTagged(gmetaTagCompleted, true)
	if len(beaten) != 1 || beaten[0].Title != "Fresh win" {
		t.Fatalf("Beaten this year = %+v", beaten)
	}
	if got := autoCollName(autoCollIDBacklog); got != "Backlog" {
		t.Fatalf("folder name = %q", got)
	}
	if got := autoCollName(autoCollIDPlaying); got != "Playing now" {
		t.Fatalf("folder name = %q", got)
	}
	if got := autoCollName(autoCollIDBeaten); got != "Beaten this year" {
		t.Fatalf("folder name = %q", got)
	}
}

func TestNextUpPicksTheLongestWait(t *testing.T) {
	const system = "Next Up Console"
	old := time.Now().AddDate(0, 0, -30)
	set := func(title, tag string) {
		t.Helper()
		gmetaUpdate(system, title, func(rec *gmetaRecord) { rec.Tags = []string{tag} })
	}
	set("waited long", gmetaTagBacklog)
	set("new", gmetaTagBacklog)
	set("already playing", gmetaTagPlaying)
	lib := gameLibraryReport{Systems: []librarySystem{{Name: system, Games: []libraryGame{
		{System: system, Title: "waited long", Path: "roms/old.gba", AddedAt: old},
		{System: system, Title: "new", Path: "roms/new.gba", AddedAt: time.Now()},
		{System: system, Title: "already playing", Path: "roms/play.gba", AddedAt: old},
	}}}}
	game, days, ok := gameNextUpPick(lib)
	if !ok || game.Title != "waited long" {
		t.Fatalf("next up = %q, %d days, ok=%v", game.Title, days, ok)
	}
	if days != 30 {
		t.Fatalf("wait = %d days, want 30", days)
	}
	if label := gameWaitLabel(days); !strings.Contains(label, "30 days") {
		t.Fatalf("wait label = %q", label)
	}
	if label := gameWaitLabel(0); label != "just added" {
		t.Fatalf("new game label = %q", label)
	}
}

func TestJournalStatusChipFromTags(t *testing.T) {
	const system = "Journal Chip Console"
	write := func(title string, tags ...string) {
		t.Helper()
		gmetaUpdate(system, title, func(rec *gmetaRecord) { rec.Tags = tags })
	}
	write("Active", gmetaTagBacklog, gmetaTagPlaying)
	write("Done", gmetaTagCompleted, gmetaTagFavorite)
	write("Fav only", gmetaTagFavorite)
	write("Wanted", gmetaTagWishlist)
	write("Given up", gmetaTagDropped)
	write("No tags")

	for _, tc := range []struct {
		title, want string
	}{
		{"Active", gmetaTagPlaying},
		{"Done", gmetaTagCompleted},
		{"Fav only", gmetaTagFavorite},
		{"Wanted", gmetaTagWishlist},
		{"Given up", gmetaTagDropped},
		{"No tags", ""},
	} {
		rec, ok := gmetaGet(system, tc.title)
		if !ok {
			t.Fatalf("no record for %q", tc.title)
		}
		if got := gmetaStatus(rec); got != tc.want {
			t.Fatalf("%s: status = %q, want %q", tc.title, got, tc.want)
		}
	}

	// The library's decoration is the only source the cards read.
	games := []libraryGame{
		{System: system, Title: "Active"},
		{System: system, Title: "No tags"},
	}
	libraryDecorateGames(games, nil)
	if games[0].JournalStatus != gmetaTagPlaying {
		t.Fatalf("decorated status = %q", games[0].JournalStatus)
	}
	if strings.TrimSpace(games[1].JournalStatus) != "" {
		t.Fatalf("untagged game has status %q", games[1].JournalStatus)
	}
}
