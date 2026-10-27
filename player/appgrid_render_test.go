package main

// The Apps grid has to show an installed app's own icon, not just its initial,
// and every tile has to stay inside the panel. These checks render the real
// grid offscreen and look at the pixels.

import (
	"image/color"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// appGridProbeFrame renders the launcher with the given entries and returns the
// captured frame plus the tile rects it was laid out into.
func appGridProbeFrame(t *testing.T, entries []FileEntry, focus int) (*probeFrame, appGridLayout) {
	t.Helper()
	withScreen(t, probeW, probeH)
	ClearTextCache()
	t.Cleanup(ClearTextCache)
	t.Cleanup(clearAppIconCache)

	config := &Config{}
	config.Variables.Custom = map[string]interface{}{appsEntriesVar: entries}
	element := Element{
		Type:     "dynamiclist",
		Variable: appsEntriesVar,
		X:        24,
		Y:        100,
		Width:    "1232",
		Height:   "488",
	}
	prevFocus := focusedFileIndex
	focusedFileIndex = focus
	t.Cleanup(func() { focusedFileIndex = prevFocus })
	// The accent is normally set from the config at startup; the probe needs it
	// so the focus ring it counts is the one the app draws.
	prevAccent := accentColor
	accentColor = ColorAccent
	t.Cleanup(func() { accentColor = prevAccent })

	f := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderAppGrid(renderer, config, element)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Fatalf("ReadPixels failed on the offscreen renderer: %v (the frame assertions that follow would otherwise never run)", err)
	}
	return f, newAppGridLayout(element, appGridElementWidth(element))
}

// countPixelsMatching counts pixels inside rect that satisfy match.
func countPixelsMatching(f *probeFrame, rect sdl.Rect, match func(sdl.Color) bool) int {
	n := 0
	for y := rect.Y; y < rect.Y+rect.H; y++ {
		for x := rect.X; x < rect.X+rect.W; x++ {
			if x < 0 || y < 0 || x >= probeW || y >= probeH {
				continue
			}
			if match(f.at(x, y)) {
				n++
			}
		}
	}
	return n
}

func appGridIconRect(cell sdl.Rect) sdl.Rect {
	return sdl.Rect{
		X: cell.X + (cell.W-appIconBox)/2,
		Y: cell.Y + appIconTop,
		W: appIconBox,
		H: appIconBox,
	}
}

func TestAppGridDrawsTheAppsOwnIcon(t *testing.T) {
	dir := t.TempDir()
	icon := filepath.Join(dir, "Iconed.png")
	writeSolidPNG(t, icon, 64, 64, color.RGBA{R: 255, G: 40, B: 40, A: 255})

	entries := []FileEntry{
		{Name: "Iconed", Path: filepath.Join(dir, "Iconed.exe"), Icon: icon},
		{Name: "Plain", Path: filepath.Join(dir, "Plain.exe")},
	}
	f, layout := appGridProbeFrame(t, entries, 1)

	red := func(c sdl.Color) bool { return c.R > 180 && c.G < 90 && c.B < 90 }
	iconed := appGridIconRect(appGridCellRect(layout, 0, 0))
	if n := countPixelsMatching(f, iconed, red); n < 500 {
		t.Errorf("tile 0 painted %d red pixels: the app icon was not drawn", n)
	}

	plain := appGridIconRect(appGridCellRect(layout, 1, 0))
	if n := countPixelsMatching(f, plain, red); n > 0 {
		t.Errorf("tile 1 painted %d red pixels although it has no icon", n)
	}
	// A tile without artwork must still show its initial rather than an empty
	// well, so its icon box carries some pixels brighter than the card.
	bright := func(c sdl.Color) bool { return int(c.R)+int(c.G)+int(c.B) > 240 }
	if n := countPixelsMatching(f, plain, bright); n < 20 {
		t.Errorf("tile 1 painted %d bright pixels: the initial is missing", n)
	}
}

// The grid must not draw over its own header or outside the panel, and the
// focused tile has to be inside it too.
func TestAppGridKeepsTilesInsideThePanel(t *testing.T) {
	entries := make([]FileEntry, 0, 20)
	for _, name := range []string{
		"Alpha", "Beta", "Gamma", "Delta", "Epsilon", "Zeta", "Eta",
		"Theta", "Iota", "Kappa", "Lambda", "Mu", "Nu", "Xi",
		"Omicron", "Pi", "Rho", "Sigma", "Tau", "Upsilon",
	} {
		entries = append(entries, FileEntry{Name: name, Path: "/apps/" + name + ".exe"})
	}
	_, layout := appGridProbeFrame(t, entries, 0)

	if layout.cols < 5 {
		t.Fatalf("columns = %d, want a dense launcher on a 1232px panel", layout.cols)
	}
	first := appGridCellRect(layout, 0, 0)
	if first.Y < 100+appGridHeadH {
		t.Errorf("the first row starts at y=%d, inside the header band", first.Y)
	}
	lastCol := appGridCellRect(layout, layout.cols-1, 0)
	if lastCol.X+lastCol.W > 24+1232 {
		t.Errorf("the last column ends at %d, past the panel", lastCol.X+lastCol.W)
	}
	// Three rows of tiles have to fit the 488px panel, so the common case does
	// not scroll on the device.
	if rows := appGridRows(Element{X: 24, Y: 100, Width: "1232", Height: "488"}, 488, layout); rows < 3 {
		t.Errorf("visible rows = %d, want at least 3", rows)
	}
}
