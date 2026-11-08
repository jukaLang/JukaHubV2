package main

// setwidgets_test.go — the controls the Settings hub is built from measure
// before they draw: a pill is as wide as its word (the theme gallery's
// "Current" badge used to be smaller than its own text), a carousel is as wide
// as its widest value, and a tap lands on the part it looks like it lands on.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestSetPillIsSizedFromItsText(t *testing.T) {
	font := testInterFont(t, 14)
	w, h := setPillSize(font, "Current")
	tw, th := measureText(font, "Current")
	if tw <= 0 || th <= 0 {
		t.Fatal("the probe font measured nothing")
	}
	if w < tw+16 {
		t.Errorf("the pill is %dpx wide for a %dpx word: its rounded ends cut into the letters", w, tw)
	}
	if h < th+6 {
		t.Errorf("the pill is %dpx tall for %dpx text", h, th)
	}
	longW, _ := setPillSize(font, "Current theme")
	if longW <= w {
		t.Errorf("a longer word got a pill no wider (%d vs %d)", longW, w)
	}
	if ew, eh := setPillSize(font, ""); ew != 0 || eh != 0 {
		t.Errorf("an empty pill is %dx%d, want nothing", ew, eh)
	}
}

func TestSetCarouselIsSizedFromItsWidestValue(t *testing.T) {
	font := testInterFont(t, 17)
	const h = int32(40)
	options := []setOption{{Label: "Auto · 720p"}, {Label: "360p"}, {Label: "1080p"}}
	w := setCarouselWidthFor(font, options, "360p", h, 0, 0)
	widest, _ := measureText(font, "Auto · 720p")
	if inner := setCarouselLayout(sdl.Rect{W: w, H: h}).Value.W; inner < widest {
		t.Errorf("the value well is %dpx for a %dpx widest value: cycling would cut it short", inner, widest)
	}
	// The width does not follow the value on show, so the arrows stay put.
	if other := setCarouselWidthFor(font, options, "1080p", h, 0, 0); other != w {
		t.Errorf("the carousel is %dpx on one value and %dpx on another", w, other)
	}
	if capped := setCarouselWidthFor(font, options, "360p", h, 0, 100); capped != 100 {
		t.Errorf("a 100px cap gave %dpx", capped)
	}
	if floor := setCarouselWidthFor(font, []setOption{{Label: "x"}}, "x", h, 150, 0); floor != 150 {
		t.Errorf("a 150px floor gave %dpx", floor)
	}
	plain := setCarouselWidthFor(font, []setOption{{Label: "Slate"}}, "Slate", h, 0, 0)
	chip := setCarouselWidthFor(font, []setOption{{Label: "Slate", Swatch: "#2E4061"}}, "Slate", h, 0, 0)
	if chip-plain != setCarouselChip+8 {
		t.Errorf("a colour chip adds %dpx, want %d", chip-plain, setCarouselChip+8)
	}
}

func TestSetCarouselHitMatchesItsParts(t *testing.T) {
	rect := sdl.Rect{X: 100, Y: 50, W: 260, H: 40}
	parts := setCarouselLayout(rect)
	if parts.Prev.X != rect.X || parts.Next.X+parts.Next.W != rect.X+rect.W {
		t.Fatalf("the arrows are not at the ends: %+v in %+v", parts, rect)
	}
	if parts.Value.X < parts.Prev.X+parts.Prev.W || parts.Value.X+parts.Value.W > parts.Next.X {
		t.Fatalf("the value overlaps an arrow: %+v", parts)
	}
	centre := func(r sdl.Rect) (int32, int32) { return r.X + r.W/2, r.Y + r.H/2 }
	for _, c := range []struct {
		name string
		at   sdl.Rect
		part int
		hit  bool
	}{
		{"the left arrow", parts.Prev, -1, true},
		{"the right arrow", parts.Next, 1, true},
		{"the value", parts.Value, 0, true},
	} {
		x, y := centre(c.at)
		part, hit := setCarouselHit(parts, x, y)
		if part != c.part || hit != c.hit {
			t.Errorf("a tap on %s resolved to part %d (hit %v), want %d", c.name, part, hit, c.part)
		}
	}
	if _, hit := setCarouselHit(parts, rect.X+rect.W/2, rect.Y+rect.H+200); hit {
		t.Error("a tap far below the carousel counted as a hit")
	}
}

func TestSetCycleIndexWraps(t *testing.T) {
	for _, c := range []struct{ i, dir, n, want int }{
		{0, 1, 3, 1},
		{2, 1, 3, 0},
		{0, -1, 3, 2},
		{1, -1, 3, 0},
		{0, 1, 0, 0},
		{5, 1, 3, 0},
	} {
		if got := setCycleIndex(c.i, c.dir, c.n); got != c.want {
			t.Errorf("setCycleIndex(%d, %d, %d) = %d, want %d", c.i, c.dir, c.n, got, c.want)
		}
	}
}

func TestSetSwitchKeepsItsProportions(t *testing.T) {
	for _, rowH := range []int32{20, 44, 58, 64, 120} {
		w, h := setSwitchSize(rowH)
		if h < 18 || h > 30 {
			t.Errorf("row %d: the switch is %dpx tall, want 18..30", rowH, h)
		}
		if w != 2*h+h/4 {
			t.Errorf("row %d: the switch is %dx%d, want a little over twice as wide as tall", rowH, w, h)
		}
	}
}
