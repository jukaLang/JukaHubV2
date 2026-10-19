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

	for _, start := range []HomeFocusID{HomeFocusMedia, HomeFocusFiles, HomeFocusSettings} {
		if !homeLayout.Focus.Set(start) {
			t.Fatalf("cannot focus %q", start)
		}
		homeKey(t, config, sdl.K_UP)
		if got := homeLayout.Focus.Current(); got != HomeFocusClock {
			t.Errorf("Up from %q focused %q, want clock", start, got)
		}
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
