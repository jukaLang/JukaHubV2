package main

// The Playtime screen is a 100-badge page now, so two things need pinning that
// the six-badge screen never could: that the list really is 100 complete badges,
// and that every one of them is reachable by scrolling. The ordering rule —
// earned badges first — is checked here too, because it decides what a player
// sees before they touch anything.

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

// withPlaytimeViewport gives a test the wide canvas and restores the scroll
// state afterwards, so a scroll test cannot move the frame a later render test
// measures.
func withPlaytimeViewport(t *testing.T, w, h int32) {
	t.Helper()
	withScreen(t, w, h)
	prevScroll, prevFor := playtimeScroll, playtimeScrollFor
	playtimeScroll, playtimeScrollFor = 0, currentSceneIndex
	t.Cleanup(func() { playtimeScroll, playtimeScrollFor = prevScroll, prevFor })
}

func TestPlaytimeHasOneHundredAchievements(t *testing.T) {
	if got := len(playtimeAchievements); got != playtimeAchievementCount {
		t.Fatalf("badge set has %d achievements, want %d", got, playtimeAchievementCount)
	}
	if playtimeAchievementCount != 100 {
		t.Fatalf("the published count is %d, want 100", playtimeAchievementCount)
	}

	seenID := map[string]bool{}
	seenIcon := map[string]bool{}
	seenTitle := map[string]bool{}
	for i, a := range playtimeAchievements {
		if a.ID == "" || a.Title == "" || a.Desc == "" {
			t.Errorf("badge %d is incomplete: %+v", i, a)
		}
		if a.Check == nil {
			t.Errorf("badge %q has no check, so it can never unlock", a.ID)
		}
		if seenID[a.ID] {
			t.Errorf("badge id %q appears twice", a.ID)
		}
		seenID[a.ID] = true
		if a.Icon == "" || seenIcon[a.Icon] {
			t.Errorf("badge %q has icon %q, which is empty or repeated", a.ID, a.Icon)
		}
		seenIcon[a.Icon] = true
		if seenTitle[strings.ToLower(a.Title)] {
			t.Errorf("badge title %q appears twice", a.Title)
		}
		seenTitle[strings.ToLower(a.Title)] = true
	}
}

// TestPlaytimeAchievementChecksAreHonest runs every check against an empty
// snapshot and a full one. An empty snapshot must unlock nothing (so a fresh
// install does not open on 40 badges), and no check may panic on the nil maps an
// old save can carry.
func TestPlaytimeAchievementChecksAreHonest(t *testing.T) {
	empty := playtimeData{}
	for _, a := range playtimeAchievements {
		if a.Check(empty) {
			t.Errorf("badge %q unlocks on an empty snapshot", a.ID)
		}
	}

	// Fill everything: 40 hours in every screen, a solid month of days, and the
	// badge that counts badges is excluded from the expectation because it reads
	// the unlock map, which this snapshot has not filled.
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
		if strings.HasPrefix(a.ID, "badges_") {
			continue
		}
		if a.Check(full) {
			unlocked++
		}
	}
	// The scene badges for screens the fixture never used are the only ones that
	// may stay locked; everything else describes data the fixture has.
	if unlocked < 40 {
		t.Fatalf("a full snapshot unlocked only %d badges; the checks are too strict to be reachable", unlocked)
	}
}

// TestPlaytimeAchievementsAreAchievedFirst pins the display order: earned badges
// first, in their definition order, then the rest, also in definition order. The
// icon number must not move with the sort — it is the badge's identity.
func TestPlaytimeAchievementsAreAchievedFirst(t *testing.T) {
	data := newPlaytimeData()
	earned := map[string]bool{"one_hour": true, "gamer": true, "explorer": true}
	for id := range earned {
		data.Unlocked[id] = "2026-10-01T10:00:00Z"
	}

	ordered := playtimeAchievementsOrdered(data)
	if len(ordered) != len(playtimeAchievements) {
		t.Fatalf("ordering returned %d badges, want %d", len(ordered), len(playtimeAchievements))
	}

	// Find the first locked badge; nothing earned may come after it.
	firstLocked := -1
	for i, a := range ordered {
		_, done := earned[a.ID]
		if !done {
			firstLocked = i
			break
		}
		if firstLocked >= 0 {
			t.Fatalf("earned badge %q sits at %d, after the locked badge at %d", a.ID, i, firstLocked)
		}
	}
	if firstLocked != len(earned) {
		t.Fatalf("earned badges end at %d, want %d (one per unlocked badge)", firstLocked, len(earned))
	}

	// Within each half the definition order holds.
	defPos := map[string]int{}
	for i, a := range playtimeAchievements {
		defPos[a.ID] = i
	}
	for i := 1; i < len(ordered); i++ {
		prevDone := earned[ordered[i-1].ID]
		curDone := earned[ordered[i].ID]
		if prevDone != curDone {
			continue
		}
		if defPos[ordered[i].ID] < defPos[ordered[i-1].ID] {
			t.Fatalf("%q moved ahead of %q although both are in the same half",
				ordered[i].ID, ordered[i-1].ID)
		}
	}

	// The icon is the badge's number in the fixed set, whatever its row.
	for _, a := range ordered {
		want := playtimeAchievements[defPos[a.ID]].Icon
		if a.Icon != want {
			t.Fatalf("badge %q shows icon %q after sorting, want %q", a.ID, a.Icon, want)
		}
	}
}

// TestPlaytimePageScrollsToTheLastBadge: the page must be taller than the
// screen, scroll in bounded steps, and put the final badge fully on screen at
// the bottom of the range. This is the check that would have caught the old
// "break once the card is full" behaviour, where 94 badges were simply never
// drawn.
func TestPlaytimePageScrollsToTheLastBadge(t *testing.T) {
	withPlaytimeViewport(t, probeW, probeH)

	viewport := playtimeViewportHeight()
	if viewport <= 0 {
		t.Fatalf("viewport height = %d", viewport)
	}
	if playtimeContentHeight() <= viewport {
		t.Fatalf("100 badges fit a %dpx viewport (content %dpx): the page is not scrollable",
			viewport, playtimeContentHeight())
	}
	max := playtimeMaxScroll()
	if max <= 0 {
		t.Fatalf("max scroll = %d with content %d and viewport %d", max, playtimeContentHeight(), viewport)
	}

	// Up at the top and Down at the bottom are both no-ops.
	playtimeScroll = 0
	playtimeScrollBy(-playtimeScrollStep)
	if playtimeScroll != 0 {
		t.Fatalf("scrolling up from the top left the page at %d", playtimeScroll)
	}
	playtimeScroll = max
	playtimeScrollBy(playtimeScrollStep)
	if playtimeScroll != max {
		t.Fatalf("scrolling past the bottom left the page at %d, want %d", playtimeScroll, max)
	}

	// The last badge is visible at the bottom of the range and not invisible at
	// the top of it (the two ends must differ, or scrolling is pointless).
	last := playtimeBadgeCell(len(playtimeAchievements)-1, probeW-56, playtimeAchY)
	viewportBottom := screenHeight - 8
	if bottom := last.Y + last.H - max; bottom > viewportBottom {
		t.Fatalf("last badge ends at y=%d when fully scrolled, past the viewport at %d", bottom, viewportBottom)
	}
	if top := last.Y - max; top < playtimePageY {
		t.Fatalf("last badge starts at y=%d when fully scrolled, above the page top %d", top, playtimePageY)
	}
	if last.Y < viewportBottom {
		t.Fatalf("last badge is already on screen unscrolled (y=%d): the layout is too short", last.Y)
	}

	// Every badge has a scroll position that shows it: walk the list and require
	// each cell to be reachable inside the viewport window.
	for i := range playtimeAchievements {
		cell := playtimeBadgeCell(i, probeW-56, playtimeAchY)
		lowest := cell.Y + cell.H - viewportBottom
		if lowest < 0 {
			lowest = 0
		}
		highest := cell.Y - playtimePageY
		if highest > max {
			highest = max
		}
		if lowest > highest {
			t.Fatalf("badge %d (cell %+v) has no scroll offset that shows it", i, cell)
		}
	}
}

// TestPlaytimeWheelAndKeysMoveThePage: the wheel, the D-pad and the page keys all
// drive the same clamp.
func TestPlaytimeWheelAndKeysMoveThePage(t *testing.T) {
	withPlaytimeViewport(t, probeW, probeH)
	playtimeScroll = 0

	if handlePlaytimeWheel(0) {
		t.Fatal("a zero wheel delta must not be consumed")
	}
	// SDL reports a positive wheel delta when the wheel turns away from the
	// user, which is "scroll up" here.
	if !handlePlaytimeWheel(3) {
		t.Fatal("a wheel-up event was not consumed")
	}
	if playtimeScroll != 0 {
		t.Fatalf("wheel up at the top moved the page to %d", playtimeScroll)
	}
	handlePlaytimeWheel(-3)
	if playtimeScroll <= 0 {
		t.Fatalf("wheel down did not move the page (scroll=%d)", playtimeScroll)
	}
	before := playtimeScroll
	handlePlaytimeWheel(1)
	if playtimeScroll >= before {
		t.Fatalf("wheel up did not move the page back (%d -> %d)", before, playtimeScroll)
	}

	// End jumps to the bottom, Home returns to the top.
	playtimeScene{}.handleKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_END}}, &Config{})
	if playtimeScroll != playtimeMaxScroll() {
		t.Fatalf("End left the page at %d, want %d", playtimeScroll, playtimeMaxScroll())
	}
	playtimeScene{}.handleKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_HOME}}, &Config{})
	if playtimeScroll != 0 {
		t.Fatalf("Home left the page at %d", playtimeScroll)
	}

	// The pad's Down moves it too, and its Up comes back.
	playtimeScene{}.handleController(&sdl.ControllerButtonEvent{
		Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_DOWN,
	}, &Config{})
	if playtimeScroll <= 0 {
		t.Fatalf("pad Down did not scroll (scroll=%d)", playtimeScroll)
	}
	playtimeScene{}.handleController(&sdl.ControllerButtonEvent{
		Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_UP,
	}, &Config{})
	if playtimeScroll != 0 {
		t.Fatalf("pad Up did not return to the top (scroll=%d)", playtimeScroll)
	}
}

// TestPlaytimeRendersTheBottomOfThePage is the pixel half: after scrolling to
// the end, the last badge's cells paint something, and the pinned title band is
// still there. Without this the geometry could be right and the list still blank.
func TestPlaytimeRendersTheBottomOfThePage(t *testing.T) {
	withPlaytimeViewport(t, probeW, probeH)
	prevAccent := accentColor
	SetAccentColor(ColorAccent)
	t.Cleanup(func() { SetAccentColor(prevAccent) })

	max := playtimeMaxScroll()
	if max <= 0 {
		t.Fatalf("max scroll = %d; nothing to scroll", max)
	}

	// Land at the bottom first; the render clamps to the same value.
	playtimeScroll = max
	frame := renderPlaytimeProbe(t, newPlaytimeData())
	if playtimeScroll != max {
		t.Fatalf("render clamped the scroll to %d, want %d", playtimeScroll, max)
	}

	// The last badge row is on screen and painted.
	last := playtimeBadgeCell(len(playtimeAchievements)-1, probeW-56, playtimeAchY)
	cell := sdl.Rect{X: last.X, Y: last.Y - playtimeScroll, W: last.W, H: last.H}
	if cell.Y < playtimePageY || cell.Y+cell.H > screenHeight-8 {
		t.Fatalf("last badge cell %+v is not inside the viewport", cell)
	}
	if got := paintedPixelsIn(frame, cell); got < 40 {
		t.Fatalf("the last badge row painted only %d pixels at the bottom of the page", got)
	}

	// The title band is pinned: it still has ink while the page is at its end.
	title := sdl.Rect{X: 0, Y: 0, W: probeW, H: playtimePageY}
	if got := paintedPixelsIn(frame, title); got < 100 {
		t.Fatalf("the pinned title band painted only %d pixels", got)
	}

	// And the scrollbar thumb sits near the bottom of its track.
	if playtimeScrollbarRect.W == 0 {
		t.Fatal("a scrollable page drew no scrollbar")
	}
	if playtimeScrollbarRect.Y < screenHeight/2 {
		t.Fatalf("scrollbar thumb at y=%d did not follow the page to the bottom", playtimeScrollbarRect.Y)
	}
}

// TestPlaytimeEntersAtTheTop: a second visit starts at the top rather than where
// the last one stopped, and a fresh render of the same visit keeps the offset.
func TestPlaytimeEntersAtTheTop(t *testing.T) {
	withPlaytimeViewport(t, probeW, probeH)
	renderPlaytimeProbe(t, newPlaytimeData())
	if playtimeScroll != 0 {
		t.Fatalf("a fresh visit started at %d", playtimeScroll)
	}

	// Same scene, scrolled: a re-render must keep the offset.
	playtimeScroll = 200
	renderPlaytimeProbe(t, newPlaytimeData())
	if playtimeScroll != 200 {
		t.Fatalf("re-render moved the page to %d, want 200", playtimeScroll)
	}

	// A different scene index means the screen was left and entered again.
	playtimeScroll = 400
	playtimeScrollFor = currentSceneIndex + 1
	renderPlaytimeProbe(t, newPlaytimeData())
	if playtimeScroll != 0 {
		t.Fatalf("re-entering the screen kept the offset at %d", playtimeScroll)
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

// TestEarnedBadgesShowTheirUnlockDate is the pixel half: the third line of an
// earned badge carries the date, and a locked badge's row leaves that line
// empty. Without the second half a layout bug could paint the date on every
// row, including badges that have never been earned.
func TestEarnedBadgesShowTheirUnlockDate(t *testing.T) {
	withPlaytimeViewport(t, probeW, probeH)
	prevAccent := accentColor
	SetAccentColor(ColorAccent)
	t.Cleanup(func() { SetAccentColor(prevAccent) })

	data := newPlaytimeData()
	first := playtimeAchievements[0]
	data.Unlocked[first.ID] = "2026-03-02T20:11:00Z"

	playtimeScroll = playtimeAchY - playtimePageY
	playtimeScrollFor = currentSceneIndex
	frame := renderPlaytimeProbe(t, data)

	cardW := probeW - 56
	dateBand := func(i int) sdl.Rect {
		cell := playtimeBadgeCell(i, cardW, playtimeAchY)
		return sdl.Rect{
			X: cell.X + 48,
			Y: cell.Y - playtimeScroll + 2 + 2*playtimeBadgeLinePitch,
			W: cell.W - 58,
			H: playtimeBadgeLinePitch,
		}
	}
	// Badge 0 is the earned one (earned first), badge 1 is locked.
	if earned := accentishPixels(frame, dateBand(0)); earned < 30 {
		t.Fatalf("the earned badge's date line painted only %d accent pixels", earned)
	}
	if locked := accentishPixels(frame, dateBand(1)); locked > 0 {
		t.Fatalf("a locked badge painted %d pixels on the date line", locked)
	}

	// The date line must fit inside the row: three lines of text plus the
	// top inset have to stay under the cell height, or the row below clips it.
	if need, have := 2+3*playtimeBadgeLinePitch, playtimeAchRowH-8; need > have {
		t.Fatalf("three badge lines need %dpx but a row is %dpx tall", need, have)
	}
}

// accentishPixels counts cyan text pixels: the unlock date is the only
// accent-coloured text on a badge row, and it is drawn to the right of the
// badge's number tile. The channel spread matters — the grey description text
// next to it is bluish too, so "bright blue" alone would count it.
func accentishPixels(f *probeFrame, r sdl.Rect) int {
	n := 0
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			if x < 0 || y < 0 || x >= probeW || y >= probeH {
				continue
			}
			c := f.at(x, y)
			if int(c.B) > 90 && int(c.G)-int(c.R) > 50 && int(c.B)-int(c.R) > 60 {
				n++
			}
		}
	}
	return n
}

// TestPlaytimePageKeepsThePadFromTheTopBar pins the routing bug that made the
// badge list unscrollable: the Playtime screen has no config elements, so the
// only nodes in its focus graph were the top-bar buttons, focus started on the
// back pill, and handleChromeKey swallowed every arrow before the page saw it.
func TestPlaytimePageKeepsThePadFromTheTopBar(t *testing.T) {
	withPlaytimeViewport(t, probeW, probeH)
	useTopChrome(t)

	config := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	ensurePlaytimeScene(config)
	idx := findSceneIndex(config, playtimeSceneName)
	if idx < 0 {
		t.Fatal("the Playtime scene was not injected")
	}
	prevIdx, prevSel := currentSceneIndex, selectedButtonIndex
	t.Cleanup(func() { currentSceneIndex, selectedButtonIndex = prevIdx, prevSel })
	currentSceneIndex = idx
	playtimeScroll = 0

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
	if m, ok := lookupScene(playtimeSceneName); !ok || !m.handleKey(down, config) {
		t.Fatal("the Playtime page did not take Down")
	}
	if playtimeScroll <= 0 {
		t.Fatalf("Down did not scroll the badge page (scroll=%d)", playtimeScroll)
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
