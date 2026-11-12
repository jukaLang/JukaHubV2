package main

// Tests for the trophy room's cup in the top bar (header_trophy.go): its place
// in Home's focus graph and in a scene's bar, its hit area, its slot in the
// Home layout and the count it carries.

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// keepHeaderRects clears every top-bar rect for a test and puts them back
// after it, so a rect another test measured cannot become a stop here.
func keepHeaderRects(t *testing.T) {
	t.Helper()
	back, start, clock := sceneTitleBackRect, tb.startRect, statusClockRect
	bell, wifi, weather := nc.bellRect, statusWiFiRect, weatherStatusRect
	media, badge := statusMediaRect, updatePrompt.badge
	volume, brightness, trophy, bug := statusVolumeRect, statusBrightnessRect, headerTrophyRect, headerBugRect
	bluetooth := statusBluetoothRect
	home := homeLayoutActive
	t.Cleanup(func() {
		sceneTitleBackRect, tb.startRect, statusClockRect = back, start, clock
		nc.bellRect, statusWiFiRect, weatherStatusRect = bell, wifi, weather
		statusMediaRect, updatePrompt.badge = media, badge
		statusVolumeRect, statusBrightnessRect, headerTrophyRect = volume, brightness, trophy
		headerBugRect, statusBluetoothRect = bug, bluetooth
		homeLayoutActive = home
	})
	sceneTitleBackRect, tb.startRect, statusClockRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	nc.bellRect, statusWiFiRect, weatherStatusRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	statusMediaRect, updatePrompt.badge = sdl.Rect{}, sdl.Rect{}
	statusVolumeRect, statusBrightnessRect, headerTrophyRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	headerBugRect, statusBluetoothRect = sdl.Rect{}, sdl.Rect{}
	homeLayoutActive = false
}

// On Home the cup sits between the release offer and the bell: Left from the
// bell reaches it and Right from it the bell; with the offer hidden its
// neighbour is the clock, and with no cup the corner walks as it always did.
func TestHomeFocusWalksThroughTheTrophyCup(t *testing.T) {
	f := NewHomeFocusController()
	f.Set(HomeFocusBell)
	if got := f.Move(FocusLeft); got != HomeFocusBug {
		t.Fatalf("Left from the bell = %s, want Report a bug", got)
	}
	if got := f.Move(FocusLeft); got != HomeFocusTrophy {
		t.Fatalf("Left from Report a bug = %s, want the cup", got)
	}
	if got := f.Move(FocusLeft); got != HomeFocusUpdate {
		t.Fatalf("Left from the cup = %s, want the release offer", got)
	}
	if got := f.Move(FocusRight); got != HomeFocusTrophy {
		t.Fatalf("Right from the offer = %s, want the cup", got)
	}
	if got := f.Move(FocusRight); got != HomeFocusBug {
		t.Fatalf("Right from the cup = %s, want Report a bug", got)
	}
	if got := f.Move(FocusRight); got != HomeFocusBell {
		t.Fatalf("Right from Report a bug = %s, want the bell", got)
	}

	// No release offered and nothing playing: the clock is the cup's neighbour.
	f.SetEnabled(HomeFocusUpdate, false)
	f.SetEnabled(HomeFocusNowPlaying, false)
	f.Set(HomeFocusClock)
	if got := f.Move(FocusRight); got != HomeFocusTrophy {
		t.Fatalf("Right from the clock = %s, want the cup", got)
	}
	if got := f.Move(FocusLeft); got != HomeFocusClock {
		t.Fatalf("Left from the cup = %s, want the clock", got)
	}

	// No cup drawn: it is not a stop on the way.
	f.SetEnabled(HomeFocusTrophy, false)
	f.Set(HomeFocusBell)
	if got := f.Move(FocusLeft); got != HomeFocusBug {
		t.Fatalf("with no cup Left from the bell = %s, want Report a bug", got)
	}
	if got := f.Move(FocusLeft); got != HomeFocusClock {
		t.Fatalf("with no cup Left from Report a bug = %s, want the clock", got)
	}
	f.Set(HomeFocusClock)
	if got := f.Move(FocusRight); got != HomeFocusBug {
		t.Fatalf("with no cup Right from the clock = %s, want Report a bug", got)
	}
	if got := f.Move(FocusRight); got != HomeFocusBell {
		t.Fatalf("with no cup Right from Report a bug = %s, want the bell", got)
	}
}

// Down from the cup lands on the pairing code under that end of the bar, or
// on the launcher when no code is drawn — the bell's own way down.
func TestHomeFocusDownFromTheTrophyCup(t *testing.T) {
	f := NewHomeFocusController()
	f.Set(HomeFocusTrophy)
	if got := f.Move(FocusDown); got != HomeFocusQR {
		t.Fatalf("Down from the cup = %s, want the pairing code", got)
	}
	f.SetEnabled(HomeFocusQR, false)
	f.Set(HomeFocusTrophy)
	if got := f.Move(FocusDown); got != HomeFocusPS5 {
		t.Fatalf("with no code Down from the cup = %s, want the PS5 launcher", got)
	}
	f.Set(HomeFocusTrophy)
	if got := f.Move(FocusUp); got != HomeFocusTrophy {
		t.Fatalf("Up from the cup = %s, want it to stay at the top edge", got)
	}
}

// On a scene the cup is a top-bar button, chained between the release offer
// and the bell in the order they are drawn, and a cup that is not drawn is
// not a stop.
func TestChromeTrophyCupJoinsTheBar(t *testing.T) {
	keepHeaderRects(t)
	if chromeTier(ChromeTrophyIndex) != 1 {
		t.Fatal("the cup is not a top-bar button")
	}
	updatePrompt.badge = sdl.Rect{X: 1040, Y: 12, W: 96, H: 30}
	headerTrophyRect = sdl.Rect{X: 1146, Y: 13, W: 28, H: 28}
	nc.bellRect = sdl.Rect{X: 1184, Y: 13, W: 28, H: 28}
	g := NewFocusEngine().BuildGraph(SceneConfig{Name: "TrophyCupProbe"})
	at := map[int]int{}
	for i, n := range g.Nodes {
		at[n.Index] = i
	}
	cup, ok := at[ChromeTrophyIndex]
	if !ok {
		t.Fatalf("the bar has no cup: %+v", g.Nodes)
	}
	if n := g.Nodes[cup]; n.Label != headerTrophyLabel || n.Type != ChromeTrophyType || n.Rect != headerTrophyRect {
		t.Fatalf("cup node = %+v", n)
	}
	linked := func(a, b int) bool {
		for _, j := range g.Edges[a] {
			if j == b {
				return true
			}
		}
		return false
	}
	bell, offer := at[ChromeBellIndex], at[ChromeUpdateIndex]
	if !linked(cup, bell) || !linked(bell, cup) {
		t.Error("the cup and the bell are not neighbours")
	}
	if !linked(cup, offer) || !linked(offer, cup) {
		t.Error("the cup and the release offer are not neighbours")
	}
	if linked(offer, bell) {
		t.Error("the release offer still skips the cup to reach the bell")
	}

	headerTrophyClear()
	for _, n := range NewFocusEngine().BuildGraph(SceneConfig{Name: "TrophyCupProbe"}).Nodes {
		if n.Index == ChromeTrophyIndex {
			t.Fatal("a cup that is not drawn is still a stop")
		}
	}
}

// The cup takes the shared finger-sized target around the drawn chip, and a
// frame that draws no bar (the full-screen Browser) leaves nothing to tap.
func TestHeaderTrophyHitArea(t *testing.T) {
	keepHeaderRects(t)
	if headerTrophyHit(10, 10) {
		t.Fatal("no cup is drawn, yet a tap hit it")
	}
	headerTrophyRect = sdl.Rect{X: 1146, Y: 13, W: 28, H: 28}
	if !headerTrophyHit(1160, 27) {
		t.Fatal("a tap on the cup missed")
	}
	if !headerTrophyHit(1141, 8) {
		t.Fatal("a tap a few pixels off the chip missed the touch target")
	}
	if headerTrophyHit(1100, 27) {
		t.Fatal("a tap well left of the cup hit it")
	}
	brwClearChromeHits()
	if headerTrophyRect.W != 0 || headerTrophyHit(1160, 27) {
		t.Fatal("a frame without the bar left the cup tappable")
	}
}

// Home has a slot for the cup before the first frame (on both canvases), in
// the hit-test order after the bell (main.go tests the bell first, so hover and
// click agree where their touch targets meet), kept out of config overrides,
// the overlap audit and the boot arrival's cards.
func TestHomeLayoutHasTheTrophyCup(t *testing.T) {
	for _, c := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		layout := NewHomeLayout(c.w, c.h)
		r := layout.Items[HomeFocusTrophy].Rect
		if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > c.w || r.Y+r.H > c.h {
			t.Errorf("%dx%d: the cup's placeholder %+v is off the canvas", c.w, c.h, r)
		}
		if a := layout.Items[HomeFocusTrophy].Action; a != HomeActionTrophies {
			t.Errorf("%dx%d: the cup's action is %q", c.w, c.h, a)
		}
		cup, bell := -1, -1
		for i, id := range layout.Order {
			switch id {
			case HomeFocusTrophy:
				cup = i
			case HomeFocusBell:
				bell = i
			}
		}
		if cup < 0 || bell < 0 || cup < bell {
			t.Errorf("%dx%d: the order has the cup at %d and the bell at %d", c.w, c.h, cup, bell)
		}
		for _, problem := range layout.BoundsViolations() {
			if strings.Contains(problem, string(HomeFocusTrophy)) {
				t.Errorf("%dx%d: %s", c.w, c.h, problem)
			}
		}
		if !layout.Focus.IsEnabled(HomeFocusTrophy) {
			t.Errorf("%dx%d: the cup cannot take the focus before the first frame", c.w, c.h)
		}
	}
	if over, ok := homeConfigOverlayables()[HomeFocusTrophy]; !ok || over {
		t.Fatal("a config tile could move the cup")
	}
	if !bootArriveIsHeaderChip(HomeFocusTrophy) {
		t.Fatal("the boot arrival would cut the cup out as a card")
	}
}

// keepTrophyCupState keeps the playtime snapshot (keepTrophyState) and the
// cup's own caches, and starts the test with no session baseline.
func keepTrophyCupState(t *testing.T) {
	t.Helper()
	keepTrophyState(t)
	prevUnseen, prevGen, prevBase := trophyUnseen, trophySeenGen, trophyBaseline
	t.Cleanup(func() {
		trophyUnseen, trophySeenGen, trophyBaseline = prevUnseen, prevGen, prevBase
	})
	trophyUnseen.valid = false
	trophyBaseline.set, trophyBaseline.n = false, 0
}

// The count is what has been earned beyond the record the room writes — beyond
// what was earned when JukaHub started, while there is none — with no clock in
// it, and the room clears it.
func TestTrophyCupCountsWhatTheRoomHasNotShown(t *testing.T) {
	keepTrophyCupState(t)
	if len(playtimeAchievements) < 4 {
		t.Skip("the trophy set is too small for this test")
	}
	const stamp = "2026-11-10T12:00:00Z"
	d := newPlaytimeData()
	d.Unlocked[playtimeAchievements[0].ID] = stamp
	d.Unlocked["not-a-trophy"] = stamp // an old id counts for nothing
	playtime = playtimeState{loaded: true, data: d, rev: playtime.rev + 1}

	// Never opened: what was earned when the cup was first asked for is the
	// baseline, so nothing is new yet.
	config := &Config{}
	if got := trophyUnseenCount(config); got != 0 {
		t.Fatalf("count at the start = %d, want 0", got)
	}
	// A trophy earned while JukaHub runs is new, whatever its stamp says (a
	// clock that has not been set yet stamps the past).
	d.Unlocked[playtimeAchievements[1].ID] = "2001-01-01T00:00:00Z"
	playtime.rev++
	if got := trophyUnseenCount(config); got != 1 {
		t.Fatalf("count after a trophy = %d, want 1", got)
	}

	// The room on screen writes the record and clears the count; with nothing
	// new it does not write again.
	trophyRoomNoteSeen(config)
	if got := trophyUnseenCount(config); got != 0 {
		t.Fatalf("count with the room on screen = %d, want 0", got)
	}
	if n, ok := trophySeenRecord(config); !ok || n != 2 {
		t.Fatalf("record = %d (stored %v), want 2", n, ok)
	}
	gen := trophySeenGen
	trophyRoomNoteSeen(config)
	if trophySeenGen != gen {
		t.Fatal("a visit with nothing new wrote the record again")
	}

	// Two more earned: two new.
	d.Unlocked[playtimeAchievements[2].ID] = stamp
	d.Unlocked[playtimeAchievements[3].ID] = stamp
	playtime.rev++
	if got := trophyUnseenCount(config); got != 2 {
		t.Fatalf("count after two more = %d, want 2", got)
	}

	// A record above what is earned (a backup with fewer trophies restored)
	// counts nothing, and the room writes it again.
	config.Variables.Custom[trophySeenKey] = "50"
	trophySeenGen++
	if got := trophyUnseenCount(config); got != 0 {
		t.Fatalf("count under a higher record = %d, want 0", got)
	}
	trophyRoomNoteSeen(config)
	if n, _ := trophySeenRecord(config); n != 4 {
		t.Fatalf("record after the room = %d, want 4", n)
	}

	// An unreadable record is no record, and the first build's visit time
	// goes with the next write.
	config.Variables.Custom[trophySeenKey] = "lots"
	config.Variables.Custom[trophySeenOldKey] = stamp
	trophySeenGen++
	if _, ok := trophySeenRecord(config); ok {
		t.Fatal("an unreadable record was read")
	}
	trophyRoomNoteSeen(config)
	if _, ok := config.Variables.Custom[trophySeenOldKey]; ok {
		t.Fatal("the first build's visit time was kept")
	}
	if n, ok := trophySeenRecord(config); !ok || n != 4 {
		t.Fatalf("record after an unreadable one = %d (stored %v), want 4", n, ok)
	}

	// The first visit ever starts the record even with nothing to clear.
	fresh := &Config{}
	trophyRoomNoteSeen(fresh)
	if n, ok := trophySeenRecord(fresh); !ok || n != 4 {
		t.Fatalf("first record = %d (stored %v), want 4", n, ok)
	}
}

// In the room already, the cup starts the visit over instead of opening the
// room again: the Trophies tab, the top of the grid, no sheet.
func TestHeaderTrophyInTheRoomStartsTheVisitOver(t *testing.T) {
	prevScene, prevFor, prevScroll, prevRoom := currentSceneIndex, playtimeScrollFor, playtimeScroll, trophyRoom
	t.Cleanup(func() {
		currentSceneIndex, playtimeScrollFor, playtimeScroll, trophyRoom = prevScene, prevFor, prevScroll, prevRoom
	})
	config := trophyAccessConfig()
	room := sceneIndexExact(config, playtimeSceneName)
	if room < 0 {
		t.Fatal("the trophy room was not injected")
	}
	currentSceneIndex = room
	if !headerTrophyInRoom(config) {
		t.Fatal("the room is the screen, yet the cup does not know it")
	}
	playtimeScrollFor, playtimeScroll = room, 480
	trophyRoom.tab, trophyRoom.sheet = trophyTabStats, true
	headerTrophyActivate(config)
	if currentSceneIndex != room {
		t.Fatalf("the press left the room for scene %d", currentSceneIndex)
	}
	if playtimeScroll != 0 || trophyRoom.tab != trophyTabTrophies || trophyRoom.sheet {
		t.Fatalf("after the press: scroll %d, tab %d, sheet %v", playtimeScroll, trophyRoom.tab, trophyRoom.sheet)
	}
	currentSceneIndex = sceneIndexExact(config, "Main")
	if headerTrophyInRoom(config) || headerTrophyInRoom(nil) {
		t.Fatal("the cup thinks Home is the trophy room")
	}
}
