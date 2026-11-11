package main

// boxart_auto_test.go — box art found in the background. What has to hold for
// a pass to run unattended: it starts only at a moment that costs the user
// nothing, it is stamped only when every title got an answer, the cheap look at
// the ROM folders sees new games but not saves, and a pass that found nothing
// says nothing.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBoxArtPassDue(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	stampAt := func(at time.Time) boxArtStamp {
		return boxArtStamp{Version: boxArtStampVersion, LastPass: at, Library: "abc"}
	}
	fresh := stampAt(now.Add(-2 * time.Hour))
	cases := []struct {
		name    string
		stamp   boxArtStamp
		have    bool
		library string
		want    bool
	}{
		{"no stamp: the first pass on this card", boxArtStamp{}, false, "abc", true},
		{"a stamp with no date", stampAt(time.Time{}), true, "abc", true},
		{"pre-2020 stamp (no RTC battery)", stampAt(time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)), true, "abc", true},
		{"future stamp (clock rolled back)", stampAt(now.Add(48 * time.Hour)), true, "abc", true},
		{"passed two hours ago, folders unchanged", fresh, true, "abc", false},
		{"passed two hours ago, folders not looked at yet", fresh, true, "", false},
		{"passed two hours ago, new games since", fresh, true, "def", true},
		{"passed yesterday", stampAt(now.Add(-25 * time.Hour)), true, "abc", true},
	}
	for _, tc := range cases {
		got, why := boxArtPassDue(tc.stamp, tc.have, tc.library, now)
		if got != tc.want {
			t.Errorf("%s: due = %v (%q), want %v", tc.name, got, why, tc.want)
		}
		if got && why == "" {
			t.Errorf("%s: a due pass gave no reason for the log", tc.name)
		}
	}
}

func TestBoxArtMomentOK(t *testing.T) {
	quiet := boxArtConditions{
		Enabled: true, Network: true, Online: true, ClockSet: true,
		Handheld: true, Battery: 80, Idle: 5 * time.Minute,
	}
	cases := []struct {
		name     string
		starting bool
		change   func(c *boxArtConditions)
		want     bool
	}{
		{"idle on a charged battery", true, func(*boxArtConditions) {}, true},
		{"switched off", true, func(c *boxArtConditions) { c.Enabled = false }, false},
		{"safe mode", true, func(c *boxArtConditions) { c.Network = false }, false},
		{"offline", true, func(c *boxArtConditions) { c.Online = false }, false},
		{"offline between batches", false, func(c *boxArtConditions) { c.Online = false }, false},
		{"the clock is not set", true, func(c *boxArtConditions) { c.ClockSet = false }, false},
		{"a game has the screen", true, func(c *boxArtConditions) { c.InGame = true }, false},
		{"a game started between batches", false, func(c *boxArtConditions) { c.InGame = true }, false},
		{"another fetch is running", true, func(c *boxArtConditions) { c.Fetching = true }, false},
		{"an install is running", true, func(c *boxArtConditions) { c.Installing = true }, false},
		{"an install started mid-pass", false, func(c *boxArtConditions) { c.Installing = true }, true},
		{"low battery", true, func(c *boxArtConditions) { c.Battery = 15 }, false},
		{"low battery mid-pass", false, func(c *boxArtConditions) { c.Battery = 15 }, false},
		{"low battery on the charger", true, func(c *boxArtConditions) { c.Battery, c.Charging = 15, true }, true},
		{"exactly the minimum", true, func(c *boxArtConditions) { c.Battery = boxArtMinBattery }, true},
		{"on battery while the user is busy", true, func(c *boxArtConditions) { c.Idle = 10 * time.Second }, false},
		{"busy, but the pass already started", false, func(c *boxArtConditions) { c.Idle = 10 * time.Second }, true},
		{"busy, but charging", true, func(c *boxArtConditions) { c.Idle, c.Charging = 10*time.Second, true }, true},
		{"a desktop reads no battery", true, func(c *boxArtConditions) { c.Handheld, c.Battery, c.Idle = false, -1, 0 }, true},
		{"a handheld with no reading, busy", true, func(c *boxArtConditions) { c.Battery, c.Idle = -1, 0 }, false},
		{"a handheld with no reading, idle", true, func(c *boxArtConditions) { c.Battery = -1 }, true},
	}
	for _, tc := range cases {
		c := quiet
		tc.change(&c)
		got, why := boxArtMomentOK(c, tc.starting)
		if got != tc.want {
			t.Errorf("%s: ok = %v (%q), want %v", tc.name, got, why, tc.want)
		}
		if !got && why == "" {
			t.Errorf("%s: a refusal gave no reason for the log", tc.name)
		}
	}
}

func TestBoxArtNoteGameIsNotTimeAway(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := &boxArtScheduler{}
	// No game seen yet: the input clock answers alone.
	c := boxArtConditions{Idle: time.Hour}
	s.noteGame(&c, now)
	if c.Idle != time.Hour {
		t.Fatalf("idle with no game seen = %s, want 1h0m0s", c.Idle)
	}
	c = boxArtConditions{InGame: true, Idle: time.Hour}
	s.noteGame(&c, now)
	// A minute after the game: the hour without a press in JukaHub was play.
	c = boxArtConditions{Idle: time.Hour}
	s.noteGame(&c, now.Add(time.Minute))
	if c.Idle != time.Minute {
		t.Fatalf("idle a minute after a game = %s, want 1m0s", c.Idle)
	}
	quiet := boxArtConditions{Enabled: true, Network: true, Online: true, ClockSet: true, Handheld: true, Battery: 80, Idle: c.Idle}
	if ok, _ := boxArtMomentOK(quiet, true); ok {
		t.Fatal("a pass on battery started a minute after a game")
	}
	// A press since then is the shorter answer and wins.
	c = boxArtConditions{Idle: 10 * time.Second}
	s.noteGame(&c, now.Add(5*time.Minute))
	if c.Idle != 10*time.Second {
		t.Fatalf("idle after a press = %s, want 10s", c.Idle)
	}
}

func TestBoxArtHeld(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if boxArtHeld(time.Time{}, now) {
		t.Error("no hold was treated as one")
	}
	if !boxArtHeld(now.Add(time.Minute), now) {
		t.Error("a hold a minute from now did not hold")
	}
	if boxArtHeld(now.Add(-time.Minute), now) {
		t.Error("a hold that has ended still held")
	}
}

func TestBoxArtRetryDelay(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{1, 30 * time.Minute},
		{2, time.Hour},
		{3, 2 * time.Hour},
		{6, 16 * time.Hour},
		{7, 24 * time.Hour},
		{40, 24 * time.Hour},
	}
	for _, tc := range cases {
		if got := boxArtRetryDelay(tc.failures); got != tc.want {
			t.Errorf("wait after %d failed pass(es) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}

func TestBoxArtFoundMessage(t *testing.T) {
	cases := []struct {
		found int
		want  string
	}{
		{0, ""},
		{-3, ""},
		{1, "Found box art for 1 game"},
		{12, "Found box art for 12 games"},
	}
	for _, tc := range cases {
		if got := boxArtFoundMessage(tc.found); got != tc.want {
			t.Errorf("message for %d = %q, want %q", tc.found, got, tc.want)
		}
	}
}

func TestBoxArtStampRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, boxArtStampFile)
	if _, ok := loadBoxArtStamp(path); ok {
		t.Fatal("a missing stamp file was read as a stamp")
	}
	want := boxArtStamp{
		Version:  boxArtStampVersion,
		LastPass: time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC),
		Library:  "1f2e3d",
		Games:    412,
		Found:    12,
	}
	if err := saveBoxArtStamp(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := loadBoxArtStamp(path)
	if !ok {
		t.Fatal("the saved stamp did not load")
	}
	if !got.LastPass.Equal(want.LastPass) || got.Library != want.Library || got.Games != want.Games || got.Found != want.Found {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	// A stamp from another layout, or a damaged one, is no stamp: the next pass
	// is due instead of being held back by a file this build cannot read.
	foreign := filepath.Join(dir, "foreign.json")
	if err := os.WriteFile(foreign, []byte(`{"version": 99, "lastPass": "2026-11-10T09:30:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadBoxArtStamp(foreign); ok {
		t.Error("a stamp with another version was accepted")
	}
	damaged := filepath.Join(dir, "damaged.json")
	if err := os.WriteFile(damaged, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadBoxArtStamp(damaged); ok {
		t.Error("a damaged stamp was accepted")
	}
	if _, ok := loadBoxArtStamp(""); ok {
		t.Error("no path gave a stamp")
	}
	if err := saveBoxArtStamp("", want); err != nil {
		t.Errorf("saving with no data folder = %v, want a quiet no-op", err)
	}
}

// boxArtWriteFile creates a file, and its folders, for a test tree.
func boxArtWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBoxArtLibrarySignature(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Roms")
	boxArtWriteFile(t, filepath.Join(root, "GBA", "Metroid Fusion (USA).gba"))
	boxArtWriteFile(t, filepath.Join(root, "PS", "Final Fantasy VII", "Final Fantasy VII (Disc 1).chd"))
	roots := []string{root}
	base := boxArtLibrarySignature(roots)
	if base == "" {
		t.Fatal("no signature for a readable library")
	}
	if again := boxArtLibrarySignature(roots); again != base {
		t.Fatalf("the same folders gave two signatures: %s then %s", base, again)
	}

	// What is not a new game must not look like one: a save beside the ROM,
	// the firmware's own Imgs folder, a folder the scanner does not read, and
	// the dot files a Mac leaves behind.
	boxArtWriteFile(t, filepath.Join(root, "GBA", "Metroid Fusion (USA).srm"))
	boxArtWriteFile(t, filepath.Join(root, "GBA", "Imgs", "Metroid Fusion (USA).png"))
	boxArtWriteFile(t, filepath.Join(root, "BIOS", "scph1001.bin"))
	boxArtWriteFile(t, filepath.Join(root, "GBA", "._Metroid Fusion (USA).gba"))
	if got := boxArtLibrarySignature(roots); got != base {
		t.Fatalf("a save, Imgs, BIOS or a dot file changed the signature (%s, then %s)", base, got)
	}

	// A new game at the top of a console folder does.
	boxArtWriteFile(t, filepath.Join(root, "GBA", "Golden Sun (USA).gba"))
	withGame := boxArtLibrarySignature(roots)
	if withGame == base {
		t.Fatal("a new ROM did not change the signature")
	}

	// So does a new console folder.
	if err := os.MkdirAll(filepath.Join(root, "SFC"), 0o755); err != nil {
		t.Fatal(err)
	}
	withConsole := boxArtLibrarySignature(roots)
	if withConsole == withGame {
		t.Fatal("a new console folder did not change the signature")
	}

	// A game kept in a sub-folder is followed by that folder's time, which
	// Linux moves when a disc is added inside it.
	when := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "PS", "Final Fantasy VII"), when, when); err != nil {
		t.Fatal(err)
	}
	if got := boxArtLibrarySignature(roots); got == withConsole {
		t.Fatal("a game folder's new time did not change the signature")
	}

	// A root that cannot be read still answers, the same way each time and not
	// like a real library, so a card that appears later is noticed.
	missing := []string{filepath.Join(t.TempDir(), "absent")}
	first, second := boxArtLibrarySignature(missing), boxArtLibrarySignature(missing)
	if first == "" || first != second {
		t.Fatalf("an absent root gave %q, then %q", first, second)
	}
	if first == base {
		t.Fatal("an absent root matched a real library")
	}
}

func TestBoxArtMissKeyMatchesTheFetcher(t *testing.T) {
	rom := scannedROM{
		Path:       filepath.Join("Roms", "GBA", "Metroid Fusion (USA).gba"),
		System:     "Game Boy Advance",
		Collection: "Nintendo - Game Boy Advance",
	}
	// fetchROMArtworkTracked files a 404 under the collection and the stem.
	if got, want := boxArtMissKey(rom), "Nintendo - Game Boy Advance/Metroid Fusion (USA)"; got != want {
		t.Fatalf("miss key = %q, want %q", got, want)
	}
}

func TestBoxArtTitlesToTryAndTally(t *testing.T) {
	dest := t.TempDir()
	romDir := filepath.Join(t.TempDir(), "Roms", "GBA")
	rom := func(name string) scannedROM {
		return scannedROM{Path: filepath.Join(romDir, name), System: "Game Boy Advance", Collection: "Nintendo - Game Boy Advance"}
	}
	saved := rom("Metroid Fusion (USA).gba")
	unlisted := rom("My Homebrew.gba")
	stale := rom("Golden Sun (USA).gba")
	fresh := rom("Advance Wars (USA).gba")
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	misses := map[string]time.Time{
		// Asked yesterday and not in the thumbnail set: not asked again yet.
		boxArtMissKey(unlisted): now.Add(-24 * time.Hour),
		// Asked two months ago: the set may have it by now.
		boxArtMissKey(stale): now.Add(-2 * artworkMissTTL),
	}
	cover := romArtworkPath(saved, dest)
	if err := os.MkdirAll(filepath.Dir(cover), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cover, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The pass drops the title answered yesterday and keeps the rest.
	todo := boxArtTitlesToTry([]scannedROM{unlisted, stale, fresh}, misses, now)
	if len(todo) != 2 || todo[0] != stale || todo[1] != fresh {
		t.Fatalf("titles to try = %+v, want the old miss and the title never asked about", todo)
	}
	if got := boxArtTitlesToTry(nil, misses, now); len(got) != 0 {
		t.Fatalf("an empty list gave %d titles", len(got))
	}

	// After a batch: one cover on the card, one real "not found", and two with
	// no answer — which is what keeps a pass from being stamped.
	found, notFound, unanswered := boxArtTally([]scannedROM{saved, unlisted, stale, fresh}, dest, misses, now)
	if found != 1 || notFound != 1 || unanswered != 2 {
		t.Fatalf("tally = %d found, %d not found, %d unanswered; want 1, 1, 2", found, notFound, unanswered)
	}
}

func TestBoxArtAutoEnabled(t *testing.T) {
	if !boxArtAutoEnabled(nil) {
		t.Error("with no config the switch should read on")
	}
	if !boxArtAutoEnabled(&Config{Variables: Variables{Custom: map[string]interface{}{}}}) {
		t.Error("an unset switch should read on")
	}
	cases := []struct {
		name  string
		value interface{}
		want  bool
	}{
		{"stored on", "true", true},
		{"stored off", "false", false},
		{"a JSON false", false, false},
		{"a JSON true", true, true},
		{"off in words", "off", false},
		{"no", "no", false},
		{"zero", float64(0), false},
		{"a garbled value keeps the default", "maybe", true},
		{"null keeps the default", nil, true},
	}
	for _, tc := range cases {
		cfg := &Config{Variables: Variables{Custom: map[string]interface{}{boxArtAutoKey: tc.value}}}
		if got := boxArtAutoEnabled(cfg); got != tc.want {
			t.Errorf("%s: enabled = %v, want %v", tc.name, got, tc.want)
		}
	}
	builder := &Config{Variables: Variables{Custom: map[string]interface{}{"ArtAutoFetch": "false"}}}
	if boxArtAutoEnabled(builder) {
		t.Error("the web builder's capitalised key was ignored")
	}
}
