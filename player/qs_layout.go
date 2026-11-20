package main

// qs_layout.go — Quick Settings' layouts: which buttons the panel shows, in
// what order and at what size, and how each layout is walked.
//
// Select on Home opens the panel (so do F2 on a Brick, Start → Quick Settings
// and Ctrl+Q). It has three layouts:
//
//   - Tiles, the default: a four-column grid of glass tiles, like a phone's
//     control centre. A switch lights up in its own colour while it is on, a
//     slider shows its level, a mode row its value;
//   - Compact: a six-column grid of icon buttons that fits everything on one
//     screen; the line under the title names the focused button and its state;
//   - List: the one-column list the panel always had.
//
// L2 (Q on a keyboard) edits the panel (qs_edit.go): L1/R1 switch the layout,
// A picks a button up and the D-pad carries it, X resizes a tile a cell at a
// time — Edit Home's widget grid, one more time — and Y hides a button or
// shows it again.
//
// In the grid layouts the D-pad only moves the focus. A on a slider (Volume,
// Brightness) starts adjusting it, the way the PS5's control centre does:
// Left/Right then move the level and A or B end. L1/R1 move a focused
// slider's level, or step a focused mode row, at any time. The List layout
// keeps its keys: Left/Right adjust the focused row, L1/R1 page.
//
// The arrangement is one ordered list the three layouts share (Custom
// "quickSettingsTiles"): each button's id with an optional "@WxH" size in
// grid cells and a leading "-" on a hidden one, joined by ";" —
// "wifi;bluetooth;volume@4x1;-lanplay;…". Blank is the default arrangement. A
// button missing from a stored list (one a later build adds) goes back where
// the default list has it, so an update never hides a new switch. The layout
// is Custom "quickSettingsLayout": tiles, compact or list.

import (
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	qsLayoutKey = "quickSettingsLayout"
	qsTilesKey  = "quickSettingsTiles"

	qsLayoutTiles   = "tiles"
	qsLayoutCompact = "compact"
	qsLayoutList    = "list"

	// The grid layouts' widths in cells, and the tallest a tile can be.
	qsTilesCols   = 4
	qsCompactCols = 6
	qsTileMaxH    = 2
)

// qsLayoutChoices is the order L1/R1 step through and Edit's strip shows.
var qsLayoutChoices = [...]string{qsLayoutTiles, qsLayoutCompact, qsLayoutList}

// qsLayoutLabel is what the strip and the toasts call a layout.
func qsLayoutLabel(name string) string {
	switch name {
	case qsLayoutCompact:
		return "Compact"
	case qsLayoutList:
		return "List"
	}
	return "Tiles"
}

// normalizeQSLayout reads a stored layout name; anything unknown is Tiles.
func normalizeQSLayout(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case qsLayoutCompact:
		return qsLayoutCompact
	case qsLayoutList, "classic":
		return qsLayoutList
	}
	return qsLayoutTiles
}

// qsLayoutCurrent is the layout in force. It runs every frame the panel is
// up, so the exact key is a map lookup and a type assertion.
func qsLayoutCurrent(config *Config) string {
	if config == nil || config.Variables.Custom == nil {
		return qsLayoutTiles
	}
	v, ok := config.Variables.Custom[qsLayoutKey]
	if !ok || v == nil {
		return qsLayoutTiles
	}
	if s, isStr := v.(string); isStr {
		return normalizeQSLayout(s)
	}
	s, _ := customString(config.Variables.Custom, qsLayoutKey)
	return normalizeQSLayout(s)
}

// qsLayoutIndex is the layout's place on qsLayoutChoices.
func qsLayoutIndex(name string) int {
	for i, n := range qsLayoutChoices {
		if n == name {
			return i
		}
	}
	return 0
}

// qsLayoutCols is a layout's grid width in cells; 0 for the list.
func qsLayoutCols(name string) int {
	switch name {
	case qsLayoutTiles:
		return qsTilesCols
	case qsLayoutCompact:
		return qsCompactCols
	}
	return 0
}

// qsLayoutSet stores the layout through the path every Settings row uses.
func qsLayoutSet(config *Config, name string) {
	if config != nil {
		setStoreString(config, qsLayoutKey, normalizeQSLayout(name))
	}
}

// ── The buttons ───────────────────────────────────────────────────────────

// qsKind is which of the panel's row types a button is.
type qsKind uint8

const (
	qsKindToggle qsKind = iota
	qsKindSlider
	qsKindLink
	qsKindCycle
)

// qsTileSpec is one button the panel can show: its stored id, its row in
// the panel's state (the switch's action, the slider's action, the link's
// action or the cycle row's index), its glyph (icondefs.go) and the size it
// has by default. A zero Size is one cell.
type qsTileSpec struct {
	ID     string
	Kind   qsKind
	Toggle qsAction
	Slider string
	Link   qsLinkAction
	Cycle  int
	Glyph  string
	Size   homeTileSize
}

// qsTileSpecs is every button, in the default arrangement's order: the radios
// first, then the two levels side by side, the everyday switches, the mode
// rows two cells wide, the rest of the switches, and the pages the panel
// opens. Buttons a device does not have (Bluetooth Devices without
// bluetoothctl, Auto power off without an RTC wake) keep their place in the
// stored list and are simply not drawn there.
var qsTileSpecs = [...]qsTileSpec{
	{ID: "wifi", Kind: qsKindToggle, Toggle: qsActionWifi, Glyph: "wifi"},
	{ID: "bluetooth", Kind: qsKindToggle, Toggle: qsActionBluetooth, Glyph: "bluetooth"},
	{ID: "airplane", Kind: qsKindToggle, Toggle: qsActionAirplane, Glyph: "airplane"},
	{ID: "vpn", Kind: qsKindToggle, Toggle: qsActionVPN, Glyph: "shield"},
	{ID: "volume", Kind: qsKindSlider, Slider: "volume", Glyph: "volume", Size: homeTileSize{W: 2, H: 1}},
	{ID: "brightness", Kind: qsKindSlider, Slider: "brightness", Glyph: "sun", Size: homeTileSize{W: 2, H: 1}},
	{ID: "lowpower", Kind: qsKindToggle, Toggle: qsActionLowPower, Glyph: "battery"},
	{ID: "quiet", Kind: qsKindToggle, Toggle: qsActionQuiet, Glyph: "moon"},
	{ID: "music", Kind: qsKindToggle, Toggle: qsActionBgMusic, Glyph: "music"},
	{ID: "magnifier", Kind: qsKindToggle, Toggle: qsActionMagnifier, Glyph: "search"},
	// Record screen, on a handheld that can (screenrec.go).
	{ID: "screenrec", Kind: qsKindToggle, Toggle: qsActionScreenRec, Glyph: "video"},
	{ID: "powermode", Kind: qsKindCycle, Cycle: qsCycleRowPowerMode, Glyph: "bolt", Size: homeTileSize{W: 2, H: 1}},
	{ID: "lock", Kind: qsKindCycle, Cycle: qsCycleScreensaverRow, Glyph: "lock", Size: homeTileSize{W: 2, H: 1}},
	{ID: "spatial", Kind: qsKindCycle, Cycle: qsCycleRowSpatialMode, Glyph: "radar", Size: homeTileSize{W: 2, H: 1}},
	{ID: "ledmode", Kind: qsKindCycle, Cycle: qsCycleRowLED, Glyph: "sparkles", Size: homeTileSize{W: 2, H: 1}},
	{ID: "ledsync", Kind: qsKindToggle, Toggle: qsActionLedSync, Glyph: "sync"},
	{ID: "motion", Kind: qsKindToggle, Toggle: qsActionReducedMotion, Glyph: "speed"},
	{ID: "fullscreen", Kind: qsKindToggle, Toggle: qsActionFullscreen, Glyph: "fullscreen"},
	{ID: "lanplay", Kind: qsKindToggle, Toggle: qsActionNetplay, Glyph: "users"},
	{ID: "experience", Kind: qsKindCycle, Cycle: qsCycleRowXPack, Glyph: "theme", Size: homeTileSize{W: 2, H: 1}},
	{ID: "autooff", Kind: qsKindCycle, Cycle: qsCycleRowAutoOff, Glyph: "clock", Size: homeTileSize{W: 2, H: 1}},
	{ID: "networks", Kind: qsKindLink, Link: qsLinkWiFi, Glyph: "network"},
	{ID: "btdevices", Kind: qsKindLink, Link: qsLinkBluetooth, Glyph: "devices"},
	{ID: "ledstyle", Kind: qsKindLink, Link: qsLinkLEDPanel, Glyph: "palette"},
	{ID: "legend", Kind: qsKindLink, Link: qsLinkControls, Glyph: "gamepad"},
	// The Stopwatch app, its line the running clock (stopwatch.go).
	{ID: "stopwatch", Kind: qsKindLink, Link: qsLinkStopwatch, Glyph: "stopwatch"},
	// Controller Mode's page (ctlmode_ui.go).
	{ID: "ctlmode", Kind: qsKindLink, Link: qsLinkControllerMode, Glyph: "gamepad"},
	{ID: "closeall", Kind: qsKindLink, Link: qsLinkCloseAll, Glyph: "close"},
	{ID: "power", Kind: qsKindLink, Link: qsLinkPower, Glyph: "power"},
}

// qsSpecIndex finds a spec by id.
var qsSpecIndex = func() map[string]int {
	m := make(map[string]int, len(qsTileSpecs))
	for i, s := range qsTileSpecs {
		m[s.ID] = i
	}
	return m
}()

// qsSpecFor is the spec with this id.
func qsSpecFor(id string) (qsTileSpec, bool) {
	i, ok := qsSpecIndex[id]
	if !ok {
		return qsTileSpec{}, false
	}
	return qsTileSpecs[i], true
}

// qsSpecDefaultSize is the size a button has until it is resized. A size
// equal to it is stored without "@WxH", so these values are part of the
// stored form.
func qsSpecDefaultSize(spec qsTileSpec) homeTileSize {
	if spec.Size.W > 0 && spec.Size.H > 0 {
		return spec.Size
	}
	return homeTileSize{W: 1, H: 1}
}

// qsSizeLimits are the smallest and the largest size a button can take on a
// grid cols wide: one cell up to the grid's width and two rows; a slider
// needs two cells across for its bar.
func qsSizeLimits(spec qsTileSpec, cols int) (lo, hi homeTileSize) {
	lo = homeTileSize{W: 1, H: 1}
	if spec.Kind == qsKindSlider {
		lo.W = 2
	}
	if cols < lo.W {
		cols = lo.W
	}
	return lo, homeTileSize{W: cols, H: qsTileMaxH}
}

// qsSnapSize keeps a size inside the button's limits on a grid cols wide.
func qsSnapSize(spec qsTileSpec, s homeTileSize, cols int) homeTileSize {
	lo, hi := qsSizeLimits(spec, cols)
	return homeTileSize{W: homeClampInt(s.W, lo.W, hi.W), H: homeClampInt(s.H, lo.H, hi.H)}
}

// qsResolve is where the button sits in the panel's state, if this device
// has it.
func qsResolve(spec qsTileSpec) (int, bool) {
	switch spec.Kind {
	case qsKindToggle:
		for i := range qs.toggles {
			if qs.toggles[i].Action == spec.Toggle {
				return i, true
			}
		}
	case qsKindSlider:
		for i := range qs.sliders {
			if qs.sliders[i].Action == spec.Slider {
				return i, true
			}
		}
	case qsKindLink:
		for i := range qs.links {
			if qs.links[i].Action == spec.Link {
				return i, true
			}
		}
	case qsKindCycle:
		if spec.Cycle >= 0 && spec.Cycle < len(qs.cycles) {
			return spec.Cycle, true
		}
	}
	return 0, false
}

// ── The stored arrangement ────────────────────────────────────────────────

// qsTileEntry is one button on the arrangement.
type qsTileEntry struct {
	ID     string
	Size   homeTileSize
	Hidden bool
}

// qsDefaultTiles is a fresh copy of the default arrangement.
func qsDefaultTiles() []qsTileEntry {
	out := make([]qsTileEntry, 0, len(qsTileSpecs))
	for _, s := range qsTileSpecs {
		out = append(out, qsTileEntry{ID: s.ID, Size: qsSpecDefaultSize(s)})
	}
	return out
}

// parseQSTiles reads the stored arrangement. Blank (or nothing usable) is the
// default; unknown ids and repeats are dropped; buttons the text does not
// name are put back where the default list has them (qsFillMissing).
func parseQSTiles(s string) []qsTileEntry {
	s = strings.TrimSpace(s)
	if s == "" {
		return qsDefaultTiles()
	}
	out := make([]qsTileEntry, 0, len(qsTileSpecs))
	seen := make(map[string]bool, len(qsTileSpecs))
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		hidden := false
		if strings.HasPrefix(part, "-") {
			hidden = true
			part = strings.TrimSpace(part[1:])
		}
		id, size := part, ""
		if i := strings.IndexByte(part, '@'); i >= 0 {
			id, size = part[:i], part[i+1:]
		}
		id = strings.ToLower(strings.TrimSpace(id))
		spec, ok := qsSpecFor(id)
		if !ok || seen[id] {
			continue
		}
		e := qsTileEntry{ID: id, Size: qsSpecDefaultSize(spec), Hidden: hidden}
		if sz, ok := parseHomeTileSize(size); ok {
			// Snapped to the widest grid, so a size made on Compact survives.
			e.Size = qsSnapSize(spec, sz, qsCompactCols)
		}
		seen[id] = true
		out = append(out, e)
	}
	if len(out) == 0 {
		return qsDefaultTiles()
	}
	return qsFillMissing(out, seen)
}

// qsFillMissing inserts every button the list does not name after the
// nearest button the default list has before it (first when there is none).
func qsFillMissing(out []qsTileEntry, seen map[string]bool) []qsTileEntry {
	for k, spec := range qsTileSpecs {
		if seen[spec.ID] {
			continue
		}
		at := 0
		for p := k - 1; p >= 0; p-- {
			if i := qsTilesIndex(out, qsTileSpecs[p].ID); i >= 0 {
				at = i + 1
				break
			}
		}
		out = append(out, qsTileEntry{})
		copy(out[at+1:], out[at:])
		out[at] = qsTileEntry{ID: spec.ID, Size: qsSpecDefaultSize(spec)}
		seen[spec.ID] = true
	}
	return out
}

// formatQSTiles writes the stored form; a button at its default size is
// written without one.
func formatQSTiles(tiles []qsTileEntry) string {
	var b strings.Builder
	for _, t := range tiles {
		spec, ok := qsSpecFor(t.ID)
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(';')
		}
		if t.Hidden {
			b.WriteByte('-')
		}
		b.WriteString(t.ID)
		if sz := qsSnapSize(spec, t.Size, qsCompactCols); sz != qsSpecDefaultSize(spec) {
			b.WriteByte('@')
			b.WriteString(strconv.Itoa(sz.W))
			b.WriteByte('x')
			b.WriteString(strconv.Itoa(sz.H))
		}
	}
	return b.String()
}

// qsTilesIndex is the index of id on a list, or -1.
func qsTilesIndex(tiles []qsTileEntry, id string) int {
	for i, t := range tiles {
		if t.ID == id {
			return i
		}
	}
	return -1
}

// qsTilesMoveEntry moves the entry at i to index j, in place.
func qsTilesMoveEntry(tiles []qsTileEntry, i, j int) []qsTileEntry {
	if i < 0 || i >= len(tiles) || j < 0 || j >= len(tiles) || i == j {
		return tiles
	}
	t := tiles[i]
	if i < j {
		copy(tiles[i:j], tiles[i+1:j+1])
	} else {
		copy(tiles[j+1:i+1], tiles[j:i])
	}
	tiles[j] = t
	return tiles
}

// qsTilesState caches the parsed arrangement against the stored text: the
// panel asks for it every frame and parses again only when the text changes.
// Render/event thread only.
var qsTilesState struct {
	valid, have bool
	raw         string
	tiles       []qsTileEntry
}

// qsTilesRaw reads the stored text without allocating.
func qsTilesRaw(config *Config) (string, bool) {
	if config == nil || config.Variables.Custom == nil {
		return "", false
	}
	v, ok := config.Variables.Custom[qsTilesKey]
	if !ok || v == nil {
		return "", false
	}
	if s, isStr := v.(string); isStr {
		return s, true
	}
	return customString(config.Variables.Custom, qsTilesKey)
}

// qsTilesCurrent is the arrangement in force. The slice is shared: copy it
// before changing it.
func qsTilesCurrent(config *Config) []qsTileEntry {
	raw, have := qsTilesRaw(config)
	st := &qsTilesState
	if st.valid && st.raw == raw && st.have == have {
		return st.tiles
	}
	st.tiles = parseQSTiles(raw)
	st.raw, st.have, st.valid = raw, have, true
	return st.tiles
}

// qsTilesSet stores an arrangement. The default one is stored as blank, so a
// later build's default reaches anyone who never changed theirs.
func qsTilesSet(config *Config, tiles []qsTileEntry) {
	raw := formatQSTiles(tiles)
	if raw == formatQSTiles(qsDefaultTiles()) {
		raw = ""
	}
	st := &qsTilesState
	st.tiles = parseQSTiles(raw)
	st.raw, st.have, st.valid = raw, true, true
	if config != nil {
		setStoreString(config, qsTilesKey, raw)
	}
}

// ── What the panel shows ──────────────────────────────────────────────────

// qsSlot is one button as the panel shows it: its spec's id, kind and glyph,
// its row in the panel's state, its size on this layout's grid and whether it
// is hidden (drawn only while editing).
type qsSlot struct {
	ID     string
	Kind   qsKind
	Idx    int
	Glyph  string
	Size   homeTileSize
	Hidden bool
}

// qsLay is the panel as last laid out. qs.cursor indexes slots. Render/event
// thread only.
var qsLay struct {
	layout string
	cols   int // 0 on the list
	slots  []qsSlot
	sizes  []homeTileSize
	cells  []homeCell
	rows   int

	// adjusting: A on a grid slider; Left/Right move its level until A or B.
	adjusting bool
	// wantCol is the column Up and Down aim for: where the last sideways
	// move or click landed, so a trip through a wide tile comes back to the
	// column it left.
	wantCol int

	// As last drawn, for the pointer: the card, the scrolling area, each
	// slot's rect, each slider's bar and the title's Edit pencil.
	panel    sdl.Rect
	list     sdl.Rect
	rects    []sdl.Rect
	bars     []sdl.Rect
	editChip sdl.Rect
}

// qsLayoutSync lays the panel out from the arrangement (the one being edited
// while Edit is open) and keeps the focus on the button it was on. It is
// cheap — two dozen buttons — and every frame and every press call it, so
// nothing can act on a stale list.
func qsLayoutSync(config *Config) {
	prev := ""
	if qs.cursor >= 0 && qs.cursor < len(qsLay.slots) {
		prev = qsLay.slots[qs.cursor].ID
	}
	layout := qsLayoutCurrent(config)
	entries := qsTilesCurrent(config)
	if qsEd.open {
		entries = qsEd.work
	}
	cols := qsLayoutCols(layout)
	qsLay.layout, qsLay.cols = layout, cols
	slots := qsLay.slots[:0]
	for _, e := range entries {
		if e.Hidden && !qsEd.open {
			continue
		}
		spec, ok := qsSpecFor(e.ID)
		if !ok {
			continue
		}
		idx, ok := qsResolve(spec)
		if !ok {
			continue
		}
		size := homeTileSize{W: 1, H: 1}
		if cols > 0 {
			size = qsSnapSize(spec, e.Size, cols)
		}
		slots = append(slots, qsSlot{ID: e.ID, Kind: spec.Kind, Idx: idx, Glyph: spec.Glyph, Size: size, Hidden: e.Hidden})
	}
	qsLay.slots = slots
	if cols > 0 {
		sizes := qsLay.sizes[:0]
		for _, s := range slots {
			sizes = append(sizes, s.Size)
		}
		qsLay.sizes = sizes
		qsLay.cells, qsLay.rows = homePackTiles(sizes, cols, qsLay.cells)
	} else {
		qsLay.cells = qsLay.cells[:0]
		qsLay.rows = len(slots)
	}
	if prev != "" && (qs.cursor >= len(slots) || slots[qs.cursor].ID != prev) {
		for i, s := range slots {
			if s.ID == prev {
				qs.cursor = i
				break
			}
		}
	}
	qs.cursor = clampIndex(qs.cursor, len(slots))
	if qsLay.adjusting {
		if f, ok := qsFocused(); !ok || f.Kind != qsKindSlider || cols == 0 || qsEd.open {
			qsLay.adjusting = false
		}
	}
}

// qsFocused is the focused button.
func qsFocused() (qsSlot, bool) {
	if qs.cursor < 0 || qs.cursor >= len(qsLay.slots) {
		return qsSlot{}, false
	}
	return qsLay.slots[qs.cursor], true
}

// qsFocusID puts the focus on the button with this id, if it is shown.
func qsFocusID(id string) {
	for i, s := range qsLay.slots {
		if s.ID == id {
			qs.cursor = i
			qsNoteCol()
			return
		}
	}
}

// qsNoteCol makes the focused tile's column the one Up and Down aim for.
func qsNoteCol() {
	if qs.cursor >= 0 && qs.cursor < len(qsLay.cells) {
		qsLay.wantCol = qsLay.cells[qs.cursor].Col
	}
}

// ── Walking the grid ──────────────────────────────────────────────────────

// qsSpanOverlap reports whether two runs of rows (or columns) share one.
func qsSpanOverlap(a, aLen, b, bLen int) bool {
	return a < b+bLen && b < a+aLen
}

// qsSpanDist is how far col lies outside the run start..start+n-1 (0 inside).
func qsSpanDist(col, start, n int) int {
	switch {
	case col < start:
		return start - col
	case col >= start+n:
		return col - (start + n - 1)
	}
	return 0
}

// qsGridStep is the tile a D-pad press moves to from tile i, or -1. Sideways
// it is the nearest tile on the same rows, and past the end of a row the
// next tile in reading order (the one before it going left), so every tile is
// reachable with Left/Right alone; up and down it is the nearest row's tile
// closest to wantCol.
func qsGridStep(cells []homeCell, i int, dir FocusDirection, wantCol int) int {
	if i < 0 || i >= len(cells) {
		return -1
	}
	c := cells[i]
	best, bestKey := -1, 0
	for j, d := range cells {
		if j == i {
			continue
		}
		var gap, off int
		switch dir {
		case FocusRight:
			if d.Col < c.Col+c.W || !qsSpanOverlap(c.Row, c.H, d.Row, d.H) {
				continue
			}
			gap, off = d.Col-(c.Col+c.W), homeAbsInt(d.Row-c.Row)
		case FocusLeft:
			if d.Col+d.W > c.Col || !qsSpanOverlap(c.Row, c.H, d.Row, d.H) {
				continue
			}
			gap, off = c.Col-(d.Col+d.W), homeAbsInt(d.Row-c.Row)
		case FocusDown:
			if d.Row < c.Row+c.H {
				continue
			}
			gap, off = d.Row-(c.Row+c.H), qsSpanDist(wantCol, d.Col, d.W)
		case FocusUp:
			if d.Row+d.H > c.Row {
				continue
			}
			gap, off = c.Row-(d.Row+d.H), qsSpanDist(wantCol, d.Col, d.W)
		default:
			return -1
		}
		key := gap*1024 + off
		if best < 0 || key < bestKey {
			best, bestKey = j, key
		}
	}
	if best < 0 {
		switch {
		case dir == FocusRight && i+1 < len(cells):
			return i + 1
		case dir == FocusLeft && i > 0:
			return i - 1
		}
	}
	return best
}

// qsGridMove moves the grid's focus one step.
func qsGridMove(dir FocusDirection) bool {
	j := qsGridStep(qsLay.cells, qs.cursor, dir, qsLay.wantCol)
	if j < 0 || j == qs.cursor {
		return false
	}
	qs.cursor = j
	if dir == FocusLeft || dir == FocusRight {
		qsNoteCol()
	}
	PlayNavSound()
	return true
}

// ── Input ─────────────────────────────────────────────────────────────────

// qsMove is a D-pad press outside Edit.
func qsMove(dir FocusDirection) {
	if qsLay.cols == 0 {
		switch dir {
		case FocusUp:
			qsNavigate(-1)
		case FocusDown:
			qsNavigate(1)
		case FocusLeft:
			qsNavigateSlider(-5)
		case FocusRight:
			qsNavigateSlider(5)
		}
		return
	}
	if qsLay.adjusting {
		if f, ok := qsFocused(); ok && f.Kind == qsKindSlider {
			switch dir {
			case FocusLeft:
				qsStepSlider(f.Idx, -5)
				return
			case FocusRight:
				qsStepSlider(f.Idx, 5)
				return
			}
		}
		// Up or Down leaves the slider.
		qsLay.adjusting = false
	}
	qsGridMove(dir)
}

// qsBack is B outside Edit: it ends adjusting a slider, else closes.
func qsBack() {
	if qsLay.adjusting {
		qsLay.adjusting = false
		PlayBackSound()
		return
	}
	qsToggleOpen()
	PlayBackSound()
}

// qsPad is the controller while the panel is open (controller.go routes
// every press here; the triggers come through qsTriggerPress).
func qsPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return true
	}
	qsLayoutSync(config)
	if qsEd.open {
		qsEditPad(e, config)
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		qsMove(FocusUp)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		qsMove(FocusDown)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		qsMove(FocusLeft)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		qsMove(FocusRight)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		qsShoulderPress(padL1)
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		qsShoulderPress(padR1)
	case sdl.CONTROLLER_BUTTON_A:
		qsSelect()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_B:
		qsBack()
	case sdl.CONTROLLER_BUTTON_BACK:
		qsToggleOpen()
		PlayBackSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSTICK:
		// F2 on a Brick opened the panel, and the same key puts it away.
		if brickHasFunctionKeys() {
			qsToggleOpen()
			PlayBackSound()
		}
	}
	return true
}

// qsKey is the keyboard while the panel is open: the arrows, Enter or Space
// (A), Esc or B, PgUp/PgDn (L1/R1), Home or Q (L2: Edit), End (R2) and
// Ctrl+Q, the chord that opened it.
func qsKey(e *sdl.KeyboardEvent, config *Config) {
	if e == nil || e.Type != sdl.KEYDOWN || !qs.open {
		return
	}
	qsLayoutSync(config)
	if qsEd.open {
		qsEditKey(e, config)
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		qsMove(FocusUp)
	case sdl.K_DOWN:
		qsMove(FocusDown)
	case sdl.K_LEFT:
		qsMove(FocusLeft)
	case sdl.K_RIGHT:
		qsMove(FocusRight)
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		// The same press and sound as the pad's A.
		qsSelect()
		PlayActivateSound()
	case sdl.K_PAGEUP, sdl.K_PAGEDOWN, sdl.K_HOME, sdl.K_END:
		// The keyboard twins of the pad's shoulders (inputhints.go).
		if e.Repeat == 0 || e.Keysym.Sym == sdl.K_PAGEUP || e.Keysym.Sym == sdl.K_PAGEDOWN {
			qsShoulderPress(keyShoulder(e.Keysym.Sym))
		}
	case sdl.K_ESCAPE, sdl.K_b:
		qsBack()
	case sdl.K_q:
		if e.Repeat != 0 {
			break
		}
		// Ctrl+Q opened the panel; the same chord puts it away. Q alone is
		// L2's twin, as on Home: it edits the panel.
		if e.Keysym.Mod&sdl.KMOD_CTRL != 0 {
			qs.open = false
			PlayBackSound()
			break
		}
		qsEditOpen(config)
	}
}

// qsShoulderPress is a shoulder or trigger while the panel is open. L2 edits
// the panel and R2 jumps to the last button. L1/R1 move a focused slider's
// level and, in the grid layouts, step a focused mode row. On the list they
// page any other row, as they always did; a grid does not page, so a run of
// presses can never land on a slider or a mode row and start changing it.
func qsShoulderPress(s padShoulder) {
	if qsEd.open {
		qsEditShoulder(s, appConfig)
		return
	}
	qsLayoutSync(appConfig)
	switch s {
	case padL2:
		qsEditOpen(appConfig)
		return
	case padR2:
		if n := len(qsLay.slots); n > 0 {
			qs.cursor = n - 1
			qsLay.adjusting = false
			qsNoteCol()
			PlayNavSound()
		}
		return
	}
	step := -1
	if s == padR1 {
		step = 1
	}
	if f, ok := qsFocused(); ok {
		switch {
		case f.Kind == qsKindSlider:
			qsStepSlider(f.Idx, 5*step)
			return
		case f.Kind == qsKindCycle && qsLay.cols > 0:
			qsStepCycle(f.Idx, step)
			return
		}
	}
	n := len(qsLay.slots)
	if n == 0 || qsLay.cols > 0 {
		return
	}
	qs.cursor = clampIndex(qs.cursor+step*qsPageRows, n)
	PlayNavSound()
}

// qsOwnsPad reports whether the panel is open and in front: no card, sheet or
// panel the controller router (controller.go) serves before it is up over it
// — the notification list a toast opened, the controller legend, a popover.
// The triggers, the pointer and the wheel go to the panel only then.
func qsOwnsPad() bool {
	if !qs.open {
		return false
	}
	switch {
	case modalPanelBlocksShoulders(), ambientIsActive(), launchFailure.open, videoPrepareActive(), voiceOrbModal(),
		recoveryCard.open, emuPickOpen(), qrZoom.open, ledPanel.open, podPanel.open, themeCommunityPanel.open,
		browserCookiePanel.open, browserBookmarkPanel.open, brwInfo.open, homeCalendarOpen, nc.open,
		controlsLegend.open, shortcutsOpen, clipSearchOpen, globalSearchOpen:
		return false
	}
	return true
}

// qsTriggerPress takes L2/R2 while the panel is in front. The triggers arrive
// as axis motion, which an open overlay used to swallow whole
// (triggerOwnedByOverlay), so L2 could never reach the panel.
func qsTriggerPress(s padShoulder) bool {
	if !qsOwnsPad() {
		return false
	}
	qsShoulderPress(s)
	return true
}

// qsWheel is the mouse wheel while the panel is in front: a step up or down.
func qsWheel(dy int32) bool {
	if !qsOwnsPad() {
		return false
	}
	qsLayoutSync(appConfig)
	if qsEd.open && (qsEd.holding || qsEd.resizing || qsEd.strip) {
		return true
	}
	dir := FocusDown
	if dy > 0 {
		dir = FocusUp
	} else if dy == 0 {
		return true
	}
	qsLay.adjusting = false
	if qsLay.cols == 0 {
		step := 1
		if dir == FocusUp {
			step = -1
		}
		if n := len(qsLay.slots); n > 0 {
			if next := clampIndex(qs.cursor+step, n); next != qs.cursor {
				qs.cursor = next
				PlayNavSound()
			}
		}
		return true
	}
	qsGridMove(dir)
	return true
}

// qsMouseDown is a left press while the panel is open. The panel is modal:
// a press outside the card closes it, and nothing behind it is pressed.
func qsMouseDown(mx, my int32, config *Config) bool {
	if !qsOwnsPad() {
		return false
	}
	qsLayoutSync(config)
	p := qsLay.panel
	if p.W <= 0 || !pointInRect(mx, my, p) {
		qsToggleOpen() // saves and ends an edit too
		PlayBackSound()
		return true
	}
	if qsEd.open {
		qsEditClick(mx, my, config)
		return true
	}
	// The pencil in the title's corner: Edit for a mouse with no L2.
	if qsLay.editChip.W > 0 && pointInRect(mx, my, qsLay.editChip) {
		qsEditOpen(config)
		return true
	}
	i := qsSlotAt(mx, my)
	if i < 0 {
		return true
	}
	was := qs.cursor
	qs.cursor = i
	qsNoteCol()
	s := qsLay.slots[i]
	if s.Kind == qsKindSlider {
		// A press on the bar sets the level where it lands; anywhere else on
		// a slider focuses it, and on a grid starts adjusting it (as A).
		if i < len(qsLay.bars) && qsLay.bars[i].W > 0 && pointInRect(mx, my, qsLay.bars[i]) {
			qsLay.adjusting = false
			qsSliderClick(s.Idx, i, mx)
			return true
		}
		if qsLay.cols > 0 && (i == was || !qsLay.adjusting) {
			qsSelect()
			PlayActivateSound()
			return true
		}
		qsLay.adjusting = false
		PlayNavSound()
		return true
	}
	qsLay.adjusting = false
	qsSelect()
	PlayActivateSound()
	return true
}

// qsSlotAt is the slot under the pointer, or -1. Only the part of the list
// on screen counts: a row scrolled under the title is not there.
func qsSlotAt(mx, my int32) int {
	if !pointInRect(mx, my, qsLay.list) {
		return -1
	}
	for i := range qsLay.rects {
		if i < len(qsLay.slots) && qsLay.rects[i].W > 0 && pointInRect(mx, my, qsLay.rects[i]) {
			return i
		}
	}
	return -1
}

// qsSliderClick sets slider idx (drawn as slot) to the level under x.
func qsSliderClick(idx, slot int, x int32) {
	if idx < 0 || idx >= len(qs.sliders) || slot < 0 || slot >= len(qsLay.bars) {
		return
	}
	s := &qs.sliders[idx]
	if !s.Available {
		// Says why there is nothing to set (qsStepSlider's toast).
		qsStepSlider(idx, 0)
		return
	}
	bar := qsLay.bars[slot]
	if bar.W <= 0 {
		return
	}
	frac := float64(x-bar.X) / float64(bar.W)
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	target := s.Min + frac*(s.Max-s.Min)
	delta := int((target-s.Value)*100 + 0.5)
	if target < s.Value {
		delta = int((target-s.Value)*100 - 0.5)
	}
	if delta != 0 {
		qsStepSlider(idx, delta)
	}
}
