package main

import (
	"path/filepath"
	"testing"
)

// TestShippedConfigsAreDpadReachable walks the focus graph of every scene in
// both shipped designs and fails on a focusable control the D-pad cannot reach
// from the scene's first control (TEST-3). It replaces the manual "walk every
// screen with the pad" checklist: a control placed off the grid, or covered by
// another, shows up here with its scene and label.
func TestShippedConfigsAreDpadReachable(t *testing.T) {
	paths := []string{
		"jukaconfig.json",
		filepath.Join("trimui", "jukaconfig.device.json"),
	}
	fe := NewFocusEngine()
	for _, path := range paths {
		cfg, _, err := LoadXMLConfigWithFallback(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, scene := range cfg.Scenes {
			graph := fe.BuildGraph(scene)
			for _, node := range unreachableFocusNodes(graph) {
				t.Errorf("%s %q: %s %q (element %d) is unreachable from the D-pad",
					path, scene.Name, node.Type, node.Label, node.Index)
			}
		}
	}
}

// unreachableFocusNodes returns the graph's nodes that a breadth-first walk
// from the node a fresh screen focuses cannot reach. A scene with no controls
// of its own has nothing to report.
func unreachableFocusNodes(g *FocusGraph) []FocusNode {
	if g == nil || len(g.Nodes) == 0 {
		return nil
	}
	start := firstSceneNode(g)
	if start < 0 {
		// A bar-only graph is entered with Up; its leftmost node is the entry.
		start = 0
	}
	seen := make([]bool, len(g.Nodes))
	seen[start] = true
	queue := []int{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range g.Edges[cur] {
			if next >= 0 && next < len(g.Nodes) && !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	var out []FocusNode
	for i, node := range g.Nodes {
		if !seen[i] {
			out = append(out, node)
		}
	}
	return out
}
