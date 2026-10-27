package main

// netplay_render_test.go — the panel is drawn offscreen so the layout math and
// the click targets are checked against real geometry instead of by eye. A
// panel taller than the screen or a row that does not match its hit box would
// make LAN play unreachable on the handheld.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestNetplayPanelRendersInsideTheScreen(t *testing.T) {
	netplayTestPanel(t, "GBA")
	f := newProbeFrame(t)
	if font, _ := getCachedFont(&Config{}, "small"); font == nil {
		t.Fatalf("no usable font in this environment: the shipped Inter-Regular.ttf did not load, so this check cannot run")
	}

	prevW, prevH := screenWidth, screenHeight
	screenWidth, screenHeight = probeW, probeH
	t.Cleanup(func() { screenWidth, screenHeight = prevW, prevH })

	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Fatalf("could not create the offscreen software renderer: %v (SDL video is already up and the dummy driver supports it, so this is a defect, not an environment gate)", err)
	}
	defer destroyTestRenderer(renderer)
	ClearTextCache()

	renderer.SetDrawColor(f.bg.R, f.bg.G, f.bg.B, 255)
	renderer.Clear()
	renderNetplayPanel(renderer, &Config{})

	panel := netplayPanel.panel
	if panel.W <= 0 || panel.H <= 0 {
		t.Fatalf("panel rect = %+v", panel)
	}
	if panel.X < 0 || panel.Y < 0 || panel.X+panel.W > probeW || panel.Y+panel.H > probeH {
		t.Fatalf("panel %+v does not fit in %dx%d", panel, probeW, probeH)
	}
	if panel.W > probeW-48 && panel.W != probeW-48 {
		t.Fatalf("panel width %d exceeds the 48px margin", panel.W)
	}

	hits := netplayPanel.rowHits
	if len(hits) != len(netplayMenuRows) {
		t.Fatalf("%d row hits for %d rows", len(hits), len(netplayMenuRows))
	}
	for i, hit := range hits {
		if hit.index != i {
			t.Fatalf("hit %d carries index %d", i, hit.index)
		}
		if hit.rect.X < panel.X || hit.rect.X+hit.rect.W > panel.X+panel.W {
			t.Fatalf("row %d spans %+v outside panel %+v", i, hit.rect, panel)
		}
		if hit.rect.Y < panel.Y || hit.rect.Y+hit.rect.H > panel.Y+panel.H {
			t.Fatalf("row %d sits at %+v outside panel %+v", i, hit.rect, panel)
		}
		if i > 0 && hit.rect.Y <= hits[i-1].rect.Y {
			t.Fatalf("row %d is not below row %d: %+v", i, i-1, hit.rect)
		}
	}

	// Clicking the second row selects it; clicking the backdrop closes the
	// panel instead of leaking the click to the library underneath.
	mx, my := hits[1].rect.X+6, hits[1].rect.Y+6
	if !handleNetplayMouse(mx, my) || netplayPanel.cursor != 1 {
		t.Fatalf("click at %d,%d left cursor=%d", mx, my, netplayPanel.cursor)
	}
	if !handleNetplayMouse(2, 2) || netplayPanel.open {
		t.Fatal("click outside the panel did not close it")
	}
}
