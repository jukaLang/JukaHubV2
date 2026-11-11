package main

// Tests for reaching the trophy room: its place in Start → Screens, the count
// beside it, the Apps list entry, and the tap on a celebration card.

import (
	"strconv"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// trophyAccessConfig is a small config with Home, two other screens and the
// trophy room injected the way startup does it.
func trophyAccessConfig() *Config {
	config := &Config{Scenes: []SceneConfig{
		{Name: "Main", Icon: "H"},
		{Name: "Tube", Icon: "M"},
		{Name: "Settings", Icon: "S"},
	}}
	ensurePlaytimeScene(config)
	return config
}

// keepTrophyState restores the playtime snapshot, the availability and the
// summary cache a test replaces, and starts the test with an empty cache so a
// count left by another test can never be returned.
func keepTrophyState(t *testing.T) {
	t.Helper()
	prevPlaytime, prevAvail, prevCache := playtime, trophyAvail, trophySummaryCache
	t.Cleanup(func() {
		playtime, trophyAvail, trophySummaryCache = prevPlaytime, prevAvail, prevCache
	})
	trophySummaryCache.valid = false
}

func TestStartMenuListsTrophiesRightAfterHome(t *testing.T) {
	if pins := xpackPinnedScenes(); len(pins) > 0 {
		t.Skipf("an experience pack pins %v first", pins)
	}
	keepTrophyState(t)
	config := trophyAccessConfig()
	entries := startMenuSceneEntries(config)
	if len(entries) < 2 {
		t.Fatalf("Screens = %+v", entries)
	}
	if entries[0].scene != "Main" || entries[1].scene != playtimeSceneName {
		t.Fatalf("Screens starts %q, %q; want Main, then the trophy room", entries[0].scene, entries[1].scene)
	}
	if entries[1].name != "Trophies" {
		t.Fatalf("the trophy room's row is named %q", entries[1].name)
	}
	// Counted over the groups themselves: getStartMenuApps drops repeated
	// names, so a duplicate would never show there.
	rows := 0
	for _, group := range startMenuGroups(config) {
		for _, entry := range group.items {
			if entry.scene == playtimeSceneName || entry.name == "Trophies" {
				rows++
			}
		}
	}
	if rows != 1 {
		t.Fatalf("the Start menu lists the trophy room %d times, want once", rows)
	}
}

func TestStartMenuTrophyBadgeIsOnlyDrawn(t *testing.T) {
	keepTrophyState(t)
	playtime = playtimeState{loaded: true, data: newPlaytimeData(), rev: playtime.rev + 1}
	playtime.data.Unlocked[playtimeAchievements[0].ID] = "2026-11-10T12:00:00Z"
	earned, shown := trophyCount(playtime.data)
	want := strconv.Itoa(earned) + "/" + strconv.Itoa(shown)
	for _, entry := range startMenuSceneEntries(trophyAccessConfig()) {
		switch entry.scene {
		case playtimeSceneName:
			if entry.badge != want || entry.name != "Trophies" {
				t.Fatalf("trophy row = name %q badge %q, want Trophies / %q", entry.name, entry.badge, want)
			}
		default:
			if entry.badge != "" {
				t.Errorf("row %q has badge %q", entry.name, entry.badge)
			}
		}
	}
}

func TestTrophySummaryCountsLikeTheRoomAndCaches(t *testing.T) {
	keepTrophyState(t)
	n := len(playtimeAchievements)
	if n < 3 {
		t.Skip("the trophy set is too small for this test")
	}
	ok := make([]bool, n)
	for i := range ok {
		ok[i] = i%2 == 0
	}
	trophyAvail.known, trophyAvail.ok = true, ok
	trophyAvail.gen++

	d := newPlaytimeData()
	d.Unlocked[playtimeAchievements[0].ID] = "2026-11-10T12:00:00Z" // available and earned
	d.Unlocked[playtimeAchievements[1].ID] = "2026-11-10T12:00:00Z" // earned before it stopped being available
	d.Unlocked["not-a-trophy"] = "2026-11-10T12:00:00Z"             // an old id: counts for nothing
	playtime = playtimeState{loaded: true, data: d, rev: playtime.rev + 1}

	// Every available trophy is shown, and an earned one always is.
	wantShown, wantEarned := (n+1)/2+1, 2
	if earned, shown := trophySummary(); earned != wantEarned || shown != wantShown {
		t.Fatalf("summary = %d/%d, want %d/%d", earned, shown, wantEarned, wantShown)
	}
	if got := trophySummaryBadge(); got != strconv.Itoa(wantEarned)+"/"+strconv.Itoa(wantShown) {
		t.Fatalf("badge = %q", got)
	}

	// Nothing changed: the cached count is returned, not a new one.
	trophySummaryCache.earned = -7
	if earned, _ := trophySummary(); earned != -7 {
		t.Fatalf("an unchanged snapshot was counted again (earned %d)", earned)
	}
	// The snapshot moved on: the count is made again.
	playtime.rev++
	if earned, _ := trophySummary(); earned != wantEarned {
		t.Fatalf("a changed snapshot kept the cached count (earned %d)", earned)
	}
	// A new unlock is seen even if rev were to match an older count.
	d.Unlocked[playtimeAchievements[2].ID] = "2026-11-10T12:00:00Z"
	if earned, _ := trophySummary(); earned != wantEarned+1 {
		t.Fatalf("a new unlock was not counted (earned %d)", earned)
	}
}

func TestPlaytimeInitKeepsTheRevisionMoving(t *testing.T) {
	keepTrophyState(t)
	playtime.rev = 41
	// A backup restore reloads the snapshot mid-session: a counter starting
	// again could match a count cached from the old snapshot.
	playtimeInit(&Config{})
	if playtime.rev <= 41 {
		t.Fatalf("rev after a reload = %d, want past 41", playtime.rev)
	}
}

func TestTrophySummaryBadgeEmptyWithNothingShown(t *testing.T) {
	keepTrophyState(t)
	trophyAvail.known, trophyAvail.ok = true, make([]bool, len(playtimeAchievements))
	trophyAvail.gen++
	playtime = playtimeState{loaded: true, data: newPlaytimeData(), rev: playtime.rev + 1}
	if got := trophySummaryBadge(); got != "" {
		t.Fatalf("badge with nothing to show = %q", got)
	}
}

func TestAppsListOffersTrophies(t *testing.T) {
	want := builtinScenePrefix + playtimeSceneName
	has := func(entries []FileEntry) bool {
		for _, e := range entries {
			if e.Path == want && e.Name == "Trophies (built-in)" {
				return true
			}
		}
		return false
	}
	if !has(builtinAppEntries(trophyAccessConfig())) {
		t.Fatal("the Apps list has no Trophies row on a config with the trophy room")
	}
	bare := &Config{Scenes: []SceneConfig{{Name: "Main", Icon: "H"}}}
	if has(builtinAppEntries(bare)) {
		t.Fatal("the Apps list offers Trophies on a config without the trophy room")
	}
}

func TestTrophyCardClick(t *testing.T) {
	if reason := modalPanelBlockReason(); reason != "" {
		t.Skipf("%s", reason)
	}
	if playerPhase() != "idle" {
		t.Skip("a video is open")
	}
	prevShow, prevScene := trophyShow, currentSceneIndex
	t.Cleanup(func() { trophyShow, currentSceneIndex = prevShow, prevScene })
	config := trophyAccessConfig()
	room := sceneIndexExact(config, playtimeSceneName)
	if room < 0 {
		t.Fatal("the trophy room was not injected")
	}

	card := sdl.Rect{X: 800, Y: 60, W: 420, H: 96}
	trophyShow.active, trophyShow.card = true, card
	if trophyCardClick(10, 10, config) || !trophyShow.active {
		t.Fatal("a click off the card was taken")
	}
	trophyShow.active = false
	if trophyCardClick(card.X+5, card.Y+5, config) {
		t.Fatal("a click where a finished card was drawn was taken")
	}

	// In the room already, the tap puts the card away and stays.
	currentSceneIndex = room
	trophyShow.active, trophyShow.card = true, card
	if !trophyCardClick(card.X+5, card.Y+5, config) {
		t.Fatal("a tap on the card was not taken")
	}
	if trophyShow.active || trophyShow.card.W != 0 || currentSceneIndex != room {
		t.Fatalf("after the tap: active %v, card %+v, scene %d", trophyShow.active, trophyShow.card, currentSceneIndex)
	}

	// Over something that owns the screen the tap only puts the card away:
	// opening the room would tear Quick Settings down with the scene change.
	prevQS := qs.open
	t.Cleanup(func() { qs.open = prevQS })
	home := sceneIndexExact(config, "Main")
	currentSceneIndex = home
	qs.open = true
	trophyShow.active, trophyShow.card = true, card
	if !trophyCardClick(card.X+5, card.Y+5, config) {
		t.Fatal("a tap on the card over Quick Settings was not taken")
	}
	if trophyShow.active || currentSceneIndex != home || !qs.open {
		t.Fatalf("over Quick Settings: active %v, scene %d (want %d), Quick Settings open %v",
			trophyShow.active, currentSceneIndex, home, qs.open)
	}
}
