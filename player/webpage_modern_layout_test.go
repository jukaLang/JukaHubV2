package main

import "testing"

func TestWebGridAutoFitUsesTheMeasuredWidth(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:20px}</style>` +
		`<div class="cards"><div class="card">A</div><div class="card">B</div>` +
		`<div class="card">C</div><div class="card">D</div></div>`
	doc := parseWebPage(source, "", "https://example.com/")
	for _, block := range doc.Blocks {
		if block.FlexGroup != 0 && block.GridMin != 160 {
			t.Fatalf("grid track minimum = %g, want 160", block.GridMin)
		}
	}

	wide := doc.computeLayout(&Config{}, Element{Font: "body"}, 1600)
	aRow, aX := webTestTextPosition(t, wide, "A")
	bRow, bX := webTestTextPosition(t, wide, "B")
	cRow, cX := webTestTextPosition(t, wide, "C")
	dRow, dX := webTestTextPosition(t, wide, "D")
	if aRow != bRow || bRow != cRow || dRow <= aRow || dX != aX || !(aX < bX && bX < cX) {
		t.Fatalf("auto-fit grid at 1600: A=(%d,%d) B=(%d,%d) C=(%d,%d) D=(%d,%d)",
			aRow, aX, bRow, bX, cRow, cX, dRow, dX)
	}

	narrow := doc.computeLayout(&Config{}, Element{Font: "body"}, 420)
	aRow, _ = webTestTextPosition(t, narrow, "A")
	bRow, _ = webTestTextPosition(t, narrow, "B")
	cRow, _ = webTestTextPosition(t, narrow, "C")
	dRow, _ = webTestTextPosition(t, narrow, "D")
	if !(aRow < bRow && bRow < cRow && cRow < dRow) {
		t.Fatalf("auto-fit grid at 420 should collapse to one column: A=%d B=%d C=%d D=%d", aRow, bRow, cRow, dRow)
	}
}

func TestWebLengthSupportsModernMathFunctions(t *testing.T) {
	cases := []struct {
		value string
		rel   float64
		want  float64
	}{
		{"min(100%, 600px)", 800, 600},
		{"max(1rem, 24px)", 16, 24},
		{"clamp(12px, 1rem, 20px)", 16, 16},
		{"clamp(24px, 1rem, 20px)", 16, 24},
	}
	for _, tc := range cases {
		if got := webLength(tc.value, tc.rel); got != tc.want {
			t.Errorf("webLength(%q, %g) = %g, want %g", tc.value, tc.rel, got, tc.want)
		}
	}
}

func TestWebOrderReordersFlexItems(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.row{display:flex;gap:10px}.first{order:2}.second{order:1}</style>` +
		`<div class="row"><div class="first">A</div><div class="second">B</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	aRow, aX := webTestTextPosition(t, layout, "A")
	bRow, bX := webTestTextPosition(t, layout, "B")
	if aRow != bRow || bX >= aX {
		t.Fatalf("order should put B before A on one row: A=(%d,%d) B=(%d,%d)", aRow, aX, bRow, bX)
	}
}

func TestWebPlaceItemsSetsCrossAxisAlignment(t *testing.T) {
	style := webBaseStyle()
	applyWebDecls(&style, map[string]string{
		"display":     "grid",
		"place-items": "center",
	})
	if style.AlignItems != webItemCenter {
		t.Fatalf("place-items: center set AlignItems = %d, want %d", style.AlignItems, webItemCenter)
	}
}

func TestWebImageAspectRatioUsesCSSWidth(t *testing.T) {
	style := webBaseStyle()
	style.Width = 300
	style.AspectRatio = 16.0 / 9.0
	w, h := webImageDimsFromBlock(webBlock{Image: true, Style: style}, nil, 1, 900)
	if w != 300 {
		t.Fatalf("CSS width produced %d px, want 300", w)
	}
	if h != 168 {
		t.Fatalf("16:9 image with 300px width produced height %d, want 168", h)
	}
}

func TestWebInsetShorthandSetsEveryEdge(t *testing.T) {
	style := webBaseStyle()
	applyWebDecls(&style, map[string]string{
		"position": "absolute",
		"inset":    "10px 20px 30px 40px",
	})
	if style.Position != webPositionAbsolute {
		t.Fatalf("position = %d, want absolute", style.Position)
	}
	if !style.HasTop || !style.HasRight || !style.HasBottom || !style.HasLeft {
		t.Fatalf("inset did not set every edge: %+v", style)
	}
	if style.Top != 10 || style.Right != 20 || style.Bottom != 30 || style.Left != 40 {
		t.Fatalf("inset = %g %g %g %g, want 10 20 30 40", style.Top, style.Right, style.Bottom, style.Left)
	}

	zero := webBaseStyle()
	applyWebDecls(&zero, map[string]string{"inset": "0"})
	if !zero.HasTop || !zero.HasRight || !zero.HasBottom || !zero.HasLeft {
		t.Fatal("inset: 0 must pin every edge, even though each value is zero")
	}
}
