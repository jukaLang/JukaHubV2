package main

// navmode.go — D-pad navigation modes.
//
// The focus graph (focus.go) walks a screen spatially: from the focused
// control to the nearest neighbour roughly in the pressed direction. That
// suits a screen laid out by hand and stays the default, "Classic". The ideas
// list asked for a way of walking per kind of content, so three more exist:
//
//	Grid      strict rows and columns: Left/Right stay in the row and stop at
//	          its ends, Up/Down keep the column. With Wrap on, Right at the end
//	          of a row goes on to the next row and Left back to the previous.
//	List      one item after another in reading order: Up/Down step, Left/
//	          Right jump a page. With Wrap on, Down from the last item goes
//	          back to the first.
//	Freeform  the nearest control whose centre lies within 45° of the pressed
//	          direction; on a tie, the one less to the side.
//
// Each kind of content is an area with a default: the game library walks as
// a Grid, Files and the media lists as a List, Settings Freeform and every
// other screen Classic. Settings → Controls overrides each area.
//
// The library, the lists and Settings draw their content with their own
// widgets and walk it with their own code, which is what the defaults
// describe; those widgets follow an override through navmodeMove. The focus
// graph — the buttons a config screen or a user-built screen is made of —
// keeps the spatial walk it was laid out for until the user picks a mode for
// its area on purpose. Home and the top bar always keep their own wiring.

import (
	"github.com/veandco/go-sdl2/sdl"
)

// navMode is a way of walking with the D-pad.
type navMode uint8

const (
	navModeClassic navMode = iota
	navModeGrid
	navModeList
	navModeFreeform
)

// navmodeModes lists the modes in the order the Settings carousel shows.
var navmodeModes = []navMode{navModeClassic, navModeGrid, navModeList, navModeFreeform}

// navArea is a kind of content with a mode of its own.
type navArea uint8

const (
	navAreaLibrary navArea = iota
	navAreaLists
	navAreaSettings
	navAreaScreens
	navAreaCount
)

// navmodeAreaInfo describes one area: its settings key, its row's label and
// its default mode.
type navmodeAreaInfo struct {
	key   string
	label string
	def   navMode
}

var navmodeAreas = [navAreaCount]navmodeAreaInfo{
	{key: "navModeLibrary", label: "D-pad in the library", def: navModeGrid},
	{key: "navModeLists", label: "D-pad in lists", def: navModeList},
	{key: "navModeSettings", label: "D-pad in Settings", def: navModeFreeform},
	{key: "navModeScreens", label: "D-pad elsewhere", def: navModeClassic},
}

const (
	// navmodeKeyWrap is the Wrap switch's settings key.
	navmodeKeyWrap = "navModeWrap"
	// navmodeListPage is how far Left/Right jump in List mode.
	navmodeListPage = 5
)

// navmodeState is the user's choices: set[a] is false while area a uses its
// default. Main thread only.
var navmodeState struct {
	mode [navAreaCount]navMode
	set  [navAreaCount]bool
	wrap bool
}

// navmodeName is the stored spelling of a mode.
func navmodeName(m navMode) string {
	switch m {
	case navModeGrid:
		return "grid"
	case navModeList:
		return "list"
	case navModeFreeform:
		return "freeform"
	}
	return "classic"
}

// navmodeLabel is a mode as Settings shows it.
func navmodeLabel(m navMode) string {
	switch m {
	case navModeGrid:
		return "Grid"
	case navModeList:
		return "List"
	case navModeFreeform:
		return "Freeform"
	}
	return "Classic"
}

// navmodeDescription says in a line what a mode does.
func navmodeDescription(m navMode) string {
	switch m {
	case navModeGrid:
		return "Rows and columns: Left and Right stay in the row"
	case navModeList:
		return "One item after another; Left and Right jump a page"
	case navModeFreeform:
		return "The nearest control within 45° of the press"
	}
	return "The nearest control in the pressed direction, as laid out"
}

// navmodeParse reads a stored mode name.
func navmodeParse(s string) (navMode, bool) {
	switch inmapNormName(s) {
	case "classic", "auto", "spatial":
		return navModeClassic, true
	case "grid":
		return navModeGrid, true
	case "list":
		return navModeList, true
	case "freeform", "free":
		return navModeFreeform, true
	}
	return navModeClassic, false
}

// navmodeLoadSettings reads the choices from the config (startup and every
// reload; inmapLoadSettings calls it).
func navmodeLoadSettings(config *Config) {
	navmodeState.set = [navAreaCount]bool{}
	navmodeState.wrap = false
	if config == nil {
		return
	}
	for a := range navmodeAreas {
		s, ok := customString(config.Variables.Custom, navmodeAreas[a].key)
		if !ok {
			continue
		}
		if m, ok := navmodeParse(s); ok {
			navmodeState.mode[a], navmodeState.set[a] = m, true
		}
	}
	if v, ok := customValue(config.Variables.Custom, navmodeKeyWrap); ok {
		navmodeState.wrap = coerceBool(v)
	}
}

// navmodeFor is the mode an area walks in: the user's choice, else the
// area's default. Widgets that walk their own content call it.
func navmodeFor(a navArea) navMode {
	if a >= navAreaCount {
		return navModeClassic
	}
	if navmodeState.set[a] {
		return navmodeState.mode[a]
	}
	return navmodeAreas[a].def
}

// navmodeWrapEnabled reports whether Grid and List wrap at their edges.
func navmodeWrapEnabled() bool {
	return navmodeState.wrap
}

// navmodeAreaForScene says which area a screen belongs to.
func navmodeAreaForScene(name string) navArea {
	switch name {
	case "Games", "Emulator", "Emulators":
		return navAreaLibrary
	case "FileExplorer", "Tube", "MusicPlayer", "Podcasts", "IPStream", "LiveTV",
		"Recordings", "Favorites", "Shorts", "Tickers":
		return navAreaLists
	case "Settings", "SettingsGeneral", "SettingsAppearance", "SettingsSmart",
		"ThemePresets", "Plugins", vkSettingsSceneName:
		return navAreaSettings
	}
	return navAreaScreens
}

// navmodeFocusMode is the mode the focus graph walks a screen in: the mode
// the user chose for its area, else Classic (see the file comment).
func navmodeFocusMode(sceneName string) navMode {
	a := navmodeAreaForScene(sceneName)
	if navmodeState.set[a] {
		return navmodeState.mode[a]
	}
	return navModeClassic
}

// navmodeSetArea stores the choice for one area: "" puts it back on its
// default.
func navmodeSetArea(config *Config, a navArea, value string) {
	if a >= navAreaCount {
		return
	}
	if m, ok := navmodeParse(value); ok && value != "" {
		navmodeState.mode[a], navmodeState.set[a] = m, true
	} else {
		navmodeState.set[a] = false
		value = ""
	}
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[navmodeAreas[a].key] = value
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// navmodeSetWrap stores the Wrap switch.
func navmodeSetWrap(config *Config, on bool) {
	navmodeState.wrap = on
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[navmodeKeyWrap] = on
	saveUserConfigDebounced(userConfigFromVariables(config))
}

// ── Walking ──────────────────────────────────────────────────────────────

// navmodeOpts tunes a walk: whether Grid and List wrap, and List's page.
type navmodeOpts struct {
	wrap bool
	page int
}

// navmodeCentre is a rect's centre.
func navmodeCentre(r sdl.Rect) (int32, int32) {
	return r.X + r.W/2, r.Y + r.H/2
}

// navmodeRows groups rects into visual rows, top to bottom, each row left to
// right, as indices into rects. A rect joins a row when its centre lies
// within the height of the row's first rect.
func navmodeRows(rects []sdl.Rect) [][]int {
	order := make([]int, len(rects))
	for i := range order {
		order[i] = i
	}
	// Insertion sort by centre, top to bottom then left to right: a screen
	// has a few dozen controls at most.
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && navmodeBefore(rects[order[j]], rects[order[j-1]]); j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	var rows [][]int
	for _, idx := range order {
		_, cy := navmodeCentre(rects[idx])
		if n := len(rows); n > 0 {
			first := rects[rows[n-1][0]]
			if cy >= first.Y && cy < first.Y+max32(first.H, 1) {
				rows[n-1] = append(rows[n-1], idx)
				continue
			}
		}
		rows = append(rows, []int{idx})
	}
	for _, row := range rows {
		for i := 1; i < len(row); i++ {
			for j := i; j > 0 && rects[row[j]].X < rects[row[j-1]].X; j-- {
				row[j], row[j-1] = row[j-1], row[j]
			}
		}
	}
	return rows
}

// navmodeBefore orders rects by centre: higher first, then further left.
func navmodeBefore(a, b sdl.Rect) bool {
	ax, ay := navmodeCentre(a)
	bx, by := navmodeCentre(b)
	if ay != by {
		return ay < by
	}
	return ax < bx
}

// navmodeLocate finds an index's row and column.
func navmodeLocate(rows [][]int, idx int) (int, int) {
	for r, row := range rows {
		for c, i := range row {
			if i == idx {
				return r, c
			}
		}
	}
	return -1, -1
}

// navmodeMove walks rects from cur in direction (dx, dy) — one of them ±1,
// the other 0 — under mode m. It returns the next index and whether there is
// one. Classic always reports false: the caller keeps its own spatial walk.
func navmodeMove(m navMode, rects []sdl.Rect, cur, dx, dy int, opts navmodeOpts) (int, bool) {
	if cur < 0 || cur >= len(rects) || (dx == 0 && dy == 0) {
		return cur, false
	}
	switch m {
	case navModeGrid:
		return navmodeGridMove(rects, cur, dx, dy, opts.wrap)
	case navModeList:
		return navmodeListMove(rects, cur, dx, dy, opts)
	case navModeFreeform:
		return navmodeFreeformMove(rects, cur, dx, dy)
	}
	return cur, false
}

// navmodeGridMove is Grid: Left/Right inside the row, Up/Down to the same
// column of the next row (its last item when that row is shorter).
func navmodeGridMove(rects []sdl.Rect, cur, dx, dy int, wrap bool) (int, bool) {
	rows := navmodeRows(rects)
	r, c := navmodeLocate(rows, cur)
	if r < 0 {
		return cur, false
	}
	if dx != 0 {
		if n := c + dx; n >= 0 && n < len(rows[r]) {
			return rows[r][n], true
		}
		if !wrap {
			return cur, false
		}
		if dx > 0 && r+1 < len(rows) {
			return rows[r+1][0], true
		}
		if dx < 0 && r > 0 {
			prev := rows[r-1]
			return prev[len(prev)-1], true
		}
		return cur, false
	}
	t := r + dy
	if t < 0 || t >= len(rows) {
		return cur, false
	}
	col := c
	if col >= len(rows[t]) {
		col = len(rows[t]) - 1
	}
	return rows[t][col], true
}

// navmodeListMove is List: reading order, Up/Down by one, Left/Right by a
// page (stopping at the ends).
func navmodeListMove(rects []sdl.Rect, cur, dx, dy int, opts navmodeOpts) (int, bool) {
	var order []int
	for _, row := range navmodeRows(rects) {
		order = append(order, row...)
	}
	pos := -1
	for i, idx := range order {
		if idx == cur {
			pos = i
			break
		}
	}
	if pos < 0 {
		return cur, false
	}
	n := pos + dy
	if dx != 0 {
		page := opts.page
		if page < 1 {
			page = navmodeListPage
		}
		n = pos + dx*page
		if n < 0 {
			n = 0
		}
		if n >= len(order) {
			n = len(order) - 1
		}
	}
	if n >= len(order) {
		// Down from the last item: back to the first with Wrap on. Up from
		// the first never wraps, so it can still reach the top bar.
		if !opts.wrap {
			return cur, false
		}
		n = 0
	}
	if n < 0 || order[n] == cur {
		return cur, false
	}
	return order[n], true
}

// navmodeFreeformMove is Freeform: the nearest rect whose centre lies within
// 45° of the direction; on a tie the one less to the side, then the first.
func navmodeFreeformMove(rects []sdl.Rect, cur, dx, dy int) (int, bool) {
	cx, cy := navmodeCentre(rects[cur])
	best := -1
	var bestDist, bestCross int64
	for i, r := range rects {
		if i == cur {
			continue
		}
		x, y := navmodeCentre(r)
		ox, oy := int64(x-cx), int64(y-cy)
		primary := ox*int64(dx) + oy*int64(dy)
		cross := ox*int64(dy) + oy*int64(dx)
		if cross < 0 {
			cross = -cross
		}
		if primary <= 2 || cross > primary {
			continue
		}
		dist := ox*ox + oy*oy
		if best < 0 || dist < bestDist || (dist == bestDist && cross < bestCross) {
			best, bestDist, bestCross = i, dist, cross
		}
	}
	if best < 0 {
		return cur, false
	}
	return best, true
}

// navmodeActionDir turns a navigation action into a direction.
func navmodeActionDir(action Action) (int, int) {
	switch action {
	case ActionNavigateUp:
		return 0, -1
	case ActionNavigateDown:
		return 0, 1
	case ActionNavigateLeft:
		return -1, 0
	case ActionNavigateRight:
		return 1, 0
	}
	return 0, 0
}

// navmodeNavigate is FocusEngine.Navigate's hook. Under a mode other than
// Classic it walks the screen's own controls and reports handled, with the
// node to move to (-1 when the mode stops at an edge, so moveSelection's
// bridge to the top bar still runs). The top-bar chrome, Home, and screens
// left on Classic report not handled and keep the spatial graph; so does a
// Freeform press with nothing in its cone. fe.mu is held by the caller.
func (fe *FocusEngine) navmodeNavigate(action Action) (int, bool) {
	mode := navmodeFocusMode(fe.scene)
	if mode == navModeClassic || homeLayoutActive || fe.graph == nil ||
		fe.current < 0 || fe.current >= len(fe.graph.Nodes) {
		return -1, false
	}
	if chromeTier(fe.graph.Nodes[fe.current].Index) != 0 {
		return -1, false
	}
	dx, dy := navmodeActionDir(action)
	if dx == 0 && dy == 0 {
		return -1, false
	}
	rects := make([]sdl.Rect, 0, len(fe.graph.Nodes))
	nodes := make([]int, 0, len(fe.graph.Nodes))
	at := -1
	for i, n := range fe.graph.Nodes {
		if chromeTier(n.Index) != 0 {
			continue
		}
		if i == fe.current {
			at = len(rects)
		}
		rects = append(rects, n.Rect)
		nodes = append(nodes, i)
	}
	next, ok := navmodeMove(mode, rects, at, dx, dy, navmodeOpts{wrap: navmodeState.wrap, page: navmodeListPage})
	if ok {
		return nodes[next], true
	}
	if mode == navModeFreeform {
		return -1, false
	}
	return -1, true
}

// navmodeSettingsRow is one area's row in Settings → Controls: a carousel of
// "Default · <mode>" and the four modes.
func navmodeSettingsRow(a navArea) setRow {
	info := navmodeAreas[a]
	options := func(*Config) []setOption {
		out := make([]setOption, 0, len(navmodeModes)+1)
		out = append(out, setOption{Value: "", Label: "Default · " + navmodeLabel(info.def)})
		for _, m := range navmodeModes {
			out = append(out, setOption{Value: navmodeName(m), Label: navmodeLabel(m)})
		}
		return out
	}
	return setRow{
		ID: info.key, Label: info.label, Kind: setKindChoice,
		Detail:  func(*Config) string { return navmodeDescription(navmodeFor(a)) },
		Options: options,
		Current: func(*Config) int {
			if !navmodeState.set[a] {
				return 0
			}
			for i, m := range navmodeModes {
				if m == navmodeState.mode[a] {
					return i + 1
				}
			}
			return 0
		},
		Choose: func(c *Config, value string) {
			navmodeSetArea(c, a, value)
			setToast(info.label + ": " + navmodeLabel(navmodeFor(a)))
		},
	}
}
