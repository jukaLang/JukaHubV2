package main

// D-pad reachability audit.
//
// The menu audit proves what a scene draws is what a button does. This proves
// the other half of the accessibility contract: that every focusable control on
// every generic (config-driven) scene can actually be *reached* with the D-pad.
//
// Two known traps this guards:
//
//   - A scene whose elements all sit at a diagonal bias, so the 1.5x primary
//     direction rule in buildSpatialEdges drops every edge and focus is
//     stranded on the first node.
//   - A lone element in a row/column whose only neighbour was filtered out,
//     making it unreachable no matter which direction is pressed.
//
// Scenes that are registered modules (Terminal, MusicPlayer, ...) draw their
// own controls and own their key handling; those are covered by their own
// tests. This audit walks the generic renderer path only.

import (
	"strings"
	"testing"
)

// walkSceneDPad performs a BFS over the focus graph exactly the way the key
// handler does: reinstall the graph (which preserves focus), press one of the
// four directions, and follow the returned element index. It returns the set of
// scene element indices reachable from the starting focus.
func walkSceneDPad(t *testing.T, scene SceneConfig) map[int]bool {
	t.Helper()
	reached := map[int]bool{}

	// Fresh engine per scene so persisted focus from another scene cannot
	// smuggle the walk past an unreachable node.
	prevEngine := focusEngine
	prevSceneIdx, prevSel := currentSceneIndex, selectedButtonIndex
	focusEngine = NewFocusEngine()
	currentSceneIndex, selectedButtonIndex = 0, 0
	t.Cleanup(func() {
		focusEngine = prevEngine
		currentSceneIndex, selectedButtonIndex = prevSceneIdx, prevSel
	})

	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	start := focusEngine.ElementIndex()
	if start < 0 {
		return reached
	}
	reached[start] = true
	queue := []int{start}
	actions := []Action{ActionNavigateUp, ActionNavigateDown, ActionNavigateLeft, ActionNavigateRight}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, act := range actions {
			if !focusEngine.SetByElementIndex(cur) {
				continue
			}
			next := focusEngine.Navigate(act)
			// Dead-ends bridge into the top-bar chrome; that only moves focus
			// onto negative sentinels, which are not scene elements.
			if next < 0 || reached[next] {
				continue
			}
			reached[next] = true
			queue = append(queue, next)
		}
	}
	return reached
}

// TestEveryGenericSceneIsDpadReachable walks every config-driven scene of both
// shipped configs (plus the applets the startup injections add) and requires
// every focusable element to be selectable with the D-pad from the initial
// focus. A control that renders, has a trigger and stays in bounds is still
// broken if the user can never highlight it.
func TestEveryGenericSceneIsDpadReachable(t *testing.T) {
	withScreen(t, probeW, probeH)
	for name, config := range loadAuditConfigs(t) {
		startupInjectScenes(config)
		for _, scene := range config.Scenes {
			if strings.EqualFold(scene.Layout, "home") {
				// Home draws its own cards and focus graph; the home nav test
				// already walks that graph.
				continue
			}
			if _, isModule := lookupScene(scene.Name); isModule {
				// Registered modules own their controls and key handling.
				continue
			}
			var focusable []int
			for i, el := range scene.Elements {
				if isFocusableElement(el) {
					focusable = append(focusable, i)
				}
			}
			if len(focusable) == 0 {
				continue
			}
			reached := walkSceneDPad(t, scene)
			if len(reached) == 0 {
				t.Errorf("%s / %s: no focusable node at all (%d focusable elements)",
					name, scene.Name, len(focusable))
				continue
			}
			for _, i := range focusable {
				if !reached[i] {
					t.Errorf("%s / %s: %q at (%d,%d) cannot be reached with the D-pad",
						name, scene.Name, scene.Elements[i].Text,
						scene.Elements[i].X, scene.Elements[i].Y)
				}
			}
		}
	}
}

// TestEveryFocusableTypeIsInTheGraph pins the focus engine's type list against
// the renderer's element types: an element the renderer draws as interactive but
// BuildGraph skips can never be selected with the pad.
func TestEveryFocusableTypeIsInTheGraph(t *testing.T) {
	// Types the generic renderer draws as something the user operates.
	drawnInteractive := []string{
		"button", "input", "searchresults", "dynamiclist", "recent",
		"favorites", "chat", "themegallery", "toggle", "textbrowser",
		"shortslist", "packagelist",
	}
	for _, typ := range drawnInteractive {
		if !isFocusableElement(Element{Type: typ}) {
			t.Errorf("element type %q is drawn as interactive but is not focusable", typ)
		}
	}
	// And the reverse: a decorative type must not steal focus.
	for _, typ := range []string{"label", "image", "video", "textlog", "piechart", "unitconverter", "menu"} {
		if isFocusableElement(Element{Type: typ}) {
			t.Errorf("decorative type %q is focusable and would trap the pad", typ)
		}
	}
}

// walkSceneViaKeyPath drives the *real* key handler (moveSelection) the way an
// SDL keydown does, rather than poking the engine directly. From each reached
// element it presses every one of the four pad directions and records where the
// handler lands, so the walk exercises the handler's graph reinstall, its
// chrome bridge and its linear fallback — not just the engine's Navigate.
func walkSceneViaKeyPath(t *testing.T, config *Config, sceneIdx int) map[int]bool {
	t.Helper()
	reached := map[int]bool{}

	prevEngine := focusEngine
	prevSceneIdx, prevSel := currentSceneIndex, selectedButtonIndex
	prevResult := focusedResultIndex
	focusEngine = NewFocusEngine()
	currentSceneIndex, selectedButtonIndex = sceneIdx, 0
	focusedResultIndex = -1
	t.Cleanup(func() {
		focusEngine = prevEngine
		currentSceneIndex, selectedButtonIndex = prevSceneIdx, prevSel
		focusedResultIndex = prevResult
	})

	scene := config.Scenes[sceneIdx]
	focusEngine.SetGraph(scene.Name, focusEngine.BuildGraph(scene))
	// The real app syncs the global from the engine right after installing a
	// graph; skipping that leaves selectedButtonIndex pointing at whatever
	// element 0 happens to be, which aborts the walk on any scene whose first
	// focusable element is not index 0.
	focusEngine.SyncSelected()
	if focusEngine.ElementIndex() < 0 {
		return reached
	}
	reached[selectedButtonIndex] = true

	actions := []Action{ActionNavigateUp, ActionNavigateDown, ActionNavigateLeft, ActionNavigateRight}
	queue := []int{selectedButtonIndex}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, act := range actions {
			// Pin focus on the node under test before pressing, so the handler
			// navigates from there rather than from wherever the last press left
			// off. This keeps the walk a BFS over the real handler.
			if !focusEngine.SetByElementIndex(cur) {
				continue
			}
			moveSelection(config, act)
			next := selectedButtonIndex
			// Chrome nodes carry negative sentinels; they are not scene elements
			// but are still worth recording so the walk can leave them again.
			if next < 0 || reached[next] {
				continue
			}
			reached[next] = true
			queue = append(queue, next)
		}
	}
	return reached
}

// TestEveryGenericSceneIsReachableViaTheRealKeyPath repeats the audit through
// moveSelection, so a bug in the handler (not the engine) is caught too: the
// handler owns the graph reinstall, the chrome bridge and the linear fallback.
func TestEveryGenericSceneIsReachableViaTheRealKeyPath(t *testing.T) {
	withScreen(t, probeW, probeH)
	for name, config := range loadAuditConfigs(t) {
		startupInjectScenes(config)
		for i, scene := range config.Scenes {
			if strings.EqualFold(scene.Layout, "home") {
				continue
			}
			if _, isModule := lookupScene(scene.Name); isModule {
				continue
			}
			var focusable []int
			for j, el := range scene.Elements {
				if isFocusableElement(el) {
					focusable = append(focusable, j)
				}
			}
			if len(focusable) == 0 {
				continue
			}
			reached := walkSceneViaKeyPath(t, config, i)
			for _, j := range focusable {
				if !reached[j] {
					t.Errorf("%s / %s: %q at (%d,%d) is never highlighted by moveSelection",
						name, scene.Name, scene.Elements[j].Text,
						scene.Elements[j].X, scene.Elements[j].Y)
				}
			}
		}
	}
}

// TestTheReachabilityAuditHasTeeth pins the audit against a scene it *must*
// reject: two controls drawn at exactly the same centre, which the spatial
// builder cannot connect (a zero vector is not a cardinal direction), leaving
// each one isolated. If this synthetic scene passes, the audit has stopped
// guarding anything.
func TestTheReachabilityAuditHasTeeth(t *testing.T) {
	scene := SceneConfig{Name: "TeethCheck", Elements: []Element{
		{Type: "button", Text: "A", X: 24, Y: 72, Width: "296", Height: "55"},
		{Type: "button", Text: "B", X: 24, Y: 72, Width: "296", Height: "55"},
	}}
	reached := walkSceneDPad(t, scene)
	if len(reached) == len(scene.Elements) {
		t.Fatalf("audit reports every node reachable on a deliberately isolated scene: %v", reached)
	}
	// And the real key path must agree that the isolated node is unreachable.
	config := &Config{Scenes: []SceneConfig{scene}}
	viaKeys := walkSceneViaKeyPath(t, config, 0)
	if len(viaKeys) == len(scene.Elements) {
		t.Fatalf("moveSelection walk reports full reach on an isolated scene: %v", viaKeys)
	}
}

// TestFocusGraphEdgesAreBidirectional: spatial navigation only ever walks edges,
// so an edge that points one way makes the return trip impossible and strands
// focus at that node until the user restarts the scene.
func TestFocusGraphEdgesAreBidirectional(t *testing.T) {
	for name, config := range loadAuditConfigs(t) {
		startupInjectScenes(config)
		for _, scene := range config.Scenes {
			if strings.EqualFold(scene.Layout, "home") {
				continue
			}
			if _, isModule := lookupScene(scene.Name); isModule {
				continue
			}
			graph := NewFocusEngine().BuildGraph(scene)
			for from, tos := range graph.Edges {
				for _, to := range tos {
					back := false
					for _, backTo := range graph.Edges[to] {
						if backTo == from {
							back = true
							break
						}
					}
					if !back {
						t.Errorf("%s / %s: focus edge %d -> %d has no return edge",
							name, scene.Name, from, to)
					}
				}
			}
		}
	}
}
