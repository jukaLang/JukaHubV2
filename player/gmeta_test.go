package main

// Tests for the game metadata: the clrmamepro DAT parser, the index that
// merges the libretro detail files and matches a game in it, the fetch and its
// cache (against an httptest server), the store's keys, tags and persistence,
// the library's tag filter, the milestones, the save-state listing and the slot
// staging, and the CRCs a ROM is matched by.

import (
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const gmetaTestGenreDAT = `clrmamepro (
	name "Nintendo - Game Boy Advance"
	description "Nintendo - Game Boy Advance"
)

game (
	comment "Advance Wars (USA)"
	genre "Strategy"
	rom ( crc 5ad0e571 )
)

game ( comment "Golden Sun (Japan)" genre "Role-playing (RPG)" rom ( crc 11111111 ) )
game ( comment "Golden Sun (USA, Europe)" genre "Action RPG" rom ( crc 22222222 ) )

game (
	name "Metroid Fusion (Europe)"
	genre "Action"
	rom ( name "Metroid Fusion (Europe).gba" size 8388608 crc 3d8f5f5b md5 0123456789abcdef0123456789abcdef )
)
`

const gmetaTestDeveloperDAT = `game ( comment "Advance Wars (USA)" developer "Intelligent Systems" rom ( crc 5AD0E571 ) )`

// gmetaTestYearDAT spells the year both ways the metadat files have.
const gmetaTestYearDAT = "game ( comment \"Advance Wars (USA)\" releaseyear \"2001\" rom ( crc 5AD0E571 ) )\n" +
	"game ( comment \"Metroid Fusion (Europe)\" year \"2002\" rom ( crc 3D8F5F5B ) )\n"

const gmetaTestUsersDAT = `game ( comment "Advance Wars (USA)" users 4 rom ( crc 5AD0E571 ) )`

// gmetaTestStore starts a test on an empty store in the suite's temporary data
// folder (TestMain points gmetaStorePath there).
func gmetaTestStore(t *testing.T) {
	t.Helper()
	path := gmetaStorePath()
	if path == "" || !strings.HasPrefix(filepath.Clean(path), filepath.Clean(os.TempDir())) {
		t.Skip("the store is not in a temporary folder")
	}
	clean := func() {
		for _, p := range []string{path, path + ".bak", path + ".tmp"} {
			_ = os.Remove(p)
		}
		gmetaResetForTest()
	}
	clean()
	t.Cleanup(clean)
}

func gmetaTestParse(t *testing.T, dat string) []gmetaDatEntry {
	t.Helper()
	entries, err := gmetaParseDAT(strings.NewReader(dat))
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestGmetaParseDAT(t *testing.T) {
	entries := gmetaTestParse(t, gmetaTestGenreDAT)
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4 (the header is not a game)", len(entries))
	}
	if e := entries[0]; e.Name != "Advance Wars (USA)" || e.CRC != "5AD0E571" || e.Fields["genre"] != "Strategy" {
		t.Errorf("first entry = %+v", e)
	}
	if e := entries[3]; e.Name != "Metroid Fusion (Europe)" || e.CRC != "3D8F5F5B" || e.Fields["genre"] != "Action" {
		t.Errorf("an entry named by name and a full rom line = %+v", e)
	}
	users := gmetaTestParse(t, gmetaTestUsersDAT)
	if len(users) != 1 || gmetaFieldValue("maxusers", users[0]) != "4" {
		t.Errorf("the maxusers file's \"users\" field was not read: %+v", users)
	}
	quoted := gmetaTestParse(t, `game ( comment "The \"Best\" Game" genre "Puzzle" )`)
	if len(quoted) != 1 || quoted[0].Name != `The "Best" Game` {
		t.Errorf("an escaped quote in a name = %+v", quoted)
	}
}

func TestGmetaIndexMatchesByCRCNameAndTitle(t *testing.T) {
	idx := gmetaBuildIndex(map[string][]gmetaDatEntry{
		"genre":       gmetaTestParse(t, gmetaTestGenreDAT),
		"developer":   gmetaTestParse(t, gmetaTestDeveloperDAT),
		"releaseyear": gmetaTestParse(t, gmetaTestYearDAT),
		"maxusers":    gmetaTestParse(t, gmetaTestUsersDAT),
	})
	info, ok := gmetaLookup(idx, []string{"deadbeef", "5ad0e571"}, "a renamed file")
	if !ok || info.Genre != "Strategy" || info.Developer != "Intelligent Systems" || info.Year != "2001" || info.Players != "4" {
		t.Errorf("by CRC = %+v (%v), want every detail of Advance Wars merged", info, ok)
	}
	info, ok = gmetaLookup(idx, nil, "Metroid Fusion (Europe)")
	if !ok || info.Genre != "Action" || info.Year != "2002" {
		t.Errorf("by exact name = %+v (%v), want Metroid Fusion with its year from the \"year\" field", info, ok)
	}
	info, ok = gmetaLookup(idx, nil, "golden_sun")
	if !ok || info.Genre != "Action RPG" {
		t.Errorf("by folded title = %+v (%v), want the USA release preferred", info, ok)
	}
	if _, ok := gmetaLookup(idx, nil, "Unknown Game"); ok {
		t.Error("a game the database does not have was matched")
	}
	if _, ok := gmetaLookup(nil, []string{"5AD0E571"}, "Advance Wars"); ok {
		t.Error("a nil index matched")
	}
	if gmetaPlayersLabel("1") != "1 player" || gmetaPlayersLabel("4") != "4 players" || gmetaPlayersLabel("") != "" {
		t.Error("gmetaPlayersLabel")
	}
	if gmetaLooseTitle("Legend of Zelda, The: A Link to the Past") != gmetaLooseTitle("The Legend of Zelda - A Link to the Past (USA)") {
		t.Error("the RetroAchievements and No-Intro spellings of one title do not meet")
	}
}

func TestGmetaLibretroFetchAndCache(t *testing.T) {
	const collection = "Nintendo - Game Boy Advance"
	served := map[string]string{
		"/metadat/genre/" + collection + ".dat":     gmetaTestGenreDAT,
		"/metadat/developer/" + collection + ".dat": gmetaTestDeveloperDAT,
	}
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		body, ok := served[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prevBase, prevDir := gmetaDatBaseURL, gmetaDatDir
	dir := t.TempDir()
	gmetaDatBaseURL = srv.URL
	gmetaDatDir = func() string { return dir }
	gmetaForgetIndex(collection)
	t.Cleanup(func() {
		gmetaDatBaseURL, gmetaDatDir = prevBase, prevDir
		gmetaForgetIndex(collection)
	})

	idx, res := gmetaIndexFor(context.Background(), collection, true)
	if res != gmetaFetchOK || idx == nil {
		t.Fatalf("online index = %v (result %d), want it fetched", idx, res)
	}
	if info, ok := gmetaLookup(idx, []string{"5AD0E571"}, ""); !ok || info.Genre != "Strategy" || info.Developer != "Intelligent Systems" {
		t.Errorf("fetched lookup = %+v (%v)", info, ok)
	}
	for _, field := range gmetaDatFields {
		path := gmetaDatPath(dir, field, collection)
		_, fileErr := os.Stat(path)
		_, markerErr := os.Stat(path + ".missing")
		switch field {
		case "genre", "developer":
			if fileErr != nil {
				t.Errorf("%s was not cached", field)
			}
		default:
			if markerErr != nil {
				t.Errorf("%s is not on the server, but that was not remembered", field)
			}
		}
	}

	// Offline, the cached files answer; nothing is asked of the server.
	gmetaForgetIndex(collection)
	mu.Lock()
	before := hits
	mu.Unlock()
	idx, res = gmetaIndexFor(context.Background(), collection, false)
	if res != gmetaFetchOK || idx == nil {
		t.Fatalf("offline index = %v (result %d), want the cached files", idx, res)
	}
	mu.Lock()
	after := hits
	mu.Unlock()
	if after != before {
		t.Errorf("the offline lookup made %d request(s)", after-before)
	}

	// Offline with nothing cached says so instead of failing.
	gmetaForgetIndex("Sony - PlayStation")
	if _, res := gmetaIndexFor(context.Background(), "Sony - PlayStation", false); res != gmetaFetchOffline {
		t.Errorf("offline with no cache = %d, want gmetaFetchOffline", res)
	}
}

func TestGmetaStoreKeysTagsAndPersistence(t *testing.T) {
	gmetaTestStore(t)
	rec := gmetaSetTag("Game Boy Advance", "Zelda (USA)", "favorite", true)
	if len(rec.Tags) != 1 || rec.Tags[0] != gmetaTagFavorite {
		t.Fatalf("tags = %v, want the built-in Favorite spelling", rec.Tags)
	}
	if got, ok := gmetaGet("Game Boy Advance", "zelda"); !ok || !gmetaHasTag(got, gmetaTagFavorite) {
		t.Error("\"Zelda (USA)\" and \"zelda\" are not the same game")
	}
	if _, ok := gmetaGet("SNES", "Zelda"); ok {
		t.Error("a game on another console shares the record")
	}
	gmetaUpdate("Game Boy Advance", "Zelda", func(r *gmetaRecord) {
		r.Description = "  A sword and a shield.  "
		r.Rating = 9
		r.CRC = "abcd1234"
	})
	gmetaResetForTest()
	got, ok := gmetaGet("Game Boy Advance", "Zelda")
	if !ok || got.Description != "A sword and a shield." || got.Rating != 5 || got.CRC != "ABCD1234" || !gmetaHasTag(got, gmetaTagFavorite) {
		t.Fatalf("read back = %+v (%v), want the cleaned record from the file", got, ok)
	}
	// A renamed file finds its record through the CRC.
	gmetaStore.mu.Lock()
	_, byCRC := gmetaFindLocked("Game Boy Advance", "Zelda - Renamed Dump", "abcd1234")
	gmetaStore.mu.Unlock()
	if byCRC == nil {
		t.Error("the record was not found by its CRC")
	}
	// Once the renamed file's CRC is read, it takes over what was written.
	renamed := gmetaAttachCRC("Game Boy Advance", "Zelda - Renamed Dump", "abcd1234")
	if renamed.Description != "A sword and a shield." || !gmetaHasTag(renamed, gmetaTagFavorite) || renamed.CRC != "ABCD1234" {
		t.Errorf("renamed record = %+v, want the description and tags of the old name", renamed)
	}
	if other := gmetaAttachCRC("SNES", "Zelda - Renamed Dump", "abcd1234"); other.Description != "" || len(other.Tags) != 0 {
		t.Errorf("a game on another console took the record over: %+v", other)
	}
	if name := gmetaAddTag("  co-op  "); name != "co-op" {
		t.Errorf("new tag = %q", name)
	}
	if name := gmetaAddTag("CO-OP"); name != "co-op" {
		t.Errorf("the same tag in capitals = %q, want the existing one", name)
	}
	if tags := gmetaAllTags(); len(tags) != 5 || tags[4] != "co-op" {
		t.Errorf("all tags = %v, want the four built-in and co-op", tags)
	}
	rec = gmetaSetTag("Game Boy Advance", "Zelda", gmetaTagFavorite, false)
	if gmetaHasTag(rec, gmetaTagFavorite) {
		t.Error("the tag was not removed")
	}
	if sorted := gmetaSortedTags([]string{"zz", gmetaTagBacklog, "aa", gmetaTagFavorite}); strings.Join(sorted, ",") != "Favorite,Backlog,aa,zz" {
		t.Errorf("sorted tags = %v", sorted)
	}
}

func TestGmetaTagFilterInTheLibrary(t *testing.T) {
	gmetaTestStore(t)
	prev, prevFilter := gameLib, gmetaFilter
	t.Cleanup(func() { gameLib, gmetaFilter = prev, prevFilter })
	gameOptsUI.open = false
	resetGameRandomPicker()
	gmetaCloseAll()
	gameLib.open = true
	gameLib.lib = gameLibraryReport{Systems: []librarySystem{
		{Name: "Game Boy Advance", Games: []libraryGame{
			{Path: "/roms/GBA/Zelda.gba", System: "Game Boy Advance", Title: "Zelda"},
			{Path: "/roms/GBA/Metroid.gba", System: "Game Boy Advance", Title: "Metroid"},
		}},
		{Name: "NES", Games: []libraryGame{{Path: "/roms/FC/Contra.nes", System: "NES", Title: "Contra"}}},
	}, Total: 3}
	gameLib.recents = []recentGame{{Path: "/roms/GBA/Zelda.gba", System: "Game Boy Advance", Title: "Zelda"}}
	gameLib.query = ""
	gmetaFilter.tag, gmetaFilter.paths = "", nil

	gmetaSetTag("Game Boy Advance", "Zelda", gmetaTagFavorite, true)
	gmetaSetTag("NES", "Contra", gmetaTagFavorite, true)
	gmetaSetTag("NES", "Contra", gmetaTagBacklog, true)
	if counts := gmetaTagCounts(gameLib.lib.Systems); counts[gmetaTagFavorite] != 2 || counts[gmetaTagBacklog] != 1 {
		t.Errorf("counts = %v", counts)
	}
	if n := len(currentGameSystems()); n != 3 {
		t.Fatalf("without a filter the library shows %d rows, want Continue and two consoles", n)
	}
	gmetaSetFilter(gmetaTagBacklog)
	systems := currentGameSystems()
	if len(systems) != 1 || systems[0].Name != "NES" || len(systems[0].Games) != 1 || systems[0].Games[0].Title != "Contra" {
		t.Fatalf("Backlog filter = %+v, want only Contra", systems)
	}
	if status := gameLibraryStatus(); !strings.Contains(status, "Showing Backlog") || !strings.Contains(status, "1 game") {
		t.Errorf("status = %q", status)
	}
	// A tag added while the filter is on shows at once.
	gmetaSetTag("Game Boy Advance", "Metroid", gmetaTagBacklog, true)
	if n := len(currentGameSystems()); n != 2 {
		t.Errorf("after tagging Metroid the filter shows %d consoles, want 2", n)
	}
	// Back clears the filter first; the library stays open.
	if !handleGameLibraryKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}, nil) {
		t.Fatal("Escape was not consumed")
	}
	if gmetaFilterTag() != "" || !gameLib.open {
		t.Fatalf("after Back: filter %q, library open %v; want the filter cleared and the library kept", gmetaFilterTag(), gameLib.open)
	}
	if n := len(currentGameSystems()); n != 3 {
		t.Errorf("after clearing the filter the library shows %d rows, want 3", n)
	}
}

func TestGmetaPageInputRouting(t *testing.T) {
	gmetaTestStore(t)
	trophyResetForTest()
	prev, prevVK, prevCelebrate := gameLib, virtualKeyboardActive, gmetaCelebrations
	gmetaCelebrations = true
	t.Cleanup(func() {
		gmetaCloseAll()
		gmetaSetFilter("")
		gameLib, virtualKeyboardActive, gmetaCelebrations = prev, prevVK, prevCelebrate
		trophyResetForTest()
	})
	virtualKeyboardActive = false
	gameOptsUI.open = false
	resetGameRandomPicker()
	gameLib.open = true
	gameLib.lib = gameLibraryReport{Systems: []librarySystem{
		{Name: "Game Boy Advance", Games: []libraryGame{
			{Path: "/roms/GBA/Zelda.gba", System: "Game Boy Advance", Title: "Zelda"},
		}},
	}, Total: 1}
	gameLib.recents = nil
	gameLib.query = ""
	gameLib.system, gameLib.cursor, gameLib.focus = 0, 0, 1
	press := func(button uint8) {
		t.Helper()
		ctrlGameLibrary(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}, nil)
	}

	press(sdl.CONTROLLER_BUTTON_A)
	if !gmetaUI.open || gmetaUI.game.Title != "Zelda" {
		t.Fatalf("A on a game opened %+v, want Zelda's page", gmetaUI.game)
	}
	if gmetaUI.zone != 0 || gmetaUI.button != gmetaBtnPlay {
		t.Error("the page does not open on Play")
	}
	press(sdl.CONTROLLER_BUTTON_X)
	if !gmetaHasTag(gmetaUI.rec, gmetaTagFavorite) {
		t.Error("X on the page did not favourite the game")
	}
	press(sdl.CONTROLLER_BUTTON_DPAD_RIGHT)
	press(sdl.CONTROLLER_BUTTON_DPAD_RIGHT)
	if gmetaUI.button != gmetaBtnTags {
		t.Fatalf("button after two Rights = %d, want Tags", gmetaUI.button)
	}
	press(sdl.CONTROLLER_BUTTON_A)
	if !gmetaTagUI.open {
		t.Fatal("A on Tags did not open the tag sheet")
	}
	press(sdl.CONTROLLER_BUTTON_DPAD_DOWN)
	press(sdl.CONTROLLER_BUTTON_DPAD_DOWN)
	press(sdl.CONTROLLER_BUTTON_A)
	if !gmetaHasTag(gmetaUI.rec, gmetaTagCompleted) || gmetaUI.rec.Milestones[gmetaMilestoneCompleted] == "" {
		t.Fatalf("ticking Completed = %+v, want the tag and the milestone", gmetaUI.rec)
	}
	if card, ok := trophyDequeue(); !ok || card.title != "Completed" || card.caption != "GAME MILESTONE" || card.detail != "Zelda" {
		t.Errorf("milestone card = %+v (%v), want Completed for Zelda", card, ok)
	}
	press(sdl.CONTROLLER_BUTTON_B)
	if gmetaTagUI.open || !gmetaUI.open {
		t.Fatal("B did not close the tag sheet alone")
	}
	press(sdl.CONTROLLER_BUTTON_B)
	if gmetaUI.open || !gameLib.open {
		t.Fatal("B did not close the page alone")
	}

	// L3 chooses a tag to show; Favorite is offered with its one game.
	press(sdl.CONTROLLER_BUTTON_LEFTSTICK)
	if !gmetaPick.open || len(gmetaPick.rows) < 2 || gmetaPick.rows[0].label != "All games" {
		t.Fatalf("L3 opened %+v, want the chooser with All games first", gmetaPick.rows)
	}
	for i, row := range gmetaPick.rows {
		if row.tag == gmetaTagFavorite {
			gmetaPick.cursor = i
		}
	}
	press(sdl.CONTROLLER_BUTTON_A)
	if gmetaPick.open || gmetaFilterTag() != gmetaTagFavorite {
		t.Fatalf("A on Favorite: chooser %v, filter %q", gmetaPick.open, gmetaFilterTag())
	}
	press(sdl.CONTROLLER_BUTTON_B)
	if gmetaFilterTag() != "" || !gameLib.open {
		t.Error("B did not clear the filter before leaving")
	}
}

func TestGmetaMilestones(t *testing.T) {
	ids := func(ms []gmetaMilestone) string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.ID
		}
		return strings.Join(out, ",")
	}
	crossed, past := gmetaTimeMilestones(1800, 5*3600+60, map[string]string{})
	if ids(crossed) != "1h,5h" || len(past) != 0 {
		t.Errorf("30 min → 5 h: crossed %s, past %s", ids(crossed), ids(past))
	}
	crossed, past = gmetaTimeMilestones(6*3600, 6*3600+30, map[string]string{})
	if len(crossed) != 0 || ids(past) != "1h,5h" {
		t.Errorf("already past 6 h: crossed %s, past %s", ids(crossed), ids(past))
	}
	crossed, _ = gmetaTimeMilestones(0, 11*3600, map[string]string{"1h": "x"})
	if ids(crossed) != "5h,10h" {
		t.Errorf("with 1 h recorded: crossed %s", ids(crossed))
	}

	gmetaTestStore(t)
	trophyResetForTest()
	prevGames, prevCelebrate := playtime.data.Games, gmetaCelebrations
	gameRecentsMu.Lock()
	prevRecents, prevLoaded := gameRecents, gameRecentsLoaded
	gameRecents, gameRecentsLoaded = nil, true
	gameRecentsMu.Unlock()
	playtime.data.Games = map[string]int64{}
	gmetaCelebrations = true
	t.Cleanup(func() {
		playtime.data.Games, gmetaCelebrations = prevGames, prevCelebrate
		gameRecentsMu.Lock()
		gameRecents, gameRecentsLoaded = prevRecents, prevLoaded
		gameRecentsMu.Unlock()
		trophyResetForTest()
	})

	game := libraryGame{Path: "/roms/GBA/Golden Sun.gba", System: "Game Boy Advance", Title: "Golden Sun"}
	gmetaNoteLaunch(game)
	if card, ok := trophyDequeue(); !ok || card.title != "First launch" {
		t.Fatalf("first launch card = %+v (%v)", card, ok)
	}
	gmetaCheckPlaytime(game, 2*3600)
	if card, ok := trophyDequeue(); !ok || card.title != "One hour in" || card.tier != trophyBronze {
		t.Fatalf("one hour card = %+v (%v)", card, ok)
	}
	gmetaCheckPlaytime(game, 2*3600+60)
	if n := trophyQueued(); n != 0 {
		t.Errorf("a check with nothing new queued %d card(s)", n)
	}

	// Hours from before the game was watched are recorded quietly, and so is
	// the "first" launch of a game that has been played already.
	old := libraryGame{Path: "/roms/GBA/Old.gba", System: "Game Boy Advance", Title: "Old Favourite"}
	gmetaCheckPlaytime(old, 12*3600)
	playtime.data.Games[old.Title] = 12 * 3600
	gmetaNoteLaunch(old)
	if n := trophyQueued(); n != 0 {
		t.Errorf("history was celebrated: %d card(s)", n)
	}
	if rec, _ := gmetaGet(old.System, old.Title); rec.Milestones["10h"] == "" || rec.Milestones[gmetaMilestoneFirst] == "" {
		t.Errorf("quiet milestones were not recorded: %+v", rec.Milestones)
	}
}

func TestGmetaStatesListAndStage(t *testing.T) {
	dir := t.TempDir()
	romDir := filepath.Join(dir, "Roms", "GBA")
	states := filepath.Join(dir, "states")
	rom := filepath.Join(romDir, "Zelda (USA).gba")
	rommWriteFile(t, rom, []byte("rom"))
	write := func(path, data string, age time.Duration) {
		t.Helper()
		rommWriteFile(t, path, []byte(data))
		at := time.Now().Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(states, "Zelda (USA).state"), "slot0", 3*time.Hour)
	write(filepath.Join(states, "Zelda (USA).state3"), "slot3", time.Hour)
	write(filepath.Join(states, "Zelda (USA).state3.png"), "png", time.Hour)
	write(filepath.Join(states, "Zelda (USA).state.auto"), "auto", 2*time.Hour)
	write(filepath.Join(states, "mGBA", "Zelda (USA).state1"), "slot1", 30*time.Minute)
	write(filepath.Join(states, "Metroid (USA).state2"), "other game", time.Minute)
	write(filepath.Join(romDir, "Zelda (USA).state5"), "beside the rom", 10*time.Minute)

	list := gmetaListStates(rom, []string{states, romDir})
	slots := make([]string, len(list))
	for i, st := range list {
		slots[i] = gmetaStateLabel(st)
	}
	if got := strings.Join(slots, ","); got != "Resume,Slot 5,Slot 1,Slot 3,Slot 0" {
		t.Fatalf("states = %s, want the auto state first, then newest first", got)
	}
	var slot3 gmetaState
	for _, st := range list {
		if st.Slot == 3 {
			slot3 = st
		}
		if (st.Slot == 3) != (st.Thumb != "") {
			t.Errorf("%s: screenshot %q", gmetaStateLabel(st), st.Thumb)
		}
	}

	for _, c := range []struct {
		name string
		slot int
		ok   bool
	}{
		{"Zelda.state", 0, true},
		{"Zelda.state12", 12, true},
		{"Zelda.state.auto", -1, true},
		{"zelda.STATE2", 2, true},
		{"Zelda.state3.png", 0, false},
		{"Zelda.state03", 0, false},
		{"Zelda.state.auto.jukahub-prev", 0, false},
		{"Other.state1", 0, false},
	} {
		slot, ok := gmetaStateSlot(c.name, "Zelda")
		if ok != c.ok || (ok && slot != c.slot) {
			t.Errorf("gmetaStateSlot(%q) = %d, %v; want %d, %v", c.name, slot, ok, c.slot, c.ok)
		}
	}

	// Starting from slot 3: it becomes the auto state, the old auto state is
	// kept, and the screenshot follows.
	if err := gmetaStageState(slot3); err != nil {
		t.Fatal(err)
	}
	auto := filepath.Join(states, "Zelda (USA).state.auto")
	if data, _ := os.ReadFile(auto); string(data) != "slot3" {
		t.Errorf("auto state = %q, want slot 3's", data)
	}
	if data, _ := os.ReadFile(auto + ".jukahub-prev"); string(data) != "auto" {
		t.Errorf("kept auto state = %q, want the old one", data)
	}
	if data, _ := os.ReadFile(auto + ".png"); string(data) != "png" {
		t.Errorf("auto screenshot = %q, want slot 3's", data)
	}
	for _, st := range gmetaListStates(rom, []string{states}) {
		if strings.Contains(st.Path, ".jukahub-") || strings.HasSuffix(st.Path, ".png") {
			t.Errorf("the listing shows %s", st.Path)
		}
	}
	if gmetaWhen(time.Now().Add(-30*time.Second), time.Now()) != "just now" {
		t.Error("gmetaWhen")
	}
}

func TestGmetaROMCRCs(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte{1, 2, 3, 4}, 4096)
	nes := append([]byte("NES\x1a\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), body...)
	nesPath := filepath.Join(dir, "Game.nes")
	rommWriteFile(t, nesPath, nes)
	want := fmt.Sprintf("%08X,%08X", crc32.ChecksumIEEE(nes), crc32.ChecksumIEEE(body))
	if got := strings.Join(gmetaROMCRCs(nesPath), ","); got != want {
		t.Errorf("iNES CRCs = %s, want with and without the header %s", got, want)
	}

	snes := rommTestSNES(0x10000+512, 512, 0x7FC0)
	snesPath := filepath.Join(dir, "Game.smc")
	rommWriteFile(t, snesPath, snes)
	want = fmt.Sprintf("%08X,%08X", crc32.ChecksumIEEE(snes), crc32.ChecksumIEEE(snes[512:]))
	if got := strings.Join(gmetaROMCRCs(snesPath), ","); got != want {
		t.Errorf("SNES CRCs = %s, want with and without the copier header %s", got, want)
	}

	gba := rommTestGBA(9)
	gbaPath := filepath.Join(dir, "Game.gba")
	rommWriteFile(t, gbaPath, gba)
	if got := gmetaROMCRCs(gbaPath); len(got) != 1 || got[0] != fmt.Sprintf("%08X", crc32.ChecksumIEEE(gba)) {
		t.Errorf("GBA CRCs = %v", got)
	}
	zipPath := filepath.Join(dir, "Game.zip")
	rommTestZip(t, zipPath, map[string][]byte{"Game.gba": gba})
	if got := gmetaROMCRCs(zipPath); len(got) != 1 || got[0] != fmt.Sprintf("%08X", crc32.ChecksumIEEE(gba)) {
		t.Errorf("zip CRCs = %v, want the game's from the zip directory", got)
	}
}
