package main

// Tests for the CSS properties the reader approximates. Each test states the
// approximation it is checking, so a future change to the trick (a real
// gradient fill, a bigger line-height cap) has to update the expectation here
// rather than silently drift.

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

func webCSSTestURL() string { return "https://example.com/reader" }

// wordSoup is enough text to wrap several lines at the widths used below.
func wordSoup(n int) string {
	return strings.Repeat("word ", n)
}

// firstRunX and lastRunEnd describe the text box a layout actually used.
func textExtent(layout *webLayout) (firstX, lastEnd int32) {
	if layout == nil {
		return 0, 0
	}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Font == nil || strings.TrimSpace(run.Text) == "" {
				continue
			}
			w, _, err := run.Font.SizeUTF8(run.Text)
			if err != nil {
				continue
			}
			if firstX == 0 || run.X < firstX {
				firstX = run.X
			}
			if end := run.X + int32(w); end > lastEnd {
				lastEnd = end
			}
		}
	}
	return firstX, lastEnd
}

// line-height turns into slot height: the row pitch of a line-height:2.4
// paragraph is larger than the default one, and the text still fits the panel.
func TestWebLineHeightSpacesTheRows(t *testing.T) {
	config := layoutTestConfig(t)
	elem := Element{Type: "textbrowser", Font: "small"}

	plain := parseWebPage(`<p>`+wordSoup(80)+`</p>`, "", webCSSTestURL())
	tall := parseWebPage(`<p style="line-height: 2.4">`+wordSoup(80)+`</p>`, "", webCSSTestURL())
	a := plain.layoutFor(config, elem, 600)
	b := tall.layoutFor(config, elem, 600)
	if a == nil || b == nil || len(a.Rows) < 3 || len(b.Rows) < 3 {
		t.Fatalf("layouts missing: %d / %d rows", len(a.Rows), len(b.Rows))
	}

	// A line claims extra row slots, so the *average* distance between two
	// text lines grows even though the grid pitch itself is unchanged.
	pitch := func(l *webLayout) float64 {
		var ys []int32
		for _, row := range l.Rows {
			if len(row.Runs) > 0 {
				ys = append(ys, row.Y)
			}
		}
		if len(ys) < 2 {
			return 0
		}
		return float64(ys[len(ys)-1]-ys[0]) / float64(len(ys)-1)
	}
	if pitch(b) <= pitch(a) {
		t.Errorf("line-height:2.4 pitch = %.1f, default = %.1f: leading was not applied", pitch(b), pitch(a))
	}
	if len(b.Rows) <= len(a.Rows) {
		t.Errorf("line-height:2.4 gave %d rows, default %d: rows did not grow", len(b.Rows), len(a.Rows))
	}
	if b.Height <= a.Height {
		t.Errorf("line-height:2.4 height = %d, default %d", b.Height, a.Height)
	}
	_, end := textExtent(b)
	if end > 600 {
		t.Errorf("leading pushed text past the panel: end=%d", end)
	}

	// A unitless number, a percentage and a length all resolve to px.
	for _, c := range []struct {
		value string
		want  float64
	}{
		{"1.5", 24}, {"150%", 24}, {"24px", 24},
	} {
		if got, ok := webLineHeightPx(c.value, 16); !ok || got != c.want {
			t.Errorf("line-height %q = %v (%t), want %v", c.value, got, ok, c.want)
		}
	}
	if _, ok := webLineHeightPx("normal", 16); ok {
		t.Error("line-height:normal must leave the grid in charge")
	}
	if px, ok := webLineHeightPx("5000px", 16); !ok || px > 96 {
		t.Errorf("an absurd line-height was not capped: %v (%t)", px, ok)
	}
}

// A linear gradient is a ramp, not a colour: the stops come back in paint
// order along the axis the reader can draw, and sampling the middle is the mix
// the author asked for.
func TestWebGradientParsesStopsAndAxis(t *testing.T) {
	// 90deg points to the right, so the ramp runs on the horizontal axis.
	g, ok := webParseGradient("linear-gradient(90deg, #000 0%, #fff 100%)")
	if !ok {
		t.Fatal("a two-stop angle gradient was not parsed")
	}
	if g.Vertical {
		t.Fatal("90deg was treated as a vertical ramp")
	}
	if len(g.Stops) != 2 || g.Stops[0].Pos != 0 || g.Stops[1].Pos != 1 {
		t.Fatalf("stops = %+v, want 0 and 1", g.Stops)
	}
	if mid := g.At(0.5); mid.R < 120 || mid.R > 136 {
		t.Fatalf("middle of black→white = %v, want ~128", mid)
	}

	// No direction means "to bottom".
	g, ok = webParseGradient("linear-gradient(red, green, blue)")
	if !ok || !g.Vertical {
		t.Fatalf("default direction: vertical=%v ok=%v", g.Vertical, ok)
	}
	if len(g.Stops) != 3 {
		t.Fatalf("three colour stops gave %d stops", len(g.Stops))
	}
	if g.Stops[1].Pos < 0.49 || g.Stops[1].Pos > 0.51 {
		t.Fatalf("an unpositioned middle stop sat at %v, want ~0.5", g.Stops[1].Pos)
	}
	// The tails are flat, which is what a browser does with a single stop at
	// the end: nothing interpolates past it. (The named colours are the
	// reader's own palette, so compare against the stops rather than literals.)
	if got, want := g.At(0), g.Stops[0].Color; got != want {
		t.Fatalf("the ramp did not start at its first stop: %v vs %v", got, want)
	}
	if got, want := g.At(1), g.Stops[len(g.Stops)-1].Color; got != want {
		t.Fatalf("the ramp did not end at its last stop: %v vs %v", got, want)
	}

	// "to top" mirrors the ramp: blue sits at the bottom, red at the top.
	g, ok = webParseGradient("linear-gradient(to top, blue, red)")
	if !ok || !g.Vertical {
		t.Fatalf("to top: vertical=%v ok=%v", g.Vertical, ok)
	}
	if c := g.At(0); c.R <= c.B {
		t.Fatalf("to top starts at %v, want the red end", c)
	}

	// Explicit positions survive, and the parts before them stay flat.
	g, ok = webParseGradient("linear-gradient(to right, red 20%, blue 80%)")
	if !ok || g.Vertical {
		t.Fatalf("to right: vertical=%v ok=%v", g.Vertical, ok)
	}
	if g.Stops[0].Pos != 0.2 || g.Stops[1].Pos != 0.8 {
		t.Fatalf("positions = %v / %v, want 0.2 / 0.8", g.Stops[0].Pos, g.Stops[1].Pos)
	}
	if c := g.At(0); c != g.Stops[0].Color || c.R <= c.B {
		t.Fatalf("before the first stop = %v, want the red stop", c)
	}

	// Angles pick the axis they point most along, and mirror the ramp when they
	// point up or left. 0deg is up, 180deg down, 135deg is a diagonal that still
	// runs downwards.
	for _, tc := range []struct {
		angle    string
		vertical bool
		atStart  bool // the author's first stop lands at the top/left end
	}{
		{"0deg", true, false},     // up: the first stop belongs at the bottom
		{"180deg", true, true},    // down
		{"135deg", true, true},    // down-right, vertical component wins
		{"90deg", false, true},    // right
		{"270deg", false, false},  // left
		{"0.25turn", false, true}, // a quarter turn is 90deg
	} {
		g, ok := webParseGradient("linear-gradient(" + tc.angle + ", #ff0000, #0000ff)")
		if !ok {
			t.Fatalf("%s: not parsed", tc.angle)
		}
		if g.Vertical != tc.vertical {
			t.Fatalf("%s: vertical=%v, want %v", tc.angle, g.Vertical, tc.vertical)
		}
		red := g.Stops[0].Color.R > g.Stops[0].Color.B
		if red != tc.atStart {
			t.Fatalf("%s: first stop is red=%v, want %v", tc.angle, red, tc.atStart)
		}
	}

	// A radial gradient is not a ramp: the reader has no circles to paint, so
	// it keeps the averaged fill.
	if _, ok := webParseGradient("radial-gradient(circle, #fff, #000)"); ok {
		t.Fatal("a radial gradient was treated as a ramp")
	}
	if _, ok := webParseGradient("url(bg.png)"); ok {
		t.Fatal("an image url was treated as a ramp")
	}
}

// The ramp reaches the rect the reader draws, with both stops intact, and the
// average colour is still stored for contrast decisions.
func TestWebGradientReachesTheDrawnRect(t *testing.T) {
	config := layoutTestConfig(t)
	doc := parseWebPage(
		`<div style="background-image: linear-gradient(#102040, #eef2ff)">`+wordSoup(40)+`</div>`,
		"", webCSSTestURL())
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
	if layout == nil {
		t.Fatal("no layout")
	}
	var graded *webRect
	for i := range layout.Rects {
		if layout.Rects[i].HasGradient {
			graded = &layout.Rects[i]
		}
	}
	if graded == nil {
		t.Fatal("the gradient block produced no ramp rect")
	}
	if len(graded.Gradient.Stops) != 2 || !graded.Gradient.Vertical {
		t.Fatalf("ramp = %+v, want two vertical stops", graded.Gradient)
	}
	if graded.Gradient.Stops[0].Color.R > 40 || graded.Gradient.Stops[1].Color.R < 200 {
		t.Fatalf("stops = %v / %v, want the dark and light ends", graded.Gradient.Stops[0].Color, graded.Gradient.Stops[1].Color)
	}
	// The contrast/fade colour is the average, as before.
	if graded.Color.R > 160 {
		t.Fatalf("average colour = %v, want a mid tone", graded.Color)
	}
}

// The paint path draws a ramp rather than one averaged fill: the top and the
// bottom of the block have to end up different colours on screen.
func TestWebGradientPaintsARamp(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := layoutTestConfig(t)
	doc := parseWebPage(
		`<body style="background: #ffffff"><div style="background-image: linear-gradient(#000000, #ffffff)">`+wordSoup(120)+`</div></body>`,
		"", webCSSTestURL())
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
	if layout == nil {
		t.Fatal("no layout")
	}
	var box webRect
	found := false
	for _, rect := range layout.Rects {
		if rect.HasGradient && rect.Rect.H > 60 {
			box, found = rect, true
			break
		}
	}
	if !found {
		t.Fatal("no tall gradient rect to sample")
	}

	frame := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	renderer.SetDrawColor(255, 0, 255, 255)
	renderer.Clear()
	drawWebDocument(renderer, config, doc, layout, sdl.Rect{X: 0, Y: 0, W: probeW, H: probeH}, 0)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}

	// Sample down the right edge of the block, past the wrapped text, so a
	// glyph cannot be mistaken for the ramp.
	x := box.Rect.X + box.Rect.W - 3
	top := frame.at(x, box.Rect.Y+2)
	bottom := frame.at(x, box.Rect.Y+box.Rect.H-3)
	if top.R > 60 || top.G > 60 || top.B > 60 {
		t.Fatalf("top of the ramp = %v, want near-black", top)
	}
	if bottom.R < 190 || bottom.G < 190 || bottom.B < 190 {
		t.Fatalf("bottom of the ramp = %v, want near-white", bottom)
	}
	mid := frame.at(x, box.Rect.Y+box.Rect.H/2)
	if mid.R < top.R+40 || mid.R > bottom.R-40 {
		t.Fatalf("middle of the ramp = %v, want a value between %v and %v", mid, top, bottom)
	}
}

// A gradient still reports its average colour, which is what text contrast and
// opacity use; a radial or unparseable value keeps that as its whole fill.
func TestWebGradientBackgroundIsAveraged(t *testing.T) {
	c, ok := webGradientColor("linear-gradient(90deg, rgb(0, 0, 0) 0%, rgb(255, 255, 255) 100%)")
	if !ok {
		t.Fatal("a two-stop gradient was not recognised")
	}
	if c.R < 120 || c.R > 136 || c.G < 120 || c.G > 136 || c.B < 120 || c.B > 136 {
		t.Errorf("average of black and white = %v, want ~128 grey", c)
	}

	if _, ok := webGradientColor("#123456"); ok {
		t.Error("a plain colour was treated as a gradient")
	}
	if _, ok := webGradientColor("linear-gradient(90deg, to right)"); ok {
		t.Error("a gradient without colour stops produced a colour")
	}

	doc := parseWebPage(`<div style="background-image: linear-gradient(#102040, #204080)">Header</div>`, "", webCSSTestURL())
	if len(doc.Blocks) == 0 {
		t.Fatal("the gradient block produced no text block")
	}
	b := doc.Blocks[0]
	if !b.HasBg {
		t.Fatal("the gradient block has no background")
	}
	if b.Background.B <= b.Background.R {
		t.Errorf("averaged background = %v, want a blue", b.Background)
	}

	// An explicit background-color still wins over the gradient.
	doc = parseWebPage(`<div style="background-color: #ff0000; background-image: linear-gradient(#000, #fff)">x</div>`, "", webCSSTestURL())
	if len(doc.Blocks) == 0 || doc.Blocks[0].Background.R < 200 {
		t.Errorf("background-color was overridden by the gradient: %v", doc.Blocks[0].Background)
	}
}

// border-radius reaches the rect that gets drawn, including the clamped
// "as round as possible" reading of a percentage radius.
func TestWebBorderRadiusReachesTheDrawnRect(t *testing.T) {
	config := layoutTestConfig(t)
	doc := parseWebPage(
		`<div style="border: 1px solid #334455; border-radius: 14px; padding: 8px">Card body</div>`,
		"", webCSSTestURL())
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
	if layout == nil {
		t.Fatal("no layout")
	}
	found := false
	for _, rect := range layout.Rects {
		if rect.Stroke && rect.Radius > 0 {
			found = true
			if corner := webRectCorner(rect, 3); corner <= 3 {
				t.Errorf("author radius collapsed to the default corner: %d", corner)
			}
		}
	}
	if !found {
		t.Fatalf("no stroked rect carried the radius: %+v", layout.Rects)
	}

	// Half the shorter side is the ceiling, so a pill stays inside its box.
	box := webRect{Rect: sdl.Rect{X: 0, Y: 0, W: 100, H: 20}, Radius: 999}
	if got := webRectCorner(box, 2); got != 10 {
		t.Errorf("clamped radius = %d, want 10", got)
	}
	if got := webRectCorner(webRect{Rect: box.Rect}, 2); got != 2 {
		t.Errorf("default corner = %d, want 2", got)
	}
}

// opacity is blended into the colour that gets painted: half-opacity white on
// the page's paper lands between the two.
func TestWebOpacityFadesTowardThePage(t *testing.T) {
	paper := sdl.Color{R: 255, G: 255, B: 255, A: 255}
	black := sdl.Color{R: 0, G: 0, B: 0, A: 255}
	faded := webFadeColor(black, paper, webStyle{HasOpacity: true, Opacity: 0.5})
	if faded.R < 120 || faded.R > 136 {
		t.Errorf("half-opacity black over white = %v, want ~128", faded)
	}
	if opaque := webFadeColor(black, paper, webStyle{}); opaque != black {
		t.Errorf("a style without opacity changed the colour: %v", opaque)
	}

	doc := parseWebPage(`<p style="color: #ffffff; opacity: 0.5">half</p>`, "", webCSSTestURL())
	if len(doc.Blocks) == 0 || len(doc.Blocks[0].Runs) == 0 {
		t.Fatal("the faded paragraph produced no runs")
	}
	style := doc.Blocks[0].Runs[0].Style
	if !style.HasOpacity || style.Opacity != 0.5 {
		t.Fatalf("opacity was not parsed: %+v", style)
	}
	got := webResolveColor(style, doc)
	if got.R >= 240 {
		t.Errorf("faded text = %v, want a colour between the page and white", got)
	}
	for _, c := range []struct {
		value string
		want  float64
	}{
		{"0.25", 0.25}, {"50%", 0.5}, {"2", 1}, {"-1", 0},
	} {
		if v, ok := webOpacityValue(c.value); !ok || v != c.want {
			t.Errorf("opacity %q = %v (%t), want %v", c.value, v, ok, c.want)
		}
	}
}

// list-style-type picks the marker: decimal, letters, roman numerals and the
// bullet glyphs, and `none` removes it — on the container, not just the item.
func TestWebListMarkersFollowListStyleType(t *testing.T) {
	cases := []struct {
		html string
		want string
	}{
		{`<ol><li>one</li></ol>`, "1."},
		{`<ol style="list-style-type: lower-alpha"><li>one</li></ol>`, "a."},
		{`<ol style="list-style-type: upper-alpha"><li>one</li></ol>`, "A."},
		{`<ol style="list-style-type: lower-roman"><li>one</li></ol>`, "i."},
		{`<ol style="list-style-type: upper-roman"><li>one</li></ol>`, "I."},
		{`<ul><li>one</li></ul>`, "•"},
		{`<ul style="list-style-type: circle"><li>one</li></ul>`, "◦"},
		{`<ul style="list-style-type: square"><li>one</li></ul>`, "▪"},
		{`<ul style="list-style: none"><li>one</li></ul>`, ""},
		{`<ul style="list-style: square inside"><li>one</li></ul>`, "▪"},
	}
	for _, c := range cases {
		doc := parseWebPage(c.html, "", webCSSTestURL())
		if len(doc.Blocks) == 0 {
			t.Fatalf("%s produced no block", c.html)
		}
		if got := doc.Blocks[0].Bullet; got != c.want {
			t.Errorf("%s marker = %q, want %q", c.html, got, c.want)
		}
	}

	// Later items keep counting: b, c on a list that started with a.
	doc := parseWebPage(`<ol style="list-style-type: lower-alpha"><li>a</li><li>b</li><li>c</li></ol>`, "", webCSSTestURL())
	var bullets []string
	for _, b := range doc.Blocks {
		if b.Bullet != "" {
			bullets = append(bullets, b.Bullet)
		}
	}
	if strings.Join(bullets, ",") != "a.,b.,c." {
		t.Errorf("ordered markers = %v, want a.,b.,c.", bullets)
	}
	if webRomanMarker(1994) != "MCMXCIV" || webRomanMarker(4000) != "4000" {
		t.Error("roman numerals are wrong")
	}
	if webAlphaMarker(27, false) != "aa" {
		t.Error("alpha markers do not continue past z")
	}
}

// margin-left and padding-right narrow the column: the first line starts
// further in and the last run ends further out from the right edge.
func TestWebLeftMarginAndRightGutterNarrowTheLine(t *testing.T) {
	config := layoutTestConfig(t)
	elem := Element{Type: "textbrowser", Font: "small"}

	plain := parseWebPage(`<p>`+wordSoup(90)+`</p>`, "", webCSSTestURL())
	inset := parseWebPage(`<p style="margin-left: 40px; padding-right: 40px">`+wordSoup(90)+`</p>`, "", webCSSTestURL())
	a := plain.layoutFor(config, elem, 700)
	b := inset.layoutFor(config, elem, 700)
	if a == nil || b == nil {
		t.Fatal("layouts missing")
	}

	aX, aEnd := textExtent(a)
	bX, bEnd := textExtent(b)
	if bX <= aX {
		t.Errorf("margin-left did not move the text in: %d vs %d", bX, aX)
	}
	if bEnd >= aEnd {
		t.Errorf("padding-right did not narrow the measure: end %d vs %d", bEnd, aEnd)
	}
	if len(b.Rows) <= len(a.Rows) {
		t.Errorf("narrower column wrapped to %d rows, wider one to %d", len(b.Rows), len(a.Rows))
	}
}

// The drawn card really is a card: the gradient's averaged fill covers the
// inside, and the rounded corner leaves the page paper showing.
func TestDrawWebDocumentRoundsAndFillsACard(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	doc := parseWebPage(
		`<div style="background-image: linear-gradient(#ff0000, #ff0000); border-radius: 40px; padding: 24px">card body text</div>`,
		"", webCSSTestURL())
	config := layoutTestConfig(t)
	withScreen(t, probeW, probeH)
	frame := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
	if layout == nil {
		t.Fatal("no layout")
	}
	var card *webRect
	for i := range layout.Rects {
		if !layout.Rects[i].Stroke && layout.Rects[i].Rect.W > 100 {
			card = &layout.Rects[i]
			break
		}
	}
	if card == nil {
		t.Fatal("the gradient block produced no fill rect")
	}

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	rect := sdl.Rect{X: 0, Y: 0, W: 620, H: 400}
	drawWebDocument(renderer, config, doc, layout, rect, 0)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}
	pixel := func(x, y int32) (r, g, b uint8) {
		i := (int(y)*int(probeW) + int(x)) * 4
		return frame.buf[i+2], frame.buf[i+1], frame.buf[i]
	}

	// Just inside the box, but outside a 40px corner: paper, not the fill.
	r, g, b := pixel(rect.X+card.Rect.X+2, rect.Y+card.Rect.Y+2)
	if r == 255 && g < 40 && b < 40 {
		t.Errorf("the rounded corner was filled: pixel = %d,%d,%d", r, g, b)
	}
	// Midway along the top edge, past the corner: the averaged gradient.
	r, g, b = pixel(rect.X+card.Rect.X+card.Rect.W/2, rect.Y+card.Rect.Y+1)
	if r < 200 || g > 60 || b > 60 {
		t.Errorf("card fill = %d,%d,%d, want the red the stops average to", r, g, b)
	}
}

// An <hr> is a rule the sheet styles: its colour and weight come from the
// border or background it declares, and only an unstyled one is the reader's
// grey hairline.
func TestWebHorizontalRuleKeepsItsStyling(t *testing.T) {
	config := layoutTestConfig(t)
	layoutFor := func(html string) *webLayout {
		t.Helper()
		doc := parseWebPage(html, "", webCSSTestURL())
		layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
		if layout == nil {
			t.Fatal("no layout")
		}
		return layout
	}

	layout := layoutFor(`<p>above</p><hr style="border: 4px solid #cc0000"><p>below</p>`)
	var rule *webRect
	for i := range layout.Rects {
		if r := layout.Rects[i]; !r.Stroke && r.Rect.W > 200 && r.Rect.H <= 12 {
			rule = &layout.Rects[i]
		}
	}
	if rule == nil {
		t.Fatalf("the styled rule produced no rect: %+v", layout.Rects)
	}
	if rule.Rect.H != 4 {
		t.Errorf("rule height = %d, want the authored 4px", rule.Rect.H)
	}
	if rule.Color.R < 150 || rule.Color.G > 90 {
		t.Errorf("rule colour = %v, want the authored red", rule.Color)
	}

	// A background-coloured rule is painted with that background.
	layout = layoutFor(`<p>above</p><hr style="background: #0000cc"><p>below</p>`)
	var blue *webRect
	for i := range layout.Rects {
		if r := layout.Rects[i]; !r.Stroke && r.Rect.W > 200 && r.Rect.H <= 12 {
			blue = &layout.Rects[i]
		}
	}
	if blue == nil || blue.Color.B < 150 || blue.Color.R > 90 {
		t.Fatalf("background rule = %+v, want the authored blue", blue)
	}

	// An unstyled rule is still the reader's own hairline.
	layout = layoutFor(`<p>above</p><hr><p>below</p>`)
	var plain *webRect
	for i := range layout.Rects {
		if r := layout.Rects[i]; !r.Stroke && r.Rect.W > 200 && r.Rect.H <= 12 {
			plain = &layout.Rects[i]
		}
	}
	if plain == nil || plain.Rect.H != 2 {
		t.Fatalf("unstyled rule = %+v, want the 2px hairline", plain)
	}
}

// The font shorthand is how a lot of sheets (and nearly every reset) set the
// page's type: `font: 700 18px/1.8 Georgia, serif` has to set the size, the
// weight and the leading at once. Its family is the one part the reader drops,
// because the app ships a single face.
func TestWebFontShorthandSetsSizeWeightAndLeading(t *testing.T) {
	doc := parseWebPage(`<p style="font: italic 700 18px/1.8 Georgia, serif">text</p>`, "", webCSSTestURL())
	if len(doc.Blocks) == 0 || len(doc.Blocks[0].Runs) == 0 {
		t.Fatal("the paragraph produced no runs")
	}
	st := doc.Blocks[0].Runs[0].Style
	if st.FontPx < 17.5 || st.FontPx > 18.5 {
		t.Errorf("font shorthand size = %v, want 18", st.FontPx)
	}
	if !st.Bold || !st.Italic {
		t.Errorf("font shorthand weight/style = bold:%v italic:%v, want both", st.Bold, st.Italic)
	}
	if st.LineHeight < 32 || st.LineHeight > 33 {
		t.Errorf("font shorthand line-height = %v, want 32.4", st.LineHeight)
	}

	// A bare number in the shorthand is a weight, never a size: 700 must not
	// become a 700px font.
	doc = parseWebPage(`<p style="font: 700 16px Arial">text</p>`, "", webCSSTestURL())
	st = doc.Blocks[0].Runs[0].Style
	if st.FontPx < 15.5 || st.FontPx > 16.5 {
		t.Errorf("font: 700 16px gave size %v, want 16", st.FontPx)
	}
	if !st.Bold {
		t.Error("font: 700 16px did not read the weight")
	}

	// 100..500 is not bold, and a normal reset clears it.
	doc = parseWebPage(`<p style="font: 400 14px Arial">text</p>`, "", webCSSTestURL())
	if doc.Blocks[0].Runs[0].Style.Bold {
		t.Error("font: 400 was treated as bold")
	}

	// Without a size the shorthand is not guessed at: a system-font keyword
	// must leave the paragraph exactly as an unstyled one.
	plain := parseWebPage(`<p>text</p>`, "", webCSSTestURL())
	doc = parseWebPage(`<p style="font: menu">text</p>`, "", webCSSTestURL())
	got := doc.Blocks[0].Runs[0].Style
	if want := plain.Blocks[0].Runs[0].Style.FontPx; got.FontPx != want {
		t.Errorf("font: menu set size %v, want the default %v", got.FontPx, want)
	}
}

// The reader is a screen, so a stylesheet's screen rules apply and its print
// rules do not. Print sheets hide the navigation with display:none and repaint
// the page for paper, which on screen reads as a page with its menu missing.
// @supports and @layer are this reader's modern CSS, so their rules count.
func TestWebStylesheetHonoursTheScreenMedium(t *testing.T) {
	cases := []struct {
		name     string
		css      string
		wantRed  bool
		wantGone bool
	}{
		{"print colour", `@media print { p { color: #ff0000 } }`, false, false},
		{"print width", `@media print and (min-width: 100px) { p { color: #ff0000 } }`, false, false},
		{"print hides nav", `@media print { p { display: none } }`, false, true},
		{"print and screen", `@media screen, print { p { color: #ff0000 } }`, true, false},
		{"screen width", `@media screen and (max-width: 600px) { p { color: #ff0000 } }`, true, false},
		{"feature query", `@media (prefers-color-scheme: dark) { p { color: #ff0000 } }`, true, false},
		{"not print", `@media not print { p { color: #ff0000 } }`, true, false},
		{"supports", `@supports (display: flex) { p { color: #ff0000 } }`, true, false},
		{"layer", `@layer base { p { color: #ff0000 } }`, true, false},
		{"media in layer", `@layer base { @media print { p { color: #ff0000 } } }`, false, false},
	}
	for _, tc := range cases {
		doc := parseWebPage(`<p>plain paragraph</p>`, tc.css, webCSSTestURL())
		if doc == nil {
			t.Fatalf("%s: no document", tc.name)
		}
		var run *webRun
		for i := range doc.Blocks {
			for j := range doc.Blocks[i].Runs {
				if strings.TrimSpace(doc.Blocks[i].Runs[j].Text) == "" {
					continue
				}
				run = &doc.Blocks[i].Runs[j]
			}
		}
		if run == nil {
			if tc.wantGone {
				continue
			}
			t.Fatalf("%s: the paragraph produced no text", tc.name)
		}
		red := run.Style.HasColor && run.Style.Color.R > 180 && run.Style.Color.G < 90 && run.Style.Color.B < 90
		if red != tc.wantRed {
			t.Errorf("%s: coloured = %v, want %v (%+v)", tc.name, red, tc.wantRed, run.Style.Color)
		}
	}
}

// Every border edge paints where the author put it, in the author's colour and
// at the author's width. A quote's rule used to be a fixed grey 3px bar (so a
// `border-left: 5px solid #c00` lost both its colour and its weight), and
// `border-right` set the left flag — the divider ended up on the wrong edge.
func TestWebBorderEdgesPaintTheirOwnSideColourAndWidth(t *testing.T) {
	config := layoutTestConfig(t)
	layoutFor := func(html string) *webLayout {
		t.Helper()
		doc := parseWebPage(html, "", webCSSTestURL())
		layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
		if layout == nil {
			t.Fatalf("no layout for %s", html)
		}
		return layout
	}

	// A quote's left bar: 5px wide, at the left edge, in the author's red.
	layout := layoutFor(`<blockquote style="border-left: 5px solid #cc0000">` + wordSoup(30) + `</blockquote>`)
	var left *webRect
	for i := range layout.Rects {
		if r := layout.Rects[i]; !r.Stroke && r.Rect.W == 5 && r.Rect.X < 40 {
			left = &layout.Rects[i]
		}
	}
	if left == nil {
		t.Fatalf("no 5px left bar in %+v", layout.Rects)
	}
	if left.Color.R < 150 || left.Color.G > 90 || left.Color.B > 90 {
		t.Fatalf("left bar colour = %v, want the author's red", left.Color)
	}

	// border-right draws on the right edge, not the left one.
	layout = layoutFor(`<div style="border-right: 4px solid #0000cc">` + wordSoup(30) + `</div>`)
	var right *webRect
	leftBars := 0
	for i := range layout.Rects {
		r := layout.Rects[i]
		if r.Stroke {
			continue
		}
		if r.Rect.W == 4 && r.Rect.X > 400 {
			right = &layout.Rects[i]
		}
		if r.Rect.W == 4 && r.Rect.X < 40 {
			leftBars++
		}
	}
	if right == nil {
		t.Fatalf("border-right produced no right-hand rule in %+v", layout.Rects)
	}
	if leftBars != 0 {
		t.Errorf("border-right also drew %d left bar(s)", leftBars)
	}
	if right.Color.B < 150 || right.Color.R > 90 {
		t.Errorf("right rule colour = %v, want the author's blue", right.Color)
	}

	// A full outline keeps the declared thickness rather than a fixed 2px.
	layout = layoutFor(`<div style="border: 4px solid #008800">` + wordSoup(30) + `</div>`)
	var box *webRect
	for i := range layout.Rects {
		if layout.Rects[i].Stroke {
			box = &layout.Rects[i]
		}
	}
	if box == nil || box.Thickness != 4 {
		t.Fatalf("outline = %+v, want a 4px stroke", box)
	}

	// A nav underline is a 3px band on the bottom edge.
	layout = layoutFor(`<nav style="border-bottom: 3px solid #aa00aa">` + wordSoup(30) + `</nav>`)
	var underline *webRect
	for i := range layout.Rects {
		if r := layout.Rects[i]; !r.Stroke && r.Rect.H == 3 && r.Rect.W > 200 {
			underline = &layout.Rects[i]
		}
	}
	if underline == nil {
		t.Fatalf("no 3px underline in %+v", layout.Rects)
	}
	if underline.Color.R < 120 || underline.Color.B < 120 {
		t.Errorf("underline colour = %v, want the author's magenta", underline.Color)
	}

	// A width given in a longhand is honoured too, and an uncoloured border
	// still falls back to the reader's hairline rather than disappearing.
	layout = layoutFor(`<div style="border-top-width: 6px; border-top-style: solid">x</div>`)
	var ruled *webRect
	for i := range layout.Rects {
		if r := layout.Rects[i]; !r.Stroke && r.Rect.H == 6 {
			ruled = &layout.Rects[i]
		}
	}
	if ruled == nil {
		t.Errorf("border-top-width: 6px produced no 6px rule in %+v", layout.Rects)
	}
}

// Vertical padding and the logical shorthands are spacing too: a card's
// `padding-top`, and the `margin-inline` / `padding-block` forms modern sheets
// are written in, must reach the same fields the layout reads.
func TestWebBlockAndLogicalSpacingReachTheLayout(t *testing.T) {
	doc := parseWebPage(`<div style="padding-top: 24px; padding-bottom: 18px; margin-inline: 10px 20px">x</div>`, "", webCSSTestURL())
	if len(doc.Blocks) == 0 {
		t.Fatal("no block")
	}
	b := doc.Blocks[0]
	if b.MarginTop < 24 || b.MarginBot < 18 {
		t.Errorf("vertical padding = %v/%v, want at least 24/18", b.MarginTop, b.MarginBot)
	}
	if b.Indent < 10 || b.Style.IndentRight < 20 {
		t.Errorf("margin-inline = %v/%v, want 10/20", b.Indent, b.Style.IndentRight)
	}

	doc = parseWebPage(`<div style="padding-block: 12px; margin-inline-start: 14px; margin-inline-end: 7px; padding-block-end: 9px">x</div>`, "", webCSSTestURL())
	b = doc.Blocks[0]
	if b.MarginTop < 12 || b.MarginBot < 12 {
		t.Errorf("padding-block = %v/%v, want 12/12", b.MarginTop, b.MarginBot)
	}
	if b.MarginBot < 12+9 {
		t.Errorf("padding-block-end added nothing: bottom = %v", b.MarginBot)
	}
	if b.Indent < 14 || b.Style.IndentRight < 7 {
		t.Errorf("logical longhands = %v/%v, want 14/7", b.Indent, b.Style.IndentRight)
	}
}

// The shorthands feed the same fields: padding and margin split into the four
// sides the layout reads.
func TestWebBoxShorthandsFillAllSides(t *testing.T) {
	doc := parseWebPage(`<div style="padding: 10px 12px 14px 16px; margin: 20px 22px 24px 26px">x</div>`, "", webCSSTestURL())
	if len(doc.Blocks) == 0 {
		t.Fatal("no block")
	}
	b := doc.Blocks[0]
	if b.Indent != 16+26 {
		t.Errorf("left inset = %v, want padding-left 16 + margin-left 26", b.Indent)
	}
	if b.Style.IndentRight != 12+22 {
		t.Errorf("right inset = %v, want padding-right 12 + margin-right 22", b.Style.IndentRight)
	}
	if b.MarginTop != 10+20 || b.MarginBot != 14+24 {
		t.Errorf("vertical margins = %v/%v, want 30/38", b.MarginTop, b.MarginBot)
	}
}
