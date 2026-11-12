package main

// navmode_test.go — the D-pad navigation modes on synthetic layouts and the
// focus graph following a mode chosen for its area.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// navmodeTestGrid lays out rows of 100×80 cells; counts is the number of
// cells per row. Indices run in reading order.
func navmodeTestGrid(counts ...int) []sdl.Rect {
	var out []sdl.Rect
	for r, n := range counts {
		for c := 0; c < n; c++ {
			out = append(out, sdl.Rect{X: int32(c * 110), Y: int32(r * 90), W: 100, H: 80})
		}
	}
	return out
}

// navmodeTestCase is one move and what it should land on.
type navmodeTestCase struct {
	name   string
	cur    int
	dx, dy int
	wrap   bool
	want   int
	ok     bool
}

func navmodeRunCases(t *testing.T, mode navMode, rects []sdl.Rect, cases []navmodeTestCase) {
	t.Helper()
	for _, c := range cases {
		got, ok := navmodeMove(mode, rects, c.cur, c.dx, c.dy, navmodeOpts{wrap: c.wrap, page: navmodeListPage})
		if got != c.want || ok != c.ok {
			t.Errorf("%s %s: got %d (%v), want %d (%v)", navmodeLabel(mode), c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestNavmodeGrid(t *testing.T) {
	// 0 1 2
	// 3 4 5
	// 6 7
	navmodeRunCases(t, navModeGrid, navmodeTestGrid(3, 3, 2), []navmodeTestCase{
		{name: "right in the row", cur: 0, dx: 1, want: 1, ok: true},
		{name: "right stops at the end", cur: 2, dx: 1, want: 2},
		{name: "right wraps onto the next row", cur: 2, dx: 1, wrap: true, want: 3, ok: true},
		{name: "left wraps back", cur: 3, dx: -1, wrap: true, want: 2, ok: true},
		{name: "left stops at the start", cur: 3, dx: -1, want: 3},
		{name: "down keeps the column", cur: 1, dy: 1, want: 4, ok: true},
		{name: "down into a shorter row", cur: 5, dy: 1, want: 7, ok: true},
		{name: "up from the top is the edge", cur: 1, dy: -1, wrap: true, want: 1},
		{name: "down from the bottom is the edge", cur: 7, dy: 1, wrap: true, want: 7},
		{name: "the very last does not wrap", cur: 7, dx: 1, wrap: true, want: 7},
	})
}

func TestNavmodeList(t *testing.T) {
	navmodeRunCases(t, navModeList, navmodeTestGrid(1, 1, 1, 1, 1, 1, 1, 1), []navmodeTestCase{
		{name: "down", cur: 0, dy: 1, want: 1, ok: true},
		{name: "up", cur: 3, dy: -1, want: 2, ok: true},
		{name: "up from the first is the edge", cur: 0, dy: -1, wrap: true, want: 0},
		{name: "down from the last stops", cur: 7, dy: 1, want: 7},
		{name: "down from the last wraps", cur: 7, dy: 1, wrap: true, want: 0, ok: true},
		{name: "right pages", cur: 0, dx: 1, want: 5, ok: true},
		{name: "right clamps at the end", cur: 5, dx: 1, want: 7, ok: true},
		{name: "left pages back", cur: 7, dx: -1, want: 2, ok: true},
		{name: "left at the first stays", cur: 0, dx: -1, want: 0},
	})
	// Two columns are still one list, in reading order.
	navmodeRunCases(t, navModeList, navmodeTestGrid(2, 2), []navmodeTestCase{
		{name: "down from a row's end", cur: 1, dy: 1, want: 2, ok: true},
		{name: "up into the row above", cur: 2, dy: -1, want: 1, ok: true},
	})
}

func TestNavmodeFreeform(t *testing.T) {
	at := func(x, y int32) sdl.Rect { return sdl.Rect{X: x - 10, Y: y - 10, W: 20, H: 20} }
	rects := []sdl.Rect{
		at(0, 0),
		at(100, 10),
		at(60, 70),
		at(0, 120),
		at(-80, -80),
		at(100, -10),
	}
	navmodeRunCases(t, navModeFreeform, rects, []navmodeTestCase{
		// (60,70) is nearer but 70 to the side of a 60 step: outside the cone.
		{name: "right takes the level one", cur: 0, dx: 1, want: 1, ok: true},
		{name: "down takes the nearest in the cone", cur: 0, dy: 1, want: 2, ok: true},
		{name: "the diagonal is inside the cone", cur: 0, dy: -1, want: 4, ok: true},
		{name: "left too", cur: 0, dx: -1, want: 4, ok: true},
		{name: "back left from the right", cur: 1, dx: -1, want: 0, ok: true},
		{name: "nothing in the cone", cur: 1, dx: 1, want: 1},
	})
	// Equally near: the one less to the side wins.
	tie := []sdl.Rect{at(0, 0), at(60, 80), at(0, 100)}
	if got, ok := navmodeMove(navModeFreeform, tie, 0, 0, 1, navmodeOpts{}); !ok || got != 2 {
		t.Fatalf("tie went to %d (%v), want the straight one (2)", got, ok)
	}
	if _, ok := navmodeMove(navModeClassic, rects, 0, 1, 0, navmodeOpts{}); ok {
		t.Fatal("Classic took a move; it leaves the walk to the spatial graph")
	}
}

func TestNavmodeRows(t *testing.T) {
	rects := []sdl.Rect{
		{X: 120, Y: 10, W: 100, H: 40},
		{X: 0, Y: 80, W: 100, H: 60},
		{X: 0, Y: 0, W: 100, H: 60},
	}
	rows := navmodeRows(rects)
	if len(rows) != 2 || len(rows[0]) != 2 || rows[0][0] != 2 || rows[0][1] != 0 || rows[1][0] != 1 {
		t.Fatalf("rows = %v, want [[2 0] [1]]", rows)
	}
}

func TestNavmodeAreasAndDefaults(t *testing.T) {
	prev := navmodeState
	t.Cleanup(func() { navmodeState = prev })
	navmodeState.set = [navAreaCount]bool{}
	for name, want := range map[string]navArea{"Games": navAreaLibrary, "FileExplorer": navAreaLists, "Tube": navAreaLists,
		"Settings": navAreaSettings, "ThemePresets": navAreaSettings, "About": navAreaScreens} {
		if got := navmodeAreaForScene(name); got != want {
			t.Errorf("%s is in area %d, want %d", name, got, want)
		}
	}
	for a, want := range [navAreaCount]navMode{navModeGrid, navModeList, navModeFreeform, navModeClassic} {
		if got := navmodeFor(navArea(a)); got != want {
			t.Errorf("area %d defaults to %s, want %s", a, navmodeLabel(got), navmodeLabel(want))
		}
	}
	// The focus graph keeps its spatial walk until a mode is chosen on purpose.
	if navmodeFocusMode("FileExplorer") != navModeClassic {
		t.Fatal("Files' buttons left Classic without a choice")
	}
	config := &Config{}
	config.Variables.Custom = map[string]interface{}{"navModeLists": "freeform", "navModeWrap": "true", "navModeLibrary": "bogus"}
	navmodeLoadSettings(config)
	if navmodeFocusMode("FileExplorer") != navModeFreeform || !navmodeWrapEnabled() || navmodeFor(navAreaLibrary) != navModeGrid {
		t.Fatalf("loaded lists %s wrap %v library %s", navmodeLabel(navmodeFocusMode("FileExplorer")),
			navmodeWrapEnabled(), navmodeLabel(navmodeFor(navAreaLibrary)))
	}
}

func TestNavmodeFocusEngineFollowsTheChosenMode(t *testing.T) {
	prev, prevHome := navmodeState, homeLayoutActive
	prevBack, prevStart, prevClock := sceneTitleBackRect, tb.startRect, statusClockRect
	prevBell, prevWiFi, prevWeather := nc.bellRect, statusWiFiRect, weatherStatusRect
	prevMedia, prevBadge := statusMediaRect, updatePrompt.badge
	prevVolume, prevBrightness, prevTrophy, prevBug := statusVolumeRect, statusBrightnessRect, headerTrophyRect, headerBugRect
	prevBluetooth := statusBluetoothRect
	t.Cleanup(func() {
		navmodeState, homeLayoutActive = prev, prevHome
		sceneTitleBackRect, tb.startRect, statusClockRect = prevBack, prevStart, prevClock
		nc.bellRect, statusWiFiRect, weatherStatusRect = prevBell, prevWiFi, prevWeather
		statusMediaRect, updatePrompt.badge = prevMedia, prevBadge
		statusVolumeRect, statusBrightnessRect, headerTrophyRect = prevVolume, prevBrightness, prevTrophy
		headerBugRect, statusBluetoothRect = prevBug, prevBluetooth
	})
	// No top bar measured: the graph is the scene's own buttons.
	sceneTitleBackRect, tb.startRect, statusClockRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	nc.bellRect, statusWiFiRect, weatherStatusRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	statusMediaRect, updatePrompt.badge = sdl.Rect{}, sdl.Rect{}
	statusVolumeRect, statusBrightnessRect, headerTrophyRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	headerBugRect, statusBluetoothRect = sdl.Rect{}, sdl.Rect{}
	homeLayoutActive = false
	navmodeState.set = [navAreaCount]bool{}
	navmodeState.wrap = false

	scene := SceneConfig{Name: "NavmodeProbe", Elements: []Element{
		{Type: "button", Text: "A", X: 0, Y: 0, Width: "100", Height: "60"},
		{Type: "button", Text: "B", X: 120, Y: 0, Width: "100", Height: "60"},
		{Type: "button", Text: "C", X: 0, Y: 100, Width: "100", Height: "60"},
	}}
	fe := NewFocusEngine()
	fe.SetGraph(scene.Name, fe.BuildGraph(scene))
	if !fe.SetByElementIndex(1) {
		t.Fatal("B is not in the graph")
	}
	if got := fe.Navigate(ActionNavigateDown); got != 2 {
		t.Fatalf("Classic: Down from B went to %d, want C (2)", got)
	}

	navmodeState.mode[navAreaScreens], navmodeState.set[navAreaScreens] = navModeGrid, true
	fe.SetByElementIndex(1)
	if got := fe.Navigate(ActionNavigateRight); got != 1 {
		t.Fatalf("Grid: Right at the row's end went to %d, want to stay on B (1)", got)
	}
	fe.SetByElementIndex(1)
	if got := fe.Navigate(ActionNavigateDown); got != 2 {
		t.Fatalf("Grid: Down from B went to %d, want C, the short row's last (2)", got)
	}

	navmodeState.mode[navAreaScreens] = navModeList
	fe.SetByElementIndex(0)
	if got := fe.Navigate(ActionNavigateRight); got != 2 {
		t.Fatalf("List: Right from A went to %d, want a page on, clamped to C (2)", got)
	}
	fe.SetByElementIndex(1)
	if got := fe.Navigate(ActionNavigateDown); got != 2 {
		t.Fatalf("List: Down from B went to %d, want C (2)", got)
	}

	// Home keeps its own wiring whatever is chosen.
	homeLayoutActive = true
	fe.SetByElementIndex(0)
	if got := fe.Navigate(ActionNavigateRight); got != 1 {
		t.Fatalf("on Home Right from A went to %d, want the spatial walk's B (1)", got)
	}
}
