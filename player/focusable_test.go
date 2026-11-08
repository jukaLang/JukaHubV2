package main

// focusable_test.go pins the Focusable contract the focus graph reads. The
// point of the interface is that the graph cannot quietly disagree with the
// geometry an element reports, so these checks compare the two directly.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestFocusableElementDefaultsMatchTheGraph(t *testing.T) {
	// BuildGraph also adds the top-bar chrome that the last render measured, so
	// this check depends on no scene having been drawn yet. It passed alone and
	// grew two phantom nodes whenever a rendering test ran first; clearing the
	// measured rects keeps the element-only graph it is about.
	prevBack, prevTB, prevNC, prevUpdate := sceneTitleBackRect, tb, nc, updatePrompt
	prevClock, prevWeather, prevWiFi, prevMedia := statusClockRect, weatherStatusRect, statusWiFiRect, statusMediaRect
	t.Cleanup(func() {
		sceneTitleBackRect, tb, nc, updatePrompt = prevBack, prevTB, prevNC, prevUpdate
		statusClockRect, weatherStatusRect, statusWiFiRect, statusMediaRect = prevClock, prevWeather, prevWiFi, prevMedia
	})
	sceneTitleBackRect, statusClockRect, weatherStatusRect = sdl.Rect{}, sdl.Rect{}, sdl.Rect{}
	statusWiFiRect, statusMediaRect = sdl.Rect{}, sdl.Rect{}
	tb.startRect = sdl.Rect{}
	nc.bellRect = sdl.Rect{}
	updatePrompt.badge = sdl.Rect{}

	scene := SceneConfig{Elements: []Element{
		{Type: "button", Text: "Play", X: 20, Y: 60, Width: "240", Height: "80"},
		{Type: "button", X: 400, Y: 60},
	}}
	graph := NewFocusEngine().BuildGraph(scene)
	if len(graph.Nodes) != 2 {
		t.Fatalf("graph has %d nodes, want 2", len(graph.Nodes))
	}

	byIndex := map[int]FocusNode{}
	for _, node := range graph.Nodes {
		byIndex[node.Index] = node
	}
	for i, el := range scene.Elements {
		node, ok := byIndex[i]
		if !ok {
			t.Fatalf("element %d has no graph node", i)
		}
		var f Focusable = el
		if got, want := node.Rect, f.FocusRect(); got != want {
			t.Errorf("element %d: node rect %+v != Focusable rect %+v", i, got, want)
		}
		if node.Type != f.FocusType() {
			t.Errorf("element %d: node type %q != Focusable type %q", i, node.Type, f.FocusType())
		}
		if node.Label != f.FocusLabel() {
			t.Errorf("element %d: node label %q != Focusable label %q", i, node.Label, f.FocusLabel())
		}
	}

	// An element with no width/height keeps the graph's documented defaults.
	if got, want := scene.Elements[1].FocusRect(), (sdl.Rect{X: 400, Y: 60, W: 1100, H: 200}); got != want {
		t.Fatalf("default element rect = %+v, want %+v", got, want)
	}
	// A label-less element names itself by type rather than by an empty string.
	if got := scene.Elements[1].FocusLabel(); got != "button" {
		t.Fatalf("label-less element FocusLabel() = %q, want the type", got)
	}
}

func TestFocusNodeSatisfiesFocusable(t *testing.T) {
	var f Focusable = FocusNode{
		Rect: sdl.Rect{X: 1, Y: 2, W: 3, H: 4},
		Type: ChromeBellType,
	}
	if got := f.FocusRect(); got != (sdl.Rect{X: 1, Y: 2, W: 3, H: 4}) {
		t.Fatalf("FocusRect() = %+v", got)
	}
	if got := f.FocusType(); got != ChromeBellType {
		t.Fatalf("FocusType() = %q, want %q", got, ChromeBellType)
	}
	if got := f.FocusLabel(); got != ChromeBellType {
		t.Fatalf("FocusLabel() without a label = %q, want the type", got)
	}
}
