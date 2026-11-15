package main

import (
	"testing"

	"github.com/veandco/go-sdl2/ttf"
)

// TestTrophyFilterChipsWrapInsideContent covers the room's filter row at both
// supported canvases. Every chip's drawn rect must sit inside the content
// gutter, below the tabs, above the card viewport, and no two chips may
// overlap. The compact canvas must actually use more than one row: that is the
// case the old clipped carousel could not show.
func TestTrophyFilterChipsWrapInsideContent(t *testing.T) {
	if err := ttf.Init(); err != nil {
		t.Fatalf("initialize SDL_ttf: %v", err)
	}
	oldW, oldH := screenWidth, screenHeight
	oldTab := trophyRoom.tab
	t.Cleanup(func() {
		screenWidth, screenHeight = oldW, oldH
		trophyRoom.tab = oldTab
	})

	cfg := &Config{}
	trophyRoom.tab = trophyTabTrophies
	for _, c := range []struct {
		name          string
		width, height int32
	}{
		{"wide", 1280, 720},
		{"compact", 960, 720},
	} {
		t.Run(c.name, func(t *testing.T) {
			screenWidth, screenHeight = c.width, c.height
			f := trophyRoomFonts(cfg)
			if f.chip == nil || f.cardTitle == nil {
				t.Fatal("the trophy room fonts did not load")
			}
			l := trophyRoomLayoutFor(cfg, f)
			if l.chipRows < 1 {
				t.Fatal("the filter chips got no row")
			}
			if c.width == 960 && l.chipRows < 2 {
				t.Fatalf("compact chips fit on %d row(s); the long labels should wrap", l.chipRows)
			}
			if want := l.chips.Y + l.chips.H + 12; l.content.Y != want {
				t.Fatalf("content starts at %d, want %d: it must follow the measured chip band", l.content.Y, want)
			}
			if l.content.Y+l.content.H > sceneFooterBand().Y-10 {
				t.Fatalf("content ends at %d, past the footer at %d", l.content.Y+l.content.H, sceneFooterBand().Y-10)
			}
			if l.content.H < l.cardH {
				t.Fatalf("content is %d tall, shorter than one %d card", l.content.H, l.cardH)
			}
			if l.cols < 1 {
				t.Fatal("the card grid got no column")
			}

			right := l.content.X + l.content.W
			for i := 0; i < trophyChipCount; i++ {
				r := l.chipRects[i]
				if r.W <= 0 || r.H <= 0 {
					t.Fatalf("chip %d has no rect: %+v", i, r)
				}
				if r.X < l.content.X || r.X+r.W > right {
					t.Fatalf("chip %q spans %d..%d outside the content %d..%d", trophyRoomChipLabel(cfg, i), r.X, r.X+r.W, l.content.X, right)
				}
				if r.Y < l.tabsY+l.tabsH || r.Y+r.H > l.chips.Y+l.chips.H {
					t.Fatalf("chip %q spans y %d..%d outside the chip band %d..%d", trophyRoomChipLabel(cfg, i), r.Y, r.Y+r.H, l.chips.Y, l.chips.Y+l.chips.H)
				}
				tw, _ := measureText(f.chip, trophyRoomChipLabel(cfg, i))
				left := r.X + 14
				if trophyRoomChipHasGlyph(i) {
					left += 16 + 6
				}
				if left+tw > r.X+r.W-14 {
					t.Fatalf("chip %q label overruns its %d px rect", trophyRoomChipLabel(cfg, i), r.W)
				}
				if got, ok := trophyChipAtPoint(l.chipRects, r.X+r.W/2, r.Y+r.H/2); !ok || got != i {
					t.Fatalf("centre of chip %q hit chip %d (ok=%v)", trophyRoomChipLabel(cfg, i), got, ok)
				}
				for j := 0; j < i; j++ {
					o := l.chipRects[j]
					if r.X < o.X+o.W && o.X < r.X+r.W && r.Y < o.Y+o.H && o.Y < r.Y+r.H {
						t.Fatalf("chips %q and %q overlap", trophyRoomChipLabel(cfg, i), trophyRoomChipLabel(cfg, j))
					}
				}
			}
		})
	}
}
