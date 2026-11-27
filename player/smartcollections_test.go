package main

// smartcollections_test.go — LIB-3: the automatic collections the metadata
// makes (genre, decade, player count), their regroup when details arrive late
// or change, the settings that hide them, and the Games library's rows built
// from the same store.
//
// The tests drive autoCollGroupGames, autoCollIDList and userCollectionRow:
// the decisions themselves, with fixtures, so nothing here reads the card, the
// network or the real user files.

import (
	"fmt"
	"reflect"
	"testing"
)

// smartColGame is one fixture game.
func smartColGame(system, title string) libraryGame {
	return libraryGame{
		Path:   "/roms/" + system + "/" + title + ".zip",
		System: system,
		Title:  title,
		Art:    "/art/" + title + ".png",
		HasArt: true,
	}
}

// smartColMeta is a test's details lookup.
func smartColMeta(meta map[string]autoCollMeta) func(libraryGame) (autoCollMeta, bool) {
	return func(g libraryGame) (autoCollMeta, bool) {
		m, ok := meta[g.Title]
		return m, ok
	}
}

// smartColFixture is the library the grouping tests use: five games across two
// consoles, with the details a metadata lookup would have.
func smartColFixture() ([]libraryGame, map[string]autoCollMeta) {
	games := []libraryGame{
		smartColGame("Mega Drive", "Sonic"),
		smartColGame("Mega Drive", "Streets of Rage"),
		smartColGame("SNES", "Chrono Trigger"),
		smartColGame("Mega Drive", "Golden Axe"),
		smartColGame("SNES", "Mario Kart"),
	}
	meta := map[string]autoCollMeta{
		"Sonic":           {Genre: "Action / Platformer", Year: "1991", Players: "1"},
		"Streets of Rage": {Genre: "Action", Year: "1991", Players: "2"},
		"Chrono Trigger":  {Genre: "Role-Playing (RPG)", Year: "1995", Players: "1"},
		"Golden Axe":      {Genre: "Action", Year: "1989", Players: "3"},
		"Mario Kart":      {Genre: "Racing", Year: "1992-08-27", Players: "1-4"},
	}
	return games, meta
}

// smartColFolder finds a folder by name.
func smartColFolder(t *testing.T, folders []autoCollGenre, name string) autoCollGenre {
	t.Helper()
	for _, f := range folders {
		if f.name == name {
			return f
		}
	}
	t.Fatalf("no %q folder in %v", name, smartColFolderNames(folders))
	return autoCollGenre{}
}

func smartColFolderNames(folders []autoCollGenre) []string {
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		out = append(out, f.name)
	}
	return out
}

func smartColTitles(games []libraryGame) []string {
	out := make([]string, 0, len(games))
	for _, g := range games {
		out = append(out, g.Title)
	}
	return out
}

// TestSmartCollectionsGroupByGenreDecadeAndPlayers is the grouping the task
// asks for: genre, decade and player count from the details, each a folder of
// its own, and a game may be in three at once.
func TestSmartCollectionsGroupByGenreDecadeAndPlayers(t *testing.T) {
	games, meta := smartColFixture()
	got := autoCollGroupGames(games, smartColMeta(meta))

	// Multiplayer keeps its old rule: two players or more.
	if want := []string{"Golden Axe", "Mario Kart", "Streets of Rage"}; !reflect.DeepEqual(smartColTitles(got.multi), want) {
		t.Fatalf("multiplayer = %v, want %v", smartColTitles(got.multi), want)
	}

	// Genres keep their old rule: three games or more.
	action := smartColFolder(t, got.genres, "Action")
	if want := []string{"Golden Axe", "Sonic", "Streets of Rage"}; !reflect.DeepEqual(smartColTitles(action.games), want) {
		t.Fatalf("Action = %v, want %v", smartColTitles(action.games), want)
	}
	if len(got.genres) != 1 {
		t.Fatalf("genres = %v, want Action alone (one-game genres stay out)", smartColFolderNames(got.genres))
	}

	// Decades: the year field, first four-digit year in it.
	if want := []string{"Golden Axe"}; !reflect.DeepEqual(smartColTitles(smartColFolder(t, got.decades, "1980s").games), want) {
		t.Fatalf("1980s = %v, want %v", smartColTitles(smartColFolder(t, got.decades, "1980s").games), want)
	}
	if want := []string{"Chrono Trigger", "Mario Kart", "Sonic", "Streets of Rage"}; !reflect.DeepEqual(smartColTitles(smartColFolder(t, got.decades, "1990s").games), want) {
		t.Fatalf("1990s = %v, want %v", smartColTitles(smartColFolder(t, got.decades, "1990s").games), want)
	}

	// Player counts: the most players the details give.
	if want := []string{"Chrono Trigger", "Sonic"}; !reflect.DeepEqual(smartColTitles(smartColFolder(t, got.players, "1 player").games), want) {
		t.Fatalf("1 player = %v, want %v", smartColTitles(smartColFolder(t, got.players, "1 player").games), want)
	}
	if want := []string{"Streets of Rage"}; !reflect.DeepEqual(smartColTitles(smartColFolder(t, got.players, "2 players").games), want) {
		t.Fatalf("2 players = %v, want %v", smartColTitles(smartColFolder(t, got.players, "2 players").games), want)
	}
	if want := []string{"Golden Axe", "Mario Kart"}; !reflect.DeepEqual(smartColTitles(smartColFolder(t, got.players, "3+ players").games), want) {
		t.Fatalf("3+ players = %v, want %v", smartColTitles(smartColFolder(t, got.players, "3+ players").games), want)
	}

	// One game, three folders: membership is a view, not a copy.
	sonicFolders := 0
	for _, list := range [][]autoCollGenre{got.genres, got.decades, got.players} {
		for _, f := range list {
			for _, g := range f.games {
				if g.Title == "Sonic" {
					sonicFolders++
				}
			}
		}
	}
	if sonicFolders != 3 {
		t.Fatalf("Sonic is in %d folders, want 3 (genre, decade, player count)", sonicFolders)
	}

	// The same listing and details give the same folders.
	if again := autoCollGroupGames(games, smartColMeta(meta)); !reflect.DeepEqual(got, again) {
		t.Fatal("grouping is not deterministic")
	}
	t.Logf("fixture: %d games -> %d genres, %d decades, %d player folders",
		len(games), len(got.genres), len(got.decades), len(got.players))
}

// TestSmartCollectionsRegroupWhenMetadataArrivesLate covers the details landing
// after Home already asked: no folder first, the right folders next, and a
// changed year moves the game without duplicating or dropping it.
func TestSmartCollectionsRegroupWhenMetadataArrivesLate(t *testing.T) {
	games, meta := smartColFixture()

	before := autoCollGroupGames(games, func(libraryGame) (autoCollMeta, bool) { return autoCollMeta{}, false })
	if len(before.genres) != 0 || len(before.decades) != 0 || len(before.players) != 0 || len(before.multi) != 0 {
		t.Fatalf("no details made folders: %d genres, %d decades, %d players, %d multiplayer",
			len(before.genres), len(before.decades), len(before.players), len(before.multi))
	}

	arrived := autoCollGroupGames(games, smartColMeta(meta))
	if len(arrived.decades) != 2 {
		t.Fatalf("details that arrived made %v, want 1980s and 1990s", smartColFolderNames(arrived.decades))
	}

	// A corrected year moves the game from one decade to the other, once.
	meta["Sonic"] = autoCollMeta{Genre: "Action / Platformer", Year: "1988", Players: "1"}
	changed := autoCollGroupGames(games, smartColMeta(meta))
	eighties := smartColTitles(smartColFolder(t, changed.decades, "1980s").games)
	if !reflect.DeepEqual(eighties, []string{"Golden Axe", "Sonic"}) {
		t.Fatalf("1980s after the correction = %v, want Golden Axe and Sonic", eighties)
	}
	nineties := smartColTitles(smartColFolder(t, changed.decades, "1990s").games)
	for _, title := range nineties {
		if title == "Sonic" {
			t.Fatalf("Sonic stayed in the 1990s: %v", nineties)
		}
	}
	// Every game is filed exactly once per criterion after the change.
	seen := map[string]int{}
	for _, f := range changed.decades {
		for _, g := range f.games {
			seen[g.Title]++
		}
	}
	if len(seen) != len(games) {
		t.Fatalf("%d games filed by decade, want %d: %v", len(seen), len(games), seen)
	}
	for title, n := range seen {
		if n != 1 {
			t.Fatalf("%s is in %d decade folders, want 1", title, n)
		}
	}
}

// TestSmartCollectionsDoNotTruncateALargeFolder is the old tag limit's failure
// mode: a collection bigger than 32 must list every game, not its first 32.
func TestSmartCollectionsDoNotTruncateALargeFolder(t *testing.T) {
	games := make([]libraryGame, 0, 40)
	meta := map[string]autoCollMeta{}
	for i := 0; i < 40; i++ {
		g := smartColGame("SNES", fmt.Sprintf("Game %02d", i))
		games = append(games, g)
		meta[g.Title] = autoCollMeta{Genre: "Action", Year: "1994", Players: "2"}
	}
	got := autoCollGroupGames(games, smartColMeta(meta))
	if n := len(smartColFolder(t, got.genres, "Action").games); n != 40 {
		t.Fatalf("Action lists %d games, want 40", n)
	}
	if n := len(smartColFolder(t, got.decades, "1990s").games); n != 40 {
		t.Fatalf("1990s lists %d games, want 40", n)
	}
	if n := len(smartColFolder(t, got.players, "2 players").games); n != 40 {
		t.Fatalf("2 players lists %d games, want 40", n)
	}
	// And the library row built from such a folder keeps them all.
	rows := libraryCollectionRows([]libraryCollectionRow{{ID: "auto-genre-action", Name: "Action", Games: got.genres[0].games}})
	if len(rows) != 1 || len(rows[0].Games) != 40 || rows[0].Collection != "auto-genre-action" {
		t.Fatalf("library row = %+v, want one row of 40 games", rows)
	}
}

// TestAutomaticCollectionSettingsGateTheRows proves Settings → Automatic
// collections governs what the Games library lists, and that turning a switch
// off takes effect on the next ask (the panel's cache key changes with it, so
// no restart is involved).
func TestAutomaticCollectionSettingsGateTheRows(t *testing.T) {
	oldGenres, oldDecades, oldPlayers := autoCollIdx.genres, autoCollIdx.decades, autoCollIdx.players
	defer func() {
		autoCollIdx.genres, autoCollIdx.decades, autoCollIdx.players = oldGenres, oldDecades, oldPlayers
	}()
	game := smartColGame("SNES", "Chrono Trigger")
	autoCollIdx.genres = []autoCollGenre{{id: autoCollGenrePrefix + "action", name: "Action", games: []libraryGame{game}}}
	autoCollIdx.decades = []autoCollGenre{{id: autoCollDecadePrefix + "1990s", name: "1990s", games: []libraryGame{game}}}
	autoCollIdx.players = []autoCollGenre{{id: autoCollPlayerPrefix + "1", name: "1 player", games: []libraryGame{game}}}

	on := &Config{Variables: Variables{Custom: map[string]interface{}{
		autoCollSettingKey: "last,decades",
	}}}
	ids := autoCollIDList(on)
	if !reflect.DeepEqual(ids, []string{autoCollIDLast, autoCollDecadePrefix + "1990s"}) {
		t.Fatalf("with only Last played and Decades on, rows = %v", ids)
	}

	off := &Config{Variables: Variables{Custom: map[string]interface{}{
		autoCollSettingKey: "none",
	}}}
	if ids := autoCollIDList(off); len(ids) != 0 {
		t.Fatalf("with every switch off, rows = %v, want none", ids)
	}
	if keyOn, keyOff := autoCollSwitchKey(on), autoCollSwitchKey(off); keyOn == keyOff {
		t.Fatal("the library's cache key does not change when a switch is turned off")
	}

	// A folder whose switch is off has no row even if it is built.
	rows := libraryCollectionRows([]libraryCollectionRow{{ID: autoCollGenrePrefix + "action", Name: "Action", Games: []libraryGame{game}}})
	if len(rows) != 1 {
		t.Fatalf("an enabled folder makes %d rows, want 1", len(rows))
	}
	gated := make([]libraryCollectionRow, 0)
	for _, id := range autoCollIDList(on) {
		if id == autoCollGenrePrefix+"action" {
			gated = append(gated, libraryCollectionRow{ID: id, Name: "Action", Games: []libraryGame{game}})
		}
	}
	if rows := libraryCollectionRows(gated); len(rows) != 0 {
		t.Fatalf("a switch that is off still listed %v", rows)
	}
}

// TestUserCollectionRowKeepsEveryGameAndSkipsNonGames is collections.json's
// half: only its games reach the library, each once, however many it holds.
func TestUserCollectionRowKeepsEveryGameAndSkipsNonGames(t *testing.T) {
	byPath := map[string]libraryGame{}
	col := homeTabsCollection{ID: "k3x9a1", Name: "Couch co-op"}
	for i := 0; i < 40; i++ {
		g := smartColGame("SNES", fmt.Sprintf("Co-op %02d", i))
		byPath[g.Path] = g
		col.Items = append(col.Items, homeTabsColItem{Ref: homeTabRefGame + g.Path, Title: g.Title})
	}
	// A duplicate and a non-game item: neither may double or empty the row.
	first := col.Items[0].Ref
	col.Items = append(col.Items,
		homeTabsColItem{Ref: first, Title: "Again"},
		homeTabsColItem{Ref: "app:do:Steam", Title: "Steam"},
	)

	row := userCollectionRow(col, byPath)
	if row.ID != col.ID || row.Name != col.Name {
		t.Fatalf("row = %+v, want the collection's own id and name", row)
	}
	if len(row.Games) != 40 {
		t.Fatalf("row has %d games, want 40 (duplicates and apps stay out)", len(row.Games))
	}
	rows := libraryCollectionRows([]libraryCollectionRow{row})
	if len(rows) != 1 || rows[0].Collection != "k3x9a1" || len(rows[0].Games) != 40 {
		t.Fatalf("library rows = %+v, want the collection's 40 games", rows)
	}
}

// TestTagDirectoryIsBoundedPastThirtyTwo is the 32-name cap's replacement: a
// tag directory that no longer stops at 32 (which read as a collection that
// vanished), still bounded, and per-game tagging unchanged.
func TestTagDirectoryIsBoundedPastThirtyTwo(t *testing.T) {
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("lib3tag%02d", i)
		if got := gmetaAddTag(name); got == "" {
			t.Fatalf("tag %q was refused; the directory still stops before 40", name)
		}
	}
	if len(gmetaAllTags()) < len(gmetaBuiltinTags)+40 {
		t.Fatalf("all tags = %d, want at least %d", len(gmetaAllTags()), len(gmetaBuiltinTags)+40)
	}
	gmetaStore.mu.Lock()
	n := len(gmetaStore.tags)
	gmetaStore.mu.Unlock()
	if n > gmetaMaxTagNames {
		t.Fatalf("the directory grew to %d, past its bound %d", n, gmetaMaxTagNames)
	}
	if gmetaMaxTagNames > 4096 {
		t.Fatalf("the directory bound %d is not a safe size", gmetaMaxTagNames)
	}

	// A game can still carry tags, and the built-in statuses are untouched.
	rec := gmetaSetTag("SNES", "Chrono Trigger", "lib3tag00", true)
	if !gmetaHasTag(rec, "lib3tag00") {
		t.Fatal("a custom tag was not put on the game")
	}
	if len(gmetaBuiltinTags) != 6 {
		t.Fatalf("built-in tags = %v, want six", gmetaBuiltinTags)
	}
}

// BenchmarkSmartCollectionGrouping measures the grouping pass over a library
// the size of a well-filled card.
func BenchmarkSmartCollectionGrouping(b *testing.B) {
	games := make([]libraryGame, 0, 500)
	meta := map[string]autoCollMeta{}
	genres := []string{"Action", "Racing", "Role-Playing (RPG)", "Puzzle", "Sports", "Fighting"}
	for i := 0; i < 500; i++ {
		g := smartColGame("SNES", fmt.Sprintf("Game %03d", i))
		games = append(games, g)
		meta[g.Title] = autoCollMeta{
			Genre:   genres[i%len(genres)],
			Year:    fmt.Sprintf("%d", 1980+i%30),
			Players: fmt.Sprintf("%d", 1+i%4),
		}
	}
	lookup := smartColMeta(meta)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		autoCollGroupGames(games, lookup)
	}
}
