package main

// downloadsui_test.go — offscreen checks for the Downloads panel. The panel is
// drawn with the software renderer against a known background, so the geometry
// the input handlers depend on (row and tab hit rects, the storage card, the
// scroll window) is verified on real pixels instead of by inspection.

import (
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func downloadUITestConfig() *Config {
	return &Config{Variables: Variables{Custom: map[string]interface{}{}}}
}

// withDownloadsPanelState seeds the queue and the panel, then restores the
// package globals so a failing probe cannot leak into the next test.
func withDownloadsPanelState(t *testing.T, items []*downloadItem, cursor int) {
	t.Helper()
	downloads.mu.Lock()
	prevItems, prevID := downloads.items, downloads.nextID
	downloads.items = items
	downloads.nextID = int64(len(items))
	downloads.mu.Unlock()
	prevRoot, prevOpen, prevTab, prevCursor, prevScroll := downloadsRootOverride,
		dlPanel.open, dlPanel.tab, dlPanel.cursor, dlPanel.scroll
	downloadsRootOverride = t.TempDir()
	dlPanel.open = true
	dlPanel.tab = downloadTabAll
	dlPanel.cursor = cursor
	dlPanel.scroll = 0
	t.Cleanup(func() {
		downloads.mu.Lock()
		downloads.items = prevItems
		downloads.nextID = prevID
		downloads.mu.Unlock()
		downloadsRootOverride = prevRoot
		dlPanel.open, dlPanel.tab, dlPanel.cursor, dlPanel.scroll = prevOpen, prevTab, prevCursor, prevScroll
		dlPanel.rows, dlPanel.hits, dlPanel.tabs = nil, nil, nil
		dlPanel.panel = sdl.Rect{}
	})
}

func renderDownloadsProbe(t *testing.T, config *Config) (*probeFrame, sdl.Rect, []sdl.Rect, []sdl.Rect) {
	t.Helper()
	f := newProbeFrame(t)
	if font, _ := getCachedFont(config, "small"); font == nil {
		t.Skip("no usable font in this environment")
	}
	prevW, prevH := screenWidth, screenHeight
	prevAccent := accentColor
	screenWidth, screenHeight = probeW, probeH
	accentColor = ColorAccent
	t.Cleanup(func() {
		screenWidth, screenHeight = prevW, prevH
		accentColor = prevAccent
	})

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderDownloadsPanel(renderer, config)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&f.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("renderer.ReadPixels unavailable: %v", err)
	}
	hits := make([]sdl.Rect, 0, len(dlPanel.hits))
	for _, hit := range dlPanel.hits {
		hits = append(hits, hit.rect)
	}
	return f, dlPanel.panel, hits, append([]sdl.Rect(nil), dlPanel.tabs...)
}

func TestDownloadsPanelRendersRowsAndTabs(t *testing.T) {
	items := []*downloadItem{
		{ID: 1, URL: "https://example.com/a", Status: downloadActive, Title: "Night Drive Theory",
			Source: "Lumen Coast", Total: 8 << 20, Done: 6 << 20, Speed: 2 << 20},
		{ID: 2, URL: "https://example.com/b", Status: downloadQueued, Title: "Sodium Lights"},
		{ID: 3, URL: "https://example.com/c", Status: downloadFailed, Source: "Lumen Coast", Err: "Video unavailable"},
		{ID: 4, URL: "https://example.com/d", Status: downloadDone, Title: "Overpass", Done: 5 << 20},
	}
	withDownloadsPanelState(t, items, 0)
	f, panel, hits, tabs := renderDownloadsProbe(t, downloadUITestConfig())

	if panel.W <= 0 || panel.H <= 0 {
		t.Fatal("the panel recorded no rect")
	}
	if panel.X < 0 || panel.Y < 0 || panel.X+panel.W > probeW || panel.Y+panel.H > probeH {
		t.Fatalf("panel %+v does not fit the screen", panel)
	}
	// Six rows do not all fit at 720p: the list scrolls, so only the visible
	// rows carry hit rects, starting with Add a link.
	if len(hits) < 3 {
		t.Fatalf("%d visible rows, want at least 3", len(hits))
	}
	// A partially visible row is fine (the list clips at the footer band), but
	// no row may start inside it.
	if last := hits[len(hits)-1]; last.Y >= panel.Y+panel.H-30 {
		t.Fatalf("the last visible row %+v starts inside the footer", last)
	}
	for i, r := range hits {
		if r.X < panel.X || r.X+r.W > panel.X+panel.W {
			t.Errorf("row %d %+v is outside the panel", i, r)
		}
	}
	if len(tabs) != 3 {
		t.Fatalf("%d tab rects, want 3", len(tabs))
	}
	if tabs[1].X < tabs[0].X+tabs[0].W-1 {
		t.Errorf("tabs overlap: %+v %+v", tabs[0], tabs[1])
	}

	// The panel is a real surface with a storage card, and the selected row's
	// accent tile is painted: a black frame would fail all three.
	if got := f.countColor(ColorSurface, panel.X+2, panel.Y+2, panel.X+panel.W-2, panel.Y+panel.H-2); got < 5000 {
		t.Errorf("panel surface painted only %d pixels", got)
	}
	// The storage card is the only raised surface above the first row.
	if got := f.countColor(ColorPanelRaised, panel.X+40, panel.Y+60, panel.X+panel.W-40, panel.Y+250); got < 5000 {
		t.Errorf("storage card painted only %d pixels", got)
	}
	// The Add a link glyph tile is a tinted square with a bright plus, so it is
	// clearly lighter than the row surface beside it.
	add := hits[0]
	tile := sdl.Rect{X: add.X + 18, Y: add.Y + (add.H-30)/2, W: 30, H: 30}
	// The strip between the panel edge and the rows is bare panel surface.
	panelEdge := sdl.Rect{X: panel.X + 8, Y: tile.Y, W: 16, H: 30}
	if tileLuma, edgeLuma := bandLuma(f, tile), bandLuma(f, panelEdge); tileLuma <= edgeLuma+20 {
		t.Errorf("Add a link tile luma %d is not brighter than the panel (%d)", tileLuma, edgeLuma)
	}

	// A progress bar is drawn for the running row, to the right of its art
	// tile and left of the percent/speed column.
	running := hits[1]
	if got := f.countColor(accentColor, running.X+64, running.Y, running.X+running.W-150, running.Y+running.H); got < 100 {
		t.Errorf("the running row's progress bar painted %d accent pixels", got)
	}
}

func TestDownloadsPanelScrollsToTheCursor(t *testing.T) {
	var items []*downloadItem
	for i := 0; i < 12; i++ {
		items = append(items, &downloadItem{
			ID: int64(i + 1), URL: "https://example.com/x", Status: downloadDone,
			Title: "Track", Done: 1 << 20,
		})
	}
	withDownloadsPanelState(t, items, 12) // the last row
	f, panel, hits, _ := renderDownloadsProbe(t, downloadUITestConfig())
	if dlPanel.scroll <= 0 {
		t.Fatal("a long list did not scroll to keep the cursor visible")
	}
	last := hits[len(hits)-1]
	if last.Y < panel.Y || last.Y+last.H > panel.Y+panel.H {
		t.Fatalf("the focused row %+v fell outside the panel %+v", last, panel)
	}
	// The rows that scrolled off are clipped rather than painted over the card.
	if got := f.countColor(ColorPanelRaised, panel.X+40, panel.Y+60, panel.X+panel.W-40, panel.Y+250); got < 5000 {
		t.Errorf("list painted over the storage card (%d card pixels left)", got)
	}
}

func TestDownloadsPanelTabsFilterRows(t *testing.T) {
	items := []*downloadItem{
		{ID: 1, URL: "https://example.com/a", Status: downloadDone, Title: "Saved one", Done: 1 << 20},
		{ID: 2, URL: "https://example.com/b", Status: downloadFailed, Title: "Broken"},
	}
	withDownloadsPanelState(t, items, 0)
	dlPanel.tab = downloadTabSaved
	_, _, hits, _ := renderDownloadsProbe(t, downloadUITestConfig())
	if len(hits) != 2 {
		t.Fatalf("saved tab drew %d rows, want 2 (Add a link + the saved track)", len(hits))
	}

	// Mouse and controller both go through the same tab switch.
	dlPanel.cursor = 0
	handleDownloadsMouse(5, 5, downloadUITestConfig())
	if dlPanel.open {
		t.Error("a click outside the panel did not close it")
	}
}
