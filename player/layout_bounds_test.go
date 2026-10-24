package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// Every card must stay inside the viewport and clear of its neighbours at the
// 720p design size and at the smaller handheld resolutions JukaHub targets.
// A regression here renders as a clipped or unreachable tile.
func TestHomeLayoutFitsViewport(t *testing.T) {
	sizes := []struct {
		name string
		w, h int32
	}{
		{"720p design", 1280, 720},
		{"TrimUI Smart Pro", 1280, 720},
		{"small handheld", 960, 544},
		{"tiny handheld", 800, 480},
		{"desktop 1080p", 1920, 1080},
	}
	for _, size := range sizes {
		t.Run(size.name, func(t *testing.T) {
			h := NewHomeLayout(size.w, size.h)
			// 14 cards plus the update download chip, the now-playing chip
			// and the notification bell in the header band.
			if got, want := len(h.Order), 17; got != want {
				t.Fatalf("items = %d, want %d", got, want)
			}
			for _, problem := range h.BoundsViolations() {
				t.Errorf("%s", problem)
			}
			// Each card's centre must hit-test back to that card, so focus,
			// mouse and touch all agree with what is drawn.
			for _, id := range h.Order {
				item := h.Items[id]
				cx := item.Rect.X + item.Rect.W/2
				cy := item.Rect.Y + item.Rect.H/2
				if !h.FocusAt(cx, cy) {
					t.Errorf("%s: centre (%d,%d) misses its card %+v", id, cx, cy, item.Rect)
					continue
				}
				if got := h.Focus.Current(); got != id {
					t.Errorf("%s: centre hit %s instead", id, got)
				}
			}
		})
	}
}

// The header/footer bands must cover the full width so the top bar and footer
// never show a gap at any supported size.
func TestHomeHeaderAndFooterSpanWidth(t *testing.T) {
	for _, size := range []struct{ w, h int32 }{{1280, 720}, {960, 544}, {800, 480}} {
		h := NewHomeLayout(size.w, size.h)
		header := ScaleHomeRect(sdl.Rect{X: 0, Y: 0, W: 1280, H: 56}, h.Width, h.Height)
		if header.X != 0 || header.W != h.Width {
			t.Errorf("%dx%d header %+v does not span the width", size.w, size.h, header)
		}
		footer := ScaleHomeRect(sdl.Rect{X: 0, Y: 672, W: 1280, H: 48}, h.Width, h.Height)
		if footer.Y+footer.H > h.Height {
			t.Errorf("%dx%d footer %+v falls below the viewport", size.w, size.h, footer)
		}
		exit := h.Items[HomeFocusExit].Rect
		if exit.Y < footer.Y || exit.Y+exit.H > h.Height {
			t.Errorf("%dx%d Exit chip %+v is outside the footer band %+v", size.w, size.h, exit, footer)
		}
	}
}
