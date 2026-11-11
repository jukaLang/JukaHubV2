package main

// home_tiles.go — the editable Home's tile list: which tiles Home shows in the
// Carousel and Widgets layouts, in what order, at what size, and how the list
// is stored.
//
// The two layouts read ONE ordered list (Custom "homeTiles"); the carousel
// ignores the sizes. A tile is named by a ref:
//
//   - a built-in Home card: continue, media, emulator (the Games card),
//     packages, apps, favorites, browser, chat, tools, files, settings — the
//     HomeFocusIDs of homeBodyCards, so a card keeps its focus ID, its title
//     and the activation it has on the dashboard;
//   - a widget: "w:clock", "w:weather", "w:battery", "w:storage",
//     "w:nowplaying", "w:calendar", "w:playtime" (home_widgetdraw.go paints
//     them);
//   - a Start menu entry: "app:scene:<scene name>" for a screen, or
//     "app:do:<entry name>" for one of the menu's actions (Downloads,
//     Wallpapers, a plugin's button…);
//   - "edit:add", the add tile edit mode (and an empty Home) shows. It is
//     never stored.
//
// The stored form is the refs joined by ";", each with an optional "@WxH"
// size: "continue@2x2;media@4x1;emulator;…;w:clock;…". Inside a ref "%", ";"
// and "@" are written %25, %3B and %40. A missing or blank value is the
// default list, "none" is an empty Home. Parsing forgives hand edits: blank
// entries, unknown cards and widgets, duplicates and the add tile are dropped;
// an app ref is kept even while its entry is missing (a plugin switched off),
// so switching the plugin back on brings the tile back where it was.

import (
	"strconv"
	"strings"
)

const (
	homeTilesKey  = "homeTiles"
	homeTilesNone = "none"
	// homeTilesMax bounds the list: a hand-edited value cannot make Home lay
	// out thousands of tiles every time it rebuilds.
	homeTilesMax = 64

	homeTileAddRef       = "edit:add"
	homeTileWidgetPrefix = "w:"
	homeTileScenePrefix  = "app:scene:"
	homeTileActionPrefix = "app:do:"

	// homeGridCols is the widget grid's width in cells; homeGridRowsShown how
	// many rows the body shows at once (it scrolls past them).
	homeGridCols      = 6
	homeGridRowsShown = 3
	// homeTileMaxH is the tallest a tile can be.
	homeTileMaxH = 2
)

// homeDefaultTilesText is the default list, and what Reset restores: the
// dashboard's cards on the six-column grid, with the clock widget in the slot
// the dashboard's wider Games/Packages/Apps row leaves over. It packs into
// exactly the three rows Home shows (homePackTiles).
const homeDefaultTilesText = "continue@2x2;media@4x1;emulator;packages;apps;w:clock;favorites;browser;chat;tools;files;settings"

// homeTileKind is what a ref names.
type homeTileKind uint8

const (
	homeTileUnknown homeTileKind = iota
	homeTileCard
	homeTileWidget
	homeTileApp
	homeTileAdd
)

// homeTileSize is a tile's size in grid cells.
type homeTileSize struct{ W, H int }

// homeTile is one entry of the list.
type homeTile struct {
	Ref  string
	Size homeTileSize
}

// The sizes a tile can take, in the order X steps through them. The first is
// the size a new tile gets. Continue never goes below two rows: its chip, its
// progress line and its pill collide on one (homeGridRects says the same).
var (
	homeSizesCard       = []homeTileSize{{1, 1}, {2, 1}, {2, 2}}
	homeSizesContinue   = []homeTileSize{{2, 2}, {3, 2}, {4, 2}}
	homeSizesMedia      = []homeTileSize{{2, 1}, {3, 1}, {4, 1}, {2, 2}, {4, 2}}
	homeSizesSmallWide  = []homeTileSize{{1, 1}, {2, 1}}
	homeSizesNowPlaying = []homeTileSize{{2, 1}, {2, 2}, {1, 1}}
	homeSizesCalendar   = []homeTileSize{{2, 2}, {1, 1}}
	homeSizesAdd        = []homeTileSize{{1, 1}}
)

// homeWidgetSpec is one built-in widget: its name (the ref without "w:"),
// what the add sheet and the carousel call it, the glyph it is listed with
// and the sizes it can take.
type homeWidgetSpec struct {
	Name  string
	Title string
	Note  string
	Glyph string
	Sizes []homeTileSize
}

// homeWidgetSpecs is the widget catalogue, in the add sheet's order.
var homeWidgetSpecs = []homeWidgetSpec{
	{Name: "clock", Title: "Clock", Note: "The time, the date and your next alarm", Glyph: "clock", Sizes: homeSizesCard},
	{Name: "weather", Title: "Weather", Note: "Today's forecast and the days after", Glyph: "weather", Sizes: homeSizesCard},
	{Name: "calendar", Title: "Calendar", Note: "Today's date, or the whole month", Glyph: "calendar", Sizes: homeSizesCalendar},
	{Name: "nowplaying", Title: "Now playing", Note: "What is playing, one press from the player", Glyph: "music", Sizes: homeSizesNowPlaying},
	{Name: "battery", Title: "Battery", Note: "Charge level and whether it is charging", Glyph: "battery", Sizes: homeSizesSmallWide},
	{Name: "storage", Title: "Storage", Note: "Free space where JukaHub is installed", Glyph: "disk", Sizes: homeSizesSmallWide},
	{Name: "playtime", Title: "Playtime", Note: "Time played today and trophies earned", Glyph: "trophy", Sizes: homeSizesSmallWide},
}

// homeWidgetSpecFor finds a widget by name.
func homeWidgetSpecFor(name string) (homeWidgetSpec, bool) {
	for _, spec := range homeWidgetSpecs {
		if spec.Name == name {
			return spec, true
		}
	}
	return homeWidgetSpec{}, false
}

// homeTileWidgetName is the widget name of a "w:" ref ("" for any other ref).
func homeTileWidgetName(ref string) string {
	if strings.HasPrefix(ref, homeTileWidgetPrefix) {
		return ref[len(homeTileWidgetPrefix):]
	}
	return ""
}

// homeTileIsCard reports whether ref names one of the built-in Home cards.
func homeTileIsCard(ref string) bool {
	return ref != "" && homeIsBodyCard(HomeFocusID(ref))
}

// homeTileKindOf sorts a (normalised) ref.
func homeTileKindOf(ref string) homeTileKind {
	switch {
	case ref == homeTileAddRef:
		return homeTileAdd
	case homeTileIsCard(ref):
		return homeTileCard
	case strings.HasPrefix(ref, homeTileWidgetPrefix):
		// A widget this build does not know (one a newer build saved, or a
		// typo) is kept and drawn as unavailable rather than dropped, so the
		// next edit cannot delete it from the config behind the user's back.
		if strings.TrimSpace(homeTileWidgetName(ref)) != "" {
			return homeTileWidget
		}
	case strings.HasPrefix(ref, homeTileScenePrefix):
		if strings.TrimSpace(ref[len(homeTileScenePrefix):]) != "" {
			return homeTileApp
		}
	case strings.HasPrefix(ref, homeTileActionPrefix):
		if strings.TrimSpace(ref[len(homeTileActionPrefix):]) != "" {
			return homeTileApp
		}
	}
	return homeTileUnknown
}

// homeAppRefAliases maps an app ref whose Start menu entry was renamed (keyed
// lower-case) to the entry's name now, so a tile made before the rename keeps
// working.
var homeAppRefAliases = map[string]string{
	homeTileActionPrefix + "remote cast": homeTileActionPrefix + "Screen Share",
}

// homeTileNormalizeRef puts a hand-written ref into the form the list uses:
// card and widget names are lower-case ("Media" → media, "games" → emulator,
// "W:Clock" → w:clock), the app prefixes are lower-case while the screen or
// entry name after them keeps its spelling. "" when nothing is left.
func homeTileNormalizeRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	lower := strings.ToLower(ref)
	switch {
	case strings.HasPrefix(lower, homeTileScenePrefix):
		return homeTileScenePrefix + strings.TrimSpace(ref[len(homeTileScenePrefix):])
	case strings.HasPrefix(lower, homeTileActionPrefix):
		out := homeTileActionPrefix + strings.TrimSpace(ref[len(homeTileActionPrefix):])
		if to, ok := homeAppRefAliases[strings.ToLower(out)]; ok {
			return to
		}
		return out
	case strings.HasPrefix(lower, homeTileWidgetPrefix):
		return homeTileWidgetPrefix + strings.TrimSpace(lower[len(homeTileWidgetPrefix):])
	case lower == homeTileAddRef:
		return homeTileAddRef
	}
	if homeTileIsCard(lower) {
		return lower
	}
	// The names the Games card has had, as homeTitleAlias reads a config tile.
	if id := homeTitleAlias(lower); id != "" && homeIsBodyCard(id) {
		return string(id)
	}
	return lower
}

// homeTileSizesFor is the size cycle of a ref (homeSizesCard for an unknown one).
func homeTileSizesFor(ref string) []homeTileSize {
	switch homeTileKindOf(ref) {
	case homeTileAdd:
		return homeSizesAdd
	case homeTileCard:
		switch HomeFocusID(ref) {
		case HomeFocusContinue:
			return homeSizesContinue
		case HomeFocusMedia:
			return homeSizesMedia
		}
	case homeTileWidget:
		if spec, ok := homeWidgetSpecFor(homeTileWidgetName(ref)); ok && len(spec.Sizes) > 0 {
			return spec.Sizes
		}
	}
	return homeSizesCard
}

// homeTileDefaultSize is the size a ref gets when it is added or stored
// without one.
func homeTileDefaultSize(ref string) homeTileSize {
	return homeTileSizesFor(ref)[0]
}

// homeTileSnapSize maps any size onto the nearest one the ref can take (the
// smallest |dW|+|dH|; a tie keeps the earlier size of the cycle).
func homeTileSnapSize(ref string, s homeTileSize) homeTileSize {
	if homeTileUnknownWidget(ref) {
		// A widget this build cannot draw keeps the size it was saved with,
		// for the build that can.
		return homeTileSize{W: homeClampInt(s.W, 1, homeGridCols), H: homeClampInt(s.H, 1, homeTileMaxH)}
	}
	sizes := homeTileSizesFor(ref)
	best, bestD := sizes[0], -1
	for _, c := range sizes {
		d := homeAbsInt(c.W-s.W) + homeAbsInt(c.H-s.H)
		if bestD < 0 || d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

// homeTileNextSize is the size after s in the ref's cycle, wrapping. A size
// that is not in the cycle snaps first.
func homeTileNextSize(ref string, s homeTileSize) homeTileSize {
	sizes := homeTileSizesFor(ref)
	s = homeTileSnapSize(ref, s)
	for i, c := range sizes {
		if c == s {
			return sizes[(i+1)%len(sizes)]
		}
	}
	return sizes[0]
}

// homeTileCanResize reports whether X does anything on the ref.
func homeTileCanResize(ref string) bool {
	return !homeTileUnknownWidget(ref) && len(homeTileSizesFor(ref)) > 1
}

// homeTileUnknownWidget reports a "w:" ref this build has no widget for.
func homeTileUnknownWidget(ref string) bool {
	if !strings.HasPrefix(ref, homeTileWidgetPrefix) {
		return false
	}
	_, ok := homeWidgetSpecFor(homeTileWidgetName(ref))
	return !ok
}

func homeClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// homeTileSizeLabels are the size chips' words ("2×1"), made once so edit
// mode does not format a string per tile per frame.
var homeTileSizeLabels = func() (out [homeGridCols + 1][homeTileMaxH + 1]string) {
	for w := 1; w <= homeGridCols; w++ {
		for h := 1; h <= homeTileMaxH; h++ {
			out[w][h] = strconv.Itoa(w) + "×" + strconv.Itoa(h)
		}
	}
	return out
}()

// homeTileSizeLabel is the chip text for a size.
func homeTileSizeLabel(s homeTileSize) string {
	if s.W >= 1 && s.W <= homeGridCols && s.H >= 1 && s.H <= homeTileMaxH {
		return homeTileSizeLabels[s.W][s.H]
	}
	return strconv.Itoa(s.W) + "×" + strconv.Itoa(s.H)
}

func homeAbsInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// homeTileEscape writes the three characters the stored form uses as
// separators so a ref can carry them (a plugin named "Notes; work").
func homeTileEscape(ref string) string {
	if !strings.ContainsAny(ref, "%;@") {
		return ref
	}
	r := strings.NewReplacer("%", "%25", ";", "%3B", "@", "%40")
	return r.Replace(ref)
}

// homeTileUnescape undoes homeTileEscape. Any other "%" sequence is kept as
// written.
func homeTileUnescape(ref string) string {
	if !strings.Contains(ref, "%") {
		return ref
	}
	var b strings.Builder
	for i := 0; i < len(ref); i++ {
		if ref[i] == '%' && i+2 < len(ref) {
			switch strings.ToUpper(ref[i+1 : i+3]) {
			case "25":
				b.WriteByte('%')
				i += 2
				continue
			case "3B":
				b.WriteByte(';')
				i += 2
				continue
			case "40":
				b.WriteByte('@')
				i += 2
				continue
			}
		}
		b.WriteByte(ref[i])
	}
	return b.String()
}

// parseHomeTileSize reads "2x1" (or "2×1", "2X1").
func parseHomeTileSize(s string) (homeTileSize, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return homeTileSize{}, false
	}
	s = strings.ReplaceAll(s, "×", "x")
	s = strings.ToLower(s)
	w, h, ok := strings.Cut(s, "x")
	if !ok {
		return homeTileSize{}, false
	}
	wi, err1 := strconv.Atoi(strings.TrimSpace(w))
	hi, err2 := strconv.Atoi(strings.TrimSpace(h))
	if err1 != nil || err2 != nil || wi < 1 || hi < 1 {
		return homeTileSize{}, false
	}
	return homeTileSize{W: wi, H: hi}, true
}

// homeDefaultTiles is a fresh copy of the default list.
func homeDefaultTiles() []homeTile {
	return parseHomeTilesStrict(homeDefaultTilesText)
}

// parseHomeTiles reads the stored list. Blank is the default list, "none" an
// empty Home; a value with nothing usable in it is treated as blank, so a
// mangled hand edit cannot leave Home empty without saying so.
func parseHomeTiles(s string) []homeTile {
	s = strings.TrimSpace(s)
	if s == "" {
		return homeDefaultTiles()
	}
	if strings.EqualFold(s, homeTilesNone) {
		return []homeTile{}
	}
	tiles := parseHomeTilesStrict(s)
	if len(tiles) == 0 {
		return homeDefaultTiles()
	}
	return tiles
}

// parseHomeTilesStrict is parseHomeTiles without the fallbacks.
func parseHomeTilesStrict(s string) []homeTile {
	out := make([]homeTile, 0, 16)
	seen := make(map[string]bool, 16)
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ref, size := part, ""
		if i := strings.LastIndexByte(part, '@'); i >= 0 {
			ref, size = part[:i], part[i+1:]
		}
		ref = homeTileNormalizeRef(homeTileUnescape(ref))
		if ref == "" || seen[ref] {
			continue
		}
		switch homeTileKindOf(ref) {
		case homeTileUnknown, homeTileAdd:
			continue
		}
		t := homeTile{Ref: ref, Size: homeTileDefaultSize(ref)}
		if sz, ok := parseHomeTileSize(size); ok {
			t.Size = homeTileSnapSize(ref, sz)
		}
		seen[ref] = true
		out = append(out, t)
		if len(out) >= homeTilesMax {
			break
		}
	}
	return out
}

// formatHomeTiles writes the stored form. A tile at its default size is
// written without one, so the value stays short and readable.
func formatHomeTiles(tiles []homeTile) string {
	var b strings.Builder
	for _, t := range tiles {
		if t.Ref == "" || t.Ref == homeTileAddRef {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(';')
		}
		b.WriteString(homeTileEscape(t.Ref))
		if size := homeTileSnapSize(t.Ref, t.Size); size != homeTileDefaultSize(t.Ref) {
			b.WriteByte('@')
			b.WriteString(strconv.Itoa(size.W))
			b.WriteByte('x')
			b.WriteString(strconv.Itoa(size.H))
		}
	}
	if b.Len() == 0 {
		return homeTilesNone
	}
	return b.String()
}

// ── The list in force ─────────────────────────────────────────────────────

// homeTilesState caches the parsed list against the stored text. Home asks
// for the list every frame; it is parsed again only when the text changes (a
// Settings edit, a config reload, an import). gen moves with every change, so
// the layout knows when to rebuild. Render thread only.
var homeTilesState struct {
	valid bool
	raw   string
	have  bool
	tiles []homeTile
	gen   uint64
}

// homeTilesRaw reads the stored text without allocating: the exact key is a
// map lookup and a type assertion. (customString formats every value it
// returns, which Home would pay for on every frame.)
func homeTilesRaw(config *Config) (string, bool) {
	if config == nil || config.Variables.Custom == nil {
		return "", false
	}
	v, ok := config.Variables.Custom[homeTilesKey]
	if !ok || v == nil {
		return "", false
	}
	if s, isStr := v.(string); isStr {
		return s, true
	}
	return customString(config.Variables.Custom, homeTilesKey)
}

// homeTilesCurrent is the stored list (the default one when nothing is
// stored). The slice is shared: copy it before changing it.
func homeTilesCurrent(config *Config) []homeTile {
	raw, have := homeTilesRaw(config)
	st := &homeTilesState
	if st.valid && st.raw == raw && st.have == have {
		return st.tiles
	}
	if have {
		st.tiles = parseHomeTiles(raw)
	} else {
		st.tiles = homeDefaultTiles()
	}
	st.raw, st.have, st.valid = raw, have, true
	st.gen++
	return st.tiles
}

// homeTilesGen is the generation of the list homeTilesCurrent last returned.
func homeTilesGen() uint64 { return homeTilesState.gen }

// homeTilesSet stores a new list: in the cache at once, and in the user
// config through the shared override-and-save path every Settings row uses.
func homeTilesSet(config *Config, tiles []homeTile) {
	raw := formatHomeTiles(tiles)
	st := &homeTilesState
	st.tiles = parseHomeTiles(raw)
	st.raw, st.have, st.valid = raw, true, true
	st.gen++
	if config != nil {
		setStoreString(config, homeTilesKey, raw)
	}
}

// homeTilesPresent is the set of refs on a list, lower-cased: an app ref a
// hand edit spelt "app:do:downloads" is the Downloads tile all the same.
func homeTilesPresent(tiles []homeTile) map[string]bool {
	out := make(map[string]bool, len(tiles))
	for _, t := range tiles {
		out[strings.ToLower(t.Ref)] = true
	}
	return out
}

// homeTilesIndex is the index of ref on a list, or -1.
func homeTilesIndex(tiles []homeTile, ref string) int {
	for i, t := range tiles {
		if t.Ref == ref {
			return i
		}
	}
	return -1
}

// ── Packing ───────────────────────────────────────────────────────────────

// homeCell is where a packed tile sits: its first column and row and the
// cells it covers.
type homeCell struct{ Col, Row, W, H int }

// homePackTiles places sizes on a cols-wide grid in order, the way a CSS grid
// auto-places with "sparse" flow: a cursor walks the cells row by row; each
// tile goes to the first place at or after the cursor where it fits (wrapping
// to the next row when the rest of the row is too narrow or a taller tile is
// in the way), and the cursor moves to just after it. Holes left behind are
// never back-filled, so the reading order of the grid is the list's order and
// moving a tile one place earlier or later does what it looks like it does.
// out is reused when it is big enough; the second result is the row count.
func homePackTiles(sizes []homeTileSize, cols int, out []homeCell) ([]homeCell, int) {
	if cols < 1 {
		cols = 1
	}
	if cols > 30 {
		cols = 30
	}
	if cap(out) < len(sizes) {
		out = make([]homeCell, len(sizes))
	}
	out = out[:len(sizes)]
	var occ []uint32 // one bit per column, one word per row
	used := func(row int, mask uint32) bool {
		return row < len(occ) && occ[row]&mask != 0
	}
	rows := 0
	cr, cc := 0, 0
	for i, s := range sizes {
		w, h := s.W, s.H
		if w < 1 {
			w = 1
		}
		if w > cols {
			w = cols
		}
		if h < 1 {
			h = 1
		}
		if h > homeTileMaxH {
			h = homeTileMaxH
		}
		mask := uint32(1)<<uint(w) - 1
		r, c := cr, cc
		for {
			if c+w > cols {
				r++
				c = 0
				continue
			}
			m := mask << uint(c)
			fits := true
			for rr := r; rr < r+h; rr++ {
				if used(rr, m) {
					fits = false
					break
				}
			}
			if fits {
				break
			}
			c++
		}
		for len(occ) < r+h {
			occ = append(occ, 0)
		}
		m := mask << uint(c)
		for rr := r; rr < r+h; rr++ {
			occ[rr] |= m
		}
		out[i] = homeCell{Col: c, Row: r, W: w, H: h}
		cr, cc = r, c+w
		if r+h > rows {
			rows = r + h
		}
	}
	return out, rows
}

// ── What a tile is called and what it opens ───────────────────────────────

// homeAppInfo is a Start menu entry as an app tile shows it.
type homeAppInfo struct {
	Title string
	Note  string
	Glyph string
	ok    bool
}

// homeAppRefFor is the ref an app tile for a Start menu entry stores: the
// screen for a screen row, the entry's name for an action row.
func homeAppRefFor(entry startMenuEntry) string {
	if entry.scene != "" {
		return homeTileScenePrefix + entry.scene
	}
	return homeTileActionPrefix + entry.name
}

// homeAppGroupNote is the line an app tile shows under its name: the Start
// menu group it comes from, in the singular.
func homeAppGroupNote(group string) string {
	switch group {
	case "Screens":
		return "Screen"
	case "Plugins":
		return "Plugin"
	case "Games":
		return "Games"
	case "Tools":
		return "Tool"
	case "Device":
		return "Device"
	}
	return group
}

// homeAppTileOffered reports whether a Start menu entry may become a tile:
// not Home itself, not a screen a built-in card already opens (the card is
// the better tile), not the handoff rows (they name devices that come and
// go), not quitting (the power pill does that) and not Edit Home.
func homeAppTileOffered(entry startMenuEntry) bool {
	if entry.name == "" || (entry.scene == "" && entry.action == nil) {
		return false
	}
	if strings.HasPrefix(entry.name, "Send to ") {
		return false
	}
	switch entry.name {
	case "Quit JukaHub", "Edit Home", gamesLibraryEntryName:
		return false
	}
	switch entry.scene {
	case "Main", "Tube", "FileExplorer", "Packages", "Apps", "Favorites", "Chat", "Settings",
		browserSceneName, toolsHubSceneName:
		return false
	}
	return true
}

// homeAppEntries lists the Start menu's entries with the group each is in,
// skipping duplicates by name like getStartMenuApps does.
func homeAppEntries(config *Config) ([]startMenuEntry, []string) {
	if config == nil {
		return nil, nil
	}
	var entries []startMenuEntry
	var groups []string
	seen := make(map[string]bool)
	for _, group := range startMenuGroups(config) {
		for _, entry := range group.items {
			if seen[entry.name] {
				continue
			}
			seen[entry.name] = true
			entries = append(entries, entry)
			groups = append(groups, group.name)
		}
	}
	return entries, groups
}

// homeAppEntryFor finds the Start menu entry an app ref names. The name is
// matched without regard to case, so a hand-edited "app:do:downloads" still
// finds Downloads.
func homeAppEntryFor(config *Config, ref string) (startMenuEntry, bool) {
	entries, _ := homeAppEntries(config)
	for _, entry := range entries {
		if strings.EqualFold(homeAppRefFor(entry), ref) {
			return entry, true
		}
	}
	return startMenuEntry{}, false
}

// homeAppTitleFromRef is the name an app tile shows while its entry is
// missing: the screen's display name or the entry's own name.
func homeAppTitleFromRef(ref string) string {
	switch {
	case strings.HasPrefix(ref, homeTileScenePrefix):
		return sceneDisplayName(ref[len(homeTileScenePrefix):])
	case strings.HasPrefix(ref, homeTileActionPrefix):
		return ref[len(homeTileActionPrefix):]
	}
	return ref
}

// homeResolveApps names every app ref on a list from the Start menu as it
// stands. A ref with no entry comes back with ok=false.
func homeResolveApps(config *Config, tiles []homeTile) map[string]homeAppInfo {
	out := make(map[string]homeAppInfo)
	need := false
	for _, t := range tiles {
		if homeTileKindOf(t.Ref) == homeTileApp {
			need = true
			out[t.Ref] = homeAppInfo{Title: homeAppTitleFromRef(t.Ref), Note: "Not available here", Glyph: "apps"}
		}
	}
	if !need || config == nil {
		return out
	}
	entries, groups := homeAppEntries(config)
	for i, entry := range entries {
		menuRef := homeAppRefFor(entry)
		for ref := range out {
			if !strings.EqualFold(ref, menuRef) {
				continue
			}
			glyph := startMenuGlyph(entry)
			if glyph == "" {
				glyph = "apps"
			}
			out[ref] = homeAppInfo{Title: entry.name, Note: homeAppGroupNote(groups[i]), Glyph: glyph, ok: true}
		}
	}
	return out
}

// homeAppRefsSig is the app refs of a list, joined: what decides whether the
// app tiles have to be named again (home_dynbody.go). "" when there are none.
func homeAppRefsSig(tiles []homeTile) string {
	var b strings.Builder
	for _, t := range tiles {
		if homeTileKindOf(t.Ref) == homeTileApp {
			b.WriteString(t.Ref)
			b.WriteByte(';')
		}
	}
	return b.String()
}

// homeActivateApp opens the Start menu entry an app tile names, the way the
// menu itself opens it (startMenuActivate): the panels a choice replaces
// close first, an action runs, a screen opens unless the connection it needs
// is missing.
func homeActivateApp(config *Config, ref string) {
	entry, ok := homeAppEntryFor(config, ref)
	if !ok {
		ShowToast(homeAppTitleFromRef(ref)+" is not available here", ToastKindInfo)
		return
	}
	startMenuCloseOverlays()
	switch {
	case entry.action != nil:
		entry.action(config)
	case entry.scene == toolsHubSceneName:
		toolsHubOpen(config)
	case entry.scene != "":
		idx := sceneIndexExact(config, entry.scene)
		if idx < 0 {
			ShowToast(entry.name+" is not part of this setup", ToastKindInfo)
			return
		}
		if reason, blocked := offlineSceneBlock(entry.scene); blocked {
			showToast(reason, ToastWarn())
			return
		}
		if idx != currentSceneIndex {
			changeSceneTo(config, idx)
		}
	}
}

// ── The add sheet's catalogue ─────────────────────────────────────────────

// The add sheet's sections.
const (
	homeCatalogWidgets = "Widgets"
	homeCatalogCards   = "Home cards"
	homeCatalogApps    = "Apps and tools"
)

// homeCatalogEntry is one row of the add sheet.
type homeCatalogEntry struct {
	Ref     string
	Title   string
	Note    string
	Glyph   string
	Section string
	Size    homeTileSize
}

// homeCardInfo is a built-in card's name, line and glyph as rebuildItems
// gives them; the live layout's items when there is one.
func homeCardInfo(id HomeFocusID) (title, note, glyph string) {
	if homeLayout != nil {
		if item, ok := homeLayout.Items[id]; ok && item.Title != "" {
			return item.Title, item.Subtitle, item.Icon
		}
	}
	probe := NewHomeLayout(HomeBaseWidth, HomeBaseHeight)
	item := probe.Items[id]
	return item.Title, item.Subtitle, item.Icon
}

// homeTileCatalog lists what the add sheet offers: every widget this device
// can show, the built-in cards and the Start menu's screens, tools and
// actions — minus what is already on Home (present).
func homeTileCatalog(config *Config, present map[string]bool) []homeCatalogEntry {
	var out []homeCatalogEntry
	for _, spec := range homeWidgetSpecs {
		ref := homeTileWidgetPrefix + spec.Name
		if present[strings.ToLower(ref)] || !homeWidgetAvailable(spec.Name) {
			continue
		}
		out = append(out, homeCatalogEntry{
			Ref: ref, Title: spec.Title, Note: spec.Note, Glyph: spec.Glyph,
			Section: homeCatalogWidgets, Size: homeTileDefaultSize(ref),
		})
	}
	for _, id := range homeBodyCards {
		ref := string(id)
		if present[ref] {
			continue
		}
		title, note, glyph := homeCardInfo(id)
		out = append(out, homeCatalogEntry{
			Ref: ref, Title: title, Note: note, Glyph: glyph,
			Section: homeCatalogCards, Size: homeTileDefaultSize(ref),
		})
	}
	entries, groups := homeAppEntries(config)
	for i, entry := range entries {
		if !homeAppTileOffered(entry) {
			continue
		}
		ref := homeAppRefFor(entry)
		if present[strings.ToLower(ref)] {
			continue
		}
		glyph := startMenuGlyph(entry)
		if glyph == "" {
			glyph = "apps"
		}
		out = append(out, homeCatalogEntry{
			Ref: ref, Title: entry.name, Note: homeAppGroupNote(groups[i]), Glyph: glyph,
			Section: homeCatalogApps, Size: homeTileDefaultSize(ref),
		})
	}
	return out
}
