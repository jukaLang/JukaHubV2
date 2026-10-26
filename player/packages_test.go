package main

// The Package Manager is a two-pane screen: a list on the left, details on the
// right. The cursor is the only thing telling you which package the right-hand
// pane describes, and it used to be a 50-alpha accent wash plus a 3px spine —
// on a dim panel with a theme whose accent sits close to the row surface that
// reads as "one card is slightly lighter" and nothing more. These checks render
// the real list offscreen and look at the pixels: the focused row must carry a
// ring in the app's focus colour, in one row only, and its interior must be
// clearly brighter than its neighbours.

import (
	"math"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// packageListProbe renders renderPackageList for the given list and focus index
// and captures the frame. The accent is set the way startup sets it, so the
// probe counts the exact ring the app draws.
func packageListProbe(t *testing.T, pkgs []Package, focus int) (*probeFrame, Element) {
	t.Helper()
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)

	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	packagesMutex.Lock()
	prevList, prevFocus := packagesList, packagesFocusIndex
	packagesList = pkgs
	packagesFocusIndex = focus
	packagesMutex.Unlock()
	t.Cleanup(func() {
		packagesMutex.Lock()
		packagesList = prevList
		packagesFocusIndex = prevFocus
		packagesMutex.Unlock()
	})

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	t.Cleanup(func() { destroyTestRenderer(renderer) })
	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	elem := Element{Type: "packagelist", X: 60, Y: 110, Width: "1160", Height: "500"}
	renderPackageList(renderer, &Config{}, elem)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	return f, elem
}

// probePackages is a list long enough to scroll: the focused row sits between
// two neighbours, which is where a weak highlight is easiest to lose.
func probePackages(n int) []Package {
	pkgs := make([]Package, 0, n)
	for i := 0; i < n; i++ {
		pkgs = append(pkgs, Package{
			Name:        "Package " + string(rune('A'+i)),
			Author:      "someone",
			Version:     "1.2.3",
			Description: "A package used by the render probe.",
			Date:        "2026-01-01",
		})
	}
	return pkgs
}

// packageListColumn is the list column the renderer lays out: three fifths of
// the element, less the inter-column gap.
func packageListColumn(elem Element) sdl.Rect {
	elemW := getElementWidth(elem, 1160)
	return sdl.Rect{X: elem.X, Y: elem.Y, W: elemW*3/5 - 16, H: getElementHeight(elem, 500)}
}

// TestPackageManagerFocusRingIsVisible is the regression guard for "I can't
// tell which package is selected": the focused row draws a ring in the focus
// colour all the way round, and no other row draws one.
func TestPackageManagerFocusRingIsVisible(t *testing.T) {
	f, elem := packageListProbe(t, probePackages(7), 2)
	column := packageListColumn(elem)
	ring := installHighlight()

	if ring == ColorSurfaceRow {
		t.Fatalf("the focus ring colour matches the row surface: nothing to see")
	}
	// The ring must actually differ from an unfocused row, or it is a border.
	if math.Abs(colorLuma(ring)-colorLuma(ColorSurfaceRow)) < 40 {
		t.Fatalf("focus ring luma %.1f is too close to the row surface %.1f",
			colorLuma(ring), colorLuma(ColorSurfaceRow))
	}

	isRing := func(c sdl.Color) bool { return c.R == ring.R && c.G == ring.G && c.B == ring.B }
	ringPixels := countPixelsMatching(f, column, isRing)
	if ringPixels == 0 {
		t.Fatal("the focused row has no focus ring")
	}

	// Every ring pixel must belong to one row: a 2px outline of a single row is
	// a few thousand pixels, six ringed rows would be several times that.
	box, found := focusRingBounds(f, column, isRing)
	if !found {
		t.Fatal("no focus ring pixels found")
	}
	perimeter := 2 * 2 * (box.W + box.H)
	if int32(ringPixels) > perimeter*3/2 {
		t.Fatalf("%d ring pixels is more than one row's outline (%d): the cursor is not unique",
			ringPixels, perimeter)
	}
	// A ring, not a bar: it must span the row's width and its height, and be
	// light on the left/right edges as well as the top/bottom ones.
	// A row is one measured line of text now, so the ring is shorter than the
	// two-line row this probe was written against.
	if box.W < column.W/2 || box.H < 24 {
		t.Fatalf("focus ring box %dx%d does not outline a row", box.W, box.H)
	}
	if !isRing(f.at(box.X+1, box.Y+box.H/2)) || !isRing(f.at(box.X+box.W-2, box.Y+box.H/2)) {
		t.Fatal("the focus ring does not close on the row's sides")
	}

	// The interior must be brighter than the neighbouring row's: the ring marks
	// the edge, the tint marks the block, and both are what makes it read at a
	// glance on a dim screen.
	midX := box.X + box.W/2
	lit := colorLuma(f.at(midX, box.Y+box.H/2))
	pitch := box.H + 4 // rowH is lineH-4
	upper := box.Y - pitch + box.H/2
	if upper <= column.Y {
		t.Fatalf("the probe's focused row has no row above it (box y=%d column y=%d)", box.Y, column.Y)
	}
	if plain := colorLuma(f.at(midX, upper)); lit-plain < 10 {
		t.Fatalf("focused row luma %.1f is not clearly above the row above it (%.1f)", lit, plain)
	}
}

// TestPackageManagerRingSurvivesAFlatAccent covers the theme where the accent
// is the same colour as the rows: the ring still has to be visible, so the
// focus colour falls back to something with contrast instead of drawing a
// border nothing can see.
func TestPackageManagerRingSurvivesAFlatAccent(t *testing.T) {
	// installHighlight reads the global accent, so point it at a row-coloured
	// accent for the length of the call.
	prev := accentColor
	accentColor = ColorSurfaceRow
	defer func() { accentColor = prev }()
	ring := installHighlight()
	if ring == ColorSurfaceRow {
		t.Fatal("a row-coloured accent was returned as its own focus ring")
	}
	if math.Abs(colorLuma(ring)-colorLuma(ColorSurfaceRow)) < 40 {
		t.Fatalf("fallback focus ring luma %.1f has no contrast against the row surface %.1f",
			colorLuma(ring), colorLuma(ColorSurfaceRow))
	}
}

// TestPackageRowsAreCompact is the density guard: a row is one measured line
// of text — name left, version and author right — so a 500px pane holds well
// over the seven rows the two-line layout allowed.
func TestPackageRowsAreCompact(t *testing.T) {
	pitch, nameH, metaH := packageRowMetrics(nil, nil)
	if want := 2*packageRowPadY + nameH; pitch != want {
		t.Fatalf("nil-font pitch = %d, want one line of %d", pitch, want)
	}
	if metaH >= nameH {
		t.Fatalf("default meta height %d is not smaller than the name's %d", metaH, nameH)
	}
	if rows := (500 - 20) / pitch; rows < 12 {
		t.Fatalf("a 500px pane shows %d rows, want at least 12", rows)
	}
}

// TestPackageRowMetaNamesVersionAndAuthor pins the right-hand column's content:
// whichever half of the metadata the registry supplied, separator and all.
func TestPackageRowMetaNamesVersionAndAuthor(t *testing.T) {
	cases := []struct {
		p    Package
		want string
	}{
		{Package{Version: "1.2.3", Author: "someone"}, "v1.2.3  ·  someone"},
		{Package{Version: "1.2.3"}, "v1.2.3"},
		{Package{Author: "someone"}, "someone"},
		{Package{}, ""},
	}
	for _, c := range cases {
		if got := packageRowMeta(c.p); got != c.want {
			t.Errorf("packageRowMeta(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

// TestPackageRowMetaUsesTheRightEdge checks the other half of the tighten on
// real pixels: the metadata moved into the right side of the row, which used to
// be nothing but row surface.
func TestPackageRowMetaUsesTheRightEdge(t *testing.T) {
	cfg := &Config{}
	f, elem := packageListProbe(t, probePackages(7), 2)
	listW := getElementWidth(elem, 1160)*3/5 - 16
	listFont, _ := getCachedFont(cfg, elem.Font)
	smallFont, _ := getCachedFont(cfg, "small")
	if listFont == nil {
		listFont = smallFont
	}
	lineH, _, _ := packageRowMetrics(listFont, smallFont)

	rowX := elem.X + 8
	rowW := listW - 16
	rowH := lineH - 4
	rowY := elem.Y + 12 // the first row: unfocused, so no ring to exclude

	// The fill is sampled 4px inside the right edge, past the meta column's
	// own right margin, so it is bare row surface.
	fill := f.at(rowX+rowW-4, rowY+rowH/2)
	painted := 0
	for y := rowY + 4; y < rowY+rowH-4; y++ {
		for x := rowX + rowW*2/3; x < rowX+rowW-16; x++ {
			c := f.at(x, y)
			if abs32(int32(c.R)-int32(fill.R)) <= 2 &&
				abs32(int32(c.G)-int32(fill.G)) <= 2 &&
				abs32(int32(c.B)-int32(fill.B)) <= 2 {
				continue
			}
			painted++
		}
	}
	if painted < 20 {
		t.Fatalf("the right third of a package row holds %d painted pixels: the meta column is missing", painted)
	}
}

// packageRowRect is the row rectangle renderPackageList draws for row n of the
// list, measured off the same fonts the renderer uses. A row is one line of text
// plus its padding.
func packageRowRect(t *testing.T, elem Element, row int) sdl.Rect {
	t.Helper()
	listW := getElementWidth(elem, 1160)*3/5 - 16
	listFont, _ := getCachedFont(&Config{}, elem.Font)
	smallFont, _ := getCachedFont(&Config{}, "small")
	if listFont == nil {
		listFont = smallFont
	}
	lineH, _, _ := packageRowMetrics(listFont, smallFont)
	return sdl.Rect{
		X: elem.X + 8,
		Y: elem.Y + 10 + int32(row)*lineH + 2,
		W: listW - 16,
		H: lineH - 4,
	}
}

// markPackagesInstalled puts the named packages in the in-memory set the rows
// read, and restores the set afterwards.
func markPackagesInstalled(t *testing.T, names ...string) {
	t.Helper()
	installedPackagesMu.Lock()
	prev := installedPackages
	set := make(map[string]bool, len(prev)+len(names))
	for k, v := range prev {
		set[k] = v
	}
	for _, n := range names {
		set[packageDirName(n)] = true
	}
	installedPackages = set
	installedPackagesMu.Unlock()
	t.Cleanup(func() {
		installedPackagesMu.Lock()
		installedPackages = prev
		installedPackagesMu.Unlock()
	})
}

// TestPackageRowsMarkInstalledPackages is the request, on pixels: a package that
// is already on the card carries the app's check glyph in the row's left gutter,
// and a package that is not carries nothing there. The details pane already said
// "Installed"; the list is where the question "what do I still need" is asked.
func TestPackageRowsMarkInstalledPackages(t *testing.T) {
	pkgs := probePackages(7)
	markPackagesInstalled(t, pkgs[2].Name)
	f, elem := packageListProbe(t, pkgs, 0)

	mark := ColorSuccess
	isMark := func(c sdl.Color) bool {
		return abs32(int32(c.R)-int32(mark.R)) <= 12 &&
			abs32(int32(c.G)-int32(mark.G)) <= 12 &&
			abs32(int32(c.B)-int32(mark.B)) <= 12
	}

	// The gutter only: the name and the meta column are elsewhere in the row.
	smallFont, _ := getCachedFont(&Config{}, "small")
	if smallFont == nil {
		t.Fatal("no small font")
	}
	installW, _, err := smallFont.SizeUTF8(packageInstalledMark)
	if err != nil {
		t.Fatalf("cannot measure the mark: %v", err)
	}
	gutter := sdl.Rect{X: elem.X + 8, Y: elem.Y, W: int32(installW) + 10, H: 500}

	marked := packageRowRect(t, elem, 2)
	marked.X, marked.W = gutter.X, gutter.W
	if got := countPixelsMatching(f, marked, isMark); got < 12 {
		t.Fatalf("the installed row drew %d mark pixels, want the check glyph", got)
	}
	for _, row := range []int{1, 3} {
		plain := packageRowRect(t, elem, row)
		plain.X, plain.W = gutter.X, gutter.W
		if got := countPixelsMatching(f, plain, isMark); got != 0 {
			t.Fatalf("row %d is not installed but drew %d mark pixels", row, got)
		}
	}
}

// TestPackageInstalledFlagsMatchTheNames pins the mapping the rows rely on: the
// set is keyed by install directory, which is the sanitised name, not the name
// the registry spells.
func TestPackageInstalledFlagsMatchTheNames(t *testing.T) {
	markPackagesInstalled(t, "Some Package: Deluxe")
	pkgs := []Package{
		{Name: "Some Package: Deluxe"},
		{Name: "Other"},
	}
	flags := packageInstalledFlags(pkgs)
	if len(flags) != 2 {
		t.Fatalf("%d flags for 2 packages", len(flags))
	}
	if !flags[0] {
		t.Fatal("an installed package is not flagged: the name was not sanitised the same way")
	}
	if flags[1] {
		t.Fatal("an uninstalled package is flagged as installed")
	}
}

// abs32 is the distance between two colour channels, for the fill comparison.
func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// focusRingBounds is the bounding box of the pixels matching match inside rect.
func focusRingBounds(f *probeFrame, rect sdl.Rect, match func(sdl.Color) bool) (sdl.Rect, bool) {
	box := sdl.Rect{X: probeW, Y: probeH, W: 0, H: 0}
	x0, y0, x1, y1 := probeW, probeH, int32(-1), int32(-1)
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			if x < 0 || y < 0 || x >= probeW || y >= probeH {
				continue
			}
			if !match(f.at(x, y)) {
				continue
			}
			if x < x0 {
				x0 = x
			}
			if y < y0 {
				y0 = y
			}
			if x > x1 {
				x1 = x
			}
			if y > y1 {
				y1 = y
			}
		}
	}
	if x1 < 0 {
		return box, false
	}
	return sdl.Rect{X: x0, Y: y0, W: x1 - x0 + 1, H: y1 - y0 + 1}, true
}
