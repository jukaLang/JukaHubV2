package main

import (
	"testing"

	"github.com/veandco/go-sdl2/ttf"
)

// TestMediaTabsWrapInsideContent is the Media counterpart of the trophy chip
// geometry test: every tab must sit inside the content gutter, the compact
// canvas must wrap the strip, and the content viewport must start below the
// rows that were actually drawn.
func TestMediaTabsWrapInsideContent(t *testing.T) {
	if err := ttf.Init(); err != nil {
		t.Fatalf("initialize SDL_ttf: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	oldSearch, oldTag := mediaHub.hasSearch, mediaHub.downloadsTag
	t.Cleanup(func() {
		screenWidth, screenHeight = oldW, oldH
		mediaHub.hasSearch, mediaHub.downloadsTag = oldSearch, oldTag
	})

	cfg := &Config{}
	mediaHub.hasSearch, mediaHub.downloadsTag = false, ""
	for _, c := range []struct {
		name          string
		width, height int32
	}{
		{"wide", 1280, 720},
		{"compact", 960, 720},
	} {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			l := mediaHubComputeLayout(cfg, 0)
			tabs := mediaHubTabs()
			if l.tabRows < 1 {
				t.Fatal("the media tabs got no row")
			}
			if c.width == 960 && l.tabRows < 2 {
				t.Fatalf("compact media tabs fit on %d row(s); the strip should wrap", l.tabRows)
			}
			if l.tabs.H < 38 {
				t.Fatalf("tab band is %d tall", l.tabs.H)
			}
			if l.content.Y != l.tabs.Y+l.tabs.H+14 {
				t.Fatalf("content starts at %d, tab band ends at %d", l.content.Y, l.tabs.Y+l.tabs.H)
			}
			if l.content.Y+l.content.H > sceneFooterBand().Y-10 {
				t.Fatalf("content ends at %d, past the footer at %d", l.content.Y+l.content.H, sceneFooterBand().Y-10)
			}
			if l.content.H < 80 {
				t.Fatalf("content is only %d tall", l.content.H)
			}
			font := FontForRole(cfg, TypeLabel)
			if font == nil {
				t.Fatal("the tab font did not load")
			}
			right := l.tabs.X + l.tabs.W
			for i, tab := range tabs {
				r := l.tabRects[i]
				if r.W <= 0 || r.H <= 0 {
					t.Fatalf("tab %q has no rect: %+v", mediaTabLabel(tab), r)
				}
				if r.X < l.tabs.X || r.X+r.W > right {
					t.Fatalf("tab %q spans %d..%d outside the tab band %d..%d", mediaTabLabel(tab), r.X, r.X+r.W, l.tabs.X, right)
				}
				if r.Y < l.tabs.Y || r.Y+r.H > l.tabs.Y+l.tabs.H {
					t.Fatalf("tab %q spans y %d..%d outside the band %d..%d", mediaTabLabel(tab), r.Y, r.Y+r.H, l.tabs.Y, l.tabs.Y+l.tabs.H)
				}
				tw, _ := measureText(font, mediaTabLabel(tab))
				if r.X+14+18+8+tw > r.X+r.W-16 {
					t.Fatalf("tab %q label overruns its %d px rect", mediaTabLabel(tab), r.W)
				}
				for j := 0; j < i; j++ {
					o := l.tabRects[j]
					if r.X < o.X+o.W && o.X < r.X+r.W && r.Y < o.Y+o.H && o.Y < r.Y+r.H {
						t.Fatalf("tabs %q and %q overlap", mediaTabLabel(tab), mediaTabLabel(tabs[j]))
					}
				}
			}
		})
	}
}
