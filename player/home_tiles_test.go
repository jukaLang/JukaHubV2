package main

import (
	"math"
	"strings"
	"testing"
)

// The default list is the dashboard on the six-column grid: it fills exactly
// the three rows Home shows, every cell once.
func TestHomeTilesDefaultPacksIntoThreeRows(t *testing.T) {
	tiles := homeDefaultTiles()
	if len(tiles) != 12 {
		t.Fatalf("default list has %d tiles, want 12", len(tiles))
	}
	sizes := make([]homeTileSize, len(tiles))
	for i, tile := range tiles {
		sizes[i] = tile.Size
	}
	cells, rows := homePackTiles(sizes, homeGridCols, nil)
	if rows != homeGridRowsShown {
		t.Fatalf("default list packs into %d rows, want %d", rows, homeGridRowsShown)
	}
	want := map[string]homeCell{
		"continue":  {Col: 0, Row: 0, W: 2, H: 2},
		"media":     {Col: 2, Row: 0, W: 4, H: 1},
		"emulator":  {Col: 2, Row: 1, W: 1, H: 1},
		"w:clock":   {Col: 5, Row: 1, W: 1, H: 1},
		"favorites": {Col: 0, Row: 2, W: 1, H: 1},
		"settings":  {Col: 5, Row: 2, W: 1, H: 1},
	}
	var grid [homeGridRowsShown][homeGridCols]int
	for i, tile := range tiles {
		if w, ok := want[tile.Ref]; ok && cells[i] != w {
			t.Errorf("%s at %+v, want %+v", tile.Ref, cells[i], w)
		}
		c := cells[i]
		for r := c.Row; r < c.Row+c.H; r++ {
			for col := c.Col; col < c.Col+c.W; col++ {
				grid[r][col]++
			}
		}
	}
	for r := range grid {
		for c := range grid[r] {
			if grid[r][c] != 1 {
				t.Errorf("cell (%d,%d) covered %d times", c, r, grid[r][c])
			}
		}
	}
}

// Packing keeps the list's reading order: a tile too wide for the rest of a
// row starts the next one (the hole stays), tiles flow around a tall one, and
// a tile wider than the grid is clamped to it.
func TestHomePackTilesSparseFlow(t *testing.T) {
	cases := []struct {
		name  string
		cols  int
		sizes []homeTileSize
		want  []homeCell
		rows  int
	}{
		{
			name: "wrap", cols: 6,
			sizes: []homeTileSize{{4, 1}, {3, 1}, {1, 1}},
			want:  []homeCell{{0, 0, 4, 1}, {0, 1, 3, 1}, {3, 1, 1, 1}},
			rows:  2,
		},
		{
			name: "around a tall tile", cols: 4,
			sizes: []homeTileSize{{1, 1}, {2, 2}, {1, 1}, {1, 1}, {1, 1}, {1, 1}},
			want:  []homeCell{{0, 0, 1, 1}, {1, 0, 2, 2}, {3, 0, 1, 1}, {0, 1, 1, 1}, {3, 1, 1, 1}, {0, 2, 1, 1}},
			rows:  3,
		},
		{
			name: "clamped", cols: 6,
			sizes: []homeTileSize{{9, 1}, {1, 3}},
			want:  []homeCell{{0, 0, 6, 1}, {0, 1, 1, 2}},
			rows:  3,
		},
	}
	for _, c := range cases {
		got, rows := homePackTiles(c.sizes, c.cols, nil)
		if rows != c.rows {
			t.Errorf("%s: %d rows, want %d", c.name, rows, c.rows)
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("%s: tile %d at %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// The stored list forgives hand edits and round-trips what Home writes.
func TestHomeTilesParseAndFormat(t *testing.T) {
	if got := parseHomeTiles(""); len(got) != 12 {
		t.Errorf("blank gave %d tiles, want the default 12", len(got))
	}
	if got := parseHomeTiles(" none "); len(got) != 0 {
		t.Errorf("none gave %v, want an empty Home", got)
	}
	if got := parseHomeTiles("garbage;;nonsense@2x2;w:"); len(got) != 12 {
		t.Errorf("a value with nothing usable gave %d tiles, want the default 12", len(got))
	}
	// An unknown widget is kept (a newer build may have saved it), so an
	// edit here cannot delete it; unknown cards are dropped.
	got := parseHomeTiles("Media@3x1; games ;W:Clock@2x1;w:news;media;edit:add;continue@1x1;nope")
	want := []homeTile{
		{Ref: "media", Size: homeTileSize{3, 1}},
		{Ref: "emulator", Size: homeTileSize{1, 1}},
		{Ref: "w:clock", Size: homeTileSize{2, 1}},
		{Ref: "w:news", Size: homeTileSize{1, 1}},
		{Ref: "continue", Size: homeTileSize{2, 2}},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tile %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if s := formatHomeTiles(got); s != "media@3x1;emulator;w:clock@2x1;w:news;continue" {
		t.Errorf("formatted %q", s)
	}
	if got := parseHomeTiles("app:do:Remote Cast"); len(got) != 1 || got[0].Ref != "app:do:Screen Share" {
		t.Errorf("a renamed Start menu entry read back as %+v", got)
	}
	if s := formatHomeTiles(nil); s != homeTilesNone {
		t.Errorf("an empty list is written %q, want %q", s, homeTilesNone)
	}

	// App refs keep the screen's or the entry's own spelling, and a name with
	// the separators in it survives the round trip.
	apps := parseHomeTiles("APP:SCENE:Notes;app:do:Network tools@2x1")
	if len(apps) != 2 || apps[0].Ref != "app:scene:Notes" || apps[1].Ref != "app:do:Network tools" || apps[1].Size != (homeTileSize{2, 1}) {
		t.Errorf("app refs parsed as %+v", apps)
	}
	odd := []homeTile{{Ref: "app:do:A;B@C%D", Size: homeTileSize{1, 1}}}
	written := formatHomeTiles(odd)
	if strings.ContainsAny(strings.TrimPrefix(written, "app:do:"), ";@") {
		t.Errorf("separators were not escaped: %q", written)
	}
	if back := parseHomeTiles(written); len(back) != 1 || back[0] != odd[0] {
		t.Errorf("%q read back as %+v", written, back)
	}

	defaults := homeDefaultTiles()
	again := parseHomeTiles(formatHomeTiles(defaults))
	if len(again) != len(defaults) {
		t.Fatalf("the default list did not round-trip: %v", again)
	}
	for i := range defaults {
		if again[i] != defaults[i] {
			t.Errorf("default tile %d round-tripped as %+v, want %+v", i, again[i], defaults[i])
		}
	}
}

// X steps through a tile's sizes and wraps; a size a tile cannot take snaps
// to the nearest one it can.
func TestHomeTileSizeCycles(t *testing.T) {
	cases := []struct {
		ref      string
		from, to homeTileSize
	}{
		{"continue", homeTileSize{2, 2}, homeTileSize{3, 2}},
		{"continue", homeTileSize{4, 2}, homeTileSize{2, 2}},
		{"media", homeTileSize{4, 1}, homeTileSize{2, 2}},
		{"chat", homeTileSize{2, 2}, homeTileSize{1, 1}},
		{"w:battery", homeTileSize{2, 1}, homeTileSize{1, 1}},
		{"app:do:Downloads", homeTileSize{1, 1}, homeTileSize{2, 1}},
	}
	for _, c := range cases {
		if got := homeTileNextSize(c.ref, c.from); got != c.to {
			t.Errorf("%s: after %v comes %v, want %v", c.ref, c.from, got, c.to)
		}
	}
	if got := homeTileSnapSize("media", homeTileSize{1, 1}); got != (homeTileSize{2, 1}) {
		t.Errorf("media at 1x1 snapped to %v, want 2x1", got)
	}
	if got := homeTileSnapSize("continue", homeTileSize{6, 1}); got != (homeTileSize{4, 2}) {
		t.Errorf("continue at 6x1 snapped to %v, want 4x2", got)
	}
	if got := homeTileDefaultSize("w:nowplaying"); got != (homeTileSize{2, 1}) {
		t.Errorf("now playing arrives at %v, want 2x1", got)
	}
	if homeTileCanResize(homeTileAddRef) {
		t.Error("the add tile can be resized")
	}
	if got := homeTileSizeLabel(homeTileSize{2, 1}); got != "2×1" {
		t.Errorf("size label %q", got)
	}
}

// The carousel goes round the shorter way, and its cards are where the
// design puts them.
func TestHomeCarouselWrapAndGeometry(t *testing.T) {
	cases := []struct {
		d    float64
		n    int
		want float64
	}{
		{0, 5, 0}, {1, 5, 1}, {3, 5, -2}, {-3, 5, 2}, {4.5, 5, -0.5}, {7, 5, 2},
		{-1, 2, -1}, {1, 2, -1}, {0.25, 1, 0.25}, {2, 0, 0},
	}
	for _, c := range cases {
		if got := homeCarouselWrap(c.d, c.n); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("wrap(%v, %d) = %v, want %v", c.d, c.n, got, c.want)
		}
	}
	geom := []struct{ o, cx, w, h float64 }{
		{0, 640, 520, 330}, {1, 1054, 260, 165}, {-1, 226, 260, 165}, {2, 1338, 260, 165}, {0.5, 847, 390, 247.5},
	}
	for _, g := range geom {
		cx, w, h := homeCarouselGeom(g.o)
		if math.Abs(cx-g.cx) > 1e-9 || math.Abs(w-g.w) > 1e-9 || math.Abs(h-g.h) > 1e-9 {
			t.Errorf("offset %v: centre %v size %vx%v, want %v %vx%v", g.o, cx, w, h, g.cx, g.w, g.h)
		}
	}
	// Two cards half way through a slide keep a gutter between them.
	lx, lw, _ := homeCarouselGeom(-0.5)
	rx, rw, _ := homeCarouselGeom(0.5)
	if gap := (rx - rw/2) - (lx + lw/2); gap < 16 {
		t.Errorf("cards mid-slide are %v apart", gap)
	}
}

// The focus controller takes the tile IDs a dynamic layout adds, and forgets
// them when the layout goes.
func TestHomeFocusAcceptsDynamicTiles(t *testing.T) {
	f := NewHomeFocusController()
	if f.IsEnabled(HomeFocusEditPill) {
		t.Fatal("the edit pill starts enabled before anything draws it")
	}
	if f.Set("w:clock") {
		t.Fatal("an unknown tile took the focus")
	}
	f.SetDynamic([]HomeFocusID{"w:clock"}, []HomeFocusID{"w:clock", HomeFocusMedia})
	if !f.Set("w:clock") {
		t.Fatal("a registered tile could not take the focus")
	}
	f.SetDynamic(nil, nil)
	if f.IsEnabled("w:clock") || f.Current() == "w:clock" {
		t.Fatalf("a dropped tile kept the focus (%s)", f.Current())
	}
	// A lost focus goes to the first tile of the layout's order.
	f.SetDynamic([]HomeFocusID{"w:weather"}, []HomeFocusID{"w:weather", HomeFocusChat})
	f.SetEnabled(HomeFocusContinue, false)
	f.Set(HomeFocusChat)
	f.SetEnabled(HomeFocusChat, false)
	if got := f.Current(); got != "w:weather" {
		t.Fatalf("a lost focus went to %s, want the first tile", got)
	}
}

func TestHomeArrangementChoices(t *testing.T) {
	norm := map[string]string{
		"Carousel": homeArrangementCarousel, " widgets ": homeArrangementWidgets, "widget": homeArrangementWidgets,
		"grid": homeArrangementGrid, "dashboard": homeArrangementDashboard, "nope": homeArrangementCustom, "": homeArrangementCustom,
	}
	for in, want := range norm {
		if got := normalizeHomeArrangement(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if !homeArrangementIsDynamic(homeArrangementCarousel) || !homeArrangementIsDynamic(homeArrangementWidgets) ||
		homeArrangementIsDynamic(homeArrangementGrid) || homeArrangementIsDynamic(homeArrangementCustom) {
		t.Error("homeArrangementIsDynamic is wrong")
	}
	if got := homeArrangementChoiceIndex(homeArrangementDashboard); got != 0 {
		t.Errorf("the dashboard is choice %d, want Standard (0)", got)
	}
	for i, name := range homeArrangementChoiceList {
		if got := homeArrangementChoiceIndex(name); got != i {
			t.Errorf("%s is choice %d, want %d", name, got, i)
		}
		if homeArrangementChoiceLabel(name) == "" {
			t.Errorf("%s has no label", name)
		}
	}
	if got := homeArrangementLabel(homeArrangementWidgets); got != "Widgets" {
		t.Errorf("label %q", got)
	}
}

// homeTestDynLayout is a Home on a tile layout, laid out with motion off so
// every animation is already settled.
func homeTestDynLayout(t *testing.T, w, h int32, arrangement, tiles string) (*HomeLayout, *Config) {
	t.Helper()
	if homeEdit.open {
		t.Fatal("edit mode left open by an earlier test")
	}
	cfg := &Config{}
	cfg.Variables.Custom = map[string]interface{}{"homeArrangement": arrangement, homeTilesKey: tiles}
	layout := NewHomeLayout(w, h)
	layout.Motion = false
	layout.SetArrangement(homeArrangement(cfg))
	layout.dynSync(cfg)
	layout.dynSync(cfg)
	return layout, cfg
}

// The default tiles sit inside the body without touching, on the Smart Pro's
// canvas and the Brick's, and the cards that are not on the list are off.
func TestHomeWidgetsLayoutPlacesTheDefaultTiles(t *testing.T) {
	for _, c := range []struct{ w, h int32 }{{1280, 720}, {960, 720}} {
		layout, _ := homeTestDynLayout(t, c.w, c.h, homeArrangementWidgets, "")
		if !layout.dynActive() || len(layout.dyn.tiles) != 12 {
			t.Fatalf("%dx%d: dynamic body active=%v with %d tiles", c.w, c.h, layout.dynActive(), len(layout.dyn.tiles))
		}
		vp := layout.dynViewport()
		var rects []HomeItem
		for _, tile := range layout.dyn.tiles {
			r := layout.Items[tile.ID].Rect
			if r.W <= 0 || r.X < vp.X || r.Y < vp.Y || r.X+r.W > vp.X+vp.W || r.Y+r.H > vp.Y+vp.H {
				t.Errorf("%dx%d: %s at %+v leaves the body %+v", c.w, c.h, tile.ID, r, vp)
			}
			for _, other := range rects {
				if homeRectsOverlap(r, other.Rect) {
					t.Errorf("%dx%d: %s overlaps %s", c.w, c.h, tile.ID, other.ID)
				}
			}
			rects = append(rects, HomeItem{ID: tile.ID, Rect: r})
		}
		for _, problem := range layout.BoundsViolations() {
			t.Errorf("%dx%d: %s", c.w, c.h, problem)
		}
		if layout.Order[0] != HomeFocusContinue || layout.Order[len(layout.dyn.tiles)] != HomeFocusSearch {
			t.Errorf("%dx%d: order starts %v", c.w, c.h, layout.Order[:len(layout.dyn.tiles)+1])
		}
	}
	layout, _ := homeTestDynLayout(t, 1280, 720, homeArrangementWidgets, "media;w:clock")
	if layout.Focus.IsEnabled(HomeFocusChat) || layout.Focus.Set(HomeFocusChat) {
		t.Error("a card that is not on the list can take the focus")
	}
	if !layout.Focus.Set("w:clock") {
		t.Error("the clock widget cannot take the focus")
	}
}

// The D-pad walks the grid by what is drawn, and leaves it for the header
// and the footer at its edges.
func TestHomeWidgetsFocusWalksTheGrid(t *testing.T) {
	layout, _ := homeTestDynLayout(t, 1280, 720, homeArrangementWidgets, "")
	layout.Focus.Set(HomeFocusSearch)
	steps := []struct {
		dir  FocusDirection
		want HomeFocusID
	}{
		{FocusDown, HomeFocusContinue},
		{FocusRight, HomeFocusMedia},
		{FocusDown, HomeFocusEmulator},
		{FocusDown, HomeFocusChat},
		{FocusLeft, HomeFocusBrowser},
		{FocusLeft, HomeFocusFavorites},
		{FocusLeft, HomeFocusFavorites},
		{FocusDown, HomeFocusExit},
		{FocusDown, HomeFocusExit},
		{FocusUp, HomeFocusFavorites},
		{FocusUp, HomeFocusContinue},
		{FocusUp, HomeFocusSearch},
	}
	for i, s := range steps {
		if got := layout.Move(s.dir); got != s.want {
			t.Fatalf("step %d: got %s, want %s", i, got, s.want)
		}
	}
}

// A grid longer than the body scrolls to the focus and back.
func TestHomeWidgetsScrollsToTheFocus(t *testing.T) {
	const tiles = "continue@3x2;media@3x1;emulator;packages;apps;favorites;browser;chat;tools;files;settings;" +
		"w:clock;w:calendar@1x1;w:weather;w:storage;w:playtime;w:nowplaying@1x1"
	layout, cfg := homeTestDynLayout(t, 1280, 720, homeArrangementWidgets, tiles)
	if layout.dyn.rows != 4 {
		t.Fatalf("%d rows, want 4", layout.dyn.rows)
	}
	layout.Focus.Set(HomeFocusFavorites)
	if got := layout.Move(FocusDown); got != "w:clock" {
		t.Fatalf("Down from Favorites = %s, want the clock", got)
	}
	if layout.dyn.first != 1 {
		t.Fatalf("first shown row = %d, want 1", layout.dyn.first)
	}
	layout.dynSync(cfg)
	vp := layout.dynViewport()
	if r := layout.Items["w:clock"].Rect; r.Y+r.H > vp.Y+vp.H || r.Y < vp.Y {
		t.Errorf("the focused clock at %+v is outside the body %+v", r, vp)
	}
	if layout.dynVisible(layout.dynTileIndex(HomeFocusMedia)) {
		t.Error("the first row is still shown after scrolling")
	}
	if got := layout.Move(FocusUp); got != HomeFocusFavorites {
		t.Fatalf("Up from the clock = %s, want Favorites", got)
	}
	if got := layout.Move(FocusUp); got != HomeFocusContinue || layout.dyn.first != 0 {
		t.Fatalf("Up from Favorites = %s with first row %d, want Continue and 0", got, layout.dyn.first)
	}
}

// The carousel goes round, keeps one card focused and hands the focus to the
// header and back.
func TestHomeCarouselWrapsAndKeepsOneFocus(t *testing.T) {
	layout, cfg := homeTestDynLayout(t, 1280, 720, homeArrangementCarousel, "")
	layout.Focus.Set(HomeFocusContinue)
	layout.dynEnsureVisible(HomeFocusContinue)
	if got := layout.Move(FocusLeft); got != HomeFocusSettings {
		t.Fatalf("Left from the first card = %s, want the last", got)
	}
	if got := layout.Move(FocusRight); got != HomeFocusContinue {
		t.Fatalf("Right from the last card = %s, want the first", got)
	}
	if got := layout.Move(FocusUp); got != HomeFocusSearch {
		t.Fatalf("Up from the carousel = %s, want the search pill", got)
	}
	if got := layout.Move(FocusDown); got != HomeFocusContinue {
		t.Fatalf("Down from the search pill = %s, want the focused card", got)
	}
	layout.dynSync(cfg)
	if got, want := layout.Items[HomeFocusContinue].Rect, layout.carRect(0); got != want {
		t.Errorf("the focused card is at %+v, want the middle %+v", got, want)
	}
	if hit := layout.dynTileAt(640, 330); hit != HomeFocusContinue {
		t.Errorf("the middle of the screen hits %s", hit)
	}
	// Hover never moves the row.
	side := layout.Items[HomeFocusMedia].Rect
	layout.FocusAt(side.X+side.W/2, side.Y+side.H/2)
	if got := layout.Focus.Current(); got != HomeFocusContinue {
		t.Errorf("hovering a side card moved the focus to %s", got)
	}
}

// Going back to a standard layout puts the cards, their order and their
// focus back exactly.
func TestHomeLeavingTheTileLayoutsRestoresTheCards(t *testing.T) {
	plain := NewHomeLayout(1280, 720)
	base := append([]HomeFocusID(nil), plain.Order...)
	layout, cfg := homeTestDynLayout(t, 1280, 720, homeArrangementWidgets, "media;w:clock")
	cfg.Variables.Custom["homeArrangement"] = homeArrangementCustom
	layout.SetArrangement(homeArrangement(cfg))
	layout.dynSync(cfg)
	if layout.dynActive() {
		t.Fatal("the dynamic body is still active")
	}
	if len(layout.Order) != len(base) {
		t.Fatalf("order has %d entries, want %d", len(layout.Order), len(base))
	}
	for i := range base {
		if layout.Order[i] != base[i] {
			t.Fatalf("order[%d] = %s, want %s", i, layout.Order[i], base[i])
		}
	}
	for _, id := range homeBodyCards {
		if !layout.Focus.IsEnabled(id) {
			t.Errorf("%s stayed off", id)
		}
	}
	if _, ok := layout.Items["w:clock"]; ok {
		t.Error("the clock widget's item survived")
	}
}

// Edit mode moves, resizes, removes and adds tiles, and saves each finished
// edit in the stored form.
func TestHomeEditMovesResizesRemovesAndAdds(t *testing.T) {
	prevLayout, prevActive := homeLayout, homeLayoutActive
	t.Cleanup(func() {
		homeEdit.open, homeEdit.work, homeEdit.holding, homeEdit.strip = false, nil, false, false
		homeEdit.sheet = homeAddSheetState{}
		homeLayout, homeLayoutActive = prevLayout, prevActive
	})
	layout, cfg := homeTestDynLayout(t, 1280, 720, homeArrangementWidgets, "")
	homeLayout, homeLayoutActive = layout, true

	homeEditBegin(cfg)
	if !homeEdit.open || !layout.dynIsTile(HomeFocusID(homeTileAddRef)) {
		t.Fatal("edit mode did not open with the add tile")
	}
	stored := func() string {
		s, _ := cfg.Variables.Custom[homeTilesKey].(string)
		return s
	}

	// Carry Media one place to the left: it goes before Continue.
	layout.Focus.Set(HomeFocusMedia)
	homeEditPrimary(cfg)
	if !homeEdit.holding {
		t.Fatal("A did not pick the tile up")
	}
	homeEditNavigate(cfg, FocusLeft)
	homeEditPrimary(cfg)
	if homeEdit.holding || !strings.HasPrefix(stored(), "media@4x1;continue;") {
		t.Fatalf("after the move: holding=%v stored %q", homeEdit.holding, stored())
	}
	if layout.Focus.Current() != HomeFocusMedia {
		t.Errorf("the carried tile lost the focus to %s", layout.Focus.Current())
	}

	// Games to 2x1.
	layout.Focus.Set(HomeFocusEmulator)
	homeEditResize(cfg)
	if !strings.Contains(stored(), "emulator@2x1") {
		t.Errorf("after the resize: %q", stored())
	}

	// The clock goes; the focus takes the tile that took its place.
	layout.Focus.Set("w:clock")
	homeEditRemove(cfg)
	if strings.Contains(stored(), "w:clock") {
		t.Errorf("after the removal: %q", stored())
	}
	if layout.dynIsTile("w:clock") || layout.Focus.Current() == "w:clock" {
		t.Error("the removed tile is still on Home")
	}

	// Battery after Games.
	homeEditInsert(cfg, "w:battery", HomeFocusEmulator)
	if !strings.Contains(stored(), "emulator@2x1;w:battery") || layout.Focus.Current() != "w:battery" {
		t.Errorf("after the insert: %q, focus %s", stored(), layout.Focus.Current())
	}

	homeEditClose(cfg)
	if homeEdit.open || layout.dynIsTile(HomeFocusID(homeTileAddRef)) {
		t.Error("closing edit mode left the add tile")
	}
	if got := homeTilesCurrent(cfg); len(got) != 12 || got[0].Ref != "media" {
		t.Errorf("the saved list reads back as %+v", got)
	}
}

// App tiles name the Start menu entry they open, and the add sheet leaves out
// what a card already does better.
func TestHomeAppTileRefs(t *testing.T) {
	action := startMenuEntry{name: "Downloads", action: func(*Config) {}}
	scene := startMenuEntry{name: "Notes", scene: "Notes"}
	if got := homeAppRefFor(action); got != "app:do:Downloads" {
		t.Errorf("action ref %q", got)
	}
	if got := homeAppRefFor(scene); got != "app:scene:Notes" {
		t.Errorf("scene ref %q", got)
	}
	if !homeAppTileOffered(action) || !homeAppTileOffered(scene) {
		t.Error("an ordinary entry is not offered")
	}
	for _, e := range []startMenuEntry{
		{name: "Quit JukaHub", action: func(*Config) {}},
		{name: "Send to Phone", action: func(*Config) {}},
		{name: "Edit Home", action: func(*Config) {}},
		{name: "Media", scene: "Tube"},
		{name: "Home", scene: "Main"},
	} {
		if homeAppTileOffered(e) {
			t.Errorf("%q is offered as a tile", e.name)
		}
	}
	if got := homeAppTitleFromRef("app:do:Smart Home"); got != "Smart Home" {
		t.Errorf("title %q", got)
	}
	if homeTileKindOf("app:do:") != homeTileUnknown || homeTileKindOf("app:scene:Notes") != homeTileApp {
		t.Error("app refs sort wrong")
	}
}
