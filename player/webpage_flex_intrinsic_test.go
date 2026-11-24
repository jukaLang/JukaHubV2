package main

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func webTestBackgroundsOf(t *testing.T, layout *webLayout, color sdl.Color) []sdl.Rect {
	t.Helper()
	var rects []sdl.Rect
	for _, bg := range layout.Backgrounds {
		if bg.Color.R == color.R && bg.Color.G == color.G && bg.Color.B == color.B {
			rects = append(rects, bg.Rect)
		}
	}
	return rects
}

func TestWebFlexMinContentFloorShrinksWithBasisAndGap(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.row{display:flex;width:300px;gap:20px}` +
		`.long{flex:1 1 200px;background:#cc0000}` +
		`.short{flex:1 1 200px;background:#0000cc}` +
		`</style><div class="row"><div class="long">Supercalifragilisticexpialidocious</div>` +
		`<div class="short">Short</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	long := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	short := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	if len(long) != 1 || len(short) != 1 {
		t.Fatalf("expected one long and one short item, got %d/%d", len(long), len(short))
	}
	if abs32(long[0].Y-short[0].Y) > 2 {
		t.Fatalf("the two items should share one row: long=%+v short=%+v", long[0], short[0])
	}
	if abs32(long[0].W-306) > 6 {
		t.Fatalf("long word's min-content width = %d, want 306 (±6): %+v", long[0].W, long[0])
	}
	if abs32(short[0].W-130) > 6 {
		t.Fatalf("short item width = %d, want 130 after yielding the excess: %+v", short[0].W, short[0])
	}
	if abs32(long[0].X-2) > 4 || abs32(short[0].X-330) > 6 {
		t.Fatalf("item positions = %d/%d, want 2/330: %+v %+v", long[0].X, short[0].X, long[0], short[0])
	}
	if gap := short[0].X - (long[0].X + long[0].W); gap < 20 || gap > 24 {
		t.Fatalf("item gap = %d, want the scaled 20px gap (background edges subtract 8px)", gap)
	}

	// An explicit min-width: 0 replaces the automatic content floor. The long
	// word may then shrink to the same share as its sibling.
	zeroSource := strings.Replace(source, ".long{flex:1 1 200px", ".long{min-width:0;flex:1 1 200px", 1)
	zeroLayout := parseWebPage(zeroSource, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	zeroLong := webTestBackgroundsOf(t, zeroLayout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	zeroShort := webTestBackgroundsOf(t, zeroLayout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	if len(zeroLong) != 1 || len(zeroShort) != 1 {
		t.Fatalf("min-width:0 produced %d/%d item boxes", len(zeroLong), len(zeroShort))
	}
	if abs32(zeroLong[0].W-218) > 6 || abs32(zeroShort[0].W-218) > 6 {
		t.Fatalf("min-width:0 should leave equal 218px shares, got long=%+v short=%+v", zeroLong[0], zeroShort[0])
	}
}

func TestWebFlexAutoMinContentFloorWithoutBasis(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.row{display:flex;width:300px;gap:20px}` +
		`.long{background:#cc0000}.short{background:#0000cc}` +
		`</style><div class="row"><div class="long">Supercalifragilisticexpialidocious</div>` +
		`<div class="short">Short</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	long := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	short := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	if len(long) != 1 || len(short) != 1 {
		t.Fatalf("expected one long and one short item, got %d/%d", len(long), len(short))
	}
	t.Logf("auto long=%+v short=%+v", long[0], short[0])
	if long[0].Y != short[0].Y {
		t.Fatalf("auto-sized items should share a row: long=%+v short=%+v", long[0], short[0])
	}
	if long[0].W < 300 || short[0].W > 140 {
		t.Fatalf("auto-sized floor did not protect the long word: long=%+v short=%+v", long[0], short[0])
	}
	if gap := short[0].X - (long[0].X + long[0].W); gap < 20 || gap > 24 {
		t.Fatalf("auto-sized gap = %d, want about 22", gap)
	}
}

func TestWebFlexWrapUsesMinContentFloorWithGap(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.row{display:flex;flex-wrap:wrap;gap:12px;width:600px}` +
		`.item{flex:0 0 180px}.a{background:#cc0000}.b{background:#00cc00}.c{background:#0000cc}` +
		`</style><div class="row"><div class="item a">WWWWWWWWWWWWWWWW</div>` +
		`<div class="item b">WWWWWWWWWWWWWWWW</div>` +
		`<div class="item c">WWWWWWWWWWWWWWWW</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 1000)
	a := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	b := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0xcc, B: 0, A: 255})
	c := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	if len(a) != 1 || len(b) != 1 || len(c) != 1 {
		t.Fatalf("wrapped items = %d/%d/%d, want 1 each", len(a), len(b), len(c))
	}
	if a[0].Y != b[0].Y || c[0].Y <= b[0].Y {
		t.Fatalf("min-content floors should wrap 2 items then 1: a=%+v b=%+v c=%+v", a[0], b[0], c[0])
	}
	if abs32(a[0].W-319) > 4 || abs32(b[0].W-319) > 4 || abs32(c[0].W-319) > 4 {
		t.Fatalf("wrapped item widths = %d/%d/%d, want 319 each", a[0].W, b[0].W, c[0].W)
	}
	// The page fills the view (6px gutters), so the row starts at the left
	// edge: the background is drawn 4px left of the 6px content edge.
	if abs32(a[0].X-2) > 4 || abs32(b[0].X-331) > 4 || abs32(c[0].X-2) > 4 {
		t.Fatalf("wrapped item positions = %d/%d/%d, want 2/331/2", a[0].X, b[0].X, c[0].X)
	}
	if gap := b[0].X - (a[0].X + a[0].W); gap < 8 || gap > 12 {
		t.Fatalf("wrapped row gap = %d, want the scaled 12px gap (background edges subtract 8px)", gap)
	}
	if gap := c[0].Y - (a[0].Y + a[0].H); gap < 18 {
		t.Fatalf("wrapped row gap = %d, want at least the scaled 12px row gap", gap)
	}
	t.Logf("a=%+v b=%+v c=%+v rows=%d", a[0], b[0], c[0], len(layout.Rows))
}
