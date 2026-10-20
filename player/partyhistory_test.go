package main

// Headless tests for the durable Watch Party archive: cumulative totals across
// rooms, per-host room history, idempotent snapshot folding and persistence.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func withPartyHistoryTempFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), partyHistoryFileName)

	prevPath := partyHistoryPath
	partyHistoryStore.mu.Lock()
	prevLoaded := partyHistoryStore.loaded
	prevData := partyHistoryStore.data
	prevDirty := partyHistoryStore.dirty
	prevSave := partyHistoryStore.lastSave

	partyHistoryPath = func() string { return path }
	partyHistoryStore.loaded = false
	partyHistoryStore.data = newPartyHistoryData()
	partyHistoryStore.dirty = false
	partyHistoryStore.lastSave = time.Time{}
	partyHistoryStore.mu.Unlock()

	t.Cleanup(func() {
		partyHistoryPath = prevPath
		partyHistoryStore.mu.Lock()
		partyHistoryStore.loaded = prevLoaded
		partyHistoryStore.data = prevData
		partyHistoryStore.dirty = prevDirty
		partyHistoryStore.lastSave = prevSave
		partyHistoryStore.mu.Unlock()
	})
	return path
}

func partyHistoryTestSnapshot(code, room, addr, host, title string, scores ...partyLeaderRow) partySnapshot {
	hostID := "host-" + code
	return partySnapshot{
		Code:     code,
		Name:     room,
		HostID:   hostID,
		HostAddr: addr,
		Members: []partyMember{
			{ID: hostID, Name: host, Host: true},
			{ID: "friend-" + code, Name: "Guest", Host: false},
		},
		Playback:    partyPlayback{URL: "https://example.com/party.mp4", Title: title, Playing: true},
		Leaderboard: scores,
	}
}

func TestPartyHistoryAccumulatesAcrossRooms(t *testing.T) {
	withPartyHistoryTempFile(t)
	addr := "192.168.1.5:8318"
	invite := addr + " ABCD2"

	movieNight := partyHistoryTestSnapshot("ABCD2", "Movie Night", addr, "Alice", "Dune",
		partyLeaderRow{Name: "Alice", WatchSecs: 60},
		partyLeaderRow{Name: "Bob", WatchSecs: 30},
	)
	partyHistoryObserve(movieNight, invite)
	// Polling the same room repeatedly must not count the same seconds twice.
	partyHistoryObserve(movieNight, invite)
	partyHistoryObserve(movieNight, invite)

	report := partyHistoryReport()
	if len(report.People) != 2 {
		t.Fatalf("people = %+v, want Alice and Bob", report.People)
	}
	if report.People[0].Name != "Alice" || report.People[0].WatchSecs != 60 || report.People[0].Rooms != 1 {
		t.Fatalf("Alice after one room = %+v", report.People[0])
	}
	if report.People[1].Name != "Bob" || report.People[1].WatchSecs != 30 || report.People[1].Rooms != 1 {
		t.Fatalf("Bob after one room = %+v", report.People[1])
	}
	if len(report.Rooms) != 1 || report.Rooms[0].WatchSecs != 90 || report.Rooms[0].Title != "Dune" {
		t.Fatalf("room after one room = %+v", report.Rooms)
	}
	if len(report.Friends) != 1 || report.Friends[0].Name != "Alice" || report.Friends[0].WatchSecs != 90 {
		t.Fatalf("friend after one room = %+v", report.Friends)
	}

	// A new room code is a new party, but the same friend and the same
	// cumulative leaderboard.
	scifi := partyHistoryTestSnapshot("EFGH3", "Sci-Fi Sunday", addr, "Alice", "Moon",
		partyLeaderRow{Name: "Alice", WatchSecs: 20},
	)
	partyHistoryObserve(scifi, addr+" EFGH3")

	report = partyHistoryReport()
	if report.People[0].Name != "Alice" || report.People[0].WatchSecs != 80 || report.People[0].Rooms != 2 {
		t.Fatalf("Alice after two rooms = %+v", report.People[0])
	}
	if report.People[1].Name != "Bob" || report.People[1].WatchSecs != 30 || report.People[1].Rooms != 1 {
		t.Fatalf("Bob after two rooms = %+v", report.People[1])
	}
	if len(report.Friends) != 1 || report.Friends[0].WatchSecs != 110 {
		t.Fatalf("friend after two rooms = %+v", report.Friends)
	}
	if len(report.Rooms) != 2 {
		t.Fatalf("rooms after two rooms = %+v", report.Rooms)
	}
	roomTitles := map[string]string{}
	for _, room := range report.Rooms {
		roomTitles[room.Code] = room.Name + "/" + room.Title
	}
	if roomTitles["ABCD2"] != "Movie Night/Dune" || roomTitles["EFGH3"] != "Sci-Fi Sunday/Moon" {
		t.Fatalf("room history = %+v", roomTitles)
	}
}

func TestPartyHistoryOnlyAddsScoreGrowth(t *testing.T) {
	withPartyHistoryTempFile(t)
	addr := "192.168.1.5:8318"
	first := partyHistoryTestSnapshot("ABCD2", "Movie Night", addr, "Alice", "Dune",
		partyLeaderRow{Name: "Alice", WatchSecs: 60},
	)
	partyHistoryObserve(first, addr+" ABCD2")

	growing := partyHistoryTestSnapshot("ABCD2", "Movie Night", addr, "Alice", "Dune",
		partyLeaderRow{Name: "Alice", WatchSecs: 95},
	)
	partyHistoryObserve(growing, addr+" ABCD2")
	if got := partyHistoryReport().People[0]; got.WatchSecs != 95 || got.Rooms != 1 {
		t.Fatalf("growth was over-counted: %+v", got)
	}

	// A reused room code that reports a lower score is a reset, not negative
	// watch time.
	reset := partyHistoryTestSnapshot("ABCD2", "Movie Night", addr, "Alice", "Dune",
		partyLeaderRow{Name: "Alice", WatchSecs: 10},
	)
	partyHistoryObserve(reset, addr+" ABCD2")
	if got := partyHistoryReport().People[0]; got.WatchSecs != 95 {
		t.Fatalf("score reset changed the archive: %+v", got)
	}
}

func TestPartyHistoryKeepsPerFriendRooms(t *testing.T) {
	withPartyHistoryTempFile(t)

	aliceA := partyHistoryTestSnapshot("ABCD2", "Movie Night", "192.168.1.5:8318", "Alice", "Dune",
		partyLeaderRow{Name: "Alice", WatchSecs: 60},
	)
	partyHistoryObserve(aliceA, "192.168.1.5:8318 ABCD2")
	aliceB := partyHistoryTestSnapshot("EFGH3", "Sci-Fi Sunday", "192.168.1.5:8318", "Alice", "Moon",
		partyLeaderRow{Name: "Alice", WatchSecs: 30},
	)
	partyHistoryObserve(aliceB, "192.168.1.5:8318 EFGH3")
	carol := partyHistoryTestSnapshot("JKMN4", "Cartoon Hour", "192.168.1.9:9000", "Carol", "Looney Tunes",
		partyLeaderRow{Name: "Carol", WatchSecs: 45},
	)
	partyHistoryObserve(carol, "192.168.1.9:9000 JKMN4")

	report := partyHistoryReport()
	if len(report.Friends) != 2 {
		t.Fatalf("friends = %+v, want Alice and Carol", report.Friends)
	}
	byName := map[string]partyFriendHistory{}
	for _, f := range report.Friends {
		byName[f.Name] = f
		if f.LastSeen == "" {
			t.Errorf("friend %q has no last-seen stamp", f.Name)
		}
	}
	if got := byName["Alice"]; got.WatchSecs != 90 {
		t.Errorf("Alice history = %+v, want 90s", got)
	}
	if got := byName["Carol"]; got.WatchSecs != 45 {
		t.Errorf("Carol history = %+v, want 45s", got)
	}
	counts := map[string]int{}
	for _, room := range report.Rooms {
		counts[room.Host]++
	}
	if counts["http://192.168.1.5:8318"] != 2 || counts["http://192.168.1.9:9000"] != 1 {
		t.Fatalf("rooms per host = %+v", counts)
	}
}

func TestPartyHistoryPersistsAndRebuildsRows(t *testing.T) {
	path := withPartyHistoryTempFile(t)
	snap := partyHistoryTestSnapshot("ABCD2", "Movie Night", "192.168.1.5:8318", "Alice", "Dune",
		partyLeaderRow{Name: "Alice", WatchSecs: 3600},
		partyLeaderRow{Name: "Bob", WatchSecs: 120},
	)
	partyHistoryObserve(snap, "192.168.1.5:8318 ABCD2")
	partyHistoryFlush()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("history file not written: %v", err)
	}

	// Reload as a fresh launch would.
	partyHistoryStore.mu.Lock()
	partyHistoryStore.loaded = false
	partyHistoryStore.data = newPartyHistoryData()
	partyHistoryStore.dirty = false
	partyHistoryStore.mu.Unlock()

	report := partyHistoryReport()
	if len(report.People) != 2 || report.People[0].Name != "Alice" || report.People[0].WatchSecs != 3600 {
		t.Fatalf("reloaded people = %+v", report.People)
	}
	if len(report.Rooms) != 1 || report.Rooms[0].Title != "Dune" {
		t.Fatalf("reloaded rooms = %+v", report.Rooms)
	}

	// The persisted baseline must stop the first poll after a relaunch from
	// counting the room's whole cumulative score a second time.
	partyHistoryObserve(snap, "192.168.1.5:8318 ABCD2")
	report = partyHistoryReport()
	if report.People[0].WatchSecs != 3600 || report.People[0].Rooms != 1 {
		t.Fatalf("reloaded re-poll duplicated time: %+v", report.People[0])
	}

	prevHistory := partyUI.history
	prevOpen := partyUI.open
	partyUI.history = true
	partyUI.open = true
	t.Cleanup(func() {
		partyUI.history = prevHistory
		partyUI.open = prevOpen
	})
	rows := partyHistoryRows()
	if len(rows) < 4 {
		t.Fatalf("history rows = %+v, want leaderboard and friend rows", rows)
	}
	var labels []string
	for _, row := range rows {
		labels = append(labels, row.label)
	}
	joined := strings.Join(labels, "|")
	for _, want := range []string{"ALL-TIME WATCH LEADERBOARD", "FRIEND HISTORY", "Alice", "Movie Night", "Back to watch party"} {
		if !strings.Contains(joined, want) {
			t.Errorf("history rows missing %q: %v", want, labels)
		}
	}
	if rows[len(rows)-1].kind != partyRowHistoryBack {
		t.Errorf("last history row = %+v, want Back", rows[len(rows)-1])
	}
}

func TestPartyHistoryOverlayRenders(t *testing.T) {
	withPartyHistoryTempFile(t)
	snap := partyHistoryTestSnapshot("ABCD2", "Movie Night", "192.168.1.5:8318", "Alice", "Dune",
		partyLeaderRow{Name: "Alice", WatchSecs: 3600},
	)
	partyHistoryObserve(snap, "192.168.1.5:8318 ABCD2")

	cfg := &Config{}
	f := newProbeFrame(t)
	if font, _ := getCachedFont(cfg, "small"); font == nil {
		t.Skip("no usable font in this environment")
	}
	prevW, prevH := screenWidth, screenHeight
	prevOpen, prevHistory := partyUI.open, partyUI.history
	screenWidth, screenHeight = probeW, probeH
	partyUI.open = true
	partyUI.history = true
	partyUI.cursor = 0
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		partyUI.open = prevOpen
		partyUI.history = prevHistory
		partyRowRects = nil
		partyPanelRect = sdl.Rect{}
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer renderer.Destroy()
	ClearTextCache()
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	partyRender(renderer, cfg)

	if partyPanelRect.W < 400 || partyPanelRect.H < 300 {
		t.Fatalf("panel rect = %+v, want a readable modal", partyPanelRect)
	}
	if len(partyRowRects) < 4 {
		t.Fatalf("history rows drawn = %d, want several", len(partyRowRects))
	}
}
