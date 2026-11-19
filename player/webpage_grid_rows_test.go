package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func gridRowTestRect(t *testing.T, layout *webLayout, color sdl.Color) sdl.Rect {
	t.Helper()
	rects := webTestBackgroundsOf(t, layout, color)
	if len(rects) != 1 {
		t.Fatalf("expected one background for %+v, got %d", color, len(rects))
	}
	return rects[0]
}

func TestWebGridExplicitRowTracksAndEmptyTrailingRow(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.grid{display:grid;grid-template-columns:100px 100px;` +
		`grid-template-rows:40px 80px 50px;gap:10px}` +
		`.one{grid-row:1;background:#cc0000}.two{grid-row:2;background:#0000cc}` +
		`.after{background:#00aa00}</style>` +
		`<div class="grid"><div class="one">One</div><div class="two">Two</div></div>` +
		`<p class="after">After</p>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	one := gridRowTestRect(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	two := gridRowTestRect(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	after := gridRowTestRect(t, layout, sdl.Color{R: 0, G: 0xaa, B: 0, A: 255})
	t.Logf("one=%+v two=%+v after=%+v", one, two, after)

	if one.Y != 0 || one.H != 60 {
		t.Fatalf("first explicit row = %+v, want y=0 h=60 (40px at this scale)", one)
	}
	if two.Y != 75 || two.H != 120 {
		t.Fatalf("second explicit row = %+v, want y=75 h=120 (40px + 10px gap)", two)
	}
	if after.Y < 285 {
		t.Fatalf("the empty third row did not reserve its height: after=%+v, want y >= 285", after)
	}
}

func TestWebGridRowSpanAndBothAxisSpan(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.grid{display:grid;grid-template-columns:100px 100px;` +
		`grid-template-rows:40px 80px;gap:10px}` +
		`.span{grid-row:span 2;background:#cc0000}` +
		`.both{grid-column:span 2;grid-row:1 / 3;background:#0000cc}` +
		`</style><div class="grid"><div class="span">Span</div>` +
		`<div class="both">Both</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	span := gridRowTestRect(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	both := gridRowTestRect(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	t.Logf("span=%+v both=%+v", span, both)

	if both.Y != 0 || both.H != 195 {
		t.Fatalf("both-axis span = %+v, want y=0 h=195", both)
	}
	if both.W <= span.W {
		t.Fatalf("both-axis span should include both columns: span=%+v both=%+v", span, both)
	}
	// The row-pinned item claims the explicit rows first; the fully automatic
	// span starts in the next free implicit row instead of overlapping it.
	if span.Y < both.Y+both.H {
		t.Fatalf("auto row span overlaps the pinned item: span=%+v both=%+v", span, both)
	}
}

func TestWebGridRowStartEndAndAutoPlacementAvoidsPins(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.grid{display:grid;grid-template-columns:100px 100px;` +
		`grid-template-rows:40px 80px;gap:10px}` +
		`.a{background:#cc0000}.b{background:#0000cc}` +
		`.pin{grid-row-start:2;grid-row-end:3;grid-column:2;background:#00aa00}` +
		`.c{background:#aa00aa}</style>` +
		`<div class="grid"><div class="a">A</div><div class="b">B</div>` +
		`<div class="pin">Pin</div><div class="c">C</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	a := gridRowTestRect(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	b := gridRowTestRect(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	pin := gridRowTestRect(t, layout, sdl.Color{R: 0, G: 0xaa, B: 0, A: 255})
	c := gridRowTestRect(t, layout, sdl.Color{R: 0xaa, G: 0, B: 0xaa, A: 255})
	t.Logf("a=%+v b=%+v pin=%+v c=%+v", a, b, pin, c)

	if a.Y != 0 || b.Y != 0 {
		t.Fatalf("the first two auto items should fill row 1: a=%+v b=%+v", a, b)
	}
	if b.X <= a.X {
		t.Fatalf("the second auto item should advance to column 2: a=%+v b=%+v", a, b)
	}
	if pin.Y != 75 || pin.H != 120 {
		t.Fatalf("grid-row: 2 pin did not land on the second track: %+v", pin)
	}
	if c.Y != 75 {
		t.Fatalf("auto placement should use the free cell in row 2: c=%+v", c)
	}
	if c.X >= pin.X {
		t.Fatalf("auto placement overlapped the pinned column: c=%+v pin=%+v", c, pin)
	}
}

func TestWebGridRowPlacementParses(t *testing.T) {
	cases := []struct {
		css   string
		start int
		end   int
		span  int
	}{
		{"grid-row: span 2", 0, 0, 2},
		{"grid-row: 2", 2, 0, 0},
		{"grid-row: 1 / 3", 1, 3, 0},
		{"grid-row-start: 2;grid-row-end: 4", 2, 4, 0},
	}
	for _, tc := range cases {
		st := webBaseStyle()
		decls, _ := parseWebDeclsSplit(tc.css)
		applyWebDecls(&st, decls)
		if st.GridRowStart != tc.start || st.GridRowEnd != tc.end || st.GridRowSpan != tc.span {
			t.Errorf("%q parsed as start=%d end=%d span=%d, want %d/%d/%d",
				tc.css, st.GridRowStart, st.GridRowEnd, st.GridRowSpan, tc.start, tc.end, tc.span)
		}
	}
}
