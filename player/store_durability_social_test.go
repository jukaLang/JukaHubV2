package main

// store_durability_social_test.go — the stores a reader or a player builds by
// hand that sit outside jukauser.json: saved browser pages, the sites where
// ad-blocking was turned off, remembered party hosts, the Watch Party archive,
// PS Remote pairing and the warm-start marker.
//
// Every one of them used to be written through a hand-rolled temp+rename with
// no fsync and no backup, and read with a bare Unmarshal that returned an empty
// store on a parse error. One truncated byte — a card pulled mid-write, a bad
// edit — therefore read as "you never had any", with a single log line to show
// for it. They now use the same pair as notes.json and jukaalarm.json:
// AtomicWrite leaves the previous version as <file>.bak, and readJSONWithBackup
// answers from it. These tests pin both halves, and the first-run case that
// must keep working: no file is an empty store, not damage.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// socialDurNoTemp fails when an atomic save left its scratch file behind.
func socialDurNoTemp(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("a temp file was left beside %s", path)
	}
}

// socialDurBackupContains requires the .bak beside the store to hold want, which
// is how the previous version is supposed to survive a save.
func socialDurBackupContains(t *testing.T, path, want string) {
	t.Helper()
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup beside %s: %v", path, err)
	}
	if !strings.Contains(string(backup), want) {
		t.Errorf("%s.bak does not hold %q:\n%s", path, want, backup)
	}
}

// socialDurCorrupt simulates the power cut every store here has to survive.
func socialDurCorrupt(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"truncated":`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// socialDurReloadBookmarks drops the in-memory list so the next read hits the
// card, the way a relaunch does.
func socialDurReloadBookmarks() {
	browserBookmarks.mu.Lock()
	browserBookmarks.list = nil
	browserBookmarks.loaded = false
	browserBookmarks.mu.Unlock()
}

// TestBrowserBookmarksSurviveADamagedFile covers the saved-page list, including
// the one destructive path: clearing every bookmark must take the backup with
// it, or the next damaged read brings the deleted pages back.
func TestBrowserBookmarksSurviveADamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-bookmarks.json")
	t.Cleanup(browserBookmarksResetForTest(path))

	if got := browserBookmarksCount(); got != 0 {
		t.Fatalf("a missing file read as %d bookmarks; the first run must be empty", got)
	}
	if !browserBookmarkAdd("https://example.com/first", "First") {
		t.Fatal("the first bookmark was not saved")
	}
	if !browserBookmarkAdd("https://example.com/second", "Second") {
		t.Fatal("the second bookmark was not saved")
	}
	socialDurNoTemp(t, path)
	socialDurBackupContains(t, path, "example.com/first")

	socialDurCorrupt(t, path)
	socialDurReloadBookmarks()
	marks := browserBookmarksList()
	if len(marks) != 1 || !strings.Contains(marks[0].URL, "example.com/first") {
		t.Fatalf("bookmarks after a damaged file = %+v, want the backed-up first entry", marks)
	}

	browserBookmarkRemove(marks[0].URL)
	for _, gone := range []string{path, path + ".bak"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still exists after clearing the list; a cleared list must not be resurrectable", gone)
		}
	}
}

// TestBrowserAdBlockSitesSurviveADamagedFile covers the per-site exceptions the
// reader remembers: losing them silently turns filtering back on for pages the
// user unblocked.
func TestBrowserAdBlockSitesSurviveADamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser-block.json")

	if got := loadBrowserAdBlockSites(path); len(got) != 0 {
		t.Fatalf("a missing file read as %v; the first run must be empty", got)
	}
	if err := saveBrowserAdBlockSites(path, map[string]bool{"ads.example": true}); err != nil {
		t.Fatal(err)
	}
	if err := saveBrowserAdBlockSites(path, map[string]bool{"ads.example": true, "tracker.example": true}); err != nil {
		t.Fatal(err)
	}
	socialDurNoTemp(t, path)
	socialDurBackupContains(t, path, "ads.example")
	if backup, err := os.ReadFile(path + ".bak"); err == nil && strings.Contains(string(backup), "tracker.example") {
		t.Errorf("the backup holds the newest list instead of the previous one:\n%s", backup)
	}

	socialDurCorrupt(t, path)
	sites := loadBrowserAdBlockSites(path)
	if len(sites) != 1 || !sites["ads.example"] {
		t.Fatalf("unblocked sites after a damaged file = %v, want the backed-up exception", sites)
	}
}

// TestPartyFriendsSurviveADamagedFile covers the remembered hosts: the invite
// line is the only way back into a room, and re-typing one from memory is not
// something the app can ask for.
func TestPartyFriendsSurviveADamagedFile(t *testing.T) {
	path := withPartyFriendsTempFile(t)

	if got := partyFriends(); len(got) != 0 {
		t.Fatalf("a missing file read as %+v; the first run must be empty", got)
	}
	if err := partyFriendAdd("Alice", "192.168.1.5:8318 ABCD2"); err != nil {
		t.Fatal(err)
	}
	if err := partyFriendAdd("Bob", "192.168.1.6:8318 EFGH3"); err != nil {
		t.Fatal(err)
	}
	socialDurNoTemp(t, path)
	socialDurBackupContains(t, path, "Alice")

	socialDurCorrupt(t, path)
	partyFriendsStore.mu.Lock()
	partyFriendsStore.loaded = false
	partyFriendsStore.items = nil
	partyFriendsStore.mu.Unlock()
	got := partyFriends()
	if len(got) != 1 || got[0].Name != "Alice" {
		t.Fatalf("friends after a damaged file = %+v, want the backed-up host", got)
	}
}

// TestPartyHistorySurvivesADamagedFile covers the watch-time archive: reading a
// damaged file as an empty one resets every member's cumulative total, which is
// the one thing the archive exists to keep.
func TestPartyHistorySurvivesADamagedFile(t *testing.T) {
	path := withPartyHistoryTempFile(t)
	addr := "192.168.1.5:8318"
	invite := addr + " ABCD2"

	if got := partyHistoryReport(); len(got.People) != 0 {
		t.Fatalf("a missing file read as %+v; the first run must be empty", got.People)
	}
	first := partyHistoryTestSnapshot("ABCD2", "Movie Night", addr, "Alice", "Dune",
		partyLeaderRow{Name: "Alice", WatchSecs: 60},
	)
	partyHistoryObserve(first, invite)
	partyHistoryFlush()

	// A second version makes the first one the backup.
	grown := partyHistoryTestSnapshot("ABCD2", "Movie Night", addr, "Alice", "Dune",
		partyLeaderRow{Name: "Alice", WatchSecs: 90},
	)
	partyHistoryObserve(grown, invite)
	partyHistoryFlush()

	socialDurNoTemp(t, path)
	socialDurBackupContains(t, path, `"watch_seconds": 60`)

	socialDurCorrupt(t, path)
	partyHistoryStore.mu.Lock()
	partyHistoryStore.loaded = false
	partyHistoryStore.data = newPartyHistoryData()
	partyHistoryStore.mu.Unlock()
	report := partyHistoryReport()
	if len(report.People) != 1 || report.People[0].Name != "Alice" || report.People[0].WatchSecs != 60 {
		t.Fatalf("archive after a damaged file = %+v, want the backed-up total of 60s", report.People)
	}
}

// TestPSRemotePairingSurvivesADamagedFile covers psremote.json. Its pairing keys
// cannot be regenerated without the console and a PIN on the TV, so a silent
// reset costs a re-pair, not just a preference.
func TestPSRemotePairingSurvivesADamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "psremote.json")
	prev := psHostsPathOverride
	psHostsPathOverride = path
	t.Cleanup(func() { psHostsPathOverride = prev })

	if got := loadPSHosts(); len(got.Hosts) != 0 {
		t.Fatalf("a missing file read as %+v; the first run must have no consoles", got.Hosts)
	}
	save := func(name string) {
		t.Helper()
		if err := savePSHosts(psHostsFile{Hosts: []psConsole{{
			Host:      "192.168.1.5",
			Name:      name,
			RegistKey: "rk-1",
			RPKey:     "rp-1",
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	save("Living Room")
	save("Living Room TV")

	socialDurNoTemp(t, path)
	socialDurBackupContains(t, path, "Living Room")
	socialDurBackupContains(t, path, "rk-1")

	socialDurCorrupt(t, path)
	got := loadPSHosts()
	if len(got.Hosts) != 1 {
		t.Fatalf("consoles after a damaged file = %+v, want the backed-up one", got.Hosts)
	}
	host := got.Hosts[0]
	if host.RegistKey != "rk-1" || host.RPKey != "rp-1" || !host.paired() {
		t.Fatalf("recovered console lost its pairing: %+v", host)
	}
	if host.Name != "Living Room" {
		t.Errorf("recovered console is %q, want the backed-up name", host.Name)
	}
}

// TestStartupStateSurvivesADamagedFile covers the warm-start marker: a corrupt
// one only costs a cold splash, but the store must not read as "never launched"
// while a good copy exists — and a marker from another build is still refused.
func TestStartupStateSurvivesADamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-state.json")

	if _, ok := loadStartupState(path); ok {
		t.Fatal("a missing marker read as a state")
	}
	save := func(total int64) {
		t.Helper()
		if err := saveStartupState(path, startupState{Version: startupStateVersion, TotalMS: total}); err != nil {
			t.Fatal(err)
		}
	}
	save(120)
	save(240)

	socialDurNoTemp(t, path)
	socialDurBackupContains(t, path, `"totalMs":120`)

	socialDurCorrupt(t, path)
	state, ok := loadStartupState(path)
	if !ok || state.TotalMS != 120 {
		t.Fatalf("marker after a damaged file = %+v (ok=%v), want the backed-up 120ms", state, ok)
	}

	// A version this build does not know is refused even when a good backup sits
	// beside it: the backup describes a layout that cannot be read either.
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadStartupState(path); ok {
		t.Error("a marker from another schema version was accepted")
	}
}
