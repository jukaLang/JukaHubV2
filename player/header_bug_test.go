package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestHeaderBugIsBetweenBellAndTrophy(t *testing.T) {
	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		h := NewHomeLayout(size.w, size.h)
		bug := h.Items[HomeFocusBug]
		if bug.Action != HomeActionBugReport || bug.Rect.W <= 0 || !h.Focus.IsEnabled(HomeFocusBug) {
			t.Fatalf("%dx%d: report shortcut %+v is not available", size.w, size.h, bug)
		}
		if over, ok := homeConfigOverlayables()[HomeFocusBug]; !ok || over {
			t.Fatal("a config tile could move the report shortcut")
		}
		if !bootArriveIsHeaderChip(HomeFocusBug) {
			t.Fatal("the report shortcut would render as a card")
		}
		at := map[HomeFocusID]int{}
		for i, id := range h.Order {
			at[id] = i
		}
		if !(at[HomeFocusBell] < at[HomeFocusBug] && at[HomeFocusBug] < at[HomeFocusTrophy]) {
			t.Fatalf("%dx%d: header hit order is bell %d, bug %d, trophy %d", size.w, size.h,
				at[HomeFocusBell], at[HomeFocusBug], at[HomeFocusTrophy])
		}
	}

	keepHeaderRects(t)
	if chromeTier(ChromeBugIndex) != 1 {
		t.Fatal("Report a bug is not in the top bar")
	}
	headerTrophyRect = sdl.Rect{X: 1146, Y: 13, W: 28, H: 28}
	headerBugRect = sdl.Rect{X: 1184, Y: 13, W: 28, H: 28}
	nc.bellRect = sdl.Rect{X: 1222, Y: 13, W: 28, H: 28}
	g := NewFocusEngine().BuildGraph(SceneConfig{Name: "BugHeaderProbe"})
	found := false
	at := map[int]int{}
	for i, n := range g.Nodes {
		at[n.Index] = i
		if n.Index == ChromeBugIndex {
			found = n.Label == headerBugLabel && n.Type == ChromeBugType && n.Rect == headerBugRect
		}
	}
	if !found {
		t.Fatal("scene focus graph omitted Report a bug")
	}
	linked := func(a, b int) bool {
		for _, neighbor := range g.Edges[a] {
			if neighbor == b {
				return true
			}
		}
		return false
	}
	if !linked(at[ChromeTrophyIndex], at[ChromeBugIndex]) || !linked(at[ChromeBugIndex], at[ChromeBellIndex]) ||
		linked(at[ChromeTrophyIndex], at[ChromeBellIndex]) {
		t.Fatal("scene focus skips Report a bug between the cup and bell")
	}
}

func TestHeaderBugHitClearsWithFullscreenBar(t *testing.T) {
	keepHeaderRects(t)
	if headerBugHit(100, 20) {
		t.Fatal("an undrawn report shortcut accepted a tap")
	}
	headerBugRect = sdl.Rect{X: 1184, Y: 13, W: 28, H: 28}
	if !headerBugHit(1198, 27) {
		t.Fatal("a tap on Report a bug missed")
	}
	headerTrophyRect = sdl.Rect{X: 1146, Y: 13, W: 28, H: 28}
	nc.bellRect = sdl.Rect{X: 1222, Y: 13, W: 28, H: 28}
	if headerTrophyHit(1185, 27) || notifIsPointInBell(1211, 27) {
		t.Fatal("an adjacent button stole a tap on the report icon")
	}
	brwClearChromeHits()
	if headerBugHit(1198, 27) {
		t.Fatal("full screen left Report a bug tappable")
	}
}
