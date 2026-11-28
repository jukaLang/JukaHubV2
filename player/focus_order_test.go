package main

import "testing"

// The web builder's D-pad order is a scene-level list of element indices;
// BuildGraph must follow it while older scenes keep the Y/X fallback.
func TestBuildGraphRespectsFocusOrder(t *testing.T) {
	scene := SceneConfig{Name: "OrderProbe", Elements: []Element{
		{Type: "button", X: 10, Y: 10, Width: "60", Height: "30"},
		{Type: "button", X: 10, Y: 60, Width: "60", Height: "30"},
		{Type: "button", X: 10, Y: 110, Width: "60", Height: "30"},
	}}
	indices := func(g *FocusGraph) []int {
		var out []int
		for _, n := range g.Nodes[:min(3, len(g.Nodes))] {
			out = append(out, n.Index)
		}
		return out
	}
	wantDefault := []int{0, 1, 2}
	got := indices(NewFocusEngine().BuildGraph(scene))
	if len(got) != len(wantDefault) || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("default order = %v, want %v", got, wantDefault)
	}

	scene.FocusOrder = []int{2, 0, 1}
	got = indices(NewFocusEngine().BuildGraph(scene))
	if len(got) != 3 || got[0] != 2 || got[1] != 0 || got[2] != 1 {
		t.Fatalf("authored order = %v, want [2 0 1]", got)
	}

	// An unlisted element comes after the listed ones, still in Y/X order.
	scene.FocusOrder = []int{1}
	got = indices(NewFocusEngine().BuildGraph(scene))
	if len(got) != 3 || got[0] != 1 || got[1] != 0 || got[2] != 2 {
		t.Fatalf("partial order = %v, want [1 0 2]", got)
	}
}
