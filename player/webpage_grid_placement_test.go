package main

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestWebGridColumnSpanUsesMultipleTracks(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.grid{display:grid;grid-template-columns:repeat(3,100px);gap:10px}` +
		`.wide{grid-column:span 2;background:#cc0000}.a{background:#0000cc}.b{background:#00aa00}` +
		`</style><div class="grid"><div class="wide">Wide</div><div class="a">A</div>` +
		`<div class="b">B</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	wide := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	a := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	b := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0xaa, B: 0, A: 255})
	if len(wide) != 1 || len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected three grid cells, got %d/%d/%d", len(wide), len(a), len(b))
	}
	t.Logf("wide=%+v a=%+v b=%+v", wide[0], a[0], b[0])

	if wide[0].Y != a[0].Y {
		t.Fatalf("span and the following item should share a row: wide=%+v a=%+v", wide[0], a[0])
	}
	if b[0].Y <= wide[0].Y {
		t.Fatalf("the third item should start a new grid row: wide=%+v b=%+v", wide[0], b[0])
	}
	if wide[0].W <= a[0].W {
		t.Fatalf("a two-track span should be wider than one track: wide=%+v a=%+v", wide[0], a[0])
	}
	// The layout gap is 15px here; the painted background boxes overlap it
	// by their 8px edge, leaving a 7px visible gutter.
	if gap := a[0].X - (wide[0].X + wide[0].W); gap < 6 || gap > 8 {
		t.Fatalf("span-to-next-track painted gutter = %d, want about 7", gap)
	}
	if b[0].X != wide[0].X {
		t.Fatalf("the second row should restart at the grid start: wide=%+v b=%+v", wide[0], b[0])
	}
}

func TestWebGridColumnStartAndEndPlaceTracks(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.grid{display:grid;grid-template-columns:repeat(3,100px);gap:10px}` +
		`.a{background:#cc0000}.span{grid-column:2 / 4;background:#0000cc}` +
		`.full{grid-column:1 / -1;background:#00aa00}` +
		`</style><div class="grid"><div class="a">A</div><div class="span">B</div>` +
		`<div class="full">C</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	a := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	span := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	full := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0xaa, B: 0, A: 255})
	if len(a) != 1 || len(span) != 1 || len(full) != 1 {
		t.Fatalf("expected three grid cells, got %d/%d/%d", len(a), len(span), len(full))
	}
	t.Logf("a=%+v span=%+v full=%+v", a[0], span[0], full[0])

	if span[0].Y != a[0].Y {
		t.Fatalf("explicitly placed tracks should share a row: a=%+v span=%+v", a[0], span[0])
	}
	if span[0].X <= a[0].X {
		t.Fatalf("grid-column: 2 / 4 did not move the item to the second track: a=%+v span=%+v", a[0], span[0])
	}
	if span[0].W <= a[0].W {
		t.Fatalf("grid-column: 2 / 4 should occupy two tracks: a=%+v span=%+v", a[0], span[0])
	}
	if full[0].Y <= span[0].Y {
		t.Fatalf("a full-width item should start after the occupied row: span=%+v full=%+v", span[0], full[0])
	}
	if full[0].W <= span[0].W {
		t.Fatalf("grid-column: 1 / -1 should fill all three tracks: span=%+v full=%+v", span[0], full[0])
	}
	if full[0].X != a[0].X {
		t.Fatalf("the full-width row should start at the grid edge: a=%+v full=%+v", a[0], full[0])
	}
}

func TestWebGridColumnPlacementParsesAndSupports(t *testing.T) {
	cases := []struct {
		css      string
		start    int
		end      int
		span     int
		supports bool
	}{
		{"grid-column: span 2", 0, 0, 2, true},
		{"grid-column: 2 / 4", 2, 4, 0, true},
		{"grid-column: 1 / -1", 1, -1, 0, true},
		{"grid-column-start: 2;grid-column-end: 4", 2, 4, 0, true},
		{"grid-column: nope", 0, 0, 0, false},
	}
	for _, tc := range cases {
		st := webBaseStyle()
		decls, _ := parseWebDeclsSplit(tc.css)
		applyWebDecls(&st, decls)
		if st.GridColumnStart != tc.start || st.GridColumnEnd != tc.end || st.GridColumnSpan != tc.span {
			t.Errorf("%q parsed as start=%d end=%d span=%d, want %d/%d/%d",
				tc.css, st.GridColumnStart, st.GridColumnEnd, st.GridColumnSpan, tc.start, tc.end, tc.span)
		}
		firstDecl := strings.SplitN(tc.css, ";", 2)[0]
		parts := strings.SplitN(firstDecl, ":", 2)
		if len(parts) != 2 {
			t.Fatalf("test case %q has no declaration", tc.css)
		}
		if got := webSupportsApplies("(" + strings.TrimSpace(parts[0]) + ":" + parts[1] + ")"); got != tc.supports {
			t.Errorf("@supports %q = %v, want %v", firstDecl, got, tc.supports)
		}
	}
}
