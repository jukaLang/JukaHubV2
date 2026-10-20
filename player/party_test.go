package main

// Headless tests for Watch Party: presence expiry, watch-time leaderboards,
// queue edits, server-authoritative playback projection, drift thresholds,
// invite parsing and the HTTP endpoints. The room model and networking are
// SDL-free; one render probe checks the overlay actually draws.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func partyTestRoom(t *testing.T) (*partyRoom, string, time.Time) {
	t.Helper()
	clock := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	room, hostID := newPartyRoom("Movie Night", "Alice")
	room.now = func() time.Time { return clock }
	return room, hostID, clock
}

func TestPartyRoomStartsWithHost(t *testing.T) {
	room, hostID, _ := partyTestRoom(t)
	if !partyCodeValid(room.Code()) {
		t.Fatalf("code %q is not a valid room code", room.Code())
	}
	snap := room.Snapshot()
	if snap.HostID != hostID || len(snap.Members) != 1 {
		t.Fatalf("host snapshot = %+v", snap)
	}
	if !snap.Members[0].Host || snap.Members[0].Name != "Alice" {
		t.Errorf("host member = %+v", snap.Members[0])
	}
}

func TestPartyJoinAndAuthorize(t *testing.T) {
	room, _, _ := partyTestRoom(t)
	id, err := room.Join("Bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := room.Authorize(room.Code(), id); err != nil {
		t.Errorf("valid member rejected: %v", err)
	}
	if err := room.Authorize("WRONG", id); err == nil {
		t.Error("wrong room code accepted")
	}
	if err := room.Authorize(room.Code(), "nope"); err == nil {
		t.Error("unknown member accepted")
	}
	if snap := room.Snapshot(); len(snap.Members) != 2 {
		t.Errorf("members = %d, want 2", len(snap.Members))
	}
}

func TestPartyPresenceExpires(t *testing.T) {
	room, hostID, clock := partyTestRoom(t)
	bob, _ := room.Join("Bob")
	if err := room.Heartbeat(bob, 5); err != nil {
		t.Fatal(err)
	}
	// Move past the presence TTL without a heartbeat from Bob.
	room.now = func() time.Time { return clock.Add(partyPresenceTTL + time.Second) }
	if err := room.Heartbeat(hostID, 0); err != nil {
		t.Fatal(err)
	}
	snap := room.Snapshot()
	if len(snap.Members) != 1 || snap.Members[0].ID != hostID {
		t.Fatalf("stale member still present: %+v", snap.Members)
	}
	found := false
	for _, row := range snap.Leaderboard {
		if row.Name == "Bob" {
			found = true
			if row.Online {
				t.Error("departed member still marked online")
			}
		}
	}
	if !found {
		t.Error("departed member lost their leaderboard score")
	}
}

func TestPartyWatchTimeIsClampedAndRanked(t *testing.T) {
	room, hostID, _ := partyTestRoom(t)
	bob, _ := room.Join("Bob")

	if err := room.Heartbeat(hostID, 5); err != nil {
		t.Fatal(err)
	}
	// A client cannot claim an hour in one poll.
	if err := room.Heartbeat(bob, 3600); err != nil {
		t.Fatal(err)
	}
	snap := room.Snapshot()
	if len(snap.Leaderboard) != 2 {
		t.Fatalf("leaderboard = %+v", snap.Leaderboard)
	}
	if snap.Leaderboard[0].Name != "Bob" || snap.Leaderboard[0].WatchSecs != partyMaxWatchDelta {
		t.Errorf("top row = %+v, want Bob with %v", snap.Leaderboard[0], partyMaxWatchDelta)
	}
	if snap.Leaderboard[1].Name != "Alice" || snap.Leaderboard[1].WatchSecs != 5 {
		t.Errorf("second row = %+v", snap.Leaderboard[1])
	}
}

func TestPartyQueueLifecycle(t *testing.T) {
	room, hostID, _ := partyTestRoom(t)
	if _, err := room.AddQueue(hostID, "not a link", ""); err == nil {
		t.Error("invalid URL was queued")
	}
	item, err := room.AddQueue(hostID, "https://example.com/movie.mp4", "")
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "movie" {
		t.Errorf("derived title = %q, want movie", item.Title)
	}
	second, err := room.AddQueue(hostID, "https://example.com/live.m3u8", "Final")
	if err != nil {
		t.Fatal(err)
	}

	if err := room.SelectQueue(hostID, 0); err != nil {
		t.Fatal(err)
	}
	snap := room.Snapshot()
	if snap.Current != 0 || snap.Playback.URL != item.URL || !snap.Playback.Playing {
		t.Fatalf("select did not start playback: %+v", snap.Playback)
	}

	// Removing the current item keeps the queue and selection coherent.
	if err := room.RemoveQueue(hostID, item.ID); err != nil {
		t.Fatal(err)
	}
	snap = room.Snapshot()
	if len(snap.Queue) != 1 || snap.Queue[0].ID != second.ID {
		t.Fatalf("queue after remove = %+v", snap.Queue)
	}
	if snap.Current != 0 || snap.Playback.URL != second.URL {
		t.Errorf("playback after remove = %+v current=%d", snap.Playback, snap.Current)
	}

	if err := room.RemoveQueue(hostID, second.ID); err != nil {
		t.Fatal(err)
	}
	snap = room.Snapshot()
	if snap.Current != -1 || snap.Playback.URL != "" {
		t.Errorf("empty queue still has playback: %+v", snap.Playback)
	}
}

func TestPartyPlaybackProjection(t *testing.T) {
	room, hostID, clock := partyTestRoom(t)
	if err := room.SetPlayback(hostID, "https://example.com/a.mp4", "A", 10, true); err != nil {
		t.Fatal(err)
	}
	room.now = func() time.Time { return clock.Add(5 * time.Second) }
	snap := room.Snapshot()
	if snap.Playback.Position < 14.9 || snap.Playback.Position > 15.1 {
		t.Fatalf("projected position = %v, want 15", snap.Playback.Position)
	}

	// A paused room does not advance.
	if err := room.SetPlayback(hostID, "", "", 42, false); err != nil {
		t.Fatal(err)
	}
	room.now = func() time.Time { return clock.Add(60 * time.Second) }
	snap = room.Snapshot()
	if snap.Playback.Position != 42 {
		t.Errorf("paused position = %v, want 42", snap.Playback.Position)
	}
}

// Commands distinguish a real pause/seek from a poll repeating the same state.
// Without this a client could not tell a new instruction from its own refresh.
func TestPartyPlaybackCommandsAdvance(t *testing.T) {
	room, hostID, _ := partyTestRoom(t)
	if err := room.SetPlayback(hostID, "https://example.com/a.mp4", "A", 0, true); err != nil {
		t.Fatal(err)
	}
	first := room.Snapshot().Playback.Command
	if first == 0 {
		t.Fatal("playback command did not advance")
	}

	if err := room.SetPlayback(hostID, "", "", 12, false); err != nil {
		t.Fatal(err)
	}
	second := room.Snapshot().Playback.Command
	if second <= first {
		t.Fatalf("pause command %d did not advance past %d", second, first)
	}

	if _, err := room.AddQueue(hostID, "https://example.com/b.mp4", "B"); err != nil {
		t.Fatal(err)
	}
	if err := room.SelectQueue(hostID, 0); err != nil {
		t.Fatal(err)
	}
	third := room.Snapshot().Playback.Command
	if third <= second {
		t.Fatalf("select command %d did not advance past %d", third, second)
	}
}

func TestPartyLivePositionAndSeekThreshold(t *testing.T) {
	received := time.Now()
	snap := partySnapshot{Playback: partyPlayback{Position: 30, Playing: true}}
	if got := partyLivePosition(snap, received, received.Add(2*time.Second)); got < 31.9 || got > 32.1 {
		t.Errorf("live position = %v, want 32", got)
	}
	if got := partyLivePosition(snap, received, received); got != 30 {
		t.Errorf("zero elapsed = %v, want 30", got)
	}
	snap.Playback.Playing = false
	if got := partyLivePosition(snap, received, received.Add(9*time.Second)); got != 30 {
		t.Errorf("paused position = %v, want 30", got)
	}
	if partyNeedsSeek(10, 11, 2.5) {
		t.Error("small drift should not seek")
	}
	if !partyNeedsSeek(10, 14, 2.5) {
		t.Error("large drift should seek")
	}
}

func TestPartySnapshotJSONRoundTrip(t *testing.T) {
	room, hostID, _ := partyTestRoom(t)
	if _, err := room.AddQueue(hostID, "https://example.com/x.mp4", "X"); err != nil {
		t.Fatal(err)
	}
	if err := room.Heartbeat(hostID, 12); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(room.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var back partySnapshot
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Code != room.Code() || len(back.Queue) != 1 || len(back.Leaderboard) != 1 {
		t.Fatalf("round trip lost data: %+v", back)
	}
}

func TestPartyParseInvite(t *testing.T) {
	cases := []struct {
		in       string
		wantBase string
		wantCode string
		wantErr  bool
	}{
		{"192.168.1.5:8318 ABCD2", "http://192.168.1.5:8318", "ABCD2", false},
		{"http://10.0.0.2:9000/ XYZ34", "http://10.0.0.2:9000", "XYZ34", false},
		{"192.168.1.5 ABCD2", "http://192.168.1.5:8318", "ABCD2", false},
		{"192.168.1.5", "", "", true},
		{"192.168.1.5:8318 BAD", "", "", true},
		{"", "", "", true},
		{"ftp://1.2.3.4:21 ABCD2", "", "", true},
	}
	for _, c := range cases {
		base, code, err := partyParseInvite(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q parsed without error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if base != c.wantBase || code != c.wantCode {
			t.Errorf("%q -> %q %q, want %q %q", c.in, base, code, c.wantBase, c.wantCode)
		}
	}
}

func TestPartyHTTPFlow(t *testing.T) {
	room, hostID, _ := partyTestRoom(t)
	srv := newPartyServer(room)
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	post := func(path string, body interface{}) (*http.Response, partySnapshot) {
		data, _ := json.Marshal(body)
		resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var snap partySnapshot
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(resp.Body).Decode(&snap)
		}
		return resp, snap
	}

	// Wrong code is rejected before a member exists.
	bad, _ := post("/api/party/join", map[string]string{"code": "WRONG", "name": "Eve"})
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong join code = %d, want 401", bad.StatusCode)
	}

	resp, _ := post("/api/party/join", map[string]string{"code": room.Code(), "name": "Bob"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("join = %d", resp.StatusCode)
	}

	// The client polls with its member ID and a watch delta.
	client := newPartyClient(ts.URL, room.Code(), hostID)
	if !client.Poll(3) {
		t.Fatal("poll refused")
	}
	// The poll is asynchronous over HTTP; wait for the snapshot briefly.
	deadline := time.Now().Add(2 * time.Second)
	var snap partySnapshot
	for time.Now().Before(deadline) {
		if got, _, ok := client.Snapshot(); ok {
			snap = got
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(snap.Members) != 2 {
		t.Fatalf("state members = %d, want 2", len(snap.Members))
	}

	// Any member can queue and start a stream for the room.
	if _, err := client.Queue("https://example.com/party.mp4", "Party"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Select(0); err != nil {
		t.Fatal(err)
	}
	snap, _, _ = client.Snapshot()
	if snap.Playback.URL != "https://example.com/party.mp4" || !snap.Playback.Playing {
		t.Fatalf("playback not set over HTTP: %+v", snap.Playback)
	}

	// A member without the room code cannot mutate state.
	anon := newPartyClient(ts.URL, "WRONG", hostID)
	if _, err := anon.Queue("https://example.com/hack.mp4", ""); err == nil {
		t.Error("wrong room code could queue")
	}

	// Leaving drops presence but keeps the score.
	client.Leave()
	snap = room.Snapshot()
	for _, m := range snap.Members {
		if m.ID == hostID {
			t.Fatal("leave did not remove the member")
		}
	}
}

// The session layer is exercised against a real listener: join, poll, queue and
// select all travel over HTTP, exactly as they do between two handhelds.
func TestPartySessionJoinQueueAndSelect(t *testing.T) {
	room, _, _ := partyTestRoom(t)
	srv := newPartyServer(room)
	if err := srv.Start(); err != nil {
		t.Skipf("cannot listen for the party test: %v", err)
	}
	defer srv.Stop()
	t.Cleanup(partyLeaveSession)

	invite := fmt.Sprintf("127.0.0.1:%d %s", srv.Port(), room.Code())
	if err := partyJoin(invite, "Bob"); err != nil {
		t.Fatalf("join: %v", err)
	}
	if !partyActive() {
		t.Fatal("session is not active after join")
	}
	snap, _, ok, _ := partyState()
	if !ok || len(snap.Members) != 2 {
		t.Fatalf("joined snapshot = %+v", snap)
	}

	if err := partyQueueURL("https://example.com/e2e.mp4", "E2E"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := partySelectItem(0); err != nil {
		t.Fatalf("select: %v", err)
	}
	snap, _, _, _ = partyState()
	if snap.Playback.URL != "https://example.com/e2e.mp4" || !snap.Playback.Playing {
		t.Fatalf("session playback = %+v", snap.Playback)
	}
	if partyInviteText() == "" {
		t.Error("invite text is empty")
	}
}

func TestPartyLeaderboardKeepsDepartedScores(t *testing.T) {
	room, _, _ := partyTestRoom(t)
	bob, _ := room.Join("Bob")
	if err := room.Heartbeat(bob, 20); err != nil {
		t.Fatal(err)
	}
	room.Leave(bob)
	snap := room.Snapshot()
	if len(snap.Members) != 1 {
		t.Fatalf("members = %d, want 1", len(snap.Members))
	}
	if len(snap.Leaderboard) != 1 || snap.Leaderboard[0].Name != "Bob" || snap.Leaderboard[0].WatchSecs != partyMaxWatchDelta {
		t.Fatalf("leaderboard after leave = %+v", snap.Leaderboard)
	}
}

func withPartyFriendsTempFile(t *testing.T) string {
	t.Helper()
	prevPath := partyFriendsPath
	prevLoaded := partyFriendsStore.loaded
	prevItems := partyFriendsStore.items

	path := filepath.Join(t.TempDir(), partyFriendsFileName)
	partyFriendsPath = func() string { return path }
	partyFriendsStore.mu.Lock()
	partyFriendsStore.loaded = false
	partyFriendsStore.items = nil
	partyFriendsStore.mu.Unlock()

	t.Cleanup(func() {
		partyFriendsPath = prevPath
		partyFriendsStore.mu.Lock()
		partyFriendsStore.loaded = prevLoaded
		partyFriendsStore.items = prevItems
		partyFriendsStore.mu.Unlock()
	})
	return path
}

func TestPartyFriendsRoundTrip(t *testing.T) {
	path := withPartyFriendsTempFile(t)

	if err := partyFriendAdd("Alice", "192.168.1.5:8318 ABCD2"); err != nil {
		t.Fatal(err)
	}
	// The same invite updates the existing friend instead of duplicating it.
	if err := partyFriendAdd("Alice's TV", "http://192.168.1.5:8318 ABCD2"); err != nil {
		t.Fatal(err)
	}
	if err := partyFriendAdd("Nobody", "not an invite"); err == nil {
		t.Error("invalid invite was saved")
	}

	friends := partyFriends()
	if len(friends) != 1 {
		t.Fatalf("friends = %+v, want one entry", friends)
	}
	if friends[0].Name != "Alice's TV" {
		t.Errorf("name = %q, want the updated one", friends[0].Name)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("friends file not written: %v", err)
	}

	// Reload as a fresh launch would.
	partyFriendsStore.mu.Lock()
	partyFriendsStore.loaded = false
	partyFriendsStore.items = nil
	partyFriendsStore.mu.Unlock()
	if got := partyFriends(); len(got) != 1 || got[0].Name != "Alice's TV" {
		t.Fatalf("friends after reload = %+v", got)
	}

	partyFriendRemove(friends[0].Invite)
	if got := partyFriends(); len(got) != 0 {
		t.Fatalf("friends after remove = %+v", got)
	}
}

func TestPartyFriendsAreCapped(t *testing.T) {
	withPartyFriendsTempFile(t)
	for i := 0; i < partyFriendsLimit+4; i++ {
		invite := fmt.Sprintf("192.168.1.%d:%d ABCD2", i%250+1, 8300+i)
		if err := partyFriendAdd("Friend", invite); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if got := partyFriends(); len(got) > partyFriendsLimit {
		t.Fatalf("friends = %d, want at most %d", len(got), partyFriendsLimit)
	}
}

func TestPartyOverlayRenders(t *testing.T) {
	withPartyFriendsTempFile(t)
	cfg := &Config{}
	// The probe initialises SDL/TTF; fonts cannot load before it.
	f := newProbeFrame(t)
	if font, _ := getCachedFont(cfg, "small"); font == nil {
		t.Skip("no usable font in this environment")
	}
	prevW, prevH := screenWidth, screenHeight
	prevOpen := partyUI.open
	screenWidth, screenHeight = probeW, probeH
	partyUI.open = true
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		partyUI.open = prevOpen
		partyRowRects = nil
		partyPanelRect = sdl.Rect{}
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	partyRender(renderer, cfg)

	if partyPanelRect.W < 400 || partyPanelRect.H < 300 {
		t.Fatalf("panel rect = %+v, want a readable modal", partyPanelRect)
	}
	if len(partyRowRects) < 2 {
		t.Fatalf("rows drawn = %d, want host/join rows", len(partyRowRects))
	}
	colored := 0
	for y := partyPanelRect.Y; y < partyPanelRect.Y+partyPanelRect.H; y += 3 {
		for x := partyPanelRect.X; x < partyPanelRect.X+partyPanelRect.W; x += 3 {
			c := f.at(x, y)
			if c.R != f.bg.R || c.G != f.bg.G || c.B != f.bg.B {
				colored++
			}
		}
	}
	if colored < 500 {
		t.Fatalf("panel has only %d coloured pixels", colored)
	}
	if strings.TrimSpace(partyUI.flash) != "" {
		t.Errorf("unexpected flash %q", partyUI.flash)
	}
}
