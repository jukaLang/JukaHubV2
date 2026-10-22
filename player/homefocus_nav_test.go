package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// homeKey sends a key-down event to the home key handler.
func homeKey(t *testing.T, config *Config, sym sdl.Keycode) {
	t.Helper()
	e := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sym}}
	if !handleHomeKey(e, config) {
		t.Fatalf("home key handler did not consume %v", sym)
	}
}

// useHomeAsCurrent points the home globals at a fresh layout for a test.
func useHomeAsCurrent(t *testing.T) {
	t.Helper()
	prevLayout, prevActive := homeLayout, homeLayoutActive
	homeLayout = NewHomeLayout(1280, 720)
	homeLayoutActive = true
	t.Cleanup(func() { homeLayout, homeLayoutActive = prevLayout, prevActive })
}

// TestHomeClockSelectableWithArrows verifies the header clock can be focused
// with the arrow keys (and therefore the D-pad), from the top row of cards.
func TestHomeClockSelectableWithArrows(t *testing.T) {
	useHomeAsCurrent(t)
	config := &Config{}

	for _, start := range []HomeFocusID{HomeFocusMedia, HomeFocusSettings} {
		if !homeLayout.Focus.Set(start) {
			t.Fatalf("cannot focus %q", start)
		}
		homeKey(t, config, sdl.K_UP)
		if got := homeLayout.Focus.Current(); got != HomeFocusClock {
			t.Errorf("Up from %q focused %q, want clock", start, got)
		}
	}

	// Files sits under the launcher, so Up reaches the launcher first and a
	// second Up continues to the clock.
	if !homeLayout.Focus.Set(HomeFocusFiles) {
		t.Fatal("cannot focus files")
	}
	homeKey(t, config, sdl.K_UP)
	if got := homeLayout.Focus.Current(); got != HomeFocusPS5 {
		t.Fatalf("Up from files focused %q, want the PS5 launcher", got)
	}
	homeKey(t, config, sdl.K_UP)
	if got := homeLayout.Focus.Current(); got != HomeFocusClock {
		t.Fatalf("Up from the PS5 launcher focused %q, want clock", got)
	}
}

// TestHomePS5LauncherFocusFallsThroughWhenHidden keeps the D-pad sane on
// screens too narrow for the launcher: the disabled entry must not trap focus.
func TestHomePS5LauncherFocusFallsThroughWhenHidden(t *testing.T) {
	useHomeAsCurrent(t)
	homeLayout.Focus.SetEnabled(HomeFocusPS5, false)
	homeLayout.Focus.SetTileLink(HomeFocusFiles)
	if !homeLayout.Focus.Set(HomeFocusFiles) {
		t.Fatal("cannot focus files")
	}
	homeKey(t, &Config{}, sdl.K_UP)
	if got := homeLayout.Focus.Current(); got != HomeFocusClock {
		t.Fatalf("Up from files with the launcher hidden focused %q, want clock", got)
	}
}

// TestHomePS5LauncherTileLinkFollowsTheTile covers the layout-derived edge: Up
// reaches the launcher from the card it sits over and Down comes back there,
// whichever card that turned out to be.
func TestHomePS5LauncherTileLinkFollowsTheTile(t *testing.T) {
	useHomeAsCurrent(t)

	if !homeLayout.Focus.Set(HomeFocusFiles) {
		t.Fatal("cannot focus files")
	}
	homeKey(t, &Config{}, sdl.K_UP)
	if got := homeLayout.Focus.Current(); got != HomeFocusPS5 {
		t.Fatalf("Up from files focused %q, want the PS5 launcher", got)
	}

	// A wide status cluster pushes the launcher over Media instead.
	homeLayout.Focus.SetTileLink(HomeFocusMedia)
	if !homeLayout.Focus.Set(HomeFocusMedia) {
		t.Fatal("cannot focus media")
	}
	homeKey(t, &Config{}, sdl.K_UP)
	if got := homeLayout.Focus.Current(); got != HomeFocusPS5 {
		t.Fatalf("Up from media with the launcher link focused %q, want the PS5 launcher", got)
	}
	homeKey(t, &Config{}, sdl.K_DOWN)
	if got := homeLayout.Focus.Current(); got != HomeFocusMedia {
		t.Fatalf("Down from the launcher focused %q, want media", got)
	}

	// Clearing the link (the tile is hidden) restores the static graph.
	homeLayout.Focus.SetTileLink("")
	if !homeLayout.Focus.Set(HomeFocusPS5) {
		t.Fatal("cannot focus the launcher")
	}
	homeKey(t, &Config{}, sdl.K_DOWN)
	if got := homeLayout.Focus.Current(); got != HomeFocusFiles {
		t.Fatalf("Down from the launcher without a link focused %q, want files", got)
	}
	homeLayout.Focus.SetEnabled(HomeFocusPS5, false)
	if !homeLayout.Focus.Set(HomeFocusMedia) {
		t.Fatal("cannot focus media")
	}
	homeKey(t, &Config{}, sdl.K_UP)
	if got := homeLayout.Focus.Current(); got != HomeFocusClock {
		t.Fatalf("Up from media without the launcher focused %q, want clock", got)
	}
}

// TestHomePS5LauncherIsReachableWithDpad walks the controller path onto the
// launcher: D-pad moves focus there and A presses it. This is the bug the
// touch-only tile had on the Smart Pro.
func TestHomePS5LauncherIsReachableWithDpad(t *testing.T) {
	clearToastAfterTest(t)
	closePSPanelAfterTest(t)
	useHomeAsCurrent(t)

	if !homeLayout.Focus.Set(HomeFocusFiles) {
		t.Fatal("cannot focus files")
	}
	up := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_DPAD_UP}
	if !handleHomeController(up, &Config{}) {
		t.Fatal("D-pad Up was not handled on home")
	}
	if got := homeLayout.Focus.Current(); got != HomeFocusPS5 {
		t.Fatalf("D-pad Up from files focused %q, want the PS5 launcher", got)
	}

	// A must press it. Where the native client cannot run the press is refused,
	// which is exactly what proves the button is wired to the launcher.
	a := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}
	if !handleHomeController(a, &Config{}) {
		t.Fatal("A was not handled on home")
	}
	if homePressedID != HomeFocusPS5 {
		t.Fatalf("A on the launcher gave press feedback for %q", homePressedID)
	}
	if !psRemoteSupported() && chiakiPanel.open {
		t.Fatal("pressing A opened the client on a host that cannot run it")
	}
}

// TestHomeBellSelectableWithDpad pins the "notification bell is unreachable on
// Home" report: the bell is a chrome control on every other scene, but Home
// drives its own focus graph, so it needs a node of its own. The rendered bell
// rect must also be the one focus and clicks use.
func TestHomeBellSelectableWithDpad(t *testing.T) {
	clearToastAfterTest(t)
	snapshotHomeGlobals(t)
	prevNC := nc
	nc = notifCenter{}
	t.Cleanup(func() {
		nc = prevNC
		homeCalendarOpen = false
	})

	// Render once so the bell is measured and enabled exactly as on screen.
	_ = renderHomeFrame(t, false)
	bell := nc.bellRect
	if bell.W <= 0 || bell.H <= 0 {
		t.Fatalf("bell was not drawn: %+v", bell)
	}
	if got := homeLayout.Items[HomeFocusBell].Rect; got != bell {
		t.Fatalf("bell focus rect %+v differs from the drawn bell %+v", got, bell)
	}
	if !homeLayout.Focus.IsEnabled(HomeFocusBell) {
		t.Fatal("bell is not focusable on Home")
	}

	// Right from the clock reaches it; Left and Down leave it again.
	if !homeLayout.Focus.Set(HomeFocusClock) {
		t.Fatal("cannot focus the clock")
	}
	homeKey(t, &Config{}, sdl.K_RIGHT)
	if got := homeLayout.Focus.Current(); got != HomeFocusBell {
		t.Fatalf("Right from the clock focused %q, want the bell", got)
	}
	homeKey(t, &Config{}, sdl.K_LEFT)
	if got := homeLayout.Focus.Current(); got != HomeFocusClock {
		t.Fatalf("Left from the bell focused %q, want the clock", got)
	}
	if !homeLayout.Focus.Set(HomeFocusBell) {
		t.Fatal("cannot focus the bell")
	}
	homeKey(t, &Config{}, sdl.K_DOWN)
	if got := homeLayout.Focus.Current(); got != HomeFocusSettings {
		t.Fatalf("Down from the bell focused %q, want settings", got)
	}

	// A opens the notification center, the same as clicking the bell.
	if !homeLayout.Focus.Set(HomeFocusBell) {
		t.Fatal("cannot focus the bell")
	}
	a := &sdl.ControllerButtonEvent{Type: sdl.CONTROLLERBUTTONDOWN, Button: sdl.CONTROLLER_BUTTON_A}
	if !handleHomeController(a, &Config{}) {
		t.Fatal("A was not handled on home")
	}
	if !nc.open {
		t.Fatal("A on the bell did not open the notification center")
	}
	if homePressedID != HomeFocusBell {
		t.Fatalf("A on the bell gave press feedback for %q", homePressedID)
	}
	if notifUnreadCount() != 0 {
		t.Fatalf("opening the bell left %d unread, want 0", notifUnreadCount())
	}
}

// TestHomeFocusGraphFullyReachable walks the home focus graph the way the
// D-pad does and asserts every card can be focused from every other card.
func TestHomeFocusGraphFullyReachable(t *testing.T) {
	useHomeAsCurrent(t)

	all := make([]HomeFocusID, 0, len(homeFocusGraph))
	for id := range homeFocusGraph {
		all = append(all, id)
	}
	dirs := []FocusDirection{FocusUp, FocusDown, FocusLeft, FocusRight}

	for _, start := range all {
		reached := map[HomeFocusID]bool{start: true}
		queue := []HomeFocusID{start}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, d := range dirs {
				if !homeLayout.Focus.Set(cur) {
					t.Fatalf("cannot focus %q", cur)
				}
				if next := homeLayout.Focus.Move(d); !reached[next] {
					reached[next] = true
					queue = append(queue, next)
				}
			}
		}
		for _, id := range all {
			if !reached[id] {
				t.Errorf("card %q is not reachable from %q", id, start)
			}
		}
	}
}
