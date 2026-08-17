package main

import (
	"math"
	"sort"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

// Semantic input actions used by the focus engine and input normalization.
type Action int

const (
	ActionNone Action = iota
	ActionNavigateUp
	ActionNavigateDown
	ActionNavigateLeft
	ActionNavigateRight
	ActionConfirm
	ActionBack
	ActionContext
	ActionSearch
	ActionPagePrevious
	ActionPageNext
	ActionHome
	ActionQuitChord
)

// FocusNode represents one focusable element in a scene.
type FocusNode struct {
	Index int      // scene element index
	Rect  sdl.Rect // bounds used for spatial navigation
	Type  string   // "button", "input", "recent", "dynamiclist", "searchresults", etc.
	Label string   // display text for debugging/logging
}

// FocusGraph is the explicit focus graph for one scene.
type FocusGraph struct {
	Nodes []FocusNode
	Edges map[int][]int // node index -> neighbor node indices
}

// FocusEngine manages focus for the active scene.
// It is safe for single-threaded SDL use; no internal locking required.
type FocusEngine struct {
	graph        *FocusGraph
	current      int            // index into graph.Nodes (-1 = none)
	persistFocus map[string]int // scene name -> last focused element index
	mu           sync.RWMutex
}

// NewFocusEngine creates a new FocusEngine with empty persistence.
func NewFocusEngine() *FocusEngine {
	return &FocusEngine{
		persistFocus: make(map[string]int),
	}
}

// BuildGraph constructs a focus graph from a scene's elements.
// It includes button, input, recent, dynamiclist, and searchresults elements.
// Elements are ordered left-to-right, top-to-bottom for stable traversal.
func (fe *FocusEngine) BuildGraph(scene SceneConfig) *FocusGraph {
	var nodes []FocusNode
	for i, el := range scene.Elements {
		if !isFocusableElement(el) {
			continue
		}
		label := el.Text
		if label == "" {
			label = el.Type
		}
		nodes = append(nodes, FocusNode{
			Index: i,
			Rect:  sdl.Rect{X: el.X, Y: el.Y, W: getElementWidth(el, 1100), H: getElementHeight(el, 200)},
			Type:  el.Type,
			Label: label,
		})
	}
	// Sort by Y then X for stable ordering.
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Rect.Y != nodes[j].Rect.Y {
			return nodes[i].Rect.Y < nodes[j].Rect.Y
		}
		return nodes[i].Rect.X < nodes[j].Rect.X
	})

	edges := buildSpatialEdges(nodes)
	return &FocusGraph{Nodes: nodes, Edges: edges}
}

// SetGraph installs a new focus graph and restores persisted focus if available.
func (fe *FocusEngine) SetGraph(sceneName string, graph *FocusGraph) {
	fe.mu.Lock()
	fe.graph = graph
	fe.current = -1

	if graph != nil && len(graph.Nodes) > 0 {
		// Restore persisted focus or default to first node.
		if persisted, ok := fe.persistFocus[sceneName]; ok {
			for i, node := range graph.Nodes {
				if node.Index == persisted {
					fe.current = i
					break
				}
			}
		}
		if fe.current == -1 {
			fe.current = 0
		}
	}
	fe.mu.Unlock()
}

// Current returns the current FocusNode and its scene element index.
// Returns false if no focus is active.
func (fe *FocusEngine) Current() (FocusNode, bool) {
	fe.mu.RLock()
	defer fe.mu.RUnlock()
	if fe.graph == nil || fe.current < 0 || fe.current >= len(fe.graph.Nodes) {
		return FocusNode{}, false
	}
	return fe.graph.Nodes[fe.current], true
}

// ElementIndex returns the scene element index of the currently focused node.
func (fe *FocusEngine) ElementIndex() int {
	node, ok := fe.Current()
	if !ok {
		return -1
	}
	return node.Index
}

// Navigate moves focus in the given direction using the spatial graph.
// It returns the new focused scene element index, or -1 if no valid target exists.
func (fe *FocusEngine) Navigate(action Action) int {
	fe.mu.Lock()
	defer fe.mu.Unlock()

	if fe.graph == nil || fe.current < 0 || fe.current >= len(fe.graph.Nodes) {
		return -1
	}

	neighbors := fe.graph.Edges[fe.current]
	best := -1
	bestDist := int32(math.MaxInt32)

	curNode := fe.graph.Nodes[fe.current]
	cx := curNode.Rect.X + curNode.Rect.W/2
	cy := curNode.Rect.Y + curNode.Rect.H/2

	for _, ni := range neighbors {
		if ni < 0 || ni >= len(fe.graph.Nodes) {
			continue
		}
		n := fe.graph.Nodes[ni]
		nx := n.Rect.X + n.Rect.W/2
		ny := n.Rect.Y + n.Rect.H/2
		dx := nx - cx
		dy := ny - cy

		if !isValidDirection(dx, dy, action) {
			continue
		}
		dist := dx*dx + dy*dy
		if dist < bestDist {
			bestDist = dist
			best = ni
		}
	}

	if best >= 0 {
		fe.current = best
		return fe.graph.Nodes[best].Index
	}
	return curNode.Index
}

// Confirm returns the current focused scene element index for activation.
func (fe *FocusEngine) Confirm() int {
	node, _ := fe.Current()
	return node.Index
}

// Persist saves the current focus for the given scene name.
func (fe *FocusEngine) Persist(sceneName string) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if fe.graph != nil && fe.current >= 0 && fe.current < len(fe.graph.Nodes) {
		fe.persistFocus[sceneName] = fe.graph.Nodes[fe.current].Index
	}
}

// Restore reloads persisted focus for the given scene name (no-op if graph not set).
func (fe *FocusEngine) Restore(sceneName string) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if fe.graph == nil || len(fe.graph.Nodes) == 0 {
		return
	}
	if persisted, ok := fe.persistFocus[sceneName]; ok {
		for i, node := range fe.graph.Nodes {
			if node.Index == persisted {
				fe.current = i
				return
			}
		}
	}
	fe.current = 0
}

// Clear removes all persisted focus state.
func (fe *FocusEngine) Clear() {
	fe.mu.Lock()
	fe.persistFocus = make(map[string]int)
	fe.mu.Unlock()
}

// SyncSelected updates the global selectedButtonIndex from the engine's current node.
// This bridges the FocusEngine with the existing rendering and input code.
func (fe *FocusEngine) SyncSelected() {
	node, ok := fe.Current()
	if ok {
		selectedButtonIndex = node.Index
	} else {
		selectedButtonIndex = -1
	}
}

// SetByElementIndex moves focus to the given scene element index.
// It returns true if the element was found in the current graph.
func (fe *FocusEngine) SetByElementIndex(idx int) bool {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if fe.graph == nil {
		return false
	}
	for i, node := range fe.graph.Nodes {
		if node.Index == idx {
			fe.current = i
			return true
		}
	}
	return false
}

// isFocusableElement reports whether an element should participate in the focus graph.
func isFocusableElement(el Element) bool {
	switch el.Type {
	case "button", "input", "searchresults", "dynamiclist", "recent", "favorites", "chat", "themegallery", "toggle":
		return true
	}
	return false
}

// buildSpatialEdges creates a bidirectional spatial graph between nodes.
// Each node connects to its nearest neighbor in each cardinal direction.
func buildSpatialEdges(nodes []FocusNode) map[int][]int {
	edges := make(map[int][]int)
	n := len(nodes)
	if n == 0 {
		return edges
	}

	for i := 0; i < n; i++ {
		edges[i] = []int{}
	}

	// For each pair of nodes, compute the vector from i to j.
	// If j is primarily in one cardinal direction from i, add an edge.
	for i := 0; i < n; i++ {
		ci := nodes[i].Rect.X + nodes[i].Rect.W/2
		di := nodes[i].Rect.Y + nodes[i].Rect.H/2
		for j := i + 1; j < n; j++ {
			cj := nodes[j].Rect.X + nodes[j].Rect.W/2
			dj := nodes[j].Rect.Y + nodes[j].Rect.H/2
			dx := cj - ci
			dy := dj - di
			adx := absI32(dx)
			ady := absI32(dy)

			// Require a clear primary direction (at least 1.5x bias).
			if adx == 0 && ady == 0 {
				continue
			}
			if float64(adx) < 1.5*float64(ady) && float64(ady) < 1.5*float64(adx) {
				continue
			}

			if adx > ady || ady == 0 {
				edges[i] = append(edges[i], j)
				edges[j] = append(edges[j], i)
			} else {
				edges[i] = append(edges[i], j)
				edges[j] = append(edges[j], i)
			}
		}
	}

	// Fallback pass: the strict 1.5x bias rule above drops every edge when two
	// nodes are diagonally offset (e.g. a controls row above a results grid),
	// which traps focus. Guarantee each node keeps at least one neighbor in
	// every cardinal direction where a candidate exists, choosing the nearest
	// one so directional navigation never dead-ends.
	center := func(i int) (int32, int32) {
		return nodes[i].Rect.X + nodes[i].Rect.W/2, nodes[i].Rect.Y + nodes[i].Rect.H/2
	}
	dirs := []struct {
		vx, vy int32 // unit vector of the direction we must find a neighbor for
		idx    int   // index into a per-node hasDir mask
	}{
		{0, -1, 0}, // up
		{0, 1, 1},  // down
		{-1, 0, 2}, // left
		{1, 0, 3},  // right
	}
	for i := 0; i < n; i++ {
		var hasDir [4]bool
		ci, di := center(i)
		for _, j := range edges[i] {
			cj, dj := center(j)
			switch {
			case dj-di < -2:
				hasDir[0] = true
			case dj-di > 2:
				hasDir[1] = true
			case cj-ci < -2:
				hasDir[2] = true
			case cj-ci > 2:
				hasDir[3] = true
			}
		}
		for _, d := range dirs {
			if hasDir[d.idx] {
				continue
			}
			best := -1
			bestDist := int64(math.MaxInt64)
			for j := 0; j < n; j++ {
				if j == i {
					continue
				}
				cj, dj := center(j)
				dx := cj - ci
				dy := dj - di
				if d.vx == 0 {
					if d.vy < 0 && dy >= -2 {
						continue
					}
					if d.vy > 0 && dy <= 2 {
						continue
					}
				} else {
					if d.vx < 0 && dx >= -2 {
						continue
					}
					if d.vx > 0 && dx <= 2 {
						continue
					}
				}
				dist := int64(dx)*int64(dx) + int64(dy)*int64(dy)
				if dist < bestDist {
					bestDist = dist
					best = j
				}
			}
			if best >= 0 {
				edges[i] = append(edges[i], best)
				edges[best] = append(edges[best], i)
			}
		}
	}

	// Deduplicate and sort adjacency lists.
	for i := range edges {
		seen := make(map[int]bool)
		var deduped []int
		for _, j := range edges[i] {
			if !seen[j] && j != i {
				seen[j] = true
				deduped = append(deduped, j)
			}
		}
		sort.Ints(deduped)
		edges[i] = deduped
	}

	return edges
}

// isValidDirection returns true if (dx, dy) is aligned with the requested action.
func isValidDirection(dx, dy int32, action Action) bool {
	switch action {
	case ActionNavigateUp:
		return dy < -2
	case ActionNavigateDown:
		return dy > 2
	case ActionNavigateLeft:
		return dx < -2
	case ActionNavigateRight:
		return dx > 2
	default:
		return false
	}
}

// absI32 returns the absolute value of x.
func absI32(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}
