package main

// The Playtime screen is the trophy room now: 100 trophies and a platinum in a
// scrolling grid with a chip row above it. These tests pin that the set is
// complete and honest, that every trophy can be reached with the pad and the
// keys, that a fresh visit starts at the top, and that the top bar still gets
// the Up at the top of the page.

import (
	"strings"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// nowMinusDays is a snapshot day key n days before today.
func nowMinusDays(n int) string {
	return time.Now().AddDate(0, 0, -n).Format(wrappedDayKey)
}

// withTrophyRoom gives a test the wide canvas, a fresh room, a snapshot and an
// availability that offers every trophy, and restores them afterwards.
func withTrophyRoom(t *testing.T, data playtimeData) {
	t.Helper()
	withScreen(t, probeW, probeH)
	prevData, prevLoaded := playtime.data, playtime.loaded
	prevScroll, prevFor := playtimeScroll, playtimeScrollFor
	t.Cleanup(func() {
		playtime.data, playtime.loaded = prevData, prevLoaded
		playtime.rev++
		trophyRoom = trophyRoomState{}
		playtimeScroll, playtimeScrollFor = prevScroll, prevFor
		trophyAvail.known = false
		trophyAvail.cfg = nil
	})
	playtime.data = data
	playtime.loaded = true
	playtime.rev++
	trophyRoom = trophyRoomState{}
	playtimeScroll, playtimeScrollFor = 0, -2
	trophyAvail.known = false
	trophyAvail.cfg = nil
}

// roomKey sends one key press to the room's module.
func roomKey(sym sdl.Keycode) bool {
	return playtimeScene{}.handleKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}, &Config{})
}

// roomPad sends one controller press to the room's module.
func roomPad(button uint8) bool {
	return playtimeScene{}.handleController(&sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: button}, &Config{})
}

func TestPlaytimeHasAHundredTrophiesAndAPlatinum(t *testing.T) {
	if got := len(playtimeAchievements); got != playtimeAchievementCount {
		t.Fatalf("the set has %d trophies, want %d", got, playtimeAchievementCount)
	}
	if playtimeAchievementCount != 101 {
		t.Fatalf("the published count is %d, want the 100 trophies plus the platinum", playtimeAchievementCount)
	}

	seenID := map[string]bool{}
	seenIcon := map[string]bool{}
	seenTitle := map[string]bool{}
	platinums := 0
	for i, a := range playtimeAchievements {
		if a.ID == "" || a.Title == "" || a.Desc == "" || a.Group == "" {
			t.Errorf("trophy %d is incomplete: %+v", i, a)
		}
		if a.Check == nil {
			t.Errorf("trophy %q has no check, so it can never unlock", a.ID)
		}
		if a.Metric == trophyMetricScene && a.Scene == "" {
			t.Errorf("screen trophy %q names no screen", a.ID)
		}
		if a.Metric != trophyMetricPlatinum && a.Goal <= 0 {
			t.Errorf("trophy %q has no goal", a.ID)
		}
		if seenID[a.ID] {
			t.Errorf("trophy id %q appears twice", a.ID)
		}
		seenID[a.ID] = true
		if a.Icon == "" || seenIcon[a.Icon] {
			t.Errorf("trophy %q has number %q, which is empty or repeated", a.ID, a.Icon)
		}
		seenIcon[a.Icon] = true
		if seenTitle[strings.ToLower(a.Title)] {
			t.Errorf("trophy title %q appears twice", a.Title)
		}
		seenTitle[strings.ToLower(a.Title)] = true
		if a.Tier == trophyPlatinum {
			platinums++
			if i != len(playtimeAchievements)-1 || a.Metric != trophyMetricPlatinum {
				t.Errorf("the platinum %q must be the last trophy and count the others", a.ID)
			}
		}
	}
	if platinums != 1 {
		t.Fatalf("the set has %d platinum trophies, want exactly one", platinums)
	}
}

// TestPlaytimeAchievementChecksAreHonest runs every check against an empty
// snapshot and a full one. An empty snapshot must unlock nothing (so a fresh
// install does not open on 40 trophies), and no check may panic on the nil maps
// an old save can carry.
func TestPlaytimeAchievementChecksAreHonest(t *testing.T) {
	empty := playtimeData{}
	for _, a := range playtimeAchievements {
		if a.Check(empty) {
			t.Errorf("trophy %q unlocks on an empty snapshot", a.ID)
		}
	}

	// Fill everything: 200 hours in three screens and a solid quarter of days.
	// The trophies that count trophies (and the platinum) are excluded: they
	// read the unlock map, which this snapshot has not filled.
	full := newPlaytimeData()
	full.Total = 2000 * 3600
	for _, scene := range []string{"Games", "MusicPlayer", "Tube"} {
		full.Scenes[scene] = 200 * 3600
	}
	for i := 0; i < 90; i++ {
		full.Days[nowMinusDays(i)] = 4 * 3600
	}
	unlocked := 0
	for _, a := range playtimeAchievements {
		if strings.HasPrefix(a.ID, "badges_") || a.Metric == trophyMetricPlatinum {
			continue
		}
		if a.Check(full) {
			unlocked++
		}
	}
	// The screen trophies for screens the fixture never used are the only ones
	// that may stay locked; everything else describes data the fixture has.
	if unlocked < 40 {
		t.Fatalf("a full snapshot unlocked only %d trophies; the checks are too strict to be reachable", unlocked)
	}
}

// TestPlaytimeAchievementsAreAchievedFirst pins the default order: earned
// trophies first, in their definition order, then the rest, also in definition
// order. The number on a trophy must not move with the sort.
func TestPlaytimeAchievementsAreAchievedFirst(t *testing.T) {
	data := newPlaytimeData()
	earned := map[string]bool{"one_hour": true, "gamer": true, "explorer": true}
	for id := range earned {
		data.Unlocked[id] = "2026-10-01T10:00:00Z"
	}

	ordered := playtimeAchievementsOrdered(data)
	if len(ordered) != len(playtimeAchievements) {
		t.Fatalf("ordering returned %d trophies, want %d", len(ordered), len(playtimeAchievements))
	}
	for i, a := range ordered {
		if i < len(earned) && !earned[a.ID] {
			t.Fatalf("locked trophy %q sits at %d, among the earned ones", a.ID, i)
		}
		if i >= len(earned) && earned[a.ID] {
			t.Fatalf("earned trophy %q sits at %d, after the locked ones", a.ID, i)
		}
	}

	defPos := map[string]int{}
	for i, a := range playtimeAchievements {
		defPos[a.ID] = i
	}
	for i := 1; i < len(ordered); i++ {
		if earned[ordered[i-1].ID] != earned[ordered[i].ID] {
			continue
		}
		if defPos[ordered[i].ID] < defPos[ordered[i-1].ID] {
			t.Fatalf("%q moved ahead of %q although both are in the same half", ordered[i].ID, ordered[i-1].ID)
		}
	}
	for _, a := range ordered {
		if want := playtimeAchievements[defPos[a.ID]].Icon; a.Icon != want {
			t.Fatalf("trophy %q shows number %q after sorting, want %q", a.ID, a.Icon, want)
		}
	}
}

// TestTrophyRoomGridReachesTheLastTrophy: the grid is taller than the screen,
// End lands on the last trophy with the grid scrolled to its end, every card
// has an offset that shows it, and Home comes back to the top.
func TestTrophyRoomGridReachesTheLastTrophy(t *testing.T) {
	withTrophyRoom(t, newPlaytimeData())

	if !roomKey(sdl.K_DOWN) || trophyRoom.zone != trophyZoneGrid {
		t.Fatal("Down from the chip row did not enter the grid")
	}
	roomKey(sdl.K_END)
	n := len(trophyRoom.list)
	if n != len(playtimeAchievements) {
		t.Fatalf("the room lists %d trophies, want all %d", n, len(playtimeAchievements))
	}
	if trophyRoom.cursor != n-1 {
		t.Fatalf("End focused %d, want the last trophy %d", trophyRoom.cursor, n-1)
	}
	limit := trophyRoomMaxScroll()
	if limit <= 0 {
		t.Fatal("a hundred trophies fit the viewport: the grid is not scrollable")
	}
	if playtimeScroll != limit {
		t.Fatalf("End left the grid at %d, want the end %d", playtimeScroll, limit)
	}
	cols, cardH, gap, viewH := trophyRoomMetrics()
	for pos := 0; pos < n; pos++ {
		top := int32(pos/cols) * (cardH + gap)
		if top+cardH-viewH > limit {
			t.Fatalf("card %d can never be scrolled fully into view", pos)
		}
	}

	roomKey(sdl.K_HOME)
	if trophyRoom.cursor != 0 || playtimeScroll != 0 {
		t.Fatalf("Home left the focus at %d and the grid at %d", trophyRoom.cursor, playtimeScroll)
	}
}

// TestTrophyRoomRendersTheLastTrophy is the pixel half: scrolled to the end,
// the focused last card is drawn whole inside the grid's viewport.
func TestTrophyRoomRendersTheLastTrophy(t *testing.T) {
	withTrophyRoom(t, newPlaytimeData())
	roomKey(sdl.K_DOWN)
	roomKey(sdl.K_END)

	frame := renderPlaytimeProbe(t, newPlaytimeData())
	last := len(trophyRoom.list) - 1
	found := false
	for k := 0; k < trophyRoom.cardN; k++ {
		if trophyRoom.cardPos[k] != last {
			continue
		}
		found = true
		card := trophyRoom.cardRects[k]
		if card.H < trophyRoom.cardH-1 {
			t.Fatalf("the last card is cut to %dpx of %dpx at the bottom of the grid", card.H, trophyRoom.cardH)
		}
		if got := paintedPixelsIn(frame, card); got < 40 {
			t.Fatalf("the last card painted only %d pixels", got)
		}
	}
	if !found {
		t.Fatalf("the last trophy (%d) was not drawn with the grid at its end", last)
	}
	// The bar is still at the top: the page scrolls under it.
	title := sdl.Rect{X: 0, Y: 0, W: probeW, H: headerBarHeight()}
	if got := paintedPixelsIn(frame, title); got < 100 {
		t.Fatalf("the top bar painted only %d pixels", got)
	}
}

// TestTrophyRoomKeysPadAndWheel: the keys, the pad and the wheel drive the same
// room — the chip row filters as the focus moves, Down enters the grid, Up from
// its first row returns, L1/R1 switch tabs and the wheel scrolls.
func TestTrophyRoomKeysPadAndWheel(t *testing.T) {
	data := newPlaytimeData()
	data.Unlocked["first_steps"] = "2026-03-02T20:11:00Z"
	withTrophyRoom(t, data)

	if !roomKey(sdl.K_RIGHT) || trophyRoom.filter != trophyFilterEarned {
		t.Fatalf("Right on the chip row left the filter at %d, want Earned", trophyRoom.filter)
	}
	if len(trophyRoom.list) != 1 || playtimeAchievements[trophyRoom.list[0]].ID != "first_steps" {
		t.Fatalf("the Earned filter lists %v", trophyRoom.list)
	}
	roomKey(sdl.K_LEFT)
	if trophyRoom.filter != trophyFilterAll {
		t.Fatalf("Left did not return to All (filter %d)", trophyRoom.filter)
	}

	cols, _, _, _ := trophyRoomMetrics()
	roomPad(sdl.CONTROLLER_BUTTON_DPAD_DOWN)
	roomPad(sdl.CONTROLLER_BUTTON_DPAD_RIGHT)
	roomPad(sdl.CONTROLLER_BUTTON_DPAD_DOWN)
	if trophyRoom.cursor != 1+cols {
		t.Fatalf("Down, Right, Down focused %d, want %d", trophyRoom.cursor, 1+cols)
	}
	roomPad(sdl.CONTROLLER_BUTTON_DPAD_UP)
	roomPad(sdl.CONTROLLER_BUTTON_DPAD_UP)
	if trophyRoom.zone != trophyZoneChips {
		t.Fatal("Up from the grid's first row did not return to the chip row")
	}
	if roomPad(sdl.CONTROLLER_BUTTON_DPAD_UP) {
		t.Fatal("Up at the top of the page was not handed to the top bar")
	}

	roomPad(sdl.CONTROLLER_BUTTON_RIGHTSHOULDER)
	if trophyRoom.tab != trophyTabStats {
		t.Fatal("R1 did not open the Playtime tab")
	}
	if roomKey(sdl.K_UP) {
		t.Fatal("Up on the Playtime tab must reach the top bar")
	}
	roomKey(sdl.K_TAB)
	if trophyRoom.tab != trophyTabTrophies {
		t.Fatal("Tab did not return to the Trophies tab")
	}

	before := trophyRoom.sort
	roomPad(sdl.CONTROLLER_BUTTON_X)
	if trophyRoom.sort == before {
		t.Fatal("X did not change the sort")
	}

	if handlePlaytimeWheel(0) {
		t.Fatal("a zero wheel delta must not be consumed")
	}
	playtimeScroll = 0
	handlePlaytimeWheel(2)
	if playtimeScroll != 0 {
		t.Fatalf("wheel up at the top moved the grid to %d", playtimeScroll)
	}
	handlePlaytimeWheel(-3)
	if playtimeScroll <= 0 {
		t.Fatalf("wheel down did not move the grid (scroll %d)", playtimeScroll)
	}
}

// TestTrophyRoomDetailsSheet: A opens the focused trophy, Left/Right step
// through the list, B closes it and leaves the room's focus where it was.
func TestTrophyRoomDetailsSheet(t *testing.T) {
	withTrophyRoom(t, newPlaytimeData())
	roomKey(sdl.K_DOWN)
	roomKey(sdl.K_RIGHT)
	roomKey(sdl.K_RETURN)
	if !trophyRoom.sheet {
		t.Fatal("A on a card did not open its details")
	}
	if trophyRoom.sheetIdx != trophyRoom.list[1] {
		t.Fatalf("the sheet shows trophy %d, want the focused %d", trophyRoom.sheetIdx, trophyRoom.list[1])
	}
	roomKey(sdl.K_RIGHT)
	if trophyRoom.sheetIdx != trophyRoom.list[2] || trophyRoom.cursor != 2 {
		t.Fatal("Right in the sheet did not step to the next trophy")
	}
	if !roomKey(sdl.K_ESCAPE) || trophyRoom.sheet {
		t.Fatal("B did not close the sheet")
	}
	if trophyRoom.zone != trophyZoneGrid || trophyRoom.cursor != 2 {
		t.Fatal("closing the sheet lost the grid focus")
	}
}

// TestTrophyRoomEntersAtTheTop: a second visit starts fresh, a re-render of the
// same visit keeps the state.
func TestTrophyRoomEntersAtTheTop(t *testing.T) {
	withTrophyRoom(t, newPlaytimeData())
	prevIdx := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevIdx })
	currentSceneIndex = 5

	trophyRoomSyncVisit()
	trophyRoom.zone, trophyRoom.cursor, trophyRoom.tab = trophyZoneGrid, 7, trophyTabStats
	playtimeScroll = 300
	trophyRoomSyncVisit()
	if trophyRoom.cursor != 7 || playtimeScroll != 300 || trophyRoom.tab != trophyTabStats {
		t.Fatal("the same visit lost its place")
	}

	playtimeScrollFor = currentSceneIndex + 1
	trophyRoomSyncVisit()
	if trophyRoom.zone != trophyZoneChips || trophyRoom.cursor != 0 || playtimeScroll != 0 || trophyRoom.tab != trophyTabTrophies {
		t.Fatalf("re-entering kept zone %d, cursor %d, scroll %d, tab %d",
			trophyRoom.zone, trophyRoom.cursor, playtimeScroll, trophyRoom.tab)
	}
}

// TestPlaytimeUnlockLabel pins the date wording and, more importantly, that a
// save whose timestamp cannot be read gets no label at all instead of one
// built from a zero time.
func TestPlaytimeUnlockLabel(t *testing.T) {
	cases := map[string]string{
		"2026-03-02T20:11:00Z": "Unlocked 2 Mar 2026",
		"2026-12-24T23:59:00Z": "Unlocked 24 Dec 2026",
		"":                     "",
		"2026-03-02":           "",
		"yesterday":            "",
	}
	for stamp, want := range cases {
		if got := playtimeUnlockLabel(stamp); got != want {
			t.Errorf("playtimeUnlockLabel(%q) = %q, want %q", stamp, got, want)
		}
	}
}

// TestEarnedTrophyShowsItsMetal: an earned card carries the cup in its tier's
// metal and the date it was earned; a locked card is a grey silhouette.
func TestEarnedTrophyShowsItsMetal(t *testing.T) {
	withTrophyRoom(t, newPlaytimeData())
	data := newPlaytimeData()
	data.Unlocked["first_steps"] = "2026-03-02T20:11:00Z"
	frame := renderPlaytimeProbe(t, data)

	idx := trophyIndexByID("first_steps")
	if got := trophyRoom.rows[idx].when; got != "Earned 2 Mar 2026" {
		t.Fatalf("the earned card's date line is %q", got)
	}
	medal := func(pos int) sdl.Rect {
		for k := 0; k < trophyRoom.cardN; k++ {
			if trophyRoom.cardPos[k] == pos {
				c := trophyRoom.cardRects[k]
				return sdl.Rect{X: c.X + 12, Y: c.Y + 8, W: 62, H: c.H - 16}
			}
		}
		t.Fatalf("card %d was not drawn", pos)
		return sdl.Rect{}
	}
	if trophyRoom.list[0] != idx {
		t.Fatalf("the earned trophy is not first (list starts with %d)", trophyRoom.list[0])
	}
	if got := bronzePixelsIn(frame, medal(0)); got < 60 {
		t.Fatalf("the earned bronze trophy painted only %d bronze pixels", got)
	}
	if got := bronzePixelsIn(frame, medal(1)); got > 0 {
		t.Fatalf("a locked trophy painted %d bronze pixels; it must be a grey silhouette", got)
	}
}

// bronzePixelsIn counts warm metal pixels: red well above blue, green between.
func bronzePixelsIn(f *probeFrame, r sdl.Rect) int {
	n := 0
	for y := r.Y; y < r.Y+r.H && y < probeH; y++ {
		for x := r.X; x < r.X+r.W && x < probeW; x++ {
			if x < 0 || y < 0 {
				continue
			}
			c := f.at(x, y)
			if int(c.R) > 110 && int(c.R)-int(c.B) > 70 && c.G < c.R && c.G > c.B {
				n++
			}
		}
	}
	return n
}

// TestPlaytimePageKeepsThePadFromTheTopBar pins the routing: the Playtime
// screen has no config elements, so the top bar's buttons are the only nodes in
// its focus graph. The focus must not start on them, Down must reach the room
// (into its grid), and Up at the top of the room must reach the bar.
func TestPlaytimePageKeepsThePadFromTheTopBar(t *testing.T) {
	withTrophyRoom(t, newPlaytimeData())
	useTopChrome(t)

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	ensurePlaytimeScene(config)
	idx := sceneIndexExact(config, playtimeSceneName)
	if idx < 0 {
		t.Fatal("the Playtime scene was not injected")
	}
	prevIdx, prevSel := currentSceneIndex, selectedButtonIndex
	t.Cleanup(func() { currentSceneIndex, selectedButtonIndex = prevIdx, prevSel })
	currentSceneIndex = idx

	focusEngine.Clear()
	focusEngine.SetGraph(playtimeSceneName, focusEngine.BuildGraph(config.Scenes[idx]))
	focusEngine.SyncSelected()
	if got := focusEngine.ElementIndex(); got != -1 {
		t.Fatalf("focus starts on %d; a page with no elements must not start on the top bar", got)
	}

	down := &sdl.KeyboardEvent{Type: sdl.KEYDOWN}
	down.Keysym.Sym = sdl.K_DOWN
	if handleChromeKey(down, config) {
		t.Fatal("the top bar consumed Down on the Playtime page")
	}
	m, ok := lookupScene(playtimeSceneName)
	if !ok || !m.handleKey(down, config) {
		t.Fatal("the room did not take Down")
	}
	if trophyRoom.zone != trophyZoneGrid {
		t.Fatal("Down did not move the focus into the trophy grid")
	}
	up := &sdl.KeyboardEvent{Type: sdl.KEYDOWN}
	up.Keysym.Sym = sdl.K_UP
	if !m.handleKey(up, config) || trophyRoom.zone != trophyZoneChips {
		t.Fatal("Up from the grid's first row did not return to the chip row")
	}
	if m.handleKey(up, config) {
		t.Fatal("Up at the top of the room was swallowed instead of reaching the top bar")
	}
}

// paintedPixelsIn counts pixels clearly brighter than the screen background.
func paintedPixelsIn(f *probeFrame, r sdl.Rect) int {
	n := 0
	for y := r.Y; y < r.Y+r.H && y < probeH; y++ {
		for x := r.X; x < r.X+r.W && x < probeW; x++ {
			if x < 0 || y < 0 {
				continue
			}
			c := f.at(x, y)
			if int(c.R)+int(c.G)+int(c.B) > 150 {
				n++
			}
		}
	}
	return n
}
