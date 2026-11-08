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

// Focusable is the contract the focus system needs from anything it can land
// on: where it is, what kind of control it is, and what a log line should call
// it. A scene element and a measured node are two shapes of the same idea, so
// BuildGraph reads them through this interface instead of reaching into their
// fields — an overlay target, a synthetic chrome node or a future control only
// has to answer these three questions to join the graph.
type Focusable interface {
	FocusRect() sdl.Rect
	FocusType() string
	FocusLabel() string
}

var (
	_ Focusable = Element{}
	_ Focusable = FocusNode{}
)

// FocusRect is the element's configured rectangle with the graph's own default
// size for an element that leaves width or height out. It is the single
// definition of that geometry: BuildGraph used to repeat the numbers inline.
func (el Element) FocusRect() sdl.Rect {
	return sdl.Rect{
		X: el.X,
		Y: el.Y,
		W: getElementWidth(el, 1100),
		H: getElementHeight(el, 200),
	}
}

func (el Element) FocusType() string { return el.Type }

// FocusLabel is the text the element shows, falling back to its type so a
// button with no label still names itself in a log line.
func (el Element) FocusLabel() string {
	if el.Text != "" {
		return el.Text
	}
	return el.Type
}

func (n FocusNode) FocusRect() sdl.Rect { return n.Rect }
func (n FocusNode) FocusType() string   { return n.Type }
func (n FocusNode) FocusLabel() string {
	if n.Label != "" {
		return n.Label
	}
	return n.Type
}

// focusNodeFrom adapts a scene element to a graph node through Focusable, so
// the graph never reads element fields directly.
func focusNodeFrom(el Element) FocusNode {
	var f Focusable = el
	return FocusNode{
		Rect:  f.FocusRect(),
		Type:  f.FocusType(),
		Label: f.FocusLabel(),
	}
}

// Chrome focus indices identify the buttons that are not config elements: the
// top-bar back pill, status-bar clock, Apps pill and notification bell. They
// join the focus graph so the D-pad and arrow keys can reach every button on
// screen. ElementIndex reports -1 for "no focus", so chrome uses deeper
// sentinels.
const (
	ChromeBackIndex    = -2
	ChromeClockIndex   = -3
	ChromeStartIndex   = -4
	ChromeBellIndex    = -5
	ChromeWeatherIndex = -6
	ChromeUpdateIndex  = -7
	ChromeMediaIndex   = -8
	ChromeWiFiIndex    = -9
	ChromeBackType     = "back"
	ChromeClockType    = "clock"
	ChromeStartType    = "start"
	ChromeBellType     = "bell"
	ChromeWeatherType  = "weather"
	ChromeUpdateType   = "update"
	ChromeMediaType    = "media"
	ChromeWiFiType     = "wifi"
)

// chromeTier groups focus targets by band. Tier 1 is the top bar (back pill,
// Apps pill, status clock, notification bell); tier 0 is scene content. The
// tiers are hand-wired instead of spatial, and StepChrome moves between them.
// The bottom of the screen is now the hint bar only, so there is no tier 2.
func chromeTier(idx int) int {
	switch idx {
	case ChromeBackIndex, ChromeClockIndex, ChromeStartIndex, ChromeBellIndex, ChromeWeatherIndex, ChromeUpdateIndex, ChromeMediaIndex, ChromeWiFiIndex:
		return 1
	}
	return 0
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
	scene        string         // scene name the installed graph was built for
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
		node := focusNodeFrom(el)
		node.Index = i
		nodes = append(nodes, node)
	}
	// Sort by Y then X for stable ordering.
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Rect.Y != nodes[j].Rect.Y {
			return nodes[i].Rect.Y < nodes[j].Rect.Y
		}
		return nodes[i].Rect.X < nodes[j].Rect.X
	})

	edges := buildSpatialEdges(nodes)

	// Top-bar chrome: the back pill, Apps pill, status clock and notification
	// bell are buttons too and must stay reachable. Their rects are measured
	// while the bar renders. The home screen draws its own focusable clock card
	// and footer instead, so it stays element-only.
	//
	// Chrome is hand-wired instead of spatial: the bar sits above the content at
	// shallow angles whose vectors would hijack Left/Right along the grid rows.
	// The four bar buttons connect left-to-right in their measured order; Up/
	// Down between the content and the bar is routed by StepChrome.
	//
	// Every screen keeps the bar, including the ones that draw no controls of
	// their own (Playtime's badge list, the music player, the terminal): on a
	// pad-only handheld the bar is the only Back, clock and bell those screens
	// have, so it cannot be tap-only. What such a screen must not get is focus
	// *starting* on the bar: SetGraph leaves a bar-only graph unfocused, and
	// screenPadPress steps onto the bar with Up.
	if scene.Layout != "home" {
		var chromeIdx []int
		addNode := func(idx int, rect sdl.Rect, typ, label string) {
			nodes = append(nodes, FocusNode{Index: idx, Rect: rect, Type: typ, Label: label})
			chromeIdx = append(chromeIdx, len(nodes)-1)
		}
		if sceneTitleBackRect.W > 0 {
			addNode(ChromeBackIndex, sceneTitleBackRect, ChromeBackType, "Back")
		}
		// Stale rects from a previous scene must not become phantom targets; the
		// status bar and home header clear theirs when they are not drawn.
		if tb.startRect.W > 0 {
			addNode(ChromeStartIndex, tb.startRect, ChromeStartType, "Menu")
		}
		if statusClockRect.W > 0 {
			addNode(ChromeClockIndex, statusClockRect, ChromeClockType, "Clock")
		}
		if weatherStatusRect.W > 0 {
			addNode(ChromeWeatherIndex, weatherStatusRect, ChromeWeatherType, "Local weather")
		}
		// The signal meter opens the network picker, the same as a tap on it.
		// It joins the bar so the D-pad can reach Wi-Fi (and the network list)
		// without a touchscreen: on the handheld the meter used to be the one
		// drawn control the pad could not select.
		if statusWiFiRect.W > 0 {
			addNode(ChromeWiFiIndex, statusWiFiRect, ChromeWiFiType, "Wi-Fi")
		}
		if nc.bellRect.W > 0 {
			addNode(ChromeBellIndex, nc.bellRect, ChromeBellType, "Notifications")
		}
		// The now-playing chip is a button only while a player is behind it. The
		// idle output chip is drawn in the same spot but names headphones, so it
		// stays out of the focus order rather than becoming a dead stop.
		if statusMediaRect.W > 0 && nowPlayingChipIsButton() {
			addNode(ChromeMediaIndex, statusMediaRect, ChromeMediaType, "Now playing")
		}
		// The "Update" pill only exists while a release is advertised, so it
		// joins the bar for that launch. Home has its own chip entry; this is
		// the same offer on every other screen, reachable the same way.
		if updatePrompt.badge.W > 0 {
			addNode(ChromeUpdateIndex, updatePrompt.badge, ChromeUpdateType, "Update")
		}
		// Cut the spatial edges chrome picked up while the graph was built, so
		// a diagonal vector can never pull Left/Right off a content row.
		chromeSet := map[int]bool{}
		for _, n := range chromeIdx {
			chromeSet[n] = true
			edges[n] = []int{}
		}
		for i := range nodes {
			if chromeSet[i] {
				continue
			}
			kept := edges[i][:0]
			for _, j := range edges[i] {
				if !chromeSet[j] {
					kept = append(kept, j)
				}
			}
			edges[i] = kept
		}
		sort.Slice(chromeIdx, func(i, j int) bool {
			return nodes[chromeIdx[i]].Rect.X < nodes[chromeIdx[j]].Rect.X
		})
		for i := 0; i+1 < len(chromeIdx); i++ {
			a, b := chromeIdx[i], chromeIdx[i+1]
			edges[a] = append(edges[a], b)
			edges[b] = append(edges[b], a)
		}
	}

	return &FocusGraph{Nodes: nodes, Edges: edges}
}

// SetGraph installs a new focus graph and restores persisted focus if available.
// Reinstalling a graph for the same scene keeps the currently focused node
// (matched by element index) instead of resetting: key handlers rebuild the
// graph on every press to pick up dynamic buttons and the measured top-bar
// chrome rects, and losing the user's place on each reinstall trapped
// navigation on the first buttons.
func (fe *FocusEngine) SetGraph(sceneName string, graph *FocusGraph) {
	fe.mu.Lock()
	defer fe.mu.Unlock()

	keepIndex, keep := 0, false
	if fe.scene == sceneName && fe.graph != nil && fe.current >= 0 && fe.current < len(fe.graph.Nodes) {
		keepIndex, keep = fe.graph.Nodes[fe.current].Index, true
	}

	fe.graph = graph
	fe.scene = sceneName
	fe.current = -1

	if graph != nil && len(graph.Nodes) > 0 {
		if keep {
			for i, node := range graph.Nodes {
				if node.Index == keepIndex {
					fe.current = i
					break
				}
			}
		}
		if fe.current == -1 {
			// Restore persisted focus or default to first node.
			if persisted, ok := fe.persistFocus[sceneName]; ok {
				for i, node := range graph.Nodes {
					if node.Index == persisted {
						fe.current = i
						break
					}
				}
			}
		}
		if fe.current == -1 {
			fe.current = firstSceneNode(graph)
		}
	}
}

// firstSceneNode picks the node a fresh screen starts on: the first content
// node, left-to-right then top-to-bottom, or -1 when the screen has no controls
// of its own.
//
// Starting on node 0 would put the focus on the first top-bar button for a
// screen whose only nodes are the bar, which is wrong twice over: the ring says
// the bar holds the press, and a module that binds the same buttons (the music
// player's A, the terminal's arrows) still gets them. A screen with nothing to
// focus starts with no focus, so the bar is entered on purpose with Up.
func firstSceneNode(graph *FocusGraph) int {
	if graph == nil {
		return -1
	}
	for i, node := range graph.Nodes {
		if chromeTier(node.Index) == 0 {
			return i
		}
	}
	return -1
}

// firstChromeNode returns the index into graph.Nodes of the bar's leftmost
// button, in the order the bar draws them, or -1 when the bar has no nodes.
func (fe *FocusEngine) firstChromeNode() int {
	if fe.graph == nil {
		return -1
	}
	for i, node := range fe.graph.Nodes {
		if chromeTier(node.Index) == 1 {
			return i
		}
	}
	return -1
}

// FocusChromeBar moves the focus onto the top bar's leftmost button and
// reports whether the bar had one. It is how a screen with no controls of its
// own reaches the header: there is nothing above to step up from, so Up on an
// unfocused screen goes straight to the bar.
func (fe *FocusEngine) FocusChromeBar() bool {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	i := fe.firstChromeNode()
	if i < 0 {
		return false
	}
	fe.current = i
	return true
}

// ClearFocus drops the focus entirely (ElementIndex reports -1). It hands the
// pad back to a screen that draws no controls: Down from the bar on such a
// screen has nowhere to step, so without this the ring would sit on a bar
// button the user can no longer leave.
func (fe *FocusEngine) ClearFocus() {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.current = -1
}

// SceneName returns the scene the installed graph was built for ("" if none).
// Callers use it to avoid reinstalling the graph on every key press: SetGraph
// restores persisted focus, so reinstalling resets navigation progress.
func (fe *FocusEngine) SceneName() string {
	fe.mu.RLock()
	defer fe.mu.RUnlock()
	return fe.scene
}

// NodeCount returns how many nodes the installed graph has (0 if none).
func (fe *FocusEngine) NodeCount() int {
	fe.mu.RLock()
	defer fe.mu.RUnlock()
	if fe.graph == nil {
		return 0
	}
	return len(fe.graph.Nodes)
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

	// A D-pad mode chosen in Settings → Controls walks the screen's own
	// controls (navmode.go); Classic, the top bar and Home keep the graph.
	if next, handled := fe.navmodeNavigate(action); handled {
		if next >= 0 {
			fe.current = next
		}
		return fe.graph.Nodes[fe.current].Index
	}

	neighbors := fe.graph.Edges[fe.current]
	best := -1
	bestDist := int64(math.MaxInt64)

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
		// Prefer a target aligned with the requested direction over a closer
		// diagonal one. Otherwise Left from a right-hand field can jump to an
		// input in the row above, making its neighboring field unreachable.
		primary, cross := int64(dx), int64(dy)
		if action == ActionNavigateUp || action == ActionNavigateDown {
			primary, cross = cross, primary
		}
		dist := primary*primary + 9*cross*cross
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

// StepChrome bridges the focus grid and the hand-wired chrome buttons. The bar
// sits above the content area at a shallow angle the spatial cones reject, so
// Up dead-ending at the grid's top row steps onto the nearest top-bar button
// and Down from the bar steps back into the content. It reports the
// element/chrome index to move to and whether one exists.
func (fe *FocusEngine) StepChrome(action Action) (int, bool) {
	fe.mu.RLock()
	defer fe.mu.RUnlock()
	if fe.graph == nil || fe.current < 0 || fe.current >= len(fe.graph.Nodes) {
		return 0, false
	}
	cur := fe.graph.Nodes[fe.current]
	cx := int64(cur.Rect.X + cur.Rect.W/2)
	cy := int64(cur.Rect.Y + cur.Rect.H/2)
	curTier := chromeTier(cur.Index)

	// Up targets the top bar; Down leaves it for the content.
	var wantTier int
	switch action {
	case ActionNavigateUp:
		wantTier = 1
	case ActionNavigateDown:
		wantTier = 0
	default:
		return 0, false
	}
	if curTier == wantTier {
		return 0, false
	}

	best, bestDist := -1, int64(math.MaxInt64)
	for i, n := range fe.graph.Nodes {
		if chromeTier(n.Index) != wantTier {
			continue
		}
		dx := int64(n.Rect.X+n.Rect.W/2) - cx
		dy := int64(n.Rect.Y+n.Rect.H/2) - cy
		if action == ActionNavigateUp && dy >= -2 {
			continue
		}
		if action == ActionNavigateDown && dy <= 2 {
			continue
		}
		if d := dx*dx + dy*dy; d < bestDist {
			bestDist, best = d, i
		}
	}
	if best < 0 {
		return 0, false
	}
	return fe.graph.Nodes[best].Index, true
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
	case "button", "input", "searchresults", "dynamiclist", "recent", "favorites", "chat", "themegallery", "toggle", "textbrowser", "shortslist", "packagelist":
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
			dx := cj - ci
			dy := dj - di
			// Classify by the axis this edge was built along, not by the sign
			// of the raw offset. An edge whose dominant axis is horizontal can
			// still carry a small vertical offset; counting that as "up" used
			// to suppress the fallback below, leaving a node with no real
			// neighbour in that direction — a control off to the side then
			// answered Up from a control that had something directly above it.
			if absI32(dx) >= absI32(dy) {
				if dx < 0 {
					hasDir[2] = true
				} else {
					hasDir[3] = true
				}
			} else {
				if dy < 0 {
					hasDir[0] = true
				} else {
					hasDir[1] = true
				}
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
