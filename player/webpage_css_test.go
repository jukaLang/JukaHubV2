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

// A gradient becomes the average of its stops: a black-to-white one is mid
// grey, and the block carries it as its background.
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
