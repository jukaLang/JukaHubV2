package main

import (
	"testing"

	"github.com/veandco/go-sdl2/ttf"
)

// TestOverlayCardsStayAboveFooter locks the vertical edge of the detail
// overlays the reflowing screens open. The trophy and Media screens now spend
// extra vertical room on wrapped chips and tabs, so their sheets and the chat
// member list must still end above the footer band at both canvases.
func TestOverlayCardsStayAboveFooter(t *testing.T) {
	if err := ttf.Init(); err != nil {
		t.Fatalf("initialize SDL_ttf: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })

	cfg := &Config{}
	label, _ := getCachedFont(cfg, "small")
	if label == nil {
		t.Fatal("the overlay font did not load")
	}
	lineH := int32(label.Height())

	for _, c := range []struct {
		name          string
		width, height int32
	}{
		{"wide", 1280, 720},
		{"compact", 960, 720},
	} {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			footer := sceneFooterBand().Y

			if r := mediaSheetRect(); r.Y+r.H > footer {
				t.Fatalf("the Media sheet ends at %d, past the footer at %d", r.Y+r.H, footer)
			}
			if r := chatProfileCardRect(); r.Y+r.H > footer {
				t.Fatalf("the chat profile card ends at %d, past the footer at %d", r.Y+r.H, footer)
			}
			for count := 1; count <= chatMembersMax; count++ {
				l := chatMembersLayoutFor(c.width, c.height, lineH, count)
				if l.rect.Y+l.rect.H > footer {
					t.Fatalf("the %d-member card ends at %d, past the footer at %d", count, l.rect.Y+l.rect.H, footer)
				}
				for i := 0; i < l.visible; i++ {
					row := l.rowRect(i)
					if row.H > 0 && row.Y+row.H > l.rect.Y+l.rect.H {
						t.Fatalf("member row %d ends at %d, past its card at %d", i, row.Y+row.H, l.rect.Y+l.rect.H)
					}
				}
			}
		})
	}
}
